package provider

import (
	"encoding/json"
	"strings"
)

// ToolRunState is local execution evidence; it is never sent as a wire field.
// Unknown legacy results are classified conservatively when interrupted.
type ToolRunState string

const (
	ToolRunPending   ToolRunState = "pending"
	ToolRunRunning   ToolRunState = "running"
	ToolRunCompleted ToolRunState = "completed"
	ToolRunFailed    ToolRunState = "failed"
	ToolRunCancelled ToolRunState = "cancelled"
	ToolRunUnknown   ToolRunState = "unknown"
	// ToolRunNotStarted is retained for old transcripts and recovery sidecars.
	// New records use ToolRunCancelled or ToolRunPending.
	ToolRunNotStarted ToolRunState = "not_started"
	// ToolRunUserConfirmed is an unknown outcome a user attested to after
	// inspecting the workspace. It is deliberately distinct from completed:
	// the host never saw the result and must not claim it did.
	ToolRunUserConfirmed ToolRunState = "completed_by_user_confirmation"
)

// ToolCallRecord is the local, provider-excluded execution receipt. Arguments
// are persisted for an explicit user retry, never copied into the model-facing
// recovery block. Keeping this record local lets the host distinguish a tool
// that did not start from one whose side effect may already exist.
type ToolCallRecord struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Arguments      json.RawMessage `json:"arguments,omitempty"`
	State          ToolRunState    `json:"state"`
	ReadOnly       bool            `json:"read_only"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	StartedAt      int64           `json:"started_at,omitempty"`
	FinishedAt     int64           `json:"finished_at,omitempty"`
	ExitCode       *int            `json:"exit_code,omitempty"`
	ResultDigest   string          `json:"result_digest,omitempty"`
	EffectSummary  string          `json:"effect_summary,omitempty"`
}

func ToolResultRunState(m Message) ToolRunState {
	switch m.ToolRunState {
	case ToolRunPending, ToolRunRunning, ToolRunCompleted, ToolRunFailed, ToolRunCancelled, ToolRunNotStarted, ToolRunUnknown:
		return m.ToolRunState
	case "":
	default:
		return ToolRunUnknown
	}
	text := strings.ToLower(strings.TrimSpace(m.Content))
	if strings.Contains(text, "write outcome unknown:") || text == interruptedToolResult {
		return ToolRunUnknown
	}
	if strings.HasPrefix(text, "cancelled: context cancelled before execution") || strings.HasPrefix(text, "cancelled: tool dispatch was not durable") {
		return ToolRunCancelled
	}
	if strings.HasPrefix(text, "cancelled:") || strings.Contains(text, "context canceled") || strings.Contains(text, "context cancelled") {
		return ToolRunUnknown
	}
	return ToolRunCompleted
}

// RecordToolRecovery retains legacy interrupted names for older readers while
// new readers distinguish calls proven not to have run from uncertain effects.
func RecordToolRecovery(r *InterruptedTurnRecovery, call InterruptedToolSummary, state ToolRunState) {
	if call.Name == "" {
		return
	}
	switch state {
	case ToolRunCompleted:
		r.CompletedTools = append(r.CompletedTools, call)
	case ToolRunFailed:
		r.FailedTools = append(r.FailedTools, call)
	case ToolRunUserConfirmed:
		r.UserConfirmedTools = append(r.UserConfirmedTools, call)
	case ToolRunCancelled:
		r.CancelledTools = append(r.CancelledTools, call)
		r.NotStartedTools = append(r.NotStartedTools, call)
		r.InterruptedTools = append(r.InterruptedTools, call.Name)
	case ToolRunNotStarted, ToolRunPending:
		r.NotStartedTools = append(r.NotStartedTools, call)
		r.InterruptedTools = append(r.InterruptedTools, call.Name)
	default:
		r.UnknownTools = append(r.UnknownTools, call)
		r.InterruptedTools = append(r.InterruptedTools, call.Name)
	}
}

// InterruptedTurnRecovery is the durable,
// provider-excluded handoff for an unfinished turn. It contains bounded facts;
// raw partial reasoning remains local for display.
type InterruptedTurnRecovery struct {
	TurnID                  string                   `json:"turn_id,omitempty"`
	AttemptID               string                   `json:"attempt_id,omitempty"`
	Cause                   string                   `json:"cause,omitempty"`
	WriteChecks             []WriteRecoveryCheck     `json:"write_checks,omitempty"`
	SatisfiedWrites         []InterruptedToolSummary `json:"satisfied_writes,omitempty"`
	Pending                 bool                     `json:"pending,omitempty"`
	CompletedTools          []InterruptedToolSummary `json:"completed_tools,omitempty"`
	FailedTools             []InterruptedToolSummary `json:"failed_tools,omitempty"`
	UserConfirmedTools      []InterruptedToolSummary `json:"user_confirmed_tools,omitempty"`
	UserConfirmations       []UserToolConfirmation   `json:"user_confirmations,omitempty"`
	InterruptedTools        []string                 `json:"interrupted_tools,omitempty"`
	NotStartedTools         []InterruptedToolSummary `json:"not_started_tools,omitempty"`
	UnknownTools            []InterruptedToolSummary `json:"unknown_tools,omitempty"`
	CancelledTools          []InterruptedToolSummary `json:"cancelled_tools,omitempty"`
	ToolCalls               []ToolCallRecord         `json:"tool_calls,omitempty"`
	RequiresUserDecision    bool                     `json:"requires_user_decision,omitempty"`
	DisplayOnlyPartial      bool                     `json:"display_only_partial,omitempty"`
	SilentInterruption      bool                     `json:"silent_interruption,omitempty"`
	DroppedPartialText      bool                     `json:"dropped_partial_text,omitempty"`
	DroppedPartialReasoning bool                     `json:"dropped_partial_reasoning,omitempty"`
}

// InterruptedToolSummary records a completed, fully paired tool call without duplicating arguments or results.
// The canonical assistant/tool messages immediately before the recovery record remain the source of truth.
type InterruptedToolSummary struct {
	ID      string   `json:"id,omitempty"`
	Name    string   `json:"name"`
	Files   []string `json:"files,omitempty"`
	Added   int      `json:"added,omitempty"`
	Removed int      `json:"removed,omitempty"`
}

// WriteRecoveryCheck reports a current postcondition, not an execution receipt.
type WriteRecoveryCheck struct {
	CallID string `json:"call_id"`
	Path   string `json:"path"`
	State  string `json:"state"`
}

// UserToolConfirmation is the durable provenance of one manual attestation.
// It records who resolved an unknown outcome and when, never a tool result.
type UserToolConfirmation struct {
	CallID      string `json:"call_id"`
	Source      string `json:"source"`
	ConfirmedAt int64  `json:"confirmed_at"`
}

// IsInterruptedPlaceholder reports whether a tool result is the placeholder
// normalization backfills for an unanswered call. It carries no evidence about
// execution, so recovery must prefer any real proof over it rather than
// reading it as a genuine unknown outcome.
func IsInterruptedPlaceholder(m Message) bool {
	return m.Role == RoleTool && m.ToolRunState == "" && strings.TrimSpace(m.Content) == interruptedToolResult
}
