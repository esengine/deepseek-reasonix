package main

import (
	"testing"

	"reasonix/internal/provider"
)

func TestHistoryRecoveryCardReportsProofNotGuesses(t *testing.T) {
	card := historyRecoveryCard(&provider.InterruptedTurnRecovery{
		Pending: true, TurnID: "turn_a", Cause: "runtime_restart",
		CompletedTools:     []provider.InterruptedToolSummary{{ID: "edit", Name: "edit_file", Files: []string{"a.go", "b.go"}}},
		UserConfirmedTools: []provider.InterruptedToolSummary{{ID: "tag", Name: "bash"}},
		FailedTools:        []provider.InterruptedToolSummary{{ID: "lint", Name: "bash"}},
		UnknownTools:       []provider.InterruptedToolSummary{{ID: "commit", Name: "bash"}},
		CancelledTools:     []provider.InterruptedToolSummary{{ID: "push", Name: "bash"}},
		ToolCalls: []provider.ToolCallRecord{
			{ID: "commit", Name: "bash", Arguments: []byte(`{"command":"git commit"}`)},
			{ID: "push", Name: "bash", Arguments: []byte(`{"command":"git push"}`)},
		},
	})
	if card == nil {
		t.Fatal("no card")
	}
	states := map[string]string{}
	args := map[string]string{}
	for _, tool := range card.Tools {
		states[tool.ID] = tool.State
		args[tool.ID] = tool.Arguments
	}
	want := map[string]string{
		"edit": string(provider.ToolRunCompleted), "tag": string(provider.ToolRunUserConfirmed),
		"lint": string(provider.ToolRunFailed), "commit": string(provider.ToolRunUnknown),
		"push": string(provider.ToolRunCancelled),
	}
	for id, state := range want {
		if states[id] != state {
			t.Fatalf("%s state = %q, want %q", id, states[id], state)
		}
	}
	if !card.RequiresUser {
		t.Fatal("an unknown effect must ask the user to decide")
	}
	// Arguments are the whole point of the card for a long write: the model
	// never sees them, the user must.
	if args["commit"] != `{"command":"git commit"}` || args["push"] != `{"command":"git push"}` {
		t.Fatalf("card dropped the retry arguments: %v", args)
	}
	if card.Tools[0].Effect != "a.go (+more)" {
		t.Fatalf("effect summary = %q", card.Tools[0].Effect)
	}
}

func TestHistoryRecoveryCardMarksSilentInterruption(t *testing.T) {
	card := historyRecoveryCard(&provider.InterruptedTurnRecovery{
		Pending: true, Cause: "runtime_restart", SilentInterruption: true,
	})
	if card == nil || !card.Silent {
		t.Fatalf("card = %+v, want a silent interruption", card)
	}
	if card.RequiresUser || len(card.Tools) != 0 {
		t.Fatalf("nothing ran, yet the card demands a decision: %+v", card)
	}
	if historyRecoveryCard(nil) != nil {
		t.Fatal("nil handoff produced a card")
	}
}
