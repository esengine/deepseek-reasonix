package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/transcript"
)

func remoteTranscriptFixture(server *httptest.Server) (*App, *remoteTab) {
	tab := &remoteTab{id: "remote", state: "ready", client: server.Client(), base: server.URL, gen: 1,
		routing: remoteTabSessionRouting{currentPath: "/session.jsonl"}}
	return &App{remoteTabs: map[string]*remoteTab{tab.id: tab}}, tab
}

func TestRemoteTranscriptNegotiatesOldServeWithoutMutation(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/transcript/snapshot" || r.URL.Query().Get("session") != "/session.jsonl" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte("<html>old Serve index</html>"))
			}))
			defer server.Close()
			app, tab := remoteTranscriptFixture(server)
			result, err := app.RemoteTranscriptSnapshotForTab(tab.id, transcript.PageRequest{})
			if err != nil || result.Supported || result.Snapshot != nil {
				t.Fatalf("negotiation = %+v, %v", result, err)
			}
			if tab.state != "ready" || tab.gen != 1 {
				t.Fatal("capability probe changed the connection")
			}
		})
	}
}

func TestRemoteTranscriptRejectsLateSessionResponse(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		_ = json.NewEncoder(w).Encode(transcript.Snapshot{Boundary: transcript.Boundary{ProtocolVersion: 1, SnapshotID: "old"}})
	}))
	defer server.Close()
	app, tab := remoteTranscriptFixture(server)
	done := make(chan error, 1)
	go func() { _, err := app.RemoteTranscriptSnapshotForTab(tab.id, transcript.PageRequest{}); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	app.remoteTabMu.Lock()
	tab.routing.currentPath = "/new-session.jsonl"
	app.remoteTabMu.Unlock()
	close(release)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "replaced session") {
		t.Fatalf("late response accepted: %v", err)
	}
}

func TestRemoteTabMetadataDoesNotRequestHistory(t *testing.T) {
	var historyReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/history" {
			historyReads.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/status" {
			_, _ = w.Write([]byte(`{"sessionPath":"/session.jsonl","running":false}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	app, tab := remoteTranscriptFixture(server)
	metadata, err := app.RemoteTabMetadata(tab.id)
	if err != nil || historyReads.Load() != 0 || len(metadata.History) != 0 {
		t.Fatalf("metadata requested history: count=%d error=%v", historyReads.Load(), err)
	}
}
