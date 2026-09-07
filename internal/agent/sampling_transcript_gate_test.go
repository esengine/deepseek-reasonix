package agent

import (
	"context"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// The gate validates the same normalized view the adapters put on the wire, so
// a stored history no adapter could send is repaired before it leaves, and the
// repaired shape is what the gate approved.
func TestSamplingGateApprovesExactlyTheWireView(t *testing.T) {
	prov := testutil.NewMock("m", testutil.Turn{Text: "done"})
	session := NewSession("system")
	session.AddBatch(
		provider.Message{Role: provider.RoleUser, Content: "earlier"},
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "garbage", Name: "echo", Arguments: "not json"},
			{ID: "dangling", Name: "echo", Arguments: `{"text":"x"`},
		}},
		provider.Message{Role: provider.RoleTool, ToolCallID: "garbage", Name: "echo", Content: "echoed", ToolRunState: provider.ToolRunCompleted},
		provider.Message{Role: provider.RoleTool, ToolCallID: "ghost", Name: "echo", Content: "orphan"},
	)
	a := New(prov, echoRegistry(), session, Options{}, event.Discard)
	if err := a.Run(withNoClosedLoop(context.Background()), "go"); err != nil {
		t.Fatalf("repairable history refused: %v", err)
	}
	req := prov.LastRequest()
	if req == nil {
		t.Fatal("provider never called")
	}
	wire := provider.SanitizeToolPairing(provider.ModelMessages(req.Messages))
	if err := provider.ValidateTranscript(wire); err != nil {
		t.Fatalf("wire view failed the gate the agent already passed: %v", err)
	}
	for _, m := range wire {
		if m.Role == provider.RoleTool && m.ToolCallID == "ghost" {
			t.Fatal("orphan result reached the wire")
		}
	}
}
