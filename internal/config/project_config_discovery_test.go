package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProjectConfigPath pins the project-config discovery rule: reasonix.toml
// wins when present, else the standard .reasonix/config.toml (the creation
// default).
func TestProjectConfigPath(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())

	t.Run("legacy plain only", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "reasonix.toml"), []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
		if got := ProjectConfigPath(root); got != filepath.Join(root, "reasonix.toml") {
			t.Fatalf("ProjectConfigPath = %q, want reasonix.toml", got)
		}
	})

	t.Run("local only", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".reasonix"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".reasonix", "config.toml"), []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(root, ".reasonix", "config.toml")
		if got := ProjectConfigPath(root); got != want {
			t.Fatalf("ProjectConfigPath = %q, want %q", got, want)
		}
	})

	t.Run("legacy plain wins over local", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "reasonix.toml"), []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, ".reasonix"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".reasonix", "config.toml"), []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
		if got := ProjectConfigPath(root); got != filepath.Join(root, "reasonix.toml") {
			t.Fatalf("ProjectConfigPath = %q, want reasonix.toml to win", got)
		}
	})

	t.Run("neither defaults to local", func(t *testing.T) {
		root := t.TempDir()
		want := filepath.Join(root, ".reasonix", "config.toml")
		if got := ProjectConfigPath(root); got != want {
			t.Fatalf("ProjectConfigPath = %q, want %q default", got, want)
		}
	})

	t.Run("bothProjectConfigsExist only for plain+local", func(t *testing.T) {
		root := t.TempDir()
		if bothProjectConfigsExist(root) {
			t.Fatal("bothProjectConfigsExist true with no files")
		}
		if err := os.WriteFile(filepath.Join(root, "reasonix.toml"), []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
		if bothProjectConfigsExist(root) {
			t.Fatal("bothProjectConfigsExist true with only the plain file")
		}
		if err := os.MkdirAll(filepath.Join(root, ".reasonix"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".reasonix", "config.toml"), []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
		if !bothProjectConfigsExist(root) {
			t.Fatal("bothProjectConfigsExist false with plain+local")
		}
	})
}

// TestLoadForRootProjectConfig pins discovery through a full load: the
// standard .reasonix/config.toml is read, and reasonix.toml wins when present.
func TestLoadForRootProjectConfig(t *testing.T) {
	isolateUserConfigHome(t)
	t.Setenv("REASONIX_HOME", "")

	t.Run("local only", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".reasonix"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".reasonix", "config.toml"), []byte("default_model = \"local/model\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadForRoot(root)
		if err != nil {
			t.Fatalf("LoadForRoot: %v", err)
		}
		if cfg.DefaultModel != "local/model" {
			t.Fatalf("DefaultModel = %q, want .reasonix/config.toml content", cfg.DefaultModel)
		}
	})

	t.Run("legacy plain wins over local", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".reasonix"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".reasonix", "config.toml"), []byte("default_model = \"local/model\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "reasonix.toml"), []byte("default_model = \"plain/model\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadForRoot(root)
		if err != nil {
			t.Fatalf("LoadForRoot: %v", err)
		}
		if cfg.DefaultModel != "plain/model" {
			t.Fatalf("DefaultModel = %q, want reasonix.toml to win", cfg.DefaultModel)
		}
	})
}

// TestSourcePathForRootPicksLocal pins that the source-resolution helper
// follows the same rule so writes/edits target the loaded file.
func TestSourcePathForRootPicksLocal(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".reasonix"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, ".reasonix", "config.toml")
	if err := os.WriteFile(want, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := SourcePathForRoot(root); got != want {
		t.Fatalf("SourcePathForRoot = %q, want %q", got, want)
	}
}

// TestIsProjectConfigFile pins the recognizer and its user-config exclusion.
func TestIsProjectConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	if !IsProjectConfigFile(filepath.Join(home, "proj", "reasonix.toml")) {
		t.Error("legacy plain reasonix.toml should be a project config")
	}
	if !IsProjectConfigFile(filepath.Join(home, "proj", ".reasonix", "config.toml")) {
		t.Error(".reasonix/config.toml should be a project config")
	}
	if IsProjectConfigFile(userConfigPath()) {
		t.Error("user config should not be a project config")
	}
	if IsProjectConfigFile(filepath.Join(home, "proj", "other.toml")) {
		t.Error("unrelated file should not be a project config")
	}
}
