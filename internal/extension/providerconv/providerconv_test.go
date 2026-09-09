package providerconv

import (
	"strings"
	"testing"

	"reasonix/internal/extension/protocol"
	"reasonix/internal/provider"
)

// Round trips through the wire DTOs must preserve every provider-visible
// field and drop nothing the extension side needs.
func TestRequestRoundTripPreservesProviderVisibleFields(t *testing.T) {
	temperature := 0.25
	req := provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "sys"},
			{Role: provider.RoleUser, Content: "hi", Images: []string{"data:image/png;base64,AA=="}},
			{
				Role: provider.RoleAssistant, Content: "prev",
				ReasoningContent: "because", ReasoningSignature: "sig",
				ToolCalls: []provider.ToolCall{{ID: "c1", Name: "bash", Arguments: `{"cmd":"ls"}`, ThoughtSignature: "ts"}},
			},
			{Role: provider.RoleTool, ToolCallID: "c1", Name: "bash", Content: "ok"},
		},
		Tools: []provider.ToolSchema{{
			Name: "bash", Description: "run", Parameters: []byte(`{"type":"object"}`),
		}},
		Temperature:    &temperature,
		MaxTokens:      64,
		ResponseFormat: &provider.ResponseFormat{Type: "json_object"},
	}

	back := RequestFromProtocol(RequestToProtocol(req, "test-provider", "test-model"))
	if len(back.Messages) != len(req.Messages) || len(back.Tools) != 1 {
		t.Fatalf("round trip = %+v", back)
	}
	assistant := back.Messages[2]
	if assistant.ReasoningContent != "because" || assistant.ReasoningSignature != "sig" {
		t.Fatalf("assistant reasoning = %+v", assistant)
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ThoughtSignature != "ts" {
		t.Fatalf("assistant tool calls = %+v", assistant.ToolCalls)
	}
	if back.Messages[1].Images[0] != "data:image/png;base64,AA==" {
		t.Fatalf("images = %+v", back.Messages[1].Images)
	}
	if back.Tools[0].Name != "bash" || string(back.Tools[0].Parameters) != `{"type":"object"}` {
		t.Fatalf("tools = %+v", back.Tools)
	}
	if back.Temperature == nil || *back.Temperature != temperature || back.MaxTokens != 64 {
		t.Fatalf("scalars = %+v", back)
	}
	if back.ResponseFormat == nil || back.ResponseFormat.Type != "json_object" {
		t.Fatalf("response format = %+v", back.ResponseFormat)
	}
	if RequestFromProtocol(RequestToProtocol(provider.Request{}, "", "")).ResponseFormat != nil {
		t.Fatal("nil response format must stay nil")
	}
}

func TestUsageRoundTrip(t *testing.T) {
	usage := &provider.Usage{
		PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3,
		CacheHitTokens: 4, CacheMissTokens: 5, ReasoningTokens: 6, FinishReason: "stop",
	}
	back := UsageFromProtocol(UsageToProtocol(usage))
	if *back != *usage {
		t.Fatalf("usage round trip = %+v, want %+v", back, usage)
	}
	if UsageToProtocol(nil) != nil || UsageFromProtocol(nil) != nil {
		t.Fatal("nil usage must stay nil")
	}
}

func TestChunkFromProtocolMapsEveryType(t *testing.T) {
	cases := []struct {
		wire protocol.ProviderChunkType
		want provider.ChunkType
	}{
		{protocol.ChunkText, provider.ChunkText},
		{protocol.ChunkReasoning, provider.ChunkReasoning},
		{protocol.ChunkToolCallStart, provider.ChunkToolCallStart},
		{protocol.ChunkToolCallDelta, provider.ChunkToolCallArgsDelta},
		{protocol.ChunkToolCall, provider.ChunkToolCall},
		{protocol.ChunkUsage, provider.ChunkUsage},
		{protocol.ChunkDone, provider.ChunkDone},
		{protocol.ChunkError, provider.ChunkError},
	}
	for _, tc := range cases {
		got := ChunkFromProtocol(protocol.ProviderChunk{Type: tc.wire}).Type
		if got != tc.want {
			t.Fatalf("type %q mapped to %v, want %v", tc.wire, got, tc.want)
		}
	}
}

func TestChunkFromProtocolErrorCodes(t *testing.T) {
	const secret = "sk-abcdef1234567890SECRETKEY"
	failed := ChunkFromProtocol(protocol.ProviderChunk{
		Type:  protocol.ChunkError,
		Error: &protocol.ProviderError{Code: protocol.ProviderFailed, Message: "provider rejected api_key=" + secret},
	})
	if failed.Err == nil || strings.Contains(failed.Err.Error(), secret) || provider.IsStreamInterrupted(failed.Err) {
		t.Fatalf("failed chunk = %+v", failed)
	}
	if !strings.Contains(failed.Err.Error(), "provider rejected api_key=") {
		t.Fatalf("failed error lost diagnostic context: %q", failed.Err)
	}
	interrupted := ChunkFromProtocol(protocol.ProviderChunk{
		Type:  protocol.ChunkError,
		Error: &protocol.ProviderError{Code: protocol.ProviderInterrupted, Message: "provider interrupted token=" + secret},
	})
	if !provider.IsStreamInterrupted(interrupted.Err) {
		t.Fatalf("interrupted chunk = %+v", interrupted)
	}
	if strings.Contains(interrupted.Err.Error(), secret) {
		t.Fatalf("interrupted error leaked credential: %q", interrupted.Err)
	}
}

func TestDescriptorFromProtocolCopiesFields(t *testing.T) {
	wire := protocol.ProviderDescriptor{
		Ref: "plugin/demo/fake/x", DisplayName: "Demo", Model: "x",
		ContextWindow: 128_000, PricingCurrency: "$",
		CacheHitPerMillion: 0.1, InputPerMillion: 1.0, OutputPerMillion: 2.0,
		Vision: true, InputModalities: []string{"text", "image"}, Tools: true, Reasoning: true,
		Efforts: []string{"low", "high"}, DefaultEffort: "low",
		ToolCallReasoning: true, ReasoningRoundTrip: true, WarnOnMissingToolCallReasoning: true,
	}
	d := DescriptorFromProtocol(wire)
	if d.Ref != wire.Ref || d.DisplayName != wire.DisplayName || d.Model != wire.Model ||
		d.ContextWindow != wire.ContextWindow || d.PricingCurrency != wire.PricingCurrency ||
		d.CacheHitPerMillion != wire.CacheHitPerMillion || d.InputPerMillion != wire.InputPerMillion ||
		d.OutputPerMillion != wire.OutputPerMillion || d.Vision != wire.Vision || len(d.InputModalities) != 2 || d.InputModalities[1] != "image" || d.Tools != wire.Tools ||
		d.Reasoning != wire.Reasoning || d.DefaultEffort != wire.DefaultEffort ||
		d.ToolCallReasoning != wire.ToolCallReasoning || d.ReasoningRoundTrip != wire.ReasoningRoundTrip ||
		d.WarnOnMissingToolCallReasoning != wire.WarnOnMissingToolCallReasoning ||
		len(d.Efforts) != 2 || d.Efforts[1] != "high" {
		t.Fatalf("descriptor = %+v", d)
	}
}
