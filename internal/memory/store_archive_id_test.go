package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStoreDeleteByIDRemovesFromAllDirs covers the ID-reference path of
// Archive: an ID may name the same fact in several directories (migration
// duplicates share the deterministic legacy ID), so deletion by ID must
// archive from every directory — the unqualified-name path already does.
func TestStoreDeleteByIDRemovesFromAllDirs(t *testing.T) {
	dir := t.TempDir()
	s := Store{
		Dir:       filepath.Join(dir, "project", "memory"),
		GlobalDir: filepath.Join(dir, "global"),
	}

	name := "prefers-tabs"
	for _, d := range []string{s.Dir, s.GlobalDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		m := Memory{Name: name, Description: "user pref", Type: TypeUser, Body: "use tabs"}
		if err := os.WriteFile(filepath.Join(d, name+".md"), []byte(render(m, name)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := reindexIn(d, name, m); err != nil {
			t.Fatal(err)
		}
	}

	// Both copies share one identity (same name + scope → same legacy ID).
	list := s.List()
	if len(list) != 1 {
		t.Fatalf("want 1 deduplicated memory, got %d", len(list))
	}

	// Delete by the identity ID, not the name.
	if err := s.Delete(list[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{s.Dir, s.GlobalDir} {
		if _, err := os.Stat(filepath.Join(d, name+".md")); !os.IsNotExist(err) {
			t.Fatalf("copy in %s should be gone after ID delete", d)
		}
	}
	if idx := s.Index(); idx != "" {
		t.Fatalf("Index() should be empty after deleting all entries, got:\n%s", idx)
	}
}
