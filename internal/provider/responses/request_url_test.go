package responses

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"reasonix/internal/provider"
)

func TestStreamUsesConfiguredRequestURLExactly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/custom/responses/?token=1" {
			t.Errorf("request URI = %q, want /custom/responses/?token=1", r.URL.RequestURI())
		}
		writeEvents(w, `{"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
	}))
	defer server.Close()

	collect(t, New(Config{
		Name: "custom-responses", APIKey: "key", BaseURL: server.URL + "/base", RequestURL: server.URL + "/custom/responses/?token=1", Model: "m", Mode: "stateless",
	}), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}})
}

func TestFactoryUsesRequestURLAndIgnoresLegacyChatURL(t *testing.T) {
	p, err := newFromConfig(provider.Config{
		BaseURL: "https://base.example.com/v1",
		Model:   "m",
		Extra: map[string]any{
			"chat_url":    "https://stale.example.com/chat/completions",
			"request_url": "https://exact.example.com/custom/responses/?token=1",
		},
	})
	if err != nil {
		t.Fatalf("newFromConfig: %v", err)
	}
	if got := p.(*client).requestURL; got != "https://exact.example.com/custom/responses/?token=1" {
		t.Fatalf("requestURL = %q, want exact request_url", got)
	}

	legacy, err := newFromConfig(provider.Config{
		BaseURL: "https://base.example.com/v1",
		Model:   "m",
		Extra:   map[string]any{"chat_url": "https://stale.example.com/chat/completions"},
	})
	if err != nil {
		t.Fatalf("newFromConfig legacy: %v", err)
	}
	if got := legacy.(*client).requestURL; got != "https://base.example.com/v1/responses" {
		t.Fatalf("legacy requestURL = %q, want base-derived endpoint", got)
	}
}

func TestRequestURLEqualToBaseURLIsIgnored(t *testing.T) {
	p, err := newFromConfig(provider.Config{
		BaseURL: "https://base.example.com/v1",
		Model:   "m",
		Extra:   map[string]any{"request_url": "https://base.example.com/v1"},
	})
	if err != nil {
		t.Fatalf("newFromConfig: %v", err)
	}
	if got := p.(*client).requestURL; got != "https://base.example.com/v1/responses" {
		t.Fatalf("requestURL = %q, want base-derived /responses endpoint", got)
	}
}
