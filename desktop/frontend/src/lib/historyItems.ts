// historyItems converts durable HistoryMessage rows (and legacy HistoryPage
// payloads) into transcript Items for the single-shot hydration path. The
// windowed counterpart lives in transcriptStore; both projections must agree
// on item identity and tool call/result folding.
import { asArray } from "./array";
import { historicalResultNotice } from "./completionResultState";
import { appendNoticeItem, deliveryReadinessDetail, readinessMissingIds } from "./controllerNotices";
import { createUniqueItemIDAllocator } from "./historyItemIds";
import { t } from "./i18n";
import { upsertReadPause } from "./readPause";
import { historySearchAndAnswer } from "./searchTranscript";
import { fileDiffFromWire, summarizeFileDiff } from "./tools";
import type { HistoryMessage, HistoryPage, MemoryCitation } from "./types";
import type { Item } from "./useController";

/** Mirrors Go backend's ReadOnly() hints. */
export function isReadOnlyTool(name: string): boolean {
  switch (name) {
    case "read_file":
    case "ls":
    case "grep":
    case "glob":
    case "web_fetch":
    case "web_search":
    case "code_index":
    case "bash_output":
    case "waitJob":
    case "todo_write":
    case "read_skill":
      return true;
    default:
      return false;
  }
}

export function historyMessagesToItems(messages: HistoryMessage[], idPrefix: string, startSeq = 0): { items: Item[]; seq: number } {
  const resultByID = new Map<string, HistoryMessage>();
  for (const m of messages) {
    if (m.role === "tool" && m.toolCallId && !resultByID.has(m.toolCallId)) {
      resultByID.set(m.toolCallId, m);
    }
  }
  const positionalResults = positionalToolResults(messages);
  const consumedPositionalToolIndexes = new Set(Array.from(positionalResults.values(), (result) => result.index));

  let items: Item[] = [];
  let seq = startSeq;
  const consumedToolIDs = new Set<string>();
	const uniqueItemID = createUniqueItemIDAllocator();
  for (let messageIndex = 0; messageIndex < messages.length; messageIndex += 1) {
    const m = messages[messageIndex];
		const recordItemId = m.recordId ? `record:${m.recordId}` : `${idPrefix}${seq}`;
    if (m.role === "system") continue;
    if (m.role === "phase") {
      if (m.content.trim() !== "") {
        items.push({ kind: "phase", id: recordItemId, text: m.content });
        seq++;
      }
      continue;
    }
    if (m.role === "notice") {
      if (m.code === "incomplete_read") {
        items = upsertReadPause(items, m.readPause, recordItemId);
        seq++;
        continue;
      }
      if (m.completionReceipt || m.completionSummary) {
        const result = historicalResultNotice(m, recordItemId);
        if (result) { items.push(result); seq++; }
        continue;
      }
      if (m.code === "protocol_recovery" && m.pending && m.protocolRecovery?.id) {
        items.push({kind:"notice",id:recordItemId,level:"info",code:m.code,text:t("notice.protocolRecoveryBody"),action:"recover_context",recoveryId:m.protocolRecovery.id});
				seq++;
        continue;
      }
      if (m.code === "final_readiness" && m.pending) {
        items.push({
          kind: "notice",
          id: recordItemId,
          level: "info",
          variant: "delivery",
          title: t("notice.deliveryIncompleteTitle"),
          text: t("notice.deliveryIncompleteBody"),
          detail: deliveryReadinessDetail(m.readiness),
          action: "continue_delivery",
          missing: readinessMissingIds(m.readiness),
        });
        seq++;
        continue;
      }
      if (m.content.trim() !== "" || m.decisionReceipt) {
        const next = appendNoticeItem(items, seq, recordItemId, m.level === "warn" ? "warn" : "info", m.content, m.detail, m.code, m.decisionReceipt);
        items = next.items;
        seq = next.seq;
      }
      continue;
    }
    if (m.role === "compaction") {
      items.push({
        kind: "compaction",
        id: recordItemId,
        pending: Boolean(m.pending),
        trigger: m.trigger ?? "",
        messages: m.messages ?? 0,
        summary: m.summary ?? "",
        archive: m.archive ?? "",
      });
      seq++;
      continue;
    }
    if (m.role === "user") {
      if (m.content.trim() === "") continue;
      items.push({ kind: "user", id: m.messageId ? `m:${m.messageId}` : recordItemId, messageId: m.messageId, submissionId: m.submissionId, text: m.content, submitText: m.submitText, createdAt: m.createdAt, checkpointTurn: m.checkpointTurn, historyTurn: m.historyTurn });
      seq++;
      continue;
    }
    if (m.role === "assistant") {
      const memoryCitations = asArray<MemoryCitation>(m.memoryCitations);
      const messageItemId = m.messageId ? `m:${m.messageId}` : m.recordId ? recordItemId : undefined;
      const built = historySearchAndAnswer(messageItemId ?? `${idPrefix}${seq}`, {
        content: m.content,
        reasoning: m.reasoning,
        workDurationMs: m.workDurationMs,
        memoryCitations: memoryCitations.length > 0 ? memoryCitations : undefined,
        serverSearch: m.serverSearch,
      });
      for (const item of built) {
        if (item.kind === "assistant") { item.id = messageItemId ?? `${idPrefix}${seq}`; item.streaming = Boolean(m.pending); }
        items.push(item);
        seq++;
      }
			if (m.pending && !built.some((item) => item.kind === "assistant")) {
				items.push({ kind: "assistant", id: messageItemId ?? recordItemId, text: m.content, reasoning: m.reasoning ?? "", streaming: true });
				seq++;
			}
      const toolCalls = m.toolCalls ?? [];
      for (let callIndex = 0; callIndex < toolCalls.length; callIndex += 1) {
        const tc = toolCalls[callIndex];
        const positionalResult = tc.id ? undefined : positionalResults.get(positionalToolResultKey(messageIndex, callIndex));
        const result = tc.id ? resultByID.get(tc.id) : positionalResult?.message;
        if (tc.id) consumedToolIDs.add(tc.id);
        const archived = Boolean(tc.argumentsArchived || result?.toolResultArchived);
        const output = result?.toolResultArchived ? undefined : result?.content ?? "";
        const error = result?.toolResultError || (output ? historyToolError(output) : undefined);
        const fileDiff = fileDiffFromWire(tc);
        items.push({
          kind: "tool",
          id: uniqueItemID(tc.id || "", m.recordId ? `${recordItemId}:tc${callIndex}` : `${idPrefix}tool${seq}`),
					messageId: m.messageId,
					parentId: tc.parentId,
					argChars: tc.argChars,
					startedAt: tc.startedAt,
          name: tc.name,
          args: tc.arguments ?? "",
          readOnly: typeof tc.resolvedReadOnly === "boolean" ? tc.resolvedReadOnly : isReadOnlyTool(tc.name),
          resolvedName: tc.resolvedName,
          capabilityId: tc.capabilityId,
          status: result ? (error ? "error" : "done") : tc.pending ? "running" : "stopped",
          output,
          error,
          dataArchived: archived || undefined,
          subject: tc.subject,
          summary: summarizeFileDiff(fileDiff) || tc.summary,
          fileDiff,
          isShell: tc.name === "bash" || (tc.id || "").startsWith("shell-"),
          execution: result?.execution,
        });
        seq++;
      }
      continue;
    }
    if (m.role === "tool") {
      if ((m.toolCallId && consumedToolIDs.has(m.toolCallId)) || consumedPositionalToolIndexes.has(messageIndex)) continue;
      const output = m.toolResultArchived ? undefined : m.content;
      const error = m.toolResultError || (output ? historyToolError(output) : undefined);
      items.push({
        kind: "tool",
        id: uniqueItemID(m.toolCallId || "", m.recordId ? `${recordItemId}:tool` : `${idPrefix}tool${seq}`),
        name: m.toolName || "tool",
        args: "",
        readOnly: isReadOnlyTool(m.toolName || "tool"),
        status: error ? "error" : "done",
        output,
        error,
        dataArchived: m.toolResultArchived || undefined,
        isShell: (m.toolName || "") === "bash" || (m.toolCallId || "").startsWith("shell-"),
        execution: m.execution,
      });
      seq++;
      continue;
    }
  }
  return { items, seq };
}

export function applyTurnCheckpoint(items: Item[], submissionId: string | undefined, turn: number | undefined): Item[] {
  if (!submissionId) return items;
  const validTurn = turn !== undefined && Number.isInteger(turn) && turn >= 0;
  let changed = false;
  const next = items.map((item) => {
    if (item.kind !== "user" || item.submissionId !== submissionId) return item;
    changed = true;
    return { ...item, submissionId: undefined, checkpointTurn: item.checkpointTurn ?? (validTurn ? turn : undefined) };
  });
  return changed ? next : items;
}

export function historyPageItems(page: HistoryPage): { items: Item[]; seq: number; firstTurn: number } {
  const converted = historyMessagesToItems(asArray(page.messages), `h${page.startTurn}-`, 0);
  let historyTurn = page.startTurn + 1;
  const items = converted.items.map((item) => {
    if (item.kind !== "user") return item;
    const user = { ...item, historyTurn };
    historyTurn += 1;
    return user;
  });
  return {
    items,
    seq: converted.seq,
    // Legacy HistoryPage is 0-based while HistorySlice is 1-based. State uses
    // the HistorySlice coordinate so every transcript consumer sees one model.
    firstTurn: page.totalTurns > 0 ? page.startTurn + 1 : 0,
  };
}

function positionalToolResults(messages: HistoryMessage[]): Map<string, { message: HistoryMessage; index: number }> {
  const out = new Map<string, { message: HistoryMessage; index: number }>();
  const consumed = new Set<number>();
  for (let messageIndex = 0; messageIndex < messages.length; messageIndex += 1) {
    const message = messages[messageIndex];
    const toolCalls = message.role === "assistant" ? message.toolCalls ?? [] : [];
    if (toolCalls.length === 0) continue;
    let resultIndex = messageIndex + 1;
    for (let callIndex = 0; callIndex < toolCalls.length; callIndex += 1) {
      if (toolCalls[callIndex].id) continue;
      let matched = false;
      while (resultIndex < messages.length) {
        const candidate = messages[resultIndex];
        if (candidate.role !== "tool") break;
        const candidateIndex = resultIndex;
        resultIndex += 1;
        if (candidate.toolCallId || consumed.has(candidateIndex)) continue;
        consumed.add(candidateIndex);
        out.set(positionalToolResultKey(messageIndex, callIndex), { message: candidate, index: candidateIndex });
        matched = true;
        break;
      }
      if (!matched) break;
    }
  }
  return out;
}

function positionalToolResultKey(messageIndex: number, callIndex: number): string {
  return `${messageIndex}:${callIndex}`;
}

export function historyToolError(output: string): string | undefined {
  const trimmed = output.trimStart();
  if (
    trimmed.startsWith("[error") ||
    trimmed.startsWith("Error:") ||
    trimmed.startsWith("error:") ||
    trimmed.startsWith("blocked:")
  ) {
    return output;
  }
  return undefined;
}
