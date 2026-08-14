package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeProjectDefaultTestConfig(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A provider defined once in the user-global config must carry the workspace's
// cachecontext, so projects can vary their DeepSeek user_id without repeating
// the provider entry per project.
func TestLoadForRoot_ProjectCacheContextPropagatesToSharedProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	writeProjectDefaultTestConfig(t, home, "config.toml", `default_model = "deepseek-pro"

[[providers]]
name        = "deepseek-pro"
kind        = "anthropic"
base_url    = "https://api.deepseek.com/anthropic"
model       = "deepseek-v4-pro"
api_key_env = "DEEPSEEK_API_KEY"
`)

	project := t.TempDir()
	writeProjectDefaultTestConfig(t, project, "reasonix.toml", `cachecontext = "proj-alpha"
`)

	cfg, err := LoadForRoot(project)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if cfg.CacheContext != "proj-alpha" {
		t.Fatalf("CacheContext = %q, want %q", cfg.CacheContext, "proj-alpha")
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(cfg.Providers))
	}
	if got := cfg.Providers[0].CacheContextValue(); got != "proj-alpha" {
		t.Fatalf("shared provider CacheContextValue = %q, want %q", got, "proj-alpha")
	}
}

// A workspace with no cachecontext must leave provider entries empty so no
// spurious user_id is emitted.
func TestLoadForRoot_NoCacheContextStaysEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	writeProjectDefaultTestConfig(t, home, "config.toml", `default_model = "deepseek-pro"

[[providers]]
name        = "deepseek-pro"
kind        = "anthropic"
base_url    = "https://api.deepseek.com/anthropic"
model       = "deepseek-v4-pro"
api_key_env = "DEEPSEEK_API_KEY"
`)

	cfg, err := LoadForRoot(t.TempDir())
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if cfg.CacheContext != "" {
		t.Fatalf("CacheContext = %q, want empty", cfg.CacheContext)
	}
	if got := cfg.Providers[0].CacheContextValue(); got != "" {
		t.Fatalf("provider CacheContextValue = %q, want empty", got)
	}
}

// #4218: pre-v1.11 persistence paths full-rendered ./reasonix.toml and pinned
// the built-in default_model ("deepseek-flash") into it. Once the user's
// [[providers]] replaced the built-in presets, that stale name resolved to
// nothing and every launch from that folder failed. The load must fall back to
// the user/global default_model instead.
func TestLoadForRoot_UnresolvableProjectDefaultModelFallsBackToUserDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	writeProjectDefaultTestConfig(t, home, "config.toml", `default_model = "deepseek-pro"

[[providers]]
name        = "deepseek-pro"
kind        = "openai"
base_url    = "https://api.deepseek.com"
model       = "deepseek-v4-pro"
api_key_env = "DEEPSEEK_API_KEY"
`)

	project := t.TempDir()
	writeProjectDefaultTestConfig(t, project, "reasonix.toml", `default_model = "deepseek-flash"

[permissions]
allow = ["Bash(go test*)"]
`)

	cfg, err := LoadForRoot(project)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if cfg.DefaultModel != "deepseek-pro" {
		t.Fatalf("DefaultModel = %q, want fallback to user default %q", cfg.DefaultModel, "deepseek-pro")
	}
	if got := cfg.IgnoredProjectDefaultModel(); got != "deepseek-flash" {
		t.Fatalf("IgnoredProjectDefaultModel() = %q, want %q", got, "deepseek-flash")
	}
	if _, ok := cfg.ResolveModel(cfg.DefaultModel); !ok {
		t.Fatalf("ResolveModel(%q) failed after fallback", cfg.DefaultModel)
	}
}

// A project default_model that does resolve is a legitimate override and must
// keep winning over the user config.
func TestLoadForRoot_ResolvableProjectDefaultModelStillWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	writeProjectDefaultTestConfig(t, home, "config.toml", `default_model = "deepseek-pro"

[[providers]]
name        = "deepseek-pro"
kind        = "openai"
base_url    = "https://api.deepseek.com"
model       = "deepseek-v4-pro"
api_key_env = "DEEPSEEK_API_KEY"

[[providers]]
name        = "local"
kind        = "openai"
base_url    = "http://127.0.0.1:8080/v1"
model       = "local-model"
`)

	project := t.TempDir()
	writeProjectDefaultTestConfig(t, project, "reasonix.toml", `default_model = "local"
`)

	cfg, err := LoadForRoot(project)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if cfg.DefaultModel != "local" {
		t.Fatalf("DefaultModel = %q, want project override %q kept", cfg.DefaultModel, "local")
	}
	if got := cfg.IgnoredProjectDefaultModel(); got != "" {
		t.Fatalf("IgnoredProjectDefaultModel() = %q, want empty", got)
	}
}

// When neither the project override nor the user default resolves, keep the
// project value so the existing boot error names what the user actually wrote.
func TestLoadForRoot_ProjectDefaultModelKeptWhenUserDefaultUnresolvable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	writeProjectDefaultTestConfig(t, home, "config.toml", `default_model = "gone-too"

[[providers]]
name        = "deepseek-pro"
kind        = "openai"
base_url    = "https://api.deepseek.com"
model       = "deepseek-v4-pro"
api_key_env = "DEEPSEEK_API_KEY"
`)

	project := t.TempDir()
	writeProjectDefaultTestConfig(t, project, "reasonix.toml", `default_model = "gone"
`)

	cfg, err := LoadForRoot(project)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if cfg.DefaultModel != "gone" {
		t.Fatalf("DefaultModel = %q, want project value %q kept", cfg.DefaultModel, "gone")
	}
	if got := cfg.IgnoredProjectDefaultModel(); got != "" {
		t.Fatalf("IgnoredProjectDefaultModel() = %q, want empty", got)
	}
}

// When the project file is the user's only config, a broken default_model must
// keep failing loudly at boot instead of silently substituting the built-in
// default (see boot.TestBuildUnknownModelErrorIsActionable): no fallback fires
// because no user config explicitly defines default_model.
func TestLoadForRoot_NoUserConfigKeepsBrokenProjectDefaultModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)

	project := t.TempDir()
	writeProjectDefaultTestConfig(t, project, "reasonix.toml", `default_model = "legacy-missing"

[[providers]]
name        = "deepseek-flash"
kind        = "openai"
base_url    = "https://example.invalid"
model       = "deepseek-v4-flash"
api_key_env = "REASONIX_TEST_KEY_UNSET"
`)

	cfg, err := LoadForRoot(project)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if cfg.DefaultModel != "legacy-missing" {
		t.Fatalf("DefaultModel = %q, want broken project value %q kept", cfg.DefaultModel, "legacy-missing")
	}
	if got := cfg.IgnoredProjectDefaultModel(); got != "" {
		t.Fatalf("IgnoredProjectDefaultModel() = %q, want empty", got)
	}
}

// No project override: the user default is untouched and nothing is reported
// as ignored, even when the user default itself is stale.
func TestLoadForRoot_NoProjectOverrideLeavesUserDefaultAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	writeProjectDefaultTestConfig(t, home, "config.toml", `default_model = "deepseek-pro"

[[providers]]
name        = "deepseek-pro"
kind        = "openai"
base_url    = "https://api.deepseek.com"
model       = "deepseek-v4-pro"
api_key_env = "DEEPSEEK_API_KEY"
`)

	project := t.TempDir()

	cfg, err := LoadForRoot(project)
	if err != nil {
		t.Fatalf("LoadForRoot: %v", err)
	}
	if cfg.DefaultModel != "deepseek-pro" {
		t.Fatalf("DefaultModel = %q, want %q", cfg.DefaultModel, "deepseek-pro")
	}
	if got := cfg.IgnoredProjectDefaultModel(); got != "" {
		t.Fatalf("IgnoredProjectDefaultModel() = %q, want empty", got)
	}
}
