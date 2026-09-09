package control

import (
	"context"
	"errors"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func TestRepeatedCompleteWithOnlyProjectCheckFinishes(t *testing.T) {
	g := &goalMachine{goal: "audit the tree", status: GoalStatusRunning, turnsLimit: unlimitedGoalTurns}
	in := goalAdvanceInput{
		report: &goalTurnReport{status: GoalStatusComplete},
		readiness: agent.ReadinessResult{
			Ready:   false,
			Missing: []string{"project_check"},
			Reason:  `run "godot --headless -s e2e.gd" from AGENTS.md:4 after the latest write`,
		},
	}
	first := g.advance(in)
	if !first.cont || g.status != GoalStatusRunning {
		t.Fatalf("first complete should continue: result=%+v status=%s", first, g.status)
	}
	if !strings.Contains(first.intercept, "unverified") {
		t.Fatalf("first intercept = %q, want the unverified escape hatch", first.intercept)
	}
	second := g.advance(in)
	if second.cont || g.status != GoalStatusComplete || second.notice != goalCompleteNotice {
		t.Fatalf("second identical check-only complete should finish: result=%+v status=%s", second, g.status)
	}
}

func TestRepeatedCompleteWithOnlyVerificationFinishes(t *testing.T) {
	g := &goalMachine{goal: "audit the tree", status: GoalStatusRunning, turnsLimit: unlimitedGoalTurns}
	in := goalAdvanceInput{
		report: &goalTurnReport{status: GoalStatusComplete},
		readiness: agent.ReadinessResult{
			Ready:   false,
			Missing: []string{"verification"},
			Reason:  "run a relevant verification command after the latest write for the current role setting",
		},
	}
	if first := g.advance(in); !first.cont || g.status != GoalStatusRunning {
		t.Fatalf("first complete should continue: result=%+v status=%s", first, g.status)
	}
	if second := g.advance(in); second.cont || g.status != GoalStatusComplete {
		t.Fatalf("second identical verification gap should finish: result=%+v status=%s", second, g.status)
	}
}

func TestRejectedCompleteDoesNotCountAsProgress(t *testing.T) {
	g := &goalMachine{goal: "audit the tree", status: GoalStatusRunning, turnsLimit: unlimitedGoalTurns}
	in := goalAdvanceInput{
		report:    &goalTurnReport{status: GoalStatusComplete},
		readiness: agent.ReadinessResult{Ready: false, Missing: []string{"verification"}, Reason: "run a relevant verification command"},
	}
	g.advance(in)
	if g.noProgressTurns != 1 {
		t.Fatalf("rejected complete noProgressTurns = %d, want 1", g.noProgressTurns)
	}
}

// A failed complete_step and a later successful command deliberately reuse the
// same provider id. The session replay must keep the failure attached to its
// own assistant turn; otherwise the canonical todo becomes completed, final
// readiness becomes ready, and Goal's repeated-complete escape can stop early.
func TestCollidingToolCallIDCannotUnlockRepeatedGoalComplete(t *testing.T) {
	sess := agent.NewSession("")
	for _, msg := range []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "todo", Name: "todo_write",
			Arguments: `{"todos":[{"content":"ship","status":"in_progress"}]}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "todo", Name: "todo_write", Content: "Todos updated"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_0", Name: "complete_step", Arguments: `{"step":"ship"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "complete_step", Content: "error: no evidence"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_0", Name: "bash", Arguments: `{"command":"go test ./..."}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "bash", Content: "PASS"},
	} {
		sess.Add(msg)
	}

	reg := tool.NewRegistry()
	reg.Add(minimalFakeTool{name: "bash"})
	prov := &scriptedTurns{turns: [][]provider.Chunk{
		{toolCallChunk("call_0", "bash", `{"command":"go test ./..."}`), {Type: provider.ChunkDone}},
		textTurn("complete"),
	}}
	exec := agent.New(prov, reg, sess, agent.Options{}, event.Discard)
	exec.SetSession(sess)

	todos := exec.CanonicalTodoState()
	if len(todos) != 1 || todos[0].Status != "in_progress" {
		t.Fatalf("colliding success advanced the failed signoff: %+v", todos)
	}
	var readinessErr *agent.FinalReadinessError
	if err := exec.Run(context.Background(), "verify and finish"); !errors.As(err, &readinessErr) {
		t.Fatalf("Run error = %v, want final-readiness rejection", err)
	}
	readiness := exec.ReadinessResult()
	if readiness.Ready || !containsGoalMissing(readiness.Missing, "todo") {
		t.Fatalf("readiness = %+v, want canonical todo debt", readiness)
	}

	g := &goalMachine{goal: "ship", status: GoalStatusRunning, turnsLimit: unlimitedGoalTurns}
	in := goalAdvanceInput{
		report:    &goalTurnReport{status: GoalStatusComplete},
		readiness: readiness,
		todos:     exec.CanonicalTodoState(),
	}
	for turn := 1; turn <= 2; turn++ {
		if res := g.advance(in); !res.cont || g.status != GoalStatusRunning {
			t.Fatalf("complete claim %d stopped with colliding evidence: result=%+v status=%s", turn, res, g.status)
		}
	}
}

func containsGoalMissing(missing []string, want string) bool {
	for _, got := range missing {
		if got == want {
			return true
		}
	}
	return false
}
