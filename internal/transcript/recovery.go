package transcript

import (
	"strings"

	"reasonix/internal/event"
	"reasonix/internal/turnevent"
)

// PendingDisplayMessages is the legacy migration path. Modern recovery uses
// the whole identified event stream from a durable projection checkpoint.
func PendingDisplayMessages(projection turnevent.PendingProjection, format Formatter) []Message {
	planner, executor := Buffer{Format: format}, Buffer{Format: format}
	for _, envelope := range projection.Events {
		e, ok := EventFromEnvelope(envelope)
		if !ok {
			continue
		}
		buffer := &executor
		if strings.TrimSpace(e.Source) == event.UsageSourcePlanner {
			buffer = &planner
		}
		buffer.Apply(e)
	}
	out := LegacyDisplayMessages(planner.Messages())
	if projection.Status != event.TurnInterrupted {
		out = append(out, executor.ResultMessages()...)
	}
	if projection.Status == event.TurnInterrupted {
		out = append(out, LegacyDisplayMessages(executor.Messages())...)
		if len(out) > 0 {
			out = append(out, Message{Role: "notice", Level: "info", Code: event.NoticeCodeCancelledTurn,
				Content: "This turn was interrupted. Partial output is kept for reference; only completed tool pairs and a bounded recovery summary enter the next model turn. Inspect the workspace before continuing or reverting changes."})
		}
	}
	return out
}

// Legacy sidecars supplement provider history. User rows already have a
// canonical owner, and empty sampling placeholders are never persisted.
func LegacyDisplayMessages(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == "user" || (message.Role == "assistant" && strings.TrimSpace(message.Content+message.Reasoning) == "" && len(message.ToolCalls) == 0 && len(message.MemoryCitations) == 0) {
			continue
		}
		message.Pending = false
		for i := range message.ToolCalls {
			message.ToolCalls[i].Pending = false
		}
		out = append(out, message)
	}
	return out
}
