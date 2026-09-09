package transcript

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/i18n"
	"reasonix/internal/provider"
)

var pastedLabel = regexp.MustCompile(`^\[(?:已粘贴文本|已貼上文字|Pasted text) #[0-9]+ · [0-9]+ (?:行|lines)\]$`)

// CollapseExpandedPaste keeps the display card while SubmitText retains the
// original expanded payload used to reconstruct an edited prompt.
func CollapseExpandedPaste(content string) string {
	const prefix = "--- Begin "
	for scan := 0; scan < len(content); {
		begin := strings.Index(content[scan:], prefix)
		if begin < 0 {
			break
		}
		begin += scan
		start := begin + len(prefix)
		end := strings.Index(content[start:], " ---")
		if end < 0 {
			break
		}
		end += start
		label := content[start:end]
		body := end + len(" ---")
		scan = body
		if !pastedLabel.MatchString(label) {
			continue
		}
		marker := "--- End " + label + " ---"
		last := strings.Index(content[body:], marker)
		if last < 0 {
			continue
		}
		copy := strings.LastIndex(content[:begin], label)
		if copy < 0 || strings.TrimSpace(content[copy+len(label):begin]) != "" {
			continue
		}
		content = content[:copy+len(label)] + content[body+last+len(marker):]
		scan = copy + len(label)
	}
	return strings.TrimSpace(content)
}

func interruptedNotice(recovery *provider.InterruptedTurnRecovery) Message {
	if recovery != nil && recovery.TerminalStatus == "failed" {
		diagnostic := recovery.FailureDiagnostic
		text := "The provider request failed. Check the connection settings and try again."
		if diagnostic != nil {
			if status := i18n.M.ProviderStatusMessage(diagnostic.Status); status != "" {
				text = status
			} else if diagnostic.Status > 0 {
				text = fmt.Sprintf("Provider request failed (HTTP %d).", diagnostic.Status)
			}
			if label := provider.ProviderDisplayLabel(diagnostic.ProviderID, diagnostic.ProviderDisplayName, diagnostic.Protocol); label != "" {
				text = label + ": " + text
			}
		}
		return Message{Role: "notice", Level: "warn", Code: event.NoticeCodeProviderRequestFailed, Content: text, Detail: provider.FailureDiagnosticDetail(diagnostic), Diagnostic: diagnostic}
	}
	return Message{Role: "notice", Level: "info", Code: event.NoticeCodeCancelledTurn,
		Content: "This turn was interrupted. Partial output is kept for reference; only completed tool pairs and a bounded recovery summary enter the next model turn. Inspect the workspace before continuing or reverting changes."}
}

func toolFailed(content string) bool {
	content = strings.TrimSpace(content)
	return strings.HasPrefix(content, "error:") || strings.HasPrefix(content, "blocked:") || strings.HasPrefix(content, "Error:") || strings.HasPrefix(content, "[error")
}

func completedTodoArguments(messages []provider.Message) map[string]string {
	successful := make(map[string]bool)
	for _, message := range messages {
		if message.Role == provider.RoleTool && message.ToolCallID != "" && !toolFailed(message.Content) {
			successful[message.ToolCallID] = true
		}
	}
	out := make(map[string]string)
	var todos []evidence.TodoItem
	var latest string
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if call.ID == "" || !successful[call.ID] {
				continue
			}
			switch call.Name {
			case "todo_write":
				receipt := evidence.ReceiptFromToolCall(call.Name, json.RawMessage(call.Arguments), true, true)
				if len(receipt.Todos) == 0 {
					continue
				}
				todos, latest = evidence.NormalizeSerialTodos(receipt.Todos), call.ID
			case "complete_step":
				if latest == "" || len(todos) == 0 {
					continue
				}
				receipt := evidence.ReceiptFromToolCall(call.Name, json.RawMessage(call.Arguments), true, true)
				match, ok := evidence.MatchStep(receipt.Step, todos)
				if !ok || !evidence.AdvanceSerialTodo(todos, match.Index-1) {
					continue
				}
			default:
				continue
			}
			if encoded, err := json.Marshal(map[string]any{"todos": todos}); err == nil {
				out[latest] = string(encoded)
			}
		}
	}
	return out
}
