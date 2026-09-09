package agent

import (
	"strings"
	"testing"
)

func TestContextBudgetBlockShapes(t *testing.T) {
	regular := ContextBudgetBlock(41_000, 102_400, 128_000)
	if !strings.Contains(regular, "<context-budget>context: 41k/128k tokens (32%); auto-compaction at 80%</context-budget>") {
		t.Fatalf("regular block = %q", regular)
	}
	if strings.Contains(regular, "approaching") {
		t.Fatalf("regular block must not warn: %q", regular)
	}

	near := ContextBudgetBlock(95_000, 102_400, 128_000)
	if !strings.Contains(near, "approaching auto-compaction; older context will be summarized, not lost") {
		t.Fatalf("near-trigger block missing guidance: %q", near)
	}
	if !strings.HasPrefix(near, "<context-budget>") {
		t.Fatalf("guidance must trail the tag line: %q", near)
	}

	if got := ContextBudgetBlock(0, 102_400, 128_000); got != "" {
		t.Fatalf("no-usage block = %q, want empty", got)
	}
	if got := ContextBudgetBlock(41_000, 102_400, 0); got != "" {
		t.Fatalf("unknown-window block = %q, want empty", got)
	}
}

func TestWithContextBudgetPrefixesAndSkips(t *testing.T) {
	sess := foldableSessionOverForce(10)
	prov := &overflowSummaryProvider{}
	a := agentOverForceWindow(t, prov, sess, 60_000)
	out := a.WithContextBudget("user text")
	if !strings.HasPrefix(out, "<context-budget>") || !strings.Contains(out, "user text") {
		t.Fatalf("budget block missing from turn: %q", out)
	}
	again := a.WithContextBudget(out)
	if again != out {
		t.Fatal("double injection must be a no-op")
	}
	var nilAgent *Agent
	if got := nilAgent.WithContextBudget("x"); got != "x" {
		t.Fatalf("nil agent changed content: %q", got)
	}
}
