// Package turnevent owns the local lifecycle ledger for a session. The ledger
// is a projection/recovery artifact only and never contributes to model input.
package turnevent

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/eventwire"
	"reasonix/internal/fileutil"
	"reasonix/internal/store"
)

const (
	legacySchemaVersion = 1
	schemaVersion       = 2

	defaultCompactBytes  int64 = 8 << 20
	defaultCompactEvents       = 4096
	closeCompactBytes    int64 = 256 << 10
	terminalSummaryLimit       = 16
	replayMaxEvents            = 512
	replaySoftBytes      int64 = 2 << 20
)

var ErrTurnLedgerUnavailable = errors.New("turn event ledger unavailable")

var atomicWriteLedgerFile = fileutil.AtomicWriteFileStrict

// UnsupportedSchemaError is deliberately distinct from corruption. A newer
// Reasonix may own the file, so the current process must leave it untouched.
type UnsupportedSchemaError struct{ Version int }

func (e *UnsupportedSchemaError) Error() string {
	return fmt.Sprintf("unsupported turn event ledger schema %d", e.Version)
}

// Envelope is one durable runtime event. Dynamic routing fields stay local and
// are never injected into prompts or provider requests.
type Envelope struct {
	SchemaVersion      int              `json:"schemaVersion"`
	SessionID          string           `json:"sessionId"`
	TurnID             string           `json:"turnId"`
	Sequence           uint64           `json:"seq"`
	ItemID             string           `json:"itemId,omitempty"`
	AttemptID          string           `json:"attemptId,omitempty"`
	RuntimeEpoch       string           `json:"runtimeEpoch,omitempty"`
	SubmissionID       string           `json:"submissionId,omitempty"`
	Source             string           `json:"source,omitempty"`
	Kind               string           `json:"kind"`
	Status             event.TurnStatus `json:"status"`
	TranscriptRevision int64            `json:"transcriptRevision,omitempty"`
	TranscriptDigest   string           `json:"transcriptDigest,omitempty"`
	CreatedAt          int64            `json:"createdAt"`
	Event              eventwire.Event  `json:"event"`
}

// TerminalSummary is the bounded, content-free history kept by checkpoints.
type TerminalSummary struct {
	TurnID             string           `json:"turnId"`
	TerminalSequence   uint64           `json:"terminalSeq"`
	Status             event.TurnStatus `json:"status"`
	Outcome            string           `json:"outcome,omitempty"`
	RuntimeEpoch       string           `json:"runtimeEpoch,omitempty"`
	SubmissionID       string           `json:"submissionId,omitempty"`
	StartedAt          int64            `json:"startedAt,omitempty"`
	FinishedAt         int64            `json:"finishedAt,omitempty"`
	DurationMs         int64            `json:"durationMs,omitempty"`
	TranscriptRevision int64            `json:"transcriptRevision,omitempty"`
	TranscriptDigest   string           `json:"transcriptDigest,omitempty"`
}

// ReplayView is a bounded page plus the retained-history contract a frontend
// needs to distinguish an ordinary sequence gap from checkpoint compaction.
type ReplayView struct {
	Events             []Envelope `json:"events"`
	FloorSequence      uint64     `json:"floorSeq"`
	LatestSequence     uint64     `json:"latestSeq"`
	NextAfterSequence  uint64     `json:"nextAfterSeq"`
	HasMore            bool       `json:"hasMore"`
	ResetRequired      bool       `json:"resetRequired"`
	TranscriptRevision int64      `json:"transcriptRevision,omitempty"`
	TranscriptDigest   string     `json:"transcriptDigest,omitempty"`
	RuntimeEpoch       string     `json:"runtimeEpoch,omitempty"`
}

// PendingProjection is an unacknowledged terminal Turn whose full events must
// remain available until the Desktop display-only sidecar is rebuilt.
type PendingProjection struct {
	TurnID string
	Status event.TurnStatus
	Events []Envelope
}

type diskEventRecord struct {
	RecordType string `json:"recordType"`
	Envelope
}

type projectionAckRecord struct {
	SchemaVersion    int    `json:"schemaVersion"`
	RecordType       string `json:"recordType"`
	TurnID           string `json:"turnId"`
	TerminalSequence uint64 `json:"terminalSeq"`
	CreatedAt        int64  `json:"createdAt"`
}

type checkpointRecord struct {
	SchemaVersion              int               `json:"schemaVersion"`
	RecordType                 string            `json:"recordType"`
	SessionID                  string            `json:"sessionId"`
	CompactedThroughSequence   uint64            `json:"compactedThroughSeq"`
	ProjectionCommittedThrough uint64            `json:"projectionCommittedThroughSeq"`
	LastTurnID                 string            `json:"lastTurnId,omitempty"`
	LastStatus                 event.TurnStatus  `json:"lastStatus,omitempty"`
	TranscriptRevision         int64             `json:"transcriptRevision,omitempty"`
	TranscriptDigest           string            `json:"transcriptDigest,omitempty"`
	TerminalSummaries          []TerminalSummary `json:"terminalSummaries"`
}

type routingMetadata struct {
	runtimeEpoch string
	submissionID string
}

type transcriptSnapshot struct {
	revision int64
	digest   string
}

// MetricsSnapshot contains counters only; no event content, ids or paths leave
// the ledger through this surface.
type MetricsSnapshot struct {
	RawEvents             uint64
	StreamRecords         uint64
	BytesWritten          uint64
	ReplayEvents          uint64
	ReplayBytes           uint64
	ReplayResets          uint64
	Compactions           uint64
	CompactionFailures    uint64
	BytesBeforeCompact    uint64
	BytesAfterCompact     uint64
	TornTails             uint64
	WriteFailures         uint64
	ProjectionRetries     uint64
	OpenCount             uint64
	SyncCount             uint64
	CloseCount            uint64
	AppendLatencyBuckets  [5]uint64
	ReplayLatencyBuckets  [5]uint64
	CompactLatencyBuckets [5]uint64
	FileSizeBytes         int64
	UnconfirmedTurns      int
}

// Ledger serializes sequence allocation, file I/O, projection acknowledgement
// and checkpoint replacement for exactly one session actor lane.
type Ledger struct {
	mu        sync.Mutex
	path      string
	damaged   string
	sessionID string

	nextSeq      uint64
	turnStartSeq uint64
	turnStarted  int64
	active       string
	status       event.TurnStatus
	terminal     bool
	routing      routingMetadata
	nextRouting  routingMetadata
	transcript   transcriptSnapshot

	submissionTurns            map[string]string
	records                    []Envelope
	summaries                  []TerminalSummary
	projectionAcks             map[string]uint64
	compactedThrough           uint64
	projectionCommittedThrough uint64

	writer               *os.File
	writeVersion         int
	fileSize             int64
	poisoned             error
	requireProjectionAck bool

	compactBytes  int64
	compactEvents int
	metrics       MetricsSnapshot
}

type parsedLedger struct {
	records             []Envelope
	summaries           []TerminalSummary
	acks                map[string]uint64
	compactedThrough    uint64
	projectionCommitted uint64
	checkpoint          transcriptSnapshot
	fileSize            int64
	sawV1               bool
}

// Open loads the valid prefix, isolates a recognized torn tail, and converts
// an orphaned non-terminal turn into interrupted. Tools are never replayed.
func Open(sessionPath, sessionID string) (*Ledger, error) {
	l := &Ledger{
		path: store.SessionTurnEventLog(sessionPath), damaged: store.SessionTurnEventLogDamaged(sessionPath),
		sessionID: sessionID, nextSeq: 1, writeVersion: schemaVersion,
		submissionTurns: make(map[string]string), projectionAcks: make(map[string]uint64),
		compactBytes: defaultCompactBytes, compactEvents: defaultCompactEvents,
	}
	if l.path == "" {
		return l, nil
	}
	parsed, err := l.readAndRepairLocked()
	if err != nil {
		return nil, err
	}
	l.records = parsed.records
	l.summaries = append([]TerminalSummary(nil), parsed.summaries...)
	l.projectionAcks = parsed.acks
	l.compactedThrough = parsed.compactedThrough
	l.projectionCommittedThrough = parsed.projectionCommitted
	l.transcript = parsed.checkpoint
	l.fileSize = parsed.fileSize

	pendingTools := make(map[string]eventwire.Tool)
	pendingToolOrder := make([]string, 0)
	startedTools := make(map[string]bool)
	for _, rec := range l.records {
		if rec.Sequence >= l.nextSeq {
			l.nextSeq = rec.Sequence + 1
		}
		if rec.TurnID != "" {
			if rec.TurnID != l.active {
				clear(pendingTools)
				clear(startedTools)
				pendingToolOrder = pendingToolOrder[:0]
				l.turnStartSeq = rec.Sequence
				l.turnStarted = rec.CreatedAt
			}
			l.active = rec.TurnID
			l.status = rec.Status
			l.terminal = rec.Status.Terminal()
			l.routing = routingMetadata{runtimeEpoch: rec.RuntimeEpoch, submissionID: rec.SubmissionID}
			if rec.SubmissionID != "" {
				l.submissionTurns[rec.SubmissionID] = rec.TurnID
			}
			l.transcript = transcriptSnapshot{revision: rec.TranscriptRevision, digest: rec.TranscriptDigest}
		}
		if rec.Event.Tool != nil && rec.Event.Tool.ID != "" {
			switch rec.Kind {
			case "tool_dispatch":
				if _, exists := pendingTools[rec.Event.Tool.ID]; !exists {
					pendingToolOrder = append(pendingToolOrder, rec.Event.Tool.ID)
				}
				pendingTools[rec.Event.Tool.ID] = *rec.Event.Tool
			case "tool_started":
				startedTools[rec.Event.Tool.ID] = true
			case "tool_result":
				delete(pendingTools, rec.Event.Tool.ID)
			}
		}
	}
	if l.nextSeq <= l.compactedThrough {
		l.nextSeq = l.compactedThrough + 1
	}
	for _, summary := range l.summaries {
		if summary.SubmissionID != "" {
			l.submissionTurns[summary.SubmissionID] = summary.TurnID
		}
	}
	if parsed.sawV1 && l.active != "" && !l.terminal {
		l.writeVersion = legacySchemaVersion
	}
	if len(l.records) == 0 && l.compactedThrough == 0 && legacyTranscriptExists(sessionPath) {
		id, idErr := newTurnID()
		if idErr != nil {
			return nil, idErr
		}
		l.active, l.status, l.terminal = id, event.TurnQueued, false
		l.turnStartSeq, l.turnStarted = l.nextSeq, time.Now().UnixMilli()
		bootstrap := event.Event{Kind: event.TurnStatusChanged, TurnID: id, Status: event.TurnCompleted}
		if _, ok, appendErr := l.appendLocked(bootstrap, event.TurnCompleted); appendErr != nil {
			return nil, appendErr
		} else if !ok {
			return nil, fmt.Errorf("bootstrap legacy session %s: terminal append rejected", sessionID)
		}
	}
	if err := l.closeOrphanedTurnLocked(pendingToolOrder, pendingTools, startedTools); err != nil {
		return nil, err
	}
	return l, nil
}

func legacyTranscriptExists(sessionPath string) bool {
	if sessionPath == "" {
		return false
	}
	info, err := os.Stat(sessionPath)
	return err == nil && !info.IsDir() && info.Size() > 0
}

func (l *Ledger) Begin() (string, error) {
	if l == nil {
		return "", nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.poisoned != nil {
		return "", l.unavailableLocked()
	}
	if l.active != "" && !l.terminal {
		return "", fmt.Errorf("turn %s is still active", l.active)
	}
	id, err := newTurnID()
	if err != nil {
		return "", err
	}
	l.active, l.status, l.terminal = id, event.TurnQueued, false
	l.turnStartSeq, l.turnStarted = l.nextSeq, time.Now().UnixMilli()
	l.routing, l.nextRouting = l.nextRouting, routingMetadata{}
	if l.routing.submissionID != "" {
		l.submissionTurns[l.routing.submissionID] = id
	}
	l.transcript = transcriptSnapshot{}
	return id, nil
}

func (l *Ledger) SetRoutingMetadata(runtimeEpoch, submissionID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.nextRouting = routingMetadata{runtimeEpoch: runtimeEpoch, submissionID: submissionID}
	l.mu.Unlock()
}

func (l *Ledger) RequireProjectionAck(required bool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.requireProjectionAck = required
	l.mu.Unlock()
}

func (l *Ledger) ProjectionAckRequired() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.requireProjectionAck
}

func (l *Ledger) TurnIDForSubmission(submissionID string) string {
	if l == nil || submissionID == "" {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.submissionTurns[submissionID]
}

func (l *Ledger) SetTranscriptSnapshot(revision int64, digest string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.transcript = transcriptSnapshot{revision: revision, digest: digest}
	l.mu.Unlock()
}

// ObserveRawEvent counts provider stream pressure before the coalescer. It
// intentionally records no content or routing identity.
func (l *Ledger) ObserveRawEvent(e event.Event) {
	if l == nil || (e.Kind != event.Text && e.Kind != event.Reasoning) {
		return
	}
	l.mu.Lock()
	l.metrics.RawEvents++
	l.mu.Unlock()
}

// ObserveProjectionRetry counts display-sidecar retry pressure without
// retaining the Turn identity, transcript content or filesystem path.
func (l *Ledger) ObserveProjectionRetry() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.metrics.ProjectionRetries++
	l.mu.Unlock()
}

func (l *Ledger) ActiveTurnID() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.terminal {
		return ""
	}
	return l.active
}

func (l *Ledger) CurrentStatus() event.TurnStatus {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status
}

func (l *Ledger) ProjectionCursor() (latest, replayAfter uint64) {
	if l == nil {
		return 0, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	latest = l.latestLocked()
	replayAfter = latest
	if l.active != "" && !l.terminal && l.turnStartSeq > 0 {
		replayAfter = l.turnStartSeq - 1
	}
	return latest, replayAfter
}

func (l *Ledger) Append(e event.Event, status event.TurnStatus) (event.Event, bool, error) {
	if l == nil {
		return e, true, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.appendLocked(e, status)
}

func (l *Ledger) appendLocked(e event.Event, status event.TurnStatus) (event.Event, bool, error) {
	if l.poisoned != nil {
		return e, false, l.unavailableLocked()
	}
	if l.active == "" {
		return e, true, nil
	}
	if l.terminal {
		return e, false, nil
	}
	if status == "" {
		status = l.status
	}
	next, err := nextTurnStatus(l.status, status)
	if err != nil {
		return e, false, err
	}
	status = next
	e.TurnID, e.Sequence, e.Status = l.active, l.nextSeq, status
	if l.path == "" {
		l.nextSeq++
		l.status = status
		if status.Terminal() {
			l.terminal = true
		}
		return e, true, nil
	}

	w := eventwire.ToWire(e)
	attemptID := ""
	if e.Kind == event.StreamAttempt {
		attemptID = e.StreamAttempt.ID
	} else if e.Tool.AttemptID != "" {
		attemptID = e.Tool.AttemptID
	}
	kind, _ := eventwire.KindName(e.Kind)
	rec := Envelope{
		SchemaVersion: l.writeVersion, SessionID: l.sessionID, TurnID: e.TurnID,
		Sequence: e.Sequence, ItemID: e.ItemID, AttemptID: attemptID,
		RuntimeEpoch: l.routing.runtimeEpoch, SubmissionID: l.routing.submissionID,
		Source: e.Source,
		Kind:   kind, Status: status, TranscriptRevision: l.transcript.revision,
		TranscriptDigest: l.transcript.digest, CreatedAt: time.Now().UnixMilli(), Event: w,
	}
	var line []byte
	if l.writeVersion == legacySchemaVersion {
		line, err = json.Marshal(rec)
	} else {
		rec.SchemaVersion = schemaVersion
		line, err = json.Marshal(diskEventRecord{RecordType: "event", Envelope: rec})
	}
	if err != nil {
		return e, false, err
	}
	terminal := status.Terminal()
	if err := l.appendLineLocked(line, terminal); err != nil {
		return e, false, err
	}
	l.records = append(l.records, rec)
	if e.Kind == event.Text || e.Kind == event.Reasoning {
		l.metrics.StreamRecords++
	}
	l.nextSeq++
	l.status = status
	if terminal {
		l.terminal = true
		l.addSummaryLocked(rec, e.Outcome)
		if l.writeVersion == legacySchemaVersion {
			l.writeVersion = schemaVersion
		}
	}
	return e, true, nil
}

func (l *Ledger) appendLineLocked(line []byte, terminal bool) error {
	started := time.Now()
	defer func() { l.metrics.AppendLatencyBuckets[latencyBucket(time.Since(started))]++ }()
	if err := l.ensureWriterLocked(); err != nil {
		return l.poisonLocked(err)
	}
	payload := append(append([]byte(nil), line...), '\n')
	if _, err := l.writer.Write(payload); err != nil {
		return l.poisonLocked(err)
	}
	l.fileSize += int64(len(payload))
	l.metrics.BytesWritten += uint64(len(payload))
	if terminal {
		if err := l.writer.Sync(); err != nil {
			return l.poisonLocked(err)
		}
		l.metrics.SyncCount++
		if err := l.closeWriterLocked(); err != nil {
			return l.poisonLocked(err)
		}
	}
	return nil
}

func (l *Ledger) ensureWriterLocked() error {
	if l.path == "" || l.writer != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	l.writer = f
	l.metrics.OpenCount++
	return nil
}

func (l *Ledger) closeWriterLocked() error {
	if l.writer == nil {
		return nil
	}
	f := l.writer
	l.writer = nil
	err := f.Close()
	l.metrics.CloseCount++
	return err
}

func (l *Ledger) poisonLocked(err error) error {
	if err == nil {
		return nil
	}
	_ = l.closeWriterLocked()
	if l.poisoned == nil {
		l.poisoned = err
		l.metrics.WriteFailures++
	}
	return l.unavailableLocked()
}

func (l *Ledger) unavailableLocked() error {
	return fmt.Errorf("%w: %w", ErrTurnLedgerUnavailable, l.poisoned)
}

// AcknowledgeProjection records that the terminal display projection is
// durable (or that the consumer has no separate display store), then attempts
// bounded checkpoint compaction while the ledger is idle.
func (l *Ledger) AcknowledgeProjection(turnID string) error {
	if l == nil || turnID == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.path == "" {
		return nil
	}
	if l.poisoned != nil {
		return l.unavailableLocked()
	}
	seq := l.terminalSequenceLocked(turnID)
	if seq == 0 {
		return fmt.Errorf("turn %s has no durable terminal event", turnID)
	}
	if l.projectionAcks[turnID] >= seq || seq <= l.projectionCommittedThrough {
		// Retry a failed checkpoint after its acknowledgement became durable.
		// Retention work must not turn a committed projection into a storage error.
		_ = l.maybeCompactLocked(false)
		return nil
	}
	rec := projectionAckRecord{SchemaVersion: schemaVersion, RecordType: "projection_ack", TurnID: turnID, TerminalSequence: seq, CreatedAt: time.Now().UnixMilli()}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := l.appendLineLocked(line, false); err != nil {
		return err
	}
	l.projectionAcks[turnID] = seq
	if l.terminal {
		if err := l.closeWriterLocked(); err != nil {
			return l.poisonLocked(err)
		}
	}
	// The projection acknowledgement is the correctness boundary. Checkpoint
	// compaction is best-effort: on failure AtomicWriteFileStrict leaves the old
	// sidecar intact and the metrics surface records the retry signal.
	_ = l.maybeCompactLocked(false)
	return nil
}

func (l *Ledger) terminalSequenceLocked(turnID string) uint64 {
	for _, rec := range slices.Backward(l.records) {
		if rec.TurnID == turnID && rec.Status.Terminal() {
			return rec.Sequence
		}
	}
	for _, summary := range slices.Backward(l.summaries) {
		if summary.TurnID == turnID {
			return summary.TerminalSequence
		}
	}
	return 0
}

func (l *Ledger) addSummaryLocked(rec Envelope, outcome string) {
	started := l.turnStarted
	finished := rec.CreatedAt
	summary := TerminalSummary{
		TurnID: rec.TurnID, TerminalSequence: rec.Sequence, Status: rec.Status, Outcome: outcome,
		RuntimeEpoch: rec.RuntimeEpoch, SubmissionID: rec.SubmissionID,
		StartedAt: started, FinishedAt: finished, TranscriptRevision: rec.TranscriptRevision,
		TranscriptDigest: rec.TranscriptDigest,
	}
	if started > 0 && finished >= started {
		summary.DurationMs = finished - started
	}
	l.summaries = appendTerminalSummary(l.summaries, summary)
}

// Replay returns a bounded page without rereading the whole sidecar. Open owns
// validation/index construction; reconnects use the retained in-memory index.
func (l *Ledger) Replay(after uint64) (ReplayView, error) {
	started := time.Now()
	view := ReplayView{Events: []Envelope{}}
	if l == nil {
		return view, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.poisoned != nil {
		return view, l.unavailableLocked()
	}
	latest := l.latestLocked()
	floor := l.compactedThrough + 1
	if len(l.records) > 0 {
		floor = l.records[0].Sequence
	}
	view.FloorSequence = floor
	view.LatestSequence = latest
	view.ResetRequired = after < l.compactedThrough || after > latest
	if view.ResetRequired {
		l.metrics.ReplayResets++
	}
	view.TranscriptRevision = l.transcript.revision
	view.TranscriptDigest = l.transcript.digest
	view.RuntimeEpoch = l.routing.runtimeEpoch
	effective := after
	if effective < l.compactedThrough || effective > latest {
		effective = l.compactedThrough
	}
	view.NextAfterSequence = effective
	var pageBytes int64
	for _, rec := range l.records {
		if rec.Sequence <= effective {
			continue
		}
		encoded, _ := json.Marshal(rec)
		size := int64(len(encoded))
		if len(view.Events) >= replayMaxEvents || (len(view.Events) > 0 && pageBytes+size > replaySoftBytes) {
			break
		}
		view.Events = append(view.Events, rec)
		pageBytes += size
		view.NextAfterSequence = rec.Sequence
	}
	view.HasMore = view.NextAfterSequence < latest
	l.metrics.ReplayEvents += uint64(len(view.Events))
	l.metrics.ReplayBytes += uint64(pageBytes)
	l.metrics.ReplayLatencyBuckets[latencyBucket(time.Since(started))]++
	return view, nil
}

// EventsAfter is retained for non-Wails callers and compatibility tests.
func (l *Ledger) EventsAfter(after uint64) ([]Envelope, error) {
	if l == nil {
		return []Envelope{}, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.poisoned != nil {
		return nil, l.unavailableLocked()
	}
	out := make([]Envelope, 0)
	for _, rec := range l.records {
		if rec.Sequence > after {
			out = append(out, rec)
		}
	}
	return out, nil
}

// PendingProjections returns complete retained event groups for terminal Turns
// that do not yet have a durable display projection acknowledgement.
func (l *Ledger) PendingProjections() []PendingProjection {
	if l == nil {
		return []PendingProjection{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pendingProjectionsLocked()
}

func (l *Ledger) pendingProjectionsLocked() []PendingProjection {
	byTurn := make(map[string][]Envelope)
	order := make([]string, 0)
	seen := make(map[string]bool)
	for _, rec := range l.records {
		if rec.TurnID == "" {
			continue
		}
		if !seen[rec.TurnID] {
			seen[rec.TurnID] = true
			order = append(order, rec.TurnID)
		}
		byTurn[rec.TurnID] = append(byTurn[rec.TurnID], rec)
	}
	out := make([]PendingProjection, 0)
	for _, turnID := range order {
		records := byTurn[turnID]
		if len(records) == 0 {
			continue
		}
		terminal := records[len(records)-1]
		if !terminal.Status.Terminal() || terminal.Sequence <= l.projectionCommittedThrough || l.projectionAcks[turnID] >= terminal.Sequence {
			continue
		}
		out = append(out, PendingProjection{TurnID: turnID, Status: terminal.Status, Events: append([]Envelope(nil), records...)})
	}
	return out
}

func (l *Ledger) latestLocked() uint64 {
	if l.nextSeq == 0 {
		return 0
	}
	return l.nextSeq - 1
}

// Compact forces an idle eligible-prefix checkpoint.
func (l *Ledger) Compact() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.compactLocked(true)
}

func (l *Ledger) maybeCompactLocked(onClose bool) error {
	if l.active != "" && !l.terminal {
		return nil
	}
	if !onClose && l.fileSize < l.compactBytes && len(l.records) < l.compactEvents {
		return nil
	}
	if onClose && l.fileSize < closeCompactBytes {
		return nil
	}
	return l.compactLocked(false)
}

func (l *Ledger) compactLocked(force bool) error {
	started := time.Now()
	if l.path == "" || (l.active != "" && !l.terminal) {
		return nil
	}
	cutoff := l.compactedThrough
	for _, rec := range l.records {
		if !rec.Status.Terminal() {
			continue
		}
		if l.projectionAcks[rec.TurnID] < rec.Sequence && rec.Sequence > l.projectionCommittedThrough {
			break
		}
		cutoff = rec.Sequence
	}
	if cutoff <= l.compactedThrough {
		return nil
	}
	if !force && l.fileSize < l.compactBytes && len(l.records) < l.compactEvents && l.fileSize < closeCompactBytes {
		return nil
	}
	if err := l.closeWriterLocked(); err != nil {
		return l.poisonLocked(err)
	}
	before := l.fileSize
	last := TerminalSummary{}
	for _, summary := range l.summaries {
		if summary.TerminalSequence <= cutoff && summary.TerminalSequence >= last.TerminalSequence {
			last = summary
		}
	}
	checkpoint := checkpointRecord{
		SchemaVersion: schemaVersion, RecordType: "checkpoint", SessionID: l.sessionID,
		CompactedThroughSequence: cutoff, ProjectionCommittedThrough: cutoff,
		LastTurnID: last.TurnID, LastStatus: last.Status,
		TranscriptRevision: last.TranscriptRevision, TranscriptDigest: last.TranscriptDigest,
		TerminalSummaries: append([]TerminalSummary(nil), l.summaries...),
	}
	if checkpoint.TerminalSummaries == nil {
		checkpoint.TerminalSummaries = []TerminalSummary{}
	}
	line, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	data := append(append([]byte(nil), line...), '\n')
	retained := make([]Envelope, 0)
	for _, rec := range l.records {
		if rec.Sequence <= cutoff {
			continue
		}
		rec.SchemaVersion = schemaVersion
		line, err = json.Marshal(diskEventRecord{RecordType: "event", Envelope: rec})
		if err != nil {
			return err
		}
		data = append(data, line...)
		data = append(data, '\n')
		retained = append(retained, rec)
	}
	for turnID, seq := range l.projectionAcks {
		if seq <= cutoff {
			continue
		}
		line, err = json.Marshal(projectionAckRecord{SchemaVersion: schemaVersion, RecordType: "projection_ack", TurnID: turnID, TerminalSequence: seq, CreatedAt: time.Now().UnixMilli()})
		if err != nil {
			return err
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := atomicWriteLedgerFile(l.path, data, 0o600); err != nil {
		l.metrics.CompactionFailures++
		l.metrics.CompactLatencyBuckets[latencyBucket(time.Since(started))]++
		return err
	}
	l.records = retained
	l.compactedThrough = cutoff
	l.projectionCommittedThrough = cutoff
	l.fileSize = int64(len(data))
	l.writeVersion = schemaVersion
	for turnID, seq := range l.projectionAcks {
		if seq <= cutoff {
			delete(l.projectionAcks, turnID)
		}
	}
	l.metrics.Compactions++
	l.metrics.BytesBeforeCompact += uint64(before)
	l.metrics.BytesAfterCompact += uint64(len(data))
	l.metrics.CompactLatencyBuckets[latencyBucket(time.Since(started))]++
	return nil
}

// Close releases the active descriptor and opportunistically checkpoints an
// idle ledger. It never manufactures a terminal event for an active Turn.
func (l *Ledger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.closeWriterLocked(); err != nil {
		return l.poisonLocked(err)
	}
	return l.maybeCompactLocked(true)
}

func (l *Ledger) MetricsSnapshot() MetricsSnapshot {
	if l == nil {
		return MetricsSnapshot{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.metrics
	out.FileSizeBytes = l.fileSize
	out.UnconfirmedTurns = len(l.pendingProjectionsLocked())
	return out
}

func (l *Ledger) DrainMetrics() MetricsSnapshot {
	if l == nil {
		return MetricsSnapshot{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.metrics
	out.FileSizeBytes = l.fileSize
	out.UnconfirmedTurns = len(l.pendingProjectionsLocked())
	l.metrics = MetricsSnapshot{}
	return out
}

func latencyBucket(elapsed time.Duration) int {
	switch {
	case elapsed < time.Millisecond:
		return 0
	case elapsed < 5*time.Millisecond:
		return 1
	case elapsed < 20*time.Millisecond:
		return 2
	case elapsed < 100*time.Millisecond:
		return 3
	default:
		return 4
	}
}

func (l *Ledger) readAndRepairLocked() (parsedLedger, error) {
	result := parsedLedger{records: []Envelope{}, summaries: []TerminalSummary{}, acks: make(map[string]uint64)}
	data, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.fileSize = int64(len(data))
	validBytes := 0
	expectedSeq := uint64(1)
	seenRecord := false
	for validBytes < len(data) {
		rest := data[validBytes:]
		newline := bytes.IndexByte(rest, '\n')
		if newline < 0 {
			break
		}
		lineEnd := validBytes + newline
		line := bytes.TrimSpace(data[validBytes:lineEnd])
		if len(line) == 0 {
			validBytes = lineEnd + 1
			continue
		}
		var header struct {
			SchemaVersion int    `json:"schemaVersion"`
			RecordType    string `json:"recordType"`
		}
		if err := json.Unmarshal(line, &header); err != nil {
			break
		}
		if header.SchemaVersion > schemaVersion {
			return result, &UnsupportedSchemaError{Version: header.SchemaVersion}
		}
		if header.SchemaVersion <= 0 {
			goto damaged
		}
		switch header.SchemaVersion {
		case legacySchemaVersion:
			var rec Envelope
			if err := json.Unmarshal(line, &rec); err != nil || rec.Sequence != expectedSeq {
				goto damaged
			}
			result.sawV1 = true
			result.records = append(result.records, rec)
			expectedSeq++
		case schemaVersion:
			switch header.RecordType {
			case "checkpoint":
				if seenRecord {
					goto damaged
				}
				var checkpoint checkpointRecord
				if err := json.Unmarshal(line, &checkpoint); err != nil {
					goto damaged
				}
				result.compactedThrough = checkpoint.CompactedThroughSequence
				result.projectionCommitted = checkpoint.ProjectionCommittedThrough
				result.checkpoint = transcriptSnapshot{revision: checkpoint.TranscriptRevision, digest: checkpoint.TranscriptDigest}
				result.summaries = append(result.summaries, checkpoint.TerminalSummaries...)
				expectedSeq = checkpoint.CompactedThroughSequence + 1
			case "event":
				var rec diskEventRecord
				if err := json.Unmarshal(line, &rec); err != nil || rec.Sequence != expectedSeq {
					goto damaged
				}
				result.records = append(result.records, rec.Envelope)
				expectedSeq++
			case "projection_ack":
				var ack projectionAckRecord
				if err := json.Unmarshal(line, &ack); err != nil || ack.TurnID == "" || ack.TerminalSequence == 0 {
					goto damaged
				}
				result.acks[ack.TurnID] = ack.TerminalSequence
			default:
				return result, fmt.Errorf("unsupported turn event record type %q", header.RecordType)
			}
		}
		seenRecord = true
		validBytes = lineEnd + 1
	}

damaged:
	if validBytes < len(data) {
		if err := os.WriteFile(l.damaged, data[validBytes:], 0o600); err != nil {
			return result, err
		}
		if err := os.Truncate(l.path, int64(validBytes)); err != nil {
			return result, err
		}
		result.fileSize = int64(validBytes)
		l.metrics.TornTails++
	}
	startedByTurn := make(map[string]int64)
	for _, rec := range result.records {
		if rec.TurnID != "" {
			if _, ok := startedByTurn[rec.TurnID]; !ok {
				startedByTurn[rec.TurnID] = rec.CreatedAt
			}
		}
		if !rec.Status.Terminal() {
			continue
		}
		started := startedByTurn[rec.TurnID]
		summary := TerminalSummary{
			TurnID: rec.TurnID, TerminalSequence: rec.Sequence, Status: rec.Status,
			Outcome:      rec.Event.Outcome,
			RuntimeEpoch: rec.RuntimeEpoch, SubmissionID: rec.SubmissionID,
			StartedAt: started, FinishedAt: rec.CreatedAt, TranscriptRevision: rec.TranscriptRevision,
			TranscriptDigest: rec.TranscriptDigest,
		}
		if started > 0 && rec.CreatedAt >= started {
			summary.DurationMs = rec.CreatedAt - started
		}
		result.summaries = appendTerminalSummary(result.summaries, summary)
	}
	return result, nil
}

func appendTerminalSummary(in []TerminalSummary, summary TerminalSummary) []TerminalSummary {
	for i := range in {
		if in[i].TurnID == summary.TurnID {
			in[i] = summary
			return in
		}
	}
	in = append(in, summary)
	if len(in) > terminalSummaryLimit {
		in = append([]TerminalSummary(nil), in[len(in)-terminalSummaryLimit:]...)
	}
	return in
}

func nextTurnStatus(current, requested event.TurnStatus) (event.TurnStatus, error) {
	if current == "" || current == requested {
		return requested, nil
	}
	if current.Terminal() {
		return requested, fmt.Errorf("turn is already terminal (%s)", current)
	}
	if current == event.TurnCancelling && !requested.Terminal() {
		return event.TurnCancelling, nil
	}
	valid := false
	switch current {
	case event.TurnQueued:
		valid = requested == event.TurnInProgress || requested == event.TurnWaitingUser || requested == event.TurnCancelling || requested.Terminal()
	case event.TurnInProgress:
		valid = requested == event.TurnWaitingUser || requested == event.TurnCancelling || requested.Terminal()
	case event.TurnWaitingUser:
		valid = requested == event.TurnInProgress || requested == event.TurnCancelling || requested.Terminal()
	case event.TurnCancelling:
		valid = requested.Terminal()
	}
	if !valid {
		return requested, fmt.Errorf("invalid turn status transition %s -> %s", current, requested)
	}
	return requested, nil
}

func newTurnID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "turn_" + hex.EncodeToString(raw[:]), nil
}
