package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

const incompleteReadKeyRule = "KEY_RULE_362_MUST_BE_READ"

type incompleteReadEventSink struct {
	mu     sync.Mutex
	events []event.Event
}

func (s *incompleteReadEventSink) Emit(e event.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *incompleteReadEventSink) hasCode(code string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if e.Code == code {
			return true
		}
	}
	return false
}

type inspectingProvider struct {
	inner  *scriptedProvider
	before func(int, provider.Request)
}

func (p *inspectingProvider) Name() string { return p.inner.Name() }

func (p *inspectingProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	if p.before != nil {
		p.before(p.inner.call, req)
	}
	return p.inner.Stream(ctx, req)
}

type incompleteReadMutationTool struct {
	mu    sync.Mutex
	calls int
}

func (*incompleteReadMutationTool) Name() string { return "incomplete_read_mutation" }
func (*incompleteReadMutationTool) Description() string {
	return "Mutate durable state for incomplete-read tests."
}
func (*incompleteReadMutationTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (*incompleteReadMutationTool) ReadOnly() bool { return false }
func (t *incompleteReadMutationTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	return "mutated", nil
}

func (t *incompleteReadMutationTool) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

func makeIncompleteReadFixture(t *testing.T, name string, lines, width, keyLine int, key string) string {
	t.Helper()
	var body strings.Builder
	for line := 1; line <= lines; line++ {
		prefix := fmt.Sprintf("rule-%04d ", line)
		payload := strings.Repeat(string(rune('a'+line%26)), max(width-len(prefix), 1))
		if line == keyLine {
			payload = key + " " + strings.Repeat("k", max(width-len(prefix)-len(key)-1, 1))
		}
		body.WriteString(prefix)
		body.WriteString(payload)
		body.WriteString("\r\n")
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func makeShortPagedReadFixture(t *testing.T, name string, lines, keyLine int, key string) string {
	t.Helper()
	var body strings.Builder
	for line := 1; line <= lines; line++ {
		value := "x"
		if line == keyLine {
			value = key
		}
		body.WriteString(value)
		body.WriteString("\r\n")
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func incompleteReadBuiltin(t *testing.T) tool.Tool {
	t.Helper()
	read, ok := tool.LookupBuiltin("read_file")
	if !ok {
		t.Fatal("read_file builtin is not registered")
	}
	return read
}

func expectedReadOutput(t *testing.T, read tool.Tool, args string) string {
	t.Helper()
	out, err := read.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("read_file fixture: %v", err)
	}
	return out
}

func recoveryCapabilityArgs(t *testing.T, callID, resultRef string, offset int) string {
	t.Helper()
	args, err := json.Marshal(map[string]any{
		"action":        "call",
		"capability_id": sessionToolResultCapabilityID,
		"arguments": map[string]any{
			"tool_call_id": callID,
			"result_ref":   resultRef,
			"offset":       offset,
			"limit":        toolResultPageMaxBytes,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(args)
}

func newLegacyIncompleteReadTestAgent(prov provider.Provider, read tool.Tool, session *Session, sink event.Sink, extra ...tool.Tool) *Agent {
	return newLegacyIncompleteReadTestAgentWithOptions(prov, read, session, sink, Options{ContextWindow: 64_000}, extra...)
}

func newLegacyIncompleteReadTestAgentWithOptions(prov provider.Provider, read tool.Tool, session *Session, sink event.Sink, opts Options, extra ...tool.Tool) *Agent {
	opts.ReadPipeline.LegacyCoordinator = true
	opts.ReadPipeline.LegacyImplicitFullReads = true
	return newIncompleteReadTestAgentWithOptions(prov, read, session, sink, opts, extra...)
}

func newIncompleteReadTestAgent(prov provider.Provider, read tool.Tool, session *Session, sink event.Sink, extra ...tool.Tool) *Agent {
	return newIncompleteReadTestAgentWithOptions(prov, read, session, sink, Options{ContextWindow: 64_000}, extra...)
}

func newIncompleteReadTestAgentWithOptions(prov provider.Provider, read tool.Tool, session *Session, sink event.Sink, opts Options, extra ...tool.Tool) *Agent {
	reg := tool.NewRegistry()
	reg.Add(read)
	for _, candidate := range extra {
		reg.Add(candidate)
	}
	reg.Add(NewUseCapabilityTool(context.Background(), nil, nil, reg, nil, nil, nil))
	return New(prov, reg, session, opts, sink)
}

func requestHasText(req provider.Request, text string) bool {
	for _, msg := range req.Messages {
		if strings.Contains(msg.Content, text) {
			return true
		}
	}
	return false
}

func TestReadFileTruncationUsesContiguousUTF8PrefixAndExactRecovery(t *testing.T) {
	path := makeIncompleteReadFixture(t, "更新笔记助手.txt", 430, 96, 362, incompleteReadKeyRule)
	read := incompleteReadBuiltin(t)
	args := fmt.Sprintf(`{"path":%q}`, path)
	full := expectedReadOutput(t, read, args)
	if len(full) < 43*1024 {
		t.Fatalf("fixture bytes=%d, want a recoverable result of at least 43 KiB", len(full))
	}

	bounded, notice := truncateToolOutputFor(full, "read_file", "read-43k")
	if notice == "" || len(bounded) > maxToolOutputBytes || !strings.Contains(bounded, "next_offset=") {
		t.Fatalf("read_file was not bounded with a continuation cursor: bytes=%d notice=%q", len(bounded), notice)
	}
	offset, ok := readFileRecoveryOffset(bounded)
	if !ok || offset <= 0 || offset >= len(full) {
		t.Fatalf("recovery offset=%d ok=%v total=%d", offset, ok, len(full))
	}
	marker := strings.Index(bounded, "\n\n…[truncated tool=read_file ")
	if marker != offset || bounded[:marker] != full[:offset] {
		t.Fatalf("visible prefix is not exactly full[:next_offset]: marker=%d offset=%d", marker, offset)
	}
	if strings.Contains(bounded[:marker], incompleteReadKeyRule) {
		t.Fatal("key rule unexpectedly landed in the first prefix; fixture no longer exercises continuation")
	}

	session := &Session{Messages: []provider.Message{{
		Role: provider.RoleTool, Name: "read_file", ToolCallID: "read-43k", Content: bounded, RawContent: full,
	}}}
	_, proxy := newToolResultCapabilityAgent(t, session)
	header, suffix, err := executeToolResultPage(t, proxy, "read-43k", toolResultRef("read-43k", full), offset, toolResultPageMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !header.Complete || header.Offset != offset || header.NextOffset != len(full) {
		t.Fatalf("unexpected terminal recovery header: %+v", header)
	}
	if got := full[:offset] + suffix; got != full {
		t.Fatalf("prefix + suffix did not reconstruct the original: got=%d want=%d", len(got), len(full))
	}
	if !strings.Contains(suffix, incompleteReadKeyRule) {
		t.Fatal("recovered suffix did not contain line 362's key rule")
	}
}

func TestIncompleteReadAllowsFinalWithoutForcedRecovery(t *testing.T) {
	path := makeIncompleteReadFixture(t, "rules-crlf.txt", 430, 96, 362, incompleteReadKeyRule)
	read := incompleteReadBuiltin(t)
	readArgs := fmt.Sprintf(`{"path":%q}`, path)

	inner := &scriptedProvider{name: "incomplete-read", turns: [][]provider.Chunk{
		{toolCallChunk("read-1", "read_file", readArgs), {Type: provider.ChunkDone}},
		textTurn("I can answer from the visible prefix."),
	}}
	agent := newLegacyIncompleteReadTestAgentWithOptions(
		inner, read, NewSession("sys"), event.Discard, Options{ContextWindow: 64_000},
	)
	if err := agent.Run(context.Background(), "Skim the rules file and answer."); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if inner.call != 2 {
		t.Fatalf("provider rounds=%d, want read + accepted final", inner.call)
	}
}

func TestIncompleteReadDoesNotPauseOnPrematureFinal(t *testing.T) {
	path := makeIncompleteReadFixture(t, "ignored.txt", 430, 96, 362, incompleteReadKeyRule)
	read := incompleteReadBuiltin(t)
	readArgs := fmt.Sprintf(`{"path":%q}`, path)
	prov := &scriptedProvider{name: "ignore-continuation", turns: [][]provider.Chunk{
		{toolCallChunk("read-ignore", "read_file", readArgs), {Type: provider.ChunkDone}},
		textTurn("first premature answer"),
	}}
	agent := newLegacyIncompleteReadTestAgentWithOptions(prov, read, NewSession("sys"), event.Discard, Options{ContextWindow: 256_000})
	if err := agent.Run(context.Background(), "Read it fully."); err != nil {
		t.Fatalf("Run error=%v, want clean final", err)
	}
	if prov.call != 2 {
		t.Fatalf("rounds=%d, want read + final", prov.call)
	}
}

func TestIncompleteReadAllowsSameBatchMutation(t *testing.T) {
	path := makeIncompleteReadFixture(t, "read-before-write.txt", 430, 96, 362, incompleteReadKeyRule)
	read := incompleteReadBuiltin(t)
	readArgs := fmt.Sprintf(`{"path":%q}`, path)
	writer := &incompleteReadMutationTool{}
	prov := &scriptedProvider{name: "read-then-write", turns: [][]provider.Chunk{
		{
			toolCallChunk("read-mutate", "read_file", readArgs),
			toolCallChunk("mutate", writer.Name(), `{}`),
			{Type: provider.ChunkDone},
		},
		textTurn("done"),
	}}
	agent := newLegacyIncompleteReadTestAgent(prov, read, NewSession("sys"), event.Discard, writer)
	if err := agent.Run(context.Background(), "Read the rules and then update state."); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if writer.count() != 1 {
		t.Fatalf("mutation executed %d times, want 1", writer.count())
	}
	if got := toolResultByID(agent.Session(), "mutate"); strings.Contains(got, "unread content") {
		t.Fatalf("mutation result=%q, must not be blocked by incomplete-read", got)
	}
}

func TestIncompleteReadRecoversParallelFilesBeforeFinal(t *testing.T) {
	const keyA = "PARALLEL_KEY_A_362"
	const keyB = "PARALLEL_KEY_B_362"
	pathA := makeIncompleteReadFixture(t, "parallel-a.txt", 430, 96, 362, keyA)
	pathB := makeIncompleteReadFixture(t, "parallel-b.txt", 430, 96, 362, keyB)
	read := incompleteReadBuiltin(t)
	argsA := fmt.Sprintf(`{"path":%q}`, pathA)
	argsB := fmt.Sprintf(`{"path":%q}`, pathB)
	fullA := expectedReadOutput(t, read, argsA)
	fullB := expectedReadOutput(t, read, argsB)
	boundedA, _ := truncateToolOutputFor(fullA, "read_file", "read-a")
	boundedB, _ := truncateToolOutputFor(fullB, "read_file", "read-b")
	offsetA, okA := readFileRecoveryOffset(boundedA)
	offsetB, okB := readFileRecoveryOffset(boundedB)
	if !okA || !okB {
		t.Fatal("parallel fixtures did not produce recovery offsets")
	}
	prov := &scriptedProvider{name: "parallel-incomplete-reads", turns: [][]provider.Chunk{
		{
			toolCallChunk("read-a", "read_file", argsA),
			toolCallChunk("read-b", "read_file", argsB),
			{Type: provider.ChunkDone},
		},
		{
			toolCallChunk("recover-a", "use_capability", recoveryCapabilityArgs(t, "read-a", toolResultRef("read-a", fullA), offsetA)),
			toolCallChunk("recover-b", "use_capability", recoveryCapabilityArgs(t, "read-b", toolResultRef("read-b", fullB), offsetB)),
			{Type: provider.ChunkDone},
		},
		textTurn("Both files were recovered completely."),
	}}
	agent := newLegacyIncompleteReadTestAgentWithOptions(prov, read, NewSession("sys"), event.Discard, Options{ContextWindow: 256_000})
	if err := agent.Run(context.Background(), "Read both rules files completely."); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if prov.call != 3 || len(agent.task.ledger.TextObservations()) != 2 {
		t.Fatalf("rounds=%d observations=%d, want both reads complete before final", prov.call, len(agent.task.ledger.TextObservations()))
	}
	if !requestHasText(prov.requests[2], keyA) || !requestHasText(prov.requests[2], keyB) {
		t.Fatal("accepted final request did not contain both recovered tails")
	}
}

func TestExplicitFullReadContinuesSourcePagesToEOF(t *testing.T) {
	const tailKey = "SOURCE_PAGE_KEY_2050"
	path := makeShortPagedReadFixture(t, "more-than-default-limit.txt", 2105, 2050, tailKey)
	read := incompleteReadBuiltin(t)
	firstArgs := fmt.Sprintf(`{"path":%q,"intent":"full"}`, path)
	secondArgs := fmt.Sprintf(`{"path":%q,"offset":2000,"limit":2000}`, path)
	first := expectedReadOutput(t, read, firstArgs)
	if len(first) >= maxToolOutputBytes || !strings.Contains(first, "pass offset=2000") {
		t.Fatalf("first source page bytes=%d does not exercise the under-cap source trailer", len(first))
	}
	inner := &scriptedProvider{name: "source-pages", turns: [][]provider.Chunk{
		{toolCallChunk("source-1", "read_file", firstArgs), {Type: provider.ChunkDone}},
		{toolCallChunk("source-2", "read_file", secondArgs), {Type: provider.ChunkDone}},
		textTurn("Reached EOF and found the tail rule."),
	}}
	var agent *Agent
	prov := &inspectingProvider{inner: inner, before: func(round int, req provider.Request) {
		if round == 1 && len(agent.task.ledger.TextObservations()) != 0 {
			t.Error("the first source window was credited before the explicit full read reached EOF")
		}
		if round == 2 && !requestHasText(req, tailKey) {
			t.Error("the accepted final request did not contain the source tail page")
		}
	}}
	agent = newLegacyIncompleteReadTestAgent(prov, read, NewSession("sys"), event.Discard)
	if err := agent.Run(context.Background(), "Read the entire file without a line limit."); err != nil {
		t.Fatalf("Run: %v", err)
	}
	observations := agent.task.ledger.TextObservations()
	if len(observations) != 2 {
		t.Fatalf("source observation windows=%d, want 2", len(observations))
	}
	if len(observations[0].LineHashes) != 2000 || len(observations[1].LineHashes) != 105 {
		t.Fatalf("source observation lines=(%d,%d), want deferred (2000,105)", len(observations[0].LineHashes), len(observations[1].LineHashes))
	}
}

func TestExplicitReadLimitConsumesOnlyRequestedWindow(t *testing.T) {
	path := makeShortPagedReadFixture(t, "explicit-window.txt", 2105, 2050, "outside-window")
	read := incompleteReadBuiltin(t)
	args := fmt.Sprintf(`{"path":%q,"offset":0,"limit":2000}`, path)
	prov := &scriptedProvider{name: "explicit-window", turns: [][]provider.Chunk{
		{toolCallChunk("window", "read_file", args), {Type: provider.ChunkDone}},
		textTurn("The requested window is complete."),
	}}
	agent := newLegacyIncompleteReadTestAgent(prov, read, NewSession("sys"), event.Discard)
	if err := agent.Run(context.Background(), "Read only the specified window."); err != nil {
		t.Fatalf("Run: %v", err)
	}
	observations := agent.task.ledger.TextObservations()
	if prov.call != 2 || len(observations) != 1 || len(observations[0].LineHashes) != 2000 {
		t.Fatalf("rounds=%d observations=%+v, explicit limit should not arm an EOF continuation", prov.call, observations)
	}
}

func TestIncompleteReadAutomaticallyRecoversBeyondFormerByteAndTokenCaps(t *testing.T) {
	path := makeIncompleteReadFixture(t, "oversize.txt", 1500, 96, 1400, "OVERSIZE_TAIL_RULE")
	read := incompleteReadBuiltin(t)
	readArgs := fmt.Sprintf(`{"path":%q}`, path)
	full := expectedReadOutput(t, read, readArgs)
	if len(full) <= 128*1024 || estimateCrossLanguageReadTokens(full) <= 32*1024 {
		t.Fatalf("fixture bytes=%d tokens=%d, want beyond former byte and token caps", len(full), estimateCrossLanguageReadTokens(full))
	}
	sink := &incompleteReadEventSink{}
	turns := [][]provider.Chunk{
		{toolCallChunk("read-oversize", "read_file", readArgs), {Type: provider.ChunkDone}},
	}
	bounded, _ := truncateToolOutputFor(full, "read_file", "read-oversize")
	next, ok := readFileRecoveryOffset(bounded)
	if !ok {
		t.Fatal("large fixture did not truncate")
	}
	for page := 0; next < len(full); page++ {
		turns = append(turns, []provider.Chunk{
			toolCallChunk(fmt.Sprintf("recover-large-%d", page), "use_capability", recoveryCapabilityArgs(t, "read-oversize", toolResultRef("read-oversize", full), next)),
			{Type: provider.ChunkDone},
		})
		end := min(len(full), next+toolResultPageMaxBytes)
		for end > next && end < len(full) && !utf8.RuneStart(full[end]) {
			end--
		}
		next = end
	}
	turns = append(turns, textTurn("The entire large file was read."))
	prov := &scriptedProvider{name: "large-auto-recovery", turns: turns}
	agent := newLegacyIncompleteReadTestAgentWithOptions(prov, read, NewSession("sys"), sink, Options{ContextWindow: 1_000_000})
	if err := agent.Run(context.Background(), "Read the complete oversized file."); err != nil {
		t.Fatalf("Run: %v", err)
	}
	observations := agent.task.ledger.TextObservations()
	if len(observations) != 1 || len(observations[0].LineHashes) != 1500 {
		t.Fatalf("observations=%+v, want complete 1500-line evidence", observations)
	}
	if sink.hasCode(event.NoticeCodeReadStrategyRequired) || sink.hasCode(event.NoticeCodeReadOversizeRejected) {
		t.Fatal("large read incorrectly entered strategy or legacy oversize rejection")
	}
}

func TestIncompleteReadContextLimitEntersSearchReadReceiptStrategy(t *testing.T) {
	path := makeIncompleteReadFixture(t, "context-soft-limit.txt", 430, 96, 362, incompleteReadKeyRule)
	read := incompleteReadBuiltin(t)
	readArgs := fmt.Sprintf(`{"path":%q}`, path)
	full := expectedReadOutput(t, read, readArgs)
	readID := incompleteReadID("read-context-soft", toolResultRef("read-context-soft", full), path)
	grepTool, ok := tool.LookupBuiltin("grep")
	if !ok {
		t.Fatal("grep builtin unavailable")
	}
	sink := &incompleteReadEventSink{}
	receipt, err := json.Marshal(map[string]any{
		"action": "call", "capability_id": sessionReadStrategyReceiptCapabilityID,
		"arguments": map[string]any{
			"read_id": readID, "search_tool_call_ids": []string{"grep-key"},
			"read_tool_call_ids": []string{"read-key-window"}, "conclusion": "The key rule and its surrounding section were searched and read.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	prov := &scriptedProvider{name: "context-strategy", turns: [][]provider.Chunk{
		{toolCallChunk("read-context-soft", "read_file", readArgs), {Type: provider.ChunkDone}},
		{toolCallChunk("grep-key", "grep", fmt.Sprintf(`{"pattern":%q,"path":%q}`, incompleteReadKeyRule, path)), {Type: provider.ChunkDone}},
		{toolCallChunk("read-key-window", "read_file", fmt.Sprintf(`{"path":%q,"offset":350,"limit":30}`, path)), {Type: provider.ChunkDone}},
		{toolCallChunk("strategy-receipt", "use_capability", string(receipt)), {Type: provider.ChunkDone}},
		textTurn("The answer is based on the searched and explicitly read rule window."),
	}}
	agent := newLegacyIncompleteReadTestAgentWithOptions(
		prov, read, NewSession("sys"), sink, Options{ContextWindow: 32_000}, grepTool,
	)
	if err := agent.Run(context.Background(), "Find and apply the key rule from the large rules file."); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if prov.call != 5 {
		t.Fatalf("provider rounds=%d, want read + grep + exact read + receipt + next-round final", prov.call)
	}
	observations := agent.task.ledger.TextObservations()
	if len(observations) != 1 || len(observations[0].LineHashes) != 30 {
		t.Fatalf("observations=%+v, want only the explicit 30-line strategy window", observations)
	}
	result := toolResultByID(agent.Session(), "read-context-soft")
	if !strings.Contains(result, "PARTIAL READ") || strings.Contains(result, incompleteReadKeyRule) {
		t.Fatalf("provider-visible strategy result is ambiguous or leaks an unseen tail: %.500q", result)
	}
	for _, code := range []string{event.NoticeCodeReadStrategyRequired, event.NoticeCodeReadStrategyProgress, event.NoticeCodeReadStrategyResolved} {
		if !sink.hasCode(code) {
			t.Errorf("missing strategy audit notice %q", code)
		}
	}
}

func TestIncompleteReadUnknownContextEntersStrategyWithoutImmediatePause(t *testing.T) {
	path := makeIncompleteReadFixture(t, "unknown-context.txt", 430, 96, 362, incompleteReadKeyRule)
	read := incompleteReadBuiltin(t)
	readArgs := fmt.Sprintf(`{"path":%q}`, path)
	prov := &scriptedProvider{name: "unknown-context", turns: [][]provider.Chunk{
		{toolCallChunk("read-unknown", "read_file", readArgs), {Type: provider.ChunkDone}},
		textTurn("answer from the visible prefix"),
	}}
	sink := &incompleteReadEventSink{}
	agent := newLegacyIncompleteReadTestAgentWithOptions(prov, read, NewSession("sys"), sink, Options{})
	if err := agent.Run(context.Background(), "Read the complete file."); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if prov.call != 2 || !sink.hasCode(event.NoticeCodeReadStrategyRequired) {
		t.Fatalf("rounds=%d strategy_notice=%v, want strategy marker then accepted final", prov.call, sink.hasCode(event.NoticeCodeReadStrategyRequired))
	}
	if got := toolResultByID(agent.Session(), "read-unknown"); !strings.Contains(got, "PARTIAL READ") {
		t.Fatalf("unknown-context result=%q, want explicit strategy marker", got)
	}
}

func TestReadStrategyReceiptUnlocksOnlyAfterBatchBoundary(t *testing.T) {
	path := makeIncompleteReadFixture(t, "receipt-boundary.txt", 430, 96, 362, incompleteReadKeyRule)
	read := incompleteReadBuiltin(t)
	grepTool, ok := tool.LookupBuiltin("grep")
	if !ok {
		t.Fatal("grep builtin unavailable")
	}
	full := expectedReadOutput(t, read, fmt.Sprintf(`{"path":%q}`, path))
	readID := incompleteReadID("read-boundary", toolResultRef("read-boundary", full), path)
	receipt, err := json.Marshal(map[string]any{
		"action": "call", "capability_id": sessionReadStrategyReceiptCapabilityID,
		"arguments": map[string]any{
			"read_id": readID, "search_tool_call_ids": []string{"grep-boundary"},
			"read_tool_call_ids": []string{"read-boundary-window"}, "conclusion": "searched and read the key rule",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	writer := &incompleteReadMutationTool{}
	prov := &scriptedProvider{name: "receipt-boundary", turns: [][]provider.Chunk{
		{toolCallChunk("read-boundary", "read_file", fmt.Sprintf(`{"path":%q}`, path)), {Type: provider.ChunkDone}},
		{toolCallChunk("grep-boundary", "grep", fmt.Sprintf(`{"pattern":%q,"path":%q}`, incompleteReadKeyRule, path)), {Type: provider.ChunkDone}},
		{toolCallChunk("read-boundary-window", "read_file", fmt.Sprintf(`{"path":%q,"offset":350,"limit":30}`, path)), {Type: provider.ChunkDone}},
		{
			toolCallChunk("receipt-boundary-call", "use_capability", string(receipt)),
			toolCallChunk("write-same-batch", writer.Name(), `{}`),
			{Type: provider.ChunkDone},
		},
		textTurn("The receipt is now committed; no write was attempted again."),
	}}
	agent := newLegacyIncompleteReadTestAgentWithOptions(prov, read, NewSession("sys"), event.Discard, Options{ContextWindow: 32_000}, grepTool, writer)
	if err := agent.Run(context.Background(), "Search and read the key rule, then mutate only after the receipt boundary."); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if writer.count() != 1 {
		t.Fatalf("same-batch writer executed %d times, want 1 (unpaid strategy pages do not freeze mutations)", writer.count())
	}
	if got := toolResultByID(agent.Session(), "write-same-batch"); strings.Contains(got, "restricted search/read mode") || strings.Contains(got, "unread content") {
		t.Fatalf("same-batch write result=%q, must not be blocked by strategy gate", got)
	}
	if prov.call != 5 {
		t.Fatalf("provider rounds=%d, want final after the receipt/write batch", prov.call)
	}
}

func TestReadStrategyViolationsCountOncePerModelRound(t *testing.T) {
	state := incompleteReadState{}
	state.addEntryLocked(&incompleteRead{
		key: "ir-round", readID: "ir-round", path: "rules.txt", requestPath: "rules.txt",
		phase: incompleteReadAutoResultPage, toolCallID: "read-1", resultRef: "tr-correct", nextByteOffset: 32000,
	})
	wrong := &toolCallPlan{
		evidenceName: "session_tool_result",
		evidenceArgs: json.RawMessage(`{"tool_call_id":"read-1","result_ref":"tr-wrong","offset":0,"limit":24576}`),
	}
	for range 3 {
		if _, blocked := state.gate(wrong); !blocked {
			t.Fatal("wrong continuation did not block")
		}
	}
	if round := state.finishToolRound(); round.pause != nil || state.consecutiveViolations != 1 {
		t.Fatalf("first violating round pause=%v consecutive=%d, want one violation", round.pause, state.consecutiveViolations)
	}
	if _, blocked := state.gate(wrong); !blocked {
		t.Fatal("wrong continuation did not block second round")
	}
	if round := state.finishToolRound(); round.pause == nil {
		t.Fatal("second consecutive violating model round did not pause")
	}
}

func TestReadStrategyReceiptRejectsChangedFileAndClearsEvidence(t *testing.T) {
	path := makeIncompleteReadFixture(t, "receipt-file-change.txt", 430, 96, 362, incompleteReadKeyRule)
	state := incompleteReadState{}
	entry := &incompleteRead{
		key: "ir-change", readID: "ir-change", path: path, phase: incompleteReadStrategy,
		strategyVersion: snapshotIncompleteReadFile(path),
		searches:        map[string]incompleteReadSearch{"grep": {callID: "grep", pattern: incompleteReadKeyRule, matchLines: []int{362}}},
		reads:           map[string]incompleteReadWindow{"read": {callID: "read", startLine: 350, endLine: 380}},
	}
	state.addEntryLocked(entry)
	entry.reads["read"] = incompleteReadWindow{
		callID: "read", startLine: 1, endLine: 20,
		observed: []tool.ModelTextObservation{{Path: path, StartLine: 1, LineHashes: []string{"synthetic"}}},
	}
	_, err := state.submitStrategyReceipt(context.Background(), readStrategyReceiptArgs{ReadID: "ir-change", SearchToolCallIDs: []string{"grep"}, ReadToolCallIDs: []string{"read"}, Conclusion: "wrong range"})
	if err == nil || !strings.Contains(err.Error(), "overlaps") || entry.pendingReceipt != nil {
		t.Fatalf("non-overlap receipt error=%v receipt=%v", err, entry.pendingReceipt)
	}
	entry.reads["read"] = incompleteReadWindow{
		callID: "read", startLine: 350, endLine: 380,
		observed: []tool.ModelTextObservation{{Path: path, StartLine: 350, LineHashes: []string{"synthetic"}}},
	}
	if err := os.WriteFile(path, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = state.submitStrategyReceipt(context.Background(), readStrategyReceiptArgs{
		ReadID: "ir-change", SearchToolCallIDs: []string{"grep"}, ReadToolCallIDs: []string{"read"}, Conclusion: "stale evidence",
	})
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("receipt error=%v, want changed-file rejection", err)
	}
	if len(entry.searches) != 0 || len(entry.reads) != 0 || entry.pendingReceipt != nil {
		t.Fatalf("stale evidence was retained: searches=%v reads=%v receipt=%v", entry.searches, entry.reads, entry.pendingReceipt)
	}
}

func TestReadStrategyReceiptRevalidatesWindowHashesWhenStatIsUnchanged(t *testing.T) {
	path := makeIncompleteReadFixture(t, "receipt-same-stat-change.txt", 430, 96, 362, incompleteReadKeyRule)
	stableTime := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(path, stableTime, stableTime); err != nil {
		t.Fatal(err)
	}
	read := incompleteReadBuiltin(t)
	args := fmt.Sprintf(`{"path":%q,"offset":349,"limit":31}`, path)
	output := expectedReadOutput(t, read, args)
	observer, ok := read.(tool.ModelTextObserver)
	if !ok {
		t.Fatal("read_file does not expose model-text observations")
	}
	observed, ok := observer.ObserveModelText(json.RawMessage(args), output)
	if !ok {
		t.Fatal("read_file window did not produce an observation")
	}

	state := incompleteReadState{}
	entry := &incompleteRead{
		key: "ir-same-stat", readID: "ir-same-stat", path: path, requestPath: path,
		phase: incompleteReadStrategy, strategyVersion: snapshotIncompleteReadFile(path), readTool: read,
		searches: map[string]incompleteReadSearch{"grep": {callID: "grep", pattern: incompleteReadKeyRule, matchLines: []int{362}}},
		reads: map[string]incompleteReadWindow{"read": {
			callID: "read", startLine: 350, endLine: 380,
			observed: []tool.ModelTextObservation{observed},
		}},
	}
	state.addEntryLocked(entry)

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := strings.Repeat("X", len(incompleteReadKeyRule))
	after := strings.Replace(string(before), incompleteReadKeyRule, replacement, 1)
	if len(after) != len(before) || after == string(before) {
		t.Fatal("fixture mutation did not preserve byte size")
	}
	if err := os.WriteFile(path, []byte(after), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stableTime, stableTime); err != nil {
		t.Fatal(err)
	}
	if current := snapshotIncompleteReadFile(path); !sameIncompleteReadFileVersion(entry.strategyVersion, current) {
		t.Fatalf("fixture stat changed: before=%+v after=%+v", entry.strategyVersion, current)
	}

	_, err = state.submitStrategyReceipt(context.Background(), readStrategyReceiptArgs{
		ReadID: "ir-same-stat", SearchToolCallIDs: []string{"grep"}, ReadToolCallIDs: []string{"read"}, Conclusion: "stale same-stat evidence",
	})
	if err == nil || !strings.Contains(err.Error(), "window changed") {
		t.Fatalf("receipt error=%v, want line-hash revalidation failure", err)
	}
	if len(entry.searches) != 0 || len(entry.reads) != 0 || entry.pendingReceipt != nil {
		t.Fatalf("same-stat stale evidence was retained: searches=%v reads=%v receipt=%v", entry.searches, entry.reads, entry.pendingReceipt)
	}
}

func TestReadStrategyKeepsOtherPendingFilesLocked(t *testing.T) {
	state := incompleteReadState{}
	first := &incompleteRead{
		key: "ir-a", readID: "ir-a", path: "a.txt", phase: incompleteReadStrategy,
		reads:          map[string]incompleteReadWindow{"read-a": {callID: "read-a", startLine: 1, endLine: 2}},
		pendingReceipt: &incompleteReadReceipt{readIDs: []string{"read-a"}},
	}
	second := &incompleteRead{
		key: "ir-b", readID: "ir-b", path: "b.txt", phase: incompleteReadStrategy,
		searches: map[string]incompleteReadSearch{}, reads: map[string]incompleteReadWindow{},
	}
	state.addEntryLocked(first)
	state.addEntryLocked(second)
	state.roundProgress = true
	round := state.finishToolRound()
	if len(round.resolvedIDs) != 1 || round.resolvedIDs[0] != "ir-a" || !state.hasPending() {
		t.Fatalf("round=%+v pending=%v, want only first file resolved", round, state.hasPending())
	}
	if instruction := state.nextInstruction(); !strings.Contains(instruction, "ir-b") {
		t.Fatalf("next instruction=%q, want remaining file", instruction)
	}
}

func TestReadStrategyCapabilityIsDynamicWithoutChangingProviderSchema(t *testing.T) {
	reg := tool.NewRegistry()
	proxy := NewUseCapabilityTool(context.Background(), nil, nil, reg, nil, nil, nil)
	reg.Add(proxy)
	schemaBefore := string(proxy.Schema())
	agent := New(&scriptedProvider{name: "strategy-capability"}, reg, NewSession("sys"), Options{}, event.Discard)
	agent.turn.incompleteReads.addEntryLocked(&incompleteRead{
		key: "ir-cap", readID: "ir-cap", path: "rules.txt", phase: incompleteReadStrategy,
		searches: map[string]incompleteReadSearch{}, reads: map[string]incompleteReadWindow{},
	})
	listed, err := proxy.listCapabilities()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listed, sessionReadStrategyReceiptCapabilityID) {
		t.Fatalf("active strategy capability missing from list: %s", listed)
	}
	if schemaAfter := string(proxy.Schema()); schemaAfter != schemaBefore {
		t.Fatalf("use_capability schema changed after dynamic strategy activation\nbefore=%s\nafter=%s", schemaBefore, schemaAfter)
	}
}

func TestIncompleteReadRequiresExactContinuationMetadata(t *testing.T) {
	state := incompleteReadState{}
	entry := &incompleteRead{
		key: "root", path: "rules.txt", phase: incompleteReadAutoResultPage,
		toolCallID: "read-1", resultRef: "tr-correct", nextByteOffset: 32000,
		totalBytes: 43000, sha256: strings.Repeat("a", 64),
	}
	state.addEntryLocked(entry)
	plan := &toolCallPlan{
		evidenceName: "session_tool_result",
		evidenceArgs: json.RawMessage(`{"tool_call_id":"read-1","result_ref":"tr-wrong","offset":0,"limit":24576}`),
	}
	if msg, blocked := state.gate(plan); !blocked || !strings.Contains(msg, "require result_ref=\"tr-correct\" offset=32000") {
		t.Fatalf("wrong continuation blocked=%v msg=%q", blocked, msg)
	}
	for _, finalizer := range []string{"complete_step", "submit_plan"} {
		finalizerState := incompleteReadState{}
		finalizerState.addEntryLocked(&incompleteRead{key: "pending", phase: incompleteReadAutoResultPage})
		if msg, blocked := finalizerState.gate(&toolCallPlan{evidenceName: finalizer}); blocked {
			t.Errorf("%s must not be frozen by unpaid pages: blocked=%v msg=%q", finalizer, blocked, msg)
		}
	}

	goodPlan := &toolCallPlan{incompleteReadRoot: "root"}
	badHeader := fmt.Sprintf(`{"result_ref":"tr-correct","offset":32000,"next_offset":43000,"total_bytes":43000,"sha256":%q,"complete":true}`,
		strings.Repeat("b", 64))
	state.observeResultPage(goodPlan, badHeader+"\n"+strings.Repeat("x", 11000), true)
	if failure := state.currentFailure(); failure == nil || !strings.Contains(failure.Reason, "integrity check") {
		t.Fatalf("digest mismatch failure=%+v", failure)
	}
}
