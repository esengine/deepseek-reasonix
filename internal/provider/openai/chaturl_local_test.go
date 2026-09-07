package openai

import "testing"

// Local OpenAI-compatible servers (Ollama on :11434, LM Studio on :1234,
// llama.cpp, vLLM) serve their chat surface under /v1. A base_url entered as
// a bare host used to POST /chat/completions, which Ollama answers with 405.
func TestLocalHostBaseURLGainsV1Prefix(t *testing.T) {
	for _, base := range []string{
		"http://localhost:11434",
		"http://localhost:11434/",
		"http://127.0.0.1:11434",
		"http://localhost:1234/",
		"https://localhost:8080",
	} {
		if got := normalizeChatURL(base, ""); got != trimTrailingSlash(base)+"/v1/chat/completions" {
			t.Fatalf("normalizeChatURL(%q) = %q, want the /v1-prefixed chat URL", base, got)
		}
	}
}

// Any base URL that already carries a path — including an explicit /v1 — is
// the user's own routing decision and must not be rewritten.
func TestLocalHostBaseURLWithPathIsUntouched(t *testing.T) {
	for _, base := range []string{
		"http://localhost:11434/v1",
		"http://localhost:11434/ollama/v1",
		"http://127.0.0.1:8080/api",
	} {
		if got := normalizeChatURL(base, ""); got != trimTrailingSlash(base)+"/chat/completions" {
			t.Fatalf("normalizeChatURL(%q) = %q, want the explicit path preserved", base, got)
		}
	}
}

// Remote hosts are never rewritten, with or without a path.
func TestRemoteBaseURLsAreNeverRewritten(t *testing.T) {
	for _, base := range []string{
		"https://api.deepseek.com",
		"https://ollama.com/v1",
		"http://192.168.1.5:11434",
	} {
		if got := normalizeChatURL(base, ""); got != trimTrailingSlash(base)+"/chat/completions" {
			t.Fatalf("normalizeChatURL(%q) = %q, want plain suffix", base, got)
		}
	}
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
