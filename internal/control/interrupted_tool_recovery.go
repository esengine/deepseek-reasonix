package control

import (
	"strings"

	"reasonix/internal/provider"
)

// interruptedTailEvidence is what the lifecycle ledger proved about a turn the
// runtime never finished: which unanswered calls crossed the start barrier.
type interruptedTailEvidence struct {
	turnID string
	states map[string]provider.ToolRunState
}

func (c *Controller) ledgerTailEvidence() *interruptedTailEvidence {
	orphan := c.turnEventLedger().OrphanRecovery()
	if orphan == nil {
		return nil
	}
	ev := &interruptedTailEvidence{turnID: orphan.TurnID, states: make(map[string]provider.ToolRunState, len(orphan.Tools))}
	for _, tool := range orphan.Tools {
		state := provider.ToolRunCancelled
		if tool.Started {
			state = provider.ToolRunUnknown
		}
		ev.states[tool.ID] = state
	}
	return ev
}

// applyInterruptedTailEvidence names the restart and marks a turn that died
// before producing anything as silent, so the next turn is told exactly that.
func applyInterruptedTailEvidence(r *provider.InterruptedTurnRecovery, ev *interruptedTailEvidence) {
	if ev == nil {
		return
	}
	if r.Cause == "" {
		r.Cause = "runtime_restart"
	}
	if r.TurnID == "" {
		r.TurnID = ev.turnID
	}
	produced := len(r.ToolCalls) > 0 || len(r.CompletedTools) > 0 || r.DroppedPartialText || r.DroppedPartialReasoning
	r.SilentInterruption = r.SilentInterruption || !produced
}

// A call without a stored result is unknown unless the ledger proves it never
// crossed the start barrier; a session that outran the ledger stays unknown.
func recordInterruptedAssistantRecovery(r *provider.InterruptedTurnRecovery, msgs []provider.Message, i int, ev *interruptedTailEvidence) {
	results := make(map[string]provider.Message)
	for j := i + 1; j < len(msgs) && msgs[j].Role == provider.RoleTool && !msgs[j].LocalOnly; j++ {
		result := msgs[j]
		results[result.ToolCallID+"\x00"+result.Name] = result
	}
	for _, call := range msgs[i].ToolCalls {
		state := provider.ToolRunUnknown
		result, stored := results[call.ID+"\x00"+call.Name]
		// Loading a session backfills a placeholder for every unanswered call,
		// so a stored result only counts as evidence when it is a real one.
		if stored && !provider.IsInterruptedPlaceholder(result) {
			state = provider.ToolResultRunState(result)
		} else if ev != nil {
			if proven, ok := ev.states[call.ID]; ok {
				state = proven
			}
		}
		provider.RecordToolRecovery(r, interruptedToolSummary(call), state)
		record := provider.ToolCallRecord{
			ID: call.ID, Name: call.Name, Arguments: append([]byte(nil), call.Arguments...), State: state,
		}
		if state == provider.ToolRunUnknown {
			r.RequiresUserDecision = true
		}
		r.ToolCalls = append(r.ToolCalls, record)
	}
}

// mergeInterruptedRecovery folds an earlier local handoff into the one being
// rebuilt after cancellation, so a second interruption keeps the first's facts.
func mergeInterruptedRecovery(dst, src *provider.InterruptedTurnRecovery) {
	dst.TurnID = src.TurnID
	dst.AttemptID = src.AttemptID
	dst.Cause = src.Cause
	dst.RequiresUserDecision = dst.RequiresUserDecision || src.RequiresUserDecision
	dst.SilentInterruption = dst.SilentInterruption || src.SilentInterruption
	dst.CompletedTools = append(dst.CompletedTools, src.CompletedTools...)
	dst.FailedTools = append(dst.FailedTools, src.FailedTools...)
	dst.InterruptedTools = append(dst.InterruptedTools, src.InterruptedTools...)
	dst.NotStartedTools = append(dst.NotStartedTools, src.NotStartedTools...)
	dst.UnknownTools = append(dst.UnknownTools, src.UnknownTools...)
	dst.CancelledTools = append(dst.CancelledTools, src.CancelledTools...)
	dst.ToolCalls = append(dst.ToolCalls, src.ToolCalls...)
}

// stampInterruptedTurnID binds the handoff to the ledger turn it interrupted.
// The strip runs before TurnDone lands, so the active turn is still that one.
func (c *Controller) stampInterruptedTurnID(r *provider.InterruptedTurnRecovery) {
	if r.TurnID != "" {
		return
	}
	if ledger := c.turnEventLedger(); ledger != nil {
		r.TurnID = ledger.ActiveTurnID()
	}
}

// localizeInterruptedMessage turns an unpaired assistant or tool message from
// the cancelled turn into a display-only carrier. Partial assistant output is
// recorded as dropped so the model-facing block can say it was excluded.
func localizeInterruptedMessage(m provider.Message, r *provider.InterruptedTurnRecovery) (provider.Message, bool) {
	local := m
	switch m.Role {
	case provider.RoleAssistant:
		local.Role = provider.RoleTool
		local.InterruptedTurn = nil
		r.DroppedPartialText = r.DroppedPartialText || strings.TrimSpace(m.Content) != ""
		r.DroppedPartialReasoning = r.DroppedPartialReasoning || strings.TrimSpace(m.ReasoningContent) != ""
	case provider.RoleTool:
		local.ToolCalls = []provider.ToolCall{{ID: m.ToolCallID, Name: m.Name}}
	default:
		return provider.Message{}, false
	}
	local.LocalOnly = true
	local.ToolCallID = provider.LocalOnlyToolID
	local.Name = provider.LocalOnlyToolName
	return local, true
}
