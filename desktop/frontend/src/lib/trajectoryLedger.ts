// Trajectory ledger wiring.
//
// The controller's single wire handler is the only place that sees every frame
// the runtime accepts for a tab — live and gap-repaired alike — so the ledger's
// bookkeeping lives behind these calls and the controller only has to name them.
// Keeping the wiring here also keeps `useController.ts` from growing: that file
// carries a repolint file-size budget, and a feature may not inflate it.

import { app } from "./bridge";
import type { TurnEventProjector } from "./turnEventProjection";
import type { TurnEventMeta, TurnEventReplayView, WireEvent } from "./types";
import { useTrajectoryStore } from "../store/trajectory";

export type { TurnEventMeta } from "./types";

/** Record one accepted frame, live or replayed. */
export function observeTrajectoryEvent(tabId: string, event: WireEvent, meta?: TurnEventMeta): void {
  const store = useTrajectoryStore.getState();
  store.ingest(tabId, event, meta);
  // A host with no durable ledger can never answer the coverage question, so it
  // is answered rather than left reading as "not read yet" forever.
  // observeCoverage is idempotent, so this costs nothing after the first frame.
  if (typeof app.TurnEventsForTab !== "function") store.observeCoverage(tabId, null);
}

/** The kernel emits no event for the user's own turn, so its row opens here. */
export function recordTrajectoryUserTurn(tabId: string, text: string): void {
  useTrajectoryStore.getState().ingestUser(tabId, text);
}

/** The session or head was replaced: the rows describe a surface now gone. */
export function resetTrajectory(tabId: string): void {
  useTrajectoryStore.getState().reset(tabId);
}

/** The tab is gone: keeping its rows would outlive the only view that shows them. */
export function forgetTrajectory(tabId: string): void {
  useTrajectoryStore.getState().release(tabId);
}

/** Publish coverage from every replay page, before the rows it describes. */
export function bindTrajectoryLedger(projector: TurnEventProjector): () => void {
  const observer = (tabId: string, replay: TurnEventReplayView) =>
    useTrajectoryStore.getState().observeCoverage(tabId, replay);
  projector.bindReplayObserver(observer);
  return () => projector.unbindReplayObserver(observer);
}
