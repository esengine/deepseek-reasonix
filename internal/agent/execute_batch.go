package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

// mutationBarrierCause is an immutable, argument-free description of the
// first durable-state write that failed or was blocked in a tool batch.
type mutationBarrierCause struct {
	callID                string
	toolName              string
	stateMutation         bool
	workspaceMutation     bool
	contentMutation       bool
	repositoryMutation    bool
	classificationKnown   bool
	reason, blockingPhase string
}

func (c *mutationBarrierCause) message() string {
	if c == nil {
		return "blocked: skipped because an earlier modification failed or was blocked in this tool batch. " +
			"Fix or re-run the failed change first; verification was not executed."
	}
	reason := c.reason
	if reason == "" {
		reason = "state mutation whose effects cannot be proven read-only"
	}
	action := "failed"
	if c.blockingPhase == "blocked" {
		action = "was blocked"
	}
	return "blocked: skipped because an earlier modification (" + reason + ") " + action + " in this tool batch. " +
		"Fix or re-run the failed change first; verification was not executed."
}

// toolOutcome is one tool call's result. output is the first-visible bounded
// form the model sees; rawOutput is the full original when truncation applied
// (empty when identical so we avoid double storage). images ride outside text.
type toolOutcome struct {
	runState                   provider.ToolRunState
	visionSummary              *provider.VisionSummary
	output                     string
	rawOutput                  string // full original when different from output
	images                     []string
	blocked                    bool
	errMsg                     string
	truncated                  bool
	truncMsg                   string
	resolved                   bool
	resolvedName               string
	capabilityID               string
	resolvedReadOnly, executed bool
	workspaceMutation          *event.WorkspaceMutation
	effective                  workspaceEffectiveCall
	// execution is local shell metadata (optional). Provider messages strip it
	// via ModelMessages; UI/event sinks surface it on ToolResult cards.
	execution *tool.ShellExecution
	// mcpApp is the optional MCP Apps presentation; provider-excluded like
	// execution, persisted for Desktop cards.
	mcpApp *provider.MCPAppPresentation
	// recoveryGeneration is the gate generation captured before execution so
	// ObserveResult can ignore stale results after a mode switch.
	recoveryGeneration uint64
	// recoveryStopTurn is set when Auto Episode budgets are exhausted.
	recoveryStopTurn   bool
	recoveryStopReason string
	readTaskID         string
	readEnvelope       *tool.ReadResultEnvelope
	finalReadEnvelope  *tool.ReadResultEnvelope
	readReference      *readDelivery
	readActiveMillis   int64
	incompleteRead     *incompleteReadDeferred
	subagentOutcome    *SubagentOutcome
}

// batchExecution is the result of one provider tool-call batch.
type batchExecution struct {
	results            []string
	outcomes           []toolOutcome
	images             [][]string
	executions         []*tool.ShellExecution
	err                error
	recoveryStopTurn   bool
	recoveryStopReason string
}

// executeBatch dispatches one model turn's tool calls. ToolDispatch events are
// emitted up front in call order; contiguous known ReadOnly calls fan out
// across goroutines while unknown and writer calls run serially so write/read
// ordering stays provider-ordered. Each completed serial call (or read-only
// group) is checkpointed before the next group starts.
func (a *Agent) executeBatch(ctx context.Context, turn *turnRuntime, calls []provider.ToolCall) batchExecution {
	// The assistant message already stored this slice in Session. Keep execution
	// state separate so refreshing a dependent preview never mutates shared
	// session memory outside Session's lock.
	calls = append([]provider.ToolCall(nil), calls...)
	if err := a.prepareToolBatch(ctx, calls); err != nil {
		return batchExecution{err: err}
	}
	if a.task.ledger != nil {
		ctx = withObservationBoundary(ctx, a.task.ledger.ObservationBoundary())
	}

	slots := newBatchSlots(calls)
	// Evidence is evaluated once for the whole batch, before anything runs: a
	// call whose writer cannot prove what it replaces never starts, and a read
	// from this same batch can never satisfy it.
	evidenceBlocked := a.preflightEvidenceBatch(ctx, calls)
	results, outcomes, durations, startedAt := slots.results, slots.outcomes, slots.durations, slots.startedAt
	ranParallel := make([]bool, len(calls))
	batchStart := time.Now()
	// Snapshot the receipt count before the batch runs: if a loop guard fires
	// for this batch, successes recorded during it (a mixed batch where only one
	// call was guard-blocked) must already count as progress against the pass.
	receiptMark := 0
	if a.task.ledger != nil {
		receiptMark = a.task.ledger.Len()
	}
	// Full dispatches used the batch's initial file state. After a writer runs
	// (even a failed one — disk may have mutated), refresh dependent writer
	// previews. The first writer stays on the single-preview fast path.
	earlierWriterRan := false
	surfaceWriters := slots.surfaceWriters
	var batchErr error
	var batchErrOnce sync.Once
	run := func(s *batchSlots, i int) {
		if pre, blocked := evidenceBlocked[i]; blocked {
			s.outcomes[i] = pre
			s.results[i] = pre.output
			return
		}
		t, _, ambiguous := a.svc.tools.ResolveCall(s.calls[i].Name)
		known := t != nil && len(ambiguous) == 0
		writer := known && !t.ReadOnly()
		s.surfaceWriters[i] = writer
		if earlierWriterRan && writer {
			if refreshed, changed := refreshCurrentFileDiff(ctx, t, s.calls[i]); changed {
				s.calls[i] = refreshed
				a.sess.conversation.UpdateToolCallPreview(refreshed)
				if err := a.emitFullToolDispatch(ctx, refreshed, true); err != nil {
					wrapped := fmt.Errorf("persist refreshed tool dispatch %s: %w", refreshed.ID, err)
					batchErrOnce.Do(func() { batchErr = wrapped })
					s.outcomes[i] = toolOutcome{output: "cancelled: tool dispatch was not durable", errMsg: wrapped.Error()}
					s.results[i] = s.outcomes[i].output
					return
				}
			}
		}
		start := time.Now()
		s.startedAt[i] = start.UnixMilli()
		// Claude Code-style interrupt: tools with InterruptBehaviorContinue
		// (bash) get a background-derived context so they keep running even
		// when the turn is cancelled by a user interrupt.
		toolCtx := ctx
		if t, _, _ := a.svc.tools.ResolveCall(s.calls[i].Name); t != nil {
			if ib, ok := t.(tool.Interruptible); ok && ib.ToolInterruptBehavior() == tool.InterruptBehaviorContinue {
				toolCtx = context.Background()
			}
		}
		s.outcomes[i] = a.executeOne(toolCtx, turn, s.calls[i])
		recordWorkspaceMutation(a.svc.sink, s.outcomes[i].workspaceMutation)
		if s.outcomes[i].executed {
			s.surfaceWriters[i] = s.outcomes[i].workspaceMutation != nil
		}
		if s.outcomes[i].resolved {
			readOnly := s.outcomes[i].resolvedReadOnly
			s.calls[i].ResolvedName = s.outcomes[i].resolvedName
			s.calls[i].CapabilityID = s.outcomes[i].capabilityID
			s.calls[i].ResolvedReadOnly = &readOnly
			s.surfaceWriters[i] = !readOnly
		}
		s.durations[i] = time.Since(start).Milliseconds()
		s.results[i] = s.outcomes[i].output
	}
	committed := make([]bool, len(calls))
	finalize := func(i int) {
		if committed[i] {
			return
		}
		committed[i] = true
		a.finalizeIncompleteReadOutcome(ctx, outcomes[i].incompleteRead, &outcomes[i])
		a.finalizeReadDelivery(ctx, calls[i], &outcomes[i])
		results[i] = outcomes[i].output
		a.commitBatchCallResolution(calls[i])
		a.storeBatchToolResult(ctx, calls[i], outcomes[i])
		if err := a.emitBatchToolResult(calls[i], outcomes[i], durations[i], startedAt[i], ranParallel[i], batchStart); err != nil {
			batchErrOnce.Do(func() { batchErr = fmt.Errorf("persist tool result %s: %w", calls[i].ID, err) })
		}
		if surfaceWriters[i] || (outcomes[i].resolved && !outcomes[i].resolvedReadOnly) {
			earlierWriterRan = true
		}
	}
	cancelled := false
	markCancelled := func(start int) {
		errMsg := context.Canceled.Error()
		if err := ctx.Err(); err != nil {
			errMsg = err.Error()
		}
		output := "cancelled: context cancelled before execution"
		for j := start; j < len(calls); j++ {
			results[j] = output
			outcomes[j] = toolOutcome{output: output, errMsg: errMsg}
		}
		cancelled = true
	}

	// recoveryBatchStop blocks remaining tools after Episode budgets are
	// exhausted so tool-call / result pairs stay complete for the provider.
	recoveryBatchStop := false
	recoveryStopReason := ""
	markRecoveryStopped := func(start int, reason string) {
		msg := "blocked: Auto recovery paused this turn; do not call more tools. Summarize completed work for the user."
		for j := start; j < len(calls); j++ {
			if results[j] != "" {
				continue
			}
			results[j] = msg
			outcomes[j] = toolOutcome{
				output:             msg,
				blocked:            true,
				errMsg:             firstLine(msg),
				recoveryStopTurn:   true,
				recoveryStopReason: reason,
			}
		}
		recoveryBatchStop = true
		if reason != "" {
			recoveryStopReason = reason
		}
	}

	// Deterministic dependency barrier: after a mutating call fails or is
	// blocked, later mutations/verifications in the batch are skipped; read-only
	// diagnosis still runs. executeOne re-checks after proxy resolution.
	mutationBatchStop := false
	a.mutationDependencyBarrier.Store(nil)
	markDependencySkipped := func(start int, cause *mutationBarrierCause) {
		a.markDependencySkipped(calls, outcomes, results, durations, start, cause)
		mutationBatchStop = true
	}

	for _, batch := range a.toolCallBatches(calls) {
		if ctx.Err() != nil || batchErr != nil {
			markCancelled(batch.start)
			break
		}
		if recoveryBatchStop {
			markRecoveryStopped(batch.start, recoveryStopReason)
			break
		}
		if batch.parallel && batch.end-batch.start > 1 {
			// Parallel segments are read-only by construction; no mutation barrier.
			private := slots.fork()
			ranUntil, finished := runParallel(ctx, batch.start, batch.end, func(i int) {
				a.stragglers.enter()
				defer a.stragglers.leave()
				run(private, i)
			})
			for i := batch.start; i < ranUntil; i++ {
				if finished[i] {
					slots.adopt(private, i)
				} else {
					slots.abandon(i)
				}
				ranParallel[i] = true
				finalize(i)
			}
			// After parallel execution completes, check if context was cancelled.
			// The individual tool executions should have detected ctx.Done(), but
			// we verify here to ensure we don't continue to subsequent batches.
			if ctx.Err() != nil {
				markCancelled(ranUntil)
				break
			}
			for i := batch.start; i < batch.end; i++ {
				if outcomes[i].recoveryStopTurn {
					recoveryBatchStop = true
					recoveryStopReason = outcomes[i].recoveryStopReason
					markRecoveryStopped(batch.end, recoveryStopReason)
					break
				}
			}
			if recoveryBatchStop {
				break
			}
			continue
		}
		for i := batch.start; i < batch.end; i++ {
			// Before executing the next tool, check if context was cancelled.
			// This prevents starting new tools when a previous tool's execution
			// triggered cancellation.
			if ctx.Err() != nil || batchErr != nil {
				// Claude Code-style interrupt: tools with InterruptBehaviorContinue
				// (bash) keep running in the background instead of being killed.
				if t, _, _ := a.svc.tools.ResolveCall(calls[i].Name); t != nil {
					if ib, ok := t.(tool.Interruptible); ok && ib.ToolInterruptBehavior() == tool.InterruptBehaviorContinue {
						go a.executeOne(context.Background(), turn, calls[i])
						outcomes[i] = toolOutcome{output: "interrupted: running in background"}
					}
				}
				markCancelled(i)
				break
			}
			if recoveryBatchStop {
				markRecoveryStopped(i, recoveryStopReason)
				break
			}
			if mutationBatchStop {
				// Fill dependency skips for remaining mutating/verify calls, then
				// allow any residual read-only diagnosis to run individually.
				if results[i] != "" {
					finalize(i)
					continue
				}
				if batchCallStaticallySkippable(a, calls[i]) {
					markDependencySkipped(i, nil)
					// markDependencySkipped fills this index; move on.
					if results[i] != "" {
						finalize(i)
						continue
					}
				}
			}
			if results[i] != "" {
				// Pre-filled dependency skip.
				finalize(i)
				continue
			}
			run(slots, i)
			finalize(i)
			if outcomes[i].recoveryStopTurn {
				recoveryBatchStop = true
				recoveryStopReason = outcomes[i].recoveryStopReason
				markRecoveryStopped(i+1, recoveryStopReason)
				break
			}
			// Mutation/verification failure barrier for the rest of this batch.
			if cause := batchCallMutationFailureCause(a, calls[i], outcomes[i]); cause != nil {
				mutationBatchStop = true
				markDependencySkipped(i+1, cause)
			}
			// After each tool execution, also check if the context was cancelled.
			// If so, stop executing remaining tools and return immediately so
			// the agent loop can detect the cancellation and exit.
			if ctx.Err() != nil {
				markCancelled(i + 1)
				break
			}
		}
		if cancelled || recoveryBatchStop {
			break
		}
	}

	for i := range calls {
		finalize(i)
	}
	a.applyBatchGuards(ctx, cancelled, calls, outcomes, results, receiptMark)
	a.storeBatchGuardResults(calls, results)
	images := make([][]string, len(calls))
	executions := make([]*tool.ShellExecution, len(calls))
	for i := range outcomes {
		images[i] = outcomes[i].images
		executions[i] = outcomes[i].execution
		if outcomes[i].recoveryStopTurn {
			recoveryBatchStop = true
			if outcomes[i].recoveryStopReason != "" {
				recoveryStopReason = outcomes[i].recoveryStopReason
			}
		}
	}
	return batchExecution{
		results:            results,
		outcomes:           outcomes,
		images:             images,
		executions:         executions,
		err:                batchErr,
		recoveryStopTurn:   recoveryBatchStop,
		recoveryStopReason: recoveryStopReason,
	}
}

func (a *Agent) commitBatchCallResolution(call provider.ToolCall) {
	if call.ResolvedReadOnly == nil {
		return
	}
	a.sess.conversation.UpdateToolCallResolution(call)
	a.emitResolvedToolDispatch(call)
}

// batchCallMutationFailureCause returns a sanitized effect description when a
// durable-state mutation failed or was blocked. Verification failures alone do
// not open the dependency barrier.
func batchCallMutationFailureCause(a *Agent, call provider.ToolCall, o toolOutcome) *mutationBarrierCause {
	if o.errMsg == "" && !o.blocked {
		return nil
	}
	readOnly := false
	toolName := call.Name
	toolArgs := json.RawMessage(call.Arguments)
	t, _, ambiguous := a.svc.tools.ResolveCall(call.Name)
	known := t != nil && len(ambiguous) == 0
	if known {
		readOnly = t.ReadOnly()
	}
	if call.ResolvedReadOnly != nil {
		readOnly = *call.ResolvedReadOnly
	}
	if o.resolved {
		readOnly = o.resolvedReadOnly
	}
	if o.effective.name != "" {
		toolName = o.effective.name
		toolArgs = o.effective.args
		readOnly = o.effective.readOnly
	}
	effects := evidence.ClassifyToolCall(toolName, toolArgs, readOnly)
	if toolName == "bash" && evidence.IsVerificationCommand(bashCommandFromArgs(toolArgs)) && !effects.StateMutation {
		return nil
	}
	if !effects.StateMutation {
		return nil
	}
	phase := "failed"
	if o.blocked {
		phase = "blocked"
	}
	return &mutationBarrierCause{
		callID:              call.ID,
		toolName:            toolName,
		stateMutation:       effects.StateMutation,
		workspaceMutation:   effects.WorkspaceMutation,
		contentMutation:     effects.ContentMutation,
		repositoryMutation:  effects.RepositoryMutation,
		classificationKnown: effects.Known && known,
		reason:              effects.Reason,
		blockingPhase:       phase,
	}
}

// batchCallStaticallySkippable reports whether a remaining call can be marked
// not_run/dependency without resolving a proxy. Proxies and unknown tools
// return false so executeOne can resolve the real target first.
func batchCallStaticallySkippable(a *Agent, call provider.ToolCall) bool {
	t, _, ambiguous := a.svc.tools.ResolveCall(call.Name)
	if t == nil || len(ambiguous) > 0 {
		// Unknown / ambiguous: fail closed via executeOne path.
		return false
	}
	// A proxy may resolve against a live capability whose result can change
	// between calls, so never resolve here just to pre-fill a skip: executeOne
	// resolves exactly once and classifies the real target before Commit.
	if _, ok := t.(tool.CallResolver); ok {
		return false
	}
	readOnly := t.ReadOnly()
	isVerification := call.Name == "bash" && evidence.IsVerificationCommand(bashCommandFromArgs(json.RawMessage(call.Arguments)))
	if isVerification {
		return true
	}
	return evidence.ClassifyToolCall(call.Name, json.RawMessage(call.Arguments), readOnly).StateMutation
}

type toolCallBatch struct {
	start    int
	end      int
	parallel bool
}

// toolCallBatches preserves read-only fan-out unless a tool hook can mutate the
// workspace. Such hooks are covered by a whole-workspace claim, so their calls
// must run in provider order instead of racing that claim against each other.
func (a *Agent) toolCallBatches(calls []provider.ToolCall) []toolCallBatch {
	batches := partitionToolCalls(a.svc.tools, calls)
	if !toolHooksMayMutateWorkspace(a.svc.hooks) {
		return batches
	}
	for i := range batches {
		batches[i].parallel = false
	}
	return batches
}

// partitionToolCalls keeps provider order while letting contiguous known
// read-only tools run together; unknown and writer tools are single-call
// serial batches. Evidence-ledger tools (complete_step, todo_write, wait,
// bash_output) never join a parallel run so provider order stays receipt
// order; use_capability is serial as it may resolve to a real MCP writer.
func partitionToolCalls(r *tool.Registry, calls []provider.ToolCall) []toolCallBatch {
	var batches []toolCallBatch
	for i := 0; i < len(calls); {
		if parallelisableCall(r, calls[i]) {
			start := i
			i++
			for i < len(calls) && parallelisableCall(r, calls[i]) {
				i++
			}
			batches = append(batches, toolCallBatch{start: start, end: i, parallel: true})
			continue
		}
		batches = append(batches, toolCallBatch{start: i, end: i + 1})
		i++
	}
	return batches
}

func parallelisableCall(r *tool.Registry, call provider.ToolCall) bool {
	switch call.Name {
	case "complete_step", "todo_write", "wait", "bash_output", "compress":
		return false
	}
	target, _, ambiguous := r.ResolveCall(call.Name)
	if target == nil || len(ambiguous) != 0 {
		return false
	}
	if classifier, ok := target.(tool.BatchClassifier); ok {
		class := classifier.ClassifyCall(json.RawMessage(call.Arguments))
		return class.Known && class.ReadOnly && class.ParallelSafe
	}
	if _, dynamic := target.(tool.CallResolver); dynamic {
		return false
	}
	return target.ReadOnly()
}

// parallelStragglerGrace bounds how long a cancelled parallel segment waits for
// tools that have not returned. Tool owners kill their own processes within
// their WaitDelay; past this the batch reports the effect as unknown instead
// of keeping the whole turn wedged behind one call that ignores its context.
var parallelStragglerGrace = 15 * time.Second

// runParallel returns the launched prefix and which of those calls finished.
// An unfinished index belongs to a straggler that still owns its private slot.
func runParallel(ctx context.Context, start, end int, run func(int)) (int, []bool) {
	const maxParallel = 8
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	completed := make(chan int, end-start)
	ranUntil := start
launch:
	for i := start; i < end; i++ {
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break launch
		}
		if ctx.Err() != nil {
			<-sem
			break
		}

		wg.Add(1)
		ranUntil = i + 1
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			run(i)
			completed <- i
		}()
	}
	allDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(allDone)
	}()
	select {
	case <-allDone:
	case <-ctx.Done():
		select {
		case <-allDone:
		case <-time.After(parallelStragglerGrace):
		}
	}
	finished := make([]bool, end)
	for {
		select {
		case i := <-completed:
			finished[i] = true
		default:
			return ranUntil, finished
		}
	}
}

// batchSlots is one batch's per-call execution state. Parallel segments run
// against a fork so a tool that outlives cancellation writes only into slots
// the batch has already stopped reading.
type batchSlots struct {
	calls          []provider.ToolCall
	outcomes       []toolOutcome
	results        []string
	durations      []int64
	startedAt      []int64
	surfaceWriters []bool
}

func newBatchSlots(calls []provider.ToolCall) *batchSlots {
	n := len(calls)
	return &batchSlots{
		calls: calls, outcomes: make([]toolOutcome, n), results: make([]string, n),
		durations: make([]int64, n), startedAt: make([]int64, n), surfaceWriters: make([]bool, n),
	}
}

func (s *batchSlots) fork() *batchSlots {
	return newBatchSlots(append([]provider.ToolCall(nil), s.calls...))
}

func (s *batchSlots) adopt(from *batchSlots, i int) {
	s.calls[i], s.outcomes[i], s.results[i] = from.calls[i], from.outcomes[i], from.results[i]
	s.durations[i], s.startedAt[i], s.surfaceWriters[i] = from.durations[i], from.startedAt[i], from.surfaceWriters[i]
}

const abandonedToolOutput = "interrupted: the tool did not stop after cancellation; its effect is unknown"

func (s *batchSlots) abandon(i int) {
	s.outcomes[i] = toolOutcome{output: abandonedToolOutput, errMsg: abandonedToolOutput, executed: true}
	s.results[i] = abandonedToolOutput
}
