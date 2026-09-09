import assert from "node:assert/strict";
import type { AppBindings } from "../lib/bridge";
import type { TurnEventReplayView } from "../lib/types";

let resetEntered!: () => void;
const resetStarted = new Promise<void>((resolve) => { resetEntered = resolve; });
let releaseReset!: () => void;
const resetReleased = new Promise<void>((resolve) => { releaseReset = resolve; });

const replay: TurnEventReplayView = {
  events: [
    {
      turnId: "turn-reset",
      seq: 11,
      status: "in_progress",
      event: { kind: "turn_started", turnId: "turn-reset", status: "in_progress" },
    },
    {
      turnId: "turn-reset",
      seq: 12,
      status: "in_progress",
      event: { kind: "text", turnId: "turn-reset", status: "in_progress", text: "durable" },
    },
  ],
  floorSeq: 11,
  latestSeq: 12,
  nextAfterSeq: 12,
  hasMore: false,
  resetRequired: true,
  transcriptRevision: 7,
  transcriptDigest: "digest-7",
  runtimeEpoch: "epoch-a",
};

const binding: Partial<AppBindings> = {
  TurnEventsForTab: async () => replay,
};
Object.defineProperty(globalThis, "window", {
  configurable: true,
  value: { go: { main: { App: binding as AppBindings } } } as Window,
});

const [{ TurnEventProjector }, { initialState, reducer }] = await Promise.all([
  import("../lib/turnEventProjection"),
  import("../lib/useController"),
]);

const projected: number[] = [];
const projector = new TurnEventProjector();
projector.bind((event) => projected.push(event.seq ?? 0));
projector.bindReset(async (_tabId, view) => {
  assert.equal(view.transcriptRevision, 7);
  resetEntered();
  await resetReleased;
  return true;
});
projector.observeRuntime("tab", "epoch-a", 1, 1, true);
assert.equal(projector.receiveLive("tab", { kind: "turn_status", seq: 3, runtimeEpoch: "epoch-a" }, "epoch-a"), false);
await resetStarted;
assert.equal(projector.receiveLive("tab", { kind: "turn_done", seq: 13, runtimeEpoch: "epoch-a", status: "completed" }, "epoch-a"), false);
releaseReset();
for (let attempt = 0; attempt < 40; attempt += 1) await Promise.resolve();
assert.deepEqual(projected, [11, 12, 13], "checkpoint replay is projected before the queued live tail");

let resolveStaleReplay!: (view: TurnEventReplayView) => void;
binding.TurnEventsForTab = async () => new Promise<TurnEventReplayView>((resolve) => { resolveStaleReplay = resolve; });
const staleProjection: number[] = [];
const releasedProjector = new TurnEventProjector();
releasedProjector.bind((event) => staleProjection.push(event.seq ?? 0));
releasedProjector.observeRuntime("released-tab", "epoch-old", 1, 1, true);
releasedProjector.receiveLive("released-tab", { kind: "turn_status", seq: 3, runtimeEpoch: "epoch-old" }, "epoch-old");
await Promise.resolve();
releasedProjector.release("released-tab");
resolveStaleReplay({ ...replay, resetRequired: false, floorSeq: 2, events: [replay.events[1]] });
for (let attempt = 0; attempt < 20; attempt += 1) await Promise.resolve();
assert.deepEqual(staleProjection, [], "a replay response that returns after tab release is discarded");

const persistedAssistant = { kind: "assistant", id: "assistant-live", text: "partial", reasoning: "", streaming: false } as const;
const persistedUser = { kind: "user", id: "persisted-user-live", text: "persisted question" } as const;
const optimisticUser = { kind: "user", id: "optimistic-user", text: "new question" } as const;
const oldHistory = { kind: "user", id: "history-old", text: "old question" } as const;
const state = {
  ...initialState,
  items: [oldHistory, persistedUser, persistedAssistant, optimisticUser],
  historyPrefixCount: 1,
  historyRevision: 6,
};
const rebased = reducer(state, {
  type: "history_rebase",
  items: [
    { kind: "user", id: "history-new", text: "newest persisted question" },
    { kind: "user", id: "persisted-user-durable", text: "persisted question" },
    persistedAssistant,
  ],
  startTurn: 5,
  totalTurns: 7,
  hasOlder: true,
  revision: 7,
  digest: "digest-7",
});
assert.equal(rebased.historyPrefixCount, 3);
assert.equal(rebased.items.filter((item) => item.id === "assistant-live").length, 1, "transcript/live overlap is deduplicated");
assert.equal(rebased.items.find((item) => item.id === "optimistic-user"), optimisticUser, "optimistic user item identity survives rebase");
assert.equal(rebased.historyLayoutRevision, initialState.historyLayoutRevision + 1);

const older = reducer(rebased, {
  type: "history_rebase",
  items: [{ kind: "user", id: "stale", text: "stale" }],
  startTurn: 0,
  totalTurns: 1,
  hasOlder: false,
  revision: 6,
});
assert.equal(older, rebased, "an older transcript revision cannot replace the current projection");

const monotonicProjector = new TurnEventProjector();
monotonicProjector.observeRuntime("monotonic-tab", "epoch-stable", 12, 12, false);
assert.equal(
  monotonicProjector.receiveLive("monotonic-tab", { kind: "turn_started", seq: 1, runtimeEpoch: "epoch-stable" }, "epoch-stable"),
  false,
  "a stale seq=1 event cannot reset a ledger cursor whose sequence is defined to be monotonic",
);

console.log("turn event projection reset tests passed");

const optimistic = reducer(initialState, { type: "user", text: "question", seq: 0, submissionId: "submission" });
const optimisticID = optimistic.items.find((item) => item.kind === "user")!.id;
const admitted = reducer(optimistic, { type: "event", e: { kind: "user_message", messageId: "backend-user", submissionId: "submission", text: "question" } });
assert.equal(admitted.items.filter((item) => item.kind === "user").length, 1);
assert.equal(admitted.items.find((item) => item.kind === "user")!.id, optimisticID, "admission preserves mounted optimistic identity");
const userRebased = reducer(admitted, { type: "history_rebase", items: [{ kind: "user", id: "m:backend-user", text: "question" }], startTurn: 0, totalTurns: 1, hasOlder: false });
assert.equal(userRebased.items.filter((item) => item.kind === "user").length, 1, "history deduplicates the admitted user by identity");
assert.equal(userRebased.items[0].id, optimisticID, "history retains the mounted user key");

// A snapshot and the live suffix share one ownership boundary. No runtime
// poll or pending replay may advance coverage before its rows are installed.
const snapshotIdentity = { sessionId: "session", headId: "head", rewriteEpoch: 1, runtimeEpoch: "runtime" };
const snapshotBoundary = { protocolVersion: 1 as const, snapshotId: "snapshot", identity: snapshotIdentity, projectionRevision: 5, coveredThroughSeq: 5 };
let snapshotReplayAfter = -1;
const snapshotCommits: string[] = [];
const snapshotProjector = new TurnEventProjector({ replay: async (_tab, afterSeq) => {
  snapshotReplayAfter = afterSeq;
  return { events: [], floorSeq: 1, latestSeq: afterSeq, nextAfterSeq: afterSeq, hasMore: false, resetRequired: false, runtimeEpoch: "runtime" };
} });
snapshotProjector.bind((event) => snapshotCommits.push(`event:${event.seq}`));
const firstSnapshotRequest = snapshotProjector.beginSnapshot("snapshot-tab", snapshotIdentity);
snapshotProjector.receiveLive("snapshot-tab", { kind: "text", seq: 5, runtimeEpoch: "runtime" });
snapshotProjector.receiveLive("snapshot-tab", { kind: "text", seq: 6, runtimeEpoch: "runtime" });
snapshotProjector.observeRuntime("snapshot-tab", "runtime", 6, 6, false);
assert.equal(snapshotReplayAfter, -1);
assert.equal(snapshotCommits.length, 0);
assert.throws(() => snapshotProjector.installSnapshot(firstSnapshotRequest, snapshotBoundary, () => { throw new Error("store rejected snapshot"); }), /store rejected snapshot/);
assert.equal(snapshotReplayAfter, -1, "failed snapshot commit does not release the queue");
assert.equal(snapshotProjector.installSnapshot(firstSnapshotRequest, snapshotBoundary, () => snapshotCommits.push("snapshot:5")), true);
for (let i = 0; i < 30; i++) await Promise.resolve();
assert.equal(snapshotReplayAfter, 5, "replay starts at actual snapshot coverage, never a runtime hint");
assert.deepEqual(snapshotCommits, ["snapshot:5", "event:6"]);
assert.equal(snapshotProjector.installSnapshot(firstSnapshotRequest, snapshotBoundary, () => assert.fail("snapshot committed twice")), false);

const staleSnapshotRequest = snapshotProjector.beginSnapshot("snapshot-tab", snapshotIdentity);
const replacementIdentity = { ...snapshotIdentity, sessionId: "replacement", headId: "replacement-head" };
const replacementRequest = snapshotProjector.beginSnapshot("snapshot-tab", replacementIdentity);
assert.equal(snapshotProjector.installSnapshot(staleSnapshotRequest, snapshotBoundary, () => assert.fail("stale response wrote to new session")), false);
assert.throws(() => snapshotProjector.installSnapshot(replacementRequest, snapshotBoundary, () => assert.fail("wrong identity committed")), /invalid transcript snapshot/);
assert.equal(snapshotProjector.installSnapshot(replacementRequest, { ...snapshotBoundary, snapshotId: "replacement-snapshot", identity: replacementIdentity, coveredThroughSeq: 0 }, () => {}), true);

const unboundProjector = new TurnEventProjector();
assert.throws(() => unboundProjector.receiveLive("unbound", { kind: "text", seq: 1 }), /no commit handler/);
const afterBinding: number[] = [];
unboundProjector.bind((event) => afterBinding.push(event.seq!));
unboundProjector.receiveLive("unbound", { kind: "text", seq: 1 });
assert.deepEqual(afterBinding, [1], "missing handler cannot consume coverage");

let overflowAfter = -1;
const overflowCommits: number[] = [];
const overflowProjector = new TurnEventProjector({ replay: async (_tab, afterSeq) => {
  overflowAfter = afterSeq;
  const end = Math.min(afterSeq + 512, 1200);
  return { events: Array.from({ length: end - afterSeq }, (_, index) => ({
    seq: afterSeq + index + 1, turnId: "overflow-turn", status: "in_progress" as const, event: { kind: "text", text: "x" },
  })), floorSeq: 1, latestSeq: 1200, nextAfterSeq: end, hasMore: end < 1200, resetRequired: false, runtimeEpoch: "runtime" };
} });
overflowProjector.bind((event) => overflowCommits.push(event.seq!));
const overflowRequest = overflowProjector.beginSnapshot("overflow", snapshotIdentity);
for (let seq = 1; seq <= 1200; seq++) overflowProjector.receiveLive("overflow", { kind: "text", seq, text: "x", runtimeEpoch: "runtime" });
assert.equal(overflowAfter, -1);
overflowProjector.installSnapshot(overflowRequest, { ...snapshotBoundary, coveredThroughSeq: 0 }, () => {});
for (let i = 0; i < 50; i++) await Promise.resolve();
assert.deepEqual(overflowCommits, Array.from({ length: 1200 }, (_, index) => index + 1), "bounded queue overflow recovers every event exactly once from durable replay");

// Exercise the actual ingress/commit contract, not a handler that merely
// records replay callbacks. A queued event must never re-enter admission.
let completeGap!: (view: TurnEventReplayView) => void;
binding.TurnEventsForTab = async () => new Promise((resolve) => { completeGap = resolve; });
const ordered = new TurnEventProjector();
const committed: number[] = [];
ordered.bind((event) => { committed.push(event.seq!); });
ordered.observeRuntime("ordered", "epoch", 1, 1, false);
ordered.receiveLive("ordered", { kind: "text", seq: 3, text: "queued", runtimeEpoch: "epoch" }, "epoch");
completeGap({ ...replay, resetRequired: false, runtimeEpoch: "epoch", floorSeq: 1, latestSeq: 2, nextAfterSeq: 2,
  events: [{ turnId: "turn", seq: 2, status: "in_progress", event: { kind: "text", text: "replayed" } }],
});
for (let i = 0; i < 40; i++) await Promise.resolve();
assert.deepEqual(committed, [2, 3], "durable replay precedes queued live commit exactly once");
ordered.receiveLive("ordered", { kind: "text", seq: 3, text: "queued", runtimeEpoch: "epoch" }, "epoch");
assert.deepEqual(committed, [2, 3], "a duplicate delivery cannot commit twice");

const failing = new TurnEventProjector();
failing.observeRuntime("failure", "epoch", 1, 1, false);
failing.bind(() => { throw new Error("commit failed"); });
assert.throws(() => failing.receiveLive("failure", { kind: "text", seq: 2, runtimeEpoch: "epoch" }, "epoch"), /commit failed/);
const retried: number[] = [];
failing.bind((event) => { retried.push(event.seq!); });
failing.receiveLive("failure", { kind: "text", seq: 2, runtimeEpoch: "epoch" }, "epoch");
assert.deepEqual(retried, [2], "failed commits never advance the applied cursor");

let queuedRepair!: (view: TurnEventReplayView) => void;
binding.TurnEventsForTab = async () => new Promise((resolve) => { queuedRepair = resolve; });
const queueFailure = new TurnEventProjector();
queueFailure.observeRuntime("queue-failure", "epoch", 1, 1, false);
let failQueued = true;
const queueCommits: number[] = [];
queueFailure.bind((event) => {
  if (event.seq === 3 && failQueued) throw new Error("queued commit failed");
  queueCommits.push(event.seq!);
});
queueFailure.receiveLive("queue-failure", { kind: "text", seq: 3, runtimeEpoch: "epoch" }, "epoch");
queuedRepair({ ...replay, resetRequired: false, runtimeEpoch: "epoch", floorSeq: 1, latestSeq: 2, nextAfterSeq: 2,
  events: [{ turnId: "turn", seq: 2, status: "in_progress", event: { kind: "text" } }],
});
for (let i = 0; i < 40; i++) await Promise.resolve();
failQueued = false;
queueFailure.observeRuntime("queue-failure", "epoch", 3, 2, true);
queuedRepair({ ...replay, resetRequired: false, runtimeEpoch: "epoch", floorSeq: 1, latestSeq: 3, nextAfterSeq: 2, events: [] });
for (let i = 0; i < 40; i++) await Promise.resolve();
assert.deepEqual(queueCommits, [2, 3], "a failed queued commit remains owned until a later repair commits it");

const canonicalHistory = [
  { kind: "assistant", id: "m:a", text: "same", reasoning: "thought", streaming: false },
  { kind: "assistant", id: "m:b", text: "same", reasoning: "thought", streaming: false },
  { kind: "assistant", id: "m:c", text: "later", reasoning: "", streaming: false },
] as const;
const canonicalRebase = reducer({ ...initialState, items: canonicalHistory.slice(0, 2) }, {
  type: "history_rebase", items: [...canonicalHistory], startTurn: 0, totalTurns: 1, hasOlder: false, revision: 1,
});
assert.deepEqual(canonicalRebase.items.map((item) => item.id), ["m:a", "m:b", "m:c"], "a page extending beyond the live prefix cannot duplicate that prefix");
const separateMessage = { ...canonicalHistory[0], id: "m:d" };
const repeatedText = reducer({ ...initialState, items: [separateMessage] }, {
  type: "history_rebase", items: [...canonicalHistory], startTurn: 0, totalTurns: 1, hasOlder: false, revision: 1,
});
assert.deepEqual(repeatedText.items.map((item) => item.id), ["m:a", "m:b", "m:c", "m:d"], "equal text from distinct messages is preserved");

let owned = reducer(initialState, { type: "event", e: { kind: "reasoning", messageId: "a", text: "thought" } });
owned = reducer(owned, { type: "event", e: { kind: "message", messageId: "a", reasoning: "thought", text: "answer" } });
owned = reducer(owned, { type: "event", e: { kind: "reasoning", messageId: "b", text: "next" } });
assert.deepEqual(owned.items.filter((item) => item.kind === "assistant").map((item) => item.id), ["m:a", "m:b"]);
assert.equal(owned.live?.id, "m:b");

let discarded = reducer(initialState, { type: "event", e: { kind: "stream_attempt", messageId: "failed", streamAttempt: { id: "failed", action: "begin" } } });
discarded = reducer(discarded, { type: "event", e: { kind: "message", messageId: "failed", text: "rejected" } });
discarded = reducer(discarded, { type: "event", e: { kind: "stream_attempt", messageId: "failed", streamAttempt: { id: "failed", action: "discard" } } });
assert.equal(discarded.items.some((item) => item.id === "m:failed"), false, "discard removes the attempted message even if its full response was already emitted");
assert.equal(discarded.live, undefined);
discarded = reducer(discarded, { type: "event", e: { kind: "stream_attempt", messageId: "success", streamAttempt: { id: "success", action: "begin" } } });
discarded = reducer(discarded, { type: "event", e: { kind: "reasoning", messageId: "success", text: "accepted" } });
assert.equal(discarded.live?.id, "m:success");
assert.equal(discarded.live?.reasoning, "accepted");
