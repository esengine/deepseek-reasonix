package agent

import (
	"strings"
	"testing"

	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/provider"
)

func TestFinalReadinessFallsBackToCanonicalTodos(t *testing.T) {
	ran := evidence.Receipt{ToolName: "bash", Success: true, Write: true, Mutation: true, Command: "git push"}
	open := []evidence.TodoItem{{Content: "push", Status: "completed"}, {Content: "rebase", Status: "pending"}}

	// Turn did work (a successful bash) but issued no todo_write this turn, so the
	// per-turn ledger has no list — the canonical state must still gate inside a
	// closed-loop turn.
	a := &Agent{task: taskRuntime{ledger: readinessLedger(ran)}, sess: sessionRuntime{todoState: open}, turn: turnRuntime{deliveryScopeActive: true}}
	if got := a.ReadinessResult(); !strings.Contains(got.Reason, "incomplete") {
		t.Fatalf("cross-turn gate = %q, want it to report incomplete canonical todos", got.Reason)
	}

	// A turn that touched nothing (pure Q&A) must never gate on stale canonical state.
	idle := &Agent{task: taskRuntime{ledger: evidence.NewLedger()}, sess: sessionRuntime{todoState: open}, turn: turnRuntime{deliveryScopeActive: true}}
	if got := idle.ReadinessResult(); !got.Ready {
		t.Fatalf("no-work turn gated on canonical todos: %+v", got)
	}

	// All canonical items completed → no gate even after work.
	done := &Agent{task: taskRuntime{ledger: readinessLedger(ran)}, sess: sessionRuntime{todoState: []evidence.TodoItem{{Content: "push", Status: "completed"}}}, turn: turnRuntime{deliveryScopeActive: true}}
	if got := done.ReadinessResult(); !got.Ready {
		t.Fatalf("completed canonical todos still gated: %+v", got)
	}
}

func TestAdvanceCanonicalTodoCompletesAndPromotes(t *testing.T) {
	a := &Agent{
		svc: agentServices{sink: event.Discard},
		sess: sessionRuntime{todoState: []evidence.TodoItem{
			{Content: "sync branch", Status: "in_progress"},
			{Content: "push to origin", Status: "pending"},
			{Content: "rebase", Status: "pending"},
		}},
	}
	a.advanceCanonicalTodo("sync branch")

	if a.sess.todoState[0].Status != "completed" {
		t.Fatalf("signed-off item not completed: %+v", a.sess.todoState[0])
	}
	if a.sess.todoState[1].Status != "in_progress" {
		t.Fatalf("next pending item not promoted: %+v", a.sess.todoState[1])
	}
	if a.sess.todoState[2].Status != "pending" {
		t.Fatalf("a later item was promoted out of order: %+v", a.sess.todoState[2])
	}
}

func TestAdvanceCanonicalTodoRejectsPendingMatchByNumber(t *testing.T) {
	a := &Agent{svc: agentServices{sink: event.Discard}, sess: sessionRuntime{todoState: []evidence.TodoItem{
		{Content: "first", Status: "in_progress"},
		{Content: "second", Status: "pending"},
	}}}
	a.advanceCanonicalTodo("2")
	if a.sess.todoState[1].Status != "pending" || a.sess.todoState[0].Status != "in_progress" {
		t.Fatalf("pending numeric step advanced out of order: %+v", a.sess.todoState)
	}
}

func TestRebuildTodoStateIgnoresHistoricalPendingSignoff(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "t1", Name: "todo_write",
			Arguments: `{"todos":[{"content":"a","status":"in_progress"},{"content":"b","status":"pending"}]}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "t1", Name: "todo_write", Content: "Todos updated"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "c1", Name: "complete_step", Arguments: `{"step":"b"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "c1", Name: "complete_step", Content: "signed off"},
	}
	a := &Agent{}
	a.rebuildTodoState(msgs)
	if a.sess.todoState[0].Status != "in_progress" || a.sess.todoState[1].Status != "pending" {
		t.Fatalf("historical pending signoff restored invalid state: %+v", a.sess.todoState)
	}
}

func TestSetTodoStateNormalizesLegacyOutOfOrderSnapshot(t *testing.T) {
	a := &Agent{}
	a.setTodoState([]evidence.TodoItem{
		{Content: "first", Status: "in_progress"},
		{Content: "second", Status: "completed"},
	})
	if a.sess.todoState[0].Status != "in_progress" || a.sess.todoState[1].Status != "pending" {
		t.Fatalf("legacy snapshot was not normalized: %+v", a.sess.todoState)
	}
}

func TestRebuildTodoStateReplaysCompleteSteps(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "t1", Name: "todo_write",
			Arguments: `{"todos":[{"content":"a","status":"in_progress"},{"content":"b","status":"pending"}]}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "t1", Name: "todo_write", Content: "Todos updated"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "c1", Name: "complete_step", Arguments: `{"step":"a"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "c1", Name: "complete_step", Content: "signed off"},
	}
	a := &Agent{}
	a.rebuildTodoState(msgs)

	if len(a.sess.todoState) != 2 {
		t.Fatalf("rebuilt %d todos, want 2", len(a.sess.todoState))
	}
	if a.sess.todoState[0].Status != "completed" {
		t.Fatalf("complete_step not replayed onto canonical state: %+v", a.sess.todoState[0])
	}
	if a.sess.todoState[1].Status != "in_progress" {
		t.Fatalf("next item not promoted on replay: %+v", a.sess.todoState[1])
	}
}

func TestRebuildTodoStateSkipsFailedCompleteStep(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "t1", Name: "todo_write",
			Arguments: `{"todos":[{"content":"a","status":"in_progress"}]}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "t1", Name: "todo_write", Content: "Todos updated"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "c1", Name: "complete_step", Arguments: `{"step":"a"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "c1", Name: "complete_step", Content: "error: no evidence"},
	}
	a := &Agent{}
	a.rebuildTodoState(msgs)

	if a.sess.todoState[0].Status == "completed" {
		t.Fatalf("a failed complete_step must not advance canonical state: %+v", a.sess.todoState[0])
	}
}

func TestRebuildTodoStateRequiresToolResults(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "t1", Name: "todo_write",
			Arguments: `{"todos":[{"content":"a","status":"in_progress"}]}`,
		}}},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "c1", Name: "complete_step", Arguments: `{"step":"a"}`,
		}}},
	}
	a := &Agent{}
	a.rebuildTodoState(msgs)
	if len(a.sess.todoState) != 0 {
		t.Fatalf("todo_write without tool result rebuilt canonical state: %+v", a.sess.todoState)
	}

	msgs = append(msgs[:1],
		provider.Message{Role: provider.RoleTool, ToolCallID: "t1", Name: "todo_write", Content: "Todos updated"},
		msgs[1],
	)
	a.rebuildTodoState(msgs)
	if len(a.sess.todoState) != 1 {
		t.Fatalf("successful todo_write did not rebuild canonical state: %+v", a.sess.todoState)
	}
	if got := a.sess.todoState[0].Status; got != "in_progress" {
		t.Fatalf("complete_step without tool result changed status to %q", got)
	}
}

func TestRebuildTodoStateHonorsEmptyTodoWriteClear(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID:        "t1",
			Name:      "todo_write",
			Arguments: `{"todos":[{"content":"a","status":"in_progress"}]}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "t1", Name: "todo_write", Content: "Todos updated"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID:        "t2",
			Name:      "todo_write",
			Arguments: `{"todos":[]}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "t2", Name: "todo_write", Content: "Todos updated"},
	}
	a := &Agent{}
	a.rebuildTodoState(msgs)
	if len(a.sess.todoState) != 0 {
		t.Fatalf("empty todo_write should clear rebuilt canonical state: %+v", a.sess.todoState)
	}
}

func TestSeedTodoState(t *testing.T) {
	a := &Agent{svc: agentServices{sink: event.Discard}}
	todos := []evidence.TodoItem{
		{Content: "step 1", Status: "in_progress"},
		{Content: "step 2", Status: "pending"},
	}
	a.SeedTodoState(todos)
	if len(a.sess.todoState) != 2 {
		t.Fatalf("SeedTodoState: got %d items, want 2", len(a.sess.todoState))
	}
	if a.sess.todoState[0].Status != "in_progress" {
		t.Fatalf("SeedTodoState: first item status = %q, want in_progress", a.sess.todoState[0].Status)
	}
}

func TestSeedTodoStateReplacesExisting(t *testing.T) {
	a := &Agent{svc: agentServices{sink: event.Discard}, sess: sessionRuntime{todoState: []evidence.TodoItem{
		{Content: "existing", Status: "in_progress"},
	}}}
	a.SeedTodoState([]evidence.TodoItem{
		{Content: "new", Status: "in_progress"},
	})
	if len(a.sess.todoState) != 1 || a.sess.todoState[0].Content != "new" {
		t.Fatalf("SeedTodoState did not replace existing state: %+v", a.sess.todoState)
	}
}

func TestSeedTodoStateAllowsAdvanceAfterSeed(t *testing.T) {
	a := &Agent{svc: agentServices{sink: event.Discard}}
	a.SeedTodoState([]evidence.TodoItem{
		{Content: "step 1", Status: "in_progress"},
		{Content: "step 2", Status: "pending"},
	})
	a.advanceCanonicalTodo("step 1")
	if a.sess.todoState[0].Status != "completed" {
		t.Fatalf("advance after seed: item 0 status = %q, want completed", a.sess.todoState[0].Status)
	}
	if a.sess.todoState[1].Status != "in_progress" {
		t.Fatalf("advance after seed: item 1 status = %q, want in_progress", a.sess.todoState[1].Status)
	}
}

func TestAdvanceCanonicalTodoWalksPhaseChain(t *testing.T) {
	a := &Agent{svc: agentServices{sink: event.Discard}, sess: sessionRuntime{todoState: []evidence.TodoItem{
		{Content: "Port the parser", Status: "pending"},
		{Content: "move files", Status: "in_progress", Level: 1},
		{Content: "fix imports", Status: "pending", Level: 1},
		{Content: "Ship it", Status: "pending"},
		{Content: "run tests", Status: "pending", Level: 1},
	}}}

	a.advanceCanonicalTodo("Port the parser")
	if a.sess.todoState[0].Status != "pending" {
		t.Fatalf("pending phase advanced ahead of its sub-steps: %+v", a.sess.todoState)
	}

	a.advanceCanonicalTodo("move files")
	if a.sess.todoState[1].Status != "completed" || a.sess.todoState[2].Status != "in_progress" {
		t.Fatalf("completing a sub-step should promote its sibling: %+v", a.sess.todoState)
	}

	a.advanceCanonicalTodo("fix imports")
	if a.sess.todoState[2].Status != "completed" || a.sess.todoState[0].Status != "in_progress" {
		t.Fatalf("after the last sub-step the phase should become the signable item: %+v", a.sess.todoState)
	}

	a.advanceCanonicalTodo("Port the parser")
	if a.sess.todoState[0].Status != "completed" || a.sess.todoState[3].Status != "pending" || a.sess.todoState[4].Status != "in_progress" {
		t.Fatalf("phase sign-off should promote the next phase's first sub-step: %+v", a.sess.todoState)
	}
}

func TestAcceptTodoWriteDefersAndConsumesContiguousCompletions(t *testing.T) {
	a := &Agent{
		svc: agentServices{sink: event.Discard},
		sess: sessionRuntime{todoState: []evidence.TodoItem{
			{Content: "A", Status: "in_progress", StepID: "a"},
			{Content: "B", Status: "pending", StepID: "b"},
			{Content: "C", Status: "pending", StepID: "c"},
		}},
	}
	early := []evidence.TodoItem{
		{Content: "A", Status: "in_progress", StepID: "a"},
		{Content: "B", Status: "completed", StepID: "b"},
		{Content: "C", Status: "completed", StepID: "c"},
	}

	got := a.acceptTodoWrite(early)
	if got[0].Status != "in_progress" || got[1].Status != "pending" || got[2].Status != "pending" {
		t.Fatalf("early completion was not canonicalized: %+v", got)
	}
	if len(a.sess.deferredTodoCompletions) != 2 {
		t.Fatalf("deferred completions = %v, want two entries", a.sess.deferredTodoCompletions)
	}

	// Replaying the same model update must not create duplicate facts.
	a.acceptTodoWrite(early)
	if len(a.sess.deferredTodoCompletions) != 2 {
		t.Fatalf("replayed update changed deferred cardinality: %v", a.sess.deferredTodoCompletions)
	}

	a.advanceCanonicalTodo("A")
	for i, todo := range a.sess.todoState {
		if todo.Status != "completed" {
			t.Fatalf("todo %d after boundary consumption = %+v, want completed", i+1, todo)
		}
	}
	if len(a.sess.deferredTodoCompletions) != 0 {
		t.Fatalf("deferred completions remained after contiguous consumption: %v", a.sess.deferredTodoCompletions)
	}
}

func TestAcceptTodoWriteDoesNotSkipAnUnfinishedTodo(t *testing.T) {
	a := &Agent{
		svc: agentServices{sink: event.Discard},
		sess: sessionRuntime{todoState: []evidence.TodoItem{
			{Content: "A", Status: "in_progress", StepID: "a"},
			{Content: "B", Status: "pending", StepID: "b"},
			{Content: "D", Status: "pending", StepID: "d"},
			{Content: "E", Status: "pending", StepID: "e"},
		}},
	}
	a.acceptTodoWrite([]evidence.TodoItem{
		{Content: "A", Status: "in_progress", StepID: "a"},
		{Content: "B", Status: "completed", StepID: "b"},
		{Content: "D", Status: "pending", StepID: "d"},
		{Content: "E", Status: "completed", StepID: "e"},
	})

	a.advanceCanonicalTodo("A")
	if a.sess.todoState[1].Status != "completed" || a.sess.todoState[2].Status != "in_progress" || a.sess.todoState[3].Status != "pending" {
		t.Fatalf("consumption crossed unfinished D: %+v", a.sess.todoState)
	}
	if len(a.sess.deferredTodoCompletions) != 1 {
		t.Fatalf("deferred E was lost after B: %v", a.sess.deferredTodoCompletions)
	}

	a.advanceCanonicalTodo("D")
	if a.sess.todoState[3].Status != "completed" || len(a.sess.deferredTodoCompletions) != 0 {
		t.Fatalf("deferred E was not consumed at its boundary: todos=%+v deferred=%v", a.sess.todoState, a.sess.deferredTodoCompletions)
	}
}
