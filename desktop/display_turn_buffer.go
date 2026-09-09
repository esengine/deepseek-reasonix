package main

import (
	"reasonix/internal/event"
	"reasonix/internal/transcript"
	"reasonix/internal/turnevent"
)

type displayTurnBuffer struct{ transcript.Buffer }

func (b *displayTurnBuffer) reset() { b.Reset() }
func (b *displayTurnBuffer) materialize() []HistoryMessage {
	return transcript.LegacyDisplayMessages(b.Messages())
}
func (b *displayTurnBuffer) resultMessages() []HistoryMessage { return b.ResultMessages() }
func recordHistoryDisplayEvent(buffer *displayTurnBuffer, e event.Event) {
	if e.Kind == event.UserMessage {
		return
	}
	buffer.Format = transcript.Formatter{ToolSubject: historyToolSubject, ToolSummary: historyToolSummary, ToolResult: plannerToolResultDisplay}
	buffer.Apply(e)
}

func displayEventFromEnvelope(envelope turnevent.Envelope) (event.Event, bool) {
	return transcript.EventFromEnvelope(envelope)
}

func displayMessagesFromProjection(projection turnevent.PendingProjection) []HistoryMessage {
	return transcript.PendingDisplayMessages(projection, transcript.Formatter{ToolSubject: historyToolSubject, ToolSummary: historyToolSummary, ToolResult: plannerToolResultDisplay})
}
