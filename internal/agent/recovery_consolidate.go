package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"reasonix/internal/store"
)

// Recovery-copy consolidation: the manual entry point behind the desktop
// "merge recovery copies" session action. Long sessions touched by a stale
// runtime can fork repeatedly, leaving several *-recovery-* transcripts whose
// identity drifts from the main file — the root cause behind the permanent
// "earlier conversation could not be loaded" failures (#9468/#9470).
//
// Consolidation picks the fullest loadable copy as the canonical transcript,
// swaps it into the main identity (the previous main is archived whole under
// the recoverable .trash), and folds every fully covered loser into the same
// trash. Copies that still hold unique content are preserved untouched and
// reported, so the operation can never destroy the only copy of a turn.

var (
	// ErrNoRecoveryBranches means the session has no recovery copies to merge.
	ErrNoRecoveryBranches = errors.New("session has no recovery copies to consolidate")
	// ErrSessionLeaseHeldForConsolidation means a live runtime still holds the
	// session; consolidation needs the transcript at rest.
	ErrSessionLeaseHeldForConsolidation = errors.New("session is open in a running runtime; close it before consolidating recovery copies")
	// ErrMainNotCoveredByWinner means the fullest copy does not contain the
	// whole current main transcript. Swapping anyway would push main-only
	// turns into the trash archive, so the caller is asked to decide first.
	ErrMainNotCoveredByWinner = errors.New("fullest recovery copy does not contain the current main transcript; refusing the swap to avoid data loss")
)

// RecoveryBranchCandidate is one member of a session's recovery lineage as
// seen by a cold, conservative load.
type RecoveryBranchCandidate struct {
	Path         string
	MessageCount int
	Revision     int64
	UpdatedAt    time.Time
	Loadable     bool
	IsMain       bool
}

// ConsolidationReport summarizes one consolidation run for the UI.
type ConsolidationReport struct {
	MainPath           string
	WinnerPath         string // "" when the main transcript already was the winner
	Promoted           bool
	NormalizedMain     bool // an older-format main was rewritten in place first
	// BlockedByDivergence reports that the fullest copy and the main
	// transcript each hold turns the other lacks (typical after a main-side
	// compaction). Nothing was merged; the caller may retry with Force.
	BlockedByDivergence bool
	MainMessageCount    int
	WinnerMessageCount  int
	Trashed            []string
	SkippedNotCovered  []string
	SkippedUnloadable  []string
}

// validateConsolidationTarget rejects paths that cannot be a consolidation
// target: non-transcripts, event logs, and recovery copies themselves.
func validateConsolidationTarget(mainPath string) error {
	mainPath = filepath.Clean(strings.TrimSpace(mainPath))
	if !strings.HasSuffix(mainPath, ".jsonl") || strings.HasSuffix(mainPath, ".events.jsonl") {
		return fmt.Errorf("consolidation targets a session transcript, got %s", mainPath)
	}
	if strings.Contains(filepath.Base(mainPath), "-recovery-") {
		return fmt.Errorf("consolidation targets the main transcript, not a recovery copy: %s", mainPath)
	}
	return nil
}

// normalizeTranscriptInPlaceIfDirty rewrites an older-format transcript in
// place with its normalized view — exactly the rewrite the session's next
// successful save would perform. Returns whether a rewrite happened. Load
// failures other than "needs normalization" are returned untouched.
func normalizeTranscriptInPlaceIfDirty(path string) (bool, error) {
	s, err := LoadSession(path)
	if err != nil || s == nil {
		return false, err
	}
	if !s.normalizedDirty {
		return false, nil
	}
	if err := s.SaveRewrite(path); err != nil {
		return false, err
	}
	return true, nil
}

// recoveryCopiesForMain lists the recovery copies that belong to mainPath by
// the stable <stem>-recovery-* naming convention in the same directory.
func recoveryCopiesForMain(mainPath string) ([]string, error) {
	dir := filepath.Dir(mainPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	stem := strings.TrimSuffix(filepath.Base(mainPath), ".jsonl")
	prefix := stem + "-recovery-"
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") || strings.HasSuffix(e.Name(), ".events.jsonl") {
			continue
		}
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if !IsVisibleSession(path) {
			continue
		}
		out = append(out, path)
	}
	sort.Strings(out)
	return out, nil
}

// recoveryConsolidationCandidate cold-loads one transcript for lineage
// analysis. Damaged or unnormalizable files are reported as unloadable rather
// than guessed at.
func recoveryConsolidationCandidate(path string, isMain bool) (RecoveryBranchCandidate, bool) {
	snap, ok := LoadSessionContentSnapshot(path)
	if !ok {
		return RecoveryBranchCandidate{Path: path, IsMain: isMain, Loadable: false}, false
	}
	meta, _, err := LoadBranchMeta(path)
	if err != nil {
		meta = BranchMeta{}
	}
	return RecoveryBranchCandidate{
		Path:         path,
		MessageCount: snap.Len(),
		Revision:     meta.Revision,
		UpdatedAt:    meta.UpdatedAt,
		Loadable:     true,
		IsMain:       isMain,
	}, true
}

// consolidationCandidateBeats reports whether a is a better canonical than b:
// most messages first, then the highest revision, then the newest update.
func consolidationCandidateBeats(a, b RecoveryBranchCandidate) bool {
	if a.MessageCount != b.MessageCount {
		return a.MessageCount > b.MessageCount
	}
	if a.Revision != b.Revision {
		return a.Revision > b.Revision
	}
	return a.UpdatedAt.After(b.UpdatedAt)
}

// ListSessionRecoveryBranches enumerates the recovery copies of a session
// with their cold-load stats. UI callers use it for pre-flight checks; it
// never mutates anything.
func ListSessionRecoveryBranches(mainPath string) ([]RecoveryBranchCandidate, error) {
	mainPath = filepath.Clean(strings.TrimSpace(mainPath))
	if err := validateConsolidationTarget(mainPath); err != nil {
		return nil, err
	}
	copies, err := recoveryCopiesForMain(mainPath)
	if err != nil {
		return nil, err
	}
	out := make([]RecoveryBranchCandidate, 0, len(copies)+1)
	if main, ok := recoveryConsolidationCandidate(mainPath, true); ok {
		out = append(out, main)
	} else {
		out = append(out, RecoveryBranchCandidate{Path: mainPath, IsMain: true, Loadable: false})
	}
	for _, copy := range copies {
		if cand, ok := recoveryConsolidationCandidate(copy, false); ok {
			out = append(out, cand)
		} else {
			out = append(out, RecoveryBranchCandidate{Path: copy, IsMain: false, Loadable: false})
		}
	}
	return out, nil
}

// ConsolidateOptions tunes one consolidation run.
type ConsolidateOptions struct {
	// Force lets a winner that does NOT cover the current main transcript
	// still be promoted. The previous main is archived whole under the
	// recoverable .trash, so the user can always roll back; nothing is
	// hard-deleted. Meant for an explicit user confirmation after the
	// engine reported BlockedByDivergence.
	Force bool
}

// ConsolidateSessionRecoveryBranches merges the recovery copies of mainPath
// with default (conservative) options.
func ConsolidateSessionRecoveryBranches(mainPath string) (ConsolidationReport, error) {
	return ConsolidateSessionRecoveryBranchesWithOptions(mainPath, ConsolidateOptions{})
}

// ConsolidateSessionRecoveryBranchesWithOptions merges the recovery copies of
// mainPath: the fullest loadable copy becomes the canonical main transcript,
// the previous main is archived whole under the recoverable .trash, and every
// copy fully covered by the winner is folded into the same trash. Copies with
// unique content are preserved and reported. Nothing is ever hard-deleted.
func ConsolidateSessionRecoveryBranchesWithOptions(mainPath string, opts ConsolidateOptions) (ConsolidationReport, error) {
	mainPath = filepath.Clean(strings.TrimSpace(mainPath))
	report := ConsolidationReport{MainPath: mainPath}
	if err := validateConsolidationTarget(mainPath); err != nil {
		return report, err
	}
	if SessionLeaseHeld(mainPath) {
		return report, ErrSessionLeaseHeldForConsolidation
	}
	copies, err := recoveryCopiesForMain(mainPath)
	if err != nil {
		return report, err
	}
	if len(copies) == 0 {
		return report, ErrNoRecoveryBranches
	}

	mainCand, mainOK := recoveryConsolidationCandidate(mainPath, true)
	if !mainOK {
		normalized, normErr := normalizeTranscriptInPlaceIfDirty(mainPath)
		if normErr != nil {
			return report, fmt.Errorf("could not normalize the main transcript before consolidating %s: %w", mainPath, normErr)
		}
		if normalized {
			report.NormalizedMain = true
			if mainCand, mainOK = recoveryConsolidationCandidate(mainPath, true); !mainOK {
				return report, fmt.Errorf("main transcript still failed a conservative load after normalization %s", mainPath)
			}
		} else {
			return report, fmt.Errorf("main transcript failed a conservative load; refusing to consolidate %s", mainPath)
		}
	}
	report.MainMessageCount = mainCand.MessageCount

	cands := make([]RecoveryBranchCandidate, 0, len(copies)+1)
	cands = append(cands, mainCand)
	for _, copy := range copies {
		cand, ok := recoveryConsolidationCandidate(copy, false)
		if !ok {
			// Recovery forks usually carry the same older-format payload as
			// the main they forked from; normalize them the same way so they
			// can participate instead of being silently skipped.
			if normalized, normErr := normalizeTranscriptInPlaceIfDirty(copy); normErr == nil && normalized {
				if cand, ok = recoveryConsolidationCandidate(copy, false); !ok {
					report.SkippedUnloadable = append(report.SkippedUnloadable, copy)
					continue
				}
			} else {
				report.SkippedUnloadable = append(report.SkippedUnloadable, copy)
				continue
			}
		}
		cands = append(cands, cand)
	}

	winner := mainCand
	for _, cand := range cands {
		if !cand.IsMain && consolidationCandidateBeats(cand, winner) {
			winner = cand
		}
	}
	report.WinnerMessageCount = winner.MessageCount
	if winner.Path != mainPath {
		report.WinnerPath = winner.Path
	}

	dir := filepath.Dir(mainPath)
	if winner.Path != mainPath {
		if SessionLeaseHeld(winner.Path) {
			return report, ErrSessionLeaseHeldForConsolidation
		}
		// A main that went through compaction holds a summarized transcript
		// whose prefix no longer matches the pre-compaction recovery fork, so
		// neither side covers the other. Refuse with a structured report the
		// UI can turn into an explicit confirmation instead of failing.
		if !SessionContentCovers(winner.Path, mainPath) && !opts.Force {
			report.BlockedByDivergence = true
			report.WinnerPath = winner.Path
			report.WinnerMessageCount = winner.MessageCount
			return report, nil
		}
		if err := promoteRecoveryCopyToMain(mainPath, winner.Path, dir, opts.Force); err != nil {
			return report, err
		}
		report.Promoted = true
		report.MainMessageCount = winner.MessageCount
	}

	// Fold covered losers into the recoverable trash. The winner itself has
	// already been renamed onto the main path; everything else that the
	// canonical transcript fully covers is now redundant by definition.
	for _, cand := range cands {
		if cand.IsMain || cand.Path == winner.Path {
			continue
		}
		if err := TrashRecoveryBranchCoveredBy(cand.Path, mainPath, dir); err != nil {
			report.SkippedNotCovered = append(report.SkippedNotCovered, cand.Path)
			continue
		}
		report.Trashed = append(report.Trashed, cand.Path)
	}
	sort.Strings(report.Trashed)
	sort.Strings(report.SkippedNotCovered)
	sort.Strings(report.SkippedUnloadable)
	return report, nil
}

// promoteRecoveryCopyToMain swaps the winner copy onto the main session
// identity. Both transcripts are held behind removal guards for the whole
// operation; the previous main is archived as one recoverable .trash entry
// (transcript plus every sidecar) before the winner takes over, so a crash
// between the two renames still leaves a complete rollback artifact.
func promoteRecoveryCopyToMain(mainPath, winnerPath, dir string, force bool) error {
	paths := []string{mainPath, winnerPath}
	sort.Strings(paths)
	guards := make([]*SessionRemovalGuard, 0, len(paths))
	for _, path := range paths {
		guard, err := TryAcquireSessionRemovalGuard(path)
		if err != nil {
			for _, held := range guards {
				held.Release()
			}
			return err
		}
		guards = append(guards, guard)
	}
	defer func() {
		for _, guard := range guards {
			guard.Release()
		}
	}()

	// The swap direction is only safe when the winner contains the whole
	// current main transcript; otherwise main-only turns would survive only
	// inside the trash archive. Force skips this refusal after an explicit
	// user confirmation — the previous main is still archived whole, so the
	// swapped-out content stays recoverable.
	if !SessionContentCovers(winnerPath, mainPath) {
		if !force {
			return ErrMainNotCoveredByWinner
		}
	}

	legacyMeta, legacyOK, err := LoadBranchMeta(mainPath)
	if err != nil {
		return err
	}
	winnerMeta, winnerOK, err := LoadBranchMeta(winnerPath)
	if err != nil {
		return err
	}
	if !winnerOK {
		return fmt.Errorf("recovery copy meta is missing for %s", winnerPath)
	}

	// 1) Archive the previous main (transcript + sidecars) as one recoverable
	// trash entry, staged atomically like every other recovery trash move.
	key := filepath.Base(mainPath)
	if !validRecoveryTrashKey(key) {
		return fmt.Errorf("invalid main session path for trash staging: %s", mainPath)
	}
	stageDir, err := reserveRecoveryTrashStage(dir)
	if err != nil {
		return err
	}
	if err := writeRecoveryTrashPending(stageDir, key); err != nil {
		return err
	}
	if err := moveRecoveryTrashPath(mainPath, filepath.Join(stageDir, key)); err != nil {
		return err
	}
	for _, src := range recoveryTrashSidecars(mainPath) {
		if err := moveRecoveryTrashPath(src, filepath.Join(stageDir, filepath.Base(src))); err != nil {
			return err
		}
	}
	itemDir, err := publishRecoveryTrashStage(dir, key, stageDir)
	if err != nil {
		return err
	}
	if err := writeRecoveryTrashMetaExisting(itemDir, key); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		// Published entries may already have been restored or purged.
	}
	if err := clearRecoveryTrashPending(itemDir); err != nil {
		return err
	}

	// 2) The winner copy takes over the main identity. Derived indexes are
	// dropped rather than renamed: the next load rebuilds them from content.
	if err := os.Rename(winnerPath, mainPath); err != nil {
		return err
	}
	winnerStem := strings.TrimSuffix(filepath.Base(winnerPath), ".jsonl")
	mainStem := strings.TrimSuffix(filepath.Base(mainPath), ".jsonl")
	dropSidecar := map[string]bool{
		filepath.Base(store.SessionEventIndex(winnerPath)):  true,
		filepath.Base(store.SessionDisplayIndex(winnerPath)): true,
	}
	artifacts := append([]string{}, store.SessionSidecarFiles(winnerPath)...)
	artifacts = append(artifacts,
		winnerPath+".telemetry.json",
		store.SessionCheckpointDir(winnerPath),
		store.SessionJobsDir(winnerPath),
		store.SessionInboxDir(winnerPath),
	)
	for _, src := range artifacts {
		base := filepath.Base(src)
		if dropSidecar[base] {
			if err := os.RemoveAll(src); err != nil {
				return err
			}
			continue
		}
		dst := filepath.Join(dir, strings.Replace(base, winnerStem, mainStem, 1))
		if err := moveRecoveryTrashPath(src, dst); err != nil {
			return err
		}
	}

	// 3) Rewrite the main meta: keep the session's display/ownership identity,
	// take the winner's content identity, and clear every recovery marker so
	// the lineage reads as resolved. The touched UpdatedAt is what open tabs
	// notice (the #9468 reload path) when they refresh.
	return UpdateBranchMeta(mainPath, true, func(meta *BranchMeta) error {
		meta.ID = BranchID(mainPath)
		meta.Recovered = false
		meta.RecoveryReason = ""
		meta.RecoveryDigest = ""
		meta.RecoveryDepth = 0
		meta.RecoveryPreferred = false
		meta.RecoveryPreferredDigest = ""
		meta.ParentID = ""
		meta.ForkTurn = 0
		meta.ForkMessageIndex = 0
		meta.InFlightTurn = nil
		meta.Revision = winnerMeta.Revision
		meta.ContentDigest = winnerMeta.ContentDigest
		meta.Turns = winnerMeta.Turns
		meta.Preview = winnerMeta.Preview
		meta.SchemaVersion = winnerMeta.SchemaVersion
		meta.WriterID = SessionWriterID()
		if legacyOK {
			meta.Name = legacyMeta.Name
			meta.CustomTitle = legacyMeta.CustomTitle
			meta.TopicID = legacyMeta.TopicID
			meta.TopicTitle = legacyMeta.TopicTitle
			meta.Scope = legacyMeta.Scope
			meta.WorkspaceRoot = legacyMeta.WorkspaceRoot
			meta.Model = legacyMeta.Model
			meta.QualityFloor = legacyMeta.QualityFloor
			meta.Mode = legacyMeta.Mode
			meta.ToolApprovalMode = legacyMeta.ToolApprovalMode
			meta.Goal = legacyMeta.Goal
			meta.CreatedAt = legacyMeta.CreatedAt
			meta.DismissedTodoBatches = legacyMeta.DismissedTodoBatches
		}
		return nil
	})
}
