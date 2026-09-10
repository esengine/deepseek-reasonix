package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/tool"
)

func init() { tool.RegisterBuiltin(extendResearchBudget{}) }

// extendResearchBudget doubles the read-only soft research budget for the
// active turn when the model judges that the investigation genuinely needs
// more rounds. The host only accepts it after the soft budget already nudged
// the turn toward convergence (10 -> 20 -> 40 -> 80, at most three times);
// outside that situation, or outside an agent turn, it fails closed without
// changing any state. It is host bookkeeping: it never needs approval and
// grants no permissions.
type extendResearchBudget struct{}

func (extendResearchBudget) Name() string { return "extend_research_budget" }

func (extendResearchBudget) Description() string {
	return "Ask the host to double this turn's read-only research budget when the soft-budget nudge fired but deeper investigation is genuinely warranted. Only available after the nudge (10 -> 20 -> 40 -> 80 rounds, at most three extensions per turn); the reason is required and recorded. If the investigation does not actually need more rounds, converge and report the evidence you already have instead of calling this."
}

func (extendResearchBudget) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "reason":{"type":"string","description":"REQUIRED. Why the investigation genuinely needs more rounds — what is still unknown and which concrete question the extra rounds will answer."}
},
"required":["reason"]
}`)
}

// ReadOnly is true: the call only adjusts in-turn host bookkeeping (the
// read-only round limit). It never touches files, permissions, or the sandbox.
func (extendResearchBudget) ReadOnly() bool { return true }

func (extendResearchBudget) ProviderVisible(ctx context.Context) bool {
	_, ok := tool.ResearchBudgetExtenderFromContext(ctx)
	return ok
}

// PlanModeSafe reports true: extending the read-only budget cannot mutate the
// workspace, and outside a nudged turn Execute fails closed anyway.
func (extendResearchBudget) PlanModeSafe() bool { return true }

func (extendResearchBudget) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid extend_research_budget args: %w", err)
	}
	reason := strings.TrimSpace(p.Reason)
	if reason == "" {
		return "", fmt.Errorf("extend_research_budget: reason is required — state what is still unknown and why more rounds will resolve it")
	}
	extender, ok := tool.ResearchBudgetExtenderFromContext(ctx)
	if !ok {
		return "", fmt.Errorf("extend_research_budget is only available after the read-only budget nudge in an active turn — no budget was changed")
	}
	result, text, err := extender.ExtendResearchBudget(reason)
	if err != nil {
		return "", err
	}
	if text != "" {
		return text, nil
	}
	return fmt.Sprintf("Read-only research budget doubled to %d rounds (%d extension(s) left this turn).", result.Rounds, result.Remaining), nil
}
