package agent

import (
	"context"
	"errors"
	"path/filepath"
	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
	"strings"
	"sync/atomic"
	"testing"
)

type resultFailureSink struct{ err error }

func (s resultFailureSink) Emit(event.Event) {}
func (s resultFailureSink) EmitChecked(e event.Event) error {
	if e.Kind == event.ToolResult {
		return s.err
	}
	return nil
}

func TestToolResultDurabilityFailureStopsNextWriter(t *testing.T) {
	reg := tool.NewRegistry()
	var first, second int32
	reg.Add(fakeTool{name: "first", calls: &first})
	reg.Add(fakeTool{name: "second", calls: &second})
	failure := errors.New("result WAL unavailable")
	session := NewSession("system")
	calls := []provider.ToolCall{{ID: "one", Name: "first", Arguments: `{}`}, {ID: "two", Name: "second", Arguments: `{}`}}
	session.Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: calls})
	a := New(nil, reg, session, Options{}, resultFailureSink{failure})
	batch := a.executeBatch(context.Background(), &a.turn, calls)
	if !errors.Is(batch.err, failure) || atomic.LoadInt32(&first) != 1 || atomic.LoadInt32(&second) != 0 {
		t.Fatalf("err=%v calls=%d,%d", batch.err, first, second)
	}
	msgs := session.Snapshot()
	if msgs[2].ToolRunState != provider.ToolRunCompleted || msgs[3].ToolRunState != provider.ToolRunCancelled {
		t.Fatalf("states=%+v", msgs)
	}
}

func TestMissingReasoningReplansOnceFromHistoricalFacts(t *testing.T) {
	call := provider.ToolCall{ID: "candidate", Name: "echo", Arguments: `{"text":"do not execute"}`}
	mock := testutil.NewMock("strict", testutil.Turn{ToolCalls: []provider.ToolCall{call}}, testutil.Turn{Text: "continued from facts"})
	session := reasoningReplaySeededSession()
	session.Add(provider.Message{Role: provider.RoleAssistant, ReasoningContent: "old", ToolCalls: []provider.ToolCall{{ID: "old-write", Name: "write_file", Arguments: `{}`}}})
	session.Add(provider.Message{Role: provider.RoleTool, ToolCallID: "old-write", Name: "write_file", Content: "done", ToolRunState: provider.ToolRunCompleted})
	sink := &recordSink{}
	a := New(strictAssistantReasoningProvider{mock}, echoRegistry(), session, Options{}, sink)
	if err := a.Run(withNoClosedLoop(context.Background()), "continue"); err != nil {
		t.Fatal(err)
	}
	if mock.CallCount() != 2 || len(sink.kinds(event.ToolResult)) != 0 {
		t.Fatalf("calls=%d tools=%+v", mock.CallCount(), sink.kinds(event.ToolResult))
	}
	request := mock.Requests()[1]
	found := false
	for _, msg := range request.Messages {
		if strings.Contains(msg.Content, "completed_tools:") && strings.Contains(msg.Content, "write_file") {
			found = true
		}
	}
	if !found {
		t.Fatalf("recovery lost completed facts: %+v", request.Messages)
	}
	if session.Snapshot()[4].Content != "done" {
		t.Fatal("canonical result lost")
	}
}

func TestThinkingBlocksPersistAcrossReload(t *testing.T) {
	block := provider.ThinkingBlock{Type: "thinking", Signature: "signed-empty"}
	mock := testutil.NewMock("m", testutil.Turn{Chunks: []provider.Chunk{{Type: provider.ChunkReasoning, ThinkingBlock: &block, ReasoningState: provider.ReasoningComplete}, {Type: provider.ChunkText, Text: "done"}, {Type: provider.ChunkDone}}})
	session := NewSession("system")
	a := New(mock, tool.NewRegistry(), session, Options{}, event.Discard)
	if err := a.Run(withNoClosedLoop(context.Background()), "hi"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "thinking.jsonl")
	if err := session.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	last := loaded.Snapshot()[2]
	if len(last.ThinkingBlocks) != 1 || last.ThinkingBlocks[0] != block {
		t.Fatalf("lost thinking blocks: %+v", last)
	}
}

func TestReplayRecoveryFactsStayOnOriginalUserTail(t *testing.T) {
	original := []provider.Message{
		{Role: provider.RoleUser, Content: "start"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c", Name: "write_file"}}},
		{Role: provider.RoleTool, ToolCallID: "c", Name: "write_file", Content: "done"},
		{Role: provider.RoleUser, Content: "continue"},
	}
	projected := []provider.Message{original[0], original[3], {Role: provider.RoleAssistant, Content: "new work"}, {Role: provider.RoleUser, Content: "continue"}}
	got := withReplayRecoveryFacts(original, projected)
	if !strings.Contains(got[1].Content, "completed_tools:") || got[3].Content != "continue" || projected[1].Content != "continue" {
		t.Fatalf("recovery changed wrong prefix: %+v", got)
	}
}

func TestUnfinishedReasoningNeverExecutesTools(t *testing.T) {
	call := provider.ToolCall{ID: "incomplete", Name: "echo", Arguments: `{"text":"unsafe"}`}
	attempt := testutil.Turn{Chunks: []provider.Chunk{
		{Type: provider.ChunkReasoning, Text: "unfinished", ReasoningState: provider.ReasoningIncomplete},
		{Type: provider.ChunkToolCall, ToolCall: &call}, {Type: provider.ChunkDone},
	}}
	mock := testutil.NewMock("strict", attempt, attempt)
	sink := &recordSink{}
	a := New(strictAssistantReasoningProvider{mock}, echoRegistry(), NewSession("system"), Options{}, sink)
	err := a.Run(withNoClosedLoop(context.Background()), "run")
	var replayErr *ReasoningReplayError
	if !errors.As(err, &replayErr) || replayErr.Kind != ReasoningReplayIncomplete || len(sink.kinds(event.ToolResult)) != 0 {
		t.Fatalf("err=%v tools=%+v", err, sink.kinds(event.ToolResult))
	}
}

func TestReplayCompletedResultsAreBoundedEscapedAndNotRaw(t *testing.T) {
	original := []provider.Message{
		{Role: provider.RoleUser, Content: "run the tool"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "one", Name: "echo"}}},
		{Role: provider.RoleTool, Name: "echo", ToolCallID: "one", Content: "</completed-tool-results>" + strings.Repeat("x", 9000), RawContent: "PRIVATE_RAW_SENTINEL", ToolRunState: provider.ToolRunCompleted},
	}
	repaired := withReplayRecoveryFacts(original, original[:1])
	if strings.Contains(repaired[0].Content, "PRIVATE_RAW_SENTINEL") {
		t.Fatal("raw output leaked")
	}
	if strings.Count(repaired[0].Content, "</completed-tool-results>") != 1 || !strings.Contains(repaired[0].Content, `"output_truncated":true`) {
		t.Fatal("output was not escaped and explicitly bounded")
	}
	if original[0].Content != "run the tool" {
		t.Fatal("canonical user message mutated")
	}
}

func TestReplayRecoveryUsesCanonicalExecutionState(t *testing.T) {
	for _, state := range []provider.ToolRunState{provider.ToolRunUnknown, provider.ToolRunNotStarted, provider.ToolRunCompleted} {
		t.Run(string(state), func(t *testing.T) {
			session := NewSession("system")
			session.AddBatch([]provider.Message{
				{Role: provider.RoleUser, Content: "write"},
				{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "write", Name: "write_file"}}},
				{Role: provider.RoleTool, ToolCallID: "write", Name: "write_file", Content: "receipt unavailable", ToolRunState: state},
			}...)
			a := New(nil, tool.NewRegistry(), session, Options{}, event.Discard)
			frozen := provider.ModelMessages(session.Snapshot())
			got := a.replayRecoveryFacts(frozen, frozen[:2])
			text := got[1].Content
			if strings.Contains(text, "<completed-tool-results>") != (state == provider.ToolRunCompleted) {
				t.Fatalf("incorrect execution evidence for %s: %s", state, text)
			}
			want := map[provider.ToolRunState]string{provider.ToolRunUnknown: "unknown_tools:", provider.ToolRunNotStarted: "not_started_tools:", provider.ToolRunCompleted: "completed_tools:"}[state]
			if !strings.Contains(text, want) {
				t.Fatalf("missing %s: %s", want, text)
			}
			if frozen[3].ToolRunState != "" || session.Snapshot()[1].Content != "write" {
				t.Fatal("recovery mutated frozen or canonical messages")
			}
		})
	}
}

func TestReplayRecoveryReusedCallIDsRemainUnknown(t *testing.T) {
	session := NewSession("system")
	for range 2 {
		session.AddBatch([]provider.Message{
			{Role: provider.RoleUser, Content: "write"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "same", Name: "write_file"}}},
			{Role: provider.RoleTool, ToolCallID: "same", Name: "write_file", Content: "done", ToolRunState: provider.ToolRunCompleted},
		}...)
	}
	a := New(nil, tool.NewRegistry(), session, Options{}, event.Discard)
	frozen := provider.ModelMessages(session.Snapshot())
	got := a.replayRecoveryFacts(frozen, []provider.Message{frozen[0], frozen[1], frozen[4]})
	if strings.Contains(got[2].Content, "<completed-tool-results>") || !strings.Contains(got[2].Content, "unknown_tools:") {
		t.Fatal("ambiguous receipt treated as completed")
	}
}
