import type { TranscriptContentChunk, TranscriptContentRef, TranscriptPageRequest, TranscriptRecord, TranscriptSnapshot } from "./transcriptProtocol";
import { snapshotRecords } from "./transcriptSnapshotState";
import { TurnEventProjector } from "./turnEventProjection";
import type { HistoryMessage, WireEvent } from "./types";
import type { Item, State } from "./useController";

export async function resolveSnapshotItems(client: TranscriptSnapshotClient, tabId: string, entryId: string,
  getState: () => State | undefined, convert: (messages: HistoryMessage[], prefix: string) => { items: Item[] },
  commit: (patches: Record<string, Item>) => void): Promise<TranscriptRecord | undefined> {
  let resolved: TranscriptRecord | undefined;
  for (let attempt = 0; attempt < 8; attempt++) {
  const before = new Map(getState()?.items.map((item) => [item.id, item]));
  const record = await client.content(tabId, entryId);
  if (!record) return resolved;
  const converted = convert([{ ...record.message, recordId: record.id }], "snapshot:");
  const current = getState();
  const patches: Record<string, Item> = {};
  for (const item of converted.items) {
    const existing = current?.items.find((candidate) => candidate.id === item.id);
    if (existing && existing === before.get(item.id)) patches[item.id] = item.kind === "tool" && existing.kind === "tool"
      ? { ...existing, ...(record.message.role === "tool" ? { output: item.output } : { args: item.args }) } : item;
  }
  commit(patches);
  if (Object.keys(patches).length === 0) return resolved;
  client.acceptContent(tabId, record);
  resolved = record;
  }
  return resolved;
}

export interface SnapshotTransport {
  snapshot(tabId: string, request: TranscriptPageRequest): Promise<TranscriptSnapshot | undefined>;
  page(tabId: string, request: TranscriptPageRequest): Promise<TranscriptSnapshot>;
  content(tabId: string, request: TranscriptContentRef & { offset: number }): Promise<TranscriptContentChunk>;
}

type Cut = { snapshot: TranscriptSnapshot; records: Map<string, TranscriptRecord>; touched: Set<string>; expired?: boolean; reads?: Map<string, Promise<TranscriptRecord>> };
export class StaleCut extends Error {}

/** Owns immutable page/content leases; the projector owns event admission.
 * A stale response can neither advance coverage nor mutate another cut. */
export class TranscriptSnapshotClient {
  private readonly cuts = new Map<string, Cut>();
  private readonly generations = new Map<string, number>();
  constructor(private readonly transport: SnapshotTransport, private readonly projector: TurnEventProjector,
    private readonly pinned: (tabId: string) => boolean = () => true) {}

  release(tabId: string) {
    this.generations.set(tabId, (this.generations.get(tabId) ?? 0) + 1);
    this.cuts.delete(tabId);
    this.projector.release(tabId);
  }

  installed(tabId: string): boolean { return this.projector.snapshotBoundary(tabId) !== undefined; }

  acceptContent(tabId: string, record: TranscriptRecord) {
    const cut = this.cuts.get(tabId);
    if (cut?.records.has(record.id)) cut.records.delete(record.id);
  }

  observeEvent(tabId: string, event: WireEvent) {
    const cut = this.cuts.get(tabId);
    if (!cut) return;
    if (event.messageId) cut.touched.add(`m:${event.messageId}`);
    if (event.tool?.id) cut.touched.add(`tool:${event.tool.id}`);
    if (cut.touched.size > 2048) { cut.expired = true; cut.touched.clear(); }
  }

  prune() {
    let unpinned = 0;
    let bytes = 0;
    for (const [tabId, cut] of [...this.cuts.entries()].reverse()) {
      if (this.pinned(tabId)) continue;
      unpinned++;
      // Only unresolved previews are retained here. Full bodies live in the
      // mounted transcript; resolving a record releases its client cache.
      for (const record of cut.records.values()) bytes += JSON.stringify(record).length * 2;
      if (unpinned > 3 || bytes > 32 * 1024 * 1024) this.cuts.delete(tabId);
    }
  }

  async load(tabId: string, commit: (snapshot: TranscriptSnapshot) => void, current: () => boolean = () => true): Promise<boolean> {
    const generation = (this.generations.get(tabId) ?? 0) + 1;
    this.generations.set(tabId, generation);
    const lease = this.projector.beginSnapshot(tabId);
    const valid = () => this.generations.get(tabId) === generation && current();
    try {
      for (let attempt = 0; attempt < 3; attempt++) {
        const snapshot = await this.transport.snapshot(tabId, {});
        if (!valid()) return false;
        if (!snapshot) { this.release(tabId); return false; }
        if (snapshot.stale) continue;
        const records = snapshotRecords(snapshot);
        try {
          // Mutable owners need their full prefix before accepting any suffix.
          const active = new Set((snapshot.activeAttempts ?? []).map((entry) => entry.messageId));
          for (const record of records) {
            if (record.message.pending || active.has(record.message.messageId ?? "") ||
                record.message.toolCalls?.some((tool) => tool.pending)) {
              await this.resolveRecord(tabId, record, valid);
            }
          }
        } catch (error) {
          if (error instanceof StaleCut && valid()) continue;
          throw error;
        }
        if (!valid()) return false;
        const cut = { snapshot: { ...snapshot, records: [], activeRecords: [] },
          records: new Map(records.filter((record) => record.refs?.length).map((record) => [record.id, record])), touched: new Set<string>() };
        return this.projector.installSnapshot(lease, snapshot, () => {
          commit(snapshot);
          this.cuts.set(tabId, cut);
          this.prune();
        });
      }
      throw new Error("transcript snapshot expired during loading");
    } catch (error) {
      if (valid()) this.projector.abortSnapshot(lease);
      throw error;
    }
  }

  async older(tabId: string, commit: (page: TranscriptSnapshot) => void): Promise<"loaded" | "stale" | "absent"> {
    const cut = this.cuts.get(tabId);
    if (!cut || cut.expired) return this.installed(tabId) ? "stale" : "absent";
    if (!cut.snapshot.hasOlder) return "absent";
    const page = await this.transport.page(tabId, { snapshotId: cut.snapshot.snapshotId, before: cut.snapshot.before });
    if (this.cuts.get(tabId) !== cut) return "absent";
    if (page.stale) return "stale";
    if (page.snapshotId !== cut.snapshot.snapshotId || page.coveredThroughSeq !== cut.snapshot.coveredThroughSeq ||
        page.before >= cut.snapshot.before) throw new Error("invalid transcript page boundary");
    const records = snapshotRecords({ ...page, activeRecords: [] }).filter((record) => !cut.touched.has(record.id) &&
      !cut.touched.has(`m:${record.message.messageId}`) && !cut.touched.has(`tool:${record.message.toolCallId}`));
    commit({ ...page, records, activeRecords: [] });
    for (const record of records) if (record.refs?.length && !cut.records.has(record.id)) cut.records.set(record.id, record);
    cut.snapshot = { ...cut.snapshot, before: page.before, hasOlder: page.hasOlder };
    return "loaded";
  }

  /** Resolve one immutable record. Caller fences patches against item identity
   * changes while this read is in flight, including streamed mutations. */
  async content(tabId: string, itemId: string): Promise<TranscriptRecord | undefined> {
    const cut = this.cuts.get(tabId);
    if (!cut || cut.expired) { if (this.installed(tabId)) throw new StaleCut(); return undefined; }
    const record = [...cut.records.values()].find((entry) => entry.refs?.length && !cut.touched.has(entry.id) &&
      !cut.touched.has(`m:${entry.message.messageId}`) && !cut.touched.has(`tool:${entry.message.toolCallId}`) && (itemId === entry.id || itemId === `record:${entry.id}` ||
      (entry.message.messageId && itemId === `m:${entry.message.messageId}`) ||
      itemId === entry.message.toolCallId || entry.message.toolCalls?.some((tool) => tool.id === itemId)));
    if (!record || !record.refs?.length) return undefined;
    const reads = cut.reads ??= new Map();
    let pending = reads.get(record.id);
    if (!pending) {
      const detached: TranscriptRecord = JSON.parse(JSON.stringify(record));
      pending = this.resolveRecord(tabId, detached, () => this.cuts.get(tabId) === cut).then(() => detached);
      reads.set(record.id, pending);
      const request = pending;
      const cleanup = () => { if (reads.get(record.id) === request) reads.delete(record.id); };
      void pending.then(cleanup, cleanup);
    }
    let detached: TranscriptRecord;
    try { detached = await pending; }
    catch (error) { if (error instanceof StaleCut && this.cuts.get(tabId) !== cut) return undefined; throw error; }
    if (this.cuts.get(tabId) !== cut || cut.touched.has(record.id) || cut.touched.has(`m:${record.message.messageId}`) ||
        cut.touched.has(`tool:${record.message.toolCallId}`)) return undefined;
    return detached;
  }

  private async resolveRecord(tabId: string, record: TranscriptRecord, current: () => boolean) {
    for (const ref of record.refs ?? []) {
      let offset = 0;
      const chunks: string[] = [];
      for (;;) {
        const chunk = await this.transport.content(tabId, { ...ref, offset });
        if (!current() || chunk.stale) throw new StaleCut();
        if (!Number.isSafeInteger(chunk.nextOffset) || chunk.nextOffset < offset || chunk.nextOffset > ref.bytes ||
            (!chunk.done && chunk.nextOffset === offset)) throw new Error("invalid transcript content offset");
        chunks.push(chunk.data);
        offset = chunk.nextOffset;
        if (chunk.done) {
          if (offset !== ref.bytes) throw new Error("incomplete transcript content");
          break;
        }
      }
      let target: unknown = record.message;
      for (const key of ref.path.slice(0, -1)) {
        if (key === "__proto__" || key === "constructor" || key === "prototype" || !target || typeof target !== "object") throw new Error("invalid transcript content path");
        target = (target as Record<string, unknown>)[key];
      }
      const key = ref.path[ref.path.length - 1];
      if (!key || key === "__proto__" || key === "constructor" || key === "prototype" || !target || typeof target !== "object") throw new Error("invalid transcript content path");
      (target as Record<string, unknown>)[key] = chunks.join("");
    }
    record.refs = [];
  }
}
