import { mockProviderTemplate, mockPreset, mockBundlePreset, mockKimiAPIModels, mockLongCatModels, mockTokenRhythmModels, mockTokenRhythmModelOverrides, mockMiMoV25Models, mockMiniMaxModels, mockGLMAPIModels, mockGLMCodingModels, mockGLMAnthropicModels, mockQwenAPIModels, mockQwenPlanModels, mockQwenPlanVisionModels, mockStepFunModels, mockOpenCodeGoModels, mockNovitaModels, mockGMIModels, mockVercelModels, mockOllamaCloudModels } from "./mockProviderTemplates";
// Wails and the browser mock share this React-to-Go contract.
// @ts-ignore generated locally; fresh checkouts use the disabled drift check below.
import type * as GeneratedApp from "../../wailsjs/go/main/App";
import type { InvocationRequest } from "./invocationDisplay";
import { addBreadcrumb } from "./breadcrumbs";
import { maybeShare } from "./queryCoalesce";
import { makeMockSessionCatalogBindings } from "./sessionCatalogBridge";
import { makeMockHistoryCatalogBindings, type HistoryCatalogBindings } from "./historyCatalogBridge";
import { makeMockTaskCatalogBindings, type TaskCatalogBindings } from "./taskCatalogBridge";
import { makeMockBlankProjectBindings, type BlankProjectBindings } from "./blankProjectBridge";
import { makeMockQualityFloorBindings, type QualityFloorBindings } from "./deliveryFloorBridge";
import { t } from "./i18n";
import { makeMockForkBindings } from "./forkWorktree";
import { makeMockWorktreeMergeBindings } from "./worktreeMergeMock";
import { providerIsConfigured, providerRequiresKey, removeProviderAccessesForMock } from "./providerModels";
import { DEFAULT_STATUS_BAR_ITEMS } from "./statusBarItems";
import { registerTrustedThemeBackgroundURLs } from "./themePack";
import { modeHasAutoApproveTools, modeWithAutoApproveTools, modeWithPlan, normalizeCollaborationMode, normalizeMode, normalizeToolApprovalMode } from "./types";
import { makeMockProjectTreeOrganizationBindings } from "./mockProjectTreeOrganization";
import { decisionSurfaceMockFromInput, isLongDecisionOptionsMockInput } from "./decisionSurfaceMock";
import { mockWorkspaceFile } from "./mockWorkspaceFile";
import { mockAIRenameSession, type SessionTitleBindings } from "./mockSessionTitle";
import { mockHistoryContentField, mockHistorySlice } from "./bridgeHistoryFixtures";
import { createMockModelScopePreset, type MockProviderPresetTemplate } from "./mockModelScopePreset";
import { createMockRemoteProjects } from "./mockRemoteProjects";
import { mockRemoteHostView } from "./mockRemoteHosts";
import type { RemoteProjectBindings } from "./remoteProjectBridge";
import type { ScrollDiagnosticBindings } from "./scrollDiagnosticBridge";
import { makeMockMCPAppBindings, type MCPAppBindings } from "./mcpAppBridge";
import { makeMockPinnedContextBindings, type PinnedContextBindings } from "./pinnedContextBridge";
import { createDesktopPreferencesMock } from "./desktopPreferencesMock";
import type {
  RemoteHostView,
  RemoteHostInput,
  RemoteConnectionStatus,
  RemoteDirEntry,
  RemoteFilePreview,
  RemoteWriteResult,
  RemoteForwardInput,
  RemoteForwardView,
  RemoteServerView,
  RemoteForwardsEvent,
  RemoteLegacyWorkbenchData,
  BalanceInfo,
  UsageStatsRange,
  UsageStatsRequest,
  BotConnectionDiagnostic,
  BotInstallPollResult,
  BotInstallStartResult,
  BotRuntimeStatusView,
  BotSettingsView,
  CapabilitiesView,
  CapabilityDiagnosticsReport,
  RuntimeDoctorReport,
  CheckpointMeta,
  CommandInfo,
  ControlResult,
  ContextInfo,
  ContextPanelInfo,
  DirEntry,
  DesktopStartupSettingsView,
  DeliveryWorktreeAvailability,
  DeliveryWorktreeOpenResult,
  WorktreeMergeInspection,
  WorktreeMergeRequest,
  WorktreeMergeResult,
  WorktreeCleanupRequest,
  WorktreeCleanupResult,
  CloseMergedWorktreeTabRequest,
  CloseMergedWorktreeTabResult,
  DroppedItem,
  EffortInfo,
  ExtensionActionView,
  FilePreview,
  ExternalOpenersView,
  HistoryMessage,
  HistoryPage,
  HistoryContentChunk,
  HistoryContentRef,
  HistorySlice,
  HistorySliceRequest,
  TabMetaRefreshEvent,
  TopicActivationEvent,
  TopicActivationRequest,
  TopicActivationTicket,
  HookConfigView,
  HooksSettingsView,
  JobView,
  ActiveWorkView,
  BackgroundRuntimeView,
  JobCancelBatchView,
  WorkspaceConflictView,
  MCPMarketplaceEntry,
  MCPServerInput,
  MCPInstallResult,
  MCPMarketplaceView,
  MCPToolView,
  MemoryFact,
  MemorySuggestion,
  MemorySuggestionsView,
  MemoryView,
  Meta,
  Mode,
  ModelInfo,
  NetworkView,
  PluginInstallOptions,
  PluginView,
  ProjectNode,
  ProjectTreeOrganizationBindings,
  RecoveryLineageView,
  RecoveryCleanupRequest,
  RecoveryCleanupResult,
  SessionCatalogBindings,
  PromptHistoryEntry,
  PromptHistoryResult,
  ProviderModelCapabilityView,
  ProviderModelCatalogUpdate,
  ProviderPresetView,
  ProviderView,
  QuestionAnswer,
  ServerView,
  SessionMeta,
  SessionRecoveryFailedEvent,
  SessionRecoveryEvent,
  SettingsView,
  ShellInstallResult,
  SkillsSettingsView,
  SkillRootView,
  SkillSuggestion,
  SkillView,
  TaskEvent,
  TaskSnapshot,
  SlashArgsResult,
  SubagentProfileInput,
  TabMeta,
  TerminalSessionView,
  TerminalWorkspaceView,
  TopicMeta,
  ToolApprovalMode,
  TurnEventReplayView,
  UpdateInfo,
  UpdateProgress,
  WireEvent,
  WorkspaceChangeDetailView,
  WorkspaceChangesView,
  WorkspaceRevisions,
  GitCommitView,
  GitCommitDetailView,
  WorkspaceView,
  SessionClearResult,
} from "./types";
import { browserPreviewShellSupport } from "./shellSupportPreview";
export * from "./remoteTabEvents";
export const COMPACT_RATIO_MIN_PERCENT = 30, COMPACT_RATIO_MAX_PERCENT = 85;

export interface DesktopShellStatusView {
  trayState: "probing" | "ready" | "unavailable";
  backgroundCloseAvailable: boolean;
  reason?: string;
}
import type { MarkdownImageView } from "./markdownImage";
const GLOBAL_PROJECT_ORDER_KEY = "__global__";

function stripLegacyGoalBudgetFlags(arg: string): string {
  const parts = arg.trim().split(/\s+/).filter(Boolean);
  while (parts.length > 0) {
    const flag = parts[0].toLowerCase();
    if (flag !== "--research" && flag !== "--auto-research" && flag !== "--deep" && flag !== "--simple" && flag !== "--no-research") break;
    parts.shift();
  }
  return parts.join(" ");
}

// AppBindings is derived from the Wails-generated Go → TS method signatures, so
// the compiler catches drift between the Go binding surface and the frontend mock.
// Run `wails generate module` after adding/renaming a bound method on App, then
// `pnpm typecheck` to verify the mock still satisfies the contract.
//
// Types for native-feel bindings, used only by AppBindings and the dev mock.
interface NativeConfirmRequest {
  title: string;
  message: string;
  detail: string;
  confirmLabel: string;
  cancelLabel: string;
  destructive: boolean;
}

interface DesktopWindowState {
  width: number;
  height: number;
  x: number;
  y: number;
  maximised: boolean;
}
// AppBindings is the hand-written React-to-Go contract. _CheckGeneratedBindings
// catches generated methods missing here; update this interface and typecheck.
export interface AppBindings extends SessionCatalogBindings, ProjectTreeOrganizationBindings, HistoryCatalogBindings, TaskCatalogBindings, BlankProjectBindings, QualityFloorBindings, SessionTitleBindings, ScrollDiagnosticBindings, RemoteProjectBindings, MCPAppBindings, PinnedContextBindings {
  Platform(): Promise<string>;
  MinimiseMainWindow(): Promise<void>;
  ToggleMaximiseMainWindow(): Promise<void>;
  IsMainWindowMaximised(): Promise<boolean>;
  CloseMainWindow(): Promise<void>;
  // ── Heartbeat ──
  HeartbeatListTasks(): Promise<unknown>;
  HeartbeatReloadTasks(): Promise<unknown>;
  HeartbeatSaveTasks(tasks: unknown): Promise<void>;
  HeartbeatReloadConfig(): Promise<unknown>;
  HeartbeatSaveConfig(update: unknown): Promise<unknown>;
  HeartbeatTriggerNow(id: string): Promise<void>;
  HeartbeatGenerateID(): Promise<string>;
  Submit(input: string): Promise<void>;
  SubmitToTab(tabID: string, input: string): Promise<void>;
  SubmitToTabWithID(tabID: string, input: string, submissionID: string): Promise<void>;
  StartTurnForTab?(tabID: string, input: string, submissionID: string): Promise<{ turnId: string; status: string; disposition?: "turn_started" | "management_handled"; runtimeEpoch?: string; submissionId?: string }>;
  SubmitDisplay(display: string, input: string): Promise<void>;
  SubmitDisplayToTab(tabID: string, display: string, input: string): Promise<void>;
  SubmitDisplayToTabWithID(tabID: string, display: string, input: string, submissionID: string): Promise<void>;
  SubmitDeliveryRecoveryToTab(tabID: string, display: string, input: string): Promise<void>;
  SubmitDeliveryRecoveryToTabWithID(tabID: string, display: string, input: string, submissionID: string): Promise<void>;
  SubmitInvocationsToTab(tabID: string, display: string, input: string, invocations: InvocationRequest[]): Promise<void>;
  SubmitInvocationsToTabWithID(tabID: string, display: string, input: string, invocations: InvocationRequest[], submissionID: string): Promise<void>;
  SubmitInitialGoalToTab(
    tabID: string,
    goal: string,
    display: string,
    input: string,
    invocations: InvocationRequest[],
    collaborationMode: string,
    toolApprovalMode: string,
  ): Promise<string[]>;
  SubmitInitialGoalToTabWithID(tabID: string, goal: string, display: string, input: string, invocations: InvocationRequest[], collaborationMode: string, toolApprovalMode: string, submissionID: string): Promise<string[]>;
  SubmitEditedDisplayToTab(tabID: string, display: string, input: string, original: string): Promise<void>;
  SubmitEditedDisplayToTabWithID(tabID: string, display: string, input: string, original: string, submissionID: string): Promise<void>;
  RunShell(command: string): Promise<void>;
  RunShellForTab(tabID: string, command: string): Promise<void>;
  Steer(text: string): Promise<void>;
  SteerForTab(tabID: string, text: string): Promise<void>;
  InboxSnapshot(tabID: string): Promise<{
    revision: number;
    paused: boolean;
    recovered: boolean;
    recoveredCount?: number; sessionPath?: string;
    items: Array<{
      id: string;
      intent: string;
      state: string;
      preview: string;
      byteSize: number;
      source?: string;
      position: number;
      blockReason?: string;
    }>;
    itemsCount: number;
    bytes: number;
    maxItems: number;
    maxBytes: number;
  }>;
  EnqueueInboxFollowup(tabID: string, display: string, submit: string, idempotency: string): Promise<{ itemId: string; disposition: string; position: number; paused: boolean; idempotent?: boolean; error?: string }>;
  EnqueueInboxFollowupWithInvocations(tabID: string, display: string, submit: string, invocations: InvocationRequest[], idempotency: string): Promise<{ itemId: string; disposition: string; position: number; paused: boolean; idempotent?: boolean; error?: string }>;
  EnqueueInboxSteer(tabID: string, display: string, submit: string, idempotency: string): Promise<{ itemId: string; disposition: string; position: number; paused: boolean; idempotent?: boolean; error?: string }>;
  EnqueueInboxSteerForTurn?(tabID: string, turnID: string, display: string, submit: string, idempotency: string): Promise<{ itemId: string; disposition: string; position: number; paused: boolean; idempotent?: boolean; error?: string }>;
  SteerInboxItem(tabID: string, itemID: string): Promise<{
    itemId: string; disposition: string; position: number; paused: boolean; idempotent?: boolean; error?: string;
  }>;
  SteerInboxItemForTurn?(tabID: string, turnID: string, itemID: string): Promise<{
    itemId: string; disposition: string; position: number; paused: boolean; idempotent?: boolean; error?: string;
  }>;
  ReadInboxItem(tabID: string, id: string): Promise<{ id: string; displayText: string; rawText: string; submitText: string }>;
  UpdateInboxItem(tabID: string, id: string, display: string, submit: string): Promise<void>;
  DeleteInboxItem(tabID: string, id: string): Promise<void>;
  MoveInboxItem(tabID: string, id: string, toIndex: number): Promise<void>;
  SetInboxPaused(tabID: string, paused: boolean): Promise<void>;
  RetryInboxItem(tabID: string, id: string): Promise<void>;
  RefreshInboxItem(tabID: string, id: string): Promise<void>;
  InboxHasItems(tabID: string): Promise<boolean>;
  Cancel(): Promise<void>;
  CancelTab(tabID: string): Promise<void>;
  CancelTabWithInboxItems(tabID: string, itemIDs: string[]): Promise<void>;
  CancelTabWithInboxItemsResult?(tabID: string, itemIDs: string[]): Promise<{ discardedItemIds: string[]; warning?: string }>;
  InterruptTurnForTab?(tabID: string, turnID: string): Promise<void>;
  InterruptTurnWithInboxItemsForTab?(tabID: string, turnID: string, itemIDs: string[]): Promise<{ discardedItemIds: string[]; warning?: string }>;
  TurnEventsForTab?(tabID: string, afterSeq: number): Promise<TurnEventReplayView>;
  Approve(id: string, allow: boolean, session: boolean, persist: boolean): Promise<void>;
  ApproveTab(tabID: string, id: string, allow: boolean, session: boolean, persist: boolean): Promise<void>;
  ApproveTabForTurn?(tabID: string, turnID: string, runtimeEpoch: string, id: string, allow: boolean, session: boolean, persist: boolean): Promise<void>;
  ResolvePlanDecision(id: string, action: "start_execution" | "revise_plan" | "exit_plan"): Promise<void>;
  ResolvePlanDecisionTab(tabID: string, id: string, action: "start_execution" | "revise_plan" | "exit_plan"): Promise<void>;
  ResolvePlanDecisionTabForTurn?(tabID: string, turnID: string, runtimeEpoch: string, id: string, action: "start_execution" | "revise_plan" | "exit_plan"): Promise<void>;
  ResolveRecovery(id: string, action: string, feedback: string): Promise<void>;
  ResolveRecoveryTab(tabID: string, id: string, action: string, feedback: string): Promise<void>;
  ResolveRecoveryTabForTurn?(tabID: string, turnID: string, runtimeEpoch: string, id: string, action: string, feedback: string): Promise<void>;
  SetRecoveryCheckpointEnabled(enabled: boolean): Promise<void>;
  SetRecoveryCheckpointEnabledTab(tabID: string, enabled: boolean): Promise<void>;
  RecoveryCheckpointEnabled(): Promise<boolean>;
  RecoveryCheckpointEnabledTab(tabID: string): Promise<boolean>;
  AnswerQuestion(id: string, answers: QuestionAnswer[]): Promise<void>;
  AnswerQuestionForTab(tabID: string, id: string, answers: QuestionAnswer[]): Promise<void>;
  AnswerMCPInteractionForTab(
    tabID: string,
    id: string,
    action: "accept" | "decline" | "cancel",
    content: Record<string, unknown> | null,
  ): Promise<void>;
  AnswerMCPInteractionForTurn?(tabID: string, turnID: string, runtimeEpoch: string, id: string, action: "accept" | "decline" | "cancel", content: Record<string, unknown> | null): Promise<void>;
  AnswerPromptForTab?(tabID: string, turnID: string, id: string, answers: QuestionAnswer[]): Promise<void>;
  ResolvePromptForTab?(tabID: string, promptID: string, turnID: string, runtimeEpoch: string, kind: string, answer: {
    questions?: QuestionAnswer[]; allow?: boolean; session?: boolean; persist?: boolean;
    action?: string; feedback?: string; content?: Record<string, unknown> | null;
  }): Promise<void>;
  PendingPromptIdentitiesForTab?(tabID: string): Promise<Array<{ promptId: string; turnId: string; runtimeEpoch?: string; kind: string }>>;
  ReplayPendingPromptIdentitiesForTab?(tabID: string): Promise<Array<{ promptId: string; turnId: string; runtimeEpoch?: string; kind: string }>>;
  ReplayPendingPrompts(): Promise<void>;
  ReplayPendingPromptsForTab(tabID: string): Promise<void>;
  SetPlanMode(on: boolean): Promise<void>;
  SetMode(mode: string): Promise<void>;
  // Returns auto-allowed prompt ids; unlisted prompts remain pending (#6432).
  SetModeForTab(tabID: string, mode: string): Promise<string[] | void>;
  SetAutoApproveTools(on: boolean): Promise<void>;
  SetCollaborationMode(mode: string): Promise<void>;
  SetCollaborationModeForTab(tabID: string, mode: string): Promise<void>;
  SetToolApprovalMode(mode: string): Promise<void>;
  // Same drained-prompt-id contract as SetModeForTab.
  SetToolApprovalModeForTab(tabID: string, mode: string): Promise<string[] | void>;
  // Atomically applies the controller-facing composer profile and reports any
  // approval prompts drained by the resulting tool-approval posture.
  SetComposerProfileForTab(tabID: string, collaborationMode: string, toolApprovalMode: string, goal: string): Promise<string[] | void>;
  SetGoal(goal: string): Promise<void>;
  SetGoalForTab(tabID: string, goal: string): Promise<void>;
  ResumeGoalForTab(tabID: string): Promise<boolean>;
  PauseGoalForTab(tabID: string): Promise<boolean>;
  ClearGoal(): Promise<void>;
  ClearGoalForTab(tabID: string): Promise<void>;
  Compact(): Promise<void>;
  CompactForTab(tabID: string): Promise<void>;
  NewSession(): Promise<void>;
  NewSessionForTab(tabID: string): Promise<void>;
  ClearSession(): Promise<SessionClearResult>;
  ClearSessionForTab(tabID: string): Promise<SessionClearResult>;
  History(): Promise<HistoryMessage[]>;
  HistoryForTab(tabID: string): Promise<HistoryMessage[]>;
  HistoryPage(beforeTurn: number, limit: number): Promise<HistoryPage>;
  HistoryPageForTab(tabID: string, beforeTurn: number, limit: number): Promise<HistoryPage>;
  // Windowed history paging (supersedes HistoryPageForTab for tab history).
  HistorySliceForTab(tabID: string, req: HistorySliceRequest): Promise<HistorySlice>;
  HistoryContentForTab(tabID: string, ref: HistoryContentRef, chunkIndex: number): Promise<HistoryContentChunk>;
  HistoryCheckpointTurnsForTab(tabID: string): Promise<number[]>;
  Checkpoints(): Promise<CheckpointMeta[]>;
  CheckpointsForTab(tabID: string): Promise<CheckpointMeta[]>;
  Rewind(turn: number, scope: string): Promise<void>;
  RewindForTab(tabID: string, turn: number, scope: string): Promise<void>;
  PreviewRewindForTab(tabID: string, turn: number, scope: string): Promise<import("./types").RewindPlanView>;
  CommitRewindForTab(tabID: string, planID: string, turn: number, scope: string): Promise<import("./types").RewindResultView>;
  UndoRewindForTab(tabID: string, transactionID: string): Promise<import("./types").RewindResultView>;
  PreviewWorkspaceFileRevertForTab(tabID: string, path: string): Promise<import("./types").RewindPlanView>;
  CommitWorkspaceFileRevertForTab(tabID: string, planID: string, resolution: string): Promise<import("./types").RewindResultView>;
  Fork(turn: number): Promise<TabMeta>;
  ForkForTab(tabID: string, turn: number): Promise<TabMeta>;
  ForkWorktreeForTab(tabID: string, turn: number): Promise<import("./forkWorktree").ForkWorktreeResultView>;
  SummarizeFrom(turn: number): Promise<void>;
  SummarizeFromForTab(tabID: string, turn: number): Promise<void>;
  SummarizeUpTo(turn: number): Promise<void>;
  SummarizeUpToForTab(tabID: string, turn: number): Promise<void>;
  ListSessions(): Promise<SessionMeta[]>;
  ListSessionsForTab(tabID: string): Promise<SessionMeta[]>;
  ListTrashedSessions(): Promise<SessionMeta[]>;
  ResumeSession(path: string): Promise<HistoryMessage[]>;
  ResumeSessionForTab(tabID: string, path: string): Promise<HistoryMessage[]>;
  ResumeSessionPage(path: string, limit: number): Promise<HistoryPage>;
  ResumeSessionPageForTab(tabID: string, path: string, limit: number): Promise<HistoryPage>;
  OpenChannelSessionForTab(tabID: string, path: string): Promise<HistoryMessage[]>;
  OpenChannelSessionPageForTab(tabID: string, path: string, limit: number): Promise<HistoryPage>;
  PreviewSession(path: string): Promise<HistoryMessage[]>;
  QuerySessionTakeover(tabId: string): Promise<import("./types").SessionTakeoverView | null>;
  TakeoverSession(tabId: string, mode: "wait" | "interrupt"): Promise<void>;
  DeleteSession(path: string): Promise<void>;
  DeleteRecoveryCopy(path: string): Promise<void>;
  GetRecoveryLineage(key: { scope: string; workspaceRoot?: string; topicId: string; path?: string; recordClassification?: boolean }): Promise<RecoveryLineageView>;
  GetSessionVersionState(key: { scope: string; workspaceRoot?: string; topicId: string; path?: string; recordClassification?: boolean }): Promise<import("./types").SessionVersionStateView>;
  SetActiveSessionVersion(request: import("./types").RecoveryPreferenceRequest): Promise<void>;
  RetrySessionRecovery(request: import("./types").RecoveryPreferenceRequest): Promise<void>;
  ReconcileRecoveryVersions(key: { scope: string; workspaceRoot?: string; topicId: string; path?: string }): Promise<void>;
  ChooseRecoveryBranch(request: import("./types").RecoveryPreferenceRequest): Promise<void>;
  CleanRecoveryLineage(request: RecoveryCleanupRequest): Promise<RecoveryCleanupResult>;
  RestoreSession(path: string): Promise<void>;
  PurgeTrashedSession(path: string): Promise<void>;
  PurgeRecoveryCopy(path: string): Promise<void>;
  RenameSession(path: string, title: string): Promise<void>;
  ScanPromptHistory(nonce: string): Promise<PromptHistoryResult>;
  ListWorkspaces(): Promise<WorkspaceView[]>;
  PickWorkspace(): Promise<string>;
  SwitchWorkspace(path: string): Promise<string>;
  RemoveWorkspace(path: string): Promise<void>;
  ContextUsage(): Promise<ContextInfo>;
  ContextUsageForTab(tabID: string): Promise<ContextInfo>;
  Balance(): Promise<BalanceInfo>;
  BalanceForTab(tabID: string): Promise<BalanceInfo>;
  UsageStats(req: UsageStatsRequest): Promise<UsageStatsRange>;
  Jobs(): Promise<JobView[]>;
  ListTasks(): Promise<TaskSnapshot[]>;
  CurrentTaskSessionID(): Promise<string>;
  ListTasksForSession(sessionID: string): Promise<TaskSnapshot[]>;
  GetTask(taskID: string): Promise<TaskSnapshot | null>;
  ListTaskEvents(taskID: string, afterSequence: number): Promise<TaskEvent[]>;
  StopTask(taskID: string, expectedVersion: number, reason: string, idemKey: string): Promise<ControlResult>;
  CancelTask(taskID: string, expectedVersion: number, reason: string, idemKey: string): Promise<ControlResult>;
  RequeueTask(taskID: string, expectedVersion: number, idemKey: string): Promise<ControlResult>;
  OpenTaskSession(taskID: string): Promise<ControlResult>;
  ListTasksForTab(tabID: string): Promise<TaskSnapshot[]>;
  ListTaskEventsForTab(tabID: string, taskID: string, afterSequence: number): Promise<TaskEvent[]>;
  StopTaskForTab(tabID: string, taskID: string, expectedVersion: number, reason: string, idemKey: string): Promise<ControlResult>;
  CancelTaskForTab(tabID: string, taskID: string, expectedVersion: number, reason: string, idemKey: string): Promise<ControlResult>;
  RequeueTaskForTab(tabID: string, taskID: string, expectedVersion: number, idemKey: string): Promise<ControlResult>;
  OpenTaskSessionForTab(tabID: string, taskID: string): Promise<ControlResult>;
  JobsForTab(tabID: string): Promise<JobView[]>;
  CancelJob(jobID: string): Promise<boolean>;
  CancelJobForTab(tabID: string, jobID: string): Promise<boolean>;
  CancelJobsForTab(tabID: string, jobIDs: string[]): Promise<JobCancelBatchView>;
  ActiveWorkForTab(tabID: string): Promise<ActiveWorkView>;
  BackgroundRuntimes(): Promise<BackgroundRuntimeView[]>;
  RevealBackgroundRuntime(tabID: string): Promise<TabMeta>;
  WorkspaceConflictForTab(tabID: string): Promise<WorkspaceConflictView>;
  RevealWorkspaceWriterForTab(tabID: string): Promise<TabMeta>;
  CloseTabWithPolicy(tabID: string, policy: "keep_running" | "stop_and_close"): Promise<void>;
  ToolResultForTab(tabID: string, toolID: string): Promise<{ args: string; output: string; execution?: import("./types").WireShellExecution; mcpApp?: import("./types").MCPAppPresentation } | null>;
  Meta(): Promise<Meta>;
  MetaForTab(tabID: string): Promise<Meta>; DismissTodoBatchForTab(tabID: string, batchKey: string): Promise<void>;
  Commands(): Promise<CommandInfo[]>;
  Capabilities(): Promise<CapabilitiesView>;
  MCPServers(): Promise<ServerView[]>;
  MCPCapabilityMatrix(): Promise<{
    views: Array<{ id: string; layer: string; state: string; negotiated: boolean; detail: string }>;
    hostProfile: string;
  }>;
  MCPMarketplace(query: string): Promise<MCPMarketplaceView>;
  MCPMarketplaceResolve(registryName: string): Promise<MCPMarketplaceEntry>;
  SkillsSettings(): Promise<SkillsSettingsView>;
  CapabilityDiagnostics(includeSessionRuntime: boolean): Promise<CapabilityDiagnosticsReport>;
  RuntimeDoctor(): Promise<RuntimeDoctorReport>;
  Plugins(): Promise<PluginView[]>;
  PlanPluginInstall(source: string, options: PluginInstallOptions): Promise<string>;
  InstallPlugin(source: string, options: PluginInstallOptions): Promise<string>;
  RemovePlugin(name: string): Promise<void>;
  SetPluginEnabled(name: string, enabled: boolean): Promise<void>;
  UpdatePlugin(name: string): Promise<string>;
  PluginDoctor(name: string): Promise<PluginView>;
  // Extension UI (stage 8b2): enumerate handshake-declared extension actions
  // for the command palette, invoke one, and deliver a form surface's values
  // (or {cancelled: true} on dismissal) back to the owning sidecar.
  ExtensionActions(tabID: string): Promise<ExtensionActionView[]>;
  InvokeExtensionAction(tabID: string, name: string, args: Record<string, string>): Promise<string>;
  SubmitExtensionForm(tabID: string, pluginID: string, surfaceID: string, values: Record<string, unknown>): Promise<void>;
  AddMCPServer(input: MCPServerInput): Promise<number>;
  InstallMCPServer(input: MCPServerInput): Promise<MCPInstallResult>;
  UpdateMCPServer(name: string, input: MCPServerInput): Promise<void>;
  RemoveMCPServer(name: string): Promise<void>;
  AuthorizeAndConnectMCPServer(name: string): Promise<void>;
  AuthenticateMCPServer(name: string): Promise<void>;
  ReconnectMCPServer(name: string): Promise<void>;
  ClearMCPServerAuthentication(name: string): Promise<void>;
  PickSkillFolder(): Promise<string>;
  PickPluginFolder(): Promise<string>;
  AddSkillPath(path: string): Promise<void>;
  RemoveSkillPath(path: string): Promise<void>;
  SetSkillPathEnabled(path: string, enabled: boolean): Promise<void>;
  RefreshSkills(): Promise<void>;
  ReloadCommands(): Promise<void>;
  SetSkillEnabled(name: string, enabled: boolean): Promise<void>;
  SetSkillImplicitInvocation(enabled: boolean): Promise<void>;
  AvailableSubagentTools(): Promise<MCPToolView[]>;
  CreateSubagentProfile(input: SubagentProfileInput): Promise<string>;
  UpdateSubagentProfile(name: string, scope: string, input: SubagentProfileInput): Promise<void>;
  DeleteSubagentProfile(name: string, scope: string): Promise<void>;
  SetSubagentProfileModel(name: string, ref: string): Promise<void>;
  SetSubagentProfileEffort(name: string, level: string): Promise<void>;
  TrySubagentProfile(input: SubagentProfileInput, task: string): Promise<string>;
  CancelTrySubagentProfile(): Promise<void>;
  SetMCPServerEnabled(name: string, enabled: boolean): Promise<void>;
  SetMCPServerTier(name: string, tier: string): Promise<void>;
  SlashArgs(input: string): Promise<SlashArgsResult>;
  ListDir(rel: string): Promise<DirEntry[]>;
  ListDirForTab(tabID: string, rel: string): Promise<DirEntry[]>;
  SearchFileRefs(query: string): Promise<DirEntry[]>;
  SearchFileRefsForTab(tabID: string, query: string): Promise<DirEntry[]>;
  ReadFile(rel: string): Promise<FilePreview>;
  ReadFileForTab(tabID: string, rel: string): Promise<FilePreview>;
  ResolveMarkdownImageForTab(tabID: string, source: string): Promise<MarkdownImageView>;
  WorkspaceRevisionForTab(tabID: string): Promise<{ revisions: WorkspaceRevisions; watchState: "active" | "degraded" | "unavailable" }>;
  WorkspaceChanges(tabID: string): Promise<WorkspaceChangesView>;
  WorkspaceChangeDetail(tabID: string, path: string): Promise<WorkspaceChangeDetailView>;
  GitBranches(): Promise<string[]>;
  GitCheckout(branch: string): Promise<void>;
  WorkspaceGitHistory(tabID: string, path: string): Promise<GitCommitView[]>;
  WorkspaceGitCommitDetail(tabID: string, hash: string, path: string): Promise<GitCommitDetailView>;
  OpenWorkspacePath(rel: string): Promise<void>;
  OpenWorkspacePathForTab(tabID: string, rel: string): Promise<void>;
  ResolveWorkspacePathForTab(tabID: string, rel: string): Promise<string>;
  ExternalOpeners(): Promise<ExternalOpenersView>; ExternalOpenersForTab(tabID: string): Promise<ExternalOpenersView>;
  SetPreferredExternalOpener(id: string): Promise<void>;
  OpenWorkspaceInExternalOpener(id: string): Promise<void>;
  OpenWorkspaceInExternalOpenerForTab(tabID: string, id: string): Promise<void>; OpenLocalPathInExternalOpener(path: string, id: string): Promise<void>; SaveLocalPathAs(path: string): Promise<string>;
  RevealWorkspacePath(rel: string): Promise<void>;
  RevealWorkspacePathForTab(tabID: string, rel: string): Promise<void>;
  RevealPath(path: string): Promise<void>;
  OpenLocalPath(path: string): Promise<void>;
  SavePastedImage(dataUrl: string): Promise<string>;
  SaveClipboardImage(): Promise<string>;
  SavePastedFile(name: string, dataUrl: string): Promise<string>;
  PickExportFile(defaultFilename: string, mimeType: string): Promise<string>;
  SaveExportFile(path: string, payload: string, base64Encoded: boolean): Promise<void>;
  SaveExportImageFiles(path: string, payloads: string[]): Promise<void>;
  AttachDropped(path: string): Promise<DroppedItem>;
  AttachmentDataURL(path: string): Promise<string>;
  Models(): Promise<ModelInfo[]>;
  SetModel(name: string): Promise<void>;
  ModelsForTab(tabID: string): Promise<ModelInfo[]>;
  SetModelForTab(tabID: string, name: string): Promise<void>;
  Effort(): Promise<EffortInfo>;
  SetEffort(level: string): Promise<void>;
  EffortForTab(tabID: string): Promise<EffortInfo>;
  SetEffortForTab(tabID: string, level: string): Promise<void>;
  // ReloadRuntime rebuilds the tab's agent runtime in place (tools, skills,
  // commands, hooks, providers, MCP servers) via boot.Rebuild, keeping the
  // session. Busy tabs queue one reload for when they go idle.
  ReloadRuntime(tabID: string): Promise<void>;
  Memory(): Promise<MemoryView>;
  MemorySuggestions(): Promise<MemorySuggestionsView>;
  AcceptMemorySuggestion(suggestion: MemorySuggestion): Promise<string>;
  AcceptSkillSuggestion(suggestion: SkillSuggestion): Promise<string>;
  MemoryForTab(tabID: string): Promise<MemoryView>;
  MemoryRevisions(ref: string): Promise<MemoryFact[]>;
  MemoryRevisionsForTab(tabID: string, ref: string): Promise<MemoryFact[]>;
  RestoreMemoryRevision(ref: string, revision: number): Promise<MemoryFact>;
  RestoreMemoryRevisionForTab(tabID: string, ref: string, revision: number): Promise<MemoryFact>;
  MemorySuggestionsForTab(tabID: string): Promise<MemorySuggestionsView>;
  AcceptMemorySuggestionForTab(tabID: string, suggestion: MemorySuggestion): Promise<string>;
  AcceptSkillSuggestionForTab(tabID: string, suggestion: SkillSuggestion): Promise<string>;
  Remember(scope: string, note: string): Promise<string>;
  RememberForTab(tabID: string, scope: string, note: string): Promise<string>;
  Forget(name: string): Promise<void>;
  ForgetForTab(tabID: string, name: string): Promise<void>;
  RestoreArchivedMemory(archivePath: string): Promise<MemoryFact>;
  RestoreArchivedMemoryForTab(tabID: string, archivePath: string): Promise<MemoryFact>;
  SaveDoc(path: string, body: string): Promise<string>;
  SaveDocForTab(tabID: string, path: string, body: string): Promise<string>;
  DesktopStartupSettings(): Promise<DesktopStartupSettingsView>;
  Settings(): Promise<SettingsView>;
  HooksSettings(scope: string): Promise<HooksSettingsView>;
  SaveHooksSettings(scope: string, hooks: HookConfigView[]): Promise<void>;
  SaveHooksSettingsForRoot(scope: string, projectRoot: string, hooks: HookConfigView[]): Promise<void>;
  TrustProjectHooks(): Promise<void>;
  TrustProjectHooksForRoot(projectRoot: string): Promise<void>;
  SetDefaultModel(ref: string): Promise<void>;
  SetPlannerModel(ref: string): Promise<void>;
  SetVisionModel(ref: string): Promise<void>;
  SetSubagentModel(ref: string): Promise<void>;
  SetSubagentEffort(level: string): Promise<void>;
  SetMaxSubagentDepth(depth: number): Promise<void>;
  SetMaxSubagentConcurrency(n: number): Promise<void>;
  SetMaxParallelWriters(n: number): Promise<void>;
  SetAutoPlan(mode: string): Promise<void>;
  SetDefaultToolApprovalMode(mode: string): Promise<void>;
  SetDefaultAutoRecoveryCheckpoint(enabled: boolean): Promise<void>;

  RenameProviderConnections: typeof GeneratedApp.RenameProviderConnections;
  SaveProvider(p: ProviderView): Promise<void>;
  SetProviderWebSearch(names: string[], enabled: boolean): Promise<void>;
  SaveProviderModelCatalogs(updates: ProviderModelCatalogUpdate[]): Promise<string[]>;
  SaveProviderWithKey(p: ProviderView, key: string): Promise<string>;
  AddOfficialProviderAccess(kind: string, key: string): Promise<string>;
  UpgradeDeepSeekProviderAccess(name: string): Promise<string>;
  AddProviderPresetAccess(id: string, key: string): Promise<string>;
  ResetProviderPresetAccess(id: string): Promise<void>;
  FetchProviderModels(p: ProviderView): Promise<string[]>;
  FetchProviderModelCatalog(p: ProviderView): Promise<ProviderModelCapabilityView[]>;
  FetchProviderModelCatalogDraft(p: ProviderView, key: string): Promise<ProviderModelCapabilityView[]>;
  TestProviderModel(p: ProviderView, model: string, key: string): Promise<void>;
  FetchAllProviderModelCatalogs(providers: ProviderView[]): Promise<Record<string, ProviderModelCapabilityView[]>>;
  FetchAllProviderModels(providers: ProviderView[]): Promise<Record<string, string[]>>;
  DeleteProvider(name: string): Promise<void>;
  RemoveProviderAccess(name: string): Promise<void>;
  RemoveProviderAccesses(names: string[]): Promise<void>;
  SaveProviderKey(apiKeyEnv: string, value: string): Promise<string>;
  SetConnectionKey(name: string, value: string): Promise<string>;
  AddProviderConnectionWithOptions(presetID: string, sourceName: string, key: string, baseURL: string, kind: string): Promise<string>;
  AddProviderConnectionWithURL(presetID: string, sourceName: string, key: string, baseURL: string): Promise<string>;
  AddProviderConnection(presetID: string, sourceName: string, key: string): Promise<string>;
  SetProviderKey(apiKeyEnv: string, value: string): Promise<string>;
  ClearProviderKey(apiKeyEnv: string): Promise<void>;
  SetPermissionMode(mode: string): Promise<void>;
  AddPermissionRule(list: string, rule: string): Promise<void>;
  RemovePermissionRule(list: string, rule: string): Promise<void>;
  ReloadSettings(): Promise<void>;
  SetShellPreference(prefer: string): Promise<void>;
  InstallShellSupport(id: string): Promise<ShellInstallResult>;
  CancelShellInstall(): Promise<void>;
  SetSandbox(bash: string, network: boolean, workspaceRoot: string, allowWrite: string[], shell: string): Promise<void>;
  SetNetwork(n: NetworkView): Promise<void>;
  SetBotSettings(b: BotSettingsView): Promise<void>;
  SetBotConnectionToolApprovalMode(connID: string, mode: string): Promise<void>;
  SetBotDingtalkToolApprovalMode(mode: string): Promise<void>;
  SetBotSecret(envName: string, value: string): Promise<void>;
  ClearBotSecret(envName: string): Promise<void>;
  StartBotConnectionInstall(provider: string, domain: string): Promise<BotInstallStartResult>;
  PollBotConnectionInstall(installID: string): Promise<BotInstallPollResult>;
  BotRuntimeStatus(): Promise<BotRuntimeStatusView>;
  DiagnoseBotConnection(id: string): Promise<BotConnectionDiagnostic>;
  TestBotConnection(id: string, target?: string): Promise<BotConnectionDiagnostic>;
  TestDingtalkBot(): Promise<BotConnectionDiagnostic>;
  SetCloseBehavior(mode: string): Promise<void>;
  SetSessionExperience(mode: "standard" | "deep"): Promise<void>;
  SetDisplayMode(mode: string): Promise<void>;
  SetStatusBarStyle(style: string): Promise<void>;
  SetStatusBarItems(items: string[]): Promise<void>; SetReasoningDisplayMode(mode: "hidden" | "summary" | "auto" | "expanded"): Promise<void>;
  SetDesktopLanguage(lang: string): Promise<void>;
  SetDesktopCurrency(currency: string): Promise<void>;
  SetDesktopAppearance(theme: string, style: string): Promise<void>;
  SetDesktopTerminalTheme(theme: string): Promise<void>;
  ListThemePacks(): Promise<import("./themePack").ThemePackView[]>;
  GetActiveThemePack(): Promise<import("./themePack").ThemeActiveView>;
  GetThemeExperience(): Promise<import("./themeExperience").ThemeExperienceView>;
  ActivateThemePack(id: string): Promise<void>;
  ActivateBaseStyle(style: string): Promise<void>;
  DisableThemePack(): Promise<void>;
  RestoreGraphiteAppearance(): Promise<void>;
  ResetThemePack(): Promise<void>;
  SaveThemePack(input: import("./themePack").ThemeSaveInput): Promise<import("./themePack").ThemePackView>;
  DeleteThemePack(id: string): Promise<void>;
  CopyThemePack(sourceID: string, newID: string, newName: string): Promise<import("./themePack").ThemePackView>;
  ImportThemePack(sourcePath: string, replace: boolean): Promise<import("./themePack").ThemeImportResult>;
  ExportThemePack(id: string, destPath: string): Promise<string>;
  PickThemeBackground(): Promise<string>;
  SetDesktopLayoutStyle(style: string): Promise<void>;
  SetDesktopZoomFactor(factor: number): Promise<void>;
  GetDesktopZoomFactor(): Promise<number>;
  RestartApplication(): Promise<void>;
  ReportDesktopWebViewReady(): Promise<void>;
  GetDesktopShellStatus(): Promise<DesktopShellStatusView>;
  SetDesktopCheckUpdates(enabled: boolean): Promise<void>;
  SetDesktopUpdateChannel(channel: string): Promise<void>;
  SetDesktopTelemetry(enabled: boolean): Promise<void>;
  SetDesktopMetrics(enabled: boolean): Promise<void>;
  SetExpandThinking(on: boolean): Promise<void>;
  SetDesktopConversationWidth(width: string): Promise<void>;
  MigrateDesktopPreferences(language: string, theme: string, style: string): Promise<void>;
  SetAgentParams(temperature: number, maxSteps: number, plannerMaxSteps: number, systemPrompt: string): Promise<void>;
  SetCompactRatio(ratio: number): Promise<void>;
  SetReasoningLanguage(lang: string): Promise<void>;
  SetTrayLocale(locale: "en" | "zh" | "zh-TW" | "es"): Promise<void>;
  // SetBypass is the legacy Wails name for YOLO/full-access tool auto-approval
  // (ask questions and plan approvals still wait; deny rules still apply).
  // Runtime-only.
  SetBypass(on: boolean): Promise<void>;
  Version(): Promise<string>;
  CheckUpdate(channel: string): Promise<UpdateInfo | null>;
  /** v1.20+ single-action update: download, verify, install, relaunch. */
  ApplyUpdateRequest(channel: string, expectedVersion: string, requestId: string): Promise<void>;
  /** Discard a stuck previous update transaction so the next install can proceed. */
  AbandonPendingUpdate?(): Promise<void>;
  OpenDownloadPage(): Promise<void>;
  OpenUserConfigPath?(): Promise<void>;
  ReloadUserConfig?(): Promise<{ configWarnings?: string[]; configWarningsRevision?: number; configPath?: string } | null>;
  StorageSettings(): Promise<{ defaultWorkspace: string; statePath: string; cachePath: string; extensionsPath: string }>;
  NeedsOnboarding(): Promise<boolean>;
  ConnectKey(apiKey: string): Promise<string>;
  // Crash overlay "Send report" (desktop/crash_app.go): scrubs user paths, attaches
  // version/os/arch, POSTs to the collection endpoint. Only ever sent on user click.
  ReportCrash(kind: string, detail: string): Promise<void>;
  RecordUIPerf(signals: Record<string, string>): Promise<void>;
  ListTabs(): Promise<TabMeta[]>;
  OpenProjectTab(workspaceRoot: string, topicID: string): Promise<TabMeta>;
  IsolatedWorktreeAvailability(workspaceRoot: string): Promise<DeliveryWorktreeAvailability>;
  CreateIsolatedWorktree(workspaceRoot: string): Promise<DeliveryWorktreeOpenResult>;
  InspectWorktreeMerge(tabID: string): Promise<WorktreeMergeInspection>;
  GetWorktreeStatus(tabID: string): Promise<WorktreeMergeInspection>;
  PrepareWorktreeMerge(tabID: string): Promise<WorktreeMergeInspection>;
  MergeWorktreeBack(request: WorktreeMergeRequest): Promise<WorktreeMergeResult>;
  RegisterNavigationIntent(token: string): Promise<void>;
  CloseMergedWorktreeTab(request: CloseMergedWorktreeTabRequest): Promise<CloseMergedWorktreeTabResult>;
  FinalizeWorktreeMerge(request: WorktreeCleanupRequest): Promise<WorktreeCleanupResult>;
  // Deprecated one-version aliases kept bound for older desktop clients.
  DeliveryWorktreeAvailability(workspaceRoot: string): Promise<DeliveryWorktreeAvailability>;
  CreateDeliveryWorktree(workspaceRoot: string): Promise<DeliveryWorktreeOpenResult>;
  SetAgentPreset(preset: string): Promise<void>;
  SetAgentPresetForTab(tabID: string, preset: string): Promise<void>;
  SetTokenMode(mode: string): Promise<void>;
  SetTokenModeForTab(tabID: string, mode: string): Promise<void>;
  OpenGlobalTab(topicID: string): Promise<TabMeta>;
  OpenTopicSession(scope: string, workspaceRoot: string, topicID: string, sessionPath: string): Promise<TabMeta>;
  EnsureBlankTab(scope: string, workspaceRoot: string): Promise<TabMeta>;
  ActivateTopic(scope: string, workspaceRoot: string, topicID: string, sessionPath: string): Promise<TabMeta>;
  // Two-phase ticketed topic activation (supersedes ActivateTopic for topic
  // navigation): returns a ticket after the surface switch; completion lands
  // on the "topic:activation" channel.
  StartTopicActivation(req: TopicActivationRequest): Promise<TopicActivationTicket>;
  EnsureBlankSurface(scope: string, workspaceRoot: string): Promise<TabMeta>;
  SetActiveTab(tabID: string): Promise<void>;
  ReorderTabs(tabIDs: string[]): Promise<void>;
  CloseTab(tabID: string): Promise<void>;
  TerminalWorkspaceForTab(tabID: string): Promise<TerminalWorkspaceView>;
  TerminalOutputForTab(tabID: string, sessionID: string): Promise<string>;
  CreateTerminalForTab(tabID: string, relativePath: string, shellID: string): Promise<TerminalSessionView>;
  WriteTerminalForTab(tabID: string, sessionID: string, data: string): Promise<void>;
  ResizeTerminalForTab(tabID: string, sessionID: string, cols: number, rows: number): Promise<void>;
  CloseTerminalForTab(tabID: string, sessionID: string): Promise<void>;
  RenameTerminalForTab(tabID: string, sessionID: string, title: string): Promise<void>;
  ListProjectTree(): Promise<ProjectNode[]>;
  RenameProject(workspaceRoot: string, title: string): Promise<void>;
  SetProjectColor(workspaceRoot: string, color: string): Promise<void>;
  SetProjectPinned(workspaceRoot: string, pinned: boolean): Promise<void>;
  ReorderProjects(workspaceRoots: string[]): Promise<void>;
  CreateTopic(scope: string, workspaceRoot: string, title: string): Promise<TopicMeta>;
  RenameTopic(topicID: string, title: string): Promise<void>;
  DeleteTopic(topicID: string): Promise<void>;
  TrashTopic(topicID: string): Promise<void>;
  SetTopicPinned(topicID: string, pinned: boolean): Promise<void>;
  ContextPanel(tabID: string): Promise<ContextPanelInfo>;
  // New native-feel bindings (added with the desktop native-feel plan).
  ConfirmAction(req: NativeConfirmRequest): Promise<boolean>;
  SaveWindowState(state: DesktopWindowState): Promise<void>;
  // ── Remote (SSH) ──
  RemoteHosts(): Promise<RemoteHostView[]>;
  AddRemoteHost(input: RemoteHostInput): Promise<RemoteHostView>;
  UpdateRemoteHost(id: string, input: RemoteHostInput): Promise<RemoteHostView>;
  RemoveRemoteHost(id: string): Promise<void>;
  ScanSSHConfig(): Promise<RemoteHostInput[]>;
  ConnectRemoteHost(id: string): Promise<void>;
  DisconnectRemoteHost(id: string): Promise<void>;
  RemoteConnectionStatuses(): Promise<RemoteConnectionStatus[]>;
  ConfirmRemoteHostKey(hostId: string, accept: boolean): Promise<void>;
  ConfirmRemoteSecret(hostId: string, promptId: string, secret: string, accept: boolean): Promise<void>;
  ListRemoteDir(hostId: string, path: string): Promise<RemoteDirEntry[]>;
  ReadRemoteFile(hostId: string, path: string): Promise<RemoteFilePreview>;
  WriteRemoteFile(hostId: string, path: string, body: string, expectMtimeUnix: number): Promise<RemoteWriteResult>;
  MkdirRemote(hostId: string, path: string): Promise<void>;
  RenameRemotePath(hostId: string, oldPath: string, newPath: string): Promise<void>;
  DeleteRemotePath(hostId: string, path: string, recursive: boolean): Promise<void>;
  RemoteForwards(hostId: string): Promise<RemoteForwardView[]>;
  AddRemoteForward(hostId: string, input: RemoteForwardInput): Promise<RemoteForwardView>;
  RemoveRemoteForward(hostId: string, forwardId: string): Promise<void>;
  OpenRemoteWorkspace(hostId: string, workspace: string): Promise<void>;
  PickRemoteIdentityFile(): Promise<string>;
  CheckRemotePlatform(hostId: string): Promise<void>;
  StopRemoteServer(hostId: string, workspace: string): Promise<void>;
  RemoteServerStatus(hostId: string, workspace: string): Promise<RemoteServerView>;
  RemoteServerLogs(hostId: string, workspace: string, tailLines: number): Promise<string>;
  RemoteLastWorkspace(hostId: string): Promise<string>;
  ScanRemoteLegacyWorkbenchData(): Promise<RemoteLegacyWorkbenchData>;
  CleanRemoteLegacyWorkbenchData(target: "mirrors" | "trust"): Promise<void>;
}
// Compile-time drift check. Exclude<A, B> extracts keys in A that are missing
// from B. If that set is non-empty, AssertNever<non-never> fails with
// "Type 'X' does not satisfy the constraint 'never'".
// _CheckGenToApp errors mean a generated Go method has no TS counterpart.
// These compare method *names* only; full signature checking isn't possible here
// because local types (types.ts) use plain interfaces while generated types
// (models.ts) use classes with a convertValues prototype method. The structural
// mismatch would produce false positives. Method-arity and parameter-order drift
// are caught at the call sites by tsc when components invoke app.<method>(...).
type AssertNever<T extends never> = T;
type GeneratedAppKeys = keyof typeof GeneratedApp;
type GeneratedAppMissing =
  string extends GeneratedAppKeys ? true :
  number extends GeneratedAppKeys ? true :
  symbol extends GeneratedAppKeys ? true :
  false;
export type _CheckGenToApp = AssertNever<
  GeneratedAppMissing extends true ? never : Exclude<GeneratedAppKeys, keyof AppBindings>
>;

interface WailsRuntime {
  EventsOn(name: string, cb: (...data: unknown[]) => void): () => void;
  BrowserOpenURL(url: string): void;
  WindowSetSystemDefaultTheme?(): void;
  WindowSetLightTheme?(): void;
  WindowSetDarkTheme?(): void;
  WindowSetBackgroundColour?(r: number, g: number, b: number, a: number): void;
  WindowGetSize?(): Promise<{ w: number; h: number }>;
  WindowGetPosition?(): Promise<{ x: number; y: number }>;
  WindowIsMaximised?(): Promise<boolean>;
  ClipboardSetText?(text: string): Promise<boolean>;
  ClipboardGetText?(): Promise<string>;
  // Native OS file drop; useDropTarget gates delivery to --wails-drop-target elements. Absent in browser mocks.
  OnFileDrop?(cb: (x: number, y: number, paths: string[]) => void, useDropTarget: boolean): void;
  OnFileDropOff?(): void;
}

declare global {
  interface Window {
    runtime?: WailsRuntime;
    go?: { main?: { App?: AppBindings } };
  }
}

// Must match desktop/app.go's eventChannel constant.
const EVENT_CHANNEL = "agent:event";
const RECENT_NATIVE_FILE_DRAG_MS = 2000;
const WAILS_NON_FILE_DRAG_MESSAGE = "additional File object is not a file on the disk";
const UNCAUGHT_ERROR_PREFIX_RE = /^Uncaught(?:\s+\(in promise\))?(?:\s+\w*Error)?:\s*/i;
const WAILS_IPC_CONNECTING_RE = /Failed to execute 'send' on 'WebSocket': Still in CONNECTING state/i;
const WAILS_IPC_NULL_SEND_RE = /Cannot read properties of null \(reading 'send'\)/i;

// Resolve the Wails binding at CALL time, not module-load time: in dev the Wails
// runtime can inject window.go AFTER this module first evaluates, so snapshotting
// once would pin the browser mock for the whole session (and show fake data — the
// dev mock's model list leaking into the real app was exactly this bug).
function realApp(): AppBindings | undefined {
  return typeof window !== "undefined" ? window.go?.main?.App : undefined;
}

let mockSingleton: AppBindings | null = null;
function getMock(): AppBindings {
  if (!mockSingleton) mockSingleton = makeMockApp();
  return mockSingleton;
}

// onEvent subscribes to the agent's typed event stream; returns an unsubscribe.
export function onEvent(cb: (e: WireEvent) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn(EVENT_CHANNEL, (payload) => cb(payload as WireEvent));
  }
  return mockSubscribe(cb);
}

export interface TerminalOutputEvent {
  id: string;
  data: string;
}

export interface TerminalExitEvent {
  id: string;
  exitCode: number;
  removed?: boolean;
}

function terminalEventPayload<T>(payload: unknown): T | null {
  if (!payload || typeof payload !== "object") return null;
  return payload as T;
}

export function onTerminalOutput(cb: (event: TerminalOutputEvent) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("terminal:output", (payload) => {
      const event = terminalEventPayload<TerminalOutputEvent>(payload);
      if (event?.id && typeof event.data === "string") cb(event);
    });
  }
  mockTerminalOutputListeners.add(cb);
  return () => mockTerminalOutputListeners.delete(cb);
}

export function onTerminalExit(cb: (event: TerminalExitEvent) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("terminal:exit", (payload) => {
      const event = terminalEventPayload<TerminalExitEvent>(payload);
      if (event?.id && typeof event.exitCode === "number") cb(event);
    });
  }
  mockTerminalExitListeners.add(cb);
  return () => mockTerminalExitListeners.delete(cb);
}

const mockTerminalOutputListeners = new Set<(event: TerminalOutputEvent) => void>();
const mockTerminalExitListeners = new Set<(event: TerminalExitEvent) => void>();

export function __emitMockTerminalOutput(event: TerminalOutputEvent): void {
  mockTerminalOutputListeners.forEach((listener) => listener(event));
}

export function __emitMockTerminalExit(event: TerminalExitEvent): void {
  mockTerminalExitListeners.forEach((listener) => listener(event));
}

// onUpdaterProgress subscribes to the auto-updater's progress events (a separate
// channel from the agent stream); returns an unsubscribe. Must match the event
// name emitted in desktop/updater_app.go.
export function onUpdaterProgress(cb: (p: UpdateProgress) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("updater:progress", (p) => cb(p as UpdateProgress));
  }
  updaterListeners.add(cb);
  return () => {
    updaterListeners.delete(cb);
  };
}

function errorMessage(err: unknown): string {
  if (err && typeof err === "object" && "message" in err) {
    const msg = (err as { message?: unknown }).message;
    if (typeof msg === "string") return msg;
  }
  return String(err);
}

export function isWailsNonFileDragError(err: unknown, recentNativeFileDrag = false): boolean {
  const msg = errorMessage(err).trim().replace(UNCAUGHT_ERROR_PREFIX_RE, "");
  if (msg.includes(WAILS_NON_FILE_DRAG_MESSAGE)) return true;
  return recentNativeFileDrag && msg.toLowerCase() === "invalid argument";
}

export function isWailsNonFileDragErrorEvent(
  event: Pick<ErrorEvent, "error" | "message">,
  recentNativeFileDrag = false,
): boolean {
  if (isWailsNonFileDragError(event.error ?? event.message, recentNativeFileDrag)) return true;
  return event.error != null && isWailsNonFileDragError(event.message, recentNativeFileDrag);
}

export function isTransientWailsIPCError(err: unknown): boolean {
  const msg = errorMessage(err).trim().replace(UNCAUGHT_ERROR_PREFIX_RE, "");
  return WAILS_IPC_CONNECTING_RE.test(msg) || WAILS_IPC_NULL_SEND_RE.test(msg);
}

function dataTransferLooksLikeFileDrag(dt: DataTransfer | null): boolean {
  if (!dt) return false;
  if (dt.files?.length > 0) return true;
  return Array.from(dt.types ?? []).includes("Files");
}

let wailsDragSuppressionRefs = 0;
let wailsDragSuppressionUninstall: (() => void) | null = null;
let lastNativeFileDragAt = 0;

export function installWailsNonFileDragErrorSuppression(): () => void {
  if (typeof window === "undefined") return () => {};

  wailsDragSuppressionRefs += 1;
  if (!wailsDragSuppressionUninstall) {
    const markNativeFileDrag = (e: DragEvent) => {
      if (dataTransferLooksLikeFileDrag(e.dataTransfer)) lastNativeFileDragAt = Date.now();
    };
    const hasRecentNativeFileDrag = () => Date.now() - lastNativeFileDragAt <= RECENT_NATIVE_FILE_DRAG_MS;
    const suppressNonFileDragError = (e: ErrorEvent) => {
      if (isWailsNonFileDragErrorEvent(e, hasRecentNativeFileDrag()) || isTransientWailsIPCError(e.error ?? e.message)) {
        e.preventDefault();
      }
    };
    const suppressNonFileDragRejection = (e: PromiseRejectionEvent) => {
      if (isWailsNonFileDragError(e.reason, hasRecentNativeFileDrag()) || isTransientWailsIPCError(e.reason)) {
        e.preventDefault();
      }
    };

    window.addEventListener("dragenter", markNativeFileDrag, true);
    window.addEventListener("dragover", markNativeFileDrag, true);
    window.addEventListener("drop", markNativeFileDrag, true);
    window.addEventListener("error", suppressNonFileDragError);
    window.addEventListener("unhandledrejection", suppressNonFileDragRejection);
    wailsDragSuppressionUninstall = () => {
      window.removeEventListener("dragenter", markNativeFileDrag, true);
      window.removeEventListener("dragover", markNativeFileDrag, true);
      window.removeEventListener("drop", markNativeFileDrag, true);
      window.removeEventListener("error", suppressNonFileDragError);
      window.removeEventListener("unhandledrejection", suppressNonFileDragRejection);
      lastNativeFileDragAt = 0;
    };
  }

  let disposed = false;
  return () => {
    if (disposed) return;
    disposed = true;
    wailsDragSuppressionRefs = Math.max(0, wailsDragSuppressionRefs - 1);
    if (wailsDragSuppressionRefs === 0 && wailsDragSuppressionUninstall) {
      wailsDragSuppressionUninstall();
      wailsDragSuppressionUninstall = null;
    }
  };
}

// onFilesDropped subscribes to native OS file drops landing on the composer (the
// --wails-drop-target element); the callback gets the dropped files' absolute
// paths. No-op in the browser dev mock, where the runtime is absent.
export function onFilesDropped(cb: (paths: string[]) => void): () => void {
  const rt = typeof window !== "undefined" ? window.runtime : undefined;
  if (!rt?.OnFileDrop) return () => {};

  // Wails' internal ResolveFilePaths throws when a non-file object (e.g. the
  // window icon) is dragged onto the webview. The error is uncaught and crashes
  // the app. Intercept it here so only real file drops reach the callback.
  const uninstallDragSuppression = installWailsNonFileDragErrorSuppression();

  rt.OnFileDrop((_x, _y, paths) => {
    if (Array.isArray(paths) && paths.length > 0) cb(paths);
  }, true);
  return () => {
    rt.OnFileDropOff?.();
    uninstallDragSuppression();
  };
}

// onReady subscribes to the agent:ready event fired when boot.Build completes.
// The frontend re-fetches Meta/Context/History when this lands.
// onRuntimeRebuilt fires when a tab's controller is replaced in place
// (model/effort/token-mode switch, clear-while-running). The rebuilt
// controller restarts prompt ids, so per-tab id-keyed state must reset.
export function onRuntimeRebuilt(cb: (tabId?: string, runtimeEpoch?: string) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("runtime:rebuilt", (tabId?: unknown, runtimeEpoch?: unknown) =>
      cb(
        typeof tabId === "string" ? tabId : undefined,
        typeof runtimeEpoch === "string" ? runtimeEpoch : undefined,
      )
    );
  }
  return () => {};
}

export function onReady(cb: (tabId?: string) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("agent:ready", (tabId?: unknown) => cb(typeof tabId === "string" ? tabId : undefined));
  }
  // In dev mock, fire immediately since there's no real boot sequence.
  cb();
  return () => {};
}

export function onProjectTreeChanged(cb: () => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("project-tree:changed", (payload?: unknown) => (payload as { reason?: unknown } | undefined)?.reason !== "runtime" && (payload as { reason?: unknown } | undefined)?.reason !== "catalog-v2" && cb());
  }
  return () => {};
}

// onTopicActivation subscribes to the "topic:activation" channel carrying the
// lifecycle of ticketed StartTopicActivation requests (starting/ready/failed/
// cancelled). Returns an unsubscribe.
export function onTopicActivation(cb: (event: TopicActivationEvent) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("topic:activation", (payload?: unknown) => {
      if (payload && typeof payload === "object") cb(payload as TopicActivationEvent);
    });
  }
  mockTopicActivationListeners.add(cb);
  return () => mockTopicActivationListeners.delete(cb);
}

// onTabMeta subscribes to the "tab:meta" channel: a full refreshed Meta pushed
// after the backend recomputes the expensive MetaForTab fields (git branch,
// image-input capability) in the background.
export function onTabMeta(cb: (event: TabMetaRefreshEvent) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("tab:meta", (payload?: unknown) => {
      if (payload && typeof payload === "object") cb(payload as TabMetaRefreshEvent);
    });
  }
  mockTabMetaListeners.add(cb);
  return () => mockTabMetaListeners.delete(cb);
}

const mockTopicActivationListeners = new Set<(event: TopicActivationEvent) => void>();
const mockTabMetaListeners = new Set<(event: TabMetaRefreshEvent) => void>();

export function __emitMockTopicActivation(event: TopicActivationEvent): void {
  mockTopicActivationListeners.forEach((listener) => listener(event));
}

export function __emitMockTabMeta(event: TabMetaRefreshEvent): void {
  mockTabMetaListeners.forEach((listener) => listener(event));
}

export function onSessionRecovered(cb: (payload: SessionRecoveryEvent) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("session:recovered", (payload?: unknown) => cb((payload ?? {}) as SessionRecoveryEvent));
  }
  return () => {};
}

export function onSessionActiveVersionChanged(cb: (payload: SessionRecoveryEvent) => void): () => void {
  if (typeof window === "undefined" || !window.runtime?.EventsOn) return () => {};
  return window.runtime.EventsOn("session:active-version-changed", (payload?: unknown) => cb((payload ?? {}) as SessionRecoveryEvent));
}

export function onSessionRecoveryFailed(cb: (payload: SessionRecoveryFailedEvent) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("session:recovery-failed", (payload?: unknown) => cb((payload ?? {}) as SessionRecoveryFailedEvent));
  }
  return () => {};
}

export function onRemoteStatus(cb: (s: RemoteConnectionStatus) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("remote:status", (payload?: unknown) => cb((payload ?? {}) as RemoteConnectionStatus));
  }
  return registerMockRemoteListener("status", cb as (v: unknown) => void);
}

export function onRemoteForwards(cb: (e: RemoteForwardsEvent) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("remote:forwards", (payload?: unknown) => cb((payload ?? {}) as RemoteForwardsEvent));
  }
  return registerMockRemoteListener("forwards", cb as (v: unknown) => void);
}

export function onRemoteServer(cb: (s: RemoteServerView) => void): () => void {
  if (realApp() && typeof window !== "undefined" && window.runtime) {
    return window.runtime.EventsOn("remote:server", (payload?: unknown) => cb((payload ?? {}) as RemoteServerView));
  }
  return registerMockRemoteListener("server", cb as (v: unknown) => void);
}

// Mock event fan-out so browser-dev and tsx tests can drive remote:* events
// without a Wails runtime.
type MockRemoteChannel = "status" | "forwards" | "server";
const mockRemoteListeners: Record<MockRemoteChannel, Set<(v: unknown) => void>> = {
  status: new Set(),
  forwards: new Set(),
  server: new Set(),
};
function registerMockRemoteListener(ch: MockRemoteChannel, cb: (v: unknown) => void): () => void {
  mockRemoteListeners[ch].add(cb);
  return () => mockRemoteListeners[ch].delete(cb);
}
export function __emitMockRemote(ch: MockRemoteChannel, payload: unknown): void {
  for (const cb of mockRemoteListeners[ch]) cb(payload);
}

// app proxies each call to the live binding (or the dev mock only when truly
// outside the shell), so a late-injected window.go is picked up transparently.
function bridgeBreadcrumb(method: string): string {
  if (method === "ReportCrash" || method === "RecordUIPerf") return "";
  if (/^(Submit|SubmitDisplay|RunShell|Steer|Cancel|Approve|AnswerQuestion|ReplayPendingPrompts)/.test(method))
    return `turn ${method}`;
  if (/^(SetModel|SetEffort|SetDefaultModel|SetPlannerModel|SetVisionModel|SetSubagentModel|SetSubagentEffort|SetMaxSubagentDepth|SetMaxSubagentConcurrency|SetMaxParallelWriters)/.test(method))
    return `model ${method}`;
  if (/^(SetDesktop|SetCloseBehavior|SetDisplayMode|SetStatusBar|SetReasoningDisplayMode|SetExpandThinking|SetAutoPlan|SetDefaultToolApprovalMode|SetCompactRatio|SetReasoningLanguage)/.test(method))
    return `settings ${method}`;
  if (/^(SetConnectionKey|AddProviderConnection|RenameProviderConnections|SaveProvider|SetProviderWebSearch|SaveProviderModelCatalogs|AddOfficialProviderAccess|UpgradeDeepSeekProviderAccess|AddProviderPresetAccess|ResetProviderPresetAccess|RemoveProviderAccess|RemoveProviderAccesses|DeleteProvider|SaveProviderKey|SetProviderKey|ClearProviderKey|TestProviderModel|FetchProviderModelCatalog|FetchAllProviderModelCatalogs|FetchProviderModels|FetchAllProviderModels|ConnectKey)/.test(method))
    return `provider ${method}`;
  if (/^(CheckUpdate|ApplyUpdateRequest|OpenDownloadPage|OpenUserConfigPath|ReloadUserConfig)/.test(method)) return `update ${method}`;
  if (/^(AddMCPServer|InstallMCPServer|UpdateMCPServer|RemoveMCPServer|AuthorizeAndConnectMCPServer|AuthenticateMCPServer|ReconnectMCPServer|ClearMCPServerAuthentication|SetMCPServer)/.test(method))
    return `mcp ${method}`;
  if (/^(AddSkillPath|RemoveSkillPath|SetSkillPathEnabled|RefreshSkills|SetSkillEnabled|SetSkillImplicitInvocation|AcceptSkillSuggestion|AvailableSubagentTools|CreateSubagentProfile|UpdateSubagentProfile|DeleteSubagentProfile|SetSubagentProfileModel|SetSubagentProfileEffort|TrySubagentProfile|CancelTrySubagentProfile)/.test(method))
    return `skill ${method}`;
  if (/^(MinimiseMainWindow|ToggleMaximiseMainWindow|IsMainWindowMaximised|CloseMainWindow)$/.test(method)) return `window ${method}`;
  if (/^(OpenProjectTab|OpenGlobalTab|OpenTopicSession|EnsureBlankTab|ActivateTopic|StartTopicActivation|EnsureBlankSurface|SetActiveTab|CloseTab|RegisterNavigationIntent|CloseMergedWorktreeTab|ReorderTabs|CreateTopic|RenameTopic|DeleteTopic|TrashTopic|RenameProject|RemoveWorkspace|SwitchWorkspace|PickWorkspace|IsolatedWorktreeAvailability|CreateIsolatedWorktree|InspectWorktreeMerge|GetWorktreeStatus|PrepareWorktreeMerge|ReconcileRecoveryVersions|MergeWorktreeBack|FinalizeWorktreeMerge|DeliveryWorktreeAvailability|CreateDeliveryWorktree)/.test(method))
    return `nav ${method}`;
  return "";
}

function elapsedMs(startedAt: number): number {
  const now = typeof performance !== "undefined" ? performance.now() : Date.now();
  return Math.max(0, Math.round(now - startedAt));
}

export const app: AppBindings = new Proxy({} as AppBindings, {
  get(_t, prop) {
    const target = realApp() ?? getMock();
    const v = (target as unknown as Record<string, unknown>)[String(prop)];
    if (typeof v !== "function") return v;
    return (...args: unknown[]) => {
      const method = String(prop), crumb = bridgeBreadcrumb(method);
      const startedAt = crumb ? (typeof performance !== "undefined" ? performance.now() : Date.now()) : 0;
      if (crumb) addBreadcrumb("bridge", crumb);
      try {
        const result = maybeShare(method, args, () => (v as (...a: unknown[]) => unknown).apply(target, args));
        if (result && typeof (result as Promise<unknown>).then === "function") {
          return (result as Promise<unknown>).then(
            (value) => {
              if (crumb) addBreadcrumb("bridge", `${crumb} done ms=${elapsedMs(startedAt)}`);
              return value;
            },
            (err) => {
              if (crumb) addBreadcrumb("bridge.error", `${method} ms=${elapsedMs(startedAt)}`);
              throw err;
            },
          );
        }
        if (crumb) addBreadcrumb("bridge", `${crumb} done ms=${elapsedMs(startedAt)}`);
        return result;
      } catch (err) {
        if (crumb) addBreadcrumb("bridge.error", `${method} ms=${elapsedMs(startedAt)}`);
        throw err;
      }
    };
  },
});

// openExternal opens a URL in the system browser (so links in rendered markdown
// don't navigate the webview away from the app). Falls back to window.open in the
// browser dev mock.
export function openExternal(url: string): void {
  if (typeof window !== "undefined" && window.runtime?.BrowserOpenURL) {
    window.runtime.BrowserOpenURL(url);
  } else if (typeof window !== "undefined") {
    window.open(url, "_blank", "noopener");
  }
}

// --- browser dev mock --------------------------------------------------------

const listeners = new Set<(e: WireEvent) => void>();
let mockScopedTabId: string | undefined;
let mockPendingTopicActivation: { requestId: string; tabId: string } | undefined;
let mockTopicActivationCounter = 0;

function mockSubscribe(cb: (e: WireEvent) => void): () => void {
  listeners.add(cb);
  return () => {
    listeners.delete(cb);
  };
}

function emit(e: WireEvent) {
  const event = mockScopedTabId && !e.tabId ? { ...e, tabId: mockScopedTabId } : e;
  listeners.forEach((l) => l(event));
}

export function mockToolApprovalModeAfterModeChange(current: string | undefined, nextMode: Mode): ToolApprovalMode {
  if (modeHasAutoApproveTools(nextMode)) return "yolo";
  const currentMode = normalizeToolApprovalMode(current);
  return currentMode === "yolo" ? "ask" : currentMode;
}

async function withMockTabScope<T>(tabId: string, fn: () => Promise<T>): Promise<T> {
  const previous = mockScopedTabId;
  mockScopedTabId = tabId || previous;
  try {
    return await fn();
  } finally {
    mockScopedTabId = previous;
  }
}

// Updater progress has its own listener set so the browser dev mock can stream a
// fake download/install flow through onUpdaterProgress.
const updaterListeners = new Set<(p: UpdateProgress) => void>();

function emitUpdater(p: UpdateProgress) {
  updaterListeners.forEach((l) => l(p));
}

// Test seam for the browser-dev updater state machine. Production Wails builds
// receive the same payloads through runtime.EventsOn("updater:progress").
export function __emitMockUpdater(p: UpdateProgress): void {
  emitUpdater(p);
}

function delay(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

function baseName(path: string): string {
  return path.replace(/[/\\]+$/, "").split(/[/\\]/).filter(Boolean).pop() ?? path;
}

function browserPlatformOverride(): "darwin" | "windows" | "linux" | "" {
  if (typeof window === "undefined" || window.runtime) return "";
  const value = new URLSearchParams(window.location.search).get("platform");
  return value === "darwin" || value === "windows" || value === "linux" ? value : "";
}

function browserMockDesktopLayoutStyle(): "classic" | "workbench" | "creation" {
  if (typeof window === "undefined" || window.go?.main?.App) return "workbench";
  const value = new URLSearchParams(window.location.search).get("layout");
  return value === "classic" || value === "creation" ? value : "workbench";
}

function browserPreviewBashSandboxMode(): "enforce" | "off" {
  return browserPlatformOverride() === "windows" ? "off" : "enforce";
}

function browserPreviewEffectiveShell(prefer = "auto"): "bash" | "git-bash" | "powershell" | "pwsh" {
  const normalized = prefer.trim().toLowerCase();
  if (normalized === "powershell" || normalized === "pwsh") return normalized;
  return browserPlatformOverride() === "windows" ? "git-bash" : "bash";
}

function mockScenario(): "demo" | "fresh" | "running" | "guidance" | "recovery" | "sandbox_escape" | "notice" | "deepseek_upgrade" | "bench" {
  if (typeof window === "undefined") return "demo";
  const value = new URLSearchParams(window.location.search).get("mock")?.trim().toLowerCase();
  if (value === "fresh" || value === "empty" || value === "first-run") return "fresh";
  if (typeof import.meta.env !== "undefined" && import.meta.env.DEV && (value === "recovery" || value === "inbox-recovery")) return "recovery"; if (value === "guidance" || value === "guide" || value === "steer") return "guidance";
  if (value === "running" || value === "busy" || value === "streaming") return "running";
  if (value === "sandbox_escape" || value === "sandbox-escape" || value === "sandboxescape") return "sandbox_escape";
  if (value === "notice" || value === "notices" || value === "notice-preview") return "notice";
  if (value === "deepseek_upgrade" || value === "deepseek-upgrade") return "deepseek_upgrade";
  if (value === "bench" || value === "benchmark" || value === "perf") return "bench";
  return "demo";
}

const mockProviderPresetTemplates: MockProviderPresetTemplate[] = [
  mockBundlePreset(
    "opencode-go-recommended",
    "OpenCode Go (Recommended)",
    "One key, three safe routes: Chat, Anthropic Messages, and Responses. Defaults use a low-cost effort and a 32K output cap.",
    "OPENCODE_GO_API_KEY",
    [
      mockProviderTemplate({ name: "opencode-go", kind: "openai", baseUrl: "https://opencode.ai/zen/go/v1", models: mockOpenCodeGoModels, visionModels: ["kimi-k3"], default: "glm-5.3", apiKeyEnv: "OPENCODE_GO_API_KEY", contextWindow: 128000, modelOverrides: [{ model: "glm-5.3", reasoningProtocol: "openai", supportedEfforts: ["low", "high", "max"], defaultEffort: "low", maxOutputTokens: 32768 }, { model: "kimi-k3", reasoningProtocol: "openai", supportedEfforts: ["high", "max"], defaultEffort: "max", contextWindow: 1048576, maxOutputTokens: 32768 }] }),
      mockProviderTemplate({ name: "opencode-go-anthropic", kind: "anthropic", baseUrl: "https://opencode.ai/zen/go", models: ["qwen3.8-max", "qwen3.7-plus", "qwen3.7-max", "qwen3.6-plus", "minimax-m3", "minimax-m2.7", "minimax-m2.5"], visionModels: ["qwen3.8-max", "qwen3.7-plus", "qwen3.6-plus"], default: "qwen3.7-plus", apiKeyEnv: "OPENCODE_GO_API_KEY", thinking: "adaptive", contextWindow: 262144 }),
      mockProviderTemplate({ name: "opencode-go-responses", kind: "responses", baseUrl: "https://opencode.ai/zen/go/v1", models: ["grok-4.5", "gpt-5.6-luna", "muse-spark-1.2-contributor"], visionModels: ["grok-4.5", "gpt-5.6-luna", "muse-spark-1.2-contributor"], default: "grok-4.5", apiKeyEnv: "OPENCODE_GO_API_KEY", contextWindow: 500000, supportedEfforts: ["low", "medium", "high"], defaultEffort: "low" }),
    ],
    { recommended: true, billingMode: "subscription_equivalent", displayGroup: "opencode", displaySection: "go", displayTier: "primary", routeKind: "bundle", displayOrder: 0 },
  ),
  mockPreset("deepseek-responses", "DeepSeek Official Responses API", "Official stateless DeepSeek Responses API for Flash and Pro with server-side web search.", "DEEPSEEK_API_KEY", mockProviderTemplate({ name: "deepseek-responses", kind: "responses", baseUrl: "https://api.deepseek.com", models: ["deepseek-v4-flash", "deepseek-v4-pro"], default: "deepseek-v4-flash", apiKeyEnv: "DEEPSEEK_API_KEY", balanceUrl: "https://api.deepseek.com/user/balance", webSearch: true, serverWebSearchCapability: true, contextWindow: 1000000, modelOverrides: [{ model: "deepseek-v4-flash", reasoningProtocol: "", supportedEfforts: ["disabled", "low", "high", "max"], defaultEffort: "high" }, { model: "deepseek-v4-pro", reasoningProtocol: "", supportedEfforts: ["disabled", "high", "max"], defaultEffort: "high" }] })),
  mockPreset("longcat-openai", "LongCat OpenAI", "LongCat Platform OpenAI-compatible endpoint for LongCat-2.0.", "LONGCAT_API_KEY", mockProviderTemplate({ name: "longcat-openai", kind: "openai", baseUrl: "https://api.longcat.chat/openai/v1", modelsUrl: "https://api.longcat.chat/openai/v1/models", models: mockLongCatModels, default: "LongCat-2.0", apiKeyEnv: "LONGCAT_API_KEY", contextWindow: 131072, thinking: "enabled", supportedEfforts: ["enabled", "disabled"], defaultEffort: "enabled" })),
  mockPreset("longcat-anthropic", "LongCat Anthropic", "LongCat Platform Anthropic-compatible Messages endpoint for LongCat-2.0.", "LONGCAT_API_KEY", mockProviderTemplate({ name: "longcat-anthropic", kind: "anthropic", baseUrl: "https://api.longcat.chat/anthropic", modelsUrl: "https://api.longcat.chat/anthropic/v1/models", models: mockLongCatModels, default: "LongCat-2.0", apiKeyEnv: "LONGCAT_API_KEY", authHeader: true, contextWindow: 131072, thinking: "enabled", supportedEfforts: ["enabled", "disabled"], defaultEffort: "enabled" })),
  mockPreset("token-rhythm", "Token Rhythm", "Token Rhythm (基元律动) multi-model OpenAI-compatible gateway.", "TOKEN_RHYTHM_API_KEY", mockProviderTemplate({ name: "token-rhythm", kind: "openai", baseUrl: "https://tokenrhythm.studio/v1", modelsUrl: "https://tokenrhythm.studio/v1/models", models: mockTokenRhythmModels, visionModels: ["kimi-k2.5", "kimi-k2.6", "kimi-k2.7-code"], default: "deepseek-v4-flash", apiKeyEnv: "TOKEN_RHYTHM_API_KEY", contextWindow: 1000000, modelOverrides: mockTokenRhythmModelOverrides })),
  mockPreset("kimi-cn", "Kimi CN API", "Moonshot Kimi China OpenAI-compatible API.", "KIMI_API_KEY", mockProviderTemplate({ name: "kimi-cn", kind: "openai", baseUrl: "https://api.moonshot.cn/v1", models: mockKimiAPIModels, visionModels: mockKimiAPIModels, default: "kimi-k2.7-code", apiKeyEnv: "KIMI_API_KEY", balanceUrl: "https://api.moonshot.cn/v1/users/me/balance", contextWindow: 262144, reasoningProtocol: "none", modelOverrides: [{ model: "kimi-k3", reasoningProtocol: "openai", supportedEfforts: ["low", "high", "max"], defaultEffort: "max", contextWindow: 1048576 }] })),
  mockPreset("kimi-global", "Kimi Global API", "Moonshot Kimi international OpenAI-compatible API.", "MOONSHOT_API_KEY", mockProviderTemplate({ name: "kimi-global", kind: "openai", baseUrl: "https://api.moonshot.ai/v1", models: mockKimiAPIModels, visionModels: mockKimiAPIModels, default: "kimi-k2.7-code", apiKeyEnv: "MOONSHOT_API_KEY", balanceUrl: "https://api.moonshot.ai/v1/users/me/balance", contextWindow: 262144, reasoningProtocol: "none", modelOverrides: [{ model: "kimi-k3", reasoningProtocol: "openai", supportedEfforts: ["low", "high", "max"], defaultEffort: "max", contextWindow: 1048576 }] })),
  mockPreset("kimi-coding-plan", "Kimi Coding Plan", "Kimi Coding Plan via its dedicated Anthropic-compatible endpoint.", "KIMI_CODING_API_KEY", mockProviderTemplate({ name: "kimi-coding-plan", kind: "anthropic", baseUrl: "https://api.kimi.com/coding/", models: ["kimi-for-coding"], visionModels: ["kimi-for-coding"], default: "kimi-for-coding", apiKeyEnv: "KIMI_CODING_API_KEY", headers: { "User-Agent": "claude-code/0.1.0" }, thinking: "adaptive", contextWindow: 262144 })),
  mockPreset("mimo-api", "MiMo API", "Xiaomi MiMo direct API with text and vision-capable models.", "MIMO_API_KEY", mockProviderTemplate({ name: "mimo-api", kind: "openai", baseUrl: "https://api.xiaomimimo.com/v1", models: mockMiMoV25Models, visionModels: ["mimo-v2.5"], default: "mimo-v2.5-pro", apiKeyEnv: "MIMO_API_KEY", contextWindow: 1048576 })),
  mockPreset("mimo-anthropic", "MiMo Anthropic", "Xiaomi MiMo direct Anthropic-compatible endpoint.", "MIMO_API_KEY", mockProviderTemplate({ name: "mimo-anthropic", kind: "anthropic", baseUrl: "https://api.xiaomimimo.com/anthropic", models: mockMiMoV25Models, visionModels: ["mimo-v2.5"], default: "mimo-v2.5-pro", apiKeyEnv: "MIMO_API_KEY", thinking: "adaptive", contextWindow: 1048576 })),
  mockPreset("mimo-token-plan-cn", "MiMo Token Plan CN", "Xiaomi MiMo token-plan China endpoint.", "MIMO_TOKEN_PLAN_API_KEY", mockProviderTemplate({ name: "mimo-token-plan-cn", kind: "openai", baseUrl: "https://token-plan-cn.xiaomimimo.com/v1", models: mockMiMoV25Models, visionModels: ["mimo-v2.5"], default: "mimo-v2.5-pro", apiKeyEnv: "MIMO_TOKEN_PLAN_API_KEY", contextWindow: 1048576 })),
  mockPreset("mimo-token-plan-cn-anthropic", "MiMo Token Plan CN Anthropic", "Xiaomi MiMo token-plan China Anthropic-compatible endpoint.", "MIMO_TOKEN_PLAN_API_KEY", mockProviderTemplate({ name: "mimo-token-plan-cn-anthropic", kind: "anthropic", baseUrl: "https://token-plan-cn.xiaomimimo.com/anthropic", models: mockMiMoV25Models, visionModels: ["mimo-v2.5"], default: "mimo-v2.5-pro", apiKeyEnv: "MIMO_TOKEN_PLAN_API_KEY", thinking: "adaptive", contextWindow: 1048576 })),
  mockPreset("mimo-token-plan-sgp", "MiMo Token Plan SGP", "Xiaomi MiMo token-plan Singapore endpoint.", "MIMO_TOKEN_PLAN_API_KEY", mockProviderTemplate({ name: "mimo-token-plan-sgp", kind: "openai", baseUrl: "https://token-plan-sgp.xiaomimimo.com/v1", models: mockMiMoV25Models, visionModels: ["mimo-v2.5"], default: "mimo-v2.5-pro", apiKeyEnv: "MIMO_TOKEN_PLAN_API_KEY", contextWindow: 1048576 })),
  mockPreset("mimo-token-plan-sgp-anthropic", "MiMo Token Plan SGP Anthropic", "Xiaomi MiMo token-plan Singapore Anthropic-compatible endpoint.", "MIMO_TOKEN_PLAN_API_KEY", mockProviderTemplate({ name: "mimo-token-plan-sgp-anthropic", kind: "anthropic", baseUrl: "https://token-plan-sgp.xiaomimimo.com/anthropic", models: mockMiMoV25Models, visionModels: ["mimo-v2.5"], default: "mimo-v2.5-pro", apiKeyEnv: "MIMO_TOKEN_PLAN_API_KEY", thinking: "adaptive", contextWindow: 1048576 })),
  mockPreset("mimo-token-plan-ams", "MiMo Token Plan AMS", "Xiaomi MiMo token-plan Amsterdam endpoint.", "MIMO_TOKEN_PLAN_API_KEY", mockProviderTemplate({ name: "mimo-token-plan-ams", kind: "openai", baseUrl: "https://token-plan-ams.xiaomimimo.com/v1", models: mockMiMoV25Models, visionModels: ["mimo-v2.5"], default: "mimo-v2.5-pro", apiKeyEnv: "MIMO_TOKEN_PLAN_API_KEY", contextWindow: 1048576 })),
  mockPreset("mimo-token-plan-ams-anthropic", "MiMo Token Plan AMS Anthropic", "Xiaomi MiMo token-plan Amsterdam Anthropic-compatible endpoint.", "MIMO_TOKEN_PLAN_API_KEY", mockProviderTemplate({ name: "mimo-token-plan-ams-anthropic", kind: "anthropic", baseUrl: "https://token-plan-ams.xiaomimimo.com/anthropic", models: mockMiMoV25Models, visionModels: ["mimo-v2.5"], default: "mimo-v2.5-pro", apiKeyEnv: "MIMO_TOKEN_PLAN_API_KEY", thinking: "adaptive", contextWindow: 1048576 })),
  mockPreset("minimax-cn-api", "MiniMax CN API", "MiniMax China OpenAI-compatible M-series API endpoint.", "MINIMAX_API_KEY", mockProviderTemplate({ name: "minimax-cn-api", kind: "openai", baseUrl: "https://api.minimaxi.com/v1", models: mockMiniMaxModels, visionModels: ["MiniMax-M3"], default: "MiniMax-M3", apiKeyEnv: "MINIMAX_API_KEY", extraBody: { reasoning_split: true }, contextWindow: 1048576, thinking: "adaptive", supportedEfforts: ["disabled", "adaptive"], defaultEffort: "adaptive" })),
  mockPreset("minimax-global-api", "MiniMax Global API", "MiniMax international OpenAI-compatible M-series API endpoint.", "MINIMAX_API_KEY", mockProviderTemplate({ name: "minimax-global-api", kind: "openai", baseUrl: "https://api.minimax.io/v1", models: mockMiniMaxModels, visionModels: ["MiniMax-M3"], default: "MiniMax-M3", apiKeyEnv: "MINIMAX_API_KEY", extraBody: { reasoning_split: true }, contextWindow: 1048576, thinking: "adaptive", supportedEfforts: ["disabled", "adaptive"], defaultEffort: "adaptive" })),
  mockPreset("minimax-cn-anthropic", "MiniMax CN Anthropic", "MiniMax China Anthropic-compatible M-series endpoint.", "MINIMAX_PLAN_API_KEY", mockProviderTemplate({ name: "minimax-cn-anthropic", kind: "anthropic", baseUrl: "https://api.minimaxi.com/anthropic", models: mockMiniMaxModels, visionModels: ["MiniMax-M3"], default: "MiniMax-M3", apiKeyEnv: "MINIMAX_PLAN_API_KEY", authHeader: true, contextWindow: 1048576, thinking: "adaptive", supportedEfforts: ["disabled", "adaptive"], defaultEffort: "adaptive" })),
  mockPreset("minimax-global-anthropic", "MiniMax Global Anthropic", "MiniMax international Anthropic-compatible endpoint with Bearer auth.", "MINIMAX_API_KEY", mockProviderTemplate({ name: "minimax-global-anthropic", kind: "anthropic", baseUrl: "https://api.minimax.io/anthropic", models: mockMiniMaxModels, visionModels: ["MiniMax-M3"], default: "MiniMax-M3", apiKeyEnv: "MINIMAX_API_KEY", authHeader: true, contextWindow: 1048576, thinking: "adaptive", supportedEfforts: ["disabled", "adaptive"], defaultEffort: "adaptive" })),
  mockPreset("glm-cn", "GLM CN API", "Zhipu GLM China OpenAI-compatible API with thinking controls.", "GLM_API_KEY", mockProviderTemplate({ name: "glm-cn", kind: "openai", baseUrl: "https://open.bigmodel.cn/api/paas/v4", models: mockGLMAPIModels, visionModels: ["glm-5v-turbo"], default: "glm-5.2", apiKeyEnv: "GLM_API_KEY", contextWindow: 1000000, thinking: "enabled", supportedEfforts: ["enabled", "disabled"], defaultEffort: "enabled" })),
  mockPreset("zai-global", "Z.AI Global API", "Z.AI international OpenAI-compatible GLM API.", "ZAI_API_KEY", mockProviderTemplate({ name: "zai-global", kind: "openai", baseUrl: "https://api.z.ai/api/paas/v4", models: mockGLMAPIModels, visionModels: ["glm-5v-turbo"], default: "glm-5.2", apiKeyEnv: "ZAI_API_KEY", contextWindow: 1000000, thinking: "enabled", supportedEfforts: ["enabled", "disabled"], defaultEffort: "enabled" })),
  mockPreset("glm-coding-plan-cn", "GLM Coding Plan CN", "Zhipu GLM China coding-plan endpoint.", "GLM_PLAN_API_KEY", mockProviderTemplate({ name: "glm-coding-plan-cn", kind: "openai", baseUrl: "https://open.bigmodel.cn/api/coding/paas/v4", models: mockGLMCodingModels, default: "glm-5.2", apiKeyEnv: "GLM_PLAN_API_KEY", contextWindow: 1000000, thinking: "enabled", supportedEfforts: ["enabled", "disabled"], defaultEffort: "enabled" })),
  mockPreset("glm-coding-plan-cn-anthropic", "GLM Coding Plan CN Anthropic", "Zhipu GLM China coding-plan Anthropic-compatible endpoint.", "GLM_PLAN_API_KEY", mockProviderTemplate({ name: "glm-coding-plan-cn-anthropic", kind: "anthropic", baseUrl: "https://open.bigmodel.cn/api/anthropic", models: mockGLMAnthropicModels, default: "glm-5.2", apiKeyEnv: "GLM_PLAN_API_KEY", authHeader: true, thinking: "adaptive", contextWindow: 1000000 })),
  mockPreset("zai-coding-plan-global", "Z.AI Coding Plan Global", "Z.AI international coding-plan endpoint.", "ZAI_CODING_API_KEY", mockProviderTemplate({ name: "zai-coding-plan-global", kind: "openai", baseUrl: "https://api.z.ai/api/coding/paas/v4", models: mockGLMCodingModels, default: "glm-5.2", apiKeyEnv: "ZAI_CODING_API_KEY", contextWindow: 1000000, thinking: "enabled", supportedEfforts: ["enabled", "disabled"], defaultEffort: "enabled" })),
  mockPreset("zai-coding-plan-global-anthropic", "Z.AI Coding Plan Global Anthropic", "Z.AI international coding-plan Anthropic-compatible endpoint.", "ZAI_CODING_API_KEY", mockProviderTemplate({ name: "zai-coding-plan-global-anthropic", kind: "anthropic", baseUrl: "https://api.z.ai/api/anthropic", models: mockGLMAnthropicModels, default: "glm-5.2", apiKeyEnv: "ZAI_CODING_API_KEY", authHeader: true, thinking: "adaptive", contextWindow: 1000000 })),
  mockPreset("opencode-go", "OpenCode Go", "OpenCode Go relay with per-model capability overrides.", "OPENCODE_GO_API_KEY", mockProviderTemplate({ name: "opencode-go", kind: "openai", baseUrl: "https://opencode.ai/zen/go/v1", models: mockOpenCodeGoModels, visionModels: ["kimi-k3"], default: "glm-5.2", apiKeyEnv: "OPENCODE_GO_API_KEY", contextWindow: 128000, modelOverrides: [{ model: "glm-5.3", reasoningProtocol: "openai", supportedEfforts: ["low", "high", "max"], defaultEffort: "high" }, { model: "glm-5.2", reasoningProtocol: "openai", supportedEfforts: ["high", "max"], defaultEffort: "high" }, { model: "kimi-k3", reasoningProtocol: "openai", supportedEfforts: ["high", "max"], defaultEffort: "max", contextWindow: 1048576 }, { model: "hy3", reasoningProtocol: "openai", supportedEfforts: ["none", "low", "high"], defaultEffort: "high" }] }), { displayGroup: "opencode", displaySection: "go", displayTier: "compatibility", routeKind: "chat", displayOrder: 90 }),
  mockPreset("opencode-go-anthropic", "OpenCode Go Anthropic", "OpenCode Go subscription Anthropic-compatible route for Qwen and MiniMax.", "OPENCODE_GO_API_KEY", mockProviderTemplate({ name: "opencode-go-anthropic", kind: "anthropic", baseUrl: "https://opencode.ai/zen/go", models: ["qwen3.8-max", "qwen3.7-plus", "qwen3.7-max", "qwen3.6-plus", "minimax-m3", "minimax-m2.7", "minimax-m2.5"], visionModels: ["qwen3.8-max", "qwen3.7-plus", "qwen3.6-plus"], default: "qwen3.7-plus", apiKeyEnv: "OPENCODE_GO_API_KEY", thinking: "adaptive", contextWindow: 262144 }), { displayGroup: "opencode", displaySection: "go", displayTier: "advanced", routeKind: "anthropic", displayOrder: 20 }),
  mockPreset("opencode-go-responses", "OpenCode Go Responses", "OpenCode Go Responses API route for Grok 4.5, GPT 5.6 Luna, and Muse Spark.", "OPENCODE_GO_API_KEY", mockProviderTemplate({ name: "opencode-go-responses", kind: "responses", baseUrl: "https://opencode.ai/zen/go/v1", models: ["grok-4.5", "gpt-5.6-luna", "muse-spark-1.2-contributor"], visionModels: ["grok-4.5", "gpt-5.6-luna", "muse-spark-1.2-contributor"], default: "grok-4.5", apiKeyEnv: "OPENCODE_GO_API_KEY", contextWindow: 500000, supportedEfforts: ["low", "medium", "high"], defaultEffort: "high", modelOverrides: [{ model: "grok-4.5", reasoningProtocol: "", supportedEfforts: ["low", "medium", "high"], defaultEffort: "high", contextWindow: 500000 }, { model: "gpt-5.6-luna", reasoningProtocol: "", supportedEfforts: ["none", "low", "medium", "high", "xhigh", "max"], defaultEffort: "high", contextWindow: 1050000 }, { model: "muse-spark-1.2-contributor", reasoningProtocol: "", supportedEfforts: ["minimal", "low", "medium", "high", "xhigh"], defaultEffort: "high", contextWindow: 1048576 }] }), { displayGroup: "opencode", displaySection: "go", displayTier: "advanced", routeKind: "responses", displayOrder: 30 }),
  mockPreset("opencode-go-deepseek-anthropic", "OpenCode Go DeepSeek Anthropic", "OpenCode Go Anthropic Messages route for DeepSeek Flash with server-side web search.", "OPENCODE_GO_API_KEY", mockProviderTemplate({ name: "opencode-go-deepseek-anthropic", kind: "anthropic", baseUrl: "https://opencode.ai/zen/go", models: ["deepseek-v4-flash"], default: "deepseek-v4-flash", apiKeyEnv: "OPENCODE_GO_API_KEY", thinking: "adaptive", webSearch: true, serverWebSearchCapability: true, contextWindow: 262144, supportedEfforts: ["disabled", "high", "max"], defaultEffort: "high" }), { displayGroup: "opencode", displaySection: "go", displayTier: "advanced", routeKind: "search-anthropic", optional: true, displayOrder: 40 }),
  mockPreset("opencode-go-deepseek-responses", "OpenCode Go DeepSeek Responses", "OpenCode Go stateless Responses API route for DeepSeek Flash with server-side web search.", "OPENCODE_GO_API_KEY", mockProviderTemplate({ name: "opencode-go-deepseek-responses", kind: "responses", baseUrl: "https://opencode.ai/zen/go/v1", models: ["deepseek-v4-flash"], default: "deepseek-v4-flash", apiKeyEnv: "OPENCODE_GO_API_KEY", webSearch: true, serverWebSearchCapability: true, contextWindow: 1000000, supportedEfforts: ["disabled", "high", "max"], defaultEffort: "high" }), { displayGroup: "opencode", displaySection: "go", displayTier: "advanced", routeKind: "search-responses", optional: true, displayOrder: 50 }),
  mockPreset("opencode-zen-anthropic", "OpenCode Zen Anthropic", "OpenCode Zen Anthropic-compatible route for Claude and Qwen models.", "OPENCODE_API_KEY", mockProviderTemplate({ name: "opencode-zen-anthropic", kind: "anthropic", baseUrl: "https://opencode.ai/zen", models: ["claude-sonnet-4-6", "claude-opus-4-8", "claude-haiku-4-5", "qwen3.6-plus", "qwen3.5-plus", "qwen3.6-plus-free"], visionModels: ["claude-sonnet-4-6", "claude-opus-4-8", "claude-haiku-4-5"], default: "claude-sonnet-4-6", apiKeyEnv: "OPENCODE_API_KEY", contextWindow: 262144 }), { displayGroup: "opencode", displaySection: "zen", displayTier: "primary", routeKind: "zen-anthropic", displayOrder: 100 }),
  mockPreset("qwen-cn", "Qwen CN API", "Alibaba DashScope China standard OpenAI-compatible endpoint.", "QWEN_API_KEY", mockProviderTemplate({ name: "qwen-cn", kind: "openai", baseUrl: "https://dashscope.aliyuncs.com/compatible-mode/v1", models: mockQwenAPIModels, visionModels: ["qwen3.7-plus", "qwen3.6-plus", "qwen3.5-plus", "kimi-k2.5"], default: "qwen3.7-plus", apiKeyEnv: "QWEN_API_KEY" })),
  mockPreset("qwen-global", "Qwen Global API", "Alibaba DashScope international standard OpenAI-compatible endpoint.", "QWEN_API_KEY", mockProviderTemplate({ name: "qwen-global", kind: "openai", baseUrl: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", models: mockQwenAPIModels, visionModels: ["qwen3.7-plus", "qwen3.6-plus", "qwen3.5-plus", "kimi-k2.5"], default: "qwen3.7-plus", apiKeyEnv: "QWEN_API_KEY" })),
  mockPreset("qwen-coding-plan-cn", "Qwen Coding Plan CN", "Alibaba Cloud Qwen Coding Plan China endpoint.", "QWEN_CODING_API_KEY", mockProviderTemplate({ name: "qwen-coding-plan-cn", kind: "openai", baseUrl: "https://coding.dashscope.aliyuncs.com/v1", models: mockQwenPlanModels, visionModels: mockQwenPlanVisionModels, default: "qwen3.7-plus", apiKeyEnv: "QWEN_CODING_API_KEY" })),
  mockPreset("qwen-coding-plan-cn-anthropic", "Qwen Coding Plan CN Anthropic", "Alibaba Cloud Qwen Coding Plan China Anthropic-compatible endpoint.", "QWEN_CODING_API_KEY", mockProviderTemplate({ name: "qwen-coding-plan-cn-anthropic", kind: "anthropic", baseUrl: "https://coding.dashscope.aliyuncs.com/apps/anthropic", models: mockQwenPlanModels, visionModels: mockQwenPlanVisionModels, default: "qwen3.7-plus", apiKeyEnv: "QWEN_CODING_API_KEY", thinking: "adaptive" })),
  mockPreset("qwen-coding-plan-global", "Qwen Coding Plan Global", "Alibaba Cloud Qwen Coding Plan international endpoint.", "QWEN_CODING_API_KEY", mockProviderTemplate({ name: "qwen-coding-plan-global", kind: "openai", baseUrl: "https://coding-intl.dashscope.aliyuncs.com/v1", models: mockQwenPlanModels, visionModels: mockQwenPlanVisionModels, default: "qwen3.7-plus", apiKeyEnv: "QWEN_CODING_API_KEY" })),
  mockPreset("qwen-coding-plan-global-anthropic", "Qwen Coding Plan Global Anthropic", "Alibaba Cloud Qwen Coding Plan international Anthropic-compatible endpoint.", "QWEN_CODING_API_KEY", mockProviderTemplate({ name: "qwen-coding-plan-global-anthropic", kind: "anthropic", baseUrl: "https://coding-intl.dashscope.aliyuncs.com/apps/anthropic", models: mockQwenPlanModels, visionModels: mockQwenPlanVisionModels, default: "qwen3.7-plus", apiKeyEnv: "QWEN_CODING_API_KEY", thinking: "adaptive" })),
  mockPreset("stepfun", "StepFun", "StepFun coding-plan OpenAI-compatible endpoint.", "STEPFUN_API_KEY", mockProviderTemplate({ name: "stepfun", kind: "openai", baseUrl: "https://api.stepfun.com/step_plan/v1", models: mockStepFunModels, default: "step-3.7-flash", apiKeyEnv: "STEPFUN_API_KEY", supportedEfforts: ["low", "medium", "high"], defaultEffort: "medium" })),
  mockPreset("stepfun-anthropic", "StepFun Anthropic", "StepFun coding-plan Anthropic-compatible endpoint.", "STEPFUN_API_KEY", mockProviderTemplate({ name: "stepfun-anthropic", kind: "anthropic", baseUrl: "https://api.stepfun.com/step_plan", models: mockStepFunModels, default: "step-3.7-flash", apiKeyEnv: "STEPFUN_API_KEY", thinking: "adaptive", supportedEfforts: ["low", "medium", "high"], defaultEffort: "medium" })),
  mockPreset("stepfun-responses", "StepFun Responses API", "StepFun Responses API endpoint with reasoning effort and tool calls (step-3.7-flash).", "STEPFUN_API_KEY", mockProviderTemplate({ name: "stepfun-responses", kind: "responses", baseUrl: "https://api.stepfun.com/v1", models: ["step-3.7-flash"], default: "step-3.7-flash", apiKeyEnv: "STEPFUN_API_KEY", supportedEfforts: ["low", "medium", "high"], defaultEffort: "medium" })),
  mockPreset("stepfun-api", "StepFun API Pay-as-you-go", "StepFun pay-as-you-go OpenAI-compatible API with vision on step-3.7-flash.", "STEPFUN_API_KEY", mockProviderTemplate({ name: "stepfun-api", kind: "openai", baseUrl: "https://api.stepfun.com/v1", models: mockStepFunModels, visionModels: ["step-3.7-flash"], default: "step-3.7-flash", apiKeyEnv: "STEPFUN_API_KEY", supportedEfforts: ["low", "medium", "high"], defaultEffort: "medium" })),
  mockPreset("stepfun-api-anthropic", "StepFun API Anthropic Pay-as-you-go", "StepFun pay-as-you-go Anthropic-compatible Messages API with automatic prefix caching.", "STEPFUN_API_KEY", mockProviderTemplate({ name: "stepfun-api-anthropic", kind: "anthropic", baseUrl: "https://api.stepfun.com", models: mockStepFunModels, default: "step-3.7-flash", apiKeyEnv: "STEPFUN_API_KEY", thinking: "adaptive", supportedEfforts: ["low", "medium", "high"], defaultEffort: "medium" })),
  mockPreset("novita", "NovitaAI", "NovitaAI OpenAI-compatible multi-model gateway.", "NOVITA_API_KEY", mockProviderTemplate({ name: "novita", kind: "openai", baseUrl: "https://api.novita.ai/openai/v1", models: mockNovitaModels, default: "zai-org/glm-5.2", apiKeyEnv: "NOVITA_API_KEY" })),
  mockPreset("gmi", "GMI Cloud", "GMI Cloud direct multi-model OpenAI-compatible gateway.", "GMI_API_KEY", mockProviderTemplate({ name: "gmi", kind: "openai", baseUrl: "https://api.gmi-serving.com/v1", models: mockGMIModels, default: "zai-org/GLM-5.2-FP8", apiKeyEnv: "GMI_API_KEY", headers: { "User-Agent": "Reasonix" } })),
  mockPreset("vercel-ai-gateway", "Vercel AI Gateway", "Vercel AI Gateway via Anthropic-compatible Messages API.", "AI_GATEWAY_API_KEY", mockProviderTemplate({ name: "vercel-ai-gateway", kind: "anthropic", baseUrl: "https://ai-gateway.vercel.sh", models: mockVercelModels, visionModels: ["anthropic/claude-sonnet-4.6", "anthropic/claude-opus-4.8", "openai/gpt-5.4", "openai/gpt-5.4-pro", "moonshotai/kimi-k2.7-code"], default: "anthropic/claude-sonnet-4.6", apiKeyEnv: "AI_GATEWAY_API_KEY", authHeader: true, contextWindow: 1000000 })),
  mockPreset("huggingface", "HuggingFace Router", "HuggingFace Inference Router OpenAI-compatible endpoint.", "HF_TOKEN", mockProviderTemplate({ name: "huggingface", kind: "openai", baseUrl: "https://router.huggingface.co/v1", models: ["zai-org/GLM-5.2", "deepseek-ai/DeepSeek-V3.2", "Qwen/Qwen3.5-72B-Instruct"], default: "zai-org/GLM-5.2", apiKeyEnv: "HF_TOKEN" })),
  mockPreset("nvidia", "NVIDIA NIM", "NVIDIA NIM OpenAI-compatible accelerated inference endpoint.", "NVIDIA_API_KEY", mockProviderTemplate({ name: "nvidia", kind: "openai", baseUrl: "https://integrate.api.nvidia.com/v1", models: ["nvidia/nemotron-3-nano-30b-a3b", "nvidia/nemotron-3-super-120b-a12b", "nvidia/nemotron-3-ultra-550b-a55b", "deepseek-ai/deepseek-v4-pro", "qwen/qwen3.5-397b-a17b"], default: "nvidia/nemotron-3-nano-30b-a3b", apiKeyEnv: "NVIDIA_API_KEY" })),
  mockPreset("kilocode", "KiloCode", "Kilo Code gateway OpenAI-compatible endpoint.", "KILOCODE_API_KEY", mockProviderTemplate({ name: "kilocode", kind: "openai", baseUrl: "https://api.kilo.ai/api/gateway", models: ["kilo/auto"], default: "kilo/auto", apiKeyEnv: "KILOCODE_API_KEY" })),
  createMockModelScopePreset(mockProviderTemplate, mockPreset),
  mockPreset("ollama-cloud", "Ollama Cloud", "Hosted Ollama Cloud OpenAI-compatible endpoint with max reasoning effort.", "OLLAMA_API_KEY", mockProviderTemplate({ name: "ollama-cloud", kind: "openai", baseUrl: "https://ollama.com/v1", models: mockOllamaCloudModels, default: "glm-5.2", apiKeyEnv: "OLLAMA_API_KEY" })),
  mockPreset("scnet", "SCNet", "SCNet (National Supercomputing Internet) OpenAI-compatible token-plan API.", "SCNET_API_KEY", mockProviderTemplate({ name: "scnet", kind: "openai", baseUrl: "https://api.scnet.cn/api/llm/v1", modelsUrl: "https://api.scnet.cn/api/llm/v1/models", models: ["GLM-5.2", "GLM-5", "GLM-5.1", "Kimi-K3", "Kimi-K2.7-Code", "Kimi-K2.6", "Kimi-K2.5", "DeepSeek-V4-Flash", "DeepSeek-V3.2", "MiniMax-M3", "MiniMax-M2.7", "MiniMax-M2.5", "MiMo-V2.5-Pro"], visionModels: ["Kimi-K2.6", "Kimi-K2.5"], default: "MiniMax-M2.5", apiKeyEnv: "SCNET_API_KEY", modelOverrides: [{ model: "DeepSeek-V4-Flash", reasoningProtocol: "openai", supportedEfforts: ["high", "max"], defaultEffort: "high" }] })),
  mockPreset("scnet-anthropic", "SCNet Anthropic", "SCNet (National Supercomputing Internet) Anthropic-compatible token-plan endpoint with Bearer auth.", "SCNET_API_KEY", mockProviderTemplate({ name: "scnet-anthropic", kind: "anthropic", baseUrl: "https://api.scnet.cn/api/llm/anthropic", models: ["GLM-5.2", "GLM-5", "GLM-5.1", "Kimi-K3", "Kimi-K2.7-Code", "Kimi-K2.6", "Kimi-K2.5", "DeepSeek-V4-Flash", "DeepSeek-V3.2", "MiniMax-M3", "MiniMax-M2.7", "MiniMax-M2.5", "MiMo-V2.5-Pro"], visionModels: ["Kimi-K2.6", "Kimi-K2.5"], default: "MiniMax-M2.5", apiKeyEnv: "SCNET_API_KEY", authHeader: true })),
];

let mockCatalogLoad: Promise<void> | undefined;
let mockCatalogs: Record<string, NonNullable<ProviderPresetView["catalog"]>> = {};
function loadMockProviderCatalog(): Promise<void> {
  return mockCatalogLoad ??= import("./providerCatalog.generated.json").then(({default: data}) => {
    mockCatalogs = data.catalogs;
    for (const template of data.templates) {
      if (!mockProviderPresetTemplates.some(p => p.id === template.id)) {
        mockProviderPresetTemplates.push(mockPreset(template.id, template.label, template.description, template.keyEnv, mockProviderTemplate(template.provider)));
      }
    }
  });
}

function mockProviderPresetViews(): ProviderPresetView[] {
  return [...mockProviderPresetTemplates].sort((a, b) => mockProviderPresetDisplayRank(a.id) - mockProviderPresetDisplayRank(b.id)).map((template) => ({
    catalog: mockCatalogs[template.id],
    id: template.id,
    label: template.label,
    description: template.description,
    keyEnv: template.keyEnv,
    recommended: Boolean(template.recommended),
    billingMode: template.billingMode,
    displayGroup: template.displayGroup,
    displaySection: template.displaySection,
    displayTier: template.displayTier,
    routeKind: template.routeKind,
    optional: Boolean(template.optional),
    displayOrder: template.displayOrder,
    providerNames: [...(template.providers ?? [template.provider]).map((provider) => provider.name)],
    models: [...(template.providers ?? [template.provider]).flatMap((provider) => provider.models)],
    added: false,
    status: "available",
    statusProviderNames: [],
    keySet: false,
    requiresKey: true,
    configured: false,
  }));
}

function mockProviderPresetDisplayRank(id: string): number {
  if (id === "opencode-go-recommended") return -3;
  if (id === "deepseek-responses") return -2;
  if (id === "glm-cn" || id === "zai-global" || id.startsWith("glm-coding-plan-") || id.startsWith("zai-coding-plan-")) return 0;
  if (id.startsWith("longcat-")) return 1;
  if (id === "token-rhythm") return 1;
  if (id.startsWith("kimi-")) return 2;
  if (id.startsWith("minimax-")) return 3;
  return 4;
}

function cloneMockProviderTemplates(id: string, key: string): ProviderView[] | undefined {
  const template = mockProviderPresetTemplates.find((candidate) => candidate.id === id);
  if (!template) return undefined;
  return (template.providers ?? [template.provider]).map((provider) => ({
    ...JSON.parse(JSON.stringify(provider)) as ProviderView,
    keySet: Boolean(key.trim()),
  }));
}

const mockPreviewImageDataURL =
  "data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='160' height='120' viewBox='0 0 160 120'%3E%3Cdefs%3E%3ClinearGradient id='g' x1='0' y1='0' x2='1' y2='1'%3E%3Cstop offset='0' stop-color='%23f97316'/%3E%3Cstop offset='1' stop-color='%232563eb'/%3E%3C/linearGradient%3E%3C/defs%3E%3Crect width='160' height='120' rx='14' fill='url(%23g)'/%3E%3Ccircle cx='44' cy='38' r='16' fill='%23fff7ed' opacity='.9'/%3E%3Cpath d='M18 96 62 58l24 22 18-16 38 32z' fill='%23ffffff' opacity='.9'/%3E%3C/svg%3E";

function mockExternalOpenerIconDataURL(color: string, label: string): string {
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="${color}"/><text x="32" y="40" text-anchor="middle" font-family="system-ui" font-size="25" font-weight="700" fill="white">${label}</text></svg>`;
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}
function makeMockApp(): AppBindings {
  const scenario = mockScenario();
  // Both bridge families publish into the same catalog, as ListTabs does in
  // the desktop backend. A remote event is not a second source of tab state.
  const remoteProjects = createMockRemoteProjects({
    get: id => { const tab = mockTabs.find(item => item.id === id); return tab && { ...tab }; },
    publish: tab => {
      const existing = mockTabs.some(item => item.id === tab.id);
      mockTabs = mockTabs.map(item => item.id === tab.id ? { ...tab } : tab.active ? { ...item, active: false } : item);
      if (!existing) mockTabs.push({ ...tab });
    },
    remove: id => {
      if (!mockTabs.some(tab => tab.id === id)) return;
      if (mockTabs.length === 1) throw new Error("cannot close the last tab");
      const index = mockTabs.findIndex(tab => tab.id === id), active = mockTabs[index].active;
      mockTabs = mockTabs.filter(tab => tab.id !== id);
      if (active) setMockActiveTab(mockTabs[Math.min(index, mockTabs.length - 1)].id);
    },
  });
  const freshMock = scenario === "fresh";
  const guidanceMock = scenario === "guidance", recoveryMock = typeof import.meta.env !== "undefined" && import.meta.env.DEV && scenario === "recovery";
  const runningMock = scenario === "running" || guidanceMock;
  const sandboxEscapeMock = scenario === "sandbox_escape";
  const noticePreviewMock = scenario === "notice";
  const deepSeekUpgradeMock = scenario === "deepseek_upgrade";
  const benchMock = scenario === "bench";
  const mockAttachmentDataURLs = new Map<string, string>();
  let cancelled = false;
  let pendingAskPreview = false, pendingApprovalPreview = false;
  // Mirrors the last emitted approval preview so mode switches can mirror the
  // backend drain contract: only non-fresh tools auto-allow; plan/sandbox
  // escape prompts stay pending and visible.
  let pendingApprovalPreviewPrompt: { id: string; tool: string } | undefined;
  const globalWorkspaceRoot = "~/Library/Application Support/reasonix/global-workspace";
  let cwd = freshMock ? globalWorkspaceRoot : "~/projects/joyquant-db"; // mutable so PickWorkspace is visible in dev
  let workspaces = freshMock ? [] : ["~/projects/joyquant-db", "~/projects/joyquant-sys", "~/projects/reasonix", "~/projects/blade"];
  let mockEffort = "auto";
  let mockDesktopZoomFactor = 1.0;
  let mockActiveThemeId = "";
  let mockBaseStyle = "graphite";
  let mockThemeMode: "auto" | "light" | "dark" = "dark";
  let mockHeartbeatRevision = 0;
  let mockHeartbeatTasks: unknown[] = [];
  // Vite rewrites these literal asset URLs in both dev and production builds.
  // Keeping them on the browser mock makes local visual acceptance match the
  // Wails bridge, whose ListThemePacks response carries the same two URLs.
  const mockOfficialThemeAssets = {
    "official-rose-dawn": {
      previewUrl: new URL("../../../themes/official/official-rose-dawn/preview.webp", import.meta.url).href,
      backgroundUrl: new URL("../../../themes/official/official-rose-dawn/background.webp", import.meta.url).href,
    },
    "official-fortune-forge": {
      previewUrl: new URL("../../../themes/official/official-fortune-forge/preview.webp", import.meta.url).href,
      backgroundUrl: new URL("../../../themes/official/official-fortune-forge/background.webp", import.meta.url).href,
    },
    "official-crimson-horizon": {
      previewUrl: new URL("../../../themes/official/official-crimson-horizon/preview.webp", import.meta.url).href,
      backgroundUrl: new URL("../../../themes/official/official-crimson-horizon/background.webp", import.meta.url).href,
    },
    "official-sage-breeze": {
      previewUrl: new URL("../../../themes/official/official-sage-breeze/preview.webp", import.meta.url).href,
      backgroundUrl: new URL("../../../themes/official/official-sage-breeze/background.webp", import.meta.url).href,
    },
    "official-spark-notebook": {
      previewUrl: new URL("../../../themes/official/official-spark-notebook/preview.webp", import.meta.url).href,
      backgroundUrl: new URL("../../../themes/official/official-spark-notebook/background.webp", import.meta.url).href,
    },
    "official-violet-starlight": {
      previewUrl: new URL("../../../themes/official/official-violet-starlight/preview.webp", import.meta.url).href,
      backgroundUrl: new URL("../../../themes/official/official-violet-starlight/background.webp", import.meta.url).href,
    },
    "official-cyan-stage": {
      previewUrl: new URL("../../../themes/official/official-cyan-stage/preview.webp", import.meta.url).href,
      backgroundUrl: new URL("../../../themes/official/official-cyan-stage/background.webp", import.meta.url).href,
    },
    "official-noir-gold": {
      previewUrl: new URL("../../../themes/official/official-noir-gold/preview.webp", import.meta.url).href,
      backgroundUrl: new URL("../../../themes/official/official-noir-gold/background.webp", import.meta.url).href,
    },
  } as const;
  registerTrustedThemeBackgroundURLs(Object.values(mockOfficialThemeAssets).map((asset) => asset.backgroundUrl));
  let mockThemePacks: import("./themePack").ThemePackView[] = [
    { id: "graphite", name: "Graphite", author: "Reasonix", baseStyle: "graphite", builtin: true, kind: "base", active: false, hasBackground: false, tokens: {}, recipes: { density: "comfortable", corners: "soft" } },
    { id: "aurora", name: "Aurora", author: "Reasonix", baseStyle: "aurora", builtin: true, kind: "base", active: false, hasBackground: false, tokens: {}, recipes: { density: "comfortable", corners: "soft" } },
    { id: "slate", name: "Slate", author: "Reasonix", baseStyle: "slate", builtin: true, kind: "base", active: false, hasBackground: false, tokens: {}, recipes: { density: "comfortable", corners: "soft" } },
    { id: "carbon", name: "Carbon", author: "Reasonix", baseStyle: "carbon", builtin: true, kind: "base", active: false, hasBackground: false, tokens: {}, recipes: { density: "comfortable", corners: "soft" } },
    { id: "nocturne", name: "Nocturne", author: "Reasonix", baseStyle: "nocturne", builtin: true, kind: "base", active: false, hasBackground: false, tokens: {}, recipes: { density: "comfortable", corners: "soft" } },
    { id: "amber", name: "Amber", author: "Reasonix", baseStyle: "amber", builtin: true, kind: "base", active: false, hasBackground: false, tokens: {}, recipes: { density: "comfortable", corners: "soft" } },
    { ...mockOfficialThemeAssets["official-rose-dawn"], id: "official-rose-dawn", name: "Rose Dawn", author: "Reasonix Contributors", license: "MIT", baseStyle: "graphite", builtin: true, kind: "official", active: false, hasBackground: true, nameKey: "settings.themes.official.official-rose-dawn.name", descriptionKey: "settings.themes.official.official-rose-dawn.description", tokens: { light: { bg: "#FFF7F8", fg: "#3A252C", accent: "#B43F65" }, dark: { bg: "#1E1419", fg: "#FFF3F6", accent: "#E26D91" } }, recipes: { density: "comfortable", corners: "round" }, background: { focusX: 0.72, focusY: 0.43, safeArea: "left", homeOpacity: 1, taskOpacity: 0.2, overlayStrength: 0.68, paneOpacity: 0.50 } },
    { ...mockOfficialThemeAssets["official-fortune-forge"], id: "official-fortune-forge", name: "Fortune Forge", author: "Reasonix Contributors", license: "MIT", baseStyle: "amber", builtin: true, kind: "official", active: false, hasBackground: true, nameKey: "settings.themes.official.official-fortune-forge.name", descriptionKey: "settings.themes.official.official-fortune-forge.description", tokens: { light: { bg: "#FFF8E8", fg: "#382116", accent: "#A92D22" }, dark: { bg: "#1D140D", fg: "#FFF2D1", accent: "#E8AD38" } }, recipes: { density: "comfortable", corners: "soft" }, background: { focusX: 0.74, focusY: 0.44, safeArea: "left", homeOpacity: 1, taskOpacity: 0.2, overlayStrength: 0.7, paneOpacity: 0.50 } },
    { ...mockOfficialThemeAssets["official-crimson-horizon"], id: "official-crimson-horizon", name: "Crimson Horizon", author: "Reasonix Contributors", license: "MIT", baseStyle: "graphite", builtin: true, kind: "official", active: false, hasBackground: true, nameKey: "settings.themes.official.official-crimson-horizon.name", descriptionKey: "settings.themes.official.official-crimson-horizon.description", tokens: { light: { bg: "#FFF8F7", fg: "#301D1D", accent: "#B92B38" }, dark: { bg: "#190D11", fg: "#FFF1F2", accent: "#FF6772" } }, recipes: { density: "comfortable", corners: "soft" }, background: { focusX: 0.75, focusY: 0.45, safeArea: "left", homeOpacity: 0.98, taskOpacity: 0.22, overlayStrength: 0.66, paneOpacity: 0.50 } },
    { ...mockOfficialThemeAssets["official-sage-breeze"], id: "official-sage-breeze", name: "Sage Breeze", author: "Reasonix Contributors", license: "MIT", baseStyle: "slate", builtin: true, kind: "official", active: false, hasBackground: true, nameKey: "settings.themes.official.official-sage-breeze.name", descriptionKey: "settings.themes.official.official-sage-breeze.description", tokens: { light: { bg: "#F7F7EF", fg: "#26332D", accent: "#47735F" }, dark: { bg: "#101814", fg: "#EEF6F0", accent: "#84CBA7" } }, recipes: { density: "comfortable", corners: "soft" }, background: { focusX: 0.73, focusY: 0.44, safeArea: "left", homeOpacity: 1, taskOpacity: 0.2, overlayStrength: 0.68, paneOpacity: 0.50 } },
    { ...mockOfficialThemeAssets["official-spark-notebook"], id: "official-spark-notebook", name: "Spark Notebook", author: "Reasonix Contributors", license: "MIT", baseStyle: "aurora", builtin: true, kind: "official", active: false, hasBackground: true, nameKey: "settings.themes.official.official-spark-notebook.name", descriptionKey: "settings.themes.official.official-spark-notebook.description", tokens: { light: { bg: "#FFF9ED", fg: "#2B2F35", accent: "#007B78" }, dark: { bg: "#14171A", fg: "#F8F5E9", accent: "#42D1C6" } }, recipes: { density: "comfortable", corners: "round" }, background: { focusX: 0.74, focusY: 0.46, safeArea: "left", homeOpacity: 0.98, taskOpacity: 0.2, overlayStrength: 0.68, paneOpacity: 0.50 } },
    { ...mockOfficialThemeAssets["official-violet-starlight"], id: "official-violet-starlight", name: "Violet Starlight", author: "Reasonix Contributors", license: "MIT", baseStyle: "nocturne", builtin: true, kind: "official", active: false, hasBackground: true, nameKey: "settings.themes.official.official-violet-starlight.name", descriptionKey: "settings.themes.official.official-violet-starlight.description", tokens: { light: { bg: "#F7F4FF", fg: "#251F3C", accent: "#6242C7" }, dark: { bg: "#0C1022", fg: "#F4F2FF", accent: "#9B86FF" } }, recipes: { density: "comfortable", corners: "round" }, background: { focusX: 0.73, focusY: 0.44, safeArea: "left", homeOpacity: 0.96, taskOpacity: 0.18, overlayStrength: 0.72, paneOpacity: 0.50 } },
    { ...mockOfficialThemeAssets["official-cyan-stage"], id: "official-cyan-stage", name: "Cyan Stage", author: "Reasonix Contributors", license: "MIT", baseStyle: "carbon", builtin: true, kind: "official", active: false, hasBackground: true, nameKey: "settings.themes.official.official-cyan-stage.name", descriptionKey: "settings.themes.official.official-cyan-stage.description", tokens: { light: { bg: "#F1FCFD", fg: "#173238", accent: "#007C92" }, dark: { bg: "#07181D", fg: "#E9FCFF", accent: "#37D7E4" } }, recipes: { density: "comfortable", corners: "round" }, background: { focusX: 0.74, focusY: 0.45, safeArea: "left", homeOpacity: 0.96, taskOpacity: 0.18, overlayStrength: 0.72, paneOpacity: 0.50 } },
    { ...mockOfficialThemeAssets["official-noir-gold"], id: "official-noir-gold", name: "Noir Gold", author: "Reasonix Contributors", license: "MIT", baseStyle: "carbon", builtin: true, kind: "official", active: false, hasBackground: true, nameKey: "settings.themes.official.official-noir-gold.name", descriptionKey: "settings.themes.official.official-noir-gold.description", tokens: { light: { bg: "#FCF8EE", fg: "#2A241B", accent: "#7A5A16" }, dark: { bg: "#0D0B09", fg: "#F8F1DF", accent: "#D9B45B" } }, recipes: { density: "comfortable", corners: "soft" }, background: { focusX: 0.73, focusY: 0.43, safeArea: "left", homeOpacity: 0.94, taskOpacity: 0.18, overlayStrength: 0.74, paneOpacity: 0.50 } },
  ];
  const day = 86_400_000;
  const t0 = Date.now();
  // Mutable so MCP add/remove/retry are observable in browser dev.
  let capServers: ServerView[] = [
    {
      name: "project-knowledge",
      transport: "http",
      status: "connected",
      configured: true,
      autoStart: true,
      tier: "background",
      source: "project",
      configSource: "reasonix.toml",
      url: "https://mcp.example.test/project",
      tools: 3,
      prompts: 0,
      resources: 1,
      toolList: [
        { name: "search_knowledge", description: "Search the project knowledge base.", readOnlyHint: true },
        { name: "get_document", description: "Read a knowledge-base document.", readOnlyHint: true },
        { name: "list_topics", description: "List available knowledge topics.", readOnlyHint: true },
      ],
    },
    {
      name: "github",
      transport: "stdio",
      status: "connected",
      configured: true,
      autoStart: true,
      tier: "background",
      command: "npx",
      args: ["-y", "@modelcontextprotocol/server-github"],
      tools: 4,
      prompts: 2,
      resources: 0,
      toolList: [
        { name: "issue_read", description: "Read GitHub issue details and comments.", readOnlyHint: true },
        { name: "pull_request_read", description: "Read pull request metadata, files, and review threads.", readOnlyHint: true },
        { name: "search_issues", description: "Search issues and pull requests.", readOnlyHint: true },
        { name: "issue_write", description: "Create or update GitHub issues." },
      ],
    },
    {
      name: "linear",
      transport: "http",
      status: "initializing",
      configured: true,
      autoStart: true,
      tier: "background",
      url: "https://mcp.linear.app/mcp",
      authStatus: "possible",
      authUrl: "https://mcp.linear.app/mcp",
      tools: 8,
      prompts: 0,
      resources: 0,
      toolList: [
        { name: "list_issues", description: "List and filter Linear issues." },
        { name: "get_issue", description: "Fetch a Linear issue by id or key." },
        { name: "create_issue", description: "Create a Linear issue." },
        { name: "update_issue", description: "Update status, assignee, priority, or labels." },
        { name: "list_projects", description: "List Linear projects." },
        { name: "get_project", description: "Fetch project details." },
        { name: "list_teams", description: "List Linear teams." },
        { name: "search", description: "Search Linear workspace objects." },
      ],
    },
    { name: "figma", transport: "http", status: "failed", configured: true, autoStart: true, tier: "background", url: "https://mcp.figma.com/mcp", authStatus: "required", authUrl: "https://mcp.figma.com/mcp", tools: 0, prompts: 0, resources: 0, error: "connect: 401 unauthorized" },
  ];
  const capSkills: SkillView[] = [
    {
      name: "explore", description: "Investigate the codebase in an isolated subagent", scope: "builtin", runAs: "subagent", enabled: true,
      allowedTools: ["read_file", "ls", "glob", "grep", "code_index"], invocation: "/explore", invocationMode: "auto",
      configuredModel: "deepseek/deepseek-v4-pro", configuredEffort: "high",
    },
    { name: "research", description: "Combine web_fetch + code reading in an isolated subagent", scope: "builtin", runAs: "subagent", enabled: true, allowedTools: ["read_file", "ls", "glob", "grep", "code_index", "web_fetch"], invocation: "/research", invocationMode: "auto" },
    { name: "review", description: "Review the staged diff", scope: "project", sourceDir: "~/projects/reasonix/.reasonix/skills", runAs: "inline", enabled: false, invocation: "/review" },
    { name: "init", description: "Scaffold a REASONIX.md for this repo", scope: "builtin", runAs: "inline", enabled: true, invocation: "/init" },
    {
      name: "my-formatter", description: "Formats code the way I like it", scope: "global", sourceDir: "~/.reasonix/skills", runAs: "subagent", enabled: true,
      model: "deepseek-pro", effort: "high", allowedTools: ["read_file", "edit_file"], color: "amber", invocation: "/my-formatter", invocationMode: "manual",
      body: "You are a code formatting assistant. Reformat the given file to match project style without changing behavior.",
    },
  ];
  let capSkillRoots: SkillRootView[] = [
    { dir: "~/projects/reasonix/.reasonix/skills", scope: "project", priority: 1, status: "missing", enabled: true, configured: false, removable: true, skills: 0 },
    {
      dir: "~/my-skills",
      scope: "custom",
      priority: 5,
      status: "ok",
      enabled: true,
      configured: true,
      removable: true,
      skills: 1,
      skillItems: [{ name: "review", description: "Review the staged diff", scope: "custom", runAs: "inline" }],
    },
    {
      dir: "~/.reasonix/skills",
      scope: "global",
      priority: 6,
      status: "ok",
      enabled: true,
      configured: false,
      removable: true,
      skills: 2,
      skillItems: [
        { name: "explore", description: "Investigate the codebase in an isolated subagent", scope: "global", runAs: "subagent" },
        { name: "init", description: "Scaffold a REASONIX.md for this repo", scope: "global", runAs: "inline" },
      ],
    },
  ];
  let capPlugins: PluginView[] = [];
  const mockSwitchWorkspace = async (path: string) => {
    cwd = path || "~";
    workspaces = [cwd, ...workspaces.filter((p) => p !== cwd)].slice(0, 12);
    if (!mockProjectTree.some((node) => node.kind === "project" && node.root === cwd)) {
      mockProjectTree.unshift({
        key: `project_${cwd}`,
        kind: "project",
        label: baseName(cwd),
        root: cwd,
        children: [],
      });
    }
    return cwd;
  };
  // Mutable so delete/rename are observable in browser dev.
  const sessions: SessionMeta[] = [
    { path: "/mock/sessions/a.jsonl", preview: "fix the login bug in auth.go", turns: 12, createdAt: t0 - 2 * day, lastActivityAt: t0 - 3_600_000, modTime: t0 - 3_600_000, current: true, open: true },
    { path: "/mock/sessions/b-recovery-0123456789abcdef.jsonl", preview: "refactor the payment module", turns: 5, createdAt: t0 - 3 * day, lastActivityAt: t0 - 6 * 3_600_000, modTime: t0 - 6 * 3_600_000, current: false, open: true, recovered: true, recoveryCopy: true },
    { path: "/mock/sessions/c.jsonl", preview: "write the README and badges", turns: 8, createdAt: t0 - 4 * day, lastActivityAt: t0 - day - 3_600_000, modTime: t0 - day - 3_600_000, current: false, open: false },
    { path: "/mock/sessions/d.jsonl", preview: "explain the plugin host design", turns: 3, createdAt: t0 - 5 * day, lastActivityAt: t0 - 4 * day, modTime: t0 - 4 * day, current: false, open: false },
  ];
  const trashedSessions: SessionMeta[] = [
    {
      path: "/mock/sessions/.trash/trash-dev-standard.jsonl",
      title: t("mock.trashDevStandardTitle"),
      preview: t("mock.trashDevStandardPreview"),
      turns: 4,
      createdAt: t0 - 8 * day,
      lastActivityAt: t0 - 7 * day,
      modTime: t0 - 7 * day,
      deletedAt: t0 - 20 * 60_000,
      current: false,
      open: false,
      scope: "project",
      workspaceRoot: "~/projects/joyquant-db",
      topicId: "topic_dev_standard",
      topicTitle: t("mock.trashDevStandardTitle"),
    },
    {
      path: "/mock/sessions/.trash/trash-p3a-review.jsonl",
      title: t("mock.trashP3aTitle"),
      preview: t("mock.trashP3aPreview"),
      turns: 7,
      createdAt: t0 - 6 * day,
      lastActivityAt: t0 - 5 * day,
      modTime: t0 - 5 * day,
      deletedAt: t0 - 2 * 3_600_000,
      current: false,
      open: false,
      scope: "project",
      workspaceRoot: "~/projects/joyquant-sys",
      topicId: "topic_p3a_pd",
      topicTitle: t("mock.trashP3aTitle"),
    },
    {
      path: "/mock/sessions/.trash/trash-global-product.jsonl",
      title: t("mock.trashGlobalProductTitle"),
      preview: t("mock.trashGlobalProductPreview"),
      turns: 2,
      createdAt: t0 - 4 * day,
      lastActivityAt: t0 - 3 * day,
      modTime: t0 - 3 * day,
      deletedAt: t0 - day,
      current: false,
      open: false,
      scope: "global",
      topicId: "topic_product",
      topicTitle: t("mock.trashGlobalProductTitle"),
      recovered: true,
      recoveryCopy: true,
    },
  ];
  if (freshMock) {
    sessions.splice(0);
    trashedSessions.splice(0);
  }
  // Mutable settings so the Settings panel's edits are observable in browser dev.
  const settings: SettingsView = {
    defaultModel: "deepseek",
    plannerModel: "",
    visionModel: "",
    subagentModel: "",
    subagentEffort: "",
    autoPlan: "off",
    providers: [
      { name: "deepseek", builtIn: true, added: deepSeekUpgradeMock, kind: "openai", baseUrl: "https://api.deepseek.com", modelsUrl: "", models: ["deepseek-v4-flash"], visionModels: [], visionModelsConfigured: false, default: "deepseek-v4-flash", apiKeyEnv: "DEEPSEEK_API_KEY", headers: deepSeekUpgradeMock ? { "X-Route": "official-custom" } : undefined, keySet: true, balanceUrl: "https://api.deepseek.com/user/balance", contextWindow: 1_000_000, reasoningProtocol: "", thinking: "enabled", webSearch: true, serverWebSearchCapability: true, supportedEfforts: ["disabled", "low", "high", "max"], defaultEffort: "high", recommendedUpgradeAvailable: false },
    ],
    officialProviders: [
      { name: "deepseek", builtIn: true, added: false, kind: "openai", baseUrl: "https://api.deepseek.com", modelsUrl: "", models: ["deepseek-v4-flash", "deepseek-v4-pro"], visionModels: [], visionModelsConfigured: false, default: "deepseek-v4-flash", apiKeyEnv: "DEEPSEEK_API_KEY", keySet: true, balanceUrl: "https://api.deepseek.com/user/balance", contextWindow: 1_000_000, reasoningProtocol: "", thinking: "enabled", webSearch: true, serverWebSearchCapability: true, supportedEfforts: ["disabled", "low", "high", "max"], defaultEffort: "high" },
    ],
    providerPresets: mockProviderPresetViews(),
    permissions: { mode: "ask", allow: ["ls", "read_file"], ask: [], deny: ["Bash(rm:*)"] },
    sandbox: { bash: browserPreviewBashSandboxMode(), network: true, workspaceRoot: "", allowWrite: [], effectiveWorkspaceRoot: cwd, effectiveWriteRoots: [cwd], shell: "auto", effectiveShell: browserPreviewEffectiveShell("auto"), resolvedShell: browserPreviewEffectiveShell("auto"), shellReloadRequired: false, ...browserPreviewShellSupport(browserPlatformOverride()) },
    network: {
      proxyMode: "auto",
      proxyUrl: "",
      noProxy: "",
      proxy: { type: "socks5", server: "127.0.0.1", port: 7890, username: "", password: "" },
    },
    agent: { temperature: 0.2, maxSteps: 0, plannerMaxSteps: 0, maxSubagentDepth: 2, maxSubagentConcurrency: 6, maxParallelWriters: 3, systemPrompt: "You are Reasonix, a coding agent.", reasoningLanguage: "auto", compactRatio: 0.8 },
    bot: {
      enabled: !freshMock,
      model: "",
      toolApprovalMode: "ask",
      maxSteps: 0,
      debounceMs: 1500,
      queueMode: "steer",
      queueCap: 20,
      queueDrop: "summarize",
      ignoreSelfMessages: true,
      selfUserIds: {
        qq: [],
        feishu: [],
        weixin: [],
        dingtalk: [],
      },
      control: {
        enabled: false,
        addr: "127.0.0.1:37913",
        tokenEnv: "REASONIX_BOT_CONTROL_TOKEN",
      },
      pairing: {
        enabled: true,
        requestTtlMinutes: 60,
        maxPendingPerPlatform: 3,
      },
      routes: [],
      allowlist: {
        enabled: true,
        allowAll: false,
        qqUsers: [],
        feishuUsers: freshMock ? [] : ["ou_mock_user_001"],
        weixinUsers: freshMock ? [] : ["wxid_mock_user_001"],
        qqApprovers: [],
        feishuApprovers: [],
        weixinApprovers: [],
        qqAdmins: [],
        feishuAdmins: [],
        weixinAdmins: [],
        qqGroups: [],
        feishuGroups: [],
        weixinGroups: [],
        dingtalkUsers: [],
        dingtalkApprovers: [],
        dingtalkAdmins: [],
        dingtalkGroups: [],
      },
      qq: { enabled: false, appId: "", appSecretEnv: "QQ_BOT_APP_SECRET", secretSet: false, sandbox: false, model: "", toolApprovalMode: "ask", workspaceRoot: "", access: { enabled: true, allowAll: false, pairingEnabled: true, users: [], groups: [], approvers: [], admins: [] } },
      feishu: {
        enabled: false,
        domain: "feishu",
        appId: "",
        appSecretEnv: "FEISHU_BOT_APP_SECRET",
        secretSet: false,
        verificationToken: "",
        mode: "webhook",
        webhookPort: 8080,
        requireMention: true,
      },
      weixin: {
        enabled: false,
        accountId: "default",
        tokenEnv: "WEIXIN_BOT_TOKEN",
        tokenSet: false,
        apiBase: "https://ilinkai.weixin.qq.com",
      },
      dingtalk: {
        enabled: false,
        clientId: "",
        clientSecretEnv: "DINGTALK_CLIENT_SECRET",
        secretSet: false,
        botName: "",
        requireMention: true,
        model: "",
        toolApprovalMode: "",
        workspaceRoot: "",
        access: { enabled: true, allowAll: false, pairingEnabled: true, users: [], groups: [], approvers: [], admins: [] },
      },
      connections: freshMock ? [] : [
        {
          id: "mock-lark-kun",
          provider: "feishu",
          domain: "lark",
          label: "kun",
          enabled: true,
          status: "connected",
	          model: "",
	          toolApprovalMode: "",
	          workspaceRoot: "",
	          access: { enabled: true, allowAll: false, pairingEnabled: true, users: ["ou_mock_user_001"], groups: [], approvers: [], admins: [] },
	          credential: {
            appId: "cli_mock_lark",
            appSecretEnv: "FEISHU_BOT_APP_SECRET",
            accountId: "",
            tokenEnv: "",
            secretSet: true,
          },
          sessionMappings: [
            {
              remoteId: "ou_mock_user_001",
              sessionId: "topic:topic_product",
              sessionSource: "",
              chatType: "",
              userId: "",
              threadId: "",
              scope: "global",
              workspaceRoot: "",
              updatedAt: new Date(Date.now() - 4 * 60_000).toISOString(),
            },
          ],
          lastError: "",
          createdAt: new Date(Date.now() - 86_400_000).toISOString(),
          updatedAt: new Date(Date.now() - 4 * 60_000).toISOString(),
        },
        {
          id: "mock-weixin-kun",
          provider: "weixin",
          domain: "weixin",
          label: "kun",
          enabled: true,
          status: "connected",
	          model: "",
	          toolApprovalMode: "",
	          workspaceRoot: "",
	          access: { enabled: true, allowAll: false, pairingEnabled: true, users: ["wxid_mock_user_001"], groups: [], approvers: [], admins: [] },
	          credential: {
            appId: "",
            appSecretEnv: "",
            accountId: "default",
            tokenEnv: "WEIXIN_BOT_TOKEN",
            secretSet: true,
          },
          sessionMappings: [
            {
              remoteId: "wxid_mock_user_001",
              sessionId: "topic:topic_ai",
              sessionSource: "",
              chatType: "",
              userId: "",
              threadId: "",
              scope: "global",
              workspaceRoot: "",
              updatedAt: new Date(Date.now() - 12 * 60_000).toISOString(),
            },
          ],
          lastError: "",
          createdAt: new Date(Date.now() - 86_400_000).toISOString(),
          updatedAt: new Date(Date.now() - 12 * 60_000).toISOString(),
        },
      ],
    },
    desktopLanguage: "",
    desktopCurrency: "",
    desktopLayoutStyle: browserMockDesktopLayoutStyle(),
    desktopTheme: "auto",
    desktopThemeStyle: "graphite",
    desktopTerminalTheme: "auto",
    conversationWidth: "standard",
    closeBehavior: "background",
    displayMode: "standard", sessionExperience: "standard", reasoningDisplayMode: "auto", reasoningDisplayModeExplicit: false,
    statusBarStyle: "text",
    statusBarItems: [...DEFAULT_STATUS_BAR_ITEMS],
    defaultToolApprovalMode: "auto",
    checkUpdates: true,
    updateChannel: "stable",
    telemetry: true,
    metrics: true,
    configPath: "~/.reasonix/config.toml",
    shadowedByPath: "~/projects/reasonix/reasonix.toml",
    providerKinds: ["openai", "anthropic", "responses"],
    autoApproveTools: false,
    bypass: false,
  };
  const hookEvents = ["PreToolUse", "PostToolUse", "UserPromptSubmit", "Stop", "PostLLMCall", "SessionStart", "SessionEnd", "SubagentStop", "Notification", "PreCompact"];
  const hookSettings: Record<string, HooksSettingsView> = {
    global: {
      scope: "global",
      path: "~/.reasonix/settings.json",
      projectRoot: "",
      trusted: true,
      events: hookEvents,
      hooks: [
        { event: "Stop", command: "echo turn done", description: "Notify after each turn" },
      ],
    },
    project: {
      scope: "project",
      path: "./.reasonix/settings.json",
      projectRoot: "/mock/project",
      trusted: false,
      events: hookEvents,
      hooks: [],
    },
  };
  settings.providers = settings.providers.map((provider) =>
    provider.apiKeyEnv === "DEEPSEEK_API_KEY" ? { ...provider, keySet: !freshMock } : provider,
  );
  if (freshMock) {
    settings.configPath = "~/.reasonix/config.toml";
    settings.shadowedByPath = "";
  }
  const mockNow = Date.now();
  const mockProjectTree: ProjectNode[] = freshMock ? [] : benchMock ? [
    {
      key: "project_~/projects/reasonix",
      kind: "project",
      label: "reasonix",
      root: "~/projects/reasonix",
      projectColor: "purple",
      children: [
        { key: "topic_bench_markdown", kind: "topic", label: "● bench:markdown-46t", root: "~/projects/reasonix", topicId: "topic_bench_markdown", projectColor: "purple", turns: 46, lastActivityAt: mockNow - 60_000, open: true },
        { key: "topic_bench_tools", kind: "topic", label: "● bench:tools-38t", root: "~/projects/reasonix", topicId: "topic_bench_tools", projectColor: "blue", turns: 38, lastActivityAt: mockNow - 120_000, open: true },
        { key: "topic_bench_small", kind: "topic", label: "bench:small-6t", root: "~/projects/reasonix", topicId: "topic_bench_small", projectColor: "green", turns: 6, lastActivityAt: mockNow - 180_000 },
        { key: "topic_bench_giant_turn", kind: "topic", label: "bench:giant-turn", root: "~/projects/reasonix", topicId: "topic_bench_giant_turn", projectColor: "amber", turns: 1, lastActivityAt: mockNow - 240_000 },
        { key: "topic_bench_windowed", kind: "topic", label: "bench:windowed-1000t", root: "~/projects/reasonix", topicId: "topic_bench_windowed", projectColor: "blue", turns: 1000, lastActivityAt: mockNow - 270_000 },
        { key: "topic_bench_reported_long_turn", kind: "topic", label: "bench:reported-long-turn", root: "~/projects/reasonix", topicId: "topic_bench_reported_long_turn", projectColor: "amber", turns: 1, lastActivityAt: mockNow - 300_000 },
        { key: "topic_bench_geometry_contract", kind: "topic", label: "bench:geometry-blocks", root: "~/projects/reasonix", topicId: "topic_bench_geometry_contract", projectColor: "amber", turns: 1, lastActivityAt: mockNow - 330_000 },
        { key: "topic_bench_storm", kind: "topic", label: "bench:storm-40t", root: "~/projects/reasonix", topicId: "topic_bench_storm", projectColor: "red", turns: 40, lastActivityAt: mockNow - 360_000 },
        { key: "topic_bench_selection_table", kind: "topic", label: "bench:selection-table", root: "~/projects/reasonix", topicId: "topic_bench_selection_table" },
      ],
    },
  ] : [
    {
      key: "project_~/projects/joyquant-db",
      kind: "project",
      label: t("mock.projectJoyquantDb"),
      root: "~/projects/joyquant-db",
      projectColor: "blue",
      children: [
        { key: "topic_dev_standard", kind: "topic", label: `● ${t("mock.topicDevStandard")}`, root: "~/projects/joyquant-db", topicId: "topic_dev_standard", projectColor: "blue", turns: 18, createdAt: mockNow - 3 * 24 * 60 * 60_000, lastActivityAt: mockNow - 8 * 60_000, open: true, running: runningMock },
        { key: "topic_db_maint", kind: "topic", label: t("mock.topicDbMaint"), root: "~/projects/joyquant-db", topicId: "topic_db_maint", projectColor: "blue", turns: 7, createdAt: mockNow - 2 * 24 * 60 * 60_000, lastActivityAt: mockNow - 2 * 60 * 60_000 },
        { key: "topic_env", kind: "topic", label: t("mock.topicEnv"), root: "~/projects/joyquant-db", topicId: "topic_env", projectColor: "blue", turns: 3, createdAt: mockNow - 24 * 60 * 60_000, lastActivityAt: mockNow - 26 * 60 * 60_000 },
      ],
    },
    {
      key: "project_~/projects/joyquant-sys",
      kind: "project",
      label: t("mock.projectJoyquantSys"),
      root: "~/projects/joyquant-sys",
      projectColor: "purple",
      children: [
        { key: "topic_p3b_pd", kind: "topic", label: `● ${t("mock.topicP3b")}`, root: "~/projects/joyquant-sys", topicId: "topic_p3b_pd", projectColor: "purple", turns: 11, lastActivityAt: mockNow - 3 * 24 * 60 * 60_000, status: runningMock ? "streaming" : undefined },
        { key: "topic_p3a_pd", kind: "topic", label: t("mock.topicP3a"), root: "~/projects/joyquant-sys", topicId: "topic_p3a_pd", projectColor: "purple", turns: 9, lastActivityAt: mockNow - 4 * 24 * 60 * 60_000, status: runningMock ? "thinking" : undefined },
        { key: "topic_hotfix", kind: "topic", label: t("mock.topicHotfix"), root: "~/projects/joyquant-sys", topicId: "topic_hotfix", projectColor: "purple", turns: 4, lastActivityAt: mockNow - 5 * 24 * 60 * 60_000, status: runningMock ? "thinking" : undefined },
        { key: "topic_sys_coord", kind: "topic", label: t("mock.topicSysCoord"), root: "~/projects/joyquant-sys", topicId: "topic_sys_coord", projectColor: "purple", turns: 14, lastActivityAt: mockNow - 6 * 24 * 60 * 60_000, status: runningMock ? "waiting_confirmation" : undefined },
        { key: "topic_sys_standard", kind: "topic", label: t("mock.topicSysStandard"), root: "~/projects/joyquant-sys", topicId: "topic_sys_standard", projectColor: "purple", turns: 6, lastActivityAt: mockNow - 7 * 24 * 60 * 60_000, status: "paused" },
        { key: "topic_sys_exception", kind: "topic", label: t("mock.topicSysException"), root: "~/projects/joyquant-sys", topicId: "topic_sys_exception", projectColor: "purple", turns: 2, lastActivityAt: mockNow - 8 * 24 * 60 * 60_000, status: "error" },
      ],
    },
    {
      key: "global_folder",
      kind: "global_folder",
      label: "Global",
      root: globalWorkspaceRoot,
      children: [
        { key: "global_topic_product", kind: "global_topic", label: t("mock.topicProduct"), topicId: "topic_product", turns: 5, lastActivityAt: mockNow - 8 * 24 * 60 * 60_000 },
        { key: "global_topic_ai", kind: "global_topic", label: t("mock.topicAi"), topicId: "topic_ai", turns: 8, lastActivityAt: mockNow - 10 * 24 * 60 * 60_000 },
        { key: "global_topic_lab", kind: "global_topic", label: t("mock.topicLab"), topicId: "topic_lab", turns: 2, lastActivityAt: mockNow - 12 * 24 * 60 * 60_000 },
      ],
    },
  ];
  const ensureMockGlobalFolder = (): ProjectNode => {
    let node = mockProjectTree.find((item) => item.kind === "global_folder");
    if (!node) {
      node = {
        key: "global_folder",
        kind: "global_folder",
        label: "Global",
        root: globalWorkspaceRoot,
        children: [],
      };
      mockProjectTree.push(node);
    }
    return node;
  };
  const mockProjectTreeForDisplay = () => {
    const pinnedProjects = mockProjectTree.filter((node) => node.kind === "project" && node.pinned);
    if (pinnedProjects.length === 0) return mockProjectTree;
    const rest = mockProjectTree.filter((node) => !(node.kind === "project" && node.pinned));
    return [...pinnedProjects, ...rest];
  };
  const cloneProjectTree = () => {
    if (mockProjectTree.length === 0) ensureMockGlobalFolder();
    return remoteProjects.appendToTree(JSON.parse(JSON.stringify(mockProjectTreeForDisplay())) as ProjectNode[]);
  };
  const projectChildren = (node: ProjectNode): ProjectNode[] => Array.isArray(node.children) ? node.children : [];
  const findMockTopic = (topicId: string): ProjectNode | null => {
    for (const parent of mockProjectTree) {
      const found = projectChildren(parent).find((child) => child.topicId === topicId);
      if (found) return found;
    }
    return null;
  };
  const setMockTopicPinned = (topicId: string, pinned: boolean) => {
    for (const parent of mockProjectTree) {
      const children = projectChildren(parent);
      const index = children.findIndex((child) => child.topicId === topicId);
      if (index < 0) continue;
      const topic = { ...children[index], pinned: pinned || undefined };
      if (!pinned) {
        parent.children = children.map((child, i) => (i === index ? topic : child));
        return;
      }
      const remaining = children.filter((_, i) => i !== index);
      parent.children = [topic, ...remaining];
      return;
    }
  };
  const setMockProjectPinned = (workspaceRoot: string, pinned: boolean) => {
    const index = mockProjectTree.findIndex((node) => node.kind === "project" && node.root === workspaceRoot);
    if (index < 0) return;
    mockProjectTree[index] = { ...mockProjectTree[index], pinned: pinned || undefined };
  };
  const deleteMockTopic = (topicId: string) => {
    for (const parent of mockProjectTree) {
      parent.children = projectChildren(parent).filter((child) => child.topicId !== topicId);
    }
  };
  const topicLabel = (topicId: string, fallback: string) => (findMockTopic(topicId)?.label || fallback).replace(/^●\s*/, "");
  const mockTopicStatus = (topicId: string) => findMockTopic(topicId)?.status ?? "";
  const mockTopicIsRunning = (topicId: string) => {
    const status = mockTopicStatus(topicId);
    return status === "streaming" || status === "thinking" || status === "waiting_confirmation";
  };
  const mockTopicIsBlank = (topicId: string) => {
    const topic = findMockTopic(topicId);
    return Boolean(topic && topic.label === t("mock.newSession") && !topic.turns && !topic.lastActivityAt && !topic.status);
  };
  const mockTopicRunsInScenario = (topicId: string) => runningMock && mockTopicIsRunning(topicId);
  const mockLongTranscriptHistory = (): HistoryMessage[] => {
    const out: HistoryMessage[] = [];
    for (let i = 1; i <= 18; i++) {
      out.push({
        role: "user",
        content: `第 ${i} 轮：检查聊天滚动定位，切换会话后应该自动停在最新消息底部。`,
        createdAt: t0 - (19 - i) * 15 * 60_000,
      });
      if (i === 4) {
        out.push({ role: "phase", content: "复现切换会话后的滚动位置" });
      }
      if (i === 8) {
        const toolID = "mock-scroll-layout-check";
        out.push({
          role: "assistant",
          content: "我会先读取滚动容器尺寸，再确认是否存在动态高度变化导致的底部偏移。",
          reasoning: "旧实现只重置 stick 标志，没有主动等待布局稳定；AskCard、Approval、Todo 这类卡片可能在下一帧改变高度。",
          toolCalls: [{ id: toolID, name: "bash", arguments: JSON.stringify({ command: "npm run check:css && pnpm typecheck" }) }],
        });
        out.push({
          role: "tool",
          toolCallId: toolID,
          toolName: "bash",
          content: "CSS syntax check passed\nz-index token check passed\ntsc --noEmit passed\n",
        });
        continue;
      }
      if (i === 13) {
        out.push({ role: "notice", level: "info", content: "模拟提示：用户向上查看历史后，右下角应出现跳到底部按钮。" });
      }
      out.push({
        role: "assistant",
        content: [
          `第 ${i} 轮结果：当前滚动契约会在切换会话或 reveal 信号到达后执行强制贴底。`,
          "它会先立即设置 scrollTop 到 scrollHeight，再连续几个 animation frame 复查，避免动态内容把底部再次推走。",
          "如果用户主动向上滚动，普通 streaming 不会强行拉回；只有点击跳到底部按钮或显式切换会话才会重新贴底。",
        ].join("\n\n"),
      });
    }
    out.push({
      role: "compaction",
      content: "",
      trigger: "manual",
      messages: 36,
      summary: "Mock 长会话用于验证桌面端 Transcript 自动贴底、多帧布局修正和跳到底部按钮。",
      archive: "mock-scroll-preview",
    });
    out.push({
      role: "assistant",
      content: "最终状态：这条消息应该位于真实底部。向上滚动后，右下角会显示跳到底部按钮；点击按钮后应回到这里。",
    });
    return out;
  };
  // Benchmark fixtures (?mock=bench) live in a lazily imported module: the
  // generators build ~1MiB of mock content and must stay out of the eager
  // bundle (initial-chunk gzip budget). See bridgeBenchFixtures.ts.
  const benchFixturesPromise = benchMock ? import("./bridgeBenchFixtures") : null;
	  const mockTopicHistory = (topicId: string): HistoryMessage[] => {
	    switch (topicId) {
      case "topic_product":
        return [
          {
            role: "user",
            content: [
              "[[reasonix-im]]",
              "provider=lark",
              "label=Feishu / Lark",
              "sender=ou_mock_user_001",
              "chat=p2p 会话",
              "[[/reasonix-im]]",
              "你可以做什么",
            ].join("\n"),
          },
          {
            role: "assistant",
            content: "这是 Global 范围下的 IM 会话。我可以先处理不依赖项目文件的问答、计划和信息整理；需要进入项目时，再由桌面端显式绑定或迁移到项目话题。",
          },
        ];
      case "topic_ai":
        return [
          {
            role: "user",
            content: [
              "[[reasonix-im]]",
              "provider=weixin",
              "label=微信",
              "sender=wxid_mock_user_001",
              "chat=单聊",
              "[[/reasonix-im]]",
              "帮我整理一下今天要做的事",
            ].join("\n"),
          },
          {
            role: "assistant",
            content: "可以。我会先在 Global 范围里整理任务清单；如果某条任务需要读取项目文件，再切到你授权的项目话题处理。",
          },
        ];
      case "topic_dev_standard":
        return mockLongTranscriptHistory();
      case "topic_p3b_pd":
        return [
          { role: "user", content: "把 p3b P&D 的范围和风险重新整理成可执行计划。" },
          { role: "phase", content: "分析需求范围" },
        ];
      case "topic_p3a_pd":
        return [
          { role: "user", content: "复盘 p3a 的技术方案，先不要写文件，先说明你的判断。" },
        ];
      case "topic_hotfix":
        return [
          { role: "user", content: "检查 post-p3-hotfix 的回归风险，重点看最近的 shell 输出和 git 改动。" },
          { role: "assistant", content: "", reasoning: "我先定位最近一次 hotfix 的上下文，然后用只读命令检查状态；左侧保持“思考中”，工具细节在这里展开。" },
        ];
      case "topic_sys_coord":
        return [
          { role: "user", content: "准备执行 joyquant-sys 的同步脚本，但需要我确认后再运行。" },
          { role: "assistant", content: "", reasoning: "这个动作会运行脚本并可能刷新本地缓存，所以需要先等用户确认。" },
        ];
      case "topic_sys_standard":
        return [
          { role: "user", content: "继续制定 SYS 项目开发规范，先停在当前检查点。" },
          { role: "assistant", content: "已暂停在规范整理阶段。当前保留了目录约定、分支策略和待确认的发布检查项；继续时可以从这里恢复。" },
          { role: "notice", level: "info", content: "会话已暂停：未继续执行命令，等待用户恢复或切换任务。" },
        ];
      case "topic_sys_exception":
        return [
          { role: "user", content: "演练异常处理流程，看看失败时界面怎么提示。" },
          { role: "assistant", content: "我尝试校验恢复脚本时遇到异常，已停止继续执行。" },
          { role: "notice", level: "warn", content: "运行异常：恢复脚本缺少必要环境变量 JOYQUANT_SYS_TOKEN。请补齐配置后重试。" },
        ];
      default:
        return [];
	    }
	  };
	  const mockHistoryPage = (messages: HistoryMessage[], beforeTurn = 0, limit = 60): HistoryPage => {
	    const totalTurns = messages.reduce((count, message) => count + (message.role === "user" ? 1 : 0), 0);
	    const safeLimit = Math.max(1, Math.min(200, Math.floor(limit || 60)));
	    const endTurn = beforeTurn > 0 && beforeTurn <= totalTurns ? beforeTurn : totalTurns;
	    const startTurn = Math.max(0, endTurn - safeLimit);
	    let turn = -1;
	    const pageMessages = messages.filter((message) => {
	      if (message.role === "user") turn += 1;
	      if (turn < 0) return startTurn === 0;
	      return turn >= startTurn && turn < endTurn;
	    });
	    return { messages: pageMessages, startTurn, endTurn, totalTurns, hasOlder: startTurn > 0 };
	  };
	  const mockRuntimeInjected = new Set<string>();
  const queueMockTopicRuntime = (tab: TabMeta) => {
    if (!runningMock) return;
    const status = mockTopicStatus(tab.topicId);
    if (status !== "streaming" && status !== "thinking" && status !== "waiting_confirmation") return;
    const key = `${tab.id}:${tab.topicId}:${status}`;
    if (mockRuntimeInjected.has(key)) return;
    mockRuntimeInjected.add(key);
    window.setTimeout(() => {
      void withMockTabScope(tab.id, async () => {
        emitMockTurnStarted();
        await delay(120);
        if (tab.topicId === "topic_p3b_pd") {
          const text = "我会先把范围拆成三层：目标、依赖、风险。当前已经确认 p3b 的交付边界，接下来补充每个模块的验收口径...";
          for (const ch of text) {
            emit({ kind: "text", text: ch });
            await delay(5);
          }
          return;
        }
        if (tab.topicId === "topic_p3a_pd") {
          emit({ kind: "reasoning", text: "我正在对比 p3a 和 p3b 的差异：先看约束，再看变更风险，最后判断是否需要拆成独立任务。\n\n" });
          await delay(220);
          emit({ kind: "reasoning", text: "当前倾向：先保留 p3a 的兼容路径，不急于删除旧逻辑。" });
          return;
        }
        if (tab.topicId === "topic_hotfix") {
          const id = "mock-hotfix-shell";
          emit({ kind: "tool_dispatch", tool: { id, name: "bash", args: JSON.stringify({ command: "git status --short && npm test" }), readOnly: true } });
          await delay(180);
          emit({ kind: "tool_progress", tool: { id, name: "bash", readOnly: true, output: "$ git status --short\n M internal/sys/runner.go\n\n$ npm test\nrunning targeted regression tests...\n" } });
          return;
        }
        if (tab.topicId === "topic_sys_coord") {
          pendingApprovalPreview = true;
          pendingApprovalPreviewPrompt = { id: "mock-sys-confirm", tool: "bash" };
          emit({ kind: "reasoning", text: "我已经准备好执行同步脚本，但这个操作会影响本地 workspace，需要用户确认。" });
          await delay(160);
          emit({
            kind: "approval_request",
            approval: {
              id: "mock-sys-confirm",
              tool: "bash",
              subject: "npm run sync:joyquant-sys\n\n该命令会同步 SYS 项目配置并刷新本地缓存。",
            },
          });
        }
      });
    }, 180);
  };
  const setMockActiveTab = (tabId: string) => {
    mockTabs = mockTabs.map((tab) => ({ ...tab, active: tab.id === tabId }));
  };
  // Single-surface prunes stash removed tabs so a later re-activation restores
  // the SAME tab identity (the real backend "opens or reuses" the topic's
  // tab), keeping the transcript store's (tabId, sessionPath) cache warm.
  const mockPrunedTabs = new Map<string, TabMeta>();
  const mockTabGraveyardKey = (tab: Pick<TabMeta, "scope" | "workspaceRoot" | "topicId">) =>
    `${tab.scope}:${tab.workspaceRoot}:${tab.topicId}`;
  const pruneMockTabsTo = (keepTabId: string) => {
    for (const tab of mockTabs) {
      if (tab.id !== keepTabId) mockPrunedTabs.set(mockTabGraveyardKey(tab), tab);
    }
    mockTabs = mockTabs.filter((item) => item.id === keepTabId).map((item) => ({ ...item, active: true }));
  };
  const restoreMockPrunedTab = (scope: string, workspaceRoot: string, topicId: string): TabMeta | undefined => {
    const key = mockTabGraveyardKey({ scope, workspaceRoot, topicId });
    const tab = mockPrunedTabs.get(key);
    if (!tab) return undefined;
    mockPrunedTabs.delete(key);
    return tab;
  };
  const currentMockTurnTabId = () => mockScopedTabId || mockTabs.find((tab) => tab.active)?.id;
  const setMockTabRunning = (tabId: string | undefined, running: boolean) => {
    if (!tabId) return;
    mockTabs = mockTabs.map((tab) => (tab.id === tabId ? { ...tab, running } : tab));
  };
  const emitMockTurnStarted = (submissionId?: string) => {
    setMockTabRunning(currentMockTurnTabId(), true);
    emit({ kind: "turn_started", submissionId });
  };
  const emitMockTurnDone = (submissionId?: string) => {
    setMockTabRunning(currentMockTurnTabId(), false);
    emit({ kind: "turn_done", submissionId });
  };
  // Fresh user decisions never auto-allow on a posture switch (mirrors the
  // backend's requiresFreshApprovalTool set).
  const mockFreshApprovalTools = new Set(["exit_plan_mode", "sandbox_escape", "memory_remember", "memory_forget", "managed_config_write"]);
  // Mirrors the backend drain contract for the mode-switch bindings: returns
  // the prompt ids the new posture auto-allowed; fresh prompts stay pending.
  const drainMockApprovalPreviews = (toolApprovalMode: string): string[] => {
    if (toolApprovalMode !== "auto" && toolApprovalMode !== "yolo") return [];
    const prompt = pendingApprovalPreviewPrompt;
    if (!pendingApprovalPreview || !prompt || mockFreshApprovalTools.has(prompt.tool)) return [];
    pendingApprovalPreview = false;
    pendingApprovalPreviewPrompt = undefined;
    emit({ kind: "message", text: `approval preview auto-allowed (${toolApprovalMode})` });
    emitMockTurnDone();
    return [prompt.id];
  };
  let mockTabs: TabMeta[] = benchMock ? [
    // Phase F benchmark scenario: the two heaviest sessions pre-opened, the
    // markdown-heavy one active (cold open renders the 500KiB answer).
    {
      id: "tab_bench_markdown",
      scope: "project",
      workspaceRoot: "~/projects/reasonix",
      workspaceName: "reasonix",
      workspacePath: "~/projects/reasonix",
      gitBranch: "bench",
      topicId: "topic_bench_markdown",
      topicTitle: "bench:markdown-46t",
      projectColor: "purple",
      label: "DeepSeek-R1",
      ready: true,
      running: false,
      mode: "normal",
      collaborationMode: "normal",
      toolApprovalMode: "ask",
      tokenMode: "full",
      active: true,
      cwd: "~/projects/reasonix",
    },
    {
      id: "tab_bench_tools",
      scope: "project",
      workspaceRoot: "~/projects/reasonix",
      workspaceName: "reasonix",
      workspacePath: "~/projects/reasonix",
      gitBranch: "bench",
      topicId: "topic_bench_tools",
      topicTitle: "bench:tools-38t",
      projectColor: "blue",
      label: "DeepSeek-R1",
      ready: true,
      running: false,
      mode: "normal",
      collaborationMode: "normal",
      toolApprovalMode: "ask",
      tokenMode: "full",
      active: false,
      cwd: "~/projects/reasonix",
    },
  ] : noticePreviewMock ? [
    {
      id: "tab_notice_preview",
      scope: "project",
      workspaceRoot: "~/projects/reasonix",
      workspaceName: "reasonix",
      workspacePath: "~/projects/reasonix",
      gitBranch: "codex/compact-chat-notices-i18n",
      topicId: "topic_notice_preview",
      topicTitle: "Compact notice preview",
      projectColor: "green",
      label: "DeepSeek-R1",
      ready: true,
      running: false,
      mode: "normal",
      collaborationMode: "normal",
      toolApprovalMode: "ask",
      tokenMode: "full",
      active: true,
      cwd: "~/projects/reasonix",
    },
  ] : freshMock ? [
    {
      id: "tab_global",
      scope: "global",
      workspaceRoot: globalWorkspaceRoot,
      workspaceName: "Global",
      workspacePath: globalWorkspaceRoot,
      topicId: "",
      topicTitle: "Global",
      label: "DeepSeek-R1",
      ready: true,
      running: false,
      mode: "normal",
      collaborationMode: "normal",
      toolApprovalMode: "ask",
      tokenMode: "full",
      active: true,
      cwd: globalWorkspaceRoot,
    },
  ] : [
    {
      id: "tab_joyquant_db",
      scope: "project",
      workspaceRoot: "~/projects/joyquant-db",
      workspaceName: "joyquant-db",
      workspacePath: "~/projects/joyquant-db",
      gitBranch: "main",
      topicId: "topic_dev_standard",
      topicTitle: t("mock.trashDevStandardTitle"),
      projectColor: "blue",
      label: "DeepSeek-R1",
      ready: true,
      running: false,
      mode: "normal",
      collaborationMode: "normal",
      toolApprovalMode: "ask",
      tokenMode: "full",
      active: !guidanceMock,
      cwd: "~/projects/joyquant-db",
    },
    {
      id: "tab_joyquant_sys",
      scope: "project",
      workspaceRoot: "~/projects/joyquant-sys",
      workspaceName: "joyquant-sys",
      workspacePath: "~/projects/joyquant-sys",
      gitBranch: "feature/p3b",
      topicId: "topic_p3b_pd",
      topicTitle: "p3b P&D",
      projectColor: "purple",
      label: "DeepSeek-R1",
      ready: true,
      running: runningMock && mockTopicIsRunning("topic_p3b_pd"),
      mode: "normal",
      collaborationMode: "normal",
      toolApprovalMode: "ask",
      tokenMode: "full",
      active: guidanceMock,
      cwd: "~/projects/joyquant-sys",
    },
    {
      id: "tab_global",
      scope: "global",
      workspaceRoot: "",
      workspaceName: "Global",
      workspacePath: "~/projects/joyquant-db",
      topicId: "topic_global",
      topicTitle: "Global",
      label: "DeepSeek-R1",
      ready: true,
      running: false,
      mode: "normal",
      collaborationMode: "normal",
      toolApprovalMode: "ask",
      tokenMode: "full",
      active: false,
      cwd: "~/projects/joyquant-db",
    },
  ];
  if (sandboxEscapeMock) {
    window.setTimeout(() => {
      if (pendingApprovalPreview) return;
      pendingApprovalPreview = true;
      pendingApprovalPreviewPrompt = { id: "mock-sandbox-escape-preview", tool: "sandbox_escape" };
      emitMockTurnStarted();
      emit({ kind: "reasoning", text: t("mock.sandboxEscapeReasoning") });
      emit({
        kind: "approval_request",
        approval: {
          id: "mock-sandbox-escape-preview",
          tool: "sandbox_escape",
          subject: t("mock.sandboxEscapeSubject"),
          reason: t("mock.sandboxEscapeReason"),
        },
      });
    }, 800);
  }
  const mockModelCatalog = [
    { ref: "deepseek/deepseek-v4-flash", provider: "deepseek", model: "deepseek-v4-flash" },
    { ref: "deepseek/deepseek-v4-pro", provider: "deepseek", model: "deepseek-v4-pro" },
  ];
  const defaultMockModelRef = mockModelCatalog[0].ref;
  const mockModelRef = (name: string): string => {
    const trimmed = name.trim();
    if (!trimmed || trimmed === "DeepSeek-R1") return defaultMockModelRef;
    const exact = mockModelCatalog.find((model) => model.ref === trimmed);
    if (exact) return exact.ref;
    const byModel = mockModelCatalog.find((model) => model.model === trimmed);
    return byModel?.ref ?? trimmed;
  };
  const mockModelLabel = (ref: string): string => mockModelCatalog.find((model) => model.ref === mockModelRef(ref))?.model ?? ref.split("/").pop() ?? ref;
  const mockTabModelRef = (tab?: TabMeta): string => mockModelRef(tab?.label ?? "");
  let mockTerminalSessions: TerminalSessionView[] = [];
  const mockTerminalOutput = new Map<string, string>();
  const mockTerminalTabIDs = new Map<string, string>();
  const mockTerminalBytes = (text: string): string => {
    if (typeof btoa === "function") return btoa(unescape(encodeURIComponent(text)));
    return "";
  };
  const setMockTabModel = (tabID: string | undefined, name: string) => {
    const ref = mockModelRef(name);
    const label = mockModelLabel(ref);
    let applied = false;
    mockTabs = mockTabs.map((tab) => {
      const match = tabID ? tab.id === tabID : tab.active;
      if (!match) return tab;
      applied = true;
      return { ...tab, label };
    });
    if (!applied && mockTabs.length > 0) {
      mockTabs = mockTabs.map((tab, index) => (index === 0 ? { ...tab, label } : tab));
    }
  };
  return {
    ...makeMockSessionCatalogBindings(cloneProjectTree),
    ...makeMockBlankProjectBindings(),
    async MinimiseMainWindow() {
      console.info("mock MinimiseMainWindow");
    },
    async ToggleMaximiseMainWindow() {
      console.info("mock ToggleMaximiseMainWindow");
    },
    async IsMainWindowMaximised() {
      return false;
    },
    async CloseMainWindow() {
      console.info("mock CloseMainWindow");
    },
    async Platform() {
      const override = browserPlatformOverride();
      if (override) return override;
      // Mirror the OS the browser dev mock runs on.
      const ua = typeof navigator !== "undefined" ? navigator.userAgent : "";
      if (/Win/i.test(ua)) return "windows";
      if (/Mac/i.test(ua)) return "darwin";
      return "linux";
    },
        async Submit(input, submissionID?: string) {
          cancelled = false;
      emitMockTurnStarted(submissionID);
      const trimmedInput = input.trim().toLowerCase();
      // Deterministic browser QA for the real recovery/source components.
      if (trimmedInput === "/mock-protocol-recovery") {
        setMockTabRunning(currentMockTurnTabId(), false);
        emit({kind:"turn_done",err:"上游拒绝了请求，但未提供具体原因",protocolRecovery:{id:"mock-protocol-once"}});
        return;
      }
      if (trimmedInput === "/recover-context mock-protocol-once") {
        await delay(1500);
        if (cancelled) return;
        emit({kind:"tool_dispatch",tool:{id:"mock-search",name:"web_search",args:'{"query":"OpenCode documentation"}',readOnly:true}});
        emit({kind:"tool_result",tool:{id:"mock-search",name:"web_search",args:'{"query":"OpenCode documentation"}',readOnly:true,output:JSON.stringify({summary:"搜索摘要仍然可读。",sources:[],sources_status:"not_provided"})}});
        emit({kind:"notice",level:"info",code:"search_sources_not_provided",text:"搜索已完成，供应商未提供可用的结构化来源"});
        emitMockTurnDone(submissionID);
        return;
      }
      const decisionSurfaceMock = decisionSurfaceMockFromInput(trimmedInput);
      const goalMatch = /^\/goal(?:\s+([\s\S]*))?$/.exec(input.trim());
      if (goalMatch) {
        const arg = stripLegacyGoalBudgetFlags((goalMatch[1] ?? "").trim());
        const lowered = arg.toLowerCase();
        const active = mockTabs.find((tab) => tab.active);
        if (!arg || lowered === "status") {
          emit({ kind: "notice", level: "info", text: active?.goal ? `goal: ${active.goal}` : "goal: none" });
          emitMockTurnDone(submissionID);
          return;
        }
        if (["clear", "off", "stop", "done"].includes(lowered)) {
          mockTabs = mockTabs.map((tab) => (tab.active ? { ...tab, goal: "", goalStatus: "stopped", collaborationMode: "normal" } : tab));
          emit({ kind: "notice", level: "info", text: "goal cleared" });
          emitMockTurnDone(submissionID);
          return;
        }
        mockTabs = mockTabs.map((tab) => (tab.active ? { ...tab, goal: arg, goalStatus: "running", collaborationMode: "goal" } : tab));
        emit({ kind: "notice", level: "info", text: `goal set: ${arg}` });
        await delay(350);
        if (cancelled) return;
        const reply = `Autonomous goal run started for: **${arg}**\n\nMock run completed.\n\n[goal:complete]`;
        emit({ kind: "message", text: reply });
        mockTabs = mockTabs.map((tab) => (tab.active ? { ...tab, goal: "", goalStatus: "complete", collaborationMode: "normal" } : tab));
        emit({ kind: "notice", level: "info", text: "goal complete" });
        emitMockTurnDone(submissionID);
        return;
      }
      if (decisionSurfaceMock === "mcp_interaction") return (await import("./mockMCPInteraction")).showMockMCPInteraction(delay, () => cancelled, emit);
      if (decisionSurfaceMock === "tool_approval") {
        pendingApprovalPreview = true;
        pendingApprovalPreviewPrompt = { id: "mock-approval-preview", tool: "bash" };
        await delay(250);
        if (cancelled) return;
        emit({
          kind: "approval_request",
          approval: {
            id: "mock-approval-preview",
            tool: "bash",
            subject: t("mock.approvalSubject"),
          },
        });
        return;
      }
      if (trimmedInput === "/recovery-preview" || trimmedInput === "recovery preview" || trimmedInput === "恢复预览") {
        pendingApprovalPreview = true;
        pendingApprovalPreviewPrompt = { id: "mock-recovery-preview", tool: "write_file" };
        await delay(250);
        if (cancelled) return;
        emit({
          kind: "approval_request",
          approval: {
            id: "mock-recovery-preview",
            tool: "write_file",
            subject: "internal/recovery/gate.go",
            reason: "The proposed recovery changes the implementation method after a failing verification.",
            fresh: true,
            kind: "recovery",
            recovery: {
              source_agent: "root",
              failed_tool: "bash",
              failed_summary: "go test ./internal/recovery failed",
              diagnosis: "The failure is isolated to recovery state persistence.",
              next_tool: "write_file",
              next_action: "Update internal/recovery/gate.go",
              change_kind: "strategy",
              change_rationale: "The proposed edit changes the recovery method and needs a fresh decision.",
              plan_before: [
                "1. Keep the existing Auto execution path [in_progress]",
                "2. Add execution-risk approval prompts [pending]",
                "3. Run the recovery regression suite [pending]",
              ].join("\n"),
              plan_after: [
                "1. Keep the existing Auto execution path [in_progress]",
                "2. Ask only when strategy or scope changes [pending]",
                "3. Show the old and proposed plan before deciding [pending]",
                "4. Run the recovery regression suite [pending]",
              ].join("\n"),
            },
          },
        });
        return;
      }
      if (
        trimmedInput === "/sandbox-escape-preview" ||
        trimmedInput === "sandbox escape preview" ||
        trimmedInput === "sandbox_escape preview" ||
        trimmedInput === "sandbox escape预览"
      ) {
        pendingApprovalPreview = true;
        pendingApprovalPreviewPrompt = { id: "mock-sandbox-escape-preview", tool: "sandbox_escape" };
        await delay(250);
        if (cancelled) return;
        emit({
          kind: "approval_request",
          approval: {
            id: "mock-sandbox-escape-preview",
            tool: "sandbox_escape",
            subject: t("mock.sandboxEscapeSubject"),
            reason: t("mock.sandboxEscapeReason"),
          },
        });
        return;
      }
      if (decisionSurfaceMock === "plan_approval") {
        pendingApprovalPreview = true;
        pendingApprovalPreviewPrompt = { id: "mock-plan-approval-preview", tool: "exit_plan_mode" };
        await delay(250);
        if (cancelled) return;
        emit({
          kind: "approval_request",
          approval: {
            id: "mock-plan-approval-preview",
            tool: "exit_plan_mode",
            subject: "",
          },
        });
        return;
      }
      if (isLongDecisionOptionsMockInput(trimmedInput)) {
        pendingAskPreview = true;
        await delay(250);
        if (cancelled) return;

        const longDescription = (...parts: string[]) => [...parts, ...parts, ...parts].join(" ");
        const longLabel = (...parts: string[]) => [...parts, ...parts, ...parts].join(" · ");
        const q1Option1Description = t("mock.askQ1Opt1Desc");
        const q1Option2Description = t("mock.askQ1Opt2Desc");
        const q1Option3Description = t("mock.askQ1Opt3Desc");
        const q2Option1Description = t("mock.askQ2Opt1Desc");
        const q2Option2Description = t("mock.askQ2Opt2Desc");
        const q2Option3Description = t("mock.askQ2Opt3Desc");
        const allowOnceDescription = t("approval.allowOnceDesc");
        const denyDescription = t("approval.denyDesc");

        emit({
          kind: "ask_request",
          ask: {
            id: "mock-long-options",
            questions: [
              {
                id: "long-options",
                header: `${t("mock.askQ1Header")} · QA`,
                prompt: `${t("mock.askQ1Prompt")} ${t("mock.askQ2Prompt")}`,
                options: [
                  {
                    label: t("mock.askQ1Opt1Label"),
                    description: longDescription(q1Option1Description, q2Option1Description, allowOnceDescription),
                  },
                  {
                    label: t("mock.askQ1Opt2Label"),
                    description: longDescription(q1Option2Description, q2Option2Description, denyDescription),
                  },
                  {
                    label: t("mock.askQ1Opt3Label"),
                    description: longDescription(q1Option3Description, q2Option3Description, allowOnceDescription),
                  },
                  {
                    // Deliberately omit description here: this exercises the
                    // legacy/malformed payload fallback where the complete
                    // decision was placed in label instead of split into a
                    // short label plus supporting description.
                    label: longLabel(
                      t("mock.askQ2Opt1Label"),
                      t("mock.askQ1Opt3Label"),
                      t("mock.askQ2Opt3Label"),
                      t("mock.askQ1Opt1Label"),
                    ),
                  },
                  {
                    label: t("mock.askQ2Opt2Label"),
                    description: longDescription(
                      q2Option2Description,
                      "DecisionSurfacePreviewWithAnExtremelyLongUnbrokenIdentifierForOverflowVerification0123456789",
                      q1Option1Description,
                    ),
                  },
                  {
                    label: t("mock.askQ2Opt3Label"),
                    description: longDescription(q2Option3Description, q1Option1Description, q2Option2Description),
                  },
                  {
                    label: t("approval.deny"),
                    description: longDescription(denyDescription, q1Option2Description, q2Option1Description),
                  },
                ],
              },
            ],
          },
        });
        return;
      }
      if (decisionSurfaceMock === "ask") {
        pendingAskPreview = true;
        await delay(250);
        if (cancelled) return;
        emit({
          kind: "ask_request",
          ask: {
            id: `mock-ask-preview-${Date.now()}`,
            questions: [
              {
                id: "q1",
                header: t("mock.askQ1Header"),
                prompt: t("mock.askQ1Prompt"),
                options: [
                  { label: t("mock.askQ1Opt1Label"), description: t("mock.askQ1Opt1Desc") },
                  { label: t("mock.askQ1Opt2Label"), description: t("mock.askQ1Opt2Desc") },
                  { label: t("mock.askQ1Opt3Label"), description: t("mock.askQ1Opt3Desc") },
                ],
              },
              {
                id: "q2",
                header: t("mock.askQ2Header"),
                prompt: t("mock.askQ2Prompt"),
                options: [
                  { label: t("mock.askQ2Opt1Label"), description: t("mock.askQ2Opt1Desc") },
                  { label: t("mock.askQ2Opt2Label"), description: t("mock.askQ2Opt2Desc") },
                  { label: t("mock.askQ2Opt3Label"), description: t("mock.askQ2Opt3Desc") },
                ],
              },
            ],
          },
        });
        return;
      }
      if (trimmedInput === "/todo-preview" || trimmedInput === "todo preview" || trimmedInput === "todo预览") {
        await delay(250);
        if (cancelled) return;
        emit({
          kind: "tool_dispatch",
          tool: {
            id: "mock-todo-preview",
            name: "todo_write",
            args: JSON.stringify({
              todos: [
                { content: t("mock.todo1"), status: "completed" },
                { content: t("mock.todo2"), activeForm: t("mock.todo2ActiveForm"), status: "in_progress" },
                { content: t("mock.todo3"), status: "pending" },
              ],
            }),
            readOnly: false,
          },
        });
        await delay(150);
        emit({
          kind: "tool_result",
          tool: {
            id: "mock-todo-preview",
            name: "todo_write",
            args: JSON.stringify({
              todos: [
                { content: t("mock.todo1"), status: "completed" },
                { content: t("mock.todo2"), activeForm: t("mock.todo2ActiveForm"), status: "in_progress" },
                { content: t("mock.todo3"), status: "pending" },
              ],
            }),
            output: "todo list updated",
            readOnly: false,
            durationMs: 150,
          },
        });
        emitMockTurnDone(submissionID);
        return;
      }
      if (trimmedInput === "/process-preview" || trimmedInput === "process preview" || trimmedInput === "过程预览") {
        await delay(200);
        if (cancelled) return;
        emit({ kind: "phase", text: "Preparing context" });
        await delay(120);
        emit({ kind: "notice", level: "info", text: "Loaded project instructions from AGENTS.md." });
        await delay(120);
        emit({ kind: "notice", level: "warn", text: "Network access is enabled; external results may change over time." });
        await delay(120);
        emit({ kind: "compaction_started", compaction: { trigger: "manual" } });
        await delay(320);
        emit({
          kind: "compaction_done",
          compaction: {
            trigger: "manual",
            messages: 6,
            summary: "Preserved the active task, relevant files, and UI decisions while trimming earlier exploratory context.",
          },
        });
        emit({ kind: "message", text: "Process card preview complete." });
        emitMockTurnDone(submissionID);
        return;
      }
      if (trimmedInput === "/nested-preview" || trimmedInput === "nested preview" || trimmedInput === "嵌套预览") {
        const parentId = "mock-nested-explore";
        await delay(180);
        if (cancelled) return;
        emit({
          kind: "reasoning",
          text: "我先快速探索相关文件，再整理这个工具行的视觉层级。",
        });
        emit({
          kind: "message",
          text: "",
          reasoning: "我先快速探索相关文件，再整理这个工具行的视觉层级。",
        });
        emit({
          kind: "tool_dispatch",
          tool: {
            id: parentId,
            name: "explore",
            args: JSON.stringify({ task: "在 Reasonix 前端中检查工具调用图标和嵌套调用展示" }),
            readOnly: true,
            profile: { model: "mock-reasonix", effort: "high" },
          },
        });
        for (let i = 1; i <= 30; i += 1) {
          if (cancelled) return;
          const id = `mock-nested-${i}`;
          const isSearch = i % 3 === 0;
          const name = isSearch ? "grep" : "read_file";
          const args = isSearch
            ? { pattern: i % 2 === 0 ? "tool__nested-count" : "explore", path: "desktop/frontend/src" }
            : { path: `desktop/frontend/src/${i % 2 === 0 ? "components/ToolCard.tsx" : "styles.css"}`, offset: i * 10, limit: 40 };
          emit({ kind: "tool_dispatch", tool: { id, name, args: JSON.stringify(args), readOnly: true, parentId } });
          emit({
            kind: "tool_result",
            tool: {
              id,
              name,
              readOnly: true,
              output: isSearch ? "3 matches" : "read 40 lines",
              durationMs: 24 + i,
            },
          });
          await delay(18);
        }
        emit({
          kind: "tool_result",
          tool: {
            id: parentId,
            name: "explore",
            readOnly: true,
            output: "已读 20 个文件 · 搜索 10 个文件",
            durationMs: 61510,
          },
        });
        emit({
          kind: "message",
          text: "Mock nested tool preview complete. The explore row now shows the compass count marker.",
        });
        emitMockTurnDone(submissionID);
        return;
      }
      // Simulate the server's pre-first-token latency so the deferred user bubble
      // and the "un-send on Esc before any reply" path are observable in browser
      // dev. Bail if cancelled during the wait — nothing was streamed yet.
      await delay(700);
      if (cancelled) return;
      const reasoningChunks = [
        "我先判断这是浏览器预览环境，所以不会调用真实 kernel。\n",
        "接着模拟 provider 的 reasoning delta：先展示思考过程，再切到正式回复。\n",
        "完成后前端应该把过程区折叠成“已工作 N 秒”。\n",
      ];
      for (const chunk of reasoningChunks) {
        if (cancelled) return;
        emit({ kind: "reasoning", reasoning: chunk });
        await delay(520);
      }
      if (cancelled) return;
      await delay(260);
      const reply =
        `You said: **${input}**\n\n` +
        "This is the browser dev mock — the real reply comes from the kernel " +
        "inside the Wails shell. Here's a fenced block to exercise the editor seam:\n\n" +
        "```go\nfunc main() {\n    println(\"hello from the mock\")\n}\n```\n";
      for (const ch of reply) {
        if (cancelled) break;
        emit({ kind: "text", text: ch });
        await delay(6);
      }
      emit({ kind: "message", text: reply });
      emit({
        kind: "tool_dispatch",
        tool: {
          id: "t1",
          name: "edit_file",
          args: '{"path":"main.go","old_string":"println(\\"hi\\")","new_string":"println(\\"hello\\")"}',
          readOnly: false,
        },
      });
      await delay(350);
      emit({
        kind: "tool_result",
        tool: { id: "t1", name: "edit_file", output: "edited main.go", readOnly: false, durationMs: 350 },
      });
      emit({
        kind: "usage",
        usage: {
          promptTokens: 1280,
          completionTokens: 64,
          totalTokens: 1344,
          cacheHitTokens: 1024,
          cacheMissTokens: 256,
          sessionCacheHitTokens: 1024,
          sessionCacheMissTokens: 256,
          cost: 0.0064,
          currency: "¥",
          currencyCode: "CNY",
          costComplete: true,
          displayComplete: true,
          displayStatus: "matched",
          costQuote: {
            original: { amount: "0.0064", currency: "CNY" },
            selected: { amount: "0.0064", currency: "CNY" },
            estimated: true,
            costComplete: true,
            displayComplete: true,
            complete: true,
            displayStatus: "matched",
            aggregateMode: "single_currency",
            rateBand: "off_peak",
            ratedAt: "2026-08-17T00:30:00Z",
          },
        },
      });
          emitMockTurnDone(submissionID);
        },
        async SubmitToTab(_tabID, input) { await withMockTabScope(_tabID, () => this.Submit(input)); },
        async SubmitToTabWithID(_tabID, input, submissionID) { const submit = this.Submit as (value: string, id?: string) => Promise<void>; await withMockTabScope(_tabID, () => submit(input, submissionID)); },
        async SubmitDisplay(_display, input) { await this.Submit(input); },
        async SubmitDisplayToTab(_tabID, display, input) { await withMockTabScope(_tabID, () => this.SubmitDisplay(display, input)); },
        async SubmitDisplayToTabWithID(_tabID, _display, input, submissionID) { await this.SubmitToTabWithID(_tabID, input, submissionID); },
        async SubmitDeliveryRecoveryToTab(_tabID, display, input) { await withMockTabScope(_tabID, () => this.SubmitDisplay(display, input)); },
        async SubmitDeliveryRecoveryToTabWithID(_tabID, _display, input, submissionID) { await this.SubmitToTabWithID(_tabID, input, submissionID); },
        async SubmitInvocationsToTab(_tabID, display, input, _invocations) { await withMockTabScope(_tabID, () => this.SubmitDisplay(display, input)); },
        async SubmitInvocationsToTabWithID(_tabID, _display, input, _invocations, submissionID) { await this.SubmitToTabWithID(_tabID, input, submissionID); },
        async SubmitInitialGoalToTab(
          _tabID,
          goal,
          display,
          input,
          invocations,
          _collaborationMode,
          _toolApprovalMode,
        ) {
          return await withMockTabScope(_tabID, async () => {
            await this.SetGoalForTab(_tabID, goal);
            if (invocations.length > 0) {
              await this.SubmitInvocationsToTab(_tabID, display, input, invocations);
              return [];
            }
            await this.SubmitDisplayToTab(_tabID, display, input);
            return [];
          });
        },
        async SubmitInitialGoalToTabWithID(_tabID, goal, display, input, invocations, _collaborationMode, _toolApprovalMode, submissionID) { await this.SetGoalForTab(_tabID, goal); if (invocations.length > 0) await this.SubmitInvocationsToTabWithID(_tabID, display, input, invocations, submissionID); else await this.SubmitDisplayToTabWithID(_tabID, display, input, submissionID); return []; },
        async SubmitEditedDisplayToTab(_tabID, display, input, _original) { await withMockTabScope(_tabID, () => this.SubmitDisplay(display, input)); },
        async SubmitEditedDisplayToTabWithID(_tabID, display, input, _original, submissionID) { await this.SubmitDisplayToTabWithID(_tabID, display, input, submissionID); },
        async RunShell(command) {
          cancelled = false;
          emitMockTurnStarted();
          await delay(100);
          if (cancelled) return;
          const id = `shell-${command.slice(0, 32)}`;
          emit({ kind: "tool_dispatch", tool: { id, name: "bash", args: JSON.stringify({ command }), readOnly: false } });
          await delay(200);
          if (cancelled) return;
          emit({ kind: "tool_progress", tool: { id, name: "bash", output: `$ ${command}\n(mock output)\n`, readOnly: false } });
          await delay(100);
          if (cancelled) return;
          emit({ kind: "tool_result", tool: { id, name: "bash", output: `$ ${command}\n(mock output)\n`, readOnly: false, durationMs: 300 } });
          emitMockTurnDone();
        },
        async RunShellForTab(_tabID, command) {
          await withMockTabScope(_tabID, () => this.RunShell(command));
        },
        async Steer(_text) {
          // Mock: emit a steer event as confirmation in the transcript.
          emit({ kind: "steer", text: _text });
        },
        async SteerForTab(_tabID, _text) {
          await this.Steer(_text);
        },
        async InboxSnapshot(_tabID) { if (recoveryMock) return (await import("./inboxRecoveryPreview")).inboxRecoveryPreviewSnapshot();
          return {
            revision: 0,
            paused: false,
            recovered: false, sessionPath: "",
            items: [],
            itemsCount: 0,
            bytes: 0,
            maxItems: 64,
            maxBytes: 64 * 1024 * 1024,
          };
        },
        async EnqueueInboxFollowup(_tabID, _display, _submit, _idempotency) {
          return { itemId: `mock-${Date.now()}`, disposition: "queued_followup", position: 1, paused: false };
        },
        async EnqueueInboxFollowupWithInvocations(_tabID, _display, _submit, _invocations, _idempotency) {
          return { itemId: `mock-invocation-${Date.now()}`, disposition: "queued_followup", position: 1, paused: false };
        },
        async EnqueueInboxSteer(_tabID, display, submit, _idempotency) {
          const itemId = `mock-steer-${Date.now()}`;
          emit({ kind: "steer", text: submit || display, itemId });
          return { itemId, disposition: "steer_accepted", position: 1, paused: false };
        },
        async SteerInboxItem(_tabID, itemID) {
          emit({ kind: "steer", itemId: itemID });
          return { itemId: itemID, disposition: "steer_accepted", position: 1, paused: false };
        },
        async ReadInboxItem(_tabID, id) {
          return { id, displayText: "", rawText: "", submitText: "" };
        },
        async UpdateInboxItem() {},
        async DeleteInboxItem() {},
        async MoveInboxItem() {},
        async SetInboxPaused(_tabID, paused) { if (recoveryMock) (await import("./inboxRecoveryPreview")).setInboxRecoveryPreviewPaused(paused); },
        async RetryInboxItem() {},
        async RefreshInboxItem() {},
        async InboxHasItems() { return recoveryMock; },
        async Cancel() {
          cancelled = true;
          emitMockTurnDone();
        },
        async CancelTab(_tabID) {
          await withMockTabScope(_tabID, () => this.Cancel());
        },
        async CancelTabWithInboxItems(_tabID, _itemIDs) {
          await withMockTabScope(_tabID, () => this.Cancel());
        },
        async CancelTabWithInboxItemsResult(_tabID, itemIDs) { await withMockTabScope(_tabID, () => this.Cancel()); return { discardedItemIds: [...itemIDs] }; },
        async Approve(_id, allow, session, persist) {
          if (!pendingApprovalPreview) return;
          pendingApprovalPreview = false;
          pendingApprovalPreviewPrompt = undefined;
          const suffix = persist ? "grant saved" : session ? "grant active this session" : "allowed once";
          emit({
            kind: "message",
            text: `approval preview answered: ${allow ? suffix : "denied"}`,
          });
          emitMockTurnDone();
        },
        async ApproveTab(_tabID, id, allow, session, persist) {
          await withMockTabScope(_tabID, () => this.Approve(id, allow, session, persist));
        },
        async ResolvePlanDecision(id, action) {
          const active = mockTabs.find((tab) => tab.active);
          await this.ResolvePlanDecisionTab(active?.id ?? "", id, action);
        },
        async ResolvePlanDecisionTab(_tabID, id, action) {
          await withMockTabScope(_tabID, async () => {
            void id;
            pendingApprovalPreview = false;
            pendingApprovalPreviewPrompt = undefined;
            emit({
              kind: "message",
              text: `plan preview answered: ${action}`,
            });
            emitMockTurnDone();
          });
        },
        async ResolveRecovery(id, action, feedback) {
          const active = mockTabs.find((tab) => tab.active);
          await this.ResolveRecoveryTab(active?.id ?? "", id, action, feedback);
        },
        async ResolveRecoveryTab(_tabID, id, action, feedback) {
          void id;
          void feedback;
          pendingApprovalPreview = false;
          pendingApprovalPreviewPrompt = undefined;
          emit({
            kind: "message",
            text: `recovery preview answered: ${action}`,
          });
          emitMockTurnDone();
        },
        async SetRecoveryCheckpointEnabled(_enabled) {},
        async SetRecoveryCheckpointEnabledTab(_tabID, _enabled) {},
        async RecoveryCheckpointEnabled() {
          return true;
        },
        async RecoveryCheckpointEnabledTab(_tabID) {
          return true;
        },
        async AnswerQuestion(_id, answers) {
      if (!pendingAskPreview) return;
      pendingAskPreview = false;
      const summary = answers
        .map((answer) => `${answer.questionId}: ${(answer.selected ?? []).join(", ") || "(no answer)"}`)
        .join("\n");
      emit({ kind: "message", text: `ask preview answered:\n\n${summary}` });
          emitMockTurnDone();
        },
        async AnswerMCPInteractionForTab(_tabID, id, _action, _content) {
          if (!cancelled && (await import("./mockMCPInteraction")).consumeMockMCPInteraction(id)) await withMockTabScope(_tabID, async () => { emit({ kind: "prompt_answered", itemId: id }); emitMockTurnDone(); });
        },
        ...makeMockMCPAppBindings(), ...makeMockPinnedContextBindings(),
        async AnswerQuestionForTab(_tabID, id, answers) {
          await withMockTabScope(_tabID, () => this.AnswerQuestion(id, answers));
        },
        async ResolvePromptForTab(tabID, promptID, _turnID, _runtimeEpoch, kind, answer) {
          await withMockTabScope(tabID, async () => {
            if (kind === "ask") return this.AnswerQuestion(promptID, answer.questions ?? []);
            if (kind === "approval") return this.Approve(promptID, Boolean(answer.allow), Boolean(answer.session), Boolean(answer.persist));
            if (kind === "plan") return this.ResolvePlanDecisionTab(tabID, promptID, (answer.action ?? "exit_plan") as "start_execution" | "revise_plan" | "exit_plan");
            if (kind === "recovery") return this.ResolveRecoveryTab(tabID, promptID, answer.action ?? "revise", answer.feedback ?? "");
            if (kind === "mcp") return this.AnswerMCPInteractionForTab(tabID, promptID, (answer.action ?? "cancel") as "accept" | "decline" | "cancel", answer.content ?? null);
            throw new Error(`unsupported prompt kind: ${kind}`);
          });
        },
        async ReplayPendingPrompts() {},
        async ReplayPendingPromptsForTab(_tabID) {},
        async ConfirmAction(req) {
          void req;
          return false;
        },
        async SetPlanMode(on) {
          const active = mockTabs.find((tab) => tab.active);
          if (active) await this.SetModeForTab(active.id, modeWithPlan(normalizeMode(active.mode), on));
        },
        async SetMode(mode) {
          const active = mockTabs.find((tab) => tab.active);
          if (active) await this.SetModeForTab(active.id, mode);
        },
        async SetModeForTab(tabID, mode) {
          const nextMode = normalizeMode(mode);
          let nextToolApprovalMode: ToolApprovalMode | "" = "";
          mockTabs = mockTabs.map((tab) => {
            if (tab.id !== tabID) return tab;
            nextToolApprovalMode = mockToolApprovalModeAfterModeChange(tab.toolApprovalMode, nextMode);
            return {
              ...tab,
              mode: nextMode,
              collaborationMode: normalizeCollaborationMode(undefined, tab.goal, nextMode),
              toolApprovalMode: nextToolApprovalMode,
            };
          });
          return drainMockApprovalPreviews(nextToolApprovalMode);
        },
        async SetCollaborationMode(mode) {
          const active = mockTabs.find((tab) => tab.active);
          if (active) await this.SetCollaborationModeForTab(active.id, mode);
        },
        async SetCollaborationModeForTab(tabID, mode) {
          const next = normalizeCollaborationMode(mode);
          mockTabs = mockTabs.map((tab) => {
            if (tab.id !== tabID) return tab;
            const toolMode = normalizeToolApprovalMode(tab.toolApprovalMode, normalizeMode(tab.mode));
            return {
              ...tab,
              collaborationMode: next,
              goal: next === "normal" || next === "plan" ? "" : tab.goal,
              mode: modeWithPlan(modeWithAutoApproveTools(normalizeMode(tab.mode), toolMode === "yolo"), next === "plan"),
            };
          });
        },
        async SetToolApprovalMode(mode) {
          const active = mockTabs.find((tab) => tab.active);
          if (active) await this.SetToolApprovalModeForTab(active.id, mode);
        },
        async SetToolApprovalModeForTab(tabID, mode) {
          const next = normalizeToolApprovalMode(mode);
          settings.autoApproveTools = next === "yolo";
          settings.bypass = next === "yolo";
          mockTabs = mockTabs.map((tab) =>
            tab.id === tabID
              ? {
                  ...tab,
                  toolApprovalMode: next,
                  mode: modeWithAutoApproveTools(normalizeMode(tab.mode), next === "yolo"),
                }
              : tab,
          );
          return drainMockApprovalPreviews(next);
        },
        async SetComposerProfileForTab(tabID, collaborationMode, toolApprovalMode, goal) {
          const nextCollaboration = normalizeCollaborationMode(collaborationMode);
          const nextToolApproval = normalizeToolApprovalMode(toolApprovalMode);
          const nextGoal = goal.trim();
          settings.autoApproveTools = nextToolApproval === "yolo";
          settings.bypass = nextToolApproval === "yolo";
          mockTabs = mockTabs.map((tab) => {
            if (tab.id !== tabID) return tab;
            const plan = !nextGoal && nextCollaboration === "plan";
            return {
              ...tab,
              collaborationMode: nextGoal ? "goal" : plan ? "plan" : "normal",
              toolApprovalMode: nextToolApproval,
              goal: nextGoal,
              goalStatus: nextGoal ? "running" : "stopped",
              mode: modeWithAutoApproveTools(modeWithPlan(normalizeMode(tab.mode), plan), nextToolApproval === "yolo"),
            };
          });
          return drainMockApprovalPreviews(nextToolApproval);
        },
        async SetGoal(goal) {
          const active = mockTabs.find((tab) => tab.active);
          if (active) await this.SetGoalForTab(active.id, goal);
        },
        async SetGoalForTab(tabID, goal) {
          const nextGoal = goal.trim();
          mockTabs = mockTabs.map((tab) =>
            tab.id === tabID
              ? {
                  ...tab,
                  goal: nextGoal,
                  goalStatus: nextGoal ? "running" : "stopped",
                  collaborationMode: nextGoal ? "goal" : "normal",
                  mode: modeWithPlan(normalizeMode(tab.mode), false),
                }
              : tab,
          );
        },
        async ResumeGoalForTab(tabID) {
          let resumed = false;
          mockTabs = mockTabs.map((tab) => {
            if (tab.id !== tabID || !tab.goal || tab.goalStatus === "complete") return tab;
            resumed = true;
            return { ...tab, goalStatus: "running", collaborationMode: "goal", goalRuntime: undefined };
          });
          return resumed;
        },
        async PauseGoalForTab(tabID) {
          let paused = false;
          mockTabs = mockTabs.map((tab) => {
            if (tab.id !== tabID || !tab.goal || tab.goalStatus !== "running") return tab;
            paused = true;
            return { ...tab, goalStatus: "blocked", goalRuntime: undefined };
          });
          return paused;
        },
        async ClearGoal() {
          await this.SetGoal("");
        },
        async ClearGoalForTab(tabID) {
          await this.SetGoalForTab(tabID, "");
        },
        async Compact() {},
        async CompactForTab() {},
        async NewSession() {},
        async NewSessionForTab() {},
        async ClearSession() { return { sessionPath: "", sessionGeneration: 0 }; },
        async ClearSessionForTab() { return { sessionPath: "", sessionGeneration: 0 }; },
    async Checkpoints() {
      return [
        { turn: 0, prompt: "你好呀", files: ["src/App.tsx"], fileCount: 1, turnFileCount: 1, time: Date.now() - 30_000, canCode: true, canConversation: true },
      ];
    },
    async CheckpointsForTab() {
      return this.Checkpoints();
    },
    async Rewind() {},
    async RewindForTab() {},
    async PreviewRewindForTab() {
      return { ok: true, canFiles: true, canConversation: true, planId: "mock", fileCount: 0 };
    },
    async CommitRewindForTab() {
      return { ok: true, undoAvailable: true, transactionId: "mock-tx" };
    },
    async UndoRewindForTab() {
      return { ok: true, undoAvailable: false };
    },
    async PreviewWorkspaceFileRevertForTab(_tabID, path) {
      return { ok: true, canFiles: true, path, planId: "mock-file" };
    },
    async CommitWorkspaceFileRevertForTab() {
      return { ok: true, undoAvailable: true, transactionId: "mock-file-tx" };
    },
    ...makeMockForkBindings(
      () => mockTabs,
      (tabs) => { mockTabs = tabs; },
      t("rewind.fork"),
    ),
    async SummarizeFrom() {},
    async SummarizeFromForTab() {},
    async SummarizeUpTo() {},
    async SummarizeUpToForTab() {},
        async History() {
          return [];
        },
        async HistoryForTab(tabID?: string) {
          const tab = mockTabs.find((item) => item.id === tabID) ?? mockTabs.find((item) => item.active);
          if (tab?.topicId) {
            queueMockTopicRuntime(tab);
            if (benchFixturesPromise) {
              const fixtures = await benchFixturesPromise;
              const history = fixtures.benchTopicHistory(tab.topicId);
              if (history) return history;
            }
            return mockTopicHistory(tab.topicId);
          }
          return this.History();
        },
        async HistoryPage(beforeTurn = 0, limit = 60) {
          return mockHistoryPage(await this.History(), beforeTurn, limit);
        },
        async HistoryPageForTab(tabID: string, beforeTurn = 0, limit = 60) {
          return mockHistoryPage(await this.HistoryForTab(tabID), beforeTurn, limit);
        },
        async HistoryCheckpointTurnsForTab(tabID: string) {
          const turns: number[] = [];
          for (const message of await this.HistoryForTab(tabID)) {
            if (message.role !== "user") continue;
            turns.push(message.checkpointTurn ?? turns.length);
          }
          return turns;
        },
        async HistorySliceForTab(tabID: string, req: HistorySliceRequest) {
          return mockHistorySlice(tabID, await this.HistoryForTab(tabID), req, benchMock);
        },
        async HistoryContentForTab(tabID: string, ref: HistoryContentRef, chunkIndex: number): Promise<HistoryContentChunk> {
          const out: HistoryContentChunk = { entryId: ref.entryId, field: ref.field, chunk: Math.max(0, chunkIndex), chunks: 1, data: "", done: true, stale: false };
          const match = /:m(\d+):o\d+$/.exec(ref.entryId);
          if (!match) return out;
          const messages = await this.HistoryForTab(tabID);
          const message = messages[Number(match[1])];
          if (benchMock && message?.content?.includes("ASYNC LAYOUT EXPANSION COMPLETE")) await delay(1_500);
          // Storm fixture: pace ref resolutions deterministically by entry
          // index so opening the session produces a seconds-long patch storm
          // instead of a single burst (#8657).
          if (benchMock && (message?.content?.includes("BENCH STORM HYDRATION RESOLVED") || message?.reasoning?.includes("BENCH STORM HYDRATION RESOLVED"))) {
            await delay(50 + (Number(match[1]) % 24) * 120);
          }
          if (!message) return { ...out, stale: true };
          out.data = mockHistoryContentField(message, ref);
          out.chunks = 1;
          return out;
        },
    async ListSessions() {
      return sessions.map((s) => ({ ...s }));
    },
    async ListSessionsForTab() {
      return sessions.map((s) => ({ ...s }));
    },
    ...makeMockHistoryCatalogBindings(sessions),
    async ListTrashedSessions() {
      return trashedSessions.map((s) => ({ ...s }));
    },
    async ResumeSession(path: string) {
      sessions.forEach((s) => {
        s.current = s.path === path;
        s.open = s.open || s.path === path;
      });
      return [
        { role: "user", content: `(mock) resumed ${path}` },
        { role: "assistant", content: "This is a mock resumed transcript — the real one comes from the kernel." },
      ];
    },
	    async ResumeSessionForTab(_tabID: string, path: string) {
	      return this.ResumeSession(path);
	    },
	    async ResumeSessionPage(path: string, limit = 60) {
	      return mockHistoryPage(await this.ResumeSession(path), 0, limit);
	    },
	    async ResumeSessionPageForTab(_tabID: string, path: string, limit = 60) {
	      return this.ResumeSessionPage(path, limit);
	    },
	    async OpenChannelSessionForTab(tabID: string, path: string) {
	      mockTabs = mockTabs.map((tab) => tab.id === tabID ? { ...tab, sessionPath: path, readOnly: true } : tab);
	      return this.ResumeSession(path);
	    },
	    async OpenChannelSessionPageForTab(tabID: string, path: string, limit = 60) {
	      return mockHistoryPage(await this.OpenChannelSessionForTab(tabID, path), 0, limit);
	    },
	    async QuerySessionTakeover(_tabId: string) {
      return { available: false, reason: "mock" } as import("./types").SessionTakeoverView;
    },
    async TakeoverSession(_tabId: string, _mode: "wait" | "interrupt") {},
    async PreviewSession(path: string) {
      const s = sessions.find((x) => x.path === path) ?? trashedSessions.find((x) => x.path === path);
      return [
        { role: "user", content: s?.preview || `(mock) preview ${path}` },
        { role: "phase", content: "Preparing read-only preview" },
        {
          role: "assistant",
          content: "This is a read-only mock preview. The active conversation is unchanged.",
          reasoning: "Preview reads the saved session without resuming it.",
        },
        { role: "notice", level: "info", content: "Preview mode keeps the active conversation untouched." },
        { role: "compaction", content: "", trigger: "manual", messages: 3, summary: "Mock preview preserved the latest task, tool result, and answer summary." },
      ];
    },
    async DeleteSession(path: string) {
      const i = sessions.findIndex((s) => s.path === path);
      if (i >= 0) {
        const [s] = sessions.splice(i, 1);
        trashedSessions.unshift({
          ...s,
          current: false,
          open: false,
          path: s.path.replace("/mock/sessions/", "/mock/sessions/.trash/"),
          deletedAt: Date.now(),
        });
      }
    },
    async DeleteRecoveryCopy(path: string) {
      return this.DeleteSession(path);
    },
    async GetRecoveryLineage(key) {
      const topic = findMockTopic(key.topicId);
      return {
        groupId: key.topicId,
        state: topic?.recoveryState ?? "normal",
        branchCount: topic?.recoveryBranchCount ?? 0,
        unresolved: topic?.recoveryUnresolvedCount ?? 0,
        cleanupEligible: topic?.recoveryCleanupEligibleCount ?? 0,
        members: [],
      };
    },
    async GetSessionVersionState(key) {
      const lineage = await this.GetRecoveryLineage(key);
      return { conversationId: key.topicId, canContinue: true, requiresChoice: lineage.state === "diverged" && lineage.unresolved > 0, lineage };
    },
    async SetActiveSessionVersion() {},
    async RetrySessionRecovery() {},
    async ReconcileRecoveryVersions() {},
    async ChooseRecoveryBranch() {},
    async CleanRecoveryLineage(request) {
      const topic = findMockTopic(request.topicId);
      const eligible = topic?.recoveryCleanupEligibleCount ?? 0;
      if (request.apply && topic) topic.recoveryCleanupEligibleCount = 0;
      return { eligible, moved: request.apply ? eligible : 0, busy: 0, kept: 0, dryRun: !request.apply, items: [] };
    },
    async RestoreSession(path: string) {
      const i = trashedSessions.findIndex((s) => s.path === path);
      if (i >= 0) {
        const [s] = trashedSessions.splice(i, 1);
        sessions.unshift({
          ...s,
          path: s.path.replace("/mock/sessions/.trash/", "/mock/sessions/"),
          deletedAt: undefined,
        });
      }
    },
    async PurgeTrashedSession(path: string) {
      const i = trashedSessions.findIndex((s) => s.path === path);
      if (i >= 0) trashedSessions.splice(i, 1);
    },
    async PurgeRecoveryCopy(path: string) {
      return this.PurgeTrashedSession(path);
    },
    async RenameSession(path: string, title: string) {
      const s = sessions.find((x) => x.path === path);
      if (s) s.title = title.trim() || undefined;
    },
	    async ScanPromptHistory(nonce: string) {
	      // Dev mock returns a static set of sample prompts for UI development.
	      const entries: PromptHistoryEntry[] = [
	        { text: "Explain the architecture of this project", at: Date.now() - 60000, sessionPath: "/mock/sessions/arch.jsonl", turn: 0 },
	        { text: "Fix the login button styling", at: Date.now() - 120000, sessionPath: "/mock/sessions/arch.jsonl", turn: 1 },
	        { text: "What is the capital of France?", at: Date.now() - 300000, sessionPath: "/mock/sessions/general.jsonl", turn: 0 },
	      ];
	      return { entries, nonce: "mock-" + nonce, olderCursor: "", hasOlder: false };
	    },
    async ListWorkspaces() {
      return mockProjectTree
        .filter((node) => node.kind === "project" && node.root)
        .map((node) => ({
          path: node.root!,
          name: node.label || baseName(node.root!),
          current: node.root === cwd,
        }));
    },
    async PickWorkspace() {
      // Browser dev has no native dialog; simulate picking a folder and re-root so
      // the topbar folder chip visibly changes.
      return mockSwitchWorkspace(cwd.endsWith("another-project") ? "~/projects/reasonix" : "~/projects/another-project");
    },
    async SwitchWorkspace(path: string) {
      return mockSwitchWorkspace(path);
    },
    async RemoveWorkspace(path: string) {
      workspaces = workspaces.filter((p) => p !== path);
      const index = mockProjectTree.findIndex((node) => node.root === path);
      if (index >= 0) mockProjectTree.splice(index, 1);
    },
        async ContextUsage() {
          return {
            used: 42124,
            window: 128000,
            sessionTokens: 34479,
            compactRatio: 0.8,
            sessionCost: 0.1287,
            sessionCurrency: "CNY",
            sessionCostQuote: {
              original: { amount: "0.1287", currency: "CNY" },
              selected: { amount: "0.1287", currency: "CNY" },
              estimated: true,
              costComplete: true,
              displayComplete: true,
              complete: true,
              displayStatus: "matched",
              aggregateMode: "single_currency",
              rateBand: "mixed",
            },
          };
        },
        async ContextUsageForTab() {
          return this.ContextUsage();
        },
        async Balance() {
      // Mirror the active mock provider: deepseek-flash carries a balance_url.
      const p = settings.providers.find((x) => x.name === settings.defaultModel);
      if (!p?.balanceUrl) return { available: false, display: "" };
          return { available: true, display: "¥128.50" };
        },
        async BalanceForTab() {
          return this.Balance();
        },
        async UsageStats() {
          // Browser dev mock has no stats files; the panel does not consume
          // provider aggregates, so keep this initial-bundle fallback lean.
          return { from: "", to: "", tokens: 0, requests: 0, turns: 0, cacheHit: 0, cacheMiss: 0, activeDays: 0, topModel: "", daily: [], models: [] } as unknown as UsageStatsRange;
        },
        async Jobs() {
          return []; // browser dev mock has no background jobs
        },
        async JobsForTab() {
          return this.Jobs();
        },
        async CancelJob() {
          return false;
        },
        async CancelJobForTab(_tabID, jobID) {
          return this.CancelJob(jobID);
        },
        async CancelJobsForTab(_tabID, jobIDs) {
          return { cancelled: [], notRunning: [...jobIDs] };
        },
        async ActiveWorkForTab() {
          return { running: false, pendingPrompt: false, cancellable: false, jobs: [] };
        },
        async BackgroundRuntimes() {
          return [];
        },
        async RevealBackgroundRuntime() {
          throw new Error("background runtime is unavailable in browser preview");
        },
        async WorkspaceConflictForTab() {
          return {
            state: "none", ownerWork: { running: false, pendingPrompt: false, cancellable: false, jobs: [] },
            canReveal: false, canCreateWorktree: false,
          };
        },
        async RevealWorkspaceWriterForTab() {
          throw new Error("workspace writer is unavailable in browser preview");
        },
        async CloseTabWithPolicy(tabID) {
          return this.CloseTab(tabID);
        },
        async ToolResultForTab() {
          return null;
        },
        async Meta() {
          const active = mockTabs.find((tab) => tab.active) ?? mockTabs[0];
          const toolApprovalMode = normalizeToolApprovalMode(active?.toolApprovalMode, active ? normalizeMode(active.mode) : "normal", settings.autoApproveTools);
          const autoApproveTools = toolApprovalMode === "yolo";
          const collaborationMode = normalizeCollaborationMode(active?.collaborationMode, active?.goal, active ? normalizeMode(active.mode) : "normal");
          const workspacePath = active?.workspacePath || active?.workspaceRoot || active?.cwd || cwd;
          return {
            label: active?.label ?? "DeepSeek-R1",
            ready: active?.ready ?? true,
            eventChannel: EVENT_CHANNEL,
            cwd: active?.cwd || cwd,
            workspaceRoot: active?.workspaceRoot || workspacePath,
            workspaceName: active?.workspaceName,
            workspacePath,
            sandboxPath: settings.sandbox.workspaceRoot,
            gitBranch: active?.gitBranch || (active?.scope === "project" ? "main" : ""),
            imageInputEnabled: true,
            autoApproveTools,
            bypass: autoApproveTools,
            collaborationMode,
            toolApprovalMode,
            goal: active?.goal ?? "",
            goalStatus: active?.goalStatus ?? (active?.goal ? "running" : "stopped"),
          };
        }, async DismissTodoBatchForTab() {},
        async MetaForTab(tabID) {
          const tab = mockTabs.find((item) => item.id === tabID) ?? mockTabs.find((item) => item.active) ?? mockTabs[0];
          const toolApprovalMode = normalizeToolApprovalMode(tab?.toolApprovalMode, tab ? normalizeMode(tab.mode) : "normal", settings.autoApproveTools);
          const autoApproveTools = toolApprovalMode === "yolo";
          const collaborationMode = normalizeCollaborationMode(tab?.collaborationMode, tab?.goal, tab ? normalizeMode(tab.mode) : "normal");
          const workspacePath = tab?.workspacePath || tab?.workspaceRoot || tab?.cwd || cwd;
          return {
            label: tab?.label ?? "DeepSeek-R1",
            ready: tab?.ready ?? true,
            eventChannel: EVENT_CHANNEL,
            cwd: tab?.cwd || cwd,
            workspaceRoot: tab?.workspaceRoot || workspacePath,
            workspaceName: tab?.workspaceName,
            workspacePath,
            sandboxPath: settings.sandbox.workspaceRoot,
            gitBranch: tab?.gitBranch || (tab?.scope === "project" ? "main" : ""),
            autoApproveTools,
            bypass: autoApproveTools,
            collaborationMode,
            toolApprovalMode,
            goal: tab?.goal ?? "",
            goalStatus: tab?.goalStatus ?? (tab?.goal ? "running" : "stopped"),
          };
        },
    async Commands() {
      const commands: CommandInfo[] = [
        { name: "new", description: "start new session; save transcript", kind: "builtin" as const, group: "actions" },
        { name: "clear", description: "discard current context", kind: "builtin" as const, group: "actions" },
        { name: "compact", description: "Summarize older history to free up context", kind: "builtin" as const, group: "actions" },
        { name: "model", description: "Switch model", kind: "builtin" as const, group: "actions" },
        { name: "effort", description: "Set reasoning effort", kind: "builtin" as const, group: "actions" },
        { name: "skill", description: "List skills", kind: "builtin" as const, group: "skills" },
        { name: "mcp", description: "Manage MCP servers", kind: "builtin" as const, group: "integrations" },
        { name: "plugins", description: "Manage plugin packages", kind: "builtin" as const, group: "integrations" },
        { name: "review", description: "Review the staged diff", hint: "[focus]", kind: "custom" as const, group: "skills" },
      ];
      const seen = new Set(commands.map((command) => command.name));
      for (const skill of capSkills) {
        if (skill.enabled === false) continue;
        const name = (skill.invocation || `/${skill.name}`).replace(/^\/+/, "");
        if (!name || seen.has(name)) continue;
        seen.add(name);
        commands.push({
          name,
          description: skill.description,
          kind: skill.runAs === "subagent" ? "subagent" : "skill",
          group: skill.runAs === "subagent" ? "subagents" : "skills",
          color: skill.color,
        });
      }
      return commands;
    },
    async Capabilities() {
      return {
        servers: capServers.map((s) => ({ ...s })),
        skills: capSkills.map((s) => ({ ...s })),
        skillRoots: capSkillRoots.map((s) => ({ ...s })),
        plugins: capPlugins.map((p) => ({ ...p })),
      };
    },
    async MCPServers() {
      return capServers.map((s) => ({ ...s }));
    },
    async MCPCapabilityMatrix() {
      return { views: [], hostProfile: "desktop-apps-2026-01-26-v1" };
    },
    async MCPMarketplace(query: string) {
      const servers = [
        {
          name: "io.modelcontextprotocol/server-filesystem",
          suggestedName: "server-filesystem",
          title: "Filesystem",
          description: "Secure file operations through MCP.",
          version: "1.0.0",
          installable: true,
          transport: "stdio",
          command: "npx",
          args: ["-y", "@modelcontextprotocol/server-filesystem@1.0.0"],
        },
        {
          name: "io.example/manual",
          suggestedName: "manual",
          title: "Manual setup example",
          description: "Requires an API key before installation.",
          version: "1.0.0",
          installable: false,
          unavailableReason: "package requires environment variables or arguments",
          args: [],
        },
      ];
      const normalized = query.trim().toLowerCase();
      return {
        servers: normalized ? servers.filter((entry) => [entry.name, entry.title, entry.description].join(" ").toLowerCase().includes(normalized)) : servers,
        cached: false,
      } as MCPMarketplaceView;
    },
    async MCPMarketplaceResolve(registryName: string) {
      const result = await this.MCPMarketplace(registryName);
      const entry = result.servers.find((candidate) => candidate.name.toLowerCase() === registryName.trim().toLowerCase());
      if (!entry) throw new Error(`MCP Registry has no server named ${JSON.stringify(registryName)}`);
      return entry;
    },
    async SkillsSettings() {
      return {
        skills: capSkills.map((s) => ({ ...s })),
        skillRoots: capSkillRoots.map((s) => ({ ...s })),
        allowImplicitInvocation: true,
      };
    },
    async RuntimeDoctor() {
      return {
        text: "runtime status: mock\nrecoverability: clean=true irreversible=false\nresume: allow=true cleanRollback=true\n",
        publishedGeneration: 0,
        allowResume: true,
        cleanRollback: true,
        hasIrreversible: false,
        noOpRebuilds: 0,
        fullRebuilds: 0,
        subgraphRebuilds: 0,
        staleDrops: 0,
        admissionRejected: 0, runtimeOwnerFallbacks: 0,
      };
    },
    async CapabilityDiagnostics(includeSessionRuntime: boolean) {
      const report: CapabilityDiagnosticsReport = {
        schema_version: 1,
        root: "<workspace>",
        live: false,
        summary: {
          errors: 0,
          warnings: 1,
          infos: includeSessionRuntime ? 1 : 0,
          instructions: 1,
          skills: capSkills.length,
          commands: 0,
          hooks: 0,
          plugins: capPlugins.length,
          mcp_servers: capServers.length,
        },
        instructions: { docs: [{ path: "<workspace>/AGENTS.md", scope: "project", directory: "<workspace>", depth: 0, order: 1 }] },
        skills: {
          roots: [{ path: "<workspace>/.reasonix/skills", scope: "project", status: "ok" }],
          entries: capSkills.map((s) => ({
            name: s.name,
            description: s.description,
            scope: s.scope,
            path: "(mock)",
            status: "winner",
            run_as: s.runAs,
          })),
          winners: capSkills.length,
          shadowed: 0,
        },
        commands: { roots: [], entries: [], winners: 0, shadowed: 0 },
        hooks: { trusted_project: true, project_defines_hooks: false, sources: [], entries: [] },
        plugins: {
          packages: capPlugins.map((p) => ({
            name: p.name,
            enabled: p.enabled,
            root: p.root || "<external>/plugin",
            skills: p.skills ?? 0,
            commands: 0,
            hooks: p.hooks ?? 0,
            mcp_servers: p.mcpServers ?? 0,
            status: p.enabled ? "ok" : "disabled",
          })),
        },
        mcp: {
          servers: capServers.map((s) => ({
            name: s.name,
            transport: s.transport || "stdio",
            start_intent: s.startIntent === "off" ? "off" : "automatic",
            source: "toml",
            runtime_status: includeSessionRuntime ? s.status || "connected" : undefined,
            tool_count: s.tools,
            env_keys: s.envKeys ?? [],
            header_keys: s.headerKeys ?? [],
          })),
        },
        issues: [
          {
            severity: "warning",
            code: "skill.missing_description",
            subsystem: "skills",
            name: "example",
            message: "mock warning for browser harness",
            remediation: "Add a description frontmatter field",
            settings_tab: "skills",
          },
          ...(includeSessionRuntime
            ? [{
                severity: "info" as const,
                code: "mcp.runtime_unavailable",
                subsystem: "mcp",
                message: "browser mock has no live Host; runtime fields are synthetic",
                settings_tab: "mcp",
              }]
            : []),
        ],
      };
      return JSON.parse(JSON.stringify(report)) as CapabilityDiagnosticsReport;
    },
    async Plugins() {
      return capPlugins.map((p) => ({ ...p }));
    },
    async PlanPluginInstall(source: string, options: PluginInstallOptions) {
      const name = options.name || source.split("/").filter(Boolean).pop()?.replace(/\.git$/, "") || "plugin";
      return JSON.stringify({
        ok: true,
        status: "planned",
        kind: "plugin",
        actions: [{ kind: "plugin", action: "install_plugin_package", name, source, status: "planned" }],
      });
    },
    async InstallPlugin(source: string, options: PluginInstallOptions) {
      const name = options.name || source.split("/").filter(Boolean).pop()?.replace(/\.git$/, "") || "plugin";
      const existing = capPlugins.findIndex((p) => p.name === name);
      const view: PluginView = {
        name,
        version: "dev",
        description: "Mock plugin",
        source,
        root: `~/.reasonix/plugins/${name}`,
        manifestKind: "reasonix",
        enabled: true,
        skills: 1,
        hooks: 0,
        mcpServers: 0,
        skillDetails: [{ name: "plan", description: "Plan work before implementation", invocation: "/plan", runAs: "inline" }],
      };
      if (existing >= 0) capPlugins[existing] = view;
      else capPlugins.push(view);
      return JSON.stringify({ ok: true, status: "done", kind: "plugin", actions: [{ kind: "plugin", name }] });
    },
    async RemovePlugin(name: string) {
      capPlugins = capPlugins.filter((p) => p.name !== name);
    },
    async SetPluginEnabled(name: string, enabled: boolean) {
      capPlugins = capPlugins.map((p) => p.name === name ? { ...p, enabled } : p);
    },
    async UpdatePlugin(name: string) {
      capPlugins = capPlugins.map((p) => p.name === name ? { ...p, version: p.version || "dev" } : p);
      return JSON.stringify({ ok: true, status: "done", kind: "plugin", name });
    },
    async PluginDoctor(name: string) {
      return capPlugins.find((p) => p.name === name) || {
        name,
        root: "",
        enabled: false,
        skills: 0,
        hooks: 0,
        mcpServers: 0,
        error: "plugin is not installed",
      };
    },
    async AddMCPServer(input: MCPServerInput) {
      const tools = input.transport === "stdio" ? 3 : 5;
      capServers.push({
        name: input.name,
        transport: input.transport,
        status: "connected",
        configured: true,
        autoStart: true,
        tier: "background",
        command: input.command,
        args: input.args,
        url: input.url,
        envKeys: input.env ? Object.keys(input.env).sort() : undefined,
        headerKeys: input.headers ? Object.keys(input.headers).sort() : undefined,
        tools,
        prompts: 0,
        resources: 0,
        toolList: Array.from({ length: tools }, (_, i) => ({
          name: `${input.name}_tool_${i + 1}`,
          description: `Mock tool ${i + 1} exposed by ${input.name}.`,
        })),
      });
      return tools;
    },
    async InstallMCPServer(input: MCPServerInput) {
      const tools = await this.AddMCPServer(input);
      return { name: input.name, state: "ready" as const, toolCount: tools, action: "none" as const, message: `${input.name} is ready` };
    },
    async UpdateMCPServer(name: string, input: MCPServerInput) {
      capServers = capServers.map((s) => {
        if (s.name !== name) return s;
        const connected = s.status === "connected" || s.status === "failed" || s.autoStart !== false;
        const nextStatus = s.status === "disabled" ? "disabled" : connected ? "connected" : "deferred";
        const nextTools = nextStatus === "connected" ? s.tools || (input.transport === "stdio" ? 3 : 5) : 0;
        return {
          ...s,
          transport: input.transport,
          status: nextStatus,
          command: input.transport === "stdio" ? input.command : "",
          args: input.transport === "stdio" ? input.args : [],
          url: input.transport === "stdio" ? "" : input.url,
          envKeys: input.env ? Object.keys(input.env).sort() : s.envKeys,
          headerKeys: input.headers ? Object.keys(input.headers).sort() : s.headerKeys,
          tools: nextTools,
          error: undefined,
          authStatus: nextStatus !== "connected" && input.transport !== "stdio" ? "possible" : undefined,
          authUrl: nextStatus !== "connected" && input.transport !== "stdio" ? input.url : undefined,
        };
      });
    },
    async RemoveMCPServer(name: string) {
      capServers = capServers.filter((s) => s.name !== name);
    },
    async AuthorizeAndConnectMCPServer(name: string) {
      capServers = capServers.map((s) => s.name === name
        ? { ...s, status: "connected", runtimeState: "ready", tools: s.tools || 4, error: undefined, requiresLaunchApproval: false }
        : s);
    },
    async AuthenticateMCPServer(name: string) {
      capServers = capServers.map((s) => s.name === name
        ? { ...s, status: "connected", runtimeState: "ready", tools: s.tools || 4, error: undefined, authStatus: "none", authUrl: undefined }
        : s);
    },
    async ReconnectMCPServer(name: string) {
      capServers = capServers.map((s) =>
        s.name === name
          ? { ...s, status: "initializing", error: undefined, authStatus: undefined, authUrl: undefined }
          : s,
      );
      await new Promise((r) => setTimeout(r, 400));
      capServers = capServers.map((s) =>
        s.name === name ? { ...s, status: "connected", tools: s.tools || 4 } : s,
      );
    },
    async ClearMCPServerAuthentication(name: string) {
      capServers = capServers.map((s) =>
        s.name === name
          ? {
              ...s,
              status: s.autoStart === false ? "disabled" : "initializing",
              tools: 0,
              error: undefined,
              authStatus: s.transport !== "stdio" ? "possible" : undefined,
              authUrl: s.transport !== "stdio" ? s.url : undefined,
              authConfigured: undefined,
            }
          : s,
      );
    },
    async PickSkillFolder() {
      return "~/my-skills";
    },
    async PickPluginFolder() {
      return "~/plugins/superpowers";
    },
    async AddSkillPath(path: string) {
      const dir = path.trim() || "~/my-skills";
      if (!capSkillRoots.some((r) => r.scope === "custom" && r.dir === dir)) {
        capSkillRoots.push({
          dir,
          scope: "custom",
          priority: capSkillRoots.length + 1,
          status: "ok",
          enabled: true,
          configured: true,
          removable: true,
          skills: 1,
          skillItems: [{ name: "local-dev", description: "Local custom development workflow", scope: "custom", runAs: "inline" }],
        });
      }
      if (!capSkills.some((s) => s.name === "local-dev")) {
        capSkills.push({ name: "local-dev", description: "Local custom development workflow", scope: "custom", runAs: "inline", enabled: true });
      }
    },
    async RemoveSkillPath(path: string) {
      capSkillRoots = capSkillRoots.filter((r) => r.dir !== path);
      if (!capSkillRoots.some((r) => r.scope === "custom")) {
        const idx = capSkills.findIndex((s) => s.name === "local-dev");
        if (idx >= 0) capSkills.splice(idx, 1);
      }
    },
    async SetSkillPathEnabled(path: string, enabled: boolean) {
      const root = capSkillRoots.find((r) => r.dir === path);
      if (root) {
        root.enabled = enabled;
        root.status = enabled ? "ok" : "disabled";
        root.skills = enabled ? (root.skillItems?.length ?? 0) : 0;
      }
    },
    async RefreshSkills() {},
    async ReloadCommands() {},
    async SetSkillEnabled(name: string, enabled: boolean) {
      const skill = capSkills.find((s) => s.name === name);
      if (skill) skill.enabled = enabled;
    },
    async SetSkillImplicitInvocation(_enabled: boolean) {},
    async AvailableSubagentTools() {
      return [
        { name: "read_file", description: "Read a file's contents", readOnlyHint: true },
        { name: "ls", description: "List a directory", readOnlyHint: true },
        { name: "glob", description: "Find files by name pattern", readOnlyHint: true },
        { name: "grep", description: "Search file contents", readOnlyHint: true },
        { name: "code_index", description: "Look up symbol definitions and file outlines", readOnlyHint: true },
        { name: "edit_file", description: "Edit an existing file" },
        { name: "write_file", description: "Write a new file" },
        { name: "bash", description: "Run a shell command" },
        { name: "web_fetch", description: "Fetch a URL" },
      ];
    },
    async CreateSubagentProfile(input: SubagentProfileInput) {
      const name = input.name.trim();
      const builtinNames = ["init", "explore", "research", "install-capability", "review", "security-review", "test"];
      if (builtinNames.includes(name)) throw new Error(`"${name}" is a built-in subagent name and cannot be reused`);
      if (capSkills.some((s) => s.name === name)) throw new Error(`"${name}" already exists`);
      capSkills.push({
        name, description: input.description, scope: input.scope === "project" ? "project" : "global",
        runAs: "subagent", enabled: true, model: input.model, effort: input.effort,
        allowedTools: input.allowedTools, color: input.color, invocation: `/${name}`, invocationMode: "manual",
      });
      return `~/.reasonix/skills/${name}/SKILL.md`;
    },
    async UpdateSubagentProfile(name: string, scope: string, input: SubagentProfileInput) {
      const skill = capSkills.find((s) => s.name === name && s.scope === scope);
      if (!skill) throw new Error(`"${name}" resolves at a different scope — refusing to update`);
      skill.description = input.description;
      skill.color = input.color;
      skill.model = input.model;
      skill.effort = input.effort;
      skill.allowedTools = input.allowedTools;
    },
    async DeleteSubagentProfile(name: string, scope: string) {
      const idx = capSkills.findIndex((s) => s.name === name && s.scope === scope);
      if (idx < 0) throw new Error(`"${name}" resolves at a different scope — refusing to delete`);
      capSkills.splice(idx, 1);
    },
    async SetSubagentProfileModel(name: string, ref: string) {
      const skill = capSkills.find((s) => s.name === name);
      if (skill) skill.configuredModel = ref || undefined;
    },
    async SetSubagentProfileEffort(name: string, level: string) {
      const skill = capSkills.find((s) => s.name === name);
      if (skill) skill.configuredEffort = level || undefined;
    },
    async CancelTrySubagentProfile() {},
    async TrySubagentProfile(input: SubagentProfileInput, task: string) {
      if (!task.trim()) throw new Error("task is required");
      if (!input.systemPrompt.trim()) throw new Error("system prompt is required");
      await new Promise((resolve) => setTimeout(resolve, 400));
      return `[mock run of "${input.name || "draft"}"]\n\nTask: ${task}\n\n(This is a dev-mode mock response — the real backend runs an isolated subagent loop against your configured model.)`;
    },
    async SetMCPServerEnabled(name: string, enabled: boolean) {
      capServers = capServers.map((s) =>
        s.name === name
          ? {
              ...s,
              status: enabled ? "connected" : "disabled",
              autoStart: s.builtIn ? enabled : s.autoStart,
              tools: enabled ? s.tools || 4 : 0,
              error: undefined,
              authStatus: !enabled && s.transport !== "stdio" ? "possible" : undefined,
              authUrl: !enabled && s.transport !== "stdio" ? s.url : undefined,
            }
          : s,
      );
    },
    async SetMCPServerTier(name: string, tier: string) {
      capServers = capServers.map((s) => {
        if (s.name !== name) return s;
        const tools = s.tools || (s.transport === "stdio" ? 3 : 5);
        return { ...s, tier, autoStart: true, status: "connected", tools, error: undefined, authStatus: undefined, authUrl: undefined };
      });
    },
    async SlashArgs(input: string) {
      // Mirror a slice of the real arg hints so the menu is exercisable in browser dev.
      const from = input.lastIndexOf(" ") + 1;
      const cur = input.slice(from);
      const cmd = input.slice(0, input.indexOf(" ") < 0 ? input.length : input.indexOf(" "));
      const subs: Record<string, { label: string; insert: string; hint: string; descend?: boolean }[]> = {
        "/skill": [
          { label: "list", insert: "list", hint: "list skills" },
          { label: "show", insert: "show ", hint: "show a skill's body", descend: true },
          { label: "enable", insert: "enable ", hint: "enable a disabled skill", descend: true },
          { label: "disable", insert: "disable ", hint: "disable an enabled skill", descend: true },
          { label: "new", insert: "new ", hint: "scaffold a new skill" },
          { label: "paths", insert: "paths", hint: "show discovery paths" },
        ],
        "/hooks": [
          { label: "list", insert: "list", hint: "list active hooks" },
        ],
        "/model": [
          { label: "deepseek/deepseek-v4-flash", insert: "deepseek/deepseek-v4-flash", hint: "current" },
          { label: "deepseek/deepseek-v4-pro", insert: "deepseek/deepseek-v4-pro", hint: "" },
        ],
        "/effort": [
          { label: "auto", insert: "auto", hint: "use the model default" },
          { label: "high", insert: "high", hint: "deeper reasoning" },
          { label: "max", insert: "max", hint: "maximum reasoning" },
        ],
      };
      const items = (subs[cmd] ?? [])
        .filter((it) => it.label.toLowerCase().startsWith(cur.toLowerCase()))
        .map((it) => ({ label: it.label, insert: it.insert, hint: it.hint, descend: it.descend ?? false }));
      return { items, from };
    },
    async ListDir(rel: string) {
      // A tiny fake tree so the @ menu is navigable in browser dev.
      if (rel === "" || rel === "./") {
        return [
          { name: "internal", isDir: true },
          { name: "desktop", isDir: true },
          { name: "README.md", isDir: false },
          { name: "go.mod", isDir: false },
        ];
      }
      if (rel === "internal/") {
        return [
          { name: "control", isDir: true },
          { name: "boot", isDir: true },
          { name: "event.go", isDir: false },
        ];
      }
      return [{ name: "file.go", isDir: false }];
    },
    async ListDirForTab(_tabID: string, rel: string) {
      return this.ListDir(rel);
    },
    async SearchFileRefs(query: string) {
      const q = query.toLowerCase();
      return ["desktop/frontend/src/lib/bridge.ts", "frontend/wailsjs/runtime/runtime.js", "internal/control/refs.go"]
        .filter((path) => path.split("/").pop()?.toLowerCase().includes(q))
        .map((name) => ({ name, isDir: false }));
    },
    async SearchFileRefsForTab(_tabID: string, query: string) {
      return this.SearchFileRefs(query);
    },
    async ReadFile(rel: string) {
      return mockWorkspaceFile(rel);
    },
    async ReadFileForTab(_tabID: string, rel: string) {
      return this.ReadFile(rel);
    },
    async ResolveMarkdownImageForTab(_tabID: string, source: string) {
      return { url: source, openHref: source };
    },
    async WorkspaceRevisionForTab(_tabID: string) {
      return { revisions: { content: 0, tree: 0, workingTree: 0, gitMeta: 0, session: 0 }, watchState: "active" as const };
    },
    async WorkspaceChanges(_tabID: string) {
      return {
        gitAvailable: true,
        gitBranch: "main",
        files: [
          {
            path: "desktop/frontend/src/components/WorkspacePanel.tsx",
            sources: ["session", "git"],
            gitStatus: "M",
            turns: [0, 2],
            latestPrompt: "Mock session edited the workspace panel.",
            latestTime: Date.now() - 60_000,
          },
          { path: "README.md", sources: ["git"], gitStatus: "??" },
          { path: "internal/control/controller.go", sources: ["session"], turns: [1], latestTime: Date.now() - 120_000 },
        ],
      };
    },
    async WorkspaceChangeDetail(_tabID: string, path: string) {
      return {
        source: "git" as const,
        added: 2,
        removed: 1,
        diff: `diff --git a/${path} b/${path}\n--- a/${path}\n+++ b/${path}\n@@ -1,2 +1,3 @@\n-old line\n+new line\n context\n+another line`,
      };
    },
    async GitBranches() {
      return ["main", "dev", "feature/branch-switcher"];
    },
    async GitCheckout(_branch: string) {
      console.info("mock GitCheckout", _branch);
    },
    async WorkspaceGitHistory(_tabID: string, path: string) {
      return [
        { hash: "abcdef123456", author: "Mock Author", date: new Date().toISOString(), message: "Mock commit message for " + path },
      ];
    },
    async WorkspaceGitCommitDetail(_tabID: string, _hash: string, path: string) {
      if (path) {
        return { diff: "--- a/mock\n+++ b/mock\n@@ -1,1 +1,1 @@\n-mock\n+mock diff" };
      }
      return { files: ["mock_file_1.ts", "mock_file_2.ts"] };
    },
    async OpenWorkspacePath(rel: string) {
      console.info("mock OpenWorkspacePath", rel);
    },
    async OpenLocalPath(path: string) {
      console.info("mock OpenLocalPath", path);
    },
    async OpenWorkspacePathForTab(_tabID: string, rel: string) {
      await this.OpenWorkspacePath(rel);
    },
    async ResolveWorkspacePathForTab(_tabID: string, rel: string) { return `${cwd.replace(/[\\/]+$/, "")}/${rel.replace(/^[/\\]+/, "").replace(/[\\/]+$/, "")}`; },
    async ExternalOpeners() {
      return {
        openers: [
          { id: "vscode", name: "VS Code", kind: "editor", iconDataUrl: mockExternalOpenerIconDataURL("#1684d6", "V") },
          { id: "cursor", name: "Cursor", kind: "editor", iconDataUrl: mockExternalOpenerIconDataURL("#25262a", "C") },
          { id: "finder", name: "Finder", kind: "file-manager", iconDataUrl: mockExternalOpenerIconDataURL("#36aaf4", "F") },
          { id: "ghostty", name: "Ghostty", kind: "terminal", iconDataUrl: mockExternalOpenerIconDataURL("#264db6", ">") },
        ],
        preferred: "vscode",
      } as ExternalOpenersView;
    }, async ExternalOpenersForTab(_tabID: string) { return { ...(await this.ExternalOpeners()), workspaceOpenable: true }; },
    async SetPreferredExternalOpener(_id: string) {},
    async OpenWorkspaceInExternalOpener(_id: string) {},
    async OpenWorkspaceInExternalOpenerForTab(_tabID: string, id: string) {
      await this.OpenWorkspaceInExternalOpener(id);
    }, async OpenLocalPathInExternalOpener(path: string, id: string) { console.info("mock OpenLocalPathInExternalOpener", path, id); }, async SaveLocalPathAs(path: string) { console.info("mock SaveLocalPathAs", path); return path; },
    async RevealWorkspacePath(rel: string) {
      console.info("mock RevealWorkspacePath", rel);
    },
    async RevealWorkspacePathForTab(_tabID: string, rel: string) {
      await this.RevealWorkspacePath(rel);
    },
    async RevealPath(path: string) {
      console.info("mock RevealPath", path);
    },
    async SavePastedImage(dataUrl: string) {
      const path = `.reasonix/attachments/mock-${mockAttachmentDataURLs.size + 1}.png`;
      mockAttachmentDataURLs.set(path, dataUrl);
      return path;
    },
    async SaveClipboardImage() {
      const path = `.reasonix/attachments/mock-clipboard-${mockAttachmentDataURLs.size + 1}.png`;
      mockAttachmentDataURLs.set(path, mockPreviewImageDataURL);
      return path;
    },
    async SavePastedFile(name: string, dataUrl: string) {
      const path = `.reasonix/attachments/mock-${name}`;
      mockAttachmentDataURLs.set(path, dataUrl);
      return path;
    },
    async PickExportFile(defaultFilename: string, _mimeType: string) {
      return defaultFilename;
    },
    async SaveExportFile(path: string, payload: string, base64Encoded: boolean) {
      const a = document.createElement("a");
      let url = "";
      if (base64Encoded) {
        url = `data:application/octet-stream;base64,${payload}`;
      } else {
        url = URL.createObjectURL(new Blob([payload], { type: "text/plain;charset=utf-8" }));
      }
      a.href = url;
      a.download = path;
      document.body.appendChild(a);
      a.click();
      a.remove();
      if (!base64Encoded) URL.revokeObjectURL(url);
    },
    async SaveExportImageFiles(path: string, payloads: string[]) {
      if (payloads.length === 0) throw new Error("No image payloads to export");
      const slash = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
      const dot = path.lastIndexOf(".");
      const extensionStart = dot > slash ? dot : path.length;
      const stem = path.slice(0, extensionStart);
      const extension = path.slice(extensionStart);
      for (let index = 0; index < payloads.length; index++) {
        const partPath = payloads.length > 1
          ? `${stem}-${index + 1}-of-${payloads.length}${extension}`
          : path;
        await this.SaveExportFile(partPath, payloads[index], true);
      }
    },
    async AttachDropped(path: string) {
      const name = path.split(/[/\\]/).filter(Boolean).pop() ?? path;
      const hasExt = /\.\w{1,6}$/i.test(name);
      if (!hasExt) {
        const tokenName = name.replace(/[^\w.-]+/g, "-") || "folder";
        return { kind: "workspace" as const, path: `__reasonix_external_folder/mock/${tokenName}`, isDir: true, displayPath: path };
      }
      const attachmentPath = `.reasonix/attachments/mock-${name}`;
      mockAttachmentDataURLs.set(attachmentPath, mockPreviewImageDataURL);
      return { kind: "attachment" as const, path: attachmentPath };
    },
    async AttachmentDataURL(path: string) {
      return mockAttachmentDataURLs.get(path) ?? mockPreviewImageDataURL;
    },
        async Models() {
          const active = mockTabs.find((tab) => tab.active) ?? mockTabs[0];
          const current = mockTabModelRef(active);
          return mockModelCatalog.map((model) => ({ ...model, current: model.ref === current }));
        },
        async ModelsForTab(tabID) {
          const tab = mockTabs.find((item) => item.id === tabID) ?? mockTabs.find((item) => item.active) ?? mockTabs[0];
          const current = mockTabModelRef(tab);
          return mockModelCatalog.map((model) => ({ ...model, current: model.ref === current }));
        },
        async SetModel(name) {
          setMockTabModel(undefined, name);
        },
        async SetModelForTab(tabID, name) {
          setMockTabModel(tabID, name);
        },
        async Effort() {
          return { supported: true, current: mockEffort, default: "high", levels: ["auto", "high", "max"] };
        },
        async EffortForTab() {
          return this.Effort();
        },
        async SetEffort(level: string) {
          mockEffort = level || "auto";
        },
        async SetEffortForTab(_tabID, level) {
          await this.SetEffort(level);
        },
        async ReloadRuntime(_tabID) {},
    async Memory() {
      return {
        available: true,
        storeDir: "~/.reasonix/projects/-mock/memory",
        storeGlobalDir: "~/.reasonix/memory/global",
        docs: [
          {
            path: "REASONIX.md",
            scope: "project",
            directory: ".",
            body: "# Reasonix project memory\n\nMock doc shown in the browser dev seam.\n\n## Notes\n\n- prefers concise replies",
            imports: [],
            depth: 0,
            order: 0,
            precedence: 0,
          },
          {
            path: "~/.reasonix/REASONIX.md",
            scope: "user",
            body: t("mock.memoryBody"),
            imports: [],
            depth: -1,
            order: 1,
            precedence: 1,
          },
        ],
        instructionDiagnostics: [],
        facts: [
          {
            name: "prefers-tabs",
            description: "User prefers tabs",
            type: "user",
            scope: "project",
            body: "Indent with tabs.",
            freshness: "fresh",
          },
        ],
        archives: [
          {
            name: "old-plan",
            description: "Superseded planning note",
            type: "project",
            scope: "project",
            body: "This plan was archived after the implementation changed.",
            path: "~/.reasonix/projects/-mock/memory/.archive/20260612-021500.000-old-plan.md",
            archivedAt: "2026-06-12T02:15:00Z",
            freshness: "current",
          },
        ],
        scopes: [
          { scope: "user", path: "~/.reasonix/REASONIX.md" },
          { scope: "project", path: "REASONIX.md" },
          { scope: "local", path: "REASONIX.local.md" },
        ],
        conflicts: [],
        lastRecall: {
          query: "",
          hits: [],
          omitted: 0,
          charBudget: 2400,
          usedChars: 0,
          suppressed: "no user turn yet",
        },
      };
    },
    async MemorySuggestions() {
      return {
        memories: [
          {
            id: "memory-prefers-concise-replies",
            name: "prefers-concise-replies",
            title: "Prefers concise replies",
            description: "User prefers concise replies unless detail is requested.",
            type: "user",
            scope: "project",
            body: "User prefers concise replies unless detail is requested.\n\n**Why:** Suggested from recent local history.\n**How to apply:** Keep answers brief by default.",
            reason: "future-facing preference",
            evidence: ["mock-session: always keep replies concise"],
          },
        ],
        skills: [
          {
            id: "skill-reasonix-pr-followup",
            name: "reasonix-pr-followup",
            description: "Review or update a Reasonix GitHub PR, address feedback, verify, and publish safely.",
            scope: "project",
            body: "# Reasonix PR Followup\n\nUse this skill for repeated Reasonix PR work.\n\n## Workflow\n\n1. Confirm branch and PR state.\n2. Inspect the diff.\n3. Fix actionable feedback.\n4. Verify and update the PR.\n",
            reason: "recent history repeatedly touched PR workflows",
            evidence: ["mock-pr-session: 提交到pr，并更新内容", "mock-review-session: 解决该pr下机器人提出来的问题"],
          },
        ],
        generatedAt: new Date().toISOString(),
        available: true,
        source: "mock",
      };
    },
    async AcceptMemorySuggestion(suggestion: MemorySuggestion) {
      emit({ kind: "notice", level: "info", text: `saved suggested memory → ${suggestion.name}` });
      return `${suggestion.name}.md`;
    },
    async AcceptSkillSuggestion(suggestion: SkillSuggestion) {
      emit({ kind: "notice", level: "info", text: `created suggested skill → ${suggestion.name}` });
      return `.reasonix/skills/${suggestion.name}/SKILL.md`;
    },
    async MemorySuggestionsForTab(_tabID: string) {
      return this.MemorySuggestions();
    },
    async AcceptMemorySuggestionForTab(_tabID: string, suggestion: MemorySuggestion) {
      return this.AcceptMemorySuggestion(suggestion);
    },
    async AcceptSkillSuggestionForTab(_tabID: string, suggestion: SkillSuggestion) {
      return this.AcceptSkillSuggestion(suggestion);
    },
    async MemoryForTab(_tabID: string) {
      return this.Memory();
    },
    async MemoryRevisions(_ref: string) {
      return [];
    },
    async MemoryRevisionsForTab(_tabID: string, ref: string) {
      return this.MemoryRevisions(ref);
    },
    async RestoreMemoryRevision(ref: string, revision: number) {
      emit({ kind: "notice", level: "info", text: `restored revision → ${ref}@${revision}` });
      return {
        id: ref,
        revision: revision + 1,
        name: ref,
        description: "Restored memory revision",
        type: "project",
        scope: "project",
        body: "Restored guidance.",
        freshness: "fresh",
      };
    },
    async RestoreMemoryRevisionForTab(_tabID: string, ref: string, revision: number) {
      return this.RestoreMemoryRevision(ref, revision);
    },
    async Remember(_scope: string, _note: string) {
      emit({ kind: "notice", level: "info", text: `remembered → ${_scope}` });
      return `${_scope} REASONIX.md (mock): ${_note}`;
    },
    async RememberForTab(_tabID: string, scope: string, note: string) {
      return this.Remember(scope, note);
    },
    async Forget(_name: string) {
      emit({ kind: "notice", level: "info", text: `forgot → ${_name}` });
    },
    async ForgetForTab(_tabID: string, name: string) {
      return this.Forget(name);
    },
    async RestoreArchivedMemory(archivePath: string) {
      emit({ kind: "notice", level: "info", text: `restored → ${archivePath}` });
      return {
        id: "mock-restored-memory",
        revision: 2,
        name: "restored-memory",
        description: "Recovered archived memory",
        type: "project",
        scope: "project",
        body: "Recovered guidance.",
        freshness: "fresh",
      };
    },
    async RestoreArchivedMemoryForTab(_tabID: string, archivePath: string) {
      return this.RestoreArchivedMemory(archivePath);
    },
    async SaveDoc(_path: string, _body: string) {
      emit({ kind: "notice", level: "info", text: `saved → ${_path}` });
      return _path;
    },
    async SaveDocForTab(_tabID: string, path: string, body: string) {
      return this.SaveDoc(path, body);
    },
    async DesktopStartupSettings() {
      const { bot, desktopLanguage, desktopLayoutStyle, desktopTheme, desktopThemeStyle, desktopTerminalTheme, displayMode, sessionExperience, reasoningDisplayMode, reasoningDisplayModeExplicit, statusBarStyle, statusBarItems, checkUpdates, conversationWidth } = settings;
      return JSON.parse(JSON.stringify({
        bot,
        desktopLanguage,
        desktopLayoutStyle,
        desktopTheme,
        desktopThemeStyle,
        desktopTerminalTheme,
        displayMode, sessionExperience, reasoningDisplayMode, reasoningDisplayModeExplicit,
        statusBarStyle,
        statusBarItems,
        checkUpdates,
        conversationWidth,
      })) as DesktopStartupSettingsView;
    },
    async Settings() {
      await loadMockProviderCatalog();
      for (const preset of mockProviderPresetViews()) {
        const existing = settings.providerPresets.find(p => p.id === preset.id);
        if (existing) existing.catalog = preset.catalog;
        else settings.providerPresets.push(preset);
      }
      return JSON.parse(JSON.stringify(settings)) as SettingsView;
    },
    async StorageSettings() { return { defaultWorkspace: cwd, statePath: `${cwd}/.reasonix`, cachePath: `${cwd}/.reasonix/cache`, extensionsPath: `${cwd}/.reasonix/plugins` }; },
    async HooksSettings(scope: string) {
      const key = scope === "project" ? "project" : "global";
      return JSON.parse(JSON.stringify(hookSettings[key])) as HooksSettingsView;
    },
    async SaveHooksSettings(scope: string, hooks: HookConfigView[]) {
      const key = scope === "project" ? "project" : "global";
      hookSettings[key].hooks = JSON.parse(JSON.stringify(hooks)) as HookConfigView[];
    },
    async SaveHooksSettingsForRoot(scope: string, _projectRoot: string, hooks: HookConfigView[]) {
      const key = scope === "project" ? "project" : "global";
      hookSettings[key].hooks = JSON.parse(JSON.stringify(hooks)) as HookConfigView[];
    },
    async TrustProjectHooks() {
      // Compatibility no-op: project hooks are enabled automatically.
    },
    async TrustProjectHooksForRoot(_projectRoot: string) {
      // Compatibility no-op: project hooks are enabled automatically.
    },
    async SetDefaultModel(ref: string) {
      settings.defaultModel = ref;
    },
    async SetPlannerModel(ref: string) {
      settings.plannerModel = ref;
    },
    async SetVisionModel(ref: string) {
      settings.visionModel = ref;
    },
    async SetSubagentModel(ref: string) {
      settings.subagentModel = ref;
    },
    async SetSubagentEffort(level: string) {
      settings.subagentEffort = level;
    },
    async SetMaxSubagentDepth(depth: number) {
      settings.agent = { ...settings.agent, maxSubagentDepth: depth <= 1 ? 1 : 2 };
    },
    async SetMaxSubagentConcurrency(n: number) {
      const total = Math.max(1, Math.min(32, Math.floor(n) || 6));
      const writers = Math.min(total, Math.max(1, settings.agent.maxParallelWriters || 3));
      settings.agent = { ...settings.agent, maxSubagentConcurrency: total, maxParallelWriters: writers };
    },
    async SetMaxParallelWriters(n: number) {
      const total = Math.max(1, Math.min(32, settings.agent.maxSubagentConcurrency || 6));
      const writers = Math.max(1, Math.min(total, Math.floor(n) || 3));
      settings.agent = { ...settings.agent, maxParallelWriters: writers };
    },
    async SetAutoPlan(mode: string) {
      if (mode !== "off") throw new Error("Automatic plan mode has been retired; use Plan Mode explicitly.");
      settings.autoPlan = "off";
    },
    async SetDefaultToolApprovalMode(mode: string) {
      settings.defaultToolApprovalMode = normalizeToolApprovalMode(mode);
    },
    async SetDefaultAutoRecoveryCheckpoint(_enabled: boolean) {
      // Legacy no-op; Auto Guard is always built into Auto.
    },
    async SetConnectionKey(name: string, value: string) {
      const p = settings.providers.find(p => p.name === name);
      if (!p) throw new Error("Connection not found");
      p.apiKeyEnv = `REASONIX_CONNECTION_${crypto.randomUUID().replaceAll("-", "")}_KEY`;
      p.keySet = Boolean(value.trim());
      return "";
    },
    async AddProviderConnectionWithOptions(_presetID: string, sourceName: string, key: string, baseURL: string, kind: string) {
      const source = settings.providers.find(p => p.name === sourceName);
      if (!source) throw new Error("Connection template unavailable in preview");
      settings.providers.push({...source, kind:kind || source.kind, baseUrl:baseURL || source.baseUrl, requestUrl:"", chatUrl:"", modelsUrl:"", name:`${source.name}-${crypto.randomUUID()}`, builtIn:false, added:true, apiKeyEnv:`REASONIX_CONNECTION_${crypto.randomUUID().replaceAll("-", "")}_KEY`, keySet:Boolean(key.trim())});
      return "";
    },
    async AddProviderConnectionWithURL(_presetID: string, sourceName: string, key: string, baseURL: string) {
      const source = settings.providers.find(p => p.name === sourceName);
      if (!source) throw new Error("Connection template unavailable in preview");
      settings.providers.push({...source, baseUrl:baseURL, requestUrl:"", chatUrl:"", modelsUrl:"", name:`${source.name}-${crypto.randomUUID()}`, builtIn:false, added:true, apiKeyEnv:`REASONIX_CONNECTION_${crypto.randomUUID().replaceAll("-", "")}_KEY`, keySet:Boolean(key.trim())});
      return "";
    },
    async AddProviderConnection(_presetID: string, sourceName: string, key: string) {
      const source = settings.providers.find(p => p.name === sourceName);
      if (!source) throw new Error("Connection template unavailable in preview");
      settings.providers.push({...source, name:`${source.name}-${crypto.randomUUID()}`, builtIn:false, added:true, apiKeyEnv:`REASONIX_CONNECTION_${crypto.randomUUID().replaceAll("-", "")}_KEY`, keySet:Boolean(key.trim())});
      return "";
    },
    async RenameProviderConnections(names: string[], displayName: string) {
      for (const name of names) if (!settings.providers.some(p => p.name === name)) throw new Error(`Provider ${name} not found`);
      settings.providers = settings.providers.map(p => names.includes(p.name) ? {...p, displayName: displayName.trim()} : p);
    },
    async SaveProvider(p: ProviderView) {
      p.added = true;
      const i = settings.providers.findIndex((x) => x.name === p.name);
      if (i >= 0) settings.providers[i] = { ...settings.providers[i], ...p };
      else settings.providers.push(p);
    },
    async SetProviderWebSearch(names: string[], enabled: boolean) {
      const requested = new Set(names);
      settings.providers = settings.providers.map((provider) => (
        requested.has(provider.name) ? { ...provider, webSearch: enabled } : provider
      ));
    },
    async SaveProviderModelCatalogs(updates: ProviderModelCatalogUpdate[]) {
      const applied: string[] = [];
      for (const update of updates) {
        const i = settings.providers.findIndex((provider) => provider.name === update.name);
        if (i < 0) continue;
        const current = settings.providers[i];
        if (!update.expectedFingerprint || current.modelCatalogFingerprint !== update.expectedFingerprint) continue;
        settings.providers[i] = {
          ...current,
          models: [...update.models],
          default: update.default,
          visionModels: [...update.visionModels],
          modelCatalogFingerprint: `${update.expectedFingerprint}:updated`,
        };
        applied.push(update.name);
      }
      return applied;
    },
    async SaveProviderWithKey(p: ProviderView, key: string) {
      p.added = true;
      p.keySet = Boolean(key.trim()) || p.keySet;
      const i = settings.providers.findIndex((x) => x.name === p.name);
      if (i >= 0) settings.providers[i] = p;
      else settings.providers.push(p);
      return "";
    },
    async AddOfficialProviderAccess(kind: string, key: string) {
      const templates: Record<string, ProviderView> = {
        deepseek: { name: "deepseek", builtIn: true, added: true, kind: "openai", baseUrl: "https://api.deepseek.com", modelsUrl: "", models: ["deepseek-v4-flash", "deepseek-v4-pro"], visionModels: [], visionModelsConfigured: false, default: "deepseek-v4-flash", apiKeyEnv: "DEEPSEEK_API_KEY", keySet: !!key.trim(), balanceUrl: "https://api.deepseek.com/user/balance", contextWindow: 1_000_000, reasoningProtocol: "", thinking: "enabled", webSearch: true, serverWebSearchCapability: true, supportedEfforts: [], defaultEffort: "", modelOverrides: [{ model: "deepseek-v4-flash", reasoningProtocol: "", supportedEfforts: ["disabled", "low", "high", "max"], defaultEffort: "high" }, { model: "deepseek-v4-pro", reasoningProtocol: "", supportedEfforts: ["disabled", "high", "max"], defaultEffort: "high" }] },
      };
      const next = templates[kind];
      if (!next) throw new Error(`unknown official provider template ${kind}`);
      const i = settings.providers.findIndex((x) => x.name === next.name);
      if (i >= 0) settings.providers[i] = { ...settings.providers[i], ...next, keySet: next.keySet || settings.providers[i].keySet };
      else settings.providers.push(next);
      return "";
    },
    async UpgradeDeepSeekProviderAccess(name: string) {
      const family = new Set(["deepseek", "deepseek-flash", "deepseek-pro"]);
      let changed = false;
      settings.providers = settings.providers.map((provider) => {
        if ((name === "deepseek" ? family.has(provider.name) : provider.name === name) && provider.recommendedUpgradeAvailable) {
          changed = true;
          return {
            ...provider,
            kind: "anthropic",
            baseUrl: "https://api.deepseek.com/anthropic",
            thinking: provider.thinking || "enabled",
            webSearch: provider.webSearch ?? true,
            serverWebSearchCapability: true,
            recommendedUpgradeAvailable: false,
          };
        }
        return provider;
      });
      if (!changed) throw new Error(`DeepSeek provider ${name} is not eligible for upgrade`);
      return "";
    },
    async AddProviderPresetAccess(id: string, key: string) {
      const preset = settings.providerPresets.find((p) => p.id === id);
      if (!preset) throw new Error(`unknown provider preset ${id}`);
      const next = cloneMockProviderTemplates(id, key);
      if (!next) throw new Error(`unknown provider preset ${id}`);
      for (const provider of next) {
        const i = settings.providers.findIndex((x) => x.name === provider.name);
        if (i >= 0) settings.providers[i] = { ...settings.providers[i], ...provider, keySet: provider.keySet || settings.providers[i].keySet };
        else settings.providers.push(provider);
      }
      preset.added = true;
      preset.status = "installed";
      preset.statusProviderNames = [...preset.providerNames];
      preset.keySet = preset.keySet || !!key.trim();
      preset.configured = !preset.requiresKey || preset.keySet;
      return "";
    },
    async ResetProviderPresetAccess(id: string) {
      const preset = settings.providerPresets.find((p) => p.id === id);
      if (!preset) throw new Error(`unknown provider preset ${id}`);
      const next = cloneMockProviderTemplates(id, "");
      if (!next) throw new Error(`unknown provider preset ${id}`);
      for (const provider of next) {
        const i = settings.providers.findIndex((x) => x.name === provider.name);
        if (i < 0) throw new Error(`provider preset ${id} cannot be reset because no same-name provider exists`);
        const existing = settings.providers[i];
        settings.providers[i] = {
          ...provider,
          added: true,
          keySet: existing.apiKeyEnv === provider.apiKeyEnv ? existing.keySet : provider.keySet,
        };
      }
      preset.added = true;
      preset.status = "installed";
      preset.statusProviderNames = [...preset.providerNames];
      preset.keySet = preset.keySet || next.every((provider) => settings.providers.some((item) => item.name === provider.name && item.keySet));
      preset.configured = !preset.requiresKey || preset.keySet;
    },
    async FetchProviderModels(p: ProviderView) {
      if (!p.baseUrl.trim()) throw new Error(t("settings.fetchModelsMissingBaseUrl"));
      if (providerRequiresKey(p) && !p.apiKeyEnv.trim()) throw new Error(t("settings.fetchModelsMissingKeyEnv"));
      await delay(350);
      if (p.baseUrl.includes("deepseek")) return ["deepseek-v4-flash", "deepseek-v4-pro"];
      if (p.baseUrl.includes("token-plan")) return ["mimo-v2.5", "mimo-v2.5-pro"];
      if (p.baseUrl.includes("xiaomimimo")) return ["mimo-v2.5-pro", "mimo-v2.5"];
      return ["gpt-5", "gpt-5-mini", "qwen3-coder"];
    },
    async FetchProviderModelCatalogDraft(p: ProviderView, _key: string) {
      return this.FetchProviderModelCatalog(p);
    },
    async TestProviderModel(_p: ProviderView, _model: string, _key: string) {},
    async FetchProviderModelCatalog(p: ProviderView) {
      const models = await this.FetchProviderModels(p);
      return models.map((model) => ({
        model,
        inputModalities: p.modelCapabilities?.find((item) => item.model === model)?.inputModalities ?? [],
        state: p.modelCapabilities?.find((item) => item.model === model)?.state ?? "unknown",
        source: "adapter",
      }));
    },
    async FetchAllProviderModels(providers: ProviderView[]) {
      const out: Record<string, string[]> = {};
      for (const p of providers) {
        try {
          out[p.name] = await this.FetchProviderModels(p);
        } catch {
          out[p.name] = [];
        }
      }
      return out;
    },
    async FetchAllProviderModelCatalogs(providers: ProviderView[]) {
      const out: Record<string, ProviderModelCapabilityView[]> = {};
      for (const p of providers) {
        try {
          out[p.name] = await this.FetchProviderModelCatalog(p);
        } catch {
          out[p.name] = [];
        }
      }
      return out;
    },
    async DeleteProvider(name: string) {
      settings.providers = settings.providers.filter((p) => p.name !== name);
    },
    async RemoveProviderAccess(name: string) { settings.providers = removeProviderAccessesForMock(settings.providers, [name]); },
    async RemoveProviderAccesses(names: string[]) { settings.providers = removeProviderAccessesForMock(settings.providers, names); },
    async SaveProviderKey(apiKeyEnv: string, _value: string) {
      settings.providers.forEach((p) => {
        if (p.apiKeyEnv === apiKeyEnv) p.keySet = true;
      });
      return "";
    },
    async SetProviderKey(apiKeyEnv: string, _value: string) {
      settings.providers.forEach((p) => {
        if (p.apiKeyEnv === apiKeyEnv) p.keySet = true;
      });
      return "";
    },
    async ClearProviderKey(apiKeyEnv: string) {
      settings.providers.forEach((p) => {
        if (p.apiKeyEnv === apiKeyEnv) p.keySet = false;
      });
    },
    async SetPermissionMode(mode: string) {
      settings.permissions.mode = mode;
    },
    async AddPermissionRule(list: string, rule: string) {
      const k = list as "allow" | "ask" | "deny";
      if (settings.permissions[k] && !settings.permissions[k].includes(rule)) settings.permissions[k].push(rule);
    },
    async RemovePermissionRule(list: string, rule: string) {
      const k = list as "allow" | "ask" | "deny";
      settings.permissions[k] = settings.permissions[k].filter((r) => r !== rule);
    },
        async ReloadSettings() {},
        async SetShellPreference(prefer: string) {
          const sb = settings.sandbox;
          if (!sb) return;
          sb.shell = prefer;
          sb.resolvedShell = browserPreviewEffectiveShell(prefer);
          sb.shellReloadRequired = sb.resolvedShell !== sb.effectiveShell;
        },
        async InstallShellSupport(id: string): Promise<ShellInstallResult> {
          if (id !== "git-for-windows") throw new Error(`unknown shell support action ${id}`);
          if (browserPlatformOverride() !== "windows") return { status: "unsupported_platform", reason: "shell helper install is only available on Windows" };
          return {
            status: "manual_required",
            reason: "automatic installation is disabled because Git for Windows cannot reliably honor user scope",
            manualUrl: "https://git-scm.com/download/win",
          };
        },
        async CancelShellInstall() {},
        async SetSandbox(bash: string, network: boolean, workspaceRoot: string, allowWrite: string[], shell: string) {
          const effectiveWorkspaceRoot = workspaceRoot.trim() || cwd;
          const prev = settings.sandbox;
          const effectiveShell = browserPreviewEffectiveShell(shell);
          const shellSupport = browserPreviewShellSupport(browserPlatformOverride());
          settings.sandbox = { bash, network, workspaceRoot, allowWrite, effectiveWorkspaceRoot, effectiveWriteRoots: [effectiveWorkspaceRoot, ...allowWrite], shell, effectiveShell,
            resolvedShell: effectiveShell, shellReloadRequired: false,
            shellCapabilities: prev?.shellCapabilities ?? shellSupport.shellCapabilities, shellInstallAction: prev?.shellInstallAction ?? shellSupport.shellInstallAction,
            shellRepairGuidance: prev?.shellRepairGuidance ?? shellSupport.shellRepairGuidance,
            gitCapability: prev?.gitCapability ?? shellSupport.gitCapability, gitRepairGuidance: prev?.gitRepairGuidance ?? shellSupport.gitRepairGuidance };
        },
        async SetNetwork(n: NetworkView) {
          settings.network = n;
        },
        async SetBotSettings(b: BotSettingsView) {
          settings.bot = JSON.parse(JSON.stringify(b)) as BotSettingsView;
        },
        async SetBotConnectionToolApprovalMode(connID, mode) {
          const conn = settings.bot.connections.find((c) => c.id === connID);
          if (conn) conn.toolApprovalMode = mode as any;
        },
        async SetBotDingtalkToolApprovalMode(mode) {
          settings.bot.dingtalk.toolApprovalMode = mode as any;
        },
        async SetBotSecret(envName: string, _value: string) {
          const name = envName.trim();
          if (settings.bot.qq.appSecretEnv === name) settings.bot.qq.secretSet = true;
          if (settings.bot.feishu.appSecretEnv === name) settings.bot.feishu.secretSet = true;
          if (settings.bot.weixin.tokenEnv === name) settings.bot.weixin.tokenSet = true;
          settings.bot.connections = settings.bot.connections.map((connection) => ({
            ...connection,
            credential: connection.credential.appSecretEnv === name || connection.credential.tokenEnv === name
              ? { ...connection.credential, secretSet: true }
              : connection.credential,
          }));
        },
        async ClearBotSecret(envName: string) {
          const name = envName.trim();
          if (settings.bot.qq.appSecretEnv === name) settings.bot.qq.secretSet = false;
          if (settings.bot.feishu.appSecretEnv === name) settings.bot.feishu.secretSet = false;
          if (settings.bot.weixin.tokenEnv === name) settings.bot.weixin.tokenSet = false;
          settings.bot.connections = settings.bot.connections.map((connection) => ({
            ...connection,
            credential: connection.credential.appSecretEnv === name || connection.credential.tokenEnv === name
              ? { ...connection.credential, secretSet: false }
              : connection.credential,
          }));
        },
        async BotRuntimeStatus() {
          const qqRunning = settings.bot.qq.enabled && settings.bot.qq.appId.trim() && settings.bot.qq.secretSet;
          const runningConnections = (qqRunning ? 1 : 0) + settings.bot.connections.filter((connection) => connection.enabled && connection.status === "connected").length;
          const dingtalkRunning = Boolean(settings.bot.dingtalk.enabled && settings.bot.dingtalk.clientId.trim() && settings.bot.dingtalk.secretSet);
          const running = settings.bot.enabled && runningConnections > 0;
          return {
            running,
            status: running ? "running" : "stopped",
            message: running ? `${runningConnections} bot connection(s) running` : "bot runtime is not started",
            connections: runningConnections,
            startedAt: running ? new Date(t0).toISOString() : "",
            platforms: {
              dingtalk: dingtalkRunning ? "running" : settings.bot.dingtalk.clientId.trim() ? "configured" : "",
              qq: qqRunning ? "running" : settings.bot.qq.appId.trim() ? "configured" : "",
              ...Object.fromEntries(settings.bot.connections
                .filter((connection) => connection.provider !== "qq")
                .map((connection) => [connection.provider, connection.enabled && connection.status === "connected" ? "running" : connection.credential.secretSet ? "configured" : ""])),
            },
          };
        },
        async StartBotConnectionInstall(provider: string, domain: string) {
          const normalizedProvider = provider === "weixin" ? "weixin" : "feishu";
          const normalizedDomain = normalizedProvider === "weixin" ? "weixin" : domain === "lark" ? "lark" : "feishu";
          return {
            ok: true,
            provider: normalizedProvider,
            domain: normalizedDomain,
            installId: `mock-${normalizedProvider}-${normalizedDomain}`,
            url: "https://example.com/reasonix-bot-qr",
            deviceCode: "MOCKDEVICE",
            userCode: normalizedProvider === "weixin" ? "" : "MOCK-CODE",
            interval: 3,
            expireIn: 300,
            message: "",
          };
        },
        async PollBotConnectionInstall(installID: string) {
          const isWeixin = installID.includes("weixin");
          const domain = installID.includes("lark") ? "lark" : isWeixin ? "weixin" : "feishu";
          const provider = isWeixin ? "weixin" : "feishu";
          const connection = {
            id: `${provider}-${domain}`,
            provider,
            domain,
            label: domain === "lark" ? "Lark" : domain === "weixin" ? "微信" : "飞书",
            enabled: true,
            status: "connected",
	            model: "",
	            toolApprovalMode: "",
	            workspaceRoot: "",
	            access: { enabled: true, allowAll: false, pairingEnabled: true, users: [provider === "weixin" ? "wxid_mock_user_001" : "ou_mock_user_001"], groups: [], approvers: [], admins: [] },
	            credential: {
              appId: provider === "feishu" ? "cli_mock" : "",
              appSecretEnv: provider === "feishu" ? (domain === "lark" ? "LARK_BOT_APP_SECRET" : "FEISHU_BOT_APP_SECRET") : "",
              accountId: provider === "weixin" ? "mock-account" : "",
              tokenEnv: provider === "weixin" ? "WEIXIN_BOT_TOKEN" : "",
              secretSet: true,
            },
            sessionMappings: [],
            lastError: "",
            createdAt: new Date().toISOString(),
            updatedAt: new Date().toISOString(),
          };
          settings.bot.connections = [...settings.bot.connections.filter((c) => c.id !== connection.id), connection];
          return { done: true, connection, status: "connected", message: "connected", error: "" };
        },
        async DiagnoseBotConnection(id: string) {
          const connection = settings.bot.connections.find((c) => c.id === id);
          const occurredAt = new Date().toISOString();
          return connection
            ? { id, label: connection.label, status: connection.enabled ? "ok" : "disabled", message: connection.enabled ? "连接配置已保存。" : "连接已保存但未启用。", messageId: "", phase: "config", code: connection.enabled ? "config_ok" : "connection_disabled", reportKind: "", reportDetail: "", occurredAt }
            : { id, label: "", status: "missing", message: "未找到连接。", messageId: "", phase: "config", code: "connection_missing", reportKind: "bot", reportDetail: JSON.stringify({ schemaVersion: 2, kind: "bot", source: "bot.runtime", label: "bot.mock.config", message: "mock missing bot connection", errorType: "BotConnectionDiagnostic", errorMessage: "bot connection record was not found", topFrame: "bot.config", occurredAt }), occurredAt };
        },
        async TestBotConnection(id: string, target?: string) {
          const diag = await this.DiagnoseBotConnection(id);
          if (target?.trim()) return { ...diag, message: `Mock test sent to ${target.trim()}`, messageId: "mock-message-id" };
          return diag;
        },
        async TestDingtalkBot() {
          const occurredAt = new Date().toISOString();
          return { id: "dingtalk", label: "DingTalk", status: "ok", message: "Mock dingtalk test sent", messageId: "mock-dingtalk-id", phase: "send", code: "dingtalk_test_send_ok", reportKind: "", reportDetail: "", occurredAt };
        },
        ...createDesktopPreferencesMock(settings),
        async SetDesktopAppearance(theme: string, style: string) {
          settings.desktopTheme = theme === "auto" || theme === "light" ? theme : "dark";
          settings.desktopThemeStyle = style;
          mockThemeMode = settings.desktopTheme as "auto" | "light" | "dark";
          if (["graphite","aurora","slate","carbon","nocturne","amber"].includes(style)) {
            mockBaseStyle = style;
          }
        },
        async SetDesktopTerminalTheme(theme: string) {
          settings.desktopTerminalTheme = theme === "dark" || theme === "light" ? theme : "auto";
        },
        async ListThemePacks() {
          const baseActive = !mockActiveThemeId;
          return mockThemePacks.map((p) => {
            const kind = p.kind || (p.builtin ? "base" : "user");
            let active = false;
            if (kind === "base") active = baseActive && p.id === mockBaseStyle;
            else active = p.id === mockActiveThemeId;
            return { ...p, active, tokens: { light: { ...(p.tokens.light || {}) }, dark: { ...(p.tokens.dark || {}) } }, recipes: { ...p.recipes } };
          });
        },
        async GetActiveThemePack() {
          // Base style ids are never active packs in the redesigned model.
          const pack = mockActiveThemeId && !["graphite","aurora","slate","carbon","nocturne","amber"].includes(mockActiveThemeId)
            ? mockThemePacks.find((p) => p.id === mockActiveThemeId)
            : null;
          return { activeThemeId: pack ? mockActiveThemeId : "", pack: pack ? { ...pack, active: true } : null };
        },
        async GetThemeExperience() {
          const pack = mockActiveThemeId && !["graphite","aurora","slate","carbon","nocturne","amber"].includes(mockActiveThemeId)
            ? mockThemePacks.find((p) => p.id === mockActiveThemeId)
            : null;
          return {
            themeMode: mockThemeMode,
            baseStyle: mockBaseStyle,
            effectiveStyle: pack?.baseStyle || mockBaseStyle,
            activeThemeId: pack ? mockActiveThemeId : "",
            activePack: pack ? { ...pack, active: true } : null,
          };
        },
        async ActivateThemePack(id: string) {
          const next = String(id || "").trim();
          if (["graphite","aurora","slate","carbon","nocturne","amber"].includes(next)) {
            throw new Error(`base style ${next} is not a theme pack; use ActivateBaseStyle`);
          }
          mockActiveThemeId = next;
        },
        async ActivateBaseStyle(style: string) {
          const s = String(style || "").trim().toLowerCase();
          if (!["graphite","aurora","slate","carbon","nocturne","amber"].includes(s)) {
            throw new Error(`unknown base style ${s}`);
          }
          mockBaseStyle = s;
          mockActiveThemeId = "";
        },
        async DisableThemePack() {
          mockActiveThemeId = "";
        },
        async RestoreGraphiteAppearance() {
          mockBaseStyle = "graphite";
          mockActiveThemeId = "";
        },
        async ResetThemePack() {
          mockActiveThemeId = "";
        },
        async SaveThemePack(input: import("./themePack").ThemeSaveInput) {
          const pack: import("./themePack").ThemePackView = {
            id: input.id,
            name: input.name,
            author: input.author,
            description: input.description,
            license: input.license,
            baseStyle: input.baseStyle,
            builtin: false,
            kind: "user",
            active: Boolean(input.activate),
            hasBackground: Boolean(
              (input.background && (input.backgroundDataUrl || input.background.image)) ||
              (input.taskBackground && (input.taskBackgroundDataUrl || input.taskBackground.image)),
            ),
            backgroundUrl: input.backgroundDataUrl || "",
            taskBackgroundUrl: input.taskBackgroundDataUrl || "",
            tokens: input.tokens || {},
            recipes: input.recipes || { density: "comfortable", corners: "soft" },
            background: input.background ?? undefined,
            taskBackground: input.taskBackground ?? undefined,
          };
          const idx = mockThemePacks.findIndex((p) => p.id === pack.id);
          if (idx >= 0) mockThemePacks[idx] = pack;
          else mockThemePacks.push(pack);
          if (input.activate) mockActiveThemeId = pack.id;
          return pack;
        },
        async DeleteThemePack(id: string) {
          mockThemePacks = mockThemePacks.filter((p) => p.id !== id || p.builtin);
          if (mockActiveThemeId === id) mockActiveThemeId = "";
        },
        async CopyThemePack(sourceID: string, newID: string, newName: string) {
          const src = mockThemePacks.find((p) => p.id === sourceID);
          if (!src) throw new Error("source theme not found");
          const pack: import("./themePack").ThemePackView = {
            ...src,
            id: newID,
            name: newName || `${src.name} Copy`,
            builtin: false,
            kind: "user",
            nameKey: undefined,
            descriptionKey: undefined,
            active: false,
          };
          mockThemePacks.push(pack);
          return pack;
        },
        async ImportThemePack(_sourcePath: string, replace: boolean) {
          if (replace) {
            return { pack: mockThemePacks[0], replaced: true };
          }
          // Simulate conflict path without re-prompting for a file on confirm.
          return { pack: mockThemePacks[0], replaced: false, needsReplace: true, pendingId: "pending-mock" };
        },
        async ExportThemePack(_id: string, _destPath: string) {
          return "";
        },
        async PickThemeBackground() {
          return "";
        },
        async SetDesktopLayoutStyle(style: string) {
          settings.desktopLayoutStyle = style === "workbench" || style === "creation" ? style : "classic";
        },
        async SetDesktopZoomFactor(factor: number) {
          mockDesktopZoomFactor = Math.min(2.0, Math.max(0.5, Number.isFinite(factor) ? factor : 1.0));
        },
        async GetDesktopZoomFactor() {
          return mockDesktopZoomFactor;
        },
        async RestartApplication() {
          // no-op in mock
        },
        async ReportDesktopWebViewReady() {
          // no-op in mock
        },
        async GetDesktopShellStatus() {
          return { trayState: "ready", backgroundCloseAvailable: true } as DesktopShellStatusView;
        },
    async SetAgentParams(temperature: number, maxSteps: number, plannerMaxSteps: number, systemPrompt: string) {
      settings.agent = { ...settings.agent, temperature, maxSteps, plannerMaxSteps, systemPrompt };
    },
    async SetCompactRatio(ratio: number) {
      if (!Number.isFinite(ratio)
        || ratio < COMPACT_RATIO_MIN_PERCENT / 100
        || ratio > COMPACT_RATIO_MAX_PERCENT / 100) {
        throw new Error(`compact ratio must be between ${COMPACT_RATIO_MIN_PERCENT / 100} and ${COMPACT_RATIO_MAX_PERCENT / 100}`);
      }
      settings.agent = { ...settings.agent, compactRatio: ratio };
    },
    async SetReasoningLanguage(lang: string) {
      const normalized = lang === "zh" || lang === "en" ? lang : "auto";
      settings.agent = { ...settings.agent, reasoningLanguage: normalized };
    },
    // ── Heartbeat mock ──
    async HeartbeatListTasks() { return mockHeartbeatTasks; },
    async HeartbeatReloadTasks() { return mockHeartbeatTasks; },
    async HeartbeatSaveTasks(tasks: unknown) { mockHeartbeatTasks = Array.isArray(tasks) ? tasks : []; },
    async HeartbeatReloadConfig() { return { revision: mockHeartbeatRevision, etag: `mock-${mockHeartbeatRevision}`, tasks: mockHeartbeatTasks }; },
    async HeartbeatSaveConfig(update: unknown) {
      const tasks = (update as { tasks?: unknown[] } | null)?.tasks;
      mockHeartbeatTasks = Array.isArray(tasks) ? tasks : [];
      mockHeartbeatRevision += 1;
      return { revision: mockHeartbeatRevision, etag: `mock-${mockHeartbeatRevision}`, tasks: mockHeartbeatTasks };
    },
    async HeartbeatTriggerNow(_id: string) {},
    async HeartbeatGenerateID() { return "mock-" + Date.now().toString(36); },
    async ListTasks() { return []; },
    async CurrentTaskSessionID() { return ""; },
    async ListTasksForSession() { return []; },
    async GetTask() { return null; },
    async ListTaskEvents() { return []; },
    async StopTask() { return { schema_version: 1, command: "stop", task_id: "", accepted: false, idempotent: false, error: { code: "mock", message: "not available in browser mock" } }; },
    async CancelTask() { return { schema_version: 1, command: "cancel", task_id: "", accepted: false, idempotent: false, error: { code: "mock", message: "not available in browser mock" } }; },
    async RequeueTask() { return { schema_version: 1, command: "requeue", task_id: "", accepted: false, idempotent: false, error: { code: "mock", message: "not available in browser mock" } }; },
    async OpenTaskSession() { return { schema_version: 1, command: "open_session", task_id: "", accepted: false, idempotent: false, error: { code: "mock", message: "not available in browser mock" } }; },
    async ListTasksForTab() { return []; },
    ...makeMockTaskCatalogBindings(),
    async ListTaskEventsForTab() { return []; },
    async StopTaskForTab() { return { schema_version: 1, command: "stop", task_id: "", accepted: false, idempotent: false, error: { code: "mock", message: "not available in browser mock" } }; },
    async CancelTaskForTab() { return { schema_version: 1, command: "cancel", task_id: "", accepted: false, idempotent: false, error: { code: "mock", message: "not available in browser mock" } }; },
    async RequeueTaskForTab() { return { schema_version: 1, command: "requeue", task_id: "", accepted: false, idempotent: false, error: { code: "mock", message: "not available in browser mock" } }; },
    async OpenTaskSessionForTab() { return { schema_version: 1, command: "open_session", task_id: "", accepted: false, idempotent: false, error: { code: "mock", message: "not available in browser mock" } }; },
    async SetTrayLocale(_locale: "en" | "zh" | "zh-TW" | "es") {},
    async SetAutoApproveTools(on: boolean) {
      await this.SetToolApprovalMode(on ? "yolo" : "ask");
    },
    async SetBypass(on: boolean) {
      await this.SetAutoApproveTools(on);
    },
    async Version() {
      return "v1.0.0 (browser dev)";
    },
    async CheckUpdate(channel: string) {
      void channel;
      // Keep the default browser preview focused on the primary product surface.
      // Updater methods remain mocked for explicit updater-flow tests.
      return {
        available: false,
        current: "v1.0.0",
        latest: "v1.0.0",
        notes: "",
        channel: "stable",
        canSelfUpdate: false,
        manualOnly: true,
        installMode: "manual",
        manualReason: "browser preview",
        downloaded: false,
        downloadUrl: "",
        assetSize: 0,
      };
    },
    async ApplyUpdateRequest(channel: string, expectedVersion: string, requestId: string) {
      void channel;
      const selectedChannel = "stable";
      const total = 12_345_678;
      for (let r = 0; r <= total; r += 1_800_000) {
        emitUpdater({ requestId, version: expectedVersion, channel: selectedChannel, phase: "downloading", received: Math.min(r, total), total });
        await delay(120);
      }
      emitUpdater({ requestId, version: expectedVersion, channel: selectedChannel, phase: "verifying", received: total, total });
      await delay(300);
      emitUpdater({ requestId, version: expectedVersion, channel: selectedChannel, phase: "installing", received: total, total });
      await delay(300);
      emitUpdater({ requestId, version: expectedVersion, channel: selectedChannel, phase: "relaunching", received: 0, total: 0 });
    },
    async AbandonPendingUpdate() {},
    async OpenDownloadPage() {
      if (typeof window !== "undefined") {
        window.open("https://reasonix.io/?download=desktop#start", "_blank", "noopener");
      }
    },
    async OpenUserConfigPath() {},
    async ReloadUserConfig() {
      return { configWarnings: [], configWarningsRevision: 0, configPath: "" };
    },
    // Dev seam: match the backend's provider-agnostic onboarding predicate.
    async NeedsOnboarding() {
      return !settings.providers.some((p) => p.models.length > 0 && providerIsConfigured(p));
    },
    async ConnectKey(apiKey: string) {
      if (!apiKey.trim()) throw new Error("key is required");
      // Match the production onboarding path: saving a DeepSeek key also
      // restores the current official provider template instead of merely
      // marking a possibly stale legacy entry as configured.
      await this.AddOfficialProviderAccess("deepseek", apiKey);
      await delay(300);
      return "";
    },
    async ReportCrash() { await delay(300); },
    async RecordUIPerf() {},
    // Tab management mocks.
    async ListTabs() {
      return mockTabs.map((tab) => ({ ...tab }));
    },
    async OpenProjectTab(workspaceRoot: string, _topicID: string) {
      const existing = mockTabs.find((tab) => tab.scope === "project" && tab.workspaceRoot === workspaceRoot && tab.topicId === _topicID);
      if (existing) {
        const active = { ...existing, active: true, running: mockTopicRunsInScenario(_topicID) };
        mockTabs = mockTabs.map((tab) => (tab.id === existing.id ? active : { ...tab, active: false }));
        return { ...active };
      }
      const pruned = restoreMockPrunedTab("project", workspaceRoot, _topicID);
      if (pruned) {
        const restored = { ...pruned, active: true, running: mockTopicRunsInScenario(_topicID) };
        mockTabs = [...mockTabs.map((item) => ({ ...item, active: false })), restored];
        return { ...restored };
      }
      const defaultToolApprovalMode = normalizeToolApprovalMode(settings.defaultToolApprovalMode);
      const tab: TabMeta = {
        id: "tab_" + Date.now(),
        scope: "project",
        workspaceRoot,
        workspaceName: workspaceRoot.split("/").filter(Boolean).pop() ?? workspaceRoot,
        workspacePath: workspaceRoot,
        gitBranch: "main",
        topicId: _topicID,
        topicTitle: topicLabel(_topicID, t("mock.newSession")),
        sessionPath: `/mock/sessions/${_topicID}.jsonl`,
        projectColor: mockProjectTree.find((node) => node.root === workspaceRoot)?.projectColor,
        label: mockModelLabel(settings.defaultModel),
        ready: true,
        running: mockTopicRunsInScenario(_topicID),
        mode: modeWithAutoApproveTools("normal", defaultToolApprovalMode === "yolo"),
        collaborationMode: "normal",
        toolApprovalMode: defaultToolApprovalMode,
        tokenMode: "full",
        active: true,
        cwd: workspaceRoot,
      };
      mockTabs = [...mockTabs.map((item) => ({ ...item, active: false })), tab];
      return { ...tab };
    },
    async IsolatedWorktreeAvailability(workspaceRoot: string) {
      return workspaceRoot
        ? { available: true, repoRoot: workspaceRoot, branch: "main", sourceDirty: false }
        : { available: false, reason: "project folder is required" };
    },
    async DeliveryWorktreeAvailability(workspaceRoot: string) {
      return this.IsolatedWorktreeAvailability(workspaceRoot);
    },
    ...makeMockQualityFloorBindings(() => mockTabs, (next) => { mockTabs = next; }),
    async SetAgentPreset(_preset: string) {},
    async SetAgentPresetForTab(_tabID: string, _preset: string) {},
    async SetTokenMode(_mode: string) {},
    async SetTokenModeForTab(_tabID: string, _mode: string) {},
    async CreateIsolatedWorktree(workspaceRoot: string) {
      if (!workspaceRoot) throw new Error("project folder is required");
      const suffix = Date.now().toString(36);
      const isolatedRoot = `/mock/reasonix-worktrees/${suffix}/${workspaceRoot.split("/").filter(Boolean).pop() ?? "project"}`;
      const topicID = `topic_worktree_${suffix}`;
      const tab = await this.OpenProjectTab(isolatedRoot, topicID);
      tab.isolatedWorktree = true;
      tab.gitBranch = `reasonix/isolated-${suffix}`;
      mockTabs = mockTabs.map((candidate) => candidate.id === tab.id ? { ...tab } : candidate);
      return {
        workspaceRoot: isolatedRoot,
        worktreeRoot: isolatedRoot,
        sourceRoot: workspaceRoot,
        branch: tab.gitBranch,
        sourceDirty: false,
        tab,
      };
    },
    async CreateDeliveryWorktree(workspaceRoot: string) {
      return this.CreateIsolatedWorktree(workspaceRoot);
    },
    ...makeMockWorktreeMergeBindings(() => mockTabs, (next) => { mockTabs = next; }),
    async OpenGlobalTab(_topicID: string) {
      const existing = mockTabs.find((tab) => tab.scope === "global" && tab.topicId === _topicID);
      if (existing) {
        setMockActiveTab(existing.id);
        return { ...existing, active: true };
      }
      const defaultToolApprovalMode = normalizeToolApprovalMode(settings.defaultToolApprovalMode);
      const tab: TabMeta = {
        id: "tab_" + Date.now(),
        scope: "global",
        workspaceRoot: "",
        workspaceName: "Global",
        workspacePath: cwd,
        topicId: _topicID,
        topicTitle: topicLabel(_topicID, "Global"),
        sessionPath: `/mock/sessions/${_topicID}.jsonl`,
        label: mockModelLabel(settings.defaultModel),
        ready: true,
        running: false,
        mode: modeWithAutoApproveTools("normal", defaultToolApprovalMode === "yolo"),
        collaborationMode: "normal",
        toolApprovalMode: defaultToolApprovalMode,
        tokenMode: "full",
        active: true,
        cwd: "",
      };
      mockTabs = [...mockTabs.map((item) => ({ ...item, active: false })), tab];
      return { ...tab };
    },
    async OpenTopicSession(scope: string, workspaceRoot: string, topicID: string, sessionPath: string) {
      const tab = scope === "project"
        ? await this.OpenProjectTab(workspaceRoot, topicID)
        : await this.OpenGlobalTab(topicID);
      const active = { ...tab, sessionPath };
      mockTabs = mockTabs.map((item) => (item.id === tab.id ? active : item));
      return { ...active };
    },
    async EnsureBlankTab(scope: string, workspaceRoot: string) {
      const targetScope = scope === "project" && workspaceRoot ? "project" : "global";
      const targetRoot = targetScope === "project" ? workspaceRoot : "";
      const existing = mockTabs.find((tab) =>
        tab.scope === targetScope &&
        (targetScope === "global" || tab.workspaceRoot === targetRoot) &&
        !tab.running &&
        mockTopicIsBlank(tab.topicId)
      );
      if (existing) {
        setMockActiveTab(existing.id);
        return { ...existing, active: true };
      }
      const topic = await this.CreateTopic(targetScope, targetRoot, "");
      return targetScope === "global" ? this.OpenGlobalTab(topic.id) : this.OpenProjectTab(targetRoot, topic.id);
    },
    async ActivateTopic(scope: string, workspaceRoot: string, topicID: string, sessionPath: string) {
      const tab = sessionPath
        ? await this.OpenTopicSession(scope, workspaceRoot, topicID, sessionPath)
        : scope === "project"
          ? await this.OpenProjectTab(workspaceRoot, topicID)
          : await this.OpenGlobalTab(topicID);
      pruneMockTabsTo(tab.id);
      return { ...mockTabs[0] };
    },
    async StartTopicActivation(req: TopicActivationRequest): Promise<TopicActivationTicket> {
      // Mirror the real two-phase contract: the surface switches synchronously
      // (same open + single-tab prune as ActivateTopic), the terminal event
      // lands asynchronously on the mock "topic:activation" listeners, and a
      // superseded pending activation is cancelled at supersede time.
      const tab = req.sessionPath
        ? await this.OpenTopicSession(req.scope, req.workspaceRoot, req.topicId, req.sessionPath)
        : req.scope === "project"
          ? await this.OpenProjectTab(req.workspaceRoot, req.topicId)
          : await this.OpenGlobalTab(req.topicId);
      pruneMockTabsTo(tab.id);
      const requestId = req.requestId?.trim() || `mock-act-${++mockTopicActivationCounter}`;
      const previous = mockPendingTopicActivation;
      mockPendingTopicActivation = { requestId, tabId: tab.id };
      if (previous && previous.requestId !== requestId) {
        __emitMockTopicActivation({ requestId: previous.requestId, tabId: previous.tabId, phase: "cancelled" });
      }
      __emitMockTopicActivation({ requestId, tabId: tab.id, phase: "starting" });
      const meta = { ...mockTabs[0] };
      window.setTimeout(() => {
        if (mockPendingTopicActivation?.requestId !== requestId) return;
        mockPendingTopicActivation = undefined;
        __emitMockTopicActivation({ requestId, tabId: tab.id, phase: "ready" });
      }, 0);
      return { requestId, tabId: tab.id, meta };
    },
    async EnsureBlankSurface(scope: string, workspaceRoot: string) {
      const tab = await this.EnsureBlankTab(scope, workspaceRoot);
      pruneMockTabsTo(tab.id);
      return { ...mockTabs[0] };
    },
    async SetActiveTab(_tabID: string) {
      const tab = mockTabs.find((item) => item.id === _tabID);
      if (!tab) throw new Error(`tab ${_tabID} not found`);
      setMockActiveTab(_tabID);
      if (!tab.remote) queueMockTopicRuntime(tab);
    },
    async ReorderTabs(_tabIDs: string[]) {
      const byId = new Map(mockTabs.map((tab) => [tab.id, tab]));
      const ordered = _tabIDs.map((id) => byId.get(id)).filter((tab): tab is TabMeta => Boolean(tab));
      if (ordered.length === mockTabs.length) mockTabs = ordered;
    },
    async CloseTab(_tabID: string) {
      if (mockTabs.length <= 1) return;
      const terminalIDs = mockTerminalSessions
        .filter((session) => mockTerminalTabIDs.get(session.id) === _tabID)
        .map((session) => session.id);
      mockTerminalSessions = mockTerminalSessions.filter((session) => !terminalIDs.includes(session.id));
      terminalIDs.forEach((id) => {
        mockTerminalOutput.delete(id);
        mockTerminalTabIDs.delete(id);
        __emitMockTerminalExit({ id, exitCode: 0, removed: true });
      });
      const wasActive = mockTabs.some((tab) => tab.id === _tabID && tab.active);
      mockTabs = mockTabs.filter((tab) => tab.id !== _tabID);
      if (wasActive && mockTabs.length > 0 && !mockTabs.some((tab) => tab.active)) {
        mockTabs[mockTabs.length - 1] = { ...mockTabs[mockTabs.length - 1], active: true };
      }
    },
    async TerminalWorkspaceForTab(tabID: string) {
      const tab = mockTabs.find((candidate) => candidate.id === tabID) ?? mockTabs.find((candidate) => candidate.active);
      const sessions = tab ? mockTerminalSessions.filter((session) => mockTerminalTabIDs.get(session.id) === tab.id) : [];
      return {
        available: true,
        readOnly: Boolean(tab?.readOnly),
        sessions: sessions.map((session) => ({ ...session })),
        shells: [
          { id: "default", label: "Default shell" },
          { id: "bash", label: "bash" },
          { id: "zsh", label: "zsh" },
        ],
      };
    },
    async TerminalOutputForTab(tabID: string, sessionID: string) {
      if (mockTerminalTabIDs.get(sessionID) !== tabID) return "";
      return mockTerminalOutput.get(sessionID) ?? "";
    },
    async CreateTerminalForTab(tabID: string, relativePath: string, shellID: string) {
      const tab = mockTabs.find((candidate) => candidate.id === tabID) ?? mockTabs.find((candidate) => candidate.active);
      if (tab?.readOnly) throw new Error("channel session is read-only");
      const id = `term-mock-${Date.now()}-${Math.random().toString(16).slice(2)}`;
      const session: TerminalSessionView = {
        id,
        title: shellID || "Default shell",
        shell: shellID || "default",
        cwd: `${tab?.cwd || cwd}/${relativePath || "."}`.replace(/\/\.\/?$/, ""),
        createdAt: Date.now(),
        running: true,
      };
      mockTerminalSessions = [...mockTerminalSessions, session];
      mockTerminalOutput.set(id, "Reasonix terminal ready\r\n");
      mockTerminalTabIDs.set(id, tabID);
      window.setTimeout(() => __emitMockTerminalOutput({ id, data: mockTerminalBytes("Reasonix terminal ready\r\n") }), 0);
      return { ...session };
    },
    async WriteTerminalForTab(_tabID: string, sessionID: string, data: string) {
      const session = mockTerminalSessions.find((candidate) => candidate.id === sessionID);
      if (!session?.running) throw new Error("terminal session has exited");
      mockTerminalOutput.set(sessionID, `${mockTerminalOutput.get(sessionID) ?? ""}${data}`);
      window.setTimeout(() => __emitMockTerminalOutput({ id: sessionID, data: mockTerminalBytes(data) }), 0);
    },
    async ResizeTerminalForTab() {},
    async CloseTerminalForTab(_tabID: string, sessionID: string) {
      mockTerminalSessions = mockTerminalSessions.filter((session) => session.id !== sessionID);
      mockTerminalOutput.delete(sessionID);
      mockTerminalTabIDs.delete(sessionID);
      __emitMockTerminalExit({ id: sessionID, exitCode: 0, removed: true });
    },
    async RenameTerminalForTab(_tabID: string, sessionID: string, title: string) {
      mockTerminalSessions = mockTerminalSessions.map((session) => session.id === sessionID ? { ...session, title } : session);
    },
    async ListProjectTree() {
      return cloneProjectTree();
    },
    async RenameProject(workspaceRoot: string, title: string) {
      const node = workspaceRoot
        ? mockProjectTree.find((item) => item.root === workspaceRoot)
        : mockProjectTree.find((item) => item.kind === "global_folder");
      if (node) node.label = title.trim() || (node.kind === "global_folder" ? "Global" : node.label);
    },
    async SetProjectColor(workspaceRoot: string, color: string) {
      const node = workspaceRoot
        ? mockProjectTree.find((item) => item.root === workspaceRoot)
        : mockProjectTree.find((item) => item.kind === "global_folder");
      if (!node) return;
      node.projectColor = color || undefined;
      for (const child of projectChildren(node)) child.projectColor = node.projectColor;
      mockTabs = mockTabs.map((tab) =>
        (workspaceRoot ? tab.workspaceRoot === workspaceRoot : tab.scope === "global")
          ? { ...tab, projectColor: node.projectColor }
        : tab,
      );
    },
    async SetProjectPinned(workspaceRoot: string, pinned: boolean) {
      setMockProjectPinned(workspaceRoot, pinned);
    },
    async ReorderProjects(workspaceRoots: string[]) {
      const projects = mockProjectTree.filter((node) => node.kind === "project");
      const globals = mockProjectTree.filter((node) => node.kind === "global_folder");
      if (!workspaceRoots.includes(GLOBAL_PROJECT_ORDER_KEY)) {
        if (workspaceRoots.length !== projects.length) return;
        const byRoot = new Map(projects.map((node) => [node.root, node]));
        const ordered = workspaceRoots.map((root) => byRoot.get(root)).filter((node): node is ProjectNode => Boolean(node));
        if (ordered.length !== projects.length) return;
        mockProjectTree.splice(0, mockProjectTree.length, ...globals, ...ordered);
        return;
      }
      const byKey = new Map<string, ProjectNode>();
      for (const node of projects) {
        if (node.root) byKey.set(node.root, node);
      }
      for (const node of globals) byKey.set(GLOBAL_PROJECT_ORDER_KEY, node);
      const seen = new Set<string>();
      const ordered: ProjectNode[] = [];
      for (const key of workspaceRoots) {
        if (seen.has(key)) return;
        const node = byKey.get(key);
        if (!node) return;
        seen.add(key);
        ordered.push(node);
      }
      if (ordered.length !== projects.length + globals.length) return;
      mockProjectTree.splice(0, mockProjectTree.length, ...ordered);
    },
    async CreateTopic(_scope: string, _workspaceRoot: string, title: string) {
      const now = Date.now();
      const id = "topic_" + now;
      const topicTitle = title.trim() || t("mock.newSession");
      const parent = _scope === "global"
        ? ensureMockGlobalFolder()
        : mockProjectTree.find((node) => node.root === _workspaceRoot);
      if (parent) {
        const global = parent.kind === "global_folder";
        parent.children = [{
          key: parent.kind === "global_folder" ? "global_topic_" + id : "topic_" + id,
          kind: global ? "global_topic" : "topic",
          label: topicTitle,
          root: parent.root,
          topicId: id,
          projectColor: parent.projectColor,
          createdAt: now,
        }, ...projectChildren(parent)];
      }
      return { id, title: topicTitle, createdAt: now };
    },
    async RenameTopic(topicID: string, title: string) {
      const topic = findMockTopic(topicID);
      const nextTitle = title.trim();
      if (!topic || !nextTitle) return;
      const activePrefix = topic.label?.startsWith("● ") ? "● " : "";
      topic.label = `${activePrefix}${nextTitle}`;
      mockTabs = mockTabs.map((tab) =>
        tab.topicId === topicID ? { ...tab, topicTitle: nextTitle } : tab,
      );
    },
    async AIRenameSession(topicID: string) { return mockAIRenameSession(findMockTopic(topicID)); },
    async DeleteTopic(topicID: string) {
      deleteMockTopic(topicID);
    },
    async TrashTopic(topicID: string) {
      deleteMockTopic(topicID);
    },
    async SetTopicPinned(topicID: string, pinned: boolean) {
      setMockTopicPinned(topicID, pinned);
    },
    ...makeMockProjectTreeOrganizationBindings(mockProjectTree),
    async SaveWindowState(_state) {
      // no-op in browser dev — no real window geometry to persist
    },
    async ContextPanel(_tabID: string) {
      const now = Date.now();
      const currency = "¥";
      const cost = (usd: number) => currency === "¥" ? Number((usd * 7.15).toFixed(4)) : usd;
      return {
        usedTokens: 42124,
        windowTokens: 128000,
        promptTokens: 22134,
        completionTokens: 12345,
        totalTokens: 34479,
        reasoningTokens: 7521,
        cacheHitTokens: 87000,
        cacheMissTokens: 13000,
        sessionCacheHitTokens: 87000,
        sessionCacheMissTokens: 13000,
        sessionCompletionTokens: 12345,
        requestCount: 10,
        elapsedMs: 33 * 60 * 1000,
        sessionCost: cost(0.018),
        sessionCurrency: currency,
        sessionCostUsd: cost(0.018),
        sessionCostQuote: {
          original: { amount: String(cost(0.018)), currency: "CNY" },
          selected: { amount: String(cost(0.018)), currency: "CNY" },
          estimated: true,
          costComplete: true,
          displayComplete: true,
          complete: true,
          displayStatus: "matched",
          aggregateMode: "single_currency",
          rateBand: "mixed",
        },
        sources: {
          executor: {
            promptTokens: 24100,
            completionTokens: 8300,
            totalTokens: 32400,
            reasoningTokens: 5200,
            cacheHitTokens: 76000,
            cacheMissTokens: 9000,
            requestCount: 4,
            sessionCost: cost(0.0124),
            sessionCurrency: currency,
            sessionCostUsd: cost(0.0124),
          },
          planner: {
            promptTokens: 1800,
            completionTokens: 600,
            totalTokens: 2400,
            reasoningTokens: 420,
            cacheHitTokens: 3400,
            cacheMissTokens: 700,
            requestCount: 1,
            sessionCost: cost(0.0011),
            sessionCurrency: currency,
            sessionCostUsd: cost(0.0011),
          },
          subagent: {
            promptTokens: 4200,
            completionTokens: 2100,
            totalTokens: 6300,
            reasoningTokens: 1500,
            cacheHitTokens: 6100,
            cacheMissTokens: 2100,
            requestCount: 2,
            sessionCost: cost(0.0032),
            sessionCurrency: currency,
            sessionCostUsd: cost(0.0032),
          },
          compaction: {
            promptTokens: 2600,
            completionTokens: 700,
            totalTokens: 3300,
            reasoningTokens: 260,
            cacheHitTokens: 1100,
            cacheMissTokens: 900,
            requestCount: 1,
            sessionCost: cost(0.0009),
            sessionCurrency: currency,
            sessionCostUsd: cost(0.0009),
          },
          classifier: {
            promptTokens: 900,
            completionTokens: 120,
            totalTokens: 1020,
            reasoningTokens: 70,
            cacheHitTokens: 300,
            cacheMissTokens: 250,
            requestCount: 1,
            sessionCost: cost(0.0003),
            sessionCurrency: currency,
            sessionCostUsd: cost(0.0003),
          },
          title: {
            promptTokens: 420,
            completionTokens: 80,
            totalTokens: 500,
            reasoningTokens: 20,
            cacheHitTokens: 100,
            cacheMissTokens: 50,
            requestCount: 1,
            sessionCost: cost(0.0001),
            sessionCurrency: currency,
            sessionCostUsd: cost(0.0001),
          },
        },
        mock: true,
        readFiles: [
          { path: "README.md", turn: 2, time: now - 34 * 60 * 1000 },
          { path: "go.mod", turn: 3, time: now - 30 * 60 * 1000 },
          { path: "desktop/file.go", turn: 5, time: now - 13 * 60 * 1000, offset: 0, limit: 180 },
          { path: "internal/event.go", turn: 6, time: now - 4 * 60 * 1000, offset: 120, limit: 80, truncated: true },
        ],
        changedFiles: [
          { path: t("mock.changedFile1Path"), sources: ["session"], gitStatus: "modified", turns: [5, 6], latestPrompt: t("mock.changedFile1Prompt"), latestTime: now - 2 * 60 * 1000 },
          { path: t("mock.changedFile2Path"), sources: ["session"], gitStatus: "added", turns: [6], latestPrompt: t("mock.changedFile2Prompt"), latestTime: now - 60 * 1000 },
        ],
      };
    },
    // ── Remote (SSH) mock ──
    async RemoteHosts() {
      return mockRemoteHosts.slice();
    },
    async AddRemoteHost(input) {
      const view = mockRemoteHostView(input.label, input);
      mockRemoteHosts = [...mockRemoteHosts.filter((h) => h.id !== view.id), view];
      return view;
    },
    async UpdateRemoteHost(id, input) {
      const previous = mockRemoteHosts.find((h) => h.id === id);
      const view = mockRemoteHostView(id, input, previous);
      mockRemoteHosts = mockRemoteHosts.map((h) => (h.id === id ? view : h));
      return view;
    },
    async RemoveRemoteHost(id) {
      mockRemoteHosts = mockRemoteHosts.filter((h) => h.id !== id);
      delete mockRemoteConn[id];
    },
    async ScanSSHConfig() {
      return [
        { label: "gpu-box", host: "gpu-box", port: 0, user: "", identityFile: "", proxyJump: "", defaultWorkspace: "", serveInstall: "auto", credentialMode: "remote", useSSHConfig: true, preserveExistingSettings: true },
      ];
    },
    async ConnectRemoteHost(id) {
      mockRemoteConn[id] = "connecting";
      __emitMockRemote("status", { hostId: id, state: "connecting" });
      setTimeout(() => {
        mockRemoteConn[id] = "connected";
        __emitMockRemote("status", { hostId: id, state: "connected" });
      }, 300);
    },
    async DisconnectRemoteHost(id) {
      mockRemoteConn[id] = "stopped";
      __emitMockRemote("status", { hostId: id, state: "stopped" });
    },
    async RemoteConnectionStatuses() {
      return Object.entries(mockRemoteConn).map(([hostId, state]) => ({ hostId, state: state as RemoteConnectionStatus["state"] }));
    },
    async ConfirmRemoteHostKey(hostId, accept) {
      mockRemoteConn[hostId] = accept ? "connected" : "stopped";
      __emitMockRemote("status", { hostId, state: mockRemoteConn[hostId] });
    },
    async ConfirmRemoteSecret(hostId, _promptId, _secret, accept) {
      mockRemoteConn[hostId] = accept ? "connected" : "stopped";
      __emitMockRemote("status", { hostId, state: mockRemoteConn[hostId] });
    },
    async ListRemoteDir(_hostId, path) {
      const base = path.replace(/\/$/, "");
      return [
        { name: "src", path: `${base}/src`, isDir: true, size: 0, mtimeUnix: 1_700_000_000, symlink: false },
        { name: "README.md", path: `${base}/README.md`, isDir: false, size: 1024, mtimeUnix: 1_700_000_500, symlink: false },
      ];
    },
    async ReadRemoteFile(_hostId, path) {
      return { path, body: `# Mock remote file\n${path}\n`, size: 40, mtimeUnix: 1_700_000_500, truncated: false, binary: false };
    },
    async WriteRemoteFile(_hostId, _path, _body, _expectMtimeUnix) {
      return { ok: true, conflict: false, newMtimeUnix: 1_700_000_900 };
    },
    async MkdirRemote() {},
    async RenameRemotePath() {},
    async DeleteRemotePath() {},
    async RemoteForwards(hostId) {
      return mockRemoteForwards[hostId] ?? [];
    },
    async AddRemoteForward(hostId, input) {
      const view: RemoteForwardView = { id: `L:${input.localPort}`, hostId, ...input, state: "active" };
      mockRemoteForwards[hostId] = [...(mockRemoteForwards[hostId] ?? []), view];
      __emitMockRemote("forwards", { hostId, forwards: mockRemoteForwards[hostId] });
      return view;
    },
    async RemoveRemoteForward(hostId, forwardId) {
      mockRemoteForwards[hostId] = (mockRemoteForwards[hostId] ?? []).filter((f) => f.id !== forwardId);
      __emitMockRemote("forwards", { hostId, forwards: mockRemoteForwards[hostId] });
    },
    async OpenRemoteWorkspace() {},
    async PickRemoteIdentityFile() { return "~/.ssh/id_ed25519"; },
    async CheckRemotePlatform() {},
    async StopRemoteServer(hostId, workspace) {
      __emitMockRemote("server", { hostId, workspace, state: "stopped" });
    },
    async RemoteServerStatus(hostId, workspace) {
      return { hostId, workspace, state: "stopped" };
    },
    async RemoteServerLogs() {
      return "mock serve log line 1\nmock serve log line 2\n";
    },
    async RemoteLastWorkspace() {
      return "~/app";
    },
    async ScanRemoteLegacyWorkbenchData() {
      return { mirrorCount: 0, mirrorBytes: 0, trustFile: false };
    },
    async ExtensionActions() {
      return [];
    },
    async InvokeExtensionAction() {
      return "";
    },
    async SubmitExtensionForm() {},
    ...remoteProjects.bindings,
    async CleanRemoteLegacyWorkbenchData() {},
  };
}
let mockRemoteHosts: RemoteHostView[] = [
  { id: "demo", label: "demo", host: "192.168.1.10", port: 22, user: "dev", identityFile: "", proxyJump: "", defaultWorkspace: "~/app", serveInstall: "auto", credentialMode: "remote", useSSHConfig: false },
];
const mockRemoteConn: Record<string, RemoteConnectionStatus["state"]> = { demo: "connected" };
const mockRemoteForwards: Record<string, RemoteForwardView[]> = {};
