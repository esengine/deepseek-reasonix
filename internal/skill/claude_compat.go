package skill

import "strings"

// mapClaudeAgentTools rewrites Claude Code tool names (Read, Edit,
// AskUserQuestion, …) to their Reasonix equivalents so skills authored for
// Claude Code keep their allowed-tools grant; unmapped names pass through.
func mapClaudeAgentTools(in []string) []string {
	mapping := map[string]string{
		"read": "read_file", "write": "write_file", "edit": "edit_file",
		"bash": "bash", "grep": "grep", "glob": "glob", "ls": "ls",
		"webfetch": "web_fetch", "websearch": "web_search",
		"askuserquestion": "ask", "multiedit": "multi_edit",
		"todowrite": "todo_write", "notebookedit": "notebook_edit",
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, name := range in {
		mapped := strings.TrimSpace(name)
		if replacement := mapping[strings.ToLower(mapped)]; replacement != "" {
			mapped = replacement
		}
		if mapped != "" && !seen[mapped] {
			seen[mapped] = true
			out = append(out, mapped)
		}
	}
	return out
}

func isClaudeModelAlias(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "sonnet", "opus", "haiku", "inherit":
		return true
	default:
		return false
	}
}
