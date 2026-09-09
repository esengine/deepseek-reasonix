package store

import (
	"path/filepath"
	"strings"
)

// Legacy display maps are directory-scoped compatibility artifacts. Session
// deletion edits the matching map entry rather than deleting other sessions.
func SessionLegacyDisplays(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, ".display.json")
}

func SessionLegacyPlannerDisplays(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, ".planner-display.json")
}
