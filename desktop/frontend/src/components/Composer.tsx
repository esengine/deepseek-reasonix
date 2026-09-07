import { recoveryStatusText, type RecoveryRetry } from "../lib/recoveryStatus";
import { useAppNavigationStore } from "../store/appNavigation";
import { lazy, Suspense, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import type { CSSProperties, ClipboardEvent, DragEvent, KeyboardEvent, MouseEvent as ReactMouseEvent, PointerEvent as ReactPointerEvent } from "react";
import { ArrowRight, ArrowUp, Check, ChevronsUpDown, CornerDownRight, Equal, Eye, FileText, Folder, Gauge, List, MessageSquare, PackageCheck, Plus, Search, Shield, ShieldAlert, ShieldCheck, Square, Target, Trash2, X } from "lucide-react";
import { asArray } from "../lib/array";
import { filterAtMatches } from "../lib/atMatches";
import { DedupIndex, sha256 } from "../lib/attachDedup";
import { app, onFilesDropped } from "../lib/bridge";
import { enqueueInboxGuidanceForActiveTurn, steerInboxItemForActiveTurn } from "../lib/inboxSubmit";
import { formatInboxError, isInboxItemMissing } from "../lib/inboxError";
import { inboxScopeKey } from "../lib/composerInboxQueue";
import { useComposerInboxRefresh } from "../lib/useComposerInboxRefresh";
import { useComposerImeGuard } from "../lib/useComposerImeGuard";
import { useComposerCommandCatalog } from "../lib/useComposerCommandCatalog";
import { guidanceIsInFlight, guidanceNeedsRetry, guidanceTextMatches, kickIdleGuidance, markGuidanceQueued } from "../lib/composerGuidance";
import { canUsePromptHistory, composerEnterAction, composerEscapeAction, composerMenuKeyAction, insertComposerNewline, isFnKeyEvent, isImeKeyEvent, promptHistoryDirectionFromEvent } from "../lib/composerKeyboard";
import { cacheGeneration, loadOlder } from "../lib/composerHistory";
import { sessionTurnsLabel } from "../lib/sessionTurnsPresentation";
import { SPINNER_WORDS, useI18n, type Translator } from "../lib/i18n";
import { detectShortcutPlatform, formatShortcutCombo, isReservedComposerHistoryShortcut, matchesShortcut, useShortcutComboLabel } from "../lib/keyboardShortcuts";
import { fallbackCopyText } from "../lib/clipboard";
import {
  commandAvailableAtSlashPosition,
  commandUsesStructuredInvocation,
  invocationRequests,
  replaceInvocationTextRange,
  serializeInvocationSubmit,
  typedStructuredInvocationDraft,
  trimInvocationDraft,
  type ComposerInvocation,
  type StructuredInvocationSubmit,
} from "../lib/invocationDisplay";
import { formatTokens } from "../lib/format";
import type { CancelOutcome } from "../lib/inboxCancel";
import type { ControllerLiveStore } from "../lib/useController";
import { clearLayoutSize, loadOptionalLayoutSize, saveLayoutSize } from "../lib/layoutPreferences";
import { createRafResizeUpdater } from "../lib/resizeDrag";
import { observeComposerMenuViewport } from "../lib/composerMenuViewport";
import { resolveComposerContentSizing } from "../lib/composerSizing";
import { useToast } from "../lib/toast";
import { type CollaborationMode, type CommandInfo, type ComposerInsertRequest, type ContextInfo, type DirEntry, type EffortInfo, type GoalRuntime, type HistoryMessage, type Mode, type PromptHistoryEntry, type QualityFloor, type SessionMeta, type SessionReference, type SlashArgItem, type SlashArgsResult, type ToolApprovalMode, type BalanceInfo } from "../lib/types";
import { ComposerPinnedFilesShelf } from "./ComposerPinnedFilesShelf";
import {
  formatWorkspaceReference,
  parseWorkspaceReference,
  readWorkspaceReferenceDrag,
  WORKSPACE_REF_DRAG_TYPE,
} from "../lib/workspaceDrag";
import { SlashMenu, sortSlashCommandsForMenu } from "./SlashMenu";
import { ArgMenu } from "./ArgMenu";
import { ANCHORED_POPOVER_CLOSE_MS, AnchoredPopover } from "./AnchoredPopover";
import { EffortSwitcher } from "./EffortSwitcher";
const ModelSwitcher = lazy(() => import("./ModelSwitcher").then((module) => ({ default: module.ModelSwitcher })));
import { Tooltip } from "./Tooltip";
const RecoveryWaitBanner = lazy(() => import("./RecoveryWaitBanner").then((module) => ({ default: module.RecoveryWaitBanner })));
import { ComposerContextCard } from "./ComposerContextCard";
import { Markdown } from "./Markdown";
import { CodeViewer } from "./CodeViewer";
import { ContextWindowRing } from "./ContextWindowRing";
import { ImageViewer } from "./ImageViewer";
import type { PendingGuidance } from "./ComposerGuidanceShelf";
import {
  RichComposerInput,
  slashQueryAt,
  type RichComposerChangeOrigin,
  type RichComposerInputHandle,
  type RichComposerSelection,
  type RichSlashQuery,
} from "./RichComposerInput";
import { VirtualMenu } from "./VirtualMenu";
import { activeFileReferenceToken, dirEntryMenuLabel, dirEntrySubmitPath } from "./FileReferenceMenu";
import { activeRefTokenRe, escapeRefPath, unescapeRefPath } from "../lib/refToken";
import { ContextMenu, contextMenuPointFromEvent, type ContextMenuItem, type ContextMenuPoint } from "./ContextMenu";
import {
  formatSelectedTextContext,
  formatSelectionLabel,
  languageFor,
  normalizeSelectedText,
  selectedTextSnippet,
  type SelectedTextInsertRequest,
  type SelectedTextReference,
} from "../lib/selectedTextContext";
import { formatGoalWorkTime } from "../lib/goalRuntime";
import { ComposerContentMenuActions } from "./ComposerContentMenuActions";

interface Attachment {
  path: string;
  previewUrl?: string;
  displayName?: string;
}

interface AttachmentDedupKey {
  hash: string;
  source: string;
}

export interface WorkspaceReference {
  path: string;
  isDir?: boolean;
  displayPath?: string;
}

const LONG_PASTE_MIN_CHARS = 2000;
const LONG_PASTE_MIN_LINES = 20;
const COMPOSER_MIN_HEIGHT = 104;
const COMPOSER_MAX_HEIGHT = 360;
// Height reserved for the in-card run strip while a turn runs; applied via a
// CSS calc so --composer-height always stays in "logical height" space.
const COMPOSER_RUN_STRIP_RESERVED = 30;
const COMPOSER_MAX_VIEWPORT_RATIO = 0.4;
const COMPOSER_AUTO_RESERVED_HEIGHT = 58;
const PROMPT_HISTORY_PREFETCH_REMAINING = 3;
const FILE_REF_SEARCH_CACHE_TTL_MS = 5000;
const ComposerGuidanceShelf = lazy(() => import("./ComposerGuidanceShelf").then((module) => ({ default: module.ComposerGuidanceShelf })));
type PastedBlock = { label: string; text: string };

type FileRefSearchCacheEntry = {
  entries: DirEntry[];
  cachedAt: number;
};

type ComposerDraft = {
  text: string;
  invocations: ComposerInvocation[];
  attachments: Attachment[];
  workspaceRefs: WorkspaceReference[];
  pastedBlocks: PastedBlock[];
  openPastedLabels: string[];
  sessionRefs: SessionReference[];
  selectedTextRefs: SelectedTextReference[];
  attachmentDedupKeys: Record<string, AttachmentDedupKey>;
  nextPasteId: number;
  historyIndex: number;
  savedText: string;
  pendingGuidance: PendingGuidance[];
  guidanceExpanded: boolean;
  guidanceSendingId: string | null;
  pendingPaste: number;
  submitting: boolean;
};

type ComposerEditSnapshot = {
  text: string;
  invocations: ComposerInvocation[];
  pastedBlocks: PastedBlock[];
  openPastedLabels: string[];
  nextPasteId: number;
  selection: RichComposerSelection;
};

type ComposerEditTransaction = {
  before: ComposerEditSnapshot;
  after: ComposerEditSnapshot;
  nativeBarrierBefore: boolean;
  nativeBarrierAfter: boolean;
};

type ComposerEditHistory = {
  undo: ComposerEditTransaction[];
  redo: ComposerEditTransaction[];
  undoNativeBarrier: boolean;
  redoNativeBarrier: boolean;
};

type WebkitFileEntry = {
  isDirectory?: boolean;
};

type GuidanceReceiptTracker = {
  start(draftKey: string): void;
  recordConsumed(draftKey: string, itemId: string): void;
  takeConsumed(draftKey: string, itemId: string): boolean;
  finish(draftKey: string): void;
};

const DEFAULT_COMPOSER_DRAFT_KEY = "__default_composer_draft__";
const MAX_COMPOSER_EDIT_HISTORY = 50;

function createGuidanceReceiptTracker(): GuidanceReceiptTracker {
  const inFlight = new Map<string, number>();
  const consumedBeforeReceipt = new Map<string, Set<string>>();
  return {
    start(draftKey) {
      inFlight.set(draftKey, (inFlight.get(draftKey) ?? 0) + 1);
    },
    recordConsumed(draftKey, itemId) {
      if ((inFlight.get(draftKey) ?? 0) === 0) return;
      const consumed = consumedBeforeReceipt.get(draftKey) ?? new Set<string>();
      consumed.add(itemId);
      consumedBeforeReceipt.set(draftKey, consumed);
    },
    takeConsumed(draftKey, itemId) {
      return consumedBeforeReceipt.get(draftKey)?.delete(itemId) ?? false;
    },
    finish(draftKey) {
      const remaining = (inFlight.get(draftKey) ?? 1) - 1;
      if (remaining > 0) {
        inFlight.set(draftKey, remaining);
        return;
      }
      inFlight.delete(draftKey);
      consumedBeforeReceipt.delete(draftKey);
    },
  };
}

function lineCount(s: string): number {
  if (s === "") return 0;
  return s.split(/\r\n|\r|\n/).length;
}

function shouldFoldPaste(s: string): boolean {
  return s.length >= LONG_PASTE_MIN_CHARS || lineCount(s) >= LONG_PASTE_MIN_LINES;
}

function renderPastedBlock(block: PastedBlock): string {
  return `${block.label}\n\n--- Begin ${block.label} ---\n${block.text}\n--- End ${block.label} ---`;
}

function baseName(path: string): string {
  const clean = path.replace(/[\\/]+$/, "");
  return clean.split(/[\\/]/).filter(Boolean).pop() ?? path;
}

function attachmentName(attachment: Attachment): string {
  return (attachment.displayName || baseName(attachment.path) || "attachment").trim();
}

function attachmentExt(name: string): string {
  const dot = name.lastIndexOf(".");
  return dot >= 0 ? name.slice(dot + 1).toUpperCase() : "";
}

function hasImageAttachments(items: Attachment[]): boolean {
  return items.some((attachment) => Boolean(attachment.previewUrl));
}

function displayRefName(name: string): string {
  return name.replace(/[\[\]\(\)\r\n]+/g, " ").replace(/\s+/g, " ").trim() || "attachment";
}

function formatAttachmentDisplayReference(attachment: Attachment): string {
  return `@[${displayRefName(attachmentName(attachment))}](${attachment.path})`;
}

function sortComposerAttachments(items: Attachment[]): Attachment[] {
  return [...items].sort((a, b) => {
    const ai = a.previewUrl ? 0 : 1;
    const bi = b.previewUrl ? 0 : 1;
    return ai - bi;
  });
}

function workspaceReferenceKey(ref: WorkspaceReference): string {
  return `${ref.isDir ? "dir" : "file"}:${ref.path}`;
}

type PastChatToken = {
  from: number;
  query: string;
};

function activePastChatToken(text: string): PastChatToken | null {
  const queryText = text.replace(/[\r\n]+$/u, "");
  const match = /(?:^|\s)#([^\s#]*)$/u.exec(queryText);
  if (!match) return null;
  return { from: match.index, query: match[1] };
}

export function composerPickFileEntry(
  text: string,
  atRaw: string | null,
  atDir: string,
  entry: DirEntry,
): { text: string; workspaceRef?: WorkspaceReference } {
  const queryText = text.replace(/[\r\n]+$/u, "");
  const atPos = queryText.length - (atRaw?.length ?? 0) - 1; // index of '@'
  const prefix = queryText.slice(0, Math.max(0, atPos));
  const refPath = dirEntrySubmitPath(entry, atDir);
  if (entry.path || entry.displayPath) {
    return { text: prefix, workspaceRef: { path: refPath, isDir: entry.isDir, displayPath: entry.displayPath } };
  }
  // Inline fallback: escape whitespace so the ref survives @-token parsing.
  return { text: prefix + "@" + escapeRefPath(refPath) + (entry.isDir ? "/" : " ") };
}

function emptyComposerDraft(): ComposerDraft {
  return {
    text: "",
    invocations: [],
    attachments: [],
    workspaceRefs: [],
    pastedBlocks: [],
    openPastedLabels: [],
    sessionRefs: [],
    selectedTextRefs: [],
    attachmentDedupKeys: {},
    nextPasteId: 1,
    historyIndex: -1,
    savedText: "",
    pendingGuidance: [],
    guidanceExpanded: false,
    guidanceSendingId: null,
    pendingPaste: 0,
    submitting: false,
  };
}

function cloneComposerDraft(draft: ComposerDraft): ComposerDraft {
  return {
    text: draft.text,
    invocations: draft.invocations.map((invocation) => ({ ...invocation, command: { ...invocation.command } })),
    attachments: [...draft.attachments],
    workspaceRefs: [...draft.workspaceRefs],
    pastedBlocks: [...draft.pastedBlocks],
    openPastedLabels: [...draft.openPastedLabels],
    sessionRefs: [...draft.sessionRefs],
    selectedTextRefs: draft.selectedTextRefs.map((reference) => ({ ...reference })),
    attachmentDedupKeys: { ...draft.attachmentDedupKeys },
    nextPasteId: draft.nextPasteId,
    historyIndex: draft.historyIndex,
    savedText: draft.savedText,
    pendingGuidance: draft.pendingGuidance.map((item) => ({ ...item })),
    guidanceExpanded: draft.guidanceExpanded,
    guidanceSendingId: draft.guidanceSendingId,
    pendingPaste: draft.pendingPaste,
    submitting: draft.submitting,
  };
}

function attachmentDedupFromKeys(keys: Record<string, AttachmentDedupKey>): DedupIndex {
  const index = new DedupIndex();
  for (const key of Object.values(keys)) {
    index.add(key.hash, key.source);
  }
  return index;
}

function draftHasAttachmentDedupKey(draft: ComposerDraft, key: AttachmentDedupKey): boolean {
  return Object.values(draft.attachmentDedupKeys).some((existing) => existing.hash === key.hash && existing.source === key.source);
}

function fileKey(file: File): string {
  return `${file.name}:${file.type}:${file.size}:${file.lastModified}`;
}

function clipboardFiles(data: DataTransfer): File[] {
  const files = Array.from(data.files);
  const seen = new Set(files.map(fileKey));
  for (const item of Array.from(data.items)) {
    if (item.kind !== "file") continue;
    const file = item.getAsFile();
    if (!file) continue;
    const key = fileKey(file);
    if (seen.has(key)) continue;
    seen.add(key);
    files.push(file);
  }
  return files;
}

function clipboardHasImageHint(data: DataTransfer): boolean {
  const imageType = (value: string) => {
    const type = value.toLowerCase();
    return type.startsWith("image/") || type.includes("png") || type.includes("jpeg") || type.includes("jpg") || type.includes("tiff");
  };
  return Array.from(data.items).some((item) => imageType(item.type)) || Array.from(data.types).some(imageType);
}

function isPasteShortcut(e: KeyboardEvent<HTMLElement>): boolean {
  return e.key.toLowerCase() === "v" && (e.metaKey || e.ctrlKey) && !e.altKey;
}

async function dataURLHash(dataUrl: string): Promise<string> {
  try {
    const res = await fetch(dataUrl);
    return sha256(await res.blob());
  } catch {
    return "";
  }
}

function composerMaxHeight(): number {
  if (typeof window === "undefined") return COMPOSER_MAX_HEIGHT;
  return Math.max(COMPOSER_MIN_HEIGHT, Math.min(COMPOSER_MAX_HEIGHT, Math.floor(window.innerHeight * COMPOSER_MAX_VIEWPORT_RATIO)));
}

// Hero (creation) input cap: the old 96px hard cap clipped longer drafts
// before the card autosize took over; give the hero min(30vh, 160px) so a
// visible scrollbar takes over instead (#8494/#8742/#9019).
function composerHeroInputMaxHeight(): number {
  if (typeof window === "undefined") return 160;
  return Math.min(Math.floor(window.innerHeight * 0.3), 160);
}

// The rendered card includes the run strip while a turn runs; subtract it to
// recover the user's logical height when measuring from the DOM.
function composerLogicalHeight(card: HTMLElement): number {
  const strip = card.querySelector(".composer-run-strip");
  const stripHeight = strip ? strip.getBoundingClientRect().height : 0;
  return card.getBoundingClientRect().height - stripHeight;
}

function clampComposerHeight(height: number): number {
  return Math.min(Math.max(Math.round(height), COMPOSER_MIN_HEIGHT), composerMaxHeight());
}

function loadComposerHeight(): number | null {
  return loadOptionalLayoutSize("composerHeight", clampComposerHeight);
}

function fmtElapsed(ms: number): string {
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  return `${Math.floor(s / 60)}m ${s % 60}s`;
}

// --- past:chats hover preview helpers (PR-C2) ---
// Pure formatting helpers used by the past:chats list tooltip. They never read
// from disk, never call PreviewSession — they only shape the data that already
// lives in the SessionMeta snapshot we fetched on entry.
const PAST_CHAT_PREVIEW_MAX = 200;

function truncatePreview(value?: string, max = PAST_CHAT_PREVIEW_MAX): string {
  const text = (value || "").trim();
  if (text.length <= max) return text;
  return `${text.slice(0, max)}...`;
}

function fmtSessionTime(value?: number): string {
  if (!value) return "";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "";
  const yyyy = d.getFullYear();
  const mm = String(d.getMonth() + 1).padStart(2, "0");
  const dd = String(d.getDate()).padStart(2, "0");
  const hh = String(d.getHours()).padStart(2, "0");
  const mi = String(d.getMinutes()).padStart(2, "0");
  return `${yyyy}-${mm}-${dd} ${hh}:${mi}`;
}

function pastChatTitle(session: SessionMeta): string {
  return session.title || session.topicTitle || session.preview || "Untitled";
}

function useTick(on: boolean): number {
  const [, setN] = useState(0);
  useEffect(() => {
    if (!on) return;
    const id = window.setInterval(() => setN((n) => n + 1), 1000);
    return () => window.clearInterval(id);
  }, [on]);
  return Date.now();
}

// --- past:chats session reference → prompt context (PR-B) ---
// Send-side helpers for "@past:chats" session references. PR-A wired the menu and
// the composer-context card; this layer reads each referenced session through the
// existing PreviewSession API and prepends a compact user/assistant transcript to
// submitText so the model sees the referenced chat as background context.
const SESSION_REF_MAX_MESSAGES = 30;
const SESSION_REF_MAX_CHARS = 20_000;
const PAST_CHATS_MENU_ITEM = "past:chats";

// limitSessionMessages keeps the most recent useful messages within a char budget.
// Walks from the end so the truncation is always "drop the oldest", which matches
// the intuition that the latest turns are the relevant ones for follow-up.
function limitSessionMessages(
  messages: HistoryMessage[],
  maxMessages = SESSION_REF_MAX_MESSAGES,
  maxChars = SESSION_REF_MAX_CHARS,
): { messages: HistoryMessage[]; truncated: boolean } {
  const useful = messages
    .filter(
      (m) =>
        (m.role === "user" || m.role === "assistant") &&
        typeof m.content === "string" &&
        m.content.trim().length > 0,
    )
    .slice(-maxMessages);
  const result: HistoryMessage[] = [];
  let total = 0;
  let truncated = useful.length >= maxMessages;
  for (let i = useful.length - 1; i >= 0; i--) {
    const msg = useful[i];
    const content = msg.content.trim();
    if (total + content.length > maxChars) {
      truncated = true;
      break;
    }
    result.unshift({ ...msg, content });
    total += content.length;
  }
  if (result.length < useful.length) truncated = true;
  return { messages: result, truncated };
}

// formatSessionContext renders one referenced session as a labelled transcript.
// Falls back to a "no usable messages" note when filtering empties the list so
// the model still sees that something was referenced.
function formatSessionContext(
  ref: SessionReference,
  messages: HistoryMessage[],
  truncated: boolean,
  t: Translator,
): string {
  const body = messages
    .map((m) => `${m.role === "user" ? t("composer.sessionContextUser") : t("composer.sessionContextAssistant")}: ${m.content.trim()}`)
    .join("\n\n");
  return [
    `[${t("composer.sessionContextSession", { title: ref.title })}]`,
    truncated ? t("composer.sessionContextTruncated") : "",
    body || t("composer.sessionContextEmpty"),
  ]
    .filter(Boolean)
    .join("\n");
}

// buildSessionContext reads each referenced session, formats the most recent
// slice, and joins them with a separator. A single failed read must not block
// the others; a localized read-failure note marks the bad one and the
// remaining refs still flow through.
async function buildSessionContext(refs: SessionReference[], t: Translator): Promise<string> {
  if (refs.length === 0) return "";
  let context = `${t("composer.sessionContextHeader")}\n\n`;
  for (const ref of refs) {
    try {
      const raw = await app.PreviewSession(ref.path);
      const limited = limitSessionMessages(asArray(raw));
      context += `${formatSessionContext(ref, limited.messages, limited.truncated, t)}\n\n---\n\n`;
    } catch (error) {
      console.error("[past:chats] failed to preview session", ref.path, error);
      context += `[${t("composer.sessionContextSession", { title: ref.title })}]\n${t("composer.sessionContextReadFailed")}\n\n---\n\n`;
    }
  }
  context += `${t("composer.sessionContextFooter")}\n`;
  return context;
}

export function Composer({
  running,
  collaborationMode,
  toolApprovalMode,
  qualityFloor,
  floorInferred,
  turnPhase,
  goal,
  goalStatus,
  goalRuntime,
  cwd,
  modelLabel,
  commandCatalog,
  imageInputEnabled = true,
  imageUnderstandingEnabled = false,
  attachmentInputEnabled = true,
  tabId, turnId,
  effort,
  onSend,
  onSteer,
  localDurableGuidance = true,
  onCancel,
  onCycleMode,
  onSetMode,
  onSetCollaborationMode,
  onSetToolApprovalMode,
  onSetQualityFloor,
  onToggleYoloApprovalMode,
  onClearGoal,
  onPauseGoal,
  onResumeGoal,
  onSwitchModel,
  onSetEffort,
  insertRequest,
  selectedTextRequest,
  disabled,
  submitDisabled = false,
  readOnly = false,
  decisionPending = false,
  ready,
  turnStartAt,
  turnWaitAccumMs = 0,
  promptWaitStartedAt,
  turnTokens,
  turnOutputTokens,
  turnOutputCharsAtUsage,
  turnModelActiveAt,
  turnModelActiveMs = 0,
  liveStore,
  turnArgChars = 0,
  retry,
  suspendedByDecision = false,
  pendingApprovalLabel,
  pendingAsk = false,
  transientDismissSignal,
  sessionKey,
  inboxSessionPath,
  workspaceScopeKey,
  fileRefRefreshKey,
  guidanceConsumedKey,
  guidanceConsumedItemId,
  guidanceConsumedText,
  guidanceQueuePreviewItems,
  showContextWindowRing = false,
  heroMode = false,
  context,
  turnCost,
  turnRateBand,
  currency,
  cacheHitTokens,
  cacheMissTokens,
  balance,
  pinnedFiles,
  onInvocationMetadataChange,
}: {
  running: boolean;
  collaborationMode: CollaborationMode;
  toolApprovalMode: ToolApprovalMode;
  qualityFloor?: QualityFloor;
  floorInferred?: boolean;
  /** Host turn phase: working | checking | verifying | reviewing */
  turnPhase?: string;
  goal?: string;
  goalStatus?: string;
  goalRuntime?: GoalRuntime;
  cwd?: string;
  modelLabel: string;
  commandCatalog?: readonly CommandInfo[];
  imageInputEnabled?: boolean;
  /** True when text-only image turns are preprocessed by a configured vision model. */
  imageUnderstandingEnabled?: boolean;
  /** False for remote sessions because local filesystem paths are not portable to Serve. */
  attachmentInputEnabled?: boolean;
  tabId?: string; turnId?: string;
  effort?: EffortInfo;
  onSend: (displayText: string, submitText?: string, tabId?: string, structured?: StructuredInvocationSubmit) => void | Promise<void>;
  onInvocationMetadataChange?: (metadata: Record<string, { kind: "skill" | "subagent"; color?: string }>) => void;
  onSteer?: (submitText: string, tabId?: string) => void | Promise<void>;
  /** False when the owning surface provides its own durable remote inbox. */
  localDurableGuidance?: boolean;
  // Returns the un-sent text plus the exact durable queue IDs the backend
  // confirmed were withdrawn and are therefore safe to restore.
  onCancel: (queuedItemIDs?: string[]) => Promise<CancelOutcome>;
  onCycleMode: () => void;
  onSetMode: (mode: Mode) => void;
  onSetCollaborationMode: (mode: CollaborationMode) => void;
  onSetToolApprovalMode: (mode: ToolApprovalMode) => void;
  onSetQualityFloor?: (floor: QualityFloor) => void;
  onToggleYoloApprovalMode: () => void;
  onClearGoal: () => void;
  onPauseGoal: () => void;
  onResumeGoal: () => void;
  onSwitchModel: (name: string) => boolean | Promise<boolean>;
  onSetEffort: (level: string) => void;
  insertRequest?: ComposerInsertRequest | null;
  selectedTextRequest?: SelectedTextInsertRequest | null;
  disabled?: boolean;
  submitDisabled?: boolean;
  readOnly?: boolean;
  decisionPending?: boolean;
  // ready/cwd/running/workspaceScopeKey re-trigger the command fetch: Commands() returns only
  // built-ins until boot.Build finishes (the controller, hence skills/custom/MCP,
  // is nil before then), the available set changes when the workspace switches,
  // and a completed turn may have installed skills or MCP prompts.
  ready?: boolean;
  turnStartAt?: number;
  // Tab-scoped user-wait from the controller (approval/ask). Counts while the
  // tab is in the background so Composer does not invent a wait start on focus.
  turnWaitAccumMs?: number;
  promptWaitStartedAt?: number;
  turnTokens?: number;
  // Completion + reasoning tokens accumulated this turn — feeds the streaming
  // TPS readout in the run ticker (composer-run-strip).
  turnOutputTokens?: number;
  // Live text+reasoning characters already covered by turnOutputTokens.
  turnOutputCharsAtUsage?: number;
  // Active provider-output time for the current turn; excludes tool gaps.
  turnModelActiveAt?: number;
  turnModelActiveMs?: number;
  // Live-stream subscription for the character-count TPS fallback (chars ÷ 4)
  // when the provider does not emit per-chunk usage events with token counts
  // during streaming. Subscribing here keeps text deltas off the main state
  // tree — only the composer re-renders, matching the controller's live-store
  // contract (pure stream deltas must not re-render the controller owner).
  liveStore?: ControllerLiveStore;
  // Streaming argument characters provide estimated progress before usage arrives.
  turnArgChars?: number;
  retry?: RecoveryRetry;
  // True while a footer decision surface (approval / ask / clear context) owns
  // the UI. Pauses the model-work ticker without rendering a "waiting approval"
  // run strip (the decision card already conveys that state).
  suspendedByDecision?: boolean;
  // Legacy strip labels kept for isolated unit tests; App prefers
  // suspendedByDecision so the decision card is not duplicated in the strip.
  pendingApprovalLabel?: string | null;
  pendingAsk?: boolean;
  transientDismissSignal?: number;
  sessionKey?: string;
  inboxSessionPath?: string;
  workspaceScopeKey?: string;
  fileRefRefreshKey?: number | string;
  guidanceConsumedKey?: string;
  guidanceConsumedItemId?: string;
  guidanceConsumedText?: string;
  guidanceQueuePreviewItems?: readonly string[];
  showContextWindowRing?: boolean;
  // Creation empty-session hero: slim centered composer under the welcome
  // headline (hides task/approval chrome; keeps model + effort).
  heroMode?: boolean;
  context?: ContextInfo;
  turnCost?: number;
  turnRateBand?: string;
  currency?: string;
  cacheHitTokens?: number;
  cacheMissTokens?: number;
  balance?: BalanceInfo;
  pinnedFiles?: import("../lib/pinnedContextBridge").PinnedFileInfo[];
}) {
  const { t, locale } = useI18n();
  const { showToast } = useToast();
  const shortcutPlatform = useMemo(() => detectShortcutPlatform(), []);
  const sendComboLabel = useShortcutComboLabel("composer.send");
  const undoComboLabel = useShortcutComboLabel("composer.undo");
  const redoComboLabel = useShortcutComboLabel("composer.redo");
  const yoloComboLabel = useShortcutComboLabel("toolApproval.yolo");
  const draftKey = sessionKey || tabId || DEFAULT_COMPOSER_DRAFT_KEY;
  const inboxSessionKey = inboxScopeKey(inboxSessionPath, workspaceScopeKey);
  const now = useTick(running);
  const [text, setText] = useState("");
  const [attachments, setAttachments] = useState<Attachment[]>([]);
  const [imageViewer, setImageViewer] = useState<{ open: boolean; url: string; name: string }>({ open: false, url: "", name: "" });
  const openComposerImageViewer = useCallback((url: string, name: string) => {
    setImageViewer({ open: true, url, name });
  }, []);

  const closeComposerImageViewer = useCallback(() => {
    setImageViewer((prev) => (prev.open ? { ...prev, open: false } : prev));
  }, []);

  const [workspaceRefs, setWorkspaceRefs] = useState<WorkspaceReference[]>([]);
  const [invocations, setInvocations] = useState<ComposerInvocation[]>([]);
  const [plainSelection, setPlainSelection] = useState<RichComposerSelection>({ start: 0, end: 0 });
  const [richSelection, setRichSelection] = useState<RichComposerSelection>({ start: 0, end: 0 });
  const [richSlashQuery, setRichSlashQuery] = useState<RichSlashQuery | null>(null);
  const [pastedBlocks, setPastedBlocks] = useState<PastedBlock[]>([]);
  const [openPastedLabels, setOpenPastedLabels] = useState<string[]>([]);
  const [pendingPaste, setPendingPaste] = useState(0);
  const pendingPasteRef = useRef(0);
  const pastedBlocksRef = useRef<PastedBlock[]>([]);
  const nextPasteId = useRef(1);
  const nextInvocationId = useRef(1);
  const [active, setActive] = useState(0);
  const [dismissed, setDismissed] = useState(false);
  const [dragOver, setDragOver] = useState(false);
  // A saved manual height is a floor, not a hard cap: longer drafts may grow
  // above it and return to it when their content shrinks.
  const [composerHeight, setComposerHeight] = useState<number | null>(loadComposerHeight);
  const [composerResizing, setComposerResizing] = useState(false);
  const [textareaAutoHeight, setTextareaAutoHeight] = useState<number | null>(null);
  const [textareaAutoOverflow, setTextareaAutoOverflow] = useState(false);
  const [intentMenuOpen, setIntentMenuOpen] = useState(false);
  const [intentMenuClosing, setIntentMenuClosing] = useState(false);
  const [moreMenuOpen, setMoreMenuOpen] = useState(false);
  const [moreMenuClosing, setMoreMenuClosing] = useState(false);
  const [contentMenuOpen, setContentMenuOpen] = useState(false);
  const [showPastChats, setShowPastChats] = useState(false);
  const [directPastChats, setDirectPastChats] = useState(false);
  const [pastChats, setPastChats] = useState<SessionMeta[]>([]);
  const [pastChatQuery, setPastChatQuery] = useState("");
  const [sessionRefs, setSessionRefs] = useState<SessionReference[]>([]);
  const [selectedTextRefs, setSelectedTextRefs] = useState<SelectedTextReference[]>([]);
  const [pendingGuidance, setPendingGuidance] = useState<PendingGuidance[]>([]);
  const [guidanceExpanded, setGuidanceExpanded] = useState(false);
  const [guidanceSendingId, setGuidanceSendingId] = useState<string | null>(null);
  const [guidanceRetryNonce, setGuidanceRetryNonce] = useState(0);
  const [guidanceDraftKey, setGuidanceDraftKey] = useState(draftKey);
  const pendingGuidanceRef = useRef<PendingGuidance[]>([]);
  const guidanceExpandedRef = useRef(false);
  const guidanceSendingIdRef = useRef<string | null>(null);
  const [loadingPastChats, setLoadingPastChats] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const cancelSettlingDraftsRef = useRef(new Set<string>());
  const [, setCancelSettlingRevision] = useState(0);
  const [inputMenuPoint, setInputMenuPoint] = useState<ContextMenuPoint | null>(null);
  const [composerPrompt, setComposerPrompt] = useState<string | null>(null);
  // Prompt history navigation (plain ↑/↓)
  // Use refs for values read inside async closures to avoid stale captures
  // on rapid key presses (the React closure trap).
  const historyIndexRef = useRef(-1);
  const historyEntriesRef = useRef<PromptHistoryEntry[]>([]);
  const historyLoadRef = useRef<Promise<void> | null>(null);
  const historyGenerationRef = useRef(cacheGeneration());
  // historyIndex state is written (via setHistoryIndex) for potential future
  // UI feedback (e.g. "3/200" indicator); currently unused in render.
  const [, setHistoryIndex] = useState(-1);
  const savedTextRef = useRef("");
  const taRef = useRef<HTMLTextAreaElement>(null);
  const measureTaRef = useRef<HTMLTextAreaElement>(null);
  const richInputRef = useRef<RichComposerInputHandle>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const editHistoryByDraftRef = useRef<Record<string, ComposerEditHistory>>({});
  const pendingNativeInputTypeRef = useRef<string | undefined>(undefined);
  const composerCardRef = useRef<HTMLDivElement>(null);
  const composerWrapRef = useRef<HTMLDivElement>(null);
  const contentMenuAnchorRef = useRef<HTMLButtonElement>(null);
  const intentMenuAnchorRef = useRef<HTMLButtonElement>(null);
  const moreMenuAnchorRef = useRef<HTMLButtonElement>(null);
  const intentCloseTimerRef = useRef<number | null>(null);
  const moreCloseTimerRef = useRef<number | null>(null);
  // Creation chrome: hover-open task menus (same pattern as ContextWindowRing).
  const intentHoverTimerRef = useRef<number | null>(null);
  const creationChrome = showContextWindowRing;
  const wasRunningByDraftRef = useRef<Record<string, boolean>>({ [draftKey]: running });
  const pastChatSearchComposingRef = useRef(false);
  const pastChatSearchLastCompositionEndAt = useRef(0);
  const lastSelectionRef = useRef({ start: 0, end: 0 });
  const consumedInsertIdByDraftRef = useRef<Record<string, number>>({});
  const consumedSelectedTextIdByDraftRef = useRef<Record<string, number>>({});
  const lastTransientDismissSignal = useRef(transientDismissSignal);
  const lastGuidanceConsumedKeyByDraftRef = useRef<Record<string, string | undefined>>(
    guidanceConsumedKey ? { [draftKey]: guidanceConsumedKey } : {},
  );
  const guidanceReceiptTrackerRef = useRef<GuidanceReceiptTracker | null>(null);
  guidanceReceiptTrackerRef.current ??= createGuidanceReceiptTracker();
  const selfDispatchedGuidanceByDraftRef = useRef<Record<string, string[]>>({});
  const submittingRef = useRef(false);
  const nativeClipboardPasteTimerRef = useRef<number | null>(null);
  // Snapshot of the current cwd so async callbacks (openPastChats) can detect
  // workspace switches and discard stale responses (issue #3601).
  const cwdRef = useRef(cwd);
  cwdRef.current = cwd;
  const attachmentDedupRef = useRef(new DedupIndex());
  const attachmentDedupKeysRef = useRef<Record<string, AttachmentDedupKey>>({});
  const guidanceQueuePreviewKey = (guidanceQueuePreviewItems ?? []).map((item) => item.trim()).filter(Boolean).join("\n");
  const draftsBySessionRef = useRef<Record<string, ComposerDraft>>({});
  const activeDraftKeyRef = useRef(draftKey);
  const draftActivationEpochRef = useRef(0);
  const textRef = useRef(text);
  const invocationsRef = useRef(invocations);
  const attachmentsRef = useRef(attachments);
  const workspaceRefsRef = useRef(workspaceRefs);
  const openPastedLabelsRef = useRef(openPastedLabels);
  const sessionRefsRef = useRef(sessionRefs);
  const selectedTextRefsRef = useRef(selectedTextRefs);
  // Plain-textarea IME freeze: while a composition is active the textarea
  // renders uncontrolled so no re-render can cancel it (#8593/#8409); the
  // hook owns the composition lifecycle, resync, and force-sync semantics.
  const { composingRef, lastCompositionEndAt, trackImeInputChange } = useComposerImeGuard({
    taRef,
    text,
    invocationCount: invocations.length,
    textRef,
    lastSelectionRef,
    setText,
    setPlainSelection,
  });
  textRef.current = text;
  invocationsRef.current = invocations;
  attachmentsRef.current = attachments;
  workspaceRefsRef.current = workspaceRefs;
  pastedBlocksRef.current = pastedBlocks;
  openPastedLabelsRef.current = openPastedLabels;
  sessionRefsRef.current = sessionRefs;
  selectedTextRefsRef.current = selectedTextRefs;
  pendingGuidanceRef.current = pendingGuidance;
  guidanceExpandedRef.current = guidanceExpanded;
  guidanceSendingIdRef.current = guidanceSendingId;
  pendingPasteRef.current = pendingPaste;
  submittingRef.current = submitting;

  const snapshotComposerDraft = (): ComposerDraft => ({
    text: textRef.current,
    invocations: invocationsRef.current.map((invocation) => ({ ...invocation, command: { ...invocation.command } })),
    attachments: [...attachmentsRef.current],
    workspaceRefs: [...workspaceRefsRef.current],
    pastedBlocks: [...pastedBlocksRef.current],
    openPastedLabels: [...openPastedLabelsRef.current],
    sessionRefs: [...sessionRefsRef.current],
    selectedTextRefs: selectedTextRefsRef.current.map((reference) => ({ ...reference })),
    attachmentDedupKeys: { ...attachmentDedupKeysRef.current },
    nextPasteId: nextPasteId.current,
    historyIndex: historyIndexRef.current,
    savedText: savedTextRef.current,
    pendingGuidance: pendingGuidanceRef.current.map((item) => ({ ...item })),
    guidanceExpanded: guidanceExpandedRef.current,
    guidanceSendingId: guidanceSendingIdRef.current,
    pendingPaste: pendingPasteRef.current,
    submitting: submittingRef.current,
  });

  const restoreComposerDraft = (draft: ComposerDraft) => {
    const next = cloneComposerDraft(draft);
    textRef.current = next.text;
    invocationsRef.current = next.invocations;
    attachmentsRef.current = next.attachments;
    workspaceRefsRef.current = next.workspaceRefs;
    openPastedLabelsRef.current = next.openPastedLabels;
    sessionRefsRef.current = next.sessionRefs;
    selectedTextRefsRef.current = next.selectedTextRefs;
    setText(next.text);
    setInvocations(next.invocations);
    setAttachments(next.attachments);
    setWorkspaceRefs(next.workspaceRefs);
    pastedBlocksRef.current = next.pastedBlocks;
    setPastedBlocks(next.pastedBlocks);
    setOpenPastedLabels(next.openPastedLabels);
    setSessionRefs(next.sessionRefs);
    setSelectedTextRefs(next.selectedTextRefs);
    attachmentDedupKeysRef.current = next.attachmentDedupKeys;
    attachmentDedupRef.current = attachmentDedupFromKeys(next.attachmentDedupKeys);
    nextPasteId.current = next.nextPasteId;
    historyIndexRef.current = next.historyIndex;
    savedTextRef.current = next.savedText;
    pendingGuidanceRef.current = next.pendingGuidance;
    guidanceExpandedRef.current = next.guidanceExpanded;
    guidanceSendingIdRef.current = next.guidanceSendingId;
    pendingPasteRef.current = next.pendingPaste;
    submittingRef.current = next.submitting;
    setPendingGuidance(next.pendingGuidance);
    setGuidanceExpanded(next.guidanceExpanded);
    setGuidanceSendingId(next.guidanceSendingId);
    setPendingPaste(next.pendingPaste);
    setSubmitting(next.submitting);
    setHistoryIndex(next.historyIndex);
    const restoredSelection = { start: next.text.length, end: next.text.length };
    lastSelectionRef.current = restoredSelection;
    setPlainSelection(restoredSelection);
    setRichSelection(restoredSelection);
    setRichSlashQuery(
      next.invocations.length > 0
        ? slashQueryAt(next.text, restoredSelection)
        : null,
    );
    setComposerPrompt(null);
    setShowPastChats(false);
    setDirectPastChats(false);
    setContentMenuOpen(false);
    setPastChatQuery("");
    setLoadingPastChats(false);
    setActive(0);
    setInputMenuPoint(null);
    setDragOver(false);
    setImageViewer((current) => current.open ? { ...current, open: false } : current);
    setIntentMenuOpen(false);
    setIntentMenuClosing(false);
    setMoreMenuOpen(false);
    setMoreMenuClosing(false);
  };

  const composerEditSnapshot = (
    targetDraftKey: string,
    selection?: RichComposerSelection,
  ): ComposerEditSnapshot => {
    if (targetDraftKey === activeDraftKeyRef.current) {
      return {
        text: textRef.current,
        invocations: invocationsRef.current.map((invocation) => ({ ...invocation, command: { ...invocation.command } })),
        pastedBlocks: [...pastedBlocksRef.current],
        openPastedLabels: [...openPastedLabelsRef.current],
        nextPasteId: nextPasteId.current,
        selection: selection ?? getComposerSelection(),
      };
    }
    const draft = draftsBySessionRef.current[targetDraftKey] ?? emptyComposerDraft();
    const start = Math.min(selection?.start ?? draft.text.length, draft.text.length);
    return {
      text: draft.text,
      invocations: draft.invocations.map((invocation) => ({ ...invocation, command: { ...invocation.command } })),
      pastedBlocks: [...draft.pastedBlocks],
      openPastedLabels: [...draft.openPastedLabels],
      nextPasteId: draft.nextPasteId,
      selection: {
        start,
        end: Math.min(selection?.end ?? start, draft.text.length),
        afterInvocationId: selection?.afterInvocationId,
      },
    };
  };

  const composerEditStateMatches = (left: ComposerEditSnapshot, right: ComposerEditSnapshot): boolean =>
    left.text === right.text
    && left.nextPasteId === right.nextPasteId
    && JSON.stringify(left.invocations) === JSON.stringify(right.invocations)
    && JSON.stringify(left.pastedBlocks) === JSON.stringify(right.pastedBlocks);

  const editHistoryForDraft = (targetDraftKey: string): ComposerEditHistory => {
    const existing = editHistoryByDraftRef.current[targetDraftKey];
    if (existing) return existing;
    const created: ComposerEditHistory = {
      undo: [],
      redo: [],
      undoNativeBarrier: false,
      redoNativeBarrier: false,
    };
    editHistoryByDraftRef.current[targetDraftKey] = created;
    return created;
  };

  const clearComposerEditHistory = (targetDraftKey: string) => {
    delete editHistoryByDraftRef.current[targetDraftKey];
  };

  const syncComposerNativeHistory = (targetDraftKey: string, inputType?: string) => {
    const history = editHistoryByDraftRef.current[targetDraftKey];
    if (!history) return;
    const current = composerEditSnapshot(targetDraftKey);
    const undoTransaction = history.undo[history.undo.length - 1];
    const redoTransaction = history.redo[history.redo.length - 1];

    if (inputType === "historyUndo") {
      history.undoNativeBarrier = Boolean(
        undoTransaction && !composerEditStateMatches(current, undoTransaction.after),
      );
      // The browser has just created at least one native redo unit. Keep it
      // ahead of any older custom redo transaction until historyRedo reaches
      // that transaction's boundary again.
      history.redoNativeBarrier = history.undo.length > 0 || history.redo.length > 0;
      return;
    }

    if (inputType === "historyRedo") {
      history.undoNativeBarrier = Boolean(
        undoTransaction && !composerEditStateMatches(current, undoTransaction.after),
      );
      if (redoTransaction) {
        history.redoNativeBarrier = !composerEditStateMatches(current, redoTransaction.before);
      } else if (undoTransaction) {
        history.redoNativeBarrier = !composerEditStateMatches(current, undoTransaction.after);
      } else {
        history.redoNativeBarrier = false;
      }
      return;
    }

    // A new browser edit sits above the latest custom transaction even when
    // its net text later returns to the same value (type then Backspace).
    history.undoNativeBarrier = history.undo.length > 0;
    history.redo = [];
    history.redoNativeBarrier = false;
  };

  const recordComposerEdit = (
    targetDraftKey: string,
    before: ComposerEditSnapshot,
    after: ComposerEditSnapshot,
  ) => {
    if (composerEditStateMatches(before, after)) return;
    const history = editHistoryForDraft(targetDraftKey);
    const previous = history.undo[history.undo.length - 1];
    const nativeBarrierBefore = Boolean(
      previous
      && (
        history.undoNativeBarrier
        || !composerEditStateMatches(before, previous.after)
      ),
    );
    history.undo.push({
      before,
      after,
      nativeBarrierBefore,
      nativeBarrierAfter: false,
    });
    if (history.undo.length > MAX_COMPOSER_EDIT_HISTORY) history.undo.shift();
    history.redo = [];
    history.undoNativeBarrier = false;
    history.redoNativeBarrier = false;
  };

  const restoreComposerEdit = (targetDraftKey: string, snapshot: ComposerEditSnapshot) => {
    const invocations = snapshot.invocations.map((invocation) => ({ ...invocation, command: { ...invocation.command } }));
    const pastedBlocks = [...snapshot.pastedBlocks];
    const openPastedLabels = [...snapshot.openPastedLabels];
    if (targetDraftKey !== activeDraftKeyRef.current) {
      const draft = cloneComposerDraft(draftsBySessionRef.current[targetDraftKey] ?? emptyComposerDraft());
      draft.text = snapshot.text;
      draft.invocations = invocations;
      draft.pastedBlocks = pastedBlocks;
      draft.openPastedLabels = openPastedLabels;
      draft.nextPasteId = snapshot.nextPasteId;
      draftsBySessionRef.current[targetDraftKey] = draft;
      return;
    }
    textRef.current = snapshot.text;
    invocationsRef.current = invocations;
    pastedBlocksRef.current = pastedBlocks;
    openPastedLabelsRef.current = openPastedLabels;
    nextPasteId.current = snapshot.nextPasteId;
    setText(snapshot.text);
    setInvocations(invocations);
    setPastedBlocks(pastedBlocks);
    setOpenPastedLabels(openPastedLabels);
    setComposerPrompt(null);
    resetPromptHistoryNavigation();
    setComposerSelection(
      snapshot.selection.start,
      snapshot.selection.end,
      snapshot.selection.afterInvocationId,
    );
  };

  const canUndoComposerEdit = (targetDraftKey: string): boolean => {
    const history = editHistoryByDraftRef.current[targetDraftKey];
    if (!history || history.undoNativeBarrier) return false;
    const transaction = history.undo[history.undo.length - 1];
    return Boolean(
      transaction
      && composerEditStateMatches(composerEditSnapshot(targetDraftKey), transaction.after),
    );
  };

  const undoComposerEdit = (targetDraftKey: string): boolean => {
    const history = editHistoryByDraftRef.current[targetDraftKey];
    if (!history || !canUndoComposerEdit(targetDraftKey)) return false;
    const transaction = history.undo.pop();
    if (!transaction) return false;
    transaction.nativeBarrierAfter = history.redoNativeBarrier;
    history.redo.push(transaction);
    restoreComposerEdit(targetDraftKey, transaction.before);
    const previous = history.undo[history.undo.length - 1];
    history.undoNativeBarrier = Boolean(previous && transaction.nativeBarrierBefore);
    history.redoNativeBarrier = false;
    return true;
  };

  const canRedoComposerEdit = (targetDraftKey: string): boolean => {
    const history = editHistoryByDraftRef.current[targetDraftKey];
    if (!history || history.redoNativeBarrier) return false;
    const transaction = history.redo[history.redo.length - 1];
    return Boolean(
      transaction
      && composerEditStateMatches(composerEditSnapshot(targetDraftKey), transaction.before),
    );
  };

  const redoComposerEdit = (targetDraftKey: string): boolean => {
    const history = editHistoryByDraftRef.current[targetDraftKey];
    if (!history || !canRedoComposerEdit(targetDraftKey)) return false;
    const transaction = history.redo.pop();
    if (!transaction) return false;
    history.undo.push(transaction);
    restoreComposerEdit(targetDraftKey, transaction.after);
    history.undoNativeBarrier = false;
    const next = history.redo[history.redo.length - 1];
    history.redoNativeBarrier = transaction.nativeBarrierAfter
      || Boolean(next && next.nativeBarrierBefore);
    return true;
  };

  const updatePendingGuidanceForDraft = (
    targetDraftKey: string,
    update: (items: PendingGuidance[]) => PendingGuidance[],
  ) => {
    if (targetDraftKey === activeDraftKeyRef.current) {
      const next = update(pendingGuidanceRef.current);
      pendingGuidanceRef.current = next;
      setPendingGuidance(next);
      return;
    }
    const draft = cloneComposerDraft(draftsBySessionRef.current[targetDraftKey] ?? emptyComposerDraft());
    draft.pendingGuidance = update(draft.pendingGuidance);
    draftsBySessionRef.current[targetDraftKey] = draft;
  };

  const updateGuidanceSendingIdForDraft = (targetDraftKey: string, next: string | null) => {
    if (targetDraftKey === activeDraftKeyRef.current) {
      guidanceSendingIdRef.current = next;
      setGuidanceSendingId(next);
      return;
    }
    const draft = cloneComposerDraft(draftsBySessionRef.current[targetDraftKey] ?? emptyComposerDraft());
    draft.guidanceSendingId = next;
    draftsBySessionRef.current[targetDraftKey] = draft;
  };

  const updatePendingPasteForDraft = (targetDraftKey: string, delta: number) => {
    if (targetDraftKey === activeDraftKeyRef.current) {
      const next = Math.max(0, pendingPasteRef.current + delta);
      pendingPasteRef.current = next;
      setPendingPaste(next);
      return;
    }
    const draft = cloneComposerDraft(draftsBySessionRef.current[targetDraftKey] ?? emptyComposerDraft());
    draft.pendingPaste = Math.max(0, draft.pendingPaste + delta);
    draftsBySessionRef.current[targetDraftKey] = draft;
  };

  const updateSubmittingForDraft = (targetDraftKey: string, next: boolean) => {
    if (targetDraftKey === activeDraftKeyRef.current) {
      submittingRef.current = next;
      setSubmitting(next);
      return;
    }
    const draft = cloneComposerDraft(draftsBySessionRef.current[targetDraftKey] ?? emptyComposerDraft());
    draft.submitting = next;
    draftsBySessionRef.current[targetDraftKey] = draft;
  };

  const draftIsSubmitting = (targetDraftKey: string): boolean =>
    targetDraftKey === activeDraftKeyRef.current
      ? submittingRef.current
      : Boolean(draftsBySessionRef.current[targetDraftKey]?.submitting);

  const draftHasPendingPaste = (targetDraftKey: string): boolean =>
    targetDraftKey === activeDraftKeyRef.current
      ? pendingPasteRef.current > 0
      : (draftsBySessionRef.current[targetDraftKey]?.pendingPaste ?? 0) > 0;

  useLayoutEffect(() => {
    const previousKey = activeDraftKeyRef.current;
    if (previousKey === draftKey) return;
    draftsBySessionRef.current[previousKey] = snapshotComposerDraft();
    draftActivationEpochRef.current += 1;
    activeDraftKeyRef.current = draftKey;
    setGuidanceDraftKey(draftKey);
    restoreComposerDraft(draftsBySessionRef.current[draftKey] ?? emptyComposerDraft());
  }, [draftKey]);

  const applyInboxQueue = useCallback((items: PendingGuidance[]) => updatePendingGuidanceForDraft(draftKey, () => items), [draftKey]);
  const collapseInboxQueue = useCallback(() => setGuidanceExpanded(false), []);
  const refreshInboxQueue = useCallback(() => setGuidanceRetryNonce((value) => value + 1), []);
  useComposerInboxRefresh(tabId, draftKey, guidanceDraftKey, inboxSessionKey, guidanceQueuePreviewKey, guidanceRetryNonce, running, applyInboxQueue, collapseInboxQueue, refreshInboxQueue);

  useEffect(() => {
    return () => {
      draftsBySessionRef.current[activeDraftKeyRef.current] = snapshotComposerDraft();
    };
  }, []);

  const clearNativeClipboardPasteTimer = () => {
    if (nativeClipboardPasteTimerRef.current === null) return;
    window.clearTimeout(nativeClipboardPasteTimerRef.current);
    nativeClipboardPasteTimerRef.current = null;
  };

  useEffect(() => () => clearNativeClipboardPasteTimer(), []);

  useEffect(() => {
    const wasRunning = wasRunningByDraftRef.current[draftKey] ?? running;
    if (wasRunning && !running) {
      setGuidanceExpanded(false);
      if (text.trim() === "") {
        pastedBlocksRef.current = [];
        setPastedBlocks([]);
        setOpenPastedLabels([]);
      }
    }
    wasRunningByDraftRef.current[draftKey] = running;
  }, [draftKey, running, text]);

  // Legacy/local preview items still need the frontend-owned send path; durable items
  // are dispatched and acknowledged exactly once by the Controller after TurnDone.
  // The draft-key guard prevents this compatibility path from using a newly selected session's onSend.
  useEffect(() => {
    // Never auto-send guidance while a decision surface owns the footer —
    // the draft must stay intact until the user finishes the decision.
    if (guidanceDraftKey !== draftKey || running || submitDisabled || suspendedByDecision) return;
    const next = pendingGuidance[0];
    if (next?.id.startsWith("local-")) void sendQueuedGuidance(next, draftKey);
  }, [draftKey, guidanceDraftKey, guidanceRetryNonce, running, submitDisabled, pendingGuidance, suspendedByDecision]);

  useEffect(() => {
    if (guidanceExpanded && pendingGuidance.length <= 2) setGuidanceExpanded(false);
  }, [guidanceExpanded, pendingGuidance.length]);

  // --- slash commands ---
  const commands = useComposerCommandCatalog(commandCatalog, ready ?? false, cwd, running, workspaceScopeKey ?? "");
  useEffect(() => {
    onInvocationMetadataChange?.(Object.fromEntries(
      commands
        .filter(commandUsesStructuredInvocation)
        .map((command) => [command.name, {
          kind: command.kind === "subagent" ? "subagent" : "skill",
          color: command.color,
        }]),
    ));
  }, [commands, onInvocationMetadataChange]);

  const slashText = useMemo(() => text.replace(/[\r\n]+$/u, ""), [text]);
  const plainSlashQuery = useMemo(() => slashQueryAt(slashText, {
    start: Math.min(plainSelection.start, slashText.length),
    end: Math.min(plainSelection.end, slashText.length),
  }), [plainSelection, slashText]);
  const activeSlashQuery = invocations.length > 0 ? richSlashQuery : plainSlashQuery;
  const slashQuery = activeSlashQuery?.query ?? null;
  const slashMatches = useMemo(
    () => slashQuery === null
      ? []
      : sortSlashCommandsForMenu(commands.filter((c) => c.name.toLowerCase().includes(slashQuery))),
    [slashQuery, commands],
  );
  const slashCommandAtStart = Boolean(
    activeSlashQuery
    && invocations.length === 0
    && slashText.slice(0, activeSlashQuery.from).trim() === "",
  );
  const slashCommandDisabled = useCallback(
    (command: CommandInfo) => !commandAvailableAtSlashPosition(command, slashCommandAtStart),
    [slashCommandAtStart],
  );
  const slashSelectableIndices = useMemo(
    () => slashMatches.flatMap((command, index) => slashCommandDisabled(command) ? [] : [index]),
    [slashCommandDisabled, slashMatches],
  );
  const slashQueryKey = activeSlashQuery
    ? `${activeSlashQuery.from}:${activeSlashQuery.to}:${activeSlashQuery.query}`
    : "";

  // --- slash argument completion ("/cmd <args>") --- mirrors the CLI: once past
  // the command word, the backend suggests sub-commands (/skill → list/show/…,
  // /mcp → add/remove, /model → refs). Fetched from app.SlashArgs. Debounced
  // by 120ms so rapid typing doesn't flood the backend with IPC calls — the
  // menu only updates after the user pauses.
  const [argRes, setArgRes] = useState<SlashArgsResult | null>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => {
    if (invocations.length > 0 || !slashText.startsWith("/") || !/\s/.test(slashText)) {
      setArgRes(null);
      return;
    }
    let live = true;
    clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => {
      app
        .SlashArgs(slashText)
        .then((r) => {
          if (!live) return;
          // Drop suggestions that wouldn't change the input — the token is already
          // fully typed (e.g. "/skill list" offering "list"). Otherwise the menu
          // lingers on a complete command and Enter keeps "accepting" a no-op
          // instead of sending. (Defense-in-depth: the backend filters these too.)
          // r.items can arrive as null (an empty Go slice serializes to JSON null),
          // so guard before filtering — otherwise the throw is swallowed and the
          // stale menu from the previous keystroke lingers (the /skill list bug).
          const items = asArray(r?.items);
          const from = r?.from ?? 0;
          const useful = items.filter((it) => slashText.slice(0, from) + it.insert !== slashText);
          setArgRes(useful.length > 0 ? { items: useful, from } : null);
          setActive(0);
        })
        .catch(() => {});
    }, 120);
    return () => {
      live = false;
      clearTimeout(debounceRef.current);
    };
  }, [invocations.length, slashText]);

  // --- @ file references (token at the end of the text) ---
  // atRaw is everything after a trailing "@token"; atDir is its path up to the
  // last "/", atFrag the part after. The menu lists one directory level (atDir)
  // and filters by atFrag — descending one level per pick.
  const activeAtToken = useMemo(() => activeFileReferenceToken(text), [text]);
  const atRaw = activeAtToken?.raw ?? null;
  const atDir = activeAtToken?.dir ?? "";
  const atFrag = activeAtToken?.frag ?? "";
  const pastChatToken = useMemo(() => activePastChatToken(text), [text]);
  const pastChatTokenQuery = pastChatToken?.query ?? null;

  const [entries, setEntries] = useState<DirEntry[]>([]);
  const [searchEntries, setSearchEntries] = useState<DirEntry[]>([]);
  const dirCache = useRef<Record<string, DirEntry[]>>({});
  const searchCache = useRef<Record<string, FileRefSearchCacheEntry>>({});
  const fileRefTabId = tabId ?? "";
  const fileRefScopeKey = workspaceScopeKey ?? `${fileRefTabId}\u0000${cwd ?? ""}`;

  const clearFileRefState = useCallback(() => {
    dirCache.current = {};
    searchCache.current = {};
    setEntries([]);
    setSearchEntries([]);
    setShowPastChats(false);
    setPastChats([]);
    setPastChatQuery("");
    setLoadingPastChats(false);
    setActive(0);
    setDismissed(false);
  }, []);

  // Controller/session changes invalidate @ mention state even when tab and
  // workspace identities stay the same (saved-session rebinds and rebuilds).
  const prevFileRefScopeRef = useRef(fileRefScopeKey);
  useEffect(() => {
    if (prevFileRefScopeRef.current === fileRefScopeKey) return;
    prevFileRefScopeRef.current = fileRefScopeKey;
    clearFileRefState();
  }, [clearFileRefState, fileRefScopeKey]);

  const prevFileRefRefreshKeyRef = useRef(fileRefRefreshKey);
  useEffect(() => {
    if (prevFileRefRefreshKeyRef.current === fileRefRefreshKey) return;
    prevFileRefRefreshKeyRef.current = fileRefRefreshKey;
    clearFileRefState();
  }, [clearFileRefState, fileRefRefreshKey]);

  useEffect(() => {
    if (atRaw === null) return;
    const cached = dirCache.current[atDir];
    if (cached) {
      setEntries(cached);
    } else {
      setEntries([]);
    }
    let live = true;
    app
      .ListDirForTab(fileRefTabId, unescapeRefPath(atDir))
      .then((es) => {
        const list = asArray(es);
        if (!live) return;
        dirCache.current[atDir] = list;
        setEntries(list);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
    // Re-fetch when the menu opens, the directory level changes, or the
    // workspace tree refreshes; cached data is only a fast first paint.
  }, [atRaw === null, atDir, fileRefRefreshKey, fileRefScopeKey, fileRefTabId]);
  useEffect(() => {
    if (atRaw === null || atDir !== "" || atFrag === "") {
      setSearchEntries([]);
      return;
    }
    const cached = searchCache.current[atFrag];
    if (cached) {
      setSearchEntries(cached.entries);
      if (Date.now() - cached.cachedAt < FILE_REF_SEARCH_CACHE_TTL_MS) return;
    } else {
      setSearchEntries([]);
    }
    let live = true;
    app
      .SearchFileRefsForTab(fileRefTabId, atFrag)
      .then((es) => {
        const list = asArray(es);
        if (!live) return;
        searchCache.current[atFrag] = { entries: list, cachedAt: Date.now() };
        setSearchEntries(list);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [atRaw === null, atDir, atFrag, fileRefRefreshKey, fileRefScopeKey, fileRefTabId]);
  const atMatches = useMemo(
    () => {
      if (atRaw === null) return [];
      return filterAtMatches(entries, searchEntries, atFrag);
    },
    [atRaw, atFrag, entries, searchEntries],
  );

  // Unified menu item model for the @ menu. "past:chats" is a real selectable
  // item (kind "pastChats"), not an active===0 special case.
  type AtMenuItem =
    | { kind: "pastChats" }
    | { kind: "file"; entry: DirEntry };

  const includePastChatsItem = atRaw !== null && atDir === "" && (atFrag === "" || PAST_CHATS_MENU_ITEM.startsWith(atFrag));

  const atMenuItems = useMemo<AtMenuItem[]>(
    () => [
      ...(includePastChatsItem ? [{ kind: "pastChats" as const }] : []),
      ...atMatches.map((entry) => ({ kind: "file" as const, entry })),
    ],
    [includePastChatsItem, atMatches],
  );
  const atMenuItemKey = useCallback(
    (item: AtMenuItem) => item.kind === "pastChats" ? "past:chats" : (item.entry.isDir ? "d:" : "f:") + (item.entry.path || item.entry.name),
    [],
  );

  // --- which menu (if any) is open --- (slash command names win; then slash
  // arguments; then @-refs — they're rarely valid at once)
  const menuMode: "slash" | "slasharg" | "at" | "pastChats" | null =
    directPastChats
      ? "pastChats"
      : slashMatches.length > 0 && !dismissed
        ? "slash"
        : argRes && argRes.items.length > 0 && !dismissed
          ? "slasharg"
          : atRaw !== null && !dismissed
            ? "at"
            : null;
  const menuOpen = menuMode !== null;
  useLayoutEffect(() => {
    if (!menuOpen) return;
    const anchor = composerWrapRef.current;
    if (!anchor) return;
    return observeComposerMenuViewport(anchor);
  }, [menuOpen]);
  const countBase =
    menuMode === "slash"
      ? slashMatches.length
      : menuMode === "slasharg"
        ? argRes!.items.length
        : menuMode === "at"
          ? atMenuItems.length
          : menuMode === "pastChats"
            ? pastChats.length
            : 0;

  // Reset highlight + un-dismiss whenever the active query changes.
  useEffect(() => {
    setActive(0);
    setDismissed(false);
  }, [slashQueryKey, atRaw, pastChatTokenQuery]);

  useEffect(() => {
    if (transientDismissSignal === undefined || transientDismissSignal === lastTransientDismissSignal.current) return;
    lastTransientDismissSignal.current = transientDismissSignal;
    setDismissed(true);
  }, [transientDismissSignal]);

  const takeSelfDispatchedGuidance = useCallback((text: string, targetDraftKey: string): boolean => {
    const selfDispatched = selfDispatchedGuidanceByDraftRef.current[targetDraftKey] ?? [];
    const idx = selfDispatched.findIndex((queued) => guidanceTextMatches(queued, text));
    if (idx < 0) return false;
    selfDispatched.splice(idx, 1);
    if (selfDispatched.length === 0) delete selfDispatchedGuidanceByDraftRef.current[targetDraftKey];
    return true;
  }, []);

  useEffect(() => {
    if (guidanceDraftKey !== draftKey || !guidanceConsumedKey) return;
    if (guidanceConsumedKey === lastGuidanceConsumedKeyByDraftRef.current[draftKey]) return;
    lastGuidanceConsumedKeyByDraftRef.current[draftKey] = guidanceConsumedKey;
    const consumed = (guidanceConsumedText ?? "").trim();
    if (guidanceConsumedItemId) {
      guidanceReceiptTrackerRef.current?.recordConsumed(draftKey, guidanceConsumedItemId);
    }
    if (!guidanceConsumedItemId && consumed && takeSelfDispatchedGuidance(consumed, draftKey)) return;
    updatePendingGuidanceForDraft(draftKey, (items) => {
      if (items.length === 0) return items;
      const byID = guidanceConsumedItemId
        ? items.findIndex((item) => item.id === guidanceConsumedItemId)
        : -1;
      const idx = guidanceConsumedItemId ? byID : consumed
        ? items.findIndex((item) => guidanceTextMatches(item.submitText, consumed) || guidanceTextMatches(item.text, consumed))
        : -1;
      // Only remove on a real match. Steer notices also fire for guidance this
      // client never queued (another window, bot bridge, turn-end flush) —
      // falling back to dropping items[0] silently deleted unrelated queued
      // guidance (#6238).
      if (idx < 0) return items;
      return items.filter((_, index) => index !== idx);
    });
  }, [draftKey, guidanceDraftKey, guidanceConsumedKey, guidanceConsumedItemId, guidanceConsumedText, takeSelfDispatchedGuidance]);

  // When the @ trigger disappears (user deleted the @), close the past:chats
  // sub-menu and reset related state. Without this, showPastChats can outlive
  // the @ token and leave the session list visible with no way to dismiss it.
  useEffect(() => {
    if (menuMode !== "at" && menuMode !== "pastChats" && showPastChats) {
      setShowPastChats(false);
      setPastChatQuery("");
      setActive(0);
    }
  }, [menuMode]);

  useEffect(() => {
    if (menuMode && menuMode !== "pastChats") setContentMenuOpen(false);
  }, [menuMode]);

  // A starting run closes the transient content surfaces. Without this the
  // popover state survives the run (its open prop gates on !running) and the
  // menu would pop back unprompted the moment the turn finishes.
  useEffect(() => {
    if (!running) return;
    setContentMenuOpen(false);
    setDirectPastChats(false);
    setShowPastChats(false);
    setPastChatQuery("");
    if (pastChatToken) setDismissed(true);
  }, [pastChatToken, running]);

  const resetPromptHistoryNavigation = () => {
    if (historyIndexRef.current === -1) return;
    historyIndexRef.current = -1;
    setHistoryIndex(-1);
  };

  const syncPromptHistoryGeneration = () => {
    const nextGeneration = cacheGeneration();
    if (historyGenerationRef.current === nextGeneration) return;
    historyGenerationRef.current = nextGeneration;
    historyEntriesRef.current = [];
    historyLoadRef.current = null;
    historyIndexRef.current = -1;
    setHistoryIndex(-1);
  };

  const ensurePromptHistoryIndex = async (index: number): Promise<boolean> => {
    if (index < historyEntriesRef.current.length) return true;
    if (historyLoadRef.current) await historyLoadRef.current;
    while (index >= historyEntriesRef.current.length) {
      let loaded = 0;
      const task = loadOlder().then((entries) => {
        loaded = entries.length;
        if (loaded > 0) {
          historyEntriesRef.current = historyEntriesRef.current.concat(entries);
        }
      });
      historyLoadRef.current = task;
      await task;
      historyLoadRef.current = null;
      if (loaded === 0) return index < historyEntriesRef.current.length;
    }
    return true;
  };

  const prefetchPromptHistoryTail = () => {
    if (historyLoadRef.current) return;
    void ensurePromptHistoryIndex(historyEntriesRef.current.length);
  };

  const focusComposerInput = () => {
    if (invocationsRef.current.length > 0) richInputRef.current?.focus();
    else taRef.current?.focus();
  };

  const requestActiveDraftFrame = (callback: () => void) => {
    const activationEpoch = draftActivationEpochRef.current;
    requestAnimationFrame(() => {
      if (draftActivationEpochRef.current !== activationEpoch) return;
      callback();
    });
  };

  const getComposerSelection = () => {
    if (invocationsRef.current.length > 0) return richInputRef.current?.getSelection() ?? richSelection;
    const ta = taRef.current;
    const start = ta?.selectionStart ?? textRef.current.length;
    const end = ta?.selectionEnd ?? start;
    return { start: Math.min(start, end), end: Math.max(start, end) };
  };

  const setComposerSelection = (start: number, end = start, afterInvocationId?: string) => {
    const nextSelection = { start, end, afterInvocationId };
    lastSelectionRef.current = { start, end };
    if (invocationsRef.current.length === 0) setPlainSelection(nextSelection);
    requestActiveDraftFrame(() => {
      if (invocationsRef.current.length > 0) {
        richInputRef.current?.setSelectionRange(start, end, afterInvocationId);
        return;
      }
      const ta = taRef.current;
      if (!ta) return;
      ta.focus();
      ta.setSelectionRange(start, end);
    });
  };

  const focusComposerFromContentBlank = (event: ReactMouseEvent<HTMLDivElement>) => {
    if (event.target !== event.currentTarget || disabled || readOnly) return;
    event.preventDefault();
    setComposerSelection(textRef.current.length);
  };

  const setTextCaretEnd = (next: string, trackEdit = true) => {
    const targetDraftKey = activeDraftKeyRef.current;
    const beforeEdit = trackEdit ? composerEditSnapshot(targetDraftKey) : null;
    textRef.current = next;
    setText(next);
    setComposerSelection(next.length);
    if (beforeEdit) {
      recordComposerEdit(
        targetDraftKey,
        beforeEdit,
        composerEditSnapshot(targetDraftKey, { start: next.length, end: next.length }),
      );
    }
  };

  const setTextForDraft = (targetDraftKey: string, next: string) => {
    if (targetDraftKey === activeDraftKeyRef.current) {
      setTextCaretEnd(next);
      return;
    }
    const draft = cloneComposerDraft(draftsBySessionRef.current[targetDraftKey] ?? emptyComposerDraft());
    draft.text = next;
    draftsBySessionRef.current[targetDraftKey] = draft;
  };

  const rememberCaret = () => {
    if (invocationsRef.current.length > 0) {
      const selection = richInputRef.current?.getSelection();
      if (selection) lastSelectionRef.current = { start: selection.start, end: selection.end };
      return;
    }
    const ta = taRef.current;
    if (!ta) return;
    const nextSelection = { start: ta.selectionStart ?? text.length, end: ta.selectionEnd ?? text.length };
    lastSelectionRef.current = nextSelection;
    setPlainSelection(nextSelection);
  };

  const insertNewlineAtCaret = () => {
    const selection = getComposerSelection();
    const targetDraftKey = activeDraftKeyRef.current;
    const beforeEdit = composerEditSnapshot(targetDraftKey, selection);
    const updated = insertComposerNewline(textRef.current, invocationsRef.current, selection);
    textRef.current = updated.text;
    invocationsRef.current = updated.invocations;
    setText(updated.text);
    setInvocations(updated.invocations);
    const caret = selection.start + 1;
    setComposerSelection(caret);
    recordComposerEdit(
      targetDraftKey,
      beforeEdit,
      composerEditSnapshot(targetDraftKey, { start: caret, end: caret }),
    );
  };

  const insertTextAtCaret = (snippet: string) => {
    const selection = getComposerSelection();
    const targetDraftKey = activeDraftKeyRef.current;
    const beforeEdit = composerEditSnapshot(targetDraftKey, selection);
    const start = selection.start;
    const end = selection.end;
    const current = textRef.current;
    const before = current.slice(0, start);
    const after = current.slice(end);
    const leading = before.length === 0 || before.endsWith("\n\n") ? "" : before.endsWith("\n") ? "\n" : "\n\n";
    const body = snippet.trimEnd();
    const trailing = after.length === 0 ? "\n" : after.startsWith("\n") ? "" : "\n\n";
    const inserted = leading + body + trailing;
    const pos = before.length + inserted.length;
    const updated = replaceInvocationTextRange(current, invocationsRef.current, start, end, inserted);
    textRef.current = updated.text;
    invocationsRef.current = updated.invocations;
    setText(updated.text);
    setInvocations(updated.invocations);
    setComposerSelection(pos);
    recordComposerEdit(
      targetDraftKey,
      beforeEdit,
      composerEditSnapshot(targetDraftKey, { start: pos, end: pos }),
    );
  };

  const replaceComposerText = (next: string) => {
    clearComposerEditHistory(activeDraftKeyRef.current);
    clearAttachments();
    setWorkspaceRefs([]);
    setSessionRefs([]);
    selectedTextRefsRef.current = [];
    setSelectedTextRefs([]);
    pastedBlocksRef.current = [];
    setPastedBlocks([]);
    setOpenPastedLabels([]);
    setTextCaretEnd(next, false);
  };

  const addWorkspaceReference = (ref: WorkspaceReference) => {
    setWorkspaceRefs((prev) => {
      const key = workspaceReferenceKey(ref);
      if (prev.some((item) => workspaceReferenceKey(item) === key)) return prev;
      const next = [...prev, ref];
      workspaceRefsRef.current = next;
      return next;
    });
    requestActiveDraftFrame(focusComposerInput);
  };

  useEffect(() => {
    if (!insertRequest || insertRequest.id === consumedInsertIdByDraftRef.current[draftKey]) return;
    consumedInsertIdByDraftRef.current[draftKey] = insertRequest.id;
    if (insertRequest.mode === "replace") {
      replaceComposerText(insertRequest.text);
      return;
    }
    if (insertRequest.mode === "prefix") {
      const prefix = `${insertRequest.text.trimEnd()} `;
      const current = textRef.current;
      setTextCaretEnd(current ? prefix + current : prefix);
      return;
    }
    const ref = parseWorkspaceReference(insertRequest.text);
    if (ref) {
      if (!attachmentInputEnabled) return;
      addWorkspaceReference(ref);
      return;
    }
    insertTextAtCaret(insertRequest.text);
  }, [draftKey, insertRequest]);

  useEffect(() => {
    if (!selectedTextRequest || selectedTextRequest.id === consumedSelectedTextIdByDraftRef.current[draftKey]) return;
    consumedSelectedTextIdByDraftRef.current[draftKey] = selectedTextRequest.id;
    const normalized = normalizeSelectedText(selectedTextRequest.text);
    if (!normalized.text) return;
    if (normalized.truncated) showToast(t("composer.selectedTextTruncated"), "warn");
    const path = selectedTextRequest.path;
    const source = selectedTextRequest.source;
    const duplicate = selectedTextRefsRef.current.some(
      (reference) => reference.text === normalized.text
        && (reference.path ?? "") === (path ?? "")
        && (reference.source ?? "") === (source ?? ""),
    );
    if (!duplicate) {
      const next = [
        ...selectedTextRefsRef.current,
        {
          id: `${path ? "code" : source === "terminal" ? "terminal" : "chat"}-selection-${selectedTextRequest.id}`,
          text: normalized.text,
          ...(path ? { path } : {}),
          ...(source ? { source } : {}),
        },
      ];
      selectedTextRefsRef.current = next;
      setSelectedTextRefs(next);
    }
    requestActiveDraftFrame(focusComposerInput);
  }, [draftKey, selectedTextRequest, showToast, t]);

  const expandPastedBlocks = (displayText: string, blocks = pastedBlocksRef.current): string => {
    let expanded = displayText;
    for (const block of blocks) {
      if (expanded.includes(block.label)) {
        expanded = expanded.split(block.label).join(renderPastedBlock(block));
      }
    }
    return expanded;
  };

  const rememberAttachment = (path: string, key: AttachmentDedupKey) => {
    attachmentDedupRef.current.add(key.hash, key.source);
    attachmentDedupKeysRef.current[path] = key;
  };

  const forgetAttachment = (path: string) => {
    const key = attachmentDedupKeysRef.current[path];
    if (key) {
      attachmentDedupRef.current.forget(key.hash, key.source);
      delete attachmentDedupKeysRef.current[path];
    }
  };

  const clearAttachments = () => {
    attachmentsRef.current = [];
    setAttachments([]);
    attachmentDedupRef.current.clear();
    attachmentDedupKeysRef.current = {};
  };

  const removeAttachment = (path: string) => {
    forgetAttachment(path);
    setAttachments(attachmentsRef.current.filter((x) => x.path !== path));
    requestActiveDraftFrame(focusComposerInput);
  };

  const attachmentSeenInDraft = (targetDraftKey: string, key: AttachmentDedupKey): boolean => {
    if (targetDraftKey === activeDraftKeyRef.current) return attachmentDedupRef.current.seen(key.hash, key.source);
    const draft = draftsBySessionRef.current[targetDraftKey];
    return draft ? draftHasAttachmentDedupKey(draft, key) : false;
  };

  const addAttachmentToDraft = (targetDraftKey: string, attachment: Attachment, key: AttachmentDedupKey): boolean => {
    if (targetDraftKey === activeDraftKeyRef.current) {
      if (attachmentDedupRef.current.seen(key.hash, key.source)) return false;
      rememberAttachment(attachment.path, key);
      const next = [...attachmentsRef.current, attachment];
      attachmentsRef.current = next;
      setAttachments(next);
      return true;
    }
    const draft = cloneComposerDraft(draftsBySessionRef.current[targetDraftKey] ?? emptyComposerDraft());
    if (draftHasAttachmentDedupKey(draft, key)) return false;
    draft.attachmentDedupKeys[attachment.path] = key;
    draft.attachments = [...draft.attachments, attachment];
    draftsBySessionRef.current[targetDraftKey] = draft;
    return true;
  };

  const addWorkspaceReferenceToDraft = (targetDraftKey: string, ref: WorkspaceReference) => {
    if (targetDraftKey === activeDraftKeyRef.current) {
      addWorkspaceReference(ref);
      return;
    }
    const draft = cloneComposerDraft(draftsBySessionRef.current[targetDraftKey] ?? emptyComposerDraft());
    const key = workspaceReferenceKey(ref);
    if (draft.workspaceRefs.some((item) => workspaceReferenceKey(item) === key)) return;
    draft.workspaceRefs = [...draft.workspaceRefs, ref];
    draftsBySessionRef.current[targetDraftKey] = draft;
  };

  const clearSubmittedDraft = (targetDraftKey: string) => {
    clearComposerEditHistory(targetDraftKey);
    if (targetDraftKey === activeDraftKeyRef.current) {
      textRef.current = "";
      setText("");
      invocationsRef.current = [];
      setInvocations([]);
      setRichSlashQuery(null);
      historyIndexRef.current = -1;
      setHistoryIndex(-1);
      clearAttachments();
      workspaceRefsRef.current = [];
      setWorkspaceRefs([]);
      sessionRefsRef.current = [];
      setSessionRefs([]);
      selectedTextRefsRef.current = [];
      setSelectedTextRefs([]);
      pastedBlocksRef.current = [];
      setPastedBlocks([]);
      openPastedLabelsRef.current = [];
      setOpenPastedLabels([]);
      savedTextRef.current = "";
      return;
    }
    const draft = cloneComposerDraft(draftsBySessionRef.current[targetDraftKey] ?? emptyComposerDraft());
    draft.text = "";
    draft.invocations = [];
    draft.attachments = [];
    draft.workspaceRefs = [];
    draft.pastedBlocks = [];
    draft.openPastedLabels = [];
    draft.sessionRefs = [];
    draft.selectedTextRefs = [];
    draft.attachmentDedupKeys = {};
    draft.historyIndex = -1;
    draft.savedText = "";
    draftsBySessionRef.current[targetDraftKey] = draft;
  };

  const clearIntentCloseTimer = useCallback(() => {
    if (intentCloseTimerRef.current === null) return;
    window.clearTimeout(intentCloseTimerRef.current);
    intentCloseTimerRef.current = null;
  }, []);

  // Hover timers only touch refs — no useCallback cross-deps (avoids TDZ on HMR).
  const clearHoverTimer = (timerRef: { current: number | null }) => {
    if (timerRef.current == null) return;
    window.clearTimeout(timerRef.current);
    timerRef.current = null;
  };

  const openIntentMenu = useCallback(() => {
    clearIntentCloseTimer();
    clearHoverTimer(intentHoverTimerRef);
    setContentMenuOpen(false);
    setDirectPastChats(false);
    setDismissed(true);
    setIntentMenuClosing(false);
    setIntentMenuOpen(true);
  }, [clearIntentCloseTimer]);

  const closeIntentMenu = useCallback((afterClose?: () => void) => {
    clearIntentCloseTimer();
    clearHoverTimer(intentHoverTimerRef);
    setIntentMenuClosing(true);
    window.requestAnimationFrame(() => setIntentMenuOpen(false));
    const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    intentCloseTimerRef.current = window.setTimeout(() => {
      intentCloseTimerRef.current = null;
      setIntentMenuClosing(false);
      afterClose?.();
    }, reduceMotion ? 0 : ANCHORED_POPOVER_CLOSE_MS);
  }, [clearIntentCloseTimer]);

  useEffect(() => () => {
    clearIntentCloseTimer();
    clearHoverTimer(intentHoverTimerRef);
  }, [clearIntentCloseTimer]);

  const onIntentHoverEnter = useCallback(() => {
    if (!creationChrome || disabled || running) return;
    clearHoverTimer(intentHoverTimerRef);
    intentHoverTimerRef.current = window.setTimeout(() => {
      intentHoverTimerRef.current = null;
      openIntentMenu();
    }, 120);
  }, [creationChrome, disabled, openIntentMenu, running]);

  const onIntentHoverLeave = useCallback(() => {
    if (!creationChrome) return;
    clearHoverTimer(intentHoverTimerRef);
    if (!intentMenuOpen && !intentMenuClosing) return;
    intentHoverTimerRef.current = window.setTimeout(() => {
      intentHoverTimerRef.current = null;
      closeIntentMenu();
    }, 140);
  }, [closeIntentMenu, creationChrome, intentMenuClosing, intentMenuOpen]);

  const onIntentPopoverEnter = useCallback(() => {
    if (!creationChrome) return;
    clearHoverTimer(intentHoverTimerRef);
  }, [creationChrome]);

  const clearMoreCloseTimer = useCallback(() => {
    if (moreCloseTimerRef.current === null) return;
    window.clearTimeout(moreCloseTimerRef.current);
    moreCloseTimerRef.current = null;
  }, []);

  const openMoreMenu = useCallback(() => {
    clearMoreCloseTimer();
    setContentMenuOpen(false);
    setDirectPastChats(false);
    setDismissed(true);
    setMoreMenuClosing(false);
    setMoreMenuOpen(true);
  }, [clearMoreCloseTimer]);

  const closeMoreMenu = useCallback((afterClose?: () => void) => {
    clearMoreCloseTimer();
    setMoreMenuClosing(true);
    window.requestAnimationFrame(() => setMoreMenuOpen(false));
    const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    moreCloseTimerRef.current = window.setTimeout(() => {
      moreCloseTimerRef.current = null;
      setMoreMenuClosing(false);
      afterClose?.();
    }, reduceMotion ? 0 : ANCHORED_POPOVER_CLOSE_MS);
  }, [clearMoreCloseTimer]);

  useEffect(() => () => clearMoreCloseTimer(), [clearMoreCloseTimer]);

  const fileDedupKey = async (file: File): Promise<AttachmentDedupKey> => ({
    hash: await sha256(file),
    source: `file:${file.name}:${file.size}:${file.lastModified}`,
  });

  const planModeOn = collaborationMode === "plan";
  const activeGoal = (goal ?? "").trim();
  const goalModeOn = collaborationMode === "goal";
  const warnImageInputFallback = useCallback((message?: string) => {
    const text = message ?? t("composer.imageInputUnsupported");
    showToast(text, "warn");
  }, [showToast, t]);

  const submit = async () => {
    if (disabled || (!running && submitDisabled) || readOnly) return;
    const submitDraftKey = activeDraftKeyRef.current;
    const submitTabId = tabId;
    if (draftIsSubmitting(submitDraftKey)) return;
    const currentText = textRef.current;
    const rawDraft = trimInvocationDraft(currentText, invocationsRef.current);
    const typedGoalDraft = goalModeOn && !activeGoal && rawDraft.invocations.length === 0
      ? typedStructuredInvocationDraft(rawDraft.text, commands)
      : null;
    const trimmedDraft = typedGoalDraft ?? rawDraft;
    const trimmedText = trimmedDraft.text;
    if (draftHasPendingPaste(submitDraftKey)) return;
    if (!attachmentInputEnabled && (attachmentsRef.current.length > 0 || workspaceRefsRef.current.length > 0)) return;
    if (!imageInputEnabled && !imageUnderstandingEnabled && hasImageAttachments(attachmentsRef.current)) {
      warnImageInputFallback();
    }
    const currentAttachments = attachmentsRef.current;
    const currentWorkspaceRefs = workspaceRefsRef.current;
    const inlineInvocationCount = trimmedDraft.invocations.filter((invocation) => invocation.command.kind === "skill").length;
    const subagentInvocationCount = trimmedDraft.invocations.filter((invocation) => invocation.command.kind === "subagent").length;
    if (goalModeOn && !activeGoal && trimmedDraft.invocations.length > 0 && !trimmedText) {
      // Goal setup still needs task text when a structured invocation is
      // present. Attachments and workspace refs remain valid task-only input.
      setComposerPrompt(t("composer.goalInputRequired"));
      requestActiveDraftFrame(focusComposerInput);
      return;
    }
    if (!trimmedText && currentAttachments.length === 0 && currentWorkspaceRefs.length === 0 && inlineInvocationCount === 0) {
      if (goalModeOn && !activeGoal) {
        setComposerPrompt(t("composer.goalInputRequired"));
        requestActiveDraftFrame(focusComposerInput);
      } else if (subagentInvocationCount > 0) {
        setComposerPrompt(t("composer.subagentTaskRequired"));
        requestActiveDraftFrame(focusComposerInput);
      }
      return;
    }
    setComposerPrompt(null);
    updateSubmittingForDraft(submitDraftKey, true);
    try {
      const orderedAttachments = sortComposerAttachments(currentAttachments);
      const refs = [
        ...currentWorkspaceRefs.map((ref) => formatWorkspaceReference(ref.path, ref.isDir)),
        ...orderedAttachments.map((a) => `@${a.path}`),
      ].join(" ");
      const displayRefs = [
        ...currentWorkspaceRefs.map((ref) => formatWorkspaceReference(ref.displayPath || ref.path, ref.isDir)),
        ...orderedAttachments.map(formatAttachmentDisplayReference),
        ...selectedTextRefsRef.current.map(formatSelectionLabel),
      ].join(" ");
      const displayText = [trimmedText, displayRefs].filter(Boolean).join(trimmedText && displayRefs ? " " : "");
      // PR-B: when past:chats refs are attached, prepend their formatted transcript
      // to submitText only (displayText stays unchanged so the user still sees their
      // original prompt in the input preview). With no refs we keep the original
      // submitText verbatim — no header, no rewording, byte-identical to pre-PR-B.
      const currentSessionRefs = sessionRefsRef.current;
      const currentSelectedTextRefs = selectedTextRefsRef.current;
      const currentPastedBlocks = [...pastedBlocksRef.current];
      const sessionContext = currentSessionRefs.length === 0 ? "" : await buildSessionContext(currentSessionRefs, t);
      const selectedTextContext = formatSelectedTextContext(currentSelectedTextRefs);
      const invocationText = serializeInvocationSubmit(trimmedText, trimmedDraft.invocations);
      const baseSubmitText = [expandPastedBlocks(invocationText, currentPastedBlocks), refs].filter(Boolean).join(" ");
      const submitBase = sessionContext ? `${sessionContext}${baseSubmitText}` : baseSubmitText;
      const submitText = [submitBase, selectedTextContext].filter(Boolean).join("\n\n");
      const structuredInput = [expandPastedBlocks(trimmedText, currentPastedBlocks), refs].filter(Boolean).join(" ");
      const structured = trimmedDraft.invocations.length > 0 ? {
        display: [invocationText, displayRefs].filter(Boolean).join(invocationText && displayRefs ? " " : ""),
        input: [sessionContext ? `${sessionContext}${structuredInput}` : structuredInput, selectedTextContext].filter(Boolean).join("\n\n"),
        invocations: invocationRequests(trimmedDraft.invocations),
      } satisfies StructuredInvocationSubmit : undefined;
      if (running) {
        // An entity-only submit has an empty displayText (entities live
        // outside the text model); fall back to the serialized slash form so
        // the queue shows the invocation instead of silently dropping it
        // while clearSubmittedDraft wipes the composer.
        const guidanceText = displayText.trim() || (structured?.display.trim() ?? "");
        const guidanceSubmitText = submitText.trim();
        if (guidanceText) {
          if (!localDurableGuidance && onSteer) {
            try {
              await onSteer(guidanceSubmitText, submitTabId);
              clearSubmittedDraft(submitDraftKey);
            } catch (error) {
              showToast(formatInboxError(error, locale), "warn");
            }
            return;
          }
          // Durable follow-up: only clear the composer after a durable receipt.
          const receiptTracker = guidanceReceiptTrackerRef.current;
          receiptTracker?.start(submitDraftKey);
          try {
            const receipt = await enqueueInboxGuidanceForActiveTurn(app, submitTabId || "", guidanceText, guidanceSubmitText, structured, turnId);
            if (receipt?.error) throw new Error(receipt.error);
            const consumedBeforeReceipt = receiptTracker?.takeConsumed(submitDraftKey, receipt.itemId) ?? false;
            if (!consumedBeforeReceipt) {
              updatePendingGuidanceForDraft(submitDraftKey, (items) => {
                const next = items.map((item) => receipt.paused ? { ...item, paused: true } : item);
                if (next.some((item) => item.id === receipt.itemId)) return next;
                return [...next, {
                  id: receipt.itemId,
                  text: guidanceText.slice(0, 120),
                  submitText: "",
                  intent: "followup",
                  state: "queued",
                  source: "desktop",
                  paused: Boolean(receipt.paused),
                  structured,
                }];
              });
            }
            clearSubmittedDraft(submitDraftKey);
          } catch (error) {
            showToast(formatInboxError(error, locale), "warn");
            // Keep draft on durable failure.
          } finally {
            receiptTracker?.finish(submitDraftKey);
          }
        }
        return;
      }
      await onSend(displayText, submitText, submitTabId, structured);
      clearSubmittedDraft(submitDraftKey);
    } catch (error) {
      showToast(formatInboxError(error, locale), "warn");
    } finally {
      updateSubmittingForDraft(submitDraftKey, false);
    }
  };

  const sendQueuedGuidance = async (
    item: PendingGuidance,
    targetDraftKey = activeDraftKeyRef.current,
    targetTabId = tabId,
  ) => {
    if (targetDraftKey !== activeDraftKeyRef.current || disabled || readOnly || guidanceSendingIdRef.current !== null) return;
    const durable = !item.id.startsWith("local-");
    if (running && item.structured) return;
    updateGuidanceSendingIdForDraft(targetDraftKey, item.id);
    try {
      if (durable && guidanceNeedsRetry(item.state)) {
        await app.RetryInboxItem(targetTabId || "", item.id);
        updatePendingGuidanceForDraft(targetDraftKey, (items) => markGuidanceQueued(items, item.id));
        setGuidanceRetryNonce((value) => value + 1);
        // Idle retries dispatch a new turn in the Controller. Busy retries are
        // requeued first, then admitted to the active turn below.
        if (!running || item.structured) return;
      }
      if (durable && !running) return await kickIdleGuidance(app.SetInboxPaused, targetTabId || "", () => setGuidanceRetryNonce((value) => value + 1));
      if (running && durable) {
        const receipt = await steerInboxItemForActiveTurn(app, targetTabId || "", item.id, turnId);
        if (receipt?.error) throw new Error(receipt.error);
        if (receipt?.disposition === "steer_accepted") {
          updatePendingGuidanceForDraft(targetDraftKey, (items) => items.filter((queued) => queued.id !== item.id));
        } else {
          // Rejected steers remain the same durable follow-up item. The
          // Controller owns its later FIFO dispatch.
          updatePendingGuidanceForDraft(targetDraftKey, (items) =>
            items.map((queued) => queued.id === item.id
              ? { ...queued, intent: "followup", state: "queued" }
              : queued),
          );
          setGuidanceRetryNonce((value) => value + 1);
        }
        return;
      }
      if (durable) return;
      // Prefer durable inbox paths: load body by id only when needed.
      let displayText = item.text.trim();
      let submitText = item.submitText.trim();
      if (!submitText || submitText === displayText) {
        try {
          const env = await app.ReadInboxItem(targetTabId || "", item.id);
          displayText = (env.displayText || env.submitText || displayText).trim();
          submitText = (env.submitText || displayText).trim();
        } catch {
          // Fall back to preview-only shelf text.
        }
      }
      if (!displayText || !submitText) return;
      const attemptedSteer = running && onSteer !== undefined;
      const selfDispatched = selfDispatchedGuidanceByDraftRef.current[targetDraftKey] ?? [];
      selfDispatched.push(submitText);
      selfDispatchedGuidanceByDraftRef.current[targetDraftKey] = selfDispatched;
      if (attemptedSteer) {
        await onSteer(submitText, targetTabId);
        updatePendingGuidanceForDraft(targetDraftKey, (items) => items.filter((queued) => queued.id !== item.id));
      } else {
        await onSend(displayText, submitText, targetTabId, item.structured);
        updatePendingGuidanceForDraft(targetDraftKey, (items) => items.filter((queued) => queued.id !== item.id));
      }
      window.setTimeout(() => {
        takeSelfDispatchedGuidance(submitText, targetDraftKey);
      }, 5000);
    } catch (error) {
      showToast(formatInboxError(error, locale), "warn");
    } finally {
      const current = targetDraftKey === activeDraftKeyRef.current
        ? guidanceSendingIdRef.current
        : draftsBySessionRef.current[targetDraftKey]?.guidanceSendingId;
      if (current === item.id) updateGuidanceSendingIdForDraft(targetDraftKey, null);
    }
  };

  const dismissQueuedGuidance = async (item: PendingGuidance) => {
    const targetDraftKey = activeDraftKeyRef.current;
    const targetTabId = tabId || "";
    try {
      if (!item.id.startsWith("local-")) {
        await app.DeleteInboxItem(targetTabId, item.id);
      }
      updatePendingGuidanceForDraft(
        targetDraftKey,
        (items) => items.filter((queued) => queued.id !== item.id),
      );
    } catch (error) {
      if (isInboxItemMissing(error)) {
        updatePendingGuidanceForDraft(
          targetDraftKey,
          (items) => items.filter((queued) => queued.id !== item.id),
        );
        return;
      }
      showToast(formatInboxError(error, locale), "warn");
    }
  };

  const editQueuedGuidance = async (item: PendingGuidance, nextText: string) => {
    const text = nextText.trim();
    if (!text || item.id.startsWith("local-")) return;
    const targetDraftKey = activeDraftKeyRef.current;
    const targetTabId = tabId || "";
    try {
      await app.UpdateInboxItem(targetTabId, item.id, text, text);
      updatePendingGuidanceForDraft(targetDraftKey, (items) =>
        items.map((queued) => queued.id === item.id ? { ...queued, text, submitText: text } : queued),
      );
    } catch (error) {
      showToast(formatInboxError(error, locale), "warn");
      throw error;
    }
  };

  const readFileAsDataURL = (file: File) =>
    new Promise<string>((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => resolve(String(reader.result));
      reader.onerror = () => reject(reader.error);
      reader.readAsDataURL(file);
    });

  const attachImageFiles = async (files: File[], sourceDraftKey: string) => {
    if (!attachmentInputEnabled) return;
    const images = files.filter((f) => f.type.startsWith("image/"));
    if (images.length === 0) return;
    for (const file of images) {
      updatePendingPasteForDraft(sourceDraftKey, 1);
      try {
        const key = await fileDedupKey(file);
        if (attachmentSeenInDraft(sourceDraftKey, key)) continue;
        const dataUrl = await readFileAsDataURL(file);
        const path = await app.SavePastedImage(dataUrl);
        const previewUrl = await app.AttachmentDataURL(path);
        addAttachmentToDraft(sourceDraftKey, { path, previewUrl, displayName: file.name }, key);
      } catch (error) {
        console.warn("[composer] failed to attach pasted image", error);
        showToast(t("composer.attachImageFailed"), "warn");
        // non-fatal: a failed image attach must not block normal text input
      } finally {
        updatePendingPasteForDraft(sourceDraftKey, -1);
      }
    }
  };

  // Non-image pastes (PDFs, docs): the clipboard hands us bytes, not a path, so
  // the kernel stores them and we reference the saved path — attached, not ignored.
  const attachOtherFiles = async (files: File[], sourceDraftKey: string) => {
    if (!attachmentInputEnabled) return;
    const others = files.filter((f) => !f.type.startsWith("image/"));
    if (others.length === 0) return;
    for (const file of others) {
      updatePendingPasteForDraft(sourceDraftKey, 1);
      try {
        const key = await fileDedupKey(file);
        if (attachmentSeenInDraft(sourceDraftKey, key)) continue;
        const dataUrl = await readFileAsDataURL(file);
        const path = await app.SavePastedFile(file.name, dataUrl);
        addAttachmentToDraft(sourceDraftKey, { path, displayName: file.name }, key);
      } catch {
        console.warn("[composer] failed to attach pasted file");
        showToast(t("composer.attachFileFailed"), "warn");
        // non-fatal: a failed attach must not block normal text input
      } finally {
        updatePendingPasteForDraft(sourceDraftKey, -1);
      }
    }
  };

  const attachFiles = (files: File[]) => {
    if (!attachmentInputEnabled) return;
    const sourceDraftKey = activeDraftKeyRef.current;
    void attachImageFiles(files, sourceDraftKey);
    void attachOtherFiles(files, sourceDraftKey);
  };

  const attachNativeClipboardImage = async (notifyOnError: boolean, sourceDraftKey: string) => {
    if (!attachmentInputEnabled) return;
    updatePendingPasteForDraft(sourceDraftKey, 1);
    try {
      const path = await app.SaveClipboardImage();
      const previewUrl = await app.AttachmentDataURL(path);
      const key = { hash: await dataURLHash(previewUrl), source: `native-clipboard:${path}` };
      if (attachmentSeenInDraft(sourceDraftKey, key)) return;
      addAttachmentToDraft(sourceDraftKey, { path, previewUrl }, key);
    } catch (error) {
      console.warn("[composer] failed to read native clipboard image", error);
      if (notifyOnError) showToast(t("composer.pasteImageFailed"), "warn");
    } finally {
      updatePendingPasteForDraft(sourceDraftKey, -1);
    }
  };

  // OS file drops arrive as absolute paths through the native bridge (the webview
  // withholds them from the HTML drop event); the kernel resolves each into a
  // workspace @reference or a stored attachment.
  const attachDroppedPaths = async (paths: string[], sourceDraftKey = activeDraftKeyRef.current) => {
    setDragOver(false);
    if (!attachmentInputEnabled) return;
    for (const path of paths) {
      updatePendingPasteForDraft(sourceDraftKey, 1);
      try {
        const key = { hash: "", source: `path:${path}` };
        if (attachmentSeenInDraft(sourceDraftKey, key)) continue;
        const item = await app.AttachDropped(path);
        if (item.kind === "workspace") {
          addWorkspaceReferenceToDraft(sourceDraftKey, { path: item.path, isDir: item.isDir, displayPath: item.displayPath });
        } else {
          addAttachmentToDraft(sourceDraftKey, { path: item.path, previewUrl: item.previewUrl, displayName: baseName(path) }, key);
        }
      } catch {
        console.warn("[composer] failed to attach dropped file");
        showToast(t("composer.attachDropFailed"), "warn");
        // non-fatal: a failed drop attach must not block normal text input
      } finally {
        updatePendingPasteForDraft(sourceDraftKey, -1);
      }
    }
  };

  useEffect(() => {
    if (!attachmentInputEnabled) return;
    return onFilesDropped((paths) => void attachDroppedPaths(paths, activeDraftKeyRef.current));
  }, [attachmentInputEnabled]);

  const onPaste = (e: ClipboardEvent<HTMLTextAreaElement | HTMLDivElement>) => {
    clearNativeClipboardPasteTimer();
    const files = clipboardFiles(e.clipboardData);
    if (files.length > 0) {
      e.preventDefault();
      if (attachmentInputEnabled) attachFiles(files);
      return;
    }

    const pasted = e.clipboardData.getData("text");
    const hasImageHint = clipboardHasImageHint(e.clipboardData);
    if (hasImageHint || pasted === "") {
      e.preventDefault();
      if (attachmentInputEnabled) void attachNativeClipboardImage(hasImageHint, activeDraftKeyRef.current);
      return;
    }

    // Always prevent the browser default paste so React's controlled-input
    // reconciliation cannot race with the native DOM update and lose the
    // pasted content (WebView2 / Windows). We insert the text manually below.
    e.preventDefault();
    const selection = getComposerSelection();
    const start = selection.start;
    const end = selection.end;
    const sourceDraftKey = activeDraftKeyRef.current;
    const beforeEdit = composerEditSnapshot(sourceDraftKey, selection);

    // Normalize CRLF from Windows clipboard so caret offsets match the
    // textarea's normalized value. The raw text (with CRLF) is preserved
    // in the PastedBlock for long pastes so block content is lossless.
    const normalizedPasted = pasted.replace(/\r\n/g, "\n");
    let caret: number;

    if (shouldFoldPaste(pasted)) {
      // Long paste: fold into a collapsible block so the composer stays compact.
      const id = nextPasteId.current++;
      const lines = lineCount(pasted);
      const label = t("composer.pastedLabel", { id, lines });
      const block: PastedBlock = { label, text: pasted }; // keep raw text (CRLF preserved)
      const next = replaceInvocationTextRange(
        textRef.current,
        invocationsRef.current,
        start,
        end,
        label,
        selection.afterInvocationId,
      );
      pastedBlocksRef.current = [...pastedBlocksRef.current, block];
      setPastedBlocks((prev) => [...prev, block]);
      textRef.current = next.text;
      invocationsRef.current = next.invocations;
      setText(next.text);
      setInvocations(next.invocations);
      caret = start + label.length;
      setComposerSelection(caret);
    } else {
      // The paste event is intentionally prevented above, so the browser
      // cannot add this edit to its native undo history. Record the complete
      // programmatic edit below while leaving ordinary typing in the native
      // history.
      resetPromptHistoryNavigation();
      const next = replaceInvocationTextRange(
        textRef.current,
        invocationsRef.current,
        start,
        end,
        normalizedPasted,
        selection.afterInvocationId,
      );
      textRef.current = next.text;
      invocationsRef.current = next.invocations;
      setText(next.text);
      setInvocations(next.invocations);
      caret = start + normalizedPasted.length;
      setComposerSelection(caret);
    }
    recordComposerEdit(
      sourceDraftKey,
      beforeEdit,
      composerEditSnapshot(sourceDraftKey, { start: caret, end: caret }),
    );
  };

  const getInputSelection = () => {
    const selection = getComposerSelection();
    const start = selection.start;
    const end = selection.end;
    const from = Math.min(start, end);
    const to = Math.max(start, end);
    return {
      from,
      to,
      selected: textRef.current.slice(from, to),
      afterInvocationId: start === end ? selection.afterInvocationId : undefined,
    };
  };

  const focusInputRange = (start: number, end = start, afterInvocationId?: string) => {
    setComposerSelection(start, end, afterInvocationId);
  };

  const replaceInputRange = (
    value: string,
    start: number,
    end: number,
    targetDraftKey = activeDraftKeyRef.current,
    afterInvocationId?: string,
  ) => {
    if (targetDraftKey === activeDraftKeyRef.current) {
      const current = textRef.current;
      const next = replaceInvocationTextRange(
        current,
        invocationsRef.current,
        start,
        end,
        value,
        afterInvocationId,
      );
      textRef.current = next.text;
      invocationsRef.current = next.invocations;
      setText(next.text);
      setInvocations(next.invocations);
      focusInputRange(start + value.length);
      return;
    }
    const draft = cloneComposerDraft(draftsBySessionRef.current[targetDraftKey] ?? emptyComposerDraft());
    const next = replaceInvocationTextRange(
      draft.text,
      draft.invocations,
      start,
      end,
      value,
      afterInvocationId,
    );
    draft.text = next.text;
    draft.invocations = next.invocations;
    draftsBySessionRef.current[targetDraftKey] = draft;
  };

  const insertPastedText = (
    pasted: string,
    start: number,
    end: number,
    targetDraftKey = activeDraftKeyRef.current,
    afterInvocationId?: string,
  ) => {
    const normalizedPasted = pasted.replace(/\r\n/g, "\n");
    const beforeEdit = composerEditSnapshot(targetDraftKey, { start, end, afterInvocationId });
    let caret: number;
    if (targetDraftKey !== activeDraftKeyRef.current) {
      const draft = cloneComposerDraft(draftsBySessionRef.current[targetDraftKey] ?? emptyComposerDraft());
      let inserted: string;
      if (shouldFoldPaste(pasted)) {
        const id = draft.nextPasteId++;
        const lines = lineCount(pasted);
        const label = t("composer.pastedLabel", { id, lines });
        draft.pastedBlocks = [...draft.pastedBlocks, { label, text: pasted }];
        inserted = label;
        caret = start + label.length;
      } else {
        draft.historyIndex = -1;
        inserted = normalizedPasted;
        caret = start + normalizedPasted.length;
      }
      const next = replaceInvocationTextRange(
        draft.text,
        draft.invocations,
        start,
        end,
        inserted,
        afterInvocationId,
      );
      draft.text = next.text;
      draft.invocations = next.invocations;
      draftsBySessionRef.current[targetDraftKey] = draft;
      recordComposerEdit(targetDraftKey, beforeEdit, composerEditSnapshot(targetDraftKey, { start: caret, end: caret }));
      return;
    }

    if (shouldFoldPaste(pasted)) {
      const id = nextPasteId.current++;
      const lines = lineCount(pasted);
      const label = t("composer.pastedLabel", { id, lines });
      const block: PastedBlock = { label, text: pasted };
      const next = replaceInvocationTextRange(
        textRef.current,
        invocationsRef.current,
        start,
        end,
        label,
        afterInvocationId,
      );
      pastedBlocksRef.current = [...pastedBlocksRef.current, block];
      setPastedBlocks((prev) => [...prev, block]);
      textRef.current = next.text;
      invocationsRef.current = next.invocations;
      setText(next.text);
      setInvocations(next.invocations);
      caret = start + label.length;
      focusInputRange(caret);
    } else {
      resetPromptHistoryNavigation();
      const next = replaceInvocationTextRange(
        textRef.current,
        invocationsRef.current,
        start,
        end,
        normalizedPasted,
        afterInvocationId,
      );
      textRef.current = next.text;
      invocationsRef.current = next.invocations;
      setText(next.text);
      setInvocations(next.invocations);
      caret = start + normalizedPasted.length;
      focusInputRange(caret);
    }
    recordComposerEdit(targetDraftKey, beforeEdit, composerEditSnapshot(targetDraftKey, { start: caret, end: caret }));
  };

  const copyComposerSelection = async (cut = false) => {
    const selection = getInputSelection();
    const sourceDraftKey = activeDraftKeyRef.current;
    setInputMenuPoint(null);
    if (!selection.selected) {
      focusInputRange(selection.from, selection.to, selection.afterInvocationId);
      return;
    }
    try {
      await navigator.clipboard.writeText(selection.selected);
    } catch {
      // Fall back to Wails desktop runtime, then execCommand
      try {
        if (typeof window !== "undefined" && (await window.runtime?.ClipboardSetText?.(selection.selected))) {
          /* ok */
        } else if (!fallbackCopyText(selection.selected)) {
          // Every clipboard path failed. Cutting now would delete text that
          // never reached the clipboard, so keep the draft intact.
          if (sourceDraftKey === activeDraftKeyRef.current) {
            focusInputRange(selection.from, selection.to, selection.afterInvocationId);
          }
          return;
        }
      } catch {
        if (sourceDraftKey === activeDraftKeyRef.current) {
          focusInputRange(selection.from, selection.to, selection.afterInvocationId);
        }
        return;
      }
    }
    if (cut) {
      const beforeEdit = composerEditSnapshot(sourceDraftKey, { start: selection.from, end: selection.to });
      if (sourceDraftKey === activeDraftKeyRef.current) resetPromptHistoryNavigation();
      replaceInputRange("", selection.from, selection.to, sourceDraftKey);
      recordComposerEdit(
        sourceDraftKey,
        beforeEdit,
        composerEditSnapshot(sourceDraftKey, { start: selection.from, end: selection.from }),
      );
    } else if (sourceDraftKey === activeDraftKeyRef.current) {
      focusInputRange(selection.from, selection.to, selection.afterInvocationId);
    }
  };

  const pasteIntoComposer = async () => {
    const selection = getInputSelection();
    const sourceDraftKey = activeDraftKeyRef.current;
    setInputMenuPoint(null);

    // Try reading clipboard items for image detection (no event in menu path)
    try {
      const items = await navigator.clipboard.read();
      if (attachmentInputEnabled && items.some((item) => item.types.some((t) => t.startsWith("image/")))) {
        void attachNativeClipboardImage(true, sourceDraftKey);
        return;
      }
    } catch {
      /* clipboard.read() not supported or permission denied; fall through */
    }

    if (!navigator.clipboard?.readText) {
      if (sourceDraftKey === activeDraftKeyRef.current) {
        focusInputRange(selection.from, selection.to, selection.afterInvocationId);
      }
      return;
    }
    try {
      const pasted = await navigator.clipboard.readText();
      if (pasted === "") {
        // Match the keyboard paste handler: an empty text read means "nothing
        // to insert" (empty clipboard, files, or unsupported types) — never
        // replace the current selection with nothing. An image may still be
        // attachable through the native clipboard path.
        if (sourceDraftKey === activeDraftKeyRef.current) {
          focusInputRange(selection.from, selection.to, selection.afterInvocationId);
        }
        if (attachmentInputEnabled) void attachNativeClipboardImage(false, sourceDraftKey);
        return;
      }
      insertPastedText(
        pasted,
        selection.from,
        selection.to,
        sourceDraftKey,
        selection.afterInvocationId,
      );
    } catch {
      if (sourceDraftKey === activeDraftKeyRef.current) {
        focusInputRange(selection.from, selection.to, selection.afterInvocationId);
      }
    }
  };

  const selectAllComposerText = () => {
    setInputMenuPoint(null);
    focusInputRange(0, text.length);
  };

  const openInputMenu = (event: ReactMouseEvent<HTMLElement>) => {
    event.preventDefault();
    event.stopPropagation();
    rememberCaret();
    setInputMenuPoint(contextMenuPointFromEvent(event));
  };

  const hasWorkspaceReferenceDrag = (dataTransfer: DataTransfer): boolean =>
    Array.from(dataTransfer.types).includes(WORKSPACE_REF_DRAG_TYPE);

  const hasFileDrag = (dataTransfer: DataTransfer): boolean =>
    Array.from(dataTransfer.items).some((it) => it.kind === "file") || dataTransfer.files.length > 0;

  const fileDragItems = (dataTransfer: DataTransfer): DataTransferItem[] =>
    Array.from(dataTransfer.items).filter((item) => item.kind === "file");

  const getWebkitFileEntry = (item: DataTransferItem): WebkitFileEntry | null => {
    const getAsEntry = (item as DataTransferItem & { webkitGetAsEntry?: () => WebkitFileEntry | null }).webkitGetAsEntry;
    return typeof getAsEntry === "function" ? getAsEntry.call(item) : null;
  };

  const hasPathlessFileDrop = (dataTransfer: DataTransfer): boolean => {
    const items = fileDragItems(dataTransfer);
    if (items.length === 0) return dataTransfer.files.length > 0;
    return items.some((item) => getWebkitFileEntry(item) === null);
  };

  const clearWailsDropTarget = () => {
    document.querySelectorAll(".wails-drop-target-active").forEach((el) => el.classList.remove("wails-drop-target-active"));
  };

  const stopNativeFileDrop = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    e.stopPropagation();
    e.nativeEvent.stopImmediatePropagation();
    clearWailsDropTarget();
  };

  const onFileDropCapture = (e: DragEvent<HTMLDivElement>) => {
    if (hasWorkspaceReferenceDrag(e.dataTransfer) || !hasFileDrag(e.dataTransfer)) return;
    e.preventDefault();
    if (!attachmentInputEnabled) {
      stopNativeFileDrop(e);
      setDragOver(false);
      return;
    }
    if (!hasPathlessFileDrop(e.dataTransfer)) return;
    const files = Array.from(e.dataTransfer.files);
    if (files.length === 0) return;
    stopNativeFileDrop(e);
    setDragOver(false);
    attachFiles(files);
  };

  const onDrop = (e: DragEvent<HTMLDivElement>) => {
    const droppedWorkspaceRef = readWorkspaceReferenceDrag(e.dataTransfer);
    if (droppedWorkspaceRef) {
      e.preventDefault();
      setDragOver(false);
      if (!attachmentInputEnabled) return;
      addWorkspaceReference(droppedWorkspaceRef);
      return;
    }

    // OS file drops deliver no usable bytes/paths here; the native bridge
    // (onFilesDropped -> AttachDropped) handles them. Prevent webview navigation.
    if (hasFileDrag(e.dataTransfer)) {
      e.preventDefault();
      setDragOver(false);
    }
  };

  const onDragOver = (e: DragEvent<HTMLDivElement>) => {
    if (!hasWorkspaceReferenceDrag(e.dataTransfer) && !hasFileDrag(e.dataTransfer)) return;
    e.preventDefault(); // required for the drop event to fire
    e.dataTransfer.dropEffect = attachmentInputEnabled ? "copy" : "none";
    setDragOver(attachmentInputEnabled);
  };

  const onDragLeave = () => setDragOver(false);
  // handleCancel stops the in-flight turn; if it was cancelled before the server
  // replied, the just-sent text is handed back so we drop it back into the input.
  const handleCancel = async () => {
    const targetDraftKey = activeDraftKeyRef.current;
    if (cancelSettlingDraftsRef.current.has(targetDraftKey)) return;
    cancelSettlingDraftsRef.current.add(targetDraftKey);
    setCancelSettlingRevision((value) => value + 1);
    const ownedGuidance = pendingGuidanceRef.current.filter((item) => item.id.startsWith("local-") || item.source === "desktop");
    const durableItemIDs = ownedGuidance
      .map((item) => item.id)
      .filter((id) => !id.startsWith("local-"));
    if (goalModeOn && activeGoal) onClearGoal();
    try {
      const outcome = (await onCancel(durableItemIDs)) ?? { discardedItemIds: [] };
      const discarded = new Set(outcome.discardedItemIds);
      const restorable = ownedGuidance.filter((item) => item.id.startsWith("local-") || discarded.has(item.id));
      const queued = restorable
        .map((item) => item.structured?.display ?? item.text)
        .filter((part) => part.trim() !== "");
      const restoredIDs = new Set(restorable.map((item) => item.id));
      if (restoredIDs.size > 0) {
        updatePendingGuidanceForDraft(targetDraftKey, (items) => items.filter((item) => !restoredIDs.has(item.id)));
      }
      const draftText = targetDraftKey === activeDraftKeyRef.current
        ? textRef.current
        : (draftsBySessionRef.current[targetDraftKey]?.text ?? "");
      const currentDraft = outcome.restoredText?.trim() === draftText.trim() ? "" : draftText;
      const nextText = [outcome.restoredText, currentDraft, ...queued]
        .filter((part): part is string => Boolean(part?.trim()))
        .join("\n");
      if (nextText) setTextForDraft(targetDraftKey, nextText);
      if (targetDraftKey === activeDraftKeyRef.current && restorable.length > 0) setGuidanceExpanded(false);
    } finally {
      cancelSettlingDraftsRef.current.delete(targetDraftKey);
      setCancelSettlingRevision((value) => value + 1);
    }
  };

  const pickCommand = (c: CommandInfo) => {
    const query = activeSlashQuery;
    if (!query || slashCommandDisabled(c)) return;
    if (!commandUsesStructuredInvocation(c)) {
      if (invocationsRef.current.length > 0 && richSlashQuery) {
        richInputRef.current?.replaceRange(`/${c.name} `, richSlashQuery.from, richSlashQuery.to);
      } else {
        const targetDraftKey = activeDraftKeyRef.current;
        const beforeEdit = composerEditSnapshot(targetDraftKey, { start: query.from, end: query.to });
        const next = replaceInvocationTextRange(
          textRef.current,
          invocationsRef.current,
          query.from,
          query.to,
          `/${c.name} `,
        );
        const caret = query.from + c.name.length + 2;
        textRef.current = next.text;
        setText(next.text);
        setComposerSelection(caret);
        recordComposerEdit(
          targetDraftKey,
          beforeEdit,
          composerEditSnapshot(targetDraftKey, { start: caret, end: caret }),
        );
      }
      return;
    }
    if (invocationsRef.current.length > 0 && richSlashQuery) {
      richInputRef.current?.insertInvocation(c, richSlashQuery);
      setRichSlashQuery(null);
      return;
    }
    const targetDraftKey = activeDraftKeyRef.current;
    const beforeEdit = composerEditSnapshot(targetDraftKey, { start: query.from, end: query.to });
    const invocation: ComposerInvocation = {
      id: `composer-invocation-${nextInvocationId.current++}`,
      offset: query.from,
      command: c,
    };
    const next = replaceInvocationTextRange(
      textRef.current,
      invocationsRef.current,
      query.from,
      query.to,
      "",
    );
    textRef.current = next.text;
    invocationsRef.current = [invocation];
    setText(next.text);
    setInvocations([invocation]);
    setRichSlashQuery(null);
    recordComposerEdit(
      targetDraftKey,
      beforeEdit,
      composerEditSnapshot(targetDraftKey, {
        start: query.from,
        end: query.from,
        afterInvocationId: invocation.id,
      }),
    );
    requestActiveDraftFrame(() => richInputRef.current?.setSelectionRange(
      query.from,
      query.from,
      invocation.id,
    ));
  };

  const activePastedBlocks = pastedBlocks.filter((block) => text.includes(block.label));
  const shellModeActive = text.trimStart().startsWith("!");

  const removeWorkspaceReference = (target: WorkspaceReference) => {
    const key = workspaceReferenceKey(target);
    setWorkspaceRefs((prev) => prev.filter((ref) => workspaceReferenceKey(ref) !== key));
    requestActiveDraftFrame(focusComposerInput);
  };

  const togglePastedPreview = (label: string) => {
    setOpenPastedLabels((prev) => {
      const next = prev.includes(label) ? prev.filter((x) => x !== label) : [...prev, label];
      openPastedLabelsRef.current = next;
      return next;
    });
  };

  const replacePastedBlockLabel = (block: PastedBlock, replacement: string): number | null => {
    const current = textRef.current;
    const start = current.indexOf(block.label);
    if (start < 0) return null;
    const next = replaceInvocationTextRange(
      current,
      invocationsRef.current,
      start,
      start + block.label.length,
      replacement,
    );
    textRef.current = next.text;
    invocationsRef.current = next.invocations;
    setText(next.text);
    setInvocations(next.invocations);
    setComposerSelection(next.text.length);
    return next.text.length;
  };

  const removePastedBlock = (block: PastedBlock) => {
    const targetDraftKey = activeDraftKeyRef.current;
    const beforeEdit = composerEditSnapshot(targetDraftKey);
    const nextBlocks = pastedBlocksRef.current.filter((x) => x.label !== block.label);
    const nextOpenLabels = openPastedLabelsRef.current.filter((x) => x !== block.label);
    pastedBlocksRef.current = nextBlocks;
    openPastedLabelsRef.current = nextOpenLabels;
    setPastedBlocks(nextBlocks);
    setOpenPastedLabels(nextOpenLabels);
    const caret = replacePastedBlockLabel(block, "");
    if (caret !== null) {
      recordComposerEdit(
        targetDraftKey,
        beforeEdit,
        composerEditSnapshot(targetDraftKey, { start: caret, end: caret }),
      );
    }
  };

  const expandPastedBlock = (block: PastedBlock) => {
    const targetDraftKey = activeDraftKeyRef.current;
    const beforeEdit = composerEditSnapshot(targetDraftKey);
    const nextBlocks = pastedBlocksRef.current.filter((x) => x.label !== block.label);
    const nextOpenLabels = openPastedLabelsRef.current.filter((x) => x !== block.label);
    pastedBlocksRef.current = nextBlocks;
    openPastedLabelsRef.current = nextOpenLabels;
    setPastedBlocks(nextBlocks);
    setOpenPastedLabels(nextOpenLabels);
    const caret = replacePastedBlockLabel(block, block.text);
    if (caret !== null) {
      recordComposerEdit(
        targetDraftKey,
        beforeEdit,
        composerEditSnapshot(targetDraftKey, { start: caret, end: caret }),
      );
    }
  };

  useEffect(() => {
    const onResize = () => setComposerHeight((height) => (height === null ? null : clampComposerHeight(height)));
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);

  const measureTextareaAutoHeight = useCallback(() => {
    // Creation empty hero starts single-line but must grow so multi-line drafts
    // stay readable before send (review: fixed 20px + overflow:hidden clipped).
    if (heroMode) {
      const measureNode = measureTaRef.current;
      if (!measureNode) {
        setTextareaAutoHeight(20);
        setTextareaAutoOverflow(false);
        return;
      }
      const scrollHeight = measureNode.scrollHeight || 20;
      const maxHeight = composerHeroInputMaxHeight();
      const nextHeight = Math.min(Math.max(scrollHeight, 20), maxHeight);
      const nextOverflow = scrollHeight > maxHeight + 1;
      setTextareaAutoHeight((current) => (current === nextHeight ? current : nextHeight));
      setTextareaAutoOverflow((current) => (current === nextOverflow ? current : nextOverflow));
      return;
    }
    const richHeight = invocationsRef.current.length > 0 ? richInputRef.current?.scrollHeight() : 0;
    const scrollHeight = richHeight || measureTaRef.current?.scrollHeight || 0;
    if (!scrollHeight) return;
    const sizing = resolveComposerContentSizing({
      contentHeight: scrollHeight,
      manualLogicalHeight: composerHeight,
      maxLogicalHeight: composerMaxHeight(),
      reservedHeight: COMPOSER_AUTO_RESERVED_HEIGHT,
    });
    setTextareaAutoHeight((current) => (current === sizing.inputHeight ? current : sizing.inputHeight));
    setTextareaAutoOverflow((current) => (current === sizing.overflow ? current : sizing.overflow));
  }, [composerHeight, heroMode, invocations.length]);

  useLayoutEffect(() => {
    measureTextareaAutoHeight();
  }, [text, measureTextareaAutoHeight]);

  useEffect(() => {
    let frame = 0;
    const update = () => {
      if (frame) window.cancelAnimationFrame(frame);
      frame = window.requestAnimationFrame(() => {
        frame = 0;
        measureTextareaAutoHeight();
      });
    };
    window.addEventListener("resize", update);
    const observer = new MutationObserver(update);
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-text-size", "data-font-family", "data-mono-font-family", "style"],
    });
    return () => {
      if (frame) window.cancelAnimationFrame(frame);
      window.removeEventListener("resize", update);
      observer.disconnect();
    };
  }, [composerHeight, measureTextareaAutoHeight]);

  const saveComposerHeight = (height: number) => {
    saveLayoutSize("composerHeight", height, clampComposerHeight);
  };

  const resetComposerHeight = () => {
    setComposerHeight(null);
    clearLayoutSize("composerHeight");
  };

  const onComposerResizeStart = (e: ReactPointerEvent<HTMLButtonElement>) => {
    if (e.button !== 0) return;
    const card = composerCardRef.current;
    if (!card) return;

    e.preventDefault();
    const startY = e.clientY;
    const startHeight = Math.max(composerHeight ?? COMPOSER_MIN_HEIGHT, composerLogicalHeight(card));
    let nextHeight = clampComposerHeight(startHeight);
    let moved = false;
    card.style.setProperty("--composer-height", `${nextHeight}px`);
    e.currentTarget.setAttribute("aria-valuenow", String(nextHeight));
    const liveResize = createRafResizeUpdater({
      target: card,
      separator: e.currentTarget,
      cssVar: "--composer-height",
    });
    setComposerResizing(true);
    document.body.classList.add("composer-resizing");

    const onMove = (event: PointerEvent) => {
      moved = true;
      nextHeight = clampComposerHeight(startHeight + startY - event.clientY);
      liveResize.schedule(nextHeight);
    };
    const onUp = () => {
      liveResize.flush();
      setComposerResizing(false);
      document.body.classList.remove("composer-resizing");
      if (moved) {
        setComposerHeight(nextHeight);
        saveComposerHeight(nextHeight);
      }
      document.removeEventListener("pointermove", onMove);
      document.removeEventListener("pointerup", onUp);
      document.removeEventListener("pointercancel", onUp);
    };

    document.addEventListener("pointermove", onMove);
    document.addEventListener("pointerup", onUp);
    document.addEventListener("pointercancel", onUp);
  };

  const onComposerResizeKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    const card = composerCardRef.current;
    const current = Math.max(
      composerHeight ?? COMPOSER_MIN_HEIGHT,
      card ? composerLogicalHeight(card) : COMPOSER_MIN_HEIGHT,
    );
    const step = e.shiftKey ? 32 : 16;
    let next: number | null = null;
    if (e.key === "ArrowUp" || e.key === "PageUp") next = current + step;
    else if (e.key === "ArrowDown" || e.key === "PageDown") next = current - step;
    else if (e.key === "Home") next = COMPOSER_MIN_HEIGHT;
    else if (e.key === "End") next = composerMaxHeight();
    if (next === null) return;
    e.preventDefault();
    const height = clampComposerHeight(next);
    setComposerHeight(height);
    saveComposerHeight(height);
  };

  const pickEntry = (e: DirEntry) => {
    const picked = composerPickFileEntry(text, atRaw, atDir, e);
    if (picked.workspaceRef) {
      setTextCaretEnd(picked.text);
      addWorkspaceReference(picked.workspaceRef);
      return;
    }
    // A directory keeps the menu open (trailing "/"); a file completes it (space).
    setTextCaretEnd(picked.text);
  };

  // --- past:chats session reference ---
  const openPastChats = useCallback(async (initialQuery = "") => {
    const snapshotCwd = cwdRef.current;
    const sourceDraftKey = activeDraftKeyRef.current;
    setShowPastChats(true);
    setActive(0);
    setPastChatQuery(initialQuery);
    setLoadingPastChats(true);
    try {
      const sessions = await app.ListSessions();
      // Discard stale response if workspace changed while the request was in-flight.
      if (cwdRef.current !== snapshotCwd || activeDraftKeyRef.current !== sourceDraftKey) return;
      const sorted = asArray(sessions)
        .filter((s) => !s.current)
        .sort((a, b) => {
          const at = a.lastActivityAt || a.modTime || a.createdAt || 0;
          const bt = b.lastActivityAt || b.modTime || b.createdAt || 0;
          return bt - at;
        })
        .slice(0, 50);
      setPastChats(sorted);
    } catch {
      if (cwdRef.current !== snapshotCwd || activeDraftKeyRef.current !== sourceDraftKey) return;
      setPastChats([]);
    } finally {
      if (cwdRef.current === snapshotCwd && activeDraftKeyRef.current === sourceDraftKey) setLoadingPastChats(false);
    }
  }, []);

  useEffect(() => {
    if (!pastChatToken || directPastChats || dismissed || running || disabled || readOnly) return;
    setDirectPastChats(true);
    void openPastChats(pastChatToken.query);
  }, [directPastChats, disabled, dismissed, openPastChats, pastChatToken, readOnly, running]);

  const clearDirectPastChatToken = () => {
    const current = textRef.current;
    const token = activePastChatToken(current);
    if (!token) return current.length;
    const next = replaceInvocationTextRange(current, invocationsRef.current, token.from, current.length, "");
    textRef.current = next.text;
    invocationsRef.current = next.invocations;
    setText(next.text);
    setInvocations(next.invocations);
    return token.from;
  };

  const dismissDirectPastChats = () => {
    // Keep the literal token text — "#6310" may be an issue number or a
    // heading, not a session query. Dismissing only closes the panel;
    // `dismissed` suppresses reopening until the query changes, the same
    // contract as the slash and @ menus. Selecting a session (pickSession)
    // is the only path that consumes the token.
    setDismissed(true);
    setDirectPastChats(false);
    setShowPastChats(false);
    setPastChatQuery("");
    setActive(0);
    requestActiveDraftFrame(focusComposerInput);
  };

  // The typed panel follows the live token: typing in the composer extends
  // the query, and deleting the token (or ending it with whitespace) closes
  // the panel instead of leaving it open on a stale query.
  useEffect(() => {
    if (!directPastChats) return;
    if (pastChatTokenQuery === null) {
      setDirectPastChats(false);
      setShowPastChats(false);
      setPastChatQuery("");
      setActive(0);
      return;
    }
    setPastChatQuery(pastChatTokenQuery);
  }, [directPastChats, pastChatTokenQuery]);

  const insertContentTrigger = (trigger: "@" | "#" | "/") => {
    const selection = getInputSelection();
    const targetDraftKey = activeDraftKeyRef.current;
    const beforeEdit = composerEditSnapshot(targetDraftKey, {
      start: selection.from,
      end: selection.to,
      afterInvocationId: selection.afterInvocationId,
    });
    const current = textRef.current;
    const needsSpace = selection.from > 0 && !/\s/.test(current.charAt(selection.from - 1));
    const value = `${needsSpace ? " " : ""}${trigger}`;
    setContentMenuOpen(false);
    setDirectPastChats(false);
    setShowPastChats(false);
    setDismissed(false);
    replaceInputRange(
      value,
      selection.from,
      selection.to,
      targetDraftKey,
      selection.afterInvocationId,
    );
    const caret = selection.from + value.length;
    recordComposerEdit(
      targetDraftKey,
      beforeEdit,
      composerEditSnapshot(targetDraftKey, { start: caret, end: caret }),
    );
    if (trigger === "#") {
      setDirectPastChats(true);
      void openPastChats();
    }
  };

  const openContentMenu = () => {
    if (intentMenuOpen || intentMenuClosing) closeIntentMenu();
    if (moreMenuOpen || moreMenuClosing) closeMoreMenu();
    setDirectPastChats(false);
    setShowPastChats(false);
    setDismissed(true);
    setContentMenuOpen(true);
  };

  const chooseAttachmentFiles = () => {
    setContentMenuOpen(false);
    if (!attachmentInputEnabled) return;
    fileInputRef.current?.click();
  };

  // PR-C1: client-side filter for the past:chats list. Matches against the
  // human-visible fields (title, topic, preview, path, workspace) so users
  // can narrow long session lists without a backend round-trip. Lowercased
  // substring match keeps the behaviour predictable across locales.
  const filteredPastChats = useMemo(() => {
    const q = pastChatQuery.trim().toLowerCase();
    if (!q) return pastChats;
    return pastChats.filter((session) =>
      [
        session.title,
        session.topicTitle,
        session.preview,
        session.path,
        session.workspaceRoot,
      ]
        .map((value) => String(value ?? "").toLowerCase())
        .some((value) => value.includes(q)),
    );
  }, [pastChats, pastChatQuery]);

  // Final menu item count: when the past:chats list is open, count the
  // filtered sessions instead of file entries + the "past:chats" row.
  const count = (menuMode === "at" && showPastChats) || menuMode === "pastChats"
    ? filteredPastChats.length
    : countBase;

  // Clamp active index when the menu item count changes (e.g. switching
  // between file list and past:chats list, or filtering sessions).
  useEffect(() => {
    if (menuMode === "slash") {
      if (!slashSelectableIndices.includes(active)) {
        setActive(slashSelectableIndices[0] ?? 0);
      }
      return;
    }
    const maxIdx = Math.max(0, count - 1);
    setActive((prev) => (prev > maxIdx ? 0 : prev));
  }, [active, count, menuMode, slashSelectableIndices]);

  const removeAtToken = (value: string) => {
    return value.replace(/[\r\n]+$/u, "").replace(activeRefTokenRe, "").trimEnd();
  };

  const pickSession = (session: SessionMeta) => {
    setSessionRefs((prev) => {
      if (prev.some((x) => x.path === session.path)) {
        return prev;
      }
      return [
        ...prev,
        {
          path: session.path,
          title: session.title || session.topicTitle || session.preview || "Untitled",
          preview: session.preview,
          turns: session.turns,
          turnsState: session.turnsState,
          createdAt: session.createdAt,
          lastActivityAt: session.lastActivityAt,
        },
      ];
    });
    const caret = directPastChats ? clearDirectPastChatToken() : null;
    if (!directPastChats) setText((prev) => removeAtToken(prev));
    setDirectPastChats(false);
    setPastChatQuery("");
    setShowPastChats(false);
    setActive(0);
    setComposerSelection(caret ?? textRef.current.length);
  };

  const removeSessionRef = (path: string) => {
    setSessionRefs((prev) => prev.filter((ref) => ref.path !== path));
  };

  // pickArg replaces just the current token with the suggestion. A "descend" item
  // (e.g. "/skill show ") ends with a space, so the effect re-fetches the next
  // level; a terminal item leaves the menu (next fetch returns nothing).
  const pickArg = (it: SlashArgItem) => {
    if (!argRes) return;
    setTextCaretEnd(slashText.slice(0, argRes.from) + it.insert);
  };

  const pickActive = () => {
    if (menuMode === "slash") {
      const item = slashMatches[active];
      if (item && !slashCommandDisabled(item)) pickCommand(item);
      return;
    }
    if (menuMode === "slasharg" && argRes) {
      const item = argRes.items[active];
      if (item) pickArg(item);
      return;
    }
    if (menuMode === "at" || menuMode === "pastChats") {
      if (showPastChats) {
        const session = filteredPastChats[active];
        if (session) pickSession(session);
        return;
      }
      if (menuMode === "pastChats") return;
      const item = atMenuItems[active];
      if (!item) return;
      if (item.kind === "pastChats") {
        void openPastChats();
        return;
      }
      pickEntry(item.entry);
    }
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement | HTMLDivElement>) => {
    const composing = isImeKeyEvent(e.nativeEvent, composingRef.current, lastCompositionEndAt.current);
    const native = e.nativeEvent as globalThis.KeyboardEvent & {
      keyCode?: number;
      which?: number;
      code?: string;
    };
    const fnKey = isFnKeyEvent(native);
    const historyDirection = promptHistoryDirectionFromEvent({
      key: e.key,
      code: native.code,
      keyCode: native.keyCode,
      which: native.which,
    });

    if (e.key === "Enter" && composing) return;
    if (fnKey) return;

    if (attachmentInputEnabled && isPasteShortcut(e) && !composing) {
      clearNativeClipboardPasteTimer();
      const sourceDraftKey = activeDraftKeyRef.current;
      nativeClipboardPasteTimerRef.current = window.setTimeout(() => {
        nativeClipboardPasteTimerRef.current = null;
        void attachNativeClipboardImage(false, sourceDraftKey);
      }, 160);
    }

    // Shift+Tab toggles plan mode only. Tool access is deliberately changed via
    // the access menu so keyboard cycling never crosses a permission boundary.
    if (e.key === "Tab" && e.shiftKey && !composing) {
      e.preventDefault();
      onCycleMode();
      return;
    }

    if (
      !composing
      && !isReservedComposerHistoryShortcut(e.nativeEvent, shortcutPlatform)
      && matchesShortcut(e.nativeEvent, "toolApproval.yolo", shortcutPlatform)
    ) {
      e.preventDefault();
      onToggleYoloApprovalMode();
      return;
    }

    syncPromptHistoryGeneration();

    const inputSelection = getComposerSelection();
    const inputValue = textRef.current;

    const canUseCurrentPromptHistory = () => canUsePromptHistory({
      direction: historyDirection,
      menuOpen: Boolean(menuMode),
      composing,
      altKey: e.altKey,
      ctrlKey: e.ctrlKey,
      metaKey: e.metaKey,
      shiftKey: e.shiftKey,
      fnKey,
      value: inputValue,
      selectionStart: inputSelection.start,
      selectionEnd: inputSelection.end,
      historyIndex: historyIndexRef.current,
    }) && invocationsRef.current.length === 0;

    // Prompt history navigation: plain ↑/↓ only. Fn/Page/Home/End are left to
    // the native textarea/OS so macOS dictation and text navigation keep working.

    // When navigating history, any other key (letter, Backspace, etc.) resets
    // back to the saved draft when another key is used.
    if (historyIndexRef.current !== -1 && !canUseCurrentPromptHistory()) {
      historyIndexRef.current = -1;
      setHistoryIndex(-1);
    }

    if (canUseCurrentPromptHistory()) {
      e.preventDefault();
      const sourceDraftKey = activeDraftKeyRef.current;
      void (async () => {
        // Keep the navigation result with the draft where the key was pressed;
        // loading older history may outlive a tab switch.
        if (historyIndexRef.current === -1) {
          savedTextRef.current = text; // save current draft
        }
        const sourceIndex = historyIndexRef.current;
        const target =
          historyDirection === "up"
            ? sourceIndex + 1
            : historyDirection === "down"
              ? sourceIndex - 1
              : sourceIndex;
        if (target >= historyEntriesRef.current.length && !(await ensurePromptHistoryIndex(target))) {
          return;
        }
        const next =
          historyDirection === "up"
            ? Math.min(target, historyEntriesRef.current.length - 1)
            : historyDirection === "down"
              ? Math.max(target, -1)
              : sourceIndex;
        const historyText = next === -1 ? null : historyEntriesRef.current[next]?.text ?? "";
        if (sourceDraftKey === activeDraftKeyRef.current) {
          historyIndexRef.current = next;
          setHistoryIndex(next);
          setTextCaretEnd(historyText ?? savedTextRef.current);
        } else {
          const beforeEdit = composerEditSnapshot(sourceDraftKey);
          const draft = cloneComposerDraft(draftsBySessionRef.current[sourceDraftKey] ?? emptyComposerDraft());
          draft.historyIndex = next;
          draft.text = historyText ?? draft.savedText;
          draftsBySessionRef.current[sourceDraftKey] = draft;
          recordComposerEdit(
            sourceDraftKey,
            beforeEdit,
            composerEditSnapshot(sourceDraftKey, { start: draft.text.length, end: draft.text.length }),
          );
        }
        if (historyDirection === "up" && historyEntriesRef.current.length - 1 - next <= PROMPT_HISTORY_PREFETCH_REMAINING) {
          prefetchPromptHistoryTail();
        }
      })();
      return;
    }

    if (menuMode && !composing) {
      if (e.key === "ArrowDown" && count > 0) {
        e.preventDefault();
        if (menuMode === "slash") {
          if (slashSelectableIndices.length > 0) {
            setActive((current) => {
              const currentPosition = slashSelectableIndices.indexOf(current);
              return slashSelectableIndices[(currentPosition + 1) % slashSelectableIndices.length];
            });
          }
        } else {
          setActive((i) => (i + 1) % count);
        }
        return;
      }
      if (e.key === "ArrowUp" && count > 0) {
        e.preventDefault();
        if (menuMode === "slash") {
          if (slashSelectableIndices.length > 0) {
            setActive((current) => {
              const currentPosition = slashSelectableIndices.indexOf(current);
              const previousPosition = currentPosition < 0 ? 0 : currentPosition - 1;
              return slashSelectableIndices[
                (previousPosition + slashSelectableIndices.length) % slashSelectableIndices.length
              ];
            });
          }
        } else {
          setActive((i) => (i - 1 + count) % count);
        }
        return;
      }
      if (e.key === "Enter" || e.key === "Tab") {
        e.preventDefault();
        pickActive();
        return;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        if (menuMode === "pastChats") {
          dismissDirectPastChats();
        } else if (showPastChats) {
          setPastChatQuery("");
          setShowPastChats(false);
          setActive(0);
        } else {
          setDismissed(true);
        }
        return;
      }
    }

    // The send chord (default Enter) sends and the newline chord (default
    // Shift+Enter) breaks the line — both configurable in Settings →
    // Shortcuts. The default send layout retains legacy modified-Enter send
    // aliases; explicit custom bindings are exact. `composing` guards IME confirms.
    if (e.key === "Enter" && !composing) {
      const enterAction = composerEnterAction(e.nativeEvent, shortcutPlatform);
      if (enterAction === "newline-insert") {
        e.preventDefault();
        insertNewlineAtCaret();
        return;
      }
      if (enterAction === "send") {
        e.preventDefault();
        submit();
        return;
      }
      if (enterAction !== "newline-native") {
        e.preventDefault();
        return;
      }
      // "newline-native" falls through so the input inserts the break itself.
    }
    // Esc interrupts the in-flight turn (matches the Stop button's hint), and
    // restores the text if the server hadn't replied yet.
    if (composerEscapeAction(e.nativeEvent, running, composing) === "cancel") {
      e.preventDefault();
      void handleCancel();
    }

    // Browser undo owns ordinary DOM edits, while programmatic composer edits
    // live in the per-draft transaction stacks. Native barriers preserve the
    // real ordering even when later browser edits happen to return to the same
    // text (for example type then Backspace).
    const undoShortcut = matchesShortcut(e.nativeEvent, "composer.undo", shortcutPlatform);
    const redoShortcut = matchesShortcut(e.nativeEvent, "composer.redo", shortcutPlatform)
      || (
        shortcutPlatform !== "darwin"
        && e.ctrlKey
        && !e.metaKey
        && !e.altKey
        && !e.shiftKey
        && e.key.toLowerCase() === "y"
      );
    if (!composing && (undoShortcut || redoShortcut)) {
      const targetDraftKey = activeDraftKeyRef.current;
      const history = editHistoryForDraft(targetDraftKey);
      const current = composerEditSnapshot(targetDraftKey);

      if (undoShortcut) {
        const transaction = history.undo[history.undo.length - 1];
        if (history.undoNativeBarrier) return;
        if (!transaction) return;
        if (!composerEditStateMatches(current, transaction.after)) {
          // A programmatic owner changed state without joining either history.
          // Do not let a stale browser entry mutate that unknown boundary.
          e.preventDefault();
          return;
        }
        e.preventDefault();
        undoComposerEdit(targetDraftKey);
        return;
      }

      const transaction = history.redo[history.redo.length - 1];
      if (history.redoNativeBarrier) return;
      if (!transaction) {
        // A custom edit is a new branch and invalidates native redo entries
        // that the browser cannot see. With no custom history, native redo
        // remains fully browser-owned.
        if (history.undo.length > 0) e.preventDefault();
        return;
      }
      if (!composerEditStateMatches(current, transaction.before)) {
        e.preventDefault();
        return;
      }
      e.preventDefault();
      redoComposerEdit(targetDraftKey);
    }
  };

  // Keydown handler for the past:chats search <input>. The search input is a
  // sibling of the <textarea>, so keyboard events never reach the textarea's
  // onKeyDown. We intercept navigation keys here and delegate to the same
  // menu logic. Regular typing keys (letters, Backspace, etc.) pass through
  // so the user can type a search query.
  const onPastChatSearchKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    const composing = isImeKeyEvent(
      e.nativeEvent,
      pastChatSearchComposingRef.current,
      pastChatSearchLastCompositionEndAt.current,
    );
    if (composerMenuKeyAction(e.nativeEvent, composing) === "handle") {
      e.preventDefault();
      e.stopPropagation();
      if (e.key === "ArrowDown" && count > 0) {
        setActive((i) => (i + 1) % count);
      } else if (e.key === "ArrowUp" && count > 0) {
        setActive((i) => (i - 1 + count) % count);
      } else if (e.key === "Enter" || e.key === "Tab") {
        pickActive();
      } else if (e.key === "Escape") {
        if (menuMode === "pastChats") dismissDirectPastChats();
        else {
          setPastChatQuery("");
          setShowPastChats(false);
          setActive(0);
        }
      }
    }
  };

  // When the run strip is visible inside a user-resized card, the card grows
  // by the strip's reserved height so the meta row stays fully visible.
  // --composer-height stays in logical card-height space. It may be the saved
  // manual floor or a larger content-derived height; the run-strip reservation
  // remains separate so the live resize writer uses the same coordinate space.
  const showRunStrip = Boolean(retry || running);
  const effectiveComposerHeight = composerHeight === null
    ? null
    : resolveComposerContentSizing({
        contentHeight: textareaAutoHeight ?? 0,
        manualLogicalHeight: composerHeight,
        maxLogicalHeight: composerMaxHeight(),
        reservedHeight: COMPOSER_AUTO_RESERVED_HEIGHT,
      }).logicalHeight;
  const composerCardStyle = effectiveComposerHeight === null
    ? undefined
    : ({
        "--composer-height": `${effectiveComposerHeight}px`,
        "--composer-run-strip-reserved": `${showRunStrip ? COMPOSER_RUN_STRIP_RESERVED : 0}px`,
      } as CSSProperties);
  const textareaStyle = !composerResizing && textareaAutoHeight !== null
    ? ({ height: `${textareaAutoHeight}px`, overflowY: textareaAutoOverflow ? "auto" : "hidden" } as CSSProperties)
    : undefined;
  const composerAutoExpanded = composerHeight === null && textareaAutoHeight !== null && textareaAutoHeight > 40;
  // Autosize mode flips overflow-y to auto once content exceeds the max
  // height; the card modifier restores a thin scrollbar for exactly that
  // state so long drafts expose their scrollability (#8494/#8742/#9019).
  const composerAutoOverflow = composerHeight === null && textareaAutoOverflow;
  const composerResizeValue = effectiveComposerHeight ?? clampComposerHeight((textareaAutoHeight ?? 0) + COMPOSER_AUTO_RESERVED_HEIGHT);
  void onSetMode;
  const chooseApprovalMode = (nextMode: ToolApprovalMode) => {
    onSetToolApprovalMode(nextMode);
    requestActiveDraftFrame(focusComposerInput);
  };
  const chooseTaskMode = (nextMode: CollaborationMode) => {
    closeIntentMenu(() => {
      if (nextMode !== collaborationMode) onSetCollaborationMode(nextMode);
      requestActiveDraftFrame(focusComposerInput);
    });
  };
  const floorOn = qualityFloor === "delivery";
  const chooseQualityFloor = (floor: QualityFloor) => {
    if (floor === qualityFloor) return;
    onSetQualityFloor?.(floor);
  };
  const stopGoalMode = () => {
    closeIntentMenu(() => {
      onClearGoal();
      requestActiveDraftFrame(focusComposerInput);
    });
  };
  const taskModeShortKey = collaborationMode === "plan"
    ? "composer.taskModePlanShort"
    : collaborationMode === "goal"
      ? "composer.taskModeGoalShort"
      : "composer.taskModeDirectShort";
  const taskModeTooltipSummaryKey = collaborationMode === "plan"
    ? "composer.taskModePlanTooltipSummary"
    : collaborationMode === "goal"
      ? "composer.taskModeGoalTooltipSummary"
      : "composer.taskModeDirectTooltipSummary";
  const TaskModeIcon = collaborationMode === "plan" ? List : collaborationMode === "goal" ? Target : ArrowRight;
  const taskModeTriggerLabel = t("composer.taskModeTrigger", { mode: t(taskModeShortKey) });
  const taskModeTooltipLabel = t("composer.controlTooltip", {
    category: t("composer.intentMenuTitle"),
    mode: t(taskModeShortKey),
    summary: t(taskModeTooltipSummaryKey),
  });
  const effortOptions = asArray(effort?.options);
  const effortLabel = (id: string) => effortOptions.find((option) => option.id === id)?.name || id;
  const effortLevels = effort?.options ? ["auto", ...effortOptions.map((option) => option.id)] : asArray(effort?.levels);
  const currentEffort = effort?.current || "auto";
  const compactEffortTitle = currentEffort === "auto"
    ? t("status.effortAutoTitle", { def: effort?.default || "auto" })
    : `${t("status.effortTitle")}: ${currentEffort}`;
  const hasEffort = Boolean(effort?.supported && effortLevels.length > 0);
  const chooseEffortLevel = (level: string) => {
    closeMoreMenu(() => {
      if (level !== currentEffort) onSetEffort(level);
      requestActiveDraftFrame(focusComposerInput);
    });
  };
  // Run-strip state machine: retry > waiting-approval > waiting-ask > streaming.
  // Decision surfaces own the "waiting on user" UI; while suspendedByDecision
  // is true we still pause the work clock but do not render a waiting strip.
  const waitingPrompt = suspendedByDecision
    ? null
    : pendingApprovalLabel
      ? "approval"
      : pendingAsk
        ? "ask"
        : null;
  const pauseWorkClock = suspendedByDecision || Boolean(waitingPrompt);
  // Decision surfaces hide the whole composer, so mode controls stay disabled.
  // Legacy tests that pass pendingApprovalLabel without suspendedByDecision
  // still keep the approval bar usable mid-prompt.
  const approvalBarDisabled = Boolean(disabled) && !(pendingApprovalLabel && !suspendedByDecision);
  // Waiting on the user is not model work. Approval/ask wait is owned by the
  // per-tab controller (turnWaitAccumMs + promptWaitStartedAt) so background
  // tabs keep accumulating. Composer only tracks local pauses for surfaces the
  // controller does not know about (clear-context, legacy strip tests).
  const controllerTracksWait = typeof promptWaitStartedAt === "number" && promptWaitStartedAt > 0;
  const controllerWaitMs = Math.max(0, turnWaitAccumMs || 0)
    + (controllerTracksWait ? Math.max(0, now - promptWaitStartedAt) : 0);
  const trackLocalPause = pauseWorkClock && !controllerTracksWait;
  const [localWaitAccumMs, setLocalWaitAccumMs] = useState(0);
  const localPauseSinceRef = useRef<number | null>(null);
  useEffect(() => {
    localPauseSinceRef.current = null;
    setLocalWaitAccumMs(0);
    if (trackLocalPause) localPauseSinceRef.current = Date.now();
    // trackLocalPause is read from the render that changed draft/turn.
    // eslint-disable-next-line react-hooks/exhaustive-deps -- intentional scope-only reset
  }, [draftKey, turnStartAt]);
  useEffect(() => {
    if (trackLocalPause) {
      if (localPauseSinceRef.current == null) localPauseSinceRef.current = Date.now();
      return;
    }
    if (localPauseSinceRef.current == null) return;
    const delta = Date.now() - localPauseSinceRef.current;
    localPauseSinceRef.current = null;
    if (delta > 0) setLocalWaitAccumMs((total) => total + delta);
  }, [trackLocalPause]);
  const localOpenWaitMs = localPauseSinceRef.current != null
    ? Math.max(0, now - localPauseSinceRef.current)
    : 0;
  const waitAccumMs = controllerWaitMs + localWaitAccumMs + localOpenWaitMs;
  // Close menus/popovers while a decision surface owns the footer.
  useEffect(() => {
    if (!suspendedByDecision) return;
    setDismissed(true);
    setContentMenuOpen(false);
    setDirectPastChats(false);
    setShowPastChats(false);
    closeIntentMenu();
    closeMoreMenu();
  }, [suspendedByDecision, closeIntentMenu, closeMoreMenu]);
  // Live text+reasoning character count for the run-strip TPS fallback. Reads
  // through the live store's own subscription so stream deltas re-render only
  // this component — the controller's bump path stays text-delta-free.
  const subscribeLiveText = useCallback(
    (cb: () => void) => liveStore?.subscribe(tabId, cb) ?? (() => {}),
    [liveStore, tabId],
  );
  const liveTextChars = useSyncExternalStore(
    subscribeLiveText,
    () => {
      const live = liveStore?.getSnapshot(tabId);
      return live ? live.text.length + live.reasoning.length : 0;
    },
  );
  const liveModelActiveAt = useSyncExternalStore(
    subscribeLiveText,
    () => liveStore?.getModelActiveAt?.(tabId),
  );
  const turnPhaseLabel = (() => {
    switch ((turnPhase ?? "").trim()) {
      case "checking":
        return t("composer.turnPhaseChecking");
      case "verifying":
        return t("composer.turnPhaseVerifying");
      case "reviewing":
        return t("composer.turnPhaseReviewing");
      case "working":
        return t("composer.turnPhaseWorking");
      default:
        return t("composer.runAnnounceRunning");
    }
  })();
  const runStateText = retry
    ? recoveryStatusText(t, retry, now)
    : waitingPrompt === "approval"
      ? t("composer.runWaitingApproval", { tool: pendingApprovalLabel ?? "" })
      : waitingPrompt === "ask"
        ? t("composer.runWaitingAsk")
        : running && !suspendedByDecision
          ? turnPhaseLabel
          : null;
  const runTicker = !retry && !pauseWorkClock && running && turnStartAt
    ? (() => {
        const elapsedMs = Math.max(0, now - turnStartAt - waitAccumMs);
        const words = SPINNER_WORDS[locale] ?? SPINNER_WORDS.en;
        const word = words[Math.floor(elapsedMs / 3000) % words.length];
        const usageTokens = turnTokens ?? 0;
        // Include streaming tool-call args in the estimate so TPS stays
        // meaningful while the model streams a write_file / long tool body.
        const inFlightChars = Math.max(0, liveTextChars - (turnOutputCharsAtUsage ?? 0)) + (turnArgChars ?? 0);
        const estimatedChars = Math.round(inFlightChars / 4);
        const liveTokens = usageTokens + estimatedChars;
        const tok = liveTokens > 0 ? ` · ↓ ${formatTokens(liveTokens)} ${t("status.tokens")}` : "";
        const outTok: number = (turnOutputTokens ?? 0) + estimatedChars;
        const modelActiveAt = liveModelActiveAt ?? turnModelActiveAt;
        const modelElapsedMs = Math.max(0, turnModelActiveMs + (modelActiveAt && modelActiveAt > 0 ? Math.max(0, now - modelActiveAt) : 0));
        const tps = outTok > 0 && modelElapsedMs >= 500 ? Math.round(outTok / (modelElapsedMs / 1000)) : null;
        const tpsStr = tps !== null ? ` · ${tps} tokens/s` : "";
        const suffix = `${tpsStr}${tok}`;
        const prefix = `${word}… ${fmtElapsed(elapsedMs)}`;
        return { prefix, suffix: suffix || null };
      })()
    : null;
  const submitEmpty = !text.trim() && attachments.length === 0 && workspaceRefs.length === 0 &&
    !invocations.some((invocation) => invocation.command.kind === "skill");
  const submitBlocked = submitting || pendingPaste > 0 || (submitEmpty && !(goalModeOn && !activeGoal)) || disabled || (!running && submitDisabled) || readOnly;
  const submitTooltip = running
    ? t("composer.queueGuidance", { combo: sendComboLabel })
    : t("composer.send", { combo: sendComboLabel });
  const composerPlaceholder = readOnly
    ? t("composer.readOnlyChannel")
    : disabled
      ? t("common.loading")
      : running
        ? t("composer.steerPlaceholder", { combo: sendComboLabel })
        : goalModeOn && !activeGoal
          ? t("composer.goalInputPlaceholder")
          : t("composer.placeholder");
  const composerMetaClass = [
    "composer-meta",
    hasEffort ? "composer-meta--has-effort" : "composer-meta--no-effort",
  ].join(" ");

  const inputSelection = getInputSelection();
  const hasInputSelection = inputSelection.from !== inputSelection.to;
  // Platform-correct hint: ⌘ on macOS, Ctrl elsewhere — same formatter the
  // shortcut settings UI uses.
  const editMenuShortcut = (key: string) =>
    formatShortcutCombo(
      shortcutPlatform === "darwin" ? { key, meta: true } : { key, ctrl: true },
      shortcutPlatform,
    );
  const inputMenuItems: ContextMenuItem[] = [
    {
      key: "undo",
      label: t("shortcuts.action.composerUndo"),
      shortcut: undoComboLabel,
      disabled: disabled || !canUndoComposerEdit(activeDraftKeyRef.current),
      onSelect: () => {
        setInputMenuPoint(null);
        undoComposerEdit(activeDraftKeyRef.current);
      },
    },
    {
      key: "redo",
      label: t("shortcuts.action.composerRedo"),
      shortcut: redoComboLabel,
      disabled: disabled || !canRedoComposerEdit(activeDraftKeyRef.current),
      onSelect: () => {
        setInputMenuPoint(null);
        redoComposerEdit(activeDraftKeyRef.current);
      },
    },
    {
      type: "separator",
      key: "edit-history-separator",
    },
    {
      key: "cut",
      label: t("common.cut"),
      shortcut: editMenuShortcut("x"),
      disabled: disabled || !hasInputSelection,
      onSelect: () => void copyComposerSelection(true),
    },
    {
      key: "copy",
      label: t("common.copy"),
      shortcut: editMenuShortcut("c"),
      disabled: !hasInputSelection,
      onSelect: () => void copyComposerSelection(),
    },
    {
      key: "paste",
      label: t("common.paste"),
      shortcut: editMenuShortcut("v"),
      disabled,
      onSelect: () => void pasteIntoComposer(),
    },
    {
      key: "select-all",
      label: t("common.selectAll"),
      shortcut: editMenuShortcut("a"),
      disabled: text.length === 0,
      onSelect: selectAllComposerText,
    },
  ];

  return (
    <div
      ref={composerWrapRef}
      className={[
        "composer-wrap",
        decisionPending ? "composer-wrap--decision-pending" : "",
        heroMode ? "composer-wrap--hero" : "",
      ].filter(Boolean).join(" ")}
      style={attachmentInputEnabled ? { "--wails-drop-target": "drop" } as CSSProperties : undefined}
      onDropCapture={onFileDropCapture}
    >
      <input
        ref={fileInputRef}
        className="composer-content-file-input"
        type="file"
        multiple
        disabled={!attachmentInputEnabled}
        tabIndex={-1}
        aria-hidden="true"
        onChange={(event) => {
          const files = Array.from(event.currentTarget.files ?? []);
          event.currentTarget.value = "";
          if (files.length > 0) attachFiles(files);
          requestActiveDraftFrame(() => taRef.current?.focus());
        }}
      />
      <AnchoredPopover
        open={contentMenuOpen && !disabled && !readOnly && !running}
        anchorRef={contentMenuAnchorRef}
        onClose={() => setContentMenuOpen(false)}
        className="composer-access-menu composer-content-menu"
        align="start"
      >
        <ComposerContentMenuActions
          attachmentInputEnabled={attachmentInputEnabled}
          textPresent={text.trim().length > 0}
          onChooseAttachment={chooseAttachmentFiles}
          onInsertTrigger={insertContentTrigger}
        />
      </AnchoredPopover>
      {!heroMode && <AnchoredPopover
        open={intentMenuOpen}
        closing={intentMenuClosing}
        anchorRef={intentMenuAnchorRef}
        onClose={() => closeIntentMenu()}
        className="composer-access-menu composer-intent-menu"
        align="start"
      >
        <div
          className="composer-access-menu__section"
          role="menu"
          aria-label={t("composer.intentMenuTitle")}
          onMouseEnter={creationChrome ? onIntentPopoverEnter : undefined}
          onMouseLeave={creationChrome ? onIntentHoverLeave : undefined}
        >
          <div className="composer-access-menu__label">{t("composer.intentMenuTitle")}</div>
          <button
            type="button"
            role="menuitemradio"
            aria-checked={collaborationMode === "normal"}
            className={`composer-access-menu__item composer-intent-menu__item${collaborationMode === "normal" ? " composer-access-menu__item--active" : ""}`}
            onClick={() => chooseTaskMode("normal")}
            disabled={disabled || running}
          >
            <ArrowRight size={16} />
            <span className="composer-access-menu__copy">
              <span className="composer-access-menu__title">{t("composer.taskModeDirect")}</span>
              <span className="composer-access-menu__desc">{t("composer.taskModeDirectDesc")}</span>
            </span>
            {collaborationMode === "normal" && <Check className="composer-intent-menu__check" size={16} aria-hidden="true" />}
          </button>
          <button
            type="button"
            role="menuitemradio"
            aria-checked={planModeOn}
            className={`composer-access-menu__item composer-intent-menu__item${planModeOn ? " composer-access-menu__item--active" : ""}`}
            onClick={() => chooseTaskMode("plan")}
            disabled={disabled || running}
          >
            <List size={16} />
            <span className="composer-access-menu__copy">
              <span className="composer-access-menu__title">{t("composer.taskModePlan")}</span>
              <span className="composer-access-menu__desc">{t("composer.taskModePlanDesc")}</span>
            </span>
            {planModeOn && <Check className="composer-intent-menu__check" size={16} aria-hidden="true" />}
          </button>
          <button
            type="button"
            role="menuitemradio"
            aria-checked={goalModeOn}
            className={`composer-access-menu__item composer-intent-menu__item${goalModeOn ? " composer-access-menu__item--active" : ""}`}
            onClick={() => chooseTaskMode("goal")}
            disabled={disabled || running}
            title={activeGoal || undefined}
          >
            <Target size={16} />
            <span className="composer-access-menu__copy">
              <span className="composer-access-menu__title">{t("composer.taskModeGoal")}</span>
              <span className="composer-access-menu__desc">{activeGoal || t("composer.taskModeGoalDesc")}</span>
            </span>
            {goalModeOn && <Check className="composer-intent-menu__check" size={16} aria-hidden="true" />}
          </button>
            {goalModeOn && activeGoal && (
            <div className="composer-intent-menu__goal-actions">
              <div className="composer-intent-menu__goal-runtime">
                {goalRuntime && (
                  <span className="composer-intent-menu__goal-runtime-line">
                    {t("composer.goalRuntimeLine", {
                      turnsUsed: goalRuntime.turnsUsed,
                      tokensUsed: formatTokens(goalRuntime.tokensUsed),
                      requestsUsed: goalRuntime.requestsUsed ?? 0,
                      workTime: formatGoalWorkTime(goalRuntime.workDurationMs),
                    })}
                  </span>
                )}
                {goalStatus === "blocked" && !goalRuntime?.stopCause && (
                  <span className="composer-intent-menu__goal-runtime-line composer-intent-menu__goal-runtime-line--blocked">
                    {t("composer.goalBlocked")}
                  </span>
                )}
                {goalStatus === "blocked" && goalRuntime?.stopCause && (
                  <span className="composer-intent-menu__goal-runtime-line composer-intent-menu__goal-runtime-line--paused">
                    {t("composer.goalPaused")}
                    {goalRuntime.lastReason ? ` — ${goalRuntime.lastReason}` : ""}
                  </span>
                )}
              </div>
              {goalStatus === "blocked" ? (
                <button
                  type="button"
                  className="composer-intent-menu__stop"
                  onClick={onResumeGoal}
                  disabled={disabled}
                >
                  {t("composer.taskModeResumeGoal")}
                </button>
              ) : (
                <button
                  type="button"
                  className="composer-intent-menu__stop"
                  onClick={onPauseGoal}
                  disabled={disabled || running}
                >
                  {t("composer.taskModePauseGoal")}
                </button>
              )}
              <button
                type="button"
                className="composer-intent-menu__stop"
                onClick={stopGoalMode}
                disabled={disabled || running}
              >
                {t("composer.taskModeStopGoal")}
              </button>
            </div>
          )}
        </div>
      </AnchoredPopover>}
      <AnchoredPopover
        open={moreMenuOpen && !disabled && !running}
        closing={moreMenuClosing}
        anchorRef={moreMenuAnchorRef}
        onClose={() => closeMoreMenu()}
        className="composer-access-menu composer-more-menu"
        align="end"
      >
        {hasEffort && (
          <div className="composer-access-menu__section">
            <div className="composer-access-menu__label">{t("status.effortTitle")}</div>
            <div className="composer-more-menu__items" role="listbox" aria-label={t("status.effortTitle")}>
              {effortLevels.map((level) => (
                <button
                  key={level}
                  type="button"
                  role="option"
                  aria-selected={level === currentEffort}
                  className={`composer-more-menu__item${level === currentEffort ? " composer-more-menu__item--active" : ""}`}
                  onClick={() => chooseEffortLevel(level)}
                  disabled={running}
                >
                  <Gauge size={14} />
                  <span>{effortLabel(level)}</span>
                  {level === currentEffort && <Check size={13} />}
                </button>
              ))}
            </div>
          </div>
        )}
      </AnchoredPopover>
      {menuMode === "slash" && (
        <SlashMenu
          items={slashMatches}
          activeIndex={active}
          onPick={pickCommand}
          onHover={setActive}
          isDisabled={slashCommandDisabled}
          disabledReason={t("slash.startOnly")}
        />
      )}
      {menuMode === "slasharg" && argRes && (
        <ArgMenu items={argRes.items} activeIndex={active} onPick={pickArg} onHover={setActive} />
      )}
      {(menuMode === "at" || menuMode === "pastChats") && (
        showPastChats ? (
          <div className="slashmenu" role="listbox">
            {loadingPastChats ? (
              <div className="slashmenu__item slashmenu__item--empty">
                <span className="slashmenu__name">{t("composer.pastChatsLoading")}</span>
              </div>
            ) : pastChats.length === 0 ? (
              <div className="slashmenu__item slashmenu__item--empty">
                <span className="slashmenu__name">{t("composer.pastChatsEmpty")}</span>
              </div>
            ) : (
              <>
                <div className="slashmenu__item slashmenu__item--search" onMouseDown={(ev) => ev.preventDefault()}>
                  <Search size={13} className="filemenu__icon" />
                  <input
                    className="slashmenu__search"
                    type="text"
                    placeholder={t("composer.pastChatsSearch")}
                    value={pastChatQuery}
                    // In the token-driven flows (typed "#" or the content-menu
                    // action) focus must stay in the composer: typing there
                    // extends the token and filters the list, and stealing
                    // focus mid-word hijacks ordinary "#123" text. Only the
                    // @-flow subpanel, which has no composer token to type
                    // into, moves focus here.
                    autoFocus={!directPastChats}
                    onChange={(ev) => {
                      setPastChatQuery(ev.target.value);
                      setActive(0);
                    }}
                    onCompositionStart={() => {
                      pastChatSearchComposingRef.current = true;
                    }}
                    onCompositionEnd={() => {
                      pastChatSearchComposingRef.current = false;
                      pastChatSearchLastCompositionEndAt.current = Date.now();
                    }}
                    onBlur={() => {
                      pastChatSearchComposingRef.current = false;
                    }}
                    onKeyDown={onPastChatSearchKeyDown}
                  />
                </div>
                {filteredPastChats.length === 0 ? (
                  <div className="slashmenu__item slashmenu__item--empty">
                    <span className="slashmenu__name">{t("composer.pastChatsNoMatches")}</span>
                  </div>
                ) : (
                  filteredPastChats.map((session, i) => {
                    // Hover preview stays on SessionMeta and never reads the transcript.
                    const turnsLabel = sessionTurnsLabel(session, t);
                    const ts = session.lastActivityAt || session.modTime || session.createdAt;
                    const preview = truncatePreview(session.preview);
                    const pathText = session.workspaceRoot || session.path;
                    const tooltipLabel =
                      turnsLabel || ts || preview || pathText ? (
                        <div className="past-chat-hover">
                          <div className="past-chat-hover__title">{pastChatTitle(session)}</div>
                          {preview && <div className="past-chat-hover__preview">{preview}</div>}
                          {(turnsLabel || ts) && (
                            <div className="past-chat-hover__meta">
                              {turnsLabel && <span>{turnsLabel}</span>}
                              {ts && <span>· {fmtSessionTime(ts)}</span>}
                            </div>
                          )}
                          {pathText && <div className="past-chat-hover__path">{pathText}</div>}
                        </div>
                      ) : null;
                    return (
                      <Tooltip key={session.path} block label={tooltipLabel}>
                        <button
                          className={`slashmenu__item ${i === active ? "slashmenu__item--active" : ""}`}
                          onMouseDown={(ev) => {
                            ev.preventDefault();
                            pickSession(session);
                          }}
                          onMouseMove={() => setActive(i)}
                        >
                          <MessageSquare size={13} className="filemenu__icon" />
                          <span className="slashmenu__name slashmenu__name--file">
                            {pastChatTitle(session)}
                            {turnsLabel ? ` (${turnsLabel})` : ""}
                          </span>
                        </button>
                      </Tooltip>
                    );
                  })
                )}
              </>
            )}
            <button
              className="slashmenu__item slashmenu__item--back"
              onMouseDown={(ev) => {
                ev.preventDefault();
                if (menuMode === "pastChats") dismissDirectPastChats();
                else {
                  setPastChatQuery("");
                  setShowPastChats(false);
                  setActive(0);
                }
              }}
            >
              <span className="slashmenu__name">
                {menuMode === "pastChats" ? t("composer.contentCloseSessions") : t("composer.backToFiles")}
              </span>
            </button>
          </div>
        ) : menuMode === "at" ? (
          <VirtualMenu
            items={atMenuItems}
            activeIndex={active}
            itemKey={atMenuItemKey}
            renderItem={(it, i) =>
              it.kind === "pastChats" ? (
                <button
                  className={`slashmenu__item${i === active ? " slashmenu__item--active" : ""}`}
                  onMouseDown={(ev) => {
                    ev.preventDefault();
                    void openPastChats();
                  }}
                  onMouseMove={() => setActive(i)}
                >
                  <MessageSquare size={13} className="filemenu__icon" />
                  <span className="slashmenu__name">{PAST_CHATS_MENU_ITEM}</span>
                </button>
              ) : (
                <button
                  role="option"
                  aria-selected={i === active}
                  className={`slashmenu__item ${i === active ? "slashmenu__item--active" : ""}`}
                  onMouseDown={(ev) => {
                    ev.preventDefault();
                    pickEntry(it.entry);
                  }}
                  onMouseMove={() => setActive(i)}
                >
                  {it.entry.isDir ? (
                    <Folder size={13} className="filemenu__icon filemenu__icon--dir" />
                  ) : (
                    <FileText size={13} className="filemenu__icon" />
                  )}
                  <span className="slashmenu__name slashmenu__name--file">
                    {dirEntryMenuLabel(it.entry)}
                    {it.entry.isDir ? "/" : ""}
                  </span>
                </button>
              )
            }
          />
        ) : null
      )}
      {pendingGuidance.length > 0 && (
        <Suspense fallback={null}>
          <ComposerGuidanceShelf
            recovery={pendingGuidance[0]?.paused && !pendingGuidance.some((item) => guidanceIsInFlight(item.state)) ? {
              draftKey,
              tabId: tabId || "",
              count: pendingGuidance[0].recoveredCount || pendingGuidance.length,
              recovered: Boolean(pendingGuidance[0].recoveredCount),
            } : null}
            recoveryDisabled={Boolean(disabled || readOnly)}
            items={pendingGuidance}
            expanded={guidanceExpanded}
            running={running}
            disabled={Boolean(disabled)}
            readOnly={readOnly}
            sendingId={guidanceSendingId}
            onReview={() => setGuidanceExpanded(true)}
            onRecoveryResumed={() => setGuidanceRetryNonce((value) => value + 1)}
            onRecoveryError={(error) => showToast(formatInboxError(error, locale), "warn")}
            onToggleExpanded={() => setGuidanceExpanded((value) => !value)}
            onSend={(item) => void sendQueuedGuidance(item)}
            onDismiss={(item) => void dismissQueuedGuidance(item)}
            onEdit={(item, text) => editQueuedGuidance(item, text)}
          />
        </Suspense>
      )}
      <ComposerPinnedFilesShelf tabId={tabId || ""} pinnedFiles={pinnedFiles} />
      {(attachments.length > 0 || workspaceRefs.length > 0 || sessionRefs.length > 0 || selectedTextRefs.length > 0) && (
        <div className="composer-context" aria-label={t("composer.contextItems")}>
          {sortComposerAttachments(attachments).map((a) => {
            const imageOnly = Boolean(a.previewUrl) && attachments.every((item) => item.previewUrl) && workspaceRefs.length === 0 && sessionRefs.length === 0;
            return (
              <ComposerContextCard
                key={a.path}
                variant="attachment"
                tooltipLabel={a.previewUrl ? `${t("imageViewer.clickToPreview")} — ${a.path}` : a.path}
                removeLabel={t("composer.removeImage")}
                onRemove={() => removeAttachment(a.path)}
                previewUrl={a.previewUrl}
                onImageClick={a.previewUrl ? () => openComposerImageViewer(a.previewUrl!, attachmentName(a)) : undefined}
                imageOnly={imageOnly}
                name={attachmentName(a)}
                meta={attachmentExt(attachmentName(a)) || t("msg.fileAttachment")}
              />
            );
          })}
          {workspaceRefs.map((ref) => (
            <ComposerContextCard
              key={workspaceReferenceKey(ref)}
              variant="workspace"
              tooltipLabel={ref.displayPath ? formatWorkspaceReference(ref.displayPath, ref.isDir) : formatWorkspaceReference(ref.path, ref.isDir)}
              removeLabel={t("composer.removeReference")}
              onRemove={() => removeWorkspaceReference(ref)}
              folder={Boolean(ref.isDir)}
              label={ref.isDir ? `${baseName(ref.displayPath || ref.path)}/` : baseName(ref.displayPath || ref.path)}
            />
          ))}
          {sessionRefs.map((ref) => (
            <div
              className="composer-context__item composer-context__item--session"
              key={ref.path}
            >
              <Tooltip label={ref.preview || ref.title}>
                <span className="composer-context__label">
                  <MessageSquare size={15} />
                  <span>
                    {ref.title}
                    {sessionTurnsLabel(ref, t) ? ` (${sessionTurnsLabel(ref, t)})` : ""}
                  </span>
                </span>
              </Tooltip>
              <Tooltip label={t("composer.removeSessionReference")}>
                <button
                  type="button"
                  onClick={() => removeSessionRef(ref.path)}
                >
                  <X size={13} />
                </button>
              </Tooltip>
            </div>
          ))}
          {selectedTextRefs.map((reference) => (
            <ComposerContextCard
              key={reference.id}
              variant="selection"
              tooltipLabel={reference.path
                ? <CodeViewer value={reference.text} language={languageFor(reference.path)} maxHeight={240} />
                : reference.source === "terminal"
                  ? <CodeViewer value={reference.text} language="console" maxHeight={240} />
                  : <Markdown text={reference.text} />}
              removeLabel={t("composer.removeSelectedText")}
              onRemove={() => {
                const next = selectedTextRefsRef.current.filter((item) => item.id !== reference.id);
                selectedTextRefsRef.current = next;
                setSelectedTextRefs(next);
                requestActiveDraftFrame(focusComposerInput);
              }}
              name={reference.path ? reference.path.split("/").filter(Boolean).pop() ?? reference.path : selectedTextSnippet(reference.text)}
              meta={reference.path
                ? t("composer.selectedCode")
                : reference.source === "terminal"
                  ? t("composer.selectedTerminal")
                  : t("composer.selectedText")}
              icon={reference.path
                ? <FileText size={20} />
                : <MessageSquare size={20} />}
            />
          ))}
        </div>
      )}
      <ImageViewer
        open={imageViewer.open}
        imageUrl={imageViewer.url}
        imageName={imageViewer.name}
        onClose={closeComposerImageViewer}
      />
      {activePastedBlocks.length > 0 && (
        <div className="composer__pasted">
          {activePastedBlocks.map((block) => {
            const open = openPastedLabels.includes(block.label);
            return (
              <div className="composer__pasted-block" key={block.label}>
                <div className="composer__pasted-head">
                  <FileText size={15} />
                  <span className="composer__pasted-label">{block.label}</span>
                  <div className="composer__pasted-actions">
                    <Tooltip label={t(open ? "composer.pastedHidePreview" : "composer.pastedShowPreview")}>
                      <button type="button" onClick={() => togglePastedPreview(block.label)}>
                        <Eye size={14} />
                      </button>
                    </Tooltip>
                    <Tooltip label={t("composer.pastedExpand")}>
                      <button type="button" onClick={() => expandPastedBlock(block)}>
                        {t("composer.pastedExpand")}
                      </button>
                    </Tooltip>
                    <Tooltip label={t("composer.pastedRemove")}>
                      <button type="button" onClick={() => removePastedBlock(block)}>
                        <Trash2 size={14} />
                      </button>
                    </Tooltip>
                  </div>
                </div>
                {open && <pre className="composer__pasted-preview">{block.text}</pre>}
              </div>
            );
          })}
        </div>
      )}
      {retry?.recovery?.waiting && <Suspense fallback={null}><RecoveryWaitBanner retry={retry} now={now} onStop={() => void handleCancel()} stopDisabled={cancelSettlingDraftsRef.current.has(draftKey)} /></Suspense>}
      <div
        className={`composer-card${composerHeight !== null || composerResizing ? " composer-card--resized" : ""}${composerAutoExpanded ? " composer-card--autosized" : ""}${composerAutoOverflow ? " composer-card--auto-overflow" : ""}${composerResizing ? " composer-card--resizing" : ""}${running ? (waitingPrompt ? " composer-card--waiting" : " composer-card--running") : ""}`}
        ref={composerCardRef}
        style={composerCardStyle}
      >
        <button
          className="composer-resize-handle"
          type="button"
          role="separator"
          aria-orientation="horizontal"
          aria-label={t("composer.resize")}
          aria-valuemin={COMPOSER_MIN_HEIGHT}
          aria-valuemax={composerMaxHeight()}
          aria-valuenow={composerResizeValue}
          title={t("composer.resize")}
          onPointerDown={onComposerResizeStart}
          onKeyDown={onComposerResizeKeyDown}
          onDoubleClick={resetComposerHeight}
        />
        {runStateText && (
          <div className={`composer-run-strip${waitingPrompt ? " composer-run-strip--waiting" : ""}`}>
            <span className="composer-run-strip__dot" aria-hidden="true" />
            {runTicker ? (
              <>
                <span className="composer-run-strip__text" aria-hidden="true">{runTicker.prefix}</span>
                {runTicker.suffix && (
                  <Tooltip label={t("composer.runStripEstimateHint")}>
                    <span className="composer-run-strip__text" aria-hidden="true">{runTicker.suffix}</span>
                  </Tooltip>
                )}
              </>
            ) : (
              <span className="composer-run-strip__text">{runStateText}</span>
            )}
            <span className="sr-only" role="status">{runStateText}</span>
          </div>
        )}
        <div
          className={`composer${invocations.length > 0 ? " composer--has-invocation" : ""}${dragOver ? " composer--dragover" : ""}${disabled || readOnly ? " composer--disabled" : ""}${shellModeActive ? " composer--shell" : ""}`}
          onDrop={onDrop}
          onDragOver={onDragOver}
          onDragLeave={onDragLeave}
        >
          <div className="composer__input-row">
            <span className="composer__caret">{shellModeActive ? "$" : "›"}</span>
            <div className="composer__content" onMouseDown={focusComposerFromContentBlank}>
              {invocations.length > 0 ? (
                <RichComposerInput
                  ref={richInputRef}
                  text={text}
                  invocations={invocations}
                  placeholder={composerPlaceholder}
                  disabled={disabled || readOnly}
                  style={textareaStyle}
                  onChange={(
                    nextText,
                    nextInvocations,
                    origin: RichComposerChangeOrigin,
                  ) => {
                    const targetDraftKey = activeDraftKeyRef.current;
                    const beforeEdit = origin.source === "programmatic"
                      ? composerEditSnapshot(targetDraftKey, origin.beforeSelection)
                      : null;
                    resetPromptHistoryNavigation();
                    const hadInvocations = invocationsRef.current.length > 0;
                    textRef.current = nextText;
                    invocationsRef.current = nextInvocations;
                    setText(nextText);
                    setInvocations(nextInvocations);
                    if (beforeEdit) {
                      recordComposerEdit(
                        targetDraftKey,
                        beforeEdit,
                        composerEditSnapshot(targetDraftKey, origin.afterSelection),
                      );
                    } else {
                      syncComposerNativeHistory(targetDraftKey, origin.inputType);
                    }
                    if (composerPrompt) setComposerPrompt(null);
                    if (hadInvocations && nextInvocations.length === 0) {
                      // Removing the last entity unmounts the rich input and
                      // swaps the plain textarea back in; without an explicit
                      // handoff the focused editable disappears and the next
                      // keystrokes land on <body>. RichComposerInput reports
                      // the removal caret through onSelectionChange before
                      // this onChange fires.
                      setComposerSelection(Math.min(lastSelectionRef.current.start, nextText.length));
                    }
                  }}
                  onSelectionChange={(selection, query) => {
                    setRichSelection(selection);
                    setRichSlashQuery(query);
                    lastSelectionRef.current = { start: selection.start, end: selection.end };
                  }}
                  onKeyDown={onKeyDown}
                  onContextMenu={openInputMenu}
                  onPaste={onPaste}
                  onCompositionStart={() => {
                    composingRef.current = true;
                  }}
                  onCompositionEnd={() => {
                    composingRef.current = false;
                    lastCompositionEndAt.current = Date.now();
                  }}
                />
              ) : (
                <>
                  <textarea
                    id="composer-input"
                    ref={taRef}
                    className="composer__input"
                    aria-label={t("composer.placeholder")} spellCheck={false} autoCorrect="off" autoCapitalize="off"
                    value={composingRef.current ? undefined : text}
                    onInputCapture={(e) => {
                      pendingNativeInputTypeRef.current = (e.nativeEvent as InputEvent).inputType;
                    }}
                    onChange={(e) => {
                      const targetDraftKey = activeDraftKeyRef.current;
                      const inputType = (e.nativeEvent as InputEvent).inputType
                        || pendingNativeInputTypeRef.current;
                      pendingNativeInputTypeRef.current = undefined;
                      trackImeInputChange(e.nativeEvent as InputEvent, inputType, e.target.value);
                      resetPromptHistoryNavigation();
                      textRef.current = e.target.value;
                      setText(e.target.value);
                      const nextSelection = {
                        start: e.target.selectionStart ?? e.target.value.length,
                        end: e.target.selectionEnd ?? e.target.value.length,
                      };
                      lastSelectionRef.current = nextSelection;
                      setPlainSelection(nextSelection);
                      syncComposerNativeHistory(targetDraftKey, inputType);
                      if (composerPrompt) setComposerPrompt(null);
                    }}
                    onSelect={rememberCaret}
                    onClick={rememberCaret}
                    onKeyUp={rememberCaret}
                    onFocus={rememberCaret}
                    onContextMenu={openInputMenu}
                    onPaste={onPaste}
                    onKeyDown={onKeyDown}
                    style={textareaStyle}
                    placeholder={composerPlaceholder}
                    rows={1}
                    disabled={disabled || readOnly}
                  />
                  <textarea
                    ref={measureTaRef} className="composer__input composer__input--measure"
                    value={text} readOnly aria-hidden="true" tabIndex={-1}
                  />
                </>
              )}
            </div>
            {composerPrompt && (
              <span className="composer__prompt" role="status">
                {composerPrompt}
              </span>
            )}
            {running && (
              <Tooltip label={t("composer.stop")}>
                <button
                  className="composer__btn composer__btn--stop"
                  type="button"
                  onClick={() => void handleCancel()}
                  disabled={cancelSettlingDraftsRef.current.has(draftKey)}
                  aria-label={t("composer.stop")}
                >
                  <Square size={12} fill="currentColor" />
                </button>
              </Tooltip>
            )}
            <Tooltip label={submitTooltip}>
              <button
                className={`composer__btn composer__btn--send${running ? " composer__btn--steer" : ""}`}
                onClick={submit}
                disabled={submitBlocked}
                aria-label={submitTooltip}
              >
                {running ? <CornerDownRight size={16} /> : <ArrowUp size={16} />}
              </button>
            </Tooltip>
          </div>
        </div>
        <ContextMenu
          open={inputMenuPoint !== null}
          point={inputMenuPoint}
          items={inputMenuItems}
          className="context-menu--composer-input"
          minWidth={64}
          ariaLabel={t("composer.inputActions")}
          onClose={() => setInputMenuPoint(null)}
        />
        <div className={composerMetaClass}>
          <div className="composer-meta__params">
            {!heroMode && (
              <div className="composer-meta__control composer-meta__control--content">
                <Tooltip label={t("composer.contentMenuTitle")} disabled={contentMenuOpen}>
                  <button
                    ref={contentMenuAnchorRef}
                    type="button"
                    className={`composer-content-trigger${contentMenuOpen ? " composer-content-trigger--open" : ""}`}
                    onClick={() => (contentMenuOpen ? setContentMenuOpen(false) : openContentMenu())}
                    disabled={disabled || readOnly || running}
                    aria-haspopup="menu"
                    aria-expanded={contentMenuOpen}
                    aria-label={t("composer.contentMenuTitle")}
                  >
                    <Plus size={17} strokeWidth={1.8} aria-hidden="true" />
                  </button>
                </Tooltip>
              </div>
            )}
            {!heroMode && (
              <div className="composer-meta__control composer-meta__control--intent">
                <Tooltip label={taskModeTooltipLabel} disabled={intentMenuOpen || intentMenuClosing || creationChrome}>
                  <button
                    ref={intentMenuAnchorRef}
                    type="button"
                    className={`composer-task-mode-trigger${intentMenuOpen || intentMenuClosing ? " composer-task-mode-trigger--open" : ""}`}
                    onClick={() => (intentMenuOpen || intentMenuClosing ? closeIntentMenu() : openIntentMenu())}
                    onMouseEnter={creationChrome ? onIntentHoverEnter : undefined}
                    onMouseLeave={creationChrome ? onIntentHoverLeave : undefined}
                    disabled={disabled || running}
                    aria-haspopup="menu"
                    aria-expanded={intentMenuOpen && !intentMenuClosing}
                    aria-label={taskModeTriggerLabel}
                    title={intentMenuOpen || intentMenuClosing || creationChrome ? undefined : taskModeTriggerLabel}
                  >
                    <TaskModeIcon size={14} aria-hidden="true" />
                    <span className="composer-task-mode-trigger__value">{t(taskModeShortKey)}</span>
                    <ChevronsUpDown size={11} aria-hidden="true" />
                  </button>
                </Tooltip>
              </div>
            )}
            {!heroMode && (
              <div className="composer-meta__control composer-meta__control--approval">
                {/* A pending tool approval disables the composer, but the approval
                    bar stays usable so mode changes remain possible mid-prompt;
                    the approval card explains that the pending request still needs
                    an explicit decision. */}
                <div
                  className="composer-modebar composer-modebar--approval"
                  data-mode={toolApprovalMode}
                  title={t("composer.accessMenuTitle", { shortcut: yoloComboLabel })}
                >
                  <span className="composer-modebar__thumb" aria-hidden="true" />
                  <button
                    type="button"
                    className={`composer-modebar__item composer-modebar__item--ask${toolApprovalMode === "ask" ? " composer-modebar__item--active" : ""}`}
                    onClick={() => chooseApprovalMode("ask")}
                    disabled={approvalBarDisabled}
                    aria-pressed={toolApprovalMode === "ask"}
                    title={t("composer.accessAskTitle")}
                  >
                    <Shield size={14} />
                    <span>{t("composer.modeAsk")}</span>
                  </button>
                  <button
                    type="button"
                    className={`composer-modebar__item composer-modebar__item--auto${toolApprovalMode === "auto" ? " composer-modebar__item--active" : ""}`}
                    onClick={() => chooseApprovalMode("auto")}
                    disabled={approvalBarDisabled}
                    aria-pressed={toolApprovalMode === "auto"}
                    title={t("composer.accessAutoTitle")}
                  >
                    <ShieldCheck size={14} />
                    <span>{t("composer.modeNormal")}</span>
                  </button>
                  <button
                    type="button"
                    className={`composer-modebar__item composer-modebar__item--yolo${toolApprovalMode === "yolo" ? " composer-modebar__item--active" : ""}`}
                    onClick={() => chooseApprovalMode("yolo")}
                    disabled={approvalBarDisabled}
                    aria-pressed={toolApprovalMode === "yolo"}
                    title={t("composer.accessYoloTitle", { shortcut: yoloComboLabel })}
                  >
                    <ShieldAlert size={14} />
                    <span>{t("composer.modeYolo")}</span>
                  </button>
                </div>
              </div>
            )}
            {!heroMode && (
              <div className="composer-meta__control composer-meta__control--floor">
                {/* Orthogonal to the intent menu: the floor only raises the
                    completion gates, so goal mode and delivery combine. */}
                <div
                  className="composer-modebar composer-modebar--floor"
                  data-floor={floorOn ? "delivery" : "standard"}
                  title={t("composer.qualityFloor")}
                >
                  <span className="composer-modebar__thumb" aria-hidden="true" />
                  <button
                    type="button"
                    className={`composer-modebar__item${floorOn ? "" : " composer-modebar__item--active"}`}
                    onClick={() => chooseQualityFloor("standard")}
                    disabled={approvalBarDisabled || !onSetQualityFloor}
                    aria-pressed={!floorOn}
                    title={t("composer.qualityFloor")}
                  >
                    <Equal size={14} />
                    <span>{t("composer.qualityFloorStandard")}</span>
                  </button>
                  <button
                    type="button"
                    className={`composer-modebar__item${floorOn ? " composer-modebar__item--active" : ""}`}
                    onClick={() => chooseQualityFloor("delivery")}
                    disabled={approvalBarDisabled || !onSetQualityFloor}
                    aria-pressed={floorOn}
                    title={t("composer.qualityFloorDeliveryTitle")}
                  >
                    <PackageCheck size={14} />
                    <span>{t("composer.qualityFloorDelivery")}</span>
                    {floorOn && floorInferred ? <span className="composer-modebar__inferred" /> : null}
                  </button>
                </div>
              </div>
            )}
            {!heroMode && <span className="composer-meta__divider" aria-hidden="true" />}
            <div className="composer-meta__control composer-meta__control--model">
              {/*
                Creation-only: showContextWindowRing is wired to sidebarCreation
                (desktopLayoutStyle === "creation") in App.tsx. The ring popover
                is portaled to <body> without an .app--creation prefix, so its
                styles look global but only ever apply in creation layout. If you
                ever surface this ring in another layout, its font sizes already
                scale via --font-scale (see .context-ring-popover in styles.css).
              */}
              {!heroMode && showContextWindowRing && (
                <ContextWindowRing
                  enabled={showContextWindowRing}
                  context={context}
                  tabId={tabId}
                  turnCost={turnCost}
                  turnRateBand={turnRateBand}
                  currency={currency}
                  cacheHitTokens={cacheHitTokens}
                  cacheMissTokens={cacheMissTokens}
                  balance={balance}
                />
              )}
              <Suspense fallback={<span className="modelsw__label">{modelLabel}</span>}><ModelSwitcher label={modelLabel} tabId={tabId} onPick={onSwitchModel} onManage={() => {
                useAppNavigationStore.getState().setSettingsFocus({ target: "model-access" });
                useAppNavigationStore.getState().setSettingsTarget("models");
              }} /></Suspense>
            </div>
            {!heroMode && hasEffort && (
              <div className="composer-meta__control composer-meta__control--effort">
                <EffortSwitcher effort={effort} disabled={running} onPick={onSetEffort} />
              </div>
            )}
            {!heroMode && hasEffort && (
              <div className="composer-meta__control composer-meta__control--more">
                <Tooltip label={compactEffortTitle} disabled={moreMenuOpen || moreMenuClosing}>
                  <button
                    ref={moreMenuAnchorRef}
                    type="button"
                    className={`composer-more-trigger composer-more-trigger--effort${currentEffort !== "auto" ? " composer-more-trigger--explicit" : ""}${moreMenuOpen || moreMenuClosing ? " composer-more-trigger--open" : ""}`}
                    onClick={() => (moreMenuOpen || moreMenuClosing ? closeMoreMenu() : openMoreMenu())}
                    disabled={disabled || running}
                    aria-haspopup="menu"
                    aria-expanded={moreMenuOpen && !moreMenuClosing}
                    aria-label={compactEffortTitle}
                    title={moreMenuOpen || moreMenuClosing ? undefined : compactEffortTitle}
                  >
                    <Gauge size={14} />
                    <span>{effortLabel(currentEffort)}</span>
                    <ChevronsUpDown size={11} />
                  </button>
                </Tooltip>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
