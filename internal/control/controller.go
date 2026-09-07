// Package control is the transport-agnostic session driver. A Controller owns
// the agent run loop and session lifecycle, takes commands (Send/Cancel/Approve/
// SetPlanMode/Compact/NewSession/…), and emits everything that happens —
// reasoning, tool calls, approvals, turn completion — as a typed event stream to
// a single event.Sink.
//
// The point is one orchestration layer behind every frontend: a terminal TUI, a
// desktop webview, or an HTTP/SSE server each drive the Controller identically
// (issue commands, render events) and none of them re-implement turn lifecycle,
// cancellation, or approval. The Controller depends on no frontend.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"reasonix/internal/ablation"
	"reasonix/internal/agent"
	"reasonix/internal/agentpreset"
	"reasonix/internal/autoresearch"
	"reasonix/internal/billing"
	"reasonix/internal/capability"
	"reasonix/internal/checkpoint"
	"reasonix/internal/command"
	"reasonix/internal/config"
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/extension"
	"reasonix/internal/extension/dispatch"
	"reasonix/internal/extension/uihub"
	"reasonix/internal/goaleval"
	"reasonix/internal/guardian"
	"reasonix/internal/hook"
	"reasonix/internal/i18n"
	"reasonix/internal/jobs"
	"reasonix/internal/mcpinteraction"
	"reasonix/internal/memory"
	"reasonix/internal/nilutil"
	"reasonix/internal/permission"
	"reasonix/internal/plugin"
	"reasonix/internal/provider"
	"reasonix/internal/recovery"
	"reasonix/internal/sandbox"
	"reasonix/internal/sessioncontext"
	"reasonix/internal/sessioninbox"
	"reasonix/internal/sessiontemp"
	"reasonix/internal/shellrun"
	"reasonix/internal/skill"
	"reasonix/internal/store"
	"reasonix/internal/taskmonitor"
	"reasonix/internal/tool"
	"reasonix/internal/workspacelease"
)

// ErrTurnRunning reports that a caller tried to start a second foreground turn
// while one is already active in the same Controller.
var ErrTurnRunning = errors.New("turn already running")

// ErrNoFinalReadinessRecovery means an explicit continuation did not match the
// immediately preceding paused readiness check (for example, an old card after
// a newer user turn). It must not silently become an ordinary turn.
var ErrNoFinalReadinessRecovery = errors.New("no pending final-readiness check to continue")

// ErrRuntimeDraining reports that a caller targeted a controller generation
// superseded by a successful rebuild.
var ErrRuntimeDraining = errors.New("runtime is draining after rebuild")

// errTurnRunningRotation and errRotationInProgress are returned by the
// session-rotation gate (beginRotation) when a rotation cannot proceed: a turn
// is in flight, or another rotation already holds the gate.
var (
	errTurnRunningRotation = errors.New("cannot start a new session while a turn is running")
	errRotationInProgress  = errors.New("cannot start a new session while another session change is in progress")
)

// errNoSessionPath is returned by snapshot when a session has content to persist
// but no resolved session path — a misconfiguration (e.g. an unresolvable data
// dir in a bot deployment) that previously dropped conversations silently
// (#4414). Callers log it and continue; it must never be swallowed quietly.
var errNoSessionPath = errors.New("session has content but no session path; conversation cannot be persisted")

// Controller drives one chat session. Construct with New; drive with the command
// methods; observe through the Sink passed in Options.
type Controller struct {
	// promptResolveMu serializes exact prompt decisions on one controller. It
	// prevents two UI submissions from racing through separate prompt managers.
	promptResolveMu    sync.Mutex
	promptRuntimeEpoch string
	promptOwner        PendingPromptOwner
	runner             agent.Runner
	executor           *agent.Agent
	guardianSess       *guardian.Session // nil when guardian is disabled
	guardianPath       string            // persisted guardian session file ("" when disabled)
	// recoveryGate is the shared Auto Guard state for this controller.
	// nil when the feature is not wired for this controller.
	recoveryGate *recovery.Gate

	// taskBudget is the configured spend gate, as passed at construction.
	taskBudget agent.TaskBudget
	// goalTokenBudget bounds an unattended Goal loop; 0 leaves it unbounded.
	goalTokenBudget int
	// evaluator is the bounded Goal completion evaluator consulted when the
	// working model submits no update_goal report. nil fails closed: the goal
	// pauses instead of defaulting to continue.
	evaluator goaleval.Evaluator
	// goalUsageTee accounts billable usage events into the active goal turn's
	// observational token total. It wraps the public sink when the caller didn't provide one.
	goalUsageTee *goalUsageTee
	sink         event.Sink
	policy       permission.Policy
	// subagentGate is the shared gate every headless-only sub-agent surface
	// reads from (see Options.SubagentGate). Nil when the caller didn't build
	// one — sub-agents then keep whatever gate they were constructed with.
	subagentGate *SharedHeadlessGate

	label                   string
	modelRef                string
	visionModel             string
	visionProviderResolver  func(string) (provider.Provider, error)
	visionModelSelector     func(string, string) (string, bool)
	modelCapabilityResolver func(*config.ProviderEntry) config.ResolvedModelCapability
	frozenImageInput        *bool
	imageCapabilityChanged  func() bool
	prompt                  controllerPromptState
	pinnedContextLoader     PinnedContextLoader
	sessionContextStatic    sessioncontext.Sections
	sessionDir              string
	commands                atomic.Pointer[[]command.Command]
	// skills owns the session's discovered skills (enabled subset, full set, and
	// the reloadable stores) — the skills slice of the Capabilities concern. See
	// skill.go.
	skills                         skillSet
	skillRunner                    skill.SubagentRunner
	readOnlySkillRunner            skill.SubagentRunner
	skillProfile                   skill.ProfileResolver
	disableImplicitSkillInvocation bool
	slashSkillSeq                  atomic.Uint64
	hooks                          *hook.Runner // session hook runner; nil-safe (no hooks configured)
	// hookContexts carries one-shot lifecycle hook context into the next real
	// user turn without changing the cache-stable system prompt.
	hookContexts []string
	// memory owns the loaded memory snapshot, the pending turn-tail notes queue,
	// and write serialization behind its own locks, off c.mu — so a memory-panel
	// save never stalls an approval or status poll. See memory.go.
	memory                 memoryManager
	cleanup                func()
	responseLanguage       string
	reasoningLanguage      string
	disableColdResumePrune bool // legacy; rewrite elision removed, still gates cold notice
	// testCacheColdAfter overrides cacheColdAfter() in tests. Zero uses the
	// vendor-aware resolution from config.
	testCacheColdAfter time.Duration

	shell                             sandbox.Shell                    // interpreter for user-invoked "!" commands; zero = auto
	startedOnce                       bool                             // guards the one-shot SessionStart hook on first turn
	closeOnce                         sync.Once                        // makes close idempotent under racing teardown paths
	onRemember                        func(rule string) RememberResult // set via Options; invoked when user picks "always allow"
	onRememberPlanModeReadOnlyCommand func(prefix string) PlanModeReadOnlyCommandTrustResult
	writeAccess                       controllerWriteAccess
	sessionRecoveryMeta               func(SessionRecoveryRequest) agent.BranchMeta
	onSessionRecovered                func(SessionRecoveryInfo) error
	onSessionTransition               func(SessionTransitionInfo) error

	// balanceURL/balanceKey target the active provider's optional wallet-balance
	// endpoint (empty when the provider declares none). Captured at build so a
	// model/key switch — which rebuilds the controller — refreshes them.
	balanceURL    string
	balanceKey    string
	balanceClient *http.Client

	// jobs is the session-scoped background-job manager. The agent's background
	// tools spawn into it; Compose drains its completion notes into the next turn;
	// Close cancels its still-running jobs.
	jobs *jobs.Manager
	// workspaceLease is the Delivery writer owner shared with the executor.
	// It is exposed only through a sanitized state snapshot for Desktop recovery.
	workspaceLease *workspacelease.Owner

	// mcp owns the session's live tool/plugin surface behind its own lock, off
	// c.mu; the Controller keeps config-facing orchestration. See mcp.go.
	mcp                   mcpManager
	mcpDefaultCallTimeout time.Duration
	mcpConfigureSpec      func(*plugin.Spec)
	capabilityRuntime     *agent.MCPCapabilityRuntime

	runtimeGeneration  uint64 // PublishGate gen; 0 disables
	runtimeOwner       *extension.RuntimeOwner
	lastResumeDecision extension.ResumeDecision
	// extensions is the frozen extension dispatcher for this controller
	// generation, or nil when no v2 runtime packages are installed (the
	// universal pre-dispatch fast path). It is installed before the controller
	// starts serving (Options.Extensions or SetExtensions) and never swapped
	// afterwards, so wiring points read it without locking.
	extensions *dispatch.Dispatcher
	// extensionUI is the host extension UI hub for this controller generation
	// (stage 8a), or nil when no v2 runtime packages started. Installed via
	// SetExtensionUI before serving and never swapped; readers take c.mu.
	extensionUI *uihub.Hub
	// providerResolver is the build's merged provider catalog (extension
	// sidecar providers over the config/broker base), or nil when no sidecar
	// declared providers. Immutable after New; ProviderCatalog reads it.
	providerResolver provider.Resolver

	// Capability routing (Delivery hybrid route + dual-model Planner proxy).
	// Not part of the provider-visible prefix; only seeds the turn-scoped ledger
	// and optional semantic router.
	pluginCfg       []config.PluginEntry
	capCachedTools  map[string][]plugin.CachedTool
	capCacheKeyOK   map[string]bool
	semanticRouter  *capability.SemanticRouter
	capabilityAudit *capability.Audit
	// capabilityProxy directs unready MCP candidates to use_capability in the
	// transient route block (Delivery and dual-model Planner).
	capabilityProxy bool
	// proxyToolsFn returns live tools observed through use_capability without
	// entering the provider-visible registry (dual-model Planner).
	proxyToolsFn func() map[string][]plugin.CachedTool
	ablation     ablation.Set

	// goals owns the active goal's FSM (status, intercepts, idle/turn counters)
	// and its persistence, behind its own mutex so a per-turn goal save never
	// stalls an approval or status poll on c.mu. See goal.go.
	goals goalMachine
	// legacyResearchArchive reads explicit pre-unification task paths. It never
	// creates or mutates archive state. See
	// autoresearch_manager.go.
	legacyResearchArchive legacyResearchArchive
	legacyRestoreMu       sync.Mutex
	legacyRestore         legacyGoalRestore

	// workspaceRoot is the workspace root: the base for resolving @-refs and slash
	// path refs, the working directory for user "!" shell commands and custom
	// command discovery, and the guard root for checkpoint restore writes. It is
	// surfaced to frontends via WorkspaceRoot().
	workspaceRoot string

	// externalFolderRefs maps session-generated @ tokens to user-dropped
	// directories outside workspaceRoot. It is intentionally per-controller:
	// dragging a folder authorizes that folder for this chat session only, without
	// widening scoped @ resolution to arbitrary absolute paths.
	externalFolderRefsMu   sync.RWMutex
	externalFolderRefs     map[string]string
	externalFolderToolRefs externalFolderToolRefs

	// checkpoints owns the snapshot-based rewind bookkeeping (the per-session
	// store, the monotonic turn counter, and the conversation-rewind boundary map)
	// behind its own lock, off c.mu — so a boundary read for a rewind/fork never
	// contends on the run-state lock. The Controller keeps the rewind/fork/summarize
	// orchestration (truncating the session, restoring code, emitting events). See
	// checkpoint.go.
	checkpoints checkpointManager
	// mutationObserver is the host-side file mutation observer for v2 checkpoints.
	mutationObserver *checkpoint.MutationObserver
	// sessionRevision increments on successful rewind/undo and is used as a
	// prepare/commit freshness token.
	sessionRevision int64

	// approval owns the approval/ask prompt bookkeeping and the runtime approval
	// posture (ask/auto/yolo, session grants, the just-approved-plan window)
	// behind its own locks, off c.mu. The Controller keeps the I/O orchestration
	// (requestApproval/Ask emit events + fire hooks + rebuild the executor gate).
	// See approval.go.
	approval approvalManager

	// mu guards the run state; every critical section under it is short and
	// non-blocking.
	mu                sync.Mutex
	cancel            context.CancelFunc
	running           bool
	finishing         bool // TurnDone is still being delivered; park a replacement turn
	finishingBoundary turnFinishingBoundary
	canceling         bool
	// closed marks the controller as terminally torn down (close() ran). It
	// seals turn admission: without it, a submit arriving AFTER close cleared
	// the parked queue — but while a still-running turn's TurnDone delivery
	// was in flight — would park again and then start against freed resources
	// when the window closed.
	closed bool
	// parkedTurns holds turn bodies that arrived during the finishing window,
	// FIFO. finishGuardedTurn starts the oldest one as it closes the window
	// (see runGuarded/finishGuardedTurn); close() discards any remainder.
	parkedTurns []func(ctx context.Context) error
	// rotating is set under mu while NewSession/ClearSession swap the executor
	// session out. Checking running once and then swapping later leaves a
	// TOCTOU window: a turn can start (running=false at check time) during the
	// intervening Snapshot() and then have its live session replaced. running
	// and rotating are mutually exclusive gates — a turn refuses to start while
	// a rotation is in progress, and a rotation refuses to start while a turn
	// runs — so the run loop's session reference cannot change under it.
	rotating   bool
	autosaveWG sync.WaitGroup
	// sessionSettings groups the per-session posture knobs that share one
	// lifetime: swapped together on session rotation.
	sessionSettings sessionSettings
	sessionPath     string
	// sessionTemp owns the logical-session private temporary directory shared
	// by Bash calls. Retained for this Controller's lifetime; rotated on
	// /new, /clear, resume of another session, and branch switches.
	sessionTemp *sessiontemp.Manager
	// snapshotMu serializes the whole save/recovery handoff for this controller.
	// Agent-level path locks protect individual files, but recovery also moves
	// controller-owned state (sessionPath, guardianPath, checkpoints, rewrite
	// baseline). Letting a second snapshot observe that migration halfway through
	// can turn one conflict into a recovery cascade. Session/path swaps
	// (new/clear/fork/branch/switch/resume/SetSessionPath) hold it for the same
	// reason: a save that reads the old path but the new session would write one
	// transcript's messages into another's file, or manufacture a bogus conflict.
	// Not reentrant — never call snapshot (or anything that snapshots, such as
	// recoverInterruptedTurn or maybeColdResumePrune) while holding it.
	snapshotMu sync.Mutex
	// turn counts model turns this session, passed to hooks in their payload.
	turn       int
	turnEvents turnEventState

	displayRecorder func(content, display string)

	// inbox is the durable session-level instruction queue. Disk I/O never
	// runs under c.mu; the store owns its own lock.
	inbox inboxState
}

type approvalReply struct {
	allow      bool
	session    bool
	persist    bool // true = write "always allow" rule to config
	onceDirs   []string
	persistErr error
}

type pendingApproval struct {
	id           string
	tool         string
	subject      string
	reason       string
	rawInput     json.RawMessage
	fresh        bool
	requireHuman bool
	autoDrain    bool
	kind         string // tool | plan | recovery | write_access; empty = tool
	recovery     *event.RecoveryApproval
	writeAccess  *event.WriteAccessApproval
	reply        chan approvalReply
}

// pendingAsk is an in-flight ask question batch. questions is retained so the
// AskRequest can be re-emitted to a frontend that reconnected after the original
// event (see ReplayPendingPrompts).
type pendingAsk struct {
	questions []event.AskQuestion
	reply     chan []event.AskAnswer
	queued    bool // registered but not yet shown; replay must skip it
}

type plannerSessionResetter interface {
	ResetPlannerSession()
}

// RuntimeStatus is the frontend-facing snapshot of foreground turn state. It is
// intentionally more explicit than the legacy Running bool so UI code can
// distinguish a cancellable foreground turn from pending prompts and background
// jobs.
type RuntimeStatus struct {
	Running         bool
	PendingPrompt   bool
	BackgroundJobs  int
	CancelRequested bool
	Cancellable     bool
	TurnID          string
	Status          event.TurnStatus
	TurnEventSeq    uint64
	ReplayAfterSeq  uint64
}

const (
	ToolApprovalAsk     = "ask"
	ToolApprovalAuto    = "auto"
	ToolApprovalDontAsk = "dontAsk"
	ToolApprovalYolo    = "yolo"
)

const (
	memoryRememberTool = "remember"
	memoryForgetTool   = "forget"
)

// RememberResult describes what happened when an approval rule was persisted.
type RememberResult struct {
	Rule      string
	Path      string
	Saved     bool
	CoveredBy string
	Err       error
}

// PlanModeReadOnlyCommandTrustResult describes what happened when a trusted bash
// command prefix was persisted for plan-mode research.
type PlanModeReadOnlyCommandTrustResult struct {
	Prefix    string
	Path      string
	Saved     bool
	CoveredBy string
	Err       error
}

type SessionRecoveryRequest struct {
	OriginalPath string
	Reason       string
	Mode         string
}

type SessionRecoveryInfo struct {
	OriginalPath string
	RecoveryPath string
	Existing     bool
	Reason       string
	BaseRevision int64
	DiskRevision int64
	Meta         agent.BranchMeta
	commit       *sessionRecoveryCommit
}

func snapshotConflictRevisions(err error) (base, disk int64) {
	var conflict *agent.SessionSnapshotConflictError
	if errors.As(err, &conflict) && conflict != nil {
		return conflict.BaseRevision, conflict.DiskRevision
	}
	return 0, 0
}

// OnCommit defers publication work until the controller has installed the
// recovery path and rebound all path-scoped state.
func (i SessionRecoveryInfo) OnCommit(fn func()) {
	if i.commit != nil && fn != nil {
		i.commit.hooks = append(i.commit.hooks, fn)
	}
}

type sessionRecoveryCommit struct {
	hooks []func()
}

func (c *sessionRecoveryCommit) publish() {
	for _, hook := range c.hooks {
		hook()
	}
	c.hooks = nil
}

type externalFolderToolRefs interface {
	RegisterReadRoot(token, root string)
}

// Options carries the already-built pieces setup assembles. Lifecycle metadata
// lets the controller mint and rotate session files; Host/Commands are surfaced
// to frontends that resolve MCP prompts and slash commands.
type Options struct {
	Runner   agent.Runner
	Executor *agent.Agent
	Guardian *guardian.Session
	// RecoveryReviewer is the optional independent recovery reviewer (nil =
	// rule-only path with fail-closed human confirmation for ambiguous cases).
	RecoveryReviewer recovery.Reviewer
	// RecoveryHeadless blocks mutations that need confirmation instead of
	// waiting forever when no human decision channel exists.
	RecoveryHeadless bool
	// TaskBudget is the configured spend gate; unset leaves a turn unbounded.
	TaskBudget agent.TaskBudget
	// GoalTokenBudget bounds an unattended Goal loop by cumulative tokens.
	GoalTokenBudget int
	// GoalEvaluator is the optional bounded Goal completion evaluator consulted
	// when the working model submits no update_goal report. nil fails closed:
	// the goal pauses instead of defaulting to continue.
	GoalEvaluator goaleval.Evaluator
	Sink          event.Sink
	Policy        permission.Policy
	// SubagentGate is the shared, mutable gate every headless-only sub-agent
	// surface (task, writer-capable skill sub-agents, planner) reads from. Nil
	// disables gating for those surfaces same as before this field existed.
	// SetToolApprovalMode and ApplyHeadlessApprovalMode call Update on it so a
	// runtime approval-mode switch reaches sub-agents, not just the parent
	// executor's own gate.
	SubagentGate *SharedHeadlessGate
	Label        string
	ModelRef     string
	// VisionModel is empty (off), "auto", or a canonical provider/model ref.
	// The resolver and selector are assembled by boot so the controller remains
	// transport-agnostic and tests can inject deterministic fake providers.
	VisionModel            string
	VisionProviderResolver func(string) (provider.Provider, error)
	VisionModelSelector    func(string, string) (string, bool)
	// ModelCapabilityResolver returns the adapter/config-resolved metadata for
	// the exact active model. Nil keeps the legacy config-only behavior.
	ModelCapabilityResolver func(*config.ProviderEntry) config.ResolvedModelCapability
	// FrozenImageInput belongs to the provider instance built for this runtime.
	FrozenImageInput       *bool
	ImageCapabilityChanged func() bool
	SystemPrompt           string
	// PinnedContextLoader snapshots the current session sidecar at turn
	// admission. The Agent persists changes as append-only user-role revisions.
	PinnedContextLoader PinnedContextLoader
	SessionDir          string
	SessionPath         string
	Host                *plugin.Host
	// MCPHostProfile is the surface lazily created hosts declare; injected
	// hosts keep their own profile.
	MCPHostProfile plugin.HostProfile
	Commands       []command.Command
	Skills         []skill.Skill
	AllSkills      []skill.Skill
	SkillStore     *skill.Store
	AllSkillStore  *skill.Store
	// DisableImplicitSkillInvocation controls model-facing discovery only;
	// explicit /skill commands and management remain host-side capabilities.
	DisableImplicitSkillInvocation bool
	// SkillRunner executes a runAs=subagent skill in an isolated child loop.
	// ReadOnlySkillRunner is reserved for explicitly read-only entry points;
	// Plan itself is a workflow instruction and uses SkillRunner with the shared
	// Permissions/Sandbox gate. SkillProfile supplies model/effort display
	// metadata for the synthetic top-level run_skill event.
	SkillRunner         skill.SubagentRunner
	ReadOnlySkillRunner skill.SubagentRunner
	SkillProfile        skill.ProfileResolver
	Hooks               *hook.Runner
	Memory              *memory.Set
	Cleanup             func()
	// BalanceURL/BalanceKey wire the active provider's optional wallet-balance
	// endpoint and bearer key; empty when the provider declares no balance_url.
	BalanceURL    string
	BalanceKey    string
	BalanceClient *http.Client
	// Jobs is the session-scoped background-job manager (nil disables background jobs).
	Jobs *jobs.Manager
	// TaskStore remains a FileStore-compatible authority. Desktop injects one
	// observed instance so recorder and task-control APIs share post-commit
	// projection hints; nil preserves the ordinary FileStore.
	TaskStore taskmonitor.WriteStore
	// WorkspaceLease is the Delivery writer owner shared with the executor.
	WorkspaceLease *workspacelease.Owner
	// Registry is the executor's live tool set, and PluginCtx the session-scoped
	// context; both are needed for hot-adding MCP servers via AddMCPServer.
	Registry  *tool.Registry
	PluginCtx context.Context
	// MCPDefaultCallTimeout is the global MCP call cap used by hot-connected
	// servers when they do not declare a server- or tool-specific override.
	MCPDefaultCallTimeout time.Duration
	// MCPConfigureSpec injects host-local launch and isolation policy into every
	// hot-connected server without persisting that state in project config.
	MCPConfigureSpec func(*plugin.Spec)
	// CapabilityRuntime is the controller-local authoritative MCP inventory used
	// by stable use_capability frontends. It shares Host processes with sibling
	// tabs but never shares their enabled/disabled state.
	CapabilityRuntime *agent.MCPCapabilityRuntime
	RuntimeGeneration uint64 // PublishGate generation for admission
	// RuntimeOwner isolates publish/drain gates and receipts to one
	// controller/session rebuild lineage. Nil preserves compatibility behavior.
	RuntimeOwner *extension.RuntimeOwner
	// WorkspaceRoot is the project root checkpoint restores are confined to ("" =
	// no confinement). Frontends pass the cwd they launched the session in.
	WorkspaceRoot          string
	ExternalFolderToolRefs externalFolderToolRefs
	// ResponseLanguage controls final-answer language preference. Empty/auto
	// means no transient injection because the stable language policy follows the
	// current user turn.
	ResponseLanguage string
	// ReasoningLanguage controls visible reasoning language preference. Empty/auto
	// means no transient injection because the stable language policy already
	// follows the conversation language.
	ReasoningLanguage string
	// SessionContextStatic carries boot-observed runtime facts that belong in a
	// host user-turn snapshot rather than the cache-stable system prompt. Only
	// Environment and Workspace are consumed; memory and skills stay live.
	SessionContextStatic sessioncontext.Sections
	// DisableColdResumePrune suppresses the cold-resume cache-state notice.
	// Resume never rewrites history regardless of this flag.
	DisableColdResumePrune bool
	// Shell is the interpreter user-invoked "!" commands run under, so /shell
	// matches the agent's configured [tools.shell] choice. Zero value = auto.
	Shell sandbox.Shell
	// OnRemember, when set, is invoked with a new allow rule the user chose to
	// persist to disk (e.g. "Bash(go test:*)"). The callback is wired into the
	// permission Gate on EnableInteractiveApproval.
	OnRemember func(rule string) RememberResult
	// OnRememberPlanModeReadOnlyCommand persists a bash command prefix as trusted
	// read-only when the user chooses "always allow" from the plan-mode trust
	// prompt.
	OnRememberPlanModeReadOnlyCommand func(prefix string) PlanModeReadOnlyCommandTrustResult
	// OnPersistWriteAccess writes sandbox.allow_write and an optional permission
	// rule to the workspace reasonix.toml as one transaction.
	OnPersistWriteAccess PersistWriteAccessFunc
	// WriteRoots is the session-scoped writable directory manager shared with
	// built-in file tools and bash.
	WriteRoots *sandbox.WritableRootSet
	// BashSandboxEnforced is true when this session's bash tool actually wraps
	// commands in an OS sandbox. Windows and bash=off leave this false so
	// directory prompts are not implied for unisolated shell writes.
	BashSandboxEnforced bool
	// SessionRecoveryMeta lets a frontend attach scope/topic/profile metadata to
	// an automatic recovery branch before it is written.
	SessionRecoveryMeta func(SessionRecoveryRequest) agent.BranchMeta
	// OnSessionRecovered is called after a stale runtime's transcript has been
	// saved as a recovery branch, before the controller commits to that branch.
	OnSessionRecovered func(SessionRecoveryInfo) error
	// OnSessionTransition transfers write ownership before an intentional
	// fork, branch, or switch publishes a different Session.
	OnSessionTransition func(SessionTransitionInfo) error
	// ApprovalTimeout bounds how long a tool-approval or ask prompt blocks waiting
	// for a user decision. Zero (default) waits forever — right for an interactive
	// terminal. Bot/headless frontends set a positive value so an unanswered
	// prompt can't wedge the session indefinitely (#4626, #4402).
	ApprovalTimeout time.Duration
	// Extensions is the frozen extension dispatcher for this controller
	// generation (Extension Protocol v2, stage 6b1). Nil means no v2 runtime
	// packages are installed: every extension wiring point takes an untouched
	// fast path. Boot installs it through SetExtensions because sidecars (and
	// therefore the dispatcher) only exist after snapshot assembly, which runs
	// after New.
	Extensions *dispatch.Dispatcher
	// ProviderResolver is the build's merged provider catalog — extension
	// sidecar providers folded over the config/broker base (stage 7). Nil when
	// no v2 runtime sidecar declared providers; ProviderCatalog then returns
	// nil and frontends enumerate providers from config alone, as before.
	ProviderResolver provider.Resolver
	// Ablation switches subsystems off for a benchmark arm. The zero value runs
	// everything.
	Ablation ablation.Set
	// SessionTemp is the logical-session private temporary directory manager
	// shared by sandboxed Bash calls. Nil creates a fresh Manager owned by this
	// Controller. Hot rebuilds pass the previous Controller's Manager so the
	// temporary directory survives model/settings swaps.
	SessionTemp *sessiontemp.Manager
}

// New builds a Controller. A nil Sink becomes event.Discard; unless the caller
// already provided a goalUsageTee (NewGoalUsageTee), the sink is wrapped in one
// so billable usage can be accounted to Goal budgets.
func controllerSessionTemp(existing *sessiontemp.Manager) *sessiontemp.Manager {
	if existing != nil {
		return existing
	}
	return sessiontemp.New()
}

func New(opts Options) *Controller {
	sink := opts.Sink
	if nilutil.IsNil(sink) {
		sink = event.Discard
	}
	usageTee, ok := sink.(*goalUsageTee)
	if !ok {
		usageTee = NewGoalUsageTee(sink).(*goalUsageTee)
		sink = usageTee
	}
	pluginCtx := opts.PluginCtx
	if pluginCtx == nil {
		pluginCtx = context.Background()
	}
	runtimeOwner := runtimeOwnerOrDefault(opts.RuntimeOwner)
	pluginCtx = extension.ContextWithRuntimeOwner(pluginCtx, runtimeOwner)
	if opts.Hooks != nil {
		opts.Hooks.SetSessionID(agent.BranchID(opts.SessionPath))
	}
	c := &Controller{
		taskBudget:                        opts.TaskBudget,
		goalTokenBudget:                   opts.GoalTokenBudget,
		goals:                             goalMachine{tokenBudget: opts.GoalTokenBudget},
		runner:                            opts.Runner,
		executor:                          opts.Executor,
		guardianSess:                      opts.Guardian,
		guardianPath:                      guardian.PathFor(opts.SessionPath),
		evaluator:                         opts.GoalEvaluator,
		goalUsageTee:                      usageTee,
		sink:                              sink,
		policy:                            opts.Policy,
		subagentGate:                      opts.SubagentGate,
		label:                             opts.Label,
		modelRef:                          opts.ModelRef,
		visionModel:                       strings.TrimSpace(opts.VisionModel),
		visionProviderResolver:            opts.VisionProviderResolver,
		visionModelSelector:               opts.VisionModelSelector,
		modelCapabilityResolver:           opts.ModelCapabilityResolver,
		frozenImageInput:                  opts.FrozenImageInput,
		imageCapabilityChanged:            opts.ImageCapabilityChanged,
		prompt:                            newControllerPromptState(opts.SystemPrompt, opts.Executor),
		pinnedContextLoader:               opts.PinnedContextLoader,
		sessionContextStatic:              opts.SessionContextStatic,
		sessionDir:                        opts.SessionDir,
		sessionPath:                       opts.SessionPath,
		commands:                          atomic.Pointer[[]command.Command]{},
		skills:                            newSkillSet(opts.Skills, opts.AllSkills, opts.SkillStore, opts.AllSkillStore),
		disableImplicitSkillInvocation:    opts.DisableImplicitSkillInvocation,
		skillRunner:                       opts.SkillRunner,
		readOnlySkillRunner:               opts.ReadOnlySkillRunner,
		skillProfile:                      opts.SkillProfile,
		hooks:                             opts.Hooks,
		memory:                            newMemoryManager(opts.Memory),
		cleanup:                           opts.Cleanup,
		responseLanguage:                  config.NormalizeLanguage(opts.ResponseLanguage),
		reasoningLanguage:                 config.NormalizeReasoningLanguage(opts.ReasoningLanguage),
		disableColdResumePrune:            opts.DisableColdResumePrune,
		shell:                             opts.Shell,
		onRemember:                        opts.OnRemember,
		onRememberPlanModeReadOnlyCommand: opts.OnRememberPlanModeReadOnlyCommand,
		writeAccess:                       newControllerWriteAccess(opts),
		sessionRecoveryMeta:               opts.SessionRecoveryMeta,
		onSessionRecovered:                opts.OnSessionRecovered,
		onSessionTransition:               opts.OnSessionTransition,
		balanceURL:                        opts.BalanceURL,
		balanceKey:                        opts.BalanceKey,
		balanceClient:                     opts.BalanceClient,
		jobs:                              opts.Jobs,
		workspaceLease:                    opts.WorkspaceLease,
		mcp:                               newMcpManager(opts.Host, opts.Registry, pluginCtx, opts.MCPHostProfile),
		mcpDefaultCallTimeout:             opts.MCPDefaultCallTimeout,
		mcpConfigureSpec:                  opts.MCPConfigureSpec,
		capabilityRuntime:                 opts.CapabilityRuntime,
		ablation:                          opts.Ablation,
		workspaceRoot:                     opts.WorkspaceRoot,
		externalFolderToolRefs:            opts.ExternalFolderToolRefs,
		providerResolver:                  opts.ProviderResolver,
		runtimeGeneration:                 opts.RuntimeGeneration,
		runtimeOwner:                      runtimeOwner,
		approval:                          newApprovalManager(opts.Policy, ToolApprovalAsk, opts.ApprovalTimeout),
	}
	// Session-private temporary directory: reuse a shared Manager on hot
	// rebuild, otherwise create one. Retain so ReleaseResources/Close drop the
	// owner reference without racing a replacement Controller.
	c.sessionTemp = controllerSessionTemp(opts.SessionTemp)
	c.sessionTemp.Retain()
	if strings.TrimSpace(opts.WorkspaceRoot) != "" {
		c.legacyResearchArchive = legacyResearchArchive{store: autoresearch.NewStore(opts.WorkspaceRoot)}
	}
	if opts.Extensions != nil {
		c.extensions = opts.Extensions
		c.sink = newFrontendEventSink(c.sink, opts.Extensions)
		if c.executor != nil {
			c.executor.SetExtensions(opts.Extensions)
		}
	}
	// Checkpoints: bind a store to the session and route writer pre-edits into it.
	c.rebindCheckpoints(opts.SessionPath)
	c.setActiveJobSession(opts.SessionPath)
	c.rebindInbox()
	// Observe Steer / unapplied-steer for durable inbox state transitions.
	// Must wrap both the controller sink and the executor sink: agent.Steer
	// emits on the executor path, TurnDone on the controller path.
	c.sink = &inboxEventSink{inner: newTurnEventSink(c.sink, c), c: c}
	if c.executor != nil {
		c.executor.SetSink(c.sink)
	}
	cmdsInit := opts.Commands
	c.commands.Store(&cmdsInit)
	if c.executor != nil {
		c.wireMutationObserver()
		c.executor.SetMemoryQueue(c)
	}
	// Auto Guard is built into Auto. Ask and YOLO bypass it through the mode
	// provider, so no separate enablement state is needed.
	c.initRecoveryGate(opts.RecoveryReviewer, opts.RecoveryHeadless)
	// Task monitoring: record background-job lifecycle into the project-local
	// task store so CLI, Desktop, scripts, and future clients observe the same
	// state/event evidence. The recorder swallows its own failures — monitoring
	// must never affect the agent pipeline. The session id is resolved lazily
	// because the session path is only fixed once the first turn begins.
	if c.jobs != nil && c.workspaceRoot != "" {
		taskStore := opts.TaskStore
		if taskStore == nil {
			taskStore = taskmonitor.NewFileStore(filepath.Join(".reasonix", "tasks"))
		}
		c.jobs.SetTaskRecorder(taskmonitor.NewTaskRecorder(
			taskStore,
			c.workspaceRoot,
			func() string { return c.parentSessionID() },
		))
	}
	return c
}

// SetDisplayRecorder installs an optional hook used by frontends that persist a
// shorter user-facing transcript than the fully composed model prompt.
func (c *Controller) SetDisplayRecorder(fn func(content, display string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.displayRecorder = fn
}

// SetExtensions installs the extension dispatcher after construction. Boot
// uses it because sidecars — and therefore the dispatcher — only exist after
// snapshot assembly, which runs after New. First non-nil install wins for the
// cold-start path; use ReplaceExtensions for generation-safe rebuild swaps.
// Nil is a no-op. The executor agent receives the same dispatcher (stage 6b2).
func (c *Controller) SetExtensions(d *dispatch.Dispatcher) {
	if d == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.extensions != nil {
		return
	}
	c.installExtensionsLocked(d)
}

// ReplaceExtensions atomically swaps the dispatcher for a reused controller
// after a narrow rebuild. Updates sink strategy owner and executor together.
func (c *Controller) ReplaceExtensions(d *dispatch.Dispatcher) {
	if c == nil || d == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.installExtensionsLocked(d)
}

func (c *Controller) installExtensionsLocked(d *dispatch.Dispatcher) {
	c.extensions = d
	// Keep the inbox observer as the outermost sink so Steer/unapplied events
	// always update durable state, while still installing/updating the
	// frontendEventSink wrapper underneath for extension rulings.
	switch sink := c.sink.(type) {
	case *inboxEventSink:
		if lifecycle, ok := sink.inner.(*turnEventSink); ok {
			current := lifecycle.innerSnapshot()
			if existing, ok := current.(*frontendEventSink); ok {
				existing.setDispatcher(d)
			} else {
				lifecycle.setInner(newFrontendEventSink(current, d))
			}
		} else if existing, ok := sink.inner.(*frontendEventSink); ok {
			existing.setDispatcher(d)
		} else {
			sink.inner = newTurnEventSink(newFrontendEventSink(sink.inner, d), c)
		}
	case *frontendEventSink:
		sink.setDispatcher(d)
		// Ensure inbox observer stays outer.
		c.sink = &inboxEventSink{inner: newTurnEventSink(sink, c), c: c}
	default:
		c.sink = &inboxEventSink{inner: newTurnEventSink(newFrontendEventSink(c.sink, d), c), c: c}
	}
	if c.executor != nil {
		c.executor.SetExtensions(d)
		c.executor.SetSink(c.sink)
	}
}

// SetProviderResolver replaces the session's merged provider catalog (narrow
// rebuild after sidecar Manager roll). Nil clears extension-hosted providers.
func (c *Controller) SetProviderResolver(r provider.Resolver) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.providerResolver = r
	c.mu.Unlock()
}

// SetOnSessionRecovered installs the ownership handoff invoked before the
// controller commits to an automatically created recovery branch. Frontends
// that acquire their session owner after controller construction (for example
// reasonix serve) use this before publishing the controller.
func (c *Controller) SetOnSessionRecovered(fn func(SessionRecoveryInfo) error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onSessionRecovered = fn
}

func (c *Controller) sessionRecoveredHandler() func(SessionRecoveryInfo) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onSessionRecovered
}

func (c *Controller) recordDisplay(content, display string) {
	if strings.TrimSpace(display) == "" || content == display {
		return
	}
	c.mu.Lock()
	record := c.displayRecorder
	c.mu.Unlock()
	if record != nil {
		record(content, display)
	}
}

// ToolContractEntries returns a stable snapshot of the executor's live tool
// contract: provider-visible names, descriptions, canonical schemas, and
// read-only flags. It is intended for diagnostics and regression tests.
func (c *Controller) ToolContractEntries() []tool.ContractEntry {
	if c == nil {
		return nil
	}
	reg := c.mcp.registry()
	if reg == nil {
		return nil
	}
	return reg.ContractEntries()
}

// AllToolContractEntries returns every registered tool, including those hidden
// from the provider-visible schema and only reachable via use_capability.
func (c *Controller) AllToolContractEntries() []tool.ContractEntry {
	if c == nil {
		return nil
	}
	reg := c.mcp.registry()
	if reg == nil {
		return nil
	}
	return reg.AllContractEntries()
}

// ProviderCatalog returns the session's merged provider catalog: the config
// (or broker) base plus every provider a live extension sidecar declared,
// keyed by ref — extension refs carry their plugin/<plugin>/<provider>/<model>
// namespace. Nil when no sidecar declared providers, so frontends can tell
// "enumerate config only" apart from "the extension catalog is empty".
func (c *Controller) ProviderCatalog() []provider.Descriptor {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	r := c.providerResolver
	c.mu.Unlock()
	if r == nil {
		return nil
	}
	return r.Catalog()
}

func (c *Controller) recordDisplayForNewUser(startMessages int, display string) {
	if strings.TrimSpace(display) == "" {
		return
	}
	msgs := c.History()
	if startMessages > len(msgs) {
		startMessages = len(msgs)
	}
	for _, m := range msgs[startMessages:] {
		if agent.IsUserAuthoredTurnMessage(m) {
			c.recordDisplay(m.Content, display)
			return
		}
	}
}

func (c *Controller) markEditedForNewUser(startMessages int, original string) {
	if strings.TrimSpace(original) == "" || c.executor == nil {
		return
	}
	s := c.executor.Session()
	msgs := s.Snapshot()
	if startMessages > len(msgs) {
		startMessages = len(msgs)
	}
	for i := startMessages; i < len(msgs); i++ {
		if !agent.IsUserAuthoredTurnMessage(msgs[i]) {
			continue
		}
		if agent.UserMessageText(msgs[i]) == original {
			return
		}
		msgs[i].Edited = true
		msgs[i].Original = original
		// A periodic autosave may already contain this user message without its
		// local edit metadata. Classify the mutation atomically so the turn-end
		// save performs an owned rewrite instead of forking a bogus
		// same-revision recovery branch. Edited/Original are local-only display
		// metadata (provider requests ignore them), so this must not report a
		// cache-prefix change — ReplaceLocalMetadata, not Rewrite.
		s.ReplaceLocalMetadata(msgs)
		return
	}
}

// ckptDir derives a session's checkpoint directory from its file path
// (…/<id>.jsonl → …/<id>.ckpt). Empty path → empty (in-memory checkpoints).
func ckptDir(sessionPath string) string {
	return store.SessionCheckpointDir(sessionPath)
}

// rebindCheckpoints points the store at the (possibly new) session, loading any
// checkpoints already on disk, and resets the turn boundaries. Called on
// construction and whenever the session path changes (NewSession/Resume/SetSessionPath).
// Also re-wires the mutation observer so capture targets the new store.
func (c *Controller) rebindCheckpoints(sessionPath string) {
	c.goals.setStatePath(goalStatePath(sessionPath))
	c.checkpoints.rebind(ckptDir(sessionPath), c.workspaceRoot)
	c.rebindTurnEvents(sessionPath)
	if c.executor != nil {
		c.wireMutationObserver()
	}
}

// commands (frontend → controller)

// spawnGuardedTurn launches an admitted turn body plus its autosave companion.
// The caller must already have claimed admission (running=true) under c.mu.
func (c *Controller) spawnGuardedTurn(ctx context.Context, cancel context.CancelFunc, body func(ctx context.Context) error) {
	body = c.prepareTurnAdmission(body)
	ctx, completion := withGuardedTurnCompletion(ctx)
	c.autosaveWG.Go(func() {
		c.autosaveWhileRunning(ctx)
	})
	go func() {
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				c.finishGuardedTurn(fmt.Errorf("internal error: %v", r), completion)
			}
		}()
		err := body(ctx)
		c.finishGuardedTurn(explainError(err), completion)
	}()
}

// finishGuardedTurn keeps admission closed while TurnDone is delivered. The
// sink fan-out may detach per-turn transports; allowing a replacement turn in
// after running=false but before that fan-out completed let the old completion
// clear or inherit the replacement turn's transport.
//
// When the window closes, the oldest parked turn (if any) is started under the
// SAME critical section that clears finishing: opening the gate first and then
// re-admitting would let an unrelated submit slip in ahead and bounce the
// parked turn back to a drop. Remaining parked turns drain one per
// finishGuardedTurn, preserving FIFO order. Rotation cannot interleave here:
// beginRotation refuses while running or finishing, and the drain flips
// finishing directly into running.
func (c *Controller) finishGuardedTurn(err error, completion *guardedTurnCompletion) {
	c.memory.clearAutoRemember()
	c.mu.Lock()
	cancelRequested := c.canceling
	c.running = false
	// A live controller keeps admission closed until TurnDone fan-out finishes.
	// Close has already sealed admission permanently, so a late completion must
	// not resurrect a finishing state after teardown.
	c.finishing = !c.closed
	c.finishingBoundary.begin(c.finishing)
	c.cancel = nil
	// Keep cancelling visible through TurnDone fan-out; clearing it here creates
	// a finishing-only window before Stop reaches its durable terminal event.
	// A closed controller has no live surface and may clear immediately.
	if c.closed {
		c.canceling = false
	}
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.finishing = false
		c.canceling = false
		c.finishingBoundary.end()
		if c.closed {
			c.mu.Unlock()
			return
		}
		if len(c.parkedTurns) == 0 {
			c.mu.Unlock()
			// No parked compatibility body: admit the next durable inbox item.
			c.maybeDispatchInbox()
			return
		}
		next := c.parkedTurns[0]
		c.parkedTurns = c.parkedTurns[1:]
		ctx, cancel := context.WithCancel(extension.ContextWithRuntimeOwner(context.Background(), c.runtimeOwner))
		c.cancel = cancel
		c.running = true
		c.canceling = false
		c.mu.Unlock()
		c.spawnGuardedTurn(ctx, cancel, next)
	}()
	c.inbox.mu.Lock()
	// Prefer a single representative id for the wire event (first active).
	// Full multi-item ack happens in onInboxTurnDone via activeItemIDs.
	activeInboxID := ""
	for id := range c.inbox.activeItemIDs {
		activeInboxID = id
		break
	}
	c.inbox.mu.Unlock()
	done := event.Event{
		Kind:           event.TurnDone,
		Err:            err,
		Cancelled:      cancelRequested,
		Outcome:        turnOutcome(err),
		CheckpointTurn: c.validatedCheckpointTurn(completion),
		Receipt:        c.executor.CompletionReceipt(),
		ItemID:         activeInboxID,
	}
	done = c.applyTurnDoneProtocol(done, cancelRequested)
	done.Diagnostic = provider.DiagnoseFailure(err)
	if pending := c.executor.PendingInterruptedRecovery(); pending != nil && pending.Pending {
		done.Recovery = &event.RecoveryStatus{
			Phase:        "turn_recovery_required",
			Reason:       pending.Cause,
			TurnID:       pending.TurnID,
			AttemptID:    pending.AttemptID,
			RequiresUser: pending.RequiresUserDecision || len(pending.UnknownTools) > 0,
			Silent:       pending.SilentInterruption,
		}
	}
	if !cancelRequested {
		done.ProtocolRecovery = c.executor.PendingProtocolRecovery()
	}
	var readinessErr *agent.FinalReadinessError
	if errors.As(err, &readinessErr) {
		done.Readiness = &event.FinalReadiness{Attempts: readinessErr.Attempts, Missing: append([]string(nil), readinessErr.Missing...)}
	}
	// Ack active durable items before exposing TurnDone. Frontends commonly
	// refresh the inbox from that event and must not observe already-consumed
	// steers in the completed turn. Dispatch still waits for finishing to clear.
	c.onInboxTurnDone()
	c.sink.Emit(done)
}

// Send starts a turn with an uncomposed message. The controller applies
// plan-mode, memory, and background-job framing inside the async turn path.
func (c *Controller) Send(input string) {
	c.SendWithRaw(input, input)
}

// SendWithRaw starts a turn with separate model input and raw prompt text.
func (c *Controller) SendWithRaw(input, raw string) {
	c.runGuarded(func(ctx context.Context) error { return c.runGoalLoopWithRaw(ctx, input, raw) })
}

// planApprovalTool is the Tool name on the ApprovalRequest the controller emits
// to gate a proposed plan. Frontends key their plan-approval UI on it (the
// desktop renders a plan card; the chat TUI a plan banner).
const planApprovalTool = "exit_plan_mode"

// PlanDecisionAction preserves the three user-owned meanings of the Plan card.
// Revise and exit both deny execution at the approval gate, but they are not the
// same product decision and must remain distinguishable in durable receipts.
type PlanDecisionAction string

const (
	PlanDecisionStartExecution PlanDecisionAction = "start_execution"
	PlanDecisionRevisePlan     PlanDecisionAction = "revise_plan"
	PlanDecisionExitPlan       PlanDecisionAction = "exit_plan"
)

// SandboxEscapeApprovalTool is the internal Tool name used for one-shot approval
// to rerun a shell command without the OS sandbox after the sandbox failed.
const SandboxEscapeApprovalTool = "sandbox_escape"

// ManagedConfigWriteApprovalTool is the internal Tool name used for per-write
// approval when a file tool targets a Reasonix-managed config file outside the
// workspace write roots. It is a fresh human decision: config files control
// providers, sandbox rules, permissions, and MCP servers for future sessions,
// so YOLO/auto approval must never answer it.
const ManagedConfigWriteApprovalTool = "config_write"

// planApprovedMessage is the follow-up turn sent once the user approves a plan —
// the in-context nudge to execute and keep the (already-seeded) task list honest.
const planApprovedMessage = "Plan approved — plan mode is off. Implement the plan now. The ordinary writer fallback is approved for this execution turn; explicit ask/deny rules and forced fresh reviews still apply. Use this workflow: 1) mark the first sub-step in_progress with todo_write (this establishes the task list); 2) execute the sub-step; 3) call complete_step with evidence — the host then marks that sub-step completed and moves the next one to in_progress for you. You may call complete_step for multiple sub-steps in one tool-call round when their work and evidence already exist, but keep calls in Todo order; the host processes them sequentially and rejects skipped or out-of-order sign-offs. You don’t need another todo_write to mark steps completed; each complete_step advances the list."

// runTurn runs one model turn, then applies the plan-approval gate. This is the
// single, frontend-agnostic plan flow: in Plan the model is instructed to
// research and write its plan as a normal answer, while any tool calls still use
// the active Permissions/Sandbox path.
// When the turn ends with a text proposal, the controller asks the user to
// approve (reusing the ApprovalRequest channel both frontends already render);
// on approval it exits plan mode, seeds the task list from the plan, and
// continues straight into execution; on rejection it stays in plan mode so the
// next turn can revise. Plan mode is only ever set interactively, so the headless
// `Run` path (which doesn't call this) never blocks on a prompt.
func (c *Controller) runTurn(ctx context.Context, input string) error {
	return c.runGoalLoopWithRaw(ctx, input, input)
}

// RunTurn executes one foreground turn synchronously through the same lifecycle
// used by interactive frontends: transient memory/background-job
// composition, checkpoints, hooks, and plan approval. It is for transports that
// need a blocking request/response boundary, such as ACP session/prompt.
func (c *Controller) RunTurn(ctx context.Context, input string) error {
	return c.runSynchronousTurn(ctx, nil, func(runCtx context.Context) error {
		return c.runTurn(runCtx, input)
	})
}

func (c *Controller) runTurnWithRaw(ctx context.Context, input, raw string) error {
	return c.runTurnWithRawDisplay(ctx, input, raw, "")
}

func (c *Controller) runGoalLoopWithRaw(ctx context.Context, input, raw string) error {
	return c.runGoalLoopWithRawDisplay(ctx, input, raw, "")
}

// withTurnFormat binds a structured-output format to the turn context
// (empty is a no-op). Extracted from the runGoalLoop closure so tests can
// assert the format actually reaches the agent request path.
func (c *Controller) withTurnFormat(ctx context.Context, format string) context.Context {
	if format == "" {
		return ctx
	}
	return agent.WithResponseFormat(ctx, format)
}

func (c *Controller) runGoalLoopWithRawDisplay(ctx context.Context, input, raw, display string) error {
	// Structured-output format is bound to the submitted turn (passed via
	// submitHTTPWithFormat → submitCommandOrTurn → runGoalLoop closure);
	// no global one-shot slot to race across concurrent requests.
	return newTurnOrchestrator(c).runGoalLoopWithRawDisplay(ctx, input, raw, display)
}

func (c *Controller) runEditedGoalLoopWithRawDisplay(ctx context.Context, input, raw, display, original string) error {
	return newTurnOrchestrator(c).runEditedGoalLoopWithRawDisplay(ctx, input, raw, display, original)
}

func (c *Controller) runTurnWithRawDisplay(ctx context.Context, input, raw, display string) error {
	return newTurnOrchestrator(c).runTurnWithRawDisplay(ctx, input, raw, display)
}

func (c *Controller) runSubagentSkillSlash(sk skill.Skill, task, raw, display string) {
	sk = c.skills.prepare(sk)
	c.runGuarded(func(ctx context.Context) error {
		planMode := c.PlanMode()
		runner := c.skillRunner
		if runner == nil {
			return fmt.Errorf("subagent skill runner is unavailable for /%s", sk.Name)
		}
		return newTurnOrchestrator(c).runSubagentSkillGoalLoop(ctx, sk, task, raw, display, runner, planMode)
	})
}

func (c *Controller) stopGoal(status string) {
	path, data, ok := c.goals.stop(status, c.goalTodos())
	c.persistGoalState(path, data, ok)
}

// lastAssistantText returns the content of the most recent assistant message with
// non-empty text — the model's final answer for the turn (its plan, in plan mode).
func lastAssistantText(msgs []provider.Message) string {
	for _, msg := range slices.Backward(msgs) {
		if msg.Role == provider.RoleAssistant && strings.TrimSpace(msg.Content) != "" {
			return msg.Content
		}
	}
	return ""
}

// Submit is the one-call entry for a simple frontend: it takes raw user input
// and does everything — slash-command dispatch, @-reference expansion, plan-mode
// composition — emitting all output as events. The HTTP/SSE server uses this so
// a browser client only POSTs the typed line.
//
// Slash commands route to the matching primitive: /compact, /new, and /clear
// run their session op and emit a Notice; /mcp__server__prompt and custom /commands
// resolve to a turn; an unknown slash emits a Notice. Anything else is a normal
// turn with its @-references resolved first.
func (c *Controller) Submit(input string) {
	c.submit(input, "", "")
}

// SubmitHTTP accepts input from the unauthenticated localhost HTTP frontend. It
// deliberately omits the trusted TUI-only "!cmd" shell shortcut and resolves file
// references only through the controller's workspace root.
func (c *Controller) SubmitHTTP(input string) {
	c.submitHTTP(input, "")
}

// SubmitHTTPFormat is SubmitHTTP with an optional structured-output format
// ("json_object") applied to the turn's completion requests. Empty format
// behaves exactly like SubmitHTTP. A format attached to a slash command,
// or other non-turn input is discarded; @reference turns preserve it because
// the format is bound to every submitted turn rather than a global slot.
func (c *Controller) SubmitHTTPFormat(input, format string) {
	// format 绑定到本次提交的 turn（随请求参数传递），不再写入 Controller
	// 全局一次性槽——评审 #7234 第 2 点：全局槽存在跨请求串用的逻辑竞态
	// （后提交的 JSON 请求先写槽，更早的普通请求先启动消费掉）。
	f := strings.TrimSpace(format)
	if f != "" && isNonTurnHTTPInput(input) {
		f = "" // 非 turn 输入（slash 命令/! 前缀）不携带 format
	}
	// @ 引用 turn（FileRefLine/SlashPathLineRef 等）同样绑定 format——
	// runRefTurnWithFormat 族 wrapper 注入 ctx（review fix7234and7168：
	// format 是每个被接纳 turn 的属性，统一架构）。
	c.submitHTTPWithFormat(input, "", f)
}

// isNonTurnHTTPInput reports inputs that never reach the agent turn loop, so a
// structured-output request attached to them would otherwise leak into the
// next real turn (the format slot is consumed only by runGoalLoopWithRawDisplay).
func isNonTurnHTTPInput(input string) bool {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return true
	}
	// Memory quick-add / remember shortcuts and goal commands bypass turns.
	if _, ok := MemoryQuickAddNote(trimmed); ok {
		return true
	}
	if _, ok := RememberCommandNote(trimmed); ok {
		return true
	}
	// "!" shell commands are rejected by submitHTTP before the turn loop
	// (403 over HTTP); a format attached to them would never be consumed.
	if strings.HasPrefix(trimmed, "!") {
		return true
	}
	// Slash commands are management verbs (/compact /new /clear /model ...)
	// or notices, not completion turns.
	if strings.HasPrefix(trimmed, "/") {
		return true
	}
	return false
}

// SubmitDisplay runs input as a turn while remembering the user-facing display
// text for transcript replay when controller-side composition expands input.
func (c *Controller) SubmitDisplay(display, input string) {
	c.submit(input, display, "")
}

// SubmitInvocationDisplay executes composer-selected invocation entities
// independently of slash-command parsing. Plain string submit entry points keep
// their existing behavior for CLI, HTTP, and backward-compatible clients.
func (c *Controller) SubmitInvocationDisplay(display, input string, invocations []InvocationRequest) {
	c.submitInvocations(input, display, invocations)
}

func (c *Controller) submitInvocations(input, display string, requests []InvocationRequest) {
	if len(requests) == 0 {
		c.SubmitDisplay(display, input)
		return
	}
	prepared, err := c.prepareInvocationTurn(input, requests)
	if err != nil {
		c.notice(err.Error())
		return
	}
	c.runGuarded(func(ctx context.Context) error {
		return c.runPreparedInvocationTurn(ctx, prepared, input, input, display, nil)
	})
}

type preparedInvocationTurn struct {
	composed  string
	subagents []skill.Skill
}

func (c *Controller) prepareInvocationTurn(input string, requests []InvocationRequest) (preparedInvocationTurn, error) {
	ordered := append([]InvocationRequest(nil), requests...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Offset < ordered[j].Offset })
	inline := make([]skill.Skill, 0, len(ordered))
	subagents := make([]skill.Skill, 0, len(ordered))
	for _, request := range ordered {
		sk, _, ok := c.resolveSkillInvocation("/" + strings.TrimSpace(request.Name))
		if !ok {
			return preparedInvocationTurn{}, fmt.Errorf("unknown invocation: /%s", strings.TrimSpace(request.Name))
		}
		kind := "skill"
		if sk.RunAs == skill.RunSubagent {
			kind = "subagent"
		}
		if strings.TrimSpace(request.Kind) != "" && request.Kind != kind {
			return preparedInvocationTurn{}, fmt.Errorf("invocation /%s is %s, not %s", sk.SlashName(), kind, request.Kind)
		}
		if sk.RunAs == skill.RunSubagent {
			subagents = append(subagents, sk)
		} else {
			inline = append(inline, sk)
		}
	}

	parts := make([]string, 0, len(inline)+1)
	for _, sk := range inline {
		parts = append(parts, c.skills.render(sk, ""))
	}
	if strings.TrimSpace(input) != "" {
		parts = append(parts, input)
	}
	composed := strings.Join(parts, "\n\n")
	if strings.TrimSpace(input) == "" {
		if len(subagents) > 0 {
			return preparedInvocationTurn{}, fmt.Errorf("subagent invocation requires a task")
		}
	}
	return preparedInvocationTurn{composed: composed, subagents: subagents}, nil
}

func (c *Controller) runPreparedInvocationTurn(
	ctx context.Context,
	prepared preparedInvocationTurn,
	input, raw, display string,
	frozenImages []string,
) error {
	if len(prepared.subagents) == 0 {
		return c.runGoalLoopWithFrozenImagesRawDisplay(ctx, prepared.composed, raw, display, frozenImages)
	}
	runner := c.skillRunner
	if runner == nil {
		return fmt.Errorf("subagent skill runner is unavailable")
	}
	return newTurnOrchestrator(c).runSubagentSkillTurnsGoalLoop(
		ctx,
		prepared.subagents,
		prepared.composed,
		input,
		display,
		runner,
		c.PlanMode(),
	)
}

// SubmitEditedDisplay is SubmitDisplay for an inline-edited prompt. The model
// sees input; the saved user message also keeps the pre-edit prompt as local UI
// metadata so the edit survives session rewrites.
func (c *Controller) SubmitEditedDisplay(display, input, original string) {
	c.submit(input, display, original)
}

// SubmitUserTurn starts a normal model turn without interpreting shell or slash
// commands. It still resolves references, so callers can submit trusted
// user-authored prompt text without expanding the command surface.
func (c *Controller) SubmitUserTurn(input, display string) {
	c.runRefTurn(input, display)
}

func (c *Controller) submit(input, display, editedOriginal string) {
	trimmed := strings.TrimSpace(input)
	if note, ok := MemoryQuickAddNote(trimmed); ok {
		c.rememberProjectNote(note)
		return
	}
	if note, ok := RememberCommandNote(trimmed); ok {
		c.rememberProjectNote(note)
		return
	}
	if c.applyGoalCommand(trimmed, display) {
		return
	}
	if strings.HasPrefix(trimmed, "!") {
		c.RunShell(trimmed[1:])
		return
	}
	c.submitCommandOrTurn(trimmed, input, display, false, editedOriginal, "")
}

func (c *Controller) submitHTTP(input, display string) {
	c.submitHTTPWithFormat(input, display, "")
}

func (c *Controller) submitHTTPWithFormat(input, display, format string) {
	trimmed := strings.TrimSpace(input)
	if note, ok := MemoryQuickAddNote(trimmed); ok {
		c.rememberProjectNote(note)
		return
	}
	if note, ok := RememberCommandNote(trimmed); ok {
		c.rememberProjectNote(note)
		return
	}
	if c.applyGoalCommand(trimmed, display) {
		return
	}
	if strings.HasPrefix(trimmed, "!") {
		c.notice("shell commands are unavailable from this frontend")
		return
	}
	c.submitCommandOrTurn(trimmed, input, display, true, "", format)
}

func (c *Controller) submitCommandOrTurnReady(trimmed, input, display string, scopedRefsOnly bool, editedOriginal, format string) {
	runRefTurn := func(input, display string) {
		c.runRefTurnWithFormat(input, display, format)
	}
	runRefTurnWithRefs := func(input, refLine, display string) {
		c.runRefTurnWithRefsFormat(input, refLine, display, format)
	}
	runGoalLoop := func(ctx context.Context, input, raw, display string) error {
		return c.runGoalLoopWithRawDisplay(c.withTurnFormat(ctx, format), input, raw, display)
	}
	if scopedRefsOnly {
		runRefTurn = func(input, display string) {
			c.runScopedRefTurnWithFormat(input, display, format)
		}
		runRefTurnWithRefs = func(input, refLine, display string) {
			c.runScopedRefTurnWithRefsFormat(input, refLine, display, format)
		}
	}
	if strings.TrimSpace(editedOriginal) != "" {
		runRefTurn = func(input, display string) {
			c.runEditedRefTurnWithFormat(input, display, editedOriginal, format)
		}
		runRefTurnWithRefs = func(input, refLine, display string) {
			c.runEditedRefTurnWithRefsFormat(input, refLine, display, editedOriginal, format)
		}
		runGoalLoop = func(ctx context.Context, input, raw, display string) error {
			return c.runEditedGoalLoopWithRawDisplay(ctx, input, raw, display, editedOriginal)
		}
	}
	if id, guidance, ok := ParseProtocolRecoveryCommand(trimmed); ok {
		c.SubmitProtocolRecovery(id, guidance)
		return
	}
	if c.submitFinalReadinessCommand(trimmed, display) {
		return
	}
	switch {
	case trimmed == "/compact" || strings.HasPrefix(trimmed, "/compact "):
		focus := strings.TrimSpace(strings.TrimPrefix(trimmed, "/compact"))
		go func() {
			if err := c.Compact(context.Background(), focus); err != nil {
				c.notice("compaction failed: " + err.Error())
			} else {
				c.notice("compacted")
				if err := c.SnapshotRewrite(); err != nil {
					slog.Warn("controller: snapshot after compact", "err", err)
				}
			}
		}()
	case trimmed == "/context":
		c.noticeDetail(c.ContextReport())
	case trimmed == "/new":
		c.runSessionVerb(c.NewSession, "new session", "new session failed: ")
	case trimmed == "/clear":
		c.runSessionVerb(c.ClearSession, "context cleared", "clear context failed: ")
	case strings.HasPrefix(trimmed, "/mcp__"):
		c.runGuarded(func(ctx context.Context) error {
			sent, found, err := c.MCPPrompt(ctx, trimmed)
			if err != nil {
				return err
			}
			if !found {
				c.notice("unknown command: " + trimmed)
				return nil
			}
			return runGoalLoop(ctx, sent, sent, display)
		})
	case SlashCodeCommentLine(trimmed):
		// Slash-prefixed code comments are prompt text, not slash commands.
		runRefTurn(input, display)
	case strings.HasPrefix(trimmed, "/"):
		if ref, ok := FileRefLine(trimmed); ok {
			runRefTurn(ref, display)
			return
		}
		if ref, ok := SlashPathLineRef(trimmed, c.workspaceRoot); ok {
			runRefTurnWithRefs(input, ref, display)
			return
		}
		if SlashPathLikeLine(trimmed) {
			runRefTurn(input, display)
			return
		}
		// Management verbs (/model /memory /skills /hooks /mcp) emit a Notice, so
		// Submit-based frontends (desktop, HTTP) get them with no extra wiring.
		// The chat TUI handles these itself with richer output.
		fields := strings.Fields(trimmed)
		switch fields[0] {
		case "/tree":
			c.notice(c.BranchTreeText())
			return
		case "/branch":
			args := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
			if turn, name, fromTurn, err := ParseBranchTarget(args); err != nil {
				c.notice(err.Error())
			} else if fromTurn {
				if _, err := c.ForkNamed(turn-1, name); err != nil {
					c.notice(err.Error())
				}
			} else {
				if _, err := c.Branch(name); err != nil {
					c.notice(err.Error())
				}
			}
			return
		case "/switch":
			ref := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
			if _, err := c.SwitchBranch(ref); err != nil {
				c.notice(err.Error())
			}
			return
		case "/rewind":
			args := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
			turn, scope, err := parseRewind(args, c.Checkpoints())
			if err != nil {
				c.notice("usage: /rewind [turn] [code|conversation|both]")
				return
			}
			if err := c.Rewind(turn, scope); err != nil {
				c.notice(err.Error())
			}
			return
		case "/plan-exec":
			c.applyPlanExec(trimmed, display)
			return
		case "/prometheus":
			c.applyPrometheus(trimmed, display)
			return
		}
		if c.managementNotice(trimmed) {
			return
		}
		if IsBuiltinDocsSlash(fields[0], c.Commands(), c.SlashSkills()) {
			query := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
			if query == "" {
				text, err := DocsCommandOverviewFor(fields[0])
				if err != nil {
					c.notice("docs: " + err.Error())
				} else {
					c.notice(text)
				}
				return
			}
			c.runGuarded(func(ctx context.Context) error {
				sent, err := docsCommandPrompt(ctx, query)
				if err != nil {
					return fmt.Errorf("docs: %w", err)
				}
				return runGoalLoop(ctx, sent, sent, display)
			})
			return
		}
		// A custom command wins over a skill of the same name; both resolve to a
		// turn. Built-ins and their explicit Reasonix namespace are handled above.
		if sent, ok := c.CustomCommand(trimmed); ok {
			c.runGuarded(func(ctx context.Context) error {
				return runGoalLoop(ctx, sent, sent, display)
			})
			return
		}
		if sk, task, ok := c.resolveSkillInvocation(trimmed); ok {
			if sk.RunAs == skill.RunSubagent {
				if strings.TrimSpace(task) == "" {
					c.notice("usage: /" + sk.Name + " <task>")
					return
				}
				c.runSubagentSkillSlash(sk, task, trimmed, display)
				return
			}
			sent := c.skills.render(sk, task)
			c.runGuarded(func(ctx context.Context) error {
				return runGoalLoop(ctx, sent, sent, display)
			})
			return
		}
		// Unknown slash input is prose more often than a typo ("/etc/hosts
		// looks wrong", pasted paths, half-remembered commands) — send it as a
		// regular message instead of dead-ending the submission, with a notice
		// so real typos are still visible (#5756).
		c.notice("unknown command: " + trimmed + " — sent as a regular message")
		runRefTurn(input, display)
	default:
		runRefTurn(input, display)
	}
}

func (c *Controller) rememberProjectNote(note string) {
	if note == "" {
		c.notice("nothing to remember")
		return
	}
	if path, err := c.QuickAdd(memory.ScopeProject, note); err != nil {
		c.notice("memory: " + err.Error())
	} else {
		c.notice("remembered → " + path)
	}
}

func (c *Controller) applyGoalCommand(input, display string) bool {
	cmd, ok := ParseGoalCommand(input)
	if !ok {
		return false
	}
	if cmd.DeprecatedBudgetFlag {
		c.notice(GoalBudgetFlagDeprecatedNotice)
	}
	switch cmd.Action {
	case GoalCommandSet:
		c.SetPlanMode(false)
		c.SetGoalWithResearchMode(cmd.Text, cmd.ResearchMode)
		c.GoalStrict(cmd.Strict)
		c.startGoalCommandTurn(cmd, display)
	case GoalCommandClear:
		c.ClearGoal()
		c.notice(i18n.M.GoalCleared)
	case GoalCommandPause:
		if !c.PauseGoal() {
			c.notice(i18n.M.GoalNotRunning)
		}
	case GoalCommandResume:
		if !c.ResumeGoal() {
			c.notice(i18n.M.GoalNotPaused)
		}
	default:
		goal := c.Goal()
		if strings.TrimSpace(goal) == "" {
			c.notice(i18n.M.GoalEmpty)
			break
		}
		rt := c.GoalRuntime()
		c.notice(fmt.Sprintf(i18n.M.GoalCurrentFmt, goal))
		c.notice(fmt.Sprintf(i18n.M.GoalRuntimeFmt,
			rt.TurnsUsed, rt.RequestsUsed, rt.TokensUsed,
			GoalWorkDurationText(rt.WorkDurationMs)))
		if rt.LastReason != "" {
			c.noticeDetail(i18n.M.GoalRuntimeLastReason, rt.LastReason)
		}
		if rt.StopCause != "" {
			c.notice(fmt.Sprintf(i18n.M.GoalPausedFmt, rt.StopCause))
		}
	}
	return true
}

// applyPlanExec reads the current canonical todo list and starts a goal that
// analyzes and dispatches independent steps concurrently via parallel_tasks.
// Supports --strict flag: /plan-exec --strict enables strict goal mode.
func (c *Controller) applyPlanExec(input, display string) {
	todos := c.executor.CanonicalTodoState()
	if len(todos) == 0 {
		c.notice("no active plan with todos to execute")
		return
	}

	// Parse --strict flag.
	strict := slices.Contains(strings.Fields(input), "--strict")

	// Count completion status.
	total := len(todos)
	done := 0
	for _, t := range todos {
		if t.Status == "completed" {
			done++
		}
	}

	var b strings.Builder
	b.WriteString("You are the execution conductor. Route each step to the right sub-agent by module.\n\n")

	// Detect project structure for module-aware routing.
	modules := c.detectProjectModules()
	if len(modules) > 0 {
		b.WriteString("## Project modules detected\n\n")
		for _, m := range modules {
			fmt.Fprintf(&b, "- %s/", m)
		}
		b.WriteString("\n\nRoute steps to the module they belong to. Steps in different modules can run in parallel.\n\n")
	}

	b.WriteString("## Plan steps\n\n")
	for _, t := range todos {
		status := t.Status
		if status == "" {
			status = "pending"
		}
		mark := " "
		if status == "completed" {
			mark = "x"
		}
		fmt.Fprintf(&b, "- [%s] %s (%s)\n", mark, t.Content, status)
	}
	b.WriteString("\n## Routing rules\n")
	b.WriteString("1. Group steps by MODULE \u2014 same module = serial, different modules = parallel batches\n")
	b.WriteString("2. Research/exploration across modules = use parallel_tasks\n")
	b.WriteString("3. Dispatch each batch via parallel_tasks \u2014 each sub-agent gets one module\u2019s context\n")
	b.WriteString("4. Verify each batch before the next\n")
	b.WriteString("5. Failures: fix before moving on\n")
	b.WriteString("\nGoal: each sub-agent focuses on one module and does not carry irrelevant context.\n")
	if done > 0 {
		fmt.Fprintf(&b, "\nNote: %d/%d steps are already completed. Focus on the remaining %d steps.\n", done, total, total-done)
	}
	prompt := b.String()

	// Show module preview.
	if len(modules) > 0 {
		c.notice(fmt.Sprintf("plan-exec: detected %d modules — %s", len(modules), strings.Join(modules, ", ")))
	}

	c.SetPlanMode(false)
	c.SetGoal("execute plan: " + ShortGoalForNotice(todos[0].Content))
	c.GoalStrict(strict)
	c.notice(fmt.Sprintf("plan-exec: dispatching %d plan steps (strict=%v)", total, strict))
	if c.runner != nil {
		c.runGuarded(func(ctx context.Context) error {
			return c.runGoalLoopWithRawDisplay(ctx, prompt, prompt, display)
		})
	}
}

// prometheusPrompt is the strategic planner system prompt.
const prometheusPrompt = "You are Prometheus, a strategic planner. Interview the user one question at a time. Cover: scope, modules, files, constraints, tests. When ready, output a numbered plan with each step tagged by module. End by calling update_goal with status complete. Do not implement.\n\nFor independent research directions, use parallel_tasks before planning."

// applyPrometheus starts an interactive planning interview, inspired by OMO's
// Prometheus agent. It enters goal mode with a structured interview prompt.
func (c *Controller) applyPrometheus(input, display string) {
	args := strings.TrimSpace(strings.TrimPrefix(input, "/prometheus"))
	if args == "" || args == "--strict" {
		c.notice("usage: /prometheus <your task description>")
		return
	}
	strict := false
	if strings.HasPrefix(args, "--strict ") {
		strict = true
		args = strings.TrimPrefix(args, "--strict ")
	}
	prompt := prometheusPrompt + "\n\n## User request\n\n" + args + "\n\nBegin the interview by asking your first clarifying question."
	c.SetPlanMode(false)
	c.SetGoal("plan: " + ShortGoalForNotice(args))
	c.GoalStrict(strict)
	c.notice("prometheus: starting planning interview")
	if c.runner != nil {
		c.runGuarded(func(ctx context.Context) error {
			return c.runGoalLoopWithRawDisplay(ctx, prompt, prompt, display)
		})
	}
}

// shellTimeout is the maximum time a user-invoked "!command" may run. Matches
// the bash tool's timeout so behaviour is consistent across invocation paths.
const shellTimeout = 120 * time.Second

// shellWaitDelay bounds how long cmd.Run() waits after context cancellation for
// the child's pipes to drain, matching the bash tool's WaitDelay.
const shellWaitDelay = 5 * time.Second

func shellCommandPreview(command string) string {
	command = strings.TrimSpace(strings.ReplaceAll(command, "\n", " "))
	const max = 48
	r := []rune(command)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return command
}

// RunShell executes a shell command directly (bypassing the model) and streams
// the output as ToolDispatch/ToolProgress/ToolResult events. It uses the same
// bash-tool infrastructure (shell resolution, timeout) and shares the runGuarded
// lock with model turns — only one can run at a time. User-invoked "!" commands
// run without the OS sandbox (the user typed the command explicitly).
func (c *Controller) RunShell(command string) {
	command = strings.TrimSpace(command)
	if command == "" {
		c.notice(i18n.M.ShellExecEmpty)
		return
	}
	c.runGuarded(func(ctx context.Context) error {
		sh := c.shell
		if sh.Path == "" {
			sh = sandbox.ResolveShell("", "", nil)
		}
		argv, _ := sandbox.Command(sandbox.Spec{}, sh, command) // false = unsandboxed (user invoked)

		preview := []rune(command)
		if len(preview) > 32 {
			preview = preview[:32]
		}
		id := "shell-" + string(preview)
		diagnosticPreview := shellCommandPreview(command)
		desc := shellrun.DescriptorFromShell(sh)

		if err := event.EmitChecked(c.sink, event.Event{
			Kind: event.ToolDispatch,
			Tool: event.Tool{
				ID:   id,
				Name: "bash",
				Args: fmt.Sprintf(`{"command":%q}`, command),
				Execution: &event.ShellExecution{
					Kind: desc.Kind, Shell: desc.Shell, ShellVersion: desc.ShellVersion,
					Platform: desc.Platform, SupportsAndAnd: desc.SupportsAndAnd,
					State: tool.ShellStateRunning,
				},
			},
		}); err != nil {
			return fmt.Errorf("persist shell dispatch: %w", err)
		}

		start := time.Now()
		res := shellrun.RunForeground(ctx, shellrun.Request{
			Argv:           argv,
			Dir:            c.workspaceRoot,
			Timeout:        shellTimeout,
			WaitDelay:      shellWaitDelay,
			CommandPreview: diagnosticPreview,
			ShellKind:      sh.Kind.String(),
			ShellPath:      sh.Path,
			Source:         "user_shell",
			Track:          true,
			Progress: func(chunk string) {
				c.sink.Emit(event.Event{
					Kind: event.ToolProgress,
					Tool: event.Tool{ID: id, Output: chunk},
				})
			},
		})
		durationMs := time.Since(start).Milliseconds()
		ex := &event.ShellExecution{
			Kind: desc.Kind, Shell: desc.Shell, ShellVersion: desc.ShellVersion,
			Platform: desc.Platform, SupportsAndAnd: desc.SupportsAndAnd,
			State: res.State, FailurePhase: res.FailurePhase,
			OutputTail: res.OutputTail, DurationMs: durationMs,
			MutationRisk: tool.ShellMutationNone,
			Verification: tool.ShellVerificationNotVerification,
		}
		if res.ExitCode != nil {
			code := *res.ExitCode
			ex.ExitCode = &code
		}
		switch res.State {
		case tool.ShellStateCompleted:
			ex.MutationRisk = tool.ShellMutationNone
		case tool.ShellStateNotRun:
			ex.MutationRisk = tool.ShellMutationNotStarted
		case tool.ShellStateFailed:
			if res.FailurePhase == tool.ShellPhaseLaunch {
				ex.MutationRisk = tool.ShellMutationNotStarted
			} else {
				ex.MutationRisk = tool.ShellMutationMayBePartial
			}
		case tool.ShellStateTimedOut, tool.ShellStateCancelled:
			ex.MutationRisk = tool.ShellMutationMayBePartial
		}

		errText := ""
		switch res.State {
		case tool.ShellStateCancelled:
			errText = i18n.M.TurnCancelled
		case tool.ShellStateTimedOut:
			errText = fmt.Sprintf(i18n.M.ShellExecTimeoutFmt, shellTimeout)
		case tool.ShellStateFailed, tool.ShellStateNotRun:
			if res.Err != nil {
				errText = fmt.Sprintf(i18n.M.ShellExecFailedFmt, res.Err)
			}
		}
		c.sink.Emit(event.Event{
			Kind: event.ToolResult,
			Tool: event.Tool{
				ID: id, Name: "bash", Output: res.Combined, Err: errText,
				DurationMs: durationMs, Execution: ex,
			},
		})
		return nil
	})
}

// runRefTurn resolves a line's @references into a context block and starts a
// turn with it prepended (or the raw line when nothing resolved).
func (c *Controller) runRefTurn(input, display string) {
	c.runRefTurnWithRefs(input, input, display)
}

// runRefTurnWithFormat runs a reference turn with a structured-output
// format bound to its context (symmetric with runGoalLoop's withTurnFormat
// injection — format is a property of every accepted turn, not just the
// plain-goal path; review #7234 binds format to the accepted turn).
func (c *Controller) runRefTurnWithFormat(input, display, format string) {
	c.runGuarded(func(ctx context.Context) error {
		return c.runRefTurnWithResolverSync(c.withTurnFormat(ctx, format), input, input, display, "", c.ResolveRefs)
	})
}

func (c *Controller) runScopedRefTurnWithFormat(input, display, format string) {
	c.runGuarded(func(ctx context.Context) error {
		return c.runRefTurnWithResolverSync(c.withTurnFormat(ctx, format), input, input, display, "", c.ResolveScopedRefs)
	})
}

func (c *Controller) runRefTurnWithRefsFormat(input, refLine, display, format string) {
	c.runGuarded(func(ctx context.Context) error {
		return c.runRefTurnWithResolverSync(c.withTurnFormat(ctx, format), input, refLine, display, "", c.ResolveRefs)
	})
}

func (c *Controller) runScopedRefTurnWithRefsFormat(input, refLine, display, format string) {
	c.runGuarded(func(ctx context.Context) error {
		return c.runRefTurnWithResolverSync(c.withTurnFormat(ctx, format), input, refLine, display, "", c.ResolveScopedRefs)
	})
}

func (c *Controller) runEditedRefTurnWithFormat(input, display, original, format string) {
	c.runGuarded(func(ctx context.Context) error {
		return c.runRefTurnWithResolverSync(c.withTurnFormat(ctx, format), input, input, display, original, c.ResolveRefs)
	})
}

func (c *Controller) runEditedRefTurnWithRefsFormat(input, refLine, display, original, format string) {
	c.runGuarded(func(ctx context.Context) error {
		return c.runRefTurnWithResolverSync(c.withTurnFormat(ctx, format), input, refLine, display, original, c.ResolveRefs)
	})
}

// runRefTurnWithRefs resolves references from refLine while preserving input as
// the user's actual prompt text. This lets compiler diagnostics such as
// "/path/File.kt:12: error" attach @/path/File.kt without rewriting the error.
func (c *Controller) runRefTurnWithRefs(input, refLine, display string) {
	c.runRefTurnWithResolver(input, refLine, display, c.ResolveRefs)
}

func (c *Controller) runRefTurnWithResolver(input, refLine, display string, resolve func(context.Context, string) (string, []string)) {
	c.runGuarded(func(ctx context.Context) error {
		return c.runRefTurnWithResolverSync(ctx, input, refLine, display, "", resolve)
	})
}

func (c *Controller) runRefTurnWithResolverSync(ctx context.Context, input, refLine, display, original string, resolve func(context.Context, string) (string, []string)) error {
	block, errs := resolve(ctx, refLine)
	for _, e := range errs {
		c.notice(e)
	}
	sent := input
	if block != "" {
		sent = "Referenced context:\n\n" + block + "\n\n" + input
	}
	if strings.TrimSpace(original) != "" {
		return c.runEditedGoalLoopWithImageRefsRawDisplay(ctx, sent, input, refLine, display, original)
	}
	return c.runGoalLoopWithImageRefsRawDisplay(ctx, sent, input, refLine, display)
}

// notice emits an informational Notice event.
func (c *Controller) notice(text string) {
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: text})
}

func (c *Controller) noticeDetail(text, detail string) {
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: text, Detail: detail})
}

// Run executes a turn synchronously, returning the agent's error. Used by the
// headless `reasonix run` path, where the Sink renders to stdout and the caller
// just needs the exit status — no TurnDone event, no cancel bookkeeping.
func (c *Controller) runReady(ctx context.Context, input string) (err error) {
	ctx = extension.ContextWithRuntimeOwner(ctx, c.RuntimeOwner())
	if c.RuntimePhase() == RuntimePhaseDraining {
		c.emitDrainingNotice()
		return ErrRuntimeDraining
	}
	defer event.RecordTurnCompletion(c.sink)
	c.maybeSessionStart(ctx)
	parentSession := c.parentSessionID()
	ctx = agent.WithParentSession(ctx, parentSession)
	ctx = jobs.WithSession(ctx, parentSession)
	rawInput := input
	ctx = c.withTurnImages(ctx, rawInput)
	ctx = agent.WithRawUserInput(ctx, rawInput)
	input = c.Compose(input)
	// input.receive: same interception seam as the orchestrated turn — the
	// composed headless input crosses the extension chain before it enters
	// the session.
	input, blocked, interceptErr := c.interceptInputReceive(ctx, input)
	if interceptErr != nil {
		return interceptErr
	}
	if blocked {
		return nil
	}
	startMessages := c.messageCount()
	var marker agent.InFlightTurnMeta
	defer func() { c.finishInFlightTurn(startMessages, marker) }()
	c.beginCheckpoint(ctx, rawInput)
	if c.guardianSess != nil {
		c.guardianSess.ResetTurn()
	}
	if c.hooks.Enabled() {
		c.mu.Lock()
		c.turn++
		turn := c.turn
		c.mu.Unlock()
		if block, _ := c.hooks.PromptSubmit(ctx, input, turn); block {
			return nil
		}
		defer func() { c.hooks.StopResult(context.Background(), lastAssistantText(c.History()), turn, err) }()
	}
	marker = c.markInFlightTurn(startMessages, true)
	ctx = c.withTurnContext(ctx, true)
	ctx = c.withPlannerTurnMetadata(ctx, rawInput, false, startMessages)
	modelInput := c.withCapabilityRoute(ctx, input, rawInput)
	modelInput, ctx, err = c.prepareVisionTurn(ctx, modelInput, agent.SubagentImageCandidates(ctx))
	if err != nil {
		return err
	}
	err = c.runModelTurn(ctx, modelInput)
	return err
}

// RunSubagentProfile executes one named runAs=subagent skill synchronously and
// returns only its final answer. It is the headless CLI counterpart to explicit
// slash invocation: the child keeps an isolated session, while the caller owns
// stdout rendering and exit status. readOnly selects the preview-safe runner
// used by `reasonix subagent try`.
func (c *Controller) RunSubagentProfile(ctx context.Context, name, task string, readOnly bool) (string, error) {
	name = strings.TrimSpace(name)
	task = strings.TrimSpace(task)
	if name == "" {
		return "", fmt.Errorf("subagent name is required")
	}
	if task == "" {
		return "", fmt.Errorf("subagent task is required")
	}
	sk, ok := c.skills.bySlashName(name)
	if !ok {
		return "", fmt.Errorf("unknown or disabled subagent profile %q", name)
	}
	if sk.RunAs != skill.RunSubagent {
		return "", fmt.Errorf("skill %q is not runAs=subagent", name)
	}
	sk = c.skills.prepare(sk)
	runner := c.skillRunner
	if readOnly {
		runner = c.readOnlySkillRunner
	}
	if runner == nil {
		return "", fmt.Errorf("subagent skill runner is unavailable for %q", name)
	}

	c.maybeSessionStart(ctx)
	parentSession := c.parentSessionID()
	ctx = agent.WithParentSession(ctx, parentSession)
	ctx = jobs.WithSession(ctx, parentSession)
	ctx = c.withTurnImages(ctx, task)
	ctx = agent.WithResponseLanguagePreference(ctx, c.responseLanguage)
	ctx = agent.WithReasoningLanguagePreference(ctx, c.reasoningLanguage)
	ctx = agent.WithSubagentDepth(ctx, 0)
	answer, err := runner(ctx, sk, task, skill.SubagentRunOptions{HostInitiated: true})
	if err != nil {
		return "", err
	}
	return tool.GuardSubagentHostDecisionText(answer), nil
}

// Cancel aborts the in-flight turn. A goroutine blocked awaiting approval
// unblocks via the cancelled context.
func (c *Controller) Cancel() {
	c.promptResolveMu.Lock()
	defer c.promptResolveMu.Unlock()
	c.cancelLocked()
}

func (c *Controller) cancelLocked() {
	c.mu.Lock()
	cancel := c.cancel
	if cancel != nil {
		c.canceling = true
	}
	c.mu.Unlock()
	if cancel != nil {
		c.emitTurnStatus(event.TurnCancelling)
		c.promptOwner.CancelAll()
		c.approval.clearAll()
		cancel()
		return
	}
	if c.goals.active() {
		c.stopGoal(GoalStatusStopped)
	}
}

// beginRotation claims the session-rotation gate. It fails if a turn is running
// or another rotation is already in progress, so the caller holds exclusive
// rights to swap the executor session from the check here through endRotation.
// This closes the TOCTOU window that a bare `if c.running` check left open:
// between that check and the actual SetSession, a turn could start and then be
// yanked out from under the run loop.
func (c *Controller) beginRotation() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running || c.finishing {
		return errTurnRunningRotation
	}
	if c.rotating {
		return errRotationInProgress
	}
	c.rotating = true
	return nil
}

// CancelRequested reports whether Cancel has been requested for the active turn.
func (c *Controller) CancelRequested() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.canceling
}

// PendingPrompt reports whether the current turn is blocked waiting for a user
// approval, plan approval, memory approval, or ask-tool answer.
func (c *Controller) PendingPrompt() bool {
	return c.approval.hasPending()
}

// RuntimeStatus reports the active work owned by the foreground controller.
func (c *Controller) RuntimeStatus() RuntimeStatus {
	c.mu.Lock()
	running := c.running
	active := running || c.finishing
	canceling := c.canceling
	c.mu.Unlock()
	pending := c.approval.hasPending()
	backgroundJobs := len(c.Jobs())
	turnID, status, turnEventSeq, replayAfterSeq := c.turnEventRuntimeStatus()
	return RuntimeStatus{
		Running:         active,
		PendingPrompt:   pending,
		BackgroundJobs:  backgroundJobs,
		CancelRequested: canceling,
		Cancellable:     running || pending || canceling,
		TurnID:          turnID,
		Status:          status,
		TurnEventSeq:    turnEventSeq,
		ReplayAfterSeq:  replayAfterSeq,
	}
}

// Turn returns the current turn number (0 before the first submit).
func (c *Controller) Turn() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.turn
}

func (c *Controller) recordDecisionReceipt(pending pendingApproval, outcome string) {
	if c == nil || c.executor == nil || pending.reply == nil {
		return
	}
	kind := pending.kind
	if kind == "" {
		kind = "tool"
		if pending.tool == planApprovalTool {
			kind = "plan"
		}
	}
	receipt := &provider.DecisionReceipt{
		ID:      pending.id,
		Kind:    kind,
		Tool:    strings.TrimSpace(pending.tool),
		Subject: clipUTF8(strings.TrimSpace(pending.subject), 240),
		Outcome: strings.TrimSpace(outcome),
	}
	// Keep the receipt bounded and provider-excluded even when an older caller
	// omits optional approval metadata.
	c.executor.Session().AddDecisionReceipt(receipt)
	c.sink.Emit(event.Event{
		Kind:            event.Notice,
		Code:            event.NoticeCodeDecisionReceipt,
		Level:           event.LevelInfo,
		Text:            "Decision recorded: " + receipt.Outcome,
		DecisionReceipt: receipt,
	})
}

// EnableInteractiveApproval swaps the executor's gate for one that routes
// approval decisions to the frontend via ApprovalRequest events, and wires the
// controller in as the executor's Asker so the `ask` tool can question the user.
// Interactive frontends (chat, desktop) call this; the headless run keeps the
// silent gate and a nil asker from setup.
func (c *Controller) EnableInteractiveApproval() {
	trustGate := planModeReadOnlyTrustApprover{c}
	escapeApprover := sandboxEscapeApprover{c}
	configApprover := managedConfigWriteApprover{c}
	c.writeAccess.interactive = true
	if c.executor != nil {
		c.executor.SetGate(c.newInteractiveGate())
		c.executor.SetPlanModeReadOnlyTrustGate(trustGate)
		c.executor.SetSandboxEscapeApprover(escapeApprover)
		c.executor.SetConfigWriteApprover(configApprover)
		c.executor.SetWriteAccessGate(c)
		c.executor.SetWriteRoots(c.writeAccess.roots)
		c.executor.SetAsker(c)
		c.executor.SetInteractionBroker(c)
	}
	if setter, ok := c.runner.(interface {
		SetPlanModeReadOnlyTrustGate(agent.PlanModeReadOnlyTrustGate)
	}); ok {
		setter.SetPlanModeReadOnlyTrustGate(trustGate)
	}
	if setter, ok := c.runner.(interface {
		SetSandboxEscapeApprover(sandbox.EscapeApprover)
	}); ok {
		setter.SetSandboxEscapeApprover(escapeApprover)
	}
	if setter, ok := c.runner.(interface {
		SetConfigWriteApprover(tool.ConfigWriteApprover)
	}); ok {
		setter.SetConfigWriteApprover(configApprover)
	}
	if setter, ok := c.runner.(interface {
		SetWriteAccessGate(agent.WriteAccessGate)
	}); ok {
		setter.SetWriteAccessGate(c)
	}
	if setter, ok := c.runner.(interface {
		SetWriteRoots(*sandbox.WritableRootSet)
	}); ok {
		setter.SetWriteRoots(c.writeAccess.roots)
	}
	if setter, ok := c.runner.(interface {
		SetPlannerPlanApprover(agent.PlannerPlanApprover)
	}); ok {
		setter.SetPlannerPlanApprover(plannerPlanApprover{c: c})
	}
	// The planner holds the real ask tool, so it reaches the same approval
	// surface the executor does instead of a parallel prose-question path.
	if setter, ok := c.runner.(interface{ SetAsker(agent.Asker) }); ok {
		setter.SetAsker(c)
	}
	if setter, ok := c.runner.(interface{ SetInteractionBroker(mcpinteraction.Broker) }); ok {
		setter.SetInteractionBroker(c)
	}
}

type plannerPlanApprover struct {
	c *Controller
}

func (p plannerPlanApprover) RunWithPlannerApproval(ctx context.Context, plan string, run func(context.Context) error) error {
	c := p.c
	allow, _, err := c.requestApprovalWithReason(ctx, planApprovalTool, "", nil, "Planner requested host approval before execution.")
	if err != nil {
		return err
	}
	if !allow {
		return nil
	}
	todoArgs := c.seedPlanTodos(plan)
	execStart := c.sessionMessageCount()
	c.approval.setPlanAutoApprove(true)
	defer c.approval.setPlanAutoApprove(false)
	if err := run(ctx); err != nil {
		return err
	}
	if todoArgs != "" && !c.hasTodoUpdateSince(execStart) {
		c.completePlanTodos(todoArgs)
	}
	return nil
}

func (c *Controller) newInteractiveGate() *permission.Gate {
	policy := c.policy
	mode := c.approval.mode()
	switch mode {
	case ToolApprovalAuto, ToolApprovalYolo:
		policy.Mode = permission.Allow
	case ToolApprovalDontAsk:
		policy.Mode = permission.Deny
	default:
		policy.Mode = permission.Ask
	}
	// SessionAllow must not cover fresh-human tools: it is checked before Ask,
	// so `--allowed-tools remember` would skip the prompt. Interactive Auto and
	// YOLO treat remember/forget as ordinary policy decisions; Auto still
	// preserves an explicit configured Ask rule, while YOLO bypasses it.
	policy.SessionAllow = rulesWithoutFreshHumanApproval(policy.SessionAllow)
	if mode != ToolApprovalAuto && mode != ToolApprovalYolo {
		policy.Ask = append(policy.Ask,
			permission.Rule{Tool: memoryRememberTool},
			permission.Rule{Tool: memoryForgetTool},
		)
	}
	var approver permission.Approver = gateApprover{c}
	if mode == ToolApprovalDontAsk {
		approver = denyPermissionApprover{}
	}
	gate := permission.NewGate(policy, approver)
	gate.OnRemember = func(rule string) {
		if c.onRemember != nil {
			_ = c.onRemember(rule)
		}
	}
	return gate
}

func (c *Controller) allowLowRiskRemember(args json.RawMessage) bool {
	mem := c.Memory()
	if mem != nil {
		if assessment := memory.AssessRememberWrite(mem.Store, args); assessment.AutoAllow {
			c.memory.authorizeAutoRemember(args)
			return true
		}
	}
	c.memory.revokeAutoRemember(args)
	return false
}

func (c *Controller) newHeadlessGate(mode string) *freshHumanHeadlessGate {
	gate := BuildHeadlessApprovalGate(c.policy, mode)
	gate.allowLowRiskFreshAction = func(toolName string, args json.RawMessage) bool {
		return toolName == memoryRememberTool && c.allowLowRiskRemember(args)
	}
	return gate
}

type denyPermissionApprover struct{}

func (denyPermissionApprover) Approve(context.Context, string, string, json.RawMessage) (bool, bool, error) {
	return false, false, nil
}

// rulesWithoutFreshHumanApproval drops any session-allow rule that targets a
// tool requiring fresh human approval, so an explicit allowlist cannot bypass
// the always-prompt contract for those tools.
func rulesWithoutFreshHumanApproval(rules []permission.Rule) []permission.Rule {
	if len(rules) == 0 {
		return rules
	}
	filtered := make([]permission.Rule, 0, len(rules))
	for _, r := range rules {
		if RequiresFreshHumanApprovalTool(r.Tool) {
			continue
		}
		filtered = append(filtered, r)
	}
	return filtered
}

// ApplyHeadlessApprovalMode configures the executor gate for a non-interactive
// (`reasonix run`) session from an explicit --permission-mode. Unlike
// EnableInteractiveApproval it installs no blocking approver, asker, or
// fresh-approval prompt: there is no key loop to answer them, and the default
// infinite approval timeout would wedge the run forever on an Ask rule, the
// `ask` tool, or a sandbox/config approval. Modes map straight onto a headless
// gate, and each preserves the interactive contract as closely as a run with no
// one to prompt allows:
//
//   - auto: auto-approve the writer fallback (Mode=Allow) but PRESERVE explicit
//     ask rules. Interactive auto prompts on those (it never auto-approves them);
//     headless can't prompt, so a would-ask decision fails closed (deny) rather
//     than running silently. Only bypass may run such a command unattended.
//   - yolo/bypassPermissions: skip ordinary approval-gated decisions (nil
//     approver); deny rules and fresh decisions still fail closed.
//   - dontAsk: deny anything that would ask, and deny the writer fallback too.
//
// Deny rules and fresh-human tools (memory, plan, sandbox, config) stay enforced
// by the gate for every mode. The only exception is a controller-assessed,
// create-only project/reference memory; every other memory write remains denied.
func (c *Controller) ApplyHeadlessApprovalMode(mode string) {
	mode = normalizeToolApprovalMode(mode)
	c.approval.setMode(mode)
	if c.subagentGate != nil {
		c.subagentGate.Update(mode)
	}
	c.writeAccess.interactive = false
	if c.executor != nil {
		c.executor.SetGate(c.newHeadlessGate(mode))
		c.executor.SetWriteAccessGate(c)
		c.executor.SetWriteRoots(c.writeAccess.roots)
	}
}

func (c *Controller) refreshInteractiveGate() {
	if c.executor != nil {
		c.executor.SetGate(c.newInteractiveGate())
	}
}

// TrySteer queues mid-turn guidance only when the active agent turn accepts it.
func (c *Controller) TrySteer(text string) bool {
	c.mu.Lock()
	exec := c.executor
	running := c.running
	c.mu.Unlock()
	return running && exec != nil && exec.Steer(text)
}

// Steer is the compatibility path for callers that cannot observe admission.
// Interactive hosts should call TrySteer so a rejected steer remains in their
// draft/queue and can be retried as a regular follow-up.
func (c *Controller) Steer(text string) {
	if c.TrySteer(text) {
		return
	}
	// No active turn accepted the steer: the frontend's runningRef was stale,
	// the turn exited between our running check and the enqueue, or no
	// executor is bound yet. Deliver it as a regular turn instead.
	c.submitSteerFallback(text)
}

// submitSteerFallback records steer text that no active turn accepted as
// unapplied guidance, not as a new task. This compatibility path deliberately
// never opens a provider turn: replaying stale historical guidance as the
// user's current request caused unintended code changes (#7045).
func (c *Controller) submitSteerFallback(text string) admissionResult {
	return c.runGuardedOrPark(func(context.Context) error {
		if c.executor != nil {
			c.executor.RecordUnappliedSteer(text)
		}
		return nil
	})
}

// SteerConsumed returns true when the steer queue is empty after the last consume.
func (c *Controller) SteerConsumed() bool {
	c.mu.Lock()
	exec := c.executor
	c.mu.Unlock()
	if exec != nil {
		return exec.SteerConsumed()
	}
	return true
}

// promptQueueNoticeDelay is how long a prompt may wait behind another before
// the user is told why nothing has appeared. Short enough to beat "it's stuck",
// long enough that an approval answered promptly never emits a notice.
var promptQueueNoticeDelay = 3 * time.Second

// lockPromptFor acquires the prompt lock, emitting one notice if the wait is
// long enough to look like a hang. It reports false only when ctx ended first;
// the lock is held on true.
func (c *Controller) lockPromptFor(ctx context.Context, kind string) bool {
	acquired := make(chan struct{})
	go func() {
		c.approval.promptMu.Lock()
		close(acquired)
	}()
	select {
	case <-acquired:
		return true
	case <-ctx.Done():
	case <-time.After(promptQueueNoticeDelay):
	}
	if ctx.Err() == nil {
		c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Code: event.NoticeCodePromptQueued,
			Text:   "A " + kind + " is waiting for you to answer the prompt ahead of it.",
			Detail: "the assistant asked something while an earlier approval or question was still open; it appears once that one is answered"})
	}
	select {
	case <-acquired:
		return true
	case <-ctx.Done():
		// The lock may still be handed to the goroutine above; release it so the
		// next prompt is not blocked by this abandoned wait.
		go func() {
			<-acquired
			c.approval.promptMu.Unlock()
		}()
		return false
	}
}

// Ask implements agent.Asker: it emits an AskRequest and blocks until
// AnswerQuestion(ID, …) answers or ctx is cancelled. promptMu serialises it
// against tool-approval prompts so at most one user prompt is outstanding.
// Unlike tool-approval gates, Ask is NOT bypassed in YOLO mode — the `ask`
// tool exists to get a genuine user decision, and YOLO only auto-approves
// tool calls; it must not answer the user's questions for them.
func (c *Controller) Ask(ctx context.Context, questions []event.AskQuestion) ([]event.AskAnswer, error) {
	// Registering after the lock left a queued question invisible everywhere:
	// no event, absent from the snapshot, unreachable by ReplayPendingPrompts.
	id, reply := c.approval.registerAsk(questions)
	c.registerOwnedPrompt(id, PromptAsk)

	if !c.lockPromptFor(ctx, "question") {
		c.cancelOwnedPrompt(id)
		return nil, ctx.Err()
	}
	defer c.approval.promptMu.Unlock()

	c.approval.promptEmitMu.Lock()
	turnID, _, _, _ := c.turnEventRuntimeStatus()
	_, runtimeEpoch := c.promptIdentitySnapshot()
	if identity := c.bindOwnedPromptRouting(id, turnID, runtimeEpoch); identity.TurnID != "" {
		turnID = identity.TurnID
	}
	if err := event.EmitChecked(c.sink, event.Event{Kind: event.AskRequest, TurnID: turnID, ItemID: id, Ask: event.Ask{ID: id, Questions: questions, TurnID: turnID}}); err != nil {
		c.approval.promptEmitMu.Unlock()
		c.cancelOwnedPrompt(id)
		return nil, fmt.Errorf("persist ask request: %w", err)
	}
	c.approval.markAskEmitted(id)
	c.approval.promptEmitMu.Unlock()

	waitCtx, cancelWait := c.approval.waitContext(ctx)
	defer cancelWait()

	select {
	case ans := <-reply:
		return ans, nil
	case <-waitCtx.Done():
		c.cancelOwnedPrompt(id)
		return nil, waitCtx.Err()
	}
}

// AnswerQuestion resolves a pending AskRequest by ID with the user's selections.
// Unknown/expired IDs are ignored.
func (c *Controller) AnswerQuestion(id string, answers []event.AskAnswer) {
	_ = c.AnswerQuestionChecked(id, answers)
}

// AnswerQuestionChecked persists the prompt transition before releasing the
// agent loop. A failed ledger write leaves the prompt pending and retryable.
func (c *Controller) AnswerQuestionChecked(id string, answers []event.AskAnswer) error {
	c.promptResolveMu.Lock()
	defer c.promptResolveMu.Unlock()
	return c.answerQuestionCheckedLocked(id, answers)
}

func (c *Controller) answerQuestionCheckedLocked(id string, answers []event.AskAnswer) error {
	pending, ok, err := c.approval.resolveAskAfter(id, func(p pendingAsk) error {
		return c.emitTurnEventChecked(event.Event{Kind: event.PromptAnswered, ItemID: id, Status: event.TurnInProgress})
	})
	if err != nil {
		return err
	}
	if ok {
		c.promptOwner.Remove(id)
		// An answer batch with no selections is the explicit "skip and continue
		// chat" path. End the current turn instead of feeding a prose dismissal
		// back to the model and trusting it not to ask again (#6869).
		if !askAnswersHaveSelection(answers) {
			c.mu.Lock()
			activeTurn := c.cancel != nil
			c.mu.Unlock()
			if activeTurn {
				c.cancelLocked()
				return nil
			}
		}
		c.recordAskDecisionReceipt(id, pending, answers)
		pending.reply <- answers // buffered, never blocks
	}
	return nil
}

func (c *Controller) recordAskDecisionReceipt(id string, pending pendingAsk, answers []event.AskAnswer) {
	if c == nil || c.executor == nil {
		return
	}
	selected := make(map[string][]string, len(answers))
	for _, answer := range answers {
		selected[answer.QuestionID] = append([]string(nil), answer.Selected...)
	}
	parts := make([]string, 0, len(pending.questions))
	for _, question := range pending.questions {
		answer := strings.TrimSpace(strings.Join(selected[question.ID], ", "))
		if answer == "" {
			answer = "—"
		}
		prompt := strings.TrimSpace(question.Prompt)
		if prompt == "" {
			prompt = strings.TrimSpace(question.Header)
		}
		if prompt == "" {
			prompt = question.ID
		}
		parts = append(parts, prompt+": "+answer)
	}
	receipt := &provider.DecisionReceipt{
		ID:      id,
		Kind:    "ask",
		Subject: clipUTF8(strings.Join(parts, " · "), 240),
		Outcome: "answered",
	}
	c.executor.Session().AddDecisionReceipt(receipt)
	c.sink.Emit(event.Event{
		Kind:            event.Notice,
		Code:            event.NoticeCodeDecisionReceipt,
		Level:           event.LevelInfo,
		Text:            "Decision recorded: answered",
		DecisionReceipt: receipt,
	})
}

func askAnswersHaveSelection(answers []event.AskAnswer) bool {
	for _, answer := range answers {
		if len(answer.Selected) > 0 {
			return true
		}
	}
	return false
}

// ReplayPendingPrompts re-emits the ApprovalRequest / AskRequest event for every
// prompt currently blocking the run loop. A frontend that reconnected or reloaded
// after the original event has no way to rebuild its approval/ask modal otherwise,
// so the blocked gate goroutine stays stuck forever while the session shows a
// "waiting" status with no actionable prompt. promptMu serialises Ask and
// requestApproval, so in practice at most one prompt is outstanding; the loops
// stay general so a future concurrent prompt would still replay correctly.
func (c *Controller) ReplayPendingPrompts() {
	c.approval.promptEmitMu.Lock()
	noApprovals := c.replayPendingPromptsTo(c.sink)
	c.approval.promptEmitMu.Unlock()
	if noApprovals {
		// Retained compatibility hook; live Auto Guard cards are ordinary approvals.
		c.ReplayUnresolvedRecoveries()
	}
}

// ReplayPendingPromptsTo re-emits pending prompts to one frontend sink. Serve
// uses this for a newly attached SSE client so existing browsers do not receive
// duplicate approval/ask cards when another client reconnects.
func (c *Controller) ReplayPendingPromptsTo(sink event.Sink) {
	c.approval.promptEmitMu.Lock()
	defer c.approval.promptEmitMu.Unlock()
	c.replayPendingPromptsTo(sink)
}

// ReplayPendingPromptsWith performs an SSE connection handoff while prompt
// registration and emission are paused. The factory must subscribe the new
// client and return a sink that targets it; this closes the attach race where
// the original prompt could otherwise land between Subscribe and replay.
func (c *Controller) ReplayPendingPromptsWith(sinkFactory func() event.Sink) {
	if sinkFactory == nil {
		return
	}
	c.approval.promptEmitMu.Lock()
	defer c.approval.promptEmitMu.Unlock()
	c.replayPendingPromptsTo(sinkFactory())
}

func (c *Controller) replayPendingPromptsTo(sink event.Sink) bool {
	approvals, asks := c.approval.snapshotPrompts()
	interactions := c.approval.snapshotMCPInteractions()
	c.emitPendingPrompts(sink, approvals, asks, interactions)
	return len(approvals) == 0
}

func (c *Controller) emitPendingPrompts(sink event.Sink, approvals []event.Approval, asks []event.Ask, interactions []event.MCPInteraction) {
	if sink == nil {
		return
	}
	for _, a := range approvals {
		if identity, ok := c.promptOwner.Identity(a.ID); ok {
			a.TurnID = identity.TurnID
		}
		sink.Emit(c.approvalRequestEvent(a))
	}
	for _, a := range asks {
		if identity, ok := c.promptOwner.Identity(a.ID); ok {
			a.TurnID = identity.TurnID
		}
		sink.Emit(event.Event{Kind: event.AskRequest, TurnID: a.TurnID, ItemID: a.ID, Ask: a})
	}
	for _, i := range interactions {
		if identity, ok := c.promptOwner.Identity(i.ID); ok {
			i.TurnID = identity.TurnID
		}
		sink.Emit(event.Event{Kind: event.MCPInteractionRequest, TurnID: i.TurnID, ItemID: i.ID, MCPInteraction: i})
	}
}

// SetPlanMode flips the executor's plan-first workflow flag without touching the
// cache-stable system/tool prefix, and remembers the state so Compose can prepend
// the plan-mode marker to outgoing user turns.
func (c *Controller) SetPlanMode(v bool) {
	c.applyPlanMode(v)
}

// SetAgentPreset writes the role through to the session quality floor:
// delivery/deliver/quality set the delivery floor, everything valid folds to
// standard, unknown values are ignored for compat. It never rebuilds the
// agent and never changes tool schemas.
func (c *Controller) SetAgentPreset(preset string) {
	if c == nil {
		return
	}
	if p, err := agentpreset.Normalize(preset); err == nil {
		_ = c.SetQualityFloor(string(p))
	}
}

// AgentPreset returns the session role label derived from the quality floor.
func (c *Controller) AgentPreset() string {
	if c.QualityFloor() == QualityFloorDelivery {
		return string(agentpreset.Delivery)
	}
	return string(agentpreset.Standard)
}

func (c *Controller) applyPlanMode(v bool) {
	c.mu.Lock()
	c.sessionSettings.planMode = v
	c.mu.Unlock()
	if setter, ok := c.runner.(interface{ SetPlanMode(bool) }); ok {
		setter.SetPlanMode(v)
		return
	}
	if c.executor != nil {
		c.executor.SetPlanMode(v)
	}
}

// SetResponseLanguage updates the final-answer language preference for
// subsequent turns.
func (c *Controller) SetResponseLanguage(lang string) {
	mode := config.NormalizeLanguage(lang)
	c.mu.Lock()
	c.responseLanguage = mode
	c.mu.Unlock()
	if setter, ok := c.runner.(interface{ SetResponseLanguage(string) }); ok {
		setter.SetResponseLanguage(mode)
	} else if c.executor != nil {
		c.executor.SetResponseLanguage(mode)
	}
}

// SetReasoningLanguage updates the visible reasoning language preference for
// subsequent turns.
func (c *Controller) SetReasoningLanguage(lang string) {
	mode := config.NormalizeReasoningLanguage(lang)
	c.mu.Lock()
	c.reasoningLanguage = mode
	c.mu.Unlock()
	if setter, ok := c.runner.(interface{ SetReasoningLanguage(string) }); ok {
		setter.SetReasoningLanguage(mode)
	} else if c.executor != nil {
		c.executor.SetReasoningLanguage(mode)
	}
}

// PlanMode reports whether outgoing turns currently receive the plan-mode
// marker.
func (c *Controller) PlanMode() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionSettings.planMode
}

// GoalStrict enables or disables strict goal mode. Since the structured
// protocol, every complete claim is validated against host readiness and an
// incomplete-todo intercept can never be overridden, so the flag is persisted
// for compatibility with older frontends but no longer changes FSM behavior.
func (c *Controller) GoalStrict(strict bool) {
	path, data, ok := c.goals.setStrict(strict, c.goalTodos())
	c.persistGoalState(path, data, ok)
}

// SetGoal stores a session-scoped active goal. Compose injects it into outgoing
// user turns, not the system prompt or tool schema, so it does not disturb the
// cache-stable prefix.
func (c *Controller) SetGoal(goal string) {
	c.SetGoalWithResearchMode(goal, GoalResearchAuto)
}

// SetGoalDurable updates the Goal only when its sidecar can be replaced
// atomically.
func (c *Controller) SetGoalDurable(goal string) error {
	snapshot := c.goals.capture()
	legacySnapshot, hadLegacySnapshot := c.legacyRestoreSnapshot()
	resolved, setup := c.resolveGoalText(goal, GoalResearchAuto)
	var path string
	var data []byte
	var persist bool
	if setup.blockReason != "" {
		path, data, persist = c.goals.setLegacyArchiveBlockedWithTaskID(resolved, setup.budgetClass, setup.blockReason, setup.legacyTaskID, c.goalTodos())
		c.replaceLegacyRestore(legacyGoalRestore{taskID: setup.legacyTaskID, epoch: c.goals.continuationToken(), explicit: setup.explicit})
	} else {
		path, data, persist = c.goals.set(resolved, setup.budgetClass, c.goalTodos())
		c.replaceLegacyRestore(legacyGoalRestore{})
	}
	if persist {
		if err := c.goals.writeStateErr(path, data); err != nil {
			c.goals.restore(snapshot)
			if hadLegacySnapshot {
				legacySnapshot.epoch = c.goals.continuationToken()
				c.replaceLegacyRestore(legacySnapshot)
			} else {
				c.replaceLegacyRestore(legacyGoalRestore{})
			}
			return err
		}
	}
	if setup.notice != "" {
		c.notice(setup.notice)
	}
	if setup.blockReason != "" {
		c.notice("legacy research archive resume failed: " + setup.blockReason)
	}
	return nil
}

func (c *Controller) SetGoalWithResearchMode(goal string, researchMode GoalResearchMode) {
	resolved, setup := c.resolveGoalText(goal, researchMode)
	if setup.notice != "" {
		c.notice(setup.notice)
	}
	var path string
	var data []byte
	var ok bool
	if setup.blockReason != "" {
		path, data, ok = c.goals.setLegacyArchiveBlockedWithTaskID(resolved, setup.budgetClass, setup.blockReason, setup.legacyTaskID, c.goalTodos())
		c.replaceLegacyRestore(legacyGoalRestore{taskID: setup.legacyTaskID, epoch: c.goals.continuationToken(), explicit: setup.explicit})
		c.notice("legacy research archive resume failed: " + setup.blockReason)
	} else {
		path, data, ok = c.goals.set(resolved, setup.budgetClass, c.goalTodos())
		c.replaceLegacyRestore(legacyGoalRestore{})
	}
	c.persistGoalState(path, data, ok)
}

// goalSetSetup is the resolved objective and budget class after archive lookup.
type goalSetSetup struct {
	budgetClass  string
	notice       string
	blockReason  string
	legacyTaskID string
	explicit     bool
}

func (c *Controller) resolveGoalText(goal string, researchMode GoalResearchMode) (string, goalSetSetup) {
	setup := goalSetSetup{budgetClass: budgetClassForLegacyMode(goal, researchMode)}
	legacy := c.prepareLegacyResearchTask(goal)
	if !legacy.explicit {
		return goal, setup
	}
	setup.notice, setup.blockReason, setup.legacyTaskID, setup.explicit = legacy.notice, legacy.blockReason, legacy.taskID, legacy.explicit
	if legacy.blockReason != "" {
		return goal, setup
	}
	setup.budgetClass = budgetClassResearch
	return legacy.goal, setup
}

// ResumeGoal re-enters a recoverable blocked/stopped Goal without resetting its
// delivery evidence scope or accumulated usage statistics.
func (c *Controller) ResumeGoal() bool {
	if handled, resumed := c.retryBlockedLegacyGoal(); handled {
		return resumed
	}
	spentBudget := c.goals.runtimeView().StopCause == stopCauseBudgetSpend
	path, data, persist, resumed := c.goals.resume(c.goalTodos())
	if !resumed {
		return false
	}
	c.persistGoalState(path, data, persist)
	if c.executor != nil {
		if spentBudget {
			c.executor.ResetTaskBudget()
		}
		c.executor.RestoreDeliveryCheckpoint(c.goals.deliveryState())
	}
	return true
}

// PauseGoal suspends a running Goal without losing its todo list, Delivery
// checkpoint, or runtime history; ResumeGoal restores it. Returns false when no
// running Goal exists.
func (c *Controller) PauseGoal() bool {
	if !c.goals.active() {
		return false
	}
	path, data, ok := c.goals.pauseFor(stopCauseManual, i18n.M.GoalPausedReason, c.goalTodos())
	c.persistGoalState(path, data, ok)
	c.notice(i18n.M.GoalPaused)
	return true
}

// GoalRuntime returns the active Goal's usage/runtime summary for frontends.
func (c *Controller) GoalRuntime() GoalRuntimeView {
	return c.goals.runtimeView()
}

// goalEvaluatorEvidence assembles the bounded evaluator's evidence: the goal
// contract, the current assistant final, a todo/readiness summary,
// turn/budget state, and the last
// continuation reason. Every field is treated as untrusted by the evaluator.
func (c *Controller) goalEvaluatorEvidence() goaleval.GoalEvidence {
	goal, _ := c.goals.snapshot()
	ev := goaleval.GoalEvidence{
		GoalContract:           goal,
		LastContinuationReason: c.goals.lastContinuationReasonText(),
	}
	if c.executor != nil {
		ev.AssistantFinal = lastAssistantText(c.History())
		todos := c.goalTodos()
		incomplete := 0
		for _, t := range todos {
			if t.Status != "completed" {
				incomplete++
			}
		}
		rr := c.executor.ReadinessResult()
		readinessText := "ready"
		if rr.Reason != "" {
			readinessText = rr.Reason
		}
		ev.TodoSummary = fmt.Sprintf("todos: %d total, %d incomplete; delivery readiness: %s", len(todos), incomplete, readinessText)
	}
	ev.TurnStatus = c.goals.budgetStatusText()
	return ev
}

func (c *Controller) persistGoalDeliveryCheckpoint() {
	if c.executor == nil {
		return
	}
	checkpoint := c.executor.DeliveryCheckpoint()
	path, data, ok := c.goals.setDeliveryCheckpoint(checkpoint, c.goalTodos())
	c.persistGoalState(path, data, ok)
}

func (c *Controller) ClearGoal() {
	c.SetGoal("")
}

func (c *Controller) Goal() string {
	return c.goals.goalText()
}

func (c *Controller) GoalStatus() string {
	return c.goals.statusForDisplay()
}

// Compact runs one compaction pass on the executor's session on demand.
// instructions is optional `/compact <focus>` guidance steering what to keep.
func (c *Controller) Compact(ctx context.Context, instructions string) error {
	if c.executor == nil {
		return nil
	}
	// The rotation gate keeps a turn from starting while a manual compaction is
	// building and installing a new model-visible projection.
	if err := c.beginRotation(); err != nil {
		if errors.Is(err, errTurnRunningRotation) {
			return fmt.Errorf("cannot compact while a turn is running")
		}
		return err
	}
	defer c.endRotation()
	return c.executor.CompactNow(ctx, instructions)
}

// maybeSessionStart fires the SessionStart hook exactly once per session, lazily
// on the first turn — by then the sink/notify is wired, and a resumed session
// fires it too (its first post-resume turn).
func (c *Controller) maybeSessionStart(ctx context.Context) {
	c.hooks.SetSessionID(c.parentSessionID())
	c.mu.Lock()
	if c.startedOnce {
		c.mu.Unlock()
		return
	}
	c.startedOnce = true
	c.mu.Unlock()
	c.enqueueHookContexts(c.hooks.SessionStart(ctx))
	c.extensionSessionEvent(extension.PointSessionStart, dispatch.PhaseStart, c.SessionPath())
}

// NewSession snapshots the current conversation, rotates to a fresh file, and
// resets the executor to a clean session carrying the same base system prompt.
// Session-owned pinned context intentionally starts empty. It ends the old
// session and starts the new one for lifecycle hooks.
func (c *Controller) NewSession() error {
	if c.executor == nil {
		return nil
	}
	// Claim the rotation gate for the whole snapshot-then-swap sequence. A bare
	// `if c.running` check released before Snapshot() left a window where a turn
	// could start during the snapshot and then have its live session replaced by
	// the SetSession below. Submit ("/new") and the bot gateway call this
	// asynchronously, so the gate is load-bearing, not defensive.
	if err := c.beginRotation(); err != nil {
		return err
	}
	defer c.endRotation()
	// Retire asynchronous recovery writes before Snapshot publishes the final
	// old-session checkpoint. Otherwise an earlier write can outlive the path
	// rotation (or process teardown) and race cleanup of the old session.
	oldPath := c.SessionPath()
	c.flushRecoveryPersistence(oldPath)
	if err := c.Snapshot(); err != nil {
		return err
	}
	// session.rotate: the session_policy owner rules on the rotation before
	// anything is torn down, so its failure (required-class) aborts the
	// rotation cleanly. SessionPath is the file being rotated away from; the
	// fresh path arrives with the session.start event below.
	if err := c.extensionSessionPhase(context.Background(), extension.PointSessionRotate, dispatch.PhaseRotate, oldPath); err != nil {
		return err
	}
	c.hooks.SessionEnd(context.Background(), "clear")
	c.extensionSessionEvent(extension.PointSessionEnd, dispatch.PhaseEnd, oldPath)
	freshPath := oldPath
	if c.sessionDir != "" {
		freshPath = agent.NewSessionPath(c.sessionDir, c.label)
	}
	freshSession := agent.NewSession(c.basePrompt())
	commitTransition, err := c.prepareSessionTransition(freshPath, "new", freshSession)
	if err != nil {
		return fmt.Errorf("bind new session: %w", err)
	}
	// Hold snapshotMu across the swap so an in-flight save cannot pair the old
	// path with the fresh session (or the fresh path with the old session).
	c.snapshotMu.Lock()
	commitTransition.publish()
	c.bindExecutorProjection(c.SessionPath(), false)
	if c.guardianSess != nil {
		c.guardianSess.Reset()
	}
	c.ResetPlannerSession()
	c.rebindCheckpoints(freshPath)
	c.resetRecoveryForNewSession(freshPath)
	c.rotateSessionTemp()
	c.snapshotMu.Unlock()
	// Old session keeps its inbox (paused); the fresh session starts empty.
	c.pauseInboxOnRotate()
	c.rebindInbox()
	// A new session starts with no active goal: without this, a running goal's
	// text kept injecting into the fresh session's first turns. The old
	// session's goal-state sidecar was persisted before the rotation and stays
	// intact, so resuming it restores its goal; the cleared state below lands
	// on the NEW path (rebindCheckpoints just moved it).
	c.ClearGoal()
	c.mu.Lock()
	c.startedOnce = true // NewSession fires SessionStart itself; don't re-fire on the next turn
	c.mu.Unlock()
	c.hooks.SetSessionID(c.parentSessionID())
	c.enqueueHookContexts(c.hooks.SessionStart(context.Background(), "clear"))
	c.extensionSessionEvent(extension.PointSessionStart, dispatch.PhaseStart, c.SessionPath())
	c.clearSessionWriteAccess()
	return nil
}

// ClearSession discards the current conversation without preserving it in
// resume/history, then rotates to a clean session carrying the same base system
// prompt and no pinned context.
func (c *Controller) ClearSession() error {
	if c.executor == nil {
		return nil
	}
	// Same rotation gate as NewSession: hold it across the whole
	// destroy-then-swap so a turn cannot start during the sequence and have its
	// live session replaced.
	if err := c.beginRotation(); err != nil {
		if errors.Is(err, errTurnRunningRotation) {
			return fmt.Errorf("cannot clear while a turn is running: %w", err)
		}
		return err
	}
	defer c.endRotation()
	c.mu.Lock()
	oldPath := c.sessionPath
	c.mu.Unlock()
	preMarkedCleanup := c.hasUnfinishedSessionJobs(oldPath)
	if preMarkedCleanup {
		if err := agent.MarkCleanupPending(oldPath, "clear"); err != nil {
			return err
		}
	}
	// Retire the old recovery state before deleting its artifacts. Async gate
	// snapshots are path-bound, so wait for every already-scheduled old-path
	// write; otherwise one can recreate the sidecar after removeSessionArtifacts.
	c.loadRecoveryState("")
	c.flushRecoveryPersistence(oldPath)
	// session.rotate: the session_policy owner rules on the rotation before any
	// artifact is destroyed, so its failure (required-class) aborts the clear
	// with the old session fully intact. SessionPath is the file being rotated
	// away from; the fresh path arrives with the session.start event below.
	if err := c.extensionSessionPhase(context.Background(), extension.PointSessionRotate, dispatch.PhaseRotate, oldPath); err != nil {
		return err
	}
	// Hold snapshotMu from artifact removal through the swap: a save slipping
	// in between would resurrect the just-removed transcript, and one that
	// overlapped the swap could pair the old path with the fresh session.
	c.snapshotMu.Lock()
	destroy := c.BeginDestroySession(oldPath)
	if !destroy.Async {
		if err := removeSessionArtifacts(oldPath); err != nil {
			destroy.Finish()
			c.snapshotMu.Unlock()
			return err
		}
		destroy.Finish()
	}
	freshPath := oldPath
	if c.sessionDir != "" {
		freshPath = agent.NewSessionPath(c.sessionDir, c.label)
	}
	freshSession := agent.NewSession(c.basePrompt())
	commitTransition, err := c.prepareSessionTransition(freshPath, "clear", freshSession)
	if err != nil {
		if destroy.Async {
			destroy.Finish()
		}
		c.snapshotMu.Unlock()
		return fmt.Errorf("bind cleared session: %w", err)
	}
	c.hooks.SessionEnd(context.Background(), "clear")
	c.extensionSessionEvent(extension.PointSessionEnd, dispatch.PhaseEnd, oldPath)
	commitTransition.publish()
	c.bindExecutorProjection(c.SessionPath(), false)
	if c.guardianSess != nil {
		c.guardianSess.Reset()
	}
	c.ResetPlannerSession()
	c.rebindCheckpoints(freshPath)
	c.resetRecoveryForNewSession(freshPath)
	c.rotateSessionTemp()
	c.snapshotMu.Unlock()
	c.rebindInbox()
	// Same contract as NewSession: the fresh session starts with no active goal.
	c.ClearGoal()
	c.mu.Lock()
	c.startedOnce = true
	c.mu.Unlock()
	c.hooks.SetSessionID(c.parentSessionID())
	c.enqueueHookContexts(c.hooks.SessionStart(context.Background(), "clear"))
	c.extensionSessionEvent(extension.PointSessionStart, dispatch.PhaseStart, c.SessionPath())
	c.clearSessionWriteAccess()
	if destroy.Async {
		go func() {
			result := destroy.Wait()
			if result.HasTimedOut() && destroy.WaitAll != nil {
				if err := agent.MarkCleanupPending(oldPath, "clear"); err != nil {
					c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "mark cleanup pending failed: " + err.Error()})
				}
				destroy.WaitAll()
			}
			if err := removeSessionArtifacts(oldPath); err != nil {
				c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "clear session cleanup failed: " + err.Error()})
			}
			destroy.Finish()
		}()
	}
	return nil
}

func (c *Controller) hasUnfinishedSessionJobs(sessionPath string) bool {
	if c.jobs == nil {
		return false
	}
	return c.jobs.HasUnfinishedForSession(agent.BranchID(sessionPath))
}

func removeSessionArtifacts(path string) error {
	if path == "" {
		return nil
	}
	if err := jobs.RemoveArtifacts(path); err != nil {
		return err
	}
	remove := []string{path}
	// Sidecars include the event log — the authoritative transcript. Leaving
	// it behind would both leak the cleared conversation and let LoadSession
	// resurrect it on the recycled path. The guardian transcript saves through
	// the same session layer, so its sidecars are swept too.
	remove = append(remove, store.SessionSidecarFiles(path)...)
	remove = append(remove, guardian.PathFor(path), guardian.CursorPathFor(path))
	remove = append(remove, store.SessionSidecarFiles(guardian.PathFor(path))...)
	for _, p := range remove {
		if p == "" {
			continue
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := sessioninbox.RemoveDir(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if dir := ckptDir(path); dir != "" {
		if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := agent.DeleteSubagentsByParent(filepath.Dir(path), agent.BranchID(path)); err != nil {
		return err
	}
	if err := agent.ClearCleanupPending(path); err != nil {
		return err
	}
	return nil
}

// RemoveSessionArtifacts removes a transcript and every durable artifact owned
// by it. Remote runtimes use this when a newly-created fork fails before it can
// be registered as a live session.
func RemoveSessionArtifacts(path string) error {
	return removeSessionArtifacts(path)
}

// ReconcileCleanupPending retries physical cleanup for logically removed
// sessions that were left behind by a previous process.
func ReconcileCleanupPending(dir string) error {
	return agent.ReconcileCleanupPending(dir, func(item agent.CleanupPendingInfo) error {
		return removeSessionArtifacts(item.SessionPath)
	})
}

// RewindScope selects what a Rewind restores.
type RewindScope int

const (
	RewindCode         RewindScope = iota // files only
	RewindConversation                    // message log only
	RewindBoth                            // both
)

// Checkpoints lists the session's rewind points (one per user turn), oldest first.
//
// Each Meta.Prompt is reduced to what the user typed. A checkpoint opens with
// the composed turn, so the stored prompt can carry the plan-mode marker and
// transient blocks; every consumer of this list is a label (the rewind picker,
// the desktop change list, the workbench projection) and the picker also
// restores the prompt into the composer, so composed text must not reach them.
// Stripping on read rather than only on write keeps checkpoints already on disk
// readable — they were recorded composed.
func (c *Controller) Checkpoints() []checkpoint.Meta {
	metas := c.checkpoints.list()
	for i := range metas {
		metas[i].Prompt = StripComposePrefixes(metas[i].Prompt)
	}
	return metas
}

func (c *Controller) CheckpointFileState(path string) (checkpoint.FileState, bool) {
	return c.checkpoints.fileState(path)
}

func (c *Controller) CheckpointTurnsByMessageIndex() map[int]int {
	return c.checkpoints.turnsByMessageIndex()
}

// rewindFail emits the error as a Warn notice (so a frontend that swallows the
// returned error — e.g. the desktop bridge's .catch — still shows the user why
// the rewind did nothing) and returns it.
func (c *Controller) rewindFail(err error) error {
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: err.Error()})
	return err
}

// Rewind is implemented in rewind.go (transactional conversation+file restore).

// Fork branches the conversation at the start of turn into a NEW session file,
// preserving the current one as the branch point, and switches to the branch. Code
// is untouched (it's a conversation operation). Like a conversation rewind it needs
// the live boundary, so it is unavailable for resumed-session turns and refused
// while a turn runs. Returns the new session path.
func (c *Controller) Fork(turn int) (string, error) {
	return c.ForkNamed(turn, "")
}

func (c *Controller) ForkNamed(turn int, name string) (string, error) {
	return c.forkNamed(turn, name, true)
}

// ForkSession copies the conversation at the start of turn into a new session
// file without switching this controller to it. Desktop uses this to open the
// branch in a new tab while the source tab keeps its current transcript.
func (c *Controller) ForkSession(turn int, name string) (string, error) {
	return c.forkNamed(turn, name, false)
}

func (c *Controller) forkNamed(turn int, name string, switchToFork bool) (string, error) {
	if err := c.beginRotation(); err != nil {
		if errors.Is(err, errTurnRunningRotation) {
			return "", c.rewindFail(fmt.Errorf("cannot fork while a turn is running"))
		}
		return "", c.rewindFail(err)
	}
	defer c.endRotation()
	return c.forkNamedReady(turn, name, switchToFork)
}

func (c *Controller) forkNamedReady(turn int, name string, switchToFork bool) (string, error) {
	if c.executor == nil {
		return "", c.rewindFail(fmt.Errorf("checkpoints unavailable"))
	}
	if c.sessionDir == "" {
		return "", c.rewindFail(fmt.Errorf("fork needs session persistence, which is disabled"))
	}
	boundary, hasBound := c.checkpoints.boundary(turn)
	if !hasBound {
		return "", c.rewindFail(fmt.Errorf("fork unavailable for turn %d (resumed session)", turn))
	}

	// Persist the current conversation first so the branch point survives, then
	// seed a fresh session with the messages up to the fork and switch to it.
	if err := c.Snapshot(); err != nil {
		slog.Warn("controller: pre-fork snapshot", "err", err)
	}
	parentPath := c.SessionPath()
	parentID := agent.BranchID(parentPath)
	src := c.executor.Session().Snapshot()
	if boundary > len(src) {
		boundary = len(src)
	}
	forked := append([]provider.Message(nil), src[:boundary]...)
	sess := agent.NewSession("")
	sess.Messages = forked

	newPath := agent.NewSessionPath(c.sessionDir, c.label)
	if err := sess.SaveIfAbsent(newPath); err != nil {
		return "", c.rewindFail(err)
	}
	if _, err := sess.CopyValidContextProjection(parentPath, newPath); err != nil {
		slog.Warn("controller: fork did not inherit context projection", "err", err)
	}
	forkPreview, forkTurns := agent.SessionPreviewFromMessages(forked)
	if err := agent.SaveBranchMeta(newPath, agent.BranchMeta{
		Name:             strings.TrimSpace(name),
		ParentID:         parentID,
		ForkTurn:         turn,
		ForkMessageIndex: boundary,
		Preview:          forkPreview,
		Turns:            forkTurns,
		SchemaVersion:    agent.BranchMetaCountsVersion,
	}); err != nil {
		return "", c.rewindFail(err)
	}
	if switchToFork {
		commitTransition, err := c.prepareSessionTransition(newPath, "fork", sess)
		if err != nil {
			return "", c.rewindFail(fmt.Errorf("bind fork session: %w", err))
		}
		// See snapshotMu: the swap must not interleave with an in-flight save.
		c.snapshotMu.Lock()
		commitTransition.publish()
		// Load the child sidecar when the covered prefix survived the fork. The
		// loader rebinds its lineage key without touching the parent's sidecar.
		c.bindExecutorProjection(newPath, true)
		c.ResetPlannerSession()
		c.rebindCheckpoints(newPath)
		// A historical fork rewinds before later failures, so it starts with no
		// active recovery event even though it inherits the session preference.
		c.loadRecoveryState(newPath)
		if c.guardianSess != nil {
			c.guardianSess.Reset()
		}
		// Switching into the fork is a new logical session for temporary files.
		c.rotateSessionTemp()
		c.snapshotMu.Unlock()
	}
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
		Text: fmt.Sprintf("forked conversation at turn %d into a new session", turn)})
	return newPath, nil
}

func (c *Controller) CheckpointHasBoundary(turn int) bool {
	boundary, ok := c.checkpoints.boundary(turn)
	if !ok {
		return false
	}
	// After compaction the key may still exist but the boundary value is
	// stale (it points past the truncated message log).  Treat those
	// turns the same as "no boundary" so the UI can disable the button.
	// Len is lock-guarded: this runs on frontend goroutines while a turn appends.
	return boundary <= c.executor.Session().Len()
}

// Branch copies the current conversation into a child branch and switches to it.
// Unlike Fork, it branches at the current tip and does not require a checkpoint.
func (c *Controller) Branch(name string) (string, error) {
	if c.executor == nil {
		return "", c.rewindFail(fmt.Errorf("branch unavailable"))
	}
	if c.sessionDir == "" {
		return "", c.rewindFail(fmt.Errorf("branch needs session persistence, which is disabled"))
	}
	// Hold the rotation gate across the Snapshot and the switch below so a turn
	// cannot start mid-branch and then have its session replaced.
	if err := c.beginRotation(); err != nil {
		if errors.Is(err, errTurnRunningRotation) {
			return "", c.rewindFail(fmt.Errorf("cannot branch while a turn is running"))
		}
		return "", c.rewindFail(err)
	}
	defer c.endRotation()
	if !c.executor.Session().HasContent() {
		return "", c.rewindFail(fmt.Errorf("nothing to branch yet"))
	}
	if err := c.Snapshot(); err != nil {
		return "", c.rewindFail(err)
	}
	parentPath := c.SessionPath()
	parentID := agent.BranchID(parentPath)
	src := c.executor.Session().Snapshot()
	branched := append([]provider.Message(nil), src...)
	sess := agent.NewSession("")
	sess.Messages = branched

	newPath := agent.NewSessionPath(c.sessionDir, c.label)
	if err := sess.SaveIfAbsent(newPath); err != nil {
		return "", c.rewindFail(err)
	}
	if _, err := sess.CopyValidContextProjection(parentPath, newPath); err != nil {
		slog.Warn("controller: branch did not inherit context projection", "err", err)
	}
	branchPreview, branchTurns := agent.SessionPreviewFromMessages(branched)
	if err := agent.SaveBranchMeta(newPath, agent.BranchMeta{
		Name:             strings.TrimSpace(name),
		ParentID:         parentID,
		ForkTurn:         -1,
		ForkMessageIndex: len(branched),
		Preview:          branchPreview,
		Turns:            branchTurns,
		SchemaVersion:    agent.BranchMetaCountsVersion,
	}); err != nil {
		return "", c.rewindFail(err)
	}
	commitTransition, err := c.prepareSessionTransition(newPath, "branch", sess)
	if err != nil {
		return "", c.rewindFail(fmt.Errorf("bind branch session: %w", err))
	}
	// See snapshotMu: the swap must not interleave with an in-flight save.
	c.snapshotMu.Lock()
	commitTransition.publish()
	c.bindExecutorProjection(newPath, true)
	c.ResetPlannerSession()
	c.rebindCheckpoints(newPath)
	if c.guardianSess != nil {
		c.guardianSess.Reset()
	}
	c.carryRecoveryState(newPath)
	c.rotateSessionTemp()
	c.snapshotMu.Unlock()
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
		Text: fmt.Sprintf("created branch %s", agent.BranchID(newPath))})
	return newPath, nil
}

// Branches lists saved conversation branches in this controller's session dir.
func (c *Controller) Branches() ([]agent.BranchInfo, error) {
	if c.sessionDir == "" {
		return nil, fmt.Errorf("session persistence is disabled")
	}
	if err := c.Snapshot(); err != nil {
		return nil, err
	}
	return agent.ListBranches(c.sessionDir)
}

func (c *Controller) SwitchBranch(ref string) (agent.BranchInfo, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return agent.BranchInfo{}, c.rewindFail(fmt.Errorf("usage: /switch <branch id|name>"))
	}
	// Hold the rotation gate across the branch listing/load and the switch so a
	// turn cannot start between the check and the SetSession below.
	if err := c.beginRotation(); err != nil {
		if errors.Is(err, errTurnRunningRotation) {
			return agent.BranchInfo{}, c.rewindFail(fmt.Errorf("cannot switch branches while a turn is running"))
		}
		return agent.BranchInfo{}, c.rewindFail(err)
	}
	defer c.endRotation()
	branches, err := c.Branches()
	if err != nil {
		return agent.BranchInfo{}, c.rewindFail(err)
	}
	match, err := resolveBranch(branches, ref)
	if err != nil {
		return agent.BranchInfo{}, c.rewindFail(err)
	}
	if !agent.IsVisibleSession(match.Path) {
		return agent.BranchInfo{}, c.rewindFail(fmt.Errorf("branch %q not found", ref))
	}
	loaded, err := agent.LoadSession(match.Path)
	if err != nil {
		return agent.BranchInfo{}, c.rewindFail(err)
	}
	commitTransition, err := c.prepareSessionTransition(match.Path, "switch", loaded)
	if err != nil {
		return agent.BranchInfo{}, c.rewindFail(fmt.Errorf("bind switched session: %w", err))
	}
	// See snapshotMu: the swap must not interleave with an in-flight save.
	c.snapshotMu.Lock()
	commitTransition.publish()
	c.bindExecutorProjection(match.Path, true)
	c.ResetPlannerSession()
	c.rebindCheckpoints(match.Path)
	c.restoreTerminalGoalTodos(match.Path)
	c.loadGuardianSession()
	c.loadRecoveryState(match.Path)
	c.rotateSessionTemp()
	c.snapshotMu.Unlock()
	c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo,
		Text: fmt.Sprintf("switched to branch %s", branchDisplayName(match))})
	return match, nil
}

// ResolveBranchRef resolves a /switch-style branch reference (id, unique
// prefix, name, or path) against a branch listing, using the same matching
// rules as SwitchBranch. Frontends use it to learn the target session path
// before switching — e.g. to move their session lease first.
func ResolveBranchRef(branches []agent.BranchInfo, ref string) (agent.BranchInfo, error) {
	return resolveBranch(branches, strings.TrimSpace(ref))
}

func resolveBranch(branches []agent.BranchInfo, ref string) (agent.BranchInfo, error) {
	refLower := strings.ToLower(ref)
	var matches []agent.BranchInfo
	for _, b := range branches {
		nameLower := strings.ToLower(strings.TrimSpace(b.Name))
		switch {
		case b.ID == ref || strings.EqualFold(b.ID, ref):
			return b, nil
		case b.Name != "" && nameLower == refLower:
			matches = append(matches, b)
		case strings.HasPrefix(strings.ToLower(b.ID), refLower):
			matches = append(matches, b)
		case strings.HasPrefix(strings.ToLower(shortBranchID(b.ID)), refLower):
			matches = append(matches, b)
		case b.Path == ref:
			return b, nil
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return agent.BranchInfo{}, fmt.Errorf("branch %q is ambiguous", ref)
	}
	return agent.BranchInfo{}, fmt.Errorf("branch %q not found", ref)
}

func branchDisplayName(b agent.BranchInfo) string {
	if strings.TrimSpace(b.Name) != "" {
		return fmt.Sprintf("%s (%s)", b.Name, b.ID)
	}
	return b.ID
}

// SummarizeFrom and SummarizeUpTo preserve the historical turn-index API while
// changing only the model-visible context projection. The canonical transcript
// and checkpoint boundaries remain available for rewind, undo, and fork.
func (c *Controller) SummarizeFrom(ctx context.Context, turn int) error {
	return c.summarizeAt(ctx, turn, true)
}

func (c *Controller) SummarizeUpTo(ctx context.Context, turn int) error {
	return c.summarizeAt(ctx, turn, false)
}

func (c *Controller) summarizeAt(ctx context.Context, turn int, from bool) error {
	if c.executor == nil {
		return c.rewindFail(fmt.Errorf("checkpoints unavailable"))
	}
	// Hold the rotation gate from the checkpoint-boundary lookup through
	// projection installation so a turn cannot start against an intermediate
	// context view.
	if err := c.beginRotation(); err != nil {
		if errors.Is(err, errTurnRunningRotation) {
			return c.rewindFail(fmt.Errorf("cannot summarize while a turn is running"))
		}
		return c.rewindFail(err)
	}
	defer c.endRotation()
	boundary, hasBound := c.checkpoints.boundary(turn)
	if !hasBound {
		return c.rewindFail(fmt.Errorf("summarize unavailable for turn %d (resumed session)", turn))
	}
	var err error
	if from {
		err = c.executor.SummarizeFrom(ctx, boundary)
	} else {
		err = c.executor.SummarizeUpTo(ctx, boundary)
	}
	if err != nil {
		return c.rewindFail(err)
	}
	return nil
}

// Resume seeds the session from a loaded transcript and pins the active file to
// its path so auto-save keeps appending there.
//
// When the controller already has a different non-empty session path, Resume
// rotates the private temporary generation so the loaded conversation cannot
// see the previous session's temporary files. Same-path Resume (hot rebuild
// migration via AdoptHistory) keeps the generation.
func (c *Controller) Resume(s *agent.Session, path string) {
	// See snapshotMu: the swap must not interleave with an in-flight save.
	// recoverInterruptedTurn and maybeColdResumePrune snapshot on their own,
	// so they stay outside the locked section (snapshotMu is not reentrant).
	prevPath := c.SessionPath()
	c.snapshotMu.Lock()
	if c.executor != nil {
		c.executor.SetSession(s)
	}
	c.mu.Lock()
	c.sessionPath = path
	c.guardianPath = guardian.PathFor(path)
	c.mu.Unlock()
	c.bindExecutorProjection(path, true)
	c.ResetPlannerSession()
	c.setActiveJobSession(path)
	c.rebindCheckpoints(path)
	migPath, migData, migrated, legacy := c.goals.restoreFromState(path)
	if !c.restorePendingLegacyGoal(legacy) && migrated {
		c.persistGoalState(migPath, migData, true)
	}
	if c.executor != nil {
		c.executor.RestoreDeliveryCheckpoint(c.goals.deliveryState())
	}
	c.restoreTerminalGoalTodos(path)
	c.loadGuardianSession()
	c.loadRecoveryState(path)
	if shouldRotateSessionTempOnResume(prevPath, path) {
		c.rotateSessionTemp()
	}
	c.snapshotMu.Unlock()
	c.rebindInbox()
	c.recoverCheckpointTransactions()
	c.recoverInterruptedTurn(path)
	c.maybeColdResumePrune(path)
	// session.load: Resume has no failure channel, so the session_policy
	// strategy is advisory this stage — a required-class failure is surfaced
	// as a warning and the load stands. The event still carries the final
	// (possibly owner-adjusted) phase payload.
	if err := c.extensionSessionPhase(context.Background(), extension.PointSessionLoad, dispatch.PhaseLoad, path); err != nil {
		c.extensionWarn("session policy failed at session.load", err)
	}
}

func shouldRotateSessionTempOnResume(prevPath, nextPath string) bool {
	prevPath = strings.TrimSpace(prevPath)
	nextPath = strings.TrimSpace(nextPath)
	if prevPath == "" || nextPath == "" {
		return false
	}
	return filepath.Clean(prevPath) != filepath.Clean(nextPath)
}

func (c *Controller) loadGuardianSession() {
	if c.guardianSess == nil {
		return
	}
	c.guardianSess.Reset()
	path := c.guardianPath
	if path == "" {
		return
	}
	if err := c.guardianSess.Load(path); err != nil && !os.IsNotExist(err) {
		slog.Warn("controller: load guardian session", "err", err)
	}
}

// ResetPlannerSession clears the planner's conversation history so the next
// plan starts fresh. In dual-model (Plan+Execute) mode, this prevents stale
// planner output from a previous session or tab from contaminating the current
// executor's handoff. Safe to call on a single-model controller (no-op).
func (c *Controller) ResetPlannerSession() {
	runner, ok := c.runner.(plannerSessionResetter)
	if ok {
		runner.ResetPlannerSession()
	}
}

// cacheColdAfter resolves how long the active provider keeps a prompt prefix
// cached. A session idle longer than this resumes against a cold cache, so a
// history rewrite at that moment costs no extra cache misses — it only shrinks
// the full-price first request. The TTL is vendor-aware: DeepSeek/unknown
// 24h (legacy default deliberately preserved), DashScope 5m, Anthropic 5m.
// Users can override per-provider
// with cache_ttl_minutes in config.toml.
func (c *Controller) cacheColdAfter() time.Duration {
	if c.testCacheColdAfter != 0 {
		if c.testCacheColdAfter == -1 {
			return 0
		}
		return c.testCacheColdAfter
	}
	// 查询路径只读：LoadForRootReadOnly 不触发配置迁移写盘（评审 #7168
	// 第 4 点）；失败时保守回退 24h（DeepSeek/未知 vendor 默认），避免
	// 把 cache TTL 过期误当成历史改写信号（resume 只记录 warm/cold/unknown）。
	cfg, err := config.LoadForRootReadOnly(c.workspaceRoot)
	if err != nil {
		return 24 * time.Hour
	}
	ref := c.modelRef
	if ref == "" {
		ref = cfg.DefaultModel
	}
	entry, ok := cfg.ResolveModel(ref)
	if !ok {
		return 24 * time.Hour
	}
	return entry.EffectiveCacheTTL()
}

// Snapshot writes the executor's conversation to the active session file. No-op
// when the executor is absent or the session has never been used (no user
// interaction). Returns errNoSessionPath when there IS content but no resolved
// path, so a misconfigured deployment surfaces instead of dropping data.
// Called after every turn so a crash loses at most one in-flight prompt.
func (c *Controller) Snapshot() error {
	return c.snapshot(false, false, false)
}

// SnapshotForShutdown performs the final session snapshot and, only when the
// compatibility file lock remains held for the full bounded wait, persists the
// in-memory transcript to a distinct recovery branch before teardown proceeds.
// Other snapshot errors retain their normal behavior and remain visible to the
// caller.
func (c *Controller) SnapshotForShutdown() error {
	return c.snapshot(false, false, true)
}

// SnapshotActivity writes the active conversation and marks the session as
// recently active. Use it only after a real user/model turn changes the
// transcript; switch/close snapshots should call Snapshot so they do not reorder
// recent-session pickers.
func (c *Controller) SnapshotActivity() error {
	return c.snapshot(true, false, false)
}

// SnapshotRewrite persists an intentional history rewrite, such as rewind or
// manual compaction. Ordinary autosave paths should use Snapshot so stale
// controllers cannot overwrite a newer transcript.
func (c *Controller) SnapshotRewrite() error {
	return c.snapshot(false, true, false)
}

func (c *Controller) snapshot(markActivity, forceRewrite, shutdownRecovery bool) error {
	_, err := c.snapshotWithDurability(markActivity, forceRewrite, shutdownRecovery)
	return err
}

// midTurnSnapshotInterval is atomic (nanoseconds) so a test shrinking it
// cannot race a previous test's still-parking autosave goroutine.
var midTurnSnapshotInterval atomic.Int64

func init() { midTurnSnapshotInterval.Store(int64(30 * time.Second)) }

// autosaveWhileRunning snapshots the session periodically while a turn runs,
// so an abrupt kill (SSH drop, force-quit) loses at most one interval of a
// long turn instead of all of it (#3772). Session.Save copies under the lock
// and replaces the file atomically, so racing the turn's appends is safe.
func (c *Controller) autosaveWhileRunning(ctx context.Context) {
	t := time.NewTicker(time.Duration(midTurnSnapshotInterval.Load()))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.snapshot(false, false, false); err != nil {
				slog.Warn("controller: mid-turn snapshot", "err", err)
			}
		}
	}
}

// snapshotWithDurability reports whether the canonical transcript reached disk
// even when a later sidecar update failed. Callers that guard a crash marker
// need this distinction: a metadata error must not make a complete transcript
// look like an in-memory-only turn.
func (c *Controller) snapshotWithDurability(markActivity, forceRewrite, shutdownRecovery bool) (bool, error) {
	c.snapshotMu.Lock()
	defer c.snapshotMu.Unlock()

	c.mu.Lock()
	path := c.sessionPath
	modelRef := c.modelRef
	c.mu.Unlock()
	if c.executor == nil {
		return false, nil
	}
	s := c.executor.Session()
	if !s.HasContent() {
		// Nothing to persist yet (e.g. a fresh session with only a system
		// prompt) — staying quiet here is correct, not a data-loss path.
		return false, nil
	}
	if !s.HasSystemMessage() {
		// The session has user/assistant/tool messages but no leading system
		// prompt.  Persisting it would create a session file that, when
		// reloaded, has no agent-identity contract — the model falls back to
		// its training-data defaults, giving wrong answers to identity
		// queries ("who are you?").  Log the anomaly so the root cause
		// (typically an empty sysPrompt reaching NewSession) can be
		// diagnosed, then refuse to write a corrupted transcript.
		slog.Warn("controller: refusing to snapshot session with content but no system message",
			"label", c.Label(), "session_dir", c.SessionDir(), "message_count", len(s.Snapshot()))
		return false, nil
	}
	if path == "" {
		// There IS content but nowhere to write it: this silently dropped whole
		// bot conversations (#4414). Surface it loudly instead of returning nil
		// so the missing session path can be diagnosed and fixed at the source.
		slog.Warn("controller: session has content but no session path; conversation will not be persisted",
			"label", c.Label(), "session_dir", c.SessionDir())
		return false, errNoSessionPath
	}
	// session.save: the session_policy owner rules on the impending save; a
	// failure (required-class) vetoes the write. The event goes out after a
	// successful save carrying the final payload. The early no-content and
	// no-path returns above are not saves and stay unobserved. Conflict
	// recovery below may rewrite the path; the phase payload reports the path
	// the save targeted.
	savePayload, strategyErr := c.extensionSessionStrategy(context.Background(), extension.PointSessionSave, dispatch.PhaseSave, path)
	if strategyErr != nil {
		return false, strategyErr
	}
	err, forceRewrite := persistSessionSnapshot(s, path, forceRewrite)
	if authoritySaveError(err) {
		// Missing/stale authority must not enter diverged/recovery. Frontends
		// rebind the lease or surface the typed error.
		return false, err
	}
	if err != nil {
		if shutdownRecovery && errors.Is(err, agent.ErrSessionFileLockHeld) {
			recoveredPath, recoverErr := c.recoverShutdownSnapshot(path, err)
			if recoverErr != nil {
				return false, recoverErr
			}
			path = recoveredPath
			s = c.executor.Session()
			err = nil
		}
	}
	if err != nil {
		if errors.Is(err, agent.ErrSessionExternallyRemoved) {
			recoveredPath, recoverErr := c.recoverExternallyRemovedSession(path, err)
			if recoverErr != nil {
				return false, recoverErr
			}
			path = recoveredPath
			s = c.executor.Session()
			err = nil
		}
	}
	if err != nil {
		if !errors.Is(err, agent.ErrSessionSnapshotConflict) {
			return false, err
		}
		recoveredPath, outcome, recoverErr := c.recoverSnapshotConflict(path, err, forceRewrite)
		if recoverErr != nil {
			if shutdownRecovery && errors.Is(recoverErr, agent.ErrSessionFileLockHeld) {
				recoveredPath, recoverErr = c.recoverShutdownSnapshot(path, recoverErr)
				if recoverErr != nil {
					return false, recoverErr
				}
				path = recoveredPath
				s = c.executor.Session()
			} else {
				return false, recoverErr
			}
		} else {
			if outcome == conflictDropped {
				return false, nil
			}
			// Whatever recovery did — adopted the disk transcript, isolated the
			// depth-capped copy, or forked — the rewrite baseline lives on
			// the session object and was advanced by the save that succeeded, so
			// there is nothing to re-anchor here.
			path = recoveredPath
			s = c.executor.Session()
		}
	}
	// Persist guardian session so the prefix cache stays warm after restart.
	if c.guardianSess != nil {
		gp := c.guardianPath
		if gp != "" {
			if gerr := c.guardianSess.Save(gp); gerr != nil {
				slog.Warn("controller: guardian snapshot", "err", gerr)
			}
		}
	}
	transcriptDurable := true
	// Persist recovery gate state so unresolved checkpoints survive restart.
	c.saveRecoveryState(path)
	// Record the listing-only sidecar fields (model, preview, user-turn count)
	// straight from the in-memory conversation, so the sidebar and resume picker
	// never have to decode the whole .jsonl just to show them. markActivity bumps
	// UpdatedAt exactly like the previous TouchBranchMeta did; false preserves it
	// like SetBranchModelPreserveUpdated. The single write subsumes the old
	// EnsureBranchMeta / SetBranchModel / TouchBranchMeta sequence.
	preview, turns := agent.SessionPreviewFromMessages(s.Snapshot())
	if err := updateSessionListingProjection(s, path, modelRef, preview, turns, markActivity); err != nil {
		return transcriptDurable, err
	}
	c.extensionSessionPayloadEvent(extension.PointSessionSave, savePayload)
	return transcriptDurable, nil
}

func updateSessionListingProjection(s *agent.Session, path, modelRef, preview string, turns int, markActivity bool) error {
	persisted, ok := s.PersistedState(path)
	if !ok {
		return fmt.Errorf("session persistence baseline missing after save")
	}
	_, err := agent.UpdateSessionListingProjectionIfCurrent(path, modelRef, preview, turns, markActivity, persisted)
	return err
}

func (c *Controller) recoverExternallyRemovedSession(path string, saveErr error) (string, error) {
	if c.executor == nil || strings.TrimSpace(path) == "" {
		return "", saveErr
	}
	const reason = "session removed while open"
	req := SessionRecoveryRequest{OriginalPath: path, Reason: reason, Mode: "external-removal"}
	meta := agent.BranchMeta{}
	if c.sessionRecoveryMeta != nil {
		meta = c.sessionRecoveryMeta(req)
	}
	info, err := c.executor.Session().SaveConflictRecoveryBranch(agent.RecoveryBranchOptions{
		OriginalPath: path,
		Reason:       reason,
		BranchMeta:   meta,
	})
	if err != nil {
		return "", fmt.Errorf("preserve externally removed session: %w", err)
	}
	if err := c.commitRecoveredSession(path, reason, info); err != nil {
		return "", err
	}
	appendSnapshotConflictDiagnostic(path, "external-removal", "moved_to_stable_recovery", saveErr, info.Path, info.Existing)
	slog.Warn("controller: active session was removed externally; moved runtime to stable recovery path",
		"path", path, "recovery", info.Path, "existing", info.Existing)
	c.sink.Emit(sessionRecoveryNotice(event.NoticeCodeSessionRecoveryForked,
		"the open session file was removed outside Reasonix; your active conversation was preserved as one recovery copy"))
	return info.Path, nil
}

// snapshotConflictLogAttrs flattens a snapshot-conflict error into slog attrs.
// Field reports of #6069-class "session changed on disk" spam are only
// diagnosable when the logs say which trigger fired and what the revision
// ledger looked like, so every recoverSnapshotConflict outcome logs these.
func snapshotConflictLogAttrs(saveErr error, path, mode string) []any {
	attrs := []any{"path", path, "mode", mode}
	var conflict *agent.SessionSnapshotConflictError
	if errors.As(saveErr, &conflict) && conflict != nil {
		attrs = append(attrs,
			"kind", string(conflict.Kind),
			"disk_messages", conflict.ExistingMessages,
			"snapshot_messages", conflict.SnapshotMessages,
			"base_revision", conflict.BaseRevision,
			"disk_revision", conflict.DiskRevision,
		)
	}
	return attrs
}

// conflictOutcome is recoverSnapshotConflict's declared result. Callers act
// on it directly instead of re-deriving what happened from path or session
// pointer comparisons — the misclassification that broke the depth-cap
// rewrite baseline (#6120) hid in exactly that inference.
type conflictOutcome int

const (
	// conflictDropped: nothing was recovered and the disk transcript could
	// not be adopted; this snapshot was deliberately dropped.
	conflictDropped conflictOutcome = iota
	// conflictAdoptedDisk: the executor session object was replaced by the
	// newer disk transcript; adoptDiskSession already reset its baselines.
	conflictAdoptedDisk
	// conflictForkedBranch: the same in-memory session moved to a freshly
	// forked recovery branch path.
	conflictForkedBranch
)

func sessionRecoveryNotice(code, text string) event.Event {
	return event.Event{
		Kind:     event.Notice,
		Level:    event.LevelWarn,
		Audience: event.NoticeAudienceOperator,
		Code:     code,
		Text:     text,
	}
}

func (c *Controller) recoverSnapshotConflict(path string, saveErr error, forceRewrite bool) (string, conflictOutcome, error) {
	if c.executor == nil || strings.TrimSpace(path) == "" {
		return "", conflictDropped, saveErr
	}
	mode := "snapshot"
	if forceRewrite {
		mode = "rewrite"
	}
	logAttrs := snapshotConflictLogAttrs(saveErr, path, mode)
	if kind, ok := agent.SnapshotConflictKind(saveErr); ok && kind == agent.SessionSnapshotConflictStalePrefix {
		if c.adoptDiskSession(path) {
			appendSnapshotConflictDiagnostic(path, mode, "adopted_newer_disk_transcript", saveErr, "", false)
			slog.Warn("controller: snapshot conflict; adopted newer disk transcript", logAttrs...)
			c.sink.Emit(sessionRecoveryNotice(event.NoticeCodeSessionRecoveryAdopted,
				"session changed on disk; adopted the newer transcript"))
			return path, conflictAdoptedDisk, nil
		}
	}
	reason := "snapshot conflict"
	if forceRewrite {
		reason = "rewrite conflict"
	}
	req := SessionRecoveryRequest{OriginalPath: path, Reason: reason, Mode: mode}
	meta := agent.BranchMeta{}
	if c.sessionRecoveryMeta != nil {
		meta = c.sessionRecoveryMeta(req)
	}
	baseRevision, diskRevision := snapshotConflictRevisions(saveErr)
	info, err := c.executor.Session().SaveRecoveryBranch(agent.RecoveryBranchOptions{
		OriginalPath: path,
		Reason:       reason,
		BranchMeta:   meta,
		BaseRevision: baseRevision,
		DiskRevision: diskRevision,
	})
	if err != nil {
		if errors.Is(err, agent.ErrSessionRecoveryNotNeeded) {
			if c.adoptDiskSession(path) {
				appendSnapshotConflictDiagnostic(path, mode, "recovery_not_needed_adopted_disk_transcript", saveErr, "", false)
				slog.Warn("controller: snapshot conflict; recovery not needed, adopted disk transcript", logAttrs...)
				c.sink.Emit(sessionRecoveryNotice(event.NoticeCodeSessionRecoveryAdoptedCovered,
					"session changed on disk; adopted the newer transcript (local changes already covered)"))
				return path, conflictAdoptedDisk, nil
			}
			// Nothing was recovered AND the disk transcript could not be
			// adopted: the snapshot is silently dropped. Leave a trace so
			// "my last turns vanished" reports can be tied to this path.
			appendSnapshotConflictDiagnostic(path, mode, "recovery_not_needed_adopt_failed", saveErr, "", false)
			slog.Warn("controller: snapshot conflict; recovery not needed but disk transcript could not be adopted", logAttrs...)
			return "", conflictDropped, nil
		}
		return "", conflictDropped, fmt.Errorf("recover stale session snapshot: %w", err)
	}
	if err := c.commitRecoveredSession(path, reason, info); err != nil {
		return "", conflictDropped, err
	}
	appendSnapshotConflictDiagnostic(path, mode, "forked_recovery_branch", saveErr, info.Path, info.Existing)
	slog.Warn("controller: snapshot conflict; forked recovery branch",
		append(logAttrs, "recovery", info.Path, "existing", info.Existing)...)
	c.sink.Emit(sessionRecoveryNotice(event.NoticeCodeSessionRecoveryForked,
		"session changed on disk; unsaved local transcript was saved as a conflict copy"))
	return info.Path, conflictForkedBranch, nil
}

func (c *Controller) recoverShutdownSnapshot(path string, saveErr error) (string, error) {
	if c.executor == nil || strings.TrimSpace(path) == "" {
		return "", saveErr
	}
	const reason = "shutdown session file lock timeout"
	req := SessionRecoveryRequest{OriginalPath: path, Reason: reason, Mode: "shutdown"}
	meta := agent.BranchMeta{}
	if c.sessionRecoveryMeta != nil {
		meta = c.sessionRecoveryMeta(req)
	}
	info, err := c.executor.Session().SaveShutdownRecoveryBranch(agent.RecoveryBranchOptions{
		OriginalPath: path,
		Reason:       reason,
		BranchMeta:   meta,
	})
	if err != nil {
		return "", fmt.Errorf("save shutdown recovery branch: %w", err)
	}
	if err := c.commitRecoveredSession(path, reason, info); err != nil {
		return "", err
	}
	appendSnapshotConflictDiagnostic(path, "shutdown", "forked_file_lock_recovery", saveErr, info.Path, info.Existing)
	slog.Warn("controller: shutdown snapshot lock timed out; forked recovery branch",
		"path", path, "recovery", info.Path, "existing", info.Existing)
	c.sink.Emit(sessionRecoveryNotice(event.NoticeCodeSessionShutdownRecoveryForked,
		"session file stayed busy during shutdown; unsaved transcript was saved as a recovery copy"))
	return info.Path, nil
}

func (c *Controller) commitRecoveredSession(originalPath, reason string, info agent.RecoveryBranchInfo) error {
	commit := &sessionRecoveryCommit{}
	recoveryInfo := SessionRecoveryInfo{
		OriginalPath: originalPath,
		RecoveryPath: info.Path,
		Existing:     info.Existing,
		Reason:       reason,
		BaseRevision: info.Meta.BaseRevision,
		DiskRevision: info.Meta.DiskRevision,
		Meta:         info.Meta,
		commit:       commit,
	}
	if onSessionRecovered := c.sessionRecoveredHandler(); onSessionRecovered != nil {
		if err := onSessionRecovered(recoveryInfo); err != nil {
			return fmt.Errorf("commit recovered session: %w", err)
		}
	}
	c.mu.Lock()
	c.sessionPath = info.Path
	c.guardianPath = guardian.PathFor(info.Path)
	c.mu.Unlock()
	// Recovery branch is a new lineage path. Load an inherited projection
	// sidecar when present so the model view stays compressed across the fork.
	c.bindExecutorProjection(info.Path, true)
	c.setActiveJobSession(info.Path)
	c.rebindCheckpoints(info.Path)
	c.transplantInFlightTurnMarker(originalPath, info.Path)
	commit.publish()
	return nil
}

func (c *Controller) adoptDiskSession(path string) bool {
	loaded, err := agent.LoadSession(path)
	if err != nil || loaded == nil {
		return false
	}
	c.executor.SetSession(loaded)
	c.bindExecutorProjection(path, true)
	c.ResetPlannerSession()
	c.rebindCheckpoints(path)
	c.setActiveJobSession(path)
	return true
}

func (c *Controller) messageCount() int {
	if c.executor == nil {
		return 0
	}
	return c.executor.Session().Len()
}

func (c *Controller) markInFlightTurn(startMessageIndex int, preserveUser bool) agent.InFlightTurnMeta {
	path := c.SessionPath()
	if path == "" {
		return agent.InFlightTurnMeta{}
	}
	marker, err := agent.BeginSessionInFlightTurn(path, startMessageIndex, preserveUser)
	if err != nil {
		slog.Warn("controller: mark in-flight turn", "err", err)
		return agent.InFlightTurnMeta{}
	}
	return marker
}

func (c *Controller) clearInFlightTurn(marker agent.InFlightTurnMeta) {
	path := c.SessionPath()
	if path == "" || marker.ID == "" {
		return
	}
	if _, err := agent.ClearSessionInFlightTurnIfMatch(path, marker); err != nil {
		slog.Warn("controller: clear in-flight turn", "err", err)
	}
}

// finishInFlightTurn persists the completed transcript before removing the
// crash marker. A crash can therefore leave either a recoverable marker or a
// durable completed transcript, never an unmarked in-memory-only suffix.
func (c *Controller) finishInFlightTurn(startMessages int, marker agent.InFlightTurnMeta) {
	commitPrepared := marker.ID == ""
	if marker.ID != "" && c.executor != nil {
		digest, digestErr := c.executor.Session().ContentDigest()
		if digestErr != nil {
			slog.Warn("controller: compute completed turn digest", "err", digestErr)
		} else if prepared, matched, prepareErr := agent.PrepareSessionInFlightTurnCommit(c.SessionPath(), marker, digest); prepareErr != nil {
			slog.Warn("controller: prepare in-flight turn commit", "err", prepareErr)
		} else if matched {
			marker = prepared
			commitPrepared = true
		}
	}
	durable, err := c.snapshotActivityIfChanged(startMessages)
	if err != nil && !durable {
		// Keep the marker when the transcript did not become durable. Resume can
		// then retry recovery instead of treating an in-memory-only tail as done.
		slog.Warn("controller: keeping in-flight marker after failed turn snapshot", "err", err)
		return
	}
	if err != nil {
		slog.Warn("controller: turn transcript saved before metadata update failed", "err", err)
	}
	if !commitPrepared {
		// Do not clear an unprepared marker: a crash between the snapshot and this
		// point would otherwise leave recovery without exact commit evidence.
		slog.Warn("controller: keeping in-flight marker without commit digest", "marker_id", marker.ID)
		return
	}
	c.clearInFlightTurn(marker)
}

// transplantInFlightTurnMarker moves a pending in-flight-turn marker from the
// session path a recovery fork abandoned onto the branch the turn continues
// on. Left behind, the stale marker would fire recoverInterruptedTurn on the
// next open of the original branch and strip messages from a turn that in
// fact kept running on the recovery branch; missing from the recovery branch,
// a crash before turn end would leave its partial tail unmarked.
func (c *Controller) transplantInFlightTurnMarker(fromPath, toPath string) {
	if strings.TrimSpace(fromPath) == "" || strings.TrimSpace(toPath) == "" || fromPath == toPath {
		return
	}
	meta, ok, err := agent.LoadBranchMeta(fromPath)
	if err != nil || !ok || meta.InFlightTurn == nil {
		if err != nil {
			slog.Warn("controller: load in-flight turn marker for transplant", "path", fromPath, "err", err)
		}
		return
	}
	marker := meta.InFlightTurn
	if err := agent.SetSessionInFlightTurn(toPath, *marker); err != nil {
		// Keep the original marker: a turn boundary on the wrong branch beats
		// no boundary anywhere if the runtime dies before the turn completes.
		slog.Warn("controller: transplant in-flight turn marker", "path", toPath, "err", err)
		return
	}
	if _, err := agent.ClearSessionInFlightTurnIfMatch(fromPath, *marker); err != nil {
		slog.Warn("controller: clear in-flight turn marker on forked-from branch", "path", fromPath, "err", err)
	}
}

func (c *Controller) recoverInterruptedTurn(path string) {
	if c.executor == nil || path == "" {
		return
	}
	meta, ok, err := agent.LoadBranchMeta(path)
	if err != nil || !ok || meta.InFlightTurn == nil {
		if err != nil {
			slog.Warn("controller: load in-flight turn marker", "err", err)
		}
		return
	}
	marker := meta.InFlightTurn
	if interruptedTurnContinuedOnRecoveryBranch(path, marker) {
		// The "interrupted" turn did not die with a runtime: a recovery branch
		// forked off this session after the marker was set, so the turn kept
		// running (and completing) there. Runtimes predating the marker
		// transplant in recoverSnapshotConflict left the marker behind on the
		// forked-from branch; stripping now would truncate a transcript the
		// completed turn already superseded. Clear the stale marker instead.
		if _, err := agent.ClearSessionInFlightTurnIfMatch(path, *marker); err != nil {
			slog.Warn("controller: clear fork-orphaned in-flight turn", "err", err)
		}
		return
	}
	msgs := c.executor.Session().Snapshot()
	if marker.CommitDigest != "" {
		if digest, digestErr := c.executor.Session().ContentDigest(); digestErr != nil {
			slog.Warn("controller: digest resumed in-flight turn", "err", digestErr)
		} else if digest == marker.CommitDigest {
			// The exact transcript named before the final snapshot is present. The
			// process died after commit and before CAS cleanup; preserve everything.
			if _, err := agent.ClearSessionInFlightTurnIfMatch(path, *marker); err != nil {
				slog.Warn("controller: clear committed in-flight turn marker", "err", err)
			}
			return
		}
	}
	start, found := resolveInterruptedTurnStart(msgs, marker.StartMessageIndex, marker.PreserveUser, marker.StartedAt, provider.Message{})
	if found && interruptedTurnCrossesLaterTurn(msgs, start) {
		slog.Warn("controller: preserving WAL transcript after stale in-flight marker",
			"path", path, "messages", len(msgs), "marker_index", marker.StartMessageIndex, "resolved_index", start,
			"marker_revision", marker.StartRevision, "current_revision", meta.Revision)
		c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn,
			Text: "Session recovery found completed turns after a stale interruption marker; the full WAL history was preserved."})
		if _, err := agent.ClearSessionInFlightTurnIfMatch(path, *marker); err != nil {
			slog.Warn("controller: clear stale multi-turn in-flight marker", "err", err)
		}
		return
	}
	changed := found && len(msgs) > start
	if changed {
		if marker.PreserveUser {
			c.stripCancelledVisibleTurnMessagesAfterWithFallbackAt(start, provider.Message{}, marker.StartedAt, c.ledgerTailEvidence())
		} else {
			c.stripTurnMessagesAfter(start)
		}
		if err := c.snapshot(false, true, false); err != nil {
			slog.Warn("controller: post-interrupted-turn snapshot", "err", err)
		}
	}
	if _, err := agent.ClearSessionInFlightTurnIfMatch(path, *marker); err != nil {
		slog.Warn("controller: clear stale in-flight turn", "err", err)
	}
}

// interruptedTurnCrossesLaterTurn detects the data-loss shape where an old
// marker survived while one or more later turns were durably appended. A
// compaction summary and mid-turn steer are not new foreground turn boundaries.
func interruptedTurnCrossesLaterTurn(msgs []provider.Message, start int) bool {
	if start < 0 || start >= len(msgs) {
		return false
	}
	turns := 0
	for _, msg := range msgs[start:] {
		if !agent.IsUserAuthoredTurnMessage(msg) {
			continue
		}
		turns++
		if turns > 1 {
			return true
		}
	}
	return false
}

// interruptedTurnContinuedOnRecoveryBranch reports whether a recovery branch
// forked off path after its in-flight-turn marker was set. Markers only exist
// while a turn runs and recovery forks happen on saves, so a child recovery
// branch younger than the marker means the marked turn itself moved there —
// the marker is a leftover from a runtime that switched paths mid-turn, not a
// crashed turn whose partial tail needs stripping. A marker without a start
// time is treated as continued whenever any recovery child exists: erring
// toward keeping messages is the data-safe direction.
func interruptedTurnContinuedOnRecoveryBranch(path string, marker *agent.InFlightTurnMeta) bool {
	if marker == nil {
		return false
	}
	branches, err := agent.ListBranches(filepath.Dir(path))
	if err != nil {
		return false
	}
	id := agent.BranchID(path)
	for _, b := range branches {
		if b.Recovered && b.ParentID == id && b.CreatedAt.After(marker.StartedAt) {
			return true
		}
	}
	return false
}

// stripTurnMessagesAfter truncates the executor's session to keep only messages
// before the given index, discarding an incomplete synthetic turn (the synthetic
// user prompt plus every assistant/tool message that followed).
func (c *Controller) stripTurnMessagesAfter(idx int) {
	if c.executor == nil {
		return
	}
	msgs := c.executor.Session().Snapshot()
	if len(msgs) <= idx {
		return
	}
	c.replaceSessionAfterCancel(msgs[:idx])
}

// stripInterruptedSyntheticTurnMessagesAfter relocates a synthetic turn after
// an in-turn compaction has rewritten the pre-turn message index, then drops
// that whole controller-created turn.
func (c *Controller) stripInterruptedSyntheticTurnMessagesAfter(idx int) {
	if c.executor == nil {
		return
	}
	msgs := c.executor.Session().Snapshot()
	startedAt := c.inFlightTurnStartedAt()
	if start, ok := resolveInterruptedTurnStart(msgs, idx, false, startedAt, provider.Message{}); ok {
		idx = start
	}
	c.stripTurnMessagesAfter(idx)
}

// stripCancelledVisibleTurnMessagesAfterWithFallback preserves the real user
// prompt and fully paired tool rounds from a cancelled visible turn. Unsafe
// assistant/tool fragments are retained as provider-excluded display history.
// It also covers coordinator
// cancellation before the executor has appended the visible user message. The
// orchestrator owns that input, so it supplies the exact message rather than
// letting cancellation infer the current turn from older transcript history.
func (c *Controller) stripCancelledVisibleTurnMessagesAfterWithFallback(idx int, fallback provider.Message) {
	c.stripCancelledVisibleTurnMessagesAfterWithFallbackAt(idx, fallback, c.inFlightTurnStartedAt(), nil)
}

func (c *Controller) stripCancelledVisibleTurnMessagesAfterWithFallbackAt(idx int, fallback provider.Message, startedAt time.Time, evidence *interruptedTailEvidence) {
	if c.executor == nil {
		return
	}
	msgs := c.executor.Session().Snapshot()
	if start, ok := resolveInterruptedTurnStart(msgs, idx, true, startedAt, fallback); ok {
		idx = start
	}
	if idx < 0 {
		idx = 0
	}
	if idx > len(msgs) {
		idx = len(msgs)
	}
	next := append([]provider.Message{}, msgs[:idx]...)
	keptUser := false
	userEnd := idx
	for i, m := range msgs[idx:] {
		if !agent.IsUserAuthoredTurnMessage(m) {
			continue
		}
		m.Content = StripComposePrefixes(m.Content)
		next = append(next, m)
		keptUser = true
		userEnd = idx + i + 1
		break
	}
	if !keptUser && agent.IsUserAuthoredTurnMessage(fallback) {
		fallback.Content = StripComposePrefixes(fallback.Content)
		if strings.TrimSpace(fallback.Content) != "" {
			fallback.Images = append([]string(nil), fallback.Images...)
			next = append(next, fallback)
			keptUser = true
			userEnd = idx
		}
	}
	if !keptUser && len(msgs) <= idx {
		return
	}
	recovery := &provider.InterruptedTurnRecovery{Pending: true}
	localIndexes := make([]int, 0, 1)
	for i := userEnd; i < len(msgs); {
		m := msgs[i]
		if m.LocalOnly {
			m.Role = provider.RoleTool
			m.ToolCallID = provider.LocalOnlyToolID
			m.Name = provider.LocalOnlyToolName
			previousRecovery := m.InterruptedTurn
			m.InterruptedTurn = nil
			next = append(next, m)
			localIndexes = append(localIndexes, len(next)-1)
			recovery.DroppedPartialText = recovery.DroppedPartialText || strings.TrimSpace(m.Content) != ""
			recovery.DroppedPartialReasoning = recovery.DroppedPartialReasoning || strings.TrimSpace(m.ReasoningContent) != ""
			if previousRecovery != nil {
				mergeInterruptedRecovery(recovery, previousRecovery)
			} else {
				for _, call := range m.ToolCalls {
					provider.RecordToolRecovery(recovery, interruptedToolSummary(call), provider.ToolRunUnknown)
				}
			}
			i++
			continue
		}
		// Auto-compaction can install a digest between the pinned current user
		// message and its recent tool tail. It summarizes pre-turn/current work
		// that is no longer present verbatim, so keep it provider-visible rather
		// than silently dropping context during recovery.
		if agent.IsCompactionSummary(m) {
			next = append(next, m)
			i++
			continue
		}
		if m.Role == provider.RoleAssistant {
			recordInterruptedAssistantRecovery(recovery, msgs, i, evidence)
		}
		if end, ok := completeToolTurnEnd(msgs, i); ok && c.executor.CanReplayAssistantMessage(m) {
			next = append(next, msgs[i:end]...)
			i = end
			continue
		}
		if local, ok := localizeInterruptedMessage(m, recovery); ok {
			next = append(next, local)
			localIndexes = append(localIndexes, len(next)-1)
		}
		i++
	}
	if len(localIndexes) == 0 {
		next = append(next, provider.Message{
			Role: provider.RoleTool, ToolCallID: provider.LocalOnlyToolID,
			Name: provider.LocalOnlyToolName, LocalOnly: true,
		})
		localIndexes = append(localIndexes, len(next)-1)
	}
	applyInterruptedTailEvidence(recovery, evidence)
	c.stampInterruptedTurnID(recovery)
	next[localIndexes[len(localIndexes)-1]].InterruptedTurn = recovery
	c.replaceSessionAfterCancel(next)
}

func (c *Controller) inFlightTurnStartedAt() time.Time {
	path := c.SessionPath()
	if path == "" {
		return time.Time{}
	}
	meta, ok, err := agent.LoadBranchMeta(path)
	if err != nil || !ok || meta.InFlightTurn == nil {
		return time.Time{}
	}
	return meta.InFlightTurn.StartedAt
}

// resolveInterruptedTurnStart turns the pre-run array index into a stable
// boundary after compaction. New user messages carry a creation timestamp set
// after the marker, and graceful cleanup also has the exact composed prompt as
// a fallback. We only fall back to the legacy index when it still points at a
// plausible turn-start user message, keeping recovery data-safe for older
// sidecars without timestamps.
func resolveInterruptedTurnStart(msgs []provider.Message, idx int, preserveUser bool, startedAt time.Time, fallback provider.Message) (int, bool) {
	fallbackContent := ""
	if agent.IsUserAuthoredTurnMessage(fallback) {
		fallbackContent = StripComposePrefixes(fallback.Content)
	}
	matchesKind := func(m provider.Message) bool {
		if m.Role != provider.RoleUser || agent.IsPinnedContextRevision(m) {
			return false
		}
		if preserveUser {
			if !agent.IsUserAuthoredTurnMessage(m) {
				return false
			}
			if fallbackContent != "" && StripComposePrefixes(m.Content) != fallbackContent {
				return false
			}
		}
		return true
	}
	startedMillis := startedAt.UnixMilli()
	if !startedAt.IsZero() {
		for i, m := range msgs {
			if matchesKind(m) && m.CreatedAt >= startedMillis {
				return i, true
			}
		}
	}
	// Tests/headless runners may not persist an in-flight sidecar. The exact
	// graceful fallback still distinguishes the current visible turn; search
	// backward so a repeated prompt selects the newest occurrence.
	if fallbackContent != "" {
		for i, msg := range slices.Backward(msgs) {
			if matchesKind(msg) {
				return i, true
			}
		}
	}
	if idx >= 0 && idx < len(msgs) && matchesKind(msgs[idx]) {
		return idx, true
	}
	return 0, false
}

func (c *Controller) hasInterruptedDisplayAfter(idx int, fallback provider.Message) bool {
	if c.executor == nil {
		return false
	}
	msgs := c.executor.Session().Snapshot()
	if start, ok := resolveInterruptedTurnStart(msgs, idx, true, c.inFlightTurnStartedAt(), fallback); ok {
		idx = start
	}
	idx = max(0, min(idx, len(msgs)))
	for _, m := range msgs[idx:] {
		if m.LocalOnly && m.InterruptedTurn != nil {
			return true
		}
	}
	return false
}

func completeToolTurnEnd(msgs []provider.Message, i int) (int, bool) {
	if i < 0 || i >= len(msgs) {
		return i, false
	}
	m := msgs[i]
	if m.LocalOnly || m.Role != provider.RoleAssistant || len(m.ToolCalls) == 0 {
		return i, false
	}
	end := i + 1
	for end < len(msgs) && msgs[end].Role == provider.RoleTool && !msgs[end].LocalOnly {
		end++
	}
	results := msgs[i+1 : end]
	if len(results) != len(m.ToolCalls) {
		return i, false
	}
	for k, call := range m.ToolCalls {
		if strings.TrimSpace(call.Name) == "" || (call.Arguments != "" && !json.Valid([]byte(call.Arguments))) {
			return i, false
		}
		if results[k].ToolCallID != call.ID || results[k].Name != call.Name {
			return i, false
		}
	}
	return end, true
}

func interruptedToolSummary(call provider.ToolCall) provider.InterruptedToolSummary {
	summary := provider.InterruptedToolSummary{
		ID: call.ID, Name: strings.TrimSpace(call.Name), Added: call.Added, Removed: call.Removed,
	}
	addFile := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || path == "/dev/null" || len(summary.Files) >= 8 {
			return
		}
		if slices.Contains(summary.Files, path) {
			return
		}
		summary.Files = append(summary.Files, path)
	}
	var args map[string]any
	if json.Unmarshal([]byte(call.Arguments), &args) == nil {
		for _, key := range []string{"path", "file", "file_path", "filename"} {
			if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
				addFile(value)
			}
		}
	}
	for line := range strings.SplitSeq(call.Diff, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			addFile(strings.TrimPrefix(line, "+++ b/"))
		case strings.HasPrefix(line, "--- a/"):
			addFile(strings.TrimPrefix(line, "--- a/"))
		case strings.HasPrefix(line, "*** Update File: "):
			addFile(strings.TrimPrefix(line, "*** Update File: "))
		case strings.HasPrefix(line, "*** Add File: "):
			addFile(strings.TrimPrefix(line, "*** Add File: "))
		case strings.HasPrefix(line, "*** Delete File: "):
			addFile(strings.TrimPrefix(line, "*** Delete File: "))
		}
	}
	return summary
}

func (c *Controller) replaceSessionAfterCancel(msgs []provider.Message) {
	// The whole cleanup is a save/recovery handoff like snapshot's: hold
	// snapshotMu from the in-memory truncation onward. Truncating outside the
	// lock would let an in-flight save capture the shortened transcript, read
	// the longer partial autosave on disk as a stale-prefix conflict, and
	// adopt it back into the executor — silently undoing the cancel cleanup
	// before the flush below could persist it.
	c.snapshotMu.Lock()
	defer c.snapshotMu.Unlock()
	c.executor.Session().Replace(append([]provider.Message(nil), msgs...))
	// Rebuild canonical todo state from the truncated transcript so
	// Controller.Todos(), goal readiness, and the task panel no longer see
	// the in_progress items written by the cancelled turn.
	c.executor.RebuildTodoState()
	// The mid-turn autosave may have already written a partial transcript to
	// disk. snapshotActivityIfChanged skips the write when messageCount()
	// returns to startMessages, so flush the cleaned transcript here. SaveRewrite
	// still checks that this controller owns the current on-disk baseline before
	// overwriting it, and also covers the edge case where the strip leaves only a
	// system message (HasContent() == false). The path is read under the lock so
	// an in-flight recovery retarget cannot leave it stale.
	c.mu.Lock()
	path := c.sessionPath
	c.mu.Unlock()
	if path != "" {
		if err := c.executor.Session().SaveRewrite(path); err != nil {
			if errors.Is(err, agent.ErrSessionSnapshotConflict) {
				if _, outcome, recoverErr := c.recoverSnapshotConflict(path, err, true); recoverErr != nil {
					slog.Warn("controller: post-cancel transcript recovery", "err", recoverErr)
				} else if outcome == conflictDropped {
					slog.Warn("controller: post-cancel transcript dropped after conflict", "path", path)
				}
			} else {
				slog.Warn("controller: post-cancel transcript flush", "err", err)
			}
		}
	}
}

func (c *Controller) snapshotActivityIfChanged(startMessages int) (bool, error) {
	if c.messageCount() <= startMessages {
		return true, nil
	}
	return c.snapshotWithDurability(true, false, false)
}

// SetSessionPath rebinds auto-save without changing the current session
// preference. Callers creating a genuinely fresh conversation should use
// SetFreshSessionPath; callers resuming history should use Resume.
func (c *Controller) SetSessionPath(p string) {
	c.setSessionPath(p, false)
}

// SetFreshSessionPath binds a path that is known to belong to a newly-created
// session and samples the configured new-session recovery default.
func (c *Controller) SetFreshSessionPath(p string) {
	c.setSessionPath(p, true)
}

func (c *Controller) setSessionPath(p string, fresh bool) {
	// See snapshotMu: the swap must not interleave with an in-flight save.
	c.snapshotMu.Lock()
	c.mu.Lock()
	c.sessionPath = p
	c.guardianPath = guardian.PathFor(p)
	c.mu.Unlock()
	// Fresh paths clear projection; rebinds keep/load the target sidecar.
	c.bindExecutorProjection(p, !fresh)
	c.setActiveJobSession(p)
	c.rebindCheckpoints(p)
	if fresh {
		c.resetRecoveryForNewSession(p)
		// A newly-created conversation must not share the previous logical
		// session's temporary files (e.g. after EnsureSessionPath on a
		// controller that already ran commands).
		c.rotateSessionTemp()
	} else {
		c.loadRecoveryState(p)
	}
	c.snapshotMu.Unlock()
	c.rebindInbox()
	if !fresh {
		c.recoverCheckpointTransactions()
	}
}

func (c *Controller) setActiveJobSession(sessionPath string) {
	if c.jobs != nil {
		c.jobs.SetActiveSessionPath(agent.BranchID(sessionPath), sessionPath)
	}
}

// SessionDir reports the directory new session files land in ("" disables
// persistence), so the caller can decide whether to mint a path.
func (c *Controller) SessionDir() string { return c.sessionDir }

// SessionPath reports the file the current conversation auto-saves to ("" when
// persistence is disabled), so a history view can mark the active session.
func (c *Controller) SessionPath() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionPath
}

func (c *Controller) parentSessionID() string {
	return agent.BranchID(c.SessionPath())
}

// History returns the executor's current message log (for repopulating a
// resumed frontend's view).
func (c *Controller) History() []provider.Message {
	if c.executor == nil {
		return nil
	}
	return c.executor.Session().Snapshot() // copy — a turn may be appending concurrently
}

// SessionHasUnsavedChanges tells desktop history whether it may safely refresh
// an idle view from the durable WAL. A failed or contended save can leave the
// controller with a newer in-memory transcript; replacing that view from disk
// would hide the user's latest turn until the next retry.
func (c *Controller) SessionHasUnsavedChanges() bool {
	if c == nil || c.executor == nil {
		return false
	}
	return c.executor.Session().HasUnsavedChanges(c.SessionPath())
}

// HistoryLen returns the number of messages in the live log.
func (c *Controller) HistoryLen() int {
	if c.executor == nil {
		return 0
	}
	return c.executor.Session().Len()
}

// HistoryWindow returns a copy of the messages in [start, end) of the live
// log. Paging frontends use it to convert a display window without copying
// the whole history.
func (c *Controller) HistoryWindow(start, end int) []provider.Message {
	if c.executor == nil {
		return []provider.Message{}
	}
	return c.executor.Session().MessageRange(start, end)
}

// SessionPersistedState exposes the session's persistence baseline for the
// controller's current session path, so a paging frontend can validate a
// display-index sidecar against the live session.
func (c *Controller) SessionPersistedState() (agent.PersistedState, bool) {
	if c.executor == nil {
		return agent.PersistedState{}, false
	}
	return c.executor.Session().PersistedState(c.SessionPath())
}

// ContextSnapshot returns (usedTokens, contextWindow) for the gauge. usedTokens
// is what the next request will send, measured the way the compaction trigger
// measures it, so the gauge and the trigger can never disagree. Both zero means
// no data yet — a gauge hides itself.
func (c *Controller) ContextSnapshot() (int, int) {
	if c.executor == nil {
		return 0, 0
	}
	return c.executor.ContextUsedTokens(), c.executor.ContextWindow()
}

// CompactRatio returns the auto-compaction threshold as a fraction of the window
// (0 when the executor is unset). The status line shows headroom against it.
func (c *Controller) CompactRatio() float64 {
	if c.executor == nil {
		return 0
	}
	return c.executor.CompactRatio()
}

// LastUsage returns the most recent turn's token telemetry (nil before the first
// turn), so frontends can derive the prompt cache-hit rate for the status line.
func (c *Controller) LastUsage() *provider.Usage {
	if c.executor == nil {
		return nil
	}
	return c.executor.LastUsage()
}

// SessionCache returns cumulative cache hit/miss prompt tokens for the session,
// so a frontend can render the aggregate (session-wide) cache-hit rate — steadier
// than the single-turn rate and unaffected by compaction.
func (c *Controller) SessionCache() (hit, miss int) {
	if c.executor == nil {
		return 0, 0
	}
	return c.executor.SessionCache()
}

// Todos returns a copy of the canonical task list (the latest todo_write state
// merged with complete_step advances) so frontends can render a live task panel.
func (c *Controller) Todos() []evidence.TodoItem {
	if c.executor == nil {
		return nil
	}
	return c.executor.CanonicalTodoState()
}

// Balance queries the active provider's wallet balance, or (nil, nil) when the
// provider declares no balance_url — so a caller treats "not configured" and
// "fetched" the same and just omits the readout when nil.
func (c *Controller) Balance(ctx context.Context) (*billing.Balance, error) {
	if strings.TrimSpace(c.balanceURL) == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	return billing.FetchWithClient(ctx, c.balanceClient, c.balanceURL, c.balanceKey)
}

// Host returns the running MCP host (nil when no plugins), for frontends that
// list servers / resolve MCP prompts.
func (c *Controller) Host() *plugin.Host { return c.mcp.hostRef() }

// Commands returns the loaded custom slash commands.
func (c *Controller) Commands() []command.Command {
	if p := c.commands.Load(); p != nil {
		return *p
	}
	return nil
}

// ReloadCommands rescans all command directories and hot-swaps the slash_command
// tool and the internal command slice — no MCP restart, no hook rerun.
func (c *Controller) ReloadCommands(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	cmds, loadErr := command.LoadRoots(config.CommandRootsForRoot(c.workspaceRoot)...)
	var cmdSkills []skill.Skill
	if !c.disableImplicitSkillInvocation {
		cmdSkills = c.SlashSkills()
	}

	entries := make([]command.SlashEntry, 0, len(cmdSkills)+len(cmds))
	for _, sk := range cmdSkills {

		entries = append(entries, command.SlashEntry{
			Name:        sk.SlashName(),
			Description: sk.Description,
			Render:      func(args []string) string { return c.skills.render(sk, strings.Join(args, " ")) },
		})
	}
	for _, cmd := range cmds {
		if cmd.Hidden {
			continue
		}

		entries = append(entries, command.SlashEntry{
			Name:        cmd.Name,
			Description: cmd.Description,
			ArgHint:     cmd.ArgHint,
			Render:      func(args []string) string { return cmd.Render(args) },
		})
	}
	c.mcp.registerTool(command.NewSlashCommandTool(entries))
	cmdSlice := cmds
	c.commands.Store(&cmdSlice)
	return loadErr
}

// Skills returns the discoverable skills (for the slash menu and `/skills`).
// When a live Store is available, scan it on demand so skills installed during
// this session appear without rewriting the cache-stable system prompt.
// Executor returns the underlying agent when present (nil for pure runners).
func (c *Controller) Executor() *agent.Agent {
	if c == nil {
		return nil
	}
	return c.executor
}

func (c *Controller) Skills() []skill.Skill {
	return c.skills.list()
}

// ImplicitSkillInvocationEnabled reports whether skills are exposed to the
// model for automatic discovery and invocation. Explicit /skill handling is
// independent of this model-facing capability.
func (c *Controller) ImplicitSkillInvocationEnabled() bool {
	return c != nil && !c.disableImplicitSkillInvocation
}

// SlashSkills returns the user-visible skill directory. Plugin skills use
// package-qualified names while Skills keeps bare model/run_skill identifiers.
func (c *Controller) SlashSkills() []skill.Skill {
	return c.skills.slashList()
}

// AllSkills returns every discoverable skill, including disabled ones, for
// management surfaces that need to re-enable a hidden skill.
func (c *Controller) AllSkills() []skill.Skill {
	return c.skills.listAll()
}

// DisabledSkills returns all discoverable skills that are disabled in config.
func (c *Controller) DisabledSkills() []skill.Skill {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	var out []skill.Skill
	for _, sk := range c.AllSkills() {
		if cfg.IsSkillDisabled(sk.Name) {
			out = append(out, sk)
		}
	}
	return out
}

// SkillEnabled reports whether a discoverable skill is enabled.
func (c *Controller) SkillEnabled(name string) bool {
	cfg, err := config.Load()
	if err != nil {
		return true
	}
	return !cfg.IsSkillDisabled(name)
}

// SetSkillEnabled persists a skill enable/disable preference. The caller should
// rebuild the controller for the prompt/tool registry to reflect it immediately.
func (c *Controller) SetSkillEnabled(name string, enabled bool) error {
	found := false
	for _, sk := range c.AllSkills() {
		if config.SkillNameKey(sk.Name) == config.SkillNameKey(name) {
			name = sk.Name
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("unknown skill: %s", name)
	}
	// Serialize the load-modify-save against other in-process user-config
	// editors so concurrent writers (bot mapping persistence, desktop
	// settings) don't drop this toggle or lose their own fields.
	unlock := config.LockUserConfigEdits()
	defer unlock()
	cfg := config.LoadForEdit(config.UserConfigPath())
	if err := cfg.SetSkillEnabled(name, enabled); err != nil {
		return err
	}
	return cfg.SaveTo(config.UserConfigPath())
}

// CreateSkill writes a new skill file at the given scope and returns its
// path. Skills()/AllSkills()/RunSkill() read the live store on demand, so the
// new skill is usable (by name) immediately with no rebuild and appears in the
// next real user turn's live session-context catalog. Rebuilds remain necessary
// when the tool registry or enabled-skill configuration changes.
func (c *Controller) CreateSkill(name string, scope skill.Scope, content string) (string, error) {
	w := c.skills.writer()
	if w == nil {
		return "", fmt.Errorf("no writable skill store in this session")
	}
	return w.CreateWithContent(name, scope, content)
}

// UpdateSkill overwrites an existing user-authored skill file in place. See
// skill.Store.UpdateContent for the builtin-refusal and scope-match rules.
func (c *Controller) UpdateSkill(name string, scope skill.Scope, content string) error {
	w := c.skills.writer()
	if w == nil {
		return fmt.Errorf("no writable skill store in this session")
	}
	return w.UpdateContent(name, scope, content)
}

// DeleteSkill removes a user-authored skill file at the given scope. See
// skill.Store.Delete for the builtin-refusal and scope-match rules.
func (c *Controller) DeleteSkill(name string, scope skill.Scope) error {
	w := c.skills.writer()
	if w == nil {
		return fmt.Errorf("no writable skill store in this session")
	}
	return w.Delete(name, scope)
}

// HookRunner returns the session's hook runner (nil-safe; may hold zero hooks),
// so a frontend can list the active hooks via `/hooks`.
func (c *Controller) HookRunner() *hook.Runner { return c.hooks }

// AddMCPServer connects an MCP server live and persists it to the user-global
// config. Its tools are registered immediately and become available on the next
// turn (the agent reads the registry per turn). The raw entry — ${VARS} intact —
// is what's written to disk; the live connection uses the expanded form. Returns
// the number of tools the server exposed. Persistence is transactional: a config
// or activation failure removes the just-connected client so the live registry
// never claims an install that will disappear after restart.
func (c *Controller) AddMCPServer(e config.PluginEntry) (int, error) {
	// AddMCPServer is an explicit user action. Mark the live entry with the same
	// provenance it will receive when the saved user config is loaded next time,
	// so /mcp add is add-and-use in the current session too.
	e.Source = config.MCPSourceUserConfig
	if effective, loadErr := config.LoadForRootReadOnly(c.workspaceRoot); loadErr != nil {
		return 0, loadErr
	} else {
		for _, configured := range effective.Plugins {
			if configured.Name != e.Name {
				continue
			}
			if configured.Source != config.MCPSourceUserConfig && configured.Source != config.MCPSourceLegacyUser {
				return 0, fmt.Errorf("MCP server %q is already configured by %s; edit or remove that declaration before installing a global server with the same name", e.Name, configured.Source)
			}
			break
		}
	}
	n, err := c.connectMCPServer(e)
	if err != nil {
		return 0, err
	}
	if _, err := config.InstallUserPluginForRoot(c.workspaceRoot, e, true); err != nil {
		c.DisconnectMCPServer(e.Name)
		return 0, fmt.Errorf("saving MCP server config: %w", err)
	}
	return n, nil
}

// ConnectMCPServer connects an MCP server entry for this session without writing
// it to config. Desktop owns config placement so it can keep user-level settings
// out of project reasonix.toml while preserving the CLI AddMCPServer semantics.
func (c *Controller) ConnectMCPServer(e config.PluginEntry) (int, error) {
	return c.connectMCPServer(e)
}

// RegisterMCPServerOnDemand restores a configured server's cached provider
// surface without forcing a handshake. It is the durable-enable counterpart to
// ConnectMCPServer, which remains the explicit install/retry operation.
func (c *Controller) RegisterMCPServerOnDemand(e config.PluginEntry) (int, error) {
	spec := c.mcpSpec(e)
	n, err := c.mcp.registerSpecOnDemand(spec)
	if err == nil && c.capabilityRuntime != nil {
		c.capabilityRuntime.UpsertServer(e, spec, true)
	}
	return n, err
}

// connectMCPServer expands an entry's ${VARS}, applies the known-server
// overrides scoped to the workspace, and connects it live via the mcp manager.
func (c *Controller) connectMCPServer(e config.PluginEntry) (int, error) {
	spec := c.mcpSpec(e)
	n, err := c.mcp.connectSpec(spec)
	if err == nil && c.capabilityRuntime != nil {
		c.capabilityRuntime.UpsertServer(e, spec, true)
	}
	return n, err
}

func (c *Controller) mcpSpec(e config.PluginEntry) plugin.Spec {
	exp := e.ExpandedPlugin()
	configSource := strings.TrimSpace(string(exp.Source))
	spec := plugin.ApplyKnownOverrides(plugin.Spec{
		Name:               exp.Name,
		Type:               exp.Type,
		Command:            exp.Command,
		Args:               exp.Args,
		Env:                exp.Env,
		URL:                exp.URL,
		Headers:            exp.Headers,
		StartupTimeout:     controllerMCPTimeout(exp.StartupTimeoutSeconds),
		DefaultCallTimeout: c.mcpDefaultCallTimeout,
		CallTimeout:        controllerMCPTimeout(exp.CallTimeoutSeconds),
		ToolTimeouts:       controllerMCPToolTimeouts(exp.ToolTimeoutSeconds),
		WorkspaceRoot:      c.WorkspaceRoot(),
		ConfigSource:       configSource,
		Authorized:         exp.Source.UserAuthorized(),
		// Explicit user installs and reconnects run as trusted host processes.
		ProcessMode: plugin.MCPProcessHost,
	}, c.WorkspaceRoot())
	if exp.Source.ProjectScoped() && strings.TrimSpace(spec.Dir) == "" {
		spec.Dir = c.WorkspaceRoot()
	}
	if c.mcpConfigureSpec != nil {
		c.mcpConfigureSpec(&spec)
		if spec.ProcessMode == "" {
			spec.ProcessMode = plugin.MCPProcessHost
		}
	}
	return spec
}

// syncCapabilityRuntimeFromConfig restores one server's authoritative runtime
// entry after a transactional disconnect/rollback. enabledOverride is used for
// a session-only disconnect; nil re-resolves the durable activation state.
func (c *Controller) syncCapabilityRuntimeFromConfig(name string, enabledOverride *bool) {
	if c == nil || c.capabilityRuntime == nil {
		return
	}
	name = strings.TrimSpace(name)
	cfg, err := config.LoadForRoot(c.workspaceRoot)
	if err != nil {
		// The caller revokes first. A config read failure must not re-enable a
		// potentially stale spec or shared-Host client.
		return
	}
	for _, entry := range cfg.Plugins {
		if strings.TrimSpace(entry.Name) != name {
			continue
		}
		enabled := entry.ShouldAutoStart()
		if enabledOverride != nil {
			enabled = *enabledOverride
		} else if resolved, resolveErr := config.DefaultMCPActivationStore().IsEnabled(entry, c.workspaceRoot); resolveErr == nil {
			enabled = resolved
		}
		c.capabilityRuntime.UpsertServer(entry, c.mcpSpec(entry), enabled)
		return
	}
	c.capabilityRuntime.RemoveServer(name)
}

func controllerMCPTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func controllerMCPToolTimeouts(values map[string]int) map[string]time.Duration {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]time.Duration, len(values))
	for name, seconds := range values {
		if name = strings.TrimSpace(name); name != "" && seconds > 0 {
			out[name] = time.Duration(seconds) * time.Second
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ImportMCPEntries persists selected MCP entries and attempts to connect them
// live. A connection failure does not roll back the config import: the user can
// fix local dependencies and reconnect in a later session.
func (c *Controller) ImportMCPEntries(entries []config.PluginEntry) (total, added, updated, connected, failed, skipped int, err error) {
	total, added, updated, err = config.ImportCCSwitchMCPEntries(entries)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, err
	}
	effectiveCfg, loadErr := config.LoadForRoot(c.workspaceRoot)
	if loadErr != nil {
		return 0, 0, 0, 0, 0, 0, loadErr
	}
	effective := make(map[string]config.PluginEntry, len(effectiveCfg.Plugins))
	for _, entry := range effectiveCfg.Plugins {
		effective[entry.Name] = entry
	}
	for _, imported := range entries {
		e, ok := effective[imported.Name]
		if !ok || e.Source != config.MCPSourceUserConfig {
			// A project declaration with the same name remains effective. The
			// imported global entry is saved as its lower-priority fallback.
			skipped++
			continue
		}
		if c.mcp.hasServer(e.Name) {
			if c.capabilityRuntime != nil {
				// Import updates may intentionally keep an existing live client, but
				// future proxy reconnects must use the newly persisted spec.
				c.capabilityRuntime.UpsertServer(e, c.mcpSpec(e), true)
			}
			skipped++
			continue
		}
		if _, err := c.AddMCPServer(e); err != nil {
			failed++
			continue
		}
		connected++
	}
	return total, added, updated, connected, failed, skipped, nil
}

func (c *Controller) ConfiguredMCPNames() []string {
	cfg, err := config.LoadForRootReadOnly(c.workspaceRoot)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(cfg.Plugins))
	for _, p := range cfg.Plugins {
		names = append(names, p.Name)
	}
	return names
}

func (c *Controller) DisconnectedMCPNames() []string {
	cfg, err := config.LoadForRootReadOnly(c.workspaceRoot)
	if err != nil {
		return nil
	}
	connected := map[string]bool{}
	for _, name := range c.mcp.serverNames() {
		connected[name] = true
	}
	var names []string
	for _, p := range cfg.Plugins {
		if !connected[p.Name] {
			names = append(names, p.Name)
		}
	}
	return names
}

func (c *Controller) ConnectConfiguredMCPServer(name string) (int, error) {
	p, err := c.configuredMCPServer(name)
	if err != nil {
		return 0, err
	}
	return c.connectMCPServer(p)
}

func (c *Controller) configuredMCPServer(name string) (config.PluginEntry, error) {
	cfg, err := config.LoadForRoot(c.workspaceRoot)
	if err != nil {
		return config.PluginEntry{}, err
	}
	for _, p := range cfg.Plugins {
		if p.Name == name {
			return p, nil
		}
	}
	return config.PluginEntry{}, fmt.Errorf("no configured MCP server named %q", name)
}

// RemoveMCPServer removes writable config before disconnecting the live server.
// A persistence failure must not produce a false-successful session-only removal.
// MCPs contributed by installed plugin packages cannot be removed independently.
func (c *Controller) RemoveMCPServer(name string) (disconnected bool, err error) {
	cfg, lerr := config.LoadForRoot(c.workspaceRoot)
	if lerr != nil {
		return false, lerr
	}
	if owner, ok := cfg.PluginPackageOwner(name); ok {
		return false, fmt.Errorf("MCP server %q is managed by plugin %q; disable or remove the plugin instead", name, owner)
	}
	entry, removed, _, rerr := config.RemovePluginFromEffectiveSourceForRoot(c.workspaceRoot, name)
	if rerr != nil {
		return false, rerr
	}
	if !removed {
		return false, fmt.Errorf("no removable MCP server named %q", name)
	}
	_ = config.DefaultMCPActivationStore().ClearServer(entry, c.workspaceRoot)
	removedState := reconcileRemovedMCPState(c.workspaceRoot, name)
	if c.capabilityRuntime != nil {
		// Revoke before touching the shared Host so an overlapping resolver cannot
		// reuse a sibling tab's still-connected client.
		c.capabilityRuntime.RemoveServer(name)
	}
	disconnected = c.mcp.disconnect(name)
	if !disconnected {
		c.mcp.removeToolPrefix(name)
	}
	// A lower-priority same-name declaration may now be effective. Restore its
	// cached/on-demand surface without starting a process; otherwise ensure the
	// removed name stays absent.
	if removedState.fallbackFound {
		enabled := removedState.fallback.ShouldAutoStart()
		if resolved, resolveErr := config.DefaultMCPActivationStore().IsEnabled(removedState.fallback, c.workspaceRoot); resolveErr == nil {
			enabled = resolved
		}
		if enabled {
			_, _ = c.RegisterMCPServerOnDemand(removedState.fallback)
		} else {
			c.syncCapabilityRuntimeFromConfig(name, &enabled)
		}
	} else {
		c.syncCapabilityRuntimeFromConfig(name, nil)
	}
	return disconnected, removedState.cleanupErr
}

// DisconnectMCPServer disconnects a live server for this session without touching
// config — the connector toggle's "off". Its tools vanish next turn; it reconnects
// on the next session start, or now via ConnectConfiguredMCPServer (the "on").
// Reports whether a live server was actually disconnected.
func (c *Controller) DisconnectMCPServer(name string) bool {
	if c.capabilityRuntime != nil {
		c.capabilityRuntime.SetServerEnabled(name, false)
	}
	disconnected := c.mcp.disconnect(name)
	removedPlaceholder := 0
	if !disconnected {
		removedPlaceholder = c.mcp.removeToolPrefix(name)
	}
	// Keep configured servers discoverable as disabled, but forget runtime-only
	// or rolled-back installs that no longer exist in configuration.
	disabled := false
	c.syncCapabilityRuntimeFromConfig(name, &disabled)
	return disconnected || removedPlaceholder > 0
}

// UnregisterMCPServerTools hides a shared MCP server from this controller only.
// The desktop shared-host path uses this for per-tab connector toggles: the
// shared client stays alive for sibling tabs, while this session's registry drops
// the server's provider-visible tools before the next turn.
func (c *Controller) UnregisterMCPServerTools(name string) bool {
	if c.capabilityRuntime != nil {
		c.capabilityRuntime.SetServerEnabled(name, false)
	}
	return c.mcp.suspendToolPrefix(name)
}

// Label returns the human-readable model label, e.g. "deepseek-flash".
func (c *Controller) Label() string { return c.label }

// ModelRef returns the canonical provider/model reference for the session.
func (c *Controller) ModelRef() string { return c.modelRef }

// WorkspaceRoot returns the workspace root for this controller's session
// (the directory that file-writers and @-references are scoped to).
// Empty means no scoping is in effect.
func (c *Controller) WorkspaceRoot() string { return c.workspaceRoot }

func (c *Controller) imageInputEnabled() bool {
	if c.frozenImageInput != nil {
		return *c.frozenImageInput
	}
	ref := c.modelRef
	cfg, err := config.LoadForRoot(c.workspaceRoot)
	if err == nil && ref == "" {
		ref = cfg.DefaultModel
	}
	if err != nil || ref == "" {
		return false
	}
	entry, ok := cfg.ResolveModel(ref)
	if !ok {
		return false
	}
	if c.modelCapabilityResolver != nil {
		return c.modelCapabilityResolver(entry).State == config.CapabilitySupported
	}
	return config.EffectiveVision(entry)
}

// ImageInputEnabled reports whether the current model accepts direct image
// inputs, so frontends can gate image-only UX before a turn starts.
func (c *Controller) ImageInputEnabled() bool { return c.imageInputEnabled() }

// ImageInputSnapshot avoids configuration reads on the Desktop metadata path.
// Legacy/custom controllers without a frozen boot snapshot use the existing
// background metadata fallback instead.
func (c *Controller) ImageInputSnapshot() (enabled, fallback, available bool) {
	if c == nil || c.frozenImageInput == nil {
		return false, false, false
	}
	return *c.frozenImageInput, c.visionModel != "", true
}

// ImageCapabilityChanged lets desktop refresh an idle runtime before admission.
func (c *Controller) ImageCapabilityChanged() bool {
	return c.imageCapabilityChanged != nil && c.imageCapabilityChanged()
}

// InheritLifecycleFrom carries same-session lifecycle state across controller
// rebuilds, such as model switches that preserve the conversation.
func (c *Controller) InheritLifecycleFrom(prev *Controller) {
	if prev == nil {
		return
	}
	prev.mu.Lock()
	started := prev.startedOnce
	turn := prev.turn
	prev.mu.Unlock()

	c.mu.Lock()
	c.startedOnce = started
	if c.turn < turn {
		c.turn = turn
	}
	c.mu.Unlock()
}

// SessionAuthorizations snapshots this controller's same-session tool
// grants ("Allow for this session") and Plan-mode read-only command trust,
// for carrying into a replacement controller across a rebuild — see
// RestoreSessionAuthorizations.
func (c *Controller) SessionAuthorizations() SessionAuthorizations {
	auth := c.approval.snapshotSessionAuthorizations()
	if c.writeAccess.roots != nil {
		auth.WriteRoots = c.writeAccess.roots.SessionRoots()
		if auth.WriteRoots == nil {
			auth.WriteRoots = []string{}
		}
	}
	return auth
}

// RestoreSessionAuthorizations re-applies session authorizations captured
// from a prior controller in the same session (see SessionAuthorizations). A
// model/effort/profile switch rebuilds the controller, and without this the
// replacement forgets every grant the user already made this session.
func (c *Controller) RestoreSessionAuthorizations(auth SessionAuthorizations) {
	c.approval.restoreSessionAuthorizations(auth)
	if c.writeAccess.roots != nil && len(auth.WriteRoots) > 0 {
		c.writeAccess.roots.GrantVerifiedSession(auth.WriteRoots)
	}
}

// ReleaseResources stops plugin subprocesses and releases resources without
// firing SessionEnd. Use it only when replacing the controller for the same
// logical session.
func (c *Controller) ReleaseResources() {
	c.close(false, closeJobsWithGrace)
}

// Close stops plugin subprocesses and releases resources. A session that ever
// started fires SessionEnd so a teardown hook runs.
func (c *Controller) Close() {
	c.close(true, closeJobsWithGrace)
}

// CloseAfterDestroy releases controller resources after the caller has already
// begun session-specific job teardown. It avoids a second synchronous job grace
// wait while still cancelling the manager root and reaping temporary artifacts
// once every job goroutine finally exits.
func (c *Controller) CloseAfterDestroy() {
	c.close(true, closeJobsAsync)
}

type closeJobsMode int

const (
	closeJobsWithGrace closeJobsMode = iota
	closeJobsAsync
)

func (c *Controller) close(fireSessionEnd bool, jobsMode closeJobsMode) {
	// Desktop tab lifecycles can race a rebind/model-switch/close on the same
	// controller; make teardown idempotent so a duplicate Close cannot re-fire
	// SessionEnd hooks or re-run cleanup. The first caller's jobsMode wins.
	c.closeOnce.Do(func() {
		c.mu.Lock()
		started := c.startedOnce
		cancel := c.cancel
		// Seal turn admission and drop anything already parked: a parked turn
		// must not start against a controller that is being torn down, and
		// without the closed flag a submit landing after this critical
		// section (while a running turn's TurnDone delivery is still in
		// flight) would park again and start after teardown.
		c.closed = true
		c.parkedTurns = nil
		// A finishing-only controller no longer needs the delivery gate because
		// closed seals every admission path. Keep running truthful until the
		// foreground goroutine actually exits; clearing it here would report idle
		// while tools and prompt waiters were still live.
		c.finishing = false
		c.finishingBoundary.end()
		if cancel != nil {
			c.canceling = true
		}
		c.mu.Unlock()
		if cancel != nil {
			// clearAll deliberately does not signal waiters. Pair it with the
			// foreground cancellation so approval/ask waits always unblock.
			c.promptResolveMu.Lock()
			c.promptOwner.CancelAll()
			c.approval.clearAll()
			c.promptResolveMu.Unlock()
			cancel()
		} else {
			c.promptOwner.Clear()
		}
		if fireSessionEnd && started {
			c.hooks.SessionEnd(context.Background(), "other")
			c.extensionSessionEvent(extension.PointSessionEnd, dispatch.PhaseEnd, c.SessionPath())
		}
		if c.jobs != nil {
			switch jobsMode {
			case closeJobsAsync:
				c.jobs.CloseAsync()
			default:
				c.jobs.Close() // cancel any still-running background jobs
			}
		}
		if ledger := c.turnEventLedger(); ledger != nil {
			if err := ledger.Close(); err != nil {
				slog.Warn("controller: close turn event ledger", "err", err)
			}
		}
		if c.cleanup != nil {
			c.cleanup()
		}
		// Drop the Controller owner reference last so background job leases
		// that outlive close still pin retired generations until they exit.
		if c.sessionTemp != nil {
			c.sessionTemp.Release()
		}
	})
}

// SessionTemp returns the logical-session private temporary directory manager.
// Hot rebuilds pass this to the replacement Controller so the directory survives
// model/settings swaps. Nil only when the Controller was constructed without one
// (should not happen after New).
func (c *Controller) SessionTemp() *sessiontemp.Manager {
	if c == nil {
		return nil
	}
	return c.sessionTemp
}

// rotateSessionTemp advances the private temporary generation so a new logical
// session cannot see the previous session's temporary files. In-flight command
// leases keep the old generation alive until they release.
func (c *Controller) rotateSessionTemp() {
	if c == nil || c.sessionTemp == nil {
		return
	}
	c.sessionTemp.Rotate()
}

// Jobs returns the still-running background jobs for the status bar (nil when
// background jobs are disabled).
func (c *Controller) Jobs() []jobs.View {
	if c.jobs == nil {
		return nil
	}
	return c.jobs.RunningForSession(c.parentSessionID())
}

// KillJob cancels a running background job by ID.
func (c *Controller) KillJob(id string) bool {
	if c.jobs == nil {
		return false
	}
	return c.jobs.Kill(id)
}

// CancelJob stops one background job owned by this controller's session.
func (c *Controller) CancelJob(id string) bool {
	if c.jobs == nil {
		return false
	}
	return c.jobs.KillForSession(c.parentSessionID(), id)
}

// WorkspaceLeaseState reports the process-local held and waiting lease scope.
// Canonical keys are used only for matching another local controller and are
// never copied into user-facing payloads.
func (c *Controller) WorkspaceLeaseState() workspacelease.State {
	return c.workspaceLease.State()
}

// WorkspaceLeaseHeldKeys is the lock-domain identity this controller currently
// holds. Empty when no write lease is held.
func (c *Controller) WorkspaceLeaseHeldKeys() []string {
	return c.workspaceLease.HeldKeys()
}

// SetToolApprovalMode changes the runtime approval posture for permission-gated
// tools. It does not answer business asks or plan approval. Sub-agents (task,
// writer-capable skill sub-agents, the planner) have no UI to prompt through,
// so this also pushes the mode to the shared headless gate they read from —
// without it, a mode switch (Shift+Tab) would only rebuild the parent
// executor's gate and leave sub-agents pinned to whatever mode was active
// when the session booted.
func (c *Controller) SetToolApprovalMode(mode string) {
	c.ApplyToolApprovalMode(mode)
}

// ApplyToolApprovalMode is SetToolApprovalMode reporting which pending
// approval prompt ids the new posture auto-allowed. Plan, sandbox escape,
// and config writes never drain; Auto keeps explicit memory asks but drains
// fallback ones; YOLO drains both. Frontends must keep the rest (#6432).
func (c *Controller) ApplyToolApprovalMode(mode string) []string {
	mode = normalizeToolApprovalMode(mode)
	// Capture mode-change recovery dismissals before approval drain so a
	// same-value hydrate/reconcile never rotates Episode state, while a real
	// Auto↔Yolo/Ask switch clears temporary failure/reviewer locks and waiters
	// without auto-approving the original mutation.
	var recoveryDismissed []string
	c.mu.Lock()
	gate := c.recoveryGate
	c.mu.Unlock()
	if gate != nil {
		if ctrl, ok := any(gate).(agent.RecoveryEpisodeControl); ok {
			// Do not hold controller/approval locks while rotating the gate.
			recoveryDismissed = ctrl.OnModeChange(mode)
		}
	}
	pending := c.approval.setMode(mode)
	if c.subagentGate != nil {
		c.subagentGate.Update(mode)
	}
	c.refreshInteractiveGate()
	// Clear recovery cards dismissed by the mode switch outside the gate lock.
	for _, id := range recoveryDismissed {
		p := c.approval.resolve(id)
		if p.reply != nil {
			// Do not approve the pending mutation; signal cancel/deny so legacy
			// paths drop the card.
			select {
			case p.reply <- approvalReply{allow: false}:
			default:
			}
		}
	}
	drained := make([]string, 0, len(pending))
	for _, p := range pending {
		p.reply <- approvalReply{allow: true}
		drained = append(drained, p.id)
	}
	return drained
}

func (c *Controller) ToolApprovalMode() string {
	return c.approval.mode()
}

// SetAutoApproveTools turns YOLO tool auto-approval on or off for the session:
// while on, every tool approval request is auto-allowed (writers and bash run
// without asking). Ask requests and plan approval still reach the user. Deny
// rules still block. Runtime-only — never written to config.
func (c *Controller) SetAutoApproveTools(on bool) {
	if on {
		c.SetToolApprovalMode(ToolApprovalYolo)
		return
	}
	c.SetToolApprovalMode(ToolApprovalAsk)
}

// SetBypass is the legacy name for SetAutoApproveTools. Keep it for existing
// desktop/serve bindings and CLI code that still uses the bypass wording.
func (c *Controller) SetBypass(on bool) {
	c.SetAutoApproveTools(on)
}

// SetMode applies the Plan workflow flag and tool auto-approval together so a turn
// submitted right after a composer mode switch can't observe a half-applied
// gate. Turning tool auto-approval on drains any pending tool approval.
func (c *Controller) SetMode(plan, autoApproveTools bool) {
	c.ApplyMode(plan, autoApproveTools)
}

// ApplyMode is SetMode reporting which pending approval prompt ids the tool
// approval switch auto-allowed (see ApplyToolApprovalMode).
func (c *Controller) ApplyMode(plan, autoApproveTools bool) []string {
	c.applyPlanMode(plan)
	if autoApproveTools {
		return c.ApplyToolApprovalMode(ToolApprovalYolo)
	}
	return c.ApplyToolApprovalMode(ToolApprovalAsk)
}

// ApplyComposerProfile publishes the collaboration, approval, and Goal axes as
// one controller operation. The only fallible mutation (durable Goal state)
// commits first, so a persistence failure leaves Plan and approval unchanged.
// Serve serializes this call with turn admission and controller replacement.
func (c *Controller) ApplyComposerProfile(plan bool, toolApprovalMode, goal string) ([]string, error) {
	toolApprovalMode = strings.ToLower(strings.TrimSpace(toolApprovalMode))
	switch toolApprovalMode {
	case ToolApprovalAsk, ToolApprovalAuto, ToolApprovalYolo:
	default:
		return nil, fmt.Errorf("tool approval mode must be ask, auto, or yolo")
	}
	goal = strings.TrimSpace(goal)
	if strings.TrimSpace(c.Goal()) != goal {
		if err := c.SetGoalDurable(goal); err != nil {
			return nil, fmt.Errorf("persist goal state: %w", err)
		}
	}
	if goal != "" {
		plan = false
	}
	c.applyPlanMode(plan)
	return c.ApplyToolApprovalMode(toolApprovalMode), nil
}

// AutoApproveTools reports whether YOLO tool auto-approval is on,
// for status indicators and mode persistence.
func (c *Controller) AutoApproveTools() bool {
	return c.ToolApprovalMode() == ToolApprovalYolo
}

// Bypass is the legacy name for AutoApproveTools.
func (c *Controller) Bypass() bool {
	return c.AutoApproveTools()
}

// memory
//
// The memory snapshot, pending standing-doc notes, and write serialization
// live in c.memory (a memoryManager) behind its own locks, off c.mu — so a
// memory-panel save never stalls an approval or status poll. These methods are
// the SessionAPI surface; each is a thin delegation. See memory.go.

// QuickAdd appends a one-line note to the doc-memory file for scope (project
// REASONIX.md by default) — the write side of "#<note>". Returns the file written.
func (c *Controller) QuickAdd(scope memory.Scope, note string) (string, error) {
	return c.memory.quickAdd(scope, note)
}

// SaveDoc overwrites a recognized memory doc with body — the save side of the
// desktop panel's in-place editor. Returns the file written.
func (c *Controller) SaveDoc(path, body string) (string, error) {
	return c.memory.saveDoc(path, body)
}

// SaveMemory writes an active auto-memory fact and refreshes the in-session
// snapshot. It is the explicit user-confirmed counterpart to the model-owned
// remember tool, used by management surfaces that preview a candidate first.
func (c *Controller) SaveMemory(m memory.Memory) (string, error) {
	return c.memory.saveMemory(m)
}

// ForgetMemory removes a saved auto-memory by name — the panel/TUI forget action,
// the manual counterpart to the model's `forget` tool.
func (c *Controller) ForgetMemory(name string) error {
	return c.memory.forget(name)
}

// QueueMemory implements memory.Queue: model remember/forget tool results are
// already visible in the current loop, so this refreshes the background snapshot
// that will be published in session-context on the next real user turn.
func (c *Controller) QueueMemory(note string) {
	c.memory.queue(note)
}

// ClaimAutoMemoryWrite consumes the one-shot create-only authorization issued
// by gateApprover for a low-risk project fact.
func (c *Controller) ClaimAutoMemoryWrite(args json.RawMessage) bool {
	return c.memory.claimAutoRemember(args)
}

func (c *Controller) MemoryRevisions(ref string) []memory.Memory {
	return c.memory.revisions(ref)
}

// RestoreMemory restores an older active-memory revision as a new audited
// revision and applies it to the next user turn.
func (c *Controller) RestoreMemory(ref string, revision int) (memory.Memory, error) {
	return c.memory.restore(ref, revision)
}

// RestoreArchivedMemory recovers an archived fact as a new audited revision and
// applies it to the next user turn.
func (c *Controller) RestoreArchivedMemory(archivePath string) (memory.Memory, error) {
	return c.memory.restoreArchived(archivePath)
}

// Memory returns the loaded memory snapshot (nil when memory is disabled), for
// frontends that surface a memory panel or the /memory command. The returned
// *Set is immutable — mutations go through QuickAdd / SaveDoc.
func (c *Controller) Memory() *memory.Set {
	return c.memory.current()
}

// approval bridge (agent gate → events)

// gateApprover adapts the Controller to permission.Approver. It is distinct
// from the public Approve command (different signature, different direction).
type gateApprover struct{ c *Controller }

const dynamicBashApprovalReason = "This command uses nested or indirect shell execution. Auto and broad allow rules cannot verify the inner command; approve this exact command or use YOLO."

func (g gateApprover) Approve(ctx context.Context, tool, subject string, args json.RawMessage) (bool, bool, error) {
	allow, remember, _, err := g.ApproveWithReason(ctx, tool, subject, args)
	return allow, remember, err
}

func (g gateApprover) ApproveWithReason(ctx context.Context, tool, subject string, args json.RawMessage) (bool, bool, string, error) {
	return g.approveWithPolicyReason(ctx, tool, subject, args, "")
}

func (g gateApprover) ApproveWithPolicyReason(ctx context.Context, tool, subject string, args json.RawMessage, policyReason string) (bool, bool, string, error) {
	return g.approveWithPolicyReason(ctx, tool, subject, args, policyReason)
}

func combineApprovalReasons(reasons ...string) string {
	var kept []string
	for _, reason := range reasons {
		if reason = strings.TrimSpace(reason); reason != "" {
			kept = append(kept, reason)
		}
	}
	return strings.Join(kept, "\n")
}

func (g gateApprover) approveWithPolicyReason(ctx context.Context, tool, subject string, args json.RawMessage, policyReason string) (bool, bool, string, error) {
	if tool == memoryRememberTool && g.c.allowLowRiskRemember(args) {
		return true, false, "", nil
	}
	subject = approvalDisplaySubject(tool, subject, args)
	requireHuman := strings.EqualFold(tool, "bash") && permission.BashSubjectRequiresExplicitApproval(subject)
	// Check pre-approval first, before any prompt or Guardian review. Dynamic
	// Bash accepts only YOLO or an exact session grant here; ordinary calls also
	// accept the just-approved-plan window. Deny rules already bit at the policy
	// level before this point.
	if requireHuman && g.c.approval.preApprovedForRequiredHuman(tool, subject) {
		return true, false, "", nil
	}
	if !requireHuman && g.c.approval.preApproved(tool, subject, args) {
		return true, false, "", nil
	}
	if g.c.guardianSess != nil && !requireHuman {
		allow, reason, reviewErr := g.c.guardianSess.Review(ctx, tool, args, g.c.executor.Session())
		if reviewErr != nil {
			return false, false, "", reviewErr
		}
		if allow && !requiresFreshApprovalTool(tool) {
			return true, false, "", nil
		}
		reason = combineApprovalReasons(policyReason, reason)
		humanAllow, remember, err := g.c.requestApprovalWithReason(ctx, tool, subject, args, reason)
		if err != nil {
			return false, false, reason, err
		}
		if !humanAllow {
			return false, false, reason, nil
		}
		return true, remember, "", nil
	}
	if requireHuman {
		reason := combineApprovalReasons(policyReason, dynamicBashApprovalReason)
		allow, remember, err := g.c.requestApprovalWithReasonOptions(ctx, tool, subject, args, reason, approvalDecisionOptions{requireHuman: true})
		return allow, remember, "", err
	}
	allow, remember, err := g.c.requestApprovalWithReason(ctx, tool, subject, args, policyReason)
	return allow, remember, "", err
}

type planModeReadOnlyTrustApprover struct{ c *Controller }

type sandboxEscapeApprover struct{ c *Controller }

func (s sandboxEscapeApprover) ApproveSandboxEscape(ctx context.Context, req sandbox.EscapeRequest) (bool, string, error) {
	subject := sandboxEscapeApprovalSubject(req.Command)
	reason := sandboxEscapeApprovalReason(req.Reason)
	reply, err := s.c.requestFreshApprovalDecision(ctx, SandboxEscapeApprovalTool, subject, req.Args, reason)
	if err != nil {
		return false, "approval aborted", err
	}
	if !reply.allow {
		return false, i18n.M.SandboxEscapeDeclined, nil
	}
	if reply.session {
		s.c.approval.grantSession(SandboxEscapeApprovalTool, subject)
	}
	return true, "", nil
}

func (s sandboxEscapeApprover) SandboxEscapeSessionAllowed(_ context.Context, req sandbox.EscapeRequest) bool {
	return s.c.approval.preApprovedForDecision(SandboxEscapeApprovalTool, sandboxEscapeApprovalSubject(req.Command), nil, true)
}

func sandboxEscapeApprovalSubject(command string) string {
	subject := strings.TrimSpace(command)
	if subject == "" {
		return i18n.M.SandboxEscapeSubjectFallback
	}
	return i18n.M.SandboxEscapeSubjectPrefix + subject
}

func sandboxEscapeApprovalReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return i18n.M.SandboxEscapeRuntimeReason
	}
	return reason
}

// managedConfigWriteApprover routes a file tool's Reasonix-managed config write
// through the fresh-human approval prompt (see ManagedConfigWriteApprovalTool).
// A session grant is tool-wide (mirroring sandbox_escape): one "allow for this
// session" covers the rest of the repair flow across the handful of managed
// config files without re-prompting on every incremental edit.
type managedConfigWriteApprover struct{ c *Controller }

func (m managedConfigWriteApprover) ApproveManagedConfigWrite(ctx context.Context, req tool.ConfigWriteRequest) (bool, string, error) {
	subject := managedConfigWriteApprovalSubject(req.Path)
	args, _ := json.Marshal(map[string]string{"path": req.Path})
	reply, err := m.c.requestFreshApprovalDecision(ctx, ManagedConfigWriteApprovalTool, subject, args, i18n.M.ConfigWriteReason)
	if err != nil {
		return false, "approval aborted", err
	}
	if !reply.allow {
		return false, i18n.M.ConfigWriteDeclined, nil
	}
	if reply.session {
		m.c.approval.grantSession(ManagedConfigWriteApprovalTool, subject)
	}
	return true, "", nil
}

func (m managedConfigWriteApprover) ManagedConfigWriteSessionAllowed(_ context.Context, req tool.ConfigWriteRequest) bool {
	return m.c.approval.preApprovedForDecision(ManagedConfigWriteApprovalTool, managedConfigWriteApprovalSubject(req.Path), nil, true)
}

func managedConfigWriteApprovalSubject(path string) string {
	return i18n.M.ConfigWriteSubjectPrefix + strings.TrimSpace(path)
}

func (p planModeReadOnlyTrustApprover) CheckPlanModeReadOnlyTrust(ctx context.Context, req agent.PlanModeReadOnlyTrustRequest) (bool, string, error) {
	prefix := normalizePlanModeReadOnlyCommandPrefix(req.Prefix)
	if prefix == "" {
		return false, "missing plan-mode read-only command prefix", nil
	}
	return p.checkBashReadOnlyCommandTrust(ctx, req, prefix)
}

func (p planModeReadOnlyTrustApprover) checkBashReadOnlyCommandTrust(ctx context.Context, req agent.PlanModeReadOnlyTrustRequest, prefix string) (bool, string, error) {
	if p.c.approval.planModeReadOnlyCommandTrusted(prefix) {
		return true, "", nil
	}
	command := strings.TrimSpace(req.Command)
	if command == "" {
		command = strings.TrimSpace(string(req.Args))
	}
	subject := fmt.Sprintf(i18n.M.PlanModeBashTrustSubjectFmt, prefix, command)
	reason := i18n.M.PlanModeBashTrustReason
	reply, err := p.c.requestFreshApprovalDecision(ctx, agent.PlanModeReadOnlyCommandApprovalTool, subject, req.Args, reason)
	if err != nil {
		return false, "approval aborted", err
	}
	if !reply.allow {
		return false, i18n.M.PlanModeBashTrustDeclined, nil
	}
	if reply.session {
		p.c.approval.grantPlanModeReadOnlyCommand(prefix)
	}
	if reply.persist && p.c.onRememberPlanModeReadOnlyCommand != nil {
		p.c.emitPlanModeReadOnlyCommandTrustResult(p.c.onRememberPlanModeReadOnlyCommand(prefix))
		p.c.approval.grantPlanModeReadOnlyCommand(prefix)
	}
	return true, "", nil
}

func approvalDisplaySubject(tool, subject string, args json.RawMessage) string {
	switch tool {
	case memoryRememberTool:
		return rememberApprovalSubject(subject, args)
	case memoryForgetTool:
		return forgetApprovalSubject(subject, args)
	case "move_file":
		return moveApprovalSubject(subject, args)
	default:
		return subject
	}
}

func moveApprovalSubject(fallback string, args json.RawMessage) string {
	if len(args) == 0 {
		return fallback
	}
	var in struct {
		SourcePath      string `json:"source_path"`
		DestinationPath string `json:"destination_path"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return fallback
	}
	if in.SourcePath == "" || in.DestinationPath == "" {
		return fallback
	}
	return in.SourcePath + " -> " + in.DestinationPath
}

func rememberApprovalSubject(fallback string, args json.RawMessage) string {
	if len(args) == 0 {
		return fallback
	}
	var in struct {
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Type        string `json:"type"`
		Body        string `json:"body"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return fallback
	}
	name := approvalCompactText(firstNonEmpty(in.Name, in.Title))
	desc := approvalTruncate(approvalCompactText(in.Description), 180)
	body := approvalTruncate(approvalCompactText(in.Body), 240)
	typ := string(memory.NormalizeType(in.Type))

	var b strings.Builder
	b.WriteString(i18n.M.MemoryApprovalSaveUpdate)
	baseLen := b.Len()
	if name != "" {
		fmt.Fprintf(&b, " %q", name)
	}
	if typ != "" {
		fmt.Fprintf(&b, " [%s]", typ)
	}
	if desc != "" {
		b.WriteString(": ")
		b.WriteString(desc)
	}
	if body != "" {
		if desc == "" {
			b.WriteString(": ")
		} else {
			b.WriteString(" | ")
		}
		b.WriteString(i18n.M.MemoryApprovalBodyLabel)
		b.WriteString(": ")
		b.WriteString(body)
	}
	if b.Len() == baseLen && fallback != "" {
		return fallback
	}
	return b.String()
}

func forgetApprovalSubject(fallback string, args json.RawMessage) string {
	if len(args) == 0 {
		return fallback
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return fallback
	}
	name := approvalCompactText(in.Name)
	if name == "" {
		return fallback
	}
	return fmt.Sprintf(i18n.M.MemoryApprovalArchiveFmt, name)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func approvalCompactText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func approvalTruncate(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

func (c *Controller) sessionMessageCount() int {
	if c.executor == nil {
		return 0
	}
	return c.executor.Session().Len()
}

// parseRewind parses the arguments after "/rewind". The user may provide:
//
//	/rewind              → latest checkpoint, both
//	/rewind <turn>       → that turn, both
//	/rewind <turn> <scope> → that turn, code|conversation|both
//
// If no turn is given, the latest checkpoint is used. If no scope is given, Both is assumed.
func parseRewind(args string, cps []checkpoint.Meta) (int, RewindScope, error) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		if len(cps) == 0 {
			return 0, RewindBoth, fmt.Errorf("no checkpoints available")
		}
		return cps[len(cps)-1].Turn, RewindBoth, nil
	}
	turn, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, RewindBoth, fmt.Errorf("invalid turn: %w", err)
	}
	scope := RewindBoth
	if len(fields) >= 2 {
		switch strings.ToLower(fields[1]) {
		case "code":
			scope = RewindCode
		case "conversation":
			scope = RewindConversation
		case "both":
			scope = RewindBoth
		default:
			return 0, RewindBoth, fmt.Errorf("unknown scope %q", fields[1])
		}
	}
	return turn, scope, nil
}

// requestApproval emits an ApprovalRequest and blocks until Approve(ID, …)
// answers or ctx is cancelled. A prior session grant (or a bypass posture) for
// the same approval scope short-circuits. The approvalManager's promptMu
// serialises outstanding prompts; this method keeps the I/O (events, hooks,
// remember) that the manager deliberately stays out of.
func (c *Controller) requestApproval(ctx context.Context, tool, subject string, args json.RawMessage) (bool, bool, error) {
	return c.requestApprovalWithReason(ctx, tool, subject, args, "")
}

func (c *Controller) requestApprovalWithReason(ctx context.Context, tool, subject string, args json.RawMessage, reason string) (bool, bool, error) {
	return c.requestApprovalWithReasonOptions(ctx, tool, subject, args, reason, approvalDecisionOptions{})
}

func (c *Controller) requestApprovalWithReasonOptions(ctx context.Context, tool, subject string, args json.RawMessage, reason string, opts approvalDecisionOptions) (bool, bool, error) {
	r, err := c.requestApprovalDecisionWithOptions(ctx, tool, subject, args, reason, opts)
	if err != nil {
		return false, false, err
	}
	// Plan approvals are one-shot — never persist a session grant for them, or
	// every future plan would auto-approve.
	if r.allow && r.session && !requiresFreshApprovalTool(tool) {
		c.approval.grantSession(tool, subject)
	}
	if r.allow && r.persist && !requiresFreshApprovalTool(tool) && c.onRemember != nil {
		c.emitRememberResult(c.onRemember(permission.RememberRuleForScope(tool, subject)))
	}
	return r.allow, false, nil
}

func (c *Controller) requestFreshApprovalDecision(ctx context.Context, tool, subject string, args json.RawMessage, reason string) (approvalReply, error) {
	return c.requestApprovalDecisionWithOptions(ctx, tool, subject, args, reason, approvalDecisionOptions{fresh: true})
}

type approvalDecisionOptions struct {
	// fresh marks a user trust/business decision rather than an ordinary tool
	// permission. It may reuse an explicit session grant, but YOLO/auto approval
	// must not answer or drain the prompt.
	fresh bool
	// requireHuman marks an ordinary tool approval that Auto, an approved-plan
	// window, Guardian, or an allowing hook must not answer. Unlike fresh it
	// retains the ordinary four-choice UI and YOLO remains an explicit bypass.
	requireHuman bool
}

func (c *Controller) requestApprovalDecisionWithOptions(ctx context.Context, tool, subject string, args json.RawMessage, reason string, opts approvalDecisionOptions) (approvalReply, error) {
	// YOLO/full access and the just-approved-plan execution window auto-allow
	// approval-gated tools without prompting. Plan approval is a user decision,
	// not a tool permission, so it deliberately stays interactive.
	if c.approval.preApprovedForDecisionOptions(tool, subject, args, opts.fresh, opts.requireHuman) {
		return approvalReply{allow: true}, nil
	}

	c.approval.promptMu.Lock()
	defer c.approval.promptMu.Unlock()

	// Re-check: a session grant may have landed while we queued behind another
	// prompt for the same subject.
	if c.approval.preApprovedForDecisionOptions(tool, subject, args, opts.fresh, opts.requireHuman) {
		return approvalReply{allow: true}, nil
	}

	// Claude's PermissionRequest contract answers the dialog on the plugin's
	// behalf (auto-allow/auto-deny) instead of merely observing it, so a
	// decision here must preempt the prompt rather than just notify — this
	// runs synchronously and before the dialog is shown. Native Reasonix
	// PermissionRequest hooks stay advisory-only (see claudePermissionBlocking).
	//
	// Hook auto-allow cannot replace a fresh-human decision. Interactive
	// YOLO may already have skipped remember/forget via preApproved. Deny
	// still applies universally — refusing is always safe to honor.
	if hookSubject, hookArgs, ok := permissionRequestHookPayload(tool, subject, args); ok {
		if decision, _ := c.hooks.PermissionRequest(ctx, tool, hookSubject, hookArgs); decision != nil {
			switch {
			case !*decision:
				return approvalReply{}, nil
			case !opts.fresh && !opts.requireHuman && !requiresFreshApprovalTool(tool):
				return approvalReply{allow: true}, nil
			}
			// An "allow" opinion on a fresh-human-required decision is
			// ignored; fall through to the normal interactive prompt.
		}
	}

	c.approval.promptEmitMu.Lock()
	var id string
	var reply chan approvalReply
	kind := ""
	if opts.fresh || opts.requireHuman || tool == planApprovalTool {
		if tool == planApprovalTool {
			kind = "plan"
		}
		id, reply = c.approval.registerDecisionKindWithInput(tool, subject, reason, args, opts.fresh, opts.requireHuman, kind, nil)
		ownerKind := PromptApproval
		if kind == "plan" {
			ownerKind = PromptPlan
		}
		c.registerOwnedPrompt(id, ownerKind)
	} else {
		id, reply = c.approval.registerWithInput(tool, subject, reason, args)
		c.registerOwnedPrompt(id, PromptApproval)
	}

	if err := event.EmitChecked(c.sink, c.approvalRequestEvent(event.Approval{ID: id, Tool: tool, Subject: subject, Reason: reason, RawInput: append(json.RawMessage(nil), args...), Fresh: opts.fresh, Kind: kind})); err != nil {
		c.approval.promptEmitMu.Unlock()
		c.cancelOwnedPrompt(id)
		return approvalReply{}, fmt.Errorf("persist approval request: %w", err)
	}
	c.approval.promptEmitMu.Unlock()
	// The agent now needs the user's attention; a Notification hook can ping an
	// external channel (desktop notice, phone) while the run blocks on the reply.
	go c.hooks.Notification(ctx, approvalNotificationText(tool, subject), "permission_prompt")

	waitCtx, cancelWait := c.approval.waitContext(ctx)
	defer cancelWait()

	select {
	case r := <-reply:
		return r, nil
	case <-waitCtx.Done():
		c.cancelOwnedPrompt(id)
		return approvalReply{}, waitCtx.Err()
	}
}

func (c *Controller) approvalRequestEvent(approval event.Approval) event.Event {
	if approval.TurnID == "" {
		approval.TurnID, _, _, _ = c.turnEventRuntimeStatus()
	}
	_, runtimeEpoch := c.promptIdentitySnapshot()
	if identity := c.bindOwnedPromptRouting(approval.ID, approval.TurnID, runtimeEpoch); identity.TurnID != "" {
		approval.TurnID = identity.TurnID
	}
	return event.Event{Kind: event.ApprovalRequest, TurnID: approval.TurnID, ItemID: approval.ID, Approval: approval}
}

func (c *Controller) emitRememberResult(r RememberResult) {
	if r.Err != nil {
		c.sink.Emit(event.Event{
			Kind:  event.Notice,
			Level: event.LevelWarn,
			Text:  fmt.Sprintf(i18n.M.PermissionSaveFailedFmt, r.Rule, r.Err),
		})
		return
	}
	switch {
	case r.Saved:
		c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: fmt.Sprintf(i18n.M.PermissionSavedFmt, r.Path, r.Rule)})
	case strings.TrimSpace(r.CoveredBy) != "":
		c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: fmt.Sprintf(i18n.M.PermissionAlreadyAllowedFmt, r.Path, r.CoveredBy)})
	}
}

func (c *Controller) emitPlanModeReadOnlyCommandTrustResult(r PlanModeReadOnlyCommandTrustResult) {
	prefix := strings.TrimSpace(r.Prefix)
	if r.Err != nil {
		c.sink.Emit(event.Event{
			Kind:  event.Notice,
			Level: event.LevelWarn,
			Text:  fmt.Sprintf(i18n.M.PlanModeReadOnlyCommandTrustFailedFmt, prefix, r.Err),
		})
		return
	}
	switch {
	case r.Saved:
		c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: fmt.Sprintf(i18n.M.PlanModeReadOnlyCommandTrustSavedFmt, r.Path, prefix)})
	case strings.TrimSpace(r.CoveredBy) != "":
		c.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: fmt.Sprintf(i18n.M.PlanModeReadOnlyCommandTrustAlreadyFmt, r.Path, r.CoveredBy)})
	}
}
