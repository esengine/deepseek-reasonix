import { asArray } from "./array";
import { app } from "./bridge";
import { recordFrontendDiagnostic } from "./frontendDiagnosticBridge";
import type { TurnEventEnvelope, TurnEventReplayView, WireEvent } from "./types";

type WireHandler = (event: WireEvent) => void;
type ResetHandler = (tabId: string, replay: TurnEventReplayView) => Promise<boolean>;

const MAX_REPLAY_PAGES = 32;
const MAX_QUEUED_EVENTS = 1024;
const MAX_QUEUED_BYTES = 8 * 1024 * 1024;

export interface TurnEventTransport {
  replay(tabId: string, afterSeq: number, identity?: TranscriptIdentity): Promise<TurnEventReplayView>;
}

export interface TranscriptIdentity {
  sessionId: string;
  headId: string;
  rewriteEpoch: number;
  runtimeEpoch: string;
}

export interface TranscriptSnapshotBoundary {
  protocolVersion: 1;
  snapshotId: string;
  identity: TranscriptIdentity;
  projectionRevision: number;
  coveredThroughSeq: number;
}

export interface SnapshotRequest {
  readonly tabId: string;
  readonly generation: number;
  readonly identity?: Readonly<TranscriptIdentity>;
}

function sameIdentity(a: Readonly<TranscriptIdentity>, b: Readonly<TranscriptIdentity>): boolean {
  return a.sessionId === b.sessionId && a.headId === b.headId &&
    a.rewriteEpoch === b.rewriteEpoch && a.runtimeEpoch === b.runtimeEpoch;
}

// TurnEventProjector is the per-tab ordered projection boundary. While a gap or
// checkpoint reset is being repaired, live events are held and applied only
// after the durable page and transcript prefix agree.
export class TurnEventProjector {
  private readonly sequenceByTab = new Map<string, number>();
  private readonly repairByTab = new Map<string, Promise<void>>();
  private readonly pendingRepairByTab = new Map<string, { afterSeq: number; runtimeEpoch?: string }>();
  private readonly gapQueueByTab = new Map<string, WireEvent[]>();
  private readonly queuedBytesByTab = new Map<string, number>();
  private readonly receivedThroughByTab = new Map<string, number>();
  private readonly receivedByEpoch = new Map<string, Map<string, number>>();
  private readonly epochByTab = new Map<string, string>();
  private readonly generationByTab = new Map<string, number>();
  private handler?: WireHandler;
  private resetHandler?: ResetHandler;
  private readonly snapshotRequestByTab = new Map<string, SnapshotRequest>();
  private readonly snapshotBoundaryByTab = new Map<string, TranscriptSnapshotBoundary>();

  constructor(private readonly transport?: TurnEventTransport) {}

  bind(handler: WireHandler) { this.handler = handler; }
  unbind(handler: WireHandler) { if (this.handler === handler) this.handler = undefined; }
  bindReset(handler: ResetHandler) { this.resetHandler = handler; }
  unbindReset(handler: ResetHandler) { if (this.resetHandler === handler) this.resetHandler = undefined; }

  release(tabId: string) {
    this.generationByTab.set(tabId, (this.generationByTab.get(tabId) ?? 0) + 1);
    this.sequenceByTab.delete(tabId);
    this.gapQueueByTab.delete(tabId);
    this.queuedBytesByTab.delete(tabId);
    this.receivedThroughByTab.delete(tabId);
    this.receivedByEpoch.delete(tabId);
    this.epochByTab.delete(tabId);
    this.pendingRepairByTab.delete(tabId);
    this.repairByTab.delete(tabId);
    this.snapshotRequestByTab.delete(tabId);
    this.snapshotBoundaryByTab.delete(tabId);
  }

  /** Suspend admission before issuing a snapshot request. The immutable lease
   * fences both its response and the live suffix to one session generation. */
  beginSnapshot(tabId: string, identity?: TranscriptIdentity): SnapshotRequest {
    const previous = this.snapshotBoundaryByTab.get(tabId)?.identity ?? this.snapshotRequestByTab.get(tabId)?.identity;
    if (previous && identity && !sameIdentity(previous, identity)) this.release(tabId);
    const generation = (this.generationByTab.get(tabId) ?? 0) + 1;
    this.generationByTab.set(tabId, generation);
    this.pendingRepairByTab.delete(tabId);
    this.repairByTab.delete(tabId);
    if (identity) this.epochByTab.set(tabId, identity.runtimeEpoch);
    const request = Object.freeze({ tabId, generation, identity: identity ? Object.freeze({ ...identity }) : undefined });
    this.snapshotRequestByTab.set(tabId, request);
    return request;
  }

  abortSnapshot(request: SnapshotRequest) {
    if (this.snapshotRequestByTab.get(request.tabId) !== request) return;
    // With no installed prefix, keep admission suspended until an explicit
    // retry supplies one. A failed fetch is not a new empty transcript.
    if (!this.snapshotBoundaryByTab.has(request.tabId)) return;
    this.snapshotRequestByTab.delete(request.tabId);
  }

  /** The caller commits rows and runtime in one synchronous store transaction.
   * Coverage changes only after that transaction returns successfully. */
  installSnapshot(request: SnapshotRequest, snapshot: TranscriptSnapshotBoundary, commit: () => void): boolean {
    const { tabId, generation } = request;
    if (this.snapshotRequestByTab.get(tabId) !== request || this.generationByTab.get(tabId) !== generation) return false;
    if (snapshot.protocolVersion !== 1 || !snapshot.snapshotId || (request.identity && !sameIdentity(request.identity, snapshot.identity)) ||
      !Number.isSafeInteger(snapshot.coveredThroughSeq) || snapshot.coveredThroughSeq < 0 ||
      !Number.isSafeInteger(snapshot.projectionRevision) || snapshot.projectionRevision < 0) {
      throw new Error("invalid transcript snapshot boundary");
    }
    const previous = this.snapshotBoundaryByTab.get(tabId);
    if (previous && sameIdentity(previous.identity, snapshot.identity) &&
      (snapshot.coveredThroughSeq < (this.sequenceByTab.get(tabId) ?? 0) || snapshot.projectionRevision < previous.projectionRevision)) {
      throw new Error("transcript snapshot would roll back committed coverage");
    }
    commit();
    if (this.snapshotRequestByTab.get(tabId) !== request || this.generationByTab.get(tabId) !== generation) return false;
    this.snapshotBoundaryByTab.set(tabId, { ...snapshot, identity: { ...snapshot.identity } });
    this.epochByTab.set(tabId, snapshot.identity.runtimeEpoch);
    this.sequenceByTab.set(tabId, snapshot.coveredThroughSeq);
    this.snapshotRequestByTab.delete(tabId);
    const remaining = asArray(this.gapQueueByTab.get(tabId)).filter((event) =>
      (event.seq ?? 0) > snapshot.coveredThroughSeq &&
      (!event.runtimeEpoch || event.runtimeEpoch === snapshot.identity.runtimeEpoch) &&
      (!event.sessionId || event.sessionId === snapshot.identity.sessionId));
    this.gapQueueByTab.set(tabId, remaining);
    this.queuedBytesByTab.set(tabId, remaining.reduce((sum, event) => sum + JSON.stringify(event).length * 2, 0));
    this.receivedThroughByTab.set(tabId, Math.max(snapshot.coveredThroughSeq,
      this.receivedByEpoch.get(tabId)?.get(snapshot.identity.runtimeEpoch) ?? 0,
      ...remaining.map((event) => event.seq ?? 0)));
    if ((this.receivedThroughByTab.get(tabId) ?? 0) > snapshot.coveredThroughSeq) {
      this.requestReplay(tabId, snapshot.coveredThroughSeq, snapshot.identity.runtimeEpoch);
    }
    return true;
  }

  snapshotBoundary(tabId: string): TranscriptSnapshotBoundary | undefined {
    const boundary = this.snapshotBoundaryByTab.get(tabId);
    return boundary ? { ...boundary, identity: { ...boundary.identity } } : undefined;
  }

  refresh(tabId: string) {
    if (!this.snapshotRequestByTab.has(tabId)) this.requestReplay(tabId, this.sequenceByTab.get(tabId) ?? 0, this.epochByTab.get(tabId));
  }

  observeRuntime(tabId: string, runtimeEpoch: string | undefined, latest: number, replayAfter: number | undefined, active: boolean) {
    // Modern snapshots own identity and initial coverage. Status polling is
    // only a high-water hint; it cannot install a different transcript prefix.
    if (this.snapshotRequestByTab.has(tabId) || this.snapshotBoundaryByTab.has(tabId)) {
      if (runtimeEpoch && runtimeEpoch !== this.epochByTab.get(tabId)) return;
      this.receivedThroughByTab.set(tabId, Math.max(this.receivedThroughByTab.get(tabId) ?? 0, latest));
      const projected = this.sequenceByTab.get(tabId) ?? 0;
      if (!this.snapshotRequestByTab.has(tabId) && latest > projected) this.requestReplay(tabId, projected, runtimeEpoch);
      return;
    }
    if (runtimeEpoch && runtimeEpoch !== this.epochByTab.get(tabId)) {
      this.generationByTab.set(tabId, (this.generationByTab.get(tabId) ?? 0) + 1);
      this.epochByTab.set(tabId, runtimeEpoch);
      this.sequenceByTab.delete(tabId);
      this.gapQueueByTab.delete(tabId);
      this.queuedBytesByTab.delete(tabId);
      this.receivedThroughByTab.delete(tabId);
      this.pendingRepairByTab.delete(tabId);
      this.repairByTab.delete(tabId);
    }
    let projected = this.sequenceByTab.get(tabId);
    if (projected === undefined) {
      projected = active ? Math.min(replayAfter ?? latest, latest) : latest;
      this.sequenceByTab.set(tabId, projected);
    }
    if (latest > projected) this.requestReplay(tabId, projected, runtimeEpoch);
  }

  receiveLive(tabId: string, event: WireEvent, runtimeEpoch?: string): boolean {
    const pendingSnapshot = this.snapshotRequestByTab.get(tabId);
    const unboundSnapshot = pendingSnapshot && !pendingSnapshot.identity;
    const epoch = this.epochByTab.get(tabId) ?? runtimeEpoch;
    if (!unboundSnapshot && epoch && event.runtimeEpoch && event.runtimeEpoch !== epoch) return false;
    if (typeof event.seq !== "number" || event.seq <= 0) {
      if (!this.handler) throw new Error("turn event projection has no commit handler");
      this.handler({ ...event, tabId });
      return true;
    }
    if (!Number.isSafeInteger(event.seq)) throw new Error("invalid live event sequence");
    const last = this.sequenceByTab.get(tabId) ?? 0;
    if (!unboundSnapshot && event.seq <= last) return false;
    const eventEpoch = event.runtimeEpoch ?? runtimeEpoch ?? epoch ?? "";
    const watermarks = this.receivedByEpoch.get(tabId) ?? new Map<string, number>();
    watermarks.set(eventEpoch, Math.max(watermarks.get(eventEpoch) ?? 0, event.seq));
    if (watermarks.size > 8) watermarks.delete(watermarks.keys().next().value!);
    this.receivedByEpoch.set(tabId, watermarks);
    this.receivedThroughByTab.set(tabId, Math.max(this.receivedThroughByTab.get(tabId) ?? 0, event.seq));
    if (this.snapshotRequestByTab.has(tabId) || this.repairByTab.has(tabId) || event.seq > last + 1) {
      const queued = this.gapQueueByTab.get(tabId) ?? [];
      if (!queued.some((pending) => pending.seq === event.seq)) {
        const bytes = JSON.stringify(event).length * 2;
        const priorBytes = this.queuedBytesByTab.get(tabId) ?? 0;
        if (queued.length >= MAX_QUEUED_EVENTS || priorBytes + bytes > MAX_QUEUED_BYTES) {
          // The ledger owns the source of truth. Releasing this redundant
          // buffer never advances coverage; replay will retrieve the suffix.
          queued.length = 0;
          this.queuedBytesByTab.set(tabId, 0);
          recordFrontendDiagnostic("runtime", "turn-events-live-buffer-overflow", {});
        }
        if (bytes <= MAX_QUEUED_BYTES) {
          queued.push({ ...event, tabId });
          this.queuedBytesByTab.set(tabId, (this.queuedBytesByTab.get(tabId) ?? 0) + bytes);
        }
      }
      this.gapQueueByTab.set(tabId, queued);
      if (!this.snapshotRequestByTab.has(tabId) && !this.repairByTab.has(tabId)) {
        this.requestReplay(tabId, last, event.runtimeEpoch ?? runtimeEpoch);
      }
      return false;
    }
    this.applyOrdered(tabId, event);
    return true;
  }

  /** The sole commit entry: callers must have established ordering first. */
  private applyOrdered(tabId: string, event: WireEvent) {
    const generation = this.generationByTab.get(tabId) ?? 0;
    if (!this.handler) throw new Error("turn event projection has no commit handler");
    this.handler({ ...event, tabId });
    if ((this.generationByTab.get(tabId) ?? 0) === generation && typeof event.seq === "number" && event.seq > 0) {
      this.sequenceByTab.set(tabId, event.seq);
    }
  }

  private requestReplay(tabId: string, afterSeq: number, runtimeEpoch?: string) {
    if (!this.transport && typeof app.TurnEventsForTab !== "function" && typeof app.TranscriptReplayForTab !== "function") return;
    if (this.repairByTab.has(tabId)) {
      this.pendingRepairByTab.set(tabId, { afterSeq, runtimeEpoch });
      return;
    }
    const generation = this.generationByTab.get(tabId) ?? 0;
    const repair = this.replayGap(tabId, afterSeq, runtimeEpoch, generation)
      .catch((error) => recordFrontendDiagnostic("runtime", "turn-events-gap-repair-failed", {
        afterSeq: this.sequenceByTab.get(tabId) ?? afterSeq,
        error: error instanceof Error ? error.message : String(error),
      }))
      .finally(() => {
        if (this.repairByTab.get(tabId) !== repair) return;
        this.repairByTab.delete(tabId);
        const pending = this.pendingRepairByTab.get(tabId);
        if (!pending) return;
        this.pendingRepairByTab.delete(tabId);
        this.requestReplay(tabId, pending.afterSeq, pending.runtimeEpoch);
      });
    this.repairByTab.set(tabId, repair);
  }

  private async replayGap(tabId: string, afterSeq: number, requestedEpoch: string | undefined, generation: number) {
    let cursor = afterSeq;
    for (let page = 0; page < MAX_REPLAY_PAGES; page += 1) {
      if ((this.generationByTab.get(tabId) ?? 0) !== generation) return;
      const identity = this.snapshotBoundaryByTab.get(tabId)?.identity;
      const replay = await (this.transport ? this.transport.replay(tabId, cursor, identity) :
        identity && app.TranscriptReplayForTab ? app.TranscriptReplayForTab(tabId, { identity, after: cursor }) : app.TurnEventsForTab!(tabId, cursor));
      if ((this.generationByTab.get(tabId) ?? 0) !== generation) return;
      const currentEpoch = this.epochByTab.get(tabId);
      if ((requestedEpoch && currentEpoch && requestedEpoch !== currentEpoch) ||
        (!replay.resetRequired && replay.runtimeEpoch && currentEpoch && replay.runtimeEpoch !== currentEpoch)) {
        return;
      }

      if (replay.resetRequired) {
        const modern = this.snapshotBoundaryByTab.has(tabId);
        if (!this.resetHandler || !(await this.resetHandler(tabId, replay))) {
          throw new Error("turn event checkpoint reset could not hydrate the transcript");
        }
        if ((this.generationByTab.get(tabId) ?? 0) !== generation) return;
        if (modern) throw new Error("transcript reset did not install an authoritative snapshot");
        cursor = Math.max(0, replay.floorSeq - 1);
        this.sequenceByTab.set(tabId, cursor);
      }

      const envelopes = asArray(replay.events).slice().sort((a, b) => a.seq - b.seq);
      for (const envelope of envelopes) {
        if (envelope.seq <= cursor) continue;
        if (envelope.seq !== cursor + 1) throw new Error(`turn event replay gap after ${cursor}`);
        this.projectEnvelope(tabId, envelope, requestedEpoch);
        if ((this.generationByTab.get(tabId) ?? 0) !== generation) return;
        cursor = envelope.seq;
      }
      if (replay.hasMore) {
        const next = replay.nextAfterSeq;
        if (next !== cursor) throw new Error(`turn event replay cursor mismatch: projected ${cursor}, backend ${next}`);
        if (envelopes.length === 0) throw new Error("turn event replay made no progress");
        continue;
      }

      const pending = asArray(this.gapQueueByTab.get(tabId)).slice().sort((a, b) => (a.seq ?? 0) - (b.seq ?? 0));
      for (const live of pending) {
        if (typeof live.seq !== "number" || live.seq <= 0) {
          this.applyOrdered(tabId, live);
          continue;
        }
        if (live.seq <= cursor) continue;
        if (live.seq !== cursor + 1) {
          break;
        }
        this.applyOrdered(tabId, live);
        if ((this.generationByTab.get(tabId) ?? 0) !== generation) return;
        cursor = live.seq;
      }
      // Keep ownership of the queue until every successful commit is known.
      // A throwing reducer must leave the uncommitted suffix available, and a
      // reentrant arrival must not be overwritten by the page's older copy.
      const remaining = asArray(this.gapQueueByTab.get(tabId)).filter((live) => (live.seq ?? 0) > cursor);
      this.gapQueueByTab.set(tabId, remaining);
      this.queuedBytesByTab.set(tabId, remaining.reduce((total, live) => total + JSON.stringify(live).length * 2, 0));
      if (remaining.length === 0 && cursor >= (this.receivedThroughByTab.get(tabId) ?? 0)) return;
    }
    recordFrontendDiagnostic("runtime", "turn-events-gap-repair-incomplete", {
      afterSeq: this.sequenceByTab.get(tabId) ?? cursor,
    });
  }

  private projectEnvelope(tabId: string, envelope: TurnEventEnvelope, runtimeEpoch?: string) {
    const durable = envelope?.event;
    if (!durable || !Number.isSafeInteger(envelope.seq) || envelope.seq <= 0) throw new Error("invalid replay envelope");
    const epoch = this.epochByTab.get(tabId) ?? runtimeEpoch;
    if (envelope.runtimeEpoch && epoch && envelope.runtimeEpoch !== epoch) throw new Error("replay envelope belongs to another runtime");
    const identity = this.snapshotBoundaryByTab.get(tabId)?.identity;
    if (identity && envelope.sessionId && envelope.sessionId !== identity.sessionId) throw new Error("replay envelope belongs to another session");
    this.applyOrdered(tabId, {
        ...durable,
        sessionId: envelope.sessionId || durable.sessionId,
        submissionId: envelope.submissionId || durable.submissionId,
        turnId: envelope.turnId || durable.turnId,
        seq: envelope.seq,
        status: (envelope.status || durable.status) as WireEvent["status"],
        tabId,
        runtimeEpoch: envelope.runtimeEpoch ?? runtimeEpoch,
    });
  }
}
