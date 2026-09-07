package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/evidence"
	"reasonix/internal/planmode"
	"reasonix/internal/tool"
)

func TestTodoWriteAcceptsLevels(t *testing.T) {
	args := json.RawMessage(`{"todos":[` +
		`{"content":"Phase","status":"pending","level":0},` +
		`{"content":"sub","status":"in_progress","level":1}]}`)
	if _, err := (todoWrite{}).Execute(context.Background(), args); err != nil {
		t.Fatalf("levels 0/1 should be accepted: %v", err)
	}
}

func TestTodoWriteRejectsBadLevel(t *testing.T) {
	args := json.RawMessage(`{"todos":[{"content":"x","status":"pending","level":2}]}`)
	_, err := (todoWrite{}).Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "level") {
		t.Fatalf("level 2 should be rejected with a level error, got %v", err)
	}
}

func TestTodoWriteRejectsNonSerialStates(t *testing.T) {
	for _, tc := range []struct {
		name string
		args string
		want string
	}{
		{
			name: "out of order completion",
			args: `{"todos":[{"content":"first","status":"in_progress"},{"content":"second","status":"completed"}]}`,
			want: "completed after unfinished",
		},
		{
			name: "multiple current items",
			args: `{"todos":[{"content":"first","status":"in_progress"},{"content":"second","status":"in_progress"}]}`,
			want: "second in_progress",
		},
		{
			name: "pending without current",
			args: `{"todos":[{"content":"first","status":"pending"}]}`,
			want: "no in_progress",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (todoWrite{}).Execute(context.Background(), json.RawMessage(tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("todo_write error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestTodoWriteAcceptsNewCompletedWithoutCompleteStepReceipt(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos:    []evidence.TodoItem{{Content: "Add parser", Status: "in_progress"}},
	})
	ctx := evidence.WithLedger(context.Background(), ledger)
	args := json.RawMessage(`{"todos":[{"content":"Add parser","status":"completed"}]}`)

	out, err := (todoWrite{}).Execute(ctx, args)
	if err != nil {
		t.Fatalf("new completion without complete_step should be accepted: %v", err)
	}
	if !strings.Contains(out, "1 completed") {
		t.Fatalf("todo_write output = %q, want 1 completed", out)
	}
}

func TestTodoWriteAcceptsNewCompletedWithCompleteStepReceipt(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos:    []evidence.TodoItem{{Content: "Add parser", Status: "in_progress"}},
	})
	ledger.Record(evidence.Receipt{ToolName: "complete_step", Success: true, Step: "Add parser"})
	ctx := evidence.WithLedger(context.Background(), ledger)
	args := json.RawMessage(`{"todos":[{"content":"Add parser","status":"completed"}]}`)

	if _, err := (todoWrite{}).Execute(ctx, args); err != nil {
		t.Fatalf("matching complete_step should authorize new completion: %v", err)
	}
}

func TestTodoWriteAcceptsInitialCompletedWithoutBaseline(t *testing.T) {
	ctx := evidence.WithLedger(context.Background(), evidence.NewLedger())
	args := json.RawMessage(`{"todos":[{"content":"Add parser","status":"completed"}]}`)

	if _, err := (todoWrite{}).Execute(ctx, args); err != nil {
		t.Fatalf("initial completed todo without baseline should be accepted: %v", err)
	}
}

func TestTodoWriteRejectsDroppingCurrentTodoWithoutReplacementAuth(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos: []evidence.TodoItem{
			{Content: "Inspect environment", Status: "in_progress"},
			{Content: "Write code", Status: "pending"},
		},
	})
	ctx := evidence.WithLedger(context.Background(), ledger)

	for _, args := range []string{
		`{"todos":[]}`,
		`{"todos":[{"content":"Write code","status":"in_progress"}]}`,
	} {
		_, err := (todoWrite{}).Execute(ctx, json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), "cannot be") {
			t.Fatalf("dropping current todo with %s should require replacement approval: %v", args, err)
		}
	}

	authorized := tool.WithPlanReplacementAuthorization(ctx)
	if _, err := (todoWrite{}).Execute(authorized, json.RawMessage(`{"todos":[{"content":"Write code","status":"in_progress"}]}`)); err != nil {
		t.Fatalf("approved replacement of the current todo should succeed: %v", err)
	}
	if _, err := (todoWrite{}).Execute(authorized, json.RawMessage(`{"todos":[]}`)); err != nil {
		t.Fatalf("approved clearing of an incomplete list should succeed: %v", err)
	}
}

func TestTodoWriteApprovedPlanReplacementPreservesCompletedHistory(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos: []evidence.TodoItem{
			{Content: "Inspect environment", Status: "completed"},
			{Content: "Implement parser", Status: "in_progress"},
		},
	})
	ctx := evidence.WithLedger(context.Background(), ledger)
	ctx = tool.WithPlanReplacementAuthorization(ctx)

	valid := json.RawMessage(`{"todos":[
		{"content":"Inspect environment","status":"completed"},
		{"content":"Replace parser architecture","status":"in_progress"}
	]}`)
	if _, err := (todoWrite{}).Execute(ctx, valid); err != nil {
		t.Fatalf("approved plan replacement should succeed: %v", err)
	}

	dropsHistory := json.RawMessage(`{"todos":[{"content":"Replace parser architecture","status":"in_progress"}]}`)
	if _, err := (todoWrite{}).Execute(ctx, dropsHistory); err == nil || !strings.Contains(err.Error(), "completed task history") {
		t.Fatalf("approved replacement dropped completed history: %v", err)
	}
}

func TestTodoWriteDoesNotTreatNumericContentAsStepIndex(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos: []evidence.TodoItem{
			{Content: "Finished", Status: "completed"},
			{Content: "2", Status: "in_progress"},
		},
	})
	ctx := evidence.WithLedger(context.Background(), ledger)
	args := json.RawMessage(`{"todos":[
		{"content":"Finished","status":"completed"},
		{"content":"Replacement","status":"in_progress"}
	]}`)

	if _, err := (todoWrite{}).Execute(ctx, args); err == nil || !strings.Contains(err.Error(), "cannot be removed or replaced") {
		t.Fatalf("numeric todo content should be matched by identity, got %v", err)
	}

	completeNumeric := json.RawMessage(`{"todos":[
		{"content":"Finished","status":"completed"},
		{"content":"2","status":"completed"}
	]}`)
	if _, err := (todoWrite{}).Execute(ctx, completeNumeric); err != nil {
		t.Fatalf("completing numeric todo content should not treat it as a step index: %v", err)
	}
}

func TestTodoWriteAllowsRephrasingCurrentTodo(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos:    []evidence.TodoItem{{Content: "Inspect environment", Status: "in_progress"}},
	})
	ctx := evidence.WithLedger(context.Background(), ledger)
	args := json.RawMessage(`{"todos":[{"content":"Inspect environment and dependencies","status":"in_progress"}]}`)
	if _, err := (todoWrite{}).Execute(ctx, args); err != nil {
		t.Fatalf("rephrasing the current todo should remain allowed: %v", err)
	}
}

func TestTodoWritePreservesCanonicalCompletedPrefixAcrossTurns(t *testing.T) {
	ctx := evidence.WithLedger(context.Background(), evidence.NewLedger())
	ctx = evidence.WithTodoState(ctx, []evidence.TodoItem{
		{Content: "Inspect environment", Status: "completed"},
		{Content: "Write code", Status: "in_progress"},
	})

	args := json.RawMessage(`{"todos":[
		{"content":"Inspect environment","status":"completed"},
		{"content":"Write code","status":"in_progress"}
	]}`)
	if _, err := (todoWrite{}).Execute(ctx, args); err != nil {
		t.Fatalf("cross-turn canonical prefix should remain valid: %v", err)
	}
}

func TestTodoWriteCanCompleteCanonicalCurrentAcrossTurns(t *testing.T) {
	ctx := evidence.WithLedger(context.Background(), evidence.NewLedger())
	ctx = evidence.WithTodoState(ctx, []evidence.TodoItem{
		{Content: "Inspect environment", Status: "in_progress"},
	})

	args := json.RawMessage(`{"todos":[{"content":"Inspect environment","status":"completed"}]}`)
	if _, err := (todoWrite{}).Execute(ctx, args); err != nil {
		t.Fatalf("cross-turn current todo completion should be accepted: %v", err)
	}
}

func TestTodoWriteRejectsDuplicatedOrReorderedCompletedPrefix(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos: []evidence.TodoItem{
			{Content: "Inspect environment", Status: "completed"},
			{Content: "Design solution", Status: "completed"},
			{Content: "Write code", Status: "in_progress"},
		},
	})
	ctx := evidence.WithLedger(context.Background(), ledger)

	for _, args := range []string{
		`{"todos":[
			{"content":"Inspect environment","status":"completed"},
			{"content":"Inspect environment","status":"completed"},
			{"content":"Write code","status":"in_progress"}
		]}`,
		`{"todos":[
			{"content":"Design solution","status":"completed"},
			{"content":"Inspect environment","status":"completed"},
			{"content":"Write code","status":"in_progress"}
		]}`,
	} {
		_, err := (todoWrite{}).Execute(ctx, json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), "cannot be inserted, duplicated, or reordered") {
			t.Fatalf("invalid completed prefix should be rejected: %v", err)
		}
	}
}

func TestTodoWriteCompletedPrefixErrorIncludesCanonicalSnapshot(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos: []evidence.TodoItem{
			{Content: "Inspect environment", Status: "completed", StepID: "inspect"},
			{Content: "Design solution", Status: "completed", StepID: "design"},
			{Content: "Write code", Status: "in_progress", StepID: "write"},
		},
	})
	ctx := evidence.WithLedger(context.Background(), ledger)
	args := json.RawMessage(`{"todos":[
		{"content":"Design solution","status":"completed","step_id":"design"},
		{"content":"Inspect environment","status":"completed","step_id":"inspect"},
		{"content":"Write code","status":"in_progress","step_id":"write"}
	]}`)

	_, err := (todoWrite{}).Execute(ctx, args)
	if err == nil {
		t.Fatal("reordered completed prefix should be rejected")
	}
	want := `canonical todo snapshot: [{"content":"Inspect environment","status":"completed","step_id":"inspect"},{"content":"Design solution","status":"completed","step_id":"design"},{"content":"Write code","status":"in_progress","step_id":"write"}]`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("todo_write error = %q, want recovery snapshot %q", err, want)
	}
}

func TestTodoWriteAcceptsCompletedAfterFailedCompleteStep(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos:    []evidence.TodoItem{{Content: "Add parser", Status: "in_progress"}},
	})
	ledger.Record(evidence.Receipt{ToolName: "complete_step", Success: false, Step: "Add parser"})
	ctx := evidence.WithLedger(context.Background(), ledger)
	args := json.RawMessage(`{"todos":[{"content":"Add parser","status":"completed"}]}`)

	if _, err := (todoWrite{}).Execute(ctx, args); err != nil {
		t.Fatalf("failed complete_step should not block todo progress: %v", err)
	}
}

func TestTodoWriteAcceptsCompletedWithoutProofBearingSignoff(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos:    []evidence.TodoItem{{Content: "Run project script", Status: "in_progress"}},
	})
	ledger.Record(evidence.Receipt{
		ToolName: "bash",
		Success:  true,
		Command:  `python "script.py"`,
	})
	ledger.Record(evidence.ReceiptFromToolCall("complete_step", json.RawMessage(`{
		"step":"Run project script",
		"result":"script ran",
		"evidence":[]
	}`), false, true))
	ctx := evidence.WithLedger(context.Background(), ledger)
	args := json.RawMessage(`{"todos":[{"content":"Run project script","status":"completed"}]}`)

	if _, err := (todoWrite{}).Execute(ctx, args); err != nil {
		t.Fatalf("todo progress should not wait for a proof-bearing complete_step: %v", err)
	}
}

func TestTodoWriteAcceptsCompletedWhenSignoffLacksResult(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos:    []evidence.TodoItem{{Content: "Run project script", Status: "in_progress"}},
	})
	ledger.Record(evidence.Receipt{
		ToolName: "bash",
		Success:  true,
		Command:  `python "script.py"`,
	})
	ledger.Record(evidence.ReceiptFromToolCall("complete_step", json.RawMessage(`{
		"step":"Run project script",
		"evidence":[{"kind":"manual","summary":"checked manually"}]
	}`), false, true))
	ctx := evidence.WithLedger(context.Background(), ledger)
	args := json.RawMessage(`{"todos":[{"content":"Run project script","status":"completed"}]}`)

	if _, err := (todoWrite{}).Execute(ctx, args); err != nil {
		t.Fatalf("todo progress should not wait for a complete_step result: %v", err)
	}
}

func TestTodoWriteCompletesWithoutSignoffRecoveryHatch(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos:    []evidence.TodoItem{{Content: "Run project script", Status: "in_progress"}},
	})
	ledger.Record(evidence.Receipt{
		ToolName: "bash",
		Success:  true,
		Command:  `python "script.py"`,
	})
	ledger.Record(evidence.ReceiptFromToolCall("complete_step", json.RawMessage(`{
		"step":"Run project script",
		"result":"script ran",
		"evidence":[{"kind":"verification","summary":"script completed","command":"python script.py"}]
	}`), false, true))
	ctx := evidence.WithLedger(context.Background(), ledger)
	args := json.RawMessage(`{"todos":[{"content":"Run project script","status":"completed"}]}`)

	if _, err := (todoWrite{}).Execute(ctx, args); err != nil {
		t.Fatalf("todo completion should not need the failed-signoff recovery hatch: %v", err)
	}
}

func TestTodoWriteCompletesWhenProgressIsAfterFailedCompleteStep(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos:    []evidence.TodoItem{{Content: "Run project script", Status: "in_progress"}},
	})
	ledger.Record(evidence.ReceiptFromToolCall("complete_step", json.RawMessage(`{
		"step":"Run project script",
		"result":"script ran",
		"evidence":[{"kind":"verification","summary":"script completed","command":"python other.py"}]
	}`), false, true))
	ledger.Record(evidence.Receipt{
		ToolName: "write_file",
		Success:  true,
		Paths:    []string{"docs/notes.md"},
		Write:    true,
	})
	ctx := evidence.WithLedger(context.Background(), ledger)
	args := json.RawMessage(`{"todos":[{"content":"Run project script","status":"completed"}]}`)

	if _, err := (todoWrite{}).Execute(ctx, args); err != nil {
		t.Fatalf("later progress should not be required to complete a todo: %v", err)
	}
}

func TestTodoWriteAcceptsPhaseChainProgress(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos: []evidence.TodoItem{
			{Content: "Port the parser", Status: "pending"},
			{Content: "move files", Status: "in_progress", Level: 1},
			{Content: "fix imports", Status: "pending", Level: 1},
		},
	})
	ctx := evidence.WithLedger(context.Background(), ledger)

	out, err := (todoWrite{}).Execute(ctx, json.RawMessage(`{"todos":[
		{"content":"Port the parser","status":"pending"},
		{"content":"move files","status":"in_progress","level":1},
		{"content":"fix imports","status":"pending","level":1},
		{"content":"update docs","status":"pending","level":1}]}`))
	if err != nil {
		t.Fatalf("narrowing work under the current phase should be accepted: %v", err)
	}
	if !strings.Contains(out, "in progress") {
		t.Fatalf("unexpected todo_write output: %q", out)
	}
}

func TestTodoWriteRejectsPhaseCompletedBeforeSubSteps(t *testing.T) {
	_, err := (todoWrite{}).Execute(context.Background(), json.RawMessage(`{"todos":[
		{"content":"Port the parser","status":"completed"},
		{"content":"move files","status":"in_progress","level":1}]}`))
	if err == nil || !strings.Contains(err.Error(), "unfinished") {
		t.Fatalf("phase completed before its sub-steps should be rejected: %v", err)
	}
}

func TestTodoWriteRejectsPhaseInProgressBeforeSubSteps(t *testing.T) {
	_, err := (todoWrite{}).Execute(context.Background(), json.RawMessage(`{"todos":[
		{"content":"Port the parser","status":"in_progress"},
		{"content":"move files","status":"pending","level":1}]}`))
	if err == nil || !strings.Contains(err.Error(), "cannot be in_progress while sub-step") {
		t.Fatalf("phase in_progress before its sub-steps finish should be rejected: %v", err)
	}
}

func TestTodoWriteRejectsOrphanSubStep(t *testing.T) {
	_, err := (todoWrite{}).Execute(context.Background(), json.RawMessage(`{"todos":[
		{"content":"move files","status":"in_progress","level":1},
		{"content":"Port the parser","status":"pending"}]}`))
	if err == nil || !strings.Contains(err.Error(), "no phase above it") {
		t.Fatalf("a level-1 sub-step with no phase should be rejected: %v", err)
	}
}

func TestTodoWriteRejectsReplacingActiveSubStepWithoutReplacementAuth(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos: []evidence.TodoItem{
			{Content: "Port the parser", Status: "pending"},
			{Content: "move files", Status: "in_progress", Level: 1},
			{Content: "fix imports", Status: "pending", Level: 1},
		},
	})
	ctx := evidence.WithLedger(context.Background(), ledger)
	args := json.RawMessage(`{"todos":[
		{"content":"Port the parser","status":"pending"},
		{"content":"rewrite everything","status":"in_progress","level":1}]}`)

	if _, err := (todoWrite{}).Execute(ctx, args); err == nil || !strings.Contains(err.Error(), "cannot be removed or replaced") {
		t.Fatalf("replacing the active sub-step should require replacement approval: %v", err)
	}
	if _, err := (todoWrite{}).Execute(tool.WithPlanReplacementAuthorization(ctx), args); err != nil {
		t.Fatalf("approved replacement of the active sub-step should succeed: %v", err)
	}
}

func TestTodoWriteAdvancesFiveItemListWithoutSignoff(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos: []evidence.TodoItem{
			{Content: "Remove debug files from git", Status: "in_progress", StepID: "cleanup_step_01"},
			{Content: "Clean leftover artifacts", Status: "pending", StepID: "cleanup_step_02"},
			{Content: "Update AGENTS.md", Status: "pending", StepID: "cleanup_step_03"},
			{Content: "Trim unused libraries", Status: "pending", StepID: "cleanup_step_04"},
			{Content: "Verify the build", Status: "pending", StepID: "cleanup_step_05"},
		},
	})
	ctx := evidence.WithLedger(context.Background(), ledger)
	args := json.RawMessage(`{"todos":[
		{"content":"Remove debug files from git","status":"completed","step_id":"cleanup_step_01"},
		{"content":"Clean leftover artifacts","status":"in_progress","step_id":"cleanup_step_02"},
		{"content":"Update AGENTS.md","status":"pending","step_id":"cleanup_step_03"},
		{"content":"Trim unused libraries","status":"pending","step_id":"cleanup_step_04"},
		{"content":"Verify the build","status":"pending","step_id":"cleanup_step_05"}
	]}`)

	out, err := (todoWrite{}).Execute(ctx, args)
	if err != nil {
		t.Fatalf("issue #9094 progress update should succeed without complete_step: %v", err)
	}
	if !strings.Contains(out, "1 completed") || !strings.Contains(out, "1 in progress") {
		t.Fatalf("todo_write output = %q, want 1 completed and 1 in progress", out)
	}
}

func TestTodoWriteRetitlesCompletedItemByStepID(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos: []evidence.TodoItem{
			{Content: "Remove debug files from git", Status: "in_progress", StepID: "cleanup_step_01"},
			{Content: "Clean leftover artifacts", Status: "pending", StepID: "cleanup_step_02"},
		},
	})
	ctx := evidence.WithLedger(context.Background(), ledger)
	args := json.RawMessage(`{"todos":[
		{"content":"Remove output/ debug files from git","status":"completed","step_id":"cleanup_step_01"},
		{"content":"Clean leftover artifacts","status":"in_progress","step_id":"cleanup_step_02"}
	]}`)

	if _, err := (todoWrite{}).Execute(ctx, args); err != nil {
		t.Fatalf("retitling a completed item by step_id should succeed: %v", err)
	}
}

func TestTodoWriteUpdatesProgressWhilePlanModeIsActive(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos: []evidence.TodoItem{
			{Content: "Inspect environment", Status: "in_progress"},
			{Content: "Draft a plan", Status: "pending"},
		},
	})
	ctx := planmode.WithActive(evidence.WithLedger(context.Background(), ledger), true)

	if _, err := (todoWrite{}).Execute(ctx, json.RawMessage(`{"todos":[
		{"content":"Inspect environment","status":"completed"},
		{"content":"Draft a plan","status":"in_progress"}
	]}`)); err != nil {
		t.Fatalf("plan mode should still accept todo progress: %v", err)
	}
	if _, err := (todoWrite{}).Execute(ctx, json.RawMessage(`{"todos":[]}`)); err == nil || !strings.Contains(err.Error(), "cannot be cleared") {
		t.Fatalf("plan mode should still require approval to clear the list: %v", err)
	}
}

func TestTodoWriteRejectsUnauthorizedCompletedHistoryRewrite(t *testing.T) {
	ledger := evidence.NewLedger()
	ledger.Record(evidence.Receipt{
		ToolName: "todo_write",
		Success:  true,
		Todos: []evidence.TodoItem{
			{Content: "Inspect environment", Status: "completed"},
			{Content: "Write code", Status: "in_progress"},
		},
	})
	ctx := evidence.WithLedger(context.Background(), ledger)
	args := json.RawMessage(`{"todos":[{"content":"Write code","status":"in_progress"}]}`)

	_, err := (todoWrite{}).Execute(ctx, args)
	if err == nil || !strings.Contains(err.Error(), "completed task history") {
		t.Fatalf("unauthorized drop of completed history should be rejected: %v", err)
	}
	want := `canonical todo snapshot: [{"content":"Inspect environment","status":"completed"},{"content":"Write code","status":"in_progress"}]`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("todo_write error = %q, want recovery snapshot %q", err, want)
	}
}
