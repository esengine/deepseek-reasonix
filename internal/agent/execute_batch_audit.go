package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

func (a *Agent) emitBatchToolResult(ctx context.Context, c provider.ToolCall, o toolOutcome, duration, started int64, parallel bool, batchStart time.Time) error {
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
	if err := event.EmitChecked(a.svc.sink, event.Event{Kind: event.ToolResult, MessageID: messageIdentity(ctx), Tool: tr}); err != nil {
		return err
	}
	if o.truncated && o.truncMsg != "" {
		a.svc.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: o.truncMsg})
	}
	a.recordToolExecutionAudit(readOnly, parallel, started, duration, batchStart, o)
	return nil
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

func (a *Agent) storeBatchToolResult(ctx context.Context, call provider.ToolCall, o toolOutcome) {
	state := outcomeRunState(o)
	msg := provider.Message{Role: provider.RoleTool, Content: o.output, Images: o.images, VisionSummary: o.visionSummary, ToolCallID: call.ID, Name: call.Name, ToolRunState: state, ToolExecution: toProviderToolExecution(o.execution)}
	if o.rawOutput != "" && o.rawOutput != o.output {
		msg.RawContent = o.rawOutput
	}
	if env, ok := a.finalizedReadEnvelope(ctx, call, o); ok {
		if raw, err := json.Marshal(env); err == nil {
			msg.ReadResult = raw
		}
		a.observeReadShadow(env, o.readActiveMillis)
		a.rememberReadDelivery(call.ID, o.output, env)
		if a.readPipelineActive() {
			if observer, ok := tReadObserver(a, call); ok {
				if observed, ok := observer.ObserveModelText(json.RawMessage(call.Arguments), o.output); ok {
					if len(env.DeliveredRanges) == 0 {
						observed.LineHashes = nil
					} else {
						count := env.DeliveredRanges[0].Lines()
						observed.LineHashes = observed.LineHashes[:min(count, len(observed.LineHashes))]
					}
					observed.Snapshot = env.Source.Snapshot
					a.recordModelTextObservationValue(observed)
				}
			}
		}
	} else if a.readPipelineActive() && (o.errMsg != "" || o.blocked) {
		a.observeFailedRead(call, o)
	}
	a.sess.conversation.Add(msg)
}

// Guard interventions revise only results that have not yet reached a model.
func (a *Agent) storeBatchGuardResults(calls []provider.ToolCall, results []string) {
	a.sess.conversation.updateBatchGuardResults(calls, results)
}
