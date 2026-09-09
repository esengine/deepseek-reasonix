/** Live-turn markers that a lagging history snapshot must not replace. */
export type HydrateLiveState = {
  running?: boolean;
  turnActive?: boolean;
  live?: unknown;
  currentAssistant?: unknown;
  pendingUser?: unknown;
  historyTotalTurns?: number;
  items: ReadonlyArray<{ kind: string; streaming?: boolean; status?: string }>;
  historyRevision?: number;
  historyDigest?: string;
};

export type HydratedHistoryApplyMode = "replace" | "prepend" | "skip";

export type HydrateProjection = {
  items: ReadonlyArray<unknown>;
  revision?: number;
  digest?: string;
};

export type SessionHydrateIdentity = {
  sessionPath?: string;
  sessionGeneration?: number;
};

export type HydrateSurfacePolicy = "preserve-current" | "replace-surface";

type ActiveTabHydrationTarget = SessionHydrateIdentity & {
  sessionRevision?: number;
  sessionDigest?: string;
};

export type ActiveTabHydrationLoadOptions = ActiveTabHydrationTarget & {
  preserveCachedHistory: boolean;
  surfacePolicy?: HydrateSurfacePolicy;
};

export function activeTabHydrationPlan(
  target: ActiveTabHydrationTarget,
  current: SessionHydrateIdentity | undefined,
  reset: boolean,
  requestedPolicy?: HydrateSurfacePolicy,
  requestedCache?: boolean,
): {
  sameSession: boolean;
  surfacePolicy: HydrateSurfacePolicy;
  loadOptions: ActiveTabHydrationLoadOptions;
} {
  const sameSession = sameSessionHydrateIdentity(target, current);
  const surfacePolicy = requestedPolicy ?? (sameSession ? "preserve-current" : "replace-surface");
  if (surfacePolicy === "replace-surface") {
    return {
      sameSession,
      surfacePolicy,
      loadOptions: {
        preserveCachedHistory: false,
        surfacePolicy,
        sessionPath: target.sessionPath,
        sessionRevision: target.sessionRevision,
        sessionDigest: target.sessionDigest,
        sessionGeneration: target.sessionGeneration,
      },
    };
  }
  return {
    sameSession,
    surfacePolicy,
    loadOptions: {
      preserveCachedHistory: sameSession && (requestedCache ?? !reset),
      sessionPath: target.sessionPath,
      sessionRevision: target.sessionRevision,
      sessionDigest: target.sessionDigest,
    },
  };
}

type UnboundLiveSurfaceState = HydrateLiveState & {
  hydrateHistoryLoaded?: boolean;
};

/** A surface may retain content only when the target session is provably the same. */
export function sameSessionHydrateIdentity(
  target: SessionHydrateIdentity | undefined,
  current: SessionHydrateIdentity | undefined,
): boolean {
  const targetPath = (target?.sessionPath ?? "").trim();
  const currentPath = (current?.sessionPath ?? "").trim();
  if (!targetPath || !currentPath || targetPath !== currentPath) return false;
  if (
    target?.sessionGeneration !== undefined &&
    current?.sessionGeneration !== undefined &&
    target.sessionGeneration !== current.sessionGeneration
  ) return false;
  return true;
}

/**
 * Adopt only a live runtime tail that has never been bound to persisted
 * history. This is the compatibility bridge for background runtime events
 * that predate the tab metadata snapshot: it must never retain a resident
 * transcript merely because the tab id matches.
 */
export function canAdoptUnboundLiveSurface(
  target: SessionHydrateIdentity | undefined,
  current: SessionHydrateIdentity | undefined,
  state: UnboundLiveSurfaceState | undefined,
  backendRunning: boolean,
  targetRuntimeEpoch?: string,
  currentRuntimeEpoch?: string,
): boolean {
  if (!backendRunning || !state) return false;
  if (!(target?.sessionPath ?? "").trim()) return false;
  if ((current?.sessionPath ?? "").trim()) return false;
  if (state.hydrateHistoryLoaded || (state.historyTotalTurns ?? 0) > 0) return false;
  if (state.historyRevision !== undefined || (state.historyDigest ?? "").trim()) return false;
  if (!state.running && !state.turnActive) return false;
  if (targetRuntimeEpoch && currentRuntimeEpoch && targetRuntimeEpoch !== currentRuntimeEpoch) return false;
  return Boolean(
    state.live ||
    state.currentAssistant ||
    state.pendingUser !== undefined ||
    state.items.some((item) =>
      (item.kind === "assistant" && item.streaming) ||
      (item.kind === "tool" && item.status === "running"),
    ),
  );
}

export function shouldPreferResidentHistory(reset: boolean, preserveCachedHistory?: boolean): boolean {
  return !reset && preserveCachedHistory !== false;
}

function sameHydrateFingerprint(state: HydrateLiveState | undefined, projection: HydrateProjection | undefined): boolean {
  if (!state || !projection) return false;
  const revision = projection.revision ?? 0;
  const digest = (projection.digest ?? "").trim();
  if (revision > 0 && state.historyRevision === revision) return true;
  if (digest !== "" && (state.historyDigest ?? "") === digest) return true;
  return false;
}

export function isStaleResidentProjection(
  state: HydrateLiveState | undefined,
  projection: HydrateProjection | undefined,
): boolean {
  if (!state || !projection || state.items.length === 0) return false;
  if (projection.items.length >= state.items.length) return false;
  return sameHydrateFingerprint(state, projection);
}

// A live turn is only "cached" once a history page has landed behind it.
// Without that, a session opened mid-stream reports a cached turn, skips the
// fetch, and streams over a blank transcript.
export function hasCachedLiveTurn(state: HydrateLiveState | undefined): boolean {
  if (!state?.running && !state?.turnActive) return false;
  if ((state.historyTotalTurns ?? 0) === 0) return false;
  if (state.live || state.currentAssistant || state.pendingUser !== undefined) return true;
  return state.items.some((item) =>
    (item.kind === "assistant" && item.streaming) ||
    (item.kind === "tool" && item.status === "running"),
  );
}

export function hasReusableCachedTranscript(
  state: (HydrateLiveState & { meta?: SessionHydrateIdentity }) | undefined,
  sessionPath?: string,
  revision?: number,
  digest?: string,
): boolean {
  if (!state || state.items.length === 0 || state.historyTotalTurns === 0) return false;
  const expectedSessionPath = (sessionPath ?? "").trim();
  if (!expectedSessionPath) return true;
  if ((state.meta?.sessionPath ?? "").trim() !== expectedSessionPath) return false;
  if (typeof revision === "number" && revision > 0) {
    return state.historyRevision === revision && (digest ?? "") === (state.historyDigest ?? "");
  }
  if ((digest ?? "").trim() !== "") return state.historyDigest === digest;
  // Missing backend fingerprints must not bless a resident page that already
  // has one; the sidecar may be between atomic replacements.
  return state.historyRevision === undefined && !state.historyDigest;
}

// An empty surface has to apply history or switch-back shows Welcome. A turn
// that has already streamed rows keeps them — but a tab with no history page
// behind it still gets one, prepended, instead of a blank transcript above the
// live turn. Only an already-hydrated live turn is left alone. An idle
// same-fingerprint resident page that is shorter than the visible transcript
// is skipped so Retry/clear cannot roll the chat back.
export function hydratedHistoryApplyMode(
  skipHistory: boolean,
  hasProjection: boolean,
  foregroundTurnActive: boolean,
  state: HydrateLiveState | undefined,
  projection?: HydrateProjection,
): HydratedHistoryApplyMode {
  if (skipHistory || !hasProjection) return "skip";
  if (!foregroundTurnActive) return isStaleResidentProjection(state, projection) ? "skip" : "replace";
  if ((state?.items.length ?? 0) === 0 && !hasCachedLiveTurn(state)) return "replace";
  return (state?.historyTotalTurns ?? 0) === 0 ? "prepend" : "skip";
}

type SignatureItem = {
  kind: string;
  id: string;
  messageId?: string;
  text?: string;
  reasoning?: string;
  name?: string;
  level?: string;
  trigger?: string;
  messages?: number;
  surfaceKey?: string;
  generation?: number;
};

function itemSignature(item: SignatureItem): string {
  switch (item.kind) {
    case "tool": return `tool|${item.id}|${item.name ?? ""}`;
    case "extension": return `extension|${item.surfaceKey ?? ""}|${item.generation ?? 0}`;
    case "compaction": return `compaction|${item.trigger ?? ""}|${item.messages ?? 0}`;
    default: return `${item.kind}|${item.level ?? ""}|${item.text ?? ""}|${item.reasoning ?? ""}`;
  }
}

// A page read while its turn is live can already carry rows the live stream
// rendered. Only a suffix of the page can overlap a prefix of the live rows, so
// the longest such match is the duplicate set.
export function duplicateLiveItemIds(
  pageItems: readonly SignatureItem[],
  liveItems: readonly SignatureItem[],
): string[] {
  // Stable backend identities are independent of where a page cuts the live
  // turn. In particular page [A,B,C] already covers live [A,B]. Content equality
  // cannot establish this relation: two messages may intentionally be equal.
  const identity = (item: SignatureItem) => item.messageId ? `m:${item.messageId}` : item.id;
  const isIdentified = (item: SignatureItem) => !!item.messageId || item.id.startsWith("m:");
  const canonicalIds = new Set(pageItems.filter((item) => isIdentified(item) || item.kind === "tool").map(identity));
  const identified = liveItems.filter((item) => canonicalIds.has(identity(item))).map((item) => item.id);
  if (pageItems.some(isIdentified) || liveItems.some(isIdentified)) return identified;
  for (let k = Math.min(pageItems.length, liveItems.length); k > 0; k -= 1) {
    let same = true;
    for (let i = 0; i < k && same; i += 1) {
      same = itemSignature(pageItems[pageItems.length - k + i]) === itemSignature(liveItems[i]);
    }
    if (same) return liveItems.slice(0, k).map((item) => item.id);
  }
  return [];
}

export function sameSessionPlaceholderItems<T>(
  target: SessionHydrateIdentity | undefined,
  prev: { meta?: SessionHydrateIdentity; items?: T[] } | undefined,
): T[] | undefined {
  return sameSessionHydrateIdentity(target, prev?.meta) ? prev?.items : undefined;
}
