// activityBar owns the right dock's tab-container state: the open tabs, the
// active one, whether the container is expanded, and the + add-menu flag.
// Panel contents are rendered by the dock region (they need its props); this
// store only tracks which tab is open so the dock can switch between them.
//
// `activityBarOpen` records the expanded state (true while ≥1 tab is open);
// closing the last tab collapses the container.
//
// Tabs are persisted per workspace root to localStorage, so switching projects
// shows each one's own tabs. The add menu stays session-local.

import { create } from "zustand";

export type TabType = "file" | "changed" | "context" | "remote" | "browser" | "trajectory";

export interface TabItem {
  id: string;
  type: TabType;
  label: string;
  meta?: Record<string, unknown>;
  /** Epoch ms the tab was opened; absent on tabs restored from an older
   *  persisted snapshot, which then simply show no relative time. */
  openedAt?: number;
}

/** A tab the user closed, kept for the tab overview's reopen list. */
export interface ClosedTabRecord {
  tab: TabItem;
  closedAt: number;
}

const CLOSED_TAB_LIMIT = 10;

const STORAGE_KEY = "reasonix.dock.tabs";

// Tabs are scoped per project (workspace root), so switching projects shows
// each one's own open tabs. A root of "" falls back to the legacy global key.
let workspaceRoot = "";
const closedByProject = new Map<string, ClosedTabRecord[]>();
function storageKey(): string {
  return workspaceRoot ? `${STORAGE_KEY}.${workspaceRoot}` : STORAGE_KEY;
}

function nextTabId(): string {
  return `dock-tab-${crypto.randomUUID()}`;
}

function loadTabs(): { tabs: TabItem[]; activeTabId: string | null } {
  if (typeof window === "undefined") return { tabs: [], activeTabId: null };
  try {
    const raw = window.localStorage.getItem(storageKey());
    if (!raw) return { tabs: [], activeTabId: null };
    const parsed = JSON.parse(raw) as { tabs?: TabItem[]; activeTabId?: string | null };
    const tabs = Array.isArray(parsed.tabs) ? parsed.tabs : [];
    // Drop structurally invalid entries and duplicate ids (a persisted
    // snapshot may contain them from an earlier bug); duplicate ids would
    // make React report "two children with the same key".
    const seen = new Set<string>();
    const valid = tabs.filter((tab) => {
      if (!tab || typeof tab.id !== "string" || typeof tab.type !== "string") return false;
      if (seen.has(tab.id)) return false;
      seen.add(tab.id);
      return true;
    });
    return { tabs: valid, activeTabId: valid.some((tab) => tab.id === parsed.activeTabId) ? parsed.activeTabId ?? null : valid[valid.length - 1]?.id ?? null };
  } catch {
    return { tabs: [], activeTabId: null };
  }
}

function persist(tabs: TabItem[], activeTabId: string | null): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(storageKey(), JSON.stringify({ tabs, activeTabId }));
  } catch {
    /* ignore storage failures */
  }
}

const initial = loadTabs();

export type ActivityBarState = {
  workspaceRoot: string;
  tabs: TabItem[];
  activeTabId: string | null;
  /** True while the tab container is expanded (dock shows the panel, not just
   *  the 48px activity bar). Independent of tabs: collapsing via the toggle
   *  keeps the tabs so re-expanding restores them. */
  activityBarOpen: boolean;
  addMenuOpen: boolean;
  /** Most recently closed tabs, newest first. Session-local. */
  recentlyClosed: ClosedTabRecord[];
  /** Open the entry's default tab, switching to it when one of that type exists. */
  openEntry: (type: TabType, label: string, meta?: Record<string, unknown>) => void;
  /** Append a new tab of the given type and activate it. */
  addTab: (type: TabType, label: string, meta?: Record<string, unknown>) => void;
  closeTab: (tabId: string) => void;
  /** Re-open a tab from the recently-closed list. */
  reopenTab: (tabId: string) => void;
  activateTab: (tabId: string) => void;
  /** Move a tab so it lands on the left/right side of another tab. */
  moveTab: (fromId: string, toId: string, side: "left" | "right") => void;
  /** Collapse/expand the tab container without touching the tab list. */
  setActivityBarOpen: (open: boolean) => void;
  setAddMenuOpen: (open: boolean) => void;
  /** Switch the active project (workspace root): reloads that project's own
   *  persisted tabs. A root of "" falls back to the legacy global tabs. */
  setWorkspaceRoot: (root: string) => void;
};

export const useActivityBarStore = create<ActivityBarState>((set, get) => ({
  workspaceRoot,
  tabs: initial.tabs,
  activeTabId: initial.activeTabId,
  activityBarOpen: initial.tabs.length > 0,
  addMenuOpen: false,
  recentlyClosed: [],
  openEntry: (type, label, meta) =>
    set((state) => {
      const existing = state.tabs.find((tab) => tab.type === type);
      if (existing) {
        persist(state.tabs, existing.id);
        return { activeTabId: existing.id, activityBarOpen: true };
      }
      const tab: TabItem = { id: nextTabId(), type, label, meta, openedAt: Date.now() };
      const tabs = [...state.tabs, tab];
      persist(tabs, tab.id);
      return { tabs, activeTabId: tab.id, activityBarOpen: true };
    }),
  addTab: (type, label, meta) =>
    set((state) => {
      const tab: TabItem = { id: nextTabId(), type, label, meta, openedAt: Date.now() };
      const tabs = [...state.tabs, tab];
      persist(tabs, tab.id);
      return { tabs, activeTabId: tab.id, activityBarOpen: true };
    }),
  closeTab: (tabId) =>
    set((state) => {
      const index = state.tabs.findIndex((tab) => tab.id === tabId);
      if (index < 0) return state;
      const tabs = state.tabs.filter((tab) => tab.id !== tabId);
      const closed = state.tabs[index];
      const recentlyClosed = [{ tab: closed, closedAt: Date.now() }, ...state.recentlyClosed]
        .slice(0, CLOSED_TAB_LIMIT);
      let activeTabId = state.activeTabId;
      if (state.activeTabId === tabId) {
        // Fall back to the neighbor on the left, then the right, then null.
        activeTabId = tabs[index - 1]?.id ?? tabs[index]?.id ?? null;
      }
      persist(tabs, activeTabId);
      // Closing the last tab collapses the container back to the activity bar.
      return { tabs, activeTabId, recentlyClosed, activityBarOpen: tabs.length > 0 };
    }),
  reopenTab: (tabId) =>
    set((state) => {
      const record = state.recentlyClosed.find((entry) => entry.tab.id === tabId);
      if (!record) return state;
      if (state.tabs.some((tab) => tab.id === tabId)) return state;
      const tabs = [...state.tabs, record.tab];
      persist(tabs, tabId);
      return {
        tabs,
        activeTabId: tabId,
        activityBarOpen: true,
        recentlyClosed: state.recentlyClosed.filter((entry) => entry.tab.id !== tabId),
      };
    }),
  activateTab: (tabId) =>
    set((state) => {
      if (!state.tabs.some((tab) => tab.id === tabId)) return state;
      persist(state.tabs, tabId);
      return { activeTabId: tabId, activityBarOpen: true };
    }),
  moveTab: (fromId, toId, side) =>
    set((state) => {
      if (fromId === toId) return state;
      const tabs = [...state.tabs];
      const fromIndex = tabs.findIndex((tab) => tab.id === fromId);
      if (fromIndex < 0) return state;
      const [moved] = tabs.splice(fromIndex, 1);
      const toIndex = tabs.findIndex((tab) => tab.id === toId);
      if (toIndex < 0) return state;
      tabs.splice(side === "right" ? toIndex + 1 : toIndex, 0, moved);
      persist(tabs, state.activeTabId);
      return { tabs };
    }),
  setActivityBarOpen: (open) => set({ activityBarOpen: open }),
  setAddMenuOpen: (open) => set({ addMenuOpen: open }),
  setWorkspaceRoot: (root) => {
    if (root === workspaceRoot) return;
    closedByProject.set(workspaceRoot, get().recentlyClosed);
    workspaceRoot = root;
    const loaded = loadTabs();
    set({
      workspaceRoot: root,
      tabs: loaded.tabs,
      activeTabId: loaded.activeTabId,
      activityBarOpen: loaded.tabs.length > 0,
      addMenuOpen: false,
      recentlyClosed: closedByProject.get(root) ?? [],
    });
  },
}));
