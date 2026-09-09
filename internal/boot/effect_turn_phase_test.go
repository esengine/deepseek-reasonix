package boot

// Effect test for turn-phase accounting: the phase buckets are only meaningful
// if a real Build assembly bills them, so this asserts through the audit the
// CLI actually exports, not through a hand-built Audit.

import (
	"context"
	"sync"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// slowPhaseProvider stalls before answering so the provider span clears
// RecordPhaseMs's sub-millisecond floor by a wide margin.
type slowPhaseProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *slowPhaseProvider) Name() string { return "boot-phase-effect-test" }

func (p *slowPhaseProvider) Stream(_ context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	time.Sleep(10 * time.Millisecond)
	chunks := []provider.Chunk{
		{Type: provider.ChunkText, Text: "ok"},
		{Type: provider.ChunkDone},
	}
	ch := make(chan provider.Chunk, len(chunks))
	for _, chunk := range chunks {
		ch <- chunk
	}
	close(ch)
	return ch, nil
}

// TestEffectTurnPhaseBillsProviderWaitThroughRealBuild pins the boundary the
// run --metrics JSON reads. Before the phase clock closed at turn end this
// reported zero for every bucket no matter how long the provider took.
func TestEffectTurnPhaseBillsProviderWaitThroughRealBuild(t *testing.T) {
	isolateConfigHome(t)
	dir := robustTempDir(t)
	t.Chdir(dir)

	prov := &slowPhaseProvider{}
	provider.Register("phase-effect", func(provider.Config) (provider.Provider, error) {
		return prov, nil
	})
	writeFile(t, dir, "reasonix.toml", `
default_model = "test-model"

[agent]
system_prompt = "BASE"

[environment]
enabled = false

[[providers]]
name = "test-model"
kind = "phase-effect"
model = "x"
`)

	ctrl, err := Build(context.Background(), Options{Sink: event.Discard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()
	if err := ctrl.Run(context.Background(), "reply ok"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	exec := ctrl.Executor()
	if exec == nil {
		t.Fatal("no executor on the built controller")
	}
	audit := exec.CapabilityAudit()
	if audit == nil {
		t.Fatal("real Build left the capability audit unwired")
	}
	phases := audit.Snapshot().Phases
	if phases.ProviderWaitMs <= 0 {
		t.Fatalf("ProviderWaitMs = %d, want the provider span billed (%+v)", phases.ProviderWaitMs, phases)
	}
}
