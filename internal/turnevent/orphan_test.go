package turnevent

import (
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

func TestLedgerReopenClassifiesOrphansByStartBarrier(t *testing.T) {
	path := testSessionPath(t)
	l := openTestLedger(t, path, "session")
	turnID, err := l.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	append := func(e event.Event) {
		t.Helper()
		if _, ok, err := l.Append(e, event.TurnInProgress); err != nil || !ok {
			t.Fatalf("append %v: ok=%v err=%v", e.Kind, ok, err)
		}
	}
	append(event.Event{Kind: event.TurnStarted})
	append(event.Event{Kind: event.ToolDispatch, Tool: event.Tool{ID: "commit", Name: "bash", Args: `{"command":"git commit"}`}})
	append(event.Event{Kind: event.ToolStarted, Tool: event.Tool{ID: "commit", Name: "bash", Args: `{"command":"git commit"}`}})
	append(event.Event{Kind: event.ToolDispatch, Tool: event.Tool{ID: "push", Name: "bash", Args: `{"command":"git push"}`}})
	if l.OrphanRecovery() != nil {
		t.Fatal("live ledger reported reopen evidence")
	}

	for reopen := range 2 {
		reopened := openTestLedger(t, path, "session")
		recs, err := reopened.EventsAfter(0)
		if err != nil {
			t.Fatalf("EventsAfter: %v", err)
		}
		states := map[string]string{}
		for _, rec := range recs {
			if rec.Kind == "tool_result" && rec.Event.Tool != nil {
				if rec.Event.Tool.Args != "" || rec.Event.Tool.Output != "" {
					t.Fatalf("synthetic result replayed tool input/output: %#v", rec.Event.Tool)
				}
				states[rec.Event.Tool.ID] = rec.Event.Tool.RunState
			}
		}
		if states["commit"] != string(provider.ToolRunUnknown) || states["push"] != string(provider.ToolRunCancelled) {
			t.Fatalf("reopen %d states = %v, want started unknown and queued cancelled", reopen, states)
		}
		terminal := recs[len(recs)-1]
		if terminal.Kind != "turn_done" || terminal.Status != event.TurnRecoveryRequired {
			t.Fatalf("reopen %d terminal = %+v, want recovery_required turn_done", reopen, terminal)
		}
		if terminal.Event.Recovery == nil || !terminal.Event.Recovery.RequiresUser || terminal.Event.Recovery.TurnID != turnID {
			t.Fatalf("reopen %d terminal recovery = %+v", reopen, terminal.Event.Recovery)
		}
		orphan := reopened.OrphanRecovery()
		if orphan == nil || orphan.TurnID != turnID || len(orphan.Tools) != 2 {
			t.Fatalf("reopen %d evidence = %+v", reopen, orphan)
		}
		if !orphan.Tools[0].Started || orphan.Tools[0].ID != "commit" || orphan.Tools[1].Started || orphan.Tools[1].ID != "push" {
			t.Fatalf("reopen %d evidence tools = %+v", reopen, orphan.Tools)
		}
		if reopened.ActiveTurnID() != "" {
			t.Fatal("recovered turn still reads as active")
		}
	}
}

func TestLedgerReopenWithoutStartedToolsStaysInterrupted(t *testing.T) {
	path := testSessionPath(t)
	l := openTestLedger(t, path, "session")
	if _, err := l.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.TurnStarted}, event.TurnInProgress); err != nil || !ok {
		t.Fatalf("append start: ok=%v err=%v", ok, err)
	}
	if _, ok, err := l.Append(event.Event{Kind: event.ToolDispatch, Tool: event.Tool{ID: "read", Name: "read_file"}}, event.TurnInProgress); err != nil || !ok {
		t.Fatalf("append dispatch: ok=%v err=%v", ok, err)
	}
	reopened := openTestLedger(t, path, "session")
	recs, err := reopened.EventsAfter(0)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	terminal := recs[len(recs)-1]
	if terminal.Status != event.TurnInterrupted || terminal.Event.Recovery != nil {
		t.Fatalf("terminal = %+v, want plain interrupted", terminal)
	}
	orphan := reopened.OrphanRecovery()
	if orphan == nil || len(orphan.Tools) != 1 || orphan.Tools[0].Started {
		t.Fatalf("evidence = %+v, want one never-started tool", orphan)
	}
	if _, ok, err := reopened.Append(event.Event{Kind: event.Text}, event.TurnInProgress); err != nil || ok {
		t.Fatalf("recovered turn accepted new events: ok=%v err=%v", ok, err)
	}
}
