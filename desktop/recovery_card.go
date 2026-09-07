package main

import "reasonix/internal/provider"

// HistoryRecoveryTool is one call in the interruption card. State is the
// host's proof about it, not a claim about what the tool produced.
type HistoryRecoveryTool struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	State     string `json:"state"`
	Effect    string `json:"effect,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// HistoryRecovery is the display-only interruption card. Arguments ride here
// so a user can inspect or re-issue a long write by hand — the model-facing
// recovery block is built separately and never carries them.
type HistoryRecovery struct {
	TurnID       string                `json:"turnId,omitempty"`
	Cause        string                `json:"cause,omitempty"`
	RequiresUser bool                  `json:"requiresUser,omitempty"`
	Silent       bool                  `json:"silentInterruption,omitempty"`
	Tools        []HistoryRecoveryTool `json:"tools,omitempty"`
}

// historyRecoveryCard projects one durable handoff for the frontend. Calls the
// host proved are listed with that proof; a call whose record is missing falls
// back to unknown so the card never understates what may have run.
func historyRecoveryCard(r *provider.InterruptedTurnRecovery) *HistoryRecovery {
	if r == nil {
		return nil
	}
	card := &HistoryRecovery{
		TurnID: r.TurnID, Cause: r.Cause,
		RequiresUser: r.RequiresUserDecision || len(r.UnknownTools) > 0,
		Silent:       r.SilentInterruption,
	}
	args := make(map[string]string, len(r.ToolCalls))
	for _, record := range r.ToolCalls {
		args[record.ID] = string(record.Arguments)
	}
	add := func(state provider.ToolRunState, calls []provider.InterruptedToolSummary) {
		for _, call := range calls {
			card.Tools = append(card.Tools, HistoryRecoveryTool{
				ID: call.ID, Name: call.Name, State: string(state),
				Effect: recoveryEffectSummary(call), Arguments: args[call.ID],
			})
		}
	}
	add(provider.ToolRunCompleted, r.CompletedTools)
	add(provider.ToolRunUserConfirmed, r.UserConfirmedTools)
	add(provider.ToolRunFailed, r.FailedTools)
	add(provider.ToolRunUnknown, r.UnknownTools)
	add(provider.ToolRunCancelled, r.CancelledTools)
	return card
}

func recoveryEffectSummary(call provider.InterruptedToolSummary) string {
	if len(call.Files) == 0 {
		return ""
	}
	summary := call.Files[0]
	if len(call.Files) > 1 {
		summary += " (+more)"
	}
	return summary
}
