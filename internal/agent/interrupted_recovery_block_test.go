package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

// Arguments are persisted locally so a user can re-run a call deliberately.
// The model-facing block must carry names, IDs, and effects only.
func TestInterruptedRecoveryBlockNeverCarriesArgumentsOrResults(t *testing.T) {
	const secret = "rm -rf /srv//private-path"
	r := &provider.InterruptedTurnRecovery{
		Pending: true, Cause: "runtime_restart", SilentInterruption: true,
		CompletedTools:  []provider.InterruptedToolSummary{{ID: "edit", Name: "edit_file", Files: []string{"a.go"}, Added: 3, Removed: 1}},
		FailedTools:     []provider.InterruptedToolSummary{{ID: "lint", Name: "bash"}},
		UnknownTools:    []provider.InterruptedToolSummary{{ID: "commit", Name: "bash"}},
		CancelledTools:  []provider.InterruptedToolSummary{{ID: "push", Name: "bash"}},
		NotStartedTools: []provider.InterruptedToolSummary{{ID: "push", Name: "bash"}},
		ToolCalls: []provider.ToolCallRecord{
			{ID: "commit", Name: "bash", Arguments: json.RawMessage(`{"command":"` + secret + `"}`), State: provider.ToolRunUnknown},
		},
		DroppedPartialText: true,
	}
	block := interruptedRecoveryBlock(r)

	if strings.Contains(block, secret) || strings.Contains(block, "command") {
		t.Fatalf("block leaked tool arguments:\n%s", block)
	}
	for _, want := range []string{
		"cause: runtime_restart", "silent_interruption: true",
		"completed_tools:", "failed_tools:", "outcome_unknown_tools:",
		"cancelled_tools:", "not_started_tools:", "unsafe_partial_output:",
		"id=commit", "id=push",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("block missing %q:\n%s", want, block)
		}
	}
}

// A failure with a paired result is proven; recovery must not demote it into
// the bucket that forbids retrying.
func TestInterruptedRecoveryBlockSeparatesFailedFromUnknown(t *testing.T) {
	r := &provider.InterruptedTurnRecovery{Pending: true}
	provider.RecordToolRecovery(r, provider.InterruptedToolSummary{ID: "lint", Name: "bash"}, provider.ToolRunFailed)
	block := interruptedRecoveryBlock(r)

	if !strings.Contains(block, "failed_tools:") {
		t.Fatalf("failed call missing from block:\n%s", block)
	}
	if strings.Contains(block, "outcome_unknown_tools:") {
		t.Fatalf("proven failure reported as unknown:\n%s", block)
	}
	if !strings.Contains(block, "interrupted_tools: none") {
		t.Fatalf("proven failure reported as interrupted:\n%s", block)
	}
}
