package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"reasonix/internal/agent/testutil"
	"reasonix/internal/event"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// cancelWhileRunningTool cancels the batch from inside its own execution: the
// shape of a host interrupt landing after the start barrier.
type cancelWhileRunningTool struct{ cancel context.CancelFunc }

func (cancelWhileRunningTool) Name() string        { return "cancel_writer" }
func (cancelWhileRunningTool) Description() string { return "writer interrupted mid-run" }
func (cancelWhileRunningTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (cancelWhileRunningTool) ReadOnly() bool { return false }
func (t cancelWhileRunningTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	t.cancel()
	<-ctx.Done()
	return "", ctx.Err()
}

// unknownOutcomeTool reports a write whose effect could not be confirmed
// without failing, the way write_file reports an interrupted rename.
type unknownOutcomeTool struct{}

func (unknownOutcomeTool) Name() string        { return "unknown_writer" }
func (unknownOutcomeTool) Description() string { return "writer with unconfirmed effect" }
func (unknownOutcomeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (unknownOutcomeTool) ReadOnly() bool { return false }
func (unknownOutcomeTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "write outcome unknown: rename interrupted", nil
}

type countingTool struct {
	name     string
	readOnly bool
	runs     *atomic.Int32
}

func (t countingTool) Name() string      { return t.name }
func (countingTool) Description() string { return "counts executions" }
func (countingTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t countingTool) ReadOnly() bool { return t.readOnly }
func (t countingTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.runs.Add(1)
	return "ran", nil
}

func TestInterruptedOutcomeDistinguishesStartedCalls(t *testing.T) {
	notStarted := interruptedOutcome(false, context.Canceled)
	if toolOutcomeState(notStarted) != provider.ToolRunCancelled {
		t.Fatalf("not-started call state = %s", toolOutcomeState(notStarted))
	}
	started := interruptedOutcome(true, context.Canceled)
	if toolOutcomeState(started) != provider.ToolRunUnknown {
		t.Fatalf("started call state = %s", toolOutcomeState(started))
	}
	if provider.ToolResultRunState(provider.Message{Content: started.output}) != provider.ToolRunUnknown {
		t.Fatal("readers without a stored run state must still see the started call as unknown")
	}
}

func TestToolOutcomeStateClassification(t *testing.T) {
	cases := []struct {
		name string
		o    toolOutcome
		want provider.ToolRunState
	}{
		{"completed", toolOutcome{executed: true, output: "ok"}, provider.ToolRunCompleted},
		{"failed", toolOutcome{executed: true, output: "error: boom", errMsg: "boom"}, provider.ToolRunFailed},
		{"blocked before run", toolOutcome{blocked: true, output: "blocked: denied", errMsg: "denied"}, provider.ToolRunNotStarted},
		{"truncated arguments", toolOutcome{output: "error: tool was not executed because the model output reached its length limit", errMsg: "truncated tool arguments"}, provider.ToolRunNotStarted},
		{"cancelled during run", toolOutcome{executed: true, output: "error: context canceled", errMsg: "context canceled"}, provider.ToolRunUnknown},
		{"unconfirmed write", toolOutcome{executed: true, output: "write outcome unknown: rename interrupted"}, provider.ToolRunUnknown},
		{"blocked after run keeps evidence", toolOutcome{blocked: true, executed: true, output: "blocked: extension rejected result", errMsg: "rejected"}, provider.ToolRunFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolOutcomeState(tc.o); got != tc.want {
				t.Fatalf("state = %s, want %s", got, tc.want)
			}
			if toolOutcomeRunState(tc.o) != string(tc.want) {
				t.Fatal("event and stored-message classifiers diverged")
			}
		})
	}
}

func TestInterruptedBatchNeverReportsStartedWriterAsNotStarted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var laterRuns atomic.Int32
	reg := tool.NewRegistry()
	reg.Add(cancelWhileRunningTool{cancel: cancel})
	reg.Add(countingTool{name: "later_writer", runs: &laterRuns})
	sink := &recordSink{}
	a := New(testutil.NewMock("m"), reg, NewSession("system"), Options{}, sink)
	calls := []provider.ToolCall{
		{ID: "first", Name: "cancel_writer", Arguments: `{}`},
		{ID: "second", Name: "later_writer", Arguments: `{}`},
	}
	a.executeBatch(ctx, &a.turn, calls)

	states := map[string]provider.ToolRunState{}
	for _, m := range a.Session().Snapshot() {
		if m.Role == provider.RoleTool {
			states[m.ToolCallID] = m.ToolRunState
		}
	}
	if states["first"] != provider.ToolRunUnknown {
		t.Fatalf("interrupted writer stored as %q, want unknown", states["first"])
	}
	if states["second"] != provider.ToolRunCancelled {
		t.Fatalf("never-started writer stored as %q, want cancelled", states["second"])
	}
	if laterRuns.Load() != 0 {
		t.Fatal("writer after the interrupt still executed")
	}
	started := sink.kinds(event.ToolStarted)
	if len(started) != 1 || started[0].Tool.ID != "first" {
		t.Fatalf("start barrier events = %+v", started)
	}
	results := sink.kinds(event.ToolResult)
	if len(results) != 2 {
		t.Fatalf("tool result events = %d", len(results))
	}
	for _, e := range results {
		if e.Tool.RunState != string(states[e.Tool.ID]) {
			t.Fatalf("ledger state %q disagrees with stored state %q for %s", e.Tool.RunState, states[e.Tool.ID], e.Tool.ID)
		}
	}
}

func TestUnknownWriteStopsLaterWritersButNotDiagnosis(t *testing.T) {
	var writerRuns, readerRuns atomic.Int32
	reg := tool.NewRegistry()
	reg.Add(unknownOutcomeTool{})
	reg.Add(countingTool{name: "later_writer", runs: &writerRuns})
	reg.Add(countingTool{name: "later_reader", readOnly: true, runs: &readerRuns})
	a := New(testutil.NewMock("m"), reg, NewSession("system"), Options{}, event.Discard)
	calls := []provider.ToolCall{
		{ID: "u", Name: "unknown_writer", Arguments: `{}`},
		{ID: "w", Name: "later_writer", Arguments: `{}`},
		{ID: "r", Name: "later_reader", Arguments: `{}`},
	}
	batch := a.executeBatch(context.Background(), &a.turn, calls)
	if toolOutcomeState(batch.outcomes[0]) != provider.ToolRunUnknown {
		t.Fatalf("unconfirmed write state = %s", toolOutcomeState(batch.outcomes[0]))
	}
	if writerRuns.Load() != 0 {
		t.Fatal("writer ran after an unknown side effect")
	}
	if readerRuns.Load() != 1 {
		t.Fatal("read-only diagnosis was blocked")
	}
	if !strings.Contains(batch.results[1], "unknown outcome") {
		t.Fatalf("skipped writer result = %q", batch.results[1])
	}
	if toolOutcomeState(batch.outcomes[1]) != provider.ToolRunNotStarted {
		t.Fatalf("skipped writer state = %s", toolOutcomeState(batch.outcomes[1]))
	}
}
