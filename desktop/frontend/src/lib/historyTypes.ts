// ── Windowed history paging (desktop/history_slice.go) ──────────────────────
// HistorySliceForTab pages toward older history with an opaque cursor; the
// first call uses cursor "" for the newest page. Entry IDs are stable for the
// life of a session revision (s<file>:r<epoch>:m<msgIndex>:o<subOrder>).
import type { HistoryMessage } from "./types";

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
