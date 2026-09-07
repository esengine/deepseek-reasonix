package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
	"reasonix/internal/turnevent"
)

// deadRuntimeLedger writes the lifecycle a runtime left behind when it died
// mid-turn: dispatched calls, a start barrier for some of them, no terminal.
func deadRuntimeLedger(t *testing.T, path string, started map[string]bool, calls ...provider.ToolCall) {
	t.Helper()
	l, err := turnevent.Open(path, agent.BranchID(path))
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	if _, err := l.Begin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	appendEvent := func(e event.Event) {
		t.Helper()
		if _, ok, err := l.Append(e, event.TurnInProgress); err != nil || !ok {
			t.Fatalf("append %v: ok=%v err=%v", e.Kind, ok, err)
		}
	}
	appendEvent(event.Event{Kind: event.TurnStarted})
	for _, call := range calls {
		tr := event.Tool{ID: call.ID, Name: call.Name, Args: call.Arguments}
		appendEvent(event.Event{Kind: event.ToolDispatch, Tool: tr})
		if started[call.ID] {
			appendEvent(event.Event{Kind: event.ToolStarted, Tool: tr})
		}
	}
	// Close only the file handle: the turn stays unterminated, as after a crash.
	if err := l.Close(); err != nil {
		t.Fatalf("close ledger: %v", err)
	}
}

func resumeAfterRestart(t *testing.T, dir, path string, msgs []provider.Message) *Controller {
	t.Helper()
	session := agent.NewSession("system")
	for _, m := range msgs {
		session.Add(m)
	}
	if err := session.Save(path); err != nil {
		t.Fatalf("save session: %v", err)
	}
	if _, err := agent.BeginSessionInFlightTurn(path, 1, true); err != nil {
		t.Fatalf("mark in-flight turn: %v", err)
	}
	// Load from disk like a real resume: a hand-built session carries no
	// persistence baseline, and the rewrite would fork as a foreign writer.
	loaded, err := agent.LoadSession(path)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	exec := agent.New(nil, tool.NewRegistry(), agent.NewSession("system"), agent.Options{}, event.Discard)
	c := New(Options{Runner: &fakeTurnRunner{}, Executor: exec, SessionDir: dir, SessionPath: path})
	t.Cleanup(c.Close)
	c.Resume(loaded, path)
	return c
}

func pendingRecovery(t *testing.T, c *Controller) *provider.InterruptedTurnRecovery {
	t.Helper()
	for _, m := range c.executor.Session().Snapshot() {
		if m.LocalOnly && m.InterruptedTurn != nil {
			return m.InterruptedTurn
		}
	}
	t.Fatal("resumed session carries no interruption handoff")
	return nil
}

func TestRestartClassifiesUnansweredCallsByLedgerStartBarrier(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	commit := provider.ToolCall{ID: "commit", Name: "bash", Arguments: `{"command":"git commit"}`}
	push := provider.ToolCall{ID: "push", Name: "bash", Arguments: `{"command":"git push"}`}
	deadRuntimeLedger(t, path, map[string]bool{"commit": true}, commit, push)

	c := resumeAfterRestart(t, dir, path, []provider.Message{
		{Role: provider.RoleUser, Content: "ship it"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{commit, push}},
	})
	recovery := pendingRecovery(t, c)

	if len(recovery.UnknownTools) != 1 || recovery.UnknownTools[0].ID != "commit" {
		t.Fatalf("unknown tools = %+v, want only the started commit", recovery.UnknownTools)
	}
	if len(recovery.CancelledTools) != 1 || recovery.CancelledTools[0].ID != "push" {
		t.Fatalf("cancelled tools = %+v, want only the never-started push", recovery.CancelledTools)
	}
	if !recovery.RequiresUserDecision {
		t.Fatal("an unproven side effect must require a user decision")
	}
	if recovery.SilentInterruption {
		t.Fatal("a turn that dispatched tools is not a silent interruption")
	}
	if recovery.Cause != "runtime_restart" {
		t.Fatalf("cause = %q, want runtime_restart", recovery.Cause)
	}
	args := map[string]string{}
	for _, record := range recovery.ToolCalls {
		args[record.ID] = string(record.Arguments)
	}
	if args["push"] != `{"command":"git push"}` {
		t.Fatalf("retry arguments lost for the safely retryable call: %v", args)
	}
}

func TestRestartBeforeAnyOutputIsReportedAsSilent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	deadRuntimeLedger(t, path, nil)

	c := resumeAfterRestart(t, dir, path, []provider.Message{{Role: provider.RoleUser, Content: "ship it"}})
	recovery := pendingRecovery(t, c)

	if !recovery.SilentInterruption {
		t.Fatalf("recovery = %+v, want a silent interruption", recovery)
	}
	if recovery.RequiresUserDecision || len(recovery.UnknownTools) != 0 {
		t.Fatalf("nothing ran, yet recovery demands a decision: %+v", recovery)
	}
	if recovery.Cause != "runtime_restart" {
		t.Fatalf("cause = %q, want runtime_restart", recovery.Cause)
	}
}

// Recovering an interrupted turn is a strip in place. Forking a session is
// reserved for a genuine cross-process file conflict; doing it for an ordinary
// tool interruption is what buried users under duplicate sessions.
func TestRestartRecoveryDoesNotForkTheSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	commit := provider.ToolCall{ID: "commit", Name: "bash", Arguments: `{"command":"git commit"}`}
	deadRuntimeLedger(t, path, map[string]bool{"commit": true}, commit)

	c := resumeAfterRestart(t, dir, path, []provider.Message{
		{Role: provider.RoleUser, Content: "ship it"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{commit}},
	})
	if pendingRecovery(t, c) == nil {
		t.Fatal("no handoff produced")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	forks := []string{}
	for _, entry := range entries {
		// The session's own .events/.turns sidecars are expected; a
		// session-recovery-*.jsonl branch is the duplication being ruled out.
		if strings.HasPrefix(entry.Name(), "session-recovery-") {
			forks = append(forks, entry.Name())
		}
	}
	if len(forks) != 0 {
		t.Fatalf("ordinary tool interruption forked the session: %v", forks)
	}
	if got := c.SessionPath(); got != path {
		t.Fatalf("controller moved to %q, want the original session", got)
	}
}
