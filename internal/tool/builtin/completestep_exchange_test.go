package builtin

import (
	"context"
	"strings"
	"testing"

	"reasonix/internal/evidence"
	"reasonix/internal/provider"
)

func TestVerifyCommandFromSessionScopesReusedIDsToTheirTurns(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_0", Name: "bash", Arguments: `{"command":"go test ./internal/evidence"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "bash", Content: "ok\nPASS"},
		{Role: provider.RoleUser, Content: "continue"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_0", Name: "bash", Arguments: `{"command":"go test ./broken"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "bash", Content: "error: exit status 1"},
	}
	ctx := evidence.WithSessionMessages(context.Background(), func() []provider.Message { return msgs })

	if !verifyCommandFromSession(ctx, "go test ./internal/evidence") {
		t.Fatal("a later failure with the same tool_call_id must not erase an earlier successful command")
	}
	if verifyCommandFromSession(ctx, "go test ./broken") {
		t.Fatal("the failed command must not borrow an earlier success with the same tool_call_id")
	}
}

func TestVerifyCommandFromSessionKeepsLaterSuccessSeparate(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_0", Name: "bash", Arguments: `{"command":"go test ./broken"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "bash", Content: "blocked: denied"},
		{Role: provider.RoleUser, Content: "run the focused test"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_0", Name: "bash", Arguments: `{"command":"go test ./internal/tool"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "bash", Content: "ok\nPASS"},
	}
	ctx := evidence.WithSessionMessages(context.Background(), func() []provider.Message { return msgs })

	if verifyCommandFromSession(ctx, "go test ./broken") {
		t.Fatal("a later success with the same tool_call_id must not revive an earlier failed command")
	}
	if !verifyCommandFromSession(ctx, "go test ./internal/tool") {
		t.Fatal("the later explicitly successful command should remain valid evidence")
	}
}

func TestVerifyCommandFromSessionScopesNamedTools(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "shared", Name: "git_status", Arguments: `{}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "shared", Name: "git_status", Content: "clean"},
		{Role: provider.RoleUser, Content: "inspect something else"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "shared", Name: "read_file", Arguments: `{"path":"missing.go"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "shared", Name: "read_file", Content: "error: missing"},
	}
	ctx := evidence.WithSessionMessages(context.Background(), func() []provider.Message { return msgs })

	if !verifyCommandFromSession(ctx, "git_status") {
		t.Fatal("a later failed call with the same ID must not invalidate a successful named tool")
	}
	if verifyCommandFromSession(ctx, "read_file missing.go") {
		t.Fatal("the failed named tool must not borrow the earlier named tool result")
	}
}

func TestVerifyCommandFromSessionDoesNotBorrowMissingOrOrphanResults(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleTool, ToolCallID: "shared", Name: "bash", Content: "orphan success"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "shared", Name: "bash", Arguments: `{"command":"go test ./missing-result"}`,
		}}},
		{Role: provider.RoleUser, Content: "new turn"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "shared", Name: "bash", Arguments: `{"command":"go test ./paired"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "shared", Name: "bash", Content: "PASS"},
	}
	ctx := evidence.WithSessionMessages(context.Background(), func() []provider.Message { return msgs })

	if verifyCommandFromSession(ctx, "go test ./missing-result") {
		t.Fatal("a call without a result must not borrow an orphan or a later turn's result")
	}
	if !verifyCommandFromSession(ctx, "go test ./paired") {
		t.Fatal("the explicitly paired command should remain valid")
	}
}

func TestVerifyCommandFromSessionPreservesWithinTurnOutOfOrderResults(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "first", Name: "bash", Arguments: `{"command":"go test ./first"}`},
			{ID: "second", Name: "bash", Arguments: `{"command":"go test ./second"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "second", Name: "bash", Content: "PASS"},
		{Role: provider.RoleTool, ToolCallID: "first", Name: "bash", Content: "PASS"},
	}
	ctx := evidence.WithSessionMessages(context.Background(), func() []provider.Message { return msgs })

	for _, command := range []string{"go test ./first", "go test ./second"} {
		if !verifyCommandFromSession(ctx, command) {
			t.Fatalf("out-of-order result for %q should remain paired by its unique ID", command)
		}
	}
}

func TestAllCommandHintsScopesReusedIDsToTheirTurns(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_0", Name: "bash", Arguments: `{"command":"go test ./successful/..."}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "bash", Content: "PASS"},
		{Role: provider.RoleUser, Content: "continue"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_0", Name: "bash", Arguments: `{"command":"go test ./failed/..."}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "bash", Content: "error: FAIL"},
	}
	ctx := evidence.WithSessionMessages(context.Background(), func() []provider.Message { return msgs })

	hint := allCommandHints(ctx, evidence.NewLedger())
	if !strings.Contains(hint, "go test ./successful/...") {
		t.Fatalf("hint should keep the successful command despite a later ID collision, got %q", hint)
	}
	if strings.Contains(hint, "go test ./failed/...") {
		t.Fatalf("hint must not list the failed colliding command, got %q", hint)
	}
}

func TestCancelledBeforeExecutionCannotVerifyOrHintCommand(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "cancelled", Name: "bash", Arguments: `{"command":"go test ./unrun/..."}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "cancelled", Name: "bash", Content: " cancelled: context cancelled before execution\n"},
	}
	ctx := evidence.WithSessionMessages(context.Background(), func() []provider.Message { return msgs })

	if verifyCommandFromSession(ctx, "go test ./unrun/...") {
		t.Fatal("a command cancelled before execution must not count as verification")
	}
	if hint := allCommandHints(ctx, evidence.NewLedger()); strings.Contains(hint, "go test ./unrun/...") {
		t.Fatalf("a command cancelled before execution must not be offered as a successful hint, got %q", hint)
	}
}
