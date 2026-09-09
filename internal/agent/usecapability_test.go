package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"reasonix/internal/capability"
	"reasonix/internal/config"
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/mcplaunch"
	"reasonix/internal/permission"
	"reasonix/internal/plugin"
	"reasonix/internal/provider"
	"reasonix/internal/skill"

	"reasonix/internal/tool"
)

type denyAllGate struct{}

func (denyAllGate) Check(_ context.Context, name string, _ json.RawMessage, _ bool) (bool, string, error) {
	return false, "denied " + name, nil
}

type completedProxyCallTool struct{}

func (completedProxyCallTool) Name() string            { return "use_capability" }
func (completedProxyCallTool) Description() string     { return "" }
func (completedProxyCallTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (completedProxyCallTool) ReadOnly() bool          { return true }
func (completedProxyCallTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

type readOnlyBoundaryTarget struct {
	name      string
	readOnly  bool
	hostStart bool
	calls     *int
}

func (t readOnlyBoundaryTarget) Name() string                        { return t.name }
func (readOnlyBoundaryTarget) Description() string                   { return "" }
func (readOnlyBoundaryTarget) Schema() json.RawMessage               { return json.RawMessage(`{"type":"object"}`) }
func (t readOnlyBoundaryTarget) ReadOnly() bool                      { return t.readOnly }
func (t readOnlyBoundaryTarget) ReadOnlyExecutionHostMutation() bool { return t.hostStart }
func (t readOnlyBoundaryTarget) Execute(context.Context, json.RawMessage) (string, error) {
	if t.calls != nil {
		(*t.calls)++
	}
	return "target executed", nil
}

type readOnlyBoundaryProxy struct {
	resolved tool.ResolvedCall
}

func (readOnlyBoundaryProxy) Name() string            { return "use_capability" }
func (readOnlyBoundaryProxy) Description() string     { return "" }
func (readOnlyBoundaryProxy) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (readOnlyBoundaryProxy) ReadOnly() bool          { return true }
func (readOnlyBoundaryProxy) Execute(context.Context, json.RawMessage) (string, error) {
	return "proxy executed", nil
}
func (p readOnlyBoundaryProxy) ResolveCall(context.Context, json.RawMessage) (tool.ResolvedCall, error) {
	return p.resolved, nil
}

type layeredReadOnlyMCPBoundaryTarget struct {
	readOnlyBoundaryTarget
	destructive      bool
	serverAuthorized bool
}

func (layeredReadOnlyMCPBoundaryTarget) MCPServerName() string      { return "test" }
func (layeredReadOnlyMCPBoundaryTarget) MCPRawToolName() string     { return "read" }
func (t layeredReadOnlyMCPBoundaryTarget) MCPDestructiveHint() bool { return t.destructive }

func (t layeredReadOnlyMCPBoundaryTarget) MCPServerAuthorized() bool { return t.serverAuthorized }

func executeReadOnlyBoundaryCall(t *testing.T, resolved tool.ResolvedCall) toolOutcome {
	t.Helper()
	reg := tool.NewRegistry()
	reg.Add(readOnlyBoundaryProxy{resolved: resolved})
	a := New(nil, reg, NewSession("sys"), Options{ReadOnlyExecution: true}, event.Discard)
	return a.executeOne(context.Background(), &a.turn, provider.ToolCall{
		ID: "ro-1", Name: "use_capability", Arguments: `{"action":"call","capability_id":"mcp-tool:test/tool"}`,
	})
}

func TestReadOnlyExecutionBlocksResolvedWriterAndHostStartup(t *testing.T) {
	for _, tc := range []struct {
		name      string
		readOnly  bool
		hostStart bool
	}{
		{name: "writer"},
		{name: "host startup", readOnly: true, hostStart: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			target := readOnlyBoundaryTarget{name: "mcp__test__tool", readOnly: tc.readOnly, hostStart: tc.hostStart, calls: &calls}
			out := executeReadOnlyBoundaryCall(t, tool.ResolvedCall{
				ProxyAction: "call", TargetName: target.Name(), Target: target, ReadOnly: tc.readOnly, Args: json.RawMessage(`{}`),
			})
			if !out.blocked || !strings.Contains(out.output, "read-only agent") {
				t.Fatalf("resolved call outcome = %+v, want host block", out)
			}
			if calls != 0 {
				t.Fatalf("target Execute calls = %d, want 0", calls)
			}
		})
	}
}

func TestReadOnlyExecutionAllowsInspectAndOrdinaryReadOnlyCall(t *testing.T) {
	inspect := executeReadOnlyBoundaryCall(t, tool.ResolvedCall{
		ProxyAction: "inspect", SkipExecute: true, ReadOnly: true, Result: "metadata",
	})
	if inspect.blocked || inspect.errMsg != "" || inspect.output != "metadata" {
		t.Fatalf("inspect outcome = %+v", inspect)
	}

	calls := 0
	target := readOnlyBoundaryTarget{name: "mcp__test__read", readOnly: true, calls: &calls}
	call := executeReadOnlyBoundaryCall(t, tool.ResolvedCall{
		ProxyAction: "call", TargetName: target.Name(), Target: target, ReadOnly: true, Args: json.RawMessage(`{}`),
	})
	if call.blocked || call.errMsg != "" || !strings.Contains(call.output, "target executed") {
		t.Fatalf("read-only call outcome = %+v", call)
	}
	if calls != 1 {
		t.Fatalf("target Execute calls = %d, want 1", calls)
	}
}

func TestReadOnlyExecutionAllowsOnlyAuthorizedReadOnlyMCPStartup(t *testing.T) {
	for _, tc := range []struct {
		name        string
		authorized  bool
		destructive bool
		wantBlocked bool
	}{
		{name: "authorized reader", authorized: true},
		{name: "unauthorized server", wantBlocked: true},
		{name: "destructive reader", authorized: true, destructive: true, wantBlocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			target := layeredReadOnlyMCPBoundaryTarget{
				readOnlyBoundaryTarget: readOnlyBoundaryTarget{
					name: "mcp__test__read", readOnly: true, hostStart: true, calls: &calls,
				},
				destructive: tc.destructive, serverAuthorized: tc.authorized,
			}
			out := executeReadOnlyBoundaryCall(t, tool.ResolvedCall{
				ProxyAction: "call", TargetName: target.Name(), Target: target, ReadOnly: true, Args: json.RawMessage(`{}`),
			})
			if out.blocked != tc.wantBlocked {
				t.Fatalf("layered MCP outcome = %+v, want blocked=%v", out, tc.wantBlocked)
			}
			wantCalls := 1
			if tc.wantBlocked {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("target Execute calls = %d, want %d", calls, wantCalls)
			}
		})
	}
}

func TestStrictReadOnlyExecutionRegistryFailsClosed(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "writer", readOnly: false})
	reg.Add(readOnlyBoundaryTarget{name: "ordinary_read", readOnly: true})
	reg.Add(layeredReadOnlyMCPBoundaryTarget{
		readOnlyBoundaryTarget: readOnlyBoundaryTarget{name: "mcp__test__trusted", readOnly: true, hostStart: true},
		serverAuthorized:       true,
	})
	reg.Add(layeredReadOnlyMCPBoundaryTarget{
		readOnlyBoundaryTarget: readOnlyBoundaryTarget{name: "mcp__test__destructive", readOnly: true, hostStart: true},
		destructive:            true,
	})

	filtered := strictReadOnlyExecutionRegistry(reg)
	if got, want := strings.Join(filtered.Names(), ","), "ordinary_read,mcp__test__trusted"; got != want {
		t.Fatalf("strict registry = %q, want %q", got, want)
	}
}

func TestReadOnlyExecutionBlocksUnauthorizedMCPAndDecline(t *testing.T) {
	calls := 0
	target := layeredReadOnlyMCPBoundaryTarget{readOnlyBoundaryTarget: readOnlyBoundaryTarget{name: "mcp__test__hint", readOnly: true, calls: &calls}}
	out := executeReadOnlyBoundaryCall(t, tool.ResolvedCall{
		ProxyAction: "call", TargetName: target.Name(), Target: target, ReadOnly: true, Args: json.RawMessage(`{}`),
	})
	if !out.blocked || calls != 0 {
		t.Fatalf("unauthorized read-only outcome = %+v calls=%d", out, calls)
	}

	ledger := capability.NewLedger()
	ledger.SeedCandidates(capability.RouteDecision{Candidates: []capability.RouteCandidate{
		{Entry: capability.Entry{ID: "skill:review"}, Policy: capability.AutoUsePrefer},
	}})
	proxy := NewUseCapabilityTool(context.Background(), nil, nil, tool.NewRegistry(), ledger, nil, nil)
	reg := tool.NewRegistry()
	reg.Add(proxy)
	readOnlyAgent := New(nil, reg, NewSession("sys"), Options{ReadOnlyExecution: true}, event.Discard)
	declineArgs := `{"action":"decline","capability_id":"skill:review","reason":"not needed"}`
	decline := readOnlyAgent.executeOne(context.Background(), &readOnlyAgent.turn, provider.ToolCall{ID: "decline-1", Name: "use_capability", Arguments: declineArgs})
	if !decline.blocked {
		t.Fatalf("decline outcome = %+v, want block", decline)
	}
	if gate := ledger.CheckFinalGate(); gate.Reason == "" {
		t.Fatal("read-only decline mutated the capability ledger")
	}

	ordinary := New(nil, reg, NewSession("sys"), Options{}, event.Discard)
	allowed := ordinary.executeOne(context.Background(), &ordinary.turn, provider.ToolCall{ID: "decline-2", Name: "use_capability", Arguments: declineArgs})
	if allowed.blocked || allowed.errMsg != "" {
		t.Fatalf("ordinary executor decline outcome = %+v", allowed)
	}
	if gate := ledger.CheckFinalGate(); gate.Reason != "" {
		t.Fatalf("ordinary decline did not update ledger: %+v", gate)
	}
}

func TestReadOnlyExecutionDoesNotStartUnauthorizedUnconnectedMCP(t *testing.T) {
	host := plugin.NewHost()
	defer host.Close()
	proxy := NewUseCapabilityTool(context.Background(), host, []plugin.Spec{{
		Name: "lazy", Type: "stdio", Command: "reasonix-test-definitely-missing-binary",
	}}, tool.NewRegistry(), capability.NewLedger(), nil, nil)
	reg := tool.NewRegistry()
	reg.Add(proxy)
	a := New(nil, reg, NewSession("sys"), Options{ReadOnlyExecution: true}, event.Discard)
	out := a.executeOne(context.Background(), &a.turn, provider.ToolCall{
		ID: "lazy-1", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-tool:lazy/read_thing","arguments":{}}`,
	})
	if !out.blocked {
		t.Fatalf("lazy MCP outcome = %+v, want block", out)
	}
	if host.HasClient("lazy") {
		t.Fatal("read-only Agent started an unconnected MCP server")
	}
}

func explicitReaderMCPServer(t *testing.T, schemaDrift *atomic.Bool, toolCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if request.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "serverInfo": map[string]any{"name": "explicit-reader", "version": "1"}}
		case "tools/list":
			schemaType := "string"
			if schemaDrift != nil && schemaDrift.Load() {
				schemaType = "number"
			}
			result = map[string]any{"tools": []map[string]any{{
				"name": "search", "description": "search",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": schemaType}}},
				"annotations": map[string]any{"readOnlyHint": true},
			}}}
		case "tools/call":
			toolCalls.Add(1)
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "reader result"}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": *request.ID, "result": result})
	}))
}

func imageMCPServer(t *testing.T, toolCalls *atomic.Int32, payload string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if request.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "serverInfo": map[string]any{"name": "image", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{
				"name": "screenshot", "description": "capture screenshot",
				"inputSchema": map[string]any{"type": "object"},
			}}}
		case "tools/call":
			toolCalls.Add(1)
			result = map[string]any{"content": []map[string]any{
				{"type": "text", "text": "captured "},
				{"type": "image", "mimeType": "image/png", "data": payload},
			}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": *request.ID, "result": result})
	}))
}

func blockingReaderMCPServer(t *testing.T, callStarted chan<- struct{}, releaseCall <-chan struct{}, toolCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if request.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "serverInfo": map[string]any{"name": "blocking-reader", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{
				"name": "search", "description": "search",
				"inputSchema": map[string]any{"type": "object"},
				"annotations": map[string]any{"readOnlyHint": true},
			}}}
		case "tools/call":
			toolCalls.Add(1)
			select {
			case callStarted <- struct{}{}:
			default:
			}
			select {
			case <-releaseCall:
			case <-r.Context().Done():
				return
			}
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "reader result"}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": *request.ID, "result": result})
	}))
}

func opaqueMCPServer(t *testing.T, toolCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if request.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "serverInfo": map[string]any{"name": "opaque", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{
				"name": "query", "description": "query without MCP safety hints",
				"inputSchema": map[string]any{"type": "object"},
			}}}
		case "tools/call":
			toolCalls.Add(1)
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "opaque result"}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": *request.ID, "result": result})
	}))
}

func cacheExplicitReaderSchema(t *testing.T, spec plugin.Spec) {
	t.Helper()
	err := plugin.SaveCachedSchema(spec.Name, plugin.CachedSchema{
		CacheKey: plugin.SchemaCacheKey(spec),
		Tools: []plugin.CachedTool{{
			Name: "search", Description: "search",
			Schema:   json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
			ReadOnly: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlyExecutionStartsInstalledUnconnectedMCPReader(t *testing.T) {
	t.Setenv("REASONIX_CACHE_HOME", t.TempDir())
	var toolCalls atomic.Int32
	server := explicitReaderMCPServer(t, nil, &toolCalls)
	defer server.Close()

	manager := mcplaunch.NewManager(filepath.Join(t.TempDir(), mcplaunch.StateFilename), t.TempDir())
	spec := plugin.Spec{
		Name: "explicit-reader", Type: "http", URL: server.URL,
		LaunchManager: manager, ConfigSource: "workspace_config",
		Authorized: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cacheExplicitReaderSchema(t, spec)

	host := plugin.NewHost()
	defer host.Close()
	proxy := NewUseCapabilityTool(ctx, host, []plugin.Spec{spec}, tool.NewRegistry(), capability.NewLedger(), nil, nil)
	reg := tool.NewRegistry()
	reg.Add(proxy)
	a := New(nil, reg, NewSession("sys"), Options{ReadOnlyExecution: true}, event.Discard)
	out := a.executeOne(ctx, &a.turn, provider.ToolCall{
		ID: "installed-reader-1", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-tool:explicit-reader/search","arguments":{}}`,
	})
	if out.blocked || out.errMsg != "" || !strings.Contains(out.output, "reader result") {
		t.Fatalf("installed lazy reader outcome = %+v", out)
	}
	if got := toolCalls.Load(); got != 1 {
		t.Fatalf("reader tools/call count = %d, want 1", got)
	}
}

func TestReadOnlyExecutionStartsPreviouslyAuthorizedProjectMCPReaderOnDemand(t *testing.T) {
	t.Setenv("REASONIX_CACHE_HOME", t.TempDir())
	var toolCalls atomic.Int32
	server := explicitReaderMCPServer(t, nil, &toolCalls)
	defer server.Close()

	manager := mcplaunch.NewManager(filepath.Join(t.TempDir(), mcplaunch.StateFilename), t.TempDir())
	spec := plugin.Spec{
		Name: "project-reader", Type: "http", URL: server.URL,
		LaunchManager: manager, ConfigSource: "project_config", RequireLaunchApproval: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := plugin.AuthorizeSpecLaunch(ctx, spec); err != nil {
		t.Fatalf("AuthorizeSpecLaunch: %v", err)
	}
	cacheExplicitReaderSchema(t, spec)

	host := plugin.NewHost()
	defer host.Close()
	proxy := NewUseCapabilityTool(ctx, host, []plugin.Spec{spec}, tool.NewRegistry(), capability.NewLedger(), nil, nil)
	reg := tool.NewRegistry()
	reg.Add(proxy)
	a := New(nil, reg, NewSession("sys"), Options{ReadOnlyExecution: true}, event.Discard)
	out := a.executeOne(ctx, &a.turn, provider.ToolCall{
		ID: "authorized-project-1", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-tool:project-reader/search","arguments":{}}`,
	})
	if out.blocked || out.errMsg != "" || !strings.Contains(out.output, "reader result") {
		t.Fatalf("authorized project reader outcome = %+v", out)
	}
	if got := toolCalls.Load(); got != 1 {
		t.Fatalf("project reader tools/call count = %d, want 1", got)
	}
}

func TestReadOnlyExecutionAllowsSchemaOnlyDriftForAuthorizedReader(t *testing.T) {
	t.Setenv("REASONIX_CACHE_HOME", t.TempDir())
	var schemaDrift atomic.Bool
	var toolCalls atomic.Int32
	server := explicitReaderMCPServer(t, &schemaDrift, &toolCalls)
	defer server.Close()

	manager := mcplaunch.NewManager(filepath.Join(t.TempDir(), mcplaunch.StateFilename), t.TempDir())
	spec := plugin.Spec{
		Name: "explicit-reader", Type: "http", URL: server.URL,
		LaunchManager: manager, ConfigSource: "workspace_config",
		Authorized: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cacheExplicitReaderSchema(t, spec)
	schemaDrift.Store(true)

	host := plugin.NewHost()
	defer host.Close()
	proxy := NewUseCapabilityTool(ctx, host, []plugin.Spec{spec}, tool.NewRegistry(), capability.NewLedger(), nil, nil)
	reg := tool.NewRegistry()
	reg.Add(proxy)
	a := New(nil, reg, NewSession("sys"), Options{ReadOnlyExecution: true}, event.Discard)
	out := a.executeOne(ctx, &a.turn, provider.ToolCall{
		ID: "drifted-lazy-1", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-tool:explicit-reader/search","arguments":{}}`,
	})
	if out.blocked || out.errMsg != "" || !strings.Contains(out.output, "reader result") {
		t.Fatalf("schema-only drifted reader outcome = %+v", out)
	}
	if got := toolCalls.Load(); got != 1 {
		t.Fatalf("schema-only drifted reader tools/call count = %d, want 1", got)
	}
}

func TestReadOnlyExecutionDoesNotMarkUnknownCapabilityUnavailable(t *testing.T) {
	ledger := capability.NewLedger()
	proxy := NewUseCapabilityTool(context.Background(), nil, nil, tool.NewRegistry(), ledger, nil, nil)
	reg := tool.NewRegistry()
	reg.Add(proxy)
	a := New(nil, reg, NewSession("sys"), Options{ReadOnlyExecution: true}, event.Discard)
	out := a.executeOne(context.Background(), &a.turn, provider.ToolCall{
		ID: "missing-1", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-tool:missing/read","arguments":{}}`,
	})
	if !out.blocked {
		t.Fatalf("unknown capability outcome = %+v, want block", out)
	}
	if _, ok := ledger.Get("mcp-tool:missing/read"); ok {
		t.Fatal("read-only Agent mutated the ledger for an unknown capability")
	}
}
func (completedProxyCallTool) ResolveCall(context.Context, json.RawMessage) (tool.ResolvedCall, error) {
	return tool.ResolvedCall{
		DisplayName:  "use_capability",
		ProxyAction:  "call",
		CapabilityID: "mcp-server:mock",
		SkipExecute:  true,
		ReadOnly:     true,
		Result:       "mcp-tool:mock/echo",
	}, nil
}

func TestUseCapabilityDeclineAndInspect(t *testing.T) {
	ledger := capability.NewLedger()
	ledger.SeedCandidates(capability.RouteDecision{Candidates: []capability.RouteCandidate{
		{Entry: capability.Entry{ID: "skill:review"}, Policy: capability.AutoUsePrefer},
	}})
	audit := &capability.Audit{}
	tl := NewUseCapabilityTool(context.Background(), nil, nil, tool.NewRegistry(), ledger, audit, func() capability.Catalog {
		return capability.Catalog{Entries: []capability.Entry{{
			ID: "skill:review", Kind: capability.KindSkill, Name: "review", Description: "review code", Status: capability.StatusReady,
		}}}
	})

	out, err := tl.Execute(context.Background(), json.RawMessage(`{"action":"inspect","capability_id":"skill:review"}`))
	if err != nil || !strings.Contains(out, "skill:review") {
		t.Fatalf("inspect: out=%q err=%v", out, err)
	}
	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"action":"decline","capability_id":"skill:review","reason":"not needed"}`)); err != nil {
		t.Fatal(err)
	}
	if gate := ledger.CheckFinalGate(); gate.Reason != "" {
		t.Fatalf("after decline gate = %+v", gate)
	}
	if got := audit.Snapshot().Declines; got != 1 {
		t.Fatalf("decline audit = %d, want 1", got)
	}
	// Cannot decline require.
	ledger.SeedCandidates(capability.RouteDecision{Candidates: []capability.RouteCandidate{
		{Entry: capability.Entry{ID: "skill:must"}, Policy: capability.AutoUseRequire},
	}})
	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"action":"decline","capability_id":"skill:must","reason":"no"}`)); err == nil {
		t.Fatal("expected decline of require to fail")
	}
}

func TestUseCapabilityInspectMCPToolDoesNotListSiblingSchemas(t *testing.T) {
	t.Setenv("REASONIX_CACHE_HOME", t.TempDir())
	spec := plugin.Spec{Name: "db", Authorized: true}
	if err := plugin.SaveCachedSchema(spec.Name, plugin.CachedSchema{
		CacheKey: plugin.SchemaCacheKey(spec),
		Tools: []plugin.CachedTool{
			{Name: "read", Description: "allowed reader", Schema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`), ReadOnly: true},
			{Name: "drop", Description: "secret destructive sibling", Schema: json.RawMessage(`{"type":"object","properties":{"table":{"type":"string"}}}`), Destructive: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	target := capability.Entry{
		ID: "mcp-tool:db/read", Kind: capability.KindMCPTool, Name: "read",
		Description: "allowed reader", Source: "db", Status: capability.StatusConfigured,
	}
	tl := NewUseCapabilityTool(context.Background(), nil, []plugin.Spec{spec}, tool.NewRegistry(), nil, nil, func() capability.Catalog {
		return capability.Catalog{Entries: []capability.Entry{target}}
	})

	out, err := tl.Execute(context.Background(), json.RawMessage(`{"action":"inspect","capability_id":"mcp-tool:db/read"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "mcp-tool:db/read") || !strings.Contains(out, "allowed reader") {
		t.Fatalf("inspect omitted allowed tool metadata:\n%s", out)
	}
	if strings.Contains(out, "mcp-tool:db/drop") || strings.Contains(out, "secret destructive sibling") || strings.Contains(out, `"table"`) {
		t.Fatalf("tool inspection leaked sibling metadata:\n%s", out)
	}
}

func TestDedicatedSecurityReviewUsesCanonicalSkillCapabilityID(t *testing.T) {
	got := capabilityIDFromToolCall("security_review", json.RawMessage(`{"task":"audit auth"}`))
	if got != "skill:security-review" {
		t.Fatalf("capability ID = %q, want skill:security-review", got)
	}
}

func TestUseCapabilitySkillCallAcceptsStringArgumentsPayload(t *testing.T) {
	var ran string
	runner := func(_ context.Context, sk skill.Skill, task string, _ skill.SubagentRunOptions) (string, error) {
		ran = sk.Name + ":" + task
		return "ok", nil
	}
	store := skill.New(skill.Options{HomeDir: t.TempDir()})
	reg := tool.NewRegistry()
	reg.Add(skill.NewRunSkillTool(store, runner))
	tl := NewUseCapabilityTool(context.Background(), nil, nil, reg, capability.NewLedger(), nil, nil)

	out, err := tl.Execute(context.Background(), json.RawMessage(`{"action":"call","capability_id":"skill:explore","arguments":"{\"task\":\"map PicAnnotateCenter\"}"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out != "ok" {
		t.Fatalf("output = %q, want ok", out)
	}
	if ran != "explore:map PicAnnotateCenter" {
		t.Fatalf("runner = %q, want explore task", ran)
	}
}

func TestUseCapabilitySkillCallAcceptsDedicatedTaskPayload(t *testing.T) {
	var ran string
	var gotOpts skill.SubagentRunOptions
	runner := func(_ context.Context, sk skill.Skill, task string, opts skill.SubagentRunOptions) (string, error) {
		ran = sk.Name + ":" + task
		gotOpts = opts
		return "ok", nil
	}
	store := skill.New(skill.Options{HomeDir: t.TempDir()})
	reg := tool.NewRegistry()
	reg.Add(skill.NewRunSkillTool(store, runner))
	tl := NewUseCapabilityTool(context.Background(), nil, nil, reg, capability.NewLedger(), nil, nil)

	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"action":"call","capability_id":"skill:explore","arguments":{"task":"map the loop","continue_from":"sa_prev"}}`)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if ran != "explore:map the loop" {
		t.Fatalf("runner = %q, want explore task", ran)
	}
	if gotOpts.ContinueFrom != "sa_prev" {
		t.Fatalf("continue_from = %q, want sa_prev", gotOpts.ContinueFrom)
	}
}

func TestSkillInvocationUnavailableIsAudited(t *testing.T) {
	audit := &capability.Audit{}
	a := New(&scriptedProvider{name: "p"}, tool.NewRegistry(), NewSession("sys"), Options{
		CapabilityLedger: capability.NewLedger(),
		CapabilityAudit:  audit,
	}, event.Discard)
	a.noteCapabilityInvocation("run_skill", json.RawMessage(`{"name":"delivery-only"}`), fmt.Errorf("run_skill: %w", skill.ErrInvocationUnavailable))
	snap := audit.Snapshot()
	if snap.SkillInvocations != 1 || snap.SkillFailures != 1 || snap.SkillUnavailable != 1 {
		t.Fatalf("skill unavailable audit: invocations=%d failures=%d unavailable=%d",
			snap.SkillInvocations, snap.SkillFailures, snap.SkillUnavailable)
	}
}

func TestUseCapabilityProxyHonorsRealMCPPermissionDeny(t *testing.T) {
	// Register a fake MCP tool in the registry so resolve uses it without host.
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "mcp__github__search_issues", readOnly: true})
	tl := NewUseCapabilityTool(context.Background(), nil, nil, reg, capability.NewLedger(), nil, nil)

	resolved, err := tl.ResolveCall(context.Background(), json.RawMessage(`{"action":"call","capability_id":"mcp-tool:github/search_issues","arguments":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.TargetName != "mcp__github__search_issues" {
		t.Fatalf("target = %q", resolved.TargetName)
	}
	if resolved.Target == nil {
		t.Fatal("expected resolved target tool")
	}
	gate := denyAllGate{}
	allow, reason, _ := gate.Check(context.Background(), resolved.TargetName, resolved.Args, resolved.ReadOnly)
	if allow || !strings.Contains(reason, "mcp__github__search_issues") {
		t.Fatalf("gate allow=%v reason=%q", allow, reason)
	}
}

func TestReviewReportToolValidatesSchema(t *testing.T) {
	tl := NewReviewReportTool()
	led := evidence.NewLedger()
	led.Record(evidence.ReceiptFromToolCall("read_file", json.RawMessage(`{"path":"a.go"}`), true, true))
	ctx := evidence.WithLedger(context.Background(), led)
	if _, err := tl.Execute(ctx, json.RawMessage(`{"kind":"review","verdict":"pass","reviewed_paths":[]}`)); err == nil {
		t.Fatal("empty reviewed_paths should fail")
	}
	out, err := tl.Execute(ctx, json.RawMessage(`{"kind":"security","verdict":"block","reviewed_paths":["a.go"],"findings":[{"severity":"critical","summary":"secret"}]}`))
	if err != nil || !strings.Contains(out, "blocking") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestReviewReportRequiresHostReadEvidence(t *testing.T) {
	tl := NewReviewReportTool()
	// No ledger on ctx: fail closed.
	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"kind":"review","verdict":"pass","reviewed_paths":["a.go"]}`)); err == nil {
		t.Fatal("expected failure without a host evidence ledger")
	}
	led := evidence.NewLedger()
	ctx := evidence.WithLedger(context.Background(), led)
	// Claimed paths without any host-observed read: rejected, names the path.
	_, err := tl.Execute(ctx, json.RawMessage(`{"kind":"review","verdict":"pass","reviewed_paths":["internal/agent/agent.go"]}`))
	if err == nil || !strings.Contains(err.Error(), "internal/agent/agent.go") {
		t.Fatalf("expected fake-coverage rejection naming the path, got %v", err)
	}
	// A successful read receipt makes the same report acceptable.
	led.Record(evidence.ReceiptFromToolCall("read_file", json.RawMessage(`{"path":"internal/agent/agent.go"}`), true, true))
	if _, err := tl.Execute(ctx, json.RawMessage(`{"kind":"review","verdict":"pass","reviewed_paths":["internal/agent/agent.go"]}`)); err != nil {
		t.Fatalf("host-read path should be accepted: %v", err)
	}
	// A git-diff bash receipt with real printed output also counts.
	led2 := evidence.NewLedger()
	diffRec := evidence.ReceiptFromToolCall("bash", json.RawMessage(`{"command":"git diff -- internal/boot/boot.go"}`), true, true)
	diffRec.OutputBytes = 512
	led2.Record(diffRec)
	ctx2 := evidence.WithLedger(context.Background(), led2)
	if _, err := tl.Execute(ctx2, json.RawMessage(`{"kind":"review","verdict":"pass","reviewed_paths":["internal/boot/boot.go"]}`)); err != nil {
		t.Fatalf("diffed path should be accepted: %v", err)
	}
}

func TestReviewReportRejectsNonContentEvidence(t *testing.T) {
	tl := NewReviewReportTool()
	report := json.RawMessage(`{"kind":"review","verdict":"pass","reviewed_paths":["internal/agent/agent.go"]}`)

	// git status mentions the path but never shows content.
	led := evidence.NewLedger()
	led.Record(evidence.ReceiptFromToolCall("bash", json.RawMessage(`{"command":"git status --short -- internal/agent/agent.go"}`), true, true))
	if _, err := tl.Execute(evidence.WithLedger(context.Background(), led), report); err == nil {
		t.Fatal("git status must not count as review evidence")
	}
	// echo output containing the path shows nothing either.
	led = evidence.NewLedger()
	led.Record(evidence.ReceiptFromToolCall("bash", json.RawMessage(`{"command":"echo internal/agent/agent.go"}`), true, true))
	if _, err := tl.Execute(evidence.WithLedger(context.Background(), led), report); err == nil {
		t.Fatal("echo must not count as review evidence")
	}
	// Writing a file is not reviewing it.
	led = evidence.NewLedger()
	led.Record(evidence.ReceiptFromToolCall("write_file", json.RawMessage(`{"path":"internal/agent/agent.go"}`), true, false))
	if _, err := tl.Execute(evidence.WithLedger(context.Background(), led), report); err == nil {
		t.Fatal("a write receipt must not count as review evidence")
	}
	// A bare basename read must not satisfy a claim for a specific full path.
	led = evidence.NewLedger()
	led.Record(evidence.ReceiptFromToolCall("read_file", json.RawMessage(`{"path":"agent.go"}`), true, true))
	if _, err := tl.Execute(evidence.WithLedger(context.Background(), led), report); err == nil {
		t.Fatal("reverse basename matching must not count as review evidence")
	}
	// Content-suppressing shell shapes: each produced-or-not output case must fail.
	bashCases := []struct {
		name    string
		command string
		output  int
	}{
		{"null redirect", "cat internal/agent/agent.go >/dev/null", 0},
		{"null redirect with output claim", "cat internal/agent/agent.go >/dev/null", 64},
		{"stat only", "git diff --stat -- internal/agent/agent.go", 64},
		{"name only", "git diff --name-only -- internal/agent/agent.go", 64},
		{"zero lines", "head -n 0 internal/agent/agent.go", 0},
		{"pipeline transform", "cat internal/agent/agent.go | wc -l", 8},
		{"and unrelated output", "git diff HEAD~1 -- internal/agent/agent.go && echo done", 512},
		{"or unrelated output", "git diff HEAD~1 -- internal/agent/agent.go || echo done", 512},
		{"separate unrelated output", "git diff HEAD~1 -- internal/agent/agent.go; echo done", 512},
		{"git show metadata", "git show HEAD -- internal/agent/agent.go", 512},
		{"substring superset", "cat internal/agent/agent.go.bak", 512},
	}
	for _, tc := range bashCases {
		led := evidence.NewLedger()
		rec := evidence.ReceiptFromToolCall("bash", json.RawMessage(`{"command":`+strconv.Quote(tc.command)+`}`), true, true)
		rec.OutputBytes = tc.output
		led.Record(rec)
		if _, err := tl.Execute(evidence.WithLedger(context.Background(), led), report); err == nil {
			t.Fatalf("%s (%q) must not count as review evidence", tc.name, tc.command)
		}
	}
	// Genuine content commands with real output still pass.
	for _, cmd := range []string{
		"cat internal/agent/agent.go",
		"git show HEAD:internal/agent/agent.go",
		"git diff HEAD~1 -- internal/agent/agent.go",
	} {
		led := evidence.NewLedger()
		rec := evidence.ReceiptFromToolCall("bash", json.RawMessage(`{"command":`+strconv.Quote(cmd)+`}`), true, true)
		rec.OutputBytes = 512
		led.Record(rec)
		if _, err := tl.Execute(evidence.WithLedger(context.Background(), led), report); err != nil {
			t.Fatalf("%q with real output should count as review evidence: %v", cmd, err)
		}
	}
}

func TestUseCapabilityServerConnectHonorsPermissionInPlanMode(t *testing.T) {
	host := plugin.NewHost()
	defer host.Close()
	specs := []plugin.Spec{{Name: "lazy", Type: "stdio", Command: "reasonix-test-definitely-missing-binary", Authorized: true}}
	reg := tool.NewRegistry()
	uc := NewUseCapabilityTool(context.Background(), host, specs, reg, capability.NewLedger(), nil, nil)
	reg.Add(uc)

	resolved, err := uc.ResolveCall(context.Background(), json.RawMessage(`{"action":"call","capability_id":"mcp-server:lazy"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Target == nil || resolved.SkipExecute {
		t.Fatalf("expected deferred connect target, got %+v", resolved)
	}
	if resolved.TargetName != plugin.MCPConnectPermissionName("lazy") || resolved.ReadOnly {
		t.Fatalf("connect gating identity wrong: name=%q readOnly=%v", resolved.TargetName, resolved.ReadOnly)
	}
	policyGate := permission.NewGate(permission.New("ask", nil, nil, []string{plugin.MCPConnectPermissionName("lazy")}), nil)
	allow, _, err := policyGate.Check(context.Background(), resolved.TargetName, resolved.Args, resolved.ReadOnly)
	if err != nil || allow {
		t.Fatalf("exact MCP connect deny must block before spawn: allow=%v err=%v", allow, err)
	}
	deniedAgent := New(&scriptedProvider{name: "p"}, reg, NewSession("sys"), Options{Gate: policyGate}, event.Discard)
	deniedAgent.SetPlanMode(true)
	denied := deniedAgent.executeOne(context.Background(), &deniedAgent.turn, provider.ToolCall{
		ID: "deny", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-server:lazy"}`,
	})
	if !denied.blocked || host.HasClient("lazy") {
		t.Fatalf("exact connect deny must block before process start: outcome=%+v connected=%v", denied, host.HasClient("lazy"))
	}
	if host.HasClient("lazy") {
		t.Fatal("server-level resolution must not start the server")
	}
}

func TestOnDemandModelNameMatchesPluginCanonicalName(t *testing.T) {
	host := plugin.NewHost()
	defer host.Close()
	specs := []plugin.Spec{{Name: "lazy", Type: "stdio", Command: "reasonix-test-definitely-missing-binary"}}
	tl := NewUseCapabilityTool(context.Background(), host, specs, tool.NewRegistry(), capability.NewLedger(), nil, nil)
	for _, raw := range []string{"@model/tool", "search/issues", "with space", "plain_ok"} {
		resolved, err := tl.ResolveCall(context.Background(),
			json.RawMessage(`{"action":"call","capability_id":"mcp-tool:lazy/`+raw+`"}`))
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		want := plugin.ModelToolName("lazy", raw)
		if resolved.TargetName != want {
			t.Fatalf("raw %q: permission-checked name %q differs from executed canonical name %q — deny/ask rules would miss", raw, resolved.TargetName, want)
		}
	}
}

func TestProxyCallAuditCountsOnAgentPath(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "mcp__github__search_issues", readOnly: true})
	audit := &capability.Audit{}
	uc := NewUseCapabilityTool(context.Background(), nil, nil, reg, capability.NewLedger(), audit, nil)
	reg.Add(uc)
	a := New(&scriptedProvider{name: "p"}, reg, NewSession("sys"),
		Options{CapabilityLedger: capability.NewLedger(), CapabilityAudit: audit}, event.Discard)
	out := a.executeOne(context.Background(), &a.turn, provider.ToolCall{
		ID: "1", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-tool:github/search_issues","arguments":{}}`,
	})
	if out.blocked || out.errMsg != "" {
		t.Fatalf("call failed: %+v", out)
	}
	if snap := audit.Snapshot(); snap.MCPCall != 1 || snap.MCPCallFailures != 0 {
		t.Fatalf("MCPCall=%d failures=%d, want 1/0", snap.MCPCall, snap.MCPCallFailures)
	}
}

func TestCompletedProxyCallCountsOnAgentSkipExecutePath(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(completedProxyCallTool{})
	ledger := capability.NewLedger()
	audit := &capability.Audit{}
	a := New(&scriptedProvider{name: "p"}, reg, NewSession("sys"),
		Options{CapabilityLedger: ledger, CapabilityAudit: audit}, event.Discard)
	out := a.executeOne(context.Background(), &a.turn, provider.ToolCall{
		ID: "1", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-server:mock"}`,
	})
	if out.blocked || out.errMsg != "" {
		t.Fatalf("completed call failed: %+v", out)
	}
	if entry, ok := ledger.Get("mcp-server:mock"); !ok || entry.Outcome != capability.OutcomeSucceeded {
		t.Fatalf("completed call ledger = %+v, found=%v", entry, ok)
	}
	if snap := audit.Snapshot(); snap.MCPCall != 1 || snap.MCPCallFailures != 0 {
		t.Fatalf("completed call audit = %d/%d, want 1/0", snap.MCPCall, snap.MCPCallFailures)
	}
}
func TestCapabilityGateRecoveryIsAudited(t *testing.T) {
	reg := tool.NewRegistry()
	audit := &capability.Audit{}
	a := New(&scriptedProvider{name: "p"}, reg, NewSession("sys"),
		Options{CapabilityLedger: capability.NewLedger(), CapabilityAudit: audit}, event.Discard)
	a.turn = turnRuntime{deliveryScopeActive: true}
	a.SeedCapabilityRoute(capability.RouteDecision{Candidates: []capability.RouteCandidate{
		{Entry: capability.Entry{ID: "skill:review"}, Policy: capability.AutoUseRequire},
	}})
	a.task.ledger.Record(evidence.ReceiptFromToolCall("read_file", json.RawMessage(`{"path":"a.go"}`), true, true))
	if check := a.finalReadinessCheckFor(); check.reason == "" {
		t.Fatal("expected a require miss first")
	}
	a.capabilityLedger.MarkInvoked("skill:review")
	a.capabilityLedger.MarkSucceeded("skill:review")
	if check := a.finalReadinessCheckFor(); strings.Contains(check.reason, "required capabilities") {
		t.Fatalf("gate should be clean after success, reason=%q", check.reason)
	}
	if snap := audit.Snapshot(); snap.RequireRecovered != 1 {
		t.Fatalf("RequireRecovered=%d, want 1", snap.RequireRecovered)
	}
}

func TestRunSubAgentRequiresReviewReport(t *testing.T) {
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{{Type: provider.ChunkText, Text: "looks fine"}, {Type: provider.ChunkDone}},
	}}
	_, err := RunSubAgentWithSession(context.Background(), prov, tool.NewRegistry(), NewSession("sys"), "review it",
		Options{RequireReviewReportKind: evidence.ReviewKindReview}, event.Discard)
	if err == nil {
		t.Fatal("expected missing-report failure")
	}
	if !IsReviewUnavailable(err) && !strings.Contains(err.Error(), "review_report") && !strings.Contains(err.Error(), "reviewer unavailable") {
		t.Fatalf("expected review unavailable / review_report failure, got %v", err)
	}
}

func TestUseCapabilityResolveCallIsSideEffectFree(t *testing.T) {
	host := plugin.NewHost()
	defer host.Close()
	specs := []plugin.Spec{{
		Name:    "lazy",
		Type:    "stdio",
		Command: "reasonix-test-definitely-missing-binary",
	}}
	tl := NewUseCapabilityTool(context.Background(), host, specs, tool.NewRegistry(), capability.NewLedger(), nil, nil)

	resolved, err := tl.ResolveCall(context.Background(), json.RawMessage(`{"action":"call","capability_id":"mcp-tool:lazy/do_write","arguments":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.SkipExecute || resolved.Target == nil {
		t.Fatalf("expected a deferred target, got %+v", resolved)
	}
	if resolved.ReadOnly {
		t.Fatal("unstarted tool without read-only metadata must resolve as a writer")
	}
	if host.HasClient("lazy") {
		t.Fatal("ResolveCall must not start the MCP server")
	}
	// Execution is where the connect finally happens — and fails for the
	// missing binary, marking the capability unavailable.
	ledger := capability.NewLedger()
	tl.ledger = ledger
	if _, err := resolved.Target.Execute(context.Background(), resolved.Args); err == nil {
		t.Fatal("expected connect failure for missing binary")
	}
	if e, ok := ledger.Get("mcp-tool:lazy/do_write"); !ok || e.Outcome != capability.OutcomeUnavailable {
		t.Fatalf("expected unavailable outcome, got %+v ok=%v", e, ok)
	}
}

func TestUseCapabilityInspectDoesNotStartServer(t *testing.T) {
	host := plugin.NewHost()
	defer host.Close()
	specs := []plugin.Spec{{Name: "lazy", Type: "stdio", Command: "reasonix-test-definitely-missing-binary"}}
	tl := NewUseCapabilityTool(context.Background(), host, specs, tool.NewRegistry(), capability.NewLedger(), nil, func() capability.Catalog {
		return capability.Catalog{Entries: []capability.Entry{{
			ID: "mcp-server:lazy", Kind: capability.KindMCPServer, Name: "lazy", Source: "lazy", Status: capability.StatusConfigured,
		}}}
	})
	out, err := tl.Execute(context.Background(), json.RawMessage(`{"action":"inspect","capability_id":"mcp-server:lazy"}`))
	if err != nil {
		t.Fatal(err)
	}
	if host.HasClient("lazy") {
		t.Fatal("inspect must not start the MCP server")
	}
	if !strings.Contains(out, "not connected") {
		t.Fatalf("inspect output should say the server is not connected: %q", out)
	}
}

func TestPlanModeBlocksInstalledWriteMCPResolvedThroughUseCapability(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(annotatedMCPTool{fakeTool: fakeTool{name: "mcp__github__create_issue", readOnly: false}, server: "github", raw: "create_issue"})
	reg.Add(annotatedMCPTool{fakeTool: fakeTool{name: "mcp__github__search_issues", readOnly: true}, server: "github", raw: "search_issues", serverAuthorized: true})
	uc := NewUseCapabilityTool(context.Background(), nil, nil, reg, capability.NewLedger(), nil, nil)
	reg.Add(uc)
	gate := &mcpPermissionRecordingGate{allowNormal: true}
	a := New(&scriptedProvider{name: "p"}, reg, NewSession("sys"), Options{Gate: gate}, event.Discard)
	a.planMode.Store(true)

	out := a.executeOne(context.Background(), &a.turn, provider.ToolCall{
		ID: "1", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-tool:github/create_issue","arguments":{}}`,
	})
	if !out.blocked || gate.normalCalls != 0 {
		t.Fatalf("installed MCP writer should be blocked before permission, outcome=%+v calls=%d", out, gate.normalCalls)
	}
	// A read-only target still passes through the proxy in plan mode.
	out = a.executeOne(context.Background(), &a.turn, provider.ToolCall{
		ID: "2", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-tool:github/search_issues","arguments":{}}`,
	})
	if out.blocked {
		t.Fatalf("read-only proxy call should pass in plan mode, got %+v", out)
	}
}

func TestPlanModeMCPStyleNameWithoutMetadataStillUsesPermission(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "mcp__github__create_issue", readOnly: false})
	uc := NewUseCapabilityTool(context.Background(), nil, nil, reg, capability.NewLedger(), nil, nil)
	reg.Add(uc)
	gate := &recordingPermissionGate{reason: "denied by ordinary permission"}
	a := New(&scriptedProvider{name: "p"}, reg, NewSession("sys"), Options{Gate: gate}, event.Discard)
	a.planMode.Store(true)

	out := a.executeOne(context.Background(), &a.turn, provider.ToolCall{
		ID: "1", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-tool:github/create_issue","arguments":{}}`,
	})
	if !out.blocked || !strings.Contains(out.output, "Plan mode") || len(gate.calls) != 0 {
		t.Fatalf("MCP-style name must be hard-blocked in Plan: outcome=%+v calls=%+v", out, gate.calls)
	}
}

func TestPlanModeBlocksAuthorizedDestructiveMCPThroughUseCapability(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(annotatedMCPTool{
		fakeTool:         fakeTool{name: "mcp__github__delete_issue", readOnly: false},
		server:           "github",
		raw:              "delete_issue",
		destructive:      true,
		serverAuthorized: true,
	})
	uc := NewUseCapabilityTool(context.Background(), nil, nil, reg, capability.NewLedger(), nil, nil)
	reg.Add(uc)
	gate := &mcpPermissionRecordingGate{allowNormal: true}
	a := New(&scriptedProvider{name: "p"}, reg, NewSession("sys"), Options{Gate: gate}, event.Discard)
	a.planMode.Store(true)

	out := a.executeOne(context.Background(), &a.turn, provider.ToolCall{
		ID: "1", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-tool:github/delete_issue","arguments":{"number":1}}`,
	})
	if !out.blocked || gate.normalCalls != 0 {
		t.Fatalf("destructive proxy should be blocked before permission, outcome=%+v normal=%d", out, gate.normalCalls)
	}
}

func TestCapabilityGateAppliesToReadOnlyTasks(t *testing.T) {
	reg := tool.NewRegistry()
	a := New(&scriptedProvider{name: "p"}, reg, NewSession("sys"),
		Options{CapabilityLedger: capability.NewLedger()}, event.Discard)
	a.turn = turnRuntime{deliveryScopeActive: true}
	a.SeedCapabilityRoute(capability.RouteDecision{Candidates: []capability.RouteCandidate{
		{Entry: capability.Entry{ID: "skill:review"}, Policy: capability.AutoUseRequire},
	}})
	a.task.ledger.Record(evidence.ReceiptFromToolCall("read_file", json.RawMessage(`{"path":"a.go"}`), true, true))
	if check := a.finalReadinessCheckFor(); !strings.Contains(check.reason, "required capabilities") {
		t.Fatalf("read-only answer must not skip the require gate; reason = %q", check.reason)
	}
}

func TestUseCapabilityListActionNoSideEffects(t *testing.T) {
	host := plugin.NewHost()
	defer host.Close()
	proxy := NewUseCapabilityTool(context.Background(), host, []plugin.Spec{
		{Name: "zeta", Authorized: true},
		{Name: "alpha", Authorized: true},
	}, tool.NewRegistry(), nil, nil, nil)
	resolved, err := proxy.ResolveCall(context.Background(), json.RawMessage(`{"action":"list"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.SkipExecute || !resolved.ReadOnly || resolved.Target != nil {
		t.Fatalf("list resolve = %+v", resolved)
	}
	if !strings.Contains(resolved.Result, `"name": "alpha"`) || !strings.Contains(resolved.Result, `"name": "zeta"`) {
		t.Fatalf("list result missing sorted servers:\n%s", resolved.Result)
	}
	// alpha must appear before zeta in the JSON array for stable ordering.
	if idxA, idxZ := strings.Index(resolved.Result, `"name": "alpha"`), strings.Index(resolved.Result, `"name": "zeta"`); idxA < 0 || idxZ < 0 || idxA > idxZ {
		t.Fatalf("list servers not sorted: alpha@%d zeta@%d\n%s", idxA, idxZ, resolved.Result)
	}
	if host.HasClient("alpha") || host.HasClient("zeta") {
		t.Fatal("list must not start servers")
	}
}

func TestPlannerAllowsAuthorizedNonReadOnlyNonDestructiveMCP(t *testing.T) {
	calls := 0
	target := layeredReadOnlyMCPBoundaryTarget{
		readOnlyBoundaryTarget: readOnlyBoundaryTarget{name: "mcp__db__query", readOnly: false, calls: &calls},
		serverAuthorized:       true,
	}
	reg := tool.NewRegistry()
	reg.Add(readOnlyBoundaryProxy{resolved: tool.ResolvedCall{
		ProxyAction: "call", TargetName: target.Name(), Target: target, ReadOnly: false, Args: json.RawMessage(`{}`),
	}})
	// Ordinary strict read-only still blocks non-readOnly MCP.
	strict := New(nil, reg, NewSession("sys"), Options{ReadOnlyExecution: true}, event.Discard)
	strictOut := strict.executeOne(context.Background(), &strict.turn, provider.ToolCall{
		ID: "s1", Name: "use_capability", Arguments: `{"action":"call","capability_id":"mcp-tool:db/query","arguments":{}}`,
	})
	if !strictOut.blocked || calls != 0 {
		t.Fatalf("strict read-only outcome = %+v calls=%d", strictOut, calls)
	}

	// Planner trusts authorized non-destructive MCP without readOnlyHint.
	planner := NewPlannerAgent(nil, reg, NewSession("sys"), Options{}, event.Discard)
	planner.SetPlanMode(true)
	if !planner.plannerMCPExecution || !planner.readOnlyExecution {
		t.Fatalf("planner flags = plannerMCP=%v readOnly=%v", planner.plannerMCPExecution, planner.readOnlyExecution)
	}
	out := planner.executeOne(context.Background(), &planner.turn, provider.ToolCall{
		ID: "p1", Name: "use_capability", Arguments: `{"action":"call","capability_id":"mcp-tool:db/query","arguments":{}}`,
	})
	if out.blocked || out.errMsg != "" || !strings.Contains(out.output, "target executed") {
		t.Fatalf("planner non-readonly MCP outcome = %+v", out)
	}
	if calls != 1 {
		t.Fatalf("planner target Execute calls = %d, want 1", calls)
	}
}

func TestPlannerPlanModeExecutesAuthorizedOpaqueMCPThroughRuntime(t *testing.T) {
	t.Setenv("REASONIX_CACHE_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var toolCalls atomic.Int32
	server := opaqueMCPServer(t, &toolCalls)
	defer server.Close()
	spec := plugin.Spec{Name: "opaque", Type: "http", URL: server.URL, Authorized: true}
	host := plugin.NewHost()
	defer host.Close()
	if _, err := host.Add(ctx, spec); err != nil {
		t.Fatal(err)
	}
	runtime := NewMCPCapabilityRuntime(ctx, host, []plugin.Spec{spec}, tool.NewRegistry(), nil)
	reg := tool.NewRegistry()
	reg.Add(runtime.NewFrontend(capability.NewLedger(), nil))
	planner := NewPlannerAgent(nil, reg, NewSession("sys"), Options{Gate: denyAllGate{}}, event.Discard)
	planner.SetPlanMode(true)

	out := planner.executeOne(ctx, &planner.turn, provider.ToolCall{
		ID: "opaque-plan", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-tool:opaque/query","arguments":{}}`,
	})
	if out.blocked || out.errMsg != "" || !strings.Contains(out.output, "opaque result") {
		t.Fatalf("Planner opaque MCP outcome = %+v", out)
	}
	if got := toolCalls.Load(); got != 1 {
		t.Fatalf("opaque tools/call count = %d, want 1", got)
	}
}

func TestPlannerAllowsConnectedServerDirectoryCall(t *testing.T) {
	t.Setenv("REASONIX_CACHE_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var toolCalls atomic.Int32
	server := explicitReaderMCPServer(t, nil, &toolCalls)
	defer server.Close()

	spec := plugin.Spec{Name: "connected", Type: "http", URL: server.URL, Authorized: true}
	host := plugin.NewHost()
	defer host.Close()
	if _, err := host.Add(ctx, spec); err != nil {
		t.Fatal(err)
	}
	runtime := NewMCPCapabilityRuntime(ctx, host, []plugin.Spec{spec}, tool.NewRegistry(), nil)
	reg := tool.NewRegistry()
	reg.Add(runtime.NewFrontend(capability.NewLedger(), nil))
	planner := NewPlannerAgent(nil, reg, NewSession("sys"), Options{}, event.Discard)

	out := planner.executeOne(ctx, &planner.turn, provider.ToolCall{
		ID: "connected-directory", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-server:connected"}`,
	})
	if out.blocked || out.errMsg != "" {
		t.Fatalf("connected server directory outcome = %+v", out)
	}
	if !strings.Contains(out.output, `mcp-tool:connected/search`) {
		t.Fatalf("connected server directory missing tool capability: %q", out.output)
	}
	if toolCalls.Load() != 0 {
		t.Fatalf("server directory call executed tools/call %d times, want 0", toolCalls.Load())
	}
}

func TestResolvedCapabilityDispatchRefreshesWriterClassification(t *testing.T) {
	calls := 0
	target := readOnlyBoundaryTarget{name: "mcp__db__write", readOnly: false, calls: &calls}
	reg := tool.NewRegistry()
	reg.Add(readOnlyBoundaryProxy{resolved: tool.ResolvedCall{
		ProxyAction:  "call",
		CapabilityID: "mcp-tool:db/write",
		TargetName:   target.Name(),
		Target:       target,
		ReadOnly:     false,
		Args:         json.RawMessage(`{"value":"x"}`),
	}})
	session := NewSession("sys")
	call := provider.ToolCall{
		ID: "writer-1", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-tool:db/write","arguments":{"value":"x"}}`,
	}
	session.Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}})
	var events []event.Event
	a := New(nil, reg, session, Options{}, event.FuncSink(func(e event.Event) {
		events = append(events, e)
	}))

	results := a.executeBatch(context.Background(), &a.turn, []provider.ToolCall{call}).results
	if calls != 1 || len(results) != 1 || results[0] != "target executed" {
		t.Fatalf("execution calls=%d results=%v", calls, results)
	}

	var dispatches []event.Tool
	var result event.Tool
	for _, e := range events {
		switch e.Kind {
		case event.ToolDispatch:
			dispatches = append(dispatches, e.Tool)
		case event.ToolResult:
			result = e.Tool
		}
	}
	if len(dispatches) != 2 {
		t.Fatalf("dispatch count = %d, want initial + resolved refresh: %+v", len(dispatches), dispatches)
	}
	if dispatches[0].Refreshed || !dispatches[0].ReadOnly {
		t.Fatalf("initial proxy dispatch = %+v, want surface ReadOnly=true", dispatches[0])
	}
	refreshed := dispatches[1]
	if !refreshed.Refreshed || refreshed.ReadOnly || refreshed.ResolvedName != target.Name() || refreshed.CapabilityID != "mcp-tool:db/write" {
		t.Fatalf("resolved dispatch = %+v", refreshed)
	}
	if result.ReadOnly || result.ResolvedName != target.Name() || result.CapabilityID != "mcp-tool:db/write" {
		t.Fatalf("resolved result = %+v", result)
	}

	stored := session.Snapshot()[1].ToolCalls[0]
	if stored.ResolvedReadOnly == nil || *stored.ResolvedReadOnly || stored.ResolvedName != target.Name() || stored.CapabilityID != "mcp-tool:db/write" {
		t.Fatalf("stored resolved metadata = %+v", stored)
	}
}

func TestResolvedCapabilityRefreshesParallelCallsInProviderOrder(t *testing.T) {
	target := fakeTool{name: "mcp__db__query", readOnly: true, delay: 5 * time.Millisecond}
	reg := tool.NewRegistry()
	reg.Add(readOnlyBoundaryProxy{resolved: tool.ResolvedCall{
		ProxyAction:  "call",
		CapabilityID: "mcp-tool:db/query",
		TargetName:   target.Name(),
		Target:       target,
		ReadOnly:     true,
		Args:         json.RawMessage(`{}`),
	}})
	calls := []provider.ToolCall{
		{ID: "c1", Name: "use_capability", Arguments: `{"action":"call","capability_id":"mcp-tool:db/query"}`},
		{ID: "c2", Name: "use_capability", Arguments: `{"action":"call","capability_id":"mcp-tool:db/query"}`},
	}
	session := NewSession("sys")
	session.Add(provider.Message{Role: provider.RoleAssistant, ToolCalls: calls})
	var events []event.Event
	a := New(nil, reg, session, Options{}, event.FuncSink(func(e event.Event) {
		events = append(events, e)
	}))

	a.executeBatch(context.Background(), &a.turn, calls)

	var refreshed []string
	for _, e := range events {
		if e.Kind == event.ToolDispatch && e.Tool.Refreshed {
			refreshed = append(refreshed, e.Tool.ID)
		}
	}
	if strings.Join(refreshed, ",") != "c1,c2" {
		t.Fatalf("resolved refresh order = %v, want provider order", refreshed)
	}
}

func TestPlannerBlocksDestructiveMCPWithExecutorHandoff(t *testing.T) {
	calls := 0
	target := layeredReadOnlyMCPBoundaryTarget{
		readOnlyBoundaryTarget: readOnlyBoundaryTarget{name: "mcp__db__drop", readOnly: false, calls: &calls},
		destructive:            true,
		serverAuthorized:       true,
	}
	reg := tool.NewRegistry()
	reg.Add(readOnlyBoundaryProxy{resolved: tool.ResolvedCall{
		ProxyAction: "call", TargetName: target.Name(), Target: target, ReadOnly: false, Args: json.RawMessage(`{}`),
	}})
	planner := NewPlannerAgent(nil, reg, NewSession("sys"), Options{}, event.Discard)
	out := planner.executeOne(context.Background(), &planner.turn, provider.ToolCall{
		ID: "p1", Name: "use_capability", Arguments: `{"action":"call","capability_id":"mcp-tool:db/drop","arguments":{}}`,
	})
	if !out.blocked || calls != 0 {
		t.Fatalf("destructive planner outcome = %+v calls=%d", out, calls)
	}
	if !strings.Contains(out.output, "Executor") || !strings.Contains(out.output, "handoff") {
		t.Fatalf("destructive block should guide Executor handoff, got %q", out.output)
	}
	if !strings.Contains(out.output, "do not treat this as missing MCP configuration") {
		t.Fatalf("destructive block should discourage config interpretation: %q", out.output)
	}
}

func TestUseCapabilityDiscoveryCallsAreParallel(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "read_file", readOnly: true})
	reg.Add(NewUseCapabilityTool(t.Context(), nil, nil, reg, nil, nil, nil))
	calls := []provider.ToolCall{
		{ID: "1", Name: "use_capability", Arguments: `{"action":"list"}`},
		{ID: "2", Name: "use_capability", Arguments: `{"action":"list"}`},
		{ID: "3", Name: "read_file", Arguments: `{"path":"a.go"}`},
	}
	got := partitionToolCalls(reg, calls)
	if len(got) != 1 || !got[0].parallel || got[0].end != 3 {
		t.Fatalf("list/read-only discovery should batch: %+v", got)
	}
}

func TestPlannerToolRegistryExcludesDirectMCPKeepsProxy(t *testing.T) {
	parent := tool.NewRegistry()
	parent.Add(fakeTool{name: "read_file", readOnly: true})
	parent.Add(fakeTool{name: "write_file", readOnly: false})
	parent.Add(annotatedMCPTool{
		fakeTool:         fakeTool{name: "mcp__gh__search", readOnly: true},
		server:           "gh",
		raw:              "search",
		serverAuthorized: true,
	})
	parent.Add(fakeTool{name: "use_capability", readOnly: true})
	planner := PlannerToolRegistry(parent)
	names := strings.Join(planner.Names(), ",")
	if strings.Contains(names, "mcp__") {
		t.Fatalf("planner registry still has direct MCP: %s", names)
	}
	if _, ok := planner.Get("use_capability"); !ok {
		t.Fatal("planner registry missing use_capability")
	}
	if _, ok := planner.Get("write_file"); ok {
		t.Fatal("planner registry must not include writers")
	}
	if _, ok := planner.Get("read_file"); !ok {
		t.Fatal("planner registry missing read_file")
	}
}

func TestPlannerSchemaStableAcrossProxyPresence(t *testing.T) {
	// Building planner registry with or without pre-registered mcp tools must
	// not change the fixed use_capability schema bytes.
	parent := tool.NewRegistry()
	proxy := NewUseCapabilityTool(context.Background(), nil, nil, parent, nil, nil, nil)
	parent.Add(proxy)
	parent.Add(fakeTool{name: "read_file", readOnly: true})
	parent.Add(annotatedMCPTool{
		fakeTool: fakeTool{name: "mcp__s__t", readOnly: true},
		server:   "s", raw: "t", serverAuthorized: true,
	})
	reg1 := PlannerToolRegistry(parent)
	schema1, ok := reg1.Get("use_capability")
	if !ok {
		t.Fatal("missing use_capability")
	}
	bytes1 := string(schema1.Schema())

	// Add more MCP tools and rebuild — schema bytes must match.
	parent.Add(annotatedMCPTool{
		fakeTool: fakeTool{name: "mcp__s__t2", readOnly: true},
		server:   "s", raw: "t2", serverAuthorized: true,
	})
	reg2 := PlannerToolRegistry(parent)
	schema2, ok := reg2.Get("use_capability")
	if !ok {
		t.Fatal("missing use_capability after MCP add")
	}
	if string(schema2.Schema()) != bytes1 {
		t.Fatalf("use_capability schema changed after MCP add\nbefore=%s\nafter=%s", bytes1, schema2.Schema())
	}
	// Provider-visible tool order for planner must not include mcp__ and must
	// keep use_capability present with identical schema.
	for _, name := range reg2.Names() {
		if strings.HasPrefix(name, "mcp__") {
			t.Fatalf("reg2 still exposes %s", name)
		}
	}
}

func TestMCPCapabilityRuntimeTracksHotLifecycleAndSharedHostRevocation(t *testing.T) {
	t.Setenv("REASONIX_CACHE_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var oldCalls atomic.Int32
	oldServer := explicitReaderMCPServer(t, nil, &oldCalls)
	defer oldServer.Close()
	var newCalls atomic.Int32
	newServer := explicitReaderMCPServer(t, nil, &newCalls)
	defer newServer.Close()

	host := plugin.NewHost()
	defer host.Close()
	runtime := NewMCPCapabilityRuntime(ctx, host, nil, tool.NewRegistry(), nil)
	frontend := runtime.NewFrontend(nil, nil)
	entry := config.PluginEntry{Name: "hot", Type: "http", URL: oldServer.URL, Source: config.MCPSourceUserConfig}
	oldSpec := plugin.Spec{Name: "hot", Type: "http", URL: oldServer.URL, Authorized: true}

	// Hot add must appear without rebuilding the provider-visible frontend.
	runtime.UpsertServer(entry, oldSpec, true)
	listed, err := frontend.Execute(ctx, json.RawMessage(`{"action":"list"}`))
	if err != nil || !strings.Contains(listed, `"name": "hot"`) {
		t.Fatalf("hot-added list = %q, %v", listed, err)
	}
	call := json.RawMessage(`{"action":"call","capability_id":"mcp-tool:hot/search","arguments":{"q":"x"}}`)
	if _, err := frontend.Execute(ctx, call); err != nil {
		t.Fatalf("old endpoint call: %v", err)
	}
	if oldCalls.Load() != 1 {
		t.Fatalf("old endpoint tool calls = %d, want 1", oldCalls.Load())
	}

	// Updating the same stable server identity clears old live metadata and the
	// next disconnected call must use the replacement endpoint.
	host.Remove("hot")
	entry.URL = newServer.URL
	newSpec := plugin.Spec{Name: "hot", Type: "http", URL: newServer.URL, Authorized: true}
	runtime.UpsertServer(entry, newSpec, true)
	if runtime.ConnectedProxyTools() != nil {
		t.Fatal("endpoint update retained stale live tool snapshot")
	}
	if _, err := frontend.Execute(ctx, call); err != nil {
		t.Fatalf("new endpoint call: %v", err)
	}
	if newCalls.Load() != 1 || oldCalls.Load() != 1 {
		t.Fatalf("endpoint calls old=%d new=%d, want 1/1", oldCalls.Load(), newCalls.Load())
	}

	// Per-controller disable wins over a still-connected shared Host client.
	// No reconnect or tools/call may occur, and live routing state is revoked.
	if !host.HasClient("hot") {
		t.Fatal("test requires shared Host client to remain connected")
	}
	if !runtime.SetServerEnabled("hot", false) {
		t.Fatal("disable did not find hot server")
	}
	if runtime.ConnectedProxyTools() != nil {
		t.Fatal("disable retained live proxy tools")
	}
	blocked, err := frontend.Execute(ctx, call)
	if err != nil || !strings.Contains(blocked, "disabled") {
		t.Fatalf("disabled shared-Host call = %q, %v, want fail-closed", blocked, err)
	}
	if newCalls.Load() != 1 {
		t.Fatalf("disabled shared-Host server executed %d calls, want 1", newCalls.Load())
	}
	listed, err = frontend.Execute(ctx, json.RawMessage(`{"action":"list"}`))
	if err != nil || !strings.Contains(listed, `"status": "disabled"`) || !strings.Contains(listed, `"connected": false`) {
		t.Fatalf("disabled list = %q, %v", listed, err)
	}

	// Uninstall removes discovery and any possibility of a stale reconnect.
	if !runtime.RemoveServer("hot") {
		t.Fatal("remove did not find hot server")
	}
	listed, err = frontend.Execute(ctx, json.RawMessage(`{"action":"list"}`))
	if err != nil || strings.Contains(listed, `"name": "hot"`) {
		t.Fatalf("removed server leaked through list = %q, %v", listed, err)
	}
}

func TestSharedHostSameNameRequiresCurrentRuntimeAuthorizationAndIdentity(t *testing.T) {
	t.Setenv("REASONIX_CACHE_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, tc := range []struct {
		name       string
		authorized bool
		want       string
	}{
		{name: "unauthorized current identity", authorized: false, want: "not authorized"},
		{name: "different authorized identity", authorized: true, want: "identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var connectedCalls atomic.Int32
			connectedServer := explicitReaderMCPServer(t, nil, &connectedCalls)
			defer connectedServer.Close()
			var currentRequests atomic.Int32
			currentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				currentRequests.Add(1)
				http.Error(w, "must not connect", http.StatusForbidden)
			}))
			defer currentServer.Close()

			host := plugin.NewHost()
			defer host.Close()
			connectedSpec := plugin.Spec{Name: "shared", Type: "http", URL: connectedServer.URL, Authorized: true}
			if _, err := host.Add(ctx, connectedSpec); err != nil {
				t.Fatal(err)
			}
			currentSpec := plugin.Spec{Name: "shared", Type: "http", URL: currentServer.URL, Authorized: tc.authorized}
			runtime := NewMCPCapabilityRuntime(ctx, host, []plugin.Spec{currentSpec}, tool.NewRegistry(), nil)
			frontend := runtime.NewFrontend(capability.NewLedger(), nil)

			out, err := frontend.Execute(ctx, json.RawMessage(`{"action":"call","capability_id":"mcp-tool:shared/search","arguments":{"q":"x"}}`))
			detail := strings.ToLower(out + " " + fmt.Sprint(err))
			if !strings.Contains(detail, tc.want) {
				t.Fatalf("same-name shared Host call = %q, %v, want %q", out, err, tc.want)
			}
			if connectedCalls.Load() != 0 || currentRequests.Load() != 0 {
				t.Fatalf("identity mismatch reached network: connected tools/call=%d current requests=%d", connectedCalls.Load(), currentRequests.Load())
			}
		})
	}
}

func TestResolvedMCPCallRechecksRuntimeDisableBeforeDispatch(t *testing.T) {
	t.Setenv("REASONIX_CACHE_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var toolCalls atomic.Int32
	server := explicitReaderMCPServer(t, nil, &toolCalls)
	defer server.Close()
	spec := plugin.Spec{Name: "revoked", Type: "http", URL: server.URL, Authorized: true}
	host := plugin.NewHost()
	defer host.Close()
	if _, err := host.Add(ctx, spec); err != nil {
		t.Fatal(err)
	}
	runtime := NewMCPCapabilityRuntime(ctx, host, []plugin.Spec{spec}, tool.NewRegistry(), nil)
	frontend := runtime.NewFrontend(capability.NewLedger(), nil)
	resolved, err := frontend.ResolveCall(ctx, json.RawMessage(`{"action":"call","capability_id":"mcp-tool:revoked/search","arguments":{"q":"x"}}`))
	if err != nil || resolved.Target == nil {
		t.Fatalf("resolve = %+v, %v", resolved, err)
	}
	if !runtime.SetServerEnabled("revoked", false) {
		t.Fatal("disable did not find resolved server")
	}
	if _, err := resolved.Target.Execute(ctx, resolved.Args); err == nil || !strings.Contains(strings.ToLower(err.Error()), "disabled") {
		t.Fatalf("resolved target after disable error = %v", err)
	}
	if toolCalls.Load() != 0 {
		t.Fatalf("resolved target executed tools/call %d times after disable", toolCalls.Load())
	}
}

func TestRuntimeDisableLinearizesWithInFlightMCPDispatch(t *testing.T) {
	t.Setenv("REASONIX_CACHE_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	callStarted := make(chan struct{}, 1)
	releaseCall := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCall) }) }
	t.Cleanup(release)
	var toolCalls atomic.Int32
	server := blockingReaderMCPServer(t, callStarted, releaseCall, &toolCalls)
	defer server.Close()

	spec := plugin.Spec{Name: "linear", Type: "http", URL: server.URL, Authorized: true}
	host := plugin.NewHost()
	defer host.Close()
	if _, err := host.Add(ctx, spec); err != nil {
		t.Fatal(err)
	}
	runtime := NewMCPCapabilityRuntime(ctx, host, []plugin.Spec{spec}, tool.NewRegistry(), nil)
	frontend := runtime.NewFrontend(capability.NewLedger(), nil)
	resolved, err := frontend.ResolveCall(ctx, json.RawMessage(`{"action":"call","capability_id":"mcp-tool:linear/search","arguments":{}}`))
	if err != nil || resolved.Target == nil {
		t.Fatalf("resolve = %+v, %v", resolved, err)
	}

	executeDone := make(chan error, 1)
	go func() {
		_, err := resolved.Target.Execute(ctx, resolved.Args)
		executeDone <- err
	}()
	select {
	case <-callStarted:
	case <-ctx.Done():
		t.Fatalf("MCP call never reached dispatch: %v", ctx.Err())
	}

	disableDone := make(chan bool, 1)
	go func() { disableDone <- runtime.SetServerEnabled("linear", false) }()
	select {
	case <-disableDone:
		t.Fatal("disable completed before the in-flight MCP dispatch crossed its linearization boundary")
	case <-time.After(100 * time.Millisecond):
	}

	release()
	if err := <-executeDone; err != nil {
		t.Fatalf("in-flight MCP dispatch failed while disable waited: %v", err)
	}
	if ok := <-disableDone; !ok {
		t.Fatal("disable did not find the configured server")
	}
	if _, err := resolved.Target.Execute(ctx, resolved.Args); err == nil || !strings.Contains(strings.ToLower(err.Error()), "disabled") {
		t.Fatalf("post-disable resolved target error = %v", err)
	}
	if got := toolCalls.Load(); got != 1 {
		t.Fatalf("tools/call count = %d, want only the in-flight dispatch", got)
	}
}

func TestMCPCapabilityRuntimeConcurrentUpdatesAndSnapshots(t *testing.T) {
	t.Setenv("REASONIX_CACHE_HOME", t.TempDir())
	runtime := NewMCPCapabilityRuntime(context.Background(), plugin.NewHost(), nil, tool.NewRegistry(), nil)
	defer runtime.host.Close()
	frontend := runtime.NewFrontend(nil, nil)
	entry := config.PluginEntry{Name: "race", Type: "http", Source: config.MCPSourceUserConfig}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 100 {
			entry.URL = fmt.Sprintf("http://127.0.0.1:%d", 10000+i)
			runtime.UpsertServer(entry, plugin.Spec{Name: "race", Type: "http", URL: entry.URL, Authorized: true}, true)
			runtime.state.setLiveTools("race", []plugin.CachedTool{{Name: "query", ReadOnly: true}})
			runtime.SetServerEnabled("race", i%2 == 0)
			if i%10 == 0 {
				runtime.RemoveServer("race")
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 100 {
			_, _ = frontend.Execute(context.Background(), json.RawMessage(`{"action":"list"}`))
			_, _, _, _, _ = runtime.CapabilityCatalogState()
		}
	}()
	wg.Wait()
}

func TestUnauthorizedNonProjectMCPZeroProcessStart(t *testing.T) {
	// Spec.Authorized is the single truth: a host/session server with
	// RequireLaunchApproval=false but Authorized=false must not start.
	host := plugin.NewHost()
	defer host.Close()
	var started atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Add(1)
		http.Error(w, "should not connect", http.StatusForbidden)
	}))
	defer srv.Close()

	spec := plugin.Spec{
		Name: "untrusted", Type: "http", URL: srv.URL,
		// Explicitly unauthorized: boot/install must set Authorized=true for trust.
		Authorized: false, RequireLaunchApproval: false,
	}
	proxy := NewUseCapabilityTool(context.Background(), host, []plugin.Spec{spec}, tool.NewRegistry(), capability.NewLedger(), nil, nil)
	reg := tool.NewRegistry()
	reg.Add(proxy)
	a := New(nil, reg, NewSession("sys"), Options{}, event.Discard)

	// Tool call path
	out := a.executeOne(context.Background(), &a.turn, provider.ToolCall{
		ID: "u1", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-tool:untrusted/search","arguments":{}}`,
	})
	if !out.blocked && out.errMsg == "" {
		// May surface as error rather than blocked depending on resolve shape.
		if !strings.Contains(out.output, "not authorized") && !strings.Contains(out.errMsg, "not authorized") {
			t.Fatalf("unauthorized tool call outcome = %+v", out)
		}
	}
	if host.HasClient("untrusted") || started.Load() != 0 {
		t.Fatalf("unauthorized non-project MCP started process/network: connected=%v starts=%d", host.HasClient("untrusted"), started.Load())
	}

	// Lifecycle connect path
	out2 := a.executeOne(context.Background(), &a.turn, provider.ToolCall{
		ID: "u2", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-server:untrusted"}`,
	})
	if host.HasClient("untrusted") || started.Load() != 0 {
		t.Fatalf("unauthorized connect started process/network: outcome=%+v connected=%v starts=%d", out2, host.HasClient("untrusted"), started.Load())
	}
	if !out2.blocked && !strings.Contains(out2.output, "not authorized") && !strings.Contains(out2.errMsg, "not authorized") {
		t.Fatalf("unauthorized connect should refuse, got %+v", out2)
	}
}

func TestAuthorizedMCPConnectUsesExplicitDenyOnlyGate(t *testing.T) {
	// dontAsk/ask policy must not block first connect of an authorized server;
	// only ExplicitlyDenies should stop it.
	t.Setenv("REASONIX_CACHE_HOME", t.TempDir())
	var toolCalls atomic.Int32
	server := explicitReaderMCPServer(t, nil, &toolCalls)
	defer server.Close()

	manager := mcplaunch.NewManager(filepath.Join(t.TempDir(), mcplaunch.StateFilename), t.TempDir())
	spec := plugin.Spec{
		Name: "explicit-reader", Type: "http", URL: server.URL,
		LaunchManager: manager, ConfigSource: "workspace_config",
		Authorized: true,
	}
	cacheExplicitReaderSchema(t, spec)

	host := plugin.NewHost()
	defer host.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	proxy := NewUseCapabilityTool(ctx, host, []plugin.Spec{spec}, tool.NewRegistry(), capability.NewLedger(), nil, nil)
	reg := tool.NewRegistry()
	reg.Add(proxy)

	// Gate that would deny all ordinary checks (simulates dontAsk / ask without answer).
	denyOrdinary := denyAllGate{}
	a := New(nil, reg, NewSession("sys"), Options{Gate: denyOrdinary}, event.Discard)
	out := a.executeOne(ctx, &a.turn, provider.ToolCall{
		ID: "c1", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-server:explicit-reader"}`,
	})
	// denyAllGate does not implement ExplicitDenyGate — trusted MCP path skips Gate.Check.
	if out.blocked || out.errMsg != "" {
		t.Fatalf("authorized lifecycle connect must not use ordinary Gate.Check: %+v", out)
	}
	if !host.HasClient("explicit-reader") {
		t.Fatal("authorized connect should start the server under deny-all ordinary gate")
	}

	// Explicit deny on mcp_connect__ must still block a fresh unauthorized name.
	// Use a second server name with deny of its connect identity.
	spec2 := plugin.Spec{
		Name: "other-reader", Type: "http", URL: server.URL,
		LaunchManager: manager, ConfigSource: "workspace_config",
		Authorized: true,
	}
	cacheExplicitReaderSchema(t, spec2)
	proxy2 := NewUseCapabilityTool(ctx, host, []plugin.Spec{spec2}, tool.NewRegistry(), capability.NewLedger(), nil, nil)
	reg2 := tool.NewRegistry()
	reg2.Add(proxy2)
	denyConnect := permission.NewGate(permission.New("ask", nil, nil, []string{plugin.MCPConnectPermissionName("other-reader")}), nil)
	a2 := New(nil, reg2, NewSession("sys"), Options{Gate: denyConnect}, event.Discard)
	out2 := a2.executeOne(ctx, &a2.turn, provider.ToolCall{
		ID: "c2", Name: "use_capability",
		Arguments: `{"action":"call","capability_id":"mcp-server:other-reader"}`,
	})
	if !out2.blocked || host.HasClient("other-reader") {
		t.Fatalf("explicit connect deny must block: outcome=%+v connected=%v", out2, host.HasClient("other-reader"))
	}
}
