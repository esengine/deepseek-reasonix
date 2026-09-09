package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"reasonix/internal/capability"
	"reasonix/internal/plugin"
	"reasonix/internal/tool"
)

func twoToolMCPServer(t *testing.T) *httptest.Server {
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
			result = map[string]any{"protocolVersion": "2024-11-05", "serverInfo": map[string]any{"name": "bfexplorer", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{
				{
					"name": "get_data", "description": "get data",
					"inputSchema": map[string]any{"type": "object"},
				},
				{
					"name": "execute_console_script", "description": "execute a console script",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"filePathName": map[string]any{"type": "string"}}},
				},
			}}
		case "tools/call":
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": *request.ID, "result": result})
	}))
}

// TestCatalogSurfacesLiveServerToolMissingFromRegistrySnapshot reproduces
// issue #9516: the session registry holds a stale subset of an MCP server's
// tools, the live server exposes one more, and the capability catalog must
// still surface the live tool so inspect on its mcp-tool id succeeds instead
// of returning "unknown capability_id".
func TestCatalogSurfacesLiveServerToolMissingFromRegistrySnapshot(t *testing.T) {
	t.Setenv("REASONIX_CACHE_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	server := twoToolMCPServer(t)
	defer server.Close()

	spec := plugin.Spec{Name: "bfexplorer", Type: "http", URL: server.URL, Authorized: true}
	host := plugin.NewHost()
	defer host.Close()

	// Stale registry snapshot: only get_data was known when it was registered.
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: plugin.ModelToolName("bfexplorer", "get_data"), readOnly: true})

	// Catalog assembly mirrors boot.Build: registry contract entries plus the
	// runtime's configured/cached/live MCP state.
	var runtime *MCPCapabilityRuntime
	catalogFn := func() capability.Catalog {
		conn := map[string]bool{}
		for _, name := range host.ServerNames() {
			conn[name] = true
		}
		opts := capability.CatalogOptions{Tools: reg.AllContractEntries(), Connected: conn}
		if runtime != nil {
			opts.Plugins, opts.CachedTools, opts.CacheKeyOK, opts.Disabled, opts.ProxyTools = runtime.CapabilityCatalogState()
		}
		return capability.BuildCatalog(opts)
	}
	runtime = NewMCPCapabilityRuntime(ctx, host, []plugin.Spec{spec}, reg, catalogFn)

	// The server connects after the runtime exists and never sends
	// notifications/tools/list_changed, so only the live tools/list — not the
	// registry — knows about execute_console_script.
	if _, err := host.Add(ctx, spec); err != nil {
		t.Fatal(err)
	}

	frontend := runtime.NewFrontend(capability.NewLedger(), nil)
	listing, err := frontend.Execute(ctx, json.RawMessage(`{"action":"call","capability_id":"mcp-server:bfexplorer"}`))
	if err != nil {
		t.Fatalf("connect-and-list on the ready server: %v", err)
	}
	if !strings.Contains(listing, "execute_console_script") {
		t.Fatalf("live server directory must list execute_console_script:\n%s", listing)
	}

	out, err := frontend.Execute(ctx, json.RawMessage(`{"action":"inspect","capability_id":"mcp-tool:bfexplorer/execute_console_script"}`))
	if err != nil {
		t.Fatalf("inspect of a tool the live connected server exposes must succeed, got: %v", err)
	}
	if !strings.Contains(out, "mcp-tool:bfexplorer/execute_console_script") {
		t.Fatalf("inspect payload lost the tool identity:\n%s", out)
	}
}
