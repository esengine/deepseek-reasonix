package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

// TestBuildRequest covers the protocol conversion: system lift, tool_use /
// tool_result blocks, coalescing consecutive tool results into one user turn,
// cache_control placement, and the max_tokens fallback.
func TestBuildRequest(t *testing.T) {
	c := &client{name: "anthropic", model: "claude-opus-4-8"}
	req := provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "You are helpful."},
			{Role: provider.RoleUser, Content: "weather in Paris and Berlin?"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
				{ID: "t1", Name: "get_weather", Arguments: `{"city":"Paris"}`},
				{ID: "t2", Name: "get_weather", Arguments: `{"city":"Berlin"}`},
			}},
			{Role: provider.RoleTool, ToolCallID: "t1", Content: "sunny"},
			{Role: provider.RoleTool, ToolCallID: "t2", Content: "cloudy"},
		},
		Tools: []provider.ToolSchema{{Name: "get_weather", Description: "w", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
	r := c.buildRequest(context.Background(), req)

	if r.Model != "claude-opus-4-8" {
		t.Fatalf("model = %q", r.Model)
	}
	if r.MaxTokens != defaultMaxTokens {
		t.Fatalf("max_tokens = %d, want default %d", r.MaxTokens, defaultMaxTokens)
	}
	// System lifted to the top level, with a cache breakpoint on its last block.
	if len(r.System) != 1 || r.System[0].Text != "You are helpful." {
		t.Fatalf("system = %+v", r.System)
	}
	if r.System[0].CacheControl == nil {
		t.Fatal("system block should carry cache_control")
	}
	// System present ⇒ the tool does NOT also get a breakpoint (system caches tools).
	if r.Tools[0].CacheControl != nil {
		t.Fatal("tool should not carry cache_control when system does")
	}
	// user, assistant(tool_use ×2), user(tool_result ×2 coalesced) = 3 messages.
	if len(r.Messages) != 3 {
		t.Fatalf("want 3 messages, got %d: %+v", len(r.Messages), r.Messages)
	}
	if r.Messages[0].Role != "user" || r.Messages[0].Content[0].Text != "weather in Paris and Berlin?" {
		t.Fatalf("msg[0] = %+v", r.Messages[0])
	}
	if r.Messages[1].Role != "assistant" || len(r.Messages[1].Content) != 2 ||
		r.Messages[1].Content[0].Type != "tool_use" || r.Messages[1].Content[0].ID != "t1" ||
		string(r.Messages[1].Content[0].Input) != `{"city":"Paris"}` {
		t.Fatalf("msg[1] = %+v", r.Messages[1])
	}
	last := r.Messages[2]
	if last.Role != "user" || len(last.Content) != 2 {
		t.Fatalf("tool results should coalesce into one user turn: %+v", last)
	}
	if last.Content[0].Type != "tool_result" || last.Content[0].ToolUseID != "t1" || last.Content[0].Content != "sunny" {
		t.Fatalf("tool_result[0] = %+v", last.Content[0])
	}
	if last.Content[1].ToolUseID != "t2" {
		t.Fatalf("tool_result[1] = %+v", last.Content[1])
	}
	// Conversation cache breakpoint on the last block of the last message.
	if last.Content[len(last.Content)-1].CacheControl == nil {
		t.Fatal("last message block should carry cache_control")
	}
}

// TestBuildRequestNoSystem checks the breakpoint falls back to the last tool when
// there is no system message.
func TestBuildRequestNoSystem(t *testing.T) {
	c := &client{model: "claude-opus-4-8"}
	r := c.buildRequest(context.Background(), provider.Request{
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
		Tools:     []provider.ToolSchema{{Name: "a"}, {Name: "b"}},
		MaxTokens: 1000,
	})
	if r.MaxTokens != 1000 {
		t.Fatalf("explicit max_tokens should win: %d", r.MaxTokens)
	}
	if r.Tools[1].CacheControl == nil {
		t.Fatal("last tool should carry cache_control when there is no system")
	}
	// A tool with no schema gets a minimal valid object schema.
	if string(r.Tools[0].InputSchema) != `{"type":"object","properties":{}}` {
		t.Fatalf("empty schema not defaulted: %s", r.Tools[0].InputSchema)
	}
}

func TestBuildRequestKeepsDefaultCacheControlBytesStable(t *testing.T) {
	c := &client{model: "claude-opus-4-8"}
	req := provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}}
	want, err := json.Marshal(c.buildRequest(context.Background(), req))
	if err != nil {
		t.Fatalf("marshal first request: %v", err)
	}
	requestCtx := t.Context()
	got, err := json.Marshal(c.buildRequest(requestCtx, req))
	if err != nil {
		t.Fatalf("marshal second request: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("request bytes changed with unrelated context:\nfirst:  %s\nsecond: %s", want, got)
	}
	if strings.Contains(string(got), `"ttl"`) {
		t.Fatalf("default cache_control unexpectedly opted into a TTL: %s", got)
	}
}

func TestCacheControlOmitsTTLByDefault(t *testing.T) {
	b, err := json.Marshal(ephemeral())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `{"type":"ephemeral"}` {
		t.Fatalf("default cache_control = %s, want byte-identical to every prior release", b)
	}
}

func TestConfiguredMaxOutputTokensRespectsMandatoryAnthropicFallback(t *testing.T) {
	configured, err := New(provider.Config{
		Name: "anthropic", Model: "claude-opus-4-8",
		Extra: map[string]any{"max_output_tokens": 8192},
	})
	if err != nil {
		t.Fatalf("New configured provider: %v", err)
	}
	if got := configured.(*client).buildRequest(context.Background(), provider.Request{}).MaxTokens; got != 8192 {
		t.Fatalf("configured max_tokens = %d, want 8192", got)
	}

	disabled, err := New(provider.Config{
		Name: "anthropic", Model: "claude-opus-4-8",
		Extra: map[string]any{"max_output_tokens": -1},
	})
	if err != nil {
		t.Fatalf("New disabled provider: %v", err)
	}
	if got := disabled.(*client).buildRequest(context.Background(), provider.Request{}).MaxTokens; got != defaultMaxTokens {
		t.Fatalf("mandatory max_tokens fallback = %d, want %d", got, defaultMaxTokens)
	}
}

func TestNewSelectsMaxOutputTokenDefaultByEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseURL string
		extra   map[string]any
		want    int
	}{
		{name: "native anthropic", want: provider.DefaultOrdinaryOutputTokens},
		{name: "unknown compatible gateway", baseURL: "https://proxy.example.com/anthropic", want: provider.DefaultOrdinaryOutputTokens},
		{name: "official deepseek", baseURL: "https://api.deepseek.com/anthropic", want: provider.DeepSeekMaxOutputTokens},
		{name: "official deepseek high", baseURL: "https://api.deepseek.com/anthropic", extra: map[string]any{"effort": "high"}, want: provider.DeepSeekMaxOutputTokens},
		{name: "official deepseek thinking off", baseURL: "https://api.deepseek.com/anthropic", extra: map[string]any{"effort": "disabled"}, want: provider.DeepSeekMaxOutputTokens},
		{name: "explicit override", baseURL: "https://api.deepseek.com/anthropic", extra: map[string]any{"max_output_tokens": 8192}, want: 8192},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := New(provider.Config{
				Name:    "test",
				BaseURL: tc.baseURL,
				Model:   "model",
				Extra:   tc.extra,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got := p.(*client).defaultMaxTokens; got != tc.want {
				t.Fatalf("defaultMaxTokens = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestStreamAnnotatesIndexedToolSchemaError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Tool 1 function has invalid 'parameters' schema"}}`))
	}))
	defer srv.Close()

	p, err := New(provider.Config{Name: "mimo-anthropic", BaseURL: srv.URL, Model: "mimo-v2.5-pro", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.Stream(context.Background(), provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
		Tools: []provider.ToolSchema{
			{Name: "read_file", Parameters: json.RawMessage(`{"type":"object"}`)},
			{Name: "mcp__files__search", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
	})
	var apiErr *provider.APIError
	if !errors.As(err, &apiErr) || !strings.Contains(apiErr.ToolContext, `MCP server "files"`) {
		t.Fatalf("Stream error = %v, want MCP tool source context", err)
	}
}

func TestBuildRequestScopesLegacyTupleMigrationToMiMo(t *testing.T) {
	legacy := json.RawMessage(`{"type":"object","properties":{"pair":{"type":"array","items":[{"type":"string"},{"type":"number"}]}}}`)
	req := provider.Request{Tools: []provider.ToolSchema{{Name: "tuple", Parameters: legacy}}}

	mimo := (&client{mimo: true}).buildRequest(context.Background(), req)
	if got := string(mimo.Tools[0].InputSchema); !strings.Contains(got, `"prefixItems"`) || strings.Contains(got, `"items":[`) {
		t.Fatalf("MiMo parameters = %s, want Draft 2020-12 tuple keywords", got)
	}

	other := (&client{}).buildRequest(context.Background(), req)
	if got := string(other.Tools[0].InputSchema); got != string(legacy) {
		t.Fatalf("non-MiMo parameters changed:\n got: %s\nwant: %s", got, legacy)
	}
}

func TestNewDetectsMiMoSchemaDialect(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		want    bool
	}{
		{"https://api.xiaomimimo.com/anthropic", true},
		{"https://token-plan-cn.xiaomimimo.com/anthropic", true},
		{"https://token-plan-sgp.xiaomimimo.com/anthropic", true},
		{"https://token-plan-ams.xiaomimimo.com/anthropic", true},
		{"https://api.anthropic.com", false},
		{"https://api.minimaxi.com/anthropic", false},
	} {
		p, err := New(provider.Config{Name: "test", BaseURL: tc.baseURL, Model: "model"})
		if err != nil {
			t.Fatalf("New(%q): %v", tc.baseURL, err)
		}
		if got := p.(*client).mimo; got != tc.want {
			t.Errorf("New(%q).mimo = %v, want %v", tc.baseURL, got, tc.want)
		}
	}
}

func TestNewDetectsOfficialDeepSeekEndpoint(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		want    bool
	}{
		{"https://api.deepseek.com/anthropic", true},
		{"https://api.deepseek.com/anthropic/v1", true},
		{"https://proxy.example.com/anthropic", false},
	} {
		p, err := New(provider.Config{Name: "test", BaseURL: tc.baseURL, Model: "deepseek-v4-flash"})
		if err != nil {
			t.Fatalf("New(%q): %v", tc.baseURL, err)
		}
		if got := p.(*client).deepseek; got != tc.want {
			t.Errorf("New(%q).deepseek = %v, want %v", tc.baseURL, got, tc.want)
		}
	}
}

func TestNewScopesNativeCacheWritePricingToAnthropic(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseURL string
		want    bool
	}{
		{name: "default", want: true},
		{name: "official v1", baseURL: "https://api.anthropic.com/v1", want: true},
		{name: "compatible gateway", baseURL: "https://proxy.example.com/anthropic", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := New(provider.Config{Name: "test", BaseURL: tc.baseURL, Model: "claude-sonnet-4-6"})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got := p.(*client).nativeAnthropic; got != tc.want {
				t.Fatalf("nativeAnthropic = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMapStopReason(t *testing.T) {
	cases := map[string]string{
		"end_turn":      "stop",
		"stop_sequence": "stop",
		"tool_use":      "tool_calls",
		"max_tokens":    "length",
		"refusal":       "refusal",
		"":              "",
	}
	for in, want := range cases {
		if got := mapStopReason(in); got != want {
			t.Errorf("mapStopReason(%q) = %q, want %q", in, got, want)
		}
	}
}

const sseFixture = `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":100,"cache_creation_input_tokens":0,"cache_read_input_tokens":50,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":25}}

event: message_stop
data: {"type":"message_stop"}
`

// TestReadStream feeds a canned Messages API SSE stream through readStream and
// asserts the emitted chunk sequence: text deltas, a tool-call start + complete,
// a usage record, then done.
func TestReadStream(t *testing.T) {
	c := &client{name: "anthropic"}
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(sseFixture))}
	ch := make(chan provider.Chunk)
	go c.readStream(context.Background(), resp, ch)

	var text strings.Builder
	var started, full *provider.ToolCall
	var usage *provider.Usage
	done := false
	for ck := range ch {
		switch ck.Type {
		case provider.ChunkText:
			text.WriteString(ck.Text)
		case provider.ChunkToolCallStart:
			started = ck.ToolCall
		case provider.ChunkToolCall:
			full = ck.ToolCall
		case provider.ChunkUsage:
			usage = ck.Usage
		case provider.ChunkDone:
			done = true
		case provider.ChunkError:
			t.Fatalf("unexpected error chunk: %v", ck.Err)
		}
	}

	if text.String() != "Hello world" {
		t.Fatalf("text = %q", text.String())
	}
	if started == nil || started.ID != "toolu_1" || started.Name != "get_weather" {
		t.Fatalf("tool start = %+v", started)
	}
	if full == nil || full.Arguments != `{"city":"Paris"}` {
		t.Fatalf("tool full = %+v", full)
	}
	switch {
	case usage == nil:
		t.Fatal("expected a usage chunk")
	case usage.PromptTokens != 150 || usage.CompletionTokens != 25 || usage.TotalTokens != 175:
		t.Fatalf("usage tokens = %+v", usage)
	case usage.CacheHitTokens != 50 || usage.CacheMissTokens != 100:
		t.Fatalf("usage cache = hit %d miss %d", usage.CacheHitTokens, usage.CacheMissTokens)
	case usage.FinishReason != "tool_calls":
		t.Fatalf("finish reason = %q", usage.FinishReason)
	}
	if !done {
		t.Fatal("expected a done chunk")
	}
}

// TestReadStreamIgnoresTransportErrorAfterMessageStop: a complete stream that
// already received message_stop must finalize successfully even if the
// connection resets while draining the rest of the body.
func TestReadStreamIgnoresTransportErrorAfterMessageStop(t *testing.T) {
	// message_stop arrives; the body then ends abruptly. We must not surface
	// StreamInterruptedError after a clean terminal.
	pr, pw := io.Pipe()
	go func() {
		_, _ = io.WriteString(pw, `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":5}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}
`)
		// Simulate a post-terminal reset while the client might still be reading.
		_ = pw.CloseWithError(io.ErrUnexpectedEOF)
	}()
	c := &client{name: "anthropic"}
	resp := &http.Response{Body: pr}
	ch := make(chan provider.Chunk)
	go c.readStream(context.Background(), resp, ch)

	var text strings.Builder
	var sawDone, sawErr bool
	for ck := range ch {
		switch ck.Type {
		case provider.ChunkText:
			text.WriteString(ck.Text)
		case provider.ChunkDone:
			sawDone = true
		case provider.ChunkError:
			sawErr = true
			t.Fatalf("post-terminal transport error must not surface: %v", ck.Err)
		}
	}
	if text.String() != "ok" || !sawDone || sawErr {
		t.Fatalf("text=%q done=%v err=%v", text.String(), sawDone, sawErr)
	}
}

// TestReadStreamRequiresMessageStop: EOF after a complete tool block but before
// message_stop or message_delta.stop_reason must surface StreamInterruptedError
// so the attempt stays uncommitted (tool calls remain speculative).
func TestReadStreamRequiresMessageStop(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":10}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"bash"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"ls\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}
`
	c := &client{name: "anthropic"}
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(sse))}
	ch := make(chan provider.Chunk)
	go c.readStream(context.Background(), resp, ch)

	var gotInterrupted bool
	var sawDone bool
	for ck := range ch {
		switch ck.Type {
		case provider.ChunkDone:
			sawDone = true
		case provider.ChunkError:
			var interrupted *provider.StreamInterruptedError
			gotInterrupted = errors.As(ck.Err, &interrupted)
		}
	}
	if sawDone {
		t.Fatal("must not emit ChunkDone without message_stop")
	}
	if !gotInterrupted {
		t.Fatal("EOF before message_stop must surface StreamInterruptedError")
	}
}

// LongCat's Anthropic-compatible SSE stream can omit message_start.usage and
// report the complete usage object in message_delta. Those input/cache counters
// must not disappear from Reasonix metrics and billing estimates.
func TestReadStreamUsageFromMessageDelta(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":13,"output_tokens":3,"cache_creation_input_tokens":5,"cache_read_input_tokens":7}}

event: message_stop
data: {"type":"message_stop"}
`
	c := &client{name: "longcat-anthropic"}
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(sse))}
	ch := make(chan provider.Chunk)
	go c.readStream(context.Background(), resp, ch)

	var usage *provider.Usage
	for ck := range ch {
		if ck.Type == provider.ChunkError {
			t.Fatalf("unexpected error chunk: %v", ck.Err)
		}
		if ck.Type == provider.ChunkUsage {
			usage = ck.Usage
		}
	}
	if usage == nil {
		t.Fatal("expected a usage chunk")
	}
	if usage.PromptTokens != 25 || usage.CompletionTokens != 3 || usage.TotalTokens != 28 {
		t.Fatalf("usage tokens = %+v", usage)
	}
	if usage.CacheHitTokens != 7 || usage.CacheMissTokens != 18 {
		t.Fatalf("usage cache = hit %d miss %d", usage.CacheHitTokens, usage.CacheMissTokens)
	}
	if usage.CacheWriteTokens != 5 || usage.CacheWriteBilledTokens != 0 {
		t.Fatalf("compatible-gateway cache write = raw %d billed %v, want 5/0", usage.CacheWriteTokens, usage.CacheWriteBilledTokens)
	}
	if usage.FinishReason != "stop" {
		t.Fatalf("finish reason = %q", usage.FinishReason)
	}
}

func TestReadStreamPricesNativeCacheWritesAtDefaultTTL(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":3,"cache_creation_input_tokens":5,"cache_read_input_tokens":7,"output_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}
`
	c := &client{name: "anthropic", nativeAnthropic: true}
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(sse))}
	ch := make(chan provider.Chunk)
	go c.readStream(context.Background(), resp, ch)

	var usage *provider.Usage
	for ck := range ch {
		if ck.Type == provider.ChunkError {
			t.Fatalf("unexpected error chunk: %v", ck.Err)
		}
		if ck.Type == provider.ChunkUsage {
			usage = ck.Usage
		}
	}
	if usage == nil {
		t.Fatal("expected a usage chunk")
	}
	if usage.CacheWriteTokens != 5 || usage.CacheWriteBilledTokens != 6.25 {
		t.Fatalf("usage cache write = raw %d billed %v, want 5/6.25", usage.CacheWriteTokens, usage.CacheWriteBilledTokens)
	}
}

// TestReadStreamError surfaces a mid-stream error event as a ChunkError.
func TestReadStreamError(t *testing.T) {
	sse := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"overloaded\"}}\n\n"
	c := &client{name: "anthropic"}
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(sse))}
	ch := make(chan provider.Chunk)
	go c.readStream(context.Background(), resp, ch)

	var gotErr error
	for ck := range ch {
		if ck.Type == provider.ChunkError {
			gotErr = ck.Err
		}
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "overloaded") {
		t.Fatalf("expected an error chunk mentioning overloaded, got %v", gotErr)
	}
}

// TestBuildRequestThinking checks that, with thinking enabled, the request carries
// the adaptive thinking + effort config and the prior assistant turn's signed
// thinking block is replayed first (before its tool_use).
func TestBuildRequestThinking(t *testing.T) {
	c := &client{model: "claude-opus-4-8", thinking: "adaptive", effort: "high"}
	r := c.buildRequest(context.Background(), provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "weather?"},
			{Role: provider.RoleAssistant, ReasoningContent: "Let me check.", ReasoningSignature: "sig-abc",
				ToolCalls: []provider.ToolCall{{ID: "t1", Name: "get_weather", Arguments: `{"city":"Paris"}`}}},
			{Role: provider.RoleTool, ToolCallID: "t1", Content: "sunny"},
		},
	})
	if r.Thinking == nil || r.Thinking.Type != "adaptive" || r.Thinking.Display != "summarized" {
		t.Fatalf("thinking config = %+v", r.Thinking)
	}
	if r.OutputConfig == nil || r.OutputConfig.Effort != "high" {
		t.Fatalf("output config = %+v", r.OutputConfig)
	}
	asst := r.Messages[1]
	if asst.Role != "assistant" || len(asst.Content) != 2 {
		t.Fatalf("assistant msg = %+v", asst)
	}
	if asst.Content[0].Type != "thinking" || asst.Content[0].Thinking != "Let me check." || asst.Content[0].Signature != "sig-abc" {
		t.Fatalf("first block should be the signed thinking block: %+v", asst.Content[0])
	}
	if asst.Content[1].Type != "tool_use" {
		t.Fatalf("tool_use should follow the thinking block: %+v", asst.Content[1])
	}
}

func TestBuildRequestOmitsResolvedToolCallMetadata(t *testing.T) {
	readOnly := false
	c := &client{model: "claude-opus-4-8"}
	req := c.buildRequest(context.Background(), provider.Request{Messages: []provider.Message{{
		Role: provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{
			ID: "call_1", Name: "use_capability", Arguments: `{}`,
			ResolvedName: "mcp__db__write", CapabilityID: "mcp-tool:db/write",
			ResolvedReadOnly: &readOnly,
		}},
	}}})
	b, err := json.Marshal(req.Messages)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"resolved_name", "resolvedName", "capability_id", "capabilityId", "resolved_read_only", "resolvedReadOnly", "mcp__db__write"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("provider request leaked local tool metadata %q: %s", forbidden, b)
		}
	}
	if !strings.Contains(string(b), `"name":"use_capability"`) {
		t.Fatalf("provider request lost stable proxy name: %s", b)
	}
}

func TestBuildRequestThinkingEnabledGateway(t *testing.T) {
	c := &client{model: "LongCat-2.0", thinking: "enabled", effort: "disabled"}
	r := c.buildRequest(context.Background(), provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "hi"},
			{Role: provider.RoleAssistant, Content: "ok", ReasoningContent: "signed reasoning", ReasoningSignature: "sig"},
		},
	})
	if r.Thinking == nil || r.Thinking.Type != "disabled" || r.Thinking.Display != "" {
		t.Fatalf("thinking config = %+v, want disabled without display", r.Thinking)
	}
	if r.OutputConfig != nil {
		t.Fatalf("enabled/disabled gateway thinking must omit output_config: %+v", r.OutputConfig)
	}
	for _, block := range r.Messages[1].Content {
		if block.Type == "thinking" {
			t.Fatalf("enabled/disabled gateway must not replay Anthropic signed thinking blocks: %+v", r.Messages[1])
		}
	}
}

func TestBuildRequestDeepSeekThinking(t *testing.T) {
	c := &client{model: "deepseek-v4-flash", deepseek: true, thinking: "enabled", effort: "max"}
	r := c.buildRequest(context.Background(), provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "stable system"},
			{Role: provider.RoleUser, Content: "weather?"},
			{Role: provider.RoleAssistant, ReasoningContent: "I should call the tool.",
				ToolCalls: []provider.ToolCall{{ID: "t1", Name: "get_weather", Arguments: `{"city":"Paris"}`}}},
			{Role: provider.RoleTool, ToolCallID: "t1", Content: "sunny"},
		},
		Tools: []provider.ToolSchema{{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})

	if !provider.RequiresToolCallReasoning(c) || provider.RequiresReasoningRoundTrip(c) {
		t.Fatal("DeepSeek thinking must preserve tool-call reasoning without retaining ordinary-turn reasoning")
	}
	if r.Thinking == nil || r.Thinking.Type != "enabled" || r.Thinking.Display != "" {
		t.Fatalf("thinking config = %+v, want enabled without Anthropic display", r.Thinking)
	}
	if r.OutputConfig == nil || r.OutputConfig.Effort != "max" {
		t.Fatalf("output_config = %+v, want max", r.OutputConfig)
	}
	asst := r.Messages[1]
	if len(asst.Content) != 2 || asst.Content[0].Type != "thinking" || asst.Content[0].Thinking != "I should call the tool." || asst.Content[0].Signature != "" || asst.Content[1].Type != "tool_use" {
		t.Fatalf("DeepSeek assistant blocks = %+v, want unsigned thinking before tool_use", asst.Content)
	}
	if r.System[0].CacheControl != nil || r.Tools[0].CacheControl != nil {
		t.Fatal("DeepSeek ignores cache_control; system/tools must omit it")
	}
	for _, message := range r.Messages {
		for _, block := range message.Content {
			if block.CacheControl != nil {
				t.Fatalf("DeepSeek message block unexpectedly carries cache_control: %+v", block)
			}
		}
	}
}

func TestMissingToolCallReasoningWarningFingerprintTracksAnthropicConfiguration(t *testing.T) {
	first := &client{name: "deepseek", baseURL: "https://api.deepseek.com/anthropic", model: "deepseek-v4-pro", deepseek: true, thinking: "enabled", effort: "high"}
	same := &client{name: "deepseek", baseURL: "https://api.deepseek.com/anthropic", model: "deepseek-v4-pro", deepseek: true, thinking: "enabled", effort: "high"}
	flash := &client{name: "deepseek", baseURL: "https://api.deepseek.com/anthropic", model: "deepseek-v4-flash", deepseek: true, thinking: "enabled", effort: "high"}
	got := provider.MissingToolCallReasoningWarningFingerprint(first)
	if got != provider.MissingToolCallReasoningWarningFingerprint(same) {
		t.Fatal("equivalent Anthropic configurations produced different fingerprints")
	}
	if got == provider.MissingToolCallReasoningWarningFingerprint(flash) {
		t.Fatal("Anthropic model change did not re-key the warning fingerprint")
	}
}

func TestBuildRequestDeepSeekThinkingModes(t *testing.T) {
	for _, tc := range []struct {
		name, model, input, want string
	}{
		{name: "Flash low", model: "deepseek-v4-flash", input: "low", want: "low"},
		{name: "Flash legacy medium", model: "deepseek-v4-flash", input: "medium", want: "high"},
		{name: "Flash legacy xhigh", model: "deepseek-v4-flash", input: "xhigh", want: "high"},
		{name: "Pro low", model: "deepseek-v4-pro", input: "low", want: "low"},
		{name: "Pro legacy medium", model: "deepseek-v4-pro", input: "medium", want: "high"},
		{name: "Pro legacy xhigh", model: "deepseek-v4-pro", input: "xhigh", want: "high"},
		{name: "Sonnet alias uses Flash", model: "claude-sonnet-4-6", input: "low", want: "low"},
		{name: "Opus alias legacy xhigh", model: "claude-opus-4-8", input: "xhigh", want: "high"},
		{name: "unknown model falls back to Flash", model: "unknown-model", input: "xhigh", want: "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.input != tc.want {
				_, err := New(provider.Config{BaseURL: "https://api.deepseek.com/anthropic", Model: tc.model, Extra: map[string]any{"effort": tc.input}})
				if err == nil {
					t.Fatal("undeclared alias accepted")
				}
				return
			}
			r := (&client{model: tc.model, deepseek: true, effort: tc.input}).buildRequest(context.Background(), provider.Request{})
			if r.Thinking == nil || r.Thinking.Type != "enabled" || r.OutputConfig == nil || r.OutputConfig.Effort != tc.want {
				t.Fatalf("DeepSeek thinking = %+v / %+v, want enabled/%s", r.Thinking, r.OutputConfig, tc.want)
			}
		})
	}
	t.Run("provider default", func(t *testing.T) {
		r := (&client{model: "deepseek-v4-flash", deepseek: true}).buildRequest(context.Background(), provider.Request{})
		if r.Thinking == nil || r.Thinking.Type != "enabled" || r.OutputConfig != nil {
			t.Fatalf("default DeepSeek thinking = %+v / %+v, want enabled/provider-default effort", r.Thinking, r.OutputConfig)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		c := &client{model: "deepseek-v4-flash", deepseek: true, thinking: "enabled", effort: "disabled"}
		r := c.buildRequest(context.Background(), provider.Request{
			Messages: []provider.Message{{Role: provider.RoleAssistant, ReasoningContent: "replay anyway"}},
			Tools:    []provider.ToolSchema{{Name: "tool"}},
		})
		if r.Thinking == nil || r.Thinking.Type != "disabled" || r.OutputConfig != nil {
			t.Fatalf("disabled DeepSeek thinking = %+v / %+v", r.Thinking, r.OutputConfig)
		}
		if provider.RequiresToolCallReasoning(c) || provider.RequiresReasoningRoundTrip(c) {
			t.Fatal("disabled DeepSeek thinking must not retain reasoning for replay")
		}
		// Historical thinking blocks are replayed even when the current request
		// disables thinking, the same rule tool-call turns already follow.
		if len(r.Messages) != 1 || len(r.Messages[0].Content) != 1 || r.Messages[0].Content[0].Type != "thinking" ||
			r.Messages[0].Content[0].Thinking != "replay anyway" {
			t.Fatalf("reasoning-only assistant under disabled thinking = %+v, want the thinking block replayed", r.Messages)
		}
	})
}

func TestBuildRequestDeepSeekPreservesCallerTemperature(t *testing.T) {
	zero := provider.TemperaturePtr(0)
	r := (&client{model: "deepseek-v4-flash", deepseek: true}).buildRequest(context.Background(), provider.Request{Temperature: zero})
	if r.Temperature == nil || *r.Temperature != 0 {
		t.Fatalf("DeepSeek temperature = %v, want explicit zero", r.Temperature)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"temperature":0`) {
		t.Fatalf("DeepSeek request omitted explicit temperature: %s", b)
	}

	native := (&client{model: "claude-opus-4-8"}).buildRequest(context.Background(), provider.Request{Temperature: provider.TemperaturePtr(0.5)})
	if native.Temperature != nil {
		t.Fatalf("native Anthropic temperature = %v, want omitted", native.Temperature)
	}
	b, err = json.Marshal(native)
	if err != nil {
		t.Fatalf("marshal native: %v", err)
	}
	if strings.Contains(string(b), `"temperature"`) {
		t.Fatalf("native Anthropic request must omit temperature: %s", b)
	}
}

// TestBuildRequestThinkingOff is the default: no thinking field, and reasoning is
// NOT replayed (even with a signature present) since the model wasn't asked to think.
func TestBuildRequestThinkingOff(t *testing.T) {
	c := &client{model: "claude-opus-4-8"}
	r := c.buildRequest(context.Background(), provider.Request{Messages: []provider.Message{
		{Role: provider.RoleUser, Content: "hi"},
		{Role: provider.RoleAssistant, Content: "ok", ReasoningContent: "x", ReasoningSignature: "sig"},
	}})
	if r.Thinking != nil || r.OutputConfig != nil {
		t.Fatalf("thinking should be off by default: %+v / %+v", r.Thinking, r.OutputConfig)
	}
	for _, b := range r.Messages[1].Content {
		if b.Type == "thinking" {
			t.Fatal("thinking block must not be replayed when thinking is off")
		}
	}
}

func TestBuildRequestDropsLocalMetadata(t *testing.T) {
	c := &client{model: "claude-opus-4-8"}
	r := c.buildRequest(context.Background(), provider.Request{Messages: []provider.Message{
		{Role: provider.RoleUser, Content: "continue"},
		{Role: provider.RoleUser, Content: "edited prompt", Edited: true, Original: "original prompt"},
		{Role: provider.RoleAssistant, Content: "done", WorkDurationMs: 24_000, MemoryCitations: []provider.MemoryCitation{{
			ID: "mem-1", Source: "MEMORY.md", LineStart: 116, LineEnd: 123, Note: "workflow",
		}}},
	}})
	b, err := json.Marshal(r.Messages)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "memoryCitations") || strings.Contains(string(b), "MEMORY.md") {
		t.Fatalf("local memory citations leaked into Anthropic request: %s", b)
	}
	if strings.Contains(string(b), "workDurationMs") || strings.Contains(string(b), "work_duration_ms") {
		t.Fatalf("local work duration leaked into Anthropic request: %s", b)
	}
	if strings.Contains(string(b), "original prompt") || strings.Contains(string(b), `"edited"`) || strings.Contains(string(b), `"original"`) {
		t.Fatalf("local edit metadata leaked into Anthropic request: %s", b)
	}
	if !strings.Contains(string(b), "done") {
		t.Fatalf("assistant content was dropped with local metadata: %s", b)
	}
}

const sseThinking = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIG123"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hi"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}

event: message_stop
data: {"type":"message_stop"}
`

// TestReadStreamThinking checks thinking_delta streams as reasoning text and
// signature_delta carries the signature back on a ChunkReasoning.
func TestReadStreamThinking(t *testing.T) {
	c := &client{name: "anthropic"}
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(sseThinking))}
	ch := make(chan provider.Chunk)
	go c.readStream(context.Background(), resp, ch)

	var reasoning, text strings.Builder
	var sig string
	for ck := range ch {
		switch ck.Type {
		case provider.ChunkReasoning:
			reasoning.WriteString(ck.Text)
			if ck.Signature != "" {
				sig = ck.Signature
			}
		case provider.ChunkText:
			text.WriteString(ck.Text)
		}
	}
	if reasoning.String() != "Let me think." {
		t.Fatalf("reasoning = %q", reasoning.String())
	}
	if sig != "SIG123" {
		t.Fatalf("signature = %q", sig)
	}
	if text.String() != "Hi" {
		t.Fatalf("text = %q", text.String())
	}
}

// TestBaseURLNormalizedForV1Messages checks the URL-rewriting step in New().
// Anthropic's Messages endpoint is {root}/v1/messages, but the setup wizard
// accepts OpenAI-style URLs (e.g. "https://proxy.example.com/v1") because
// /models probes expect that shape. Without the strip, the chat client would
// concatenate /v1/messages onto an already-versioned root and the request
// would go to https://proxy.example.com/v1/v1/messages — failing 404.
func TestBaseURLNormalizedForV1Messages(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain root (no /v1)", "https://api.anthropic.com", "https://api.anthropic.com"},
		{"versioned v1 (OpenAI shape)", "https://proxy.example.com/v1", "https://proxy.example.com"},
		{"versioned v1 with trailing slash", "https://proxy.example.com/v1/", "https://proxy.example.com"},
		{"versioned v1 with path prefix", "https://gateway.example.com/api/v1", "https://gateway.example.com/api"},
		{"trailing slash only", "https://api.anthropic.com/", "https://api.anthropic.com"},
		{"empty falls back to default", "", "https://api.anthropic.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := New(provider.Config{
				Name:    "test",
				Model:   "claude-opus-4-8",
				BaseURL: tc.in,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			c, ok := p.(*client)
			if !ok {
				t.Fatalf("provider type = %T, want *client", p)
			}
			if c.baseURL != tc.want {
				t.Errorf("baseURL = %q, want %q", c.baseURL, tc.want)
			}
		})
	}
}

func TestStreamSupportsBearerAuthHeaderAndCustomHeaders(t *testing.T) {
	var gotAuth, gotAPIKey, gotVersion, gotUserAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %q, want /v1/messages", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":2,"output_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`)
	}))
	defer srv.Close()

	p, err := New(provider.Config{
		Name:    "gateway",
		BaseURL: srv.URL,
		Model:   "claude-sonnet-4-6",
		APIKey:  "sk-test",
		Extra: map[string]any{
			"auth_header": true,
			"headers": map[string]string{
				"User-Agent":        "Reasonix",
				"Authorization":     "Bearer wrong",
				"x-api-key":         "wrong",
				"anthropic-version": "bad",
			},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch, err := p.Stream(context.Background(), provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var usage *provider.Usage
	for chunk := range ch {
		if chunk.Type == provider.ChunkUsage {
			usage = chunk.Usage
		}
	}

	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q, want Bearer sk-test", gotAuth)
	}
	if gotAPIKey != "" {
		t.Fatalf("x-api-key = %q, want omitted", gotAPIKey)
	}
	if gotVersion != anthropicVersion {
		t.Fatalf("anthropic-version = %q, want %q", gotVersion, anthropicVersion)
	}
	if gotUserAgent != "Reasonix" {
		t.Fatalf("User-Agent = %q, want Reasonix", gotUserAgent)
	}
	if usage == nil || usage.RequestCount != 1 {
		t.Fatalf("usage request count = %+v, want 1", usage)
	}
}

// TestStreamSendsMetadataUserID guards the cachecontext wiring: the workspace
// user attribution id must reach DeepSeek as metadata.user_id on the Anthropic
// wire and stay absent when unset.
func TestStreamSendsMetadataUserID(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":2,"output_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`)
	}))
	defer srv.Close()

	p, err := New(provider.Config{
		Name:    "deepseek",
		BaseURL: srv.URL,
		Model:   "deepseek-v4-flash",
		APIKey:  "sk-test",
		Extra:   map[string]any{"user_id": "proj-a"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch, err := p.Stream(context.Background(), provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for chunk := range ch {
		if chunk.Type == provider.ChunkError {
			t.Fatalf("stream error: %v", chunk.Err)
		}
	}

	meta, ok := body["metadata"].(map[string]any)
	if !ok || meta["user_id"] != "proj-a" {
		t.Fatalf("metadata = %#v, want user_id %q", body["metadata"], "proj-a")
	}
}

// TestStreamSendsSessionID guards the session_id wiring on the Anthropic wire:
// the derived workspace session id must reach the body as the top-level
// `session_id` (OpenRouter's convention) and stay absent when unset. Like
// user_id it is emitted for every upstream; gateways that do not define the
// field ignore it.
func TestStreamSendsSessionID(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":2,"output_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`)
	}))
	defer srv.Close()

	p, err := New(provider.Config{
		Name:    "gateway",
		BaseURL: srv.URL,
		Model:   "claude-sonnet-4",
		APIKey:  "sk-test",
		Extra:   map[string]any{"session_id": "alice--home-alice-proj"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch, err := p.Stream(context.Background(), provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for chunk := range ch {
		if chunk.Type == provider.ChunkError {
			t.Fatalf("stream error: %v", chunk.Err)
		}
	}
	if body["session_id"] != "alice--home-alice-proj" {
		t.Fatalf("session_id = %#v, want %q", body["session_id"], "alice--home-alice-proj")
	}
}

// TestStreamOmitsMetadataUserIDWhenUnset ensures a bare provider does not send
// a spurious empty metadata block.
func TestStreamOmitsMetadataUserIDWhenUnset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if _, ok := body["metadata"]; ok {
			t.Errorf("metadata present when user_id unset: %#v", body["metadata"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":2,"output_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`)
	}))
	defer srv.Close()

	p, err := New(provider.Config{
		Name:    "deepseek",
		BaseURL: srv.URL,
		Model:   "deepseek-v4-flash",
		APIKey:  "sk-test",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch, err := p.Stream(context.Background(), provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for chunk := range ch {
		if chunk.Type == provider.ChunkError {
			t.Fatalf("stream error: %v", chunk.Err)
		}
	}
}

// Ensure the package wires into the registry under the expected kind.
