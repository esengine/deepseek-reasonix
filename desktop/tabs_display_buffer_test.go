package main

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/eventwire"
	"reasonix/internal/provider"
	"reasonix/internal/turnevent"
)

func TestDisplayTurnBufferPreservesStreamingReplacementAndTools(t *testing.T) {
	var buffer displayTurnBuffer
	recordHistoryDisplayEvent(&buffer, event.Event{Kind: event.Reasoning, Text: "draft reason "})
	recordHistoryDisplayEvent(&buffer, event.Event{Kind: event.Reasoning, Text: "continued"})
	recordHistoryDisplayEvent(&buffer, event.Event{Kind: event.Text, Text: "draft answer"})
	recordHistoryDisplayEvent(&buffer, event.Event{
		Kind:      event.Message,
		Text:      "final answer",
		Reasoning: "final reason",
		MemoryCitations: []provider.MemoryCitation{{
			ID: "memory-1", Source: "project",
		}},
	})
	recordHistoryDisplayEvent(&buffer, event.Event{Kind: event.ToolDispatch, Tool: event.Tool{
		ID: "call-1", Name: "read_file", Args: `{"path":"settings.json"}`,
	}})
	recordHistoryDisplayEvent(&buffer, event.Event{Kind: event.ToolResult, Tool: event.Tool{
		ID: "call-1", Output: "settings contents",
	}})

	got := buffer.materialize()
	if len(got) != 3 {
		t.Fatalf("messages = %d, want 3: %+v", len(got), got)
	}
	if got[0].Role != "assistant" || got[0].Content != "final answer" || got[0].Reasoning != "final reason" {
		t.Fatalf("stream replacement changed: %+v", got[0])
	}
	if len(got[0].MemoryCitations) != 1 || got[0].MemoryCitations[0].ID != "memory-1" {
		t.Fatalf("memory citations changed: %+v", got[0].MemoryCitations)
	}
	if got[1].Role != "assistant" || len(got[1].ToolCalls) != 1 || got[1].ToolCalls[0].ID != "call-1" || got[1].ToolCalls[0].Summary == "" {
		t.Fatalf("tool call changed: %+v", got[1])
	}
	if got[2].Role != "tool" || got[2].ToolCallID != "call-1" || got[2].ToolName != "read_file" {
		t.Fatalf("tool result changed: %+v", got[2])
	}
}

func TestDisplayTurnBufferMessageIdentityAndDiscardMatchRecovery(t *testing.T) {
	events := []event.Event{
		{Kind: event.StreamAttempt, MessageID: "failed", AttemptID: "failed", StreamAttempt: event.StreamAttemptInfo{ID: "failed", Action: event.StreamAttemptBegin}},
		{Kind: event.Reasoning, MessageID: "failed", AttemptID: "failed", Text: "discard this"},
		{Kind: event.Message, MessageID: "failed", AttemptID: "failed", Text: "rejected full response"},
		{Kind: event.StreamAttempt, MessageID: "failed", AttemptID: "failed", StreamAttempt: event.StreamAttemptInfo{ID: "failed", Action: event.StreamAttemptDiscard}},
		{Kind: event.Reasoning, MessageID: "a", AttemptID: "a", Text: "first thought"},
		{Kind: event.ToolDispatch, MessageID: "a", Tool: event.Tool{ID: "call", Name: "read_file", Args: `{}`}},
		{Kind: event.ToolResult, Tool: event.Tool{ID: "call", Name: "read_file", Output: "done"}},
		{Kind: event.Reasoning, MessageID: "b", AttemptID: "b", Text: "second thought"},
		{Kind: event.Message, MessageID: "a", AttemptID: "a", Reasoning: "first thought", Text: "first answer"},
	}
	var live, recovered displayTurnBuffer
	for i, e := range events {
		recordHistoryDisplayEvent(&live, e)
		wire := eventwire.ToWire(e)
		replay, ok := displayEventFromEnvelope(turnevent.Envelope{Kind: wire.Kind, Sequence: uint64(i + 1), Event: wire})
		if !ok {
			t.Fatalf("event %s is not replayable", wire.Kind)
		}
		recordHistoryDisplayEvent(&recovered, replay)
	}
	for name, buffer := range map[string]*displayTurnBuffer{"live": &live, "recovered": &recovered} {
		rows := buffer.materialize()
		if len(rows) != 3 || rows[0].MessageID != "a" || rows[0].Content != "first answer" || rows[0].Reasoning != "first thought" || len(rows[0].ToolCalls) != 1 || rows[2].MessageID != "b" || rows[2].Reasoning != "second thought" {
			t.Fatalf("%s message ownership/discard mismatch: %+v", name, rows)
		}
	}
}

func TestDisplayTurnBufferStreamingAllocationsStayNearLinear(t *testing.T) {
	const (
		chunks    = 2_000
		chunkSize = 32
	)
	chunk := strings.Repeat("x", chunkSize)
	result := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			var buffer displayTurnBuffer
			for range chunks {
				recordHistoryDisplayEvent(&buffer, event.Event{Kind: event.Text, Text: chunk})
			}
			messages := buffer.materialize()
			if len(messages) != 1 || len(messages[0].Content) != chunks*chunkSize {
				b.Fatalf("materialized display length changed")
			}
		}
	})

	// Repeated string concatenation allocated roughly one full growing prefix
	// per chunk (>69 MiB for this 64 KiB stream). Keep a generous ceiling for
	// platform/runtime variance while pinning the intended near-linear shape.
	if got, max := result.AllocedBytesPerOp(), int64(chunks*chunkSize*16); got > max {
		t.Fatalf("stream allocated %d bytes/op, want <= %d (%s)", got, max, result.String())
	}
	if got := result.AllocsPerOp(); got > 100 {
		t.Fatalf("stream allocated %d objects/op, want <= 100 (%s)", got, result.String())
	}
	t.Logf("64 KiB stream: %d bytes/op, %d allocs/op", result.AllocedBytesPerOp(), result.AllocsPerOp())
}

func TestPendingDisplayWriteRetriesWithoutDroppingTurn(t *testing.T) {
	state := &tabDisplayState{}
	var attempts atomic.Int32
	var acknowledgements atomic.Int32
	persisted := make(chan struct{})
	acknowledged := make(chan struct{})
	write := &pendingDisplayWrite{
		dir:         "sessions",
		sessionPath: "sessions/session.jsonl",
		userContent: "prompt",
		messages:    []HistoryMessage{{Role: "assistant", Content: "partial answer"}},
		persist: func(_, _, _ string, messages []HistoryMessage) error {
			attempt := attempts.Add(1)
			if len(messages) != 1 || messages[0].Content != "partial answer" {
				return errors.New("queued turn changed")
			}
			if attempt < 3 {
				return errors.New("temporary lock contention")
			}
			close(persisted)
			return nil
		},
		onPersisted: func() {
			acknowledgements.Add(1)
			close(acknowledged)
		},
	}
	persistOrEnqueueDisplayWrite(state, write)
	select {
	case <-persisted:
	case <-time.After(3 * time.Second):
		t.Fatal("pending display write was not retried")
	}
	select {
	case <-acknowledged:
	case <-time.After(time.Second):
		t.Fatal("durable display write was not acknowledged")
	}
	state.mu.Lock()
	pending := len(state.pendingWrites)
	running := state.persistRunning
	state.mu.Unlock()
	if pending != 0 || running {
		t.Fatalf("retry worker did not drain before acknowledgement: pending=%d running=%v", pending, running)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("persist attempts = %d, want 3", got)
	}
	if got := acknowledgements.Load(); got != 1 {
		t.Fatalf("projection acknowledgements = %d, want exactly one after persistence", got)
	}
}

func TestDisplayMessagesFromInterruptedProjectionKeepsPartialOutput(t *testing.T) {
	textEvent := event.Event{Kind: event.Text, Source: event.UsageSourceExecutor, Text: "partial answer"}
	toolEvent := event.Event{Kind: event.ToolDispatch, Source: event.UsageSourceExecutor, Tool: event.Tool{ID: "call-1", Name: "read_file", Args: `{"path":"notes.txt"}`}}
	projection := turnevent.PendingProjection{
		TurnID: "turn-1", Status: event.TurnInterrupted,
		Events: []turnevent.Envelope{
			{TurnID: "turn-1", Sequence: 1, Kind: "text", Source: textEvent.Source, Event: eventwire.ToWire(textEvent)},
			{TurnID: "turn-1", Sequence: 2, Kind: "tool_dispatch", Source: toolEvent.Source, Event: eventwire.ToWire(toolEvent)},
			{TurnID: "turn-1", Sequence: 3, Kind: "turn_done", Status: event.TurnInterrupted, Event: eventwire.ToWire(event.Event{Kind: event.TurnDone})},
		},
	}

	got := displayMessagesFromProjection(projection)
	if len(got) != 3 {
		t.Fatalf("recovered display messages = %d, want partial answer, tool card and notice: %+v", len(got), got)
	}
	if got[0].Role != "assistant" || got[0].Content != "partial answer" {
		t.Fatalf("partial assistant output changed: %+v", got[0])
	}
	if got[1].Role != "assistant" || len(got[1].ToolCalls) != 1 || got[1].ToolCalls[0].ID != "call-1" {
		t.Fatalf("tool dispatch projection changed: %+v", got[1])
	}
	if got[2].Role != "notice" || got[2].Code != event.NoticeCodeCancelledTurn {
		t.Fatalf("interruption notice missing: %+v", got[2])
	}
}
