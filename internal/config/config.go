// Package config loads Reasonix's runtime configuration from TOML. Resolution order:
// flag > project ./reasonix.toml > user config.toml (in the OS user-config dir) > built-in defaults.
// Secrets come from the environment via api_key_env and are never stored in
// config files.
package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	fileencoding "reasonix/internal/fileutil/encoding"
	"reasonix/internal/netclient"
	"reasonix/internal/provider"
)

var validSkillName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// IsValidSkillName reports whether name is a usable skill identifier.
func IsValidSkillName(name string) bool { return validSkillName.MatchString(name) }

// SkillNameKey normalizes a skill identifier for config comparisons.
func SkillNameKey(name string) string {
	name = strings.TrimSpace(name)
	if !IsValidSkillName(name) {
		return ""
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(name)
	}
	return name
}

// Config is Reasonix's runtime configuration.
type Config struct {
	ConfigVersion    int    `toml:"config_version"`
	DefaultModel     string `toml:"default_model"`
	Language         string `toml:"language"` // ui/model language tag (e.g. "zh"); empty = auto-detect from $LANG / $REASONIX_LANG
	CredentialsStore string `toml:"credentials_store"`
	// CacheContext is a per-workspace user attribution id (DeepSeek user_id /
	// OpenAI user); project config sets it so shared providers need not repeat.
	CacheContext string `toml:"cachecontext"`
	// LogName and User both override the username in the auto cachecontext
	// default (repo config wins over user-global, then $LOGNAME, then the
	// system account). logname prevails when both are set; see loadForRoot.
	LogName       string              `toml:"logname"`
	User          string              `toml:"user"`
	UI            UIConfig            `toml:"ui"`
	CLI           CLIConfig           `toml:"cli"`
	Desktop       DesktopConfig       `toml:"desktop"`
	Billing       BillingConfig       `toml:"billing"`
	Telemetry     TelemetryConfig     `toml:"telemetry"`
	Notifications NotificationsConfig `toml:"notifications"`
	Agent         AgentConfig         `toml:"agent"`
	Providers     []ProviderEntry     `toml:"providers"`
	Tools         ToolsConfig         `toml:"tools"`
	Permissions   PermissionsConfig   `toml:"permissions"`
	Sandbox       SandboxConfig       `toml:"sandbox"`
	Network       NetworkConfig       `toml:"network"`
	Environment   EnvironmentConfig   `toml:"environment"`
	Plugins       []PluginEntry       `toml:"plugins"`
	Skills        SkillsConfig        `toml:"skills"`
	Statusline    StatuslineConfig    `toml:"statusline"`
	LSP           LSPConfig           `toml:"lsp"`
	Bot           BotConfig           `toml:"bot"`
	Serve         ServeConfig         `toml:"serve"`
	Secrets       SecretsConfig       `toml:"secrets"`
	Remote        RemoteConfig        `toml:"remote"`

	systemPromptFileSource     promptFileSource
	providerSources            map[string]providerSourceScope
	shadowedProjectProviders   []ProviderEntry
	ignoredProjectDefaultModel string
	ignoredLegacyStepLimits    bool
	expansionEnv               map[string]string
	pluginPackageOwners        map[string]string
	pluginPackageSkillOwners   map[string][]string
	pluginPackageAgentOwners   map[string][]string
	// explicitProjectSkillKeys records project-level skill fields that the
	// settings UI intentionally owns even when their value equals the built-in
	// default. It is transient edit metadata and is never serialized directly.
	explicitProjectSkillKeys map[string]bool
	stagedModelCredentials   []string
	editLoadErr              error
	// loadWarnings are non-fatal issues observed while loading config (corrupt
	// user/project files recovered via last-known-good or defaults). They never
	// rewrite the original file; the UI may surface them for doctor repair.
	loadWarnings      []string
	openCodeGoJournal *openCodeGoJournal
}

// KeepProjectSkillKey marks a skill field as an intentional project override.
// An explicit empty/false project value must still be written so it can
// override a non-default user setting in the layered configuration.
func (c *Config) KeepProjectSkillKey(key string) error {
	key = strings.TrimSpace(key)
	switch key {
	case "paths", "excluded_paths", "disabled_skills", "disable_implicit_invocation", "max_depth":
	default:
		return fmt.Errorf("unknown project skill key %q", key)
	}
	if c.explicitProjectSkillKeys == nil {
		c.explicitProjectSkillKeys = make(map[string]bool)
	}
	c.explicitProjectSkillKeys[key] = true
	return nil
}

func (c *Config) keepsProjectSkillKey(key string) bool {
	return c != nil && c.explicitProjectSkillKeys[key]
}

type promptFileSource uint8

const (
	promptFileSourceUnknown promptFileSource = iota
	promptFileSourceUser
	promptFileSourceProject
)

type systemPromptFileError struct {
	configured string
	candidates []string
	errors     []error
	allMissing bool
}

func (e *systemPromptFileError) Error() string {
	detail := "could not be read from any configured location"
	if e.allMissing {
		detail = "not found at any configured location"
	}
	message := fmt.Sprintf("system_prompt_file %q %s: %s", e.configured, detail, strings.Join(e.candidates, ", "))
	if !e.allMissing && len(e.errors) > 0 {
		message += ": " + errors.Join(e.errors...).Error()
	}
	return message
}

func (e *systemPromptFileError) Unwrap() error { return errors.Join(e.errors...) }

// IsMissingSystemPromptFile reports whether every allowed location for a
// configured prompt file was absent. Permission, containment, and other I/O
// failures deliberately return false so callers do not start without an
// explicitly configured prompt.
func IsMissingSystemPromptFile(err error) bool {
	var target *systemPromptFileError
	return errors.As(err, &target) && target.allMissing
}

// TelemetryConfig controls content-free CLI usage metrics. It is user-global:
// project reasonix.toml values are ignored so a cloned repository cannot opt a
// user into reporting.
type TelemetryConfig struct {
	CLIMetrics string `toml:"cli_metrics"` // auto|on|off; empty means consent has not been requested
}

// CLITelemetryConfigured reports whether the user has made an explicit CLI
// telemetry choice. The runtime policy still treats an absent value as auto,
// but persistence must preserve absence until the first eligible consent prompt.
func (c *Config) CLITelemetryConfigured() bool {
	if c == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(c.Telemetry.CLIMetrics)) {
	case "auto", "on", "off":
		return true
	default:
		return false
	}
}

// CLITelemetryMode returns the normalized CLI telemetry policy.
func (c *Config) CLITelemetryMode() string {
	if c == nil {
		return "auto"
	}
	switch strings.ToLower(strings.TrimSpace(c.Telemetry.CLIMetrics)) {
	case "on":
		return "on"
	case "off":
		return "off"
	default:
		return "auto"
	}
}

// LoadWarnings returns non-fatal config load issues (corrupt files recovered in
// memory). The returned slice is a copy.
func (c *Config) LoadWarnings() []string {
	if c == nil || len(c.loadWarnings) == 0 {
		return nil
	}
	out := make([]string, len(c.loadWarnings))
	copy(out, c.loadWarnings)
	return out
}

// HasLoadWarnings reports whether the load used a degraded in-memory fallback.
func (c *Config) HasLoadWarnings() bool {
	return c != nil && len(c.loadWarnings) > 0
}

func (c *Config) addLoadWarning(msg string) {
	if c == nil {
		return
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	c.loadWarnings = append(c.loadWarnings, msg)
}

// IgnoredLegacyAgentStepLimits reports whether this load found and ignored the
// retired [agent].max_steps or planner_max_steps settings. Boot removes standard
// key assignments before loading, while read-only/config-only loads only report
// and normalize them in memory.
func (c *Config) IgnoredLegacyAgentStepLimits() bool {
	return c != nil && c.ignoredLegacyStepLimits
}

// IgnoredProjectDefaultModel returns the project reasonix.toml default_model
// that LoadForRoot ignored because no configured provider serves it (see
// restoreUnresolvableProjectDefaultModel), or "" when none was ignored.
func (c *Config) IgnoredProjectDefaultModel() string {
	if c == nil {
		return ""
	}
	return c.ignoredProjectDefaultModel
}

// SecretsConfig controls the credential protection layers. It is a user-global
// setting: project reasonix.toml values are ignored (see LoadForRoot), so a
// cloned repository cannot silently opt the user into workflow-breaking
// protections.
type SecretsConfig struct {
	// FilterSubprocessEnv strips credential-like environment variables
	// (*_API_KEY, *TOKEN*, *SECRET*, ...) from tool subprocesses (bash, hooks,
	// LSP, MCP stdio). Default off: it breaks token-based workflows such as
	// `gh`, HTTPS `git push`, and `npm publish`.
	FilterSubprocessEnv bool `toml:"filter_subprocess_env"`
	// ProtectSensitiveFiles makes read/list/search tools treat credential
	// paths (.env, .git-credentials, .netrc, *.pem/*.key/*.p12/*.pfx, ~/.ssh)
	// as invisible. Default off because hiding the files breaks legitimate
	// "edit my .env" workflows.
	ProtectSensitiveFiles bool `toml:"protect_sensitive_files"`
}

type providerSourceScope string

const (
	providerSourceUser    providerSourceScope = "user"
	providerSourceProject providerSourceScope = "project"
)

// UIConfig controls CLI presentation-only settings. Desktop appearance is kept in
// DesktopConfig so desktop preferences cannot alter terminal output or prompts.
type UIConfig struct {
	Theme          string `toml:"theme"`           // auto|dark|light; empty resolves to auto
	ThemeStyle     string `toml:"theme_style"`     // graphite|aurora|slate|carbon|nocturne|amber and legacy aliases
	ShortcutLayout string `toml:"shortcut_layout"` // classic|desktop; accepted for compatibility
	CloseBehavior  string `toml:"close_behavior"`  // legacy desktop close behavior; prefer desktop.close_behavior
	ShowReasoning  bool   `toml:"show_reasoning"`  // Ctrl+O / /verbose: show thinking text in CLI; false = collapsed
	ShowTurnUsage  bool   `toml:"show_turn_usage"` // show per-request token/cost receipts in the CLI/TUI transcript
	CursorShape    string `toml:"cursor_shape"`    // block|underline|bar; empty defaults to bar
}

// CLIConfig controls user-global native CLI behavior. It is separate from
// project runtime settings so a repository cannot change the installed
// binary's update channel.
type CLIConfig struct {
	// UpdateChannel is decoded for compatibility with pre-single-channel
	// configurations. Runtime behavior is always the official release channel,
	// and the canonical renderer intentionally drops this field.
	UpdateChannel string `toml:"update_channel"`
}

// NotificationsConfig controls optional system notifications for CLI chat/run.
type NotificationsConfig struct {
	Enabled         bool `toml:"enabled"`
	TurnDone        bool `toml:"turn_done"`
	ApprovalRequest bool `toml:"approval_request"`
	AskRequest      bool `toml:"ask_request"`
}

// EnvironmentEnabled reports whether startup environment probing should feed the
// cache-stable system prompt.
func (c *Config) EnvironmentEnabled() bool {
	return c == nil || c.Environment.Enabled == nil || *c.Environment.Enabled
}

// UITheme normalizes ui.theme to a supported value.
func (c *Config) UITheme() string {
	switch strings.ToLower(strings.TrimSpace(c.UI.Theme)) {
	case "dark":
		return "dark"
	case "light":
		return "light"
	default:
		return "auto"
	}
}

// UIThemeStyle normalizes ui.theme_style. Empty means "pick the default style
// for the resolved light/dark shell".
func (c *Config) UIThemeStyle() string {
	return normalizeThemeStyle(c.UI.ThemeStyle)
}

// UIShortcutLayout normalizes the legacy CLI shortcut layout setting. It is kept
// for compatibility; Shift+Tab toggles Plan and Ctrl+Y toggles YOLO in both
// layouts.
func (c *Config) UIShortcutLayout() string {
	switch strings.ToLower(strings.TrimSpace(c.UI.ShortcutLayout)) {
	case "desktop", "dual", "dual-axis", "dual_axis":
		return "desktop"
	default:
		return "classic"
	}
}

// UICursorShape normalizes ui.cursor_shape. The slim "bar" default stays
// visible without covering CJK wide characters. Valid values are "block",
// "underline", and "bar".
func (c *Config) UICursorShape() string {
	switch strings.ToLower(strings.TrimSpace(c.UI.CursorShape)) {
	case "block":
		return "block"
	case "underline":
		return "underline"
	default:
		return "bar"
	}
}

func normalizeThemeStyle(style string) string {
	switch strings.ToLower(strings.TrimSpace(style)) {
	case "graphite", "aurora", "slate", "carbon", "nocturne", "amber", "ember", "midnight", "sandstone", "porcelain", "linen", "glacier":
		return strings.ToLower(strings.TrimSpace(style))
	default:
		return ""
	}
}

// The retired "classic" style normalizes to workbench, like any other value
// this build does not know, so a config written before the style was removed
// keeps working and the Go side and the UI agree on what it means.
func normalizeDesktopLayoutStyle(style string) string {
	switch strings.ToLower(strings.TrimSpace(style)) {
	case "creation":
		return "creation"
	default:
		return "workbench"
	}
}

func normalizeCloseBehavior(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "quit", "exit":
		return "quit"
	default:
		return "background"
	}
}

// DesktopLanguage normalizes the desktop UI language. Empty means auto-detect
// from the browser/OS locale; it deliberately does not read top-level language,
// which is used by the CLI/model-facing runtime.
func (c *Config) DesktopLanguage() string {
	switch strings.ToLower(strings.TrimSpace(c.Desktop.Language)) {
	case "en":
		return "en"
	case "zh":
		return "zh"
	default:
		return ""
	}
}

// DesktopCurrency returns the explicit user-global pricing currency. The
// persisted field keeps its original desktop namespace for compatibility;
// empty means the pricing region follows the desktop/CLI language.
func (c *Config) DesktopCurrency() string {
	if c == nil {
		return ""
	}
	switch strings.ToUpper(strings.TrimSpace(c.Desktop.Currency)) {
	case "CNY", "RMB", "CNH":
		return "CNY"
	case "USD":
		return "USD"
	default:
		return ""
	}
}

// DesktopTheme normalizes desktop.theme. New desktop users default to the OS
// automatic graphite product look; an explicit auto/light/dark is preserved.
func (c *Config) DesktopTheme() string {
	switch strings.ToLower(strings.TrimSpace(c.Desktop.Theme)) {
	case "auto":
		return "auto"
	case "light":
		return "light"
	case "dark":
		return "dark"
	default:
		return "auto"
	}
}

// DesktopThemeStyle normalizes desktop.theme_style. Empty means the frontend
// chooses the default style for the resolved desktop theme.
func (c *Config) DesktopThemeStyle() string {
	return normalizeThemeStyle(c.Desktop.ThemeStyle)
}

// DesktopTerminalTheme normalizes the integrated terminal colour preference.
// Auto deliberately follows the resolved desktop app theme, including OS theme
// changes while desktop.theme is also auto.
func (c *Config) DesktopTerminalTheme() string {
	switch strings.ToLower(strings.TrimSpace(c.Desktop.TerminalTheme)) {
	case "dark":
		return "dark"
	case "light":
		return "light"
	default:
		return "auto"
	}
}

// DesktopLayoutStyle defaults to workbench. The retired "classic" value is
// normalized on read rather than migrated to disk: nothing behaves differently
// for it, so there is no rewritten value worth persisting.
func (c *Config) DesktopLayoutStyle() string {
	if strings.EqualFold(strings.TrimSpace(c.Desktop.ThemeStyle), "workbench") && strings.TrimSpace(c.Desktop.LayoutStyle) == "" {
		return "workbench"
	}
	return normalizeDesktopLayoutStyle(c.Desktop.LayoutStyle)
}

// DesktopCloseBehavior normalizes the desktop close-window preference. It falls
// back to the legacy ui.close_behavior value for configs written before [desktop]
// existed.
func (c *Config) DesktopCloseBehavior() string {
	if strings.TrimSpace(c.Desktop.CloseBehavior) != "" {
		return normalizeCloseBehavior(c.Desktop.CloseBehavior)
	}
	return normalizeCloseBehavior(c.UI.CloseBehavior)
}

// UICloseBehavior is the legacy name for DesktopCloseBehavior.
func (c *Config) UICloseBehavior() string {
	return c.DesktopCloseBehavior()
}

// DesktopConversationWidth returns the normalized desktop conversation width.
// Unknown and missing values fall back to standard for backward compatibility.
func (c *Config) DesktopConversationWidth() string {
	if c != nil && strings.EqualFold(strings.TrimSpace(c.Desktop.ConversationWidth), "full") {
		return "full"
	}
	return "standard"
}

// NormalizeToolApprovalMode returns the canonical desktop/session tool approval
// posture. Unknown or missing values fall back to ask for safety.
func NormalizeToolApprovalMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "auto":
		return "auto"
	case "yolo", "full", "full-access", "bypass":
		return "yolo"
	default:
		return "ask"
	}
}

// DesktopDefaultToolApprovalMode is the Ask/Auto/YOLO default used only when
// creating a new desktop session. Existing tabs and restored sessions keep their
// own persisted runtime state.
func (c *Config) DesktopDefaultToolApprovalMode() string {
	if c == nil {
		return "ask"
	}
	return NormalizeToolApprovalMode(c.Desktop.DefaultToolApprovalMode)
}

// DesktopStatusBarStyle normalizes the desktop status bar metric label style.
// Unmigrated configurations adopt icon labels once; later choices are preserved.
func (c *Config) DesktopStatusBarStyle() string {
	if !c.Desktop.StatusBarStyleInitialized {
		return "icon"
	}
	switch strings.ToLower(strings.TrimSpace(c.Desktop.StatusBarStyle)) {
	case "icon":
		return "icon"
	case "text":
		return "text"
	default:
		return "icon"
	}
}

var defaultDesktopStatusBarItems = []string{
	"model",
	"workspace",
	"git_branch",
	"cache",
	"cache_avg",
	"session_tokens",
	"turn_tokens",
	"turn_tps",
	"turn_output_tokens",
	"turn_cache_tokens",
	"turn_cost",
	"session_turns",
	"context",
	"compact",
	"cost",
	"balance",
}

var knownDesktopStatusBarItems = desktopStatusBarItemSet(defaultDesktopStatusBarItems)

func desktopStatusBarItemSet(items []string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, item := range items {
		out[item] = true
	}
	return out
}

// DefaultDesktopStatusBarItems returns the default ordered visible desktop
// status bar items.
func DefaultDesktopStatusBarItems() []string {
	return append([]string(nil), defaultDesktopStatusBarItems...)
}

// DesktopStatusBarItems normalizes the ordered visible desktop status bar items.
// An unset or empty list uses the default full set; explicit non-empty lists
// preserve user order and omit hidden items.
func (c *Config) DesktopStatusBarItems() []string {
	return normalizeDesktopStatusBarItems(c.Desktop.StatusBarItems)
}

func normalizeDesktopStatusBarItems(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, raw := range items {
		id := strings.TrimSpace(raw)
		if !knownDesktopStatusBarItems[id] || seen[id] {
			continue
		}
		out = append(out, id)
		seen[id] = true
	}
	if len(out) == 0 {
		return DefaultDesktopStatusBarItems()
	}
	return out
}

// DesktopCheckUpdates reports whether the desktop should check for updates on
// startup. Missing configs default to true so existing users keep update notices.
func (c *Config) DesktopCheckUpdates() bool {
	if c == nil || c.Desktop.CheckUpdates == nil {
		return true
	}
	return *c.Desktop.CheckUpdates
}

// NormalizeCLIUpdateChannel returns the only public native CLI update channel.
// The input remains accepted so older preview configurations keep loading.
func NormalizeCLIUpdateChannel(_ string) string {
	return "stable"
}

// CLIUpdateChannel returns the user-global native CLI update channel.
func (c *Config) CLIUpdateChannel() string {
	if c == nil {
		return "stable"
	}
	return NormalizeCLIUpdateChannel(c.CLI.UpdateChannel)
}

// NormalizeDesktopUpdateChannel returns the only public Desktop update channel.
// Legacy preview/canary/beta/next values are deliberately ignored so an old
// configuration cannot strand the installation on the retired channel.
func NormalizeDesktopUpdateChannel(_ string) string {
	return "stable"
}

// DesktopUpdateChannel returns the desktop channel whose latest pointer should be
// checked. Missing or unknown configs default to stable.
func (c *Config) DesktopUpdateChannel() string {
	if c == nil {
		return "stable"
	}
	return NormalizeDesktopUpdateChannel(c.Desktop.UpdateChannel)
}

// ColdResumePruneEnabled reports whether stale tool results are elided when a
// session resumes past the provider cache window. Default true (cheaper cold
// restart); users keep full history by disabling it.
func (c *Config) ColdResumePruneEnabled() bool {
	if c == nil || c.Agent.ColdResumePrune == nil {
		return true
	}
	return *c.Agent.ColdResumePrune
}

// ResponseLanguage normalizes the top-level language preference for final
// answers. Empty means auto: replies follow the current user turn.
func (c *Config) ResponseLanguage() string {
	if c == nil {
		return "auto"
	}
	return NormalizeLanguage(c.Language)
}

// NormalizeLanguage returns one of auto|zh|en for UI/default reply language settings.
func NormalizeLanguage(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "", "auto", "detect", "default":
		return "auto"
	case "zh", "cn", "chinese", "中文":
		return "zh"
	case "en", "english":
		return "en"
	default:
		return "auto"
	}
}

// ReasoningLanguage normalizes agent.reasoning_language. Empty means auto:
// visible reasoning follows the conversation language already described by the
// stable LanguagePolicy. Legacy "default" is treated as auto.
func (c *Config) ReasoningLanguage() string {
	if c == nil {
		return "auto"
	}
	return NormalizeReasoningLanguage(c.Agent.ReasoningLanguage)
}

// NormalizeReasoningLanguage returns one of auto|zh|en.
func NormalizeReasoningLanguage(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "", "auto", "follow", "conversation", "detect", "default", "model", "model-default", "model_default", "provider":
		return "auto"
	case "zh", "cn", "chinese", "中文":
		return "zh"
	case "en", "english":
		return "en"
	default:
		return "auto"
	}
}

// DesktopTelemetry reports whether the desktop sends the anonymous launch ping.
// It carries no conversation, key, or file data — see desktop/README.md.
func (c *Config) DesktopTelemetry() bool {
	if c == nil || c.Desktop.Telemetry == nil {
		return true
	}
	return *c.Desktop.Telemetry
}

// DesktopMetrics reports whether the desktop sends aggregate desktop metrics —
// anonymous (signal, bucket) counters, never content. Default on.
func (c *Config) DesktopMetrics() bool {
	if c == nil || c.Desktop.Metrics == nil {
		return true
	}
	return *c.Desktop.Metrics
}

// LSPConfig governs the optional Language Server Protocol tools (lsp_definition,
// lsp_references, lsp_hover, lsp_diagnostics). Enabled defaults to true; the
// servers themselves are never bundled — each resolves on PATH and the tool
// returns an install hint when it is missing, so the capability is dormant until
// the user installs a server. Servers overrides or extends the built-in language
// → server map, keyed by language id (e.g. "go", "rust", "python").
type LSPConfig struct {
	Enabled bool                 `toml:"enabled"`
	Servers map[string]LSPServer `toml:"servers"`
}

// LSPServer overrides a built-in language's server or, when keyed by a new
// language, adds one. An empty field falls back to the built-in default for that
// language; Extensions is required when adding a language the built-ins don't
// cover (e.g. ".ex" for Elixir) so files route to it.
type LSPServer struct {
	Command     string            `toml:"command"`
	Args        []string          `toml:"args"`
	Env         map[string]string `toml:"env"`
	LanguageID  string            `toml:"language_id"`
	Extensions  []string          `toml:"extensions"`
	InstallHint string            `toml:"install_hint"`
}

// StatuslineConfig configures a custom status line. Command, when set, is run at
// startup and after each turn; its first line of stdout replaces the built-in
// status data row. A JSON payload (model, context tokens, cwd) is fed on stdin.
type StatuslineConfig struct {
	Command string `toml:"command"`
}

// BotConfig 控制多渠道 IM bot 消息网关。
type BotConfig struct {
	Enabled            bool                  `toml:"enabled"`
	Model              string                `toml:"model"` // 用于 bot 的模型名，空则用 default_model
	ToolApprovalMode   string                `toml:"tool_approval_mode"`
	MaxSteps           int                   `toml:"max_steps"`
	DebounceMs         int                   `toml:"debounce_ms"` // 消息合并窗口，毫秒
	QueueMode          string                `toml:"queue_mode"`  // steer|followup|collect|interrupt
	QueueCap           int                   `toml:"queue_cap"`
	QueueDrop          string                `toml:"queue_drop"` // summarize|old|new
	IgnoreSelfMessages bool                  `toml:"ignore_self_messages"`
	SelfUserIDs        BotSelfUserIDs        `toml:"self_user_ids"`
	Control            BotControlConfig      `toml:"control"`
	Pairing            BotPairingConfig      `toml:"pairing"`
	Allowlist          BotAllowlist          `toml:"allowlist"`
	QQ                 QQBotConfig           `toml:"qq"`
	Feishu             FeishuBotConfig       `toml:"feishu"`
	Weixin             WeixinBotConfig       `toml:"weixin"`
	Dingtalk           DingtalkBotConfig     `toml:"dingtalk"`
	Routes             []BotRouteConfig      `toml:"routes"`
	Connections        []BotConnectionConfig `toml:"connections"`
	// DesktopWatchers persists /desktop watch subscriptions so god-view
	// notifications survive a desktop restart. Managed by the desktop bot
	// bridge, not the settings UI.
	DesktopWatchers []BotDesktopWatcherConfig `toml:"desktop_watchers"`
}

// BotDesktopWatcherConfig is one bot chat subscribed to desktop events
// (/desktop watch on).
type BotDesktopWatcherConfig struct {
	Platform     string `toml:"platform"`
	ConnectionID string `toml:"connection_id"`
	Domain       string `toml:"domain"`
	ChatType     string `toml:"chat_type"`
	ChatID       string `toml:"chat_id"`
}

type BotSelfUserIDs struct {
	QQ       []string `toml:"qq"`
	Feishu   []string `toml:"feishu"`
	Weixin   []string `toml:"weixin"`
	Dingtalk []string `toml:"dingtalk"`
}

type BotControlConfig struct {
	Enabled  bool   `toml:"enabled"`
	Addr     string `toml:"addr"`
	TokenEnv string `toml:"token_env"`
}

type BotRouteConfig struct {
	ConnectionID     string `toml:"connection_id"`
	Platform         string `toml:"platform"`
	ChatType         string `toml:"chat_type"`
	ChatID           string `toml:"chat_id"`
	UserID           string `toml:"user_id"`
	ThreadID         string `toml:"thread_id"`
	Model            string `toml:"model"`
	ToolApprovalMode string `toml:"tool_approval_mode"`
	WorkspaceRoot    string `toml:"workspace_root"`
}

// BotAllowlist 控制哪些用户可以使用 bot。
type BotAllowlist struct {
	Enabled           bool     `toml:"enabled"`
	AllowAll          bool     `toml:"allow_all"`
	QQUsers           []string `toml:"qq_users"`
	FeishuUsers       []string `toml:"feishu_users"`
	WeixinUsers       []string `toml:"weixin_users"`
	QQApprovers       []string `toml:"qq_approvers"`
	FeishuApprovers   []string `toml:"feishu_approvers"`
	WeixinApprovers   []string `toml:"weixin_approvers"`
	QQAdmins          []string `toml:"qq_admins"`
	FeishuAdmins      []string `toml:"feishu_admins"`
	WeixinAdmins      []string `toml:"weixin_admins"`
	QQGroups          []string `toml:"qq_groups"`
	FeishuGroups      []string `toml:"feishu_groups"`
	WeixinGroups      []string `toml:"weixin_groups"`
	DingtalkUsers     []string `toml:"dingtalk_users"`
	DingtalkApprovers []string `toml:"dingtalk_approvers"`
	DingtalkAdmins    []string `toml:"dingtalk_admins"`
	DingtalkGroups    []string `toml:"dingtalk_groups"`
}

type BotPairingConfig struct {
	Enabled               bool `toml:"enabled"`
	RequestTTLMinutes     int  `toml:"request_ttl_minutes"`
	MaxPendingPerPlatform int  `toml:"max_pending_per_platform"`
}

// BotAccessConfig controls who may use one concrete bot connection.
type BotAccessConfig struct {
	Enabled        bool     `toml:"enabled"`
	AllowAll       bool     `toml:"allow_all"`
	PairingEnabled bool     `toml:"pairing_enabled"`
	Users          []string `toml:"users"`
	Groups         []string `toml:"groups"`
	Approvers      []string `toml:"approvers"`
	Admins         []string `toml:"admins"`
}

// QQBotConfig QQ 官方 Bot API v2 配置。
type QQBotConfig struct {
	Enabled          bool            `toml:"enabled"`
	AppID            string          `toml:"app_id"`
	AppSecretEnv     string          `toml:"app_secret_env"` // 环境变量名，如 QQ_BOT_APP_SECRET
	Sandbox          bool            `toml:"sandbox"`        // true 使用 QQ 沙箱 API / gateway
	Model            string          `toml:"model"`
	ToolApprovalMode string          `toml:"tool_approval_mode"`
	WorkspaceRoot    string          `toml:"workspace_root"`
	Access           BotAccessConfig `toml:"access"`
}

// FeishuBotConfig 飞书自建应用 Bot 配置。
type FeishuBotConfig struct {
	Enabled           bool   `toml:"enabled"`
	Domain            string `toml:"domain"` // feishu（默认）| lark
	AppID             string `toml:"app_id"`
	AppSecretEnv      string `toml:"app_secret_env"`     // 如 FEISHU_BOT_APP_SECRET
	VerificationToken string `toml:"verification_token"` // 事件订阅验证 token
	Mode              string `toml:"mode"`               // webhook（默认）| websocket
	WebhookPort       int    `toml:"webhook_port"`       // webhook 模式端口
	RequireMention    bool   `toml:"require_mention"`
	// OutboundMediaRoots contains absolute local directories the loopback /send
	// control API may attach files from. Media refs must be bare filenames and
	// must exist in exactly one configured root. Empty (the default) disables
	// outbound file sending.
	OutboundMediaRoots []string `toml:"outbound_media_roots"`
}

// WeixinBotConfig 微信 iLink Bot 配置。
type WeixinBotConfig struct {
	Enabled   bool   `toml:"enabled"`
	AccountID string `toml:"account_id"`
	TokenEnv  string `toml:"token_env"` // 环境变量名，如 WEIXIN_BOT_TOKEN
	APIBase   string `toml:"api_base"`  // iLink API base URL
}

// DingtalkBotConfig 钉钉企业内部应用机器人（Stream 模式）配置。
type DingtalkBotConfig struct {
	Enabled          bool            `toml:"enabled"`
	ClientID         string          `toml:"client_id"`          // 钉钉应用 AppKey（ClientID）
	ClientSecret     string          `toml:"client_secret"`      // 钉钉应用 AppSecret（ClientSecret）
	ClientIDEnv      string          `toml:"client_id_env"`      // 环境变量名，如 DINGTALK_CLIENT_ID
	SecretEnv        string          `toml:"secret_env"`         // 环境变量名，如 DINGTALK_CLIENT_SECRET
	BotName          string          `toml:"bot_name"`           // 机器人昵称；群聊 @ 剥离时匹配
	RequireMention   bool            `toml:"require_mention"`    // 群聊是否必须 @ 机器人
	Model            string          `toml:"model"`              // 会话模型；空 = 全局默认
	ToolApprovalMode string          `toml:"tool_approval_mode"` // ask|auto|yolo；空 = 全局默认
	WorkspaceRoot    string          `toml:"workspace_root"`     // 会话工作目录；空 = 启动 Bot 时的 cwd
	Access           BotAccessConfig `toml:"access"`             // 该渠道访问控制（allowlist）
	// SessionMappings 直配渠道的会话绑定（与 [[bot.connections]] 同构）。
	// legacy [bot.dingtalk] 没有 connection 记录，/new 旋转后的新会话路径
	// 持久化在这里，重启后仍能恢复（见 botruntime.rememberInbound）。
	SessionMappings []BotConnectionSessionMapping `toml:"session_mappings"`
}

// BotConnectionConfig is the desktop-friendly connection record for IM bot
// channels. It keeps install/runtime state separate from legacy per-provider
// knobs so the UI can expose a simple "connect first" flow while old configs
// keep working.
type BotConnectionConfig struct {
	ID               string                        `toml:"id"`
	Provider         string                        `toml:"provider"` // qq|feishu|weixin
	Domain           string                        `toml:"domain"`   // feishu|lark|weixin|qq
	Label            string                        `toml:"label"`
	Enabled          bool                          `toml:"enabled"`
	Status           string                        `toml:"status"` // disconnected|pending|connected|error
	Model            string                        `toml:"model"`
	ToolApprovalMode string                        `toml:"tool_approval_mode"`
	WorkspaceRoot    string                        `toml:"workspace_root"`
	Access           BotAccessConfig               `toml:"access"`
	Credential       BotConnectionCredential       `toml:"credential"`
	SessionMappings  []BotConnectionSessionMapping `toml:"session_mappings"`
	LastError        string                        `toml:"last_error"`
	CreatedAt        string                        `toml:"created_at"`
	UpdatedAt        string                        `toml:"updated_at"`
}

type BotConnectionCredential struct {
	AppID        string `toml:"app_id"`
	AppSecretEnv string `toml:"app_secret_env"`
	AccountID    string `toml:"account_id"`
	TokenEnv     string `toml:"token_env"`
}

type BotConnectionSessionMapping struct {
	RemoteID      string `toml:"remote_id"`
	SessionID     string `toml:"session_id"`
	SessionSource string `toml:"session_source"`
	ChatType      string `toml:"chat_type"`
	UserID        string `toml:"user_id"`
	ThreadID      string `toml:"thread_id"`
	Scope         string `toml:"scope"`
	WorkspaceRoot string `toml:"workspace_root"`
	UpdatedAt     string `toml:"updated_at"`
}

// ServeConfig controls the HTTP serve frontend security settings.
type ServeConfig struct {
	// AuthMode selects the authentication mode for the HTTP serve frontend.
	// "none" (default): no authentication.
	// "token": a pre-shared token in the URL query string.
	// "password": a login page with bcrypt password verification.
	AuthMode string `toml:"auth_mode"`
	// Token is a pre-shared token for auth_mode = "token". When empty, a
	// cryptographically random token is generated at startup and printed.
	Token string `toml:"token"`
	// PasswordHash is a bcrypt hash of the password for auth_mode = "password".
	// Generate one with: reasonix serve --hash-password --password '...'
	PasswordHash string `toml:"password_hash"`
	// BehindProxy indicates the server sits behind a trusted reverse proxy
	// (nginx, Caddy, Cloudflare, etc.) that sets X-Forwarded-For and
	// X-Forwarded-Proto headers. When true, those headers are used for
	// rate-limiting and Secure-cookie decisions. When false (default), they
	// are ignored — an attacker can otherwise forge them.
	BehindProxy bool `toml:"behind_proxy"`
}

// NetworkConfig controls ordinary outbound HTTP traffic such as model providers,
// wallet-balance lookups, updater checks, CodeGraph downloads, and web_fetch.
// web_fetch reuses these proxy settings while keeping its own SSRF-guarded
// dialer.
type NetworkConfig struct {
	// ProxyMode is "auto" (default; environment proxy for now), "env", "custom",
	// or "off". auto leaves room for OS proxy detection later without changing the
	// config shape.
	ProxyMode string `toml:"proxy_mode"`
	// ProxyURL is an advanced custom override such as "socks5://127.0.0.1:7890".
	// When set and proxy_mode = "custom", it wins over the structured proxy table.
	ProxyURL string `toml:"proxy_url"`
	// NoProxy is honored for custom proxies. Env/auto modes use NO_PROXY from the
	// process environment instead.
	NoProxy string             `toml:"no_proxy"`
	Proxy   NetworkProxyConfig `toml:"proxy"`
}

// NetworkProxyConfig is the structured custom-proxy editor shape. Password is
// optional and supports ${VAR} expansion, so users can avoid storing it literally.
type NetworkProxyConfig struct {
	Type     string `toml:"type"` // http|https|socks5|socks5h
	Server   string `toml:"server"`
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password"`
}

// NetworkProxySpec returns the expanded proxy settings used by netclient.
func (c *Config) NetworkProxySpec() netclient.ProxySpec {
	return netclient.ProxySpec{
		Mode:        c.Network.ProxyMode,
		URL:         c.expandVars(c.Network.ProxyURL),
		NoProxy:     c.expandVars(c.Network.NoProxy),
		Type:        c.Network.Proxy.Type,
		Server:      c.expandVars(c.Network.Proxy.Server),
		Port:        c.Network.Proxy.Port,
		Username:    c.expandVars(c.Network.Proxy.Username),
		Password:    c.expandVars(c.Network.Proxy.Password),
		DirectHosts: c.directProxyHosts(),
	}
}

// directProxyHosts collects the base_url hosts of providers marked no_proxy, so
// netclient bypasses the proxy for them without knowing any provider by name.
//
// Only for an auto-detected proxy (auto/env): that proxy is typically a
// GFW-circumvention one not meant for domestic endpoints (e.g. mimo), so keep
// them direct. An explicit proxy_mode = "custom" is the user saying "route
// everything through this" — e.g. a mandatory corporate proxy — so honor it for
// every provider; a custom-proxy user who wants a host direct uses
// network.no_proxy instead (#3635).
func (c *Config) directProxyHosts() []string {
	if c.NetworkProxyMode() == netclient.ModeCustom {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range c.Providers {
		if !p.NoProxy {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(p.BaseURL))
		if err != nil {
			continue
		}
		if h := u.Hostname(); h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

// NetworkProxyMode normalizes network.proxy_mode to a known value.
func (c *Config) NetworkProxyMode() string {
	return netclient.NormalizeMode(c.Network.ProxyMode)
}

// SkillsConfig configures skill discovery. Paths adds extra "custom"-scope skill
// roots — each a directory of SKILL.md / <name>.md playbooks — scanned between
// the project roots (.reasonix/.agents/.agent/.claude under the workspace) and
// the global roots. ExcludedPaths hides matching discovery roots without deleting
// folders. ~, relative paths, and ${VAR} expansion are supported. DisabledSkills
// hides named skills from the agent prompt, slash invocation, and skill tools
// while keeping them manageable. DisableImplicitInvocation keeps skills
// discoverable to the host for explicit /skill use and management, but hides
// their index and model-facing invocation tools.
type SkillsConfig struct {
	Paths                     []string `toml:"paths"`
	ExcludedPaths             []string `toml:"excluded_paths"`
	DisabledSkills            []string `toml:"disabled_skills"`
	DisableImplicitInvocation bool     `toml:"disable_implicit_invocation"`
	MaxDepth                  int      `toml:"max_depth"`
}

// ImplicitSkillInvocationEnabled reports whether the model may discover and
// invoke skills without an explicit user slash command. The zero value keeps
// the historical default enabled for old configs.
func (c *Config) ImplicitSkillInvocationEnabled() bool {
	return c == nil || !c.Skills.DisableImplicitInvocation
}

// SkillCustomPaths returns the configured custom skill roots with ${VAR}
// expanded; empty entries are dropped.
func (c *Config) SkillCustomPaths() []string {
	var out []string
	for _, p := range c.Skills.Paths {
		if p = c.expandVars(p); strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// SkillExcludedPaths returns configured skill roots that should be hidden from
// discovery, with ${VAR} expanded and empty entries dropped.
func (c *Config) SkillExcludedPaths() []string {
	var out []string
	for _, p := range c.Skills.ExcludedPaths {
		if p = c.expandVars(p); strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// SkillMaxDepth bounds nested skill discovery. Depth 3 favors bundled skill
// packs while Store keeps nested markdown safe by requiring descriptions.
func (c *Config) SkillMaxDepth() int {
	const (
		defaultDepth = 3
		maxDepth     = 5
	)
	if c == nil || c.Skills.MaxDepth == 0 {
		return defaultDepth
	}
	if c.Skills.MaxDepth < 1 {
		return 1
	}
	if c.Skills.MaxDepth > maxDepth {
		return maxDepth
	}
	return c.Skills.MaxDepth
}

// DisabledSkillNames returns valid disabled skill identifiers, preserving the
// first spelling and dropping duplicates/empty entries.
func (c *Config) DisabledSkillNames() []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range c.Skills.DisabledSkills {
		name = strings.TrimSpace(name)
		if !IsValidSkillName(name) {
			continue
		}
		key := SkillNameKey(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
	}
	return out
}

// IsSkillDisabled reports whether name is configured as disabled.
func (c *Config) IsSkillDisabled(name string) bool {
	key := SkillNameKey(name)
	if key == "" {
		return false
	}
	for _, disabled := range c.DisabledSkillNames() {
		if SkillNameKey(disabled) == key {
			return true
		}
	}
	return false
}

// SandboxConfig bounds the blast radius of tool calls (Phase 0: file-writer
// confinement). WorkspaceRoot is the directory the built-in file writers
// (write_file / edit_file / multi_edit / move_file) may modify; empty means the
// current working directory, so writes stay inside the project by default.
// AllowWrite lists extra directories writers may also touch (e.g. a sibling repo
// or a temp dir). ForbidRead lists files or directories the agent may not read or list
// (e.g. ~/.ssh for secrets). Both support ${VAR} / ${VAR:-default} expansion. Reads are
// unrestricted; confining `bash` is Phase 1 (OS-level sandbox).
type SandboxConfig struct {
	WorkspaceRoot string   `toml:"workspace_root"`
	AllowWrite    []string `toml:"allow_write"`
	ForbidRead    []string `toml:"forbid_read"`
	// Bash is the OS-sandbox mode for the bash tool: "enforce" jails each
	// command when an OS sandbox is available and refuses bash otherwise; "off"
	// runs it unconfined. Empty uses the platform default.
	Bash string `toml:"bash"`
	// Network allows network egress from inside the bash sandbox. Defaults true
	// so module/package downloads keep working; the boundary is then writes.
	Network bool `toml:"network"`
}

// WriteRoots returns the directories file-writer tools may modify: the
// workspace root (defaulting to the current working directory when unset), plus
// any AllowWrite extras, with ${VAR} expanded. The roots are returned as given
// (relative or absolute); the confiner resolves them to absolute, symlink-free
// paths. The result is always non-empty, so confinement is on by default.
func (c *Config) WriteRoots() []string {
	return c.WriteRootsForRoot(".")
}

// WriteRootsForRoot is like WriteRoots but falls back to fallbackRoot when the
// config doesn't explicitly set a workspace_root. Desktop tabs pass their
// project root here so tool confinement is correct without changing cwd.
func (c *Config) WriteRootsForRoot(fallbackRoot string) []string {
	root := c.expandVars(c.Sandbox.WorkspaceRoot)
	if root == "" {
		root = fallbackRoot
		if root == "" || root == "." {
			if wd, err := os.Getwd(); err == nil {
				root = wd
			} else {
				root = "."
			}
		}
	}
	roots := []string{root}
	for _, d := range c.Sandbox.AllowWrite {
		if d = c.expandVars(d); d != "" {
			roots = append(roots, d)
		}
	}
	return roots
}

// AllowWriteRoots returns only the configured [sandbox] allow_write extras with
// ${VAR} expanded — the explicit escape-hatch entries, without the workspace
// root that WriteRoots prepends. The session-data write guard treats these as
// user-sanctioned raw access.
func (c *Config) AllowWriteRoots() []string {
	var roots []string
	for _, d := range c.Sandbox.AllowWrite {
		if d = c.expandVars(d); d != "" {
			roots = append(roots, d)
		}
	}
	return roots
}

// ForbidReadRoots returns the paths the agent is forbidden from reading
// or listing, with ${VAR} expanded. Relative roots are resolved against the
// current working directory; the confiner resolves them to symlink-free paths.
// Empty when no forbid_read entries are configured.
func (c *Config) ForbidReadRoots() []string {
	return c.ForbidReadRootsForRoot(".")
}

// ForbidReadRootsForRoot is like ForbidReadRoots but uses fallbackRoot when
// resolving relative paths (for desktop tabs that pass their project root).
func (c *Config) ForbidReadRootsForRoot(fallbackRoot string) []string {
	root := fallbackRoot
	if root == "" || root == "." {
		if wd, err := os.Getwd(); err == nil {
			root = wd
		} else {
			root = "."
		}
	}
	roots := make([]string, 0, len(c.Sandbox.ForbidRead))
	for _, d := range c.Sandbox.ForbidRead {
		if d = c.expandVars(d); d != "" {
			if !filepath.IsAbs(d) {
				d = filepath.Join(root, d)
			}
			roots = append(roots, d)
		}
	}
	return roots
}

// BashMode normalises the bash-sandbox mode for the current host.
func (c *Config) BashMode() string {
	return c.BashModeForGOOS(runtimeGOOS)
}

// BashModeForGOOS normalises the bash-sandbox mode for tests and cross-platform
// rendering. Windows has no OS-level Bash sandbox and forces the effective mode
// off, even when older configs explicitly requested "enforce". macOS/Linux keep
// the existing explicit-mode behavior.
func (c *Config) BashModeForGOOS(goos string) string {
	if goos == "windows" {
		return "off"
	}
	switch strings.TrimSpace(c.Sandbox.Bash) {
	case "enforce":
		return "enforce"
	case "off":
		return "off"
	case "":
		return "enforce"
	default:
		return "enforce"
	}
}

// AgentConfig configures the harness loop. PlannerModel is optional: when set
// to another provider's name it enables two-model collaboration, where the
// planner handles low-frequency planning in its own session (kept separate so
// each model's prompt prefix stays cache-stable). SubagentModel is the optional
// default for runAs=subagent skills; SubagentModels overrides it per skill name.
type AgentConfig struct {
	SystemPrompt     string `toml:"system_prompt"`
	SystemPromptFile string `toml:"system_prompt_file"`
	// Deprecated compatibility fields. Old TOML and desktop clients may still
	// send them, but config loading normalizes both to zero and rendering omits
	// them. One-off CLI and unattended bot limits remain separate controls.
	MaxSteps        int     `toml:"max_steps"`
	PlannerMaxSteps int     `toml:"planner_max_steps"`
	Temperature     float64 `toml:"temperature"`
	PlannerModel    string  `toml:"planner_model"`
	WebSearchModel  string  `toml:"web_search_model"` // empty or auto preserves automatic search selection
	// VisionModel is empty (off), "auto", or a canonical provider/model ref
	// used to summarize images before a text-only executor turn.
	VisionModel         string  `toml:"vision_model"`
	GuardianModel       string  `toml:"guardian_model"`
	GuardianTemperature float64 `toml:"guardian_temperature"`
	// RecoveryModel names the optional recovery reviewer. Empty leaves
	// rule-only recovery; it is not implied by guardian or the main model.
	RecoveryModel string `toml:"recovery_model"`
	// RecoveryTemperature is accepted from older configs but ignored. Auto
	// Guard review is deterministic at temperature zero.
	RecoveryTemperature float64           `toml:"recovery_temperature"`
	SubagentModel       string            `toml:"subagent_model"`
	SubagentModels      map[string]string `toml:"subagent_models"`
	SubagentEffort      string            `toml:"subagent_effort"`
	SubagentEfforts     map[string]string `toml:"subagent_efforts"`
	MaxSubagentDepth    int               `toml:"max_subagent_depth"`
	// TaskCostBudget lands a task on one summary once it spends this much.
	TaskCostBudget float64 `toml:"task_cost_budget"`
	// TaskTimeBudgetMinutes is the same gate on wall clock. Both ship off.
	TaskTimeBudgetMinutes float64 `toml:"task_time_budget_minutes"`
	// GoalTokenBudget bounds an unattended Goal loop by cumulative tokens.
	// Off unless set: a Goal runs until it finishes or you stop it.
	GoalTokenBudget int `toml:"goal_token_budget"`
	// MaxSubagentConcurrency bounds how many sub-agents (task, fleet items,
	// profile skills, nested children) may run at once in one session.
	// 0 means the default (6). Values outside 1–32 are clamped on load.
	MaxSubagentConcurrency int `toml:"max_subagent_concurrency"`
	// MaxParallelWriters bounds concurrent writer-capable sub-agents that
	// declare non-overlapping write_paths. 0 means the default (3). Must not
	// exceed MaxSubagentConcurrency after normalization.
	MaxParallelWriters int `toml:"max_parallel_writers"`
	// OutputStyle selects a persona/tone block folded into the system prompt at
	// startup (a built-in like "explanatory"/"learning"/"concise", or a custom
	// .reasonix/output-styles/<name>.md). Empty = the unmodified prompt.
	OutputStyle string `toml:"output_style"`
	// Deprecated compatibility field. Automatic plan mode was retired in config
	// version 5; old TOML remains readable, but loading normalizes it to "off"
	// and rendering omits it. Plan mode remains available as an explicit user
	// choice.
	AutoPlan string `toml:"auto_plan"`
	// ReasoningLanguage controls the preferred language for visible reasoning
	// text. Empty/auto follows the conversation language. Applied as transient
	// turn context, not the stable prompt.
	ReasoningLanguage string `toml:"reasoning_language"`
	// Deprecated compatibility field paired with AutoPlan. Old TOML remains
	// readable, but loading clears it and rendering omits it.
	AutoPlanClassifier string `toml:"auto_plan_classifier"`
	// Soft/snip/force are retired compatibility keys; only CompactRatio is active.
	SoftCompactRatio    float64 `toml:"soft_compact_ratio"`
	ToolResultSnipRatio float64 `toml:"tool_result_snip_ratio"`
	CompactRatio        float64 `toml:"compact_ratio"`
	CompactForceRatio   float64 `toml:"compact_force_ratio"`
	// ContextEditing is retired; native tool clearing is no longer an auto path.
	ContextEditing string `toml:"context_editing"`
	// Keep and RecentKeep are deprecated compatibility fields. They remain
	// readable and writable but Harness-style compaction ignores them.
	Keep       []string `toml:"keep"`
	RecentKeep int      `toml:"recent_keep"`
	// ColdResumePrune elides stale tool results when a session reopens past the
	// provider cache window. nil = default enabled.
	ColdResumePrune *bool `toml:"cold_resume_prune"`
	// PlanModeReadOnlyCommands is retained for old config/session round trips. Main
	// Plan bash calls now use the ordinary Permissions classifier and Sandbox.
	PlanModeReadOnlyCommands []string `toml:"plan_mode_read_only_commands"`
	LegacyAnchorSafetyGate   bool     `toml:"legacy_anchor_safety_gate"`  // user-global rollback to the full-read guard
	CompletionValidation     string   `toml:"completion_validation"`      // retired; retained for old config reads
	CompletionEvaluatorModel string   `toml:"completion_evaluator_model"` // retired; ignored
}

// ProviderEntry declares a model provider instance. ContextWindow is the model's
// token budget; the harness compacts older history as a turn's prompt approaches
// it (see agent compaction). 0 disables compaction for the instance.
type ProviderEntry struct {
	DisplayName   string            `toml:"display_name,omitempty"` // UI label; Name remains the stable routing identity.
	Name          string            `toml:"name"`
	Kind          string            `toml:"kind"`
	BaseURL       string            `toml:"base_url"`
	ChatURL       string            `toml:"chat_url"`    // legacy OpenAI chat endpoint override; retained with its historical semantics
	RequestURL    string            `toml:"request_url"` // exact provider request URL written by current settings UI
	Model         string            `toml:"model"`       // a single model (back-compat)
	Models        []string          `toml:"models"`      // a vendor's model list (one base_url/key, many models)
	ModelsURL     string            `toml:"models_url"`  // auto-fetch models from this URL on startup
	Default       string            `toml:"default"`     // default model when Models is set (else Models[0])
	APIKeyEnv     string            `toml:"api_key_env"`
	PresetID      string            `toml:"preset_id"`      // curated preset identity; UI-only metadata, not sent to model providers.
	PresetVersion int               `toml:"preset_version"` // curated preset schema version for future migrations.
	Headers       map[string]string `toml:"headers"`        // optional extra HTTP headers for compatible gateways; secrets should stay in api_key_env.
	ExtraBody     map[string]any    `toml:"extra_body"`     // optional extra top-level JSON request body fields for OpenAI-compatible gateways.
	AuthHeader    bool              `toml:"auth_header"`    // for Anthropic-compatible gateways that expect Authorization: Bearer instead of x-api-key.
	// ResponsesMode selects the Responses API context strategy. Empty preserves
	// vendor detection; DeepSeek is stateless while compatible endpoints may use
	// stateful previous_response_id continuation.
	ResponsesMode string `toml:"responses_mode"`
	// ResponsesStateful is the legacy boolean form retained for config
	// compatibility. ResponsesMode wins when both are present.
	ResponsesStateful  *bool `toml:"responses_stateful"`
	resolvedAPIKey     string
	credentialsFrozen  bool
	credentialProxyURL string // runtime-only loopback transport, never persisted
	resolvedSource     CredentialSource
	cacheContext       string // workspace user attribution id (Config.CacheContext), set at load; sent as user_id
	sessionContext     string // derived per-workspace OpenRouter session_id, set at load; sent as session_id
	BalanceURL         string `toml:"balance_url"` // optional; a provider-specific wallet-balance endpoint (DeepSeek: https://api.deepseek.com/user/balance). Empty = no balance readout.
	ContextWindow      int    `toml:"context_window"`
	// MaxOutputTokens is a protocol-neutral total output budget for one turn.
	// Zero means official DeepSeek omits the field (server 384K ceiling) and
	// other vendors keep their own defaults. Effort selects thinking depth only.
	// A positive value is an explicit cost cap. A negative value omits optional
	// wire limits when the protocol allows; official DeepSeek Anthropic still
	// sends 384K because max_tokens is mandatory. Never feeds compact_ratio.
	MaxOutputTokens int                          `toml:"max_output_tokens"`
	Price           *provider.Pricing            `toml:"price"`  // legacy/provider-wide fallback
	Prices          map[string]*provider.Pricing `toml:"prices"` // optional per-model prices; keys are model ids
	// BillingCurrency is the frozen list-price currency (ISO-4217). Independent
	// of [billing].display_currency; switching display never rewrites this.
	BillingCurrency string `toml:"billing_currency"`
	// BillingMode is payg (default) or subscription_equivalent (e.g. MiMo Token Plan).
	BillingMode string `toml:"billing_mode"`

	persistedOfficialCurrency string

	// Thinking / Effort are provider-kind-specific knobs forwarded to the provider
	// via Config.Extra. The anthropic provider reads Thinking="adaptive" to enable
	// extended thinking and Effort ("low".."max") to tune depth. The
	// openai-compatible provider forwards Effort as reasoning_effort for
	// thinking-capable models; DeepSeek V4 Flash accepts low|high|max while
	// other DeepSeek models retain their model-specific capability mapping.
	// Empty = provider default.
	Thinking string `toml:"thinking"`
	Effort   string `toml:"effort"`
	// Vision marks the model as accepting image input. When set, images the user
	// attaches are embedded in the request (image_url for openai-kind, base64
	// blocks for anthropic). Off by default: text-only models 400 on image input,
	// and image tokens are heavy — gating keeps text-only flows cheap (the prompt
	// prefix is byte-identical with no image, so the cache is unaffected either way).
	Vision bool `toml:"vision"`
	// VisionModels is legacy; new settings use model-level ModelOverrides.Vision.
	// Keep this field readable for existing configurations.
	VisionModels []string `toml:"vision_models"`
	// VisionDetail sets the openai image_url detail hint (low|high); empty = auto
	// (the field is omitted). "low" caps an image to a fixed ~85 tokens for cheap
	// coarse reads; ignored by providers without the knob (e.g. anthropic).
	VisionDetail string `toml:"vision_detail"`
	// WebSearch enables independent search with this account. Nil uses the
	// official DeepSeek default; explicit values and legacy native search
	// history survive config rewrites.
	WebSearch *bool `toml:"web_search"`
	// ReasoningProtocol selects the request shape for OpenAI-compatible reasoning
	// models. Empty/auto uses the model capability registry plus endpoint
	// heuristics. Explicit values select DeepSeek, GLM, Kimi K3, or standard
	// OpenAI reasoning contracts; none disables automatic reasoning controls.
	ReasoningProtocol string `toml:"reasoning_protocol"`
	// SupportedEfforts lists the /effort levels this provider/model exposes.
	// Non-empty values override built-in Kind/BaseURL defaults except for fixed
	// Kimi K3 reasoning. "auto" is the implicit prefix — always accepted.
	// DefaultEffort resolves it; omit DefaultEffort (or set one outside this
	// list) to fall back to SupportedEfforts[0].
	SupportedEfforts []string `toml:"supported_efforts"`
	// DefaultEffort is the /effort level used when the user picks "auto" or
	// has not set Effort. Ignored for empty SupportedEfforts or fixed Kimi K3.
	DefaultEffort string `toml:"default_effort"`
	// ModelOverrides customizes capability metadata after ResolveModel selects a
	// concrete model from a multi-model provider. Use it when a gateway exposes
	// mixed DeepSeek/OpenAI/no-reasoning or mixed vision/text models under one
	// base_url/key.
	ModelOverrides map[string]ProviderModelOverride `toml:"model_overrides"`
	visionOverride *bool
	// NoProxy reaches this provider's base_url directly, never through the proxy.
	// For China-only endpoints a foreign-exit proxy resets the TLS handshake (#2803).
	NoProxy bool `toml:"no_proxy"`
	// CacheTTLMinutes overrides the vendor-default prefix-cache retention used by
	// cold-resume prune. Zero uses the vendor default (DeepSeek/unknown 24h, DashScope/Anthropic 5m).
	CacheTTLMinutes int `toml:"cache_ttl_minutes"`
}

type ProviderModelOverride struct {
	ReasoningProtocol string   `toml:"reasoning_protocol"`
	SupportedEfforts  []string `toml:"supported_efforts"`
	DefaultEffort     string   `toml:"default_effort"`
	Vision            *bool    `toml:"vision"`
	// ContextWindow overrides the provider-wide context budget for this model.
	// Zero inherits ProviderEntry.ContextWindow so existing configurations keep
	// their current compaction behavior.
	ContextWindow int `toml:"context_window"`
	// MaxOutputTokens overrides the provider-wide output budget. Zero inherits;
	// positive values set a cap and negative values omit optional wire limits.
	MaxOutputTokens int `toml:"max_output_tokens"`
}

// ModelList returns the models this provider exposes: the explicit `models` list,
// or the single `model` as a one-element list (back-compat). Empty if neither set.
func (e *ProviderEntry) ModelList() []string {
	if len(e.Models) > 0 {
		return e.Models
	}
	if e.Model != "" {
		return []string{e.Model}
	}
	return nil
}

// IsLikelyChatModel reports whether a model ID looks like a chat/completion
// model rather than a specialised audio/vision/embedding model. It applies a
// conservative name-based heuristic — the OpenAI-compatible /models API does
// not return capability/modality metadata, so this is the most reliable
// fallback until providers add such fields.
//
// The heuristic works in two passes:
//  1. Multi-word substring check for compound terms that span separators
//     (e.g. "text-embedding", "text-to-speech").
//  2. Token-level check: the model ID is split on common separators (- _ . / :)
//     and each token is compared against a set of known non-chat keywords.
//
// "voice" is intentionally absent from the non-chat set because it is too
// broad — legitimate future chat models may include it in their name.
func IsLikelyChatModel(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	lower := strings.ToLower(model)

	// Pass 1: compound terms that span separator boundaries.
	var compoundNonChat = []string{
		"text-embedding", "text-to-speech", "speech-to-text",
	}
	for _, c := range compoundNonChat {
		if strings.Contains(lower, c) {
			return false
		}
	}

	// Pass 2: token-level check.
	tokens := strings.FieldsFunc(lower, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == '/' || r == ':'
	})
	var nonChatTokens = map[string]bool{
		"asr": true, "stt": true, "tts": true,
		"whisper": true, "embedding": true,
		"moderation": true, "rerank": true, "dall": true,
		"transcription": true,
	}
	for _, tok := range tokens {
		if nonChatTokens[tok] {
			return false
		}
	}
	return true
}

// ChatModelList returns ModelList filtered to likely chat/completion models.
// Non-chat models (TTS, STT, ASR, embedding, etc.) are excluded so they do
// not appear in the chat model picker. Use ModelList() only when the full
// raw provider model list is needed, such as config serialization, provider
// diagnostics, or model-fetch editing.
func (e *ProviderEntry) ChatModelList() []string {
	raw := e.ModelList()
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, m := range raw {
		if IsLikelyChatModel(m) {
			out = append(out, m)
		}
	}
	return out
}

// DefaultModel returns the provider's default model: the explicit `default`, else
// the first of ModelList.
func (e *ProviderEntry) DefaultModel() string {
	if e.Default != "" {
		return e.Default
	}
	if l := e.ModelList(); len(l) > 0 {
		return l[0]
	}
	return ""
}

// HasModel reports whether m is one of the provider's models.
func (e *ProviderEntry) HasModel(m string) bool {
	return slices.Contains(e.ModelList(), m)
}

// PriceForModel returns the configured per-1M-token price for model. Per-model
// prices win; the legacy provider-wide price is a fallback for older configs.
func (e *ProviderEntry) PriceForModel(model string) *provider.Pricing {
	if e == nil {
		return nil
	}
	if e.Prices != nil {
		if p := e.Prices[strings.TrimSpace(model)]; p != nil {
			return clonePricing(p)
		}
	}
	return clonePricing(e.Price)
}

func (e *ProviderEntry) applyModelPrice() {
	if e == nil {
		return
	}
	e.Price = e.PriceForModel(e.Model)
}

func (e *ProviderEntry) applyModelOverride() {
	if e == nil || len(e.ModelOverrides) == 0 {
		return
	}
	ov, ok := e.modelOverrideForModel(e.Model)
	if !ok {
		return
	}
	if ov.ReasoningProtocol != "" {
		e.ReasoningProtocol = ov.ReasoningProtocol
	}
	if ov.SupportedEfforts != nil {
		e.SupportedEfforts = append([]string(nil), ov.SupportedEfforts...)
	}
	if ov.DefaultEffort != "" || ov.SupportedEfforts != nil {
		e.DefaultEffort = ov.DefaultEffort
	}
	if ov.Vision != nil {
		e.visionOverride = ov.Vision
	}
	if ov.ContextWindow > 0 {
		e.ContextWindow = ov.ContextWindow
	}
	if ov.MaxOutputTokens != 0 {
		e.MaxOutputTokens = ov.MaxOutputTokens
	}
}

func (e *ProviderEntry) modelOverrideForModel(model string) (ProviderModelOverride, bool) {
	model = strings.TrimSpace(model)
	if e == nil || model == "" || len(e.ModelOverrides) == 0 {
		return ProviderModelOverride{}, false
	}
	if ov, ok := e.ModelOverrides[model]; ok {
		return ov, true
	}
	return ProviderModelOverride{}, false
}

func clonePricing(p *provider.Pricing) *provider.Pricing {
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

// ToolsConfig selects which built-in tools are enabled. Empty means all of them.
type ToolsConfig struct {
	Enabled                  []string             `toml:"enabled"`
	BashTimeoutSeconds       *int                 `toml:"bash_timeout_seconds"`
	MCPStartupTimeoutSeconds *int                 `toml:"mcp_startup_timeout_seconds"`
	MCPCallTimeoutSeconds    *int                 `toml:"mcp_call_timeout_seconds"`
	BackgroundJobs           BackgroundJobsConfig `toml:"background_jobs"`
	Search                   SearchConfig         `toml:"search"`
	Shell                    ShellConfig          `toml:"shell"`
}

const (
	defaultBashTimeoutSeconds             = 120
	defaultMCPStartupTimeoutSeconds       = 30
	defaultMCPCallTimeoutSeconds          = 300
	defaultBackgroundJobStalledWarningSec = 900
	maxBackgroundJobStalledWarningSec     = 86400
)

// BashTimeoutSeconds returns the foreground bash timeout in seconds. An omitted
// config keeps the historical 120s safety cap, explicit 0 disables the
// tool-local cap, and positive values set a custom cap. Negative values fall
// back to the default so a typo cannot silently remove the safety net.
func (c *Config) BashTimeoutSeconds() int {
	if c.Tools.BashTimeoutSeconds == nil || *c.Tools.BashTimeoutSeconds < 0 {
		return defaultBashTimeoutSeconds
	}
	return *c.Tools.BashTimeoutSeconds
}

// MCPCallTimeoutSeconds returns the default MCP JSON-RPC call timeout in
// seconds. Omitted, zero, and negative values keep the built-in safety cap so a
// hung MCP server cannot block a turn indefinitely.
func (c *Config) MCPCallTimeoutSeconds() int {
	if c.Tools.MCPCallTimeoutSeconds == nil || *c.Tools.MCPCallTimeoutSeconds <= 0 {
		return defaultMCPCallTimeoutSeconds
	}
	return *c.Tools.MCPCallTimeoutSeconds
}

// MCPStartupTimeoutSeconds returns the background initialize + tools/list
// safety cap. Omitted, zero, and negative values keep the built-in default so
// a slow but healthy MCP can outlive the short interactive wait without running
// indefinitely.
func (c *Config) MCPStartupTimeoutSeconds() int {
	if c.Tools.MCPStartupTimeoutSeconds == nil || *c.Tools.MCPStartupTimeoutSeconds <= 0 {
		return defaultMCPStartupTimeoutSeconds
	}
	return *c.Tools.MCPStartupTimeoutSeconds
}

// BackgroundJobsConfig tunes parent-created background jobs.
type BackgroundJobsConfig struct {
	StalledWarningSeconds *int `toml:"stalled_warning_seconds"`
}

// BackgroundJobStalledWarningSeconds returns the stalled warning threshold in
// seconds. Omitted/negative values keep the default, explicit 0 disables the
// notice, and oversized values clamp to one day so a typo cannot become
// effectively invisible.
func (c *Config) BackgroundJobStalledWarningSeconds() int {
	if c.Tools.BackgroundJobs.StalledWarningSeconds == nil || *c.Tools.BackgroundJobs.StalledWarningSeconds < 0 {
		return defaultBackgroundJobStalledWarningSec
	}
	if *c.Tools.BackgroundJobs.StalledWarningSeconds > maxBackgroundJobStalledWarningSec {
		return maxBackgroundJobStalledWarningSec
	}
	return *c.Tools.BackgroundJobs.StalledWarningSeconds
}

// SearchConfig tunes the grep tool's engine. Engine is "auto" (default — use
// ripgrep when it's on PATH, else the native Go scanner), "native" (always Go),
// or "rg" (require ripgrep; warn at startup and fall back to native if absent).
// RgPath optionally points at a specific ripgrep binary instead of a PATH lookup.
type SearchConfig struct {
	Engine string `toml:"engine"`
	RgPath string `toml:"rg_path"`
}

// ShellConfig chooses the interpreter the bash tool runs commands under. Prefer
// is "auto" (default — real bash when present, else PowerShell on Windows),
// "bash", or "powershell"/"pwsh" (force it; warn at startup and fall back to
// auto if absent). Path optionally points at a specific shell executable.
type ShellConfig struct {
	Prefer string `toml:"prefer"`
	Path   string `toml:"path"`
}

// PermissionsConfig declares the per-call permission policy (see
// internal/permission). Mode is the fallback decision for writer tools when no
// rule matches ("ask" | "allow" | "deny"; default "ask"); read-only tools always
// fall back to allow. Allow/Ask/Deny are rule lists of the form "ToolName" or
// "ToolName(glob)". Precedence: deny > ask > allow > fallback.
type PermissionsConfig struct {
	Mode             string   `toml:"mode"`
	Allow            []string `toml:"allow"`
	Ask              []string `toml:"ask"`
	Deny             []string `toml:"deny"`
	AllowDynamicBash bool     `toml:"allow_dynamic_bash"`
}

// MCPConfigSource records where a merged MCP entry came from. It is runtime
// provenance only and is never serialized back into TOML or .mcp.json.
type MCPConfigSource string

const (
	MCPSourceUnknown        MCPConfigSource = ""
	MCPSourceUserConfig     MCPConfigSource = "user_config"
	MCPSourceProjectConfig  MCPConfigSource = "project_config"
	MCPSourceProjectMCPJSON MCPConfigSource = "project_mcp_json"
	MCPSourceLegacyUser     MCPConfigSource = "legacy_user_config"
	MCPSourcePluginPackage  MCPConfigSource = "plugin_package"
)

func (s MCPConfigSource) UserAuthorized() bool {
	switch s {
	case MCPSourceUserConfig, MCPSourceLegacyUser, MCPSourcePluginPackage,
		MCPSourceProjectConfig, MCPSourceProjectMCPJSON:
		return true
	default:
		return false
	}
}

// ProjectScoped reports whether an MCP entry belongs to one workspace. Project
// scope remains useful for provenance, activation, and relative-path handling;
// it no longer implies a separate launch-approval workflow.
func (s MCPConfigSource) ProjectScoped() bool {
	return s == MCPSourceProjectConfig || s == MCPSourceProjectMCPJSON
}

func (e PluginEntry) ShouldAutoStart() bool {
	return e.AutoStart == nil || *e.AutoStart
}

// ResolvedTier returns the normalized tier ("eager"|"background") with the
// project default applied. Legacy lazy and unknown values fall back to
// background so enabled MCPs are available without manual connection.
//
// Tier no longer changes runtime process start timing; it remains for config
// compatibility and diagnostics only.
func (e PluginEntry) ResolvedTier() string {
	return resolvedMCPTier(e.Tier)
}

func resolvedMCPTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "eager":
		return "eager"
	case "background", "lazy":
		return "background"
	case "":
		return "background"
	default:
		return "background"
	}
}

// AutoStartPlugins returns enabled MCP entries for the catalog. Durable
// enable/disable overrides in mcp-activation.json take precedence over the
// legacy auto_start field. auto_start=false without an override still maps to
// disabled; true/nil map to enabled. "Auto start" no longer means "spawn the
// process at session boot" — enabled servers register cached tools and start
// on first real tool call.
func (c *Config) AutoStartPlugins() []PluginEntry {
	return c.EnabledPlugins("", DefaultMCPActivationStore())
}

// EnabledPlugins returns catalog-enabled MCP entries for workspace, consulting
// the activation store when provided.
func (c *Config) EnabledPlugins(workspace string, activation *MCPActivationStore) []PluginEntry {
	if c == nil {
		return nil
	}
	out := make([]PluginEntry, 0, len(c.Plugins))
	for _, p := range c.Plugins {
		enabled := p.ShouldAutoStart()
		if activation != nil {
			if resolved, err := activation.IsEnabled(p, workspace); err == nil {
				enabled = resolved
			}
		}
		if enabled {
			out = append(out, p)
		}
	}
	return out
}

// DefaultSystemPrompt is used when config provides none.
const DefaultSystemPrompt = `You are Reasonix, a coding agent.
Use the available tools when they help you complete the user's request.
Keep changes focused and responses concise.`

// UserDecisionPolicy is appended to every system prompt, including user-custom
// prompts, so custom personas cannot accidentally remove the `ask` UI contract.
const UserDecisionPolicy = `User-owned choices: when a consequential decision has no safe, obvious default, call the ask tool so the user can choose. Otherwise proceed with a sensible reversible default. Do not ask in prose when ask is available. In non-interactive runs, state the assumption and take the safest reversible path.`

// LanguagePolicy is the auto fallback appended to the system prompt when no
// concrete UI language is resolved. It is static English text, so it stays part
// of the cache-stable prefix and avoids per-turn language injection.
const LanguagePolicy = `Reply in the same language the user is using in their most recent message: ` +
	`if they write in Chinese answer in Chinese, in English answer in English, and switch ` +
	`whenever they switch. Let this also guide the language you think in. Always keep code, ` +
	`identifiers, file paths, shell commands, and technical terms in their original form — never translate them.`

// Default returns the built-in default configuration.
func Default() *Config {
	return &Config{
		ConfigVersion:    10,
		DefaultModel:     "deepseek-flash",
		CredentialsStore: CredentialsStoreAuto,
		UI:               UIConfig{Theme: "auto", ShowTurnUsage: true},
		Desktop:          DesktopConfig{DefaultToolApprovalMode: "auto", ConversationWidth: "standard"},
		Billing:          BillingConfig{},
		Notifications: NotificationsConfig{
			Enabled:         false,
			TurnDone:        true,
			ApprovalRequest: true,
			AskRequest:      true,
		},
		Agent: AgentConfig{
			SystemPrompt: DefaultSystemPrompt,
			// Normal interactive execution has no configurable total round cap. It
			// is bounded by adaptive progress guards and context compaction instead.
			MaxSteps:        0,
			PlannerMaxSteps: 0,
			AutoPlan:        "off",
			// Soft/snip/force are load-only compatibility; CompactRatio alone drives maintenance.
			SoftCompactRatio:       0,
			ToolResultSnipRatio:    0,
			CompactRatio:           0.80,
			CompactForceRatio:      0,
			ContextEditing:         "",
			MaxSubagentDepth:       2,
			MaxSubagentConcurrency: 6,
			MaxParallelWriters:     3,
		},
		// Mode "ask" with no rules keeps `reasonix run` autonomous (no TTY → ask
		// resolves to allow) while `reasonix` prompts before writers. Users add
		// deny/allow rules to harden or quiet specific tools.
		Permissions: PermissionsConfig{Mode: "ask"},
		// Sandbox uses platform defaults: macOS/Linux jail bash by default;
		// Windows has no OS-level Bash sandbox and always forces bash off.
		// Network=true here so an absent [sandbox] in a user's file keeps egress
		// (zero value would wrongly deny it).
		Sandbox: SandboxConfig{Network: true},
		// LSP tools on by default, but dormant until a language server is on PATH;
		// a missing server yields an install hint rather than an error.
		LSP:     LSPConfig{Enabled: true},
		Network: NetworkConfig{ProxyMode: netclient.ModeAuto},
		Bot: BotConfig{
			ToolApprovalMode:   "ask",
			MaxSteps:           0,
			DebounceMs:         1500,
			QueueMode:          "steer",
			QueueCap:           20,
			QueueDrop:          "summarize",
			IgnoreSelfMessages: true,
			Control:            BotControlConfig{Addr: "127.0.0.1:37913", TokenEnv: "REASONIX_BOT_CONTROL_TOKEN"},
			Pairing:            BotPairingConfig{Enabled: true, RequestTTLMinutes: 60, MaxPendingPerPlatform: 3},
			Allowlist:          BotAllowlist{Enabled: true},
			QQ:                 QQBotConfig{AppSecretEnv: "QQ_BOT_APP_SECRET"},
			Feishu:             FeishuBotConfig{Domain: "feishu", AppSecretEnv: "FEISHU_BOT_APP_SECRET", Mode: "webhook", WebhookPort: 8080, RequireMention: true},
			Dingtalk:           DingtalkBotConfig{RequireMention: true},
			Weixin:             WeixinBotConfig{AccountID: "default", TokenEnv: "WEIXIN_BOT_TOKEN", APIBase: "https://ilinkai.weixin.qq.com"},
		},
		// Main conversations use Chat Completions; independent web_search uses
		// the official Messages endpoint with the same account.
		Providers: []ProviderEntry{
			{
				Name: "deepseek-flash", Kind: "openai", BaseURL: "https://api.deepseek.com",
				Model: "deepseek-v4-flash", APIKeyEnv: "DEEPSEEK_API_KEY",
				BalanceURL: "https://api.deepseek.com/user/balance", Thinking: "enabled",
				WebSearch: boolPointer(true), SupportedEfforts: []string{"disabled", "low", "high", "max"}, DefaultEffort: "high",
				ContextWindow: 1_000_000, Price: deepSeekV4FlashPriceUSD(),
				BillingCurrency: "USD", BillingMode: "payg",
			},
			{
				Name: "deepseek-pro", Kind: "openai", BaseURL: "https://api.deepseek.com",
				Model: "deepseek-v4-pro", APIKeyEnv: "DEEPSEEK_API_KEY",
				BalanceURL: "https://api.deepseek.com/user/balance", Thinking: "enabled",
				WebSearch: boolPointer(true), SupportedEfforts: []string{"disabled", "low", "high", "max"}, DefaultEffort: "high",
				ContextWindow: 1_000_000, Price: deepSeekV4ProPriceUSD(),
				BillingCurrency: "USD", BillingMode: "payg",
			},
		},
	}
}

// WriteFile writes the configuration to path as annotated TOML. The write is
// atomic + fsynced so an interrupted write or power loss can never truncate the
// main config into an unparseable state that leaves the app with no usable
// models (#4615, #4708).
func (c *Config) WriteFile(path string) error {
	return atomicWriteToConfigFile(path, RenderTOMLForScope(c, renderScopeForPath(path)), configFilePerm(path))
}

// Provider returns the named provider entry.
func (c *Config) Provider(name string) (*ProviderEntry, bool) {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			return &c.Providers[i], true
		}
	}
	return nil, false
}

// ResolveModel resolves a model reference to a provider entry whose Model is the
// selected model string (a copy, so the config's lists stay intact). It accepts:
//   - "provider/model" — that exact model under that provider;
//   - a provider name   — the provider's default model;
//   - a bare model name — the (first) provider that lists it.
//
// The returned entry is ready to build a provider from (NewProvider reads .Model),
// so a single "vendor with many models" entry yields one instance per model
// without duplicating base_url/api_key_env. Single-`model` entries still resolve
// by provider name, keeping older configs working unchanged.
func (c *Config) ResolveModel(ref string) (*ProviderEntry, bool) {
	if entry, ok := c.resolveCurrentModel(ref); ok {
		return entry, true
	}
	target, err := c.resolveOpenCodeGoAlias(ref, false)
	if err != nil || target == ref {
		return nil, false
	}
	return c.resolveCurrentModel(target)
}

func (c *Config) resolveCurrentModel(ref string) (*ProviderEntry, bool) {
	if ref == "" {
		return nil, false
	}
	if access := desktopProviderAccessMap(c.Desktop.ProviderAccess); len(access) > 0 {
		if access["deepseek"] && !canCanonicalizeLegacyDeepSeekProviders(c) {
			delete(access, "deepseek")
		}
		ref = retargetDesktopOfficialRef(ref, access)
	}
	// "provider/model"
	if prov, model, ok := strings.Cut(ref, "/"); ok {
		if e, found := c.Provider(prov); found && e.HasModel(model) {
			cp := *e
			cp.Model = model
			cp.applyModelPrice()
			cp.applyModelOverride()
			return &cp, true
		}
	}
	// a provider name → its default model
	if e, found := c.Provider(ref); found {
		cp := *e
		cp.Model = e.DefaultModel()
		cp.applyModelPrice()
		cp.applyModelOverride()
		return &cp, true
	}
	// a bare model name → the provider that lists it
	for i := range c.Providers {
		if c.Providers[i].HasModel(ref) {
			cp := c.Providers[i]
			cp.Model = ref
			cp.applyModelPrice()
			cp.applyModelOverride()
			return &cp, true
		}
	}
	return nil, false
}

// ResolveModelWithFallback resolves a model reference to the canonical
// "provider/model" form used by the desktop runtime. If ref is stale or empty,
// it tries the user's configured default_model before falling back to the first
// configured provider — so preference isn't overwritten by iteration order.
func (c *Config) ResolveModelWithFallback(ref string) (resolvedRef string, fallback bool, ok bool) {
	ref = strings.TrimSpace(ref)
	if c.ModelReferenceError(ref) != nil {
		return "", false, false
	}
	if ref != "" {
		if e, found := c.ResolveModel(ref); found {
			return e.Name + "/" + e.Model, false, true
		}
	}
	// Before falling back to the first configured provider (which may not be the
	// user's preferred choice), try the configured default_model.  Skip when ref
	// already WAS the DefaultModel (it already failed above, so retrying won't
	// help) or when the default provider has no API key configured.
	if ref != c.DefaultModel && c.DefaultModel != "" {
		if e, found := c.ResolveModel(c.DefaultModel); found && e.Configured() {
			return e.Name + "/" + e.Model, true, true
		}
	}
	for i := range c.Providers {
		p := &c.Providers[i]
		// Skip providers with no models or no API key: falling back onto a keyless
		// provider just boots the tab onto something that fails on first use. Mirrors
		// the Configured() gate the provider-removal/selection paths already apply.
		if len(p.ModelList()) == 0 || !p.Configured() {
			continue
		}
		return p.Name + "/" + p.DefaultModel(), true, true
	}
	return "", false, false
}

// ResolveNewSessionChatModel selects the model for a newly-created chat
// session. Configured candidates win; if every chat candidate is keyless, the
// valid default (or first chat model) is preserved so callers can surface their
// existing missing-key recovery UI. An unknown default is also preserved for
// the CLI's actionable configuration error. Provider order is otherwise stable.
func (c *Config) ResolveNewSessionChatModel() (resolvedRef string, fallback bool, ok bool) {
	return c.resolveNewSessionChatModel(nil, true)
}

func (c *Config) resolveNewSessionChatModel(providerAllowed func(string) bool, preserveUnknownDefault bool) (resolvedRef string, fallback bool, ok bool) {
	if c == nil {
		return "", false, false
	}
	if providerAllowed == nil {
		providerAllowed = func(string) bool { return true }
	}

	def := strings.TrimSpace(c.DefaultModel)
	keylessDefault := ""
	if def != "" {
		if entry, found := c.ResolveModel(def); found {
			if providerAllowed(entry.Name) && IsLikelyChatModel(entry.Model) {
				if entry.Configured() {
					return def, false, true
				}
				keylessDefault = def
			}
		} else if preserveUnknownDefault {
			// CLI/boot callers need the stale value intact so their existing
			// unknown-model error can name it and explain the providers that
			// replaced it. Desktop uses its recovery UI and does not preserve it.
			return def, false, true
		}
	}

	keylessFallback := ""
	for i := range c.Providers {
		p := &c.Providers[i]
		if !providerAllowed(p.Name) {
			continue
		}
		chatModels := p.ChatModelList()
		if len(chatModels) == 0 {
			continue
		}
		model := chatModels[0]
		for _, candidate := range chatModels {
			if candidate == p.DefaultModel() {
				model = candidate
				break
			}
		}
		resolved := p.Name + "/" + model
		if p.Configured() {
			return resolved, true, true
		}
		if keylessFallback == "" {
			keylessFallback = resolved
		}
	}
	if keylessDefault != "" {
		return keylessDefault, false, true
	}
	if keylessFallback != "" {
		return keylessFallback, true, true
	}
	return "", false, false
}

// ResolveDesktopNewSessionModel selects the model for a newly-created desktop
// session. It shares the chat-model fallback policy with other frontends while
// limiting candidates to providers exposed by the desktop access catalog.
func (c *Config) ResolveDesktopNewSessionModel() (resolvedRef string, fallback bool, ok bool) {
	if c == nil {
		return "", false, false
	}
	access := desktopProviderAccessMap(c.Desktop.ProviderAccess)
	return c.resolveNewSessionChatModel(func(name string) bool {
		return c.Desktop.ProviderAccess == nil || access[strings.TrimSpace(name)]
	}, false)
}

// CacheContextValue returns the workspace user attribution id assigned at load
// (Config.CacheContext). Empty when unset or the entry was built outside a
// workspace config.
func (e *ProviderEntry) CacheContextValue() string {
	if e == nil {
		return ""
	}
	return e.cacheContext
}

// SessionContextValue returns the derived OpenRouter session_id assigned at
// load (Config.EffectiveSessionContext). Empty when the entry was built outside
// a workspace config.
func (e *ProviderEntry) SessionContextValue() string {
	if e == nil {
		return ""
	}
	return e.sessionContext
}

// APIKey resolves the entry's API key from its api_key_env.
func (e *ProviderEntry) APIKey() string {
	if e == nil {
		return ""
	}
	if e.credentialsFrozen || e.resolvedAPIKey != "" {
		return e.resolvedAPIKey
	}
	if e.APIKeyEnv == "" {
		return ""
	}
	value, _, ok := storedCredentialValue(e.APIKeyEnv)
	if !ok {
		return ""
	}
	return value
}

// ResolveAPIKeyFromProcessEnvForProbe pins a setup-time, user-entered key onto
// this entry for an immediate connectivity probe. Normal runtime resolution does
// not call this; loaded provider entries still resolve only from Reasonix's
// global .env.
func (e *ProviderEntry) ResolveAPIKeyFromProcessEnvForProbe() {
	if e == nil {
		return
	}
	key := strings.TrimSpace(e.APIKeyEnv)
	if key == "" {
		return
	}
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return
	}
	e.resolvedAPIKey = value
	e.resolvedSource = CredentialSource{Kind: CredentialSourceEnvironment, Label: "setup prompt"}
}

func (e *ProviderEntry) APIKeySourceLabel() string {
	if e == nil || strings.TrimSpace(e.APIKeyEnv) == "" {
		return ""
	}
	if e.resolvedAPIKey != "" {
		return credentialSourceLabel(e.resolvedSource)
	}
	return ResolveCredentialForRootGlobalFirst(".", e.APIKeyEnv).Source.Label
}

// RequiresAPIKey reports whether this provider should be hidden/validated when
// its configured api_key_env is empty. A blank api_key_env means the provider is
// intentionally no-auth. Local OpenAI-compatible gateways often keep a legacy
// api_key_env in config even though they accept unauthenticated requests, so
// loopback/private endpoints are also allowed to run without a resolved key.
func (e *ProviderEntry) RequiresAPIKey() bool {
	if e == nil {
		return false
	}
	if strings.TrimSpace(e.APIKeyEnv) == "" {
		return providerBaseURLRequiresAPIKey(e.BaseURL)
	}
	return !providerBaseURLAllowsMissingAPIKey(e.BaseURL)
}

func providerBaseURLRequiresAPIKey(raw string) bool {
	switch officialProviderHost(raw) {
	case "api.deepseek.com", "api.xiaomimimo.com", "token-plan-cn.xiaomimimo.com", "api.minimaxi.com", "api.openai.com":
		return true
	default:
		return false
	}
}

func providerBaseURLAllowsMissingAPIKey(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.Trim(strings.ToLower(u.Hostname()), "[]")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}

// Configured reports whether the provider is selectable. Providers that do not
// require an API key are configured by definition; providers that name an env var
// require that variable to resolve unless their endpoint is local/private.
func (e *ProviderEntry) Configured() bool {
	return e != nil && (!e.RequiresAPIKey() || e.APIKey() != "")
}

// ResolveSystemPrompt returns the system prompt, reading system_prompt_file if set.
func (c *Config) ResolveSystemPrompt() (string, error) {
	return c.ResolveSystemPromptForRoot(".")
}

// ResolveSystemPromptForRoot is like ResolveSystemPrompt but resolves a relative
// system_prompt_file against root. Desktop tabs pass their workspace root here so
// prompt files are project-scoped even when the process cwd is elsewhere. A path
// inherited from user config may fall back to Reasonix home, while a path chosen
// by project config is confined to the workspace and never probes user files.
func (c *Config) ResolveSystemPromptForRoot(root string) (string, error) {
	path := c.Agent.SystemPromptFile
	if path == "" {
		return c.InlineSystemPrompt(), nil
	}

	if c.systemPromptFileSource == promptFileSourceProject {
		if filepath.IsAbs(path) || !filepath.IsLocal(filepath.Clean(path)) {
			return "", fmt.Errorf("project system_prompt_file %q must be a relative path within the workspace", path)
		}
		candidate := filepath.Join(resolveRoot(root), path)
		b, err := readProjectSystemPromptFile(root, path)
		if err != nil {
			return "", newSystemPromptFileError(path, []string{candidate}, []error{err})
		}
		return strings.TrimSpace(string(b)), nil
	}

	if filepath.IsAbs(path) {
		b, err := fileencoding.ReadFileUTF8(path)
		if err != nil {
			return "", newSystemPromptFileError(path, []string{path}, []error{err})
		}
		return strings.TrimSpace(string(b)), nil
	}

	candidates := []string{filepath.Join(resolveRoot(root), path)}
	if home := ReasonixHomeDir(); home != "" {
		homeCandidate := filepath.Join(home, path)
		if filepath.Clean(homeCandidate) != filepath.Clean(candidates[0]) {
			candidates = append(candidates, homeCandidate)
		}
	}
	readErrors := make([]error, 0, len(candidates))
	for _, candidate := range candidates {
		b, err := fileencoding.ReadFileUTF8(candidate)
		if err == nil {
			return strings.TrimSpace(string(b)), nil
		}
		readErrors = append(readErrors, fmt.Errorf("%s: %w", candidate, err))
	}
	return "", newSystemPromptFileError(path, candidates, readErrors)
}

func readProjectSystemPromptFile(root, path string) ([]byte, error) {
	workspace, err := filepath.Abs(resolveRoot(root))
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	rootHandle, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, fmt.Errorf("open workspace root %q: %w", workspace, err)
	}
	defer rootHandle.Close()
	f, err := rootHandle.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return fileencoding.DecodeToUTF8(b), nil
}

func newSystemPromptFileError(configured string, candidates []string, readErrors []error) error {
	allMissing := len(readErrors) > 0
	for _, err := range readErrors {
		if !errors.Is(err, fs.ErrNotExist) {
			allMissing = false
			break
		}
	}
	return &systemPromptFileError{
		configured: configured,
		candidates: append([]string(nil), candidates...),
		errors:     append([]error(nil), readErrors...),
		allMissing: allMissing,
	}
}

// InlineSystemPrompt returns the configured system_prompt, or DefaultSystemPrompt
// when unset. It is the fallback when system_prompt_file cannot be read.
func (c *Config) InlineSystemPrompt() string {
	if strings.TrimSpace(c.Agent.SystemPrompt) == "" {
		return DefaultSystemPrompt
	}
	return c.Agent.SystemPrompt
}

// Validate checks that the selected model's provider is usable.
func (c *Config) Validate(model string) error {
	e, ok := c.ResolveModel(model)
	if !ok {
		return fmt.Errorf("unknown model %q (configured: %s)", model, c.providerNames())
	}
	if e.Kind == "" {
		return fmt.Errorf("provider %q: kind is required", model)
	}
	if e.BaseURL == "" {
		return fmt.Errorf("provider %q: base_url is required", model)
	}
	if strings.TrimSpace(e.APIKeyEnv) != "" && !IsValidCredentialKey(e.APIKeyEnv) {
		return fmt.Errorf("provider %q: api_key_env %q is invalid; use letters, numbers, and underscores, not a model name", model, e.APIKeyEnv)
	}
	if e.RequiresAPIKey() && e.APIKey() == "" {
		return fmt.Errorf("provider %q: missing env %s", model, e.APIKeyEnv)
	}
	return nil
}

func (c *Config) providerNames() string {
	names := make([]string, len(c.Providers))
	for i, p := range c.Providers {
		names[i] = p.Name
	}
	return strings.Join(names, ", ")
}
