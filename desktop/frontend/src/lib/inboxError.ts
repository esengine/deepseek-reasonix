import type { Locale } from "./i18n";

const CODE_PREFIX = "reasonix_error:";

// Message arrays share one stable code-to-index table so the three localized
// copies do not repeat object keys in the initial desktop bundle.
const CODE_INDEX = {
  inbox_paused: 0,
  inbox_capacity_items: 1,
  inbox_capacity_bytes: 2,
  inbox_item_too_large: 3,
  inbox_item_not_found: 4,
  inbox_invalid_state: 5,
  inbox_schema_readonly: 6,
  inbox_closed: 7,
  inbox_empty: 8,
  inbox_idempotency_conflict: 9,
  channel_read_only: 10,
  workspace_starting: 11,
  workspace_start_failed: 12,
} as const;

type InboxErrorCode = keyof typeof CODE_INDEX;
const UNKNOWN_INDEX = 13;
const STEER_QUEUED_INDEX = 14;
const CANCEL_FAILED_INDEX = 15;

const ERROR_COPY: Record<Exclude<Locale, "es">, readonly string[]> = {
  en: [
    "Inbox is paused",
    "The inbox has reached its item limit",
    "The inbox has reached its storage limit",
    "This instruction is too large to add to the inbox",
    "This inbox instruction no longer exists",
    "This inbox instruction cannot be changed in its current state",
    "This inbox was created by a newer version and is read-only",
    "The inbox is closed",
    "A blank instruction cannot be added",
    "This instruction conflicts with an earlier submission",
    "This session is read-only, so its inbox cannot be changed",
    "The workspace is still starting. Try again shortly",
    "The workspace failed to start",
    "The inbox operation could not be completed",
    "The turn ended before guidance could be applied. It will remain queued for the next turn",
    "Cancel failed: {error}",
  ],
  zh: [
    "收件箱已暂停",
    "收件箱已达到条目上限",
    "收件箱已达到容量上限",
    "这条指令太大，无法加入收件箱",
    "这条收件箱指令已不存在",
    "当前状态下无法操作这条收件箱指令",
    "收件箱由较新版本创建，当前只能读取",
    "收件箱已关闭",
    "不能加入空白指令",
    "这条指令与已提交的请求冲突",
    "当前会话为只读，无法修改收件箱",
    "工作区还在启动，请稍后重试",
    "工作区启动失败",
    "无法完成收件箱操作",
    "引导尚未应用时当前回合已结束；它会保留在队列中，供下一回合处理",
    "取消失败：{error}",
  ],
  "zh-TW": [
    "收件匣已暫停",
    "收件匣已達到項目上限",
    "收件匣已達到容量上限",
    "這條指令太大，無法加入收件匣",
    "這條收件匣指令已不存在",
    "目前狀態下無法操作這條收件匣指令",
    "收件匣由較新版本建立，目前只能讀取",
    "收件匣已關閉",
    "不能加入空白指令",
    "這條指令與已提交的請求衝突",
    "目前會話為唯讀，無法修改收件匣",
    "工作區還在啟動，請稍後重試",
    "工作區啟動失敗",
    "無法完成收件匣操作",
    "引導尚未套用時目前回合已結束；它會保留在佇列中，供下一回合處理",
    "取消失敗：{error}",
  ],
};

const LEGACY_CODES: Record<string, InboxErrorCode> = {
  "inbox is paused": "inbox_paused",
  "session inbox item limit reached": "inbox_capacity_items",
  "session inbox byte limit reached": "inbox_capacity_bytes",
  "inbox item exceeds single-item size limit": "inbox_item_too_large",
  "inbox item not found": "inbox_item_not_found",
  "inbox item state does not allow this operation": "inbox_invalid_state",
  "inbox schema is newer and is read-only": "inbox_schema_readonly",
  "inbox is closed": "inbox_closed",
  "inbox item body is empty": "inbox_empty",
  "idempotency key was already used for different input": "inbox_idempotency_conflict",
  "channel session is read-only": "channel_read_only",
  "workspace is still starting": "workspace_starting",
};

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export function formatInboxError(error: unknown, locale: Locale): string {
  const raw = errorText(error);
  const encodedCode = raw.startsWith(CODE_PREFIX) ? raw.slice(CODE_PREFIX.length) : "";
  const legacyCode = LEGACY_CODES[raw]
    ?? (raw.startsWith("workspace failed to start:") ? "workspace_start_failed" : undefined);
  const code = encodedCode || legacyCode;
  if (!code) return raw;
  const index = CODE_INDEX[code as InboxErrorCode];
  const copy = ERROR_COPY[locale as keyof typeof ERROR_COPY] ?? ERROR_COPY.en;
  return copy[index ?? UNKNOWN_INDEX];
}

export function inboxSteerQueuedMessage(locale: Locale): string {
  const copy = ERROR_COPY[locale as keyof typeof ERROR_COPY] ?? ERROR_COPY.en;
  return copy[STEER_QUEUED_INDEX];
}

export function isInboxItemMissing(error: unknown): boolean {
  const raw = errorText(error);
  return raw === `${CODE_PREFIX}inbox_item_not_found` || raw === "inbox item not found";
}

// The idle-tab code is consumed by the Stop path, never displayed.
export function isTurnNotRunning(error: unknown): boolean {
  return errorText(error) === `${CODE_PREFIX}turn_not_running`;
}

export function formatInboxCancelError(error: unknown, locale: Locale): string {
  const copy = ERROR_COPY[locale as keyof typeof ERROR_COPY] ?? ERROR_COPY.en;
  return copy[CANCEL_FAILED_INDEX].replace("{error}", formatInboxError(error, locale));
}
