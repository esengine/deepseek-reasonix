package skill

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type recordingSubagentRunner struct {
	called bool
	task   string
}

func (r *recordingSubagentRunner) run(context.Context, Skill, string, SubagentRunOptions) (string, error) {
	r.called = true
	return "ok", nil
}

func subagentSkillHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	writeSkill(t, home, ".reasonix/skills/explore.md", "---\ndescription: explore\nrunAs: subagent\n---\nbody")
	return home
}

// The capability proxy types arguments as an object, but models sometimes
// emit a JSON-encoded string of that object — exactly the #8472 payload:
//
//	arguments: "{\"name\": \"explore\", \"arguments\": \"...\"}"
//
// run_skill used to fail that with "json: cannot unmarshal string into Go
// value of type struct { Name …; Arguments … }", killing every explore call.
func TestRunSkillAcceptsStringWrappedObjectArgs(t *testing.T) {
	runner := &recordingSubagentRunner{}
	tl := NewRunSkillTool(New(Options{HomeDir: subagentSkillHome(t), DisableBuiltins: true}), runner.run)

	inner := map[string]any{"name": "explore", "arguments": "survey the codebase"}
	wrapped, err := json.Marshal(inner)
	if err != nil {
		t.Fatal(err)
	}
	doubleEncoded, err := json.Marshal(string(wrapped))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := tl.Execute(context.Background(), doubleEncoded); err != nil {
		t.Fatalf("string-wrapped object args must run: %v", err)
	}
	if !runner.called {
		t.Fatal("subagent did not run")
	}
}

// Plain-object args keep working byte-for-byte.
func TestRunSkillKeepsObjectArgs(t *testing.T) {
	runner := &recordingSubagentRunner{}
	tl := NewRunSkillTool(New(Options{HomeDir: subagentSkillHome(t), DisableBuiltins: true}), runner.run)

	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"name":"explore","arguments":"look around"}`)); err != nil {
		t.Fatalf("object args: %v", err)
	}
	if !runner.called {
		t.Fatal("subagent did not run")
	}
}

// A plain string (not JSON-of-object) is left alone: it is not a wrapped
// object, and silently reinterpreting it would mask genuine misuse.
func TestUnwrapIgnoresNonObjectStrings(t *testing.T) {
	for _, raw := range []string{`"just text"`, `""`, `123`, `null`, `{"name":"explore"}`, ``} {
		if got := string(unwrapJSONStringArgs([]byte(raw))); got != raw {
			t.Fatalf("unwrapJSONStringArgs(%q) = %q, want unchanged", raw, got)
		}
	}
	if got := string(unwrapJSONStringArgs([]byte(`"{\"name\":\"explore\"}"`))); !strings.HasPrefix(got, "{") {
		t.Fatalf("wrapped object not unwrapped: %q", got)
	}
}
