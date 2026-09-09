import type { HistoryMessage, TurnEventReplayView, TurnStatus, WireEvent, WireCompletionSummary } from "./types";
import type { TranscriptIdentity, TranscriptSnapshotBoundary } from "./turnEventProjection";

export interface TranscriptContentRef {
  snapshotId: string;
  recordId: string;
  path: string[];
  bytes: number;
}

export interface TranscriptRecord {
  id: string;
  order: number;
  message: HistoryMessage;
  refs: TranscriptContentRef[];
}

export interface TranscriptSnapshot extends TranscriptSnapshotBoundary {
  records: TranscriptRecord[];
  activeRecords: TranscriptRecord[];
  runtime: { turnId?: string; submissionId?: string; status?: TurnStatus; phase?: string; startedAt?: number; completionSummary?: WireCompletionSummary; pendingEvents: WireEvent[] };
  activeAttempts: Array<{ id: string; messageId: string }>;
  before: number;
  hasOlder: boolean;
  totalRecords: number;
	totalTurns: number;
  stale: boolean;
}

export interface TranscriptPageRequest {
  snapshotId?: string;
  before?: number;
  records?: number;
  bytes?: number;
}

export interface TranscriptContentChunk { data: string; nextOffset: number; done: boolean; stale: boolean }
export interface TranscriptReplayRequest { identity: TranscriptIdentity; after: number }
export interface TranscriptReplay extends TranscriptSnapshotBoundary, TurnEventReplayView {}

export interface TranscriptProtocolBindings {
  TranscriptSnapshotForTab?(tabId: string, request: TranscriptPageRequest): Promise<TranscriptSnapshot>;
  TranscriptPageForTab?(tabId: string, request: TranscriptPageRequest): Promise<TranscriptSnapshot>;
  TranscriptContentForTab?(tabId: string, request: TranscriptContentRef & { offset: number }): Promise<TranscriptContentChunk>;
  TranscriptReplayForTab?(tabId: string, request: TranscriptReplayRequest): Promise<TranscriptReplay>;
  RemoteTranscriptSnapshotForTab?(tabId: string, request: TranscriptPageRequest): Promise<{ supported: boolean; snapshot?: TranscriptSnapshot }>;
  RemoteTranscriptPageForTab?(tabId: string, request: TranscriptPageRequest): Promise<TranscriptSnapshot>;
  RemoteTranscriptContentForTab?(tabId: string, request: TranscriptContentRef & { offset: number }): Promise<TranscriptContentChunk>;
  RemoteTranscriptReplayForTab?(tabId: string, request: TranscriptReplayRequest): Promise<TranscriptReplay>;
  ResumeTranscriptSessionForTab?(tabID: string, path: string): Promise<void>;
  OpenChannelTranscriptSessionForTab?(tabID: string, path: string): Promise<void>;
}
