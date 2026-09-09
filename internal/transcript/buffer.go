package transcript

import (
	"fmt"
	"reasonix/internal/event"
	"reasonix/internal/eventwire"
	"reasonix/internal/provider"
	"slices"
	"strings"
)

// displayTextAccumulator retains provider chunks without repeatedly copying
// the complete prefix. A turn only materializes the final string when its
// display-only history is persisted; successful executor turns are discarded
// without ever joining their chunks.
type displayTextAccumulator struct {
	parts []string
	size  int
}

func (a *displayTextAccumulator) append(text string) {
	if text == "" {
		return
	}
	a.parts = append(a.parts, text)
	a.size += len(text)
}

func (a *displayTextAccumulator) replace(text string) {
	a.parts = nil
	a.size = 0
	a.append(text)
}

func (a *displayTextAccumulator) hasNonWhitespace() bool {
	for _, part := range a.parts {
		if strings.TrimSpace(part) != "" {
			return true
		}
	}
	return false
}

func (a *displayTextAccumulator) string() string {
	switch len(a.parts) {
	case 0:
		return ""
	case 1:
		return a.parts[0]
	}
	var out strings.Builder
	out.Grow(a.size)
	for _, part := range a.parts {
		out.WriteString(part)
	}
	return out.String()
}

type bufferedMessage struct {
	message   Message
	content   displayTextAccumulator
	reasoning displayTextAccumulator
}

func (m *bufferedMessage) materialize() Message {
	out := m.message
	if out.Role == "assistant" {
		out.Content = m.content.string()
		out.Reasoning = m.reasoning.string()
	}
	if len(out.MemoryCitations) > 0 {
		out.MemoryCitations = append([]provider.MemoryCitation(nil), out.MemoryCitations...)
	}
	if len(out.ToolCalls) > 0 {
		out.ToolCalls = append([]ToolCall(nil), out.ToolCalls...)
	}
	return out
}

type Buffer struct {
	Format      Formatter
	messages    []*bufferedMessage
	byMessageID map[string]*bufferedMessage
	tools       map[string]string
	completion  *eventwire.CompletionSummary
	userTurns   int
}

func (buffer *Buffer) Reset() {
	buffer.messages = nil
	buffer.byMessageID = nil
	buffer.tools = nil
	buffer.completion = nil
	buffer.userTurns = 0
}

func (buffer *Buffer) ResultMessages() []Message {
	var out []Message
	for _, m := range buffer.messages {
		if m.message.Code == "turn_result" {
			out = append(out, m.materialize())
		}
	}
	return out
}

func (buffer *Buffer) Messages() []Message {
	if len(buffer.messages) == 0 {
		return nil
	}
	out := make([]Message, 0, len(buffer.messages))
	for _, message := range buffer.messages {
		out = append(out, message.materialize())
	}
	return out
}

func (buffer *Buffer) Apply(e event.Event) {
	start := len(buffer.messages)
	defer buffer.stampAppliedMessages(e, start)
	switch e.Kind {
	case event.UserMessage:
		buffer.applyUserMessage(e)
	case event.StreamAttempt:
		buffer.applyStreamAttempt(e)
	case event.CompletionSummary:
		buffer.completion = eventwire.ToWire(e).Completion
	case event.TurnDone:
		buffer.applyTurnDone(e)
	case event.Phase:
		if strings.TrimSpace(e.Text) != "" {
			buffer.messages = append(buffer.messages, &bufferedMessage{message: Message{Role: "phase", Content: e.Text}})
		}
	case event.Reasoning:
		if e.Text != "" {
			ensureIdentifiedDisplayAssistant(buffer, e.MessageID, false).reasoning.append(e.Text)
		}
	case event.Text:
		if e.Text != "" {
			ensureIdentifiedDisplayAssistant(buffer, e.MessageID, false).content.append(e.Text)
		}
	case event.Message:
		buffer.applyAssistantMessage(e)
	case event.ToolDispatch:
		recordHistoryToolDispatch(buffer, e)
	case event.ToolResult:
		buffer.applyToolResult(e)
	case event.Notice:
		buffer.applyNotice(e)
	}
}

func (buffer *Buffer) stampAppliedMessages(e event.Event, start int) {
	for i := start; i < len(buffer.messages); i++ {
		m := &buffer.messages[i].message
		m.Source, m.TurnID = e.Source, e.TurnID
		if m.RecordID == "" {
			switch {
			case m.Role == "tool" && m.ToolCallID != "":
				m.RecordID = "tool:" + m.ToolCallID
			case m.MessageID != "":
				m.RecordID = "m:" + m.MessageID
			case e.Sequence > 0:
				m.RecordID = fmt.Sprintf("e:%s:%d:%d", e.TurnID, e.Sequence, i-start)
			}
		}
	}
}

func (buffer *Buffer) applyUserMessage(e event.Event) {
	if e.Source != "" && e.Source != event.UsageSourceExecutor {
		return
	}
	if e.MessageID != "" && buffer.byMessageID[e.MessageID] == nil {
		if buffer.byMessageID == nil {
			buffer.byMessageID = make(map[string]*bufferedMessage)
		}
		m := &bufferedMessage{message: Message{Role: "user", MessageID: e.MessageID, Content: e.Text}}
		buffer.userTurns++
		m.message.HistoryTurn = buffer.userTurns
		buffer.messages = append(buffer.messages, m)
		buffer.byMessageID[e.MessageID] = m
	}
}

func (buffer *Buffer) applyStreamAttempt(e event.Event) {
	if e.StreamAttempt.Action == event.StreamAttemptDiscard && e.MessageID != "" {
		kept := buffer.messages[:0]
		for _, message := range buffer.messages {
			if message.message.MessageID != e.MessageID {
				kept = append(kept, message)
			}
		}
		clear(buffer.messages[len(kept):])
		buffer.messages = kept
		delete(buffer.byMessageID, e.MessageID)
	}
	if e.MessageID != "" && e.StreamAttempt.Action == event.StreamAttemptBegin {
		m := ensureIdentifiedDisplayAssistant(buffer, e.MessageID, false)
		m.message.AttemptID, m.message.Pending = e.AttemptID, true
	}
	if e.MessageID != "" && e.StreamAttempt.Action == event.StreamAttemptCommit {
		if m := buffer.byMessageID[e.MessageID]; m != nil {
			m.message.Pending = false
		}
	}
}

func (buffer *Buffer) applyTurnDone(e event.Event) {
	for _, m := range buffer.messages {
		m.message.Pending = false
		if m.message.Role == "user" && m.message.TurnID == e.TurnID {
			m.message.CheckpointTurn = e.CheckpointTurn
		}
		for i := range m.message.ToolCalls {
			m.message.ToolCalls[i].Pending = false
		}
	}
	wire := eventwire.ToWire(e)
	if wire.Receipt != nil || buffer.completion != nil {
		buffer.messages = append(buffer.messages, &bufferedMessage{message: Message{
			Role: "notice", Code: "turn_result", Level: "info", TurnID: e.TurnID,
			CompletionReceipt: wire.Receipt, CompletionSummary: buffer.completion, CheckpointTurn: e.CheckpointTurn,
		}})
	}
}

func (buffer *Buffer) applyAssistantMessage(e event.Event) {
	if e.Text == "" && e.Reasoning == "" && len(e.MemoryCitations) == 0 {
		return
	}
	hm := ensureIdentifiedDisplayAssistant(buffer, e.MessageID, false)
	if e.Text != "" {
		hm.content.replace(e.Text)
	}
	if e.Reasoning != "" {
		hm.reasoning.replace(e.Reasoning)
	}
	if len(e.MemoryCitations) > 0 {
		hm.message.MemoryCitations = append([]provider.MemoryCitation(nil), e.MemoryCitations...)
	}
}

func (buffer *Buffer) applyToolResult(e event.Event) {
	callID := strings.TrimSpace(e.Tool.ID)
	content := firstNonEmpty(e.Tool.Output, e.Tool.Err)
	display, errPreview := buffer.Format.result(content, e.Tool.Err != "")
	if callID != "" {
		updateBufferedToolCallSummary(buffer, callID, content)
		for _, row := range buffer.messages {
			for i := range row.message.ToolCalls {
				if row.message.ToolCalls[i].ID == callID {
					row.message.ToolCalls[i].Pending = false
				}
			}
		}
	}
	toolName := e.Tool.Name
	if toolName == "" && buffer.tools != nil {
		toolName = buffer.tools[callID]
	}
	result := Message{
		Role:            "tool",
		ToolCallID:      callID,
		ToolName:        toolName,
		Content:         display,
		ToolResultError: errPreview,
	}
	if callID != "" {
		for _, row := range buffer.messages {
			if row.message.Role == "tool" && row.message.ToolCallID == callID {
				result.RecordID = row.message.RecordID
				result.Source, result.TurnID = row.message.Source, row.message.TurnID
				row.message = result
				return
			}
		}
	}
	buffer.messages = append(buffer.messages, &bufferedMessage{message: result})
}

func (buffer *Buffer) applyNotice(e event.Event) {
	if strings.TrimSpace(e.Text) == "" {
		return
	}
	level := "info"
	if e.Level == event.LevelWarn {
		level = "warn"
	}
	buffer.messages = append(buffer.messages, &bufferedMessage{message: Message{
		Role:            "notice",
		Level:           level,
		Content:         e.Text,
		Detail:          e.Detail,
		Code:            e.Code,
		DecisionReceipt: cloneDecisionReceipt(e.DecisionReceipt),
	}})
}

func recordHistoryToolDispatch(buffer *Buffer, e event.Event) {
	if strings.TrimSpace(e.Tool.Name) == "" && e.Tool.ID == "" {
		return
	}
	hm := ensureIdentifiedDisplayAssistant(buffer, e.MessageID, true)
	resolvedReadOnly := e.Tool.ReadOnly
	call := ToolCall{
		Partial:          e.Tool.Partial,
		ArgChars:         e.Tool.ArgChars,
		Pending:          true,
		ParentID:         e.Tool.ParentID,
		StartedAt:        e.Tool.StartedAt,
		ID:               e.Tool.ID,
		Name:             e.Tool.Name,
		Arguments:        e.Tool.Args,
		ResolvedName:     e.Tool.ResolvedName,
		CapabilityID:     e.Tool.CapabilityID,
		ResolvedReadOnly: &resolvedReadOnly,
		Subject:          buffer.Format.subject(e.Tool.Name, e.Tool.Args),
		Summary:          buffer.Format.summary(e.Tool.Name, e.Tool.Args, ""),
		Diff:             e.Tool.Diff,
		Added:            e.Tool.Added,
		Removed:          e.Tool.Removed,
	}
	replaced := false
	if call.ID != "" {
		for i := range hm.message.ToolCalls {
			if hm.message.ToolCalls[i].ID == call.ID {
				if call.Partial {
					previous := hm.message.ToolCalls[i]
					// A progress update cannot reopen a committed invocation.
					if !previous.Partial {
						return
					}
					if call.Name == "" {
						call.Name = previous.Name
					}
					if call.Arguments == "" {
						call.Arguments = previous.Arguments
					}
				}
				hm.message.ToolCalls[i] = call
				replaced = true
				break
			}
		}
		if buffer.tools == nil {
			buffer.tools = map[string]string{}
		}
		buffer.tools[call.ID] = call.Name
	}
	if !replaced {
		hm.message.ToolCalls = append(hm.message.ToolCalls, call)
	}
}

func ensureIdentifiedDisplayAssistant(buffer *Buffer, messageID string, tool bool) *bufferedMessage {
	if messageID == "" {
		if tool {
			return ensureDisplayAssistantForTool(buffer)
		}
		return ensureDisplayAssistant(buffer)
	}
	if existing := buffer.byMessageID[messageID]; existing != nil {
		return existing
	}
	if buffer.byMessageID == nil {
		buffer.byMessageID = make(map[string]*bufferedMessage)
	}
	message := &bufferedMessage{message: Message{MessageID: messageID, Role: "assistant"}}
	buffer.messages = append(buffer.messages, message)
	buffer.byMessageID[messageID] = message
	return message
}
func ensureDisplayAssistant(buffer *Buffer) *bufferedMessage {
	if n := len(buffer.messages); n > 0 && buffer.messages[n-1].message.Role == "assistant" {
		return buffer.messages[n-1]
	}
	message := &bufferedMessage{message: Message{Role: "assistant"}}
	buffer.messages = append(buffer.messages, message)
	return message
}

func ensureDisplayAssistantForTool(buffer *Buffer) *bufferedMessage {
	if n := len(buffer.messages); n > 0 && buffer.messages[n-1].message.Role == "assistant" && !buffer.messages[n-1].content.hasNonWhitespace() {
		return buffer.messages[n-1]
	}
	message := &bufferedMessage{message: Message{Role: "assistant"}}
	buffer.messages = append(buffer.messages, message)
	return message
}

func updateBufferedToolCallSummary(buffer *Buffer, callID, output string) {
	if callID == "" {
		return
	}
	for _, v := range slices.Backward(buffer.messages) {
		for j := range v.message.ToolCalls {
			call := &v.message.ToolCalls[j]
			if call.ID != callID {
				continue
			}
			if call.Summary == "" {
				call.Summary = buffer.Format.summary(call.Name, call.Arguments, output)
			}
			return
		}
	}
}

type Formatter struct {
	ToolSubject func(name, args string) string
	ToolSummary func(name, args, output string) string
	ToolResult  func(content string, failed bool) (string, string)
}

func (f Formatter) subject(name, args string) string {
	if f.ToolSubject != nil {
		return f.ToolSubject(name, args)
	}
	return ""
}
func (f Formatter) summary(name, args, output string) string {
	if f.ToolSummary != nil {
		return f.ToolSummary(name, args, output)
	}
	return ""
}
func (f Formatter) result(content string, failed bool) (string, string) {
	if f.ToolResult != nil {
		return f.ToolResult(content, failed)
	}
	if failed {
		return content, content
	}
	return content, ""
}
func (buffer *Buffer) ResetToolsIfEmpty() {
	if len(buffer.messages) == 0 {
		buffer.tools = nil
	}
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
func cloneDecisionReceipt(in *provider.DecisionReceipt) *provider.DecisionReceipt {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
