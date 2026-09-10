package agent

import (
	"strings"
	"testing"
	"time"

	"reasonix/internal/tool"
)

func TestSoftBudgetUsesRollingMedianElapsedTime(t *testing.T) {
	a := &Agent{}
	a.modelRef = t.Name()
	key := a.softBudgetHistoryKey()
	t.Cleanup(func() {
		readonlySoftBudgetHistory.Lock()
		delete(readonlySoftBudgetHistory.byKey, key)
		readonlySoftBudgetHistory.Unlock()
	})
	for _, sample := range []time.Duration{90, 95, 100, 105, 110} {
		recordReadonlySoftBudgetDuration(key, sample*time.Millisecond)
	}
	a.turn.budget = runBudget{started: time.Now().Add(-250 * time.Millisecond), rounds: 2}
	first := a.applySoftBudget(nil)
	if first.verdict != verdictRedirect || !strings.Contains(first.notice.Detail, "2x rolling median") {
		t.Fatalf("elapsed-time nudge = %+v", first)
	}

	a.turn.budget.rounds = 4
	second := a.applySoftBudget(nil)
	if second.verdict != verdictRedirect || !strings.Contains(second.guidance, "two further rounds") {
		t.Fatalf("hard follow-up = %+v", second)
	}
}

func TestExtendResearchBudgetDoublesGatesAndCapsAtThree(t *testing.T) {
	// Before the nudge the tool is refused: the model must not pre-emptively
	// raise its own budget.
	fresh := &Agent{}
	fresh.turn.budget = runBudget{started: time.Now(), rounds: 1}
	if _, _, err := fresh.ExtendResearchBudget("curious"); err == nil {
		t.Fatal("extension before the nudge should be refused")
	}
	if _, _, err := fresh.ExtendResearchBudget("   "); err == nil {
		t.Fatal("extension without a reason should be refused")
	}

	a := &Agent{}
	a.modelRef = t.Name()
	a.turn.budget = runBudget{started: time.Now(), rounds: readonlySoftBudgetRounds}

	nudge := a.applySoftBudget(nil)
	if nudge.verdict != verdictRedirect || !strings.Contains(nudge.guidance, "extend_research_budget") {
		t.Fatalf("first nudge should offer the extension: %+v", nudge)
	}

	ext, text, err := a.ExtendResearchBudget("the conflict surface needs another pass")
	if err != nil || ext.Rounds != 20 || ext.Remaining != 2 {
		t.Fatalf("first extension = %+v %q %v", ext, text, err)
	}
	if !strings.Contains(text, "20 rounds") || !strings.Contains(text, "2 extension") {
		t.Fatalf("extension text = %q", text)
	}

	// Inside the doubled budget no further nudge fires.
	a.turn.budget.rounds = 15
	if got := a.applySoftBudget(nil); got.verdict != verdictContinue {
		t.Fatalf("round 15 under the doubled budget should not nudge: %+v", got)
	}
	// The extension re-armed the nudge: it fires again at the new limit.
	a.turn.budget.rounds = 20
	if got := a.applySoftBudget(nil); got.verdict != verdictRedirect {
		t.Fatalf("round 20 should nudge again: %+v", got)
	}

	for _, want := range []int{40, 80} {
		ext, _, err := a.ExtendResearchBudget("still digging")
		if err != nil || ext.Rounds != want {
			t.Fatalf("extension to %d = %+v %v", want, ext, err)
		}
	}
	if _, _, err := a.ExtendResearchBudget("one more"); err == nil {
		t.Fatal("a fourth extension should be refused")
	}

	// The time gate doubles with the same counter: 3 extensions mean 16x the
	// rolling median instead of 2x.
	key := a.softBudgetHistoryKey()
	t.Cleanup(func() {
		readonlySoftBudgetHistory.Lock()
		delete(readonlySoftBudgetHistory.byKey, key)
		readonlySoftBudgetHistory.Unlock()
	})
	for _, sample := range []time.Duration{100, 100, 100, 100, 100} {
		recordReadonlySoftBudgetDuration(key, sample*time.Millisecond)
	}
	a.turn.budget = runBudget{started: time.Now().Add(-1 * time.Second), rounds: 1}
	if got := a.applySoftBudget(nil); got.verdict != verdictContinue {
		t.Fatalf("1s elapsed is inside 16x100ms after three extensions: %+v", got)
	}
	a.turn.budget = runBudget{started: time.Now().Add(-2 * time.Second), rounds: 1}
	if got := a.applySoftBudget(nil); got.verdict != verdictRedirect {
		t.Fatalf("2s elapsed should exceed 16x100ms: %+v", got)
	}
}

func TestExtendResearchBudgetToolFailsClosedWithoutExtender(t *testing.T) {
	builtin, ok := tool.LookupBuiltin("extend_research_budget")
	if !ok {
		t.Fatal("extend_research_budget builtin not registered")
	}
	if _, err := builtin.Execute(t.Context(), []byte(`{"reason":"need more evidence"}`)); err == nil {
		t.Fatal("the tool must fail closed outside an agent turn")
	}
}
