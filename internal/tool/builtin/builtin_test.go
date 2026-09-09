package builtin

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"go.uber.org/goleak"
	"golang.org/x/text/encoding/simplifiedchinese"

	"reasonix/internal/tool"
)

// argsJSON marshals m into the JSON form a tool expects. Tests must not build
// the JSON by concatenating Go strings: on Windows, t.TempDir() returns a path
// like C:\Users\… and the embedded backslashes are interpreted as JSON string
// escapes (\U triggers a parse error). json.Marshal handles the escaping.
func argsJSON(t *testing.T, m map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return json.RawMessage(b)
}

func runTool(t *testing.T, tl tool.Tool, m map[string]any) string {
	t.Helper()
	out, err := tl.Execute(context.Background(), argsJSON(t, m))
	if err != nil {
		t.Fatalf("%s: %v", tl.Name(), err)
	}
	return out
}

func TestBuiltinsRegistered(t *testing.T) {
	want := []string{"bash", "code_index", "compress", "edit_file", "glob", "grep", "ls", "move_file", "multi_edit", "read_file", "web_fetch", "write_file"}
	for _, name := range want {
		if _, ok := tool.LookupBuiltin(name); !ok {
			t.Errorf("built-in %q not registered", name)
		}
	}
}

// TestBuiltinReadOnlyClassification locks in which built-ins the agent may
// parallelise. Flipping a writer (write_file, edit_file, bash) to ReadOnly
// would re-order writes against reads in the same turn; this test fails fast
// if that ever happens. bash specifically must stay non-ReadOnly even though
// many invocations are pure reads — args aren't introspected.
func TestBuiltinReadOnlyClassification(t *testing.T) {
	readOnly := map[string]bool{
		"read_file": true, "ls": true, "glob": true, "grep": true, "code_index": true, "compress": true, "web_fetch": true,
		"write_file": false, "edit_file": false, "multi_edit": false, "move_file": false, "bash": false,
	}
	for name, want := range readOnly {
		tl, ok := tool.LookupBuiltin(name)
		if !ok {
			t.Fatalf("built-in %q not registered", name)
		}
		if got := tl.ReadOnly(); got != want {
			t.Errorf("%s.ReadOnly() = %v, want %v", name, got, want)
		}
	}
}

type compressStub struct {
	request tool.CompressRequest
	result  tool.CompressResult
}

func (s *compressStub) CompressContext(_ context.Context, request tool.CompressRequest) (tool.CompressResult, error) {
	s.request = request
	return s.result, nil
}

func TestCompressToolProtocol(t *testing.T) {
	stub := &compressStub{result: tool.CompressResult{
		Status: "ok", Direction: "before", Anchor: "unique", Messages: 4,
		SourceTokens: 900, ProjectionTokens: 200, Mode: "summarized",
	}}
	ctx := tool.WithContextCompressor(context.Background(), stub)
	out, err := (compressContext{}).Execute(ctx, argsJSON(t, map[string]any{
		"direction": "before", "anchor": "  unique excerpt  ", "focus": " keep decisions ",
	}))
	if err != nil {
		t.Fatalf("compress Execute: %v", err)
	}
	if stub.request.Direction != "before" || stub.request.Anchor != "unique excerpt" || stub.request.Focus != "keep decisions" {
		t.Fatalf("forwarded request = %+v", stub.request)
	}
	var got tool.CompressResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if got != stub.result {
		t.Fatalf("result = %+v, want %+v", got, stub.result)
	}
	if !(compressContext{}).ReadOnly() || !(compressContext{}).PlanModeSafe() {
		t.Fatal("compress must be workspace-read-only and Plan Mode safe")
	}
}

func TestCompressToolRejectsInvalidArgs(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "direction", args: map[string]any{"direction": "around", "anchor": "x"}, want: "direction"},
		{name: "case-sensitive direction", args: map[string]any{"direction": "Before", "anchor": "x"}, want: "direction"},
		{name: "empty anchor", args: map[string]any{"direction": "before", "anchor": "  "}, want: "empty"},
		{name: "long anchor", args: map[string]any{"direction": "before", "anchor": strings.Repeat("a", 513)}, want: "512 bytes"},
		{name: "long focus", args: map[string]any{"direction": "before", "anchor": "x", "focus": strings.Repeat("a", 2001)}, want: "2000 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (compressContext{}).Execute(context.Background(), argsJSON(t, tt.args))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestCompressToolRequiresActiveAgent(t *testing.T) {
	_, err := (compressContext{}).Execute(context.Background(), argsJSON(t, map[string]any{
		"direction": "after", "anchor": "unique",
	}))
	if err == nil || !strings.Contains(err.Error(), "active agent session") {
		t.Fatalf("error = %v, want unavailable context compressor", err)
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "src.go")
	body := "package main\n\nfunc main() {}\n"
	os.WriteFile(f, []byte(body), 0o644)

	out := runTool(t, readFile{}, map[string]any{"path": f})
	// Line numbers must be present, right-aligned, with the arrow separator.
	for _, want := range []string{"1→package main", "2→", "3→func main"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestReadFileDirectory(t *testing.T) {
	dir := t.TempDir()
	_, err := readFile{}.Execute(context.Background(), argsJSON(t, map[string]any{"path": dir}))
	if err == nil {
		t.Fatal("read_file on a directory should error, not return contents")
	}
	// The message must be actionable (point at ls) and not the doubled
	// "read X: read X:" the raw scanner error produced.
	if !strings.Contains(err.Error(), "directory") || !strings.Contains(err.Error(), "ls") {
		t.Errorf("error should tell the model to use ls, got: %v", err)
	}
	if strings.Count(err.Error(), "read "+dir) > 1 {
		t.Errorf("error is doubled: %v", err)
	}
}

func TestReadFileOffsetLimit(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "many.txt")
	var b strings.Builder
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	os.WriteFile(f, []byte(b.String()), 0o644)

	out := runTool(t, readFile{}, map[string]any{"path": f, "offset": 10, "limit": 5})
	// Should see lines 11-15 only.
	for _, want := range []string{"11→line 11", "15→line 15"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, leak := range []string{"line 5\n", "line 16\n", "line 20\n"} {
		if strings.Contains(out, leak) {
			t.Errorf("leaked %q (outside the slice)\n%s", leak, out)
		}
	}
	// Trailer announces what's left so the model can paginate.
	if !strings.Contains(out, "PARTIAL view") || !strings.Contains(out, "offset=15") {
		t.Errorf("pagination hint missing:\n%s", out)
	}
}

func TestReadFileBinary(t *testing.T) {
	f := filepath.Join(t.TempDir(), "blob")
	os.WriteFile(f, []byte{0x7f, 'E', 'L', 'F', 0, 0, 0}, 0o644)

	_, err := readFile{}.Execute(context.Background(), argsJSON(t, map[string]any{"path": f}))
	if err == nil || !strings.Contains(err.Error(), "binary") {
		t.Errorf("expected binary-file error, got %v", err)
	}
}

func TestReadFileBOM(t *testing.T) {
	enc := func(order binary.ByteOrder, s string) []byte {
		var b bytes.Buffer
		if order == binary.LittleEndian {
			b.Write([]byte{0xFF, 0xFE})
		} else {
			b.Write([]byte{0xFE, 0xFF})
		}
		for _, r := range utf16.Encode([]rune(s)) {
			_ = binary.Write(&b, order, r)
		}
		return b.Bytes()
	}
	cases := map[string][]byte{
		"utf16le.txt": enc(binary.LittleEndian, "hello world\nsecond line"),
		"utf16be.txt": enc(binary.BigEndian, "hello world\nsecond line"),
		"utf8bom.txt": append([]byte{0xEF, 0xBB, 0xBF}, []byte("hello world\nsecond line")...),
	}
	for name, content := range cases {
		f := filepath.Join(t.TempDir(), name)
		os.WriteFile(f, content, 0o644)
		out := runTool(t, readFile{}, map[string]any{"path": f})
		if !strings.Contains(out, "hello world") || !strings.Contains(out, "second line") {
			t.Errorf("%s: expected decoded text, got %q", name, out)
		}
		if strings.Contains(out, "\ufeff") || strings.IndexByte(out, 0) >= 0 {
			t.Errorf("%s: BOM/NUL leaked into output: %q", name, out)
		}
	}
}

func TestReadFileEmpty(t *testing.T) {
	f := filepath.Join(t.TempDir(), "empty.txt")
	os.WriteFile(f, nil, 0o644)
	if out := runTool(t, readFile{}, map[string]any{"path": f}); !strings.Contains(out, "empty") {
		t.Errorf("empty file should report empty, got %q", out)
	}
}

func TestEditFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(f, []byte("hello world\n"), 0o644)

	out := runTool(t, editFile{}, map[string]any{"path": f, "old_string": "world", "new_string": "reasonix"})
	for _, want := range []string{"Actual replacement receipt after write:", "-world", "+reasonix"} {
		if !strings.Contains(out, want) {
			t.Fatalf("edit result should contain %q in actual post-write receipt:\n%s", want, out)
		}
	}
	if strings.Contains(out, "hello") {
		t.Fatalf("edit receipt should not include unchanged same-line content:\n%s", out)
	}
	if b, _ := os.ReadFile(f); string(b) != "hello reasonix\n" {
		t.Fatalf("after edit = %q", b)
	}

	// Non-unique old_string must error and not modify the file.
	os.WriteFile(f, []byte("x x x"), 0o644)
	args := argsJSON(t, map[string]any{"path": f, "old_string": "x", "new_string": "y"})
	if _, err := (editFile{}).Execute(context.Background(), args); err == nil {
		t.Fatal("expected not-unique error")
	} else if !strings.Contains(err.Error(), "repeated separator lines") {
		t.Fatalf("not-unique error should steer away from weak anchors, got: %v", err)
	}
	if b, _ := os.ReadFile(f); string(b) != "x x x" {
		t.Fatalf("file modified despite error: %q", b)
	}
}

func TestMultiEdit(t *testing.T) {
	f := filepath.Join(t.TempDir(), "src.go")
	body := "package old\n\nfunc old() {\n\told()\n}\n"
	os.WriteFile(f, []byte(body), 0o644)

	// Two edits: rename the package (unique) then sweep every old → new.
	out := runTool(t, multiEdit{}, map[string]any{
		"path": f,
		"edits": []map[string]any{
			{"old_string": "package old", "new_string": "package new"},
			{"old_string": "old", "new_string": "reasonix", "replace_all": true},
		},
	})
	if !strings.Contains(out, "multi_edit") || !strings.Contains(out, "2 edits applied") {
		t.Errorf("summary unexpected: %q", out)
	}
	for _, want := range []string{"Actual replacement receipt after write:", "-package old", "+package new", "-old", "+reasonix"} {
		if !strings.Contains(out, want) {
			t.Fatalf("multi_edit result should contain %q in actual post-write receipt:\n%s", want, out)
		}
	}
	if strings.Contains(out, "func reasonix") {
		t.Fatalf("multi_edit receipt should not include unchanged same-line content:\n%s", out)
	}
	got, _ := os.ReadFile(f)
	want := "package new\n\nfunc reasonix() {\n\treasonix()\n}\n"
	if string(got) != want {
		t.Errorf("after multi_edit = %q\n          want = %q", got, want)
	}
}

// TestMultiEditAtomicity is the safety guarantee: if any edit fails, the file
// stays exactly as it was. A chained sequence of single edit_file calls would
// have left a half-written intermediate state.
func TestMultiEditAtomicity(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.txt")
	original := "alpha\nbeta\ngamma\n"
	os.WriteFile(f, []byte(original), 0o644)

	args := argsJSON(t, map[string]any{
		"path": f,
		"edits": []map[string]any{
			{"old_string": "alpha", "new_string": "ALPHA"},
			{"old_string": "no-such-text", "new_string": "x"},
			{"old_string": "gamma", "new_string": "GAMMA"},
		},
	})
	if _, err := (multiEdit{}).Execute(context.Background(), args); err == nil {
		t.Fatal("expected failure on the missing edit")
	}
	got, _ := os.ReadFile(f)
	if string(got) != original {
		t.Errorf("file was modified despite failure:\n got %q\nwant %q", got, original)
	}
}

func TestGrep(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc Foo() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("var x = 1\n"), 0o644)

	out := runTool(t, grepTool{}, map[string]any{"pattern": "func ", "path": dir})
	if !strings.Contains(out, "Foo") || strings.Contains(out, "var x") {
		t.Fatalf("grep result = %q", out)
	}
}

// TestWebFetchHTML serves a tiny HTML page and checks the reducer keeps the
// readable text while removing scripts, styles, and tags.
func TestWebFetchHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html>
<html><head><title>T</title><style>body{color:red}</style></head>
<body>
<h1>Hello &amp; world</h1>
<script>alert("bad")</script>
<p>Visible text.</p>
</body></html>`))
	}))
	defer srv.Close()

	out := runTool(t, webFetch{}, map[string]any{"url": srv.URL})
	for _, want := range []string{"Hello & world", "Visible text", "text/html"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, leak := range []string{"<script", "alert(", "<style", "<h1>", "&amp;"} {
		if strings.Contains(out, leak) {
			t.Errorf("leaked raw HTML/script %q", leak)
		}
	}
}

func TestWebFetchHTMLTokenizerHandlesAttributesAndEntities(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><p title="1 > 0">Tom&#39;s &nbsp; docs</p><script>visible = false</script><p>Next</p></body></html>`))
	}))
	defer srv.Close()

	out := runTool(t, webFetch{}, map[string]any{"url": srv.URL})
	for _, want := range []string{"Tom's docs", "Next"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "visible = false") || strings.Contains(out, "title=") || strings.Contains(out, "&#39;") {
		t.Fatalf("HTML tokenizer leaked markup/script/entity:\n%s", out)
	}
}

func TestWebFetchHTMLStructuredText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>Doc title</title></head><body>
<h1>Main</h1>
<p>Read the <a href="/guide?a=1&amp;b=2">guide</a>.</p>
<ul><li>First</li><li>Second</li></ul>
<pre>go test ./...
line two</pre>
<table><tr><th>Name</th><th>Value</th></tr><tr><td>A</td><td>42</td></tr></table>
</body></html>`))
	}))
	defer srv.Close()

	out := runTool(t, webFetch{}, map[string]any{"url": srv.URL})
	for _, want := range []string{
		"# Doc title",
		"# Main",
		"guide (/guide?a=1&b=2)",
		"- First",
		"- Second",
		"```\ngo test ./...\nline two\n```",
		"Name | Value",
		"A | 42",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("structured HTML output missing %q:\n%s", want, out)
		}
	}
}

// TestWebFetchPlain confirms non-HTML bodies pass through untouched (apart
// from the prepended status header).
func TestWebFetchPlain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("line1\nline2\n"))
	}))
	defer srv.Close()

	out := runTool(t, webFetch{}, map[string]any{"url": srv.URL})
	if !strings.Contains(out, "line1") || !strings.Contains(out, "line2") {
		t.Errorf("plain body content missing:\n%s", out)
	}
	if strings.Contains(out, "<") {
		t.Errorf("html reducer ran on text/plain: %s", out)
	}
}

// TestWebFetchSchemeRejected blocks anything that's not http(s) so the tool
// can't be tricked into reading file:// or arbitrary URI schemes.
func TestWebFetchSchemeRejected(t *testing.T) {
	_, err := webFetch{}.Execute(context.Background(), argsJSON(t, map[string]any{"url": "file:///etc/passwd"}))
	if err == nil || !strings.Contains(err.Error(), "http(s)") {
		t.Errorf("expected scheme rejection, got %v", err)
	}
}

func TestLsAndGlob(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte("hi"), 0o644)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)

	ls := runTool(t, listDir{}, map[string]any{"path": dir})
	if !strings.Contains(ls, "x.txt") || !strings.Contains(ls, "sub/") {
		t.Fatalf("ls result = %q", ls)
	}

	g := runTool(t, globTool{}, map[string]any{"pattern": filepath.Join(dir, "*.txt")})
	if !strings.Contains(g, "x.txt") {
		t.Fatalf("glob result = %q", g)
	}
}

func TestGlobRecursive(t *testing.T) {
	dir := t.TempDir()
	// Create a nested structure:
	// dir/a.go
	// dir/sub/b.go
	// dir/sub/deep/c.go
	// dir/sub/deep/c.txt
	// dir/other.txt
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0o644)
	os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "b.go"), []byte("package b"), 0o644)
	os.WriteFile(filepath.Join(dir, "sub", "deep", "c.go"), []byte("package c"), 0o644)
	os.WriteFile(filepath.Join(dir, "sub", "deep", "c.txt"), []byte("text"), 0o644)
	os.WriteFile(filepath.Join(dir, "other.txt"), []byte("other"), 0o644)

	// **  *.go should find all .go files recursively.
	out := runTool(t, globTool{}, map[string]any{"pattern": filepath.Join(dir, "**", "*.go")})
	if !strings.Contains(out, "a.go") {
		t.Errorf("missing a.go in:\n%s", out)
	}
	if !strings.Contains(out, "b.go") {
		t.Errorf("missing b.go in:\n%s", out)
	}
	if !strings.Contains(out, "c.go") {
		t.Errorf("missing c.go in:\n%s", out)
	}
	// Should not include .txt files.
	if strings.Contains(out, "other.txt") || strings.Contains(out, "c.txt") {
		t.Errorf("should not include .txt files:\n%s", out)
	}

	// **  *.txt should find all .txt files recursively.
	out2 := runTool(t, globTool{}, map[string]any{"pattern": filepath.Join(dir, "**", "*.txt")})
	if !strings.Contains(out2, "other.txt") {
		t.Errorf("missing other.txt in:\n%s", out2)
	}
	if !strings.Contains(out2, "c.txt") {
		t.Errorf("missing c.txt in:\n%s", out2)
	}

	// **  with no suffix should find all files.
	out3 := runTool(t, globTool{}, map[string]any{"pattern": filepath.Join(dir, "**")})
	if !strings.Contains(out3, "a.go") || !strings.Contains(out3, "c.txt") {
		t.Errorf("bare ** should find all files:\n%s", out3)
	}
}

func TestGlobForwardSlashPattern(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0o755)
	os.WriteFile(filepath.Join(dir, "top.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "sub", "deep", "nested.txt"), []byte("y"), 0o644)
	t.Chdir(dir)

	out := runTool(t, globTool{}, map[string]any{"pattern": "**/*.txt"})
	if !strings.Contains(out, "top.txt") || !strings.Contains(out, "nested.txt") {
		t.Errorf("forward-slash recursive pattern should match every .txt:\n%s", out)
	}
}

func TestGlobRecursiveDoublestarBracePattern(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("go"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("txt"), 0o644)
	os.WriteFile(filepath.Join(dir, "c.md"), []byte("md"), 0o644)

	out := runTool(t, globTool{}, map[string]any{"pattern": filepath.Join(dir, "**", "*.{go,txt}")})
	if !strings.Contains(out, "a.go") || !strings.Contains(out, "b.txt") {
		t.Fatalf("brace pattern should match go and txt:\n%s", out)
	}
	if strings.Contains(out, "c.md") {
		t.Fatalf("brace pattern should not match markdown:\n%s", out)
	}
}

func TestGlobRecursiveNoMatches(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "a.go"), []byte("package a"), 0o644)

	out := runTool(t, globTool{}, map[string]any{"pattern": filepath.Join(dir, "**", "*.py")})
	if !strings.Contains(out, "(no matches)") {
		t.Errorf("expected (no matches), got:\n%s", out)
	}
}

func TestGlobNoMatches(t *testing.T) {
	dir := t.TempDir()
	out := runTool(t, globTool{}, map[string]any{"pattern": filepath.Join(dir, "*.xyz")})
	if !strings.Contains(out, "(no matches)") {
		t.Errorf("expected (no matches), got:\n%s", out)
	}
}

// GB18030 encoding integration tests (issue #2637)

func TestReadFileGB18030(t *testing.T) {
	f := filepath.Join(t.TempDir(), "gbk.txt")
	gb, err := simplifiedchinese.GB18030.NewEncoder().String("你好世界\n第二行")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	os.WriteFile(f, []byte(gb), 0o644)

	out := runTool(t, readFile{}, map[string]any{"path": f})
	if !strings.Contains(out, "你好世界") || !strings.Contains(out, "第二行") {
		t.Errorf("expected decoded Chinese text, got:\n%s", out)
	}
}

func TestEditFileGB18030RoundTrip(t *testing.T) {
	f := filepath.Join(t.TempDir(), "gbk.txt")
	original, _ := simplifiedchinese.GB18030.NewEncoder().String("你好世界\n第二行\n")
	os.WriteFile(f, []byte(original), 0o644)

	runTool(t, editFile{}, map[string]any{
		"path":       f,
		"old_string": "第二行",
		"new_string": "新的行",
	})

	got, _ := os.ReadFile(f)
	// The file should still be GB18030-encoded (not silently converted to UTF-8).
	dec, _ := simplifiedchinese.GB18030.NewDecoder().Bytes(got)
	if string(dec) != "你好世界\n新的行\n" {
		t.Errorf("after edit = %q (decoded)", dec)
	}
}

func TestMultiEditGB18030RoundTrip(t *testing.T) {
	f := filepath.Join(t.TempDir(), "gbk.txt")
	original, _ := simplifiedchinese.GB18030.NewEncoder().String("package old\n\nfunc old() {\n\told()\n}\n")
	os.WriteFile(f, []byte(original), 0o644)

	runTool(t, multiEdit{}, map[string]any{
		"path": f,
		"edits": []map[string]any{
			{"old_string": "package old", "new_string": "package new"},
			{"old_string": "old", "new_string": "reasonix", "replace_all": true},
		},
	})

	got, _ := os.ReadFile(f)
	dec, _ := simplifiedchinese.GB18030.NewDecoder().Bytes(got)
	want := "package new\n\nfunc reasonix() {\n\treasonix()\n}\n"
	if string(dec) != want {
		t.Errorf("after multi_edit = %q (decoded), want %q", dec, want)
	}
}

func TestGrepGB18030(t *testing.T) {
	dir := t.TempDir()
	gb, _ := simplifiedchinese.GB18030.NewEncoder().String("你好世界\n包含函数的行\n")
	os.WriteFile(filepath.Join(dir, "gbk.txt"), []byte(gb), 0o644)

	out := runTool(t, grepTool{}, map[string]any{"pattern": "函数", "path": dir})
	if !strings.Contains(out, "函数") {
		t.Errorf("expected match in decoded GB18030 text, got:\n%s", out)
	}
}

func TestGrepGB18030TruncationDoesNotLeakGoroutine(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	var content strings.Builder
	for range grepMaxMatches {
		content.WriteString("命中\n")
	}
	content.WriteString(strings.Repeat("padding\n", 2000))
	gb, err := simplifiedchinese.GB18030.NewEncoder().String(content.String())
	if err != nil {
		t.Fatalf("encode GB18030: %v", err)
	}

	path := filepath.Join(t.TempDir(), "many-matches.gbk")
	if err := os.WriteFile(path, []byte(gb), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	out := runTool(t, grepTool{}, map[string]any{"pattern": "命中", "path": path})
	if got := strings.Count(out, ":命中"); got != grepMaxMatches {
		t.Fatalf("matches = %d, want %d:\n%s", got, grepMaxMatches, out)
	}
	if !strings.Contains(out, "truncated at 200 matches") {
		t.Fatalf("missing truncation marker:\n%s", out)
	}
}
