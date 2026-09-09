package serve

import (
	"reasonix/internal/agent"
	"reasonix/internal/provider"
)

func historyMessageContent(message provider.Message) string {
	if message.Role == provider.RoleUser {
		return agent.UserMessageText(message)
	}
	return message.Content
}

type historyToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type historyMessage struct {
	MessageID        string                           `json:"messageId,omitempty"`
	ServerSearch     []provider.ServerSearchCall      `json:"serverSearch,omitempty"`
	ProtocolRecovery *provider.ProtocolRecoveryAction `json:"protocolRecovery,omitempty"`
	Role             string                           `json:"role"`
	Content          string                           `json:"content"`
	Missing          []string                         `json:"missing,omitempty"`
	Reasoning        string                           `json:"reasoning,omitempty"`
	ToolCalls        []historyToolCall                `json:"toolCalls,omitempty"`
	ToolCallID       string                           `json:"toolCallId,omitempty"`
	ToolName         string                           `json:"toolName,omitempty"`
}

func historyMessages(msgs []provider.Message) []historyMessage {
	out := make([]historyMessage, 0, len(msgs))
	for _, m := range historyWithoutPinnedContextRevisions(msgs) {
		if recovered, handled := finalReadinessHistoryMessage(m); handled {
			out = append(out, recovered...)
			continue
		}
		// Steer messages are surfaced as a notice, not a user message.
		if m.Role == provider.RoleUser {
			if text, handled := agent.ReplaySteerText(m.Content); handled {
				if text != "" {
					out = append(out, historyMessage{Role: "notice", Content: "↪ " + text})
				}
				continue
			}
		}
		hm := historyMessage{MessageID: m.ID, Role: string(m.Role), Content: historyMessageContent(m)}
		if m.Role == provider.RoleAssistant {
			hm.Reasoning = m.ReasoningContent
			for _, search := range m.ServerSearch {
				search.Raw = nil
				hm.ServerSearch = append(hm.ServerSearch, search)
			}
			if len(m.ToolCalls) > 0 {
				hm.ToolCalls = make([]historyToolCall, len(m.ToolCalls))
				for i, tc := range m.ToolCalls {
					hm.ToolCalls[i] = historyToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}
				}
			}
		}
		if m.Role == provider.RoleTool {
			hm.ToolCallID = m.ToolCallID
			hm.ToolName = m.Name
		}
		out = append(out, hm)
	}
	return out
}
