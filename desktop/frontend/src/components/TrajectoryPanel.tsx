// The trajectory panel: a ledger of what a run actually did, drawn as a table
// with a time axis.
//
// It renders rows the store already folded; it owns no event handling and no
// transcript state. Times arrive as numbers and are drawn as facts — a row with
// no duration is a tick, never a bar of invented width — and the footer states
// what the record covers instead of letting the last row read as the end.

import { memo, useCallback, useState } from "react";
import { app } from "../lib/bridge";
import type { Translator } from "../lib/i18n";
import { useToast } from "../lib/toast";
import {
  axisSpan,
  serializeTrajectory,
  type Span,
  type TrajectoryAvailability,
  type TrajectoryRow,
} from "../lib/trajectoryProjection";
import { useTabTrajectory } from "../store/trajectory";
import "./TrajectoryPanel.css";

/** Fixed decimal places: the axis is read against itself, so a steady width
 *  matters more than trimming trailing zeroes. */
const decimals = (value: number, places: number): string => value.toFixed(places);

/** The band a row's bar is drawn in. The model round is the trunk of a turn and
 *  gets its own tone; everything else is read as what happened inside it. */
function categoryOf(row: TrajectoryRow): string {
  if (row.kind === "model_round") return "round";
  if (row.kind === "tool") return "tool";
  if (row.kind === "recovery") return "recovery";
  if (row.kind === "turn" || row.kind === "user" || row.kind === "assistant") return "turn";
  return "sys";
}

function Spans({ of }: { of: Span[] }) {
  return (
    <>
      {of.map((span, index) => (
        // Spans are positional and immutable once folded, so the index is the
        // identity the row already has.
        "b" in span
          ? <b key={index}>{span.b}</b>
          : "n" in span
            ? <span className="trajectory__num" key={index}>{span.n}</span>
            : <span key={index}>{span.t}</span>
      ))}
    </>
  );
}

const Track = memo(function Track({ row, span }: { row: TrajectoryRow; span: number }) {
  const dur = row.dur ?? 0;
  const start = row.at;
  const end = start + dur;
  const at = (value: number) => `+${decimals(value, 2)}s`;
  const label = dur > 0 ? `${at(start)} → ${at(end)} · ${decimals(dur, 2)}s` : at(start);
  return (
    <span className="trajectory__track" data-open={row.open ? "true" : undefined} title={label}>
      <i
        className={dur > 0 ? "trajectory__bar" : "trajectory__tick"}
        data-c={categoryOf(row)}
        style={{
          left: `${(start / span) * 100}%`,
          width: dur > 0 ? `${(dur / span) * 100}%` : undefined,
        }}
      />
    </span>
  );
});

const Row = memo(function Row({ row, span }: { row: TrajectoryRow; span: number }) {
  return (
    <tr data-k={row.kind}>
      <td className="trajectory__seq">{row.seq}</td>
      <td className="trajectory__at">{decimals(row.at, 2)}s</td>
      <td className="trajectory__axis"><Track row={row} span={span} /></td>
      <td className="trajectory__kind">{row.kind}</td>
      <td className="trajectory__payload">
        <Spans of={row.payload} />
        {row.subs.map((sub, index) => (
          <span className="trajectory__sub" key={index}><Spans of={sub} /></span>
        ))}
      </td>
    </tr>
  );
});

function coverageText(availability: TrajectoryAvailability, t: Translator): string {
  switch (availability) {
    case "complete": return t("trajectory.coverage.complete");
    case "compacted": return t("trajectory.coverage.compacted");
    case "live_only": return t("trajectory.coverage.liveOnly");
    default: return t("trajectory.coverage.unread");
  }
}

function exportName(): string {
  return `trajectory-${new Date().toISOString().slice(0, 19).replace("T", "-").replace(/:/g, "")}.json`;
}

export function TrajectoryPanel({ tabId, t }: { tabId?: string; t: Translator }) {
  const state = useTabTrajectory(tabId);
  const { showToast } = useToast();
  const [busy, setBusy] = useState(false);
  // The axis spans the last-finishing activity, not the last one to start: the
  // longest bar is not necessarily the one opened last.
  const span = Math.max(1, axisSpan(state.rows));

  const onExport = useCallback(async () => {
    setBusy(true);
    try {
      const path = await app.PickExportFile(exportName(), "application/json");
      if (!path) return;
      await app.SaveExportFile(path, serializeTrajectory(state), false);
      showToast(t("trajectory.exportSuccess", { path }), "info");
    } catch (error) {
      showToast(
        t("trajectory.exportFailed", { error: error instanceof Error ? error.message : String(error) }),
        "error",
      );
    } finally {
      setBusy(false);
    }
  }, [showToast, state, t]);

  return (
    <div className="trajectory">
      <div className="trajectory__toolbar">
        <button
          type="button"
          className="btn btn--small"
          disabled={busy || state.rows.length === 0}
          onClick={() => void onExport()}
        >
          {t("trajectory.export")}
        </button>
        <span className="trajectory__span">
          {t("trajectory.timeline")}
          <span className="trajectory__num"> 0 – {decimals(span, 1)}s</span>
        </span>
      </div>

      {state.rows.length === 0 ? (
        <div className="trajectory__empty">{t("trajectory.empty")}</div>
      ) : (
        <div className="trajectory__scroll">
          <table className="trajectory__table">
            <thead>
              <tr>
                <th className="trajectory__seq">seq</th>
                <th className="trajectory__at">+t</th>
                <th className="trajectory__axis">{t("trajectory.timeline")}</th>
                <th className="trajectory__kind">record</th>
                <th>payload</th>
              </tr>
            </thead>
            <tbody>
              {state.rows.map((row) => <Row key={row.seq} row={row} span={span} />)}
            </tbody>
          </table>
        </div>
      )}

      {/* What the rows cover, in the host's words. The table cannot tell a
          session that did little from one whose record stops early, and reading
          the last row as the end is exactly the mistake. */}
      <div className="trajectory__note" data-coverage={state.availability}>
        {coverageText(state.availability, t)}
        {state.trimmedLocally ? <span className="trajectory__trimmed">{t("trajectory.trimmedLocal")}</span> : null}
      </div>
    </div>
  );
}
