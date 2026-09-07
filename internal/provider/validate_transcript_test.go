package provider

import (
	"strings"
	"testing"
)

func TestValidateTranscriptAcceptsNormalizedHistory(t *testing.T) {
	msgs := NormalizeMessages([]Message{
		{Role: RoleUser, Content: "go"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a", Name: "echo", Arguments: `{"text":"x"}`}, {ID: "b", Name: "list"}}},
		{Role: RoleTool, ToolCallID: "a", Name: "echo", Content: "x"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{Name: "noid"}}},
		{Role: RoleTool, Content: "positional"},
	})
	if err := ValidateTranscript(msgs); err != nil {
		t.Fatalf("normalized history rejected: %v", err)
	}
}

func TestValidateTranscriptRejectsWhatNormalizationCannotRepair(t *testing.T) {
	cases := map[string][]Message{
		"undecodable arguments": {
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a", Name: "echo", Arguments: "not json"}}},
			{Role: RoleTool, ToolCallID: "a", Name: "echo", Content: "x"},
		},
		"missing result": {
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a", Name: "echo", Arguments: `{}`}}},
			{Role: RoleUser, Content: "next"},
		},
		"misordered results": {
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a", Name: "echo"}, {ID: "b", Name: "echo"}}},
			{Role: RoleTool, ToolCallID: "b", Name: "echo"},
			{Role: RoleTool, ToolCallID: "a", Name: "echo"},
		},
		"orphan result": {
			{Role: RoleUser, Content: "go"},
			{Role: RoleTool, ToolCallID: "ghost", Name: "echo"},
		},
	}
	for name, msgs := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateTranscript(msgs); err == nil {
				t.Fatal("malformed transcript accepted")
			}
		})
	}
}

func TestValidateTranscriptDoesNotMutateInput(t *testing.T) {
	msgs := []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a", Name: "echo", Arguments: "{"}}}}
	err := ValidateTranscript(msgs)
	if err == nil || !strings.Contains(err.Error(), "undecodable") {
		t.Fatalf("err=%v", err)
	}
	if msgs[0].ToolCalls[0].Arguments != "{" {
		t.Fatal("validator repaired arguments in place")
	}
}

// Normalization must never emit a transcript the final gate rejects; the gate
// exists to make a normalizer regression loud instead of a provider 400.
func TestNormalizeMessagesOutputAlwaysValidates(t *testing.T) {
	inputs := map[string][]Message{
		"garbage arguments":   {{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a", Name: "echo", Arguments: "not json"}}}, {Role: RoleTool, ToolCallID: "a", Name: "echo"}},
		"truncated arguments": {{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a", Name: "echo", Arguments: `{"text":"x`}}}},
		"dangling call":       {{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a", Name: "echo", Arguments: `{}`}}}, {Role: RoleUser, Content: "next"}},
		"orphan result":       {{Role: RoleUser, Content: "go"}, {Role: RoleTool, ToolCallID: "ghost", Name: "echo"}},
		"reordered results":   {{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a", Name: "echo"}, {ID: "b", Name: "echo"}}}, {Role: RoleTool, ToolCallID: "b", Name: "echo"}, {Role: RoleTool, ToolCallID: "a", Name: "echo"}},
		"duplicate ids":       {{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "same", Name: "echo"}, {ID: "same", Name: "echo"}}}, {Role: RoleTool, ToolCallID: "same", Name: "echo"}},
		"local-only noise":    {{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a", Name: "echo"}}}, {Role: RoleTool, LocalOnly: true, ToolCallID: LocalOnlyToolID, Name: LocalOnlyToolName}, {Role: RoleTool, ToolCallID: "a", Name: "echo"}},
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			if err := ValidateTranscript(NormalizeMessages(in)); err != nil {
				t.Fatalf("normalized transcript rejected: %v", err)
			}
		})
	}
}
