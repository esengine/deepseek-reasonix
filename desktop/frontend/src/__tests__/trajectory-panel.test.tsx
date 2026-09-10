// Run: tsx src/__tests__/trajectory-panel.test.tsx
//
// The panel's job is to draw what the ledger folded and to say what that
// ledger covers, so these cases pin the drawn facts (bars, categories, the
// in-flight marker) and every coverage wording that keeps a prefix from
// reading as a whole session.

import { JSDOM } from "jsdom";
import { registerHooks } from "node:module";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { TurnEventReplayView, WireEvent } from "../lib/types";
import { installDesktopHostStub, type AppStubTable } from "./desktopHostStub";

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier.endsWith(".css")) {
      return nextResolve("./asset-stub-for-tests.ts", { ...context, parentURL: import.meta.url });
    }
    return nextResolve(specifier, context);
  },
});

let failed = 0;
function ok(value: boolean, label: string) {
  if (value) process.stdout.write(`  PASS  ${label}\n`);
  else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

const saved: { path: string; payload: string }[] = [];
const bindings: AppStubTable = {
  PickExportFile: async () => "/tmp/trajectory.json",
  SaveExportFile: async (path: string, payload: string) => { saved.push({ path, payload }); },
};

const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", {
  pretendToBeVisual: true,
  url: "http://localhost/",
});
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
globalThis.Node = dom.window.Node;
globalThis.HTMLElement = dom.window.HTMLElement;
globalThis.Event = dom.window.Event;
globalThis.MouseEvent = dom.window.MouseEvent;
globalThis.localStorage = dom.window.localStorage;
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
installDesktopHostStub(bindings);

const [{ TrajectoryPanel }, { useTrajectoryStore }] = await Promise.all([
  import("../components/TrajectoryPanel"),
  import("../store/trajectory"),
]);

// A realistic fixture: the user's own turn is stamped at receipt time, and the
// host's turn stamp follows it a moment later.
const T0 = Date.now();
const TAB = "tab-trajectory";
const rootEl = dom.window.document.getElementById("root") as HTMLElement;
const root = createRoot(rootEl);
/** The panel asks its translator for keys, so the assertions read the keys. */
const t = (key: string) => key;
const render = async () => {
  await act(async () => {
    root.render(<TrajectoryPanel tabId={TAB} t={t} />);
  });
};
/** Store mutations reach the panel through its subscription, so they run inside
 *  act for the same reason a click would. */
const mutate = async (fn: () => void) => {
  await act(async () => { fn(); });
};

// --- empty -----------------------------------------------------------------
await render();
ok((rootEl.textContent ?? "").includes("trajectory.empty"), "an empty ledger says so instead of drawing nothing");
ok((rootEl.querySelector("button") as HTMLButtonElement).disabled, "export is disabled while there is nothing to export");

// --- rows ------------------------------------------------------------------
const ingest = (event: WireEvent) => useTrajectoryStore.getState().ingest(TAB, event);
await mutate(() => {
  useTrajectoryStore.getState().ingestUser(TAB, "please audit the branch");
  ingest({ kind: "turn_started", turnId: "turn-1", turnStartedAt: T0 + 50 });
  ingest({ kind: "stream_attempt", streamAttempt: { id: "a1", action: "begin" }, turnId: "turn-1" });
  ingest({ kind: "tool_dispatch", tool: { id: "t1", name: "bash", resolvedName: "bash", readOnly: false, startedAt: T0 + 200 }, turnId: "turn-1" });
  ingest({ kind: "tool_result", tool: { id: "t1", name: "bash", readOnly: false, durationMs: 2500 }, turnId: "turn-1" });
});
await render();

const rows = [...rootEl.querySelectorAll("tbody tr")];
ok(rows.length === 4, `one row per activity (got ${rows.length})`);
ok(rows[0]?.getAttribute("data-k") === "user", "the request that caused the run opens the ledger");
ok(rows[1]?.getAttribute("data-k") === "turn", "the turn is an activity of its own");
ok(rows[2]?.getAttribute("data-k") === "model_round", "the open model round is its own row");
ok(rootEl.querySelectorAll('tr[data-k="tool"]').length === 1, "a dispatch and its result are one row, not two");
ok(rows[3]?.textContent?.includes("bash") === true, "the tool row names the tool");

const toolBar = rows[3]?.querySelector(".trajectory__bar") as HTMLElement | null;
ok(toolBar?.getAttribute("data-c") === "tool", "the bar carries its category for colouring");
ok(Boolean(toolBar?.style.width), "a settled activity is drawn as a bar, not a tick");
const openTrack = rows[2]?.querySelector(".trajectory__track") as HTMLElement | null;
ok(openTrack?.getAttribute("data-open") === "true", "an activity still running is marked as in flight");
ok(rows[2]?.querySelector(".trajectory__tick") !== null, "an activity with no measured duration is a tick");

// --- coverage --------------------------------------------------------------
const coverage = () => rootEl.querySelector(".trajectory__note")?.getAttribute("data-coverage");
ok(coverage() === "unread", "coverage starts unread rather than assumed complete");

const replay = (floorSeq: number): TurnEventReplayView =>
  ({ floorSeq, latestSeq: 9, nextAfterSeq: 9, hasMore: false, resetRequired: false, events: [] }) as TurnEventReplayView;

await mutate(() => useTrajectoryStore.getState().observeCoverage(TAB, replay(7)));
await render();
ok(coverage() === "compacted", "a folded prefix is reported as compacted, never as the whole record");
ok((rootEl.textContent ?? "").includes("trajectory.coverage.compacted"), "the footer states the compacted wording");

await mutate(() => useTrajectoryStore.getState().observeCoverage(TAB, null));
await render();
ok(coverage() === "live_only", "no ledger to read is reported as live only");

await mutate(() => useTrajectoryStore.getState().observeCoverage(TAB, replay(1)));
await render();
ok(coverage() === "complete", "a record retained from its first event is complete");

// --- export ----------------------------------------------------------------
await act(async () => {
  (rootEl.querySelector("button") as HTMLButtonElement).dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true }));
  await new Promise((resolve) => setTimeout(resolve, 0));
});
ok(saved.length === 1, "export writes one file");
ok(saved[0]?.path === "/tmp/trajectory.json", "export uses the picked path");
const exported = JSON.parse(saved[0]?.payload ?? "{}") as { availability?: string; span?: number; rows?: { kind: string }[] };
ok(exported.availability === "complete", "the file carries the coverage it was written under");
ok(exported.rows?.length === 4, "the file carries the rows");
ok((exported.span ?? 0) >= 2.5, "the axis span is exported as a number");

// --- the cap is disclosed, not hidden --------------------------------------
await mutate(() => {
  useTrajectoryStore.setState((state) => ({
    byTab: { ...state.byTab, [TAB]: { ...state.byTab[TAB], trimmedLocally: true } },
  }));
});
await render();
ok((rootEl.textContent ?? "").includes("trajectory.trimmedLocal"), "a locally trimmed prefix is disclosed");

await act(async () => root.unmount());
dom.window.close();
if (failed > 0) process.exit(1);
