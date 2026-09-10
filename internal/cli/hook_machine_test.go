package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/hook"
)

func TestHookMachineListRedactsCommandsAndEnablesProjectHooks(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	projectSettings := filepath.Join(root, ".reasonix", "settings.json")
	if err := os.MkdirAll(filepath.Dir(projectSettings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectSettings, []byte(`{"hooks":{"PreToolUse":[{"match":"bash","command":"printf PRIVATE_COMMAND"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	globalSettings := filepath.Join(home, ".reasonix", "settings.json")
	if err := os.MkdirAll(filepath.Dir(globalSettings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalSettings, []byte(`{"hooks":{"Stop":[{"command":"PRIVATE_GLOBAL_COMMAND"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := runHookCommand([]string{"list", "--json", "--project-root", root, "--home-dir", home}, &out); code != 0 {
		t.Fatalf("hook list exit code = %d, output = %s", code, out.String())
	}
	var response machineHookList
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatalf("decode hook list: %v", err)
	}
	if len(response.Hooks) != 2 {
		t.Fatalf("hooks = %+v", response.Hooks)
	}
	if response.Hooks[0].Event != "PreToolUse" || response.Hooks[0].Status != "active" {
		t.Errorf("project hook = %+v", response.Hooks[0])
	}
	if response.Hooks[1].Event != "Stop" || response.Hooks[1].Status != "active" {
		t.Errorf("global hook = %+v", response.Hooks[1])
	}
	if strings.Contains(out.String(), "PRIVATE") || strings.Contains(out.String(), root) || strings.Contains(out.String(), home) {
		t.Fatalf("hook output leaked private data: %s", out.String())
	}
}

func TestHookMachineListReportsExecutability(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	globalSettings := filepath.Join(home, ".reasonix", "settings.json")
	if err := os.MkdirAll(filepath.Dir(globalSettings), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"hooks":{` +
		`"PreToolUse":[{"match":"bash","command":""},{"match":"[","command":"PRIVATE_INVALID_MATCHER"}],` +
		`"Stop":[{"match":"[","command":"PRIVATE_NON_TOOL"}],` +
		`"UnknownEvent":[{"command":"PRIVATE_UNKNOWN_EVENT"}]` +
		`}}`
	if err := os.WriteFile(globalSettings, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := runHookCommand([]string{"list", "--json", "--project-root", root, "--home-dir", home}, &out); code != 0 {
		t.Fatalf("hook list exit code = %d, output = %s", code, out.String())
	}
	var response machineHookList
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatalf("decode hook list: %v", err)
	}
	statuses := map[string]string{}
	for _, item := range response.Hooks {
		statuses[item.Event+"|"+item.Match] = item.Status
	}
	want := map[string]string{
		"PreToolUse|bash": "invalid",
		"PreToolUse|[":    "invalid",
		"Stop|[":          "active",
		"UnknownEvent|*":  "invalid",
	}
	if len(statuses) != len(want) {
		t.Fatalf("statuses = %+v, want %+v", statuses, want)
	}
	for key, wantStatus := range want {
		if got := statuses[key]; got != wantStatus {
			t.Errorf("status[%q] = %q, want %q", key, got, wantStatus)
		}
	}
	if strings.Contains(out.String(), "PRIVATE") {
		t.Fatalf("hook output leaked command content: %s", out.String())
	}
}

func TestHookMachineEntryStatusRejectsNonRegularContextFile(t *testing.T) {
	contextDir := t.TempDir()
	entry := hook.Entry{
		Event:       hook.SessionStart,
		Scope:       hook.ScopePlugin,
		ContextFile: contextDir,
	}
	if got, _ := machineHookEntryStatus(entry); got != "invalid" {
		t.Fatalf("directory context status = %q, want invalid", got)
	}

	contextFile := filepath.Join(contextDir, "context.md")
	if err := os.WriteFile(contextFile, []byte("plugin context"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry.ContextFile = contextFile
	if got, _ := machineHookEntryStatus(entry); got != "active" {
		t.Fatalf("readable regular context status = %q, want active", got)
	}
}

func TestHookMachineStatusHasStableRedactedSources(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	var out bytes.Buffer
	if code := runHookCommand([]string{"status", "--json", "--project-root", root, "--home-dir", home}, &out); code != 0 {
		t.Fatalf("hook status exit code = %d, output = %s", code, out.String())
	}
	var response machineHookStatus
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatalf("decode hook status: %v", err)
	}
	if response.SchemaVersion != machineSchemaVersion || response.Command != "hook.status" || !response.TrustedProject {
		t.Fatalf("status = %+v", response)
	}
	if len(response.Sources) != 2 {
		t.Fatalf("sources = %+v", response.Sources)
	}
	if response.Sources[0].Scope != "global" || response.Sources[1].Scope != "project" {
		t.Fatalf("sources are not stable: %+v", response.Sources)
	}
}

// TestHookMachineListFindsGlobalHooksWithoutHomeDir: with no --home-dir, the
// hook machine resolves global hooks from the platform Reasonix home
// (config.ReasonixHomeDir → settings.json), not a doubled .reasonix segment
// (#7420). REASONIX_HOME is set explicitly so the resolved path is
// deterministic regardless of the OS user-config lookup.
func TestHookMachineListFindsGlobalHooksWithoutHomeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	settingsPath := filepath.Join(home, "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"hooks":{"Stop":[{"command":"PRIVATE_GLOBAL"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := runHookCommand([]string{"list", "--json", "--project-root", t.TempDir()}, &out); code != 0 {
		t.Fatalf("hook list exit code = %d, output = %s", code, out.String())
	}
	var response machineHookList
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatalf("decode hook list: %v", err)
	}
	for _, h := range response.Hooks {
		if h.Event == "Stop" && h.Status == "active" {
			return
		}
	}
	t.Fatalf("global Stop hook not found active: %+v", response.Hooks)
}
