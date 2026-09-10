package skill

import (
	"slices"
	"testing"
)

func TestMapClaudeAgentToolsCoversClaudeCodeNames(t *testing.T) {
	in := []string{
		"Read", "Write", "Edit", "MultiEdit", "TodoWrite", "NotebookEdit",
		"AskUserQuestion", "WebFetch", "WebSearch", "Bash", "Grep", "Glob", "LS",
		"read_file", "mcp__github__search", "Read",
	}
	want := []string{
		"read_file", "write_file", "edit_file", "multi_edit", "todo_write",
		"notebook_edit", "ask", "web_fetch", "web_search", "bash", "grep",
		"glob", "ls", "mcp__github__search",
	}
	if got := mapClaudeAgentTools(in); !slices.Equal(got, want) {
		t.Fatalf("mapped tools = %v, want %v", got, want)
	}
}

func TestClaudeRootTranslatesToolsAndClearsModelAlias(t *testing.T) {
	proj := t.TempDir()
	writeSkill(t, proj, ".claude/skills/probe/SKILL.md",
		"---\ndescription: probe\ncontext: fork\nmodel: sonnet\nallowed-tools:\n  - Read\n  - Grep\n  - AskUserQuestion\n---\nbody")

	st := New(Options{HomeDir: t.TempDir(), ProjectRoot: proj, DisableBuiltins: true})
	sk, ok := st.Read("probe")
	if !ok {
		t.Fatal("probe not found")
	}
	want := []string{"read_file", "grep", "ask"}
	if !slices.Equal(sk.AllowedTools, want) {
		t.Fatalf("allowed tools = %v, want %v", sk.AllowedTools, want)
	}
	if sk.Model != "" {
		t.Fatalf("model alias should clear, got %q", sk.Model)
	}
	if sk.RunAs != RunSubagent {
		t.Fatalf("runAs = %v, want subagent from its own frontmatter", sk.RunAs)
	}
	if sk.Invocation == "manual" {
		t.Fatal("invocation must not be forced to manual for .claude skills")
	}
}

func TestClaudeRootKeepsInlineRunAs(t *testing.T) {
	proj := t.TempDir()
	writeSkill(t, proj, ".claude/skills/flat/SKILL.md",
		"---\ndescription: flat probe\nallowed-tools: [Edit, WebFetch]\nmodel: deepseek-pro\n---\nbody")

	st := New(Options{HomeDir: t.TempDir(), ProjectRoot: proj, DisableBuiltins: true})
	sk, ok := st.Read("flat")
	if !ok {
		t.Fatal("flat not found")
	}
	if sk.RunAs != RunInline {
		t.Fatalf("runAs = %v, want inline", sk.RunAs)
	}
	if want := []string{"edit_file", "web_fetch"}; !slices.Equal(sk.AllowedTools, want) {
		t.Fatalf("allowed tools = %v, want %v", sk.AllowedTools, want)
	}
	if sk.Model != "deepseek-pro" {
		t.Fatalf("non-alias model must survive, got %q", sk.Model)
	}
}

func TestCustomRootKeepsClaudeNamesUntranslated(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "probe/SKILL.md",
		"---\ndescription: probe\ncontext: fork\nmodel: sonnet\nallowed-tools:\n  - Read\n  - AskUserQuestion\n---\nbody")

	st := New(Options{HomeDir: t.TempDir(), CustomPaths: []string{root}, DisableBuiltins: true})
	sk, ok := st.Read("probe")
	if !ok {
		t.Fatal("probe not found")
	}
	if want := []string{"Read", "AskUserQuestion"}; !slices.Equal(sk.AllowedTools, want) {
		t.Fatalf("allowed tools = %v, want %v", sk.AllowedTools, want)
	}
	if sk.Model != "sonnet" {
		t.Fatalf("custom-root model must stay, got %q", sk.Model)
	}
}
