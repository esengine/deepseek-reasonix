package cli

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"reasonix/internal/config"
)

func setupTestConfig() *config.Config {
	cfg := config.Default()
	cfg.Providers = []config.ProviderEntry{
		{Name: "desktop-provider", Kind: "openai", BaseURL: "https://desktop.example/v1", Model: "desktop-model", APIKeyEnv: "SHARED_API_KEY"},
		{Name: "cli-provider", Kind: "openai", BaseURL: "https://cli.example/v1", Model: "cli-model"},
	}
	cfg.DefaultModel = "desktop-provider"
	cfg.Agent.Temperature = 0.77
	cfg.Desktop.ProviderAccess = []string{"desktop-provider", "cli-provider"}
	return cfg
}

func TestProviderSetupSessionAddPreservesExistingProvidersAndSettings(t *testing.T) {
	cfg := setupTestConfig()
	s := newProviderSetupSession(cfg)
	added := config.ProviderEntry{Name: "grok-relay", Kind: "openai", BaseURL: "https://relay.example/v1", Model: "grok-4.5", APIKeyEnv: "GROK_RELAY_API_KEY"}
	if err := s.upsert([]config.ProviderEntry{added}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 3 || cfg.Providers[0].Name != "desktop-provider" || cfg.Providers[1].Name != "cli-provider" {
		t.Fatalf("existing providers were not preserved: %+v", cfg.Providers)
	}
	if cfg.DefaultModel != "desktop-provider" || cfg.Agent.Temperature != 0.77 {
		t.Fatalf("unrelated settings changed: default=%q temperature=%v", cfg.DefaultModel, cfg.Agent.Temperature)
	}
	s.addProviderAccess([]config.ProviderEntry{added})
	if got := cfg.Desktop.ProviderAccess; !containsString(got, "desktop-provider") || !containsString(got, "cli-provider") || !containsString(got, "grok-relay") {
		t.Fatalf("desktop provider access was not preserved and extended: %v", got)
	}
}

func TestProviderSetupSessionEditPreservesSiblingAndAdvancedFields(t *testing.T) {
	cfg := setupTestConfig()
	cfg.Providers[0].Headers = map[string]string{"X-Relay": "yes"}
	s := newProviderSetupSession(cfg)
	edited := cfg.Providers[0]
	edited.Models = []string{"desktop-model", "desktop-model-2"}
	edited.Model = ""
	if err := s.upsert([]config.ProviderEntry{edited}); err != nil {
		t.Fatal(err)
	}
	if cfg.Providers[1].Name != "cli-provider" {
		t.Fatalf("sibling provider changed: %+v", cfg.Providers[1])
	}
	if cfg.Providers[0].Headers["X-Relay"] != "yes" {
		t.Fatalf("advanced provider fields were lost: %+v", cfg.Providers[0])
	}
}

func TestProviderSetupSessionAddRejectsExistingProviderWithoutChangingIt(t *testing.T) {
	cfg := setupTestConfig()
	baseURL := "https://desktop.example/v1"
	name := providerSlug("custom", baseURL)
	cfg.Providers[0].Name = name
	cfg.DefaultModel = name
	cfg.Providers[0].Headers = map[string]string{"X-Relay": "yes"}
	cfg.Providers[0].NoProxy = true
	want := cfg.Providers[0]
	s := newProviderSetupSession(cfg)
	replacement := config.ProviderEntry{
		Name: providerSlug("custom", baseURL), Kind: "openai", BaseURL: baseURL,
		Model: "new-model", APIKeyEnv: "SHARED_API_KEY",
	}
	if err := s.add([]config.ProviderEntry{replacement}); err == nil {
		t.Fatal("adding an existing provider should require the edit flow")
	}
	if !reflect.DeepEqual(cfg.Providers[0], want) {
		t.Fatalf("existing provider changed after rejected add:\n got: %+v\nwant: %+v", cfg.Providers[0], want)
	}
}

func TestProviderSetupSessionRemovalIsExplicitAndRepairsDefault(t *testing.T) {
	cfg := setupTestConfig()
	s := newProviderSetupSession(cfg)
	if len(cfg.Providers) != 2 {
		t.Fatal("provider changed before explicit remove")
	}
	if err := s.remove("desktop-provider"); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "cli-provider" {
		t.Fatalf("remove result = %+v", cfg.Providers)
	}
	if cfg.DefaultModel != "cli-provider" {
		t.Fatalf("default fallback = %q, want cli-provider", cfg.DefaultModel)
	}
	if containsString(cfg.Desktop.ProviderAccess, "desktop-provider") || !containsString(cfg.Desktop.ProviderAccess, "cli-provider") {
		t.Fatalf("desktop provider access was not cleaned safely: %v", cfg.Desktop.ProviderAccess)
	}
}

func TestProviderSetupSessionAddPromotesDefaultWhenCurrentDefaultUnusable(t *testing.T) {
	isolateUserConfig(t)
	cfg := setupTestConfig()
	// desktop-provider names SHARED_API_KEY, which is neither stored nor staged.
	s := newProviderSetupSession(cfg)
	added := config.ProviderEntry{Name: "grok-relay", Kind: "openai", BaseURL: "https://relay.example/v1", Model: "grok-4.5", APIKeyEnv: "GROK_RELAY_API_KEY"}
	if err := s.add([]config.ProviderEntry{added}); err != nil {
		t.Fatal(err)
	}
	if err := s.setCredential("GROK_RELAY_API_KEY", "staged-secret"); err != nil {
		t.Fatal(err)
	}
	s.promoteDefaultToNewProviders([]config.ProviderEntry{added})
	if cfg.DefaultModel != "grok-relay" {
		t.Fatalf("default = %q, want promotion to grok-relay", cfg.DefaultModel)
	}
}

func TestProviderSetupSessionAddKeepsUsableDefault(t *testing.T) {
	isolateUserConfig(t)
	added := config.ProviderEntry{Name: "grok-relay", Kind: "openai", BaseURL: "https://relay.example/v1", Model: "grok-4.5", APIKeyEnv: "GROK_RELAY_API_KEY"}

	// cli-provider needs no key, so the default is usable and must not move.
	cfg := setupTestConfig()
	cfg.DefaultModel = "cli-provider"
	s := newProviderSetupSession(cfg)
	if err := s.add([]config.ProviderEntry{added}); err != nil {
		t.Fatal(err)
	}
	if err := s.setCredential("GROK_RELAY_API_KEY", "staged-secret"); err != nil {
		t.Fatal(err)
	}
	s.promoteDefaultToNewProviders([]config.ProviderEntry{added})
	if cfg.DefaultModel != "cli-provider" {
		t.Fatalf("keyless default was hijacked: %q", cfg.DefaultModel)
	}

	// A default whose key was staged earlier in this session is usable too.
	cfg = setupTestConfig()
	s = newProviderSetupSession(cfg)
	if err := s.setCredential("SHARED_API_KEY", "staged-secret"); err != nil {
		t.Fatal(err)
	}
	if err := s.add([]config.ProviderEntry{added}); err != nil {
		t.Fatal(err)
	}
	s.promoteDefaultToNewProviders([]config.ProviderEntry{added})
	if cfg.DefaultModel != "desktop-provider" {
		t.Fatalf("staged-key default was hijacked: %q", cfg.DefaultModel)
	}
}

func TestFirstRunAddCustomProviderPersistsPromotedDefault(t *testing.T) {
	isolateUserConfig(t)
	path := config.UserConfigPath()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("first-run precondition: config already exists at %s", path)
	}
	cfg := config.LoadForEdit(path)
	s := newProviderSetupSessionForPath(cfg, path)
	added := config.ProviderEntry{Name: "grok-relay", Kind: "openai", BaseURL: "https://relay.example/v1", Model: "grok-4.5", APIKeyEnv: "GROK_RELAY_API_KEY"}
	if err := s.add([]config.ProviderEntry{added}); err != nil {
		t.Fatal(err)
	}
	s.addProviderAccess([]config.ProviderEntry{added})
	if err := s.setCredential("GROK_RELAY_API_KEY", "staged-secret"); err != nil {
		t.Fatal(err)
	}
	s.promoteDefaultToNewProviders([]config.ProviderEntry{added})
	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatal(err)
	}
	got := config.LoadForEdit(path)
	if got.DefaultModel != "grok-relay" {
		t.Fatalf("persisted default = %q, want grok-relay (built-in default has no key)", got.DefaultModel)
	}
	if _, ok := got.ResolveModel(got.DefaultModel); !ok {
		t.Fatalf("persisted default %q does not resolve", got.DefaultModel)
	}
}

func TestProviderSetupSessionUpsertRepairsDanglingDefaultRef(t *testing.T) {
	isolateUserConfig(t)
	cfg := setupTestConfig()
	cfg.DefaultModel = "cli-provider/cli-model"
	s := newProviderSetupSession(cfg)
	refreshed := cfg.Providers[1]
	refreshed.Model = ""
	refreshed.Models = []string{"cli-model-2"}
	if err := s.upsert([]config.ProviderEntry{refreshed}); err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "cli-provider" {
		t.Fatalf("dangling default = %q, want repair to cli-provider", cfg.DefaultModel)
	}
	if _, ok := cfg.ResolveModel(cfg.DefaultModel); !ok {
		t.Fatalf("repaired default %q does not resolve", cfg.DefaultModel)
	}
}

func TestProviderSetupSessionAddAccessRespectsExplicitEmptyList(t *testing.T) {
	cfg := setupTestConfig()
	cfg.Desktop.ProviderAccess = nil
	s := newProviderSetupSession(cfg)
	s.accessDeclared = true
	added := config.ProviderEntry{Name: "grok-relay", Kind: "openai", BaseURL: "https://relay.example/v1", Model: "grok-4.5", APIKeyEnv: "GROK_API_KEY"}
	s.addProviderAccess([]config.ProviderEntry{added})
	if got := cfg.Desktop.ProviderAccess; len(got) != 1 || got[0] != "grok-relay" {
		t.Fatalf("explicit empty access should enable only the added provider, got %v", got)
	}
}

func TestProviderSetupSessionAddAccessSeedsUndeclaredLegacyProviders(t *testing.T) {
	cfg := setupTestConfig()
	cfg.Desktop.ProviderAccess = nil
	s := newProviderSetupSession(cfg)
	added := config.ProviderEntry{Name: "grok-relay", Kind: "openai", BaseURL: "https://relay.example/v1", Model: "grok-4.5", APIKeyEnv: "GROK_API_KEY"}
	s.addProviderAccess([]config.ProviderEntry{added, added})
	if got := cfg.Desktop.ProviderAccess; !containsString(got, "cli-provider") || !containsString(got, "grok-relay") {
		t.Fatalf("undeclared legacy access should preserve configured siblings and add the new provider: %v", got)
	}
	count := 0
	for _, name := range cfg.Desktop.ProviderAccess {
		if name == "grok-relay" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("added provider access should be deduplicated: %v", cfg.Desktop.ProviderAccess)
	}
}

func TestLocalProviderSetupAccessOnlySeedsProjectProviders(t *testing.T) {
	isolateUserConfig(t)
	path := filepath.Join(t.TempDir(), "reasonix.toml")
	if err := os.WriteFile(path, []byte(`
[[providers]]
name = "project-relay"
kind = "openai"
base_url = "https://project.example/v1"
model = "project-model"
api_key_env = ""
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.LoadForEdit(path)
	s := newProviderSetupSessionForPath(cfg, path)
	added := config.ProviderEntry{Name: "grok-relay", Kind: "openai", BaseURL: "https://relay.example/v1", Model: "grok-4.5"}
	if err := s.add([]config.ProviderEntry{added}); err != nil {
		t.Fatal(err)
	}
	s.addProviderAccess([]config.ProviderEntry{added})
	got := cfg.Desktop.ProviderAccess
	if !containsString(got, "project-relay") || !containsString(got, "grok-relay") {
		t.Fatalf("project provider access = %v, want existing and added project providers", got)
	}
	for _, forbidden := range []string{"deepseek", "deepseek-flash", "deepseek-pro"} {
		if containsString(got, forbidden) {
			t.Fatalf("project provider access unexpectedly enabled built-in %q: %v", forbidden, got)
		}
	}
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	reloaded := config.LoadForEditWithoutCredentials(path)
	if got := reloaded.Desktop.ProviderAccess; !containsString(got, "project-relay") || !containsString(got, "grok-relay") {
		t.Fatalf("reloaded project provider access = %v", got)
	} else {
		for _, forbidden := range []string{"deepseek", "deepseek-flash", "deepseek-pro"} {
			if containsString(got, forbidden) {
				t.Fatalf("reloaded project access unexpectedly enabled %q: %v", forbidden, got)
			}
		}
	}
}

func TestNewProviderSetupSessionDetectsExplicitProviderAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[desktop]\nprovider_access = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newProviderSetupSessionForPath(setupTestConfig(), path)
	if !s.accessDeclared {
		t.Fatal("explicit empty desktop.provider_access was treated as undeclared")
	}
}

func TestProviderSetupSessionPersistsEmptyAccessAfterLastRemoval(t *testing.T) {
	isolateUserConfig(t)
	cfg := setupTestConfig()
	cfg.Desktop.ProviderAccess = []string{"desktop-provider"}
	s := newProviderSetupSession(cfg)
	s.accessDeclared = true
	if err := s.remove("desktop-provider"); err != nil {
		t.Fatal(err)
	}
	if cfg.Desktop.ProviderAccess == nil || len(cfg.Desktop.ProviderAccess) != 0 {
		t.Fatalf("last removal should retain an explicit empty access list: %#v", cfg.Desktop.ProviderAccess)
	}
	path := config.UserConfigPath()
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "provider_access = []") {
		t.Fatalf("explicit empty provider access was omitted from saved config:\n%s", body)
	}
}

func TestProviderSetupSessionAllowsSharedCredentialName(t *testing.T) {
	cfg := setupTestConfig()
	s := newProviderSetupSession(cfg)
	shared := config.ProviderEntry{Name: "second-relay", Kind: "openai", BaseURL: "https://other.example/v1", Model: "grok-4.5", APIKeyEnv: "SHARED_API_KEY"}
	if err := s.upsert([]config.ProviderEntry{shared}); err != nil {
		t.Fatalf("intentional shared api_key_env should be valid: %v", err)
	}
	if err := s.setCredential("SHARED_API_KEY", "shared-secret"); err != nil {
		t.Fatal(err)
	}
	if got := s.credentialLines(); len(got) != 1 || got[0] != "SHARED_API_KEY=shared-secret" {
		t.Fatalf("credential lines = %v", got)
	}
}

func TestProviderSetupSessionCancelDoesNotWriteFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := []byte("default_model = \"keep\"\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := setupTestConfig()
	s := newProviderSetupSession(cfg)
	if err := s.upsert([]config.ProviderEntry{{Name: "staged", Kind: "openai", BaseURL: "https://staged.example/v1", Model: "staged-model", APIKeyEnv: "STAGED_API_KEY"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.setCredential("STAGED_API_KEY", "not-written"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("staging changed config on disk: %q", got)
	}
}

func TestPromptOptionalAPIKeyEnvNameAllowsNoAuthProvider(t *testing.T) {
	var out bytes.Buffer
	got := promptOptionalAPIKeyEnvName(bufio.NewScanner(strings.NewReader("\n")), &out, "API key variable", "")
	if got != "" {
		t.Fatalf("optional API key variable = %q, want empty", got)
	}
}

func TestProviderSetupSessionSummaryReportsChanges(t *testing.T) {
	cfg := setupTestConfig()
	s := newProviderSetupSession(cfg)
	if err := s.remove("cli-provider"); err != nil {
		t.Fatal(err)
	}
	if err := s.upsert([]config.ProviderEntry{{Name: "grok-relay", Kind: "openai", BaseURL: "https://relay.example/v1", Model: "grok-4.5", APIKeyEnv: "GROK_API_KEY"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.setCredential("GROK_API_KEY", "secret"); err != nil {
		t.Fatal(err)
	}
	text := strings.Join(s.summary(), "\n")
	for _, want := range []string{"grok-relay", "cli-provider", "1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary %q missing %q", text, want)
		}
	}
}

func TestProviderSetupSummaryDoesNotLeakCredential(t *testing.T) {
	s := newProviderSetupSession(setupTestConfig())
	const secret = "rk_test_ABCDEF1234567890"
	if err := s.setCredential("GROK_API_KEY", secret); err != nil {
		t.Fatal(err)
	}
	text := strings.Join(s.summary(), "\n")
	if strings.Contains(text, secret) {
		t.Fatalf("summary leaked credential: %q", text)
	}
}

func TestProviderSetupOperationReplayPreservesConcurrentUnrelatedChanges(t *testing.T) {
	isolateUserConfig(t)
	path := config.UserConfigPath()
	initial := setupTestConfig()
	if err := initial.SaveTo(path); err != nil {
		t.Fatal(err)
	}

	working := config.LoadForEdit(path)
	s := newProviderSetupSessionForPath(working, path)
	s.setLanguage("en")
	edited := working.Providers[0]
	edited.Models = []string{"desktop-model", "desktop-model-2"}
	edited.Model = ""
	if err := s.upsert([]config.ProviderEntry{edited}); err != nil {
		t.Fatal(err)
	}

	external := config.LoadForEdit(path)
	external.Agent.Temperature = 0.23
	external.DefaultModel = "cli-provider"
	external.Providers[1].Headers = map[string]string{"X-External": "keep"}
	if err := external.SaveTo(path); err != nil {
		t.Fatal(err)
	}

	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatalf("commitProviderSetupSession: %v", err)
	}
	got := config.LoadForEdit(path)
	if got.Agent.Temperature != 0.23 || got.DefaultModel != "cli-provider" || got.Language != "en" {
		t.Fatalf("scalar replay lost data: temperature=%v default=%q language=%q", got.Agent.Temperature, got.DefaultModel, got.Language)
	}
	if got.Providers[1].Headers["X-External"] != "keep" {
		t.Fatalf("concurrent sibling provider edit was lost: %+v", got.Providers[1])
	}
	if models := got.Providers[0].ModelList(); !containsString(models, "desktop-model-2") {
		t.Fatalf("setup provider edit was not replayed: %v", models)
	}
}

func TestProviderSetupCommitWithNoOperationsDoesNotRewriteConfig(t *testing.T) {
	isolateUserConfig(t)
	path := config.UserConfigPath()
	initial := setupTestConfig()
	if err := initial.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := newProviderSetupSessionForPath(config.LoadForEdit(path), path)
	written, err := commitProviderSetupSession(s, path)
	if err != nil {
		t.Fatal(err)
	}
	if written {
		t.Fatal("no-op setup reported a config write")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("no-op setup rewrote config")
	}
}

func TestProviderSetupOperationReplayRejectsConcurrentSameProviderEdit(t *testing.T) {
	isolateUserConfig(t)
	path := config.UserConfigPath()
	initial := setupTestConfig()
	if err := initial.SaveTo(path); err != nil {
		t.Fatal(err)
	}

	working := config.LoadForEdit(path)
	s := newProviderSetupSessionForPath(working, path)
	edited := working.Providers[0]
	edited.Model = "setup-model"
	edited.Models = nil
	if err := s.upsert([]config.ProviderEntry{edited}); err != nil {
		t.Fatal(err)
	}

	external := config.LoadForEdit(path)
	external.Providers[0].BaseURL = "https://external.example/v1"
	if err := external.SaveTo(path); err != nil {
		t.Fatal(err)
	}

	_, err := commitProviderSetupSession(s, path)
	var conflict *providerSetupConflictError
	if !errors.As(err, &conflict) || !strings.Contains(conflict.field, "desktop-provider") {
		t.Fatalf("commit conflict = %v, want provider conflict", err)
	}
	got := config.LoadForEdit(path)
	if got.Providers[0].BaseURL != "https://external.example/v1" || got.Providers[0].Model != "desktop-model" {
		t.Fatalf("conflicting setup edit changed disk config: %+v", got.Providers[0])
	}
}

func TestProviderSetupSaveConflictDoesNotWriteStagedCredentials(t *testing.T) {
	isolateUserConfig(t)
	path := config.UserConfigPath()
	initial := setupTestConfig()
	if err := initial.SaveTo(path); err != nil {
		t.Fatal(err)
	}

	working := config.LoadForEdit(path)
	s := newProviderSetupSessionForPath(working, path)
	edited := working.Providers[0]
	edited.Model = "setup-model"
	edited.Models = nil
	if err := s.upsert([]config.ProviderEntry{edited}); err != nil {
		t.Fatal(err)
	}
	if err := s.setCredential("STAGED_API_KEY", "must-not-be-written"); err != nil {
		t.Fatal(err)
	}

	external := config.LoadForEdit(path)
	external.Providers[0].BaseURL = "https://external.example/v1"
	if err := external.SaveTo(path); err != nil {
		t.Fatal(err)
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = readEnd
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = readEnd.Close()
		_ = writeEnd.Close()
	})
	if _, err := writeEnd.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeEnd.Close(); err != nil {
		t.Fatal(err)
	}

	if rc := saveProviderSetupSession(s, path, config.UserCredentialsPath()); rc != 1 {
		t.Fatalf("saveProviderSetupSession return code = %d, want 1", rc)
	}
	if config.CredentialStored("STAGED_API_KEY") {
		t.Fatal("config conflict wrote staged credentials")
	}
}

func TestProviderSetupOperationReplayRejectsConcurrentDefaultEdit(t *testing.T) {
	isolateUserConfig(t)
	path := config.UserConfigPath()
	initial := setupTestConfig()
	if err := initial.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	working := config.LoadForEdit(path)
	s := newProviderSetupSessionForPath(working, path)
	if err := s.setDefaultModel("cli-provider"); err != nil {
		t.Fatal(err)
	}
	external := config.LoadForEdit(path)
	external.DefaultModel = "desktop-provider/desktop-model"
	if err := external.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	_, err := commitProviderSetupSession(s, path)
	var conflict *providerSetupConflictError
	if !errors.As(err, &conflict) || conflict.field != "default_model" {
		t.Fatalf("commit conflict = %v, want default_model conflict", err)
	}
	if got := config.LoadForEdit(path).DefaultModel; got != "desktop-provider/desktop-model" {
		t.Fatalf("external default_model was overwritten: %q", got)
	}
}

func TestProviderSetupOperationReplayMergesConcurrentAccessAddition(t *testing.T) {
	isolateUserConfig(t)
	path := config.UserConfigPath()
	initial := setupTestConfig()
	if err := initial.SaveTo(path); err != nil {
		t.Fatal(err)
	}

	working := config.LoadForEdit(path)
	s := newProviderSetupSessionForPath(working, path)
	setupProvider := config.ProviderEntry{Name: "setup-provider", Kind: "openai", BaseURL: "https://setup.example/v1", Model: "setup-model"}
	if err := s.add([]config.ProviderEntry{setupProvider}); err != nil {
		t.Fatal(err)
	}
	s.addProviderAccess([]config.ProviderEntry{setupProvider})

	external := config.LoadForEdit(path)
	externalProvider := config.ProviderEntry{Name: "external-provider", Kind: "openai", BaseURL: "https://external.example/v1", Model: "external-model"}
	if err := external.UpsertProvider(externalProvider); err != nil {
		t.Fatal(err)
	}
	external.Desktop.ProviderAccess = append(external.Desktop.ProviderAccess, externalProvider.Name)
	if err := external.SaveTo(path); err != nil {
		t.Fatal(err)
	}

	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatalf("commitProviderSetupSession: %v", err)
	}
	got := config.LoadForEdit(path)
	for _, name := range []string{setupProvider.Name, externalProvider.Name} {
		if _, ok := got.Provider(name); !ok || !containsString(got.Desktop.ProviderAccess, name) {
			t.Fatalf("provider/access %q missing after replay: providers=%v access=%v", name, got.Providers, got.Desktop.ProviderAccess)
		}
	}
}

func TestProviderSetupOperationReplayRejectsConcurrentAccessDeclaration(t *testing.T) {
	isolateUserConfig(t)
	path := config.UserConfigPath()
	initial := setupTestConfig()
	initial.Desktop.ProviderAccess = nil
	if err := initial.SaveTo(path); err != nil {
		t.Fatal(err)
	}

	working := config.LoadForEdit(path)
	s := newProviderSetupSessionForPath(working, path)
	added := config.ProviderEntry{Name: "setup-provider", Kind: "openai", BaseURL: "https://setup.example/v1", Model: "setup-model"}
	if err := s.add([]config.ProviderEntry{added}); err != nil {
		t.Fatal(err)
	}
	s.addProviderAccess([]config.ProviderEntry{added})

	external := config.LoadForEdit(path)
	external.Desktop.ProviderAccess = []string{}
	if err := external.SaveTo(path); err != nil {
		t.Fatal(err)
	}

	_, err := commitProviderSetupSession(s, path)
	var conflict *providerSetupConflictError
	if !errors.As(err, &conflict) || conflict.field != "desktop.provider_access" {
		t.Fatalf("commit conflict = %v, want provider_access declaration conflict", err)
	}
	declared, err := config.DesktopProviderAccessDeclared(path)
	if err != nil {
		t.Fatal(err)
	}
	got := config.LoadForEdit(path)
	if !declared || got.Desktop.ProviderAccess == nil || len(got.Desktop.ProviderAccess) != 0 {
		t.Fatalf("external explicit empty access was overwritten: declared=%v access=%#v", declared, got.Desktop.ProviderAccess)
	}
}

func TestProviderSetupOperationReplayMaterializesLatestProjectProviders(t *testing.T) {
	isolateUserConfig(t)
	path := filepath.Join(t.TempDir(), "reasonix.toml")
	initialBody := `
[[providers]]
name = "initial-project"
kind = "openai"
base_url = "https://initial.example/v1"
model = "initial-model"
api_key_env = ""
`
	if err := os.WriteFile(path, []byte(initialBody), 0o644); err != nil {
		t.Fatal(err)
	}
	working := config.LoadForEdit(path)
	s := newProviderSetupSessionForPath(working, path)
	added := config.ProviderEntry{Name: "setup-provider", Kind: "openai", BaseURL: "https://setup.example/v1", Model: "setup-model"}
	if err := s.add([]config.ProviderEntry{added}); err != nil {
		t.Fatal(err)
	}
	s.addProviderAccess([]config.ProviderEntry{added})

	externalBody := initialBody + `
[[providers]]
name = "external-project"
kind = "openai"
base_url = "https://external.example/v1"
model = "external-model"
api_key_env = ""
`
	if err := os.WriteFile(path, []byte(externalBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := commitProviderSetupSession(s, path); err != nil {
		t.Fatalf("commitProviderSetupSession: %v", err)
	}
	got := config.LoadForEditWithoutCredentials(path)
	for _, name := range []string{"initial-project", "external-project", "setup-provider"} {
		if _, ok := got.Provider(name); !ok || !containsString(got.Desktop.ProviderAccess, name) {
			t.Fatalf("latest project provider/access %q missing: providers=%v access=%v", name, got.Providers, got.Desktop.ProviderAccess)
		}
	}
	for _, forbidden := range []string{"deepseek", "deepseek-flash", "deepseek-pro"} {
		if containsString(got.Desktop.ProviderAccess, forbidden) {
			t.Fatalf("materialized project access unexpectedly enabled %q: %v", forbidden, got.Desktop.ProviderAccess)
		}
	}
}

func TestProviderSetupCommitDoesNotOverwriteMalformedConcurrentConfig(t *testing.T) {
	isolateUserConfig(t)
	path := config.UserConfigPath()
	initial := setupTestConfig()
	if err := initial.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	working := config.LoadForEdit(path)
	s := newProviderSetupSessionForPath(working, path)
	s.setLanguage("en")
	malformed := []byte("[[providers]\nname =")
	if err := os.WriteFile(path, malformed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := commitProviderSetupSession(s, path); err == nil {
		t.Fatal("commit should reject malformed concurrent config")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(malformed) {
		t.Fatalf("malformed concurrent config was overwritten:\n%s", after)
	}
}

func TestResolveSetupTargetsLocalKeepsGlobalCredentialTarget(t *testing.T) {
	targets := resolveSetupTargets([]string{"--local"})
	if targets.config != "reasonix.toml" {
		t.Fatalf("local config target = %q", targets.config)
	}
	if targets.env != config.CredentialsTargetDescription() {
		t.Fatalf("credential target = %q, want global %q", targets.env, config.CredentialsTargetDescription())
	}
}

func TestLocalSetupPersistsWorkspaceProviderAccess(t *testing.T) {
	cfg := setupTestConfig()
	cfg.Desktop.ProviderAccess = []string{"grok-relay"}
	path := filepath.Join(t.TempDir(), "reasonix.toml")
	if err := cfg.SaveTo(path); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "[desktop]") || !strings.Contains(text, `provider_access = ["grok-relay"]`) {
		t.Fatalf("local setup omitted workspace desktop access:\n%s", text)
	}
	if strings.Contains(text, "theme_style") || strings.Contains(text, "default_tool_approval_mode") {
		t.Fatalf("local setup leaked user-global desktop preferences:\n%s", text)
	}
}
