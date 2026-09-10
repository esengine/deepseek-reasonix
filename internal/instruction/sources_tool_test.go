package instruction

import (
	"context"
	"strings"
	"testing"
)

func TestSourcesToolReturnsExactActiveInstructionPaths(t *testing.T) {
	root := t.TempDir()
	userPath := root + "/reasonix-home/AGENTS.md"
	projectPath := root + "/workspace/AGENTS.md"
	tool := NewSourcesTool([]Document{
		{Path: userPath, Scope: ScopeUser, Directory: root + "/reasonix-home"},
		{Path: projectPath, Scope: ScopeProject, Directory: root + "/workspace"},
	})

	out, err := tool.Execute(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{userPath, projectPath, "scope=user", "scope=project", "exact host-provided paths", "do not infer"} {
		if !strings.Contains(out, want) {
			t.Fatalf("instruction source output missing %q:\n%s", want, out)
		}
	}
	if !tool.ReadOnly() {
		t.Fatal("instruction_sources must remain read-only")
	}
}

func TestSourcesToolReportsNoActiveInstructions(t *testing.T) {
	out, err := NewSourcesTool(nil).Execute(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "No standing instruction files are active." {
		t.Fatalf("empty output = %q", out)
	}
}
