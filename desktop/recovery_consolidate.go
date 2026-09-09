package main

import (
	"errors"
	"log/slog"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/botruntime"
)

// errConsolidationMainNotCovered mirrors agent.ErrMainNotCoveredByWinner for
// the desktop: the fullest recovery copy does not contain the whole current
// main transcript, so the swap was refused to avoid hiding main-only turns.
var errConsolidationMainNotCovered = errors.New("the fullest recovery copy does not contain the current main transcript; nothing was merged")

// ConsolidateTopicRecoveryCopies resolves the topic's canonical transcript
// (the automatic open target) server-side and merges the recovery copies of
// that session. Context menus call this instead of the path-based variant so
// the action stays clickable even when the sidebar row predates the runtime
// projection that would carry a session path.
func (a *App) ConsolidateTopicRecoveryCopies(scope, workspaceRoot, topicID string) (agent.ConsolidationReport, error) {
	path := a.catalogSessionPathForTopic(scope, workspaceRoot, topicID)
	if strings.TrimSpace(path) == "" {
		return agent.ConsolidationReport{}, friendlySessionFileError(errors.New("could not resolve the main transcript of this topic"))
	}
	return a.consolidateSessionRecoveryCopies(path, false)
}

// ForceConsolidateTopicRecoveryCopies mirrors ForceConsolidateSessionRecoveryCopies
// for topic-resolved sessions.
func (a *App) ForceConsolidateTopicRecoveryCopies(scope, workspaceRoot, topicID string) (agent.ConsolidationReport, error) {
	path := a.catalogSessionPathForTopic(scope, workspaceRoot, topicID)
	if strings.TrimSpace(path) == "" {
		return agent.ConsolidationReport{}, friendlySessionFileError(errors.New("could not resolve the main transcript of this topic"))
	}
	return a.consolidateSessionRecoveryCopies(path, true)
}

// ConsolidateSessionRecoveryCopies is the desktop entry point behind the
// "merge recovery copies" session action. It promotes the fullest recovery
// copy onto the main session identity, archives the previous main as one
// recoverable trash entry, and folds fully covered copies into the same
// trash. A session that is open in an idle tab stays consolidatable: the
// tab notices the identity change through the load-older-history reload
// path (#9468/#9469). A running runtime keeps its lease and blocks the
// merge until it is stopped.
func (a *App) ConsolidateSessionRecoveryCopies(path string) (agent.ConsolidationReport, error) {
	return a.consolidateSessionRecoveryCopies(path, false)
}

// ForceConsolidateSessionRecoveryCopies runs the merge after an explicit user
// confirmation that the winner may replace a main transcript it does not
// fully cover (typical after a main-side compaction). The previous main is
// still archived whole under the recoverable trash.
func (a *App) ForceConsolidateSessionRecoveryCopies(path string) (agent.ConsolidationReport, error) {
	return a.consolidateSessionRecoveryCopies(path, true)
}

func (a *App) consolidateSessionRecoveryCopies(path string, force bool) (agent.ConsolidationReport, error) {
	dir := a.activeSessionDir()
	sessionPath, _, err := validateSessionPath(dir, path)
	if err != nil {
		var foundErr error
		if dir, sessionPath, foundErr = a.sessionDirForPath(path); foundErr != nil {
			return agent.ConsolidationReport{}, friendlySessionFileError(err)
		}
	}
	report, err := func() (agent.ConsolidationReport, error) {
		defer a.lockRuntimeMutation("consolidate-recovery-copies")()
		a.sessionRemovalMu.Lock()
		defer a.sessionRemovalMu.Unlock()
		report, err := agent.ConsolidateSessionRecoveryBranchesWithOptions(sessionPath, agent.ConsolidateOptions{Force: force})
		if err != nil {
			switch {
			case errors.Is(err, agent.ErrNoRecoveryBranches):
				return report, errNoRecoveryCopiesToConsolidate
			case errors.Is(err, agent.ErrSessionLeaseHeldForConsolidation), errors.Is(err, agent.ErrSessionLeaseHeld):
				return report, errSessionBusyElsewhere
			case errors.Is(err, agent.ErrMainNotCoveredByWinner):
				return report, errConsolidationMainNotCovered
			}
			return report, err
		}
		return report, nil
	}()
	if err != nil {
		return report, friendlySessionFileError(err)
	}
	if report.Promoted || len(report.Trashed) > 0 {
		if err := botruntime.ForgetAutoSessionMappingsForPath(sessionPath); err != nil {
			slog.Warn("desktop: failed to clear auto bot session mapping", "err", err)
		}
		for _, trashed := range report.Trashed {
			a.removeSessionCatalogPath(trashed, "recovery_copies_consolidated")
		}
		a.emitProjectTreeChangedForSessionDirs(dir)
		a.invalidatePromptHistoryCache()
	}
	return report, nil
}
