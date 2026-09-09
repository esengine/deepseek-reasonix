package serve

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/store"
)

// GET /projects — read-only multi-project session browser.
//
// Reads the same desktop-projects.json the desktop sidebar uses, resolves each
// project's session directory, and returns the per-session metadata in the same
// shape as GET /sessions.  This endpoint does NOT bind to a different workspace;
// submit/resume/approve remain bound to the serve instance's startup directory.
func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	type sessionEntry struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		Title   string `json:"title,omitempty"`
		Turns   int    `json:"turns,omitempty"`
		Current bool   `json:"current,omitempty"`
	}
	type projectEntry struct {
		Root     string         `json:"root"`
		Name     string         `json:"name,omitempty"`
		Sessions []sessionEntry `json:"sessions"`
	}

	home := config.ReasonixHomeDir()
	if home == "" {
		writeJSON(w, []projectEntry{})
		return
	}

	// Parse desktop-projects.json.
	type pf struct {
		Projects []struct {
			Root string `json:"root"`
		} `json:"projects"`
	}
	var saved pf
	if data, err := os.ReadFile(filepath.Join(home, "desktop-projects.json")); err == nil {
		_ = json.Unmarshal(data, &saved)
	}

	// Always include the global workspace.
	seen := map[string]bool{}
	var roots []string
	addRoot := func(root string) {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "" || root == "." || seen[root] {
			return
		}
		seen[root] = true
		roots = append(roots, root)
	}
	addRoot(filepath.Join(home, "global-workspace"))
	for _, p := range saved.Projects {
		addRoot(p.Root)
	}

	currentSession := ""
	if s.ctl() != nil {
		currentSession = filepath.Clean(s.ctl().SessionPath())
	}

	var out []projectEntry
	for _, root := range roots {
		sessDir := config.ProjectSessionDir(root)
		if sessDir == "" {
			continue
		}
		entries, err := os.ReadDir(sessDir)
		if err != nil {
			continue
		}
		var sessions []sessionEntry
		for _, e := range entries {
			if e.IsDir() || !store.IsSessionTranscriptName(e.Name()) {
				continue
			}
			path := filepath.Join(sessDir, e.Name())
			if agent.IsCleanupPending(path) {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".jsonl")
			se := sessionEntry{
				Name:    name,
				Path:    path,
				Current: filepath.Clean(path) == currentSession,
			}
			if first, turns := agent.SessionPreview(path); turns > 0 {
				se.Turns = turns
				se.Title = s.sessionTitle(r.Context(), e.Name(), first, agent.SessionContentModTime(path).UnixNano())
			}
			sessions = append(sessions, se)
		}
		// Reverse for newest-first.
		for i, j := 0, len(sessions)-1; i < j; i, j = i+1, j-1 {
			sessions[i], sessions[j] = sessions[j], sessions[i]
		}
		if sessions == nil {
			sessions = []sessionEntry{}
		}
		out = append(out, projectEntry{
			Root:     root,
			Name:     filepath.Base(root),
			Sessions: sessions,
		})
	}
	if out == nil {
		out = []projectEntry{}
	}
	writeJSON(w, out)
}
