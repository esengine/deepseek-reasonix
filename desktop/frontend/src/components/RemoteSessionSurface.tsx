import { useEffect, useState } from "react";
import { app } from "../lib/bridge";
import { useRemoteNavigationCommand } from "../lib/remoteNavigationCommands";
import { Transcript, type TranscriptProps } from "./Transcript";
import { AskCard } from "./AskCard";
import { ApprovalModal } from "./ApprovalModal";
import { ExtensionFormDialog } from "./ExtensionFormDialog";
import { SessionRecoveryBanner, SessionRecoveryPlaceholder } from "./SessionRecoveryBanner";
import { projectSessionAvailability } from "../lib/sessionAvailability";
import type { RemoteSessionApi } from "../lib/useRemoteSession";
export { hydrateRemoteTelemetry, loadRemoteStatusSnapshot } from "../lib/remoteTelemetry";
import type { TabMeta, WireApproval, WireAsk } from "../lib/types";
import { useT } from "../lib/i18n";

/**
 * RemoteSessionSurface renders the active remote tab's content area with
 * the SAME Transcript component local tabs use — the session hook feeds the
 * shared reducer with serve frames, so items, live streaming, approvals, and
 * asks arrive in the local shapes. Only the connection state machine and
 * the approval/ask cards are remote-specific; the composer lives in the
 * app shell, shared with local tabs.
 */
export function RemoteSessionSurface({ tab, session, surfaceCommitToken, onSurfacePaintReady }: {
  tab: TabMeta; session: RemoteSessionApi;
} & Pick<TranscriptProps, "surfaceCommitToken" | "onSurfacePaintReady">) {
  const navigateRemote = useRemoteNavigationCommand();
  const t = useT();
  const availability = projectSessionAvailability({ remote: session });
  const ready = availability.kind === "ready";
  const hasContent = session.transcript.items.length > 0 || Boolean(session.transcript.live?.text || session.transcript.live?.reasoning);
  const approval = session.transcript.approval as WireApproval | undefined;
  const ask = session.transcript.ask as WireAsk | undefined;
  const extensionForm = session.transcript.extensionForm;
  const [actionError, setActionError] = useState("");
  const [extensionFormBusy, setExtensionFormBusy] = useState(false);
  useEffect(() => { setActionError(""); setExtensionFormBusy(false); }, [session.state, tab.id]);
  const runAction = async (action: () => Promise<unknown>, propagate = false): Promise<void> => {
    setActionError("");
    try {
      await action();
    } catch (error) {
      setActionError(error instanceof Error ? error.message : String(error));
      if (propagate) throw error;
    }
  };
  const submitExtensionForm = (values: Record<string, unknown>) => {
    if (!extensionForm || extensionFormBusy) return;
    setExtensionFormBusy(true);
    runAction(() => app.SubmitRemoteTabExtensionForm(tab.id, extensionForm.pluginId, extensionForm.surfaceId, values)
      .then(() => session.clearExtensionForm(extensionForm.pluginId, extensionForm.surfaceId))
      .finally(() => setExtensionFormBusy(false)));
  };
  if (!tab.remote) return null;

  return (
    <>
    <SessionRecoveryBanner key={`${tab.id}:${session.surfaceGeneration}`} availability={availability} onRetry={async () => {
      if (availability.source === "history") { await session.retryHydration(); return; }
      // No new-session target: preserve the parked session when reconnecting.
      const outcome = await navigateRemote(tab.remote!, {});
      if (outcome.status === "failed") throw outcome.error;
    }} />
    <main className="main">
    <div className="remote-surface remote-surface--ready">
      {session.hydrated && session.syncMode === "legacy" ? <div className="remote-surface__detail" role="status">{t("remote.legacyTranscriptSync")}</div> : null}
      {!ready && !hasContent ? <SessionRecoveryPlaceholder availability={availability} /> : <Transcript
        items={session.transcript.items}
        live={session.transcript.live}
        tabId={tab.id}
        revealSignal={session.surfaceGeneration}
        hydrating={!session.hydrated && !hasContent}
        surfaceCommitToken={surfaceCommitToken}
        onSurfacePaintReady={onSurfacePaintReady}
        running={session.transcript.running}
        hasOlderHistory={session.transcript.historyHasOlder}
        stableHistoryPaging={session.syncMode === "snapshot"}
        historyStartTurn={session.transcript.historyStartTurn}
        historyTotalTurns={session.transcript.historyTotalTurns}
        loadingOlderHistory={session.transcript.historyOlderLoading}
        olderHistoryError={session.transcript.historyOlderError}
        onLoadOlderHistory={session.loadOlderHistory}
        contentRevision={session.transcript.historyLayoutRevision}
        historyMutation={session.transcript.historyMutation}
        checkpoints={session.transcript.checkpoints}
        onPrompt={(prompt) => runAction(() => session.submit(prompt))}
        onRewind={(turn, scope) => runAction(() => session.rewind(turn, scope))}
        rewindDisabled={session.running || !ready}
      />}

      {ready && approval ? (
        <div className="remote-surface__approval">
          <ApprovalModal
            key={`${tab.id}:${approval.id}`}
            approval={approval}
            cwd={tab.cwd}
            tabId={tab.id}
            toolApprovalMode={session.composerProfile?.toolApprovalMode}
            onAnswer={(allow, sessionScope, persist) => runAction(() => approval.tool === "exit_plan_mode"
              ? session.resolvePlanDecision(approval.id, allow ? "start_execution" : "revise_plan")
              : session.approve(
                  approval.id,
                  allow ? (persist ? "persist" : sessionScope ? "session" : "allow") : "deny",
                ))}
            onRevisePlan={(text) => runAction(() => session.resolvePlanDecision(approval.id, "revise_plan", text))}
            onExitPlan={() => runAction(() => session.resolvePlanDecision(approval.id, "exit_plan"))}
            onStop={() => runAction(session.cancelTurn)}
          />
        </div>
      ) : null}

      {ready && ask?.questions?.length ? (
        <AskCard
          key={`${tab.id}:${ask.id}`}
          ask={ask}
          draftScope={tab.id}
          onAnswer={(id, answers) => runAction(() => session.answer(id, answers.map((answer) => ({
            QuestionID: answer.questionId,
            Selected: answer.selected,
          }))), true)}
          onDismiss={() => runAction(() => session.answer(ask.id, []), true)}
          onStop={() => runAction(() => session.cancelTurn())}
        />
      ) : null}
      {ready && extensionForm ? (
        <ExtensionFormDialog
          key={`${tab.id}:${extensionForm.pluginId}:${extensionForm.surfaceId}`}
          surface={extensionForm}
          busy={extensionFormBusy}
          onSubmit={submitExtensionForm}
          onCancel={() => submitExtensionForm({ cancelled: true })}
        />
      ) : null}
      {ready && (actionError || session.promptError || session.error) ? (
        <div className="remote-surface__detail" role="alert">{actionError || session.promptError || session.error}</div>
      ) : null}
    </div>
    </main>
    </>
  );
}
