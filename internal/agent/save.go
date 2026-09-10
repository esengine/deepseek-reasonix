package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"reasonix/internal/filelock"
	"reasonix/internal/fileutil"
	fileencoding "reasonix/internal/fileutil/encoding"
	"reasonix/internal/pathidentity"
	"reasonix/internal/provider"
	"reasonix/internal/store"
)

const (
	cleanupPendingExt             = ".cleanup-pending.json"
	maxRecoveryParentStemBytes    = 80
	sessionLockSidecarSuffix      = ".jsonl.lock"
	sessionLeaseLockSidecarSuffix = ".jsonl.lease.lock"
	sessionLeaseInfoSidecarSuffix = ".jsonl.lease.json"
	guardianSidecarSuffix         = ".guardian.jsonl"
	// nameMaxBytes is the single-component filename limit shared by the
	// filesystems Reasonix targets (APFS, ext4, NTFS all cap at 255).
	nameMaxBytes = 255
	// maxSessionBasenameBytes bounds transcript basenames that reconciliation
	// leaves in place. Sidecars append up to ~16 bytes to the transcript name
	// or its stem (".lease.lock", ".cleanup-pending.json", ".guardian.jsonl"),
	// so 224 keeps every sidecar comfortably under nameMaxBytes with headroom
	// for future suffixes. Names past this bound come from the pre-bounded
	// recovery cascade and get renamed by reconcileOverlongSessionFilenames.
	maxSessionBasenameBytes = 224
)

var (
	sessionSaveLocks sync.Map
	// sessionFileLockWait bounds cross-process save-lock acquisition. Session
	// leases normally prevent competing writers, but CLI/legacy writers and a
	// stalled process can still hold the compatibility .lock file. Navigation
	// and desktop shutdown snapshot synchronously; waiting forever here wedges
	// the UI and keeps the session lease (and WebView) alive indefinitely.
	// Package vars let focused tests shorten the wait without slowing the suite.
	sessionFileLockWait         = 5 * time.Second
	sessionFileLockPollInterval = 25 * time.Millisecond
	sessionMetaLockWait         = 5 * time.Second
	ErrSessionSnapshotConflict  = errors.New("session snapshot conflicts with newer transcript")
	// ErrSessionExternallyRemoved means a live Session still has a verified
	// baseline for path, but every authoritative transcript artifact disappeared.
	// Treating it as a first save would silently recreate a file the user or an
	// external cleanup tool deliberately removed.
	ErrSessionExternallyRemoved = errors.New("session was removed while still open")
	ErrSessionRecoveryNotNeeded = errors.New("session recovery not needed")
	// ErrSessionFileLockHeld reports that another process kept the
	// compatibility save lock for the full bounded acquisition window. Callers
	// that are about to terminate can use this sentinel to persist a recovery
	// branch without waiting on the same stalled file again.
	ErrSessionFileLockHeld = errors.New("session file lock held")
	// ErrSessionRecoveryDepthExceeded is retained for older callers. New
	// recovery writes update one stable branch and no longer return it.
	ErrSessionRecoveryDepthExceeded = errors.New("session recovery chain depth exceeded")
	sessionWriterID                 = newSessionWriterID()
)

// SessionRecoveryMaxDepth is the historical nested-fork cap. New writes stamp
// RecoveryDepth=1 and update one stable path instead of deepening a chain.
const SessionRecoveryMaxDepth = 3

type sessionPersistState struct {
	projectionPending bool
	path              string
	digest            [sha256.Size]byte
	version           uint64
	revision          int64
	// revisionKnown marks revision as a real ledger value. It is false when
	// the baseline was established while the meta sidecar was unreadable
	// (torn or corrupt): the session must still open, but revision 0 must not
	// pose as a baseline or every honest on-disk revision would read as a
	// stale-runtime conflict. CAS checks fall back to digest+version until a
	// successful save re-learns the revision.
	revisionKnown bool
	// saveVerified marks a baseline established by a completed save in this
	// process, whose write path verified transcript and ledger agree. A
	// baseline adopted at load time pairs the disk transcript with whatever
	// the meta sidecar said — which can lag the transcript after an
	// interrupted save — so only save-verified baselines may arm the
	// snapshot no-op fast path; the first save after a load must run in full
	// and heal a stale ledger.
	saveVerified bool
	ok           bool
}

type sessionSaveMode int

const (
	sessionSaveSnapshot sessionSaveMode = iota
	sessionSaveRewrite
	sessionSaveRewriteCompact
	sessionSaveToolCheckpoint
)

type snapshotWriteDecision struct {
	revision         int64
	reservedRevision int64
	upToDate         bool
	appendFrom       int
	appendOnly       bool
	// repairLog is set when the on-disk event log was damaged (torn tail with
	// a lost suffix, or nothing decodable): the safe write shape is a full
	// rewrite that also compacts the log back to a healthy single event.
	repairLog bool
	// ledgerStale is set when the on-disk transcript already matches the
	// snapshot but the meta ledger still describes older content — the
	// aftermath of a save whose bytes landed and whose revision record then
	// failed. The up-to-date path must heal the ledger instead of skipping it.
	ledgerStale bool
}

type SessionSnapshotConflictKind string

const (
	SessionSnapshotConflictStalePrefix SessionSnapshotConflictKind = "stale_prefix"
	SessionSnapshotConflictDiverged    SessionSnapshotConflictKind = "diverged"
)

type SessionSnapshotConflictError struct {
	Path             string
	Kind             SessionSnapshotConflictKind
	ExistingMessages int
	SnapshotMessages int
	BaseRevision     int64
	DiskRevision     int64
}

func (e *SessionSnapshotConflictError) Error() string {
	if e == nil {
		return ErrSessionSnapshotConflict.Error()
	}
	switch e.Kind {
	case SessionSnapshotConflictStalePrefix:
		return fmt.Sprintf("%s: %s has %d messages at revision %d; stale snapshot has %d messages from revision %d",
			ErrSessionSnapshotConflict, e.Path, e.ExistingMessages, e.DiskRevision, e.SnapshotMessages, e.BaseRevision)
	default:
		return fmt.Sprintf("%s: %s diverged on disk (%d messages, revision %d) from snapshot (%d messages, revision %d)",
			ErrSessionSnapshotConflict, e.Path, e.ExistingMessages, e.DiskRevision, e.SnapshotMessages, e.BaseRevision)
	}
}

func (e *SessionSnapshotConflictError) Unwrap() error {
	return ErrSessionSnapshotConflict
}

func SnapshotConflictKind(err error) (SessionSnapshotConflictKind, bool) {
	var conflict *SessionSnapshotConflictError
	if errors.As(err, &conflict) && conflict != nil {
		return conflict.Kind, true
	}
	return "", false
}

const RecoveryBranchDefaultName = "Recovered unsaved changes from stale runtime"

type RecoveryBranchOptions struct {
	OriginalPath string
	Name         string
	Reason       string
	BranchMeta   BranchMeta
	BaseRevision int64
	DiskRevision int64
}

type RecoveryBranchInfo struct {
	Path     string
	Digest   string
	Existing bool
	Meta     BranchMeta
	Preview  string
	Turns    int
}

// Save persists the session using the normal CAS-protected snapshot protocol.
// It is kept as the convenient default for callers that do not need to spell
// out rewrite intent; it must never provide a force-overwrite escape hatch.
// The .jsonl file remains as a compatibility checkpoint and discovery anchor;
// the append-only event log is authoritative once present, while the .jsonl
// checkpoint also serves as the random-read model for history paging.
func (s *Session) Save(path string) error {
	return s.saveObserved(path, sessionSaveSnapshot)
}

// SaveSnapshot writes a normal autosave/snapshot only when doing so cannot hide
// a newer transcript already on disk. Explicit history rewrites such as rewind,
// compaction, and cancel recovery should call SaveRewrite instead.
func (s *Session) SaveSnapshot(path string) error {
	return s.saveObserved(path, sessionSaveSnapshot)
}

// SaveRewrite writes an intentional non-append history rewrite only while this
// Session still owns the current on-disk transcript baseline. It prevents a
// stale controller from force-rewinding a newer transcript written elsewhere.
func (s *Session) SaveRewrite(path string) error {
	return s.saveObserved(path, sessionSaveRewrite)
}

// SaveRewriteCompact performs a CAS-protected rewrite and folds the event log
// to one replace record. It is for destructive maintenance such as redaction:
// retaining old WAL records would keep the removed bytes recoverable on disk.
func (s *Session) SaveRewriteCompact(path string) error {
	return s.saveObserved(path, sessionSaveRewriteCompact)
}

func (s *Session) save(path string, mode sessionSaveMode) error {
	if path == "" {
		return fmt.Errorf("empty session path")
	}
	return s.withSessionSaveLocks(path, func() error {
		return s.saveLocked(path, mode)
	})
}

func (s *Session) withSessionSaveLocks(path string, fn func() error) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("empty session path")
	}
	unlock := lockSessionSavePath(path)
	defer unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}
	unlockFile, err := lockSessionFile(path)
	if err != nil {
		return fmt.Errorf("lock session file: %w", err)
	}
	defer unlockFile()
	return fn()
}

func sessionArtifactExists(path string) bool {
	if _, err := os.Lstat(path); err == nil {
		return true
	}
	for _, artifact := range store.SessionSidecarFiles(path) {
		// A legacy v0 event transcript can share the native
		// `<id>.events.jsonl` name with the destination being imported. It is
		// intentionally left in place, and must not make SaveIfAbsent believe
		// that the v1 `<id>.jsonl` destination already exists. Native event logs
		// remain owned artifacts and still protect the destination from a second
		// writer.
		if artifact == store.SessionEventLog(path) {
			probe, probeErr := probeSessionEventLog(path)
			if probeErr != nil {
				return true
			}
			if !probe.native {
				continue
			}
		}
		if _, err := os.Lstat(artifact); err == nil {
			return true
		}
	}
	return false
}

func (s *Session) saveLocked(path string, mode sessionSaveMode) error {
	baseRevision := int64(0)
	releaseAuth, err := s.requireWriteAuthorityForSave(path)
	if err != nil {
		return err
	}
	defer releaseAuth()
	observeUnleasedSessionWrite(path, mode)
	// Heal an empty/missing checkpoint from a valid WAL before classification
	// so a 0-byte .jsonl never forces a false diverged recovery.
	if err := healEmptyCheckpointFromWAL(path); err != nil {
		return err
	}
	if mode == sessionSaveSnapshot && s.snapshotUpToDate(path) {
		// Nothing changed since the last successful save to this exact path:
		// skip the rest of the save — including the full transcript serialize
		// + digest + disk probe the up-to-date decision below would still
		// pay. Desktop switch/close/prune paths snapshot defensively on every
		// navigation, and on large sessions that per-save cost is the
		// user-visible seconds of UI freeze in #6607. Version bookkeeping
		// makes this exact: any Add/Replace/preview update bumps version, any
		// rewrite bumps rewriteVersion, and load-time repairs or log damage
		// disarm the fast path until a real save persists them. The check
		// runs under the save locks, not before them, so a saver that waited
		// on a concurrent writer still re-evaluates against the state it must
		// persist when it finally enters the critical section.
		//
		// An up-to-date transcript does not imply up-to-date derived files: a
		// tool checkpoint commits the transcript but defers the listing and
		// display indexes. Republish them here instead of paying for the full
		// save above just to rebuild a derived index. A log whose derived files
		// need more than that (schema 2 also derives its head index and meta
		// mirror) declines and falls through unchanged.
		if s.flushDeferredDerivedFiles(path) {
			return nil
		}
	}
	// Capture the snapshot only while holding the save locks. Concurrent
	// in-process savers (turn-end snapshot, periodic autosave, shutdown
	// snapshot) that captured before locking could land out of order: the
	// stalest capture written last would then read the newer transcript it
	// lost the race to as a bogus stale-prefix conflict.
	msgs, version, rewriteVersion := s.snapshotWithVersion()
	probe, err := probeLogForSave(path)
	if err != nil {
		return err
	}
	if route := s.dagSaveRoute(path, probe); route != dagRouteSchemaOne {
		digest, err := s.snapshotDigest(path, msgs, version)
		if err != nil {
			return err
		}
		return s.saveDAGLocked(path, mode, route, msgs, version, rewriteVersion, digest)
	}
	digest, contentBytes, err := digestAndSizeSessionMessages(msgs)
	if err != nil {
		return err
	}
	repairLog := false
	deferProjection := mode.defersProjection()
	ownedRewrite := mode.allowsOwnedRewrite()
	decision, err := s.classifySnapshotWriteForCommit(path, msgs, digest, version, ownedRewrite, mode)
	if err != nil {
		return err
	}
	if decision.upToDate && mode != sessionSaveRewriteCompact {
		// Disk already holds exactly this transcript. Rewriting it would only
		// bump the revision, invalidating the persistence baseline of every
		// other runtime resumed on this file and turning their next
		// legitimate save into a stale-runtime conflict. Skip the write and
		// adopt the current on-disk revision as this session's baseline.
		if decision.ledgerStale {
			// ...unless the ledger never learned about this transcript: a
			// prior save landed its bytes and then failed to record the
			// revision. Same-content retries are exactly the "later save"
			// that failure deferred to, and skipping here would strand the
			// ledger on the old digest forever. Record now, reproducing
			// the state the interrupted save would have left.
			revision, err := recordSessionContentRevision(path, digest, decision.revision, decision.reservedRevision)
			if err != nil {
				return err
			}
			displayModelCurrent := true
			if err := writeSessionMessages(path, msgs); err != nil {
				displayModelCurrent = false
				slog.Warn("session: keeping save after display read-model repair failure", "path", path, "err", err)
			}
			if probe.native {
				if err := writeSessionEventIndex(path, msgs, digest, revision); err != nil {
					// See the append path below: index loss must not fail a
					// save whose transcript and revision already landed.
					slog.Warn("session: keeping save after event index write failure", "path", path, "err", err)
				}
			}
			if displayModelCurrent {
				if err := refreshCheckpointDisplayIndex(path, msgs, digest, revision, -1, deferProjection); err != nil {
					// The display index is a derived sidecar; transcript durability
					// must not depend on rebuilding it successfully.
					slog.Warn("session: keeping save after display index write failure", "path", path, "err", err)
				}
			}
			s.markCheckpointPersisted(path, digest, version, revision, rewriteVersion, msgs, deferProjection)
			return nil
		}
		s.refreshPendingCheckpointProjection(path, msgs, digest, decision.revision, deferProjection)
		s.markCheckpointPersisted(path, digest, version, decision.revision, rewriteVersion, msgs, deferProjection)
		return nil
	}
	if decision.appendOnly && probe.native && mode != sessionSaveRewriteCompact {
		logSize := sessionEventLogSize(path)
		displayModelCurrent := false
		switch {
		case logSize == 0:
			if err := appendSessionReplaceEvent(path, msgs, digest, decision.revision, "snapshot"); err != nil {
				return err
			}
			displayModelCurrent, err = appendSessionDisplayReadModel(path, msgs, decision.appendFrom, decision.revision)
		case sessionEventLogOversized(logSize, contentBytes):
			// Fold history into one replace event and refresh the random-read
			// model atomically. Normal appends keep it current below too.
			if err := compactSessionEventLog(path, msgs, digest, decision.revision, "compact"); err != nil {
				return err
			}
			if err := writeSessionMessages(path, msgs); err != nil {
				return err
			}
			displayModelCurrent = true
		default:
			if err := appendSessionAppendEvent(path, decision.appendFrom, msgs[decision.appendFrom:], digest, decision.revision); err != nil {
				return err
			}
			displayModelCurrent, err = appendSessionDisplayReadModel(path, msgs, decision.appendFrom, decision.revision)
		}
		if err != nil {
			slog.Warn("session: keeping save after display read-model append failure", "path", path, "err", err)
		}
		revision, err := recordSessionContentRevision(path, digest, decision.revision, decision.reservedRevision)
		if err != nil {
			return err
		}
		if err := writeSessionEventIndex(path, msgs, digest, revision); err != nil {
			// The event index is only a listing accelerator; the transcript
			// and its revision are already durable above. Failing the save
			// here would skip markPersisted and leave the in-memory baseline
			// behind the disk state it just wrote, misreading the next save
			// as a stale-runtime conflict.
			slog.Warn("session: keeping save after event index write failure", "path", path, "err", err)
		}
		if displayModelCurrent {
			if err := refreshCheckpointDisplayIndex(path, msgs, digest, revision, decision.appendFrom, deferProjection); err != nil {
				// The append boundary lets the refresh extend the previous index
				// instead of re-encoding the whole transcript.
				slog.Warn("session: keeping save after display index write failure", "path", path, "err", err)
			}
		}
		s.markCheckpointPersisted(path, digest, version, revision, rewriteVersion, msgs, deferProjection)
		return nil
	}
	baseRevision = decision.revision
	repairLog = decision.repairLog
	// Full-rewrite path: new snapshots, intentional history rewrites, and
	// damage repairs. The event log mutates first so a crash between the two
	// writes leaves the newer transcript authoritative; the anchor rewrite
	// keeps the compatibility .jsonl fresh for direct readers.
	reason := mode.eventReason()
	if repairLog {
		reason = "repair"
	}
	logSize := sessionEventLogSize(path)
	switch {
	case !probe.native:
		// A foreign file (legacy import leftover) squats the native log path.
		// Never write into or over it — the session stays checkpoint-only.
	case mode == sessionSaveRewriteCompact:
		// Maintenance rewrites compact even a short log so redacted or otherwise
		// removed bytes do not remain recoverable in historical WAL records.
		if err := compactSessionEventLog(path, msgs, digest, baseRevision, reason); err != nil {
			return err
		}
	case repairLog, sessionEventLogOversized(logSize, contentBytes):
		if err := compactSessionEventLog(path, msgs, digest, baseRevision, reason); err != nil {
			return err
		}
	default:
		if err := appendSessionReplaceEvent(path, msgs, digest, baseRevision, reason); err != nil {
			return err
		}
	}
	if err := writeSessionMessages(path, msgs); err != nil {
		return err
	}
	revision, err := recordSessionContentRevision(path, digest, baseRevision, decision.reservedRevision)
	if err != nil {
		return err
	}
	if probe.native {
		if err := writeSessionEventIndex(path, msgs, digest, revision); err != nil {
			// See the append path above: index loss must not fail a save whose
			// transcript and revision already landed.
			slog.Warn("session: keeping save after event index write failure", "path", path, "err", err)
		}
	}
	if err := refreshCheckpointDisplayIndex(path, msgs, digest, revision, -1, deferProjection); err != nil {
		// Warn-only like the event index above: the display index is a pure
		// derived sidecar and must never fail a save.
		slog.Warn("session: keeping save after display index write failure", "path", path, "err", err)
	}
	s.markCheckpointPersisted(path, digest, version, revision, rewriteVersion, msgs, deferProjection)
	return nil
}

// checkSnapshotWrite decides whether this session may write msgs over path, and
// whether the safe write shape is a no-op, append-only suffix, or full rewrite.
func (s *Session) checkSnapshotWrite(path string, next []provider.Message, nextDigest [sha256.Size]byte, nextVersion uint64, allowOwnedRewrite bool) (snapshotWriteDecision, error) {
	baseState := s.persistState(path)
	current, err := loadSessionUnlocked(path)
	if err != nil {
		if os.IsNotExist(err) {
			if baseState.ok {
				return snapshotWriteDecision{}, ErrSessionExternallyRemoved
			}
			return snapshotWriteDecision{}, nil
		}
		return snapshotWriteDecision{}, err
	}
	currentRevision, currentLedgerDigest, err := sessionContentRevision(path)
	if err != nil {
		return snapshotWriteDecision{}, err
	}
	existing := current.Snapshot()
	existingDigest, err := digestSessionMessages(existing)
	if err != nil {
		return snapshotWriteDecision{}, err
	}
	// raw is the transcript as stored, before load-time normalization repaired
	// it; it equals existing when no repair ran. The prefix checks below must
	// be able to fall back to it: a mid-turn snapshot legitimately cuts an
	// assistant tool call from its still-running result, normalization then
	// fabricates a placeholder answer on load, and the live session's real
	// result collides with that placeholder — misreading a pure append as
	// divergence (and forking a bogus recovery branch).
	raw, rawDigest := existing, existingDigest
	rawDiffers := current.normalizedDirty && len(current.rawMessages) > 0
	if rawDiffers {
		raw = current.rawMessages
		if rawDigest, err = digestSessionMessages(raw); err != nil {
			return snapshotWriteDecision{}, err
		}
	}
	contentUnchanged := bytes.Equal(existingDigest[:], nextDigest[:])
	exactAppend := messagesHavePrefix(next, existing)
	appendShaped := contentUnchanged || exactAppend || messagesHavePrefixWithCompatibleSystem(next, existing)
	repairPending := current.normalizedDirty
	if !appendShaped && rawDiffers {
		rawUnchanged := bytes.Equal(rawDigest[:], nextDigest[:])
		rawAppend := messagesHavePrefix(next, raw)
		if rawUnchanged || rawAppend || messagesHavePrefixWithCompatibleSystem(next, raw) {
			existing = raw
			contentUnchanged = rawUnchanged
			exactAppend = rawAppend
			appendShaped = true
			// The snapshot supersedes the repaired view — appending it lands
			// the real tool results where the placeholders were fabricated —
			// so no load-time repair is left to force a rewrite.
			repairPending = false
		}
	}
	if !appendShaped && baseState.ok && baseState.revisionKnown &&
		baseState.revision == currentRevision && !contentUnchanged {
		// Revision equality alone is not ownership proof. Require digest
		// ancestry or a live generation-bound write authority covering path.
		if s.ownsWritableBaseline(path, existingDigest, rawDigest, rawDiffers, currentRevision, currentLedgerDigest, nextVersion) {
			appendShaped = true
		}
	}
	if appendShaped {
		// An unknown-revision baseline (meta sidecar unreadable at load) cannot
		// vouch for revision equality; the digest/prefix checks above already
		// vouch for the content, so only a known baseline arms the CAS check.
		// Under an append-shaped write (at most a compatible leading-system
		// swap) a stale revision is ledger drift — a reset sidecar, a
		// same-content heal, or another runtime recording messages this
		// snapshot already contains — unless the transcript was rewound.
		// Locating the persisted baseline among the snapshot's prefixes and
		// requiring the disk transcript to still reach it tells the two apart:
		// drift keeps the baseline reachable, while a rewind cut below it and
		// appending would resurrect the suffix another runtime removed.
		if baseState.ok && baseState.revisionKnown && currentRevision != baseState.revision && !contentUnchanged &&
			!appendCoversPersistedBaseline(next, existing, baseState.digest) {
			return snapshotWriteDecision{}, snapshotConflict(path, existing, next, baseState.revision, currentRevision)
		}
		// A normalized-dirty load means LoadSession repaired the history on the
		// way in: the digests match but the raw bytes on disk do not, so the
		// repair still needs a real write to persist. A damaged event log
		// likewise needs a real write (rewrite + compact) even when the
		// replayable prefix already matches this snapshot.
		decision := snapshotWriteDecision{
			revision:  currentRevision,
			upToDate:  contentUnchanged && !repairPending && !current.eventLogDamaged,
			repairLog: current.eventLogDamaged,
		}
		// A ledger digest that describes different content than the transcript
		// on disk is the aftermath of a save whose bytes landed and whose
		// revision record then failed (crash or fail-closed record between the
		// two writes). Only a non-empty mismatch counts: a missing sidecar or
		// a legacy one without a digest is a legitimate state, and stamping it
		// here would bump revisions other runtimes still hold as baselines.
		if decision.upToDate && currentLedgerDigest != "" && currentLedgerDigest != digestString(nextDigest) {
			decision.ledgerStale = true
		}
		// An append is only chain-safe when existing measures the transcript
		// the event log actually replays. Under a pending load-time repair the
		// normalized view differs from the raw log, so an append event indexed
		// against it breaks the replay chain and orphans the appended suffix;
		// fall through to the full rewrite, which also persists the repair.
		if exactAppend && !contentUnchanged && len(existing) < len(next) && !current.eventLogDamaged && !repairPending {
			decision.appendOnly = true
			decision.appendFrom = len(existing)
		}
		return decision, nil
	}
	if allowOwnedRewrite {
		if s.ownsWritableBaseline(path, existingDigest, rawDigest, rawDiffers, currentRevision, currentLedgerDigest, nextVersion) {
			return snapshotWriteDecision{revision: currentRevision, repairLog: current.eventLogDamaged}, nil
		}
	}
	// Bound controllers: missing/stale authority must not fork recovery.
	if err := s.authorityErrorForPath(path); err != nil {
		return snapshotWriteDecision{}, err
	}
	if messagesHavePrefix(existing, next) || messagesHavePrefixWithCompatibleSystem(existing, next) ||
		(rawDiffers && (messagesHavePrefix(raw, next) || messagesHavePrefixWithCompatibleSystem(raw, next))) {
		return snapshotWriteDecision{}, &SessionSnapshotConflictError{
			Path:             path,
			Kind:             SessionSnapshotConflictStalePrefix,
			ExistingMessages: len(existing),
			SnapshotMessages: len(next),
			BaseRevision:     baseState.revision,
			DiskRevision:     currentRevision,
		}
	}
	return snapshotWriteDecision{}, &SessionSnapshotConflictError{
		Path:             path,
		Kind:             SessionSnapshotConflictDiverged,
		ExistingMessages: len(existing),
		SnapshotMessages: len(next),
		BaseRevision:     baseState.revision,
		DiskRevision:     currentRevision,
	}
}

func snapshotConflict(path string, existing, next []provider.Message, baseRevision, diskRevision int64) error {
	kind := SessionSnapshotConflictDiverged
	if messagesHavePrefix(existing, next) || messagesHavePrefixWithCompatibleSystem(existing, next) {
		kind = SessionSnapshotConflictStalePrefix
	}
	return &SessionSnapshotConflictError{
		Path:             path,
		Kind:             kind,
		ExistingMessages: len(existing),
		SnapshotMessages: len(next),
		BaseRevision:     baseRevision,
		DiskRevision:     diskRevision,
	}
}

func (s *Session) SaveRecoveryBranch(opts RecoveryBranchOptions) (RecoveryBranchInfo, error) {
	return s.saveRecoveryBranch(opts, false)
}

// SaveShutdownRecoveryBranch persists the current transcript to a distinct
// recovery branch after the normal shutdown snapshot failed with
// ErrSessionFileLockHeld. It deliberately does not re-lock or inspect the
// original session file: doing so would repeat the same bounded timeout and
// let process teardown discard the only remaining in-memory copy.
//
// The recovery filename includes this live Session's isolated lane, so another
// controller in the same process cannot replace the emergency copy.
// The result still uses the normal session, event-log, and branch-meta formats
// and is therefore discoverable and resumable through existing flows.
func (s *Session) SaveShutdownRecoveryBranch(opts RecoveryBranchOptions) (RecoveryBranchInfo, error) {
	return s.saveRecoveryBranch(opts, true)
}

// SaveConflictRecoveryBranch writes the depth-cap isolated copy (one path per
// live Session). Subsequent conflicts from that Session rewrite it in place.
func (s *Session) SaveConflictRecoveryBranch(opts RecoveryBranchOptions) (RecoveryBranchInfo, error) {
	return s.saveRecoveryBranch(opts, true)
}

func (s *Session) saveRecoveryBranch(opts RecoveryBranchOptions, shutdown bool) (RecoveryBranchInfo, error) {
	originalPath := strings.TrimSpace(opts.OriginalPath)
	if originalPath == "" {
		return RecoveryBranchInfo{}, fmt.Errorf("empty original session path")
	}
	opts.OriginalPath = originalPath
	msgs, version, rewriteVersion := s.snapshotWithVersion()
	preview, turns := SessionPreviewFromMessages(msgs)
	if turns == 0 {
		return RecoveryBranchInfo{}, ErrSessionRecoveryNotNeeded
	}
	digest, err := digestSessionMessages(msgs)
	if err != nil {
		return RecoveryBranchInfo{}, err
	}
	digestText := digestString(digest)

	if !shutdown {
		unlockOriginal := lockSessionSavePath(originalPath)
		unlockOriginalFile, lockErr := lockSessionFile(originalPath)
		if lockErr != nil {
			unlockOriginal()
			return RecoveryBranchInfo{}, fmt.Errorf("lock original session file: %w", lockErr)
		}
		current, loadErr := loadSessionUnlocked(originalPath)
		unlockOriginalFile()
		unlockOriginal()
		if loadErr != nil && !os.IsNotExist(loadErr) {
			return RecoveryBranchInfo{}, loadErr
		}
		if loadErr == nil && current != nil {
			existing := current.Snapshot()
			existingDigest, digestErr := digestSessionMessages(existing)
			if digestErr != nil {
				return RecoveryBranchInfo{}, digestErr
			}
			covered := bytes.Equal(existingDigest[:], digest[:]) ||
				messagesHavePrefix(existing, msgs) ||
				messagesHavePrefixWithCompatibleSystem(existing, msgs)
			if !covered && current.normalizedDirty && len(current.rawMessages) > 0 {
				// Judge coverage against the pre-repair transcript too, for the
				// same reason as checkSnapshotWrite: load-time normalization can
				// reshape what is actually stored, and a recovery fork is only
				// warranted when the stored bytes themselves fail to cover this
				// snapshot.
				raw := current.rawMessages
				rawDigest, rawErr := digestSessionMessages(raw)
				if rawErr != nil {
					return RecoveryBranchInfo{}, rawErr
				}
				covered = bytes.Equal(rawDigest[:], digest[:]) ||
					messagesHavePrefix(raw, msgs) ||
					messagesHavePrefixWithCompatibleSystem(raw, msgs)
			}
			if covered {
				return RecoveryBranchInfo{}, ErrSessionRecoveryNotNeeded
			}
		}
	}

	// One stable recovery file per (root branch, writer generation). Nested
	// -recovery- names peel back to the root so conflicts update in place.
	for range 8 {
		recoveryPath, lane := s.isolatedRecoverySessionPath(originalPath)
		info, collision, err := s.writeRecoveryBranchAtPath(recoveryPath, opts, msgs, digest,
			version, rewriteVersion, preview, turns, digestText, 1, shutdown)
		if err != nil {
			return RecoveryBranchInfo{}, err
		}
		if !collision {
			return info, nil
		}
		s.rotateRecoveryLane(lane)
	}
	return RecoveryBranchInfo{}, fmt.Errorf("allocate isolated recovery lane: too many existing collisions")
}

func recoveryParentStem(parent string) string {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return "session"
	}
	sum := sha256.Sum256([]byte(parent))
	if before, _, ok := strings.Cut(parent, "-recovery-"); ok {
		base := strings.Trim(before, "-_. ")
		if base == "" {
			base = "session"
		}
		base = strings.Trim(truncateUTF8Bytes(base, maxRecoveryParentStemBytes), "-_. ")
		if base == "" {
			base = "session"
		}
		return fmt.Sprintf("%s-%x", base, sum[:6])
	}
	if len(parent) <= maxRecoveryParentStemBytes {
		return parent
	}
	prefix := strings.Trim(truncateUTF8Bytes(parent, maxRecoveryParentStemBytes), "-_. ")
	if prefix == "" {
		prefix = "session"
	}
	return fmt.Sprintf("%s-%x", prefix, sum[:6])
}

func truncateUTF8Bytes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	used := 0
	for i, r := range s {
		size := utf8.RuneLen(r)
		if size < 0 {
			size = 1
		}
		if used+size > max {
			return s[:i]
		}
		used += size
	}
	return s
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (s *Session) ownsPersistedState(path string, existingDigest [sha256.Size]byte, existingRevision int64, existingLedgerDigest string, nextVersion uint64) bool {
	state := s.persistState(path)
	if !state.ok || state.version > nextVersion || !bytes.Equal(existingDigest[:], state.digest[:]) {
		return false
	}
	// An unknown-revision baseline still owns the transcript it loaded — the
	// digest+version match proves it. Requiring revision equality here would
	// make every rewrite from such a baseline a permanent conflict, because
	// the revision can only be re-learned by a successful save.
	// A disk ledger with no recorded revision is the mirror case: recorded
	// revisions start at 1, so revision 0 means the sidecar was deleted or
	// rebuilt by a listing-only writer after this session's save. An absent
	// claim cannot revoke the ownership the digest+version match proves.
	if !state.revisionKnown || existingRevision == 0 || state.revision == existingRevision {
		return true
	}
	// A foreign revision stamp whose recorded digest still describes these
	// exact bytes (a same-content heal or no-op record by another runtime)
	// vouches for no content of its own: the transcript is byte-for-byte what
	// this session last persisted, so rewriting it destroys nothing of
	// theirs — at worst the conflict moves to the stamper's next divergent
	// save, where its in-memory history forks a recovery branch as usual.
	// A stamp that disagrees with the on-disk transcript (or a legacy stamp
	// with no digest) keeps revoking ownership: that is the aftermath of a
	// save whose bytes and record split, the bytes cannot be attributed, and
	// only the conservative conflict path preserves both sides.
	return existingLedgerDigest == digestString(existingDigest)
}

// snapshotUpToDate reports whether a snapshot save to path is a provable
// no-op from in-memory bookkeeping alone: the last successful save went to
// this same path with a known ledger revision, the transcript version and
// rewrite version have not moved since, and no load-time repair or event-log
// damage is waiting to be persisted. Every one of these flags fails open —
// when any is unset or stale the caller falls through to the full save path,
// which re-derives the truth from disk.
//
// persisted.projectionPending is deliberately not part of this decision. It
// reports derived files (listing sidecar, display cache) that a tool
// checkpoint or an unlocked shutdown append left to a later save; the
// transcript bytes themselves are already committed, and this baseline
// describes exactly those bytes. Letting it veto the no-op would push a
// defensive switch/close snapshot on a large session through the serialize +
// digest + probe path this fast path exists to avoid (#6607) merely to
// republish a derived index. The caller republishes those derived files
// directly instead — see flushDeferredDerivedFiles.
func (s *Session) snapshotUpToDate(path string) bool {
	key := canonicalSessionSavePath(path)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.persisted.ok &&
		s.persisted.saveVerified &&
		s.persisted.path == key &&
		s.persisted.version == s.version &&
		s.persisted.revisionKnown &&
		s.rewriteVersion == s.persistedRewriteVersion &&
		!s.normalizedDirty &&
		!s.eventLogDamaged
}

func (s *Session) persistState(path string) sessionPersistState {
	key := canonicalSessionSavePath(path)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.persisted.ok && s.persisted.path == key {
		return s.persisted
	}
	return sessionPersistState{}
}

// PersistedState is a read-only view of the baseline the session last
// persisted to (or loaded from) its transcript path. History paging uses it to
// validate a display-index sidecar against the live session without touching
// disk: an index built at the same revision with the same content digest
// describes exactly the persisted prefix of the in-memory log.
type PersistedState struct {
	// Digest is the content digest of the persisted transcript.
	Digest [sha256.Size]byte
	// DigestHex is Digest in the hex form sidecars store.
	DigestHex string
	// Revision is the CAS ledger revision of the persisted transcript. It is
	// meaningful only when RevisionKnown is true.
	Revision      int64
	RevisionKnown bool
	// RewriteEpoch is the highest rewriteVersion that has reached disk. It
	// changes only when a content rewrite (compaction, rewind, …) is saved, so
	// it doubles as a stable epoch token for history entry IDs: append-only
	// saves keep it, rewrites bump it.
	RewriteEpoch int
	// AppendOnlyTail reports that no rewrite landed after the baseline, so the
	// persisted transcript is still a prefix of the in-memory log (the tail,
	// if any, is unsaved appends).
	AppendOnlyTail bool
	// UnchangedSincePersisted reports that the in-memory log is exactly the
	// persisted transcript (no appends, no rewrites since the baseline).
	UnchangedSincePersisted bool
}

// PersistedState returns the session's persistence baseline for path, or
// false when the session has never persisted to (or loaded from) that path.
func (s *Session) PersistedState(path string) (PersistedState, bool) {
	key := canonicalSessionSavePath(path)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.persisted.ok || s.persisted.path != key {
		return PersistedState{}, false
	}
	return PersistedState{
		Digest:                  s.persisted.digest,
		DigestHex:               digestString(s.persisted.digest),
		Revision:                s.persisted.revision,
		RevisionKnown:           s.persisted.revisionKnown,
		RewriteEpoch:            s.persistedRewriteVersion,
		AppendOnlyTail:          s.rewriteVersion == s.persistedRewriteVersion,
		UnchangedSincePersisted: s.persisted.version == s.version,
	}, true
}

// SessionContentIdentity returns the ledger identity of the authoritative
// persisted transcript without loading its message bodies. It is intended for
// derived sidecars such as the desktop display index: a matching checkpoint
// size alone cannot prove that offsets still describe the event-log-backed
// transcript. The bool is false for legacy sessions that have no digest in
// their branch metadata; callers must then validate against the transcript
// bytes directly.
func SessionContentIdentity(path string) (PersistedState, bool, error) {
	revision, digestHex, err := sessionContentRevision(path)
	if err != nil {
		return PersistedState{}, false, err
	}
	if digestHex == "" {
		return PersistedState{}, false, nil
	}
	var digest [sha256.Size]byte
	decoded, err := hex.DecodeString(digestHex)
	if err != nil || len(decoded) != len(digest) {
		return PersistedState{}, false, fmt.Errorf("invalid session content digest")
	}
	copy(digest[:], decoded)
	return PersistedState{
		Digest:        digest,
		DigestHex:     digestString(digest),
		Revision:      revision,
		RevisionKnown: true,
	}, true, nil
}

// sessionContentRevision reads the CAS ledger (revision + content digest) from
// the branch-meta sidecar. A missing sidecar is revision 0 — a session that
// has never recorded one. An unreadable sidecar is an error: reporting it as
// revision 0 would desync every runtime baseline from the ledger and turn the
// next honest save into a bogus conflict (and a recovery branch).
func sessionContentRevision(path string) (int64, string, error) {
	meta, ok, err := loadBranchMetaRetry(path)
	if err != nil {
		return 0, "", err
	}
	if !ok {
		return 0, "", nil
	}
	return meta.Revision, strings.TrimSpace(meta.ContentDigest), nil
}

func recordSessionContentRevision(path string, digest [sha256.Size]byte, baseRevision, reservedRevision int64) (int64, error) {
	// Revision allocation is a read-modify-write transaction. Holding only the
	// final SaveBranchMeta lock would still let another writer replace the
	// sidecar between our read and write, so keep the ledger lock across the
	// increment and read-back as well.
	unlock, err := LockSessionMetaPath(path)
	if err != nil {
		return 0, err
	}
	defer unlock()

	meta, ok, err := loadBranchMetaRetry(path)
	if err != nil {
		// Fail the save instead of rebuilding the ledger from a bad read: the
		// transcript bytes already landed, so a later save can record the
		// revision once the sidecar reads cleanly again — a content-bearing
		// save lands here again, and a same-content retry heals through the
		// up-to-date ledgerStale path in save().
		return 0, err
	}
	if !ok {
		meta = BranchMeta{ID: BranchID(path)}
	}
	if reservedRevision > 0 {
		if reservedRevision != baseRevision+1 || meta.Revision != reservedRevision || meta.SchemaVersion != 0 {
			return 0, fmt.Errorf("session revision reservation changed: base=%d reserved=%d current=%d schema=%d", baseRevision, reservedRevision, meta.Revision, meta.SchemaVersion)
		}
	} else {
		if meta.Revision < baseRevision {
			meta.Revision = baseRevision
		}
		if meta.Revision == math.MaxInt64 {
			return 0, fmt.Errorf("session revision exhausted")
		}
		meta.Revision++
	}
	meta.ContentDigest = digestString(digest)
	meta.WriterID = SessionWriterID()
	if err := saveBranchMeta(path, meta, false); err != nil {
		return 0, err
	}
	stored, ok, err := loadBranchMetaRetry(path)
	if err != nil {
		return 0, err
	}
	if ok && stored.Revision > 0 {
		return stored.Revision, nil
	}
	return meta.Revision, nil
}

func digestString(digest [sha256.Size]byte) string {
	return fmt.Sprintf("%x", digest[:])
}

func SessionWriterID() string {
	return sessionWriterID
}

func newSessionWriterID() string {
	host, _ := os.Hostname()
	host = strings.TrimSpace(host)
	if host == "" {
		host = "unknown-host"
	}
	host = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, host)
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Sprintf("%s-%d-%d", host, os.Getpid(), time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-%d-%x", host, os.Getpid(), nonce[:])
}

func digestSessionMessages(msgs []provider.Message) ([sha256.Size]byte, error) {
	digest, _, err := digestAndSizeSessionMessages(msgs)
	return digest, err
}

func messageForSessionIdentity(m provider.Message) provider.Message {
	// CreatedAt and ID are local metadata. Keep them out of transcript identity
	// so older builds that ignore the optional fields can share the same event-
	// log revision and append without false conflicts.
	m.CreatedAt = 0
	m.ID = ""
	return m
}

// snapshotDigest returns the transcript digest a schema-2 save needs. When the
// persistence baseline already describes exactly this version — the same proof
// the snapshot no-op relies on — the stored digest is that value by
// construction, because it was written for this version and only a version or
// rewrite bump invalidates it. A schema-2 save only records this digest and
// publishes it into derived files; it never uses it to decide what to append
// (planDAGWrite diffs the transcript itself). Recomputing it instead costs a
// json.Marshal + sha256 pass over every message, which on a large session
// dominates a defensive switch/close snapshot.
func (s *Session) snapshotDigest(path string, msgs []provider.Message, version uint64) ([sha256.Size]byte, error) {
	if s.snapshotUpToDate(path) {
		if state := s.persistState(path); state.ok && state.version == version {
			return state.digest, nil
		}
	}
	digest, _, err := digestAndSizeSessionMessages(msgs)
	return digest, err
}

// digestAndSizeSessionMessages also reports the encoded transcript size, which
// the save path uses to bound the event log relative to the live content.
func digestAndSizeSessionMessages(msgs []provider.Message) ([sha256.Size]byte, int64, error) {
	h := sha256.New()
	size := int64(0)
	for _, m := range msgs {
		m = messageForSessionIdentity(m)
		b, err := json.Marshal(m)
		if err != nil {
			return [sha256.Size]byte{}, 0, err
		}
		if _, err := h.Write(b); err != nil {
			return [sha256.Size]byte{}, 0, err
		}
		if _, err := h.Write([]byte{'\n'}); err != nil {
			return [sha256.Size]byte{}, 0, err
		}
		size += int64(len(b)) + 1
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out, size, nil
}

// sessionTranscriptHasher accumulates the transcript digest one message at a
// time, exactly like digestAndSizeSessionMessages, so load paths hash during
// decode instead of re-serializing. A nil receiver disables hashing.
type sessionTranscriptHasher struct {
	h   hash.Hash
	err error
}

func newSessionTranscriptHasher() *sessionTranscriptHasher {
	return &sessionTranscriptHasher{h: sha256.New()}
}

// rehash restarts accumulation over msgs (event-log replace records).
func (s *sessionTranscriptHasher) rehash(msgs []provider.Message) {
	if s == nil {
		return
	}
	s.h.Reset()
	s.err = nil
	s.addAll(msgs)
}

func (s *sessionTranscriptHasher) addAll(msgs []provider.Message) {
	for _, m := range msgs {
		s.add(m)
	}
}

// add hashes one message and returns it for append chaining.
func (s *sessionTranscriptHasher) add(m provider.Message) provider.Message {
	if s == nil || s.err != nil {
		return m
	}
	b, err := json.Marshal(messageForSessionIdentity(m))
	if err != nil {
		s.err = err
		return m
	}
	_, _ = s.h.Write(b)
	_, _ = s.h.Write([]byte{'\n'})
	return m
}

// sum reports the accumulated digest; ok is false when hashing was disabled
// or a message failed to encode, and callers fall back to digestSessionMessages.
func (s *sessionTranscriptHasher) sum() (digest [sha256.Size]byte, ok bool) {
	if s == nil || s.err != nil {
		return digest, false
	}
	copy(digest[:], s.h.Sum(nil))
	return digest, true
}

func messagesHavePrefix(full, prefix []provider.Message) bool {
	if len(prefix) > len(full) {
		return false
	}
	for i := range prefix {
		if !messagesEqualForStorage(full[i], prefix[i]) {
			return false
		}
	}
	return true
}

// messagesPrefixDigestDepth returns the number of leading messages of msgs
// whose storage digest equals target, or -1 when no prefix matches. The
// digest accumulates exactly like digestAndSizeSessionMessages, so a match at
// depth k means msgs[:k] has the same transcript identity as target.
func messagesPrefixDigestDepth(msgs []provider.Message, target [sha256.Size]byte) int {
	h := sha256.New()
	sum := make([]byte, 0, sha256.Size)
	for i, m := range msgs {
		m = messageForSessionIdentity(m)
		b, err := json.Marshal(m)
		if err != nil {
			return -1
		}
		h.Write(b)
		h.Write([]byte{'\n'})
		sum = h.Sum(sum[:0])
		if bytes.Equal(sum, target[:]) {
			return i + 1
		}
	}
	return -1
}

// appendCoversPersistedBaseline reports whether an append-shaped write (disk
// transcript a prefix of next, modulo a compatible leading-system swap) still
// covers everything this session ever persisted: the baseline digest must be
// reachable as a prefix of the pending snapshot, and the disk transcript must
// still extend at least to that depth. A shorter disk transcript means some
// other runtime deliberately rewound below the baseline — appending over it
// would resurrect the removed suffix, so the caller must conflict instead.
func appendCoversPersistedBaseline(next, existing []provider.Message, baseDigest [sha256.Size]byte) bool {
	depth := messagesPrefixDigestDepth(next, baseDigest)
	if depth < 0 && len(next) > 0 && len(existing) > 0 &&
		next[0].Role == provider.RoleSystem && existing[0].Role == provider.RoleSystem &&
		!messagesEqualForStorage(next[0], existing[0]) {
		// A resume that swapped the system prompt persisted its baseline with
		// the previous system message — the one still on disk. Re-anchor the
		// search on that message so the swap alone doesn't hide the baseline.
		variant := append([]provider.Message{existing[0]}, next[1:]...)
		depth = messagesPrefixDigestDepth(variant, baseDigest)
	}
	return depth >= 0 && len(existing) >= depth
}

func messagesHavePrefixWithCompatibleSystem(full, prefix []provider.Message) bool {
	full = messagesWithoutLeadingSystem(full)
	prefix = messagesWithoutLeadingSystem(prefix)
	return messagesHavePrefix(full, prefix)
}

func messagesWithoutLeadingSystem(msgs []provider.Message) []provider.Message {
	if len(msgs) > 0 && msgs[0].Role == provider.RoleSystem {
		return msgs[1:]
	}
	return msgs
}

func messagesEqualForStorage(a, b provider.Message) bool {
	a = messageForSessionIdentity(a)
	b = messageForSessionIdentity(b)
	ab, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}

func messagesEqualForStorageList(a, b []provider.Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !messagesEqualForStorage(a[i], b[i]) {
			return false
		}
	}
	return true
}

func messagesCompatibleForStorageBaseline(a, b []provider.Message) bool {
	if messagesEqualForStorageList(a, b) {
		return true
	}
	return messagesEqualForStorageList(messagesWithoutLeadingSystem(a), messagesWithoutLeadingSystem(b))
}

func lockSessionSavePath(path string) func() {
	key := canonicalSessionSavePath(path)
	v, _ := sessionSaveLocks.LoadOrStore(key, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// tryLockSessionSavePath lets low-priority maintenance yield immediately to a
// foreground save. The returned unlock is non-nil only when acquired.
func tryLockSessionSavePath(path string) (func(), bool) {
	key := canonicalSessionSavePath(path)
	v, _ := sessionSaveLocks.LoadOrStore(key, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	if !mu.TryLock() {
		return nil, false
	}
	return mu.Unlock, true
}

// lockSessionFile waits briefly for the cross-process compatibility save lock.
// A short overlap with a legitimate writer is allowed to settle, but an
// stalled or indefinitely held lock fails the save instead of freezing tab
// switching or application shutdown. The caller keeps its in-memory transcript
// and can retry through the existing autosave/recovery paths.
func lockSessionFile(path string) (func(), error) {
	wait := sessionFileLockWait
	poll := sessionFileLockPollInterval
	if poll <= 0 {
		poll = time.Millisecond
	}
	deadline := time.Now().Add(wait)
	for {
		unlock, err := tryLockSessionFile(path)
		if err == nil {
			return unlock, nil
		}
		if !errors.Is(err, ErrSessionFileLockHeld) {
			return nil, err
		}
		remaining := time.Until(deadline)
		if wait <= 0 || remaining <= 0 {
			return nil, ErrSessionFileLockHeld
		}
		if poll > remaining {
			poll = remaining
		}
		time.Sleep(poll)
	}
}

// LockSessionMetaPath serializes a complete branch-meta read-modify-write
// cycle with both goroutines in this process and other Reasonix processes.
// Callers must hold it from the first read through the final replace.
func LockSessionMetaPath(path string) (func(), error) {
	lockPath, err := sessionMetaLockTarget(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionMetaLockWait)
	releaseFile, err := filelock.Acquire(ctx, lockPath)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("lock session metadata: %w", err)
	}
	return func() {
		releaseFile()
	}, nil
}

func tryLockSessionMetaPath(path string) (func(), bool, error) {
	lockPath, err := sessionMetaLockTarget(path)
	if err != nil {
		return nil, false, err
	}
	releaseFile, err := filelock.TryAcquire(lockPath)
	if errors.Is(err, filelock.ErrHeld) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("try lock session metadata: %w", err)
	}
	return releaseFile, true, nil
}

func sessionMetaLockTarget(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("empty session path")
	}
	canonical := canonicalSessionSavePath(path)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		return "", fmt.Errorf("create session metadata dir: %w", err)
	}
	return sessionMetaLockPath(canonical), nil
}

func sessionMetaLockPath(path string) string {
	canonical := canonicalSessionSavePath(path)
	digest := sha256.Sum256([]byte(canonical))
	return filepath.Join(filepath.Dir(canonical), "."+hex.EncodeToString(digest[:8])+".meta.lock")
}

// UpdateBranchMeta is the owner-level metadata API. The callback runs while
// the cross-process metadata lock is held, so callers cannot accidentally
// load a stale sidecar and overwrite fields written by another runtime.
func UpdateBranchMeta(path string, touchUpdated bool, update func(*BranchMeta) error) error {
	unlock, err := LockSessionMetaPath(path)
	if err != nil {
		return err
	}
	defer unlock()
	m, err := ensureBranchMetaUnlocked(path)
	if err != nil {
		return err
	}
	if update != nil {
		if err := update(&m); err != nil {
			return err
		}
	}
	return saveBranchMeta(path, m, touchUpdated)
}

func canonicalSessionSavePath(path string) string {
	return pathidentity.Canonical(path)
}

// CanonicalSessionPath is the identity key of a session path: cleaned,
// absolute, and case-folded on Windows, matching the key form used by the
// lease registry and the save-path locks. Any runtime bookkeeping that
// compares or maps session paths (desktop tabs, detached runtimes) must use
// this exact form, or the same file splits into distinct keys — e.g.
// `C:\Users\...` vs the lease's lowercased `c:\users\...`. Empty input stays
// empty instead of resolving to the working directory.
func CanonicalSessionPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return canonicalSessionSavePath(path)
}

// LoadSession reads a saved session into a fresh Session value. New sessions
// replay the append-only event log; legacy sessions without an event log fall
// back to the compatibility .jsonl checkpoint. A damaged log is replayed to its
// last clean record (or the checkpoint when nothing decodes) and flagged so the
// next save heals it with a rewrite-and-compact.
// In-process loads share the save path mutex so they cannot observe a local
// SaveSnapshot between appending an event-log record and refreshing the index.
// Missing files surface as os.IsNotExist so callers can fall through to a
// new session.
func LoadSession(path string) (*Session, error) {
	unlock := lockSessionSavePath(path)
	defer unlock()
	return loadSessionUnlocked(path)
}

func loadSessionUnlocked(path string) (*Session, error) {
	hasher := newSessionTranscriptHasher()
	res, err := loadSessionTranscript(context.Background(), path, defaultSessionReplayLimits, hasher)
	if err != nil {
		return nil, err
	}
	msgs := res.msgs
	s := &Session{Messages: msgs, eventLogDamaged: res.damaged, head: sessionHeadState{ref: res.head, dag: res.dag, headCount: res.headCount, state: res.state, openTurn: res.openTurn, events: res.events}}
	// Repair persisted-history-safe issues before anything reads the session.
	// Old sessions (pre adde2d3e) and interrupted turns can carry empty tool-call
	// names, dangling tool_calls, or half-streamed argument JSON that DeepSeek
	// rejects with a 400 on replay. Wire-only cleanup, such as dropping orphan
	// tool messages, stays in the provider send path so Save/LoadSession keeps
	// its round-trip contract. The fast path returns the input slice unchanged
	// for a well-formed history, so we detect an actual repair by comparing
	// slice headers: when NormalizeSession allocated a new backing array, the
	// session is marked dirty so the next Save persists the fix.
	normalized := NormalizeSession(s.Messages)
	normalized = migrateLegacyProviderContent(normalized)
	if len(normalized) != len(s.Messages) || (len(s.Messages) > 0 && &normalized[0] != &s.Messages[0]) {
		s.normalizedDirty = true
		// Keep the pre-repair transcript: checkSnapshotWrite must be able to
		// recognize a snapshot that extends the bytes actually on disk, which
		// the repaired view no longer represents (an interrupted tool turn
		// gets a placeholder result fabricated here that the live session
		// answered for real).
		s.rawMessages = msgs
	}
	s.Messages = normalized
	assignLegacyMessageIDs(path, s.Messages)
	// Decode already hashed the transcript; when the repairs above returned it
	// unchanged (the common case) reuse that digest, else re-hash the repair.
	digest, digestOK := hasher.sum()
	if s.normalizedDirty || !digestOK {
		if d, derr := digestSessionMessages(s.Messages); derr == nil {
			digest, digestOK = d, true
		} else {
			digestOK = false
		}
	}
	if digestOK {
		// Pair the raw pre-repair transcript when normalization changed it.
		diskView := s.Messages
		if s.normalizedDirty {
			diskView = s.rawMessages
		}
		if meta, ok, metaErr := loadBranchMetaRetry(path); metaErr != nil {
			// The sidecar exists but is unreadable even after retries (torn or
			// corrupt). The session must still open, but revision 0 must not
			// pose as a real baseline: the next save would misread the honest
			// on-disk revision as another runtime's write and fork a recovery
			// branch. Anchor the baseline on digest+version only until a
			// successful save re-learns the revision.
			s.markPersistedRevisionUnknown(path, digest, s.version, s.rewriteVersion, diskView)
		} else {
			revision := int64(0)
			if ok {
				revision = meta.Revision
			}
			s.markPersistedFromLoad(path, digest, s.version, revision, s.rewriteVersion, diskView)
		}
	}
	return s, nil
}

// SessionInfo summarises a saved session for the --resume picker: where it is on
// disk, when it was created/last active, the first user message as a preview, and
// a rough turn count.
type SessionInfo struct {
	Path                 string
	CreatedAt            time.Time
	LastActivityAt       time.Time
	ModTime              time.Time // compatibility alias for LastActivityAt
	Preview              string
	Turns                int
	CountsKnown          bool
	Scope                string
	WorkspaceRoot        string
	TopicID              string
	TopicTitle           string
	CustomTitle          string
	Recovered            bool
	RecoveryReason       string
	RecoveryDigest       string
	ParentID             string
	VersionKind          SessionVersionKind
	VersionState         SessionVersionState
	ParentConversationID string
	ParentVersionID      string
}

// CleanupPendingMeta records that a session was logically removed but still has
// artifacts waiting for a background job to unwind before physical cleanup.
type CleanupPendingMeta struct {
	Operation string `json:"operation"`
	CreatedAt int64  `json:"createdAt"`
}

// CleanupPendingInfo describes one durable delayed-cleanup marker and the
// session transcript it belongs to.
type CleanupPendingInfo struct {
	SessionPath string
	MarkerPath  string
	Meta        CleanupPendingMeta
}

// CleanupPendingPath returns the durable marker path for a session transcript.
func CleanupPendingPath(sessionPath string) string {
	return store.SessionCleanupPending(sessionPath)
}

// MarkCleanupPending hides a logically removed session from resume/list surfaces
// until delayed physical cleanup has finished.
func MarkCleanupPending(sessionPath, operation string) error {
	path := CleanupPendingPath(sessionPath)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	meta := CleanupPendingMeta{Operation: strings.TrimSpace(operation), CreatedAt: time.Now().UnixMilli()}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	// The marker controls session visibility during delayed cleanup. Publish it
	// atomically so a crash cannot leave malformed JSON that hides the session
	// and blocks reconciliation on the next startup.
	return fileutil.AtomicWriteFile(path, b, 0o644)
}

// ClearCleanupPending removes a delayed-cleanup marker after physical cleanup.
func ClearCleanupPending(sessionPath string) error {
	path := CleanupPendingPath(sessionPath)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// IsCleanupPending reports whether a session is hidden pending delayed cleanup.
func IsCleanupPending(sessionPath string) bool {
	path := CleanupPendingPath(sessionPath)
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// IsVisibleSession reports whether a persisted session should appear on normal
// user/agent-facing list, restore, and retrieval surfaces.
func IsVisibleSession(sessionPath string) bool {
	return strings.TrimSpace(sessionPath) != "" && !IsCleanupPending(sessionPath)
}

// ListCleanupPending returns delayed-cleanup markers left in dir. A missing
// directory is not an error.
func ListCleanupPending(dir string) ([]CleanupPendingInfo, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []CleanupPendingInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), cleanupPendingExt) {
			continue
		}
		markerPath := filepath.Join(dir, e.Name())
		var meta CleanupPendingMeta
		b, err := fileencoding.ReadFileUTF8(markerPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if strings.TrimSpace(string(b)) != "" {
			if err := json.Unmarshal(b, &meta); err != nil {
				return nil, fmt.Errorf("read cleanup-pending marker %s: %w", markerPath, err)
			}
		}
		name := strings.TrimSuffix(e.Name(), cleanupPendingExt) + ".jsonl"
		out = append(out, CleanupPendingInfo{
			SessionPath: filepath.Join(dir, name),
			MarkerPath:  markerPath,
			Meta:        meta,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].SessionPath < out[j].SessionPath
	})
	return out, nil
}

// ReconcileCleanupPending retries physical cleanup for leftover delayed-cleanup
// markers and stale lock/lease sidecars. It keeps going after individual
// cleanup errors and returns them joined.
func ReconcileCleanupPending(dir string, cleanup func(CleanupPendingInfo) error) error {
	var errs []error
	if err := ReconcileSessionSidecars(dir); err != nil {
		errs = append(errs, err)
	}
	if err := reconcileRecoveryTrashStages(dir); err != nil {
		errs = append(errs, err)
	}
	pending, err := ListCleanupPending(dir)
	if err != nil {
		errs = append(errs, err)
		return errors.Join(errs...)
	}
	for _, item := range pending {
		handled, err := reconcileRecoveryTrashPending(item)
		if !handled {
			if cleanup == nil {
				continue
			}
			err = cleanup(item)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", item.SessionPath, err))
		}
	}
	return errors.Join(errs...)
}

// ReconcileSessionSidecars renames transcripts whose filenames outgrew their
// sidecars and removes stale lock and lease files left beside sessions by
// older runtimes. It never removes .jsonl transcripts; recovered conversations
// may contain useful user history even when their names are ugly.
func ReconcileSessionSidecars(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	var errs []error
	if err := reconcileOverlongSessionFilenames(dir); err != nil {
		errs = append(errs, err)
	}
	// Re-list after the rename pass: it retires old names and their sidecars.
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return errors.Join(errs...)
		}
		errs = append(errs, err)
		return errors.Join(errs...)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		sidecarPath := filepath.Join(dir, name)
		switch {
		case strings.HasSuffix(name, sessionLeaseInfoSidecarSuffix):
			base := filepath.Join(dir, strings.TrimSuffix(name, ".lease.json"))
			if err := removeStaleSessionLeaseInfoSidecar(base, sidecarPath); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", sidecarPath, err))
			}
		case strings.HasSuffix(name, sessionLeaseLockSidecarSuffix):
			base := filepath.Join(dir, strings.TrimSuffix(name, ".lease.lock"))
			if err := removeStaleSessionLeaseLockSidecar(base, sidecarPath); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", sidecarPath, err))
			}
		case strings.HasSuffix(name, sessionLockSidecarSuffix):
			base := filepath.Join(dir, strings.TrimSuffix(name, ".lock"))
			if err := removeStaleSessionLockSidecar(base, sidecarPath); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", sidecarPath, err))
			}
		}
	}
	return errors.Join(errs...)
}

func removeStaleSessionLockSidecar(basePath, sidecarPath string) error {
	basePath = canonicalSessionSavePath(basePath)
	if sessionLeaseHeldLocally(basePath) || SessionLeaseHeldByOtherRuntime(basePath) {
		return nil
	}
	lock, err := tryTakeSessionLockFile(sidecarPath)
	if err != nil {
		if errors.Is(err, ErrSessionFileLockHeld) {
			return nil
		}
		return err
	}
	// The removal is atomic with the release (unlink-under-flock on Unix,
	// delete-disposition on the held handle on Windows), so a concurrent
	// saver can never acquire a lock file that is being deleted under it.
	return lock.RemoveAndUnlock()
}

// removeStaleSessionLeaseLockSidecar retires a leftover .lease.lock. The file
// is the lease lock itself, so taking it non-blocking proves no runtime holds
// the lease, and RemoveAndUnlock deletes it atomically with the release.
func removeStaleSessionLeaseLockSidecar(basePath, _ string) error {
	basePath = canonicalSessionSavePath(basePath)
	if sessionLeaseHeldLocally(basePath) {
		return nil
	}
	lock, err := tryTakeSessionLeaseLock(basePath)
	if err != nil {
		if errors.Is(err, ErrSessionLeaseHeld) {
			return nil
		}
		return err
	}
	return lock.RemoveAndUnlock()
}

// removeStaleSessionLeaseInfoSidecar retires a leftover .lease.json while
// holding the lease lock, so no runtime can adopt the info file mid-removal.
// The info file itself is never held open by anyone, so a plain remove under
// the lock is safe on every platform.
func removeStaleSessionLeaseInfoSidecar(basePath, sidecarPath string) error {
	basePath = canonicalSessionSavePath(basePath)
	if sessionLeaseHeldLocally(basePath) {
		return nil
	}
	lockPath := basePath + ".lease.lock"
	if _, err := os.Stat(lockPath); err == nil {
		unlock, err := tryLockSessionLeaseFile(basePath)
		if err != nil {
			if errors.Is(err, ErrSessionLeaseHeld) {
				return nil
			}
			return err
		}
		removeErr := os.Remove(sidecarPath)
		if unlock != nil {
			unlock()
		}
		if removeErr != nil && !os.IsNotExist(removeErr) {
			return removeErr
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	// No lease lock file: holders keep it present (and locked) for their whole
	// lifetime, so the leftover info sidecar has no owner to race with.
	if err := os.Remove(sidecarPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func sessionLeaseHeldLocally(path string) bool {
	_, ok := sessionLeaseOwners.Load(canonicalSessionSavePath(path))
	return ok
}

// sessionLockSidecarFits reports whether basePath's .lock sidecar name stays
// within the filesystem's per-component limit; past it, no process can hold
// (or ever have held) the file lock, because the lock file cannot be created.
func sessionLockSidecarFits(basePath string) bool {
	return len(filepath.Base(basePath))+len(".lock") <= nameMaxBytes
}

// sessionLeaseSidecarFits is the lease-file analogue of sessionLockSidecarFits.
func sessionLeaseSidecarFits(basePath string) bool {
	return len(filepath.Base(basePath))+len(".lease.lock") <= nameMaxBytes
}

// reconcileOverlongSessionFilenames renames transcripts whose basenames grew
// past maxSessionBasenameBytes — the leftover shape of the pre-bounded
// recovery cascade (#5923), where lock and lease sidecars could no longer be
// created and the session became unsaveable. The conversation bytes are kept
// verbatim under a bounded name derived the same way new recovery branches
// are named; branch meta moves along with its ID rewritten, and sessions
// pointing at the old ID are re-parented so lineage survives the rename.
func reconcileOverlongSessionFilenames(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var errs []error
	renamed := map[string]string{} // old branch ID -> new branch ID
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !store.IsSessionTranscriptName(name) {
			continue
		}
		if len(name) <= maxSessionBasenameBytes {
			continue
		}
		oldPath := filepath.Join(dir, name)
		if IsCleanupPending(oldPath) {
			// Being deleted; renaming would orphan the cleanup marker.
			continue
		}
		newID, err := renameOverlongSession(oldPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", oldPath, err))
		}
		// A non-empty newID means the transcript rename landed even if some
		// sidecar migration failed; record it so children still re-parent —
		// this run is the only one that knows the old-to-new mapping.
		if newID != "" {
			renamed[BranchID(oldPath)] = newID
		}
	}
	if len(renamed) > 0 {
		if err := reparentSessionBranches(dir, renamed); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// renameOverlongSession moves one overlong transcript to its bounded name and
// migrates the sidecars that carry user state. It returns the new branch ID,
// or "" when the session was skipped because a runtime may still own it.
func renameOverlongSession(oldPath string) (string, error) {
	oldID := BranchID(oldPath)
	newID := recoveryParentStem(oldID)
	if newID == oldID {
		return "", nil
	}
	newPath := filepath.Join(filepath.Dir(oldPath), newID+".jsonl")
	if _, err := os.Stat(newPath); err == nil {
		return "", fmt.Errorf("rename target %s already exists", filepath.Base(newPath))
	} else if !os.IsNotExist(err) {
		return "", err
	}
	unlockOld := lockSessionSavePath(oldPath)
	defer unlockOld()
	unlockNew := lockSessionSavePath(newPath)
	defer unlockNew()
	if sessionLeaseHeldLocally(oldPath) {
		return "", nil
	}
	// Names past the sidecar limit cannot have lease or lock holders in any
	// process — the holder files themselves are uncreatable — so probing them
	// would only manufacture ENAMETOOLONG errors and wrongly skip the exact
	// sessions this pass exists to repair.
	if sessionLeaseSidecarFits(oldPath) && SessionLeaseHeldByOtherRuntime(oldPath) {
		return "", nil
	}
	var lockFile *sessionLockFile
	if sessionLockSidecarFits(oldPath) {
		lock, err := tryTakeSessionLockFile(oldPath + ".lock")
		if err != nil {
			if errors.Is(err, ErrSessionFileLockHeld) {
				return "", nil
			}
			return "", err
		}
		lockFile = lock
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		// Nothing moved: the old transcript is intact and the next
		// reconciliation can retry, so its lock file stays in place too.
		if lockFile != nil {
			lockFile.Unlock()
		}
		return "", err
	}
	// The transcript is committed under its new name from here on. Sidecar
	// migration and lock cleanup failures are reported, but the new ID is
	// still returned so the caller re-parents children: the old name is gone,
	// and a later run would have no way to reconstruct this mapping.
	var errs []error
	if err := migrateSessionSidecars(oldPath, newPath, newID); err != nil {
		errs = append(errs, err)
	}
	// Retire the old disposable lease sidecars: any holder was ruled out
	// above, and nothing keeps these files open, so a plain remove is safe.
	if sessionLeaseSidecarFits(oldPath) {
		for _, stale := range []string{oldPath + ".lease.lock", oldPath + ".lease.json"} {
			if err := os.Remove(stale); err != nil && !os.IsNotExist(err) {
				errs = append(errs, err)
			}
		}
	}
	// The old .lock goes atomically with the release of the lock we hold on it.
	if lockFile != nil {
		if err := lockFile.RemoveAndUnlock(); err != nil {
			errs = append(errs, err)
		}
	}
	return newID, errors.Join(errs...)
}

// migrateSessionSidecars moves the user-state sidecars of a renamed session:
// branch meta (with its ID rewritten to match the new filename), goal state,
// and the checkpoint/job directories. Lock and lease files are disposable and
// are removed by the caller instead.
func migrateSessionSidecars(oldPath, newPath, newID string) error {
	errs := []error{migratePinnedContextSidecar(oldPath, newPath, newID)}
	if len(filepath.Base(oldPath))+len(".meta") <= nameMaxBytes {
		if meta, ok, err := LoadBranchMeta(oldPath); err != nil {
			errs = append(errs, err)
		} else if ok {
			meta.ID = newID
			if err := SaveBranchMetaPreserveUpdated(newPath, meta); err != nil {
				errs = append(errs, err)
			} else if err := os.Remove(BranchMetaPath(oldPath)); err != nil && !os.IsNotExist(err) {
				errs = append(errs, err)
			}
		}
	}
	for _, pair := range [][2]string{
		{store.SessionGoalState(oldPath), store.SessionGoalState(newPath)},
		{store.SessionEventLog(oldPath), store.SessionEventLog(newPath)},
		{store.SessionEventLogDamaged(oldPath), store.SessionEventLogDamaged(newPath)},
		{store.SessionTurnEventLog(oldPath), store.SessionTurnEventLog(newPath)},
		{store.SessionTurnEventLogDamaged(oldPath), store.SessionTurnEventLogDamaged(newPath)},
		{store.SessionEventIndex(oldPath), store.SessionEventIndex(newPath)},
		{store.SessionConflictLog(oldPath), store.SessionConflictLog(newPath)},
		{store.SessionRecoveryState(oldPath), store.SessionRecoveryState(newPath)},
		{store.SessionCheckpointDir(oldPath), store.SessionCheckpointDir(newPath)},
		{store.SessionJobsDir(oldPath), store.SessionJobsDir(newPath)},
		{store.SessionInboxDir(oldPath), store.SessionInboxDir(newPath)},
	} {
		// A source name past the filesystem limit cannot exist; renaming it
		// would just manufacture ENAMETOOLONG instead of a clean not-exist.
		if len(filepath.Base(pair[0])) > nameMaxBytes {
			continue
		}
		if err := os.Rename(pair[0], pair[1]); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// reparentSessionBranches rewrites ParentID references from renamed branch IDs
// to their bounded replacements so the branch tree stays connected.
func reparentSessionBranches(dir string, renamed map[string]string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !store.IsSessionTranscriptName(name) {
			continue
		}
		if len(name)+len(".meta") > nameMaxBytes {
			continue
		}
		path := filepath.Join(dir, name)
		unlock := lockSessionSavePath(path)
		meta, ok, err := LoadBranchMeta(path)
		if err == nil && ok {
			if newParent, hit := renamed[meta.ParentID]; hit && newParent != meta.ParentID {
				meta.ParentID = newParent
				err = SaveBranchMetaPreserveUpdated(path, meta)
			}
		}
		unlock()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

// ListSessionOrder returns every *.jsonl session under dir in the same
// most-recently-active order used by ListSessions, using only file metadata and
// branch sidecars. A missing directory is not an error.
func ListSessionOrder(dir string) ([]SessionOrderInfo, error) {
	return ListSessionOrderWithRecoveryPreferenceResolver(dir, RecoveryPreferenceCurrent)
}

// ListSessionOrderWithRecoveryPreferenceResolver lets catalog reconciliation
// reuse its wave-local transcript snapshot when validating explicit choices.
// Other callers keep the ordinary ListSessionOrder behavior above.
func ListSessionOrderWithRecoveryPreferenceResolver(dir string, resolve RecoveryPreferenceResolver) ([]SessionOrderInfo, error) {
	if resolve == nil {
		resolve = RecoveryPreferenceCurrent
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []SessionOrderInfo
	for _, e := range entries {
		if e.IsDir() || !store.IsSessionTranscriptName(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if !IsVisibleSession(full) {
			continue
		}
		contentMod := SessionContentModTime(full)
		if contentMod.IsZero() {
			contentMod = info.ModTime()
		}
		createdAt := info.ModTime()
		lastActivityAt := contentMod
		scope := "global"
		workspaceRoot := ""
		topicID := ""
		topicTitle := ""
		customTitle := ""
		recovered := false
		recoveryReason := ""
		recoveryDigest := ""
		parentID := ""
		versionKind := VersionNormal
		versionState := VersionActive
		parentConversationID := ""
		var headMirror BranchMeta
		parentVersionID := ""
		recoveryPreferred := false
		turns := 0
		preview := ""
		schemaVersion := 0
		revision := int64(0)
		contentDigest := ""
		listingRevision := int64(0)
		listingContentDigest := ""
		if meta, ok, err := LoadBranchMeta(full); err == nil && ok {
			if !meta.CreatedAt.IsZero() {
				createdAt = meta.CreatedAt
			}
			if !meta.UpdatedAt.IsZero() {
				lastActivityAt = meta.UpdatedAt
			}
			scope = meta.DefaultScope()
			workspaceRoot = meta.WorkspaceRoot
			topicID = meta.TopicID
			topicTitle = meta.TopicTitle
			customTitle = meta.CustomTitle
			recovered = meta.Recovered
			recoveryReason = meta.RecoveryReason
			recoveryDigest = meta.RecoveryDigest
			parentID = meta.ParentID
			versionKind = meta.EffectiveVersionKind()
			versionState = meta.EffectiveVersionState()
			parentConversationID = meta.ParentConversationID
			parentVersionID = meta.ParentVersionID
			recoveryPreferred = resolve(full, meta)
			turns = meta.Turns
			preview = meta.Preview
			schemaVersion = meta.SchemaVersion
			revision = meta.Revision
			contentDigest = meta.ContentDigest
			listingRevision = meta.ListingRevision
			listingContentDigest = meta.ListingContentDigest
			headMirror = meta
		}
		// Old recovery files may lack Recovered meta; filename still proves
		// automatic recovery lineage for catalog folding.
		if !recovered && LooksLikeRecoveryFilename(full) {
			recovered = true
			versionKind = VersionRecovery
			if parentID == "" {
				if parent, ok := RecoveryFilenameParentID(full); ok {
					parentID = parent
				}
			}
		}
		out = append(out, SessionOrderInfo{
			Path:                 full,
			CreatedAt:            createdAt,
			LastActivityAt:       lastActivityAt,
			ModTime:              lastActivityAt,
			Scope:                scope,
			WorkspaceRoot:        workspaceRoot,
			TopicID:              topicID,
			TopicTitle:           topicTitle,
			CustomTitle:          customTitle,
			Recovered:            recovered,
			RecoveryReason:       recoveryReason,
			RecoveryDigest:       recoveryDigest,
			ParentID:             parentID,
			VersionKind:          versionKind,
			VersionState:         versionState,
			ParentConversationID: parentConversationID,
			ParentVersionID:      parentVersionID,
			RecoveryPreferred:    recoveryPreferred,
			Turns:                turns,
			Preview:              preview,
			SchemaVersion:        schemaVersion,
			Revision:             revision,
			ContentDigest:        contentDigest,
			ListingRevision:      listingRevision,
			ListingContentDigest: listingContentDigest,
			HeadID:               headMirror.HeadID,
			HeadCount:            headMirror.HeadCount,
			LogSchema:            headMirror.LogSchema,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastActivityAt.Equal(out[j].LastActivityAt) {
			return out[i].Path < out[j].Path
		}
		return out[i].LastActivityAt.After(out[j].LastActivityAt)
	})
	return out, nil
}

// ListSessions returns every non-empty *.jsonl session under dir,
// most-recently-active first, each with a preview line so the picker can show
// something the user recognises. It never decodes a transcript: legacy counts
// remain explicitly unknown until the session catalog's single repair worker
// validates them. A missing directory is not an error.
func ListSessions(dir string) ([]SessionInfo, error) {
	ordered, err := ListSessionOrder(dir)
	if err != nil {
		return nil, err
	}
	var out []SessionInfo
	for _, session := range ordered {
		preview, turns := session.Preview, session.Turns
		if !sessionListingProjectionFresh(session.SchemaVersion, turns, session.Revision, session.ListingRevision, session.ContentDigest, session.ListingContentDigest) {
			if !sessionArtifactsHaveContent(session.Path) {
				continue
			}
			if strings.TrimSpace(preview) == "" {
				preview = "History is being indexed — " + filepath.Base(session.Path)
			}
			out = append(out, sessionInfoFromOrder(session, preview, turns, false))
			continue
		}
		if turns == 0 {
			// Never had user interaction — an empty conversation that should not
			// appear in the history panel or the resume picker.
			continue
		}
		out = append(out, sessionInfoFromOrder(session, preview, turns, true))
	}
	return out, nil
}

// SessionPreview returns the same preview and user-turn count used by
// ListSessions for one session file.
func SessionPreview(path string) (string, int) {
	return previewSession(path)
}

// SessionPreviewWithError returns the same preview and user-turn count as
// SessionPreview, but preserves read and decode failures for callers that must
// not persist a fallback derived from an unreadable transcript.
func SessionPreviewWithError(path string) (string, int, error) {
	return previewSessionWithError(path)
}

// SessionPreviewFromMessages computes the same preview line and user-turn count
// as previewSession, but from an in-memory message slice. Session.Save writes
// exactly these messages to the .jsonl, so this is byte-for-byte equivalent to
// decoding the file — letting the autosave path persist the counts into the
// sidecar without a disk read.
func SessionPreviewFromMessages(msgs []provider.Message) (string, int) {
	first := ""
	turns := 0
	for _, m := range msgs {
		if IsUserAuthoredTurnMessage(m) {
			turns++
			if first == "" {
				first = truncatePreview(previewProse(UserMessageText(m)))
			}
		}
	}
	return first, turns
}

// previewSession returns the first user message (truncated) and the number of
// user-role messages so the picker can show "5 turns · 'help me debug the…'".
// Errors are swallowed — a malformed file just shows up with an empty preview.
func previewSession(path string) (string, int) {
	preview, turns, _ := previewSessionWithError(path)
	return preview, turns
}

// previewProse drops the leading @file references a prompt opens with so the
// preview shows what was asked rather than a row of paths. A prompt that is
// nothing but references keeps them — there is nothing else to show.
func previewProse(s string) string {
	rest := strings.TrimLeft(s, " \t")
	for strings.HasPrefix(rest, "@") {
		end := strings.IndexAny(rest, " \t\r\n")
		if end < 0 {
			return s
		}
		next := strings.TrimLeft(rest[end:], " \t")
		if strings.TrimSpace(next) == "" {
			return s
		}
		rest = next
	}
	if rest == "" {
		return s
	}
	return rest
}

// truncatePreview clamps a preview line to 80 runes with an ellipsis, matching
// what the pickers render.
func truncatePreview(s string) string {
	if r := []rune(s); len(r) > 80 {
		return string(r[:77]) + "…"
	}
	return s
}
