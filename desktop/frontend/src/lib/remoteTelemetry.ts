import type { BalanceInfo, ContextInfo, CostQuote, JobView, WireUsage } from "./types";
import type { State } from "./useController";
import { app } from "./bridge";

type RemoteTelemetryStatus = {
  used?: unknown;
  window?: unknown;
  cacheHit?: unknown;
  cacheMiss?: unknown;
  lastUsage?: unknown;
  balance?: unknown;
  sessionCostQuote?: unknown;
  jobs?: unknown;
};

async function retryRemote<T>(load: () => Promise<T>, maxAttempts: number, cancelled: () => boolean): Promise<T | undefined> {
  for (let attempt = 0; attempt < maxAttempts && !cancelled(); attempt++) {
    try {
      return await load();
    } catch (error) {
      if (attempt + 1 === maxAttempts) throw error;
      await new Promise<void>((resolve) => setTimeout(resolve, 500));
    }
  }
}

export function loadRemoteStatusSnapshot(tabId: string, maxAttempts: number, cancelled: () => boolean, valid: (status: unknown) => boolean, metadataOnly = false) {
  return retryRemote(async () => {
    const snapshot = metadataOnly && app.RemoteTabMetadata ? await app.RemoteTabMetadata(tabId) : await app.RemoteTabSnapshot(tabId);
    const status = snapshot.status ?? await app.RemoteTabStatus(tabId);
    if (!valid(status)) throw new Error("remote status is incomplete");
    return [snapshot, status] as const;
  }, maxAttempts, cancelled);
}

function finiteNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function remoteUsage(value: unknown): WireUsage | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const raw = value as Record<string, unknown>;
  const number = (camel: string, legacy: string) => finiteNumber(raw[camel] ?? raw[legacy]) ?? 0;
  return {
    promptTokens: number("promptTokens", "PromptTokens"),
    completionTokens: number("completionTokens", "CompletionTokens"),
    totalTokens: number("totalTokens", "TotalTokens"),
    cacheHitTokens: number("cacheHitTokens", "CacheHitTokens"),
    cacheMissTokens: number("cacheMissTokens", "CacheMissTokens"),
    reasoningTokens: number("reasoningTokens", "ReasoningTokens"),
    estimated: raw.estimated === true || raw.Estimated === true,
    sessionCacheHitTokens: number("sessionCacheHitTokens", "SessionCacheHitTokens"),
    sessionCacheMissTokens: number("sessionCacheMissTokens", "SessionCacheMissTokens"),
    contextPromptTokens: number("contextPromptTokens", "ContextPromptTokens") || undefined,
    contextCompletionTokens: number("contextCompletionTokens", "ContextCompletionTokens") || undefined,
    contextReasoningTokens: number("contextReasoningTokens", "ContextReasoningTokens") || undefined,
    contextCacheHitTokens: number("contextCacheHitTokens", "ContextCacheHitTokens") || undefined,
    contextCacheMissTokens: number("contextCacheMissTokens", "ContextCacheMissTokens") || undefined,
  };
}

function remoteBalance(value: unknown): BalanceInfo | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const raw = value as Record<string, unknown>;
  return {
    available: raw.available === true,
    display: typeof raw.display === "string" ? raw.display : "",
    detail: typeof raw.detail === "string" ? raw.detail : undefined,
    complete: raw.complete === true,
    primaryCurrency: typeof raw.primaryCurrency === "string" ? raw.primaryCurrency : undefined,
    costDisplayCurrency: typeof raw.costDisplayCurrency === "string" ? raw.costDisplayCurrency : undefined,
    multiCurrency: raw.multiCurrency === true,
  };
}

function remoteJobs(value: unknown): JobView[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((entry) => {
    if (!entry || typeof entry !== "object" || Array.isArray(entry)) return [];
    const raw = entry as Record<string, unknown>;
    if (typeof raw.id !== "string" || raw.id.trim() === "") return [];
    return [{
      id: raw.id,
      kind: typeof raw.kind === "string" ? raw.kind : "task",
      label: typeof raw.label === "string" ? raw.label : raw.id,
      status: typeof raw.status === "string" ? raw.status : "running",
      startedAt: finiteNumber(raw.startedAt) ?? 0,
    }];
  });
}

export function hydrateRemoteTelemetry(state: State, status: unknown): State {
  const raw = (status ?? {}) as RemoteTelemetryStatus;
  const lastUsage = remoteUsage(raw.lastUsage);
  const quote = raw.sessionCostQuote && typeof raw.sessionCostQuote === "object" && !Array.isArray(raw.sessionCostQuote)
    ? raw.sessionCostQuote as CostQuote
    : undefined;
  const selectedAmount = quote?.selected ? Number.parseFloat(quote.selected.amount) : 0;
  const cacheHit = finiteNumber(raw.cacheHit) ?? lastUsage?.sessionCacheHitTokens ?? 0;
  const cacheMiss = finiteNumber(raw.cacheMiss) ?? lastUsage?.sessionCacheMissTokens ?? 0;
  const context: ContextInfo = {
    used: Math.max(0, finiteNumber(raw.used) ?? 0),
    window: Math.max(0, finiteNumber(raw.window) ?? 0),
    sessionTokens: Math.max(0, cacheHit + cacheMiss, lastUsage?.totalTokens ?? 0),
    cacheHitTokens: Math.max(0, cacheHit),
    cacheMissTokens: Math.max(0, cacheMiss),
    sessionCost: Number.isFinite(selectedAmount) ? Math.max(0, selectedAmount) : 0,
    sessionCurrency: quote?.selected?.currency ?? "",
    sessionCostComplete: quote?.costComplete === true,
    sessionCostQuote: quote,
    estimated: quote?.estimated === true || lastUsage?.estimated === true,
  };
  return {
    ...state,
    context,
    balance: remoteBalance(raw.balance),
    jobs: remoteJobs(raw.jobs),
    usage: lastUsage,
    sessionTokens: context.sessionTokens ?? 0,
    sessionCost: context.sessionCost ?? 0,
    sessionCurrency: context.sessionCurrency ?? "",
    lastTurnOutputTokens: lastUsage ? Math.max(0, lastUsage.completionTokens) : 0,
    lastTurnOutputEstimated: lastUsage?.estimated === true,
  };
}
