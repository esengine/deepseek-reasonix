package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

func TestDeepSeekReplaysEveryReasoningCarryingAssistantTurn(t *testing.T) {
	p, err := New(provider.Config{
		Name: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-pro", APIKey: "k",
		Extra: map[string]any{"reasoning_protocol": "deepseek", "thinking": "enabled"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !provider.RequiresAssistantReasoningReplay(p, provider.Message{
		Role: provider.RoleAssistant, Content: "plain", ReasoningContent: "provider reasoning",
	}) {
		t.Fatal("DeepSeek plain assistant reasoning must be replay-required")
	}
	if provider.RequiresAssistantReasoningReplay(p, provider.Message{
		Role: provider.RoleAssistant, Content: "plain",
	}) {
		t.Fatal("DeepSeek plain assistant without reasoning must not invent a replay requirement")
	}
	if !provider.RequiresAssistantReasoningReplay(p, provider.Message{
		Role:      provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "read_file"}},
	}) {
		t.Fatal("DeepSeek tool turn must remain replay-required when reasoning is missing")
	}

	disabled := p.(*client)
	disabled.thinkingType = "disabled"
	if !provider.RequiresAssistantReasoningReplay(disabled, provider.Message{
		Role: provider.RoleAssistant, Content: "plain", ReasoningContent: "previous reasoning",
	}) {
		t.Fatal("thinking-disabled DeepSeek must still replay stored reasoning")
	}
	if provider.RequiresAssistantReasoningReplay(disabled, provider.Message{
		Role:      provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{ID: "call_2", Name: "read_file"}},
	}) {
		t.Fatal("thinking-disabled DeepSeek must not require missing tool reasoning")
	}
}

func TestGLMPreservesIssuedReasoningAndAllowsEmptyToolFallback(t *testing.T) {
	p, err := New(provider.Config{
		Name: "glm", BaseURL: "https://open.bigmodel.cn/api/coding/paas/v4", Model: "glm-5.2", APIKey: "k",
		Extra: map[string]any{"reasoning_protocol": "glm"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !provider.RequiresReasoningRoundTrip(p) {
		t.Fatal("thinking-enabled GLM must preserve provider-issued reasoning")
	}
	if !provider.RequiresAssistantReasoningReplay(p, provider.Message{Role: provider.RoleAssistant, ReasoningContent: "provider reasoning"}) {
		t.Fatal("GLM must replay reasoning the provider emitted")
	}
	if provider.RequiresAssistantReasoningReplay(p, provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "read_file"}}}) {
		t.Fatal("GLM tool turns without provider reasoning must remain replayable")
	}
	if !provider.AllowsEmptyReasoningFallback(p) {
		t.Fatal("GLM must accept an empty reasoning_content value when the provider emitted none")
	}
	if provider.RequiresAssistantReasoningReplay(p, provider.Message{Role: provider.RoleAssistant, Content: "plain answer"}) {
		t.Fatal("GLM plain answers without reasoning must remain replayable")
	}
}

func TestGLMSerializesEmptyReasoningContentForToolHistory(t *testing.T) {
	p, err := New(provider.Config{
		Name: "glm", BaseURL: "https://open.bigmodel.cn/api/coding/paas/v4", Model: "glm-5.2", APIKey: "k",
		Extra: map[string]any{"reasoning_protocol": "glm"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := p.(*client).buildRequest(provider.Request{Messages: []provider.Message{
		{Role: provider.RoleUser, Content: "inspect"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "read_file", Arguments: `{}`}}},
		{Role: provider.RoleTool, ToolCallID: "call_1", Name: "read_file", Content: "package main"},
	}})
	if got := req.Messages[1].ReasoningContent; got == nil || *got != "" {
		t.Fatalf("GLM empty tool reasoning_content = %v, want explicit empty string", got)
	}
}

// TestBuildRequestSendsEmptyReasoningKeyOnPlainDeepSeekTurn guards the wire
// contract behind the repeated 400s: under the deepseek reasoning protocol in
// thinking mode, the API rejects an assistant history turn whose
// reasoning_content KEY is missing — even a plain text turn that carried no
// reasoning and no tool call. The key must serialize as an empty string.
// Generic thinking mode and non-DeepSeek backends must keep omitting it on
// plain turns, so the fix hinges on the protocol, not on thinking alone.
func TestBuildRequestSendsEmptyReasoningKeyOnPlainDeepSeekTurn(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "explain"},
		{Role: provider.RoleAssistant, Content: "plain answer"},
		{Role: provider.RoleUser, Content: "thanks"},
	}
	deepseek, err := json.Marshal((&client{model: "deepseek-v4", deepseek: true, thinkingType: "enabled"}).buildRequest(provider.Request{Messages: msgs}).Messages)
	if err != nil {
		t.Fatalf("marshal deepseek: %v", err)
	}
	var req []map[string]json.RawMessage
	if err := json.Unmarshal(deepseek, &req); err != nil {
		t.Fatalf("unmarshal deepseek: %v", err)
	}
	rc, ok := req[1]["reasoning_content"]
	if !ok {
		t.Fatal("plain DeepSeek assistant turn must still serialize the reasoning_content key")
	}
	if string(rc) != `""` {
		t.Fatalf("reasoning_content = %s, want empty string", rc)
	}

	// Generic thinking mode (RequiresToolCallReasoning without the deepseek
	// protocol) must NOT emit the key on a plain turn — only the deepseek
	// reasoning protocol demands it.
	generic, err := json.Marshal((&client{model: "mimo-v2", thinkingType: "enabled"}).buildRequest(provider.Request{Messages: msgs}).Messages)
	if err != nil {
		t.Fatalf("marshal generic: %v", err)
	}
	if strings.Contains(string(generic), "reasoning_content") {
		t.Fatalf("generic thinking mode must not serialize reasoning_content on a plain turn: %s", generic)
	}

	// The deepseek protocol with thinking disabled keeps the old guard: a plain
	// turn carries no key.
	off, err := json.Marshal((&client{model: "deepseek-v4", deepseek: true, thinkingType: "disabled"}).buildRequest(provider.Request{Messages: msgs}).Messages)
	if err != nil {
		t.Fatalf("marshal deepseek-off: %v", err)
	}
	if strings.Contains(string(off), "reasoning_content") {
		t.Fatalf("deepseek protocol with thinking disabled must not serialize reasoning_content on a plain turn: %s", off)
	}

	other, err := json.Marshal((&client{model: "mimo-v2"}).buildRequest(provider.Request{Messages: msgs}).Messages)
	if err != nil {
		t.Fatalf("marshal other: %v", err)
	}
	if strings.Contains(string(other), "reasoning_content") {
		t.Fatalf("non-DeepSeek backends must not serialize reasoning_content on a plain turn: %s", other)
	}
}
