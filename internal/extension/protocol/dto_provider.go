package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Provider DTOs: the extension-hosted provider broker. The extension holds
// provider credentials and runs streams; the host only ever sees these
// credential-free public copies. Conversion to and from internal/provider
// types lives host-side in a later stage; these DTOs deliberately do not
// import internal/provider so the public wire schema stays self-contained.

// ProviderDescriptor mirrors provider.Descriptor field-for-field as a public
// DTO. It never carries endpoints, credentials, headers, or env names.
type ProviderDescriptor struct {
	Ref                            string   `json:"ref" validate:"nonempty"`
	DisplayName                    string   `json:"displayName,omitempty"`
	Model                          string   `json:"model,omitempty"`
	ContextWindow                  int      `json:"contextWindow,omitempty" validate:"min=0"`
	PricingCurrency                string   `json:"pricingCurrency,omitempty"`
	CacheHitPerMillion             float64  `json:"cacheHitPerMillion,omitempty" validate:"min=0"`
	InputPerMillion                float64  `json:"inputPerMillion,omitempty" validate:"min=0"`
	OutputPerMillion               float64  `json:"outputPerMillion,omitempty" validate:"min=0"`
	Vision                         bool     `json:"vision,omitempty"`
	InputModalities                []string `json:"inputModalities,omitempty"`
	Tools                          bool     `json:"tools,omitempty"`
	Reasoning                      bool     `json:"reasoning,omitempty"`
	Efforts                        []string `json:"efforts,omitempty"`
	DefaultEffort                  string   `json:"defaultEffort,omitempty"`
	ToolCallReasoning              bool     `json:"toolCallReasoning,omitempty"`
	ReasoningRoundTrip             bool     `json:"reasoningRoundTrip,omitempty"`
	WarnOnMissingToolCallReasoning bool     `json:"warnOnMissingToolCallReasoning,omitempty"`
}

// PluginRefOwner extracts the plugin ID from a plugin-namespaced provider ref
// (plugin/<pluginID>/<rest...>) — the namespace every extension-hosted
// provider ref carries. Anything else — including the two-segment
// "plugin/<model>" shape, which stays an ordinary host ref — returns "".
// Host layers (boot, config validation, frontends) use it to route plugin
// refs away from config-backed catalogs they can never appear in.
func PluginRefOwner(ref string) string {
	rest, ok := strings.CutPrefix(ref, "plugin/")
	if !ok {
		return ""
	}
	pluginID, remainder, ok := strings.Cut(rest, "/")
	if !ok || pluginID == "" || remainder == "" {
		return ""
	}
	return pluginID
}

// ProviderMessage is the public copy of provider.Message. It keeps the same
// JSON field names (snake_case) so transcripts read identically, and drops
// the local-only UI metadata fields that never belong on the wire.
type ProviderMessage struct {
	Role               ProviderRole       `json:"role,omitempty"`
	Content            string             `json:"content,omitempty" externalizable:"true"`
	Images             []string           `json:"images,omitempty"`
	ReasoningContent   string             `json:"reasoning_content,omitempty"`
	ReasoningSignature string             `json:"reasoning_signature,omitempty"`
	ToolCalls          []ProviderToolCall `json:"tool_calls,omitempty"`
	ToolCallID         string             `json:"tool_call_id,omitempty"`
	Name               string             `json:"name,omitempty"`
}

// ProviderToolCall is the public copy of provider.ToolCall: provider-visible
// fields only, no Reasonix-local display metadata.
type ProviderToolCall struct {
	ID               string `json:"id" validate:"nonempty"`
	Name             string `json:"name" validate:"nonempty"`
	Arguments        string `json:"arguments"`
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

// ProviderToolSchema is the public copy of provider.ToolSchema. Parameters is
// a JSON Schema object.
type ProviderToolSchema struct {
	Name        string          `json:"name" validate:"nonempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ProviderResponseFormat asks an extension-hosted provider to constrain its
// output shape. It is optional so ordinary requests retain their existing,
// cache-stable wire representation.
type ProviderResponseFormat struct {
	Type string `json:"type" validate:"nonempty"`
}

// ProviderRequest is the credential-free completion request the host asks the
// extension to stream. Nil Messages/Tools arrays are invalid; empty arrays
// are the canonical form.
type ProviderRequest struct {
	Provider       string                  `json:"provider,omitempty"`
	Model          string                  `json:"model,omitempty"`
	Messages       []ProviderMessage       `json:"messages"`
	Tools          []ProviderToolSchema    `json:"tools"`
	Temperature    *float64                `json:"temperature,omitempty"`
	MaxTokens      int                     `json:"maxTokens" validate:"min=0"`
	ResponseFormat *ProviderResponseFormat `json:"responseFormat,omitempty"`
}

// Validate enforces the deterministic wire shape.
func (request ProviderRequest) Validate() error {
	if request.Messages == nil || request.Tools == nil {
		return validationError("messages and tools must be arrays")
	}
	if request.MaxTokens < 0 {
		return validationError("maxTokens must be non-negative")
	}
	if request.ResponseFormat != nil && strings.TrimSpace(request.ResponseFormat.Type) == "" {
		return validationError("responseFormat.type must be non-empty")
	}
	for _, tool := range request.Tools {
		parameters := bytes.TrimSpace(tool.Parameters)
		if len(parameters) == 0 || parameters[0] != '{' || !json.Valid(parameters) {
			return validationError("tool parameters must be a JSON object")
		}
	}
	return nil
}

// ProviderUsage is the public copy of provider.Usage token accounting.
type ProviderUsage struct {
	PromptTokens     int    `json:"promptTokens" validate:"min=0"`
	CompletionTokens int    `json:"completionTokens" validate:"min=0"`
	TotalTokens      int    `json:"totalTokens" validate:"min=0"`
	CacheHitTokens   int    `json:"cacheHitTokens" validate:"min=0"`
	CacheMissTokens  int    `json:"cacheMissTokens" validate:"min=0"`
	ReasoningTokens  int    `json:"reasoningTokens" validate:"min=0"`
	FinishReason     string `json:"finishReason,omitempty"`
}

// ProviderError is deliberately generic, like the Remote broker's: raw
// provider errors can contain API keys, authorization headers, endpoints, or
// response bodies and must never cross the extension boundary.
type ProviderError struct {
	Code    ProviderErrorCode `json:"code"`
	Message string            `json:"message" validate:"nonempty"`
}

// ProviderChunk is one chunk of an extension-hosted provider stream.
type ProviderChunk struct {
	Type       ProviderChunkType `json:"type"`
	Text       string            `json:"text,omitempty"`
	Signature  string            `json:"signature,omitempty"`
	ToolCall   *ProviderToolCall `json:"toolCall,omitempty"`
	ArgChars   int               `json:"argChars,omitempty" validate:"min=0"`
	Usage      *ProviderUsage    `json:"usage,omitempty"`
	Error      *ProviderError    `json:"error,omitempty"`
	Generation uint64            `json:"generation,omitempty"`
	Epoch      string            `json:"epoch,omitempty"`
}

// Validate enforces chunk invariants the tags cannot express.
func (chunk ProviderChunk) Validate() error {
	if chunk.ArgChars < 0 {
		return validationError("argChars must be non-negative")
	}
	if chunk.Type == ChunkError && chunk.Error == nil {
		return validationError("error chunks require error")
	}
	if chunk.Type != ChunkError && chunk.Error != nil {
		return validationError("non-error chunks forbid error")
	}
	if chunk.Type == ChunkUsage && chunk.Usage == nil {
		return validationError("usage chunks require usage")
	}
	return nil
}

// ProviderCatalogParams asks for the extension's full provider catalog.
type ProviderCatalogParams struct{}

// ProviderCatalogResult is the extension's non-secret provider catalog.
type ProviderCatalogResult struct {
	Providers []ProviderDescriptor `json:"providers"`
}

// StreamOpenParams opens one provider stream for a host turn. Chunks flow
// back as extension/provider/stream/chunk notifications numbered from
// SeqBase; the stream ends with exactly one stream/end notification.
type StreamOpenParams struct {
	StreamID    string          `json:"streamId" validate:"nonempty"`
	ProviderRef string          `json:"providerRef" validate:"nonempty"`
	Model       string          `json:"model,omitempty"`
	Effort      string          `json:"effort,omitempty"`
	Request     ProviderRequest `json:"request"`
	SeqBase     int             `json:"seqBase" validate:"min=0"`
	Generation  uint64          `json:"generation,omitempty"`
	Epoch       string          `json:"epoch,omitempty"`
}

// Validate enforces required identifiers plus the request invariants.
func (p StreamOpenParams) Validate() error {
	if strings.TrimSpace(p.StreamID) == "" || strings.TrimSpace(p.ProviderRef) == "" {
		return validationError("streamId and providerRef are required")
	}
	return p.Request.Validate()
}

// StreamOpenResult acknowledges the stream; chunks arrive as notifications.
type StreamOpenResult struct {
	Accepted bool `json:"accepted"`
}

// StreamCancelParams cancels one in-flight provider stream.
type StreamCancelParams struct {
	StreamID string `json:"streamId" validate:"nonempty"`
}

// StreamCancelResult acknowledges the cancel.
type StreamCancelResult struct {
	Cancelled bool `json:"cancelled"`
}

// StreamChunkParams is one provider chunk, Extension → Host.
type StreamChunkParams struct {
	StreamID   string        `json:"streamId" validate:"nonempty"`
	Seq        int64         `json:"seq" validate:"min=1"`
	Chunk      ProviderChunk `json:"chunk"`
	Generation uint64        `json:"generation,omitempty"`
	Epoch      string        `json:"epoch,omitempty"`
}

// Validate enforces stream ordering preconditions and chunk invariants.
func (p StreamChunkParams) Validate() error {
	if strings.TrimSpace(p.StreamID) == "" {
		return validationError("streamId is required")
	}
	if p.Seq < 1 {
		return validationError("seq must be >= 1")
	}
	return p.Chunk.Validate()
}

// StreamEndParams ends a stream, success or failure. LastSeq freezes the
// terminal ordering boundary: the receiver must hold chunks 1..LastSeq before
// completing the stream, and a missing chunk is a stream_gap error.
type StreamEndParams struct {
	StreamID string `json:"streamId" validate:"nonempty"`
	LastSeq  int64  `json:"lastSeq" validate:"min=0"`
	// Error is a redacted, non-secret failure message when the stream failed.
	Error string `json:"error,omitempty"`
	// Interrupted is true when the stream was cut mid-flight (transport drop
	// or cancel), not when it finished or failed cleanly.
	Interrupted bool `json:"interrupted,omitempty"`
}
