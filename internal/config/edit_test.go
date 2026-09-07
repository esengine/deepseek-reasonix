package config

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestUIThemeNormalizes(t *testing.T) {
	c := Default()
	for _, tt := range []struct {
		in   string
		want string
	}{
		{"", "auto"},
		{"AUTO", "auto"},
		{"dark", "dark"},
		{" light ", "light"},
		{"unknown", "auto"},
	} {
		c.UI.Theme = tt.in
		if got := c.UITheme(); got != tt.want {
			t.Errorf("UITheme(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestUIThemeStyleNormalizes(t *testing.T) {
	c := Default()
	for _, tt := range []struct {
		in   string
		want string
	}{
		{"", ""},
		{"AURORA", "aurora"},
		{" nocturne ", "nocturne"},
		{" glacier ", "glacier"},
		{"unknown", ""},
	} {
		c.UI.ThemeStyle = tt.in
		if got := c.UIThemeStyle(); got != tt.want {
			t.Errorf("UIThemeStyle(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestUICursorShapeNormalizes(t *testing.T) {
	c := Default()
	for _, tt := range []struct {
		in   string
		want string
	}{
		{"", "bar"},
		{"UNDERLINE", "underline"},
		{" block ", "block"},
		{"bar", "bar"},
		{"unknown", "bar"},
	} {
		c.UI.CursorShape = tt.in
		if got := c.UICursorShape(); got != tt.want {
			t.Errorf("UICursorShape(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestUICloseBehaviorNormalizes(t *testing.T) {
	c := Default()
	for _, tt := range []struct {
		in   string
		want string
	}{
		{"", "background"},
		{"QUIT", "quit"},
		{"exit", "quit"},
		{" background ", "background"},
		{"hide", "background"},
		{"unknown", "background"},
	} {
		c.UI.CloseBehavior = tt.in
		if got := c.UICloseBehavior(); got != tt.want {
			t.Errorf("UICloseBehavior(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDesktopPreferencesAreSeparateFromCLI(t *testing.T) {
	c := Default()
	c.Language = "zh"
	c.UI.Theme = "light"
	c.UI.ThemeStyle = "glacier"

	if err := c.SetDesktopLanguage("en"); err != nil {
		t.Fatalf("SetDesktopLanguage: %v", err)
	}
	if err := c.SetDesktopAppearance("dark", "graphite"); err != nil {
		t.Fatalf("SetDesktopAppearance: %v", err)
	}
	if err := c.SetDesktopTerminalTheme("light"); err != nil {
		t.Fatalf("SetDesktopTerminalTheme: %v", err)
	}
	if err := c.SetDesktopLayoutStyle("workbench"); err != nil {
		t.Fatalf("SetDesktopLayoutStyle: %v", err)
	}
	if err := c.SetDesktopStatusBarStyle("text"); err != nil {
		t.Fatalf("SetDesktopStatusBarStyle: %v", err)
	}
	if err := c.SetDesktopStatusBarItems([]string{"model", "balance", "cache"}); err != nil {
		t.Fatalf("SetDesktopStatusBarItems: %v", err)
	}

	if c.Language != "zh" {
		t.Fatalf("CLI language changed to %q", c.Language)
	}
	if got := c.UITheme(); got != "light" {
		t.Fatalf("CLI theme = %q, want light", got)
	}
	if got := c.UIThemeStyle(); got != "glacier" {
		t.Fatalf("CLI theme style = %q, want glacier", got)
	}
	if got := c.DesktopLanguage(); got != "en" {
		t.Fatalf("desktop language = %q, want en", got)
	}
	if got := c.DesktopTheme(); got != "dark" {
		t.Fatalf("desktop theme = %q, want dark", got)
	}
	if got := c.DesktopThemeStyle(); got != "graphite" {
		t.Fatalf("desktop theme style = %q, want graphite", got)
	}
	if got := c.DesktopTerminalTheme(); got != "light" {
		t.Fatalf("desktop terminal theme = %q, want light", got)
	}
	if got := c.DesktopLayoutStyle(); got != "workbench" {
		t.Fatalf("desktop layout style = %q, want workbench", got)
	}
	if got := c.DesktopStatusBarStyle(); got != "text" {
		t.Fatalf("desktop status bar style = %q, want text", got)
	}
	if got, want := c.DesktopStatusBarItems(), []string{"model", "balance", "cache"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("desktop status bar items = %v, want %v", got, want)
	}
}

func TestSetDesktopTerminalThemeValidatesPreference(t *testing.T) {
	c := Default()
	for _, theme := range []string{"auto", "dark", "light"} {
		if err := c.SetDesktopTerminalTheme(theme); err != nil {
			t.Fatalf("SetDesktopTerminalTheme(%q): %v", theme, err)
		}
		if got := c.DesktopTerminalTheme(); got != theme {
			t.Fatalf("DesktopTerminalTheme() = %q, want %q", got, theme)
		}
	}
	if err := c.SetDesktopTerminalTheme("sepia"); err == nil {
		t.Fatal("SetDesktopTerminalTheme(sepia) succeeded, want validation error")
	}
}

func TestDesktopCurrencyNormalizesAndRefreshesOfficialPricing(t *testing.T) {
	c := Default()
	c.Desktop.Language = "zh"
	flash, _ := c.Provider("deepseek-flash")
	// Capture frozen list price before display switches.
	wantOutput := flash.Price.Output
	wantCurrency := flash.Price.Currency
	if err := c.SetDesktopCurrency("usd"); err != nil {
		t.Fatalf("SetDesktopCurrency USD: %v", err)
	}
	if got := c.DesktopCurrency(); got != "USD" {
		t.Fatalf("desktop currency = %q, want USD", got)
	}
	if got := c.DisplayCurrencyPref(); got != "USD" {
		t.Fatalf("display currency pref = %q, want USD", got)
	}
	// Display currency must not rewrite frozen provider list prices.
	if flash.Price == nil || flash.Price.Output != wantOutput || flash.Price.Currency != wantCurrency {
		t.Fatalf("list price mutated by display switch: %+v", flash.Price)
	}
	if err := c.SetDesktopCurrency("auto"); err != nil {
		t.Fatalf("SetDesktopCurrency auto: %v", err)
	}
	if got := c.DesktopCurrency(); got != "" {
		t.Fatalf("auto desktop currency = %q, want empty", got)
	}
	if flash.Price == nil || flash.Price.Output != wantOutput || flash.Price.Currency != wantCurrency {
		t.Fatalf("list price mutated after auto: %+v", flash.Price)
	}
	if err := c.SetDesktopCurrency("EUR"); err == nil {
		t.Fatal("SetDesktopCurrency accepted unsupported EUR")
	}
}

func TestDesktopLayoutStyleNormalizes(t *testing.T) {
	if got := Default().DesktopLayoutStyle(); got != "workbench" {
		t.Fatalf("default desktop layout style = %q, want workbench", got)
	}
	for _, tt := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "workbench", false},
		{"classic", "classic", false},
		{" workbench ", "workbench", false},
		{"workspace", "workbench", false},
		{"creation", "creation", false},
		{" Creation ", "creation", false},
		{"later", "workbench", true},
	} {
		c := Default()
		if err := c.SetDesktopLayoutStyle(tt.in); (err != nil) != tt.wantErr {
			t.Fatalf("SetDesktopLayoutStyle(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if got := c.DesktopLayoutStyle(); got != tt.want {
			t.Fatalf("DesktopLayoutStyle(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}

	c := Default()
	c.Desktop.ThemeStyle = "workbench"
	if got := c.DesktopLayoutStyle(); got != "workbench" {
		t.Fatalf("legacy desktop theme_style=workbench layout = %q, want workbench", got)
	}
	if got := c.DesktopThemeStyle(); got != "" {
		t.Fatalf("legacy desktop theme_style=workbench theme style = %q, want empty", got)
	}
}

func TestDesktopConversationWidthNormalizes(t *testing.T) {
	if got := Default().DesktopConversationWidth(); got != "standard" {
		t.Fatalf("default desktop conversation width = %q, want standard", got)
	}

	for _, tt := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "standard", false},
		{"standard", "standard", false},
		{" FULL ", "full", false},
		{"wide", "standard", true},
	} {
		c := Default()
		if err := c.SetDesktopConversationWidth(tt.in); (err != nil) != tt.wantErr {
			t.Fatalf("SetDesktopConversationWidth(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if got := c.DesktopConversationWidth(); got != tt.want {
			t.Fatalf("DesktopConversationWidth(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}

	c := Default()
	c.Desktop.ConversationWidth = " FULL "
	if got := c.DesktopConversationWidth(); got != "full" {
		t.Fatalf("manually edited conversation width = %q, want full", got)
	}
}

func TestDesktopExternalOpenerValidation(t *testing.T) {
	c := Default()
	if got := c.DesktopExternalOpener(); got != "" {
		t.Fatalf("default external opener = %q, want empty platform fallback", got)
	}
	if err := c.SetDesktopExternalOpener(" Cursor "); err != nil {
		t.Fatalf("SetDesktopExternalOpener: %v", err)
	}
	if got := c.DesktopExternalOpener(); got != "cursor" {
		t.Fatalf("DesktopExternalOpener = %q, want cursor", got)
	}
	for _, invalid := range []string{"../../bin/sh", "vscode;open", "app id"} {
		if err := c.SetDesktopExternalOpener(invalid); err == nil {
			t.Fatalf("SetDesktopExternalOpener(%q) unexpectedly succeeded", invalid)
		}
	}
	if err := c.SetDesktopExternalOpener(""); err != nil || c.DesktopExternalOpener() != "" {
		t.Fatalf("clearing external opener = (%q, %v), want empty", c.DesktopExternalOpener(), err)
	}
}

func TestDesktopStatusBarStyleNormalizes(t *testing.T) {
	if got := Default().DesktopStatusBarStyle(); got != "text" {
		t.Fatalf("default desktop status bar style = %q, want text", got)
	}
	for _, tt := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "text", false},
		{"icon", "icon", false},
		{"icons", "icon", false},
		{"text", "text", false},
		{"labels", "text", false},
		{"later", "text", true},
	} {
		c := Default()
		if err := c.SetDesktopStatusBarStyle(tt.in); (err != nil) != tt.wantErr {
			t.Fatalf("SetDesktopStatusBarStyle(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if got := c.DesktopStatusBarStyle(); got != tt.want {
			t.Fatalf("DesktopStatusBarStyle(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDesktopStatusBarItemsNormalizeAndValidate(t *testing.T) {
	if got, want := Default().DesktopStatusBarItems(), DefaultDesktopStatusBarItems(); !reflect.DeepEqual(got, want) {
		t.Fatalf("default desktop status bar items = %v, want %v", got, want)
	}
	for _, id := range []string{"workspace", "git_branch"} {
		if !slices.Contains(DefaultDesktopStatusBarItems(), id) {
			t.Fatalf("default desktop status bar items must include configurable item %q", id)
		}
	}

	c := Default()
	c.Desktop.StatusBarItems = []string{" balance ", "cache", "cache", "unknown", "model"}
	if got, want := c.DesktopStatusBarItems(), []string{"balance", "cache", "model"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized desktop status bar items = %v, want %v", got, want)
	}

	c = Default()
	if err := c.SetDesktopStatusBarItems([]string{"balance", "cache", "balance", "model"}); err != nil {
		t.Fatalf("SetDesktopStatusBarItems subset: %v", err)
	}
	if got, want := c.DesktopStatusBarItems(), []string{"balance", "cache", "model"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("saved desktop status bar items = %v, want %v", got, want)
	}

	c = Default()
	if err := c.SetDesktopStatusBarItems([]string{"workspace", "git_branch", "model"}); err != nil {
		t.Fatalf("SetDesktopStatusBarItems workspace metadata: %v", err)
	}
	if got, want := c.DesktopStatusBarItems(), []string{"workspace", "git_branch", "model"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("saved workspace metadata status bar items = %v, want %v", got, want)
	}

	if err := c.SetDesktopStatusBarItems(nil); err != nil {
		t.Fatalf("SetDesktopStatusBarItems nil: %v", err)
	}
	if got, want := c.DesktopStatusBarItems(), DefaultDesktopStatusBarItems(); !reflect.DeepEqual(got, want) {
		t.Fatalf("nil desktop status bar items = %v, want default %v", got, want)
	}

	if err := c.SetDesktopStatusBarItems([]string{"ghost"}); err == nil {
		t.Fatal("expected error for unknown status bar item")
	}
}

func TestDesktopCloseBehaviorFallsBackToLegacyUI(t *testing.T) {
	c := Default()
	c.UI.CloseBehavior = "quit"
	if got := c.DesktopCloseBehavior(); got != "quit" {
		t.Fatalf("legacy close behavior = %q, want quit", got)
	}
	c.Desktop.CloseBehavior = "background"
	if got := c.DesktopCloseBehavior(); got != "background" {
		t.Fatalf("desktop close behavior = %q, want background", got)
	}
}

func TestSetUICloseBehavior(t *testing.T) {
	c := Default()
	if err := c.SetUICloseBehavior("background"); err != nil {
		t.Fatalf("SetUICloseBehavior background: %v", err)
	}
	if got := c.UICloseBehavior(); got != "background" {
		t.Fatalf("close behavior = %q, want background", got)
	}
	if err := c.SetUICloseBehavior("quit"); err != nil {
		t.Fatalf("SetUICloseBehavior quit: %v", err)
	}
	if got := c.UICloseBehavior(); got != "quit" {
		t.Fatalf("close behavior = %q, want quit", got)
	}
	if err := c.SetUICloseBehavior("later"); err == nil {
		t.Fatal("expected error for invalid close behavior")
	}
}

func TestSetPlannerModel(t *testing.T) {
	c := Default()
	if err := c.SetPlannerModel("deepseek-pro"); err != nil {
		t.Fatalf("set planner: %v", err)
	}
	if c.Agent.PlannerModel != "deepseek-pro" {
		t.Errorf("planner = %q", c.Agent.PlannerModel)
	}
	if err := c.SetPlannerModel(""); err != nil || c.Agent.PlannerModel != "" {
		t.Errorf("clearing planner failed: err=%v planner=%q", err, c.Agent.PlannerModel)
	}
	if err := c.SetPlannerModel("ghost"); err == nil {
		t.Error("expected error for unknown planner")
	}
}

func TestSetAutoPlanRejectsRetiredModes(t *testing.T) {
	c := Default()
	if err := c.SetAutoPlan("off"); err != nil {
		t.Fatalf("SetAutoPlan(off): %v", err)
	}
	if c.Agent.AutoPlan != "off" || c.Agent.AutoPlanClassifier != "" {
		t.Fatalf("retired auto-plan state = (%q, %q), want off/empty", c.Agent.AutoPlan, c.Agent.AutoPlanClassifier)
	}
	for _, mode := range []string{"on", "ask", "auto"} {
		if err := c.SetAutoPlan(mode); err == nil || !strings.Contains(err.Error(), "retired") {
			t.Fatalf("SetAutoPlan(%q) err = %v, want retired error", mode, err)
		}
	}
}

func TestSetDesktopDefaultToolApprovalMode(t *testing.T) {
	c := Default()
	if got := c.DesktopDefaultToolApprovalMode(); got != "auto" {
		t.Fatalf("desktop default tool approval mode = %q, want built-in auto", got)
	}
	for _, mode := range []string{"ask", "auto", "yolo"} {
		if err := c.SetDesktopDefaultToolApprovalMode(mode); err != nil {
			t.Fatalf("SetDesktopDefaultToolApprovalMode(%q): %v", mode, err)
		}
		if c.DesktopDefaultToolApprovalMode() != mode {
			t.Fatalf("desktop default tool approval mode = %q, want %q", c.DesktopDefaultToolApprovalMode(), mode)
		}
	}
	if err := c.SetDesktopDefaultToolApprovalMode("full-access"); err != nil {
		t.Fatalf("legacy full-access should be accepted: %v", err)
	}
	if c.DesktopDefaultToolApprovalMode() != "yolo" {
		t.Fatalf("legacy full-access should save as yolo, got %q", c.DesktopDefaultToolApprovalMode())
	}
	if err := c.SetDesktopDefaultToolApprovalMode("maybe"); err == nil {
		t.Fatal("expected error for invalid desktop default tool approval mode")
	}
}

func TestLoadForEditMissingDesktopApprovalDefaultsAuto(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("config_version = 4\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if got := LoadForEdit(path).DesktopDefaultToolApprovalMode(); got != "auto" {
		t.Fatalf("missing desktop default tool approval mode = %q, want auto", got)
	}
}

func TestSetUIShortcutLayout(t *testing.T) {
	c := Default()
	if got := c.UIShortcutLayout(); got != "classic" {
		t.Fatalf("default shortcut layout = %q, want classic", got)
	}
	if err := c.SetUIShortcutLayout("desktop"); err != nil {
		t.Fatalf("SetUIShortcutLayout desktop: %v", err)
	}
	if got := c.UIShortcutLayout(); got != "desktop" {
		t.Fatalf("shortcut layout = %q, want desktop", got)
	}
	if err := c.SetUIShortcutLayout("dual-axis"); err != nil {
		t.Fatalf("SetUIShortcutLayout alias: %v", err)
	}
	if got := c.UIShortcutLayout(); got != "desktop" {
		t.Fatalf("shortcut layout alias = %q, want desktop", got)
	}
	if err := c.SetUIShortcutLayout("classic"); err != nil {
		t.Fatalf("SetUIShortcutLayout classic: %v", err)
	}
	if got := c.UIShortcutLayout(); got != "classic" {
		t.Fatalf("shortcut layout = %q, want classic", got)
	}
	if err := c.SetUIShortcutLayout("surprise"); err == nil {
		t.Fatal("expected error for invalid shortcut layout")
	}
}

func TestUpsertProvider(t *testing.T) {
	c := Default()
	n := len(c.Providers)

	// Add a new one.
	if err := c.UpsertProvider(ProviderEntry{Name: "local", Kind: "openai", BaseURL: "http://localhost:1234/v1", Model: "x"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(c.Providers) != n+1 {
		t.Fatalf("provider count = %d, want %d", len(c.Providers), n+1)
	}

	// Replace it in place (no growth, position preserved).
	if err := c.UpsertProvider(ProviderEntry{Name: "local", Kind: "openai", BaseURL: "http://localhost:9999/v1", Model: "y"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if len(c.Providers) != n+1 {
		t.Errorf("replace grew the list to %d", len(c.Providers))
	}
	got, _ := c.Provider("local")
	if got.BaseURL != "http://localhost:9999/v1" || got.Model != "y" {
		t.Errorf("replace didn't apply: %+v", got)
	}

	// Multi-model providers may omit the back-compat single model field.
	if err := c.UpsertProvider(ProviderEntry{
		Name:    "multi",
		Kind:    "openai",
		BaseURL: "http://localhost:8888/v1",
		Models:  []string{"m1", "m2"},
		Default: "m1",
	}); err != nil {
		t.Fatalf("multi-model add: %v", err)
	}

	// Missing required fields error.
	for _, bad := range []ProviderEntry{
		{Kind: "openai", BaseURL: "u", Model: "m"},                                   // no name
		{Name: "a", BaseURL: "u", Model: "m"},                                        // no kind
		{Name: "a", Kind: "openai", Model: "m"},                                      // no base_url
		{Name: "a", Kind: "openai", BaseURL: "u"},                                    // no model
		{Name: "a", Kind: "openai", BaseURL: "u", Model: "m", APIKeyEnv: "grok-4.5"}, // invalid credential variable name
	} {
		if err := c.UpsertProvider(bad); err == nil {
			t.Errorf("expected validation error for %+v", bad)
		}
	}
}

func TestSetProviderEffort(t *testing.T) {
	c := Default()
	if err := c.SetProviderEffort("deepseek-flash", "MAX"); err != nil {
		t.Fatalf("SetProviderEffort: %v", err)
	}
	p, _ := c.Provider("deepseek-flash")
	if p.Effort != "max" {
		t.Fatalf("effort = %q, want max", p.Effort)
	}
	if err := c.SetProviderEffort("missing", "high"); err == nil {
		t.Fatal("SetProviderEffort should reject unknown provider")
	}
}

func TestSetReasoningLanguage(t *testing.T) {
	c := Default()
	if err := c.SetReasoningLanguage("中文"); err != nil {
		t.Fatalf("SetReasoningLanguage zh: %v", err)
	}
	if c.Agent.ReasoningLanguage != "zh" || c.ReasoningLanguage() != "zh" {
		t.Fatalf("reasoning language = %q/%q, want zh", c.Agent.ReasoningLanguage, c.ReasoningLanguage())
	}
	if err := c.SetReasoningLanguage("model-default"); err != nil {
		t.Fatalf("SetReasoningLanguage legacy default: %v", err)
	}
	if c.Agent.ReasoningLanguage != "" || c.ReasoningLanguage() != "auto" {
		t.Fatalf("legacy default should normalize to empty/auto, got %q/%q", c.Agent.ReasoningLanguage, c.ReasoningLanguage())
	}
	if err := c.SetReasoningLanguage("auto"); err != nil {
		t.Fatalf("SetReasoningLanguage auto: %v", err)
	}
	if c.Agent.ReasoningLanguage != "" || c.ReasoningLanguage() != "auto" {
		t.Fatalf("reasoning language = %q/%q, want empty/auto", c.Agent.ReasoningLanguage, c.ReasoningLanguage())
	}
	if err := c.SetReasoningLanguage("klingon"); err == nil {
		t.Fatal("SetReasoningLanguage should reject unknown values")
	}
}

func TestSetCompactRatio(t *testing.T) {
	c := Default()
	for _, ratio := range []float64{0.30, 0.64, 0.65, 0.7, 0.8, 0.85} {
		if err := c.SetCompactRatio(ratio); err != nil {
			t.Fatalf("SetCompactRatio(%v): %v", ratio, err)
		}
		if c.Agent.CompactRatio != ratio {
			t.Fatalf("compact ratio = %v, want %v", c.Agent.CompactRatio, ratio)
		}
	}

	previous := c.Agent.CompactRatio
	for _, ratio := range []float64{0.29, 0.86, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := c.SetCompactRatio(ratio); err == nil {
			t.Fatalf("SetCompactRatio(%v) should fail", ratio)
		}
		if c.Agent.CompactRatio != previous {
			t.Fatalf("rejected ratio %v changed compact ratio to %v", ratio, c.Agent.CompactRatio)
		}
	}

	// Deprecated snip/force ratios no longer constrain SetCompactRatio.
	c.Agent.ToolResultSnipRatio = 0.75
	c.Agent.CompactForceRatio = 0.8
	if err := c.SetCompactRatio(0.7); err != nil {
		t.Fatalf("SetCompactRatio(0.7) with legacy snip/force fields: %v", err)
	}
	if err := c.SetCompactRatio(0.8); err != nil {
		t.Fatalf("SetCompactRatio(0.8) with legacy force field: %v", err)
	}
}

func TestNormalizeEffortDeepSeek(t *testing.T) {
	e := &ProviderEntry{Name: "deepseek", Kind: "openai", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4"}
	cap := EffortCapabilityForEntry(e)
	if !cap.Supported || len(cap.Levels) != 4 || cap.Levels[0] != "auto" || cap.Levels[1] != "disabled" || cap.Levels[2] != "high" || cap.Levels[3] != "max" {
		t.Fatalf("DeepSeek levels = %+v, want auto/disabled/high/max", cap)
	}
	for in, want := range map[string]string{"auto": "", "disabled": "disabled", "high": "high", "max": "max", "low": "high", "medium": "high", "xhigh": "max"} {
		got, err := NormalizeEffort(e, in)
		if in != want && in != "auto" {
			if err == nil {
				t.Fatalf("undeclared %q accepted as %q", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Fatalf("NormalizeEffort(%q) = %q/%v, want %q/nil", in, got, err, want)
		}
	}
	// "off" is the retired DeepSeek "no thinking" spelling — now maps to disabled.
	if got, err := NormalizeEffort(e, "off"); err == nil {
		t.Fatalf("NormalizeEffort(\"off\") = %q/%v, want \"disabled\"/nil", got, err)
	}
}

func TestNormalizeLegacyEffortMigratesProviderDefaults(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{
		{Name: "deepseek", Effort: "off"},
		{Name: "deepseek-upper", Effort: "OFF"},
		{Name: "deepseek-auto", Effort: "auto"},
		{Name: "deepseek-auto-upper", Effort: "AUTO"},
		{Name: "keep", Effort: "high"},
	}}
	normalizeLegacyEffort(c)
	normalizeEffortConfig(c)
	if c.Providers[0].Effort != "" || c.Providers[1].Effort != "" || c.Providers[2].Effort != "" || c.Providers[3].Effort != "" {
		t.Fatalf("provider default efforts should migrate to empty, got %q/%q/%q/%q", c.Providers[0].Effort, c.Providers[1].Effort, c.Providers[2].Effort, c.Providers[3].Effort)
	}
	if c.Providers[4].Effort != "high" {
		t.Fatalf("non-legacy effort changed: %q", c.Providers[4].Effort)
	}
}

func TestNormalizeEffortAnthropic(t *testing.T) {
	e := &ProviderEntry{Name: "claude", Kind: "anthropic", Model: "claude-opus-4-8"}
	cap := EffortCapabilityForEntry(e)
	if !cap.Supported || len(cap.Levels) != 6 {
		t.Fatalf("Anthropic levels = %+v, want auto plus five levels", cap)
	}
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		got, err := NormalizeEffort(e, level)
		if err != nil || got != level {
			t.Fatalf("NormalizeEffort(%q) = %q/%v, want %q/nil", level, got, err, level)
		}
	}
	got, err := NormalizeEffort(e, "auto")
	if err != nil || got != "" {
		t.Fatalf("NormalizeEffort(auto) = %q/%v, want empty/nil", got, err)
	}
}

func TestResolveModelPreservesProviderEffort(t *testing.T) {
	c := Default()
	c.Providers = append(c.Providers, ProviderEntry{
		Name:      "deepseek",
		Kind:      "openai",
		BaseURL:   "https://api.deepseek.com",
		Model:     "deepseek-v4-flash",
		Models:    []string{"deepseek-v4-flash", "deepseek-v4-pro"},
		Default:   "deepseek-v4-flash",
		APIKeyEnv: "DEEPSEEK_API_KEY",
		Effort:    "max",
	})
	e, ok := c.ResolveModel("deepseek/deepseek-v4-pro")
	if !ok {
		t.Fatal("ResolveModel did not find deepseek/deepseek-v4-pro")
	}
	if e.Name != "deepseek" || e.Model != "deepseek-v4-pro" || e.Effort != "max" {
		t.Fatalf("resolved entry = %+v, want provider deepseek model deepseek-v4-pro effort max", e)
	}
}

func TestEffectiveVisionForMimoEndpointModels(t *testing.T) {
	c := Default()
	c.Providers = append(c.Providers, legacyMimoCustomProvider("mimo-api"))
	c.Desktop.ProviderAccess = []string{"mimo-api"}
	normalizeDesktopOfficialProviderAccess(c)

	pro, ok := c.ResolveModel("mimo-api/mimo-v2.5-pro")
	if !ok {
		t.Fatal("ResolveModel did not find mimo-api/mimo-v2.5-pro")
	}
	if EffectiveVision(pro) {
		t.Fatalf("mimo-v2.5-pro should remain text-only by default")
	}

	vision, ok := c.ResolveModel("mimo-api/mimo-v2.5")
	if !ok {
		t.Fatal("ResolveModel did not find mimo-api/mimo-v2.5")
	}
	if !EffectiveVision(vision) {
		t.Fatalf("mimo-v2.5 on the official MiMo API should enable vision")
	}

	omni, ok := c.ResolveModel("mimo-api/mimo-v2-omni")
	if !ok {
		t.Fatal("ResolveModel did not find mimo-api/mimo-v2-omni")
	}
	if !EffectiveVision(omni) {
		t.Fatalf("mimo-v2-omni on the official MiMo API should enable vision")
	}
}

func TestEffectiveVisionDoesNotInferCustomMimoProxy(t *testing.T) {
	custom := &ProviderEntry{
		Name:    "mimo-proxy",
		Kind:    "openai",
		BaseURL: "https://proxy.example.com/v1",
		Model:   "mimo-v2.5",
	}
	if EffectiveVision(custom) {
		t.Fatalf("custom MiMo proxy should require explicit vision=true")
	}
	custom.Vision = true
	if !EffectiveVision(custom) {
		t.Fatalf("explicit vision=true should still enable custom providers")
	}
}

func TestEffectiveVisionRejectsOfficialDeepSeekOverridesButPreservesCustomGateways(t *testing.T) {
	for _, endpoint := range []struct {
		kind    string
		baseURL string
	}{
		{kind: "openai", baseURL: "https://api.deepseek.com"},
		{kind: "openai", baseURL: "https://api.deepseek.com/v1"},
		{kind: "openai", baseURL: "https://eu.deepseek.com/v1"},
		{kind: "anthropic", baseURL: "https://api.deepseek.com/anthropic"},
	} {
		visionOn := true
		official := &ProviderEntry{
			Name:              "deepseek",
			Kind:              endpoint.kind,
			BaseURL:           endpoint.baseURL,
			Model:             "deepseek-v4-pro",
			Vision:            true,
			VisionModels:      []string{"deepseek-v4-pro"},
			visionOverride:    &visionOn,
			ReasoningProtocol: ReasoningProtocolDeepSeek,
		}
		if !CanConfigureVision(official) {
			t.Fatalf("official DeepSeek endpoint %q must allow Settings vision checkboxes", endpoint.baseURL)
		}
		if EffectiveVision(official) {
			t.Fatalf("official DeepSeek endpoint %q must remain text-only", endpoint.baseURL)
		}
		if ExplicitModelVision(official) {
			t.Fatalf("official DeepSeek endpoint %q must not expose ignored vision metadata as usable", endpoint.baseURL)
		}
		if !official.HasVisionModel("deepseek-v4-pro") {
			t.Fatalf("official DeepSeek endpoint %q lost persisted vision metadata instead of ignoring it", endpoint.baseURL)
		}
	}

	future := &ProviderEntry{
		Name:         "deepseek",
		Kind:         "openai",
		BaseURL:      "https://api.deepseek.com",
		Model:        "deepseek-v5-vision",
		VisionModels: []string{"deepseek-v5-vision"},
	}
	if EffectiveVision(future) {
		t.Fatal("a future model name must not bypass the official DeepSeek wire constraint")
	}

	visionOn := true
	cfg := &Config{Providers: []ProviderEntry{{
		Name:    "deepseek",
		Kind:    "openai",
		BaseURL: "https://api.deepseek.com",
		Models:  []string{"deepseek-v5-override"},
		ModelOverrides: map[string]ProviderModelOverride{
			"deepseek-v5-override": {Vision: &visionOn},
		},
	}}}
	overridden, ok := cfg.ResolveModel("deepseek/deepseek-v5-override")
	if !ok {
		t.Fatal("ResolveModel did not find explicit future DeepSeek model")
	}
	if EffectiveVision(overridden) {
		t.Fatal("model_overrides vision=true must not bypass the official DeepSeek wire constraint")
	}

	custom := &ProviderEntry{
		Name:              "deepseek-gateway",
		Kind:              "openai",
		BaseURL:           "https://gateway.example/v1",
		Model:             "deepseek-v4-pro",
		Vision:            true,
		ReasoningProtocol: ReasoningProtocolDeepSeek,
	}
	if !CanConfigureVision(custom) || !EffectiveVision(custom) {
		t.Fatal("explicit vision=true must remain available for custom DeepSeek gateways")
	}
	custom.Vision = false
	custom.VisionModels = []string{"deepseek-v4-pro"}
	if !ExplicitModelVision(custom) {
		t.Fatal("custom DeepSeek gateway must expose positive model-scoped vision metadata")
	}
}

func TestEffectiveVisionUsesPerModelVisionList(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{{
		Name:         "custom",
		Kind:         "openai",
		BaseURL:      "https://proxy.example.com/v1",
		Models:       []string{"text-only", "qwen-vl-plus"},
		Default:      "text-only",
		VisionModels: []string{"qwen-vl-plus"},
	}}}

	textOnly, ok := c.ResolveModel("custom/text-only")
	if !ok {
		t.Fatal("ResolveModel did not find custom/text-only")
	}
	if EffectiveVision(textOnly) {
		t.Fatalf("text-only should remain text-only when not listed in vision_models")
	}

	vision, ok := c.ResolveModel("custom/qwen-vl-plus")
	if !ok {
		t.Fatal("ResolveModel did not find custom/qwen-vl-plus")
	}
	if !EffectiveVision(vision) {
		t.Fatalf("model listed in vision_models should enable image input")
	}

	textOnly.Vision = true
	if !EffectiveVision(textOnly) {
		t.Fatalf("provider-level vision=true should still enable every selected model")
	}
}

func TestResolveModelAppliesModelOverrides(t *testing.T) {
	visionOff := false
	c := &Config{Providers: []ProviderEntry{{
		Name:              "gateway",
		Kind:              "openai",
		BaseURL:           "https://proxy.example.com/v1",
		Models:            []string{"deepseek-v4-flash", "plain-chat"},
		Default:           "plain-chat",
		ContextWindow:     131_072,
		MaxOutputTokens:   8_192,
		ReasoningProtocol: ReasoningProtocolOpenAI,
		SupportedEfforts:  []string{"low", "medium", "high"},
		ModelOverrides: map[string]ProviderModelOverride{
			"deepseek-v4-flash": {
				ReasoningProtocol: ReasoningProtocolDeepSeek,
				SupportedEfforts:  []string{"high", "max"},
				DefaultEffort:     "max",
				Vision:            &visionOff,
				ContextWindow:     1_000_000,
				MaxOutputTokens:   32_768,
			},
		},
	}}}

	deepseek, ok := c.ResolveModel("gateway/deepseek-v4-flash")
	if !ok {
		t.Fatal("ResolveModel did not find gateway/deepseek-v4-flash")
	}
	if protocol := ReasoningProtocolForEntry(deepseek); protocol != ReasoningProtocolDeepSeek {
		t.Fatalf("deepseek protocol = %q, want deepseek", protocol)
	}
	cap := EffortCapabilityForEntry(deepseek)
	if cap.Default != "max" || !containsString(cap.Levels, "max") || containsString(cap.Levels, "low") {
		t.Fatalf("deepseek effort capability = %+v, want high|max default max", cap)
	}
	if EffectiveVision(deepseek) {
		t.Fatalf("vision override false should disable image input")
	}
	if deepseek.ContextWindow != 1_000_000 {
		t.Fatalf("deepseek context window = %d, want per-model override", deepseek.ContextWindow)
	}
	if deepseek.MaxOutputTokens != 32_768 {
		t.Fatalf("deepseek max output tokens = %d, want per-model override", deepseek.MaxOutputTokens)
	}

	plain, ok := c.ResolveModel("gateway/plain-chat")
	if !ok {
		t.Fatal("ResolveModel did not find gateway/plain-chat")
	}
	if protocol := ReasoningProtocolForEntry(plain); protocol != ReasoningProtocolOpenAI {
		t.Fatalf("plain protocol = %q, want provider-level openai", protocol)
	}
	if plain.ContextWindow != 131_072 {
		t.Fatalf("plain context window = %d, want inherited provider value", plain.ContextWindow)
	}
	if plain.MaxOutputTokens != 8_192 {
		t.Fatalf("plain max output tokens = %d, want inherited provider value", plain.MaxOutputTokens)
	}
}

func TestRemoveProvider(t *testing.T) {
	c := Default()
	c.Agent.PlannerModel = "deepseek-pro"

	// Cannot remove the default model when no configured fallback is available.
	for i := range c.Providers {
		c.Providers[i].APIKeyEnv = ""
	}
	if err := c.RemoveProvider(c.DefaultModel); err == nil {
		t.Error("expected error removing the default model")
	}
	// Removing the planner provider clears planner_model.
	if err := c.RemoveProvider("deepseek-pro"); err != nil {
		t.Fatalf("remove planner provider: %v", err)
	}
	if c.Agent.PlannerModel != "" {
		t.Errorf("planner should be cleared, got %q", c.Agent.PlannerModel)
	}
	if _, ok := c.Provider("deepseek-pro"); ok {
		t.Error("provider not actually removed")
	}
	// Unknown name errors.
	if err := c.RemoveProvider("ghost"); err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestPermissionMutators(t *testing.T) {
	c := Default()

	if err := c.SetPermissionMode("DENY"); err != nil || c.Permissions.Mode != "deny" {
		t.Errorf("set mode: err=%v mode=%q", err, c.Permissions.Mode)
	}
	if err := c.SetPermissionMode("nonsense"); err == nil {
		t.Error("expected error for bad mode")
	}

	if err := c.AddPermissionRule("deny", "Bash(rm -rf*)"); err != nil {
		t.Fatalf("add deny: %v", err)
	}
	// Duplicate is a no-op, not an error or a second entry.
	if err := c.AddPermissionRule("deny", "Bash(rm -rf*)"); err != nil {
		t.Fatalf("dup add: %v", err)
	}
	if len(c.Permissions.Deny) != 1 {
		t.Errorf("deny list = %v, want one entry", c.Permissions.Deny)
	}
	// Invalid rule and unknown list both error.
	if err := c.AddPermissionRule("deny", "  "); err == nil {
		t.Error("expected error for empty rule")
	}
	if err := c.AddPermissionRule("nope", "read_file"); err == nil {
		t.Error("expected error for unknown list")
	}

	removed, err := c.RemovePermissionRule("deny", "Bash(rm -rf*)")
	if err != nil || !removed {
		t.Errorf("remove: removed=%v err=%v", removed, err)
	}
	if removed, _ := c.RemovePermissionRule("deny", "absent"); removed {
		t.Error("removing absent rule should report false")
	}
}

func TestSkillPathMutators(t *testing.T) {
	c := Default()
	root := t.TempDir()
	if err := c.ExcludeSkillPath(root); err != nil {
		t.Fatalf("exclude skill path: %v", err)
	}
	if err := c.AddSkillPath(root); err != nil {
		t.Fatalf("add skill path: %v", err)
	}
	if len(c.Skills.ExcludedPaths) != 0 {
		t.Fatalf("add skill path should restore excluded path, got %v", c.Skills.ExcludedPaths)
	}
	if err := c.AddSkillPath(filepath.Join(root, ".")); err != nil {
		t.Fatalf("duplicate skill path: %v", err)
	}
	if len(c.Skills.Paths) != 1 {
		t.Fatalf("paths = %v, want one deduped entry", c.Skills.Paths)
	}
	if err := c.AddSkillPath(" "); err == nil {
		t.Fatal("empty skill path should error")
	}
	removed, err := c.RemoveSkillPath(filepath.Join(root, "."))
	if err != nil || !removed {
		t.Fatalf("remove skill path: removed=%v err=%v", removed, err)
	}
	if len(c.Skills.Paths) != 0 {
		t.Fatalf("paths after remove = %v", c.Skills.Paths)
	}
	if removed, err := c.RemoveSkillPath(root); err != nil || removed {
		t.Fatalf("remove absent: removed=%v err=%v", removed, err)
	}
	if err := c.ExcludeSkillPath(filepath.Join(root, ".")); err != nil {
		t.Fatalf("exclude skill path: %v", err)
	}
	if err := c.ExcludeSkillPath(root); err != nil {
		t.Fatalf("duplicate exclude skill path: %v", err)
	}
	if len(c.Skills.ExcludedPaths) != 1 {
		t.Fatalf("excluded paths = %v, want one deduped entry", c.Skills.ExcludedPaths)
	}
	if err := c.ExcludeSkillPath(" "); err == nil {
		t.Fatal("empty excluded skill path should error")
	}
	if err := c.RestoreSkillPath(root); err != nil {
		t.Fatalf("restore skill path: %v", err)
	}
	if len(c.Skills.ExcludedPaths) != 0 {
		t.Fatalf("excluded paths after restore = %v, want empty", c.Skills.ExcludedPaths)
	}
	if err := c.RestoreSkillPath(" "); err == nil {
		t.Fatal("empty restored skill path should error")
	}
}

func TestSkillPathEnabledMutatorPreservesConfiguredPath(t *testing.T) {
	c := Default()
	root := t.TempDir()
	if err := c.AddSkillPath(root); err != nil {
		t.Fatalf("add skill path: %v", err)
	}
	if err := c.SetSkillPathEnabled(root, false); err != nil {
		t.Fatalf("disable skill path: %v", err)
	}
	if len(c.Skills.Paths) != 1 || filepath.Clean(c.Skills.Paths[0]) != filepath.Clean(root) {
		t.Fatalf("paths after disable = %v, want %q preserved", c.Skills.Paths, root)
	}
	if len(c.Skills.ExcludedPaths) != 1 || CanonicalSkillPath(c.Skills.ExcludedPaths[0]) != CanonicalSkillPath(root) {
		t.Fatalf("excluded paths after disable = %v, want %q", c.Skills.ExcludedPaths, root)
	}
	if err := c.SetSkillPathEnabled(root, true); err != nil {
		t.Fatalf("enable skill path: %v", err)
	}
	if len(c.Skills.Paths) != 1 || len(c.Skills.ExcludedPaths) != 0 {
		t.Fatalf("state after enable = paths %v excluded %v", c.Skills.Paths, c.Skills.ExcludedPaths)
	}
}

func TestSkillEnabledMutator(t *testing.T) {
	c := Default()
	if err := c.SetSkillEnabled("review", false); err != nil {
		t.Fatalf("disable skill: %v", err)
	}
	if err := c.SetSkillEnabled("review", false); err != nil {
		t.Fatalf("disable duplicate skill: %v", err)
	}
	if len(c.Skills.DisabledSkills) != 1 || c.Skills.DisabledSkills[0] != "review" {
		t.Fatalf("disabled skills = %v, want [review]", c.Skills.DisabledSkills)
	}
	if !c.IsSkillDisabled("review") {
		t.Fatal("review should be disabled")
	}
	if err := c.SetSkillEnabled("review", true); err != nil {
		t.Fatalf("enable skill: %v", err)
	}
	if len(c.Skills.DisabledSkills) != 0 {
		t.Fatalf("disabled skills after enable = %v, want empty", c.Skills.DisabledSkills)
	}
	if err := c.SetSkillEnabled("bad name", false); err == nil {
		t.Fatal("invalid skill name should error")
	}
}

func TestSkillImplicitInvocationMutator(t *testing.T) {
	c := Default()
	if !c.ImplicitSkillInvocationEnabled() {
		t.Fatal("implicit skill invocation should be enabled by default")
	}
	c.SetSkillImplicitInvocation(false)
	if c.ImplicitSkillInvocationEnabled() || !c.Skills.DisableImplicitInvocation {
		t.Fatal("implicit skill invocation should be disabled")
	}
	c.SetSkillImplicitInvocation(true)
	if !c.ImplicitSkillInvocationEnabled() || c.Skills.DisableImplicitInvocation {
		t.Fatal("implicit skill invocation should be enabled")
	}
}

func TestPluginMutators(t *testing.T) {
	c := Default()

	if err := c.UpsertPlugin(PluginEntry{Name: "ex", Command: "reasonix-plugin-example"}); err != nil {
		t.Fatalf("add stdio: %v", err)
	}
	if err := c.UpsertPlugin(PluginEntry{Name: "stripe", Type: "http", URL: "https://mcp.stripe.com"}); err != nil {
		t.Fatalf("add http: %v", err)
	}
	if len(c.Plugins) != 2 {
		t.Fatalf("plugin count = %d, want 2", len(c.Plugins))
	}

	// Transport validation: stdio needs command, http needs url.
	if err := c.UpsertPlugin(PluginEntry{Name: "bad"}); err == nil {
		t.Error("stdio without command should error")
	}
	if err := c.UpsertPlugin(PluginEntry{Name: "bad", Type: "http"}); err == nil {
		t.Error("http without url should error")
	}
	if err := c.UpsertPlugin(PluginEntry{Name: "bad", Type: "carrier-pigeon", Command: "x"}); err == nil {
		t.Error("unknown transport should error")
	}
	if err := c.UpsertPlugin(PluginEntry{Name: "bad", Command: "x", CallTimeoutSeconds: -1}); err == nil {
		t.Error("negative call_timeout_seconds should error")
	}
	if err := c.UpsertPlugin(PluginEntry{Name: "bad", Command: "x", StartupTimeoutSeconds: -1}); err == nil {
		t.Error("negative startup_timeout_seconds should error")
	}
	if err := c.UpsertPlugin(PluginEntry{Name: "bad", Command: "x", ToolTimeoutSeconds: map[string]int{"generate": -1}}); err == nil {
		t.Error("negative tool_timeout_seconds should error")
	}
	if err := c.UpsertPlugin(PluginEntry{Name: "bad", Command: "x", ToolTimeoutSeconds: map[string]int{" ": 1}}); err == nil {
		t.Error("empty tool_timeout_seconds key should error")
	}
	// Replace in place.
	if err := c.UpsertPlugin(PluginEntry{Name: "ex", Command: "other-cmd"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if len(c.Plugins) != 2 {
		t.Errorf("replace grew plugins to %d", len(c.Plugins))
	}

	if !c.RemovePlugin("ex") {
		t.Error("remove should report true")
	}
	if c.RemovePlugin("ex") {
		t.Error("second remove should report false")
	}
}

func TestAutoStartPlugins(t *testing.T) {
	c := Default()
	off := false
	on := true
	c.Plugins = []PluginEntry{
		{Name: "implicit", Command: "implicit-bin"},
		{Name: "disabled", Command: "disabled-bin", AutoStart: &off},
		{Name: "enabled", Command: "enabled-bin", AutoStart: &on},
	}
	got := c.AutoStartPlugins()
	if len(got) != 2 || got[0].Name != "implicit" || got[1].Name != "enabled" {
		t.Fatalf("AutoStartPlugins = %+v, want implicit + enabled", got)
	}
}

func TestPluginResolvedTierDefaultsToBackground(t *testing.T) {
	for _, tc := range []struct {
		name string
		tier string
		want string
	}{
		{name: "empty", tier: "", want: "background"},
		{name: "legacy lazy", tier: "lazy", want: "background"},
		{name: "background", tier: "background", want: "background"},
		{name: "eager", tier: "eager", want: "eager"},
		{name: "unknown", tier: "startup", want: "background"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := (PluginEntry{Name: "mcp", Command: "mcp-server", Tier: tc.tier}).ResolvedTier()
			if got != tc.want {
				t.Fatalf("ResolvedTier(%q) = %q, want %q", tc.tier, got, tc.want)
			}
		})
	}
}

func TestClearPluginAuthentication(t *testing.T) {
	c := Default()
	c.Plugins = []PluginEntry{{
		Name: "dida",
		Type: "http",
		URL:  "https://mcp.dida365.com/mcp?access_token=abc&workspace=main",
		Headers: map[string]string{
			"Authorization": "Bearer ${DIDA_TOKEN}",
			"X-Org":         "team",
		},
		Env: map[string]string{
			"DIDA_TOKEN": "${DIDA_TOKEN}",
			"DEBUG":      "1",
		},
		Tier: "lazy",
	}}
	updated, changed, err := c.ClearPluginAuthentication("dida")
	if err != nil {
		t.Fatalf("ClearPluginAuthentication: %v", err)
	}
	if !changed {
		t.Fatal("ClearPluginAuthentication should report changed")
	}
	if updated.URL != "https://mcp.dida365.com/mcp?workspace=main" {
		t.Fatalf("url = %q", updated.URL)
	}
	if _, ok := updated.Headers["Authorization"]; ok {
		t.Fatalf("auth header should be removed: %v", updated.Headers)
	}
	if updated.Headers["X-Org"] != "team" {
		t.Fatalf("ordinary header should be preserved: %v", updated.Headers)
	}
	if _, ok := updated.Env["DIDA_TOKEN"]; ok {
		t.Fatalf("auth env should be removed: %v", updated.Env)
	}
	if updated.Env["DEBUG"] != "1" {
		t.Fatalf("ordinary env should be preserved: %v", updated.Env)
	}
}

// TestSaveToRoundTrips stages several mutations, persists atomically, and
// re-decodes the file to confirm the changes survived a write/read cycle.
func TestSaveToRoundTrips(t *testing.T) {
	c := Default()
	if err := c.SetDefaultModel("deepseek-pro"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPlannerModel("deepseek-pro"); err != nil {
		t.Fatal(err)
	}
	if err := c.UpsertProvider(ProviderEntry{Name: "local", Kind: "openai", BaseURL: "http://localhost:1234/v1", Model: "llama"}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPermissionMode("deny"); err != nil {
		t.Fatal(err)
	}
	if err := c.AddPermissionRule("allow", "Bash(go test:*)"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetNetwork(NetworkConfig{
		ProxyMode: "custom",
		Proxy: NetworkProxyConfig{
			Type:   "socks5",
			Server: "127.0.0.1",
			Port:   7890,
		},
	}); err != nil {
		t.Fatal(err)
	}
	autoStart := false
	if err := c.UpsertPlugin(PluginEntry{Name: "stripe", Type: "http", URL: "https://mcp.stripe.com", AutoStart: &autoStart}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "nested", "reasonix.toml")
	if err := c.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	var got Config
	if _, err := toml.DecodeFile(path, &got); err != nil {
		t.Fatalf("saved file does not parse: %v", err)
	}
	if got.DefaultModel != "deepseek-pro" {
		t.Errorf("default_model = %q", got.DefaultModel)
	}
	if got.Agent.PlannerModel != "deepseek-pro" {
		t.Errorf("planner_model = %q", got.Agent.PlannerModel)
	}
	if _, ok := got.Provider("local"); !ok {
		t.Error("added provider 'local' missing after round-trip")
	}
	if got.Permissions.Mode != "deny" {
		t.Errorf("mode = %q", got.Permissions.Mode)
	}
	if len(got.Permissions.Allow) != 1 || got.Permissions.Allow[0] != "Bash(go test:*)" {
		t.Errorf("allow list = %v", got.Permissions.Allow)
	}
	if got.Network.ProxyMode != "custom" || got.Network.Proxy.Server != "127.0.0.1" || got.Network.Proxy.Port != 7890 {
		t.Errorf("network = %+v", got.Network)
	}
	if len(got.Plugins) != 1 || got.Plugins[0].Name != "stripe" {
		t.Errorf("plugins = %+v", got.Plugins)
	}
	if got.Plugins[0].AutoStart == nil || *got.Plugins[0].AutoStart {
		t.Errorf("auto_start should round-trip false, got %+v", got.Plugins[0].AutoStart)
	}
}

func TestRecoveryReviewerSettingsRoundTripThroughUserSave(t *testing.T) {
	isolateUserConfigHome(t)
	c := Default()
	c.Agent.RecoveryModel = "deepseek-pro"
	c.Agent.RecoveryTemperature = 0.25

	path := UserConfigPath()
	if err := c.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	got := LoadForEdit(path)
	if got.Agent.RecoveryModel != "deepseek-pro" || got.Agent.RecoveryTemperature != 0 {
		t.Fatalf("agent recovery settings not preserved: %+v", got.Agent)
	}
}

func TestRetiredAutoGuardKeysAreIgnoredAndRemovedOnSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reasonix.toml")
	if err := os.WriteFile(path, []byte("[desktop]\ndefault_auto_recovery_checkpoint = false\n\n[agent]\nauto_recovery_checkpoint = \"off\"\nrecovery_model = \"deepseek-pro\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := LoadForEdit(path)
	if c.Agent.RecoveryModel != "deepseek-pro" {
		t.Fatalf("unrelated recovery model was not loaded: %+v", c.Agent)
	}
	if err := c.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "default_auto_recovery_checkpoint") || strings.Contains(text, "auto_recovery_checkpoint") {
		t.Fatalf("retired Auto Guard keys survived save:\n%s", text)
	}
	if !strings.Contains(text, `recovery_model = "deepseek-pro"`) {
		t.Fatalf("save removed unrelated recovery model:\n%s", text)
	}
}

func TestSaveToScopesUserAndProjectFiles(t *testing.T) {
	home := isolateUserConfigHome(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	c := Default()
	c.Desktop.Theme = "dark"
	c.Desktop.ThemeStyle = "graphite"
	c.Desktop.CloseBehavior = "background"

	userPath := UserConfigPath()
	requireTestPathWithin(t, home, userPath)
	if err := c.SaveTo(userPath); err != nil {
		t.Fatalf("SaveTo user config: %v", err)
	}
	userBody, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	if !strings.Contains(string(userBody), "[desktop]") {
		t.Fatalf("user config should include desktop preferences:\n%s", userBody)
	}
	if info, err := os.Stat(userPath); err != nil {
		t.Fatalf("stat user config: %v", err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("user config mode = %o, want 600", info.Mode().Perm())
	}

	projectPath := filepath.Join(t.TempDir(), "reasonix.toml")
	if err := c.SaveTo(projectPath); err != nil {
		t.Fatalf("SaveTo project config: %v", err)
	}
	projectBody, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if strings.Contains(string(projectBody), "[desktop]") ||
		strings.Contains(string(projectBody), "close_behavior") ||
		strings.Contains(string(projectBody), "default_tool_approval_mode") {
		t.Fatalf("project config should not include desktop preferences:\n%s", projectBody)
	}
	if info, err := os.Stat(projectPath); err != nil {
		t.Fatalf("stat project config: %v", err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o644 {
		t.Fatalf("project config mode = %o, want 644", info.Mode().Perm())
	}
}

func TestLoadForRootKeepsOfficialProviderAliasesDistinct(t *testing.T) {
	isolateUserConfigHome(t)
	root := t.TempDir()
	userPath := UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte(`
config_version = 2
default_model = "deepseek/deepseek-v4-flash"

[desktop]
provider_access = ["deepseek"]

[[providers]]
name = "deepseek"
kind = "openai"
base_url = "https://api.deepseek.com"
models = ["deepseek-v4-flash", "deepseek-v4-pro"]
default = "deepseek-v4-flash"
api_key_env = "USER_DEEPSEEK_KEY"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "reasonix.toml"), []byte(`
[[providers]]
name = "deepseek-flash"
kind = "openai"
base_url = "https://api.deepseek.com"
model = "deepseek-v4-flash"
api_key_env = "PROJECT_DEEPSEEK_KEY"
effort = "max"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForRoot(root)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	userProvider, ok := cfg.Provider("deepseek")
	if !ok {
		t.Fatalf("user deepseek provider missing: %+v", cfg.Providers)
	}
	if userProvider.APIKeyEnv != "USER_DEEPSEEK_KEY" {
		t.Fatalf("deepseek provider = %+v, want user provider preserved", userProvider)
	}
	projectProvider, ok := cfg.Provider("deepseek-flash")
	if !ok {
		t.Fatalf("project deepseek-flash provider missing: %+v", cfg.Providers)
	}
	if projectProvider.APIKeyEnv != "PROJECT_DEEPSEEK_KEY" || projectProvider.Effort != "max" {
		t.Fatalf("deepseek-flash provider = %+v, want project provider preserved", projectProvider)
	}
}

func TestLoadForRootKeepsUserProviderOverSameNamedProjectProvider(t *testing.T) {
	isolateUserConfigHome(t)
	root := t.TempDir()
	userPath := UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte(`
[[providers]]
name = "shared"
kind = "openai"
base_url = "https://global.example/v1"
model = "global-model"
api_key_env = "GLOBAL_SHARED_KEY"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "reasonix.toml"), []byte(`
[[providers]]
name = "shared"
kind = "openai"
base_url = "https://project.example/v1"
model = "project-model"
api_key_env = "PROJECT_SHARED_KEY"

[[providers]]
name = "project-only"
kind = "openai"
base_url = "https://project.example/v1"
model = "project-only-model"
api_key_env = "PROJECT_ONLY_KEY"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForRoot(root)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	shared, ok := cfg.Provider("shared")
	if !ok {
		t.Fatalf("shared provider missing: %+v", cfg.Providers)
	}
	if shared.BaseURL != "https://global.example/v1" || shared.APIKeyEnv != "GLOBAL_SHARED_KEY" || shared.Model != "global-model" {
		t.Fatalf("shared provider = %+v, want global provider to win over project provider", shared)
	}
	if _, ok := cfg.Provider("project-only"); !ok {
		t.Fatalf("project-only provider missing: %+v", cfg.Providers)
	}
}

func TestMigrateDeprecatedAgentStepLimitsForRootRunsOnce(t *testing.T) {
	isolateUserConfigHome(t)
	root := t.TempDir()
	userPath := UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte(`
[agent]
max_steps = 17
planner_max_steps = 9
temperature = 0.4

[bot]
max_steps = 21
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "reasonix.toml"), []byte(`
default_model = "deepseek-pro"

[agent]
max_steps = 3
planner_max_steps = 4
temperature = 0.8
`), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := MigrateLegacyAgentStepLimitsForRoot(root)
	if err != nil {
		t.Fatalf("MigrateLegacyAgentStepLimitsForRoot: %v", err)
	}
	if !changed {
		t.Fatal("first migration should remove deprecated step-limit keys")
	}

	cfg, err := LoadForRoot(root)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if cfg.Agent.MaxSteps != 0 || cfg.Agent.PlannerMaxSteps != 0 {
		t.Fatalf("deprecated agent steps = max:%d planner:%d, want automatic 0/0", cfg.Agent.MaxSteps, cfg.Agent.PlannerMaxSteps)
	}
	if cfg.IgnoredLegacyAgentStepLimits() {
		t.Fatal("migrated config should no longer report legacy step limits")
	}
	if cfg.Agent.Temperature != 0.8 {
		t.Fatalf("agent temperature = %v, want project override to keep working for other agent settings", cfg.Agent.Temperature)
	}
	if cfg.DefaultModel != "deepseek-pro" {
		t.Fatalf("default_model = %q, want project config to keep overriding unrelated fields", cfg.DefaultModel)
	}
	if cfg.Bot.MaxSteps != 21 {
		t.Fatalf("bot.max_steps = %d, want independent bot limit preserved", cfg.Bot.MaxSteps)
	}
	for _, path := range []string{userPath, filepath.Join(root, "reasonix.toml")} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, changed := stripLegacyAgentStepLimitLines(string(raw)); changed {
			t.Fatalf("runtime migration left deprecated [agent] step limits in %s:\n%s", path, raw)
		}
	}
	userRaw, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(userRaw), "[bot]\nmax_steps = 21") {
		t.Fatalf("migration removed independent bot.max_steps:\n%s", userRaw)
	}

	again, err := MigrateLegacyAgentStepLimitsForRoot(root)
	if err != nil {
		t.Fatalf("second migration: %v", err)
	}
	if again {
		t.Fatal("migration notice should be one-shot after deprecated keys are removed")
	}
}

func TestMigrateLegacyRedactToolOutputForRoot(t *testing.T) {
	isolateUserConfigHome(t)
	root := t.TempDir()
	userPath := UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte(`[secrets]
redact_tool_output = true
filter_subprocess_env = true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(root, "reasonix.toml")
	if err := os.WriteFile(projectPath, []byte(`[secrets]
redact_tool_output = false
protect_sensitive_files = true
`), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := MigrateLegacyRedactToolOutputForRoot(root)
	if err != nil {
		t.Fatalf("MigrateLegacyRedactToolOutputForRoot: %v", err)
	}
	if !changed {
		t.Fatal("first migration should remove deprecated redact_tool_output keys")
	}
	for _, path := range []string{userPath, projectPath} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "redact_tool_output") {
			t.Fatalf("deprecated redact_tool_output remains in %s:\n%s", path, raw)
		}
	}
	userRaw, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(userRaw), "filter_subprocess_env = true") {
		t.Fatalf("migration removed an active secrets setting:\n%s", userRaw)
	}
	projectRaw, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(projectRaw), "protect_sensitive_files = true") {
		t.Fatalf("migration removed an unrelated project setting:\n%s", projectRaw)
	}

	again, err := MigrateLegacyRedactToolOutputForRoot(root)
	if err != nil {
		t.Fatalf("second migration: %v", err)
	}
	if again {
		t.Fatal("migration should be a no-op after deprecated keys are removed")
	}
}

func TestMigrateLegacyMemoryCompilerForRoot(t *testing.T) {
	isolateUserConfigHome(t)
	root := t.TempDir()
	userPath := UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte(`[agent]
memory_compiler = { enabled = true, verbosity = "compact" }
temperature = 0.4
`), 0o644); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(root, "reasonix.toml")
	if err := os.WriteFile(projectPath, []byte(`[agent]
memory_compiler = { enabled = false }
reasoning_language = "zh"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := MigrateLegacyMemoryCompilerForRoot(root)
	if err != nil {
		t.Fatalf("MigrateLegacyMemoryCompilerForRoot: %v", err)
	}
	if !changed {
		t.Fatal("first migration should remove deprecated memory_compiler keys")
	}
	for _, path := range []string{userPath, projectPath} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "memory_compiler") {
			t.Fatalf("deprecated memory_compiler remains in %s:\n%s", path, raw)
		}
	}
	userRaw, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(userRaw), "temperature = 0.4") {
		t.Fatalf("migration removed an active agent setting:\n%s", userRaw)
	}
	projectRaw, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(projectRaw), `reasoning_language = "zh"`) {
		t.Fatalf("migration removed an unrelated project setting:\n%s", projectRaw)
	}

	again, err := MigrateLegacyMemoryCompilerForRoot(root)
	if err != nil {
		t.Fatalf("second migration: %v", err)
	}
	if again {
		t.Fatal("migration should be a no-op after deprecated keys are removed")
	}
}

func TestRetiredConfigMigrationRequiresConfigFileLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	const original = "[agent]\nmemory_compiler = \"compact\"\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := acquireConfigFileEditLockWithTimeout(path, time.Second)
	if err != nil {
		t.Fatalf("hold config file lock: %v", err)
	}
	defer release()

	previousTimeout := configEditLockTimeout
	configEditLockTimeout = 30 * time.Millisecond
	t.Cleanup(func() { configEditLockTimeout = previousTimeout })

	changed, err := migrateLegacyMemoryCompilerFile(path)
	if err == nil || changed {
		t.Fatalf("migration while file lock held = (%v, %v), want unchanged lock error", changed, err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Fatalf("blocked migration changed config:\n%s", got)
	}
}

func TestLegacyMCPTierMigrationRequiresConfigFileLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	const original = "[[plugins]]\nname = \"playwright\"\ntier = \"lazy\"\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := acquireConfigFileEditLockWithTimeout(path, time.Second)
	if err != nil {
		t.Fatalf("hold config file lock: %v", err)
	}
	defer release()

	previousTimeout := configEditLockTimeout
	configEditLockTimeout = 30 * time.Millisecond
	t.Cleanup(func() { configEditLockTimeout = previousTimeout })

	err = migrateLegacyMCPTiersFile(path)
	if err == nil {
		t.Fatal("migration succeeded while another process-equivalent config transaction held the file lock")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Fatalf("blocked migration changed config:\n%s", got)
	}
}

// TestMigrateLegacyMemoryCompilerKeepsMultilineSystemPrompt reproduces the
// review finding: a multiline system_prompt quoting a `memory_compiler = ...`
// example line must survive the retired-key migration byte-for-byte.
func TestMigrateLegacyMemoryCompilerKeepsMultilineSystemPrompt(t *testing.T) {
	isolateUserConfigHome(t)
	root := t.TempDir()
	userPath := UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `[agent]
system_prompt = """
You are Reasonix. Historical config example:
memory_compiler = { enabled = true, verbosity = "compact" }
Keep answers short.
"""
temperature = 0.2
`
	if err := os.WriteFile(userPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := MigrateLegacyMemoryCompilerForRoot(root)
	if err != nil {
		t.Fatalf("MigrateLegacyMemoryCompilerForRoot: %v", err)
	}
	if changed {
		t.Fatal("migration must not rewrite a config whose only memory_compiler text lives inside a multiline string")
	}
	raw, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Fatalf("multiline system_prompt was modified:\n--- got ---\n%s\n--- want ---\n%s", raw, original)
	}
}

// TestStripTOMLKeyLinesPreservesMultilineStrings pins the shared stripper used
// by every retired-config-key migration: lines inside TOML multiline strings
// are never treated as section headers or key assignments, while real retired
// keys outside strings are still removed.
func TestStripTOMLKeyLinesPreservesMultilineStrings(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		section     string
		keys        []string
		wantChanged bool
		wantSame    bool   // raw must round-trip unchanged
		wantKept    string // substring that must survive
		wantGone    string // substring that must be removed
	}{
		{
			name:    "multiline basic string keeps quoted example",
			raw:     "[agent]\nsystem_prompt = \"\"\"\nmemory_compiler = { enabled = true }\n\"\"\"\n",
			section: "agent", keys: []string{"memory_compiler"},
			wantChanged: false, wantSame: true,
		},
		{
			name:    "multiline literal string keeps quoted example",
			raw:     "[agent]\nsystem_prompt = '''\nmemory_compiler = { enabled = true }\n'''\n",
			section: "agent", keys: []string{"memory_compiler"},
			wantChanged: false, wantSame: true,
		},
		{
			name:    "section header inside multiline string does not switch sections",
			raw:     "[agent]\nsystem_prompt = \"\"\"\n[secrets]\nredact_tool_output = true\n\"\"\"\n",
			section: "secrets", keys: []string{"redact_tool_output"},
			wantChanged: false, wantSame: true,
		},
		{
			name:    "real key next to a multiline string is still removed",
			raw:     "[agent]\nsystem_prompt = \"\"\"\nmemory_compiler = { enabled = true }\n\"\"\"\nmemory_compiler = { enabled = true, verbosity = \"compact\" }\n",
			section: "agent", keys: []string{"memory_compiler"},
			wantChanged: true,
			wantKept:    "system_prompt = \"\"\"\nmemory_compiler = { enabled = true }\n\"\"\"",
			wantGone:    "verbosity",
		},
		{
			name:    "single-line triple-quoted value does not open a multiline state",
			raw:     "[agent]\nsystem_prompt = \"\"\"one line\"\"\"\nmax_steps = 40\n",
			section: "agent", keys: []string{"max_steps", "planner_max_steps"},
			wantChanged: true,
			wantKept:    "system_prompt = \"\"\"one line\"\"\"",
			wantGone:    "max_steps",
		},
		{
			name:    "comment containing triple quotes does not open a multiline state",
			raw:     "[plugins]\n# docs say \"\"\" starts a multiline string\ntier = 2\n",
			section: "plugins", keys: []string{"tier"},
			wantChanged: true,
			wantKept:    "# docs say \"\"\" starts a multiline string",
			wantGone:    "tier = 2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := stripTOMLKeyLines(tc.raw, tc.section, tc.keys...)
			if changed != tc.wantChanged {
				t.Fatalf("changed = %v, want %v\n--- got ---\n%s", changed, tc.wantChanged, got)
			}
			if tc.wantSame && got != tc.raw {
				t.Fatalf("content was modified:\n--- got ---\n%s\n--- want ---\n%s", got, tc.raw)
			}
			if tc.wantKept != "" && !strings.Contains(got, tc.wantKept) {
				t.Fatalf("expected content was removed:\n--- got ---\n%s\n--- want kept ---\n%s", got, tc.wantKept)
			}
			if tc.wantGone != "" && strings.Contains(got, tc.wantGone) {
				t.Fatalf("retired key survived:\n--- got ---\n%s\n--- want gone ---\n%s", got, tc.wantGone)
			}
		})
	}
}

func TestLoadForRootReadOnlyIgnoresDeprecatedAgentStepLimitsWithoutRewriting(t *testing.T) {
	isolateUserConfigHome(t)
	root := t.TempDir()
	path := filepath.Join(root, "reasonix.toml")
	original := []byte(`
[agent]
max_steps = 3
planner_max_steps = 4
`)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForRootReadOnly(root)
	if err != nil {
		t.Fatalf("LoadForRootReadOnly: %v", err)
	}
	if cfg.Agent.MaxSteps != 0 || cfg.Agent.PlannerMaxSteps != 0 {
		t.Fatalf("deprecated steps = max:%d planner:%d, want automatic 0/0", cfg.Agent.MaxSteps, cfg.Agent.PlannerMaxSteps)
	}
	if !cfg.IgnoredLegacyAgentStepLimits() {
		t.Fatal("read-only load should report ignored deprecated step limits")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, original) {
		t.Fatalf("read-only load rewrote config:\n%s", raw)
	}
}

func TestSaveForRootPreservesShadowedProjectProvider(t *testing.T) {
	isolateUserConfigHome(t)
	root := t.TempDir()
	userPath := UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte(`
[[providers]]
name = "shared"
kind = "openai"
base_url = "https://global.example/v1"
model = "global-model"
api_key_env = "GLOBAL_SHARED_KEY"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(root, "reasonix.toml")
	if err := os.WriteFile(projectPath, []byte(`
[[providers]]
name = "shared"
kind = "openai"
base_url = "https://project.example/v1"
model = "project-model"
api_key_env = "PROJECT_SHARED_KEY"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForRoot(root)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if err := cfg.SaveForRoot(root); err != nil {
		t.Fatalf("SaveForRoot: %v", err)
	}
	var saved Config
	if _, err := toml.DecodeFile(projectPath, &saved); err != nil {
		t.Fatalf("saved project config does not parse: %v", err)
	}
	shared, ok := saved.Provider("shared")
	if !ok {
		t.Fatalf("saved project provider missing: %+v", saved.Providers)
	}
	if shared.BaseURL != "https://project.example/v1" || shared.APIKeyEnv != "PROJECT_SHARED_KEY" {
		t.Fatalf("saved provider = %+v, want original project provider", shared)
	}
}

func TestSaveForRootDoesNotWriteUserProvidersIntoProjectConfig(t *testing.T) {
	isolateUserConfigHome(t)
	root := t.TempDir()
	userPath := UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte(`
config_version = 2

[[providers]]
name = "global"
kind = "openai"
base_url = "https://global.example/v1"
model = "global-model"
api_key_env = "GLOBAL_KEY"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(root, "reasonix.toml")
	if err := os.WriteFile(projectPath, []byte(`
config_version = 2
default_model = "project-local/project-model"

[[providers]]
name = "project-local"
kind = "openai"
base_url = "https://project.example/v1"
model = "project-model"
api_key_env = "PROJECT_KEY"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadForRoot(root)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if _, ok := cfg.Provider("global"); !ok {
		t.Fatal("runtime config should include user provider before saving")
	}
	if _, ok := cfg.Provider("project-local"); !ok {
		t.Fatal("runtime config should include project provider before saving")
	}
	if err := cfg.SaveForRoot(root); err != nil {
		t.Fatalf("SaveForRoot: %v", err)
	}

	var got Config
	if _, err := toml.DecodeFile(projectPath, &got); err != nil {
		t.Fatalf("saved project config does not parse: %v", err)
	}
	if _, ok := got.Provider("global"); ok {
		t.Fatalf("user provider leaked into project config: %+v", got.Providers)
	}
	if _, ok := got.Provider("project-local"); !ok {
		t.Fatalf("project provider missing after save: %+v", got.Providers)
	}
}

func TestSaveToExistingProjectPersistsTopLevelDelta(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "reasonix.toml")
	if err := os.WriteFile(projectPath, []byte("[permissions]\nallow = [\"Bash(go test:*)\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.ConfigVersion = 2
	if err := cfg.SetDefaultModel("deepseek-pro"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveTo(projectPath); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	body, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if !strings.Contains(string(body), `default_model = "deepseek-pro"`) {
		t.Fatalf("project config dropped top-level default_model delta:\n%s", body)
	}
	if !strings.Contains(string(body), "config_version = 2") {
		t.Fatalf("project config dropped top-level config_version delta:\n%s", body)
	}
	var got Config
	if _, err := toml.DecodeFile(projectPath, &got); err != nil {
		t.Fatalf("saved project config does not parse: %v", err)
	}
	if got.DefaultModel != "deepseek-pro" {
		t.Fatalf("default_model = %q, want deepseek-pro", got.DefaultModel)
	}
	if got.ConfigVersion != 2 {
		t.Fatalf("config_version = %d, want 2", got.ConfigVersion)
	}
}

func TestSaveToExistingProjectRemovesResetSkillOverrides(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		set   func(*Config)
		reset func(*Config)
	}{
		{name: "paths", key: "paths", set: func(c *Config) { c.Skills.Paths = []string{"project-skills"} }, reset: func(c *Config) { c.Skills.Paths = nil }},
		{name: "excluded paths", key: "excluded_paths", set: func(c *Config) { c.Skills.ExcludedPaths = []string{"project-skills"} }, reset: func(c *Config) { c.Skills.ExcludedPaths = nil }},
		{name: "disabled skills", key: "disabled_skills", set: func(c *Config) { c.Skills.DisabledSkills = []string{"review"} }, reset: func(c *Config) { c.Skills.DisabledSkills = nil }},
		{name: "implicit invocation", key: "disable_implicit_invocation", set: func(c *Config) { c.Skills.DisableImplicitInvocation = true }, reset: func(c *Config) { c.Skills.DisableImplicitInvocation = false }},
		{name: "max depth", key: "max_depth", set: func(c *Config) { c.Skills.MaxDepth = 2 }, reset: func(c *Config) { c.Skills.MaxDepth = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectPath := filepath.Join(t.TempDir(), "reasonix.toml")
			cfg := Default()
			tt.set(cfg)
			if err := cfg.SaveTo(projectPath); err != nil {
				t.Fatalf("initial SaveTo: %v", err)
			}
			loaded, err := LoadForEditReadOnlyStrict(projectPath)
			if err != nil {
				t.Fatalf("load project config: %v", err)
			}
			tt.reset(loaded)
			if err := loaded.SaveTo(projectPath); err != nil {
				t.Fatalf("reset SaveTo: %v", err)
			}
			body, err := os.ReadFile(projectPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), tt.key+" =") {
				t.Fatalf("reset left stale %s override:\n%s", tt.key, body)
			}
			fresh, err := LoadForEditReadOnlyStrict(projectPath)
			if err != nil {
				t.Fatalf("reload reset project config: %v", err)
			}
			if fresh.Skills.Paths != nil || fresh.Skills.ExcludedPaths != nil || fresh.Skills.DisabledSkills != nil || fresh.Skills.DisableImplicitInvocation || fresh.Skills.MaxDepth != 0 {
				t.Fatalf("reloaded skills retained reset override: %+v", fresh.Skills)
			}
		})
	}
}

func TestSaveToExistingProjectPreservesExplicitSkillDefaults(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "reasonix.toml")
	if err := os.WriteFile(projectPath, []byte("[skills]\npaths = [\"project-skills\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadForEditReadOnlyStrict(projectPath)
	if err != nil {
		t.Fatalf("load project config: %v", err)
	}
	cfg.Skills.Paths = nil
	cfg.Skills.ExcludedPaths = nil
	cfg.Skills.DisabledSkills = nil
	cfg.Skills.DisableImplicitInvocation = false
	cfg.Skills.MaxDepth = 0
	for _, key := range projectSkillKeys {
		if err := cfg.KeepProjectSkillKey(key); err != nil {
			t.Fatalf("keep %s: %v", key, err)
		}
	}
	if err := cfg.SaveTo(projectPath); err != nil {
		t.Fatalf("save explicit project defaults: %v", err)
	}
	body, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"paths = []",
		"excluded_paths = []",
		"disabled_skills = []",
		"disable_implicit_invocation = false",
		"max_depth = 0",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("explicit project default %q missing from:\n%s", want, text)
		}
	}
}

func TestUnrelatedProjectSavePreservesExplicitDefaultSkillOverride(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "reasonix.toml")
	if err := os.WriteFile(projectPath, []byte("[skills]\ndisable_implicit_invocation = false\n\n[permissions]\nmode = \"ask\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadForEditReadOnlyStrict(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetDefaultModel("deepseek-pro"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveTo(projectPath); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "disable_implicit_invocation = false") {
		t.Fatalf("explicit default override was removed:\n%s", body)
	}
}

func TestExplicitProjectSkillDefaultOverridesUserConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	project := t.TempDir()
	user := Default()
	user.Skills.DisableImplicitInvocation = true
	if err := user.SaveTo(UserConfigPath()); err != nil {
		t.Fatalf("save user config: %v", err)
	}
	projectPath := filepath.Join(project, "reasonix.toml")
	if err := os.WriteFile(projectPath, []byte("[skills]\ndisable_implicit_invocation = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadForEditReadOnlyStrict(projectPath)
	if err != nil {
		t.Fatalf("load project config: %v", err)
	}
	cfg.SetSkillImplicitInvocation(true)
	if err := cfg.KeepProjectSkillKey("disable_implicit_invocation"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveTo(projectPath); err != nil {
		t.Fatalf("save project override: %v", err)
	}
	effective, err := LoadForRootReadOnly(project)
	if err != nil {
		t.Fatalf("load effective config: %v", err)
	}
	if !effective.ImplicitSkillInvocationEnabled() {
		t.Fatalf("project explicit false did not override user config: %+v", effective.Skills)
	}
}

func TestSaveToExistingProjectRemovesMultilineSkillArray(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "reasonix.toml")
	original := "[skills]\npaths = [\n  \"project-skills\",\n  \"shared-skills\",\n]\n\n[permissions]\nmode = \"ask\"\n"
	if err := os.WriteFile(projectPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadForEditReadOnlyStrict(projectPath)
	if err != nil {
		t.Fatalf("load project config: %v", err)
	}
	cfg.Skills.Paths = nil
	if err := cfg.SaveTo(projectPath); err != nil {
		t.Fatalf("reset multiline paths: %v", err)
	}
	body, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "project-skills") || strings.Contains(string(body), "shared-skills") {
		t.Fatalf("multiline skill array was only partially removed:\n%s", body)
	}
	if err := ValidateFile(projectPath); err != nil {
		t.Fatalf("reset project config is invalid TOML: %v\n%s", err, body)
	}
}

func TestSaveToExistingProjectPersistsProviderAccessWithoutReplacingDesktopSection(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "reasonix.toml")
	if err := os.WriteFile(projectPath, []byte("[desktop]\nlegacy_preference = \"keep\"\n\n[permissions]\nallow = [\"Bash(go test:*)\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := LoadForEditWithoutCredentials(projectPath)
	cfg.Desktop.ProviderAccess = []string{"project-relay"}
	if err := cfg.SaveTo(projectPath); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	body, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{`provider_access = ["project-relay"]`, `legacy_preference = "keep"`, `[permissions]`} {
		if !strings.Contains(text, want) {
			t.Fatalf("existing project config missing %q after provider access update:\n%s", want, text)
		}
	}
	cfg.Desktop.ProviderAccess = []string{}
	if err := cfg.SaveTo(projectPath); err != nil {
		t.Fatalf("SaveTo explicit empty access: %v", err)
	}
	body, err = os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "provider_access = []") {
		t.Fatalf("explicit empty project provider access was not persisted:\n%s", body)
	}
}

func TestWritePermissionsAllowUpdatesOnlyAllow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reasonix.toml")
	original := `[permissions]
# Keep the policy rationale.
mode = "deny"
allow = [
  # Keep the list rationale.
  "Bash(existing)", # Keep the existing rule rationale.
] # Keep the allow rationale.
ask = ["Edit(*.env)"]
deny = ["Bash(rm:*)"]
future_policy = "keep"

[desktop]
legacy_preference = "keep"
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WritePermissionsAllow(path, []string{"Bash(existing)", "Edit(src/app.go)"}); err != nil {
		t.Fatal(err)
	}

	got, err := LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatalf("updated config does not parse: %v", err)
	}
	if !reflect.DeepEqual(got.Permissions.Allow, []string{"Bash(existing)", "Edit(src/app.go)"}) {
		t.Fatalf("permissions.allow = %v", got.Permissions.Allow)
	}
	if got.Permissions.Mode != "deny" || !reflect.DeepEqual(got.Permissions.Ask, []string{"Edit(*.env)"}) || !reflect.DeepEqual(got.Permissions.Deny, []string{"Bash(rm:*)"}) {
		t.Fatalf("permission policy changed: %+v", got.Permissions)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		"# Keep the policy rationale.",
		"# Keep the list rationale.",
		"# Keep the existing rule rationale.",
		"# Keep the allow rationale.",
		`future_policy = "keep"`,
		"[desktop]\nlegacy_preference = \"keep\"",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("updated config missing %q:\n%s", want, body)
		}
	}
}

func TestWritePermissionsAllowIgnoresSectionExamplesInMultilineStrings(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "multiline basic string with five-quote close before existing section",
			body: `[agent]
system_prompt = """
Example only:
A "quoted" explanation and an escaped \" marker.
[permissions]
allow = ["Bash(example)"]
Ends with two quotes."""""

[permissions]
mode = "ask"
allow = ["Bash(existing)"]
deny = ["Bash(rm:*)"]
`,
		},
		{
			name: "multiline literal string with four-quote close without existing section",
			body: `[agent]
system_prompt = '''
Example only:
A 'quoted' explanation.
[permissions]
allow = ["Bash(example)"]
Ends with one quote.''''
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "reasonix.toml")
			if err := os.WriteFile(path, []byte(tt.body), 0o644); err != nil {
				t.Fatal(err)
			}

			wantAllow := []string{"Bash(existing)", "Edit(src/app.go)"}
			if !strings.Contains(tt.body, `Bash(existing)`) {
				wantAllow = []string{"Edit(src/app.go)"}
			}
			if err := WritePermissionsAllow(path, wantAllow); err != nil {
				t.Fatal(err)
			}

			got, err := LoadForEditReadOnlyStrict(path)
			if err != nil {
				t.Fatalf("updated config does not parse: %v", err)
			}
			if !reflect.DeepEqual(got.Permissions.Allow, wantAllow) {
				t.Fatalf("permissions.allow = %v, want %v", got.Permissions.Allow, wantAllow)
			}
			if !strings.Contains(got.Agent.SystemPrompt, "[permissions]\nallow = [\"Bash(example)\"]") {
				t.Fatalf("system prompt example changed: %q", got.Agent.SystemPrompt)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "[permissions]\nallow = [\"Bash(example)\"]") {
				t.Fatalf("multiline string content changed:\n%s", raw)
			}
		})
	}
}

func TestWritePermissionsAllowReplacesArrayContainingMultilineString(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reasonix.toml")
	original := `[permissions]
allow = [
  """Bash(example]
[desktop]
)""",
  "Bash(existing)",
]
deny = ["Bash(rm:*)"]

[desktop]
legacy_preference = "keep"
`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	wantAllow := []string{"Bash(existing)", "Edit(src/app.go)"}
	if err := WritePermissionsAllow(path, wantAllow); err != nil {
		t.Fatal(err)
	}
	got, err := LoadForEditReadOnlyStrict(path)
	if err != nil {
		t.Fatalf("updated config does not parse: %v", err)
	}
	if !reflect.DeepEqual(got.Permissions.Allow, wantAllow) {
		t.Fatalf("permissions.allow = %v, want %v", got.Permissions.Allow, wantAllow)
	}
	if !reflect.DeepEqual(got.Permissions.Deny, []string{"Bash(rm:*)"}) {
		t.Fatalf("permissions.deny = %v", got.Permissions.Deny)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "[desktop]\nlegacy_preference = \"keep\"") {
		t.Fatalf("unrelated section changed:\n%s", raw)
	}
}

func TestProviderEntriesConfigEqualIgnoresRuntimeState(t *testing.T) {
	a := ProviderEntry{Name: "relay", Kind: "openai", BaseURL: "https://relay.example/v1", Model: "m", APIKeyEnv: "RELAY_API_KEY"}
	b := a
	a.resolvedAPIKey = "old-secret"
	a.resolvedSource = CredentialSource{Kind: CredentialSourceCredentials, Label: "old"}
	a.persistedOfficialCurrency = "USD"
	b.resolvedAPIKey = "new-secret"
	b.resolvedSource = CredentialSource{Kind: CredentialSourceEnvironment, Label: "new"}
	if !ProviderEntriesConfigEqual(a, b) {
		t.Fatal("runtime-only provider state caused a persisted provider conflict")
	}
	b.Headers = map[string]string{"X-External": "changed"}
	if ProviderEntriesConfigEqual(a, b) {
		t.Fatal("persisted provider field change was ignored")
	}
	snapshot := ProviderEntryConfigSnapshot(a)
	if snapshot.resolvedAPIKey != "" || snapshot.resolvedSource != (CredentialSource{}) || snapshot.persistedOfficialCurrency != "" {
		t.Fatal("provider config snapshot retained runtime state")
	}
	cfg := &Config{Providers: []ProviderEntry{a}}
	updated := a
	updated.resolvedAPIKey = ""
	updated.resolvedSource = CredentialSource{}
	updated.Headers = map[string]string{"X-Replayed": "yes"}
	if err := cfg.UpsertProviderPreservingRuntime(updated); err != nil {
		t.Fatal(err)
	}
	got, _ := cfg.Provider("relay")
	if got.APIKey() != "old-secret" || got.Headers["X-Replayed"] != "yes" || got.persistedOfficialCurrency != "USD" {
		t.Fatalf("runtime-preserving upsert = %+v", got)
	}
	updated.APIKeyEnv = "NEW_RELAY_API_KEY"
	if err := cfg.UpsertProviderPreservingRuntime(updated); err != nil {
		t.Fatal(err)
	}
	got, _ = cfg.Provider("relay")
	if got.resolvedAPIKey != "" || got.resolvedSource != (CredentialSource{}) {
		t.Fatal("runtime credential survived an api_key_env change")
	}
	if got.persistedOfficialCurrency != "USD" {
		t.Fatal("pricing provenance was lost after an api_key_env change")
	}
}

func TestSaveToExistingProjectRemovesPluginDelta(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "reasonix.toml")
	cfg := Default()
	if err := cfg.UpsertPlugin(PluginEntry{Name: "ed", Type: "http", URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer token"}}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveTo(projectPath); err != nil {
		t.Fatalf("initial SaveTo: %v", err)
	}
	if !cfg.RemovePlugin("ed") {
		t.Fatal("RemovePlugin should report changed")
	}
	if err := cfg.SaveTo(projectPath); err != nil {
		t.Fatalf("SaveTo after remove: %v", err)
	}
	body, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if strings.Contains(string(body), "[[plugins]]") || strings.Contains(string(body), "[plugins.headers]") || strings.Contains(string(body), "Authorization") {
		t.Fatalf("removed plugin should not remain in project config:\n%s", body)
	}
	var got Config
	if _, err := toml.DecodeFile(projectPath, &got); err != nil {
		t.Fatalf("saved project config does not parse: %v", err)
	}
	if len(got.Plugins) != 0 {
		t.Fatalf("plugins = %+v, want none", got.Plugins)
	}
}

func TestSaveToNewProjectKeepsPluginSourcesSeparate(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "reasonix.toml")
	cfg := Default()
	cfg.Plugins = []PluginEntry{
		{Name: "unknown", Command: "unknown-mcp"},
		{Name: "user", Command: "user-mcp", Source: MCPSourceUserConfig},
		{Name: "project", Command: "project-mcp", Source: MCPSourceProjectConfig},
		{Name: "mcp-json", Command: "json-mcp", Source: MCPSourceProjectMCPJSON},
		{Name: "legacy", Command: "legacy-mcp", Source: MCPSourceLegacyUser},
		{Name: "package", Command: "package-mcp", Source: MCPSourcePluginPackage},
	}
	if err := cfg.SaveTo(projectPath); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	body, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, name := range []string{"unknown", "project"} {
		if !strings.Contains(text, `name    = "`+name+`"`) {
			t.Fatalf("new project config missing plugin %q:\n%s", name, text)
		}
	}
	for _, name := range []string{"user", "mcp-json", "legacy", "package"} {
		if strings.Contains(text, `name    = "`+name+`"`) {
			t.Fatalf("new project config leaked plugin %q:\n%s", name, text)
		}
	}
}

func TestSaveToExistingProjectKeepsPluginSourcesSeparate(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "reasonix.toml")
	if err := os.WriteFile(projectPath, []byte("# keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.Plugins = []PluginEntry{
		{Name: "user", Command: "user-mcp", Source: MCPSourceUserConfig},
		{Name: "project", Command: "project-mcp", Source: MCPSourceProjectConfig},
		{Name: "mcp-json", Command: "json-mcp", Source: MCPSourceProjectMCPJSON},
	}
	if err := cfg.SaveTo(projectPath); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	body, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `name    = "project"`) || strings.Contains(text, `name    = "user"`) || strings.Contains(text, `name    = "mcp-json"`) {
		t.Fatalf("existing project config crossed plugin source boundaries:\n%s", text)
	}
}

func TestSaveToExistingProjectRemovesPluginDeltaWithOnlyForeignSources(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), "reasonix.toml")
	if err := os.WriteFile(projectPath, []byte("[[plugins]]\nname = \"old\"\ncommand = \"old-mcp\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.Plugins = []PluginEntry{
		{Name: "user", Command: "user-mcp", Source: MCPSourceUserConfig},
		{Name: "mcp-json", Command: "json-mcp", Source: MCPSourceProjectMCPJSON},
		{Name: "legacy", Command: "legacy-mcp", Source: MCPSourceLegacyUser},
		{Name: "package", Command: "package-mcp", Source: MCPSourcePluginPackage},
	}
	if err := cfg.SaveTo(projectPath); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	body, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "[[plugins]]") {
		t.Fatalf("project plugin block remained after its last owned entry was removed:\n%s", body)
	}
}

func TestSaveToExistingProjectRemovesIneffectiveWindowsBashEnforce(t *testing.T) {
	setRuntimeGOOS(t, "windows")
	projectPath := filepath.Join(t.TempDir(), "reasonix.toml")
	if err := os.WriteFile(projectPath, []byte("[sandbox]\nbash = \"enforce\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Default()
	cfg.Sandbox.Bash = "enforce"
	if err := cfg.SaveTo(projectPath); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	body, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if strings.Contains(string(body), `[sandbox]`) || strings.Contains(string(body), `bash = "enforce"`) {
		t.Fatalf("ineffective Windows project bash enforce should be removed:\n%s", body)
	}
	if _, err := toml.Decode(string(body), &Config{}); err != nil {
		t.Fatalf("saved project config does not parse: %v", err)
	}
}

func TestSaveToExistingProjectRemovesIneffectiveWindowsBashEnforceWhenTargetIsOff(t *testing.T) {
	setRuntimeGOOS(t, "windows")
	projectPath := filepath.Join(t.TempDir(), "reasonix.toml")
	if err := os.WriteFile(projectPath, []byte("[sandbox]\nbash = \"enforce\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Default()
	cfg.Sandbox.Bash = "off"
	if err := cfg.SaveTo(projectPath); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	body, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if strings.Contains(string(body), `[sandbox]`) || strings.Contains(string(body), `bash = "enforce"`) {
		t.Fatalf("ineffective Windows project bash enforce should be removed even when the target mode is raw off:\n%s", body)
	}
	if _, err := toml.Decode(string(body), &Config{}); err != nil {
		t.Fatalf("saved project config does not parse: %v", err)
	}
}

func TestSaveToExistingProjectRemovesOnlyIneffectiveWindowsBashEnforce(t *testing.T) {
	setRuntimeGOOS(t, "windows")
	projectPath := filepath.Join(t.TempDir(), "reasonix.toml")
	if err := os.WriteFile(projectPath, []byte("[sandbox]\nbash = \"enforce\"\nnetwork = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Default()
	cfg.Sandbox.Bash = "enforce"
	if err := cfg.SaveTo(projectPath); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	body, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if strings.Contains(string(body), `bash = "enforce"`) {
		t.Fatalf("ineffective Windows project bash enforce should be removed:\n%s", body)
	}
	if !strings.Contains(string(body), `[sandbox]`) || !strings.Contains(string(body), `network = true`) {
		t.Fatalf("other sandbox fields should be preserved:\n%s", body)
	}
	var got Config
	if _, err := toml.Decode(string(body), &got); err != nil {
		t.Fatalf("saved project config does not parse: %v", err)
	}
	if !got.Sandbox.Network {
		t.Fatalf("network = false, want preserved true")
	}
}

func TestSaveForRootDoesNotWriteUserAgentSettingsIntoProjectConfig(t *testing.T) {
	isolateUserConfigHome(t)
	root := t.TempDir()
	userPath := UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(userPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte("[agent]\ntemperature = 0.42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(root, "reasonix.toml")
	if err := os.WriteFile(projectPath, []byte("[permissions]\nallow = [\"Bash(go test:*)\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadForRoot(root)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if cfg.Agent.Temperature != 0.42 {
		t.Fatalf("runtime temperature = %v, want merged user config", cfg.Agent.Temperature)
	}
	if err := cfg.SaveForRoot(root); err != nil {
		t.Fatalf("SaveForRoot: %v", err)
	}
	body, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("read project config: %v", err)
	}
	if strings.Contains(string(body), "temperature") {
		t.Fatalf("user agent setting leaked into project config:\n%s", body)
	}
}

func TestSetNetworkRejectsIncompleteCustomProxy(t *testing.T) {
	c := Default()
	if err := c.SetNetwork(NetworkConfig{ProxyMode: "custom"}); err == nil {
		t.Fatal("custom proxy without server/port should be rejected")
	}
}

func TestEffortCapabilityCustomSupportedEfforts(t *testing.T) {
	e := &ProviderEntry{
		Name:             "custom",
		Kind:             "openai",
		BaseURL:          "https://example.com",
		SupportedEfforts: []string{"low", "medium", "high"},
		DefaultEffort:    "high",
	}
	cap := EffortCapabilityForEntry(e)
	if !cap.Supported {
		t.Fatalf("expected supported, got %+v", cap)
	}
	wantLevels := []string{"auto", "low", "medium", "high"}
	if len(cap.Levels) != len(wantLevels) {
		t.Fatalf("levels = %v, want %v", cap.Levels, wantLevels)
	}
	for i, l := range wantLevels {
		if cap.Levels[i] != l {
			t.Errorf("levels[%d] = %q, want %q", i, cap.Levels[i], l)
		}
	}
	if cap.Default != "high" {
		t.Errorf("default = %q, want high", cap.Default)
	}
}

func TestEffortCapabilityUsesKnownModelRegistry(t *testing.T) {
	e := &ProviderEntry{
		Name:    "deepseek-proxy",
		Kind:    "openai",
		BaseURL: "https://proxy.example.com/v1",
		Model:   "deepseek-v4-flash",
	}
	cap := EffortCapabilityForEntry(e)
	if !cap.Supported {
		t.Fatalf("deepseek model behind proxy should expose effort, got %+v", cap)
	}
	wantLevels := []string{"auto", "disabled", "low", "high", "max"}
	if len(cap.Levels) != len(wantLevels) {
		t.Fatalf("levels = %v, want %v", cap.Levels, wantLevels)
	}
	for i, want := range wantLevels {
		if cap.Levels[i] != want {
			t.Fatalf("levels[%d] = %q, want %q", i, cap.Levels[i], want)
		}
	}
	if cap.Default != "high" {
		t.Fatalf("default = %q, want high", cap.Default)
	}
	if protocol := ReasoningProtocolForEntry(e); protocol != ReasoningProtocolDeepSeek {
		t.Fatalf("protocol = %q, want deepseek", protocol)
	}
	if got, err := NormalizeEffort(e, "max"); err != nil || got != "max" {
		t.Fatalf("NormalizeEffort(max) = %q/%v, want max/nil", got, err)
	}
	if got, err := NormalizeEffort(e, "low"); err != nil || got != "low" {
		t.Fatalf("NormalizeEffort(low) = %q/%v, want low/nil", got, err)
	}
}

func TestReasoningProtocolOverrideControlsEffortCapability(t *testing.T) {
	e := &ProviderEntry{
		Name:              "deepseek-proxy",
		Kind:              "openai",
		BaseURL:           "https://proxy.example.com/v1",
		Model:             "deepseek-v4-flash",
		ReasoningProtocol: "none",
	}
	if cap := EffortCapabilityForEntry(e); cap.Supported {
		t.Fatalf("reasoning_protocol=none should disable effort, got %+v", cap)
	}
	if protocol := ReasoningProtocolForEntry(e); protocol != ReasoningProtocolNone {
		t.Fatalf("protocol = %q, want none", protocol)
	}
	if _, err := NormalizeEffort(e, "max"); err == nil {
		t.Fatal("NormalizeEffort should reject effort when reasoning_protocol=none")
	}

	e.ReasoningProtocol = "openai"
	cap := EffortCapabilityForEntry(e)
	if !cap.Supported {
		t.Fatalf("reasoning_protocol=openai should expose OpenAI effort levels, got %+v", cap)
	}
	wantLevels := []string{"auto", "low", "medium", "high"}
	if len(cap.Levels) != len(wantLevels) {
		t.Fatalf("levels = %v, want %v", cap.Levels, wantLevels)
	}
	for i, want := range wantLevels {
		if cap.Levels[i] != want {
			t.Fatalf("levels[%d] = %q, want %q", i, cap.Levels[i], want)
		}
	}
	if _, err := NormalizeEffort(e, "max"); err == nil {
		t.Fatal("OpenAI reasoning_protocol should reject max")
	}
	if got, err := NormalizeEffort(e, "medium"); err != nil || got != "medium" {
		t.Fatalf("NormalizeEffort(medium) = %q/%v, want medium/nil", got, err)
	}
}

func TestNormalizeEffortCustomSupportedEfforts(t *testing.T) {
	e := &ProviderEntry{
		Name:             "custom",
		Kind:             "openai",
		BaseURL:          "https://example.com",
		SupportedEfforts: []string{"low", "medium", "high"},
	}
	for in, want := range map[string]string{"auto": "", "low": "low", "MEDIUM": "medium", "high": "high"} {
		got, err := NormalizeEffort(e, in)
		if in != want && in != "auto" {
			if err == nil {
				t.Fatalf("undeclared %q accepted as %q", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Fatalf("NormalizeEffort(%q) = %q/%v, want %q/nil", in, got, err, want)
		}
	}
	for _, bad := range []string{"max", "xhigh", "", "  "} {
		if _, err := NormalizeEffort(e, bad); err == nil {
			t.Errorf("NormalizeEffort(%q) should be rejected", bad)
		}
	}
}

func TestNormalizeEffortCustomDefaultEffort(t *testing.T) {
	e := &ProviderEntry{
		Name:             "custom",
		Kind:             "openai",
		BaseURL:          "https://example.com",
		SupportedEfforts: []string{"low", "medium", "high"},
		DefaultEffort:    "xhigh", // not in the list — must fall back to the first level
	}
	cap := EffortCapabilityForEntry(e)
	if cap.Default != "xhigh" {
		t.Fatalf("invalid default must remain visible for validation, got %q", cap.Default)
	}
	// Omitting DefaultEffort also falls back to the first level.
	e2 := *e
	e2.DefaultEffort = ""
	if cap := EffortCapabilityForEntry(&e2); cap.Default != "low" {
		t.Errorf("empty default = %q, want low", cap.Default)
	}
	// /effort auto still maps to "" regardless of DefaultEffort.
	if got, err := NormalizeEffort(e, "auto"); err != nil || got != "" {
		t.Fatalf("NormalizeEffort(auto) = %q/%v, want empty/nil", got, err)
	}
	e.Effort = "auto"
	if got := EffectiveEffort(e); got != "xhigh" {
		t.Fatalf("invalid configured default must not silently fall back, got %q", got)
	}
	e.Effort = "high"
	if got := EffectiveEffort(e); got != "high" {
		t.Fatalf("explicit effort should win over default_effort, got %q", got)
	}
}

func TestNormalizeEffortCustomLevelsCaseInsensitive(t *testing.T) {
	e := &ProviderEntry{
		Name:             "custom",
		Kind:             "openai",
		BaseURL:          "https://example.com",
		SupportedEfforts: []string{"Low", "MEDIUM", "medium", "auto", " "},
		DefaultEffort:    "MEDIUM",
	}
	cap := EffortCapabilityForEntry(e)
	wantLevels := []string{"auto", "low", "medium"}
	if len(cap.Levels) != len(wantLevels) {
		t.Fatalf("levels = %v, want %v", cap.Levels, wantLevels)
	}
	for i, want := range wantLevels {
		if cap.Levels[i] != want {
			t.Fatalf("levels[%d] = %q, want %q", i, cap.Levels[i], want)
		}
	}
	if cap.Default != "medium" {
		t.Fatalf("default = %q, want medium", cap.Default)
	}
	got, err := NormalizeEffort(e, "MEDIUM")
	if err == nil {
		t.Fatalf("NormalizeEffort(MEDIUM) accepted nonexact ID %q", got)
	}
	if got := EffectiveEffort(e); got != "medium" {
		t.Fatalf("EffectiveEffort = %q, want medium", got)
	}
}

func TestUpsertProviderNormalizesCustomEffortFields(t *testing.T) {
	c := &Config{}
	if err := c.UpsertProvider(ProviderEntry{
		Name:              "custom",
		Kind:              "openai",
		BaseURL:           "https://example.com",
		Model:             "m",
		Effort:            " HIGH ",
		ReasoningProtocol: " OPENAI ",
		SupportedEfforts:  []string{"Low", "MEDIUM", "medium", "auto"},
		DefaultEffort:     " LOW ",
	}); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	got, _ := c.Provider("custom")
	if got.Effort != "high" || got.DefaultEffort != "low" {
		t.Fatalf("effort/default = %q/%q, want high/low", got.Effort, got.DefaultEffort)
	}
	if got.ReasoningProtocol != "openai" {
		t.Fatalf("reasoning_protocol = %q, want openai", got.ReasoningProtocol)
	}
	wantSupported := []string{"low", "medium"}
	if len(got.SupportedEfforts) != len(wantSupported) {
		t.Fatalf("supported_efforts = %v, want %v", got.SupportedEfforts, wantSupported)
	}
	for i, want := range wantSupported {
		if got.SupportedEfforts[i] != want {
			t.Fatalf("supported_efforts[%d] = %q, want %q", i, got.SupportedEfforts[i], want)
		}
	}
}

func TestEffortCapabilityEmptySupportedEffortsNotConfigurable(t *testing.T) {
	// mimo-pro without SupportedEfforts: no built-in heuristic, /effort must reject.
	e := &ProviderEntry{
		Name:    "mimo-pro",
		Kind:    "openai",
		BaseURL: "https://unknown-gateway.example.com/v1",
		Model:   "mimo-v2.5-pro",
	}
	if cap := EffortCapabilityForEntry(e); cap.Supported {
		t.Fatalf("mimo-pro without SupportedEfforts should not be configurable, got %+v", cap)
	}
	if _, err := NormalizeEffort(e, "high"); err == nil {
		t.Fatal("NormalizeEffort should reject level for unsupported provider")
	}
	// `supported_efforts = []` (empty slice) is treated like nil — the v2 design
	// has no way to opt out of the built-in heuristic; users either configure
	// levels or leave the field unset.
	e2 := *e
	e2.SupportedEfforts = []string{}
	if cap := EffortCapabilityForEntry(&e2); cap.Supported {
		t.Fatalf("empty supported_efforts should also fall through to the heuristic, got %+v", cap)
	}
}

func TestWriteFilePreservesSymlinkToWritableTarget(t *testing.T) {
	home := t.TempDir()
	targetDir := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	target := filepath.Join(targetDir, "target.toml")
	link := UserConfigPath()
	if err := os.WriteFile(target, []byte("default_model = \"old\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	cfg := Default()
	cfg.DefaultModel = "deepseek-pro"
	if err := cfg.WriteFile(link); err != nil {
		t.Fatalf("WriteFile through symlink: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("WriteFile replaced the config symlink")
	}
	var persisted Config
	if _, err := toml.DecodeFile(target, &persisted); err != nil {
		t.Fatalf("decode target: %v", err)
	}
	if persisted.DefaultModel != "deepseek-pro" {
		t.Fatalf("target default_model = %q, want deepseek-pro", persisted.DefaultModel)
	}
}

func TestSaveToPreservesMultiLevelSymlinkChain(t *testing.T) {
	home := t.TempDir()
	targetDir := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	target := filepath.Join(targetDir, "target.toml")
	first := filepath.Join(targetDir, "first.toml")
	second := UserConfigPath()
	if err := os.WriteFile(target, []byte("default_model = \"old\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, first); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if err := os.Symlink(first, second); err != nil {
		t.Skipf("symlink chains are unavailable: %v", err)
	}

	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveConfigAccessPath(second, true)
	if err != nil {
		t.Fatalf("resolveConfigAccessPath(second): %v", err)
	}
	if got != resolvedTarget {
		t.Fatalf("resolveConfigAccessPath(second) = %q, want %q", got, resolvedTarget)
	}

	cfg := Default()
	cfg.DefaultModel = "deepseek-pro"
	if err := cfg.SaveTo(second); err != nil {
		t.Fatalf("SaveTo through symlink chain: %v", err)
	}
	for name, path := range map[string]string{"first": first, "second": second} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("Lstat(%s): %v", name, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("SaveTo replaced the %s symlink", name)
		}
	}
	var persisted Config
	if _, err := toml.DecodeFile(target, &persisted); err != nil {
		t.Fatalf("decode target: %v", err)
	}
	if persisted.DefaultModel != "deepseek-pro" {
		t.Fatalf("target default_model = %q, want deepseek-pro", persisted.DefaultModel)
	}
}

// makeDirReadOnly makes a directory non-writable using the platform's real
// permission mechanism. Windows directory read-only attributes do not block
// writes, so the test must use an ACL there.
func makeDirReadOnly(dir string) (func(), error) {
	if runtime.GOOS == "windows" {
		const everyoneSID = "*S-1-1-0"
		if err := exec.Command("icacls", dir, "/deny", everyoneSID+":(W)").Run(); err != nil {
			return nil, fmt.Errorf("icacls /deny: %w", err)
		}
		return func() {
			_ = exec.Command("icacls", dir, "/remove:d", everyoneSID).Run()
		}, nil
	}

	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		return nil, err
	}
	return func() { _ = os.Chmod(dir, info.Mode().Perm()) }, nil
}

func TestSaveToUnwritableUserSymlinkTargetPreservesLink(t *testing.T) {
	home := t.TempDir()
	targetDir := filepath.Join(t.TempDir(), "readonly")
	t.Setenv("REASONIX_HOME", home)
	target := filepath.Join(targetDir, "target.toml")
	link := UserConfigPath()
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("default_model = \"old\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	cleanup, err := makeDirReadOnly(targetDir)
	if err != nil {
		t.Fatalf("make target directory read-only: %v", err)
	}
	t.Cleanup(cleanup)

	cfg := Default()
	cfg.DefaultModel = "deepseek-pro"
	if err := cfg.SaveTo(link); err == nil {
		t.Fatal("SaveTo through symlink with unwritable target unexpectedly succeeded")
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("failed target write replaced the user config symlink")
	}
	var persisted Config
	if _, err := toml.DecodeFile(target, &persisted); err != nil {
		t.Fatalf("decode unchanged target config: %v", err)
	}
	if persisted.DefaultModel != "old" {
		t.Fatalf("failed write changed target default_model to %q", persisted.DefaultModel)
	}
}

func TestSaveToBrokenUserSymlinkFailsAndPreservesLink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	link := UserConfigPath()
	missingTarget := filepath.Join(t.TempDir(), "missing", "target.toml")
	if err := os.Symlink(missingTarget, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	cfg := Default()
	cfg.DefaultModel = "deepseek-pro"
	if err := cfg.SaveTo(link); err == nil {
		t.Fatal("SaveTo through broken user symlink unexpectedly succeeded")
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("failed write replaced the broken user config symlink")
	}
}

func TestSaveToProjectSymlinkOutsideRootFailsWithoutReadingOrReplacing(t *testing.T) {
	project := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "target.toml")
	link := filepath.Join(project, "reasonix.toml")
	const sentinel = "private_token = \"must-not-be-copied\"\n"
	if err := os.WriteFile(target, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if _, err := LoadForRootReadOnly(project); err == nil {
		t.Fatal("LoadForRootReadOnly accepted a project config symlink outside root")
	}

	cfg := Default()
	cfg.DefaultModel = "deepseek-pro"
	if err := cfg.SaveTo(link); err == nil {
		t.Fatal("SaveTo through project symlink outside root unexpectedly succeeded")
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("failed project config write replaced the external symlink")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sentinel {
		t.Fatalf("project config write changed outside target:\n%s", got)
	}
}

func TestProjectConfigSymlinkWithinRootLoadsAndSavesTarget(t *testing.T) {
	project := t.TempDir()
	targetDir := filepath.Join(project, "config")
	target := filepath.Join(targetDir, "reasonix.toml")
	link := filepath.Join(project, "reasonix.toml")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("default_model = \"deepseek-pro\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("config", "reasonix.toml"), link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	loaded, err := LoadForRootReadOnly(project)
	if err != nil {
		t.Fatalf("LoadForRootReadOnly through internal symlink: %v", err)
	}
	if loaded.DefaultModel != "deepseek-pro" {
		t.Fatalf("loaded default_model = %q, want deepseek-pro", loaded.DefaultModel)
	}
	loaded.Agent.Temperature = 0.42
	if err := loaded.SaveTo(link); err != nil {
		t.Fatalf("SaveTo through internal project symlink: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("SaveTo replaced an internal project config symlink")
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "temperature = 0.42") {
		t.Fatalf("internal symlink target was not updated:\n%s", raw)
	}
}

func TestBrokenProjectConfigSymlinkFailsLoadAndSave(t *testing.T) {
	project := t.TempDir()
	link := filepath.Join(project, "reasonix.toml")
	if err := os.Symlink(filepath.Join("missing", "reasonix.toml"), link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	if _, err := LoadForRootReadOnly(project); err == nil {
		t.Fatal("LoadForRootReadOnly accepted a broken project config symlink")
	}
	cfg := Default()
	cfg.DefaultModel = "deepseek-pro"
	if err := cfg.SaveTo(link); err == nil {
		t.Fatal("SaveTo accepted a broken project config symlink")
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("failed operations replaced the broken project config symlink")
	}
}
