package checkpoint

import (
	"time"

	fileenc "reasonix/internal/fileutil/encoding"
)

// Schema versions for on-disk checkpoint JSON.
const (
	SchemaV1 = 1
	SchemaV2 = 2
	SchemaV3 = 3
)

// Coverage describes how completely a checkpoint captured workspace mutations.
type Coverage string

const (
	CoverageComplete Coverage = "complete"
	CoveragePartial  Coverage = "partial"
	CoverageNone     Coverage = "none"
	CoverageLegacy   Coverage = "legacy"
)

// CoverageGap records why a checkpoint cannot guarantee full file restore.
type CoverageGap struct {
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Path   string `json:"path,omitempty"`
}

// Common coverage-gap reasons.
const (
	GapBashSideEffect   = "bash_side_effect"
	GapHookWrite        = "hook_write"
	GapMCPExternal      = "mcp_external"
	GapScratch          = "scratch"
	GapOutsideWorkspace = "outside_workspace"
	GapSymlink          = "symlink"
	GapHardlink         = "hardlink"
	GapUnreadable       = "unreadable"
	GapOversized        = "oversized"
	GapBackgroundWriter = "background_writer_cross_turn"
	GapLegacyUnverified = "legacy_unverified"
	GapCaptureFailed    = "capture_failed"
	GapExpiredPayload   = "expired_file_payload"
)

// HasProjectCoverageGap reports a gap that can prevent restoring workspace
// files. Scratch-only gaps do not.
func HasProjectCoverageGap(gaps []CoverageGap) bool {
	for _, gap := range gaps {
		if gap.Reason != "" && gap.Reason != GapScratch {
			return true
		}
	}
	return false
}

// CaptureSource identifies how a preimage was obtained.
type CaptureSource string

const (
	CapturePreviewer      CaptureSource = "previewer"
	CaptureBeforeMutation CaptureSource = "before_mutation"
	CaptureAfterMutation  CaptureSource = "after_mutation"
	CaptureLegacy         CaptureSource = "legacy"
	CaptureManual         CaptureSource = "manual"
)

// FileRevision is the v2 per-file preimage plus last Reasonix-owned after fingerprint.
type FileRevision struct {
	Path          string        `json:"path"`
	Existed       bool          `json:"existed"`
	Mode          uint32        `json:"mode,omitempty"`
	Encoding      *fileenc.Kind `json:"encoding,omitempty"`
	SHA256        string        `json:"sha256,omitempty"`
	BlobRef       string        `json:"blobRef,omitempty"`
	CaptureSource CaptureSource `json:"captureSource,omitempty"`
	// AfterSHA256 is the fingerprint of the file after the last Reasonix-owned write.
	// Empty means "no after fingerprint" (legacy or never observed).
	AfterSHA256  string `json:"afterSha256,omitempty"`
	AfterExisted *bool  `json:"afterExisted,omitempty"`
	AfterMode    uint32 `json:"afterMode,omitempty"`
	// Inline content is only used for in-memory stores without a blob dir, and
	// for legacy v1 migration paths. Persisted v2 checkpoints prefer BlobRef.
	Content *string `json:"content,omitempty"`
}

// MutationRecord tracks one observed mutation for ownership and conflict detection.
type MutationRecord struct {
	Seq       int64         `json:"seq"`
	Path      string        `json:"path"`
	Tool      string        `json:"tool,omitempty"`
	Source    CaptureSource `json:"source,omitempty"`
	WriterID  string        `json:"writerId,omitempty"`
	Turn      int           `json:"turn"`
	BeforeSHA string        `json:"beforeSha,omitempty"`
	AfterSHA  string        `json:"afterSha,omitempty"`
	Time      time.Time     `json:"time,omitempty"`
}

// ActiveWriter describes a background writer that still owns open mutations.
type ActiveWriter struct {
	ID        string    `json:"id"`
	Turn      int       `json:"turn"`
	StartedAt time.Time `json:"startedAt,omitempty"`
	Kind      string    `json:"kind,omitempty"` // "background_subagent", ...
}

// RewindScope selects what a rewind restores. Mirrors control.RewindScope without
// importing control (checkpoint is a lower layer).
type RewindScope int

const (
	RewindCode         RewindScope = iota // files only
	RewindConversation                    // message log only
	RewindBoth                            // both
)

// RewindConflict describes a file that cannot be safely restored.
type RewindConflict struct {
	Path            string `json:"path"`
	Reason          string `json:"reason"`
	CheckpointSHA   string `json:"checkpointSha,omitempty"`
	LastOwnedSHA    string `json:"lastOwnedSha,omitempty"`
	CurrentSHA      string `json:"currentSha,omitempty"`
	CheckpointMode  uint32 `json:"checkpointMode,omitempty"`
	CurrentMode     uint32 `json:"currentMode,omitempty"`
	CurrentExisted  bool   `json:"currentExisted"`
	CheckpointExist bool   `json:"checkpointExisted"`
}

// Conflict reason constants.
const (
	ConflictManualEdit      = "manual_edit"
	ConflictExternalChange  = "external_change"
	ConflictDeletedRecreate = "deleted_and_recreated"
	ConflictTypeChange      = "type_change"
	ConflictModeChange      = "mode_change"
	ConflictMissingPayload  = "missing_payload"
	ConflictPathUnsafe      = "path_unsafe"
	ConflictBusyWriter      = "active_writer"
	ConflictStalePlan       = "stale_plan"
	ConflictBoundaryInvalid = "boundary_invalid"
	ConflictCoverageLegacy  = "legacy_unverified"
	ConflictExpired         = "expired_payload"
)

// FileStage records per-file progress through a rewind transaction.
type FileStage struct {
	Path        string `json:"path"`
	Phase       string `json:"phase"`            // precheck|prepare|commit|compensate|done|skipped
	Action      string `json:"action,omitempty"` // write|delete|restore
	Error       string `json:"error,omitempty"`
	Compensated bool   `json:"compensated,omitempty"`
	CompError   string `json:"compensateError,omitempty"`
}

// RewindPlan is the structured precheck result returned to the controller/UI.
type RewindPlan struct {
	PlanID             string           `json:"planId"`
	Turn               int              `json:"turn"`
	Scope              RewindScope      `json:"scope"`
	Coverage           Coverage         `json:"coverage"`
	CoverageGaps       []CoverageGap    `json:"coverageGaps,omitempty"`
	Legacy             bool             `json:"legacy,omitempty"`
	ExpiredFilePayload bool             `json:"expiredFilePayload,omitempty"`
	CanFiles           bool             `json:"canFiles"`
	CanConversation    bool             `json:"canConversation"`
	DisabledReason     string           `json:"disabledReason,omitempty"`
	Conflicts          []RewindConflict `json:"conflicts,omitempty"`
	Files              []string         `json:"files,omitempty"`
	FileCount          int              `json:"fileCount"`
	ActiveWriters      []ActiveWriter   `json:"activeWriters,omitempty"`
	SessionRevision    int64            `json:"sessionRevision"`
	WorkspaceToken     string           `json:"workspaceToken,omitempty"`
	BoundaryIndex      int              `json:"boundaryIndex,omitempty"`
	HasBoundary        bool             `json:"hasBoundary"`
	CreatedAt          time.Time        `json:"createdAt"`
	ConversationAction string           `json:"conversationAction,omitempty"`
	// Single-file revert extras.
	Path               string `json:"path,omitempty"`
	ConflictResolution string `json:"conflictResolution,omitempty"`
}

// RewindResult is returned after commit or undo.
type RewindResult struct {
	OK                 bool             `json:"ok"`
	TransactionID      string           `json:"transactionId,omitempty"`
	UndoAvailable      bool             `json:"undoAvailable"`
	Written            []string         `json:"written,omitempty"`
	Deleted            []string         `json:"deleted,omitempty"`
	Files              []FileStage      `json:"files,omitempty"`
	ConversationOK     bool             `json:"conversationOk,omitempty"`
	ConversationForked bool             `json:"conversationForked,omitempty"`
	OperationID        string           `json:"operationId,omitempty"`
	Branch             string           `json:"branch,omitempty"`
	Partial            bool             `json:"partial,omitempty"`
	Error              string           `json:"error,omitempty"`
	Conflicts          []RewindConflict `json:"conflicts,omitempty"`
	Coverage           Coverage         `json:"coverage,omitempty"`
	CoverageGaps       []CoverageGap    `json:"coverageGaps,omitempty"`
}

// ConflictResolution chooses how to handle a single-file conflict on commit.
type ConflictResolution string

const (
	// ResolveKeepCurrent leaves the on-disk file alone.
	ResolveKeepCurrent ConflictResolution = "keep_current"
	// ResolveOverwriteCheckpoint force-writes the checkpoint preimage after
	// the user explicitly confirmed in the single-file UI.
	ResolveOverwriteCheckpoint ConflictResolution = "overwrite_checkpoint"
)

// TransactionState is the durable lifecycle of a rewind transaction.
type TransactionState string

const (
	TxPrepared   TransactionState = "prepared"
	TxCommitting TransactionState = "committing"
	TxCommitted  TransactionState = "committed"
	TxAborted    TransactionState = "aborted"
	TxUndone     TransactionState = "undone"
)

// TransactionTarget is one file's forward/restore payload inside a transaction.
type TransactionTarget struct {
	Path    string `json:"path"`
	AbsPath string `json:"absPath"`
	// Restore: what to write (or delete) to reach checkpoint state.
	RestoreExisted  bool          `json:"restoreExisted"`
	RestoreMode     uint32        `json:"restoreMode,omitempty"`
	RestoreSHA      string        `json:"restoreSha,omitempty"`
	RestoreBlob     string        `json:"restoreBlob,omitempty"`
	RestoreInline   []byte        `json:"restoreInline,omitempty"`
	RestoreEncoding *fileenc.Kind `json:"restoreEncoding,omitempty"`
	// Forward: current on-disk state at prepare time (for compensate / undo).
	ForwardExisted bool   `json:"forwardExisted"`
	ForwardMode    uint32 `json:"forwardMode,omitempty"`
	ForwardSHA     string `json:"forwardSha,omitempty"`
	ForwardBlob    string `json:"forwardBlob,omitempty"`
	ForwardInline  []byte `json:"forwardInline,omitempty"`
	// Staging paths are transaction-unique siblings of AbsPath so publish and
	// compensation never cross filesystems.
	PublishTmp string `json:"publishTmp,omitempty"`
	BackupPath string `json:"backupPath,omitempty"`
	// Published is a durable "may have published" intent. It is persisted before
	// the first rename so crash recovery conservatively inspects this target.
	Published bool `json:"published"`
	// Action describes the intended commit action.
	Action string `json:"action"` // write|delete
}

// TransactionManifest is the durable description of a rewind/undo transaction.
type TransactionManifest struct {
	SchemaVersion   int                 `json:"schemaVersion"`
	ID              string              `json:"id"`
	SessionID       string              `json:"sessionId,omitempty"`
	WorkspaceRoot   string              `json:"workspaceRoot"`
	State           TransactionState    `json:"state"`
	Kind            string              `json:"kind"` // rewind|undo|file_revert
	Turn            int                 `json:"turn"`
	Scope           RewindScope         `json:"scope"`
	Path            string              `json:"path,omitempty"` // single-file
	CreatedAt       time.Time           `json:"createdAt"`
	UpdatedAt       time.Time           `json:"updatedAt"`
	SessionRevision int64               `json:"sessionRevision"`
	WorkspaceToken  string              `json:"workspaceToken,omitempty"`
	Coverage        Coverage            `json:"coverage,omitempty"`
	CoverageGaps    []CoverageGap       `json:"coverageGaps,omitempty"`
	Targets         []TransactionTarget `json:"targets,omitempty"`
	// ConversationForward holds a JSON-encoded message snapshot when conversation
	// is part of the transaction. Opaque to this package so it can stay free of
	// provider imports; the controller supplies and applies it.
	ConversationForward []byte `json:"conversationForward,omitempty"`
	BoundaryIndex       int    `json:"boundaryIndex,omitempty"`
	HasBoundary         bool   `json:"hasBoundary"`
	ConversationAction  string `json:"conversationAction,omitempty"`
	// TruncateFrom is the checkpoint turn to drop after a successful conversation rewind.
	TruncateFrom int `json:"truncateFrom,omitempty"`
	// CheckpointTurns holds serialized future checkpoints for undo.
	CheckpointBackup []byte `json:"checkpointBackup,omitempty"`
	// ParentTransaction is set for undo transactions that reverse a committed rewind.
	ParentTransaction string `json:"parentTransaction,omitempty"`
	Error             string `json:"error,omitempty"`
}

// Default retention and soft byte budget for file payloads. Both v3 raw
// preimages and legacy blobs use the same budget value in their own stores.
const (
	DefaultRetainCheckpoints = 100
	DefaultBlobQuotaBytes    = 1 << 30  // 1 GiB
	DefaultMaxFileBytes      = 32 << 20 // 32 MiB per file capture
)

// Checkpoint anchors the pre-edit state of every distinct file touched during one
// user turn. MsgIndex is len(Session.Messages) at the turn's start — the
// conversation-rewind boundary — persisted so a resumed session can rewind the
// conversation and fork, not just the code.
type Checkpoint struct {
	SchemaVersion       int            `json:"schemaVersion,omitempty"`
	Turn                int            `json:"turn"`
	Time                time.Time      `json:"time"`
	Prompt              string         `json:"prompt"`
	MsgIndex            int            `json:"msgIndex"`
	SessionID           string         `json:"sessionId,omitempty"`
	Files               []FileSnap     `json:"files"`
	Coverage            Coverage       `json:"coverage,omitempty"`
	CoverageGaps        []CoverageGap  `json:"coverageGaps,omitempty"`
	ActiveWriters       []ActiveWriter `json:"activeWriters,omitempty"`
	LastMutationSeq     int64          `json:"lastMutationSeq,omitempty"`
	SessionRevision     int64          `json:"sessionRevision,omitempty"`
	TurnID              string         `json:"turnId,omitempty"`
	AttemptID           string         `json:"attemptId,omitempty"`
	ToolCallID          string         `json:"toolCallId,omitempty"`
	ToolArgumentsDigest string         `json:"toolArgumentsDigest,omitempty"`
	ProviderDigest      string         `json:"providerDigest,omitempty"`
	WorkspaceReference  string         `json:"workspaceReference,omitempty"`
	Legacy              bool           `json:"legacy,omitempty"`
	ExpiredFilePayload  bool           `json:"expiredFilePayload,omitempty"`
}
