package cli

import (
	"strings"
	"testing"

	"reasonix/internal/hook"
)

// Five separate checks produce "invalid", so a bare status leaves the caller
// guessing which one fired — and the matcher check discarded a message
// ValidateMatcher had already built. Each rejection now names itself.
func TestMachineHookEntryStatusExplainsEveryRejection(t *testing.T) {
	cases := []struct {
		name  string
		entry hook.Entry
		want  string
	}{
		{
			name:  "unknown event",
			entry: hook.Entry{Event: "NotAnEvent", Command: "echo hi"},
			want:  "unknown event",
		},
		{
			name:  "missing command",
			entry: hook.Entry{Event: hook.PostToolUse, Scope: hook.ScopeGlobal},
			want:  "command is required",
		},
		{
			name:  "bad matcher regex",
			entry: hook.Entry{Event: hook.PostToolUse, Scope: hook.ScopeGlobal, Command: "echo hi", Match: "("},
			want:  "matcher",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, reason := machineHookEntryStatus(tc.entry)
			if status != "invalid" {
				t.Fatalf("status = %q, want invalid", status)
			}
			if !strings.Contains(reason, tc.want) {
				t.Fatalf("reason = %q, want it to mention %q", reason, tc.want)
			}
		})
	}
}

func TestMachineHookEntryStatusLeavesActiveEntriesUnexplained(t *testing.T) {
	status, reason := machineHookEntryStatus(hook.Entry{
		Event:   hook.PostToolUse,
		Scope:   hook.ScopeGlobal,
		Command: "echo hi",
		Match:   "write_file",
	})
	if status != "active" {
		t.Fatalf("status = %q, want active", status)
	}
	if reason != "" {
		t.Fatalf("active entry carries a reason: %q", reason)
	}
}
