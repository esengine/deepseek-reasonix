package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// read_file extended tests

func TestReadFileMissing(t *testing.T) {
	_, err := readFile{}.Execute(context.Background(), argsJSON(t, map[string]any{"path": "/nonexistent/file.txt"}))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Errorf("error should mention 'read': %v", err)
	}
}

func TestReadFileMissingPath(t *testing.T) {
	_, err := readFile{}.Execute(context.Background(), argsJSON(t, map[string]any{}))
	if err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestReadFileLargeFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "large.txt")
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	os.WriteFile(f, []byte(b.String()), 0o644)

	// Read with small limit.
	out := runTool(t, readFile{}, map[string]any{"path": f, "offset": 0, "limit": 3})
	if !strings.Contains(out, "1→line 1") {
		t.Errorf("missing first line: %s", out)
	}
	if !strings.Contains(out, "3→line 3") {
		t.Errorf("missing third line: %s", out)
	}
	if strings.Contains(out, "4→line 4") {
		t.Errorf("should not contain fourth line: %s", out)
	}
	// Pagination hint points at the next page; it no longer reads the whole file
	// to compute an exact remaining count.
	if !strings.Contains(out, "PARTIAL view") || !strings.Contains(out, "offset=3") {
		t.Errorf("pagination hint missing: %s", out)
	}
}

func TestReadFileOffsetPastEOF(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "short.txt")
	os.WriteFile(f, []byte("one\ntwo\n"), 0o644)

	out := runTool(t, readFile{}, map[string]any{"path": f, "offset": 100, "limit": 10})
	if !strings.Contains(out, "past EOF") {
		t.Errorf("should report past EOF: %s", out)
	}
}

func TestReadFileInvalidArgs(t *testing.T) {
	_, err := readFile{}.Execute(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// ls extended tests

func TestLsEmptyDir(t *testing.T) {
	dir := t.TempDir()
	out := runTool(t, listDir{}, map[string]any{"path": dir})
	if !strings.Contains(out, "(empty directory)") {
		t.Errorf("empty dir should report (empty directory): %s", out)
	}
}

func TestLsMissingDir(t *testing.T) {
	_, err := listDir{}.Execute(context.Background(), argsJSON(t, map[string]any{"path": "/nonexistent"}))
	if err == nil {
		t.Fatal("expected error for missing dir")
	}
}

func TestLsDefaultPath(t *testing.T) {
	// Default path "." should list the current directory without error.
	out := runTool(t, listDir{}, map[string]any{})
	if out == "" {
		t.Error("ls with default path should return something")
	}
}

func TestLsInvalidArgs(t *testing.T) {
	_, err := listDir{}.Execute(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// grep extended tests

func TestGrepSingleFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "code.go")
	os.WriteFile(f, []byte("func Foo() {}\nfunc Bar() {}\nvar x = 1\n"), 0o644)

	out := runTool(t, grepTool{}, map[string]any{"pattern": "func ", "path": f})
	if !strings.Contains(out, "Foo") || !strings.Contains(out, "Bar") {
		t.Errorf("should find both functions: %s", out)
	}
	if strings.Contains(out, "var x") {
		t.Errorf("should not include non-matching line: %s", out)
	}
}

func TestGrepNoMatches(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world\n"), 0o644)

	out := runTool(t, grepTool{}, map[string]any{"pattern": "xyzzy", "path": dir})
	if !strings.Contains(out, "(no matches)") {
		t.Errorf("expected (no matches): %s", out)
	}
}

func TestGrepInvalidPattern(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("test\n"), 0o644)

	_, err := grepTool{}.Execute(context.Background(), argsJSON(t, map[string]any{"pattern": "[invalid", "path": dir}))
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestGrepInvalidArgs(t *testing.T) {
	_, err := grepTool{}.Execute(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestGrepMissingPattern(t *testing.T) {
	_, err := grepTool{}.Execute(context.Background(), argsJSON(t, map[string]any{"path": "."}))
	if err == nil {
		t.Fatal("expected error for missing pattern")
	}
}

func TestGrepSkipsGitDir(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("secret = true\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)

	out := runTool(t, grepTool{}, map[string]any{"pattern": "secret", "path": dir})
	if strings.Contains(out, ".git") {
		t.Errorf("grep should skip .git directory: %s", out)
	}
}

func TestGrepTruncation(t *testing.T) {
	dir := t.TempDir()
	// Create a file with many matching lines.
	var b strings.Builder
	for i := range 300 {
		fmt.Fprintf(&b, "match %d\n", i)
	}
	os.WriteFile(filepath.Join(dir, "many.txt"), []byte(b.String()), 0o644)

	out := runTool(t, grepTool{}, map[string]any{"pattern": "match", "path": dir})
	if !strings.Contains(out, "truncated") {
		t.Errorf("should mention truncation: %s", out)
	}
}

// glob extended tests

func TestGlobEmptyPattern(t *testing.T) {
	_, err := globTool{}.Execute(context.Background(), argsJSON(t, map[string]any{"pattern": ""}))
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
}

func TestGlobInvalidArgs(t *testing.T) {
	_, err := globTool{}.Execute(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestGlobCharClass(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644)
	os.WriteFile(filepath.Join(dir, "c.log"), []byte("c"), 0o644)

	out := runTool(t, globTool{}, map[string]any{"pattern": filepath.Join(dir, "[ab].txt")})
	if !strings.Contains(out, "a.txt") || !strings.Contains(out, "b.txt") {
		t.Errorf("should match [ab].txt: %s", out)
	}
	if strings.Contains(out, "c.log") {
		t.Errorf("should not match c.log: %s", out)
	}
}

func TestGlobQuestionMark(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(dir, "ab.txt"), []byte("ab"), 0o644)

	out := runTool(t, globTool{}, map[string]any{"pattern": filepath.Join(dir, "?.txt")})
	if !strings.Contains(out, "a.txt") {
		t.Errorf("should match a.txt: %s", out)
	}
	if strings.Contains(out, "ab.txt") {
		t.Errorf("?.txt should not match ab.txt: %s", out)
	}
}
