package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

// TestStreamRetriesThenSucceeds drives the real retry path end-to-end: the
// server returns 503 twice, then a valid SSE stream. The provider must back off,
// fire the retry-notify callback for each attempt, and ultimately stream the answer.
func TestStreamRetriesThenSucceeds(t *testing.T) {
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		if reqs <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"overloaded"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi there\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := New(provider.Config{Name: "deepseek", BaseURL: srv.URL, Model: "deepseek-v4", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var attempts []int
	ctx := provider.WithRetryNotify(context.Background(), func(i provider.RetryInfo) {
		attempts = append(attempts, i.Attempt)
		if i.Max != provider.MaxRetries {
			t.Errorf("RetryInfo.Max = %d, want %d", i.Max, provider.MaxRetries)
		}
	})

	ch, err := p.Stream(ctx, provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream after retries: %v", err)
	}
	var got strings.Builder
	var usage *provider.Usage
	for chunk := range ch {
		if chunk.Type == provider.ChunkError {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		if chunk.Type == provider.ChunkText {
			got.WriteString(chunk.Text)
		}
		if chunk.Type == provider.ChunkUsage {
			usage = chunk.Usage
		}
	}
	if got.String() != "hi there" {
		t.Errorf("streamed text = %q, want %q", got.String(), "hi there")
	}
	if reqs != 3 {
		t.Errorf("server saw %d requests, want 3 (2 failures + 1 success)", reqs)
	}
	if len(attempts) != 2 || attempts[0] != 1 || attempts[1] != 2 {
		t.Errorf("retry-notify attempts = %v, want [1 2]", attempts)
	}
	if usage == nil || usage.RequestCount != 3 {
		t.Errorf("usage request count = %+v, want 3", usage)
	}
}

func TestMergeUsageCountsStreamsNotUsageChunks(t *testing.T) {
	firstChunk := &provider.Usage{PromptTokens: 2, TotalTokens: 2, RequestCount: 2, CacheWriteTokens: 2, CacheWriteBilledTokens: 2.5}
	secondChunk := &provider.Usage{CompletionTokens: 1, TotalTokens: 1, RequestCount: 2, CacheWriteTokens: 3, CacheWriteBilledTokens: 6}
	oneStream := mergeUsage(firstChunk, secondChunk, false)
	if oneStream.RequestCount != 2 {
		t.Fatalf("same-stream request count = %d, want 2", oneStream.RequestCount)
	}
	if oneStream.CacheWriteTokens != 5 || oneStream.CacheWriteBilledTokens != 8.5 {
		t.Fatalf("same-stream cache writes = raw %d billed %v, want 5/8.5", oneStream.CacheWriteTokens, oneStream.CacheWriteBilledTokens)
	}
	nextStream := &provider.Usage{PromptTokens: 3, TotalTokens: 3, RequestCount: 1}
	combined := mergeUsage(oneStream, nextStream, true)
	if combined.RequestCount != 3 {
		t.Fatalf("multi-stream request count = %d, want 3", combined.RequestCount)
	}
}

// TestStreamInsufficientBalance verifies a 402 fails fast (no retry) as a typed
// *provider.APIError carrying the status, so the display layer can explain it.
func TestStreamInsufficientBalance(t *testing.T) {
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":"Insufficient Balance"}`))
	}))
	defer srv.Close()

	p, _ := New(provider.Config{Name: "deepseek", BaseURL: srv.URL, Model: "deepseek-v4", APIKey: "k"})
	_, err := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}})
	var apiErr *provider.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 402 {
		t.Fatalf("want *provider.APIError{Status:402}, got %T: %v", err, err)
	}
	if reqs != 1 {
		t.Errorf("402 should not retry, server saw %d requests", reqs)
	}
}

func TestStreamAnnotatesIndexedToolSchemaError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Tool 1 function has invalid 'parameters' schema"}}`))
	}))
	defer srv.Close()

	p, err := New(provider.Config{Name: "mimo", BaseURL: srv.URL, Model: "mimo-v2.5-pro", APIKey: "k"})
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

	mimo := (&client{mimo: true}).buildRequest(req)
	if got := string(mimo.Tools[0].Function.Parameters); !strings.Contains(got, `"prefixItems"`) || strings.Contains(got, `"items":[`) {
		t.Fatalf("MiMo parameters = %s, want Draft 2020-12 tuple keywords", got)
	}

	other := (&client{}).buildRequest(req)
	if got := string(other.Tools[0].Function.Parameters); got != string(legacy) {
		t.Fatalf("non-MiMo parameters changed:\n got: %s\nwant: %s", got, legacy)
	}
}

func TestBuildRequestOrdinaryDeepSeekBytesStayPrefixFree(t *testing.T) {
	c := &client{model: "deepseek-v4-flash", deepseek: true, effort: "high"}
	body, err := json.Marshal(c.buildRequest(provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}}))
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	want := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true},"reasoning_effort":"high","thinking":{"type":"enabled"}}`
	if string(body) != want {
		t.Fatalf("ordinary DeepSeek request bytes changed:\n got: %s\nwant: %s", body, want)
	}
	if strings.Contains(string(body), `"prefix"`) {
		t.Fatalf("ordinary request leaked prefix mode: %s", body)
	}
}

func TestBuildPrefixRequestAppendsWireOnlyAssistantTail(t *testing.T) {
	c := &client{model: "deepseek-v4-pro", deepseek: true, effort: "high"}
	req := provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "write a long answer"}}}
	body, err := json.Marshal(c.buildPrefixRequest(req, "partial answer", "provider reasoning"))
	if err != nil {
		t.Fatalf("marshal prefix request: %v", err)
	}
	var decoded struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode prefix request: %v", err)
	}
	if len(decoded.Messages) != 2 {
		t.Fatalf("messages = %d, want original user plus wire-only assistant prefix", len(decoded.Messages))
	}
	last := decoded.Messages[1]
	if string(last["role"]) != `"assistant"` || string(last["content"]) != `"partial answer"` || string(last["prefix"]) != `true` {
		t.Fatalf("prefix tail = %v, want assistant content with prefix=true", last)
	}
	if string(last["reasoning_content"]) != `"provider reasoning"` {
		t.Fatalf("thinking prefix lost reasoning_content: %s", last)
	}
	if len(req.Messages) != 1 {
		t.Fatal("buildPrefixRequest mutated the caller's persisted message slice")
	}

	disabled := &client{model: c.model, deepseek: true, effort: c.effort, thinkingType: "disabled"}
	disabledBody, err := json.Marshal(disabled.buildPrefixRequest(req, "partial answer", "must stay local"))
	if err != nil {
		t.Fatalf("marshal disabled prefix request: %v", err)
	}
	if strings.Contains(string(disabledBody), "reasoning_content") || strings.Contains(string(disabledBody), "must stay local") {
		t.Fatalf("non-thinking prefix must omit reasoning_content: %s", disabledBody)
	}
}

func TestNewScopesPrefixContinuationToOfficialDeepSeekChatURL(t *testing.T) {
	official, err := New(provider.Config{Name: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-flash", APIKey: "k"})
	if err != nil {
		t.Fatalf("New official DeepSeek: %v", err)
	}
	if got := official.(*client).prefixChatURL; got != "https://api.deepseek.com/beta/chat/completions" {
		t.Fatalf("official prefix URL = %q", got)
	}

	gateway, err := New(provider.Config{
		Name: "gateway", BaseURL: "https://gateway.example/v1", Model: "deepseek-v4-flash", APIKey: "k",
		Extra: map[string]any{"reasoning_protocol": "deepseek"},
	})
	if err != nil {
		t.Fatalf("New custom gateway: %v", err)
	}
	if got := gateway.(*client).prefixChatURL; got != "" {
		t.Fatalf("custom gateway must not bypass itself for Beta continuation, got %q", got)
	}
}

func TestStreamContinuesDeepSeekLengthWithAssistantPrefix(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		switch r.URL.Path {
		case "/chat/completions":
			if strings.Contains(string(body), `"prefix":true`) {
				t.Errorf("initial request unexpectedly enabled prefix mode: %s", body)
			}
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think one. \"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"length\"}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":3,\"total_tokens\":13,\"prompt_cache_hit_tokens\":8,\"prompt_cache_miss_tokens\":2,\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		case "/beta/chat/completions":
			var decoded struct {
				Messages []map[string]json.RawMessage `json:"messages"`
			}
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Errorf("decode continuation request: %v", err)
				http.Error(w, "invalid continuation request", http.StatusBadRequest)
				return
			}
			last := decoded.Messages[len(decoded.Messages)-1]
			if string(last["role"]) != `"assistant"` || string(last["content"]) != `"partial"` || string(last["prefix"]) != `true` {
				t.Errorf("continuation tail = %s", last)
			}
			if string(last["reasoning_content"]) != `"think one. "` {
				t.Errorf("continuation reasoning_content = %s", last["reasoning_content"])
			}
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think two. \"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\" rest\"},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":2,\"total_tokens\":22,\"prompt_cache_hit_tokens\":18,\"prompt_cache_miss_tokens\":2,\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &client{
		name: "deepseek", apiKey: "k", baseURL: srv.URL, chatURL: srv.URL + "/chat/completions",
		prefixChatURL: srv.URL + "/beta/chat/completions", model: "deepseek-v4-flash", deepseek: true,
		effort: "high", http: srv.Client(), idleTimeout: defaultStreamIdleTimeout,
	}
	ch, err := c.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "write"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var text, reasoning strings.Builder
	var usage *provider.Usage
	usageChunks, doneChunks := 0, 0
	for chunk := range ch {
		switch chunk.Type {
		case provider.ChunkText:
			text.WriteString(chunk.Text)
		case provider.ChunkReasoning:
			reasoning.WriteString(chunk.Text)
		case provider.ChunkUsage:
			usageChunks++
			usage = chunk.Usage
		case provider.ChunkDone:
			doneChunks++
		case provider.ChunkError:
			t.Fatalf("automatic continuation errored: %v", chunk.Err)
		}
	}
	if requests != 2 || text.String() != "partial rest" || reasoning.String() != "think one. think two. " {
		t.Fatalf("requests=%d text=%q reasoning=%q", requests, text.String(), reasoning.String())
	}
	if usageChunks != 1 || doneChunks != 1 || usage == nil {
		t.Fatalf("usage chunks=%d done chunks=%d usage=%+v", usageChunks, doneChunks, usage)
	}
	if usage.PromptTokens != 30 || usage.CompletionTokens != 5 || usage.TotalTokens != 35 || usage.RequestCount != 2 ||
		usage.CacheHitTokens != 26 || usage.CacheMissTokens != 4 || usage.ReasoningTokens != 2 || usage.FinishReason != "stop" {
		t.Fatalf("merged usage = %+v", usage)
	}
}

func TestStreamContinuesReasoningOnlyDeepSeekLength(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		switch r.URL.Path {
		case "/chat/completions":
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think one. \"},\"finish_reason\":\"length\"}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":3,\"total_tokens\":13,\"completion_tokens_details\":{\"reasoning_tokens\":3}}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		case "/beta/chat/completions":
			var decoded struct {
				Messages []map[string]json.RawMessage `json:"messages"`
			}
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Errorf("decode continuation request: %v", err)
				http.Error(w, "invalid continuation request", http.StatusBadRequest)
				return
			}
			last := decoded.Messages[len(decoded.Messages)-1]
			if string(last["role"]) != `"assistant"` || string(last["content"]) != `""` || string(last["prefix"]) != `true` {
				t.Errorf("reasoning-only continuation tail = %s", last)
			}
			if string(last["reasoning_content"]) != `"think one. "` {
				t.Errorf("continuation reasoning_content = %s", last["reasoning_content"])
			}
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think two. \"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":4,\"total_tokens\":24,\"completion_tokens_details\":{\"reasoning_tokens\":2}}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &client{
		name: "deepseek", apiKey: "k", baseURL: srv.URL, chatURL: srv.URL + "/chat/completions",
		prefixChatURL: srv.URL + "/beta/chat/completions", model: "deepseek-v4-flash", deepseek: true,
		effort: "high", http: srv.Client(), idleTimeout: defaultStreamIdleTimeout,
	}
	ch, err := c.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "write"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var text, reasoning strings.Builder
	var usage *provider.Usage
	for chunk := range ch {
		switch chunk.Type {
		case provider.ChunkText:
			text.WriteString(chunk.Text)
		case provider.ChunkReasoning:
			reasoning.WriteString(chunk.Text)
		case provider.ChunkUsage:
			usage = chunk.Usage
		case provider.ChunkError:
			t.Fatalf("automatic reasoning-only continuation errored: %v", chunk.Err)
		}
	}
	if requests != 2 || text.String() != "answer" || reasoning.String() != "think one. think two. " {
		t.Fatalf("requests=%d text=%q reasoning=%q", requests, text.String(), reasoning.String())
	}
	if usage == nil || usage.PromptTokens != 30 || usage.CompletionTokens != 7 || usage.TotalTokens != 37 ||
		usage.ReasoningTokens != 5 || usage.FinishReason != "stop" {
		t.Fatalf("merged usage = %+v", usage)
	}
}

func TestStreamKeepsTruncatedAnswerWhenDeepSeekBetaFails(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/beta/chat/completions" {
			http.Error(w, `{"error":{"message":"beta unavailable"}}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"keep me\"},\"finish_reason\":\"length\"}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := &client{
		name: "deepseek", apiKey: "k", baseURL: srv.URL, chatURL: srv.URL + "/chat/completions",
		prefixChatURL: srv.URL + "/beta/chat/completions", model: "deepseek-v4-flash", deepseek: true,
		effort: "high", http: srv.Client(), idleTimeout: defaultStreamIdleTimeout,
	}
	ch, err := c.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "write"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var text strings.Builder
	var usage *provider.Usage
	for chunk := range ch {
		switch chunk.Type {
		case provider.ChunkText:
			text.WriteString(chunk.Text)
		case provider.ChunkUsage:
			usage = chunk.Usage
		case provider.ChunkError:
			t.Fatalf("Beta failure must fall back to the original answer, got %v", chunk.Err)
		}
	}
	if requests != 2 || text.String() != "keep me" || usage == nil || usage.FinishReason != "length" {
		t.Fatalf("requests=%d text=%q usage=%+v", requests, text.String(), usage)
	}
}

func TestStreamBoundsRepeatedDeepSeekLengthContinuation(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"piece\"},\"finish_reason\":\"length\"}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := &client{
		name: "deepseek", apiKey: "k", baseURL: srv.URL, chatURL: srv.URL + "/chat/completions",
		prefixChatURL: srv.URL + "/beta/chat/completions", model: "deepseek-v4-flash", deepseek: true,
		effort: "high", http: srv.Client(), idleTimeout: defaultStreamIdleTimeout,
	}
	ch, err := c.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "write"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var text strings.Builder
	var usage *provider.Usage
	for chunk := range ch {
		if chunk.Type == provider.ChunkText {
			text.WriteString(chunk.Text)
		}
		if chunk.Type == provider.ChunkUsage {
			usage = chunk.Usage
		}
		if chunk.Type == provider.ChunkError {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
	}
	if requests != 2 || text.String() != "piecepiece" || usage == nil || usage.FinishReason != "length" {
		t.Fatalf("requests=%d text=%q usage=%+v", requests, text.String(), usage)
	}
}

func TestStreamDoesNotPrefixContinueToolCalls(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{}"}}]}}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := &client{
		name: "deepseek", apiKey: "k", baseURL: srv.URL, chatURL: srv.URL + "/chat/completions",
		prefixChatURL: srv.URL + "/beta/chat/completions", model: "deepseek-v4-flash", deepseek: true,
		effort: "high", http: srv.Client(), idleTimeout: defaultStreamIdleTimeout,
	}
	ch, err := c.Stream(context.Background(), provider.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	toolCalls := 0
	var usage *provider.Usage
	for chunk := range ch {
		if chunk.Type == provider.ChunkToolCall {
			toolCalls++
		}
		if chunk.Type == provider.ChunkUsage {
			usage = chunk.Usage
		}
		if chunk.Type == provider.ChunkError {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
	}
	if requests != 1 || toolCalls != 1 || usage == nil || usage.FinishReason != "length" {
		t.Fatalf("requests=%d toolCalls=%d usage=%+v", requests, toolCalls, usage)
	}
}

// TestStreamAuthError verifies a 401 surfaces as an actionable *provider.AuthError
// (naming the provider and its key env var) rather than a raw status body.
func TestStreamAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Authentication Fails, Your api key: ****ae54 is invalid"}}`))
	}))
	defer srv.Close()

	p, err := New(provider.Config{
		Name:    "deepseek",
		BaseURL: srv.URL,
		Model:   "deepseek-v4",
		APIKey:  "bad",
		Extra:   map[string]any{"api_key_env": "DEEPSEEK_API_KEY"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = p.Stream(context.Background(), provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	var authErr *provider.AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("want *provider.AuthError, got %T: %v", err, err)
	}
	if authErr.Provider != "deepseek" || authErr.KeyEnv != "DEEPSEEK_API_KEY" || authErr.Status != 401 {
		t.Errorf("AuthError fields wrong: %+v", authErr)
	}
	if msg := authErr.Error(); !strings.Contains(msg, "DEEPSEEK_API_KEY") || strings.Contains(msg, "ae54") {
		t.Errorf("message should name the env var and not dump the raw body: %q", msg)
	}
}

func TestStreamUsesConfiguredChatURL(t *testing.T) {
	var sawRequest bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRequest = true
		if r.URL.RequestURI() != "/proxy/v1/chat/completions" {
			t.Errorf("request URI = %s, want /proxy/v1/chat/completions", r.URL.RequestURI())
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := New(provider.Config{
		Name:    "custom",
		BaseURL: srv.URL + "/base",
		Model:   "model-a",
		APIKey:  "k",
		Extra:   map[string]any{"chat_url": srv.URL + "/proxy/v1/chat/completions/"},
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
	var got strings.Builder
	for chunk := range ch {
		if chunk.Type == provider.ChunkError {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		if chunk.Type == provider.ChunkText {
			got.WriteString(chunk.Text)
		}
	}
	if !sawRequest {
		t.Fatal("server did not receive request")
	}
	if got.String() != "ok" {
		t.Fatalf("streamed text = %q, want ok", got.String())
	}
}

func TestStreamSendsCustomHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer real-key" {
			http.Error(w, "authorization was not preserved", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("HTTP-Referer") != "https://app.example" || r.Header.Get("X-Title") != "Reasonix" {
			http.Error(w, "custom headers missing", http.StatusForbidden)
			return
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			http.Error(w, "reserved Accept header was overwritten", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := New(provider.Config{
		Name:    "custom",
		BaseURL: srv.URL,
		Model:   "model-a",
		APIKey:  "real-key",
		Extra: map[string]any{"headers": map[string]string{
			"Authorization": "Bearer wrong",
			"Accept":        "application/json",
			"HTTP-Referer":  "https://app.example",
			"X-Title":       "Reasonix",
		}},
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

func TestStreamUsesMiMoAPIKeyHeader(t *testing.T) {
	var gotAuth, gotAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("api-key")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := New(provider.Config{
		Name:    "mimo",
		BaseURL: "https://api.xiaomimimo.com/v1",
		Model:   "mimo-v2.5-pro",
		APIKey:  "mimo-key",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c := p.(*client)
	if !c.mimo {
		t.Fatal("official MiMo endpoint did not enable the Draft 2020-12 schema adapter")
	}
	c.chatURL = srv.URL

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
	if gotAPIKey != "mimo-key" {
		t.Fatalf("api-key = %q, want mimo-key", gotAPIKey)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization = %q, want omitted for MiMo", gotAuth)
	}
}

func TestStreamSendsExtraBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if req["enable_thinking"] != true {
			http.Error(w, "extra enable_thinking missing", http.StatusBadRequest)
			return
		}
		if got, ok := req["top_p"].(float64); !ok || got != 0.7 {
			http.Error(w, "extra top_p missing", http.StatusBadRequest)
			return
		}
		if req["model"] != "model-a" || req["stream"] != true {
			http.Error(w, "reserved fields were overwritten", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := New(provider.Config{
		Name:    "custom",
		BaseURL: srv.URL,
		Model:   "model-a",
		APIKey:  "real-key",
		Extra: map[string]any{"extra_body": map[string]any{
			"enable_thinking": true,
			"top_p":           0.7,
			"model":           "wrong",
			"stream":          false,
		}},
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

// TestStreamSendsUserID guards the cachecontext wiring: the workspace user
// attribution id must reach the OpenAI-compatible body as `user` and stay
// absent when unset.
func TestStreamSendsUserID(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := New(provider.Config{
		Name:    "deepseek",
		BaseURL: srv.URL,
		Model:   "deepseek-chat",
		APIKey:  "real-key",
		Extra:   map[string]any{"user_id": "proj-b"},
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
	if body["user"] != "proj-b" {
		t.Fatalf("user = %#v, want %q", body["user"], "proj-b")
	}
}

// TestStreamSendsSessionID guards the session_id wiring: the derived workspace
// session id must reach the OpenAI-compatible body as the top-level
// `session_id` and stay absent when unset. Like user_id it is emitted for every
// upstream; OpenRouter defined the field, other gateways ignore it.
func TestStreamSendsSessionID(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := New(provider.Config{
		Name:    "gateway",
		BaseURL: srv.URL,
		Model:   "m",
		APIKey:  "real-key",
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

// TestBuildRequestAlwaysSerializesContent guards the DeepSeek 400 regression:
// DeepSeek rejects a message missing the `content` field, so every message must
// serialize one. A pure tool_calls assistant turn carries null (OpenAI-spec,
// and accepted by DeepSeek — verified against a live multi-tool session); other
// roles serialize a string. The field must never be absent.
func TestBuildRequestAlwaysSerializesContent(t *testing.T) {
	c := &client{model: "deepseek-v4"}
	req := c.buildRequest(provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "list the files"},
			// Assistant turn with no text, only a tool call — the offending shape.
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
				{ID: "call_1", Name: "ls", Arguments: `{"path":"."}`},
			}},
			{Role: provider.RoleTool, Content: "main.go", ToolCallID: "call_1", Name: "ls"},
		},
	})

	b, err := json.Marshal(req.Messages)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Decode generically so we can assert the key's presence (not just its value).
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for i, m := range raw {
		if _, ok := m["content"]; !ok {
			t.Errorf("messages[%d] is missing the content field: %s", i, b)
		}
	}
	// The tool-call-only assistant message must carry content:null and its tool_calls.
	if got := string(raw[1]["content"]); got != `null` {
		t.Errorf("assistant content = %s, want null", got)
	}
	if _, ok := raw[1]["tool_calls"]; !ok {
		t.Errorf("assistant message lost its tool_calls: %s", b)
	}
}

func TestBuildRequestOmitsResolvedToolCallMetadata(t *testing.T) {
	readOnly := false
	c := &client{model: "deepseek-v4"}
	req := c.buildRequest(provider.Request{Messages: []provider.Message{{
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

// TestToolResultEmptyNameStillSerialized guards MiMo #4711: a strict
// OpenAI-compatible backend rejects a role=tool message whose `name` key is
// absent ("Param Incorrect, name is not set"). A legacy empty-name tool result
// must still carry the key (as an empty string) rather than vanish via
// omitempty.
func TestToolResultEmptyNameStillSerialized(t *testing.T) {
	c := &client{model: "deepseek-v4"}
	req := c.buildRequest(provider.Request{Messages: []provider.Message{
		// Both the tool_call and its result have an empty name: the legacy
		// #4727 shape where backfill has no source to recover from. The wire
		// must still carry the name key so strict backends don't 400.
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "", Arguments: `{}`}}},
		{Role: provider.RoleTool, ToolCallID: "call_1", Name: "", Content: "file contents"},
	}})
	b, err := json.Marshal(req.Messages)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The tool result message must carry the name key even though the name is
	// empty — strict backends 400 a missing key.
	var msgs []map[string]any
	if err := json.Unmarshal(b, &msgs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	roles := []string{msgs[0]["role"].(string), msgs[1]["role"].(string)}
	if roles[1] != "tool" {
		t.Fatalf("second message role = %q, want tool", roles[1])
	}
	// Tool message: name key must be present (empty string serialized).
	if _, ok := msgs[1]["name"]; !ok {
		t.Fatalf("tool message lost its name key (must serialize empty): %s", b)
	}
	if name, _ := msgs[1]["name"].(string); name != "" {
		t.Fatalf("tool message name = %q, want empty (legacy empty-name result)", name)
	}
	// Non-tool messages: name key must stay absent (byte-stable prefix).
	for i, m := range msgs {
		if roles[i] == "tool" {
			continue
		}
		if _, ok := m["name"]; ok {
			t.Fatalf("non-tool message %d leaked name key: %s", i, b)
		}
	}
}

// TestStreamRepairsDanglingToolCalls reproduces and guards the DeepSeek 400
// "An assistant message with 'tool_calls' must be followed by tool messages
// responding to each 'tool_call_id'". A resumed/interrupted session can carry an
// assistant tool_calls turn whose tool results never landed; the server here
// mimics DeepSeek and rejects any unpaired tool_call with that exact 400, so the
// request must be repaired before it is sent.
func TestStreamRepairsDanglingToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role      string `json:"role"`
				ToolCalls []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
				ToolCallID string `json:"tool_call_id"`
			} `json:"messages"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		answered := map[string]bool{}
		for _, m := range req.Messages {
			if m.Role == "tool" {
				answered[m.ToolCallID] = true
			}
		}
		for _, m := range req.Messages {
			if m.Role != "assistant" {
				continue
			}
			for _, tc := range m.ToolCalls {
				if !answered[tc.ID] {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"message":"An assistant message with 'tool_calls' must be followed by tool messages responding to each 'tool_call_id'. (insufficient tool messages following tool_calls message)","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`))
					return
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1,\"total_tokens\":6}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := New(provider.Config{Name: "deepseek-flash", BaseURL: srv.URL, Model: "deepseek-v4", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// An assistant tool_calls turn whose tool result never landed (an interrupted
	// turn), followed by a fresh user message — the exact shape that 400s.
	ch, err := p.Stream(context.Background(), provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "list the files"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
				{ID: "call_1", Name: "ls", Arguments: `{"path":"."}`},
			}},
			{Role: provider.RoleUser, Content: "never mind, what time is it?"},
		},
	})
	if err != nil {
		t.Fatalf("Stream sent a dangling tool_calls to the API: %v", err)
	}
	var streamErr error
	var text strings.Builder
	for chunk := range ch {
		switch chunk.Type {
		case provider.ChunkText:
			text.WriteString(chunk.Text)
		case provider.ChunkError:
			streamErr = chunk.Err
		}
	}
	if streamErr != nil {
		t.Fatalf("stream errored: %v", streamErr)
	}
	if text.String() != "done" {
		t.Fatalf("completion text = %q, want \"done\"", text.String())
	}
}

// TestNormaliseUsageDeepSeekShape covers DeepSeek's top-level cache fields.
func TestNormaliseUsageDeepSeekShape(t *testing.T) {
	u := normaliseUsage(&wireUsage{
		PromptTokens:          1000,
		CompletionTokens:      200,
		TotalTokens:           1200,
		PromptCacheHitTokens:  900,
		PromptCacheMissTokens: 100,
	})
	if u.CacheHitTokens != 900 || u.CacheMissTokens != 100 {
		t.Errorf("DeepSeek-shape cache fields lost: hit=%d miss=%d", u.CacheHitTokens, u.CacheMissTokens)
	}
}

// TestNormaliseUsageMiMoShape covers the nested prompt_tokens_details /
// completion_tokens_details path used by OpenAI and MiMo. Miss is derived
// from prompt - hit when only hit is provided.
func TestNormaliseUsageMiMoShape(t *testing.T) {
	u := normaliseUsage(&wireUsage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		PromptTokensDetails: &struct {
			CachedTokens int `json:"cached_tokens"`
		}{CachedTokens: 600},
		CompletionTokensDetails: &struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		}{ReasoningTokens: 180},
	})
	if u.CacheHitTokens != 600 || u.CacheMissTokens != 400 {
		t.Errorf("nested cache normalisation wrong: hit=%d miss=%d (want 600 / 400)", u.CacheHitTokens, u.CacheMissTokens)
	}
	if u.ReasoningTokens != 180 {
		t.Errorf("reasoning tokens lost: %d", u.ReasoningTokens)
	}
}

// TestBuildRequestReplaysReasoningOnPlainAssistantTurn guards the DeepSeek
// replay contract: a reasoning-bearing assistant turn must keep its exact
// reasoning_content in later requests even when it made no tool call.
func TestBuildRequestReplaysReasoningOnPlainAssistantTurn(t *testing.T) {
	c := &client{model: "deepseek-reasoner", deepseek: true}
	req := c.buildRequest(provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "explain"},
			{Role: provider.RoleAssistant, Content: "the answer", ReasoningContent: "SECRET-CHAIN-OF-THOUGHT"},
			{Role: provider.RoleUser, Content: "thanks"},
		},
	})
	b, err := json.Marshal(req.Messages)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), "reasoning_content") {
		t.Errorf("a reasoning-bearing assistant turn must carry reasoning_content: %s", b)
	}
	if !strings.Contains(string(b), "SECRET-CHAIN-OF-THOUGHT") {
		t.Errorf("the assistant chain-of-thought was dropped from the request: %s", b)
	}
	if !strings.Contains(string(b), "the answer") {
		t.Errorf("assistant content was dropped along with reasoning: %s", b)
	}
}

func TestBuildRequestDropsLocalMetadata(t *testing.T) {
	c := &client{model: "deepseek-chat", deepseek: true}
	req := c.buildRequest(provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "continue"},
			{Role: provider.RoleUser, Content: "edited prompt", Edited: true, Original: "original prompt"},
			{Role: provider.RoleAssistant, Content: "done", WorkDurationMs: 24_000, MemoryCitations: []provider.MemoryCitation{{
				ID: "mem-1", Source: "MEMORY.md", LineStart: 116, LineEnd: 123, Note: "workflow",
			}}},
		},
	})
	b, err := json.Marshal(req.Messages)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "memoryCitations") || strings.Contains(string(b), "MEMORY.md") {
		t.Fatalf("local memory citations leaked into OpenAI-compatible request: %s", b)
	}
	if strings.Contains(string(b), "workDurationMs") || strings.Contains(string(b), "work_duration_ms") {
		t.Fatalf("local work duration leaked into OpenAI-compatible request: %s", b)
	}
	if strings.Contains(string(b), "original prompt") || strings.Contains(string(b), `"edited"`) || strings.Contains(string(b), `"original"`) {
		t.Fatalf("local edit metadata leaked into OpenAI-compatible request: %s", b)
	}
	if !strings.Contains(string(b), "done") {
		t.Fatalf("assistant content was dropped with local metadata: %s", b)
	}
}

// DeepSeek thinking mode 400s a tool_calls turn whose reasoning_content was
// dropped on a cache-miss replay, so it must be round-tripped — but only on the
// turn that carries tool calls, and only for the DeepSeek protocol.
func TestBuildRequestRoundTripsReasoningOnDeepSeekToolCalls(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "count the go files"},
		{
			Role:             provider.RoleAssistant,
			ReasoningContent: "CHAIN-OF-THOUGHT",
			ToolCalls:        []provider.ToolCall{{ID: "c1", Name: "bash", Arguments: `{"command":"ls"}`}},
		},
		{Role: provider.RoleTool, Content: "14", ToolCallID: "c1", Name: "bash"},
	}
	deepseek, _ := json.Marshal((&client{model: "deepseek-v4", deepseek: true}).buildRequest(provider.Request{Messages: msgs}).Messages)
	if !strings.Contains(string(deepseek), "reasoning_content") || !strings.Contains(string(deepseek), "CHAIN-OF-THOUGHT") {
		t.Errorf("DeepSeek tool_calls turn must round-trip reasoning_content: %s", deepseek)
	}

	other, _ := json.Marshal((&client{model: "mimo-v2"}).buildRequest(provider.Request{Messages: msgs}).Messages)
	if strings.Contains(string(other), "CHAIN-OF-THOUGHT") {
		t.Errorf("non-DeepSeek backends must not re-upload reasoning_content: %s", other)
	}
}

func TestBuildRequestForwardsReasoningEffort(t *testing.T) {
	c := &client{model: "mimo-v2", effort: "high"}
	if got := c.buildRequest(provider.Request{}).ReasoningEffort; got != "high" {
		t.Errorf("ReasoningEffort = %q, want high", got)
	}

	b, err := json.Marshal((&client{model: "deepseek-v4"}).buildRequest(provider.Request{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "reasoning_effort") {
		t.Errorf("empty effort must be omitted from the payload: %s", b)
	}
}

func TestNewDeepSeekV4FlashForwardsLowEffort(t *testing.T) {
	p, err := New(provider.Config{
		Name:    "deepseek",
		BaseURL: "https://api.deepseek.com",
		Model:   "deepseek-v4-flash",
		APIKey:  "test",
		Extra: map[string]any{
			"effort":             "low",
			"reasoning_protocol": "deepseek",
		},
	})
	if err != nil {
		t.Fatalf("New Flash low: %v", err)
	}
	if got := p.(*client).buildRequest(provider.Request{}).ReasoningEffort; got != "low" {
		t.Fatalf("Flash reasoning_effort = %q, want low", got)
	}

	pro, err := New(provider.Config{
		Name:    "deepseek",
		BaseURL: "https://api.deepseek.com",
		Model:   "deepseek-v4-pro",
		APIKey:  "test",
		Extra: map[string]any{
			"effort":             "low",
			"reasoning_protocol": "deepseek",
		},
	})
	if err != nil {
		t.Fatalf("New Pro low: %v", err)
	}
	if got := pro.(*client).buildRequest(provider.Request{}).ReasoningEffort; got != "low" {
		t.Fatalf("Pro reasoning_effort = %q, want low", got)
	}

	custom, err := New(provider.Config{
		Name:    "custom-deepseek",
		BaseURL: "https://gateway.example.com/v1",
		Model:   "custom-flash",
		APIKey:  "test",
		Extra: map[string]any{
			"effort":             "low",
			"reasoning_protocol": "deepseek",
			"supported_efforts":  []string{"low", "high", "max"},
		},
	})
	if err != nil {
		t.Fatalf("New explicit custom low: %v", err)
	}
	if got := custom.(*client).buildRequest(provider.Request{}).ReasoningEffort; got != "low" {
		t.Fatalf("custom reasoning_effort = %q, want explicit low", got)
	}
}

func TestDeepSeekV4EffortAliasesRejected(t *testing.T) {
	for _, model := range []string{"deepseek-v4-flash", "deepseek-v4-pro", OfficialDeepSeekVisionModel} {
		for _, alias := range []string{"medium", "xhigh"} {
			_, err := New(provider.Config{BaseURL: "https://api.deepseek.com", Model: model, Extra: map[string]any{"effort": alias}})
			if err == nil {
				t.Fatalf("%s accepted undeclared %s", model, alias)
			}
		}
	}
}

func TestBuildRequestTemperatureSerialization(t *testing.T) {
	c := &client{model: "m"}

	omitted := c.buildRequest(provider.Request{})
	if omitted.Temperature != nil {
		t.Fatalf("unset request temperature = %v, want nil", omitted.Temperature)
	}
	b, err := json.Marshal(omitted)
	if err != nil {
		t.Fatalf("marshal omitted: %v", err)
	}
	if strings.Contains(string(b), "temperature") {
		t.Fatalf("unset temperature must be omitted from payload: %s", b)
	}

	zero := c.buildRequest(provider.Request{Temperature: provider.TemperaturePtr(0)})
	if zero.Temperature == nil || *zero.Temperature != 0 {
		t.Fatalf("zero request temperature = %v, want ptr(0)", zero.Temperature)
	}
	b, err = json.Marshal(zero)
	if err != nil {
		t.Fatalf("marshal zero: %v", err)
	}
	if !strings.Contains(string(b), `"temperature":0`) {
		t.Fatalf("explicit zero temperature must be serialized: %s", b)
	}

	nonzero := c.buildRequest(provider.Request{Temperature: provider.TemperaturePtr(0.25)})
	if nonzero.Temperature == nil || *nonzero.Temperature != 0.25 {
		t.Fatalf("nonzero request temperature = %v, want ptr(0.25)", nonzero.Temperature)
	}
}

func TestBuildRequestKimiK3OfficialWireShape(t *testing.T) {
	p, err := New(provider.Config{
		Name:    "kimi-cn",
		BaseURL: "https://api.moonshot.cn/v1",
		Model:   "kimi-k3",
		APIKey:  "k",
		Extra: map[string]any{
			"effort":             "max",
			"supported_efforts":  []string{"low", "high", "max"},
			"reasoning_protocol": "openai",
			"extra_body": map[string]any{
				"top_p":                 0.5,
				"n":                     2,
				"presence_penalty":      1,
				"frequency_penalty":     1,
				"max_completion_tokens": 99,
				"trace_id":              "keep-me",
			},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !provider.RequiresReasoningRoundTrip(p) {
		t.Fatal("official Kimi K3 must retain raw reasoning for complete assistant-message replay")
	}
	req := p.(*client).buildRequest(provider.Request{
		Temperature: provider.TemperaturePtr(0),
		MaxTokens:   2000,
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "first"},
			{Role: provider.RoleAssistant, Content: "answer", ReasoningContent: "provider reasoning"},
			{Role: provider.RoleUser, Content: "use a tool"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "lookup", Arguments: `{}`}}},
			{Role: provider.RoleTool, ToolCallID: "call-1", Name: "lookup", Content: "result"},
		},
	})
	if req.Temperature != nil || req.MaxTokens != 0 || req.MaxCompletionTokens != 2000 {
		t.Fatalf("Kimi K3 request limits = temperature %v, max_tokens %d, max_completion_tokens %d", req.Temperature, req.MaxTokens, req.MaxCompletionTokens)
	}
	if req.ReasoningEffort != "max" {
		t.Fatalf("reasoning_effort = %q, want max", req.ReasoningEffort)
	}
	if got := req.Messages[1].ReasoningContent; got == nil || *got != "provider reasoning" {
		t.Fatalf("plain assistant reasoning_content = %v, want provider reasoning", got)
	}
	if got := req.Messages[3].ReasoningContent; got == nil || *got != "" {
		t.Fatalf("tool-call assistant reasoning_content = %v, want explicit empty string", got)
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, field := range []string{"temperature", "max_tokens", "top_p", "n", "presence_penalty", "frequency_penalty"} {
		if _, ok := wire[field]; ok {
			t.Fatalf("official Kimi K3 payload must omit %q: %s", field, body)
		}
	}
	if wire["max_completion_tokens"] != float64(2000) || wire["trace_id"] != "keep-me" {
		t.Fatalf("Kimi K3 payload lost output budget or unrelated extra body: %s", body)
	}

	gateway, err := New(provider.Config{
		Name:    "opencode-go",
		BaseURL: "https://opencode.ai/zen/go/v1",
		Model:   "kimi-k3",
		Extra: map[string]any{
			"effort":            "max",
			"supported_efforts": []string{"high", "max"},
		},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	if provider.RequiresReasoningRoundTrip(gateway) {
		t.Fatal("Kimi-specific wire policy must not be inferred for a relay")
	}
	gatewayReq := gateway.(*client).buildRequest(provider.Request{Temperature: provider.TemperaturePtr(0), MaxTokens: 77})
	if gatewayReq.Temperature == nil || gatewayReq.MaxTokens != 77 || gatewayReq.MaxCompletionTokens != 0 {
		t.Fatalf("relay request was changed by official Kimi compatibility: %+v", gatewayReq)
	}
}

func TestBuildRequestUsesProviderSpecificOutputBudget(t *testing.T) {
	newClient := func(t *testing.T, baseURL, model string, maxOutputTokens int) *client {
		t.Helper()
		p, err := New(provider.Config{
			Name: "test", BaseURL: baseURL, Model: model,
			Extra: map[string]any{"max_output_tokens": maxOutputTokens},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return p.(*client)
	}

	// Official DeepSeek auto omits max_tokens so the server uses its 384K ceiling.
	// Effort only selects thinking depth; it must not invent a 16/32/64K cap.
	deepseek := newClient(t, "https://api.deepseek.com", "deepseek-v4-flash", 0).buildRequest(provider.Request{})
	if deepseek.MaxTokens != 0 || deepseek.MaxCompletionTokens != 0 {
		t.Fatalf("DeepSeek auto budget = max_tokens %d, max_completion_tokens %d, want omitted",
			deepseek.MaxTokens, deepseek.MaxCompletionTokens)
	}

	lowEffort, err := New(provider.Config{
		Name: "test", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-flash",
		Extra: map[string]any{"effort": "low", "max_output_tokens": 0},
	})
	if err != nil {
		t.Fatalf("New low-effort DeepSeek: %v", err)
	}
	lowReq := lowEffort.(*client).buildRequest(provider.Request{})
	if lowReq.MaxTokens != 0 {
		t.Fatalf("low-effort auto budget = %d, want omitted", lowReq.MaxTokens)
	}

	thinkingDisabledProvider, err := New(provider.Config{
		Name: "test", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-pro",
		Extra: map[string]any{"thinking": "disabled", "max_output_tokens": 0},
	})
	if err != nil {
		t.Fatalf("New thinking-disabled DeepSeek: %v", err)
	}
	thinkingDisabled := thinkingDisabledProvider.(*client).buildRequest(provider.Request{})
	if thinkingDisabled.MaxTokens != 0 {
		t.Fatalf("thinking-disabled DeepSeek auto budget = %d, want omitted", thinkingDisabled.MaxTokens)
	}
	effortDisabledProvider, err := New(provider.Config{
		Name: "test", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-pro",
		Extra: map[string]any{"effort": "disabled", "max_output_tokens": 0},
	})
	if err != nil {
		t.Fatalf("New effort-disabled DeepSeek: %v", err)
	}
	effortDisabled := effortDisabledProvider.(*client).buildRequest(provider.Request{})
	if effortDisabled.MaxTokens != 0 || effortDisabled.Thinking == nil || effortDisabled.Thinking.Type != "disabled" {
		t.Fatalf("effort-disabled DeepSeek request = %+v, want thinking disabled with omitted budget", effortDisabled)
	}

	explicitDisabledProvider, err := New(provider.Config{
		Name: "test", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-pro",
		Extra: map[string]any{"thinking": "disabled", "max_output_tokens": 8192},
	})
	if err != nil {
		t.Fatalf("New explicitly capped DeepSeek: %v", err)
	}
	explicitDisabled := explicitDisabledProvider.(*client).buildRequest(provider.Request{})
	if explicitDisabled.MaxTokens != 8192 {
		t.Fatalf("explicit thinking-disabled DeepSeek budget = %d, want 8192", explicitDisabled.MaxTokens)
	}

	disabledDeepSeek := newClient(t, "https://api.deepseek.com", "deepseek-v4-flash", -1).buildRequest(provider.Request{})
	if disabledDeepSeek.MaxTokens != 0 || disabledDeepSeek.MaxCompletionTokens != 0 {
		t.Fatalf("disabled DeepSeek output budget = %+v", disabledDeepSeek)
	}

	officialOpenAI := newClient(t, "https://api.openai.com/v1", "o3", 8192).buildRequest(provider.Request{})
	if officialOpenAI.MaxTokens != 0 || officialOpenAI.MaxCompletionTokens != 8192 {
		t.Fatalf("official OpenAI output budget = max_tokens %d, max_completion_tokens %d", officialOpenAI.MaxTokens, officialOpenAI.MaxCompletionTokens)
	}

	gateway := newClient(t, "https://gateway.example/v1", "plain-chat", 8192).buildRequest(provider.Request{})
	if gateway.MaxTokens != 8192 || gateway.MaxCompletionTokens != 0 {
		t.Fatalf("compatible gateway output budget = max_tokens %d, max_completion_tokens %d", gateway.MaxTokens, gateway.MaxCompletionTokens)
	}

	unspecifiedGateway := newClient(t, "https://gateway.example/v1", "plain-chat", 0).buildRequest(provider.Request{})
	if unspecifiedGateway.MaxTokens != 0 || unspecifiedGateway.MaxCompletionTokens != 0 {
		t.Fatalf("unspecified compatible gateway received a budget: %+v", unspecifiedGateway)
	}
}

func TestBuildRequestDeepSeekThinking(t *testing.T) {
	for _, tc := range []struct {
		name          string
		effort        string
		wantThinking  string
		wantReasoning string
	}{
		{name: "high", effort: "high", wantThinking: "enabled", wantReasoning: "high"},
		{name: "max", effort: "max", wantThinking: "enabled", wantReasoning: "max"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := (&client{model: "deepseek-v4", deepseek: true, effort: tc.effort}).buildRequest(provider.Request{})
			if req.Thinking == nil || req.Thinking.Type != tc.wantThinking {
				t.Fatalf("Thinking = %+v, want %q", req.Thinking, tc.wantThinking)
			}
			if req.ReasoningEffort != tc.wantReasoning {
				t.Fatalf("ReasoningEffort = %q, want %q", req.ReasoningEffort, tc.wantReasoning)
			}
		})
	}
}

func TestBuildRequestDeepSeekPreservesCallerTemperature(t *testing.T) {
	c := &client{model: "deepseek-v4", deepseek: true, effort: "high"}

	omitted := c.buildRequest(provider.Request{})
	if omitted.Temperature != nil {
		t.Fatalf("DeepSeek default temperature = %v, want omitted", omitted.Temperature)
	}

	zero := c.buildRequest(provider.Request{Temperature: provider.TemperaturePtr(0)})
	if zero.Temperature == nil || *zero.Temperature != 0 {
		t.Fatalf("DeepSeek explicit zero temperature = %v, want ptr(0)", zero.Temperature)
	}
	if zero.Thinking == nil || zero.Thinking.Type != "enabled" {
		t.Fatalf("DeepSeek thinking = %+v, want enabled", zero.Thinking)
	}
}

// TestBuildRequestMiniMaxThinking covers the M3 wire shape: thinking.type is
// the only knob (no reasoning_effort), and the empty-effort / auto case still
// emits an explicit "adaptive" because that's what the M3 model default means
// (M3 has no implicit "no thinking" mode at the wire level).
func TestBuildRequestMiniMaxThinking(t *testing.T) {
	for _, tc := range []struct {
		name         string
		effort       string
		wantThinking string
	}{
		{name: "auto-defaults-to-adaptive", effort: "", wantThinking: "adaptive"},
		{name: "adaptive", effort: "adaptive", wantThinking: "adaptive"},
		{name: "disabled", effort: "disabled", wantThinking: "disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := (&client{model: "MiniMax-M3", minimax: true, effort: tc.effort}).buildRequest(provider.Request{})
			if req.Thinking == nil || req.Thinking.Type != tc.wantThinking {
				t.Fatalf("Thinking = %+v, want %q", req.Thinking, tc.wantThinking)
			}
			if req.ReasoningEffort != "" {
				t.Fatalf("MiniMax must not send reasoning_effort, got %q", req.ReasoningEffort)
			}
		})
	}
}

// TestNewMiniMaxEffortValidation locks in the boot-time validation for the
// MiniMax path. The config effort layer remaps legacy level names, so by the
// time effort reaches this factory it must be one of: "", "adaptive",
// "disabled". Anything else is a config bug, surfaced now (not at request
// time) for an actionable error.
func TestNewMiniMaxEffortValidation(t *testing.T) {
	base := provider.Config{Name: "m3", BaseURL: "https://api.minimaxi.com/v1", Model: "MiniMax-M3", APIKey: "k"}
	// happy path: auto (empty effort) and both explicit values are accepted
	for _, ok := range []string{"", "adaptive", "disabled"} {
		if _, err := New(withEffort(base, ok)); err != nil {
			t.Errorf("effort=%q should be accepted: %v", ok, err)
		}
	}
	// unhappy: anything else is rejected up front
	for _, bad := range []string{"high", "low", "max", "turbo"} {
		if _, err := New(withEffort(base, bad)); err == nil {
			t.Errorf("effort=%q should be rejected", bad)
		}
	}
}

// TestNewMiniMaxSetsFlag is a smoke test for base-URL detection: the factory
// must set the `minimax` flag when the base URL points at api.minimaxi.com
// (with or without the /v1 suffix) so buildRequest picks the right wire shape.
func TestNewMiniMaxSetsFlag(t *testing.T) {
	for _, baseURL := range []string{
		"https://api.minimaxi.com/v1",
		"https://api.minimaxi.com",
	} {
		p, err := New(provider.Config{Name: "m3", BaseURL: baseURL, Model: "MiniMax-M3", APIKey: "k"})
		if err != nil {
			t.Fatalf("New(%q): %v", baseURL, err)
		}
		c := p.(*client)
		if !c.minimax {
			t.Errorf("minimax flag not set for baseURL=%q", baseURL)
		}
	}
}

// TestBuildRequestZhipuThinking covers the Zhipu GLM wire shape: thinking.type
// is enabled|disabled and reasoning_effort is never sent (the endpoint ignores
// it). Auto (empty effort) defaults to "enabled" — the GLM model default.
func TestBuildRequestZhipuThinking(t *testing.T) {
	for _, tc := range []struct {
		name         string
		effort       string
		wantThinking string
	}{
		{name: "auto-defaults-to-enabled", effort: "", wantThinking: "enabled"},
		{name: "enabled", effort: "enabled", wantThinking: "enabled"},
		{name: "disabled", effort: "disabled", wantThinking: "disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := (&client{model: "glm-4.5-air", zhipu: true, effort: tc.effort}).buildRequest(provider.Request{})
			if req.Thinking == nil || req.Thinking.Type != tc.wantThinking {
				t.Fatalf("Thinking = %+v, want %q", req.Thinking, tc.wantThinking)
			}
			if req.ReasoningEffort != "" {
				t.Fatalf("Zhipu must not send reasoning_effort, got %q", req.ReasoningEffort)
			}
		})
	}
}

// TestNewZhipuEffortValidation locks in boot-time validation for the Zhipu path.
// The config effort layer remaps depth levels, so by the time effort reaches the
// factory it must be one of: "", "enabled", "disabled".
func TestNewZhipuEffortValidation(t *testing.T) {
	base := provider.Config{Name: "glm", BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-4.5-air", APIKey: "k"}
	for _, ok := range []string{"", "enabled", "disabled"} {
		if _, err := New(withEffort(base, ok)); err != nil {
			t.Errorf("effort=%q should be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{"high", "low", "max", "adaptive"} {
		if _, err := New(withEffort(base, bad)); err == nil {
			t.Errorf("effort=%q should be rejected", bad)
		}
	}
}

// TestNewZhipuSetsFlag is a smoke test for base-URL detection across both the
// China (bigmodel.cn) and international (z.ai) GLM endpoints.
func TestNewZhipuSetsFlag(t *testing.T) {
	for _, baseURL := range []string{
		"https://open.bigmodel.cn/api/paas/v4",
		"https://api.z.ai/api/paas/v4",
	} {
		p, err := New(provider.Config{Name: "glm", BaseURL: baseURL, Model: "glm-4.5-air", APIKey: "k"})
		if err != nil {
			t.Fatalf("New(%q): %v", baseURL, err)
		}
		if c := p.(*client); !c.zhipu {
			t.Errorf("zhipu flag not set for baseURL=%q", baseURL)
		}
	}
}

func TestNewExplicitGLMProtocolOnGateway(t *testing.T) {
	for _, tc := range []struct {
		effort string
		want   string
	}{
		{effort: "", want: "enabled"},
		{effort: "enabled", want: "enabled"},
		{effort: "disabled", want: "disabled"},
	} {
		p, err := New(provider.Config{
			Name:    "glm-gateway",
			BaseURL: "https://gateway.example.com/v1",
			Model:   "glm-5.2",
			APIKey:  "k",
			Extra: map[string]any{
				"reasoning_protocol": "glm",
				"effort":             tc.effort,
			},
		})
		if err != nil {
			t.Fatalf("New(explicit GLM, effort=%q): %v", tc.effort, err)
		}
		c := p.(*client)
		if !c.zhipu {
			t.Fatalf("explicit GLM protocol did not select GLM wire shape")
		}
		req := c.buildRequest(provider.Request{})
		if req.Thinking == nil || req.Thinking.Type != tc.want {
			t.Fatalf("effort=%q thinking = %+v, want %q", tc.effort, req.Thinking, tc.want)
		}
		if req.ReasoningEffort != "" {
			t.Fatalf("explicit GLM protocol sent reasoning_effort=%q", req.ReasoningEffort)
		}
	}
}

func TestBuildRequestRoundTripsGLMReasoningHistory(t *testing.T) {
	build := func(effort string) (*client, chatRequest) {
		p, err := New(provider.Config{
			Name:    "glm-gateway",
			BaseURL: "https://tokenrhythm.studio/v1",
			Model:   "glm-5.2",
			APIKey:  "k",
			Extra: map[string]any{
				"reasoning_protocol": "glm",
				"effort":             effort,
			},
		})
		if err != nil {
			t.Fatalf("New(GLM, effort=%q): %v", effort, err)
		}
		c := p.(*client)
		out := c.buildRequest(provider.Request{Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "inspect"},
			{Role: provider.RoleAssistant, ReasoningContent: "read main.go first", ToolCalls: []provider.ToolCall{{
				ID: "call_1", Name: "read_file", Arguments: `{"path":"main.go"}`,
			}}},
			{Role: provider.RoleTool, ToolCallID: "call_1", Name: "read_file", Content: "package main"},
			{Role: provider.RoleUser, Content: "continue"},
			{Role: provider.RoleAssistant, Content: "done", ReasoningContent: "combine the result"},
		}})
		return c, out
	}

	enabled, enabledReq := build("enabled")
	if enabled.RequiresToolCallReasoning() || !enabled.RequiresReasoningRoundTrip() {
		t.Fatal("thinking-enabled GLM must preserve complete reasoning history without enabling DeepSeek recovery policy")
	}
	if got := enabledReq.Messages[1].ReasoningContent; got == nil || *got != "read main.go first" {
		t.Fatalf("enabled GLM reasoning_content = %v, want provider-issued reasoning", got)
	}
	if got := enabledReq.Messages[4].ReasoningContent; got == nil || *got != "combine the result" {
		t.Fatalf("enabled GLM plain-turn reasoning_content = %v, want complete reasoning history", got)
	}
	if provider.WarnOnMissingToolCallReasoning(enabled) {
		t.Fatal("GLM must preserve available reasoning without entering DeepSeek-specific missing-reasoning recovery")
	}

	disabled, disabledReq := build("disabled")
	if disabled.RequiresToolCallReasoning() || disabled.RequiresReasoningRoundTrip() {
		t.Fatal("thinking-disabled GLM must not require new reasoning round trips")
	}
	if got := disabledReq.Messages[1].ReasoningContent; got == nil || *got != "read main.go first" {
		t.Fatalf("disabled GLM must preserve reasoning from an earlier thinking round, got %v", got)
	}
	if got := disabledReq.Messages[4].ReasoningContent; got == nil || *got != "combine the result" {
		t.Fatalf("disabled GLM must preserve plain reasoning from an earlier thinking round, got %v", got)
	}
}

// TestBuildRequestGenericThinking covers the vendor-agnostic `thinking` config
// field on a provider we don't auto-detect: thinking.type is emitted as set, and
// an empty/unset field leaves thinking off the wire entirely.
func TestBuildRequestGenericThinking(t *testing.T) {
	for _, tc := range []struct {
		name     string
		thinking string
		wantType string // "" means no thinking field
	}{
		{name: "enabled", thinking: "enabled", wantType: "enabled"},
		{name: "disabled", thinking: "disabled", wantType: "disabled"},
		{name: "unset-omits", thinking: "", wantType: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := (&client{model: "some-model", thinkingType: tc.thinking}).buildRequest(provider.Request{})
			if tc.wantType == "" {
				if req.Thinking != nil {
					t.Fatalf("expected no thinking, got %+v", req.Thinking)
				}
				return
			}
			if req.Thinking == nil || req.Thinking.Type != tc.wantType {
				t.Fatalf("Thinking = %+v, want %q", req.Thinking, tc.wantType)
			}
		})
	}
}

// TestNewThinkingConfigParsing pins how the `thinking` config field is read:
// enabled|disabled are kept (case-insensitively), everything else is ignored so
// an unknown value can never break a request.
func TestNewThinkingConfigParsing(t *testing.T) {
	base := provider.Config{Name: "gen", BaseURL: "https://api.example.com/v1", Model: "x", APIKey: "k"}
	for in, want := range map[string]string{"enabled": "enabled", "DISABLED": "disabled", "adaptive": "", "garbage": "", "": ""} {
		cfg := base
		cfg.Extra = map[string]any{"thinking": in}
		p, err := New(cfg)
		if err != nil {
			t.Fatalf("New(thinking=%q): %v", in, err)
		}
		if got := p.(*client).thinkingType; got != want {
			t.Errorf("thinking=%q → thinkingType=%q, want %q", in, got, want)
		}
	}
}

// TestBuildRequestDeepSeekDisabled covers both user-facing ways to turn
// DeepSeek thinking off. Either input must route to thinking.type=disabled,
// drop reasoning_effort, and preserve provider-issued reasoning from earlier
// assistant turns while still omitting an empty key for a reasoning-less tool
// turn.
func TestBuildRequestDeepSeekDisabled(t *testing.T) {
	base := provider.Config{Name: "ds", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4", APIKey: "k"}
	for _, tc := range []struct {
		name  string
		extra map[string]any
	}{
		{name: "effort-disabled", extra: map[string]any{"effort": "disabled"}},
		{name: "thinking-disabled", extra: map[string]any{"thinking": "disabled"}},
		{
			name: "effort-disabled-with-explicit-levels",
			extra: map[string]any{
				"effort":            "disabled",
				"supported_efforts": []string{"disabled", "high", "max"},
			},
		},
		{
			name: "thinking-disabled-overrides-explicit-levels",
			extra: map[string]any{
				"thinking":          "disabled",
				"effort":            "max",
				"supported_efforts": []string{"high"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.Extra = tc.extra
			p, err := New(cfg)
			if err != nil {
				t.Fatalf("New(%v): %v", tc.extra, err)
			}
			req := p.(*client).buildRequest(provider.Request{
				Messages: []provider.Message{
					{Role: provider.RoleUser, Content: "inspect"},
					{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
						ID: "call_1", Name: "read_file", Arguments: `{"path":"main.go"}`,
					}}},
					{Role: provider.RoleTool, ToolCallID: "call_1", Name: "read_file", Content: "package main"},
					{Role: provider.RoleAssistant, ReasoningContent: "from a thinking round", ToolCalls: []provider.ToolCall{{
						ID: "call_2", Name: "read_file", Arguments: `{"path":"go.mod"}`,
					}}},
					{Role: provider.RoleTool, ToolCallID: "call_2", Name: "read_file", Content: "module demo"},
					{Role: provider.RoleAssistant, Content: "plain answer", ReasoningContent: "plain reasoning"},
				},
			})
			if req.Thinking == nil || req.Thinking.Type != "disabled" {
				t.Fatalf("Thinking = %+v, want disabled", req.Thinking)
			}
			if req.ReasoningEffort != "" {
				t.Fatalf("disabled DeepSeek must not send reasoning_effort, got %q", req.ReasoningEffort)
			}
			if rc := req.Messages[1].ReasoningContent; rc != nil {
				t.Fatalf("disabled mode must omit reasoning_content on a reasoning-less tool_calls turn, got %q", *rc)
			}
			if rc := req.Messages[3].ReasoningContent; rc == nil || *rc != "from a thinking round" {
				t.Fatalf("disabled mode must keep round-tripping thinking-round reasoning, got %v", rc)
			}
			if rc := req.Messages[5].ReasoningContent; rc == nil || *rc != "plain reasoning" {
				t.Fatalf("disabled mode must keep round-tripping plain-turn reasoning, got %v", rc)
			}
		})
	}
}

func withEffort(c provider.Config, effort string) provider.Config {
	extra := c.Extra
	if extra == nil {
		extra = map[string]any{}
	} else {
		cp := make(map[string]any, len(extra)+1)
		maps.Copy(cp, extra)
		extra = cp
	}
	extra["effort"] = effort
	c.Extra = extra
	return c
}

func TestBuildRequestNonDeepSeekOmitsThinking(t *testing.T) {
	req := (&client{model: "mimo-v2", effort: "high"}).buildRequest(provider.Request{})
	if req.Thinking != nil {
		t.Fatalf("non-DeepSeek request must not include thinking, got %+v", req.Thinking)
	}
	if req.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %q, want high", req.ReasoningEffort)
	}
}

func TestNewOllamaCloudReasoningEffort(t *testing.T) {
	p, err := New(provider.Config{Name: "ollama-cloud", BaseURL: "https://ollama.com/v1", Model: "nemotron-3-nano:30b", Extra: map[string]any{"effort": "max"}})
	if err != nil {
		t.Fatalf("New max: %v", err)
	}
	c := p.(*client)
	if got := c.buildRequest(provider.Request{}).ReasoningEffort; got != "max" {
		t.Fatalf("Ollama Cloud reasoning_effort = %q, want max", got)
	}

	p, err = New(provider.Config{Name: "ollama-cloud", BaseURL: "https://ollama.com/v1", Model: "nemotron-3-nano:30b", Extra: map[string]any{"effort": "none"}})
	if err != nil {
		t.Fatalf("New none: %v", err)
	}
	c = p.(*client)
	b, err := json.Marshal(c.buildRequest(provider.Request{}))
	if err != nil {
		t.Fatalf("marshal none: %v", err)
	}
	if strings.Contains(string(b), "reasoning_effort") {
		t.Fatalf("Ollama Cloud effort none must omit reasoning_effort: %s", b)
	}

	if _, err := New(provider.Config{Name: "ollama-cloud", BaseURL: "https://ollama.com/v1", Model: "nemotron-3-nano:30b", Extra: map[string]any{"effort": "ultra"}}); err == nil {
		t.Fatal("New invalid effort succeeded, want error")
	}
}

func TestNewDeepSeekThinkingDefaultsAndValidation(t *testing.T) {
	p, err := New(provider.Config{Name: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c := p.(*client)
	if !c.deepseek || c.effort != "high" {
		t.Fatalf("deepseek=%v effort=%q, want true/high", c.deepseek, c.effort)
	}

	p, err = New(provider.Config{Name: "deepseek", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-v4", Extra: map[string]any{"effort": "max"}})
	if err != nil {
		t.Fatalf("New max: %v", err)
	}
	if got := p.(*client).effort; got != "max" {
		t.Fatalf("effort = %q, want max", got)
	}

	if _, err := New(provider.Config{Name: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4", Extra: map[string]any{"effort": "medium"}}); err == nil {
		t.Fatal("New should reject invalid DeepSeek effort")
	}
	p, err = New(provider.Config{Name: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4", Extra: map[string]any{"effort": "off"}})
	if err != nil {
		t.Fatalf("New should migrate retired effort=off, not reject it: %v", err)
	}
	if got := p.(*client).effort; got != "high" {
		t.Fatalf("retired effort=off should fall back to high, got %q", got)
	}
}

func TestNewReadsEffortFromConfig(t *testing.T) {
	p, err := New(provider.Config{
		Name:    "mimo",
		BaseURL: "https://api.example.com",
		Model:   "mimo-v2",
		Extra:   map[string]any{"effort": "medium", "supported_efforts": []string{"low", "medium", "high"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := p.(*client).effort; got != "medium" {
		t.Errorf("effort = %q, want medium", got)
	}
}

func TestStreamReadsReasoningFallbackField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"reasoning":"vllm thinking","content":"answer"}}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := New(provider.Config{Name: "vllm", BaseURL: srv.URL, Model: "qwen", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch, err := p.Stream(context.Background(), provider.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var reasoning, text strings.Builder
	for chunk := range ch {
		switch chunk.Type {
		case provider.ChunkReasoning:
			reasoning.WriteString(chunk.Text)
		case provider.ChunkText:
			text.WriteString(chunk.Text)
		case provider.ChunkError:
			t.Fatalf("stream error: %v", chunk.Err)
		}
	}
	if reasoning.String() != "vllm thinking" {
		t.Fatalf("reasoning = %q, want vLLM fallback field", reasoning.String())
	}
	if text.String() != "answer" {
		t.Fatalf("text = %q, want answer", text.String())
	}
}

func TestStreamReasoningContentTakesPrecedenceOverFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"reasoning_content":"standard","reasoning":"fallback"}}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := New(provider.Config{Name: "vllm", BaseURL: srv.URL, Model: "qwen", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch, err := p.Stream(context.Background(), provider.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var reasoning strings.Builder
	for chunk := range ch {
		switch chunk.Type {
		case provider.ChunkReasoning:
			reasoning.WriteString(chunk.Text)
		case provider.ChunkError:
			t.Fatalf("stream error: %v", chunk.Err)
		}
	}
	if reasoning.String() != "standard" {
		t.Fatalf("reasoning = %q, want reasoning_content precedence", reasoning.String())
	}
}

// TestBuildRequestAlwaysSendsReasoningKeyOnDeepSeekToolCalls proves the wire
// contract verified against the live API: DeepSeek thinking mode 400s an
// assistant tool_calls turn whose reasoning_content KEY is missing from the
// request JSON, but accepts an empty string. A turn whose reasoning was lost
// upstream (gateway renamed/dropped the field, legacy session, model switch)
// must therefore still serialize the key, while plain assistant text turns
// carrying reasoning are replayed too.
func TestBuildRequestAlwaysSendsReasoningKeyOnDeepSeekToolCalls(t *testing.T) {
	p, err := New(provider.Config{
		Name:    "deepseek-proxy",
		BaseURL: "https://api.deepseek.com",
		Model:   "deepseek-v4-pro",
		APIKey:  "k",
		Extra:   map[string]any{"reasoning_protocol": "deepseek"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body, err := json.Marshal(p.(*client).buildRequest(provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "inspect"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
				ID: "call_1", Name: "read_file", Arguments: `{"path":"main.go"}`,
			}}},
			{Role: provider.RoleTool, ToolCallID: "call_1", Name: "read_file", Content: "package main"},
			{Role: provider.RoleAssistant, Content: "plain text turn", ReasoningContent: "plain reasoning"},
		},
	}))
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var req struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if len(req.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(req.Messages))
	}
	rc, ok := req.Messages[1]["reasoning_content"]
	if !ok {
		t.Fatal("tool_calls turn with lost reasoning must still serialize the reasoning_content key")
	}
	if string(rc) != `""` {
		t.Fatalf("reasoning_content = %s, want empty string", rc)
	}
	if got, ok := req.Messages[3]["reasoning_content"]; !ok || string(got) != `"plain reasoning"` {
		t.Fatalf("plain assistant text turn reasoning_content = %s, want plain reasoning", got)
	}
}

func TestWarnOnMissingToolCallReasoningFollowsDeepSeekThinkingModels(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{model: "deepseek-v4-flash", want: true},
		{model: "deepseek/deepseek-v4-flash", want: true},
		{model: "deepseek-v4-pro", want: true},
		{model: "deepseek/deepseek-v4-pro", want: true},
		{model: "deepseek-ai/DeepSeek-V4-Pro", want: true},
		{model: "deepseek-reasoner", want: true},
		{model: "deepseek-ai/DeepSeek-R1-0528", want: true},
		{model: "deepseek-ai/DeepSeek-V3.2", want: true},
		{model: "deepseek-chat", want: false},
		{model: "deepseek-ai/DeepSeek-Prover-V2", want: false},
		{model: "custom-model", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			p, err := New(provider.Config{
				Name:    "deepseek-proxy",
				BaseURL: "https://gateway.example/v1",
				Model:   tc.model,
				APIKey:  "k",
				Extra:   map[string]any{"reasoning_protocol": "deepseek"},
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if !provider.RequiresToolCallReasoning(p) {
				t.Fatal("DeepSeek protocol should keep conservative reasoning_content replay for tool-call turns")
			}
			if got := provider.WarnOnMissingToolCallReasoning(p); got != tc.want {
				t.Fatalf("WarnOnMissingToolCallReasoning() = %v, want %v", got, tc.want)
			}
		})
	}

	explicitThinking, err := New(provider.Config{
		Name: "custom-thinking", BaseURL: "https://gateway.example/v1", Model: "custom-model", APIKey: "k",
		Extra: map[string]any{"reasoning_protocol": "deepseek", "thinking": "enabled"},
	})
	if err != nil {
		t.Fatalf("New explicit thinking provider: %v", err)
	}
	if !provider.WarnOnMissingToolCallReasoning(explicitThinking) {
		t.Fatal("explicit DeepSeek thinking must diagnose missing tool-call reasoning")
	}

	p, err := New(provider.Config{
		Name:    "deepseek-v4-pro-openai-protocol",
		BaseURL: "https://gateway.example/v1",
		Model:   "deepseek-v4-pro",
		APIKey:  "k",
		Extra:   map[string]any{"reasoning_protocol": "openai"},
	})
	if err != nil {
		t.Fatalf("New OpenAI protocol: %v", err)
	}
	if provider.WarnOnMissingToolCallReasoning(p) {
		t.Fatal("OpenAI protocol should not warn using DeepSeek reasoning_content policy")
	}
	if provider.WarnOnMissingToolCallReasoning(&client{deepseek: true, thinkingType: "disabled"}) {
		t.Fatal("disabled thinking must not diagnose missing tool-call reasoning")
	}
}

func TestMissingToolCallReasoningWarningFingerprintTracksOpenAIConfiguration(t *testing.T) {
	newProvider := func(baseURL, model string) provider.Provider {
		p, err := New(provider.Config{
			Name: "deepseek", BaseURL: baseURL, Model: model, APIKey: "secret",
			Extra: map[string]any{"reasoning_protocol": "deepseek", "effort": "high"},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return p
	}
	first := provider.MissingToolCallReasoningWarningFingerprint(newProvider("https://gateway.example/v1", "deepseek-v4-pro"))
	same := provider.MissingToolCallReasoningWarningFingerprint(newProvider("https://gateway.example/v1", "deepseek-v4-pro"))
	changedEndpoint := provider.MissingToolCallReasoningWarningFingerprint(newProvider("https://other.example/v1", "deepseek-v4-pro"))
	changedModel := provider.MissingToolCallReasoningWarningFingerprint(newProvider("https://gateway.example/v1", "deepseek-v4-flash"))
	if first != same {
		t.Fatal("equivalent OpenAI configurations produced different fingerprints")
	}
	if first == changedEndpoint || first == changedModel {
		t.Fatal("endpoint or model change did not re-key the warning fingerprint")
	}
	if len(first) != 64 || strings.Contains(first, "gateway") || strings.Contains(first, "deepseek") {
		t.Fatalf("fingerprint is not an opaque SHA-256 digest: %q", first)
	}
}

// TestBuildRequestRoundTripsDeepSeekToolCallReasoning keeps the healthy-path
// bytes intact: when the session has the provider-issued reasoning, it is
// replayed verbatim on the tool_calls turn.
func TestBuildRequestRoundTripsDeepSeekToolCallReasoning(t *testing.T) {
	p, err := New(provider.Config{
		Name:    "deepseek-proxy",
		BaseURL: "https://api.deepseek.com",
		Model:   "deepseek-v4-pro",
		APIKey:  "k",
		Extra:   map[string]any{"reasoning_protocol": "deepseek"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := p.(*client).buildRequest(provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "inspect"},
			{Role: provider.RoleAssistant, ReasoningContent: "read main.go first", ToolCalls: []provider.ToolCall{{
				ID: "call_1", Name: "read_file", Arguments: `{"path":"main.go"}`,
			}}},
			{Role: provider.RoleTool, ToolCallID: "call_1", Name: "read_file", Content: "package main"},
		},
	})
	got := out.Messages[1].ReasoningContent
	if got == nil || *got != "read main.go first" {
		t.Fatalf("reasoning_content = %v, want provider-issued reasoning round-tripped", got)
	}
}

// TestBuildRequestPreservesEmptyIDToolResults proves a multi-tool turn whose
// calls carry no id (some OpenAI-compatible gateways omit it, sending only the
// index) keeps every tool result through buildRequest. SanitizeToolPairing keys
// on tool_call_id, so empty ids collapse and all but the last result is dropped.
func TestBuildRequestPreservesEmptyIDToolResults(t *testing.T) {
	c := &client{model: "deepseek-v4"}
	req := c.buildRequest(provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "scan"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
				{ID: "", Name: "read_file", Arguments: `{"p":"a"}`},
				{ID: "", Name: "read_file", Arguments: `{"p":"b"}`},
			}},
			{Role: provider.RoleTool, ToolCallID: "", Name: "read_file", Content: "RESULT-A"},
			{Role: provider.RoleTool, ToolCallID: "", Name: "read_file", Content: "RESULT-B"},
		},
	})
	var toolContents []string
	for _, m := range req.Messages {
		if m.Role == string(provider.RoleTool) {
			if s, ok := m.Content.(string); ok {
				toolContents = append(toolContents, s)
			}
		}
	}
	if len(toolContents) != 2 {
		t.Fatalf("want 2 tool results in request, got %d: %v", len(toolContents), toolContents)
	}
	if toolContents[0] == toolContents[1] {
		t.Errorf("tool results collapsed to %q — a result was dropped from the model's context", toolContents[0])
	}
}

// TestStreamSynthesizesMissingToolCallIDs covers a gateway that streams tool
// calls by index with no id (vLLM / llama.cpp do this). Each completed call must
// come back with a stable, distinct synthetic id so its result can pair back.
func TestStreamSynthesizesMissingToolCallIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"read_file","arguments":"{\"p\":\"a\"}"}}]}}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"name":"read_file","arguments":"{\"p\":\"b\"}"}}]}}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := New(provider.Config{Name: "local", BaseURL: srv.URL, Model: "qwen", APIKey: "k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch, err := p.Stream(context.Background(), provider.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var ids []string
	for chunk := range ch {
		if chunk.Type == provider.ChunkToolCall && chunk.ToolCall != nil {
			ids = append(ids, chunk.ToolCall.ID)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("want 2 tool calls, got %d: %v", len(ids), ids)
	}
	if ids[0] == "" || ids[1] == "" {
		t.Errorf("a tool call came back with an empty id: %v", ids)
	}
	if ids[0] == ids[1] {
		t.Errorf("synthesized ids must be distinct, got %v", ids)
	}
}

func TestBuildRequestContentNullForAssistantToolCalls(t *testing.T) {
	c := &client{name: "x", model: "m", baseURL: "https://api.example.com/v1"}
	req := provider.Request{
		Messages: []provider.Message{
			{Role: provider.RoleAssistant, Content: "", ToolCalls: []provider.ToolCall{{ID: "c1", Name: "ls", Arguments: `{}`}}},
			{Role: provider.RoleTool, Content: "", ToolCallID: "c1", Name: "ls"},
			{Role: provider.RoleAssistant, Content: "all done"},
		},
		Tools: []provider.ToolSchema{{Name: "noargs", Parameters: provider.CanonicalizeSchema(nil)}},
	}
	body, err := json.Marshal(c.buildRequest(req))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(body) {
		t.Fatalf("invalid JSON body: %s", body)
	}
	s := string(body)
	if !strings.Contains(s, `"tool_calls"`) || !strings.Contains(s, `"content":null`) {
		t.Errorf("assistant tool_calls turn should carry null content: %s", s)
	}
	if !strings.Contains(s, `{"role":"tool","content":""`) {
		t.Errorf("tool message should keep empty-string content, not null: %s", s)
	}
	if !strings.Contains(s, `"content":"all done"`) {
		t.Errorf("text assistant turn should keep its string content: %s", s)
	}
	if !strings.Contains(s, `"parameters":{"properties":{},"type":"object"}`) {
		t.Errorf("no-param tool should serialize a strict empty-object schema: %s", s)
	}
}

func TestBuildRequestOmitsResponseOnlyToolCallIndex(t *testing.T) {
	c := &client{name: "x", model: "m", baseURL: "https://api.example.com/v1"}
	req := provider.Request{
		Messages: []provider.Message{{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{
				ID:        "call_1",
				Name:      "bash",
				Arguments: `{"cmd":"ls"}`,
			}},
		}},
	}
	body, err := json.Marshal(c.buildRequest(req))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, `"tool_calls"`) {
		t.Fatalf("request body missing tool call: %s", s)
	}
	if strings.Contains(s, `"index"`) {
		t.Fatalf("request body contains response-only tool_call index: %s", s)
	}
}

func TestBuildRequestDefaultsEmptyToolParameters(t *testing.T) {
	c := &client{name: "x", model: "m", baseURL: "https://api.example.com/v1"}
	req := provider.Request{
		Tools: []provider.ToolSchema{{Name: "noargs"}},
	}
	body, err := json.Marshal(c.buildRequest(req))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire struct {
		Tools []struct {
			Function map[string]json.RawMessage `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("unmarshal request: %v\n%s", err, body)
	}
	if len(wire.Tools) != 1 {
		t.Fatalf("tools = %d, want 1: %s", len(wire.Tools), body)
	}
	fn := wire.Tools[0].Function
	if string(fn["name"]) != `"noargs"` {
		t.Fatalf("function name = %s, want noargs", fn["name"])
	}
	if _, ok := fn["description"]; ok {
		t.Fatalf("empty description should be omitted: %s", body)
	}
	if got, want := string(fn["parameters"]), `{"properties":{},"type":"object"}`; got != want {
		t.Fatalf("nil parameters should default to %s, got %s in %s", want, got, body)
	}
}

func TestStreamReadsGeminiThoughtSignature(t *testing.T) {
	tests := []struct {
		name     string
		toolCall string
	}{
		{
			name:     "current extra_content shape",
			toolCall: `{"index":0,"id":"call_abc123","type":"function","extra_content":{"google":{"thought_signature":"gemini_sig_xyz789"}},"function":{"name":"write_file"}}`,
		},
		{
			name:     "legacy function shape",
			toolCall: `{"index":0,"id":"call_abc123","type":"function","function":{"name":"write_file","thought_signature":"gemini_sig_xyz789"}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w,
					"data: {\"choices\":[{\"delta\":{\"tool_calls\":["+tc.toolCall+"]}}]}\n\n"+
						"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"path\\\":\\\"test.txt\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"+
						"data: [DONE]\n\n")
			}))
			defer srv.Close()

			p, err := New(provider.Config{Name: "gemini", BaseURL: srv.URL, Model: "gemini-3.6-flash"})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			ch, err := p.Stream(context.Background(), provider.Request{})
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}

			var got *provider.ToolCall
			for chunk := range ch {
				if chunk.Type == provider.ChunkToolCall {
					got = chunk.ToolCall
				}
			}
			if got == nil {
				t.Fatal("expected ChunkToolCall but none received")
			}
			if got.ThoughtSignature != "gemini_sig_xyz789" {
				t.Errorf("ThoughtSignature = %q, want %q", got.ThoughtSignature, "gemini_sig_xyz789")
			}
			if got.Arguments != `{"path":"test.txt"}` {
				t.Errorf("Arguments = %q, want complete streamed arguments", got.Arguments)
			}
		})
	}
}

func TestBuildRequestScopesGeminiThoughtSignature(t *testing.T) {
	req := provider.Request{
		Messages: []provider.Message{{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{
				ID:               "call_abc123",
				Name:             "write_file",
				Arguments:        `{"path":"test.txt"}`,
				ThoughtSignature: "gemini_sig_xyz789",
			}},
		}},
	}

	for _, tc := range []struct {
		name          string
		baseURL       string
		model         string
		wantSignature string
	}{
		{"official Gemini endpoint", "https://generativelanguage.googleapis.com/v1beta/openai", "custom-alias", "gemini_sig_xyz789"},
		{"Gemini-compatible gateway", "https://openrouter.ai/api/v1", "google/gemini-3.1-pro", "gemini_sig_xyz789"},
		{"same history after provider switch", "https://api.deepseek.com/v1", "deepseek-chat", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &client{name: tc.name, baseURL: tc.baseURL, model: tc.model}
			body, err := json.Marshal(c.buildRequest(req))
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			var wire struct {
				Messages []struct {
					ToolCalls []struct {
						ExtraContent *struct {
							Google struct {
								ThoughtSignature string `json:"thought_signature"`
							} `json:"google"`
						} `json:"extra_content"`
						Function struct {
							ThoughtSignature string `json:"thought_signature"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Fatalf("unmarshal request: %v", err)
			}
			if len(wire.Messages) == 0 || len(wire.Messages[0].ToolCalls) != 1 {
				t.Fatalf("unexpected request shape: %s", body)
			}
			toolCall := wire.Messages[0].ToolCalls[0]
			gotSignature := ""
			if toolCall.ExtraContent != nil {
				gotSignature = toolCall.ExtraContent.Google.ThoughtSignature
			}
			if gotSignature != tc.wantSignature {
				t.Errorf("thought_signature = %q, want %q", gotSignature, tc.wantSignature)
			}
			if tc.wantSignature == "" && toolCall.ExtraContent != nil {
				t.Errorf("non-Gemini request should omit extra_content: %s", body)
			}
			if got := toolCall.Function.ThoughtSignature; got != "" {
				t.Errorf("legacy function.thought_signature should not be sent, got %q", got)
			}
		})
	}
}

func TestNormaliseUsageAnthropicStyleFallback(t *testing.T) {
	tests := []struct {
		name string
		json string
		want provider.Usage
	}{
		{
			name: "cache hit",
			json: `{"usage":{"input_tokens":21,"output_tokens":393,"cache_creation_input_tokens":0,"cache_read_input_tokens":188086}}`,
			want: provider.Usage{
				PromptTokens:     188107,
				CompletionTokens: 393,
				TotalTokens:      188500,
				CacheHitTokens:   188086,
				CacheMissTokens:  21,
			},
		},
		{
			name: "cache creation",
			json: `{"usage":{"input_tokens":21,"output_tokens":393,"cache_creation_input_tokens":188086,"cache_read_input_tokens":0}}`,
			want: provider.Usage{
				PromptTokens:     188107,
				CompletionTokens: 393,
				TotalTokens:      188500,
				CacheMissTokens:  188107,
			},
		},
		{
			name: "DeepSeek fields take priority",
			json: `{"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_cache_hit_tokens":30,"prompt_cache_miss_tokens":70,"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":888,"cache_read_input_tokens":999}}`,
			want: provider.Usage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
				CacheHitTokens:   30,
				CacheMissTokens:  70,
			},
		},
		{
			name: "OpenAI nested fields take priority",
			json: `{"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":40},"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":888,"cache_read_input_tokens":999}}`,
			want: provider.Usage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
				CacheHitTokens:   40,
				CacheMissTokens:  60,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var response streamResponse
			if err := json.Unmarshal([]byte(tc.json), &response); err != nil {
				t.Fatalf("unmarshal usage fixture: %v", err)
			}
			if response.Usage == nil {
				t.Fatal("usage fixture did not decode usage")
			}
			if got := normaliseUsage(response.Usage); got == nil || *got != tc.want {
				t.Fatalf("normaliseUsage() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
