package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"reasonix/internal/netclient"
	"reasonix/internal/provider"
)

type failingRandomReader struct {
	err error
}

func (r failingRandomReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestSyntheticToolCallIDIsShortASCIIAndDistinctWithinResponse(t *testing.T) {
	namespace, fallback := readSyntheticToolCallNamespace(strings.NewReader("0123456789ab"), 1, 2, 3)
	if fallback {
		t.Fatal("complete entropy reader unexpectedly used the fallback")
	}
	first := syntheticToolCallID(namespace, 0)
	second := syntheticToolCallID(namespace, 1)
	if first == second {
		t.Fatalf("same-response synthetic ids collided: %q", first)
	}
	for _, id := range []string{first, second, syntheticToolCallID(namespace, int(^uint(0)>>1))} {
		if len(id) > 40 {
			t.Errorf("synthetic id is too long: len(%q) = %d", id, len(id))
		}
		for _, char := range id {
			if char < 0x21 || char > 0x7e {
				t.Errorf("synthetic id contains non-ASCII character %q: %q", char, id)
			}
		}
	}
}

func TestSyntheticToolCallNamespaceFallsBackWhenCryptoRandomnessFails(t *testing.T) {
	want := errors.New("entropy unavailable")
	first, fallback := readSyntheticToolCallNamespace(failingRandomReader{err: want}, 17, 1234, 1)
	if !fallback {
		t.Fatal("entropy failure did not use the documented fallback")
	}
	second, secondFallback := readSyntheticToolCallNamespace(failingRandomReader{err: want}, 17, 1234, 2)
	if !secondFallback {
		t.Fatal("second entropy failure did not use the documented fallback")
	}
	if first == "" || second == "" || first == second {
		t.Fatalf("fallback namespaces must be non-empty and sequence-scoped: %q, %q", first, second)
	}
}

// Two readStream calls model separate persisted assistant turns. Index-only
// fallbacks such as call_0 would collide across those turns.
func TestReadStreamScopesSyntheticToolCallIDsToEachResponse(t *testing.T) {
	stream := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"read_file","arguments":"{}"}}]}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	read := func() string {
		t.Helper()
		calls := readStreamToolCalls(t, stream)
		if len(calls) != 1 {
			t.Fatalf("readStream emitted %d completed tool calls, want 1", len(calls))
		}
		return calls[0].ID
	}

	first, second := read(), read()
	if first == "" || second == "" {
		t.Fatalf("synthetic ids must be non-empty: %q, %q", first, second)
	}
	if first == second {
		t.Fatalf("independent responses reused synthetic tool-call id %q", first)
	}
}

func TestStreamScopesSyntheticToolCallIDsAcrossHTTPResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"read_file","arguments":"{}"}}]}}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := New(provider.Config{
		Name: "local", BaseURL: srv.URL, Model: "test", APIKey: "test",
		Extra: map[string]any{"proxy_spec": netclient.ProxySpec{Mode: netclient.ModeOff}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	read := func() string {
		t.Helper()
		chunks, err := p.Stream(context.Background(), provider.Request{})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		var ids []string
		for chunk := range chunks {
			if chunk.Type == provider.ChunkToolCall && chunk.ToolCall != nil {
				ids = append(ids, chunk.ToolCall.ID)
			}
		}
		if len(ids) != 1 || ids[0] == "" {
			t.Fatalf("completed tool-call ids = %q, want one non-empty id", ids)
		}
		return ids[0]
	}

	first, second := read(), read()
	if first == second {
		t.Fatalf("independent HTTP responses reused synthetic tool-call id %q", first)
	}
}

func TestReadStreamKeepsNativeIDsAndSeparatesMissingIDsWithinResponse(t *testing.T) {
	const nativeID = "PrOvIdEr-ID_+/=:%"
	stream := `data: {"choices":[{"delta":{"tool_calls":[` +
		`{"index":0,"function":{"name":"first","arguments":"{}"}},` +
		`{"index":1,"id":"` + nativeID + `","function":{"name":"native","arguments":"{}"}},` +
		`{"index":2,"function":{"name":"third","arguments":"{}"}}]}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"

	calls := readStreamToolCalls(t, stream)
	if len(calls) != 3 {
		t.Fatalf("readStream emitted %d completed tool calls, want 3: %+v", len(calls), calls)
	}
	if calls[1].ID != nativeID {
		t.Errorf("native provider id changed byte-for-byte: got %q, want %q", calls[1].ID, nativeID)
	}
	if calls[0].ID == "" || calls[2].ID == "" || calls[0].ID == calls[2].ID {
		t.Errorf("same-response missing ids are not distinct: %q, %q", calls[0].ID, calls[2].ID)
	}
}

func readStreamToolCalls(t *testing.T, stream string) []provider.ToolCall {
	t.Helper()
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(stream))}
	out := make(chan provider.Chunk, 16)
	if _, err := (&client{name: "local"}).readStream(context.Background(), resp, out); err != nil {
		t.Fatalf("readStream: %v", err)
	}
	close(out)
	var calls []provider.ToolCall
	for chunk := range out {
		if chunk.Type == provider.ChunkToolCall && chunk.ToolCall != nil {
			calls = append(calls, *chunk.ToolCall)
		}
	}
	return calls
}
