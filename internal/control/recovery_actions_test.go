package control

import (
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func controllerWithPendingRecovery(t *testing.T, r *provider.InterruptedTurnRecovery) (*Controller, *agent.Session) {
	t.Helper()
	dir := t.TempDir()
	session := agent.NewSession("system")
	session.AddBatch(
		provider.Message{Role: provider.RoleUser, Content: "ship it"},
		provider.Message{
			Role: provider.RoleTool, LocalOnly: true,
			ToolCallID: provider.LocalOnlyToolID, Name: provider.LocalOnlyToolName,
			InterruptedTurn: r,
		},
	)
	exec := agent.New(nil, tool.NewRegistry(), session, agent.Options{}, event.Discard)
	c := New(Options{Runner: &fakeTurnRunner{}, Executor: exec, SessionDir: dir,
		SessionPath: filepath.Join(dir, "session.jsonl")})
	t.Cleanup(c.Close)
	return c, session
}

func TestConfirmRecoveredToolPersistsTheAttestation(t *testing.T) {
	c, session := controllerWithPendingRecovery(t, &provider.InterruptedTurnRecovery{
		Pending: true, RequiresUserDecision: true,
		UnknownTools: []provider.InterruptedToolSummary{{ID: "commit", Name: "bash"}},
		ToolCalls:    []provider.ToolCallRecord{{ID: "commit", Name: "bash", Arguments: []byte(`{"command":"git commit"}`)}},
	})
	before := session.Len()

	if err := c.ConfirmRecoveredTool("commit"); err != nil {
		t.Fatalf("ConfirmRecoveredTool: %v", err)
	}
	pending := c.PendingRecovery()
	if pending == nil || len(pending.UserConfirmedTools) != 1 || pending.RequiresUserDecision {
		t.Fatalf("pending recovery = %+v", pending)
	}
	if session.Len() != before {
		t.Fatal("attestation added a transcript message")
	}

	reloaded, err := agent.LoadSession(c.SessionPath())
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	var durable *provider.InterruptedTurnRecovery
	for _, m := range reloaded.Snapshot() {
		if m.LocalOnly && m.InterruptedTurn != nil {
			durable = m.InterruptedTurn
		}
	}
	if durable == nil || len(durable.UserConfirmations) != 1 || durable.UserConfirmations[0].CallID != "commit" {
		t.Fatalf("attestation did not survive a reload: %+v", durable)
	}
	if len(durable.UnknownTools) != 0 {
		t.Fatalf("reloaded handoff still demands a decision: %+v", durable.UnknownTools)
	}
}

func TestConfirmRecoveredToolRejectsUnknownAndProvenCalls(t *testing.T) {
	c, _ := controllerWithPendingRecovery(t, &provider.InterruptedTurnRecovery{
		Pending:        true,
		CompletedTools: []provider.InterruptedToolSummary{{ID: "edit", Name: "edit_file"}},
	})
	for _, id := range []string{"edit", "absent"} {
		if err := c.ConfirmRecoveredTool(id); err == nil {
			t.Fatalf("confirmed %q, which the host already proved or never saw", id)
		}
	}
}

func TestPendingRecoveryIgnoresConsumedHandoffs(t *testing.T) {
	c, _ := controllerWithPendingRecovery(t, &provider.InterruptedTurnRecovery{
		Pending: false, UnknownTools: []provider.InterruptedToolSummary{{ID: "commit", Name: "bash"}},
	})
	if got := c.PendingRecovery(); got != nil {
		t.Fatalf("consumed handoff surfaced as pending: %+v", got)
	}
	if err := c.ConfirmRecoveredTool("commit"); err == nil {
		t.Fatal("confirmed a call from a consumed handoff")
	}
}
