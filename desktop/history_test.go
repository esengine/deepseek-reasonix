package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"reasonix/internal/agent"
	"reasonix/internal/boot"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/store"
	"reasonix/internal/tool"
)

func TestHistoryMessagesIncludeAssistantReasoning(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "expanded prompt", CreatedAt: 1_718_000_000_000},
		{Role: provider.RoleAssistant, Content: "answer", ReasoningContent: "thinking trace", WorkDurationMs: 24_000, ToolCalls: []provider.ToolCall{{
			ID: "call_1", Name: "bash", Arguments: `{"command":"pwd"}`,
		}}, MemoryCitations: []provider.MemoryCitation{{
			ID: "mem-1", Source: "Memory v5", Note: "use previous bash failure", Kind: "constraint",
		}}},
		{Role: provider.RoleTool, Name: "bash", ToolCallID: "call_1", Content: "tool output", ReasoningContent: "ignored by frontend filter"},
		{Role: provider.RoleAssistant, ReasoningContent: "tool-call-only thinking"},
	}

	got := historyMessages(msgs, func(content string) string {
		if content != "expanded prompt" {
			t.Fatalf("unexpected user content passed to resolver: %q", content)
		}
		return "display prompt"
	})

	if len(got) != len(msgs) {
		t.Fatalf("history length = %d, want %d", len(got), len(msgs))
	}
	if got[0].Content != "display prompt" {
		t.Fatalf("user display content = %q, want display prompt", got[0].Content)
	}
	if got[0].SubmitText != "expanded prompt" {
		t.Fatalf("user submit text = %q, want expanded prompt", got[0].SubmitText)
	}
	if got[0].CreatedAt != 1_718_000_000_000 {
		t.Fatalf("user createdAt = %d, want 1718000000000", got[0].CreatedAt)
	}
	if got[1].Reasoning != "thinking trace" {
		t.Fatalf("assistant reasoning = %q, want thinking trace", got[1].Reasoning)
	}
	if got[1].WorkDurationMs != 24_000 {
		t.Fatalf("assistant work duration = %d, want 24000", got[1].WorkDurationMs)
	}
	if len(got[1].MemoryCitations) != 1 || got[1].MemoryCitations[0].Note != "use previous bash failure" {
		t.Fatalf("assistant memory citations not preserved: %+v", got[1].MemoryCitations)
	}
	if len(got[1].ToolCalls) != 1 || got[1].ToolCalls[0].ID != "call_1" || got[1].ToolCalls[0].Name != "bash" {
		t.Fatalf("assistant tool calls not preserved: %+v", got[1].ToolCalls)
	}
	if !got[1].ToolCalls[0].ArgumentsArchived || got[1].ToolCalls[0].Arguments != "" || got[1].ToolCalls[0].Subject != "pwd" {
		t.Fatalf("assistant tool call was not restored as lightweight metadata: %+v", got[1].ToolCalls[0])
	}
	if got[2].ToolCallID != "call_1" || got[2].ToolName != "bash" || got[2].Content != "" || !got[2].ToolResultArchived {
		t.Fatalf("tool result details not preserved: %+v", got[2])
	}
	if got[2].Reasoning != "" {
		t.Fatalf("non-assistant reasoning should stay hidden, got %q", got[2].Reasoning)
	}
	if got[3].Reasoning != "tool-call-only thinking" {
		t.Fatalf("empty-content assistant reasoning = %q, want tool-call-only thinking", got[3].Reasoning)
	}
}

func TestHistoryMessagesReplayAttachedDecisionReceiptAfterAssistant(t *testing.T) {
	receipt := &provider.DecisionReceipt{ID: "approval-1", Kind: "tool", Tool: "bash", Outcome: "allow_once"}
	got := historyMessages([]provider.Message{
		{Role: provider.RoleUser, Content: "run it"},
		{
			Role:             provider.RoleAssistant,
			ToolCalls:        []provider.ToolCall{{ID: "call-1", Name: "bash", Arguments: `{}`}},
			DecisionReceipts: []*provider.DecisionReceipt{receipt},
		},
		{Role: provider.RoleTool, ToolCallID: "call-1", Name: "bash", Content: "ok"},
	}, func(content string) string { return content })

	if len(got) != 4 {
		t.Fatalf("history messages = %d, want user, assistant, receipt, tool: %+v", len(got), got)
	}
	if len(got[1].ToolCalls) != 1 || got[2].Code != event.NoticeCodeDecisionReceipt || got[2].DecisionReceipt == nil {
		t.Fatalf("history did not replay the decision after its assistant call: %+v", got)
	}
	if got[3].Role != "tool" || got[3].ToolCallID != "call-1" || !got[3].ToolResultArchived {
		t.Fatalf("history lost the actual tool result: %+v", got[3])
	}
}

func TestHistoryMessagesPreferPersistedRawUserContent(t *testing.T) {
	const raw = "fix the bug"
	const rendered = "<capability-route version=\"1\">\nuse review\n</capability-route>\n\nfix the bug"
	msgs := []provider.Message{{Role: provider.RoleUser, Content: rendered, RawContent: raw}}

	got := historyMessages(msgs, historyReplayUserContent)
	if len(got) != 1 || got[0].Content != raw {
		t.Fatalf("history user content = %+v, want raw %q", got, raw)
	}
	if got[0].SubmitText != "" {
		t.Fatalf("provider-only wrapper should not become replay text, got %q", got[0].SubmitText)
	}
	if strings.Contains(got[0].Content, "capability-route") || strings.Contains(got[0].SubmitText, "capability-route") {
		t.Fatalf("provider-only wrapper leaked into history: %+v", got[0])
	}
}

func TestHistoryMessagesRecoverLegacyExpandedPasteWithoutSidecar(t *testing.T) {
	const label = "[已粘贴文本 #1 · 3 行]"
	const display = "review this\n\n" + label
	const expanded = display + "\n\n--- Begin " + label + " ---\nfirst\nsecond\nthird\n--- End " + label + " ---"
	const rendered = "<active-goal>\nship the release\n</active-goal>\n\n" + expanded
	msgs := []provider.Message{{Role: provider.RoleUser, Content: rendered, RawContent: expanded}}

	got := historyMessages(msgs, historyReplayUserContent)
	if len(got) != 1 || got[0].Content != display {
		t.Fatalf("legacy pasted display = %+v, want %q", got, display)
	}
	if got[0].SubmitText != expanded {
		t.Fatalf("legacy pasted replay = %q, want expanded user input", got[0].SubmitText)
	}
	if strings.Contains(got[0].SubmitText, "<active-goal>") {
		t.Fatalf("transient goal leaked into legacy replay: %+v", got[0])
	}
}

func TestHistoryMessagesExpandedRawSupportsSidecarAndPreviousClients(t *testing.T) {
	const label = "[Pasted text #1 · 2 lines]"
	const display = "inspect\n\n" + label
	const expanded = display + "\n\n--- Begin " + label + " ---\none\ntwo\n--- End " + label + " ---"
	const rendered = "<capability-route version=\"1\">\nuse review\n</capability-route>\n\n" + expanded
	msgs := []provider.Message{{Role: provider.RoleUser, Content: rendered, RawContent: expanded}}

	// Previous desktop releases use RawContent as their replay source. Keeping
	// the expanded markers there lets them reconstruct the same inline card
	// instead of rendering an opaque label with no accessible payload.
	previousReplay := agent.UserMessageText(msgs[0])
	if !strings.Contains(previousReplay, "--- Begin "+label+" ---") || !strings.Contains(previousReplay, "--- End "+label+" ---") {
		t.Fatalf("previous-client replay lost pasted payload markers: %q", previousReplay)
	}

	got := historyMessages(msgs, func(content string) string {
		if content != rendered {
			t.Fatalf("sidecar resolver content = %q, want rendered content", content)
		}
		return display
	})
	if len(got) != 1 || got[0].Content != display || got[0].SubmitText != expanded {
		t.Fatalf("legacy sidecar history = %+v, want display %q and expanded replay", got, display)
	}
	if strings.Contains(got[0].SubmitText, "capability-route") {
		t.Fatalf("provider-only wrapper leaked into sidecar replay: %+v", got[0])
	}
}

func TestHistoryMessagesLegacyReferenceReplayExcludesResolvedContext(t *testing.T) {
	const raw = "@src/main.go explain the entrypoint"
	const rendered = "<capability-route version=\"1\">\nuse review\n</capability-route>\n\nReferenced context:\n\n<file path=\"src/main.go\">\npackage main\n</file>\n\n" + raw
	msgs := []provider.Message{{Role: provider.RoleUser, Content: rendered, RawContent: raw}}

	got := historyMessages(msgs, historyReplayUserContent)
	if len(got) != 1 || got[0].Content != raw || got[0].SubmitText != "" {
		t.Fatalf("legacy reference history = %+v, want compact raw replay", got)
	}
	if strings.Contains(got[0].Content, "<file") || strings.Contains(got[0].SubmitText, "<file") {
		t.Fatalf("resolved reference leaked into editable history: %+v", got[0])
	}
}

func TestHistoryMessagesDoNotReplayMemoryCompilerContract(t *testing.T) {
	raw := historyMemoryCompilerContract(t, "ship the refactor")
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: raw},
		{Role: provider.RoleAssistant, Content: "done"},
	}

	got := historyMessages(msgs, control.StripComposePrefixes)
	if len(got) != 2 {
		t.Fatalf("history length = %d, want 2: %+v", len(got), got)
	}
	if got[0].Content != "ship the refactor" {
		t.Fatalf("visible user content = %q, want source_event", got[0].Content)
	}
	if got[0].SubmitText != "" {
		t.Fatalf("raw Memory v5 contract should not be replay submitText, got %q", got[0].SubmitText)
	}
	assertNoHistoryMemoryContract(t, got[0].Content)
}

func TestHistoryMessagesRestoreCompiledSkillInvocationWithoutContract(t *testing.T) {
	raw := historyMemoryCompilerContract(t, "/reasonix-develop ship the refactor")
	msgs := []provider.Message{{Role: provider.RoleUser, Content: raw}}

	got := historyMessages(msgs, func(string) string { return "ship the refactor" })
	if len(got) != 1 {
		t.Fatalf("history length = %d, want 1: %+v", len(got), got)
	}
	if got[0].Content != "ship the refactor" || got[0].SubmitText != "/reasonix-develop ship the refactor" {
		t.Fatalf("compiled skill history = %+v", got[0])
	}
	assertNoHistoryMemoryContract(t, got[0].Content)
	assertNoHistoryMemoryContract(t, got[0].SubmitText)
}

func TestHistoryMessagesStripActiveGoalFromVisibleUserContent(t *testing.T) {
	raw := "<active-goal>\nship the approval redesign\n</active-goal>\n\ncontinue implementation"
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: raw},
		{Role: provider.RoleAssistant, Content: "done"},
	}

	got := historyMessages(msgs, control.StripComposePrefixes)
	if len(got) != 2 {
		t.Fatalf("history length = %d, want 2: %+v", len(got), got)
	}
	if got[0].Content != "continue implementation" {
		t.Fatalf("visible user content = %q, want active-goal stripped", got[0].Content)
	}
	if strings.Contains(got[0].Content, "<active-goal>") || strings.Contains(got[0].Content, "ship the approval redesign") {
		t.Fatalf("active-goal leaked into visible history content: %+v", got[0])
	}
}

func TestHistoryMessagesCarryCheckpointTurnsAcrossHiddenSyntheticUsers(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "first visible"},
		{Role: provider.RoleAssistant, Content: "first answer"},
		{Role: provider.RoleUser, Content: "Continue pursuing the active goal. If it is complete, provide the concise final result."},
		{Role: provider.RoleAssistant, Content: "hidden continuation"},
		{Role: provider.RoleUser, Content: "second visible"},
		{Role: provider.RoleAssistant, Content: "second answer"},
	}

	got := historyMessagesWithPlannerDisplays(
		msgs,
		func(content string) string { return content },
		nil,
		map[int]int{1: 0, 5: 2},
	)
	var users []HistoryMessage
	for _, msg := range got {
		if msg.Role == "user" {
			users = append(users, msg)
		}
	}
	if len(users) != 2 {
		t.Fatalf("visible users = %d, want 2: %+v", len(users), got)
	}
	if users[0].CheckpointTurn == nil || *users[0].CheckpointTurn != 0 {
		t.Fatalf("first checkpoint turn = %v, want 0", users[0].CheckpointTurn)
	}
	if users[1].CheckpointTurn == nil || *users[1].CheckpointTurn != 2 {
		t.Fatalf("second checkpoint turn = %v, want 2", users[1].CheckpointTurn)
	}
}

func TestHistoryPageFromMessagesWindowsByUserTurn(t *testing.T) {
	messages := []HistoryMessage{
		{Role: "notice", Content: "session restored"},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "one"},
		{Role: "tool", ToolName: "bash", Content: "tool one"},
		{Role: "user", Content: "second"},
		{Role: "assistant", Content: "two"},
		{Role: "user", Content: "third"},
		{Role: "assistant", Content: "three"},
	}

	latest := historyPageFromMessages(messages, 0, 2)
	if latest.StartTurn != 1 || latest.EndTurn != 3 || latest.TotalTurns != 3 || !latest.HasOlder {
		t.Fatalf("latest page metadata = %+v, want turns 1-3/3 hasOlder", latest)
	}
	if len(latest.Messages) != 4 || latest.Messages[0].Content != "second" || latest.Messages[3].Content != "three" {
		t.Fatalf("latest page messages = %+v, want second and third turns", latest.Messages)
	}

	older := historyPageFromMessages(messages, latest.StartTurn, 2)
	if older.StartTurn != 0 || older.EndTurn != 1 || older.TotalTurns != 3 || older.HasOlder {
		t.Fatalf("older page metadata = %+v, want turns 0-1/3 no older", older)
	}
	if len(older.Messages) != 4 || older.Messages[0].Content != "session restored" || older.Messages[1].Content != "first" {
		t.Fatalf("older page messages = %+v, want prelude and first turn", older.Messages)
	}
}

func TestHistoryPageWithFingerprintBindsRevisionToExactContentDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := agent.SaveBranchMeta(path, agent.BranchMeta{
		Revision:      7,
		ContentDigest: "digest-v7",
	}); err != nil {
		t.Fatalf("save branch metadata: %v", err)
	}

	page := historyPageWithFingerprint(HistoryPage{Messages: []HistoryMessage{{Role: "user", Content: "hello"}}}, path, "digest-v7")
	if page.Revision != 7 || page.Digest != "digest-v7" {
		t.Fatalf("history fingerprint = revision %d digest %q, want revision 7 digest-v7", page.Revision, page.Digest)
	}

	stale := historyPageWithFingerprint(HistoryPage{Messages: []HistoryMessage{{Role: "user", Content: "older"}}}, path, "digest-v6")
	if stale.Revision != 0 || stale.Digest != "digest-v6" {
		t.Fatalf("stale page fingerprint = revision %d digest %q, want content digest without mismatched revision", stale.Revision, stale.Digest)
	}
}

func TestHistoryPageFromProviderMessagesWindowsVisibleUsers(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "first"},
		{Role: provider.RoleAssistant, Content: "one"},
		{Role: provider.RoleUser, Content: "Continue pursuing the active goal. If it is complete, provide the concise final result."},
		{Role: provider.RoleAssistant, Content: "hidden continuation"},
		{Role: provider.RoleUser, Content: "second"},
		{Role: provider.RoleAssistant, Content: "two"},
		{
			Role: provider.RoleTool, Content: agent.MidTurnSteerPrefix + "\nupdate the plan",
			ToolCallID: provider.LocalOnlyToolID, Name: provider.LocalOnlyToolName, LocalOnly: true,
		},
		{Role: provider.RoleUser, Content: "third"},
		{Role: provider.RoleAssistant, Content: "three"},
	}

	latest := historyPageFromProviderMessages(
		msgs,
		func(content string) string { return content },
		nil,
		map[int]int{1: 0, 5: 2, 8: 3},
		0,
		2,
	)
	if latest.StartTurn != 1 || latest.EndTurn != 3 || latest.TotalTurns != 3 || !latest.HasOlder {
		t.Fatalf("latest page metadata = %+v, want turns 1-3/3 hasOlder", latest)
	}
	if len(latest.Messages) != 5 {
		t.Fatalf("latest page length = %d, want 5: %+v", len(latest.Messages), latest.Messages)
	}
	if latest.Messages[0].Role != "user" || latest.Messages[0].Content != "second" {
		t.Fatalf("first latest message = %+v, want second user", latest.Messages[0])
	}
	if latest.Messages[0].CheckpointTurn == nil || *latest.Messages[0].CheckpointTurn != 2 {
		t.Fatalf("second user checkpoint = %v, want 2", latest.Messages[0].CheckpointTurn)
	}
	if latest.Messages[2].Role != "notice" ||
		latest.Messages[2].Code != event.NoticeCodeUnappliedSteer ||
		latest.Messages[2].Level != "warn" ||
		!strings.Contains(latest.Messages[2].Content, "not applied") ||
		!strings.Contains(latest.Messages[2].Content, "update the plan") {
		t.Fatalf("steer message = %+v, want explicit unapplied notice in second turn window", latest.Messages[2])
	}
	if latest.Messages[3].Role != "user" || latest.Messages[3].Content != "third" {
		t.Fatalf("third latest message = %+v, want third user", latest.Messages[3])
	}
	if latest.Messages[3].CheckpointTurn == nil || *latest.Messages[3].CheckpointTurn != 3 {
		t.Fatalf("third user checkpoint = %v, want 3", latest.Messages[3].CheckpointTurn)
	}

	older := historyPageFromProviderMessages(
		msgs,
		func(content string) string { return content },
		nil,
		map[int]int{1: 0, 5: 2, 8: 3},
		latest.StartTurn,
		2,
	)
	if older.StartTurn != 0 || older.EndTurn != 1 || older.TotalTurns != 3 || older.HasOlder {
		t.Fatalf("older page metadata = %+v, want turns 0-1/3 no older", older)
	}
	if len(older.Messages) != 4 || older.Messages[0].Role != "system" || older.Messages[1].Content != "first" || older.Messages[3].Content != "hidden continuation" {
		t.Fatalf("older page messages = %+v, want prelude and first visible turn", older.Messages)
	}
}

func TestHistoryCheckpointTurnsSkipsHiddenUsers(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "first visible"},
		{Role: provider.RoleAssistant, Content: "ok"},
		{Role: provider.RoleUser, Content: "Continue pursuing the active goal. If it is complete, provide the concise final result."},
		{Role: provider.RoleUser, Content: "second visible"},
	}
	got := historyCheckpointTurns(
		msgs,
		func(content string) string { return content },
		map[int]int{0: 0, 2: 1, 3: 2},
	)
	if len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("checkpoint turns = %v, want [0 2]", got)
	}
}

func TestHistoryForTabRestoresPlannerDisplayAfterReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	handoff := strings.Join([]string{
		"# Reasonix executor handoff",
		"",
		"You are the executor now.",
		"",
		"Original task:",
		"fix the sandbox reload bug",
		"",
		"Planner output:",
		"inspect settings rebuild and preserve planner display",
		"",
		"Executor instructions:",
		"- apply the fix",
	}, "\n")

	sess := agent.NewSession("system")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: handoff})
	sess.Add(provider.Message{Role: provider.RoleAssistant, Content: "executor kept working"})
	ag := agent.New(stubProvider{}, tool.NewRegistry(), sess, agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{Executor: ag, SessionDir: dir, SessionPath: path, Sink: event.Discard})
	if err := recordSessionDisplay(dir, path, handoff, "fix the sandbox reload bug"); err != nil {
		t.Fatalf("recordSessionDisplay: %v", err)
	}

	app := &App{
		tabs:        map[string]*WorkspaceTab{},
		activeTabID: "planner_tab",
	}
	tab := &WorkspaceTab{ID: "planner_tab", Scope: "global", Ctrl: ctrl, Ready: true, disabledMCP: map[string]ServerView{}}
	tab.sink = &tabEventSink{tabID: tab.ID, app: app}
	app.tabs[tab.ID] = tab

	tab.sink.Emit(event.Event{Kind: event.TurnStarted})
	tab.sink.Emit(event.Event{Kind: event.Phase, Text: "deepseek-v4-pro · planning", Source: event.UsageSourcePlanner})
	tab.sink.Emit(event.Event{Kind: event.Reasoning, Text: "planner thinking\n", Source: event.UsageSourcePlanner})
	tab.sink.Emit(event.Event{Kind: event.Text, Text: "planner visible plan", Source: event.UsageSourcePlanner})
	tab.sink.Emit(event.Event{Kind: event.Message, Text: "planner visible plan", Reasoning: "planner thinking\n", Source: event.UsageSourcePlanner})
	tab.sink.Emit(event.Event{Kind: event.TurnStarted})
	tab.sink.Emit(event.Event{Kind: event.Text, Text: "executor kept working", Source: event.UsageSourceExecutor})
	tab.sink.Emit(event.Event{Kind: event.Message, Text: "executor kept working", Source: event.UsageSourceExecutor})
	tab.sink.Emit(event.Event{Kind: event.TurnDone})
	waitForAutosaveIdle(t, tab)

	got := app.HistoryForTab(tab.ID)
	if len(got) != 5 {
		t.Fatalf("history length = %d, want user + planner phase + planner answer + executor answer (plus system skipped later by UI): %+v", len(got), got)
	}
	if got[1].Content != "fix the sandbox reload bug" {
		t.Fatalf("user display content = %q, want original prompt", got[1].Content)
	}
	if got[2].Role != "phase" || !strings.Contains(got[2].Content, "planning") {
		t.Fatalf("planner phase missing after reload: %+v", got)
	}
	if got[3].Role != "assistant" || got[3].Content != "planner visible plan" || got[3].Reasoning != "planner thinking\n" {
		t.Fatalf("planner assistant display missing after reload: %+v", got[3])
	}
	if got[4].Role != "assistant" || got[4].Content != "executor kept working" {
		t.Fatalf("executor answer missing after reload: %+v", got[4])
	}
}

type cancelledDisplayRunner struct {
	session *agent.Session
	sink    event.Sink
	started chan struct{}
}

type blockingPlannerProvider struct {
	started chan struct{}
}

func (p *blockingPlannerProvider) Name() string { return "blocking-planner" }

func (p *blockingPlannerProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	close(p.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (r *cancelledDisplayRunner) Run(ctx context.Context, input string) error {
	r.session.Add(provider.Message{Role: provider.RoleUser, Content: input})
	r.session.Add(provider.Message{Role: provider.RoleAssistant, ReasoningContent: "checking settings\n", ToolCalls: []provider.ToolCall{{
		ID: "call_1", Name: "read_file", Arguments: `{"path":"settings.json"}`,
	}}})
	r.session.Add(provider.Message{Role: provider.RoleTool, ToolCallID: "call_1", Name: "read_file", Content: "partial settings"})
	r.sink.Emit(event.Event{Kind: event.Reasoning, Text: "checking settings\n", Source: event.UsageSourceExecutor})
	r.sink.Emit(event.Event{Kind: event.ToolDispatch, Source: event.UsageSourceExecutor, Tool: event.Tool{
		ID: "call_1", Name: "read_file", Args: `{"path":"settings.json"}`, ReadOnly: true,
	}})
	r.sink.Emit(event.Event{Kind: event.ToolResult, Source: event.UsageSourceExecutor, Tool: event.Tool{
		ID: "call_1", Name: "read_file", Output: "partial settings", Err: "cancelled",
	}})
	close(r.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestHistoryForTabRestoresCancelledExecutorDisplayAfterReload(t *testing.T) {
	dir := t.TempDir()
	path := agent.NewSessionPath(dir, "test-model")
	sess := agent.NewSession("system")
	app := &App{tabs: map[string]*WorkspaceTab{}, activeTabID: "cancelled_tab"}
	tab := &WorkspaceTab{ID: "cancelled_tab", Scope: "global", Ready: true, disabledMCP: map[string]ServerView{}}
	tab.sink = &tabEventSink{tabID: tab.ID, app: app}
	runner := &cancelledDisplayRunner{session: sess, sink: tab.sink, started: make(chan struct{})}
	ag := agent.New(stubProvider{}, tool.NewRegistry(), sess, agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{Runner: runner, Executor: ag, SessionDir: dir, SessionPath: path, Sink: tab.sink})
	tab.Ctrl = ctrl
	app.tabs[tab.ID] = tab

	ctrl.Send("continue setup")
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled turn did not start")
	}
	ctrl.Cancel()
	waitNotRunning(t, ctrl)
	waitForAutosaveIdle(t, tab)

	if got := ctrl.History(); len(got) != 5 || !got[4].LocalOnly {
		t.Fatalf("stored transcript should retain user + completed pair + local recovery: %+v", got)
	}
	got := app.HistoryForTab(tab.ID)
	if len(got) != 5 {
		t.Fatalf("history length = %d, want system + user + assistant + tool + notice: %+v", len(got), got)
	}
	if got[1].Role != "user" || got[1].Content != "continue setup" {
		t.Fatalf("cancelled turn user missing after reload: %+v", got[1])
	}
	if got[2].Role != "assistant" || got[2].Reasoning != "checking settings\n" || len(got[2].ToolCalls) != 1 || got[2].ToolCalls[0].Name != "read_file" {
		t.Fatalf("cancelled assistant display missing after reload: %+v", got[2])
	}
	if got[3].Role != "tool" || got[3].ToolName != "read_file" || got[3].Content != "partial settings" || got[3].ToolResultError != "partial settings" {
		t.Fatalf("cancelled tool display missing after reload: %+v", got[3])
	}
	if got[4].Role != "notice" || got[4].Code != event.NoticeCodeCancelledTurn {
		t.Fatalf("cancelled turn context notice missing after reload: %+v", got[4])
	}
}

func TestHistoryForTabRestoresPlannerDisplayWhenCancelledBeforeExecutorStarts(t *testing.T) {
	dir := t.TempDir()
	path := agent.NewSessionPath(dir, "test-model")
	app := &App{tabs: map[string]*WorkspaceTab{}, activeTabID: "planner_cancelled_tab"}
	tab := &WorkspaceTab{ID: "planner_cancelled_tab", Scope: "global", Ready: true, disabledMCP: map[string]ServerView{}}
	tab.sink = &tabEventSink{tabID: tab.ID, app: app}
	executorSession := agent.NewSession("system")
	executor := agent.New(stubProvider{}, tool.NewRegistry(), executorSession, agent.Options{}, tab.sink)
	planner := &blockingPlannerProvider{started: make(chan struct{})}
	runner := agent.NewCoordinator(planner, agent.NewSession("planner system"), nil, agent.PlannerToolRegistry(tool.NewRegistry()), agent.Options{}, executor, 0, tab.sink, nil)
	ctrl := control.New(control.Options{Runner: runner, Executor: executor, SessionDir: dir, SessionPath: path, Sink: tab.sink})
	defer ctrl.Close()
	ctrl.SetPlanMode(true)
	tab.Ctrl = ctrl
	app.tabs[tab.ID] = tab

	ctrl.Send("new question")
	select {
	case <-planner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("planner did not start")
	}
	ctrl.Cancel()
	waitNotRunning(t, ctrl)
	waitForAutosaveIdle(t, tab)

	canonical := ctrl.History()
	if len(canonical) != 3 || canonical[1].Role != provider.RoleUser || canonical[1].Content != "new question" || !canonical[2].LocalOnly {
		t.Fatalf("canonical history = %+v, want user plus provider-excluded recovery marker", canonical)
	}
	visible := app.HistoryForTab(tab.ID)
	if len(visible) != 4 {
		t.Fatalf("visible history length = %d, want system + user + planner phase + notice: %+v", len(visible), visible)
	}
	if visible[1].Role != "user" || visible[1].Content != "new question" {
		t.Fatalf("cancelled planner user missing after reload: %+v", visible[1])
	}
	if visible[2].Role != "phase" || !strings.Contains(visible[2].Content, "planning") {
		t.Fatalf("cancelled planner display missing after reload: %+v", visible[2])
	}
	if visible[3].Role != "notice" || visible[3].Code != event.NoticeCodeCancelledTurn {
		t.Fatalf("cancelled planner context notice missing after reload: %+v", visible[3])
	}
}

func TestCancelledExecutorDisplayFollowsDetachedAndReattachedRuntime(t *testing.T) {
	dir := t.TempDir()
	path := agent.NewSessionPath(dir, "test-model")
	app := &App{tabs: map[string]*WorkspaceTab{}, detachedSessions: map[string]*WorkspaceTab{}, activeTabID: "source_tab"}
	source := &WorkspaceTab{ID: "source_tab", Scope: "global", Ready: true, SessionPath: path, disabledMCP: map[string]ServerView{}}
	source.sink = &tabEventSink{tabID: source.ID, app: app}
	sess := agent.NewSession("system")
	runner := &cancelledDisplayRunner{session: sess, sink: source.sink, started: make(chan struct{})}
	executor := agent.New(stubProvider{}, tool.NewRegistry(), sess, agent.Options{}, event.Discard)
	ctrl := control.New(control.Options{Runner: runner, Executor: executor, SessionDir: dir, SessionPath: path, Sink: source.sink})
	defer ctrl.Close()
	source.Ctrl = ctrl
	app.tabs[source.ID] = source

	ctrl.Send("continue setup")
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled turn did not start")
	}
	if !app.detachRuntimeForReplacement(source) {
		t.Fatal("running session could not be detached")
	}
	key := sessionRuntimeKey(path)
	detached := app.detachedSessions[key]
	if detached == nil {
		t.Fatal("detached runtime missing")
	}
	target := &WorkspaceTab{ID: "reattached_tab", Scope: "global", Ready: true, disabledMCP: map[string]ServerView{}}
	app.mu.Lock()
	delete(app.tabs, source.ID)
	app.tabs[target.ID] = target
	delete(app.detachedSessions, key)
	applyRuntimeTab(target, detached, path, app.ctx, app)
	app.activeTabID = target.ID
	app.mu.Unlock()
	target.sink.Emit(event.Event{Kind: event.Text, Text: "after reattach", Source: event.UsageSourceExecutor})

	ctrl.Cancel()
	waitNotRunning(t, ctrl)
	waitForAutosaveIdle(t, target)
	visible := app.HistoryForTab(target.ID)
	var sawBefore, sawAfter, sawNotice bool
	for _, message := range visible {
		if message.Role == "assistant" && message.Reasoning == "checking settings\n" {
			sawBefore = true
		}
		if message.Role == "assistant" && message.Content == "after reattach" {
			sawAfter = true
		}
		if message.Role == "notice" && message.Code == event.NoticeCodeCancelledTurn {
			sawNotice = true
		}
	}
	if !sawBefore || !sawAfter || !sawNotice {
		t.Fatalf("reattached cancelled history lost display state: before=%v after=%v notice=%v history=%+v", sawBefore, sawAfter, sawNotice, visible)
	}
}

func TestHistoryForTabUsesPinnedSessionBeforeControllerReady(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := globalTabWorkspaceRoot()
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "pending-controller.jsonl")
	writeHistoryTestSession(t, path, "warm prompt")

	app := NewApp()
	tab := &WorkspaceTab{
		ID:            "pending",
		Scope:         "global",
		WorkspaceRoot: root,
		SessionPath:   path,
		Ready:         false,
		disabledMCP:   map[string]ServerView{},
	}
	app.tabs[tab.ID] = tab
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	got := app.HistoryForTab(tab.ID)
	if len(got) != 1 || got[0].Role != "user" || got[0].Content != "warm prompt" {
		t.Fatalf("pending controller history = %+v, want warm prompt", got)
	}
}

func historyMemoryCompilerContract(t *testing.T, sourceEvent string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"type": "memory_v5_execution_contract",
		"planner_ir": map[string]any{
			"source_event": sourceEvent,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return "<memory-compiler-execution>\n" + string(body) + "\n</memory-compiler-execution>"
}

func assertNoHistoryMemoryContract(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, "<memory-compiler-execution>") ||
		strings.Contains(text, "</memory-compiler-execution>") ||
		strings.Contains(text, "memory_v5_execution_contract") ||
		strings.Contains(text, "planner_ir") {
		t.Fatalf("history leaked Memory v5 contract content: %q", text)
	}
}

func TestHistoryMessagesArchiveCompletedToolPayloads(t *testing.T) {
	largeArgs := `{"command":"` + strings.Repeat("printf x;", 300) + `"}`
	largeOutput := strings.Repeat("line of output\n", 600)
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_large", Name: "bash", Arguments: largeArgs,
		}}},
		{Role: provider.RoleTool, Name: "bash", ToolCallID: "call_large", Content: largeOutput},
	}

	got := historyMessages(msgs, func(content string) string { return content })
	if len(got) != 2 {
		t.Fatalf("history length = %d, want 2", len(got))
	}
	call := got[0].ToolCalls[0]
	if !call.ArgumentsArchived {
		t.Fatalf("tool arguments were not marked archived: %+v", call)
	}
	if call.Arguments != "" {
		t.Fatalf("archived tool arguments should be omitted from initial history, got %d bytes", len(call.Arguments))
	}
	if call.Subject == "" {
		t.Fatalf("archived tool call should keep a collapsed subject: %+v", call)
	}
	if call.Summary == "" {
		t.Fatalf("archived tool call should keep a collapsed summary: %+v", call)
	}
	result := got[1]
	if !result.ToolResultArchived {
		t.Fatalf("tool result was not marked archived: %+v", result)
	}
	if result.Content != "" {
		t.Fatalf("archived successful tool output should be omitted from initial history, got %d bytes", len(result.Content))
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), largeArgs) || strings.Contains(string(encoded), largeOutput) {
		t.Fatalf("initial history JSON still contains large args/output: %d bytes", len(encoded))
	}
}

func TestHistoryMessagesPreserveResolvedCapabilityMetadata(t *testing.T) {
	resolvedReadOnly := false
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_capability", Name: "use_capability",
			Arguments:        `{"action":"call","capability_id":"mcp-tool:db/write"}`,
			ResolvedName:     "mcp__db__write",
			CapabilityID:     "mcp-tool:db/write",
			ResolvedReadOnly: &resolvedReadOnly,
		}}},
		{Role: provider.RoleTool, Name: "use_capability", ToolCallID: "call_capability", Content: "done"},
	}

	got := historyMessages(msgs, func(content string) string { return content })
	if len(got) != 2 || len(got[0].ToolCalls) != 1 {
		t.Fatalf("history = %+v", got)
	}
	call := got[0].ToolCalls[0]
	if call.ResolvedName != "mcp__db__write" || call.CapabilityID != "mcp-tool:db/write" ||
		call.ResolvedReadOnly == nil || *call.ResolvedReadOnly {
		t.Fatalf("resolved capability metadata = %+v", call)
	}
}

func TestHistoryMessagesKeepRunSkillSubjectWhenArchived(t *testing.T) {
	args := `{"name":"code-reviewer","arguments":"review this branch"}`
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_skill", Name: "run_skill", Arguments: args,
		}}},
		{Role: provider.RoleTool, Name: "run_skill", ToolCallID: "call_skill", Content: "Skill completed"},
	}

	got := historyMessages(msgs, func(content string) string { return content })
	if len(got) != 2 {
		t.Fatalf("history length = %d, want 2", len(got))
	}
	call := got[0].ToolCalls[0]
	if !call.ArgumentsArchived || call.Arguments != "" {
		t.Fatalf("run_skill arguments should be archived after completion: %+v", call)
	}
	if call.Subject != "code-reviewer" {
		t.Fatalf("run_skill subject = %q, want code-reviewer", call.Subject)
	}
}

func TestHistoryMessagesKeepToolFileDiffMetadata(t *testing.T) {
	diff := "@@ -27 +27 @@\n-func save():\n+func save_file():\n"
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID:        "edit",
			Name:      "edit_file",
			Arguments: `{"path":"settings/settings_IO.gd","old_string":"func save():","new_string":"func save_file():"}`,
			Diff:      diff,
			Added:     1,
			Removed:   1,
		}}},
		{Role: provider.RoleTool, Name: "edit_file", ToolCallID: "edit", Content: "edited settings/settings_IO.gd"},
	}

	got := historyMessages(msgs, func(content string) string { return content })
	call := got[0].ToolCalls[0]
	if call.Diff != diff || call.Added != 1 || call.Removed != 1 {
		t.Fatalf("history tool diff metadata = diff:%q +%d -%d", call.Diff, call.Added, call.Removed)
	}
	if !call.ArgumentsArchived || call.Arguments != "" {
		t.Fatalf("tool arguments should still be archived: %+v", call)
	}
}

func TestHistoryMessagesKeepBoundedToolErrors(t *testing.T) {
	largeError := "error: " + strings.Repeat("permission denied ", 400)
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_error", Name: "bash", Arguments: `{"command":"rm protected"}`,
		}}},
		{Role: provider.RoleTool, Name: "bash", ToolCallID: "call_error", Content: largeError},
	}

	got := historyMessages(msgs, func(content string) string { return content })
	result := got[1]
	if result.ToolResultError == "" {
		t.Fatalf("failed tool result should keep an error preview: %+v", result)
	}
	if result.Content != result.ToolResultError {
		t.Fatalf("tool result content and error preview diverged: content=%q error=%q", result.Content, result.ToolResultError)
	}
	if len(result.Content) >= len(largeError) {
		t.Fatalf("failed tool result preview was not bounded: got %d want < %d", len(result.Content), len(largeError))
	}
	if !strings.HasPrefix(result.Content, "error: permission denied") {
		t.Fatalf("failed tool result preview lost useful prefix: %q", result.Content[:min(len(result.Content), 80)])
	}
	if !result.ToolResultArchived {
		t.Fatalf("bounded failed tool result should still be marked archived for on-demand full data: %+v", result)
	}
}

func TestHistoryMessagesClipToolErrorsAtUTF8Boundary(t *testing.T) {
	largeError := "error: " + strings.Repeat("权限不足", 1000)
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_unicode_error", Name: "bash", Arguments: `{"command":"rm 受保护文件"}`,
		}}},
		{Role: provider.RoleTool, Name: "bash", ToolCallID: "call_unicode_error", Content: largeError},
	}

	got := historyMessages(msgs, func(content string) string { return content })
	result := got[1]
	if result.ToolResultError == "" {
		t.Fatalf("failed tool result should keep an error preview: %+v", result)
	}
	if !utf8.ValidString(result.ToolResultError) {
		t.Fatalf("failed tool result preview is not valid UTF-8: %q", result.ToolResultError)
	}
	if len(result.ToolResultError) >= len(largeError) {
		t.Fatalf("failed tool result preview was not bounded: got %d want < %d", len(result.ToolResultError), len(largeError))
	}
}

func TestHistoryMessagesKeepTodoWriteArguments(t *testing.T) {
	args := `{"todos":[{"content":"A","status":"in_progress"}]}`
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "todo_1", Name: "todo_write", Arguments: args,
		}}},
		{Role: provider.RoleTool, Name: "todo_write", ToolCallID: "todo_1", Content: "Todos updated"},
	}

	got := historyMessages(msgs, func(content string) string { return content })
	call := got[0].ToolCalls[0]
	if call.ArgumentsArchived {
		t.Fatalf("todo_write arguments must remain available for restored todo panel: %+v", call)
	}
	if call.Arguments != args {
		t.Fatalf("todo_write arguments = %q, want %q", call.Arguments, args)
	}
}

func TestHistoryMessagesPreserveUnaddressableToolPayloads(t *testing.T) {
	args := `{"command":"legacy"}`
	output := "legacy output\n" + strings.Repeat("detail\n", 8)
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			Name: "bash", Arguments: args,
		}}},
		{Role: provider.RoleTool, Name: "bash", Content: output},
	}

	got := historyMessages(msgs, func(content string) string { return content })
	call := got[0].ToolCalls[0]
	if call.ArgumentsArchived {
		t.Fatalf("tool call without an id cannot be archived for later lookup: %+v", call)
	}
	if call.Arguments != args {
		t.Fatalf("tool call without an id should keep args, got %q", call.Arguments)
	}
	result := got[1]
	if result.ToolResultArchived {
		t.Fatalf("tool result without an id cannot be archived for later lookup: %+v", result)
	}
	if result.Content != output {
		t.Fatalf("tool result without an id should keep output, got %q", result.Content)
	}
}

func TestPreviewSessionMessagesLoadsWithoutResuming(t *testing.T) {
	dir := t.TempDir()
	session := agent.NewSession("")
	session.Add(provider.Message{Role: provider.RoleUser, Content: "show history"})
	session.Add(provider.Message{Role: provider.RoleAssistant, Content: "answer", ReasoningContent: "saved reasoning"})
	path := filepath.Join(dir, "session.jsonl")
	if err := session.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := previewSessionMessages(dir, path)
	if err != nil {
		t.Fatalf("previewSessionMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("preview history length = %d, want 2", len(got))
	}
	if got[1].Reasoning != "saved reasoning" {
		t.Fatalf("preview reasoning = %q, want saved reasoning", got[1].Reasoning)
	}
}

func TestPreviewSessionMessagesUpgradesLegacyExpandedPaste(t *testing.T) {
	const label = "[Pasted text #1 · 2 lines]"
	const display = "inspect this\n\n" + label
	const expanded = display + "\n\n--- Begin " + label + " ---\none\ntwo\n--- End " + label + " ---"
	const rendered = "<capability-route version=\"1\">\nuse review\n</capability-route>\n\n" + expanded

	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.jsonl")
	session := agent.NewSession("")
	session.Add(provider.Message{Role: provider.RoleUser, Content: rendered, RawContent: expanded})
	if err := session.Save(path); err != nil {
		t.Fatalf("Save legacy session: %v", err)
	}
	if err := recordSessionDisplay(dir, path, rendered, display); err != nil {
		t.Fatalf("record legacy display: %v", err)
	}

	got, err := previewSessionMessages(dir, path)
	if err != nil {
		t.Fatalf("previewSessionMessages: %v", err)
	}
	if len(got) != 1 || got[0].Content != display || got[0].SubmitText != expanded {
		t.Fatalf("upgraded legacy preview = %+v, want display %q and expanded replay", got, display)
	}
	if strings.Contains(got[0].SubmitText, "capability-route") {
		t.Fatalf("provider-only wrapper leaked after session restart: %+v", got[0])
	}
}

func TestPreviewSessionMessagesUpgradesContentOnlyExpandedPaste(t *testing.T) {
	const label = "[Pasted text #1 · 2 lines]"
	const display = "inspect this\n\n" + label
	const expanded = display + "\n\n--- Begin " + label + " ---\none\ntwo\n--- End " + label + " ---"

	dir := t.TempDir()
	path := filepath.Join(dir, "content-only.jsonl")
	session := agent.NewSession("")
	// Releases before Context Engine v2 persisted user turns without RawContent.
	session.Add(provider.Message{Role: provider.RoleUser, Content: expanded})
	if err := session.Save(path); err != nil {
		t.Fatalf("Save content-only session: %v", err)
	}

	got, err := previewSessionMessages(dir, path)
	if err != nil {
		t.Fatalf("previewSessionMessages: %v", err)
	}
	if len(got) != 1 || got[0].Content != display || got[0].SubmitText != expanded {
		t.Fatalf("upgraded content-only preview = %+v, want display %q and expanded replay", got, display)
	}
}

func TestPreviewSessionMessagesIncludesProcessEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	body := strings.Join([]string{
		`{"kind":"phase","text":"Preparing context"}`,
		`{"kind":"notice","level":"warn","text":"Network changed"}`,
		`{"kind":"compaction_started","compaction":{"trigger":"manual"}}`,
		`{"kind":"compaction_done","compaction":{"trigger":"manual","messages":6,"summary":"Kept the current task.","archive":"/tmp/archive.jsonl"}}`,
		`{"type":"user.message","text":"hello","ts":1718000000000}`,
		`{"type":"model.final","content":"hi","reasoningContent":"thinking"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := previewSessionMessages(dir, path)
	if err != nil {
		t.Fatalf("previewSessionMessages: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("preview history length = %d, want 6: %+v", len(got), got)
	}
	if got[0].Role != "phase" || got[0].Content != "Preparing context" {
		t.Fatalf("phase event not preserved: %+v", got[0])
	}
	if got[1].Role != "notice" || got[1].Level != "warn" || got[1].Content != "Network changed" {
		t.Fatalf("notice event not preserved: %+v", got[1])
	}
	if got[2].Role != "compaction" || !got[2].Pending || got[2].Trigger != "manual" {
		t.Fatalf("pending compaction event not preserved: %+v", got[2])
	}
	if got[3].Role != "compaction" || got[3].Pending || got[3].Messages != 6 || got[3].Summary != "Kept the current task." || got[3].Archive != "/tmp/archive.jsonl" {
		t.Fatalf("finished compaction event not preserved: %+v", got[3])
	}
	if got[4].Role != "user" || got[5].Reasoning != "thinking" {
		t.Fatalf("conversation events not preserved: %+v", got[4:])
	}
	if got[4].CreatedAt != 1_718_000_000_000 {
		t.Fatalf("event user createdAt = %d, want 1718000000000", got[4].CreatedAt)
	}
}

func TestPreviewSessionMessagesRestoresAppendEventUserTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	session := agent.NewSession("")
	session.Add(provider.Message{Role: provider.RoleUser, Content: "first"})
	if err := session.SaveSnapshot(path); err != nil {
		t.Fatalf("SaveSnapshot first: %v", err)
	}
	session.Add(provider.Message{Role: provider.RoleAssistant, Content: "one"})
	session.Add(provider.Message{Role: provider.RoleUser, Content: "second"})
	if err := session.SaveSnapshot(path); err != nil {
		t.Fatalf("SaveSnapshot second: %v", err)
	}

	got, err := previewSessionMessages(dir, path)
	if err != nil {
		t.Fatalf("previewSessionMessages: %v", err)
	}
	if len(got) != 3 || got[2].Role != "user" || got[2].Content != "second" {
		t.Fatalf("preview history = %+v, want second user at index 2", got)
	}
	if got[2].CreatedAt <= 0 {
		t.Fatalf("append-event user timestamp was not restored: %+v", got[2])
	}
}

func TestResumeSessionForTabTargetsSpecifiedTab(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := desktopSessionDir(globalTabWorkspaceRoot())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	activePath := filepath.Join(dir, "active.jsonl")
	inactivePath := filepath.Join(dir, "inactive.jsonl")
	targetPath := filepath.Join(dir, "target.jsonl")
	writeHistoryTestSession(t, activePath, "active prompt")
	writeHistoryTestSession(t, inactivePath, "inactive prompt")
	writeHistoryTestSession(t, targetPath, "target prompt")

	activeExec := agent.New(nil, nil, agent.NewSession(""), agent.Options{}, event.Discard)
	inactiveExec := agent.New(nil, nil, agent.NewSession(""), agent.Options{}, event.Discard)
	activeCtrl := control.New(control.Options{Executor: activeExec, SessionDir: dir, SessionPath: activePath, Label: "active"})
	inactiveCtrl := control.New(control.Options{Executor: inactiveExec, SessionDir: dir, SessionPath: inactivePath, Label: "inactive"})
	defer activeCtrl.Close()
	defer inactiveCtrl.Close()

	app := &App{
		tabs: map[string]*WorkspaceTab{
			"active": {
				ID:            "active",
				Scope:         "global",
				WorkspaceRoot: globalTabWorkspaceRoot(),
				Ctrl:          activeCtrl,
				Ready:         true,
				sink:          &tabEventSink{tabID: "active"},
				disabledMCP:   map[string]ServerView{},
			},
			"inactive": {
				ID:            "inactive",
				Scope:         "global",
				WorkspaceRoot: globalTabWorkspaceRoot(),
				SessionPath:   inactivePath,
				Ctrl:          inactiveCtrl,
				Ready:         true,
				sink:          &tabEventSink{tabID: "inactive"},
				disabledMCP:   map[string]ServerView{},
			},
		},
		tabOrder:    []string{"active", "inactive"},
		activeTabID: "active",
	}

	got, err := app.ResumeSessionForTab("inactive", targetPath)
	if err != nil {
		t.Fatalf("ResumeSessionForTab: %v", err)
	}
	if activeCtrl.SessionPath() != activePath {
		t.Fatalf("active tab session path = %q, want %q", activeCtrl.SessionPath(), activePath)
	}
	if inactiveCtrl.SessionPath() != inactivePath {
		t.Fatalf("original inactive controller session path = %q, want %q", inactiveCtrl.SessionPath(), inactivePath)
	}
	if app.tabs["inactive"].Ctrl == inactiveCtrl {
		t.Fatal("resume to a different sessionPath mutated the existing controller in place")
	}
	if app.tabs["inactive"].Ctrl.SessionPath() != targetPath {
		t.Fatalf("inactive tab session path = %q, want %q", app.tabs["inactive"].Ctrl.SessionPath(), targetPath)
	}
	f := loadTabsFile()
	var savedInactive string
	for _, entry := range f.Tabs {
		if entry.ID == "inactive" {
			savedInactive = entry.SessionPath
			break
		}
	}
	if filepath.Clean(savedInactive) != filepath.Clean(targetPath) {
		t.Fatalf("saved inactive session path = %q, want %q", savedInactive, targetPath)
	}
	if len(got) != 2 || got[0].Role != string(provider.RoleSystem) || strings.TrimSpace(got[0].Content) == "" ||
		got[1].Role != string(provider.RoleUser) || got[1].Content != "target prompt" {
		t.Fatalf("resumed history = %+v, want fresh system prompt and target prompt", got)
	}
}

func TestResumeSessionForTabDetachesRunningRuntimeForDifferentSessionPath(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := desktopSessionDir(globalTabWorkspaceRoot())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	topicID := "topic_same"
	sessionA := filepath.Join(dir, "session-a.jsonl")
	sessionB := filepath.Join(dir, "session-b.jsonl")
	writeHistoryTestSession(t, sessionA, "session A prompt")
	writeHistoryTestSession(t, sessionB, "session B prompt")

	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	ctrlA := control.New(control.Options{
		Runner:      runner,
		SessionDir:  dir,
		SessionPath: sessionA,
		Label:       "session-a",
		Sink:        event.Discard,
	})
	defer ctrlA.Close()

	app := NewApp()
	tab := &WorkspaceTab{
		ID:            "topic-tab",
		Scope:         "global",
		WorkspaceRoot: globalTabWorkspaceRoot(),
		TopicID:       topicID,
		TopicTitle:    "Same topic",
		SessionPath:   sessionA,
		Ctrl:          ctrlA,
		Ready:         true,
		sink:          &tabEventSink{tabID: "topic-tab", app: app},
		disabledMCP:   map[string]ServerView{},
	}
	app.tabs[tab.ID] = tab
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	ctrlA.Submit("keep running")
	<-runner.started

	got, err := app.ResumeSessionForTab(tab.ID, sessionB)
	if err != nil {
		t.Fatalf("ResumeSessionForTab: %v", err)
	}
	if !ctrlA.Running() {
		t.Fatal("session A controller was cancelled while resuming session B")
	}
	if ctrlA.SessionPath() != sessionA {
		t.Fatalf("session A controller path = %q, want %q", ctrlA.SessionPath(), sessionA)
	}
	detached := app.detachedSessions[sessionRuntimeKey(sessionA)]
	if detached == nil || detached.Ctrl != ctrlA {
		t.Fatalf("session A runtime was not detached: %+v", detached)
	}
	if detached.ID == tab.ID {
		t.Fatalf("detached runtime kept visible tab id %q", detached.ID)
	}
	if detached.sink == nil {
		t.Fatal("detached runtime lost its event sink")
	}
	if detached.sink.tabID == tab.ID {
		t.Fatalf("detached sink tab id = %q, want non-visible id", detached.sink.tabID)
	}
	if app.tabs[tab.ID].Ctrl == ctrlA {
		t.Fatal("visible tab still points at session A runtime after resuming session B")
	}
	if gotPath := app.tabs[tab.ID].Ctrl.SessionPath(); gotPath != sessionB {
		t.Fatalf("visible tab session path = %q, want %q", gotPath, sessionB)
	}
	if len(got) != 2 || got[0].Role != string(provider.RoleSystem) || strings.TrimSpace(got[0].Content) == "" ||
		got[1].Role != string(provider.RoleUser) || got[1].Content != "session B prompt" {
		t.Fatalf("resumed history = %+v, want fresh system prompt and session B prompt", got)
	}

	visible := app.tabs[tab.ID]
	detached.sink.Emit(event.Event{Kind: event.TurnStarted})
	if visible.ActivityStatus != "" {
		t.Fatalf("detached runtime event changed visible tab status to %q", visible.ActivityStatus)
	}
	detached.sink.Emit(event.Event{Kind: event.ToolResult, Tool: event.Tool{
		Name:   "read_file",
		Args:   `{"path":"detached.go","offset":3,"limit":7}`,
		Output: "package main",
	}})
	if got := detached.telemetrySnapshot().ReadFiles; len(got) != 1 || got[0].Path != "detached.go" || got[0].Offset != 3 || got[0].Limit != 7 {
		t.Fatalf("detached runtime read telemetry = %+v", got)
	}
	if got := visible.telemetrySnapshot().ReadFiles; len(got) != 0 {
		t.Fatalf("detached runtime read telemetry was recorded on visible tab: %+v", got)
	}
	detached.sink.Emit(event.Event{Kind: event.Usage, Usage: &provider.Usage{PromptTokens: 42}})
	detached.sink.Emit(event.Event{Kind: event.TurnDone})
	if detached.usageTelemetry.PromptTokens != 42 {
		t.Fatalf("detached runtime usage was not recorded on detached tab: %+v", detached.usageTelemetry)
	}
	if detached.usageTelemetry.RequestCount != 1 {
		t.Fatalf("detached runtime request count = %d, want 1", detached.usageTelemetry.RequestCount)
	}
	if visible.usageTelemetry.PromptTokens != 0 {
		t.Fatalf("detached runtime usage was recorded on visible tab: %+v", visible.usageTelemetry)
	}
	if visible.saveAgain || visible.saving {
		t.Fatalf("detached runtime scheduled visible tab snapshot: saving=%v saveAgain=%v", visible.saving, visible.saveAgain)
	}

	close(runner.release)
	waitNotRunning(t, ctrlA)
}

func TestResumeSessionForTabReusesOpenSessionPathRuntime(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := desktopSessionDir(globalTabWorkspaceRoot())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	openPath := filepath.Join(dir, "open-session.jsonl")
	blankPath := filepath.Join(dir, "blank-session.jsonl")
	writeHistoryTestSession(t, openPath, "open prompt")
	writeHistoryTestSession(t, blankPath, "blank prompt")

	openSink := &tabEventSink{tabID: "open"}
	openSession, err := agent.LoadSession(openPath)
	if err != nil {
		t.Fatalf("LoadSession(open): %v", err)
	}
	openExec := agent.New(nil, nil, openSession, agent.Options{}, openSink)
	openCtrl := control.New(control.Options{
		Executor:    openExec,
		SessionDir:  dir,
		SessionPath: openPath,
		Label:       "open",
		Sink:        openSink,
	})
	blankCtrl := control.New(control.Options{SessionDir: dir, SessionPath: blankPath, Label: "blank", Sink: event.Discard})
	defer openCtrl.Close()
	defer blankCtrl.Close()

	app := NewApp()
	openTab := &WorkspaceTab{
		ID:            "open",
		Scope:         "global",
		WorkspaceRoot: globalTabWorkspaceRoot(),
		SessionPath:   openPath,
		Ctrl:          openCtrl,
		Label:         "open",
		Ready:         true,
		sink:          openSink,
		disabledMCP:   map[string]ServerView{},
	}
	if err := openTab.ensureSessionLease(openPath); err != nil {
		t.Fatalf("open ensureSessionLease: %v", err)
	}
	defer openTab.releaseSessionLease()

	blankTab := &WorkspaceTab{
		ID:            "blank",
		Scope:         "global",
		WorkspaceRoot: globalTabWorkspaceRoot(),
		SessionPath:   blankPath,
		Ctrl:          blankCtrl,
		Label:         "blank",
		Ready:         true,
		sink:          &tabEventSink{tabID: "blank", app: app},
		disabledMCP:   map[string]ServerView{},
	}
	if err := blankTab.ensureSessionLease(blankPath); err != nil {
		t.Fatalf("blank ensureSessionLease: %v", err)
	}
	defer blankTab.releaseSessionLease()

	app.tabs[openTab.ID] = openTab
	app.tabs[blankTab.ID] = blankTab
	app.tabOrder = []string{openTab.ID, blankTab.ID}
	app.activeTabID = blankTab.ID

	got, err := app.ResumeSessionForTab(blankTab.ID, openPath)
	if err != nil {
		t.Fatalf("ResumeSessionForTab: %v", err)
	}
	if len(got) != 1 || got[0].Content != "open prompt" {
		t.Fatalf("resumed history = %+v, want open prompt", got)
	}
	if blankTab.Ctrl != openCtrl {
		t.Fatalf("resumed controller = %p, want open controller %p", blankTab.Ctrl, openCtrl)
	}
	if blankTab.sink != openSink {
		t.Fatalf("resumed sink = %p, want open sink %p", blankTab.sink, openSink)
	}
	if openSink.tabID != blankTab.ID {
		t.Fatalf("sink tab id = %q, want %q", openSink.tabID, blankTab.ID)
	}
	if _, ok := app.tabs[openTab.ID]; ok {
		t.Fatal("source tab for reused runtime should be removed")
	}
	if gotKey := blankTab.sessionLeaseRuntimeKey(); gotKey != sessionRuntimeKey(openPath) {
		t.Fatalf("resumed tab lease key = %q, want %q", gotKey, sessionRuntimeKey(openPath))
	}
	if gotKey := openTab.sessionLeaseRuntimeKey(); gotKey != "" {
		t.Fatalf("source tab kept session lease %q", gotKey)
	}
	if lease, err := agent.TryAcquireSessionLease(openPath); err == nil {
		lease.Release()
		t.Fatal("resumed session path allowed a second writer lease")
	}
}

func TestRebindTabToLoadedSessionReusesPreloadedTranscript(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := globalTabWorkspaceRoot()
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	currentPath := filepath.Join(dir, "current.jsonl")
	targetPath := filepath.Join(dir, "target.jsonl")
	writeHistoryTestSession(t, currentPath, "current prompt")
	writeHistoryTestSession(t, targetPath, "target prompt")

	loaded, err := agent.LoadSession(targetPath)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if err := os.Remove(targetPath); err != nil {
		t.Fatalf("remove target session: %v", err)
	}

	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: currentPath, Label: "current", Sink: event.Discard})
	defer ctrl.Close()

	app := NewApp()
	tab := &WorkspaceTab{
		ID:            "tab",
		Scope:         "global",
		WorkspaceRoot: root,
		SessionPath:   currentPath,
		Ctrl:          ctrl,
		Ready:         true,
		sink:          &tabEventSink{tabID: "tab", app: app},
		disabledMCP:   map[string]ServerView{},
	}
	app.tabs[tab.ID] = tab
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	if err := app.rebindTabToLoadedSessionPath(tab, targetPath, loaded); err != nil {
		t.Fatalf("rebindTabToLoadedSessionPath: %v", err)
	}
	got := app.HistoryForTab(tab.ID)
	if len(got) != 2 || got[0].Role != string(provider.RoleSystem) || strings.TrimSpace(got[0].Content) == "" ||
		got[1].Role != string(provider.RoleUser) || got[1].Content != "target prompt" {
		t.Fatalf("rebound history = %+v, want fresh system prompt and target prompt", got)
	}
	if gotPath := app.tabs[tab.ID].Ctrl.SessionPath(); gotPath != targetPath {
		t.Fatalf("rebound session path = %q, want %q", gotPath, targetPath)
	}
}

func TestRebindTabToLoadedSessionPersistsAndRestoresSessionProfile(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := globalTabWorkspaceRoot()
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	currentPath := filepath.Join(dir, "current.jsonl")
	targetPath := filepath.Join(dir, "target.jsonl")
	writeHistoryTestSession(t, currentPath, "current prompt")
	writeHistoryTestSession(t, targetPath, "target prompt")
	if err := agent.SaveBranchMetaPreserveUpdated(targetPath, agent.BranchMeta{
		TokenMode:        boot.TokenModeFull,
		Mode:             "yolo",
		ToolApprovalMode: control.ToolApprovalYolo,
	}); err != nil {
		t.Fatalf("SaveBranchMetaPreserveUpdated target: %v", err)
	}

	loaded, err := agent.LoadSession(targetPath)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: currentPath, Label: "current", Sink: event.Discard})
	ctrl.SetMode(true, false)
	ctrl.SetToolApprovalMode(control.ToolApprovalAuto)
	defer ctrl.Close()

	app := NewApp()
	tab := &WorkspaceTab{
		ID:               "tab",
		Scope:            "global",
		WorkspaceRoot:    root,
		SessionPath:      currentPath,
		Ctrl:             ctrl,
		Ready:            true,
		mode:             "plan",
		toolApprovalMode: control.ToolApprovalAuto,
		sink:             &tabEventSink{tabID: "tab", app: app},
		disabledMCP:      map[string]ServerView{},
	}
	app.tabs[tab.ID] = tab
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	if err := app.rebindTabToLoadedSessionPath(tab, targetPath, loaded); err != nil {
		t.Fatalf("rebindTabToLoadedSessionPath: %v", err)
	}

	currentMeta, ok, err := agent.LoadBranchMeta(currentPath)
	if err != nil || !ok {
		t.Fatalf("LoadBranchMeta current ok=%v err=%v", ok, err)
	}
	if currentMeta.TokenMode != boot.TokenModeFull || currentMeta.AgentPreset != "" ||
		currentMeta.Mode != "plan" || currentMeta.ToolApprovalMode != control.ToolApprovalAuto {
		t.Fatalf("current session profile = token:%q preset:%q mode:%q approval:%q, want full/empty/plan/auto",
			currentMeta.TokenMode, currentMeta.AgentPreset, currentMeta.Mode, currentMeta.ToolApprovalMode)
	}
	if got := currentTabTokenMode(tab); got != boot.TokenModeFull {
		t.Fatalf("rebound token mode = %q, want full", got)
	}
	if got := currentTabMode(tab); got != "yolo" {
		t.Fatalf("rebound mode = %q, want yolo", got)
	}
	if got := currentTabToolApprovalMode(tab); got != control.ToolApprovalYolo {
		t.Fatalf("rebound tool approval = %q, want yolo", got)
	}
}

func TestRebindTabToDetachedSessionPreservesRunningSourceRuntime(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := globalTabWorkspaceRoot()
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	sourcePath := filepath.Join(dir, "running-source.jsonl")
	targetPath := filepath.Join(dir, "detached-target.jsonl")
	writeHistoryTestSession(t, sourcePath, "source prompt")
	writeHistoryTestSession(t, targetPath, "target prompt")
	loaded, err := agent.LoadSession(targetPath)
	if err != nil {
		t.Fatalf("load target: %v", err)
	}

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}

	sourceRunner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	sourceSink := &tabEventSink{tabID: "visible", app: app, ctx: app.ctx}
	targetSink := &tabEventSink{tabID: "detached", app: app}
	installNoopRuntimeEvents(app, sourceSink, targetSink)
	sourceCtrl := control.New(control.Options{
		Runner: sourceRunner, SessionDir: dir, SessionPath: sourcePath,
		Label: "source", Sink: sourceSink,
	})
	targetCtrl := control.New(control.Options{
		SessionDir: dir, SessionPath: targetPath, Label: "target", Sink: targetSink,
	})
	tab := &WorkspaceTab{
		ID: "visible", Scope: "global", WorkspaceRoot: root,
		SessionPath: sourcePath, Ctrl: sourceCtrl, Ready: true, sink: sourceSink,
		disabledMCP: map[string]ServerView{},
	}
	app.tabs[tab.ID] = tab
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	if err := tab.ensureSessionLease(sourcePath); err != nil {
		t.Fatalf("lease source: %v", err)
	}
	app.mu.Lock()
	app.newSessionRuntimeLocked(tab, sessionRuntimeKey(sourcePath))
	app.advanceSessionRuntimeEpochLocked(tab)
	app.mu.Unlock()

	targetLease, err := agent.TryAcquireSessionLease(targetPath)
	if err != nil {
		t.Fatalf("lease target: %v", err)
	}
	detachedTarget := &WorkspaceTab{
		ID: detachedRuntimeTabID(sessionRuntimeKey(targetPath)), Scope: "global",
		WorkspaceRoot: root, SessionPath: targetPath, Ctrl: targetCtrl,
		Ready: true, sink: targetSink, disabledMCP: map[string]ServerView{},
	}
	detachedTarget.adoptSessionLease(targetLease)
	app.mu.Lock()
	app.detachedSessions[sessionRuntimeKey(targetPath)] = detachedTarget
	app.newSessionRuntimeLocked(detachedTarget, sessionRuntimeKey(targetPath))
	app.advanceSessionRuntimeEpochLocked(detachedTarget)
	app.mu.Unlock()
	sourceReleased := false
	t.Cleanup(func() {
		if !sourceReleased {
			close(sourceRunner.release)
		}
		sourceCtrl.Close()
		targetCtrl.Close()
		tab.releaseSessionLease()
		app.mu.RLock()
		detachedSource := app.detachedSessions[sessionRuntimeKey(sourcePath)]
		app.mu.RUnlock()
		if detachedSource != nil {
			detachedSource.releaseSessionLease()
		}
	})

	sourceCtrl.Submit("keep source running")
	select {
	case <-sourceRunner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("source turn did not start")
	}

	if err := app.rebindTabToLoadedSessionPath(tab, targetPath, loaded); err != nil {
		t.Fatalf("reattach target: %v", err)
	}
	if tab.Ctrl != targetCtrl || tab.sessionLeaseRuntimeKey() != sessionRuntimeKey(targetPath) {
		t.Fatalf("visible target runtime = ctrl %p lease %q, want %p/%q",
			tab.Ctrl, tab.sessionLeaseRuntimeKey(), targetCtrl, sessionRuntimeKey(targetPath))
	}
	if !sourceCtrl.Running() {
		t.Fatal("reattaching the target cancelled the running source controller")
	}
	app.mu.RLock()
	detachedSource := app.detachedSessions[sessionRuntimeKey(sourcePath)]
	targetStillDetached := app.detachedSessions[sessionRuntimeKey(targetPath)]
	app.mu.RUnlock()
	if detachedSource == nil || detachedSource.Ctrl != sourceCtrl ||
		detachedSource.sessionLeaseRuntimeKey() != sessionRuntimeKey(sourcePath) {
		t.Fatalf("running source was not preserved as detached runtime: %#v", detachedSource)
	}
	if targetStillDetached != nil {
		t.Fatalf("target remained detached after reattach: %#v", targetStillDetached)
	}

	close(sourceRunner.release)
	sourceReleased = true
	waitNotRunning(t, sourceCtrl)
}

func TestRebindTabToDetachedSessionReleasesIdleSourceSharedHost(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := globalTabWorkspaceRoot()
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	sourcePath := filepath.Join(dir, "idle-source.jsonl")
	targetPath := filepath.Join(dir, "detached-target.jsonl")
	writeHistoryTestSession(t, sourcePath, "source prompt")
	writeHistoryTestSession(t, targetPath, "target prompt")
	loaded, err := agent.LoadSession(targetPath)
	if err != nil {
		t.Fatalf("load target: %v", err)
	}

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	hostKey := root
	sharedHost := app.acquireSharedHost(hostKey)
	if got := app.acquireSharedHost(hostKey); got != sharedHost {
		t.Fatal("source and detached target did not share one plugin host")
	}

	sourceSink := &tabEventSink{tabID: "visible", app: app, ctx: app.ctx}
	targetSink := &tabEventSink{tabID: "detached", app: app}
	installNoopRuntimeEvents(app, sourceSink, targetSink)
	sourceCtrl := control.New(control.Options{
		SessionDir: dir, SessionPath: sourcePath, Label: "source",
		Sink: sourceSink, Host: sharedHost,
	})
	targetCtrl := control.New(control.Options{
		SessionDir: dir, SessionPath: targetPath, Label: "target",
		Sink: targetSink, Host: sharedHost,
	})
	tab := &WorkspaceTab{
		ID: "visible", Scope: "global", WorkspaceRoot: root,
		SessionPath: sourcePath, Ctrl: sourceCtrl, Ready: true, sink: sourceSink,
		SharedHostKey: hostKey, disabledMCP: map[string]ServerView{},
	}
	app.tabs[tab.ID] = tab
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	if err := tab.ensureSessionLease(sourcePath); err != nil {
		t.Fatalf("lease source: %v", err)
	}
	app.mu.Lock()
	app.newSessionRuntimeLocked(tab, sessionRuntimeKey(sourcePath))
	app.advanceSessionRuntimeEpochLocked(tab)
	app.mu.Unlock()

	targetLease, err := agent.TryAcquireSessionLease(targetPath)
	if err != nil {
		t.Fatalf("lease target: %v", err)
	}
	detachedTarget := &WorkspaceTab{
		ID: detachedRuntimeTabID(sessionRuntimeKey(targetPath)), Scope: "global",
		WorkspaceRoot: root, SessionPath: targetPath, Ctrl: targetCtrl,
		Ready: true, sink: targetSink, SharedHostKey: hostKey,
		disabledMCP: map[string]ServerView{},
	}
	detachedTarget.adoptSessionLease(targetLease)
	app.mu.Lock()
	app.detachedSessions[sessionRuntimeKey(targetPath)] = detachedTarget
	app.newSessionRuntimeLocked(detachedTarget, sessionRuntimeKey(targetPath))
	app.advanceSessionRuntimeEpochLocked(detachedTarget)
	app.mu.Unlock()
	t.Cleanup(func() {
		sourceCtrl.Close()
		targetCtrl.Close()
		tab.releaseSessionLease()
		detachedTarget.releaseSessionLease()
		app.closeAllSharedHosts()
	})

	if refs, ok := sharedHostRefsForTest(t, app, hostKey); !ok || refs != 2 {
		t.Fatalf("shared host refs before reattach = %d, present=%v, want 2/true", refs, ok)
	}
	if err := app.rebindTabToLoadedSessionPath(tab, targetPath, loaded); err != nil {
		t.Fatalf("reattach target: %v", err)
	}
	if tab.Ctrl != targetCtrl || tab.SharedHostKey != hostKey {
		t.Fatalf("visible target runtime = ctrl %p host %q, want %p/%q",
			tab.Ctrl, tab.SharedHostKey, targetCtrl, hostKey)
	}
	if refs, ok := sharedHostRefsForTest(t, app, hostKey); !ok || refs != 1 {
		t.Fatalf("shared host refs after reattach = %d, present=%v, want 1/true", refs, ok)
	}
}

func newAtomicRebindTestApp(t *testing.T) (*App, *WorkspaceTab, control.SessionAPI, string, string, *agent.Session) {
	t.Helper()
	isolateDesktopUserDirs(t)
	root := globalTabWorkspaceRoot()
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sourcePath := filepath.Join(dir, "atomic-source.jsonl")
	targetPath := filepath.Join(dir, "atomic-target.jsonl")
	writeHistoryTestSession(t, sourcePath, "source prompt")
	writeHistoryTestSession(t, targetPath, "target prompt")
	if err := agent.SaveBranchMetaPreserveUpdated(targetPath, agent.BranchMeta{
		TokenMode:        boot.TokenModeDelivery,
		Mode:             "normal",
		ToolApprovalMode: control.ToolApprovalAsk,
	}); err != nil {
		t.Fatalf("save target profile: %v", err)
	}
	loaded, err := agent.LoadSession(targetPath)
	if err != nil {
		t.Fatalf("load target: %v", err)
	}
	sourceSession, err := agent.LoadSession(sourcePath)
	if err != nil {
		t.Fatalf("load source: %v", err)
	}
	exec := agent.New(nil, nil, sourceSession, agent.Options{}, event.Discard)
	oldCtrl := control.New(control.Options{
		Executor: exec, SessionDir: dir, SessionPath: sourcePath, Label: "source", Sink: event.Discard,
	})
	oldCtrl.Resume(sourceSession, sourcePath)
	oldCtrl.SetPlanMode(true)
	oldCtrl.SetToolApprovalMode(control.ToolApprovalYolo)

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	tab := &WorkspaceTab{
		ID:               "atomic-rebind",
		Scope:            "global",
		WorkspaceRoot:    root,
		SessionPath:      sourcePath,
		Ctrl:             oldCtrl,
		Ready:            true,
		model:            "",
		mode:             "plan-yolo",
		toolApprovalMode: control.ToolApprovalYolo,
		sink:             &tabEventSink{tabID: "atomic-rebind", app: app, ctx: app.ctx},
		disabledMCP:      map[string]ServerView{},
	}
	app.tabs[tab.ID] = tab
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	if err := tab.ensureSessionLease(sourcePath); err != nil {
		t.Fatalf("lease source: %v", err)
	}
	app.mu.Lock()
	app.newSessionRuntimeLocked(tab, sessionRuntimeKey(sourcePath))
	app.advanceSessionRuntimeEpochLocked(tab)
	app.mu.Unlock()
	t.Cleanup(func() {
		if ctrl := app.controllerForTab(tab); ctrl != nil {
			ctrl.Close()
		}
		tab.releaseSessionLease()
	})
	return app, tab, oldCtrl, sourcePath, targetPath, loaded
}

func assertAtomicRebindFailurePreservedSource(
	t *testing.T,
	app *App,
	tab *WorkspaceTab,
	oldCtrl control.SessionAPI,
	sourcePath string,
	targetPath string,
	oldEpoch string,
) {
	t.Helper()
	if got := app.controllerForTab(tab); got != oldCtrl {
		t.Fatalf("controller after failed rebind = %p, want source %p", got, oldCtrl)
	}
	if got := tab.currentSessionPath(); sessionRuntimeKey(got) != sessionRuntimeKey(sourcePath) {
		t.Fatalf("session path after failed rebind = %q, want %q", got, sourcePath)
	}
	if got := tab.sessionLeaseRuntimeKey(); got != sessionRuntimeKey(sourcePath) {
		t.Fatalf("lease after failed rebind = %q, want source key %q", got, sessionRuntimeKey(sourcePath))
	}
	if !oldCtrl.PlanMode() || oldCtrl.ToolApprovalMode() != control.ToolApprovalYolo ||
		currentTabTokenMode(tab) != boot.TokenModeFull {
		t.Fatalf("source profile changed after failed rebind: plan=%v approval=%q token=%q",
			oldCtrl.PlanMode(), oldCtrl.ToolApprovalMode(), currentTabTokenMode(tab))
	}
	app.mu.RLock()
	view := app.sessionRuntimeViewLocked(tab)
	sourceRuntime := app.runtimeBySessionKey[sessionRuntimeKey(sourcePath)]
	targetRuntime := app.runtimeBySessionKey[sessionRuntimeKey(targetPath)]
	app.mu.RUnlock()
	if view.Phase != sessionRuntimeReady || view.Epoch != oldEpoch {
		t.Fatalf("runtime after failed rebind = phase %q epoch %q, want ready/%q", view.Phase, view.Epoch, oldEpoch)
	}
	if sourceRuntime == nil || sourceRuntime.Owner != tab || targetRuntime != nil {
		t.Fatalf("registry after failed rebind source=%#v target=%#v", sourceRuntime, targetRuntime)
	}
	if meta := app.MetaForTab(tab.ID); !meta.Ready || meta.Runtime.Phase != sessionRuntimeReady {
		t.Fatalf("failed rebind disabled source runtime: ready=%v phase=%q", meta.Ready, meta.Runtime.Phase)
	}
}

func TestRebindTargetLeaseFailureKeepsSourceRuntimeAtomic(t *testing.T) {
	app, tab, oldCtrl, sourcePath, targetPath, loaded := newAtomicRebindTestApp(t)
	app.mu.RLock()
	oldEpoch := app.sessionRuntimeViewLocked(tab).Epoch
	app.mu.RUnlock()

	holder, err := agent.TryAcquireSessionLease(targetPath)
	if err != nil {
		t.Fatalf("hold target lease: %v", err)
	}
	defer holder.Release()

	err = app.rebindTabToLoadedSessionPath(tab, targetPath, loaded)
	if !errors.Is(err, agent.ErrSessionLeaseHeld) {
		t.Fatalf("rebind error = %v, want ErrSessionLeaseHeld", err)
	}
	assertAtomicRebindFailurePreservedSource(t, app, tab, oldCtrl, sourcePath, targetPath, oldEpoch)
	if _, err := agent.TryAcquireSessionLease(sourcePath); !errors.Is(err, agent.ErrSessionLeaseHeld) {
		t.Fatalf("source lease became acquirable after failed target claim: %v", err)
	}
}

func TestRebindPostLeaseValidationFailureRollsBackCandidate(t *testing.T) {
	app, tab, oldCtrl, sourcePath, targetPath, loaded := newAtomicRebindTestApp(t)
	app.mu.RLock()
	oldEpoch := app.sessionRuntimeViewLocked(tab).Epoch
	app.mu.RUnlock()
	app.rebindCandidateHook = func(stage string) error {
		if stage == "lease_acquired" {
			return errors.New("injected post-lease validation failure")
		}
		return nil
	}

	err := app.rebindTabToLoadedSessionPath(tab, targetPath, loaded)
	if err == nil || !strings.Contains(err.Error(), "injected post-lease") {
		t.Fatalf("rebind error = %v, want injected validation failure", err)
	}
	assertAtomicRebindFailurePreservedSource(t, app, tab, oldCtrl, sourcePath, targetPath, oldEpoch)
	targetLease, err := agent.TryAcquireSessionLease(targetPath)
	if err != nil {
		t.Fatalf("candidate target lease leaked after rollback: %v", err)
	}
	targetLease.Release()
}

func TestCloseTabPersistsSessionProfileBeforeRemovingVisibleTab(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := globalTabWorkspaceRoot()
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	currentPath := filepath.Join(dir, "profile.jsonl")
	otherPath := filepath.Join(dir, "other.jsonl")
	writeHistoryTestSession(t, currentPath, "profile prompt")
	writeHistoryTestSession(t, otherPath, "other prompt")
	if err := agent.SaveBranchMetaPreserveUpdated(currentPath, agent.BranchMeta{}); err != nil {
		t.Fatalf("SaveBranchMetaPreserveUpdated current: %v", err)
	}

	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: currentPath, Label: "profile", Sink: event.Discard})
	ctrl.SetMode(true, false)
	ctrl.SetToolApprovalMode(control.ToolApprovalAuto)
	ctrl.SetGoal("finish the review")

	app := NewApp()
	tab := &WorkspaceTab{
		ID:               "profile",
		Scope:            "global",
		WorkspaceRoot:    root,
		SessionPath:      currentPath,
		Ctrl:             ctrl,
		Ready:            true,
		mode:             "plan",
		toolApprovalMode: control.ToolApprovalAuto,
		sink:             &tabEventSink{tabID: "profile", app: app},
		disabledMCP:      map[string]ServerView{},
	}
	other := &WorkspaceTab{
		ID:            "other",
		Scope:         "global",
		WorkspaceRoot: root,
		SessionPath:   otherPath,
		Ready:         true,
		disabledMCP:   map[string]ServerView{},
	}
	app.tabs[tab.ID] = tab
	app.tabs[other.ID] = other
	app.tabOrder = []string{tab.ID, other.ID}
	app.activeTabID = tab.ID

	if err := app.CloseTab(tab.ID); err != nil {
		t.Fatalf("CloseTab: %v", err)
	}

	meta, ok, err := agent.LoadBranchMeta(currentPath)
	if err != nil || !ok {
		t.Fatalf("LoadBranchMeta current ok=%v err=%v", ok, err)
	}
	if meta.TokenMode != boot.TokenModeFull || meta.AgentPreset != "" ||
		meta.Mode != "plan" || meta.ToolApprovalMode != control.ToolApprovalAuto || meta.Goal != "finish the review" {
		t.Fatalf("closed session profile = token:%q preset:%q mode:%q approval:%q goal:%q, want full/empty/plan/auto/goal",
			meta.TokenMode, meta.AgentPreset, meta.Mode, meta.ToolApprovalMode, meta.Goal)
	}
}

func TestKeepOnlyVisibleTabPersistsRemovedSessionProfile(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := globalTabWorkspaceRoot()
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	keepPath := filepath.Join(dir, "keep.jsonl")
	removedPath := filepath.Join(dir, "removed.jsonl")
	writeHistoryTestSession(t, keepPath, "keep prompt")
	writeHistoryTestSession(t, removedPath, "removed prompt")
	if err := agent.SaveBranchMetaPreserveUpdated(removedPath, agent.BranchMeta{}); err != nil {
		t.Fatalf("SaveBranchMetaPreserveUpdated removed: %v", err)
	}

	removedCtrl := control.New(control.Options{SessionDir: dir, SessionPath: removedPath, Label: "removed", Sink: event.Discard})
	removedCtrl.SetMode(true, false)
	removedCtrl.SetToolApprovalMode(control.ToolApprovalAuto)
	removedCtrl.SetGoal("keep this profile")

	app := NewApp()
	keep := &WorkspaceTab{
		ID:            "keep",
		Scope:         "global",
		WorkspaceRoot: root,
		SessionPath:   keepPath,
		Ready:         true,
		disabledMCP:   map[string]ServerView{},
	}
	removed := &WorkspaceTab{
		ID:               "removed",
		Scope:            "global",
		WorkspaceRoot:    root,
		SessionPath:      removedPath,
		Ctrl:             removedCtrl,
		Ready:            true,
		mode:             "plan",
		toolApprovalMode: control.ToolApprovalAuto,
		sink:             &tabEventSink{tabID: "removed", app: app},
		disabledMCP:      map[string]ServerView{},
	}
	app.tabs[keep.ID] = keep
	app.tabs[removed.ID] = removed
	app.tabOrder = []string{keep.ID, removed.ID}
	app.activeTabID = removed.ID

	if _, err := app.keepOnlyVisibleTab(keep.ID); err != nil {
		t.Fatalf("keepOnlyVisibleTab: %v", err)
	}

	meta, ok, err := agent.LoadBranchMeta(removedPath)
	if err != nil || !ok {
		t.Fatalf("LoadBranchMeta removed ok=%v err=%v", ok, err)
	}
	if meta.TokenMode != boot.TokenModeFull || meta.AgentPreset != "" ||
		meta.Mode != "plan" || meta.ToolApprovalMode != control.ToolApprovalAuto || meta.Goal != "keep this profile" {
		t.Fatalf("removed session profile = token:%q preset:%q mode:%q approval:%q goal:%q, want full/empty/plan/auto/goal",
			meta.TokenMode, meta.AgentPreset, meta.Mode, meta.ToolApprovalMode, meta.Goal)
	}
}

func TestLoadTabSessionProfileIgnoresTerminalGoalState(t *testing.T) {
	isolateDesktopUserDirs(t)
	sessionPath := filepath.Join(desktopSessionDir(globalTabWorkspaceRoot()), "terminal-goal.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeHistoryTestSession(t, sessionPath, "terminal prompt")
	if err := agent.SaveBranchMetaPreserveUpdated(sessionPath, agent.BranchMeta{
		TokenMode: "economy", Mode: "plan", ToolApprovalMode: control.ToolApprovalAuto, Goal: "stale terminal goal",
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.SessionGoalState(sessionPath), []byte(`{"goal":"stale terminal goal","status":"complete"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := loadTabSessionProfile(sessionPath)
	if profile.goal != "" || profile.tokenMode != "full" || profile.qualityFloor != control.QualityFloorStandard || profile.mode != "plan" || profile.toolApprovalMode != control.ToolApprovalAuto {
		t.Fatalf("profile=%+v", profile)
	}
	tab := &WorkspaceTab{}
	applyTabSessionProfile(tab, profile)
	if currentTabTokenMode(tab) != boot.TokenModeFull {
		t.Fatal("legacy economy must fold to standard")
	}
}

func TestLoadTabSessionProfileMissingApprovalDefaultsAsk(t *testing.T) {
	isolateDesktopUserDirs(t)
	sessionPath := filepath.Join(desktopSessionDir(globalTabWorkspaceRoot()), "legacy-missing-approval.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeHistoryTestSession(t, sessionPath, "legacy prompt")
	if err := agent.SaveBranchMetaPreserveUpdated(sessionPath, agent.BranchMeta{Mode: "normal"}); err != nil {
		t.Fatal(err)
	}
	if profile := loadTabSessionProfile(sessionPath); profile.toolApprovalMode != control.ToolApprovalAsk {
		t.Fatalf("legacy missing tool approval mode = %q, want ask", profile.toolApprovalMode)
	}
}

func writeHistoryTestSession(t *testing.T, path, prompt string) {
	t.Helper()
	session := agent.NewSession("")
	session.Add(provider.Message{Role: provider.RoleUser, Content: prompt})
	if err := session.Save(path); err != nil {
		t.Fatalf("Save %s: %v", path, err)
	}
}
