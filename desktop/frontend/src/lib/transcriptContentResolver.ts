// Lazy full-content resolution for snapshot-protocol tabs. A tab wired to the
// unified transcript snapshot registers a resolver here; the transcript store
// then answers content requests from the snapshot client instead of the
// windowed history backend. Tabs without snapshot support fall through to the
// store's HistoryContentForTab path.

export interface TranscriptContentResolver {
  resolve(entryId: string, field: string): Promise<string | undefined>;
  enabled(): boolean;
}

export class TranscriptContentResolverRegistry {
  private readonly resolvers = new Map<string, TranscriptContentResolver>();

  register(
    tabId: string,
    resolve: (entryId: string, field: string) => Promise<string | undefined>,
    enabled: () => boolean = () => true,
  ): () => void {
    const entry = { resolve, enabled };
    this.resolvers.set(tabId, entry);
    return () => { if (this.resolvers.get(tabId) === entry) this.resolvers.delete(tabId); };
  }

  active(tabId: string): TranscriptContentResolver | undefined {
    const resolver = this.resolvers.get(tabId);
    return resolver?.enabled() ? resolver : undefined;
  }
}

// Snapshot-protocol item ids are `m:<messageId>` while the windowed store keys
// records by backend entryId, so alias them back before lookup.
export function resolveTranscriptEntryAlias(
  sessions: Iterable<{ tabId: string; records: ReadonlyArray<{ entryId: string; message: { messageId?: string } }> }>,
  tabId: string,
  entryId: string,
): string {
  if (!entryId.startsWith("m:")) return entryId;
  for (const session of sessions) {
    if (session.tabId !== tabId) continue;
    const record = session.records.find((candidate) => `m:${candidate.message.messageId}` === entryId);
    if (record) return record.entryId;
  }
  return entryId;
}
