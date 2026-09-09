package transcript

import (
	"fmt"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// HistoryOptions supplies surface-specific presentation without allowing a
// frontend to invent record identity or event coverage.
type HistoryOptions struct {
	UserContent     func(provider.Message) string
	SubmitContent   func(provider.Message) string
	ToolCall        func(provider.ToolCall) ToolCall
	LegacyTurns     []LegacyDisplayTurn
	CheckpointTurns map[int]int
}

// History converts the persisted message identities into display records.
// Derived notices are keyed by their owning message, never by their body.
func History(messages []provider.Message, opts HistoryOptions) []Message {
	out := make([]Message, 0, len(messages))
	byUser := legacyTurnsByUser(opts.LegacyTurns)
	canonicalMessages := canonicalMessageIDs(messages)
	suppressCanonical := false
	todoArgs := completedTodoArguments(messages)
	for messageIndex, m := range messages {
		if suppressCanonical && m.DecisionReceipt == nil && !independentLocalMessage(m) {
			if !agent.IsUserAuthoredTurnMessage(m) {
				continue
			}
			suppressCanonical = false
		}
		rows := historyRows(m, messageIndex, opts, todoArgs)
		stampHistoryRows(rows, m)
		out = append(out, rows...)
		if m.Role == provider.RoleUser && !m.LocalOnly && agent.IsUserAuthoredTurnMessage(m) {
			if appendLegacyTurnRows(&out, byUser, canonicalMessages, m) {
				suppressCanonical = true
			}
		}
	}
	return out
}

func canonicalMessageIDs(messages []provider.Message) map[string]bool {
	canonicalMessages := make(map[string]bool)
	for _, message := range messages {
		if message.ID != "" {
			canonicalMessages[message.ID] = true
		}
	}
	return canonicalMessages
}

func legacyTurnsByUser(turns []LegacyDisplayTurn) map[string][]LegacyDisplayTurn {
	byUser := make(map[string][]LegacyDisplayTurn)
	for _, turn := range turns {
		key := turn.UserMessageID
		if key == "" {
			key = "legacy:" + turn.UserHash
		}
		byUser[key] = append(byUser[key], turn)
	}
	return byUser
}

func independentLocalMessage(m provider.Message) bool {
	_, steer := agent.ReplaySteerText(m.Content)
	return m.LocalOnly && (len(m.ProtocolRecovery) > 0 || (m.FinalReadinessRecovery != nil && m.FinalReadinessRecovery.Pending) || steer)
}

func historyRows(m provider.Message, messageIndex int, opts HistoryOptions, todoArgs map[string]string) []Message {
	switch {
	case m.Role == provider.RoleSystem:
		return nil
	case m.DecisionReceipt != nil:
		return []Message{{Role: "notice", Code: event.NoticeCodeDecisionReceipt, Level: "info", DecisionReceipt: m.DecisionReceipt}}
	case m.LocalOnly && len(m.ProtocolRecovery) > 0:
		if recovery, ok := provider.DecodeProtocolRecovery(m.ProtocolRecovery); ok && recovery.State == "pending" {
			return []Message{{Role: "notice", Code: "protocol_recovery", Level: "info", Pending: true, ProtocolRecovery: &provider.ProtocolRecoveryAction{ID: recovery.ID}}}
		}
		return nil
	case m.LocalOnly && m.FinalReadinessRecovery != nil && m.FinalReadinessRecovery.Pending:
		return []Message{{Role: "notice", Code: event.NoticeCodeFinalReadiness, Level: "info", Pending: true,
			Content:   "Task is not complete; continue the remaining work or checks.",
			Readiness: &event.FinalReadiness{Attempts: 1, Missing: append([]string(nil), m.FinalReadinessRecovery.Missing...)}}}
	default:
		return defaultHistoryRows(m, messageIndex, opts, todoArgs)
	}
}

func defaultHistoryRows(m provider.Message, messageIndex int, opts HistoryOptions, todoArgs map[string]string) []Message {
	if text, handled := agent.ReplaySteerText(m.Content); handled {
		if text == "" {
			return nil
		}
		row := Message{Role: "notice", Content: "↪ " + text}
		if m.LocalOnly {
			row.Content, row.Code, row.Level = agent.UnappliedSteerNotice(text), event.NoticeCodeUnappliedSteer, "warn"
		}
		return []Message{row}
	}
	if m.Role == provider.RoleUser && agent.IsHostGeneratedUserMessage(m) {
		return nil
	}
	row := Message{MessageID: m.ID, Role: string(m.Role), Content: m.Content, CreatedAt: m.CreatedAt,
		WorkDurationMs: m.WorkDurationMs, MemoryCitations: m.MemoryCitations, Execution: m.ToolExecution}
	if m.LocalOnly {
		row.Role = "assistant"
	}
	if m.Role == provider.RoleUser {
		applyUserHistoryContent(&row, m, messageIndex, opts)
	}
	if row.Role == "assistant" {
		applyAssistantHistoryFields(&row, m, opts, todoArgs)
	}
	if m.Role == provider.RoleTool && !m.LocalOnly {
		row.ToolCallID, row.ToolName = m.ToolCallID, m.Name
		if toolFailed(row.Content) {
			row.ToolResultError = row.Content
		}
	}
	var rows []Message
	if !m.LocalOnly || strings.TrimSpace(row.Content+row.Reasoning) != "" || len(row.ToolCalls) > 0 {
		rows = append(rows, row)
	}
	for _, receipt := range m.DecisionReceipts {
		if receipt != nil {
			rows = append(rows, Message{Role: "notice", Code: event.NoticeCodeDecisionReceipt, Level: "info", DecisionReceipt: receipt})
		}
	}
	if m.LocalOnly && m.InterruptedTurn != nil {
		rows = append(rows, interruptedNotice(m.InterruptedTurn))
	}
	return rows
}

func applyUserHistoryContent(row *Message, m provider.Message, messageIndex int, opts HistoryOptions) {
	if turn, ok := opts.CheckpointTurns[messageIndex]; ok {
		row.CheckpointTurn = &turn
	}
	row.Content = agent.UserMessageText(m)
	if opts.UserContent != nil {
		row.Content = opts.UserContent(m)
	}
	row.Content = CollapseExpandedPaste(row.Content)
	if opts.SubmitContent != nil && row.Content != m.Content {
		replay := opts.SubmitContent(m)
		if replay != row.Content && (!agent.ContainsMemoryCompilerExecution(m.Content) || strings.HasPrefix(strings.TrimSpace(replay), "/")) {
			row.SubmitText = replay
		}
	}
}

func applyAssistantHistoryFields(row *Message, m provider.Message, opts HistoryOptions, todoArgs map[string]string) {
	row.Reasoning = m.ReasoningContent
	for _, call := range m.ToolCalls {
		converted := ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments, ResolvedName: call.ResolvedName,
			CapabilityID: call.CapabilityID, ResolvedReadOnly: call.ResolvedReadOnly, Diff: call.Diff, Added: call.Added, Removed: call.Removed}
		if opts.ToolCall != nil {
			converted = opts.ToolCall(call)
		}
		if args, ok := todoArgs[call.ID]; ok && call.Name == "todo_write" {
			converted.Arguments = args
		}
		row.ToolCalls = append(row.ToolCalls, converted)
	}
	for _, search := range m.ServerSearch {
		search.Raw = nil
		row.ServerSearch = append(row.ServerSearch, search)
	}
}

func stampHistoryRows(rows []Message, m provider.Message) {
	for i := range rows {
		row := &rows[i]
		switch {
		case row.Role == "tool" && row.ToolCallID != "":
			row.RecordID = "tool:" + row.ToolCallID
		case row.MessageID != "":
			row.RecordID = "m:" + row.MessageID
		default:
			row.RecordID = fmt.Sprintf("m:%s:notice:%d", m.ID, i)
		}
		if row.CreatedAt == 0 {
			row.CreatedAt = m.CreatedAt
		}
	}
}

// appendLegacyTurnRows reports whether the replayed turn carried a
// cancelled-turn notice, which suppresses its canonical successor messages.
func appendLegacyTurnRows(out *[]Message, byUser map[string][]LegacyDisplayTurn, canonicalMessages map[string]bool, m provider.Message) bool {
	key := m.ID
	if len(byUser[key]) == 0 {
		key = "legacy:" + LegacyDisplayKey(agent.UserMessageText(m))
	}
	turns := byUser[key]
	if len(turns) == 0 {
		return false
	}
	turn := turns[0]
	byUser[key] = turns[1:]
	cancelled := false
	for _, legacy := range turn.Messages {
		if legacy.Role == "notice" && legacy.Code == event.NoticeCodeCancelledTurn {
			cancelled = true
		}
	}
	sawCancelled := false
	for i, legacy := range turn.Messages {
		if !cancelled && legacy.MessageID != "" && canonicalMessages[legacy.MessageID] {
			continue
		}
		if legacy.RecordID == "" {
			if legacy.MessageID != "" {
				legacy.RecordID = "m:" + legacy.MessageID
			} else {
				legacy.RecordID = fmt.Sprintf("m:%s:legacy:%s:%d", m.ID, turn.TurnID, i)
			}
		}
		if legacy.Role == "notice" && legacy.Code == event.NoticeCodeCancelledTurn {
			sawCancelled = true
		}
		*out = append(*out, legacy)
	}
	return sawCancelled
}
