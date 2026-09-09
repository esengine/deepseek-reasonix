package event

// WithMessageIdentity associates an attempt's output with the message that
// will be committed to the local transcript. It never modifies provider data.
func WithMessageIdentity(inner Sink, messageID, attemptID string) Sink {
	return &messageIdentitySink{AuditForwarder: AuditForwarder{Inner: inner}, messageID: messageID, attemptID: attemptID}
}

type messageIdentitySink struct {
	AuditForwarder
	messageID, attemptID string
}

var _ OptionalSinkCapabilities = (*messageIdentitySink)(nil)
var _ CheckedSink = (*messageIdentitySink)(nil)

func (s *messageIdentitySink) stamp(e Event) Event {
	switch e.Kind {
	case Text, Reasoning, Message, ToolDispatch, ToolResult:
		if e.MessageID == "" {
			e.MessageID = s.messageID
		}
		if e.AttemptID == "" {
			e.AttemptID = s.attemptID
		}
	}
	return e
}

func (s *messageIdentitySink) Emit(e Event)              { s.Inner.Emit(s.stamp(e)) }
func (s *messageIdentitySink) EmitChecked(e Event) error { return EmitChecked(s.Inner, s.stamp(e)) }
