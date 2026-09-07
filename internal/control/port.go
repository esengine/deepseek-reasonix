package control

import (
	"context"

	"reasonix/internal/agent"
	"reasonix/internal/billing"
	"reasonix/internal/checkpoint"
	"reasonix/internal/command"
	"reasonix/internal/config"
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/hook"
	"reasonix/internal/jobs"
	"reasonix/internal/memory"
	"reasonix/internal/plugin"
	"reasonix/internal/provider"
	"reasonix/internal/sandbox"
	"reasonix/internal/skill"
)

// This file defines the driving port: the typed, segregated interface surface
// that frontends (cli, desktop, bot, acp, serve) consume instead of coupling to
// the concrete *Controller and its ~99 methods. Each frontend depends only on
// the sub-ports it actually uses (interface segregation), so e.g. the bot never
// sees checkpoint or memory methods.
//
// The sub-ports are also the intended decomposition boundary for Controller
// itself: the port comes first and gives the later collaborator splits a spec to
// follow. *Controller implements every sub-port (asserted below). The full
// SessionAPI composition will accrete here as the remaining frontends migrate.

// Lifecycle covers a session's identity and lifecycle: minting, resuming,
// clearing, and locating the active session.
type Lifecycle interface {
	NewSession() error
	ClearSession() error
	Resume(s *agent.Session, path string)
	SetSessionPath(p string)
	SessionPath() string
	SessionDir() string
	Label() string
	ModelRef() string
	WorkspaceRoot() string
	Close()
}

// TurnControl covers driving a model turn and observing its run state: the
// various submit/run entry points, cancellation, steering, and status reads.
type TurnControl interface {
	Submit(input string)
	SubmitDisplay(display, input string)
	SubmitFinalReadinessRecovery(display, input string)
	SubmitDeliveryRecovery(display, input string)
	SubmitInvocationDisplay(display, input string, invocations []InvocationRequest)
	SubmitEditedDisplay(display, input, original string)
	SubmitHTTP(input string)
	SubmitHTTPFormat(input, format string)
	SubmitUserTurn(input, display string)
	Send(input string)
	SendWithRaw(input, raw string)
	Run(ctx context.Context, input string) error
	RunTurn(ctx context.Context, input string) error
	RunFinalReadinessRecovery(ctx context.Context, input string) error
	RunShell(command string)
	Cancel()
	Steer(text string)
	SteerConsumed() bool
	Running() bool
	CancelRequested() bool
	RuntimeStatus() RuntimeStatus
	Turn() int
	History() []provider.Message
	ToolResult(toolID string) *ToolResultData
}

// Approvals covers tool-approval and ask prompts plus the runtime approval
// posture (ask/auto/yolo). It mirrors the approvalManager surface.
type Approvals interface {
	Approve(id string, allow, session, persist bool)
	ResolveApproval(id string, allow bool, scope sandbox.ApprovalScope) error
	ResolvePlanDecision(id string, action PlanDecisionAction) error
	ResolvePlanDecisionWithFeedback(id string, action PlanDecisionAction, feedback string) error
	// ResolveRecovery answers an Auto Guard card: continue|continue_task|revise. Revise
	// refuses the mutation and steers feedback.
	ResolveRecovery(id string, action agent.RecoveryAction, feedback string) error
	// ConfirmRecoveredTool attests that one unverified tool effect from an
	// interrupted turn already happened. It records the attestation and never
	// synthesizes the missing tool result.
	ConfirmRecoveredTool(callID string) error
	AnswerMCPInteraction(id, action string, content map[string]any)
	AnswerQuestion(id string, answers []event.AskAnswer)
	AnswerQuestionChecked(id string, answers []event.AskAnswer) error
	// AnswerMCPInteractionChecked resolves an mcp_interaction prompt after its
	// durable transition (serve /mcp-interaction, desktop bridge).
	AnswerMCPInteractionChecked(id, action string, content map[string]any) error
	Ask(ctx context.Context, questions []event.AskQuestion) ([]event.AskAnswer, error)
	ReplayPendingPrompts()
	ReplayPendingPromptsTo(sink event.Sink)
	ReplayPendingPromptsWith(sinkFactory func() event.Sink)
	PendingPrompt() bool
	EnableInteractiveApproval()
	ToolApprovalMode() string
	SetToolApprovalMode(mode string)
	AutoApproveTools() bool
	SetAutoApproveTools(on bool)
	Bypass() bool
	SetBypass(on bool)
	SetMode(plan, autoApproveTools bool)
}

// Goals covers the active-goal FSM and plan mode.
type Goals interface {
	Goal() string
	GoalStatus() string
	SetGoal(goal string)
	// SetGoalWithResearchMode is retained for deprecated CLI budget flags. The
	// mode is translated at the boundary and is not stored in the Goal runtime.
	SetGoalWithResearchMode(goal string, researchMode GoalResearchMode)
	ResumeGoal() bool
	PauseGoal() bool
	GoalRuntime() GoalRuntimeView
	GoalStrict(strict bool)
	ClearGoal()
	ResetPlannerSession()
	PlanMode() bool
	SetPlanMode(v bool)
	// AgentPreset is the session role setting (standard|delivery), derived
	// from the quality floor.
	AgentPreset() string
	// SetAgentPreset updates the role setting for subsequent turns without
	// rebuilding the controller.
	SetAgentPreset(preset string)
	// QualityFloor is the session delivery floor (standard|delivery).
	QualityFloor() string
	// SetQualityFloor updates the session delivery floor for subsequent
	// turns without rebuilding the controller.
	SetQualityFloor(floor string) error
}

// SessionHistory covers checkpoint/rewind, branch/fork, and the log-restructuring
// operations (compact, summarize).
type SessionHistory interface {
	Checkpoints() []checkpoint.Meta
	CheckpointFileState(path string) (checkpoint.FileState, bool)
	CheckpointTurnsByMessageIndex() map[int]int
	CheckpointHasBoundary(turn int) bool
	Rewind(turn int, scope RewindScope) error
	PrepareRewind(turn int, scope RewindScope) (checkpoint.RewindPlan, error)
	CommitRewind(planID string) (checkpoint.RewindResult, error)
	UndoRewind(transactionID string) (checkpoint.RewindResult, error)
	PrepareFileRevert(path string) (checkpoint.RewindPlan, error)
	CommitFileRevert(planID string, resolution checkpoint.ConflictResolution) (checkpoint.RewindResult, error)
	Fork(turn int) (string, error)
	ForkNamed(turn int, name string) (string, error)
	ForkSession(turn int, name string) (string, error)
	Branch(name string) (string, error)
	Branches() ([]agent.BranchInfo, error)
	BranchTreeText() string
	SwitchBranch(ref string) (agent.BranchInfo, error)
	Compact(ctx context.Context, instructions string) error
	CompactRatio() float64
	ContextReport() (summary, detail string)
	SummarizeFrom(ctx context.Context, turn int) error
	SummarizeUpTo(ctx context.Context, turn int) error
}

// MemoryControl covers session/project memory reads and mutations.
type MemoryControl interface {
	Memory() *memory.Set
	QuickAdd(scope memory.Scope, note string) (string, error)
	SaveDoc(path, body string) (string, error)
	SaveMemory(m memory.Memory) (string, error)
	ForgetMemory(name string) error
	QueueMemory(note string)
	MemoryRevisions(ref string) []memory.Memory
	RestoreMemory(ref string, revision int) (memory.Memory, error)
	RestoreArchivedMemory(archivePath string) (memory.Memory, error)
	LastMemoryRecall() memory.RecallResult
}

// Capabilities covers the session's pluggable surface — MCP servers, skills,
// slash commands, hooks — and resolving prompt/command/skill inputs.
type Capabilities interface {
	Host() *plugin.Host
	Commands() []command.Command
	ReloadCommands(ctx context.Context) error
	Skills() []skill.Skill
	SlashSkills() []skill.Skill
	AllSkills() []skill.Skill
	DisabledSkills() []skill.Skill
	SkillEnabled(name string) bool
	SetSkillEnabled(name string, enabled bool) error
	CreateSkill(name string, scope skill.Scope, content string) (string, error)
	UpdateSkill(name string, scope skill.Scope, content string) error
	DeleteSkill(name string, scope skill.Scope) error
	HookRunner() *hook.Runner
	CustomCommand(input string) (sent string, found bool)
	MCPPrompt(ctx context.Context, input string) (sent string, found bool, err error)
	// MCPCapabilityViews returns the host's four-layer capability matrix
	// (Protocol Connection, Core Host, Interactive Host, Apps Host) as
	// read-only diagnostics for MCP status surfaces.
	MCPCapabilityViews() []plugin.CapabilityView
	RunSkill(input string) (sent string, found bool)
	AddMCPServer(e config.PluginEntry) (int, error)
	ConnectMCPServer(e config.PluginEntry) (int, error)
	RegisterMCPServerOnDemand(e config.PluginEntry) (int, error)
	ConnectConfiguredMCPServer(name string) (int, error)
	DisconnectMCPServer(name string) bool
	RemoveMCPServer(name string) (disconnected bool, err error)
	ConfiguredMCPNames() []string
	DisconnectedMCPNames() []string
	UnregisterMCPServerTools(name string) bool
	ImportMCPEntries(entries []config.PluginEntry) (total, added, updated, connected, failed, skipped int, err error)
	// Extension UI (stage 8a): enumerate handshake-declared extension actions
	// and invoke one by its public /<plugin>:<action> name. Nil hub → empty /
	// error; the stage-8b slash dispatch resolves these.
	ExtensionActions() []ExtensionActionView
	InvokeExtensionAction(ctx context.Context, name string, args map[string]string) (string, error)
	// ProviderCatalog is the session's merged provider catalog — config/broker
	// base plus sidecar-declared extension providers (plugin/... refs). Nil
	// when no extension declared providers; frontends merge it into their
	// model pickers and skip nil.
	ProviderCatalog() []provider.Descriptor
}

// Status covers read-only run/usage/billing telemetry and task list state.
type Status interface {
	ContextSnapshot() (int, int)
	ContextMaintenanceSnapshot() agent.ContextMaintenanceSnapshot
	LastUsage() *provider.Usage
	Balance(ctx context.Context) (*billing.Balance, error)
	Jobs() []jobs.View
	Todos() []evidence.TodoItem
	// BoundShell reports the interpreter this controller generation bound at
	// build time, so hosts can distinguish the live session's shell from what
	// a reload would resolve now.
	BoundShell() sandbox.Shell
}

// SessionPersistence covers snapshotting a session and tearing down its on-disk
// state.
type SessionPersistence interface {
	Snapshot() error
	SnapshotForShutdown() error
	SnapshotActivity() error
	// SessionHasUnsavedChanges reports whether the in-memory transcript is
	// newer than the durable session file. Frontends use this to avoid
	// replacing a failed/contended save with stale disk history.
	SessionHasUnsavedChanges() bool
	SessionCache() (hit, miss int)
	BeginDestroySession(sessionPath string) SessionDestroyHandle
	CloseAfterDestroy()
	IsDestroyingSession(sessionPath string) bool
	ReleaseResources()
}

// Input covers composing a turn's text (plan/goal/memory injection) and
// resolving @-references before submission.
type Input interface {
	Compose(text string) string
	ComposeSynthetic(text string) string
	ResolveRefs(ctx context.Context, line string) (block string, errs []string)
	HasRefs(line string) bool
	ImageInputEnabled() bool
	RegisterExternalFolderRef(path string) (token, displayPath string, err error)
}

// Settings covers runtime session settings that don't fit a richer domain.
type Settings interface {
	SetResponseLanguage(lang string)
	SetReasoningLanguage(lang string)
	SetDisplayRecorder(fn func(content, display string))
	ApplyComposerProfile(plan bool, toolApprovalMode, goal string) ([]string, error)
	SystemPrompt() string
}

// SessionAPI is the full driving port — the composition of every sub-port. A
// rich frontend (the HTTP server, the desktop app, the TUI) depends on this;
// leaner frontends (bot, acp) depend on just the sub-ports they use.
type SessionAPI interface {
	Lifecycle
	TurnControl
	Approvals
	Goals
	SessionHistory
	MemoryControl
	Capabilities
	Status
	SessionPersistence
	Input
	Settings
	Inbox
}

// Compile-time proof that the concrete controller satisfies each sub-port and
// the full port, so frontend migrations to the interfaces are mechanical and can
// never silently drift from the implementation.
var (
	_ Lifecycle          = (*Controller)(nil)
	_ TurnControl        = (*Controller)(nil)
	_ Approvals          = (*Controller)(nil)
	_ Goals              = (*Controller)(nil)
	_ SessionHistory     = (*Controller)(nil)
	_ MemoryControl      = (*Controller)(nil)
	_ Capabilities       = (*Controller)(nil)
	_ Status             = (*Controller)(nil)
	_ SessionPersistence = (*Controller)(nil)
	_ Input              = (*Controller)(nil)
	_ Settings           = (*Controller)(nil)
	_ Inbox              = (*Controller)(nil)
	_ SessionAPI         = (*Controller)(nil)
)
