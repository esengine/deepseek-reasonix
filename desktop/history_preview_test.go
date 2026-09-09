package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
)

func TestPreviewSessionMessagesLoadsWithoutResuming(t *testing.T) {
	dir := t.TempDir()
	session := agent.NewSession("")
	session.Add(provider.Message{Role: provider.RoleUser, Content: "show history"})
	session.Add(provider.Message{Role: provider.RoleAssistant, Content: "answer", ReasoningContent: "saved reasoning"})
	path := filepath.Join(dir, "session.jsonl")
	if err := session.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := previewSessionMessages(dir, path)
	if err != nil {
		t.Fatalf("previewSessionMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("preview history length = %d, want 2", len(got))
	}
	if got[1].Reasoning != "saved reasoning" {
		t.Fatalf("preview reasoning = %q, want saved reasoning", got[1].Reasoning)
	}
}

func TestPreviewSessionMessagesUpgradesLegacyExpandedPaste(t *testing.T) {
	const label = "[Pasted text #1 · 2 lines]"
	const display = "inspect this\n\n" + label
	const expanded = display + "\n\n--- Begin " + label + " ---\none\ntwo\n--- End " + label + " ---"
	const rendered = "<capability-route version=\"1\">\nuse review\n</capability-route>\n\n" + expanded

	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.jsonl")
	session := agent.NewSession("")
	session.Add(provider.Message{Role: provider.RoleUser, Content: rendered, RawContent: expanded})
	if err := session.Save(path); err != nil {
		t.Fatalf("Save legacy session: %v", err)
	}
	if err := recordSessionDisplay(dir, path, rendered, display); err != nil {
		t.Fatalf("record legacy display: %v", err)
	}

	got, err := previewSessionMessages(dir, path)
	if err != nil {
		t.Fatalf("previewSessionMessages: %v", err)
	}
	if len(got) != 1 || got[0].Content != display || got[0].SubmitText != expanded {
		t.Fatalf("upgraded legacy preview = %+v, want display %q and expanded replay", got, display)
	}
	if strings.Contains(got[0].SubmitText, "capability-route") {
		t.Fatalf("provider-only wrapper leaked after session restart: %+v", got[0])
	}
}

func TestPreviewSessionMessagesUpgradesContentOnlyExpandedPaste(t *testing.T) {
	const label = "[Pasted text #1 · 2 lines]"
	const display = "inspect this\n\n" + label
	const expanded = display + "\n\n--- Begin " + label + " ---\none\ntwo\n--- End " + label + " ---"

	dir := t.TempDir()
	path := filepath.Join(dir, "content-only.jsonl")
	session := agent.NewSession("")
	// Releases before Context Engine v2 persisted user turns without RawContent.
	session.Add(provider.Message{Role: provider.RoleUser, Content: expanded})
	if err := session.Save(path); err != nil {
		t.Fatalf("Save content-only session: %v", err)
	}

	got, err := previewSessionMessages(dir, path)
	if err != nil {
		t.Fatalf("previewSessionMessages: %v", err)
	}
	if len(got) != 1 || got[0].Content != display || got[0].SubmitText != expanded {
		t.Fatalf("upgraded content-only preview = %+v, want display %q and expanded replay", got, display)
	}
}

func TestPreviewSessionMessagesIncludesProcessEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	body := strings.Join([]string{
		`{"kind":"phase","text":"Preparing context"}`,
		`{"kind":"notice","level":"warn","text":"Network changed"}`,
		`{"kind":"compaction_started","compaction":{"trigger":"manual"}}`,
		`{"kind":"compaction_done","compaction":{"trigger":"manual","messages":6,"summary":"Kept the current task.","archive":"/tmp/archive.jsonl"}}`,
		`{"type":"user.message","text":"hello","ts":1718000000000}`,
		`{"type":"model.final","content":"hi","reasoningContent":"thinking"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := previewSessionMessages(dir, path)
	if err != nil {
		t.Fatalf("previewSessionMessages: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("preview history length = %d, want 6: %+v", len(got), got)
	}
	if got[0].Role != "phase" || got[0].Content != "Preparing context" {
		t.Fatalf("phase event not preserved: %+v", got[0])
	}
	if got[1].Role != "notice" || got[1].Level != "warn" || got[1].Content != "Network changed" {
		t.Fatalf("notice event not preserved: %+v", got[1])
	}
	if got[2].Role != "compaction" || !got[2].Pending || got[2].Trigger != "manual" {
		t.Fatalf("pending compaction event not preserved: %+v", got[2])
	}
	if got[3].Role != "compaction" || got[3].Pending || got[3].Messages != 6 || got[3].Summary != "Kept the current task." || got[3].Archive != "/tmp/archive.jsonl" {
		t.Fatalf("finished compaction event not preserved: %+v", got[3])
	}
	if got[4].Role != "user" || got[5].Reasoning != "thinking" {
		t.Fatalf("conversation events not preserved: %+v", got[4:])
	}
	if got[4].CreatedAt != 1_718_000_000_000 {
		t.Fatalf("event user createdAt = %d, want 1718000000000", got[4].CreatedAt)
	}
}

func TestPreviewSessionMessagesRestoresAppendEventUserTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	session := agent.NewSession("")
	session.Add(provider.Message{Role: provider.RoleUser, Content: "first"})
	if err := session.SaveSnapshot(path); err != nil {
		t.Fatalf("SaveSnapshot first: %v", err)
	}
	session.Add(provider.Message{Role: provider.RoleAssistant, Content: "one"})
	session.Add(provider.Message{Role: provider.RoleUser, Content: "second"})
	if err := session.SaveSnapshot(path); err != nil {
		t.Fatalf("SaveSnapshot second: %v", err)
	}

	got, err := previewSessionMessages(dir, path)
	if err != nil {
		t.Fatalf("previewSessionMessages: %v", err)
	}
	if len(got) != 3 || got[2].Role != "user" || got[2].Content != "second" {
		t.Fatalf("preview history = %+v, want second user at index 2", got)
	}
	if got[2].CreatedAt <= 0 {
		t.Fatalf("append-event user timestamp was not restored: %+v", got[2])
	}
}
