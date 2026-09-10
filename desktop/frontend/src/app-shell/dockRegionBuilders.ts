import { workspacePanelAriaMinWidth } from "../lib/workspaceLayout";
import type { Translator } from "../lib/i18n";
import type { Meta, RemoteHostView, RemoteConnectionStatus, WireCompletionSummary } from "../lib/types";
import type { projectConversation } from "../app-runtime/conversationProjection";
import type { useShellGeometry } from "../app-runtime/useShellGeometry";
import type { useWorkspacePanelCommands } from "../app-runtime/useWorkspacePanelCommands";
import type { useComposerInsertCommands } from "../app-runtime/useComposerInsertCommands";
import type { WorkspaceVerificationRevealRequest } from "../components/WorkspacePanel";
import type { WorkspaceDockRegionProps } from "./WorkspaceDockRegion";
import type { AppBottomRegionsProps } from "./AppBottomRegions";
import type { ComposerProfile } from "../lib/composerProfile";
import type { RightDockMode } from "../store/layout";
import { defaultCreationRightDockTreeWidth, defaultRightDockTreeWidth, TERMINAL_DEFAULT_HEIGHT, TERMINAL_MIN_HEIGHT } from "../store/layout";

type ShellGeometry = ReturnType<typeof useShellGeometry>;
type WorkspacePanelApi = ReturnType<typeof useWorkspacePanelCommands>;
type InsertCommands = ReturnType<typeof useComposerInsertCommands>;
type ConversationView = ReturnType<typeof projectConversation>;
type StatusBarProps = NonNullable<AppBottomRegionsProps["status"]>;

/** Pure prop assembly for the dock and bottom regions; all ownership and
 *  geometry stays in the caller's hooks, only the object literals moved. */

export function buildWorkspaceDockProps(input: {
  surface: { renderable: boolean; overlay: boolean; gridOpen: boolean };
  creation: boolean;
  showContext: boolean;
  remote: boolean;
  t: Translator;
  context: ConversationView["context"];
  sessionTurns: number;
  contextRefreshKey: number;
  workspaceKey: string;
  workspaceScopeKey: string;
  mode: RightDockMode;
  meta: Meta | null | undefined;
  tabId: string | undefined;
  completionSummary: WireCompletionSummary | undefined;
  turnStartAt: number;
  layout: { treeWidth: number; previewWidth: number; maximized: boolean };
  geometry: ShellGeometry;
  panels: WorkspacePanelApi;
  inserts: InsertCommands;
  verification: { verificationRevealRequest: WorkspaceVerificationRevealRequest | null; closeTurnResult?: () => void };
  qualityFloor: ComposerProfile["qualityFloor"];
  onFileTreeRefresh: () => void;
  onSessionRevertCommitted: WorkspaceDockRegionProps["workspace"]["onSessionRevertCommitted"];
  onOpenInTerminal: WorkspaceDockRegionProps["workspace"]["onOpenInTerminal"];
}): WorkspaceDockRegionProps {
  const { surface, geometry, panels } = input;
  const workspacePanelResetWidth = input.creation
    ? defaultCreationRightDockTreeWidth()
    : defaultRightDockTreeWidth();
  const workspacePanelResizeMinWidth = workspacePanelAriaMinWidth(geometry.workspacePanelMinWidth, geometry.workspacePanelRenderWidth);
  return {
    visible: surface.renderable,
    overlay: surface.overlay,
    mode: input.mode,
    creation: input.creation,
    showContext: input.showContext,
    t: input.t,
    onPickEntry: panels.openDockEntry,
    remote: { onClose: panels.closeWorkspacePanel },
    trajectory: { tabId: input.tabId, t: input.t },
    context: {
      ...input.context, sessionTurns: input.sessionTurns,
      refreshKey: input.contextRefreshKey,
    },
    workspaceKey: input.workspaceKey,
    workspace: {
      open: surface.renderable, tabId: input.tabId, cwd: input.meta?.cwd,
      workspaceScopeKey: input.workspaceScopeKey, workspaceMemoryKey: input.workspaceKey,
      dockTreeWidth: input.layout.treeWidth, dockPreviewWidth: input.layout.previewWidth,
      onRestoreDockWidths: panels.restoreWorkspaceDockWidths, maximized: input.layout.maximized,
      panelWidth: geometry.workspacePanelRenderWidth, onClose: panels.closeWorkspacePanel,
      onToggleMaximized: panels.toggleWorkspaceMaximized,
      onPreviewModeChange: panels.handleWorkspacePreviewModeChange, onAddToChat: input.inserts.addWorkspaceTextToComposer,
      onAddCodeToChat: input.inserts.addWorkspaceCodeToComposer, onRequestPanelWidth: geometry.ensureWorkspacePanelWidth,
      onFileTreeRefresh: input.onFileTreeRefresh, onSessionRevertCommitted: input.onSessionRevertCommitted,
      onOpenInTerminal: input.onOpenInTerminal,
      initialViewMode: input.mode === "changed" ? "changed" : "files",
      completionSummary: input.completionSummary, turnStartAt: input.turnStartAt,
      sessionPath: input.meta?.sessionPath, onDismissTurnResult: input.verification.closeTurnResult,
      verificationRevealRequest: input.verification.verificationRevealRequest, qualityFloor: input.qualityFloor,
      showViewTabs: false, creationMode: input.creation,
    },
    resizer: surface.gridOpen ? {
      min: workspacePanelResizeMinWidth,
      max: Math.max(geometry.workspacePanelAvailableWidth, geometry.workspacePanelRenderWidth),
      value: geometry.workspacePanelRenderWidth,
      onPointerDown: geometry.startWorkspacePanelResize,
      onKeyDown: geometry.resizeWorkspacePanelWithKeyboard,
      onReset: () => geometry.setSavedWorkspacePanelWidth(workspacePanelResetWidth),
    } : undefined,
  };
}

export function buildBottomRegionsProps(input: {
  t: Translator;
  chatSurfaceVisible: boolean;
  surfaceOpen: boolean;
  contentVisible: boolean;
  remote: boolean;
  readOnly: boolean;
  tabId: string | undefined;
  meta: Meta | null | undefined;
  fitEnabled: boolean;
  liveTerminalHeight: number | null;
  geometry: ShellGeometry;
  terminal: {
    onClose: () => void;
    onAddOutput: (sessionId: string) => void;
    onAddToChat: (text: string) => void;
  };
  status?: {
    base: ConversationView["status"];
    rewindCommitting: boolean;
    sessionTurns: number;
    labelStyle: StatusBarProps["labelStyle"];
    items: StatusBarProps["items"];
    extensionStatuses: StatusBarProps["extensionStatuses"];
    remoteHosts: RemoteHostView[];
    remoteStatuses: Record<string, RemoteConnectionStatus>;
    onCancelJob: StatusBarProps["onCancelJob"];
    onCancelRuntimeJob: StatusBarProps["onCancelRuntimeJob"];
    onRevealRuntime: StatusBarProps["onRevealRuntime"];
    onConnectRemote: StatusBarProps["onConnectRemote"];
    onDisconnectRemote: StatusBarProps["onDisconnectRemote"];
    onManageRemote: StatusBarProps["onManageRemote"];
    onOpenRemote: StatusBarProps["onOpenRemote"];
    onOpenRemoteWorkspace: StatusBarProps["onOpenRemoteWorkspace"];
  };
}): AppBottomRegionsProps {
  const { geometry, status } = input;
  return {
    terminal: {
      surfaceVisible: input.chatSurfaceVisible,
      open: input.surfaceOpen, contentVisible: input.contentVisible,
      remoteSurface: input.remote, t: input.t,
      panel: {
        tabId: input.tabId ?? "", cwd: input.meta?.cwd, readOnly: input.readOnly,
        open: input.surfaceOpen, fitEnabled: input.fitEnabled,
        onClose: input.terminal.onClose,
        onAddOutput: input.terminal.onAddOutput,
        onAddToChat: input.terminal.onAddToChat,
      },
      resizer: {
        min: TERMINAL_MIN_HEIGHT, max: geometry.terminalResizeMaxHeight,
        value: input.liveTerminalHeight ?? geometry.terminalRenderHeight,
        onPointerDown: geometry.startTerminalResize, onKeyDown: geometry.resizeTerminalWithKeyboard,
        onReset: () => geometry.setSavedTerminalHeight(TERMINAL_DEFAULT_HEIGHT),
      },
    },
    status: status ? {
      ...status.base,
      running: status.base.running || (!input.remote && status.rewindCommitting),
      onCancelJob: status.onCancelJob,
      onCancelRuntimeJob: status.onCancelRuntimeJob,
      onRevealRuntime: status.onRevealRuntime,
      sessionTurns: status.sessionTurns,
      labelStyle: status.labelStyle,
      items: status.items,
      extensionStatuses: status.extensionStatuses,
      onConnectRemote: status.onConnectRemote,
      onDisconnectRemote: status.onDisconnectRemote,
      onManageRemote: status.onManageRemote,
      onOpenRemote: status.onOpenRemote,
      onOpenRemoteWorkspace: status.onOpenRemoteWorkspace,
      remoteHosts: status.remoteHosts,
      remoteStatuses: status.remoteStatuses,
    } : undefined,
  };
}
