package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// A provider without the DeepSeek tool-call reasoning policy must keep the
// ordinary two-call tool loop even when its tool-call turn has no reasoning.
func TestRunNonDeepSeekMissingToolCallReasoningDoesNotRetry(t *testing.T) {
	mp := testutil.NewMock("openai",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "echo", Arguments: `{"text":"hi"}`}}},
		testutil.Turn{Text: "all set"},
	)
	sink := &recordSink{}
	a := New(mp, echoRegistry(), NewSession(""), Options{}, sink)

	if err := a.Run(withNoClosedLoop(context.Background()), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := mp.CallCount(); got != 2 {
		t.Fatalf("provider calls = %d, want tool turn + final turn without recovery retry", got)
	}
	if got := len(sink.kinds(event.ToolDispatch)); got != 1 {
		t.Fatalf("tool dispatches = %d, want one", got)
	}
	sink.mu.Lock()
	recovery := append([]event.ProtocolRecoveryAudit(nil), sink.recovery...)
	sink.mu.Unlock()
	if len(recovery) != 0 {
		t.Fatalf("non-DeepSeek provider emitted protocol recovery audits: %+v", recovery)
	}
}

// A one-off missing reasoning_content response is replaced before any tool
// executes. The retry reuses identical input, its usage is accounted for, and
// no provider-protocol warning or duplicate tool card reaches the user.
func TestRunSilentlyRecoversMissingToolCallReasoning(t *testing.T) {
	mp := testutil.NewMock("deepseek-proxy",
		testutil.Turn{
			ToolCalls: []provider.ToolCall{{ID: "c1", Name: "echo", Arguments: `{"text":"hi"}`}},
			Usage:     &provider.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, CacheMissTokens: 10, FinishReason: "tool_calls"},
		},
		testutil.Turn{
			Reasoning: "retry reasoning",
			ToolCalls: []provider.ToolCall{{ID: "c1", Name: "echo", Arguments: `{"text":"hi"}`}},
			Usage:     &provider.Usage{PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13, CacheHitTokens: 10, ReasoningTokens: 2, FinishReason: "tool_calls"},
		},
		testutil.Turn{Text: "done"},
	)
	sink := &recordSink{}
	a := New(strictToolCallReasoningProvider{mp}, echoRegistry(), NewSession(""), Options{}, sink)

	if err := a.Run(withNoClosedLoop(context.Background()), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var savedToolTurns int
	var savedReasoning string
	for _, m := range a.Session().Messages {
		if m.Role == provider.RoleAssistant && len(m.ToolCalls) > 0 {
			savedToolTurns++
			savedReasoning = m.ReasoningContent
		}
	}
	if savedToolTurns != 1 || savedReasoning != "retry reasoning" {
		t.Fatalf("saved tool turns = %d reasoning = %q, want one recovered turn: %+v", savedToolTurns, savedReasoning, a.Session().Messages)
	}
	if mp.CallCount() != 3 {
		t.Fatalf("provider calls = %d, want malformed + retry + final", mp.CallCount())
	}
	requests := mp.Requests()
	if len(requests) < 2 || !reflect.DeepEqual(requests[0], requests[1]) {
		t.Fatalf("protocol retry changed provider-visible request:\nfirst=%+v\nretry=%+v", requests[0], requests[1])
	}
	for _, e := range sink.kinds(event.Notice) {
		if strings.Contains(e.Text, "reasoning") || strings.Contains(e.Detail, "reasoning") {
			t.Fatalf("provider protocol leaked into user notice: %+v", e)
		}
	}
	if got := len(sink.kinds(event.ToolDispatch)); got != 1 {
		t.Fatalf("tool dispatches = %d, want one adopted call", got)
	}
	usageEvents := sink.kinds(event.Usage)
	if len(usageEvents) == 0 || usageEvents[0].Usage == nil || usageEvents[0].Usage.TotalTokens != 25 || usageEvents[0].Usage.CacheHitTokens != 10 || usageEvents[0].Usage.CacheMissTokens != 10 {
		t.Fatalf("recovery usage was not merged truthfully: %+v", usageEvents)
	}
	if sink.recoveryCount(event.ProtocolRecoveryMissingReasoningRetryAttempted) != 1 || sink.recoveryCount(event.ProtocolRecoveryMissingReasoningRetryRecovered) != 1 {
		t.Fatalf("unexpected recovery audit: %+v", sink.recovery)
	}
}

// An exact recovery replay may choose a normal final answer instead of
// repeating the original tool call. The replacement is authoritative because
// no tool has run yet: discard the speculative call, persist only the final
// response, and classify the outcome separately from recovered reasoning.
func TestMissingReasoningRecoveryAdoptsRetryWithoutToolCall(t *testing.T) {
	mp := testutil.NewMock("deepseek-proxy",
		testutil.Turn{
			ToolCalls: []provider.ToolCall{{ID: "discarded", Name: "echo", Arguments: `{"text":"must not run"}`}},
			Usage:     &provider.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, FinishReason: "tool_calls"},
		},
		testutil.Turn{
			Text:  "completed without a tool",
			Usage: &provider.Usage{PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13, FinishReason: "stop"},
		},
	)
	sink := &recordSink{}
	a := New(strictToolCallReasoningProvider{mp}, echoRegistry(), NewSession(""), Options{}, sink)

	if err := a.Run(withNoClosedLoop(context.Background()), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mp.CallCount() != 2 {
		t.Fatalf("provider calls = %d, want malformed + replacement", mp.CallCount())
	}
	var toolTurns, toolResults int
	for _, message := range a.Session().Messages {
		if message.Role == provider.RoleAssistant && len(message.ToolCalls) > 0 {
			toolTurns++
		}
		if message.Role == provider.RoleTool && !message.LocalOnly {
			toolResults++
		}
	}
	if toolTurns != 0 || toolResults != 0 {
		t.Fatalf("discarded tool response reached session: turns=%d results=%d session=%+v", toolTurns, toolResults, a.Session().Messages)
	}
	last := a.Session().Messages[len(a.Session().Messages)-1]
	if last.Role != provider.RoleAssistant || last.Content != "completed without a tool" {
		t.Fatalf("replacement response not adopted: %+v", last)
	}
	if got := len(sink.kinds(event.ToolDispatch)); got != 0 {
		t.Fatalf("discarded tool dispatches = %d, want 0", got)
	}
	usageEvents := sink.kinds(event.Usage)
	if len(usageEvents) == 0 || usageEvents[0].Usage == nil || usageEvents[0].Usage.TotalTokens != 25 {
		t.Fatalf("replacement usage was not merged truthfully: %+v", usageEvents)
	}
	if sink.recoveryCount(event.ProtocolRecoveryMissingReasoningRetryAttempted) != 1 ||
		sink.recoveryCount(event.ProtocolRecoveryMissingReasoningRetryReplaced) != 1 ||
		sink.recoveryCount(event.ProtocolRecoveryMissingReasoningRetryRecovered) != 0 ||
		sink.recoveryCount(event.ProtocolRecoveryMissingReasoningFallback) != 0 {
		t.Fatalf("unexpected recovery classification: %+v", sink.recovery)
	}
}

func TestCompatibleMissingReasoningKeepsOriginalWithoutRecovery(t *testing.T) {
	mp := testutil.NewMock("deepseek-proxy",
		testutil.Turn{
			ToolCalls: []provider.ToolCall{{ID: "c1", Name: "echo", Arguments: `{"text":"hi"}`}},
			Usage:     &provider.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, FinishReason: "tool_calls"},
		},
		testutil.Turn{Text: "done"},
	)
	sink := &recordSink{}
	a := New(toolCallReasoningRequiredProvider{mp}, echoRegistry(), NewSession(""), Options{}, sink)

	if err := a.Run(withNoClosedLoop(context.Background()), "go"); err != nil {
		t.Fatalf("Run should keep the provider-compatible original response, got %v", err)
	}
	var toolResults int
	for _, message := range a.Session().Messages {
		if message.Role == provider.RoleTool && message.ToolCallID == "c1" {
			toolResults++
		}
	}
	if toolResults != 1 {
		t.Fatalf("tool results = %d, want the original call executed once", toolResults)
	}
	usageEvents := sink.kinds(event.Usage)
	if len(usageEvents) == 0 || usageEvents[0].Usage == nil || usageEvents[0].Usage.TotalTokens != 12 {
		t.Fatalf("failed recovery usage was not accounted for: %+v", usageEvents)
	}
	if sink.recoveryCount(event.ProtocolRecoveryMissingReasoningFallback) != 0 {
		t.Fatalf("unexpected long-lived fallback audit: %+v", sink.recovery)
	}
}

func TestMissingReasoningRecoveryCancellationAccountsBothAttempts(t *testing.T) {
	prov := &cancelMissingReasoningRetryProvider{retryUsageSent: make(chan struct{})}
	sink := &recordSink{}
	a := New(prov, echoRegistry(), NewSession(""), Options{}, sink)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, "go") }()

	select {
	case <-prov.retryUsageSent:
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("timed out waiting for the recovery retry usage")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
	if got := prov.calls.Load(); got != 2 {
		t.Fatalf("provider calls = %d, want malformed response plus recovery retry", got)
	}
	if got := len(sink.kinds(event.ToolDispatch)); got != 0 {
		t.Fatalf("discarded tool dispatches = %d, want 0", got)
	}
	usages := sink.kinds(event.Usage)
	if len(usages) != 1 || usages[0].Usage == nil || usages[0].Usage.TotalTokens != 23 || usages[0].Usage.FinishReason != "interrupted" {
		t.Fatalf("recovery cancellation usage = %+v, want one merged interrupted total of 23", usages)
	}
}

func TestCompatibleMissingReasoningDoesNotRearmOnSessionChange(t *testing.T) {
	mp := testutil.NewMock("deepseek-proxy",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "echo", Arguments: `{"text":"hi"}`}}},
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "c1r", Name: "echo", Arguments: `{"text":"hi"}`}}},
		testutil.Turn{Text: "done"},
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "c2", Name: "echo", Arguments: `{"text":"hi"}`}}},
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "c2r", Name: "echo", Arguments: `{"text":"hi"}`}}},
		testutil.Turn{Text: "done again"},
	)
	sink := &recordSink{}
	a := New(toolCallReasoningRequiredProvider{mp}, echoRegistry(), NewSession(""), Options{}, sink)

	if err := a.Run(withNoClosedLoop(context.Background()), "go"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	a.SetSession(NewSession(""))
	if err := a.Run(withNoClosedLoop(context.Background()), "go"); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if got := sink.recoveryCount(event.ProtocolRecoveryMissingReasoningRetryAttempted); got != 0 {
		t.Fatalf("recovery retries across two sessions = %d, want 0", got)
	}
}

// A shared state dir turns the old warning cooldown into a cross-process retry
// circuit breaker. The first process retries once; a fresh process immediately
// uses the empty-key fallback without doubling the request.
func TestCompatibleMissingReasoningIgnoresLegacyIncidentState(t *testing.T) {
	stateDir := t.TempDir()
	mp := testutil.NewMock("deepseek-proxy",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "echo", Arguments: `{"text":"hi"}`}}},
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "c1r", Name: "echo", Arguments: `{"text":"hi"}`}}},
		testutil.Turn{Text: "done"},
	)
	sink1 := &recordSink{}
	a1 := New(toolCallReasoningRequiredProvider{mp}, echoRegistry(), NewSession(""), Options{MissingReasoningWarnStateDir: stateDir}, sink1)
	if err := a1.Run(withNoClosedLoop(context.Background()), "go"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if got := sink1.recoveryCount(event.ProtocolRecoveryMissingReasoningRetryAttempted); got != 0 {
		t.Fatalf("first process recovery retries = %d, want 0", got)
	}

	mp2 := testutil.NewMock("deepseek-proxy",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "c2", Name: "echo", Arguments: `{"text":"hi"}`}}},
		testutil.Turn{Text: "done again"},
	)
	sink2 := &recordSink{}
	a2 := New(toolCallReasoningRequiredProvider{mp2}, echoRegistry(), NewSession(""), Options{MissingReasoningWarnStateDir: stateDir}, sink2)
	if err := a2.Run(withNoClosedLoop(context.Background()), "go"); err != nil {
		t.Fatalf("second process Run: %v", err)
	}
	if got := sink2.recoveryCount(event.ProtocolRecoveryMissingReasoningRetryAttempted); got != 0 {
		t.Fatalf("fresh process recovery retries = %d, want 0", got)
	}
	if got := sink2.recoveryCount(event.ProtocolRecoveryMissingReasoningRetrySuppressed); got != 0 {
		t.Fatalf("fresh process suppressed retries = %d, want 0", got)
	}
}

func TestMissingReasoningRecoverySeparatesProviderConfigurations(t *testing.T) {
	stateDir := t.TempDir()
	retryCount := func(identity string) int {
		mp := testutil.NewMock("deepseek-proxy",
			testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "echo", Arguments: `{"text":"hi"}`}}},
			testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "c1r", Name: "echo", Arguments: `{"text":"hi"}`}}},
			testutil.Turn{Text: "done"},
		)
		sink := &recordSink{}
		a := New(configuredToolCallReasoningProvider{MockProvider: mp, identity: identity}, echoRegistry(), NewSession(""), Options{MissingReasoningWarnStateDir: stateDir}, sink)
		if err := a.Run(withNoClosedLoop(context.Background()), "go"); err != nil && !isReplayFailureForTest(err) {
			t.Fatalf("Run(%q): %v", identity, err)
		}
		return sink.recoveryCount(event.ProtocolRecoveryMissingReasoningRetryAttempted)
	}
	if got := retryCount("openai\x00endpoint-a\x00deepseek-v4-pro"); got != 1 {
		t.Fatalf("first configuration retries = %d, want 1", got)
	}
	if got := retryCount("openai\x00endpoint-a\x00deepseek-v4-pro"); got != 0 {
		t.Fatalf("same configuration retries = %d, want 0", got)
	}
	if got := retryCount("openai\x00endpoint-b\x00deepseek-v4-pro"); got != 1 {
		t.Fatalf("changed endpoint retries = %d, want 1", got)
	}
	if got := retryCount("openai\x00endpoint-a\x00deepseek-v4-flash"); got != 1 {
		t.Fatalf("changed model retries = %d, want 1", got)
	}
}

func TestThreeHealthyToolCallReasoningTurnsRearmFutureRegression(t *testing.T) {
	stateDir := t.TempDir()
	run := func(turns ...testutil.Turn) int {
		mp := testutil.NewMock("deepseek-proxy", turns...)
		sink := &recordSink{}
		a := New(strictToolCallReasoningProvider{mp}, echoRegistry(), NewSession(""), Options{MissingReasoningWarnStateDir: stateDir}, sink)
		if err := a.Run(withNoClosedLoop(context.Background()), "go"); err != nil && !isReplayFailureForTest(err) {
			t.Fatalf("Run: %v", err)
		}
		return sink.recoveryCount(event.ProtocolRecoveryMissingReasoningRetryAttempted)
	}
	missing := testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "echo", Arguments: `{"text":"hi"}`}}}
	healthy := testutil.Turn{Reasoning: "call echo", ToolCalls: []provider.ToolCall{{ID: "c2", Name: "echo", Arguments: `{"text":"hi"}`}}}
	if got := run(missing, missing, testutil.Turn{Text: "done"}); got != 1 {
		t.Fatalf("first incident retries = %d, want 1", got)
	}
	for healthyTurn := 1; healthyTurn <= missingReasoningHealthyResolveStreak; healthyTurn++ {
		if got := run(healthy, testutil.Turn{Text: "done"}); got != 0 {
			t.Fatalf("healthy turn %d retries = %d, want 0", healthyTurn, got)
		}
	}
	if got := run(missing, missing, testutil.Turn{Text: "done"}); got != 1 {
		t.Fatalf("post-recovery regression retries = %d, want 1", got)
	}
}

func TestHealthyToolCallReasoningStreakWorksWithinOneAgentAndResetsOnMissing(t *testing.T) {
	stateDir := t.TempDir()
	prov := strictToolCallReasoningProvider{testutil.NewMock("deepseek-proxy")}
	a := New(prov, echoRegistry(), NewSession(""), Options{MissingReasoningWarnStateDir: stateDir}, event.Discard)
	calls := []provider.ToolCall{{ID: "c1", Name: "echo", Arguments: `{"text":"hi"}`}}

	if missing, retry := a.observeMissingToolCallReasoning(calls, ""); !missing || !retry {
		t.Fatalf("initial observation = missing:%v retry:%v, want true/true", missing, retry)
	}
	for healthy := 1; healthy < missingReasoningHealthyResolveStreak; healthy++ {
		a.observeMissingToolCallReasoning(calls, "healthy reasoning")
	}
	if missing, retry := a.observeMissingToolCallReasoning(calls, ""); !missing || retry {
		t.Fatalf("missing reset = missing:%v retry:%v, want true/false", missing, retry)
	}
	for healthy := 1; healthy <= missingReasoningHealthyResolveStreak; healthy++ {
		a.observeMissingToolCallReasoning(calls, "healthy reasoning")
	}
	if missing, retry := a.observeMissingToolCallReasoning(calls, ""); !missing || !retry {
		t.Fatalf("post-recovery observation = missing:%v retry:%v, want true/true", missing, retry)
	}
}

func TestMissingReasoningRecoveryIOFailureStillSuppressesLocally(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(statePath, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	prov := strictToolCallReasoningProvider{testutil.NewMock("deepseek-proxy")}
	a := New(prov, echoRegistry(), NewSession(""), Options{MissingReasoningWarnStateDir: statePath}, event.Discard)
	calls := []provider.ToolCall{{ID: "c1", Name: "echo", Arguments: `{"text":"hi"}`}}

	if missing, retry := a.observeMissingToolCallReasoning(calls, ""); !missing || !retry {
		t.Fatalf("initial observation = missing:%v retry:%v, want true/true", missing, retry)
	}
	if missing, retry := a.observeMissingToolCallReasoning(calls, ""); !missing || retry {
		t.Fatalf("repeated observation = missing:%v retry:%v, want true/false", missing, retry)
	}
}

func TestHealthyToolCallReasoningRetriesTransientStateWriteFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod permissions are not portable to Windows")
	}
	stateDir := t.TempDir()
	prov := strictToolCallReasoningProvider{testutil.NewMock("deepseek-proxy")}
	a := New(prov, echoRegistry(), NewSession(""), Options{MissingReasoningWarnStateDir: stateDir}, event.Discard)
	calls := []provider.ToolCall{{ID: "c1", Name: "echo", Arguments: `{"text":"hi"}`}}

	if missing, retry := a.observeMissingToolCallReasoning(calls, ""); !missing || !retry {
		t.Fatalf("initial observation = missing:%v retry:%v, want true/true", missing, retry)
	}
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	permissionsRestored := false
	defer func() {
		if !permissionsRestored {
			_ = os.Chmod(stateDir, 0o700)
		}
	}()
	if missing, retry := a.observeMissingToolCallReasoning(calls, "healthy reasoning"); missing || retry {
		t.Fatalf("healthy observation = missing:%v retry:%v, want false/false", missing, retry)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	permissionsRestored = true
	for healthy := range missingReasoningHealthyResolveStreak - 1 {
		if missing, retry := a.observeMissingToolCallReasoning(calls, "healthy reasoning"); missing || retry {
			t.Fatalf("healthy recovery observation %d = missing:%v retry:%v, want false/false", healthy+1, missing, retry)
		}
	}

	if missing, retry := a.observeMissingToolCallReasoning(calls, ""); !missing || !retry {
		t.Fatalf("post-recovery observation = missing:%v retry:%v, want true/true", missing, retry)
	}
}

func TestRunPreservesOriginalRequiredToolCallReasoningAcrossHook(t *testing.T) {
	mp := testutil.NewMock("deepseek-proxy",
		testutil.Turn{
			Reasoning: "original reasoning",
			ToolCalls: []provider.ToolCall{{
				ID: "c1", Name: "echo", Arguments: `{"text":"hi"}`,
			}},
		},
		testutil.Turn{Text: "done"},
	)
	h := &stubHooks{hasPostLLM: true, postLLMOut: "translated display"}
	a := New(toolCallReasoningRequiredProvider{mp}, echoRegistry(), NewSession(""), Options{Hooks: h}, event.Discard)

	if err := a.Run(withNoClosedLoop(context.Background()), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := mp.Requests()
	if len(reqs) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(reqs))
	}
	var toolCallAssistant provider.Message
	for _, m := range reqs[1].Messages {
		if m.Role == provider.RoleAssistant && len(m.ToolCalls) > 0 {
			toolCallAssistant = m
			break
		}
	}
	if toolCallAssistant.ReasoningContent != "original reasoning" {
		t.Fatalf("tool-call reasoning = %q, want original provider reasoning", toolCallAssistant.ReasoningContent)
	}
	if toolCallAssistant.ReasoningContent == "translated display" {
		t.Fatal("translated display text leaked into provider-visible tool-call reasoning")
	}
}

func TestRunStoresTransformedNonToolReasoningForToolCallOnlyProvider(t *testing.T) {
	mp := testutil.NewMock("deepseek-proxy", testutil.Turn{
		Reasoning: "original reasoning",
		Text:      "done",
	})
	h := &stubHooks{hasPostLLM: true, postLLMOut: "translated display"}
	a := New(toolCallReasoningRequiredProvider{mp}, echoRegistry(), NewSession(""), Options{Hooks: h}, event.Discard)

	if err := a.Run(withNoClosedLoop(context.Background()), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := assistantReasoning(a.sess.conversation.Messages); got != "translated display" {
		t.Fatalf("stored non-tool reasoning = %q, want transformed display text", got)
	}
}
