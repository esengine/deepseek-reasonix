// Run: tsx src/__tests__/use-controller-meta.test.ts

import { currentTurnWaitMs, foregroundRunningFromRuntimeMeta, historyMessagesToItems, initialState, localizedBackendNoticeText, localizedNoticeText, metaFromTab, reducer, sameMeta, type Item } from "../lib/useController";
import { effortSwitchNoticeText, modelSwitchNoticeText } from "../lib/controllerSwitchNotices";
import { historyPageRequestBudget, historyTurnsToLoad } from "../lib/historyPaging";
import { shouldReconcileStaleTurn } from "../lib/useStaleTurnWatchdog";
import { parseTodos } from "../lib/tools";
import { resolveTodoPanelTodos } from "../lib/todoVisibility";
import type { HistoryMessage, Meta, TabMeta, WireUsage } from "../lib/types";

type LooseTabMeta = Omit<TabMeta, "toolApprovalMode"> & { toolApprovalMode?: TabMeta["toolApprovalMode"] | "" };

let passed = 0;
let failed = 0;

function eq(a: unknown, b: unknown, label: string) {
  if (a === b) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}: expected ${JSON.stringify(b)}, got ${JSON.stringify(a)}\n`);
    failed += 1;
  }
}

function ok(value: boolean, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

function meta(overrides: Partial<Meta> = {}): Meta {
  return {
    label: "DeepSeek-R1",
    ready: true,
    eventChannel: "events",
    cwd: "/repo",
    workspaceRoot: "/repo",
    workspaceName: "repo",
    workspacePath: "/repo",
    gitBranch: "main",
    imageInputEnabled: true,
    autoApproveTools: false,
    bypass: false,
    collaborationMode: "normal",
    toolApprovalMode: "ask",
    tokenMode: "full",
    goal: "",
    goalStatus: "stopped",
    ...overrides,
  };
}

function tab(overrides: Partial<LooseTabMeta> = {}): TabMeta {
  return {
    id: "tab-1",
    scope: "project",
    workspaceRoot: "/repo",
    workspaceName: "repo",
    workspacePath: "/repo",
    gitBranch: "main",
    topicId: "topic-1",
    topicTitle: "Topic",
    label: "DeepSeek-R1",
    ready: true,
    running: false,
    mode: "normal",
    collaborationMode: "normal",
    toolApprovalMode: "ask",
    tokenMode: "full",
    goal: "",
    goalStatus: "stopped",
    active: true,
    cwd: "/repo",
    ...overrides,
  } as TabMeta;
}

function usage(source: string): WireUsage {
  return {
    promptTokens: 100,
    completionTokens: 20,
    totalTokens: 120,
    cacheHitTokens: 80,
    cacheMissTokens: 20,
    sessionCacheHitTokens: 80,
    sessionCacheMissTokens: 20,
    source,
    cost: 0.001,
    currency: "$",
  };
}

console.log("\nuse controller meta");

{
  eq(
    modelSwitchNoticeText("active work is still running; running=false; pending_prompt=false; background_jobs=2; finish or cancel the current turn, answer pending prompts, and stop background jobs before changing model"),
    "The model cannot change while background work is active. Active jobs: 2. Open Background jobs in the status bar to stop them.",
    "model busy guard names the background-job blocker",
  );
  eq(
    effortSwitchNoticeText("active work is still running; running=true; pending_prompt=false; background_jobs=0; finish or cancel the current turn, answer pending prompts, and stop background jobs before changing effort"),
    "Reasoning effort cannot change while the current answer is running. Stop it first.",
    "effort busy guard names the running-answer blocker",
  );
  eq(
    modelSwitchNoticeText("finish or cancel the current turn, answer pending prompts, and stop background jobs before changing model"),
    "The model cannot change yet. Stop the current answer, handle pending prompts, or wait for background jobs to finish.",
    "model busy guard is localized",
  );
  eq(
    modelSwitchNoticeText("this session is already open in another Reasonix window or still running in the background; close the other window or open a copy before changing model"),
    "This session is open in another Reasonix window or still running in the background. Close that window, stop the background run, or open a copy before changing models.",
    "model lease conflict explains the safe path",
  );
  eq(
    modelSwitchNoticeText("workspace is still starting"),
    "This session is still starting. Try changing models again in a moment.",
    "model startup race asks the user to retry later",
  );
  eq(
    modelSwitchNoticeText('tab "tab-a" changed while switching model; retry'),
    "The current session changed while switching models. Try once more.",
    "model tab race asks the user to retry",
  );
  eq(
    modelSwitchNoticeText('unknown model "missing"'),
    'Unknown model "missing".',
    "model unknown error is localized",
  );
  eq(
    modelSwitchNoticeText('model "other/other-model" is not available because provider "other" is not added'),
    'Model "other/other-model" is unavailable because provider "other" is not added.',
    "model provider access error is localized",
  );
}

{
  eq(
    effortSwitchNoticeText("finish or cancel the current turn, answer pending prompts, and stop background jobs before changing effort"),
    "Reasoning effort cannot change yet. Stop the current answer, handle pending prompts, or wait for background jobs to finish.",
    "effort busy guard is worded as temporary",
  );
  eq(
    effortSwitchNoticeText("this session is already open in another Reasonix window or still running in the background; close the other window or open a copy before changing effort"),
    "This session is open in another Reasonix window or still running in the background. Close that window, stop the background run, or open a copy before changing effort.",
    "effort lease conflict explains the safe path",
  );
  eq(
    effortSwitchNoticeText("workspace is still starting"),
    "This session is still starting. Try changing reasoning effort again in a moment.",
    "effort startup race asks the user to retry later",
  );
  eq(
    effortSwitchNoticeText('tab "tab-a" changed while switching effort; retry'),
    "The current session changed while switching reasoning effort. Try once more.",
    "effort tab race asks the user to retry",
  );
  eq(
    effortSwitchNoticeText("unknown model \"missing\""),
    "Reasoning effort switch failed: unknown model \"missing\"",
    "effort true failure keeps the underlying error",
  );
}

{
  eq(
    localizedBackendNoticeText("Session autosave failed: disk full"),
    "Session autosave failed: disk full",
    "backend autosave notice is localized through the active dictionary",
  );
  eq(
    localizedBackendNoticeText("Session save failed before changing model: disk full"),
    "Session save failed before changing models: disk full",
    "backend save-before-action notice localizes the action",
  );
  eq(
    localizedBackendNoticeText('model "old/model" is no longer available; switched to new/model'),
    'Model "old/model" is no longer available; switched to new/model.',
    "backend model fallback notice is localized",
  );
  eq(
    localizedBackendNoticeText("session changed on disk; unsaved local transcript was saved as recovery branch 20260706-152144.863947300-longcat-openai-LongCat-2.0-119b7259f151-recovery-693ce51bcbcbaa9"),
    "The session changed on disk, so the unsaved local transcript was kept as another saved version.",
    "legacy recovery branch notice can be normalized without exposing internal branch id",
  );
  eq(
    localizedBackendNoticeText("session changed on disk; unsaved local transcript was saved as a conflict copy"),
    "The session changed on disk, so the unsaved local transcript was kept as another saved version.",
    "recovery copy notice can be normalized",
  );
  eq(
    localizedBackendNoticeText("session conflicts kept recurring; kept the transcript on the current recovery branch"),
    "Repeated save conflicts were detected, so the current version was saved separately.",
    "legacy repeated recovery conflict notice can be normalized",
  );
  eq(
    localizedBackendNoticeText("repeated save conflicts were detected; saved the current conflict copy in place"),
    "Repeated save conflicts were detected, so the current version was saved separately.",
    "repeated recovery conflict notice can be normalized",
  );
  eq(
    localizedBackendNoticeText("session changed on disk; adopted the newer transcript"),
    "The session changed on disk, so Reasonix adopted the newer transcript.",
    "adopted transcript notice can be normalized",
  );
  eq(
    localizedBackendNoticeText("session changed on disk; adopted the newer transcript (local changes already covered)"),
    "The session changed on disk, so Reasonix adopted the newer transcript; the local changes were already covered.",
    "covered adopted transcript notice can be normalized",
  );
  eq(
    localizedBackendNoticeText("The assistant answered before taking action; asking it to use the required tools."),
    "The assistant answered before taking action; asking it to use the required tools.",
    "canonical backend notice is routed through the locale dictionary",
  );
  eq(
    localizedBackendNoticeText("background export failed: needs attention"),
    "Background export needs attention.",
    "dynamic background job notice is user-facing",
  );
}

{
  eq(
    localizedNoticeText("Task status needs one more check (reworded backend copy).", "final_readiness"),
    "Task status needs one more check; asking the assistant to finish or explain what is blocking it.",
    "a stable notice code localizes the main copy even after backend copy edits",
  );
  eq(
    localizedNoticeText("reworded workspace contention copy", "workspace_lease"),
    "Another session is writing to this workspace; this session will continue automatically when it is safe.",
    "workspace lease contention uses its stable localized notice code",
  );
  eq(
    localizedNoticeText("reworded cancelled-turn copy", "cancelled_turn_display"),
    "This turn was interrupted. Partial output is kept for reference; only completed tool pairs and a bounded recovery summary enter the next model turn. Inspect the workspace before continuing or reverting changes.",
    "cancelled turn history explains the model-context boundary",
  );
  eq(
    localizedNoticeText("reworded unapplied copy\nuse plan B", "unapplied_steer"),
    "Guidance was not applied because the turn ended before it could be processed. Send it again if it is still needed:\nuse plan B",
    "unapplied steer keeps the user's guidance while localizing the warning",
  );
  eq(
    localizedNoticeText("reworded recovery copy", "session_recovery_forked"),
    "The session changed on disk, so the unsaved local transcript was kept as another saved version.",
    "session recovery fork localization uses its stable notice code",
  );
  eq(
    localizedNoticeText("reworded covered adoption", "session_recovery_adopted_covered"),
    "The session changed on disk, so Reasonix adopted the newer transcript; the local changes were already covered.",
    "covered session adoption localization uses its stable notice code",
  );
  eq(
    localizedNoticeText("reworded depth cap", "session_recovery_depth_cap"),
    "Repeated save conflicts were detected, so the current version was saved separately.",
    "session recovery depth-cap localization uses its stable notice code",
  );
  eq(
    localizedNoticeText("Tool round limit reached; asking the assistant to summarize progress.", "unknown_future_code"),
    "Tool round limit reached; asking the assistant to summarize progress.",
    "an unknown notice code falls back to exact-text matching",
  );
  eq(
    localizedNoticeText("some free-form backend message"),
    "some free-form backend message",
    "a codeless unmatched notice keeps its raw text",
  );
}

{
  eq(historyTurnsToLoad(941, 1_000, 1), 500, "a distant question jump uses the bounded 500-turn history window");
  eq(historyTurnsToLoad(441, 1_000, 1), 440, "the follow-up jump page reaches the requested turn without overfetching");
  eq(historyTurnsToLoad(2, 61), 60, "ordinary automatic history loading keeps the standard page size");
  eq(JSON.stringify(historyPageRequestBudget(941, 1_000, 1)), JSON.stringify({ turns: 500, entries: 1000 }), "a distant jump uses the backend's bounded entry capacity");
  eq(JSON.stringify(historyPageRequestBudget(2, 61)), JSON.stringify({ turns: 60 }), "ordinary history loading keeps the default entry and byte budgets");

  let s = reducer(initialState, {
    type: "event",
    e: { kind: "notice", level: "warn", code: "session_recovery_depth_cap", text: "reworded recovery maintenance" },
  });
  s = reducer(s, {
    type: "event",
    e: { kind: "notice", level: "warn", text: "repeated save conflicts were detected; saved the current conflict copy in place" },
  });
  const recoveryNotices = s.items.filter((item) => item.kind === "notice" && item.text.includes("current conflict copy"));
  eq(recoveryNotices.length, 0, "recovery conflict notices stay silent in the live transcript");
  eq(s.seq, 0, "silent recovery notices do not consume sequence ids");

  s = reducer(s, { type: "event", e: { kind: "notice", level: "warn", text: "runtime notice" } });
  s = reducer(s, { type: "event", e: { kind: "notice", level: "warn", text: "runtime notice" } });
  const ordinaryNotices = s.items.filter((item) => item.kind === "notice" && item.text === "runtime notice");
  eq(ordinaryNotices.length, 2, "ordinary repeated notices remain visible");
}

{
  const quietLifecycleMessages = [
    { level: "info", text: "guardian enabled · model=guardian-test" },
    { level: "warn", text: "2 MCP server(s) failed to start: fs, browser — run /mcp for details" },
    { level: "warn", text: "mcp fs: stdio plugin \"fs\": command \"missing-fs\" not found on PATH" },
    { level: "info", text: "settings applied: session refreshed after the lease was released" },
    { level: "info", text: "plugin \"slowserver\" has been slow 3 startups in a row (last 30000ms, budget 1000ms); demoting to background startup this session" },
  ] as const;
  let s = initialState;
  for (const message of quietLifecycleMessages) {
    s = reducer(s, { type: "event", e: { kind: "notice", level: message.level, text: message.text } });
  }
  eq(s.items.filter((item) => item.kind === "notice").length, 0, "background lifecycle notices stay silent in the live transcript");
  eq(s.seq, 0, "silent lifecycle notices do not consume sequence ids");

  const userActionFailure = reducer(s, { type: "event", e: { kind: "notice", level: "warn", text: "mcp connect: no configured MCP server named \"fs\"" } });
  const visibleNotices = userActionFailure.items.filter((item) => item.kind === "notice");
  eq(visibleNotices.length, 1, "user-triggered MCP failures remain visible");
  eq(visibleNotices[0]?.kind === "notice" && visibleNotices[0].text, "mcp connect: no configured MCP server named \"fs\"", "visible MCP failure keeps its text");
}

{
  const history: HistoryMessage[] = [
    { role: "notice", level: "warn", content: "session conflicts kept recurring; kept the transcript on the current recovery branch" },
    { role: "notice", level: "warn", content: "repeated save conflicts were detected; saved the current conflict copy in place" },
    { role: "notice", level: "info", content: "guardian enabled · model=guardian-test" },
    { role: "notice", level: "warn", content: "1 MCP server(s) failed to start: fs — run /mcp for details" },
    { role: "notice", level: "info", content: "settings applied: session refreshed after the lease was released" },
    { role: "user", content: "continue" },
  ];
  const hydrated = historyMessagesToItems(history, "h");
  const recoveryNotices = hydrated.items.filter((item) => item.kind === "notice" && item.text.includes("current conflict copy"));
  const lifecycleNotices = hydrated.items.filter((item) => item.kind === "notice");
  const users = hydrated.items.filter((item) => item.kind === "user");
  eq(recoveryNotices.length, 0, "recovery conflict notices stay silent when hydrating history");
  eq(lifecycleNotices.length, 0, "background lifecycle notices stay silent when hydrating history");
  eq(users[0]?.kind === "user" && users[0].id, "h0", "silent history notices keep later item ids compact");
  eq(hydrated.seq, 1, "silent history notices do not inflate the hydrated sequence");
}

{
  const hydrated = historyMessagesToItems([{ role: "notice", level: "warn", content: "short notice", detail: "historical diagnostic" }], "h");
  const notice = hydrated.items.find((item) => item.kind === "notice" && item.text === "short notice");
  eq(notice?.kind === "notice" && notice.detail, "historical diagnostic", "history notices preserve expandable detail text");
}

{
  const hydrated = historyMessagesToItems([{ role: "notice", level: "info", content: "Tool round limit reached (reworded backend copy).", code: "tool_budget" }], "h");
  const notice = hydrated.items.find((item) => item.kind === "notice");
  eq(
    notice?.kind === "notice" && notice.text,
    "Tool round limit reached; asking the assistant to summarize progress.",
    "history notices localize by stable code when the replayed record carries one",
  );
}

{
  const hydrated = historyMessagesToItems([
    { role: "user", content: "finish" },
    { role: "assistant", content: "done", reasoning: "worked", workDurationMs: 24_000 },
  ], "h");
  const assistant = hydrated.items.find((item) => item.kind === "assistant");
  eq(assistant?.kind === "assistant" && assistant.workDurationMs, 24_000, "history restores persisted turn work duration");
}

{
  eq(sameMeta(meta(), meta()), true, "identical meta is unchanged");
  eq(sameMeta(meta({ sessionGeneration: 1 }), meta({ sessionGeneration: 1 })), true, "identical sessionGeneration is unchanged");
eq(sameMeta(meta({ sessionGeneration: 1 }), meta({ sessionGeneration: 2 })), false, "sessionGeneration changes invalidate meta equality");
eq(sameMeta(meta({ collaborationMode: "normal" }), meta({ collaborationMode: "plan" })), false, "collaboration mode changes invalidate meta equality");
  eq(sameMeta(meta({ workspacePath: "/repo" }), meta({ workspacePath: "/other" })), false, "workspace path changes invalidate meta equality");
  eq(sameMeta(meta({ gitBranch: "main" }), meta({ gitBranch: "feature" })), false, "git branch changes invalidate meta equality");
  eq(sameMeta(meta({ imageInputEnabled: true }), meta({ imageInputEnabled: false })), false, "image input capability changes invalidate meta equality");
  eq(sameMeta(meta({ visionFallbackEnabled: true }), meta({ visionFallbackEnabled: false })), false, "image-understanding fallback changes invalidate meta equality");
  eq(
    sameMeta(
      meta({ canonicalTodos: [{ content: "Ship", status: "in_progress" }] }),
      meta({ canonicalTodos: [{ content: "Ship", status: "completed" }] }),
    ),
    false,
    "canonical todo progress invalidates meta equality",
  );
  eq(
    sameMeta(meta({ canonicalTodos: [] }), meta({ canonicalTodos: [] })),
    true,
    "equivalent empty canonical todo lists keep meta stable",
  );
  eq(
    sameMeta(meta({ dismissedTodoBatches: ["a"] }), meta({ dismissedTodoBatches: ["a"] })),
    true,
    "identical dismissed todo batches keep meta stable",
  );
  eq(
    sameMeta(meta({ dismissedTodoBatches: ["a"] }), meta({ dismissedTodoBatches: ["b"] })),
    false,
    "persisted todo dismissal changes invalidate meta equality",
  );
}

{
  const preserved = metaFromTab(tab({ toolApprovalMode: "" }), meta({ toolApprovalMode: "auto", autoApproveTools: false }));
  eq(preserved.toolApprovalMode, "auto", "blank tab snapshot preserves explicit auto approval mode");
  eq(preserved.autoApproveTools, false, "blank tab snapshot does not silently resurrect yolo approval");
  const todos = [{ content: "Keep task state", status: "in_progress" }];
  const withTodos = metaFromTab(tab(), meta({ canonicalTodos: todos }));
  eq(withTodos.canonicalTodos, todos, "optimistic tab metadata preserves canonical todos for the same session");
  const dismissed = metaFromTab(tab({ sessionPath: "/s/a.jsonl" }), meta({ sessionPath: "/s/a.jsonl", dismissedTodoBatches: ["done"] }));
  eq(dismissed.dismissedTodoBatches?.[0], "done", "optimistic metadata keeps session-sidecar todo dismissals");
  const remounted = metaFromTab(tab({ sessionPath: "/s/leaf.jsonl" }), meta({ sessionPath: "/s/a.jsonl", dismissedTodoBatches: ["done"] }));
  eq(remounted.dismissedTodoBatches, undefined, "a session remount waits for the new sidecar dismissals");
}

{
  const before = meta({ canonicalTodos: [{ content: "Ship", status: "in_progress" }] });
  const completed = meta({ canonicalTodos: [{ content: "Ship", status: "completed" }] });
  const updated = reducer({ ...initialState, meta: before }, { type: "meta", meta: completed });
  eq(updated.meta?.canonicalTodos?.[0]?.status, "completed", "meta refresh applies canonical todo progress");

  const withDismissed = reducer(updated, { type: "meta", meta: meta({ canonicalTodos: completed.canonicalTodos, dismissedTodoBatches: ["done"] }) });
  const reset = reducer(withDismissed, { type: "reset" });
  eq(reset.meta?.canonicalTodos, undefined, "session reset clears canonical todos from the previous session");
  eq(reset.meta?.dismissedTodoBatches, undefined, "session reset clears persisted todo dismissals from the previous session");

  const cleared = reducer(reset, { type: "meta", meta: meta({ canonicalTodos: [] }) });
  eq(cleared.meta?.canonicalTodos?.length, 0, "authoritative empty canonical todos survive meta refresh");
}

{
  const delayedLiveMeta = meta({
    canonicalTodos: [
      { content: "Inspect the report", status: "completed" },
      { content: "Ship the fix", status: "in_progress" },
    ],
  });
  const hydrated = reducer({ ...initialState, meta: delayedLiveMeta }, { type: "meta", meta: delayedLiveMeta });
  const noLiveTodo = hydrated.items.find(
    (item): item is Extract<Item, { kind: "tool" }> => item.kind === "tool" && item.name === "todo_write",
  );
  eq(
    resolveTodoPanelTodos(hydrated.meta?.canonicalTodos, noLiveTodo ? parseTodos(noLiveTodo.args) : undefined),
    delayedLiveMeta.canonicalTodos,
    "panel uses fresh Meta todos while the live todo_write event is delayed",
  );

  const staleMeta = meta({
    canonicalTodos: [
      { content: "Inspect the report", status: "in_progress" },
      { content: "Ship the fix", status: "completed" },
    ],
  });
  const rawLiveArgs = JSON.stringify({
    todos: [
      { content: "Inspect the report", status: "completed" },
      { content: "Ship the fix", status: "completed" },
    ],
  });
  const canonicalLiveArgs = JSON.stringify({
    todos: [
      { content: "Inspect the report", status: "in_progress" },
      { content: "Ship the fix", status: "pending" },
    ],
  });
  let liveState = reducer({ ...initialState, meta: staleMeta }, { type: "event", e: { kind: "turn_started" } });
  liveState = reducer(liveState, {
    type: "event",
    e: { kind: "tool_dispatch", tool: { id: "todo-live", name: "todo_write", args: rawLiveArgs, readOnly: true } },
  });
  liveState = reducer(liveState, {
    type: "event",
    e: { kind: "tool_result_preview", tool: { id: "todo-live", name: "todo_write", args: canonicalLiveArgs, readOnly: true, output: "Todos updated" } },
  });
  const liveTodo = liveState.items.find(
    (item): item is Extract<Item, { kind: "tool" }> => item.kind === "tool" && item.name === "todo_write",
  );
  eq(
    JSON.stringify(resolveTodoPanelTodos(liveState.meta?.canonicalTodos, liveTodo ? parseTodos(liveTodo.args) : undefined)),
    JSON.stringify(JSON.parse(canonicalLiveArgs).todos),
    "panel uses canonical todo args when the repaired result preview arrives",
  );
  liveState = reducer(liveState, {
    type: "event",
    e: { kind: "tool_result", tool: { id: "todo-live", name: "todo_write", args: canonicalLiveArgs, readOnly: true, output: "Todos updated", durationMs: 4 } },
  });
  eq(
    liveState.items.filter((item) => item.kind === "tool" && item.id === "todo-live").length,
    1,
    "provider-ordered terminal result upserts the preview instead of duplicating the card",
  );
}

{
  const started = reducer(initialState, { type: "event", e: { kind: "turn_started" } });
  const rendered = reducer(started, { type: "event", e: { kind: "message", text: "done", reasoning: "" } });
  eq(rendered.running, true, "message without turn_done leaves local runtime marked running");
  eq(rendered.turnActive, true, "message without turn_done still belongs to an active turn");
  eq(rendered.live, undefined, "final message closes the live stream before turn_done");
  eq(shouldReconcileStaleTurn(rendered, 1_000, 31_000), true, "stale completed stream still reconciles missed turn_done");
  eq(shouldReconcileStaleTurn(rendered, 1_000, 20_000), false, "fresh completed stream waits before reconciling");
  const optimistic = reducer(initialState, { type: "user", text: "hello", seq: 0, submissionId: "watchdog-submit" });
  eq(optimistic.turnActive, false, "optimistic send starts before turn_started arrives");
  eq(shouldReconcileStaleTurn(optimistic, 0, optimistic.turnStartAt + 30_000), true, "optimistic send reconciles even when turn_started is missed");
}

{
  const originalNow = Date.now;
  let now = 1_000;
  Date.now = () => now;
  try {
    let s = reducer(initialState, { type: "user", text: "keep timing", seq: 0, submissionId: "timing-submit" });
    now = 8_000;
    s = reducer(s, {
      type: "event",
      e: { kind: "turn_started", submissionId: "timing-submit", turnStartedAt: 1_200 },
    });
    eq(s.turnStartAt, 1_200, "a delayed turn_started event keeps the backend turn start instead of restarting the timer");

    now = 12_000;
    s = reducer(initialState, {
      type: "backend_status",
      running: true,
      cancellable: true,
      turnStartedAt: 1_200,
    });
    eq(s.turnStartAt, 1_200, "a rehydrated running tab restores the backend turn start instead of restarting the timer");

    now = 13_000;
    s = reducer(s, { type: "event", e: { kind: "turn_done" } });
    now = 20_000;
    s = reducer(s, { type: "event", e: { kind: "turn_started" } });
    eq(s.turnStartAt, 20_000, "a legacy turn_started event begins a new remote turn instead of reusing completed-turn timing");
  } finally {
    Date.now = originalNow;
  }
}

{
  const originalNow = Date.now;
  let now = 1_000;
  Date.now = () => now;
  try {
    let s = reducer(initialState, { type: "event", e: { kind: "turn_started" } });
    now = 1_200;
    s = reducer(s, { type: "event", e: { kind: "reasoning", reasoning: "plan" } });
    eq(s.live?.reasoningStartedAt, 1_200, "first reasoning delta records a reasoning start time");
    now = 3_700;
    s = reducer(s, { type: "event", e: { kind: "text", text: "answer" } });
    eq(s.live?.reasoningComplete, true, "first answer token marks reasoning complete");
    eq(s.live?.reasoningCompletedAt, 3_700, "first answer token records reasoning completion time");
    now = 4_200;
    s = reducer(s, { type: "event", e: { kind: "turn_done" } });
    const assistant = s.items.find((item) => item.kind === "assistant");
    eq(assistant?.kind === "assistant" && assistant.reasoningDurationMs, 2_500, "turn_done persists the live reasoning duration");
    eq(assistant?.kind === "assistant" && assistant.workDurationMs, 3_200, "turn_done persists the full turn wall-clock duration");
  } finally {
    Date.now = originalNow;
  }
}

{
  const originalNow = Date.now;
  let now = 5_000;
  Date.now = () => now;
  try {
    let s = reducer(initialState, { type: "event", e: { kind: "turn_started" } });
    now = 5_100;
    s = reducer(s, { type: "event", e: { kind: "reasoning", reasoning: "diagnose" } });
    now = 6_400;
    s = reducer(s, { type: "event", e: { kind: "message", text: "done", reasoning: "diagnose" } });
    const assistant = s.items.find((item) => item.kind === "assistant");
    eq(assistant?.kind === "assistant" && assistant.reasoningDurationMs, 1_300, "final message records reasoning duration when no text delta arrived");
    eq(assistant?.kind === "assistant" && assistant.workDurationMs, 1_400, "final message records cumulative turn work duration before turn_done");
    eq(s.live, undefined, "final message still closes live reasoning state");
  } finally {
    Date.now = originalNow;
  }
}

{
  const started = reducer(initialState, { type: "event", e: { kind: "turn_started" } });
  const waiting = reducer(started, { type: "event", e: { kind: "approval_request", approval: { id: "1", tool: "bash", subject: "go test" } } });
  eq(waiting.running, true, "approval prompt keeps the turn running");
  eq(waiting.pendingPrompt, true, "approval prompt marks pendingPrompt");
  eq(waiting.cancellable, true, "approval prompt remains cancellable");
  ok(typeof waiting.promptWaitStartedAt === "number" && waiting.promptWaitStartedAt > 0, "approval_request starts tab-scoped prompt wait");

  const canceling = reducer(waiting, { type: "cancel_requested" });
  eq(canceling.approval, undefined, "cancel_requested clears approval prompt locally");
  eq(canceling.pendingPrompt, false, "cancel_requested clears pendingPrompt locally");
  eq(canceling.cancelRequested, true, "cancel_requested marks cancelling");
  eq(canceling.running, true, "cancel_requested waits for backend turn_done before idling");
  eq(canceling.promptWaitStartedAt, undefined, "cancel_requested closes the open prompt wait");
  ok((canceling.turnWaitAccumMs ?? 0) >= 0, "cancel_requested accumulates closed wait into the turn");
  const stalePrompt = reducer(canceling, { type: "event", e: { kind: "approval_request", approval: { id: "late", tool: "bash", subject: "sleep" } } });
  eq(stalePrompt.approval, undefined, "late approval after cancel_requested stays hidden");

  const backgroundOnly = reducer(initialState, { type: "backend_status", running: false, backgroundJobs: 1, cancellable: false });
  eq(backgroundOnly.running, false, "background jobs alone do not make the composer runstatus active");
  eq(backgroundOnly.backgroundJobs, 1, "backend_status stores background job count");
  eq(backgroundOnly.cancellable, false, "background jobs alone are not foreground-cancellable");

  const omittedCancellableBackgroundOnly = reducer(initialState, { type: "backend_status", running: true, backgroundJobs: 1 });
  eq(omittedCancellableBackgroundOnly.running, false, "missing cancellable does not promote background-only metadata");
  eq(omittedCancellableBackgroundOnly.cancellable, false, "missing cancellable stays non-cancellable with background-only metadata");
  eq(foregroundRunningFromRuntimeMeta({ running: true }), true, "legacy running metadata remains foreground-running");
  eq(foregroundRunningFromRuntimeMeta({ running: true, pendingPrompt: true, backgroundJobs: 1 }), true, "pending prompts remain foreground-running");
  eq(foregroundRunningFromRuntimeMeta({ running: true, backgroundJobs: 1 }), false, "background jobs without cancellable are background-only");
}

// User-wait is tab-scoped: approval_request starts the clock even while the tab
// is not rendered; clearApproval folds the open interval into turnWaitAccumMs.
// workDurationMs excludes that wait so background suspension is not model work.
{
  const originalNow = Date.now;
  let now = 10_000;
  Date.now = () => now;
  try {
    let s = reducer(initialState, { type: "event", e: { kind: "turn_started" } });
    eq(s.turnStartAt, 10_000, "turn starts at t0");
    eq(s.turnWaitAccumMs, 0, "fresh turn has no wait accum");
    now = 12_000;
    s = reducer(s, {
      type: "event",
      e: { kind: "approval_request", approval: { id: "bg-1", tool: "bash", subject: "sleep 1" } },
    });
    eq(s.promptWaitStartedAt, 12_000, "approval_request records wait start at event time");
    now = 15_000;
    // Tab stays off-screen for 3s; controller still counts via open interval.
    eq(currentTurnWaitMs(s, now), 3_000, "open wait counts while tab is backgrounded");
    s = reducer(s, { type: "clearApproval" });
    eq(s.promptWaitStartedAt, undefined, "clearApproval closes the open wait");
    eq(s.turnWaitAccumMs, 3_000, "clearApproval accumulates background wait into the turn");
    now = 16_000;
    s = reducer(s, { type: "event", e: { kind: "message", text: "done", reasoning: "" } });
    s = reducer(s, { type: "event", e: { kind: "turn_done" } });
    const assistant = s.items.find((item) => item.kind === "assistant");
    // Wall 6s (10k→16k) − 3s wait = 3s model work.
    eq(assistant?.kind === "assistant" && assistant.workDurationMs, 3_000, "workDurationMs excludes user-wait including background wait");
  } finally {
    Date.now = originalNow;
  }
}

{
  const restoredContext = reducer(initialState, {
    type: "context",
    context: {
      used: 42,
      window: 200,
      sessionTokens: 120,
      compactRatio: 0.5,
      sessionCost: 0.012,
      sessionCurrency: "$",
      cacheHitTokens: 80,
      cacheMissTokens: 20,
    },
  });
  const reset = reducer(restoredContext, { type: "reset" });
  eq(reset.context.used, 0, "reset clears context used tokens");
  eq(reset.context.window, 200, "reset preserves context window");
  eq(reset.context.sessionTokens, 0, "reset clears context session tokens");
  eq(reset.context.cacheHitTokens, undefined, "reset clears restored cache hit tokens");
  eq(reset.context.cacheMissTokens, undefined, "reset clears restored cache miss tokens");
  eq(reset.context.sessionCost, undefined, "reset clears restored context session cost");
  eq(reset.sessionCost, 0, "reset clears restored session cost state");
  eq(reset.sessionCurrency, "¥", "reset restores default session currency");
}

{
  const idleExecutor = reducer(
    { ...initialState, context: { used: 0, window: 200, sessionTokens: 0 } },
    { type: "event", e: { kind: "usage", usage: usage("executor") } },
  );
  eq(idleExecutor.sessionTokens, 0, "executor usage outside a turn does not inflate session tokens");
  eq(idleExecutor.context.used, 0, "executor usage outside a turn does not refresh context used tokens");

  const idleHelper = reducer(initialState, { type: "event", e: { kind: "usage", usage: usage("classifier") } });
  eq(idleHelper.sessionTokens, 0, "helper usage outside a turn does not inflate session tokens");
  eq(idleHelper.sessionCost, 0, "helper usage outside a turn does not inflate session cost");

  const pendingClassifier = reducer(
    { ...initialState, running: true, context: { used: 0, window: 200, sessionTokens: 0 } },
    { type: "event", e: { kind: "usage", usage: usage("classifier") } },
  );
  eq(pendingClassifier.sessionTokens, 120, "classifier usage while send is running counts toward session tokens");
  eq(pendingClassifier.sessionCost, 0.001, "classifier usage while send is running counts toward session cost");
  eq(pendingClassifier.context.used, 0, "classifier usage while send is running does not refresh context used tokens");

  const active = reducer(initialState, { type: "event", e: { kind: "turn_started" } });
  const activeHelper = reducer(active, { type: "event", e: { kind: "usage", usage: usage("subagent") } });
  eq(activeHelper.sessionTokens, 120, "helper usage inside a turn still counts toward session tokens");
  eq(activeHelper.sessionCost, 0.001, "helper usage inside a turn still counts toward session cost");
  eq(activeHelper.usage, undefined, "helper usage inside a turn does not become displayed latest usage");

  const plannerFirst = reducer(active, { type: "event", e: { kind: "usage", usage: usage("planner") } });
  eq(plannerFirst.sessionTokens, 120, "planner usage inside a turn still counts toward session tokens");
  eq(plannerFirst.usage, undefined, "planner usage does not fill the single displayed usage slot");

  const activeExecutor = reducer(active, { type: "event", e: { kind: "usage", usage: usage("executor") } });
  const afterCompaction = reducer(activeExecutor, { type: "event", e: { kind: "usage", usage: usage("compaction") } });
  eq(afterCompaction.usage?.source, "executor", "compaction usage does not overwrite displayed executor usage");
  eq(afterCompaction.sessionTokens, 240, "compaction usage still contributes to session token totals");
}

{
  let s = reducer(initialState, { type: "user", text: "first", seq: 0, submissionId: "meta-submission" });
  s = reducer(s, { type: "event", e: { kind: "turn_started" } });
  s = reducer(s, { type: "event", e: { kind: "notice", level: "info", text: "runtime notice" } });
  s = reducer(s, { type: "event", e: { kind: "turn_done", checkpointTurn: 0, submissionId: "meta-submission" } });
  const user = s.items.find((item) => item.kind === "user");
  const notice = s.items.find((item) => item.kind === "notice" && item.text === "runtime notice");
  eq(user?.kind === "user" && user.checkpointTurn, 0, "turn_done stamps the exact user with checkpoint turn zero");
  eq(Boolean(notice), true, "turn_done checkpoint assignment preserves runtime notices");
}

{
  const s = reducer(initialState, { type: "event", e: { kind: "notice", level: "warn", text: "short notice", detail: "verbose diagnostic" } });
  const notice = s.items.find((item) => item.kind === "notice" && item.text === "short notice");
  eq(notice?.kind === "notice" && notice.detail, "verbose diagnostic", "runtime notices preserve expandable detail text");
}

{
  let s = reducer(initialState, {
    type: "history_page",
    mode: "replace",
    page: {
      messages: [
        { role: "user", content: "recent prompt", checkpointTurn: 1060 },
        { role: "assistant", content: "recent answer" },
      ],
      startTurn: 60,
      endTurn: 61,
      totalTurns: 61,
      hasOlder: true,
    },
  });
  eq(s.items.some((item) => item.kind === "user" && item.text === "recent prompt"), true, "history page replace renders the latest window");
  eq(s.historyStartTurn, 61, "legacy history page converts its zero-based cursor to the first absolute turn");
  eq(s.historyHasOlder, true, "history page records older availability");
  const recentUser = s.items.find((item) => item.kind === "user" && item.text === "recent prompt");
  eq(recentUser?.kind === "user" && recentUser.checkpointTurn, 1060, "paged history hydrates its authoritative checkpoint turn");
  eq(recentUser?.kind === "user" && recentUser.historyTurn, 61, "legacy history page preserves the absolute question turn");
  s = reducer(s, { type: "history_older_start" });
  eq(s.historyOlderLoading, true, "older history request marks loading");
  s = reducer(s, { type: "history_older_error", error: "read failed" });
  eq(s.historyOlderError, "read failed", "older history failures remain available to the retry UI");
  s = reducer(s, { type: "history_older_start" });
  eq(s.historyOlderError, undefined, "retrying older history clears the previous failure");
  s = reducer(s, {
    type: "history_page",
    mode: "prepend",
    page: {
      messages: [
        { role: "user", content: "older prompt" },
        { role: "assistant", content: "older answer" },
      ],
      startTurn: 0,
      endTurn: 1,
      totalTurns: 61,
      hasOlder: false,
    },
  });
  const users = s.items.filter((item) => item.kind === "user");
  eq(users[0]?.kind === "user" && users[0].text, "older prompt", "older history prepends before the current window");
  eq(users[1]?.kind === "user" && users[1].text, "recent prompt", "older history keeps the current window");
  eq(users[0]?.kind === "user" && users[0].historyTurn, 1, "legacy prepend starts at absolute turn one");
  eq(users[1]?.kind === "user" && users[1].historyTurn, 61, "legacy prepend keeps the recent page's absolute turn");
  eq(s.historyHasOlder, false, "older history clears hasOlder when all pages are loaded");
  eq(s.historyOlderLoading, false, "older history clears loading");
}

// ── Todo-only readiness cards retract once the list shows all complete ──────
{
  const args = JSON.stringify({ todos: [{ content: "Write verification notes", status: "completed" }] });
  let s = reducer(initialState, { type: "event", e: { kind: "turn_done", outcome: "final_readiness", readiness: { missing: ["todo"], attempts: 1 } } });
  ok(s.items.some((item) => item.kind === "notice" && item.variant === "delivery"), "todo-only readiness card shows at the gated turn");
  s = reducer(s, { type: "event", e: { kind: "tool_dispatch", tool: { id: "tw1", name: "todo_write", args, readOnly: true } } });
  s = reducer(s, { type: "event", e: { kind: "tool_result", tool: { id: "tw1", name: "todo_write", args, readOnly: true, output: "task list updated" } } });
  s = reducer(s, { type: "event", e: { kind: "turn_done" } });
  ok(!s.items.some((item) => item.kind === "notice" && item.variant === "delivery"), "an all-complete todo list retracts the stale todo-only card");
}
{
  const args = JSON.stringify({ todos: [{ content: "Write verification notes", status: "completed" }] });
  let s = reducer(initialState, { type: "event", e: { kind: "turn_done", outcome: "final_readiness", readiness: { missing: ["todo", "verification"], attempts: 1 } } });
  s = reducer(s, { type: "event", e: { kind: "tool_dispatch", tool: { id: "tw2", name: "todo_write", args, readOnly: true } } });
  s = reducer(s, { type: "event", e: { kind: "tool_result", tool: { id: "tw2", name: "todo_write", args, readOnly: true, output: "task list updated" } } });
  s = reducer(s, { type: "event", e: { kind: "turn_done" } });
  ok(s.items.some((item) => item.kind === "notice" && item.variant === "delivery"), "a card listing non-todo gaps survives todo completion");
}
{
  const args = JSON.stringify({ todos: [{ content: "Write verification notes", status: "in_progress" }] });
  let s = reducer(initialState, { type: "event", e: { kind: "turn_done", outcome: "final_readiness", readiness: { missing: ["todo"], attempts: 1 } } });
  s = reducer(s, { type: "event", e: { kind: "tool_dispatch", tool: { id: "tw3", name: "todo_write", args, readOnly: true } } });
  s = reducer(s, { type: "event", e: { kind: "tool_result", tool: { id: "tw3", name: "todo_write", args, readOnly: true, output: "task list updated" } } });
  s = reducer(s, { type: "event", e: { kind: "turn_done" } });
  ok(s.items.some((item) => item.kind === "notice" && item.variant === "delivery"), "an incomplete todo list keeps the todo-only card");
}

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
