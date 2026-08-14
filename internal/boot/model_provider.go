package boot

import (
	"net/http"
	"reasonix/internal/config"
	"reasonix/internal/netclient"
	"reasonix/internal/provider"
)

// NewProvider builds a provider.Provider from a configured entry. Exported so
// custom assemblers (e.g. the ACP per-session factory) can reuse it without
// going through the full Build.
func NewProvider(e *config.ProviderEntry) (provider.Provider, error) {
	return NewProviderWithProxy(e, netclient.ProxySpec{Mode: netclient.ModeAuto})
}

// NewProviderWithProxy builds a provider.Provider with the configured ordinary
// network proxy settings.
func NewProviderWithProxy(e *config.ProviderEntry, proxy netclient.ProxySpec) (provider.Provider, error) {
	return NewProviderWithProxyAndModelInfo(e, proxy, nil)
}

// NewProviderWithProxyAndModelInfo builds a provider while preserving the
// adapter-resolved metadata for the exact model instance.
func NewProviderWithProxyAndModelInfo(e *config.ProviderEntry, proxy netclient.ProxySpec, modelInfo *provider.ModelInfo) (provider.Provider, error) {
	return newProviderWithSearchMode(e, proxy, modelInfo, true)
}

// clientSearch suppresses new native searches while retaining the adapter's
// ability to read and replay existing native search history.
func newProviderWithSearchMode(e *config.ProviderEntry, proxy netclient.ProxySpec, modelInfo *provider.ModelInfo, clientSearch bool) (provider.Provider, error) {
	if err := config.ValidateProviderEndpoint(e); err != nil {
		return nil, err
	}
	reasoning := config.ReasoningCapabilityForEntry(e)
	if err := reasoning.Validate(e.Model, config.EffectiveEffort(e)); err != nil {
		return nil, err
	}
	if modelInfo == nil {
		resolved := config.NewModelCapabilityResolver().Resolve(e)
		modelInfo = &resolved.ModelInfo
	}
	var tunnelClient *http.Client
	if endpoint := e.CredentialProxyURL(); endpoint != "" {
		var err error
		tunnelClient, err = netclient.NewModelCredentialProxyClient(endpoint, e.APIKey())
		if err != nil {
			return nil, err
		}
	}
	return provider.New(e.Kind, provider.Config{
		HTTPClient: tunnelClient,
		Name:       e.Name, DisplayName: e.DisplayName, Protocol: e.Kind,
		BaseURL: e.BaseURL, Model: e.Model, APIKey: e.APIKey(), ModelInfo: modelInfo,
		// Pass the key's env var so auth failures can name where to fix it, plus
		// provider-kind-specific knobs. EffectiveEffort applies a configured
		// default_effort when the user has not explicitly selected /effort.
		Extra: map[string]any{
			"api_key_env":        e.APIKeyEnv,
			"api_key_source":     e.APIKeySourceLabel(),
			"user_id":            e.CacheContextValue(),
			"thinking":           e.Thinking,
			"effort":             config.EffectiveEffort(e),
			"supported_efforts":  e.SupportedEfforts,
			"default_effort":     reasoning.Default,
			"reasoning_protocol": config.ReasoningProtocolForEntry(e),
			"max_output_tokens":  e.MaxOutputTokens,
			"chat_url":           e.ChatURL,
			"request_url":        e.RequestURL,
			"headers":            e.Headers,
			"extra_body":         e.ExtraBody,
			"auth_header":        e.AuthHeader,
			"proxy_spec":         proxy,
			"vision":             config.EffectiveVision(e),
			"vision_detail":      e.VisionDetail,
			"web_search":         config.EffectiveWebSearch(e),
			"client_web_search":  clientSearch,
			"reject_redirects":   !clientSearch,
			"mode":               e.ResponsesMode,
			// Keep nil as nil so the responses provider can vendor-detect its
			// default instead of accidentally treating every endpoint as stateful.
			"stateful": e.ResponsesStateful,
		},
	})
}
