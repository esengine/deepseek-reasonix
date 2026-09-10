package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/diff"
)

func TestOptionsOverrideRetentionDefaults(t *testing.T) {
	s := New("", t.TempDir(), WithRetainCheckpoints(7), WithBlobQuota(1<<20))
	if s.retainN != 7 {
		t.Fatalf("retainN = %d, want 7", s.retainN)
	}
	if s.blobQuota != 1<<20 {
		t.Fatalf("blobQuota = %d, want %d", s.blobQuota, int64(1<<20))
	}
}

func TestOptionsIgnoreNonPositiveValues(t *testing.T) {
	// Zero means "unset" in TOML, so it must not clobber the default with 0 and
	// silently disable retention.
	s := New("", t.TempDir(), WithRetainCheckpoints(0), WithBlobQuota(-1))
	if s.retainN != DefaultRetainCheckpoints {
		t.Fatalf("retainN = %d, want default %d", s.retainN, DefaultRetainCheckpoints)
	}
	if s.blobQuota != DefaultBlobQuotaBytes {
		t.Fatalf("blobQuota = %d, want default %d", s.blobQuota, int64(DefaultBlobQuotaBytes))
	}
}

func TestNewIgnoresNilOption(t *testing.T) {
	s := New("", t.TempDir(), nil, WithRetainCheckpoints(3))
	if s.retainN != 3 {
		t.Fatalf("retainN = %d, want 3", s.retainN)
	}
}

// seedTurns writes one edited file per turn and captures its preimage, so each
// turn ends up with a restorable payload on disk.
func seedTurns(t *testing.T, s *Store, root string, turns int, body string) {
	t.Helper()
	for i := 0; i < turns; i++ {
		path := filepath.Join(root, fmt.Sprintf("f%d.txt", i))
		original := fmt.Sprintf("turno %d\n%s", i, body)
		if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
			t.Fatal(err)
		}
		s.Begin(i, fmt.Sprintf("edit %d", i), 0)
		s.Snapshot(diff.Change{Path: path, Kind: diff.Modify, OldText: original})
	}
}

// checkpointsWithPayloads counts checkpoints still holding restorable content,
// which is what the retention GC trims.
func checkpointsWithPayloads(s *Store) int {
	n := 0
	for _, c := range s.all() {
		if len(c.Files) > 0 || c.SchemaVersion >= SchemaV3 {
			n++
		}
	}
	return n
}

// The ordering guarantee: the startup GC inside New reads retainN (via
// pruneV3TurnsLocked), so a configured retention has to be applied before that
// prune. Otherwise reopening a session would trim using DefaultRetainCheckpoints
// and discard payloads the operator asked to keep.
func TestConfiguredRetentionAppliesToStartupGC(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(t.TempDir(), "sess.ckpt")

	const turns = 6
	const retain = 2

	s := New(dir, root)
	seedTurns(t, s, root, turns, "")
	if got := checkpointsWithPayloads(s); got != turns {
		t.Fatalf("before reopen: %d checkpoints with payloads, want %d", got, turns)
	}

	reopened := New(dir, root, WithRetainCheckpoints(retain))
	if reopened.retainN != retain {
		t.Fatalf("retainN = %d, want %d", reopened.retainN, retain)
	}
	if got := checkpointsWithPayloads(reopened); got > retain {
		t.Fatalf("startup GC kept %d checkpoints with payloads, want at most %d", got, retain)
	}
}

// WithBlobQuota has to reach the prune decision, not just the struct field. The
// retain count here is deliberately generous so the turn-count path cannot
// trigger a prune: only the byte budget can.
func TestConfiguredBlobQuotaPrunesWithinRetainCount(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(t.TempDir(), "sess.ckpt")

	const turns = 5
	body := strings.Repeat("x", 4096)

	s := New(dir, root)
	seedTurns(t, s, root, turns, body)
	if got := checkpointsWithPayloads(s); got != turns {
		t.Fatalf("before reopen: %d checkpoints with payloads, want %d", got, turns)
	}

	reopened := New(dir, root, WithRetainCheckpoints(turns+10), WithBlobQuota(1024))
	if got := checkpointsWithPayloads(reopened); got >= turns {
		t.Fatalf("byte-budget GC kept %d checkpoints with payloads, want fewer than %d", got, turns)
	}
}
