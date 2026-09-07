// Package eventwire defines the shared frontend JSON contract for event.Event.
package eventwire

import (
	"encoding/json"

	"reasonix/internal/billing"
	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// Event is the JSON-friendly form shared by event frontends.
// externalizable:"true" marks large string payloads the Remote protocol may
// offload via content refs without changing provider-visible semantics.
type Event struct {
	Kind             string                           `json:"kind"`
	PromptID         string                           `json:"promptId,omitempty"`
	PromptKind       string                           `json:"promptKind,omitempty"`
	PromptLegacy     bool                             `json:"promptLegacy,omitempty"`
	TurnID           string                           `json:"turnId,omitempty"`
	Sequence         uint64                           `json:"seq,omitempty"`
	Status           string                           `json:"status,omitempty"`
	Text             string                           `json:"text,omitempty" externalizable:"true"`
	Detail           string                           `json:"detail,omitempty" externalizable:"true"`
	Code             string                           `json:"code,omitempty"`
	Reasoning        string                           `json:"reasoning,omitempty" externalizable:"true"`
	MemoryCitations  []MemoryCitation                 `json:"memoryCitations,omitempty"`
	Level            string                           `json:"level,omitempty"`
	Tool             *Tool                            `json:"tool,omitempty"`
	Usage            *Usage                           `json:"usage,omitempty"`
	Approval         *Approval                        `json:"approval,omitempty"`
	Ask              *Ask                             `json:"ask,omitempty"`
	MCPInteraction   *MCPInteraction                  `json:"mcpInteraction,omitempty"`
	Compaction       *Compaction                      `json:"compaction,omitempty"`
	Maintenance      *ContextMaintenance              `json:"maintenance,omitempty"`
	Guardian         *Guardian                        `json:"guardian,omitempty"`
	DecisionReceipt  *DecisionReceipt                 `json:"decisionReceipt,omitempty"`
	Extension        *ExtensionSurface                `json:"extension,omitempty"`
	Err              string                           `json:"err,omitempty" externalizable:"true"`
	Outcome          string                           `json:"outcome,omitempty"`
	Readiness        *FinalReadiness                  `json:"readiness,omitempty"`
	ProtocolRecovery *provider.ProtocolRecoveryAction `json:"protocolRecovery,omitempty"`
	Diagnostic       *provider.FailureDiagnostic      `json:"diagnostic,omitempty"`
	Receipt          *CompletionReceipt               `json:"receipt,omitempty"`
	CheckpointTurn   *int                             `json:"checkpointTurn,omitempty"`
	Recovery         *event.RecoveryStatus            `json:"recovery,omitempty"`
	RetryAttempt     int                              `json:"retryAttempt,omitempty"`
	RetryMax         int                              `json:"retryMax,omitempty"`
	RetryScope       string                           `json:"retryScope,omitempty"` // "headers" | "stream" | "protocol"; omit for older clients
	StreamAttempt    *StreamAttempt                   `json:"streamAttempt,omitempty"`
	// ItemID correlates Steer / TurnDone / unapplied-steer with a durable
	// session-inbox entry. Empty for legacy text-only guidance.
	ItemID string `json:"itemId,omitempty"`
	// SessionPath routes frames emitted by detached Serve controllers. Older
	// clients ignore the omitted/unknown field and keep single-session behavior.
	SessionPath string `json:"sessionPath,omitempty"`
	// SessionCurrent is set by Serve at publication time for frames belonging
	// to its foreground controller. It lets all-session clients adopt an
	// externally selected/recovered foreground without polling on every token.
	SessionCurrent bool `json:"sessionCurrent,omitempty"`
	// SessionReset distinguishes a fresh /new or /clear target from a resumed
	// durable session when a different client rotates the foreground.
	SessionReset bool              `json:"sessionReset,omitempty"`
	Workspace    *WorkspaceChanged `json:"workspace,omitempty"`
	// Phase is set on turn_phase events: working | checking | verifying | reviewing.
	Phase string `json:"phase,omitempty"`
	// Completion is set on completion_summary events (content-free quality summary).
	Completion *CompletionSummary `json:"completion,omitempty"`
}

// CompletionSummary is the JSON form of event.CompletionSummaryInfo.
type CompletionSummary struct {
	Preset             string   `json:"preset"` // deprecated; pinned compat value
	Verdict            string   `json:"verdict"`
	Mutations          int      `json:"mutations"`
	ChecksPassed       int      `json:"checks_passed"`
	ChecksFailed       int      `json:"checks_failed"`
	ChecksSuppressed   int      `json:"checks_suppressed"`
	Review             string   `json:"review"`
	GapKinds           []string `json:"gap_kinds,omitempty"`
	ConstraintDegraded bool     `json:"constraint_degraded"`
	Floor              string   `json:"floor,omitempty"`
	Attention          bool     `json:"attention"`
}

func toWireCompletionSummary(c *event.CompletionSummaryInfo) *CompletionSummary {
	if c == nil {
		return nil
	}
	return &CompletionSummary{
		Preset:             c.Preset,
		Verdict:            c.Verdict,
		Mutations:          c.Mutations,
		ChecksPassed:       c.ChecksPassed,
		ChecksFailed:       c.ChecksFailed,
		ChecksSuppressed:   c.ChecksSuppressed,
		Review:             c.Review,
		GapKinds:           append([]string(nil), c.GapKinds...),
		ConstraintDegraded: c.ConstraintDegraded,
		Floor:              c.Floor,
		Attention:          c.Attention,
	}
}

type WorkspaceChanged struct {
	Revisions  WorkspaceRevision     `json:"revisions"`
	Changes    []WorkspacePathChange `json:"changes"`
	AllPaths   bool                  `json:"allPaths"`
	Source     string                `json:"source"`
	WatchState string                `json:"watchState"`
}

type WorkspaceRevision struct {
	Content     uint64 `json:"content"`
	Tree        uint64 `json:"tree"`
	WorkingTree uint64 `json:"workingTree"`
	GitMeta     uint64 `json:"gitMeta"`
	Session     uint64 `json:"session"`
}

type WorkspacePathChange struct {
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
	Op      string `json:"op"`
}

// StreamAttempt is the JSON form of event.StreamAttemptInfo.
type StreamAttempt struct {
	ID      string `json:"id"`
	Action  string `json:"action"` // begin | discard | commit
	Attempt int    `json:"attempt,omitempty"`
	Max     int    `json:"max,omitempty"`
	Reason  string `json:"reason,omitempty"` // connection_reset | premature_eof | idle_timeout
}

// ToWire converts a typed runtime event into the shared frontend JSON contract.
func ToWire(e event.Event) Event {
	w := Event{Kind: kindNames[e.Kind], PromptKind: e.PromptKind, TurnID: e.TurnID, Sequence: e.Sequence, Status: string(e.Status), Text: e.Text, Detail: e.Detail, Reasoning: e.Reasoning, ItemID: e.ItemID, SessionPath: e.SessionPath, SessionReset: e.SessionReset}
	if e.ItemID != "" {
		promptEvent := false
		switch e.Kind {
		case event.AskRequest:
			promptEvent = true
			w.PromptKind = "ask"
		case event.ApprovalRequest:
			promptEvent = true
			w.PromptKind = e.Approval.Kind
			if w.PromptKind == "" {
				w.PromptKind = "approval"
			}
		case event.MCPInteractionRequest:
			promptEvent = true
			w.PromptKind = "mcp"
		case event.PromptAnswered:
			promptEvent = true
		}
		if promptEvent {
			w.PromptID = e.ItemID
			w.PromptLegacy = e.TurnID == ""
		}
	}
	if len(e.MemoryCitations) > 0 {
		w.MemoryCitations = ToWireMemoryCitations(e.MemoryCitations)
	}
	switch e.Kind {
	case event.Notice:
		w.applyNotice(e)
	case event.ToolDispatch, event.ToolStarted, event.ToolResult, event.ToolProgress, event.ToolResultPreview:
		w.Tool = toWireTool(e.Tool)
	case event.WorkspaceChanged:
		ws := e.Workspace
		if ws == nil {
			ws = &event.WorkspaceChangedPayload{}
		}
		changes := make([]WorkspacePathChange, 0, len(ws.Changes))
		for _, c := range ws.Changes {
			changes = append(changes, WorkspacePathChange{Path: c.Path, OldPath: c.OldPath, Op: c.Op})
		}
		w.Workspace = &WorkspaceChanged{
			Revisions: WorkspaceRevision{Content: ws.Revisions.Content, Tree: ws.Revisions.Tree, WorkingTree: ws.Revisions.WorkingTree, GitMeta: ws.Revisions.GitMeta, Session: ws.Revisions.Session},
			Changes:   changes, AllPaths: ws.AllPaths, Source: ws.Source, WatchState: string(ws.WatchState),
		}
	case event.Usage:
		w.Usage = toWireUsage(e)
	case event.ApprovalRequest:
		w.Approval = toWireApproval(e.Approval)
	case event.AskRequest:
		w.Ask = ToWireAsk(e.Ask)
	case event.MCPInteractionRequest:
		w.MCPInteraction = ToWireMCPInteraction(e.MCPInteraction)
	case event.CompactionStarted, event.CompactionDone:
		w.Compaction = &Compaction{
			Trigger: e.Compaction.Trigger, Messages: e.Compaction.Messages,
			Summary: e.Compaction.Summary, Archive: e.Compaction.Archive,
		}
	case event.ContextMaintenanceEvent:
		if m := e.Maintenance; m != nil {
			w.Maintenance = &ContextMaintenance{
				Status: m.Status, Action: m.Action, Trigger: m.Trigger,
				OperationID: m.OperationID, InputTokens: m.InputTokens,
				ResultTokens: m.ResultTokens, SavedTokens: m.SavedTokens,
				AffectedToolResults: m.AffectedToolResults,
				ProjectionVersion:   m.ProjectionVersion, CacheBreak: m.CacheBreak,
				Reason: m.Reason,
			}
		}
	case event.GuardianAssessment:
		w.Guardian = ToWireGuardian(e.Guardian)
	case event.ExtensionSurface, event.ExtensionStatus:
		w.Extension = ToWireExtensionSurface(e.Extension)
	case event.TurnDone:
		w.Outcome = e.Outcome
		w.CheckpointTurn = e.CheckpointTurn
		w.Receipt = completionReceiptWire(e.Receipt)
		w.ProtocolRecovery = e.ProtocolRecovery
		w.Recovery = e.Recovery
		w.Diagnostic = e.Diagnostic
		if e.Readiness != nil {
			w.Readiness = &FinalReadiness{Attempts: e.Readiness.Attempts, Missing: append([]string(nil), e.Readiness.Missing...)}
		}
		if e.Err != nil {
			w.Err = e.Err.Error()
		}
	case event.Retrying:
		w.Recovery = e.Recovery
		w.RetryAttempt = e.RetryAttempt
		w.RetryMax = e.RetryMax
		if e.RetryScope != "" {
			w.RetryScope = string(e.RetryScope)
		}
	case event.StreamAttempt:
		w.StreamAttempt = &StreamAttempt{
			ID:      e.StreamAttempt.ID,
			Action:  string(e.StreamAttempt.Action),
			Attempt: e.StreamAttempt.Attempt,
			Max:     e.StreamAttempt.Max,
			Reason:  e.StreamAttempt.Reason,
		}
	case event.TurnPhase:
		w.Phase = string(e.PhaseName)
		if w.Phase == "" {
			w.Phase = e.Text
		}
	case event.CompletionSummary:
		w.Completion = toWireCompletionSummary(e.Completion)
	}
	return w
}

func toWireTool(t event.Tool) *Tool {
	wt := &Tool{
		ID: t.ID, Name: t.Name, Args: t.Args,
		ResolvedName: t.ResolvedName, CapabilityID: t.CapabilityID,
		Output: t.Output, Err: t.Err,
		RunState: t.RunState,
		ReadOnly: t.ReadOnly, Truncated: t.Truncated,
		DurationMs: t.DurationMs, Partial: t.Partial,
		StartedAt: t.StartedAt, EndedAt: t.EndedAt,
		ArgChars: t.ArgChars, Refreshed: t.Refreshed,
		ParentID: t.ParentID, AttemptID: t.AttemptID,
		Diff: t.Diff, Added: t.Added, Removed: t.Removed,
		SubagentRef: t.SubagentRef, SubagentStatus: t.SubagentStatus,
		SubagentErrorCode: t.SubagentErrorCode, SubagentRetryable: t.SubagentRetryable,
	}
	if t.Profile != nil {
		wt.Profile = &Profile{Model: t.Profile.Model, Effort: t.Profile.Effort}
	}
	if t.Execution != nil {
		wt.Execution = toWireShellExecution(t.Execution)
	}
	return wt
}

func toWireUsage(e event.Event) *Usage {
	u := e.Usage
	if u == nil {
		return nil
	}
	wire := &Usage{
		PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens,
		TotalTokens: u.TotalTokens, CacheHitTokens: u.CacheHitTokens,
		CacheMissTokens: u.CacheMissTokens, ReasoningTokens: u.ReasoningTokens,
		Estimated:               u.Estimated,
		Source:                  e.UsageSource,
		ContextPromptTokens:     u.ContextPromptTokens,
		ContextCompletionTokens: u.ContextCompletionTokens,
		ContextReasoningTokens:  u.ContextReasoningTokens,
		ContextCacheHitTokens:   u.ContextCacheHitTokens,
		ContextCacheMissTokens:  u.ContextCacheMissTokens,
		SessionCacheHitTokens:   e.SessionHit, SessionCacheMissTokens: e.SessionMiss,
	}
	if e.CacheDiagnostics != nil {
		wire.CacheDiagnostics = ToWireCacheDiagnostics(e.CacheDiagnostics)
	}
	quote := e.CostQuote
	if quote == nil && e.Pricing != nil {
		quote = event.EnsureCostQuote(e, nil)
	}
	if quote != nil {
		wire.CostQuote = quote
		wire.CostComplete = quote.CostComplete
		wire.DisplayComplete = quote.DisplayComplete
		wire.DisplayStatus = quote.DisplayStatus
		wire.AggregateMode = quote.AggregateMode
		wire.OriginalTotals = append([]billing.Money(nil), quote.OriginalTotals...)
		if quote.Selected != nil {
			wire.Cost = quote.Selected.Float64()
			wire.Currency = quote.LegacyCurrencySymbol()
			wire.CostUSD = wire.Cost
			wire.CurrencyCode = quote.LegacyCurrencyCode()
		}
	}
	return wire
}

// DecisionReceipt is the JSON form of a provider-excluded user decision.
type DecisionReceipt struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Tool    string `json:"tool,omitempty"`
	Subject string `json:"subject,omitempty"`
	Outcome string `json:"outcome"`
}

func ToWireDecisionReceipt(in *provider.DecisionReceipt) *DecisionReceipt {
	if in == nil {
		return nil
	}
	return &DecisionReceipt{ID: in.ID, Kind: in.Kind, Tool: in.Tool, Subject: in.Subject, Outcome: in.Outcome}
}

type FinalReadiness struct {
	Attempts int      `json:"attempts,omitempty"`
	Missing  []string `json:"missing,omitempty"`
}

// MemoryCitation is the JSON form of provider.MemoryCitation.
type MemoryCitation struct {
	ID        string `json:"id,omitempty"`
	Source    string `json:"source"`
	LineStart int    `json:"lineStart,omitempty"`
	LineEnd   int    `json:"lineEnd,omitempty"`
	Note      string `json:"note,omitempty"`
	Kind      string `json:"kind,omitempty"`
}

// ToWireMemoryCitations converts local memory references into frontend JSON.
func ToWireMemoryCitations(in []provider.MemoryCitation) []MemoryCitation {
	out := make([]MemoryCitation, 0, len(in))
	for _, c := range in {
		if c.Source == "" && c.ID == "" && c.Note == "" {
			continue
		}
		out = append(out, MemoryCitation{
			ID:        c.ID,
			Source:    c.Source,
			LineStart: c.LineStart,
			LineEnd:   c.LineEnd,
			Note:      c.Note,
			Kind:      c.Kind,
		})
	}
	return out
}

// Compaction is the JSON form of an event.Compaction.
type Compaction struct {
	Trigger  string `json:"trigger,omitempty"`
	Messages int    `json:"messages,omitempty"`
	Summary  string `json:"summary,omitempty" externalizable:"true"`
	Archive  string `json:"archive,omitempty" externalizable:"true"`
}

// AskOption is one JSON-formatted choice in a structured ask request.
type AskOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty" externalizable:"true"`
}

// AskQuestion is one JSON-formatted structured ask question.
type AskQuestion struct {
	ID      string      `json:"id"`
	Header  string      `json:"header,omitempty"`
	Prompt  string      `json:"prompt" externalizable:"true"`
	Options []AskOption `json:"options"`
	Multi   bool        `json:"multi,omitempty"`
}

// Ask is the JSON form of an event.Ask.
type Ask struct {
	ID        string        `json:"id"`
	Questions []AskQuestion `json:"questions"`
	TurnID    string        `json:"turnId,omitempty"`
}

// MCPInteraction is the JSON form of an event.MCPInteraction: one
// server-initiated elicitation awaiting the user's accept/decline/cancel.
// Schema and URL come from the MCP server; form answers travel only in the
// resolve call, never on this event.
type MCPInteraction struct {
	ID              string          `json:"id"`
	Server          string          `json:"server"`
	Mode            string          `json:"mode"`
	Message         string          `json:"message" externalizable:"true"`
	RequestedSchema json.RawMessage `json:"requestedSchema,omitempty"`
	URL             string          `json:"url,omitempty"`
	ElicitationID   string          `json:"elicitationId,omitempty"`
	TurnID          string          `json:"turnId,omitempty"`
}

// applyNotice fills the Notice-specific wire fields.
func (w *Event) applyNotice(e event.Event) {
	w.Code = e.Code
	if e.DecisionReceipt != nil {
		w.DecisionReceipt = ToWireDecisionReceipt(e.DecisionReceipt)
	}
	if e.Level == event.LevelWarn {
		w.Level = "warn"
	} else {
		w.Level = "info"
	}
}

// ToWireMCPInteraction converts event.MCPInteraction to its wire form.
func ToWireMCPInteraction(i event.MCPInteraction) *MCPInteraction {
	return &MCPInteraction{
		ID: i.ID, Server: i.Server, Mode: i.Mode, Message: i.Message,
		RequestedSchema: i.RequestedSchema, URL: i.URL, ElicitationID: i.ElicitationID, TurnID: i.TurnID,
	}
}

// Profile carries the subagent model/effort resolved for a tool call.
type Profile struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// Tool is the JSON form of an event.Tool.
type Tool struct {
	ID                string          `json:"id,omitempty"`
	Name              string          `json:"name"`
	Args              string          `json:"args,omitempty" externalizable:"true"`
	ResolvedName      string          `json:"resolvedName,omitempty"`
	CapabilityID      string          `json:"capabilityId,omitempty"`
	Output            string          `json:"output,omitempty" externalizable:"true"`
	Err               string          `json:"err,omitempty" externalizable:"true"`
	RunState          string          `json:"runState,omitempty"`
	ReadOnly          bool            `json:"readOnly"`
	Truncated         bool            `json:"truncated,omitempty"`
	DurationMs        int64           `json:"durationMs,omitempty"`
	StartedAt         int64           `json:"startedAt,omitempty"` // unix ms; zero when the call never ran
	EndedAt           int64           `json:"endedAt,omitempty"`
	Partial           bool            `json:"partial,omitempty"`
	ArgChars          int             `json:"argChars,omitempty"`
	Refreshed         bool            `json:"refreshed,omitempty"`
	ParentID          string          `json:"parentId,omitempty"`
	AttemptID         string          `json:"attemptId,omitempty"` // host-local stream_attempt id for speculative partials
	SubagentRef       string          `json:"subagentRef,omitempty"`
	SubagentStatus    string          `json:"subagentStatus,omitempty"`
	SubagentErrorCode string          `json:"subagentErrorCode,omitempty"`
	SubagentRetryable bool            `json:"subagentRetryable,omitempty"`
	Diff              string          `json:"diff,omitempty" externalizable:"true"`
	Added             int             `json:"added,omitempty"`
	Removed           int             `json:"removed,omitempty"`
	Profile           *Profile        `json:"profile,omitempty"`
	Execution         *ShellExecution `json:"execution,omitempty"`
}

// ShellExecution is the JSON form of event.ShellExecution (local UI metadata).
type ShellExecution struct {
	Kind           string `json:"kind,omitempty"`
	Shell          string `json:"shell,omitempty"`
	ShellVersion   string `json:"shellVersion,omitempty"`
	Platform       string `json:"platform,omitempty"`
	SupportsAndAnd bool   `json:"supportsAndAnd"`
	State          string `json:"state,omitempty"`
	FailurePhase   string `json:"failurePhase,omitempty"`
	ExitCode       *int   `json:"exitCode,omitempty"`
	OutputTail     string `json:"outputTail,omitempty"`
	MutationRisk   string `json:"mutationRisk,omitempty"`
	Verification   string `json:"verification,omitempty"`
	DurationMs     int64  `json:"durationMs,omitempty"`
}

func toWireShellExecution(in *event.ShellExecution) *ShellExecution {
	if in == nil {
		return nil
	}
	out := &ShellExecution{
		Kind: in.Kind, Shell: in.Shell, ShellVersion: in.ShellVersion,
		Platform: in.Platform, SupportsAndAnd: in.SupportsAndAnd,
		State: in.State, FailurePhase: in.FailurePhase,
		OutputTail: in.OutputTail, MutationRisk: in.MutationRisk,
		Verification: in.Verification, DurationMs: in.DurationMs,
	}
	if in.ExitCode != nil {
		code := *in.ExitCode
		out.ExitCode = &code
	}
	return out
}

// Usage is the JSON form of provider usage telemetry.
type Usage struct {
	PromptTokens     int               `json:"promptTokens"`
	CompletionTokens int               `json:"completionTokens"`
	TotalTokens      int               `json:"totalTokens"`
	CacheHitTokens   int               `json:"cacheHitTokens"`
	CacheMissTokens  int               `json:"cacheMissTokens"`
	ReasoningTokens  int               `json:"reasoningTokens,omitempty"`
	Estimated        bool              `json:"estimated,omitempty"`
	Source           string            `json:"source,omitempty"`
	CacheDiagnostics *CacheDiagnostics `json:"cacheDiagnostics,omitempty"`
	// Session-cumulative cache tokens keep status displays steadier than one-turn values.
	SessionCacheHitTokens  int `json:"sessionCacheHitTokens"`
	SessionCacheMissTokens int `json:"sessionCacheMissTokens"`
	// Context* fields are the latest single-request shape for gauges/rebind.
	// When omitted, clients fall back to the billable prompt/completion totals.
	ContextPromptTokens     int     `json:"contextPromptTokens,omitempty"`
	ContextCompletionTokens int     `json:"contextCompletionTokens,omitempty"`
	ContextReasoningTokens  int     `json:"contextReasoningTokens,omitempty"`
	ContextCacheHitTokens   int     `json:"contextCacheHitTokens,omitempty"`
	ContextCacheMissTokens  int     `json:"contextCacheMissTokens,omitempty"`
	Cost                    float64 `json:"cost,omitempty"`
	Currency                string  `json:"currency,omitempty"`
	// CurrencyCode is the ISO code for Cost (preferred over symbol Currency).
	CurrencyCode string `json:"currencyCode,omitempty"`
	// CostUSD is a compatibility alias for older consumers; it mirrors Cost
	// (selected display valuation) and does not imply USD.
	CostUSD float64 `json:"costUsd,omitempty"`
	// CostQuote is the structured host-side quote. New consumers must prefer it
	// over cost/currency aliases. Never sent to model providers.
	CostQuote       *billing.CostQuote `json:"costQuote,omitempty"`
	CostComplete    bool               `json:"costComplete,omitempty"`
	DisplayComplete bool               `json:"displayComplete,omitempty"`
	DisplayStatus   string             `json:"displayStatus,omitempty"`
	AggregateMode   string             `json:"aggregateMode,omitempty"`
	OriginalTotals  []billing.Money    `json:"originalTotals,omitempty"`
}

// CacheDiagnostics is the JSON form of cache prefix diagnostics.
type CacheDiagnostics struct {
	PrefixHash          string                     `json:"prefixHash"`
	PrefixChanged       bool                       `json:"prefixChanged"`
	PrefixChangeReasons []string                   `json:"prefixChangeReasons,omitempty"`
	SystemHash          string                     `json:"systemHash"`
	ToolsHash           string                     `json:"toolsHash"`
	LogRewriteVersion   int                        `json:"logRewriteVersion"`
	ToolSchemaTokens    int                        `json:"toolSchemaTokens"`
	CacheMissTokens     int                        `json:"cacheMissTokens"`
	CacheHitTokens      int                        `json:"cacheHitTokens"`
	SessionContext      *SessionContextDiagnostics `json:"sessionContext,omitempty"`
}

// Guardian is the JSON form of an event.GuardianResult.
type Guardian struct {
	ID                string `json:"id"`
	Tool              string `json:"tool"`
	Subject           string `json:"subject"`
	Outcome           string `json:"outcome"`
	RiskLevel         string `json:"risk_level,omitempty"`
	UserAuthorization string `json:"user_authorization,omitempty"`
	Rationale         string `json:"rationale,omitempty" externalizable:"true"`
	DurationMs        int64  `json:"duration_ms,omitempty"`
	Usage             *Usage `json:"usage,omitempty"`
}

// ToWireGuardian converts an event.GuardianResult into its JSON wire form.
func ToWireGuardian(g event.GuardianResult) *Guardian {
	out := &Guardian{
		ID:                g.ID,
		Tool:              g.Tool,
		Subject:           g.Subject,
		Outcome:           g.Outcome,
		RiskLevel:         g.RiskLevel,
		UserAuthorization: g.UserAuthorization,
		Rationale:         g.Rationale,
		DurationMs:        g.DurationMs,
	}
	if u := g.Usage; u != nil {
		out.Usage = &Usage{
			PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens,
			TotalTokens: u.TotalTokens, CacheHitTokens: u.CacheHitTokens,
			CacheMissTokens: u.CacheMissTokens, ReasoningTokens: u.ReasoningTokens,
			Estimated: u.Estimated,
		}
		if g.Pricing != nil {
			q := event.EnsureCostQuote(event.Event{Kind: event.Usage, Usage: u, Pricing: g.Pricing}, nil)
			if q != nil {
				out.Usage.CostQuote = q
				out.Usage.Cost = q.LegacyCostFloat()
				out.Usage.Currency = q.LegacyCurrencySymbol()
				out.Usage.CostUSD = out.Usage.Cost
				out.Usage.CurrencyCode = q.LegacyCurrencyCode()
			}
		}
	}
	return out
}

// ToWireAsk converts an event.Ask into its JSON wire form.
func ToWireAsk(a event.Ask) *Ask {
	qs := make([]AskQuestion, len(a.Questions))
	for i, q := range a.Questions {
		opts := make([]AskOption, len(q.Options))
		for j, o := range q.Options {
			opts[j] = AskOption{Label: o.Label, Description: o.Description}
		}
		qs[i] = AskQuestion{ID: q.ID, Header: q.Header, Prompt: q.Prompt, Options: opts, Multi: q.Multi}
	}
	return &Ask{ID: a.ID, Questions: qs, TurnID: a.TurnID}
}

// ToWireCacheDiagnostics converts cache diagnostics into their JSON wire form.
func ToWireCacheDiagnostics(d *event.CacheDiagnostics) *CacheDiagnostics {
	out := &CacheDiagnostics{
		PrefixHash:          d.PrefixHash,
		PrefixChanged:       d.PrefixChanged,
		PrefixChangeReasons: append([]string(nil), d.PrefixChangeReasons...),
		SystemHash:          d.SystemHash,
		ToolsHash:           d.ToolsHash,
		LogRewriteVersion:   d.LogRewriteVersion,
		ToolSchemaTokens:    d.ToolSchemaTokens,
		CacheMissTokens:     d.CacheMissTokens,
		CacheHitTokens:      d.CacheHitTokens,
	}
	if sc := d.SessionContext; sc != nil {
		section := func(in event.SessionContextSectionDiagnostics) SessionContextSectionDiagnostics {
			return SessionContextSectionDiagnostics{Digest: in.Digest, Chars: in.Chars}
		}
		out.SessionContext = &SessionContextDiagnostics{
			Version: sc.Version, Digest: sc.Digest, TargetRole: sc.TargetRole,
			Reasons:     append([]string(nil), sc.Reasons...),
			Environment: section(sc.Environment), Workspace: section(sc.Workspace),
			BackgroundMemory: section(sc.BackgroundMemory), SkillsCatalog: section(sc.SkillsCatalog),
		}
	}
	return out
}

// KindNames returns every stable frontend event kind in event.Kind order. It is
// the protocol-neutral source used by consumers such as the Remote schema
// generator; callers receive a copy and may sort it without mutating eventwire.
func KindNames() []string {
	names := make([]string, 0, int(event.KindCount))
	for kind := range event.KindCount {
		if name, ok := kindNames[kind]; ok {
			names = append(names, name)
		}
	}
	return names
}

// ContextMaintenance is the JSON form of event.ContextMaintenance.
type ContextMaintenance struct {
	Status              string `json:"status,omitempty"`
	Action              string `json:"action,omitempty"`
	Trigger             string `json:"trigger,omitempty"`
	OperationID         string `json:"operationId,omitempty"`
	InputTokens         int    `json:"inputTokens,omitempty"`
	ResultTokens        int    `json:"resultTokens,omitempty"`
	SavedTokens         int    `json:"savedTokens,omitempty"`
	AffectedToolResults int    `json:"affectedToolResults,omitempty"`
	ProjectionVersion   uint64 `json:"projectionVersion,omitempty"`
	CacheBreak          bool   `json:"cacheBreak,omitempty"`
	Reason              string `json:"reason,omitempty"`
}

// ExtensionSurface is the JSON form of an event.ExtensionSurfacePayload.
type ExtensionSurface struct {
	PluginID     string                 `json:"pluginId"`
	SurfaceID    string                 `json:"surfaceId"`
	SessionID    string                 `json:"sessionId,omitempty"`
	Generation   uint64                 `json:"generation,omitempty"`
	Kind         string                 `json:"kind"`
	Status       *ExtensionStatus       `json:"status,omitempty"`
	Card         *ExtensionCard         `json:"card,omitempty"`
	Form         *ExtensionForm         `json:"form,omitempty"`
	Notification *ExtensionNotification `json:"notification,omitempty"`
}

// ExtensionStatus is the JSON form of an event.ExtensionStatusView.
type ExtensionStatus struct {
	Label    string   `json:"label"`
	Detail   string   `json:"detail,omitempty"`
	Severity string   `json:"severity,omitempty"`
	Progress *float64 `json:"progress,omitempty"`
}

// ExtensionKeyValue is the JSON form of an event.ExtensionKeyValue.
type ExtensionKeyValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ExtensionActionRef is the JSON form of an event.ExtensionActionRef.
type ExtensionActionRef struct {
	ActionID string `json:"actionId"`
	Label    string `json:"label"`
}

// ExtensionCard is the JSON form of an event.ExtensionCardView.
type ExtensionCard struct {
	Title    string               `json:"title,omitempty"`
	Markdown string               `json:"markdown,omitempty" externalizable:"true"`
	Text     string               `json:"text,omitempty" externalizable:"true"`
	Fields   []ExtensionKeyValue  `json:"fields,omitempty"`
	Progress *float64             `json:"progress,omitempty"`
	Actions  []ExtensionActionRef `json:"actions,omitempty"`
}

// ExtensionFormField is the JSON form of an event.ExtensionFormField. Default
// travels as raw JSON: the remote schema has no "any" type, and the field is
// already protocol-validated JSON on arrival.
type ExtensionFormField struct {
	Key      string          `json:"key"`
	Label    string          `json:"label,omitempty"`
	Kind     string          `json:"kind,omitempty"`
	Options  []string        `json:"options,omitempty"`
	Default  json.RawMessage `json:"default,omitempty"`
	Required bool            `json:"required,omitempty"`
}

// ExtensionForm is the JSON form of an event.ExtensionFormView.
type ExtensionForm struct {
	Title   string               `json:"title,omitempty"`
	Message string               `json:"message,omitempty" externalizable:"true"`
	Fields  []ExtensionFormField `json:"fields"`
}

// ExtensionNotification is the JSON form of an event.ExtensionNotificationView.
type ExtensionNotification struct {
	Title    string `json:"title"`
	Body     string `json:"body,omitempty" externalizable:"true"`
	Severity string `json:"severity,omitempty"`
}

// ToWireExtensionSurface converts an event.ExtensionSurfacePayload into its
// JSON wire form. A nil payload yields nil so a malformed event never
// marshals a half-filled extension object.
func ToWireExtensionSurface(p *event.ExtensionSurfacePayload) *ExtensionSurface {
	if p == nil {
		return nil
	}
	out := &ExtensionSurface{
		PluginID: p.PluginID, SurfaceID: p.SurfaceID, SessionID: p.SessionID,
		Generation: p.Generation, Kind: p.Kind,
	}
	if s := p.Status; s != nil {
		out.Status = &ExtensionStatus{Label: s.Label, Detail: s.Detail, Severity: s.Severity, Progress: s.Progress}
	}
	if c := p.Card; c != nil {
		card := &ExtensionCard{
			Title: c.Title, Markdown: c.Markdown, Text: c.Text, Progress: c.Progress,
		}
		if len(c.Fields) > 0 {
			card.Fields = make([]ExtensionKeyValue, len(c.Fields))
			for i, f := range c.Fields {
				card.Fields[i] = ExtensionKeyValue{Key: f.Key, Value: f.Value}
			}
		}
		if len(c.Actions) > 0 {
			card.Actions = make([]ExtensionActionRef, len(c.Actions))
			for i, a := range c.Actions {
				card.Actions[i] = ExtensionActionRef{ActionID: a.ActionID, Label: a.Label}
			}
		}
		out.Card = card
	}
	if f := p.Form; f != nil {
		form := &ExtensionForm{Title: f.Title, Message: f.Message}
		if len(f.Fields) > 0 {
			form.Fields = make([]ExtensionFormField, len(f.Fields))
			for i, field := range f.Fields {
				wireField := ExtensionFormField{
					Key: field.Key, Label: field.Label, Kind: field.Kind,
					Options:  append([]string(nil), field.Options...),
					Required: field.Required,
				}
				if field.Default != nil {
					// The value arrived as protocol-validated JSON, so a
					// marshal failure here is unreachable in practice; a
					// pathological in-memory value simply drops the default.
					if raw, err := json.Marshal(field.Default); err == nil {
						wireField.Default = raw
					}
				}
				form.Fields[i] = wireField
			}
		}
		out.Form = form
	}
	if n := p.Notification; n != nil {
		out.Notification = &ExtensionNotification{Title: n.Title, Body: n.Body, Severity: n.Severity}
	}
	return out
}
