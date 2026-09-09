import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
  type CSSProperties,
} from "react";
import { ArrowDown, Loader2 } from "lucide-react";
import type { ControllerLiveStore, HistoryLoadTrigger, HistoryMutation, Item, LiveStream } from "../lib/useController";
import type { CheckpointMeta, WireCompletionSummary } from "../lib/types";
import type { InvocationMetadataMap } from "../lib/invocationDisplay";
import { useT } from "../lib/i18n";
import { acquireMarkdownWorkerClient, releaseMarkdownWorkerClient } from "../lib/markdownWorkerClient";
import { onSessionExperienceWillChange, useSessionExperience } from "../lib/sessionExperience";
import {
  buildTranscriptRowBlocks,
  buildTurnModels,
  EMPTY_FOLDS,
  foldMapWithReasoningOpen,
  foldMapWithToggle,
  foldSegmentStates,
  NO_LIVE,
  reconcileFoldEntries,
  type FoldMap,
  type ToolItem,
  type TranscriptLiveFlags,
} from "../lib/transcriptRows";
import { projectTranscriptTimeline, transcriptRenderMode } from "../lib/transcriptTimeline";
import {
  readTranscriptFoldOverrides,
  replaceTranscriptFoldOverrides,
  writeTranscriptFoldOverride,
} from "../lib/transcriptFoldOverrides";
import { useTranscriptCommand } from "../lib/useTranscriptCommand";
import { composeDomRef } from "../lib/composeDomRef";
import { useTranscriptKernel } from "../lib/useTranscriptKernel";
import { TranscriptHistoryRequest } from "../lib/transcriptHistoryRequest";
import type { TranscriptQuestionNavigatorHandle } from "./TranscriptQuestionNavigator";
import { useTranscriptQuestions } from "../lib/useTranscriptQuestionNavigation";
import { useTranscriptSelectableRows } from "../lib/useTranscriptSelectableRows";
import { useTranscriptSelectionRetention } from "../lib/useTranscriptSelectionRetention";
import { useCreationTranscriptScrollbar } from "../lib/useCreationTranscriptScrollbar";
import { hasTranscriptScrollableRange } from "../lib/transcriptScrollGeometry";
import { attachNestedScrollHandoff } from "../lib/nestedScrollHandoff";
import { useTranscriptEntranceAnimation } from "../lib/useEntranceAnimation";
import type { QuestionAnchor } from "../lib/transcriptGrouping";
import { transcriptSelectionStore } from "../lib/transcriptSelectionStore";
import { recordFrontendDiagnostic } from "../lib/frontendDiagnosticBridge";
import { InvocationMetadataContext } from "./Message";
import { LiveStreamContext } from "./LiveStreamContext";
import { MarkdownImageTabContext } from "./MarkdownImageContext";
import { TranscriptLayoutIntentProvider, TranscriptScrollWriteProvider } from "./TranscriptLayoutIntentContext";
import { TranscriptViewport, type TranscriptViewportHandle } from "./TranscriptViewport";
import { Welcome } from "./Welcome";
import { useTranscriptRowRenderer } from "./useTranscriptRowRenderer";

export { NoticeCard } from "./TranscriptCards";

const EMPTY_CHECKPOINTS: CheckpointMeta[] = [];
const EMPTY_INVOCATION_METADATA: InvocationMetadataMap = {};
const QUESTION_NAV_MIN_COUNT = 2;
const TranscriptQuestionNavigator = lazy(() => import("./TranscriptQuestionNavigator"));
const SHOW_FRONTEND_DIAGNOSTICS = typeof __BUILD_CHANNEL__ === "undefined"
  || __BUILD_CHANNEL__ === "test"
  || __BUILD_CHANNEL__ === "preview"
  || __BUILD_CHANNEL__ === "canary"
  || Boolean(import.meta.env?.DEV);
const FrontendDiagnosticsPanel = SHOW_FRONTEND_DIAGNOSTICS
  ? lazy(() => import("./FrontendDiagnosticsPanel"))
  : null;

export type TranscriptProps = {
  items: Item[];
  live?: LiveStream;
  liveStore?: ControllerLiveStore;
  tabId?: string;
  geometrySessionKey?: string;
  footerHeight?: number;
  onPrompt: (text: string) => void;
  onDeliveryContinue?: () => void;
  onAcceptDelivery?: () => void;
  onOpenChanges?: (summary?: WireCompletionSummary) => void;
  onOpenVerification?: (summary: WireCompletionSummary) => void;
  onEditPrompt?: (turn: number, displayText: string, submitText?: string) => boolean | void | Promise<boolean | void>;
  onRewind?: (turn: number, scope: string) => void;
  checkpoints?: CheckpointMeta[];
  actionPending?: boolean;
  rewindDisabled?: boolean;
  running?: boolean;
  questionNavigator?: boolean;
  welcomeVariant?: "default" | "creation";
  creationMode?: boolean;
  actionHoverMenus?: boolean;
  rewindSignal?: number;
  revealSignal?: number;
  hydrating?: boolean;
  hasOlderHistory?: boolean;
  stableHistoryPaging?: boolean;
  historyStartTurn?: number;
  historyTotalTurns?: number;
  loadingOlderHistory?: boolean;
  olderHistoryError?: string;
  onLoadOlderHistory?: (targetTurn?: number, trigger?: HistoryLoadTrigger) => boolean | Promise<boolean>;
  turnStartAt?: number;
  contentRevision?: number;
  invocationMetadata?: InvocationMetadataMap;
  historyMutation?: HistoryMutation;
  surfaceCommitToken?: string;
  onSurfacePaintReady?: (token: string, outcome: "ready" | "degraded") => void;
};

export function Transcript(props: TranscriptProps) {
  const {
    items, live: liveProp, liveStore, tabId, geometrySessionKey, footerHeight = 0,
    onPrompt, onDeliveryContinue, onAcceptDelivery, onOpenChanges, onOpenVerification,
    onEditPrompt, onRewind, checkpoints = EMPTY_CHECKPOINTS, actionPending = false,
    rewindDisabled = false, running = false, questionNavigator = true,
    welcomeVariant = "default", creationMode = false, actionHoverMenus = false,
    rewindSignal = 0, revealSignal = 0, hydrating = false, hasOlderHistory = false, stableHistoryPaging = false,
    historyStartTurn = 0, historyTotalTurns = 0, loadingOlderHistory = false,
    olderHistoryError, onLoadOlderHistory, turnStartAt, contentRevision = 0,
    invocationMetadata = EMPTY_INVOCATION_METADATA, historyMutation,
    surfaceCommitToken, onSurfacePaintReady,
  } = props;
  const t = useT();
  const subscribeLive = useCallback((listener: () => void) => liveStore?.subscribe(tabId, listener) ?? (() => {}), [liveStore, tabId]);
  const getLiveSnapshot = useCallback(() => liveStore?.getSnapshot(tabId) ?? liveProp, [liveProp, liveStore, tabId]);
  const live = useSyncExternalStore(subscribeLive, getLiveSnapshot, getLiveSnapshot);
  const resolvedSessionKey = geometrySessionKey || `tab:${tabId ?? "preview"}`;
  const surfaceKey = `${resolvedSessionKey}:${revealSignal}`;
  const entranceRef = useTranscriptEntranceAnimation<HTMLDivElement>(tabId, revealSignal, items);
  const viewportRef = useRef<TranscriptViewportHandle>(null);
  const committedSurfaceRef = useRef("");
  const experience = useSessionExperience();
  const liveFlags = useMemo<TranscriptLiveFlags>(() => live?.id ? {
    id: live.id,
    hasAnswerText: Boolean(live.text.trim()),
    hasReasoning: Boolean(live.reasoning),
    reasoningComplete: live.reasoningComplete,
  } : NO_LIVE, [live?.id, live?.reasoning, live?.reasoningComplete, live?.text]);
  const turnModels = useMemo(() => buildTurnModels(items, liveFlags, running, false), [items, liveFlags, running]);
  // Capture stable commands, never the per-render hook result: a memoized
  // callback holding that result can chain older render/selection contexts.
  const { kernel: transcriptKernel, setScroller: setKernelScroller, snapshot,
    beginGesture, beginStructural, scrollElement, scrollToBottom, safeMode, scrollRef, setScrollMode, writeOffset, jumpToBlock, onScroll, endGesture, commitViewportGeometry, onWheelCapture, isAtBottom, intent, onTouchStartCapture, onTouchEndCapture, onKeyDownCapture, onPointerDownCapture, beginAnchorRestore,
  } = useTranscriptKernel({
    sessionKey: surfaceKey,
    geometryRevision: `${contentRevision}:${footerHeight}:${experience}:${historyMutation?.seq ?? 0}`,
  });
  const [
    questions, loadedByTurn, totalQuestions, activeQuestion, setActiveQuestion,
    scheduleActiveQuestionSync, turnForUser, lastTurn,
  ] = useTranscriptQuestions(items, historyStartTurn, historyTotalTurns, scrollElement, scrollToBottom);

  const segmentStates = useMemo(() => foldSegmentStates(turnModels, experience === "deep"), [experience, turnModels]);
  const [folds, setFolds] = useState<FoldMap>(EMPTY_FOLDS);
  const experienceRef = useRef(experience);
  const foldSurfaceRef = useRef("");
  useLayoutEffect(() => {
    if (foldSurfaceRef.current === resolvedSessionKey) return;
    foldSurfaceRef.current = resolvedSessionKey;
    setFolds(readTranscriptFoldOverrides(resolvedSessionKey, segmentStates));
  }, [resolvedSessionKey, segmentStates]);
  useEffect(() => onSessionExperienceWillChange(() => {
    beginStructural("display-change");
  }), [beginStructural]);
  useEffect(() => {
    const preferenceChanged = experienceRef.current !== experience;
    experienceRef.current = experience;
    setFolds((previous) => {
      const next = reconcileFoldEntries(previous, segmentStates, experience, preferenceChanged);
      if (next) replaceTranscriptFoldOverrides(resolvedSessionKey, next);
      return next ?? previous;
    });
  }, [experience, resolvedSessionKey, segmentStates]);

  const subcallsByParent = useMemo(() => {
    const grouped = new Map<string, ToolItem[]>();
    for (const item of items) {
      if (item.kind !== "tool" || !item.parentId) continue;
      const children = grouped.get(item.parentId) ?? [];
      children.push(item);
      grouped.set(item.parentId, children);
    }
    return grouped;
  }, [items]);
  const checkpointsByTurn = useMemo(() => new Map(checkpoints.map((checkpoint) => [checkpoint.turn, checkpoint])), [checkpoints]);
  const blocks = useMemo(() => buildTranscriptRowBlocks(turnModels, {
    folds,
    sessionExperience: experience,
    hasOlderHistory: false,
    creationMode,
    turnForUser,
    hasCheckpointForTurn: (turn) => checkpointsByTurn.has(turn),
    subcallsByParent,
  }), [checkpointsByTurn, creationMode, experience, folds, subcallsByParent, turnForUser, turnModels]);
  const projection = useMemo(() => projectTranscriptTimeline(blocks, hasOlderHistory), [blocks, hasOlderHistory]);
  const renderMode = transcriptRenderMode(projection.completedBlocks.length, safeMode);
  const allRows = useMemo(() => blocks.flatMap((block) => block.rows), [blocks]);
  const empty = items.length === 0;
  const rowIndexByKey = useMemo(() => new Map(allRows.map((row, index) => [String(row.key), index])), [allRows]);
  const [selectableRows, liveSelectableRows] = useTranscriptSelectableRows(allRows, live);
  const cancelStreamingScroll = useCallback(() => beginGesture("selection"), [beginGesture]);
  const { clear: clearSelection, onPointerDownCapture: onSelectionPointerDown, endStaleGesture } = useTranscriptSelectionRetention({
    tabId,
    revealSignal,
    rowIndexByKey,
    selectableRows,
    selectableRowOverrides: liveSelectableRows,
    scrollRef: scrollRef,
    setScrollMode: setScrollMode,
    writeOffset: writeOffset,
    cancelStreamingScroll,
  });

  const handleFoldToggle = useTranscriptCommand((segmentKey: string, open: boolean) => {
    beginStructural("display-change");
    setFolds((previous) => {
      const next = foldMapWithToggle(previous, segmentKey, open);
      const entry = next.get(segmentKey);
      if (entry) writeTranscriptFoldOverride(resolvedSessionKey, segmentKey, entry);
      return next;
    });
  });
  const handleReasoningManualOpen = useTranscriptCommand((segmentKey: string) => {
    beginStructural("display-change");
    const active = segmentStates.find((segment) => segment.key === segmentKey)?.hasRunningWork ?? false;
    setFolds((previous) => {
      const next = foldMapWithReasoningOpen(previous, segmentKey, active);
      const entry = next.get(segmentKey);
      if (entry) writeTranscriptFoldOverride(resolvedSessionKey, segmentKey, entry);
      return next;
    });
  });
  const renderRow = useTranscriptRowRenderer({
    tabId, checkpoints, subcallsByParent, creationMode, running, actionPending,
    rewindDisabled, actionHoverMenus, turnStartAt, lastTurn,
    onFoldToggle: handleFoldToggle, onReasoningManualOpen: handleReasoningManualOpen,
    onPrompt, onDeliveryContinue, onAcceptDelivery, onOpenChanges, onOpenVerification,
    onEditPrompt, onRewind,
  });

  const jumpToLoadedQuestion = useTranscriptCommand((question: QuestionAnchor) => {
    const block = blocks.find((candidate) => candidate.questionAnchor === `u:${question.id}`);
    if (!block) return false;
    document.getSelection()?.removeAllRanges();
    clearSelection("question-navigation");
    setActiveQuestion(question.turn);
    viewportRef.current?.mountBlock(block.key);
    return jumpToBlock(block.key);
  });
  const questionNavigatorRef = useRef<TranscriptQuestionNavigatorHandle>(null);
  const history = useMemo(() => new TranscriptHistoryRequest(transcriptKernel), [transcriptKernel]);
  const requestOlder = useTranscriptCommand((turn?: number, trigger: HistoryLoadTrigger = "viewport-user") => {
    if (!onLoadOlderHistory || !hasOlderHistory || loadingOlderHistory || (running && !stableHistoryPaging)) return Promise.resolve(false);
    if (trigger !== "question-jump" && trigger !== "retry") beginStructural("prepend");
    return history.load(() => onLoadOlderHistory(turn, trigger));
  });
  const retry = useTranscriptCommand(() => {
    if (questionNavigatorRef.current) questionNavigatorRef.current.retry();
    else void requestOlder(undefined, "retry");
  });
  useEffect(() => {
    if (rewindSignal <= 0) return;
    const last = questions[questions.length - 1];
    if (last) jumpToLoadedQuestion(last);
  }, [jumpToLoadedQuestion, questions, rewindSignal]);

  const handleScroll = useTranscriptCommand(() => {
    const towardHistory = onScroll();
    if (towardHistory === null) return;
    scheduleActiveQuestionSync();
    const element = scrollRef.current;
    if (towardHistory && element && element.scrollTop <= 64) void requestOlder(undefined, "viewport-user");
  });
  const {
    state: creationScrollbar,
    handleScroll: handleCreationScroll,
    onThumbPointerDown: handleCreationScrollbarThumbPointerDown,
    onRailPointerDown: handleCreationScrollbarRailPointerDown,
  } = useCreationTranscriptScrollbar({
    enabled: creationMode,
    contentRevision,
    scrollRef: scrollRef,
    onScroll: handleScroll,
    setScrollMode: setScrollMode,
    writeOffset: writeOffset,
    finishProgrammaticScroll: endGesture,
  });

  const setScroller = useMemo(() => composeDomRef(setKernelScroller, entranceRef), [setKernelScroller, entranceRef]);
  const previousFooterHeight = useRef(footerHeight);
  useLayoutEffect(() => {
    if (previousFooterHeight.current === footerHeight) return;
    previousFooterHeight.current = footerHeight;
    beginStructural("composer-resize");
    commitViewportGeometry();
  }, [footerHeight, beginStructural, commitViewportGeometry]);

  useEffect(() => {
    acquireMarkdownWorkerClient();
    return () => releaseMarkdownWorkerClient();
  }, []);
  useEffect(() => {
    const parent = scrollElement;
    if (!parent) return;
    return attachNestedScrollHandoff({
      parent,
      onParentScrollIntent: () => onWheelCapture(),
      writeParentOffset: (top) => writeOffset("nested-scroll", top),
    }).detach;
  }, [onWheelCapture, scrollElement, writeOffset]);
  useEffect(() => {
    recordFrontendDiagnostic("transcript", "transcript.surface", {
      generation: transcriptKernel.generation,
      completedBlocks: projection.completedBlocks.length,
      renderMode,
    });
  }, [projection.completedBlocks.length, renderMode, surfaceKey, transcriptKernel.generation]);
  useEffect(() => {
    if (!surfaceCommitToken || !onSurfacePaintReady || hydrating) return;
    const commitKey = `${transcriptKernel.generation}:${surfaceCommitToken}`;
    if (committedSurfaceRef.current === commitKey) return;
    return transcriptKernel.afterCurrentGenerationPaint(() => {
      const geometry = snapshot();
      if (!geometry || (!empty && geometry.visibleBlocks.length === 0)) return;
      if (committedSurfaceRef.current === commitKey) return;
      committedSurfaceRef.current = commitKey;
      onSurfacePaintReady(surfaceCommitToken, safeMode ? "degraded" : "ready");
    });
  }, [empty, hydrating, safeMode, snapshot, onSurfacePaintReady, projection, surfaceCommitToken, transcriptKernel]);
  const autoFillRef = useRef({ surface: "", pages: 0 });
  useEffect(() => {
    if (autoFillRef.current.surface !== surfaceKey) autoFillRef.current = { surface: surfaceKey, pages: 0 };
    if (hydrating || !hasOlderHistory || loadingOlderHistory || olderHistoryError || (running && !stableHistoryPaging) || autoFillRef.current.pages >= 3) return;
    return transcriptKernel.afterCurrentGenerationPaint(() => {
      const geometry = snapshot();
      if (!geometry || geometry.clientHeight <= 0 || geometry.scrollHeight > geometry.clientHeight + 4) return;
      autoFillRef.current.pages += 1;
      void requestOlder(undefined, "auto-fill");
    });
  }, [hasOlderHistory, hydrating, snapshot, loadingOlderHistory, olderHistoryError, projection.completedBlocks.length, requestOlder, running, stableHistoryPaging, surfaceKey, transcriptKernel]);

  const showQuestionNav = questionNavigator && totalQuestions >= QUESTION_NAV_MIN_COUNT;
  const selectionSnapshot = useSyncExternalStore(transcriptSelectionStore.subscribe, transcriptSelectionStore.getSnapshot, transcriptSelectionStore.getSnapshot);
  const protectedBlockKeys = useMemo(() => {
    const keys = new Set<string>();
    if (transcriptKernel.anchor.kind === "block") keys.add(transcriptKernel.anchor.blockKey);
    const endpoints = selectionSnapshot.mode.startsWith("logical")
      ? [selectionSnapshot.anchor?.rowKey, selectionSnapshot.focus?.rowKey]
      : [];
    for (const block of blocks) {
      if (block.rows.some((row) => endpoints.includes(row.key))) keys.add(block.key);
    }
    return keys;
  }, [blocks, selectionSnapshot, transcriptKernel.anchor]);
  const jumpBottomVisible = Boolean(
    !isAtBottom
      && scrollElement
      && hasTranscriptScrollableRange(scrollElement),
  );

  return (
    <InvocationMetadataContext.Provider value={invocationMetadata}>
    <MarkdownImageTabContext.Provider value={tabId ?? ""}>
    <TranscriptLayoutIntentProvider value={() => { beginStructural("display-change"); }}>
    <TranscriptScrollWriteProvider value={writeOffset}>
      <div className="transcript-shell" aria-busy={loadingOlderHistory || undefined} data-protected-blocks={protectedBlockKeys.size}>
        {empty ? (
          <div className={`transcript transcript--empty${creationMode ? " transcript--creation-scrollbar" : ""}`} ref={setScroller} aria-busy={hydrating || undefined}>
            {hydrating ? <div className="transcript__loading" role="status" aria-live="polite"><Loader2 className="transcript__loading-icon" aria-hidden="true" /><span>{t("common.loading")}</span></div>
              : <Welcome onPrompt={onPrompt} variant={welcomeVariant} />}
          </div>
        ) : (
          <LiveStreamContext.Provider value={live}>
            <div
              ref={setScroller}
              className={`transcript${creationMode ? " transcript--creation-scrollbar" : ""}${creationMode && creationScrollbar.hot ? " transcript--scrollbar-hot" : ""}`}
              data-transcript-hydrating={hydrating ? "true" : "false"}
              data-transcript-generation={transcriptKernel.generation}
              data-transcript-intent={intent}
              data-transcript-row-count={allRows.length}
              data-transcript-block-count={blocks.length}
              data-scroll-mode={selectionSnapshot.mode !== "none" ? "selection" : intent === "tail" ? "tail-follow" : "manual"}
              onScroll={creationMode ? handleCreationScroll : handleScroll}
              onWheelCapture={() => onWheelCapture()}
              onTouchStartCapture={() => onTouchStartCapture()}
              onTouchEndCapture={() => onTouchEndCapture()}
              onTouchCancelCapture={() => onTouchEndCapture()}
              onKeyDownCapture={onKeyDownCapture}
              onPointerDownCapture={(event) => {
                onPointerDownCapture(event);
                onSelectionPointerDown(event);
              }}
              onMouseDownCapture={onPointerDownCapture}
            >
              <TranscriptViewport
                key={surfaceKey}
                ref={viewportRef}
                projection={projection}
                mode={renderMode}
                tabId={tabId}
                scrollElement={scrollElement}
                renderRow={renderRow}
                loadingOlderHistory={loadingOlderHistory}
                olderHistoryError={olderHistoryError}
                onRetryOlderHistory={retry}
                onGeometryWillChange={beginAnchorRestore}
                onGeometryChange={commitViewportGeometry}
                kernel={transcriptKernel}
                protectedBlockKeys={protectedBlockKeys}
                running={running}
                turnStartAt={turnStartAt}
              />
            </div>
          </LiveStreamContext.Provider>
        )}
        {creationMode && creationScrollbar.visible && <div className={`transcript__scrollbar${creationScrollbar.hot ? " transcript__scrollbar--hot" : ""}`} onPointerDown={handleCreationScrollbarRailPointerDown} aria-hidden="true">
          <div className="transcript__scrollbar-thumb" style={{ top: creationScrollbar.thumbTop, height: creationScrollbar.thumbHeight } as CSSProperties} onPointerDown={handleCreationScrollbarThumbPointerDown} />
        </div>}
        {!empty && showQuestionNav && <Suspense fallback={null}><TranscriptQuestionNavigator ref={questionNavigatorRef} kernel={transcriptKernel}
          requestOlder={requestOlder} loadingOlderHistory={loadingOlderHistory} running={running && !stableHistoryPaging} loadedByTurn={loadedByTurn}
          jump={jumpToLoadedQuestion} questions={questions} totalQuestions={totalQuestions} activeTurn={activeQuestion} /></Suspense>}
        {!empty && <button type="button" className="transcript__jump-bottom" hidden={!jumpBottomVisible} onClick={() => { endStaleGesture(); scrollToBottom(); }} aria-label={t("transcript.jumpToBottom")} title={t("transcript.jumpToBottom")}><ArrowDown size={18} strokeWidth={2.2} aria-hidden="true" /></button>}
        {FrontendDiagnosticsPanel && <Suspense fallback={null}><FrontendDiagnosticsPanel scrollElement={scrollElement} totalRows={allRows.length} /></Suspense>}
      </div>
    </TranscriptScrollWriteProvider>
    </TranscriptLayoutIntentProvider>
    </MarkdownImageTabContext.Provider>
    </InvocationMetadataContext.Provider>
  );
}
