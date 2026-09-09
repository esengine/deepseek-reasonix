package agent

import (
	"testing"

	"reasonix/internal/provider"
)

func TestRebuildTodoStateScopesDuplicateIDsToTheirToolTurns(t *testing.T) {
	const sharedID = "call_0"
	todoCall := func(content string) provider.Message {
		return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: sharedID, Name: "todo_write",
			Arguments: `{"todos":[{"content":"` + content + `","status":"in_progress"}]}`,
		}}}
	}
	result := func(name, content string) provider.Message {
		return provider.Message{Role: provider.RoleTool, ToolCallID: sharedID, Name: name, Content: content}
	}

	tests := []struct {
		name    string
		msgs    []provider.Message
		want    string
		wantLen int
	}{
		{
			name: "later failed todo cannot replace earlier successful todo",
			msgs: []provider.Message{
				todoCall("keep"), result("todo_write", "Todos updated"),
				todoCall("reject"), result("todo_write", "error: invalid todo state"),
			},
			want: "keep", wantLen: 1,
		},
		{
			name: "later unrelated success cannot revive failed todo",
			msgs: []provider.Message{
				todoCall("reject"), result("todo_write", "error: invalid todo state"),
				{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: sharedID, Name: "read_file", Arguments: `{"path":"README.md"}`}}},
				result("read_file", "contents"),
			},
			wantLen: 0,
		},
		{
			name: "later successful todo remains the canonical base",
			msgs: []provider.Message{
				todoCall("reject"), result("todo_write", "error: invalid todo state"),
				todoCall("keep"), result("todo_write", "Todos updated"),
			},
			want: "keep", wantLen: 1,
		},
		{
			name: "later failure does not revoke earlier successful todo",
			msgs: []provider.Message{
				todoCall("keep"), result("todo_write", "Todos updated"),
				{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: sharedID, Name: "read_file", Arguments: `{"path":"missing"}`}}},
				result("read_file", "error: file not found"),
			},
			want: "keep", wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Agent{}
			a.rebuildTodoState(tt.msgs)
			todos := a.CanonicalTodoState()
			if len(todos) != tt.wantLen {
				t.Fatalf("rebuilt todos = %+v, want %d items", todos, tt.wantLen)
			}
			if tt.wantLen > 0 && todos[0].Content != tt.want {
				t.Fatalf("rebuilt todo = %+v, want content %q", todos[0], tt.want)
			}
		})
	}
}

func TestRebuildTodoStateDoesNotBorrowResultForMissingCall(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_0", Name: "todo_write",
			Arguments: `{"todos":[{"content":"unconfirmed","status":"in_progress"}]}`,
		}}},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_0", Name: "read_file", Arguments: `{"path":"README.md"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "read_file", Content: "contents"},
	}

	a := &Agent{}
	a.rebuildTodoState(msgs)
	if todos := a.CanonicalTodoState(); len(todos) != 0 {
		t.Fatalf("todo_write without its own result borrowed another turn's success: %+v", todos)
	}
}

func TestRebuildTodoStateDoesNotAdvanceFailedDuplicateIDStep(t *testing.T) {
	msgs := []provider.Message{
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
			ID: "call_0", Name: "read_file", Arguments: `{"path":"README.md"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "read_file", Content: "contents"},
	}

	a := &Agent{}
	a.rebuildTodoState(msgs)
	if todos := a.CanonicalTodoState(); len(todos) != 1 || todos[0].Status != "in_progress" {
		t.Fatalf("failed complete_step borrowed another turn's success: %+v", todos)
	}
}

func TestRebuildTodoStateIgnoresNormalizedInterruptedResults(t *testing.T) {
	t.Run("todo_write", func(t *testing.T) {
		raw := []provider.Message{
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
				ID: "todo", Name: "todo_write",
				Arguments: `{"todos":[{"content":"unconfirmed","status":"in_progress"}]}`,
			}}},
		}
		normalized := provider.NormalizeSessionMessages(raw)
		if len(normalized) == len(raw) {
			t.Fatal("NormalizeSessionMessages did not insert an interrupted result")
		}

		a := &Agent{}
		a.rebuildTodoState(normalized)
		if todos := a.CanonicalTodoState(); len(todos) != 0 {
			t.Fatalf("interrupted todo_write rebuilt canonical state: %+v", todos)
		}
	})

	t.Run("complete_step", func(t *testing.T) {
		raw := []provider.Message{
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
				ID: "todo", Name: "todo_write",
				Arguments: `{"todos":[{"content":"ship","status":"in_progress"}]}`,
			}}},
			{Role: provider.RoleTool, ToolCallID: "todo", Name: "todo_write", Content: "Todos updated"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
				ID: "step", Name: "complete_step", Arguments: `{"step":"ship"}`,
			}}},
		}
		normalized := provider.NormalizeSessionMessages(raw)
		if len(normalized) == len(raw) {
			t.Fatal("NormalizeSessionMessages did not insert an interrupted result")
		}

		a := &Agent{}
		a.rebuildTodoState(normalized)
		if todos := a.CanonicalTodoState(); len(todos) != 1 || todos[0].Status != "in_progress" {
			t.Fatalf("interrupted complete_step advanced canonical state: %+v", todos)
		}
	})
}

func TestRebuildTodoStateIgnoresCancelledBeforeExecutionWithEmptyIDs(t *testing.T) {
	const cancelledBeforeExecution = "cancelled: context cancelled before execution"

	t.Run("todo_write", func(t *testing.T) {
		msgs := []provider.Message{
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
				Name:      "todo_write",
				Arguments: `{"todos":[{"content":"unconfirmed","status":"in_progress"}]}`,
			}}},
			{Role: provider.RoleTool, Name: "todo_write", Content: cancelledBeforeExecution},
		}

		a := &Agent{}
		a.rebuildTodoState(msgs)
		if todos := a.CanonicalTodoState(); len(todos) != 0 {
			t.Fatalf("cancelled empty-ID todo_write rebuilt canonical state: %+v", todos)
		}
	})

	t.Run("complete_step", func(t *testing.T) {
		msgs := []provider.Message{
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
				ID: "todo", Name: "todo_write",
				Arguments: `{"todos":[{"content":"ship","status":"in_progress"}]}`,
			}}},
			{Role: provider.RoleTool, ToolCallID: "todo", Name: "todo_write", Content: "Todos updated"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
				Name: "complete_step", Arguments: `{"step":"ship"}`,
			}}},
			{Role: provider.RoleTool, Name: "complete_step", Content: " \n" + cancelledBeforeExecution + "\t"},
		}

		a := &Agent{}
		a.rebuildTodoState(msgs)
		if todos := a.CanonicalTodoState(); len(todos) != 1 || todos[0].Status != "in_progress" {
			t.Fatalf("cancelled empty-ID complete_step advanced canonical state: %+v", todos)
		}
	})
}

func TestSetSessionAndRebuildTodoStateScopeDuplicateIDs(t *testing.T) {
	s := NewSession("")
	for _, msg := range []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call_0", Name: "bash", Arguments: `{"command":"go test ./..."}`}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "bash", Content: "ok"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "todo", Name: "todo_write",
			Arguments: `{"todos":[{"content":"ship","status":"in_progress"}]}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "todo", Name: "todo_write", Content: "Todos updated"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call_0", Name: "complete_step", Arguments: `{"step":"ship"}`}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "complete_step", Content: "error: no evidence"},
	} {
		s.Add(msg)
	}

	a := &Agent{}
	a.SetSession(s)
	if todos := a.CanonicalTodoState(); len(todos) != 1 || todos[0].Status != "in_progress" {
		t.Fatalf("SetSession rebuilt failed duplicate-ID step as successful: %+v", todos)
	}

	a.setTodoState(nil)
	a.RebuildTodoState()
	if todos := a.CanonicalTodoState(); len(todos) != 1 || todos[0].Status != "in_progress" {
		t.Fatalf("RebuildTodoState rebuilt failed duplicate-ID step as successful: %+v", todos)
	}
}

func TestRebuildTodoStatePreservesUniqueIDSemantics(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "todo-unique", Name: "todo_write",
			Arguments: `{"todos":[{"content":"a","status":"in_progress"},{"content":"b","status":"pending"}]}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "todo-unique", Name: "todo_write", Content: "Todos updated"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "step-unique", Name: "complete_step", Arguments: `{"step":"a"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "step-unique", Name: "complete_step", Content: "signed off"},
	}

	a := &Agent{}
	a.rebuildTodoState(msgs)
	if todos := a.CanonicalTodoState(); len(todos) != 2 || todos[0].Status != "completed" || todos[1].Status != "in_progress" {
		t.Fatalf("unique-ID replay changed: %+v", todos)
	}
}

func TestRebuildTodoStatePreservesSameBatchReplaySemantics(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "step", Name: "complete_step", Arguments: `{"step":"ship"}`},
			{ID: "todo", Name: "todo_write", Arguments: `{"todos":[{"content":"ship","status":"in_progress"}]}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "step", Name: "complete_step", Content: "signed off"},
		{Role: provider.RoleTool, ToolCallID: "todo", Name: "todo_write", Content: "Todos updated"},
	}

	a := &Agent{}
	a.rebuildTodoState(msgs)
	if todos := a.CanonicalTodoState(); len(todos) != 1 || todos[0].Status != "completed" {
		t.Fatalf("same-batch complete_step replay changed: %+v", todos)
	}
}

func TestRebuildTodoStateKeepsStepIdentityAcrossDuplicateCallIDs(t *testing.T) {
	const sharedID = "call_0"
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: sharedID, Name: "todo_write",
			Arguments: `{"todos":[{"content":"first","status":"completed","step_id":"plan_step_01"},{"content":"target","status":"in_progress","step_id":"plan_step_02"}]}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: sharedID, Name: "todo_write", Content: "Todos updated"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: sharedID, Name: "complete_step",
			Arguments: `{"step":"1","step_id":"plan_step_02"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: sharedID, Name: "complete_step", Content: "signed off"},
	}

	a := &Agent{}
	a.rebuildTodoState(msgs)
	todos := a.CanonicalTodoState()
	if len(todos) != 2 || todos[0].Status != "completed" || todos[1].Status != "completed" {
		t.Fatalf("step identity did not override conflicting position across duplicate call IDs: %+v", todos)
	}
	if todos[0].StepID != "plan_step_01" || todos[1].StepID != "plan_step_02" {
		t.Fatalf("step identity was not preserved during replay: %+v", todos)
	}
}
