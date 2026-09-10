package tool

import "context"

// ResearchBudgetExtension reports the outcome of doubling the read-only
// research budget for the active turn.
type ResearchBudgetExtension struct {
	// Rounds is the new per-turn read-only round limit (10 -> 20 -> 40 -> 80).
	Rounds int
	// Remaining is how many further doublings are still available.
	Remaining int
}

// ResearchBudgetExtender doubles the read-only soft research budget for the
// active turn when the model judges that deeper investigation is warranted.
// The host controller implements it. It is absent in ordinary contexts (plain
// chat, subagents), where the tool must fail closed without changing state.
type ResearchBudgetExtender interface {
	// ExtendResearchBudget doubles the active turn's read-only soft budget and
	// returns the new limit plus the tool-result text shown to the model.
	ExtendResearchBudget(reason string) (ResearchBudgetExtension, string, error)
}

type researchBudgetExtenderKey struct{}

// WithResearchBudgetExtender stamps ctx with the per-turn extender so the
// extend_research_budget tool can reach it from inside the run loop.
func WithResearchBudgetExtender(ctx context.Context, e ResearchBudgetExtender) context.Context {
	if e == nil {
		return ctx
	}
	return context.WithValue(ctx, researchBudgetExtenderKey{}, e)
}

// ResearchBudgetExtenderFromContext returns the per-turn extender, if any.
func ResearchBudgetExtenderFromContext(ctx context.Context) (ResearchBudgetExtender, bool) {
	e, ok := ctx.Value(researchBudgetExtenderKey{}).(ResearchBudgetExtender)
	return e, ok
}
