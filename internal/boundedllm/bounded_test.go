package boundedllm

import (
	"context"
	"strings"
	"testing"

	"reasonix/internal/provider"
)

// scriptedProvider replays a fixed chunk stream for a single call, satisfying
// provider.Provider without any real network.
type scriptedProvider struct {
	chunks []provider.Chunk
}

func (p *scriptedProvider) Name() string { return "scripted" }
func (p *scriptedProvider) Stream(context.Context, provider.Request) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, len(p.chunks))
	for _, c := range p.chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}

func TestCallReturnsVisibleTextWhenPresent(t *testing.T) {
	got, err := Call(context.Background(), Config{Provider: &scriptedProvider{chunks: []provider.Chunk{
		{Type: provider.ChunkReasoning, Text: "some thinking"},
		{Type: provider.ChunkText, Text: "final verdict"},
		{Type: provider.ChunkUsage, Usage: &provider.Usage{TotalTokens: 9}},
	}}}, "sys", "e")
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if got != "final verdict" {
		t.Fatalf("Call = %q, want visible text only (reasoning must not leak)", got)
	}
}

// TestCallFallsBackToReasoningWhenContentEmpty covers #9678: a thinking-only
// model streams everything into reasoning with an empty content block. The
// reviewer must get that reasoning (callers extract a JSON verdict from it)
// instead of an empty response that fails the goal closed.
func TestCallFallsBackToReasoningWhenContentEmpty(t *testing.T) {
	got, err := Call(context.Background(), Config{Provider: &scriptedProvider{chunks: []provider.Chunk{
		{Type: provider.ChunkReasoning, Text: `{"outcome":"succeeded","summary":"done"}`},
		{Type: provider.ChunkUsage, Usage: &provider.Usage{TotalTokens: 12}},
	}}}, "sys", "e")
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if !strings.Contains(got, "succeeded") {
		t.Fatalf("reasoning-only Call = %q, want the reasoning text to stand in for content", got)
	}
}

func TestCallReturnsEmptyWhenNothingStreamed(t *testing.T) {
	got, err := Call(context.Background(), Config{Provider: &scriptedProvider{chunks: []provider.Chunk{
		{Type: provider.ChunkDone},
	}}}, "sys", "e")
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if got != "" {
		t.Fatalf("empty stream Call = %q, want empty (fail-closed upstream)", got)
	}
}

func TestCallSurfacesStreamErrorWithoutReasoningFallback(t *testing.T) {
	_, err := Call(context.Background(), Config{Provider: &scriptedProvider{chunks: []provider.Chunk{
		{Type: provider.ChunkReasoning, Text: "thought before failure"},
		{Type: provider.ChunkError, Err: &customErr{}},
	}}}, "sys", "e")
	if err == nil {
		t.Fatal("expected stream error to surface")
	}
	if _, ok := err.(*customErr); !ok {
		t.Fatalf("Call error = %T, want customErr (reasoning-before-error must not be returned)", err)
	}
}

type customErr struct{}

func (*customErr) Error() string { return "boom" }
