// Effort vocabulary and per-request depth resolution: which reasoning levels
// an endpoint accepts, and how a Request.EffortOverride maps onto them.
package openai

import (
	"strings"

	"reasonix/internal/provider"
)

// configuredThinkingType reads the optional explicit `thinking` config field —
// a vendor-agnostic escape hatch (credit @eghrhegpe, #5063) for OpenAI-
// compatible providers we don't auto-detect (e.g. opencode.ai). Unknown values
// are ignored so a typo never breaks a request.
func configuredThinkingType(cfg provider.Config) string {
	t, _ := cfg.Extra["thinking"].(string)
	t = strings.ToLower(strings.TrimSpace(t))
	if t != "enabled" && t != "disabled" {
		return ""
	}
	return t
}

// requestEffort resolves one request's reasoning depth: a vocabulary-approved
// EffortOverride wins after Stream validation; omission inherits configuration.
func (c *client) requestEffort(req provider.Request) string {
	if req.EffortOverride != "" {
		return req.EffortOverride
	}
	return c.effort
}

func (c *client) deepSeekRequestThinking(req provider.Request) string {
	if req.EffortOverride != "" && !c.thinkingLocked {
		if req.EffortOverride == "disabled" {
			return "disabled"
		}
		return "enabled"
	}
	if c.thinkingType == "disabled" || req.EffortOverride == "disabled" {
		return "disabled"
	}
	return "enabled"
}

func supportsEffort(levels []string, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	for _, level := range levels {
		if strings.ToLower(strings.TrimSpace(level)) == want {
			return true
		}
	}
	return false
}

func hasExplicitSupportedEfforts(levels []string) bool {
	for _, level := range levels {
		level = strings.ToLower(strings.TrimSpace(level))
		if level != "" && level != "auto" {
			return true
		}
	}
	return false
}

// PerRequestEfforts reports the depth levels a request-scoped EffortOverride
// may take on this endpoint (see requestEffort). Nil or empty means the
// endpoint cannot vary depth per request, so frontends that fix effort at
// boot must keep using the configured depth (or rebuild the runtime) instead
// of writing a per-request override that would silently degrade on the wire.
func (c *client) PerRequestEfforts() []string { return c.requestEfforts }
