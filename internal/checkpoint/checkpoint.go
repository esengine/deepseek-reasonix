// Package checkpoint is reasonix's snapshot-based edit safety net. Before a writer
// tool changes a file, the agent records the file's pre-edit content here, keyed
// to the current user turn; a frontend can then rewind the workspace (and, via the
// controller, the conversation) to an earlier turn.
//
// It is deliberately git-free (like Claude Code's rewind): snapshots live beside
// the session, never touch the user's git, and work in a non-git directory. Only
// edit-tool changes are tracked — bash side effects are not (a shell command's
// targets can't be known in advance), which is why the capture hook only fires for
// tools that can Preview their change.
//
// Schema v2 adds blobs and verified restore; v3 stores new preimages in per-turn
// directories while retaining legacy blob and transaction compatibility.
package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"reasonix/internal/diff"
	"reasonix/internal/fileutil"
	fileenc "reasonix/internal/fileutil/encoding"
)

// FileSnap is one file's state at the moment it was first touched in a turn.
// Content == nil means the file did not exist then, so a restore deletes it.
//
// v2 fields (Mode, SHA256, BlobRef, After*, CaptureSource) are omitempty so v1
// readers ignore them and old JSON still unmarshals cleanly.
type FileSnap struct {
	Path          string        `json:"path"`
	Content       *string       `json:"content"`
	Encoding      *fileenc.Kind `json:"encoding,omitempty"`
	Mode          uint32        `json:"mode,omitempty"`
	SHA256        string        `json:"sha256,omitempty"`
	BlobRef       string        `json:"blobRef,omitempty"`
	CaptureSource CaptureSource `json:"captureSource,omitempty"`
	AfterSHA256   string        `json:"afterSha256,omitempty"`
	AfterExisted  *bool         `json:"afterExisted,omitempty"`
	AfterMode     uint32        `json:"afterMode,omitempty"`
	// PayloadExpired marks that the blob was GC'd while metadata remains.
	PayloadExpired bool `json:"payloadExpired,omitempty"`
	rawContent     []byte
}

// FileState is the earliest pre-edit state recorded for a file in this
// session. Content == nil means the file did not exist before the session's
// first tracked edit.
type FileState struct {
	Content  *string
	Encoding *fileenc.Kind
	Mode     uint32
	SHA256   string
	BlobRef  string
	Owned    bool // true when session has after-fingerprint ownership
}

// revisions returns FileRevision views of Files.
func (c *Checkpoint) revisions() []FileRevision {
	if c == nil {
		return nil
	}
	out := make([]FileRevision, 0, len(c.Files))
	for _, f := range c.Files {
		rev := FileRevision{
			Path:          f.Path,
			Existed:       f.Content != nil || f.BlobRef != "" || f.SHA256 != "",
			Mode:          f.Mode,
			Encoding:      f.Encoding,
			SHA256:        f.SHA256,
			BlobRef:       f.BlobRef,
			CaptureSource: f.CaptureSource,
			AfterSHA256:   f.AfterSHA256,
			AfterExisted:  f.AfterExisted,
			AfterMode:     f.AfterMode,
			Content:       f.Content,
		}
		// v1 create: Content nil and no blob → did not exist.
		if f.Content == nil && f.BlobRef == "" && f.SHA256 == "" {
			rev.Existed = false
		}
		if f.Content != nil {
			rev.Existed = true
			if rev.SHA256 == "" {
				rev.SHA256 = Digest([]byte(*f.Content))
			}
		}
		if f.PayloadExpired {
			rev.BlobRef = ""
			rev.Content = nil
		}
		out = append(out, rev)
	}
	return out
}

// Meta is the picker-facing summary of a checkpoint (no file contents).
type Meta struct {
	Turn               int
	Time               time.Time
	Prompt             string
	Paths              []string
	Coverage           Coverage
	CoverageGaps       []CoverageGap
	ExpiredFilePayload bool
	ActiveWriters      []ActiveWriter
	Legacy             bool
	CanUndoFiles       bool
	DisabledReason     string
}

// Store holds a session's checkpoints in memory and, when dir is set, persists one
// JSON file per turn under it (cheap delete, corruption-isolated). All methods are
// safe for concurrent use — the agent snapshots from tool goroutines.
type Store struct {
	dir  string // <session>.ckpt/, or "" for in-memory only
	root string // workspace root, for restore path-escape guards

	mu   sync.Mutex
	done []*Checkpoint   // finalized turns
	cur  *Checkpoint     // the active turn's checkpoint
	seen map[string]bool // paths already snapshotted this turn (dedup)

	blobs         *BlobStore
	barrier       *MutationBarrier
	activeWriters []ActiveWriter
	plans         map[string]preparedPlan
	lastUndo      *TransactionManifest
	sessionID     string
	mutationSeq   int64
	retainN       int
	blobQuota     int64
	// protectTurns prevents GC of these turn payloads (active tx / last undo).
	protectTurns map[int]bool
}

// New returns a store for the given checkpoint dir and workspace root, loading any
// checkpoints already persisted under dir. A "" dir disables persistence (the
// store still works in memory for the session).
func New(dir, root string) *Store {
	s := &Store{
		dir:          dir,
		root:         root,
		seen:         map[string]bool{},
		barrier:      NewMutationBarrier(),
		plans:        map[string]preparedPlan{},
		retainN:      DefaultRetainCheckpoints,
		blobQuota:    DefaultBlobQuotaBytes,
		protectTurns: map[int]bool{},
	}
	if dir != "" {
		s.blobs = NewBlobStore(filepath.Join(dir, "blobs"))
		s.load()
		s.RecoverTransactions()
		s.mu.Lock()
		s.gcLocked()
		s.mu.Unlock()
	}
	return s
}

// Barrier returns the workspace mutation barrier for this store.
func (s *Store) Barrier() *MutationBarrier {
	if s == nil {
		return nil
	}
	return s.barrier
}

// Blobs returns the content-addressed blob store (may be nil for in-memory).
func (s *Store) Blobs() *BlobStore {
	if s == nil {
		return nil
	}
	return s.blobs
}

// SetSessionID records the owning session id on new checkpoints.
func (s *Store) SetSessionID(id string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.sessionID = id
	s.mu.Unlock()
}

// SetActiveWriters updates the active writer list mirrored into the current checkpoint.
func (s *Store) SetActiveWriters(writers []ActiveWriter) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeWriters = append([]ActiveWriter(nil), writers...)
	if s.cur != nil {
		s.cur.ActiveWriters = append([]ActiveWriter(nil), writers...)
		s.recomputeCoverageLocked(s.cur)
		s.persistBestEffort(s.cur)
	}
}

func (s *Store) activeWriterConflicts() []RewindConflict {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conflicts := make([]RewindConflict, 0, len(s.activeWriters))
	for range s.activeWriters {
		conflicts = append(conflicts, RewindConflict{Reason: ConflictBusyWriter})
	}
	return conflicts
}

// LastUndoTransactionID returns the committed transaction id available for undo.
func (s *Store) LastUndoTransactionID() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastUndo == nil || s.lastUndo.State != TxCommitted {
		return ""
	}
	return s.lastUndo.ID
}

// InvalidateUndo clears the last undo slot (new turn / new mutation / new rewind).
func (s *Store) InvalidateUndo() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lastUndo = nil
	s.mu.Unlock()
}

// Begin opens a checkpoint for a new user turn, finalizing the previous one. The
// prompt labels it in the picker; msgIndex is the conversation-rewind boundary.
func (s *Store) Begin(turn int, prompt string, msgIndex int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil {
		s.recomputeCoverageLocked(s.cur)
		s.done = append(s.done, s.cur)
	}
	s.cur = &Checkpoint{
		SchemaVersion: SchemaV3,
		Turn:          turn,
		Time:          time.Now(),
		Prompt:        prompt,
		MsgIndex:      msgIndex,
		SessionID:     s.sessionID,
		Coverage:      CoverageNone,
	}
	s.seen = map[string]bool{}
	s.lastUndo = nil // new turn invalidates undo
	s.persistBestEffort(s.cur)
	s.gcLocked()
}

// Bounds returns turn → MsgIndex over all checkpoints (persisted + current), so
// the controller can rebuild its conversation-rewind boundaries after loading a
// resumed session's checkpoints from disk.
func (s *Store) Bounds() map[int]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := make(map[int]int, len(s.done))
	for _, c := range s.done {
		m[c.Turn] = c.MsgIndex
	}
	if s.cur != nil {
		m[s.cur.Turn] = s.cur.MsgIndex
	}
	return m
}

// Snapshot records the pre-edit state of the file a writer is about to change.
// Only the first touch of a path in the current turn is kept (that is its
// turn-start content). A no-op before the first Begin.
//
// Legacy entry point used by SetPreEditHook; prefer CaptureBefore / MutationObserver.
func (s *Store) Snapshot(ch diff.Change) {
	s.CaptureBeforeFromChange(ch, CaptureBeforeOpts{Source: CapturePreviewer})
}

// CaptureBeforeFromChange records a preimage using a Previewer change when possible.
func (s *Store) CaptureBeforeFromChange(ch diff.Change, opts CaptureBeforeOpts) {
	if ch.Path == "" {
		return
	}
	pathKey := NormalizeRelPath(s.root, ch.Path)
	if opts.Source == "" {
		opts.Source = CapturePreviewer
	}

	var enc *fileenc.Kind
	var mode uint32
	var sha string
	var content *string
	var rawContent []byte

	if ch.Kind != diff.Create {
		old := ch.OldText
		content = &old
		sha = Digest([]byte(old))
		// Detect encoding from disk for non-UTF8 restore fidelity.
		enc = s.detectEncoding(ch.Path)
		// Capture mode via Lstat; also detect symlink/hardlink gaps.
		fp, gap, err := CapturePath(ch.Path, CaptureOptions{
			WorkspaceRoot: s.root,
			ReadContent:   false,
		})
		if gap != nil {
			s.RecordGap(*gap)
		}
		if err == nil {
			mode = fp.Mode
		}
		// Prefer disk bytes when available for exact restore (encoding).
		if abs, aerr := safePath(s.root, ch.Path); aerr == nil {
			if raw, rerr := secureReadFile(s.root, abs); rerr == nil {
				sha = Digest(raw)
				rawContent = append([]byte(nil), raw...)
				e, detected := fileenc.Detect(raw)
				decoded := string(fileenc.Decode(detected, e))
				content = &decoded
				enc = &e
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil || s.seen[pathKey] {
		return
	}
	s.seen[pathKey] = true
	snap := FileSnap{
		Path:          ch.Path,
		Content:       content,
		Encoding:      enc,
		Mode:          mode,
		SHA256:        sha,
		CaptureSource: opts.Source,
		rawContent:    rawContent,
	}
	// Keep inline content in memory so FileState and the legacy restore API can
	// distinguish existing files from the nil-content deletion sentinel.
	s.cur.Files = append(s.cur.Files, snap)
	if s.cur.SchemaVersion < SchemaV3 {
		s.cur.SchemaVersion = SchemaV3
	}
	s.recomputeCoverageLocked(s.cur)
	s.persistBestEffort(s.cur)
	s.gcLocked()
}

// CaptureBefore records a preimage by Lstat+read of path.
func (s *Store) CaptureBefore(path string, opts CaptureBeforeOpts) {
	if path == "" {
		return
	}
	pathKey := NormalizeRelPath(s.root, path)
	if opts.Source == "" {
		opts.Source = CaptureBeforeMutation
	}
	fp, gap, _ := CapturePath(path, CaptureOptions{
		WorkspaceRoot: s.root,
		ReadContent:   true,
	})
	if gap != nil {
		s.RecordGap(*gap)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil || s.seen[pathKey] {
		return
	}
	s.seen[pathKey] = true
	snap := FileSnap{
		Path:          path,
		CaptureSource: opts.Source,
	}
	if fp.Existed {
		snap.Mode = fp.Mode
		snap.SHA256 = fp.SHA256
		snap.rawContent = append([]byte(nil), fp.Content...)
		// Decoded text for API compat (FileState / legacy RestoreCode path).
		enc, raw := fileenc.Detect(fp.Content)
		text := string(fileenc.Decode(raw, enc))
		snap.Content = &text
		snap.Encoding = &enc
		if snap.SHA256 == "" {
			snap.SHA256 = Digest(fp.Content)
		}
	}
	// Content nil + no blob → create (did not exist)
	s.cur.Files = append(s.cur.Files, snap)
	if s.cur.SchemaVersion < SchemaV3 {
		s.cur.SchemaVersion = SchemaV3
	}
	s.recomputeCoverageLocked(s.cur)
	s.persistBestEffort(s.cur)
	s.gcLocked()
}

// RecordGap appends a coverage gap to the current checkpoint.
func (s *Store) RecordGap(gap CoverageGap) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return
	}
	// Dedupe identical gaps.
	for _, g := range s.cur.CoverageGaps {
		if g.Reason == gap.Reason && g.Detail == gap.Detail && g.Tool == gap.Tool && g.Path == gap.Path {
			return
		}
	}
	s.cur.CoverageGaps = append(s.cur.CoverageGaps, gap)
	s.recomputeCoverageLocked(s.cur)
	s.persistBestEffort(s.cur)
}

func (s *Store) recomputeCoverageLocked(c *Checkpoint) {
	if c == nil {
		return
	}
	if c.Legacy || c.SchemaVersion < SchemaV2 {
		c.Coverage = CoverageLegacy
		return
	}
	if c.ExpiredFilePayload {
		c.Coverage = CoveragePartial
		return
	}
	hasFiles := len(c.Files) > 0
	hasGaps := len(c.CoverageGaps) > 0
	switch {
	case !hasFiles && !hasGaps:
		c.Coverage = CoverageNone
	case !hasFiles && hasGaps:
		c.Coverage = CoverageNone
	case hasFiles && hasGaps:
		c.Coverage = CoveragePartial
	default:
		c.Coverage = CoverageComplete
	}
}

func (s *Store) detectEncoding(p string) *fileenc.Kind {
	abs, err := safePath(s.root, p)
	if err != nil {
		return nil
	}
	b, err := secureReadFile(s.root, abs)
	if err != nil {
		return nil
	}
	enc, _ := fileenc.Detect(b)
	return &enc
}

func (s *Store) expiredDir() string {
	return filepath.Join(s.dir, "expired")
}

func (s *Store) checkpointPath(c *Checkpoint) string {
	dir := s.dir
	if c != nil && c.ExpiredFilePayload {
		dir = s.expiredDir()
	}
	return filepath.Join(dir, fmt.Sprintf("turn-%d.json", c.Turn))
}

func (s *Store) persist(c *Checkpoint) error {
	if s.dir == "" || c == nil {
		return nil
	}
	if c.SchemaVersion >= SchemaV3 {
		return s.persistV3(c)
	}
	// Keep inline Content even when BlobRef is present. Previous Reasonix builds
	// ignore BlobRef and interpret nil Content as "the file did not exist";
	// omitting it would make an older concurrently running binary delete files.
	wire := *c
	wire.Files = make([]FileSnap, len(c.Files))
	copy(wire.Files, c.Files)
	b, err := json.Marshal(&wire)
	if err != nil {
		return err
	}
	path := s.checkpointPath(c)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := fileutil.AtomicWriteFileStrict(path, b, 0o644); err != nil {
		return err
	}
	return nil
}

func (s *Store) persistBestEffort(c *Checkpoint) {
	if err := s.persist(c); err != nil {
		slog.Warn("checkpoint: persist failed", "turn", c.Turn, "err", err)
	}
}

// gcLocked removes old v3 turn directories and retains the legacy v1/v2 blob
// quota policy for checkpoints written by older releases.
// Caller holds s.mu.
func (s *Store) gcLocked() {
	s.pruneV3TurnsLocked()
	if s.blobs == nil || s.retainN <= 0 {
		return
	}
	// Collect recoverable checkpoints (have file payloads) oldest first.
	all := s.all()
	type entry struct {
		c *Checkpoint
	}
	var withFiles []entry
	for _, c := range all {
		if c.SchemaVersion < SchemaV3 && len(c.Files) > 0 {
			withFiles = append(withFiles, entry{c: c})
		}
	}
	// Expire payloads for all but the newest retainN.
	if len(withFiles) > s.retainN {
		expiredAny := false
		for _, e := range withFiles[:len(withFiles)-s.retainN] {
			if s.protectTurns[e.c.Turn] {
				continue
			}
			if err := s.expirePayloadLocked(e.c); err != nil {
				slog.Warn("checkpoint: expire payload failed", "turn", e.c.Turn, "err", err)
				continue
			}
			expiredAny = true
		}
		if expiredAny {
			s.pruneBlobsLocked()
		}
	}
	// Blob quota.
	size, err := s.blobs.Size()
	if err != nil || size <= s.blobQuota {
		return
	}
	for _, e := range withFiles {
		if size <= s.blobQuota {
			break
		}
		if s.protectTurns[e.c.Turn] || e.c.ExpiredFilePayload {
			continue
		}
		// Rough: expire and recompute size.
		if err := s.expirePayloadLocked(e.c); err != nil {
			slog.Warn("checkpoint: expire payload failed", "turn", e.c.Turn, "err", err)
			continue
		}
		s.pruneBlobsLocked()
		size, _ = s.blobs.Size()
	}
}

// pruneBlobsLocked performs mark-and-sweep after checkpoint metadata has been
// persisted. Transaction manifests and the current undo slot also keep their
// forward/restore payloads live. Caller holds s.mu.
func (s *Store) pruneBlobsLocked() {
	if s.blobs == nil {
		return
	}
	live := map[string]struct{}{}
	mark := func(ref string) {
		if validBlobRef(ref) {
			live[ref] = struct{}{}
		}
	}
	for _, c := range s.all() {
		for _, f := range c.Files {
			mark(f.BlobRef)
		}
	}
	if s.lastUndo != nil {
		for _, target := range s.lastUndo.Targets {
			mark(target.RestoreBlob)
			mark(target.ForwardBlob)
		}
	}
	if s.dir != "" {
		entries, _ := os.ReadDir(s.txDir())
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			var tx TransactionManifest
			if readJSONFile(filepath.Join(s.txDir(), entry.Name()), &tx) != nil {
				continue
			}
			for _, target := range tx.Targets {
				mark(target.RestoreBlob)
				mark(target.ForwardBlob)
			}
		}
	}
	if err := s.blobs.Prune(live); err != nil {
		slog.Warn("checkpoint: prune blobs", "err", err)
	}
}

func (s *Store) expirePayloadLocked(c *Checkpoint) error {
	if c == nil || c.ExpiredFilePayload {
		return nil
	}
	expired := *c
	expired.Files = append([]FileSnap(nil), c.Files...)
	expired.CoverageGaps = append([]CoverageGap(nil), c.CoverageGaps...)
	for i := range expired.Files {
		expired.Files[i].BlobRef = ""
		expired.Files[i].Content = nil
		expired.Files[i].PayloadExpired = true
	}
	expired.ExpiredFilePayload = true
	expired.Coverage = CoveragePartial
	expired.CoverageGaps = append(expired.CoverageGaps, CoverageGap{Reason: GapExpiredPayload, Detail: "file recovery payload expired"})
	if err := s.persist(&expired); err != nil {
		return err
	}
	if s.dir != "" {
		legacyVisible := filepath.Join(s.dir, fmt.Sprintf("turn-%d.json", c.Turn))
		if err := os.Remove(legacyVisible); err != nil && !os.IsNotExist(err) {
			_ = os.Remove(s.checkpointPath(&expired))
			return err
		}
	}
	*c = expired
	return nil
}

// NextTurn returns the turn number a new checkpoint should take: one past the
// highest existing turn (0 when empty), so a resumed session keeps numbering
// without colliding with checkpoints loaded from disk.
func (s *Store) NextTurn() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := 0
	for _, c := range s.done {
		if c.Turn >= next {
			next = c.Turn + 1
		}
	}
	if s.cur != nil && s.cur.Turn >= next {
		next = s.cur.Turn + 1
	}
	return next
}

// List returns every checkpoint's metadata, oldest turn first.
func (s *Store) List() []Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Meta, 0, len(s.done)+1)
	for _, c := range s.all() {
		paths := make([]string, len(c.Files))
		for i, f := range c.Files {
			paths[i] = f.Path
		}
		meta := Meta{
			Turn:               c.Turn,
			Time:               c.Time,
			Prompt:             c.Prompt,
			Paths:              paths,
			Coverage:           c.Coverage,
			CoverageGaps:       append([]CoverageGap(nil), c.CoverageGaps...),
			ExpiredFilePayload: c.ExpiredFilePayload,
			ActiveWriters:      append([]ActiveWriter(nil), c.ActiveWriters...),
			Legacy:             c.Legacy || c.Coverage == CoverageLegacy,
		}
		switch {
		case meta.Legacy:
			meta.CanUndoFiles = false
			meta.DisabledReason = "legacy checkpoint cannot verify later manual edits"
		case meta.ExpiredFilePayload:
			meta.CanUndoFiles = false
			meta.DisabledReason = "file recovery payload expired"
		case meta.Coverage == CoverageNone:
			meta.CanUndoFiles = false
		case meta.Coverage == CoveragePartial:
			meta.CanUndoFiles = len(paths) > 0
		default:
			meta.CanUndoFiles = len(paths) > 0
		}
		out = append(out, meta)
	}
	return out
}

// FileState returns the earliest pre-edit state recorded for p across the
// session. Paths are compared after resolving them against the workspace root,
// because older checkpoints may contain absolute paths while newer writers use
// workspace-relative paths.
func (s *Store) FileState(p string) (FileState, bool) {
	want, err := safePath(s.root, p)
	if err != nil {
		return FileState{}, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var earliest *FileSnap
	var latestAfterSHA string
	var latestAfterExisted *bool
	for _, c := range s.all() {
		for _, f := range c.Files {
			got, err := safePath(s.root, f.Path)
			if err != nil || got != want {
				continue
			}
			if earliest == nil {
				copy := f
				earliest = &copy
			}
			// Ownership belongs to the final observed mutation, while the restore
			// payload remains the earliest preimage. A later capture without an
			// after fingerprint deliberately clears an older ownership proof.
			latestAfterSHA = f.AfterSHA256
			latestAfterExisted = f.AfterExisted
		}
	}
	if earliest == nil || earliest.PayloadExpired {
		return FileState{}, false
	}
	state := FileState{
		Encoding: earliest.Encoding,
		Mode:     earliest.Mode,
		SHA256:   earliest.SHA256,
		BlobRef:  earliest.BlobRef,
		Owned:    latestAfterSHA != "" || latestAfterExisted != nil,
	}
	if earliest.Content != nil {
		content := *earliest.Content
		state.Content = &content
	} else if earliest.BlobRef != "" && s.blobs != nil {
		if raw, err := s.blobs.Get(earliest.BlobRef); err == nil {
			enc, payload := fileenc.Detect(raw)
			text := string(fileenc.Decode(payload, enc))
			state.Content = &text
			state.Encoding = &enc
		}
	}
	return state, true
}

// all returns done + cur in turn order. Caller holds the lock.
func (s *Store) all() []*Checkpoint {
	cps := append([]*Checkpoint(nil), s.done...)
	if s.cur != nil {
		cps = append(cps, s.cur)
	}
	sort.Slice(cps, func(i, j int) bool { return cps[i].Turn < cps[j].Turn })
	return cps
}

// TruncateFrom discards checkpoints at or after fromTurn. Conversation rewind
// removes those future turns from the transcript, so their file snapshots must
// not remain visible or collide with newly-created checkpoints that reuse the
// same turn numbers after the rewrite.
func (s *Store) TruncateFrom(fromTurn int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	deleteTurns := map[int]bool{}
	for _, c := range s.done {
		if c.Turn >= fromTurn {
			deleteTurns[c.Turn] = true
		}
	}
	if s.cur != nil && s.cur.Turn >= fromTurn {
		deleteTurns[s.cur.Turn] = true
	}
	if err := s.removeTurnArtifacts(deleteTurns); err != nil {
		return err
	}

	done := s.done[:0]
	for _, c := range s.done {
		if c.Turn >= fromTurn {
			continue
		}
		done = append(done, c)
	}
	for i := len(done); i < len(s.done); i++ {
		s.done[i] = nil
	}
	s.done = done
	if s.cur != nil && s.cur.Turn >= fromTurn {
		s.cur = nil
		s.seen = map[string]bool{}
	}
	return nil
}

// RestoreCode reverts the workspace to its state at the start of turn `fromTurn`
// using a transactional prepare+commit. Legacy checkpoints are refused because
// they cannot prove that a later manual edit is safe to overwrite. Returns the
// paths written and deleted.
//
// On any failure after partial publish, compensation restores the pre-rewind
// workspace. Unlike the pre-v2 loop, a mid-way error does not leave a half-applied
// restore.
func (s *Store) RestoreCode(fromTurn int) (written, deleted []string, err error) {
	plan, err := s.PrepareRewind(fromTurn, RewindCode, 0, 0, false)
	if err != nil {
		return nil, nil, err
	}
	if plan.Legacy && len(plan.Files) > 0 {
		return nil, nil, fmt.Errorf("legacy checkpoint cannot safely restore files without explicit conflict confirmation")
	}
	// When complete/partial with no conflicts, commit.
	if !plan.CanFiles && !plan.Legacy {
		if plan.DisabledReason != "" {
			return nil, nil, fmt.Errorf("%s", plan.DisabledReason)
		}
		if len(plan.Conflicts) > 0 {
			return nil, nil, fmt.Errorf("file conflicts detected")
		}
		// No files — success no-op.
		return nil, nil, nil
	}
	result, err := s.CommitRewindWithForward(plan.PlanID, nil, nil, nil)
	if err != nil {
		return result.Written, result.Deleted, err
	}
	return result.Written, result.Deleted, nil
}

func (s *Store) detectCurrentEncoding(path string) *fileenc.Kind {
	b, err := secureReadFile(s.root, path)
	if err != nil {
		return nil
	}
	enc, _ := fileenc.Detect(b)
	return &enc
}

// safePath resolves p against root and rejects anything escaping it — restore
// must never write outside the workspace, even if a snapshot path is hostile or
// the project moved since it was taken.
func safePath(root, p string) (string, error) {
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, p)
	}
	abs = filepath.Clean(abs)
	if root != "" {
		if err := validateWorkspacePath(root, abs); err != nil {
			return "", err
		}
	}
	return abs, nil
}

var errSymlinkPath = errors.New("workspace path contains symbolic link")

func workspaceRelative(root, abs string) (string, error) {
	if root == "" {
		return filepath.Clean(abs), nil
	}
	r := filepath.Clean(root)
	rel, err := filepath.Rel(r, filepath.Clean(abs))
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("checkpoint path %q escapes workspace %q", abs, root)
	}
	return rel, nil
}

func splitLocalPath(rel string) []string {
	var parts []string
	for rel != "." && rel != "" {
		dir, base := filepath.Split(rel)
		if base != "" {
			parts = append([]string{base}, parts...)
		}
		rel = filepath.Clean(dir)
		if rel == string(filepath.Separator) {
			break
		}
	}
	return parts
}

func validateWorkspacePath(root, abs string) error {
	rel, err := workspaceRelative(root, abs)
	if err != nil {
		return err
	}
	cur := filepath.Clean(root)
	for _, part := range splitLocalPath(rel) {
		cur = filepath.Join(cur, part)
		info, statErr := os.Lstat(cur)
		if os.IsNotExist(statErr) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", errSymlinkPath, cur)
		}
	}
	return nil
}

func writeNewFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	remove = false
	return nil
}
