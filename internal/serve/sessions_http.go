package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/store"
)

type sessionListEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Title      string `json:"title,omitempty"`
	Turns      int    `json:"turns,omitempty"`
	Current    bool   `json:"current,omitempty"`
	Running    bool   `json:"running,omitempty"`
	TakenOver  bool   `json:"takenOver,omitempty"`
	MtimeMilli int64  `json:"mtimeMilli"`
}

// listSessionDir enumerates saved session files in dir with the same
// enrichment as GET /sessions (event-log aware titles and turn counts,
// cleanup exclusion), newest first. running maps canonical session paths
// to detached-runtime activity; callers without detached runtimes pass nil.
func (s *Server) listSessionDir(ctx context.Context, dir, currentPath string, running map[string]bool) []sessionListEntry {
	var out []sessionListEntry
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []sessionListEntry{}
	}
	current := agent.CanonicalSessionPath(currentPath)
	for _, entry := range entries {
		if entry.IsDir() || !store.IsSessionTranscriptName(entry.Name()) {
			continue
		}
		path := agent.CanonicalSessionPath(filepath.Join(dir, entry.Name()))
		if agent.IsCleanupPending(path) {
			continue
		}
		mtime := agent.SessionContentModTime(path)
		cleanPath := agent.CanonicalSessionPath(path)
		row := sessionListEntry{Name: strings.TrimSuffix(entry.Name(), ".jsonl"), Path: path, Current: cleanPath == current, Running: running[cleanPath], MtimeMilli: mtime.UnixMilli()}
		first, turns, cached := agent.SessionPreviewCached(path)
		if !cached {
			first, turns = agent.SessionPreview(path)
		}
		if turns > 0 {
			row.Turns = turns
			row.Title = s.sessionTitle(ctx, entry.Name(), first, mtime.UnixNano())
		}
		out = append(out, row)
	}
	// reverse so newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if out == nil {
		out = []sessionListEntry{}
	}
	return out
}

// sessions lists saved sessions with event-log-aware titles and turn counts.
func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	ctrl := s.ctl()
	dir := ctrl.SessionDir()
	if dir == "" {
		writeJSON(w, []any{})
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeJSON(w, []any{})
		return
	}
	current := agent.CanonicalSessionPath(ctrl.SessionPath())
	running := map[string]bool{}
	s.detachedMu.Lock()
	for path, detached := range s.detached {
		running[filepath.Clean(path)] = controllerHasActiveRuntimeWork(detached.ctrl)
	}
	s.detachedMu.Unlock()
	out := make([]sessionListEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !store.IsSessionTranscriptName(entry.Name()) {
			continue
		}
		path := agent.CanonicalSessionPath(filepath.Join(dir, entry.Name()))
		if agent.IsCleanupPending(path) {
			continue
		}
		mtime := agent.SessionContentModTime(path)
		cleanPath := agent.CanonicalSessionPath(path)
		row := sessionListEntry{
			Name:       strings.TrimSuffix(entry.Name(), ".jsonl"),
			Path:       path,
			Current:    cleanPath == current,
			Running:    running[cleanPath],
			TakenOver:  s.sessionMirrored(cleanPath) || leaseHeldByForeignRuntime(cleanPath),
			MtimeMilli: mtime.UnixMilli(),
		}
		if row.Current {
			row.Running = controllerHasActiveRuntimeWork(ctrl) && !row.TakenOver
		}
		first, turns, cached := agent.SessionPreviewCached(path)
		if !cached {
			first, turns = agent.SessionPreview(path)
		}
		if turns > 0 {
			row.Turns = turns
			row.Title = s.sessionTitle(r.Context(), entry.Name(), first, mtime.UnixNano())
		}
		out = append(out, row)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	writeJSON(w, out)
}


// projects lists saved sessions across every project the serve can see.
// Read-only multi-project browsing for remote clients (#8789): it reads the
// desktop project registry (desktop-projects.json) and resolves each
// project's session dir via config.ProjectSessionDir. Without the registry
// the serve's own workspace is the single project.
func (s *Server) projects(w http.ResponseWriter, r *http.Request) {
	type project struct {
		Root string `json:"root"`
	}
	type projectSessions struct {
		Root     string             `json:"root"`
		Sessions []sessionListEntry `json:"sessions"`
	}
	roots := []string{}
	home := config.ReasonixHomeDir()
	if data, err := os.ReadFile(filepath.Join(home, "desktop-projects.json")); err == nil {
		var saved struct {
			Projects []project `json:"projects"`
		}
		if json.Unmarshal(data, &saved) == nil {
			for _, p := range saved.Projects {
				if root := strings.TrimSpace(p.Root); root != "" {
					roots = append(roots, root)
				}
			}
		}
	}
	if len(roots) == 0 {
		if w := strings.TrimSpace(s.ctl().WorkspaceRoot()); w != "" {
			roots = append(roots, w)
		}
	}
	current := s.ctl().SessionPath()
	out := []projectSessions{}
	seen := map[string]bool{}
	for _, root := range roots {
		root = filepath.Clean(root)
		if seen[root] {
			continue
		}
		seen[root] = true
		ps := projectSessions{Root: root, Sessions: s.listSessionDir(r.Context(), config.ProjectSessionDir(root), current, nil)}
		out = append(out, ps)
	}
	writeJSON(w, out)
}
