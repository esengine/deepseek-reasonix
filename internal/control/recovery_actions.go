package control

import (
	"fmt"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// PendingRecovery is the interruption handoff a frontend renders, including
// the local argument receipts a deliberate re-run needs. It is display state:
// the model-facing block is built separately and never carries arguments.
func (c *Controller) PendingRecovery() *provider.InterruptedTurnRecovery {
	if c == nil || c.executor == nil {
		return nil
	}
	pending := c.executor.PendingInterruptedRecovery()
	if pending == nil || !pending.Pending {
		return nil
	}
	return pending
}

// ConfirmRecoveredTool resolves one unknown outcome from user attestation.
// The host never saw the result, so this records who said so and when; it
// does not synthesize a tool result, and the model is still told to verify.
func (c *Controller) ConfirmRecoveredTool(callID string) error {
	if c == nil || c.executor == nil {
		return fmt.Errorf("no executor")
	}
	if c.Running() {
		return fmt.Errorf("cannot resolve an interrupted tool while a turn is running")
	}
	if err := c.executor.ConfirmInterruptedTool(callID, "user"); err != nil {
		return err
	}
	if err := c.snapshot(false, true, false); err != nil {
		return fmt.Errorf("persist tool confirmation: %w", err)
	}
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
		Code: event.NoticeCodeRecoveredToolConfirmed,
		Text: "Marked an unverified tool effect as completed. The assistant will re-read the workspace instead of repeating it."})
	return nil
}
