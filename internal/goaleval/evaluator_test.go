package goaleval

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

type scriptedProvider struct {
	turns     []string // one response per call, recycled
	reasoning string   // thinking-mode reasoning_content streamed before Text
	err       error    // stream-open error
	timeout   bool     // hang until ctx deadline
	usage     *provider.Usage
	calls     int
}

func (s *scriptedProvider) Name() string { return "scripted" }

func (s *scriptedProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	ch := make(chan provider.Chunk, 3)
	if s.timeout {
		<-ctx.Done()
		close(ch)
		return ch, nil
	}
	i := s.calls - 1
	if i >= len(s.turns) {
		i = len(s.turns) - 1
	}
	if s.reasoning != "" {
		ch <- provider.Chunk{Type: provider.ChunkReasoning, Text: s.reasoning}
	}
	ch <- provider.Chunk{Type: provider.ChunkText, Text: s.turns[i]}
	if s.usage != nil {
		ch <- provider.Chunk{Type: provider.ChunkUsage, Usage: s.usage}
	}
	close(ch)
	return ch, nil
}

func evaluate(t *testing.T, prov provider.Provider, evidence GoalEvidence) (Verdict, error) {
	t.Helper()
	s := NewSession(prov, nil)
	return s.Evaluate(context.Background(), evidence)
}

func TestEvaluateParsesVerdicts(t *testing.T) {
	for _, tc := range []struct {
		body    string
		outcome Outcome
	}{
		{`{"outcome":"complete","reason":"the goal is done"}`, OutcomeComplete},
		{`{"outcome":"continue","reason":"more work remains"}`, OutcomeContinue},
		{`{"outcome":"blocked","reason":"needs user input"}`, OutcomeBlocked},
		{`{"outcome":"uncertain","reason":"cannot judge"}`, OutcomeUncertain},
		// Fenced / prose-wrapped JSON is tolerated.
		{"```json\n{\"outcome\":\"complete\",\"reason\":\"done\"}\n```", OutcomeComplete},
		{"Here is my judgment: {\"outcome\":\"continue\",\"reason\":\"keep going\"}", OutcomeContinue},
	} {
		t.Run(tc.body, func(t *testing.T) {
			prov := &scriptedProvider{turns: []string{tc.body}}
			verdict, err := evaluate(t, prov, GoalEvidence{GoalContract: "fix the parser"})
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if verdict.Outcome != tc.outcome {
				t.Fatalf("outcome = %q, want %q", verdict.Outcome, tc.outcome)
			}
		})
	}
}

func TestEvaluateParsesReasoningOnlyVerdicts(t *testing.T) {
	// Thinking-mode providers (#9678) stream the substance into
	// reasoning_content and finish with an empty content block. The evaluator
	// must still produce a verdict instead of failing closed with
	// "empty goal evaluator response".
	prov := &scriptedProvider{
		turns:     []string{""},
		reasoning: "The goal asked to fix the parser. I considered {\"outcome\":\"continue\"} as an example format first.\nFinal judgment: {\"outcome\":\"complete\",\"reason\":\"the goal is done\"}",
	}
	verdict, err := evaluate(t, prov, GoalEvidence{GoalContract: "fix the parser"})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if verdict.Outcome != OutcomeComplete {
		t.Fatalf("outcome = %q, want %q", verdict.Outcome, OutcomeComplete)
	}
	if verdict.Reason != "the goal is done" {
		t.Fatalf("reason = %q, want %q", verdict.Reason, "the goal is done")
	}

	// Prose-only reasoning without any JSON still fails closed, but with the
	// parse error rather than "empty response".
	prov = &scriptedProvider{turns: []string{""}, reasoning: "I thought about it, but reached no conclusion."}
	if _, err := evaluate(t, prov, GoalEvidence{}); err == nil || strings.Contains(err.Error(), "empty goal evaluator response") {
		t.Fatalf("Evaluate() error = %v, want invalid-JSON fail-closed error", err)
	}
}

func TestEvaluateFailClosedOnBadResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"invalid json", "{not json"},
		{"missing outcome", `{"reason":"no outcome"}`},
		{"invalid outcome", `{"outcome":"maybe","reason":"x"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prov := &scriptedProvider{turns: []string{tc.body}}
			if _, err := evaluate(t, prov, GoalEvidence{}); err == nil {
				t.Fatalf("Evaluate() error = nil, want fail-closed error for %q", tc.body)
			}
		})
	}
}

func TestEvaluateFailsOnProviderErrors(t *testing.T) {
	prov := &scriptedProvider{err: errors.New("provider exploded")}
	if _, err := evaluate(t, prov, GoalEvidence{}); err == nil {
		t.Fatal("Evaluate() error = nil, want provider error")
	}
}

func TestEvaluateTimesOut(t *testing.T) {
	prov := &scriptedProvider{timeout: true}
	s := NewSession(prov, nil)
	s.timeout = 50 * time.Millisecond
	start := time.Now()
	_, err := s.Evaluate(context.Background(), GoalEvidence{})
	if err == nil {
		t.Fatal("Evaluate() error = nil, want timeout")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Evaluate() took %v, want bounded timeout", elapsed)
	}
}

func TestEvaluateOverlongOutputFailsClosed(t *testing.T) {
	prov := &scriptedProvider{turns: []string{strings.Repeat("x", MaxOutputBytes+1024)}}
	if _, err := evaluate(t, prov, GoalEvidence{}); err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("Evaluate() error = %v, want output cap error", err)
	}
}

func TestEvaluateEmitsGoalEvaluatorUsage(t *testing.T) {
	usage := &provider.Usage{TotalTokens: 123, PromptTokens: 100, CompletionTokens: 23}
	prov := &scriptedProvider{turns: []string{`{"outcome":"continue","reason":"keep going"}`}, usage: usage}
	var seen *event.Event
	sink := event.FuncSink(func(e event.Event) {
		if e.Kind == event.Usage {
			seen = &e
		}
	})
	s := NewSessionWithSink(prov, nil, "test/model", sink)
	if _, err := s.Evaluate(context.Background(), GoalEvidence{GoalContract: "x"}); err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if seen == nil || seen.UsageSource != event.UsageSourceGoalEvaluator || seen.Usage == nil || seen.Usage.TotalTokens != 123 {
		t.Fatalf("usage event = %+v, want goal-evaluator attribution with tokens", seen)
	}
}

func TestEvidenceIsBoundedAndUntrusted(t *testing.T) {
	// Oversized evidence fields must be clipped, not rejected — and the JSON
	// stays valid.
	prov := &scriptedProvider{turns: []string{`{"outcome":"complete","reason":"done"}`}}
	ev := GoalEvidence{
		GoalContract:   strings.Repeat("g", 10_000),
		AssistantFinal: strings.Repeat("a", 10_000),
		TodoSummary:    strings.Repeat("t", 10_000),
	}
	_, err := evaluate(t, prov, ev)
	if err != nil {
		t.Fatalf("Evaluate() error = %v, want clipped evidence accepted", err)
	}
	if prov.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", prov.calls)
	}
}
