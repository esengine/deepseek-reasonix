// Package transcript owns the display projection shared by local and remote sessions.
// Its records are never used to construct provider requests.
package transcript

import (
	"reasonix/internal/event"
	"reasonix/internal/eventwire"
	"reasonix/internal/provider"
)

type Message struct {
	RecordID           string                       `json:"recordId,omitempty"`
	AttemptID          string                       `json:"attemptId,omitempty"`
	SubmissionID       string                       `json:"submissionId,omitempty"`
	Source             string                       `json:"source,omitempty"`
	MessageID          string                       `json:"messageId,omitempty"`
	CompletionReceipt  *eventwire.CompletionReceipt `json:"completionReceipt,omitempty"`
	CompletionSummary  *eventwire.CompletionSummary `json:"completionSummary,omitempty"`
	TurnID             string                       `json:"turnId,omitempty"`
	Role               string                       `json:"role"`
	Content            string                       `json:"content"`
	Detail             string                       `json:"detail,omitempty"`
	Code               string                       `json:"code,omitempty"`
	SubmitText         string                       `json:"submitText,omitempty"`
	CheckpointTurn     *int                         `json:"checkpointTurn,omitempty"`
	HistoryTurn        int                          `json:"historyTurn,omitempty"`
	CreatedAt          int64                        `json:"createdAt,omitempty"`
	Reasoning          string                       `json:"reasoning,omitempty"`
	MemoryCitations    []provider.MemoryCitation    `json:"memoryCitations,omitempty"`
	WorkDurationMs     int64                        `json:"workDurationMs,omitempty"`
	Level              string                       `json:"level,omitempty"`
	ToolCalls          []ToolCall                   `json:"toolCalls,omitempty"`
	ToolCallID         string                       `json:"toolCallId,omitempty"`
	ToolName           string                       `json:"toolName,omitempty"`
	ToolResultArchived bool                         `json:"toolResultArchived,omitempty"`
	ToolResultError    string                       `json:"toolResultError,omitempty"`
	// Execution is local shell metadata restored onto ToolCards after history
	// reload. Omitted when absent so older frontends ignore it safely.
	Execution        *provider.ToolExecution          `json:"execution,omitempty"`
	Pending          bool                             `json:"pending,omitempty"`
	Trigger          string                           `json:"trigger,omitempty"`
	Messages         int                              `json:"messages,omitempty"`
	Summary          string                           `json:"summary,omitempty"`
	Archive          string                           `json:"archive,omitempty"`
	DecisionReceipt  *provider.DecisionReceipt        `json:"decisionReceipt,omitempty"`
	Readiness        *event.FinalReadiness            `json:"readiness,omitempty"`
	ReadPause        *provider.ReadPause              `json:"readPause,omitempty"`
	ProtocolRecovery *provider.ProtocolRecoveryAction `json:"protocolRecovery,omitempty"`
	Diagnostic       *provider.FailureDiagnostic      `json:"diagnostic,omitempty"`
	ServerSearch     []provider.ServerSearchCall      `json:"serverSearch,omitempty"`
}

type ToolCall struct {
	Partial           bool   `json:"partial,omitempty"`
	ArgChars          int    `json:"argChars,omitempty"`
	Pending           bool   `json:"pending,omitempty"`
	ParentID          string `json:"parentId,omitempty"`
	StartedAt         int64  `json:"startedAt,omitempty"`
	ID                string `json:"id"`
	Name              string `json:"name"`
	Arguments         string `json:"arguments"`
	ResolvedName      string `json:"resolvedName,omitempty"`
	CapabilityID      string `json:"capabilityId,omitempty"`
	ResolvedReadOnly  *bool  `json:"resolvedReadOnly,omitempty"`
	Subject           string `json:"subject,omitempty"`
	Summary           string `json:"summary,omitempty"`
	Diff              string `json:"diff,omitempty"`
	Added             int    `json:"added,omitempty"`
	Removed           int    `json:"removed,omitempty"`
	ArgumentsArchived bool   `json:"argumentsArchived,omitempty"`
}
