package agent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
	"reasonix/internal/store"
)

type checkpointObserver struct{ events []SessionPersistEvent }

func (o *checkpointObserver) EnqueueSessionPersist(e SessionPersistEvent) bool {
	o.events = append(o.events, e)
	return true
}

func TestToolCheckpointDefersOnlyDerivedProjection(t *testing.T) {
	path := schemaOneSessionPath(t, "session.jsonl")
	s := NewSession("system")
	s.Add(provider.Message{Role: provider.RoleUser, Content: "task"})
	bindSessionWriter(t, s, path)
	observer := &checkpointObserver{}
	s.SetPersistObserver(observer)
	if err := s.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.SessionDisplayIndex(path))
	if err != nil {
		t.Fatal(err)
	}
	observer.events = nil
	for i := range 3 {
		id := fmt.Sprint(i)
		s.AddBatch(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: id, Name: "write_file", Arguments: `{}`}}}, provider.Message{Role: provider.RoleTool, ToolCallID: id, Name: "write_file", Content: "done", ToolRunState: provider.ToolRunCompleted})
		if err := s.SaveToolCheckpoint(path, false); err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadSession(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.Snapshot()) != len(s.Snapshot()) {
			t.Fatal("canonical receipt was not durable")
		}
	}
	after, err := os.ReadFile(store.SessionDisplayIndex(path))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || len(observer.events) != 0 {
		t.Fatal("tool checkpoint refreshed the display projection")
	}
	// The deferred projection must not veto the snapshot no-op: the transcript
	// itself is committed, and only its derived files are behind. Gating the
	// no-op on it would send every defensive switch/close snapshot on a large
	// session through the serialize + digest + probe path it exists to avoid.
	if !s.snapshotUpToDate(path) {
		t.Fatal("deferred projection vetoed the transcript no-op")
	}
	if !s.DerivedFilesPending(path) {
		t.Fatal("deferred projection is not reported as pending")
	}
	if err := s.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	after, err = os.ReadFile(store.SessionDisplayIndex(path))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, after) || len(observer.events) != 1 || !s.snapshotUpToDate(path) {
		t.Fatal("normal save did not publish deferred projection")
	}
	if s.DerivedFilesPending(path) {
		t.Fatal("deferred projection stayed pending after the no-op republished it")
	}
	if observer.events[0].Rewrite {
		t.Fatal("append checkpoint falsely invalidated history as a rewrite")
	}
}

func TestToolCheckpointRewriteKeepsCanonicalEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	s := NewSession("system")
	s.Add(provider.Message{Role: provider.RoleUser, Content: "old"})
	bindSessionWriter(t, s, path)
	if err := s.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	observer := &checkpointObserver{}
	s.SetPersistObserver(observer)
	s.mu.Lock()
	s.Messages[1].Content = "repaired"
	s.version++
	s.rewriteVersion++
	s.mu.Unlock()
	if err := s.SaveToolCheckpoint(path, true); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(observer.events) != 1 || !observer.events[0].Rewrite {
		t.Fatal("rewrite checkpoint did not invalidate indexed history")
	}
	if loaded.Snapshot()[1].Content != "repaired" {
		t.Fatal("rewrite was not durable")
	}
	if err := s.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
}

func TestToolCheckpointReloadRefreshesProjection(t *testing.T) {
	path := schemaOneSessionPath(t, "session.jsonl")
	s := NewSession("system")
	if err := s.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	s.Add(provider.Message{Role: provider.RoleUser, Content: "durable task"})
	if err := s.SaveToolCheckpoint(path, false); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	index, err := LoadSessionDisplayIndex(store.SessionDisplayIndex(path))
	if err != nil {
		t.Fatal(err)
	}
	if index.MessageCount != len(loaded.Snapshot()) {
		t.Fatal("restart left stale projection")
	}
}
