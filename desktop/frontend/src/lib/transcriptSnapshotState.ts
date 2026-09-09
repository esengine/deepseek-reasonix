import type { HistoryMessage, WireEvent } from "./types";
import type { Item, State } from "./useController";
import type { TranscriptRecord, TranscriptSnapshot } from "./transcriptProtocol";

export function snapshotRecords(snapshot: TranscriptSnapshot): TranscriptRecord[] {
  const records = [...(snapshot.records ?? []), ...(snapshot.activeRecords ?? [])];
  const ids = new Set<string>();
  for (const record of records) {
    if (!record.id || ids.has(record.id) || !Number.isSafeInteger(record.order) || record.order < 0 || record.order >= snapshot.totalRecords) {
      throw new Error("invalid transcript snapshot record identity");
    }
    ids.add(record.id);
  }
  return records.sort((a, b) => a.order - b.order);
}

type Convert = (messages: HistoryMessage[], prefix: string) => { items: Item[]; seq: number };
type ApplyEvent = (state: State, event: WireEvent) => State;

function recordItemOrder(records: TranscriptRecord[], convert: Convert): Record<string, number> {
  const order: Record<string, number> = {};
  for (const record of records) {
    const items = convert([{ ...record.message, recordId: record.id }], "snapshot:").items;
    items.forEach((item, index) => { order[item.id] = Math.min(order[item.id] ?? Infinity, record.order + index / (items.length + 1)); });
  }
  return order;
}

export function transcriptPageState(state: State, page: TranscriptSnapshot, convert: Convert): State {
  const records = snapshotRecords({ ...page, activeRecords: [] });
  const converted = convert(records.map((record) => ({ ...record.message, recordId: record.id })), "snapshot:");
  const existing = new Map(state.items.map((item) => [item.id, item]));
  const order = { ...state.transcriptItemOrder };
  for (const [id, position] of Object.entries(recordItemOrder(records, convert))) order[id] = Math.min(order[id] ?? Infinity, position);
  const prefix: Item[] = [];
  for (const item of converted.items) {
    const prior = existing.get(item.id) ?? (item.kind === "user" ? state.items.find((candidate) => candidate.kind === "user" &&
      ((item.messageId && candidate.messageId === item.messageId) || (item.submissionId && candidate.submissionId === item.submissionId))) : undefined);
    if (!prior) { prefix.push(item); continue; }
    if (prior.kind === "tool" && item.kind === "tool") {
      prefix.push({ ...prior, args: prior.args || item.args, messageId: prior.messageId || item.messageId,
        name: prior.name === "tool" ? item.name : prior.name, subject: prior.subject ?? item.subject,
        summary: prior.summary ?? item.summary, fileDiff: prior.fileDiff ?? item.fileDiff });
    } else {
      if (prior.id !== item.id) { order[prior.id] = order[item.id]; delete order[item.id]; }
      prefix.push(prior);
    }
  }
  const prefixIDs = new Set(prefix.map((item) => item.id));
  const added = prefix.filter((item) => !existing.has(item.id)).length;
  const users = records.map((record) => record.message.historyTurn).filter((turn): turn is number => typeof turn === "number" && turn > 0);
  const items = [...prefix, ...state.items.filter((item) => !prefixIDs.has(item.id))];
  items.sort((a, b) => (order[a.id] ?? Infinity) - (order[b.id] ?? Infinity));
  return { ...state, items, transcriptItemOrder: order,
    seq: Math.max(state.seq, converted.seq), historyPrefixCount: state.historyPrefixCount + added,
    historyStartTurn: Math.min(state.historyStartTurn, ...users.map((turn) => turn - 1)),
    historyHasOlder: page.hasOlder, historyOlderLoading: false, historyOlderError: undefined,
    historyMutation: { seq: state.historyMutation.seq + 1, kind: "prepend" } };
}

/** One reducer transaction installs rows, runtime and the active attempt.
 * The event projector advances coverage only after this function commits. */
export function transcriptSnapshotState(state: State, snapshot: TranscriptSnapshot, convert: Convert, applyEvent: ApplyEvent, clock: number): State {
  const records = snapshotRecords(snapshot);
  const messages = records.map((record) => ({ ...record.message, recordId: record.id }));
  const converted = convert(messages, "snapshot:");
  const order = recordItemOrder(records, convert);
  const users = state.items.filter((item): item is Extract<Item, { kind: "user" }> => item.kind === "user");
  const items = converted.items.map((item) => {
    if (item.kind !== "user") return item;
    const mounted = users.find((user) =>
      (item.messageId && (user.messageId === item.messageId || user.id === `m:${item.messageId}`)) ||
      (item.submissionId && user.submissionId === item.submissionId));
    if (mounted && mounted.id !== item.id) { order[mounted.id] = order[item.id]; delete order[item.id]; }
    return mounted ? { ...item, id: mounted.id } : item;
  });
  const represented = new Set(messages.map((message) => message.submissionId).filter(Boolean));
  if (snapshot.runtime.submissionId) represented.add(snapshot.runtime.submissionId);
  const optimistic = users.filter((user) => user.submissionId && user.submissionId === state.pendingSubmissionId && !represented.has(user.submissionId));
  const active = snapshot.runtime.status === "queued" || snapshot.runtime.status === "in_progress" ||
    snapshot.runtime.status === "waiting_user" || snapshot.runtime.status === "cancelling";
  let next: State = {
    ...state,
    transcriptProtocol: 1,
    transcriptItemOrder: order,
    discardTurn: false,
    assistantSegmentOrdinal: active ? 1 : 0,
    turnStartAt: snapshot.runtime.startedAt ?? (state.activeTurnId === snapshot.runtime.turnId ? state.turnStartAt : 0),
    resolvedPromptId: undefined,
    items: [...items, ...optimistic],
    seq: Math.max(state.seq, converted.seq),
    running: active || optimistic.length > 0,
    turnActive: active,
    pendingPrompt: false,
    cancelRequested: snapshot.runtime.status === "cancelling",
    cancellable: active || optimistic.length > 0,
    activeTurnId: active ? snapshot.runtime.turnId : undefined,
    turnPhase: active ? snapshot.runtime.phase : undefined,
    completionSummary: snapshot.runtime.completionSummary,
    runtimeStatusEpoch: snapshot.identity.runtimeEpoch,
    runtimeStatusSeq: snapshot.coveredThroughSeq,
    runtimeStatusSnapshotAt: clock,
    turnLifecycleObservedAt: clock,
    pendingUser: optimistic.length ? state.pendingUser : undefined,
    pendingSubmissionId: optimistic.length ? state.pendingSubmissionId : undefined,
    live: undefined,
    currentAssistant: undefined,
    streamAttemptJournal: undefined,
    approval: undefined,
    ask: undefined,
    mcpInteraction: undefined,
    retry: undefined,
    promptArrivedAt: undefined,
    promptArrivedId: undefined,
    promptEpoch: state.promptEpoch + 1,
    promptWaitStartedAt: undefined,
    turnWaitAccumMs: 0,
    hydrateHistoryLoaded: true,
    hydratePlaceholderItems: undefined,
    historyPrefixCount: items.length,
    historyStartTurn: Math.max(0, Math.min(...messages.filter((m) => m.role === "user" && m.historyTurn).map((m) => m.historyTurn!), snapshot.totalTurns) - 1),
    historyTotalTurns: snapshot.totalTurns,
    historyHasOlder: snapshot.hasOlder,
    historyOlderLoading: false,
    historyOlderError: undefined,
    historyRevision: undefined,
    historyDigest: undefined,
    historyMutation: { seq: state.historyMutation.seq + 1, kind: "replace" },
  };
  for (const event of snapshot.runtime.pendingEvents ?? []) {
    next = applyEvent(next, { ...event, runtimeEpoch: snapshot.identity.runtimeEpoch });
  }
  const attempts = snapshot.activeAttempts ?? [];
  const attempt = attempts[attempts.length - 1];
  if (active && attempt) {
    const id = `m:${attempt.messageId}`;
    const message = messages.find((message) => message.messageId === attempt.messageId);
    const live = { id, text: message?.content ?? "", reasoning: message?.reasoning ?? "", reasoningComplete: false };
    next = { ...next, currentAssistant: id, live, streamAttemptJournal: {
      id: attempt.id,
      baselineLive: { ...live, text: "", reasoning: "" },
      baselineTurnArgChars: 0,
      createdToolIds: message?.toolCalls?.map((tool) => tool.id).filter(Boolean) ?? [],
      priorTools: {},
    } };
  }
  return next;
}
