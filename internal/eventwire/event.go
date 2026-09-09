package eventwire

import (
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// Event is the JSON-friendly form shared by event frontends.
// externalizable:"true" marks large string payloads the Remote protocol may
// offload via content refs without changing provider-visible semantics.
type Event struct {
	RuntimeState     *event.RuntimeStateSnapshot      `json:"runtimeState,omitempty"`
	Kind             string                           `json:"kind"`
	MessageID        string                           `json:"messageId,omitempty"`
	AttemptID        string                           `json:"attemptId,omitempty"`
	Source           string                           `json:"source,omitempty"`
	SessionID        string                           `json:"sessionId,omitempty"`
	RuntimeEpoch     string                           `json:"runtimeEpoch,omitempty"`
	SubmissionID     string                           `json:"submissionId,omitempty"`
	PromptID         string                           `json:"promptId,omitempty"`
	PromptKind       string                           `json:"promptKind,omitempty"`
	PromptLegacy     bool                             `json:"promptLegacy,omitempty"`
	TurnID           string                           `json:"turnId,omitempty"`
	Sequence         uint64                           `json:"seq,omitempty"`
	Status           string                           `json:"status,omitempty"`
	Text             string                           `json:"text,omitempty" externalizable:"true"`
	Detail           string                           `json:"detail,omitempty" externalizable:"true"`
	Code             string                           `json:"code,omitempty"`
	Reasoning        string                           `json:"reasoning,omitempty" externalizable:"true"`
	MemoryCitations  []MemoryCitation                 `json:"memoryCitations,omitempty"`
	Level            string                           `json:"level,omitempty"`
	Tool             *Tool                            `json:"tool,omitempty"`
	ReadStatus       *ReadStatus                      `json:"readStatus,omitempty"`
	ReadPause        *provider.ReadPause              `json:"readPause,omitempty"`
	Usage            *Usage                           `json:"usage,omitempty"`
	Approval         *Approval                        `json:"approval,omitempty"`
	Ask              *Ask                             `json:"ask,omitempty"`
	MCPInteraction   *MCPInteraction                  `json:"mcpInteraction,omitempty"`
	Compaction       *Compaction                      `json:"compaction,omitempty"`
	Maintenance      *ContextMaintenance              `json:"maintenance,omitempty"`
	Guardian         *Guardian                        `json:"guardian,omitempty"`
	DecisionReceipt  *DecisionReceipt                 `json:"decisionReceipt,omitempty"`
	Extension        *ExtensionSurface                `json:"extension,omitempty"`
	Err              string                           `json:"err,omitempty" externalizable:"true"`
	Outcome          string                           `json:"outcome,omitempty"`
	Readiness        *FinalReadiness                  `json:"readiness,omitempty"`
	ProtocolRecovery *provider.ProtocolRecoveryAction `json:"protocolRecovery,omitempty"`
	Diagnostic       *provider.FailureDiagnostic      `json:"diagnostic,omitempty"`
	Receipt          *CompletionReceipt               `json:"receipt,omitempty"`
	CheckpointTurn   *int                             `json:"checkpointTurn,omitempty"`
	Recovery         *event.RecoveryStatus            `json:"recovery,omitempty"`
	RetryAttempt     int                              `json:"retryAttempt,omitempty"`
	RetryMax         int                              `json:"retryMax,omitempty"`
	RetryScope       string                           `json:"retryScope,omitempty"` // "headers" | "stream" | "protocol"; omit for older clients
	StreamAttempt    *StreamAttempt                   `json:"streamAttempt,omitempty"`
	// ItemID correlates Steer / TurnDone / unapplied-steer with a durable
	// session-inbox entry. Empty for legacy text-only guidance.
	ItemID string `json:"itemId,omitempty"`
	// SessionPath routes frames emitted by detached Serve controllers. Older
	// clients ignore the omitted/unknown field and keep single-session behavior.
	SessionPath string `json:"sessionPath,omitempty"`
	// SessionCurrent is set by Serve at publication time for frames belonging
	// to its foreground controller. It lets all-session clients adopt an
	// externally selected/recovered foreground without polling on every token.
	SessionCurrent bool `json:"sessionCurrent,omitempty"`
	// SessionReset distinguishes a fresh /new or /clear target from a resumed
	// durable session when a different client rotates the foreground.
	SessionReset bool              `json:"sessionReset,omitempty"`
	Workspace    *WorkspaceChanged `json:"workspace,omitempty"`
	// Phase is set on turn_phase events: working | checking | verifying | reviewing.
	Phase string `json:"phase,omitempty"`
	// Completion is set on completion_summary events (content-free quality summary).
	Completion *CompletionSummary `json:"completion,omitempty"`
}
