// Package jobs is the session-scoped background-job registry behind the agent's
// background tools (bash run_in_background, task run_in_background) and the
// bash_output / kill_shell / wait tools. A Manager owns a context whose lifetime
// is the session, NOT a single turn — so a job started in one turn keeps running
// across turns and is cancelled only when the controller closes (or kill_shell is
// called). Tools reach the Manager through the call context (WithManager /
// FromContext), the same injection pattern the `ask` tool uses for the asker.
//
// The Manager emits a user-visible Notice when a job starts and finishes, and
// accumulates a one-line completion summary that the controller drains into the
// next turn (DrainCompletedNote) so the model itself learns of completions.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/nilutil"
)

var renamePath = os.Rename
var repairArtifactMeta = writeMeta

var (
	managerOwnerSeq   atomic.Uint64
	liveManagerOwners = struct {
		sync.RWMutex
		ids map[string]struct{}
	}{ids: map[string]struct{}{}}
)

// Status is a job's lifecycle state.
type Status string

const (
	Running     Status = "running"
	Done        Status = "done"
	Failed      Status = "failed"
	Killed      Status = "killed"
	Interrupted Status = "interrupted"
)

// DefaultTeardownGrace bounds Close and destroy waits for non-cooperative jobs.
const DefaultTeardownGrace = 15 * time.Second

// View is a read-only snapshot of a job for the status bar.
type View struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Status    string `json:"status"`
	StartedAt int64  `json:"startedAt"` // unix milliseconds
}

// Result is one job's terminal (or current) state returned by Wait.
type Result struct {
	ID     string
	Kind   string
	Label  string
	Status Status
	Output string // the terminal result text, or the streamed buffer when no result was set
}

// TeardownJob identifies a job that is still unwinding after teardown waited.
type TeardownJob struct {
	ID     string
	Kind   string
	Label  string
	Waited time.Duration
}

// TeardownResult reports jobs that did not unwind within the teardown grace.
type TeardownResult struct {
	TimedOut []TeardownJob
}

// HasTimedOut reports whether teardown returned before every job had unwound.
func (r TeardownResult) HasTimedOut() bool { return len(r.TimedOut) > 0 }

type teardownTarget struct {
	info TeardownJob
	done <-chan struct{}
}

// SessionTeardown is the destroy handle for a session's owned background jobs.
type SessionTeardown struct {
	SessionID string
	targets   []teardownTarget
}

// Async reports whether the handle has jobs to wait on.
func (h SessionTeardown) Async() bool { return len(h.targets) > 0 }

// DoneChannels returns each target's completion channel for legacy callers.
func (h SessionTeardown) DoneChannels() []<-chan struct{} {
	out := make([]<-chan struct{}, 0, len(h.targets))
	for _, target := range h.targets {
		out = append(out, target.done)
	}
	return out
}

// Job is one background job. The mutex guards the streaming buffer and the
// terminal fields; the run goroutine writes them, readers (Output/Wait/snapshots)
// take the same lock.
type Job struct {
	ID        string
	Kind      string // "bash" | "task"
	Label     string
	SessionID string

	mu         sync.Mutex
	tail       []byte
	readOffset int64
	status     Status
	clock      jobClock
	outcome    jobOutcome
	cancel     context.CancelFunc
	done       chan struct{}

	artifactPath     string
	artifactMetaPath string
	artifactStatus   Status // last metadata phase; may precede published terminal status
	artifactFile     *os.File
	artifactComplete bool
	artifactErr      string
	tombstone        bool

	evidence          evidence.ChildEvidenceSummary
	evidenceCommitted bool
}

// Manager is the session's background-job table. It is safe for concurrent use.
type Manager struct {
	runtimeObservers runtimeObservers
	sink             event.Sink
	root             context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	onJobStart       func(done <-chan struct{})
	ownerID          string
	ownerDone        sync.Once
	// sessionOwnershipProbe authorizes destructive repair of persisted running
	// artifacts. A nil probe is conservative: an observer that cannot prove it
	// owns the transcript must never publish an interrupted tombstone.
	sessionOwnershipProbe func(path string) bool

	mu           sync.Mutex
	seq          int
	jobs         map[string]*Job
	order        []string
	completed    []completion // finished-job summaries awaiting drain into the next turn
	active       string
	destroying   map[string]bool
	artifactDirs map[string]string
	loaded       map[string]bool
	tempRoot     string
	reservations map[string]int

	stalledWarning time.Duration
	teardownGrace  time.Duration

	taskRecorder TaskRecorder // optional task-monitoring lifecycle hook
}

type completion struct {
	sessionID string
	text      string
}

// Option configures a Manager.
type Option func(*Manager)

// TaskRecorder observes background-job lifecycle for task monitoring. The
// store-backed write side lives outside jobs (typically internal/taskmonitor);
// jobs only calls the hooks. RecordStart runs on the caller's goroutine,
// RecordDone on the job's own goroutine — implementations must be safe for
// concurrent use and must not block or fail the job pipeline (best-effort).
type TaskRecorder interface {
	RecordStart(id, kind, label string)
	RecordDone(id string, st Status, err error)
}

// WithStalledWarningAfter enables one stalled warning per job after d without
// job-owned visible output. A non-positive duration disables stalled warnings.
func WithStalledWarningAfter(d time.Duration) Option {
	return func(m *Manager) {
		if d > 0 {
			m.stalledWarning = d
		}
	}
}

// WithTeardownGrace overrides the Close/destroy grace window. Tests can set a
// short value; production uses DefaultTeardownGrace.
func WithTeardownGrace(d time.Duration) Option {
	return func(m *Manager) {
		if d >= 0 {
			m.teardownGrace = d
		}
	}
}

// WithJobStartObserver observes every registered background job before its
// goroutine starts. Delivery uses this to retain a workspace writer lease over
// the job's opening writes. The callback must return quickly.
func WithJobStartObserver(observer func(done <-chan struct{})) Option {
	return func(m *Manager) { m.onJobStart = observer }
}

// WithSessionOwnershipProbe supplies the runtime ownership check used when
// loading persisted Running artifacts. The probe must return true only when the
// current runtime owns the session transcript for writing.
func WithSessionOwnershipProbe(probe func(path string) bool) Option {
	return func(m *Manager) { m.sessionOwnershipProbe = probe }
}

// WithTaskRecorder installs an optional background-job lifecycle recorder for
// task monitoring. A nil recorder disables recording.
func WithTaskRecorder(r TaskRecorder) Option {
	return func(m *Manager) { m.taskRecorder = r }
}

// SetTaskRecorder installs (or clears, with nil) the lifecycle recorder after
// construction. Controllers that assemble their job manager before the
// recorder's dependencies (workspace root, session id) are known use this.
func (m *Manager) SetTaskRecorder(r TaskRecorder) { m.taskRecorder = r }

// TeardownGrace reports the manager's configured close/destroy wait window.
func (m *Manager) TeardownGrace() time.Duration { return m.teardownGrace }

// NewManager returns a Manager whose jobs run under a fresh session-scoped
// context (cancelled by Close). sink receives job-lifecycle notices; pass the
// session's synchronized sink (event.Sync) since jobs emit from goroutines.
func NewManager(sink event.Sink, opts ...Option) *Manager {
	if nilutil.IsNil(sink) {
		sink = event.Discard
	}
	root, cancel := context.WithCancel(context.Background())
	tempRoot, _ := os.MkdirTemp("", "reasonix-jobs-*")
	m := &Manager{
		sink:          sink,
		root:          root,
		cancel:        cancel,
		jobs:          map[string]*Job{},
		destroying:    map[string]bool{},
		artifactDirs:  map[string]string{},
		reservations:  map[string]int{},
		loaded:        map[string]bool{},
		tempRoot:      tempRoot,
		teardownGrace: DefaultTeardownGrace,
		ownerID:       newManagerOwnerID(),
	}
	registerManagerOwner(m.ownerID)
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	return m
}

func newManagerOwnerID() string {
	var token [16]byte
	if _, err := rand.Read(token[:]); err == nil {
		return hex.EncodeToString(token[:])
	}
	return fmt.Sprintf("%d-%d-%d", os.Getpid(), time.Now().UnixNano(), managerOwnerSeq.Add(1))
}

func registerManagerOwner(ownerID string) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return
	}
	liveManagerOwners.Lock()
	liveManagerOwners.ids[ownerID] = struct{}{}
	liveManagerOwners.Unlock()
}

func managerOwnerIsLive(ownerID string) bool {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return false
	}
	liveManagerOwners.RLock()
	_, ok := liveManagerOwners.ids[ownerID]
	liveManagerOwners.RUnlock()
	return ok
}

func (m *Manager) releaseOwner() {
	if m == nil {
		return
	}
	m.ownerDone.Do(func() {
		liveManagerOwners.Lock()
		delete(liveManagerOwners.ids, m.ownerID)
		liveManagerOwners.Unlock()
	})
}

// jobWriter appends a job's streamed output under its lock so a concurrent
// Output read never races the producing goroutine.
type jobWriter struct{ j *Job }

func (w jobWriter) Write(p []byte) (int, error) {
	w.j.mu.Lock()
	defer w.j.mu.Unlock()
	w.j.clock.activityAt = nowMs()
	w.j.tail = appendTail(w.j.tail, p, defaultTailBytes)
	if w.j.artifactFile != nil {
		if _, err := w.j.artifactFile.Write(p); err != nil {
			w.j.artifactErr = err.Error()
		}
	}
	return len(p), nil
}

// Start launches run on a goroutine under the manager's session context and
// returns the job immediately. run streams output to the writer and returns the
// terminal result text (a task's final answer; a bash job streams everything to
// the buffer and returns ""). The job is marked killed when its context was
// cancelled, failed on any other error, else done.
func (m *Manager) Start(kind, label string, run func(ctx context.Context, out io.Writer) (string, error)) *Job {
	return m.StartForSession("", kind, label, run)
}

// validatePathSegment rejects values that would let parentSession or kind
// escape the temp-root fallback built by artifactDirLocked. Persistent artifact
// directories bound by SetActiveSessionPath are trusted store paths and are
// intentionally outside that temp root. The check is intentionally conservative:
// it forbids any path-separator character (forward slash, backslash), NUL, and
// any control character. Empty parentSession is allowed (the unscoped default);
// kind must be non-empty.
//
// See #6932. Before this check existed, a malicious or malformed parentSession
// such as "../../etc" combined with filepath.Join(tempRoot, parentSession, id)
// resolved to a directory outside the manager's temp root, allowing the
// subsequent os.MkdirAll + os.OpenFile to create files at locations controlled
// by the caller (subject to the running process's filesystem permissions).
func validatePathSegment(name, field string) error {
	if field == "kind" && name == "" {
		return fmt.Errorf("jobs: %s must not be empty", field)
	}
	for i, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			return fmt.Errorf("jobs: %s contains control character 0x%02x at index %d", field, r, i)
		case r == '/' || r == '\\':
			return fmt.Errorf("jobs: %s contains path separator %q at index %d", field, r, i)
		}
	}
	if name == "." || name == ".." {
		return fmt.Errorf("jobs: %s is reserved (%q)", field, name)
	}
	return nil
}

// startInvalid registers a job that failed validation BEFORE any goroutine or
// artifact was created. The job is observable to Wait / list calls as Failed
// with the validation error recorded in artifactErr, and no run goroutine is
// started so the manager's wg is unaffected.
func (m *Manager) startInvalid(parentSession, kind, label string, validationErr error) *Job {
	finishedAt := nowMs()
	m.mu.Lock()
	m.seq++
	id := fmt.Sprintf("invalid-%d", m.seq)
	j := &Job{
		ID:               id,
		Kind:             kind,
		Label:            label,
		SessionID:        parentSession,
		status:           Failed,
		clock:            jobClock{startedAt: finishedAt, activityAt: finishedAt, finishedAt: finishedAt},
		outcome:          jobOutcome{returned: true},
		cancel:           func() {},
		done:             make(chan struct{}),
		artifactComplete: false,
		artifactErr:      validationErr.Error(),
	}
	key := jobKey(parentSession, id)
	m.jobs[key] = j
	m.order = append(m.order, key)
	m.mu.Unlock()
	close(j.done)
	m.recordCompletion(j, Failed, validationErr)
	m.notifyRuntime(parentSession, id)
	return j
}

func runRecovered(ctx context.Context, out io.Writer, run func(context.Context, io.Writer) (string, error)) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("internal error: panic: %v\n%s", r, debug.Stack())
		}
	}()
	return run(ctx, out)
}

func (m *Manager) openArtifactLocked(parentSession, id string) (logPath, metaPath string, file *os.File, artifactErr string) {
	dir := m.artifactDirLocked(parentSession)
	if dir == "" {
		return "", "", nil, "artifact directory unavailable"
	}
	if err := ensurePrivateArtifactDir(dir); err != nil {
		return filepath.Join(dir, id+jobLogExt), filepath.Join(dir, id+jobMetaExt), nil, err.Error()
	}
	logPath = filepath.Join(dir, id+jobLogExt)
	metaPath = filepath.Join(dir, id+jobMetaExt)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return logPath, metaPath, nil, err.Error()
	}
	// O_TRUNC does not apply the requested mode to an existing artifact. Tighten
	// it before any raw tool output is written so upgrades cannot append secrets
	// to a legacy 0644 log.
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return logPath, metaPath, nil, err.Error()
	}
	return logPath, metaPath, f, ""
}

func ensurePrivateArtifactDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// MkdirAll leaves an existing 0755 directory unchanged.
	return os.Chmod(dir, 0o700)
}

func (m *Manager) artifactDirLocked(parentSession string) string {
	parentSession = strings.TrimSpace(parentSession)
	if parentSession != "" {
		if dir := strings.TrimSpace(m.artifactDirs[parentSession]); dir != "" {
			return dir
		}
	}
	if strings.TrimSpace(m.tempRoot) == "" {
		return ""
	}
	if parentSession == "" {
		return filepath.Join(m.tempRoot, "default")
	}
	return filepath.Join(m.tempRoot, parentSession)
}

func (m *Manager) writeJobMetaLocked(j *Job, st Status) error {
	j.artifactStatus = st
	if j.artifactMetaPath == "" {
		return nil
	}
	meta := artifactMeta{
		ID:               j.ID,
		Kind:             j.Kind,
		Label:            j.Label,
		SessionID:        j.SessionID,
		OwnerID:          m.ownerID,
		Status:           st,
		StartedAt:        j.clock.startedAt,
		FinishedAt:       j.clock.finishedAt,
		ArtifactComplete: st != Running && j.artifactComplete && j.artifactErr == "",
		ArtifactError:    j.artifactErr,
		LogPath:          filepath.Base(j.artifactPath),
	}
	if j.Kind == "task" {
		meta.MutationEvidenceVersion = mutationEvidenceVersion
		meta.MutationEvidence = mutationEvidenceForArtifact(j.evidence)
	}
	return writeMeta(j.artifactMetaPath, meta)
}

func mutationEvidenceForArtifact(summary evidence.ChildEvidenceSummary) *artifactMutationEvidence {
	firstMutation := -1
	for i, receipt := range summary.Receipts {
		if receipt.Success && receipt.Mutation {
			firstMutation = i
			break
		}
	}
	if firstMutation < 0 {
		return nil
	}
	return &artifactMutationEvidence{
		Risk:  string(evidence.ClassifyMutationRiskWithin(summary.Receipts, firstMutation, summary.WorkspaceRoot)),
		Paths: summary.MutationPaths(),
	}
}

func mutationEvidenceFromArtifact(meta artifactMeta) evidence.ChildEvidenceSummary {
	if meta.Kind != "task" {
		return evidence.ChildEvidenceSummary{}
	}
	if meta.MutationEvidenceVersion != mutationEvidenceVersion {
		// Any version this build cannot parse — a pre-feature artifact
		// (version 0) or one written by a newer build — is treated as an
		// opaque mutation. A missing summary only proves the mutation state
		// was not recorded, not that the task made no changes: a legacy
		// background writer task collected after upgrade could carry real,
		// unreviewed edits. Recovering it as opaque RiskHigh forces fresh
		// inspection and review rather than silently skipping it, and keeps
		// downgrade coexistence on a shared state directory conservative.
		return opaqueRecoveredTaskMutation()
	}
	if meta.MutationEvidence == nil {
		// Same-version artifact with no summary: this build DID record the
		// mutation state and found none, so there is genuinely nothing to
		// recover.
		return evidence.ChildEvidenceSummary{}
	}

	paths := append([]string(nil), meta.MutationEvidence.Paths...)
	switch evidence.RiskLevel(meta.MutationEvidence.Risk) {
	case evidence.RiskLow, evidence.RiskMedium:
		// Known paths preserve the original adaptive risk level while still
		// requiring fresh inspection and verification after recovery.
	case evidence.RiskHigh:
		// The original risk may have come from an opaque or privileged tool,
		// which the sanitized artifact intentionally does not retain. Recover it
		// as opaque so restart cannot downgrade the security-review requirement.
		paths = nil
	default:
		return opaqueRecoveredTaskMutation()
	}
	return evidence.ChildEvidenceSummary{Receipts: []evidence.Receipt{{
		ToolName: recoveredBackgroundTaskToolName,
		Success:  true,
		Write:    true,
		Mutation: true,
		Paths:    paths,
	}}}
}

func opaqueRecoveredTaskMutation() evidence.ChildEvidenceSummary {
	return evidence.ChildEvidenceSummary{Receipts: []evidence.Receipt{{
		ToolName: recoveredBackgroundTaskToolName,
		Success:  true,
		Write:    true,
		Mutation: true,
	}}}
}

func (m *Manager) artifactTargetDirForJob(j *Job) string {
	if j == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := strings.TrimSpace(j.SessionID)
	if session == "" {
		return ""
	}
	return strings.TrimSpace(m.artifactDirs[session])
}

func (j *Job) noteArtifactErr(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	if j.artifactErr == "" {
		j.artifactErr = msg
	} else {
		j.artifactErr += "; " + msg
	}
	j.artifactComplete = false
}

func (j *Job) moveArtifactToDirLocked(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" || j.artifactPath == "" {
		return nil
	}
	if filepath.Clean(filepath.Dir(j.artifactPath)) == filepath.Clean(dir) {
		return nil
	}
	if err := ensurePrivateArtifactDir(dir); err != nil {
		return err
	}
	newLogPath := filepath.Join(dir, filepath.Base(j.artifactPath))
	if err := moveArtifactFile(j.artifactPath, newLogPath); err != nil {
		return err
	}
	j.artifactPath = newLogPath
	if j.artifactMetaPath != "" {
		j.artifactMetaPath = filepath.Join(dir, filepath.Base(j.artifactMetaPath))
	}
	return nil
}

func (m *Manager) monitorStalled(parentSession string, j *Job) {
	defer m.wg.Done()
	timer := time.NewTimer(m.stalledWarning)
	defer timer.Stop()
	for {
		select {
		case <-j.done:
			return
		case <-timer.C:
			j.mu.Lock()
			if j.outcome.returned || j.status != Running {
				j.mu.Unlock()
				return
			}
			idle := time.Since(time.UnixMilli(j.clock.activityAt))
			if idle >= m.stalledWarning && !j.clock.stalled {
				j.clock.stalled = true
				j.mu.Unlock()
				m.recordStalled(parentSession, j.ID, j.Kind, j.Label)
				return
			}
			wait := m.stalledWarning - idle
			if wait <= 0 {
				wait = m.stalledWarning
			}
			j.mu.Unlock()
			timer.Reset(wait)
		}
	}
}

// recordCompletion queues the finished-job summary for DrainCompletedNote and
// emits a closing Notice (warn for a failure, info otherwise).
func (m *Manager) recordCompletion(j *Job, st Status, err error) string {
	id, kind, label := j.ID, j.Kind, j.Label
	tag := id
	if label != "" {
		tag = fmt.Sprintf("%s (%s)", id, label)
	}
	shouldEmit := false
	m.mu.Lock()
	parentSession := strings.TrimSpace(j.SessionID)
	if parentSession != "" && m.destroying[parentSession] {
		m.mu.Unlock()
		return parentSession
	}
	m.completed = append(m.completed, completion{
		sessionID: parentSession,
		text:      fmt.Sprintf("%s — %s", tag, st),
	})
	active := m.active
	shouldEmit = active == "" || parentSession == "" || active == parentSession
	m.mu.Unlock()

	if !nilutil.IsNil(m.taskRecorder) {
		m.taskRecorder.RecordDone(id, st, err)
	}

	level, text := event.LevelInfo, fmt.Sprintf("background %s finished: %s", kind, id)
	detail := ""
	switch st {
	case Failed:
		level, text = event.LevelWarn, fmt.Sprintf("background %s failed: needs attention", kind)
		detail = fmt.Sprintf("background %s failed: %s — %v", kind, id, err)
	case Killed:
		text = fmt.Sprintf("background %s killed: %s", kind, id)
	}
	if shouldEmit {
		m.sink.Emit(event.Event{Kind: event.Notice, Code: event.NoticeCodeBackgroundJobFinished, Level: level, Text: text, Detail: detail})
	}
	return parentSession
}

func (m *Manager) recordStalled(parentSession, id, kind, label string) {
	tag := id
	if label != "" {
		tag = fmt.Sprintf("%s (%s)", id, label)
	}
	parentSession = strings.TrimSpace(parentSession)
	m.mu.Lock()
	if parentSession != "" && m.destroying[parentSession] {
		m.mu.Unlock()
		return
	}
	quietFor := m.stalledWarning.Round(time.Second)
	text := fmt.Sprintf("%s is still running after %s with no visible output — a quiet long-running job can look like this and is not necessarily stuck. If it should have finished, inspect it with wait or bash_output, or stop it with kill_shell. Tune or disable this check with tools.background_jobs.stalled_warning_seconds in your config (0 disables).", tag, quietFor)
	m.completed = append(m.completed, completion{sessionID: parentSession, text: text})
	active := m.active
	shouldEmit := active == "" || parentSession == "" || active == parentSession
	notice := event.Event{Kind: event.Notice, Level: event.LevelWarn,
		Text:   fmt.Sprintf("background %s still running after %s with no visible output: %s", kind, quietFor, id),
		Detail: "A quiet long-running job can look like this, so this is a heads-up, not an error. If it should have finished, inspect with wait or bash_output, or stop it with kill_shell. Set tools.background_jobs.stalled_warning_seconds to 0 in your config to disable this notice."}
	m.mu.Unlock()
	if shouldEmit {
		m.sink.Emit(notice)
	}
}

func (m *Manager) get(parentSession, id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.findJobLocked(parentSession, id)
}

func (m *Manager) findJobLocked(parentSession, id string) *Job {
	parentSession = strings.TrimSpace(parentSession)
	id = strings.TrimSpace(id)
	if parentSession != "" {
		return m.jobs[jobKey(parentSession, id)]
	}
	for _, key := range m.order {
		j := m.jobs[key]
		if j != nil && j.ID == id {
			return j
		}
	}
	return nil
}

// Output returns the job's output produced since the last Output call plus its
// current status. ok is false when the id is unknown.
func (m *Manager) Output(id string) (text string, status Status, ok bool) {
	return m.OutputForSession("", id)
}

// OutputForSession returns output only when id belongs to parentSession. Empty
// parentSession preserves the legacy unscoped behavior.
func (m *Manager) OutputForSession(parentSession, id string) (text string, status Status, ok bool) {
	j := m.get(parentSession, id)
	if j == nil {
		return "", "", false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.artifactPath != "" {
		text = j.readArtifactSinceOffsetLocked()
	} else {
		full := string(j.tail)
		if j.readOffset < int64(len(full)) {
			text = full[j.readOffset:]
			j.readOffset = int64(len(full))
		}
	}
	// A task job streams nothing to the tail buffer — its answer lands in
	// outcome.text. Surface it once when terminal with no buffered output, so a
	// task's answer is visible here too (bash_output promises task support).
	if text == "" && j.status != Running && j.outcome.text != "" && !j.outcome.read {
		text = j.outcome.text
		j.outcome.read = true
	}
	if j.artifactErr != "" {
		if text != "" {
			text += "\n"
		}
		text += "job artifact incomplete: " + j.artifactErr
	}
	return text, j.status, true
}

func (j *Job) readArtifactSinceOffsetLocked() string {
	f, err := os.Open(j.artifactPath)
	if err != nil {
		if j.artifactErr == "" {
			j.artifactErr = err.Error()
		}
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		if j.artifactErr == "" {
			j.artifactErr = err.Error()
		}
		return ""
	}
	size := info.Size()
	if j.readOffset > size {
		j.readOffset = size
		return ""
	}
	if _, err := f.Seek(j.readOffset, io.SeekStart); err != nil {
		if j.artifactErr == "" {
			j.artifactErr = err.Error()
		}
		return ""
	}
	b, err := io.ReadAll(f)
	if err != nil {
		if j.artifactErr == "" {
			j.artifactErr = err.Error()
		}
		return ""
	}
	text := string(b)
	j.readOffset = size
	return text
}

// readArtifactAllLocked deliberately reads raw bytes: the artifact is captured
// subprocess output (possibly binary), not a user-edited config file, and the
// incremental reader (readArtifactSinceOffsetLocked) is raw byte-offset based —
// decoding only the whole-file path would render the same artifact in two
// different encodings and could garble binary output via UTF-16 misdetection.
func (j *Job) readArtifactAllLocked() string {
	if j.artifactPath == "" {
		return ""
	}
	b, err := os.ReadFile(j.artifactPath)
	if err != nil {
		if j.artifactErr == "" {
			j.artifactErr = err.Error()
		}
		return ""
	}
	return string(b)
}

// Kill cancels a running job. Returns false when the id is unknown or the job has
// already finished.
func (m *Manager) Kill(id string) bool {
	return m.KillForSession("", id)
}

// KillForSession cancels a running job only when it belongs to parentSession.
// Empty parentSession preserves the legacy unscoped behavior.
func (m *Manager) KillForSession(parentSession, id string) bool {
	j := m.get(parentSession, id)
	if j == nil {
		return false
	}
	j.mu.Lock()
	running := j.status == Running
	if running {
		// Flip to Killed synchronously so Output/Wait reflect the kill the instant
		// it's requested, not whenever the run goroutine's cmd.Run returns (which
		// trails by WaitDelay while a cancelled process tree tears down). The
		// goroutine still sets Killed + records completion on return; this only
		// fires when the job is actually Running, so a job that just finished
		// keeps its real terminal status.
		j.status = Killed
	}
	j.mu.Unlock()
	if !running {
		return false
	}
	j.cancel()
	return true
}

// Wait blocks until the named jobs (or every currently-running job when ids is
// empty) reach a terminal state, or ctx is cancelled, or timeoutSec elapses
// (0 = no timeout). It returns each target's snapshot regardless of why it
// returned, so a timeout still reports partial progress.
func (m *Manager) Wait(ctx context.Context, ids []string, timeoutSec int) []Result {
	return m.WaitForSession(ctx, "", ids, timeoutSec)
}

// WaitForSession waits only on jobs owned by parentSession. Empty parentSession
// preserves the legacy unscoped behavior.
func (m *Manager) WaitForSession(ctx context.Context, parentSession string, ids []string, timeoutSec int) []Result {
	targets := m.resolve(parentSession, ids)
	if len(targets) == 0 {
		return nil
	}
	var timeout <-chan time.Time
	if timeoutSec > 0 {
		t := time.NewTimer(time.Duration(timeoutSec) * time.Second)
		defer t.Stop()
		timeout = t.C
	}
	for _, j := range targets {
		select {
		case <-j.done:
		case <-ctx.Done():
			return m.results(targets)
		case <-timeout:
			return m.results(targets)
		}
	}
	return m.results(targets)
}

// resolve maps requested ids to jobs; an empty list selects all running jobs.
func (m *Manager) resolve(parentSession string, ids []string) []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Job
	if len(ids) == 0 {
		for _, key := range m.order {
			j := m.jobs[key]
			if !sessionMatches(parentSession, j.SessionID) {
				continue
			}
			j.mu.Lock()
			running := j.status == Running
			j.mu.Unlock()
			if running {
				out = append(out, j)
			}
		}
		return out
	}
	for _, id := range ids {
		if j := m.findJobLocked(parentSession, id); j != nil {
			out = append(out, j)
		}
	}
	return out
}

func (m *Manager) results(targets []*Job) []Result {
	out := make([]Result, 0, len(targets))
	for _, j := range targets {
		j.mu.Lock()
		text := j.outcome.text
		if text == "" && j.artifactPath != "" {
			text = j.readArtifactAllLocked()
		}
		if text == "" {
			text = string(j.tail)
		}
		if j.artifactErr != "" {
			if text != "" {
				text += "\n"
			}
			text += "job artifact incomplete: " + j.artifactErr
		}
		out = append(out, Result{ID: j.ID, Kind: j.Kind, Label: j.Label, Status: j.status, Output: text})
		j.mu.Unlock()
	}
	return out
}

// Running returns a snapshot of the still-running jobs (for the status bar).
func (m *Manager) Running() []View {
	return m.RunningForSession("")
}

// RunningForSession returns still-running jobs owned by parentSession. Empty
// parentSession preserves the legacy unscoped behavior.
func (m *Manager) RunningForSession(parentSession string) []View {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []View
	for _, key := range m.order {
		j := m.jobs[key]
		if !sessionMatches(parentSession, j.SessionID) {
			continue
		}
		select {
		case <-j.done:
			continue
		default:
		}
		j.mu.Lock()
		// A cancellation request flips the persisted/result status to Killed
		// synchronously, but the process tree may still be unwinding. Keep the job
		// on the operational running surface until its done channel closes so
		// Desktop rebuild guards and Delivery workspace leases cannot declare the
		// runtime idle early. The public view remains "running" while a stop is
		// in flight; clients may render a local "stopping" state after they
		// request cancellation.
		out = append(out, View{ID: j.ID, Kind: j.Kind, Label: j.Label, Status: string(Running), StartedAt: j.clock.startedAt})
		j.mu.Unlock()
	}
	return out
}

// ReserveStartForSession atomically reserves capacity for a job start. The
// caller must release the reservation after StartForSession has registered the
// job (or when setup fails). Running jobs and in-flight start reservations both
// count toward limit, so concurrent callers cannot overshoot it.
func (m *Manager) ReserveStartForSession(parentSession, kind string, limit int) (release func(), running int, ok bool) {
	if limit <= 0 {
		return func() {}, 0, true
	}
	parentSession = strings.TrimSpace(parentSession)
	key := jobKey(parentSession, kind)
	m.mu.Lock()
	for _, jobKey := range m.order {
		j := m.jobs[jobKey]
		if j == nil || !sessionMatches(parentSession, j.SessionID) || j.Kind != kind {
			continue
		}
		select {
		case <-j.done:
		default:
			running++
		}
	}
	running += m.reservations[key]
	if running >= limit {
		m.mu.Unlock()
		return func() {}, running, false
	}
	m.reservations[key]++
	m.mu.Unlock()

	var once sync.Once
	release = func() {
		once.Do(func() {
			m.mu.Lock()
			m.reservations[key]--
			if m.reservations[key] == 0 {
				delete(m.reservations, key)
			}
			m.mu.Unlock()
		})
	}
	return release, running, true
}

// HasUnfinishedForSession reports whether parentSession owns any job whose
// goroutine has not fully exited yet. Empty parentSession preserves the legacy
// unscoped behavior.
func (m *Manager) HasUnfinishedForSession(parentSession string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, key := range m.order {
		j := m.jobs[key]
		if !sessionMatches(parentSession, j.SessionID) {
			continue
		}
		select {
		case <-j.done:
		default:
			return true
		}
	}
	return false
}

// DrainCompletedNote returns (and clears) a one-line summary of jobs that
// finished since the last drain, for the controller to fold into the next turn
// so the model learns of completions. "" when nothing finished.
func (m *Manager) DrainCompletedNote() string {
	return m.DrainCompletedNoteForSession("")
}

// DrainCompletedNoteForSession drains completion notes for parentSession only.
// Notes for other sessions stay queued until that session becomes active again.
// Empty parentSession preserves the legacy unscoped behavior.
func (m *Manager) DrainCompletedNoteForSession(parentSession string) string {
	m.mu.Lock()
	var c []string
	if strings.TrimSpace(parentSession) == "" {
		for _, item := range m.completed {
			c = append(c, item.text)
		}
		m.completed = nil
	} else {
		remaining := m.completed[:0]
		for _, item := range m.completed {
			if item.sessionID == parentSession {
				c = append(c, item.text)
			} else {
				remaining = append(remaining, item)
			}
		}
		m.completed = remaining
	}
	m.mu.Unlock()
	if len(c) == 0 {
		return ""
	}
	return "Background job updates since your last message: " + strings.Join(c, "; ") +
		". Read their output with bash_output or wait if you still need it."
}

// SetActiveSession controls which session receives lifecycle notices for jobs
// that finish asynchronously. Empty active session preserves legacy behavior.
func (m *Manager) SetActiveSession(parentSession string) {
	m.mu.Lock()
	m.active = strings.TrimSpace(parentSession)
	m.mu.Unlock()
}

// validateTrustedSessionPath performs defense-in-depth syntax validation on a
// transcript path already trusted by the store/controller layer. It rejects
// control characters, but deliberately preserves separators and `..`: those are
// valid host-path syntax, and rejecting them without a trusted root would break
// legitimate relative paths without establishing filesystem containment.
func validateTrustedSessionPath(sessionPath string) error {
	if sessionPath == "" {
		return fmt.Errorf("jobs: sessionPath must not be empty")
	}
	for i, r := range sessionPath {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("jobs: sessionPath contains control character 0x%02x at index %d", r, i)
		}
	}
	return nil
}

// SetActiveSessionPath binds a parent session id to its persistent transcript
// path, migrates any temporary artifacts, and loads completed job tombstones from
// the session sidecar. sessionPath must come from the trusted store/controller
// path; this method does not establish filesystem containment on its own.
func (m *Manager) SetActiveSessionPath(parentSession, sessionPath string) {
	defer func() { m.notifyRuntime("", "") }()
	parentSession = strings.TrimSpace(parentSession)
	sessionPath = strings.TrimSpace(sessionPath)
	// Preserve the legacy active-only behavior for calls without a complete
	// binding. In particular, an empty path is not an error or filesystem input.
	if parentSession == "" || sessionPath == "" {
		m.mu.Lock()
		m.active = parentSession
		m.mu.Unlock()
		return
	}
	// Reject malformed trusted paths before any filesystem side effect. This is
	// syntax hardening, not a boundary for arbitrary caller-controlled paths.
	if err := validateTrustedSessionPath(sessionPath); err != nil {
		m.mu.Lock()
		m.active = parentSession
		// A rejected rebinding must not leave future jobs writing to a stale
		// transcript that happened to use the same parent session id.
		delete(m.artifactDirs, parentSession)
		delete(m.loaded, parentSession)
		m.mu.Unlock()
		m.sink.Emit(event.Event{
			Kind:   event.Notice,
			Level:  event.LevelWarn,
			Text:   "Ignoring SetActiveSessionPath with invalid session path",
			Detail: fmt.Sprintf("session %q: %v", parentSession, err),
		})
		return
	}
	m.mu.Lock()
	m.active = parentSession
	oldDir := m.artifactDirLocked(parentSession)
	adoptDefault := false
	if _, hasDir := m.artifactDirs[parentSession]; !hasDir && m.hasUnscopedJobsLocked() {
		oldDir = m.artifactDirLocked("")
		adoptDefault = true
	}
	newDir := ArtifactDir(sessionPath)
	m.artifactDirs[parentSession] = newDir
	loaded := m.loaded[parentSession]
	m.mu.Unlock()

	var migrationErr error
	if oldDir != "" && newDir != "" && oldDir != newDir {
		oldSession := parentSession
		if adoptDefault {
			oldSession = ""
		}
		migrationErr = m.migrateArtifactDirForSession(oldSession, oldDir, newDir)
	}
	if adoptDefault {
		m.mu.Lock()
		adopted := m.adoptUnscopedJobsLocked(parentSession)
		m.mu.Unlock()
		for _, j := range adopted {
			j.mu.Lock()
			st := j.artifactStatus
			if st == "" {
				st = j.status
			}
			if err := m.writeJobMetaLocked(j, st); err != nil {
				j.noteArtifactErr("ownership metadata: " + err.Error())
			}
			j.mu.Unlock()
		}
	}
	if migrationErr != nil {
		m.recordArtifactMigrationError(parentSession, migrationErr)
	}
	if !loaded {
		m.loadSessionArtifacts(parentSession, sessionPath, newDir)
	}
}

func (m *Manager) hasUnscopedJobsLocked() bool {
	for _, j := range m.jobs {
		if j != nil && strings.TrimSpace(j.SessionID) == "" {
			return true
		}
	}
	return false
}

func (m *Manager) adoptUnscopedJobsLocked(parentSession string) []*Job {
	var adopted []*Job
	parentSession = strings.TrimSpace(parentSession)
	if parentSession == "" {
		return adopted
	}
	for i := range m.completed {
		if strings.TrimSpace(m.completed[i].sessionID) == "" {
			m.completed[i].sessionID = parentSession
		}
	}
	for oldKey, j := range m.jobs {
		if j == nil || strings.TrimSpace(j.SessionID) != "" {
			continue
		}
		newKey := jobKey(parentSession, j.ID)
		if existing := m.jobs[newKey]; existing != nil && existing != j {
			j.mu.Lock()
			j.artifactErr = "migration: job id collision while adopting temporary session"
			j.artifactComplete = false
			j.mu.Unlock()
			continue
		}
		delete(m.jobs, oldKey)
		// Manager readers and artifact writers use distinct locks. Ownership
		// changes hold both, always in manager-before-job order.
		j.mu.Lock()
		j.SessionID = parentSession
		j.mu.Unlock()
		adopted = append(adopted, j)
		m.jobs[newKey] = j
		for i, key := range m.order {
			if key == oldKey {
				m.order[i] = newKey
			}
		}
	}
	return adopted
}

func (m *Manager) recordArtifactMigrationError(parentSession string, err error) {
	text := "job artifact migration failed: " + err.Error()
	m.mu.Lock()
	for _, j := range m.jobs {
		if j == nil || !sessionMatches(parentSession, j.SessionID) {
			continue
		}
		j.mu.Lock()
		if j.artifactErr == "" {
			j.artifactErr = "migration: " + err.Error()
			j.artifactComplete = false
		}
		j.mu.Unlock()
	}
	active := m.active
	m.mu.Unlock()
	if active == "" || active == parentSession {
		m.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "Job artifact migration failed.", Detail: text})
	}
}

type artifactMigrationJob struct {
	job     *Job
	wasOpen bool
}

func (m *Manager) migrateArtifactDirForSession(parentSession, oldDir, newDir string) error {
	locked := m.lockArtifactJobsForMigration(parentSession, oldDir)
	defer unlockArtifactMigrationJobs(locked)
	skip := openArtifactMigrationFiles(locked)
	migrateErr := migrateArtifactDirSkipping(oldDir, newDir, skip)
	if migrateErr == nil {
		rebaseArtifactMigrationJobs(locked, newDir)
	}
	return migrateErr
}

func (m *Manager) lockArtifactJobsForMigration(parentSession, dir string) []artifactMigrationJob {
	parentSession = strings.TrimSpace(parentSession)
	dir = filepath.Clean(strings.TrimSpace(dir))
	m.mu.Lock()
	jobs := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		if j == nil || strings.TrimSpace(j.SessionID) != parentSession {
			continue
		}
		jobs = append(jobs, j)
	}
	m.mu.Unlock()
	sort.Slice(jobs, func(i, k int) bool {
		return jobs[i].ID < jobs[k].ID
	})
	locked := make([]artifactMigrationJob, 0, len(jobs))
	for _, j := range jobs {
		j.mu.Lock()
		if !artifactPathInDir(j.artifactPath, dir) {
			j.mu.Unlock()
			continue
		}
		locked = append(locked, artifactMigrationJob{job: j, wasOpen: j.artifactFile != nil})
	}
	return locked
}

func artifactPathInDir(path, dir string) bool {
	path = filepath.Clean(strings.TrimSpace(path))
	dir = filepath.Clean(strings.TrimSpace(dir))
	if path == "." || dir == "." {
		return false
	}
	return filepath.Dir(path) == dir
}

func openArtifactMigrationFiles(jobs []artifactMigrationJob) map[string]bool {
	skip := map[string]bool{}
	for _, item := range jobs {
		j := item.job
		if j == nil || j.artifactFile == nil {
			continue
		}
		if j.artifactPath != "" {
			skip[filepath.Base(j.artifactPath)] = true
		}
		if j.artifactMetaPath != "" {
			skip[filepath.Base(j.artifactMetaPath)] = true
		}
	}
	return skip
}

func rebaseArtifactMigrationJobs(jobs []artifactMigrationJob, dir string) {
	for _, item := range jobs {
		j := item.job
		if j == nil || item.wasOpen {
			continue
		}
		if j.artifactPath != "" {
			j.artifactPath = filepath.Join(dir, filepath.Base(j.artifactPath))
		}
		if j.artifactMetaPath != "" {
			j.artifactMetaPath = filepath.Join(dir, filepath.Base(j.artifactMetaPath))
		}
	}
}

func unlockArtifactMigrationJobs(jobs []artifactMigrationJob) {
	for _, v := range slices.Backward(jobs) {
		if v.job != nil {
			v.job.mu.Unlock()
		}
	}
}

func migrateArtifactDir(src, dst string) error {
	return migrateArtifactDirSkipping(src, dst, nil)
}

func migrateArtifactDirSkipping(src, dst string, skip map[string]bool) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := ensurePrivateArtifactDir(dst); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if skip[entry.Name()] {
			continue
		}
		if err := moveArtifactFile(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}
	_ = os.Remove(src)
	return nil
}

func moveArtifactFile(src, dst string) error {
	// A rename preserves the source mode, so tighten legacy artifacts before
	// either the fast rename or the cross-device copy fallback.
	if err := os.Chmod(src, 0o600); err != nil {
		return err
	}
	if err := renamePath(src, dst); err == nil {
		return nil
	}
	if err := copyArtifactFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

func copyArtifactFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := out.Chmod(0o600); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return closeErr
	}
	return nil
}

func (m *Manager) loadSessionArtifacts(parentSession, sessionPath, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		m.mu.Lock()
		m.loaded[parentSession] = true
		m.mu.Unlock()
		return
	}
	var loaded []*Job
	deferredLiveOwner := false
	var repairErrors []string
	maxSeq := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != jobMetaExt {
			continue
		}
		metaPath := filepath.Join(dir, entry.Name())
		meta, err := readMeta(metaPath)
		if err != nil || strings.TrimSpace(meta.ID) == "" {
			continue
		}
		id := strings.TrimSpace(meta.ID)
		if seq := maxJobSeq(id); seq > maxSeq {
			maxSeq = seq
		}
		// A persisted Running record may belong to another manager in this
		// process or to another Reasonix process entirely. Only the runtime that
		// owns the session lease may repair an abandoned record as Interrupted.
		// Observers without proof of ownership defer the artifact and leave the
		// session reloadable for a later owned bind.
		if meta.Status == Running {
			if managerOwnerIsLive(meta.OwnerID) {
				deferredLiveOwner = true
				continue
			}
			if m.sessionOwnershipProbe == nil || !m.sessionOwnershipProbe(sessionPath) {
				deferredLiveOwner = true
				continue
			}
			meta.Status = Interrupted
			if meta.FinishedAt == 0 {
				meta.FinishedAt = nowMs()
			}
			meta.ArtifactComplete = false
			if err := repairArtifactMeta(metaPath, meta); err != nil {
				// Do not publish an in-memory Interrupted tombstone when the durable
				// state still says Running. Keep the session reloadable so a later bind
				// can retry the repair, and surface the failure instead of letting live
				// and machine-facing status silently disagree.
				deferredLiveOwner = true
				repairErrors = append(repairErrors, fmt.Sprintf("repair job %s metadata: %v", id, err))
				continue
			}
		}
		done := make(chan struct{})
		close(done)
		logPath := filepath.Join(dir, id+jobLogExt)
		if strings.TrimSpace(meta.LogPath) != "" {
			logPath = filepath.Join(dir, filepath.Base(meta.LogPath))
		}
		loaded = append(loaded, &Job{
			ID:               id,
			Kind:             meta.Kind,
			Label:            meta.Label,
			SessionID:        parentSession,
			status:           meta.Status,
			clock:            jobClock{startedAt: meta.StartedAt, finishedAt: meta.FinishedAt, activityAt: meta.FinishedAt},
			done:             done,
			artifactPath:     logPath,
			artifactMetaPath: filepath.Join(dir, id+jobMetaExt),
			artifactComplete: meta.ArtifactComplete,
			artifactErr:      meta.ArtifactError,
			tombstone:        true,
			evidence:         mutationEvidenceFromArtifact(meta),
		})
	}
	if len(repairErrors) > 0 {
		m.sink.Emit(event.Event{
			Kind:   event.Notice,
			Level:  event.LevelWarn,
			Text:   "Background job recovery did not complete.",
			Detail: strings.Join(repairErrors, "; "),
		})
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range loaded {
		key := jobKey(parentSession, j.ID)
		if _, exists := m.jobs[key]; exists {
			continue
		}
		m.jobs[key] = j
		m.order = append(m.order, key)
	}
	if maxSeq > m.seq {
		m.seq = maxSeq
	}
	m.loaded[parentSession] = !deferredLiveOwner
}

// BeginDestroySession marks a parent session as being removed from active use
// and cancels its running jobs. WaitTeardown waits for the returned handle.
func (m *Manager) BeginDestroySession(parentSession string) SessionTeardown {
	parentSession = strings.TrimSpace(parentSession)
	if parentSession == "" {
		return SessionTeardown{}
	}
	var cancels []context.CancelFunc
	var targets []teardownTarget
	m.mu.Lock()
	m.destroying[parentSession] = true
	remaining := m.completed[:0]
	for _, item := range m.completed {
		if item.sessionID != parentSession {
			remaining = append(remaining, item)
		}
	}
	m.completed = remaining
	for _, key := range m.order {
		j := m.jobs[key]
		if !sessionMatches(parentSession, j.SessionID) {
			continue
		}
		j.mu.Lock()
		switch j.status {
		case Running:
			j.status = Killed
			cancels = append(cancels, j.cancel)
			targets = append(targets, teardownTarget{info: TeardownJob{ID: j.ID, Kind: j.Kind, Label: j.Label}, done: j.done})
		case Killed:
			targets = append(targets, teardownTarget{info: TeardownJob{ID: j.ID, Kind: j.Kind, Label: j.Label}, done: j.done})
		}
		j.mu.Unlock()
	}
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	return SessionTeardown{SessionID: parentSession, targets: targets}
}

// DestroySession preserves the legacy channel-based destroy API.
func (m *Manager) DestroySession(parentSession string) []<-chan struct{} {
	return m.BeginDestroySession(parentSession).DoneChannels()
}

// WaitTeardown waits for a destroy handle to unwind up to grace. A timed-out
// result means the caller should defer physical cleanup until the jobs exit.
func (m *Manager) WaitTeardown(ctx context.Context, h SessionTeardown, grace time.Duration) TeardownResult {
	result, timedOut := waitTeardownTargets(ctx, h.targets, grace)
	if timedOut {
		m.emitTeardownTimeout("destroy session "+h.SessionID, result)
	}
	return result
}

// IsDestroying reports whether parentSession is in the destroy window. Empty
// parent sessions are never considered destroyed.
func (m *Manager) IsDestroying(parentSession string) bool {
	parentSession = strings.TrimSpace(parentSession)
	if parentSession == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.destroying[parentSession]
}

// FinishDestroySession ends the destroy window after all owned jobs have unwound
// and persistent cleanup/move work has completed.
func (m *Manager) FinishDestroySession(parentSession string) {
	parentSession = strings.TrimSpace(parentSession)
	if parentSession == "" {
		return
	}
	m.mu.Lock()
	delete(m.destroying, parentSession)
	delete(m.artifactDirs, parentSession)
	delete(m.loaded, parentSession)
	m.purgeSessionLocked(parentSession)
	m.mu.Unlock()
}

func (m *Manager) purgeSessionLocked(parentSession string) {
	kept := m.order[:0]
	for _, key := range m.order {
		j := m.jobs[key]
		if j == nil || sessionMatches(parentSession, j.SessionID) {
			delete(m.jobs, key)
			continue
		}
		kept = append(kept, key)
	}
	m.order = kept
}

// Close cancels the session context and waits briefly for every background job
// goroutine to return before unblocking. If a non-cooperative job ignores
// cancellation, cleanup of the temporary artifact root continues in the
// background after the goroutines eventually unwind.
func (m *Manager) Close() {
	_ = m.CloseWithGrace(m.teardownGrace)
}

// CloseAsync cancels the manager and returns immediately. It is used when a
// caller has already begun session-specific teardown and owns the delayed
// persistent cleanup, but still needs the manager's root context and temporary
// artifact root released eventually.
func (m *Manager) CloseAsync() {
	m.cancel()
	go func() {
		m.wg.Wait()
		m.releaseOwner()
		m.removeTempRoot()
	}()
}

// CloseWithGrace is Close with an explicit wait window, used by tests and
// callers that need to surface non-cooperative jobs.
func (m *Manager) CloseWithGrace(grace time.Duration) TeardownResult {
	m.cancel()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		m.releaseOwner()
		close(done)
	}()
	result, timedOut := waitTeardownTargets(context.Background(), m.closeTargets(), grace, done)
	if timedOut {
		m.emitTeardownTimeout("close", result)
		go func() {
			<-done
			m.removeTempRoot()
		}()
		return result
	}
	m.removeTempRoot()
	return result
}

func waitTeardownTargets(ctx context.Context, targets []teardownTarget, grace time.Duration, allDone ...<-chan struct{}) (TeardownResult, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now()
	var timeout <-chan time.Time
	if grace >= 0 {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		timeout = timer.C
	}
	if len(allDone) > 0 && allDone[0] != nil {
		select {
		case <-allDone[0]:
			return TeardownResult{}, false
		case <-ctx.Done():
			return teardownTimedOut(targets, time.Since(start)), false
		case <-timeout:
			return teardownTimedOut(targets, time.Since(start)), true
		}
	}
	for _, target := range targets {
		select {
		case <-target.done:
		case <-ctx.Done():
			return teardownTimedOut(targets, time.Since(start)), false
		case <-timeout:
			return teardownTimedOut(targets, time.Since(start)), true
		}
	}
	return TeardownResult{}, false
}

func teardownTimedOut(targets []teardownTarget, waited time.Duration) TeardownResult {
	var out []TeardownJob
	for _, target := range targets {
		select {
		case <-target.done:
			continue
		default:
		}
		info := target.info
		info.Waited = waited
		out = append(out, info)
	}
	return TeardownResult{TimedOut: out}
}

func (m *Manager) closeTargets() []teardownTarget {
	m.mu.Lock()
	defer m.mu.Unlock()
	var targets []teardownTarget
	for _, key := range m.order {
		j := m.jobs[key]
		if j == nil {
			continue
		}
		select {
		case <-j.done:
			continue
		default:
		}
		j.mu.Lock()
		switch j.status {
		case Running:
			j.status = Killed
			targets = append(targets, teardownTarget{info: TeardownJob{ID: j.ID, Kind: j.Kind, Label: j.Label}, done: j.done})
		case Killed:
			targets = append(targets, teardownTarget{info: TeardownJob{ID: j.ID, Kind: j.Kind, Label: j.Label}, done: j.done})
		}
		j.mu.Unlock()
	}
	return targets
}

func (m *Manager) emitTeardownTimeout(action string, result TeardownResult) {
	if len(result.TimedOut) == 0 {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "background job teardown timed out during %s", strings.TrimSpace(action))
	for i, job := range result.TimedOut {
		if i == 0 {
			b.WriteString(": ")
		} else {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%s kind=%s", job.ID, job.Kind)
		if strings.TrimSpace(job.Label) != "" {
			fmt.Fprintf(&b, " label=%q", job.Label)
		}
		if job.Waited > 0 {
			fmt.Fprintf(&b, " waited=%s", job.Waited.Round(time.Millisecond))
		}
	}
	m.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "Background job teardown timed out.", Detail: b.String()})
}

func (m *Manager) removeTempRoot() {
	if m.tempRoot != "" {
		_ = os.RemoveAll(m.tempRoot)
	}
}

func nowMs() int64 { return time.Now().UnixMilli() }

func startedText(kind, id, label string) string {
	if label != "" {
		return fmt.Sprintf("background %s started: %s (%s)", kind, id, label)
	}
	return fmt.Sprintf("background %s started: %s", kind, id)
}

func (m *Manager) emitIfActive(parentSession string, ev event.Event) {
	m.mu.Lock()
	active := m.active
	m.mu.Unlock()
	if active == "" || strings.TrimSpace(parentSession) == "" || active == strings.TrimSpace(parentSession) {
		m.sink.Emit(ev)
	}
}

func sessionMatches(filter, jobSession string) bool {
	filter = strings.TrimSpace(filter)
	return filter == "" || strings.TrimSpace(jobSession) == filter
}

func jobKey(parentSession, id string) string {
	return strings.TrimSpace(parentSession) + "\x00" + strings.TrimSpace(id)
}

// call-context injection (mirrors agent.CallContext)

type ctxKey struct{}
type sessionCtxKey struct{}
type jobCtxKey struct{}
type noManager struct{}

// WithManager stamps ctx with the job manager so tools can reach it via
// FromContext. The agent sets this on every tool call's context.
func WithManager(ctx context.Context, m *Manager) context.Context {
	return context.WithValue(ctx, ctxKey{}, m)
}

// WithoutManager shadows an ancestor manager. Child agents without an owned
// Jobs manager must not operate the parent's background jobs by inheritance.
func WithoutManager(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKey{}, noManager{})
}

// FromContext returns the job manager set by the agent, if any. ok is false for a
// plain context (headless tests, calls outside the run loop).
func FromContext(ctx context.Context) (*Manager, bool) {
	m, ok := ctx.Value(ctxKey{}).(*Manager)
	return m, ok && m != nil
}

// WithSession stamps ctx with the active parent session ID for session-scoped job
// operations.
func WithSession(ctx context.Context, parentSession string) context.Context {
	return context.WithValue(ctx, sessionCtxKey{}, strings.TrimSpace(parentSession))
}

// SessionFromContext returns the active parent session ID for job ownership and
// filtering. Empty means no session scope is available.
func SessionFromContext(ctx context.Context) string {
	session, _ := ctx.Value(sessionCtxKey{}).(string)
	return strings.TrimSpace(session)
}

// PublishEvidence attaches a background agent's host-observed receipts to its
// job. The receipts stay independent of the parent turn ledger until the
// parent collects the terminal result with wait or bash_output.
func PublishEvidence(ctx context.Context, summary evidence.ChildEvidenceSummary) {
	j, _ := ctx.Value(jobCtxKey{}).(*Job)
	if j == nil || len(summary.Receipts) == 0 {
		return
	}
	j.mu.Lock()
	mergePublishedEvidence(&j.evidence, summary)
	j.mu.Unlock()
}

// LeaseEvidenceForSession returns a copy of a terminal job's evidence without
// consuming it. Collection is only provisional: the receipts merge into the
// collecting turn's ledger, but that ledger is discarded if the turn is
// cancelled, errors, or the process exits before the turn commits. Consuming
// here would then lose the mutation for good — the parent's next turn resets its
// ledger and this job would report nothing, so a background change would ship
// unreviewed. The evidence is drained only by CommitEvidenceForSession, which
// the agent calls after the collecting turn passes its delivery gates. A
// committed job returns empty so a re-poll after successful delivery does not
// re-demand review.
func (m *Manager) LeaseEvidenceForSession(parentSession, id string) evidence.ChildEvidenceSummary {
	summary, _ := m.tryLeaseEvidenceForSession(parentSession, id)
	return summary
}

// TryLeaseEvidenceForSession is LeaseEvidenceForSession plus a ready flag that
// separates "terminal evidence available" (possibly empty — a committed job or
// one with no mutations) from "not ready to lease yet": unknown job, still
// running, or killed but its run goroutine has not yet flushed PublishEvidence
// and closed done. KillForSession flips status to Killed synchronously, well
// before the goroutine actually returns, so a bash_output poll that lands in
// that window must not treat the empty read as final. Callers that record a
// lease (collectBackgroundEvidence) must gate on ready so they never note a
// lease before the evidence exists — noting it early would let a later commit
// drain evidence nobody ever merged or reviewed.
func (m *Manager) TryLeaseEvidenceForSession(parentSession, id string) (evidence.ChildEvidenceSummary, bool) {
	return m.tryLeaseEvidenceForSession(parentSession, id)
}

func (m *Manager) tryLeaseEvidenceForSession(parentSession, id string) (evidence.ChildEvidenceSummary, bool) {
	j := m.get(parentSession, id)
	if j == nil {
		return evidence.ChildEvidenceSummary{}, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	select {
	case <-j.done:
	default:
		return evidence.ChildEvidenceSummary{}, false
	}
	if j.evidenceCommitted {
		return evidence.ChildEvidenceSummary{}, true
	}
	out := make([]evidence.Receipt, len(j.evidence.Receipts))
	copy(out, j.evidence.Receipts)
	return evidence.ChildEvidenceSummary{Receipts: out, WorkspaceRoot: j.evidence.WorkspaceRoot}, true
}

// PendingEvidenceJobIDsForSession returns the IDs of parentSession's terminal
// jobs that carry uncommitted mutation evidence — a prior turn leased it but
// never delivered (the turn failed or was cancelled, and the next turn's Reset
// wiped it from the per-turn ledger), or the process restarted before any turn
// collected it at all. The agent re-leases these at the start of every turn so
// a turn that never calls wait/bash_output still surfaces the pending mutation
// to its final-readiness checks instead of silently shipping it unreviewed.
func (m *Manager) PendingEvidenceJobIDsForSession(parentSession string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for _, key := range m.order {
		j := m.jobs[key]
		if j == nil || !sessionMatches(parentSession, j.SessionID) {
			continue
		}
		j.mu.Lock()
		terminal := false
		select {
		case <-j.done:
			terminal = true
		default:
		}
		pending := terminal && !j.evidenceCommitted && len(j.evidence.Receipts) > 0
		j.mu.Unlock()
		if pending {
			ids = append(ids, j.ID)
		}
	}
	return ids
}

// CommitEvidenceForSession permanently consumes a terminal job's evidence after
// the collecting turn has accounted for it (passed final-readiness). It clears
// the in-memory copy and drains the persisted mutation summary so neither a
// same-process re-poll nor a restart resurrects receipts the delivered turn
// already reviewed. Best-effort on the disk rewrite — a failed rewrite merely
// restores the conservative resurrection behavior.
func (m *Manager) CommitEvidenceForSession(parentSession, id string) {
	j := m.get(parentSession, id)
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	select {
	case <-j.done:
	default:
		return
	}
	if j.evidenceCommitted {
		return
	}
	hadEvidence := len(j.evidence.Receipts) > 0
	j.evidenceCommitted = true
	j.evidence = evidence.ChildEvidenceSummary{}
	if hadEvidence {
		if err := m.writeJobMetaLocked(j, j.status); err != nil {
			j.noteArtifactErr("evidence drain: " + err.Error())
		}
	}
}
