package openai

import (
	"testing"

	"reasonix/internal/provider"
)

func TestNewPrefersExactRequestURLOverLegacyChatURL(t *testing.T) {
	p, err := New(provider.Config{
		BaseURL: "https://base.example.com/v1",
		Model:   "model-a",
		Extra: map[string]any{
			"chat_url":    "https://legacy.example.com/chat/completions/",
			"request_url": "https://exact.example.com/custom/?token=1",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := p.(*client).chatURL; got != "https://exact.example.com/custom/?token=1" {
		t.Fatalf("chatURL = %q, want exact request_url", got)
	}
}

func TestRequestURLEqualToBaseURLIsIgnored(t *testing.T) {
	p, err := New(provider.Config{
		BaseURL: "https://base.example.com/v1",
		Model:   "model-a",
		Extra:   map[string]any{"request_url": "https://base.example.com/v1"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := p.(*client).chatURL; got != "https://base.example.com/v1/chat/completions" {
		t.Fatalf("chatURL = %q, want base-derived chat/completions endpoint", got)
	}
}
