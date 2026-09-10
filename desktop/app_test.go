package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/billing"
	"reasonix/internal/boot"
	"reasonix/internal/bot"
	"reasonix/internal/command"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/history"
	"reasonix/internal/instruction"
	"reasonix/internal/jobs"
	"reasonix/internal/mcplaunch"
	"reasonix/internal/memory"
	"reasonix/internal/plugin"
	"reasonix/internal/pluginpkg"
	"reasonix/internal/provider"
	"reasonix/internal/sandbox"
	"reasonix/internal/skill"
	"reasonix/internal/stats"
	"reasonix/internal/store"
	"reasonix/internal/taskcatalog"
	"reasonix/internal/tool"
)

type todoMetaController struct {
	stubSessionAPI
	todos []evidence.TodoItem
}

func (c *todoMetaController) Todos() []evidence.TodoItem {
	return append([]evidence.TodoItem(nil), c.todos...)
}

func TestCanonicalTodosMetaWireContract(t *testing.T) {
	if got := ctrlTodos(nil); got != nil {
		t.Fatalf("nil controller todos = %+v, want unavailable", *got)
	}

	empty := Meta{CanonicalTodos: ctrlTodos(&todoMetaController{})}
	raw, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty canonical todos: %v", err)
	}
	if !strings.Contains(string(raw), `"canonicalTodos":[]`) {
		t.Fatalf("empty canonical todos must encode as an authoritative empty array: %s", raw)
	}

	ctrl := &todoMetaController{todos: []evidence.TodoItem{{Content: "Ship", Status: "completed"}}}
	got := ctrlTodos(ctrl)
	if got == nil || len(*got) != 1 || (*got)[0].Status != "completed" {
		t.Fatalf("canonical todos = %+v, want completed task", got)
	}

	unavailable, err := json.Marshal(Meta{CanonicalTodos: ctrlTodos(nil)})
	if err != nil {
		t.Fatalf("marshal unavailable canonical todos: %v", err)
	}
	if strings.Contains(string(unavailable), "canonicalTodos") {
		t.Fatalf("unavailable canonical todos should preserve the legacy fallback contract: %s", unavailable)
	}
}

func TestPluginToolsToViewPreservesSchemaError(t *testing.T) {
	got := pluginToolsToView([]plugin.ToolInfo{{
		Name: "generate_yso_bytes", Description: "Generate payload", ReadOnlyHint: true,
		SchemaError: "invalid input schema: bad nested type",
	}})
	if len(got) != 1 || got[0].SchemaError != "invalid input schema: bad nested type" {
		t.Fatalf("tool views = %+v", got)
	}
}

func desktopMCPHTTPServer(t *testing.T) *httptest.Server {
	return desktopMCPHTTPServerWithTool(t, "h", "greet")
}

func desktopMCPHTTPServerWithTool(t *testing.T, serverName, toolName string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2024-11-05",
				"serverInfo":      map[string]any{"name": serverName, "version": "0"},
			}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{
				"name":        toolName,
				"description": "Greet someone.",
				"inputSchema": map[string]any{"type": "object"},
			}}}
		default:
			result = map[string]any{}
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestDesktopMCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_DESKTOP_MCP_HELPER") != "1" {
		return
	}
	if addr := os.Getenv("DESKTOP_MCP_START_GATE_ADDR"); addr != "" {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "connect MCP start gate %s: %v\n", addr, err)
			os.Exit(24)
		}
		var release [1]byte
		if _, err := io.ReadFull(conn, release[:]); err != nil {
			_ = conn.Close()
			_, _ = fmt.Fprintf(os.Stderr, "wait for MCP start gate %s: %v\n", addr, err)
			os.Exit(25)
		}
		_ = conn.Close()
	}
	var instanceListener net.Listener
	if addr := os.Getenv("DESKTOP_MCP_SINGLE_INSTANCE_ADDR"); addr != "" {
		var err error
		instanceListener, err = net.Listen("tcp", addr)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "another MCP instance is already using %s: %v\n", addr, err)
			os.Exit(23)
		}
		defer instanceListener.Close()
	}
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatalf("decode helper request: %v", err)
		}
		if req.ID == nil {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2024-11-05",
				"serverInfo":      map[string]any{"name": "desktop-helper", "version": "0"},
			}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{
				"name": "greet", "description": "Greet someone.",
				"inputSchema": map[string]any{"type": "object"},
			}}}
		default:
			result = map[string]any{}
		}
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result}); err != nil {
			t.Fatalf("encode helper response: %v", err)
		}
	}
}

// setTestCtrl creates a minimal workspace tab (if needed) and sets its
// controller, so tests don't depend on the old App.ctrl field.
func (a *App) setTestCtrl(ctrl control.SessionAPI, model string) {
	if len(a.tabs) == 0 {
		tab := &WorkspaceTab{
			ID:          "test",
			Scope:       "global",
			Ready:       true,
			disabledMCP: map[string]ServerView{},
		}
		a.tabs = map[string]*WorkspaceTab{"test": tab}
		a.activeTabID = "test"
	}
	tab := a.tabs["test"]
	tab.Ctrl = ctrl
	a.bindControllerDisplayRecorder(ctrl)
	tab.model = model
}

func isolateDesktopUserDirs(t *testing.T) string {
	t.Helper()
	home := robustTempDir(t)
	xdg := filepath.Join(home, ".config")
	appData := filepath.Join(home, "AppData")
	for _, dir := range []string{xdg, appData} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("REASONIX_CREDENTIALS_STORE", "file")
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("REASONIX_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("REASONIX_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("AppData", appData)
	// Close process-local SQLite handles before TempDir cleanup for Windows.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		desktopTopicState.close()
		_ = history.CloseSharedCatalog(ctx)
		_ = stats.CloseUsageCatalogs(ctx)
		_ = taskcatalog.ShutdownShared(ctx)
	})
	return home
}

func primarySessionFiles(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if store.IsSessionTranscriptName(filepath.Base(path)) {
			out = append(out, path)
		}
	}
	return out
}

func readConflictLogLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read conflict log: %v", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func setDesktopTestCredential(t *testing.T, key, value string) {
	t.Helper()
	if _, err := config.SetCredential(key, value); err != nil {
		t.Fatalf("SetCredential(%s): %v", key, err)
	}
}

func TestNeedsOnboardingIgnoresInheritedEnv(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv(onboardingKeyEnv, "inherited-key")

	app := NewApp()
	if !app.NeedsOnboarding() {
		t.Fatal("NeedsOnboarding should require a key saved in Reasonix global .env")
	}
	setDesktopTestCredential(t, onboardingKeyEnv, "saved-key")
	if app.NeedsOnboarding() {
		t.Fatal("NeedsOnboarding should be false after saving the global credential")
	}
}

func TestNeedsOnboardingTreatsBlankSavedKeyAsMissing(t *testing.T) {
	isolateDesktopUserDirs(t)
	if err := os.MkdirAll(filepath.Dir(config.UserCredentialsPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.UserCredentialsPath(), []byte(onboardingKeyEnv+"=\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	if !app.NeedsOnboarding() {
		t.Fatal("NeedsOnboarding should require a non-empty saved credential")
	}
}

func TestNeedsOnboardingAcceptsConfiguredCustomProvider(t *testing.T) {
	isolateDesktopUserDirs(t)
	cfg := config.Default()
	cfg.DefaultModel = "custom/custom-model"
	cfg.Desktop.ProviderAccess = []string{"custom"}
	cfg.Providers = []config.ProviderEntry{{
		Name: "custom", Kind: "openai", BaseURL: "https://models.example.invalid/v1",
		Model: "custom-model", APIKeyEnv: "CUSTOM_API_KEY",
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save custom provider config: %v", err)
	}
	setDesktopTestCredential(t, "CUSTOM_API_KEY", "saved-custom-key")

	if NewApp().NeedsOnboarding() {
		t.Fatal("NeedsOnboarding should be false when a custom provider is configured")
	}
}

func TestNeedsOnboardingAcceptsNoAuthLocalProvider(t *testing.T) {
	isolateDesktopUserDirs(t)
	cfg := config.Default()
	cfg.DefaultModel = "local/local-model"
	cfg.Desktop.ProviderAccess = []string{"local"}
	cfg.Providers = []config.ProviderEntry{{
		Name: "local", Kind: "openai", BaseURL: "http://127.0.0.1:11434/v1",
		Model: "local-model",
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save local provider config: %v", err)
	}

	if NewApp().NeedsOnboarding() {
		t.Fatal("NeedsOnboarding should be false for a no-auth local provider")
	}
}

func providerNamesFromView(providers []ProviderView) []string {
	out := make([]string, 0, len(providers))
	for _, p := range providers {
		out = append(out, p.Name)
	}
	return out
}

func modelRefsFromView(models []ModelInfo) map[string]bool {
	out := map[string]bool{}
	for _, m := range models {
		out[m.Ref] = true
	}
	return out
}

type desktopFakeTool struct {
	name string
}

func (t desktopFakeTool) Name() string { return t.name }

func (desktopFakeTool) Description() string { return "fake desktop tool" }

func (desktopFakeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }

func (desktopFakeTool) Execute(context.Context, json.RawMessage) (string, error) { return "", nil }

func (desktopFakeTool) ReadOnly() bool { return true }

type desktopAskRuntimeRunner struct {
	ask func(context.Context) error
}

func (r *desktopAskRuntimeRunner) Run(ctx context.Context, _ string) error {
	if r.ask == nil {
		return nil
	}
	return r.ask(ctx)
}

func TestCommandsIncludesDocsAndEffortNotThinking(t *testing.T) {
	app := NewApp()
	cmds := app.Commands()
	if !hasCommand(cmds, "docs") {
		t.Fatalf("Commands() should include docs: %+v", cmds)
	}
	if !hasCommand(cmds, "effort") {
		t.Fatalf("Commands() should include effort: %+v", cmds)
	}
	if !hasCommand(cmds, "reload") {
		t.Fatalf("Commands() should include reload: %+v", cmds)
	}
	if hasCommand(cmds, "thinking") {
		t.Fatalf("Commands() should not include thinking: %+v", cmds)
	}
}

func TestCommandsDocsShowsOnlyRuntimeWinner(t *testing.T) {
	tests := []struct {
		name     string
		commands []command.Command
		skills   []skill.Skill
		wantKind string
	}{
		{
			name:     "custom command shadows builtin",
			commands: []command.Command{{Name: "docs", Description: "custom docs"}},
			wantKind: "custom",
		},
		{
			name:     "skill shadows builtin",
			skills:   []skill.Skill{{Name: "docs", Description: "docs skill"}},
			wantKind: "skill",
		},
		{
			name:     "custom command shadows skill and builtin",
			commands: []command.Command{{Name: "docs", Description: "custom docs"}},
			skills:   []skill.Skill{{Name: "docs", Description: "docs skill"}},
			wantKind: "custom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := control.New(control.Options{Commands: tt.commands, Skills: tt.skills})
			defer ctrl.Close()
			app := NewApp()
			app.setTestCtrl(ctrl, "")

			var docs []CommandInfo
			for _, cmd := range app.Commands() {
				if cmd.Name == "docs" {
					docs = append(docs, cmd)
				}
			}
			if len(docs) != 1 || docs[0].Kind != tt.wantKind {
				t.Fatalf("docs commands = %+v, want one %s entry", docs, tt.wantKind)
			}
			if fallback, ok := commandInfoByName(app.Commands(), control.ReasonixDocsSlashName); !ok || fallback.Kind != "builtin" {
				t.Fatalf("qualified docs fallback = %+v, %v; want built-in", fallback, ok)
			}
		})
	}
}

func commandInfoByName(commands []CommandInfo, name string) (CommandInfo, bool) {
	for _, command := range commands {
		if command.Name == name {
			return command, true
		}
	}
	return CommandInfo{}, false
}

func TestCommandsDocsAccountsForHiddenCompatibilityAliases(t *testing.T) {
	tests := []struct {
		name          string
		commands      []command.Command
		skills        []skill.Skill
		wantCanonical string
	}{
		{
			name: "hidden plugin command alias",
			commands: []command.Command{
				{Name: "docs", Plugin: "manuals", Hidden: true},
				{Name: "manuals:docs", Plugin: "manuals"},
			},
			wantCanonical: "manuals:docs",
		},
		{
			name:          "compatible plugin skill alias",
			skills:        []skill.Skill{{Name: "docs", Plugin: "manuals"}},
			wantCanonical: "manuals:docs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := control.New(control.Options{Commands: tt.commands, Skills: tt.skills})
			defer ctrl.Close()
			app := NewApp()
			app.setTestCtrl(ctrl, "")
			commands := app.Commands()
			if _, ok := commandInfoByName(commands, "docs"); ok {
				t.Fatalf("hidden runtime owner left a misleading docs entry: %+v", commands)
			}
			for _, want := range []string{control.ReasonixDocsSlashName, tt.wantCanonical} {
				if _, ok := commandInfoByName(commands, want); !ok {
					t.Fatalf("commands missing %q: %+v", want, commands)
				}
			}
		})
	}
}

func TestCommandsDocsDoesNotDisplaceQualifiedCustomCommands(t *testing.T) {
	ctrl := control.New(control.Options{Commands: []command.Command{
		{Name: "docs", Description: "custom docs"},
		{Name: "reasonix:docs", Description: "qualified custom docs"},
		{Name: "reasonix:builtin:docs", Description: "second qualified custom docs"},
	}})
	defer ctrl.Close()
	app := NewApp()
	app.setTestCtrl(ctrl, "")
	commands := app.Commands()
	for _, want := range []struct {
		name string
		kind string
	}{
		{name: "docs", kind: "custom"},
		{name: "reasonix:docs", kind: "custom"},
		{name: "reasonix:builtin:docs", kind: "custom"},
		{name: "reasonix:builtin:docs:2", kind: "builtin"},
	} {
		if command, ok := commandInfoByName(commands, want.name); !ok || command.Kind != want.kind {
			t.Fatalf("command %q = %+v, %v; want kind %q", want.name, command, ok, want.kind)
		}
	}
}

func TestCommandsClassifiesSubagentSkills(t *testing.T) {
	ctrl := control.New(control.Options{Skills: []skill.Skill{
		{Name: "init", Description: "inline skill", RunAs: skill.RunInline},
		{Name: "explore", Description: "isolated skill", RunAs: skill.RunSubagent, Color: "amber"},
	}})
	defer ctrl.Close()
	app := NewApp()
	app.setTestCtrl(ctrl, "")

	kinds := map[string]string{}
	groups := map[string]string{}
	colors := map[string]string{}
	for _, cmd := range app.Commands() {
		kinds[cmd.Name] = cmd.Kind
		groups[cmd.Name] = cmd.Group
		colors[cmd.Name] = cmd.Color
	}
	if kinds["init"] != "skill" {
		t.Fatalf("inline skill kind = %q, want skill", kinds["init"])
	}
	if kinds["explore"] != "subagent" {
		t.Fatalf("subagent skill kind = %q, want subagent", kinds["explore"])
	}
	if colors["explore"] != "amber" {
		t.Fatalf("subagent skill color = %q, want amber", colors["explore"])
	}
	if groups["new"] != "actions" {
		t.Fatalf("new command group = %q, want actions", groups["new"])
	}
	if groups["mcp"] != "integrations" || groups["plugins"] != "integrations" {
		t.Fatalf("integration command groups = mcp:%q plugins:%q", groups["mcp"], groups["plugins"])
	}
	if groups["skill"] != "skills" {
		t.Fatalf("skill command group = %q, want skills", groups["skill"])
	}
}

func TestMetaForTabIncludesWorkspaceContext(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	isolateDesktopUserDirs(t)
	resetWorkspaceGitBranchMetaCacheForTest(t)

	repo := t.TempDir()
	configuredSandboxRoot := filepath.Join(t.TempDir(), "sandbox")
	cfg := config.LoadForEdit(config.UserConfigPath())
	cfg.Sandbox.WorkspaceRoot = configuredSandboxRoot
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatal(err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatal(err)
		}
	}()
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	runGit(t, "init")
	runGit(t, "checkout", "-b", "feature/meta")

	app := NewApp()
	app.tabs = map[string]*WorkspaceTab{"tab-1": {
		ID:            "tab-1",
		Scope:         "project",
		WorkspaceRoot: repo,
		Ready:         true,
		disabledMCP:   map[string]ServerView{},
	}}
	app.activeTabID = "tab-1"

	got := app.MetaForTab("tab-1")
	if got.Cwd != repo || got.WorkspaceRoot != repo || got.WorkspacePath != repo {
		t.Fatalf("workspace fields = cwd:%q root:%q path:%q, want %q", got.Cwd, got.WorkspaceRoot, got.WorkspacePath, repo)
	}
	if got.WorkspaceName != filepath.Base(repo) {
		t.Fatalf("workspaceName = %q, want %q", got.WorkspaceName, filepath.Base(repo))
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if strings.Contains(string(raw), "sandboxPath") || strings.Contains(string(raw), configuredSandboxRoot) {
		t.Fatalf("meta should not expose configured sandbox root as sandboxPath: %s", raw)
	}
	// The first git process launch can be noticeably slower on Windows runners
	// while MetaForTab intentionally keeps the caller path non-blocking.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got = app.MetaForTab("tab-1"); got.GitBranch == "feature/meta" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("gitBranch = %q, want feature/meta after async refresh", got.GitBranch)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestListTabsDoesNotExposeConfiguredSandboxPath(t *testing.T) {
	isolateDesktopUserDirs(t)
	workspace := t.TempDir()
	configuredSandboxRoot := filepath.Join(t.TempDir(), "sandbox")
	cfg := config.LoadForEdit(config.UserConfigPath())
	cfg.Sandbox.WorkspaceRoot = configuredSandboxRoot
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.tabs = map[string]*WorkspaceTab{"tab-1": {
		ID:            "tab-1",
		Scope:         "project",
		WorkspaceRoot: workspace,
		Ready:         true,
		disabledMCP:   map[string]ServerView{},
	}}
	app.activeTabID = "tab-1"
	app.tabOrder = []string{"tab-1"}

	raw, err := json.Marshal(app.ListTabs())
	if err != nil {
		t.Fatalf("marshal tabs: %v", err)
	}
	if strings.Contains(string(raw), "sandboxPath") || strings.Contains(string(raw), configuredSandboxRoot) {
		t.Fatalf("tab metadata should not expose configured sandbox root as sandboxPath: %s", raw)
	}
}

func TestListTabsExposesStructuredRuntimeStatus(t *testing.T) {
	asks := make(chan event.Ask, 1)
	done := make(chan event.Event, 1)
	runner := &desktopAskRuntimeRunner{}
	ctrl := control.New(control.Options{
		Runner: runner,
		Sink: event.FuncSink(func(e event.Event) {
			switch e.Kind {
			case event.AskRequest:
				asks <- e.Ask
			case event.TurnDone:
				done <- e
			}
		}),
	})
	runner.ask = func(ctx context.Context) error {
		_, err := ctrl.Ask(ctx, []event.AskQuestion{{
			ID:      "choice",
			Prompt:  "Pick one",
			Options: []event.AskOption{{Label: "A"}, {Label: "B"}},
		}})
		return err
	}

	app := NewApp()
	app.setTestCtrl(ctrl, "prov/model")
	app.tabOrder = []string{"test"}
	ctrl.Send("ask user")
	select {
	case <-asks:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ask request")
	}

	tabs := app.ListTabs()
	if len(tabs) != 1 {
		t.Fatalf("tabs = %d, want 1", len(tabs))
	}
	if !tabs[0].Running || !tabs[0].PendingPrompt || !tabs[0].Cancellable || tabs[0].CancelRequested {
		t.Fatalf("tab runtime = running:%v pending:%v cancellable:%v cancel:%v", tabs[0].Running, tabs[0].PendingPrompt, tabs[0].Cancellable, tabs[0].CancelRequested)
	}

	app.CancelTab("test")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turn_done")
	}
}

func TestMetaForTabLeavesGitBranchEmptyOutsideGit(t *testing.T) {
	isolateDesktopUserDirs(t)
	workspace := t.TempDir()
	app := NewApp()
	app.tabs = map[string]*WorkspaceTab{"tab-1": {
		ID:            "tab-1",
		Scope:         "project",
		WorkspaceRoot: workspace,
		Ready:         true,
		disabledMCP:   map[string]ServerView{},
	}}
	app.activeTabID = "tab-1"

	if got := app.MetaForTab("tab-1"); got.GitBranch != "" {
		t.Fatalf("gitBranch = %q, want empty", got.GitBranch)
	}
}

func TestEffortDefaultsBeforeStartup(t *testing.T) {
	isolateDesktopUserDirs(t)

	got := NewApp().Effort()
	if !got.Supported || got.Current != "auto" || got.Default != "high" || !hasLevel(got.Levels, "auto") {
		t.Fatalf("pre-startup Effort() = %+v, want auto with DeepSeek default high", got)
	}
}

func TestMemoryViewReturnsNonNilArraysBeforeStartup(t *testing.T) {
	isolateDesktopUserDirs(t)

	view := NewApp().Memory()
	if view.Docs == nil || view.Facts == nil || view.Archives == nil || view.Scopes == nil || view.InstructionDiagnostics == nil || view.Conflicts == nil || view.LastRecall.Hits == nil {
		t.Fatalf("Memory() arrays must be non-nil before startup: %+v", view)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal Memory(): %v", err)
	}
	for _, bad := range []string{`"docs":null`, `"facts":null`, `"archives":null`, `"scopes":null`, `"instructionDiagnostics":null`, `"conflicts":null`, `"hits":null`} {
		if strings.Contains(string(raw), bad) {
			t.Fatalf("Memory() JSON contains %s; frontend expects []: %s", bad, raw)
		}
	}
	if revisions := NewApp().MemoryRevisions("missing"); revisions == nil {
		t.Fatal("MemoryRevisions must return [] before startup, not nil")
	}
}

func TestMemoryViewIncludesRecallFreshnessAndOverrides(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	store := memory.Store{Dir: filepath.Join(root, "project"), GlobalDir: filepath.Join(root, "global")}
	if _, err := (memory.Store{Dir: store.GlobalDir}).Save(memory.Memory{
		Name: "deploy-target", Title: "Deploy target", Description: "legacy deployment target", Scope: memory.FactScopeGlobal, Type: memory.TypeProject, Body: "Deploy payments to the legacy cluster.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := (memory.Store{Dir: store.Dir}).Save(memory.Memory{
		Name: "deploy-target", Title: "Deploy target", Description: "current deployment target", Scope: memory.FactScopeProject, Type: memory.TypeProject, Body: "Deploy payments to the green cluster.",
	}); err != nil {
		t.Fatal(err)
	}
	ctrl := control.New(control.Options{Memory: &memory.Set{Store: store}})
	ctrl.Compose("deploy payments target cluster")
	app := NewApp()
	app.setTestCtrl(ctrl, "test-model")

	view := app.Memory()
	if len(view.Facts) != 2 || view.Facts[0].Freshness == "" || view.Facts[1].Freshness == "" {
		t.Fatalf("facts with freshness = %+v", view.Facts)
	}
	if len(view.Conflicts) != 1 || view.Conflicts[0].Resolution != "project_over_global" {
		t.Fatalf("conflicts = %+v", view.Conflicts)
	}
	if view.LastRecall.Query != "deploy payments target cluster" || len(view.LastRecall.Hits) != 1 || view.LastRecall.Hits[0].Scope != "project" {
		t.Fatalf("last recall = %+v", view.LastRecall)
	}
}

func TestMemoryRevisionAPIRestoresSelectedRevision(t *testing.T) {
	isolateDesktopUserDirs(t)
	store := memory.Store{Dir: t.TempDir()}
	first, err := store.SaveWithOptions(memory.Memory{Name: "fact", Description: "one", Body: "v1"}, memory.SaveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveWithOptions(memory.Memory{ID: first.Memory.ID, Name: "fact", Description: "two", Body: "v2"}, memory.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Memory: &memory.Set{Store: store}}), "test-model")

	revisions := app.MemoryRevisions(first.Memory.ID)
	if len(revisions) != 1 || revisions[0].Revision != 1 {
		t.Fatalf("revisions = %+v", revisions)
	}
	restored, err := app.RestoreMemoryRevision(first.Memory.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != 3 || restored.Body != "v1" {
		t.Fatalf("restored = %+v", restored)
	}
}

func TestMemoryViewIncludesActiveAndArchivedFacts(t *testing.T) {
	isolateDesktopUserDirs(t)
	userDir := t.TempDir()
	cwd := t.TempDir()
	store := memory.Store{Dir: filepath.Join(userDir, "projects", "test", "memory")}
	if _, err := store.Save(memory.Memory{
		Name:        "active-fact",
		Title:       "Active fact",
		Description: "Still applies",
		Type:        memory.TypeProject,
		Body:        "Active body",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(memory.Memory{
		Name:        "archived-fact",
		Description: "No longer applies",
		Type:        memory.TypeFeedback,
		Body:        "Archived body",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Archive("archived-fact"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Memory: &memory.Set{
		Docs: []memory.Source{{
			Path: filepath.Join(cwd, "AGENTS.md"), Scope: memory.ScopeProject, Directory: cwd,
			Body: "Project instructions", Imports: []instruction.Import{{Path: filepath.Join(cwd, "shared.md"), SourcePath: filepath.Join(cwd, "AGENTS.md")}},
		}},
		InstructionDiagnostics: []instruction.Diagnostic{{Code: "import_cycle", Path: "shared.md", SourcePath: filepath.Join(cwd, "AGENTS.md"), Line: 3, Message: "cycle"}},
		Store:                  store, CWD: cwd, UserDir: userDir,
	}}), "test-model")

	view := app.Memory()
	if !view.Available || view.StoreDir != store.Dir {
		t.Fatalf("Memory() availability/store = %v/%q, want true/%q", view.Available, view.StoreDir, store.Dir)
	}
	if len(view.Docs) != 1 || view.Docs[0].Scope != "project" || !strings.Contains(view.Docs[0].Body, "Project instructions") {
		t.Fatalf("Memory() docs = %+v", view.Docs)
	}
	if view.Docs[0].Directory != cwd || len(view.Docs[0].Imports) != 1 || len(view.InstructionDiagnostics) != 1 || view.InstructionDiagnostics[0].Code != "import_cycle" {
		t.Fatalf("Memory() instruction provenance = docs %+v diagnostics %+v", view.Docs, view.InstructionDiagnostics)
	}
	if len(view.Facts) != 1 || view.Facts[0].Name != "active-fact" || view.Facts[0].Type != "project" || view.Facts[0].Scope != "project" {
		t.Fatalf("Memory() active facts = %+v", view.Facts)
	}
	if view.Facts[0].ID == "" || view.Facts[0].Revision != 1 || view.Facts[0].CreatedAt == "" || view.Facts[0].UpdatedAt == "" {
		t.Fatalf("Memory() active fact metadata = %+v", view.Facts[0])
	}
	if len(view.Archives) != 1 || view.Archives[0].Name != "archived-fact" || view.Archives[0].Type != "feedback" || view.Archives[0].Scope != "project" ||
		view.Archives[0].Path == "" || view.Archives[0].ArchivedAt == "" {
		t.Fatalf("Memory() archived facts = %+v", view.Archives)
	}
	if view.Archives[0].ID == "" || view.Archives[0].Revision != 1 || view.Archives[0].CreatedAt == "" || view.Archives[0].UpdatedAt == "" {
		t.Fatalf("Memory() archived fact metadata = %+v", view.Archives[0])
	}
	if len(view.Scopes) != 3 {
		t.Fatalf("Memory() scopes = %+v, want user/project/local", view.Scopes)
	}
}

func TestRestoreArchivedMemoryRecoversFactForCurrentSession(t *testing.T) {
	isolateDesktopUserDirs(t)
	userDir := t.TempDir()
	cwd := t.TempDir()
	store := memory.StoreFor(userDir, cwd)
	first, err := store.SaveWithOptions(memory.Memory{
		Name: "restorable-fact", Description: "recover me", Body: "Recovered guidance.",
	}, memory.SaveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	archivePath, err := store.Archive(first.Memory.ID)
	if err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Memory: &memory.Set{Store: store, CWD: cwd, UserDir: userDir}}), "test-model")
	if _, err := app.RestoreArchivedMemory(archivePath); err != nil {
		t.Fatal(err)
	}
	view := app.Memory()
	if len(view.Facts) != 1 || view.Facts[0].ID != first.Memory.ID || view.Facts[0].Revision != 2 {
		t.Fatalf("restored memory view = %+v", view)
	}
	if len(view.Archives) != 0 {
		t.Fatalf("restored archive remained visible: %+v", view.Archives)
	}
}

func TestBeforeCloseAllowsSystemQuitWhenBackgroundCloseEnabled(t *testing.T) {
	isolateDesktopUserDirs(t)
	consumeSystemQuitRequested()
	t.Cleanup(func() { consumeSystemQuitRequested() })

	userCfg := config.LoadForEdit(config.UserConfigPath())
	if err := userCfg.SetDesktopCloseBehavior("background"); err != nil {
		t.Fatal(err)
	}
	if err := userCfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatal(err)
	}

	markSystemQuitRequested()
	if prevent := NewApp().beforeClose(context.Background()); prevent {
		t.Fatal("system quit should bypass background close-to-tray behavior")
	}
	if consumeSystemQuitRequested() {
		t.Fatal("system quit marker should be consumed by beforeClose")
	}
}

func TestBackgroundCloseHideStrategyByPlatform(t *testing.T) {
	tests := []struct {
		goos string
		want bool
	}{
		{goos: "darwin", want: true},
		{goos: "windows", want: false},
		{goos: "linux", want: false},
		{goos: "freebsd", want: false},
	}
	for _, tt := range tests {
		if got := backgroundCloseUsesApplicationHide(tt.goos); got != tt.want {
			t.Fatalf("backgroundCloseUsesApplicationHide(%q) = %v, want %v", tt.goos, got, tt.want)
		}
	}
}

func TestBackgroundCloseRequiresRestorePath(t *testing.T) {
	tests := []struct {
		name        string
		goos        string
		trayStarted bool
		trayReady   bool
		want        bool
	}{
		{name: "macOS restores from Dock", goos: "darwin", trayStarted: false, trayReady: false, want: true},
		{name: "Windows tray ready", goos: "windows", trayStarted: true, trayReady: true, want: true},
		{name: "Windows tray started but not ready", goos: "windows", trayStarted: true, trayReady: false, want: false},
		{name: "Linux tray ready", goos: "linux", trayStarted: true, trayReady: true, want: true},
		{name: "Linux tray started but not ready", goos: "linux", trayStarted: true, trayReady: false, want: false},
		{name: "Linux no tray", goos: "linux", trayStarted: false, trayReady: false, want: false},
		{name: "other Unix no tray", goos: "freebsd", trayStarted: false, trayReady: false, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := backgroundCloseHasRestorePathFor(tt.goos, tt.trayStarted, tt.trayReady); got != tt.want {
				t.Fatalf("backgroundCloseHasRestorePathFor(%q, %v, %v) = %v, want %v", tt.goos, tt.trayStarted, tt.trayReady, got, tt.want)
			}
		})
	}
}

func TestBackgroundCloseReadySignalRequiresCurrentReadyState(t *testing.T) {
	app := NewApp()
	tray := newDesktopTray()
	app.mu.Lock()
	app.tray = tray
	app.mu.Unlock()

	if app.waitForTrayReady(0) {
		t.Fatal("tray should not be ready before its ready signal")
	}

	tray.markReady()
	if app.waitForTrayReady(0) {
		t.Fatal("closed ready signal should not count without the current ready state")
	}

	app.mu.Lock()
	app.trayReady = true
	app.mu.Unlock()
	if !app.waitForTrayReady(0) {
		t.Fatal("ready state should be accepted after the tray is marked ready")
	}

	app.mu.Lock()
	app.trayReady = false
	app.mu.Unlock()
	if app.waitForTrayReady(0) {
		t.Fatal("stale ready signal should not count after the tray exits")
	}
}

func TestBackgroundCloseWaitsForTrayReadySignal(t *testing.T) {
	app := NewApp()
	tray := newDesktopTray()
	app.mu.Lock()
	app.tray = tray
	app.mu.Unlock()

	go func() {
		time.Sleep(10 * time.Millisecond)
		app.mu.Lock()
		app.trayReady = true
		app.mu.Unlock()
		tray.markReady()
	}()

	if !app.waitForTrayReady(200 * time.Millisecond) {
		t.Fatal("waitForTrayReady should observe the tray becoming ready")
	}
}

func TestBackgroundRestoreMaximiseStrategy(t *testing.T) {
	tests := []struct {
		goos      string
		maximised bool
		want      bool
	}{
		{goos: "windows", maximised: true, want: true},
		{goos: "linux", maximised: true, want: true},
		{goos: "darwin", maximised: true, want: false},
		{goos: "windows", maximised: false, want: false},
	}
	for _, tt := range tests {
		if got := backgroundRestoreShouldMaximise(tt.goos, tt.maximised); got != tt.want {
			t.Fatalf("backgroundRestoreShouldMaximise(%q, %v) = %v, want %v", tt.goos, tt.maximised, got, tt.want)
		}
	}
}

func TestBackgroundRestorePlanAvoidsNormalWindowFlash(t *testing.T) {
	tests := []struct {
		name      string
		goos      string
		maximised bool
		want      backgroundRestorePlan
	}{
		{
			name:      "maximised Windows window",
			goos:      "windows",
			maximised: true,
			want:      backgroundRestorePlan{maximiseBeforeShow: true},
		},
		{
			name:      "normal Windows window",
			goos:      "windows",
			maximised: false,
			want:      backgroundRestorePlan{unminimiseAfterShow: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := backgroundRestorePlanFor(tt.goos, tt.maximised)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("backgroundRestorePlanFor(%q, %v) = %v, want %v", tt.goos, tt.maximised, got, tt.want)
			}
		})
	}
}

func TestEmitReadyInvokesReadyHook(t *testing.T) {
	app := NewApp()
	var calls atomic.Int32
	app.readyHook = func() {
		calls.Add(1)
	}

	app.emitReady(context.TODO())

	if got := calls.Load(); got != 1 {
		t.Fatalf("ready hook calls = %d, want 1", got)
	}
}

func TestSetEffortPersistsAndAutoClears(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	if err := app.SetEffort("max"); err != nil {
		t.Fatalf("SetEffort(max): %v", err)
	}
	if got := app.Effort().Current; got != "max" {
		t.Fatalf("Effort current = %q, want max", got)
	}
	if err := app.SetEffort("auto"); err != nil {
		t.Fatalf("SetEffort(auto): %v", err)
	}
	if got := app.Effort().Current; got != "auto" {
		t.Fatalf("Effort current = %q, want auto", got)
	}
	body, err := os.ReadFile(config.UserConfigPath())
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if strings.Contains(string(body), `effort      = "max"`) {
		t.Fatalf("auto should clear explicit max effort:\n%s", body)
	}
}

func TestSettingsUsesUserDesktopPreferencesNotProjectConfig(t *testing.T) {
	isolateDesktopUserDirs(t)

	project := robustTempDir(t)
	if err := os.WriteFile(filepath.Join(project, "reasonix.toml"), []byte(`
[desktop]
language = "zh"
layout_style = "workbench"
theme = "light"
theme_style = "glacier"
close_behavior = "quit"
status_bar_style = "icon"
status_bar_items = ["cost", "balance"]
`), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	userCfg := config.LoadForEdit(config.UserConfigPath())
	if err := userCfg.SetDesktopLanguage("en"); err != nil {
		t.Fatalf("set desktop language: %v", err)
	}
	if err := userCfg.SetDesktopLayoutStyle("classic"); err != nil {
		t.Fatalf("set desktop layout style: %v", err)
	}
	if err := userCfg.SetDesktopAppearance("dark", "graphite"); err != nil {
		t.Fatalf("set desktop appearance: %v", err)
	}
	if err := userCfg.SetDesktopTerminalTheme("light"); err != nil {
		t.Fatalf("set desktop terminal theme: %v", err)
	}
	if err := userCfg.SetDesktopCloseBehavior("background"); err != nil {
		t.Fatalf("set desktop close behavior: %v", err)
	}
	if err := userCfg.SetDesktopStatusBarStyle("text"); err != nil {
		t.Fatalf("set desktop status bar style: %v", err)
	}
	if err := userCfg.SetDesktopStatusBarItems([]string{"model", "balance", "cache"}); err != nil {
		t.Fatalf("set desktop status bar items: %v", err)
	}
	if err := userCfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save user config: %v", err)
	}

	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(project); err != nil {
		t.Fatalf("chdir project: %v", err)
	}

	got := NewApp().Settings()
	if got.DesktopLanguage != "en" || got.DesktopLayoutStyle != "workbench" || got.DesktopTheme != "dark" || got.DesktopThemeStyle != "graphite" || got.DesktopTerminalTheme != "light" || got.CloseBehavior != "background" || got.StatusBarStyle != "text" {
		t.Fatalf("desktop settings = lang:%q layout:%q theme:%q style:%q close:%q status:%q, want user-level desktop prefs", got.DesktopLanguage, got.DesktopLayoutStyle, got.DesktopTheme, got.DesktopThemeStyle, got.CloseBehavior, got.StatusBarStyle)
	}
	if want := []string{"model", "balance", "cache"}; !reflect.DeepEqual(got.StatusBarItems, want) {
		t.Fatalf("desktop status bar items = %v, want user-level %v", got.StatusBarItems, want)
	}
}

func TestDesktopStartupSettingsUsesUserDesktopPreferencesWithoutFullSettingsPayload(t *testing.T) {
	isolateDesktopUserDirs(t)

	userCfg := config.LoadForEdit(config.UserConfigPath())
	if err := userCfg.SetDesktopLanguage("en"); err != nil {
		t.Fatalf("set desktop language: %v", err)
	}
	if err := userCfg.SetDesktopLayoutStyle("classic"); err != nil {
		t.Fatalf("set desktop layout style: %v", err)
	}
	if err := userCfg.SetDesktopAppearance("dark", "graphite"); err != nil {
		t.Fatalf("set desktop appearance: %v", err)
	}
	if err := userCfg.SetDesktopTerminalTheme("light"); err != nil {
		t.Fatalf("set desktop terminal theme: %v", err)
	}
	if err := userCfg.SetDesktopStatusBarStyle("icon"); err != nil {
		t.Fatalf("set desktop status bar style: %v", err)
	}
	if err := userCfg.SetDesktopStatusBarItems([]string{"workspace", "git_branch", "model"}); err != nil {
		t.Fatalf("set desktop status bar items: %v", err)
	}
	if err := userCfg.SetDesktopCheckUpdates(false); err != nil {
		t.Fatalf("set desktop check updates: %v", err)
	}
	if err := userCfg.SetDesktopUpdateChannel("preview"); err != nil {
		t.Fatalf("set desktop update channel: %v", err)
	}
	userCfg.Bot.Enabled = true
	userCfg.Bot.Allowlist.Enabled = true
	userCfg.Bot.Allowlist.QQUsers = []string{"alice"}
	if err := userCfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save user config: %v", err)
	}

	got := NewApp().DesktopStartupSettings()
	if got.DesktopLanguage != "en" || got.DesktopLayoutStyle != "workbench" || got.DesktopTheme != "dark" || got.DesktopThemeStyle != "graphite" || got.DesktopTerminalTheme != "light" || got.DisplayMode != "standard" || got.StatusBarStyle != "icon" || got.CheckUpdates || got.UpdateChannel != "stable" {
		t.Fatalf("DesktopStartupSettings desktop prefs = %+v, want user-level startup prefs", got)
	}
	if want := []string{"workspace", "git_branch", "model"}; !reflect.DeepEqual(got.StatusBarItems, want) {
		t.Fatalf("DesktopStartupSettings status bar items = %v, want %v", got.StatusBarItems, want)
	}
	if !got.Bot.Enabled || !got.Bot.Allowlist.Enabled || !reflect.DeepEqual(got.Bot.Allowlist.QQUsers, []string{"alice"}) {
		t.Fatalf("DesktopStartupSettings bot settings = %+v, want lightweight bot snapshot", got.Bot)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal DesktopStartupSettings: %v", err)
	}
	if strings.Contains(string(raw), "providers") || strings.Contains(string(raw), "officialProviders") || strings.Contains(string(raw), "providerKinds") {
		t.Fatalf("DesktopStartupSettings must not include full Settings provider payload: %s", raw)
	}
}

func BenchmarkDesktopSettingsPayloads(b *testing.B) {
	home := b.TempDir()
	xdg := filepath.Join(home, ".config")
	appData := filepath.Join(home, "AppData")
	for _, dir := range []string{xdg, appData} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
	}
	b.Setenv("HOME", home)
	b.Setenv("REASONIX_CREDENTIALS_STORE", "file")
	b.Setenv("USERPROFILE", home)
	b.Setenv("XDG_CONFIG_HOME", xdg)
	b.Setenv("REASONIX_STATE_HOME", filepath.Join(home, "state"))
	b.Setenv("REASONIX_CACHE_HOME", filepath.Join(home, "cache"))
	b.Setenv("AppData", appData)
	b.Setenv("SHARED_PROVIDER_KEY", "sk-test")

	cfg := config.LoadForEdit(config.UserConfigPath())
	for i := range 40 {
		cfg.Providers = append(cfg.Providers, config.ProviderEntry{
			Name:      fmt.Sprintf("custom-%02d", i),
			Kind:      "openai",
			BaseURL:   "https://example.invalid/v1",
			APIKeyEnv: "SHARED_PROVIDER_KEY",
			Models:    []string{"model-a", "model-b"},
			Default:   "model-a",
		})
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		b.Fatalf("save config: %v", err)
	}
	app := NewApp()

	b.Run("Settings", func(b *testing.B) {
		for range b.N {
			_ = app.Settings()
		}
	})
	b.Run("DesktopStartupSettings", func(b *testing.B) {
		for range b.N {
			_ = app.DesktopStartupSettings()
		}
	})
}

func TestSettingsIgnoresActiveWorkspaceDotEnvCredentialsWithUserConfig(t *testing.T) {
	isolateDesktopUserDirs(t)

	project := robustTempDir(t)
	launch := robustTempDir(t)
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("WORKSPACE_ONLY_KEY=from-project\n"), 0o600); err != nil {
		t.Fatalf("write project env: %v", err)
	}
	userCfg := config.LoadForEdit(config.UserConfigPath())
	if err := userCfg.UpsertProvider(config.ProviderEntry{
		Name:      "workspace-provider",
		Kind:      "openai",
		BaseURL:   "https://workspace.example/v1",
		Model:     "workspace-model",
		APIKeyEnv: "WORKSPACE_ONLY_KEY",
	}); err != nil {
		t.Fatalf("upsert provider: %v", err)
	}
	userCfg.Desktop.ProviderAccess = []string{"workspace-provider"}
	if err := userCfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save user config: %v", err)
	}
	t.Setenv("WORKSPACE_ONLY_KEY", "")
	os.Unsetenv("WORKSPACE_ONLY_KEY")
	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(launch); err != nil {
		t.Fatalf("chdir launch: %v", err)
	}

	app := NewApp()
	app.tabs = map[string]*WorkspaceTab{"project": {ID: "project", WorkspaceRoot: project}}
	app.activeTabID = "project"
	got := app.Settings()
	for _, p := range got.Providers {
		if p.Name == "workspace-provider" {
			if p.KeySet {
				t.Fatalf("workspace provider keySet = true, want false because workspace .env is ignored: %+v", p)
			}
			if p.Configured {
				t.Fatalf("workspace provider configured = true, want false because workspace .env is ignored: %+v", p)
			}
			return
		}
	}
	t.Fatalf("workspace provider missing from settings: %+v", got.Providers)
}

func TestSettingsShowsGlobalCredentialWithoutMutatingWorkspaceEnv(t *testing.T) {
	isolateDesktopUserDirs(t)

	project := robustTempDir(t)
	launch := robustTempDir(t)
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("SHARED_SETTINGS_KEY=from-project\n"), 0o600); err != nil {
		t.Fatalf("write project env: %v", err)
	}
	if _, err := config.SetCredential("SHARED_SETTINGS_KEY", "from-credentials"); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	userCfg := config.LoadForEditWithoutCredentials(config.UserConfigPath())
	if err := userCfg.UpsertProvider(config.ProviderEntry{
		Name:      "settings-provider",
		Kind:      "openai",
		BaseURL:   "https://settings.example/v1",
		Model:     "settings-model",
		APIKeyEnv: "SHARED_SETTINGS_KEY",
	}); err != nil {
		t.Fatalf("upsert provider: %v", err)
	}
	userCfg.Desktop.ProviderAccess = []string{"settings-provider"}
	if err := userCfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save user config: %v", err)
	}
	t.Setenv("SHARED_SETTINGS_KEY", "from-project")
	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(launch); err != nil {
		t.Fatalf("chdir launch: %v", err)
	}

	app := NewApp()
	app.tabs = map[string]*WorkspaceTab{"project": {ID: "project", WorkspaceRoot: project}}
	app.activeTabID = "project"
	got := app.Settings()
	for _, p := range got.Providers {
		if p.Name != "settings-provider" {
			continue
		}
		if !p.KeySet || !strings.Contains(p.KeySource, "Reasonix credentials") {
			t.Fatalf("settings-provider key = set:%v source:%q, want Reasonix credentials: %+v", p.KeySet, p.KeySource, p)
		}
		if env := os.Getenv("SHARED_SETTINGS_KEY"); env != "from-project" {
			t.Fatalf("Settings mutated SHARED_SETTINGS_KEY = %q, want existing project env", env)
		}
		return
	}
	t.Fatalf("settings provider missing from settings: %+v", got.Providers)
}

func TestSettingsSeedsMissingUserConfigFromLegacyProjectConfig(t *testing.T) {
	isolateDesktopUserDirs(t)

	project := robustTempDir(t)
	if err := os.WriteFile(filepath.Join(project, "reasonix.toml"), []byte(`
default_model = "legacy-provider/legacy-model"

[desktop]
language = "zh"
layout_style = "workbench"
theme = "light"
theme_style = "glacier"
close_behavior = "quit"
status_bar_style = "text"
status_bar_items = ["model", "cache", "balance"]
`), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(project); err != nil {
		t.Fatalf("chdir project: %v", err)
	}

	app := NewApp()
	got := app.Settings()
	if got.ConfigPath != config.UserConfigPath() {
		t.Fatalf("Settings configPath = %q, want user config %q", got.ConfigPath, config.UserConfigPath())
	}
	if got.DefaultModel != "legacy-provider/legacy-model" || got.DesktopLanguage != "zh" || got.DesktopLayoutStyle != "workbench" || got.DesktopTheme != "light" || got.DesktopThemeStyle != "glacier" || got.CloseBehavior != "quit" || got.StatusBarStyle != "icon" {
		t.Fatalf("Settings did not seed from legacy project config: %+v", got)
	}
	if want := []string{"model", "cache", "balance"}; !reflect.DeepEqual(got.StatusBarItems, want) {
		t.Fatalf("Settings did not seed status bar items from legacy project config: got %v want %v", got.StatusBarItems, want)
	}
	if _, err := os.Stat(config.UserConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("Settings() should not write user config before an edit, stat err = %v", err)
	}
	if err := app.SetDesktopLanguage("en"); err != nil {
		t.Fatalf("SetDesktopLanguage: %v", err)
	}
	userCfg := config.LoadForEdit(config.UserConfigPath())
	if userCfg.DesktopLanguage() != "en" || userCfg.DesktopLayoutStyle() != "workbench" || userCfg.DesktopTheme() != "light" || userCfg.DesktopThemeStyle() != "glacier" || userCfg.DesktopCloseBehavior() != "quit" || userCfg.DesktopStatusBarStyle() != "icon" {
		t.Fatalf("saved user config did not preserve seeded desktop prefs: lang:%q layout:%q theme:%q style:%q close:%q status:%q", userCfg.DesktopLanguage(), userCfg.DesktopLayoutStyle(), userCfg.DesktopTheme(), userCfg.DesktopThemeStyle(), userCfg.DesktopCloseBehavior(), userCfg.DesktopStatusBarStyle())
	}
	if want := []string{"model", "cache", "balance"}; !reflect.DeepEqual(userCfg.DesktopStatusBarItems(), want) {
		t.Fatalf("saved user config did not preserve seeded status bar items: got %v want %v", userCfg.DesktopStatusBarItems(), want)
	}
}

func TestSettingsSubagentDefaultsRoundTrip(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "DEEPSEEK_API_KEY", "sk-test")
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
default_model = "deepseek/deepseek-v4-flash"

[[providers]]
name = "deepseek"
kind = "openai"
base_url = "https://api.deepseek.com"
models = ["deepseek-v4-flash", "deepseek-v4-pro"]
default = "deepseek-v4-flash"
api_key_env = "DEEPSEEK_API_KEY"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	app := NewApp()
	if got := app.Settings().Agent.MaxSubagentDepth; got != agent.DefaultMaxSubagentDepth {
		t.Fatalf("default max subagent depth = %d, want %d", got, agent.DefaultMaxSubagentDepth)
	}
	if err := app.SetSubagentModel("deepseek/deepseek-v4-pro"); err != nil {
		t.Fatalf("SetSubagentModel: %v", err)
	}
	if err := app.SetSubagentEffort("max"); err != nil {
		t.Fatalf("SetSubagentEffort: %v", err)
	}
	if err := app.SetMaxSubagentDepth(1); err != nil {
		t.Fatalf("SetMaxSubagentDepth(1): %v", err)
	}
	if err := app.SetMaxSubagentDepth(2); err != nil {
		t.Fatalf("SetMaxSubagentDepth(2): %v", err)
	}

	got := app.Settings()
	if got.SubagentModel != "deepseek/deepseek-v4-pro" || got.SubagentEffort != "max" {
		t.Fatalf("subagent settings = model:%q effort:%q", got.SubagentModel, got.SubagentEffort)
	}
	if got.Agent.MaxSubagentDepth != 2 {
		t.Fatalf("max subagent depth = %d, want 2", got.Agent.MaxSubagentDepth)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if cfg.Agent.SubagentModel != "deepseek/deepseek-v4-pro" || cfg.Agent.SubagentEffort != "max" {
		t.Fatalf("saved config = model:%q effort:%q", cfg.Agent.SubagentModel, cfg.Agent.SubagentEffort)
	}
	if cfg.Agent.MaxSubagentDepth != 2 {
		t.Fatalf("saved max_subagent_depth = %d, want 2", cfg.Agent.MaxSubagentDepth)
	}
}

func TestSettingsSurfacesOfficialProviderTemplatesSeparately(t *testing.T) {
	isolateDesktopUserDirs(t)

	got := NewApp().Settings()
	providers := providerAccessSet(providerNamesFromView(got.Providers))
	official := providerAccessSet(providerNamesFromView(got.OfficialProviders))
	if providers["mimo-api"] {
		t.Fatalf("mimo-api should not be mixed into configured providers: %+v", got.Providers)
	}
	if !official["deepseek"] || official["mimo-api"] || official["mimo-token-plan"] {
		t.Fatalf("official providers = %+v, want only deepseek", got.OfficialProviders)
	}
}

func TestSettingsRepairsLegacyOfficialProviderWithoutModel(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "DEEPSEEK_API_KEY", "sk-test")
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
default_model = "deepseek-flash"

[[providers]]
name = "deepseek-flash"
kind = "openai"
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got := NewApp().Settings()
	for _, p := range got.Providers {
		if p.Name != "deepseek" {
			continue
		}
		if !p.BuiltIn {
			t.Fatalf("deepseek provider should be marked built-in for official endpoint: %+v", p)
		}
		if !p.Added || !p.KeySet || len(p.Models) != 3 || p.Models[0] != "deepseek-v4-flash" || p.Models[1] != "deepseek-v4-pro" || p.Models[2] != "deepseek-v4-flash-vision-exp" || !slices.Equal(p.VisionModels, []string{"deepseek-v4-flash-vision-exp"}) || p.Default != "deepseek-v4-flash" {
			t.Fatalf("deepseek provider = %+v, want added repaired official model list", p)
		}
		if got.DefaultModel != "deepseek/deepseek-v4-flash" {
			t.Fatalf("default_model = %q, want deepseek/deepseek-v4-flash", got.DefaultModel)
		}
		return
	}
	t.Fatalf("settings providers missing deepseek: %+v", got.Providers)
}

func TestSettingsTreatsReservedProviderNameWithExternalEndpointAsCustom(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "DEEPSEEK_API_KEY", "sk-test")
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
default_model = "deepseek/deepseek-v4-Flash"

[desktop]
provider_access = ["deepseek"]

[[providers]]
name = "deepseek"
kind = "openai"
base_url = "https://opencode.ai/zen/go/v1"
models = ["deepseek-v4-Flash", "deepseek-v4-pro", "glm-5"]
default = "deepseek-v4-Flash"
api_key_env = "DEEPSEEK_API_KEY"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got := NewApp().Settings()
	var custom *ProviderView
	for i := range got.Providers {
		if got.Providers[i].Name == "deepseek" {
			custom = &got.Providers[i]
			break
		}
	}
	if custom == nil {
		t.Fatalf("settings providers missing deepseek: %+v", got.Providers)
	}
	if custom.BuiltIn {
		t.Fatalf("external deepseek endpoint should be custom, got built-in provider: %+v", *custom)
	}
	if !custom.Added || !custom.KeySet || custom.BaseURL != "https://opencode.ai/zen/go/v1" {
		t.Fatalf("external deepseek provider = %+v, want added key-set custom opencode endpoint", *custom)
	}
	for _, p := range got.OfficialProviders {
		if p.Name == "deepseek" && p.Added {
			t.Fatalf("official DeepSeek template should not be marked added by external endpoint: %+v", p)
		}
	}
}

func TestSettingsInfersLegacyProviderAccessWhenMissing(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "DEEPSEEK_API_KEY", "sk-test")
	setDesktopTestCredential(t, "MIMO_API_KEY", "sk-test")
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
default_model = "deepseek-flash/deepseek-v4-pro"

[[providers]]
name = "deepseek-flash"
kind = "openai"
base_url = "https://api.deepseek.com"
models = ["deepseek-v4-flash", "deepseek-v4-pro"]
default = "deepseek-v4-flash"
api_key_env = "DEEPSEEK_API_KEY"

[[providers]]
name = "mimo-pro"
kind = "openai"
base_url = "https://token-plan-cn.xiaomimimo.com/v1"
model = "mimo-v2.5-pro"
api_key_env = "MIMO_API_KEY"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got := NewApp().Settings()
	providers := map[string]ProviderView{}
	for _, p := range got.Providers {
		providers[p.Name] = p
	}
	if !providers["deepseek"].Added || !providers["deepseek"].KeySet {
		t.Fatalf("deepseek provider = %+v, want inferred added key-set provider", providers["deepseek"])
	}
	if !providers["mimo-pro"].Added || !providers["mimo-pro"].KeySet || providers["mimo-pro"].BuiltIn {
		t.Fatalf("mimo-pro provider = %+v, want inferred custom key-set provider", providers["mimo-pro"])
	}
	if got.DefaultModel != "deepseek/deepseek-v4-pro" {
		t.Fatalf("default_model = %q, want deepseek/deepseek-v4-pro", got.DefaultModel)
	}
}

func TestSettingsDoesNotInferProviderAccessWhenExplicitlyEmpty(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "DEEPSEEK_API_KEY", "sk-test")
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
default_model = "deepseek-flash/deepseek-v4-flash"

[desktop]
provider_access = []

[[providers]]
name = "deepseek-flash"
kind = "openai"
base_url = "https://api.deepseek.com"
models = ["deepseek-v4-flash"]
default = "deepseek-v4-flash"
api_key_env = "DEEPSEEK_API_KEY"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got := NewApp().Settings()
	for _, p := range got.Providers {
		if p.Added {
			t.Fatalf("provider %+v should not be inferred as added when provider_access is explicit empty", p)
		}
	}
}

func TestSettingsInfersConfiguredBuiltInsWithoutConfigFile(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "DEEPSEEK_API_KEY", "sk-test")
	setDesktopTestCredential(t, "MIMO_API_KEY", "sk-test")

	got := NewApp().Settings()
	providers := map[string]ProviderView{}
	for _, p := range got.Providers {
		providers[p.Name] = p
	}
	if !providers["deepseek"].Added || !providers["deepseek"].KeySet {
		t.Fatalf("deepseek provider = %+v, want inferred added provider from configured key", providers["deepseek"])
	}
	if _, ok := providers["mimo-token-plan"]; ok {
		t.Fatalf("mimo-token-plan should not be inferred from MIMO_API_KEY alone: %+v", providers["mimo-token-plan"])
	}
}

func TestSettingsDoesNotInferBuiltInsWithoutKeys(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("MIMO_API_KEY", "")

	got := NewApp().Settings()
	for _, p := range got.Providers {
		if p.Added {
			t.Fatalf("provider %+v should not be inferred as added without a configured key", p)
		}
	}
}

func TestAddOfficialProviderAccessReplacesLegacyProviderWithoutModel(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("DEEPSEEK_API_KEY", "")
	os.Unsetenv("DEEPSEEK_API_KEY")
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
default_model = "deepseek-flash"

[[providers]]
name = "deepseek-flash"
kind = "openai"
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := NewApp().AddOfficialProviderAccess("deepseek", "test-key"); err != nil {
		t.Fatalf("AddOfficialProviderAccess: %v", err)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	p, ok := cfg.Provider("deepseek")
	if !ok {
		t.Fatal("deepseek provider not saved")
	}
	if len(p.Models) != 3 || p.Models[0] != "deepseek-v4-flash" || p.Models[1] != "deepseek-v4-pro" || p.Models[2] != "deepseek-v4-flash-vision-exp" || !slices.Equal(p.VisionModels, []string{"deepseek-v4-flash-vision-exp"}) || p.Default != "deepseek-v4-flash" {
		t.Fatalf("deepseek provider after add = %+v, want official model list", p)
	}
	if !providerAccessSet(cfg.Desktop.ProviderAccess)["deepseek"] {
		t.Fatalf("provider_access missing deepseek: %+v", cfg.Desktop.ProviderAccess)
	}
	if cfg.DefaultModel != "deepseek/deepseek-v4-flash" {
		t.Fatalf("default_model = %q, want deepseek/deepseek-v4-flash", cfg.DefaultModel)
	}
}

func TestSettingsSurfacesCuratedProviderPresets(t *testing.T) {
	isolateDesktopUserDirs(t)

	view := NewApp().Settings()
	if len(view.ProviderPresets) < 18 {
		t.Fatalf("Settings().ProviderPresets length = %d, want curated custom presets", len(view.ProviderPresets))
	}
	got := map[string]ProviderPresetView{}
	for _, preset := range view.ProviderPresets {
		got[preset.ID] = preset
	}
	for _, curated := range config.CuratedProviderPresets() {
		id := curated.ID
		preset, ok := got[id]
		if !ok {
			t.Fatalf("Settings().ProviderPresets missing %q: %+v", id, view.ProviderPresets)
		}
		if preset.KeyEnv == "" || len(preset.ProviderNames) == 0 || len(preset.Models) == 0 {
			t.Fatalf("preset %q view has missing fields: %+v", id, preset)
		}
		if preset.ID == "opencode-go-recommended" && (preset.DisplayGroup != "opencode" || preset.DisplaySection != "go" || preset.DisplayTier != "primary" || preset.RouteKind != "bundle") {
			t.Fatalf("recommended OpenCode metadata = %+v", preset)
		}
	}
}

func providerPresetViewByID(t *testing.T, view SettingsView, id string) ProviderPresetView {
	t.Helper()
	for _, preset := range view.ProviderPresets {
		if preset.ID == id {
			return preset
		}
	}
	t.Fatalf("Settings().ProviderPresets missing %q: %+v", id, view.ProviderPresets)
	return ProviderPresetView{}
}

func TestSettingsMarksPresetAddedWhenSameNameProviderExistsWithoutAccess(t *testing.T) {
	isolateDesktopUserDirs(t)
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
[desktop]
provider_access = []

[[providers]]
name = "mimo-api"
kind = "openai"
base_url = "https://custom.example/v1"
models = ["custom-model"]
default = "custom-model"
api_key_env = "MIMO_API_KEY"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	view := NewApp().Settings()
	presetView := providerPresetViewByID(t, view, "mimo-api")
	if !presetView.Added || presetView.Status != providerPresetStatusNameConflict || !reflect.DeepEqual(presetView.StatusProviderNames, []string{"mimo-api"}) {
		t.Fatalf("mimo-api preset view = %+v, want name-conflict because a different same-name provider exists", presetView)
	}

	var providerView *ProviderView
	for i := range view.Providers {
		if view.Providers[i].Name == "mimo-api" {
			providerView = &view.Providers[i]
			break
		}
	}
	if providerView == nil {
		t.Fatal("mimo-api provider view missing")
	}
	if providerView.Added {
		t.Fatalf("mimo-api provider Added = true, want false until provider_access explicitly enables it")
	}
}

func TestSettingsMarksLegacyEquivalentPresetAsInstalled(t *testing.T) {
	isolateDesktopUserDirs(t)
	preset, ok := config.CuratedProviderPreset("mimo-api")
	if !ok || len(preset.Entries) == 0 {
		t.Fatal("missing mimo-api preset")
	}
	legacy := preset.Entries[0]
	legacy.PresetID = ""
	legacy.PresetVersion = 0
	cfg := config.Default()
	if err := cfg.UpsertProvider(legacy); err != nil {
		t.Fatalf("upsert legacy provider: %v", err)
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	view := NewApp().Settings()
	presetView := providerPresetViewByID(t, view, "mimo-api")
	if !presetView.Added || presetView.Status != providerPresetStatusInstalled || !reflect.DeepEqual(presetView.StatusProviderNames, []string{"mimo-api"}) {
		t.Fatalf("mimo-api preset view = %+v, want installed for legacy equivalent config", presetView)
	}
}

func TestSettingsMarksPresetWithChangedCoreConfigAsModified(t *testing.T) {
	isolateDesktopUserDirs(t)
	preset, ok := config.CuratedProviderPreset("mimo-api")
	if !ok || len(preset.Entries) == 0 {
		t.Fatal("missing mimo-api preset")
	}
	modified := preset.Entries[0]
	modified.BaseURL = "https://custom.example/v1"
	cfg := config.Default()
	if err := cfg.UpsertProvider(modified); err != nil {
		t.Fatalf("upsert modified provider: %v", err)
	}
	cfg.Desktop.ProviderAccess = []string{"mimo-api"}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	view := NewApp().Settings()
	presetView := providerPresetViewByID(t, view, "mimo-api")
	if !presetView.Added || presetView.Status != providerPresetStatusInstalledModified || !reflect.DeepEqual(presetView.StatusProviderNames, []string{"mimo-api"}) {
		t.Fatalf("mimo-api preset view = %+v, want installed-modified for edited preset provider", presetView)
	}
}

func TestSettingsPreservesStepFunRegionalPresetBaseURLs(t *testing.T) {
	isolateDesktopUserDirs(t)

	cfg := config.Default()
	stepfun, ok := config.CuratedProviderPreset("stepfun")
	if !ok || len(stepfun.Entries) != 1 {
		t.Fatal("missing stepfun preset")
	}
	stepfunEntry := stepfun.Entries[0]
	stepfunEntry.BaseURL = "https://api.stepfun.ai/step_plan/v1"
	stepfunAnthropic, ok := config.CuratedProviderPreset("stepfun-anthropic")
	if !ok || len(stepfunAnthropic.Entries) != 1 {
		t.Fatal("missing stepfun-anthropic preset")
	}
	stepfunAnthropicEntry := stepfunAnthropic.Entries[0]
	stepfunAnthropicEntry.BaseURL = "https://api.stepfun.ai/step_plan"
	cfg.Providers = append(cfg.Providers, stepfunEntry, stepfunAnthropicEntry)
	cfg.Desktop.ProviderAccess = []string{"stepfun", "stepfun-anthropic"}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	view := NewApp().Settings()
	for _, id := range []string{"stepfun", "stepfun-anthropic"} {
		presetView := providerPresetViewByID(t, view, id)
		if !presetView.Added || presetView.Status != providerPresetStatusInstalledModified {
			t.Fatalf("%s preset view = %+v, want installed-modified for a preserved regional endpoint", id, presetView)
		}
	}

	loaded := config.LoadForEdit(config.UserConfigPath())
	stepfunEntryView, ok := loaded.Provider("stepfun")
	if !ok {
		t.Fatal("stepfun provider missing after load")
	}
	if got := stepfunEntryView.BaseURL; got != "https://api.stepfun.ai/step_plan/v1" {
		t.Fatalf("stepfun base_url = %q, want preserved regional URL", got)
	}
	stepfunAnthropicEntryView, ok := loaded.Provider("stepfun-anthropic")
	if !ok {
		t.Fatal("stepfun-anthropic provider missing after load")
	}
	if got := stepfunAnthropicEntryView.BaseURL; got != "https://api.stepfun.ai/step_plan" {
		t.Fatalf("stepfun-anthropic base_url = %q, want preserved regional URL", got)
	}
}

func TestSettingsMarksSimilarProviderPresetWithoutBlockingAdd(t *testing.T) {
	isolateDesktopUserDirs(t)
	preset, ok := config.CuratedProviderPreset("mimo-api")
	if !ok || len(preset.Entries) == 0 {
		t.Fatal("missing mimo-api preset")
	}
	similar := preset.Entries[0]
	similar.Name = "my-mimo"
	similar.PresetID = ""
	similar.PresetVersion = 0
	cfg := config.Default()
	if err := cfg.UpsertProvider(similar); err != nil {
		t.Fatalf("upsert similar provider: %v", err)
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	view := NewApp().Settings()
	presetView := providerPresetViewByID(t, view, "mimo-api")
	if presetView.Added || presetView.Status != providerPresetStatusSimilarExisting || !reflect.DeepEqual(presetView.StatusProviderNames, []string{"my-mimo"}) {
		t.Fatalf("mimo-api preset view = %+v, want non-blocking similar-existing status", presetView)
	}
}

func TestAddProviderPresetAccessSavesEditableProviderAndKey(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("MIMO_API_KEY", "")
	os.Unsetenv("MIMO_API_KEY")

	if warning, err := NewApp().AddProviderPresetAccess("mimo-api", "sk-mimo"); err != nil {
		t.Fatalf("AddProviderPresetAccess: %v", err)
	} else if warning != "" {
		t.Fatalf("AddProviderPresetAccess warning = %q, want none", warning)
	}

	cfg := config.LoadForEdit(config.UserConfigPath())
	p, ok := cfg.Provider("mimo-api")
	if !ok {
		t.Fatal("mimo-api provider not saved")
	}
	if p.Kind != "openai" || p.BaseURL != "https://api.xiaomimimo.com/v1" || p.Default != "mimo-v2.5-pro" {
		t.Fatalf("mimo-api provider after preset add = %+v", p)
	}
	if p.PresetID != "mimo-api" || p.PresetVersion != config.ProviderPresetVersion {
		t.Fatalf("mimo-api preset metadata = %q/%d, want mimo-api/%d", p.PresetID, p.PresetVersion, config.ProviderPresetVersion)
	}
	if !p.NoProxy {
		t.Fatal("mimo-api preset should save no_proxy = true")
	}
	if !p.HasVisionModel("mimo-v2.5") || p.HasVisionModel("mimo-v2.5-pro") {
		t.Fatalf("mimo vision_models = %+v, want only vision-capable MiMo models", p.VisionModels)
	}
	if price := p.PriceForModel("mimo-v2.5-pro"); price == nil || price.Currency != "¥" {
		t.Fatalf("mimo-v2.5-pro price = %+v, want RMB pricing", price)
	}
	if !providerAccessSet(cfg.Desktop.ProviderAccess)["mimo-api"] {
		t.Fatalf("provider_access missing mimo-api: %+v", cfg.Desktop.ProviderAccess)
	}
	data, err := os.ReadFile(config.UserCredentialsPath())
	if err != nil {
		t.Fatalf("read saved credentials: %v", err)
	}
	if p.APIKeyEnv == "MIMO_API_KEY" || !strings.Contains(string(data), p.APIKeyEnv+"=sk-mimo") {
		t.Fatal("saved credentials missing the isolated MiMo key reference")
	}

	view := NewApp().Settings()
	var presetView *ProviderPresetView
	var providerView *ProviderView
	for i := range view.ProviderPresets {
		if view.ProviderPresets[i].ID == "mimo-api" {
			presetView = &view.ProviderPresets[i]
		}
	}
	for i := range view.Providers {
		if view.Providers[i].Name == "mimo-api" {
			providerView = &view.Providers[i]
		}
	}
	if presetView == nil || !presetView.Added || presetView.Status != providerPresetStatusInstalled || !presetView.KeySet {
		t.Fatalf("mimo-api preset view = %+v, want installed/key-set", presetView)
	}
	if providerView == nil || providerView.BuiltIn || !providerView.Added || !providerView.KeySet {
		t.Fatalf("mimo provider view = %+v, want editable added custom provider with key", providerView)
	}
}

func TestAddProviderPresetAccessDoesNotOverwriteExistingProvider(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("MIMO_API_KEY", "")
	os.Unsetenv("MIMO_API_KEY")
	setDesktopTestCredential(t, "MIMO_API_KEY", "sk-original")

	cfg := config.Default()
	custom := config.ProviderEntry{
		Name:      "mimo-api",
		Kind:      "openai",
		BaseURL:   "https://custom.example/v1",
		Models:    []string{"custom-model"},
		Default:   "custom-model",
		APIKeyEnv: "MIMO_API_KEY",
		Headers:   map[string]string{"X-Custom": "keep-me"},
	}
	if err := cfg.UpsertProvider(custom); err != nil {
		t.Fatalf("upsert custom provider: %v", err)
	}
	cfg.Desktop.ProviderAccess = []string{"mimo-api"}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	if warning, err := NewApp().AddProviderPresetAccess("mimo-api", "sk-new"); err == nil {
		t.Fatal("AddProviderPresetAccess unexpectedly overwrote an existing provider")
	} else if !strings.Contains(err.Error(), "provider name(s) already exist") {
		t.Fatalf("AddProviderPresetAccess error = %v, want name-exists guard", err)
	} else if warning != "" {
		t.Fatalf("AddProviderPresetAccess warning = %q, want none on rejected add", warning)
	}

	cfg = config.LoadForEdit(config.UserConfigPath())
	got, ok := cfg.Provider("mimo-api")
	if !ok {
		t.Fatal("mimo-api provider missing after rejected add")
	}
	if got.BaseURL != custom.BaseURL || got.DefaultModel() != custom.DefaultModel() || !reflect.DeepEqual(got.ModelList(), custom.ModelList()) || !reflect.DeepEqual(got.Headers, custom.Headers) {
		t.Fatalf("mimo-api provider was overwritten: %+v, want custom %+v", got, custom)
	}
	data, err := os.ReadFile(config.UserCredentialsPath())
	if err != nil {
		t.Fatalf("read saved credentials: %v", err)
	}
	if strings.Contains(string(data), "sk-new") || !strings.Contains(string(data), "MIMO_API_KEY=sk-original") {
		t.Fatalf("credentials changed after rejected add:\n%s", data)
	}
}

func TestResetProviderPresetAccessOverwritesSameNameProvider(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("MIMO_API_KEY", "")
	os.Unsetenv("MIMO_API_KEY")
	setDesktopTestCredential(t, "MIMO_API_KEY", "sk-original")

	cfg := config.Default()
	custom := config.ProviderEntry{
		Name:      "mimo-api",
		Kind:      "openai",
		BaseURL:   "https://custom.example/v1",
		Models:    []string{"custom-model"},
		Default:   "custom-model",
		APIKeyEnv: "MIMO_API_KEY",
		Headers:   map[string]string{"X-Custom": "remove-me"},
	}
	if err := cfg.UpsertProvider(custom); err != nil {
		t.Fatalf("upsert custom provider: %v", err)
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	if err := NewApp().ResetProviderPresetAccess("mimo-api"); err != nil {
		t.Fatalf("ResetProviderPresetAccess: %v", err)
	}

	cfg = config.LoadForEdit(config.UserConfigPath())
	got, ok := cfg.Provider("mimo-api")
	if !ok {
		t.Fatal("mimo-api provider missing after reset")
	}
	if got.BaseURL != "https://api.xiaomimimo.com/v1" || got.DefaultModel() != "mimo-v2.5-pro" || got.PresetID != "mimo-api" || got.PresetVersion != config.ProviderPresetVersion {
		t.Fatalf("mimo-api provider after reset = %+v, want preset template", got)
	}
	if len(got.Headers) != 0 {
		t.Fatalf("mimo-api headers after reset = %+v, want preset headers", got.Headers)
	}
	if !providerAccessSet(cfg.Desktop.ProviderAccess)["mimo-api"] {
		t.Fatalf("provider_access missing mimo-api after reset: %+v", cfg.Desktop.ProviderAccess)
	}
	data, err := os.ReadFile(config.UserCredentialsPath())
	if err != nil {
		t.Fatalf("read saved credentials: %v", err)
	}
	if !strings.Contains(string(data), "MIMO_API_KEY=sk-original") {
		t.Fatalf("credentials changed after reset:\n%s", data)
	}

	presetView := providerPresetViewByID(t, NewApp().Settings(), "mimo-api")
	if !presetView.Added || presetView.Status != providerPresetStatusInstalled {
		t.Fatalf("mimo-api preset view = %+v, want installed after reset", presetView)
	}
}

func TestResetProviderPresetAccessRejectsMissingSameNameProvider(t *testing.T) {
	isolateDesktopUserDirs(t)

	if err := NewApp().ResetProviderPresetAccess("mimo-api"); err == nil {
		t.Fatal("ResetProviderPresetAccess unexpectedly reset a missing provider")
	} else if !strings.Contains(err.Error(), "no same-name provider exists") {
		t.Fatalf("ResetProviderPresetAccess error = %v, want missing same-name provider guard", err)
	}
}

func TestAddEveryProviderPresetAccessInstallsTemplate(t *testing.T) {
	for _, preset := range config.CuratedProviderPresets() {
		t.Run(preset.ID, func(t *testing.T) {
			isolateDesktopUserDirs(t)

			if warning, err := NewApp().AddProviderPresetAccess(preset.ID, "sk-test"); err != nil {
				t.Fatalf("AddProviderPresetAccess(%q): %v", preset.ID, err)
			} else if warning != "" {
				t.Fatalf("AddProviderPresetAccess(%q) warning = %q, want none", preset.ID, warning)
			}

			cfg := config.LoadForEdit(config.UserConfigPath())
			access := providerAccessSet(cfg.Desktop.ProviderAccess)
			for _, entry := range preset.Entries {
				got, ok := cfg.Provider(entry.Name)
				if !ok {
					t.Fatalf("provider %q from preset %q was not saved", entry.Name, preset.ID)
				}
				if !access[entry.Name] {
					t.Fatalf("provider_access for preset %q missing %q: %+v", preset.ID, entry.Name, cfg.Desktop.ProviderAccess)
				}
				if got.Kind != entry.Kind || got.BaseURL != entry.BaseURL || got.DefaultModel() != entry.DefaultModel() || got.APIKeyEnv == entry.APIKeyEnv || !config.CredentialStored(got.APIKeyEnv) || got.AuthHeader != entry.AuthHeader || got.NoProxy != entry.NoProxy {
					t.Fatalf("provider %q core fields = %+v, want template %+v", entry.Name, got, entry)
				}
				if got.PresetID != preset.ID || got.PresetVersion != config.ProviderPresetVersion {
					t.Fatalf("provider %q preset metadata = %q/%d, want %q/%d", entry.Name, got.PresetID, got.PresetVersion, preset.ID, config.ProviderPresetVersion)
				}
				if got.ContextWindow != entry.ContextWindow || got.Thinking != entry.Thinking || got.DefaultEffort != entry.DefaultEffort || got.ReasoningProtocol != entry.ReasoningProtocol {
					t.Fatalf("provider %q capability fields = %+v, want template %+v", entry.Name, got, entry)
				}
				if !reflect.DeepEqual(got.ModelList(), entry.ModelList()) || !reflect.DeepEqual(got.VisionModels, entry.VisionModels) || !reflect.DeepEqual(got.SupportedEfforts, entry.SupportedEfforts) {
					t.Fatalf("provider %q models/capabilities = %+v, want template %+v", entry.Name, got, entry)
				}
				if !reflect.DeepEqual(got.Headers, entry.Headers) || !reflect.DeepEqual(got.ExtraBody, entry.ExtraBody) {
					t.Fatalf("provider %q request extras = %+v, want template %+v", entry.Name, got, entry)
				}
			}

			view := NewApp().Settings()
			var presetView *ProviderPresetView
			for i := range view.ProviderPresets {
				if view.ProviderPresets[i].ID == preset.ID {
					presetView = &view.ProviderPresets[i]
					break
				}
			}
			if presetView == nil || !presetView.Added || presetView.Status != providerPresetStatusInstalled || !presetView.KeySet || !presetView.Configured {
				t.Fatalf("preset view for %q = %+v, want installed/key-set/configured", preset.ID, presetView)
			}
		})
	}
}

func TestAddOpenCodeGoRecommendedPresetCompletesMissingRoutes(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("OPENCODE_GO_API_KEY", "")
	os.Unsetenv("OPENCODE_GO_API_KEY")

	preset, ok := config.CuratedProviderPreset("opencode-go-recommended")
	if !ok || len(preset.Entries) != 3 {
		t.Fatalf("recommended preset = %+v, found=%v", preset, ok)
	}
	cfg := config.Default()
	seed := preset.Entries[0]
	seed.PresetID = "opencode-go"
	if err := cfg.UpsertProvider(seed); err != nil {
		t.Fatalf("seed existing OpenCode Go route: %v", err)
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save seed config: %v", err)
	}
	partial := providerPresetViewByID(t, NewApp().Settings(), preset.ID)
	if partial.Status != providerPresetStatusPartial || len(partial.MissingProviderNames) != 2 {
		t.Fatalf("recommended preset partial view = %+v, want two missing routes", partial)
	}

	if warning, err := NewApp().AddProviderPresetAccess(preset.ID, "sk-opencode"); err != nil {
		t.Fatalf("AddProviderPresetAccess: %v", err)
	} else if warning != "" {
		t.Fatalf("AddProviderPresetAccess warning = %q, want none", warning)
	}

	cfg = config.LoadForEdit(config.UserConfigPath())
	for _, entry := range preset.Entries {
		if _, ok := cfg.Provider(entry.Name); !ok {
			t.Fatalf("missing recommended route %q after completion", entry.Name)
		}
	}
	data, err := os.ReadFile(config.UserCredentialsPath())
	if err != nil {
		t.Fatalf("read saved credentials: %v", err)
	}
	for _, route := range preset.Entries {
		entry, _ := cfg.Provider(route.Name)
		if entry.APIKeyEnv == "OPENCODE_GO_API_KEY" || !strings.Contains(string(data), entry.APIKeyEnv+"=sk-opencode") {
			t.Fatalf("route %s did not receive an isolated credential reference", route.Name)
		}
	}
}

func TestAddOpenCodeGoRecommendedPresetSelectsUsableDefaultForFreshSetup(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("OPENCODE_GO_API_KEY", "")
	os.Unsetenv("OPENCODE_GO_API_KEY")

	cfg := config.Default()
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save fresh config: %v", err)
	}
	if _, err := NewApp().AddProviderPresetAccess("opencode-go-recommended", "sk-opencode"); err != nil {
		t.Fatalf("AddProviderPresetAccess: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	if got.DefaultModel != "opencode-go/glm-5.3" {
		t.Fatalf("default model = %q, want ready-to-use OpenCode Go default", got.DefaultModel)
	}
}

func TestAddOpenCodeGoRecommendedPresetPreservesConfiguredDefault(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("OPENCODE_GO_API_KEY", "")
	os.Unsetenv("OPENCODE_GO_API_KEY")

	cfg := config.Default()
	if err := cfg.UpsertProvider(config.ProviderEntry{
		Name:    "local-ready",
		Kind:    "openai",
		BaseURL: "http://127.0.0.1:11434/v1",
		Models:  []string{"local-model"},
		Default: "local-model",
	}); err != nil {
		t.Fatalf("upsert configured provider: %v", err)
	}
	if err := cfg.SetDefaultModel("local-ready/local-model"); err != nil {
		t.Fatalf("set configured default: %v", err)
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save configured default: %v", err)
	}

	if _, err := NewApp().AddProviderPresetAccess("opencode-go-recommended", "sk-opencode"); err != nil {
		t.Fatalf("AddProviderPresetAccess: %v", err)
	}
	if got := config.LoadForEdit(config.UserConfigPath()).DefaultModel; got != "local-ready/local-model" {
		t.Fatalf("default model = %q, want existing configured default preserved", got)
	}
}

func TestAddOpenCodeGoRecommendedPresetPreservesModifiedRoute(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("OPENCODE_GO_API_KEY", "")
	os.Unsetenv("OPENCODE_GO_API_KEY")

	preset, ok := config.CuratedProviderPreset("opencode-go-recommended")
	if !ok || len(preset.Entries) != 3 {
		t.Fatalf("recommended preset = %+v, found=%v", preset, ok)
	}
	cfg := config.Default()
	modified := preset.Entries[0]
	modified.BaseURL = "https://custom.example/v1"
	modified.PresetID = "opencode-go"
	if err := cfg.UpsertProvider(modified); err != nil {
		t.Fatalf("seed modified OpenCode Go route: %v", err)
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save modified config: %v", err)
	}

	if _, err := NewApp().AddProviderPresetAccess(preset.ID, "sk-opencode"); err != nil {
		t.Fatalf("AddProviderPresetAccess: %v", err)
	}
	cfg = config.LoadForEdit(config.UserConfigPath())
	got, ok := cfg.Provider("opencode-go")
	if !ok || got.BaseURL != "https://custom.example/v1" {
		t.Fatalf("modified route = %+v, want preserved custom endpoint", got)
	}
	for _, name := range []string{"opencode-go-anthropic", "opencode-go-responses"} {
		if _, ok := cfg.Provider(name); !ok {
			t.Fatalf("missing route %q after completing bundle around modified route", name)
		}
	}
}

func TestAddOpenCodeGoRecommendedPresetRejectsConflictAtomically(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("OPENCODE_GO_API_KEY", "")
	os.Unsetenv("OPENCODE_GO_API_KEY")

	cfg := config.Default()
	conflict := config.ProviderEntry{
		Name:          "opencode-go",
		Kind:          "openai",
		BaseURL:       "https://custom.example/v1",
		Models:        []string{"custom-model"},
		Default:       "custom-model",
		APIKeyEnv:     "OPENCODE_GO_API_KEY",
		PresetID:      "custom",
		PresetVersion: 1,
	}
	if err := cfg.UpsertProvider(conflict); err != nil {
		t.Fatalf("upsert conflicting provider: %v", err)
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save conflict config: %v", err)
	}

	if warning, err := NewApp().AddProviderPresetAccess("opencode-go-recommended", "sk-should-not-save"); err == nil {
		t.Fatal("AddProviderPresetAccess unexpectedly accepted same-name conflict")
	} else if !strings.Contains(err.Error(), "opencode-go") {
		t.Fatalf("AddProviderPresetAccess error = %v, want opencode-go conflict", err)
	} else if warning != "" {
		t.Fatalf("AddProviderPresetAccess warning = %q, want none", warning)
	}

	cfg = config.LoadForEdit(config.UserConfigPath())
	if _, ok := cfg.Provider("opencode-go-anthropic"); ok {
		t.Fatal("conflicting bundle partially installed Anthropic route")
	}
	if _, err := os.Stat(config.UserCredentialsPath()); err == nil {
		data, readErr := os.ReadFile(config.UserCredentialsPath())
		if readErr != nil {
			t.Fatalf("read credentials: %v", readErr)
		}
		if strings.Contains(string(data), "sk-should-not-save") {
			t.Fatalf("conflicting bundle saved credentials: %s", data)
		}
	}
}

func TestAddOfficialProviderAccessPreservesBackgroundJobsWhenSavingKey(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("DEEPSEEK_API_KEY", "")
	os.Unsetenv("DEEPSEEK_API_KEY")

	app := NewApp()
	app.readyHook = func() {}
	app.setTestCtrl(newBackgroundJobController(t, "provider-access-job"), "deepseek-flash/deepseek-v4-flash")

	_, err := app.AddOfficialProviderAccess("deepseek", "sk-test")
	if err != nil || !controllerHasActiveRuntimeWork(app.activeCtrl()) {
		t.Fatalf("AddOfficialProviderAccess interrupted background work: %v", err)
	}
	p, _ := config.LoadForEdit(config.UserConfigPath()).Provider("deepseek")
	if !config.CredentialStored(p.APIKeyEnv) || p.APIKeyEnv == "DEEPSEEK_API_KEY" {
		t.Fatal("official key was not committed to a new reference")
	}
}

func TestSetProviderKeyRestoresOfficialProviderAccess(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("DEEPSEEK_API_KEY", "")
	os.Unsetenv("DEEPSEEK_API_KEY")
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
default_model = "deepseek/deepseek-v4-flash"

[desktop]
provider_access = []

[[providers]]
name = "deepseek"
kind = "openai"
base_url = "https://api.deepseek.com"
models = ["deepseek-v4-flash", "deepseek-v4-pro"]
default = "deepseek-v4-flash"
api_key_env = "DEEPSEEK_API_KEY"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := NewApp().SetProviderKey("DEEPSEEK_API_KEY", "sk-test"); err != nil {
		t.Fatalf("SetProviderKey: %v", err)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if !providerAccessSet(cfg.Desktop.ProviderAccess)["deepseek"] {
		t.Fatalf("provider_access = %+v, want deepseek restored", cfg.Desktop.ProviderAccess)
	}
	got := NewApp().Settings()
	for _, p := range got.Providers {
		if p.Name == "deepseek" {
			if !p.Added || !p.KeySet {
				t.Fatalf("deepseek settings = %+v, want added and key-set", p)
			}
			return
		}
	}
	t.Fatalf("settings providers missing deepseek: %+v", got.Providers)
}

func TestSetProviderKeyKeepsCustomAliasProviderAccess(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("PROXY_DEEPSEEK_KEY", "")
	os.Unsetenv("PROXY_DEEPSEEK_KEY")
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
[desktop]
provider_access = []

[[providers]]
name = "deepseek-flash"
kind = "openai"
base_url = "https://proxy.example/v1"
model = "deepseek-v4-flash"
api_key_env = "PROXY_DEEPSEEK_KEY"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := NewApp().SetProviderKey("PROXY_DEEPSEEK_KEY", "sk-test"); err != nil {
		t.Fatalf("SetProviderKey: %v", err)
	}
	cfg := config.LoadForEditWithoutCredentials(config.UserConfigPath())
	access := providerAccessSet(cfg.Desktop.ProviderAccess)
	if !access["deepseek-flash"] {
		t.Fatalf("provider_access = %+v, want custom alias deepseek-flash", cfg.Desktop.ProviderAccess)
	}
	if access["deepseek"] {
		t.Fatalf("provider_access = %+v, should not canonicalize custom proxy to deepseek", cfg.Desktop.ProviderAccess)
	}
}

func TestSetProviderKeyLeaseHeldKeepsCurrentController(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old"}
	cfg.Providers = []config.ProviderEntry{
		{Name: "old", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "old-model", APIKeyEnv: "OLD_MODEL_KEY"},
		{Name: "longcat", Kind: "openai", BaseURL: "https://longcat.example/v1", Model: "longcat-chat", APIKeyEnv: "LONGCAT_API_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(dir, "externally-leased-provider-key.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0o644); err != nil {
		t.Fatalf("write placeholder session: %v", err)
	}
	externalLease, err := agent.TryAcquireSessionLease(sessionPath)
	if err != nil {
		t.Fatalf("TryAcquireSessionLease: %v", err)
	}
	defer externalLease.Release()

	oldSession := agent.NewSession("old system prompt")
	oldSession.Add(provider.Message{Role: provider.RoleUser, Content: "hello"})
	oldExec := agent.New(nil, nil, oldSession, agent.Options{}, event.Discard)
	oldCtrl := control.New(control.Options{Executor: oldExec, SessionDir: dir, SessionPath: sessionPath, Label: "old", Sink: event.Discard})
	defer oldCtrl.Close()

	app := NewApp()
	app.ctx = context.Background()
	tab := &WorkspaceTab{
		ID:          "tab_provider",
		Scope:       "global",
		SessionPath: sessionPath,
		Ready:       true,
		model:       "old/old-model",
		Ctrl:        oldCtrl,
		sink:        &tabEventSink{tabID: "tab_provider", app: app},
		disabledMCP: map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	warning, err := app.SetProviderKey("LONGCAT_API_KEY", "sk-longcat")
	if err != nil {
		t.Fatalf("SetProviderKey: %v", err)
	}
	if warning != "" {
		t.Fatalf("SetProviderKey warning = %q; saving does not acquire the session lease", warning)
	}
	if strings.Contains(warning, sessionPath) || strings.Contains(warning, "held by") {
		t.Fatalf("SetProviderKey surfaced raw lease details: %v", warning)
	}
	if tab.Ctrl != oldCtrl {
		t.Fatalf("tab controller changed after failed provider-key rebuild")
	}
	if tab.StartupErr != "" {
		t.Fatalf("tab startup error = %q, want unchanged current session", tab.StartupErr)
	}
	if got := tab.Ctrl.History(); len(got) < 2 || got[1].Content != "hello" {
		t.Fatalf("history after failed provider-key rebuild = %+v", got)
	}
	if access := providerAccessSet(config.LoadForEditWithoutCredentials(config.UserConfigPath()).Desktop.ProviderAccess); !access["longcat"] {
		t.Fatalf("provider_access should still persist longcat after key save")
	}
}

func TestSetProviderKeyPreservesInFlightStartupBuild(t *testing.T) {
	isolateDesktopUserDirs(t)

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old"}
	cfg.Providers = []config.ProviderEntry{{
		Name:      "old",
		Kind:      "openai",
		BaseURL:   "https://example.invalid/v1",
		Model:     "old-model",
		APIKeyEnv: "OLD_MODEL_KEY",
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(dir, "startup-build-in-flight.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0o644); err != nil {
		t.Fatalf("write placeholder session: %v", err)
	}

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	// Model the async startup build still being in flight: no controller yet,
	// a live build generation, and a cancellable build context.
	buildCtx, buildCancel := context.WithCancel(context.Background())
	const startupGeneration = 1
	tab := &WorkspaceTab{
		ID:              "tab_key_rebuild",
		Scope:           "global",
		SessionPath:     sessionPath,
		model:           "old/old-model",
		buildGeneration: startupGeneration,
		buildCancel:     buildCancel,
		disabledMCP:     map[string]ServerView{},
	}
	tab.sink = &tabEventSink{tabID: tab.ID, app: app}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	t.Cleanup(tab.releaseSessionLease)

	if _, err := app.SetProviderKey("OLD_MODEL_KEY", "sk-new"); err != nil {
		t.Fatalf("SetProviderKey: %v", err)
	}
	if tab.Ctrl != nil || tab.buildGeneration != startupGeneration || buildCtx.Err() != nil {
		t.Fatal("saving a key published or cancelled an in-flight startup; publication owns its version check")
	}
	buildCancel()
}

func TestSaveProviderWithKeyLeaseHeldPersistsCustomProvider(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old"}
	cfg.Providers = []config.ProviderEntry{{
		Name:      "old",
		Kind:      "openai",
		BaseURL:   "https://example.invalid/v1",
		Model:     "old-model",
		APIKeyEnv: "OLD_MODEL_KEY",
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(dir, "externally-leased-custom-provider.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0o644); err != nil {
		t.Fatalf("write placeholder session: %v", err)
	}
	externalLease, err := agent.TryAcquireSessionLease(sessionPath)
	if err != nil {
		t.Fatalf("TryAcquireSessionLease: %v", err)
	}
	defer externalLease.Release()

	oldSession := agent.NewSession("old system prompt")
	oldSession.Add(provider.Message{Role: provider.RoleUser, Content: "hello"})
	oldExec := agent.New(nil, nil, oldSession, agent.Options{}, event.Discard)
	oldCtrl := control.New(control.Options{Executor: oldExec, SessionDir: dir, SessionPath: sessionPath, Label: "old", Sink: event.Discard})
	defer oldCtrl.Close()

	app := NewApp()
	app.ctx = context.Background()
	tab := &WorkspaceTab{
		ID:          "tab_custom_provider",
		Scope:       "global",
		SessionPath: sessionPath,
		Ready:       true,
		model:       "old/old-model",
		Ctrl:        oldCtrl,
		sink:        &tabEventSink{tabID: "tab_custom_provider", app: app},
		disabledMCP: map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	warning, err := app.SaveProviderWithKey(ProviderView{
		Name:      "proxy",
		Kind:      "openai",
		BaseURL:   "https://proxy.example/v1",
		Models:    []string{"model-a", "model-b"},
		Default:   "model-a",
		APIKeyEnv: "PROXY_API_KEY",
	}, "sk-proxy")
	if err != nil {
		t.Fatalf("SaveProviderWithKey: %v", err)
	}
	if warning != "" {
		t.Fatalf("SaveProviderWithKey warning = %q; saving does not acquire the session lease", warning)
	}
	if strings.Contains(warning, sessionPath) || strings.Contains(warning, "held by") {
		t.Fatalf("SaveProviderWithKey surfaced raw lease details: %v", warning)
	}
	if tab.Ctrl != oldCtrl {
		t.Fatalf("tab controller changed after failed provider rebuild")
	}
	gotCfg := config.LoadForEditWithoutCredentials(config.UserConfigPath())
	got, ok := gotCfg.Provider("proxy")
	if !ok {
		t.Fatal("custom provider was not saved")
	}
	if want := []string{"model-a", "model-b"}; !reflect.DeepEqual(got.ModelList(), want) {
		t.Fatalf("custom provider models = %v, want %v", got.ModelList(), want)
	}
	if !providerAccessSet(gotCfg.Desktop.ProviderAccess)["proxy"] {
		t.Fatalf("provider_access = %+v, want proxy", gotCfg.Desktop.ProviderAccess)
	}
	data, err := os.ReadFile(config.UserCredentialsPath())
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	if got.APIKeyEnv == "PROXY_API_KEY" || !strings.Contains(string(data), got.APIKeyEnv+"=sk-proxy") {
		t.Fatal("provider key was not saved with an isolated reference")
	}
}

func TestConfigChangeLeaseHeldPersistsAndDefersRefresh(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old"}
	cfg.Providers = []config.ProviderEntry{{
		Name:      "old",
		Kind:      "openai",
		BaseURL:   "https://example.invalid/v1",
		Model:     "old-model",
		APIKeyEnv: "OLD_MODEL_KEY",
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(dir, "externally-leased-settings.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0o644); err != nil {
		t.Fatalf("write placeholder session: %v", err)
	}
	externalLease, err := agent.TryAcquireSessionLease(sessionPath)
	if err != nil {
		t.Fatalf("TryAcquireSessionLease: %v", err)
	}
	defer externalLease.Release()

	oldExec := agent.New(nil, nil, agent.NewSession("old system prompt"), agent.Options{}, event.Discard)
	oldCtrl := control.New(control.Options{Executor: oldExec, SessionDir: dir, SessionPath: sessionPath, Label: "old", Sink: event.Discard})
	defer oldCtrl.Close()

	app := NewApp()
	app.ctx = context.Background()
	tab := &WorkspaceTab{
		ID:          "tab_settings",
		Scope:       "global",
		SessionPath: sessionPath,
		Ready:       true,
		model:       "old/old-model",
		Ctrl:        oldCtrl,
		sink:        &tabEventSink{tabID: "tab_settings", app: app},
		disabledMCP: map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	if err := app.SetMaxSubagentDepth(1); err != nil {
		t.Fatalf("SetMaxSubagentDepth should defer lease-held refresh instead of failing: %v", err)
	}
	if tab.Ctrl != oldCtrl {
		t.Fatalf("tab controller changed after deferred settings rebuild")
	}
	got := config.LoadForEditWithoutCredentials(config.UserConfigPath())
	if got.Agent.MaxSubagentDepth != 1 {
		t.Fatalf("saved max_subagent_depth = %d, want 1", got.Agent.MaxSubagentDepth)
	}
}

func TestDeferredRebuildRetryAppliesAfterLeaseRelease(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old"}
	cfg.Providers = []config.ProviderEntry{{
		Name:      "old",
		Kind:      "openai",
		BaseURL:   "https://example.invalid/v1",
		Model:     "old-model",
		APIKeyEnv: "OLD_MODEL_KEY",
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(dir, "deferred-rebuild-retry.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0o644); err != nil {
		t.Fatalf("write placeholder session: %v", err)
	}
	externalLease, err := agent.TryAcquireSessionLease(sessionPath)
	if err != nil {
		t.Fatalf("TryAcquireSessionLease: %v", err)
	}
	released := false
	defer func() {
		if !released {
			externalLease.Release()
		}
	}()

	oldExec := agent.New(nil, nil, agent.NewSession("old system prompt"), agent.Options{}, event.Discard)
	oldCtrl := control.New(control.Options{Executor: oldExec, SessionDir: dir, SessionPath: sessionPath, Label: "old", Sink: event.Discard})
	defer oldCtrl.Close()

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	tab := &WorkspaceTab{
		ID:          "tab_deferred_retry",
		Scope:       "global",
		SessionPath: sessionPath,
		Ready:       true,
		model:       "old/old-model",
		Ctrl:        oldCtrl,
		sink:        &tabEventSink{tabID: "tab_deferred_retry", app: app},
		disabledMCP: map[string]ServerView{},
	}
	installNoopRuntimeEvents(app, tab.sink)
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	t.Cleanup(func() {
		if c := app.controllerForTab(tab); c != nil && c != oldCtrl {
			c.Close()
		}
		tab.releaseSessionLease()
	})

	if err := app.SetAgentParams(0.2, 0, 0, "updated prompt"); err != nil {
		t.Fatalf("SetAgentParams: %v", err)
	}
	if !app.deferredRebuildPending(tab.ID) {
		t.Fatal("deferred rebuild was not scheduled while the lease is held")
	}
	if app.controllerForTab(tab) != oldCtrl {
		t.Fatal("controller changed while the lease is still held")
	}

	externalLease.Release()
	released = true

	app.deferredRebuildTick(false)
	if app.deferredRebuildPending(tab.ID) {
		t.Fatal("deferred rebuild is still pending after the lease was released")
	}
	if c := app.controllerForTab(tab); c == nil || c == oldCtrl {
		t.Fatalf("controller was not rebuilt after the lease release: got %p", c)
	}
}

func TestDeferredRebuildScheduleAfterStopIsNoop(t *testing.T) {
	app := NewApp()
	app.stopDeferredRebuildRetry()
	app.scheduleDeferredRebuild("tab_x", "settings")
	if app.deferredRebuildPending("tab_x") {
		t.Fatal("schedule after stop should not register pending work")
	}
}

func TestDeferredRebuildAppliesToItsInactiveTarget(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old"}
	cfg.Providers = []config.ProviderEntry{{
		Name:      "old",
		Kind:      "openai",
		BaseURL:   "https://example.invalid/v1",
		Model:     "old-model",
		APIKeyEnv: "OLD_MODEL_KEY",
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(dir, "deferred-rebuild-inactive.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0o644); err != nil {
		t.Fatalf("write placeholder session: %v", err)
	}
	externalLease, err := agent.TryAcquireSessionLease(sessionPath)
	if err != nil {
		t.Fatalf("TryAcquireSessionLease: %v", err)
	}
	released := false
	defer func() {
		if !released {
			externalLease.Release()
		}
	}()

	oldExec := agent.New(nil, nil, agent.NewSession("old system prompt"), agent.Options{}, event.Discard)
	oldCtrl := control.New(control.Options{Executor: oldExec, SessionDir: dir, SessionPath: sessionPath, Label: "old", Sink: event.Discard})
	defer oldCtrl.Close()

	otherCtrl := control.New(control.Options{Label: "other"})
	defer otherCtrl.Close()

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	tab := &WorkspaceTab{
		ID:          "tab_pending",
		Scope:       "global",
		SessionPath: sessionPath,
		Ready:       true,
		model:       "old/old-model",
		Ctrl:        oldCtrl,
		sink:        &tabEventSink{tabID: "tab_pending", app: app},
		disabledMCP: map[string]ServerView{},
	}
	installNoopRuntimeEvents(app, tab.sink)
	other := &WorkspaceTab{
		ID:          "tab_other",
		Scope:       "global",
		Ready:       true,
		model:       "old/old-model",
		Ctrl:        otherCtrl,
		sink:        &tabEventSink{tabID: "tab_other", app: app},
		disabledMCP: map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab, other.ID: other}
	app.tabOrder = []string{tab.ID, other.ID}
	app.activeTabID = tab.ID
	t.Cleanup(func() {
		if c := app.controllerForTab(tab); c != nil && c != oldCtrl {
			c.Close()
		}
		tab.releaseSessionLease()
	})

	if err := app.SetAgentParams(0.2, 0, 0, "updated prompt"); err != nil {
		t.Fatalf("SetAgentParams: %v", err)
	}
	if !app.deferredRebuildPending(tab.ID) {
		t.Fatal("deferred rebuild was not scheduled while the lease is held")
	}

	// A concrete target retains its rebuild ownership when focus moves.
	app.mu.Lock()
	app.activeTabID = other.ID
	app.mu.Unlock()
	externalLease.Release()
	released = true
	app.deferredRebuildTick(false)
	if app.deferredRebuildPending(tab.ID) || app.controllerForTab(tab) == oldCtrl {
		t.Fatal("inactive target was not rebuilt")
	}
	if app.controllerForTab(other) != otherCtrl {
		t.Fatal("retry rebuilt the focused sibling")
	}
}

func TestSetEffortForTabLeaseHeldKeepsOldControllerAlive(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old"}
	cfg.Providers = []config.ProviderEntry{{
		Name:             "old",
		Kind:             "openai",
		BaseURL:          "https://example.invalid/v1",
		Model:            "old-model",
		APIKeyEnv:        "OLD_MODEL_KEY",
		SupportedEfforts: []string{"low", "max"},
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(dir, "externally-leased-effort-switch.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0o644); err != nil {
		t.Fatalf("write placeholder session: %v", err)
	}
	externalLease, err := agent.TryAcquireSessionLease(sessionPath)
	if err != nil {
		t.Fatalf("TryAcquireSessionLease: %v", err)
	}
	released := false
	defer func() {
		if !released {
			externalLease.Release()
		}
	}()

	oldExec := agent.New(nil, nil, agent.NewSession("old system prompt"), agent.Options{}, event.Discard)
	oldCtrl := control.New(control.Options{Executor: oldExec, SessionDir: dir, SessionPath: sessionPath, Label: "old", Sink: event.Discard})
	defer oldCtrl.Close()

	app := NewApp()
	app.ctx = context.Background()
	tab := &WorkspaceTab{
		ID:          "tab_effort",
		Scope:       "global",
		SessionPath: sessionPath,
		Ready:       true,
		model:       "old/old-model",
		Ctrl:        oldCtrl,
		sink:        &tabEventSink{tabID: "tab_effort", app: app},
		disabledMCP: map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	t.Cleanup(func() {
		if c := app.controllerForTab(tab); c != nil && c != oldCtrl {
			c.Close()
		}
		tab.releaseSessionLease()
	})

	err = app.SetEffortForTab(tab.ID, "max")
	if !errors.Is(err, agent.ErrSessionLeaseHeld) {
		t.Fatalf("SetEffortForTab err = %v, want ErrSessionLeaseHeld", err)
	}
	if strings.Contains(err.Error(), sessionPath) || strings.Contains(err.Error(), "held by") {
		t.Fatalf("SetEffortForTab surfaced raw lease details: %v", err)
	}
	if tab.Ctrl != oldCtrl {
		t.Fatal("tab controller changed after failed effort switch")
	}

	// The failed switch must leave the old runtime alive: after the other
	// window releases the lease, retrying from the same tab has to succeed.
	// (The old code closed the old controller before acquiring the lease, so
	// this retry died on a snapshot of a closed session.)
	externalLease.Release()
	released = true
	if err := app.SetEffortForTab(tab.ID, "max"); err != nil {
		t.Fatalf("SetEffortForTab retry after lease release: %v", err)
	}
	if tab.Ctrl == oldCtrl {
		t.Fatal("retry did not rebuild the controller")
	}
}

func TestSetEffortForTabReanchorsDepthCapRecoveryBranch(t *testing.T) {
	isolateDesktopUserDirsSchemaOne(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old"}
	cfg.Providers = []config.ProviderEntry{{
		Name:             "old",
		Kind:             "openai",
		BaseURL:          "https://example.invalid/v1",
		Model:            "old-model",
		APIKeyEnv:        "OLD_MODEL_KEY",
		SupportedEfforts: []string{"low", "max"},
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	recoveryPath := filepath.Join(dir, "effort-switch-conflict-recovery-deadbeef.jsonl")
	disk := agent.NewSession("old system prompt")
	disk.Add(provider.Message{Role: provider.RoleUser, Content: "first"})
	disk.Add(provider.Message{Role: provider.RoleAssistant, Content: "one"})
	disk.Add(provider.Message{Role: provider.RoleUser, Content: "disk second"})
	if err := disk.Save(recoveryPath); err != nil {
		t.Fatalf("save recovery branch: %v", err)
	}
	meta, ok, err := agent.LoadBranchMeta(recoveryPath)
	if err != nil || !ok {
		t.Fatalf("LoadBranchMeta ok=%v err=%v", ok, err)
	}
	meta.Recovered = true
	meta.ParentID = "effort-switch-conflict"
	meta.RecoveryReason = "snapshot conflict"
	meta.RecoveryDepth = agent.SessionRecoveryMaxDepth
	if err := agent.SaveBranchMeta(recoveryPath, meta); err != nil {
		t.Fatalf("SaveBranchMeta: %v", err)
	}

	stale := agent.NewSession("old system prompt")
	stale.Add(provider.Message{Role: provider.RoleUser, Content: "first"})
	stale.Add(provider.Message{Role: provider.RoleAssistant, Content: "one"})
	stale.Add(provider.Message{Role: provider.RoleUser, Content: "local second"})
	oldExec := agent.New(nil, nil, stale, agent.Options{}, event.Discard)

	app := NewApp()
	app.ctx = context.Background()
	app.runtimeEvents.emit = func(context.Context, string, ...any) {}
	tab := &WorkspaceTab{
		ID:          "tab_depth_cap_effort",
		Scope:       "global",
		SessionPath: recoveryPath,
		Ready:       true,
		model:       "old/old-model",
		disabledMCP: map[string]ServerView{},
	}
	tab.sink = &tabEventSink{tabID: tab.ID, app: app}
	oldCtrl := control.New(control.Options{
		Executor:            oldExec,
		SessionDir:          dir,
		SessionPath:         recoveryPath,
		Label:               "old",
		Sink:                tab.sink,
		SessionRecoveryMeta: app.tabSessionRecoveryMeta(tab),
		OnSessionRecovered:  app.handleTabSessionRecovered(tab),
	})
	tab.Ctrl = oldCtrl
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	t.Cleanup(func() {
		if tab.Ctrl != nil {
			tab.Ctrl.Close()
		}
		tab.releaseSessionLease()
	})
	stale.IncrementRewrite()

	if err := app.SetEffortForTab(tab.ID, "max"); err != nil {
		t.Fatalf("SetEffortForTab: %v", err)
	}
	isolatedPath := tab.Ctrl.SessionPath()
	if isolatedPath == recoveryPath || !strings.Contains(isolatedPath, "-recovery-") {
		t.Fatalf("session path after effort switch = %q, want an isolated recovery branch", isolatedPath)
	}
	if got := tab.currentSessionPath(); got != isolatedPath {
		t.Fatalf("tab current session path = %q, want %q", got, isolatedPath)
	}
	if tab.sessionLease == nil || sessionRuntimeKey(tab.sessionLease.Path()) != sessionRuntimeKey(isolatedPath) {
		t.Fatalf("tab lease path = %q, want %q", tab.sessionLeaseRuntimeKey(), isolatedPath)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*-recovery-*.jsonl"))
	if err != nil {
		t.Fatalf("glob recovery branches: %v", err)
	}
	matches = primarySessionFiles(matches)
	if len(matches) != 2 || !slices.Contains(matches, recoveryPath) || !slices.Contains(matches, isolatedPath) {
		t.Fatalf("recovery branches after effort switch = %v, want canonical and isolated paths", matches)
	}

	lines := readConflictLogLines(t, store.SessionConflictLog(recoveryPath))
	if len(lines) != 1 {
		t.Fatalf("conflict log lines = %v, want one recovery diagnostic", lines)
	}
	if !strings.Contains(lines[0], `"outcome":"forked_recovery_branch"`) {
		t.Fatalf("conflict diagnostic = %s, want stable recovery fork", lines[0])
	}
	if strings.Contains(lines[0], dir) || strings.Contains(lines[0], recoveryPath) {
		t.Fatalf("conflict diagnostic leaked local path: %s", lines[0])
	}

	if err := tab.Ctrl.Snapshot(); err != nil {
		t.Fatalf("Snapshot after effort switch recovery: %v", err)
	}
	afterLines := readConflictLogLines(t, store.SessionConflictLog(recoveryPath))
	if len(afterLines) != len(lines) {
		t.Fatalf("follow-up snapshot appended conflict diagnostics: before=%v after=%v", lines, afterLines)
	}
	matches, err = filepath.Glob(filepath.Join(dir, "*-recovery-*.jsonl"))
	if err != nil {
		t.Fatalf("glob recovery branches after snapshot: %v", err)
	}
	matches = primarySessionFiles(matches)
	if len(matches) != 2 || !slices.Contains(matches, recoveryPath) || !slices.Contains(matches, isolatedPath) {
		t.Fatalf("recovery branches after follow-up snapshot = %v, want canonical and isolated paths", matches)
	}
}

func TestSetEffortForTabSameLevelShortCircuit(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old"}
	cfg.Providers = []config.ProviderEntry{{
		Name:             "old",
		Kind:             "openai",
		BaseURL:          "https://example.invalid/v1",
		Model:            "old-model",
		APIKeyEnv:        "OLD_MODEL_KEY",
		SupportedEfforts: []string{"low", "max"},
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	exec := agent.New(nil, nil, agent.NewSession("old system prompt"), agent.Options{}, event.Discard)

	app := NewApp()
	app.ctx = context.Background()
	app.runtimeEvents.emit = func(context.Context, string, ...any) {}
	tab := &WorkspaceTab{
		ID:          "tab_effort_short_circuit",
		Scope:       "global",
		Ready:       true,
		model:       "old/old-model",
		disabledMCP: map[string]ServerView{},
	}
	tab.sink = &tabEventSink{tabID: tab.ID, app: app}
	tab.Ctrl = control.New(control.Options{
		Executor:    exec,
		SessionDir:  dir,
		SessionPath: filepath.Join(dir, "effort-short-circuit.jsonl"),
		Label:       "old",
		Sink:        tab.sink,
	})
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	t.Cleanup(func() {
		tab.Ctrl.Close()
		tab.releaseSessionLease()
	})

	if err := app.SetEffortForTab(tab.ID, "max"); err != nil {
		t.Fatalf("SetEffortForTab max: %v", err)
	}
	if tab.effort == nil || *tab.effort != "max" {
		t.Fatalf("tab effort after switch = %v, want max", tab.effort)
	}
	// Let any post-rebuild async settle so the short-circuit assertion below
	// compares against a quiescent tab.
	time.Sleep(500 * time.Millisecond)
	rebuilt := tab.Ctrl
	t.Logf("before-second: ctrl=%p rebuilt=%p", tab.Ctrl, rebuilt)

	// Same level again: the short circuit must keep the running controller
	// instead of paying for a full runtime rebuild.
	if err := app.SetEffortForTab(tab.ID, "max"); err != nil {
		t.Fatalf("SetEffortForTab same level: %v", err)
	}
	t.Logf("after-second: ctrl=%p rebuilt=%p tab.effort=%v", tab.Ctrl, rebuilt, tab.effort)
	if tab.Ctrl != rebuilt {
		t.Fatal("same-level effort switch rebuilt the controller")
	}

	// A genuinely different depth still rebuilds.
	if err := app.SetEffortForTab(tab.ID, "low"); err != nil {
		t.Fatalf("SetEffortForTab low: %v", err)
	}
	if tab.Ctrl == rebuilt {
		t.Fatal("different-level effort switch kept the controller")
	}
}

func TestRemoveBuiltInProviderAccessRetargetsDefaultToRemainingAccess(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "MIMO_API_KEY", "sk-test")
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
default_model = "deepseek-flash/deepseek-v4-pro"

[desktop]
provider_access = ["deepseek-flash", "mimo-pro"]

[[providers]]
name = "deepseek-flash"
kind = "openai"
base_url = "https://api.deepseek.com"
models = ["deepseek-v4-flash", "deepseek-v4-pro"]
default = "deepseek-v4-flash"
api_key_env = "DEEPSEEK_API_KEY"

[[providers]]
name = "mimo-pro"
kind = "openai"
base_url = "https://token-plan-cn.xiaomimimo.com/v1"
model = "mimo-v2.5-pro"
api_key_env = "MIMO_API_KEY"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := NewApp().RemoveProviderAccess("deepseek"); err != nil {
		t.Fatalf("RemoveProviderAccess: %v", err)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	access := providerAccessSet(cfg.Desktop.ProviderAccess)
	if access["deepseek"] || !access["mimo-pro"] {
		t.Fatalf("provider_access = %+v, want only mimo-pro", cfg.Desktop.ProviderAccess)
	}
	if cfg.DefaultModel != "mimo-pro/mimo-v2.5-pro" {
		t.Fatalf("default_model = %q, want mimo-pro/mimo-v2.5-pro", cfg.DefaultModel)
	}
}

func TestModelsForTabOnlyListsProviderAccessWhenConfigured(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "DEEPSEEK_API_KEY", "sk-test")
	setDesktopTestCredential(t, "MIMO_API_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "deepseek/deepseek-v4-flash"
	cfg.Desktop.ProviderAccess = []string{"deepseek", "mimo-pro"}
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name: "deepseek", Kind: "anthropic", BaseURL: "https://api.deepseek.com/anthropic",
		Models: []string{"deepseek-v4-flash", "deepseek-v4-pro"}, Default: "deepseek-v4-flash", APIKeyEnv: "DEEPSEEK_API_KEY",
	})
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	models := NewApp().Models()
	refs := modelRefsFromView(models)
	for _, want := range []string{
		"deepseek/deepseek-v4-flash",
		"deepseek/deepseek-v4-pro",
		"mimo-pro/mimo-v2.5-pro",
		"mimo-pro/mimo-v2.5",
	} {
		if !refs[want] {
			t.Fatalf("Models() refs = %+v, missing %s", models, want)
		}
	}
	for _, hidden := range []string{
		"deepseek-pro/deepseek-v4-pro",
		"mimo-flash/mimo-v2.5",
	} {
		if refs[hidden] {
			t.Fatalf("Models() refs = %+v, should not include hidden provider %s", models, hidden)
		}
	}
	if len(models) != 5 {
		t.Fatalf("Models() len = %d, want 5: %+v", len(models), models)
	}
}

func TestModelsForTabListsNothingWhenProviderAccessExplicitlyEmpty(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "DEEPSEEK_API_KEY", "sk-test")

	cfg := config.Default()
	cfg.Desktop.ProviderAccess = []string{}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	if models := NewApp().Models(); len(models) != 0 {
		t.Fatalf("Models() = %+v, want no models when provider access is explicitly empty", models)
	}
}

func TestModelsForTabListsCustomMultiModelProviderWithoutMetadata(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "LOCAL_API_KEY", "sk-test")
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
default_model = "local/model-a"

[desktop]
provider_access = ["local"]

[[providers]]
name = "local"
kind = "openai"
base_url = "http://127.0.0.1:23333/v1"
models = ["model-a", "model-b"]
default = "model-a"
api_key_env = "LOCAL_API_KEY"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	models := NewApp().Models()
	refs := modelRefsFromView(models)
	for _, want := range []string{"local/model-a", "local/model-b"} {
		if !refs[want] {
			t.Fatalf("Models() refs = %+v, missing %s", models, want)
		}
	}
	if len(models) != 2 {
		t.Fatalf("Models() len = %d, want 2: %+v", len(models), models)
	}
}

func TestModelsForTabListsKeylessCustomMultiModelProvider(t *testing.T) {
	isolateDesktopUserDirs(t)
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
default_model = "local/model-a"

[desktop]
provider_access = ["local"]

[[providers]]
name = "local"
kind = "openai"
base_url = "http://127.0.0.1:23333/v1"
models = ["model-a", "model-b"]
default = "model-a"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	models := NewApp().Models()
	refs := modelRefsFromView(models)
	for _, want := range []string{"local/model-a", "local/model-b"} {
		if !refs[want] {
			t.Fatalf("Models() refs = %+v, missing %s", models, want)
		}
	}
}

func TestModelsForTabListsLoopbackCustomProviderWithMissingKeyEnv(t *testing.T) {
	isolateDesktopUserDirs(t)
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
default_model = "local/model-a"

[desktop]
provider_access = ["local"]

[[providers]]
name = "local"
kind = "openai"
base_url = "http://127.0.0.1:23333/v1"
models = ["model-a", "model-b"]
default = "model-a"
api_key_env = "LOCAL_API_KEY"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	models := NewApp().Models()
	refs := modelRefsFromView(models)
	for _, want := range []string{"local/model-a", "local/model-b"} {
		if !refs[want] {
			t.Fatalf("Models() refs = %+v, missing %s", models, want)
		}
	}
}

func TestModelsForTabListsMimoAPIPaidAccess(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "MIMO_API_KEY", "sk-test")

	cfg := config.Default()
	preset, ok := config.CuratedProviderPreset("mimo-api")
	if !ok || len(preset.Entries) == 0 {
		t.Fatal("mimo-api preset missing")
	}
	if err := cfg.UpsertProvider(preset.Entries[0]); err != nil {
		t.Fatalf("upsert mimo-api preset: %v", err)
	}
	cfg.DefaultModel = "mimo-api/mimo-v2.5-pro"
	cfg.Desktop.ProviderAccess = []string{"mimo-api"}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	models := NewApp().Models()
	refs := modelRefsFromView(models)
	for _, want := range []string{
		"mimo-api/mimo-v2.5-pro",
		"mimo-api/mimo-v2.5",
	} {
		if !refs[want] {
			t.Fatalf("Models() refs = %+v, missing %s", models, want)
		}
	}
	if len(models) != 2 {
		t.Fatalf("Models() len = %d, want 2: %+v", len(models), models)
	}
}

func TestModelsForTabKeepsUserProvidersWithProjectConfig(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "DEEPSEEK_API_KEY", "sk-test")
	setDesktopTestCredential(t, "MIMO_API_KEY", "sk-test")

	userCfg := config.Default()
	userCfg.DefaultModel = "mimo-pro/mimo-v2.5-pro"
	userCfg.Desktop.ProviderAccess = []string{"deepseek-flash", "mimo-pro"}
	if err := userCfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save user config: %v", err)
	}

	projectRoot := t.TempDir()
	projectConfig := `default_model = "deepseek-flash/deepseek-v4-flash"

[desktop]
provider_access = ["deepseek-flash"]

[[providers]]
name = "deepseek-flash"
kind = "openai"
base_url = "https://api.deepseek.com"
model = "deepseek-v4-flash"
api_key_env = "DEEPSEEK_API_KEY"
`
	if err := os.WriteFile(filepath.Join(projectRoot, "reasonix.toml"), []byte(projectConfig), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	app := NewApp()
	tab := &WorkspaceTab{ID: "project", WorkspaceRoot: projectRoot, Ready: true}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.activeTabID = tab.ID

	models := app.ModelsForTab(tab.ID)
	refs := modelRefsFromView(models)
	for _, want := range []string{
		"deepseek/deepseek-v4-flash",
		"mimo-pro/mimo-v2.5-pro",
	} {
		if !refs[want] {
			t.Fatalf("ModelsForTab refs = %+v, missing %s", models, want)
		}
	}
}

func TestSetModelForTabRejectsProviderOutsideAccess(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "DEEPSEEK_API_KEY", "sk-test")
	setDesktopTestCredential(t, "MIMO_API_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "deepseek-flash/deepseek-v4-flash"
	cfg.Desktop.ProviderAccess = []string{"deepseek-flash"}
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{Name: "other", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "other-model"})
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := NewApp()
	app.ctx = context.Background()
	tab := &WorkspaceTab{ID: "tab_a", Scope: "global", Ready: true, model: "deepseek-flash/deepseek-v4-flash"}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	err := app.SetModelForTab(tab.ID, "other/other-model")
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("SetModelForTab hidden provider error = %v, want not available", err)
	}
}

func TestSetModelForTabRefreshesCarriedSystemPromptWithoutChangingDefaults(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")
	setDesktopTestCredential(t, "NEW_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old", "new"}
	cfg.Providers = []config.ProviderEntry{
		{Name: "old", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "old-model", APIKeyEnv: "OLD_MODEL_KEY"},
		{Name: "new", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "new-model", APIKeyEnv: "NEW_MODEL_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := os.MkdirAll(config.MemoryUserDir(), 0o755); err != nil {
		t.Fatalf("mkdir memory dir: %v", err)
	}
	const freshRule = "Fresh global AGENTS rule for model switch"
	if err := os.WriteFile(filepath.Join(config.MemoryUserDir(), "AGENTS.md"), []byte(freshRule), 0o644); err != nil {
		t.Fatalf("write global AGENTS.md: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	oldSession := agent.NewSession("old system prompt without memory")
	oldSession.Add(provider.Message{Role: provider.RoleUser, Content: "hello"})
	oldExec := agent.New(nil, nil, oldSession, agent.Options{}, event.Discard)
	oldPath := filepath.Join(dir, "old.jsonl")
	oldCtrl := control.New(control.Options{Executor: oldExec, SessionDir: dir, SessionPath: oldPath, Label: "old", Sink: event.Discard})

	app := NewApp()
	app.ctx = context.Background()
	tab := &WorkspaceTab{
		ID:          "tab_a",
		Scope:       "global",
		Ready:       true,
		model:       "old/old-model",
		Ctrl:        oldCtrl,
		sink:        &tabEventSink{tabID: "tab_a", app: app},
		disabledMCP: map[string]ServerView{},
	}
	sibling := &WorkspaceTab{
		ID:          "tab_b",
		Scope:       "global",
		Ready:       true,
		model:       "old/old-model",
		disabledMCP: map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab, sibling.ID: sibling}
	app.tabOrder = []string{tab.ID, sibling.ID}
	app.activeTabID = tab.ID
	var switchTiming modelSwitchTiming
	app.modelSwitchTimingHook = func(timing modelSwitchTiming) { switchTiming = timing }
	t.Cleanup(func() {
		if tab.Ctrl != nil {
			tab.Ctrl.Close()
		}
	})

	if err := app.SetModelForTab(tab.ID, "new/new-model"); err != nil {
		t.Fatalf("SetModelForTab: %v", err)
	}
	history := tab.Ctrl.History()
	if len(history) < 2 {
		t.Fatalf("history length = %d, want system + user", len(history))
	}
	if history[0].Role != provider.RoleSystem {
		t.Fatalf("first message role = %s, want system", history[0].Role)
	}
	if !strings.Contains(history[0].Content, freshRule) {
		t.Fatalf("refreshed system prompt missing global AGENTS rule:\n%s", history[0].Content)
	}
	if history[1].Role != provider.RoleUser || history[1].Content != "hello" {
		t.Fatalf("carried user message changed: %+v", history[1])
	}
	if got := config.LoadForEdit(config.UserConfigPath()).DefaultModel; got != "old/old-model" {
		t.Fatalf("default model after session switch = %q, want old/old-model", got)
	}
	if sibling.model != "old/old-model" {
		t.Fatalf("sibling tab model after session switch = %q, want old/old-model", sibling.model)
	}
	if switchTiming.Outcome != "ok" || switchTiming.Total <= 0 {
		t.Fatalf("model switch timing = %+v, want successful non-zero observation", switchTiming)
	}
	if switchTiming.Build <= 0 || switchTiming.LeaseAndResume <= 0 || switchTiming.SwapAndPersist <= 0 {
		t.Fatalf("model switch stage timing incomplete: %+v", switchTiming)
	}
}

// TestSetModelForTabRestoresSessionAuthorizations pins the fix for a model
// switch dropping same-session "Allow for this session" tool grants and
// Plan-mode read-only command trust, forcing the user to re-approve
// something already granted this session after every model/effort/token-mode
// switch.
func TestSetModelForTabRestoresSessionAuthorizations(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")
	setDesktopTestCredential(t, "NEW_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old", "new"}
	cfg.Providers = []config.ProviderEntry{
		{Name: "old", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "old-model", APIKeyEnv: "OLD_MODEL_KEY"},
		{Name: "new", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "new-model", APIKeyEnv: "NEW_MODEL_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	oldExec := agent.New(nil, nil, agent.NewSession("old system prompt"), agent.Options{}, event.Discard)
	oldPath := filepath.Join(dir, "old.jsonl")
	oldCtrl := control.New(control.Options{Executor: oldExec, SessionDir: dir, SessionPath: oldPath, Label: "old", Sink: event.Discard})
	oldCtrl.RestoreSessionAuthorizations(control.SessionAuthorizations{
		Grants:                   []string{"bash|go test ./..."},
		PlanModeReadOnlyCommands: []string{"go test ./..."},
	})

	app := NewApp()
	app.ctx = context.Background()
	tab := &WorkspaceTab{
		ID:          "tab_a",
		Scope:       "global",
		Ready:       true,
		model:       "old/old-model",
		Ctrl:        oldCtrl,
		sink:        &tabEventSink{tabID: "tab_a", app: app},
		disabledMCP: map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	t.Cleanup(func() {
		if tab.Ctrl != nil {
			tab.Ctrl.Close()
		}
	})

	if err := app.SetModelForTab(tab.ID, "new/new-model"); err != nil {
		t.Fatalf("SetModelForTab: %v", err)
	}

	newCtrl, ok := tab.Ctrl.(*control.Controller)
	if !ok {
		t.Fatalf("tab.Ctrl = %T, want *control.Controller", tab.Ctrl)
	}
	got := newCtrl.SessionAuthorizations()
	if len(got.Grants) != 1 || got.Grants[0] != "bash|go test ./..." {
		t.Fatalf("restored grants = %+v, want [\"bash|go test ./...\"]", got.Grants)
	}
	if len(got.PlanModeReadOnlyCommands) != 1 || got.PlanModeReadOnlyCommands[0] != "go test ./..." {
		t.Fatalf("restored plan-mode read-only commands = %+v, want [\"go test ./...\"]", got.PlanModeReadOnlyCommands)
	}
}

// TestRebuildSettingLockedRestoresSessionAuthorizations covers the same
// dropped-session-authorization bug for the settings-change rebuild path
// (also used by the deferred-rebuild retry loop), independent from
// SetModelForTab's own rebuild.
func TestRebuildSettingLockedRestoresSessionAuthorizations(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old"}
	cfg.Providers = []config.ProviderEntry{
		{Name: "old", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "old-model", APIKeyEnv: "OLD_MODEL_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	oldExec := agent.New(nil, nil, agent.NewSession("old system prompt"), agent.Options{}, event.Discard)
	oldPath := filepath.Join(dir, "old.jsonl")
	oldCtrl := control.New(control.Options{Executor: oldExec, SessionDir: dir, SessionPath: oldPath, Label: "old", Sink: event.Discard})
	oldCtrl.RestoreSessionAuthorizations(control.SessionAuthorizations{
		Grants:                   []string{"bash|go test ./..."},
		PlanModeReadOnlyCommands: []string{"go test ./..."},
	})

	app := NewApp()
	app.ctx = context.Background()
	tab := &WorkspaceTab{
		ID:          "tab_a",
		Scope:       "global",
		Ready:       true,
		model:       "old/old-model",
		Ctrl:        oldCtrl,
		sink:        &tabEventSink{tabID: "tab_a", app: app},
		disabledMCP: map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	app.readyHook = func() {}
	t.Cleanup(func() {
		if tab.Ctrl != nil {
			tab.Ctrl.Close()
		}
	})

	if err := app.rebuildSetting("settings"); err != nil {
		t.Fatalf("rebuildSetting: %v", err)
	}

	newCtrl, ok := tab.Ctrl.(*control.Controller)
	if !ok {
		t.Fatalf("tab.Ctrl = %T, want *control.Controller", tab.Ctrl)
	}
	got := newCtrl.SessionAuthorizations()
	if len(got.Grants) != 1 || got.Grants[0] != "bash|go test ./..." {
		t.Fatalf("restored grants = %+v, want [\"bash|go test ./...\"]", got.Grants)
	}
	if len(got.PlanModeReadOnlyCommands) != 1 || got.PlanModeReadOnlyCommands[0] != "go test ./..." {
		t.Fatalf("restored plan-mode read-only commands = %+v, want [\"go test ./...\"]", got.PlanModeReadOnlyCommands)
	}
}

func TestSetModelForTabContinuesRecoveryPathAfterSnapshotConflict(t *testing.T) {
	isolateDesktopUserDirsSchemaOne(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")
	setDesktopTestCredential(t, "NEW_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old", "new"}
	cfg.Providers = []config.ProviderEntry{
		{Name: "old", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "old-model", APIKeyEnv: "OLD_MODEL_KEY"},
		{Name: "new", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "new-model", APIKeyEnv: "NEW_MODEL_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	originalPath := filepath.Join(dir, "model-switch-conflict.jsonl")
	current := agent.NewSession("old system prompt")
	current.Add(provider.Message{Role: provider.RoleUser, Content: "first"})
	current.Add(provider.Message{Role: provider.RoleAssistant, Content: "one"})
	current.Add(provider.Message{Role: provider.RoleUser, Content: "disk second"})
	if err := current.Save(originalPath); err != nil {
		t.Fatalf("save current session: %v", err)
	}

	stale := agent.NewSession("old system prompt")
	stale.Add(provider.Message{Role: provider.RoleUser, Content: "first"})
	stale.Add(provider.Message{Role: provider.RoleAssistant, Content: "one"})
	stale.Add(provider.Message{Role: provider.RoleUser, Content: "local second"})
	oldExec := agent.New(nil, nil, stale, agent.Options{}, event.Discard)

	app := NewApp()
	app.ctx = context.Background()
	app.runtimeEvents.emit = func(context.Context, string, ...any) {}
	tab := &WorkspaceTab{
		ID:          "tab_recovery_model",
		Scope:       "global",
		SessionPath: originalPath,
		Ready:       true,
		model:       "old/old-model",
		disabledMCP: map[string]ServerView{},
	}
	tab.sink = &tabEventSink{tabID: tab.ID, app: app}
	oldCtrl := control.New(control.Options{
		Executor:            oldExec,
		SessionDir:          dir,
		SessionPath:         originalPath,
		Label:               "old",
		Sink:                tab.sink,
		SessionRecoveryMeta: app.tabSessionRecoveryMeta(tab),
		OnSessionRecovered:  app.handleTabSessionRecovered(tab),
	})
	tab.Ctrl = oldCtrl
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	t.Cleanup(func() {
		if tab.Ctrl != nil {
			tab.Ctrl.Close()
		}
		tab.releaseSessionLease()
	})

	if err := app.SetModelForTab(tab.ID, "new/new-model"); err != nil {
		t.Fatalf("SetModelForTab: %v", err)
	}
	recoveryPath := tab.Ctrl.SessionPath()
	if recoveryPath == "" || recoveryPath == originalPath || !strings.Contains(filepath.Base(recoveryPath), "-recovery-") {
		t.Fatalf("model switch session path = %q, want recovery path distinct from %q", recoveryPath, originalPath)
	}
	if got := tab.currentSessionPath(); got != recoveryPath {
		t.Fatalf("tab current session path = %q, want recovery path %q", got, recoveryPath)
	}
	if tab.sessionLease == nil || sessionRuntimeKey(tab.sessionLease.Path()) != sessionRuntimeKey(recoveryPath) {
		t.Fatalf("tab lease path = %q, want recovery path %q", tab.sessionLeaseRuntimeKey(), recoveryPath)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*-recovery-*.jsonl"))
	if err != nil {
		t.Fatalf("glob recovery branches: %v", err)
	}
	matches = primarySessionFiles(matches)
	if len(matches) != 1 || matches[0] != recoveryPath {
		t.Fatalf("recovery branches after model switch = %v, want only %q", matches, recoveryPath)
	}
	if err := tab.Ctrl.Snapshot(); err != nil {
		t.Fatalf("Snapshot after model switch recovery: %v", err)
	}
	matches, err = filepath.Glob(filepath.Join(dir, "*-recovery-*.jsonl"))
	if err != nil {
		t.Fatalf("glob recovery branches after snapshot: %v", err)
	}
	matches = primarySessionFiles(matches)
	if len(matches) != 1 || matches[0] != recoveryPath {
		t.Fatalf("recovery branches after follow-up snapshot = %v, want only %q", matches, recoveryPath)
	}
}

func TestSetModelForTabReusesCurrentSessionLease(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")
	setDesktopTestCredential(t, "NEW_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old", "new"}
	cfg.Providers = []config.ProviderEntry{
		{Name: "old", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "old-model", APIKeyEnv: "OLD_MODEL_KEY"},
		{Name: "new", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "new-model", APIKeyEnv: "NEW_MODEL_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	oldSession := agent.NewSession("old system prompt")
	oldSession.Add(provider.Message{Role: provider.RoleUser, Content: "hello"})
	oldExec := agent.New(nil, nil, oldSession, agent.Options{}, event.Discard)
	oldPath := filepath.Join(dir, "leased-model-switch.jsonl")
	oldCtrl := control.New(control.Options{Executor: oldExec, SessionDir: dir, SessionPath: oldPath, Label: "old", Sink: event.Discard})

	app := NewApp()
	app.ctx = context.Background()
	tab := &WorkspaceTab{
		ID:          "tab_a",
		Scope:       "global",
		Ready:       true,
		model:       "old/old-model",
		Ctrl:        oldCtrl,
		sink:        &tabEventSink{tabID: "tab_a", app: app},
		disabledMCP: map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	t.Cleanup(func() {
		if tab.Ctrl != nil {
			tab.Ctrl.Close()
		}
		tab.releaseSessionLease()
	})

	if err := tab.ensureSessionLease(oldPath); err != nil {
		t.Fatalf("ensureSessionLease: %v", err)
	}
	if err := app.SetModelForTab(tab.ID, "new/new-model"); err != nil {
		t.Fatalf("SetModelForTab: %v", err)
	}
	if tab.Ctrl == nil || tab.Ctrl == oldCtrl {
		t.Fatalf("tab controller was not rebuilt")
	}
	if got := tab.model; got != "new/new-model" {
		t.Fatalf("tab model = %q, want new/new-model", got)
	}
	if tab.sessionLease == nil || sessionRuntimeKey(tab.sessionLease.Path()) != sessionRuntimeKey(oldPath) {
		t.Fatalf("session lease path = %q, want %q", tab.currentSessionPath(), oldPath)
	}
	history := tab.Ctrl.History()
	if len(history) < 2 || history[1].Role != provider.RoleUser || history[1].Content != "hello" {
		t.Fatalf("carried history = %+v, want original user message", history)
	}
}

func TestSetModelForTabWaitsForConcurrentBlankSessionLease(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")
	setDesktopTestCredential(t, "NEW_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old", "new"}
	cfg.Providers = []config.ProviderEntry{
		{Name: "old", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "old-model", APIKeyEnv: "OLD_MODEL_KEY"},
		{Name: "new", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "new-model", APIKeyEnv: "NEW_MODEL_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := desktopSessionDir(globalTabWorkspaceRoot())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	path := filepath.Join(dir, "blank-model-switch-race.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write blank session: %v", err)
	}

	app := NewApp()
	app.ctx = context.Background()
	tab := &WorkspaceTab{
		ID:            "tab_blank_race",
		Scope:         "global",
		WorkspaceRoot: globalTabWorkspaceRoot(),
		SessionPath:   path,
		Ready:         true,
		model:         "old/old-model",
		sink:          &tabEventSink{tabID: "tab_blank_race", app: app},
		disabledMCP:   map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	t.Cleanup(func() {
		if tab.Ctrl != nil {
			tab.Ctrl.Close()
		}
		tab.releaseSessionLease()
	})

	acquired := make(chan struct{})
	releaseHook := make(chan struct{})
	var once sync.Once
	sessionLeaseAcquireHookForTest = func() {
		once.Do(func() {
			close(acquired)
			<-releaseHook
		})
	}
	t.Cleanup(func() { sessionLeaseAcquireHookForTest = nil })

	buildErr := make(chan error, 1)
	go func() {
		buildErr <- tab.ensureSessionLease(path)
	}()

	select {
	case <-acquired:
	case err := <-buildErr:
		t.Fatalf("background lease acquire returned before hook: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("background lease acquire did not start")
	}

	switchErr := make(chan error, 1)
	go func() {
		switchErr <- app.SetModelForTab(tab.ID, "new/new-model")
	}()

	select {
	case err := <-switchErr:
		t.Fatalf("SetModelForTab returned before concurrent lease was bound: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseHook)
	if err := <-buildErr; err != nil {
		t.Fatalf("background ensureSessionLease: %v", err)
	}
	if err := <-switchErr; err != nil {
		t.Fatalf("SetModelForTab: %v", err)
	}
	if tab.Ctrl == nil {
		t.Fatal("model switch did not build a controller")
	}
	if got := tab.model; got != "new/new-model" {
		t.Fatalf("tab model = %q, want new/new-model", got)
	}
	if tab.sessionLease == nil || sessionRuntimeKey(tab.sessionLease.Path()) != sessionRuntimeKey(path) {
		t.Fatalf("session lease path = %q, want %q", tab.currentSessionPath(), path)
	}
}

func TestSetModelForTabLeaseHeldKeepsCurrentController(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")
	setDesktopTestCredential(t, "NEW_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old", "new"}
	cfg.Providers = []config.ProviderEntry{
		{Name: "old", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "old-model", APIKeyEnv: "OLD_MODEL_KEY"},
		{Name: "new", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "new-model", APIKeyEnv: "NEW_MODEL_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	oldPath := filepath.Join(dir, "externally-leased-model-switch.jsonl")
	if err := os.WriteFile(oldPath, nil, 0o644); err != nil {
		t.Fatalf("write placeholder session: %v", err)
	}
	externalLease, err := agent.TryAcquireSessionLease(oldPath)
	if err != nil {
		t.Fatalf("TryAcquireSessionLease: %v", err)
	}
	defer externalLease.Release()

	oldSession := agent.NewSession("old system prompt")
	oldSession.Add(provider.Message{Role: provider.RoleUser, Content: "hello"})
	oldExec := agent.New(nil, nil, oldSession, agent.Options{}, event.Discard)
	oldCtrl := control.New(control.Options{Executor: oldExec, SessionDir: dir, SessionPath: oldPath, Label: "old", Sink: event.Discard})
	defer oldCtrl.Close()

	app := NewApp()
	app.ctx = context.Background()
	tab := &WorkspaceTab{
		ID:          "tab_a",
		Scope:       "global",
		Ready:       true,
		model:       "old/old-model",
		Ctrl:        oldCtrl,
		sink:        &tabEventSink{tabID: "tab_a", app: app},
		disabledMCP: map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	err = app.SetModelForTab(tab.ID, "new/new-model")
	if !errors.Is(err, agent.ErrSessionLeaseHeld) {
		t.Fatalf("SetModelForTab err = %v, want ErrSessionLeaseHeld", err)
	}
	if strings.Contains(err.Error(), oldPath) || strings.Contains(err.Error(), "held by") {
		t.Fatalf("SetModelForTab surfaced raw lease details: %v", err)
	}
	if tab.Ctrl != oldCtrl {
		t.Fatalf("tab controller changed after failed switch")
	}
	if got := tab.model; got != "old/old-model" {
		t.Fatalf("tab model = %q, want old/old-model", got)
	}
	info, err := os.Stat(oldPath)
	if err != nil {
		t.Fatalf("stat session: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("session file size = %d, want unchanged empty file", info.Size())
	}
}

func TestSetModelForTabReattachesDetachedRuntime(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")
	setDesktopTestCredential(t, "NEW_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old", "new"}
	cfg.Providers = []config.ProviderEntry{
		{Name: "old", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "old-model", APIKeyEnv: "OLD_MODEL_KEY"},
		{Name: "new", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "new-model", APIKeyEnv: "NEW_MODEL_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := desktopSessionDir(globalTabWorkspaceRoot())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "detached-model-switch.jsonl")
	oldSession := agent.NewSession("old system prompt")
	oldSession.Add(provider.Message{Role: provider.RoleUser, Content: "hello from detached"})
	oldExec := agent.New(nil, nil, oldSession, agent.Options{}, event.Discard)
	oldCtrl := control.New(control.Options{Executor: oldExec, SessionDir: dir, SessionPath: path, Label: "old", Sink: event.Discard})
	lease, err := agent.TryAcquireSessionLease(path)
	if err != nil {
		t.Fatalf("TryAcquireSessionLease: %v", err)
	}

	app := NewApp()
	app.ctx = context.Background()
	key := sessionRuntimeKey(path)
	detached := &WorkspaceTab{
		ID:             detachedRuntimeTabID(key),
		Scope:          "global",
		SessionPath:    path,
		Ctrl:           oldCtrl,
		Ready:          true,
		model:          "old/old-model",
		disabledMCP:    map[string]ServerView{},
		SharedHostKey:  "detached-host",
		ActivityStatus: "",
	}
	detached.adoptSessionLease(lease)
	tab := &WorkspaceTab{
		ID:          "tab_a",
		Scope:       "global",
		SessionPath: path,
		Ready:       true,
		model:       "old/old-model",
		sink:        &tabEventSink{tabID: "tab_a", app: app},
		disabledMCP: map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.detachedSessions = map[string]*WorkspaceTab{key: detached}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	t.Cleanup(func() {
		if tab.Ctrl != nil {
			tab.Ctrl.Close()
		}
		tab.releaseSessionLease()
		if detached.sessionLease != nil {
			detached.releaseSessionLease()
		}
	})

	if err := app.SetModelForTab(tab.ID, "new/new-model"); err != nil {
		t.Fatalf("SetModelForTab: %v", err)
	}
	if _, ok := app.detachedSessions[key]; ok {
		t.Fatal("detached runtime was not consumed")
	}
	if tab.Ctrl == nil || tab.Ctrl == oldCtrl {
		t.Fatalf("tab controller was not rebuilt from detached runtime")
	}
	if got := tab.model; got != "new/new-model" {
		t.Fatalf("tab model = %q, want new/new-model", got)
	}
	if tab.sessionLease == nil || sessionRuntimeKey(tab.sessionLease.Path()) != key {
		t.Fatalf("session lease path = %q, want %q", tab.currentSessionPath(), path)
	}
	history := tab.Ctrl.History()
	if len(history) < 2 || history[1].Content != "hello from detached" {
		t.Fatalf("carried history = %+v, want detached user message", history)
	}
}

type staleWorkspaceBindingFixture struct {
	app          *App
	tab          *WorkspaceTab
	oldCtrl      control.SessionAPI
	projectA     string
	sessionDirA  string
	sessionPathA string
}

func newStaleWorkspaceBindingFixture(t *testing.T, suffix string) staleWorkspaceBindingFixture {
	return newStaleWorkspaceBindingFixtureWithLayout(t, suffix, "")
}

func newStaleWorkspaceBindingFixtureWithLayout(t *testing.T, suffix, layoutStyle string) staleWorkspaceBindingFixture {
	t.Helper()
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "TEST_MODEL_KEY", "sk-test")

	// Submitted turns use a real provider, so the fixture must complete them
	// instantly instead of pointing at an unreachable host.
	providerStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(providerStub.Close)

	cfg := config.Default()
	cfg.DefaultModel = "test/test-model"
	cfg.Desktop.ProviderAccess = []string{"test"}
	cfg.Providers = []config.ProviderEntry{
		{Name: "test", Kind: "openai", BaseURL: providerStub.URL, Model: "test-model", APIKeyEnv: "TEST_MODEL_KEY"},
	}
	if strings.TrimSpace(layoutStyle) != "" {
		if err := cfg.SetDesktopLayoutStyle(layoutStyle); err != nil {
			t.Fatalf("set desktop layout style: %v", err)
		}
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	projectA := t.TempDir()
	projectB := t.TempDir()
	if err := addProject(projectA, "Project A"); err != nil {
		t.Fatalf("add project A: %v", err)
	}
	if err := addProject(projectB, "Project B"); err != nil {
		t.Fatalf("add project B: %v", err)
	}

	topicID := "topic_" + suffix
	topicTitle := "Rebuild workspace " + suffix
	sessionDirA := desktopSessionDir(projectA)
	sessionDirB := desktopSessionDir(projectB)
	if err := os.MkdirAll(sessionDirA, 0o755); err != nil {
		t.Fatalf("mkdir project A sessions: %v", err)
	}
	if err := os.MkdirAll(sessionDirB, 0o755); err != nil {
		t.Fatalf("mkdir project B sessions: %v", err)
	}
	sessionPathA := writeTopicSessionWithPrompt(t, sessionDirA, "project-a.jsonl", topicID, topicTitle, projectA, "project A prompt", time.Now())
	sessionPathB := filepath.Join(sessionDirB, "wrong.jsonl")

	oldSession := agent.NewSession("old system prompt")
	oldSession.Add(provider.Message{Role: provider.RoleUser, Content: "carry me"})
	oldExec := agent.New(nil, nil, oldSession, agent.Options{}, event.Discard)
	oldCtrl := control.New(control.Options{
		Executor:      oldExec,
		SessionDir:    sessionDirB,
		SessionPath:   sessionPathB,
		Label:         "test/test-model",
		ModelRef:      "test/test-model",
		WorkspaceRoot: projectB,
		Sink:          event.Discard,
	})

	app := NewApp()
	app.readyHook = func() {}
	tab := &WorkspaceTab{
		ID:            "tab_stale_workspace_" + suffix,
		Scope:         "project",
		WorkspaceRoot: projectB,
		TopicID:       topicID,
		TopicTitle:    topicTitle,
		SessionPath:   sessionPathA,
		Ready:         true,
		model:         "test/test-model",
		Ctrl:          oldCtrl,
		sink:          &tabEventSink{tabID: "tab_stale_workspace_" + suffix, app: app},
		disabledMCP:   map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	t.Cleanup(func() {
		if tab.Ctrl != nil {
			tab.Ctrl.Close()
		}
	})

	return staleWorkspaceBindingFixture{
		app:          app,
		tab:          tab,
		oldCtrl:      oldCtrl,
		projectA:     projectA,
		sessionDirA:  sessionDirA,
		sessionPathA: sessionPathA,
	}
}

func assertTabRebuiltToPinnedWorkspace(t *testing.T, f staleWorkspaceBindingFixture) {
	t.Helper()
	if f.tab.Ctrl == nil {
		t.Fatal("controller was not rebuilt")
	}
	if f.tab.Ctrl == f.oldCtrl {
		t.Fatal("stale controller was reused")
	}
	if got := normalizeProjectRoot(f.tab.WorkspaceRoot); got != normalizeProjectRoot(f.projectA) {
		t.Fatalf("tab workspace root = %q, want project A %q", got, normalizeProjectRoot(f.projectA))
	}
	if got := normalizeProjectRoot(f.tab.Ctrl.WorkspaceRoot()); got != normalizeProjectRoot(f.projectA) {
		t.Fatalf("controller workspace root = %q, want project A %q", got, normalizeProjectRoot(f.projectA))
	}
	if !sameDesktopPath(f.tab.Ctrl.SessionDir(), f.sessionDirA) {
		t.Fatalf("controller session dir = %q, want %q", f.tab.Ctrl.SessionDir(), f.sessionDirA)
	}
	if !sameDesktopPath(f.tab.Ctrl.SessionPath(), f.sessionPathA) {
		t.Fatalf("controller session path = %q, want %q", f.tab.Ctrl.SessionPath(), f.sessionPathA)
	}
}

type blockingSnapshotCtrl struct {
	control.SessionAPI

	firstSnapshotStarted  chan struct{}
	secondSnapshotStarted chan struct{}
	releaseSnapshot       chan struct{}
	firstOnce             sync.Once
	secondOnce            sync.Once
	snapshotCount         atomic.Int32
	closeCount            atomic.Int32
}

func newBlockingSnapshotCtrl(ctrl control.SessionAPI) *blockingSnapshotCtrl {
	return &blockingSnapshotCtrl{
		SessionAPI:            ctrl,
		firstSnapshotStarted:  make(chan struct{}),
		secondSnapshotStarted: make(chan struct{}),
		releaseSnapshot:       make(chan struct{}),
	}
}

func (c *blockingSnapshotCtrl) Snapshot() error {
	count := c.snapshotCount.Add(1)
	switch count {
	case 1:
		c.firstOnce.Do(func() { close(c.firstSnapshotStarted) })
	case 2:
		c.secondOnce.Do(func() { close(c.secondSnapshotStarted) })
	}
	<-c.releaseSnapshot
	if c.SessionAPI == nil {
		return nil
	}
	return c.SessionAPI.Snapshot()
}

func (c *blockingSnapshotCtrl) Close() {
	c.closeCount.Add(1)
	if c.SessionAPI != nil {
		c.SessionAPI.Close()
	}
}

func (f *staleWorkspaceBindingFixture) installBlockingSnapshotController() *blockingSnapshotCtrl {
	ctrl := newBlockingSnapshotCtrl(f.tab.Ctrl)
	f.tab.Ctrl = ctrl
	f.oldCtrl = ctrl
	return ctrl
}

func TestEnsureTabControllerWorkspaceRebuildsStaleWorkspace(t *testing.T) {
	f := newStaleWorkspaceBindingFixture(t, "rebuild_workspace")

	if err := f.app.ensureTabControllerWorkspace(f.tab); err != nil {
		t.Fatalf("ensureTabControllerWorkspace: %v", err)
	}
	assertTabRebuiltToPinnedWorkspace(t, f)
}

func TestEnsureTabControllerWorkspaceWarnsWhenPinnedSessionSwitchesWorkspace(t *testing.T) {
	f := newStaleWorkspaceBindingFixture(t, "warn_workspace_switch")
	events := make(chan event.Event, 8)
	f.tab.sink.SetBotSink(event.FuncSink(func(e event.Event) {
		events <- e
	}))

	if err := f.app.ensureTabControllerWorkspace(f.tab); err != nil {
		t.Fatalf("ensureTabControllerWorkspace: %v", err)
	}
	assertTabRebuiltToPinnedWorkspace(t, f)

	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Kind == event.Notice &&
				e.Level == event.LevelWarn &&
				strings.Contains(strings.ToLower(e.Text), strings.ToLower(f.projectA)) &&
				strings.Contains(e.Text, "switched tab") {
				return
			}
		case <-deadline:
			t.Fatal("did not receive workspace switch warning notice")
		}
	}
}

func TestDescribeSessionBindingWorkspaceKeepsWindowsPathReadable(t *testing.T) {
	path := `C:\Users\Jane Doe\Reasonix`
	want := `project workspace "C:\Users\Jane Doe\Reasonix"`
	if got := describeSessionBindingWorkspace("project", path); got != want {
		t.Fatalf("describeSessionBindingWorkspace = %q, want %q", got, want)
	}
}

func TestSteerForTabReconcilesStaleWorkspaceBeforeRejectingIdleGuidance(t *testing.T) {
	f := newStaleWorkspaceBindingFixture(t, "steer_idle_fallback")

	err := f.app.SteerForTab(f.tab.ID, "steer guidance")
	if err == nil || !strings.Contains(err.Error(), "remain queued") {
		t.Fatalf("SteerForTab error = %v, want explicit rejected-guidance result", err)
	}
	assertTabRebuiltToPinnedWorkspace(t, f)
}

func TestCompactReconcilesStaleWorkspaceBeforeCompaction(t *testing.T) {
	f := newStaleWorkspaceBindingFixture(t, "compact")

	if err := f.app.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	assertTabRebuiltToPinnedWorkspace(t, f)
}

func TestEffortCommandUsesPinnedSessionOwnerBeforeStaleWorkspaceRoot(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OWNER_MODEL_KEY", "sk-test")
	setDesktopTestCredential(t, "STALE_MODEL_KEY", "sk-test")

	projectA := t.TempDir()
	projectB := t.TempDir()
	if err := addProject(projectA, "Project A"); err != nil {
		t.Fatalf("add project A: %v", err)
	}
	if err := addProject(projectB, "Project B"); err != nil {
		t.Fatalf("add project B: %v", err)
	}
	ownerConfig := `default_model = "owner/owner-model"
[[providers]]
name = "owner"
kind = "openai"
base_url = "https://owner.example.invalid/v1"
model = "owner-model"
api_key_env = "OWNER_MODEL_KEY"
supported_efforts = ["max"]
default_effort = "max"
`
	if err := os.WriteFile(filepath.Join(projectA, "reasonix.toml"), []byte(ownerConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	staleConfig := `default_model = "stale/stale-model"
[[providers]]
name = "stale"
kind = "openai"
base_url = "https://stale.example.invalid/v1"
model = "stale-model"
api_key_env = "STALE_MODEL_KEY"
reasoning_protocol = "none"
`
	if err := os.WriteFile(filepath.Join(projectB, "reasonix.toml"), []byte(staleConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	topicID := "topic_effort_owner"
	topicTitle := "Effort owner"
	sessionDirA := desktopSessionDir(projectA)
	sessionDirB := desktopSessionDir(projectB)
	if err := os.MkdirAll(sessionDirA, 0o755); err != nil {
		t.Fatalf("mkdir project A sessions: %v", err)
	}
	if err := os.MkdirAll(sessionDirB, 0o755); err != nil {
		t.Fatalf("mkdir project B sessions: %v", err)
	}
	sessionPathA := writeTopicSessionWithPrompt(t, sessionDirA, "project-a.jsonl", topicID, topicTitle, projectA, "project A prompt", time.Now())
	oldCtrl := control.New(control.Options{
		SessionDir:    sessionDirB,
		SessionPath:   filepath.Join(sessionDirB, "wrong.jsonl"),
		WorkspaceRoot: projectB,
		Sink:          event.Discard,
	})

	app := NewApp()
	app.readyHook = func() {}
	tab := &WorkspaceTab{
		ID:            "tab_stale_effort",
		Scope:         "project",
		WorkspaceRoot: projectB,
		TopicID:       topicID,
		TopicTitle:    topicTitle,
		SessionPath:   sessionPathA,
		Ready:         true,
		Ctrl:          oldCtrl,
		sink:          &tabEventSink{tabID: "tab_stale_effort", app: app},
		disabledMCP:   map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	t.Cleanup(func() {
		if tab.Ctrl != nil {
			tab.Ctrl.Close()
		}
	})

	if err := app.SubmitToTab(tab.ID, "/effort max"); err != nil {
		t.Fatalf("SubmitToTab(/effort max): %v", err)
	}
	waitNotRunning(t, tab.Ctrl)
	if tab.effort == nil || *tab.effort != "max" {
		t.Fatalf("tab effort = %#v, want max from pinned project A provider", tab.effort)
	}
	if got := normalizeProjectRoot(tab.WorkspaceRoot); got != normalizeProjectRoot(projectA) {
		t.Fatalf("tab workspace root = %q, want project A %q", got, normalizeProjectRoot(projectA))
	}
	if got := normalizeProjectRoot(tab.Ctrl.WorkspaceRoot()); got != normalizeProjectRoot(projectA) {
		t.Fatalf("controller workspace root = %q, want project A %q", got, normalizeProjectRoot(projectA))
	}
}

// A config file written before the classic style was removed still holds the
// retired value. It must read as workbench — not fail, and not resurrect the
// multi-tab model that value used to select, which the UI can no longer show.
func TestRetiredClassicLayoutReadsAsWorkbench(t *testing.T) {
	isolateDesktopUserDirs(t)
	if err := editUserConfig(func(c *config.Config) error {
		c.Desktop.LayoutStyle = "classic"
		return nil
	}); err != nil {
		t.Fatalf("write the retired layout style: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.DesktopLayoutStyle(); got != "workbench" {
		t.Fatalf("retired classic reads as %q, want workbench", got)
	}
}

func TestWorkbenchLayoutQuickClicksSerializeWorkspaceRebuild(t *testing.T) {
	runQuickClickWorkspaceReconcileTest(t, "workbench")
}

func TestCreationLayoutQuickClicksSerializeWorkspaceRebuild(t *testing.T) {
	runQuickClickWorkspaceReconcileTest(t, "creation")
}

func runQuickClickWorkspaceReconcileTest(t *testing.T, layoutStyle string) {
	t.Helper()
	f := newStaleWorkspaceBindingFixtureWithLayout(t, "quick_click_"+layoutStyle, layoutStyle)
	blockingCtrl := f.installBlockingSnapshotController()

	type quickAction struct {
		name string
		run  func() error
	}
	actions := []quickAction{
		{name: "submit", run: func() error { return f.app.SubmitToTab(f.tab.ID, "/unknown-command") }},
		{name: "steer", run: func() error { return f.app.SteerForTab(f.tab.ID, "steer guidance") }},
		{name: "compact", run: func() error { return f.app.Compact() }},
		{name: "submit-display", run: func() error { return f.app.SubmitDisplayToTab(f.tab.ID, "/unknown display", "/unknown-command") }},
	}

	start := make(chan struct{})
	ready := make(chan struct{}, len(actions))
	errs := make(chan error, len(actions))
	var wg sync.WaitGroup
	for _, action := range actions {
		wg.Go(func() {
			ready <- struct{}{}
			<-start
			if err := action.run(); err != nil {
				errs <- fmt.Errorf("%s: %w", action.name, err)
			}
		})
	}
	for range actions {
		<-ready
	}
	close(start)

	select {
	case <-blockingCtrl.firstSnapshotStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first stale controller snapshot")
	}
	select {
	case <-blockingCtrl.secondSnapshotStarted:
		t.Fatal("workspace rebuild was not serialized: second stale snapshot started before the first rebuild finished")
	case <-time.After(75 * time.Millisecond):
	}
	close(blockingCtrl.releaseSnapshot)
	wg.Wait()
	close(errs)
	for err := range errs {
		// Racing quick clicks may legitimately observe a busy controller or an
		// already-ended steer target. This test asserts workspace-rebuild
		// serialization, not that every concurrent action wins admission.
		if strings.Contains(err.Error(), "turn already running") ||
			strings.Contains(err.Error(), "cannot compact while a turn is running") ||
			strings.Contains(err.Error(), "remain queued") {
			continue
		}
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	if got := blockingCtrl.snapshotCount.Load(); got != 1 {
		t.Fatalf("stale snapshot count = %d, want 1", got)
	}
	if got := blockingCtrl.closeCount.Load(); got != 1 {
		t.Fatalf("stale close count = %d, want 1", got)
	}
	waitNotRunning(t, f.tab.Ctrl)
	assertTabRebuiltToPinnedWorkspace(t, f)
}

func TestListSessionsUsesPinnedSessionOwnerBeforeStaleRuntimeDir(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectA := t.TempDir()
	projectB := t.TempDir()
	if err := addProject(projectA, "Project A"); err != nil {
		t.Fatalf("add project A: %v", err)
	}
	if err := addProject(projectB, "Project B"); err != nil {
		t.Fatalf("add project B: %v", err)
	}
	sessionDirA := desktopSessionDir(projectA)
	sessionDirB := desktopSessionDir(projectB)
	if err := os.MkdirAll(sessionDirA, 0o755); err != nil {
		t.Fatalf("mkdir project A sessions: %v", err)
	}
	if err := os.MkdirAll(sessionDirB, 0o755); err != nil {
		t.Fatalf("mkdir project B sessions: %v", err)
	}
	sessionPathA := writeTopicSessionWithPrompt(t, sessionDirA, "project-a.jsonl", "topic_project_a", "Project A topic", projectA, "project A prompt", time.Now())
	sessionPathB := writeTopicSessionWithPrompt(t, sessionDirB, "project-b.jsonl", "topic_project_b", "Project B topic", projectB, "project B prompt", time.Now().Add(time.Minute))

	app := NewApp()
	oldCtrl := control.New(control.Options{
		SessionDir:    sessionDirB,
		SessionPath:   sessionPathB,
		WorkspaceRoot: projectB,
		Sink:          event.Discard,
	})
	tab := &WorkspaceTab{
		ID:            "tab_stale_runtime_dir",
		Scope:         "project",
		WorkspaceRoot: projectB,
		TopicID:       "topic_project_a",
		TopicTitle:    "Project A topic",
		SessionPath:   sessionPathA,
		Ready:         true,
		Ctrl:          oldCtrl,
		disabledMCP:   map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	installSessionCatalogForTest(t, app, sessionDirA, "project", projectA)
	t.Cleanup(oldCtrl.Close)
	sessions := listSessionsAfterPinnedOwnerReconcile(t, app, sessionDirA, projectA)
	if len(sessions) == 0 {
		t.Fatal("ListSessions() returned no sessions")
	}
	if filepath.Clean(sessions[0].Path) != filepath.Clean(sessionPathA) {
		t.Fatalf("ListSessions()[0].Path = %q, want pinned project A session %q", sessions[0].Path, sessionPathA)
	}
	for _, item := range sessions {
		if filepath.Clean(item.Path) == filepath.Clean(sessionPathB) {
			t.Fatalf("ListSessions() included stale project B runtime session: %+v", sessions)
		}
	}
	if got := normalizeProjectRoot(tab.WorkspaceRoot); got != normalizeProjectRoot(projectA) {
		t.Fatalf("tab workspace root = %q, want project A %q", got, normalizeProjectRoot(projectA))
	}
}

func TestSetDefaultModelRejectsProviderWithoutKey(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("MIMO_API_KEY", "")

	cfg := config.Default()
	cfg.Desktop.ProviderAccess = []string{"mimo-api"}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := NewApp()
	tab := &WorkspaceTab{ID: "tab_a", Scope: "global", Ready: true, model: "deepseek-flash/deepseek-v4-flash"}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	err := app.SetDefaultModel("mimo-api/mimo-v2.5-pro")
	if err == nil || !strings.Contains(err.Error(), "has no key") {
		t.Fatalf("SetDefaultModel no-key error = %v, want has no key", err)
	}
	if tab.model != "deepseek-flash/deepseek-v4-flash" {
		t.Fatalf("tab model after failed default change = %q, want previous", tab.model)
	}
}

func TestSaveProviderPersistsReasoningProtocol(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	if err := app.SaveProvider(ProviderView{
		Name:              "deepseek-proxy",
		Kind:              "openai",
		BaseURL:           "https://proxy.example.com/v1",
		Models:            []string{"deepseek-v4-flash"},
		Default:           "deepseek-v4-flash",
		APIKeyEnv:         "DEEPSEEK_PROXY_KEY",
		ReasoningProtocol: "none",
		SupportedEfforts:  []string{"high", "max"},
		DefaultEffort:     "max",
	}); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}

	cfg := config.LoadForEdit(config.UserConfigPath())
	got, ok := cfg.Provider("deepseek-proxy")
	if !ok {
		t.Fatal("saved provider not found")
	}
	if got.ReasoningProtocol != "none" || got.DefaultEffort != "max" {
		t.Fatalf("saved provider = %+v, want reasoning_protocol none and default_effort max", got)
	}

	view := app.Settings()
	for _, p := range view.Providers {
		if p.Name == "deepseek-proxy" {
			if p.ReasoningProtocol != "none" {
				t.Fatalf("settings reasoningProtocol = %q, want none", p.ReasoningProtocol)
			}
			return
		}
	}
	t.Fatalf("Settings() missing saved provider: %+v", view.Providers)
}

func TestDeleteProviderMigratesConfigAndPreservesOpenTabs(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "REASONIX_TEST_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "prov-a/model-a2"
	cfg.Providers = []config.ProviderEntry{
		{Name: "prov-a", Kind: "openai", BaseURL: "https://a.example.com", Model: "model-a1", Models: []string{"model-a1", "model-a2"}, APIKeyEnv: "REASONIX_TEST_KEY"},
		{Name: "prov-b", Kind: "openai", BaseURL: "https://b.example.com", Model: "model-b1", APIKeyEnv: "REASONIX_TEST_KEY"},
	}
	cfg.Agent.PlannerModel = "prov-a"
	cfg.Desktop.ProviderAccess = []string{"prov-a", "prov-b"}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	ctrl := control.New(control.Options{Label: "old"})
	defer ctrl.Close()
	app := NewApp()
	tab := &WorkspaceTab{ID: "tab_a", Scope: "global", Ctrl: ctrl, Label: "prov-a/model-a1", Ready: true, model: "prov-a/model-a1"}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	if err := app.DeleteProvider("prov-a"); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	if _, ok := got.Provider("prov-a"); ok {
		t.Fatal("prov-a should be removed")
	}
	if got.DefaultModel != "prov-b" || got.Agent.PlannerModel != "prov-b" {
		t.Fatalf("model refs after delete = default:%q planner:%q, want prov-b", got.DefaultModel, got.Agent.PlannerModel)
	}
	if providerAccessSet(got.Desktop.ProviderAccess)["prov-a"] {
		t.Fatalf("provider access still contains prov-a: %+v", got.Desktop.ProviderAccess)
	}
	if tab.model != "prov-a/model-a1" || tab.Label != "prov-a/model-a1" {
		t.Fatalf("saving deletion changed current identity: model:%q label:%q", tab.model, tab.Label)
	}
	if tab.Ctrl != ctrl {
		t.Fatal("saving deletion closed the current controller")
	}
}

// assertTabBuildSuperseded checks that the startup build registered before the
// mutation (generation) can no longer install its controller and that its
// build context was cancelled.
func assertTabBuildSuperseded(t *testing.T, app *App, tab *WorkspaceTab, generation uint64, buildCtx context.Context) {
	t.Helper()
	app.mu.Lock()
	superseded := app.tabBuildSupersededLocked(tab, generation)
	app.mu.Unlock()
	if !superseded {
		t.Fatal("in-flight startup build was not superseded; finishing it would reinstall a stale controller")
	}
	select {
	case <-buildCtx.Done():
	default:
		t.Fatal("in-flight startup build context was not cancelled")
	}
	if tab.buildCancel != nil {
		t.Fatal("build cancel was not cleared")
	}
}

func TestDeleteProviderLeavesStartupPublicationToVersionFence(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "REASONIX_TEST_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "prov-b/model-b1"
	cfg.Providers = []config.ProviderEntry{
		{Name: "prov-a", Kind: "openai", BaseURL: "https://a.example.com", Model: "model-a1", APIKeyEnv: "REASONIX_TEST_KEY"},
		{Name: "prov-b", Kind: "openai", BaseURL: "https://b.example.com", Model: "model-b1", APIKeyEnv: "REASONIX_TEST_KEY"},
	}
	cfg.Desktop.ProviderAccess = []string{"prov-a", "prov-b"}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := NewApp()
	// Model the async startup build still being in flight for the affected
	// tab: no controller yet, a live generation, a cancellable build context.
	buildCtx, buildCancel := context.WithCancel(context.Background())
	tab := &WorkspaceTab{
		ID:              "tab_a",
		Scope:           "global",
		model:           "prov-a/model-a1",
		buildGeneration: 1,
		buildCancel:     buildCancel,
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	if err := app.DeleteProvider("prov-a"); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	if tab.buildGeneration != 1 || buildCtx.Err() != nil || tab.model != "prov-a/model-a1" {
		t.Fatal("saving deletion changed an in-flight startup before its publication fence")
	}
	buildCancel()
}

func TestRemoveBuiltInProviderAccessLeavesStartupPublicationToVersionFence(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "REASONIX_TEST_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "prov-b/model-b1"
	cfg.Providers = []config.ProviderEntry{
		{Name: "deepseek", Kind: "openai", BaseURL: "https://api.deepseek.com", Model: "deepseek-chat", APIKeyEnv: "REASONIX_TEST_KEY"},
		{Name: "prov-b", Kind: "openai", BaseURL: "https://b.example.com", Model: "model-b1", APIKeyEnv: "REASONIX_TEST_KEY"},
	}
	cfg.Desktop.ProviderAccess = []string{"deepseek", "prov-b"}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := NewApp()
	buildCtx, buildCancel := context.WithCancel(context.Background())
	tab := &WorkspaceTab{
		ID:              "tab_ds",
		Scope:           "global",
		model:           "deepseek/deepseek-chat",
		buildGeneration: 1,
		buildCancel:     buildCancel,
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	if err := app.RemoveProviderAccess("deepseek"); err != nil {
		t.Fatalf("RemoveProviderAccess: %v", err)
	}
	if tab.buildGeneration != 1 || buildCtx.Err() != nil || tab.model != "deepseek/deepseek-chat" {
		t.Fatal("saving access removal changed an in-flight startup before its publication fence")
	}
	buildCancel()
}

func TestClearActiveSessionRuntimeSupersedesInFlightStartupBuild(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old"}
	cfg.Providers = []config.ProviderEntry{{
		Name:      "old",
		Kind:      "openai",
		BaseURL:   "https://example.invalid/v1",
		Model:     "old-model",
		APIKeyEnv: "OLD_MODEL_KEY",
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(dir, "clear-runtime-in-flight.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0o644); err != nil {
		t.Fatalf("write placeholder session: %v", err)
	}

	oldSession := agent.NewSession("old system prompt")
	oldExec := agent.New(nil, nil, oldSession, agent.Options{}, event.Discard)
	oldCtrl := control.New(control.Options{Executor: oldExec, SessionDir: dir, SessionPath: sessionPath, Label: "old", Sink: event.Discard})

	app := NewApp()
	// A runtime is attached while an older async build is still in flight
	// (e.g. attached via topic activation); destroying the session must
	// invalidate that build so it cannot resurrect the destroyed session.
	buildCtx, buildCancel := context.WithCancel(context.Background())
	tab := &WorkspaceTab{
		ID:              "tab_clear",
		Scope:           "global",
		SessionPath:     sessionPath,
		model:           "old/old-model",
		Ready:           true,
		Ctrl:            oldCtrl,
		buildGeneration: 1,
		buildCancel:     buildCancel,
		disabledMCP:     map[string]ServerView{},
	}
	tab.sink = &tabEventSink{tabID: tab.ID, app: app}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	t.Cleanup(tab.releaseSessionLease)

	if _, err := app.clearActiveSessionRuntime(tab, oldCtrl); err != nil {
		t.Fatalf("clearActiveSessionRuntime: %v", err)
	}
	if tab.Ctrl == nil || tab.Ctrl == oldCtrl {
		t.Fatalf("clear did not install a fresh controller (ctrl=%v)", tab.Ctrl)
	}
	defer tab.Ctrl.Close()
	assertTabBuildSuperseded(t, app, tab, 1, buildCtx)
}

func TestClearActiveSessionRuntimeReleasesResourcesWhenTabReplaced(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old"}
	cfg.Providers = []config.ProviderEntry{{
		Name:      "old",
		Kind:      "openai",
		BaseURL:   "https://example.invalid/v1",
		Model:     "old-model",
		APIKeyEnv: "OLD_MODEL_KEY",
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(dir, "clear-runtime-replaced-tab.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0o644); err != nil {
		t.Fatalf("write placeholder session: %v", err)
	}

	oldSession := agent.NewSession("old system prompt")
	oldExec := agent.New(nil, nil, oldSession, agent.Options{}, event.Discard)
	oldCtrl := control.New(control.Options{Executor: oldExec, SessionDir: dir, SessionPath: sessionPath, Label: "old", Sink: event.Discard})

	app := NewApp()
	tab := &WorkspaceTab{
		ID:          "tab_replaced",
		Scope:       "global",
		SessionPath: sessionPath,
		model:       "old/old-model",
		Ready:       true,
		Ctrl:        oldCtrl,
		disabledMCP: map[string]ServerView{},
	}
	tab.sink = &tabEventSink{tabID: tab.ID, app: app}
	// The tab entry now points at a replacement struct (the tab was closed and
	// reopened while the clear ran off-lock), so the swap must not apply.
	replacement := &WorkspaceTab{ID: tab.ID, Scope: "global"}
	app.tabs = map[string]*WorkspaceTab{tab.ID: replacement}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	t.Cleanup(tab.releaseSessionLease)

	_, err := app.clearActiveSessionRuntime(tab, oldCtrl)
	if err == nil || !strings.Contains(err.Error(), "changed while clearing") {
		t.Fatalf("clearActiveSessionRuntime error = %v, want tab-changed error", err)
	}
	if replacement.Ctrl != nil {
		t.Fatalf("replacement tab controller = %v, want untouched nil", replacement.Ctrl)
	}
	if tab.Ctrl != oldCtrl {
		t.Fatalf("replaced tab controller = %v, want left on the destroyed runtime", tab.Ctrl)
	}
	if key := tab.sessionLeaseRuntimeKey(); key != "" {
		t.Fatalf("replaced tab still holds a session lease for %q; the fresh lease leaked", key)
	}
	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Fatalf("old session artifacts were not destroyed (stat err=%v)", err)
	}
}

func TestDeleteProviderPreservesRunningAffectedTab(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "REASONIX_TEST_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "prov-a/model-a1"
	cfg.Providers = []config.ProviderEntry{
		{Name: "prov-a", Kind: "openai", BaseURL: "https://a.example.com", Model: "model-a1", APIKeyEnv: "REASONIX_TEST_KEY"},
		{Name: "prov-b", Kind: "openai", BaseURL: "https://b.example.com", Model: "model-b1", APIKeyEnv: "REASONIX_TEST_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Runner: runner}), "prov-a/model-a1")
	ctrl := app.activeCtrl()
	ctrl.Submit("work")
	<-runner.started

	err := app.DeleteProvider("prov-a")
	if err != nil || app.activeCtrl() != ctrl || !ctrl.RuntimeStatus().Running {
		t.Fatalf("DeleteProvider interrupted accepted work: %v", err)
	}
	if _, ok := config.LoadForEdit(config.UserConfigPath()).Provider("prov-a"); ok {
		t.Fatal("provider deletion was not persisted during the run")
	}

	close(runner.release)
	waitNotRunning(t, ctrl)
	ctrl.Close()
}

func TestDeleteProviderDoesNotWaitForRuntimeReconstruction(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "REASONIX_TEST_KEY", "sk-test")
	cfg := config.Default()
	cfg.DefaultModel = "prov-a/model-a1"
	cfg.Providers = []config.ProviderEntry{
		{Name: "prov-a", Kind: "openai", BaseURL: "https://a.example.com", Model: "model-a1", APIKeyEnv: "REASONIX_TEST_KEY"},
		{Name: "prov-b", Kind: "openai", BaseURL: "https://b.example.com", Model: "model-b1", APIKeyEnv: "REASONIX_TEST_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Runner: runner}), "prov-a/model-a1")
	ctrl := app.activeCtrl()
	app.runtimeRebuildMu.Lock()
	defer app.runtimeRebuildMu.Unlock()
	ctrl.Submit("work")
	<-runner.started
	done := make(chan error, 1)
	go func() { done <- app.DeleteProvider("prov-a") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("saving provider deletion waited for runtime reconstruction")
	}
	if app.activeCtrl() != ctrl || !ctrl.RuntimeStatus().Running {
		t.Fatal("saving deletion interrupted accepted work")
	}
	if _, ok := config.LoadForEdit(config.UserConfigPath()).Provider("prov-a"); ok {
		t.Fatal("deletion was not committed")
	}
	close(runner.release)
	waitNotRunning(t, ctrl)
	ctrl.Close()
}

func TestDeleteProviderPreservesAffectedTabSharedHostReference(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "REASONIX_TEST_KEY", "sk-test")
	cfg := config.Default()
	cfg.DefaultModel = "prov-a/model-a1"
	cfg.Providers = []config.ProviderEntry{
		{Name: "prov-a", Kind: "openai", BaseURL: "https://a.example.com", Model: "model-a1", APIKeyEnv: "REASONIX_TEST_KEY"},
		{Name: "prov-b", Kind: "openai", BaseURL: "https://b.example.com", Model: "model-b1", APIKeyEnv: "REASONIX_TEST_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := NewApp()
	hostKey := "provider-shared-host"
	host := app.acquireSharedHost(hostKey)
	ctrl := control.New(control.Options{Host: host})
	tab := &WorkspaceTab{
		ID: "affected", Scope: "global", Ready: true, Ctrl: ctrl,
		model: "prov-a/model-a1", SharedHostKey: hostKey, disabledMCP: map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	if err := app.DeleteProvider("prov-a"); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	if tab.SharedHostKey != hostKey || tab.Ctrl != ctrl {
		t.Fatal("saving deletion released the current runtime's shared host")
	}
	app.sharedHostsMu.Lock()
	_, retained := app.sharedHosts[hostKey]
	app.sharedHostsMu.Unlock()
	if !retained {
		t.Fatal("provider deletion released an owned shared host reference")
	}
	ctrl.Close()
	app.releaseSharedHost(hostKey)
}

func TestRemoveBuiltInProviderAccessPreservesAffectedTabSharedHostReference(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "REASONIX_TEST_KEY", "sk-test")
	cfg := config.Default()
	cfg.DefaultModel = "deepseek/deepseek-chat"
	cfg.Providers = []config.ProviderEntry{
		{Name: "deepseek", Kind: "openai", BaseURL: "https://api.deepseek.com", Model: "deepseek-chat", APIKeyEnv: "REASONIX_TEST_KEY"},
		{Name: "prov-b", Kind: "openai", BaseURL: "https://b.example.com", Model: "model-b1", APIKeyEnv: "REASONIX_TEST_KEY"},
	}
	cfg.Desktop.ProviderAccess = []string{"deepseek", "prov-b"}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := NewApp()
	hostKey := "provider-access-shared-host"
	host := app.acquireSharedHost(hostKey)
	ctrl := control.New(control.Options{Host: host})
	tab := &WorkspaceTab{
		ID: "affected", Scope: "global", Ready: true, Ctrl: ctrl,
		model: "deepseek/deepseek-chat", SharedHostKey: hostKey, disabledMCP: map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	if err := app.RemoveProviderAccess("deepseek"); err != nil {
		t.Fatalf("RemoveProviderAccess: %v", err)
	}
	if tab.SharedHostKey != hostKey || tab.Ctrl != ctrl {
		t.Fatal("saving access removal released the current runtime's shared host")
	}
	app.sharedHostsMu.Lock()
	_, retained := app.sharedHosts[hostKey]
	app.sharedHostsMu.Unlock()
	if !retained {
		t.Fatal("provider access removal released an owned shared host reference")
	}
	ctrl.Close()
	app.releaseSharedHost(hostKey)
}

func TestDeleteProviderPreservesAffectedBackgroundJobs(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "REASONIX_TEST_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "prov-a/model-a1"
	cfg.Providers = []config.ProviderEntry{
		{Name: "prov-a", Kind: "openai", BaseURL: "https://a.example.com", Model: "model-a1", APIKeyEnv: "REASONIX_TEST_KEY"},
		{Name: "prov-b", Kind: "openai", BaseURL: "https://b.example.com", Model: "model-b1", APIKeyEnv: "REASONIX_TEST_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "provider-job.jsonl")
	jm := jobs.NewManager(event.Discard)
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: path, Label: "test", Jobs: jm})
	defer ctrl.Close()
	app := NewApp()
	app.setTestCtrl(ctrl, "prov-a/model-a1")
	jm.StartForSession(agent.BranchID(path), "bash", "provider job", func(ctx context.Context, _ io.Writer) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})

	err := app.DeleteProvider("prov-a")
	if err != nil || !controllerHasActiveRuntimeWork(ctrl) {
		t.Fatalf("DeleteProvider interrupted background work: %v", err)
	}
	if _, ok := config.LoadForEdit(config.UserConfigPath()).Provider("prov-a"); ok {
		t.Fatal("provider deletion was not persisted")
	}
}

func TestDeleteProviderPreservesUnaffectedBackgroundJobsWhenSavingConfig(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "REASONIX_TEST_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "prov-b/model-b1"
	cfg.Providers = []config.ProviderEntry{
		{Name: "prov-a", Kind: "openai", BaseURL: "https://a.example.com", Model: "model-a1", APIKeyEnv: "REASONIX_TEST_KEY"},
		{Name: "prov-b", Kind: "openai", BaseURL: "https://b.example.com", Model: "model-b1", APIKeyEnv: "REASONIX_TEST_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := NewApp()
	app.ctx = context.Background()
	app.setTestCtrl(newBackgroundJobController(t, "provider-unaffected-job"), "prov-b/model-b1")

	err := app.DeleteProvider("prov-a")
	if err != nil || !controllerHasActiveRuntimeWork(app.activeCtrl()) {
		t.Fatalf("DeleteProvider interrupted unrelated background work: %v", err)
	}
	if _, ok := config.LoadForEdit(config.UserConfigPath()).Provider("prov-a"); ok {
		t.Fatal("provider deletion was not persisted")
	}
}

func TestRemoveBuiltInProviderAccessPreservesBackgroundJobsWhenSavingConfig(t *testing.T) {
	isolateDesktopUserDirs(t)
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
default_model = "mimo-pro/mimo-v2.5-pro"

[desktop]
provider_access = ["deepseek-flash", "mimo-pro"]

[[providers]]
name = "deepseek-flash"
kind = "openai"
base_url = "https://api.deepseek.com"
models = ["deepseek-v4-flash", "deepseek-v4-pro"]
default = "deepseek-v4-flash"
api_key_env = "DEEPSEEK_API_KEY"

[[providers]]
name = "mimo-pro"
kind = "openai"
base_url = "https://token-plan-cn.xiaomimimo.com/v1"
model = "mimo-v2.5-pro"
api_key_env = "MIMO_API_KEY"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	app := NewApp()
	app.ctx = context.Background()
	app.setTestCtrl(newBackgroundJobController(t, "provider-access-unaffected-job"), "mimo-token-plan/mimo-v2.5-pro")

	err := app.RemoveProviderAccess("deepseek")
	if err != nil || !controllerHasActiveRuntimeWork(app.activeCtrl()) {
		t.Fatalf("RemoveProviderAccess interrupted background work: %v", err)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	access := providerAccessSet(cfg.Desktop.ProviderAccess)
	if access["deepseek"] || access["deepseek-flash"] {
		t.Fatalf("provider access removal was not persisted: %+v", cfg.Desktop.ProviderAccess)
	}
}

func TestConnectKeySavesWhileBackgroundJobKeepsItsController(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("DEEPSEEK_API_KEY", "")
	os.Unsetenv("DEEPSEEK_API_KEY")
	oldFetch := connectKeyBalanceFetch
	connectKeyBalanceFetch = func(context.Context, *http.Client, string, string) (*billing.Balance, error) {
		return &billing.Balance{Available: true}, nil
	}
	t.Cleanup(func() { connectKeyBalanceFetch = oldFetch })

	app := NewApp()
	app.ctx = context.Background()
	app.setTestCtrl(newBackgroundJobController(t, "connect-key-job"), "deepseek-flash/deepseek-v4-flash")
	oldCtrl := app.activeCtrl()

	_, err := app.ConnectKey("sk-test")
	if err != nil {
		t.Fatalf("ConnectKey with background job: %v", err)
	}
	p, ok := config.LoadForEdit(config.UserConfigPath()).Provider("deepseek")
	if !ok || !p.Configured() || app.activeCtrl() != oldCtrl {
		t.Fatal("key save must configure the connection and preserve the background runtime")
	}
}

func TestConnectKeyRestoresDeepSeekProviderAccess(t *testing.T) {
	isolateDesktopUserDirs(t)
	cfg := config.Default()
	cfg.DefaultModel = "custom/custom-model"
	cfg.Desktop.ProviderAccess = []string{"custom"}
	cfg.Providers = []config.ProviderEntry{{
		Name: "custom", Kind: "openai", BaseURL: "https://models.example.invalid/v1",
		Model: "custom-model", APIKeyEnv: "CUSTOM_API_KEY",
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save custom provider config: %v", err)
	}

	oldFetch := connectKeyBalanceFetch
	connectKeyBalanceFetch = func(context.Context, *http.Client, string, string) (*billing.Balance, error) {
		return &billing.Balance{Available: true}, nil
	}
	t.Cleanup(func() { connectKeyBalanceFetch = oldFetch })

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	app.setTestCtrl(control.New(control.Options{Label: "custom"}), "custom/custom-model")
	defer func() {
		if ctrl := app.activeCtrl(); ctrl != nil {
			ctrl.Close()
		}
	}()
	if _, err := app.ConnectKey("sk-test"); err != nil {
		t.Fatalf("ConnectKey: %v", err)
	}

	got := config.LoadForEditWithoutCredentials(config.UserConfigPath())
	if !providerAccessSet(got.Desktop.ProviderAccess)["deepseek"] {
		t.Fatalf("provider_access = %v, want DeepSeek restored", got.Desktop.ProviderAccess)
	}
	if _, ok := got.Provider("deepseek"); !ok {
		t.Fatal("DeepSeek provider template should be restored")
	}
	if app.NeedsOnboarding() {
		t.Fatal("restored DeepSeek access and saved key should satisfy onboarding")
	}
}

func TestConnectKeyFreshInstallUsesDeepSeekChatAndIndependentSearchDefaults(t *testing.T) {
	isolateDesktopUserDirs(t)
	oldFetch := connectKeyBalanceFetch
	connectKeyBalanceFetch = func(context.Context, *http.Client, string, string) (*billing.Balance, error) {
		return &billing.Balance{Available: true}, nil
	}
	t.Cleanup(func() { connectKeyBalanceFetch = oldFetch })

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	app.setTestCtrl(control.New(control.Options{Label: "fresh-install"}), "deepseek-flash/deepseek-v4-flash")
	workspace := t.TempDir()
	app.tabs["test"].WorkspaceRoot = workspace
	defer func() {
		if ctrl := app.activeCtrl(); ctrl != nil {
			ctrl.Close()
		}
	}()

	if _, err := app.ConnectKey("sk-test"); err != nil {
		t.Fatalf("ConnectKey: %v", err)
	}
	cfg, err := config.LoadForRootReadOnly(workspace)
	if err != nil {
		t.Fatalf("load fresh-install config: %v", err)
	}
	entry, ok := cfg.ResolveModel(cfg.DefaultModel)
	if !ok {
		t.Fatalf("default model %q did not resolve", cfg.DefaultModel)
	}
	if entry.Kind != "openai" || entry.BaseURL != "https://api.deepseek.com" ||
		entry.Thinking != "enabled" || !config.EffectiveIndependentWebSearch(entry) || !config.EffectiveVision(entry) {
		t.Fatalf("fresh-install DeepSeek entry = %+v; want Chat Completions, thinking, independent search, and native image input", entry)
	}
	if app.NeedsOnboarding() {
		t.Fatal("fresh-install onboarding should close after the validated DeepSeek key is stored")
	}
}

func TestBalanceForTabUsesDesktopPricingCurrency(t *testing.T) {
	isolateDesktopUserDirs(t)
	cfg := config.Default()
	cfg.Desktop.Currency = "USD"
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save USD desktop currency: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"70.16"},{"currency":"USD","total_balance":"9.82"}]}`)
	}))
	defer srv.Close()

	app := NewApp()
	app.ctx = context.Background()
	ctrl := control.New(control.Options{BalanceURL: srv.URL, BalanceClient: srv.Client()})
	t.Cleanup(ctrl.Close)
	app.setTestCtrl(ctrl, "deepseek/deepseek-v4-flash")

	got := app.BalanceForTab("test")
	// Prefer the matching USD wallet exactly; no FX approximation is used.
	if !got.Available || got.Err != "" {
		t.Fatalf("USD desktop balance = %+v, want available", got)
	}
	if !strings.Contains(got.Display, "9.82") && !strings.Contains(got.Display, "$9.82") {
		t.Fatalf("USD desktop balance display = %q, want USD 9.82", got.Display)
	}
}

func TestConnectKeyRebuildLeaseHeldKeepsCurrentController(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv(onboardingKeyEnv, "")
	os.Unsetenv(onboardingKeyEnv)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")

	oldFetch := connectKeyBalanceFetch
	connectKeyBalanceFetch = func(context.Context, *http.Client, string, string) (*billing.Balance, error) {
		return &billing.Balance{Available: true}, nil
	}
	t.Cleanup(func() { connectKeyBalanceFetch = oldFetch })

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old"}
	cfg.Providers = []config.ProviderEntry{
		{Name: "old", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "old-model", APIKeyEnv: "OLD_MODEL_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(dir, "externally-leased-connect-key.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0o644); err != nil {
		t.Fatalf("write placeholder session: %v", err)
	}
	externalLease, err := agent.TryAcquireSessionLease(sessionPath)
	if err != nil {
		t.Fatalf("TryAcquireSessionLease: %v", err)
	}
	defer externalLease.Release()

	oldSession := agent.NewSession("old system prompt")
	oldSession.Add(provider.Message{Role: provider.RoleUser, Content: "hello"})
	oldExec := agent.New(nil, nil, oldSession, agent.Options{}, event.Discard)
	oldCtrl := control.New(control.Options{Executor: oldExec, SessionDir: dir, SessionPath: sessionPath, Label: "old", Sink: event.Discard})
	defer oldCtrl.Close()

	app := NewApp()
	app.ctx = context.Background()
	tab := &WorkspaceTab{
		ID:          "tab_connect",
		Scope:       "global",
		SessionPath: sessionPath,
		Ready:       true,
		model:       "old/old-model",
		Ctrl:        oldCtrl,
		sink:        &tabEventSink{tabID: "tab_connect", app: app},
		disabledMCP: map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	warning, err := app.ConnectKey("sk-test")
	if err != nil {
		t.Fatalf("ConnectKey: %v", err)
	}
	if warning != "" {
		t.Fatalf("ConnectKey warning = %q; saving does not acquire a runtime lease", warning)
	}
	if tab.Ctrl != oldCtrl {
		t.Fatalf("tab controller changed after failed connect-key rebuild")
	}
	if tab.StartupErr != "" {
		t.Fatalf("tab startup error = %q, want unchanged current session", tab.StartupErr)
	}
	p, ok := config.LoadForEdit(config.UserConfigPath()).Provider("deepseek")
	if !ok || !p.Configured() {
		t.Fatal("onboarding key should be persisted under the new connection reference")
	}
}

func TestMigrateDesktopPreferencesDoesNotOverwriteExistingConfig(t *testing.T) {
	isolateDesktopUserDirs(t)

	userCfg := config.LoadForEdit(config.UserConfigPath())
	if err := userCfg.SetDesktopLanguage("en"); err != nil {
		t.Fatalf("set desktop language: %v", err)
	}
	if err := userCfg.SetDesktopLayoutStyle("workbench"); err != nil {
		t.Fatalf("set desktop layout style: %v", err)
	}
	if err := userCfg.SetDesktopAppearance("dark", "graphite"); err != nil {
		t.Fatalf("set desktop appearance: %v", err)
	}
	if err := userCfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save user config: %v", err)
	}

	if err := NewApp().MigrateDesktopPreferences("zh", "light", "glacier"); err != nil {
		t.Fatalf("migrate desktop preferences: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	if got.DesktopLanguage() != "en" || got.DesktopLayoutStyle() != "workbench" || got.DesktopTheme() != "dark" || got.DesktopThemeStyle() != "graphite" {
		t.Fatalf("desktop prefs after migration = lang:%q layout:%q theme:%q style:%q, want existing config preserved", got.DesktopLanguage(), got.DesktopLayoutStyle(), got.DesktopTheme(), got.DesktopThemeStyle())
	}
}

func TestSetEffortRebuildsController(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	old := control.New(control.Options{Label: "old-controller"})
	app.setTestCtrl(old, "deepseek-flash/deepseek-v4-flash")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	if err := app.SetEffort("max"); err != nil {
		t.Fatalf("SetEffort(max): %v", err)
	}
	if c := app.activeCtrl(); c == nil {
		t.Fatal("SetEffort should leave a rebuilt controller")
	}
	if c := app.activeCtrl(); c == old {
		t.Fatal("SetEffort should rebuild the active controller so the provider sees the new effort")
	}
	if got := app.Effort().Current; got != "max" {
		t.Fatalf("Effort current = %q, want max", got)
	}
}

func TestSetEffortMigratesStaleOfficialDeepSeekTabModel(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "DEEPSEEK_API_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "deepseek/deepseek-v4-flash"
	cfg.Desktop.ProviderAccess = []string{"deepseek"}
	cfg.Providers = []config.ProviderEntry{{
		Name:      "deepseek",
		Kind:      "openai",
		BaseURL:   "https://api.deepseek.com",
		Model:     "glm-5",
		APIKeyEnv: "DEEPSEEK_API_KEY",
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	old := control.New(control.Options{Label: "old-controller"})
	app.setTestCtrl(old, "deepseek-flash/deepseek-v4-flash")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	if err := app.SetEffort("max"); err != nil {
		t.Fatalf("SetEffort(max): %v", err)
	}
	tab := app.activeTab()
	if tab == nil {
		t.Fatal("active tab missing")
	}
	if tab.model != "deepseek/deepseek-v4-flash" {
		t.Fatalf("tab model = %q, want migrated official ref", tab.model)
	}
}

func captureTabNotices(app *App, tab *WorkspaceTab) *[]string {
	var notices []string
	if tab.sink == nil {
		tab.sink = &tabEventSink{tabID: tab.ID, app: app, ctx: context.Background()}
	}
	tab.sink.SetBotSink(event.FuncSink(func(e event.Event) {
		if e.Kind == event.Notice && strings.TrimSpace(e.Text) != "" {
			notices = append(notices, e.Text)
		}
	}))
	return &notices
}

func assertDeprecatedExecutionModeNoop(t *testing.T, app *App, tab *WorkspaceTab, old control.SessionAPI, notices []string) {
	t.Helper()
	if tab == nil {
		t.Fatal("tab missing")
	}
	if tab.Ctrl == nil || tab.Ctrl != old {
		t.Fatalf("controller identity changed: got %p want %p", tab.Ctrl, old)
	}
	if got := old.AgentPreset(); got != boot.AgentPresetStandard {
		t.Fatalf("controller AgentPreset = %q, want standard (light folds)", got)
	}
	if got := currentTabTokenMode(tab); got != boot.TokenModeFull {
		t.Fatalf("token mode = %q, want full", got)
	}
	meta := app.MetaForTab(tab.ID)
	if meta.TokenMode != boot.TokenModeFull || meta.AgentPreset != boot.AgentPresetStandard {
		t.Fatalf("meta token/preset = %q/%q, want full/standard", meta.TokenMode, meta.AgentPreset)
	}
}

func assertSetTokenModeDidNotPersistLiveModes(t *testing.T) {
	t.Helper()
	for _, entry := range loadTabsFile().Tabs {
		if entry.TokenMode == "economy" || entry.TokenMode == "light" {
			t.Fatalf("SetTokenMode persisted folded mode %q", entry.TokenMode)
		}
		if entry.AgentPreset == "light" || entry.AgentPreset == "balanced" {
			t.Fatalf("SetTokenMode persisted non-floor preset %q", entry.AgentPreset)
		}
	}
}

func assertPinnedCompatPersisted(t *testing.T, app *App, tab *WorkspaceTab) {
	t.Helper()
	app.persistTabTokenMode(tab)
	saved := loadTabsFile()
	if len(saved.Tabs) != 1 {
		t.Fatalf("saved tabs = %+v, want 1", saved.Tabs)
	}
	if saved.Tabs[0].TokenMode != boot.TokenModeFull {
		t.Fatalf("saved compat token = %q, want full", saved.Tabs[0].TokenMode)
	}
}

func TestSetTokenModeRebuildsController(t *testing.T) {
	// Name kept for history; SetTokenMode is a deprecated no-op wrapper.
	isolateDesktopUserDirs(t)

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	old := control.New(control.Options{Label: "old-controller"})
	app.setTestCtrl(old, "deepseek-flash/deepseek-v4-flash")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()
	tab := app.activeTab()
	notices := captureTabNotices(app, tab)

	if err := app.SetTokenMode("economy"); err != nil {
		t.Fatalf("SetTokenMode(economy): %v", err)
	}
	assertDeprecatedExecutionModeNoop(t, app, tab, old, *notices)
	assertSetTokenModeDidNotPersistLiveModes(t)
	assertPinnedCompatPersisted(t, app, tab)
}

func TestSetTokenModeDeliveryRebuildsAndPersistsProfile(t *testing.T) {
	// SetTokenMode(delivery) now writes the session quality floor in place.
	isolateDesktopUserDirs(t)

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	old := control.New(control.Options{Label: "old-controller"})
	app.setTestCtrl(old, "deepseek-flash/deepseek-v4-flash")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()
	tab := app.activeTab()
	notices := captureTabNotices(app, tab)

	if err := app.SetTokenMode(boot.TokenModeDelivery); err != nil {
		t.Fatalf("SetTokenMode(delivery): %v", err)
	}
	if tab.Ctrl == nil || tab.Ctrl != old {
		t.Fatalf("controller identity changed: got %p want %p", tab.Ctrl, old)
	}
	if got := old.QualityFloor(); got != control.QualityFloorDelivery {
		t.Fatalf("controller QualityFloor = %q, want delivery", got)
	}
	if got := tab.qualityFloor; got != control.QualityFloorDelivery {
		t.Fatalf("tab qualityFloor = %q, want delivery", got)
	}

	if err := app.SetTokenMode(boot.TokenModeFull); err != nil {
		t.Fatalf("SetTokenMode(full): %v", err)
	}
	assertDeprecatedExecutionModeNoop(t, app, tab, old, *notices)
	assertPinnedCompatPersisted(t, app, tab)
}

func TestSetTokenModeReusesCurrentSessionLease(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old"}
	cfg.Providers = []config.ProviderEntry{
		{Name: "old", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "old-model", APIKeyEnv: "OLD_MODEL_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	session := agent.NewSession("old system prompt")
	session.Add(provider.Message{Role: provider.RoleUser, Content: "hello"})
	exec := agent.New(nil, nil, session, agent.Options{}, event.Discard)
	path := filepath.Join(dir, "leased-token-mode-switch.jsonl")
	oldCtrl := control.New(control.Options{Executor: exec, SessionDir: dir, SessionPath: path, Label: "old", Sink: event.Discard})

	app := NewApp()
	app.ctx = context.Background()
	tab := &WorkspaceTab{
		ID:          "tab_a",
		Scope:       "global",
		Ready:       true,
		model:       "old/old-model",
		Ctrl:        oldCtrl,
		sink:        &tabEventSink{tabID: "tab_a", app: app},
		disabledMCP: map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	t.Cleanup(func() {
		if tab.Ctrl != nil {
			tab.Ctrl.Close()
		}
		tab.releaseSessionLease()
	})

	if err := tab.ensureSessionLease(path); err != nil {
		t.Fatalf("ensureSessionLease: %v", err)
	}
	notices := captureTabNotices(app, tab)
	if err := app.SetTokenModeForTab(tab.ID, "economy"); err != nil {
		t.Fatalf("SetTokenModeForTab: %v", err)
	}
	assertDeprecatedExecutionModeNoop(t, app, tab, oldCtrl, *notices)
	if tab.sessionLease == nil || sessionRuntimeKey(tab.sessionLease.Path()) != sessionRuntimeKey(path) {
		t.Fatalf("session lease path = %q, want %q", tab.currentSessionPath(), path)
	}
	history := tab.Ctrl.History()
	if len(history) < 2 || history[1].Role != provider.RoleUser || history[1].Content != "hello" {
		t.Fatalf("carried history = %+v, want original user message", history)
	}
}

func TestSetTokenModeLeaseHeldKeepsCurrentController(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old"}
	cfg.Providers = []config.ProviderEntry{
		{Name: "old", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "old-model", APIKeyEnv: "OLD_MODEL_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "externally-leased-token-mode-switch.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write placeholder session: %v", err)
	}
	externalLease, err := agent.TryAcquireSessionLease(path)
	if err != nil {
		t.Fatalf("TryAcquireSessionLease: %v", err)
	}
	defer externalLease.Release()

	session := agent.NewSession("old system prompt")
	session.Add(provider.Message{Role: provider.RoleUser, Content: "hello"})
	exec := agent.New(nil, nil, session, agent.Options{}, event.Discard)
	oldCtrl := control.New(control.Options{Executor: exec, SessionDir: dir, SessionPath: path, Label: "old", Sink: event.Discard})
	defer oldCtrl.Close()

	app := NewApp()
	app.ctx = context.Background()
	tab := &WorkspaceTab{
		ID:          "tab_a",
		Scope:       "global",
		Ready:       true,
		model:       "old/old-model",
		Ctrl:        oldCtrl,
		sink:        &tabEventSink{tabID: "tab_a", app: app},
		disabledMCP: map[string]ServerView{},
	}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID

	// Deprecated wrapper must not re-acquire the session lease, so an
	// externally held lease does not block the call or replace the controller.
	notices := captureTabNotices(app, tab)
	if err := app.SetTokenModeForTab(tab.ID, "economy"); err != nil {
		t.Fatalf("SetTokenModeForTab: %v", err)
	}
	assertDeprecatedExecutionModeNoop(t, app, tab, oldCtrl, *notices)
	meta := app.MetaForTab(tab.ID)
	if !meta.Ready || meta.Runtime.Phase != sessionRuntimeReady {
		t.Fatalf("deprecated mode call disabled current runtime: ready=%v phase=%q", meta.Ready, meta.Runtime.Phase)
	}
}

func TestSetTokenModeMigratesStaleOfficialDeepSeekTabModel(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "DEEPSEEK_API_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "deepseek/deepseek-v4-flash"
	cfg.Desktop.ProviderAccess = []string{"deepseek"}
	cfg.Providers = []config.ProviderEntry{{
		Name:      "deepseek",
		Kind:      "openai",
		BaseURL:   "https://api.deepseek.com",
		Model:     "glm-5",
		APIKeyEnv: "DEEPSEEK_API_KEY",
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	old := control.New(control.Options{Label: "old-controller"})
	app.setTestCtrl(old, "deepseek-flash/deepseek-v4-flash")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	tab := app.activeTab()
	notices := captureTabNotices(app, tab)
	if err := app.SetTokenMode("economy"); err != nil {
		t.Fatalf("SetTokenMode(economy): %v", err)
	}
	if tab == nil {
		t.Fatal("active tab missing")
	}
	// SetTokenMode does not rebuild, so stale model aliases stay put
	// (migration still runs on model/effort rebuilds).
	if tab.model != "deepseek-flash/deepseek-v4-flash" {
		t.Fatalf("tab model = %q, want unchanged stale ref without rebuild", tab.model)
	}
	assertDeprecatedExecutionModeNoop(t, app, tab, old, *notices)
}

func TestMetaForTabReportsImageInputCapability(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "CUSTOM_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "custom/text-only"
	cfg.Desktop.ProviderAccess = []string{"custom"}
	cfg.Providers = []config.ProviderEntry{{
		Name:         "custom",
		Kind:         "openai",
		BaseURL:      "https://example.invalid/v1",
		APIKeyEnv:    "CUSTOM_KEY",
		Models:       []string{"text-only", "vision-pro"},
		VisionModels: []string{"vision-pro"},
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	app.setTestCtrl(control.New(control.Options{Label: "custom/text-only"}), "custom/text-only")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	if got := app.Meta().ImageInputEnabled; got {
		t.Fatal("text-only meta should disable image input")
	}
	if err := app.SetModel("custom/vision-pro"); err != nil {
		t.Fatalf("SetModel(custom/vision-pro): %v", err)
	}
	// ImageInputEnabled is served from the per-tab cache; the model change
	// invalidates it and a background refresh repopulates it (tab:meta).
	waitForMetaImageInput(t, app, true)
}

// waitForMetaImageInput polls until the cached image-input capability reaches
// the expected value. MetaForTab serves the background-refreshed cache, so the
// value flips asynchronously after a model/settings change.
func waitForMetaImageInput(t *testing.T, app *App, want bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if app.Meta().ImageInputEnabled == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Meta().ImageInputEnabled did not become %v", want)
}

func TestMetaForTabImageInputCapabilityUsesCurrentRef(t *testing.T) {
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "CUSTOM_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "custom/vision-pro"
	cfg.Desktop.ProviderAccess = []string{"custom"}
	cfg.Providers = []config.ProviderEntry{{
		Name:         "custom",
		Kind:         "openai",
		BaseURL:      "https://example.invalid/v1",
		APIKeyEnv:    "CUSTOM_KEY",
		Models:       []string{"text-only", "vision-pro"},
		VisionModels: []string{"vision-pro"},
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	app.setTestCtrl(control.New(control.Options{Label: "deleted/model"}), "deleted/model")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	if got := app.Meta().ImageInputEnabled; got {
		t.Fatal("unknown model ref should not inherit image input from the default fallback model")
	}
}

func TestSetTokenModeKeepsControllerWhenRebuildFails(t *testing.T) {
	// Name kept for history; an unknown model must not block the deprecated no-op.
	isolateDesktopUserDirs(t)
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("MIMO_API_KEY", "")

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	old := control.New(control.Options{Label: "old-controller"})
	app.setTestCtrl(old, "missing-token-mode-model")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()
	tab := app.activeTab()
	notices := captureTabNotices(app, tab)

	if err := app.SetTokenMode("economy"); err != nil {
		t.Fatalf("SetTokenMode(economy): %v", err)
	}
	assertDeprecatedExecutionModeNoop(t, app, tab, old, *notices)
}

func TestSetEffortRejectsRunningTurn(t *testing.T) {
	isolateDesktopUserDirs(t)

	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Runner: runner}), "")
	app.activeCtrl().Submit("work")
	<-runner.started

	err := app.SetEffort("max")
	if err == nil || !strings.Contains(err.Error(), "finish or cancel") {
		t.Fatalf("SetEffort while running error = %v, want finish/cancel guard", err)
	}

	close(runner.release)
	waitNotRunning(t, app.activeCtrl())
}

func TestSetTokenModeRejectsRunningTurn(t *testing.T) {
	// Name kept for history; the deprecated wrapper does not require an idle tab.
	isolateDesktopUserDirs(t)

	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	app := NewApp()
	old := control.New(control.Options{Runner: runner})
	app.setTestCtrl(old, "")
	tab := app.activeTab()
	notices := captureTabNotices(app, tab)
	old.Submit("work")
	<-runner.started

	if err := app.SetTokenMode("economy"); err != nil {
		t.Fatalf("SetTokenMode while running: %v", err)
	}
	assertDeprecatedExecutionModeNoop(t, app, tab, old, *notices)

	close(runner.release)
	waitNotRunning(t, app.activeCtrl())
}

func TestSetTokenModeRejectsBackgroundJobs(t *testing.T) {
	// Name kept for history; background jobs must not block the deprecated wrapper.
	isolateDesktopUserDirs(t)
	setDesktopTestCredential(t, "OLD_MODEL_KEY", "sk-test")

	cfg := config.Default()
	cfg.DefaultModel = "old/old-model"
	cfg.Desktop.ProviderAccess = []string{"old"}
	cfg.Providers = []config.ProviderEntry{
		{Name: "old", Kind: "openai", BaseURL: "https://example.invalid/v1", Model: "old-model", APIKeyEnv: "OLD_MODEL_KEY"},
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "jobs.jsonl")
	jm := jobs.NewManager(event.Discard)
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: path, Label: "test", Jobs: jm})
	app := NewApp()
	app.ctx = context.Background()
	app.setTestCtrl(ctrl, "old/old-model")
	t.Cleanup(func() {
		if current := app.activeCtrl(); current != nil {
			current.Close()
		}
	})
	tab := app.activeTab()
	notices := captureTabNotices(app, tab)

	release := make(chan struct{})
	job := jm.StartForSession(agent.BranchID(path), "bash", "long job", func(ctx context.Context, _ io.Writer) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-release:
			return "", nil
		}
	})
	t.Cleanup(func() { close(release) })

	if err := app.SetTokenMode("economy"); err != nil {
		t.Fatalf("SetTokenMode with background job: %v", err)
	}
	assertDeprecatedExecutionModeNoop(t, app, tab, ctrl, *notices)
	cancelled, err := app.CancelJobForTab("", job.ID)
	if err != nil || !cancelled {
		t.Fatalf("CancelJobForTab = %v, %v, want true, nil", cancelled, err)
	}
	if result := jm.WaitForSession(context.Background(), agent.BranchID(path), []string{job.ID}, 5); len(result) != 1 || result[0].Status != jobs.Killed {
		t.Fatalf("stopped background job = %+v, want one killed result", result)
	}
}

func TestSetTokenModeUnknownTabErrors(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	err := app.SetTokenModeForTab("missing-tab", "economy")
	if err == nil || !strings.Contains(err.Error(), `tab "missing-tab" not found`) {
		t.Fatalf("SetTokenModeForTab(unknown) = %v, want tab not found", err)
	}
	err = app.SetAgentPresetForTab("missing-tab", "light")
	if err == nil || !strings.Contains(err.Error(), `tab "missing-tab" not found`) {
		t.Fatalf("SetAgentPresetForTab(unknown) = %v, want tab not found", err)
	}
}

func TestSettingsRebuildRejectsBackgroundJobs(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "settings-job.jsonl")
	jm := jobs.NewManager(event.Discard)
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: path, Label: "test", Jobs: jm})
	defer ctrl.Close()
	app := NewApp()
	app.ctx = context.Background()
	app.setTestCtrl(ctrl, "deepseek-flash/deepseek-v4-flash")

	jm.StartForSession(agent.BranchID(path), "bash", "settings job", func(ctx context.Context, _ io.Writer) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})

	err := app.SetSandbox("enforce", true, "", nil, "")
	if err == nil || !strings.Contains(err.Error(), "stop background jobs") {
		t.Fatalf("SetSandbox with background job error = %v, want background-job guard", err)
	}
}

func TestClearSessionCancelsRunningRuntimeAndKeepsTopic(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "clear-running.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"old"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	oldCtrl := control.New(control.Options{Runner: runner, SessionDir: dir, SessionPath: path, Label: "test"})
	app := NewApp()
	app.projectTreeChangedHook = func() {}
	app.setTestCtrl(oldCtrl, "deepseek-flash/deepseek-v4-flash")
	app.tabs["test"].TopicID = "topic_clear"
	app.tabs["test"].TopicTitle = "Clear topic"
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	oldCtrl.Submit("work")
	<-runner.started
	if _, err := app.ClearSession(); err != nil {
		t.Fatalf("ClearSession: %v", err)
	}
	waitNotRunning(t, oldCtrl)
	tab := app.activeTab()
	if tab == nil || tab.Ctrl == nil {
		t.Fatalf("active tab/controller missing after clear")
	}
	if tab.Ctrl == oldCtrl {
		t.Fatalf("clear should replace the active controller after cancelling old work")
	}
	if tab.TopicID != "topic_clear" || tab.TopicTitle != "Clear topic" {
		t.Fatalf("clear changed topic identity: %+v", tab)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("old cleared session artifacts should be removed, stat err = %v", err)
	}
	if got := tab.currentSessionPath(); got == "" || got == path {
		t.Fatalf("new session path = %q, want fresh path", got)
	}
}

func TestClearSessionRemovesRunningJobArtifacts(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "clear-running-job.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"old"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	jm := jobs.NewManager(event.Discard)
	oldCtrl := control.New(control.Options{SessionDir: dir, SessionPath: path, Label: "test", Jobs: jm})
	app := NewApp()
	app.projectTreeChangedHook = func() {}
	app.setTestCtrl(oldCtrl, "deepseek-flash/deepseek-v4-flash")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	started := make(chan struct{})
	jm.StartForSession(agent.BranchID(path), "bash", "clear artifact", func(ctx context.Context, _ io.Writer) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	<-started
	jobsDir := jobs.ArtifactDir(path)
	if _, err := os.Stat(jobsDir); err != nil {
		t.Fatalf("job sidecar should exist before clear: %v", err)
	}

	if _, err := app.ClearSession(); err != nil {
		t.Fatalf("ClearSession: %v", err)
	}
	if _, err := os.Stat(jobsDir); !os.IsNotExist(err) {
		t.Fatalf("old job sidecar should be removed after clear, stat err = %v", err)
	}
}

func TestSearchFileRefsFindsNestedBasename(t *testing.T) {
	orig, _ := os.Getwd()
	defer os.Chdir(orig)

	dir := robustTempDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "frontend", "wailsjs", "runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "frontend", "wailsjs", "runtime", "runtime.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "frontend", "Thumbs.db"), []byte("noise"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "frontend", ".DS_Store"), []byte("noise"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "runtime.js"), []byte("noise"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, noise := range []string{".codex", ".npm", ".pnpm-store", "bin", "dist", "stage", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, noise), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, noise, "runtime.js"), []byte("noise"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "desktop", "frontend", "wailsjs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "desktop", "frontend", "wailsjs", "runtime.js"), []byte("generated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "product", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "product", "bin", "runtime.js"), []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	app := &App{}
	listed := app.ListDir("")
	for _, hidden := range []string{".codex", ".npm", ".pnpm-store", "bin", "dist", "stage", "tmp"} {
		if hasDirEntry(listed, hidden) {
			t.Fatalf("ListDir should hide local noise %q, got %+v", hidden, listed)
		}
	}
	desktopFrontend := app.ListDir("desktop/frontend")
	if hasDirEntry(desktopFrontend, "wailsjs") {
		t.Fatalf("ListDir should hide generated Wails bindings, got %+v", desktopFrontend)
	}
	frontendEntries := app.ListDir("frontend")
	for _, hidden := range []string{".DS_Store", "Thumbs.db"} {
		if hasDirEntry(frontendEntries, hidden) {
			t.Fatalf("ListDir should hide local noise file %q, got %+v", hidden, frontendEntries)
		}
	}

	got := app.SearchFileRefs("runtime.js")
	if !hasDirEntry(got, "frontend/wailsjs/runtime/runtime.js") {
		t.Fatalf("SearchFileRefs(runtime.js) should find nested workspace file, got %+v", got)
	}
	if !hasDirEntry(got, "product/bin/runtime.js") {
		t.Fatalf("SearchFileRefs should keep non-root bin directories searchable, got %+v", got)
	}
	if hasDirEntry(got, "node_modules/pkg/runtime.js") {
		t.Fatalf("SearchFileRefs should skip node_modules noise, got %+v", got)
	}
	for _, hidden := range []string{
		".codex/runtime.js",
		".npm/runtime.js",
		".pnpm-store/runtime.js",
		"bin/runtime.js",
		"desktop/frontend/wailsjs/runtime.js",
		"dist/runtime.js",
		"stage/runtime.js",
		"tmp/runtime.js",
	} {
		if hasDirEntry(got, hidden) {
			t.Fatalf("SearchFileRefs should skip local noise %q, got %+v", hidden, got)
		}
	}
	if noise := app.SearchFileRefs("Thumbs"); hasDirEntry(noise, "frontend/Thumbs.db") {
		t.Fatalf("SearchFileRefs should skip Thumbs.db noise, got %+v", noise)
	}
	if noise := app.SearchFileRefs(".DS"); hasDirEntry(noise, "frontend/.DS_Store") {
		t.Fatalf("SearchFileRefs should skip .DS_Store noise even for dot-prefixed search, got %+v", noise)
	}
}

func TestFileRefsUseActiveTabWorkspaceRoot(t *testing.T) {
	orig, _ := os.Getwd()
	defer os.Chdir(orig)

	launchRoot := robustTempDir(t)
	projectRoot := robustTempDir(t)
	if err := os.WriteFile(filepath.Join(launchRoot, "launch-only.txt"), []byte("wrong"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, "frontend", "wailsjs", "runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	projectFile := filepath.Join(projectRoot, "frontend", "wailsjs", "runtime", "runtime.js")
	if err := os.WriteFile(projectFile, []byte("right workspace"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(launchRoot); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	tab := &WorkspaceTab{ID: "project", Scope: "project", WorkspaceRoot: projectRoot}
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.activeTabID = tab.ID

	listed := app.ListDir("")
	if !hasDirEntry(listed, "frontend") {
		t.Fatalf("ListDir should list active project root, got %+v", listed)
	}
	if hasDirEntry(listed, "launch-only.txt") {
		t.Fatalf("ListDir leaked launch cwd entries, got %+v", listed)
	}

	found := app.SearchFileRefs("runtime.js")
	if !hasDirEntry(found, "frontend/wailsjs/runtime/runtime.js") {
		t.Fatalf("SearchFileRefs should search active project root, got %+v", found)
	}
	preview := app.ReadFile("frontend/wailsjs/runtime/runtime.js")
	if preview.Err != "" || preview.Body != "right workspace" {
		t.Fatalf("ReadFile active project preview = %+v, want project file", preview)
	}
}

func TestFileRefsForTabIgnoreActiveParentWorkspace(t *testing.T) {
	parentRoot := robustTempDir(t)
	childRoot := filepath.Join(parentRoot, "child")
	if err := os.MkdirAll(childRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parentRoot, "parent-only.txt"), []byte("parent"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parentRoot, "shared.txt"), []byte("parent shared"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childRoot, "child-only.txt"), []byte("child"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childRoot, "shared.txt"), []byte("child shared"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := &App{
		tabs: map[string]*WorkspaceTab{
			"parent": {ID: "parent", Scope: "project", WorkspaceRoot: parentRoot},
			"child":  {ID: "child", Scope: "project", WorkspaceRoot: childRoot},
		},
		activeTabID: "parent",
	}

	listed := app.ListDirForTab("child", "")
	if !hasDirEntry(listed, "child-only.txt") || hasDirEntry(listed, "parent-only.txt") {
		t.Fatalf("ListDirForTab(child) = %+v, want only child workspace entries", listed)
	}
	found := app.SearchFileRefsForTab("child", "child-only")
	if !hasDirEntry(found, "child-only.txt") {
		t.Fatalf("SearchFileRefsForTab(child) = %+v, want child-only.txt", found)
	}
	preview := app.ReadFileForTab("child", "shared.txt")
	if preview.Err != "" || preview.Body != "child shared" {
		t.Fatalf("ReadFileForTab(child) = %+v, want child workspace file", preview)
	}
	path, ok, err := app.workspaceOrExternalPathForTab("child", "shared.txt")
	if err != nil || !ok || path != filepath.Join(childRoot, "shared.txt") {
		t.Fatalf("workspaceOrExternalPathForTab(child) = (%q, %v, %v)", path, ok, err)
	}

	legacy := app.ReadFile("shared.txt")
	if legacy.Err != "" || legacy.Body != "parent shared" {
		t.Fatalf("ReadFile legacy active-tab behavior = %+v, want parent workspace file", legacy)
	}
}

func TestFileRefsIncludeRegisteredExternalFolderChildren(t *testing.T) {
	workspace := robustTempDir(t)
	external := filepath.Join(robustTempDir(t), "Folder With Spaces")
	if err := os.MkdirAll(filepath.Join(external, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(external, "src", "outside.txt"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectedExternal := external
	if resolved, err := filepath.EvalSymlinks(external); err == nil {
		expectedExternal = resolved
	}
	expectedDisplayPath := filepath.ToSlash(expectedExternal)

	ctrl := &control.Controller{}
	token, _, err := ctrl.RegisterExternalFolderRef(external)
	if err != nil {
		t.Fatalf("RegisterExternalFolderRef: %v", err)
	}
	app := &App{
		tabs: map[string]*WorkspaceTab{
			"project": {ID: "project", WorkspaceRoot: workspace, Ctrl: ctrl},
			"other":   {ID: "other", WorkspaceRoot: robustTempDir(t)},
		},
		activeTabID: "other",
	}

	listed := app.ListDirForTab("project", token+"/src/")
	if len(listed) != 1 ||
		listed[0].Name != "outside.txt" ||
		listed[0].Path != token+"/src/outside.txt" ||
		listed[0].DisplayPath != expectedDisplayPath+"/src/outside.txt" {
		t.Fatalf("ListDir external src = %+v, want outside token/display path", listed)
	}

	found := app.SearchFileRefsForTab("project", "outside")
	var externalHit *DirEntry
	for i := range found {
		if found[i].Path == token+"/src/outside.txt" {
			externalHit = &found[i]
			break
		}
	}
	if externalHit == nil || externalHit.DisplayName != "Folder With Spaces/src/outside.txt" || externalHit.DisplayPath != expectedDisplayPath+"/src/outside.txt" {
		t.Fatalf("SearchFileRefs external hit = %+v, all results %+v", externalHit, found)
	}

	preview := app.ReadFileForTab("project", token+"/src/outside.txt")
	if preview.Err != "" || preview.Body != "outside" {
		t.Fatalf("ReadFile external token preview = %+v, want outside file body", preview)
	}
}

func TestDeleteSessionCancelsActiveRuntime(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "active.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"hello"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	app := NewApp()
	activeCtrl := control.New(control.Options{SessionDir: dir, SessionPath: path, Label: "test"})
	keepPath := filepath.Join(dir, "keep.jsonl")
	if err := os.WriteFile(keepPath, []byte(`{"role":"user","content":"keep"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write keep session: %v", err)
	}
	keepCtrl := control.New(control.Options{SessionDir: dir, SessionPath: keepPath, Label: "keep"})
	defer keepCtrl.Close()
	app.setTestCtrl(activeCtrl, "")
	app.tabs["keep"] = &WorkspaceTab{ID: "keep", Scope: "global", Ctrl: keepCtrl, Ready: true}
	app.tabOrder = []string{"test", "keep"}

	if err := app.DeleteSession(filepath.Base(path)); err != nil {
		t.Fatalf("DeleteSession(active basename): %v", err)
	}
	if _, ok := app.tabs["test"]; ok {
		t.Fatalf("deleted active session runtime should be removed")
	}
	if got := app.activeTabID; got != "keep" {
		t.Fatalf("active tab after delete = %q, want keep", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("active session should be moved out of active history, stat err = %v", err)
	}
	trashPath := filepath.Join(dir, sessionTrashDir, "active.jsonl", "active.jsonl")
	if _, err := os.Stat(trashPath); err != nil {
		t.Fatalf("active session should be moved to trash: %v", err)
	}
}

func TestDeleteSessionCancelsPreReadyBlankBuild(t *testing.T) {
	isolateDesktopUserDirs(t)

	globalRoot := globalTabWorkspaceRoot()
	dir := desktopSessionDir(globalRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "pre-ready-blank.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write blank session: %v", err)
	}
	cancelled := false
	blank := &WorkspaceTab{
		ID:            "blank",
		Scope:         "global",
		WorkspaceRoot: globalRoot,
		SessionPath:   path,
		buildCancel:   func() { cancelled = true },
		disabledMCP:   map[string]ServerView{},
	}
	keep := &WorkspaceTab{
		ID:            "keep",
		Scope:         "global",
		WorkspaceRoot: globalRoot,
		Ready:         true,
		disabledMCP:   map[string]ServerView{},
	}
	app := &App{
		tabs:        map[string]*WorkspaceTab{"blank": blank, "keep": keep},
		tabOrder:    []string{"blank", "keep"},
		activeTabID: "blank",
	}

	if err := app.DeleteSession(filepath.Base(path)); err != nil {
		t.Fatalf("DeleteSession(pre-ready blank): %v", err)
	}
	if !cancelled {
		t.Fatal("pre-ready blank build was not cancelled")
	}
	if !blank.removed {
		t.Fatal("pre-ready blank tab was not marked removed")
	}
	if _, ok := app.tabs["blank"]; ok {
		t.Fatal("pre-ready blank tab should be removed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("blank session should be moved out of active history, stat err = %v", err)
	}
}

func TestDeleteLastTopicSessionFallbackDoesNotReuseDeletedTopic(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	topicID := "topic_delete_last"
	if err := addProject(projectRoot, ""); err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := setTopicTitle(projectRoot, topicID, "Delete last"); err != nil {
		t.Fatalf("set topic title: %v", err)
	}
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := writeTopicSession(t, dir, "delete-last.jsonl", topicID, "Delete last", projectRoot)
	ctrl := controllerWithContent(t, path)
	app := &App{
		tabs: map[string]*WorkspaceTab{
			"only": {
				ID:            "only",
				Scope:         "project",
				WorkspaceRoot: projectRoot,
				TopicID:       topicID,
				TopicTitle:    "Delete last",
				Ctrl:          ctrl,
				Ready:         true,
				disabledMCP:   map[string]ServerView{},
			},
		},
		tabOrder:    []string{"only"},
		activeTabID: "only",
	}

	if err := app.DeleteSession(path); err != nil {
		t.Fatalf("DeleteSession(last topic session): %v", err)
	}

	if _, ok := app.tabs["only"]; ok {
		t.Fatalf("deleted topic session tab should be removed")
	}
	for id, tab := range app.tabs {
		if tab.TopicID == topicID {
			t.Fatalf("fallback tab %q reused deleted topic %q", id, topicID)
		}
		if strings.TrimSpace(tab.TopicID) != "" {
			t.Fatalf("fallback tab %q topic ID = %q, want transient unindexed blank", id, tab.TopicID)
		}
	}
	trashPath := filepath.Join(dir, sessionTrashDir, "delete-last.jsonl", "delete-last.jsonl")
	if _, err := os.Stat(trashPath); err != nil {
		t.Fatalf("deleted session should be moved to trash: %v", err)
	}
}

func TestDeleteSessionFallbackKeepsTopicWithRemainingHistory(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	topicID := "topic_delete_keep_history"
	if err := addProject(projectRoot, ""); err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := setTopicTitle(projectRoot, topicID, "Keep history"); err != nil {
		t.Fatalf("set topic title: %v", err)
	}
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := writeTopicSession(t, dir, "delete-one.jsonl", topicID, "Keep history", projectRoot)
	remainingPath := writeTopicSessionWithPrompt(t, dir, "remaining.jsonl", topicID, "Keep history", projectRoot, "remaining turn", time.Now().Add(-time.Minute))
	ctrl := controllerWithContent(t, path)
	app := &App{
		tabs: map[string]*WorkspaceTab{
			"only": {
				ID:            "only",
				Scope:         "project",
				WorkspaceRoot: projectRoot,
				TopicID:       topicID,
				TopicTitle:    "Keep history",
				Ctrl:          ctrl,
				Ready:         true,
				disabledMCP:   map[string]ServerView{},
			},
		},
		tabOrder:    []string{"only"},
		activeTabID: "only",
	}

	if err := app.DeleteSession(path); err != nil {
		t.Fatalf("DeleteSession(topic with remaining history): %v", err)
	}

	found := false
	for _, tab := range app.tabs {
		if tab.TopicID == topicID {
			found = true
			if got := filepath.Clean(tab.currentSessionPath()); got != filepath.Clean(remainingPath) {
				t.Fatalf("fallback session path = %q, want remaining history %q", got, remainingPath)
			}
		}
	}
	if !found {
		t.Fatalf("fallback should keep topic %q when another session remains", topicID)
	}
}

func TestDeleteSessionWithStuckJobReturnsAfterSingleGrace(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "stuck-delete.jsonl")
	keepPath := filepath.Join(dir, "keep.jsonl")
	for _, p := range []string{path, keepPath} {
		if err := os.WriteFile(p, []byte(`{"role":"user","content":"hello"}`+"\n"), 0o644); err != nil {
			t.Fatalf("write session %s: %v", p, err)
		}
	}

	grace := 500 * time.Millisecond
	teardownNotices := make(chan event.Event, 2)
	jm := jobs.NewManager(teardownNoticeSink(teardownNotices), jobs.WithTeardownGrace(grace))
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: path, Label: "test", Jobs: jm})
	keepCtrl := control.New(control.Options{SessionDir: dir, SessionPath: keepPath, Label: "keep"})
	releaseJob := startNonCooperativeSessionJob(t, jm, path)
	defer func() {
		releaseJob()
		ctrl.Close()
		keepCtrl.Close()
	}()

	app := NewApp()
	app.setTestCtrl(ctrl, "")
	app.tabs["keep"] = &WorkspaceTab{ID: "keep", Scope: "global", Ctrl: keepCtrl, Ready: true}
	app.tabOrder = []string{"test", "keep"}

	start := time.Now()
	if err := app.DeleteSession(filepath.Base(path)); err != nil {
		t.Fatalf("DeleteSession(stuck job): %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > grace+2*time.Second {
		t.Fatalf("DeleteSession took %s, want one teardown grace plus bounded metadata I/O", elapsed)
	}
	assertSingleTeardownTimeoutNotice(t, teardownNotices, grace)
	if !agent.IsCleanupPending(path) {
		t.Fatalf("stuck delete should mark cleanup pending")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stuck session file should remain until delayed cleanup: %v", err)
	}
}

func TestDeleteSessionTrashConflictKeepsRuntime(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "active-conflict.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"hello"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, sessionTrashDir, filepath.Base(path)), 0o755); err != nil {
		t.Fatalf("create trash conflict: %v", err)
	}

	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	ctrl := control.New(control.Options{Runner: runner, SessionDir: dir, SessionPath: path, Label: "test"})
	app := NewApp()
	app.setTestCtrl(ctrl, "")
	defer ctrl.Close()
	ctrl.Submit("work")
	<-runner.started

	err := app.DeleteSession(filepath.Base(path))
	if err != nil {
		t.Fatalf("DeleteSession should succeed after cleaning empty trash dir: %v", err)
	}
	if _, ok := app.tabs["test"]; ok {
		t.Fatalf("deleted session runtime should be removed from tabs")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("session file should be moved out of active history, stat err = %v", err)
	}
	trashPath := filepath.Join(dir, sessionTrashDir, filepath.Base(path), filepath.Base(path))
	if _, err := os.Stat(trashPath); err != nil {
		t.Fatalf("session should be moved to trash: %v", err)
	}

	close(runner.release)
	waitNotRunning(t, ctrl)
}

func TestDeleteSessionValidTrashRemovesEmptyLiveStub(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "stale-live.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write live stub: %v", err)
	}
	trashPath := filepath.Join(dir, sessionTrashDir, filepath.Base(path), filepath.Base(path))
	if err := os.MkdirAll(filepath.Dir(trashPath), 0o755); err != nil {
		t.Fatalf("create trash dir: %v", err)
	}
	if err := os.WriteFile(trashPath, []byte(`{"role":"user","content":"trashed"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write trash session: %v", err)
	}

	activePath := filepath.Join(dir, "active.jsonl")
	if err := os.WriteFile(activePath, []byte(`{"role":"user","content":"active"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write active session: %v", err)
	}
	activeCtrl := control.New(control.Options{SessionDir: dir, SessionPath: activePath, Label: "active"})
	defer activeCtrl.Close()
	app := &App{
		tabs:        map[string]*WorkspaceTab{"active": {ID: "active", Scope: "global", Ctrl: activeCtrl, Ready: true}},
		activeTabID: "active",
		tabOrder:    []string{"active"},
	}

	if err := app.DeleteSession(filepath.Base(path)); err != nil {
		t.Fatalf("DeleteSession should remove stale live stub: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("live stub should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(trashPath); err != nil {
		t.Fatalf("existing trash should remain authoritative: %v", err)
	}
}

func TestDeleteSessionValidTrashRemovesDuplicateLiveSession(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "duplicate-recovery.jsonl")
	content := []byte(`{"role":"user","content":"same recovery"}` + "\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write live session: %v", err)
	}
	trashPath := filepath.Join(dir, sessionTrashDir, filepath.Base(path), filepath.Base(path))
	if err := os.MkdirAll(filepath.Dir(trashPath), 0o755); err != nil {
		t.Fatalf("create trash dir: %v", err)
	}
	if err := os.WriteFile(trashPath, content, 0o644); err != nil {
		t.Fatalf("write trash session: %v", err)
	}

	activePath := filepath.Join(dir, "active.jsonl")
	if err := os.WriteFile(activePath, []byte(`{"role":"user","content":"active"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write active session: %v", err)
	}
	activeCtrl := control.New(control.Options{SessionDir: dir, SessionPath: activePath, Label: "active"})
	defer activeCtrl.Close()
	app := &App{
		tabs:        map[string]*WorkspaceTab{"active": {ID: "active", Scope: "global", Ctrl: activeCtrl, Ready: true}},
		activeTabID: "active",
		tabOrder:    []string{"active"},
	}

	if err := app.DeleteSession(filepath.Base(path)); err != nil {
		t.Fatalf("DeleteSession should remove duplicate live session: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("duplicate live session should be removed, stat err = %v", err)
	}
	if got, err := os.ReadFile(trashPath); err != nil || string(got) != string(content) {
		t.Fatalf("existing trash should remain authoritative, got %q err=%v", string(got), err)
	}
}

func TestRestoreSessionRejectsOpenEmptyLiveStub(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "restore-open.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"trashed"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write trash source: %v", err)
	}
	if err := deleteSessionFile(dir, path); err != nil {
		t.Fatalf("trash source: %v", err)
	}
	trashPath := filepath.Join(dir, sessionTrashDir, filepath.Base(path), filepath.Base(path))
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write live stub: %v", err)
	}
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: path, Label: "open"})
	defer ctrl.Close()
	app := &App{
		tabs:        map[string]*WorkspaceTab{"open": {ID: "open", Scope: "global", Ctrl: ctrl, Ready: true}},
		tabOrder:    []string{"open"},
		activeTabID: "open",
	}

	err := app.RestoreSession(trashPath)
	if err == nil || !strings.Contains(err.Error(), "session is open") {
		t.Fatalf("RestoreSession error = %v, want open-session rejection", err)
	}
	if info, statErr := os.Stat(path); statErr != nil || info.Size() != 0 {
		t.Fatalf("open live stub should remain empty, info=%v err=%v", info, statErr)
	}
	if _, err := os.Stat(trashPath); err != nil {
		t.Fatalf("trash session should remain after rejected restore: %v", err)
	}
}

func TestDeleteSessionValidTrashRenamesDifferentLiveConflict(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "real-live.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"new work"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write live session: %v", err)
	}
	trashPath := filepath.Join(dir, sessionTrashDir, filepath.Base(path), filepath.Base(path))
	if err := os.MkdirAll(filepath.Dir(trashPath), 0o755); err != nil {
		t.Fatalf("create trash dir: %v", err)
	}
	if err := os.WriteFile(trashPath, []byte(`{"role":"user","content":"trashed"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write trash session: %v", err)
	}

	activeCtrl := control.New(control.Options{SessionDir: dir, SessionPath: path, Label: "active"})
	defer activeCtrl.Close()
	app := NewApp()
	app.setTestCtrl(activeCtrl, "")

	if err := app.DeleteSession(filepath.Base(path)); err != nil {
		t.Fatalf("DeleteSession should move different live session to a unique trash item: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("live session should be moved out of active history, stat err = %v", err)
	}
	if got, err := os.ReadFile(trashPath); err != nil || !strings.Contains(string(got), "trashed") {
		t.Fatalf("original trash session should remain, got %q err=%v", string(got), err)
	}
	trashed, err := listTrashedSessionFiles(dir)
	if err != nil {
		t.Fatalf("list trash: %v", err)
	}
	var renamedPath string
	for _, candidate := range trashed {
		if candidate != trashPath && filepath.Base(candidate) == filepath.Base(path) {
			renamedPath = candidate
			break
		}
	}
	if renamedPath == "" {
		t.Fatalf("renamed trash copy not found in %#v", trashed)
	}
	if filepath.Base(filepath.Dir(renamedPath)) == filepath.Base(path) {
		t.Fatalf("renamed trash copy reused fixed trash item dir: %s", renamedPath)
	}
	if got, err := os.ReadFile(renamedPath); err != nil || !strings.Contains(string(got), "new work") {
		t.Fatalf("renamed trash session = %q err=%v, want live content", string(got), err)
	}
}

func TestDeleteSessionCancelsInactiveOpenRuntime(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	activePath := filepath.Join(dir, "active.jsonl")
	inactivePath := filepath.Join(dir, "inactive.jsonl")
	otherPath := filepath.Join(dir, "other.jsonl")
	for _, path := range []string{activePath, inactivePath, otherPath} {
		if err := os.WriteFile(path, []byte(`{"role":"user","content":"hello"}`+"\n"), 0o644); err != nil {
			t.Fatalf("write session %s: %v", path, err)
		}
	}

	activeCtrl := control.New(control.Options{SessionDir: dir, SessionPath: activePath, Label: "active"})
	inactiveCtrl := control.New(control.Options{SessionDir: dir, SessionPath: inactivePath, Label: "inactive"})
	defer activeCtrl.Close()
	defer inactiveCtrl.Close()

	app := &App{
		tabs: map[string]*WorkspaceTab{
			"active":   {ID: "active", Scope: "global", Ctrl: activeCtrl, Ready: true},
			"inactive": {ID: "inactive", Scope: "global", Ctrl: inactiveCtrl, Ready: true},
		},
		tabOrder:    []string{"active", "inactive"},
		activeTabID: "active",
	}
	installSessionCatalogForTest(t, app, dir, "global", "")
	if err := app.DeleteSession(filepath.Base(inactivePath)); err != nil {
		t.Fatalf("DeleteSession(inactive open basename): %v", err)
	}
	if _, ok := app.tabs["inactive"]; ok {
		t.Fatalf("deleted inactive session runtime should be removed")
	}
	if _, err := os.Stat(inactivePath); !os.IsNotExist(err) {
		t.Fatalf("inactive open session should be moved out of active history, stat err = %v", err)
	}
	trashPath := filepath.Join(dir, sessionTrashDir, "inactive.jsonl", "inactive.jsonl")
	if _, err := os.Stat(trashPath); err != nil {
		t.Fatalf("inactive open session should be moved to trash: %v", err)
	}

	sessions := app.ListSessions()
	current := map[string]bool{}
	open := map[string]bool{}
	for _, s := range sessions {
		current[filepath.Base(s.Path)] = s.Current
		open[filepath.Base(s.Path)] = s.Open
	}
	if !current[filepath.Base(activePath)] {
		t.Fatalf("ListSessions should mark active session current, got %#v", current)
	}
	if current[filepath.Base(otherPath)] {
		t.Fatalf("ListSessions marked unopened session current, got %#v", current)
	}
	if !open[filepath.Base(activePath)] {
		t.Fatalf("ListSessions should mark active and inactive open sessions open, got %#v", open)
	}
	if open[filepath.Base(inactivePath)] || open[filepath.Base(otherPath)] {
		t.Fatalf("ListSessions marked unopened session open, got %#v", open)
	}
}

func TestTrashTopicRejectsBackgroundJob(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	topicID := "topic_stuck_trash"
	if err := addProject(projectRoot, ""); err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := setTopicTitle(projectRoot, topicID, "Stuck trash"); err != nil {
		t.Fatalf("set topic title: %v", err)
	}
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	sessionPath := writeTopicSession(t, dir, "stuck-topic.jsonl", topicID, "Stuck trash", projectRoot)

	jm := jobs.NewManager(event.Discard)
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: sessionPath, Label: "test", Jobs: jm, WorkspaceRoot: projectRoot})
	releaseJob := startNonCooperativeSessionJob(t, jm, sessionPath)
	defer func() {
		releaseJob()
		ctrl.Close()
	}()

	app := &App{
		tabs: map[string]*WorkspaceTab{
			"stuck": {
				ID:            "stuck",
				Scope:         "project",
				WorkspaceRoot: projectRoot,
				TopicID:       topicID,
				TopicTitle:    "Stuck trash",
				Ctrl:          ctrl,
				Ready:         true,
				disabledMCP:   map[string]ServerView{},
			},
			"keep": {
				ID:            "keep",
				Scope:         "project",
				WorkspaceRoot: projectRoot,
				TopicID:       "topic_keep",
				TopicTitle:    "Keep",
				Ready:         true,
				disabledMCP:   map[string]ServerView{},
			},
		},
		tabOrder:    []string{"stuck", "keep"},
		activeTabID: "stuck",
	}

	if err := app.TrashTopic(topicID); !errors.Is(err, errTopicHasActiveWork) {
		t.Fatalf("TrashTopic(background job) error = %v, want %v", err, errTopicHasActiveWork)
	}
	if _, ok := app.tabs["stuck"]; !ok {
		t.Fatal("rejected archive should keep the background-job topic tab")
	}
	if agent.IsCleanupPending(sessionPath) {
		t.Fatal("rejected archive should not mark session cleanup pending")
	}
	if _, err := os.Stat(sessionPath); err != nil {
		t.Fatalf("rejected archive should preserve the live session: %v", err)
	}
	trashPath := filepath.Join(dir, sessionTrashDir, "stuck-topic.jsonl", "stuck-topic.jsonl")
	if _, err := os.Stat(trashPath); !os.IsNotExist(err) {
		t.Fatalf("rejected archive created a trash entry, stat err = %v", err)
	}
	if got := loadTopicTitle(projectRoot, topicID); got != "Stuck trash" {
		t.Fatalf("rejected archive topic title = %q, want Stuck trash", got)
	}
}

func teardownNoticeSink(out chan<- event.Event) event.Sink {
	return event.FuncSink(func(e event.Event) {
		if e.Kind == event.Notice && strings.Contains(e.Detail, "background job teardown timed out") {
			out <- e
		}
	})
}

func assertSingleTeardownTimeoutNotice(t *testing.T, notices <-chan event.Event, grace time.Duration) {
	t.Helper()
	var notice event.Event
	select {
	case notice = <-notices:
	default:
		t.Fatal("missing background-job teardown timeout notice")
	}
	var waited time.Duration
	for field := range strings.FieldsSeq(notice.Detail) {
		if !strings.HasPrefix(field, "waited=") {
			continue
		}
		parsed, err := time.ParseDuration(strings.TrimSuffix(strings.TrimPrefix(field, "waited="), ";"))
		if err != nil {
			t.Fatalf("parse teardown waited field %q: %v", field, err)
		}
		waited = parsed
		break
	}
	if waited < grace-10*time.Millisecond || waited > grace+250*time.Millisecond {
		t.Fatalf("teardown notice waited %s, want one %s grace; detail: %s", waited, grace, notice.Detail)
	}
	select {
	case extra := <-notices:
		t.Fatalf("duplicate teardown timeout notice: %+v", extra)
	default:
	}
}

func TestWaitDestroyHandlesWaitsConcurrently(t *testing.T) {
	started := make(chan int, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()

	handle := func(id int) control.SessionDestroyHandle {
		return control.SessionDestroyHandle{Wait: func() jobs.TeardownResult {
			started <- id
			<-release
			return jobs.TeardownResult{TimedOut: []jobs.TeardownJob{{ID: strconv.Itoa(id)}}}
		}}
	}
	done := make(chan bool, 1)
	go func() { done <- waitDestroyHandles([]control.SessionDestroyHandle{handle(1), handle(2)}) }()

	seen := map[int]bool{}
	for len(seen) < 2 {
		select {
		case id := <-started:
			seen[id] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("destroy waits did not start concurrently; started=%v", seen)
		}
	}
	releaseAll()
	select {
	case timedOut := <-done:
		if !timedOut {
			t.Fatal("waitDestroyHandles lost timed-out result")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitDestroyHandles did not return after all waits completed")
	}
}

func TestRestoreSessionRejectsDestroyingSession(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(dir, "trash-me.jsonl")
	if err := os.WriteFile(sessionPath, []byte(`{"role":"user","content":"hello"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := deleteSessionFile(dir, sessionPath); err != nil {
		t.Fatalf("deleteSessionFile: %v", err)
	}
	trashPath := filepath.Join(dir, sessionTrashDir, filepath.Base(sessionPath), filepath.Base(sessionPath))

	jm := jobs.NewManager(event.Discard)
	defer jm.Close()
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: filepath.Join(dir, "active.jsonl"), Label: "active", Jobs: jm})
	defer ctrl.Close()
	destroy := ctrl.BeginDestroySession(sessionPath)
	defer destroy.Finish()

	app := NewApp()
	app.setTestCtrl(ctrl, "")
	if err := app.RestoreSession(trashPath); err == nil || !strings.Contains(err.Error(), "cleanup is still in progress") {
		t.Fatalf("RestoreSession while destroying error = %v, want cleanup-in-progress", err)
	}
	if _, err := os.Stat(trashPath); err != nil {
		t.Fatalf("trashed session should remain after rejected restore: %v", err)
	}

	destroy.Finish()
	if err := app.RestoreSession(trashPath); err != nil {
		t.Fatalf("RestoreSession after finish: %v", err)
	}
	if _, err := os.Stat(sessionPath); err != nil {
		t.Fatalf("session should be restored: %v", err)
	}
}

func TestDesktopSessionAPIsUseControllerSessionDir(t *testing.T) {
	isolateDesktopUserDirs(t)
	dirA := filepath.Join(t.TempDir(), "workspace-a-sessions")
	dirB := filepath.Join(t.TempDir(), "workspace-b-sessions")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatalf("mkdir dirA: %v", err)
	}
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatalf("mkdir dirB: %v", err)
	}
	pathA := filepath.Join(dirA, "a.jsonl")
	pathB := filepath.Join(dirB, "b.jsonl")
	if err := os.WriteFile(pathA, []byte(`{"role":"user","content":"workspace A"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write pathA: %v", err)
	}
	if err := os.WriteFile(pathB, []byte(`{"role":"user","content":"workspace B"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write pathB: %v", err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{SessionDir: dirA, SessionPath: pathA, Label: "test"}), "")
	defer app.activeCtrl().Close()
	installSessionCatalogForTest(t, app, dirA, "global", "")
	sessions := app.ListSessions()
	if len(sessions) != 1 || sessions[0].Path != pathA || sessions[0].TurnsState != "unknown" ||
		!strings.Contains(sessions[0].Preview, "being indexed") {
		t.Fatalf("ListSessions should read the active controller session dir only, got %+v", sessions)
	}
	if err := app.RenameSession(pathA, "A title"); err != nil {
		t.Fatalf("RenameSession in active session dir: %v", err)
	}
	meta, ok, err := agent.LoadBranchMeta(pathA)
	if err != nil || !ok {
		t.Fatalf("LoadBranchMeta after RenameSession ok=%v err=%v", ok, err)
	}
	if meta.CustomTitle != "A title" {
		t.Fatalf("custom title should be written to branch meta, got %q", meta.CustomTitle)
	}
	reconcileSessionCatalogForTest(t, app, dirA, "global", "")
	sessions = app.ListSessions()
	if len(sessions) != 1 || sessions[0].Title != "A title" {
		t.Fatalf("ListSessions should return custom title from branch meta, got %+v", sessions)
	}
	if titles := loadSessionTitles(dirA); titles["a.jsonl"] != "A title" {
		t.Fatalf("title should be written beside the active session, got %+v", titles)
	}
	if titles := loadSessionTitles(dirB); len(titles) != 0 {
		t.Fatalf("inactive workspace title sidecar should remain untouched, got %+v", titles)
	}
}

func TestListSessionsMarksAutoBotSessionAsChannel(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "bot-channel.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"from channel"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	cfg := config.Default()
	cfg.Bot.Connections = []config.BotConnectionConfig{{
		ID: "weixin-weixin", Provider: "weixin", Domain: "weixin", Label: "微信", Enabled: true, Status: "connected",
		SessionMappings: []config.BotConnectionSessionMapping{{
			RemoteID: "wx-chat-1", SessionID: "path:" + path, SessionSource: "auto",
		}},
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{SessionDir: dir, SessionPath: filepath.Join(dir, "active.jsonl"), Label: "test"}), "")
	defer app.activeCtrl().Close()
	installSessionCatalogForTest(t, app, dir, "global", "")
	sessions := app.ListSessions()
	if len(sessions) != 1 {
		t.Fatalf("ListSessions len = %d, want 1: %+v", len(sessions), sessions)
	}
	got := sessions[0]
	if got.Kind != "channel" || got.Channel != "weixin" || got.ChannelLabel != "微信" || got.RemoteID != "wx-chat-1" || got.SessionSource != "auto" {
		t.Fatalf("channel session meta = %+v", got)
	}
}

func TestDeleteSessionClearsAutoBotSessionMapping(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "bot-channel.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"from channel"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	other := filepath.Join(dir, "other-channel.jsonl")
	cfg := config.Default()
	cfg.Bot.Connections = []config.BotConnectionConfig{{
		ID: "weixin-weixin", Provider: "weixin", Domain: "weixin", Label: "微信", Enabled: true, Status: "connected",
		SessionMappings: []config.BotConnectionSessionMapping{
			{RemoteID: "remove-auto", SessionID: "path:" + path, SessionSource: "auto"},
			{RemoteID: "keep-explicit", SessionID: "path:" + path},
			{RemoteID: "keep-other-auto", SessionID: "path:" + other, SessionSource: "auto"},
		},
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := NewApp()
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: filepath.Join(dir, "active.jsonl"), Label: "test"})
	app.setTestCtrl(ctrl, "")
	defer app.activeCtrl().Close()

	if err := app.DeleteSession(path); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	got := config.LoadForEdit(config.UserConfigPath())
	mappings := got.Bot.Connections[0].SessionMappings
	if len(mappings) != 2 {
		t.Fatalf("session mappings = %+v, want explicit and other auto mappings preserved", mappings)
	}
	for _, mapping := range mappings {
		if mapping.RemoteID == "remove-auto" {
			t.Fatalf("deleted session auto mapping was preserved: %+v", mappings)
		}
	}
}

func TestOpenChannelSessionForTabIsReadOnly(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "bot-channel.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"from channel"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	app := NewApp()
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: filepath.Join(dir, "active.jsonl"), Label: "test"})
	app.setTestCtrl(ctrl, "")
	defer app.activeCtrl().Close()

	if _, err := app.OpenChannelSessionForTab("test", path); err != nil {
		t.Fatalf("OpenChannelSessionForTab: %v", err)
	}
	if meta := app.tabMeta(app.activeTab(), true); !meta.ReadOnly {
		t.Fatalf("channel tab should be read-only: %+v", meta)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	app.SubmitToTab("test", "must not append")
	app.RunShellForTab("test", "echo must-not-run")
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("read-only channel transcript changed:\nbefore=%s\nafter=%s", before, after)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.WriteString(`{"role":"user","content":"external follow-up"}` + "\n"); err != nil {
		f.Close()
		t.Fatalf("append external message: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close append: %v", err)
	}
	app.snapshotAllTabs()
	afterSnapshot, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after snapshot: %v", err)
	}
	if !strings.Contains(string(afterSnapshot), "external follow-up") {
		t.Fatalf("read-only channel snapshot overwrote external append:\n%s", afterSnapshot)
	}
}

func TestUserTriggeredCommandsReturnErrorsWhenUnavailable(t *testing.T) {
	tests := []struct {
		name string
		app  *App
		call func(*App) error
		want string
	}{
		{
			name: "submit read-only",
			app: &App{
				tabs:        map[string]*WorkspaceTab{"test": {ID: "test", Scope: "global", ReadOnly: true}},
				activeTabID: "test",
			},
			call: func(app *App) error { return app.SubmitToTab("test", "hello") },
			want: "read-only",
		},
		{
			name: "submit workspace unavailable",
			app: &App{
				tabs:        map[string]*WorkspaceTab{"test": {ID: "test", Scope: "global", StartupErr: "boom"}},
				activeTabID: "test",
			},
			call: func(app *App) error { return app.SubmitToTab("test", "hello") },
			want: "workspace failed to start: boom",
		},
		{
			name: "run shell workspace unavailable",
			app: &App{
				tabs:        map[string]*WorkspaceTab{"test": {ID: "test", Scope: "global"}},
				activeTabID: "test",
			},
			call: func(app *App) error { return app.RunShellForTab("test", "echo hi") },
			want: "workspace is still starting",
		},
		{
			name: "steer workspace unavailable",
			app: &App{
				tabs:        map[string]*WorkspaceTab{"test": {ID: "test", Scope: "global"}},
				activeTabID: "test",
			},
			call: func(app *App) error { return app.SteerForTab("test", "please continue") },
			want: "workspace is still starting",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(tt.app)
			if err == nil {
				t.Fatalf("expected error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want to contain %q", err, tt.want)
			}
		})
	}
}

func TestSubmitEntryPointsRejectEmptyProviderInput(t *testing.T) {
	app := NewApp()
	for _, tt := range []struct {
		name string
		call func() error
	}{
		{name: "plain", call: func() error { return app.SubmitToTab("missing", " \n\t ") }},
		{name: "display", call: func() error { return app.SubmitDisplayToTab("missing", "visible prompt", " ") }},
		{name: "delivery recovery", call: func() error {
			return app.SubmitDeliveryRecoveryToTab("missing", "visible prompt", "")
		}},
		{name: "invocations", call: func() error {
			return app.SubmitInvocationsToTab("missing", "/skill visible", "", nil)
		}},
		{name: "edited display", call: func() error {
			return app.SubmitEditedDisplayToTab("missing", "visible prompt", "\n", "original prompt")
		}},
		{name: "initial goal", call: func() error {
			_, err := app.SubmitInitialGoalToTab("missing", "goal", "visible prompt", "", nil, "normal", "auto")
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); !errors.Is(err, errEmptyTurnInput) {
				t.Fatalf("error = %v, want errEmptyTurnInput", err)
			}
		})
	}
}

func TestInvocationEntryPointsAllowEmptyExplicitTaskForSkillOnlyTurn(t *testing.T) {
	invocations := []InvocationRequest{{Name: "skill", Kind: "skill"}}
	if err := validateInvocationTurnInput("", invocations); err != nil {
		t.Fatalf("skill-only invocation input rejected: %v", err)
	}
	if err := validateInvocationTurnInput("", nil); !errors.Is(err, errEmptyTurnInput) {
		t.Fatalf("empty input without invocations = %v, want errEmptyTurnInput", err)
	}
}

func TestCloseReadOnlyChannelTabDoesNotSnapshotTranscript(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, "bot-channel.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"from channel"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	app := NewApp()
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: filepath.Join(dir, "active.jsonl"), Label: "test"})
	app.setTestCtrl(ctrl, "")
	defer ctrl.Close()

	if _, err := app.OpenChannelSessionForTab("test", path); err != nil {
		t.Fatalf("OpenChannelSessionForTab: %v", err)
	}
	app.mu.Lock()
	app.tabs["survivor"] = &WorkspaceTab{ID: "survivor", Scope: "global", Ready: true, disabledMCP: map[string]ServerView{}}
	app.tabOrder = []string{"test", "survivor"}
	app.activeTabID = "test"
	app.mu.Unlock()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.WriteString(`{"role":"user","content":"external close follow-up"}` + "\n"); err != nil {
		f.Close()
		t.Fatalf("append external message: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close append: %v", err)
	}

	if err := app.CloseTab("test"); err != nil {
		t.Fatalf("CloseTab: %v", err)
	}
	afterClose, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after close: %v", err)
	}
	if !strings.Contains(string(afterClose), "external close follow-up") {
		t.Fatalf("closing read-only channel tab overwrote external append:\n%s", afterClose)
	}
}

func TestResumeSessionRejectsCleanupPending(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	activePath := filepath.Join(dir, "active.jsonl")
	pendingPath := filepath.Join(dir, "pending.jsonl")
	for _, path := range []string{activePath, pendingPath} {
		if err := os.WriteFile(path, []byte(`{"role":"user","content":"hello"}`+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if err := agent.MarkCleanupPending(pendingPath, "delete"); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: activePath, Label: "test"})
	app.setTestCtrl(ctrl, "")
	defer app.activeCtrl().Close()

	if _, err := app.ResumeSession(pendingPath); err == nil || !strings.Contains(err.Error(), "pending cleanup") {
		t.Fatalf("ResumeSession cleanup-pending error = %v, want pending cleanup", err)
	}
	if got := app.activeCtrl().SessionPath(); filepath.Clean(got) != filepath.Clean(activePath) {
		t.Fatalf("active session path after rejected resume = %q, want %q", got, activePath)
	}
	if _, err := app.OpenChannelSessionForTab("test", pendingPath); err == nil || !strings.Contains(err.Error(), "pending cleanup") {
		t.Fatalf("OpenChannelSessionForTab cleanup-pending error = %v, want pending cleanup", err)
	}
	if meta := app.tabMeta(app.activeTab(), true); meta.ReadOnly {
		t.Fatalf("rejected channel open should not make tab read-only: %+v", meta)
	}
}

func TestResumeSessionRejectsPathOutsideControllerSessionDir(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	activePath := filepath.Join(dirA, "active.jsonl")
	outsidePath := filepath.Join(dirB, "outside.jsonl")
	for _, path := range []string{activePath, outsidePath} {
		if err := os.WriteFile(path, []byte(`{"role":"user","content":"hello"}`+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{SessionDir: dirA, SessionPath: activePath, Label: "test"}), "")
	defer app.activeCtrl().Close()

	if _, err := app.ResumeSession(outsidePath); err == nil {
		t.Fatal("ResumeSession should reject a transcript outside the active session dir")
	}
	if _, err := app.PreviewSession(outsidePath); err == nil {
		t.Fatal("PreviewSession should reject a transcript outside the active session dir")
	}
}

func BenchmarkDesktopListSessionsScoped(b *testing.B) {
	dirA := filepath.Join(b.TempDir(), "workspace-a-sessions")
	dirB := filepath.Join(b.TempDir(), "workspace-b-sessions")
	for _, dir := range []string{dirA, dirB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatalf("mkdir %s: %v", dir, err)
		}
		for i := range 120 {
			path := filepath.Join(dir, fmt.Sprintf("session-%03d.jsonl", i))
			body := fmt.Sprintf(`{"role":"user","content":"session %03d"}`+"\n", i)
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				b.Fatalf("write session: %v", err)
			}
		}
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{SessionDir: dirA, SessionPath: filepath.Join(dirA, "session-000.jsonl"), Label: "test"}), "")
	defer app.activeCtrl().Close()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		sessions := app.ListSessions()
		if len(sessions) != 120 {
			b.Fatalf("ListSessions len = %d, want 120", len(sessions))
		}
	}
}

type appendingDesktopRunner struct {
	session *agent.Session
	started chan string
}

func (r *appendingDesktopRunner) Run(_ context.Context, input string) error {
	r.started <- input
	r.session.Add(provider.Message{Role: provider.RoleUser, Content: input})
	r.session.Add(provider.Message{Role: provider.RoleAssistant, Content: "ok"})
	return nil
}

func TestForkCreatesActiveTabWithoutSwitchingSourceController(t *testing.T) {
	isolateDesktopUserDirsSchemaOne(t)

	workspace := robustTempDir(t)
	if err := os.WriteFile(filepath.Join(workspace, "reasonix.toml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := agent.NewSessionPath(dir, "test")
	sess := agent.NewSession("sys")
	exec := agent.New(nil, nil, sess, agent.Options{}, event.Discard)
	runner := &appendingDesktopRunner{session: sess, started: make(chan string, 2)}
	ctrl := control.New(control.Options{
		Runner:        runner,
		Executor:      exec,
		Sink:          event.Discard,
		SessionDir:    dir,
		SessionPath:   path,
		Label:         "test",
		WorkspaceRoot: workspace,
	})
	app := NewApp()
	app.setTestCtrl(ctrl, "deepseek/test")
	app.tabs["test"].Scope = "project"
	app.tabs["test"].WorkspaceRoot = workspace
	app.tabs["test"].TopicID = "topic_source"
	app.tabs["test"].TopicTitle = "Source topic"
	defer ctrl.Close()

	ctrl.Submit("first")
	<-runner.started
	waitNotRunning(t, ctrl)
	ctrl.Submit("second")
	<-runner.started
	waitNotRunning(t, ctrl)
	if got := len(ctrl.History()); got != 5 {
		t.Fatalf("source history len before fork = %d, want 5", got)
	}

	meta, err := app.Fork(1)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if !meta.Active || meta.ID == "" || meta.ID == "test" {
		t.Fatalf("fork meta = %+v, want a new active tab", meta)
	}
	if got := app.activeTabID; got != meta.ID {
		t.Fatalf("active tab = %q, want fork tab %q", got, meta.ID)
	}
	if got := ctrl.SessionPath(); got != path {
		t.Fatalf("source controller session path = %q, want %q", got, path)
	}
	if got := len(ctrl.History()); got != 5 {
		t.Fatalf("source history len after fork = %d, want 5", got)
	}
	if got, want := meta.TopicTitle, "Source topic · 分叉"; got != want {
		t.Fatalf("fork topic title = %q, want %q", got, want)
	}

	var forkPath string
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read session dir: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		candidate := filepath.Join(dir, entry.Name())
		if candidate == path {
			continue
		}
		m, ok, err := agent.LoadBranchMeta(candidate)
		if err != nil {
			t.Fatalf("load fork meta: %v", err)
		}
		if ok && m.TopicID == meta.TopicID {
			forkPath = candidate
			if m.ParentID != agent.BranchID(path) || m.ForkTurn != 1 || m.ForkMessageIndex != 3 {
				t.Fatalf("fork branch meta = %+v, want parent %q turn 1 index 3", m, agent.BranchID(path))
			}
			if m.Scope != "project" || m.WorkspaceRoot != workspace || m.TopicTitle != "Source topic · 分叉" {
				t.Fatalf("fork topic meta = %+v", m)
			}
		}
	}
	if forkPath == "" {
		t.Fatalf("fork session with topic %q not found in %s", meta.TopicID, dir)
	}
}

func TestCapabilitiesShowsDefaultMCPAsAutomaticIdleNotDisabled(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(`
[[plugins]]
name = "playwright"
command = "npx"
args = ["-y", "@playwright/mcp"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "playwright" {
			if s.Status != "deferred" || s.StartIntent != "automatic" || s.RuntimeState != "idle" {
				t.Fatalf("default MCP view = %+v, want deferred automatic idle", s)
			}
			return
		}
	}
	t.Fatalf("playwright MCP missing from Capabilities: %+v", view.Servers)
}

func TestCapabilitiesIncludesInstalledPlugins(t *testing.T) {
	isolateDesktopUserDirs(t)
	reasonixHome := config.ReasonixHomeDir()
	root := filepath.Join(reasonixHome, "plugins", "superpowers")
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "skills", "plan"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "plan", "SKILL.md"), []byte("---\ndescription: Plan work\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".codex-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".codex-plugin", "plugin.json"), []byte(`{
  "name": "superpowers",
  "version": "6.1.0",
  "description": "Planning workflows",
  "skills": "./skills/"
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pluginpkg.Upsert(reasonixHome, pluginpkg.InstalledPlugin{
		Name:         "superpowers",
		Root:         "plugins/superpowers",
		Version:      "6.1.0",
		Description:  "Planning workflows",
		ManifestKind: "codex",
		Enabled:      true,
	}); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	plugins := app.Capabilities().Plugins
	if len(plugins) != 1 || plugins[0].Name != "superpowers" || plugins[0].Skills != 1 {
		t.Fatalf("Capabilities().Plugins = %+v", plugins)
	}
	if len(plugins[0].SkillDetails) != 1 || plugins[0].SkillDetails[0].Invocation != "/superpowers:plan" {
		t.Fatalf("Capabilities().Plugins skill details = %+v", plugins[0].SkillDetails)
	}
}

func TestDesktopSharedHostProjectMCPConnectsWithoutLaunchApproval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping background MCP boot integration test in short mode")
	}

	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)

	srv := desktopMCPHTTPServer(t)
	defer srv.Close()
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), fmt.Appendf(nil, `
[[plugins]]
name = "h"
type = "http"
url = %q
`, srv.URL), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sharedHost := plugin.NewHost()
	defer sharedHost.Close()
	ctrl, err := boot.Build(ctx, boot.Options{
		WorkspaceRoot: dir,
		SessionDir:    filepath.Join(dir, "sessions"),
		SharedHost:    sharedHost,
		Stderr:        io.Discard,
	})
	if err != nil {
		t.Fatalf("boot.Build: %v", err)
	}
	defer ctrl.Close()

	deadline := time.Now().Add(3 * time.Second)
	for !sharedHost.HasClient("h") && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if !sharedHost.HasClient("h") {
		t.Fatalf("project MCP did not connect automatically; failures=%+v", sharedHost.Failures())
	}
	for _, failure := range sharedHost.Failures() {
		if failure.Name == "h" && failure.RequiresLaunchApproval {
			t.Fatalf("project MCP unexpectedly requested launch approval: %+v", failure)
		}
	}

	app := NewApp()
	app.tabs = map[string]*WorkspaceTab{
		"test": {
			ID:            "test",
			Scope:         "global",
			WorkspaceRoot: dir,
			Ready:         true,
			Ctrl:          ctrl,
			SharedHostKey: dir,
			disabledMCP:   map[string]ServerView{},
		},
	}
	app.activeTabID = "test"

	view := app.MCPServers()
	if len(view) != 1 || view[0].Name != "h" || view[0].Status != "connected" || view[0].RuntimeState != "ready" || view[0].RequiresLaunchApproval {
		t.Fatalf("MCPServers() = %+v, want trusted connected project h", view)
	}
}

func TestProjectMCPViewIsTrustedAndKeepsProjectSource(t *testing.T) {
	entry := config.PluginEntry{Name: "project", Source: config.MCPSourceProjectConfig}
	connected := withPluginConfig(ServerView{Name: entry.Name, Status: "connected"}, entry)
	if connected.RequiresLaunchApproval {
		t.Fatalf("connected project MCP still requires launch approval: %+v", connected)
	}
	blocked := withPluginConfig(ServerView{
		Name: entry.Name, Status: "failed", RequiresLaunchApproval: true,
	}, entry)
	if blocked.RequiresLaunchApproval {
		t.Fatalf("project MCP exposed obsolete launch approval action: %+v", blocked)
	}
	if blocked.Source != "project" || blocked.ConfigSource != "reasonix.toml" {
		t.Fatalf("blocked project MCP source = %q/%q, want project/reasonix.toml", blocked.Source, blocked.ConfigSource)
	}

	user := withPluginConfig(ServerView{Name: "user", Status: "connected"},
		config.PluginEntry{Name: "user", Source: config.MCPSourceUserConfig})
	if user.RequiresLaunchApproval {
		t.Fatalf("user-config MCP must not be launch-gate governed: %+v", user)
	}
	if user.Source != "user" || user.ConfigSource != "config.toml" {
		t.Fatalf("user MCP source = %q/%q, want user/config.toml", user.Source, user.ConfigSource)
	}
}

func TestMCPServersMatchesCapabilitiesServerProjection(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(`
[[plugins]]
name = "playwright"
command = "npx"
args = ["-y", "@playwright/mcp"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()

	if got, want := app.MCPServers(), app.Capabilities().Servers; !reflect.DeepEqual(got, want) {
		t.Fatalf("MCPServers() = %+v, want Capabilities().Servers %+v", got, want)
	}
}

func TestConfiguredMCPWithFormerBuiltInNameIsUserServer(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(`
[[plugins]]
name = "time"
command = "custom-time"
args = ["serve"]
tier = "lazy"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()

	view := app.Capabilities()
	found := false
	for _, s := range view.Servers {
		if s.Name != "time" {
			continue
		}
		found = true
		if s.BuiltIn || !s.Configured || s.Command != "custom-time" || !reflect.DeepEqual(s.Args, []string{"serve"}) {
			t.Fatalf("configured time view = %+v, want ordinary user MCP config", s)
		}
	}
	if !found {
		t.Fatalf("configured time server missing from Capabilities: %+v", view.Servers)
	}

	if err := app.SetMCPServerEnabled("time", false); err != nil {
		t.Fatalf("SetMCPServerEnabled(time,false): %v", err)
	}
	view = app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "time" {
			if s.Status != "disabled" || s.BuiltIn || s.Command != "custom-time" {
				t.Fatalf("disabled configured time view = %+v, want disabled external config", s)
			}
			return
		}
	}
	t.Fatalf("time missing after disable: %+v", view.Servers)
}

func TestSetMCPServerEnabledRestoresOnDemandWithoutConnecting(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("REASONIX_CACHE_HOME", t.TempDir())
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(`
[[plugins]]
name = "offline"
type = "http"
url = "http://127.0.0.1:1/mcp"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	host := plugin.NewHost()
	defer host.Close()
	reg := tool.NewRegistry()
	ctrl := control.New(control.Options{Host: host, Registry: reg, PluginCtx: context.Background(), WorkspaceRoot: dir})
	app := NewApp()
	app.setTestCtrl(ctrl, "")
	app.tabs["test"].WorkspaceRoot = dir

	if err := app.SetMCPServerEnabled("offline", false); err != nil {
		t.Fatalf("SetMCPServerEnabled(false): %v", err)
	}
	if err := app.SetMCPServerEnabled("offline", true); err != nil {
		t.Fatalf("SetMCPServerEnabled(true) forced an unavailable connection: %v", err)
	}
	if host.HasClient("offline") {
		t.Fatal("durable enable started the disconnected MCP server")
	}
	if _, ok := reg.Get("mcp__offline__connect"); !ok {
		t.Fatalf("on-demand connect stub missing after enable; names=%v", reg.Names())
	}
}

func TestSetMCPServerEnabledSharedHostPreservesSiblingTabs(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)

	srv := desktopMCPHTTPServer(t)
	defer srv.Close()
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), fmt.Appendf(nil, `
[[plugins]]
name = "h"
type = "http"
url = %q
`, srv.URL), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sharedHost := plugin.NewHost()
	defer sharedHost.Close()
	tools, err := sharedHost.Add(ctx, plugin.Spec{Name: "h", Type: "http", URL: srv.URL})
	if err != nil {
		t.Fatalf("sharedHost.Add: %v", err)
	}

	activeRegistry := tool.NewRegistry()
	siblingRegistry := tool.NewRegistry()
	for _, mt := range tools {
		activeRegistry.Add(mt)
		siblingRegistry.Add(mt)
	}
	activeCtrl := control.New(control.Options{Host: sharedHost, Registry: activeRegistry, PluginCtx: context.Background()})
	siblingCtrl := control.New(control.Options{Host: sharedHost, Registry: siblingRegistry, PluginCtx: context.Background()})
	app := NewApp()
	app.tabs = map[string]*WorkspaceTab{
		"active": {
			ID:            "active",
			Scope:         "global",
			WorkspaceRoot: dir,
			Ready:         true,
			Ctrl:          activeCtrl,
			SharedHostKey: dir,
			disabledMCP:   map[string]ServerView{},
		},
		"sibling": {
			ID:            "sibling",
			Scope:         "global",
			WorkspaceRoot: dir,
			Ready:         true,
			Ctrl:          siblingCtrl,
			SharedHostKey: dir,
			disabledMCP:   map[string]ServerView{},
		},
	}
	app.activeTabID = "active"

	if err := app.SetMCPServerEnabled("h", false); err != nil {
		t.Fatalf("SetMCPServerEnabled(h,false): %v", err)
	}
	if _, found := activeRegistry.Get("mcp__h__greet"); found {
		t.Fatal("active tab still has h tools after disabling the shared server")
	}
	if _, found := siblingRegistry.Get("mcp__h__greet"); !found {
		t.Fatal("sibling tab lost h tools when active tab disabled the shared server")
	}
	if !sharedHost.HasClient("h") {
		t.Fatal("shared host client was removed by a per-tab disable")
	}
	view := app.Capabilities()
	if len(view.Servers) != 1 || view.Servers[0].Name != "h" || view.Servers[0].Status != "disabled" {
		t.Fatalf("Capabilities after disable = %+v, want h disabled for the active tab", view.Servers)
	}

	if err := app.SetMCPServerEnabled("h", true); err != nil {
		t.Fatalf("SetMCPServerEnabled(h,true): %v", err)
	}
	if _, found := activeRegistry.Get("mcp__h__greet"); !found {
		t.Fatal("active tab did not re-register h tools from the existing shared client")
	}
	view = app.Capabilities()
	if len(view.Servers) != 1 || view.Servers[0].Name != "h" || view.Servers[0].Status != "connected" {
		t.Fatalf("Capabilities after re-enable = %+v, want h connected for the active tab", view.Servers)
	}
}

func TestAuthorizeAndConnectMCPServerStartsProjectOnlyOnce(t *testing.T) {
	gateAddr, attempts := newDesktopMCPStartGate(t, func(_ int, conn net.Conn) {
		_, _ = conn.Write([]byte{1})
	})
	fixture := newGatedDesktopMCPLaunchFixture(t, gateAddr)
	waitForDesktopMCPStartAttempt(t, attempts, 1)
	oldSiblingTool, found := fixture.siblingRegistry.Get("mcp__h__greet")
	if !found {
		t.Fatal("sibling registry missing initial h tool")
	}

	if err := fixture.app.AuthorizeAndConnectMCPServer("h"); err != nil {
		t.Fatalf("AuthorizeAndConnectMCPServer(h): %v", err)
	}
	waitForDesktopMCPStartAttempt(t, attempts, 2)
	select {
	case attempt := <-attempts:
		t.Fatalf("project authorization started a temporary connection process (unexpected attempt %d)", attempt)
	case <-time.After(250 * time.Millisecond):
	}
	if !fixture.sharedHost.HasClient("h") {
		t.Fatal("project authorization did not leave h connected")
	}
	if _, found := fixture.activeRegistry.Get("mcp__h__greet"); !found {
		t.Fatal("active registry was not refreshed after project authorization")
	}
	newSiblingTool, found := fixture.siblingRegistry.Get("mcp__h__greet")
	if !found || newSiblingTool == oldSiblingTool {
		t.Fatal("sibling registry did not receive the single new project connection")
	}
	if _, found := fixture.disabledRegistry.Get("mcp__h__greet"); found {
		t.Fatal("project authorization re-enabled h in a disabled sibling tab")
	}
}

func TestReconnectMCPServerRefreshesEverySharedHostRegistry(t *testing.T) {
	fixture := newGatedDesktopMCPLaunchFixture(t, "")
	oldSiblingTool, found := fixture.siblingRegistry.Get("mcp__h__greet")
	if !found {
		t.Fatal("sibling registry missing initial h tool")
	}
	if err := fixture.app.ReconnectMCPServer("h"); err != nil {
		t.Fatalf("ReconnectMCPServer(h): %v", err)
	}
	if !fixture.sharedHost.HasClient("h") {
		t.Fatal("shared host did not reconnect h")
	}
	if _, found := fixture.activeRegistry.Get("mcp__h__greet"); !found {
		t.Fatal("active registry was not refreshed")
	}
	newSiblingTool, found := fixture.siblingRegistry.Get("mcp__h__greet")
	if !found || newSiblingTool == oldSiblingTool {
		t.Fatal("sibling registry retained the tool backed by the disconnected client")
	}
	if _, found := fixture.disabledRegistry.Get("mcp__h__greet"); found {
		t.Fatal("reconnect re-enabled a tab where the server was disabled")
	}
}

func TestReconnectMCPServerUsesEffectiveProjectConfigWhenUserNameIsShadowed(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	userServer := desktopMCPHTTPServerWithTool(t, "user-shadow", "user_tool")
	defer userServer.Close()
	projectServer := desktopMCPHTTPServerWithTool(t, "project-effective", "project_tool")
	defer projectServer.Close()

	userCfg := config.LoadForEdit(config.UserConfigPath())
	userCfg.Plugins = []config.PluginEntry{{Name: "h", Type: "http", URL: userServer.URL}}
	if err := userCfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), fmt.Appendf(nil, `
[[plugins]]
name = "h"
type = "http"
url = %q
`, projectServer.URL), 0o644); err != nil {
		t.Fatal(err)
	}

	host := plugin.NewHost()
	t.Cleanup(host.Close)
	registry := tool.NewRegistry()
	ctrl := control.New(control.Options{
		Host: host, Registry: registry, PluginCtx: context.Background(), WorkspaceRoot: dir,
		MCPConfigureSpec: func(spec *plugin.Spec) {
			// This test isolates effective-source selection from the project launch
			// approval flow, which has its own end-to-end coverage.
			spec.RequireLaunchApproval = false
			spec.Authorized = true
		},
	})
	app := NewApp()
	app.tabs = map[string]*WorkspaceTab{
		"active": {
			ID: "active", Scope: "global", WorkspaceRoot: dir, Ready: true,
			Ctrl: ctrl, disabledMCP: map[string]ServerView{},
		},
	}
	app.activeTabID = "active"

	if err := app.ReconnectMCPServer("h"); err != nil {
		t.Fatalf("ReconnectMCPServer(h): %v", err)
	}
	if _, found := registry.Get("mcp__h__project_tool"); !found {
		t.Fatal("reconnect did not use the effective project MCP configuration")
	}
	if _, found := registry.Get("mcp__h__user_tool"); found {
		t.Fatal("reconnect used the shadowed user MCP configuration")
	}
}

func TestUpdateMCPServerRefreshesEverySharedHostRegistry(t *testing.T) {
	fixture := newGatedDesktopMCPLaunchFixture(t, "")
	oldSiblingTool, found := fixture.siblingRegistry.Get("mcp__h__greet")
	if !found {
		t.Fatal("sibling registry missing initial h tool")
	}
	root := fixture.app.tabs["active"].WorkspaceRoot
	cfg, err := config.LoadForRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	entry, found := findPluginEntry(cfg.Plugins, "h")
	if !found {
		t.Fatal("fixture config missing h")
	}
	if err := fixture.app.UpdateMCPServer("h", MCPServerInput{
		Name: "h", Transport: entry.Type, Command: entry.Command, Args: entry.Args,
	}); err != nil {
		t.Fatalf("UpdateMCPServer(h): %v", err)
	}
	if !fixture.sharedHost.HasClient("h") {
		t.Fatal("shared host did not reconnect h")
	}
	if _, found := fixture.activeRegistry.Get("mcp__h__greet"); !found {
		t.Fatal("active registry was not refreshed")
	}
	newSiblingTool, found := fixture.siblingRegistry.Get("mcp__h__greet")
	if !found || newSiblingTool == oldSiblingTool {
		t.Fatal("sibling registry retained the tool backed by the disconnected client")
	}
	if _, found := fixture.disabledRegistry.Get("mcp__h__greet"); found {
		t.Fatal("update re-enabled a tab where the server was disabled")
	}
}

func TestClearMCPServerAuthenticationClearsEverySharedHostRegistry(t *testing.T) {
	fixture := newGatedDesktopMCPLaunchFixture(t, "")
	if err := fixture.app.ClearMCPServerAuthentication("h"); err != nil {
		t.Fatalf("ClearMCPServerAuthentication(h): %v", err)
	}
	if fixture.sharedHost.HasClient("h") {
		t.Fatal("shared host retained h after clearing authentication")
	}
	for label, registry := range map[string]*tool.Registry{
		"active": fixture.activeRegistry, "sibling": fixture.siblingRegistry, "disabled": fixture.disabledRegistry,
	} {
		if _, found := registry.Get("mcp__h__greet"); found {
			t.Fatalf("%s registry retained h after clearing authentication", label)
		}
	}
}

func TestRemoveMCPServerClearsEverySharedHostRegistry(t *testing.T) {
	fixture := newGatedDesktopMCPLaunchFixture(t, "")
	if err := fixture.app.RemoveMCPServer("h"); err != nil {
		t.Fatalf("RemoveMCPServer(h): %v", err)
	}
	if fixture.sharedHost.HasClient("h") {
		t.Fatal("shared host retained the removed server")
	}
	for label, registry := range map[string]*tool.Registry{
		"active": fixture.activeRegistry, "sibling": fixture.siblingRegistry, "disabled": fixture.disabledRegistry,
	} {
		if _, found := registry.Get("mcp__h__greet"); found {
			t.Fatalf("%s registry retained the removed server tool", label)
		}
	}
	for id, tab := range fixture.app.tabs {
		if _, disabled := tab.disabledMCP["h"]; disabled {
			t.Fatalf("tab %s retained removed-server disabled state", id)
		}
	}
}

type gatedDesktopMCPLaunchFixture struct {
	app              *App
	sharedHost       *plugin.Host
	activeRegistry   *tool.Registry
	siblingRegistry  *tool.Registry
	disabledRegistry *tool.Registry
}

func newGatedDesktopMCPLaunchFixture(t *testing.T, startGateAddr string) gatedDesktopMCPLaunchFixture {
	t.Helper()
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	singleInstanceAddr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	gateConfig := ""
	if startGateAddr != "" {
		gateConfig = fmt.Sprintf("DESKTOP_MCP_START_GATE_ADDR = %q\n", startGateAddr)
	}
	helperArgs := []string{"-test.run=TestDesktopMCPHelperProcess", "--"}
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), fmt.Appendf(nil, `
[[plugins]]
name = "h"
command = %q
args = ["-test.run=TestDesktopMCPHelperProcess", "--"]

[plugins.env]
GO_WANT_DESKTOP_MCP_HELPER = "1"
DESKTOP_MCP_SINGLE_INSTANCE_ADDR = %q
%s
[sandbox]
network = true
`, exe, singleInstanceAddr, gateConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	entry := config.PluginEntry{
		Name: "h", Command: exe, Args: helperArgs,
		Env: map[string]string{
			"GO_WANT_DESKTOP_MCP_HELPER":       "1",
			"DESKTOP_MCP_SINGLE_INSTANCE_ADDR": singleInstanceAddr,
		},
	}
	if startGateAddr != "" {
		entry.Env["DESKTOP_MCP_START_GATE_ADDR"] = startGateAddr
	}
	cfg, err := config.LoadForRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	runtimeSpecs := boot.PluginSpecsForRootWithOptions([]config.PluginEntry{entry}, dir, boot.PluginSpecOptions{
		DefaultCallTimeout: time.Duration(cfg.MCPCallTimeoutSeconds()) * time.Second,
		LaunchManager:      mcplaunch.ForWorkspace(config.ReasonixHomeDir(), dir),
		ConfigSource:       "workspace_config",
		StateHome:          config.ReasonixHomeDir(),
		WriterRoots:        cfg.WriteRootsForRoot(dir),
		ForbidReadRoots:    cfg.ForbidReadRootsForRoot(dir),
		Network:            cfg.Sandbox.Network,
	})
	if len(runtimeSpecs) != 1 {
		t.Fatalf("runtime specs = %d, want 1", len(runtimeSpecs))
	}
	runtimeSpec := runtimeSpecs[0]
	configure := func(spec *plugin.Spec) { *spec = runtimeSpec }
	lifeCtx, lifeCancel := context.WithCancel(context.Background())
	t.Cleanup(lifeCancel)
	callCtx, callCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer callCancel()
	sharedHost := plugin.NewHost()
	t.Cleanup(sharedHost.Close)
	tools, err := sharedHost.AddWithLifecycle(lifeCtx, callCtx, runtimeSpec)
	if err != nil {
		t.Fatalf("sharedHost.Add: %v", err)
	}

	activeRegistry := tool.NewRegistry()
	siblingRegistry := tool.NewRegistry()
	disabledRegistry := tool.NewRegistry()
	for _, mt := range tools {
		activeRegistry.Add(mt)
		siblingRegistry.Add(mt)
		disabledRegistry.Add(mt)
	}
	activeCtrl := control.New(control.Options{
		Host: sharedHost, Registry: activeRegistry, PluginCtx: lifeCtx,
		MCPConfigureSpec: configure, WorkspaceRoot: dir,
	})
	siblingCtrl := control.New(control.Options{
		Host: sharedHost, Registry: siblingRegistry, PluginCtx: lifeCtx,
		MCPConfigureSpec: configure, WorkspaceRoot: dir,
	})
	disabledCtrl := control.New(control.Options{
		Host: sharedHost, Registry: disabledRegistry, PluginCtx: lifeCtx,
		MCPConfigureSpec: configure, WorkspaceRoot: dir,
	})
	disabledCtrl.UnregisterMCPServerTools("h")
	app := NewApp()
	app.tabs = map[string]*WorkspaceTab{
		"active": {
			ID: "active", Scope: "global", WorkspaceRoot: dir, Ready: true,
			Ctrl: activeCtrl, SharedHostKey: dir, disabledMCP: map[string]ServerView{},
		},
		"sibling": {
			ID: "sibling", Scope: "global", WorkspaceRoot: dir, Ready: true,
			Ctrl: siblingCtrl, SharedHostKey: dir, disabledMCP: map[string]ServerView{},
		},
		"disabled": {
			ID: "disabled", Scope: "global", WorkspaceRoot: dir, Ready: true,
			Ctrl: disabledCtrl, SharedHostKey: dir,
			disabledMCP: map[string]ServerView{"h": {Name: "h", Status: "disabled"}},
		},
	}
	app.activeTabID = "active"
	return gatedDesktopMCPLaunchFixture{
		app: app, sharedHost: sharedHost,
		activeRegistry: activeRegistry, siblingRegistry: siblingRegistry, disabledRegistry: disabledRegistry,
	}
}

func newDesktopMCPStartGate(t *testing.T, handle func(attempt int, conn net.Conn)) (string, <-chan int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	attempts := make(chan int, 8)
	go func() {
		for attempt := 1; ; attempt++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			attempts <- attempt
			handle(attempt, conn)
			_ = conn.Close()
		}
	}()
	return listener.Addr().String(), attempts
}

func waitForDesktopMCPStartAttempt(t *testing.T, attempts <-chan int, want int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case got := <-attempts:
			if got == want {
				return
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for MCP start attempt %d", want)
		}
	}
}

func TestAuthorizeAndConnectMCPServerSerializesConcurrentDisable(t *testing.T) {
	releaseConnection := make(chan struct{})
	gateAddr, attempts := newDesktopMCPStartGate(t, func(attempt int, conn net.Conn) {
		if attempt == 2 {
			<-releaseConnection
		}
		_, _ = conn.Write([]byte{1})
	})
	fixture := newGatedDesktopMCPLaunchFixture(t, gateAddr)

	disableEntered := make(chan struct{})
	var disableOnce sync.Once
	fixture.app.runtimeMutationBeforeLockHook = func(operation string) {
		if operation == "set-enabled" {
			disableOnce.Do(func() { close(disableEntered) })
		}
	}
	authorizeDone := make(chan error, 1)
	go func() { authorizeDone <- fixture.app.AuthorizeAndConnectMCPServer("h") }()
	waitForDesktopMCPStartAttempt(t, attempts, 2)
	disableDone := make(chan error, 1)
	go func() { disableDone <- fixture.app.SetMCPServerEnabled("h", false) }()
	select {
	case <-disableEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent disable did not reach the MCP lifecycle lock")
	}
	select {
	case err := <-disableDone:
		t.Fatalf("concurrent disable bypassed authorization serialization: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(releaseConnection)
	if err := <-authorizeDone; err != nil {
		t.Fatalf("AuthorizeAndConnectMCPServer(h): %v", err)
	}
	if err := <-disableDone; err != nil {
		t.Fatalf("SetMCPServerEnabled(h,false): %v", err)
	}
	if _, found := fixture.activeRegistry.Get("mcp__h__greet"); found {
		t.Fatal("authorization reconnect overrode the later per-tab disable")
	}
	if _, disabled := fixture.app.tabs["active"].disabledMCP["h"]; !disabled {
		t.Fatal("active tab did not retain the later disable decision")
	}
	if _, found := fixture.siblingRegistry.Get("mcp__h__greet"); !found {
		t.Fatal("active-tab disable removed the shared MCP from its enabled sibling")
	}
}

// RemovePlugin disconnects the uninstalled plugin's MCP servers, so it must
// serialize on the MCP lifecycle lock: an unlocked disconnect interleaving
// with authorization lets the reconnect relaunch the just-removed
// server from its stale snapshot. The plugin does not need to exist — the
// lock is taken before the uninstall runs, which is the contract under test.
func TestRemovePluginSerializesWithMCPAuthorization(t *testing.T) {
	releaseConnection := make(chan struct{})
	gateAddr, attempts := newDesktopMCPStartGate(t, func(attempt int, conn net.Conn) {
		if attempt == 2 {
			<-releaseConnection
		}
		_, _ = conn.Write([]byte{1})
	})
	fixture := newGatedDesktopMCPLaunchFixture(t, gateAddr)

	removeEntered := make(chan struct{})
	var removeOnce sync.Once
	fixture.app.runtimeMutationBeforeLockHook = func(operation string) {
		if operation == "remove-plugin" {
			removeOnce.Do(func() { close(removeEntered) })
		}
	}
	authorizeDone := make(chan error, 1)
	go func() { authorizeDone <- fixture.app.AuthorizeAndConnectMCPServer("h") }()
	waitForDesktopMCPStartAttempt(t, attempts, 2)
	removeDone := make(chan error, 1)
	go func() { removeDone <- fixture.app.RemovePlugin("not-an-installed-plugin") }()
	select {
	case <-removeEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("RemovePlugin did not reach the MCP lifecycle lock")
	}
	select {
	case err := <-removeDone:
		t.Fatalf("RemovePlugin bypassed authorization serialization: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(releaseConnection)
	if err := <-authorizeDone; err != nil {
		t.Fatalf("AuthorizeAndConnectMCPServer(h): %v", err)
	}
	// The uninstall itself is expected to fail (the plugin is not installed);
	// only the ordering matters. It must complete once the lock is free.
	select {
	case <-removeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("RemovePlugin did not complete after the trust connection released the lock")
	}
	if _, found := fixture.activeRegistry.Get("mcp__h__greet"); !found {
		t.Fatal("authorization reconnect result was lost after the serialized RemovePlugin")
	}
}

// installGatedTestPluginPackage registers an installed plugin package whose
// manifest declares the gated fixture's MCP server, so RemovePlugin exercises
// the real uninstall and MCP disconnect flow. Returns the plugin root.
func installGatedTestPluginPackage(t *testing.T, mcpServerName string) string {
	t.Helper()
	reasonixHome := config.ReasonixHomeDir()
	root := filepath.Join(reasonixHome, "plugins", "review-helper")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, pluginpkg.NativeManifest), fmt.Appendf(nil, `{"apiVersion": "reasonix.io/plugin/v2",
  "name": "review-helper",
  "version": "1.0.0",
  "mcpServers": {
    %q: { "type": "stdio", "command": "helper" }
  }
}`, mcpServerName), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pluginpkg.Upsert(reasonixHome, pluginpkg.InstalledPlugin{
		Name:         "review-helper",
		Root:         "plugins/review-helper",
		Version:      "1.0.0",
		ManifestKind: "reasonix",
		Enabled:      true,
	}); err != nil {
		t.Fatal(err)
	}
	return root
}

func installedPluginNamed(t *testing.T, name string) bool {
	t.Helper()
	st, err := pluginpkg.LoadState(config.ReasonixHomeDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range st.Plugins {
		if p.Name == name {
			return true
		}
	}
	return false
}

// A global plugin uninstall must clean every runtime, not only the active tab:
// sibling registries on the shared Host would otherwise keep provider-visible
// tools backed by the closed client, and other workspaces would keep running
// the uninstalled server.
func TestRemovePluginDisconnectsEveryRuntime(t *testing.T) {
	fixture := newGatedDesktopMCPLaunchFixture(t, "")
	pluginRoot := installGatedTestPluginPackage(t, "h")

	if err := fixture.app.RemovePlugin("review-helper"); err != nil {
		t.Fatalf("RemovePlugin(review-helper): %v", err)
	}
	if fixture.sharedHost.HasClient("h") {
		t.Fatal("uninstall left the shared MCP client connected")
	}
	for name, reg := range map[string]*tool.Registry{
		"active":   fixture.activeRegistry,
		"sibling":  fixture.siblingRegistry,
		"disabled": fixture.disabledRegistry,
	} {
		if _, found := reg.Get("mcp__h__greet"); found {
			t.Fatalf("%s registry still exposes the uninstalled MCP tool", name)
		}
	}
	if _, err := os.Stat(pluginRoot); !os.IsNotExist(err) {
		t.Fatalf("plugin root still present after uninstall (err=%v)", err)
	}
	if installedPluginNamed(t, "review-helper") {
		t.Fatal("plugin state still lists the uninstalled plugin")
	}
}

// The pre-lock active-work check can go stale during the lifecycle-lock wait.
// Work that starts mid-wait must fail the removal before anything is deleted;
// the old order deleted the plugin first and only then reported the failure.
func TestRemovePluginRechecksActiveWorkUnderLock(t *testing.T) {
	releaseConnection := make(chan struct{})
	gateAddr, attempts := newDesktopMCPStartGate(t, func(attempt int, conn net.Conn) {
		if attempt == 2 {
			<-releaseConnection
		}
		_, _ = conn.Write([]byte{1})
	})
	fixture := newGatedDesktopMCPLaunchFixture(t, gateAddr)
	installGatedTestPluginPackage(t, "h")

	removeEntered := make(chan struct{})
	var removeOnce sync.Once
	fixture.app.runtimeMutationBeforeLockHook = func(operation string) {
		if operation == "remove-plugin" {
			removeOnce.Do(func() { close(removeEntered) })
		}
	}
	authorizeDone := make(chan error, 1)
	go func() { authorizeDone <- fixture.app.AuthorizeAndConnectMCPServer("h") }()
	waitForDesktopMCPStartAttempt(t, attempts, 2)
	removeDone := make(chan error, 1)
	go func() { removeDone <- fixture.app.RemovePlugin("review-helper") }()
	select {
	case <-removeEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("RemovePlugin did not reach the MCP lifecycle lock")
	}
	// While RemovePlugin waits for the lock, background work starts on the
	// active tab — exactly the window the pre-lock check cannot see.
	busy := newBackgroundJobController(t, "remove-plugin-active-work")
	fixture.app.mu.Lock()
	fixture.app.tabs["active"].Ctrl = busy
	fixture.app.mu.Unlock()
	close(releaseConnection)
	if err := <-authorizeDone; err != nil {
		t.Fatalf("AuthorizeAndConnectMCPServer(h): %v", err)
	}
	err := <-removeDone
	if err == nil || !strings.Contains(err.Error(), "stop background jobs") {
		t.Fatalf("RemovePlugin during background work error = %v, want active-work guard", err)
	}
	if !installedPluginNamed(t, "review-helper") {
		t.Fatal("active-work guard fired only after the plugin was already uninstalled")
	}
}

// A global uninstall disconnects every runtime, so the busy guard must cover
// every runtime too: a background job on a sibling tab must fail the removal
// before anything is deleted, not silently lose its plugin MCP mid-run.
func TestRemovePluginRejectsBusySiblingRuntime(t *testing.T) {
	fixture := newGatedDesktopMCPLaunchFixture(t, "")
	installGatedTestPluginPackage(t, "h")
	busy := newBackgroundJobController(t, "sibling-busy")
	fixture.app.mu.Lock()
	fixture.app.tabs["sibling"].Ctrl = busy
	fixture.app.mu.Unlock()

	err := fixture.app.RemovePlugin("review-helper")
	if err == nil || !strings.Contains(err.Error(), "stop background jobs") {
		t.Fatalf("RemovePlugin with busy sibling error = %v, want active-work guard", err)
	}
	if !installedPluginNamed(t, "review-helper") {
		t.Fatal("plugin was uninstalled despite a busy sibling runtime")
	}
	if !fixture.sharedHost.HasClient("h") {
		t.Fatal("busy-sibling guard still disconnected the shared MCP client")
	}
	if _, found := fixture.activeRegistry.Get("mcp__h__greet"); !found {
		t.Fatal("busy-sibling guard still removed active registry tools")
	}
}

// Detached runtimes keep running after their tab is closed; the uninstall busy
// guard must see them through the same gate sweep as visible tabs.
func TestRemovePluginRejectsBusyDetachedRuntime(t *testing.T) {
	fixture := newGatedDesktopMCPLaunchFixture(t, "")
	installGatedTestPluginPackage(t, "h")
	busy := newBackgroundJobController(t, "detached-busy")
	fixture.app.mu.Lock()
	fixture.app.detachedSessions = map[string]*WorkspaceTab{
		"detached": {
			ID: "detached", Scope: "global", Ready: true,
			Ctrl: busy, disabledMCP: map[string]ServerView{},
		},
	}
	fixture.app.mu.Unlock()

	err := fixture.app.RemovePlugin("review-helper")
	if err == nil || !strings.Contains(err.Error(), "stop background jobs") {
		t.Fatalf("RemovePlugin with busy detached runtime error = %v, want active-work guard", err)
	}
	if !installedPluginNamed(t, "review-helper") {
		t.Fatal("plugin was uninstalled despite a busy detached runtime")
	}
}

// A turn start holds its tab's turn gate before the controller reports active
// work, so an idle check done without the gate can go stale immediately. The
// authorization must wait on the sibling's gate — never disconnect first — and must
// fail once the gated re-check sees the started work.
func TestAuthorizeAndConnectMCPServerWaitsForSiblingTurnGate(t *testing.T) {
	gateAddr, attempts := newDesktopMCPStartGate(t, func(attempt int, conn net.Conn) {
		_, _ = conn.Write([]byte{1})
	})
	fixture := newGatedDesktopMCPLaunchFixture(t, gateAddr)
	waitForDesktopMCPStartAttempt(t, attempts, 1) // drain the fixture's initial connect
	sibling := fixture.app.tabs["sibling"]

	// Simulate the racing turn: it takes the gate first, and only transitions
	// its controller to busy while holding it.
	sibling.turnStartMu.Lock()
	authorizeDone := make(chan error, 1)
	go func() { authorizeDone <- fixture.app.AuthorizeAndConnectMCPServer("h") }()
	select {
	case got := <-attempts:
		t.Fatalf("trust connection launched (attempt %d) while a sibling turn gate was held", got)
	case err := <-authorizeDone:
		t.Fatalf("AuthorizeAndConnectMCPServer returned %v without waiting for the sibling turn gate", err)
	case <-time.After(700 * time.Millisecond):
	}
	if !fixture.sharedHost.HasClient("h") {
		t.Fatal("authorization disconnected the shared client while a sibling turn gate was held")
	}
	busy := newBackgroundJobController(t, "sibling-turn")
	fixture.app.mu.Lock()
	sibling.Ctrl = busy
	fixture.app.mu.Unlock()
	sibling.turnStartMu.Unlock()

	err := <-authorizeDone
	if err == nil || !strings.Contains(err.Error(), "stop background jobs") {
		t.Fatalf("AuthorizeAndConnectMCPServer after sibling turn start error = %v, want active-work guard", err)
	}
	if !fixture.sharedHost.HasClient("h") {
		t.Fatal("failed authorization left the shared client disconnected")
	}
	if _, found := fixture.siblingRegistry.Get("mcp__h__greet"); !found {
		t.Fatal("authorization stripped the busy sibling of its MCP tools")
	}
	if _, found := fixture.activeRegistry.Get("mcp__h__greet"); !found {
		t.Fatal("authorization stripped the active tab of its MCP tools")
	}
}

// waitForRuntimeAdmissionBarrier polls until the work-admission write lock is
// held, marking the point where a lifecycle mutation froze new admissions.
func waitForRuntimeAdmissionBarrier(t *testing.T, app *App) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if app.runtimeAdmissionMu.TryRLock() {
			app.runtimeAdmissionMu.RUnlock()
			time.Sleep(2 * time.Millisecond)
			continue
		}
		return
	}
	t.Fatal("lifecycle mutation never acquired the work-admission barrier")
}

func TestBridgeDriveReleasesRuntimeAdmissionWhenTakeoverWasReclaimed(t *testing.T) {
	fixture := newGatedDesktopMCPLaunchFixture(t, "")
	fixture.app.tabs["active"].sink = &tabEventSink{tabID: "active", app: fixture.app}
	fixture.app.botBridge = &botBridgeHub{
		takeovers:    make(map[string]bot.DesktopWatchRoute),
		takeoverTabs: make(map[string]string),
	}

	err := fixture.app.bridgeDrive("active", "hello", bot.DesktopWatchRoute{})
	if err == nil || !strings.Contains(err.Error(), "接管已解除") {
		t.Fatalf("bridgeDrive error = %v, want lost-takeover error", err)
	}
	if !fixture.app.runtimeAdmissionMu.TryLock() {
		t.Fatal("bridgeDrive leaked the runtime-admission read lock")
	}
	fixture.app.runtimeAdmissionMu.Unlock()
}

func TestBeginTabTurnWorkspaceRepairStaysOutsideLifecycleAdmission(t *testing.T) {
	fixture := newStaleWorkspaceBindingFixture(t, "admission_writer")
	fixture.tab.reconcileMu.Lock()

	turnDone := make(chan error, 1)
	go func() {
		admission, _, err := fixture.app.beginTabTurn(fixture.tab.ID, false)
		if admission != nil {
			admission.abort()
		}
		turnDone <- err
	}()
	writerRebuildLocked := make(chan struct{})
	writerAdmissionLocked := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		fixture.app.runtimeRebuildMu.Lock()
		close(writerRebuildLocked)
		fixture.app.runtimeAdmissionMu.Lock()
		close(writerAdmissionLocked)
		fixture.app.runtimeAdmissionMu.Unlock()
		fixture.app.runtimeRebuildMu.Unlock()
		close(writerDone)
	}()
	<-writerRebuildLocked
	select {
	case <-writerAdmissionLocked:
		// The repair is still blocked on reconcileMu; acquiring the lifecycle
		// writer here proves no slow repair/build I/O owns the read side.
	case <-time.After(5 * time.Second):
		fixture.tab.reconcileMu.Unlock()
		t.Fatal("workspace repair held runtimeAdmissionMu while waiting")
	}
	fixture.tab.reconcileMu.Unlock()

	select {
	case err := <-turnDone:
		if err != nil {
			t.Fatalf("beginTabTurn after workspace repair: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("workspace repair did not complete after lifecycle writer released")
	}
	select {
	case <-writerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle writer did not complete after repaired turn admission")
	}
}

func TestAuthorizeAndConnectMCPServerSerializesCloseOfCapturedRuntime(t *testing.T) {
	releaseConnection := make(chan struct{})
	gateAddr, attempts := newDesktopMCPStartGate(t, func(attempt int, conn net.Conn) {
		if attempt == 2 {
			<-releaseConnection
		}
		_, _ = conn.Write([]byte{1})
	})
	fixture := newGatedDesktopMCPLaunchFixture(t, gateAddr)
	waitForDesktopMCPStartAttempt(t, attempts, 1)

	dir := fixture.app.tabs["active"].WorkspaceRoot
	otherRoot := robustTempDir(t)
	otherHost := plugin.NewHost()
	t.Cleanup(otherHost.Close)
	otherCtrl := control.New(control.Options{Host: otherHost, WorkspaceRoot: otherRoot})
	t.Cleanup(otherCtrl.Close)
	fixture.app.tabs = map[string]*WorkspaceTab{
		"active": fixture.app.tabs["active"],
		"other": {
			ID: "other", Scope: "project", WorkspaceRoot: otherRoot, Ready: true,
			Ctrl: otherCtrl, disabledMCP: map[string]ServerView{},
		},
	}
	fixture.app.tabOrder = []string{"active", "other"}
	fixture.app.activeTabID = "active"
	fixture.app.sharedHosts = map[string]*sharedPluginHost{
		dir: {host: fixture.sharedHost, refs: 1},
	}

	authorizeDone := make(chan error, 1)
	go func() { authorizeDone <- fixture.app.AuthorizeAndConnectMCPServer("h") }()
	waitForDesktopMCPStartAttempt(t, attempts, 2)
	closeDone := make(chan error, 1)
	go func() { closeDone <- fixture.app.CloseTab("active") }()
	select {
	case err := <-closeDone:
		t.Fatalf("CloseTab bypassed the MCP lifecycle barrier: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	close(releaseConnection)
	if err := <-authorizeDone; err != nil {
		t.Fatalf("AuthorizeAndConnectMCPServer(h): %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("CloseTab(active) after trust: %v", err)
	}
}

func TestCloseTabWaitsForPendingTurnAdmission(t *testing.T) {
	fixture := newGatedDesktopMCPLaunchFixture(t, "")
	admission, _, err := fixture.app.beginTabTurn("active", false)
	if err != nil {
		t.Fatalf("beginTabTurn(active): %v", err)
	}
	released := false
	defer func() {
		if !released {
			admission.abort()
		}
	}()

	closeDone := make(chan error, 1)
	go func() { closeDone <- fixture.app.CloseTab("active") }()
	select {
	case err := <-closeDone:
		admission.abort()
		released = true
		t.Fatalf("CloseTab bypassed a pending turn admission and closed its controller: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	admission.abort()
	released = true
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("CloseTab(active) after turn admission release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CloseTab did not resume after the pending turn admission was released")
	}
}

func TestCloseTabRemainsVisibleToPendingMCPHostGateSnapshot(t *testing.T) {
	fixture := newGatedDesktopMCPLaunchFixture(t, "")
	active := fixture.app.tabs["active"]
	active.turnStartMu.Lock()

	authorizeDone := make(chan error, 1)
	go func() { authorizeDone <- fixture.app.AuthorizeAndConnectMCPServer("h") }()
	waitForRuntimeAdmissionBarrier(t, fixture.app)

	closeDone := make(chan error, 1)
	go func() { closeDone <- fixture.app.CloseTab("active") }()
	time.Sleep(300 * time.Millisecond)
	fixture.app.mu.RLock()
	stillVisible := fixture.app.tabs["active"] == active
	fixture.app.mu.RUnlock()
	if !stillVisible {
		active.turnStartMu.Unlock()
		t.Fatal("CloseTab unlinked the runtime before the pending MCP Host gate snapshot")
	}
	select {
	case err := <-closeDone:
		active.turnStartMu.Unlock()
		t.Fatalf("CloseTab bypassed the lifecycle barrier: %v", err)
	default:
	}

	active.turnStartMu.Unlock()
	if err := <-authorizeDone; err != nil {
		t.Fatalf("AuthorizeAndConnectMCPServer(h): %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("CloseTab(active): %v", err)
	}
}

func TestAuthorizeAndConnectMCPServerKeepsInvokingWorkspaceWhenActiveTabChanges(t *testing.T) {
	fixture := newGatedDesktopMCPLaunchFixture(t, "")
	otherRoot := robustTempDir(t)
	otherCtrl := control.New(control.Options{Host: plugin.NewHost(), WorkspaceRoot: otherRoot})
	t.Cleanup(otherCtrl.Close)
	fixture.app.tabs["other"] = &WorkspaceTab{
		ID: "other", Scope: "project", WorkspaceRoot: otherRoot, Ready: true,
		Ctrl: otherCtrl, disabledMCP: map[string]ServerView{},
	}
	fixture.app.tabOrder = []string{"active", "sibling", "disabled", "other"}

	sibling := fixture.app.tabs["sibling"]
	sibling.turnStartMu.Lock()
	authorizeDone := make(chan error, 1)
	go func() { authorizeDone <- fixture.app.AuthorizeAndConnectMCPServer("h") }()
	waitForRuntimeAdmissionBarrier(t, fixture.app)
	if err := fixture.app.SetActiveTab("other"); err != nil {
		sibling.turnStartMu.Unlock()
		t.Fatalf("SetActiveTab(other): %v", err)
	}
	sibling.turnStartMu.Unlock()

	if err := <-authorizeDone; err != nil {
		t.Fatalf("authorization operation drifted from its invoking workspace: %v", err)
	}
}

// Work admission — not tab-set stability — is the gate invariant: a runtime
// added after the gate snapshot must not be able to start a turn while the
// uninstall holds the barrier. The late turn goes through the real
// beginTabTurn admission path and must only be admitted after the uninstall.
func TestRemovePluginBlocksLateTurnAdmission(t *testing.T) {
	fixture := newGatedDesktopMCPLaunchFixture(t, "")
	installGatedTestPluginPackage(t, "h")
	dir := fixture.app.tabs["active"].WorkspaceRoot

	// Hold an existing tab's gate so the uninstall blocks mid-acquisition
	// with the admission barrier already held.
	sibling := fixture.app.tabs["sibling"]
	sibling.turnStartMu.Lock()
	removeDone := make(chan error, 1)
	go func() { removeDone <- fixture.app.RemovePlugin("review-helper") }()
	waitForRuntimeAdmissionBarrier(t, fixture.app)

	lateCtrl := control.New(control.Options{Host: plugin.NewHost(), WorkspaceRoot: dir})
	t.Cleanup(lateCtrl.Close)
	fixture.app.mu.Lock()
	fixture.app.tabs["late"] = &WorkspaceTab{
		ID: "late", Scope: "global", WorkspaceRoot: dir, Ready: true,
		Ctrl: lateCtrl, disabledMCP: map[string]ServerView{},
	}
	fixture.app.mu.Unlock()
	type admission struct {
		turn *tabTurnAdmission
		ctrl control.SessionAPI
		err  error
	}
	admitted := make(chan admission, 1)
	go func() {
		turn, ctrl, err := fixture.app.beginTabTurn("late", false)
		admitted <- admission{turn: turn, ctrl: ctrl, err: err}
	}()
	select {
	case got := <-admitted:
		t.Fatalf("late turn was admitted (err=%v) while the uninstall held the admission barrier", got.err)
	case <-time.After(300 * time.Millisecond):
	}

	sibling.turnStartMu.Unlock()
	if err := <-removeDone; err != nil {
		t.Fatalf("RemovePlugin(review-helper): %v", err)
	}
	got := <-admitted
	if got.err != nil {
		t.Fatalf("late turn admission after uninstall: %v", got.err)
	}
	got.turn.abort()
	if installedPluginNamed(t, "review-helper") {
		t.Fatal("uninstall did not complete before the late turn was admitted")
	}
}

// A tab created during the 30s trust connection must not complete its async
// controller build — attaching to the shared Host mid-connection can relaunch a
// single-instance server or leave a registry the authorization never saw. The build
// goes through the real startTabControllerBuild path and must only run after
// the authorization releases the barrier.
func TestAuthorizeAndConnectMCPServerBlocksLateControllerBuild(t *testing.T) {
	releaseConnection := make(chan struct{})
	gateAddr, attempts := newDesktopMCPStartGate(t, func(attempt int, conn net.Conn) {
		if attempt == 2 {
			<-releaseConnection
		}
		_, _ = conn.Write([]byte{1})
	})
	fixture := newGatedDesktopMCPLaunchFixture(t, gateAddr)
	waitForDesktopMCPStartAttempt(t, attempts, 1) // drain the fixture's initial connect
	dir := fixture.app.tabs["active"].WorkspaceRoot

	authorizeDone := make(chan error, 1)
	go func() { authorizeDone <- fixture.app.AuthorizeAndConnectMCPServer("h") }()
	waitForDesktopMCPStartAttempt(t, attempts, 2) // connection launched: gates held

	late := &WorkspaceTab{
		ID: "late", Scope: "global", WorkspaceRoot: dir,
		disabledMCP: map[string]ServerView{},
	}
	late.sink = &tabEventSink{tabID: "late", app: fixture.app}
	fixture.app.mu.Lock()
	fixture.app.tabs["late"] = late
	fixture.app.mu.Unlock()
	buildDone := make(chan struct{})
	go func() {
		// a.ctx is nil in this fixture, so the build runs synchronously on
		// this goroutine — through the real buildTabControllerWithContext.
		fixture.app.startTabControllerBuild(late)
		close(buildDone)
	}()
	select {
	case <-buildDone:
		t.Fatal("late controller build completed while the trust connection held the admission barrier")
	case <-time.After(400 * time.Millisecond):
	}

	close(releaseConnection)
	if err := <-authorizeDone; err != nil {
		t.Fatalf("AuthorizeAndConnectMCPServer(h): %v", err)
	}
	select {
	case <-buildDone:
	case <-time.After(10 * time.Second):
		t.Fatal("late controller build never ran after the authorization released the barrier")
	}
	if !fixture.sharedHost.HasClient("h") {
		t.Fatal("authorization did not leave the shared client reconnected")
	}
}

func TestSetMCPServerEnabledRejectsBackgroundJobs(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	app.setTestCtrl(newBackgroundJobController(t, "mcp-enabled-job"), "")

	err := app.SetMCPServerEnabled("time", false)
	if err == nil || !strings.Contains(err.Error(), "stop background jobs") {
		t.Fatalf("SetMCPServerEnabled with background job error = %v, want active-work guard", err)
	}
	if tab := app.activeTab(); tab == nil || len(tab.disabledMCP) != 0 {
		t.Fatalf("disabled MCP state changed after rejected toggle: %+v", tab)
	}
}

func TestEditAndRemoveConfiguredMCPWithBuiltInName(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(`
[[plugins]]
name = "time"
command = "custom-time"
args = ["serve"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()
	app.activeTab().disabledMCP["time"] = ServerView{Name: "time", Status: "disabled", Enabled: false}

	if err := app.UpdateMCPServer("time", MCPServerInput{
		Name:      "time",
		Transport: "stdio",
		Command:   "updated-time",
		Args:      []string{"run"},
	}); err != nil {
		t.Fatalf("UpdateMCPServer(time): %v", err)
	}
	cfg, err := config.LoadForRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	updated, ok := findPluginEntry(cfg.Plugins, "time")
	if !ok || updated.Command != "updated-time" || !reflect.DeepEqual(updated.Args, []string{"run"}) {
		t.Fatalf("updated time plugin = %+v, found=%v", updated, ok)
	}

	if err := app.RemoveMCPServer("time"); err != nil {
		t.Fatalf("RemoveMCPServer(time): %v", err)
	}
	cfg, err = config.LoadForRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findPluginEntry(cfg.Plugins, "time"); ok {
		t.Fatalf("time plugin still configured after remove: %+v", cfg.Plugins)
	}
}

func TestRemoveProjectMCPRevealsAndRegistersGlobalFallback(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	userCfg := config.LoadForEdit(config.UserConfigPath())
	userCfg.Plugins = []config.PluginEntry{{Name: "docs", Command: "global-docs"}}
	if err := userCfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(dir, "reasonix.toml")
	if err := os.WriteFile(projectPath, []byte(`
[[plugins]]
name = "docs"
command = "project-docs"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := tool.NewRegistry()
	var configured []plugin.Spec
	ctrl := control.New(control.Options{
		Host:          plugin.NewHost(),
		Registry:      reg,
		WorkspaceRoot: dir,
		MCPConfigureSpec: func(spec *plugin.Spec) {
			configured = append(configured, *spec)
		},
	})
	defer ctrl.Close()
	projectEntry, found, err := desktopEffectiveMCPServer(dir, "docs")
	if err != nil || !found {
		t.Fatalf("load project docs: entry=%+v found=%v err=%v", projectEntry, found, err)
	}
	if _, err := ctrl.RegisterMCPServerOnDemand(projectEntry); err != nil {
		t.Fatalf("register project docs: %v", err)
	}

	app := NewApp()
	app.setTestCtrl(ctrl, "")
	app.activeTab().WorkspaceRoot = dir
	if err := app.RemoveMCPServer("docs"); err != nil {
		t.Fatalf("RemoveMCPServer(docs): %v", err)
	}

	projectCfg := config.LoadForEdit(projectPath)
	if _, found := findPluginEntry(projectCfg.Plugins, "docs"); found {
		t.Fatalf("project docs still configured after removal: %+v", projectCfg.Plugins)
	}
	globalCfg := config.LoadForEdit(config.UserConfigPath())
	globalEntry, found := findPluginEntry(globalCfg.Plugins, "docs")
	if !found || globalEntry.Command != "global-docs" {
		t.Fatalf("global docs fallback = %+v, found=%v", globalEntry, found)
	}
	effective, found, err := desktopEffectiveMCPServer(dir, "docs")
	if err != nil || !found || effective.Source != config.MCPSourceUserConfig || effective.Command != "global-docs" {
		t.Fatalf("effective docs fallback = %+v, found=%v err=%v", effective, found, err)
	}
	if len(configured) < 2 || configured[len(configured)-1].Command != "global-docs" {
		t.Fatalf("registered specs = %+v, want global fallback registered last", configured)
	}
	if _, found := reg.Get("mcp__docs__connect"); !found {
		t.Fatalf("global fallback connect surface missing; names=%v", reg.Names())
	}
}

func TestRemoveMCPServerClearsRecordedStartupFailure(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(`
[[plugins]]
name = "broken"
command = "reasonix-missing-mcp-binary"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()
	recordMCPFailure(app.activeCtrl(), config.PluginEntry{
		Name:    "broken",
		Command: "reasonix-missing-mcp-binary",
	}, errors.New("connect: missing binary"))

	view := app.Capabilities()
	if len(view.Servers) != 1 || view.Servers[0].Name != "broken" || view.Servers[0].Status != "failed" {
		t.Fatalf("Capabilities before remove = %+v, want broken failed", view.Servers)
	}

	if err := app.RemoveMCPServer("broken"); err != nil {
		t.Fatalf("RemoveMCPServer(broken): %v", err)
	}
	if mcpFailed(app.activeCtrl(), "broken") {
		t.Fatalf("Host.Failures() still contains broken after remove: %+v", app.activeCtrl().Host().Failures())
	}
	view = app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "broken" {
			t.Fatalf("Capabilities after remove still contains broken: %+v", view.Servers)
		}
	}
}

func TestRemoveMCPServerDeletesProjectMCPJSONEntry(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{
  "mcpServers": {
    "codegraph": { "command": "codegraph", "args": ["serve", "--mcp"] },
    "keep": { "command": "keep-mcp" }
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()

	if err := app.RemoveMCPServer("codegraph"); err != nil {
		t.Fatalf("RemoveMCPServer(.mcp.json codegraph): %v", err)
	}
	cfg, err := config.LoadForRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findPluginEntry(cfg.Plugins, "codegraph"); ok {
		t.Fatalf("codegraph still merged after remove: %+v", cfg.Plugins)
	}
	if _, ok := findPluginEntry(cfg.Plugins, "keep"); !ok {
		t.Fatalf("unrelated .mcp.json server should be preserved: %+v", cfg.Plugins)
	}
}

func TestRemoveMCPServerRejectsPluginManagedServerWithoutDisconnecting(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)

	srv := desktopMCPHTTPServer(t)
	defer srv.Close()
	reasonixHome := config.ReasonixHomeDir()
	root := filepath.Join(reasonixHome, "plugins", "superpowers")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, pluginpkg.NativeManifest), fmt.Appendf(nil, `{"apiVersion": "reasonix.io/plugin/v2",
  "name": "superpowers",
  "version": "1.0.0",
  "mcpServers": {
    "helper": { "type": "http", "url": %q }
  }
}`, srv.URL), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pluginpkg.Upsert(reasonixHome, pluginpkg.InstalledPlugin{
		Name:         "superpowers",
		Root:         "plugins/superpowers",
		Version:      "1.0.0",
		ManifestKind: "reasonix",
		Enabled:      true,
	}); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadForRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := findPluginEntry(cfg.Plugins, "helper")
	if !ok {
		t.Fatalf("plugin-managed MCP missing from config: %+v", cfg.Plugins)
	}
	ctrl := control.New(control.Options{Host: plugin.NewHost()})
	defer ctrl.Close()
	if _, err := ctrl.ConnectMCPServer(entry); err != nil {
		t.Fatalf("connect plugin-managed MCP: %v", err)
	}

	app := NewApp()
	app.setTestCtrl(ctrl, "")
	app.activeTab().WorkspaceRoot = dir
	err = app.RemoveMCPServer("helper")
	if err == nil || !strings.Contains(err.Error(), "managed by plugin") || !strings.Contains(err.Error(), "superpowers") {
		t.Fatalf("RemoveMCPServer(plugin-managed) error = %v", err)
	}
	if !mcpConnected(ctrl, "helper") {
		t.Fatal("plugin-managed MCP was disconnected despite rejected removal")
	}
	for action, actionErr := range map[string]error{
		"clear auth": app.ClearMCPServerAuthentication("helper"),
		"update":     app.UpdateMCPServer("helper", MCPServerInput{Name: "helper", Transport: "http", URL: srv.URL}),
	} {
		if actionErr == nil || !strings.Contains(actionErr.Error(), "managed by plugin") {
			t.Fatalf("%s plugin-managed MCP error = %v", action, actionErr)
		}
	}
	if _, found := findPluginEntry(config.LoadForEdit(config.UserConfigPath()).Plugins, "helper"); found {
		t.Fatal("plugin-managed MCP mutation created a user-config shadow")
	}
	servers := app.MCPServers()
	if len(servers) != 1 || servers[0].Name != "helper" || servers[0].ManagedByPlugin != "superpowers" {
		t.Fatalf("MCPServers() = %+v, want helper managed by superpowers", servers)
	}
}

func TestRemoveMCPServerRejectsRuntimeOnlyServerWithoutDisconnecting(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)

	srv := desktopMCPHTTPServer(t)
	defer srv.Close()
	ctrl := control.New(control.Options{Host: plugin.NewHost()})
	defer ctrl.Close()
	if _, err := ctrl.ConnectMCPServer(config.PluginEntry{Name: "runtime-only", Type: "http", URL: srv.URL}); err != nil {
		t.Fatalf("connect runtime-only MCP: %v", err)
	}

	app := NewApp()
	app.setTestCtrl(ctrl, "")
	app.activeTab().WorkspaceRoot = dir
	err := app.RemoveMCPServer("runtime-only")
	if err == nil || !strings.Contains(err.Error(), "no removable MCP server") {
		t.Fatalf("RemoveMCPServer(runtime-only) error = %v", err)
	}
	if !mcpConnected(ctrl, "runtime-only") {
		t.Fatal("runtime-only MCP was disconnected despite failed persistence removal")
	}
}

func TestUpdateMCPServerEditsProjectMCPJSONEntry(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{
  "mcpServers": {
    "codegraph": { "command": "codegraph", "args": ["serve", "--mcp"] }
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()
	entry, ok, err := desktopEffectiveMCPServer(dir, "codegraph")
	if err != nil || !ok {
		t.Fatalf("load codegraph entry: found=%v err=%v", ok, err)
	}
	if err := config.DefaultMCPActivationStore().SetServerEnabled(entry, dir, false); err != nil {
		t.Fatal(err)
	}
	app.activeTab().disabledMCP["codegraph"] = ServerView{}

	if err := app.UpdateMCPServer("codegraph", MCPServerInput{
		Name:      "codegraph",
		Transport: "stdio",
		Command:   "reasonix-missing-mcp-binary",
		Args:      []string{"serve", "--mcp"},
		Env:       map[string]string{"CODEGRAPH_LOG": "debug"},
	}); err != nil {
		t.Fatalf("UpdateMCPServer(.mcp.json codegraph): %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	got := doc.MCPServers["codegraph"]
	if got.Command != "reasonix-missing-mcp-binary" || !reflect.DeepEqual(got.Args, []string{"serve", "--mcp"}) || got.Env["CODEGRAPH_LOG"] != "debug" {
		t.Fatalf(".mcp.json codegraph = %+v, want updated command/args/env", got)
	}
	if _, ok := findPluginEntry(config.LoadForEdit(config.UserConfigPath()).Plugins, "codegraph"); ok {
		t.Fatalf(".mcp.json update should not create a user config shadow entry")
	}
}

func TestUpdateMCPServerPreservesProjectTOMLSourceAndGlobalShadow(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	userCfg := config.LoadForEdit(config.UserConfigPath())
	userCfg.Plugins = []config.PluginEntry{{Name: "docs", Command: "global-docs"}}
	if err := userCfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(dir, "reasonix.toml")
	if err := os.WriteFile(projectPath, []byte(`
[[plugins]]
name = "docs"
command = "project-docs"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost(), WorkspaceRoot: dir}), "")
	defer app.activeCtrl().Close()
	app.activeTab().WorkspaceRoot = dir
	entry, ok, err := desktopEffectiveMCPServer(dir, "docs")
	if err != nil || !ok || entry.Source != config.MCPSourceProjectConfig {
		t.Fatalf("load project docs entry: entry=%+v found=%v err=%v", entry, ok, err)
	}
	if err := config.DefaultMCPActivationStore().SetServerEnabled(entry, dir, false); err != nil {
		t.Fatal(err)
	}
	app.activeTab().disabledMCP["docs"] = ServerView{}

	if err := app.UpdateMCPServer("docs", MCPServerInput{
		Name: "docs", Transport: "stdio", Command: "project-docs-updated",
	}); err != nil {
		t.Fatalf("UpdateMCPServer(project reasonix.toml docs): %v", err)
	}

	projectCfg := config.LoadForEdit(projectPath)
	projectEntry, found := findPluginEntry(projectCfg.Plugins, "docs")
	if !found || projectEntry.Command != "project-docs-updated" {
		t.Fatalf("project docs entry = %+v, found=%v", projectEntry, found)
	}
	globalCfg := config.LoadForEdit(config.UserConfigPath())
	globalEntry, found := findPluginEntry(globalCfg.Plugins, "docs")
	if !found || globalEntry.Command != "global-docs" {
		t.Fatalf("global shadow changed while editing project entry: %+v, found=%v", globalEntry, found)
	}
	effective, found, err := desktopEffectiveMCPServer(dir, "docs")
	if err != nil || !found || effective.Source != config.MCPSourceProjectConfig || effective.Command != "project-docs-updated" {
		t.Fatalf("effective docs after edit = %+v, found=%v err=%v", effective, found, err)
	}
}

func TestAddMCPServerPersistsRemoteHeaders(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	t.Setenv("STRIPE_TOKEN", "stripe-test-token")
	srv := desktopMCPHTTPServer(t)
	defer srv.Close()

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()

	tools, err := app.AddMCPServer(MCPServerInput{
		Name:      "stripe",
		Transport: "http",
		URL:       srv.URL,
		Headers: map[string]string{
			"Authorization": "Bearer ${STRIPE_TOKEN}",
			"X-Org":         "team",
		},
	})
	if err != nil {
		t.Fatalf("AddMCPServer(stripe): %v", err)
	}
	if tools != 1 {
		t.Fatalf("tools = %d, want 1", tools)
	}

	cfg, err := config.LoadForRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := findPluginEntry(cfg.Plugins, "stripe")
	if !ok {
		t.Fatalf("stripe plugin missing from config: %+v", cfg.Plugins)
	}
	if p.Type != "http" || p.URL != srv.URL {
		t.Fatalf("stripe plugin transport = %q url = %q", p.Type, p.URL)
	}
	if p.Headers["Authorization"] != "Bearer ${STRIPE_TOKEN}" || p.Headers["X-Org"] != "team" {
		t.Fatalf("stripe headers = %+v", p.Headers)
	}

	view := app.MCPServers()
	for _, s := range view {
		if s.Name == "stripe" {
			if !reflect.DeepEqual(s.HeaderKeys, []string{"Authorization", "X-Org"}) {
				t.Fatalf("stripe header keys = %+v", s.HeaderKeys)
			}
			return
		}
	}
	t.Fatalf("stripe MCP missing from view: %+v", view)
}

func TestInstallMCPServerHandshakeFailureDoesNotPersist(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()

	result, err := app.InstallMCPServer(MCPServerInput{
		Name: "broken", Transport: "stdio", Command: "reasonix-missing-mcp-binary",
	})
	if err != nil {
		t.Fatalf("InstallMCPServer returned transport error instead of structured issue: %v", err)
	}
	if result.State != "issue" || result.Action != "retry" {
		t.Fatalf("install result = %+v, want retryable issue", result)
	}
	cfg, err := config.LoadForRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findPluginEntry(cfg.Plugins, "broken"); ok {
		t.Fatalf("failed candidate was persisted: %+v", cfg.Plugins)
	}
	for _, server := range app.MCPServers() {
		if server.Name == "broken" {
			t.Fatalf("failed candidate leaked into the installed server list: %+v", server)
		}
	}
}

func TestInstallMCPServerAuthenticationRequiredPersistsForResume(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()

	result, err := app.InstallMCPServer(MCPServerInput{Name: "oauth", Transport: "http", URL: srv.URL})
	if err != nil {
		t.Fatalf("InstallMCPServer auth result: %v", err)
	}
	if result.State != "action_required" || result.Action != "authenticate" {
		t.Fatalf("install result = %+v, want authentication action", result)
	}
	cfg, err := config.LoadForRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findPluginEntry(cfg.Plugins, "oauth"); !ok {
		t.Fatalf("auth-pending candidate must persist for resume: %+v", cfg.Plugins)
	}
}

func TestAddMCPServerPersistsConnectionConfiguration(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	srv := desktopMCPHTTPServer(t)
	defer srv.Close()

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()
	autoStart := false
	callTimeout := 45
	_, err := app.AddMCPServer(MCPServerInput{
		Name:               "admin",
		Transport:          "streamable-http",
		URL:                srv.URL,
		AutoStart:          &autoStart,
		CallTimeoutSeconds: &callTimeout,
		ToolTimeoutSeconds: map[string]int{
			"wipe": 120,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadForRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := findPluginEntry(cfg.Plugins, "admin")
	if !ok || entry.Type != "http" || entry.AutoStart == nil || *entry.AutoStart ||
		entry.CallTimeoutSeconds != 45 || entry.ToolTimeoutSeconds["wipe"] != 120 {
		t.Fatalf("persisted advanced MCP entry = %+v, found=%v", entry, ok)
	}

	views := app.MCPServers()
	if len(views) != 1 || views[0].Transport != "http" || views[0].AutoStart ||
		views[0].CallTimeoutSeconds != 45 || views[0].ToolTimeoutSeconds["wipe"] != 120 {
		t.Fatalf("advanced MCP ServerView = %+v", views)
	}
}

func TestUpdateMCPServerPreservesAbsentFieldsAndClearsExplicitOnes(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	srv := desktopMCPHTTPServer(t)
	defer srv.Close()

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()
	callTimeout := 45
	if _, err := app.AddMCPServer(MCPServerInput{
		Name:               "admin",
		Transport:          "http",
		URL:                srv.URL,
		CallTimeoutSeconds: &callTimeout,
		ToolTimeoutSeconds: map[string]int{"wipe": 120},
	}); err != nil {
		t.Fatal(err)
	}

	// An old frontend (or a partial payload) omits optional timeout fields.
	if err := app.UpdateMCPServer("admin", MCPServerInput{Name: "admin", Transport: "http", URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadForRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := findPluginEntry(cfg.Plugins, "admin")
	if !ok || entry.CallTimeoutSeconds != 45 || entry.ToolTimeoutSeconds["wipe"] != 120 {
		t.Fatalf("absent input fields must preserve persisted values, entry = %+v, found=%v", entry, ok)
	}

	// Explicit zero values are the editor's clear semantics.
	cleared := 0
	if err := app.UpdateMCPServer("admin", MCPServerInput{
		Name:               "admin",
		Transport:          "http",
		URL:                srv.URL,
		CallTimeoutSeconds: &cleared,
		ToolTimeoutSeconds: map[string]int{},
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.LoadForRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok = findPluginEntry(cfg.Plugins, "admin")
	if !ok || entry.CallTimeoutSeconds != 0 || len(entry.ToolTimeoutSeconds) != 0 {
		t.Fatalf("explicit empty fields must clear persisted values, entry = %+v, found=%v", entry, ok)
	}
}

func TestUpdateMCPServerFailedCandidateRollsBackConfigAndConnection(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	srv := desktopMCPHTTPServer(t)
	defer srv.Close()

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()
	if _, err := app.AddMCPServer(MCPServerInput{Name: "stable", Transport: "http", URL: srv.URL}); err != nil {
		t.Fatal(err)
	}

	err := app.UpdateMCPServer("stable", MCPServerInput{
		Name: "stable", Transport: "stdio", Command: "reasonix-missing-mcp-binary",
	})
	if err == nil {
		t.Fatal("broken update candidate should fail")
	}
	cfg, err := config.LoadForRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := findPluginEntry(cfg.Plugins, "stable")
	if !ok || entry.Type != "http" || entry.URL != srv.URL {
		t.Fatalf("failed update changed durable config: %+v, found=%v", entry, ok)
	}
	if !app.activeCtrl().Host().HasClient("stable") {
		t.Fatal("previous MCP connection was not restored after failed update")
	}
}

func TestCapabilitiesMarksBackgroundRemoteMCPAuthPossible(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(`
[[plugins]]
name = "dida"
type = "http"
url = "https://mcp.dida365.com"
tier = "lazy"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()

	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "dida" {
			if s.Status != "deferred" || s.StartIntent != "automatic" || s.RuntimeState != "idle" || s.AuthStatus != "possible" || s.AuthURL != "https://mcp.dida365.com" {
				t.Fatalf("dida auth diagnosis = %+v", s)
			}
			return
		}
	}
	t.Fatalf("dida MCP missing from Capabilities: %+v", view.Servers)
}

func TestCapabilitiesDoesNotMarkRemoteMCPWithAuthHeaderPossible(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(`
[[plugins]]
name = "stripe"
type = "http"
url = "https://mcp.stripe.com"
headers = { Authorization = "Bearer ${STRIPE_TOKEN}" }
tier = "lazy"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()

	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "stripe" {
			if s.AuthStatus != "none" {
				t.Fatalf("stripe auth status = %q, want none; server = %+v", s.AuthStatus, s)
			}
			return
		}
	}
	t.Fatalf("stripe MCP missing from Capabilities: %+v", view.Servers)
}

func TestCapabilitiesMarksAuthFailureRequired(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(`
[[plugins]]
name = "figma"
type = "http"
url = "https://mcp.figma.com/mcp"
tier = "lazy"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	host := plugin.NewHost()
	host.RecordFailure(plugin.Spec{Name: "figma", Type: "http", URL: "https://mcp.figma.com/mcp"}, errors.New("connect: 401 unauthorized"))
	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: host}), "")
	defer app.activeCtrl().Close()

	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "figma" {
			if s.Status != "failed" || s.AuthStatus != "required" || s.AuthURL != "https://mcp.figma.com/mcp" {
				t.Fatalf("figma auth diagnosis = %+v", s)
			}
			return
		}
	}
	t.Fatalf("figma MCP missing from Capabilities: %+v", view.Servers)
}

func TestClearMCPServerAuthenticationClearsConfigAndFailure(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(`
[[plugins]]
name = "figma"
type = "http"
url = "https://mcp.figma.com/mcp?access_token=abc&workspace=main"
headers = { Authorization = "Bearer ${FIGMA_TOKEN}", "X-Org" = "team" }
env = { FIGMA_TOKEN = "${FIGMA_TOKEN}", DEBUG = "1" }
tier = "lazy"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	host := plugin.NewHost()
	host.RecordFailure(plugin.Spec{Name: "figma", Type: "http", URL: "https://mcp.figma.com/mcp"}, errors.New("connect: 401 unauthorized"))
	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: host}), "")
	defer app.activeCtrl().Close()

	if err := app.ClearMCPServerAuthentication("figma"); err != nil {
		t.Fatalf("ClearMCPServerAuthentication: %v", err)
	}
	if failures := host.Failures(); len(failures) != 0 {
		t.Fatalf("failure should be cleared: %+v", failures)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Plugins[0]
	if p.URL != "https://mcp.figma.com/mcp?workspace=main" {
		t.Fatalf("url = %q", p.URL)
	}
	if _, ok := p.Headers["Authorization"]; ok {
		t.Fatalf("auth header should be removed: %v", p.Headers)
	}
	if p.Headers["X-Org"] != "team" {
		t.Fatalf("ordinary header should be preserved: %v", p.Headers)
	}
	if _, ok := p.Env["FIGMA_TOKEN"]; ok {
		t.Fatalf("auth env should be removed: %v", p.Env)
	}
	if p.Env["DEBUG"] != "1" {
		t.Fatalf("ordinary env should be preserved: %v", p.Env)
	}
	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "figma" {
			if s.Status != "deferred" || s.StartIntent != "automatic" || s.RuntimeState != "idle" || s.AuthStatus != "possible" {
				t.Fatalf("figma should return to background possible auth: %+v", s)
			}
			return
		}
	}
	t.Fatalf("figma MCP missing from Capabilities: %+v", view.Servers)
}

func TestUpdateMCPServerMigratesLegacyTierInProjectSource(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(`
[[plugins]]
name = "playwright"
command = "npx"
args = ["-y", "@playwright/mcp"]
env = { TOKEN = "${PLAYWRIGHT_TOKEN}" }
tier = "lazy"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()
	entry, ok, err := desktopEffectiveMCPServer(dir, "playwright")
	if err != nil || !ok {
		t.Fatalf("load playwright entry: found=%v err=%v", ok, err)
	}
	if err := config.DefaultMCPActivationStore().SetServerEnabled(entry, dir, false); err != nil {
		t.Fatal(err)
	}
	app.activeTab().disabledMCP["playwright"] = ServerView{Name: "playwright", Status: "disabled", Enabled: false}

	if err := app.UpdateMCPServer("playwright", MCPServerInput{
		Name:      "playwright",
		Transport: "stdio",
		Command:   "node",
		Args:      []string{"server.js"},
	}); err != nil {
		t.Fatalf("UpdateMCPServer: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Plugins[0].Command; got != "node" {
		t.Fatalf("updated command = %q, want node", got)
	}
	if got := cfg.Plugins[0].Env["TOKEN"]; got != "${PLAYWRIGHT_TOKEN}" {
		t.Fatalf("env TOKEN = %q, want preserved env", got)
	}
	userCfg := config.LoadForEdit(config.UserConfigPath())
	if _, ok := findPluginEntry(userCfg.Plugins, "playwright"); ok {
		t.Fatalf("project plugin should not be copied to user config: %+v", userCfg.Plugins)
	}
	projectCfg := config.LoadForEdit(filepath.Join(dir, "reasonix.toml"))
	projectPlugin, ok := findPluginEntry(projectCfg.Plugins, "playwright")
	if !ok {
		t.Fatalf("playwright should remain in project config: %+v", projectCfg.Plugins)
	}
	if projectPlugin.Command != "node" || projectPlugin.Env["TOKEN"] != "${PLAYWRIGHT_TOKEN}" {
		t.Fatalf("project plugin after update = %+v", projectPlugin)
	}
	if projectPlugin.Tier != "" {
		t.Fatalf("project plugin tier = %q, want migrated empty", projectPlugin.Tier)
	}
	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "playwright" {
			if s.Status != "disabled" {
				t.Fatalf("updated MCP status = %q, want disabled without a readiness probe; server = %+v", s.Status, s)
			}
			if s.Command != "node" || len(s.Args) != 1 || s.Args[0] != "server.js" {
				t.Fatalf("server command not refreshed: %+v", s)
			}
			return
		}
	}
	t.Fatalf("playwright MCP missing from Capabilities: %+v", view.Servers)
}

func TestUpdateMCPServerSplitsPastedCommandLine(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(`
[[plugins]]
name = "playwright"
command = "npx"
args = ["-y", "@playwright/mcp"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()
	app.activeTab().disabledMCP["playwright"] = ServerView{}

	if err := app.UpdateMCPServer("playwright", MCPServerInput{
		Name:      "playwright",
		Transport: "stdio",
		Command:   "npx -y @modelcontextprotocol/server-filesystem .",
	}); err != nil {
		t.Fatalf("UpdateMCPServer: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Plugins[0]
	if p.Command != "npx" {
		t.Fatalf("command = %q, want npx", p.Command)
	}
	if got := strings.Join(p.Args, "\x00"); got != strings.Join([]string{"-y", "@modelcontextprotocol/server-filesystem", "."}, "\x00") {
		t.Fatalf("args = %v", p.Args)
	}
}

func TestUpdateMCPServerRejectsReconnectFailureWithoutPersisting(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(`
[[plugins]]
name = "broken"
command = "reasonix-old-missing-mcp-binary"
tier = "background"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()

	if err := app.UpdateMCPServer("broken", MCPServerInput{
		Name:      "broken",
		Transport: "stdio",
		Command:   "reasonix-missing-mcp-binary",
	}); err == nil {
		t.Fatal("UpdateMCPServer should reject an unusable candidate")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Plugins[0].Command; got != "reasonix-old-missing-mcp-binary" {
		t.Fatalf("failed update command = %q, want original command", got)
	}
	if got := cfg.Plugins[0].Tier; got != "" {
		t.Fatalf("loaded legacy tier = %q, want normalized empty", got)
	}
	if !mcpFailed(app.activeCtrl(), "broken") {
		t.Fatalf("Host.Failures() = %+v, want broken failure recorded", app.activeCtrl().Host().Failures())
	}
	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "broken" {
			if s.Status != "failed" {
				t.Fatalf("server status = %q, want failed; server = %+v", s.Status, s)
			}
			if s.Command != "reasonix-old-missing-mcp-binary" || s.Tier != "background" {
				t.Fatalf("failed candidate leaked into server config: %+v", s)
			}
			return
		}
	}
	t.Fatalf("broken MCP missing from Capabilities: %+v", view.Servers)
}

func TestReconnectMCPServerClearsInitializingPlaceholderAndRecordsFailure(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(`
[[plugins]]
name = "codegraph"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := tool.NewRegistry()
	reg.Add(desktopFakeTool{name: "mcp__codegraph__connect"})
	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost(), Registry: reg}), "")
	defer app.activeCtrl().Close()

	view := app.Capabilities()
	foundIdle := false
	for _, s := range view.Servers {
		if s.Name == "codegraph" {
			foundIdle = true
			if s.Status != "deferred" || s.StartIntent != "automatic" || s.RuntimeState != "idle" {
				t.Fatalf("initial codegraph server = %+v, want automatic idle background state", s)
			}
		}
	}
	if !foundIdle {
		t.Fatalf("codegraph missing before reconnect: %+v", view.Servers)
	}
	if _, ok := reg.Get("mcp__codegraph__connect"); !ok {
		t.Fatal("test setup expected stale codegraph connect placeholder")
	}

	if err := app.ReconnectMCPServer("codegraph"); err == nil || !strings.Contains(err.Error(), "command is required") {
		t.Fatalf("ReconnectMCPServer error = %v, want missing command", err)
	}
	if _, ok := reg.Get("mcp__codegraph__connect"); ok {
		t.Fatalf("stale codegraph placeholder still registered after reconnect failure; names=%v", reg.Names())
	}
	if !mcpFailed(app.activeCtrl(), "codegraph") {
		t.Fatalf("Host.Failures() = %+v, want codegraph failure recorded", app.activeCtrl().Host().Failures())
	}

	view = app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "codegraph" {
			if s.Status != "failed" || s.Error == "" {
				t.Fatalf("codegraph after failed reconnect = %+v, want failed with error", s)
			}
			return
		}
	}
	t.Fatalf("codegraph missing after reconnect: %+v", view.Servers)
}

func TestSetMCPServerTierPreservesProjectSourceAndRecordsConnectFailure(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(`
[[plugins]]
name = "broken"
command = "reasonix-missing-mcp-binary"
tier = "lazy"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	if err := app.SetMCPServerTier("broken", "background"); err != nil {
		t.Fatalf("SetMCPServerTier legacy binding: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Plugins[0].Tier; got != "" {
		t.Fatalf("saved tier = %q, want migrated empty", got)
	}
	userCfg := config.LoadForEdit(config.UserConfigPath())
	if _, ok := findPluginEntry(userCfg.Plugins, "broken"); ok {
		t.Fatalf("project plugin should not be copied to user config: %+v", userCfg.Plugins)
	}
	projectCfg := config.LoadForEdit(filepath.Join(dir, "reasonix.toml"))
	projectPlugin, ok := findPluginEntry(projectCfg.Plugins, "broken")
	if !ok {
		t.Fatalf("broken should remain in project config: %+v", projectCfg.Plugins)
	}
	if projectPlugin.Tier != "" {
		t.Fatalf("project plugin tier = %q, want migrated empty", projectPlugin.Tier)
	}
	if !mcpFailed(app.activeCtrl(), "broken") {
		t.Fatalf("Host.Failures() = %+v, want broken failure recorded", app.activeCtrl().Host().Failures())
	}
	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "broken" {
			if s.Status != "failed" {
				t.Fatalf("server status = %q, want failed; server = %+v", s.Status, s)
			}
			if s.Tier != "background" {
				t.Fatalf("server tier = %q, want background so radio selection does not jump back", s.Tier)
			}
			return
		}
	}
	t.Fatalf("broken MCP missing from Capabilities: %+v", view.Servers)
}

func TestSetMCPServerTierRejectsBackgroundJobsBeforeSavingConfig(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(), []byte(`
[[plugins]]
name = "broken"
command = "reasonix-missing-mcp-binary"
tier = "lazy"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(newBackgroundJobController(t, "mcp-tier-job"), "")

	err := app.SetMCPServerTier("broken", "background")
	if err == nil || !strings.Contains(err.Error(), "stop background jobs") {
		t.Fatalf("SetMCPServerTier with background job error = %v, want active-work guard", err)
	}
	data, readErr := os.ReadFile(config.UserConfigPath())
	if readErr != nil {
		t.Fatalf("read config: %v", readErr)
	}
	if !strings.Contains(string(data), `tier = "lazy"`) {
		t.Fatalf("plugin config changed after rejected tier update:\n%s", data)
	}
}

func TestCapabilitiesMigratesFailedMCPConfiguredTierAfterRestart(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "reasonix.toml"), []byte(`
[[plugins]]
name = "broken"
command = "reasonix-missing-mcp-binary"
tier = "eager"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.setTestCtrl(control.New(control.Options{Host: plugin.NewHost()}), "")
	defer app.activeCtrl().Close()
	recordMCPFailure(app.activeCtrl(), config.PluginEntry{
		Name:    "broken",
		Command: "reasonix-missing-mcp-binary",
		Tier:    "eager",
	}, errors.New("connect: missing binary"))

	view := app.Capabilities()
	for _, s := range view.Servers {
		if s.Name == "broken" {
			if s.Status != "failed" {
				t.Fatalf("server status = %q, want failed; server = %+v", s.Status, s)
			}
			if s.Tier != "background" {
				t.Fatalf("server tier = %q, want migrated background default", s.Tier)
			}
			if !s.Configured {
				t.Fatalf("server configured = false, want true; server = %+v", s)
			}
			return
		}
	}
	t.Fatalf("broken MCP missing from Capabilities: %+v", view.Servers)
}

func TestRunShellForTabRoutesToRequestedTab(t *testing.T) {
	isolateDesktopUserDirs(t)

	activeEvents := make(chan event.Event, 16)
	inactiveEvents := make(chan event.Event, 16)
	activeCtrl := control.New(control.Options{Sink: event.FuncSink(func(e event.Event) { activeEvents <- e })})
	inactiveCtrl := control.New(control.Options{Sink: event.FuncSink(func(e event.Event) { inactiveEvents <- e })})
	defer activeCtrl.Close()
	defer inactiveCtrl.Close()

	app := &App{
		tabs: map[string]*WorkspaceTab{
			"active":   {ID: "active", Scope: "global", Ctrl: activeCtrl, Ready: true},
			"inactive": {ID: "inactive", Scope: "global", Ctrl: inactiveCtrl, Ready: true},
		},
		tabOrder:    []string{"active", "inactive"},
		activeTabID: "active",
	}

	app.RunShellForTab("inactive", "echo route-test")

	sawDispatch := false
	deadline := time.After(3 * time.Second)
	for {
		select {
		case e := <-inactiveEvents:
			if e.Kind == event.ToolDispatch && strings.Contains(e.Tool.Args, "route-test") {
				sawDispatch = true
			}
			if e.Kind == event.TurnDone {
				if !sawDispatch {
					t.Fatal("inactive tab finished without receiving shell dispatch")
				}
				select {
				case active := <-activeEvents:
					t.Fatalf("active tab received event for inactive shell: %+v", active)
				default:
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for inactive shell turn")
		}
	}
}

func TestRunShellForTabStaysBoundDuringRapidProjectTabSwitching(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping shell cancellation integration test in short mode")
	}

	isolateDesktopUserDirs(t)

	projectA := t.TempDir()
	projectB := t.TempDir()
	globalRoot := t.TempDir()
	shellEvents := make(chan event.Event, 64)
	projectEvents := make(chan event.Event, 64)
	globalEvents := make(chan event.Event, 64)
	shellCtrl := control.New(control.Options{
		Sink:          event.FuncSink(func(e event.Event) { shellEvents <- e }),
		WorkspaceRoot: projectA,
	})
	projectCtrl := control.New(control.Options{
		Sink:          event.FuncSink(func(e event.Event) { projectEvents <- e }),
		WorkspaceRoot: projectB,
	})
	globalCtrl := control.New(control.Options{
		Sink:          event.FuncSink(func(e event.Event) { globalEvents <- e }),
		WorkspaceRoot: globalRoot,
	})
	defer shellCtrl.Close()
	defer projectCtrl.Close()
	defer globalCtrl.Close()

	app := &App{
		tabs: map[string]*WorkspaceTab{
			"shell":     {ID: "shell", Scope: "project", WorkspaceRoot: projectA, Ctrl: shellCtrl, Ready: true},
			"project-b": {ID: "project-b", Scope: "project", WorkspaceRoot: projectB, Ctrl: projectCtrl, Ready: true},
			"global":    {ID: "global", Scope: "global", WorkspaceRoot: globalRoot, Ctrl: globalCtrl, Ready: true},
		},
		tabOrder:    []string{"shell", "project-b", "global"},
		activeTabID: "shell",
	}

	marker := "shell-route-marker.txt"
	if err := app.RunShellForTab("shell", longRunningMarkerCommand(marker)); err != nil {
		t.Fatalf("RunShellForTab: %v", err)
	}
	waitForShellDispatch(t, shellEvents, marker)
	waitForFile(t, filepath.Join(projectA, marker), "shell")

	for range 8 {
		if err := app.SetActiveTab("project-b"); err != nil {
			t.Fatalf("SetActiveTab(project-b): %v", err)
		}
		if err := app.SetActiveTab("global"); err != nil {
			t.Fatalf("SetActiveTab(global): %v", err)
		}
		if err := app.SetActiveTab("shell"); err != nil {
			t.Fatalf("SetActiveTab(shell): %v", err)
		}
	}
	if err := app.SetActiveTab("project-b"); err != nil {
		t.Fatalf("SetActiveTab(project-b final): %v", err)
	}
	app.CancelTab("shell")

	cancelled := false
	deadline := time.After(15 * time.Second)
	for {
		select {
		case e := <-shellEvents:
			if e.Kind == event.ToolResult && e.Tool.Name == "bash" {
				cancelled = e.Tool.Err != ""
			}
			if e.Kind == event.TurnDone {
				if !cancelled {
					t.Fatal("shell tab finished without a cancelled shell result")
				}
				if _, err := os.Stat(filepath.Join(projectB, marker)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("shell marker appeared in project-b workspace: %v", err)
				}
				if got := activeTabIDForTest(app); got != "project-b" {
					t.Fatalf("active tab = %q, want project-b after background shell cancel", got)
				}
				assertNoEvents(t, projectEvents, "project-b")
				assertNoEvents(t, globalEvents, "global")
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for shell tab cancellation")
		}
	}
}

func longRunningMarkerCommand(marker string) string {
	if sandbox.ResolveShell("", "", nil).Kind == sandbox.ShellPowerShell {
		return fmt.Sprintf("Set-Content -LiteralPath %s -Value shell; Start-Sleep -Seconds 30", marker)
	}
	return fmt.Sprintf("printf shell > %s; sleep 30", marker)
}

func waitForShellDispatch(t *testing.T, ch <-chan event.Event, marker string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.Kind == event.ToolDispatch && strings.Contains(e.Tool.Args, marker) {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for shell dispatch")
		}
	}
}

func activeTabIDForTest(app *App) string {
	app.mu.RLock()
	defer app.mu.RUnlock()
	return app.activeTabID
}

func assertNoEvents(t *testing.T, ch <-chan event.Event, name string) {
	t.Helper()
	select {
	case e := <-ch:
		t.Fatalf("%s received event while shell ran in another tab: %+v", name, e)
	default:
	}
}

type blockingRunner struct {
	started chan struct{}
	release chan struct{}
}

func (r *blockingRunner) Run(ctx context.Context, _ string) error {
	close(r.started)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.release:
		return nil
	}
}

func startNonCooperativeSessionJob(t *testing.T, jm *jobs.Manager, sessionPath string) func() {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	jm.StartForSession(agent.BranchID(sessionPath), "bash", "stuck job", func(ctx context.Context, _ io.Writer) (string, error) {
		close(started)
		<-ctx.Done()
		<-release
		return "", ctx.Err()
	})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background job never started")
	}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		close(release)
	}
}

func waitNotRunning(t *testing.T, ctrl control.SessionAPI) {
	t.Helper()
	// Windows release runners can take more than one second to schedule the
	// controller's asynchronous completion while the full desktop suite is
	// active. Keep a bounded responsiveness check without treating scheduler
	// delay as a leaked controller.
	deadline := time.Now().Add(5 * time.Second)
	for ctrl.Running() {
		if time.Now().After(deadline) {
			t.Fatal("controller still running")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newBackgroundJobController(t *testing.T, label string) *control.Controller {
	t.Helper()
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	path := filepath.Join(dir, label+".jsonl")
	jm := jobs.NewManager(event.Discard)
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: path, Label: "test", Jobs: jm})
	t.Cleanup(ctrl.Close)
	jm.StartForSession(agent.BranchID(path), "bash", label, func(ctx context.Context, _ io.Writer) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	return ctrl
}

func hasLevel(levels []string, want string) bool {
	return slices.Contains(levels, want)
}

func hasCommand(cmds []CommandInfo, name string) bool {
	for _, cmd := range cmds {
		if cmd.Name == name {
			return true
		}
	}
	return false
}

func hasDirEntry(entries []DirEntry, name string) bool {
	for _, entry := range entries {
		if entry.Name == name {
			return true
		}
	}
	return false
}

func TestSessionActionsWithoutControllerReturnError(t *testing.T) {
	app := &App{tabs: map[string]*WorkspaceTab{}}
	if err := app.NewSession(); err == nil {
		t.Error("NewSession with no controller must surface an error, not silently no-op")
	}
	if _, err := app.ClearSession(); err == nil {
		t.Error("ClearSession with no controller must surface an error")
	}

	app = &App{
		tabs:        map[string]*WorkspaceTab{"t1": {ID: "t1", StartupErr: "boot exploded"}},
		activeTabID: "t1",
	}
	err := app.NewSession()
	if err == nil || !strings.Contains(err.Error(), "boot exploded") {
		t.Errorf("error should carry the tab's startup failure, got %v", err)
	}
}

// Prompt history scanning tests

func identityPromptDisplay(text string) string { return text }

// TestCollectPromptHistoryEntriesLegacyEvent verifies that the legacy event format
// {"kind":"user.message","text":"..."} is correctly extracted.
func TestCollectPromptHistoryEntriesLegacyEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"user.message","text":"hello world"}
{"kind":"user.message","text":"second prompt"}
{"kind":"model.final","content":"response"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := collectPromptHistoryEntries(path, info, identityPromptDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Text != "hello world" {
		t.Errorf("expected 'hello world', got %q", entries[0].Text)
	}
	if entries[1].Text != "second prompt" {
		t.Errorf("expected 'second prompt', got %q", entries[1].Text)
	}
	if entries[0].Turn != 0 || entries[1].Turn != 1 {
		t.Errorf("expected turns 0,1; got %d,%d", entries[0].Turn, entries[1].Turn)
	}
	if entries[0].SessionPath != path {
		t.Errorf("expected session path %q, got %q", path, entries[0].SessionPath)
	}
}

// TestCollectPromptHistoryEntriesEarlyEvent verifies that the migrated legacy event
// format {"type":"user.message","text":"..."} is correctly extracted.
func TestCollectPromptHistoryEntriesEarlyEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user.message","text":"v0 prompt"}
{"type":"model.final","content":"response"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := collectPromptHistoryEntries(path, info, identityPromptDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Text != "v0 prompt" {
		t.Errorf("expected 'v0 prompt', got %q", entries[0].Text)
	}
}

// TestCollectPromptHistoryEntriesProviderMessage verifies that the current
// provider.Message format {"role":"user","content":"..."} is correctly extracted.
func TestCollectPromptHistoryEntriesProviderMessage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"hello from provider"}
{"role":"assistant","content":"response"}
{"role":"user","content":"another prompt"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := collectPromptHistoryEntries(path, info, identityPromptDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Text != "hello from provider" {
		t.Errorf("expected 'hello from provider', got %q", entries[0].Text)
	}
	if entries[1].Text != "another prompt" {
		t.Errorf("expected 'another prompt', got %q", entries[1].Text)
	}
}

// TestCollectPromptHistoryEntriesMixedFormats verifies that both formats in the
// same file are extracted.
func TestCollectPromptHistoryEntriesMixedFormats(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"user.message","text":"legacy prompt"}
{"role":"user","content":"modern prompt"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := collectPromptHistoryEntries(path, info, identityPromptDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Text != "legacy prompt" {
		t.Errorf("expected 'legacy prompt', got %q", entries[0].Text)
	}
	if entries[1].Text != "modern prompt" {
		t.Errorf("expected 'modern prompt', got %q", entries[1].Text)
	}
}

func TestCollectPromptHistoryEntriesReadsEventTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	rfcTime := time.Date(2026, 6, 14, 10, 30, 5, 6_000_000, time.UTC)
	if err := os.WriteFile(path, []byte(`{"kind":"user.message","text":"legacy timed","time":1800000000123}
{"role":"user","content":"modern timed","createdAt":`+strconv.Quote(rfcTime.Format(time.RFC3339Nano))+`}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := collectPromptHistoryEntries(path, info, identityPromptDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].At != 1800000000123 {
		t.Errorf("numeric event time = %d, want 1800000000123", entries[0].At)
	}
	if entries[1].At != rfcTime.UnixMilli() {
		t.Errorf("RFC3339 event time = %d, want %d", entries[1].At, rfcTime.UnixMilli())
	}
}

// TestCollectPromptHistoryEntriesUsesDisplayResolver verifies history recall uses
// the user-visible prompt text, not the controller-expanded model input.
func TestCollectPromptHistoryEntriesUsesDisplayResolver(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	expanded := "<memory-update>\nSaved memory\n</memory-update>\n\nvisible prompt"
	if err := os.WriteFile(path, []byte(`{"role":"user","content":`+strconv.Quote(expanded)+`}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recordSessionDisplay(dir, path, expanded, "visible prompt"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := collectPromptHistoryEntries(path, info, sessionDisplayResolver(dir, path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Text != "visible prompt" {
		t.Errorf("expected visible prompt, got %q", entries[0].Text)
	}
}

func TestCollectPromptHistoryEntriesSkipsSyntheticMessages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"Plan approved — plan mode is off"}
{"role":"user","content":"real prompt"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := collectPromptHistoryEntries(path, info, identityPromptDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Text != "real prompt" {
		t.Errorf("expected real prompt, got %q", entries[0].Text)
	}
}

// TestCollectPromptHistoryEntriesNoUserMessages verifies that a file with only
// assistant/tool messages returns no entries.
func TestCollectPromptHistoryEntriesNoUserMessages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"model.final","content":"response"}
{"kind":"tool.result","output":"done"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := collectPromptHistoryEntries(path, info, identityPromptDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

// TestCollectPromptHistoryEntriesEmptyFile verifies that an empty JSONL file
// returns no entries without error.
func TestCollectPromptHistoryEntriesEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := collectPromptHistoryEntries(path, info, identityPromptDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

// TestScanPromptHistoryFromDir verifies that scanPromptHistoryFromDir scans
// multiple JSONL files and returns prompts newest-first.
func TestScanPromptHistoryFromDir(t *testing.T) {
	app := &App{tabs: map[string]*WorkspaceTab{"t1": {ID: "t1", Ctrl: nil, WorkspaceRoot: ""}}}
	_ = app

	dir := t.TempDir()
	// Write two session files with different mtimes (sleep to ensure ordering).
	if err := os.WriteFile(filepath.Join(dir, "a.jsonl"), []byte(`{"role":"user","content":"older prompt"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "b.jsonl"), []byte(`{"role":"user","content":"newer prompt"}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := app.scanPromptHistoryFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	// Newest-first: "newer prompt" should be first.
	if entries[0].Text != "newer prompt" {
		t.Errorf("expected 'newer prompt' first, got %q", entries[0].Text)
	}
	if entries[1].Text != "older prompt" {
		t.Errorf("expected 'older prompt' second, got %q", entries[1].Text)
	}
}

func TestScanPromptHistoryFromDirUsesSessionActivityBeforeEventInterleaving(t *testing.T) {
	app := &App{}
	dir := t.TempDir()
	base := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	early := filepath.Join(dir, "early.jsonl")
	late := filepath.Join(dir, "late.jsonl")

	if err := os.WriteFile(early, fmt.Appendf(nil, `{"role":"user","content":"early first","time":%d}
{"role":"assistant","content":"ok"}
{"role":"user","content":"early second","time":%d}
`, base.UnixMilli(), base.Add(time.Minute).UnixMilli()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(late, fmt.Appendf(nil, `{"role":"user","content":"late newest","time":%d}
`, base.Add(2*time.Minute).UnixMilli()), 0o644); err != nil {
		t.Fatal(err)
	}
	// Invert file mtimes: session activity should keep each session grouped
	// before event timestamps are considered within that session.
	if err := os.Chtimes(early, base.Add(3*time.Hour), base.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(late, base.Add(-3*time.Hour), base.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}

	entries, err := app.scanPromptHistoryFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	want := []string{"early second", "early first", "late newest"}
	for i, w := range want {
		if entries[i].Text != w {
			t.Fatalf("entries[%d] = %q, want %q; all=%+v", i, entries[i].Text, w, entries)
		}
	}
}

func TestScanPromptHistoryFromDirUsesBranchMetaActivityFallback(t *testing.T) {
	app := &App{}
	dir := t.TempDir()
	base := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	early := filepath.Join(dir, "early.jsonl")
	late := filepath.Join(dir, "late.jsonl")

	if err := os.WriteFile(early, []byte(`{"role":"user","content":"early first"}
{"role":"assistant","content":"ok"}
{"role":"user","content":"early second"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(late, []byte(`{"role":"user","content":"late newest"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(early, agent.BranchMeta{
		CreatedAt: base,
		UpdatedAt: base.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(late, agent.BranchMeta{
		CreatedAt: base.Add(time.Minute),
		UpdatedAt: base.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	// Invert file mtimes: branch UpdatedAt should be the activity clock.
	if err := os.Chtimes(early, base.Add(3*time.Hour), base.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(late, base.Add(-3*time.Hour), base.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}

	entries, err := app.scanPromptHistoryFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	want := []string{"late newest", "early second", "early first"}
	for i, w := range want {
		if entries[i].Text != w {
			t.Fatalf("entries[%d] = %q, want %q; all=%+v", i, entries[i].Text, w, entries)
		}
	}
}

func TestScanPromptHistoryFromDirSkipsEmptyOrderedSessions(t *testing.T) {
	app := &App{}
	dir := t.TempDir()
	base := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	empty := filepath.Join(dir, "empty.jsonl")
	real := filepath.Join(dir, "real.jsonl")

	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte(`{"role":"user","content":"real prompt"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(empty, agent.BranchMeta{
		CreatedAt: base,
		UpdatedAt: base.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(real, agent.BranchMeta{
		CreatedAt: base,
		UpdatedAt: base,
	}); err != nil {
		t.Fatal(err)
	}

	entries, err := app.scanPromptHistoryFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Text != "real prompt" {
		t.Fatalf("entries = %+v, want only real prompt after skipping empty session", entries)
	}
}

func TestScanPromptHistoryUsesCurrentSessionBeforeCrossSession(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "current.jsonl")
	other := filepath.Join(dir, "other.jsonl")
	if err := os.WriteFile(current, []byte(`{"role":"user","content":"current first"}
{"role":"assistant","content":"ok"}
{"role":"user","content":"current second"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte(`{"role":"user","content":"other newest"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	if err := agent.SaveBranchMetaPreserveUpdated(current, agent.BranchMeta{
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(other, agent.BranchMeta{
		CreatedAt: now.Add(time.Minute),
		UpdatedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: current, Label: "test"})
	defer ctrl.Close()
	app.setTestCtrl(ctrl, "")

	result, err := app.ScanPromptHistory("")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 3 {
		t.Fatalf("expected current-session entries followed by cross-session fallback, got %d: %+v", len(result.Entries), result.Entries)
	}
	want := []string{"current second", "current first", "other newest"}
	for i, w := range want {
		if result.Entries[i].Text != w {
			t.Fatalf("entries[%d] = %q, want %q; all=%+v", i, result.Entries[i].Text, w, result.Entries)
		}
	}
}

func TestScanPromptHistoryPaginatesCurrentSessionBeforeCrossSession(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "current.jsonl")
	other := filepath.Join(dir, "other.jsonl")
	var lines []byte
	for i := range 55 {
		lines = append(lines, fmt.Appendf(nil, `{"role":"user","content":"current %d"}
`, i)...)
	}
	if err := os.WriteFile(current, lines, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte(`{"role":"user","content":"other newest"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	if err := agent.SaveBranchMetaPreserveUpdated(current, agent.BranchMeta{
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(other, agent.BranchMeta{
		CreatedAt: now.Add(time.Minute),
		UpdatedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	ctrl := control.New(control.Options{SessionDir: dir, SessionPath: current, Label: "test"})
	defer ctrl.Close()
	app.setTestCtrl(ctrl, "")

	result, err := app.ScanPromptHistory("")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != promptHistoryPageLimit {
		t.Fatalf("expected %d entries, got %d", promptHistoryPageLimit, len(result.Entries))
	}
	if result.Entries[0].Text != "current 54" {
		t.Fatalf("first entry = %q, want current 54", result.Entries[0].Text)
	}
	if result.Entries[len(result.Entries)-1].Text != "current 5" {
		t.Fatalf("last first-page entry = %q, want current 5", result.Entries[len(result.Entries)-1].Text)
	}
	if !result.HasOlder || result.OlderCursor == "" {
		t.Fatalf("first page should expose an older cursor: %+v", result)
	}
	for _, entry := range result.Entries {
		if entry.Text == "other newest" {
			t.Fatalf("cross-session entry appeared before current-session page was exhausted: %+v", result.Entries)
		}
	}

	nextRequest, err := json.Marshal(promptHistoryRequest{Cursor: result.OlderCursor})
	if err != nil {
		t.Fatal(err)
	}
	next, err := app.ScanPromptHistory(string(nextRequest))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"current 4", "current 3", "current 2", "current 1", "current 0", "other newest"}
	if len(next.Entries) != len(want) {
		t.Fatalf("second page entries = %+v, want %d entries", next.Entries, len(want))
	}
	for i, w := range want {
		if next.Entries[i].Text != w {
			t.Fatalf("second page entries[%d] = %q, want %q; all=%+v", i, next.Entries[i].Text, w, next.Entries)
		}
	}
}

func TestScanPromptHistoryFromDirReadsAllEntriesForInternalHelper(t *testing.T) {
	app := &App{}
	dir := t.TempDir()
	var lines []byte
	for i := range 250 {
		lines = append(lines, fmt.Appendf(nil, `{"role":"user","content":"prompt %d"}
`, i)...)
	}
	if err := os.WriteFile(filepath.Join(dir, "many.jsonl"), lines, 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := app.scanPromptHistoryFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 250 {
		t.Fatalf("expected 250 entries, got %d", len(entries))
	}
	if entries[0].Text != "prompt 249" {
		t.Errorf("expected newest 'prompt 249' first, got %q", entries[0].Text)
	}
}

// TestScanPromptHistoryFromDirEmpty verifies an empty directory returns nil.
func TestScanPromptHistoryFromDirEmpty(t *testing.T) {
	app := &App{}
	dir := t.TempDir()
	entries, err := app.scanPromptHistoryFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

// TestScanPromptHistoryCacheHit verifies that ScanPromptHistory returns nil
// on cache hit (nonce matches).
func TestScanPromptHistoryCacheHit(t *testing.T) {
	app := &App{tabs: map[string]*WorkspaceTab{}}
	result, err := app.ScanPromptHistory("")
	if err != nil {
		t.Fatal(err)
	}
	nonce := result.Nonce
	if nonce == "" {
		t.Error("expected a non-empty nonce on first call")
	}

	// Second call with the same nonce should be a cache hit (nil entries).
	result2, err := app.ScanPromptHistory(nonce)
	if err != nil {
		t.Fatal(err)
	}
	if result2.Entries != nil {
		t.Error("expected nil entries on cache hit")
	}
	if result2.Nonce != nonce {
		t.Errorf("expected nonce %q unchanged, got %q", nonce, result2.Nonce)
	}
}

func TestScanPromptHistoryCacheIsScopedBySessionDir(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	pathA := filepath.Join(dirA, "a.jsonl")
	pathB := filepath.Join(dirB, "b.jsonl")
	if err := os.WriteFile(pathA, []byte(`{"role":"user","content":"workspace A"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathB, []byte(`{"role":"user","content":"workspace B"}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	ctrlA := control.New(control.Options{SessionDir: dirA, SessionPath: pathA, Label: "test"})
	ctrlB := control.New(control.Options{SessionDir: dirB, SessionPath: pathB, Label: "test"})
	defer ctrlA.Close()
	defer ctrlB.Close()

	app.setTestCtrl(ctrlA, "")
	first, err := app.ScanPromptHistory("")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 1 || first.Entries[0].Text != "workspace A" {
		t.Fatalf("first entries = %+v, want workspace A", first.Entries)
	}

	app.setTestCtrl(ctrlB, "")
	second, err := app.ScanPromptHistory(first.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	if second.Entries == nil {
		t.Fatal("expected rescan after session dir changes, got cache hit")
	}
	if len(second.Entries) != 1 || second.Entries[0].Text != "workspace B" {
		t.Fatalf("second entries = %+v, want workspace B", second.Entries)
	}
}

func TestScanPromptHistoryCacheIsScopedBySessionPath(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.jsonl")
	pathB := filepath.Join(dir, "b.jsonl")
	if err := os.WriteFile(pathA, []byte(`{"role":"user","content":"session A"}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathB, []byte(`{"role":"user","content":"session B"}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	ctrlA := control.New(control.Options{SessionDir: dir, SessionPath: pathA, Label: "test"})
	ctrlB := control.New(control.Options{SessionDir: dir, SessionPath: pathB, Label: "test"})
	defer ctrlA.Close()
	defer ctrlB.Close()

	app.setTestCtrl(ctrlA, "")
	first, err := app.ScanPromptHistory("")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 2 || first.Entries[0].Text != "session A" || first.Entries[1].Text != "session B" {
		t.Fatalf("first entries = %+v, want session A followed by session B", first.Entries)
	}

	app.setTestCtrl(ctrlB, "")
	second, err := app.ScanPromptHistory(first.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	if second.Entries == nil {
		t.Fatal("expected rescan after session path changes, got cache hit")
	}
	if len(second.Entries) != 2 || second.Entries[0].Text != "session B" || second.Entries[1].Text != "session A" {
		t.Fatalf("second entries = %+v, want session B followed by session A", second.Entries)
	}
}
