package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func echoRegistry() *tool.Registry {
	reg := tool.NewRegistry()
	reg.Add(echoTool{})
	return reg
}

func TestStreamIdentityMatchesPersistedAssistant(t *testing.T) {
	prov := testutil.NewMock("m", testutil.Turn{Text: "answer"})
	session := NewSession("system")
	sink := &recordSink{}
	a := New(prov, tool.NewRegistry(), session, Options{}, sink)
	if err := a.Run(withNoClosedLoop(context.Background()), "question"); err != nil {
		t.Fatal(err)
	}
	messages := session.Snapshot()
	users := sink.kinds(event.UserMessage)
	if len(users) != 1 || users[0].MessageID == "" || users[0].Text != "question" {
		t.Fatalf("missing admitted user identity: %+v", users)
	}
	if messages[len(messages)-2].ID != users[0].MessageID {
		t.Fatal("user event does not identify the persisted user message")
	}
	assistant := messages[len(messages)-1]
	if assistant.Role != provider.RoleAssistant || assistant.ID == "" {
		t.Fatalf("missing persisted assistant identity: %+v", assistant)
	}
	for _, kind := range []event.Kind{event.Text, event.Message, event.StreamAttempt} {
		events := sink.kinds(kind)
		if len(events) == 0 {
			t.Fatalf("no %v events", kind)
		}
		for _, e := range events {
			if e.MessageID != assistant.ID || e.AttemptID != assistant.ID {
				t.Fatalf("event identity differs from committed message %q: %+v", assistant.ID, e)
			}
		}
	}
}

func TestToolEventsRetainCommittedMessageIdentityAcrossRounds(t *testing.T) {
	prov := testutil.NewMock("m",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "first", Name: "echo", Arguments: `{"text":"one"}`}}},
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "second", Name: "echo", Arguments: `{"text":"two"}`}}},
		testutil.Turn{Text: "done"},
	)
	session := NewSession("system")
	sink := &recordSink{}
	a := New(prov, echoRegistry(), session, Options{}, sink)
	if err := a.Run(withNoClosedLoop(context.Background()), "run both"); err != nil {
		t.Fatal(err)
	}
	owners := map[string]string{}
	for _, m := range session.Snapshot() {
		for _, call := range m.ToolCalls {
			owners[call.ID] = m.ID
		}
	}
	if owners["first"] == "" || owners["second"] == "" || owners["first"] == owners["second"] {
		t.Fatalf("invalid committed owners: %v", owners)
	}
	for _, kind := range []event.Kind{event.ToolDispatch, event.ToolResult} {
		for _, e := range sink.kinds(kind) {
			if expected := owners[e.Tool.ID]; expected != "" && e.MessageID != expected {
				t.Fatalf("tool %s event %v owner %q, want %q", e.Tool.ID, kind, e.MessageID, expected)
			}
		}
	}
}

func TestRunPersistsUserCreatedAtWithoutSendingItToProvider(t *testing.T) {
	const existingCreatedAt int64 = 1_718_000_000_000
	prov := testutil.NewMock("m", testutil.Turn{Text: "done"})
	session := NewSession("system")
	session.Add(provider.Message{Role: provider.RoleUser, Content: "existing", CreatedAt: existingCreatedAt})
	agent := New(prov, tool.NewRegistry(), session, Options{}, event.Discard)

	if err := agent.Run(withNoClosedLoop(context.Background()), "new prompt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	request := prov.LastRequest()
	if request == nil {
		t.Fatal("provider received no request")
	}
	for i, message := range request.Messages {
		if message.CreatedAt != 0 {
			t.Fatalf("provider message %d leaked createdAt %d", i, message.CreatedAt)
		}
	}

	messages := session.Snapshot()
	if len(messages) < 3 || messages[1].CreatedAt != existingCreatedAt {
		t.Fatalf("persisted existing timestamp changed: %+v", messages)
	}
	if messages[2].Role != provider.RoleUser || messages[2].CreatedAt <= 0 {
		t.Fatalf("new user timestamp was not persisted: %+v", messages[2])
	}
}

func TestRunPersistsResponsesItemsAcrossSessionReload(t *testing.T) {
	raw := json.RawMessage(`{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"latest"}}`)
	prov := testutil.NewMock("deepseek-responses", testutil.Turn{Chunks: []provider.Chunk{
		{Type: provider.ChunkResponsesItem, ResponsesItem: raw},
		{Type: provider.ChunkText, Text: "answer"},
		{Type: provider.ChunkDone},
	}})
	session := NewSession("system")
	agent := New(prov, tool.NewRegistry(), session, Options{}, event.Discard)
	if err := agent.Run(withNoClosedLoop(context.Background()), "search"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	messages := session.Snapshot()
	assistant := messages[len(messages)-1]
	if assistant.Role != provider.RoleAssistant || len(assistant.ResponsesItems) != 1 || string(assistant.ResponsesItems[0]) != string(raw) {
		t.Fatalf("assistant Responses items = %#v, want persisted search item", assistant.ResponsesItems)
	}

	path := filepath.Join(t.TempDir(), "responses-items.jsonl")
	if err := session.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := LoadSession(path)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	loadedAssistant := loaded.Messages[len(loaded.Messages)-1]
	if len(loadedAssistant.ResponsesItems) != 1 || string(loadedAssistant.ResponsesItems[0]) != string(raw) {
		t.Fatalf("reloaded Responses items = %#v, want original item", loadedAssistant.ResponsesItems)
	}
}

// TestRunMultiToolRoundEmptyIDsSurvivePairing drives the real loop through a turn
// that fans out two tool calls carrying no id (a gateway that streams by index),
// then asserts both results still pair back after SanitizeToolPairing — the repair
// that runs on every send. Keying on tool_call_id alone collapsed them into one,
// dropping a result from the model's context on the very next turn.
func TestRunMultiToolRoundEmptyIDsSurvivePairing(t *testing.T) {
	mp := testutil.NewMock("m",
		testutil.Turn{ToolCalls: []provider.ToolCall{
			{ID: "", Name: "echo", Arguments: `{"text":"alpha"}`},
			{ID: "", Name: "echo", Arguments: `{"text":"beta"}`},
		}},
		testutil.Turn{Text: "done"},
	)
	a := New(mp, echoRegistry(), NewSession(""), Options{}, event.Discard)
	if err := a.Run(withNoClosedLoop(context.Background()), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	repaired := provider.SanitizeToolPairing(a.Session().Messages)
	var results []string
	for _, m := range repaired {
		if m.Role == provider.RoleTool {
			results = append(results, m.Content)
		}
	}
	if len(results) != 2 {
		t.Fatalf("want 2 tool results after pairing, got %d: %v", len(results), results)
	}
	if results[0] == results[1] {
		t.Fatalf("both results collapsed to %q — one was lost from the model's context", results[0])
	}
	if !strings.Contains(results[0], "alpha") || !strings.Contains(results[1], "beta") {
		t.Errorf("results lost their identity: %v", results)
	}
}

func TestRunPersistsCumulativeAssistantWorkDuration(t *testing.T) {
	mp := testutil.NewMock("m",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "echo", Arguments: `{"text":"hello"}`}}},
		testutil.Turn{Text: "done"},
	)
	a := New(mp, echoRegistry(), NewSession(""), Options{}, event.Discard)
	if err := a.Run(withNoClosedLoop(context.Background()), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var durations []int64
	for _, message := range a.Session().Messages {
		if message.Role == provider.RoleAssistant {
			durations = append(durations, message.WorkDurationMs)
		}
	}
	if len(durations) != 2 {
		t.Fatalf("assistant durations = %v, want two rounds", durations)
	}
	if durations[0] <= 0 || durations[1] < durations[0] {
		t.Fatalf("assistant durations must be positive and cumulative: %v", durations)
	}
}

// TestRunCancelledMidStreamLeavesResumableSession proves a turn cancelled before
// the model answered leaves the session well-formed: the user message stands,
// nothing dangling, and the repaired history is sendable as-is on resume.
func TestRunCancelledMidStreamLeavesResumableSession(t *testing.T) {
	mp := testutil.NewMock("m", testutil.ErrorTurn(context.Canceled))
	a := New(mp, echoRegistry(), NewSession("sys"), Options{}, event.Discard)

	err := a.Run(withNoClosedLoop(context.Background()), "do the thing")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run should surface the cancellation, got %v", err)
	}

	repaired := provider.SanitizeToolPairing(a.Session().Messages)
	for i, m := range repaired {
		if m.Role == provider.RoleTool {
			t.Fatalf("a cancelled turn left a dangling tool message at %d: %+v", i, m)
		}
	}
	last := repaired[len(repaired)-1]
	if last.Role != provider.RoleUser || StripTransientUserBlocks(last.Content) != "do the thing" {
		t.Errorf("the pending user message should survive a cancel, got %+v", last)
	}
}

func TestRunRecoversInterruptedStreamAfterPartialText(t *testing.T) {
	interrupted := &provider.StreamInterruptedError{Err: errors.New("deepseek-flash: read stream: unexpected EOF"), Reason: provider.StreamInterruptPrematureEOF}
	mp := testutil.NewMock("m",
		testutil.Turn{Text: "partial ", ChunkError: interrupted},
		testutil.Turn{Text: "continued"},
	)
	sink := &recordSink{}
	a := New(mp, echoRegistry(), NewSession(""), Options{}, sink)

	if err := a.Run(withNoClosedLoop(context.Background()), "go"); err != nil {
		t.Fatalf("Run should recover the interrupted stream, got %v", err)
	}
	if mp.CallCount() != 2 {
		t.Fatalf("provider calls = %d, want 2", mp.CallCount())
	}

	reqs := mp.Requests()
	if len(reqs) != 2 {
		t.Fatalf("recorded requests = %d, want 2", len(reqs))
	}
	// Codex-style: exact original request replay — no synthetic recovery user
	// message, no partial assistant in the provider body.
	if !providerRequestBodiesEqual(reqs[0], reqs[1]) {
		t.Fatalf("retry must replay the identical provider request\nfirst=%+v\nsecond=%+v", reqs[0], reqs[1])
	}
	for _, message := range reqs[1].Messages {
		if message.LocalOnly || message.Content == "partial " {
			t.Fatalf("partial assistant leaked into provider recovery request: %+v", reqs[1].Messages)
		}
		if strings.Contains(message.Content, "interrupted") && message.Role == provider.RoleUser {
			t.Fatalf("synthetic stream recovery must not be injected: %+v", message)
		}
	}
	// Successful recovery never persists a LocalOnly interrupted record.
	for _, message := range a.Session().Messages {
		if message.LocalOnly {
			t.Fatalf("successful recovery must not leave LocalOnly interrupt records: %+v", message)
		}
	}

	var streamed strings.Builder
	for _, e := range sink.kinds(event.Text) {
		streamed.WriteString(e.Text)
	}
	// Both attempts emit text to the sink; Desktop discards the first via
	// stream_attempt. Agent still emits both for non-journal sinks.
	if !strings.Contains(streamed.String(), "continued") {
		t.Fatalf("streamed text = %q, want final continued answer", streamed.String())
	}
	retries := sink.kinds(event.Retrying)
	if len(retries) != 1 || retries[0].RetryAttempt != 1 || retries[0].RetryMax != maxStreamRecoveries || retries[0].RetryScope != event.RetryScopeStream {
		t.Fatalf("retry events = %+v, want one stream recovery retry", retries)
	}
	attempts := sink.kinds(event.StreamAttempt)
	if len(attempts) < 3 {
		t.Fatalf("stream_attempt events = %d, want begin/discard/begin/commit at least", len(attempts))
	}
	var sawDiscard, sawCommit bool
	for _, e := range attempts {
		if e.StreamAttempt.Action == event.StreamAttemptDiscard {
			sawDiscard = true
			if e.StreamAttempt.Reason != provider.StreamInterruptPrematureEOF {
				t.Fatalf("discard reason = %q", e.StreamAttempt.Reason)
			}
		}
		if e.StreamAttempt.Action == event.StreamAttemptCommit {
			sawCommit = true
		}
	}
	if !sawDiscard || !sawCommit {
		t.Fatalf("stream attempts missing discard/commit: %+v", attempts)
	}
}

func TestRunRecoversRepeatedInterruptedStreams(t *testing.T) {
	interrupted := &provider.StreamInterruptedError{Err: errors.New("deepseek-flash: read stream: unexpected EOF")}
	mp := testutil.NewMock("m",
		testutil.Turn{Text: "first ", ChunkError: interrupted},
		testutil.Turn{Text: "second ", ChunkError: interrupted},
		testutil.Turn{Text: "done"},
	)
	sink := &recordSink{}
	a := New(mp, echoRegistry(), NewSession(""), Options{}, sink)

	if err := a.Run(withNoClosedLoop(context.Background()), "go"); err != nil {
		t.Fatalf("Run should recover repeated interrupted streams, got %v", err)
	}
	if mp.CallCount() != 3 {
		t.Fatalf("provider calls = %d, want 3", mp.CallCount())
	}
	reqs := mp.Requests()
	if !providerRequestBodiesEqual(reqs[0], reqs[1]) || !providerRequestBodiesEqual(reqs[0], reqs[2]) {
		t.Fatalf("all retries must replay the same frozen provider request")
	}

	var streamed strings.Builder
	for _, e := range sink.kinds(event.Text) {
		streamed.WriteString(e.Text)
	}
	if !strings.Contains(streamed.String(), "done") {
		t.Fatalf("streamed text = %q, want final done", streamed.String())
	}
	retries := sink.kinds(event.Retrying)
	if len(retries) != 2 || retries[0].RetryAttempt != 1 || retries[1].RetryAttempt != 2 {
		t.Fatalf("retry events = %+v, want attempts 1 and 2", retries)
	}
	for _, retry := range retries {
		if retry.RetryMax != maxStreamRecoveries || retry.RetryScope != event.RetryScopeStream {
			t.Fatalf("retry = %+v, want max=%d scope=stream", retry, maxStreamRecoveries)
		}
	}
}

func TestRunRecoversInterruptedPartialToolCallWithoutExecutingIt(t *testing.T) {
	interrupted := &provider.StreamInterruptedError{Err: errors.New("deepseek-flash: read stream: unexpected EOF")}
	mp := testutil.NewMock("m",
		testutil.Turn{Chunks: []provider.Chunk{
			{Type: provider.ChunkToolCallStart, ToolCall: &provider.ToolCall{ID: "c1", Name: "echo"}},
			{Type: provider.ChunkError, Err: interrupted},
		}},
		testutil.Turn{Text: "recovered"},
	)
	a := New(mp, echoRegistry(), NewSession(""), Options{}, event.Discard)

	if err := a.Run(withNoClosedLoop(context.Background()), "go"); err != nil {
		t.Fatalf("Run should recover the interrupted tool-call stream, got %v", err)
	}

	for _, m := range a.Session().Messages {
		if m.Role == provider.RoleTool && !m.LocalOnly {
			t.Fatalf("partial tool call should not have executed or produced a tool result: %+v", m)
		}
		if m.LocalOnly {
			t.Fatalf("successful recovery must not leave LocalOnly interrupt: %+v", m)
		}
	}
	reqs := mp.Requests()
	if len(reqs) != 2 || !providerRequestBodiesEqual(reqs[0], reqs[1]) {
		t.Fatalf("partial-tool interrupt must exact-replay without synthetic recovery")
	}
}

func TestRunStreamRetryRequestCountIsLinearNotTriangular(t *testing.T) {
	interrupted := &provider.StreamInterruptedError{Err: errors.New("eof"), Reason: provider.StreamInterruptPrematureEOF}
	mp := testutil.NewMock("m",
		testutil.Turn{Text: "a", Usage: &provider.Usage{PromptTokens: 30, CompletionTokens: 1, TotalTokens: 31, CacheMissTokens: 30}, ChunkError: interrupted},
		testutil.Turn{Text: "b", Usage: &provider.Usage{PromptTokens: 30, CompletionTokens: 1, TotalTokens: 31, CacheMissTokens: 30}, ChunkError: interrupted},
		testutil.Turn{Text: "ok", Usage: &provider.Usage{PromptTokens: 30, CompletionTokens: 2, TotalTokens: 32, CacheMissTokens: 30}},
	)
	sink := &recordSink{}
	a := New(mp, echoRegistry(), NewSession(""), Options{}, sink)
	if err := a.Run(withNoClosedLoop(context.Background()), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mp.CallCount() != 3 {
		t.Fatalf("provider calls = %d, want 3", mp.CallCount())
	}
	usages := sink.kinds(event.Usage)
	if len(usages) != 1 || usages[0].Usage == nil {
		t.Fatalf("usage events = %d, want one aggregate", len(usages))
	}
	u := usages[0].Usage
	if u.RequestCount != 3 {
		t.Fatalf("RequestCount = %d, want 3 (linear, not triangular 6)", u.RequestCount)
	}
	// Billable input is summed; context gauge uses ContextPromptTokens.
	if u.PromptTokens != 90 {
		t.Fatalf("PromptTokens = %d, want billable sum 90", u.PromptTokens)
	}
	if u.ContextPromptTokens != 30 {
		t.Fatalf("ContextPromptTokens = %d, want latest 30", u.ContextPromptTokens)
	}
	if u.CacheHitTokens+u.CacheMissTokens != u.PromptTokens {
		t.Fatalf("cache split %d+%d must align with PromptTokens %d", u.CacheHitTokens, u.CacheMissTokens, u.PromptTokens)
	}
	if u.CompletionTokens != 4 {
		t.Fatalf("CompletionTokens = %d, want billable sum 4", u.CompletionTokens)
	}
	// ContextSnapshot and compaction use the latest full attempt shape.
	if last := a.sess.output.lastUsage.Load(); last == nil || last.PromptTokens != 30 {
		t.Fatalf("lastUsage prompt = %+v, want latest attempt prompt 30", last)
	}
}

func TestRunExhaustedStreamRetriesPersistPendingLocalOnly(t *testing.T) {
	interrupted := &provider.StreamInterruptedError{Err: errors.New("eof"), Reason: provider.StreamInterruptPrematureEOF}
	turns := make([]testutil.Turn, 0, maxSamplingAttempts)
	for range maxSamplingAttempts {
		turns = append(turns, testutil.Turn{Text: "half", ChunkError: interrupted})
	}
	mp := testutil.NewMock("m", turns...)
	a := New(mp, echoRegistry(), NewSession(""), Options{}, event.Discard)

	err := a.Run(withNoClosedLoop(context.Background()), "go")
	if !provider.IsStreamInterrupted(err) {
		t.Fatalf("Run error = %v, want StreamInterruptedError after exhausting retries", err)
	}
	if mp.CallCount() != maxSamplingAttempts {
		t.Fatalf("provider calls = %d, want %d", mp.CallCount(), maxSamplingAttempts)
	}
	var pending *provider.InterruptedTurnRecovery
	var local provider.Message
	for _, m := range a.Session().Messages {
		if m.LocalOnly && m.InterruptedTurn != nil && m.InterruptedTurn.Pending {
			pending = m.InterruptedTurn
			local = m
		}
	}
	if pending == nil || local.Content != "half" {
		t.Fatalf("exhausted retries must leave one pending LocalOnly record: local=%+v pending=%+v", local, pending)
	}
	// No synthetic recovery user messages mid-turn.
	for _, m := range a.Session().Messages {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "previous assistant response was interrupted") {
			t.Fatalf("must not inject synthetic stream recovery: %+v", m)
		}
	}
}

func TestRunCompleteUncommittedToolCallNeverExecutes(t *testing.T) {
	// Full tool block arrived, but the stream was interrupted before a clean
	// terminal — the call stays speculative and must never reach executeBatch.
	interrupted := &provider.StreamInterruptedError{Err: errors.New("eof"), Reason: provider.StreamInterruptPrematureEOF}
	writer := &countingWriterTool{}
	reg := tool.NewRegistry()
	reg.Add(writer)
	mp := testutil.NewMock("m",
		testutil.Turn{Chunks: []provider.Chunk{
			{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: "w1", Name: "write_file", Arguments: `{"path":"x.txt","content":"from-writer"}`}},
			{Type: provider.ChunkError, Err: interrupted},
		}},
		testutil.Turn{Text: "recovered without write"},
	)
	a := New(mp, reg, NewSession(""), Options{}, event.Discard)
	if err := a.Run(withNoClosedLoop(context.Background()), "write it"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if writer.calls.Load() != 0 {
		t.Fatalf("writer executed %d times, want 0 (uncommitted tool call)", writer.calls.Load())
	}
}

type countingWriterTool struct{ calls atomic.Int32 }

func (c *countingWriterTool) Name() string        { return "write_file" }
func (c *countingWriterTool) Description() string { return "count writes" }
func (c *countingWriterTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}}}`)
}
func (c *countingWriterTool) ReadOnly() bool { return false }
func (c *countingWriterTool) Execute(context.Context, json.RawMessage) (string, error) {
	c.calls.Add(1)
	return "wrote", nil
}

// providerRequestBodiesEqual compares the provider-visible request surface
// (messages, tools order/bytes, temperature, token limit, response format).
func providerRequestBodiesEqual(a, b provider.Request) bool {
	if a.MaxTokens != b.MaxTokens {
		return false
	}
	if (a.Temperature == nil) != (b.Temperature == nil) {
		return false
	}
	if a.Temperature != nil && b.Temperature != nil && *a.Temperature != *b.Temperature {
		return false
	}
	if (a.ResponseFormat == nil) != (b.ResponseFormat == nil) {
		return false
	}
	if a.ResponseFormat != nil && b.ResponseFormat != nil && a.ResponseFormat.Type != b.ResponseFormat.Type {
		return false
	}
	if len(a.Messages) != len(b.Messages) || len(a.Tools) != len(b.Tools) {
		return false
	}
	for i := range a.Messages {
		am, bm := a.Messages[i], b.Messages[i]
		if am.Role != bm.Role || am.Content != bm.Content || am.ReasoningContent != bm.ReasoningContent ||
			am.Name != bm.Name || am.ToolCallID != bm.ToolCallID || am.LocalOnly != bm.LocalOnly {
			return false
		}
		if len(am.ToolCalls) != len(bm.ToolCalls) {
			return false
		}
		for j := range am.ToolCalls {
			if am.ToolCalls[j].ID != bm.ToolCalls[j].ID || am.ToolCalls[j].Name != bm.ToolCalls[j].Name ||
				am.ToolCalls[j].Arguments != bm.ToolCalls[j].Arguments {
				return false
			}
		}
	}
	for i := range a.Tools {
		if a.Tools[i].Name != b.Tools[i].Name || a.Tools[i].Description != b.Tools[i].Description ||
			string(a.Tools[i].Parameters) != string(b.Tools[i].Parameters) {
			return false
		}
	}
	return true
}

func TestRunGenericStreamErrorPersistsLocalDisplayAndInjectsBoundedRecovery(t *testing.T) {
	apiErr := errors.New("upstream reset")
	mp := testutil.NewMock("m",
		testutil.Turn{Reasoning: "private partial reasoning", Text: "visible partial", ChunkError: apiErr},
		testutil.Turn{Text: "continued safely"},
	)
	session := NewSession("system")
	a := New(mp, echoRegistry(), session, Options{}, event.Discard)

	if err := a.Run(withNoClosedLoop(context.Background()), "change the file"); !errors.Is(err, apiErr) {
		t.Fatalf("first Run error = %v, want %v", err, apiErr)
	}
	msgs := session.Snapshot()
	last := msgs[len(msgs)-1]
	if !last.LocalOnly || last.InterruptedTurn == nil || !last.InterruptedTurn.Pending {
		t.Fatalf("terminal stream error did not leave pending local recovery: %+v", last)
	}
	if last.Content != "visible partial" || last.ReasoningContent != "private partial reasoning" {
		t.Fatalf("local display lost streamed output: %+v", last)
	}

	if err := a.Run(withNoClosedLoop(context.Background()), "continue"); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	req := mp.Requests()[1]
	for _, message := range req.Messages {
		if message.LocalOnly || strings.Contains(message.Content, "visible partial") || strings.Contains(message.ReasoningContent, "private partial reasoning") {
			t.Fatalf("unsafe partial output leaked to provider: %+v", req.Messages)
		}
	}
	lastUser := req.Messages[len(req.Messages)-1]
	if lastUser.Role != provider.RoleUser || !strings.Contains(lastUser.Content, "<interrupted-turn-recovery>") ||
		!strings.Contains(lastUser.Content, "unsafe_partial_output: excluded") || !strings.Contains(lastUser.Content, "continue") {
		t.Fatalf("next user turn missing bounded recovery block: %+v", lastUser)
	}
	if got := StripTransientUserBlocks(lastUser.Content); got != "continue" {
		t.Fatalf("recovery block leaked into user display: %q", got)
	}
}

func TestRunRecoveryKeepsCompletedToolPairAndSummarizesChangedFile(t *testing.T) {
	session := NewSession("system")
	session.Add(provider.Message{Role: provider.RoleUser, Content: "update config"})
	session.Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
		ID: "done-1", Name: "write_file", Arguments: `{"path":"config.json","content":"{}"}`, Added: 1,
	}}})
	session.Add(provider.Message{Role: provider.RoleTool, ToolCallID: "done-1", Name: "write_file", Content: "wrote config.json"})
	session.Add(provider.Message{
		Role: provider.RoleTool, ToolCallID: provider.LocalOnlyToolID, Name: provider.LocalOnlyToolName, LocalOnly: true,
		ReasoningContent: "unsafe partial reasoning",
		InterruptedTurn: &provider.InterruptedTurnRecovery{
			Pending: true,
			CompletedTools: []provider.InterruptedToolSummary{{
				ID: "done-1", Name: "write_file", Files: []string{"config.json"}, Added: 1,
			}},
			InterruptedTools:        []string{"bash"},
			DroppedPartialReasoning: true,
		},
	})
	mp := testutil.NewMock("m", testutil.Turn{Text: "done"})
	a := New(mp, echoRegistry(), session, Options{}, event.Discard)
	if err := a.Run(withNoClosedLoop(context.Background()), "continue"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	req := mp.Requests()[0]
	if len(req.Messages) != 5 {
		t.Fatalf("provider request should contain system + user + complete pair + recovery user, got %+v", req.Messages)
	}
	if req.Messages[2].Role != provider.RoleAssistant || req.Messages[3].Role != provider.RoleTool {
		t.Fatalf("completed tool pair was not replayed canonically: %+v", req.Messages)
	}
	last := req.Messages[len(req.Messages)-1]
	for _, want := range []string{"write_file files=config.json diff=+1/-0", "interrupted_tools: bash", "inspect the current workspace", "continue"} {
		if !strings.Contains(last.Content, want) {
			t.Fatalf("recovery user message missing %q: %s", want, last.Content)
		}
	}
	if strings.Contains(last.Content, "unsafe partial reasoning") {
		t.Fatalf("raw partial reasoning leaked into recovery summary: %s", last.Content)
	}
}

// TestRunWellFormedToolLoopRoundTrips is the happy-path baseline: a tool round
// then a final answer. The session must end with the assistant answer and pair
// cleanly (the repair is a no-op on well-formed histories).
func TestRunWellFormedToolLoopRoundTrips(t *testing.T) {
	mp := testutil.NewMock("m",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "echo", Arguments: `{"text":"hi"}`}}},
		testutil.Turn{Text: "all set"},
	)
	a := New(mp, echoRegistry(), NewSession(""), Options{}, event.Discard)
	if err := a.Run(withNoClosedLoop(context.Background()), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	msgs := a.Session().Messages
	last := msgs[len(msgs)-1]
	if last.Role != provider.RoleAssistant || last.Content != "all set" {
		t.Fatalf("final message should be the assistant answer, got %+v", last)
	}
	before := len(msgs)
	if after := len(provider.SanitizeToolPairing(msgs)); after != before {
		t.Errorf("repair mutated a well-formed session: %d -> %d", before, after)
	}
}

// TestRunNotifiesWhenStreamRetriesExhausted pins the #9560 visibility fix:
// when every sampling attempt of a model round ends in a stream interruption,
// the run must surface a user-readable warn notice explaining the failure —
// not only the generic interrupted-turn record.
func TestRunNotifiesWhenStreamRetriesExhausted(t *testing.T) {
	interrupted := &provider.StreamInterruptedError{Err: errors.New("dial tcp: lookup gw.invalid: no such host"), Reason: provider.StreamInterruptIdleTimeout}
	script := make([]testutil.Turn, maxSamplingAttempts)
	for i := range script {
		script[i] = testutil.Turn{ChunkError: interrupted}
	}
	mp := testutil.NewMock("m", script...)
	sink := &recordSink{}
	a := New(mp, echoRegistry(), NewSession(""), Options{}, sink)

	err := a.Run(withNoClosedLoop(context.Background()), "go")
	if err == nil {
		t.Fatal("Run must fail after exhausting stream retries")
	}
	if !provider.IsStreamInterrupted(err) {
		t.Fatalf("terminal error = %v, want a stream interruption", err)
	}
	var sawExplanation bool
	for _, e := range sink.kinds(event.Notice) {
		if e.Level == event.LevelWarn && strings.Contains(e.Text, "idle timeout") {
			sawExplanation = true
			if e.Code != event.NoticeCodeStreamInterruptedIdleTimeout {
				t.Fatalf("stream interruption notice code = %q", e.Code)
			}
			if strings.Contains(e.Text, "gw.invalid") || strings.Contains(e.Text, "dial tcp") {
				t.Fatalf("notice leaks raw transport error text: %q", e.Text)
			}
		}
	}
	if !sawExplanation {
		notices := sink.kinds(event.Notice)
		t.Fatalf("no warn notice explains the exhausted stream; notices = %+v", notices)
	}
}
