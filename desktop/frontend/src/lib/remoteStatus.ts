import type { CheckpointMeta, EffortInfo, GoalRuntime, GoalStatus, QualityFloor, ToolApprovalMode } from "./types";

// Raw /status payload mapping for the remote session surface. The serve reports
// the fields it knows; everything else stays undefined so callers keep prior
// values. These helpers are pure shape guards shared by the hydration,
// reconciliation, and watchdog paths in useRemoteSession.

// remoteStatusToAction maps the serve's raw /status payload onto the shared
// backend_status action so the remote surface reuses the local tab's running
// reconciliation (including its staleness guards). The serve reports the
// fields it knows; the rest stay undefined and the reducer keeps prior values.
export function remoteStatusToAction(status: unknown, snapshotAt: number, previousRunning = false) {
  const raw = (status ?? null) as { running?: unknown; pendingPrompt?: unknown; backgroundJobs?: unknown; cancelRequested?: unknown; cancellable?: unknown } | null;
  return {
    type: "backend_status" as const,
    running: typeof raw?.running === "boolean" ? raw.running : previousRunning,
    pendingPrompt: raw?.pendingPrompt === undefined ? undefined : raw.pendingPrompt === true,
    backgroundJobs: typeof raw?.backgroundJobs === "number" ? raw.backgroundJobs : undefined,
    cancelRequested: raw?.cancelRequested === undefined ? undefined : raw.cancelRequested === true,
    cancellable: raw?.cancellable === undefined ? undefined : raw.cancellable === true,
    snapshotAt,
  };
}

export type RemoteStatus = {
  running?: unknown;
  pendingPrompt?: unknown;
  label?: unknown;
  plan?: unknown;
  toolApprovalMode?: unknown;
  goal?: unknown;
  goalStatus?: unknown;
  effort?: unknown;
  used?: unknown;
  window?: unknown;
  cacheHit?: unknown;
  cacheMiss?: unknown;
  lastUsage?: unknown;
  balance?: unknown;
  sessionCostQuote?: unknown;
  jobs?: unknown;
  qualityFloor?: unknown;
  sessionName?: unknown;
  goalRuntime?: unknown;
};

export function isAuthoritativeRemoteStatus(status: unknown): status is RemoteStatus {
  if (!status || typeof status !== "object" || Array.isArray(status)) return false;
  const raw = status as RemoteStatus;
  return typeof raw.plan === "boolean"
    && (raw.toolApprovalMode === "ask" || raw.toolApprovalMode === "auto" || raw.toolApprovalMode === "yolo")
    && typeof raw.goal === "string";
}

export function remoteComposerState(status: unknown) {
  const raw = (status ?? null) as RemoteStatus | null;
  const goal = typeof raw?.goal === "string" ? raw.goal.trim() : "";
  const toolApprovalMode: ToolApprovalMode = raw?.toolApprovalMode === "auto" || raw?.toolApprovalMode === "yolo"
    ? raw.toolApprovalMode
    : "ask";
  const rawGoalStatus = raw?.goalStatus;
  const goalStatus: GoalStatus | undefined = rawGoalStatus === "running" || rawGoalStatus === "complete"
    || rawGoalStatus === "blocked" || rawGoalStatus === "stopped" ? rawGoalStatus : undefined;
  const effort = raw?.effort as Partial<EffortInfo> | undefined;
  const qualityFloor: QualityFloor = raw?.qualityFloor === "delivery" ? "delivery" : "standard";
  return {
    modelLabel: typeof raw?.label === "string" ? raw.label : "",
    composerProfile: {
      collaborationMode: goal ? "goal" as const : raw?.plan === true ? "plan" as const : "normal" as const,
      toolApprovalMode,
      goal,
      goalStatus,
      qualityFloor,
    },
    effort: effort && typeof effort.supported === "boolean"
      ? {
          supported: effort.supported,
          current: typeof effort.current === "string" ? effort.current : "auto",
          default: typeof effort.default === "string" ? effort.default : "",
          levels: Array.isArray(effort.levels) ? effort.levels.filter((level): level is string => typeof level === "string") : [],
        }
      : undefined,
  };
}

export function remoteGoalRuntime(status: unknown): GoalRuntime | undefined {
  const value = (status as RemoteStatus | null)?.goalRuntime;
  if (!value || typeof value !== "object") return undefined;
  const runtime = value as GoalRuntime;
  return typeof runtime.turnsUsed === "number" && typeof runtime.tokensUsed === "number" ? runtime : undefined;
}

export function remoteCheckpoints(value: unknown): CheckpointMeta[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((entry) => {
    const raw = (entry ?? null) as Record<string, unknown> | null;
    if (!raw || typeof raw.turn !== "number" || !Number.isFinite(raw.turn)) return [];
    const files = Array.isArray(raw.files)
      ? raw.files.filter((path): path is string => typeof path === "string")
      : [];
    const numericFileCount = typeof raw.files === "number" ? raw.files : raw.fileCount;
    const fileCount = typeof numericFileCount === "number" && Number.isFinite(numericFileCount)
      ? Math.max(0, numericFileCount)
      : files.length;
    return [{
      turn: raw.turn,
      prompt: typeof raw.prompt === "string" ? raw.prompt : "",
      files,
      fileCount,
      filesTruncated: raw.filesTruncated === true,
      turnFileCount: typeof raw.turnFileCount === "number" ? raw.turnFileCount : undefined,
      time: typeof raw.time === "number" ? raw.time : 0,
      canCode: raw.canCode === true,
      canConversation: raw.canConversation === true,
      coverage: typeof raw.coverage === "string" ? raw.coverage : undefined,
      coverageGaps: Array.isArray(raw.coverageGaps)
        ? raw.coverageGaps.filter((gap): gap is string => typeof gap === "string")
        : undefined,
      expiredFilePayload: raw.expiredFilePayload === true,
      activeWriters: typeof raw.activeWriters === "number" ? raw.activeWriters : undefined,
      legacy: raw.legacy === true,
      canUndoFiles: raw.canUndoFiles === true,
      disabledReason: typeof raw.disabledReason === "string" ? raw.disabledReason : undefined,
    }];
  });
}
