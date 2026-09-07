import type { RecoveryEventFields } from "./recoveryStatus";
// Wire contract — mirrors desktop/wire.go (itself mirroring internal/serve/wire.go).
// One event channel carries every kind; `kind` discriminates the payload.
import type { HistoryServerSearch } from "./searchSources";
import type { Todo } from "./tools";
import type { ContextBudgetInfo, ContextMaintenanceInfo, WireContextMaintenance } from "./contextMaintenanceTypes";
import type { WireApproval } from "./approvalTypes";
import type { RemoteProjectNodeFields, RemoteSessionMetaFields, RemoteTabMetaFields } from "./remoteTypes";
import type { PinnedFileInfo } from "./pinnedContextBridge";
import type { RecoveryLineageView } from "./sessionRecoveryTypes";
export * from "./remoteTypes";
export type { ContextBudgetInfo, ContextMaintenanceInfo, ContextMaintenanceReceipt, WireContextMaintenance } from "./contextMaintenanceTypes";
export type { ProjectGroupsSnapshot, ProjectRuntimeTopic, ProjectTopicKey, ProjectTopicPage, ProjectTopicPageRequest, ProjectTreeChangedV2, ProjectTreeOrganizationBindings, ProjectTreeRuntimeSnapshot, ProjectTreeSnapshot, SessionCatalogBindings, SessionCatalogStatus, SessionGroup, SessionReference } from "./sessionCatalogTypes";
export type EventKind =
  | "turn_started"
  | "reasoning"
  | "text"
  | "message"
  | "tool_dispatch"
  | "tool_result"
  | "tool_result_preview"
  | "turn_status"
  | "prompt_answered" | "session_changed"
  | "mcp_interaction"
  | "tool_progress" | "tool_started"
  | "usage"
  | "notice"
  | "phase"
  | "approval_request"
  | "ask_request"
  | "turn_done"
  | "compaction_started"
  | "compaction_done"
  | "mcp_surface_ready"
  | "retrying"
  | "steer"
  | "guardian_assessment"
  | "extension_surface"
  | "extension_status"
  | "stream_attempt"
  | "context_maintenance"
  | "workspace_changed"
  | "turn_phase"
  | "completion_summary"
  | "provider_unreachable";
export type StreamAttemptAction = "begin" | "discard" | "commit";
export type TurnStatus = "queued" | "in_progress" | "waiting_user" | "cancelling" | "completed" | "interrupted" | "failed" | "protocol_failed" | "recovery_required" | "unknown";
export interface TurnEventEnvelope {
  turnId: string;
  seq: number;
  status: TurnStatus | string;
  runtimeEpoch?: string;
  submissionId?: string;
  transcriptRevision?: number;
  transcriptDigest?: string;
  event: WireEvent;
}
export interface TurnEventReplayView {
  events: TurnEventEnvelope[];
  floorSeq: number;
  latestSeq: number;
  nextAfterSeq: number;
  hasMore: boolean;
  resetRequired: boolean;
  transcriptRevision?: number;
  transcriptDigest?: string;
  runtimeEpoch?: string;
}
export interface WireStreamAttempt {
  id: string;
  action: StreamAttemptAction;
  attempt?: number;
  max?: number;
  /** Fixed enum only: connection_reset | premature_eof | idle_timeout */
  reason?: string;
}
export interface WireCompaction {
  trigger?: string; // "auto" | "manual"
  messages?: number; // done: how many messages were folded into the summary
  summary?: string; // done: the briefing (empty on an aborted pass)
  archive?: string; // done: archive path, if any
}
export interface WireProfile {
  model?: string;
  effort?: string;
}

export interface WireShellExecution {
  kind?: string;
  shell?: string;
  shellVersion?: string;
  platform?: string;
  supportsAndAnd?: boolean;
  state?: string;
  failurePhase?: string;
  exitCode?: number;
  outputTail?: string;
  mutationRisk?: string;
  verification?: string;
  durationMs?: number;
}

export interface WireTool {
  id?: string;
  name: string;
  args?: string;
  resolvedName?: string;
  capabilityId?: string;
  output?: string;
  err?: string;
  readOnly: boolean;
  truncated?: boolean;
  durationMs?: number;
  partial?: boolean; // an early dispatch (name only) — a full one with args follows
  argChars?: number; // partial only: cumulative argument chars streamed so far
  refreshed?: boolean; // same-ID full dispatch with a preview recomputed after an earlier write
	parentId?: string; // set on a sub-agent's calls — the parent `task` call's id
  /** Host-local stream_attempt id for speculative parent partials only. */
  attemptId?: string;
  diff?: string;
  added?: number;
  removed?: number; subagentRef?: string; subagentStatus?: string; subagentErrorCode?: string; subagentRetryable?: boolean;
  profile?: WireProfile; // subagent model/effort resolved for this call
  execution?: WireShellExecution; // local shell metadata; never provider-visible
}

export interface WireCacheDiagnostics {
  prefixHash: string;
  prefixChanged: boolean;
  prefixChangeReasons?: string[];
  systemHash: string;
  toolsHash: string;
  logRewriteVersion: number;
  toolSchemaTokens: number;
  cacheMissTokens: number;
  cacheHitTokens: number;
  sessionContext?: import("./sessionContextTypes").WireSessionContextDiagnostics;
}
export interface WireUsage {
  promptTokens: number;
  completionTokens: number;
  totalTokens: number;
  cacheHitTokens: number;
  cacheMissTokens: number;
  reasoningTokens?: number;
  estimated?: boolean;
  source?: string;
  cacheDiagnostics?: WireCacheDiagnostics;
  // Session-cumulative cache tokens — the status bar shows the aggregate
  // hit-rate (Σhit/Σ(hit+miss)), steadier than the single-turn cacheHitTokens.
  sessionCacheHitTokens: number;
  sessionCacheMissTokens: number;
  /** Latest single-request shape for context gauges; omit → use billable totals. */
  contextPromptTokens?: number;
  contextCompletionTokens?: number;
  contextReasoningTokens?: number;
  contextCacheHitTokens?: number;
  contextCacheMissTokens?: number;
  cost?: number;
  currency?: string;
  currencyCode?: string;
  // Deprecated compatibility alias. Prefer cost + currencyCode / costQuote.
  costUsd?: number;
  costComplete?: boolean;
  displayComplete?: boolean;
  displayStatus?: string;
  aggregateMode?: string;
  originalTotals?: Money[];
  /** Host-side structured quote; prefer over cost/currency aliases. */
  costQuote?: CostQuote;
}

export interface Money {
  amount: string;
  currency: string;
}

export interface CostQuote {
  original: Money;
  originalTotals?: Money[];
  valuations?: Record<string, {
    money: Money;
    basis: string;
    source: string;
    asOf: string;
    rateSnapshot?: { base: string; quote: string; rate: number; source: string; asOf: string; stale?: boolean };
    stale?: boolean;
  }>;
  selected?: Money;
  billingMode?: string;
  estimated: boolean;
  costComplete?: boolean;
  displayComplete?: boolean;
  complete: boolean;
  displayStatus?: "matched" | "fallback_original" | "bucketed" | "unavailable" | string;
  aggregateMode?: "single_currency" | "common_valuation" | "currency_buckets" | string;
  modelRef?: string;
  usageSource?: string;
  pricingFingerprint?: string;
  rateDate?: string;
  rateBand?: "peak" | "off_peak" | "mixed" | string;
  ratedAt?: string;
  incompleteReason?: string;
  legacyEstimate?: boolean;
  catalogSource?: string;
}

export interface WireRecoveryApproval {
  source_agent?: string;
  failed_tool?: string;
  failed_summary?: string;
  diagnosis?: string;
  next_tool?: string;
  next_action?: string;
  change_kind?: string;
  change_rationale?: string;
  review_rationale?: string;
  plan_before?: string;
  plan_after?: string;
  can_grant_task?: boolean;
  task_grant_scope?: string;
}

export type { WireApproval, WireWriteAccessApproval } from "./approvalTypes";

export interface WireGuardian {
  id: string;
  tool: string;
  subject: string;
  outcome: string;
  risk_level?: string;
  user_authorization?: string;
  rationale?: string;
  duration_ms?: number;
  usage?: WireUsage;
}

export interface WireDecisionReceipt {
  id: string;
  kind: string;
  tool?: string;
  subject?: string;
  outcome: string;
}

export interface WireAskOption {
  label: string;
  description?: string;
}

export type PromptKind = "ask" | "approval" | "plan" | "recovery" | "mcp";
export interface PromptIdentity {
  promptId: string;
  turnId?: string;
  runtimeEpoch?: string;
  kind: PromptKind;
}
export type PromptLifecycle = "pending" | "submitting" | "resolved" | "expired";
export interface DecisionCardState {
  identity: PromptIdentity;
  lifecycle: PromptLifecycle;
  failureNoticeShown?: boolean;
}

export interface WireAskQuestion {
  id: string;
  header?: string;
  prompt: string;
  options: WireAskOption[];
  multi?: boolean;
}

export interface WireAsk {
  id: string;
  questions: WireAskQuestion[];
  turnId?: string;
  runtimeEpoch?: string;
}

export type { MCPAppInstanceView, MCPAppPresentation } from "./mcpAppProtocol";

// One server-initiated MCP elicitation awaiting accept/decline/cancel. Form
// mode carries a flat primitive JSON schema; url mode a credential-free target.
export interface WireMCPInteraction {
  id: string;
  server: string;
  mode: "form" | "url";
  message: string;
  requestedSchema?: unknown;
  url?: string;
  elicitationId?: string;
  turnId?: string;
  runtimeEpoch?: string;
}

// Extension UI surfaces (stage 8a) — structured-only documents published by
// extension sidecars through the host UI hub. Exactly one sub-struct is set,
// selected by `kind`.
export interface WireExtensionStatus {
  label: string;
  detail?: string;
  severity?: string; // "info" | "warn" | "error"
  progress?: number;
}

export interface WireExtensionKeyValue {
  key: string;
  value: string;
}

export interface WireExtensionActionRef {
  actionId: string;
  label: string;
}

export interface WireExtensionCard {
  title?: string;
  markdown?: string;
  text?: string;
  fields?: WireExtensionKeyValue[];
  progress?: number;
  actions?: WireExtensionActionRef[];
}

export interface WireExtensionFormField {
  key: string;
  label?: string;
  kind?: string; // "confirm" | "input" | "select" | "multiselect"
  options?: string[];
  default?: unknown;
  required?: boolean;
}

export interface WireExtensionForm {
  title?: string;
  message?: string;
  fields: WireExtensionFormField[];
}

export interface WireExtensionNotification {
  title: string;
  body?: string;
  severity?: string; // "info" | "warn" | "error"
}

export interface WireExtensionSurface {
  pluginId: string;
  surfaceId: string;
  sessionId?: string;
  generation?: number;
  kind: string; // "status" | "card" | "form" | "notification"
  status?: WireExtensionStatus;
  card?: WireExtensionCard;
  form?: WireExtensionForm;
  notification?: WireExtensionNotification;
}

// ExtensionActionView is one handshake-declared extension UI action, the JSON
// twin of desktop's ExtensionActionView (stage 8b2). Slash is the public
// invocation name, "/<plugin>:<action>".
export interface ExtensionActionView {
  plugin: string;
  action: string;
  slash: string;
  description?: string;
}

// QuestionAnswer is the reply for one question, sent back via AnswerQuestion.
export interface QuestionAnswer {
  questionId: string;
  selected: string[];
}

export interface MemoryCitation {
  id?: string;
  source: string;
  lineStart?: number;
  lineEnd?: number;
  note?: string;
  kind?: string;
}

export interface WireEvent extends RecoveryEventFields {
  kind: EventKind;
  promptId?: string;
  promptKind?: "ask" | "approval" | "plan" | "recovery" | "mcp" | string;
  promptLegacy?: boolean;
  turnId?: string;
  seq?: number;
  status?: TurnStatus;
  text?: string;
  detail?: string;
  // Stable notice id for localization; empty/absent = localize by text match.
  code?: string;
  reasoning?: string;
  memoryCitations?: MemoryCitation[];
  level?: "info" | "warn";
  tool?: WireTool;
  usage?: WireUsage;
  approval?: WireApproval;
  ask?: WireAsk;
  mcpInteraction?: WireMCPInteraction;
  compaction?: WireCompaction;
  maintenance?: WireContextMaintenance;
  guardian?: WireGuardian;
  decisionReceipt?: WireDecisionReceipt;
  extension?: WireExtensionSurface;
  err?: string;
  checkpointTurn?: number; // Authoritative TurnDone rewind target; zero is valid.
  submissionId?: string; // Opaque correlation for the exact optimistic user submission.
  outcome?: "completed" | "partial" | "blocked" | "final_readiness" | "recovery_paused" | "completion_uncertain";
  readiness?: WireFinalReadiness;
  protocolRecovery?: { id: string };
  diagnostic?: { kind: string; status?: number; traceId?: string };
  /** Optional: "headers" | "stream". Older clients ignore unknown fields. */
  retryScope?: "headers" | "stream" | "protocol";
  streamAttempt?: WireStreamAttempt;
  /** Durable session-inbox item id for steer / TurnDone correlation. */
  itemId?: string;
  sessionPath?: string; // Serve multi-session routing tag; absent locally.
  workspace?: WireWorkspaceChanged;
  /** turn_phase: working | checking | verifying | reviewing */
  phase?: string;
  /** completion_summary: content-free quality summary for role settings */
  completion?: WireCompletionSummary;
  tabId?: string; // Go's tabEventSink tags events for the correct per-tab reducer.
  runtimeEpoch?: string; sessionGeneration?: number;
  /** Unix milliseconds recorded by the desktop host when this turn began. */
  turnStartedAt?: number;
  sessionHitTokens?: number;
  sessionMissTokens?: number;
  sessionCost?: number;
  sessionCurrency?: string;
  // Deprecated compatibility alias. Prefer sessionCost + sessionCurrency.
  sessionCostUsd?: number;
}

export interface WireCompletionSummary {
  preset: string;
  verdict: string;
  mutations: number;
  checks_passed: number;
  checks_failed: number;
  checks_suppressed: number;
  review: string;
  gap_kinds?: string[];
  constraint_degraded: boolean;
  /** Turn-time policy floor. Missing on historical events. */
  floor?: "standard" | "delivery" | string;
  /** Backend decision; authoritative when floor is present. */
  attention?: boolean;
}

export type WorkspaceWatchState = "active" | "degraded" | "unavailable";
export type WorkspaceChangeOp = "create" | "write" | "remove" | "rename" | "unknown";

export interface WorkspaceRevisions {
  content: number;
  tree: number;
  workingTree: number;
  gitMeta: number;
  session: number;
}

export interface WorkspacePathChange {
  path: string;
  oldPath?: string;
  op: WorkspaceChangeOp;
}

export interface WireWorkspaceChanged {
  revisions: WorkspaceRevisions;
  changes: WorkspacePathChange[];
  allPaths: boolean;
  source: "agent" | "filesystem" | "git" | "mixed" | "reconcile";
  watchState: WorkspaceWatchState;
}

export type SessionRuntimePhase = "starting" | "ready" | "lease_blocked" | "failed" | "closing";

export interface SessionRuntimeIssue {
  code: "session_lease_held" | "startup_failed";
  message: string;
  retryable: boolean;
  holderPid?: number;
  holderHost?: string;
  acquiredAt?: string;
}

export interface SessionRuntimeView {
  phase: SessionRuntimePhase;
  epoch: string;
  issue?: SessionRuntimeIssue;
}

/** Occupancy report for a session a local serve holds; drives the takeover dialog. */
export interface SessionTakeoverView {
  available: boolean;
  reason?: string;
  sessionPath?: string;
  holder?: "serve" | "external" | "other" | "free";
  remoteAttached?: boolean;
  running?: boolean;
  mirrored?: boolean;
  holderPid?: number;
  holderHost?: string;
}

export interface WireFinalReadiness {
  attempts?: number;
  missing?: string[];
}

// Tab management types (desktop/tabs.go).
export interface TabMeta extends RemoteTabMetaFields {
  id: string;
  tabType?: "session" | "file";
  scope: string;
  workspaceRoot: string;
  workspaceName: string;
  workspacePath?: string;
  gitBranch?: string;
  isolatedWorktree?: boolean;
  topicId: string;
  topicTitle: string;
  sessionPath?: string;
  sessionRevision?: number;
  sessionDigest?: string;
  sessionGeneration?: number;
  readOnly?: boolean;
  /** Remote tab whose session a local runtime on the serve host took over. */
  takenOver?: boolean;
  filePath?: string;
  projectColor?: string;
  label: string;
  ready: boolean;
  runtime?: SessionRuntimeView;
  running: boolean;
  /** Unix milliseconds for the currently active foreground turn. */
  turnStartedAt?: number;
  pendingPrompt?: boolean;
  backgroundJobs?: number;
  cancelRequested?: boolean;
  cancellable?: boolean;
  turnId?: string;
  turnStatus?: TurnStatus;
  turnEventSeq?: number;
  turnReplayAfterSeq?: number;
  mode: Mode;
  collaborationMode?: CollaborationMode;
  toolApprovalMode?: ToolApprovalMode;
  tokenMode?: TokenMode;
  agentPreset?: AgentPreset; // canonical role; prefer qualityFloor
  qualityFloor?: QualityFloor; // absent means standard
  floorInferred?: boolean; // facts, not user choice, put the session at delivery
  goal?: string;
  goalStatus?: GoalStatus;
  recovered?: boolean;
  recoveryReason?: string;
  recoveryDigest?: string;
  recoveryParentId?: string;
  versionKind?: "normal" | "recovery" | "subagent" | string;
  versionState?: "active" | "pending" | "resolved" | "trashed" | string;
  parentVersionId?: string;
  startupErr?: string;
  active: boolean;
  cwd: string;
}

export interface TerminalSessionView {
  id: string;
  title: string;
  shell: string;
  cwd: string;
  createdAt: number;
  exitCode?: number;
  running: boolean;
}

export interface TerminalShellView {
  id: string;
  label: string;
}

export interface TerminalWorkspaceView {
  available: boolean;
  readOnly: boolean;
  reason?: string;
  sessions: TerminalSessionView[];
  shells: TerminalShellView[];
}

export interface ProjectNode extends RemoteProjectNodeFields {
  key: string;
  kind: "project" | "topic" | "session" | "global_folder" | "global_topic" | "global_session";
  label: string;
  root?: string;
  topicId?: string;
  recoveryPath?: string;
  sessionPath?: string;
  preview?: string;
  projectColor?: string;
  turns?: number;
  turnsState?: "unknown" | "valid" | "corrupt" | string;
  health?: "ok" | "missing" | "corrupt" | "degraded" | string;
  createdAt?: number;
  lastActivityAt?: number;
  open?: boolean;
  running?: boolean;
  status?: ProjectTopicStatus;
  pinned?: boolean;
  sortOrder?: number;
  recovered?: boolean;
  recoveryReason?: string;
  recoveryDigest?: string;
  recoveryParentId?: string;
  recoveryState?: "normal" | "repairing" | "adopted" | "preferred" | "diverged" | "recovery_only" | string;
  recoveryBranchCount?: number;
  recoveryUnresolvedCount?: number;
  recoveryCleanupEligibleCount?: number;
  recoveryCopyCount?: number; // Deprecated: ordinary trees hide physical copies.
  isolatedWorktree?: boolean;
  runtimeOnly?: boolean;
  children?: ProjectNode[];
}

export type { RecoveryLineageMember, RecoveryLineageView } from "./sessionRecoveryTypes";

export interface RecoveryCleanupRequest {
  scope: string;
  workspaceRoot?: string;
  topicId: string;
  apply: boolean;
}

export interface RecoveryPreferenceRequest {
  scope: string;
  workspaceRoot?: string;
  topicId: string;
  path: string;
}

export interface RecoveryCleanupItem {
  path: string;
  status: "eligible" | "moved" | "busy" | "kept" | string;
  error?: string;
}

export interface RecoveryCleanupResult {
  eligible: number;
  moved: number;
  busy: number;
  kept: number;
  dryRun: boolean;
  items: RecoveryCleanupItem[];
}

export interface DeliveryWorktreeAvailability {
  available: boolean;
  reason?: string;
  repoRoot?: string;
  branch?: string;
  sourceDirty?: boolean;
}

export interface DeliveryWorktreeOpenResult {
  workspaceRoot: string;
  worktreeRoot: string;
  sourceRoot: string;
	branch: string;
	sourceDirty: boolean;
	sourceRevision?: string;
	taskId?: string;
	conversationId?: string;
	tab: TabMeta;
}

export * from "./worktreeMergeTypes";

export type ProjectTopicStatus = "thinking" | "streaming" | "waiting_confirmation" | "background_job" | "paused" | "awaiting_delivery" | "error" | "diverged_recovery";

export interface TopicMeta {
  id: string;
  title: string;
  createdAt: number;
}

export interface SessionRecoveryEvent {
	conversationId?: string;
	activeVersionId?: string;
	recoveryVersionId?: string;
  originalPath?: string;
  recoveryPath: string;
  scope?: string;
  workspaceRoot?: string;
  topicId?: string;
  topicTitle?: string;
  recoveryReason?: string;
  recoveryDigest?: string;
  recoveryParentId?: string;
	existing?: boolean;
	baseRevision?: number;
	diskRevision?: number;
	canContinue?: boolean;
	requiresChoice?: boolean;
}

export interface SessionVersionStateView {
  conversationId?: string;
  activeVersionId?: string;
  activePath?: string;
  recoveryVersionId?: string;
  canContinue: boolean;
  requiresChoice: boolean;
  lineage: RecoveryLineageView;
}

export interface SessionRecoveryFailedEvent {
  reason?: "lease_held" | "lease_unavailable" | string;
  conversationId?: string;
  topicId?: string;
  canContinue?: boolean;
  recoveryPending?: boolean;
  recoveryPath?: string;
  workspaceRoot?: string;
}

export interface ContextPanelInfo {
  usedTokens: number;
  windowTokens: number;
  promptTokens: number;
  completionTokens: number;
  totalTokens: number;
  reasoningTokens: number;
  cacheHitTokens: number;
  cacheMissTokens: number;
  estimated?: boolean;
  sessionCacheHitTokens: number;
  sessionCacheMissTokens: number;
  sessionCompletionTokens: number;
  sessionEstimated?: boolean;
  requestCount?: number;
  elapsedMs?: number;
  sessionCost?: number;
  sessionCurrency?: string;
  // Deprecated compatibility alias. Prefer sessionCost + sessionCurrency.
  sessionCostUsd?: number;
  sessionCostComplete?: boolean;
  sessionCostEstimated?: boolean;
  sessionBillingMode?: string;
  sessionCostQuote?: CostQuote;
  sources?: Record<string, UsageSourceStats>;
  mock?: boolean;
  readFiles: ReadFileRecord[];
  changedFiles: ChangedFileInfo[];
  contextBudget?: ContextBudgetInfo;
}

export interface UsageSourceStats {
  promptTokens: number;
  completionTokens: number;
  totalTokens: number;
  reasoningTokens: number;
  cacheHitTokens: number;
  cacheMissTokens: number;
  estimated?: boolean;
  requestCount: number;
  sessionCost?: number;
  sessionCurrency?: string;
  sessionCostUsd?: number;
}

export interface ReadFileRecord {
  path: string;
  turn: number;
  time: number;
  offset?: number;
  limit?: number;
  truncated?: boolean;
}

export interface ChangedFileInfo {
  path: string;
  oldPath?: string;
  sources: string[];
  gitStatus?: string;
  turns: number[];
  latestPrompt?: string;
  latestTime?: number;
}

// Bound-method payloads (desktop/app.go).
export interface HistoryMessage {
  role: string;
  content: string;
  detail?: string;
  code?: string;
  submitText?: string;
  checkpointTurn?: number;
  createdAt?: number;
  reasoning?: string;
  workDurationMs?: number;
  memoryCitations?: MemoryCitation[];
  level?: "info" | "warn";
  toolCalls?: HistoryToolCall[];
  toolCallId?: string;
  toolName?: string;
  toolResultArchived?: boolean;
  toolResultError?: string;
  execution?: WireShellExecution;
  pending?: boolean;
  trigger?: string;
  messages?: number;
  summary?: string;
  archive?: string;
  decisionReceipt?: WireDecisionReceipt;
  readiness?: WireFinalReadiness;
  protocolRecovery?: { id: string };
  diagnostic?: { kind: string; status?: number; traceId?: string };
  serverSearch?: HistoryServerSearch[];
}

export interface HistoryToolCall {
  id: string;
  name: string;
  arguments: string;
  resolvedName?: string;
  capabilityId?: string;
  resolvedReadOnly?: boolean;
  subject?: string;
  summary?: string;
  diff?: string;
  added?: number;
  removed?: number;
  argumentsArchived?: boolean;
}

export interface HistoryPage {
  messages: HistoryMessage[];
  startTurn: number;
  endTurn: number;
  totalTurns: number;
  hasOlder: boolean;
  revision?: number;
  digest?: string;
}

// ── Windowed history paging (desktop/history_slice.go) ──────────────────────
// HistorySliceForTab pages toward older history with an opaque cursor; the
// first call uses cursor "" for the newest page. Entry IDs are stable for the
// life of a session revision (s<file>:r<epoch>:m<msgIndex>:o<subOrder>).

export interface HistorySliceRequest {
  cursor: string; // "" = newest page; pass nextCursor to page older
  turns?: number;
  entries?: number;
  bytes?: number;
}

// HistoryContentRef marks a string field replaced inline by a ≤4KiB preview;
// the full value is fetchable in chunks via HistoryContentForTab.
export interface HistoryContentRef {
  entryId: string;
  field: string; // content|reasoning|submitText|detail|code|summary|archive|toolResultError|toolArguments|toolSubject|toolSummary|toolDiff
  size: number;
  chunks: number;
  toolCallId?: string;
  revision: number;
  revKnown?: boolean;
  digest: string;
}

export interface HistoryEntry {
  entryId: string;
  turn: number; // 1-based visible turn (0 = before the first turn)
  order: number; // absolute provider-message index
  message: HistoryMessage;
  refs: HistoryContentRef[];
}

export interface SessionClearResult { sessionPath: string; sessionRevision?: number; sessionDigest?: string; sessionGeneration: number }

export interface HistorySlice {
  entries: HistoryEntry[];
  nextCursor: string; // toward older; empty when none
  hasOlder: boolean;
  totalTurns: number;
  startTurn: number;
  endTurn: number;
  stale: boolean; // cursor bound to an older session revision: discard + reload
  revision: number;
  revisionKnown?: boolean;
  digest?: string;
  // Diagnostic read path: index|scan|event-log|live-index|live-fallback.
  source?: string;
  error?: string; // failed read; empty entries alone are not an error
}

export interface HistoryContentChunk {
  entryId: string;
  field: string;
  chunk: number;
  chunks: number;
  data: string;
  done: boolean;
  stale: boolean;
}

// ── Two-phase topic activation (desktop/topic_activation.go) ────────────────

export interface TopicActivationRequest {
  scope: string;
  workspaceRoot: string;
  topicId: string;
  sessionPath: string;
  requestId?: string;
}

export interface TopicActivationTicket {
  requestId: string;
  tabId: string;
  meta: TabMeta;
}

export type TopicActivationPhase = "starting" | "ready" | "failed" | "cancelled";

export interface TopicActivationEvent {
  requestId: string;
  tabId: string;
  phase: TopicActivationPhase;
  error?: string;
}

// tab:meta channel: a full refreshed Meta pushed after the background refresh
// of the expensive MetaForTab fields (git branch, image-input capability).
export interface TabMetaRefreshEvent {
  tabId: string;
  meta: Meta;
}

export interface PromptHistoryEntry {
  text: string;
  at: number;          // unix ms
  sessionPath: string;
  turn: number;
}

export interface PromptHistoryResult {
  entries: PromptHistoryEntry[] | null;
  nonce: string;
  olderCursor?: string;
  hasOlder?: boolean;
}

// CheckpointMeta is one rewind point (a user turn) for the rewind UI.
export interface CheckpointMeta {
  turn: number;
  prompt: string;
  files: string[];
  fileCount?: number;
  filesTruncated?: boolean;
  turnFileCount?: number;
  time: number; // unix ms
  canCode?: boolean;
  canConversation?: boolean;
  coverage?: string;
  coverageGaps?: string[];
  expiredFilePayload?: boolean;
  activeWriters?: number;
  legacy?: boolean;
  canUndoFiles?: boolean;
  disabledReason?: string;
}

export type { RewindPlanView } from "./rewindTypes";

export interface RewindResultView {
  ok?: boolean;
  transactionId?: string;
  undoAvailable?: boolean;
  written?: string[];
  deleted?: string[];
  conversationOk?: boolean;
  conversationForked?: boolean;
  operationId?: string;
  branch?: string;
  partial?: boolean;
  tabId?: string;
  tab?: TabMeta;
  error?: string;
  conflicts?: string[];
  coverage?: string;
}

export type { SessionMeta } from "./sessionMetaTypes";

export type { HistoryIndexStatus, HistorySearchContextLine, HistorySearchContextRequest, HistorySearchHit, HistorySearchPage, HistorySearchRequest, HistorySessionPage, HistorySessionPageRequest } from "./historyCatalogTypes";

export interface WorkspaceView {
  path: string;
  name: string;
  current: boolean;
}

export interface ContextInfo {
  used: number;
  window: number;
  sessionTokens: number;
  compactRatio?: number;
  sessionCost?: number;
  sessionCurrency?: string;
  cacheHitTokens?: number;
  cacheMissTokens?: number;
  estimated?: boolean;
  sessionCostComplete?: boolean;
  sessionCostQuote?: CostQuote;
  sources?: Record<string, UsageSourceStats>;
  maintenance?: ContextMaintenanceInfo;
  contextBudget?: ContextBudgetInfo;
}

export interface Meta extends RemoteSessionMetaFields {
  label: string;
  ready: boolean;
  runtime?: SessionRuntimeView;
  startupErr?: string;
  eventChannel: string;
  sessionPath?: string;
  sessionRevision?: number;
  sessionDigest?: string;
  sessionGeneration?: number;
  cwd: string;
  workspaceRoot?: string;
  workspaceName?: string;
  workspacePath?: string;
  gitBranch?: string;
  imageInputEnabled?: boolean;
  visionFallbackEnabled?: boolean;
  autoApproveTools?: boolean;
  bypass?: boolean; // legacy JSON key for YOLO/full-access tool auto-approval
  collaborationMode?: CollaborationMode;
  toolApprovalMode?: ToolApprovalMode;
  tokenMode?: TokenMode;
  agentPreset?: AgentPreset; // canonical role; prefer qualityFloor
  qualityFloor?: QualityFloor; // absent means standard
  floorInferred?: boolean; // facts, not user choice, put the session at delivery
  goal?: string;
  goalStatus?: GoalStatus;
  goalRuntime?: GoalRuntime;
  canonicalTodos?: Todo[]; dismissedTodoBatches?: string[]; pinnedFiles?: PinnedFileInfo[];
}
export type CollaborationMode = "normal" | "plan" | "goal";
export type ToolApprovalMode = "ask" | "auto" | "yolo";
// TokenMode is the dual-write wire value for the session quality floor.
// The floor itself is standard|delivery; light and its aliases fold to
// standard, and full/economy remain one compatibility version of old values.
export type TokenMode = "full" | "economy" | "delivery" | "light" | "balanced";
export type AgentPreset = "light" | "balanced" | "delivery";
export type QualityFloor = "standard" | "delivery";
export type GoalStatus = "running" | "complete" | "blocked" | "stopped";
// Optional Goal runtime summary; absent for old hosts or when no goal is active.
export interface GoalRuntime {
  turnsUsed: number;
  turnsLimit: number; // Deprecated: Goal exposes no turn limit and returns 0.
  tokensUsed: number;
  requestsUsed?: number;
  workDurationMs?: number;
  /** @deprecated Goal has no hard token limit; retained as 0 for old hosts/clients. */
  tokensLimit: number;
  noProgressTurns: number;
  /** @deprecated No longer enforced; retained for old hosts/clients. */
  noProgressLimit: number;
  lastReason?: string;
  stopCause?: string;
  budgetExtensions: number; // Deprecated: resumes no longer extend a numeric quota.
}
export function normalizeCollaborationMode(mode?: string, goal?: string, legacyMode?: Mode): CollaborationMode {
  if (mode === "plan" || mode === "goal" || mode === "normal") return mode;
  if (legacyMode && modeHasPlan(legacyMode)) return "plan";
  if ((goal ?? "").trim()) return "goal";
  return "normal";
}

export function normalizeToolApprovalMode(
  mode?: string,
  legacyMode?: Mode,
  legacyAutoApproveTools?: boolean,
  fallbackMode?: ToolApprovalMode,
): ToolApprovalMode {
  const normalized = typeof mode === "string" ? mode.trim().toLowerCase() : "";
  if (normalized === "auto" || normalized === "yolo" || normalized === "ask") return normalized as ToolApprovalMode;
  if (legacyAutoApproveTools || (legacyMode && modeHasAutoApproveTools(legacyMode))) return "yolo";
  if (fallbackMode === "auto" && normalized === "") return "auto";
  return "ask";
}

export function normalizeTokenMode(mode?: string): TokenMode {
  const m = (mode ?? "").trim().toLowerCase();
  if (m === "economy" || m === "light" || m === "lite" || m === "eco") return "economy";
  if (m === "delivery" || m === "deliver" || m === "quality") return "delivery";
  // balanced | full | empty | unknown → balanced wire value "full"
  return "full";
}

/** Canonical product id for the three Agent role settings. */
export function normalizeAgentPreset(mode?: string): AgentPreset {
  const wire = normalizeTokenMode(mode);
  if (wire === "economy" || wire === "light") return "light";
  if (wire === "delivery") return "delivery";
  return "balanced";
}

export function tokenModeFromAgentPreset(preset: AgentPreset): TokenMode {
  switch (preset) {
    case "light":
      return "economy";
    case "delivery":
      return "delivery";
    default:
      return "full";
  }
}

// Mode is the compatibility string for two independent composer axes:
// plan (plan-first workflow) and yolo (tool auto-approval).
export type Mode = "normal" | "plan" | "yolo" | "plan-yolo";

export function normalizeMode(mode?: string): Mode {
  if (mode === "plan" || mode === "yolo" || mode === "plan-yolo" || mode === "yolo-plan") {
    return mode === "yolo-plan" ? "plan-yolo" : mode;
  }
  return "normal";
}

export function modeHasPlan(mode: Mode): boolean {
  return mode === "plan" || mode === "plan-yolo";
}

export function modeHasAutoApproveTools(mode: Mode): boolean {
  return mode === "yolo" || mode === "plan-yolo";
}

export function modeFromAxes(plan: boolean, autoApproveTools: boolean): Mode {
  if (plan && autoApproveTools) return "plan-yolo";
  if (plan) return "plan";
  if (autoApproveTools) return "yolo";
  return "normal";
}

export function modeWithPlan(mode: Mode, plan: boolean): Mode {
  return modeFromAxes(plan, modeHasAutoApproveTools(mode));
}

export function modeWithAutoApproveTools(mode: Mode, autoApproveTools: boolean): Mode {
  return modeFromAxes(modeHasPlan(mode), autoApproveTools);
}

export interface CommandInfo {
  name: string; // without the leading slash
  description: string;
  hint?: string;
  kind: "builtin" | "custom" | "mcp" | "skill" | "subagent";
  group?: "actions" | "management" | "subagents" | "skills" | "integrations";
  plugin?: string;
  color?: string;
}

export interface DirEntry {
  name: string;
  path?: string;
  isDir: boolean;
  displayName?: string;
  displayPath?: string;
}

export interface DroppedItem {
  kind: "workspace" | "attachment";
  path: string;
  isDir?: boolean;
  displayPath?: string;
  previewUrl?: string;
}

export interface FilePreview {
  path: string;
  body: string;
  size: number;
  truncated: boolean;
  binary: boolean;
  kind?: "image" | "pdf";
  mime?: string;
  url?: string;
  err?: string;
}

export interface WorkspaceChangeView {
  path: string;
  oldPath?: string;
  sources: string[];
  gitStatus?: string;
  turns?: number[];
  latestPrompt?: string;
  latestTime?: number;
  canSessionRevert?: boolean;
}

export interface WorkspaceChangesView {
  files: WorkspaceChangeView[];
  gitAvailable: boolean;
  gitErr?: string;
  gitBranch?: string;
}

export interface WorkspaceChangeDetailView {
  diff?: string;
  source?: "git" | "session";
  added?: number;
  removed?: number;
  binary?: boolean;
  truncated?: boolean;
}

export interface GitCommitView {
  hash: string;
  author: string;
  date: string;
  message: string;
}

export interface GitCommitDetailView {
  diff?: string;
  files?: string[];
}

export interface ComposerInsertRequest {
  id: number;
  text: string;
  mode?: "insert" | "replace" | "prefix";
}

// MCP & Skills drawer (desktop/app.go Capabilities) — the GUI counterpart to
// /mcp + /skill: connected/failed servers and discoverable skills.
export interface ServerView {
  name: string;
  transport: string;
  status: "connected" | "deferred" | "failed" | "initializing" | "disabled";
  /** @deprecated derived from enabled */
  startIntent?: "off" | "automatic" | string;
  runtimeState?: "idle" | "connecting" | "ready" | "issue" | string;
  protocolVersion?: string;
  sessionState?: "connecting" | "listening" | "ready" | "reconnecting" | "failed" | "closed" | string;
  reconnectAttempts?: number;
  errorKind?: "auth_required" | "session_missing" | "stream_closed" | "timeout" | "protocol" | "transport" | string;
  /** Product availability: available_on_demand | starting | connected | auth_required | project_auth_changed | start_failed | disabled */
  availability?: string;
  enabled?: boolean;
  installed?: boolean;
  action?: "none" | "authenticate" | "authorize" | "retry" | string;
  source?: "project" | "user" | "plugin" | "builtin" | string;
  configSource?: string;
  builtIn?: boolean;
  configured?: boolean;
  /** @deprecated same as enabled */
  autoStart: boolean;
  /** @deprecated ignored by runtime */
  tier?: "background" | "eager" | string;
  command?: string;
  args?: string[];
  url?: string;
  envKeys?: string[];
  headerKeys?: string[];
  tools: number;
  toolCount?: number;
  prompts: number;
  resources: number;
  hasTools?: boolean;
  error?: string;
  toolList?: MCPToolView[];
  callTimeoutSeconds?: number;
  toolTimeoutSeconds?: Record<string, number>;
  requiresLaunchApproval?: boolean;
  authStatus?: "none" | "possible" | "required" | string;
  authUrl?: string;
  authConfigured?: boolean;
  managedByPlugin?: string;
}
export interface MCPToolView {
  name: string;
  description: string;
  readOnlyHint?: boolean;
  destructiveHint?: boolean;
  schemaError?: string;
}
export interface SkillView {
  name: string;
  description: string;
  scope: string;
  sourceDir?: string;
  runAs: string;
  enabled: boolean;
  plugin?: string;
  model?: string;
  effort?: string;
  allowedTools?: string[];
  readOnly?: boolean;
  color?: string;
  invocation?: string;
  invocationMode?: string;
  body?: string;
  configuredModel?: string;
  configuredEffort?: string;
}
export interface SkillRootSkillView {
  name: string;
  description: string;
  scope: string;
  runAs: string;
  plugin?: string;
  model?: string;
  effort?: string;
  allowedTools?: string[];
  color?: string;
  invocation?: string;
}
export interface SkillRootView {
  dir: string;
  scope: string;
  priority: number;
  status: string;
  enabled: boolean;
  configured: boolean;
  removable: boolean;
  skills: number;
  skillItems?: SkillRootSkillView[];
  warning?: string;
}
export interface CapabilitiesView {
  servers: ServerView[];
  skills: SkillView[];
  skillRoots: SkillRootView[];
  plugins: PluginView[];
  allowImplicitInvocation?: boolean;
}
export interface SkillsSettingsView {
  skills: SkillView[];
  skillRoots: SkillRootView[];
  allowImplicitInvocation?: boolean;
}
export interface SubagentProfileInput {
  name: string;
  description: string;
  systemPrompt: string;
  color?: string;
  model?: string;
  effort?: string;
  allowedTools?: string[];
  readOnly?: boolean;
  scope?: "project" | "global";
}
export interface PluginView {
  name: string;
  version?: string;
  description?: string;
  source?: string;
  root: string;
  manifestKind?: string;
  enabled: boolean;
  skills: number;
  commands?: number;
  hooks: number;
  mcpServers: number;
  agents?: number;
  compatibility?: "full" | "partial" | "none" | string;
  mappedCapabilities?: string[];
  skippedCapabilities?: PluginCompatibilityIssue[];
  skillDetails?: PluginSkillView[];
  agentDetails?: PluginAgentView[];
  commandDetails?: PluginCommandView[];
  hookDetails?: PluginHookView[];
  mcpServerDetails?: PluginMCPServerView[];
  warnings?: string[];
  error?: string;
}
export interface PluginCompatibilityIssue {
  capability: string;
  path?: string;
  reason: string;
}
export interface PluginAgentView {
  name: string;
  description?: string;
  path?: string;
  invocation?: string;
  model?: string;
  allowedTools?: string[];
}
export interface PluginSkillView {
  name: string;
  description?: string;
  path?: string;
  invocation?: string;
  runAs?: string;
}
export interface PluginCommandView {
  name: string;
  description?: string;
  argHint?: string;
  path?: string;
  invocation?: string;
  shadowed?: boolean;
  shadowedByPlugin?: string;
}
export interface PluginHookView {
  event: string;
  match?: string;
  command?: string;
  contextFile?: string;
  description?: string;
}
export interface PluginMCPServerView {
  name: string;
  displayName?: string;
  description?: string;
  transport?: string;
  command?: string;
  url?: string;
  autoStart?: boolean;
}
export interface PluginInstallOptions {
  dryRun?: boolean;
  link?: boolean;
  replace?: boolean;
  name?: string;
}
export interface MCPServerInput {
  name: string;
  transport: string; // stdio | http | sse
  command: string;
  args: string[];
  url: string;
  env?: Record<string, string> | null;
  headers?: Record<string, string> | null;
  autoStart?: boolean | null;
  callTimeoutSeconds?: number | null;
  toolTimeoutSeconds?: Record<string, number> | null;
}

export interface MCPInstallResult {
  name: string;
  state: "ready" | "action_required" | "issue";
  toolCount: number;
  action: "none" | "authenticate" | "authorize" | "retry";
  message: string;
}

export interface MCPMarketplaceEntry {
  name: string;
  suggestedName: string;
  title?: string;
  description?: string;
  version?: string;
  repositoryUrl?: string;
  installable: boolean;
  unavailableReason?: string;
  transport?: "stdio" | "http" | "sse" | string;
  command?: string;
  args: string[];
  url?: string;
}

export interface MCPMarketplaceView {
  servers: MCPMarketplaceEntry[];
  cached: boolean;
  warning?: string;
}

export interface ModelInfo {
  ref: string; // "provider/model" — pass to SetModel
  provider: string;
  model: string;
  current: boolean;
  contextWindow?: number;
  vision?: boolean;
}

export interface EffortInfo {
  supported: boolean;
  current: string; // "auto" | "low" | "medium" | "high" | "xhigh" | "max"
  default: string;
  levels: string[];
}

// Slash sub-command / argument completion (desktop/app.go SlashArgs). Mirrors the
// CLI's arg hints so the composer can suggest e.g. /skill → list/show/new/paths.
export interface SlashArgItem {
  label: string;
  insert: string; // token to place at the current position
  hint: string;
  descend: boolean; // re-open the menu one level deeper after accepting
}
export interface SlashArgsResult {
  items: SlashArgItem[];
  from: number; // byte offset where the current token begins
}

// Memory panel payloads (desktop/app.go MemoryView).
export interface MemoryDoc {
  path: string;
  scope: string; // "user" | "ancestor" | "project" | "local"
  directory?: string;
  body: string;
  imports: Array<{ path: string; sourcePath: string }>;
  depth: number;
  order: number;
  precedence: number;
}

export interface InstructionDiagnostic {
  code: string;
  path: string;
  sourcePath?: string;
  line?: number;
  message: string;
}

export interface MemoryFact {
  id?: string;
  revision?: number;
  createdAt?: string;
  updatedAt?: string;
  name: string;
  title?: string;
  description: string;
  type: string; // "user" | "feedback" | "project" | "reference"
  scope: string; // "project" | "global"
  body: string;
  freshness: string; // "fresh" | "current" | "stale"
}

export interface MemoryConflict {
  key: string;
  projectId: string;
  projectName: string;
  globalId: string;
  globalName: string;
  resolution: "project_over_global";
}

export interface MemoryRecallHit {
  id: string;
  revision: number;
  name: string;
  title?: string;
  type: string;
  scope: string;
  score: number;
  freshness: string;
  reason: string;
  snippet: string;
}

export interface MemoryRecallTrace {
  query: string;
  hits: MemoryRecallHit[];
  omitted: number;
  charBudget: number;
  usedChars: number;
  suppressed?: string;
}

export interface MemoryArchive extends MemoryFact {
  path: string;
  archivedAt?: string;
}

export interface MemoryScope {
  scope: string; // "user" | "project" | "local"
  path: string;
}

export interface MemorySuggestion {
  id: string;
  name: string;
  title: string;
  description: string;
  type: string;
  scope: string; // "project" | "global"
  body: string;
  reason: string;
  evidence: string[];
}

export interface SkillSuggestion {
  id: string;
  name: string;
  description: string;
  scope: string;
  body: string;
  reason: string;
  evidence: string[];
}

export interface MemorySuggestionsView {
  memories: MemorySuggestion[];
  skills: SkillSuggestion[];
  generatedAt: string;
  available: boolean;
  source: string;
}

export interface MemoryView {
  docs: MemoryDoc[];
  facts: MemoryFact[];
  archives: MemoryArchive[];
  scopes: MemoryScope[];
  instructionDiagnostics: InstructionDiagnostic[];
  conflicts: MemoryConflict[];
  lastRecall: MemoryRecallTrace;
  storeDir: string;
  storeGlobalDir?: string;
  available: boolean;
}

// SettingsTab is the top-level navigation item in the Settings Centre modal.
export type SettingsTab = "general" | "models" | "providers" | "bots" | "mcp" | "remote" | "skills" | "subagents" | "plugins" | "memory" | "hooks" | "diagnostics" | "shortcuts" | "permissions" | "sandbox" | "network" | "appearance" | "storage" | "updates";

/** Extension runtime doctor report from App.RuntimeDoctor. */
export interface RuntimeDoctorReport {
  text: string;
  publishedGeneration: number;
  allowResume: boolean;
  cleanRollback: boolean;
  hasIrreversible: boolean;
  noOpRebuilds: number;
  fullRebuilds: number;
  subgraphRebuilds: number;
  staleDrops: number;
  admissionRejected: number; runtimeOwnerFallbacks: number;
}

/** Capability diagnostics report from App.CapabilityDiagnostics (capdiag.Report). */
export interface CapabilityDiagnosticsReport {
  schema_version: number;
  root: string;
  live: boolean;
  summary: {
    errors: number;
    warnings: number;
    infos: number;
    instructions: number;
    skills: number;
    commands: number;
    hooks: number;
    plugins: number;
    mcp_servers: number;
  };
  instructions: { docs: Array<{ path: string; scope: string; directory?: string; depth: number; order: number }> };
  skills: CapabilityAssetReport;
  commands: CapabilityAssetReport;
  hooks: {
    trusted_project: boolean;
    project_defines_hooks: boolean;
    sources: Array<{ scope: string; path: string; status: string; hook_count: number; parse_error?: string }>;
    entries: Array<{
      event: string;
      match?: string;
      command?: string;
      context_file?: string;
      description?: string;
      timeout_ms?: number;
      scope: string;
      source: string;
      blocking: boolean;
    }>;
  };
  plugins: {
    state_path?: string;
    packages: Array<{
      name: string;
      enabled: boolean;
      version?: string;
      root: string;
      manifest_kind?: string;
      skills: number;
      commands: number;
      hooks: number;
      mcp_servers: number;
      warnings?: string[];
      status: string;
    }>;
  };
  mcp: {
    servers: Array<{
      name: string;
      source?: string;
      package_owner?: string;
      transport: string;
      start_intent: string;
      command?: string;
      url_host?: string;
      env_keys?: string[];
      header_keys?: string[];
      runtime_status?: string;
      tool_count?: number;
      tools?: Array<{ name: string; read_only_hint?: boolean }>;
      error?: string;
    }>;
  };
  issues: CapabilityIssue[];
}

export interface CapabilityAssetReport {
  roots: Array<{ path: string; scope?: string; status: string }>;
  entries: Array<{
    name: string;
    description?: string;
    scope?: string;
    path: string;
    status: string;
    winner_path?: string;
    error?: string;
    run_as?: string;
  }>;
  winners: number;
  shadowed: number;
  disabled?: number;
  parse_errors?: number;
}

export interface CapabilityIssue {
  severity: "error" | "warning" | "info" | string;
  code: string;
  subsystem: string;
  name?: string;
  source?: string;
  message: string;
  remediation?: string;
  settings_tab?: string;
}
// Settings panel payloads (desktop/settings_app.go).
export interface ProviderView {
  name: string;
  builtIn: boolean;
  added: boolean;
  kind: string;
  baseUrl: string;
  chatUrl?: string; // legacy OpenAI chat endpoint override; preserved for old-config compatibility
  requestUrl?: string; // exact provider request URL written by the current settings UI
  models: string[];
  visionModels: string[]; // legacy subset; new UI derives capability from modelOverrides
  visionModelsConfigured: boolean; // legacy explicit-list marker retained for old configs
  visionCapability?: "configurable" | "unsupported"; // backend authority; absent on older Wails payloads
  modelsUrl: string; // optional override for model discovery; empty derives from baseUrl
  default: string;
  apiKeyEnv: string;
  headers?: Record<string, string> | null; // optional extra request headers for compatible gateways
  extraBody?: Record<string, unknown> | null; // optional extra top-level request body fields for compatible gateways
  authHeader?: boolean; // Anthropic-compatible: send Authorization: Bearer instead of x-api-key
  noProxy?: boolean; // reach this provider's endpoint directly, bypassing the configured/system proxy
  keySet: boolean; // the env var currently resolves to a value
  requiresKey?: boolean; // false for explicit no-auth providers
  configured?: boolean; // selectable: key is set or no key is required
  keySource?: string;
  keySourcePath?: string;
  balanceUrl: string; // optional wallet-balance endpoint; "" disables the readout
  contextWindow: number;
  reasoningProtocol: string; // auto|deepseek|glm|kimi-k3|openai|none; empty = auto/model registry
  thinking: string; // provider-specific thinking override: ""|enabled|disabled|adaptive
  webSearch?: boolean; // expose a provider-executed web search tool when supported
  serverWebSearchCapability?: boolean; // backend-verified provider capability; absent on older Wails payloads
  supportedEfforts: string[]; // custom /effort levels; empty = use built-in Kind/BaseURL default
  defaultEffort: string; // /effort level when user picks "auto" or unset; "" = supportedEfforts[0]
  modelOverrides?: ProviderModelOverrideView[] | null;
  modelCapabilities?: ProviderModelCapabilityView[] | null;
  recommendedUpgradeAvailable?: boolean; // official legacy OpenAI entry can switch to recommended Anthropic access
  modelCatalogFingerprint?: string; // opaque compare-and-apply token for background model discovery
}

export interface ProviderModelCatalogUpdate {
  name: string;
  expectedFingerprint: string;
  models: string[];
  default: string;
  visionModels: string[];
  modelCapabilities?: ProviderModelCapabilityUpdate[];
}

export interface ProviderModelCapabilityView {
	automaticState?: string;
	automaticSource?: string;
	imageInputEnableAllowed?: boolean;
	imageInputBlockReason?: string;
  model: string;
  inputModalities: string[];
  state: "supported" | "unsupported" | "unknown" | string;
  source: string;
}

export interface ProviderModelCapabilityUpdate {
  model: string;
  inputModalities: string[];
}

export interface ProviderPresetView {
  id: string;
  label: string;
  description: string;
  keyEnv: string;
  recommended?: boolean;
  billingMode?: string;
  displayGroup?: string;
  displaySection?: string;
  displayTier?: "primary" | "advanced" | "compatibility" | string;
  routeKind?: string;
  optional?: boolean;
  displayOrder?: number;
  providerNames: string[];
  models: string[];
  added: boolean;
  status?: "available" | "installed" | "installed_modified" | "partial" | "name_conflict" | "similar_existing";
  statusProviderNames?: string[];
  missingProviderNames?: string[];
  keySet: boolean;
  requiresKey?: boolean;
  configured?: boolean;
  keySource?: string;
  keySourcePath?: string;
}

export interface ProviderModelOverrideView {
  model: string;
  reasoningProtocol: string;
  supportedEfforts: string[];
  defaultEffort: string;
  vision?: boolean | null;
  contextWindow?: number;
  maxOutputTokens?: number;
}

// BalanceInfo is the wallet-balance readout (desktop/app.go Balance). available
// is false when the provider declares no balanceUrl or a fetch failed; display is
// the formatted amount in an original wallet currency; no implicit FX conversion.
export interface BalanceInfo {
  available: boolean;
  display: string;
  detail?: string;
  complete?: boolean;
  rateDate?: string;
  approx?: boolean;
  currencies?: string[];
  primaryCurrency?: string;
  costDisplayCurrency?: string;
  multiCurrency?: boolean;
  err?: string;
}

// ── Usage statistics (desktop/stats_app.go) ────────────────────────────────

// UsageStatsRequest selects the aggregation range and optional entry-point
// filter for the usage statistics panel. Range is "7" | "14" | "30" | "90" |
// "custom"; custom requires from/to as "2006-01-02" (inclusive, local dates).
// Source "" or "all" aggregates every entry point; "desktop" | "cli" | "serve"
// | "bot" | "remote" filters to that source's records.
export interface UsageStatsRequest {
  range: string;
  from?: string;
  to?: string;
  source?: string;
}

// DailyTokenUsage is one day's token total, per-model split and turn count in
// the daily trend series.
export interface DailyTokenUsage {
  day: string; // "2006-01-02"
  total: number;
  byModel: Record<string, number>; // model ref -> tokens
  byProvider: Record<string, number>; // provider name -> tokens
  requests: number; // API calls that day
  turns: number;
  cacheHit: number; // cached input tokens that day
  cacheMiss: number; // uncached input tokens that day
}

// ModelTokenUsage is one model's aggregate within the range.
export interface ModelTokenUsage {
  model: string; // canonical "provider/model"
  provider: string;
  tokens: number;
  percent: number; // 0..100
}

// ProviderTokenUsage is one provider's aggregate within the range.
export interface ProviderTokenUsage {
  provider: string;
  tokens: number;
  percent: number;
}

// UsageStatsRange is the full aggregate the settings panel renders.
export interface UsageStatsRange {
  from: string;
  to: string;
  tokens: number;
  requests: number; // API calls
  turns: number; // completed turns
  cacheHit: number;
  cacheMiss: number;
  activeDays: number;
  topModel: string;
  topProvider: string;
  daily: DailyTokenUsage[];
  models: ModelTokenUsage[];
  providers: ProviderTokenUsage[];
}

// JobView is one running background job (desktop/app.go Jobs) for the status bar.
export interface JobView {
  id: string;
  kind: string; // "bash" | "task"
  label: string;
  status: string; // "running"
  startedAt: number; // unix milliseconds
}

export interface ActiveWorkView {
  running: boolean;
  pendingPrompt: boolean;
  cancellable: boolean;
  jobs: JobView[];
}

export interface JobCancelBatchView {
  cancelled: string[];
  notRunning: string[];
}

export interface BackgroundRuntimeView {
  tabId: string;
  title: string;
  detached: boolean;
  running: boolean;
  pendingPrompt: boolean;
  jobs: JobView[];
}

export interface WorkspaceConflictView {
  state: "none" | "local" | "external";
  ownerTabId?: string; ownerTitle?: string;
  ownerScope?: string; ownerLabel?: string;
  ownerWork: ActiveWorkView;
  canReveal: boolean;
  canCreateWorktree: boolean;
}

export interface PermissionsView {
  mode: string; // "ask" | "allow" | "deny"
  allow: string[];
  ask: string[];
  deny: string[];
}

export interface SandboxView {
  bash: string; // "enforce" | "off"
  network: boolean;
  workspaceRoot: string;
  allowWrite: string[];
  effectiveWorkspaceRoot: string;
  effectiveWriteRoots: string[];
  shell: string; // "auto" | "bash" | "powershell" | "pwsh"
  effectiveShell?: string; // bound shell: "bash" | "zsh" | "sh" | "git-bash" | "powershell" | "pwsh"
  resolvedShell?: string; // shell a reload would pick now
  shellReloadRequired?: boolean;
  shellCapabilities: ShellCapabilityView[];
  gitCapability?: ShellCapabilityView | null;
  shellInstallAction?: ShellInstallActionView | null;
  shellRepairGuidance?: { manager: string; command?: string } | null;
  gitRepairGuidance?: { manager: string; command?: string } | null;
}
// One discovered interpreter: usable on this host, where, and why not.
export interface ShellCapabilityView {
  id: string; // "bash" | "zsh" | "sh" | "git-bash" | "powershell" | "pwsh"
  variant?: string;
  available: boolean;
  path?: string;
  source?: string;
  reason?: string;
}
// Optional manual repair action the sandbox section may offer (Windows only).
export interface ShellInstallActionView {
  id: string; // "git-for-windows"
  mode: string; // "manual"
  available: boolean;
  manualUrl?: string;
}
// Structured outcome of InstallShellSupport.
export interface ShellInstallResult {
  status: string; // "manual_required" | "unsupported_platform"
  path?: string;
  reason?: string;
  manualUrl?: string;
}

export interface NetworkProxyView {
  type: string;
  server: string;
  port: number;
  username: string;
  password: string;
}

export interface NetworkView {
  proxyMode: string; // "auto" | "custom" | "off" (backend may still return legacy "env")
  proxyUrl: string;
  noProxy: string;
  proxy: NetworkProxyView;
}

export interface AgentView {
  temperature: number;
  maxSteps: number;
  plannerMaxSteps: number;
  maxSubagentDepth: number;
  maxSubagentConcurrency: number;
  maxParallelWriters: number;
  systemPrompt: string;
  reasoningLanguage: string; // "auto" | "zh" | "en"
  compactRatio?: number; // Advanced global default; older backends omit it.
  effectiveCompactRatio?: number; // Active local session after project overrides.
  compactRatioOverridden?: boolean;
}

export interface BotAllowlistView {
  enabled: boolean;
  allowAll: boolean;
  qqUsers: string[];
  feishuUsers: string[];
  weixinUsers: string[];
  qqApprovers: string[];
  feishuApprovers: string[];
  weixinApprovers: string[];
  qqAdmins: string[];
  feishuAdmins: string[];
  weixinAdmins: string[];
  qqGroups: string[];
  feishuGroups: string[];
  weixinGroups: string[];
  dingtalkUsers: string[];
  dingtalkApprovers: string[];
  dingtalkAdmins: string[];
  dingtalkGroups: string[];
}

export interface BotAccessView {
  enabled: boolean;
  allowAll: boolean;
  pairingEnabled: boolean;
  users: string[];
  groups: string[];
  approvers: string[];
  admins: string[];
}

export interface BotSelfUserIDsView {
  qq: string[];
  feishu: string[];
  weixin: string[];
  dingtalk: string[];
}

export interface BotPairingView {
  enabled: boolean;
  requestTtlMinutes: number;
  maxPendingPerPlatform: number;
}

export interface BotControlView {
  enabled: boolean;
  addr: string;
  tokenEnv: string;
}

export interface BotRouteView {
  connectionId: string;
  platform: string;
  chatType: string;
  chatId: string;
  userId: string;
  threadId: string;
  model: string;
  toolApprovalMode: ToolApprovalMode | "" | string;
  workspaceRoot: string;
}

export interface QQBotView {
  enabled: boolean;
  appId: string;
  appSecretEnv: string;
  secretSet: boolean;
  sandbox: boolean;
  model: string;
  toolApprovalMode: ToolApprovalMode | "" | string;
  workspaceRoot: string;
  access: BotAccessView;
}

export interface FeishuBotView {
  enabled: boolean;
  domain: string;
  appId: string;
  appSecretEnv: string;
  secretSet: boolean;
  verificationToken: string;
  mode: string;
  webhookPort: number;
  requireMention: boolean;
}

export interface WeixinBotView {
  enabled: boolean;
  accountId: string;
  tokenEnv: string;
  tokenSet: boolean;
  apiBase: string;
}

export interface DingtalkBotView {
  enabled: boolean;
  clientId: string;
  clientSecretEnv: string;
  secretSet: boolean;
  botName: string;
  requireMention: boolean;
  model: string;
  toolApprovalMode: string;
  workspaceRoot: string;
  access: BotAccessView;
}

export interface BotConnectionCredentialView {
  appId: string;
  appSecretEnv: string;
  accountId: string;
  tokenEnv: string;
  secretSet: boolean;
}

export interface BotConnectionSessionMappingView {
  remoteId: string;
  sessionId: string;
  sessionSource: string;
  chatType: string;
  userId: string;
  threadId: string;
  scope: "global" | "project" | string;
  workspaceRoot: string;
  updatedAt: string;
}

export interface BotConnectionView {
  id: string;
  provider: "qq" | "feishu" | "weixin" | string;
  domain: "qq" | "feishu" | "lark" | "weixin" | string;
  label: string;
  enabled: boolean;
  status: "disconnected" | "pending" | "connected" | "error" | string;
  model: string;
  toolApprovalMode: ToolApprovalMode | "" | string;
  workspaceRoot: string;
  access: BotAccessView;
  credential: BotConnectionCredentialView;
  sessionMappings: BotConnectionSessionMappingView[];
  lastError: string;
  createdAt: string;
  updatedAt: string;
}

export interface BotSettingsView {
  enabled: boolean;
  model: string;
  toolApprovalMode: ToolApprovalMode | "" | string;
  maxSteps: number;
  debounceMs: number;
  queueMode: string;
  queueCap: number;
  queueDrop: string;
  ignoreSelfMessages: boolean;
  selfUserIds: BotSelfUserIDsView;
  control: BotControlView;
  pairing: BotPairingView;
  routes: BotRouteView[];
  allowlist: BotAllowlistView;
  qq: QQBotView;
  feishu: FeishuBotView;
  weixin: WeixinBotView;
  dingtalk: DingtalkBotView;
  connections: BotConnectionView[];
}

export interface BotRuntimeStatusView {
  running: boolean;
  status: string;
  message: string;
  connections: number;
  startedAt: string;
  platforms: Record<string, string>;
}

export interface BotInstallStartResult {
  ok: boolean;
  provider: string;
  domain: string;
  installId: string;
  url: string;
  deviceCode: string;
  userCode: string;
  interval: number;
  expireIn: number;
  message: string;
}

export interface BotInstallPollResult {
  done: boolean;
  connection: BotConnectionView;
  status: string;
  message: string;
  error: string;
}

export interface HookConfigView {
  event: string;
  match?: string;
  command: string;
  description?: string;
  timeout?: number;
  cwd?: string;
}

export interface HooksSettingsView {
  scope: string;
  path: string;
  projectRoot: string;
  trusted: boolean;
  hooks: HookConfigView[];
  events: string[];
}

export interface BotConnectionDiagnostic {
  id: string;
  label: string;
  status: string;
  message: string;
  messageId: string;
  phase: string;
  code: string;
  reportKind: string;
  reportDetail: string;
  occurredAt: string;
}

export interface SettingsView {
  defaultModel: string;
  plannerModel: string;
  visionModel: string;
  subagentModel: string;
  subagentEffort: string;
  autoPlan: string;
  providers: ProviderView[];
  officialProviders: ProviderView[];
  providerPresets: ProviderPresetView[];
  permissions: PermissionsView;
  sandbox: SandboxView;
  network: NetworkView;
  agent: AgentView;
  bot: BotSettingsView;
  desktopLanguage: string; // "" | "en" | "zh"; empty = auto
  desktopCurrency?: string; // "" | "CNY" | "USD"; absent/empty = follow language
  desktopLayoutStyle: string; // "classic" | "workbench" | "creation"
  desktopTheme: string; // "auto" | "dark" | "light"
  desktopThemeStyle: string;
  desktopTerminalTheme: string; // "auto" follows app | "dark" | "light"
  closeBehavior: string; // "background" | "quit"
  displayMode: string; reasoningDisplayMode: string; reasoningDisplayModeExplicit?: boolean;
  statusBarStyle: string; // "icon" | "text"
  statusBarItems: string[]; // ordered visible status bar item ids
  defaultToolApprovalMode: ToolApprovalMode | string; // default for newly-created sessions
  checkUpdates: boolean; // check for new versions on startup
  updateChannel: string; // compatibility field; always "stable"
  telemetry: boolean; // anonymous launch ping + scrubbed next-launch native crash diagnostics
  metrics: boolean; // aggregate quality/lifecycle metrics (anonymous signal/bucket counts)
  configPath: string;
  shadowedByPath?: string; // workspace reasonix.toml that outranks configPath, when one exists
  providerKinds: string[]; // provider implementations the kernel registered (for the kind picker)
  autoApproveTools: boolean;
  bypass: boolean; // legacy JSON key for live YOLO/full-access tool auto-approval
  conversationWidth?: string; // "standard" | "full"; absent from older Wails payloads
}

export interface DesktopStartupSettingsView {
  bot: BotSettingsView;
  desktopLanguage: string; // "" | "en" | "zh"; empty = auto
  desktopLayoutStyle: string; // "classic" | "workbench"
  desktopTheme: string; // "auto" | "dark" | "light"
  desktopThemeStyle: string;
  desktopTerminalTheme: string; // "auto" follows app | "dark" | "light"
  displayMode: string; reasoningDisplayMode: string; reasoningDisplayModeExplicit?: boolean;
  statusBarStyle: string; // "icon" | "text"
  statusBarItems: string[]; // ordered visible status bar item ids
  checkUpdates: boolean; // check for new versions on startup
  updateChannel: string; // compatibility field; always "stable"
  conversationWidth?: string; // "standard" | "full"; absent from older Wails payloads
  configWarnings?: string[]; configWarningsRevision?: number; // load recovery notices and async delivery barrier
  configPath?: string;
}

export type ExternalOpenerKind = "file-manager" | "editor" | "terminal";

export interface ExternalOpenerView {
  id: string;
  name: string;
  kind: ExternalOpenerKind;
  iconDataUrl?: string;
}

export interface ExternalOpenersView {
  openers: ExternalOpenerView[];
  preferred: string; workspaceOpenable?: boolean;
}

// Auto-updater payloads (desktop/updater.go). UpdateInfo drives the update banner;
// UpdateProgress streams on the "updater:progress" event during download/install.
export interface UpdateInfo {
  available: boolean;
  current: string;
  latest: string;
  notes: string;
  channel: string;
  canSelfUpdate: boolean; // macOS true only for signed/notarized builds
  manualOnly?: boolean;
  manualReason?: string;
  installMode?: "portable" | "deb" | "manual" | string;
  requiresElevation?: boolean;
  downloaded: boolean;
  downloadUrl: string; // human-facing releases page (macOS path / fallback link)
  assetSize: number; // running platform's artifact size, for the progress bar
  err?: string; // set when the check itself failed (both endpoints down)
}

export interface UpdateDownloadResult {
  requestId: string;
  version: string;
  channel: string;
  path: string;
  size: number;
  sha256: string;
}

export interface UpdateProgress {
  requestId: string;
  version: string;
  channel: "stable" | "preview" | string;
  phase: "downloading" | "verifying" | "downloaded" | "authorizing" | "recovering" | "installing" | "relaunching" | "done" | "error";
  received: number;
  total: number;
  err?: string;
}

// Task Monitor panel types (internal/taskmonitor).

export type TaskState =
  | "queued"
  | "running"
  | "waiting"
  | "succeeded"
  | "failed"
  | "cancelled"
  | "stale"
  | string; // forward-compat

export type RuntimeState = "unknown" | "alive" | "exited" | string;

export interface TaskSnapshot {
  schema_version: number;
  task_id: string;
  job_id?: string; // jobs.Manager-local runtime identifier
  session_id: string;
  state: TaskState;
  runtime_state?: RuntimeState; // absent in snapshots written before this field existed
  version: number;
  created_at: string; // ISO 8601
  updated_at: string; // ISO 8601
  error_code?: string;
  error_summary?: string;
}

export type { TaskActionRequest, TaskCatalogItem, TaskCatalogStatus, TaskEventPage, TaskEventPageRequest, TaskPage, TaskPageRequest } from "./taskCatalogTypes";

export interface ControlResult {
  schema_version: number;
  command: string;
  task_id: string;
  session_id?: string;
  state?: TaskState;
  runtime_state?: RuntimeState;
  version?: number;
  accepted: boolean;
  idempotent: boolean;
  error?: { code: string; message: string };
}

export interface TaskEvent {
  sequence: number;
  timestamp: string; // ISO 8601
  event_type: string;
  task_id: string;
  session_id: string;
  state: TaskState;
  runtime_state?: RuntimeState;
  error_code?: string;
  error_summary?: string;
}
