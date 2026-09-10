// Trajectory projection: folds the session's own event stream into a ledger of
// *activities* — what ran, when, and for how long — which the dock's trajectory
// panel draws as a table with a time axis.
//
// It is a pure fold over the events the transcript already consumes, so it opens
// no second event path and reads no state the transcript owns. The row model is
// an activity state machine rather than one row per event: an event that starts
// an activity opens exactly one row and the event that ends it edits that row,
// which is what keeps one tool call on one line instead of two.
//
// Times come from the kernel wherever the kernel measured one (a replayed
// envelope's `createdAt`, a turn's `turnStartedAt`, a tool's `startedAt`) and
// from receipt time otherwise. The provenance is kept per row so an
// approximation is never presented as a measurement.

import type { TurnEventMeta, TurnEventReplayView, WireEvent } from "./types";

/** A piece of a rendered payload: plain text, a bound value, or a number. */
export type Span = { t: string } | { b: string } | { n: string };

/** What the rows actually cover, as the host answered it. Absence is not one of
 *  these: "unread" means the coverage read has not landed yet, never that the
 *  record is empty — reading the last row as the end is the mistake this field
 *  exists to stop. */
export type TrajectoryAvailability = "unread" | "complete" | "compacted" | "live_only";

export type TrajectoryKind =
  | "turn"
  | "user"
  | "assistant"
  | "tool"
  | "model_round"
  | "usage"
  | "approval"
  | "ask"
  | "guardian"
  | "compaction"
  | "maintenance"
  | "recovery"
  | "phase"
  | "steer"
  | "completion"
  | "notice";

/** Where a row's time came from. "kernel" was measured by the host; "receipt"
 *  is when this client received the event, which is an approximation. */
export type TrajectoryStamp = "kernel" | "receipt";

export interface TrajectoryRow {
  /** Client row ordinal, 1-based and stable for the life of the tab. */
  seq: number;
  /** Seconds since the axis origin. */
  at: number;
  /** Seconds the activity ran. Absent means "started here": drawn as a tick,
   *  never as an invented width. */
  dur?: number;
  kind: TrajectoryKind;
  tool?: string;
  turnId?: string;
  payload: Span[];
  subs: Span[][];
  stamped: TrajectoryStamp;
  /** Still running. The bar ends at the last observed event, so an in-flight
   *  row is never given a completion time it has not reached. */
  open?: boolean;
}

export interface TrajectoryState {
  rows: TrajectoryRow[];
  /** Axis origin, unix ms. Zero until the first row lands. */
  t0: number;
  /** Activities still running, by the id the kernel gave them. */
  open: Record<string, number>;
  /** Model rounds by their attempt id, kept after they settle so a late usage
   *  report still finds the row it belongs to. */
  rounds: Record<string, number>;
  /** Row indices of model rounds, oldest first, for claiming by recency. */
  roundRows: number[];
  /** Rounds that already absorbed a usage report, by row seq. */
  claimed: Record<number, true>;
  /** Highest sequence already folded in; guards replay/live interleaving. */
  lastSeq: number;
  availability: TrajectoryAvailability;
  /** True once this view dropped its oldest rows at the local cap. */
  trimmedLocally: boolean;
  /** Host clock minus receipt clock, in ms. */
  skewMs: number;
  skewSampled: boolean;
}

/** Local row cap per tab. The host's ledger owns durability; this only bounds
 *  what one open panel keeps in memory. */
export const TRAJECTORY_ROW_CAP = 5000;

export type TrajectoryInput =
  | WireEvent
  | { kind: "__clear" }
  | { kind: "__user"; text: string }
  | { kind: "__coverage"; view: TurnEventReplayView | null };

export function initialTrajectory(): TrajectoryState {
  return {
    rows: [], t0: 0, open: {}, rounds: {}, roundRows: [], claimed: {},
    lastSeq: 0, availability: "unread", trimmedLocally: false,
    skewMs: 0, skewSampled: false,
  };
}

/** Axis extent in seconds: the end of the last-finishing activity, which is not
 *  the same as the last row to start. */
export function axisSpan(rows: TrajectoryRow[]): number {
  let span = 0;
  for (const row of rows) {
    const end = row.at + (row.dur ?? 0);
    if (end > span) span = end;
  }
  return span;
}

/** Flattens spans to the text they carry; both halves of a span are values. */
function flat(spans: Span[]): string {
  return spans.map((s) => ("b" in s ? s.b : "n" in s ? s.n : s.t)).join("");
}

/** A record's effect, before it is resolved against the rows already on the
 *  page. Exactly one of open/close/touch/usage is set. */
interface Made {
  kind: TrajectoryKind;
  payload: Span[];
  subs?: Span[][];
  dur?: number;
  tool?: string;
  /** Key this record opens an activity under. */
  open?: string;
  /** Key this record settles. */
  close?: string;
  /** Key this record extends without settling. */
  touch?: string;
  /** A usage report looking for the round it bills. */
  usage?: boolean;
  /** Head for the row a usage report gets when it bills against no round. */
  standalone?: Span[];
}

const secs = (ms: number): Span => ({ n: (ms / 1000).toFixed(2) });
const tokens = (n: number | undefined): Span => ({ n: String(n ?? 0) });

/** How far a receipt clock may be corrected by one kernel sample. Delivery lag
 *  is milliseconds; a remote host can be seconds off. Beyond a minute the
 *  sample is not describing a clock offset worth bending the axis for. */
const SKEW_LIMIT_MS = 60_000;
const clampSkew = (sample: number): number => Math.max(-SKEW_LIMIT_MS, Math.min(SKEW_LIMIT_MS, sample));

function record(e: WireEvent): Made | null {
  switch (e.kind) {
    case "turn_started":
      // The turn is the outermost activity of the run: every model round and
      // tool call inside it is a bar nested in this one.
      return { kind: "turn", payload: [{ t: "turn_started" }], open: e.turnId || "__turn" };

    case "turn_done": {
      return {
        kind: "turn",
        payload: [{ t: e.err ? `turn_done · err=${e.err}` : "turn_done" }],
        close: e.turnId || "__turn",
      };
    }

    case "message":
      return e.itemId === "user"
        ? { kind: "user", payload: [{ t: "user_message" }] }
        : { kind: "assistant", payload: [{ t: "assistant_text" }] };

    case "tool_dispatch": {
      const tool = e.tool;
      // A partial dispatch is one call streaming its arguments in, not a second
      // call; recording it doubles every row. Without an id the call cannot be
      // addressed later, so there is nothing to open.
      if (!tool || tool.partial || !tool.id) return null;
      const name = tool.resolvedName || tool.name;
      const head: Span[] = [{ t: "tool " }, { b: name }];
      if (tool.parentId) head.push({ t: " ↳ " }, { b: tool.parentId });
      return { kind: "tool", payload: head, tool: name, open: tool.id };
    }

    // Progress is the same call still running, so it lands on the line the call
    // already has rather than opening one of its own.
    case "tool_progress": {
      const tool = e.tool;
      if (!tool?.id) return null;
      return { kind: "tool", payload: [], touch: tool.id };
    }

    case "tool_result": {
      const tool = e.tool;
      if (!tool?.id) return null;
      const name = tool.resolvedName || tool.name;
      const tail: Span[] = [];
      // The kernel measured this one; receipt time here would also count the
      // trip back to the browser.
      if (tool.durationMs != null) tail.push({ t: " · " }, secs(tool.durationMs));
      if (tool.err) tail.push({ t: " · err=" }, { b: tool.err });
      return {
        kind: tool.err ? "recovery" : "tool",
        payload: tail,
        tool: name,
        dur: tool.durationMs != null ? tool.durationMs / 1000 : undefined,
        close: tool.id,
      };
    }

    // The model round is where a turn's time actually goes, and without it the
    // trajectory draws everything except the thing the reader waited for.
    case "stream_attempt": {
      const sa = e.streamAttempt;
      if (!sa?.id) return null;
      if (sa.action === "begin") {
        const head: Span[] = [{ t: "model_round" }];
        if ((sa.attempt ?? 1) > 1) head.push({ t: " · retry " }, { n: `${sa.attempt}/${sa.max ?? "?"}` });
        return { kind: "model_round", payload: head, open: sa.id };
      }
      const spent: Span[] =
        sa.action === "discard" ? [{ t: " · discarded" }, { t: " · " }, { b: sa.reason || "unknown" }] : [];
      return { kind: "model_round", payload: spent, close: sa.id };
    }

    case "usage": {
      const u = e.usage;
      if (!u) return null;
      return {
        kind: "usage",
        usage: true,
        standalone: [{ t: "usage" }],
        payload: [
          { t: " · hit " }, tokens(u.cacheHitTokens),
          { t: " · miss " }, tokens(u.cacheMissTokens),
          { t: " · out " }, tokens(u.completionTokens),
          { t: " · src=" }, { b: u.source || "executor" },
        ],
      };
    }

    case "retrying":
      return {
        kind: "recovery",
        payload: [
          { t: "retry " },
          { n: `${e.retryAttempt ?? 0}/${e.retryMax ?? 0}` },
          { t: " · scope=" }, { b: e.retryScope ?? "stream" },
        ],
      };

    case "provider_unreachable":
      return {
        kind: "recovery",
        payload: [{ t: "provider_unreachable" }, ...(e.detail ? [{ t: " · " }, { b: e.detail } satisfies Span] : [])],
      };

    case "notice":
      // `code` is the stable id a notice is localized by, not a severity, so
      // the level is what decides whether this reads as a problem.
      return {
        kind: e.level === "warn" ? "recovery" : "notice",
        payload: [{ t: "notice " }, { b: e.level ?? "info" }, { t: " · " + (e.text ?? "") }],
      };

    case "guardian_assessment": {
      const g = e.guardian;
      if (!g) return null;
      return {
        kind: "guardian",
        payload: [{ t: "tool=" }, { b: g.tool }, { t: " · verdict=" }, { b: g.outcome }],
        subs: g.rationale ? [[{ t: g.rationale }]] : [],
        tool: g.tool,
      };
    }

    case "approval_request":
      return e.approval
        ? {
            kind: "approval",
            payload: [{ t: "approval tool=" }, { b: e.approval.tool }, { t: " · " + (e.approval.subject ?? "") }],
            tool: e.approval.tool,
          }
        : null;

    case "ask_request":
      return e.ask
        ? { kind: "ask", payload: [{ t: "ask_request · questions=" }, { n: String(e.ask.questions.length) }] }
        : null;

    case "compaction_started":
      return { kind: "compaction", payload: [{ t: "compaction_started" }] };

    case "compaction_done":
      return {
        kind: "compaction",
        payload: [{ t: "compaction_done · folded=" }, { n: String(e.compaction?.messages ?? 0) }],
      };

    case "context_maintenance": {
      const m = e.maintenance;
      if (!m) return { kind: "maintenance", payload: [{ t: e.text || "context_maintenance" }] };
      const payload: Span[] = [{ t: "context_maintenance · status=" }, { b: m.status ?? "" }];
      if (m.action) payload.push({ t: " · action=" }, { b: m.action });
      // A verdict without its reason is the line that makes a runaway
      // unreadable: it says maintenance happened, never what it decided.
      if (m.reason) payload.push({ t: " · reason=" }, { b: m.reason });
      if (m.inputTokens) payload.push({ t: " · in " }, { n: String(m.inputTokens) });
      if (m.resultTokens) payload.push({ t: " → " }, { n: String(m.resultTokens) });
      if (m.cacheBreak) payload.push({ t: " · " }, { b: "cache_break" });
      return { kind: "maintenance", payload };
    }

    case "completion_summary": {
      const c = e.completion;
      if (!c) return null;
      return {
        kind: "completion",
        payload: [
          { t: "verdict=" }, { b: c.verdict },
          { t: " · mutations " }, { n: String(c.mutations) },
          { t: " · checks " }, { n: `${c.checks_passed}/${c.checks_passed + c.checks_failed}` },
        ],
        subs: c.review ? [[{ t: c.review }]] : [],
      };
    }

    case "phase":
    case "turn_phase":
      return { kind: "phase", payload: [{ t: "phase · " }, { b: e.phase ?? "" }] };

    case "steer":
      return { kind: "steer", payload: [{ t: "steer · " }, { b: e.text ?? "" }] };

    default:
      // Transcript content and low-signal frames (text, reasoning, read status,
      // workspace changes) belong to the transcript. Unknown kinds are ignored
      // so a newer host cannot break this view.
      return null;
  }
}

/** Kernel-measured time for this event, when the host measured one. */
function kernelTime(e: WireEvent, meta: TurnEventMeta | undefined): number | undefined {
  if (meta?.createdAt && meta.createdAt > 0) return meta.createdAt;
  // turnStartedAt is set only on turn_started by the host, so it dates that row
  // and nothing else.
  if (e.turnStartedAt && e.turnStartedAt > 0) return e.turnStartedAt;
  const startedAt = e.tool?.startedAt;
  if (startedAt && startedAt > 0) return startedAt;
  return undefined;
}

function userMade(text: string): Made {
  return { kind: "user", payload: [{ t: "user_message · " }, { b: text.slice(0, 60) }] };
}

/** The newest model round in this turn that has not absorbed a usage report.
 *  The host's usage payload carries no attempt id, so recency within the turn
 *  is the only address it has. */
function unclaimedRound(s: TrajectoryState, turnId: string | undefined): number | undefined {
  for (let i = s.roundRows.length - 1; i >= 0; i -= 1) {
    const index = s.roundRows[i];
    const row = s.rows[index];
    if (!row || s.claimed[row.seq]) continue;
    if (turnId && row.turnId && row.turnId !== turnId) continue;
    return index;
  }
  return undefined;
}

/** Applies the local row cap, shifting the index maps with the dropped prefix. */
function cap(state: TrajectoryState): TrajectoryState {
  const drop = state.rows.length - TRAJECTORY_ROW_CAP;
  if (drop <= 0) return state;
  const shift = (map: Record<string, number>): Record<string, number> => {
    const next: Record<string, number> = {};
    for (const [key, index] of Object.entries(map)) {
      if (index >= drop) next[key] = index - drop;
    }
    return next;
  };
  const firstKept = state.rows[drop]?.seq ?? 0;
  return {
    ...state,
    rows: state.rows.slice(drop),
    open: shift(state.open),
    rounds: shift(state.rounds),
    roundRows: state.roundRows.filter((index) => index >= drop).map((index) => index - drop),
    // Claims belong to rows that are still on the page; seq increases with the
    // row ordinal, so the first kept row's seq is the floor.
    claimed: Object.fromEntries(
      Object.entries(state.claimed).filter(([seq]) => Number(seq) >= firstKept),
    ),
    trimmedLocally: true,
  };
}

export function reduceCoverage(s: TrajectoryState, view: TurnEventReplayView | null): TrajectoryState {
  // No ledger to read: the rows are only what this connection happened to see.
  if (!view) return s.availability === "live_only" ? s : { ...s, availability: "live_only" };
  // floorSeq is the oldest individually replayable sequence. Above 1 the host
  // has folded earlier events away, so a rebuilt view genuinely starts later.
  const availability: TrajectoryAvailability = view.floorSeq > 1 ? "compacted" : "complete";
  // A reset means the rows on the page no longer describe this record; rebuild
  // from the page rather than appending to a stale prefix.
  if (view.resetRequired) return { ...initialTrajectory(), availability };
  // Repeating an answer changes nothing, and a probe that runs per frame must
  // not hand subscribers a new object each time.
  return s.availability === availability ? s : { ...s, availability };
}

export function reduceTrajectory(
  s: TrajectoryState,
  input: TrajectoryInput,
  nowMs: number,
  meta?: TurnEventMeta,
): TrajectoryState {
  if (input.kind === "__clear") return initialTrajectory();
  if (input.kind === "__coverage") return reduceCoverage(s, input.view ?? null);

  const wire = input.kind === "__user" ? undefined : (input as WireEvent);
  // Recorded first, so an event this view ignores returns the very same state
  // object: stream deltas arrive per token, and a new reference per token would
  // wake every subscriber for nothing.
  const made = wire ? record(wire) : userMade((input as { text: string }).text);
  if (!made) return s;

  let state = s;
  if (wire && typeof wire.seq === "number" && wire.seq > 0) {
    // Events already folded in are dropped: a gap repair replays a suffix that
    // live frames may have covered.
    if (wire.seq <= state.lastSeq) return state;
    state = { ...state, lastSeq: wire.seq };
  }

  const kernel = wire ? kernelTime(wire, meta) : undefined;
  const stamped: TrajectoryStamp = kernel !== undefined ? "kernel" : "receipt";
  if (kernel !== undefined && !state.skewSampled && meta === undefined) {
    // A live event carrying a kernel stamp dates the host clock against ours:
    // the host measured it, we merely received it, so the difference is the
    // offset to add to every receipt time (normally a few ms of delivery lag,
    // larger when the host is remote). A replayed event is excluded — its
    // receipt time is the repair moment, not the moment the event happened —
    // and the sample is bounded so one odd timestamp cannot bend the axis.
    state = { ...state, skewSampled: true, skewMs: clampSkew(kernel - nowMs) };
  }
  const t = kernel ?? nowMs + state.skewMs;
  const t0 = state.t0 || t;
  const at = (t - t0) / 1000;
  const turnId = wire?.turnId;

  // Settling or extending a line already on the page: the activity has been one
  // row since it started, so its end is an edit, not another entry.
  const key = made.close ?? made.touch;
  if (key !== undefined) {
    const index = state.open[key];
    const row = index === undefined ? undefined : state.rows[index];
    // Settling something never opened is a frame missing its other half.
    if (!row) return state;
    const rows = state.rows.slice();
    rows[index] = {
      ...row,
      payload: made.payload.length ? [...row.payload, ...made.payload] : row.payload,
      subs: made.subs?.length ? [...row.subs, ...made.subs] : row.subs,
      // A tool reports its own duration; anything else is measured by how long
      // the reader waited, and only once it has actually ended.
      dur: made.close ? (made.dur ?? at - row.at) : at - row.at,
      tool: row.tool ?? made.tool,
      open: made.close === undefined ? true : undefined,
    };
    if (made.close === undefined) return { ...state, rows };
    const open = { ...state.open };
    delete open[key];
    return { ...state, rows, open };
  }

  // A usage report bills the round it belongs to. Compaction and planner calls
  // bill against no round at all, and a row of their own says so.
  if (made.usage) {
    const index = unclaimedRound(state, turnId);
    const row = index === undefined ? undefined : state.rows[index];
    if (row && index !== undefined) {
      const rows = state.rows.slice();
      rows[index] = { ...row, payload: [...row.payload, ...made.payload] };
      return { ...state, rows, claimed: { ...state.claimed, [row.seq]: true } };
    }
  }

  const payload = made.standalone ? [...made.standalone, ...made.payload] : made.payload;
  const rows = [
    ...state.rows,
    {
      seq: state.rows.length + 1, at, kind: made.kind, payload,
      subs: made.subs ?? [], dur: made.dur, tool: made.tool, turnId, stamped,
      open: made.open !== undefined ? true : undefined,
    },
  ];
  let next: TrajectoryState = { ...state, t0, rows };
  if (made.open !== undefined) {
    next = { ...next, open: { ...state.open, [made.open]: rows.length - 1 } };
    if (made.kind === "model_round") {
      next = {
        ...next,
        rounds: { ...state.rounds, [made.open]: rows.length - 1 },
        // Kept past the close: this is the row a late usage report must find.
        roundRows: [...state.roundRows, rows.length - 1],
      };
    }
  }
  return cap(next);
}

/** Machine-readable export: what was seen, when, and how much it covers. A file
 *  outlives the window that made it, so it carries its own coverage — a prefix
 *  handed over without one reads as a whole session. */
export function serializeTrajectory(s: TrajectoryState): string {
  return JSON.stringify(
    {
      exported: new Date().toISOString(),
      availability: s.availability,
      trimmedLocally: s.trimmedLocally,
      span: Number(axisSpan(s.rows).toFixed(3)),
      rows: s.rows.map((row) => ({
        seq: row.seq,
        at: Number(row.at.toFixed(3)),
        dur: row.dur === undefined ? undefined : Number(row.dur.toFixed(3)),
        kind: row.kind,
        tool: row.tool,
        turnId: row.turnId,
        stamped: row.stamped,
        open: row.open,
        text: flat(row.payload),
        detail: row.subs.map(flat),
      })),
    },
    null,
    2,
  );
}
