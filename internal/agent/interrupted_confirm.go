package agent

import (
	"fmt"
	"slices"
	"time"

	"reasonix/internal/provider"
)

// ConfirmInterruptedTool resolves one unknown outcome from user attestation.
// It only changes state and records provenance: no tool result is fabricated,
// so the model still has to re-read the workspace before relying on the
// effect. Only calls the host could not prove are eligible.
func (a *Agent) ConfirmInterruptedTool(callID, source string) error {
	if a == nil || a.sess.conversation == nil {
		return fmt.Errorf("no session")
	}
	if callID == "" {
		return fmt.Errorf("empty tool call id")
	}
	msgs := a.sess.conversation.Snapshot()
	for i, v := range slices.Backward(msgs) {
		m := v
		if IsUserAuthoredTurnMessage(m) {
			break
		}
		if !m.LocalOnly || m.InterruptedTurn == nil || !m.InterruptedTurn.Pending {
			continue
		}
		updated, err := confirmUnknownTool(m.InterruptedTurn, callID, source)
		if err != nil {
			return err
		}
		next := append([]provider.Message(nil), msgs...)
		next[i].InterruptedTurn = updated
		a.sess.conversation.ReplaceLocalMetadata(next)
		return nil
	}
	return fmt.Errorf("no pending interrupted turn owns tool call %s", callID)
}

// confirmUnknownTool returns a copy with the call moved out of the unknown
// bucket. RequiresUserDecision survives only while another unknown remains.
func confirmUnknownTool(r *provider.InterruptedTurnRecovery, callID, source string) (*provider.InterruptedTurnRecovery, error) {
	index := slices.IndexFunc(r.UnknownTools, func(c provider.InterruptedToolSummary) bool { return c.ID == callID })
	if index < 0 {
		return nil, fmt.Errorf("tool call %s has no unknown outcome to confirm", callID)
	}
	next := *r
	confirmed := r.UnknownTools[index]
	next.UnknownTools = slices.Delete(append([]provider.InterruptedToolSummary(nil), r.UnknownTools...), index, index+1)
	next.UserConfirmedTools = append(append([]provider.InterruptedToolSummary(nil), r.UserConfirmedTools...), confirmed)
	next.InterruptedTools = slices.DeleteFunc(append([]string(nil), r.InterruptedTools...), func(name string) bool { return name == confirmed.Name })
	next.UserConfirmations = append(append([]provider.UserToolConfirmation(nil), r.UserConfirmations...),
		provider.UserToolConfirmation{CallID: callID, Source: source, ConfirmedAt: time.Now().UnixMilli()})
	next.ToolCalls = append([]provider.ToolCallRecord(nil), r.ToolCalls...)
	for i := range next.ToolCalls {
		if next.ToolCalls[i].ID == callID {
			next.ToolCalls[i].State = provider.ToolRunUserConfirmed
		}
	}
	next.RequiresUserDecision = len(next.UnknownTools) > 0
	return &next, nil
}
