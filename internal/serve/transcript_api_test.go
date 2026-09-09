package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/provider"
	"reasonix/internal/transcript"
)

func TestTranscriptHTTPBindsSessionAndImmutableContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	session := agent.NewSession("system")
	session.Add(provider.Message{Role: provider.RoleUser, Content: "question"})
	session.Add(provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("body", 20000)})
	if err := session.Save(path); err != nil {
		t.Fatal(err)
	}
	bc := NewBroadcaster()
	executor := agent.New(nil, nil, session, agent.Options{}, bc)
	ctrl := control.New(control.Options{Executor: executor, SessionDir: dir, SessionPath: path, Sink: bc})
	defer ctrl.Close()
	server := httptest.NewServer(New(ctrl, bc, config.ServeConfig{}).Handler())
	defer server.Close()
	response, err := http.Get(server.URL + "/transcript/snapshot?session=" + url.QueryEscape(path))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("snapshot status=%d cache=%q", response.StatusCode, response.Header.Get("Cache-Control"))
	}
	var snapshot transcript.Snapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ProtocolVersion != 1 || len(snapshot.Records) != 2 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	ref := snapshot.Records[1].Refs[0]
	encoded, _ := json.Marshal(transcript.ContentRequest{ContentRef: ref})
	contentResponse, err := http.Get(server.URL + "/transcript/content?session=" + url.QueryEscape(path) + "&request=" + url.QueryEscape(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	defer contentResponse.Body.Close()
	var content transcript.ContentChunk
	if err := json.NewDecoder(contentResponse.Body).Decode(&content); err != nil || len(content.Data) != 64<<10 {
		t.Fatalf("content bytes=%d err=%v", len(content.Data), err)
	}
	wrong, err := http.Get(server.URL + "/transcript/snapshot?session=" + url.QueryEscape(filepath.Join(dir, "different.jsonl")))
	if err != nil {
		t.Fatal(err)
	}
	wrong.Body.Close()
	if wrong.StatusCode != http.StatusConflict {
		t.Fatalf("wrong-session status=%d", wrong.StatusCode)
	}
	malformed, err := http.Get(server.URL + "/transcript/page?request=%7B")
	if err != nil {
		t.Fatal(err)
	}
	malformed.Body.Close()
	if malformed.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed status=%d", malformed.StatusCode)
	}
}
