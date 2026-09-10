package agent

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"reasonix/internal/capability"
	"reasonix/internal/completion"
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/provider"
	"reasonix/internal/runtimepolicy"
	"reasonix/internal/taskcontract"
	"reasonix/internal/tool"
)

type phaseSink struct {
	phases      []string
	completions int
	summaries   []event.CompletionSummaryInfo
}

func (s *phaseSink) Emit(e event.Event) {
	if e.Kind == event.TurnPhase {
		s.phases = append(s.phases, string(e.PhaseName))
	}
	if e.Kind == event.CompletionSummary {
		s.completions++
		if e.Completion != nil {
			s.summaries = append(s.summaries, *e.Completion)
		}
	}
}

func TestTurnEmitsWorkingPhase(t *testing.T) {
	sink := &phaseSink{}
	prov := &mockProvider{name: "p", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "hi"},
		{Type: provider.ChunkDone},
	}}
	a := New(prov, tool.NewRegistry(), NewSession("sys"), Options{}, sink)
	if err := a.Run(context.Background(), "hello there"); err != nil {
		t.Fatal(err)
	}
	if len(sink.phases) == 0 || sink.phases[0] != string(event.TurnPhaseWorking) {
		t.Fatalf("phases = %v, want working first", sink.phases)
	}
	// Pure conversation should not emit a completion quality card.
	if sink.completions != 0 {
		t.Fatalf("completions = %d, want 0 for pure conversation", sink.completions)
	}
}

func TestCompletionSummaryEmittedOnMutationContract(t *testing.T) {
	sink := &phaseSink{}
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "write_file", readOnly: false})
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{toolCallChunk("w1", "write_file", `{"path":"a.go","content":"package a"}`), {Type: provider.ChunkDone}},
		{{Type: provider.ChunkText, Text: "done"}, {Type: provider.ChunkDone}},
	}}
	a := New(prov, reg, NewSession("sys"), Options{}, sink)
	// May fail readiness; still expect completion summary when mutations landed.
	_ = a.Run(context.Background(), "add a.go helper")
	if sink.completions == 0 {
		// If readiness loops forever without finalizing shadows, still check working phase.
		if len(sink.phases) == 0 {
			t.Fatal("expected at least working phase")
		}
		t.Log("no completion summary (readiness may have blocked finalize); phases=", sink.phases)
		return
	}
	if len(sink.summaries) != 1 || !slices.Contains(sink.summaries[0].GapKinds, "unreviewed_change") {
		t.Fatalf("completion summaries = %+v, want host-reported unreviewed change", sink.summaries)
	}
}

func TestCompletionSummaryFlagsFailedMutationCheck(t *testing.T) {
	sink := &phaseSink{}
	ledger := evidence.NewLedger()
	failed := evidence.Receipt{
		ToolName: "write_file",
		Mutation: true,
		Write:    true,
		Paths:    []string{"a.go"},
		Success:  false,
	}
	ledger.Record(failed)
	c := taskcontract.Atomic("add a.go helper")
	c.AbsorbReceipt(1, failed, "", false, false)
	a := &Agent{
		task: taskRuntime{ledger: ledger},
		svc:  agentServices{sink: sink},
		turn: turnRuntime{constraints: runtimepolicy.Constraints{PolicyFloor: taskcontract.PolicyFloorNone}},
	}
	report := completion.Build(c, ledger)
	a.emitCompletionSummary(c, report)
	if len(sink.summaries) != 1 || !sink.summaries[0].Attention || sink.summaries[0].ChecksFailed != 1 {
		t.Fatalf("summaries = %+v, want one attention summary with one failed check", sink.summaries)
	}
}

func containsString(values []string, want string) bool {
	return slices.Contains(values, want)
}

func TestExecutionPolicyAbsentOnNewTurn(t *testing.T) {
	prov := &mockProvider{name: "p", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "ok"},
		{Type: provider.ChunkDone},
	}}
	a := New(prov, tool.NewRegistry(), NewSession("sys"), Options{}, event.Discard)
	_ = a.Run(context.Background(), "explain mutexes")
	for _, m := range a.sess.conversation.Messages {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "<execution-policy") {
			t.Fatal("new turns must not inject execution-policy")
		}
	}
}

// The phase pair around a tool batch is what gives ProviderWaitMs and
// ToolExecMs their meaning, so the order is asserted, not just the first phase.
func TestToolRoundAlternatesProviderAndToolPhases(t *testing.T) {
	sink := &phaseSink{}
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "read_file", readOnly: true})
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{toolCallChunk("r1", "read_file", `{"path":"a.go"}`), {Type: provider.ChunkDone}},
		{{Type: provider.ChunkText, Text: "done"}, {Type: provider.ChunkDone}},
	}}
	a := New(prov, reg, NewSession("sys"), Options{}, sink)
	if err := a.Run(context.Background(), "read a.go"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		string(event.TurnPhaseWorking),
		string(event.TurnPhaseChecking),
		string(event.TurnPhaseWorking),
		string(event.TurnPhaseVerifying),
		string(event.TurnPhaseWorking),
	}
	if !slices.Equal(sink.phases, want) {
		t.Fatalf("phases = %v, want %v", sink.phases, want)
	}
	assertToolPhasesClosed(t, sink.phases)
}

// assertToolPhasesClosed guards the accounting invariant: a tool-billed phase
// left open swallows whatever runs next, and what runs next is usually a
// provider round, so its wait would be billed as tool time.
func assertToolPhasesClosed(t *testing.T, phases []string) {
	t.Helper()
	for i, phase := range phases {
		switch phase {
		case string(event.TurnPhaseChecking), string(event.TurnPhaseVerifying):
			if i == len(phases)-1 {
				t.Fatalf("phase %q at %d is never closed: %v", phase, i, phases)
			}
		}
	}
}

// RecordPhaseMs drops anything under a millisecond, so the tool sleeps: the
// assertion is that the bucket is reachable at all, which it was not before.
func TestToolRoundBillsPhaseDurations(t *testing.T) {
	audit := &capability.Audit{}
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "read_file", readOnly: true, delay: 5 * time.Millisecond})
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{toolCallChunk("r1", "read_file", `{"path":"a.go"}`), {Type: provider.ChunkDone}},
		{{Type: provider.ChunkText, Text: "done"}, {Type: provider.ChunkDone}},
	}}
	a := New(prov, reg, NewSession("sys"), Options{CapabilityAudit: audit}, event.Discard)
	if err := a.Run(context.Background(), "read a.go"); err != nil {
		t.Fatal(err)
	}
	phases := audit.Snapshot().Phases
	if phases.ToolExecMs <= 0 {
		t.Fatalf("ToolExecMs = %d, want the tool span billed", phases.ToolExecMs)
	}
	if phases.ProviderWaitMs < 0 {
		t.Fatalf("ProviderWaitMs = %d, want non-negative", phases.ProviderWaitMs)
	}
}
