// Package providerconv holds the host-side conversions between
// internal/provider values and the public Extension Protocol v2 wire DTOs.
// The protocol package deliberately does not import internal/provider, so
// every host surface that moves provider data across the extension boundary —
// the agent's intercept wiring (stage 6b2) and the sidecar provider adapter
// (stage 7) — shares these helpers. Wire DTOs never carry credentials:
// ProviderError messages must be redacted by the producer before they cross,
// and the host defensively redacts them again at the trust boundary.
package providerconv

import (
	"errors"

	"reasonix/internal/extension/protocol"
	"reasonix/internal/provider"
	"reasonix/internal/secrets"
)

// MessagesToProtocol copies provider messages into their public wire form,
// dropping the local-only UI metadata fields that never belong on the wire.
func MessagesToProtocol(msgs []provider.Message) []protocol.ProviderMessage {
	out := make([]protocol.ProviderMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, protocol.ProviderMessage{
			Role:               protocol.ProviderRole(m.Role),
			Content:            m.Content,
			Images:             m.Images,
			ReasoningContent:   m.ReasoningContent,
			ReasoningSignature: m.ReasoningSignature,
			ToolCalls:          ToolCallsToProtocol(m.ToolCalls),
			ToolCallID:         m.ToolCallID,
			Name:               m.Name,
		})
	}
	return out
}

// MessagesFromProtocol copies wire messages back into provider messages.
func MessagesFromProtocol(msgs []protocol.ProviderMessage) []provider.Message {
	out := make([]provider.Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, provider.Message{
			Role:               provider.Role(m.Role),
			Content:            m.Content,
			Images:             m.Images,
			ReasoningContent:   m.ReasoningContent,
			ReasoningSignature: m.ReasoningSignature,
			ToolCalls:          ToolCallsFromProtocol(m.ToolCalls),
			ToolCallID:         m.ToolCallID,
			Name:               m.Name,
		})
	}
	return out
}

// ToolCallsToProtocol copies tool calls into their provider-visible wire form.
func ToolCallsToProtocol(calls []provider.ToolCall) []protocol.ProviderToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]protocol.ProviderToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, protocol.ProviderToolCall{
			ID:               c.ID,
			Name:             c.Name,
			Arguments:        c.Arguments,
			ThoughtSignature: c.ThoughtSignature,
		})
	}
	return out
}

// ToolCallsFromProtocol copies wire tool calls back into provider tool calls.
func ToolCallsFromProtocol(calls []protocol.ProviderToolCall) []provider.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]provider.ToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, provider.ToolCall{
			ID:               c.ID,
			Name:             c.Name,
			Arguments:        c.Arguments,
			ThoughtSignature: c.ThoughtSignature,
		})
	}
	return out
}

// RequestToProtocol copies a completion request into the credential-free wire
// form the host sends to an extension-hosted provider.
func RequestToProtocol(req provider.Request, providerName, model string) protocol.ProviderRequest {
	tools := make([]protocol.ProviderToolSchema, 0, len(req.Tools))
	for _, s := range req.Tools {
		tools = append(tools, protocol.ProviderToolSchema{
			Name:        s.Name,
			Description: s.Description,
			Parameters:  s.Parameters,
		})
	}
	var responseFormat *protocol.ProviderResponseFormat
	if req.ResponseFormat != nil {
		responseFormat = &protocol.ProviderResponseFormat{Type: req.ResponseFormat.Type}
	}
	return protocol.ProviderRequest{
		Provider:       providerName,
		Model:          model,
		Messages:       MessagesToProtocol(req.Messages),
		Tools:          tools,
		Temperature:    req.Temperature,
		MaxTokens:      req.MaxTokens,
		ResponseFormat: responseFormat,
	}
}

// RequestFromProtocol copies a wire request back into a provider request.
func RequestFromProtocol(req protocol.ProviderRequest) provider.Request {
	tools := make([]provider.ToolSchema, 0, len(req.Tools))
	for _, s := range req.Tools {
		tools = append(tools, provider.ToolSchema{
			Name:        s.Name,
			Description: s.Description,
			Parameters:  s.Parameters,
		})
	}
	var responseFormat *provider.ResponseFormat
	if req.ResponseFormat != nil {
		responseFormat = &provider.ResponseFormat{Type: req.ResponseFormat.Type}
	}
	return provider.Request{
		Messages:       MessagesFromProtocol(req.Messages),
		Tools:          tools,
		Temperature:    req.Temperature,
		MaxTokens:      req.MaxTokens,
		ResponseFormat: responseFormat,
	}
}

// UsageToProtocol copies token accounting into its wire form.
func UsageToProtocol(u *provider.Usage) *protocol.ProviderUsage {
	if u == nil {
		return nil
	}
	return &protocol.ProviderUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CacheHitTokens:   u.CacheHitTokens,
		CacheMissTokens:  u.CacheMissTokens,
		ReasoningTokens:  u.ReasoningTokens,
		FinishReason:     u.FinishReason,
	}
}

// UsageFromProtocol copies wire token accounting back into a provider Usage.
func UsageFromProtocol(u *protocol.ProviderUsage) *provider.Usage {
	if u == nil {
		return nil
	}
	return &provider.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CacheHitTokens:   u.CacheHitTokens,
		CacheMissTokens:  u.CacheMissTokens,
		ReasoningTokens:  u.ReasoningTokens,
		FinishReason:     u.FinishReason,
	}
}

// DescriptorFromProtocol copies an extension's provider catalog entry into the
// host's descriptor form. The DTO mirrors provider.Descriptor field-for-field.
func DescriptorFromProtocol(d protocol.ProviderDescriptor) provider.Descriptor {
	return provider.Descriptor{
		Ref:                            d.Ref,
		DisplayName:                    d.DisplayName,
		Model:                          d.Model,
		ContextWindow:                  d.ContextWindow,
		PricingCurrency:                d.PricingCurrency,
		CacheHitPerMillion:             d.CacheHitPerMillion,
		InputPerMillion:                d.InputPerMillion,
		OutputPerMillion:               d.OutputPerMillion,
		Vision:                         d.Vision,
		InputModalities:                stringModalities(d.InputModalities),
		Tools:                          d.Tools,
		Reasoning:                      d.Reasoning,
		Efforts:                        append([]string(nil), d.Efforts...),
		DefaultEffort:                  d.DefaultEffort,
		ToolCallReasoning:              d.ToolCallReasoning,
		ReasoningRoundTrip:             d.ReasoningRoundTrip,
		WarnOnMissingToolCallReasoning: d.WarnOnMissingToolCallReasoning,
	}
}

func stringModalities(values []string) []provider.ModelModality {
	if values == nil {
		return nil
	}
	out := make([]provider.ModelModality, 0, len(values))
	for _, value := range values {
		out = append(out, provider.ModelModality(value))
	}
	return out
}

// ChunkFromProtocol converts one inbound extension stream chunk. The protocol
// requires extensions to redact provider errors before sending them, but the
// host treats the sidecar as untrusted and defensively redacts again. A
// provider_interrupted code maps to StreamInterruptedError so the agent's
// interruption recovery applies.
func ChunkFromProtocol(chunk protocol.ProviderChunk) provider.Chunk {
	converted := provider.Chunk{
		Type:      chunkTypeFromProtocol(chunk.Type),
		Text:      chunk.Text,
		Signature: chunk.Signature,
		ArgChars:  chunk.ArgChars,
	}
	if chunk.ToolCall != nil {
		converted.ToolCall = &provider.ToolCall{
			ID:               chunk.ToolCall.ID,
			Name:             chunk.ToolCall.Name,
			Arguments:        chunk.ToolCall.Arguments,
			ThoughtSignature: chunk.ToolCall.ThoughtSignature,
		}
	}
	if chunk.Usage != nil {
		converted.Usage = UsageFromProtocol(chunk.Usage)
	}
	if chunk.Error != nil {
		err := errors.New(secrets.RedactCredentials(chunk.Error.Message))
		if chunk.Error.Code == protocol.ProviderInterrupted {
			err = &provider.StreamInterruptedError{Err: err}
		}
		converted.Err = err
	}
	return converted
}

func chunkTypeFromProtocol(kind protocol.ProviderChunkType) provider.ChunkType {
	switch kind {
	case protocol.ChunkText:
		return provider.ChunkText
	case protocol.ChunkReasoning:
		return provider.ChunkReasoning
	case protocol.ChunkToolCallStart:
		return provider.ChunkToolCallStart
	case protocol.ChunkToolCallDelta:
		return provider.ChunkToolCallArgsDelta
	case protocol.ChunkToolCall:
		return provider.ChunkToolCall
	case protocol.ChunkUsage:
		return provider.ChunkUsage
	case protocol.ChunkDone:
		return provider.ChunkDone
	case protocol.ChunkError:
		return provider.ChunkError
	default:
		return provider.ChunkError
	}
}
