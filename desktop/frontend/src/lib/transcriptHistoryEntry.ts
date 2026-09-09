import type { TranscriptRow } from "./transcriptRows";

/**
 * History-backed items carry ids derived from their backend entry
 * (`he:<entryId>`, tool calls `he:<entryId>:tc<index>`, or a bare toolCallId).
 * Returns the entryId for rows that may carry unresolved lazy-content refs.
 */
export function historyEntryIdForItemId(id: string | undefined): string | undefined {
  if (id?.startsWith("m:") || id?.startsWith("record:")) return id;
  if (!id || !id.startsWith("he:")) return undefined;
  return id.slice(3).replace(/:tc\d+$/, "");
}

/** The entry a row can trigger lazy full-content resolution for, if any. */
export function historyEntryIdForRow(row: TranscriptRow): string | undefined {
  switch (row.kind) {
    case "user":
    case "reasoning":
    case "phase":
    case "process-notice":
    case "compaction":
    case "answer":
    case "notice":
      return historyEntryIdForItemId(row.item.id);
    case "tool":
      return historyEntryIdForItemId(row.item.id) ?? row.item.id;
    case "tool-batch":
    case "tool-group":
      return historyEntryIdForItemId(row.items[0]?.id) ?? row.items[0]?.id;
    default:
      return undefined;
  }
}
