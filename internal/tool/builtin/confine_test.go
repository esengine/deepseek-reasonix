package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/config"
	"reasonix/internal/sandbox"
	"reasonix/internal/secrets"
	"reasonix/internal/testenv"
	"reasonix/internal/tool"
)

func isolateBuiltinTestUserState(t *testing.T) string {
	t.Helper()
	cleanup, err := testenv.IsolateUserState()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return os.Getenv("HOME")
}

func TestWithin(t *testing.T) {
	root := filepath.FromSlash("/work/proj")
	cases := []struct {
		path string
		want bool
	}{
		{filepath.FromSlash("/work/proj"), true},           // the root itself
		{filepath.FromSlash("/work/proj/a/b.go"), true},    // nested
		{filepath.FromSlash("/work/proj/../proj/x"), true}, // normalises back inside
		{filepath.FromSlash("/work/other"), false},         // sibling
		{filepath.FromSlash("/work/proj-2"), false},        // prefix collision, not within
		{filepath.FromSlash("/etc/passwd"), false},         // elsewhere
		{filepath.FromSlash("/work"), false},               // parent
	}
	for _, c := range cases {
		if got := within(root, filepath.Clean(c.path)); got != c.want {
			t.Errorf("within(%q, %q) = %v, want %v", root, c.path, got, c.want)
		}
	}
}

func TestConfineUnconfinedWhenNoRoots(t *testing.T) {
	if err := confine(nil, "/anywhere/at/all"); err != nil {
		t.Errorf("empty roots should be unconfined, got %v", err)
	}
}

func TestRebindBashWriteRootsUsesMinimalWriteSurface(t *testing.T) {
	root := t.TempDir()
	claim := filepath.Join(root, "claimed")
	tool, ok := RebindBashWriteRoots(ConfineBash(sandbox.Spec{
		Mode:       "enforce",
		WriteRoots: []string{root},
	}, SessionDataGuard{}), []string{claim})
	if !ok {
		t.Fatal("expected confined bash to be rebound")
	}
	rebound, ok := tool.(bash)
	if !ok {
		t.Fatalf("rebound tool type = %T, want bash", tool)
	}
	if !rebound.sb.MinimalWrites {
		t.Fatal("rebound bash must disable write allowances outside claim roots")
	}
	want := realRoots([]string{claim})
	if len(rebound.sb.WriteRoots) != 1 || rebound.sb.WriteRoots[0] != want[0] {
		t.Fatalf("write roots = %v, want %v", rebound.sb.WriteRoots, want)
	}
	if len(rebound.sb.AppContainerWriteRoots) != 1 || rebound.sb.AppContainerWriteRoots[0] != want[0] {
		t.Fatalf("app-container write roots = %v, want %v", rebound.sb.AppContainerWriteRoots, want)
	}
}

func TestReboundBashCannotWriteOutsideClaim(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("OS sandbox unavailable")
	}
	root := t.TempDir()
	claim := filepath.Join(root, "claimed")
	if err := os.MkdirAll(claim, 0o755); err != nil {
		t.Fatal(err)
	}
	rebound, ok := RebindBashWriteRoots(ConfineBash(sandbox.Spec{
		Mode:       "enforce",
		WriteRoots: []string{root},
	}, SessionDataGuard{}), []string{claim})
	if !ok {
		t.Fatal("expected confined bash to be rebound")
	}

	inside := filepath.Join(claim, "inside.txt")
	args, _ := json.Marshal(map[string]string{"command": fmt.Sprintf("printf inside > %q", inside)})
	if _, err := rebound.Execute(context.Background(), args); err != nil {
		t.Fatalf("write inside claim failed: %v", err)
	}
	if _, err := os.Stat(inside); err != nil {
		t.Fatalf("write inside claim did not land: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "escaped.txt")
	args, _ = json.Marshal(map[string]string{"command": fmt.Sprintf("printf escaped > %q", outside)})
	_, _ = rebound.Execute(context.Background(), args)
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("rebound bash wrote outside claim, stat err=%v", err)
	}
}

func TestConfineInsideAndOutside(t *testing.T) {
	root := t.TempDir()
	roots := realRoots([]string{root})

	if err := confine(roots, filepath.Join(root, "src", "main.go")); err != nil {
		t.Errorf("path inside root rejected: %v", err)
	}
	// A sibling of the root and a parent escape must both be refused.
	if err := confine(roots, filepath.Join(root, "..", "escape.txt")); err == nil {
		t.Error("parent-escape path accepted, want error")
	}
	if err := confine(roots, filepath.Join(filepath.Dir(root), "neighbour", "x")); err == nil {
		t.Error("sibling path accepted, want error")
	}
}

func TestConfineRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	// A symlinked directory inside the root pointing outside must not become a
	// tunnel: a write "within" the link still resolves outside the root.
	link := filepath.Join(root, "out")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	roots := realRoots([]string{root})
	if err := confine(roots, filepath.Join(link, "evil.txt")); err == nil {
		t.Error("write through symlinked dir escaped the root, want error")
	}
	// A normal file under the real root still passes.
	if err := confine(roots, filepath.Join(root, "ok.txt")); err != nil {
		t.Errorf("legit path rejected: %v", err)
	}
}

func TestWriteFileConfinement(t *testing.T) {
	root := t.TempDir()
	w := writeFile{roots: realRoots([]string{root})}

	// Inside: written.
	in := filepath.Join(root, "a", "in.txt")
	args, _ := json.Marshal(map[string]string{"path": in, "content": "hi"})
	if _, err := w.Execute(context.Background(), args); err != nil {
		t.Fatalf("write inside root failed: %v", err)
	}
	if _, err := os.Stat(in); err != nil {
		t.Errorf("file not created inside root: %v", err)
	}

	// Outside: refused, and the file must not be created.
	out := filepath.Join(t.TempDir(), "out.txt")
	args, _ = json.Marshal(map[string]string{"path": out, "content": "nope"})
	if _, err := w.Execute(context.Background(), args); err == nil {
		t.Error("write outside root should error")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("file outside root must not be created")
	}
}

func TestWriteFileDefaultRootsDenyUserConfigUnlessAllowed(t *testing.T) {
	home := isolateBuiltinTestUserState(t)

	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	w := writeFile{roots: realRoots(cfg.WriteRootsForRoot(project))}

	userConfig := config.UserConfigPath()
	args, _ := json.Marshal(map[string]string{
		"path":    userConfig,
		"content": "default_model = \"deepseek\"\n",
	})
	if _, err := w.Execute(context.Background(), args); err == nil {
		t.Fatalf("write user config should be denied by default")
	}
	if _, err := os.Stat(userConfig); !os.IsNotExist(err) {
		t.Fatalf("user config must not be created by default, stat err=%v", err)
	}

	cfg.Sandbox.AllowWrite = []string{filepath.Dir(userConfig)}
	w = writeFile{roots: realRoots(cfg.WriteRootsForRoot(project))}
	if _, err := w.Execute(context.Background(), args); err != nil {
		t.Fatalf("write user config should be allowed with allow_write: %v", err)
	}
	if _, err := os.Stat(userConfig); err != nil {
		t.Fatalf("user config was not created with allow_write: %v", err)
	}
}

// stubConfigWriteApprover is a scripted tool.ConfigWriteApprover recording the
// paths it was asked about.
type stubConfigWriteApprover struct {
	allow  bool
	reason string
	asked  []string
}

func (s *stubConfigWriteApprover) ApproveManagedConfigWrite(_ context.Context, req tool.ConfigWriteRequest) (bool, string, error) {
	s.asked = append(s.asked, req.Path)
	return s.allow, s.reason, nil
}

func TestManagedConfigWriteFailsClosedWithoutApprover(t *testing.T) {
	home := isolateBuiltinTestUserState(t)

	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	managed := NewManagedConfigPaths(config.ReasonixManagedConfigPaths())
	w := writeFile{roots: realRoots(cfg.WriteRootsForRoot(project)), managed: managed}

	// Headless runs and sub-agents with no interactive parent carry no approver
	// on ctx: the managed-config escape hatch must fail closed.
	userConfig := config.UserConfigPath()
	args, _ := json.Marshal(map[string]string{"path": userConfig, "content": "{}\n"})
	_, err := w.Execute(context.Background(), args)
	if err == nil {
		t.Fatalf("managed config write without an approver should be denied")
	}
	if !strings.Contains(err.Error(), "interactive user approval") {
		t.Fatalf("fail-closed error should name the missing approval, got: %v", err)
	}
	if _, err := os.Stat(userConfig); !os.IsNotExist(err) {
		t.Fatalf("user config must not be created without approval, stat err=%v", err)
	}
}

func TestManagedConfigWriteGatedOnApprover(t *testing.T) {
	home := isolateBuiltinTestUserState(t)

	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	managed := NewManagedConfigPaths(config.ReasonixManagedConfigPaths())
	w := writeFile{roots: realRoots(cfg.WriteRootsForRoot(project)), managed: managed}

	// Approved: current config.toml and the legacy v0.x config.json become
	// writable, and the approver sees each target.
	approve := &stubConfigWriteApprover{allow: true}
	ctx := tool.WithConfigWriteApprover(context.Background(), approve)
	for _, target := range []string{
		config.UserConfigPath(),
		filepath.Join(home, ".reasonix", "config.json"),
	} {
		args, _ := json.Marshal(map[string]string{"path": target, "content": "{}\n"})
		if _, err := w.Execute(ctx, args); err != nil {
			t.Fatalf("approved managed config write %s: %v", target, err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("managed config was not created %s: %v", target, err)
		}
	}
	if len(approve.asked) != 2 {
		t.Fatalf("approver should be asked once per write, asked=%v", approve.asked)
	}

	// Declined: the approver's reason surfaces to the model and nothing lands.
	decline := &stubConfigWriteApprover{allow: false, reason: "the user declined this Reasonix config write"}
	dctx := tool.WithConfigWriteApprover(context.Background(), decline)
	declinedTarget := config.UserConfigPath()
	if err := os.Remove(declinedTarget); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove approved config before declined write: %v", err)
	}
	args, _ := json.Marshal(map[string]string{"path": declinedTarget, "content": "{}\n"})
	if _, err := w.Execute(dctx, args); err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("declined managed config write should surface the reason, got: %v", err)
	}
	if _, err := os.Stat(declinedTarget); !os.IsNotExist(err) {
		t.Fatalf("declined config must not be created, stat err=%v", err)
	}

	// Even with an always-allowing approver, non-config files in the Reasonix
	// home and the rest of the OS home stay denied — the escape hatch is
	// file-level, not directory-level.
	for _, target := range []string{
		filepath.Join(home, "notes.txt"),
		filepath.Join(home, ".reasonix", ".env"),
		filepath.Join(home, ".reasonix", "settings.json"),
		filepath.Join(home, ".reasonix", "skills", "evil", "SKILL.md"),
	} {
		asked := len(approve.asked)
		args, _ := json.Marshal(map[string]string{"path": target, "content": "nope\n"})
		if _, err := w.Execute(ctx, args); err == nil {
			t.Fatalf("write outside managed config files should be denied: %s", target)
		}
		if len(approve.asked) != asked {
			t.Fatalf("non-managed target %s must not reach the approver", target)
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("file must not be created %s, stat err=%v", target, err)
		}
	}
}

// TestManagedConfigWriteAsksEvenInsideRoots pins that a Reasonix-managed config
// file stays gated behind the fresh per-write approval even when the write
// roots already cover it: YOLO and a widened allow_write must never let an
// agent rewrite config.toml silently.
func TestManagedConfigWriteAsksEvenInsideRoots(t *testing.T) {
	home := isolateBuiltinTestUserState(t)
	if err := os.MkdirAll(filepath.Join(home, ".reasonix"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	userConfig := config.UserConfigPath()
	// The whole isolated home is a write root, config.toml included.
	cfg.Sandbox.AllowWrite = []string{home}
	managed := NewManagedConfigPaths(config.ReasonixManagedConfigPaths())
	w := writeFile{roots: realRoots(cfg.WriteRootsForRoot(home)), managed: managed}
	args, _ := json.Marshal(map[string]string{"path": userConfig, "content": "{}\n"})

	// No approver: fails closed even though the path is inside the roots.
	if _, err := w.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "interactive user approval") {
		t.Fatalf("in-root managed config write without an approver should be denied, got: %v", err)
	}
	if _, err := os.Stat(userConfig); !os.IsNotExist(err) {
		t.Fatalf("user config must not be created without approval, stat err=%v", err)
	}

	// Approved: the approver is consulted and the write lands.
	approve := &stubConfigWriteApprover{allow: true}
	ctx := tool.WithConfigWriteApprover(context.Background(), approve)
	if _, err := w.Execute(ctx, args); err != nil {
		t.Fatalf("approved in-root managed config write: %v", err)
	}
	if len(approve.asked) != 1 || approve.asked[0] != userConfig {
		t.Fatalf("approver should be asked for the in-root config path, asked=%v", approve.asked)
	}

	// Declined: reason surfaces, nothing lands.
	decline := &stubConfigWriteApprover{allow: false, reason: "the user declined this Reasonix config write"}
	dctx := tool.WithConfigWriteApprover(context.Background(), decline)
	if err := os.Remove(userConfig); err != nil {
		t.Fatalf("remove approved config before declined write: %v", err)
	}
	if _, err := w.Execute(dctx, args); err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("declined in-root managed config write should surface the reason, got: %v", err)
	}
	if _, err := os.Stat(userConfig); !os.IsNotExist(err) {
		t.Fatalf("declined config must not be created, stat err=%v", err)
	}

	// A plain file inside the roots still writes silently: the managed gate is
	// file-level, not directory-level.
	plain := filepath.Join(home, "notes.txt")
	pargs, _ := json.Marshal(map[string]string{"path": plain, "content": "hi\n"})
	if _, err := w.Execute(context.Background(), pargs); err != nil {
		t.Fatalf("plain in-root write must not consult the approver: %v", err)
	}
	if len(approve.asked) != 1 {
		t.Fatalf("plain in-root write must not reach the approver, asked=%v", approve.asked)
	}
}

func TestBashSandboxConfinement(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("OS sandbox not available")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	work, err := os.MkdirTemp(home, ".reasonix-bashsb-*")
	if err != nil {
		t.Skipf("cannot create work dir under home: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(work) })
	t.Chdir(work)
	spec := sandbox.Spec{Mode: "enforce", WriteRoots: []string{work}, Network: true}
	b := ConfineBash(spec, SessionDataGuard{})

	// Writing inside the root works; writing to a sibling under $HOME is denied
	// by the sandbox the bash tool wrapped the command in.
	inCommand := "echo hi > " + filepath.Join(work, "in.txt")
	inArgs, _ := json.Marshal(map[string]string{"command": inCommand})
	if _, err := b.Execute(context.Background(), inArgs); err != nil {
		t.Fatalf("bash write inside root failed: %v", err)
	}
	outPath := filepath.Join(home, ".reasonix-bashsb-escape.txt")
	t.Cleanup(func() { os.Remove(outPath) })
	outCommand := "echo nope > " + outPath
	outArgs, _ := json.Marshal(map[string]string{"command": outCommand})
	if _, err := b.Execute(context.Background(), outArgs); err == nil {
		t.Error("bash write outside the workspace should be denied by the sandbox")
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Error("escaping write must not create the file")
	}
}

func TestBashEnforceRejectsWhenSandboxUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b := bash{
		sb: sandbox.Spec{
			Mode:       "enforce",
			WriteRoots: []string{t.TempDir()},
		},
		shell: sandbox.Shell{Kind: sandbox.ShellBash, Path: exe},
	}

	args, _ := json.Marshal(map[string]string{"command": "ignored"})
	out, err := b.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("bash should reject enforce mode when the OS sandbox is unavailable")
	}
	if !strings.Contains(err.Error(), "bash sandbox requested but unavailable") {
		t.Fatalf("error = %q, want sandbox unavailable", err)
	}
	if out != "" {
		t.Fatalf("output = %q, want no command execution", out)
	}
}

func TestUnconfinedWriterWritesAnywhere(t *testing.T) {
	// A zero-value writer (roots nil, as registered at init) is unconfined.
	out := filepath.Join(t.TempDir(), "free.txt")
	args, _ := json.Marshal(map[string]string{"path": out, "content": "ok"})
	if _, err := (writeFile{}).Execute(context.Background(), args); err != nil {
		t.Fatalf("unconfined write failed: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("unconfined writer did not write: %v", err)
	}
}

// confineRead & ConfineReaders

func TestConfineReadEmpty(t *testing.T) {
	if confineRead(nil, "/anywhere") {
		t.Error("empty forbidRoots should be unconfined")
	}
}

func TestConfineReadInsideAndOutside(t *testing.T) {
	root := t.TempDir()
	forbidRoots := realRoots([]string{root})

	if !confineRead(forbidRoots, filepath.Join(root, "secret", "key.pem")) {
		t.Error("path inside forbid root should be forbidden")
	}
	// A path outside must pass.
	if confineRead(forbidRoots, filepath.Join(t.TempDir(), "ok.txt")) {
		t.Error("path outside forbid root should not be forbidden")
	}
}

func TestConfineReadExactFileRoot(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "credentials.env")
	visible := filepath.Join(dir, "project.env")
	for _, path := range []string{secret, visible} {
		if err := os.WriteFile(path, []byte("value"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	forbidRoots := realRoots([]string{secret})
	if !confineRead(forbidRoots, secret) {
		t.Fatal("exact forbidden file should be unreadable")
	}
	if confineRead(forbidRoots, visible) {
		t.Fatal("sibling file should remain readable")
	}
}

func TestConfineReadBlocksReadFile(t *testing.T) {
	forbidDir := t.TempDir()
	secretPath := filepath.Join(forbidDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("classified"), 0o644); err != nil {
		t.Fatal(err)
	}
	forbidRoots := realRoots([]string{forbidDir})
	rf := readFile{forbidRoots: forbidRoots}
	args, _ := json.Marshal(map[string]string{"path": secretPath})
	_, err := rf.Execute(context.Background(), args)
	if err == nil {
		t.Error("read_file should refuse a forbid-read path")
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Errorf("read_file forbid-read error should be *os.PathError, got %T: %v", err, err)
	}
	// Unconfined (nil forbidRoots) should work.
	rfUnconfined := readFile{}
	if _, err := rfUnconfined.Execute(context.Background(), args); err != nil {
		t.Errorf("unconfined read_file should work: %v", err)
	}
}

// withProtectSensitiveFiles flips the [secrets] protect_sensitive_files
// toggle for one test and restores the default-off state afterwards.
func withProtectSensitiveFiles(t *testing.T, enabled bool) {
	t.Helper()
	secrets.SetProtectSensitiveFiles(enabled)
	t.Cleanup(func() { secrets.SetProtectSensitiveFiles(false) })
}

func TestSensitiveReadPathsAreBlockedWhenProtected(t *testing.T) {
	withProtectSensitiveFiles(t, true)
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("DEEPSEEK_API_KEY=sk-real-secret-value-123456\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pemPath := filepath.Join(dir, "client.pem")
	if err := os.WriteFile(pemPath, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{envPath, pemPath} {
		if !confineRead(nil, path) {
			t.Fatalf("sensitive path %s should be blocked when protection is on", path)
		}
		rf := readFile{}
		_, err := rf.Execute(context.Background(), argsJSON(t, map[string]any{"path": path}))
		if err == nil {
			t.Fatalf("read_file should refuse sensitive path %s", path)
		}
	}

	visible := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(visible, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if confineRead(nil, visible) {
		t.Fatalf("ordinary path %s should not be blocked", visible)
	}
}

func TestSensitiveReadPathsAllowedByDefault(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("PORT=8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if confineRead(nil, envPath) {
		t.Fatalf(".env should stay readable while protect_sensitive_files is off (default)")
	}
	rf := readFile{}
	out, err := rf.Execute(context.Background(), argsJSON(t, map[string]any{"path": envPath}))
	if err != nil {
		t.Fatalf("read_file .env with protection off: %v", err)
	}
	if !strings.Contains(out, "PORT=8080") {
		t.Fatalf("read_file dropped .env content:\n%s", out)
	}
}

func TestGlobFiltersSensitiveMatchesWhenProtected(t *testing.T) {
	withProtectSensitiveFiles(t, true)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET_TOKEN=abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := globTool{}
	out, err := g.Execute(context.Background(), argsJSON(t, map[string]any{"pattern": filepath.Join(dir, "*")}))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if strings.Contains(out, ".env") {
		t.Fatalf("glob leaked sensitive match:\n%s", out)
	}
	if !strings.Contains(out, "notes.txt") {
		t.Fatalf("glob dropped ordinary match:\n%s", out)
	}
}

// grep forbid-read

func TestConfineReadBlocksGrepFile(t *testing.T) {
	forbidDir := t.TempDir()
	secretPath := filepath.Join(forbidDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("needle in a haystack"), 0o644); err != nil {
		t.Fatal(err)
	}
	forbidRoots := realRoots([]string{forbidDir})
	g := grepTool{forbidRoots: forbidRoots}
	args, _ := json.Marshal(map[string]string{"pattern": "needle", "path": secretPath})
	_, err := g.Execute(context.Background(), args)
	if err == nil {
		t.Error("grep on a forbid-read file should error, not return (no matches)")
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Errorf("grep forbid-read error should be *os.PathError, got %T: %v", err, err)
	}
	// Unconfined (nil forbidRoots) should work.
	gUnconfined := grepTool{}
	if out, err := gUnconfined.Execute(context.Background(), args); err != nil {
		t.Errorf("unconfined grep should work: %v", err)
	} else if out == "(no matches)" {
		t.Error("unconfined grep should find the needle")
	}
}

func TestConfineReadBlocksNativeGrepDirectoryRoot(t *testing.T) {
	root := t.TempDir()
	forbidDir := filepath.Join(root, "secret")
	secretPath := filepath.Join(forbidDir, "secret.txt")
	if err := os.MkdirAll(forbidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretPath, []byte("needle in a haystack"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := grepTool{workDir: root, forbidRoots: realRoots([]string{forbidDir})}
	out, err := g.Execute(context.Background(), argsJSON(t, map[string]any{"pattern": "needle", "path": "secret"}))
	if err != nil {
		t.Fatalf("grep forbidden directory should look empty, got error: %v", err)
	}
	if out != "(no matches)" {
		t.Fatalf("grep forbidden directory = %q, want (no matches)", out)
	}
}

func TestConfineReadFiltersPlainGlobMatches(t *testing.T) {
	root := t.TempDir()
	forbidDir := filepath.Join(root, "secret")
	if err := os.MkdirAll(forbidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(forbidDir, "secret.go"), []byte("package secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := globTool{workDir: root, forbidRoots: realRoots([]string{forbidDir})}
	out, err := g.Execute(context.Background(), argsJSON(t, map[string]any{"pattern": "secret/*.go"}))
	if err != nil {
		t.Fatalf("glob forbidden directory: %v", err)
	}
	if out != "(no matches)" {
		t.Fatalf("glob leaked forbidden paths:\n%s", out)
	}
}
