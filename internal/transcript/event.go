package transcript

import (
	"reasonix/internal/event"
	"reasonix/internal/eventwire"
	"reasonix/internal/provider"
	"reasonix/internal/turnevent"
)

func EventFromEnvelope(envelope turnevent.Envelope) (event.Event, bool) {
	w := envelope.Event
	e := event.Event{
		MessageID: w.MessageID, AttemptID: w.AttemptID,
		TurnID: envelope.TurnID, Sequence: envelope.Sequence, Status: envelope.Status,
		Text: w.Text, Detail: w.Detail, Reasoning: w.Reasoning, ItemID: envelope.ItemID, Source: envelope.Source,
	}
	switch envelope.Kind {
	case "user_message":
		e.Kind = event.UserMessage
	case "stream_attempt":
		e.Kind = event.StreamAttempt
		if w.StreamAttempt != nil {
			e.StreamAttempt = event.StreamAttemptInfo{ID: w.StreamAttempt.ID, Action: event.StreamAttemptAction(w.StreamAttempt.Action), Attempt: w.StreamAttempt.Attempt, Max: w.StreamAttempt.Max, Reason: w.StreamAttempt.Reason}
		}
	case "phase":
		e.Kind = event.Phase
	case "reasoning":
		e.Kind = event.Reasoning
	case "text":
		e.Kind = event.Text
	case "message":
		e.Kind = event.Message
	case "tool_dispatch":
		e.Kind = event.ToolDispatch
	case "tool_result":
		e.Kind = event.ToolResult
	case "notice":
		e.Kind = event.Notice
	case "completion_summary":
		e.Kind = event.CompletionSummary
		if w.Completion != nil {
			c := w.Completion
			e.Completion = &event.CompletionSummaryInfo{Preset: c.Preset, Verdict: c.Verdict, Mutations: c.Mutations, ChecksPassed: c.ChecksPassed, ChecksFailed: c.ChecksFailed, ChecksSuppressed: c.ChecksSuppressed, Review: c.Review, GapKinds: c.GapKinds, ConstraintDegraded: c.ConstraintDegraded, Floor: c.Floor, Attention: c.Attention}
		}
	case "turn_done":
		e.Kind = event.TurnDone
		e.Receipt = eventwire.CompletionReceiptEvent(w.Receipt)
		e.CheckpointTurn = w.CheckpointTurn
	default:
		return event.Event{}, false
	}
	if w.Level == "warn" {
		e.Level = event.LevelWarn
	}
	e.Code = w.Code
	if w.Tool != nil {
		e.Tool = event.Tool{
			ID: w.Tool.ID, Name: w.Tool.Name, Args: w.Tool.Args, ResolvedName: w.Tool.ResolvedName,
			CapabilityID: w.Tool.CapabilityID, Output: w.Tool.Output, Err: w.Tool.Err,
			ReadOnly: w.Tool.ReadOnly, Truncated: w.Tool.Truncated, DurationMs: w.Tool.DurationMs,
			StartedAt: w.Tool.StartedAt, EndedAt: w.Tool.EndedAt, Partial: w.Tool.Partial,
			ArgChars: w.Tool.ArgChars, Refreshed: w.Tool.Refreshed, ParentID: w.Tool.ParentID,
			AttemptID: w.Tool.AttemptID, FileDiff: event.FileDiff{Diff: w.Tool.Diff, Added: w.Tool.Added, Removed: w.Tool.Removed},
			SubagentRef: w.Tool.SubagentRef, SubagentStatus: w.Tool.SubagentStatus,
			SubagentErrorCode: w.Tool.SubagentErrorCode, SubagentRetryable: w.Tool.SubagentRetryable,
		}
	}
	if len(w.MemoryCitations) > 0 {
		e.MemoryCitations = make([]provider.MemoryCitation, 0, len(w.MemoryCitations))
		for _, citation := range w.MemoryCitations {
			e.MemoryCitations = append(e.MemoryCitations, provider.MemoryCitation{
				ID: citation.ID, Source: citation.Source, LineStart: citation.LineStart,
				LineEnd: citation.LineEnd, Note: citation.Note, Kind: citation.Kind,
			})
		}
	}
	if w.DecisionReceipt != nil {
		e.DecisionReceipt = &provider.DecisionReceipt{
			ID: w.DecisionReceipt.ID, Kind: w.DecisionReceipt.Kind, Tool: w.DecisionReceipt.Tool,
			Subject: w.DecisionReceipt.Subject, Outcome: w.DecisionReceipt.Outcome,
		}
	}
	return e, true
}
