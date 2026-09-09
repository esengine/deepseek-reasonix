import { lazy, Suspense } from "react";
import { Transcript, type TranscriptProps } from "../components/Transcript";
import { SessionRecoveryBanner, SessionRecoveryPlaceholder } from "../components/SessionRecoveryBanner";
import { NoticePreviewPanel, noticePreviewMockEnabled } from "./NoticePreviewPanel";
import type { SidebarImConnection } from "../app-runtime/sidebarImProjection";
import type { TabMeta } from "../lib/types";
import type { State } from "../lib/useController";
import type { RemoteSessionApi } from "../lib/useRemoteSession";
import type { Translator } from "../lib/i18n";
import type { SessionAvailability } from "../lib/sessionAvailability";

const RemoteSessionSurface = lazy(() => import("../components/RemoteSessionSurface").then((module) => ({ default: module.RemoteSessionSurface })));
const SidebarImConnectionDetail = lazy(() => import("./SidebarImConnectionDetail").then((module) => ({ default: module.SidebarImConnectionDetail })));

export type ChatPaneTranscriptInput = {
  state: State;
  items: TranscriptProps["items"];
  tabId: TranscriptProps["tabId"];
  geometrySessionKey: TranscriptProps["geometrySessionKey"];
  footerHeight: TranscriptProps["footerHeight"];
  revealSignal: TranscriptProps["revealSignal"];
  invocationMetadata: TranscriptProps["invocationMetadata"];
  surfaceCommitToken: TranscriptProps["surfaceCommitToken"];
  liveStore: TranscriptProps["liveStore"];
  transcriptHydrating: boolean;
  navigationDataReady: boolean;
  readOnly: boolean;
  controllerReady: boolean;
  hydratePlaceholderActive: boolean;
  clearContextPending: boolean;
  creation: boolean;
  emptyHero?: boolean;
  availability: SessionAvailability;
  rewind: {
    stateActive: boolean;
    committing: boolean;
    signal: TranscriptProps["rewindSignal"];
  };
};

export type ChatPaneRegionProps = {
  transitioning: boolean;
  t: Translator;
  imDetail: {
    connection: SidebarImConnection;
    onClose: () => void;
    onOpenSettings: () => void;
    onManageAllowlist: (connectionId: string) => void;
    onOpenSession: (connection: SidebarImConnection) => void;
  } | null;
  remote: { tab: TabMeta; session: RemoteSessionApi } | undefined;
  transcript: ChatPaneTranscriptInput;
  onRetryHistory: () => Promise<unknown>;
  commands: {
    onPrompt: TranscriptProps["onPrompt"];
    onDeliveryContinue: TranscriptProps["onDeliveryContinue"];
    onAcceptDelivery: TranscriptProps["onAcceptDelivery"];
    onOpenChanges: TranscriptProps["onOpenChanges"];
    onOpenVerification: TranscriptProps["onOpenVerification"];
    onEditPrompt: TranscriptProps["onEditPrompt"];
    onRewind: TranscriptProps["onRewind"];
    onLoadOlderHistory: TranscriptProps["onLoadOlderHistory"];
    onSurfacePaintReady: TranscriptProps["onSurfacePaintReady"];
  };
};

/**
 * The chat-pane main surface: IM/bot detail, notice preview mock, remote
 * session surface or the local transcript with its navigation-transition
 * wrapper and history-load error. Pure prop-driven; all ownership stays in
 * the caller's owners.
 */
export function ChatPaneRegion(props: ChatPaneRegionProps) {
  const { transitioning, t, transcript, commands } = props;
  const { state, rewind } = transcript;
  const rewindDisabled = transcript.readOnly || !transcript.controllerReady || transcript.hydratePlaceholderActive
    || rewind.stateActive || rewind.committing || state.running
    || state.messageAction != null || state.approval != null || state.ask != null
    || transcript.clearContextPending || transitioning;
  const noticePreview = noticePreviewMockEnabled();
  if (props.remote && !(props.imDetail && !transitioning) && !noticePreview) {
    return <Suspense fallback={null}><RemoteSessionSurface tab={props.remote.tab} session={props.remote.session}
      surfaceCommitToken={transcript.surfaceCommitToken} onSurfacePaintReady={commands.onSurfacePaintReady} /></Suspense>;
  }
  const recoveringEmpty = !transitioning && transcript.availability.kind !== "ready" && transcript.items.length === 0
    && !state.live?.text && !state.live?.reasoning;
  return (
    <>
    {!transitioning && !props.imDetail && !noticePreview && <SessionRecoveryBanner key={transcript.tabId}
      availability={transcript.availability} onRetry={props.onRetryHistory} />}
    <main className="main">
      {props.imDetail && !transitioning ? (
        <SidebarImConnectionDetail
          connection={props.imDetail.connection}
          onClose={props.imDetail.onClose}
          onOpenSettings={props.imDetail.onOpenSettings}
          onManageAllowlist={() => props.imDetail!.onManageAllowlist(props.imDetail!.connection.connectionId)}
          onOpenSession={() => props.imDetail!.onOpenSession(props.imDetail!.connection)}
        />
      ) : noticePreview ? (
        <NoticePreviewPanel />
      ) : (
        <>
          <div className="transcript-navigation-surface" aria-busy={transitioning}>
            <div
              className="transcript-navigation-content"
              aria-hidden={transitioning || undefined}
              ref={(node) => {
                if (!node) return;
                (node as HTMLElement & { inert?: boolean }).inert = transitioning;
              }}
            >
              {recoveringEmpty ? <SessionRecoveryPlaceholder availability={transcript.availability} /> : <Transcript
                items={transcript.items}
                live={transitioning ? undefined : state.live}
                liveStore={transcript.liveStore}
                tabId={transcript.tabId}
                geometrySessionKey={transcript.geometrySessionKey}
                footerHeight={transcript.footerHeight}
                onPrompt={commands.onPrompt}
                onDeliveryContinue={commands.onDeliveryContinue}
                onAcceptDelivery={commands.onAcceptDelivery}
                onOpenChanges={commands.onOpenChanges}
                onOpenVerification={commands.onOpenVerification}
                onEditPrompt={commands.onEditPrompt}
                onRewind={commands.onRewind}
                checkpoints={state.checkpoints}
                actionPending={state.messageAction != null}
                rewindDisabled={rewindDisabled}
                running={state.running || rewind.committing}
                turnStartAt={state.turnStartAt}
                contentRevision={state.historyLayoutRevision}
                historyMutation={state.historyMutation}
                welcomeVariant={transcript.creation || transcript.emptyHero ? "creation" : "default"}
                creationMode={transcript.creation}
                actionHoverMenus={transcript.creation && !transcript.hydratePlaceholderActive && !transitioning}
                rewindSignal={rewind.signal}
                revealSignal={transcript.revealSignal}
                hydrating={transcript.transcriptHydrating || (transitioning && !transcript.navigationDataReady)}
                hasOlderHistory={!transitioning && state.historyHasOlder && !rewind.stateActive}
                stableHistoryPaging={state.transcriptProtocol === 1}
                historyStartTurn={state.historyStartTurn}
                historyTotalTurns={state.historyTotalTurns}
                loadingOlderHistory={state.historyOlderLoading}
                olderHistoryError={state.historyOlderError}
                onLoadOlderHistory={commands.onLoadOlderHistory}
                invocationMetadata={transcript.invocationMetadata}
                surfaceCommitToken={transcript.surfaceCommitToken}
                onSurfacePaintReady={commands.onSurfacePaintReady}
              />}
            </div>
            {transitioning ? (
              <div className="transcript-navigation-overlay" role="status" aria-live="polite">
                <span className="transcript-navigation-overlay__spinner" aria-hidden="true" />
                <span>{t("common.loading")}</span>
              </div>
            ) : null}
          </div>
        </>
      )}
    </main>
    </>
  );
}
