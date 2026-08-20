package config

import (
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"reasonix/internal/fileutil"
	fileencoding "reasonix/internal/fileutil/encoding"
	"reasonix/internal/provider"
)

// Load builds the configuration: defaults, then user config, then project
// config, then MCP servers from Claude Code's .mcp.json, then (lowest priority)
// the v0.x ~/.reasonix/config.json's mcpServers. Provider api_key_env values
// resolve from Reasonix's global .env, not from project .env files.
func Load() (*Config, error) {
	return LoadForRoot(".")
}

// LoadForRoot builds the configuration with project files resolved from root
// instead of the current working directory. When root is "" or ".", it behaves
// like Load(). This is the workspace-aware entry point: desktop tabs use it so
// each project's reasonix.toml + .mcp.json are resolved independently without
// changing the process cwd, while provider keys stay rooted in Reasonix home.
//
// Note: LoadForRoot may rewrite legacy MCP `tier` lines on disk (see
// mergeRuntimeTOMLFileSnapshot). Callers that must not mutate config files should use
// LoadForRootReadOnly instead.
func LoadForRoot(root string) (*Config, error) {
	return loadForRoot(root, loadForRootOptions{migrateOnDisk: true, loadCredentials: true})
}

// LoadForRootReadOnly is like LoadForRoot but never writes config files: it skips
// on-disk legacy MCP tier migration. Prefer this for diagnostics, doctor, and
// other read-only inspection paths.
func LoadForRootReadOnly(root string) (*Config, error) {
	return loadForRoot(root, loadForRootOptions{loadCredentials: true})
}

// LoadForRootWithoutCredentialsReadOnly is the credential-free form of
// LoadForRootReadOnly. It still merges the effective user + project config and
// carries project .env values for workspace-scoped expansion, but it neither
// pins Reasonix credentials into the process environment nor resolves provider
// API keys. Settings probes use it when they need runtime network policy before
// resolving only the edited provider's credential explicitly.
func LoadForRootWithoutCredentialsReadOnly(root string) (*Config, error) {
	return loadForRoot(root, loadForRootOptions{})
}

// LoadUserConfigReadOnly loads only the trusted user-global config. It never
// reads project reasonix.toml files and never performs on-disk migrations.
// Host-owned features that may execute a configured binary should use this
// instead of LoadForRoot so an untrusted checkout cannot choose the process.
func LoadUserConfigReadOnly() (*Config, error) {
	cfg := Default()
	if path := userConfigLoadPath(); path != "" {
		meta, err := mergeFileSnapshot(cfg, path)
		if err != nil {
			return nil, err
		}
		if meta.IsDefined("agent", "system_prompt_file") {
			cfg.systemPromptFileSource = promptFileSourceUser
		}
	}
	normalizeConfigForEdit(cfg)
	cfg.loadOpenCodeGoJournal(userConfigLoadPath())
	return cfg, nil
}

type loadForRootOptions struct {
	migrateOnDisk   bool
	loadCredentials bool
}

func loadForRoot(root string, opts loadForRootOptions) (*Config, error) {
	root = resolveRoot(root)
	expansionEnv := loadProjectDotEnvForExpansion(root)
	if opts.loadCredentials {
		loadCredentialStoreForRoot(root)
	}
	cfg := Default()
	cfg.setExpansionEnv(expansionEnv)
	cfg.CredentialsStore = credentialsStoreMode()

	projectTOML := ProjectConfigPath(root)
	if bothProjectConfigsExist(root) {
		slog.Warn("project config: reasonix.toml and .reasonix/config.toml both exist; reasonix.toml wins", "root", root)
	}
	if primary := userConfigPath(); primary != "" {
		if _, err := resolveConfigAccessPath(primary, true); err != nil {
			return nil, err
		}
	}
	if _, err := resolveConfigAccessPath(projectTOML, false); err != nil {
		return nil, err
	}

	mergeTOML := mergeFileSnapshot
	if opts.migrateOnDisk {
		mergeTOML = mergeRuntimeTOMLFileSnapshot
	}

	var tomlSources []string
	userDefaultModelExplicit := false
	if uc := userConfigLoadPath(); uc != "" {
		tomlSources = append(tomlSources, uc)
		meta, err := mergeTOML(cfg, uc)
		if err != nil {
			// Never rewrite the broken original file. Prefer the last verified
			// snapshot in memory, then built-in defaults, and keep loading so
			// the rest of the app stays usable.
			lkgCfg := Default()
			lkgCfg.setExpansionEnv(expansionEnv)
			lkgCfg.CredentialsStore = credentialsStoreMode()
			if lkgErr := loadLastKnownGoodUserConfig(lkgCfg); lkgErr == nil {
				*cfg = *lkgCfg
				cfg.addLoadWarning(fmt.Sprintf(
					"user config %s is invalid (%v); using last-known-good snapshot in memory without modifying the original file",
					uc, err,
				))
			} else {
				cfg.addLoadWarning(fmt.Sprintf(
					"user config %s is invalid (%v); using built-in defaults in memory without modifying the original file",
					uc, err,
				))
			}
		} else {
			userDefaultModelExplicit = meta.IsDefined("default_model")
			if meta.IsDefined("agent", "system_prompt_file") {
				cfg.systemPromptFileSource = promptFileSourceUser
			}
		}
	}
	// A last-known-good recovery is still trusted user configuration even though
	// the broken source file cannot provide usable TOML metadata.
	if cfg.systemPromptFileSource == promptFileSourceUnknown && cfg.Agent.SystemPromptFile != "" {
		cfg.systemPromptFileSource = promptFileSourceUser
	}
	userDefaultModel := cfg.DefaultModel
	globalCLI := cfg.CLI
	globalSecrets := cfg.Secrets
	globalRemote := cfg.Remote.Clone()
	globalDesktopLanguage := cfg.Desktop.Language
	globalPricingCurrency := cfg.Desktop.Currency
	globalBillingDisplayCurrency := cfg.Billing.DisplayCurrency
	globalTelemetry, globalLegacyAnchorSafetyGate := cfg.Telemetry, cfg.Agent.LegacyAnchorSafetyGate

	tomlSources = append(tomlSources, projectTOML)
	projectMeta, err := mergeTOML(cfg, projectTOML)
	if err != nil {
		// Project config damage is isolated to this workspace: continue with
		// user/global config so other tabs stay available.
		cfg.addLoadWarning(fmt.Sprintf(
			"project config %s is invalid (%v); ignored for this workspace",
			projectTOML, err,
		))
		// Drop the project path from later multi-file merges so a broken TOML
		// cannot fail plugin/provider re-merges.
		tomlSources = tomlSources[:len(tomlSources)-1]
	} else if projectMeta.IsDefined("agent", "system_prompt_file") {
		cfg.systemPromptFileSource = promptFileSourceProject
	}
	// The native CLI update channel controls the one user-installed binary.
	// A repository-local reasonix.toml must never switch that global choice.
	cfg.CLI = globalCLI
	// Secret protection is a user-global security control: a cloned repo's
	// reasonix.toml must not be able to flip on the workflow-breaking env/path
	// protections.
	cfg.Secrets = globalSecrets
	// Remote SSH hosts are equally user-global: a cloned repo's reasonix.toml
	// must not be able to inject hosts, jump chains, or port forwards that
	// steer where Reasonix opens connections.
	cfg.Remote = globalRemote
	// Desktop language and pricing currency are user-level regional preferences.
	// A repository must not be able to alter how the user's spend is shown.
	cfg.Desktop.Language = globalDesktopLanguage
	cfg.Desktop.Currency = globalPricingCurrency
	cfg.Billing.DisplayCurrency = globalBillingDisplayCurrency
	// CLI telemetry is an explicit user-global privacy choice. Project config
	// cannot opt a user in or out, including when the global value is absent.
	cfg.Telemetry, cfg.Agent.LegacyAnchorSafetyGate = globalTelemetry, globalLegacyAnchorSafetyGate
	// TOML decoding replaces [[plugins]] wholesale, so cfg.Plugins now holds
	// only the last file's. Re-merge by name across all sources (later wins) so a
	// project reasonix.toml doesn't drop the global config's MCP servers.
	// mergeTOMLPlugins only reads files; it does not run on-disk migrations.
	plugins, err := mergeTOMLPlugins(tomlSources)
	if err != nil {
		cfg.addLoadWarning(fmt.Sprintf("plugin configuration could not be merged (%v); continuing without those entries", err))
	} else {
		cfg.Plugins = plugins
	}
	if providers, providerSources, shadowedProjectProviders, ok, err := mergeTOMLProviders(tomlSources); err != nil {
		cfg.addLoadWarning(fmt.Sprintf("provider configuration could not be merged (%v); keeping providers already loaded", err))
	} else if ok {
		cfg.Providers = providers
		cfg.providerSources = providerSources
		cfg.shadowedProjectProviders = shadowedProjectProviders
	}
	if access, ok, err := mergeTOMLProviderAccess(tomlSources); err != nil {
		cfg.addLoadWarning(fmt.Sprintf("provider access configuration could not be merged (%v)", err))
	} else if ok {
		cfg.Desktop.ProviderAccess = access
	}

	// Claude Code's .mcp.json (project root) is read last and merged into
	// [[plugins]], so a server configured for Claude works here unchanged.
	// Project reasonix.toml wins on a name collision; project .mcp.json wins
	// over a same-name user-global entry (see mergeMCPJSON).
	mcpFile := mcpJSONFile
	if root != "." {
		mcpFile = filepath.Join(root, mcpJSONFile)
	}
	entries, err := loadMCPJSON(mcpFile)
	if err != nil {
		cfg.addLoadWarning(fmt.Sprintf("project .mcp.json is invalid (%v); MCP servers from that file are ignored", err))
	} else {
		cfg.mergeMCPJSON(entries)
	}

	// Lowest priority before the one-time v1.9.1 MCP migration: the v0.x
	// ~/.reasonix/config.json's mcpServers. Once the migration marker exists, the
	// current config is authoritative even when it is empty; reading the legacy
	// source again would resurrect servers the user removed from current config.
	if !mcpGlobalMigrationComplete() {
		cfg.mergeMCPJSON(loadLegacyMCP(legacyConfigPath()))
	}
	_ = mergeInstalledPluginPackages(cfg, root)
	if err := normalizeRuntimeConfigWithMigrationJournal(cfg); err != nil {
		return nil, err
	}
	if userDefaultModelExplicit {
		restoreUnresolvableProjectDefaultModel(cfg, userDefaultModel)
	}
	cfg.CredentialsStore = credentialsStoreMode()
	cfg.setExpansionEnv(expansionEnv)
	if opts.loadCredentials {
		resolveProviderCredentialsForRoot(root, cfg)
	}
	return cfg, nil
}

// LoadBuiltinDefaultsForRoot returns a read-only built-in-only configuration
// without reading or migrating user/project TOML. Diagnostic and recovery tools
// use it when configuration is malformed; it does not put the process into any
// degraded product "mode". Provider credentials still resolve only from
// Reasonix's global credential store.
func LoadBuiltinDefaultsForRoot(root string) *Config {
	cfg := Default()
	cfg.Plugins = nil
	cfg.Skills = SkillsConfig{}
	cfg.Bot.Enabled = false
	cfg.Bot.Connections = nil
	cfg.Bot.Routes = nil
	cfg.Statusline.Command = ""
	cfg.LSP.Enabled = false
	cfg.setExpansionEnv(nil)
	cfg.CredentialsStore = credentialsStoreMode()
	resolveProviderCredentialsForRoot(root, cfg)
	return cfg
}

// LoadRecoveryDefaultsForRoot is retained as an alias of LoadBuiltinDefaultsForRoot
// for older recovery call sites.
func LoadRecoveryDefaultsForRoot(root string) *Config {
	return LoadBuiltinDefaultsForRoot(root)
}

func (c *Config) setExpansionEnv(env map[string]string) {
	if c == nil {
		return
	}
	c.expansionEnv = cloneStringMap(env)
	for i := range c.Plugins {
		c.Plugins[i].expansionEnv = c.expansionEnv
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

// restoreUnresolvableProjectDefaultModel falls back to the user/global
// default_model when a project reasonix.toml overrides it with a reference no
// configured provider serves (#4218). Pre-v1.11 persistence paths (e.g. the
// "always allow" writer) full-rendered ./reasonix.toml and pinned the built-in
// default_model ("deepseek-flash") into it; once the user's [[providers]]
// replaced the built-in presets, that stale name resolved to nothing and boot
// hard-failed in every launch from that folder. In-memory only — the project
// file is untouched, and a project override that does resolve still wins. The
// ignored value is kept so boot can surface a notice.
//
// Callers must only invoke this when the user config explicitly defines
// default_model: falling back to the built-in default would silently mask a
// broken ref when the project file is the user's only config, and that case
// must keep the actionable boot error (TestBuildUnknownModelErrorIsActionable).
func restoreUnresolvableProjectDefaultModel(c *Config, userDefault string) {
	if c == nil {
		return
	}
	if c.DefaultModel == userDefault {
		return
	}
	if _, ok := c.ResolveModel(c.DefaultModel); ok {
		return
	}
	if _, ok := c.ResolveModel(userDefault); !ok {
		return
	}
	c.ignoredProjectDefaultModel = c.DefaultModel
	c.DefaultModel = userDefault
}

// tomlFileDefinesKey reports whether the TOML file at path explicitly defines
// the given top-level key. Missing or unparseable files report false.
func tomlFileDefinesKey(path string, key ...string) bool {
	var f Config
	meta, err := decodeTOMLFile(path, &f)
	if err != nil {
		return false
	}
	return meta.IsDefined(key...)
}

// backfillDeepSeekPro restores deepseek-pro for configs the pre-fix setup wizard
// wrote with only deepseek-v4-flash: a keyless /models probe used to drop the Pro
// SKU, leaving users unable to switch to it. In-memory only — the user's file is
// untouched. Narrowly scoped to the official DeepSeek endpoint (which is known to
// serve pro) so a custom flash-only deployment isn't given an entry that 404s.
func backfillDeepSeekPro(c *Config) {
	const flashModel, proModel = "deepseek-v4-flash", "deepseek-v4-pro"
	var flash *ProviderEntry
	for i := range c.Providers {
		p := &c.Providers[i]
		if p.Name == "deepseek-pro" {
			return
		}
		for _, m := range p.ModelList() {
			switch m {
			case proModel:
				return // pro already reachable
			case flashModel:
				if strings.Contains(p.BaseURL, "api.deepseek.com") {
					flash = p
				}
			}
		}
	}
	if flash == nil {
		return
	}
	// If the user has explicitly curated a model list for the flash provider
	// (e.g. unchecked pro in Settings), respect that choice and do not backfill.
	if len(flash.Models) > 0 {
		return
	}
	for _, bp := range Default().Providers {
		if bp.Name == "deepseek-pro" {
			bp.APIKeyEnv = flash.APIKeyEnv
			// Inherit the flash provider's frozen billing currency for list prices.
			currency := flash.ProviderBillingCurrency()
			if currency == "" {
				currency = flash.persistedOfficialCurrency
			}
			if currency == "" {
				currency = "USD"
			}
			bp.BillingCurrency = currency
			bp.persistedOfficialCurrency = currency
			bp.Price = deepSeekV4PriceForModel(currency, proModel)
			c.Providers = append(c.Providers, bp)
			return
		}
	}
}

func backfillDeepSeekOfficialPrices(c *Config) {
	if c == nil {
		return
	}
	for i := range c.Providers {
		p := &c.Providers[i]
		if officialProviderKind(p) != "deepseek" {
			continue
		}
		backfillDeepSeekOfficialEndpointDefaults(p)
		currency := p.ProviderBillingCurrency()
		if currency == "" {
			currency = p.persistedOfficialCurrency
		}
		if currency == "" {
			currency = "USD"
		}
		defaults := DeepSeekV4PricesForCurrency(currency)
		if p.Price != nil {
			continue
		}
		if p.Prices == nil {
			p.Prices = map[string]*provider.Pricing{}
		}
		for model, price := range defaults {
			if p.HasModel(model) && p.Prices[model] == nil {
				p.Prices[model] = clonePricing(price)
			}
		}
	}
}

// backfillDeepSeekOfficialEndpointDefaults restores the two official-endpoint
// fields a config may legitimately omit. Both are safe to infer here precisely
// because the caller already matched api.deepseek.com: the wallet endpoint is
// the vendor's own, and 1M is that vendor's real window. Values the file
// declares are never overwritten.
//
// This is keyed on the endpoint rather than on list position, so it cannot leak
// onto a custom provider the way the previous positional decode overlay did
// (#7357, #7358).
func backfillDeepSeekOfficialEndpointDefaults(p *ProviderEntry) {
	if p == nil {
		return
	}
	if strings.TrimSpace(p.BalanceURL) == "" {
		p.BalanceURL = "https://api.deepseek.com/user/balance"
	}
	backfillOfficialContextWindow(p, 1_000_000)
}

func officialProviderKind(p *ProviderEntry) string {
	if p == nil {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(p.BaseURL))
	if err != nil {
		return ""
	}
	if strings.EqualFold(u.Hostname(), "api.deepseek.com") {
		return "deepseek"
	}
	return ""
}

func resolveRoot(root string) string {
	if root == "" || root == "." {
		return "."
	}
	return filepath.Clean(root)
}

// normalizeLegacyEffort migrates the retired DeepSeek effort="off" (the old
// /thinking off that disabled thinking) to the provider default, so a config
// written by an older version keeps loading instead of erroring on a value the
// provider no longer accepts.
func normalizeLegacyEffort(c *Config) {
	for i := range c.Providers {
		if strings.EqualFold(strings.TrimSpace(c.Providers[i].Effort), "off") {
			c.Providers[i].Effort = ""
		}
	}
}

// mergeTOMLPlugins merges [[plugins]] across TOML sources by name (later source wins).
func mergeTOMLPlugins(paths []string) ([]PluginEntry, error) {
	var merged []PluginEntry
	index := map[string]int{}
	for _, path := range paths {
		_, exists, err := statConfigPath(path)
		if err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
		if !exists {
			continue
		}
		var f Config
		if _, err := decodeTOMLFile(path, &f); err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
		for _, p := range f.Plugins {
			p, _ = NormalizePluginCommandLine(p)
			if isUserConfigPath(path) {
				p.Source = MCPSourceUserConfig
			} else {
				p.Source = MCPSourceProjectConfig
			}
			if i, ok := index[p.Name]; ok {
				merged[i] = p
				continue
			}
			index[p.Name] = len(merged)
			merged = append(merged, p)
		}
	}
	return merged, nil
}

// mergeTOMLProviders merges [[providers]] across TOML sources by provider name.
// User-global providers win over same-named project providers; project providers
// only fill names the global config does not define. Keep official legacy aliases
// distinct here: they can carry different default models and effort capabilities,
// and the later desktop normalization layer handles canonical Settings access.
func mergeTOMLProviders(paths []string) ([]ProviderEntry, map[string]providerSourceScope, []ProviderEntry, bool, error) {
	var merged []ProviderEntry
	var shadowedProject []ProviderEntry
	index := map[string]int{}
	sources := map[string]providerSourceScope{}
	saw := false
	for _, path := range paths {
		_, exists, err := statConfigPath(path)
		if err != nil {
			return nil, nil, nil, false, fmt.Errorf("config %s: %w", path, err)
		}
		if !exists {
			continue
		}
		var f Config
		if _, err := decodeTOMLFile(path, &f); err != nil {
			return nil, nil, nil, false, fmt.Errorf("config %s: %w", path, err)
		}
		markPersistedDeepSeekOfficialPricing(&f)
		if len(f.Providers) == 0 {
			continue
		}
		saw = true
		source := providerSourceForPath(path)
		for _, p := range f.Providers {
			normalizeProviderEffortFields(&p)
			key := providerMergeKey(p)
			if i, ok := index[key]; ok {
				if sources[key] == providerSourceProject && source == providerSourceUser {
					shadowedProject = append(shadowedProject, merged[i])
					merged[i] = p
					sources[key] = source
				} else if sources[key] == providerSourceUser && source == providerSourceProject {
					shadowedProject = append(shadowedProject, p)
				}
				continue
			} else {
				index[key] = len(merged)
				merged = append(merged, p)
				sources[key] = source
			}
		}
	}
	return merged, sources, shadowedProject, saw, nil
}

func providerSourceForPath(path string) providerSourceScope {
	if isUserConfigPath(path) {
		return providerSourceUser
	}
	return providerSourceProject
}

func providerMergeKey(p ProviderEntry) string {
	return strings.TrimSpace(p.Name)
}

// mergeTOMLProviderAccess merges desktop.provider_access across TOML sources so
// project desktop settings do not hide account-level providers from the desktop
// model switcher.
func mergeTOMLProviderAccess(paths []string) ([]string, bool, error) {
	var merged []string
	seen := map[string]bool{}
	saw := false
	userDeclared := false
	for _, path := range paths {
		_, exists, err := statConfigPath(path)
		if err != nil {
			return nil, false, fmt.Errorf("config %s: %w", path, err)
		}
		if !exists {
			continue
		}
		var f Config
		meta, err := decodeTOMLFile(path, &f)
		if err != nil {
			return nil, false, fmt.Errorf("config %s: %w", path, err)
		}
		if !meta.IsDefined("desktop", "provider_access") {
			continue
		}
		if !saw {
			// Preserve declaration state even when the list is explicitly empty.
			// A nil slice means legacy/undeclared access; a non-nil empty slice
			// means the user intentionally removed every desktop provider.
			merged = []string{}
		}
		saw = true
		if isUserConfigPath(path) {
			userDeclared = true
		}
		for _, name := range f.Desktop.ProviderAccess {
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			merged = append(merged, name)
		}
	}
	// An undeclared user list means "allow all"; a union with a project-only
	// list would silently narrow that to whatever the project happens to name.
	if saw && !userDeclared {
		return nil, false, nil
	}
	return merged, saw, nil
}

// ConfigFileDeclarations contains provider settings explicitly declared by one
// TOML file, without defaults or values inherited from another scope.
type ConfigFileDeclarations struct {
	ProviderNames                 []string
	DesktopProviderAccessDeclared bool
}

// InspectConfigFileDeclarations returns the provider-related fields explicitly
// present in one TOML file. It deliberately does not include built-in defaults
// or values inherited from another config scope.
func InspectConfigFileDeclarations(path string) (ConfigFileDeclarations, error) {
	var declarations ConfigFileDeclarations
	path = strings.TrimSpace(path)
	if path == "" {
		return declarations, nil
	}
	_, exists, err := statConfigPath(path)
	if err != nil {
		return declarations, err
	}
	if !exists {
		return declarations, nil
	}
	var f Config
	meta, err := decodeTOMLFile(path, &f)
	if err != nil {
		return declarations, fmt.Errorf("config %s: %w", path, err)
	}
	seen := make(map[string]bool, len(f.Providers))
	for _, provider := range f.Providers {
		name := strings.TrimSpace(provider.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		declarations.ProviderNames = append(declarations.ProviderNames, name)
	}
	declarations.DesktopProviderAccessDeclared = meta.IsDefined("desktop", "provider_access")
	return declarations, nil
}

// DesktopProviderAccessDeclared reports whether path explicitly declares
// desktop.provider_access. It distinguishes omission from an intentional [].
func DesktopProviderAccessDeclared(path string) (bool, error) {
	declarations, err := InspectConfigFileDeclarations(path)
	return declarations.DesktopProviderAccessDeclared, err
}

// LoadForEdit returns a config to seed the `reasonix setup` wizard when reconfiguring:
// the built-in defaults with the file at path (if present) decoded on top, so a
// reconfigure preserves the user's existing providers and agent settings instead
// of resetting to defaults. Reasonix's global .env is loaded so api_key_env
// resolution works while the wizard decides which keys are still missing.
func LoadForEdit(path string) *Config {
	return loadForEdit(path, true, false)
}

// LoadForEditReadOnlyStrict is the error-returning commit-time variant. It must
// not fall back to defaults when another writer leaves malformed TOML, because
// saving that fallback would overwrite the user's recoverable file.
func LoadForEditReadOnlyStrict(path string) (*Config, error) {
	return loadForEditStrict(path, true, false)
}

// LoadForEditWithoutCredentialsReadOnlyStrict is the credential-free strict
// edit loader. It never writes migrations and never substitutes defaults for a
// malformed file.
func LoadForEditWithoutCredentialsReadOnlyStrict(path string) (*Config, error) {
	return loadForEditStrict(path, false, false)
}

// ValidateFile parses one TOML config in isolation without loading credentials,
// applying migrations, or writing the file. A missing file is valid.
func ValidateFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	_, exists, err := statConfigPath(path)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	cfg := Default()
	if _, err := decodeTOMLFile(path, cfg); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	return nil
}

// ValidateBytes parses one in-memory TOML config without loading credentials,
// applying migrations, or writing any state.
func ValidateBytes(data []byte) error {
	cfg := Default()
	if _, err := decodeTOMLBytes(data, cfg); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

func loadForEdit(path string, loadCredentials, persistMigrations bool) *Config {
	cfg, err := loadForEditStrict(path, loadCredentials, persistMigrations)
	if err == nil {
		return cfg
	}
	slog.Warn("config: load for edit failed, using defaults", "path", path, "err", err)
	if loadCredentials {
		loadDotEnvForEditPath(path)
	}
	cfg = Default()
	normalizeConfigForEdit(cfg)
	cfg.editLoadErr = err
	return cfg
}

func LoadForEditWithoutCredentials(path string) *Config {
	return loadForEdit(path, false, false)
}

func loadForEditStrict(path string, loadCredentials, persistMigrations bool) (*Config, error) {
	if loadCredentials {
		loadDotEnvForEditPath(path)
	}
	cfg := Default()
	meta, err := mergeFileSnapshot(cfg, path)
	if err != nil {
		return nil, err
	}
	markExplicitDefaultProjectSkillKeys(cfg, path, meta)
	changed := normalizeConfigForEdit(cfg)
	cfg.loadOpenCodeGoJournal(path)
	if persistMigrations && changed && strings.TrimSpace(path) != "" {
		if _, err := os.Stat(path); err == nil {
			if err := cfg.SaveTo(path); err != nil {
				return nil, err
			}
		}
	}
	return cfg, nil
}

// markExplicitDefaultProjectSkillKeys preserves project skill fields that are
// explicitly present in a file but equal the built-in default. Without this
// transient provenance, saving an unrelated project setting would mistake an
// intentional `false`/empty override for a stale delta and remove it.
func markExplicitDefaultProjectSkillKeys(c *Config, path string, meta toml.MetaData) {
	if c == nil || isUserConfigPath(path) {
		return
	}
	for _, key := range projectSkillKeys {
		if !meta.IsDefined("skills", key) || !projectSkillKeyIsDefault(c, key) {
			continue
		}
		if c.explicitProjectSkillKeys == nil {
			c.explicitProjectSkillKeys = make(map[string]bool)
		}
		c.explicitProjectSkillKeys[key] = true
	}
}

func normalizeConfigForEdit(cfg *Config) bool {
	normalizePluginCommandLines(cfg)
	normalizeLegacyEffort(cfg)
	normalizeLegacyAgentStepLimits(cfg)
	changed := normalizeRetiredAutoPlan(cfg)
	changed = normalizeRetiredMultiThresholdCompaction(cfg) || changed
	normalizeLegacyMCPTiers(cfg)
	changed = normalizeLegacyStepFunBaseURLs(cfg) || changed
	changed = normalizeLegacyLongCatContextWindows(cfg) || changed
	changed = normalizeLegacyQwenContextWindows(cfg) || changed
	changed = normalizeLegacyKimiK3Catalog(cfg) || changed
	changed = normalizeLegacyOpenCodeGoInstalls(cfg) || changed
	changed = normalizeLegacyMimoCustomProviders(cfg) || changed
	normalizeLegacyProviderModels(cfg)
	normalizeDesktopOfficialProviderAccess(cfg)
	normalizeOfficialDeepSeekModels(cfg)
	migrateBillingDisplayCurrency(cfg)
	freezeProviderBillingCurrencies(cfg)
	applyDeepSeekOfficialDefaultPricing(cfg)
	backfillDeepSeekOfficialPrices(cfg)
	normalizeEffortConfig(cfg)
	return changed
}

// normalizeRetiredMultiThresholdCompaction clears retired multi-threshold keys
// so they never reach the Agent. Disk migration removes them on ordinary start;
// loading still ignores them if migration could not rewrite the file.
func normalizeRetiredMultiThresholdCompaction(c *Config) bool {
	if c == nil {
		return false
	}
	changed := c.Agent.SoftCompactRatio != 0 ||
		c.Agent.ToolResultSnipRatio != 0 ||
		c.Agent.CompactForceRatio != 0 ||
		c.Agent.ColdResumePrune != nil ||
		strings.TrimSpace(c.Agent.ContextEditing) != ""
	c.Agent.SoftCompactRatio = 0
	c.Agent.ToolResultSnipRatio = 0
	c.Agent.CompactForceRatio = 0
	c.Agent.ColdResumePrune = nil
	c.Agent.ContextEditing = ""
	if c.Agent.CompactRatio <= 0 {
		c.Agent.CompactRatio = Default().Agent.CompactRatio
		changed = true
	}
	return changed
}

// normalizeRetiredAutoPlan keeps pre-v5 configs readable while enforcing the
// single explicit-plan experience. The deprecated fields remain in AgentConfig
// only so old TOML and older desktop payloads decode safely.
func normalizeRetiredAutoPlan(c *Config) bool {
	if c == nil {
		return false
	}
	changed := strings.TrimSpace(c.Agent.AutoPlan) != "" && !strings.EqualFold(strings.TrimSpace(c.Agent.AutoPlan), "off") ||
		strings.TrimSpace(c.Agent.AutoPlanClassifier) != ""
	c.Agent.AutoPlan = "off"
	c.Agent.AutoPlanClassifier = ""
	return changed
}

func loadDotEnvForEditPath(path string) {
	path = strings.TrimSpace(path)
	if path == "" || isUserConfigPath(path) {
		loadDotEnv()
		return
	}
	loadDotEnvForRoot(filepath.Dir(path))
}

// mergeFile decodes a TOML file onto cfg if it exists. An absent file is not an error.
func mergeFile(cfg *Config, path string) error {
	_, err := mergeFileSnapshot(cfg, path)
	return err
}

// mergeFileSnapshot decodes one immutable read of a TOML file onto cfg and
// returns metadata from those exact bytes. Callers that derive source or
// precedence decisions from metadata must use this result instead of reading
// the path again: a config file may be atomically replaced between reads.
func mergeFileSnapshot(cfg *Config, path string) (toml.MetaData, error) {
	return mergeFileSnapshotWithRead(cfg, path, fileencoding.ReadFileUTF8)
}

func mergeFileSnapshotWithRead(cfg *Config, path string, readFile func(string) ([]byte, error)) (toml.MetaData, error) {
	resolved, exists, err := statConfigPath(path)
	if err != nil {
		return toml.MetaData{}, err
	}
	if !exists {
		return toml.MetaData{}, nil
	}
	data, err := readFile(resolved)
	if err != nil {
		return toml.MetaData{}, fmt.Errorf("config %s: %w", path, err)
	}
	// BurntSushi/toml decodes struct fields incrementally and can leave earlier
	// fields mutated when a later value has the wrong type. Validate the complete
	// snapshot against a disposable Config before merging those same bytes into
	// the active object. This makes user LKG fallback, project-level isolation,
	// and metadata-derived provenance transactional with respect to file changes.
	var validated Config
	if _, err := decodeTOMLBytes(data, &validated); err != nil {
		return toml.MetaData{}, fmt.Errorf("config %s: %w", path, err)
	}
	meta, err := decodeTOMLBytes(data, cfg)
	if err != nil {
		return toml.MetaData{}, fmt.Errorf("config %s: %w", path, err)
	}
	if meta.IsDefined("providers") {
		var persisted Config
		if _, err := decodeTOMLBytes(data, &persisted); err != nil {
			return toml.MetaData{}, fmt.Errorf("config %s: %w", path, err)
		}
		markPersistedDeepSeekOfficialPricing(&persisted)
		markers := map[string]string{}
		for i := range persisted.Providers {
			markers[providerMergeKey(persisted.Providers[i])] = persisted.Providers[i].persistedOfficialCurrency
		}
		for i := range cfg.Providers {
			cfg.Providers[i].persistedOfficialCurrency = markers[providerMergeKey(cfg.Providers[i])]
		}
	}
	return meta, nil
}

func mergeRuntimeTOMLFileSnapshot(cfg *Config, path string) (toml.MetaData, error) {
	if _, err := os.Stat(path); err == nil {
		if err := migrateLegacyMCPTiersFile(path); err != nil {
			slog.Warn("config: legacy mcp tier migration failed", "path", path, "err", err)
		}
	}
	return mergeFileSnapshot(cfg, path)
}

// normalizeLegacyMCPTiers keeps loaded legacy config files on the new product
// behavior: enabled MCP servers connect in the background by default, and the
// retired per-server startup tier is no longer a user-facing setting.
func normalizeLegacyMCPTiers(c *Config) {
	if c == nil {
		return
	}
	for i := range c.Plugins {
		c.Plugins[i].Tier = ""
	}
}

// normalizeLegacyAgentStepLimits keeps old TOML readable without allowing a
// stale hidden value to override the adaptive progress policy. The fields stay
// in AgentConfig for decoder and cross-version desktop compatibility only.
func normalizeLegacyAgentStepLimits(c *Config) bool {
	if c == nil {
		return false
	}
	found := c.Agent.MaxSteps != 0 || c.Agent.PlannerMaxSteps != 0
	c.Agent.MaxSteps = 0
	c.Agent.PlannerMaxSteps = 0
	return found
}

// MigrateLegacyAgentStepLimitsForRoot removes retired [agent] step-limit keys
// from the user and project config selected for root. Boot calls it immediately
// before LoadForRoot, so config-only/read-only commands never rewrite files and
// the runtime can surface exactly one migration notice.
func MigrateLegacyAgentStepLimitsForRoot(root string) (bool, error) {
	root = resolveRoot(root)
	paths := make([]string, 0, 2)
	if userPath := userConfigLoadPath(); userPath != "" {
		paths = append(paths, userPath)
	}
	paths = append(paths, ProjectConfigPath(root))

	changedAny := false
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		changed, err := migrateLegacyAgentStepLimitsFile(path)
		if err != nil {
			return changedAny, fmt.Errorf("migrate deprecated agent step limits in %s: %w", path, err)
		}
		changedAny = changedAny || changed
	}
	return changedAny, nil
}

// migrateLegacyAgentStepLimitsFile removes retired [agent] step-limit keys
// before runtime decoding. A process-wide lock makes concurrent desktop tab
// builds observe a single migration; the atomic rewrite protects other readers.
func migrateLegacyAgentStepLimitsFile(path string) (bool, error) {
	return migrateRetiredConfigKeysFile(path, stripLegacyAgentStepLimitLines)
}

func stripLegacyAgentStepLimitLines(raw string) (string, bool) {
	return stripTOMLKeyLines(raw, "agent", "max_steps", "planner_max_steps")
}

// MigrateLegacyRedactToolOutputForRoot removes the retired
// [secrets].redact_tool_output setting from the user and project configs chosen
// for root. The setting no longer controls any runtime behavior; removing it
// avoids leaving an explicit `true` value on disk that falsely suggests live
// output or transcript redaction is still active.
func MigrateLegacyRedactToolOutputForRoot(root string) (bool, error) {
	root = resolveRoot(root)
	paths := make([]string, 0, 2)
	if userPath := userConfigLoadPath(); userPath != "" {
		paths = append(paths, userPath)
	}
	paths = append(paths, ProjectConfigPath(root))

	changedAny := false
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		changed, err := migrateLegacyRedactToolOutputFile(path)
		if err != nil {
			return changedAny, fmt.Errorf("migrate deprecated redact_tool_output in %s: %w", path, err)
		}
		changedAny = changedAny || changed
	}
	return changedAny, nil
}

func migrateLegacyRedactToolOutputFile(path string) (bool, error) {
	return migrateRetiredConfigKeysFile(path, stripLegacyRedactToolOutputLines)
}

func stripLegacyRedactToolOutputLines(raw string) (string, bool) {
	return stripTOMLKeyLines(raw, "secrets", "redact_tool_output")
}

// MigrateLegacyMemoryCompilerForRoot removes the retired
// [agent].memory_compiler setting from the user and project configs chosen for
// root. The Memory v5 execution compiler was removed; stripping the key avoids
// leaving values on disk that falsely suggest compiler behavior (especially a
// stale verbosity = "compact") is still active.
func MigrateLegacyMemoryCompilerForRoot(root string) (bool, error) {
	root = resolveRoot(root)
	paths := make([]string, 0, 2)
	if userPath := userConfigLoadPath(); userPath != "" {
		paths = append(paths, userPath)
	}
	paths = append(paths, ProjectConfigPath(root))

	changedAny := false
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		changed, err := migrateLegacyMemoryCompilerFile(path)
		if err != nil {
			return changedAny, fmt.Errorf("migrate deprecated memory_compiler in %s: %w", path, err)
		}
		changedAny = changedAny || changed
	}
	return changedAny, nil
}

func migrateLegacyMemoryCompilerFile(path string) (bool, error) {
	return migrateRetiredConfigKeysFile(path, stripLegacyMemoryCompilerLines)
}

func migrateRetiredConfigKeysFile(path string, strip func(string) (string, bool)) (bool, error) {
	unlock, err := LockConfigFileEdits(path)
	if err != nil {
		return false, err
	}
	defer unlock()
	resolved, exists, err := statConfigPath(path)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return false, err
	}
	raw, err := fileencoding.ReadFileUTF8(resolved)
	if err != nil {
		return false, err
	}
	next, changed := strip(string(raw))
	if !changed {
		return false, nil
	}
	if err := fileutil.AtomicWriteFile(resolved, []byte(next), info.Mode().Perm()); err != nil {
		return false, err
	}
	return true, nil
}

func stripLegacyMemoryCompilerLines(raw string) (string, bool) {
	return stripTOMLKeyLines(raw, "agent", "memory_compiler")
}

// MigrateLegacyMultiThresholdCompactionForRoot strips retired soft/snip/force keys.
func MigrateLegacyMultiThresholdCompactionForRoot(root string) (bool, error) {
	root = resolveRoot(root)
	paths := make([]string, 0, 2)
	if userPath := userConfigLoadPath(); userPath != "" {
		paths = append(paths, userPath)
	}
	paths = append(paths, ProjectConfigPath(root))

	changedAny := false
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		changed, err := migrateLegacyMultiThresholdCompactionFile(path)
		if err != nil {
			return changedAny, fmt.Errorf("migrate deprecated multi-threshold compaction keys in %s: %w", path, err)
		}
		changedAny = changedAny || changed
	}
	return changedAny, nil
}

func migrateLegacyMultiThresholdCompactionFile(path string) (bool, error) {
	return migrateRetiredConfigKeysFile(path, stripLegacyMultiThresholdCompactionLines)
}

func stripLegacyMultiThresholdCompactionLines(raw string) (string, bool) {
	return stripTOMLKeyLines(raw, "agent",
		"soft_compact_ratio",
		"tool_result_snip_ratio",
		"compact_force_ratio",
		"cold_resume_prune",
		"context_editing",
	)
}

func migrateLegacyMCPTiersFile(path string) error {
	_, err := migrateRetiredConfigKeysFile(path, stripLegacyMCPTierLines)
	return err
}

// MigrateLegacyMCPTiersForRoot keeps boot's historical on-disk migration
// separate from immutable snapshots, whose freshness checks must be read-only.
func MigrateLegacyMCPTiersForRoot(root string) {
	for _, path := range []string{userConfigLoadPath(), filepath.Join(resolveRoot(root), "reasonix.toml")} {
		if path == "" {
			continue
		}
		if err := migrateLegacyMCPTiersFile(path); err != nil {
			slog.Warn("config: legacy mcp tier migration failed", "path", path, "err", err)
		}
	}
}

func stripLegacyMCPTierLines(raw string) (string, bool) {
	return stripTOMLKeyLines(raw, "plugins", "tier")
}

// tomlStringState tracks whether a line-oriented scan is currently inside a
// TOML multiline string, so retired-key strippers never treat prose inside a
// `"""..."""` or `”'...”'` value (e.g. a config example quoted in a
// system_prompt) as a section header or key assignment.
type tomlStringState int

const (
	tomlOutside tomlStringState = iota
	tomlInMultilineBasic
	tomlInMultilineLiteral
)

// advanceTOMLStringState scans one raw line and returns the multiline-string
// state after it. Outside strings it honours single-line strings and `#`
// comments so quote delimiters inside them cannot open a multiline state.
// The scan is intentionally conservative: on malformed input it prefers
// staying/returning outside, which makes callers keep lines rather than
// delete them.
func advanceTOMLStringState(state tomlStringState, line string) tomlStringState {
	i := 0
	for i < len(line) {
		switch state {
		case tomlInMultilineBasic:
			if line[i] == '\\' {
				i += 2
				continue
			}
			if strings.HasPrefix(line[i:], `"""`) {
				state = tomlOutside
				i += 3
				continue
			}
			i++
		case tomlInMultilineLiteral:
			if strings.HasPrefix(line[i:], "'''") {
				state = tomlOutside
				i += 3
				continue
			}
			i++
		default: // tomlOutside
			switch {
			case line[i] == '#':
				return state // rest of the line is a comment
			case strings.HasPrefix(line[i:], `"""`):
				state = tomlInMultilineBasic
				i += 3
			case strings.HasPrefix(line[i:], "'''"):
				state = tomlInMultilineLiteral
				i += 3
			case line[i] == '"': // single-line basic string
				i++
				for i < len(line) && line[i] != '"' {
					if line[i] == '\\' {
						i++
					}
					i++
				}
				i++ // closing quote (or line end on malformed input)
			case line[i] == '\'': // single-line literal string
				i++
				for i < len(line) && line[i] != '\'' {
					i++
				}
				i++
			default:
				i++
			}
		}
	}
	return state
}

// stripTOMLKeyLines removes top-level `key = ...` assignment lines under the
// named section while leaving every line inside a TOML multiline string
// untouched. All retired-config-key migrations share it so none of them can
// corrupt a multiline value (such as a system_prompt quoting a config
// example). A dropped line is first checked to not itself open a multiline
// value; if it would, the line is kept — for these retired keys that never
// happens (their values are single-line), and keeping a stale line is always
// safer than truncating a string the user wrote.
func stripTOMLKeyLines(raw, section string, keys ...string) (string, bool) {
	lines := strings.Split(raw, "\n")
	current := ""
	state := tomlOutside
	changed := false
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if state != tomlOutside {
			// Inside a multiline string: never a section header or key line.
			out = append(out, line)
			state = advanceTOMLStringState(state, line)
			continue
		}
		if header := tomlSectionHeader(line); header != "" {
			current = header
		}
		next := advanceTOMLStringState(tomlOutside, line)
		if current == section && next == tomlOutside {
			dropped := false
			for _, key := range keys {
				if isTOMLKeyAssignment(line, key) {
					changed = true
					dropped = true
					break
				}
			}
			if dropped {
				continue
			}
		}
		out = append(out, line)
		state = next
	}
	return strings.Join(out, "\n"), changed
}

func tomlSectionHeader(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return ""
	}
	if i := strings.Index(trimmed, "#"); i >= 0 {
		trimmed = strings.TrimSpace(trimmed[:i])
	}
	if strings.HasPrefix(trimmed, "[[") && strings.HasSuffix(trimmed, "]]") {
		return strings.TrimSpace(trimmed[2 : len(trimmed)-2])
	}
	if strings.HasSuffix(trimmed, "]") {
		return strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	}
	return "other"
}

// normalizeLegacyProviderModels repairs provider entries written by older
// desktop builds that carried the official provider name/endpoint but omitted the
// model field. The repair is intentionally narrow: valid user-provided model
// lists are left untouched, while known official aliases get the model implied by
// their preset name so model pickers and provider validation have an option.
func normalizeLegacyProviderModels(c *Config) {
	if c == nil {
		return
	}
	for i := range c.Providers {
		p := &c.Providers[i]
		if providerHasAnyModel(*p) {
			continue
		}
		if model := legacyOfficialProviderModel(p.Name); model != "" {
			p.Model = model
		}
	}
}

const (
	legacyStepFunOpenAIBaseURL      = "https://api.stepfun.ai/step_plan/v1"
	officialStepFunOpenAIBaseURL    = "https://api.stepfun.com/step_plan/v1"
	legacyStepFunAnthropicBaseURL   = "https://api.stepfun.ai/step_plan"
	officialStepFunAnthropicBaseURL = "https://api.stepfun.com/step_plan"
)

func normalizeLegacyStepFunBaseURLs(c *Config) bool {
	// Both stepfun.ai (global) and stepfun.com (China) are official endpoints.
	// BaseURL is user-owned provider configuration, so neither runtime loading
	// nor an unrelated settings save may infer a region and rewrite it.
	return false
}

func normalizedBaseURLForMigration(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func normalizeLegacyLongCatContextWindows(c *Config) bool {
	if c == nil {
		return false
	}
	changed := false
	for i := range c.Providers {
		p := &c.Providers[i]
		if p.ContextWindow != legacyLongCat20ContextWindow {
			continue
		}
		var kind, baseURL string
		switch strings.TrimSpace(p.PresetID) {
		case "longcat-openai":
			kind, baseURL = "openai", longCatOpenAIBaseURL
		case "longcat-anthropic":
			kind, baseURL = "anthropic", longCatAnthropicBaseURL
		default:
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(p.Kind), kind) ||
			normalizedBaseURLForMigration(p.BaseURL) != baseURL ||
			!stringSlicesEqual(p.Models, longCat20Models) ||
			p.Model != "" ||
			p.Default != longCat20Models[0] {
			continue
		}
		p.ContextWindow = longCat20ContextWindow
		changed = true
	}
	return changed
}

// normalizeLegacyQwenContextWindows upgrades only installed official Qwen
// presets that still carry the old zero context window and untouched model
// catalog. Custom endpoints, catalogs, provider-wide windows, and existing
// per-model override values remain user-owned.
func normalizeLegacyQwenContextWindows(c *Config) bool {
	if c == nil {
		return false
	}
	changed := false
	for i := range c.Providers {
		p := &c.Providers[i]
		if p.ContextWindow != 0 {
			continue
		}
		presetID := qwenPresetIDForMigration(*p)
		if presetID == "" {
			continue
		}
		preset, ok := CuratedProviderPreset(presetID)
		if !ok || len(preset.Entries) != 1 {
			continue
		}
		canonical := preset.Entries[0]
		if !strings.EqualFold(strings.TrimSpace(p.Kind), strings.TrimSpace(canonical.Kind)) ||
			normalizedBaseURLForMigration(p.BaseURL) != normalizedBaseURLForMigration(canonical.BaseURL) ||
			!stringSlicesEqual(p.Models, canonical.Models) ||
			strings.TrimSpace(p.Model) != "" {
			continue
		}
		p.ContextWindow = canonical.ContextWindow
		mergeMissingQwenContextOverrides(p, canonical.ModelOverrides)
		changed = true
	}
	return changed
}

func qwenPresetIDForMigration(p ProviderEntry) string {
	presetID := strings.TrimSpace(p.PresetID)
	if presetID == "" {
		presetID = strings.TrimSpace(p.Name)
	}
	switch presetID {
	case "qwen-cn",
		"qwen-global",
		"qwen-coding-plan-cn",
		"qwen-coding-plan-cn-anthropic",
		"qwen-coding-plan-global",
		"qwen-coding-plan-global-anthropic":
		return presetID
	default:
		return ""
	}
}

func mergeMissingQwenContextOverrides(p *ProviderEntry, defaults map[string]ProviderModelOverride) {
	if p == nil || len(defaults) == 0 {
		return
	}
	if p.ModelOverrides == nil {
		p.ModelOverrides = make(map[string]ProviderModelOverride, len(defaults))
	}
	for defaultKey, defaultOverride := range defaults {
		overrideKey := defaultKey
		for key := range p.ModelOverrides {
			if strings.EqualFold(strings.TrimSpace(key), defaultKey) {
				overrideKey = key
				break
			}
		}
		override := p.ModelOverrides[overrideKey]
		if override.ContextWindow == 0 {
			override.ContextWindow = defaultOverride.ContextWindow
			p.ModelOverrides[overrideKey] = override
		}
	}
}

// normalizeLegacyKimiK3Catalog upgrades only untouched Kimi direct-API model
// catalogs on the official regional endpoints. Custom model lists, endpoints,
// defaults, credentials, and provider-wide settings remain user-owned.
func normalizeLegacyKimiK3Catalog(c *Config) bool {
	if c == nil {
		return false
	}
	changed := false
	for i := range c.Providers {
		p := &c.Providers[i]
		presetID := strings.TrimSpace(p.PresetID)
		name := strings.TrimSpace(p.Name)
		var baseURL string
		switch {
		case presetID == "kimi-cn" || (presetID == "" && name == "kimi-cn"):
			baseURL = "https://api.moonshot.cn/v1"
		case presetID == "kimi-global" || (presetID == "" && name == "kimi-global"):
			baseURL = "https://api.moonshot.ai/v1"
		default:
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(p.Kind), "openai") ||
			normalizedBaseURLForMigration(p.BaseURL) != baseURL ||
			!stringSlicesEqual(p.Models, legacyKimiAPIModels) ||
			strings.TrimSpace(p.Model) != "" {
			continue
		}
		p.Models = append([]string(nil), kimiAPIModels...)
		p.VisionModels = migrateKimiK3VisionModels(p.VisionModels, legacyKimiAPIModels)
		mergeMissingKimiK3Override(p, kimiK3DirectOverride())
		changed = true
	}
	return changed
}

// migrateKimiK3VisionModels preserves explicit provider-level vision choices.
// A nil list or an exact copy of the old preset list indicates that the user
// has not customized vision support and should receive Kimi K3's capability.
func migrateKimiK3VisionModels(current, legacy []string) []string {
	if current != nil && (legacy == nil || !stringSlicesEqual(current, legacy)) {
		return current
	}
	return mergeModelLists([]string{"kimi-k3"}, current)
}

func mergeMissingKimiK3Override(p *ProviderEntry, defaults ProviderModelOverride) {
	if p.ModelOverrides == nil {
		p.ModelOverrides = map[string]ProviderModelOverride{}
	}
	overrideKey := "kimi-k3"
	for key := range p.ModelOverrides {
		if strings.EqualFold(strings.TrimSpace(key), overrideKey) {
			overrideKey = key
			break
		}
	}
	kimiK3 := p.ModelOverrides[overrideKey]
	if strings.TrimSpace(kimiK3.ReasoningProtocol) == "" {
		kimiK3.ReasoningProtocol = defaults.ReasoningProtocol
	}
	if kimiK3.SupportedEfforts == nil {
		kimiK3.SupportedEfforts = append([]string(nil), defaults.SupportedEfforts...)
	}
	if strings.TrimSpace(kimiK3.DefaultEffort) == "" && containsString(normalizedEffortLevels(kimiK3.SupportedEfforts), defaults.DefaultEffort) {
		kimiK3.DefaultEffort = defaults.DefaultEffort
	}
	if kimiK3.ContextWindow <= 0 {
		kimiK3.ContextWindow = defaults.ContextWindow
	}
	p.ModelOverrides[overrideKey] = kimiK3
}

// normalizeLegacyOpenCodeGoKimiK3Catalog upgrades only the untouched model
// catalog from the original editable OpenCode Go preset. A user-curated model
// list or custom endpoint is left alone, while other provider edits (headers,
// key env, provider-wide context) survive the additive K3 capability update.
func normalizeLegacyOpenCodeGoKimiK3Catalog(c *Config) (changed bool) {
	if c == nil {
		return false
	}
	for i := range c.Providers {
		p := &c.Providers[i]
		presetID := strings.TrimSpace(p.PresetID)
		if (presetID != "opencode-go" && (presetID != "" || strings.TrimSpace(p.Name) != "opencode-go")) ||
			!strings.EqualFold(strings.TrimSpace(p.Kind), "openai") ||
			normalizedBaseURLForMigration(p.BaseURL) != "https://opencode.ai/zen/go/v1" ||
			!stringSlicesEqual(p.Models, legacyOpenCodeGoModels) ||
			strings.TrimSpace(p.Model) != "" {
			continue
		}
		p.Models = append([]string(nil), opencodeGoModels...)
		p.VisionModels = migrateKimiK3VisionModels(p.VisionModels, nil)
		mergeMissingKimiK3Override(p, ProviderModelOverride{
			ReasoningProtocol: ReasoningProtocolOpenAI,
			SupportedEfforts:  []string{"high", "max"},
			DefaultEffort:     "max",
			ContextWindow:     1_048_576,
		})
		changed = true
	}
	return changed
}

// normalizeLegacyOpenCodeGoVisionCatalog upgrades only the untouched Chat
// catalog that predates OpenCode Go's DeepSeek vision SKU. Custom model lists
// and explicit image-input choices remain user-owned.
func normalizeLegacyOpenCodeGoVisionCatalog(c *Config) (changed bool) {
	if c == nil {
		return false
	}
	for i := range c.Providers {
		p := &c.Providers[i]
		presetID := strings.TrimSpace(p.PresetID)
		if (presetID != "opencode-go" && (presetID != "" || strings.TrimSpace(p.Name) != "opencode-go")) ||
			!strings.EqualFold(strings.TrimSpace(p.Kind), "openai") ||
			normalizedBaseURLForMigration(p.BaseURL) != "https://opencode.ai/zen/go/v1" ||
			!stringSlicesEqual(p.Models, preVisionOpenCodeGoModels) ||
			strings.TrimSpace(p.Model) != "" {
			continue
		}
		p.Models = append([]string(nil), opencodeGoModels...)
		if p.VisionModels == nil || stringSlicesEqual(p.VisionModels, preVisionOpenCodeGoVisionModels) {
			p.VisionModels = append([]string(nil), opencodeGoVisionModels...)
		}
		mergeMissingOpenCodeGoVisionOverride(p)
		changed = true
	}
	return changed
}

func mergeMissingOpenCodeGoVisionOverride(p *ProviderEntry) {
	if p.ModelOverrides == nil {
		p.ModelOverrides = map[string]ProviderModelOverride{}
	}
	const model = "deepseek-v4-flash-vision-exp"
	key := model
	for candidate := range p.ModelOverrides {
		if strings.EqualFold(strings.TrimSpace(candidate), model) {
			key = candidate
			break
		}
	}
	override := p.ModelOverrides[key]
	if strings.TrimSpace(override.ReasoningProtocol) == "" {
		override.ReasoningProtocol = ReasoningProtocolDeepSeek
	}
	if override.SupportedEfforts == nil {
		override.SupportedEfforts = []string{"disabled", "low", "high", "max"}
	}
	if strings.TrimSpace(override.DefaultEffort) == "" && containsString(normalizedEffortLevels(override.SupportedEfforts), "high") {
		override.DefaultEffort = "high"
	}
	if override.ContextWindow <= 0 {
		override.ContextWindow = 1_000_000
	}
	p.ModelOverrides[key] = override
}

func normalizeLegacyMimoProviderCatalogs(c *Config) bool {
	if c == nil {
		return false
	}
	changed := false
	for i := range c.Providers {
		p := &c.Providers[i]
		if legacyMimoProviderName(p.Name) == "" || len(p.Models) > 0 {
			continue
		}
		switch officialProviderHost(p.BaseURL) {
		case "api.xiaomimimo.com":
			if applyLegacyMimoCatalog(p, legacyMimoAPIModels(), []string{"mimo-v2.5", "mimo-v2-omni"}, "mimo-v2.5-pro") {
				changed = true
			}
		case "token-plan-cn.xiaomimimo.com":
			if applyLegacyMimoCatalog(p, legacyMimoTokenPlanModels(), []string{"mimo-v2.5"}, "mimo-v2.5-pro") {
				changed = true
			}
		}
	}
	return changed
}

func applyLegacyMimoCatalog(p *ProviderEntry, models, visionModels []string, fallbackDefault string) bool {
	if p == nil || len(models) == 0 {
		return false
	}
	beforeModels := append([]string(nil), p.Models...)
	beforeVision := append([]string(nil), p.VisionModels...)
	beforeDefault := p.Default
	beforeModel := p.Model
	beforeWindow := p.ContextWindow
	beforeNoProxy := p.NoProxy
	beforePricesLen := len(p.Prices)

	currentDefault := strings.TrimSpace(p.Default)
	if currentDefault == "" {
		currentDefault = strings.TrimSpace(p.Model)
	}
	p.Models = mergeModelLists(models, p.ModelList())
	p.Model = p.Models[0]
	p.Default = firstKnownModel(currentDefault, p.Models, fallbackDefault)
	p.VisionModels = mergeModelLists(visionModels, p.VisionModels)
	backfillOfficialContextWindow(p, 1_048_576)
	p.NoProxy = true
	if p.Prices == nil {
		p.Prices = mimoDomesticPrices(models)
	} else {
		for model, price := range mimoDomesticPrices(models) {
			if p.Prices[model] == nil {
				p.Prices[model] = price
			}
		}
	}

	return !stringSlicesEqual(beforeModels, p.Models) ||
		!stringSlicesEqual(beforeVision, p.VisionModels) ||
		beforeDefault != p.Default ||
		beforeModel != p.Model ||
		beforeWindow != p.ContextWindow ||
		beforeNoProxy != p.NoProxy ||
		beforePricesLen != len(p.Prices)
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func normalizeOfficialDeepSeekModels(c *Config) {
	if c == nil {
		return
	}
	for i := range c.Providers {
		p := &c.Providers[i]
		if officialProviderHost(p.BaseURL) != "api.deepseek.com" {
			continue
		}
		switch strings.TrimSpace(p.Name) {
		case "deepseek":
			ensureProviderModels(p, []string{"deepseek-v4-flash", "deepseek-v4-pro"}, "deepseek-v4-flash")
		case "deepseek-flash":
			ensureProviderModels(p, []string{"deepseek-v4-flash"}, "deepseek-v4-flash")
		case "deepseek-pro":
			ensureProviderModels(p, []string{"deepseek-v4-pro"}, "deepseek-v4-pro")
		case "deepseek-responses":
			ensureProviderModels(p, []string{"deepseek-v4-flash", "deepseek-v4-pro"}, "deepseek-v4-flash")
		}
		backfillOfficialDeepSeekResponsesModels(p)
		backfillDeepSeekAnthropicCapabilities(p)
		backfillOfficialDeepSeekResponsesCapabilities(p)
	}
}

func backfillDeepSeekAnthropicCapabilities(p *ProviderEntry) {
	if p == nil || !strings.EqualFold(strings.TrimSpace(p.Kind), "anthropic") ||
		!IsOfficialDeepSeekWebSearchEndpoint(p) {
		return
	}
	if strings.TrimSpace(p.Thinking) == "" {
		p.Thinking = "enabled"
	}
	backfillOfficialDeepSeekEffortOverrides(p)
}

func officialProviderHost(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func ensureProviderModels(p *ProviderEntry, required []string, fallbackDefault string) {
	if p == nil {
		return
	}
	// If the user has explicitly curated a model list (via Settings), respect
	// that choice and do not merge additional required models.
	if len(p.Models) > 0 {
		return
	}
	models := mergeModelLists(required, p.ModelList())
	if len(models) == 0 {
		return
	}
	p.Model = models[0]
	if len(models) > 1 {
		p.Models = models
		p.Default = firstKnownModel(p.Default, models, fallbackDefault)
		return
	}
	p.Models = nil
	p.Default = ""
}

func legacyOfficialProviderModel(name string) string {
	switch strings.TrimSpace(name) {
	case "deepseek-flash":
		return "deepseek-v4-flash"
	case "deepseek-pro":
		return "deepseek-v4-pro"
	case "mimo", "xiaomi-mimo", "xiaomi_mimo", "mimo-api", "mimo-token-plan", "mimo-pro":
		return "mimo-v2.5-pro"
	case "mimo-flash":
		return "mimo-v2.5"
	default:
		return ""
	}
}

func normalizeLegacyMimoCustomProviders(c *Config) bool {
	return normalizeLegacyMimoCustomProvidersForRefs(c, legacyMimoConfigRefs(c)...)
}

// NormalizeLegacyMimoCustomProvidersForRefs appends custom OpenAI-compatible
// MiMo providers needed by legacy refs that live outside reasonix.toml, such as
// restored desktop tab state.
func NormalizeLegacyMimoCustomProvidersForRefs(c *Config, refs ...string) bool {
	return normalizeLegacyMimoCustomProvidersForRefs(c, refs...)
}

func normalizeLegacyMimoCustomProvidersForRefs(c *Config, refs ...string) bool {
	if c == nil {
		return false
	}
	needed := map[string]bool{}
	addRef := func(ref string) {
		if name := legacyMimoProviderNameForRef(ref); name != "" {
			needed[name] = true
		}
	}
	for _, ref := range refs {
		addRef(ref)
	}
	changed := normalizeLegacyMimoProviderCatalogs(c)
	for name := range needed {
		if _, ok := c.Provider(name); ok {
			continue
		}
		c.Providers = append(c.Providers, legacyMimoCustomProvider(name))
		changed = true
	}
	if normalizeLegacyMimoProviderCatalogs(c) {
		changed = true
	}
	return changed
}

func legacyMimoConfigRefs(c *Config) []string {
	if c == nil {
		return nil
	}
	refs := []string{
		c.DefaultModel,
		c.Agent.PlannerModel,
		c.Agent.VisionModel,
		c.Agent.WebSearchModel,
		c.Agent.SubagentModel,
		c.Bot.Model,
	}
	for _, ref := range c.Agent.SubagentModels {
		refs = append(refs, ref)
	}
	for _, conn := range c.Bot.Connections {
		refs = append(refs, conn.Model)
	}
	refs = append(refs, c.Desktop.ProviderAccess...)
	return refs
}

func legacyMimoProviderName(ref string) string {
	switch strings.TrimSpace(ref) {
	case "mimo", "xiaomi-mimo", "xiaomi_mimo", "mimo-api", "mimo-token-plan", "mimo-pro", "mimo-flash":
		return strings.TrimSpace(ref)
	default:
		return ""
	}
}

func legacyMimoProviderNameForRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	providerName, _, hasModel := strings.Cut(ref, "/")
	if name := legacyMimoProviderName(providerName); name != "" {
		return name
	}
	if hasModel {
		return ""
	}
	switch ref {
	case "mimo-v2.5-pro":
		return "mimo-pro"
	case "mimo-v2.5":
		return "mimo-flash"
	case "mimo-v2-omni":
		return "mimo-api"
	default:
		return ""
	}
}

func legacyMimoAPIModels() []string {
	return []string{"mimo-v2.5-pro", "mimo-v2.5", "mimo-v2-omni"}
}

func legacyMimoTokenPlanModels() []string {
	return []string{"mimo-v2.5-pro", "mimo-v2.5"}
}

func legacyMimoCustomProvider(name string) ProviderEntry {
	switch strings.TrimSpace(name) {
	case "mimo", "xiaomi-mimo", "xiaomi_mimo", "mimo-api":
		models := legacyMimoAPIModels()
		return ProviderEntry{
			Name:          strings.TrimSpace(name),
			Kind:          "openai",
			BaseURL:       "https://api.xiaomimimo.com/v1",
			Models:        models,
			VisionModels:  []string{"mimo-v2.5", "mimo-v2-omni"},
			Default:       "mimo-v2.5-pro",
			APIKeyEnv:     "MIMO_API_KEY",
			ContextWindow: 1_048_576,
			Prices:        mimoDomesticPrices(models),
			NoProxy:       true,
		}
	case "mimo-token-plan":
		models := legacyMimoTokenPlanModels()
		return ProviderEntry{
			Name:          "mimo-token-plan",
			Kind:          "openai",
			BaseURL:       "https://token-plan-cn.xiaomimimo.com/v1",
			Models:        models,
			VisionModels:  []string{"mimo-v2.5"},
			Default:       "mimo-v2.5-pro",
			APIKeyEnv:     "MIMO_API_KEY",
			ContextWindow: 1_048_576,
			Prices:        mimoDomesticPrices(models),
			NoProxy:       true,
		}
	case "mimo-flash":
		return ProviderEntry{Name: "mimo-flash", Kind: "openai", BaseURL: "https://token-plan-cn.xiaomimimo.com/v1", Model: "mimo-v2.5", APIKeyEnv: "MIMO_API_KEY", ContextWindow: 1_000_000, Price: mimoV25Price(), NoProxy: true}
	default:
		return ProviderEntry{Name: "mimo-pro", Kind: "openai", BaseURL: "https://token-plan-cn.xiaomimimo.com/v1", Model: "mimo-v2.5-pro", APIKeyEnv: "MIMO_API_KEY", ContextWindow: 1_000_000, Price: mimoV25ProPrice(), NoProxy: true}
	}
}

func normalizeDesktopOfficialProviderAccess(c *Config) {
	if c == nil || len(c.Desktop.ProviderAccess) == 0 {
		return
	}
	canCanonicalizeDeepSeek := canCanonicalizeLegacyDeepSeekProviders(c)
	_, hasCanonicalDeepSeek := c.Provider("deepseek")
	legacyDeepSeek := officialLegacyDeepSeekProviders(c)
	seen := desktopProviderAccessMap(nil)
	next := make([]string, 0, len(c.Desktop.ProviderAccess))
	for _, name := range c.Desktop.ProviderAccess {
		name = strings.TrimSpace(name)
		if name == "deepseek" && !canCanonicalizeDeepSeek && !hasCanonicalDeepSeek && len(legacyDeepSeek) > 0 {
			for _, legacy := range legacyDeepSeek {
				if !seen[legacy.Name] {
					seen[legacy.Name] = true
					next = append(next, legacy.Name)
				}
			}
			continue
		}
		if CanonicalDesktopOfficialProviderName(name) != "deepseek" || name == "deepseek" || canCanonicalizeDeepSeek {
			name = desktopProviderAccessNameForConfig(c, name)
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		next = append(next, name)
	}
	c.Desktop.ProviderAccess = next
	if seen["deepseek"] {
		ensureDeepSeekOfficialProvider(c)
	}
	normalizeLegacyMimoProviderCatalogs(c)
	retargetAccess := maps.Clone(seen)
	if p, ok := c.Provider("deepseek"); !canCanonicalizeDeepSeek || !ok || officialProviderKind(p) != "deepseek" {
		delete(retargetAccess, "deepseek")
	}
	retargetDesktopOfficialRefs(c, retargetAccess)
}

// NormalizeLegacyDesktopProviderAccess seeds the desktop provider-access list
// for configs written before Settings tracked explicit provider access. Callers
// should only use this when they know the TOML did not declare provider_access;
// an explicit empty list means the user removed all access entries.
func NormalizeLegacyDesktopProviderAccess(c *Config) {
	if c == nil || len(c.Desktop.ProviderAccess) > 0 {
		return
	}
	seen := desktopProviderAccessMap(nil)
	var access []string
	add := func(name string) {
		name = desktopProviderAccessNameForConfig(c, name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		access = append(access, name)
	}
	addRef := func(ref string) {
		if entry, ok := c.ResolveModel(ref); ok {
			if !entry.Configured() {
				return
			}
			add(entry.Name)
		}
	}
	addRef(c.DefaultModel)
	addRef(c.Agent.PlannerModel)
	addRef(c.Agent.VisionModel)
	addRef(c.Agent.WebSearchModel)
	addRef(c.Agent.SubagentModel)
	for _, ref := range c.Agent.SubagentModels {
		addRef(ref)
	}
	addRef(c.Bot.Model)
	for _, conn := range c.Bot.Connections {
		addRef(conn.Model)
	}
	for i := range c.Providers {
		p := &c.Providers[i]
		if legacyMimoProviderName(p.Name) != "" && len(p.ModelList()) > 0 {
			add(p.Name)
			continue
		}
		if p.Configured() && len(p.ModelList()) > 0 {
			add(p.Name)
		}
	}
	if len(access) == 0 {
		return
	}
	c.Desktop.ProviderAccess = access
	normalizeDesktopOfficialProviderAccess(c)
}

func canonicalDesktopOfficialProviderName(name string) string {
	switch strings.TrimSpace(name) {
	case "deepseek-flash", "deepseek-pro":
		return "deepseek"
	default:
		return strings.TrimSpace(name)
	}
}

func desktopProviderAccessNameForConfig(c *Config, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	canonical := canonicalDesktopOfficialProviderName(name)
	if canonical == name {
		return name
	}
	if c == nil {
		return canonical
	}
	if p, ok := c.Provider(name); ok && !providerEntryMatchesCanonicalOfficialAccess(p, canonical) {
		return name
	}
	return canonical
}

func providerEntryMatchesCanonicalOfficialAccess(p *ProviderEntry, canonical string) bool {
	if p == nil {
		return false
	}
	switch canonical {
	case "deepseek":
		return isCanonicalizableLegacyDeepSeekProvider(p)
	default:
		return false
	}
}

// CanonicalDesktopOfficialProviderName returns the Settings Center provider ID
// for built-in official provider aliases.
func CanonicalDesktopOfficialProviderName(name string) string {
	return canonicalDesktopOfficialProviderName(name)
}

func desktopProviderAccessMap(names []string) map[string]bool {
	out := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			out[name] = true
		}
	}
	return out
}

func ensureDeepSeekOfficialProvider(c *Config) {
	if p, ok := c.Provider("deepseek"); ok {
		if officialProviderKind(p) == "deepseek" {
			backfillOfficialContextWindow(p, 1_000_000)
		}
		return
	}
	if !canCanonicalizeLegacyDeepSeekProviders(c) {
		return
	}
	entry := ProviderEntry{
		Name:           "deepseek",
		Kind:           "anthropic",
		BaseURL:        deepSeekAnthropicBaseURL,
		Models:         []string{"deepseek-v4-flash", "deepseek-v4-pro"},
		Default:        "deepseek-v4-flash",
		APIKeyEnv:      "DEEPSEEK_API_KEY",
		BalanceURL:     "https://api.deepseek.com/user/balance",
		Thinking:       "enabled",
		WebSearch:      boolPointer(true),
		ContextWindow:  1_000_000,
		Prices:         deepSeekV4PricesForConfig(c),
		ModelOverrides: deepSeekV4EffortOverrides(),
	}
	legacyProviders := officialLegacyDeepSeekProviders(c)
	if len(legacyProviders) > 0 {
		entry = officialProviderFromLegacy(entry, legacyProviders[0])
		currency := c.DeepSeekOfficialPricingCurrency()
		if c.DesktopCurrency() == "" && legacyProviders[0].persistedOfficialCurrency != "" {
			currency = legacyProviders[0].persistedOfficialCurrency
			entry.persistedOfficialCurrency = currency
		}
		entry.Prices = DeepSeekV4PricesForCurrency(currency)
		for _, old := range legacyProviders {
			entry.Models = mergeModelLists(entry.Models, old.ModelList())
			mergeLegacyDeepSeekModelConfiguration(&entry, old)
		}
		entry.Default = preferredLegacyDeepSeekDefault(legacyProviders, entry.Models, entry.Default)
	}
	backfillOfficialContextWindow(&entry, 1_000_000)
	c.Providers = append(c.Providers, entry)
}

func isOpenAIProviderKind(e *ProviderEntry) bool {
	return e != nil && strings.EqualFold(strings.TrimSpace(e.Kind), "openai")
}

func mergeCuratedModelsIntoProvider(e *ProviderEntry, models []string, fallback string) {
	// If the user has explicitly curated a model list (via Settings), respect
	// that choice and do not merge additional curated models.
	if len(e.Models) > 0 {
		return
	}
	currentDefault := e.Default
	if strings.TrimSpace(currentDefault) == "" {
		currentDefault = e.Model
	}
	e.Models = mergeModelLists(models, e.ModelList())
	e.Default = firstKnownModel(currentDefault, e.Models, fallback)
}

func backfillOfficialContextWindow(e *ProviderEntry, fallback int) {
	if e != nil && e.ContextWindow <= 0 {
		e.ContextWindow = fallback
	}
}

func officialProviderFromLegacy(entry ProviderEntry, old *ProviderEntry) ProviderEntry {
	if old == nil {
		return entry
	}
	// Start from the legacy entry so current and future transport fields are not
	// silently dropped from the effective canonical provider. Identity, catalog,
	// pricing and capability fields are merged model by model below.
	legacy := cloneProviderEntry(*old)
	legacy.Name = entry.Name
	legacy.Model = ""
	legacy.Models = append([]string(nil), entry.Models...)
	legacy.Default = entry.Default
	legacy.ContextWindow = entry.ContextWindow
	legacy.MaxOutputTokens = entry.MaxOutputTokens
	legacy.Price = nil
	legacy.Prices = clonePricingMap(entry.Prices)
	legacy.ReasoningProtocol = entry.ReasoningProtocol
	legacy.SupportedEfforts = append([]string(nil), entry.SupportedEfforts...)
	legacy.DefaultEffort = entry.DefaultEffort
	legacy.Vision = entry.Vision
	legacy.VisionModels = append([]string(nil), entry.VisionModels...)
	legacy.ModelOverrides = cloneModelOverrideMap(entry.ModelOverrides)
	return legacy
}

func officialLegacyDeepSeekProviders(c *Config) []*ProviderEntry {
	if c == nil {
		return nil
	}
	out := make([]*ProviderEntry, 0, 2)
	for _, name := range []string{"deepseek-flash", "deepseek-pro"} {
		if p, ok := c.Provider(name); ok && isCanonicalizableLegacyDeepSeekProvider(p) {
			out = append(out, p)
		}
	}
	return out
}

func isCanonicalizableLegacyDeepSeekProvider(p *ProviderEntry) bool {
	if p == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(p.Kind)) {
	case "openai":
		return isOfficialDeepSeekOpenAIEndpoint(p.BaseURL)
	case "anthropic":
		return IsOfficialDeepSeekWebSearchEndpoint(p)
	default:
		return false
	}
}

func canCanonicalizeLegacyDeepSeekProviders(c *Config) bool {
	if c == nil {
		return true
	}
	legacy := officialLegacyDeepSeekProviders(c)
	if canonical, ok := c.Provider("deepseek"); ok {
		if officialProviderKind(canonical) != "deepseek" {
			return false
		}
		for _, old := range legacy {
			if !legacyDeepSeekProviderWideFieldsEqual(canonical, old) ||
				!legacyDeepSeekModelFieldsCompatibleIgnoringDefault(canonical, old) {
				return false
			}
		}
	}
	for i := 1; i < len(legacy); i++ {
		if !legacyDeepSeekProviderWideFieldsEqual(legacy[0], legacy[i]) ||
			!legacyDeepSeekModelFieldsCompatible(legacy[0], legacy[i]) {
			return false
		}
	}
	return true
}

func legacyDeepSeekModelFieldsCompatibleIgnoringDefault(a, b *ProviderEntry) bool {
	if a == nil || b == nil {
		return a == b
	}
	left := cloneProviderEntry(*a)
	right := cloneProviderEntry(*b)
	left.Default = ""
	right.Default = ""
	return legacyDeepSeekModelFieldsCompatible(&left, &right)
}

func legacyDeepSeekProviderWideFieldsEqual(a, b *ProviderEntry) bool {
	if a == nil || b == nil {
		return a == b
	}
	left := legacyDeepSeekProviderWideProjection(a)
	right := legacyDeepSeekProviderWideProjection(b)
	return reflect.DeepEqual(left, right)
}

func legacyDeepSeekProviderWideProjection(entry *ProviderEntry) ProviderEntry {
	out := cloneProviderEntry(*entry)
	out.Name = ""
	out.Kind = strings.ToLower(strings.TrimSpace(out.Kind))
	out.BaseURL = normalizedBaseURLForMigration(out.BaseURL)
	out.ChatURL = strings.TrimSpace(out.ChatURL)
	out.RequestURL = strings.TrimSpace(out.RequestURL)
	out.ModelsURL = strings.TrimSpace(out.ModelsURL)
	out.APIKeyEnv = strings.TrimSpace(out.APIKeyEnv)
	out.BalanceURL = normalizedDeepSeekBalanceURL(out.BalanceURL)
	out.ResponsesMode = strings.TrimSpace(out.ResponsesMode)
	out.Thinking = strings.TrimSpace(out.Thinking)
	out.Effort = strings.TrimSpace(out.Effort)
	out.VisionDetail = strings.TrimSpace(out.VisionDetail)

	// These fields can be represented independently for every model in the
	// canonical provider. They are compared by legacyDeepSeekModelFieldsCompatible.
	out.Model = ""
	out.Models = nil
	out.Default = ""
	out.ContextWindow = 0
	out.MaxOutputTokens = 0
	out.Price = nil
	out.Prices = nil
	out.ReasoningProtocol = ""
	out.SupportedEfforts = nil
	out.DefaultEffort = ""
	out.Vision = false
	out.VisionModels = nil
	out.ModelOverrides = nil
	out.visionOverride = nil
	out.resolvedAPIKey = ""
	out.resolvedSource = CredentialSource{}
	return out
}

func normalizedDeepSeekBalanceURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return deepSeekOfficialBalanceURL
	}
	return raw
}

type legacyDeepSeekModelFields struct {
	contextWindowSet     bool
	contextWindow        int
	maxOutputTokensSet   bool
	maxOutputTokens      int
	priceSet             bool
	price                *provider.Pricing
	reasoningProtocolSet bool
	reasoningProtocol    string
	supportedEffortsSet  bool
	supportedEfforts     []string
	defaultEffortSet     bool
	defaultEffort        string
	visionSet            bool
	vision               bool
}

func legacyDeepSeekModelFieldsCompatible(a, b *ProviderEntry) bool {
	if a == nil || b == nil {
		return a == b
	}
	if left, right := strings.TrimSpace(a.Default), strings.TrimSpace(b.Default); left != "" && right != "" && left != right {
		return false
	}
	models := map[string]string{}
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model != "" {
			models[strings.ToLower(model)] = model
		}
	}
	for _, entry := range []*ProviderEntry{a, b} {
		for _, model := range entry.ModelList() {
			add(model)
		}
		for _, model := range entry.VisionModels {
			add(model)
		}
		for model := range entry.Prices {
			add(model)
		}
		for model := range entry.ModelOverrides {
			add(model)
		}
	}
	for _, model := range models {
		left, leftSet := legacyDeepSeekModelFieldProjection(a, model)
		right, rightSet := legacyDeepSeekModelFieldProjection(b, model)
		if leftSet && rightSet && !legacyDeepSeekModelFieldProjectionsCompatible(left, right) {
			return false
		}
	}
	return true
}

func legacyDeepSeekModelFieldProjection(entry *ProviderEntry, model string) (legacyDeepSeekModelFields, bool) {
	var out legacyDeepSeekModelFields
	if entry == nil {
		return out, false
	}
	listed := entry.HasModel(model)
	if listed {
		out.contextWindowSet = true
		out.contextWindow = entry.ContextWindow
		if out.contextWindow <= 0 {
			out.contextWindow = 1_000_000
		}
		out.maxOutputTokensSet = true
		out.maxOutputTokens = entry.MaxOutputTokens
		out.reasoningProtocolSet = true
		out.reasoningProtocol = strings.TrimSpace(entry.ReasoningProtocol)
		out.supportedEffortsSet = true
		out.supportedEfforts = append([]string(nil), entry.SupportedEfforts...)
		out.defaultEffortSet = true
		out.defaultEffort = strings.TrimSpace(entry.DefaultEffort)
		out.visionSet = true
		out.vision = entry.Vision || entry.HasVisionModel(model)
		if price := entry.PriceForModel(model); price != nil {
			out.priceSet = true
			out.price = price
		}
	}
	if price, ok := pricingForModelKey(entry.Prices, model); ok {
		out.priceSet = true
		out.price = clonePricing(price)
	}
	if override, ok := entry.modelOverrideForModel(model); ok {
		if override.ContextWindow > 0 {
			out.contextWindowSet = true
			out.contextWindow = override.ContextWindow
		}
		if override.MaxOutputTokens != 0 {
			out.maxOutputTokensSet = true
			out.maxOutputTokens = override.MaxOutputTokens
		}
		if strings.TrimSpace(override.ReasoningProtocol) != "" {
			out.reasoningProtocolSet = true
			out.reasoningProtocol = strings.TrimSpace(override.ReasoningProtocol)
		}
		if override.SupportedEfforts != nil {
			out.supportedEffortsSet = true
			out.supportedEfforts = append([]string(nil), override.SupportedEfforts...)
			out.defaultEffortSet = true
			out.defaultEffort = strings.TrimSpace(override.DefaultEffort)
		}
		if override.Vision != nil {
			out.visionSet = true
			out.vision = *override.Vision
		}
	}
	return out, listed || out.contextWindowSet || out.maxOutputTokensSet || out.priceSet ||
		out.reasoningProtocolSet || out.supportedEffortsSet || out.defaultEffortSet || out.visionSet
}

func pricingForModelKey(prices map[string]*provider.Pricing, model string) (*provider.Pricing, bool) {
	for key, price := range prices {
		if strings.EqualFold(strings.TrimSpace(key), strings.TrimSpace(model)) {
			return price, true
		}
	}
	return nil, false
}

func legacyDeepSeekModelFieldProjectionsCompatible(a, b legacyDeepSeekModelFields) bool {
	return (!a.contextWindowSet || !b.contextWindowSet || a.contextWindow == b.contextWindow) &&
		(!a.maxOutputTokensSet || !b.maxOutputTokensSet || a.maxOutputTokens == b.maxOutputTokens) &&
		(!a.priceSet || !b.priceSet || reflect.DeepEqual(a.price, b.price)) &&
		(!a.reasoningProtocolSet || !b.reasoningProtocolSet || a.reasoningProtocol == b.reasoningProtocol) &&
		(!a.supportedEffortsSet || !b.supportedEffortsSet || slices.Equal(a.supportedEfforts, b.supportedEfforts)) &&
		(!a.defaultEffortSet || !b.defaultEffortSet || a.defaultEffort == b.defaultEffort) &&
		(!a.visionSet || !b.visionSet || a.vision == b.vision)
}

func preferredLegacyDeepSeekDefault(entries []*ProviderEntry, models []string, fallback string) string {
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		candidate := strings.TrimSpace(entry.Default)
		if candidate != "" && slices.Contains(models, candidate) {
			return candidate
		}
	}
	return firstKnownModel(fallback, models, "deepseek-v4-flash")
}

func mergeLegacyDeepSeekModelConfiguration(entry, old *ProviderEntry) {
	if entry == nil || old == nil {
		return
	}
	if entry.Prices == nil {
		entry.Prices = map[string]*provider.Pricing{}
	}
	if entry.ModelOverrides == nil {
		entry.ModelOverrides = map[string]ProviderModelOverride{}
	}
	entry.VisionModels = mergeModelLists(entry.VisionModels, old.VisionModels)
	for model, price := range old.Prices {
		entry.Prices[model] = clonePricing(price)
	}
	for _, model := range old.ModelList() {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if price := old.PriceForModel(model); price != nil {
			entry.Prices[model] = price
		}
		override := entry.ModelOverrides[model]
		if old.ContextWindow > 0 && old.ContextWindow != entry.ContextWindow {
			override.ContextWindow = old.ContextWindow
		}
		if old.MaxOutputTokens != entry.MaxOutputTokens {
			override.MaxOutputTokens = old.MaxOutputTokens
		}
		if protocol := strings.TrimSpace(old.ReasoningProtocol); protocol != "" {
			override.ReasoningProtocol = protocol
		}
		if len(old.SupportedEfforts) > 0 {
			override.SupportedEfforts = append([]string(nil), old.SupportedEfforts...)
			override.DefaultEffort = old.DefaultEffort
		}
		if old.Vision || old.HasVisionModel(model) {
			vision := true
			override.Vision = &vision
		}
		if explicit, ok := old.modelOverrideForModel(model); ok {
			mergeProviderModelOverride(&override, explicit)
		}
		entry.ModelOverrides[model] = override
	}
	for model, override := range old.ModelOverrides {
		if old.HasModel(model) {
			continue
		}
		current := entry.ModelOverrides[model]
		mergeProviderModelOverride(&current, override)
		entry.ModelOverrides[model] = current
	}
}

func mergeProviderModelOverride(dst *ProviderModelOverride, src ProviderModelOverride) {
	if dst == nil {
		return
	}
	if strings.TrimSpace(src.ReasoningProtocol) != "" {
		dst.ReasoningProtocol = src.ReasoningProtocol
	}
	if len(src.SupportedEfforts) > 0 {
		dst.SupportedEfforts = append([]string(nil), src.SupportedEfforts...)
		dst.DefaultEffort = src.DefaultEffort
	}
	if src.Vision != nil {
		vision := *src.Vision
		dst.Vision = &vision
	}
	if src.ContextWindow > 0 {
		dst.ContextWindow = src.ContextWindow
	}
	if src.MaxOutputTokens != 0 {
		dst.MaxOutputTokens = src.MaxOutputTokens
	}
}

func mergeModelLists(primary, extra []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(primary))
	for _, list := range [][]string{primary, extra} {
		for _, model := range list {
			model = strings.TrimSpace(model)
			if model == "" || seen[model] {
				continue
			}
			seen[model] = true
			out = append(out, model)
		}
	}
	return out
}

func firstKnownModel(current string, models []string, fallback string) string {
	current = strings.TrimSpace(current)
	if slices.Contains(models, current) {
		return current
	}
	if slices.Contains(models, fallback) {
		return fallback
	}
	if len(models) > 0 {
		return models[0]
	}
	return ""
}

func retargetDesktopOfficialRefs(c *Config, access map[string]bool) {
	c.DefaultModel = retargetDesktopOfficialRef(c.DefaultModel, access)
	c.Agent.PlannerModel = retargetDesktopOfficialRef(c.Agent.PlannerModel, access)
	c.Agent.VisionModel = retargetDesktopOfficialRef(c.Agent.VisionModel, access)
	c.Agent.SubagentModel = retargetDesktopOfficialRef(c.Agent.SubagentModel, access)
	for skill, ref := range c.Agent.SubagentModels {
		c.Agent.SubagentModels[skill] = retargetDesktopOfficialRef(ref, access)
	}
}

func retargetDesktopOfficialRef(ref string, access map[string]bool) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	provider, model, hasModel := strings.Cut(ref, "/")
	switch provider {
	case "deepseek-flash":
		if !access["deepseek"] {
			return ref
		}
		if !hasModel || strings.TrimSpace(model) == "" {
			model = "deepseek-v4-flash"
		}
		return "deepseek/" + model
	case "deepseek-pro":
		if !access["deepseek"] {
			return ref
		}
		if !hasModel || strings.TrimSpace(model) == "" {
			model = "deepseek-v4-pro"
		}
		return "deepseek/" + model
	default:
		return ref
	}
}
