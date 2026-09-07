package agent

import (
	"context"
	"strings"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

func (a *Agent) emitBatchToolResult(c provider.ToolCall, o toolOutcome, duration, started int64, parallel bool, batchStart time.Time) error {
	t, _, ambiguous := a.svc.tools.ResolveCall(c.Name)
	ok := t != nil && len(ambiguous) == 0
	readOnly := ok && t.ReadOnly()
	if c.ResolvedReadOnly != nil {
		readOnly = *c.ResolvedReadOnly
	}
	tr := event.Tool{
		ID:           c.ID,
		Name:         c.Name,
		Args:         c.Arguments,
		ResolvedName: c.ResolvedName,
		CapabilityID: c.CapabilityID,
		Output:       o.output,
		Err:          o.errMsg,
		ReadOnly:     readOnly,
		RunState:     toolOutcomeRunState(o),
		Truncated:    o.truncated,
		DurationMs:   duration,
		Execution:    toEventShellExecution(o.execution, duration),
	}
	if o.subagentOutcome != nil {
		tr.SubagentRef = o.subagentOutcome.Ref
		tr.SubagentStatus = string(o.subagentOutcome.Status)
		tr.SubagentErrorCode = o.subagentOutcome.ErrorCode
		tr.SubagentRetryable = o.subagentOutcome.Retryable
	} else if isSubagentToolCall(c) {
		if outcome, ok := ParseSubagentOutcome(o.output); ok {
			tr.SubagentRef = outcome.Ref
			tr.SubagentStatus = string(outcome.Status)
			tr.SubagentErrorCode = outcome.ErrorCode
			tr.SubagentRetryable = outcome.Retryable
		}
	}
	if started > 0 {
		tr.StartedAt = started
		tr.EndedAt = started + duration
		if mutation := o.workspaceMutation; mutation != nil {
			tr.WorkspaceMutation = true
			tr.WorkspacePaths = append([]string(nil), mutation.Paths...)
			tr.WorkspaceAllPaths = mutation.AllPaths
		}
	}
	if err := event.EmitChecked(a.svc.sink, event.Event{Kind: event.ToolResult, Tool: tr}); err != nil {
		return err
	}
	if o.truncated && o.truncMsg != "" {
		a.svc.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: o.truncMsg})
	}
	a.recordToolExecutionAudit(readOnly, parallel, started, duration, batchStart, o)
	return nil
}

func toolOutcomeRunState(o toolOutcome) string {
	return string(toolOutcomeState(o))
}

// toolOutcomeState is the single classifier behind the durable event and the
// stored tool message, so the two can never disagree. Only a pre-start
// cancellation or a refusal proves a call never ran; interruption evidence in
// the result text wins over the executed flag because a call that crossed the
// start barrier may already have side effects.
func toolOutcomeState(o toolOutcome) provider.ToolRunState {
	if o.cancelledBeforeExecution {
		return provider.ToolRunCancelled
	}
	if o.blocked && !o.executed {
		return provider.ToolRunNotStarted
	}
	if provider.ToolResultRunState(provider.Message{Content: o.output + "\n" + o.errMsg}) == provider.ToolRunUnknown {
		return provider.ToolRunUnknown
	}
	if !o.executed {
		return provider.ToolRunNotStarted
	}
	if o.errMsg != "" {
		return provider.ToolRunFailed
	}
	return provider.ToolRunCompleted
}

// interruptedOutcome shapes a call the batch could not finish. A call that
// never crossed the start barrier is provably not run; one that did may
// already have side effects, so its outcome must read as unknown.
func interruptedOutcome(started bool, ctxErr error) toolOutcome {
	errMsg := context.Canceled.Error()
	if ctxErr != nil {
		errMsg = ctxErr.Error()
	}
	if started {
		const output = "cancelled: context cancelled during execution; outcome unknown"
		return toolOutcome{output: output, errMsg: errMsg}
	}
	const output = "cancelled: context cancelled before execution"
	return toolOutcome{output: output, errMsg: errMsg, cancelledBeforeExecution: true}
}

func isSubagentToolName(name string) bool {
	switch name {
	case "task", "read_only_task", "run_skill", "read_only_skill", "explore", "research", "review", "security_review", "security-review", "parallel_tasks", "fleet":
		return true
	default:
		return false
	}
}

func isSubagentToolCall(call provider.ToolCall) bool {
	return isSubagentToolName(call.Name) || strings.HasPrefix(strings.TrimSpace(call.CapabilityID), "skill:")
}

func (a *Agent) recordToolExecutionAudit(readOnly, parallel bool, startedAt, durationMs int64, batchStart time.Time, o toolOutcome) {
	if a == nil || a.capabilityAudit == nil || startedAt <= 0 {
		return
	}
	queueMs := max(startedAt-batchStart.UnixMilli(), 0)
	rawBytes := len(o.output)
	if o.rawOutput != "" {
		rawBytes = len(o.rawOutput)
	}
	a.capabilityAudit.RecordToolExecution(readOnly, parallel, queueMs, durationMs, rawBytes, len(o.output))
}

func (a *Agent) storeBatchToolResult(call provider.ToolCall, o toolOutcome) {
	msg := provider.Message{Role: provider.RoleTool, Content: o.output, Images: o.images, ToolCallID: call.ID, Name: call.Name, ToolRunState: toolOutcomeState(o), ToolExecution: toProviderToolExecution(o.execution)}
	if o.rawOutput != "" && o.rawOutput != o.output {
		msg.RawContent = o.rawOutput
	}
	a.sess.conversation.Add(msg)
}

// Guard interventions revise only results that have not yet reached a model.
func (a *Agent) storeBatchGuardResults(calls []provider.ToolCall, results []string) {
	a.sess.conversation.updateBatchGuardResults(calls, results)
}
