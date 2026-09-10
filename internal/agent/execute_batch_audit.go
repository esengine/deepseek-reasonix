package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func (a *Agent) emitBatchToolResult(c provider.ToolCall, o toolOutcome, duration, started int64, parallel bool, batchStart time.Time) error {
	t, _, ambiguous := a.svc.tools.ResolveCall(c.Name)
	ok := t != nil && len(ambiguous) == 0
	readOnly := ok && t.ReadOnly()
	if c.ResolvedReadOnly != nil {
		readOnly = *c.ResolvedReadOnly
	}
	args := c.Arguments
	if c.Name == "todo_write" && o.errMsg == "" && !o.blocked {
		if canonical := canonicalTodoArgs(a.CanonicalTodoState()); canonical != "" {
			args = canonical
		}
	}
	tr := event.Tool{
		RunState:     outcomeRunState(o),
		ID:           c.ID,
		Name:         c.Name,
		Args:         args,
		ResolvedName: c.ResolvedName,
		CapabilityID: c.CapabilityID,
		Output:       o.output,
		Err:          o.errMsg,
		ReadOnly:     readOnly,
		Truncated:    o.truncated,
		DurationMs:   duration,
		Execution:    toEventShellExecution(o.execution, duration),
	}
	if o.diagnostic != nil {
		tr.Diagnostic, _ = json.Marshal(o.diagnostic)
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
	if o.executed && o.errMsg == "" && !o.blocked {
		a.retireWrittenSource(o.evidenceSource)
	}
	state := outcomeRunState(o)
	msg := provider.Message{Role: provider.RoleTool, Content: o.output, Images: o.images, VisionSummary: o.visionSummary, ToolCallID: call.ID, Name: call.Name, ToolRunState: state, ToolExecution: toProviderToolExecution(o.execution)}
	if o.diagnostic != nil {
		msg.ToolDiagnostic, _ = json.Marshal(o.diagnostic)
		if o.diagnostic.Code == tool.WriteTargetAbsent && a.task.ledger != nil {
			a.task.ledger.RecordTextObservation(evidence.TextObservation{Path: o.diagnostic.Path, Absent: true})
		}
	}
	if o.rawOutput != "" && o.rawOutput != o.output {
		msg.RawContent = o.rawOutput
	}
	if env, ok := a.finalizedReadEnvelope(ctx, call, o); ok {
		if env.HasMore {
			msg.ToolDiagnostic, _ = json.Marshal(tool.OperationDiagnostic{Code: tool.ReadPartial, Path: env.Source.CanonicalPath, OperationID: call.ID, ActualSnapshot: env.Source.Snapshot, RequiredRanges: env.DeliveredRanges, Recovery: "continue with the next window only if the task requires more coverage"})
		}
		if raw, err := json.Marshal(env); err == nil {
			msg.ReadResult = raw
		}
		a.observeReadShadow(env, o.readActiveMillis)
		a.rememberReadDelivery(call.ID, o.output, env)
		args, _ := parseReadFileArgs([]byte(call.Arguments))
		// Rollback may retain the rest for optional recovery, but ordinary
		// partial windows still provide exact evidence for visible local edits.
		if a.readPipelineActive() || (o.rawOutput != "" && !args.fullRead()) {
			if observer, ok := tReadObserver(a, call); ok {
				if observed, ok := observer.ObserveModelText(json.RawMessage(call.Arguments), o.output); ok {
					if len(env.DeliveredRanges) == 0 {
						observed.LineHashes = nil
					} else {
						count := env.DeliveredRanges[0].Lines()
						observed.LineHashes = observed.LineHashes[:min(count, len(observed.LineHashes))]
					}
					observed.Snapshot = env.Source.Snapshot
					a.recordModelTextObservation(observed, call.ID)
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
