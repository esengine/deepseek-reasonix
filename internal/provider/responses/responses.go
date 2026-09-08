// Package responses implements the OpenAI Responses API wire protocol.
// DeepSeek uses it statelessly and requires the complete input history on every
// request; compatible stateful endpoints may opt into previous_response_id.
package responses

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"reasonix/internal/netclient"
	"reasonix/internal/provider"
	"reasonix/internal/provider/openai"
)

const (
	defaultStreamIdleTimeout     = 300 * time.Second
	maxReplayableSearchItemBytes = 512 * 1024
)

func init() {
	provider.RegisterReasoning("responses", ReasoningForConfig)
	provider.RegisterReasoning("dashscope-responses", ReasoningForConfig)
	provider.Register("responses", newFromConfig)
	provider.Register("dashscope-responses", newFromConfig)
}

// Config holds Responses API provider settings.
type Config struct {
	Name       string
	APIKey     string
	BaseURL    string
	Model      string
	ModelInfo  *provider.ModelInfo
	Effort     string
	Mode       string // stateful | stateless; empty uses vendor detection.
	Stateful   *bool  // legacy form of Mode; nil preserves vendor detection.
	WebSearch  bool   // expose the provider-executed web_search tool.
	Proxy      netclient.ProxySpec
	KeyEnv     string
	KeySource  string
	RequestURL string // optional exact Responses request URL; empty derives from BaseURL
	// MaxOutputTokens is the total provider output budget. Zero omits the field
	// on official DeepSeek (server 384K ceiling) and unknown endpoints; MiMo
	// still applies its 16K/32K ladder. Negative values omit it.
	MaxOutputTokens int
	// SessionCache controls DashScope's opt-in header. The header is never sent
	// to non-DashScope endpoints even when this value is true.
	SessionCache *bool
	// Extra carries kind-specific options; "vision" (bool) enables embedding
	// attached Images as input_image parts on user turns.
	Extra map[string]any
}

func (c Config) mode() string {
	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	if mode == "stateful" || mode == "stateless" {
		return mode
	}
	if c.Stateful != nil {
		if *c.Stateful {
			return "stateful"
		}
		return "stateless"
	}
	if capabilitiesFor(DetectVendor(c.BaseURL)).stateless {
		return "stateless"
	}
	return "stateful"
}

// DetectVendor lives in vendor.go (capabilities table): it covers dashscope/
// deepseek (incl. eu.deepseek.com) / mimo via exact-host matching.

type client struct {
	identityHeaders                    http.Header
	reasoning                          provider.ReasoningCapability
	name, apiKey, keyEnv, keySource    string
	baseURL, requestURL, model, effort string
	vendor, mode                       string
	caps                               vendorCapabilities
	sessionCache                       bool
	search                             provider.SearchPolicy
	maxOutputTokens                    int
	vision                             bool // model accepts image input; embed Images as input_image parts
	modelInfo                          provider.ModelInfo
	http                               *http.Client
	idleTimeout                        time.Duration
	authed                             atomic.Bool

	mu                   sync.Mutex
	lastResponseID       string
	expectedPrefixDigest string
}

// New creates a Responses API provider.
func New(cfg Config) provider.Provider {
	vendor := DetectVendor(cfg.BaseURL)
	cap := capabilitiesFor(vendor)
	// Explicit replay contracts apply to compatible gateways as well as exact
	// vendor hosts. Do not inherit endpoint defaults, headers, or output limits.
	if protocol, _ := cfg.Extra["reasoning_protocol"].(string); strings.EqualFold(strings.TrimSpace(protocol), "deepseek") || strings.EqualFold(strings.TrimSpace(protocol), "mimo") {
		cap.toolCallReasoning = true
	}
	maxOutputTokens := cfg.MaxOutputTokens
	// Official DeepSeek omits max_output_tokens (server 384K). MiMo still uses
	// the 16K/32K effort ladder. Compact_ratio is independent.
	if maxOutputTokens == 0 && vendor == "mimo" {
		maxOutputTokens = responsesAutoOutputBudget(vendor, cfg.Effort)
	} else if maxOutputTokens == 0 && vendor != "deepseek" && cap.defaultMaxOutputTokens > 0 {
		maxOutputTokens = cap.defaultMaxOutputTokens
	}
	sessionCache := cap.sessionCacheHeader
	if cfg.SessionCache != nil {
		sessionCache = *cfg.SessionCache
	}
	vision, _ := cfg.Extra["vision"].(bool)
	if cfg.ModelInfo != nil {
		vision = cfg.ModelInfo.SupportsInput(provider.ModalityImage)
	}
	// Official DeepSeek image input is pinned to one SKU. Ignore metadata or
	// Extra["vision"] for Flash/Pro.
	vision = openai.DeepSeekImageInputAllowed(vendor == "deepseek", cfg.RequestURL, cfg.Model, cfg.ModelInfo != nil, vision)
	httpClient := &http.Client{}
	if built, err := netclient.NewHTTPClient(cfg.Proxy, netclient.TransportOptions{
		DialTimeout: 30 * time.Second, KeepAlive: 30 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 300 * time.Second,
	}); err == nil {
		httpClient = built
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	requestURL := strings.TrimSpace(cfg.RequestURL)
	// A request_url that merely repeats the base URL is a no-op: fall through
	// to the derived endpoint instead of POSTing to the base itself.
	if requestURL != "" && strings.TrimRight(requestURL, "/") == baseURL {
		requestURL = ""
	}
	if requestURL == "" {
		requestURL = baseURL + "/responses"
	}
	modelInfo := provider.ModelInfo{ID: cfg.Model, InputModalities: []provider.ModelModality{provider.ModalityText}}
	if cfg.ModelInfo != nil {
		modelInfo = *cfg.ModelInfo
		modelInfo.ID = cfg.Model
	}
	clientWebSearch, _ := cfg.Extra["client_web_search"].(bool)
	if reject, _ := cfg.Extra["reject_redirects"].(bool); reject {
		httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	if vision {
		modelInfo.InputModalities = []provider.ModelModality{provider.ModalityText, provider.ModalityImage}
	} else if modelInfo.SupportsInput(provider.ModalityImage) {
		modelInfo.InputModalities = []provider.ModelModality{provider.ModalityText}
	}
	return &client{
		identityHeaders: provider.NewClientIdentityHeaders(),
		name:            cfg.Name, apiKey: cfg.APIKey, keyEnv: cfg.KeyEnv, keySource: cfg.KeySource,
		reasoning: ReasoningForConfig(provider.Config{BaseURL: cfg.BaseURL, Model: cfg.Model, Extra: cfg.Extra}),
		baseURL:   baseURL, requestURL: requestURL, model: cfg.Model, effort: cfg.Effort,
		vendor: vendor, caps: cap, mode: cfg.mode(), sessionCache: sessionCache, search: provider.SearchPolicy{NativeEnabled: cfg.WebSearch, ClientEnabled: clientWebSearch}, maxOutputTokens: maxOutputTokens,
		vision:    vision,
		modelInfo: modelInfo,
		http:      httpClient, idleTimeout: defaultStreamIdleTimeout,
	}
}

func (c *client) ModelInfo() provider.ModelInfo {
	if c == nil {
		return provider.ModelInfo{}
	}
	info := c.modelInfo
	info.InputModalities = append([]provider.ModelModality(nil), info.InputModalities...)
	return info
}

func responsesReasoningDisabled(effort string) bool {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "disabled", "off":
		return true
	default:
		return false
	}
}

// responsesAutoOutputBudget is the MiMo (and similar) 16K/32K ladder.
// Official DeepSeek must not call this; it omits max_output_tokens instead.
func responsesAutoOutputBudget(vendor, effort string) int {
	if responsesReasoningDisabled(effort) {
		return provider.AutoOutputBudget(false, effort)
	}
	e := strings.ToLower(strings.TrimSpace(effort))
	if vendor == "deepseek" && (e == "" || e == "auto") {
		e = "high"
	}
	return provider.AutoOutputBudget(true, e)
}

func (c *client) Name() string { return c.name }

func (c *client) NativeToolSearchAvailable() bool {
	return c != nil && provider.IsFirstPartyOpenAI(c.baseURL) && nativeToolSearchModel(c.model)
}

func nativeToolSearchModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "gpt-5.4") || strings.HasPrefix(model, "gpt-5.5") || strings.HasPrefix(model, "gpt-5.6")
}

func (c *client) sendOpts() provider.SendOptions {
	return provider.SendOptions{Provider: c.name, KeyEnv: c.keyEnv, KeySource: c.keySource, KeyPresent: c.apiKey != "", RetryAuth: c.authed.Load()}
}

// ResetContext drops stateful continuation metadata. Full-input stateless mode
// is unaffected.
func (c *client) ResetContext() {
	c.mu.Lock()
	c.lastResponseID = ""
	c.expectedPrefixDigest = ""
	c.mu.Unlock()
}

func (c *client) Stream(ctx context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	if c.effort != "auto" && c.effort != "off" {
		if err := c.reasoning.Validate(c.model, c.effort); err != nil {
			return nil, err
		}
	}
	if err := c.reasoning.Validate(c.model, req.EffortOverride); err != nil {
		return nil, err
	}
	requestCtx := provider.WithRequestAttemptCounter(ctx)
	body, usedPrevious, wireMessages := c.buildRequestBody(req)
	resp, err := c.send(requestCtx, body)
	if err != nil && usedPrevious && isStalePreviousResponseError(err) {
		// A stateful response ID may expire server-side. Retrying once with full
		// history is safe because no response body has started streaming.
		c.ResetContext()
		body, _, wireMessages = c.buildRequestBody(req)
		resp, err = c.send(requestCtx, body)
	}
	if err != nil {
		return nil, err
	}
	c.authed.Store(true)
	out := make(chan provider.Chunk, 64)
	go c.readStream(requestCtx, resp, out, wireMessages)
	return out, nil
}

func (c *client) send(ctx context.Context, body map[string]any) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("responses: marshal request: %w", err)
	}
	newRequest := func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.requestURL, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		provider.ApplyOpenCodeGoHeaders(req, c.baseURL, c.identityHeaders)
		if c.caps.sessionCacheHeader && c.sessionCache {
			req.Header.Set("x-dashscope-session-cache", "enable")
		}
		return req, nil
	}
	return provider.SendWithRetry(ctx, c.http, c.sendOpts(), newRequest)
}

func isStalePreviousResponseError(err error) bool {
	var apiErr *provider.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		return false
	}
	body := strings.ToLower(apiErr.Body)
	mentionsID := strings.Contains(body, "previous_response_id") || strings.Contains(body, "previous response") || strings.Contains(body, "response id")
	return mentionsID &&
		(strings.Contains(body, "not found") || strings.Contains(body, "invalid") || strings.Contains(body, "expired"))
}

func (c *client) buildRequestBody(req provider.Request) (map[string]any, bool, []provider.Message) {
	messages := provider.SanitizeToolPairing(provider.ModelMessages(req.Messages))
	body := map[string]any{"model": c.model, "stream": true}

	effort := c.effort
	if req.EffortOverride != "" {
		effort = req.EffortOverride
	}

	switch effort {
	case "auto":
		effort = ""
	case "disabled", "off":
		effort = "none"
	}
	if effort != "" {
		body["reasoning"] = map[string]any{"effort": effort}
	}
	maxOutputTokens := req.MaxTokens
	if maxOutputTokens == 0 {
		maxOutputTokens = c.maxOutputTokens
	}
	if maxOutputTokens == 0 && c.vendor == "mimo" {
		maxOutputTokens = responsesAutoOutputBudget(c.vendor, c.effort)
	} else if maxOutputTokens == 0 && c.vendor != "deepseek" && c.caps.defaultMaxOutputTokens > 0 {
		maxOutputTokens = c.caps.defaultMaxOutputTokens
	}
	if maxOutputTokens > 0 {
		body["max_output_tokens"] = maxOutputTokens
	}
	if req.ResponseFormat != nil && req.ResponseFormat.Type != "" {
		// Structured output: Responses text.format. MiMo/DashScope/OpenAI
		// all accept {"text":{"format":{"type":"json_object"}}}. The model
		// only emits JSON when the instructions also demand it.
		body["text"] = map[string]any{
			"format": map[string]any{"type": req.ResponseFormat.Type},
		}
	}
	if req.Temperature != nil && !c.caps.ignoresTemperature {
		body["temperature"] = *req.Temperature
	}
	if c.search.NativeEnabled || len(req.Tools) > 0 {
		body["tools"] = encodeResponsesTools(c, req)
	}
	instructions, rest := splitInstructions(messages)
	if instructions != "" {
		body["instructions"] = instructions
	}

	c.mu.Lock()
	previousID, expectedDigest := c.lastResponseID, c.expectedPrefixDigest
	c.mu.Unlock()
	if c.canUseStatefulContinuation(messages, previousID, expectedDigest) {
		body["input"] = messages[len(messages)-1].Content
		body["previous_response_id"] = previousID
		return body, true, messages
	}

	body["input"] = messagesToInput(rest, c.vision, c.search.NativeEnabled, c.caps.summaryRequired)
	return body, false, messages
}

func inputImagePart(ref string) map[string]string {
	switch provider.ClassifyImage(ref) {
	case provider.ImageFileID:
		return map[string]string{"type": "input_image", "file_id": ref}
	case provider.ImageDataURL, provider.ImageHTTPURL:
		return map[string]string{"type": "input_image", "image_url": ref}
	default:
		return nil
	}
}

func splitInstructions(messages []provider.Message) (string, []provider.Message) {
	if len(messages) == 0 || messages[0].Role != provider.RoleSystem {
		return "", messages
	}
	return messages[0].Content, messages[1:]
}

func messagesToInput(messages []provider.Message, vision, replayWebSearchItems, summary bool) []map[string]any {
	input := make([]map[string]any, 0, len(messages)*2)
	for _, message := range messages {
		switch message.Role {
		case provider.RoleSystem, provider.RoleUser:
			// Text-only turns keep the documented TextInput string shape.
			// Vision-capable user turns with attached images switch to the
			// InputItemList array form ({type:input_text} + {type:input_image})
			// so the text and every image ride the same message, matching the
			// MiMo/DashScope multimodal example. The system message is always
			// plain text: images only attach to user turns.
			if vision && message.Role == provider.RoleUser && len(message.Images) > 0 {
				parts := make([]map[string]string, 0, len(message.Images)+1)
				if message.Content != "" {
					parts = append(parts, map[string]string{"type": "input_text", "text": message.Content})
				}
				for _, ref := range message.Images {
					if part := inputImagePart(ref); part != nil {
						parts = append(parts, part)
					}
				}
				if len(parts) == 0 || (len(parts) == 1 && parts[0]["type"] == "input_text") {
					input = append(input, map[string]any{"role": "user", "content": message.Content})
				} else {
					input = append(input, map[string]any{"role": "user", "content": parts})
				}
			} else {
				input = append(input, map[string]any{"role": string(message.Role), "content": message.Content})
			}
		case provider.RoleAssistant:
			var rawReasoning bool
			input, rawReasoning = appendReasoningItems(input, message.ResponsesItems)
			if !rawReasoning && message.ReasoningContent != "" {
				// Reasoning items: the OpenAI base format only needs
				// `content`. DashScope additionally requires a `summary`
				// list ("Invalid 'summary': summary is required and must be
				// a list for reasoning."). Other vendors (MiMo) do not
				// define summary in their schema; sending it leaks the
				// reasoning text into an extra field the server may echo
				// back into the model context, doubling chain-of-thought
				// each turn — so only send it where the wire demands it.
				item := map[string]any{
					"type":    "reasoning",
					"content": []map[string]string{{"type": "reasoning_text", "text": message.ReasoningContent}},
				}
				if message.ReasoningID != "" {
					// OpenAI Responses schema marks Reasoning.id required;
					// round-trip the provider-issued id when we captured one.
					item["id"] = message.ReasoningID
				}
				if message.ReasoningStatus != "" {
					item["status"] = message.ReasoningStatus
				}
				if summary {
					item["summary"] = []map[string]string{{"type": "summary_text", "text": message.ReasoningContent}}
				}
				input = append(input, item)
			}
			if replayWebSearchItems {
				for _, raw := range message.ResponsesItems {
					if item, ok := decodeReplayableWebSearchItem(raw); ok {
						input = append(input, item)
					}
				}
			}
			if message.Content != "" || len(message.ToolCalls) == 0 {
				input = append(input, map[string]any{"role": "assistant", "content": message.Content})
			}
			for _, call := range message.ToolCalls {
				input = append(input, map[string]any{
					"type": "function_call", "call_id": call.ID,
					"name": call.Name, "arguments": call.Arguments,
				})
			}
		case provider.RoleTool:
			input = append(input, map[string]any{
				"type": "function_call_output", "call_id": message.ToolCallID, "output": message.Content,
			})
		}
	}
	return input
}

func decodeReplayableWebSearchItem(raw json.RawMessage) (map[string]any, bool) {
	if len(raw) == 0 || len(raw) > maxReplayableSearchItemBytes || !json.Valid(raw) {
		return nil, false
	}
	var item map[string]any
	if err := json.Unmarshal(raw, &item); err != nil || item["type"] != "web_search_call" {
		return nil, false
	}
	id, _ := item["id"].(string)
	status, _ := item["status"].(string)
	if strings.TrimSpace(id) == "" || status != "completed" {
		return nil, false
	}
	return item, true
}

func (c *client) conversationDigest(messages []provider.Message) string {
	instructions, rest := splitInstructions(messages)
	// Digest must mirror the wire exactly: the stateful fast path compares
	// this against the previous request's input, so a mismatch would skip
	// previous_response_id and force a full replay (cache-hit loss). Use the
	// same vision/summary knobs as buildRequestBody.
	payload, _ := json.Marshal(struct {
		Instructions string           `json:"instructions,omitempty"`
		Input        []map[string]any `json:"input"`
	}{Instructions: instructions, Input: messagesToInput(rest, c.vision, c.search.NativeEnabled, c.caps.summaryRequired)})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

type streamedCall struct {
	id, name, arguments string
	argChars            int
	completed           bool
}

func (c *client) readStream(ctx context.Context, resp *http.Response, out chan<- provider.Chunk, requestMessages []provider.Message) {
	defer resp.Body.Close()
	defer close(out)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	idle := c.idleTimeout
	if idle <= 0 {
		idle = defaultStreamIdleTimeout
	}
	watchDone := make(chan struct{})
	activity := make(chan struct{}, 1)
	var reasoningSnapshots responseReasoningSnapshots
	var stalled atomic.Bool
	go func() {
		timer := time.NewTimer(idle)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = resp.Body.Close()
				return
			case <-watchDone:
				return
			case <-activity:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(idle)
			case <-timer.C:
				stalled.Store(true)
				_ = resp.Body.Close()
				return
			}
		}
	}()
	defer close(watchDone)

	calls := make(map[string]*streamedCall)
	callOrder := make([]string, 0)
	callForItem := func(itemID string) *streamedCall {
		if call := calls[itemID]; call != nil {
			return call
		}
		call := &streamedCall{id: itemID}
		calls[itemID] = call
		callOrder = append(callOrder, itemID)
		return call
	}
	textDeltas := make(map[string]bool)
	reasoningDeltas := make(map[string]bool)
	seenSearchItems := make(map[string]struct{})
	var responsesItems []json.RawMessage
	var text, reasoning strings.Builder
	reasoningID := ""
	reasoningStatus := ""
	terminal := false
	failed := false
	completedResponseID := ""

	for scanner.Scan() {
		select {
		case activity <- struct{}{}:
		default:
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			terminal = true
			break
		}
		var event sseEvent
		if json.Unmarshal([]byte(data), &event) != nil {
			continue
		}
		key := fmt.Sprintf("%s:%d", event.ItemID, event.ContentIndex)
		reasoningSnapshots.capture(event)
		switch event.Type {
		case "response.output_text.delta":
			textDeltas[key] = true
			text.WriteString(event.Delta)
			if !sendChunk(ctx, out, provider.Chunk{Type: provider.ChunkText, Text: event.Delta}) {
				return
			}
		case "response.output_text.done":
			if event.Text != "" && !textDeltas[key] {
				text.WriteString(event.Text)
				if !sendChunk(ctx, out, provider.Chunk{Type: provider.ChunkText, Text: event.Text}) {
					return
				}
			}
		case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			reasoningDeltas[key] = true
			reasoning.WriteString(event.Delta)
			if !sendChunk(ctx, out, provider.Chunk{Type: provider.ChunkReasoning, Text: event.Delta}) {
				return
			}
		case "response.reasoning_text.done", "response.reasoning_summary_text.done":
			if event.Text != "" && !reasoningDeltas[key] {
				reasoning.WriteString(event.Text)
				if !sendChunk(ctx, out, provider.Chunk{Type: provider.ChunkReasoning, Text: event.Text}) {
					return
				}
			}
		case "response.output_item.added":
			if event.Item != nil {
				switch event.Item.Type {
				case "function_call":
					call := callForItem(event.Item.ID)
					call.id = event.Item.CallID
					call.name = event.Item.Name
					if !sendChunk(ctx, out, provider.Chunk{Type: provider.ChunkToolCallStart, ToolCall: &provider.ToolCall{ID: call.id, Name: call.name}}) {
						return
					}
				case "reasoning":
					// Capture the provider-issued reasoning item id so the
					// next turn's input reasoning item can carry it (the
					// OpenAI Responses schema marks Reasoning.id required).
					if event.Item.ID != "" {
						// 多段推理（DeepSeek 长思考分多段）时末段 id 覆盖：round-trip
						// 合并为一个 reasoning item 只带末段 id（服务端接受）。
						reasoningID = event.Item.ID
					}
				}
			}
		case "response.function_call_arguments.delta":
			call := callForItem(event.ItemID)
			call.arguments += event.Delta
			call.argChars += len(event.Delta)
			if !sendChunk(ctx, out, provider.Chunk{Type: provider.ChunkToolCallArgsDelta, ToolCall: &provider.ToolCall{ID: call.id, Name: call.name}, ArgChars: call.argChars}) {
				return
			}
		case "response.function_call_arguments.done":
			call := callForItem(event.ItemID)
			if event.Arguments != "" {
				call.arguments = event.Arguments
			}
			if !call.completed {
				call.completed = true
				if !sendChunk(ctx, out, provider.Chunk{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: call.id, Name: call.name, Arguments: call.arguments}}) {
					return
				}
			}
		case "response.output_item.done":
			if event.Item != nil && event.Item.Type == "web_search_call" && c.search.NativeEnabled {
				if _, ok := decodeReplayableWebSearchItem(event.Item.Raw); ok {
					key := event.Item.ID
					if key == "" {
						key = string(event.Item.Raw)
					}
					if _, seen := seenSearchItems[key]; !seen {
						seenSearchItems[key] = struct{}{}
						raw := append(json.RawMessage(nil), event.Item.Raw...)
						responsesItems = append(responsesItems, raw)
						if !emitSearchReplay(ctx, out, raw) {
							return
						}
					}
				}
			}
			if event.Item != nil {
				switch event.Item.Type {
				case "function_call":
					call := callForItem(event.Item.ID)
					if event.Item.CallID != "" {
						call.id = event.Item.CallID
					}
					if event.Item.Name != "" {
						call.name = event.Item.Name
					}
					if event.Item.Arguments != "" {
						call.arguments = event.Item.Arguments
					}
					if !call.completed {
						call.completed = true
						if !sendChunk(ctx, out, provider.Chunk{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: call.id, Name: call.name, Arguments: call.arguments}}) {
							return
						}
					}
				case "reasoning":
					// The done event carries the final item status
					// ("completed" after the thinking stream finishes);
					// round-trip it with the reasoning item so the input
					// matches the wire schema.
					if event.Item.Status != "" {
						reasoningStatus = event.Item.Status
					}
				}
			}
		case "response.completed", "response.incomplete", "response.failed":
			terminal = true
			if event.Type == "response.incomplete" {
				if !sendChunk(ctx, out, provider.Chunk{Type: provider.ChunkReasoning, ReasoningState: provider.ReasoningIncomplete}) {
					return
				}
			}
			completedResponseID = terminalResponseID(event)
			if !emitTerminalResponseUsage(ctx, out, event) {
				return
			}
			if event.Type == "response.failed" {
				failed = true
				err := fmt.Errorf("responses: response failed")
				if event.Response != nil && event.Response.Error != nil {
					if authErr := authErrorFromResponse(c, event.Response.Error); authErr != nil {
						err = authErr
					} else {
						err = fmt.Errorf("responses: %s", event.Response.Error.Message)
					}
				}
				if !sendChunk(ctx, out, provider.Chunk{Type: provider.ChunkError, Err: err}) {
					return
				}
			}
		}
		if terminal {
			break
		}
	}

	if ctx.Err() != nil {
		return
	}
	if err := scanner.Err(); err != nil {
		var reason string
		if stalled.Load() {
			err = fmt.Errorf("responses: stream idle timeout after %s", idle)
			reason = provider.StreamInterruptIdleTimeout
		} else {
			reason = provider.ClassifyStreamInterrupt(err)
		}
		_ = sendChunk(ctx, out, provider.Chunk{Type: provider.ChunkError, Err: provider.StreamInterrupt(err, reason)})
		return
	}
	// Protocol-defined terminal response events are required. Connection close
	// before a terminal event leaves the attempt uncommitted — including any
	// complete tool calls already forwarded as speculative output.
	if !terminal {
		_ = sendChunk(ctx, out, provider.Chunk{Type: provider.ChunkError, Err: provider.StreamInterrupt(io.ErrUnexpectedEOF, provider.StreamInterruptPrematureEOF)})
		return
	}
	if !reasoningSnapshots.emit(ctx, out) {
		return
	}
	responsesItems = append(responsesItems, reasoningSnapshots.items...)
	if len(reasoningSnapshots.items) > 0 {
		reasoningID, reasoningStatus = reasoningSnapshots.metadata()
	}
	if completedResponseID != "" {
		assistant := provider.Message{Role: provider.RoleAssistant, Content: text.String(), ReasoningContent: reasoning.String(), ReasoningID: reasoningID, ReasoningStatus: reasoningStatus, ResponsesItems: responsesItems}
		for _, itemID := range callOrder {
			call := calls[itemID]
			if call.completed {
				assistant.ToolCalls = append(assistant.ToolCalls, provider.ToolCall{ID: call.id, Name: call.name, Arguments: call.arguments})
			}
		}
		expected := append(append([]provider.Message(nil), requestMessages...), assistant)
		c.mu.Lock()
		c.lastResponseID = completedResponseID
		c.expectedPrefixDigest = c.conversationDigest(expected)
		c.mu.Unlock()
	} else {
		c.ResetContext()
	}
	if !failed {
		// 把 reasoning item 的 id/status 作为元数据 chunk 流给 Agent
		// （空 Text，随 ChunkReasoning 语义）——Agent 持久化进 session，
		// 下一轮 input reasoning item 回传 id/status（评审 #7234 第 1 点）。
		if reasoningID != "" || reasoningStatus != "" {
			if !sendChunk(ctx, out, provider.Chunk{Type: provider.ChunkReasoning, ReasoningID: reasoningID, ReasoningStatus: reasoningStatus}) {
				return
			}
		}
		_ = sendChunk(ctx, out, provider.Chunk{Type: provider.ChunkDone})
	}
}

func sendChunk(ctx context.Context, out chan<- provider.Chunk, chunk provider.Chunk) bool {
	select {
	case out <- chunk:
		return true
	default:
	}
	notifySendChunkEnterBlocking()
	select {
	case out <- chunk:
		return true
	case <-ctx.Done():
		return false
	}
}

func usageFromResponse(response *sseResponse) *provider.Usage {
	usage := &provider.Usage{}
	if response == nil || response.Usage == nil {
		return usage
	}
	u := response.Usage
	cached, reasoning := 0, 0
	if u.InputTokensDetails != nil {
		cached = u.InputTokensDetails.CachedTokens
	}
	if u.OutputTokensDetails != nil {
		reasoning = u.OutputTokensDetails.ReasoningTokens
	}
	miss := max(u.InputTokens-cached, 0)
	total := u.TotalTokens
	if total == 0 {
		total = u.InputTokens + u.OutputTokens
	}
	return &provider.Usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens, TotalTokens: total, CacheHitTokens: cached, CacheMissTokens: miss, ReasoningTokens: reasoning}
}

func authErrorFromResponse(c *client, responseError *sseError) error {
	if responseError == nil {
		return nil
	}
	value := strings.ToLower(responseError.Code + " " + responseError.Message)
	if !strings.Contains(value, "auth") && !strings.Contains(value, "api key") && !strings.Contains(value, "unauthorized") && !strings.Contains(value, "forbidden") && !strings.Contains(value, "permission") {
		return nil
	}
	status := http.StatusUnauthorized
	if strings.Contains(value, "forbidden") || strings.Contains(value, "permission") {
		status = http.StatusForbidden
	}
	return &provider.AuthError{Provider: c.name, KeyEnv: c.keyEnv, KeySource: c.keySource, Status: status, HasKey: c.apiKey != "", Body: responseError.Message}
}

type sseEvent struct {
	Type         string       `json:"type"`
	Delta        string       `json:"delta"`
	Text         string       `json:"text"`
	Arguments    string       `json:"arguments"`
	ItemID       string       `json:"item_id"`
	ContentIndex int          `json:"content_index"`
	Item         *sseItem     `json:"item"`
	Response     *sseResponse `json:"response"`
}

type sseItem struct {
	ID, Type, CallID, Name, Arguments, Status string
	Raw                                       json.RawMessage
}

func (i *sseItem) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*i = sseItem{ID: wire.ID, Type: wire.Type, CallID: wire.CallID, Name: wire.Name, Arguments: wire.Arguments, Status: wire.Status, Raw: append(json.RawMessage(nil), data...)}
	return nil
}

type sseResponse struct {
	Output            []sseItem         `json:"output"`
	ID                string            `json:"id"`
	Usage             *sseUsage         `json:"usage"`
	Error             *sseError         `json:"error"`
	IncompleteDetails incompleteDetails `json:"incomplete_details"`
}

type incompleteDetails struct {
	Reason string `json:"reason"`
}
type sseError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}
type sseUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}
