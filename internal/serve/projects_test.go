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

func TestListProjectsEndpoint(t *testing.T) {
	// Set up a temp REASONIX_HOME with desktop-projects.json and session files.
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)

	// Create a fake project workspace.
	projRoot := filepath.Join(home, "test-project")
	if err := os.MkdirAll(projRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	sessDir := config.ProjectSessionDir(projRoot)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a session file.
	s := agent.NewSession("system prompt")
	s.Add(provider.Message{Role: provider.RoleUser, Content: "hello"})
	if err := s.Save(filepath.Join(sessDir, "test-session.jsonl")); err != nil {
		t.Fatal(err)
	}

	// Write desktop-projects.json pointing to our test project.
	type pf struct {
		Projects []struct {
			Root string `json:"root"`
		} `json:"projects"`
	}
	pfData, _ := json.Marshal(pf{
		Projects: []struct {
			Root string `json:"root"`
		}{{Root: projRoot}},
	})
	if err := os.WriteFile(filepath.Join(home, "desktop-projects.json"), pfData, 0o644); err != nil {
		t.Fatal(err)
	}

	// Also create the global-workspace sessions dir so it's included.
	gwRoot := filepath.Join(home, "global-workspace")
	gwSessDir := config.ProjectSessionDir(gwRoot)
	t.Logf("global-workspace sessions dir: %s", gwSessDir)
	if err := os.MkdirAll(gwSessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	bc := NewBroadcaster()
	ctrl := control.New(control.Options{Sink: bc})
	srv := httptest.NewServer(New(ctrl, bc, config.ServeConfig{}).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/projects")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var projects []struct {
		Root     string `json:"root"`
		Name     string `json:"name"`
		Sessions []struct {
			Name  string `json:"name"`
			Title string `json:"title,omitempty"`
			Turns int    `json:"turns,omitempty"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&projects); err != nil {
		t.Fatal(err)
	}

	// Should have at least global-workspace + test-project.
	if len(projects) < 2 {
		t.Fatalf("got %d projects, want >= 2", len(projects))
	}

	// Find our test project.
	var found bool
	for _, p := range projects {
		if filepath.Clean(p.Root) == filepath.Clean(projRoot) {
			found = true
			if len(p.Sessions) != 1 {
				t.Fatalf("test-project sessions = %d, want 1", len(p.Sessions))
			}
			if p.Sessions[0].Name != "test-session" {
				t.Fatalf("session name = %q, want test-session", p.Sessions[0].Name)
			}
			if p.Sessions[0].Turns != 1 {
				t.Fatalf("session turns = %d, want 1", p.Sessions[0].Turns)
			}
		}
	}
	if !found {
		t.Fatal("test-project not found in /projects response")
	}
}
