// Package anthropic implements the Anthropic Messages API provider (POST
// /v1/messages, SSE streaming) with a hand-written net/http client — no SDK. It
// self-registers under the "anthropic" kind, so any Claude model is a config
// instance rather than code.
//
// Two notes, both rooted in the transport-agnostic provider.Message abstraction:
//
//   - Extended thinking is opt-in (provider config thinking="adaptive"). Anthropic
//     requires the *signed* thinking block be replayed on the next turn when a tool
//     call followed thinking, so Message carries ReasoningSignature alongside
//     ReasoningContent and this provider replays the signed block on the next
//     request. DeepSeek's Anthropic endpoint instead uses unsigned thinking blocks,
//     thinking.type enabled|disabled, and output_config.effort; requests carrying
//     tools must replay all provider reasoning. Some other compatible gateways such
//     as LongCat use the binary toggle without output_config. (redacted_thinking
//     blocks are not yet captured/replayed.)
//   - Native Anthropic requests omit temperature/top_p. Current Claude models
//     (Opus 4.8/4.7) reject sampling parameters with a 400; Anthropic steers
//     behavior via prompting instead. DeepSeek's compatible endpoint accepts the
//     caller's temperature, so that field is preserved only for DeepSeek.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"reasonix/internal/netclient"
	"reasonix/internal/provider"
	"reasonix/internal/provider/openai"
)

// defaultStreamIdleTimeout caps how long a started SSE stream may go silent before
// it's treated as a dropped connection — a half-open TCP connection (proxy switched
// mid-stream) sends no RST, so scanner.Scan() would block forever. Generous on
// purpose; live streams emit far more often. Stored per-client (client.idleTimeout)
// so a test can shorten it without a shared global that races other watchdogs.
const defaultStreamIdleTimeout = 300 * time.Second

const (
	// anthropicVersion is the required API version header value.
	anthropicVersion = "2023-06-01"
	// defaultBaseURL is the first-party endpoint; config may override it (e.g. a
	// gateway). Bedrock/Vertex use a different request shape and are out of scope.
	defaultBaseURL = "https://api.anthropic.com"
	// defaultMaxTokens is the mandatory Anthropic fallback when neither config
	// nor request supplies max_tokens. Native Anthropic stays at 16K; official
	// DeepSeek sends the documented 384K ceiling because budget_tokens is ignored.
	defaultMaxTokens = provider.DefaultOrdinaryOutputTokens
)

func init() {
	provider.RegisterReasoning("anthropic", ReasoningForConfig)
	provider.Register("anthropic", New)
}

// New builds an Anthropic provider from a resolved config.
func New(cfg provider.Config) (provider.Provider, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("anthropic: model is required for provider %q", cfg.Name)
	}
	name := cfg.Name
	if name == "" {
		name = "anthropic"
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	// Anthropic's API surface is at {root}/v1/messages, so c.baseURL stores
	// the *root* -- without any trailing /v1. The setup wizard, however, lets
	// users paste a full OpenAI-compatible URL (e.g.
	// "https://proxy.example.com/v1") because that's what /models probes
	// expect. Stripping the trailing /v1 here makes both forms land on the
	// same endpoint without forcing users to remember Anthropic's quirky
	// root-vs-versioned split. Without this, a user pasting
	// "https://proxy.example.com/v1" would probe /v1/models successfully
	// but get the chat client concatenating onto
	// "https://proxy.example.com/v1/v1/messages" -- a 404.
	root := strings.TrimRight(baseURL, "/")
	root = strings.TrimSuffix(root, "/v1")
	if root == "" {
		root = defaultBaseURL
	}
	requestURL, _ := cfg.Extra["request_url"].(string)
	requestURL = strings.TrimSpace(requestURL)
	// A request_url that merely repeats the base root (e.g. the settings UI
	// mirroring base_url into the exact-URL field) is a no-op: treat it as
	// unset and derive the canonical endpoint instead of POSTing to the root.
	if requestURL != "" && strings.TrimSuffix(strings.TrimRight(requestURL, "/"), "/v1") == root {
		requestURL = ""
	}
	if requestURL == "" {
		requestURL = root + "/v1/messages"
	}
	officialDeepSeek := openai.IsDeepSeek(root)
	reasoningProtocol, _ := cfg.Extra["reasoning_protocol"].(string)
	reasoningProtocol = strings.ToLower(strings.TrimSpace(reasoningProtocol))
	deepSeekReplay := officialDeepSeek
	switch reasoningProtocol {
	case "deepseek":
		deepSeekReplay = true
	case "none":
		deepSeekReplay = false
	}
	keyEnv, _ := cfg.Extra["api_key_env"].(string) // for actionable auth errors
	keySource, _ := cfg.Extra["api_key_source"].(string)
	thinking, _ := cfg.Extra["thinking"].(string)
	thinking = strings.ToLower(strings.TrimSpace(thinking))
	effort, err := configuredEffort(cfg)
	if err != nil {
		return nil, err
	}
	vision, _ := cfg.Extra["vision"].(bool)
	modelInfo := provider.ModelInfo{ID: cfg.Model, InputModalities: []provider.ModelModality{provider.ModalityText}}
	if cfg.ModelInfo != nil {
		modelInfo = *cfg.ModelInfo
		modelInfo.ID = cfg.Model
	}
	if cfg.ModelInfo != nil {
		vision = modelInfo.SupportsInput(provider.ModalityImage)
	}
	// Official DeepSeek image input is pinned to one SKU even when metadata
	// claims otherwise.
	vision = openai.DeepSeekImageInputAllowed(officialDeepSeek, requestURL, cfg.Model, cfg.ModelInfo != nil, vision)
	if vision {
		modelInfo.InputModalities = []provider.ModelModality{provider.ModalityText, provider.ModalityImage}
	} else if modelInfo.SupportsInput(provider.ModalityImage) {
		modelInfo.InputModalities = []provider.ModelModality{provider.ModalityText}
	}
	webSearch, _ := cfg.Extra["web_search"].(bool)
	clientWebSearch, _ := cfg.Extra["client_web_search"].(bool)
	headers, _ := cfg.Extra["headers"].(map[string]string)
	authHeader, _ := cfg.Extra["auth_header"].(bool)
	maxOutputTokens, _ := cfg.Extra["max_output_tokens"].(int)
	if maxOutputTokens <= 0 {
		// Messages requires max_tokens. 0 = automatic; negative also falls back
		// because the wire field is mandatory.
		if officialDeepSeek {
			maxOutputTokens = provider.DeepSeekMaxOutputTokens
		} else {
			// Native Anthropic and unknown gateways: conservative ordinary default.
			maxOutputTokens = defaultMaxTokens
			if strings.EqualFold(thinking, "adaptive") || strings.EqualFold(thinking, "enabled") {
				maxOutputTokens = provider.AutoOutputBudget(true, effort)
			}
		}
	}
	httpClient, err := newHTTPClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("anthropic: network: %w", err)
	}
	if reject, _ := cfg.Extra["reject_redirects"].(bool); reject {
		httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return &client{
		identityHeaders:  provider.NewClientIdentityHeaders(),
		reasoning:        ReasoningForConfig(cfg),
		name:             name,
		apiKey:           cfg.APIKey,
		keyEnv:           keyEnv,
		keySource:        keySource,
		baseURL:          root,
		requestURL:       requestURL,
		model:            cfg.Model,
		nativeAnthropic:  strings.EqualFold(root, defaultBaseURL),
		deepseek:         deepSeekReplay,
		thinking:         thinking,
		effort:           effort,
		vision:           vision,
		modelInfo:        modelInfo,
		mimo:             provider.IsMiMoEndpoint(root),
		search:           provider.SearchPolicy{NativeEnabled: webSearch, ClientEnabled: clientWebSearch},
		headers:          cleanCustomHeaders(headers),
		authHeader:       authHeader,
		defaultMaxTokens: maxOutputTokens,
		http:             httpClient, // no overall timeout; lifecycle is ctx-driven
		idleTimeout:      defaultStreamIdleTimeout,
	}, nil
}

func newHTTPClient(cfg provider.Config) (*http.Client, error) {
	spec, _ := cfg.Extra["proxy_spec"].(netclient.ProxySpec)
	return netclient.NewHTTPClient(spec, netclient.TransportOptions{
		DialTimeout:           30 * time.Second,
		KeepAlive:             30 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 300 * time.Second,
	})
}

type client struct {
	identityHeaders  http.Header
	reasoning        provider.ReasoningCapability
	name             string
	apiKey           string
	keyEnv           string // api_key_env name, surfaced in auth errors
	keySource        string // source of keyEnv, surfaced in auth errors
	baseURL          string
	requestURL       string
	model            string
	nativeAnthropic  bool   // first-party endpoint: documented default-5m cache-write pricing applies
	deepseek         bool   // official DeepSeek Anthropic endpoint: unsigned reasoning replay + automatic cache
	thinking         string // "adaptive" enables extended thinking; "" = off (config-driven)
	effort           string // output_config.effort: low|medium|high|xhigh|max; "" = provider default
	vision           bool   // model accepts image input — embed attached images as base64 image blocks
	modelInfo        provider.ModelInfo
	mimo             bool // true for MiMo — upgrades legacy tuple schemas to Draft 2020-12
	search           provider.SearchPolicy
	headers          map[string]string
	authHeader       bool // send Authorization: Bearer instead of Anthropic's x-api-key header
	defaultMaxTokens int
	http             *http.Client
	idleTimeout      time.Duration // SSE stall watchdog window; defaultStreamIdleTimeout unless a test overrides
	authed           atomic.Bool   // a request has succeeded — gate transient-401 retry
}

func (c *client) Name() string { return c.name }

func (c *client) ModelInfo() provider.ModelInfo {
	if c == nil {
		return provider.ModelInfo{}
	}
	info := c.modelInfo
	info.InputModalities = append([]provider.ModelModality(nil), info.InputModalities...)
	return info
}

func (c *client) deepSeekThinkingEnabled() bool {
	return c != nil && c.deepseek && c.thinking != "disabled" && c.effort != "disabled"
}

func (c *client) RequiresAssistantReasoningReplay(m provider.Message) bool {
	if c == nil {
		return false
	}
	if !c.deepseek {
		return c.requiresReceivedReasoning(m)
	}
	// A turn carrying provider-issued reasoning must replay it, tools or not:
	// DeepSeek's thinking mode 400s when a stored thinking block is not passed
	// back. Without stored reasoning there is nothing to replay — plain text
	// turns stay out of projection so healthy histories keep their backing.
	if strings.TrimSpace(m.ReasoningContent) != "" {
		return true
	}
	activity := len(m.ToolCalls) > 0 || len(m.ServerSearch) > 0
	return activity && c.deepSeekThinkingEnabled()
}

func (c *client) AllowsEmptyReasoningFallback() bool { return false }

func (c *client) MissingToolCallReasoningWarningIdentity() string {
	if c == nil {
		return ""
	}
	protocol := "anthropic"
	if c.deepseek {
		protocol = "deepseek-anthropic"
	}
	return strings.Join([]string{
		"anthropic", strings.TrimSpace(c.name), strings.TrimSpace(c.requestURL),
		strings.TrimSpace(c.model), protocol, strings.TrimSpace(c.thinking), strings.TrimSpace(c.effort),
	}, "\x00")
}

func (c *client) sendOpts() provider.SendOptions {
	return provider.SendOptions{
		Provider:   c.name,
		KeyEnv:     c.keyEnv,
		KeySource:  c.keySource,
		KeyPresent: c.apiKey != "",
		RetryAuth:  c.authed.Load(),
	}
}

func cleanCustomHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for name, value := range in {
		name = strings.TrimSpace(name)
		if name == "" || reservedCustomHeader(name) {
			continue
		}
		out[name] = strings.TrimSpace(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func reservedCustomHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "content-type", "accept", "x-api-key", "authorization", "anthropic-version", "anthropic-beta":
		return true
	default:
		return false
	}
}

func applyCustomHeaders(h http.Header, headers map[string]string) {
	for name, value := range cleanCustomHeaders(headers) {
		h.Set(name, value)
	}
}

// bufPool reuses byte buffers for JSON-marshalled request bodies, reducing GC
// churn from repeated alloc/free of ~10-100KB buffers per turn.
var bufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

func (c *client) Stream(ctx context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	if err := c.reasoning.Validate(c.model, req.EffortOverride); err != nil {
		return nil, err
	}
	requestCtx := provider.WithRequestAttemptCounter(ctx)
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	if err := json.NewEncoder(buf).Encode(c.buildRequest(requestCtx, req)); err != nil {
		bufPool.Put(buf)
		return nil, fmt.Errorf("%s: marshal request: %w", c.name, err)
	}
	body := make([]byte, buf.Len())
	copy(body, buf.Bytes())
	bufPool.Put(buf)

	newReq := func(ctx context.Context) (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.requestURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		if c.authHeader {
			httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
		} else {
			httpReq.Header.Set("x-api-key", c.apiKey)
		}
		httpReq.Header.Set("anthropic-version", anthropicVersion)
		if visionRequestUsesFileID(req) {
			httpReq.Header.Set("anthropic-beta", "files-api-2025-04-14")
		}
		applyCustomHeaders(httpReq.Header, c.headers)
		provider.ApplyOpenCodeGoHeaders(httpReq, c.baseURL, c.identityHeaders)
		return httpReq, nil
	}
	resp, err := provider.SendWithRetry(requestCtx, c.http, c.sendOpts(), newReq)
	if err != nil {
		return nil, provider.AnnotateToolSchemaError(err, req.Tools)
	}
	c.authed.Store(true)

	out := make(chan provider.Chunk)
	go c.readStream(requestCtx, resp, out)
	return out, nil
}

// buildRequest converts the transport-agnostic Request into the Messages API shape:
// RoleSystem messages lift to the top-level `system` field; assistant tool calls
// become `tool_use` blocks; RoleTool results become `tool_result` blocks in a user
// turn. Consecutive same-role messages are coalesced because the API requires
// alternating user/assistant turns (tool results are user turns).
func (c *client) buildRequest(ctx context.Context, req provider.Request) anthRequest {
	var system []textBlock
	var msgs []anthMessage
	// appendBlocks adds blocks under role, merging into the previous message when
	// it shares the role (keeps user/assistant strictly alternating).
	appendBlocks := func(role string, blocks ...contentBlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			msgs[n-1].Content = mergeThinkingFirst(msgs[n-1].Content, blocks)
			return
		}
		msgs = append(msgs, anthMessage{Role: role, Content: blocks})
	}

	messages := c.replayMessages(req.Messages)
	for _, m := range provider.SanitizeToolPairing(messages) {
		switch m.Role {
		case provider.RoleSystem:
			if m.Content != "" {
				system = append(system, textBlock{Type: "text", Text: m.Content})
			}
		case provider.RoleUser:
			if m.Content != "" {
				appendBlocks("user", sessionContextTextBlocks(m.Content)...)
			}
			if c.vision {
				for _, ref := range m.Images {
					if src := imageSourceFromRef(ref); src != nil {
						appendBlocks("user", contentBlock{Type: "image", Source: src})
					}
				}
			}
		case provider.RoleTool:
			content := m.Content
			if content == "" {
				content = "(no output)" // tool_result content must be non-empty
			}
			block := contentBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: content}
			if c.vision && !c.deepseek {
				if blocks := toolResultBlocks(content, m.Images); blocks != nil {
					block.Content = blocks
				}
			}
			appendBlocks("user", block)
		case provider.RoleAssistant:
			var blocks []contentBlock
			// Replay provider reasoning ahead of the content it led to. DeepSeek's
			// thinking mode requires every historical assistant turn's thinking
			// block back whenever the request declares tools — tool-call turn or
			// not — even if the current request no longer declares tools or has
			// since disabled thinking. Anthropic proper requires a signature, so
			// reasoning without one cannot be replayed on that endpoint.
			blocks = append(blocks, c.replayReasoningBlocks(m)...)
			blocks = appendServerSearchBlocks(blocks, m.ServerSearch)
			if m.Content != "" {
				blocks = append(blocks, contentBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				input := json.RawMessage(tc.Arguments)
				if len(input) == 0 {
					input = json.RawMessage("{}") // input is required, even when empty
				}
				blocks = append(blocks, contentBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: input})
			}
			appendBlocks("assistant", blocks...)
		}
	}

	tools := encodeAnthTools(c, req)
	if !c.deepseek {
		markPromptCacheBreakpoints(system, tools, msgs)
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = c.defaultMaxTokens
		if maxTokens <= 0 {
			maxTokens = defaultMaxTokens
		}
	}
	r := anthRequest{
		Model:     c.model,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  msgs,
		Tools:     tools,
		Stream:    true,
	}
	effort := c.effort
	if req.EffortOverride != "" {
		effort = req.EffortOverride
	}
	// Extended thinking is provider-specific. DeepSeek defaults to enabled and
	// accepts output_config.effort alongside its binary toggle. Anthropic proper
	// uses type=adaptive plus display/output_config. LongCat-style compatible
	// gateways use the simpler enabled|disabled knob and reject output_config.
	if c.deepseek {
		c.applyDeepSeekThinking(&r, req)
	} else {
		thinking := c.thinking
		if effort != "" && thinking == "" {
			thinking = "adaptive"
		}
		switch thinking {
		case "adaptive":
			r.Thinking = &thinkingConfig{Type: "adaptive", Display: "summarized"}
			if effort != "" {
				r.OutputConfig = &outputConfig{Effort: effort}
			}
		case "enabled", "disabled":
			t := c.thinking
			if effort == "enabled" || effort == "disabled" {
				t = effort
			}
			r.Thinking = &thinkingConfig{Type: t}
		}
	}
	return r
}

// readStream parses the Messages API SSE stream into Chunks. Text deltas emit live;
// each tool_use content block emits a ChunkToolCallStart when its id+name are known
// and a complete ChunkToolCall when the block closes; usage is assembled from
// message_start/message_delta usage (compatible gateways may put every counter
// in the final delta) and emitted once before ChunkDone.
func (c *client) readStream(ctx context.Context, resp *http.Response, out chan<- provider.Chunk) {
	defer resp.Body.Close()
	defer close(out)

	// Close the body if the stream stalls past c.idleTimeout so scanner.Scan()
	// unblocks instead of hanging on a half-open connection. The watchdog owns the
	// timer; the read loop only pings the buffered activity channel (no Timer.Reset
	// race). A context cancel already unblocks the scan via the transport.
	idleTimeout := c.idleTimeout
	if idleTimeout <= 0 { // zero-value client (constructed without New)
		idleTimeout = defaultStreamIdleTimeout
	}
	done := make(chan struct{})
	defer close(done)
	activity := make(chan struct{}, 1)
	var stalled atomic.Bool
	go func() {
		idle := time.NewTimer(idleTimeout)
		defer idle.Stop()
		for {
			select {
			case <-ctx.Done():
				resp.Body.Close()
				return
			case <-idle.C:
				stalled.Store(true)
				resp.Body.Close()
				return
			case <-activity:
				if !idle.Stop() {
					select {
					case <-idle.C:
					default:
					}
				}
				idle.Reset(idleTimeout)
			case <-done:
				return
			}
		}
	}()

	send := func(chunk provider.Chunk) bool {
		return sendChunk(ctx, out, chunk)
	}

	tools := map[int]*provider.ToolCall{} // tool_use blocks, keyed by content index
	searches := newSearchStream()
	thinking := map[int]*provider.ThinkingBlock{}
	argBuckets := map[int]int{} // last emitted 2KB progress bucket per block
	var inTok, outTok, cacheCreate, cacheRead int
	var stopReason string
	haveUsage := false
	mergeUsage := func(usage *wireUsage) {
		if usage == nil {
			return
		}
		// The native Anthropic stream reports input/cache counters in
		// message_start and output_tokens in message_delta. Compatible gateways
		// such as LongCat report all counters in message_delta instead. Counters
		// are cumulative and non-negative, so retaining the largest value also
		// tolerates gateways that repeat partial usage in both events.
		inTok = max(inTok, usage.InputTokens)
		outTok = max(outTok, usage.OutputTokens)
		cacheCreate = max(cacheCreate, usage.CacheCreationInputTokens)
		cacheRead = max(cacheRead, usage.CacheReadInputTokens)
		haveUsage = true
	}

	scanner := provider.NewStreamScanner(resp.Body, 1024*1024)

	for scanner.Scan() {
		select { // ping the idle watchdog; non-blocking so a full buffer is fine
		case activity <- struct{}{}:
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		// SSE carries `event:` and `data:` lines; the data JSON's own `type` field
		// is authoritative, so we only need the data payloads.
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}

		var ev streamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			send(provider.Chunk{Type: provider.ChunkError, Err: scanner.DecodeError(c.name, data, err)})
			return
		}

		if !updateThinkingStream(thinking, ev, send) {
			return
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil && ev.Message.Usage != nil {
				mergeUsage(ev.Message.Usage)
			}
		case "content_block_start":
			if chunk := beginContentBlock(ev.Index, ev.ContentBlock, tools, searches); chunk != nil {
				if !send(*chunk) {
					return
				}
			}
		case "content_block_delta":
			if ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					if !send(provider.Chunk{Type: provider.ChunkText, Text: ev.Delta.Text}) {
						return
					}
				}
			case "input_json_delta":
				if tc := tools[ev.Index]; tc != nil {
					tc.Arguments += ev.Delta.PartialJSON
					// Progress ticks for large streaming argument payloads, one
					// per 2KB bucket (see the openai provider for rationale).
					if bucket := len(tc.Arguments) / 2048; bucket > argBuckets[ev.Index] {
						argBuckets[ev.Index] = bucket
						if !send(provider.Chunk{Type: provider.ChunkToolCallArgsDelta, ToolCall: &provider.ToolCall{ID: tc.ID, Name: tc.Name}, ArgChars: len(tc.Arguments)}) {
							return
						}
					}
				}
				if next := searches.argsDelta(ev.Index, ev.Delta.PartialJSON); next != nil {
					if !send(provider.Chunk{Type: provider.ChunkServerSearch, ServerSearch: next}) {
						return
					}
				}
			case "web_search_tool_result_delta":
				// Some DeepSeek-compatible streams deliver the result array in a
				// delta instead of the block-start content; without this the card
				// stays empty and the model-written source list is all that shows.
				if next := searches.resultsDelta(ev.Index, ev.Delta.WebSearchResults); next != nil {
					if !send(provider.Chunk{Type: provider.ChunkServerSearch, ServerSearch: next}) {
						return
					}
				}
			}
		case "content_block_stop":
			if tc := tools[ev.Index]; tc != nil {
				if !send(provider.Chunk{Type: provider.ChunkToolCall, ToolCall: tc}) {
					return
				}
				delete(tools, ev.Index)
			}
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				stopReason = ev.Delta.StopReason
			}
			mergeUsage(ev.Usage)
		case "message_stop":
			// Anthropic's terminal event. Tool blocks may already have closed;
			// without this, the attempt stays speculative and is not committed.
			// Stop reading immediately so a post-terminal connection reset cannot
			// reclassify a complete response as interrupted.
			goto finalize
		case "error":
			msg := "stream error"
			if ev.Error != nil && ev.Error.Message != "" {
				msg = ev.Error.Message
			}
			send(provider.Chunk{Type: provider.ChunkError, Err: fmt.Errorf("%s: %s", c.name, msg)})
			return
		}
	}

	if ctx.Err() != nil {
		return
	}
	if err := streamScanEndError(c.name, idleTimeout, stalled.Load(), scanner.Err(), stopReason); err != nil {
		send(provider.Chunk{Type: provider.ChunkError, Err: err})
		return
	}
	goto finalize

finalize:
	if len(thinking) > 0 {
		send(provider.Chunk{Type: provider.ChunkReasoning, ReasoningState: provider.ReasoningIncomplete})
	}
	if haveUsage {
		cacheWriteBilledTokens := 0.0
		if cacheCreate > 0 && c.nativeAnthropic {
			cacheWriteBilledTokens = float64(cacheCreate) * cacheWrite5MinuteInputMultiplier
		}
		usage := &provider.Usage{
			PromptTokens:           inTok + cacheCreate + cacheRead,
			CompletionTokens:       outTok,
			TotalTokens:            inTok + cacheCreate + cacheRead + outTok,
			CacheHitTokens:         cacheRead,
			CacheMissTokens:        inTok + cacheCreate,
			CacheWriteTokens:       cacheCreate,
			CacheWriteBilledTokens: cacheWriteBilledTokens,
			FinishReason:           mapStopReason(stopReason),
		}
		provider.ApplyRequestAttemptCount(ctx, usage)
		if !send(provider.Chunk{Type: provider.ChunkUsage, Usage: usage}) {
			return
		}
	}
	send(provider.Chunk{Type: provider.ChunkDone})
}

func sendChunk(ctx context.Context, out chan<- provider.Chunk, chunk provider.Chunk) bool {
	select {
	case out <- chunk:
		return true
	default:
	}
	select {
	case <-ctx.Done():
		return false
	case out <- chunk:
		return true
	}
}

// mapStopReason translates Anthropic stop reasons to the OpenAI-style finish
// reasons the agent already recognises (it surfaces abnormal ones like "length").
func mapStopReason(s string) string {
	switch s {
	case "end_turn", "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	default:
		return s // "refusal", "pause_turn", "" — pass through
	}
}

// Messages API wire protocol

const cacheWrite5MinuteInputMultiplier = 1.25

func ephemeral() *cacheControl { return &cacheControl{Type: "ephemeral"} }

type cacheControl struct {
	Type string `json:"type"`
}

type anthRequest struct {
	Model        string          `json:"model"`
	MaxTokens    int             `json:"max_tokens"`
	System       []textBlock     `json:"system,omitempty"`
	Messages     []anthMessage   `json:"messages"`
	Tools        []anthTool      `json:"tools,omitempty"`
	Temperature  *float64        `json:"temperature,omitempty"`
	Thinking     *thinkingConfig `json:"thinking,omitempty"`
	OutputConfig *outputConfig   `json:"output_config,omitempty"`
	Stream       bool            `json:"stream"`
}

type thinkingConfig struct {
	Type    string `json:"type"`              // "adaptive"
	Display string `json:"display,omitempty"` // "summarized" to stream the reasoning text
}

type outputConfig struct {
	Effort string `json:"effort,omitempty"` // low | high | max
}

type textBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type anthMessage struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

// contentBlock is the union of the block kinds we emit in a request: text,
// tool_use (echoing a prior assistant call), and tool_result. Unused fields are
// omitted so each block serialises to its canonical shape.
type contentBlock struct {
	Data         string          `json:"data,omitempty"`
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`        // text
	Thinking     string          `json:"thinking,omitempty"`    // thinking
	Signature    string          `json:"signature,omitempty"`   // thinking
	ID           string          `json:"id,omitempty"`          // tool_use
	Name         string          `json:"name,omitempty"`        // tool_use
	Input        json.RawMessage `json:"input,omitempty"`       // tool_use
	ToolUseID    string          `json:"tool_use_id,omitempty"` // tool_result
	Content      any             `json:"content,omitempty"`     // tool_result: string, or []contentBlock when the result carries images
	Source       *imageSource    `json:"source,omitempty"`      // image
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

type imageSource struct {
	Type      string `json:"type"` // base64 | url | file
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
	FileID    string `json:"file_id,omitempty"`
}

// toolResultBlocks builds array content for a tool_result whose message carries
// images: the text first, then one image block per parseable data URL. It
// returns nil when nothing parses, so text-only results keep plain string
// content — byte-identical serialization to previous releases.
func toolResultBlocks(text string, images []string) []contentBlock {
	var imgs []contentBlock
	for _, url := range images {
		if mt, data, ok := provider.ParseImageDataURL(url); ok {
			imgs = append(imgs, contentBlock{Type: "image", Source: &imageSource{Type: "base64", MediaType: mt, Data: data}})
		}
	}
	if imgs == nil {
		return nil
	}
	return append([]contentBlock{{Type: "text", Text: text}}, imgs...)
}

type anthTool struct {
	Type         string          `json:"type,omitempty"` // "web_search" for server-side search; empty for named tools
	Name         string          `json:"name,omitempty"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	Strict       bool            `json:"strict,omitempty"`
	DeferLoading bool            `json:"defer_loading,omitempty"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

// streamEvent is the discriminated SSE event; read the fields matching Type.
type streamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage *wireUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *streamContentBlock `json:"content_block"`
	Delta        *struct {
		Type             string          `json:"type"`         // text_delta | thinking_delta | signature_delta | input_json_delta | web_search_tool_result_delta
		Text             string          `json:"text"`         // text_delta
		Thinking         string          `json:"thinking"`     // thinking_delta
		Signature        string          `json:"signature"`    // signature_delta
		PartialJSON      string          `json:"partial_json"` // input_json_delta
		StopReason       string          `json:"stop_reason"`  // message_delta
		WebSearchResults json.RawMessage `json:"results"`      // web_search_tool_result_delta
	} `json:"delta"`
	Usage *wireUsage `json:"usage"` // message_delta (cumulative output_tokens)
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type wireUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}
