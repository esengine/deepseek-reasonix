package config

import (
	"os"
	"path/filepath"
	"strings"
)

func renderScopeForPath(path string) RenderScope {
	if isUserConfigPath(path) {
		return RenderScopeUser
	}
	return RenderScopeProject
}

func isUserConfigPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	for _, uc := range userConfigCandidatePaths() {
		uc = strings.TrimSpace(uc)
		if uc == "" {
			continue
		}
		pathAbs, pathErr := filepath.Abs(path)
		ucAbs, ucErr := filepath.Abs(uc)
		if pathErr == nil && ucErr == nil {
			if filepath.Clean(pathAbs) == filepath.Clean(ucAbs) {
				return true
			}
			continue
		}
		if filepath.Clean(path) == filepath.Clean(uc) {
			return true
		}
	}
	return false
}

// IsUserConfigPath reports whether path is one of Reasonix's current or legacy
// user-global config locations. Other paths use project-scoped rendering.
func IsUserConfigPath(path string) bool {
	return isUserConfigPath(path)
}

// Save writes the configuration back to the file it was loaded from
// (SourcePath), or to ./reasonix.toml when none exists yet — the conventional
// project-local target a fresh GUI session would create.
func (c *Config) Save() error {
	path := SourcePath()
	if path == "" {
		path = projectConfigLocal
	}
	return c.SaveTo(path)
}

// SaveForRoot saves root's project config when it exists, falling back to the
// user's global config when root has no reasonix.toml. Existing project files
// are edited from their own TOML only, never from a runtime user+project merge.
func (c *Config) SaveForRoot(root string) error {
	root = resolveRoot(root)
	projectTOML := ProjectConfigPath(root)
	if _, err := os.Stat(projectTOML); err == nil {
		projectCfg := LoadForEditWithoutCredentials(projectTOML)
		return projectCfg.SaveTo(projectTOML)
	}
	if uc := userConfigPath(); uc != "" {
		if err := os.MkdirAll(filepath.Dir(uc), 0o755); err != nil {
			return err
		}
		return c.SaveTo(uc)
	}
	return c.SaveTo(projectTOML)
}
