package provider

import (
	"encoding/json"
	"testing"
)

func TestRecordToolRecoveryDistinguishesCancelledAndUnknown(t *testing.T) {
	r := &InterruptedTurnRecovery{}
	RecordToolRecovery(r, InterruptedToolSummary{ID: "cancel", Name: "write_file"}, ToolRunCancelled)
	RecordToolRecovery(r, InterruptedToolSummary{ID: "unknown", Name: "bash"}, ToolRunUnknown)
	if len(r.CancelledTools) != 1 || r.CancelledTools[0].ID != "cancel" {
		t.Fatalf("cancelled tools = %#v", r.CancelledTools)
	}
	if len(r.UnknownTools) != 1 || r.UnknownTools[0].ID != "unknown" {
		t.Fatalf("unknown tools = %#v", r.UnknownTools)
	}
}

func TestRecordToolRecoveryKeepsFailedOutOfUnknown(t *testing.T) {
	r := &InterruptedTurnRecovery{}
	RecordToolRecovery(r, InterruptedToolSummary{ID: "fail", Name: "write_file"}, ToolRunFailed)
	if len(r.FailedTools) != 1 || r.FailedTools[0].ID != "fail" {
		t.Fatalf("failed tools = %#v", r.FailedTools)
	}
	if len(r.UnknownTools) != 0 || len(r.InterruptedTools) != 0 {
		t.Fatalf("paired failure treated as interrupted: unknown=%#v interrupted=%#v", r.UnknownTools, r.InterruptedTools)
	}
}

func TestToolCallRecordRoundTripsArgumentsLocally(t *testing.T) {
	want := ToolCallRecord{ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{"command":"git status"}`), State: ToolRunUnknown}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got ToolCallRecord
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if string(got.Arguments) != string(want.Arguments) || got.State != ToolRunUnknown {
		t.Fatalf("round trip = %#v", got)
	}
}

// Loading a session backfills a placeholder for every unanswered call. Reading
// it as a genuine unknown outcome would mask real proof of what actually ran.
func TestInterruptedPlaceholderIsDistinguishableFromRealEvidence(t *testing.T) {
	placeholder := Message{Role: RoleTool, ToolCallID: "a", Content: interruptedToolResult}
	if !IsInterruptedPlaceholder(placeholder) {
		t.Fatal("backfilled placeholder not recognized")
	}
	for name, m := range map[string]Message{
		"real unknown":  {Role: RoleTool, ToolCallID: "a", Content: interruptedToolResult, ToolRunState: ToolRunUnknown},
		"real output":   {Role: RoleTool, ToolCallID: "a", Content: "done"},
		"assistant":     {Role: RoleAssistant, Content: interruptedToolResult},
		"empty content": {Role: RoleTool, ToolCallID: "a"},
	} {
		if IsInterruptedPlaceholder(m) {
			t.Fatalf("%s misread as a backfilled placeholder", name)
		}
	}
}
