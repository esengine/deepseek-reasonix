package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
)

// seedMainTranscript writes a 3-turn stalled main transcript, the on-disk
// shape a session is left in when a snapshot conflict forked its unsaved
// tail into a recovery copy.
func seedMainTranscript(t *testing.T, dir, name string) string {
	t.Helper()
	mainPath := filepath.Join(dir, name+".jsonl")
	s := NewSession("sys")
	s.Add(provider.Message{Role: provider.RoleUser, Content: "first"})
	s.Add(provider.Message{Role: provider.RoleAssistant, Content: "one"})
	s.Add(provider.Message{Role: provider.RoleUser, Content: "tail " + name})
	if err := s.Save(mainPath); err != nil {
		t.Fatalf("Save main: %v", err)
	}
	return mainPath
}

// forkCopyFromSnapshot forks a recovery copy for mainPath whose content is
// snapshot plus extra turns — the real conflict shape: same prefix as the
// on-disk main, diverging only at the tail.
func forkCopyFromSnapshot(t *testing.T, mainPath string, snapshot []provider.Message, extra []provider.Message) string {
	t.Helper()
	stale := NewSession("sys")
	stale.Replace(append(append([]provider.Message(nil), snapshot...), extra...))
	info, err := stale.SaveRecoveryBranch(RecoveryBranchOptions{OriginalPath: mainPath})
	if err != nil {
		t.Fatalf("SaveRecoveryBranch: %v", err)
	}
	return info.Path
}

func loadSnapshot(t *testing.T, path string) []provider.Message {
	t.Helper()
	s, err := LoadSession(path)
	if err != nil {
		t.Fatalf("LoadSession %s: %v", path, err)
	}
	return s.Snapshot()
}

func appendTurns(t *testing.T, path string, msgs ...provider.Message) {
	t.Helper()
	s, err := LoadSession(path)
	if err != nil {
		t.Fatalf("LoadSession %s: %v", path, err)
	}
	for _, m := range msgs {
		s.Add(m)
	}
	if err := s.Save(path); err != nil {
		t.Fatalf("Save %s: %v", path, err)
	}
}

func sessionMessageCount(t *testing.T, path string) int {
	t.Helper()
	snap, ok := LoadSessionContentSnapshot(path)
	if !ok {
		t.Fatalf("conservative load failed for %s", path)
	}
	return snap.Len()
}

func TestConsolidatePromotesFullestCopyAndFoldsCoveredLosers(t *testing.T) {
	dir := t.TempDir()
	mainPath := seedMainTranscript(t, dir, "topic")
	base := loadSnapshot(t, mainPath)

	// Copy A went on accumulating: base + 2 turns (5 total). It covers main.
	copyA := forkCopyFromSnapshot(t, mainPath, base, []provider.Message{
		{Role: provider.RoleUser, Content: "recovered turn a"},
		{Role: provider.RoleAssistant, Content: "recovered reply b"},
	})
	// Copy B stopped one turn earlier: base + 1 turn (4 total) — a strict
	// prefix of A, so the winner fully covers it and it folds into the trash.
	copyB := forkCopyFromSnapshot(t, mainPath, base, []provider.Message{
		{Role: provider.RoleUser, Content: "recovered turn a"},
	})

	legacyMeta, legacyOK, err := LoadBranchMeta(mainPath)
	if err != nil {
		t.Fatalf("LoadBranchMeta main before consolidate: %v", err)
	}

	report, err := ConsolidateSessionRecoveryBranches(mainPath)
	if err != nil {
		t.Fatalf("ConsolidateSessionRecoveryBranches: %v", err)
	}
	if !report.Promoted {
		t.Fatalf("expected a promotion, report = %+v", report)
	}
	if filepath.Clean(report.WinnerPath) != filepath.Clean(copyA) {
		t.Fatalf("winner = %q, want copy A %q", report.WinnerPath, copyA)
	}
	if report.MainMessageCount != 6 {
		t.Fatalf("main message count after promote = %d, want 6", report.MainMessageCount)
	}
	if len(report.Trashed) != 1 || filepath.Clean(report.Trashed[0]) != filepath.Clean(copyB) {
		t.Fatalf("trashed = %v, want [%q]", report.Trashed, copyB)
	}
	if len(report.SkippedNotCovered) != 0 || len(report.SkippedUnloadable) != 0 {
		t.Fatalf("unexpected skips: %+v", report)
	}
	// The main transcript now carries the winner's content...
	if got := sessionMessageCount(t, mainPath); got != 6 {
		t.Fatalf("main transcript message count = %d, want 6", got)
	}
	// ...under the main identity with recovery markers cleared.
	meta, ok, err := LoadBranchMeta(mainPath)
	if err != nil || !ok {
		t.Fatalf("LoadBranchMeta main: ok=%v err=%v", ok, err)
	}
	if meta.Recovered {
		t.Fatalf("promoted main still marked Recovered")
	}
	if meta.RecoveryDigest != "" || meta.ParentID != "" || meta.RecoveryPreferred {
		t.Fatalf("recovery markers not cleared: %+v", meta)
	}
	if meta.ID != BranchID(mainPath) {
		t.Fatalf("meta ID = %q, want %q", meta.ID, BranchID(mainPath))
	}
	if legacyOK && meta.Name != legacyMeta.Name {
		t.Fatalf("main display name changed in promotion: %q -> %q", legacyMeta.Name, meta.Name)
	}
	// The archived main stays recoverable in the trash.
	if _, err := os.Stat(filepath.Join(dir, ".trash")); err != nil {
		t.Fatalf("trash directory missing: %v", err)
	}
	// The winner copy no longer exists at its old path.
	if _, err := os.Stat(copyA); !os.IsNotExist(err) {
		t.Fatalf("winner copy still present at %s: %v", copyA, err)
	}
}

func TestConsolidateWithoutPromotionOnlyFoldsCoveredCopies(t *testing.T) {
	dir := t.TempDir()
	mainPath := seedMainTranscript(t, dir, "ahead")
	base := loadSnapshot(t, mainPath)

	copyPath := forkCopyFromSnapshot(t, mainPath, base, []provider.Message{
		{Role: provider.RoleUser, Content: "recovered turn a"},
	})

	// The main adopted the copy's recovered turn and went on: the copy is
	// now a strict prefix of the main, so no promotion is needed and the
	// copy folds straight into the trash.
	appendTurns(t, mainPath,
		provider.Message{Role: provider.RoleUser, Content: "recovered turn a"},
		provider.Message{Role: provider.RoleAssistant, Content: "kept going"},
	)

	report, err := ConsolidateSessionRecoveryBranches(mainPath)
	if err != nil {
		t.Fatalf("ConsolidateSessionRecoveryBranches: %v", err)
	}
	if report.Promoted {
		t.Fatalf("unexpected promotion: %+v", report)
	}
	if report.WinnerPath != "" {
		t.Fatalf("winner path = %q, want empty", report.WinnerPath)
	}
	if len(report.Trashed) != 1 || filepath.Clean(report.Trashed[0]) != filepath.Clean(copyPath) {
		t.Fatalf("trashed = %v, want [%q]", report.Trashed, copyPath)
	}
}

func TestConsolidateRefusesSwapWhenWinnerMissesMainTurns(t *testing.T) {
	dir := t.TempDir()
	mainPath := seedMainTranscript(t, dir, "diverged")
	base := loadSnapshot(t, mainPath)

	// The copy diverged at the tail and grew longer: base + two of its own
	// turns, so it wins the candidacy but cannot cover the main.
	copyPath := forkCopyFromSnapshot(t, mainPath, base, []provider.Message{
		{Role: provider.RoleUser, Content: "copy-only turn a"},
		{Role: provider.RoleAssistant, Content: "copy-only reply b"},
	})
	// The main also gained its own turn: neither side covers the other.
	appendTurns(t, mainPath, provider.Message{Role: provider.RoleUser, Content: "main-only turn"})

	// The conservative run refuses the swap with a structured report instead
	// of an error, so the UI can turn it into an explicit confirmation.
	report, err := ConsolidateSessionRecoveryBranches(mainPath)
	if err != nil {
		t.Fatalf("err = %v, want nil (report %+v)", err, report)
	}
	if !report.BlockedByDivergence {
		t.Fatalf("BlockedByDivergence = false, want true: %+v", report)
	}
	if report.Promoted {
		t.Fatalf("unexpected promotion: %+v", report)
	}
	// Both transcripts stay untouched.
	if got := sessionMessageCount(t, mainPath); got != 5 {
		t.Fatalf("main message count = %d, want 5 (unchanged)", got)
	}
	if got := sessionMessageCount(t, copyPath); got != 6 {
		t.Fatalf("copy message count = %d, want 6 (unchanged)", got)
	}

	// The forced run promotes the fullest copy; the previous main is archived
	// whole under the recoverable trash instead of being lost.
	forced, err := ConsolidateSessionRecoveryBranchesWithOptions(mainPath, ConsolidateOptions{Force: true})
	if err != nil {
		t.Fatalf("forced consolidation: %v", err)
	}
	if !forced.Promoted {
		t.Fatalf("forced report = %+v, want Promoted", forced)
	}
	if got := sessionMessageCount(t, mainPath); got != 6 {
		t.Fatalf("main message count after force = %d, want 6", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".trash")); err != nil {
		t.Fatalf("trash directory missing after forced swap: %v", err)
	}
}

func TestConsolidateKeepsLoserWithUniqueTurns(t *testing.T) {
	dir := t.TempDir()
	mainPath := seedMainTranscript(t, dir, "topic")
	base := loadSnapshot(t, mainPath)

	// Clear winner: base + 4 turns, covers main.
	forkCopyFromSnapshot(t, mainPath, base, []provider.Message{
		{Role: provider.RoleUser, Content: "recovered turn a"},
		{Role: provider.RoleAssistant, Content: "recovered reply b"},
		{Role: provider.RoleUser, Content: "recovered turn c"},
		{Role: provider.RoleAssistant, Content: "recovered reply d"},
	})
	// Copy B holds one turn that exists nowhere else, so even though A is
	// longer it does not cover B; B must be preserved untouched.
	copyB := forkCopyFromSnapshot(t, mainPath, base, []provider.Message{
		{Role: provider.RoleUser, Content: "unique to B"},
	})

	report, err := ConsolidateSessionRecoveryBranches(mainPath)
	if err != nil {
		t.Fatalf("ConsolidateSessionRecoveryBranches: %v", err)
	}
	if !report.Promoted {
		t.Fatalf("expected promotion of copy A: %+v", report)
	}
	if len(report.SkippedNotCovered) != 1 || filepath.Clean(report.SkippedNotCovered[0]) != filepath.Clean(copyB) {
		t.Fatalf("skippedNotCovered = %v, want [%q]", report.SkippedNotCovered, copyB)
	}
	if got := sessionMessageCount(t, copyB); got != 5 {
		t.Fatalf("copy B message count = %d, want 5 (preserved)", got)
	}
}

func TestConsolidateNoCopiesAndValidation(t *testing.T) {
	dir := t.TempDir()
	mainPath := seedMainTranscript(t, dir, "plain")

	if _, err := ConsolidateSessionRecoveryBranches(mainPath); !errors.Is(err, ErrNoRecoveryBranches) {
		t.Fatalf("err = %v, want ErrNoRecoveryBranches", err)
	}

	// Recovery copies themselves are not valid targets.
	base := loadSnapshot(t, mainPath)
	copyPath := forkCopyFromSnapshot(t, mainPath, base, []provider.Message{
		{Role: provider.RoleUser, Content: "copy turn"},
	})
	if _, err := ConsolidateSessionRecoveryBranches(copyPath); err == nil {
		t.Fatalf("consolidating a recovery copy must fail")
	}

	// Listing reports the copy alongside the main transcript.
	cands, err := ListSessionRecoveryBranches(mainPath)
	if err != nil {
		t.Fatalf("ListSessionRecoveryBranches: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want 2", len(cands))
	}
	if !cands[0].IsMain {
		t.Fatalf("first candidate must be the main transcript: %+v", cands[0])
	}
}

func TestConsolidateNormalizesDirtyMainFirst(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "dirty.jsonl")
	// Dangling tool call: loading this older-format transcript normalizes
	// dirty (a placeholder tool result gets fabricated), which is exactly the
	// state that used to make consolidation refuse.
	writeLegacyJSONLSession(t, mainPath, []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "run it"},
		{Role: provider.RoleAssistant, Content: "", ToolCalls: []provider.ToolCall{
			{ID: "call_1", Name: "read_file", Arguments: `{"path":"a.go"}`},
		}},
	})
	if s, err := LoadSession(mainPath); err != nil || !s.normalizedDirty {
		t.Fatalf("precondition: normalizedDirty = %v err = %v, want true", s.normalizedDirty, err)
	}

	// The recovery copy continued past the repaired view: it is the fullest
	// transcript and covers the main once normalized.
	repaired := loadSnapshot(t, mainPath)
	forkCopyFromSnapshot(t, mainPath, repaired, []provider.Message{
		{Role: provider.RoleAssistant, Content: "post-recovery reply"},
	})

	report, err := ConsolidateSessionRecoveryBranches(mainPath)
	if err != nil {
		t.Fatalf("ConsolidateSessionRecoveryBranches: %v", err)
	}
	if !report.NormalizedMain {
		t.Fatalf("expected the dirty main to be normalized in place: %+v", report)
	}
	if !report.Promoted {
		t.Fatalf("expected promotion after normalization: %+v", report)
	}
	if got := sessionMessageCount(t, mainPath); got != len(repaired)+1 {
		t.Fatalf("main message count = %d, want %d", got, len(repaired)+1)
	}
	if meta, ok, err := LoadBranchMeta(mainPath); err != nil || !ok || meta.Recovered {
		t.Fatalf("promoted main meta: ok=%v recovered=%v err=%v", ok, meta.Recovered, err)
	}
}
