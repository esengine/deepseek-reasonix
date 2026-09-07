package eventwire

import "reasonix/internal/event"

// KindName returns the stable wire name of one event kind, or false for a
// kind outside the known set.
func KindName(kind event.Kind) (string, bool) {
	name, ok := kindNames[kind]
	return name, ok
}

var kindNames = map[event.Kind]string{
	event.TurnStarted:             "turn_started",
	event.Reasoning:               "reasoning",
	event.Text:                    "text",
	event.Message:                 "message",
	event.ToolDispatch:            "tool_dispatch",
	event.ToolResult:              "tool_result",
	event.Usage:                   "usage",
	event.Notice:                  "notice",
	event.Phase:                   "phase",
	event.ApprovalRequest:         "approval_request",
	event.AskRequest:              "ask_request",
	event.TurnDone:                "turn_done",
	event.CompactionStarted:       "compaction_started",
	event.CompactionDone:          "compaction_done",
	event.ToolProgress:            "tool_progress",
	event.ToolStarted:             "tool_started",
	event.MCPSurfaceReady:         "mcp_surface_ready",
	event.Retrying:                "retrying",
	event.Steer:                   "steer",
	event.GuardianAssessment:      "guardian_assessment",
	event.ExtensionSurface:        "extension_surface",
	event.ExtensionStatus:         "extension_status",
	event.StreamAttempt:           "stream_attempt",
	event.ContextMaintenanceEvent: "context_maintenance",
	event.WorkspaceChanged:        "workspace_changed",
	event.TurnPhase:               "turn_phase",
	event.CompletionSummary:       "completion_summary",
	event.ToolResultPreview:       "tool_result_preview",
	event.TurnStatusChanged:       "turn_status",
	event.MCPInteractionRequest:   "mcp_interaction",
	event.PromptAnswered:          "prompt_answered",
	event.SessionChanged:          "session_changed",
}
