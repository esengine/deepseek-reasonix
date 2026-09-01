package acp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/permission"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// These tests drive the full real stack — acp.Serve → control.Controller →
// agent.Agent — with a scripted provider and a fake tool standing in for the
// model and a real tool. They are the keyless, deterministic counterpart to a
// live network run: they exercise session/update streaming, the gate→approval
// round-trip, cancellation, and transcript persistence end to end.

// scriptedProvider returns the i-th preset response on the i-th Stream call (the
// agent calls Stream once per step), repeating the last response thereafter.
type scriptedProvider struct {
	name      string
	responses [][]provider.Chunk
	mu        sync.Mutex
	calls     int
}

func (p *scriptedProvider) Name() string { return p.name }

func (p *scriptedProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	// Respect ctx like a real provider: a cancelled turn fails the next step's
	// completion rather than streaming on.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	i := p.calls
	if i >= len(p.responses) {
		i = len(p.responses) - 1
	}
	p.calls++
	resp := p.responses[i]
	p.mu.Unlock()

	ch := make(chan provider.Chunk, len(resp))
	for _, c := range resp {
		ch <- c
	}
	close(ch)
	return ch, nil
}

// fakeTool is a no-op tool whose read-only flag and output the test controls.
type fakeTool struct {
	name string
	ro   bool
	out  string
}

func (t fakeTool) Name() string            { return t.name }
func (t fakeTool) Description() string     { return "fake tool" }
func (t fakeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t fakeTool) ReadOnly() bool          { return t.ro }
func (t fakeTool) Execute(context.Context, json.RawMessage) (string, error) {
	return t.out, nil
}

// e2eFactory builds a real Controller around a real Agent driven by the scripted
// provider, with the fake tool registered and a transcript dir for persistence.
type e2eFactory struct {
	prov       provider.Provider
	tool       tool.Tool
	policy     permission.Policy
	sessionDir string
	options    *agent.Options
}

func (f *e2eFactory) SessionDir() string { return f.sessionDir }

func (f *e2eFactory) NewSession(_ context.Context, p SessionParams) (*control.Controller, error) {
	reg := tool.NewRegistry()
	reg.Add(f.tool)
	opts := agent.Options{MaxSteps: 5}
	if f.options != nil {
		opts = *f.options
	}
	executor := agent.New(f.prov, reg, agent.NewSession("you are a test agent"),
		opts, p.Sink)
	ctrl := control.New(control.Options{
		Runner:       executor,
		Executor:     executor,
		Sink:         p.Sink,
		Policy:       f.policy,
		Label:        "fake-model",
		SessionDir:   f.sessionDir,
		SystemPrompt: "you are a test agent",
	})
	// The composition root wires these through boot.Options; the e2e factory
	// must too, or path transitions (e.g. /clear) fail closed.
	ctrl.SetOnSessionRecovered(p.OnSessionRecovered)
	ctrl.SetOnSessionTransition(p.OnSessionTransition)
	return ctrl, nil
}

func TestE2EExplicitMaxStepsReturnsSuccessfulPause(t *testing.T) {
	responses := [][]provider.Chunk{
		{toolCallChunk("c1", "peek", `{}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c2", "peek", `{}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c3", "peek", `{}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c4", "peek", `{}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c5", "peek", `{}`), {Type: provider.ChunkDone}},
		{{Type: provider.ChunkText, Text: "Saved progress summary."}, {Type: provider.ChunkDone}},
	}
	assertE2ERunPause(t, responses, nil, StopMaxTurnRequests, "paused after 5 tool-call rounds")
}

func TestE2ETaskBudgetReturnsSuccessfulPause(t *testing.T) {
	responses := [][]provider.Chunk{
		{
			toolCallChunk("c1", "peek", `{}`),
			{Type: provider.ChunkUsage, Usage: &provider.Usage{PromptTokens: 100, CacheMissTokens: 100}},
			{Type: provider.ChunkDone},
		},
		{{Type: provider.ChunkText, Text: "Saved budget summary."}, {Type: provider.ChunkDone}},
	}
	options := &agent.Options{
		Pricing:    &provider.Pricing{Input: 1, Currency: "USD"},
		TaskBudget: agent.TaskBudget{Cost: 0.000001},
	}
	assertE2ERunPause(t, responses, options, StopEndTurn, "paused after reaching this task's cost budget")
}

func assertE2ERunPause(t *testing.T, responses [][]provider.Chunk, options *agent.Options, wantStop StopReason, wantWarning string) {
	t.Helper()
	factory := &e2eFactory{
		prov:       &scriptedProvider{name: "fake", responses: responses},
		tool:       fakeTool{name: "peek", ro: true, out: "ok"},
		policy:     permission.New("ask", nil, nil, nil),
		sessionDir: t.TempDir(),
		options:    options,
	}
	client, stop := startServer(t, factory)
	defer stop()

	sid := openSession(t, client)
	promptCh := client.callAsync("session/prompt", SessionPromptParams{
		SessionID: sid,
		Prompt:    []ContentBlock{{Type: "text", Text: "keep working"}},
	})
	notifications, resp := drainPrompt(t, client, promptCh)
	if resp.Error != nil {
		t.Fatalf("controlled pause returned JSON-RPC error: %+v", resp.Error)
	}
	var result SessionPromptResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("prompt result: %v", err)
	}
	if result.StopReason != wantStop {
		t.Errorf("stopReason = %q, want %q", result.StopReason, wantStop)
	}

	var warned, paused bool
	for _, notification := range notifications {
		if notification.Method == sessionStatusUpdateMethod {
			var update ReasonixStatusUpdate
			if err := json.Unmarshal(notification.Params, &update); err != nil {
				t.Fatalf("status update: %v", err)
			}
			if update.Event == "pause" && update.Status.TurnOutcome.Kind == "paused" {
				paused = true
			}
		}
		var params SessionUpdateParams
		if err := json.Unmarshal(notification.Params, &params); err != nil {
			continue
		}
		update, ok := params.Update.(map[string]any)
		if !ok || update["sessionUpdate"] != "agent_message_chunk" {
			continue
		}
		content, _ := update["content"].(map[string]any)
		text, _ := content["text"].(string)
		if strings.Contains(text, "[warning]") && strings.Contains(text, wantWarning) {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("missing controlled-pause warning containing %q", wantWarning)
	}
	if !paused {
		t.Fatal("missing paused status outcome")
	}
}

func toolCallChunk(id, name, args string) provider.Chunk {
	return provider.Chunk{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: id, Name: name, Arguments: args}}
}

// openSession runs initialize + session/new and returns the session id.
func openSession(t *testing.T, c *rpcClient) string {
	t.Helper()
	c.call(t, "initialize", InitializeParams{ProtocolVersion: 1})
	resp := c.call(t, "session/new", SessionNewParams{Cwd: t.TempDir()})
	var nr SessionNewResult
	if err := json.Unmarshal(resp.Result, &nr); err != nil || nr.SessionID == "" {
		t.Fatalf("session/new: %v (%q)", err, nr.SessionID)
	}
	return nr.SessionID
}

// TestE2EToolTurnAndPersistence runs a full turn that streams text, calls a
// read-only tool (auto-allowed, no prompt), streams more text, and ends — then
// checks the session/update stream, the stopReason, and that the turn was
// persisted to the transcript path returned to the client.
func TestE2EToolTurnAndPersistence(t *testing.T) {
	dir := t.TempDir()
	prov := &scriptedProvider{name: "fake", responses: [][]provider.Chunk{
		{
			{Type: provider.ChunkText, Text: "Reading the file."},
			toolCallChunk("c1", "peek", `{"path":"x"}`),
			{Type: provider.ChunkDone},
		},
		{
			{Type: provider.ChunkText, Text: "All done."},
			{Type: provider.ChunkDone},
		},
	}}
	factory := &e2eFactory{
		prov:       prov,
		tool:       fakeTool{name: "peek", ro: true, out: "file contents here"},
		policy:     permission.New("ask", nil, nil, nil),
		sessionDir: dir,
	}
	client, stop := startServer(t, factory)
	defer stop()

	sid := openSession(t, client)
	promptCh := client.callAsync("session/prompt", SessionPromptParams{
		SessionID: sid,
		Prompt:    []ContentBlock{{Type: "text", Text: "look at x"}},
	})
	notifs, resp := drainPrompt(t, client, promptCh)

	// The update stream carries both message chunks, the tool call, and its result.
	kinds := map[string]int{}
	var toolResultText string
	for _, n := range notifs {
		k := updateKind(t, n)
		kinds[k]++
		if k == "tool_call_update" {
			var p struct {
				Update struct {
					Status  string `json:"status"`
					Content []struct {
						Content struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"content"`
				} `json:"update"`
			}
			json.Unmarshal(n.Params, &p)
			if p.Update.Status != "completed" {
				t.Errorf("tool_call_update status = %q, want completed", p.Update.Status)
			}
			if len(p.Update.Content) > 0 {
				toolResultText = p.Update.Content[0].Content.Text
			}
		}
	}
	if kinds["agent_message_chunk"] < 2 {
		t.Errorf("want >=2 message chunks, got %d (all: %v)", kinds["agent_message_chunk"], kinds)
	}
	if kinds["tool_call"] != 1 || kinds["tool_call_update"] != 1 {
		t.Errorf("want 1 tool_call + 1 tool_call_update, got %v", kinds)
	}
	if toolResultText != "file contents here" {
		t.Errorf("tool result text = %q", toolResultText)
	}

	var pr SessionPromptResult
	if err := json.Unmarshal(resp.Result, &pr); err != nil {
		t.Fatalf("prompt result: %v", err)
	}
	if pr.StopReason != StopEndTurn {
		t.Errorf("stopReason = %q, want end_turn", pr.StopReason)
	}

	// Persistence: a transcript path was returned and the turn is on disk.
	if pr.TranscriptPath == nil {
		t.Fatal("no transcriptPath returned")
	}
	if !strings.HasPrefix(*pr.TranscriptPath, dir) {
		t.Errorf("transcriptPath %q not under session dir %q", *pr.TranscriptPath, dir)
	}
	data, err := os.ReadFile(*pr.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	body := string(data)
	for _, want := range []string{"look at x", "All done.", "peek"} {
		if !strings.Contains(body, want) {
			t.Errorf("transcript missing %q; got:\n%s", want, body)
		}
	}
}

// TestE2ESessionLoad runs a turn in one server (saving a transcript keyed by
// session id), then resumes it in a fresh server pointed at the same session dir
// — simulating a restart — and checks the conversation is replayed to the client
// as session/update notifications.
func TestE2ESessionLoad(t *testing.T) {
	dir := t.TempDir()
	mkFactory := func() *e2eFactory {
		return &e2eFactory{
			prov: &scriptedProvider{name: "fake", responses: [][]provider.Chunk{
				{
					{Type: provider.ChunkText, Text: "Reading the file."},
					toolCallChunk("c1", "peek", `{"path":"x"}`),
					{Type: provider.ChunkDone},
				},
				{{Type: provider.ChunkText, Text: "All done."}, {Type: provider.ChunkDone}},
			}},
			tool:       fakeTool{name: "peek", ro: true, out: "file contents here"},
			policy:     permission.New("ask", nil, nil, nil),
			sessionDir: dir,
		}
	}

	// Run 1: create a session and run a turn, persisting the transcript.
	client1, stop1 := startServer(t, mkFactory())
	sid := openSession(t, client1)
	promptCh := client1.callAsync("session/prompt", SessionPromptParams{
		SessionID: sid,
		Prompt:    []ContentBlock{{Type: "text", Text: "look at x"}},
	})
	drainPrompt(t, client1, promptCh)
	stop1()

	// Run 2: a brand-new server (same session dir) resumes by id.
	client2, stop2 := startServer(t, mkFactory())
	defer stop2()
	loadCh := client2.callAsync("session/load", SessionLoadParams{SessionID: sid})
	notifs, resp := drainPrompt(t, client2, loadCh)

	if resp.Error != nil {
		t.Fatalf("session/load errored: %+v", resp.Error)
	}

	// The replay reconstructs the conversation: the user turn, the tool call and
	// its result, and the assistant's answers.
	kinds := map[string]int{}
	texts := map[string]string{}
	for _, n := range notifs {
		k := updateKind(t, n)
		kinds[k]++
		var p struct {
			Update struct {
				Content struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		}
		json.Unmarshal(n.Params, &p)
		if p.Update.Content.Text != "" {
			texts[k] += p.Update.Content.Text
		}
	}
	if kinds["user_message_chunk"] != 1 || !strings.Contains(texts["user_message_chunk"], "look at x") {
		t.Errorf("user replay = %dx %q, want the original prompt", kinds["user_message_chunk"], texts["user_message_chunk"])
	}
	if !strings.Contains(texts["agent_message_chunk"], "All done.") {
		t.Errorf("assistant replay = %q, want it to include the answer", texts["agent_message_chunk"])
	}
	if kinds["tool_call"] != 1 || kinds["tool_call_update"] != 1 {
		t.Errorf("tool replay = %v, want 1 tool_call + 1 tool_call_update", kinds)
	}
}

func TestE2ESessionListResumeAndDelete(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	mkFactory := func() *e2eFactory {
		return &e2eFactory{
			prov: &scriptedProvider{name: "fake", responses: [][]provider.Chunk{
				{{Type: provider.ChunkText, Text: "Stored answer."}, {Type: provider.ChunkDone}},
			}},
			tool:       fakeTool{name: "peek", ro: true, out: "unused"},
			policy:     permission.New("ask", nil, nil, nil),
			sessionDir: dir,
		}
	}

	client1, stop1 := startServer(t, mkFactory())
	client1.call(t, "initialize", InitializeParams{ProtocolVersion: 1})
	newResp := client1.call(t, "session/new", SessionNewParams{Cwd: cwd})
	var nr SessionNewResult
	if err := json.Unmarshal(newResp.Result, &nr); err != nil || nr.SessionID == "" {
		t.Fatalf("session/new: %v (%q)", err, nr.SessionID)
	}
	promptCh := client1.callAsync("session/prompt", SessionPromptParams{
		SessionID: nr.SessionID,
		Prompt:    []ContentBlock{{Type: "text", Text: "remember this session"}},
	})
	_, promptResp := drainPrompt(t, client1, promptCh)
	var pr SessionPromptResult
	if err := json.Unmarshal(promptResp.Result, &pr); err != nil {
		t.Fatalf("prompt result: %v", err)
	}
	if pr.TranscriptPath == nil {
		t.Fatal("prompt did not return a transcript path")
	}
	transcript := *pr.TranscriptPath
	stop1()

	client2, stop2 := startServer(t, mkFactory())
	defer stop2()
	client2.call(t, "initialize", InitializeParams{ProtocolVersion: 1})
	listResp := client2.call(t, "session/list", SessionListParams{Cwd: cwd})
	var lr SessionListResult
	if err := json.Unmarshal(listResp.Result, &lr); err != nil {
		t.Fatalf("session/list result: %v", err)
	}
	if len(lr.Sessions) != 1 {
		t.Fatalf("session/list returned %d sessions, want 1: %+v", len(lr.Sessions), lr.Sessions)
	}
	got := lr.Sessions[0]
	if got.SessionID != nr.SessionID || got.Cwd != cwd {
		t.Fatalf("listed session = %+v, want id %q cwd %q", got, nr.SessionID, cwd)
	}
	if !strings.Contains(got.Title, "remember this session") {
		t.Fatalf("listed title = %q, want prompt preview", got.Title)
	}
	if got.UpdatedAt == "" {
		t.Fatal("listed session missing updatedAt")
	}

	resumeResp := client2.call(t, "session/resume", SessionResumeParams{SessionID: nr.SessionID, Cwd: cwd})
	if resumeResp.Error != nil {
		t.Fatalf("session/resume errored: %+v", resumeResp.Error)
	}
	select {
	case n := <-client2.notifs:
		t.Fatalf("session/resume replayed an unexpected notification: %+v", n)
	default:
	}

	deleteResp := client2.call(t, "session/delete", SessionDeleteParams{SessionID: nr.SessionID})
	if deleteResp.Error != nil {
		t.Fatalf("session/delete errored: %+v", deleteResp.Error)
	}
	listResp = client2.call(t, "session/list", SessionListParams{Cwd: cwd})
	if err := json.Unmarshal(listResp.Result, &lr); err != nil {
		t.Fatalf("session/list after delete: %v", err)
	}
	if len(lr.Sessions) != 0 {
		t.Fatalf("session/list after delete = %+v, want empty", lr.Sessions)
	}
	if _, err := os.Stat(transcript); !os.IsNotExist(err) {
		t.Fatalf("transcript after delete stat err = %v, want not exist", err)
	}
}

func TestE2ESessionListSkipsUnpromptedSessionAfterRestart(t *testing.T) {
	dir := t.TempDir()
	factory := &e2eFactory{
		prov: &scriptedProvider{name: "fake", responses: [][]provider.Chunk{
			{{Type: provider.ChunkText, Text: "unused"}, {Type: provider.ChunkDone}},
		}},
		tool:       fakeTool{name: "peek", ro: true, out: "unused"},
		policy:     permission.New("ask", nil, nil, nil),
		sessionDir: dir,
	}

	client1, stop1 := startServer(t, factory)
	client1.call(t, "initialize", InitializeParams{ProtocolVersion: 1})
	resp := client1.call(t, "session/new", SessionNewParams{Cwd: t.TempDir()})
	var nr SessionNewResult
	if err := json.Unmarshal(resp.Result, &nr); err != nil || nr.SessionID == "" {
		t.Fatalf("session/new: %v (%q)", err, nr.SessionID)
	}
	stop1()

	client2, stop2 := startServer(t, factory)
	defer stop2()
	client2.call(t, "initialize", InitializeParams{ProtocolVersion: 1})
	listResp := client2.call(t, "session/list", SessionListParams{})
	var lr SessionListResult
	if err := json.Unmarshal(listResp.Result, &lr); err != nil {
		t.Fatalf("session/list result: %v", err)
	}
	if len(lr.Sessions) != 0 {
		t.Fatalf("session/list returned unprompted session: %+v", lr.Sessions)
	}
}

func TestE2EDeleteActiveSessionDoesNotRecreateFiles(t *testing.T) {
	dir := t.TempDir()
	releaseTool := make(chan struct{})
	started := make(chan struct{})
	prov := &scriptedProvider{name: "fake", responses: [][]provider.Chunk{
		{
			{Type: provider.ChunkText, Text: "Starting."},
			toolCallChunk("c1", "slow", `{}`),
			{Type: provider.ChunkDone},
		},
		{{Type: provider.ChunkText, Text: "unreachable"}, {Type: provider.ChunkDone}},
	}}
	factory := &e2eFactory{
		prov:       prov,
		tool:       blockingTool{started: started, release: releaseTool},
		policy:     permission.New("ask", nil, nil, nil),
		sessionDir: dir,
	}
	client, stop := startServer(t, factory)
	defer stop()

	sid := openSession(t, client)
	promptCh := client.callAsync("session/prompt", SessionPromptParams{
		SessionID: sid,
		Prompt:    []ContentBlock{{Type: "text", Text: "delete me while running"}},
	})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool never started")
	}
	deleteResp := client.call(t, "session/delete", SessionDeleteParams{SessionID: sid})
	if deleteResp.Error != nil {
		t.Fatalf("session/delete errored: %+v", deleteResp.Error)
	}

	select {
	case resp := <-promptCh:
		if resp.Error != nil {
			t.Fatalf("prompt errored after delete: %+v", resp.Error)
		}
		var pr SessionPromptResult
		if err := json.Unmarshal(resp.Result, &pr); err != nil {
			t.Fatalf("prompt result: %v", err)
		}
		if pr.StopReason != StopCancelled {
			t.Fatalf("stopReason = %q, want cancelled", pr.StopReason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prompt did not finish after delete")
	}

	listResp := client.call(t, "session/list", SessionListParams{})
	var lr SessionListResult
	if err := json.Unmarshal(listResp.Result, &lr); err != nil {
		t.Fatalf("session/list result: %v", err)
	}
	if len(lr.Sessions) != 0 {
		t.Fatalf("session/list after active delete = %+v, want empty", lr.Sessions)
	}
	for _, path := range []string{transcriptPath(dir, sid), acpMetaPath(transcriptPath(dir, sid))} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s after active delete stat err = %v, want not exist", path, err)
		}
	}
	close(releaseTool)
}

// TestE2EApprovalRoundTrip drives a write tool through the gate: the policy asks,
// the controller raises an ApprovalRequest, the sink forwards it as
// session/request_permission, the client allows it, and the tool then runs.
func TestE2EApprovalRoundTrip(t *testing.T) {
	prov := &scriptedProvider{name: "fake", responses: [][]provider.Chunk{
		{
			{Type: provider.ChunkText, Text: "Writing."},
			toolCallChunk("w1", "writeit", `{"path":"README.md"}`),
			{Type: provider.ChunkDone},
		},
		{
			{Type: provider.ChunkText, Text: "Wrote it."},
			{Type: provider.ChunkDone},
		},
	}}
	factory := &e2eFactory{
		prov:       prov,
		tool:       fakeTool{name: "writeit", ro: false, out: "written ok"},
		policy:     permission.New("ask", nil, nil, nil),
		sessionDir: t.TempDir(),
	}
	client, stop := startServer(t, factory)
	defer stop()

	sid := openSession(t, client)
	promptCh := client.callAsync("session/prompt", SessionPromptParams{
		SessionID: sid,
		Prompt:    []ContentBlock{{Type: "text", Text: "write README.md"}},
	})

	// Answer the permission request while the prompt is still in flight. The
	// turn cannot finish until this round-trip completes.
	var req frame
	select {
	case req = <-client.reqs:
	case <-time.After(2 * time.Second):
		t.Fatal("no permission request was raised")
	}
	var pr PermissionRequestParams
	if err := json.Unmarshal(req.Params, &pr); err != nil {
		t.Fatalf("permission params: %v", err)
	}
	if pr.SessionID != sid {
		t.Errorf("permission sessionId = %q, want %q", pr.SessionID, sid)
	}
	if pr.ToolCall.Kind != "edit" {
		t.Errorf("permission kind = %q, want edit", pr.ToolCall.Kind)
	}
	if !strings.Contains(pr.ToolCall.Title, "writeit") {
		t.Errorf("permission title = %q, want it to mention writeit", pr.ToolCall.Title)
	}
	assertACPv1PermissionOptionKinds(t, pr.Options)
	if _, ok := invalidACPv1PermissionOptionKind(pr.Options); ok {
		client.replyError(req.ID, ErrInvalidParams, "Invalid params")
	} else {
		client.reply(req.ID, PermissionRequestResult{
			Outcome: PermissionOutcome{Outcome: "selected", OptionID: string(OptAllowOnce)},
		})
	}

	notifs, resp := drainPrompt(t, client, promptCh)

	// The allowed tool ran: a completed tool_call_update with its output.
	var ran bool
	for _, n := range notifs {
		if updateKind(t, n) != "tool_call_update" {
			continue
		}
		var p struct {
			Update struct {
				Status  string `json:"status"`
				Content []struct {
					Content struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"content"`
			} `json:"update"`
		}
		json.Unmarshal(n.Params, &p)
		if p.Update.Status == "completed" && len(p.Update.Content) > 0 &&
			strings.HasPrefix(p.Update.Content[0].Content.Text, "written ok") {
			ran = true
		}
	}
	if !ran {
		t.Error("approved tool did not run to completion")
	}

	var result SessionPromptResult
	json.Unmarshal(resp.Result, &result)
	// Adaptive standard execution may pause the turn for missing readiness
	// after a write; controlled readiness pauses use ACP v1 end_turn.
	if result.StopReason != StopEndTurn {
		t.Errorf("stopReason = %q, want end_turn", result.StopReason)
	}
}

// TestE2ECancelMidTurn cancels while the tool is executing and checks the turn
// ends with stopReason cancelled.
func TestE2ECancelMidTurn(t *testing.T) {
	releaseTool := make(chan struct{})
	started := make(chan struct{})
	prov := &scriptedProvider{name: "fake", responses: [][]provider.Chunk{
		{
			{Type: provider.ChunkText, Text: "Starting."},
			toolCallChunk("c1", "slow", `{}`),
			{Type: provider.ChunkDone},
		},
		{{Type: provider.ChunkText, Text: "unreachable"}, {Type: provider.ChunkDone}},
	}}
	factory := &e2eFactory{
		prov:       prov,
		tool:       blockingTool{started: started, release: releaseTool},
		policy:     permission.New("ask", nil, nil, nil),
		sessionDir: t.TempDir(),
	}
	client, stop := startServer(t, factory)
	defer stop()

	sid := openSession(t, client)
	promptCh := client.callAsync("session/prompt", SessionPromptParams{
		SessionID: sid,
		Prompt:    []ContentBlock{{Type: "text", Text: "go"}},
	})

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool never started")
	}
	client.notify("session/cancel", SessionCancelParams{SessionID: sid})

	select {
	case resp := <-promptCh:
		var pr SessionPromptResult
		json.Unmarshal(resp.Result, &pr)
		if pr.StopReason != StopCancelled {
			t.Errorf("stopReason = %q, want cancelled", pr.StopReason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not end the turn")
	}
	close(releaseTool) // let the tool goroutine unwind
}

// blockingTool blocks in Execute until released or ctx is cancelled, signalling
// when it has started so the test can cancel mid-execution.
type blockingTool struct {
	started chan struct{}
	release chan struct{}
}

func (t blockingTool) Name() string            { return "slow" }
func (t blockingTool) Description() string     { return "blocks until cancelled" }
func (t blockingTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t blockingTool) ReadOnly() bool          { return true }
func (t blockingTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	close(t.started)
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-t.release:
		return "released", nil
	}
}
