package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/agent"
	"reasonix/internal/command"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/skill"
)

// writeAt creates dir/rel (with parents) holding content, for fs-backed tests.
func writeAt(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCustomCommandHintIdentifiesPluginSource(t *testing.T) {
	got := customCommandHint(command.Command{Description: "Create a plan", Plugin: "pwf", ShortName: "plan"})
	if got != "plugin pwf · Create a plan" {
		t.Fatalf("customCommandHint = %q", got)
	}
	if got := customCommandHint(command.Command{Description: "Project plan"}); got != "Project plan" {
		t.Fatalf("project hint changed = %q", got)
	}
}

func TestSlashCompletionFilterAndAccept(t *testing.T) {
	m := newTestChatTUI()
	m.input.SetValue("/co")
	m.updateCompletion()

	if !m.completion.active || m.completion.kind != compSlash {
		t.Fatalf("typing /co should open the slash menu: %+v", m.completion)
	}
	// The common commands keep their stable order; explicit readiness recovery
	// is discoverable after them and fails safely when no card is pending.
	want := []string{"/compact", "/context", "/copy", "/continue-checks", "/recover-context"}
	if got := labels(m.completion.items); !slices.Equal(got, want) {
		t.Fatalf("filter = %v, want %v", got, want)
	}

	m.acceptCompletion()
	if got := m.input.Value(); got != "/compact " {
		t.Errorf("accept should fill the input, got %q", got)
	}
	if m.completion.active {
		t.Error("menu should close after accept")
	}
}

func TestSlashCompletionIncludesCustomCommands(t *testing.T) {
	m := newTestChatTUI()
	m.commands = []command.Command{{Name: "review", Description: "review the diff"}}
	m.input.SetValue("/re")
	m.updateCompletion()

	if !hasLabel(m.completion.items, "/review") {
		t.Errorf("custom command should appear in completion: %v", labels(m.completion.items))
	}
}

func TestSlashCompletionDocsShowsOnlyRuntimeWinner(t *testing.T) {
	tests := []struct {
		name     string
		commands []command.Command
		skills   []skill.Skill
		wantHint string
	}{
		{
			name:     "custom command shadows builtin",
			commands: []command.Command{{Name: "docs", Description: "custom docs"}},
			wantHint: "custom docs",
		},
		{
			name:     "skill shadows builtin",
			skills:   []skill.Skill{{Name: "docs", Description: "docs skill"}},
			wantHint: "docs skill",
		},
		{
			name:     "custom command shadows skill and builtin",
			commands: []command.Command{{Name: "docs", Description: "custom docs"}},
			skills:   []skill.Skill{{Name: "docs", Description: "docs skill"}},
			wantHint: "custom docs",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestChatTUI()
			m.commands = tt.commands
			m.skills = tt.skills
			var docs []compItem
			for _, item := range m.slashItems() {
				if item.label == "/docs" {
					docs = append(docs, item)
				}
			}
			if len(docs) != 1 || docs[0].hint != tt.wantHint {
				t.Fatalf("/docs completion entries = %+v, want one entry with hint %q", docs, tt.wantHint)
			}
			if !hasLabel(m.slashItems(), "/reasonix:docs") {
				t.Fatalf("shadowed built-in docs fallback missing: %v", labels(m.slashItems()))
			}
		})
	}
}

func TestSlashCompletionDocsAccountsForHiddenCompatibilityAliases(t *testing.T) {
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
			wantCanonical: "/manuals:docs",
		},
		{
			name:          "compatible plugin skill alias",
			skills:        []skill.Skill{{Name: "docs", Plugin: "manuals"}},
			wantCanonical: "/manuals:docs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestChatTUI()
			m.commands = tt.commands
			m.skills = tt.skills
			items := m.slashItems()
			if hasLabel(items, "/docs") {
				t.Fatalf("hidden runtime owner left a misleading /docs entry: %v", labels(items))
			}
			for _, want := range []string{"/reasonix:docs", tt.wantCanonical} {
				if !hasLabel(items, want) {
					t.Fatalf("completion missing %q: %v", want, labels(items))
				}
			}
		})
	}
}

func TestSlashCompletionDocsDoesNotDisplaceQualifiedCustomCommands(t *testing.T) {
	m := newTestChatTUI()
	m.commands = []command.Command{
		{Name: "docs", Description: "custom docs"},
		{Name: "reasonix:docs", Description: "qualified custom docs"},
		{Name: "reasonix:builtin:docs", Description: "second qualified custom docs"},
	}
	items := m.slashItems()
	for _, want := range []string{"/docs", "/reasonix:docs", "/reasonix:builtin:docs", "/reasonix:builtin:docs:2"} {
		if !hasLabel(items, want) {
			t.Fatalf("completion displaced %q: %v", want, labels(items))
		}
	}
}

func TestCompletionClosesOnSpaceAndNonMatch(t *testing.T) {
	m := newTestChatTUI()

	m.input.SetValue("/compact ") // space → typing args, not naming a command
	m.updateCompletion()
	if m.completion.active {
		t.Error("menu should close once a space is typed (now entering args)")
	}

	m.input.SetValue("/zzz") // no command matches
	m.updateCompletion()
	if m.completion.active {
		t.Error("menu should close when nothing matches")
	}

	m.input.SetValue("hello") // not a slash line
	m.updateCompletion()
	if m.completion.active {
		t.Error("menu should be inactive for non-slash input")
	}
}

func TestMoveCompletionWraps(t *testing.T) {
	m := newTestChatTUI()
	m.completion = completion{active: true, kind: compSlash, items: []compItem{{label: "/a"}, {label: "/b"}, {label: "/c"}}, sel: 0}
	m.moveCompletion(-1)
	if m.completion.sel != 2 {
		t.Errorf("up from first should wrap to last, got %d", m.completion.sel)
	}
	m.moveCompletion(1)
	if m.completion.sel != 0 {
		t.Errorf("down from last should wrap to first, got %d", m.completion.sel)
	}
}

func TestActiveAtToken(t *testing.T) {
	cases := []struct {
		val     string
		wantTok string
		wantOK  bool
		wantAt  int
	}{
		{"@", "", true, 0},
		{"look at @src/m", "src/m", true, 8},
		{"@internal/agent/", "internal/agent/", true, 0},
		{"a@b.com", "", false, 0},  // '@' not whitespace-preceded → not a ref
		{"@foo bar", "", false, 0}, // cursor token after the space isn't an @ref
		{"plain text", "", false, 0},
		{`@docs/my\ file.md`, `docs/my\ file.md`, true, 0}, // escaped space stays in the token
		{`see @my\ dir/`, `my\ dir/`, true, 4},
	}
	for _, c := range cases {
		at, end, tok, ok := activeAtToken(c.val, len(c.val))
		if ok != c.wantOK || (ok && (tok != c.wantTok || at != c.wantAt)) {
			t.Errorf("activeAtToken(%q) = (%d,%d,%q,%v), want (%d,_,%q,%v)", c.val, at, end, tok, ok, c.wantAt, c.wantTok, c.wantOK)
		}
		if ok {
			if end < at || end > len(c.val) || !strings.HasPrefix(c.val[at:end], "@") {
				t.Errorf("activeAtToken(%q) span [%d,%d) invalid", c.val, at, end)
			}
			// At EOF, caret-limited query equals the full token after '@'.
			fullTok := c.val[at+1 : end]
			if !strings.HasPrefix(fullTok, tok) {
				t.Errorf("activeAtToken(%q) query %q is not a prefix of full token %q", c.val, tok, fullTok)
			}
		}
	}
}

// TestFileItemsEscapedSpaces verifies names with spaces complete as
// escaped @tokens and that completion can descend through such a directory:
// the escaped token is unescaped for filesystem reads.
func TestFileItemsEscapedSpaces(t *testing.T) {
	dir := t.TempDir()
	writeAt(t, dir, "my file.md", "x")
	writeAt(t, dir, "my dir/inner.md", "y")

	m := newTestChatTUI()
	items := m.fileItems(dir + "/")
	wantFile := "@" + dir + `/my\ file.md`
	wantDir := "@" + dir + `/my\ dir/`
	var gotFile, gotDir bool
	for _, it := range items {
		gotFile = gotFile || it.insert == wantFile
		gotDir = gotDir || it.insert == wantDir
	}
	if !gotFile || !gotDir {
		t.Fatalf("inserts should escape spaces, want %q and %q in %v", wantFile, wantDir, labels(items))
	}

	deeper := m.fileItems(dir + `/my\ dir/`)
	if !hasLabel(deeper, "inner.md") {
		t.Fatalf("descending through an escaped dir should list its entries, got %v", labels(deeper))
	}
}

func TestSplitPathToken(t *testing.T) {
	cases := []struct{ in, dir, frag string }{
		{"main", "", "main"},
		{"internal/age", "internal/", "age"},
		{"a/b/c", "a/b/", "c"},
		{"internal/", "internal/", ""},
	}
	for _, c := range cases {
		if d, f := splitPathToken(c.in); d != c.dir || f != c.frag {
			t.Errorf("splitPathToken(%q) = (%q,%q), want (%q,%q)", c.in, d, f, c.dir, c.frag)
		}
	}
}

// TestFileItemsOneLevel verifies @ completion lists exactly one directory level
// (no recursion): a subdir shows as a descendable entry, its contents do not.
func TestFileItemsOneLevel(t *testing.T) {
	dir := t.TempDir()
	writeAt(t, dir, "alpha.go", "x")
	writeAt(t, dir, "sub/deep.go", "y") // creates sub/ with a file inside
	writeAt(t, dir, ".hidden", "z")

	m := newTestChatTUI()
	items := m.fileItems(dir + "/") // token = "<tmp>/", frag = ""

	if !hasLabel(items, "alpha.go") {
		t.Errorf("file alpha.go should be listed: %v", labels(items))
	}
	if !hasLabel(items, "sub/") {
		t.Errorf("subdir should be listed as 'sub/': %v", labels(items))
	}
	if hasLabel(items, "deep.go") {
		t.Errorf("nested file deep.go must NOT be listed (one level only): %v", labels(items))
	}
	if hasLabel(items, ".hidden") {
		t.Errorf("hidden file should be skipped unless frag starts with '.': %v", labels(items))
	}
	// The subdir entry must be a descend (accepting it navigates into it).
	for _, it := range items {
		if it.label == "sub/" && !it.descend {
			t.Error("directory entry should be a descend")
		}
	}
}

func TestFileItemsSubdirUsesWorkspaceRoot(t *testing.T) {
	cwd := t.TempDir()
	workspace := t.TempDir()
	writeAt(t, cwd, "src/cwd.go", "wrong")
	writeAt(t, workspace, "src/workspace.go", "right")

	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}

	m := newTestChatTUI()
	m.ctrl = control.New(control.Options{SessionDir: t.TempDir(), WorkspaceRoot: workspace})
	items := m.fileItems("src/")

	if !hasLabel(items, "workspace.go") {
		t.Fatalf("workspace file should be listed for @src/: %v", labels(items))
	}
	if hasLabel(items, "cwd.go") {
		t.Fatalf("cwd file should not leak into workspace completion: %v", labels(items))
	}
}

func TestFileItemsSearchesBasenameAtTopLevel(t *testing.T) {
	orig, _ := os.Getwd()
	defer os.Chdir(orig)

	dir := t.TempDir()
	writeAt(t, dir, "frontend/wailsjs/runtime/runtime.js", "x")
	writeAt(t, dir, "node_modules/pkg/runtime.js", "noise")
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	m := newTestChatTUI()
	items := m.fileItems("runtime.js")

	if !hasLabel(items, "frontend/wailsjs/runtime/runtime.js") {
		t.Fatalf("top-level @runtime.js should offer nested file path, got %v", labels(items))
	}
	if hasLabel(items, "node_modules/pkg/runtime.js") {
		t.Fatalf("file search should skip node_modules noise, got %v", labels(items))
	}
}

func TestFileItemsSearchRespectsMenuCap(t *testing.T) {
	orig, _ := os.Getwd()
	defer os.Chdir(orig)

	dir := t.TempDir()
	for i := range maxCompItems {
		writeAt(t, dir, filepath.Join("aa-dir-"+fmt.Sprintf("%03d", i), "file.txt"), "x")
	}
	writeAt(t, dir, "nested/aa-deep.js", "y")
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	m := newTestChatTUI()
	items := m.fileItems("aa")

	if len(items) != maxCompItems {
		t.Fatalf("fileItems should stay capped at %d entries, got %d", maxCompItems, len(items))
	}
	if hasLabel(items, "nested/aa-deep.js") {
		t.Fatalf("search result should not exceed capped menu: %v", labels(items))
	}
}

func TestFileItemsHiddenWhenDotTyped(t *testing.T) {
	dir := t.TempDir()
	writeAt(t, dir, ".hidden", "z")
	m := newTestChatTUI()
	items := m.fileItems(dir + "/.") // frag = "." → show hidden
	if !hasLabel(items, ".hidden") {
		t.Errorf("hidden file should appear when frag starts with '.': %v", labels(items))
	}
}

// TestSlashArgCompletionMCPSubcommands proves explicit help syntax opens the
// subcommand menu; a bare trailing space stays submit-ready.
func TestSlashArgCompletionMCPSubcommands(t *testing.T) {
	m := newTestChatTUI()
	m.input.SetValue("/mcp?")
	m.updateCompletion()
	if !m.completion.active || m.completion.kind != compSlashArg {
		t.Fatalf("/mcp? should open the argument menu: %+v", m.completion)
	}
	for _, want := range []string{"add", "connect", "remove", "show", "tools", "import"} {
		if !hasLabel(m.completion.items, want) {
			t.Errorf("subcommand %q missing: %v", want, labels(m.completion.items))
		}
	}
	if hasLabel(m.completion.items, "list") {
		t.Errorf("redundant list subcommand should be hidden from /mcp? menu: %v", labels(m.completion.items))
	}
	m.acceptCompletion()
	if got := m.input.Value(); got != "/mcp add " {
		t.Fatalf("accepting /mcp? subcommand should replace ? with command, got %q", got)
	}

	m.input.SetValue("/mcp ")
	m.updateCompletion()
	if m.completion.active {
		t.Fatalf("/mcp <space> should not open the argument menu: %+v", m.completion)
	}
}

// TestSlashArgCompletionMCPFilterAndAccept proves the typed prefix filters the
// subcommands and that accepting replaces only the current token (not the line).
func TestSlashArgCompletionMCPFilterAndAccept(t *testing.T) {
	m := newTestChatTUI()
	m.input.SetValue("/mcp re")
	m.updateCompletion()
	if len(m.completion.items) != 1 || m.completion.items[0].label != "remove" {
		t.Fatalf("/mcp re should filter to remove, got %v", labels(m.completion.items))
	}
	m.acceptCompletion()
	if got := m.input.Value(); got != "/mcp remove " {
		t.Errorf("accept should replace just the token, got %q want %q", got, "/mcp remove ")
	}
}

// TestSlashArgCompletionMCPAddFlags proves add offers transport flags once the
// token starts with "-", and stays quiet for the free-form server name.
func TestSlashArgCompletionMCPAddFlags(t *testing.T) {
	m := newTestChatTUI()
	m.input.SetValue("/mcp add myserver --h")
	m.updateCompletion()
	if !hasLabel(m.completion.items, "--http") {
		t.Errorf("--h should offer --http: %v", labels(m.completion.items))
	}

	m.input.SetValue("/mcp add my")
	m.updateCompletion()
	if m.completion.active {
		t.Error("the free-form server name should not open a menu")
	}
}

// TestSlashCompletionMCPDoesNotAutoDescend proves accepting "/mcp" keeps the
// bare command submit-ready; only an explicitly typed trailing space opens the
// subcommand menu.
func TestSlashCompletionMCPDoesNotAutoDescend(t *testing.T) {
	m := newTestChatTUI()
	m.input.SetValue("/mcp")
	m.updateCompletion()
	m.acceptCompletion()
	if got := m.input.Value(); got != "/mcp" {
		t.Fatalf("accepting /mcp should keep %q, got %q", "/mcp", got)
	}
	if m.completion.active {
		t.Fatalf("accepting /mcp should not chain into the subcommand menu: %+v", m.completion)
	}
}

func TestEnterOnExactMCPSubmitsManager(t *testing.T) {
	isolateUserConfig(t)
	m := newTestChatTUI()
	m.input.SetValue("/mcp")
	m.updateCompletion()
	if !m.completion.active {
		t.Fatal("typing /mcp should show slash completion before Enter")
	}
	if m.completion.kind == compSlashArg {
		t.Fatalf("typing exact /mcp should not open subcommand completion: %+v", m.completion)
	}

	got, _ := m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	next := got.(chatTUI)
	if next.mcp == nil || next.mcp.stage != mcpStageList {
		t.Fatalf("Enter on exact /mcp should open manager, got %#v", next.mcp)
	}
}

func TestEnterOnMCPWithTrailingSpaceSubmitsManager(t *testing.T) {
	isolateUserConfig(t)
	m := newTestChatTUI()
	m.input.SetValue("/mcp ")
	m.updateCompletion()
	if m.completion.active {
		t.Fatalf("/mcp <space> should stay submit-ready before Enter: %+v", m.completion)
	}

	got, _ := m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	next := got.(chatTUI)
	if next.mcp == nil || next.mcp.stage != mcpStageList {
		t.Fatalf("Enter on bare /mcp arg menu should open manager, got %#v", next.mcp)
	}
}

func TestEnterOnExactSlashArgSubmitsWhenPrefixAlsoMatches(t *testing.T) {
	m := newTestChatTUI()
	m.ctrl = control.New(control.Options{SessionDir: t.TempDir()})
	m.input.SetValue("/resume 1")
	m.completion = completion{
		active:      true,
		kind:        compSlashArg,
		items:       []compItem{{label: "1", insert: "1"}, {label: "10", insert: "10"}},
		sel:         0,
		replaceFrom: len("/resume "),
		replaceTo:   len("/resume 1"),
	}

	got, _ := m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	next := got.(chatTUI)
	if next.completion.active {
		t.Fatalf("Enter on exact selected arg should close completion: %+v", next.completion)
	}
	if got := next.input.Value(); got != "" {
		t.Fatalf("Enter on exact selected arg should submit command, input=%q", got)
	}
}

// TestSlashArgCompletionRemoveNoHost proves "/mcp remove " stays closed when no
// servers are connected (nothing to suggest), rather than showing an empty box.
func TestSlashArgCompletionRemoveNoHost(t *testing.T) {
	m := newTestChatTUI()
	m.input.SetValue("/mcp remove ")
	m.updateCompletion()
	if m.completion.active {
		t.Error("remove with no connected servers should not open a menu")
	}
}

func TestSlashArgCompletionSwitchBranches(t *testing.T) {
	dir := t.TempDir()
	exec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
	exec.Session().Add(provider.Message{Role: provider.RoleUser, Content: "root prompt"})
	ctrl := control.New(control.Options{Executor: exec, SessionDir: dir, Label: "test"})
	rootPath := filepath.Join(dir, "root.jsonl")
	ctrl.SetSessionPath(rootPath)
	if err := ctrl.Snapshot(); err != nil {
		t.Fatal(err)
	}

	child := agent.NewSession("sys")
	child.Add(provider.Message{Role: provider.RoleUser, Content: "child prompt"})
	childPath := filepath.Join(dir, "child.jsonl")
	if err := child.Save(childPath); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMeta(childPath, agent.BranchMeta{Name: "experiment", ParentID: agent.BranchID(rootPath)}); err != nil {
		t.Fatal(err)
	}
	pending := agent.NewSession("sys")
	pending.Add(provider.Message{Role: provider.RoleUser, Content: "pending child prompt"})
	pendingPath := filepath.Join(dir, "pending.jsonl")
	if err := pending.Save(pendingPath); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMeta(pendingPath, agent.BranchMeta{Name: "exp-pending", ParentID: agent.BranchID(rootPath)}); err != nil {
		t.Fatal(err)
	}
	if err := agent.MarkCleanupPending(pendingPath, "delete"); err != nil {
		t.Fatal(err)
	}

	m := newTestChatTUI()
	m.ctrl = ctrl
	m.input.SetValue("/switch exp")
	m.updateCompletion()
	if !m.completion.active || m.completion.kind != compSlashArg {
		t.Fatalf("/switch should open branch completion: %+v", m.completion)
	}
	if len(m.completion.items) != 1 || m.completion.items[0].label != "child" {
		t.Fatalf("branch completion = %v, want child", labels(m.completion.items))
	}
}

func TestSlashArgCompletionLanguage(t *testing.T) {
	m := newTestChatTUI()
	m.input.SetValue("/language ")
	m.updateCompletion()
	if !m.completion.active || m.completion.kind != compSlashArg {
		t.Fatalf("/language should open arg completion: %+v", m.completion)
	}
	for _, want := range []string{"auto", "en", "zh", "es"} {
		if !hasLabel(m.completion.items, want) {
			t.Fatalf("/language completion missing %q: %v", want, labels(m.completion.items))
		}
	}
}

func TestSlashArgCompletionCurrency(t *testing.T) {
	m := newTestChatTUI()
	m.input.SetValue("/currency ")
	m.updateCompletion()
	if !m.completion.active || m.completion.kind != compSlashArg {
		t.Fatalf("/currency should open arg completion: %+v", m.completion)
	}
	for _, want := range []string{"auto", "CNY", "USD"} {
		if !hasLabel(m.completion.items, want) {
			t.Fatalf("/currency completion missing %q: %v", want, labels(m.completion.items))
		}
	}
}

func TestSlashArgCompletionReasoningLanguage(t *testing.T) {
	m := newTestChatTUI()
	m.input.SetValue("/reasoning-language ")
	m.updateCompletion()
	if !m.completion.active || m.completion.kind != compSlashArg {
		t.Fatalf("/reasoning-language should open arg completion: %+v", m.completion)
	}
	for _, want := range []string{"auto", "zh", "en"} {
		if !hasLabel(m.completion.items, want) {
			t.Fatalf("/reasoning-language completion missing %q: %v", want, labels(m.completion.items))
		}
	}
	if hasLabel(m.completion.items, "中文") {
		t.Fatalf("/reasoning-language completion should expose only auto|zh|en: %v", labels(m.completion.items))
	}
}

func labels(items []compItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.label
	}
	return out
}

func hasLabel(items []compItem, label string) bool {
	for _, it := range items {
		if it.label == label {
			return true
		}
	}
	return false
}

// fuzzy matching for / completion

// TestFuzzyFilterSlashSubsequence proves the slash-menu fuzzy filter matches
// command labels whose letters appear in order, even when they are not a
// prefix: /mdl should surface /model (m-o-d-l) without also pulling in /mcp
// (m-c-p is not a subsequence of m-d-l).
func TestFuzzyFilterSlashSubsequence(t *testing.T) {
	m := newTestChatTUI()
	m.input.SetValue("/mdl")
	m.updateCompletion()

	if !m.completion.active {
		t.Fatal("menu should open on a partial / token")
	}
	if !hasLabel(m.completion.items, "/model") {
		t.Errorf("/model should match /mdl as a subsequence: %v", labels(m.completion.items))
	}
	if hasLabel(m.completion.items, "/mcp") {
		t.Errorf("/mcp should NOT match /mdl (m-c-p is not a subsequence of m-d-l): %v", labels(m.completion.items))
	}
}

// TestFuzzyFilterSlashPrefixFirst proves prefix hits rank ahead of
// subsequence-only hits, matching the menu behavior we want: typing "/mo"
// should put /model and /mouse (both true "/mo" prefixes) at the top, not
// buried after non-prefix matches.
func TestFuzzyFilterSlashPrefixFirst(t *testing.T) {
	m := newTestChatTUI()
	m.input.SetValue("/mo")
	m.updateCompletion()

	if !m.completion.active {
		t.Fatal("menu should open for /mo")
	}
	// /model and /mouse are the only built-ins whose label starts with /mo;
	// slashItems() declares /model first, and the filter is stable, so it
	// leads.
	if len(m.completion.items) < 2 || m.completion.items[0].label != "/model" || m.completion.items[1].label != "/mouse" {
		t.Fatalf("prefix hits /model, /mouse should rank first in declaration order, got %v", labels(m.completion.items))
	}
	// Any other built-ins in the list are subsequence-only matches and must
	// therefore NOT be prefix hits of /mo.
	for _, it := range m.completion.items[2:] {
		if strings.HasPrefix(it.label, "/mo") {
			t.Errorf("%q should not appear after the /mo prefix hits", it.label)
		}
	}
}

// TestFuzzyFilterSlashCaseInsensitive proves the subsequence match is
// case-insensitive, since users routinely type commands in lowercase while
// the menu labels are all lowercase already.
func TestFuzzyFilterSlashCaseInsensitive(t *testing.T) {
	m := newTestChatTUI()
	m.input.SetValue("/COMP")
	m.updateCompletion()

	if !hasLabel(m.completion.items, "/compact") {
		t.Fatalf("uppercase /COMP should still match /compact: %v", labels(m.completion.items))
	}
}

// TestFuzzyFilterSlashEmptyQueryMatchesAll proves a bare "/" opens the menu
// with every command -- the same behavior the old prefix filter had, since
// every label trivially starts with "".
func TestFuzzyFilterSlashEmptyQueryMatchesAll(t *testing.T) {
	m := newTestChatTUI()
	all := len(m.slashItems())

	m.input.SetValue("/")
	m.updateCompletion()

	if !m.completion.active {
		t.Fatal("menu should open on a bare /")
	}
	if got := len(m.completion.items); got != all {
		t.Errorf("bare / should list every slash item, got %d want %d", got, all)
	}
}

// TestFuzzyFilterSlashNoMatchClosesMenu proves the menu still closes when the
// query matches nothing -- the contract the existing /zzz test relies on.
func TestFuzzyFilterSlashNoMatchClosesMenu(t *testing.T) {
	m := newTestChatTUI()
	m.input.SetValue("/xzqzqz")
	m.updateCompletion()

	if m.completion.active {
		t.Errorf("menu should close when no command matches: items=%v", labels(m.completion.items))
	}
}

// TestFuzzyFilterSlashAppliesToCustomCommands proves the fuzzy filter also
// covers custom slash commands (not just built-ins) -- the practical payoff,
// since users tend to invent short names like /review and type them fast.
func TestFuzzyFilterSlashAppliesToCustomCommands(t *testing.T) {
	m := newTestChatTUI()
	m.commands = []command.Command{
		{Name: "review", Description: "review the diff"},
		{Name: "release-notes", Description: "draft release notes"},
	}
	// /rle should match /release-notes (r-l-e in order) but NOT /review
	// (r-e-v-i-e-w has no 'l' after the initial r).
	m.input.SetValue("/rle")
	m.updateCompletion()

	if !hasLabel(m.completion.items, "/release-notes") {
		t.Errorf("/release-notes should match /rle: %v", labels(m.completion.items))
	}
	if hasLabel(m.completion.items, "/review") {
		t.Errorf("/review should NOT match /rle (r-e-v-i-e-w has no 'l' after r): %v", labels(m.completion.items))
	}
}

// TestFuzzyFilterSlashAcceptFillsInput proves the end-to-end accept path still
// works under the fuzzy filter: typing /compt then Tab should fill the input
// with the top-ranked hit, which is /compact.
func TestFuzzyFilterSlashAcceptFillsInput(t *testing.T) {
	m := newTestChatTUI()
	m.input.SetValue("/compt")
	m.updateCompletion()

	if !m.completion.active {
		t.Fatal("menu should open for /compt")
	}
	if m.completion.items[0].label != "/compact" {
		t.Fatalf("/compt should rank /compact first via subsequence match, got %v",
			labels(m.completion.items))
	}
	m.acceptCompletion()
	if got := m.input.Value(); got != "/compact " {
		t.Errorf("accept should fill the input with /compact , got %q", got)
	}
}

// TestSubsequenceMatchUnit covers the matcher directly so future tweaks to the
// scoring policy (prefix-first vs. subsequence-only) don't have to re-derive
// edge cases from end-to-end tests.
func TestSubsequenceMatchUnit(t *testing.T) {
	cases := []struct {
		target, query string
		want          bool
	}{
		{"", "", true},
		{"", "a", false},
		{"/model", "", true},
		{"/model", "mod", true},
		{"/model", "mdl", true}, // m-o-d-l in order
		{"/model", "xz", false},
		{"/compact", "compt", true},
		{"/compact", "cmpt", true}, // c then m then p then t
		{"/branch", "brh", true},
		{"/branch", "brnch", true},
		{"/paste-image", "pimg", true}, // p-a-s-t-e-...-i-m-g in order
		{"/mcp", "mrl", false},         // m-c-p is not a subsequence of m-r-l
		{"/review", "rle", false},      // r-e-v-i-e-w has no 'l'
		{"/memory", "memr", true},      // m-e-m-r in order (skip o)
	}
	for _, c := range cases {
		if got := subsequenceMatch(strings.ToLower(c.target), strings.ToLower(c.query)); got != c.want {
			t.Errorf("subsequenceMatch(%q, %q) = %v, want %v", c.target, c.query, got, c.want)
		}
	}
}
