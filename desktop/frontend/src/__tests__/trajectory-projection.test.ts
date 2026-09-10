// The trajectory projection is a pure fold, so these cases pin the behaviour
// that the panel's honesty rests on: one activity is one row, a time is only
// called measured when the kernel measured it, and coverage is stated rather
// than implied.

import assert from "node:assert/strict";
import {
  TRAJECTORY_ROW_CAP,
  axisSpan,
  initialTrajectory,
  reduceCoverage,
  reduceTrajectory,
  serializeTrajectory,
  type Span,
  type TrajectoryState,
} from "../lib/trajectoryProjection";
import type { TurnEventReplayView, WireEvent, WireUsage } from "../lib/types";

const T0 = 1_700_000_000_000;

function fold(state: TrajectoryState, event: WireEvent, nowMs: number, createdAt?: number): TrajectoryState {
  return reduceTrajectory(state, event, nowMs, createdAt === undefined ? undefined : { createdAt });
}

function flat(spans: Span[]): string {
  return spans.map((s) => ("b" in s ? s.b : "n" in s ? s.n : s.t)).join("");
}

const usage: WireUsage = {
  promptTokens: 10, completionTokens: 20, totalTokens: 30,
  cacheHitTokens: 100, cacheMissTokens: 7,
  source: "executor",
} as WireUsage;

/** One tool call is one line: the dispatch opens it, the result edits it. */
{
  let s = initialTrajectory();
  s = fold(s, { kind: "tool_dispatch", tool: { id: "t1", name: "bash", resolvedName: "bash", readOnly: false, startedAt: T0 } }, T0);
  s = fold(s, { kind: "tool_result", tool: { id: "t1", name: "bash", readOnly: false, durationMs: 1500 } }, T0 + 1500);
  assert.equal(s.rows.length, 1, "a settled call stays on one row");
  assert.equal(s.rows[0].kind, "tool");
  assert.equal(s.rows[0].tool, "bash");
  assert.equal(s.rows[0].dur, 1.5, "the kernel's duration is used as-is");
  assert.equal(s.rows[0].stamped, "kernel", "startedAt is a kernel measurement");
  assert.equal(s.rows[0].open, undefined, "a settled row is no longer in flight");
  assert.equal(flat(s.rows[0].payload), "tool bash · 1.50");
}

/** A partial dispatch is one call streaming its arguments, not a second call. */
{
  const s = initialTrajectory();
  assert.equal(
    fold(s, { kind: "tool_dispatch", tool: { id: "t2", name: "bash", readOnly: false, partial: true } }, T0),
    s,
    "a partial dispatch records nothing, and returns the same state object",
  );
}

/** Frames that address an activity never opened are not new activities. */
{
  const s = initialTrajectory();
  assert.equal(fold(s, { kind: "tool_progress", tool: { id: "ghost", name: "bash", readOnly: false } }, T0), s);
  assert.equal(fold(s, { kind: "tool_result", tool: { id: "ghost", name: "bash", readOnly: false } }, T0), s);
  assert.equal(fold(s, { kind: "tool_result", tool: { name: "bash", readOnly: false } }, T0), s, "a result with no id cannot be addressed");
}

/** Progress extends the line the call already has. */
{
  let s = initialTrajectory();
  s = fold(s, { kind: "tool_dispatch", tool: { id: "t3", name: "bash", readOnly: false } }, T0);
  s = fold(s, { kind: "tool_progress", tool: { id: "t3", name: "bash", readOnly: false } }, T0 + 4000);
  assert.equal(s.rows.length, 1, "progress does not open a row");
  assert.equal(s.rows[0].open, true);
  assert.equal(s.rows[0].dur, 4, "an in-flight row ends at the last observed event");
}

/** An unfinished model round is never given a completion time. */
{
  let s = initialTrajectory();
  s = fold(s, { kind: "stream_attempt", streamAttempt: { id: "a1", action: "begin" } }, T0);
  assert.equal(s.rows[0].kind, "model_round");
  assert.equal(s.rows[0].open, true);
  assert.equal(s.rows[0].dur, undefined, "no invented duration for a round still running");
  assert.equal(s.rows[0].stamped, "receipt", "a live round has no kernel timestamp");
  s = fold(s, { kind: "stream_attempt", streamAttempt: { id: "a1", action: "commit" } }, T0 + 2100);
  assert.equal(s.rows.length, 1);
  assert.equal(s.rows[0].dur, 2.1, "a settled round is measured by how long the reader waited");
}

/** Retries and discards say why. */
{
  let s = initialTrajectory();
  s = fold(s, { kind: "stream_attempt", streamAttempt: { id: "a2", action: "begin", attempt: 2, max: 3 } }, T0);
  assert.ok(flat(s.rows[0].payload).includes("retry 2/3"));
  s = fold(s, { kind: "stream_attempt", streamAttempt: { id: "a2", action: "discard", reason: "premature_eof" } }, T0 + 500);
  assert.ok(flat(s.rows[0].payload).includes("discarded"));
  assert.ok(flat(s.rows[0].payload).includes("premature_eof"));
}

/** Usage bills the round it belongs to, and stands alone when there is none. */
{
  let s = initialTrajectory();
  s = fold(s, { kind: "stream_attempt", streamAttempt: { id: "a3", action: "begin" }, turnId: "turn-1" }, T0);
  const round = s.rows.length - 1;
  s = fold(s, { kind: "stream_attempt", streamAttempt: { id: "a3", action: "commit" }, turnId: "turn-1" }, T0 + 1000);
  const rowsAfterRound = s.rows.length;
  s = fold(s, { kind: "usage", usage, turnId: "turn-1" }, T0 + 1001);
  assert.equal(s.rows.length, rowsAfterRound, "a late usage report finds the round it bills");
  assert.ok(flat(s.rows[round].payload).includes("hit 100"));
  s = fold(s, { kind: "usage", usage, turnId: "turn-1" }, T0 + 1002);
  assert.equal(s.rows.length, rowsAfterRound + 1, "a second report in the same turn has nothing to bill");
  assert.ok(flat(s.rows[s.rows.length - 1].payload).startsWith("usage"));
}

/** A sequence already folded in is not folded twice. */
{
  let s = initialTrajectory();
  s = fold(s, { kind: "turn_started", turnId: "T", seq: 5 }, T0);
  s = fold(s, { kind: "turn_started", turnId: "T", seq: 5 }, T0 + 1000);
  assert.equal(s.rows.length, 1, "a gap repair cannot duplicate what live frames covered");
  s = fold(s, { kind: "turn_done", turnId: "T", seq: 6 }, T0 + 2000);
  assert.equal(s.rows.length, 1);
  assert.equal(s.rows[0].dur, 2, "the turn is the outermost activity of the run");
}

/** Content the transcript owns never wakes the ledger. */
{
  const s = initialTrajectory();
  assert.equal(fold(s, { kind: "text", text: "hi" }, T0), s);
  assert.equal(fold(s, { kind: "reasoning", reasoning: "hmm" }, T0), s);
  assert.equal(fold(s, { kind: "read_status", readStatus: undefined } as WireEvent, T0), s);
}

/** A replayed envelope's own timestamp wins over when we happened to read it. */
{
  let s = fold(initialTrajectory(), { kind: "turn_started", turnId: "T" }, T0 + 60_000, T0);
  assert.equal(s.rows[0].at, 0);
  assert.equal(s.rows[0].stamped, "kernel");
  s = fold(s, { kind: "tool_dispatch", tool: { id: "r1", name: "read", readOnly: true, startedAt: T0 + 999_000 } }, T0 + 60_001, T0 + 2000);
  assert.equal(s.rows[1].at, 2, "the durable time is used, not the repair moment");
  assert.equal(s.skewSampled, false, "a replay says nothing about clock skew");
}

/** A live kernel stamp dates the host clock against ours. */
{
  let s = fold(initialTrajectory(), { kind: "turn_started", turnId: "T", turnStartedAt: T0 + 250 }, T0);
  assert.equal(s.skewMs, 250, "the host clock reads 250ms ahead of ours");
  assert.equal(s.skewSampled, true);
  s = fold(s, { kind: "notice", text: "hello", level: "info" }, T0 + 1250);
  const notice = s.rows[s.rows.length - 1];
  assert.equal(notice.stamped, "receipt");
  // The turn anchors the axis at its kernel time (T0+250). The receipt clock is
  // 250ms behind, so the notice is placed 1.25s in — the 1.0s an uncorrected
  // receipt time would claim is the offset, not the gap.
  assert.equal(notice.at, 1.25, "a receipt time is corrected toward the host clock");
}

/** Coverage is answered by the host, never inferred from the rows. */
{
  const view = (floorSeq: number, resetRequired = false): TurnEventReplayView =>
    ({ events: [], floorSeq, latestSeq: 10, nextAfterSeq: 10, hasMore: false, resetRequired });

  assert.equal(initialTrajectory().availability, "unread");
  assert.equal(reduceCoverage(initialTrajectory(), null).availability, "live_only");
  assert.equal(reduceCoverage(initialTrajectory(), view(1)).availability, "complete");
  assert.equal(reduceCoverage(initialTrajectory(), view(7)).availability, "compacted");

  const dirty = fold(initialTrajectory(), { kind: "turn_started", turnId: "T" }, T0);
  const rebuilt = reduceTrajectory(dirty, { kind: "__coverage", view: { ...view(7), resetRequired: true } }, T0);
  assert.equal(rebuilt.rows.length, 0, "a reset drops rows that describe the old record");
  assert.equal(rebuilt.availability, "compacted");

  // Repeating an answer changes nothing: this probe runs per frame on a host
  // that cannot answer at all, and must not wake subscribers each time.
  const live = reduceCoverage(initialTrajectory(), null);
  assert.equal(reduceCoverage(live, null), live);
  const complete = reduceCoverage(initialTrajectory(), view(1));
  assert.equal(reduceCoverage(complete, view(1)), complete);
}

/** Clearing returns the ledger to its initial state. */
{
  let s = fold(initialTrajectory(), { kind: "turn_started", turnId: "T" }, T0);
  s = reduceTrajectory(s, { kind: "__clear" }, T0);
  assert.deepEqual(s.rows, []);
  assert.equal(s.availability, "unread");
  assert.equal(s.lastSeq, 0);
}

/** The axis spans the last-finishing activity, not the last one to start. */
{
  let s = initialTrajectory();
  s = fold(s, { kind: "tool_dispatch", tool: { id: "long", name: "bash", readOnly: false } }, T0);
  s = fold(s, { kind: "notice", text: "meanwhile", level: "info" }, T0 + 1000);
  s = fold(s, { kind: "tool_result", tool: { id: "long", name: "bash", readOnly: false, durationMs: 9000 } }, T0 + 9000);
  assert.equal(axisSpan(s.rows), 9);

  const parsed = JSON.parse(serializeTrajectory(s)) as {
    availability: string; trimmedLocally: boolean; span: number;
    rows: { seq: number; at: number; dur?: number; kind: string; stamped: string; text: string }[];
  };
  assert.equal(parsed.availability, "unread");
  assert.equal(parsed.trimmedLocally, false);
  assert.equal(parsed.span, 9);
  assert.equal(parsed.rows.length, 2);
  assert.equal(parsed.rows[0].kind, "tool");
  assert.equal(parsed.rows[0].text, "tool bash · 9.00");
  assert.equal(parsed.rows[0].stamped, "receipt");
}

/** The local cap bounds one panel's memory and says so when it bites. */
{
  let s = initialTrajectory();
  for (let i = 0; i < TRAJECTORY_ROW_CAP + 5; i += 1) {
    s = fold(s, { kind: "notice", text: `n${i}`, level: "info" }, T0 + i);
  }
  assert.equal(s.rows.length, TRAJECTORY_ROW_CAP);
  assert.equal(s.trimmedLocally, true, "a trimmed prefix is stated, not hidden");
  assert.equal(s.rows[0].seq, 6, "the oldest rows are the ones dropped");
  assert.ok(!("1" in s.claimed));
}

console.log("trajectory-projection: ok");
