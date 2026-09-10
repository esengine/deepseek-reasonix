// trajectory owns the per-tab activity ledger the dock's trajectory panel
// renders. It is fed from the controller's single wire-event handler, so the
// rows are a projection of the same stream the transcript consumes rather than
// a second event path.
//
// State is keyed by tab id: the panel shows one tab at a time, and a tab that
// closes (or a session that is replaced) drops its rows instead of keeping a
// ledger no surface can reach.

import { create } from "zustand";
import {
  initialTrajectory,
  reduceTrajectory,
  type TrajectoryInput,
  type TrajectoryState,
} from "../lib/trajectoryProjection";
import type { TurnEventMeta, TurnEventReplayView, WireEvent } from "../lib/types";

/** Stable empty state for a tab with no rows yet, so a selector does not hand
 *  React a new object on every render. */
export const EMPTY_TRAJECTORY: TrajectoryState = Object.freeze(initialTrajectory());

export interface TrajectoryStoreState {
  byTab: Record<string, TrajectoryState>;
  /** Fold one wire event. Replay and live frames both arrive here. */
  ingest: (tabId: string, event: WireEvent, meta?: TurnEventMeta) => void;
  /** Record the user's own turn: the kernel emits no event for it. */
  ingestUser: (tabId: string, text: string) => void;
  /** Fold what the host says the durable record covers. */
  observeCoverage: (tabId: string, view: TurnEventReplayView | null) => void;
  /** Session or head replaced under the same path: drop the stale rows. */
  reset: (tabId: string) => void;
  /** Tab closed: drop everything held for it. */
  release: (tabId: string) => void;
}

function fold(
  state: TrajectoryStoreState,
  tabId: string,
  input: TrajectoryInput,
  meta: TurnEventMeta | undefined,
  nowMs: number,
): Partial<TrajectoryStoreState> | null {
  const held = state.byTab[tabId];
  const prev = held ?? EMPTY_TRAJECTORY;
  const next = reduceTrajectory(prev, input, nowMs, meta);
  // The reducer returns the same object when an event contributes nothing.
  // Writing it anyway would wake every subscriber once per streamed token, and
  // would also mint an entry for a tab that has nothing to show.
  if (next === prev) return null;
  return { byTab: { ...state.byTab, [tabId]: next } };
}

function drop(state: TrajectoryStoreState, tabId: string): Partial<TrajectoryStoreState> | null {
  if (!(tabId in state.byTab)) return null;
  const byTab = { ...state.byTab };
  delete byTab[tabId];
  return { byTab };
}

export const useTrajectoryStore = create<TrajectoryStoreState>((set) => ({
  byTab: {},
  ingest: (tabId, event, meta) =>
    set((state) => fold(state, tabId, event, meta, Date.now()) ?? state),
  ingestUser: (tabId, text) =>
    set((state) => fold(state, tabId, { kind: "__user", text }, undefined, Date.now()) ?? state),
  observeCoverage: (tabId, view) =>
    set((state) => fold(state, tabId, { kind: "__coverage", view }, undefined, Date.now()) ?? state),
  reset: (tabId) => set((state) => drop(state, tabId) ?? state),
  release: (tabId) => set((state) => drop(state, tabId) ?? state),
}));

/** The rows held for one tab, or the shared empty state. */
export function useTabTrajectory(tabId: string | undefined): TrajectoryState {
  return useTrajectoryStore((state) => (tabId ? state.byTab[tabId] : undefined) ?? EMPTY_TRAJECTORY);
}
