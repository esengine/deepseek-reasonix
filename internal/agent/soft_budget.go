package agent

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/i18n"
	"reasonix/internal/tool"
)

const (
	readonlySoftBudgetRounds = 10
	softBudgetHardFollowup   = 2
	softBudgetHistorySamples = 5
	softBudgetHistoryWindow  = 21
	// maxSoftBudgetExtensions bounds extend_research_budget: 10 -> 20 -> 40 -> 80.
	maxSoftBudgetExtensions = 3
)

var readonlySoftBudgetHistory = struct {
	sync.Mutex
	byKey map[string][]time.Duration
}{byKey: map[string][]time.Duration{}}

func (a *Agent) applySoftBudget(outcomes []toolOutcome) intervention {
	if a == nil {
		return intervention{}
	}
	limit := a.task.budget.limit
	if limit.Tokens > 0 || limit.Wall > 0 || limit.Cost > 0 {
		return intervention{}
	}
	if !a.readonlySoftBudgetApplies(outcomes) {
		return intervention{}
	}
	rounds := a.turn.budget.rounds
	elapsed := a.turn.budget.elapsed()
	median := rollingReadonlySoftBudgetMedian(a.softBudgetHistoryKey())
	extensions := a.turn.loop.softBudgetExtensionCount()
	// Each granted extension doubles both gates: rounds 10/20/40/80 and the
	// time gate 2x/4x/8x/16x the rolling median.
	roundLimit := readonlySoftBudgetRounds << extensions
	remaining := maxSoftBudgetExtensions - extensions
	timeLimit := time.Duration(1<<uint(extensions+1)) * median
	timeExceeded := median > 0 && elapsed >= timeLimit
	if rounds < roundLimit && !timeExceeded {
		return intervention{}
	}
	if a.turn.loop.markSoftBudgetNudged(rounds) {
		if a.capabilityAudit != nil {
			a.capabilityAudit.RecordLoopGuard("soft_budget")
		}
		trigger := fmt.Sprintf("%d main-model rounds", rounds)
		if timeExceeded {
			trigger = fmt.Sprintf("%s elapsed (2x rolling median %s)", elapsed.Round(time.Second), median.Round(time.Second))
		}
		return intervention{
			verdict:  verdictRedirect,
			guidance: "Host budget check: this read-only planning/analysis task exceeded its soft round or elapsed-time budget. First judge whether deeper investigation is genuinely needed: if yes, call extend_research_budget with a reason to double the budget (" + fmt.Sprintf("%d", remaining) + " extension(s) left this turn); if not, stop expanding scope, summarize the evidence you already have, and name the remaining real blocker if any.",
			notice:   noticeFor(event.NoticeCodeLoopGuard, event.LevelInfo, i18n.M.SoftBudgetConverge, "soft budget after "+trigger),
		}
	}
	if nudgedAt := a.turn.loop.softBudgetNudgedAt(); nudgedAt > 0 && rounds >= nudgedAt+softBudgetHardFollowup {
		guidance := "Host budget check: two further rounds passed after the convergence nudge. Output the current result or name exactly one real blocker. Do not continue exploring."
		if remaining > 0 {
			guidance = "Host budget check: two further rounds passed after the convergence nudge. Output the current result or name exactly one real blocker. If deeper investigation is genuinely required, call extend_research_budget (" + fmt.Sprintf("%d", remaining) + " extension(s) left this turn); otherwise stop exploring."
		}
		return intervention{
			verdict:  verdictRedirect,
			guidance: guidance,
		}
	}
	return intervention{}
}

func (a *Agent) readonlySoftBudgetApplies(outcomes []toolOutcome) bool {
	if a.planMode.Load() {
		return true
	}
	for _, outcome := range outcomes {
		if outcome.workspaceMutation != nil || (outcome.resolved && !outcome.resolvedReadOnly) {
			a.turn.softBudgetMutation = true
			return false
		}
	}
	if a.task.ledger == nil {
		return true
	}
	for _, rec := range a.task.ledger.Receipts() {
		if rec.Write {
			a.turn.softBudgetMutation = true
			return false
		}
	}
	return true
}

func (a *Agent) softBudgetHistoryKey() string {
	if a == nil {
		return ""
	}
	kind := "analysis"
	if a.planMode.Load() {
		kind = "plan"
	} else if a.readOnlyExecution {
		kind = "read_only_agent"
	}
	return strings.TrimSpace(a.modelRef) + "|" + kind
}

func recordReadonlySoftBudgetDuration(key string, duration time.Duration) {
	if key == "" || duration <= 0 {
		return
	}
	readonlySoftBudgetHistory.Lock()
	defer readonlySoftBudgetHistory.Unlock()
	values := append(readonlySoftBudgetHistory.byKey[key], duration)
	if len(values) > softBudgetHistoryWindow {
		values = append([]time.Duration(nil), values[len(values)-softBudgetHistoryWindow:]...)
	}
	readonlySoftBudgetHistory.byKey[key] = values
}

func rollingReadonlySoftBudgetMedian(key string) time.Duration {
	readonlySoftBudgetHistory.Lock()
	values := append([]time.Duration(nil), readonlySoftBudgetHistory.byKey[key]...)
	readonlySoftBudgetHistory.Unlock()
	if len(values) < softBudgetHistorySamples {
		return 0
	}
	slices.Sort(values)
	return values[len(values)/2]
}

func (a *Agent) recordReadonlySoftBudgetSample(state *turnRuntime, runErr error) {
	if a == nil || state == nil || runErr != nil || state.softBudgetMutation || !state.usedAnyTool || state.budget.rounds == 0 {
		return
	}
	limit := a.task.budget.limit
	if limit.Tokens > 0 || limit.Wall > 0 || limit.Cost > 0 {
		return
	}
	recordReadonlySoftBudgetDuration(a.softBudgetHistoryKey(), state.budget.elapsed())
}

// ExtendResearchBudget implements tool.ResearchBudgetExtender. It doubles the
// active turn's read-only soft budget after the convergence nudge fired this
// turn, at most maxSoftBudgetExtensions times (10 -> 20 -> 40 -> 80 rounds).
// The extension re-arms the nudge so the hard follow-up is measured from the
// new budget instead of firing two rounds later.
func (a *Agent) ExtendResearchBudget(reason string) (tool.ResearchBudgetExtension, string, error) {
	if a == nil {
		return tool.ResearchBudgetExtension{}, "", fmt.Errorf("extend_research_budget is not available")
	}
	if strings.TrimSpace(reason) == "" {
		return tool.ResearchBudgetExtension{}, "", fmt.Errorf("extend_research_budget: reason is required")
	}
	// Only after the nudge fired this turn: either it is still armed, or a
	// previous extension re-armed it (the counter persists for the turn).
	if a.turn.loop.softBudgetNudgedAt() == 0 && a.turn.loop.softBudgetExtensionCount() == 0 {
		return tool.ResearchBudgetExtension{}, "", fmt.Errorf("extend_research_budget is only available after the read-only budget nudge fired this turn — no budget was changed")
	}
	extensions, ok := a.turn.loop.extendSoftBudget(maxSoftBudgetExtensions)
	if !ok {
		return tool.ResearchBudgetExtension{}, "", fmt.Errorf("extend_research_budget: all %d extensions are used this turn — converge and report the evidence you already have", maxSoftBudgetExtensions)
	}
	rounds := readonlySoftBudgetRounds << extensions
	remaining := maxSoftBudgetExtensions - extensions
	if a.capabilityAudit != nil {
		a.capabilityAudit.RecordLoopGuard("soft_budget_extended")
	}
	return tool.ResearchBudgetExtension{Rounds: rounds, Remaining: remaining},
		fmt.Sprintf("Read-only research budget doubled to %d rounds (%d extension(s) left this turn).", rounds, remaining), nil
}
