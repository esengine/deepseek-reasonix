package openai

import "testing"

// TestIsDeepSeek pins the host-matching rule for DeepSeek: the canonical
// api.deepseek.com, any *.deepseek.com subdomain, but NOT the apex
// deepseek.com (a misconfiguration we explicitly reject).
func TestIsDeepSeek(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		want    bool
	}{
		// Canonical
		{"https://api.deepseek.com", true},
		{"https://api.deepseek.com/v1", true},
		{"https://api.deepseek.com/anthropic", true},
		// Regional subdomains under the apex
		{"https://eu.deepseek.com/v1", true},
		{"https://us.deepseek.com/v1", true},
		// Apex rejected (would require a user pointing their base_url at
		// the apex domain, which is a misconfiguration)
		{"https://deepseek.com/v1", false},
		{"https://deepseek.com", false},
		// Other vendors must not match
		{"https://api.minimaxi.com/v1", false},
		{"https://api.openai.com/v1", false},
		// Wrong-spelling TLDs (e.g. "deepseek.io") must not match
		{"https://api.deepseek.io", false},
		{"https://deepseek.io", false},
		// Garbage
		{"", false},
		{"not-a-url", false},
	} {
		if got := IsDeepSeek(tc.baseURL); got != tc.want {
			t.Errorf("IsDeepSeek(%q) = %v, want %v", tc.baseURL, got, tc.want)
		}
	}
}

func TestIsOfficialDeepSeekVisionModel(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  bool
	}{
		{OfficialDeepSeekVisionModel, true},
		{"DEEPSEEK-V4-FLASH-VISION-EXP", true},
		{" deepseek-v4-flash-vision-exp ", true},
		// Provider-prefixed model refs (#9671): the segment after the last "/"
		// is the model id, so a prefixed ref of the vision SKU matches.
		{"deepseek/deepseek-v4-flash-vision-exp", true},
		{"DEEPSEEK/Deepseek-V4-Flash-Vision-Exp", true},
		{" deepseek/deepseek-v4-flash-vision-exp ", true},
		// Prefixed non-SKU names must still fail.
		{"deepseek/deepseek-v4-flash", false},
		{"gateway/deepseek-v4-flash-vision-exp/extra", false},
		{"deepseek-v4-flash", false},
		{"deepseek-v4-pro", false},
		{"deepseek-v5-vision", false},
		{"", false},
	} {
		if got := IsOfficialDeepSeekVisionModel(tc.model); got != tc.want {
			t.Errorf("IsOfficialDeepSeekVisionModel(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestOfficialDeepSeekAllowsVision(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		model   string
		want    bool
	}{
		{"https://api.deepseek.com", OfficialDeepSeekVisionModel, true},
		{"https://api.deepseek.com/anthropic", OfficialDeepSeekVisionModel, true},
		{"https://eu.deepseek.com/v1", OfficialDeepSeekVisionModel, true},
		{"https://api.deepseek.com", "deepseek-v4-flash", false},
		{"https://api.deepseek.com", "deepseek-v4-pro", false},
		{"https://api.deepseek.com", "deepseek-v5-vision", false},
		{"https://gateway.example/v1", OfficialDeepSeekVisionModel, false},
	} {
		if got := OfficialDeepSeekAllowsVision(tc.baseURL, tc.model); got != tc.want {
			t.Errorf("OfficialDeepSeekAllowsVision(%q, %q) = %v, want %v", tc.baseURL, tc.model, got, tc.want)
		}
	}
}

func TestIsOpenAI(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		want    bool
	}{
		{"https://api.openai.com/v1", true},
		{"https://API.OPENAI.COM/v1", true},
		{"https://platform.openai.com/v1", false},
		{"https://gateway.example/v1", false},
		{"not-a-url", false},
	} {
		if got := IsOpenAI(tc.baseURL); got != tc.want {
			t.Errorf("IsOpenAI(%q) = %v, want %v", tc.baseURL, got, tc.want)
		}
	}
}

func TestDeepSeekPrefixChatURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "canonical", in: "https://api.deepseek.com/chat/completions", want: "https://api.deepseek.com/beta/chat/completions"},
		{name: "v1 and query", in: "https://api.deepseek.com/v1/chat/completions?trace=1", want: "https://api.deepseek.com/beta/chat/completions"},
		{name: "regional port", in: "https://sg.deepseek.com:8443/v1/chat/completions", want: "https://sg.deepseek.com:8443/beta/chat/completions"},
		{name: "custom gateway", in: "https://gateway.example/v1/chat/completions", want: ""},
		{name: "apex", in: "https://deepseek.com/chat/completions", want: ""},
		{name: "garbage", in: "not-a-url", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := deepSeekPrefixChatURL(tc.in); got != tc.want {
				t.Fatalf("deepSeekPrefixChatURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestGeminiAPIModelNormalization(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		model   string
		want    string
	}{
		{"https://generativelanguage.googleapis.com/v1beta/openai/", "models/gemini-3.6-flash", "gemini-3.6-flash"},
		{"https://generativelanguage.googleapis.com/v1beta/openai", "gemini-3.6-flash", "gemini-3.6-flash"},
		{"https://api.example.com/v1", "models/gemini-3.6-flash", "models/gemini-3.6-flash"},
	} {
		if got := normalizeModelID(tc.baseURL, tc.model); got != tc.want {
			t.Errorf("normalizeModelID(%q, %q) = %q, want %q", tc.baseURL, tc.model, got, tc.want)
		}
	}
}

func TestUsesGeminiThoughtSignatures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseURL string
		model   string
		want    bool
	}{
		{"official endpoint", "https://generativelanguage.googleapis.com/v1beta/openai", "custom-alias", true},
		{"compatible gateway", "https://openrouter.ai/api/v1", "google/gemini-3.1-pro", true},
		{"different provider", "https://api.deepseek.com/v1", "deepseek-chat", false},
		{"incidental model text", "https://api.example.com/v1", "not-gemini-compatible", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := usesGeminiThoughtSignatures(tc.baseURL, tc.model); got != tc.want {
				t.Fatalf("usesGeminiThoughtSignatures(%q, %q) = %v, want %v", tc.baseURL, tc.model, got, tc.want)
			}
		})
	}
}

// TestIsMiniMax pins the host-matching rule for MiniMax. The spelling is
// `minimaxi`, not `minimax` — the latter is reserved for any future
// minimax-branded gateway so the two never collide.
func TestIsMiniMax(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		want    bool
	}{
		// Canonical
		{"https://api.minimaxi.com", true},
		{"https://api.minimaxi.com/v1", true},
		{"https://api.minimaxi.com/anthropic", true},
		// Regional subdomains under the apex
		{"https://eu.minimaxi.com/v1", true},
		{"https://us.minimaxi.com/v1", true},
		// Apex rejected
		{"https://minimaxi.com/v1", false},
		{"https://minimaxi.com", false},
		// Other vendors must not match
		{"https://api.deepseek.com", false},
		{"https://api.openai.com/v1", false},
		// Wrong spelling — minimax, not minimaxi — must not match
		{"https://api.minimax.com/v1", false},
		{"https://api.minimax.example.com", false},
		// Garbage
		{"", false},
		{"not-a-url", false},
	} {
		if got := IsMiniMax(tc.baseURL); got != tc.want {
			t.Errorf("IsMiniMax(%q) = %v, want %v", tc.baseURL, got, tc.want)
		}
	}
}

func TestIsMiMo(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		want    bool
	}{
		{"https://api.xiaomimimo.com/v1", true},
		{"https://token-plan-cn.xiaomimimo.com/v1", true},
		{"https://token-plan-sgp.xiaomimimo.com/v1", true},
		{"https://token-plan-ams.xiaomimimo.com/v1", true},
		{"https://xiaomimimo.com/v1", false},
		{"https://api.deepseek.com", false},
		{"", false},
		{"not-a-url", false},
	} {
		if got := IsMiMo(tc.baseURL); got != tc.want {
			t.Errorf("IsMiMo(%q) = %v, want %v", tc.baseURL, got, tc.want)
		}
	}
}

// TestIsZhipu pins the host-matching rule for Zhipu GLM across both the China
// (bigmodel.cn) and international (z.ai) endpoints.
func TestIsZhipu(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		want    bool
	}{
		// Canonical China endpoint
		{"https://open.bigmodel.cn/api/paas/v4", true},
		{"https://open.bigmodel.cn", true},
		// Subdomains under the China apex
		{"https://api.bigmodel.cn/v1", true},
		// Canonical international endpoint
		{"https://api.z.ai/api/paas/v4", true},
		{"https://api.z.ai", true},
		// Subdomain under the z.ai apex
		{"https://open.z.ai/v1", true},
		// Bare apexes rejected (misconfiguration)
		{"https://bigmodel.cn/v1", false},
		{"https://z.ai", false},
		// Other vendors must not match
		{"https://api.deepseek.com", false},
		{"https://api.minimaxi.com/v1", false},
		{"https://api.openai.com/v1", false},
		// Garbage
		{"", false},
		{"not-a-url", false},
	} {
		if got := IsZhipu(tc.baseURL); got != tc.want {
			t.Errorf("IsZhipu(%q) = %v, want %v", tc.baseURL, got, tc.want)
		}
	}
}

func TestIsTokenRhythm(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		want    bool
	}{
		{"https://tokenrhythm.studio/v1", true},
		{"https://TOKENRHYTHM.STUDIO/v1", true},
		{"https://api.tokenrhythm.studio/v1", false},
		{"https://tokenrhythm.example.com/v1", false},
		{"https://opencode.ai/zen/go/v1", false},
		{"", false},
	} {
		if got := IsTokenRhythm(tc.baseURL); got != tc.want {
			t.Errorf("IsTokenRhythm(%q) = %v, want %v", tc.baseURL, got, tc.want)
		}
	}
}

func TestIsLongCat(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		want    bool
	}{
		{"https://api.longcat.chat/openai/v1", true},
		{"https://api.longcat.chat/anthropic", true},
		{"https://gateway.longcat.chat/openai/v1", true},
		{"https://longcat.chat/openai/v1", false},
		{"https://api.deepseek.com", false},
		{"", false},
		{"not-a-url", false},
	} {
		if got := IsLongCat(tc.baseURL); got != tc.want {
			t.Errorf("IsLongCat(%q) = %v, want %v", tc.baseURL, got, tc.want)
		}
	}
}

func TestIsKimiAPI(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		want    bool
	}{
		{"https://api.moonshot.cn/v1", true},
		{"https://api.moonshot.ai/v1", true},
		{"https://API.MOONSHOT.CN/v1/chat/completions", true},
		{"https://moonshot.cn/v1", false},
		{"https://gateway.moonshot.cn/v1", false},
		{"https://opencode.ai/zen/go/v1", false},
		{"", false},
		{"not-a-url", false},
	} {
		if got := IsKimiAPI(tc.baseURL); got != tc.want {
			t.Errorf("IsKimiAPI(%q) = %v, want %v", tc.baseURL, got, tc.want)
		}
	}
}

func TestIsOllamaCloud(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		want    bool
	}{
		{"https://ollama.com/v1", true},
		{"https://api.ollama.com/v1", true},
		{"https://localhost:11434/v1", false},
		{"http://127.0.0.1:11434/v1", false},
		{"https://api.openai.com/v1", false},
		{"", false},
		{"not-a-url", false},
	} {
		if got := IsOllamaCloud(tc.baseURL); got != tc.want {
			t.Errorf("IsOllamaCloud(%q) = %v, want %v", tc.baseURL, got, tc.want)
		}
	}
}
