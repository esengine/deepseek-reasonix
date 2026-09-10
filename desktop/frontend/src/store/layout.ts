// layout owns the desktop shell's geometry state — sidebar + right-dock widths
// and the sidebar collapse flag — as a selectable store rather than App-local
// useState. Components read a single slice via selector (only that slice
// re-renders), with no prop drilling. The geometry constants, clamps, and the
// localStorage-backed load/save helpers live here too: they are layout-domain
// knowledge that belongs with the store, and keeping them here lets the store
// initialize itself from persisted state at module load without depending on App.
//
// Persistence behavior is intentionally unchanged from the previous App-local
// implementation: the store's setters are pure (state only), and callers keep
// invoking the exported save* helpers exactly where they did before, so the
// on-disk localStorage schema and write timing are byte-identical.

import type { Dispatch, SetStateAction } from "react";
import { create } from "zustand";

import { loadLayoutSize, loadOptionalLayoutSize, saveLayoutSize } from "../lib/layoutPreferences";

import { applySetState } from "./setState";

const SIDEBAR_COLLAPSED_KEY = "reasonix.sidebar.collapsed";
const SIDEBAR_DEFAULT_WIDTH = 264;
export const SIDEBAR_MIN_WIDTH = 264;
export const CREATION_SIDEBAR_MIN_WIDTH = 236;
// Creation keeps the expanded rail at the narrow floor by default.
export const CREATION_SIDEBAR_DEFAULT_WIDTH = CREATION_SIDEBAR_MIN_WIDTH;
export const SIDEBAR_MAX_WIDTH = 300;
const SIDEBAR_VIEWPORT_RATIO = 0.18;

const RIGHT_DOCK_TREE_DEFAULT_WIDTH = 300;
export const RIGHT_DOCK_TREE_MIN_WIDTH = 300;
// Creation file-tree dock stays tighter than classic 300. With Creation's
// narrower Windows caption strip (~108px), 252 is enough for icon+label tabs.
export const CREATION_RIGHT_DOCK_TREE_MIN_WIDTH = 252;
export const CREATION_RIGHT_DOCK_TREE_DEFAULT_WIDTH = CREATION_RIGHT_DOCK_TREE_MIN_WIDTH;
export const RIGHT_DOCK_TREE_MAX_WIDTH = 560;
export const RIGHT_DOCK_PREVIEW_DEFAULT_WIDTH = 660;
export const RIGHT_DOCK_PREVIEW_MIN_WIDTH = 420;
export const RIGHT_DOCK_MIN_RENDER_WIDTH = 280;
// Creation tree mode may render below the classic 280 floor when the viewport squeezes.
export const CREATION_RIGHT_DOCK_MIN_RENDER_WIDTH = 236;
export const RIGHT_DOCK_MAX_WIDTH = 860;
const WORKSPACE_PANEL_OPEN_KEY = "reasonix.workspacePanel.open";
// First-launch default when no preference is stored (matches post-#6371 UX).
const WORKSPACE_PANEL_DEFAULT_OPEN = true;

export function clampSidebarWidth(width: number): number {
  return Math.min(SIDEBAR_MAX_WIDTH, Math.max(SIDEBAR_MIN_WIDTH, Math.round(width)));
}

export function clampCreationSidebarWidth(width: number): number {
  return Math.min(SIDEBAR_MAX_WIDTH, Math.max(CREATION_SIDEBAR_MIN_WIDTH, Math.round(width)));
}

function clampStoredSidebarWidth(width: number): number {
  return Math.min(SIDEBAR_MAX_WIDTH, Math.max(CREATION_SIDEBAR_MIN_WIDTH, Math.round(width)));
}

export function clampRightDockPreviewWidth(width: number, maxWidth = RIGHT_DOCK_MAX_WIDTH): number {
  // Cap at maxWidth (which may be below the default maximum when the viewport
  // is narrow) while never dropping below the applicable minimum.
  return Math.min(Math.max(maxWidth, RIGHT_DOCK_PREVIEW_MIN_WIDTH), Math.max(RIGHT_DOCK_PREVIEW_MIN_WIDTH, Math.round(width)));
}

export function clampRightDockTreeWidth(width: number, maxWidth = RIGHT_DOCK_TREE_MAX_WIDTH): number {
  return Math.min(Math.max(maxWidth, RIGHT_DOCK_TREE_MIN_WIDTH), Math.max(RIGHT_DOCK_TREE_MIN_WIDTH, Math.round(width)));
}

export function clampCreationRightDockTreeWidth(width: number, maxWidth = RIGHT_DOCK_TREE_MAX_WIDTH): number {
  return Math.min(Math.max(maxWidth, CREATION_RIGHT_DOCK_TREE_MIN_WIDTH), Math.max(CREATION_RIGHT_DOCK_TREE_MIN_WIDTH, Math.round(width)));
}

function clampStoredRightDockTreeWidth(width: number): number {
  // Stored widths are validated again against the live viewport at load time
  // (resolveWorkspacePanelWidth clamps to the chat pane's 400px floor), so
  // persistence only guards the sane lower bound and integer form.
  return Math.max(CREATION_RIGHT_DOCK_TREE_MIN_WIDTH, Math.round(width));
}

export function defaultSidebarWidth(): number {
  if (typeof window !== "undefined") {
    return clampSidebarWidth(window.innerWidth * SIDEBAR_VIEWPORT_RATIO);
  }
  return SIDEBAR_DEFAULT_WIDTH;
}

export function defaultCreationSidebarWidth(): number {
  return CREATION_SIDEBAR_DEFAULT_WIDTH;
}

export function defaultRightDockTreeWidth(): number {
  return RIGHT_DOCK_TREE_DEFAULT_WIDTH;
}

export function defaultCreationRightDockTreeWidth(): number {
  return CREATION_RIGHT_DOCK_TREE_DEFAULT_WIDTH;
}

function loadSidebarCollapsed(): boolean {
  if (typeof window === "undefined") return false;
  try {
    return window.localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === "1";
  } catch {
    return false;
  }
}

export function saveSidebarCollapsed(collapsed: boolean): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(SIDEBAR_COLLAPSED_KEY, collapsed ? "1" : "0");
  } catch {
    /* ignore storage failures */
  }
}

function loadSidebarWidth(): number {
  return loadLayoutSize("sidebarWidthGraphite", defaultSidebarWidth(), clampStoredSidebarWidth);
}

export function saveSidebarWidth(width: number): void {
  saveLayoutSize("sidebarWidthGraphite", width, clampStoredSidebarWidth);
}

function loadRightDockTreeWidth(): number {
  return loadLayoutSize("rightDockTreeWidth", defaultRightDockTreeWidth(), clampStoredRightDockTreeWidth);
}

export function saveRightDockTreeWidth(width: number): void {
  saveLayoutSize("rightDockTreeWidth", width, clampStoredRightDockTreeWidth);
}

function loadRightDockPreviewWidth(): number {
  return loadLayoutSize("rightDockPreviewWidth", RIGHT_DOCK_PREVIEW_DEFAULT_WIDTH, clampRightDockPreviewWidth);
}

export function saveRightDockPreviewWidth(width: number): void {
  saveLayoutSize("rightDockPreviewWidth", width, clampRightDockPreviewWidth);
}

// rightDockMode selects what the right dock shows. workspacePanelOpen is
// restored from localStorage (same pattern as sidebarCollapsed) so a collapsed
// dock survives restart. maximized/preview stay session-local — they are view
// layout, not a durable preference. Transient geometry (drag flags, live drag
// widths, the sidebar button-press flag) is session-local state on this store
// so resize lifecycles and their consumers read one source of truth; measured
// footer height and viewport width live in the windowChrome store.
export type RightDockMode = "context" | "files" | "changed" | "remote" | "browser" | "trajectory";

// terminalPanelOpen is independent from rightDockMode — the terminal is a
// bottom drawer that coexists with the workspace panel, not a mode of it.
// Persisted to localStorage so it survives restart.
const TERMINAL_PANEL_OPEN_KEY = "reasonix.terminalPanel.open";
const TERMINAL_PANEL_DEFAULT_OPEN = false;

function loadTerminalPanelOpen(): boolean {
  if (typeof window === "undefined") return TERMINAL_PANEL_DEFAULT_OPEN;
  try {
    return window.localStorage.getItem(TERMINAL_PANEL_OPEN_KEY) === "1";
  } catch {
    return TERMINAL_PANEL_DEFAULT_OPEN;
  }
}

export function saveTerminalPanelOpen(open: boolean): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(TERMINAL_PANEL_OPEN_KEY, open ? "1" : "0");
  } catch {
    /* ignore storage failures */
  }
}

// Terminal height defaults and clamps for the bottom drawer.
export const TERMINAL_DEFAULT_HEIGHT = 280;
export const TERMINAL_MIN_HEIGHT = 120;
export const TERMINAL_MAX_HEIGHT_RATIO = 0.5; // max 50% of viewport height

const TERMINAL_HEIGHT_KEY = "reasonix.terminalPanel.height";

function loadTerminalHeight(): number {
  if (typeof window === "undefined") return TERMINAL_DEFAULT_HEIGHT;
  try {
    const raw = window.localStorage.getItem(TERMINAL_HEIGHT_KEY);
    if (raw === null) return TERMINAL_DEFAULT_HEIGHT;
    const parsed = Number(raw);
    if (Number.isFinite(parsed) && parsed >= TERMINAL_MIN_HEIGHT) return parsed;
    return TERMINAL_DEFAULT_HEIGHT;
  } catch {
    return TERMINAL_DEFAULT_HEIGHT;
  }
}

export function saveTerminalHeight(height: number): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(TERMINAL_HEIGHT_KEY, String(Math.round(height)));
  } catch {
    /* ignore storage failures */
  }
}

export function terminalMaxHeight(viewportHeight: number): number {
  return Math.max(TERMINAL_MIN_HEIGHT, Math.floor(Math.max(0, viewportHeight) * TERMINAL_MAX_HEIGHT_RATIO));
}

export function clampTerminalHeight(height: number, viewportHeight: number): number {
  const max = terminalMaxHeight(viewportHeight);
  return Math.min(max, Math.max(TERMINAL_MIN_HEIGHT, Math.round(height)));
}

function workspacePanelOpenStorageKey(workspaceRoot: string): string {
  return workspaceRoot ? `${WORKSPACE_PANEL_OPEN_KEY}.${workspaceRoot}` : WORKSPACE_PANEL_OPEN_KEY;
}

export function loadWorkspacePanelOpen(workspaceRoot: string): boolean {
  if (typeof window === "undefined") return WORKSPACE_PANEL_DEFAULT_OPEN;
  try {
    const raw = window.localStorage.getItem(workspacePanelOpenStorageKey(workspaceRoot));
    if (raw !== null) return raw !== "0";
    // Migration: the legacy single global key predates per-project keys.
    // When a project has no stored preference yet, seed it from the old
    // global value so an upgrade does not flip a user's existing choice
    // (e.g. they had the dock closed; first open of any project stays closed).
    const legacyRaw = window.localStorage.getItem(WORKSPACE_PANEL_OPEN_KEY);
    if (legacyRaw !== null) return legacyRaw !== "0";
    return WORKSPACE_PANEL_DEFAULT_OPEN;
  } catch {
    return WORKSPACE_PANEL_DEFAULT_OPEN;
  }
}

export function saveWorkspacePanelOpen(open: boolean, workspaceRoot = ""): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(workspacePanelOpenStorageKey(workspaceRoot), open ? "1" : "0");
  } catch {
    /* ignore storage failures */
  }
}

export type LayoutState = {
  sidebarCollapsed: boolean;
  sidebarWidth: number;
  rightDockTreeWidth: number;
  rightDockPreviewWidth: number;
  workspacePanelOpen: boolean;
  workspacePanelMaximized: boolean;
  workspacePreviewActive: boolean;
  rightDockMode: RightDockMode;
  terminalPanelOpen: boolean;
  terminalHeight: number;
  sidebarTogglePressed: boolean;
  sidebarResizing: boolean;
  liveSidebarWidth: number | null;
  workspacePanelResizing: boolean;
  liveWorkspacePanelRenderWidth: number | null;
  liveTerminalHeight: number | null;
  setSidebarCollapsed: (collapsed: boolean) => void;
  setSidebarWidth: (width: number) => void;
  setRightDockTreeWidth: (width: number) => void;
  setRightDockPreviewWidth: (width: number) => void;
  setWorkspacePanelOpen: Dispatch<SetStateAction<boolean>>;
  setWorkspacePanelMaximized: Dispatch<SetStateAction<boolean>>;
  setWorkspacePreviewActive: Dispatch<SetStateAction<boolean>>;
  setRightDockMode: Dispatch<SetStateAction<RightDockMode>>;
  setTerminalPanelOpen: Dispatch<SetStateAction<boolean>>;
  setTerminalHeight: (height: number) => void;
  setSidebarTogglePressed: (pressed: boolean) => void;
  setSidebarResizing: (resizing: boolean) => void;
  setLiveSidebarWidth: (width: number | null) => void;
  setWorkspacePanelResizing: (resizing: boolean) => void;
  setLiveWorkspacePanelRenderWidth: (width: number | null) => void;
  setLiveTerminalHeight: (height: number | null) => void;
};

export const useLayoutStore = create<LayoutState>((set) => ({
  sidebarCollapsed: loadSidebarCollapsed(),
  sidebarWidth: loadSidebarWidth(),
  rightDockTreeWidth: loadRightDockTreeWidth(),
  rightDockPreviewWidth: loadRightDockPreviewWidth(),
  workspacePanelOpen: loadWorkspacePanelOpen(""),
  workspacePanelMaximized: false,
  workspacePreviewActive: false,
  rightDockMode: "context",
  terminalPanelOpen: loadTerminalPanelOpen(),
  terminalHeight: loadTerminalHeight(),
  sidebarTogglePressed: false,
  sidebarResizing: false,
  liveSidebarWidth: null,
  workspacePanelResizing: false,
  liveWorkspacePanelRenderWidth: null,
  liveTerminalHeight: null,
  setSidebarCollapsed: (collapsed) => set({ sidebarCollapsed: collapsed }),
  setSidebarWidth: (width) => set({ sidebarWidth: width }),
  setRightDockTreeWidth: (width) => set({ rightDockTreeWidth: width }),
  setRightDockPreviewWidth: (width) => set({ rightDockPreviewWidth: width }),
  setWorkspacePanelOpen: (update) => set((s) => ({ workspacePanelOpen: applySetState(s.workspacePanelOpen, update) })),
  setWorkspacePanelMaximized: (update) => set((s) => ({ workspacePanelMaximized: applySetState(s.workspacePanelMaximized, update) })),
  setWorkspacePreviewActive: (update) => set((s) => ({ workspacePreviewActive: applySetState(s.workspacePreviewActive, update) })),
  setRightDockMode: (update) => set((s) => ({ rightDockMode: applySetState(s.rightDockMode, update) })),
  setTerminalPanelOpen: (update) => set((s) => ({ terminalPanelOpen: applySetState(s.terminalPanelOpen, update) })),
  setTerminalHeight: (height) => set({ terminalHeight: height }),
  setSidebarTogglePressed: (pressed) => set({ sidebarTogglePressed: pressed }),
  setSidebarResizing: (resizing) => set({ sidebarResizing: resizing }),
  setLiveSidebarWidth: (width) => set({ liveSidebarWidth: width }),
  setWorkspacePanelResizing: (resizing) => set({ workspacePanelResizing: resizing }),
  setLiveWorkspacePanelRenderWidth: (width) => set({ liveWorkspacePanelRenderWidth: width }),
  setLiveTerminalHeight: (height) => set({ liveTerminalHeight: height }),
}));

export function applyLayoutStyleDefaults(style: "classic" | "workbench" | "creation"): void {
  const state = useLayoutStore.getState();
  if (loadOptionalLayoutSize("sidebarWidthGraphite", clampStoredSidebarWidth) === null) {
    state.setSidebarWidth(style === "creation" ? defaultCreationSidebarWidth() : defaultSidebarWidth());
  }
  if (loadOptionalLayoutSize("rightDockTreeWidth", clampStoredRightDockTreeWidth) === null) {
    state.setRightDockTreeWidth(style === "creation" ? defaultCreationRightDockTreeWidth() : defaultRightDockTreeWidth());
  }
}
