package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/provider"
)

// fakeProjectsCtrl answers the workspace/session surface GET /projects uses.
type fakeProjectsCtrl struct {
	control.SessionAPI // nil-embed: only the three methods below are exercised
	root               string
	dir                string
	path               string
}

func (f *fakeProjectsCtrl) WorkspaceRoot() string { return f.root }
func (f *fakeProjectsCtrl) SessionDir() string    { return f.dir }
func (f *fakeProjectsCtrl) SessionPath() string   { return f.path }

func newProjectsServer(t *testing.T, root string) *Server {
	t.Helper()
	dir := config.ProjectSessionDir(root)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &Server{
		ctrl:   &fakeProjectsCtrl{root: root, dir: dir, path: filepath.Join(dir, "current.jsonl")},
		titles: newTitleCache(dir),
	}
}

// writeDesktopRegistry writes a desktop-projects.json with the given roots.
func writeDesktopRegistry(t *testing.T, roots ...string) {
	t.Helper()
	home := config.ReasonixHomeDir()
	if home == "" {
		t.Fatal("ReasonixHomeDir empty under isolated test env")
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	type project struct {
		Root string `json:"root"`
	}
	b, err := json.Marshal(struct {
		Projects []project `json:"projects"`
	}{Projects: func() []project {
		out := make([]project, 0, len(roots))
		for _, r := range roots {
			out = append(out, project{Root: r})
		}
		return out
	}()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "desktop-projects.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedSession writes a valid one-user-turn session file into a project's
// session dir so SessionPreview reports turns=1.
func seedSession(t *testing.T, root, name string) {
	t.Helper()
	dir := config.ProjectSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sess := agent.NewSession("system")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "hello from " + name})
	if err := sess.Save(filepath.Join(dir, name+".jsonl")); err != nil {
		t.Fatal(err)
	}
}

type projectsResponse []struct {
	Root     string         `json:"root"`
	Sessions []sessionListEntry `json:"sessions"`
}

func callProjects(t *testing.T, srv *Server) projectsResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.projects(rec, httptest.NewRequest(http.MethodGet, "/projects", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var out projectsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestProjectsListsDesktopRegistry(t *testing.T) {
	rootA := filepath.Join(t.TempDir(), "projA")
	rootB := filepath.Join(t.TempDir(), "projB")
	writeDesktopRegistry(t, rootA, rootB)
	seedSession(t, rootA, "alpha")
	seedSession(t, rootB, "beta")

	srv := newProjectsServer(t, rootA)
	out := callProjects(t, srv)

	if len(out) != 2 {
		t.Fatalf("projects = %d, want 2 (%+v)", len(out), out)
	}
	found := map[string]int{}
	for _, p := range out {
		found[p.Root] = len(p.Sessions)
	}
	if found[filepath.Clean(rootA)] != 1 || found[filepath.Clean(rootB)] != 1 {
		t.Fatalf("session counts = %+v, want 1 per project", found)
	}
	// The session entry carries the same shape as /sessions: turns filled.
	for _, p := range out {
		if len(p.Sessions) != 1 || p.Sessions[0].Turns != 1 || p.Sessions[0].Title == "" {
			t.Fatalf("session entry malformed: %+v", p.Sessions)
		}
	}
}

func TestProjectsFallsBackToServeWorkspace(t *testing.T) {
	// Other tests may have written a registry into the shared isolated home;
	// the fallback path must be exercised without one.
	_ = os.Remove(filepath.Join(config.ReasonixHomeDir(), "desktop-projects.json"))
	root := filepath.Join(t.TempDir(), "only")
	seedSession(t, root, "solo")
	// No desktop-projects.json: the serve's own workspace is the single project.
	srv := newProjectsServer(t, root)
	out := callProjects(t, srv)
	if len(out) != 1 {
		t.Fatalf("projects = %d, want 1 (fallback)", len(out))
	}
	if filepath.Clean(out[0].Root) != filepath.Clean(root) {
		t.Fatalf("root = %q, want %q", out[0].Root, root)
	}
	if len(out[0].Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(out[0].Sessions))
	}
}

func TestProjectsEmptySessionDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "empty")
	writeDesktopRegistry(t, root)
	srv := newProjectsServer(t, root)
	out := callProjects(t, srv)
	if len(out) != 1 || len(out[0].Sessions) != 0 {
		t.Fatalf("expected one project with zero sessions, got %+v", out)
	}
}

func TestProjectsDedupesRoots(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dup")
	writeDesktopRegistry(t, root, root)
	seedSession(t, root, "one")
	srv := newProjectsServer(t, root)
	out := callProjects(t, srv)
	if len(out) != 1 {
		t.Fatalf("projects = %d, want 1 (dedup)", len(out))
	}
}
