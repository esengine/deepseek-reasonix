package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resolveSetupTargets treats any unrecognized argument as the config path, so
// before this guard `reasonix setup --help` wrote a config file literally named
// "--help" into the working directory — and so did every mistyped flag.
func TestSetupRejectsDashedArgumentsInsteadOfWritingThem(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	for _, arg := range []string{"--help", "-h"} {
		if rc := setupConfig([]string{arg}); rc != 0 {
			t.Fatalf("setup %s returned %d, want 0 (help is not an error)", arg, rc)
		}
	}
	if rc := setupConfig([]string{"--not-a-flag"}); rc != 2 {
		t.Fatalf("setup --not-a-flag returned %d, want 2", rc)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "-") {
			t.Fatalf("setup wrote a file from a flag argument: %s", filepath.Join(dir, e.Name()))
		}
	}
}
