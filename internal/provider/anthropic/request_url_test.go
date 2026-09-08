package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"reasonix/internal/provider"
)

func TestStreamUsesConfiguredRequestURLExactly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/custom/messages/?token=1" {
			t.Errorf("request URI = %q, want /custom/messages/?token=1", r.URL.RequestURI())
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
		Name:    "custom-anthropic",
		BaseURL: srv.URL + "/base",
		Model:   "model",
		APIKey:  "key",
		Extra:   map[string]any{"request_url": srv.URL + "/custom/messages/?token=1"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stream, err := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for chunk := range stream {
		if chunk.Type == provider.ChunkError {
			t.Fatalf("stream error: %v", chunk.Err)
		}
	}
}

func TestLegacyChatURLRemainsIgnored(t *testing.T) {
	p, err := New(provider.Config{
		BaseURL: "https://base.example.com/v1",
		Model:   "model",
		Extra:   map[string]any{"chat_url": "https://stale.example.com/chat/completions"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := p.(*client).requestURL; got != "https://base.example.com/v1/messages" {
		t.Fatalf("requestURL = %q, want legacy base-derived endpoint", got)
	}
}

func TestRequestURLEqualToBaseRootIsIgnored(t *testing.T) {
	p, err := New(provider.Config{
		BaseURL: "https://base.example.com/api",
		Model:   "model",
		Extra:   map[string]any{"request_url": "https://base.example.com/api"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := p.(*client).requestURL; got != "https://base.example.com/api/v1/messages" {
		t.Fatalf("requestURL = %q, want base-derived /v1/messages endpoint", got)
	}
}

func TestRequestURLEqualToV1BaseIsIgnored(t *testing.T) {
	p, err := New(provider.Config{
		BaseURL: "https://base.example.com/v1",
		Model:   "model",
		Extra:   map[string]any{"request_url": "https://base.example.com/v1"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := p.(*client).requestURL; got != "https://base.example.com/v1/messages" {
		t.Fatalf("requestURL = %q, want base-derived /v1/messages endpoint", got)
	}
}
