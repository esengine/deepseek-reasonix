import assert from "node:assert/strict";
import type { TranscriptSnapshot } from "../lib/transcriptProtocol";
import type { SnapshotTransport } from "../lib/transcriptSnapshotClient";

Object.defineProperty(globalThis, "window", { configurable: true, value: { go: { main: { App: {} } } } });
const [{ TurnEventProjector }, { TranscriptSnapshotClient, resolveSnapshotItems }, { initialState, reducer, historyMessagesToItems }] = await Promise.all([
  import("../lib/turnEventProjection"), import("../lib/transcriptSnapshotClient"), import("../lib/useController"),
]);
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}
function cut(overrides: Partial<TranscriptSnapshot> = {}): TranscriptSnapshot {
  return { protocolVersion: 1, snapshotId: "cut-1", identity: { sessionId: "session", headId: "head", rewriteEpoch: 0, runtimeEpoch: "epoch" },
    projectionRevision: 4, coveredThroughSeq: 4, records: [
      { id: "m:user", order: 0, message: { role: "user", messageId: "user", submissionId: "submit", content: "same question", historyTurn: 1 }, refs: [] },
      { id: "m:assistant", order: 1, message: { role: "assistant", messageId: "assistant", content: "prefix", pending: true }, refs: [] },
    ], activeRecords: [], runtime: { turnId: "turn", submissionId: "submit", status: "in_progress", pendingEvents: [] },
    activeAttempts: [{ id: "attempt", messageId: "assistant" }], before: 0, hasOlder: false, totalRecords: 2, totalTurns: 1, stale: false, ...overrides };
}
const quietTransport = { replay: async () => ({ events: [], floorSeq: 1, latestSeq: 4, nextAfterSeq: 4, hasMore: false, resetRequired: false, runtimeEpoch: "epoch" }) };
const transport: SnapshotTransport = { snapshot: async () => cut(), page: async () => cut(), content: async () => ({ data: "", nextOffset: 0, done: true, stale: false }) };

// Hydration suspends the live suffix, installs a full prefix, then advances the
// cursor only after reducer commits. Equal user text is never the correlation key.
{
  let state = reducer(initialState, { type: "user", text: "same question", seq: 0, submissionId: "submit" });
  const userID = state.items[0].id;
  const fetched = deferred<TranscriptSnapshot>();
  const projector = new TurnEventProjector(quietTransport);
  const client = new TranscriptSnapshotClient({ ...transport, snapshot: () => fetched.promise }, projector);
  projector.bind((event) => { client.observeEvent("tab", event); state = reducer(state, { type: "event", e: event }); });
  const loading = client.load("tab", (snapshot) => { state = reducer(state, { type: "transcript_snapshot", snapshot }); });
  projector.receiveLive("tab", { kind: "text", seq: 5, runtimeEpoch: "epoch", sessionId: "session", messageId: "assistant", text: " suffix" });
  fetched.resolve(cut());
  assert.equal(await loading, true);
  for (let i = 0; i < 30; i++) await Promise.resolve();
  assert.equal(state.live?.text, "prefix suffix");
  assert.equal(state.items.filter((item) => item.kind === "user").length, 1);
  assert.equal(state.items.find((item) => item.kind === "user")?.id, userID);
}

// A full active prefix is fetched from the same immutable cut before install.
{
  const content = deferred<{ data: string; nextOffset: number; done: boolean; stale: boolean }>();
  const snapshot = cut();
  snapshot.records[1].refs = [{ snapshotId: "cut-1", recordId: "m:assistant", path: ["content"], bytes: 11 }];
  let committed: TranscriptSnapshot | undefined;
  const projector = new TurnEventProjector(quietTransport);
  projector.bind(() => {});
  const client = new TranscriptSnapshotClient({ ...transport, snapshot: async () => snapshot, content: () => content.promise }, projector);
  const loading = client.load("tab", (value) => { committed = value; });
  await Promise.resolve();
  assert.equal(committed, undefined);
  content.resolve({ data: "full prefix", nextOffset: 11, done: true, stale: false });
  await loading;
  assert.equal((committed as TranscriptSnapshot | undefined)?.records[1].message.content, "full prefix");
}

// Session replacement fences both snapshot responses and content reads.
{
  const old = deferred<TranscriptSnapshot>();
  const projector = new TurnEventProjector(quietTransport);
  projector.bind(() => {});
  const client = new TranscriptSnapshotClient({ ...transport, snapshot: () => old.promise }, projector);
  let commits = 0;
  const loading = client.load("tab", () => { commits++; });
  client.release("tab");
  old.resolve(cut());
  assert.equal(await loading, false);
  assert.equal(commits, 0);
}

// Older pages cannot resurrect a discarded active attempt or replace a live
// tool result. Rows split across page boundaries merge by tool call identity.
{
  let state = initialState;
  const projector = new TurnEventProjector(quietTransport);
  const newest = cut({ records: [{ id: "tool:call", order: 2, message: { role: "tool", content: "result", toolCallId: "call", toolName: "read_file" }, refs: [] }],
    activeRecords: [cut().records[1]], totalRecords: 3, before: 2, hasOlder: true });
  const older = cut({ records: [cut().records[0], { ...cut().records[1], message: { ...cut().records[1].message,
    toolCalls: [{ id: "call", name: "read_file", arguments: "args" }] } }], totalRecords: 3 });
  const client = new TranscriptSnapshotClient({ ...transport, snapshot: async () => newest, page: async () => older }, projector);
  projector.bind((event) => { client.observeEvent("tab", event); state = reducer(state, { type: "event", e: event }); });
  await client.load("tab", (snapshot) => { state = reducer(state, { type: "transcript_snapshot", snapshot }); });
  await client.older("tab", (snapshot) => { state = reducer(state, { type: "transcript_page", snapshot }); });
  const tool = state.items.find((item) => item.kind === "tool");
  assert.equal(tool?.kind === "tool" && tool.args, "args");
  assert.equal(tool?.kind === "tool" && tool.output, "result");
  assert.equal(state.items.filter((item) => item.id === "call").length, 1);
}

// A delayed content response cannot overwrite a text mutation after the cut.
{
  const snapshot = cut({ activeAttempts: [], runtime: { status: "completed", pendingEvents: [] } });
  snapshot.records[1].message.pending = false;
  snapshot.records[1].refs = [{ snapshotId: "cut-1", recordId: "m:assistant", path: ["content"], bytes: 4 }];
  const content = deferred<{ data: string; nextOffset: number; done: boolean; stale: boolean }>();
  const projector = new TurnEventProjector(quietTransport);
  let state = initialState;
  const client = new TranscriptSnapshotClient({ ...transport, snapshot: async () => snapshot, content: () => content.promise }, projector);
  projector.bind((event) => { client.observeEvent("tab", event); state = reducer(state, { type: "event", e: event }); });
  await client.load("tab", (value) => { state = reducer(state, { type: "transcript_snapshot", snapshot: value }); });
  const pending = resolveSnapshotItems(client, "tab", "m:assistant", () => state, historyMessagesToItems,
    (patches) => { state = reducer(state, { type: "history_items_patch", patches }); });
  projector.receiveLive("tab", { kind: "text", seq: 5, runtimeEpoch: "epoch", messageId: "assistant", text: " new" });
  content.resolve({ data: "old!", nextOffset: 4, done: true, stale: false });
  await pending;
  assert.equal(state.live?.text, "prefix new");
  assert.notEqual(state.items.find((item) => item.kind === "assistant")?.text, "old!");
}
// A pinned active user can precede the newest page by hundreds of records.
// Loading the middle must insert below that user, never move body above it.
{
  const record = (id: string, order: number) => ({ id: `m:${id}`, order, message: { role: "assistant", messageId: id, content: id }, refs: [] });
  const newest = cut({ records: [record("tail", 4)], activeRecords: [cut().records[0]], totalRecords: 5, before: 4, hasOlder: true, activeAttempts: [] });
  const page = cut({ records: [record("middle1", 2), record("middle2", 3)], activeRecords: [cut().records[0]], totalRecords: 5, before: 2, hasOlder: true });
  let state = initialState;
  const projector = new TurnEventProjector(quietTransport);
  projector.bind(() => {});
  const client = new TranscriptSnapshotClient({ ...transport, snapshot: async () => newest, page: async () => page }, projector);
  await client.load("tab", (snapshot) => { state = reducer(state, { type: "transcript_snapshot", snapshot }); });
  await client.older("tab", (snapshot) => { state = reducer(state, { type: "transcript_page", snapshot }); });
  assert.deepEqual(state.items.map((item) => item.id), ["m:user", "m:middle1", "m:middle2", "m:tail"]);
}

// A failed attempt removed by the suffix is a tombstone for older pages.
{
  let state = initialState;
  const newest = cut({ records: [], activeRecords: cut().records, before: 2, hasOlder: true });
  const projector = new TurnEventProjector(quietTransport);
  const client = new TranscriptSnapshotClient({ ...transport, snapshot: async () => newest, page: async () => cut() }, projector);
  projector.bind((event) => { client.observeEvent("tab", event); state = reducer(state, { type: "event", e: event }); });
  await client.load("tab", (snapshot) => { state = reducer(state, { type: "transcript_snapshot", snapshot }); });
  projector.receiveLive("tab", { kind: "stream_attempt", messageId: "assistant", seq: 5, runtimeEpoch: "epoch", streamAttempt: { id: "attempt", action: "discard" } });
  await client.older("tab", (snapshot) => { state = reducer(state, { type: "transcript_page", snapshot }); });
  assert.equal(state.items.some((item) => item.id === "m:assistant"), false);
}
console.log("transcript snapshot client races: ok");
