package openai

import (
	"net/url"
	"slices"
	"strings"

	"reasonix/internal/provider"
)

// matchesVendorHost reports whether baseURL points at one of the canonical
// hostnames (exact match, case-insensitive) or at any subdomain of apex.
// Returns false on any parse error or empty host.
//
// We take the apex separately from the canonical because they differ: the
// canonical (e.g. api.minimaxi.com) is the specific endpoint, but regional
// subdomains like eu.minimaxi.com or us.minimaxi.com should also match —
// the wire shape is the same, just hosted in a different region. The bare
// apex (e.g. minimaxi.com) is intentionally rejected: it would only happen
// if the user pointed their base_url at the apex domain, which is a
// misconfiguration — not a path we want to silently accept.
func matchesVendorHost(baseURL, apex string, canonical ...string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if slices.Contains(canonical, host) {
		return true
	}
	return strings.HasSuffix(host, "."+apex)
}

// IsDeepSeek reports whether baseURL points at DeepSeek's API
// (api.deepseek.com or any *.deepseek.com subdomain).
func IsDeepSeek(baseURL string) bool {
	return matchesVendorHost(baseURL, "deepseek.com", "api.deepseek.com")
}

// OfficialDeepSeekVisionModel has built-in image support. Unknown models need
// capability metadata or an explicit declaration, not a name-based guess.
const OfficialDeepSeekVisionModel = "deepseek-v4-flash-vision-exp"

// IsOfficialDeepSeekVisionModel reports whether model is the pinned official
// DeepSeek vision SKU. Matching is case-insensitive and trims surrounding
// space. Model refs may carry a provider prefix ("deepseek/deepseek-v4-flash-
// vision-exp" from a multi-provider config), so only the segment after the
// last "/" — the actual model id — participates in the SKU check (#9671).
// Flash, Pro, and future names that merely contain "vision" still fail.
func IsOfficialDeepSeekVisionModel(model string) bool {
	model = strings.TrimSpace(model)
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	return strings.EqualFold(model, OfficialDeepSeekVisionModel)
}

// IsOfficialDeepSeekTextModel identifies known text-only models, not future SKUs.
func IsOfficialDeepSeekTextModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "deepseek-v4-flash", "deepseek-v4-pro":
		return true
	}
	return false
}

// DeepSeekImageInputAllowed applies the official endpoint hard limit after a
// provider has resolved its configured or catalog-derived image capability.
func DeepSeekImageInputAllowed(officialBase bool, requestURL, model string, metadataProvided, enabled bool) bool {
	if !officialBase && !IsDeepSeek(requestURL) {
		return enabled
	}
	if IsOfficialDeepSeekTextModel(model) {
		return false
	}
	return enabled || (!metadataProvided && IsOfficialDeepSeekVisionModel(model))
}

// OfficialDeepSeekAllowsVision reports whether this official DeepSeek endpoint
// may serialize image parts for the selected model. Custom gateways never match.
func OfficialDeepSeekAllowsVision(baseURL, model string) bool {
	return IsDeepSeek(baseURL) && IsOfficialDeepSeekVisionModel(model)
}

// IsOpenAI reports whether baseURL points at OpenAI's official API host. Keep
// this exact-host so a compatible gateway under another openai.com subdomain
// cannot accidentally receive the official max_completion_tokens wire shape.
func IsOpenAI(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), "api.openai.com")
}

// deepSeekPrefixChatURL returns the official Beta chat endpoint that enables
// assistant-prefix completion. Derive it only from a URL already hosted by
// DeepSeek: custom gateways may opt into the DeepSeek reasoning wire shape, but
// must never be bypassed by an automatic request to the vendor's direct API.
func deepSeekPrefixChatURL(chatURL string) string {
	if !IsDeepSeek(chatURL) {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(chatURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.Path = "/beta/chat/completions"
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// IsGeminiAPI reports whether baseURL points at Google's Gemini Developer API.
// Keep this exact-host: other googleapis.com services do not share Gemini's
// model resource-name compatibility quirk.
func IsGeminiAPI(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), "generativelanguage.googleapis.com")
}

// usesGeminiThoughtSignatures reports whether the current endpoint/model speaks
// Gemini's OpenAI-compatible thought-signature extension. The official endpoint
// is authoritative even when a custom model alias is used; compatible gateways
// are detected from the model ID they route (for example google/gemini-3-pro).
// Keeping this decision on the current client prevents a Gemini-authored history
// from leaking extra_content.google fields after a same-session provider switch.
func usesGeminiThoughtSignatures(baseURL, model string) bool {
	if IsGeminiAPI(baseURL) {
		return true
	}
	for _, segment := range strings.FieldsFunc(strings.ToLower(strings.TrimSpace(model)), func(r rune) bool {
		return r == '/' || r == ':'
	}) {
		if segment == "gemini" || strings.HasPrefix(segment, "gemini-") || strings.HasPrefix(segment, "gemini_") {
			return true
		}
	}
	return false
}

// normalizeModelID converts Gemini's resource-form model names returned by some
// /models responses into the bare IDs required by OpenAI-compatible chat calls.
// Other providers and already-normalized Gemini IDs pass through unchanged.
func normalizeModelID(baseURL, model string) string {
	model = strings.TrimSpace(model)
	if IsGeminiAPI(baseURL) {
		model = strings.TrimPrefix(model, "models/")
	}
	return model
}

// Explicit official beta alias verified against Chat Completions. Keep
// configuration identity exact; never case-fold arbitrary IDs or gateway calls.
func deepSeekChatWireModel(endpoint, model string) string {
	u, err := url.Parse(endpoint)
	if err == nil && u.Scheme == "https" && u.Host == "api.deepseek.com" &&
		u.User == nil && u.RawQuery == "" && u.Fragment == "" &&
		(u.Path == "/chat/completions" || u.Path == "/v1/chat/completions") &&
		model == "DeepSeek-V4.1-Flash-Expires-On-0910" {
		return "deepseek-v4.1-flash-expires-on-0910"
	}
	return model
}

// IsMiniMax reports whether baseURL points at MiniMax's OpenAI-compatible
// endpoint (api.minimaxi.com or any *.minimaxi.com subdomain).
//
// The host string is matched exactly — the spelling is `minimaxi`, not
// `minimax` — to avoid clashing with any future minimax-branded gateway.
func IsMiniMax(baseURL string) bool {
	return matchesVendorHost(baseURL, "minimaxi.com", "api.minimaxi.com")
}

// IsMiMo reports whether baseURL points at Xiaomi MiMo's OpenAI-compatible API.
// MiMo follows the OpenAI chat shape but authenticates with an `api-key` header
// instead of the usual Authorization bearer header.
func IsMiMo(baseURL string) bool {
	return provider.IsMiMoEndpoint(baseURL)
}

// IsZhipu reports whether baseURL points at Zhipu's OpenAI-compatible endpoint
// for GLM models — either the China host (open.bigmodel.cn, *.bigmodel.cn) or
// the international Z.ai host (api.z.ai, *.z.ai). Both speak the same wire shape,
// where chain-of-thought is gated by `thinking.type` (enabled|disabled) and
// `reasoning_effort` is silently ignored, so the client routes reasoning control
// to the thinking knob for either host.
func IsZhipu(baseURL string) bool {
	return matchesVendorHost(baseURL, "bigmodel.cn", "open.bigmodel.cn") ||
		matchesVendorHost(baseURL, "z.ai", "api.z.ai")
}

// IsTokenRhythm reports whether baseURL points at Token Rhythm's official
// OpenAI-compatible gateway. Keep this exact-host: model-aware protocol
// upgrades must not affect unrelated subdomains or similarly named relays.
func IsTokenRhythm(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), "tokenrhythm.studio")
}

// IsLongCat reports whether baseURL points at LongCat's OpenAI-compatible API.
// LongCat uses the OpenAI chat shape, but gates thinking with thinking.type
// enabled|disabled rather than the generic reasoning_effort field.
func IsLongCat(baseURL string) bool {
	return matchesVendorHost(baseURL, "longcat.chat", "api.longcat.chat")
}

// IsKimiAPI reports whether baseURL is one of Moonshot's official Kimi direct
// API endpoints. Gate Kimi-specific wire compatibility on the exact API hosts
// so OpenAI-compatible relays carrying the same model ID remain untouched.
func IsKimiAPI(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "api.moonshot.cn", "api.moonshot.ai":
		return true
	default:
		return false
	}
}

// IsOllamaCloud reports whether baseURL points at Ollama Cloud's hosted
// OpenAI-compatible endpoint. Local Ollama servers intentionally do not match:
// the hosted API accepts the reasoning_effort=max extension, while localhost
// deployments vary by model/version.
func IsOllamaCloud(baseURL string) bool {
	return matchesVendorHost(baseURL, "ollama.com", "ollama.com")
}
