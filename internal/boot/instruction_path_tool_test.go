package boot

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/config"
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

func TestBuildMemoryToolReportsExactActiveInstructionPaths(t *testing.T) {
	isolateConfigHome(t)
	dir := robustTempDir(t)
	t.Chdir(dir)
	userInstructionPath := filepath.Join(config.MemoryUserDir(), "AGENTS.md")
	writeFile(t, config.MemoryUserDir(), "AGENTS.md", "Always report the configured Reasonix instruction path exactly.")
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[[providers]]
name = "test-model"
kind = "boot-retrieval-tool-test"
model = "x"
`)

	registerBootRetrievalToolTestProvider()
	prov := testutil.NewMock("boot-retrieval-tool-test",
		testutil.Turn{ToolCalls: []provider.ToolCall{{
			ID: "instructions-1", Name: "use_capability",
			Arguments: `{"action":"call","capability_id":"tool:instruction_sources","arguments":{}}`,
		}}},
		testutil.Turn{Text: "done"},
	)
	setBootRetrievalToolTestProvider(t, prov)

	ctrl, err := Build(context.Background(), Options{Sink: event.Discard, SessionDir: filepath.Join(t.TempDir(), "sessions")})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()

	sys := systemMessage(ctrl.History())
	if strings.Contains(sys, userInstructionPath) {
		t.Fatalf("cache-stable prompt exposed exact host path %q:\n%s", userInstructionPath, sys)
	}
	if !strings.Contains(sys, "user/AGENTS.md") {
		t.Fatalf("cache-stable prompt missing provider-relative instruction label:\n%s", sys)
	}
	if !strings.Contains(sys, "instruction_sources capability") {
		t.Fatalf("instruction prompt does not route exact-path questions to instruction_sources:\n%s", sys)
	}

	if err := ctrl.Run(context.Background(), "Where is the active global instruction file?"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var combined string
	for _, msg := range ctrl.History() {
		if msg.Role == provider.RoleTool {
			combined += "\n" + msg.Content
		}
	}
	if !strings.Contains(combined, userInstructionPath) || !strings.Contains(combined, "exact host-provided paths") {
		t.Fatalf("instruction tool result did not include exact active path %q:\n%s", userInstructionPath, combined)
	}
}
