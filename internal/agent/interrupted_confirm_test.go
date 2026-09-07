package agent

import (
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func sessionWithPendingRecovery(r *provider.InterruptedTurnRecovery) (*Agent, *Session) {
	s := NewSession("system")
	s.AddBatch(
		provider.Message{Role: provider.RoleUser, Content: "ship it"},
		provider.Message{
			Role: provider.RoleTool, LocalOnly: true,
			ToolCallID: provider.LocalOnlyToolID, Name: provider.LocalOnlyToolName,
			InterruptedTurn: r,
		},
	)
	return New(nil, tool.NewRegistry(), s, Options{}, event.Discard), s
}

func TestConfirmInterruptedToolAttestsWithoutFabricatingAResult(t *testing.T) {
	r := &provider.InterruptedTurnRecovery{
		Pending: true, RequiresUserDecision: true,
		UnknownTools:     []provider.InterruptedToolSummary{{ID: "commit", Name: "bash"}, {ID: "tag", Name: "bash"}},
		InterruptedTools: []string{"bash"},
		ToolCalls: []provider.ToolCallRecord{
			{ID: "commit", Name: "bash", Arguments: []byte(`{"command":"git commit"}`), State: provider.ToolRunUnknown},
			{ID: "tag", Name: "bash", Arguments: []byte(`{"command":"git tag"}`), State: provider.ToolRunUnknown},
		},
	}
	a, session := sessionWithPendingRecovery(r)
	before := session.Len()

	if err := a.ConfirmInterruptedTool("commit", "user"); err != nil {
		t.Fatalf("ConfirmInterruptedTool: %v", err)
	}
	got := a.PendingInterruptedRecovery()
	if len(got.UserConfirmedTools) != 1 || got.UserConfirmedTools[0].ID != "commit" {
		t.Fatalf("user confirmed = %+v", got.UserConfirmedTools)
	}
	if len(got.UnknownTools) != 1 || got.UnknownTools[0].ID != "tag" {
		t.Fatalf("unknown tools = %+v, want only the unconfirmed call", got.UnknownTools)
	}
	if !got.RequiresUserDecision {
		t.Fatal("a remaining unknown call must still require a decision")
	}
	if len(got.UserConfirmations) != 1 || got.UserConfirmations[0].CallID != "commit" ||
		got.UserConfirmations[0].Source != "user" || got.UserConfirmations[0].ConfirmedAt == 0 {
		t.Fatalf("provenance = %+v", got.UserConfirmations)
	}
	for _, record := range got.ToolCalls {
		if record.ID == "commit" && record.State != provider.ToolRunUserConfirmed {
			t.Fatalf("confirmed record state = %s", record.State)
		}
		if record.ID == "tag" && record.State != provider.ToolRunUnknown {
			t.Fatalf("untouched record state = %s", record.State)
		}
	}
	if session.Len() != before {
		t.Fatal("confirmation fabricated a transcript message")
	}
	for _, m := range session.Snapshot() {
		if m.Role == provider.RoleTool && m.ToolCallID == "commit" {
			t.Fatal("confirmation synthesized a tool result")
		}
	}

	if err := a.ConfirmInterruptedTool("tag", "user"); err != nil {
		t.Fatalf("second confirmation: %v", err)
	}
	if a.PendingInterruptedRecovery().RequiresUserDecision {
		t.Fatal("no unknown calls remain, yet a decision is still demanded")
	}
}

func TestConfirmInterruptedToolRefusesCallsTheHostAlreadyProved(t *testing.T) {
	r := &provider.InterruptedTurnRecovery{
		Pending:        true,
		CompletedTools: []provider.InterruptedToolSummary{{ID: "edit", Name: "edit_file"}},
		CancelledTools: []provider.InterruptedToolSummary{{ID: "push", Name: "bash"}},
	}
	a, _ := sessionWithPendingRecovery(r)
	for _, id := range []string{"edit", "push", "absent"} {
		if err := a.ConfirmInterruptedTool(id, "user"); err == nil {
			t.Fatalf("confirmed %q, which has no unknown outcome", id)
		}
	}
}

func TestConfirmedEffectsAppearInTheBlockAsUserAttested(t *testing.T) {
	r := &provider.InterruptedTurnRecovery{Pending: true, RequiresUserDecision: true,
		UnknownTools: []provider.InterruptedToolSummary{{ID: "commit", Name: "bash"}},
		ToolCalls:    []provider.ToolCallRecord{{ID: "commit", Name: "bash", Arguments: []byte(`{"command":"git commit -m secret"}`)}},
	}
	a, _ := sessionWithPendingRecovery(r)
	if err := a.ConfirmInterruptedTool("commit", "user"); err != nil {
		t.Fatal(err)
	}
	block := interruptedRecoveryBlock(a.PendingInterruptedRecovery())
	if !strings.Contains(block, "user_confirmed_effects_do_not_repeat") || !strings.Contains(block, "id=commit") {
		t.Fatalf("block missing the attestation:\n%s", block)
	}
	if strings.Contains(block, "outcome_unknown_tools") {
		t.Fatalf("confirmed call still listed as unknown:\n%s", block)
	}
	if strings.Contains(block, "secret") {
		t.Fatalf("block leaked arguments:\n%s", block)
	}
}
