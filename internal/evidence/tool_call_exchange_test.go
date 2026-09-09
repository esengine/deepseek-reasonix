package evidence

import (
	"testing"

	"reasonix/internal/provider"
)

func TestPathsProvenInSessionScopesResultsToTheirToolTurn(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_0", Name: "read_file", Arguments: `{"path":"earlier.go"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "read_file", Content: "contents"},
		{Role: provider.RoleUser, Content: "continue"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_0", Name: "read_file", Arguments: `{"path":"later.go"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "read_file", Content: "error: missing"},
	}

	if !PathsProvenInSession(msgs, []string{"earlier.go"}, false) {
		t.Fatal("a later failure with the same tool_call_id must not erase an earlier successful read")
	}
	if PathsProvenInSession(msgs, []string{"later.go"}, false) {
		t.Fatal("the failed read must not become evidence through an earlier success with the same tool_call_id")
	}
}

func TestPathsProvenInSessionDoesNotReviveEarlierFailure(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_0", Name: "read_file", Arguments: `{"path":"failed.go"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "read_file", Content: "error: denied"},
		{Role: provider.RoleUser, Content: "try another file"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_0", Name: "read_file", Arguments: `{"path":"successful.go"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "read_file", Content: "contents"},
	}

	if PathsProvenInSession(msgs, []string{"failed.go"}, false) {
		t.Fatal("a later success with the same tool_call_id must not revive an earlier failed read")
	}
	if !PathsProvenInSession(msgs, []string{"successful.go"}, false) {
		t.Fatal("the later successful read should remain usable as evidence")
	}
}

func TestPathsProvenInSessionDoesNotBorrowMissingOrOrphanResults(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleTool, ToolCallID: "shared", Name: "read_file", Content: "orphan success"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "shared", Name: "read_file", Arguments: `{"path":"missing-result.go"}`,
		}}},
		{Role: provider.RoleUser, Content: "new turn"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "shared", Name: "read_file", Arguments: `{"path":"other.go"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "shared", Name: "read_file", Content: "contents"},
	}

	if PathsProvenInSession(msgs, []string{"missing-result.go"}, false) {
		t.Fatal("a call with no result must not borrow an orphan or another turn's result")
	}
	if !PathsProvenInSession(msgs, []string{"other.go"}, false) {
		t.Fatal("the explicitly paired later result should count")
	}
}

func TestPathsProvenInSessionRejectsNormalizedMissingResult(t *testing.T) {
	msgs := provider.NormalizeSessionMessages([]provider.Message{{
		Role: provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{
			ID: "missing", Name: "read_file", Arguments: `{"path":"unread.go"}`,
		}},
	}})
	if len(msgs) != 2 || msgs[1].Role != provider.RoleTool {
		t.Fatalf("session normalization did not add the expected interrupted-result placeholder: %+v", msgs)
	}
	if PathsProvenInSession(msgs, []string{"unread.go"}, false) {
		t.Fatal("an interrupted-result placeholder must not count as successful file evidence")
	}
}

func TestPathsProvenInSessionRejectsCancelledBeforeExecution(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "cancelled", Name: "read_file", Arguments: `{"path":"unread.go"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "cancelled", Name: "read_file", Content: " cancelled: context cancelled before execution\n"},
	}

	if PathsProvenInSession(msgs, []string{"unread.go"}, false) {
		t.Fatal("a call cancelled before execution must not count as successful file evidence")
	}
}

func TestPathsProvenInSessionPreservesWithinTurnPairing(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "read-a", Name: "read_file", Arguments: `{"path":"a.go"}`},
			{ID: "read-b", Name: "read_file", Arguments: `{"path":"b.go"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "read-b", Name: "read_file", Content: "b contents"},
		{Role: provider.RoleTool, ToolCallID: "read-a", Name: "read_file", Content: "a contents"},
	}

	if !PathsProvenInSession(msgs, []string{"a.go", "b.go"}, false) {
		t.Fatal("distinct tool_call_ids must keep supporting out-of-order results within one turn")
	}
}

func TestPathsProvenInSessionPairsDuplicateIDsByPosition(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "duplicate", Name: "read_file", Arguments: `{"path":"first.go"}`},
			{ID: "duplicate", Name: "read_file", Arguments: `{"path":"second.go"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "duplicate", Name: "read_file", Content: "first contents"},
		{Role: provider.RoleTool, ToolCallID: "duplicate", Name: "read_file", Content: "error: second failed"},
	}

	if !PathsProvenInSession(msgs, []string{"first.go"}, false) {
		t.Fatal("the first duplicate-ID call should use the first result")
	}
	if PathsProvenInSession(msgs, []string{"second.go"}, false) {
		t.Fatal("the second duplicate-ID call should use the failed second result")
	}
}

func TestPathsProvenInSessionRejectsDuplicateIDBatchWithOrphanReplacement(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "duplicate", Name: "read_file", Arguments: `{"path":"unread.go"}`},
			{ID: "duplicate", Name: "bash", Arguments: `{"command":"go test ./..."}`},
		}},
		// The successful row is an orphan replacement, not the read_file result.
		{Role: provider.RoleTool, ToolCallID: "orphan", Name: "bash", Content: "PASS"},
		{Role: provider.RoleTool, ToolCallID: "duplicate", Name: "read_file", Content: "error: denied"},
	}

	if PathsProvenInSession(msgs, []string{"unread.go"}, false) {
		t.Fatal("an orphan replacement in an ambiguous duplicate-ID batch must not grant path evidence")
	}
}

func TestPathsProvenInSessionKeepsWriterEffectsScopedToTheirTurns(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_0", Name: "write_file", Arguments: `{"path":"written.go"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "write_file", Content: "wrote written.go"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_0", Name: "write_file", Arguments: `{"path":"failed.go"}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "write_file", Content: "error: denied"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
			ID: "call_0", Name: "bash", Arguments: `{"command":"GOROOT=/sdk go test ./..."}`,
		}}},
		{Role: provider.RoleTool, ToolCallID: "call_0", Name: "bash", Content: "PASS"},
	}

	if !PathsProvenInSession(msgs, []string{"written.go"}, true) {
		t.Fatal("the successful writer lost its path evidence after later id collisions")
	}
	if PathsProvenInSession(msgs, []string{"failed.go"}, true) {
		t.Fatal("a failed writer borrowed a later colliding verification result")
	}
	verification := ReceiptFromToolCall("bash", []byte(`{"command":"GOROOT=/sdk go test ./..."}`), true, false)
	if verification.Mutation || verification.Write || !IsDeliveryVerificationCommand(verification.Command) {
		t.Fatalf("env-prefixed verification effects changed: %+v", verification)
	}
	writer := ReceiptFromToolCall("write_file", []byte(`{"path":"written.go"}`), true, false)
	if !writer.Mutation || !writer.Write || len(writer.Paths) != 1 || writer.Paths[0] != "written.go" {
		t.Fatalf("writer receipt lost mutation/path effects: %+v", writer)
	}
}
