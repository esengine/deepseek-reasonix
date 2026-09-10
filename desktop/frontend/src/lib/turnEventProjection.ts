import { asArray } from "./array";
import { app } from "./bridge";
import { recordFrontendDiagnostic } from "./frontendDiagnosticBridge";
import type { TurnEventEnvelope, TurnEventMeta, TurnEventReplayView, WireEvent } from "./types";

type WireHandler = (event: WireEvent, meta?: TurnEventMeta) => void;
type ResetHandler = (tabId: string, replay: TurnEventReplayView) => Promise<boolean>;
/** Told about every replay page as it lands, so a reader can state what the
 *  durable record covers without opening a second event path of its own. */
type ReplayObserver = (tabId: string, replay: TurnEventReplayView) => void;

const MAX_REPLAY_PAGES = 32;

// TurnEventProjector is the per-tab ordered projection boundary. While a gap or
// checkpoint reset is being repaired, live events are held and applied only
// after the durable page and transcript prefix agree.
export class TurnEventProjector {
  private readonly sequenceByTab = new Map<string, number>();
  private readonly repairByTab = new Map<string, Promise<void>>();
  private readonly pendingRepairByTab = new Map<string, { afterSeq: number; runtimeEpoch?: string }>();
  private readonly gapQueueByTab = new Map<string, WireEvent[]>();
  private readonly epochByTab = new Map<string, string>();
  private readonly generationByTab = new Map<string, number>();
  private readonly projectingReplayByTab = new Set<string>();
  private handler: WireHandler = () => {};
  private resetHandler?: ResetHandler;
  private replayObserver?: ReplayObserver;

  bind(handler: WireHandler) { this.handler = handler; }
  unbind(handler: WireHandler) { if (this.handler === handler) this.handler = () => {}; }
  bindReset(handler: ResetHandler) { this.resetHandler = handler; }
  unbindReset(handler: ResetHandler) { if (this.resetHandler === handler) this.resetHandler = undefined; }
  bindReplayObserver(observer: ReplayObserver) { this.replayObserver = observer; }
  unbindReplayObserver(observer: ReplayObserver) { if (this.replayObserver === observer) this.replayObserver = undefined; }

  release(tabId: string) {
    this.generationByTab.set(tabId, (this.generationByTab.get(tabId) ?? 0) + 1);
    this.sequenceByTab.delete(tabId);
    this.gapQueueByTab.delete(tabId);
    this.epochByTab.delete(tabId);
    this.projectingReplayByTab.delete(tabId);
    this.pendingRepairByTab.delete(tabId);
    this.repairByTab.delete(tabId);
  }

  observeRuntime(tabId: string, runtimeEpoch: string | undefined, latest: number, replayAfter: number | undefined, active: boolean) {
    if (runtimeEpoch && runtimeEpoch !== this.epochByTab.get(tabId)) {
      this.generationByTab.set(tabId, (this.generationByTab.get(tabId) ?? 0) + 1);
      this.epochByTab.set(tabId, runtimeEpoch);
      this.sequenceByTab.delete(tabId);
      this.gapQueueByTab.delete(tabId);
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

  acceptLive(tabId: string, event: WireEvent, runtimeEpoch?: string): boolean {
    if (this.projectingReplayByTab.has(tabId)) return true;
    if (typeof event.seq !== "number" || event.seq <= 0) return true;
    const last = this.sequenceByTab.get(tabId) ?? 0;
    if (event.seq <= last) return false;
    if (this.repairByTab.has(tabId) || event.seq > last + 1) {
      const queued = this.gapQueueByTab.get(tabId) ?? [];
      queued.push(event);
      this.gapQueueByTab.set(tabId, queued);
      if (!this.repairByTab.has(tabId)) {
        this.requestReplay(tabId, last, event.runtimeEpoch ?? runtimeEpoch);
      }
      return false;
    }
    this.sequenceByTab.set(tabId, event.seq);
    return true;
  }

  private requestReplay(tabId: string, afterSeq: number, runtimeEpoch?: string) {
    if (typeof app.TurnEventsForTab !== "function") return;
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
      const replay = await app.TurnEventsForTab!(tabId, cursor);
      if ((this.generationByTab.get(tabId) ?? 0) !== generation) return;
      const currentEpoch = this.epochByTab.get(tabId);
      if ((requestedEpoch && currentEpoch && requestedEpoch !== currentEpoch) ||
        (replay.runtimeEpoch && currentEpoch && replay.runtimeEpoch !== currentEpoch)) {
        return;
      }

      if (replay.resetRequired) {
        if (!this.resetHandler || !(await this.resetHandler(tabId, replay))) {
          throw new Error("turn event checkpoint reset could not hydrate the transcript");
        }
        if ((this.generationByTab.get(tabId) ?? 0) !== generation) return;
        cursor = Math.max(0, replay.floorSeq - 1);
        this.sequenceByTab.set(tabId, cursor);
      }

      // Coverage is published before the rows it describes, so no reader ever
      // spends a frame showing a prefix as if it were the whole record.
      this.replayObserver?.(tabId, replay);

      const envelopes = asArray(replay.events).slice().sort((a, b) => a.seq - b.seq);
      for (const envelope of envelopes) {
        if (envelope.seq <= cursor) continue;
        if (envelope.seq !== cursor + 1) throw new Error(`turn event replay gap after ${cursor}`);
        this.projectEnvelope(tabId, envelope, requestedEpoch);
        cursor = envelope.seq;
        this.sequenceByTab.set(tabId, cursor);
      }
      if (replay.hasMore) {
        const next = replay.nextAfterSeq;
        if (next !== cursor) throw new Error(`turn event replay cursor mismatch: projected ${cursor}, backend ${next}`);
        if (envelopes.length === 0) throw new Error("turn event replay made no progress");
        continue;
      }

      const pending = asArray(this.gapQueueByTab.get(tabId)).slice().sort((a, b) => (a.seq ?? 0) - (b.seq ?? 0));
      this.gapQueueByTab.delete(tabId);
      const remaining: WireEvent[] = [];
      for (const live of pending) {
        if (typeof live.seq !== "number" || live.seq <= 0) {
          this.handler(live);
          continue;
        }
        if (live.seq <= cursor) continue;
        if (live.seq !== cursor + 1) {
          remaining.push(live);
          continue;
        }
        this.handler(live);
        cursor = live.seq;
        this.sequenceByTab.set(tabId, cursor);
      }
      if (remaining.length === 0) return;
      this.gapQueueByTab.set(tabId, remaining);
    }
    recordFrontendDiagnostic("runtime", "turn-events-gap-repair-incomplete", {
      afterSeq: this.sequenceByTab.get(tabId) ?? cursor,
    });
  }

  private projectEnvelope(tabId: string, envelope: TurnEventEnvelope, runtimeEpoch?: string) {
    const durable = envelope?.event;
    if (!durable || typeof envelope.seq !== "number") return;
    this.projectingReplayByTab.add(tabId);
    try {
      this.handler({
        ...durable,
        turnId: envelope.turnId || durable.turnId,
        seq: envelope.seq,
        status: (envelope.status || durable.status) as WireEvent["status"],
        tabId,
        runtimeEpoch: envelope.runtimeEpoch ?? runtimeEpoch,
      }, { createdAt: envelope.createdAt });
    } finally {
      this.projectingReplayByTab.delete(tabId);
    }
  }
}
