package event

// TurnStatus is the authoritative lifecycle of one top-level turn.
type TurnStatus string

const (
	TurnQueued      TurnStatus = "queued"
	TurnInProgress  TurnStatus = "in_progress"
	TurnWaitingUser TurnStatus = "waiting_user"
	TurnCancelling  TurnStatus = "cancelling"
	TurnCompleted   TurnStatus = "completed"
	TurnInterrupted TurnStatus = "interrupted"
	// TurnRecoveryRequired means the host cannot prove the outcome of one or
	// more in-flight operations. It is terminal for this attempt, but requires
	// an explicit recovery action before another write-capable turn.
	TurnRecoveryRequired TurnStatus = "recovery_required"
	TurnUnknown          TurnStatus = "unknown"
	TurnFailed           TurnStatus = "failed"
	// TurnProtocolFailed is retained for replaying ledgers written by releases
	// that required the model-visible finish tool. New turns do not emit it.
	TurnProtocolFailed TurnStatus = "protocol_failed"
)

func (s TurnStatus) Terminal() bool {
	switch s {
	case TurnCompleted, TurnInterrupted, TurnRecoveryRequired, TurnUnknown, TurnFailed, TurnProtocolFailed:
		return true
	default:
		return false
	}
}
