package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"reasonix/internal/extension/protocol"
	"reasonix/internal/fileutil"
	fileencoding "reasonix/internal/fileutil/encoding"
	"reasonix/internal/mcpdiag"
	"reasonix/internal/netclient"
	"reasonix/internal/permission"
)

var validDesktopExternalOpenerID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// edit.go is the programmatic mutation surface a settings UI drives: change the
// default model, add/remove a provider, set the planner, edit permission rules,
// add/remove an MCP server — each validated, then persisted with SaveTo. It is
// separate from the `reasonix setup` wizard (cli) so a GUI can apply one setting at a
// time without replaying the whole interactive flow. Every mutator works on the
// in-memory *Config; nothing writes to disk until SaveTo/Save is called, so a UI
// can stage several changes and commit once. Mutations round-trip through
// RenderTOML → Load (the wizard relies on the same guarantee).

// permission rule list names accepted by the rule mutators.
const (
	listAllow = "allow"
	listAsk   = "ask"
	listDeny  = "deny"

	// CompactRatioMin and CompactRatioMax are the bounds shared by the
	// programmatic config editor and all CLI/Desktop callers.
	CompactRatioMin = 0.30
	CompactRatioMax = 0.85
)

// SetDefaultModel points default_model at an existing model. It accepts both
// forms used by the runtime resolver:
//   - "provider"          — the provider's own default model;
//   - "provider/model"    — that specific model under that provider.
//
// Either is rejected when the target does not exist, so a UI can't strand
// the config on a model that doesn't exist. Plugin-namespaced refs
// (plugin/<plugin>/<provider>/<model>) are the exception: they belong to
// extension sidecars, so the config catalog cannot vouch for them — boot's
// merged resolver gates them at the next launch instead.
func (c *Config) SetDefaultModel(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("set default: empty name")
	}
	if _, ok := c.ResolveModel(name); !ok && protocol.PluginRefOwner(name) == "" {
		return fmt.Errorf("set default: no such model %q (configured: %s)", name, c.providerNames())
	}
	c.DefaultModel = name
	return nil
}

// SetPlannerModel sets (or, with "", clears) agent.planner_model for two-model
// collaboration. A non-empty name must be a configured provider.
func (c *Config) SetPlannerModel(name string) error {
	if name == "" {
		c.Agent.PlannerModel = ""
		return nil
	}
	if _, ok := c.Provider(name); !ok {
		return fmt.Errorf("set planner: no provider %q (configured: %s)", name, c.providerNames())
	}
	c.Agent.PlannerModel = name
	return nil
}

// SetVisionModel sets (or clears) the optional image-understanding fallback.
// "auto" is resolved by the runtime within the active provider; an explicit
// value must be a configured vision-capable model.
func (c *Config) SetVisionModel(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		c.Agent.VisionModel = ""
		return nil
	}
	if strings.EqualFold(name, "auto") {
		c.Agent.VisionModel = "auto"
		return nil
	}
	entry, ok := c.ResolveModel(name)
	if !ok {
		return fmt.Errorf("set vision model: no such model %q (configured: %s)", name, c.providerNames())
	}
	if NewModelCapabilityResolver().Resolve(entry).State != CapabilitySupported {
		return fmt.Errorf("set vision model: %q does not support image input", name)
	}
	if !entry.Configured() {
		return fmt.Errorf("set vision model: provider %q has no key", entry.Name)
	}
	c.Agent.VisionModel = entry.Name + "/" + entry.Model
	return nil
}

// SetAutoPlan is retained for source compatibility with older desktop clients.
// Automatic plan mode is retired: "off" is an idempotent compatibility write,
// while every attempt to enable it is rejected explicitly.
func (c *Config) SetAutoPlan(mode string) error {
	if strings.EqualFold(strings.TrimSpace(mode), "off") {
		c.Agent.AutoPlan = "off"
		c.Agent.AutoPlanClassifier = ""
		return nil
	}
	return fmt.Errorf("automatic plan mode has been retired; use Plan Mode explicitly")
}

// SetDesktopDefaultToolApprovalMode sets the Ask/Auto/YOLO posture used only
// for newly-created desktop sessions.
func (c *Config) SetDesktopDefaultToolApprovalMode(mode string) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "ask":
		c.Desktop.DefaultToolApprovalMode = "ask"
	case "auto":
		c.Desktop.DefaultToolApprovalMode = "auto"
	case "yolo", "full", "full-access", "bypass":
		c.Desktop.DefaultToolApprovalMode = "yolo"
	default:
		return fmt.Errorf("default_tool_approval_mode %q: must be ask|auto|yolo", mode)
	}
	return nil
}

// SetUIShortcutLayout selects the CLI keyboard shortcut layout. "classic" keeps
// historical behavior; "desktop" enables the two-axis desktop-style shortcuts.
func (c *Config) SetUIShortcutLayout(layout string) error {
	switch strings.ToLower(strings.TrimSpace(layout)) {
	case "", "classic", "default", "legacy", "off":
		c.UI.ShortcutLayout = "classic"
	case "desktop", "dual", "dual-axis", "dual_axis":
		c.UI.ShortcutLayout = "desktop"
	default:
		return fmt.Errorf("shortcut_layout %q: must be classic|desktop", layout)
	}
	return nil
}

// UpsertProvider adds e, or replaces an existing provider with the same name
// (preserving its position). Required fields (name, kind, base_url, model/models)
// are validated; whether the kind is actually registered and the key resolves is
// checked later by provider.New / Validate, which give actionable errors.
func (c *Config) UpsertProvider(e ProviderEntry) error {
	normalizeProviderEffortFields(&e)
	if err := validateProvider(e); err != nil {
		return err
	}
	for i := range c.Providers {
		if c.Providers[i].Name == e.Name {
			c.Providers[i] = e
			return nil
		}
	}
	c.Providers = append(c.Providers, e)
	return nil
}

// UpsertProviderPreservingRuntime applies persisted provider fields while
// retaining process-only state derived by the latest config load. It is used
// when replaying an optimistic edit log onto fresh state.
func (c *Config) UpsertProviderPreservingRuntime(e ProviderEntry) error {
	if current, ok := c.Provider(e.Name); ok {
		e.persistedOfficialCurrency = current.persistedOfficialCurrency
		if strings.TrimSpace(current.APIKeyEnv) == strings.TrimSpace(e.APIKeyEnv) {
			e.resolvedAPIKey = current.resolvedAPIKey
			e.resolvedSource = current.resolvedSource
			e.visionOverride = current.visionOverride
		}
	}
	return c.UpsertProvider(e)
}

// ProviderEntryConfigSnapshot strips process-only state from a provider copy so
// optimistic edit logs contain only persisted configuration.
func ProviderEntryConfigSnapshot(entry ProviderEntry) ProviderEntry {
	entry.resolvedAPIKey = ""
	entry.resolvedSource = CredentialSource{}
	entry.visionOverride = nil
	entry.persistedOfficialCurrency = ""
	return entry
}

// ProviderEntriesConfigEqual compares persisted provider configuration while
// ignoring credentials and capability state resolved only for the current
// process. Setup uses it for optimistic conflict detection during replay.
func ProviderEntriesConfigEqual(a, b ProviderEntry) bool {
	return reflect.DeepEqual(ProviderEntryConfigSnapshot(a), ProviderEntryConfigSnapshot(b))
}

// SetProviderEffort updates a provider's provider-specific thinking effort knob.
func (c *Config) SetProviderEffort(name, effort string) error {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			c.Providers[i].Effort = normalizeStoredEffort(effort)
			return nil
		}
	}
	return fmt.Errorf("set provider effort: no provider %q", name)
}

// SetDesktopLanguage pins the desktop UI language. It intentionally does not
// modify Config.Language, which is used by the CLI/model-facing runtime.
func (c *Config) SetDesktopLanguage(lang string) error {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "", "auto":
		c.Desktop.Language = ""
	case "en":
		c.Desktop.Language = "en"
	case "zh":
		c.Desktop.Language = "zh"
	default:
		return fmt.Errorf("desktop language %q: must be auto|en|zh", lang)
	}
	c.ApplyDeepSeekOfficialDefaultPricing()
	return nil
}

// SetDesktopAppearance sets desktop-only theme preferences. It must not affect
// CLI theme settings or provider-visible request data.
func (c *Config) SetDesktopAppearance(theme, style string) error {
	switch strings.ToLower(strings.TrimSpace(theme)) {
	case "auto":
		c.Desktop.Theme = "auto"
	case "light":
		c.Desktop.Theme = "light"
	case "", "dark":
		c.Desktop.Theme = "dark"
	default:
		return fmt.Errorf("desktop theme %q: must be auto|dark|light", theme)
	}
	if strings.TrimSpace(style) == "" {
		c.Desktop.ThemeStyle = ""
		return nil
	}
	normalized := normalizeThemeStyle(style)
	if normalized == "" {
		return fmt.Errorf("desktop theme style %q: must be graphite|aurora|slate|carbon|nocturne|amber", style)
	}
	c.Desktop.ThemeStyle = normalized
	return nil
}

// SetDesktopTerminalTheme sets the integrated terminal colour preference.
// This is desktop-only UI state and never rebuilds or changes model requests.
func (c *Config) SetDesktopTerminalTheme(theme string) error {
	switch strings.ToLower(strings.TrimSpace(theme)) {
	case "", "auto":
		c.Desktop.TerminalTheme = "auto"
	case "dark":
		c.Desktop.TerminalTheme = "dark"
	case "light":
		c.Desktop.TerminalTheme = "light"
	default:
		return fmt.Errorf("desktop terminal theme %q: must be auto|dark|light", theme)
	}
	return nil
}

// SetDesktopLayoutStyle sets the desktop layout style. UI-only; it must not
// affect CLI output or provider-visible request data.
func (c *Config) SetDesktopLayoutStyle(style string) error {
	switch strings.ToLower(strings.TrimSpace(style)) {
	case "classic":
		c.Desktop.LayoutStyle = "classic"
	case "", "workbench", "workspace":
		c.Desktop.LayoutStyle = "workbench"
	case "creation":
		c.Desktop.LayoutStyle = "creation"
	default:
		return fmt.Errorf("desktop layout style %q: must be classic|workbench|creation", style)
	}
	return nil
}

// SetDesktopExternalOpener stores the stable id selected by the desktop Open
// control. Availability is deliberately checked by the native desktop shell,
// because config is shared across operating systems and installations.
func (c *Config) SetDesktopExternalOpener(id string) error {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		c.Desktop.ExternalOpener = ""
		return nil
	}
	if !validDesktopExternalOpenerID.MatchString(id) {
		return fmt.Errorf("external opener %q: invalid id", id)
	}
	c.Desktop.ExternalOpener = id
	return nil
}

// SetDesktopCloseBehavior sets the desktop close-window preference. It is
// intentionally UI-only and must not affect model prompts or provider-visible
// request data.
func (c *Config) SetDesktopCloseBehavior(mode string) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "quit", "exit":
		c.Desktop.CloseBehavior = "quit"
	case "", "background", "hide":
		c.Desktop.CloseBehavior = "background"
	default:
		return fmt.Errorf("close behavior %q: must be quit|background", mode)
	}
	return nil
}

// SetDesktopDisplayMode sets the transcript display mode. UI-only.
func (c *Config) SetDesktopDisplayMode(mode string) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "compact", "minimal":
		c.Desktop.DisplayMode = "compact"
	case "", "standard":
		c.Desktop.DisplayMode = "standard"
	default:
		return fmt.Errorf("display mode %q: must be standard|compact", mode)
	}
	return nil
}

// SetDesktopStatusBarStyle sets the desktop status bar metric label style.
// UI-only; it must not affect CLI output or provider-visible request data.
func (c *Config) SetDesktopStatusBarStyle(style string) error {
	switch strings.ToLower(strings.TrimSpace(style)) {
	case "icon", "icons":
		c.Desktop.StatusBarStyle = "icon"
	case "", "text", "label", "labels":
		c.Desktop.StatusBarStyle = "text"
	default:
		return fmt.Errorf("status bar style %q: must be icon|text", style)
	}
	return nil
}

// SetDesktopStatusBarItems sets the ordered visible desktop status bar items.
// UI-only; it must not affect CLI output or provider-visible request data.
func (c *Config) SetDesktopStatusBarItems(items []string) error {
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, raw := range items {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		if !knownDesktopStatusBarItems[id] {
			return fmt.Errorf("status bar item %q: unknown item", id)
		}
		out = append(out, id)
		seen[id] = true
	}
	if len(out) == 0 {
		out = DefaultDesktopStatusBarItems()
	}
	c.Desktop.StatusBarItems = out
	return nil
}

// SetDesktopCheckUpdates sets whether the desktop app checks for updates on
// startup. Manual checks remain available in Settings regardless of this value.
func (c *Config) SetDesktopCheckUpdates(enabled bool) error {
	c.Desktop.CheckUpdates = &enabled
	return nil
}

// SetDesktopUpdateChannel is retained for pre-single-channel Wails clients.
// Clearing the legacy field keeps the next canonical write channel-free.
func (c *Config) SetDesktopUpdateChannel(_ string) error {
	c.Desktop.UpdateChannel = ""
	return nil
}

// SetCLIUpdateChannel is retained for older CLI scripts. Every recognized
// historical value migrates to the official channel and is omitted on save.
func (c *Config) SetCLIUpdateChannel(channel string) error {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "", "stable", "preview", "canary", "beta", "next":
		c.CLI.UpdateChannel = ""
	default:
		return fmt.Errorf("CLI update channel %q is unsupported; Reasonix now uses the official release channel", channel)
	}
	return nil
}

// SetColdResumePrune toggles auto-elision of stale tool results on cold resume.
func (c *Config) SetColdResumePrune(enabled bool) error {
	c.Agent.ColdResumePrune = &enabled
	return nil
}

// SetCompactRatio updates the sole user-controlled automatic compaction
// threshold. Allowed range is CompactRatioMin–CompactRatioMax; presets are
// 0.70 / 0.80 / 0.85.
func (c *Config) SetCompactRatio(ratio float64) error {
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < CompactRatioMin || ratio > CompactRatioMax {
		return fmt.Errorf("compact ratio %v: must be between %.2f and %.2f", ratio, CompactRatioMin, CompactRatioMax)
	}
	c.Agent.CompactRatio = ratio
	return nil
}

// SetDesktopTelemetry sets whether the desktop sends the anonymous launch ping.
func (c *Config) SetDesktopTelemetry(enabled bool) error {
	c.Desktop.Telemetry = &enabled
	return nil
}

// SetDesktopMetrics sets whether the desktop sends aggregate desktop metrics.
func (c *Config) SetDesktopMetrics(enabled bool) error {
	c.Desktop.Metrics = &enabled
	return nil
}

// SetCLITelemetryMode sets the user-global content-free CLI metrics policy.
func (c *Config) SetCLITelemetryMode(mode string) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "auto":
		c.Telemetry.CLIMetrics = "auto"
	case "on":
		c.Telemetry.CLIMetrics = "on"
	case "off":
		c.Telemetry.CLIMetrics = "off"
	default:
		return fmt.Errorf("cli_metrics %q: must be auto|on|off", mode)
	}
	return nil
}

// SetDesktopConversationWidth sets the max transcript width preference.
// standard = 960px fixed; full = 90% of the parent, with a 960px floor.
// An empty value resets to standard.
func (c *Config) SetDesktopConversationWidth(width string) error {
	switch strings.ToLower(strings.TrimSpace(width)) {
	case "", "standard":
		c.Desktop.ConversationWidth = "standard"
	case "full":
		c.Desktop.ConversationWidth = "full"
	default:
		return fmt.Errorf("conversation width %q: must be standard|full", width)
	}
	return nil
}

// SetUICloseBehavior is kept for callers compiled against the old edit API.
func (c *Config) SetUICloseBehavior(mode string) error {
	return c.SetDesktopCloseBehavior(mode)
}

// SetShowReasoning sets the CLI's default verbose-reasoning preference. When
// true, thinking text is shown in the chat TUI on startup; when false (the
// default), it stays collapsed until the user toggles it with Ctrl+O or
// /verbose.
func (c *Config) SetShowReasoning(on bool) error {
	c.UI.ShowReasoning = on
	return nil
}

// SetProviderThinking updates a provider's provider-specific thinking mode knob.
func (c *Config) SetProviderThinking(name, thinking string) error {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			c.Providers[i].Thinking = strings.ToLower(strings.TrimSpace(thinking))
			return nil
		}
	}
	return fmt.Errorf("set provider thinking: no provider %q", name)
}

// SetNetwork updates ordinary outbound network proxy settings. Invalid custom
// proxy settings are rejected here so the desktop panel cannot save a config that
// would break provider startup.
func (c *Config) SetNetwork(n NetworkConfig) error {
	n.ProxyMode = netclient.NormalizeMode(n.ProxyMode)
	n.ProxyURL = strings.TrimSpace(n.ProxyURL)
	n.NoProxy = strings.TrimSpace(n.NoProxy)
	n.Proxy.Type = strings.ToLower(strings.TrimSpace(n.Proxy.Type))
	n.Proxy.Server = strings.TrimSpace(n.Proxy.Server)
	n.Proxy.Username = strings.TrimSpace(n.Proxy.Username)
	c.Network = n
	return netclient.Validate(c.NetworkProxySpec())
}

// ModelRefsProvider reports whether ref targets the named provider. It matches
// both bare provider names ("deepseek") and "provider/model" refs.
func ModelRefsProvider(ref, name string) bool {
	ref = strings.TrimSpace(ref)
	name = strings.TrimSpace(name)
	if ref == "" || name == "" {
		return false
	}
	if ref == name {
		return true
	}
	prov, _, ok := strings.Cut(ref, "/")
	return ok && prov == name
}

func (c *Config) modelRefTargetsProvider(ref, name string) bool {
	if ModelRefsProvider(ref, name) {
		return true
	}
	if e, ok := c.ResolveModel(ref); ok {
		return e.Name == name
	}
	return false
}

// RemoveProvider deletes the named provider. References to the removed provider
// are migrated to the first remaining configured provider when possible. The
// default model is required, so removal is refused when no fallback exists;
// optional planner/subagent refs are cleared instead of being left dangling.
func (c *Config) RemoveProvider(name string) error {
	name = strings.TrimSpace(name)
	idx := -1
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("remove provider: no provider %q", name)
	}

	defaultRefsProvider := c.modelRefTargetsProvider(c.DefaultModel, name)
	plannerRefsProvider := c.modelRefTargetsProvider(c.Agent.PlannerModel, name)
	subagentRefsProvider := c.modelRefTargetsProvider(c.Agent.SubagentModel, name)
	subagentModelRefsProvider := map[string]bool{}
	for skill, ref := range c.Agent.SubagentModels {
		if c.modelRefTargetsProvider(ref, name) {
			subagentModelRefsProvider[skill] = true
		}
	}

	fallback := ""
	visionRefsProvider := c.modelRefTargetsProvider(c.Agent.VisionModel, name)
	if defaultRefsProvider || plannerRefsProvider || visionRefsProvider || subagentRefsProvider || len(subagentModelRefsProvider) > 0 {
		fallback = c.providerRemovalFallback(name)
	}
	if defaultRefsProvider && fallback == "" {
		return fmt.Errorf("remove provider: %q is referenced by default_model and no other configured provider exists", name)
	}

	c.Providers = append(c.Providers[:idx], c.Providers[idx+1:]...)

	if defaultRefsProvider {
		c.DefaultModel = fallback
	}
	if plannerRefsProvider {
		c.Agent.PlannerModel = fallback
	}
	if visionRefsProvider {
		c.Agent.VisionModel = ""
	}
	if subagentRefsProvider {
		c.Agent.SubagentModel = fallback
	}
	for skill := range subagentModelRefsProvider {
		if fallback != "" {
			c.Agent.SubagentModels[skill] = fallback
		} else {
			delete(c.Agent.SubagentModels, skill)
		}
	}
	return nil
}

func (c *Config) providerRemovalFallback(name string) string {
	for i := range c.Providers {
		p := &c.Providers[i]
		if p.Name == name || !p.Configured() || len(p.ModelList()) == 0 {
			continue
		}
		return p.Name
	}
	return ""
}

// validateProvider checks the fields a provider can't function without.
func validateProvider(e ProviderEntry) error {
	switch {
	case strings.TrimSpace(e.Name) == "":
		return fmt.Errorf("provider: name is required")
	case strings.TrimSpace(e.Kind) == "":
		return fmt.Errorf("provider %q: kind is required", e.Name)
	case strings.TrimSpace(e.BaseURL) == "":
		return fmt.Errorf("provider %q: base_url is required", e.Name)
	case !providerHasAnyModel(e):
		return fmt.Errorf("provider %q: model is required", e.Name)
	case strings.TrimSpace(e.APIKeyEnv) != "" && !IsValidCredentialKey(e.APIKeyEnv):
		return fmt.Errorf("provider %q: api_key_env %q is not a valid environment variable name", e.Name, e.APIKeyEnv)
	}
	return nil
}

func providerHasAnyModel(e ProviderEntry) bool {
	if strings.TrimSpace(e.Model) != "" {
		return true
	}
	for _, m := range e.Models {
		if strings.TrimSpace(m) != "" {
			return true
		}
	}
	return false
}

// SetPermissionMode sets the writer-fallback mode. Accepts "ask", "allow", or
// "deny" (case-insensitive); anything else errors rather than silently
// defaulting, so a UI surfaces a typo instead of installing a surprising mode.
func (c *Config) SetPermissionMode(mode string) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "ask", "allow", "deny":
		c.Permissions.Mode = strings.ToLower(strings.TrimSpace(mode))
		return nil
	default:
		return fmt.Errorf("permission mode %q: must be ask|allow|deny", mode)
	}
}

// AddPermissionRule appends a rule ("ToolName" or "ToolName(glob)") to the
// allow / ask / deny list. The rule is validated with the same parser the gate
// uses, and a duplicate is a no-op so a UI can call it idempotently.
func (c *Config) AddPermissionRule(list, rule string) error {
	target, err := c.ruleList(list)
	if err != nil {
		return err
	}
	rule = strings.TrimSpace(rule)
	if _, ok := permission.ParseRule(rule); !ok {
		return fmt.Errorf("invalid permission rule %q (want \"ToolName\" or \"ToolName(glob)\")", rule)
	}
	if slices.Contains(*target, rule) {
		return nil // already present
	}
	*target = append(*target, rule)
	return nil
}

// RemovePermissionRule drops the first exact match of rule from the named list,
// reporting whether anything was removed.
func (c *Config) RemovePermissionRule(list, rule string) (bool, error) {
	target, err := c.ruleList(list)
	if err != nil {
		return false, err
	}
	rule = strings.TrimSpace(rule)
	for i, existing := range *target {
		if existing == rule {
			*target = append((*target)[:i], (*target)[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

// ruleList returns a pointer to the named rule slice so mutators can append to
// it in place. An unknown list name errors.
func (c *Config) ruleList(list string) (*[]string, error) {
	switch strings.ToLower(strings.TrimSpace(list)) {
	case listAllow:
		return &c.Permissions.Allow, nil
	case listAsk:
		return &c.Permissions.Ask, nil
	case listDeny:
		return &c.Permissions.Deny, nil
	default:
		return nil, fmt.Errorf("unknown permission list %q (want allow|ask|deny)", list)
	}
}

// AddSkillPath appends a custom skill root, deduping by its expanded absolute
// path while preserving the caller's original spelling in the config file.
func (c *Config) AddSkillPath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("skill path: empty path")
	}
	want := CanonicalSkillPath(path)
	c.removeExcludedSkillPath(want)
	for _, existing := range c.Skills.Paths {
		if CanonicalSkillPath(existing) == want {
			return nil
		}
	}
	c.Skills.Paths = append(c.Skills.Paths, path)
	return nil
}

// RemoveSkillPath removes the first custom skill root matching path after
// expansion and path cleaning. It reports whether anything changed.
func (c *Config) RemoveSkillPath(path string) (bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return false, fmt.Errorf("skill path: empty path")
	}
	want := CanonicalSkillPath(path)
	for i, existing := range c.Skills.Paths {
		if CanonicalSkillPath(existing) == want {
			c.Skills.Paths = append(c.Skills.Paths[:i], c.Skills.Paths[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

// RestoreSkillPath removes a pseudo-deleted skill source from excluded_paths.
func (c *Config) RestoreSkillPath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("skill path: empty path")
	}
	want := CanonicalSkillPath(path)
	if want == "" {
		return fmt.Errorf("skill path: empty path")
	}
	c.removeExcludedSkillPath(want)
	return nil
}

// ExcludeSkillPath hides any skill discovery root matching path. This is used by
// UI "remove source" actions for convention roots that are not stored in paths.
func (c *Config) ExcludeSkillPath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("skill path: empty path")
	}
	want := CanonicalSkillPath(path)
	if want == "" {
		return fmt.Errorf("skill path: empty path")
	}
	for _, existing := range c.Skills.ExcludedPaths {
		if CanonicalSkillPath(existing) == want {
			return nil
		}
	}
	c.Skills.ExcludedPaths = append(c.Skills.ExcludedPaths, path)
	return nil
}

// SetSkillPathEnabled enables or disables a skill discovery root without
// deleting its configured path. Disabled roots are recorded in excluded_paths
// and can be restored without asking the user to browse for the folder again.
func (c *Config) SetSkillPathEnabled(path string, enabled bool) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("skill path: empty path")
	}
	want := CanonicalSkillPath(path)
	if want == "" {
		return fmt.Errorf("skill path: empty path")
	}
	if enabled {
		c.removeExcludedSkillPath(want)
		return nil
	}
	return c.ExcludeSkillPath(path)
}

func (c *Config) removeExcludedSkillPath(want string) {
	next := c.Skills.ExcludedPaths[:0]
	for _, existing := range c.Skills.ExcludedPaths {
		if CanonicalSkillPath(existing) != want {
			next = append(next, existing)
		}
	}
	c.Skills.ExcludedPaths = next
}

// SetSkillEnabled persists a per-skill enable/disable preference. Skills are
// enabled by default; disabling records the name, enabling removes it.
func (c *Config) SetSkillEnabled(name string, enabled bool) error {
	name = strings.TrimSpace(name)
	key := SkillNameKey(name)
	if key == "" {
		return fmt.Errorf("skill name %q: use letters, digits, '_', '-', '.', 1-64 chars, starting alphanumeric", name)
	}
	next := c.DisabledSkillNames()
	idx := -1
	for i, existing := range next {
		if SkillNameKey(existing) == key {
			idx = i
			break
		}
	}
	if enabled {
		if idx >= 0 {
			next = append(next[:idx], next[idx+1:]...)
		}
		c.Skills.DisabledSkills = next
		return nil
	}
	if idx < 0 {
		next = append(next, name)
	}
	c.Skills.DisabledSkills = next
	return nil
}

// SetSkillImplicitInvocation controls whether skills are exposed to the model
// for automatic discovery and invocation. Explicit /skill commands remain
// available regardless of this setting.
func (c *Config) SetSkillImplicitInvocation(enabled bool) {
	c.Skills.DisableImplicitInvocation = !enabled
}

// CanonicalSkillPath expands env vars, ~ and relative segments to an absolute
// cleaned path for comparing skill roots. On Windows it folds case so paths that
// differ only in casing dedupe. Use only for comparison, never as stored config.
func CanonicalSkillPath(path string) string {
	path = ExpandVars(strings.TrimSpace(path))
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	} else if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			path = home
		}
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	// Resolve existing paths before cleaning so Windows 8.3 short names and
	// long names compare identically. A missing configured path still falls
	// back to the absolute lexical form used for persistence and diagnostics.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

// UpsertPlugin adds e, or replaces an MCP server with the same name (preserving
// position). The transport-specific required fields are validated: stdio needs
// a command, http/sse need a url.
func (c *Config) UpsertPlugin(e PluginEntry) error {
	e, _ = NormalizePluginCommandLine(e)
	if err := validatePlugin(e); err != nil {
		return err
	}
	for i := range c.Plugins {
		if c.Plugins[i].Name == e.Name {
			c.Plugins[i] = e
			return nil
		}
	}
	c.Plugins = append(c.Plugins, e)
	return nil
}

// RemovePlugin deletes the named MCP server, reporting whether it was present.
func (c *Config) RemovePlugin(name string) bool {
	for i := range c.Plugins {
		if c.Plugins[i].Name == name {
			c.Plugins = append(c.Plugins[:i], c.Plugins[i+1:]...)
			return true
		}
	}
	return false
}

// ClearPluginAuthentication removes locally stored auth-like material for one
// MCP server while keeping the server entry itself. It intentionally leaves
// non-auth config (command, URL host/path, ordinary env/header keys, tier) alone.
func (c *Config) ClearPluginAuthentication(name string) (PluginEntry, bool, error) {
	for i := range c.Plugins {
		if c.Plugins[i].Name != name {
			continue
		}
		headers, env, url, changed := mcpdiag.ClearAuthConfig(c.Plugins[i].Headers, c.Plugins[i].Env, c.Plugins[i].URL)
		c.Plugins[i].Headers = headers
		c.Plugins[i].Env = env
		c.Plugins[i].URL = url
		return c.Plugins[i], changed, nil
	}
	return PluginEntry{}, false, fmt.Errorf("clear plugin authentication: no plugin %q", name)
}

// ClearPluginAuthenticationInSource clears auth material in the file that actually
// owns the MCP server. Load() merges user/project TOML and project .mcp.json into
// one Config, so callers must not mutate that merged view and Save() it back: a
// .mcp.json-only server would otherwise be serialized into reasonix.toml or the
// user config. Source priority mirrors Load(): project TOML, user TOML, then the
// project .mcp.json entry if TOML did not define that server.
func ClearPluginAuthenticationInSource(name string) (PluginEntry, bool, string, error) {
	return ClearPluginAuthenticationInSourceForRoot(".", name)
}

// ClearPluginAuthenticationInSourceForRoot clears auth material in the source
// that owns name for the supplied workspace. The root is explicit so a desktop
// action cannot drift to another project's reasonix.toml or .mcp.json after the
// user switches tabs while the action is waiting on a lifecycle lock.
func ClearPluginAuthenticationInSourceForRoot(root, name string) (PluginEntry, bool, string, error) {
	resolvedRoot := resolveRoot(root)
	projectTOML := "reasonix.toml"
	projectMCPJSON := mcpJSONFile
	if resolvedRoot != "." {
		projectTOML = filepath.Join(resolvedRoot, "reasonix.toml")
		projectMCPJSON = filepath.Join(resolvedRoot, mcpJSONFile)
	}
	lockPaths := append([]string{}, userConfigCandidatePaths()...)
	lockPaths = append(lockPaths, projectTOML, projectMCPJSON)
	if legacy := legacyConfigPath(); strings.TrimSpace(legacy) != "" {
		lockPaths = append(lockPaths, legacy)
	}
	unlock, err := lockConfigFilesEdits(lockPaths...)
	if err != nil {
		return PluginEntry{}, false, "", fmt.Errorf("clear plugin authentication: %w", err)
	}
	defer unlock()

	cfg, err := LoadForRootReadOnly(root)
	if err != nil {
		return PluginEntry{}, false, "", err
	}
	entry, found := pluginEntryByName(cfg.Plugins, strings.TrimSpace(name))
	if !found {
		return PluginEntry{}, false, "", fmt.Errorf("clear plugin authentication: no plugin %q", name)
	}
	path := MCPConfigPathForEntry(root, entry)
	if entry.Source != MCPSourceProjectMCPJSON {
		cfg, err := LoadForEditReadOnlyStrict(path)
		if err != nil {
			return PluginEntry{}, false, path, err
		}
		updated, changed, err := cfg.ClearPluginAuthentication(name)
		if err != nil {
			return PluginEntry{}, false, path, err
		}
		if changed {
			if err := cfg.SaveTo(path); err != nil {
				return PluginEntry{}, false, path, err
			}
		}
		return updated, changed, path, nil
	}
	updated, changed, err := clearMCPJSONAuthentication(path, name)
	if err != nil {
		return PluginEntry{}, false, "", err
	}
	return updated, changed, path, nil
}

func pluginTOMLSourcePathForRoot(root, name string) string {
	projectTOML := "reasonix.toml"
	if resolved := resolveRoot(root); resolved != "." {
		projectTOML = filepath.Join(resolved, "reasonix.toml")
	}
	paths := append([]string{projectTOML}, userConfigCandidatePaths()...)
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		cfg := LoadForEdit(path)
		for _, p := range cfg.Plugins {
			if p.Name == name {
				return path
			}
		}
	}
	return ""
}

// MCPConfigPathForEntry returns the writable config file that owns entry.
// Runtime configuration is merged by name, so callers must use provenance
// instead of saving the merged Config back to whichever file happens to have
// the highest priority.
func MCPConfigPathForEntry(root string, entry PluginEntry) string {
	resolvedRoot := resolveRoot(root)
	projectTOML := "reasonix.toml"
	projectMCPJSON := mcpJSONFile
	if resolvedRoot != "." {
		projectTOML = filepath.Join(resolvedRoot, "reasonix.toml")
		projectMCPJSON = filepath.Join(resolvedRoot, mcpJSONFile)
	}
	switch entry.Source {
	case MCPSourceProjectConfig:
		return projectTOML
	case MCPSourceProjectMCPJSON:
		return projectMCPJSON
	case MCPSourceUserConfig:
		for _, path := range userConfigCandidatePaths() {
			cfg := LoadForEditWithoutCredentials(path)
			if _, ok := pluginEntryByName(cfg.Plugins, entry.Name); ok {
				return path
			}
		}
		return UserConfigPath()
	case MCPSourceLegacyUser:
		return legacyConfigPath()
	case MCPSourcePluginPackage:
		return ""
	}
	if path := pluginTOMLSourcePathForRoot(root, entry.Name); path != "" {
		return path
	}
	if _, found, err := LoadMCPJSONPlugin(projectMCPJSON, entry.Name); err == nil && found {
		return projectMCPJSON
	}
	return UserConfigPath()
}

// UpsertPluginInSourceForRoot writes entry back to its owning scope. New and
// legacy user entries are normalized into the current user-global config;
// project entries remain in their original project file.
func UpsertPluginInSourceForRoot(root string, entry PluginEntry) (string, error) {
	path := MCPConfigPathForEntry(root, entry)
	switch entry.Source {
	case MCPSourceProjectMCPJSON:
		unlock, err := LockConfigFileEdits(path)
		if err != nil {
			return path, err
		}
		defer unlock()
		if _, err := UpsertMCPJSONPlugin(path, entry); err != nil {
			return path, err
		}
		return path, nil
	case MCPSourcePluginPackage:
		return "", fmt.Errorf("MCP server %q is managed by an installed plugin package", entry.Name)
	case MCPSourceProjectConfig:
		// Keep the project path selected above.
	default:
		path = UserConfigPath()
		if strings.TrimSpace(path) == "" {
			return "", fmt.Errorf("cannot resolve user config path")
		}
		entry.Source = MCPSourceUserConfig
	}

	unlock, err := LockConfigFileEdits(path)
	if err != nil {
		return path, err
	}
	defer unlock()
	cfg, err := LoadForEditReadOnlyStrict(path)
	if err != nil {
		return path, err
	}
	if err := cfg.UpsertPlugin(entry); err != nil {
		return path, err
	}
	return path, cfg.SaveTo(path)
}

// InstallUserPluginForRoot persists an explicit global MCP install together
// with its durable activation state. All config sources that can shadow the
// global declaration stay locked through conflict detection, save, activation,
// and any rollback, so a failed activation write cannot remove or overwrite a
// concurrent config update.
func InstallUserPluginForRoot(root string, entry PluginEntry, forceEnable bool) (string, error) {
	entry.Source = MCPSourceUserConfig
	unlock, err := LockConfigFilesEdits(mcpConfigSourcePathsForRoot(root)...)
	if err != nil {
		return "", fmt.Errorf("install MCP server: %w", err)
	}
	defer unlock()

	effective, err := LoadForRootReadOnly(root)
	if err != nil {
		return "", err
	}
	for _, configured := range effective.Plugins {
		if configured.Name != entry.Name {
			continue
		}
		if configured.Source != MCPSourceUserConfig && configured.Source != MCPSourceLegacyUser {
			return "", fmt.Errorf(
				"MCP server %q is already configured by %s; edit or remove that declaration before installing a global server with the same name",
				entry.Name,
				configured.Source,
			)
		}
		break
	}

	path := UserConfigPath()
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("cannot resolve user config path")
	}
	cfg, err := LoadForEditReadOnlyStrict(path)
	if err != nil {
		return path, err
	}
	previous, hadPrevious := pluginEntryByName(cfg.Plugins, entry.Name)
	if err := cfg.UpsertPlugin(entry); err != nil {
		return path, err
	}
	if err := cfg.SaveTo(path); err != nil {
		return path, err
	}

	store := DefaultMCPActivationStore()
	var activationErr error
	if forceEnable {
		activationErr = store.SetServerEnabled(entry, root, true)
	} else {
		activationErr = store.ClearServer(entry, root)
	}
	if activationErr == nil {
		return path, nil
	}

	var restoreErr error
	if hadPrevious {
		restoreErr = cfg.UpsertPlugin(previous)
	} else {
		cfg.RemovePlugin(entry.Name)
	}
	if restoreErr == nil {
		restoreErr = cfg.SaveTo(path)
	}
	if restoreErr != nil {
		restoreErr = fmt.Errorf("restore MCP server config: %w", restoreErr)
	}
	return path, errors.Join(activationErr, restoreErr)
}

// RemovePluginFromSourceForRoot removes exactly the declaration represented by
// entry. Lower-priority same-name declarations are intentionally preserved so
// they can become effective after a project override is removed.
func RemovePluginFromSourceForRoot(root string, entry PluginEntry) (bool, string, error) {
	path := MCPConfigPathForEntry(root, entry)
	if entry.Source == MCPSourcePluginPackage {
		return false, "", fmt.Errorf("MCP server %q is managed by an installed plugin package", entry.Name)
	}
	if strings.TrimSpace(path) == "" {
		return false, "", nil
	}
	unlock, err := LockConfigFileEdits(path)
	if err != nil {
		return false, path, err
	}
	defer unlock()
	return removePluginFromSourceForRootLocked(entry, path)
}

// removePluginFromSourceForRootLocked removes exactly one source declaration
// while the caller holds that source's config edit lock.
func removePluginFromSourceForRootLocked(entry PluginEntry, path string) (bool, string, error) {
	switch entry.Source {
	case MCPSourceProjectMCPJSON:
		removed, err := RemoveMCPJSONPlugin(path, entry.Name)
		return removed, path, err
	case MCPSourcePluginPackage:
		return false, "", fmt.Errorf("MCP server %q is managed by an installed plugin package", entry.Name)
	case MCPSourceLegacyUser:
		edit, changed, err := planLegacyMCPDisable(path, entry.Name)
		if err != nil || !changed {
			return false, path, err
		}
		if err := applyConfigSourceEdits([]configSourceEdit{edit}); err != nil {
			return false, path, err
		}
		return true, path, nil
	}
	cfg, err := LoadForEditReadOnlyStrict(path)
	if err != nil {
		return false, path, err
	}
	if !cfg.RemovePlugin(entry.Name) {
		return false, path, nil
	}
	if err := cfg.SaveTo(path); err != nil {
		return false, path, err
	}
	return true, path, nil
}

// RemovePluginFromEffectiveSourceForRoot removes only the declaration currently
// selected by the project-over-global precedence rules.
func RemovePluginFromEffectiveSourceForRoot(root, name string) (PluginEntry, bool, string, error) {
	unlock, err := LockConfigFilesEdits(mcpConfigSourcePathsForRoot(root)...)
	if err != nil {
		return PluginEntry{}, false, "", fmt.Errorf("remove effective MCP server: %w", err)
	}
	defer unlock()

	cfg, err := LoadForRootReadOnly(root)
	if err != nil {
		return PluginEntry{}, false, "", err
	}
	entry, found := pluginEntryByName(cfg.Plugins, strings.TrimSpace(name))
	if !found {
		return PluginEntry{}, false, "", nil
	}
	path := MCPConfigPathForEntry(root, entry)
	removed, path, err := removePluginFromSourceForRootLocked(entry, path)
	return entry, removed, path, err
}

// mcpConfigSourcePathsForRoot returns every writable source whose precedence can
// decide which declaration is effective. A source-selection operation must lock
// all of them before loading, otherwise another process can add a higher-priority
// declaration after the load and turn a "remove effective" action into a removal
// of a now-shadowed source.
func mcpConfigSourcePathsForRoot(root string) []string {
	resolvedRoot := resolveRoot(root)
	projectTOML := "reasonix.toml"
	projectMCPJSON := mcpJSONFile
	if resolvedRoot != "." {
		projectTOML = filepath.Join(resolvedRoot, "reasonix.toml")
		projectMCPJSON = filepath.Join(resolvedRoot, mcpJSONFile)
	}
	paths := append([]string{}, userConfigCandidatePaths()...)
	paths = append(paths, projectTOML, projectMCPJSON)
	if legacy := legacyConfigPath(); strings.TrimSpace(legacy) != "" {
		paths = append(paths, legacy)
	}
	return paths
}

type configSourceEdit struct {
	path         string
	resolvedPath string
	before       []byte
	perm         os.FileMode
	write        func() error
}

func newConfigSourceEdit(path string, write func() error) (configSourceEdit, error) {
	userOwned := isUserConfigPath(path) || samePath(path, legacyConfigPath())
	resolved, err := resolveConfigAccessPath(path, userOwned)
	if err != nil {
		return configSourceEdit{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return configSourceEdit{}, err
	}
	before, err := os.ReadFile(resolved)
	if err != nil {
		return configSourceEdit{}, err
	}
	return configSourceEdit{
		path:         path,
		resolvedPath: resolved,
		before:       before,
		perm:         info.Mode().Perm(),
		write:        write,
	}, nil
}

func applyConfigSourceEdits(edits []configSourceEdit) error {
	for i := range edits {
		if err := edits[i].write(); err != nil {
			var rollbackErrs []error
			for j := i; j >= 0; j-- {
				if rollbackErr := fileutil.AtomicWriteFile(edits[j].resolvedPath, edits[j].before, edits[j].perm); rollbackErr != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("restore %s: %w", edits[j].path, rollbackErr))
				}
			}
			if rollbackErr := errors.Join(rollbackErrs...); rollbackErr != nil {
				return errors.Join(err, fmt.Errorf("roll back MCP config removal: %w", rollbackErr))
			}
			return err
		}
	}
	return nil
}

func planTOMLPluginRemoval(path, name string) (configSourceEdit, bool, error) {
	_, exists, err := statConfigPath(path)
	if err != nil {
		return configSourceEdit{}, false, err
	}
	if !exists {
		return configSourceEdit{}, false, nil
	}
	cfg := Default()
	if err := mergeFile(cfg, path); err != nil {
		return configSourceEdit{}, false, err
	}
	normalizeConfigForEdit(cfg)
	if !cfg.RemovePlugin(name) {
		return configSourceEdit{}, false, nil
	}
	edit, err := newConfigSourceEdit(path, func() error { return cfg.SaveTo(path) })
	return edit, err == nil, err
}

func planMCPJSONPluginRemoval(path, name string) (configSourceEdit, bool, error) {
	resolved, exists, err := statConfigPath(path)
	if err != nil {
		return configSourceEdit{}, false, err
	}
	if !exists {
		return configSourceEdit{}, false, nil
	}
	root, servers, err := readMCPJSONRaw(resolved)
	if err != nil {
		return configSourceEdit{}, false, err
	}
	if _, ok := servers[name]; !ok {
		return configSourceEdit{}, false, nil
	}
	delete(servers, name)
	edit, err := newConfigSourceEdit(path, func() error { return writeMCPJSONServers(resolved, root, servers) })
	return edit, err == nil, err
}

func planLegacyMCPDisable(path, name string) (configSourceEdit, bool, error) {
	if strings.TrimSpace(path) == "" {
		return configSourceEdit{}, false, nil
	}
	resolved, err := resolveConfigAccessPath(path, true)
	if err != nil {
		return configSourceEdit{}, false, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return configSourceEdit{}, false, nil
		}
		return configSourceEdit{}, false, err
	}
	data, err := fileencoding.ReadFileUTF8(resolved)
	if err != nil {
		return configSourceEdit{}, false, err
	}
	var root map[string]json.RawMessage
	var view struct {
		MCP         []string                   `json:"mcp"`
		MCPServers  map[string]json.RawMessage `json:"mcpServers"`
		MCPDisabled []string                   `json:"mcpDisabled"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return configSourceEdit{}, false, nil
	}
	if err := json.Unmarshal(data, &view); err != nil {
		return configSourceEdit{}, false, nil
	}

	foundNamed := false
	changed := false
	filtered := make([]string, 0, len(view.MCP))
	for i, raw := range view.MCP {
		entry, ok := parseLegacyMCPSpec(raw)
		if !ok {
			filtered = append(filtered, raw)
			continue
		}
		effectiveName := entry.Name
		if effectiveName == "" {
			effectiveName = anonymousMCPName(i)
		}
		if effectiveName != name {
			filtered = append(filtered, raw)
			continue
		}
		if entry.Name == "" {
			changed = true
			continue
		}
		foundNamed = true
		filtered = append(filtered, raw)
	}
	if _, ok := view.MCPServers[name]; ok {
		foundNamed = true
	}
	if foundNamed && !containsString(view.MCPDisabled, name) {
		view.MCPDisabled = append(view.MCPDisabled, name)
		changed = true
	}
	if !changed {
		return configSourceEdit{}, false, nil
	}
	if len(filtered) != len(view.MCP) {
		raw, marshalErr := json.Marshal(filtered)
		if marshalErr != nil {
			return configSourceEdit{}, false, marshalErr
		}
		root["mcp"] = raw
	}
	disabledRaw, err := json.Marshal(view.MCPDisabled)
	if err != nil {
		return configSourceEdit{}, false, err
	}
	root["mcpDisabled"] = disabledRaw
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return configSourceEdit{}, false, err
	}
	out = append(out, '\n')
	edit, err := newConfigSourceEdit(path, func() error {
		return fileutil.AtomicWriteFile(resolved, out, info.Mode().Perm())
	})
	return edit, err == nil, err
}

// RemovePluginFromSourcesForRoot removes an MCP server from every writable
// config source that can contribute it for root. Removing all matching TOML
// declarations prevents a lower-priority duplicate from reappearing after the
// higher-priority entry is deleted. Every edit is planned before the first write,
// and legacy JSON receives a disable marker for older Reasonix versions.
func RemovePluginFromSourcesForRoot(root, name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, fmt.Errorf("remove MCP server: name is required")
	}

	userPaths := userConfigCandidatePaths()
	resolvedRoot := resolveRoot(root)
	projectTOML := "reasonix.toml"
	if resolvedRoot != "." {
		projectTOML = filepath.Join(resolvedRoot, "reasonix.toml")
	}
	isUserPath := false
	for _, path := range userPaths {
		if samePath(path, projectTOML) {
			isUserPath = true
			break
		}
	}
	mcpPath := mcpJSONFile
	if resolvedRoot != "." {
		mcpPath = filepath.Join(resolvedRoot, mcpJSONFile)
	}
	legacyPath := legacyConfigPath()
	lockPaths := append([]string{}, userPaths...)
	if !isUserPath {
		lockPaths = append(lockPaths, projectTOML)
	}
	lockPaths = append(lockPaths, mcpPath)
	if legacyPath != "" {
		lockPaths = append(lockPaths, legacyPath)
	}
	unlock, err := lockConfigFilesEdits(lockPaths...)
	if err != nil {
		return false, fmt.Errorf("remove MCP server: %w", err)
	}
	defer unlock()

	var edits []configSourceEdit
	planTOML := func(path string) error {
		edit, changed, err := planTOMLPluginRemoval(path, name)
		if err != nil {
			return err
		}
		if changed {
			edits = append(edits, edit)
		}
		return nil
	}
	for _, path := range userPaths {
		if err := planTOML(path); err != nil {
			return false, err
		}
	}
	if !isUserPath {
		if err := planTOML(projectTOML); err != nil {
			return false, err
		}
	}

	mcpEdit, changed, err := planMCPJSONPluginRemoval(mcpPath, name)
	if err != nil {
		return false, err
	}
	if changed {
		edits = append(edits, mcpEdit)
	}
	legacyEdit, changed, err := planLegacyMCPDisable(legacyPath, name)
	if err != nil {
		return false, err
	}
	if changed {
		edits = append(edits, legacyEdit)
	}
	if len(edits) == 0 {
		return false, nil
	}
	if err := applyConfigSourceEdits(edits); err != nil {
		return false, err
	}
	return true, nil
}

// validatePlugin checks a plugin entry by transport. An empty Type means stdio.
func validatePlugin(e PluginEntry) error {
	if strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("plugin: name is required")
	}
	if e.StartupTimeoutSeconds < 0 {
		return fmt.Errorf("plugin %q: startup_timeout_seconds must be >= 0", e.Name)
	}
	if e.CallTimeoutSeconds < 0 {
		return fmt.Errorf("plugin %q: call_timeout_seconds must be >= 0", e.Name)
	}
	for name, sec := range e.ToolTimeoutSeconds {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("plugin %q: tool_timeout_seconds contains an empty tool name", e.Name)
		}
		if sec < 0 {
			return fmt.Errorf("plugin %q: tool_timeout_seconds[%q] must be >= 0", e.Name, name)
		}
	}
	switch strings.ToLower(strings.TrimSpace(e.Type)) {
	case "", "stdio":
		if strings.TrimSpace(e.Command) == "" {
			return fmt.Errorf("plugin %q: command is required for a stdio server", e.Name)
		}
	case "http", "sse", "streamable-http":
		if strings.TrimSpace(e.URL) == "" {
			return fmt.Errorf("plugin %q: url is required for a %s server", e.Name, e.Type)
		}
	default:
		return fmt.Errorf("plugin %q: unknown type %q (want stdio|http|sse)", e.Name, e.Type)
	}
	return nil
}

// SaveTo writes the configuration to path as annotated TOML, atomically: it
// writes a sibling temp file then renames, so a crash mid-write can't leave a
// half-written reasonix.toml that fails to parse on next load. Parent directories
// are created as needed.
//
// For project configs (./reasonix.toml) the write is incremental: only sections
// and fields that differ from built-in defaults are written, so the file never
// accumulates fields that override the user's global config. User configs still
// write the full annotated template since they are the user's own settings store.
func (c *Config) SaveTo(path string) error {
	if c == nil {
		return fmt.Errorf("save config: nil config")
	}
	if c.editLoadErr != nil {
		return fmt.Errorf("save config loaded from %q: %w", path, c.editLoadErr)
	}
	scope := renderScopeForPath(path)
	if scope == RenderScopeUser {
		if err := currentUserConfigEditLockError(); err != nil {
			return fmt.Errorf("save user config: %w", err)
		}
	}
	resolved, err := resolveConfigAccessPath(path, scope == RenderScopeUser)
	if err != nil {
		return err
	}
	if scope == RenderScopeProject {
		return c.saveProjectIncrementalResolved(path, resolved)
	}
	return writeConfigFileResolved(resolved, RenderTOMLForScope(c, scope), configFilePerm(path))
}

func (c *Config) SaveToScope(path string, scope RenderScope) error {
	if c == nil {
		return fmt.Errorf("save config: nil config")
	}
	if c.editLoadErr != nil {
		return fmt.Errorf("save config loaded from %q: %w", path, c.editLoadErr)
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("save: empty config path")
	}
	userConfig := scope == RenderScopeUser || (scope == RenderScopeFull && isUserConfigPath(path))
	if userConfig {
		if err := currentUserConfigEditLockError(); err != nil {
			return fmt.Errorf("save user config: %w", err)
		}
	}
	resolved, err := resolveConfigAccessPath(path, userConfig)
	if err != nil {
		return err
	}
	return writeConfigFileResolved(resolved, RenderTOMLForScope(c, scope), configFilePerm(path))
}

func (c *Config) saveProjectIncrementalResolved(logicalPath, resolvedPath string) error {
	raw, err := fileencoding.ReadFileUTF8(resolvedPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		raw = nil
	}

	body := string(raw)
	isNew := body == ""

	if isNew {
		return writeConfigFileResolved(resolvedPath, RenderTOMLForScope(c, RenderScopeProject), configFilePerm(logicalPath))
	}

	delta := RenderTOMLProjectDelta(c)
	if tomlBodyHasTopLevelKey(body, "config_version") && !tomlBodyHasTopLevelKey(delta, "config_version") {
		delta = fmt.Sprintf("config_version = %d\n", configVersion(c)) + delta
	}
	removePlugins := len(tomlPluginsForScope(c.Plugins, RenderScopeProject)) == 0 && tomlBodyHasSection(body, "plugins")
	removeSandboxBash := shouldRemoveIneffectiveProjectSandboxBash(body, c)
	removeSkills := projectSkillsKeysToRemove(body, c)
	_, hasLegacyDesktopAutoGuard := tomlSectionKeyValue(body, "desktop", "default_auto_recovery_checkpoint")
	_, hasRetiredAgentAutoGuard := tomlSectionKeyValue(body, "agent", "auto_recovery_checkpoint")
	removeRetiredAutoGuard := hasLegacyDesktopAutoGuard || hasRetiredAgentAutoGuard
	writeProviderAccess := c.Desktop.ProviderAccess != nil
	if strings.TrimSpace(delta) == "" && !removePlugins && !removeSandboxBash && !removeSkills && !removeRetiredAutoGuard && !writeProviderAccess {
		return nil // no changes to write
	}

	// Parse delta into section blocks and merge each into body
	if strings.TrimSpace(delta) != "" {
		body = mergeTOMLDelta(body, delta)
	}
	if removePlugins {
		body = removeTOMLSection(body, "plugins")
	}
	if removeSandboxBash {
		body = removeTOMLSectionKey(body, "sandbox", "bash")
	}
	if removeSkills {
		body = cleanupProjectSkillsKeys(body, c)
	}
	if removeRetiredAutoGuard {
		body = removeTOMLSectionKey(body, "desktop", "default_auto_recovery_checkpoint")
		body = removeTOMLSectionKey(body, "agent", "auto_recovery_checkpoint")
	}
	if writeProviderAccess {
		body = upsertTOMLSectionKey(body, "desktop", "provider_access", "provider_access = "+renderStringArray(c.Desktop.ProviderAccess))
	}
	return writeConfigFileResolved(resolvedPath, body, configFilePerm(logicalPath))
}

// projectSkillsKeysToRemove reports whether an existing project [skills]
// section contains a field whose current edit value is the built-in default.
// Project saves are incremental, so an empty RenderTOMLProjectDelta cannot
// remove a stale override without this explicit cleanup pass.
func projectSkillsKeysToRemove(body string, c *Config) bool {
	if c == nil || !tomlBodyHasSection(body, "skills") {
		return false
	}
	for _, key := range projectSkillKeys {
		if projectSkillKeyIsDefault(c, key) {
			if _, ok := tomlSectionKeyValue(body, "skills", key); ok {
				return true
			}
		}
	}
	return false
}

var projectSkillKeys = [...]string{"paths", "excluded_paths", "disabled_skills", "disable_implicit_invocation", "max_depth"}

func projectSkillKeyIsDefault(c *Config, key string) bool {
	if c != nil && c.keepsProjectSkillKey(key) {
		return false
	}
	switch key {
	case "paths":
		return len(c.Skills.Paths) == 0
	case "excluded_paths":
		return len(c.Skills.ExcludedPaths) == 0
	case "disabled_skills":
		return len(c.Skills.DisabledSkills) == 0
	case "disable_implicit_invocation":
		return !c.Skills.DisableImplicitInvocation
	case "max_depth":
		return c.Skills.MaxDepth == 0
	default:
		return false
	}
}

func cleanupProjectSkillsKeys(body string, c *Config) string {
	if c == nil {
		return body
	}
	for _, key := range projectSkillKeys {
		if projectSkillKeyIsDefault(c, key) {
			body = removeTOMLSectionKey(body, "skills", key)
		}
	}
	return body
}

func shouldRemoveIneffectiveProjectSandboxBash(body string, c *Config) bool {
	if c == nil || runtimeGOOS != "windows" {
		return false
	}
	if c.BashMode() != "off" {
		return false
	}
	value, ok := tomlSectionKeyValue(body, "sandbox", "bash")
	return ok && tomlStringLiteralEquals(value, "enforce")
}

// mergeTOMLDelta parses delta into named TOML blocks and merges each into body
// via replaceTOMLSection. Consecutive array-of-tables entries ([[plugins]],
// [[providers]]) with the same name are merged into a single block so the
// replacement doesn't lose entries.
func mergeTOMLDelta(body, delta string) string {
	lines := strings.Split(delta, "\n")
	type section struct {
		name    string
		content string
		isArray bool
	}
	var topLevel strings.Builder
	var sections []section
	var curName string
	var curBuf strings.Builder
	curIsArray := false

	flush := func() {
		if curName == "" {
			return
		}
		content := curBuf.String()
		if curIsArray && len(sections) > 0 && sections[len(sections)-1].isArray && sections[len(sections)-1].name == curName {
			sections[len(sections)-1].content += content
		} else {
			sections = append(sections, section{curName, content, curIsArray})
		}
		curBuf.Reset()
	}

	for _, line := range lines {
		if name, isArray, ok := tomlEditSectionHeader(line); ok {
			flush()
			curName = name
			curIsArray = isArray
			curBuf.WriteString(line + "\n")
			continue
		}
		if curName != "" {
			curBuf.WriteString(line + "\n")
			continue
		}
		if strings.TrimSpace(line) != "" {
			topLevel.WriteString(line + "\n")
		}
	}
	flush()

	if top := strings.TrimSpace(topLevel.String()); top != "" {
		body = mergeTOMLTopLevelFields(body, top+"\n")
	}
	for _, s := range sections {
		body = replaceTOMLSection(body, s.name, s.content)
	}
	return body
}

func mergeTOMLTopLevelFields(body, fields string) string {
	for line := range strings.SplitSeq(fields, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, ok := tomlTopLevelKey(line)
		if !ok {
			continue
		}
		body = replaceTOMLTopLevelField(body, key, line+"\n")
	}
	return body
}

// SaveMinimalProjectReasoningLanguage writes a new project config that only
// overrides [agent].reasoning_language.
func SaveMinimalProjectReasoningLanguage(path, lang string) (string, error) {
	cfg := Default()
	if err := cfg.SetReasoningLanguage(lang); err != nil {
		return "", err
	}
	body := fmt.Sprintf(`# Reasonix project configuration.
# Project-local overrides are merged over the user config.

[agent]
reasoning_language = %q
`, cfg.ReasoningLanguage())
	return cfg.ReasoningLanguage(), writeConfigFile(path, body)
}

// SaveMinimalProjectCompactRatio writes a new project config that only
// overrides [agent].compact_ratio.
func SaveMinimalProjectCompactRatio(path string, ratio float64) (float64, error) {
	cfg := Default()
	if err := cfg.SetCompactRatio(ratio); err != nil {
		return 0, err
	}
	body := fmt.Sprintf(`# Reasonix project configuration.
# Project-local overrides are merged over the user config.

[agent]
compact_ratio = %s
`, formatFloat(cfg.Agent.CompactRatio))
	return cfg.Agent.CompactRatio, writeConfigFile(path, body)
}

func writeConfigFile(path, body string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("save: empty config path")
	}
	return atomicWriteToConfigFile(path, body, configFilePerm(path))
}

func writeConfigFileResolved(path, body string, perm os.FileMode) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("save: empty config path")
	}
	return fileutil.AtomicWriteFile(path, []byte(body), perm)
}

// atomicWriteToConfigFile resolves the path once and writes only the validated
// final target. This preserves valid links and fails closed for broken user
// links or project links that escape their project root.
func atomicWriteToConfigFile(path, body string, perm os.FileMode) error {
	resolved, err := resolveConfigReadPath(path)
	if err != nil {
		return err
	}
	if err := fileutil.AtomicWriteFile(resolved, []byte(body), perm); err != nil {
		return fmt.Errorf("write symlink target %q: %w", resolved, err)
	}
	return nil
}

func configFilePerm(path string) os.FileMode {
	if isUserConfigPath(path) {
		return 0o600
	}
	return 0o644
}

// WritePermissionsAllow updates only permissions.allow in a TOML file. All
// other permission policy fields and unrelated content remain byte-for-byte
// unchanged. Callers must validate and lock the latest file across their full
// read-modify-write transaction before calling this function.
func WritePermissionsAllow(path string, allow []string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("write permissions: empty config path")
	}

	resolved, exists, err := statConfigPath(path)
	if err != nil {
		return err
	}
	var raw []byte
	if exists {
		raw, err = fileencoding.ReadFileUTF8(resolved)
		if err != nil {
			return err
		}
	} else {
		raw = nil
	}

	body := string(raw)
	if body == "" {
		body = fmt.Sprintf("[permissions]\nallow = %s\n", renderStringArray(allow))
	} else {
		body = upsertTOMLSectionKey(body, "permissions", "allow", "allow = "+renderStringArray(allow))
	}

	var candidate Config
	if _, err := toml.Decode(body, &candidate); err != nil {
		return fmt.Errorf("write permissions: validate updated config: %w", err)
	}
	if !slices.Equal(candidate.Permissions.Allow, allow) {
		return fmt.Errorf("write permissions: validate updated allow: got %v, want %v", candidate.Permissions.Allow, allow)
	}
	return writeConfigFileResolved(resolved, body, configFilePerm(path))
}

// replaceTOMLSection replaces the content of a named TOML section (including
// its header line) with newContent. It handles both [section] and [[section]]
// array-of-tables headers. If the section doesn't exist, newContent is appended
// at the end.
func replaceTOMLSection(body, sectionName, newContent string) string {
	spans := tomlLineSpans(body)
	structural := tomlStructuralLineMask(spans)
	arrayIdx := -1
	for i, span := range spans {
		if !structural[i] {
			continue
		}
		name, isArray, ok := tomlEditSectionHeader(span.text)
		if ok && isArray && name == sectionName {
			arrayIdx = i
			break
		}
	}
	if arrayIdx >= 0 {
		start := spans[arrayIdx].start
		end := len(body)
		for i := arrayIdx + 1; i < len(spans); i++ {
			if !structural[i] {
				continue
			}
			name, isArray, ok := tomlEditSectionHeader(spans[i].text)
			if !ok {
				continue
			}
			if (isArray && name == sectionName) || strings.HasPrefix(name, sectionName+".") {
				continue
			}
			end = spans[i].start
			break
		}
		return body[:start] + strings.TrimRight(newContent, "\n") + "\n" + body[end:]
	}

	for i, span := range spans {
		if !structural[i] {
			continue
		}
		name, isArray, ok := tomlEditSectionHeader(span.text)
		if !ok || isArray || name != sectionName {
			continue
		}
		end := len(body)
		for nextIdx, next := range spans {
			if !structural[nextIdx] {
				continue
			}
			if next.start <= span.start {
				continue
			}
			if _, _, ok := tomlEditSectionHeader(next.text); ok {
				end = next.start
				break
			}
		}
		return body[:span.start] + newContent + body[end:]
	}
	return strings.TrimRight(body, "\n") + "\n\n" + newContent
}

func upsertTOMLSectionKey(body, sectionName, key, line string) string {
	line = strings.TrimRight(line, "\r\n") + "\n"
	spans := tomlLineSpans(body)
	structural := tomlStructuralLineMask(spans)
	sectionIdx := -1
	sectionEnd := len(body)
	for i, span := range spans {
		if !structural[i] {
			continue
		}
		name, isArray, ok := tomlEditSectionHeader(span.text)
		if ok {
			if sectionIdx >= 0 {
				sectionEnd = span.start
				break
			}
			if !isArray && name == sectionName {
				sectionIdx = i
			}
			continue
		}
		if sectionIdx >= 0 {
			if got, _, ok := tomlKeyValue(span.text); ok && got == key {
				endIdx := tomlValueEndSpan(spans, i)
				end := spans[endIdx].end
				if endIdx > i {
					if comments := tomlCommentsInSpans(spans, i, endIdx); len(comments) > 0 {
						line = strings.Join(comments, "\n") + "\n" + line
					}
				} else if comment := tomlInlineComment(spans[endIdx].text); comment != "" {
					line = strings.TrimRight(line, "\r\n") + " " + comment + "\n"
				}
				return body[:span.start] + line + body[end:]
			}
		}
	}
	if sectionIdx < 0 {
		block := fmt.Sprintf("[%s]\n%s", sectionName, line)
		return replaceTOMLSection(body, sectionName, block)
	}
	prefix := body[:sectionEnd]
	if prefix != "" && !strings.HasSuffix(prefix, "\n") {
		prefix += "\n"
	}
	return prefix + line + body[sectionEnd:]
}

type tomlLexState struct {
	stringKind tomlStringKind
	escaped    bool
}

type tomlStringKind uint8

const (
	tomlStringNone tomlStringKind = iota
	tomlStringBasic
	tomlStringLiteral
	tomlStringMultilineBasic
	tomlStringMultilineLiteral
)

func (s tomlLexState) inMultilineString() bool {
	return s.stringKind == tomlStringMultilineBasic || s.stringKind == tomlStringMultilineLiteral
}

func scanTOMLLine(line string, state *tomlLexState, outsideString func(byte)) int {
	for i := 0; i < len(line); {
		ch := line[i]
		switch state.stringKind {
		case tomlStringBasic:
			if state.escaped {
				state.escaped = false
				i++
				continue
			}
			switch ch {
			case '\\':
				state.escaped = true
			case '"':
				state.stringKind = tomlStringNone
			}
			i++
			continue
		case tomlStringLiteral:
			if ch == '\'' {
				state.stringKind = tomlStringNone
			}
			i++
			continue
		case tomlStringMultilineBasic:
			if state.escaped {
				state.escaped = false
				i++
				continue
			}
			if ch == '\\' {
				state.escaped = true
				i++
				continue
			}
			if ch == '"' {
				run := tomlQuoteRun(line, i, '"')
				if run >= 3 {
					state.stringKind = tomlStringNone
				}
				i += run
				continue
			}
			i++
			continue
		case tomlStringMultilineLiteral:
			if ch == '\'' {
				run := tomlQuoteRun(line, i, '\'')
				if run >= 3 {
					state.stringKind = tomlStringNone
				}
				i += run
				continue
			}
			i++
			continue
		}

		switch ch {
		case '#':
			return i
		case '"':
			run := tomlQuoteRun(line, i, '"')
			switch {
			case run == 1:
				state.stringKind = tomlStringBasic
			case run >= 3 && run < 6:
				state.stringKind = tomlStringMultilineBasic
			}
			i += run
			continue
		case '\'':
			run := tomlQuoteRun(line, i, '\'')
			switch {
			case run == 1:
				state.stringKind = tomlStringLiteral
			case run >= 3 && run < 6:
				state.stringKind = tomlStringMultilineLiteral
			}
			i += run
			continue
		default:
			if outsideString != nil {
				outsideString(ch)
			}
		}
		i++
	}
	return -1
}

func tomlQuoteRun(line string, start int, quote byte) int {
	end := start
	for end < len(line) && line[end] == quote {
		end++
	}
	return end - start
}

func tomlStructuralLineMask(spans []tomlLineSpan) []bool {
	structural := make([]bool, len(spans))
	state := tomlLexState{}
	for i, span := range spans {
		structural[i] = !state.inMultilineString()
		scanTOMLLine(span.text, &state, nil)
	}
	return structural
}

func tomlValueEndSpan(spans []tomlLineSpan, start int) int {
	if start < 0 || start >= len(spans) {
		return start
	}
	_, value, ok := tomlKeyValue(spans[start].text)
	if !ok || !strings.HasPrefix(strings.TrimSpace(value), "[") {
		return start
	}
	depth := 0
	seenArray := false
	state := tomlLexState{}
	for i := start; i < len(spans); i++ {
		closed := false
		scanTOMLLine(spans[i].text, &state, func(ch byte) {
			switch ch {
			case '[':
				seenArray = true
				depth++
			case ']':
				if seenArray {
					depth--
					closed = depth == 0
				}
			}
		})
		if closed {
			return i
		}
	}
	return start
}

func tomlInlineComment(line string) string {
	state := tomlLexState{}
	if i := scanTOMLLine(line, &state, nil); i >= 0 {
		return strings.TrimRight(line[i:], "\r\n")
	}
	return ""
}

func tomlCommentsInSpans(spans []tomlLineSpan, start, end int) []string {
	state := tomlLexState{}
	var comments []string
	for i := start; i <= end; i++ {
		line := spans[i].text
		commentAt := scanTOMLLine(line, &state, nil)
		if commentAt < 0 {
			continue
		}
		indentEnd := 0
		for indentEnd < len(line) && (line[indentEnd] == ' ' || line[indentEnd] == '\t') {
			indentEnd++
		}
		comment := strings.TrimRight(line[commentAt:], "\r\n")
		comments = append(comments, line[:indentEnd]+comment)
	}
	return comments
}

func removeTOMLSection(body, sectionName string) string {
	spans := tomlLineSpans(body)
	for i, span := range spans {
		name, isArray, ok := tomlEditSectionHeader(span.text)
		if !ok || name != sectionName {
			continue
		}
		end := len(body)
		for j := i + 1; j < len(spans); j++ {
			nextName, nextIsArray, ok := tomlEditSectionHeader(spans[j].text)
			if !ok {
				continue
			}
			if (isArray && nextIsArray && nextName == sectionName) || strings.HasPrefix(nextName, sectionName+".") {
				continue
			}
			end = spans[j].start
			break
		}
		return strings.TrimRight(body[:span.start], "\n") + "\n" + body[end:]
	}
	return body
}

func removeTOMLSectionKey(body, sectionName, key string) string {
	spans := tomlLineSpans(body)
	sectionIdx := -1
	keyIdx := -1
	endIdx := len(spans)
	for i, span := range spans {
		name, isArray, ok := tomlEditSectionHeader(span.text)
		if ok {
			if sectionIdx >= 0 {
				endIdx = i
				break
			}
			if !isArray && name == sectionName {
				sectionIdx = i
			}
			continue
		}
		if sectionIdx >= 0 && keyIdx < 0 {
			if got, _, ok := tomlKeyValue(span.text); ok && got == key {
				keyIdx = i
			}
		}
	}
	if sectionIdx < 0 || keyIdx < 0 {
		return body
	}
	keyEndIdx := tomlValueEndSpan(spans, keyIdx)
	for i := sectionIdx + 1; i < endIdx; i++ {
		if i >= keyIdx && i <= keyEndIdx {
			continue
		}
		trimmed := strings.TrimSpace(spans[i].text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return body[:spans[keyIdx].start] + body[spans[keyEndIdx].end:]
	}
	sectionStart := spans[sectionIdx].start
	sectionEnd := len(body)
	if endIdx < len(spans) {
		sectionEnd = spans[endIdx].start
	}
	return strings.TrimRight(body[:sectionStart], "\n") + "\n" + body[sectionEnd:]
}

type tomlLineSpan struct {
	start int
	end   int
	text  string
}

func tomlLineSpans(body string) []tomlLineSpan {
	if body == "" {
		return nil
	}
	var spans []tomlLineSpan
	for start := 0; start < len(body); {
		end := len(body)
		if idx := strings.IndexByte(body[start:], '\n'); idx >= 0 {
			end = start + idx + 1
		}
		spans = append(spans, tomlLineSpan{start: start, end: end, text: body[start:end]})
		start = end
	}
	return spans
}

func tomlEditSectionHeader(line string) (string, bool, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", false, false
	}
	if before, _, ok := strings.Cut(trimmed, "#"); ok {
		trimmed = strings.TrimSpace(before)
	}
	if strings.HasPrefix(trimmed, "[[") && strings.HasSuffix(trimmed, "]]") {
		name := strings.TrimSpace(trimmed[2 : len(trimmed)-2])
		return name, true, name != ""
	}
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		name := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
		return name, false, name != ""
	}
	return "", false, false
}

func replaceTOMLTopLevelField(body, key, newLine string) string {
	spans := tomlLineSpans(body)
	insertAt := len(body)
	for _, span := range spans {
		if _, _, ok := tomlEditSectionHeader(span.text); ok {
			insertAt = span.start
			break
		}
		if got, ok := tomlTopLevelKey(span.text); ok && got == key {
			return body[:span.start] + newLine + body[span.end:]
		}
	}
	return body[:insertAt] + newLine + body[insertAt:]
}

func tomlTopLevelKey(line string) (string, bool) {
	key, _, ok := tomlKeyValue(line)
	return key, ok
}

func tomlKeyValue(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	if before, _, ok := strings.Cut(trimmed, "#"); ok {
		trimmed = strings.TrimSpace(before)
	}
	key, value, ok := strings.Cut(trimmed, "=")
	if !ok {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	if key == "" || strings.Contains(key, ".") {
		return "", "", false
	}
	return key, strings.TrimSpace(value), true
}

func tomlSectionKeyValue(body, sectionName, key string) (string, bool) {
	inSection := false
	for _, span := range tomlLineSpans(body) {
		if name, isArray, ok := tomlEditSectionHeader(span.text); ok {
			inSection = !isArray && name == sectionName
			continue
		}
		if !inSection {
			continue
		}
		got, value, ok := tomlKeyValue(span.text)
		if ok && got == key {
			return value, true
		}
	}
	return "", false
}

func tomlStringLiteralEquals(value, want string) bool {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		quote := value[0]
		if (quote == '"' || quote == '\'') && value[len(value)-1] == quote {
			return value[1:len(value)-1] == want
		}
	}
	return value == want
}

func tomlBodyHasTopLevelKey(body, key string) bool {
	for _, span := range tomlLineSpans(body) {
		if _, _, ok := tomlEditSectionHeader(span.text); ok {
			return false
		}
		if got, ok := tomlTopLevelKey(span.text); ok && got == key {
			return true
		}
	}
	return false
}

func tomlBodyHasSection(body, sectionName string) bool {
	for _, span := range tomlLineSpans(body) {
		name, _, ok := tomlEditSectionHeader(span.text)
		if ok && name == sectionName {
			return true
		}
	}
	return false
}

func renderScopeForPath(path string) RenderScope {
	if isUserConfigPath(path) {
		return RenderScopeUser
	}
	return RenderScopeProject
}

func isUserConfigPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	for _, uc := range userConfigCandidatePaths() {
		uc = strings.TrimSpace(uc)
		if uc == "" {
			continue
		}
		pathAbs, pathErr := filepath.Abs(path)
		ucAbs, ucErr := filepath.Abs(uc)
		if pathErr == nil && ucErr == nil {
			if filepath.Clean(pathAbs) == filepath.Clean(ucAbs) {
				return true
			}
			continue
		}
		if filepath.Clean(path) == filepath.Clean(uc) {
			return true
		}
	}
	return false
}

// IsUserConfigPath reports whether path is one of Reasonix's current or legacy
// user-global config locations. Other paths use project-scoped rendering.
func IsUserConfigPath(path string) bool {
	return isUserConfigPath(path)
}

// Save writes the configuration back to the file it was loaded from
// (SourcePath), or to ./reasonix.toml when none exists yet — the conventional
// project-local target a fresh GUI session would create.
func (c *Config) Save() error {
	path := SourcePath()
	if path == "" {
		path = "reasonix.toml"
	}
	return c.SaveTo(path)
}

// SaveForRoot saves root's project config when it exists, falling back to the
// user's global config when root has no reasonix.toml. Existing project files
// are edited from their own TOML only, never from a runtime user+project merge.
func (c *Config) SaveForRoot(root string) error {
	root = resolveRoot(root)
	projectTOML := "reasonix.toml"
	if root != "." {
		projectTOML = filepath.Join(root, "reasonix.toml")
	}
	if _, err := os.Stat(projectTOML); err == nil {
		projectCfg := LoadForEditWithoutCredentials(projectTOML)
		return projectCfg.SaveTo(projectTOML)
	}
	if uc := userConfigPath(); uc != "" {
		if err := os.MkdirAll(filepath.Dir(uc), 0o755); err != nil {
			return err
		}
		return c.SaveTo(uc)
	}
	return c.SaveTo(projectTOML)
}
