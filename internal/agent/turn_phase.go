package agent

import (
	"slices"
	"time"

	"reasonix/internal/agentpreset"
	"reasonix/internal/completion"
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/taskcontract"
)

type phaseClock struct {
	last event.TurnPhaseName
	at   time.Time
}

// emitTurnPhase publishes a content-free host phase for the active turn.
func (a *Agent) emitTurnPhase(phase event.TurnPhaseName) {
	if a == nil || a.svc.sink == nil || phase == "" {
		return
	}
	now := time.Now()
	a.recordOpenPhase(now)
	a.turn.phase = phaseClock{last: phase, at: now}
	a.svc.sink.Emit(event.Event{Kind: event.TurnPhase, PhaseName: phase, Text: string(phase)})
}

// recordOpenPhase bills the phase that is ending. A span is accounted for only
// when something closes it, so a phase nobody follows records nothing.
func (a *Agent) recordOpenPhase(now time.Time) {
	if a.turn.phase.at.IsZero() || a.capabilityAudit == nil {
		return
	}
	a.capabilityAudit.RecordPhaseMs(phaseAuditName(a.turn.phase.last), now.Sub(a.turn.phase.at).Milliseconds())
}

// closeTurnPhase bills the open phase without opening another one, for the end
// of a turn: beginRunTurn resets a.turn, so an unclosed tail span is lost.
func (a *Agent) closeTurnPhase() {
	if a == nil {
		return
	}
	a.recordOpenPhase(time.Now())
	a.turn.phase = phaseClock{}
}

func phaseAuditName(phase event.TurnPhaseName) string {
	switch phase {
	case event.TurnPhaseWorking:
		return "provider"
	case event.TurnPhaseChecking, event.TurnPhaseVerifying:
		return "tool"
	case event.TurnPhaseReviewing:
		return "review"
	default:
		return ""
	}
}

// emitCompletionSummary publishes the content-free end-of-turn quality summary
// when the turn mutated state or finished Partial/Blocked. Pure conversation
// and ordinary read-only success do not emit a quality card.
func (a *Agent) emitCompletionSummary(c *taskcontract.Contract, report completion.Report) {
	if a == nil || a.svc.sink == nil || c == nil {
		return
	}
	mutations := 0
	if a.task.ledger != nil {
		for _, r := range a.task.ledger.Receipts() {
			if evidence.IsDeliveryMutation(r, a.writeWorkspaceRoot, nil) {
				mutations++
			}
		}
	}
	passed, failed, suppressed := 0, 0, 0
	for _, check := range c.Checks {
		switch check.Status {
		case taskcontract.Satisfied:
			passed++
		case taskcontract.Failed:
			failed++
		case taskcontract.Suppressed:
			suppressed++
		}
	}
	verdict := c.GoalVerdict()
	floor := a.turn.constraints.PolicyFloor.String()
	attention := completion.NeedsAttention(completion.AttentionInput{
		Verdict:            verdict.String(),
		ChecksFailed:       failed,
		GapKinds:           report.GapKinds(),
		Floor:              floor,
		RequiredSuppressed: c.HasSuppressed(),
	})
	if mutations == 0 && !attention && len(report.Verifications) == 0 {
		return
	}
	review := "none"
	if a.task.ledger != nil {
		if mut, ok := a.task.ledger.LatestSuccessfulMutationIndex(); ok {
			if a.task.ledger.HasSuccessfulReviewAfter(mut) {
				review = "passed"
			} else if a.requiresIndependentReview() {
				review = "unavailable"
			}
		}
	}
	var gaps []string
	if c.HasSuppressed() {
		gaps = append(gaps, "suppressed")
	}
	for _, check := range c.Checks {
		if check.Status == taskcontract.Stale {
			gaps = append(gaps, "stale_check")
			break
		}
	}
	for _, req := range c.Requirements {
		if req.Required && req.Status == taskcontract.Suppressed {
			gaps = append(gaps, "suppressed_requirement")
			break
		}
	}
	gaps = completionGapKinds(gaps, report)
	constraintDegraded := a.turn.constraints.ForbidTests || len(a.turn.constraints.AllowedChecks) > 0
	summaryVerdict := verdict.String()
	switch verdict {
	case taskcontract.VerdictComplete:
		summaryVerdict = "complete"
	case taskcontract.VerdictPartial:
		summaryVerdict = "partial"
	case taskcontract.VerdictBlocked:
		summaryVerdict = "blocked"
	case taskcontract.VerdictContinue:
		summaryVerdict = "continue"
	}
	a.svc.sink.Emit(event.Event{
		Kind: event.CompletionSummary,
		Completion: &event.CompletionSummaryInfo{
			// Preset is a deprecated wire-compat field: it is pinned to the
			// historical default so one-version-old clients keep parsing. New
			// surfaces read the verdict/check/review/gap fields instead.
			Preset:             string(agentpreset.Standard),
			Verdict:            summaryVerdict,
			Mutations:          mutations,
			ChecksPassed:       passed,
			ChecksFailed:       failed,
			ChecksSuppressed:   suppressed,
			Review:             review,
			GapKinds:           gaps,
			ConstraintDegraded: constraintDegraded,
			Floor:              floor,
			Attention:          attention,
		},
	})
}

func completionGapKinds(gaps []string, report completion.Report) []string {
	for _, gap := range report.Gaps {
		kind := gap.Kind.String()
		if !slices.Contains(gaps, kind) {
			gaps = append(gaps, kind)
		}
	}
	return gaps
}
