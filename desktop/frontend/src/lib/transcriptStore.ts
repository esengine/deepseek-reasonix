// transcriptStore is the per-session record store behind the transcript view
// (Phase C of the session-switch/history refactor). It replaces the old
// "convert a whole HistoryPage on every call" flow with windowed paging over
// HistorySliceForTab:
//
//   - Records keyed by stable backend entryId, kept sorted by (order, entryId)
//     and merged a page at a time (replace / prepend / append) — no full
//     re-sort on each op; pages are contiguous suffixes/prefixes.
//   - Item projection derives Item ids from entryIds, so ids stay stable
//     across page merges (the old h<startTurn>-<seq> scheme renumbered every
//     item on prepend). Cross-page tool call/result pairs merge exactly like
//     the single-shot historyMessagesToItems conversion: a result row that
//     paged in before its call converts standalone first and is folded into
//     the call's tool item (same item id: the toolCallId) when the call's
//     page arrives.
//   - Weighted LRU: at most maxResidentSessions sessions keep records
//     resident; history body bytes and the parsed-markdown cache each have a
//     byte budget. Sessions whose tab is active, running, or mid-turn are
//     pinned out of eviction. Eviction only releases memory — records are
//     re-fetchable from the backend via HistorySliceForTab.
//   - Generation binding: every in-flight slice/content request carries the
//     session generation it started under. Switching away, evicting, or
//     starting a newer load bumps the generation; late responses are
//     discarded (Wails calls are not abortable).
//   - Lazy content: entries carrying refs[] keep preview text inline;
//     requestFullContent fetches and assembles HistoryContentForTab chunks on
//     demand (and automatically for refs in the newest page). A stale chunk
//     marks the ref stale and keeps the preview.
//
// Rendering consumes the store through TranscriptProjection (items + paging
// state); useController dispatches projections into per-tab reducer state.
import { asArray } from "./array";
import { historicalResultNotice } from "./completionResultState";
import { app } from "./bridge";
import { noteHistoryPage, registerTranscriptCacheDiagnostics } from "./sessionDiagnostics";
import { TranscriptMarkdownCache, type ParsedMarkdownValue } from "./transcriptMarkdownCache";
export type { ParsedMarkdownValue } from "./transcriptMarkdownCache";
import { historySearchAndAnswer } from "./searchTranscript";
import { fileDiffFromWire, summarizeFileDiff } from "./tools";
import {
  historyToolError,
  isReadOnlyTool,
  type Item,
} from "./useController";
import { historyNoticeItems } from "./controllerNotices";
import type {
  HistoryContentChunk,
  HistoryContentRef,
  HistoryEntry,
  HistoryMessage,
  HistorySlice,
  HistorySliceRequest,
  MemoryCitation,
} from "./types";

export interface TranscriptBackend {
  HistorySliceForTab(tabID: string, req: HistorySliceRequest): Promise<HistorySlice>;
  HistoryContentForTab(tabID: string, ref: HistoryContentRef, chunkIndex: number): Promise<HistoryContentChunk>;
}

/** Grace window after a tab stops being active before its sessions become eviction candidates. */
const DEFAULT_EVICT_COOLDOWN_MS = 5 * 60_000;

export interface TranscriptStoreOptions {
  /** Resident sessions with records (unpinned). Default 3. */
  maxResidentSessions?: number;
  /** Total inline history body bytes across resident sessions. Default 32MiB. */
  historyBodyBudgetBytes?: number;
  /** Parsed-markdown cache budget. Default 16MiB. */
  markdownBudgetBytes?: number;
  /**
   * Grace window (ms) after a tab stops being the active tab before its
   * sessions may be evicted. Guards the "switch to a busy tab, switch back,
   * scroll up" flow: without it the freshly left session is the first FIFO
   * victim and its scroll anchor is lost mid-browse. Default 5 minutes.
   */
  evictCooldownMs?: number;
}

export interface TranscriptProjection {
  items: Item[];
  startTurn: number;
  endTurn: number;
  totalTurns: number;
  hasOlder: boolean;
  revision: number;
  revisionKnown: boolean;
  digest: string;
}

export interface LoadOlderResult extends TranscriptProjection {
  /** "prepend": page older items; "reload": cursor went stale, full latest replace. */
  kind: "prepend" | "reload";
  /** Items contributed by the older page (kind === "prepend"). */
  prependItems: Item[];
  /** Existing item ids superseded by cross-page tool merges (kind === "prepend"). */
  removeIds: string[];
}

export interface TranscriptContentChange {
  tabId: string;
  /** Re-converted items keyed by their stable item id. */
  patches: Record<string, Item>;
}

interface TranscriptRecord {
  entryId: string;
  turn: number;
  order: number;
  message: HistoryMessage;
  refs: HistoryContentRef[];
  /** field -> full content once fetched; also applied into message. */
  resolved?: Record<string, string>;
  /** field -> true when the backend reported the ref stale; preview is kept. */
  staleRefs?: Record<string, true>;
  bytes: number;
}

interface RecordConversion {
  items: Item[];
  /** Result records this record's tool calls consumed (entryIds). */
  claims: string[];
  /** ID-based calls converted without a result (toolCallIds). */
  unresolvedIds: string[];
  /** Positional (id-less) call indexes still unmatched. */
  pendingPositional: number[];
  /** callIndex -> result record entryId for matched calls (re-conversion input). */
  matches: Map<number, string>;
}

interface SessionTranscript {
  key: string;
  tabId: string;
  sessionPath: string;
  records: TranscriptRecord[];
  byId: Map<string, TranscriptRecord>;
  /** toolCallId -> result record entryId (first record wins, like resultByID). */
  toolResultOwners: Map<string, string>;
  /** entryId -> projected items of that record ([] when consumed). */
  contributions: Map<string, Item[]>;
  /** Result record entryIds folded into a call's tool item. */
  consumed: Set<string>;
  /** result entryId -> claimer (assistant) entryId. */
  consumedBy: Map<string, string>;
  /** toolCallId -> assistant record entryId whose call still lacks a result. */
  unresolvedCalls: Map<string, string>;
  /** assistant entryId -> unmatched positional call indexes. */
  pendingPositional: Map<string, number[]>;
  /** assistant entryId -> callIndex -> result entryId (for re-conversion). */
  matchTables: Map<string, Map<number, string>>;
  itemsCache: Item[] | null;
  nextCursor: string;
  hasOlder: boolean;
  totalTurns: number;
  startTurn: number;
  endTurn: number;
  revision: number;
  revisionKnown: boolean;
  digest: string;
  generation: number;
  bodyBytes: number;
  olderInFlight: boolean;
  pendingContent: Map<string, { generation: number; promise: Promise<string | undefined> }>;
}

const DEFAULT_MAX_RESIDENT_SESSIONS = 3;
const DEFAULT_HISTORY_BODY_BUDGET = 32 << 20;
const DEFAULT_MARKDOWN_BUDGET = 16 << 20;

function sessionKeyFor(tabId: string, sessionPath: string): string {
  return `${tabId}\n${sessionPath}`;
}

function sliceRevisionKnown(slice: Pick<HistorySlice, "revision" | "revisionKnown">): boolean {
  // Compatibility with the first HistorySlice contract: positive revisions
  // were already canonical, but revisionKnown was not exposed yet.
  return slice.revisionKnown ?? (slice.revision ?? 0) > 0;
}

function compareRecords(a: Pick<TranscriptRecord, "order" | "entryId">, b: Pick<TranscriptRecord, "order" | "entryId">): number {
  if (a.order !== b.order) return a.order - b.order;
  return a.entryId < b.entryId ? -1 : a.entryId > b.entryId ? 1 : 0;
}

import { recordBytes } from "./transcriptRecordBytes";

function entryToRecord(entry: HistoryEntry): TranscriptRecord {
  return {
    entryId: entry.entryId,
    turn: entry.turn,
    order: entry.order,
    message: entry.message,
    refs: asArray<HistoryContentRef>(entry.refs),
    bytes: recordBytes(entry.message),
  };
}

// itemIdForToolCall mirrors the single-shot conversion: id-addressed tool
// items take the toolCallId whether they come from the call or from a
// standalone result row, so a late-merging pair keeps one stable item id.
function itemIdForToolCall(tcId: string, fallback: string): string {
  return tcId || fallback;
}

// Convert one record into its items. Mirrors historyMessagesToItems per role,
// with tool results resolved through the session-wide view instead of a
// page-local map. priorMatches/priorClaims carry a re-conversion's earlier
// positional assignments so they reproduce exactly.
function convertRecord(
  rec: TranscriptRecord,
  view: { records: TranscriptRecord[]; indexOf: Map<string, number>; toolResultOwners: Map<string, string> },
  consumed: Set<string>,
  priorMatches?: Map<number, string>,
): RecordConversion {
  const items: Item[] = [];
  const claims: string[] = [];
  const unresolvedIds: string[] = [];
  const pendingPositional: number[] = [];
  const matches = new Map<number, string>(priorMatches);
  const m = rec.message;
  const id = `he:${rec.entryId}`;

  if (m.role === "system") return { items, claims, unresolvedIds, pendingPositional, matches };
  if (m.role === "phase") {
    if (m.content.trim() !== "") items.push({ kind: "phase", id, text: m.content });
    return { items, claims, unresolvedIds, pendingPositional, matches };
  }
  if (m.role === "notice") {
    if (m.completionReceipt || m.completionSummary) {
      const result = historicalResultNotice(m, id);
      if (result) items.push(result);
      return { items, claims, unresolvedIds, pendingPositional, matches };
    }
    return { items: historyNoticeItems(m, id), claims, unresolvedIds, pendingPositional, matches };
  }
  if (m.role === "compaction") {
    items.push({
      kind: "compaction",
      id,
      pending: Boolean(m.pending),
      trigger: m.trigger ?? "",
      messages: m.messages ?? 0,
      summary: m.summary ?? "",
      archive: m.archive ?? "",
    });
    return { items, claims, unresolvedIds, pendingPositional, matches };
  }
  if (m.role === "user") {
    if (m.content.trim() !== "") {
      items.push({ kind: "user", id, text: m.content, submitText: m.submitText, createdAt: m.createdAt, checkpointTurn: m.checkpointTurn, historyTurn: rec.turn > 0 ? rec.turn : undefined });
    }
    return { items, claims, unresolvedIds, pendingPositional, matches };
  }

  if (m.role === "assistant") {
    const memoryCitations = asArray<MemoryCitation>(m.memoryCitations);
    items.push(...historySearchAndAnswer(id, {
      content: m.content,
      reasoning: m.reasoning,
      workDurationMs: m.workDurationMs,
      memoryCitations: memoryCitations.length > 0 ? memoryCitations : undefined,
      serverSearch: m.serverSearch,
    }));
    const toolCalls = m.toolCalls ?? [];
    // Positional scan cursor: id-less calls consume the following unconsumed
    // id-less tool rows in order, stopping at the first non-tool record —
    // the same run positionalToolResults walks in the single-shot pass.
    let scan = (view.indexOf.get(rec.entryId) ?? -1) + 1;
    for (let callIndex = 0; callIndex < toolCalls.length; callIndex += 1) {
      const tc = toolCalls[callIndex];
      let result: HistoryMessage | undefined;
      let resultEntryId: string | undefined;
      const prior = matches.get(callIndex);
      if (prior) {
        resultEntryId = prior;
        result = view.records[view.indexOf.get(prior) ?? -1]?.message;
      } else if (tc.id) {
        const owner = view.toolResultOwners.get(tc.id);
        if (owner) {
          resultEntryId = owner;
          result = view.records[view.indexOf.get(owner) ?? -1]?.message;
        } else {
          unresolvedIds.push(tc.id);
        }
      } else {
        while (scan < view.records.length) {
          const candidate = view.records[scan];
          if (candidate.message.role !== "tool") break;
          scan += 1;
          if (candidate.message.toolCallId || consumed.has(candidate.entryId)) continue;
          resultEntryId = candidate.entryId;
          result = candidate.message;
          break;
        }
        if (!resultEntryId) pendingPositional.push(callIndex);
      }
      if (resultEntryId) {
        matches.set(callIndex, resultEntryId);
        claims.push(resultEntryId);
        consumed.add(resultEntryId);
      }
      const archived = Boolean(tc.argumentsArchived || result?.toolResultArchived);
      const output = result?.toolResultArchived ? undefined : result?.content ?? "";
      const error = result?.toolResultError || (output ? historyToolError(output) : undefined);
      const fileDiff = fileDiffFromWire(tc);
      items.push({
        kind: "tool",
        id: itemIdForToolCall(tc.id, `he:${rec.entryId}:tc${callIndex}`),
        name: tc.name,
        args: tc.arguments ?? "",
        readOnly: typeof tc.resolvedReadOnly === "boolean" ? tc.resolvedReadOnly : isReadOnlyTool(tc.name),
        resolvedName: tc.resolvedName,
        capabilityId: tc.capabilityId,
        status: result ? (error ? "error" : "done") : "stopped",
        output,
        error,
        dataArchived: archived || undefined,
        subject: tc.subject,
        summary: summarizeFileDiff(fileDiff) || tc.summary,
        fileDiff,
        isShell: tc.name === "bash" || (tc.id || "").startsWith("shell-"),
        execution: result?.execution,
      });
    }
    return { items, claims, unresolvedIds, pendingPositional, matches };
  }

  if (m.role === "tool") {
    if (consumed.has(rec.entryId)) return { items, claims, unresolvedIds, pendingPositional, matches };
    const output = m.toolResultArchived ? undefined : m.content;
    const error = m.toolResultError || (output ? historyToolError(output) : undefined);
    items.push({
      kind: "tool",
      id: itemIdForToolCall(m.toolCallId ?? "", id),
      name: m.toolName || "tool",
      args: "",
      readOnly: isReadOnlyTool(m.toolName || "tool"),
      status: error ? "error" : "done",
      output,
      error,
      dataArchived: m.toolResultArchived || undefined,
      isShell: (m.toolName || "") === "bash" || (m.toolCallId || "").startsWith("shell-"),
      execution: m.execution,
    });
    return { items, claims, unresolvedIds, pendingPositional, matches };
  }

  return { items, claims, unresolvedIds, pendingPositional, matches };
}

function applyResolvedField(rec: TranscriptRecord, ref: HistoryContentRef, data: string): boolean {
  const m = rec.message;
  switch (ref.field) {
    case "content": rec.message = { ...m, content: data }; return true;
    case "reasoning": rec.message = { ...m, reasoning: data }; return true;
    case "submitText": rec.message = { ...m, submitText: data }; return true;
    case "detail": rec.message = { ...m, detail: data }; return true;
    case "code": rec.message = { ...m, code: data }; return true;
    case "summary": rec.message = { ...m, summary: data }; return true;
    case "archive": rec.message = { ...m, archive: data }; return true;
    case "toolResultError": rec.message = { ...m, toolResultError: data }; return true;
    case "toolArguments":
    case "toolSubject":
    case "toolSummary":
    case "toolDiff": {
      const toolCalls = (m.toolCalls ?? []).map((tc) => {
        if (tc.id !== ref.toolCallId) return tc;
        if (ref.field === "toolArguments") return { ...tc, arguments: data };
        if (ref.field === "toolSubject") return { ...tc, subject: data };
        if (ref.field === "toolSummary") return { ...tc, summary: data };
        return { ...tc, diff: data };
      });
      rec.message = { ...m, toolCalls };
      return true;
    }
    default:
      return false;
  }
}

export class TranscriptStore {
  private readonly backend: TranscriptBackend;
  private readonly maxResidentSessions: number;
  private readonly historyBodyBudgetBytes: number;
  /** Insertion-ordered (oldest first); touch re-inserts at the end. */
  private readonly sessions = new Map<string, SessionTranscript>();
  private readonly tabPins = new Map<string, { live: boolean; active: boolean }>();
  private readonly listeners = new Map<string, Set<(change: TranscriptContentChange) => void>>();
  private readonly markdown: TranscriptMarkdownCache;
  private historyEvictions = 0;
  private readonly evictCooldownMs: number;
  private readonly lastActiveAt = new Map<string, number>();

  constructor(backend: TranscriptBackend, options: TranscriptStoreOptions = {}) {
    this.backend = backend;
    this.maxResidentSessions = Math.max(1, options.maxResidentSessions ?? DEFAULT_MAX_RESIDENT_SESSIONS);
    this.historyBodyBudgetBytes = Math.max(0, options.historyBodyBudgetBytes ?? DEFAULT_HISTORY_BODY_BUDGET);
    this.markdown = new TranscriptMarkdownCache(Math.max(0, options.markdownBudgetBytes ?? DEFAULT_MARKDOWN_BUDGET));
    this.evictCooldownMs = Math.max(0, options.evictCooldownMs ?? DEFAULT_EVICT_COOLDOWN_MS);
  }

  // ── session identity / LRU ────────────────────────────────────────────────

  private newSession(key: string, tabId: string, sessionPath: string): SessionTranscript {
    return {
      key,
      tabId,
      sessionPath,
      records: [],
      byId: new Map(),
      toolResultOwners: new Map(),
      contributions: new Map(),
      consumed: new Set(),
      consumedBy: new Map(),
      unresolvedCalls: new Map(),
      pendingPositional: new Map(),
      matchTables: new Map(),
      itemsCache: null,
      nextCursor: "",
      hasOlder: false,
      totalTurns: 0,
      startTurn: 0,
      endTurn: 0,
      revision: 0,
      revisionKnown: false,
      digest: "",
      generation: 0,
      bodyBytes: 0,
      olderInFlight: false,
      pendingContent: new Map(),
    };
  }

  private touch(session: SessionTranscript): void {
    this.sessions.delete(session.key);
    this.sessions.set(session.key, session);
  }

  private isPinned(session: SessionTranscript): boolean {
    const pins = this.tabPins.get(session.tabId);
    return Boolean(pins?.live || pins?.active);
  }

  /** Pin/unpin a tab with live or in-flight turn state out of the LRU. */
  setPinned(tabId: string, pinned: boolean): void {
    const pins = this.tabPins.get(tabId) ?? { live: false, active: false };
    if (pins.live === pinned) return;
    this.tabPins.set(tabId, { ...pins, live: pinned });
  }

  /**
   * The visible tab changed: pin the new active tab out of eviction and drop
   * the previous tab's active pin. Generations are NOT bumped here — a
   * background tab's in-flight load still completes into its own state (and
   * the store); generations move on session switch (fresh loadLatest), evict,
   * and unload only.
   */
  noteActiveTab(tabId: string | undefined, previousTabId?: string): void {
    if (previousTabId && previousTabId !== tabId) {
      const pins = this.tabPins.get(previousTabId) ?? { live: false, active: false };
      if (pins.active) {
        this.tabPins.set(previousTabId, { ...pins, active: false });
        // Start the eviction cooldown for the freshly deactivated tab: its
        // scroll anchor is still live in the composer/transcript and the user
        // may switch right back (see evictable()'s cooldown filter).
        this.lastActiveAt.set(previousTabId, Date.now());
      }
    }
    if (tabId) {
      const pins = this.tabPins.get(tabId) ?? { live: false, active: false };
      if (!pins.active) this.tabPins.set(tabId, { ...pins, active: true });
    }
  }

  /** Drop all sessions of a tab (pruned surface, closed tab). */
  evictTab(tabId: string): void {
    for (const [key, session] of Array.from(this.sessions.entries())) {
      if (session.tabId === tabId) this.sessions.delete(key);
    }
    this.tabPins.delete(tabId);
    this.lastActiveAt.delete(tabId);
  }

  private evictSession(session: SessionTranscript): void {
    session.generation += 1; // in-flight responses discard against a missing/stale session
    this.sessions.delete(session.key);
    this.historyEvictions += 1;
  }

  private enforceBudgets(): void {
    const now = Date.now();
    const evictable = (): SessionTranscript[] =>
      Array.from(this.sessions.values()).filter(
        (s) =>
          s.records.length > 0 &&
          !this.isPinned(s) &&
          // Eviction cooldown: a tab that just stopped being active keeps its
          // scroll anchor live for a grace window — the user may switch right
          // back and scroll (see noteActiveTab's lastActiveAt stamp).
          now - (this.lastActiveAt.get(s.tabId) ?? 0) >= this.evictCooldownMs,
      );
    let candidates = evictable();
    let resident = candidates.length;
    while (resident > this.maxResidentSessions && candidates.length > 0) {
      const victim = candidates.shift();
      if (!victim) break;
      this.evictSession(victim);
      resident -= 1;
    }
    let total = 0;
    for (const session of this.sessions.values()) total += session.bodyBytes;
    candidates = evictable();
    while (total > this.historyBodyBudgetBytes && candidates.length > 0) {
      const victim = candidates.shift();
      if (!victim) break;
      total -= victim.bodyBytes;
      this.evictSession(victim);
    }
  }

  // ── projection ────────────────────────────────────────────────────────────

  private rebuildProjection(session: SessionTranscript): Item[] {
    const items: Item[] = [];
    for (const rec of session.records) {
      const contribution = session.contributions.get(rec.entryId);
      if (contribution) items.push(...contribution);
    }
    session.itemsCache = items;
    return items;
  }

  private projectionOf(session: SessionTranscript): TranscriptProjection {
    return {
      items: session.itemsCache ?? this.rebuildProjection(session),
      startTurn: session.startTurn,
      endTurn: session.endTurn,
      totalTurns: session.totalTurns,
      hasOlder: session.hasOlder,
      revision: session.revision,
      revisionKnown: session.revisionKnown,
      digest: session.digest,
    };
  }

  /** Synchronous projection for an LRU-resident session; undefined on a miss. */
  peek(tabId: string, sessionPath: string): TranscriptProjection | undefined {
    const session = this.sessions.get(sessionKeyFor(tabId, sessionPath));
    if (!session || session.records.length === 0) return undefined;
    this.touch(session);
    return this.projectionOf(session);
  }

  /** Test/diagnostic introspection. */
  isResident(tabId: string, sessionPath: string): boolean {
    const session = this.sessions.get(sessionKeyFor(tabId, sessionPath));
    return Boolean(session && session.records.length > 0);
  }

  residentSessionCount(): number {
    let count = 0;
    for (const session of this.sessions.values()) if (session.records.length > 0) count += 1;
    return count;
  }

  totalBodyBytes(): number {
    let total = 0;
    for (const session of this.sessions.values()) total += session.bodyBytes;
    return total;
  }

  /** Cache-weight snapshot for diagnostics (sessionDiagnostics/crash context). */
  stats() {
    return {
      residentSessions: this.residentSessionCount(),
      maxResidentSessions: this.maxResidentSessions,
      bodyBytes: this.totalBodyBytes(),
      bodyBudgetBytes: this.historyBodyBudgetBytes,
      markdownBytes: this.markdown.bytes,
      markdownBudgetBytes: this.markdown.budgetBytes,
      historyEvictions: this.historyEvictions,
      markdownEvictions: this.markdown.evictions,
    };
  }

  // fetchSlice times one backend page request and records the content-free
  // page stats (entries, inline bytes, duration, stale, read-path source).
  private async fetchSlice(tabId: string, req: HistorySliceRequest): Promise<HistorySlice> {
    const startedAt = typeof performance !== "undefined" ? performance.now() : Date.now();
    const slice = await this.backend.HistorySliceForTab(tabId, req);
    const endedAt = typeof performance !== "undefined" ? performance.now() : Date.now();
    if (slice.error?.trim()) throw new Error(slice.error.trim());
    const entries = asArray<HistoryEntry>(slice.entries);
    let inlineBytes = 0;
    for (const entry of entries) inlineBytes += recordBytes(entry.message);
    noteHistoryPage({
      entries: entries.length,
      inlineBytes,
      durationMs: Math.max(0, endedAt - startedAt),
      stale: Boolean(slice.stale),
      source: slice.source ?? "",
    });
    return slice;
  }

  generationOf(tabId: string, sessionPath: string): number | undefined {
    return this.sessions.get(sessionKeyFor(tabId, sessionPath))?.generation;
  }

  // ── record merge ops ──────────────────────────────────────────────────────

  private viewOf(records: TranscriptRecord[]): {
    records: TranscriptRecord[];
    indexOf: Map<string, number>;
    toolResultOwners: Map<string, string>;
  } {
    const indexOf = new Map<string, number>();
    const toolResultOwners = new Map<string, string>();
    records.forEach((rec, index) => {
      indexOf.set(rec.entryId, index);
      const toolCallId = rec.message.role === "tool" ? rec.message.toolCallId : undefined;
      if (toolCallId && !toolResultOwners.has(toolCallId)) toolResultOwners.set(toolCallId, rec.entryId);
    });
    return { records, indexOf, toolResultOwners };
  }

  private trackConversion(session: SessionTranscript, rec: TranscriptRecord, conversion: RecordConversion): void {
    session.contributions.set(rec.entryId, conversion.items);
    session.matchTables.set(rec.entryId, conversion.matches);
    for (const claimed of conversion.claims) session.consumedBy.set(claimed, rec.entryId);
    for (const toolCallId of conversion.unresolvedIds) session.unresolvedCalls.set(toolCallId, rec.entryId);
    if (conversion.pendingPositional.length > 0) session.pendingPositional.set(rec.entryId, conversion.pendingPositional);
    else session.pendingPositional.delete(rec.entryId);
  }

  private replaceRecords(session: SessionTranscript, entries: HistoryEntry[]): void {
    const records = entries.map(entryToRecord);
    const view = this.viewOf(records);
    const consumed = new Set<string>();
    session.records = records;
    session.byId = new Map(records.map((rec) => [rec.entryId, rec]));
    session.toolResultOwners = view.toolResultOwners;
    session.contributions = new Map();
    session.consumed = consumed;
    session.consumedBy = new Map();
    session.unresolvedCalls = new Map();
    session.pendingPositional = new Map();
    session.matchTables = new Map();
    session.bodyBytes = 0;
    for (const rec of records) {
      session.bodyBytes += rec.bytes;
      const conversion = convertRecord(rec, view, consumed);
      this.trackConversion(session, rec, conversion);
    }
    session.itemsCache = null;
    this.rebuildProjection(session);
  }

  /**
   * Prepend an older page. Returns the page's contributed items plus the ids
   * of existing standalone tool items now folded into a call from this page.
   */
  private prependRecords(session: SessionTranscript, entries: HistoryEntry[]): { items: Item[]; removeIds: string[] } {
    const fresh: TranscriptRecord[] = [];
    for (const entry of entries) {
      if (session.byId.has(entry.entryId)) continue; // contract guard: never duplicate
      fresh.push(entryToRecord(entry));
    }
    if (fresh.length === 0) return { items: [], removeIds: [] };
    const combined = [...fresh, ...session.records];
    if (session.records.length > 0 && compareRecords(fresh[fresh.length - 1], session.records[0]) > 0) {
      // Backend contract violation (pages must be contiguous prefixes): fall
      // back to one full sort rather than corrupting the order.
      combined.sort(compareRecords);
    }
    const view = this.viewOf(combined);
    const consumed = new Set(session.consumed);
    const removeIds: string[] = [];
    const prependItems: Item[] = [];
    for (const rec of fresh) {
      const before = new Set(consumed);
      const conversion = convertRecord(rec, view, consumed);
      for (const claimed of conversion.claims) {
        if (before.has(claimed)) continue;
        const existing = session.contributions.get(claimed);
        if (existing && existing.length > 0) {
          // An existing standalone tool row is now folded into this call.
          for (const item of existing) removeIds.push(item.id);
          session.contributions.set(claimed, []);
        }
      }
      this.trackConversion(session, rec, conversion);
      prependItems.push(...conversion.items);
      session.bodyBytes += rec.bytes;
    }
    session.records = combined;
    session.byId = new Map(combined.map((rec) => [rec.entryId, rec]));
    session.toolResultOwners = view.toolResultOwners;
    session.consumed = consumed;
    const removeSet = new Set(removeIds);
    const base = session.itemsCache ?? this.rebuildProjection(session);
    session.itemsCache = removeSet.size > 0 ? [...prependItems, ...base.filter((item) => !removeSet.has(item.id))] : [...prependItems, ...base];
    return { items: prependItems, removeIds };
  }

  /**
   * Append newer entries (live tail / fresh suffix). Results arriving for
   * calls that paged in unresolved fold into the call's tool item. Returns the
   * appended records' contributed items. Rendering/virtual-list phases can use
   * this to stream new rows into a resident session without a full reload.
   */
  appendEntries(tabId: string, sessionPath: string, entries: HistoryEntry[]): Item[] {
    const session = this.sessions.get(sessionKeyFor(tabId, sessionPath));
    if (!session || session.records.length === 0) return [];
    const items = this.appendRecords(session, entries);
    // A live-append refreshes the LRU position too: a busy session whose tab
    // was just deactivated must not be the first FIFO eviction victim while
    // its stream is still delivering.
    this.touch(session);
    this.enforceBudgets();
    return items;
  }

  /** Append newer entries (live tail / fresh suffix). */
  private appendRecords(session: SessionTranscript, entries: HistoryEntry[]): Item[] {
    const fresh: TranscriptRecord[] = [];
    for (const entry of entries) {
      if (session.byId.has(entry.entryId)) continue;
      fresh.push(entryToRecord(entry));
    }
    if (fresh.length === 0) return [];
    const combined = [...session.records, ...fresh];
    if (session.records.length > 0 && compareRecords(session.records[session.records.length - 1], fresh[0]) > 0) {
      combined.sort(compareRecords);
    }
    const view = this.viewOf(combined);
    const consumed = new Set(session.consumed);
    const appendedItems: Item[] = [];
    let dirty = false;

    session.records = combined;
    session.byId = new Map(combined.map((rec) => [rec.entryId, rec]));
    session.toolResultOwners = view.toolResultOwners;

    // Resolve existing calls whose results only arrive now (a page cut between
    // a call and its result, or a live tail landing after the call).
    for (const rec of fresh) {
      const toolCallId = rec.message.role === "tool" ? rec.message.toolCallId : undefined;
      if (!toolCallId) continue;
      const owner = session.unresolvedCalls.get(toolCallId);
      if (!owner) continue;
      const ownerRec = session.byId.get(owner);
      if (!ownerRec) continue;
      session.unresolvedCalls.delete(toolCallId);
      const reconverted = convertRecord(ownerRec, view, consumed, session.matchTables.get(owner));
      this.trackConversion(session, ownerRec, reconverted);
      dirty = true;
    }
    for (const rec of fresh) {
      const conversion = convertRecord(rec, view, consumed);
      this.trackConversion(session, rec, conversion);
      appendedItems.push(...conversion.items);
      session.bodyBytes += rec.bytes;
    }
    session.consumed = consumed;
    if (dirty || session.itemsCache === null) {
      this.rebuildProjection(session);
    } else {
      session.itemsCache = [...session.itemsCache, ...appendedItems];
    }
    return appendedItems;
  }

  // ── paging API ────────────────────────────────────────────────────────────

  /**
   * Load the newest page. preferResident serves an LRU-resident session
   * synchronously-equivalent projection without a backend round trip.
   * Returns undefined when the load was superseded/evicted mid-flight.
   */
  async loadLatest(
    tabId: string,
    sessionPath: string,
    options: { turns?: number; entries?: number; bytes?: number; preferResident?: boolean; expectedRevision?: number; expectedDigest?: string } = {},
  ): Promise<TranscriptProjection | undefined> {
    const key = sessionKeyFor(tabId, sessionPath);
    const existing = this.sessions.get(key);
    if (options.preferResident && existing && existing.records.length > 0 &&
      this.matchesExpectedFingerprint(existing, options.expectedRevision, options.expectedDigest)) {
      this.touch(existing);
      return this.projectionOf(existing);
    }
    const session = existing ?? this.newSession(key, tabId, sessionPath);
    session.tabId = tabId;
    session.sessionPath = sessionPath;
    // A fresh load supersedes every in-flight request of the previous load.
    session.generation += 1;
    const generation = session.generation;
    this.sessions.set(key, session);
    this.touch(session);

    const { turns, entries, bytes } = options;
    let slice = await this.fetchSlice(tabId, { cursor: "", turns, entries, bytes });
    if (this.sessions.get(key) !== session || session.generation !== generation) return undefined;
    if (slice.stale) {
      // cursor "" cannot bind a stale identity, but a concurrent rewrite may
      // still report one — retry once against the settled revision.
      slice = await this.fetchSlice(tabId, { cursor: "", turns, entries, bytes });
      if (this.sessions.get(key) !== session || session.generation !== generation) return undefined;
    }
    this.replaceRecords(session, asArray<HistoryEntry>(slice.entries));
    session.nextCursor = slice.nextCursor ?? "";
    session.hasOlder = Boolean(slice.hasOlder);
    session.totalTurns = slice.totalTurns ?? 0;
    session.startTurn = slice.startTurn ?? 0;
    session.endTurn = slice.endTurn ?? 0;
    session.revision = slice.revision ?? 0;
    session.revisionKnown = sliceRevisionKnown(slice);
    session.digest = slice.digest ?? "";
    this.autoFetchRefs(session);
    this.enforceBudgets();
    if (this.sessions.get(key) !== session) return undefined; // evicted by the budget
    return this.projectionOf(session);
  }

  /**
   * Page toward older history. On a stale cursor the records are dropped and
   * the latest page is reloaded (kind "reload": callers replace, not prepend).
   */
  async loadOlder(
    tabId: string,
    sessionPath: string,
    options: { turns?: number; entries?: number; bytes?: number } = {},
  ): Promise<LoadOlderResult | undefined> {
    const key = sessionKeyFor(tabId, sessionPath);
    const session = this.sessions.get(key);
    if (!session || session.records.length === 0) {
      // Evicted or never loaded: re-prime from the newest page; callers must
      // replace rather than prepend.
      const projection = await this.loadLatest(tabId, sessionPath, options);
      return projection ? { ...projection, kind: "reload", prependItems: [], removeIds: [] } : undefined;
    }
    if (!session.hasOlder || !session.nextCursor || session.olderInFlight) return undefined;
    session.olderInFlight = true;
    const generation = session.generation;
    try {
      const slice = await this.fetchSlice(tabId, { cursor: session.nextCursor, ...options });
      if (this.sessions.get(key) !== session || session.generation !== generation) return undefined;
      if (slice.stale) {
        const projection = await this.loadLatest(tabId, sessionPath, options);
        return projection ? { ...projection, kind: "reload", prependItems: [], removeIds: [] } : undefined;
      }
      if (!this.sameFingerprint(session, slice)) {
        // A backend that raced a rewrite may return a fresh page instead of a
        // stale marker. Never prepend rows from a different canonical state.
        const projection = await this.loadLatest(tabId, sessionPath, options);
        return projection ? { ...projection, kind: "reload", prependItems: [], removeIds: [] } : undefined;
      }
      const { items, removeIds } = this.prependRecords(session, asArray<HistoryEntry>(slice.entries));
      session.nextCursor = slice.nextCursor ?? "";
      session.hasOlder = Boolean(slice.hasOlder);
      session.totalTurns = slice.totalTurns ?? session.totalTurns;
      session.startTurn = slice.startTurn ?? session.startTurn;
      session.revision = slice.revision ?? session.revision;
      session.revisionKnown = sliceRevisionKnown(slice);
      session.digest = slice.digest ?? session.digest;
      this.enforceBudgets();
      if (this.sessions.get(key) !== session) return undefined;
      return { ...this.projectionOf(session), kind: "prepend", prependItems: items, removeIds };
    } finally {
      session.olderInFlight = false;
    }
  }

  private matchesExpectedFingerprint(session: SessionTranscript, expectedRevision?: number, expectedDigest?: string): boolean {
    const digest = (expectedDigest ?? "").trim();
    const revisionKnown = typeof expectedRevision === "number" && expectedRevision > 0;
    if (digest !== "" && session.digest !== digest) return false;
    if (revisionKnown && (!session.revisionKnown || session.revision !== expectedRevision)) return false;
    if (!revisionKnown && digest === "") {
      // Metadata identity temporarily missing cannot prove a known resident
      // projection is current. A backend round trip is the safe fallback.
      return !session.revisionKnown && session.digest === "";
    }
    return true;
  }

  private sameFingerprint(session: SessionTranscript, slice: HistorySlice): boolean {
    return session.revision === (slice.revision ?? 0) &&
      session.revisionKnown === sliceRevisionKnown(slice) &&
      session.digest === (slice.digest ?? "");
  }

  // ── lazy content ──────────────────────────────────────────────────────────

  private sessionForEntry(tabId: string, entryId: string): SessionTranscript | undefined {
    for (const session of this.sessions.values()) {
      if (session.tabId === tabId && session.byId.has(entryId)) return session;
    }
    return undefined;
  }

  /**
   * Fetch the full value of a ref-replaced field, chunk by chunk, and fold it
   * into the record. Late (generation-stale) responses are discarded; a stale
   * chunk marks the ref stale and keeps the inline preview.
   */
  async requestFullContent(tabId: string, entryId: string, field: string): Promise<string | undefined> {
    const session = this.sessionForEntry(tabId, entryId);
    const rec = session?.byId.get(entryId);
    if (!session || !rec) return undefined;
    if (rec.resolved?.[field]) return rec.resolved[field];
    const ref = rec.refs.find((candidate) => candidate.field === field);
    if (!ref) return undefined;
    const pendingKey = `${entryId}${field}`;
    // Dedupe only within the same generation: a request started before a
    // session switch/evict is doomed to discard, never join it.
    const pending = session.pendingContent.get(pendingKey);
    if (pending && pending.generation === session.generation) return pending.promise;

    const generation = session.generation;
    const request = (async (): Promise<string | undefined> => {
      let data = "";
      const chunks = Math.max(1, ref.chunks);
      for (let index = 0; index < chunks; index += 1) {
        const chunk = await this.backend.HistoryContentForTab(tabId, ref, index);
        if (this.sessions.get(session.key) !== session || session.generation !== generation) return undefined;
        if (chunk.stale) {
          rec.staleRefs = { ...rec.staleRefs, [field]: true };
          return undefined;
        }
        data += chunk.data ?? "";
        if (chunk.done) break;
      }
      if (!applyResolvedField(rec, ref, data)) return undefined;
      const previousBytes = rec.bytes;
      rec.bytes = recordBytes(rec.message);
      session.bodyBytes += rec.bytes - previousBytes;
      rec.resolved = { ...rec.resolved, [field]: data };
      this.reconvertAndNotify(session, rec);
      this.enforceBudgets();
      return data;
    })();
    const entry = { generation, promise: request };
    request.finally(() => {
      if (session.pendingContent.get(pendingKey) === entry) session.pendingContent.delete(pendingKey);
    });
    session.pendingContent.set(pendingKey, entry);
    return request;
  }

  /**
   * Resolve every unresolved ref field of an entry. The rendering layer calls
   * this when a history-backed row mounts (mount implies near-viewport with
   * the virtual list's overscan); entries without refs no-op.
   */
  requestEntryFullContent(tabId: string | undefined, entryId: string): void {
    if (!tabId) return;
    const session = this.sessionForEntry(tabId, entryId);
    const rec = session?.byId.get(entryId);
    if (!session || !rec) return;
    for (const ref of rec.refs) {
      if (rec.resolved?.[ref.field] || rec.staleRefs?.[ref.field]) continue;
      void this.requestFullContent(session.tabId, entryId, ref.field).catch(() => {});
    }
  }

  private reconvertAndNotify(session: SessionTranscript, rec: TranscriptRecord): void {
    // A resolved field on a CONSUMED tool-result row shows up in the claiming
    // call's tool item, so re-convert the claimer instead of the row.
    const targetId = session.consumedBy.get(rec.entryId) ?? rec.entryId;
    const target = session.byId.get(targetId);
    if (!target) return;
    // Re-convert with the record's established claims so tool results stay put.
    const view = this.viewOf(session.records);
    const consumed = new Set(session.consumed);
    for (const claimed of session.matchTables.get(targetId)?.values() ?? []) consumed.delete(claimed);
    const conversion = convertRecord(target, view, consumed, session.matchTables.get(targetId));
    session.consumed = consumed;
    this.trackConversion(session, target, conversion);
    session.itemsCache = null;
    this.rebuildProjection(session);
    const listeners = this.listeners.get(session.tabId);
    if (!listeners || listeners.size === 0) return;
    const patches: Record<string, Item> = {};
    for (const item of conversion.items) patches[item.id] = item;
    const change: TranscriptContentChange = { tabId: session.tabId, patches };
    for (const listener of listeners) listener(change);
  }

  /** Newest-page refs resolve eagerly so the visible transcript is complete. */
  private autoFetchRefs(session: SessionTranscript): void {
    for (const rec of session.records) {
      for (const ref of rec.refs) {
        void this.requestFullContent(session.tabId, rec.entryId, ref.field).catch(() => {});
      }
    }
  }

  // ── markdown cache (populated by the rendering/worker phase) ──────────────

  getMarkdown(entryId: string, revision: number): ParsedMarkdownValue | undefined {
    return this.markdown.get(entryId, revision);
  }

  setMarkdown(entryId: string, revision: number, value: ParsedMarkdownValue): void {
    this.markdown.set(entryId, revision, value);
  }

  pinMarkdown(entryId: string, revision: number): () => void {
    return this.markdown.pin(entryId, revision);
  }

  markdownCacheSize(): number {
    return this.markdown.size();
  }

  // ── subscriptions ─────────────────────────────────────────────────────────

  /** Notified when a record's projected items change (content resolution). */
  subscribe(tabId: string, listener: (change: TranscriptContentChange) => void): () => void {
    let set = this.listeners.get(tabId);
    if (!set) {
      set = new Set();
      this.listeners.set(tabId, set);
    }
    set.add(listener);
    return () => {
      set.delete(listener);
      if (set.size === 0) this.listeners.delete(tabId);
    };
  }
}

// Bridge-backed singleton: resolves window.go.main.App at call time through
// the app proxy, so test/dev mocks install whenever they appear.
let singleton: TranscriptStore | undefined;

export function getTranscriptStore(): TranscriptStore {
  if (!singleton) {
    singleton = new TranscriptStore({
      HistorySliceForTab: (tabID, req) => app.HistorySliceForTab(tabID, req),
      HistoryContentForTab: (tabID, ref, chunkIndex) => app.HistoryContentForTab(tabID, ref, chunkIndex),
    });
  }
  return singleton;
}

// Diagnostics provider: cache weights flow to the crash/perf context without
// crash.ts importing this module's bridge-backed graph.
registerTranscriptCacheDiagnostics(() =>
  singleton?.stats() ?? {
    residentSessions: 0,
    maxResidentSessions: DEFAULT_MAX_RESIDENT_SESSIONS,
    bodyBytes: 0,
    bodyBudgetBytes: DEFAULT_HISTORY_BODY_BUDGET,
    markdownBytes: 0,
    markdownBudgetBytes: DEFAULT_MARKDOWN_BUDGET,
    historyEvictions: 0,
    markdownEvictions: 0,
  },
);
