package acp

import (
	"encoding/json"
	"testing"
	"time"

	"reasonix/internal/control"
	"reasonix/internal/permission"
	"reasonix/internal/provider"
)

// clearTurnPath runs one prompt and returns its transcript path; a helper for
// the /clear e2e tests.
func clearTurnPath(t *testing.T, client *rpcClient, sid, text string) string {
	t.Helper()
	promptCh := client.callAsync("session/prompt", SessionPromptParams{
		SessionID: sid,
		Prompt:    []ContentBlock{{Type: "text", Text: text}},
	})
	_, resp := drainPrompt(t, client, promptCh)
	if resp.Error != nil {
		t.Fatalf("prompt %q: %+v", text, resp.Error)
	}
	var result SessionPromptResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("prompt %q result: %v", text, err)
	}
	if result.StopReason != StopEndTurn {
		t.Fatalf("prompt %q stopReason = %q, want end_turn", text, result.StopReason)
	}
	if result.TranscriptPath == nil {
		t.Fatalf("prompt %q returned no transcriptPath", text)
	}
	return *result.TranscriptPath
}

func clearTestFactory(t *testing.T) *e2eFactory {
	return &e2eFactory{
		prov: &scriptedProvider{name: "fake", responses: [][]provider.Chunk{
			{{Type: provider.ChunkText, Text: "hello"}, {Type: provider.ChunkDone}},
		}},
		tool:       fakeTool{name: "peek", ro: true, out: "ok"},
		policy:     permission.New("ask", nil, nil, nil),
		sessionDir: t.TempDir(),
	}
}

// TestE2EClearAskModeAsksAndClearsOnConfirm pins the non-Yolo /clear contract:
// the server round-trips a session/request_permission before clearing, and the
// cleared session rotates to a fresh transcript path under the same session id.
func TestE2EClearAskModeAsksAndClearsOnConfirm(t *testing.T) {
	client, stop := startServer(t, clearTestFactory(t))
	defer stop()

	sid := openSession(t, client)
	before := clearTurnPath(t, client, sid, "plain turn")

	promptCh := client.callAsync("session/prompt", SessionPromptParams{
		SessionID: sid,
		Prompt:    []ContentBlock{{Type: "text", Text: "/clear"}},
	})
	var req frame
	select {
	case req = <-client.reqs:
	case <-time.After(2 * time.Second):
		t.Fatal("no clear confirmation was requested")
	}
	var pr PermissionRequestParams
	if err := json.Unmarshal(req.Params, &pr); err != nil {
		t.Fatalf("clear confirmation params: %v", err)
	}
	if pr.SessionID != sid {
		t.Errorf("clear confirmation sessionId = %q, want %q", pr.SessionID, sid)
	}
	ids := map[string]bool{}
	for _, o := range pr.Options {
		ids[o.OptionID] = true
	}
	if !ids["reasonix_clear_confirm"] || !ids["reasonix_clear_cancel"] {
		t.Errorf("clear confirmation options = %v, want confirm+cancel", pr.Options)
	}
	client.reply(req.ID, PermissionRequestResult{
		Outcome: PermissionOutcome{Outcome: "selected", OptionID: "reasonix_clear_confirm"},
	})

	_, resp := drainPrompt(t, client, promptCh)
	if resp.Error != nil {
		t.Fatalf("/clear: %+v", resp.Error)
	}
	var result SessionPromptResult
	json.Unmarshal(resp.Result, &result)
	if result.StopReason != StopEndTurn {
		t.Errorf("/clear stopReason = %q, want end_turn", result.StopReason)
	}
	if result.TranscriptPath == nil || *result.TranscriptPath == before {
		t.Errorf("/clear transcriptPath = %v, want a fresh path != %q", result.TranscriptPath, before)
	}
	// The cleared session keeps its id and persists to the new path.
	if after := clearTurnPath(t, client, sid, "after clear"); after != *result.TranscriptPath {
		t.Errorf("post-clear transcriptPath = %q, want %q", after, *result.TranscriptPath)
	}
}

// TestE2EClearAskModeCancelAborts pins that declining the confirmation leaves
// the session and its transcript untouched.
func TestE2EClearAskModeCancelAborts(t *testing.T) {
	client, stop := startServer(t, clearTestFactory(t))
	defer stop()

	sid := openSession(t, client)
	before := clearTurnPath(t, client, sid, "plain turn")

	promptCh := client.callAsync("session/prompt", SessionPromptParams{
		SessionID: sid,
		Prompt:    []ContentBlock{{Type: "text", Text: "/clear"}},
	})
	select {
	case req := <-client.reqs:
		client.reply(req.ID, PermissionRequestResult{
			Outcome: PermissionOutcome{Outcome: "selected", OptionID: "reasonix_clear_cancel"},
		})
	case <-time.After(2 * time.Second):
		t.Fatal("no clear confirmation was requested")
	}
	_, resp := drainPrompt(t, client, promptCh)
	if resp.Error != nil {
		t.Fatalf("/clear: %+v", resp.Error)
	}
	var result SessionPromptResult
	json.Unmarshal(resp.Result, &result)
	if result.StopReason != StopEndTurn {
		t.Errorf("/clear stopReason = %q, want end_turn", result.StopReason)
	}
	if result.TranscriptPath == nil || *result.TranscriptPath != before {
		t.Errorf("cancelled /clear transcriptPath = %v, want unchanged %q", result.TranscriptPath, before)
	}
}

// TestE2EClearYoloModeSkipsConfirmation pins the Yolo fast path: /clear clears
// immediately, without a session/request_permission round-trip.
func TestE2EClearYoloModeSkipsConfirmation(t *testing.T) {
	client, stop := startServer(t, clearTestFactory(t))
	defer stop()

	sid := openSession(t, client)
	setResp := client.call(t, "session/set_config_option", SetSessionConfigOptionParams{
		SessionID: sid,
		ConfigID:  "tool_approval",
		Value:     control.ToolApprovalYolo,
	})
	if setResp.Error != nil {
		t.Fatalf("set tool_approval=yolo: %+v", setResp.Error)
	}
	before := clearTurnPath(t, client, sid, "plain turn")

	promptCh := client.callAsync("session/prompt", SessionPromptParams{
		SessionID: sid,
		Prompt:    []ContentBlock{{Type: "text", Text: "/clear"}},
	})
	_, resp := drainPrompt(t, client, promptCh)
	if resp.Error != nil {
		t.Fatalf("/clear: %+v", resp.Error)
	}
	var result SessionPromptResult
	json.Unmarshal(resp.Result, &result)
	if result.StopReason != StopEndTurn {
		t.Errorf("/clear stopReason = %q, want end_turn", result.StopReason)
	}
	if result.TranscriptPath == nil || *result.TranscriptPath == before {
		t.Errorf("/clear transcriptPath = %v, want a fresh path != %q", result.TranscriptPath, before)
	}
	select {
	case req := <-client.reqs:
		t.Fatalf("Yolo /clear must not ask the client, got request %q", req.Method)
	default:
	}
}
