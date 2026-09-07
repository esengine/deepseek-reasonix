package openai

import (
	"net/url"
	"strings"
)

// canonicalKnownVendorChatURL rewrites official-vendor bases whose documented
// form differs from the OpenAI-compatible shape (Token Rhythm, StepFun step_plan).
func canonicalKnownVendorChatURL(raw string) (string, bool) {
	if canonical, ok := canonicalTokenRhythmChatURL(raw); ok {
		return canonical, true
	}
	return canonicalStepFunPlanChatURL(raw)
}

// canonicalLocalHostChatURL appends the conventional /v1 prefix to a bare
// local base URL. Local OpenAI-compatible servers serve their chat surface
// under /v1 — Ollama on :11434, LM Studio on :1234, llama.cpp and vLLM by
// convention — so a base_url entered as "http://localhost:11434" must POST
// /v1/chat/completions, not /chat/completions (which Ollama answers with
// 405). Only localhost-family hosts with an empty path are rewritten; any
// base URL that already carries a path (including an explicit /v1) is left
// exactly as the user wrote it.
func canonicalLocalHostChatURL(raw string) (string, bool) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return "", false
	}
	u, err := url.Parse(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	host := u.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" && host != "[::1]" {
		return "", false
	}
	if u.Path != "" && u.Path != "/" {
		return "", false
	}
	return trimmed + "/v1", true
}

func resolveOpenAIChatURL(baseURL string, extra map[string]any) string {
	requestURL, _ := extra["request_url"].(string)
	requestURL = strings.TrimSpace(requestURL)
	if requestURL != "" {
		return requestURL
	}
	legacyChatURL, _ := extra["chat_url"].(string)
	return normalizeChatURL(baseURL, legacyChatURL)
}

func normalizeChatURL(baseURL, chatURL string) string {
	if legacy := strings.TrimRight(strings.TrimSpace(chatURL), "/"); legacy != "" {
		if canonical, ok := canonicalKnownVendorChatURL(legacy); ok {
			return canonical
		}
		return legacy
	}
	if canonical, ok := canonicalKnownVendorChatURL(baseURL); ok {
		return canonical
	}
	if canonical, ok := canonicalLocalHostChatURL(baseURL); ok {
		return canonical + "/chat/completions"
	}
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/chat/completions"
}
