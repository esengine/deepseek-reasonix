package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func isolateUserConfigHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{"REASONIX_HOME", "REASONIX_STATE_HOME", "REASONIX_CACHE_HOME"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}
	t.Setenv("REASONIX_CREDENTIALS_STORE", "file")
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	return home
}

// setRuntimeGOOS overrides the package-level runtimeGOOS for one test. The
// t.Setenv call is a guard: it panics if the test also uses t.Parallel, which
// would otherwise race on the shared global.
func setRuntimeGOOS(t *testing.T, goos string) {
	t.Helper()
	t.Setenv("REASONIX_TEST_GOOS", goos)
	old := runtimeGOOS
	runtimeGOOS = goos
	t.Cleanup(func() { runtimeGOOS = old })
}

func expectedDefaultReasonixHome(home string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(home, "AppData", "Roaming", "reasonix")
	}
	return filepath.Join(home, ".reasonix")
}

func TestUserConfigDisplayPathCollapsesHome(t *testing.T) {
	home := isolateUserConfigHome(t)
	got := userConfigDisplayPath()
	if !strings.HasPrefix(got, "~/") {
		t.Fatalf("display path = %q, want ~/ prefix", got)
	}
	if !strings.HasSuffix(got, "reasonix/config.toml") {
		t.Fatalf("display path = %q, want reasonix/config.toml suffix", got)
	}
	if strings.Contains(got, home) {
		t.Fatalf("display path %q must not embed the absolute home", got)
	}
}

func TestUserConfigPathUsesReasonixHome(t *testing.T) {
	home := isolateUserConfigHome(t)
	want := filepath.Join(expectedDefaultReasonixHome(home), "config.toml")
	if got := UserConfigPath(); filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("UserConfigPath() = %q, want %q", got, want)
	}
}

func TestReasonixManagedConfigPathsAreConfigFilesOnly(t *testing.T) {
	home := isolateUserConfigHome(t)
	setRuntimeGOOS(t, "windows")
	oldConfigDir := osUserConfigDir
	osUserConfigDir = func() string { return filepath.Join(home, "AppData", "Roaming") }
	t.Cleanup(func() { osUserConfigDir = oldConfigDir })

	paths := ReasonixManagedConfigPaths()
	for _, want := range []string{
		filepath.Join(home, "AppData", "Roaming", "reasonix", "config.toml"),
		filepath.Join(home, ".reasonix", "config.json"),
	} {
		found := false
		for _, got := range paths {
			if samePath(got, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("managed config paths = %v, want %s", paths, want)
		}
	}
	// The escape hatch is file-level by contract: no directories, and none of
	// the sensitive Reasonix-home siblings (credentials, hooks, skills,
	// sessions) may ride along.
	for _, got := range paths {
		if base := filepath.Base(got); base != "config.toml" && base != "config.json" {
			t.Fatalf("managed config path %q is not a known config file (paths must be files, not directories): %v", got, paths)
		}
		for _, forbidden := range []string{
			home,
			ReasonixHomeDir(),
			UserCredentialsPath(),
			filepath.Join(ReasonixHomeDir(), "settings.json"),
			filepath.Join(ReasonixHomeDir(), "skills"),
			filepath.Join(ReasonixHomeDir(), "sessions"),
		} {
			if samePath(got, forbidden) {
				t.Fatalf("managed config paths must not include %q: %v", forbidden, paths)
			}
		}
	}
}

func TestUserConfigPathHonorsReasonixHome(t *testing.T) {
	home := isolateUserConfigHome(t)
	custom := filepath.Join(home, "custom-home")
	t.Setenv("REASONIX_HOME", custom)

	want := filepath.Join(custom, "config.toml")
	if got := UserConfigPath(); filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("UserConfigPath() = %q, want %q", got, want)
	}
}

func TestLoadForRootUsesWindowsHomeFallbackWhenConfigDirUnavailable(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	oldGOOS := runtimeGOOS
	oldConfigDir := osUserConfigDir
	oldHomeDir := osUserHomeDir
	runtimeGOOS = "windows"
	osUserConfigDir = func() string { return "" }
	osUserHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() {
		runtimeGOOS = oldGOOS
		osUserConfigDir = oldConfigDir
		osUserHomeDir = oldHomeDir
	})

	t.Setenv("REASONIX_HOME", "")

	configPath := filepath.Join(home, "AppData", "Roaming", "reasonix", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("default_model = \"custom/from-home\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForRoot(project)
	if err != nil {
		t.Fatalf("LoadForRoot() error = %v", err)
	}
	if cfg.DefaultModel != "custom/from-home" {
		t.Fatalf("DefaultModel = %q, want %q", cfg.DefaultModel, "custom/from-home")
	}
}

func TestRenderTOMLHeaderShowsResolvedConfigPath(t *testing.T) {
	isolateUserConfigHome(t)
	out := RenderTOML(Default())
	want := "> " + userConfigDisplayPath() + " > built-in defaults."
	if !strings.Contains(out, want) {
		t.Fatalf("rendered header missing resolved config path %q", want)
	}
}

func TestWriteRootsForRootExcludesUserConfigDirByDefault(t *testing.T) {
	isolateUserConfigHome(t)
	project := t.TempDir()
	cfg := Default()

	roots := cfg.WriteRootsForRoot(project)
	want := filepath.Clean(filepath.Dir(UserConfigPath()))
	for _, root := range roots {
		if filepath.Clean(root) == want {
			t.Fatalf("WriteRootsForRoot() = %v, must not include user config dir %q by default", roots, want)
		}
	}
	if got := filepath.Clean(roots[0]); got != filepath.Clean(project) {
		t.Fatalf("first write root = %q, want project %q", got, project)
	}
}

// TestRenderTOMLRoundTrips ensures the annotated TOML we emit parses back into
// an equivalent config — i.e. the wizard never writes a file it can't read.
func TestRenderTOMLRoundTrips(t *testing.T) {
	orig := Default()
	orig.Providers = append(orig.Providers, legacyMimoCustomProvider("mimo-pro"))
	orig.DefaultModel = "mimo-pro"
	orig.Language = "zh"
	orig.UI.Theme = "light"
	orig.UI.ThemeStyle = "glacier"
	orig.UI.ShortcutLayout = "desktop"
	orig.UI.CursorShape = "bar"
	orig.Desktop.Language = "en"
	orig.Desktop.LayoutStyle = "workbench"
	orig.Desktop.Theme = "dark"
	orig.Desktop.ThemeStyle = "graphite"
	orig.Desktop.TerminalTheme = "light"
	orig.Desktop.CloseBehavior = "background"
	orig.Desktop.DisplayMode = "compact"
	orig.Desktop.StatusBarStyle = "text"
	orig.Desktop.StatusBarStyleInitialized = true
	orig.Desktop.StatusBarItems = []string{"model", "balance", "cache"}
	orig.Desktop.DefaultToolApprovalMode = "auto"
	orig.Desktop.CheckUpdates = boolPtr(false)
	orig.Desktop.UpdateChannel = "preview"
	orig.Desktop.Telemetry = boolPtr(false)
	orig.Notifications.Enabled = true
	orig.Notifications.TurnDone = true
	orig.Notifications.ApprovalRequest = true
	orig.Notifications.AskRequest = true
	orig.Agent.RecoveryModel = "mimo-pro"
	orig.Agent.RecoveryTemperature = 0.15
	orig.Agent.ReasoningLanguage = "zh"
	orig.Agent.CompactRatio = 0.8
	orig.Agent.SubagentModel = "mimo-pro"
	orig.Agent.SubagentModels = map[string]string{"review": "deepseek-pro"}
	orig.Agent.MaxSubagentDepth = 3
	orig.Agent.Keep = []string{"errors", "user_marked"}
	orig.Agent.RecentKeep = 4
	orig.Tools.BashTimeoutSeconds = intPtr(900)
	orig.Tools.BackgroundJobs.StalledWarningSeconds = intPtr(30)
	orig.Tools.Shell.Prefer = "bash"
	orig.Tools.Shell.Path = "/usr/local/bin/bash"
	orig.Permissions = PermissionsConfig{
		Mode:             "deny",
		Deny:             []string{"Bash(rm -rf*)"},
		Allow:            []string{"Bash(go test:*)", "read_file"},
		AllowDynamicBash: true,
	}
	orig.Network = NetworkConfig{
		ProxyMode: "custom",
		NoProxy:   "localhost,127.0.0.1",
		Proxy: NetworkProxyConfig{
			Type:     "socks5",
			Server:   "127.0.0.1",
			Port:     7890,
			Username: "user",
			Password: "${REASONIX_PROXY_PASSWORD}",
		},
	}
	orig.Environment.Enabled = boolPtr(false)
	orig.Environment.Tools = map[string]string{"go": "/opt/homebrew/bin/go", "python3": "~/.pyenv/shims/python3"}
	orig.Skills.Paths = []string{"~/my-skills", "../shared/skills"}
	orig.Skills.ExcludedPaths = []string{"~/.agents/skills"}
	orig.Skills.DisabledSkills = []string{"review", "explore"}
	orig.Skills.DisableImplicitInvocation = true
	orig.Skills.MaxDepth = 2
	orig.Bot.ToolApprovalMode = "auto"
	orig.Bot.Control = BotControlConfig{Enabled: true, Addr: "127.0.0.1:39001", TokenEnv: "BOT_CONTROL_TOKEN"}
	orig.Bot.Feishu.OutboundMediaRoots = []string{"/tmp/reasonix-media", "/srv/shots"}
	orig.Bot.Routes = []BotRouteConfig{{
		ConnectionID:     "feishu-lark",
		ChatType:         "group",
		ChatID:           "oc_group",
		Model:            "deepseek-pro",
		ToolApprovalMode: "ask",
		WorkspaceRoot:    "/tmp/reasonix-route",
	}}
	orig.Bot.DesktopWatchers = []BotDesktopWatcherConfig{{
		Platform:     "feishu",
		ConnectionID: "feishu-lark",
		Domain:       "lark",
		ChatType:     "dm",
		ChatID:       "oc_watcher",
	}}
	orig.Bot.Connections = []BotConnectionConfig{{
		ID:               "feishu-lark",
		Provider:         "feishu",
		Domain:           "lark",
		Label:            "Lark",
		Enabled:          true,
		Status:           "connected",
		Model:            "deepseek-pro",
		ToolApprovalMode: "yolo",
		WorkspaceRoot:    "/tmp/reasonix-bot",
		Credential:       BotConnectionCredential{AppID: "cli_lark", AppSecretEnv: "LARK_BOT_APP_SECRET"},
		SessionMappings: []BotConnectionSessionMapping{{
			RemoteID:      "ou_123",
			SessionID:     "topic:topic_bot",
			Scope:         "project",
			WorkspaceRoot: "/tmp/reasonix-bot",
			UpdatedAt:     "2026-06-11T00:00:00Z",
		}},
	}}
	orig.LSP = LSPConfig{
		Enabled: true,
		Servers: map[string]LSPServer{
			"lua": {
				Command:     "lua-language-server",
				Args:        []string{"--stdio"},
				Env:         map[string]string{"LUA_PATH": "./?.lua"},
				LanguageID:  "lua",
				Extensions:  []string{".lua", ".script", ".gui_script"},
				InstallHint: "install lua-language-server",
			},
		},
	}
	orig.Plugins = []PluginEntry{
		{Name: "example", Command: "reasonix-plugin-example"},
		{Name: "stripe", Type: "http", URL: "https://mcp.stripe.com", Headers: map[string]string{"Authorization": "Bearer x"}, AutoStart: boolPtr(false), Tier: "background"},
	}
	mm, _ := orig.Provider("mimo-pro")
	mm.BaseURL = "http://localhost:8000/v1"
	mm.ChatURL = "http://localhost:8000/v1/chat/completions"
	mm.RequestURL = "http://localhost:8000/custom/chat/completions/?token=1"
	mm.ModelsURL = "http://localhost:8000/v1/models"
	mm.ReasoningProtocol = "openai"
	mm.PresetID = "mimo-api"
	mm.PresetVersion = ProviderPresetVersion
	ds, _ := orig.Provider("deepseek-flash")
	ds.Effort = "max"

	rendered := RenderTOML(orig)

	var got Config
	if _, err := toml.Decode(rendered, &got); err != nil {
		t.Fatalf("rendered TOML does not parse: %v\n---\n%s", err, rendered)
	}

	if got.DefaultModel != "mimo-pro" {
		t.Errorf("default_model = %q, want mimo-pro", got.DefaultModel)
	}
	if got.ConfigVersion != Default().ConfigVersion {
		t.Errorf("config_version = %d, want %d", got.ConfigVersion, Default().ConfigVersion)
	}
	if got.Language != "zh" {
		t.Errorf("language = %q, want zh", got.Language)
	}
	if got.UI.Theme != "light" {
		t.Errorf("ui.theme = %q, want light", got.UI.Theme)
	}
	if got.UI.ThemeStyle != "glacier" {
		t.Errorf("ui.theme_style = %q, want glacier", got.UI.ThemeStyle)
	}
	if got.UI.ShortcutLayout != "desktop" {
		t.Errorf("ui.shortcut_layout = %q, want desktop", got.UI.ShortcutLayout)
	}
	if got.UICursorShape() != "bar" {
		t.Errorf("ui.cursor_shape = %q, want bar", got.UICursorShape())
	}
	if !got.UI.ShowTurnUsage {
		t.Error("ui.show_turn_usage = false, want true")
	}
	if got.Desktop.Language != "en" {
		t.Errorf("desktop.language = %q, want en", got.Desktop.Language)
	}
	if got.Desktop.LayoutStyle != "workbench" {
		t.Errorf("desktop.layout_style = %q, want workbench", got.Desktop.LayoutStyle)
	}
	if got.Desktop.Theme != "dark" {
		t.Errorf("desktop.theme = %q, want dark", got.Desktop.Theme)
	}
	if got.Desktop.ThemeStyle != "graphite" {
		t.Errorf("desktop.theme_style = %q, want graphite", got.Desktop.ThemeStyle)
	}
	if got.Desktop.TerminalTheme != "light" {
		t.Errorf("desktop.terminal_theme = %q, want light", got.Desktop.TerminalTheme)
	}
	if got.Desktop.CloseBehavior != "background" {
		t.Errorf("desktop.close_behavior = %q, want background", got.Desktop.CloseBehavior)
	}
	if got.DesktopDisplayMode() != "compact" {
		t.Errorf("desktop.display_mode = %q, want compact", got.DesktopDisplayMode())
	}
	if got.Desktop.StatusBarStyle != "text" {
		t.Errorf("desktop.status_bar_style = %q, want text", got.Desktop.StatusBarStyle)
	}
	if want := []string{"model", "balance", "cache"}; !reflect.DeepEqual(got.Desktop.StatusBarItems, want) {
		t.Errorf("desktop.status_bar_items = %v, want %v", got.Desktop.StatusBarItems, want)
	}
	if got.DesktopDefaultToolApprovalMode() != "auto" {
		t.Errorf("desktop.default_tool_approval_mode = %q, want auto", got.DesktopDefaultToolApprovalMode())
	}
	if got.Desktop.CheckUpdates == nil || *got.Desktop.CheckUpdates {
		t.Errorf("desktop.check_updates = %+v, want false", got.Desktop.CheckUpdates)
	}
	if got.DesktopUpdateChannel() != "stable" {
		t.Errorf("desktop.update_channel = %q, want stable", got.DesktopUpdateChannel())
	}
	if got.Agent.RecoveryModel != "mimo-pro" || got.Agent.RecoveryTemperature != 0 {
		t.Errorf("agent recovery settings not preserved: %+v", got.Agent)
	}
	if !got.Notifications.Enabled || !got.Notifications.TurnDone || !got.Notifications.ApprovalRequest || !got.Notifications.AskRequest {
		t.Errorf("notifications not preserved: %+v", got.Notifications)
	}
	if got.Agent.MaxSteps != orig.Agent.MaxSteps {
		t.Errorf("max_steps = %d, want %d", got.Agent.MaxSteps, orig.Agent.MaxSteps)
	}
	if got.Agent.PlannerMaxSteps != orig.Agent.PlannerMaxSteps {
		t.Errorf("planner_max_steps = %d, want %d", got.Agent.PlannerMaxSteps, orig.Agent.PlannerMaxSteps)
	}
	if len(got.Bot.Connections) != 1 || got.Bot.Connections[0].Model != "deepseek-pro" || got.Bot.Connections[0].WorkspaceRoot != "/tmp/reasonix-bot" {
		t.Errorf("bot connection not preserved: %+v", got.Bot.Connections)
	}
	if got.Bot.ToolApprovalMode != "auto" || got.Bot.Connections[0].ToolApprovalMode != "yolo" {
		t.Errorf("bot tool approval mode not preserved: bot=%q connection=%q", got.Bot.ToolApprovalMode, got.Bot.Connections[0].ToolApprovalMode)
	}
	if !got.Bot.Control.Enabled || got.Bot.Control.Addr != "127.0.0.1:39001" || got.Bot.Control.TokenEnv != "BOT_CONTROL_TOKEN" {
		t.Errorf("bot control not preserved: %+v", got.Bot.Control)
	}
	if len(got.Bot.Feishu.OutboundMediaRoots) != 2 || got.Bot.Feishu.OutboundMediaRoots[0] != "/tmp/reasonix-media" {
		t.Errorf("feishu outbound_media_roots not preserved: %+v", got.Bot.Feishu.OutboundMediaRoots)
	}
	if len(got.Bot.Routes) != 1 || got.Bot.Routes[0].WorkspaceRoot != "/tmp/reasonix-route" || got.Bot.Routes[0].ChatID != "oc_group" {
		t.Errorf("bot routes not preserved: %+v", got.Bot.Routes)
	}
	if len(got.Bot.DesktopWatchers) != 1 || got.Bot.DesktopWatchers[0].ChatID != "oc_watcher" || got.Bot.DesktopWatchers[0].Platform != "feishu" || got.Bot.DesktopWatchers[0].Domain != "lark" {
		t.Errorf("bot desktop watchers not preserved: %+v", got.Bot.DesktopWatchers)
	}
	if len(got.Bot.Connections[0].SessionMappings) != 1 || got.Bot.Connections[0].SessionMappings[0].Scope != "project" || got.Bot.Connections[0].SessionMappings[0].WorkspaceRoot != "/tmp/reasonix-bot" {
		t.Errorf("bot session mapping scope not preserved: %+v", got.Bot.Connections[0].SessionMappings)
	}
	if got.Agent.Temperature != orig.Agent.Temperature {
		t.Errorf("temperature = %v, want %v", got.Agent.Temperature, orig.Agent.Temperature)
	}
	if got.Agent.ReasoningLanguage != "zh" {
		t.Errorf("reasoning_language = %q, want zh", got.Agent.ReasoningLanguage)
	}
	if got.Agent.CompactRatio != orig.Agent.CompactRatio {
		t.Errorf("compact_ratio = %v, want %v", got.Agent.CompactRatio, orig.Agent.CompactRatio)
	}
	// Deprecated multi-threshold fields must not reappear after render/load.
	if got.Agent.SoftCompactRatio != 0 || got.Agent.ToolResultSnipRatio != 0 || got.Agent.CompactForceRatio != 0 {
		t.Errorf("deprecated compact ratios survived round-trip: soft=%v snip=%v force=%v",
			got.Agent.SoftCompactRatio, got.Agent.ToolResultSnipRatio, got.Agent.CompactForceRatio)
	}
	if strings.Join(got.Agent.Keep, ",") != strings.Join(orig.Agent.Keep, ",") {
		t.Errorf("keep = %v, want %v", got.Agent.Keep, orig.Agent.Keep)
	}
	if got.Agent.RecentKeep != orig.Agent.RecentKeep {
		t.Errorf("recent_keep = %d, want %d", got.Agent.RecentKeep, orig.Agent.RecentKeep)
	}
	if got.Agent.SystemPrompt != orig.Agent.SystemPrompt {
		t.Errorf("system_prompt mismatch:\n got %q\nwant %q", got.Agent.SystemPrompt, orig.Agent.SystemPrompt)
	}
	if !got.LSP.Enabled {
		t.Error("lsp.enabled = false, want true")
	}
	if got.Environment.Enabled == nil || *got.Environment.Enabled {
		t.Errorf("environment.enabled = %+v, want false", got.Environment.Enabled)
	}
	if !reflect.DeepEqual(got.Environment.Tools, orig.Environment.Tools) {
		t.Errorf("environment.tools = %v, want %v", got.Environment.Tools, orig.Environment.Tools)
	}
	lua := got.LSP.Servers["lua"]
	if lua.Command != "lua-language-server" || lua.LanguageID != "lua" || lua.InstallHint != "install lua-language-server" {
		t.Errorf("lsp.servers.lua scalar fields not preserved: %+v", lua)
	}
	if len(lua.Args) != 1 || lua.Args[0] != "--stdio" {
		t.Errorf("lsp.servers.lua.args = %v, want [--stdio]", lua.Args)
	}
	if lua.Env["LUA_PATH"] != "./?.lua" {
		t.Errorf("lsp.servers.lua.env = %v, want LUA_PATH", lua.Env)
	}
	if len(lua.Extensions) != 3 || lua.Extensions[2] != ".gui_script" {
		t.Errorf("lsp.servers.lua.extensions = %v", lua.Extensions)
	}
	if got.Agent.SubagentModel != "mimo-pro" {
		t.Errorf("subagent_model = %q, want mimo-pro", got.Agent.SubagentModel)
	}
	if got.Agent.SubagentModels["review"] != "deepseek-pro" {
		t.Errorf("subagent_models.review = %q, want deepseek-pro", got.Agent.SubagentModels["review"])
	}
	if got.Agent.MaxSubagentDepth != 3 {
		t.Errorf("max_subagent_depth = %d, want 3", got.Agent.MaxSubagentDepth)
	}
	if got.Tools.BashTimeoutSeconds == nil || *got.Tools.BashTimeoutSeconds != 900 {
		t.Errorf("tools.bash_timeout_seconds = %v, want 900", got.Tools.BashTimeoutSeconds)
	}
	if got.Tools.BackgroundJobs.StalledWarningSeconds == nil || *got.Tools.BackgroundJobs.StalledWarningSeconds != 30 {
		t.Errorf("tools.background_jobs.stalled_warning_seconds = %v, want 30", got.Tools.BackgroundJobs.StalledWarningSeconds)
	}
	if got.Tools.Shell.Prefer != "bash" {
		t.Errorf("tools.shell.prefer = %q, want bash", got.Tools.Shell.Prefer)
	}
	if got.Tools.Shell.Path != "/usr/local/bin/bash" {
		t.Errorf("tools.shell.path = %q, want /usr/local/bin/bash", got.Tools.Shell.Path)
	}
	if g, _ := got.Provider("mimo-pro"); g == nil || g.BaseURL != "http://localhost:8000/v1" || g.ChatURL != "http://localhost:8000/v1/chat/completions" || g.RequestURL != "http://localhost:8000/custom/chat/completions/?token=1" || g.ModelsURL != "http://localhost:8000/v1/models" || g.ReasoningProtocol != "openai" {
		t.Errorf("mimo-pro endpoint fields not preserved: %+v", g)
	}
	if g, _ := got.Provider("mimo-pro"); g == nil || g.PresetID != "mimo-api" || g.PresetVersion != ProviderPresetVersion {
		t.Errorf("mimo-pro preset metadata not preserved: %+v", g)
	}
	if g, _ := got.Provider("deepseek-flash"); g == nil || g.Effort != "max" {
		t.Errorf("deepseek-flash effort not preserved: %+v", g)
	}
	if len(got.Providers) != len(orig.Providers) {
		t.Errorf("providers count = %d, want %d", len(got.Providers), len(orig.Providers))
	}
	if got.Permissions.Mode != "deny" {
		t.Errorf("permissions.mode = %q, want deny", got.Permissions.Mode)
	}
	if len(got.Permissions.Deny) != 1 || got.Permissions.Deny[0] != "Bash(rm -rf*)" {
		t.Errorf("permissions.deny = %v, want [Bash(rm -rf*)]", got.Permissions.Deny)
	}
	if len(got.Permissions.Allow) != 2 {
		t.Errorf("permissions.allow = %v, want 2 entries", got.Permissions.Allow)
	}
	if got.Network.ProxyMode != "custom" || got.Network.Proxy.Type != "socks5" || got.Network.Proxy.Port != 7890 {
		t.Errorf("network proxy not preserved: %+v", got.Network)
	}
	if len(got.Skills.Paths) != 2 || got.Skills.Paths[0] != "~/my-skills" {
		t.Errorf("skills.paths = %v", got.Skills.Paths)
	}
	if len(got.Skills.ExcludedPaths) != 1 || got.Skills.ExcludedPaths[0] != "~/.agents/skills" {
		t.Errorf("skills.excluded_paths = %v", got.Skills.ExcludedPaths)
	}
	if len(got.Skills.DisabledSkills) != 2 || got.Skills.DisabledSkills[0] != "review" || got.Skills.DisabledSkills[1] != "explore" {
		t.Errorf("skills.disabled_skills = %v", got.Skills.DisabledSkills)
	}
	if !got.Skills.DisableImplicitInvocation || got.ImplicitSkillInvocationEnabled() {
		t.Error("skills.disable_implicit_invocation was not preserved")
	}
	if got.SkillMaxDepth() != 2 {
		t.Errorf("skills.max_depth = %d, want 2", got.SkillMaxDepth())
	}
	if len(got.Plugins) != 2 {
		t.Fatalf("plugins count = %d, want 2", len(got.Plugins))
	}
	stripe := got.Plugins[1]
	if stripe.Name != "stripe" || stripe.Type != "http" || stripe.URL != "https://mcp.stripe.com" {
		t.Errorf("http plugin not preserved: %+v", stripe)
	}
	if stripe.Headers["Authorization"] != "Bearer x" {
		t.Errorf("plugin headers not preserved: %v", stripe.Headers)
	}
	if strings.Contains(rendered, "trusted_read_only_tools") {
		t.Errorf("removed plugin reader setting survived render: entry=%+v\n%s", stripe, rendered)
	}
	if stripe.AutoStart == nil || *stripe.AutoStart {
		t.Errorf("auto_start should render and parse as false, got %+v", stripe.AutoStart)
	}
	if stripe.Tier != "" {
		t.Errorf("plugin tier should be omitted from new config, got %q", stripe.Tier)
	}
	if strings.Contains(rendered, "\ntier") {
		t.Errorf("rendered config should not contain MCP tier fields:\n%s", rendered)
	}
}

func TestRenderTOMLDocumentsPlanModeReadOnlyCommands(t *testing.T) {
	cfg := Default()
	cfg.Agent.PlanModeReadOnlyCommands = []string{"gh issue view"}

	rendered := RenderTOML(cfg)
	var got Config
	if _, err := toml.Decode(rendered, &got); err != nil {
		t.Fatalf("rendered TOML does not parse: %v\n%s", err, rendered)
	}
	if !strings.Contains(rendered, `plan_mode_read_only_commands = ["gh issue view"]`) {
		t.Fatalf("rendered config should preserve plan_mode_read_only_commands:\n%s", rendered)
	}
	if !strings.Contains(rendered, "legacy compatibility only") || !strings.Contains(rendered, "Plan bash uses Permissions") {
		t.Fatalf("rendered config should document legacy plan_mode_read_only_commands semantics:\n%s", rendered)
	}
	if !reflect.DeepEqual(got.Agent.PlanModeReadOnlyCommands, cfg.Agent.PlanModeReadOnlyCommands) {
		t.Fatalf("PlanModeReadOnlyCommands round trip = %v, want %v", got.Agent.PlanModeReadOnlyCommands, cfg.Agent.PlanModeReadOnlyCommands)
	}
}

func TestRenderTOMLDropsRetiredMCPPolicyFields(t *testing.T) {
	var cfg Config
	if _, err := toml.Decode(`[[plugins]]
name = "github"
command = "github-mcp"
trusted_read_only_tools = ["issue_read", "pull_request_read"]
default_tools_approval_mode = "writes"
approvals_reviewer = "auto_review"

[plugins.tools.wipe]
approval_mode = "prompt"
`, &cfg); err != nil {
		t.Fatalf("legacy config should still decode: %v", err)
	}

	rendered := RenderTOML(&cfg)
	for _, retired := range []string{"trusted_read_only_tools", "default_tools_approval_mode", "approvals_reviewer", "\napproval_mode ="} {
		if strings.Contains(rendered, retired) {
			t.Fatalf("rendered config retained retired MCP field %q:\n%s", retired, rendered)
		}
	}

	var got Config
	if _, err := toml.Decode(rendered, &got); err != nil {
		t.Fatalf("rendered TOML does not parse: %v\n%s", err, rendered)
	}
}

func TestRenderTOMLPreservesMCPTimeouts(t *testing.T) {
	cfg := Default()
	cfg.Tools.MCPCallTimeoutSeconds = intPtr(450)
	cfg.Tools.MCPStartupTimeoutSeconds = intPtr(45)
	cfg.Plugins = []PluginEntry{{
		Name:                  "maker",
		Command:               "maker-mcp",
		StartupTimeoutSeconds: 60,
		CallTimeoutSeconds:    600,
		ToolTimeoutSeconds: map[string]int{
			"generate/video": 1800,
			"search":         120,
		},
	}}

	rendered := RenderTOML(cfg)
	for _, want := range []string{
		"mcp_call_timeout_seconds = 450",
		"mcp_startup_timeout_seconds = 45",
		"startup_timeout_seconds = 60",
		"call_timeout_seconds = 600",
		`tool_timeout_seconds = { "generate/video" = 1800, "search" = 120 }`,
		"Raw MCP tool names",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered config missing %q:\n%s", want, rendered)
		}
	}

	var got Config
	if _, err := toml.Decode(rendered, &got); err != nil {
		t.Fatalf("rendered TOML does not parse: %v\n%s", err, rendered)
	}
	if got.Tools.MCPCallTimeoutSeconds == nil || *got.Tools.MCPCallTimeoutSeconds != 450 {
		t.Fatalf("MCPCallTimeoutSeconds round trip = %v, want 450", got.Tools.MCPCallTimeoutSeconds)
	}
	if got.Tools.MCPStartupTimeoutSeconds == nil || *got.Tools.MCPStartupTimeoutSeconds != 45 {
		t.Fatalf("MCPStartupTimeoutSeconds round trip = %v, want 45", got.Tools.MCPStartupTimeoutSeconds)
	}
	if got.Plugins[0].StartupTimeoutSeconds != 60 {
		t.Fatalf("StartupTimeoutSeconds round trip = %d, want 60", got.Plugins[0].StartupTimeoutSeconds)
	}
	if got.Plugins[0].CallTimeoutSeconds != 600 {
		t.Fatalf("CallTimeoutSeconds round trip = %d, want 600", got.Plugins[0].CallTimeoutSeconds)
	}
	if !reflect.DeepEqual(got.Plugins[0].ToolTimeoutSeconds, cfg.Plugins[0].ToolTimeoutSeconds) {
		t.Fatalf("ToolTimeoutSeconds round trip = %v, want %v", got.Plugins[0].ToolTimeoutSeconds, cfg.Plugins[0].ToolTimeoutSeconds)
	}
}

func TestRenderTOMLCreationLayoutStyle(t *testing.T) {
	c := Default()
	if err := c.SetDesktopLayoutStyle("creation"); err != nil {
		t.Fatalf("SetDesktopLayoutStyle: %v", err)
	}
	rendered := RenderTOML(c)
	var got Config
	if _, err := toml.Decode(rendered, &got); err != nil {
		t.Fatalf("rendered TOML does not parse: %v\n---\n%s", err, rendered)
	}
	if got.Desktop.LayoutStyle != "creation" {
		t.Errorf("desktop.layout_style = %q, want creation", got.Desktop.LayoutStyle)
	}
	if got.DesktopLayoutStyle() != "creation" {
		t.Errorf("DesktopLayoutStyle() = %q, want creation", got.DesktopLayoutStyle())
	}
}

func TestScopedRenderPreservesLSPConfig(t *testing.T) {
	const src = `
config_version = 4
default_model = "mimo"

[lsp]
enabled = true

[lsp.servers.lua]
command = "lua-language-server"
args = ["--stdio"]
env = { LUA_PATH = "./?.lua" }
language_id = "lua"
extensions = [".lua", ".script", ".gui_script"]
install_hint = "install lua-language-server"

[lsp.servers."c++"]
command = "clangd"
extensions = [".cc", ".cpp", ".hpp"]
`

	var cfg Config
	if _, err := toml.Decode(src, &cfg); err != nil {
		t.Fatalf("decode source TOML: %v", err)
	}

	for _, scope := range []RenderScope{RenderScopeFull, RenderScopeUser, RenderScopeProject} {
		t.Run(string(scope), func(t *testing.T) {
			rendered := RenderTOMLForScope(&cfg, scope)
			if !strings.Contains(rendered, "[lsp]") {
				t.Fatalf("render missing [lsp]:\n%s", rendered)
			}
			if !strings.Contains(rendered, "[lsp.servers.lua]") {
				t.Fatalf("render missing [lsp.servers.lua]:\n%s", rendered)
			}
			if !strings.Contains(rendered, `[lsp.servers."c++"]`) {
				t.Fatalf("render missing quoted c++ server key:\n%s", rendered)
			}

			var got Config
			if _, err := toml.Decode(rendered, &got); err != nil {
				t.Fatalf("decode rendered TOML: %v\n---\n%s", err, rendered)
			}
			if !got.LSP.Enabled {
				t.Fatalf("lsp.enabled = false, want true")
			}
			lua, ok := got.LSP.Servers["lua"]
			if !ok {
				t.Fatalf("lsp.servers.lua missing after round-trip: %+v", got.LSP.Servers)
			}
			if lua.Command != "lua-language-server" || lua.LanguageID != "lua" || lua.InstallHint != "install lua-language-server" {
				t.Fatalf("lsp.servers.lua scalar fields not preserved: %+v", lua)
			}
			if len(lua.Args) != 1 || lua.Args[0] != "--stdio" {
				t.Fatalf("lsp.servers.lua.args = %v, want [--stdio]", lua.Args)
			}
			if lua.Env["LUA_PATH"] != "./?.lua" {
				t.Fatalf("lsp.servers.lua.env = %v, want LUA_PATH", lua.Env)
			}
			if len(lua.Extensions) != 3 || lua.Extensions[0] != ".lua" || lua.Extensions[2] != ".gui_script" {
				t.Fatalf("lsp.servers.lua.extensions = %v", lua.Extensions)
			}
			cpp, ok := got.LSP.Servers["c++"]
			if !ok {
				t.Fatalf("lsp.servers.c++ missing after round-trip: %+v", got.LSP.Servers)
			}
			if cpp.Command != "clangd" || len(cpp.Extensions) != 3 || cpp.Extensions[1] != ".cpp" {
				t.Fatalf("lsp.servers.c++ not preserved: %+v", cpp)
			}
		})
	}
}

func BenchmarkRenderTOMLWithLSPServers(b *testing.B) {
	cfg := Default()
	cfg.LSP.Servers = make(map[string]LSPServer, 64)
	for i := range 64 {
		lang := "lang" + strconv.Itoa(i)
		cfg.LSP.Servers[lang] = LSPServer{
			Command:     "server-" + strconv.Itoa(i),
			Args:        []string{"--stdio", "--flag"},
			Env:         map[string]string{"SERVER_MODE": "stdio", "SERVER_ROOT": "."},
			LanguageID:  lang,
			Extensions:  []string{"." + lang, "." + lang + "x"},
			InstallHint: "install server-" + strconv.Itoa(i),
		}
	}

	b.ReportAllocs()
	for range b.N {
		rendered := RenderTOML(cfg)
		if len(rendered) == 0 {
			b.Fatal("empty render")
		}
	}
}

func TestNotificationsDefaultsKeepEventSwitchesEnabled(t *testing.T) {
	cfg := Default()
	if cfg.Notifications.Enabled {
		t.Fatal("notifications.enabled default = true, want false")
	}
	if !cfg.Notifications.TurnDone || !cfg.Notifications.ApprovalRequest || !cfg.Notifications.AskRequest {
		t.Fatalf("notification event switches default off: %+v", cfg.Notifications)
	}

	if _, err := toml.Decode("[notifications]\nenabled = true\n", cfg); err != nil {
		t.Fatalf("decode notifications: %v", err)
	}
	if !cfg.Notifications.Enabled || !cfg.Notifications.TurnDone || !cfg.Notifications.ApprovalRequest || !cfg.Notifications.AskRequest {
		t.Fatalf("enabled-only config should keep event switches on: %+v", cfg.Notifications)
	}
}

func TestScopedRenderSeparatesUserAndProjectConfig(t *testing.T) {
	c := Default()
	c.Language = "zh"
	c.Desktop.Language = "zh"
	c.Desktop.Currency = "CNY"
	c.Desktop.Theme = "dark"
	c.Desktop.ThemeStyle = "graphite"
	c.Desktop.CloseBehavior = "background"
	c.Desktop.StatusBarStyle = "text"
	c.Desktop.StatusBarStyleInitialized = true
	c.Desktop.DefaultToolApprovalMode = "auto"
	c.Desktop.CheckUpdates = boolPtr(false)
	c.Desktop.UpdateChannel = "preview"
	c.Agent.RecoveryModel = "deepseek-pro"
	c.Agent.RecoveryTemperature = 0.2

	user := RenderTOMLForScope(c, RenderScopeUser)
	for _, want := range []string{"config_version = 10", "[desktop]", `currency = "CNY"`, "[billing]", `display_currency = "CNY"`, `theme = "dark"`, `terminal_theme = "auto"`, `close_behavior = "background"`, `status_bar_style = "text"`, `default_tool_approval_mode = "auto"`, `check_updates = false`, `recovery_model = "deepseek-pro"`, "[notifications]", "[tools.shell]"} {
		if !strings.Contains(user, want) {
			t.Fatalf("user render missing %q:\n%s", want, user)
		}
	}
	if strings.Contains(user, "update_channel") || strings.Contains(user, "[cli]") {
		t.Fatalf("user render retained retired update channel:\n%s", user)
	}

	project := RenderTOMLForScope(c, RenderScopeProject)
	for _, forbidden := range []string{"[desktop]", "[notifications]", "close_behavior =", "default_tool_approval_mode =", "default_auto_recovery_checkpoint =", "check_updates =", "update_channel =", "max_steps", "planner_max_steps"} {
		if strings.Contains(project, forbidden) {
			t.Fatalf("project render should not contain %q:\n%s", forbidden, project)
		}
	}
	for _, retired := range []string{"default_auto_recovery_checkpoint", "auto_recovery_checkpoint"} {
		if strings.Contains(user, retired) || strings.Contains(project, retired) {
			t.Fatalf("retired Auto Guard key %q must not be rendered:\nuser:\n%s\nproject:\n%s", retired, user, project)
		}
	}
	if strings.Contains(project, "\nsystem_prompt = \"\"\"") {
		t.Fatalf("project render should not pin the built-in system prompt:\n%s", project)
	}
	if !strings.Contains(project, "# system_prompt =") {
		t.Fatalf("project render should leave a system prompt hint:\n%s", project)
	}
	for _, want := range []string{`recovery_model = "deepseek-pro"`} {
		if !strings.Contains(project, want) {
			t.Fatalf("project render missing %q:\n%s", want, project)
		}
	}
	if strings.Contains(user, "auto_plan") || strings.Contains(project, "auto_plan") {
		t.Fatalf("retired auto-plan keys must not be rendered:\nuser:\n%s\nproject:\n%s", user, project)
	}
	if strings.Contains(user, "recovery_temperature") || strings.Contains(project, "recovery_temperature") {
		t.Fatalf("deprecated recovery_temperature must not be rendered:\nuser:\n%s\nproject:\n%s", user, project)
	}
}

func TestScopedRenderKeepsPluginsInTheirOwningConfig(t *testing.T) {
	cfg := Default()
	cfg.Plugins = []PluginEntry{
		{Name: "unknown", Command: "unknown-mcp"},
		{Name: "user", Command: "user-mcp", Source: MCPSourceUserConfig},
		{Name: "project", Command: "project-mcp", Source: MCPSourceProjectConfig},
		{Name: "mcp-json", Command: "json-mcp", Source: MCPSourceProjectMCPJSON},
		{Name: "legacy", Command: "legacy-mcp", Source: MCPSourceLegacyUser},
		{Name: "package", Command: "package-mcp", Source: MCPSourcePluginPackage},
	}

	tests := []struct {
		name  string
		body  string
		want  []string
		avoid []string
	}{
		{name: "full", body: RenderTOMLForScope(cfg, RenderScopeFull), want: []string{"unknown", "user", "project", "mcp-json", "legacy", "package"}},
		{name: "user", body: RenderTOMLForScope(cfg, RenderScopeUser), want: []string{"unknown", "user"}, avoid: []string{"project", "mcp-json", "legacy", "package"}},
		{name: "project", body: RenderTOMLForScope(cfg, RenderScopeProject), want: []string{"unknown", "project"}, avoid: []string{"user", "mcp-json", "legacy", "package"}},
		{name: "project delta", body: RenderTOMLProjectDelta(cfg), want: []string{"unknown", "project"}, avoid: []string{"user", "mcp-json", "legacy", "package"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, name := range tt.want {
				if !strings.Contains(tt.body, `name    = "`+name+`"`) {
					t.Fatalf("render missing plugin %q:\n%s", name, tt.body)
				}
			}
			for _, name := range tt.avoid {
				if strings.Contains(tt.body, `name    = "`+name+`"`) {
					t.Fatalf("render leaked plugin %q:\n%s", name, tt.body)
				}
			}
		})
	}
}

func TestProjectDeltaRendersRecoveryReviewerOverride(t *testing.T) {
	c := Default()
	c.Agent.RecoveryModel = "deepseek-pro"
	c.Agent.RecoveryTemperature = 0.2

	delta := RenderTOMLProjectDelta(c)
	for _, want := range []string{"[agent]", `recovery_model = "deepseek-pro"`} {
		if !strings.Contains(delta, want) {
			t.Fatalf("project delta missing %q:\n%s", want, delta)
		}
	}
	if strings.Contains(delta, "recovery_temperature") {
		t.Fatalf("deprecated recovery_temperature rendered:\n%s", delta)
	}
}

func TestProjectDeltaRendersToolsShellOverrides(t *testing.T) {
	c := Default()
	c.Tools.Shell.Prefer = "bash"
	c.Tools.Shell.Path = "/usr/local/bin/bash"

	delta := RenderTOMLProjectDelta(c)
	for _, want := range []string{"[tools.shell]", `prefer = "bash"`, `path = "/usr/local/bin/bash"`} {
		if !strings.Contains(delta, want) {
			t.Fatalf("project delta missing %q:\n%s", want, delta)
		}
	}
	if strings.Contains(delta, "[tools]\n\n") {
		t.Fatalf("project delta should not emit an empty [tools] block:\n%s", delta)
	}

	got := Default()
	if _, err := toml.Decode(delta, got); err != nil {
		t.Fatalf("decode project delta: %v\n%s", err, delta)
	}
	if got.Tools.Shell.Prefer != "bash" || got.Tools.Shell.Path != "/usr/local/bin/bash" {
		t.Fatalf("tools.shell = %+v, want bash with path", got.Tools.Shell)
	}
}

func TestResponsesProviderModeRoundTripsInUserAndProjectRender(t *testing.T) {
	legacyFalse := false
	cfg := Default()
	cfg.Providers = append(cfg.Providers, ProviderEntry{
		Name: "responses-test", Kind: "responses", BaseURL: "https://example.com/v1",
		ChatURL: "https://legacy.example.com/chat/completions", RequestURL: "https://example.com/v1/custom/responses",
		Model: "model", APIKeyEnv: "RESPONSES_API_KEY",
		ResponsesMode: "stateful", ResponsesStateful: &legacyFalse,
	})

	for _, rendered := range []string{RenderTOMLForScope(cfg, RenderScopeUser), RenderTOMLProjectDelta(cfg)} {
		if !strings.Contains(rendered, `responses_mode = "stateful"`) || !strings.Contains(rendered, "responses_stateful = false") {
			t.Fatalf("responses settings missing from render:\n%s", rendered)
		}
		var decoded Config
		if _, err := toml.Decode(rendered, &decoded); err != nil {
			t.Fatalf("decode responses config: %v\n%s", err, rendered)
		}
		entry, ok := decoded.Provider("responses-test")
		if !ok || entry.ChatURL != "https://legacy.example.com/chat/completions" || entry.RequestURL != "https://example.com/v1/custom/responses" || entry.ResponsesMode != "stateful" || entry.ResponsesStateful == nil || *entry.ResponsesStateful {
			t.Fatalf("responses settings did not round-trip: %+v, found=%v", entry, ok)
		}
	}
}

func TestProjectDeltaRendersUICursorShape(t *testing.T) {
	c := Default()
	c.UI.CursorShape = "block"

	delta := RenderTOMLProjectDelta(c)
	for _, want := range []string{"[ui]", `cursor_shape = "block"`} {
		if !strings.Contains(delta, want) {
			t.Fatalf("project delta missing %q:\n%s", want, delta)
		}
	}

	got := Default()
	if _, err := toml.Decode(delta, got); err != nil {
		t.Fatalf("decode project delta: %v\n%s", err, delta)
	}
	if got.UICursorShape() != "block" {
		t.Fatalf("ui.cursor_shape = %q, want block", got.UICursorShape())
	}
}

func TestShowTurnUsageDefaultsOnAndRendersFalseOverride(t *testing.T) {
	c := Default()
	if !c.UI.ShowTurnUsage {
		t.Fatal("ui.show_turn_usage should default to true")
	}

	c.UI.ShowTurnUsage = false
	delta := RenderTOMLProjectDelta(c)
	for _, want := range []string{"[ui]", "show_turn_usage = false"} {
		if !strings.Contains(delta, want) {
			t.Fatalf("project delta missing %q:\n%s", want, delta)
		}
	}

	got := Default()
	if _, err := toml.Decode(delta, got); err != nil {
		t.Fatalf("decode project delta: %v\n%s", err, delta)
	}
	if got.UI.ShowTurnUsage {
		t.Fatal("ui.show_turn_usage false override did not round-trip")
	}
}

func TestProjectRenderPreservesNonDefaultLegacySections(t *testing.T) {
	c := Default()
	c.UI.Theme = "light"
	c.UI.CloseBehavior = "quit"
	c.Network.ProxyMode = "custom"
	c.Network.Proxy.Server = "127.0.0.1"
	c.Network.Proxy.Port = 7890

	project := RenderTOMLForScope(c, RenderScopeProject)
	for _, want := range []string{"[ui]", `theme = "light"`, `close_behavior = "quit"`, "[network]", `proxy_mode = "custom"`, `server = "127.0.0.1"`} {
		if !strings.Contains(project, want) {
			t.Fatalf("project render missing legacy/non-default %q:\n%s", want, project)
		}
	}
}

func TestRenderTOMLRoundTripsPerModelPrices(t *testing.T) {
	orig := Default()
	orig.Providers = []ProviderEntry{{
		Name:      "deepseek",
		Kind:      "openai",
		BaseURL:   "https://api.deepseek.com",
		Models:    []string{"deepseek-v4-flash", "deepseek-v4-pro"},
		Default:   "deepseek-v4-flash",
		APIKeyEnv: "DEEPSEEK_API_KEY",
		Prices:    DeepSeekV4PricesForCurrency("CNY"),
	}}

	var got Config
	if _, err := toml.Decode(RenderTOML(orig), &got); err != nil {
		t.Fatalf("rendered TOML does not parse: %v", err)
	}
	p, ok := got.Provider("deepseek")
	if !ok {
		t.Fatal("deepseek provider missing after round trip")
	}
	if p.Prices["deepseek-v4-flash"].Input != 3 || p.Prices["deepseek-v4-pro"].Output != 27 {
		t.Fatalf("prices after round trip = %+v", p.Prices)
	}
}

func TestRenderTOMLRoundTripsVisionModels(t *testing.T) {
	orig := Default()
	orig.Providers = []ProviderEntry{
		{
			Name:         "custom",
			Kind:         "openai",
			BaseURL:      "https://proxy.example.com/v1",
			Models:       []string{"text-only", "qwen-vl-plus"},
			Default:      "text-only",
			APIKeyEnv:    "CUSTOM_API_KEY",
			VisionModels: []string{"qwen-vl-plus"},
			VisionDetail: "low",
		},
		{
			Name:         "disabled-vision",
			Kind:         "openai",
			BaseURL:      "https://proxy.example.com/v1",
			Models:       []string{"qwen-vl-plus"},
			Default:      "qwen-vl-plus",
			APIKeyEnv:    "CUSTOM_API_KEY",
			VisionModels: []string{},
		},
	}

	rendered := RenderTOML(orig)
	if !strings.Contains(rendered, `vision_models = ["qwen-vl-plus"]`) {
		t.Fatalf("rendered TOML missing vision_models:\n%s", rendered)
	}
	if !strings.Contains(rendered, `vision_models = []`) {
		t.Fatalf("rendered TOML missing explicit empty vision_models:\n%s", rendered)
	}
	if !strings.Contains(rendered, `vision_detail = "low"`) {
		t.Fatalf("rendered TOML missing vision_detail:\n%s", rendered)
	}

	var got Config
	if _, err := toml.Decode(rendered, &got); err != nil {
		t.Fatalf("rendered TOML does not parse: %v", err)
	}
	p, ok := got.Provider("custom")
	if !ok {
		t.Fatal("custom provider missing after round trip")
	}
	if !reflect.DeepEqual(p.VisionModels, []string{"qwen-vl-plus"}) {
		t.Fatalf("vision_models after round trip = %v, want [qwen-vl-plus]", p.VisionModels)
	}
	if p.VisionDetail != "low" {
		t.Fatalf("vision_detail after round trip = %q, want low", p.VisionDetail)
	}
	disabled, ok := got.Provider("disabled-vision")
	if !ok {
		t.Fatal("disabled-vision provider missing after round trip")
	}
	if disabled.VisionModels == nil || len(disabled.VisionModels) != 0 {
		t.Fatalf("disabled-vision vision_models after round trip = %#v, want explicit empty list", disabled.VisionModels)
	}
}

func TestRenderTOMLRoundTripsProviderHeadersAndModelOverrides(t *testing.T) {
	orig := Default()
	orig.Providers = []ProviderEntry{{
		Name:      "gateway",
		Kind:      "openai",
		BaseURL:   "https://gateway.example/v1",
		Models:    []string{"deepseek-v4-flash", "plain-chat"},
		Default:   "plain-chat",
		APIKeyEnv: "GATEWAY_API_KEY",
		Headers: map[string]string{
			"HTTP-Referer": "https://app.example",
			"X-Title":      "Reasonix",
		},
		ExtraBody: map[string]any{
			"enable_thinking": true,
			"top_p":           0.8,
			"metadata": map[string]any{
				"mode": "fast",
			},
		},
		AuthHeader:      true,
		MaxOutputTokens: 16_384,
		ModelOverrides: map[string]ProviderModelOverride{
			"deepseek-v4-flash": {
				ReasoningProtocol: ReasoningProtocolDeepSeek,
				SupportedEfforts:  []string{"high", "max"},
				DefaultEffort:     "high",
				Vision:            boolPtr(false),
				ContextWindow:     262_144,
				MaxOutputTokens:   32_768,
			},
		},
	}}

	rendered := RenderTOML(orig)
	if !strings.Contains(rendered, `headers     = { HTTP-Referer = "https://app.example", X-Title = "Reasonix" }`) {
		t.Fatalf("rendered TOML missing headers:\n%s", rendered)
	}
	if !strings.Contains(rendered, `extra_body`) || !strings.Contains(rendered, `"enable_thinking" = true`) {
		t.Fatalf("rendered TOML missing extra_body:\n%s", rendered)
	}
	if !strings.Contains(rendered, `auth_header = true`) {
		t.Fatalf("rendered TOML missing auth_header:\n%s", rendered)
	}
	if !strings.Contains(rendered, `max_output_tokens = 16384`) || !strings.Contains(rendered, `model_overrides`) || !strings.Contains(rendered, `reasoning_protocol = "deepseek"`) || !strings.Contains(rendered, `context_window = 262144`) || !strings.Contains(rendered, `max_output_tokens = 32768`) {
		t.Fatalf("rendered TOML missing model overrides:\n%s", rendered)
	}

	var got Config
	if _, err := toml.Decode(rendered, &got); err != nil {
		t.Fatalf("rendered TOML does not parse: %v\n%s", err, rendered)
	}
	p, ok := got.Provider("gateway")
	if !ok {
		t.Fatal("gateway provider missing after round trip")
	}
	if p.Headers["HTTP-Referer"] != "https://app.example" || p.Headers["X-Title"] != "Reasonix" {
		t.Fatalf("headers after round trip = %+v", p.Headers)
	}
	if p.ExtraBody["enable_thinking"] != true || p.ExtraBody["top_p"] != 0.8 {
		t.Fatalf("extra_body after round trip = %+v", p.ExtraBody)
	}
	if !p.AuthHeader {
		t.Fatal("auth_header after round trip = false, want true")
	}
	if p.MaxOutputTokens != 16_384 {
		t.Fatalf("provider max_output_tokens after round trip = %d, want 16384", p.MaxOutputTokens)
	}
	metadata, ok := p.ExtraBody["metadata"].(map[string]any)
	if !ok || metadata["mode"] != "fast" {
		t.Fatalf("extra_body metadata after round trip = %+v", p.ExtraBody["metadata"])
	}
	ov := p.ModelOverrides["deepseek-v4-flash"]
	if ov.ReasoningProtocol != ReasoningProtocolDeepSeek || !reflect.DeepEqual(ov.SupportedEfforts, []string{"high", "max"}) || ov.DefaultEffort != "high" || ov.Vision == nil || *ov.Vision || ov.ContextWindow != 262_144 || ov.MaxOutputTokens != 32_768 {
		t.Fatalf("model override after round trip = %+v", ov)
	}

	// Older releases do not know context_window/max_output_tokens inside model
	// overrides, but their TOML decoder must still accept this release's config.
	type legacyModelOverride struct {
		ReasoningProtocol string   `toml:"reasoning_protocol"`
		SupportedEfforts  []string `toml:"supported_efforts"`
		DefaultEffort     string   `toml:"default_effort"`
		Vision            *bool    `toml:"vision"`
	}
	type legacyProvider struct {
		ModelOverrides map[string]legacyModelOverride `toml:"model_overrides"`
	}
	var legacy struct {
		Providers []legacyProvider `toml:"providers"`
	}
	if _, err := toml.Decode(rendered, &legacy); err != nil {
		t.Fatalf("legacy config shape cannot read per-model context window: %v", err)
	}
}

func TestRenderModelOverrideExtraBodyAndThinking(t *testing.T) {
	cfg := &Config{
		Providers: []ProviderEntry{{
			Name:      "nvidia",
			Kind:      "openai",
			BaseURL:   "https://integrate.api.nvidia.com/v1",
			Models:    []string{"minimax-m3", "nemotron-super", "gemma4"},
			APIKeyEnv: "NVIDIA_API_KEY",
			Effort:    "high",
			Vision:    true,
			ModelOverrides: map[string]ProviderModelOverride{
				"minimax-m3": {
					ContextWindow:   262_000,
					MaxOutputTokens: 16_384,
					ExtraBody: map[string]any{
						"chat_template_kwargs": map[string]any{"thinking_mode": "enabled"},
					},
				},
				"nemotron-super": {
					ContextWindow:   1_000_000,
					MaxOutputTokens: 32_768,
					ExtraBody: map[string]any{
						"chat_template_kwargs": map[string]any{"enable_thinking": true},
					},
				},
				"gemma4": {
					ContextWindow:   256_000,
					MaxOutputTokens: 32_768,
					Thinking:        "enabled",
					ExtraBody: map[string]any{
						"chat_template_kwargs": map[string]any{"thinking": true},
					},
				},
			},
		}},
	}

	rendered := RenderTOML(cfg)
	if !strings.Contains(rendered, `thinking = "enabled"`) {
		t.Fatalf("rendered TOML missing thinking override:\n%s", rendered)
	}
	if !strings.Contains(rendered, `extra_body`) || !strings.Contains(rendered, `thinking_mode`) {
		t.Fatalf("rendered TOML missing per-model extra_body:\n%s", rendered)
	}

	var got Config
	if _, err := toml.Decode(rendered, &got); err != nil {
		t.Fatalf("rendered TOML does not parse: %v\n%s", err, rendered)
	}
	p, ok := got.Provider("nvidia")
	if !ok {
		t.Fatal("nvidia provider missing after round trip")
	}
	gemma4 := p.ModelOverrides["gemma4"]
	if gemma4.Thinking != "enabled" {
		t.Fatalf("gemma4 thinking = %q, want %q", gemma4.Thinking, "enabled")
	}
	minimax := p.ModelOverrides["minimax-m3"]
	kwargs, ok := minimax.ExtraBody["chat_template_kwargs"].(map[string]any)
	if !ok || kwargs["thinking_mode"] != "enabled" {
		t.Fatalf("minimax-m3 extra_body chat_template_kwargs = %v", minimax.ExtraBody)
	}
	nemotron := p.ModelOverrides["nemotron-super"]
	nkwargs, ok := nemotron.ExtraBody["chat_template_kwargs"].(map[string]any)
	if !ok || nkwargs["enable_thinking"] != true {
		t.Fatalf("nemotron-super extra_body chat_template_kwargs = %v", nemotron.ExtraBody)
	}

	// Verify legacy struct (no extra_body/thinking) still reads without error.
	type legacyModelOverride struct {
		ReasoningProtocol string   `toml:"reasoning_protocol"`
		SupportedEfforts  []string `toml:"supported_efforts"`
		DefaultEffort     string   `toml:"default_effort"`
		Vision            *bool    `toml:"vision"`
		ContextWindow     int      `toml:"context_window"`
		MaxOutputTokens   int      `toml:"max_output_tokens"`
	}
	type legacyProvider struct {
		ModelOverrides map[string]legacyModelOverride `toml:"model_overrides"`
	}
	var legacy struct {
		Providers []legacyProvider `toml:"providers"`
	}
	if _, err := toml.Decode(rendered, &legacy); err != nil {
		t.Fatalf("legacy config shape cannot read per-model extra_body/thinking: %v", err)
	}
}

func TestApplyModelOverrideMergesExtraBody(t *testing.T) {
	e := &ProviderEntry{
		Thinking: "enabled",
		ExtraBody: map[string]any{
			"min_p":          0.01,
			"parallel_calls": true,
		},
		ModelOverrides: map[string]ProviderModelOverride{
			"model-a": {
				Thinking: "disabled",
				ExtraBody: map[string]any{
					"min_p":     0.05,
					"custom_fn": "hello",
				},
			},
			"model-b": {}, // no override — inherits provider
		},
	}

	// model-a: thinking overridden, extra_body merged (model keys win)
	e.Model = "model-a"
	e.applyModelOverride()
	if e.Thinking != "disabled" {
		t.Fatalf("model-a thinking = %q, want %q", e.Thinking, "disabled")
	}
	if mp, ok := e.ExtraBody["min_p"]; !ok || mp != 0.05 {
		t.Fatalf("model-a min_p = %v, want 0.05 (model wins)", mp)
	}
	if v, ok := e.ExtraBody["parallel_calls"]; !ok || v != true {
		t.Fatalf("model-a parallel_calls = %v, want true (inherited from provider)", v)
	}
	if v, ok := e.ExtraBody["custom_fn"]; !ok || v != "hello" {
		t.Fatalf("model-a custom_fn = %v, want hello (added by override)", v)
	}

	// model-b: no override — provider values intact
	e.Model = "model-b"
	e.Thinking = "enabled"
	e.ExtraBody = map[string]any{
		"min_p":          0.01,
		"parallel_calls": true,
	}
	e.applyModelOverride()
	if e.Thinking != "enabled" {
		t.Fatalf("model-b thinking = %q, want %q (unchanged)", e.Thinking, "enabled")
	}
	if mp, ok := e.ExtraBody["min_p"]; !ok || mp != 0.01 {
		t.Fatalf("model-b min_p = %v, want 0.01 (unchanged)", mp)
	}
}

func TestRenderStringMapQuotesNonBareTOMLKeys(t *testing.T) {
	rendered := renderStringMap(map[string]string{
		"github:gh-fix-ci": "deepseek-pro",
		"review":           "deepseek-flash",
	})
	if !strings.Contains(rendered, `"github:gh-fix-ci" = "deepseek-pro"`) {
		t.Fatalf("non-bare key was not quoted: %s", rendered)
	}
	var got struct {
		M map[string]string `toml:"m"`
	}
	if _, err := toml.Decode("m = "+rendered, &got); err != nil {
		t.Fatalf("rendered inline map does not parse: %v (%s)", err, rendered)
	}
	if got.M["github:gh-fix-ci"] != "deepseek-pro" || got.M["review"] != "deepseek-flash" {
		t.Fatalf("decoded map = %+v", got.M)
	}
}

func TestDesktopExternalOpenerUserScopeRoundTrip(t *testing.T) {
	cfg := Default()
	if err := cfg.SetDesktopExternalOpener("ghostty"); err != nil {
		t.Fatal(err)
	}
	rendered := RenderTOMLForScope(cfg, RenderScopeUser)
	if !strings.Contains(rendered, `external_opener = "ghostty"`) {
		t.Fatalf("user config omitted desktop external opener:\n%s", rendered)
	}
	if project := RenderTOMLForScope(cfg, RenderScopeProject); strings.Contains(project, "external_opener") {
		t.Fatalf("project config leaked user-only external opener:\n%s", project)
	}
	var decoded Config
	if _, err := toml.Decode(rendered, &decoded); err != nil {
		t.Fatalf("decode rendered user config: %v", err)
	}
	if got := decoded.DesktopExternalOpener(); got != "ghostty" {
		t.Fatalf("round-trip external opener = %q, want ghostty", got)
	}
}

func TestRenderTOMLTablePathQuotesEachSegment(t *testing.T) {
	got := renderTOMLTablePath("lsp", "servers", "c++", "github:gh-fix-ci")
	want := `lsp.servers."c++"."github:gh-fix-ci"`
	if got != want {
		t.Fatalf("renderTOMLTablePath = %q, want %q", got, want)
	}
}

func boolPtr(v bool) *bool { return &v }

func intPtr(v int) *int { return &v }

func TestRenderTOMLPreservesDesktopDisplayMode(t *testing.T) {
	c := Default()
	if err := c.SetDesktopDisplayMode("compact"); err != nil {
		t.Fatalf("SetDesktopDisplayMode: %v", err)
	}
	rendered := RenderTOMLForScope(c, RenderScopeUser)
	if !strings.Contains(rendered, `display_mode = "compact"`) {
		t.Fatalf("rendered user config missing display_mode:\n%s", rendered)
	}
	var got Config
	if _, err := toml.Decode(rendered, &got); err != nil {
		t.Fatalf("rendered TOML does not parse: %v\n---\n%s", err, rendered)
	}
	if got.DesktopDisplayMode() != "compact" {
		t.Fatalf("display_mode after round trip = %q, want compact", got.DesktopDisplayMode())
	}
}

func TestRenderTOMLConversationWidthRoundTrip(t *testing.T) {
	c := Default()
	if err := c.SetDesktopConversationWidth("full"); err != nil {
		t.Fatalf("SetDesktopConversationWidth: %v", err)
	}
	rendered := RenderTOMLForScope(c, RenderScopeUser)
	if !strings.Contains(rendered, `conversation_width = "full"`) {
		t.Fatalf("rendered user config missing conversation_width:\n%s", rendered)
	}
	if project := RenderTOMLForScope(c, RenderScopeProject); strings.Contains(project, "conversation_width") {
		t.Fatalf("project config leaked user-only conversation_width:\n%s", project)
	}

	var got Config
	if _, err := toml.Decode(rendered, &got); err != nil {
		t.Fatalf("rendered TOML does not parse: %v\n---\n%s", err, rendered)
	}
	if got.DesktopConversationWidth() != "full" {
		t.Fatalf("conversation_width after round trip = %q, want full", got.DesktopConversationWidth())
	}

	if err := c.SetDesktopConversationWidth("standard"); err != nil {
		t.Fatalf("reset conversation width: %v", err)
	}
	if rendered := RenderTOMLForScope(c, RenderScopeUser); strings.Contains(rendered, "conversation_width") {
		t.Fatalf("default conversation_width should be omitted:\n%s", rendered)
	}

	c.Desktop.ConversationWidth = " FULL "
	if rendered := RenderTOMLForScope(c, RenderScopeUser); !strings.Contains(rendered, `conversation_width = "full"`) {
		t.Fatalf("manually edited full width was not normalized:\n%s", rendered)
	}
}

func TestRenderTOMLDefaultStepsOmitted(t *testing.T) {
	isolateUserConfigHome(t)
	out := RenderTOML(Default())
	agentLines := extractSectionLines(out, "[agent]")
	for _, line := range agentLines {
		if strings.Contains(line, "max_steps") || strings.Contains(line, "planner_max_steps") {
			t.Errorf("default step limits should be hidden from generated config, got: %s", line)
		}
	}
}

func TestRenderTOMLWindowsSandboxDefaultAndExplicitEnforceDisabled(t *testing.T) {
	isolateUserConfigHome(t)
	setRuntimeGOOS(t, "windows")

	defaultRendered := RenderTOMLForScope(Default(), RenderScopeUser)
	if !strings.Contains(defaultRendered, `bash    = "off"`) {
		t.Fatalf("Windows default user config should render bash off:\n%s", defaultRendered)
	}

	cfg := Default()
	cfg.Sandbox.Bash = "enforce"
	delta := RenderTOMLProjectDelta(cfg)
	if strings.Contains(delta, `[sandbox]`) || strings.Contains(delta, `bash = `) {
		t.Fatalf("Windows explicit enforce should not render as an effective project delta:\n%s", delta)
	}
}

func extractSectionLines(toml, section string) []string {
	var lines []string
	inSection := false
	for line := range strings.SplitSeq(toml, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, section) {
			inSection = true
			continue
		}
		if inSection && strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "[[") {
			break
		}
		if inSection {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

func TestRenderTOMLOmitsDeprecatedAgentStepLimits(t *testing.T) {
	isolateUserConfigHome(t)
	c := Default()
	c.Agent.MaxSteps = 5
	c.Agent.PlannerMaxSteps = 7
	out := RenderTOML(c)
	for _, line := range extractSectionLines(out, "[agent]") {
		if strings.Contains(line, "max_steps") || strings.Contains(line, "planner_max_steps") {
			t.Fatalf("deprecated step limit should never be rendered, got: %s", line)
		}
	}
}

func TestLoadForEditIgnoresAndDropsDeprecatedAgentStepLimitsOnSave(t *testing.T) {
	isolateUserConfigHome(t)
	path := UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[agent]\nplanner_max_steps = 9\nmax_steps = 100\ntemperature = 0.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadForEdit(path)
	if cfg.Agent.MaxSteps != 0 || cfg.Agent.PlannerMaxSteps != 0 {
		t.Fatalf("deprecated limits should normalize to zero, got max=%d planner=%d", cfg.Agent.MaxSteps, cfg.Agent.PlannerMaxSteps)
	}
	if cfg.Agent.Temperature != 0.4 {
		t.Fatalf("unrelated agent setting changed: temperature=%v", cfg.Agent.Temperature)
	}
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, changed := stripLegacyAgentStepLimitLines(string(raw)); changed {
		t.Fatalf("saved config retained deprecated step limits:\n%s", raw)
	}
}

func TestIsolatedHomeDirEmptyByDefault(t *testing.T) {
	t.Setenv("REASONIX_HOME", "")
	if got := IsolatedHomeDir(); got != "" {
		t.Fatalf("IsolatedHomeDir() = %q, want empty", got)
	}
}

func TestIsolatedHomeDirReturnsCleanPath(t *testing.T) {
	raw := filepath.Join(t.TempDir(), "isolated-reasonix")
	t.Setenv("REASONIX_HOME", raw)
	got := IsolatedHomeDir()
	if filepath.Clean(got) != filepath.Clean(raw) {
		t.Fatalf("IsolatedHomeDir() = %q, want %q", got, raw)
	}
}

func TestLegacyOSSupportDirEmptyWhenIsolated(t *testing.T) {
	isolateUserConfigHome(t)
	t.Setenv("REASONIX_HOME", filepath.Join(t.TempDir(), "isolated-home"))
	if got := legacyOSSupportDir(); got != "" {
		t.Fatalf("legacyOSSupportDir() = %q, want empty when isolated", got)
	}
}

func TestLegacyXDGConfigPathsEmptyWhenIsolated(t *testing.T) {
	isolateUserConfigHome(t)
	t.Setenv("REASONIX_HOME", filepath.Join(t.TempDir(), "isolated-home"))
	if got := legacyXDGConfigPaths(); got != nil {
		t.Fatalf("legacyXDGConfigPaths() = %v, want nil when isolated", got)
	}
}

func TestCacheDirHonorsReasonixHome(t *testing.T) {
	home := t.TempDir()
	isolated := filepath.Join(home, "isolated-home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("REASONIX_HOME", isolated)

	got := CacheDir()
	want := filepath.Join(isolated, "cache")
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("CacheDir() = %q, want %q", got, want)
	}
}

func TestCacheDirHonorsReasonixCacheHomeOverReasonixHome(t *testing.T) {
	home := t.TempDir()
	cacheHome := filepath.Join(home, "custom-cache")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("REASONIX_HOME", filepath.Join(home, "isolated-home"))
	t.Setenv("REASONIX_CACHE_HOME", cacheHome)

	got := CacheDir()
	want := cacheHome
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("CacheDir() = %q, want %q (REASONIX_CACHE_HOME must win)", got, want)
	}
}

func TestUserConfigLoadPathNoLegacyFallbackWhenIsolated(t *testing.T) {
	home := isolateUserConfigHome(t)
	isolated := filepath.Join(home, "isolated-home")
	t.Setenv("REASONIX_HOME", isolated)

	// Create a legacy config at the OS production path — it must not be loaded.
	productionHome := expectedDefaultReasonixHome(home)
	if err := os.MkdirAll(productionHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(productionHome, "config.toml"), []byte("default_model = \"production/model\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The primary config under isolated home does not exist yet.
	got := userConfigLoadPath()
	want := filepath.Join(isolated, "config.toml")
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("userConfigLoadPath() = %q, want %q (must not fall back to production legacy config)", got, want)
	}
}

func TestCredentialSourceCandidatesSkipHomeEnvWhenIsolated(t *testing.T) {
	isolateUserConfigHome(t)
	t.Setenv("REASONIX_HOME", filepath.Join(t.TempDir(), "isolated-home"))

	// Write a key into the production home .env — it must not appear as a source.
	if home, err := os.UserHomeDir(); err == nil {
		if err := os.WriteFile(filepath.Join(home, ".env"), []byte("LEAKED_KEY=leaked-value\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	candidates := credentialSourceCandidates(".")
	for _, c := range candidates {
		if c.Kind == CredentialSourceHomeEnv {
			t.Fatalf("credentialSourceCandidates includes CredentialSourceHomeEnv when isolated: %v", c)
		}
	}
}

func TestMigrateLegacyIfNeededSkipsWhenIsolated(t *testing.T) {
	home := isolateUserConfigHome(t)
	isolated := filepath.Join(home, "isolated-home")
	t.Setenv("REASONIX_HOME", isolated)

	// Create a legacy config.json in production home — migration must skip it.
	legacyDir := filepath.Join(home, ".reasonix")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "config.json"), []byte(`{"model":"production-model","apiKey":"sk-legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := MigrateLegacyIfNeeded()
	if err != nil {
		t.Fatalf("MigrateLegacyIfNeeded() error = %v", err)
	}
	if res != nil {
		t.Fatalf("MigrateLegacyIfNeeded() = %+v, want nil when isolated", res)
	}
}

// TestProjectConfigCannotOverrideSecrets pins [secrets] as a user-global
// security control: a cloned repository's reasonix.toml must not be able to
// opt the user into subprocess env stripping or sensitive-path hiding.
func TestProjectConfigCannotOverrideSecrets(t *testing.T) {
	isolateUserConfigHome(t)
	t.Setenv("REASONIX_HOME", "")
	globalDir := filepath.Dir(UserConfigPath())
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	globalTOML := "[secrets]\nfilter_subprocess_env = false\nprotect_sensitive_files = false\n"
	if err := os.WriteFile(filepath.Join(globalDir, "config.toml"), []byte(globalTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	project := t.TempDir()
	projectTOML := "[secrets]\nfilter_subprocess_env = true\nprotect_sensitive_files = true\n"
	if err := os.WriteFile(filepath.Join(project, "reasonix.toml"), []byte(projectTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForRoot(project)
	if err != nil {
		t.Fatalf("LoadForRoot() error = %v", err)
	}
	if cfg.Secrets.FilterSubprocessEnv {
		t.Error("project reasonix.toml enabled filter_subprocess_env; [secrets] must stay user-global")
	}
	if cfg.Secrets.ProtectSensitiveFiles {
		t.Error("project reasonix.toml enabled protect_sensitive_files; [secrets] must stay user-global")
	}
}

// TestRenderTOMLPersistsSecretsSection pins config-save round-tripping: the
// renderer must emit [secrets] for the user scope or every WriteFile would
// silently drop the user's security toggles.
func TestRenderTOMLPersistsSecretsSection(t *testing.T) {
	cfg := Default()
	cfg.Secrets.FilterSubprocessEnv = true
	cfg.Secrets.ProtectSensitiveFiles = true

	out := RenderTOMLForScope(cfg, RenderScopeUser)
	for _, want := range []string{"[secrets]", "filter_subprocess_env = true", "protect_sensitive_files = true"} {
		if !strings.Contains(out, want) {
			t.Fatalf("user-scope render missing %q:\n%s", want, out)
		}
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	back := Default()
	if err := mergeFile(back, path); err != nil {
		t.Fatalf("round-trip decode: %v", err)
	}
	if !back.Secrets.FilterSubprocessEnv || !back.Secrets.ProtectSensitiveFiles {
		t.Fatalf("secrets toggles lost in render round-trip: %+v", back.Secrets)
	}

	// Project scope must not render the section — LoadForRoot ignores it there.
	if proj := RenderTOMLForScope(cfg, RenderScopeProject); strings.Contains(proj, "[secrets]") {
		t.Fatalf("project scope rendered [secrets]:\n%s", proj)
	}
	if strings.Contains(out, "redact_tool_output") {
		t.Fatalf("user-scope render still exposes removed live-redaction setting:\n%s", out)
	}
}
