package agent

import (
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
)

// TestSnapshotDigestReusesPersistedBaselineForSchemaTwo pins the schema-2 half
// of the defensive-snapshot cost. Once a checkpoint has committed the
// transcript, learning its digest again is a json.Marshal + sha256 pass over
// every message, which on a large session dominates a switch/close snapshot.
// A schema-2 save only records that digest and publishes it into derived files
// — it never uses it to decide what to append — so the baseline value stands in
// while the transcript is unchanged, and must not once it moves.
func TestSnapshotDigestReusesPersistedBaselineForSchemaTwo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	s := NewSession("system")
	s.Add(provider.Message{Role: provider.RoleUser, Content: "task"})
	bindSessionWriter(t, s, path)
	if err := s.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	probe, err := probeLogForSave(path)
	if err != nil {
		t.Fatal(err)
	}
	if route := s.dagSaveRoute(path, probe); route == dagRouteSchemaOne {
		t.Fatalf("route = %v, want a schema-2 log for this test", route)
	}

	s.Add(provider.Message{Role: provider.RoleAssistant, Content: "done"})
	if err := s.SaveToolCheckpoint(path, false); err != nil {
		t.Fatal(err)
	}
	state := s.persistState(path)
	if !state.ok || !state.projectionPending {
		t.Fatalf("checkpoint state = %+v, want derived files left pending", state)
	}

	msgs, version, _ := s.snapshotWithVersion()
	reused, err := s.snapshotDigest(path, msgs, version)
	if err != nil {
		t.Fatal(err)
	}
	if reused != state.digest {
		t.Fatal("snapshotDigest recomputed instead of reusing the persisted baseline")
	}
	// The shortcut must agree with what a full pass would produce, otherwise it
	// would publish a digest that does not describe the committed transcript.
	want, _, err := digestAndSizeSessionMessages(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if want != state.digest || reused != want {
		t.Fatal("reused digest disagrees with a full digest of the committed transcript")
	}

	s.Add(provider.Message{Role: provider.RoleUser, Content: "more"})
	msgs, version, _ = s.snapshotWithVersion()
	moved, err := s.snapshotDigest(path, msgs, version)
	if err != nil {
		t.Fatal(err)
	}
	if moved == state.digest {
		t.Fatal("snapshotDigest kept a digest that no longer describes the transcript")
	}
}
