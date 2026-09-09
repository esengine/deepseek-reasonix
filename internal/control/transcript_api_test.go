package control

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/store"
	"reasonix/internal/tool"
	"reasonix/internal/transcript"
)

func TestTranscriptProjectionCommitsBeforePublicationAndAllowsReentry(t *testing.T) {
	var c *Controller
	publications := 0
	c = New(Options{SessionPath: filepath.Join(t.TempDir(), "session.jsonl"), Sink: event.FuncSink(func(e event.Event) {
		if e.Sequence == 0 {
			return
		}
		publications++
		snap, err := c.TranscriptSnapshot(transcript.PageRequest{})
		if err != nil {
			t.Errorf("snapshot during publish: %v", err)
			return
		}
		if snap.CoveredThroughSeq < e.Sequence {
			t.Errorf("published %d before snapshot coverage %d", e.Sequence, snap.CoveredThroughSeq)
		}
		if e.Kind == event.AskRequest {
			if err := c.emitTurnEventChecked(event.Event{Kind: event.PromptAnswered, ItemID: "prompt"}); err != nil {
				t.Error(err)
			}
		}
	})})
	t.Cleanup(c.Close)
	c.SetTurnEventRoutingMetadata("runtime", "submission")
	if _, err := c.turnEventLedger().Begin(); err != nil {
		t.Fatal(err)
	}
	for _, e := range []event.Event{
		{Kind: event.TurnStarted},
		{Kind: event.UserMessage, MessageID: "u", Text: "question"},
		{Kind: event.Reasoning, MessageID: "a", Text: "think"},
		{Kind: event.AskRequest, ItemID: "prompt"},
	} {
		if err := c.emitTurnEventChecked(e); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := c.TranscriptSnapshot(transcript.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if publications != 5 || snap.CoveredThroughSeq != 5 || len(snap.Records) != 2 || snap.Records[1].Message.Reasoning != "think" {
		t.Fatalf("publications=%d snapshot=%+v", publications, snap)
	}
	if len(snap.Runtime.PendingEvents) != 0 {
		t.Fatal("answered prompt survived snapshot")
	}
	if snap.Records[0].Message.SubmissionID != "submission" {
		t.Fatal("lost submission identity")
	}
	view, err := c.TranscriptReplay(TranscriptReplayRequest{Identity: snap.Identity, After: 2})
	if err != nil {
		t.Fatal(err)
	}
	if view.CoveredThroughSeq != 5 || len(view.Events) != 3 {
		t.Fatalf("replay=%+v", view)
	}
}

func TestTranscriptCheckpointFailureRetainsWALWithoutFailingCompletedTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	c := New(Options{SessionPath: path, Sink: event.Discard})
	t.Cleanup(c.Close)
	if err := os.Mkdir(store.SessionTranscriptProjection(path), 0o700); err != nil {
		t.Fatal(err)
	}
	turn, err := c.turnEventLedger().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []event.Event{{Kind: event.TurnStarted}, {Kind: event.Text, MessageID: "a", Text: "kept"}, {Kind: event.TurnDone}} {
		if err := c.emitTurnEventChecked(e); err != nil {
			t.Fatal(err)
		}
	}
	if c.turnEventLedgerError() != nil {
		t.Fatal("display checkpoint failure poisoned the runtime WAL")
	}
	if len(c.PendingTurnProjections()) == 0 {
		t.Fatal("failed checkpoint allowed WAL compaction")
	}
	if err := os.Remove(store.SessionTranscriptProjection(path)); err != nil {
		t.Fatal(err)
	}
	if err := c.AcknowledgeTurnProjection(turn); err != nil {
		t.Fatal(err)
	}
	if len(c.PendingTurnProjections()) != 0 {
		t.Fatal("successful retry did not acknowledge projection")
	}
	state, ok, err := transcript.LoadCheckpoint(path)
	if err != nil || !ok || len(state.Records) != 1 || state.Records[0].Content != "kept" {
		t.Fatalf("retry checkpoint=%+v %v", state, err)
	}
}

func TestTranscriptCheckpointRestoreDoesNotReplayAutosavedTextTwice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	session := agent.NewSession("system")
	if err := session.Save(path); err != nil {
		t.Fatal(err)
	}
	newController := func(session *agent.Session) *Controller {
		executor := agent.New(nil, tool.NewRegistry(), session, agent.Options{}, event.Discard)
		return New(Options{Executor: executor, SessionPath: path, Sink: event.Discard})
	}
	c := newController(session)
	emit := func(c *Controller, e event.Event) {
		t.Helper()
		if err := c.emitTurnEventChecked(e); err != nil {
			t.Fatal(err)
		}
	}
	start := func(c *Controller, userID, assistantID, body string) {
		t.Helper()
		if _, err := c.turnEventLedger().Begin(); err != nil {
			t.Fatal(err)
		}
		emit(c, event.Event{Kind: event.TurnStarted})
		c.executor.Session().Add(provider.Message{ID: userID, Role: provider.RoleUser, Content: "question", Origin: provider.MessageOriginUser})
		emit(c, event.Event{Kind: event.UserMessage, MessageID: userID, Text: "question"})
		emit(c, event.Event{Kind: event.StreamAttempt, MessageID: assistantID, AttemptID: assistantID, StreamAttempt: event.StreamAttemptInfo{ID: assistantID, Action: event.StreamAttemptBegin}})
		emit(c, event.Event{Kind: event.Text, MessageID: assistantID, Text: body})
		emit(c, event.Event{Kind: event.Message, MessageID: assistantID, Text: body})
		emit(c, event.Event{Kind: event.StreamAttempt, MessageID: assistantID, AttemptID: assistantID, StreamAttempt: event.StreamAttemptInfo{ID: assistantID, Action: event.StreamAttemptCommit}})
		c.executor.Session().Add(provider.Message{ID: assistantID, Role: provider.RoleAssistant, Content: body})
		if err := c.executor.Session().Save(path); err != nil {
			t.Fatal(err)
		}
	}
	start(c, "u1", "a1", "first")
	emit(c, event.Event{Kind: event.TurnDone})
	before, err := c.TranscriptSnapshot(transcript.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	loaded, err := agent.LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	c = newController(loaded)
	after, err := c.TranscriptSnapshot(transcript.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Records, after.Records) || before.CoveredThroughSeq != after.CoveredThroughSeq {
		t.Fatalf("rebuild changed records: before=%+v after=%+v", before, after)
	}
	start(c, "u2", "a2", "autosaved tail")
	c.Close() // No terminal event: simulates a process ending after autosave.
	loaded, err = agent.LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	c = newController(loaded)
	t.Cleanup(c.Close)
	recovered, err := c.TranscriptSnapshot(transcript.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Records) != 4 || recovered.Records[3].Message.Content != "autosaved tail" || recovered.Runtime.Status != event.TurnInterrupted {
		t.Fatalf("recovered suffix duplicated or lost: %+v", recovered)
	}
}
