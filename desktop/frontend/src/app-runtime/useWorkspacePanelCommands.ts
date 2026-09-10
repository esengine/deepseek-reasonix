import { useCallback, useEffect, useLayoutEffect, useState } from "react";
import { useCommittedCommand } from "../lib/useCommittedCommand";
import { DOCK_ENTRIES } from "../lib/dockEntries";
import { resolveLauncherCardState, type SpaceMode } from "../lib/launcherCardState";
import type { Translator } from "../lib/i18n";
import { loadWorkspacePanelOpen, saveWorkspacePanelOpen, useLayoutStore, type RightDockMode } from "../store/layout";
import { useActivityBarStore, type TabType } from "../store/activityBar";
import { useRemoteStore } from "../store/remote";

type Input = {
  workspaceRoot: string;
  creation: boolean;
  visible: boolean;
  closeOverlays: () => void;
  clearLiveWidth: (width: null) => void;
  availableWidth: number;
  clampTreeWidth: (width: number, availableWidth: number) => number;
  setTreeWidth: (width: number) => void;
  /** True while the dock column occupies grid space: the card then overlays
   *  the transcript instead of taking layout space from it. */
  gridOpen: boolean;
  t: Translator;
};

// The dock's tab model is the source of truth for what the panel shows; the
// legacy rightDockMode enum stays in sync for the geometry/shell readers that
// still branch on it.
function dockModeForTab(type: TabType): RightDockMode {
  switch (type) {
    case "context": return "context";
    case "changed": return "changed";
    case "remote": return "remote";
    case "browser": return "browser";
    case "trajectory": return "trajectory";
    default: return "files";
  }
}

function tabForDockMode(mode: RightDockMode): TabType {
  switch (mode) {
    case "context": return "context";
    case "changed": return "changed";
    case "remote": return "remote";
    case "browser": return "browser";
    case "trajectory": return "trajectory";
    default: return "file";
  }
}

// Remote and browser tabs have no launcher entry, so they carry their own
// label keys; every other tab type is labelled by its DOCK_ENTRIES entry.
function labelKeyForTab(type: TabType): string {
  if (type === "remote") return "rightDock.remote";
  if (type === "browser") return "rightDock.browser";
  return DOCK_ENTRIES.find(entry => entry.defaultTab === type)?.labelKey ?? "workspace.filesTab";
}

/** One project-scoped preference owner, with no mirrored layout state. */
export function useWorkspacePanelCommands(input: Input) {
  const t = input.t;
  const mode = useLayoutStore(state => state.rightDockMode);
  const explorerOpen = useRemoteStore(state => state.explorerOpen);
  const hostCount = useRemoteStore(state => state.hosts.length);
  const activeTabType = useActivityBarStore(state => state.tabs.find(tab => tab.id === state.activeTabId)?.type);
  // The floating launcher card is on screen only while the dock column is
  // collapsed, the surface is wide enough and the user has not dismissed it —
  // one shared decision for the card's render condition and the toggle's
  // pressed state.
  const [launcherDismissed, setLauncherDismissed] = useState(false);
  const [launcherSpaceMode, setLauncherSpaceMode] = useState<SpaceMode>("full");
  // The narrow-surface yield only matters while the card takes layout space
  // from the chat column; over an open dock it overlays and never squeezes.
  const launcherCardSpaceMode = input.gridOpen ? "full" : launcherSpaceMode;
  const launcherCard = resolveLauncherCardState({ spaceMode: launcherCardSpaceMode, dismissed: launcherDismissed });
  // The card stays mounted whenever it could be shown, even while the space
  // mode hides it: only the mounted card measures its host, so unmounting on
  // "hidden" would leave the toggle permanently unable to bring it back.
  const launcherCardMounted = !launcherDismissed;
  // Opening the panel replaces the card on screen; the toggle can still summon
  // it back over the panel afterwards.
  useEffect(() => {
    if (input.gridOpen) setLauncherDismissed(true);
  }, [input.gridOpen]);
  const openRightDockMode = useCommittedCommand((requestedMode?: RightDockMode) => {
    input.closeOverlays();
    const layout = useLayoutStore.getState();
    const next = requestedMode ?? layout.rightDockMode;
    if (next === "context" || next !== layout.rightDockMode) layout.setWorkspacePreviewActive(false);
    layout.setRightDockMode(next);
    layout.setWorkspacePanelMaximized(false);
    // The tab list decides what the dock renders, so every caller of this
    // command must leave a matching tab behind — otherwise the panel opens
    // empty (the tab model has no "mode" to fall back to).
    useActivityBarStore.getState().openEntry(tabForDockMode(next), t(labelKeyForTab(tabForDockMode(next)) as never));
    if (layout.workspacePanelOpen && !layout.workspacePanelMaximized) return;
    layout.setWorkspacePanelOpen(true);
    saveWorkspacePanelOpen(true, input.workspaceRoot);
  });
  const closeWorkspacePanel = useCommittedCommand(() => {
    input.closeOverlays();
    const layout = useLayoutStore.getState();
    if (!layout.workspacePanelOpen) return;
    input.clearLiveWidth(null);
    layout.setWorkspacePanelMaximized(false);
    layout.setWorkspacePanelOpen(false);
    saveWorkspacePanelOpen(false, input.workspaceRoot);
  });
  // The toggle owns the card and nothing else — the dock panel has its own
  // button. It is inert only while the surface is too narrow for the card to
  // occupy space at all.
  const toggleLauncherCard = useCallback(() => {
    if (launcherCardSpaceMode === "hidden") return;
    setLauncherDismissed((dismissed) => !dismissed);
  }, [launcherCardSpaceMode]);
  const prepareBlankWorkspace = useCommittedCommand((workspaceRoot = input.workspaceRoot) => {
    input.closeOverlays();
    input.clearLiveWidth(null);
    const layout = useLayoutStore.getState();
    layout.setWorkspacePanelMaximized(false);
    layout.setWorkspacePanelOpen(false);
    // Seed the destination preference before project restoration can run.
    saveWorkspacePanelOpen(false, workspaceRoot);
  });
  // Opening a launcher entry expands the dock to that entry's tab.
  const openDockEntry = useCommittedCommand((entryId: string) => {
    const entry = DOCK_ENTRIES.find(candidate => candidate.id === entryId);
    if (!entry) return;
    openRightDockMode(dockModeForTab(entry.defaultTab));
  });
  // Plain open/close: expanding restores whatever tabs the project had and
  // leaves an empty dock to the tab picker instead of seeding a view the user
  // did not ask for. openRightDockMode stays the "open this view" command.
  const toggleWorkspacePanel = useCommittedCommand(() => {
    const layout = useLayoutStore.getState();
    if (layout.workspacePanelOpen) { closeWorkspacePanel(); return; }
    input.closeOverlays();
    const activity = useActivityBarStore.getState();
    if (activity.tabs.length > 0) activity.setActivityBarOpen(true);
    layout.setWorkspacePanelMaximized(false);
    layout.setWorkspacePanelOpen(true);
    saveWorkspacePanelOpen(true, input.workspaceRoot);
  });
  const toggleWorkspaceMaximized = useCommittedCommand(() => {
    input.closeOverlays();
    const layout = useLayoutStore.getState();
    layout.setWorkspacePanelMaximized(!layout.workspacePanelMaximized);
  });
  const handleWorkspacePreviewModeChange = useCommittedCommand((active: boolean) => {
    const layout = useLayoutStore.getState();
    if (layout.workspacePreviewActive === active) return;
    input.closeOverlays();
    layout.setWorkspacePreviewActive(active);
  });
  const openRemoteDock = useCommittedCommand(() => {
    const remote = useRemoteStore.getState();
    const fallback = remote.hosts.find(host => ["connected", "degraded"].includes(remote.statuses[host.id]?.state)) ?? remote.hosts[0];
    const hostId = remote.hosts.some(host => host.id === remote.explorerHostId) ? remote.explorerHostId : fallback?.id;
    if (hostId) remote.openExplorer(hostId);
  });
  const restoreWorkspaceDockWidths = useCommittedCommand((treeWidth: number, _previewWidth: number) => {
    // Single-width dock: only the tree width is meaningful; clamp it to the
    // dynamic available width (chat keeps its 400px floor), never a fixed
    // 560 ceiling, so the user's remembered width is preserved when reopened.
    input.setTreeWidth(input.clampTreeWidth(treeWidth, input.availableWidth));
  });
  useLayoutEffect(() => {
    useLayoutStore.getState().setWorkspacePanelOpen(loadWorkspacePanelOpen(input.workspaceRoot));
  }, [input.workspaceRoot]);
  useLayoutEffect(() => {
    if (input.creation && mode === "context") useLayoutStore.getState().setRightDockMode("files");
  }, [input.creation, mode]);
  // Keep the legacy mode mirror on the active tab's type.
  useEffect(() => {
    if (!activeTabType) return;
    const next = dockModeForTab(activeTabType);
    if (useLayoutStore.getState().rightDockMode !== next) useLayoutStore.getState().setRightDockMode(next);
  }, [activeTabType]);
  // The tab list is per workspace root; switching projects restores that
  // project's own tabs instead of carrying the previous one's over.
  useLayoutEffect(() => {
    useActivityBarStore.getState().setWorkspaceRoot(input.workspaceRoot);
  }, [input.workspaceRoot]);
  useEffect(() => {
    if (!explorerOpen) return;
    openRightDockMode("remote");
    useRemoteStore.getState().closeExplorer();
  }, [explorerOpen, openRightDockMode]);
  useEffect(() => {
    if (hostCount === 0 && mode === "remote") useLayoutStore.getState().setRightDockMode("files");
  }, [hostCount, mode]);
  return {
    openRightDockMode, closeWorkspacePanel, prepareBlankWorkspace, openDockEntry,
    toggleWorkspacePanel, toggleWorkspaceMaximized, handleWorkspacePreviewModeChange,
    openRemoteDock, restoreWorkspaceDockWidths,
    launcherCard, launcherCardMounted, launcherCardOverlay: input.gridOpen,
    toggleLauncherCard, setLauncherSpaceMode,
  };
}
