package openai

import "strings"

// canonicalKnownVendorChatURL rewrites official-vendor bases whose documented
// form differs from the OpenAI-compatible shape (Token Rhythm, StepFun step_plan).
func canonicalKnownVendorChatURL(raw string) (string, bool) {
	if canonical, ok := canonicalTokenRhythmChatURL(raw); ok {
		return canonical, true
	}
	return canonicalStepFunPlanChatURL(raw)
}

func resolveOpenAIChatURL(baseURL string, extra map[string]any) string {
	requestURL, _ := extra["request_url"].(string)
	requestURL = strings.TrimSpace(requestURL)
	if canonical, ok := canonicalKnownVendorChatURL(requestURL); ok {
		return canonical
	}
	// A request_url that merely repeats the base URL is a no-op: fall through
	// to the derived endpoint instead of POSTing to the base itself.
	if requestURL != "" && strings.TrimRight(requestURL, "/") != strings.TrimRight(strings.TrimSpace(baseURL), "/") {
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
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/chat/completions"
}
