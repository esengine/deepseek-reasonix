package acp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/agentpreset"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/extension/uihub"
	"reasonix/internal/fileutil"
	fileencoding "reasonix/internal/fileutil/encoding"
	"reasonix/internal/jobs"
	"reasonix/internal/plugin"
	"reasonix/internal/provider"
	"reasonix/internal/sessioninbox"
	"reasonix/internal/store"
	"reasonix/internal/tool/builtin"
)

// SessionParams is everything a Factory needs to assemble one ACP session's
// controller. Sink is owned by this package (an updateSink bound to the session
// id) and must be wired into the controller's event sink; the controller's
// interactive approval (see control.Controller.EnableInteractiveApproval) then
// routes "ask" decisions back through that sink as ApprovalRequest events, which
// the sink forwards to the client over session/request_permission.
//
// Cwd roots the session's file tools and bash (built via builtin.Workspace).
// Model, EffortOverride, and RuntimeProfile are optional session-local selectors
// from ACP config options. MCPServers are the MCP servers the client asked the
// agent to connect for this session. The path hooks keep service bookkeeping
// aligned; factories must wire both into the controller they build.
type SessionParams struct {
	// MCPInteractions enables interactive MCP only after explicit client negotiation.
	MCPInteractions     bool
	Cwd                 string
	MCPServers          []plugin.Spec
	Sink                event.Sink
	Model               string
	EffortOverride      *string
	RuntimeProfile      string
	OnSessionRecovered  func(control.SessionRecoveryInfo) error
	OnSessionTransition func(control.SessionTransitionInfo) error
	// FileOverlay and Terminal are non-nil when the client advertised the
	// matching capability at initialize: file tools then see unsaved editor
	// buffers, and foreground bash can run in a client-owned terminal.
	// Factories thread them into the controller's tool assembly.
	FileOverlay builtin.FileOverlay
	Terminal    builtin.TerminalRunner
}

// Factory builds the per-session controller. The composition root (the cli's
// `reasonix acp` command) implements it by reusing setup()'s assembly: a
// Provider for Model, a tool Registry rooted at Cwd via builtin.Workspace, a
// per-session MCP host from MCPServers, the event Sink, all wired into a
// control.Controller. The returned controller owns its own cleanup (Close stops
// MCP subprocesses), so the service calls ctrl.Close() on teardown.
type Factory interface {
	NewSession(ctx context.Context, p SessionParams) (*control.Controller, error)
}

// SessionConfigStateParams asks the Factory for normalized session config
// selectors. Empty Model and RuntimeProfile use configured defaults. Nil
// EffortOverride means provider config wins; a non-nil empty string means
// provider default for this session.
type SessionConfigStateParams struct {
	Cwd            string
	Model          string
	EffortOverride *string
	RuntimeProfile string
}

// SessionConfigState is the complete ACP-visible config state for a session.
type SessionConfigState struct {
	Model          string
	EffortOverride *string
	RuntimeProfile string
	Models         *SessionModelState
	ConfigOptions  []SessionConfigOption
}

// SessionConfigStateProvider lets a Factory expose model, effort, and work-mode
// selectors without making the ACP transport depend on a concrete config backend.
type SessionConfigStateProvider interface {
	SessionConfigState(ctx context.Context, p SessionConfigStateParams) (SessionConfigState, error)
}

// SessionDirProvider lets a Factory expose the persistent session directory
// without forcing session/list to build a controller first.
type SessionDirProvider interface {
	SessionDir() string
}

// SessionRebuilder lets a Factory rebuild a session's controller via
// boot.Rebuild: the replacement is built with the same boot.Options NewSession
// would use, and the session state (history, approval grants, goal/recovery,
// lifecycle) migrates off old inside the boot layer. The caller keeps the
// swap/close ordering. Factories that do not implement it leave
// _reasonix.io/session/reloadExtensions reporting unavailable.
type SessionRebuilder interface {
	RebuildSession(ctx context.Context, p SessionParams, old *control.Controller) (*control.Controller, error)
}

// AgentInfo identifies this agent to clients in the initialize reply.
type AgentInfo struct {
	Name    string
	Version string
}

// Serve runs an ACP agent on r/w (stdin/stdout in production) until the input
// ends or ctx is cancelled. It owns the JSON-RPC connection and the session
// registry; the Factory supplies the kernel wiring. This is the single entry
// point the `reasonix acp` command calls.
//
// stdout is the JSON-RPC channel: callers must keep all other output (logs,
// diagnostics) off w and on stderr, or the wire corrupts.
func Serve(ctx context.Context, r io.Reader, w io.Writer, factory Factory, info AgentInfo) error {
	conn := NewConn(r, w)
	svc := &service{
		conn:     conn,
		factory:  factory,
		info:     info,
		sessions: make(map[string]*acpSession),
	}
	conn.Handle("initialize", svc.initialize)
	conn.Handle("authenticate", svc.authenticate)
	conn.Handle("session/new", svc.sessionNew)
	conn.Handle("session/load", svc.sessionLoad)
	conn.Handle("session/resume", svc.sessionResume)
	conn.Handle("session/prompt", svc.sessionPrompt)
	conn.Handle(sessionSteerMethod, svc.sessionSteer)
	conn.Handle(sessionInboxEnqueueMethod, svc.sessionInboxEnqueue)
	conn.Handle(sessionInboxListMethod, svc.sessionInboxList)
	conn.Handle(sessionInboxGetMethod, svc.sessionInboxGet)
	conn.Handle(sessionInboxUpdateMethod, svc.sessionInboxUpdate)
	conn.Handle(sessionInboxDeleteMethod, svc.sessionInboxDelete)
	conn.Handle(sessionInboxMoveMethod, svc.sessionInboxMove)
	conn.Handle(sessionInboxPauseMethod, svc.sessionInboxSetPaused)
	conn.Handle(sessionInboxRetryMethod, svc.sessionInboxRetry)
	conn.Handle(sessionInboxRefreshMethod, svc.sessionInboxRefresh)
	conn.Handle(sessionReloadExtensionsMethod, svc.sessionReloadExtensions)
	conn.Handle(sessionStatusMethod, svc.sessionStatus)
	conn.Handle("session/set_config_option", svc.sessionSetConfigOption)
	conn.Handle("session/set_model", svc.sessionSetModel)
	conn.Handle("session/set_mode", svc.sessionSetMode)
	conn.Handle("session/close", svc.sessionClose)
	conn.Handle("session/list", svc.sessionList)
	conn.Handle("session/delete", svc.sessionDelete)
	conn.HandleNotify("session/cancel", svc.sessionCancel)
	defer svc.closeAll()
	return conn.Serve(ctx)
}

// service holds the connection-wide ACP state: the factory, agent identity, and
// the live session registry.
type service struct {
	conn    *Conn
	factory Factory
	info    AgentInfo

	mu       sync.Mutex
	sessions map[string]*acpSession
	// clientCaps is what the client offered at initialize (fs proxy, host
	// terminals). Zero until initialize arrives; sessions opened later bind a
	// clientIO built from it.
	clientCaps ClientCapabilities
}

// afterResponse wraps a result with work that must run after the transport has
// successfully written that result. Session-opening notifications use this so a
// client can register the returned session before receiving its first update.
type afterResponse struct {
	result any
	after  func()
}

func (r afterResponse) Response() any { return r.result }

func (r afterResponse) AfterResponse() {
	if r.after != nil {
		r.after()
	}
}

func (s *service) setClientCapabilities(caps ClientCapabilities) {
	s.mu.Lock()
	s.clientCaps = caps
	s.mu.Unlock()
}

func (s *service) clientCapabilities() ClientCapabilities {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clientCaps
}

// extensionSurfaceSupported reports whether the connected client advertised
// reasonix.extensionSurface support in its initialize handshake.
func (s *service) extensionSurfaceSupported() bool {
	return clientExtensionSurfaceSupported(s.clientCapabilities())
}

// clientExtensionSurfaceSupported tolerantly parses the client's vendor
// capability block: _meta["reasonix.io"]["extensionSurface"]["supported"] must
// be an explicit true. Absent keys, wrong shapes, or a malformed block all
// mean unsupported — the sink then sends only the text fallback.
func clientExtensionSurfaceSupported(caps ClientCapabilities) bool {
	vendor, ok := caps.Meta["reasonix.io"].(map[string]any)
	if !ok {
		return false
	}
	capability, ok := vendor["extensionSurface"].(map[string]any)
	if !ok {
		return false
	}
	supported, _ := capability["supported"].(bool)
	return supported
}

// bindClientIO fills SessionParams' overlay/terminal fields from the client's
// declared capabilities. The nil checks keep absent capabilities as nil
// interface fields (a typed-nil *clientIO must never reach the interface).
func (s *service) bindClientIO(p *SessionParams, sessionID string) {
	p.MCPInteractions = clientMCPInteractionSupported(s.clientCapabilities())
	io := newClientIO(s.conn, sessionID, s.clientCapabilities())
	if !io.hasAny() {
		return
	}
	if fo := io.fileOverlay(); fo != nil {
		p.FileOverlay = fo
	}
	if tr := io.terminalRunner(); tr != nil {
		p.Terminal = tr
	}
}

// acpController is the slice of the controller's driving port the ACP transport
// drives: session lifecycle + persistence, turn execution, interactive approval,
// and the capability surface (commands/skills/MCP prompts). ACP never touches
// goals, checkpoints, or memory, so it depends on those sub-ports only — not the
// concrete *control.Controller.
type acpController interface {
	control.Lifecycle
	control.TurnControl
	RunFinalReadinessRecoveryWithAdmission(ctx context.Context, input string, onAdmitted func()) error
	TrySteer(text string) bool
	control.Approvals
	control.Capabilities
	control.SessionPersistence
	// Goals backs ACP's normal/plan/goal collaboration-mode surface.
	control.Goals
}

// acpSession is one open session: its controller, the on-disk transcript path
// (empty when persistence is off), and the cancel func of the in-flight turn
// (nil when idle) so session/cancel can abort it.
type acpSession struct {
	id         string
	ctrl       acpController
	sink       *updateSink
	transcript string
	cwd        string
	mcpServers []plugin.Spec
	model      string
	// nil means use config; non-nil empty string means provider default.
	effortOverride   *string
	runtimeProfile   string
	toolApprovalMode string
	// runtimeState is the effective planner/sandbox posture captured after CLI
	// hard overrides. status snapshots never reconstruct it from user config.
	runtimeState SessionRuntimeState
	status       *statusTelemetry
	// modeID is the ACP collaboration mode last reported to the client (normal |
	// plan | goal). Goal draft mode turns the next user prompt into the goal.
	// Both are guarded by mu; controller-side completion/plan exit is reconciled
	// after each turn through current_mode_update.
	modeID        string
	goalDraftMode bool
	// pendingConfig queues config deltas requested while a turn or rebuild is
	// in flight, holding at most one entry per axis: a later request replaces
	// only its own axis (last-write-wins per axis), so a model change and a
	// work-mode change queued back to back during one turn both survive to the
	// drain instead of the second overwriting the first.
	pendingConfig []sessionConfigDelta
	// pendingReload coalesces _reasonix.io/session/reloadExtensions requests
	// made while a turn or a rebuild is in flight; the finishTurn /
	// post-maintenance drains run it once the session is idle.
	pendingReload bool
	title         string
	createdAt     time.Time
	updatedAt     time.Time

	mu sync.Mutex
	// stateChangeMu serializes controller rebuilds with collaboration/approval
	// changes so a swap cannot overwrite a newer user selection.
	stateChangeMu sync.Mutex
	cancel        context.CancelFunc
	done          chan struct{}
	running       bool
	deleted       bool
	// lease is the session lease guarding transcript against other runtimes
	// (a desktop window, the CLI) for the life of this session. Held from
	// session/new / session/load and released on close/delete/teardown.
	// Config rebuilds keep the same transcript; when a snapshot conflict
	// retargets the controller to a recovery branch, sessionRecoveredHandler
	// moves transcript and this lease to the recovery file at commit time.
	lease *agent.SessionLease
	// retiredLeases tracks outgoing leases whose Release must run after the
	// authority-guarded save that triggered a recovery callback returns. Any
	// ACP operation that exposes a completed Snapshot waits for these channels,
	// so callers never observe the old transcript as still owned after the
	// handoff has completed.
	retiredLeases []<-chan struct{}
	// maintenanceDone is non-nil while session-owned maintenance, such as an
	// idle config rebuild, is in flight outside mu.
	maintenanceDone chan struct{}
}

func (s *acpSession) begin(ctx context.Context) (context.Context, context.CancelFunc, bool) {
	runCtx, cancel := context.WithCancel(ctx)
	// Prompt admission and config-axis changes share this lock. TryLock keeps
	// ACP admission non-blocking while closing the idle-check/use window in an
	// in-place role switch.
	if !s.stateChangeMu.TryLock() {
		cancel()
		return nil, nil, false
	}
	defer s.stateChangeMu.Unlock()
	s.mu.Lock()
	// A queued pendingConfig blocks new turns so a prompt never runs on the
	// outgoing config. The turn or maintenance that queued it applies it from
	// its defer, so no new turn is needed to drain the queue.
	if s.running || s.deleted || s.maintenanceDone != nil || len(s.pendingConfig) > 0 {
		s.mu.Unlock()
		cancel()
		return nil, nil, false
	}
	s.running = true
	s.cancel = cancel
	s.done = make(chan struct{})
	s.mu.Unlock()
	return runCtx, cancel, true
}

func (s *acpSession) finish() {
	s.mu.Lock()
	done := s.done
	s.running = false
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()
	if done != nil {
		close(done)
	}
}

func (s *acpSession) abort() {
	s.mu.Lock()
	c := s.cancel
	s.mu.Unlock()
	if c != nil {
		c()
	}
}

func (s *acpSession) abortAndWait() {
	s.mu.Lock()
	c := s.cancel
	done := s.done
	maintenanceDone := s.maintenanceDone
	s.mu.Unlock()
	if c != nil {
		c()
	}
	if done != nil {
		<-done
	}
	if maintenanceDone != nil {
		<-maintenanceDone
	}
}

func (s *acpSession) deleteAndWait() {
	s.mu.Lock()
	s.deleted = true
	c := s.cancel
	done := s.done
	maintenanceDone := s.maintenanceDone
	s.mu.Unlock()
	if c != nil {
		c()
	}
	if done != nil {
		<-done
	}
	if maintenanceDone != nil {
		<-maintenanceDone
	}
}

func (s *acpSession) finishMaintenance(done chan struct{}) {
	if done == nil {
		return
	}
	closeDone := false
	s.mu.Lock()
	if s.maintenanceDone == done {
		s.maintenanceDone = nil
		closeDone = true
	}
	s.mu.Unlock()
	if closeDone {
		close(done)
	}
}

// swapModeID records the mode reported to the client and returns the previous
// value, so callers can emit current_mode_update only on change.
func (s *acpSession) swapModeID(id string) (old string) {
	s.mu.Lock()
	old = s.modeID
	s.modeID = id
	s.mu.Unlock()
	return old
}

// currentModeID returns the mode last reported to the client.
func (s *acpSession) currentModeID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.modeID == "" {
		return sessionModeNormal
	}
	return s.modeID
}

func (s *acpSession) setGoalDraftMode(on bool) {
	s.mu.Lock()
	s.goalDraftMode = on
	s.mu.Unlock()
}

func (s *acpSession) takeGoalDraftMode() bool {
	s.mu.Lock()
	on := s.goalDraftMode
	s.goalDraftMode = false
	s.mu.Unlock()
	return on
}

func (s *acpSession) isGoalDraftMode() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.goalDraftMode
}

func (s *acpSession) setToolApprovalMode(mode string) {
	s.mu.Lock()
	s.toolApprovalMode = normalizeACPToolApprovalMode(mode)
	s.mu.Unlock()
}

func (s *acpSession) swapToolApprovalMode(mode string) (old string) {
	mode = normalizeACPToolApprovalMode(mode)
	s.mu.Lock()
	old = normalizeACPToolApprovalMode(s.toolApprovalMode)
	s.toolApprovalMode = mode
	s.mu.Unlock()
	return old
}

func (s *acpSession) saveMetaIfPresent() {
	s.mu.Lock()
	path := s.transcript
	meta := s.metaLocked()
	s.mu.Unlock()
	if path != "" && sessionFileExists(path) {
		_ = saveACPMeta(path, meta)
	}
}

// currentCtrl returns the session's controller under mu. rebuildSession swaps
// ctrl while holding mu, so any read of the field outside mu races with a
// concurrent config rebuild; always go through this accessor unless mu is
// already held.
func (s *acpSession) currentCtrl() acpController {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ctrl
}

// releaseSessionLease drops the session's transcript lease, if any. Idempotent.
func (s *acpSession) releaseSessionLease() {
	s.mu.Lock()
	lease := s.lease
	s.lease = nil
	s.mu.Unlock()
	if lease != nil {
		lease.Release()
	}
	s.waitForRetiredSessionLeases()
}

// retireSessionLease defers Release until the authority-guarded save that
// invoked a recovery callback can return. Releasing synchronously inside that
// callback would wait on the very save executing the callback and deadlock.
func (s *acpSession) retireSessionLease(lease *agent.SessionLease) {
	if lease == nil {
		return
	}
	done := make(chan struct{})
	s.mu.Lock()
	s.retiredLeases = append(s.retiredLeases, done)
	s.mu.Unlock()
	go func() {
		lease.Release()
		close(done)
	}()
}

func (s *acpSession) waitForRetiredSessionLeases() {
	s.mu.Lock()
	retired := append([]<-chan struct{}(nil), s.retiredLeases...)
	s.mu.Unlock()
	for _, done := range retired {
		<-done
	}
	s.mu.Lock()
	pending := s.retiredLeases[:0]
	for _, done := range s.retiredLeases {
		select {
		case <-done:
		default:
			pending = append(pending, done)
		}
	}
	s.retiredLeases = pending
	s.mu.Unlock()
}

// sessionLeaseBindError maps a lease-acquisition failure to the protocol
// error the client sees: a held session names its holder with the shared CLI
// wording; anything else is an internal error.
func sessionLeaseBindError(method string, err error) *RPCError {
	if errors.Is(err, agent.ErrSessionLeaseHeld) {
		return &RPCError{
			Code:    ErrInvalidRequest,
			Message: method + ": " + control.SessionInUseMessage(err) + "; " + control.SessionLeaseCloseHint,
		}
	}
	return &RPCError{Code: ErrInternal, Message: method + ": session lease: " + err.Error()}
}

// initialize advertises the agent's capability set: persisted load plus ACP v1
// list/resume/close/delete lifecycle helpers, prompts carrying inline resource
// text (embeddedContext) but not image/audio, and stdio / Streamable HTTP MCP
// (no legacy sse).
func (s *service) initialize(_ context.Context, raw json.RawMessage) (any, error) {
	var p InitializeParams
	if len(raw) > 0 && json.Unmarshal(raw, &p) == nil {
		s.setClientCapabilities(p.ClientCapabilities)
	}
	return InitializeResult{
		ProtocolVersion: ProtocolVersion,
		AgentCapabilities: AgentCapabilities{
			LoadSession: true,
			SessionCapabilities: SessionCapabilities{
				List:   &EmptyCapability{},
				Resume: &EmptyCapability{},
				Close:  &EmptyCapability{},
				Delete: &EmptyCapability{},
			},
			PromptCapabilities: PromptCapabilities{
				Image:           false,
				Audio:           false,
				EmbeddedContext: true,
			},
			MCPCapabilities: MCPCapabilities{HTTP: true, SSE: false},
			Meta: map[string]any{
				"reasonix.io": ReasonixExtensionCapabilities{
					MCPInteraction: &MCPInteractionCapability{Supported: true, SchemaVersion: 1, Method: mcpInteractionMethod},
					SessionSteer:   &SessionSteerCapability{Method: sessionSteerMethod},
					SessionInbox: &SessionInboxCapability{
						SchemaVersion: sessionInboxSchemaVersion,
						Methods: map[string]string{
							"enqueue":   sessionInboxEnqueueMethod,
							"list":      sessionInboxListMethod,
							"get":       sessionInboxGetMethod,
							"update":    sessionInboxUpdateMethod,
							"delete":    sessionInboxDeleteMethod,
							"move":      sessionInboxMoveMethod,
							"setPaused": sessionInboxPauseMethod,
							"retry":     sessionInboxRetryMethod,
							"refresh":   sessionInboxRefreshMethod,
						},
					},
					SessionReloadExtensions: &SessionReloadExtensionsCapability{Method: sessionReloadExtensionsMethod},
					ExtensionSurface:        &ExtensionSurfaceCapability{Supported: true, SchemaVersion: reasonixExtensionSurfaceSchemaVersion},
				},
				sessionStatusMethod:       ReasonixSchemaCapability{SchemaVersion: reasonixStatusSchemaVersion},
				sessionStatusUpdateMethod: ReasonixSchemaCapability{SchemaVersion: reasonixStatusSchemaVersion},
			},
		},
		AgentInfo:   Implementation{Name: s.info.Name, Version: s.info.Version},
		AuthMethods: []AuthMethod{reasonixSetupAuthMethod()},
	}, nil
}

func reasonixSetupAuthMethod() AuthMethod {
	return AuthMethod{
		ID:          "reasonix-setup",
		Name:        "Reasonix setup",
		Description: "Configure Reasonix providers and credentials in a terminal",
		Type:        "terminal",
		Args:        []string{"setup"},
	}
}

func (s *service) authenticate(_ context.Context, raw json.RawMessage) (any, error) {
	var p AuthenticateParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "authenticate: " + err.Error()}
	}
	if strings.TrimSpace(p.MethodID) != reasonixSetupAuthMethod().ID {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "authenticate: unknown methodId " + p.MethodID}
	}
	return AuthenticateResult{}, nil
}

// sessionNew opens a session: it mints an id, builds the session's sink bound to
// that id, asks the Factory to assemble the controller, switches the controller
// to interactive approval (so tool gates surface as ApprovalRequest events the
// sink forwards), and registers it.
func (s *service) sessionNew(ctx context.Context, raw json.RawMessage) (any, error) {
	var p SessionNewParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, &RPCError{Code: ErrInvalidParams, Message: "session/new: " + err.Error()}
		}
	}
	cwd, err := s.resolveSessionCwd(p.Cwd, "")
	if err != nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/new: " + err.Error()}
	}
	mcpServers, err := mcpSpecs(p.MCPServers, cwd)
	if err != nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/new: " + err.Error()}
	}
	cfgState, err := s.sessionConfigState(ctx, SessionConfigStateParams{Cwd: cwd})
	if err != nil {
		return nil, &RPCError{Code: ErrInternal, Message: "session/new: " + err.Error()}
	}
	cfgState = withToolApprovalConfig(cfgState, control.ToolApprovalAsk)
	runtimeState, err := s.sessionRuntimeState(ctx, SessionRuntimeStateParams{
		Cwd: cwd, Model: cfgState.Model, RuntimeProfile: cfgState.RuntimeProfile,
	})
	if err != nil {
		return nil, &RPCError{Code: ErrInternal, Message: "session/new: " + err.Error()}
	}

	id, err := newSessionID()
	if err != nil {
		return nil, &RPCError{Code: ErrInternal, Message: "session/new: " + err.Error()}
	}

	sink := newUpdateSink(s.conn, id)
	sink.bindCwd(cwd)
	sink.bindExtensionSurface(s.extensionSurfaceSupported())
	sessionParams := SessionParams{
		Cwd:            cwd,
		MCPServers:     mcpServers,
		Sink:           sink,
		Model:          cfgState.Model,
		EffortOverride: cloneStringPtr(cfgState.EffortOverride),
		RuntimeProfile: cfgState.RuntimeProfile,
	}
	s.bindSessionPathHandlers(id, &sessionParams)
	s.bindClientIO(&sessionParams, id)
	ctrl, err := s.factory.NewSession(ctx, sessionParams)
	if err != nil {
		return nil, &RPCError{Code: ErrInternal, Message: "session/new: " + err.Error()}
	}
	ctrl.EnableInteractiveApproval()
	sink.bindControllerPrompts(ctrl, sessionParams.MCPInteractions)

	now := time.Now().UTC()
	sess := &acpSession{
		id:               id,
		ctrl:             ctrl,
		sink:             sink,
		cwd:              cwd,
		mcpServers:       clonePluginSpecs(mcpServers),
		model:            cfgState.Model,
		effortOverride:   cloneStringPtr(cfgState.EffortOverride),
		runtimeProfile:   cfgState.RuntimeProfile,
		toolApprovalMode: control.ToolApprovalAsk,
		runtimeState:     runtimeState,
		status:           newStatusTelemetry(),
		modeID:           sessionModeNormal,
		createdAt:        now,
		updatedAt:        now,
	}
	s.bindStatusEvents(sess)
	// Pin a transcript file keyed by session id when the controller has a session
	// dir, so every turn auto-saves there, session/prompt can hand the path back,
	// and session/load can find it again by id across process restarts. The
	// session lease is taken with it (defensive: the id-keyed path is brand new)
	// so no other runtime can bind the transcript while this session lives.
	if dir := ctrl.SessionDir(); dir != "" {
		sess.transcript = transcriptPath(dir, id)
		lease, err := agent.TryAcquireSessionLease(sess.transcript)
		if err != nil {
			ctrl.Close()
			return nil, sessionLeaseBindError("session/new", err)
		}
		sess.lease = lease
		ctrl.SetFreshSessionPath(sess.transcript)
		if err := bindACPWriteAuthorityOrClose(ctrl, lease); err != nil {
			sess.lease = nil
			return nil, sessionLeaseBindError("session/new", err)
		}
	}

	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()

	// Fold in the live controller's extension catalog so plugin/... models
	// are discoverable from the very first session/new result.
	cfgState = enrichStateWithExtensionModels(cfgState, ctrl.ProviderCatalog())
	return afterResponse{
		result: SessionNewResult{
			SessionID:     id,
			Models:        cfgState.Models,
			Modes:         sessionModesState(sessionModeNormal),
			ConfigOptions: cfgState.ConfigOptions,
		},
		after: func() { s.sendAvailableCommands(sess) },
	}, nil
}

// Session modes exposed over ACP describe how the agent advances the task.
// Tool approval and runtime profile are independent config options. The legacy
// default/auto ids remain accepted for clients that used the old mixed axis.
const (
	sessionModeNormal        = "normal"
	sessionModePlan          = "plan"
	sessionModeGoal          = "goal"
	sessionModeLegacyDefault = "default"
	sessionModeLegacyAuto    = "auto"
)

func sessionModesState(current string) *SessionModeState {
	return &SessionModeState{
		CurrentModeID: current,
		AvailableModes: []SessionMode{
			{ID: sessionModeNormal, Name: "Normal", Description: "Work directly and pause when user input is required"},
			{ID: sessionModePlan, Name: "Plan", Description: "Research and propose a plan before making changes"},
			{ID: sessionModeGoal, Name: "Goal", Description: "Keep advancing the next prompt as a goal until complete or blocked"},
		},
	}
}

// sessionSetMode switches the session's operating mode and confirms it with a
// current_mode_update, per the ACP session-mode contract.
func (s *service) sessionSetMode(ctx context.Context, raw json.RawMessage) (any, error) {
	var p SessionSetModeParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/set_mode: " + err.Error()}
	}
	sess := s.session(p.SessionID)
	if sess == nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/set_mode: unknown session " + p.SessionID}
	}
	sess.stateChangeMu.Lock()
	defer sess.stateChangeMu.Unlock()
	ctrl := sess.currentCtrl()
	nextMode := p.ModeID
	legacyApproval := ""
	switch p.ModeID {
	case sessionModeNormal:
		ctrl.SetPlanMode(false)
		ctrl.ClearGoal()
	case sessionModePlan:
		ctrl.ClearGoal()
		ctrl.SetPlanMode(true)
	case sessionModeGoal:
		ctrl.SetPlanMode(false)
	case sessionModeLegacyDefault:
		nextMode = sessionModeNormal
		legacyApproval = control.ToolApprovalAsk
		ctrl.SetPlanMode(false)
		ctrl.ClearGoal()
	case sessionModeLegacyAuto:
		nextMode = sessionModeNormal
		legacyApproval = control.ToolApprovalYolo
		ctrl.SetPlanMode(false)
		ctrl.ClearGoal()
	default:
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/set_mode: unknown modeId " + p.ModeID}
	}
	sess.setGoalDraftMode(nextMode == sessionModeGoal && ctrl.GoalStatus() != control.GoalStatusRunning)
	if legacyApproval != "" {
		ctrl.SetToolApprovalMode(legacyApproval)
		sess.setToolApprovalMode(legacyApproval)
		if cfgState, err := s.configStateForSession(ctx, sess); err == nil {
			sess.sink.send(configOptionUpdate{SessionUpdate: "config_option_update", ConfigOptions: cfgState.ConfigOptions})
		}
	}
	if sess.swapModeID(nextMode) != nextMode {
		sess.sink.send(currentModeUpdate{SessionUpdate: "current_mode_update", CurrentModeID: nextMode})
	}
	sess.saveMetaIfPresent()
	return SessionSetModeResult{}, nil
}

// emitModeDrift reports controller-side mode flips (plan mode auto-exits when
// a plan is approved, a config rebuild resets switches) as current_mode_update
// so the client's mode picker stays truthful.
func (s *service) emitModeDrift(sess *acpSession) {
	// Hold stateChangeMu across the controller read and the session-state swap:
	// a session/set_mode completing between them (it holds this lock) would
	// otherwise be read back as drift, roll the session's modeID and metadata
	// back to the pre-selection value, and make the next rebuild re-apply that
	// stale mode to the replacement controller.
	sess.stateChangeMu.Lock()
	defer sess.stateChangeMu.Unlock()
	ctrl := sess.currentCtrl()
	current := sessionModeNormal
	switch {
	case ctrl.PlanMode():
		current = sessionModePlan
	case ctrl.GoalStatus() == control.GoalStatusRunning || sess.isGoalDraftMode():
		current = sessionModeGoal
	}
	if sess.swapModeID(current) != current {
		sess.sink.send(currentModeUpdate{SessionUpdate: "current_mode_update", CurrentModeID: current})
		sess.saveMetaIfPresent()
	}
}

func (s *service) emitToolApprovalDrift(ctx context.Context, sess *acpSession) {
	// Same contract as emitModeDrift: serialize with switchSessionToolApproval
	// and rebuilds so a user selection landing between the controller read and
	// the swap below is never reverted.
	sess.stateChangeMu.Lock()
	defer sess.stateChangeMu.Unlock()
	current := normalizeACPToolApprovalMode(sess.currentCtrl().ToolApprovalMode())
	if sess.swapToolApprovalMode(current) == current {
		return
	}
	if cfgState, err := s.configStateForSession(ctx, sess); err == nil {
		sess.sink.send(configOptionUpdate{SessionUpdate: "config_option_update", ConfigOptions: cfgState.ConfigOptions})
	}
	sess.saveMetaIfPresent()
}

// sessionLoad resumes a previously-saved session by id: it builds a controller
// (rooted at the requested cwd), seeds it from the on-disk transcript, replays
// the conversation to the client as session/update notifications, and registers
// it for subsequent prompts. A session already live in this process is replayed
// from memory without rebuilding.
func (s *service) sessionLoad(ctx context.Context, raw json.RawMessage) (any, error) {
	var p SessionLoadParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/load: " + err.Error()}
	}
	cfgState, err := s.openExistingSession(ctx, "session/load", p.SessionID, p.Cwd, p.MCPServers, true)
	if err != nil {
		return nil, err
	}
	return afterResponse{
		result: SessionLoadResult{Models: cfgState.Models, Modes: s.sessionModesFor(p.SessionID), ConfigOptions: cfgState.ConfigOptions},
		after:  func() { s.sendAvailableCommands(s.session(p.SessionID)) },
	}, nil
}

// sessionModesFor reports the modes state for a just-opened session. A live
// session keeps its current normal/plan/goal selection, so load/resume must not
// reset a reconnecting client's mode picker to normal.
func (s *service) sessionModesFor(id string) *SessionModeState {
	if sess := s.session(id); sess != nil {
		return sessionModesState(sess.currentModeID())
	}
	return sessionModesState(sessionModeNormal)
}

// sessionResume restores a previously-saved session without replaying its
// conversation history to the client.
func (s *service) sessionResume(ctx context.Context, raw json.RawMessage) (any, error) {
	var p SessionResumeParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/resume: " + err.Error()}
	}
	cfgState, err := s.openExistingSession(ctx, "session/resume", p.SessionID, p.Cwd, p.MCPServers, false)
	if err != nil {
		return nil, err
	}
	return afterResponse{
		result: SessionResumeResult{Models: cfgState.Models, Modes: s.sessionModesFor(p.SessionID), ConfigOptions: cfgState.ConfigOptions},
		after:  func() { s.sendAvailableCommands(s.session(p.SessionID)) },
	}, nil
}

func (s *service) openExistingSession(ctx context.Context, method, id, cwdParam string, servers []MCPServerSpec, replay bool) (SessionConfigState, error) {
	if err := validateSessionID(method, id); err != nil {
		return SessionConfigState{}, err
	}
	cwd, err := s.resolveSessionCwd(cwdParam, id)
	if err != nil {
		return SessionConfigState{}, &RPCError{Code: ErrInvalidParams, Message: method + ": " + err.Error()}
	}
	mcpServers, err := mcpSpecs(servers, cwd)
	if err != nil {
		return SessionConfigState{}, &RPCError{Code: ErrInvalidParams, Message: method + ": " + err.Error()}
	}

	if sess := s.session(id); sess != nil {
		if agent.IsCleanupPending(sess.transcript) {
			return SessionConfigState{}, &RPCError{Code: ErrInvalidParams, Message: method + ": unknown session " + id}
		}
		if replay {
			ctrl := sess.currentCtrl()
			replaySink := newUpdateSink(s.conn, id)
			replaySink.bindCwd(sess.cwd)
			replaySink.replay(ctrl.History())
		}
		cfgState, err := s.configStateForSession(ctx, sess)
		if err != nil {
			return SessionConfigState{}, &RPCError{Code: ErrInternal, Message: method + ": " + err.Error()}
		}
		return cfgState, nil
	}

	var saved acpSessionMeta
	persistedPath := ""
	if dir := s.sessionDir(); dir != "" {
		persistedPath = resolveTranscriptPath(dir, id)
		if agent.IsCleanupPending(persistedPath) {
			return SessionConfigState{}, &RPCError{Code: ErrInvalidParams, Message: method + ": unknown session " + id}
		}
		meta, _, metaErr := loadACPMeta(persistedPath)
		if metaErr != nil {
			return SessionConfigState{}, &RPCError{Code: ErrInternal, Message: method + ": " + metaErr.Error()}
		}
		saved = meta
	}
	cfgParams := SessionConfigStateParams{
		Cwd:            cwd,
		Model:          saved.Model,
		EffortOverride: cloneStringPtr(saved.EffortOverride),
		RuntimeProfile: saved.RuntimeProfile,
	}
	cfgState, err := s.sessionConfigState(ctx, cfgParams)
	if err != nil && (strings.TrimSpace(saved.Model) != "" || saved.EffortOverride != nil || strings.TrimSpace(saved.RuntimeProfile) != "") {
		cfgState, err = s.sessionConfigState(ctx, SessionConfigStateParams{Cwd: cwd})
	}
	if err != nil {
		return SessionConfigState{}, &RPCError{Code: ErrInternal, Message: method + ": " + err.Error()}
	}
	runtimeState, err := s.sessionRuntimeState(ctx, SessionRuntimeStateParams{
		Cwd: cwd, Model: cfgState.Model, RuntimeProfile: cfgState.RuntimeProfile,
	})
	if err != nil {
		return SessionConfigState{}, &RPCError{Code: ErrInternal, Message: method + ": " + err.Error()}
	}

	sink := newUpdateSink(s.conn, id)
	sink.bindCwd(cwd)
	sink.bindExtensionSurface(s.extensionSurfaceSupported())
	sessionParams := SessionParams{
		Cwd:            cwd,
		MCPServers:     mcpServers,
		Sink:           sink,
		Model:          cfgState.Model,
		EffortOverride: cloneStringPtr(cfgState.EffortOverride),
		RuntimeProfile: cfgState.RuntimeProfile,
	}
	s.bindSessionPathHandlers(id, &sessionParams)
	s.bindClientIO(&sessionParams, id)
	ctrl, err := s.factory.NewSession(ctx, sessionParams)
	if err != nil {
		return SessionConfigState{}, &RPCError{Code: ErrInternal, Message: method + ": " + err.Error()}
	}
	ctrl.EnableInteractiveApproval()
	sink.bindControllerPrompts(ctrl, sessionParams.MCPInteractions)

	dir := ctrl.SessionDir()
	if dir == "" {
		ctrl.Close()
		return SessionConfigState{}, &RPCError{Code: ErrInternal, Message: method + ": persistence is disabled"}
	}
	path := resolveTranscriptPath(dir, id)
	if path != persistedPath && agent.IsCleanupPending(path) {
		ctrl.Close()
		return SessionConfigState{}, &RPCError{Code: ErrInvalidParams, Message: method + ": unknown session " + id}
	}
	// Bind the transcript for writing only if no other runtime (a desktop
	// window, the CLI) holds it; the editor should not silently double-write a
	// session that is open elsewhere.
	lease, leaseErr := agent.TryAcquireSessionLease(path)
	if leaseErr != nil {
		ctrl.Close()
		return SessionConfigState{}, sessionLeaseBindError(method, leaseErr)
	}
	loaded, err := agent.LoadSession(path)
	if err != nil {
		lease.Release()
		ctrl.Close()
		return SessionConfigState{}, &RPCError{Code: ErrInvalidParams, Message: method + ": unknown session " + id}
	}
	if err := resumeACPControllerForWrite(ctrl, loaded, path, lease); err != nil {
		return SessionConfigState{}, sessionLeaseBindError(method, err)
	}
	toolApprovalMode := normalizeACPToolApprovalMode(saved.ToolApprovalMode)
	ctrl.SetToolApprovalMode(toolApprovalMode)
	modeID := normalizeACPCollaborationMode(saved.CollaborationMode)
	goalDraftMode := false
	switch modeID {
	case sessionModePlan:
		ctrl.SetPlanMode(true)
	case sessionModeGoal:
		ctrl.SetPlanMode(false)
		goalDraftMode = ctrl.GoalStatus() != control.GoalStatusRunning
	default:
		if ctrl.GoalStatus() == control.GoalStatusRunning {
			modeID = sessionModeGoal
		} else {
			modeID = sessionModeNormal
			ctrl.SetPlanMode(false)
		}
	}

	meta := metadataForLoadedSession(path, id, cwd, ctrl.History())
	meta.Model = cfgState.Model
	meta.EffortOverride = cloneStringPtr(cfgState.EffortOverride)
	meta.RuntimeProfile = cfgState.RuntimeProfile
	meta.ToolApprovalMode = toolApprovalMode
	meta.CollaborationMode = modeID
	cfgState = withToolApprovalConfig(cfgState, toolApprovalMode)
	sess := &acpSession{
		id:               id,
		ctrl:             ctrl,
		sink:             sink,
		transcript:       path,
		cwd:              meta.Cwd,
		mcpServers:       clonePluginSpecs(mcpServers),
		model:            cfgState.Model,
		effortOverride:   cloneStringPtr(cfgState.EffortOverride),
		runtimeProfile:   cfgState.RuntimeProfile,
		toolApprovalMode: toolApprovalMode,
		runtimeState:     runtimeState,
		status:           restoreStatusTelemetry(saved.Status),
		modeID:           modeID,
		goalDraftMode:    goalDraftMode,
		title:            meta.Title,
		createdAt:        meta.CreatedAt,
		updatedAt:        meta.UpdatedAt,
		lease:            lease,
	}
	s.bindStatusEvents(sess)
	if err := saveACPMeta(path, sess.meta()); err != nil {
		sess.releaseSessionLease()
		ctrl.Close()
		return SessionConfigState{}, &RPCError{Code: ErrInternal, Message: method + ": " + err.Error()}
	}
	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()

	if replay {
		sink.replay(ctrl.History())
	}
	return enrichStateWithExtensionModels(cfgState, ctrl.ProviderCatalog()), nil
}

// transcriptPath is where a session's transcript lives — keyed by id so
// session/load can recover it. Distinct from the cli's timestamp-labelled
// chat/run session files (those are addressed by a picker, not by id).
func transcriptPath(dir, id string) string {
	return filepath.Join(dir, id+".jsonl")
}

// resolveTranscriptPath returns the transcript file session id currently
// lives in. That is the id-keyed path by default; after a snapshot recovery
// moved the live session onto a recovery branch, the id-keyed sidecar carries
// an ActiveTranscript redirect (written by sessionRecoveredHandler) that
// load/resume/delete/meta lookups must follow, or a restart silently reopens
// the pre-recovery transcript. The redirect is a basename, must stay inside
// dir, and its target must exist and claim the same session id; anything else
// falls back to the id-keyed path.
func resolveTranscriptPath(dir, id string) string {
	path := transcriptPath(dir, id)
	meta, ok, err := loadACPMeta(path)
	if err != nil || !ok {
		return path
	}
	active := strings.TrimSpace(meta.ActiveTranscript)
	if active == "" || active == filepath.Base(path) {
		return path
	}
	if filepath.Base(active) != active {
		return path
	}
	resolved := filepath.Join(dir, active)
	if !sessionFileExists(resolved) {
		return path
	}
	targetMeta, ok, err := loadACPMeta(resolved)
	if err != nil || !ok || targetMeta.SessionID != id {
		return path
	}
	return resolved
}

// sessionPrompt runs one turn. It flattens the prompt blocks to text and runs the
// session's controller synchronously under a per-turn cancelable context (so
// session/cancel can stop it), then reports why the turn ended. The controller
// streams the turn's events to the session's sink as it runs.
func (s *service) sessionPrompt(ctx context.Context, raw json.RawMessage) (any, error) {
	var p SessionPromptParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/prompt: " + err.Error()}
	}
	sess := s.session(p.SessionID)
	if sess == nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/prompt: unknown session " + p.SessionID}
	}
	text := FlattenPrompt(p.Prompt)
	if text == "" && p.Action != control.ProtocolRecoveryAction {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/prompt: empty prompt"}
	}
	protocolRecovery := p.Action == control.ProtocolRecoveryAction
	if protocolRecovery && p.RecoveryID == "" {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/prompt: missing recoveryId"}
	}
	recovery := p.Action == control.FinalReadinessRecoveryAction
	if p.Action != "" && !recovery && !protocolRecovery {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/prompt: unsupported action " + p.Action}
	}
	if p.Action == "" {
		if id, guidance, ok := control.ParseProtocolRecoveryCommand(text); ok {
			protocolRecovery = true
			p.RecoveryID = id
			text = guidance
		} else if prompt, ok := control.ParseFinalReadinessRecoveryCommand(text); ok {
			recovery = true
			text = prompt
		} else {
			// The builtin /clear verb is handled here, ahead of custom commands and
			// skills (matching Submit's precedence), because the ACP turn path never
			// reaches submitCommandOrTurnReady's verb dispatch.
			if cleared, res, err := s.handleClearPrompt(ctx, sess, text); cleared {
				return res, err
			}
			text = s.resolveSlashPrompt(ctx, sess, text)
		}
	}

	runCtx, cancel, ok := sess.begin(ctx)
	if !ok {
		return nil, &RPCError{Code: ErrInvalidRequest, Message: "session/prompt: session already has an active prompt"}
	}
	defer func() {
		sess.sink.clearTurnContext()
		s.finishTurn(ctx, sess)
		cancel()
	}()
	statusStarted := false
	beginTurn := func() {
		if sess.status == nil {
			sess.status = newStatusTelemetry()
		}
		sess.status.beginTurn()
		s.publishStatus(sess, "phase")
		sess.sink.setTurnContext(runCtx)
		if sess.takeGoalDraftMode() {
			sess.currentCtrl().SetGoal(text)
			sess.saveMetaIfPresent()
		}
		statusStarted = true
	}
	var runErr error
	if protocolRecovery {
		if runner, ok := sess.ctrl.(interface {
			RunProtocolRecoveryWithAdmission(context.Context, string, string, func()) error
		}); ok {
			runErr = runner.RunProtocolRecoveryWithAdmission(runCtx, p.RecoveryID, text, beginTurn)
		} else {
			return nil, &RPCError{Code: ErrInvalidRequest, Message: "protocol recovery is unsupported by this controller"}
		}
	} else if recovery {
		runErr = sess.ctrl.RunFinalReadinessRecoveryWithAdmission(runCtx, text, beginTurn)
	} else {
		beginTurn()
		runErr = sess.ctrl.RunTurn(runCtx, text)
	}
	if errors.Is(runErr, agent.ErrProtocolRecoveryUnavailable) && !statusStarted {
		return nil, &RPCError{Code: ErrInvalidRequest, Message: "session/prompt: protocol recovery is unavailable or stale"}
	}
	if errors.Is(runErr, control.ErrNoFinalReadinessRecovery) && !statusStarted {
		return nil, &RPCError{
			Code:    ErrInvalidRequest,
			Message: "session/prompt: no pending final-readiness check to continue",
		}
	}
	runErr = drainACPInbox(runCtx, sess.ctrl, runErr)
	cancelled := runCtx.Err() != nil

	statusEvent := sess.status.finishTurn(
		runErr,
		cancelled,
		sess.currentCtrl().GoalStatus(),
		finalAssistantSummary(sess.currentCtrl()),
	)
	s.publishStatus(sess, statusEvent)
	// Persist after status finalization (best-effort) so reconnect recovers both
	// the transcript and the same sequence/usage/outcome snapshot.
	sess.persistAfterTurn(text)

	stop, warning, promptErr := promptStopReason(runErr, cancelled, p.SessionID)
	if promptErr != nil {
		return nil, promptErr
	}
	if warning != "" {
		// The TUI keeps completed work for deliberate run boundaries; mirror
		// that for ACP and tell clients how the successful turn ended.
		sess.sink.Emit(promptPauseNotice(runErr, warning))
	}
	res := SessionPromptResult{StopReason: stop}
	if sess.transcript != "" {
		res.TranscriptPath = &sess.transcript
	}
	return res, nil
}

// finalReadinessNotice is the warning text ACP clients receive when a completed
// turn's final-readiness gate stays unsatisfied; the TUI shows the same gaps in
// its recovery card.
func finalReadinessNotice(e *agent.FinalReadinessError) string {
	const maxNoticeBytes = 2_048
	const fallback = "final-answer readiness gate not satisfied"
	if e == nil {
		return fallback
	}
	if reason := strings.TrimSpace(e.Reason); reason != "" {
		return clipStatusCredentialText(fallback+": "+reason, maxNoticeBytes)
	}
	return clipStatusError(e, maxNoticeBytes)
}

// sessionSteer durably persists guidance then attempts mid-turn admission.
// Parameter/session errors remain RPC errors; busy rejection returns a
// disposition so clients can keep the durable follow-up.
func (s *service) sessionSteer(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionSteerParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: sessionSteerMethod + ": " + err.Error()}
	}
	sess := s.session(p.SessionID)
	if sess == nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: sessionSteerMethod + ": unknown session " + p.SessionID}
	}
	text := FlattenPrompt(p.Prompt)
	if text == "" {
		return nil, &RPCError{Code: ErrInvalidParams, Message: sessionSteerMethod + ": empty prompt"}
	}
	ctrl := sess.currentCtrl()
	if api, ok := ctrl.(control.SessionAPI); ok {
		if ensurer, ok := any(api).(interface{ EnsureSessionPath() }); ok {
			ensurer.EnsureSessionPath()
		}
		// Durable path when the session has a transcript path; ephemeral
		// test controllers without persistence fall back to TrySteer.
		if api.SessionPath() != "" {
			rec, err := api.TryEnqueueAndSteer(control.InboxRequest{
				Intent:  sessioninbox.IntentSteer,
				Display: text,
				Raw:     text,
				Submit:  text,
				Source:  "acp",
			})
			if err != nil {
				return nil, &RPCError{Code: ErrInvalidRequest, Message: sessionSteerMethod + ": " + err.Error()}
			}
			return SessionSteerResult{ItemID: rec.ItemID, Disposition: string(rec.Disposition)}, nil
		}
	}
	// Compatibility for older controller stubs / pathless sessions.
	if !ctrl.TrySteer(text) {
		return nil, &RPCError{Code: ErrInvalidRequest, Message: sessionSteerMethod + ": session has no active prompt"}
	}
	return SessionSteerResult{Disposition: "steer_accepted"}, nil
}

// sessionReloadExtensions rebuilds a session's agent runtime in place —
// tools, skills, commands, hooks, MCP servers, and providers are re-discovered
// — while the session (transcript, approval grants, goal and recovery state)
// carries over via boot.Rebuild. It follows the same contract as a config
// switch: a turn or rebuild in flight coalesces exactly one queued reload,
// drained when the session goes idle; a failure keeps the old controller fully
// usable; the old controller's resources are released only after the swap.
func (s *service) sessionReloadExtensions(ctx context.Context, raw json.RawMessage) (any, error) {
	var p SessionReloadExtensionsParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: sessionReloadExtensionsMethod + ": " + err.Error()}
	}
	sess := s.session(p.SessionID)
	if sess == nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: sessionReloadExtensionsMethod + ": unknown session " + p.SessionID}
	}
	return s.reloadSessionExtensions(ctx, sess)
}

func (s *service) reloadSessionExtensions(ctx context.Context, sess *acpSession) (any, error) {
	rebuilder, ok := s.factory.(SessionRebuilder)
	if !ok {
		return nil, &RPCError{Code: ErrInvalidRequest, Message: sessionReloadExtensionsMethod + ": runtime reload is unavailable in this session"}
	}
	if !sess.stateChangeMu.TryLock() {
		// A config switch or reload is in maintenance: coalesce one reload
		// behind it; the maintenance owner's post-maintenance drain runs it
		// (mirrors the pendingConfig queue contract in rebuildSession).
		sess.mu.Lock()
		if sess.maintenanceDone != nil && !sess.deleted {
			sess.pendingReload = true
			sess.mu.Unlock()
			return SessionReloadExtensionsResult{Queued: true}, nil
		}
		sess.mu.Unlock()
		sess.stateChangeMu.Lock()
	}
	didMaintenance := false
	res, err := s.reloadSessionExtensionsLocked(ctx, sess, rebuilder, &didMaintenance)
	sess.stateChangeMu.Unlock()
	if didMaintenance {
		s.reportPendingSessionConfigError(ctx, sess, s.applyPendingSessionConfig(ctx, sess), "after maintenance")
		s.drainPendingReload(ctx, sess)
	}
	return res, err
}

// reloadSessionExtensionsLocked is reloadSessionExtensions' body; callers hold
// stateChangeMu. The busy/queue checks and the publish/close ordering mirror
// rebuildSessionLocked, but the build itself goes through the factory's
// boot.Rebuild path instead of NewSession + manual migration.
func (s *service) reloadSessionExtensionsLocked(ctx context.Context, sess *acpSession, rebuilder SessionRebuilder, didMaintenance *bool) (any, error) {
	sess.mu.Lock()
	if sess.deleted {
		sess.mu.Unlock()
		return nil, &RPCError{Code: ErrInvalidRequest, Message: sessionReloadExtensionsMethod + ": session is deleted"}
	}
	status := sess.ctrl.RuntimeStatus()
	if status.PendingPrompt {
		sess.mu.Unlock()
		return nil, sessionConfigActiveWorkError("answer pending prompts before reloading the runtime")
	}
	if !sess.running && !status.Running && status.BackgroundJobs > 0 {
		sess.mu.Unlock()
		return nil, sessionConfigActiveWorkError("stop background jobs before reloading the runtime")
	}
	if sess.running || status.Running || sess.maintenanceDone != nil {
		// Busy: coalesce exactly one reload; finishTurn (or the maintenance
		// owner's post-maintenance drain) runs it once the session is idle.
		sess.pendingReload = true
		sess.mu.Unlock()
		return SessionReloadExtensionsResult{Queued: true}, nil
	}
	// Claim the queued reload and raise maintenance in the same critical
	// section (mirrors rebuildSessionLocked): begin must never observe an
	// idle session between the two.
	sess.pendingReload = false
	cur := sess.ctrl
	sink := sess.sink
	mcpServers := clonePluginSpecs(sess.mcpServers)
	cwd := sess.cwd
	model := sess.model
	effortOverride := cloneStringPtr(sess.effortOverride)
	runtimeProfile := sess.runtimeProfile
	maintenanceDone := make(chan struct{})
	sess.maintenanceDone = maintenanceDone
	*didMaintenance = true
	sess.mu.Unlock()
	defer func() {
		sess.finishMaintenance(maintenanceDone)
	}()

	if err := snapshotACPController(sess, cur); err != nil {
		return nil, &RPCError{Code: ErrInternal, Message: sessionReloadExtensionsMethod + ": snapshot before reload: " + err.Error()}
	}
	// Read the path only after Snapshot: a conflict can retarget cur to a
	// recovery branch, and boot.Rebuild binds the replacement to whatever
	// cur reports now (see rebuildSessionLocked). SessionPath is
	// controller-locked, so reading it off sess.mu is safe.
	prevPath := cur.SessionPath()
	old, ok := cur.(*control.Controller)
	if !ok {
		return nil, &RPCError{Code: ErrInternal, Message: sessionReloadExtensionsMethod + ": session controller does not support rebuild"}
	}
	rebuildParams := SessionParams{
		Cwd:            cwd,
		MCPServers:     mcpServers,
		Sink:           sink,
		Model:          model,
		EffortOverride: effortOverride,
		RuntimeProfile: runtimeProfile,
	}
	s.bindSessionPathHandlers(sess.id, &rebuildParams)
	// The rebuilt controller must keep the client-capability wiring (fs
	// overlay, host terminal) — mirrors rebuildSessionLocked.
	s.bindClientIO(&rebuildParams, sess.id)
	newCtrl, err := rebuilder.RebuildSession(ctx, rebuildParams, old)
	if err != nil {
		return nil, &RPCError{Code: ErrInternal, Message: sessionReloadExtensionsMethod + ": " + err.Error()}
	}
	newCtrl.EnableInteractiveApproval()
	// Config on disk may have changed the effective planner/sandbox posture;
	// recompute the status snapshot from the same resolved inputs.
	runtimeState, err := s.sessionRuntimeState(ctx, SessionRuntimeStateParams{
		Cwd: cwd, Model: model, RuntimeProfile: runtimeProfile,
	})
	if err != nil {
		newCtrl.ReleaseResources()
		return nil, &RPCError{Code: ErrInternal, Message: sessionReloadExtensionsMethod + ": runtime state: " + err.Error()}
	}
	// Persist before publishing the replacement. If this fails, the outgoing
	// controller and transcript still agree and remain fully usable (mirrors
	// the config switch).
	if err := s.prepareACPReplacementAuthority(sess, newCtrl, cur, prevPath, "snapshot after reload"); err != nil {
		newCtrl.ReleaseResources()
		return nil, &RPCError{Code: ErrInternal, Message: sessionReloadExtensionsMethod + ": " + err.Error()}
	}

	sess.mu.Lock()
	if sess.deleted {
		sess.mu.Unlock()
		newCtrl.ReleaseResources()
		return nil, &RPCError{Code: ErrInvalidRequest, Message: sessionReloadExtensionsMethod + ": session is deleted"}
	}
	if sess.ctrl != cur {
		sess.mu.Unlock()
		newCtrl.ReleaseResources()
		return nil, sessionConfigActiveWorkError("session changed while reloading; retry")
	}
	sess.ctrl = newCtrl
	sess.runtimeState = runtimeState
	if sess.transcript != "" && sessionFileExists(sess.transcript) {
		_ = saveACPMeta(sess.transcript, sess.metaLocked())
	}
	sess.mu.Unlock()
	sink.bindControllerPrompts(newCtrl, rebuildParams.MCPInteractions)

	// Release the outgoing controller only after the swap published the
	// replacement. ReleaseResources (not Close): the session logically
	// continues, so SessionEnd hooks must not fire — mirrors the config
	// switch.
	cur.ReleaseResources()
	// Clients see refreshed plugin commands without waiting for the next turn.
	s.sendAvailableCommands(sess)
	return SessionReloadExtensionsResult{}, nil
}

// drainPendingReload runs the coalesced reloadExtensions request once the
// session is idle. Called from finishTurn and after a config switch's or a
// reload's own maintenance completes; callers must NOT hold stateChangeMu
// (the reload re-acquires it).
func (s *service) drainPendingReload(ctx context.Context, sess *acpSession) {
	if _, ok := s.factory.(SessionRebuilder); !ok {
		return
	}
	sess.mu.Lock()
	if !sess.pendingReload || sess.deleted || sess.running || sess.maintenanceDone != nil || len(sess.pendingConfig) > 0 {
		sess.mu.Unlock()
		return
	}
	sess.mu.Unlock()
	if _, err := s.reloadSessionExtensions(ctx, sess); err != nil {
		s.reportPendingSessionConfigError(ctx, sess, err, "after queued reload")
	}
}

// finishTurn reconciles controller-side drift and drains any config switch
// queued during the turn. Drift must be reconciled before finish() exposes
// the session as idle: a concurrent config switch races on sess.running, and
// if it wins that race while modeID/toolApprovalMode are still stale (a
// slash command or plan/goal completion changed them inside the turn), it
// rebuilds the replacement controller from the outgoing state instead of the
// one this turn actually ended in.
func (s *service) finishTurn(ctx context.Context, sess *acpSession) {
	s.emitModeDrift(sess)
	s.emitToolApprovalDrift(ctx, sess)
	sess.finish()
	s.reportPendingSessionConfigError(ctx, sess, s.applyPendingSessionConfig(ctx, sess), "after turn")
	// A reloadExtensions request queued during the turn runs now that the
	// session may be idle; the drain re-checks busy state.
	s.drainPendingReload(ctx, sess)
	// Re-check after a rebuild in case the replacement normalized state.
	s.emitModeDrift(sess)
	s.emitToolApprovalDrift(ctx, sess)
}

// sessionSetConfigOption applies ACP's generic session-level selectors for
// model, reasoning effort, work mode, and tool approval.
func (s *service) sessionSetConfigOption(ctx context.Context, raw json.RawMessage) (any, error) {
	var p SetSessionConfigOptionParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/set_config_option: " + err.Error()}
	}
	sess := s.session(p.SessionID)
	if sess == nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/set_config_option: unknown session " + p.SessionID}
	}
	// The execution-mode options are now the session quality floor: light
	// folds to standard silently, delivery sets the delivery floor.
	if id := normalizeConfigID(p.ConfigID); id == "work_mode" || id == "agent_preset" || id == "quality_floor" {
		if err := validateDeprecatedModeValue(p.Value); err != nil {
			return nil, &RPCError{Code: ErrInvalidParams, Message: "session/set_config_option: " + err.Error()}
		}
		ctrl := sess.currentCtrl()
		if ctrl != nil {
			if p, err := agentpreset.Normalize(p.Value); err == nil {
				if err := ctrl.SetQualityFloor(string(p)); err != nil {
					return nil, &RPCError{Code: ErrInternal, Message: "session/set_config_option: " + err.Error()}
				}
			}
		}
		cfgState, err := s.configStateForSession(ctx, sess)
		if err != nil {
			return nil, &RPCError{Code: ErrInternal, Message: "session/set_config_option: " + err.Error()}
		}
		return SetSessionConfigOptionResult{
			ConfigOptions: cfgState.ConfigOptions,
		}, nil
	}
	cfgState, err := s.configStateForSession(ctx, sess)
	if err != nil {
		return nil, &RPCError{Code: ErrInternal, Message: "session/set_config_option: " + err.Error()}
	}
	option, ok := findConfigOption(cfgState.ConfigOptions, p.ConfigID)
	if !ok {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/set_config_option: unknown config option " + p.ConfigID}
	}
	if !configOptionHasValue(option, p.Value) {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/set_config_option: invalid value " + p.Value + " for " + option.ID}
	}

	var next SessionConfigState
	switch configOptionCategory(option) {
	case "model":
		next, err = s.switchSessionModel(ctx, sess, p.Value)
	case "thought_level":
		next, err = s.switchSessionEffort(ctx, sess, p.Value)
	case "tool_approval":
		next, err = s.switchSessionToolApproval(ctx, sess, p.Value)
	default:
		err = &RPCError{Code: ErrInvalidParams, Message: "session/set_config_option: unsupported config option " + option.ID}
	}
	if err != nil {
		return nil, err
	}
	return SetSessionConfigOptionResult{ConfigOptions: next.ConfigOptions}, nil
}

// sessionSetModel keeps older ACP clients working while configOptions becomes
// the preferred model selector.
func (s *service) sessionSetModel(ctx context.Context, raw json.RawMessage) (any, error) {
	var p SetSessionModelParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/set_model: " + err.Error()}
	}
	sess := s.session(p.SessionID)
	if sess == nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/set_model: unknown session " + p.SessionID}
	}
	if _, err := s.switchSessionModel(ctx, sess, p.ModelID); err != nil {
		return nil, err
	}
	return SetSessionModelResult{}, nil
}

// sessionConfigDelta names exactly one config axis a caller asked to change
// (tool approval never rebuilds the controller, so it has no delta here).
// rebuildSession queues these — instead of a fully resolved SessionConfigState
// — while a turn or rebuild is in flight, one queue entry per axis, and
// applyPendingSessionConfig re-resolves the queued set against the session's
// live baseline once the session is idle. That way a queued change to one axis
// can never restore a stale value on another axis that changed in the
// meantime, whether that axis rebuilt already or is queued alongside.
type sessionConfigDelta struct {
	axis           string
	model          string
	effortOverride *string
}

func (d sessionConfigDelta) clone() sessionConfigDelta {
	d.effortOverride = cloneStringPtr(d.effortOverride)
	return d
}

// mergePendingConfig queues delta with last-write-wins per axis: it replaces a
// queued entry for the same axis and appends otherwise, so a change queued for
// one axis can never drop a change queued for another. Callers hold sess.mu.
func mergePendingConfig(queue []sessionConfigDelta, delta sessionConfigDelta) []sessionConfigDelta {
	for i := range queue {
		if queue[i].axis == delta.axis {
			queue[i] = delta.clone()
			return queue
		}
	}
	return append(queue, delta.clone())
}

// removePendingAxes drops the queue entries whose axis a rebuild is applying,
// keeping entries other requests queued in the meantime so the post-maintenance
// drain still applies them. Callers hold sess.mu.
func removePendingAxes(queue, applied []sessionConfigDelta) []sessionConfigDelta {
	if len(queue) == 0 {
		return nil
	}
	kept := queue[:0]
	for _, q := range queue {
		drop := false
		for _, d := range applied {
			if q.axis == d.axis {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, q)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

func clonePendingConfig(queue []sessionConfigDelta) []sessionConfigDelta {
	if len(queue) == 0 {
		return nil
	}
	out := make([]sessionConfigDelta, len(queue))
	for i := range queue {
		out[i] = queue[i].clone()
	}
	return out
}

func (d sessionConfigDelta) applyTo(p *SessionConfigStateParams) {
	switch d.axis {
	case "model":
		p.Model = d.model
	case "thought_level":
		p.EffortOverride = cloneStringPtr(d.effortOverride)
	}
}

// resolveSessionConfigDeltas resolves deltas against the session's current
// config baseline. Calling this fresh at every apply — instead of reusing a
// snapshot taken when a change was first requested — is what keeps a queued
// delta for one axis from clobbering another axis that rebuilt in between.
func (s *service) resolveSessionConfigDeltas(ctx context.Context, sess *acpSession, deltas []sessionConfigDelta) (SessionConfigState, error) {
	params := sess.configStateParams()
	for _, delta := range deltas {
		delta.applyTo(&params)
	}
	cfgState, err := s.sessionConfigState(ctx, params)
	if err != nil {
		return SessionConfigState{}, err
	}
	return withToolApprovalConfig(cfgState, sess.currentToolApprovalMode()), nil
}

func (s *service) switchSessionModel(ctx context.Context, sess *acpSession, modelID string) (SessionConfigState, error) {
	deltas := []sessionConfigDelta{{axis: "model", model: modelID}}
	return s.switchSessionConfig(ctx, sess, deltas)
}

func (s *service) switchSessionEffort(ctx context.Context, sess *acpSession, effort string) (SessionConfigState, error) {
	level := strings.TrimSpace(effort)
	if level == "auto" {
		level = ""
	}
	deltas := []sessionConfigDelta{{axis: "thought_level", effortOverride: &level}}
	return s.switchSessionConfig(ctx, sess, deltas)
}

func (s *service) switchSessionConfig(ctx context.Context, sess *acpSession, deltas []sessionConfigDelta) (SessionConfigState, error) {
	resolve := func() (SessionConfigState, error) {
		cfgState, err := s.resolveSessionConfigDeltas(ctx, sess, deltas)
		if err != nil {
			method := "session/set_config_option"
			if len(deltas) == 1 && deltas[0].axis == "model" {
				method = "session/set_model"
			}
			return SessionConfigState{}, &RPCError{Code: ErrInvalidParams, Message: method + ": " + err.Error()}
		}
		if len(deltas) == 1 && deltas[0].axis == "model" && cfgState.Model == "" {
			return SessionConfigState{}, &RPCError{Code: ErrInvalidRequest, Message: "session/set_model: model switching is unavailable in this session"}
		}
		return cfgState, nil
	}

	if !sess.stateChangeMu.TryLock() {
		// Preserve the non-blocking queue contract while a rebuild is already in
		// maintenance. Resolve once for validation and the immediate client update;
		// the drain resolves the queued deltas again against live state.
		cfgState, err := resolve()
		if err != nil {
			return SessionConfigState{}, err
		}
		sess.mu.Lock()
		if sess.maintenanceDone != nil && !sess.deleted {
			for _, delta := range deltas {
				sess.pendingConfig = mergePendingConfig(sess.pendingConfig, delta)
			}
			sess.mu.Unlock()
			sess.sink.send(configOptionUpdate{SessionUpdate: "config_option_update", ConfigOptions: cfgState.ConfigOptions})
			return cfgState, nil
		}
		sess.mu.Unlock()
		sess.stateChangeMu.Lock()
	}

	// Always resolve inside the serialization domain. Even a successful TryLock
	// can follow a concurrent rebuild that completed after this request began.
	cfgState, err := resolve()
	if err != nil {
		sess.stateChangeMu.Unlock()
		return SessionConfigState{}, err
	}
	didMaintenance := false
	err = s.rebuildSessionLocked(ctx, sess, cfgState, deltas, &didMaintenance)
	sess.stateChangeMu.Unlock()
	if didMaintenance {
		pendingErr := s.applyPendingSessionConfig(ctx, sess)
		s.reportPendingSessionConfigError(ctx, sess, pendingErr, "after maintenance")
		// A reloadExtensions request queued behind this maintenance runs next.
		s.drainPendingReload(ctx, sess)
		// The pending drain completes before this request returns. Refresh the RPC
		// result so an older response cannot overwrite the newer config_option_update
		// with the pre-drain full snapshot on the client.
		if current, stateErr := s.configStateForSession(ctx, sess); stateErr == nil {
			cfgState = current
		}
	}
	if err != nil {
		return SessionConfigState{}, err
	}
	return cfgState, nil
}

func (s *service) switchSessionToolApproval(ctx context.Context, sess *acpSession, mode string) (SessionConfigState, error) {
	sess.stateChangeMu.Lock()
	defer sess.stateChangeMu.Unlock()
	mode = normalizeACPToolApprovalMode(mode)
	ctrl := sess.currentCtrl()
	ctrl.SetToolApprovalMode(mode)
	sess.setToolApprovalMode(mode)
	sess.saveMetaIfPresent()
	cfgState, err := s.configStateForSession(ctx, sess)
	if err != nil {
		return SessionConfigState{}, &RPCError{Code: ErrInternal, Message: "session/set_config_option: " + err.Error()}
	}
	sess.sink.send(configOptionUpdate{SessionUpdate: "config_option_update", ConfigOptions: cfgState.ConfigOptions})
	return cfgState, nil
}

func (s *service) rebuildSession(ctx context.Context, sess *acpSession, cfgState SessionConfigState, deltas []sessionConfigDelta) error {
	if !sess.stateChangeMu.TryLock() {
		// Preserve the existing queue contract: a config change arriving during
		// a controller build returns immediately and is applied after that
		// build. The queue keeps one delta per axis (last-write-wins within an
		// axis), so changes queued for different axes never clobber each other.
		// Collaboration/approval changes do not use this queue; they wait for
		// the swap and then update the replacement controller.
		sess.mu.Lock()
		if sess.maintenanceDone != nil && !sess.deleted {
			for _, delta := range deltas {
				sess.pendingConfig = mergePendingConfig(sess.pendingConfig, delta)
			}
			sess.mu.Unlock()
			sess.sink.send(configOptionUpdate{SessionUpdate: "config_option_update", ConfigOptions: cfgState.ConfigOptions})
			return nil
		}
		sess.mu.Unlock()
		sess.stateChangeMu.Lock()
	}
	didMaintenance := false
	err := s.rebuildSessionLocked(ctx, sess, cfgState, deltas, &didMaintenance)
	sess.stateChangeMu.Unlock()
	if didMaintenance {
		pendingErr := s.applyPendingSessionConfig(ctx, sess)
		s.reportPendingSessionConfigError(ctx, sess, pendingErr, "after maintenance")
		// A reloadExtensions request queued behind this maintenance runs next.
		s.drainPendingReload(ctx, sess)
	}
	return err
}

func (s *service) rebuildSessionLocked(ctx context.Context, sess *acpSession, cfgState SessionConfigState, deltas []sessionConfigDelta, didMaintenance *bool) (retErr error) {
	sess.mu.Lock()
	if sess.deleted {
		sess.mu.Unlock()
		return &RPCError{Code: ErrInvalidRequest, Message: "session config: session is deleted"}
	}
	status := sess.ctrl.RuntimeStatus()
	if status.PendingPrompt {
		sess.mu.Unlock()
		return sessionConfigActiveWorkError("answer pending prompts before switching config")
	}
	if !sess.running && !status.Running && status.BackgroundJobs > 0 {
		sess.mu.Unlock()
		return sessionConfigActiveWorkError("stop background jobs before switching config")
	}
	if sess.running || status.Running || sess.maintenanceDone != nil {
		for _, delta := range deltas {
			sess.pendingConfig = mergePendingConfig(sess.pendingConfig, delta)
		}
		sess.mu.Unlock()
		sess.sink.send(configOptionUpdate{SessionUpdate: "config_option_update", ConfigOptions: cfgState.ConfigOptions})
		return nil
	}
	// Claim this rebuild's axes from the queue in the same critical section
	// that raises maintenanceDone below: begin must never observe an idle
	// session between the two. Axes queued by other requests stay queued and
	// are drained by the post-maintenance apply.
	sess.pendingConfig = removePendingAxes(sess.pendingConfig, deltas)

	cur := sess.ctrl
	sink := sess.sink
	mcpServers := clonePluginSpecs(sess.mcpServers)
	cwd := sess.cwd
	modeID := normalizeACPCollaborationMode(sess.modeID)
	goalDraftMode := sess.goalDraftMode
	toolApprovalMode := normalizeACPToolApprovalMode(sess.toolApprovalMode)
	if strings.TrimSpace(cfgState.RuntimeProfile) == "" {
		cfgState.RuntimeProfile = sess.runtimeProfile
	}
	maintenanceDone := make(chan struct{})
	sess.maintenanceDone = maintenanceDone
	*didMaintenance = true
	sess.mu.Unlock()
	defer func() {
		sess.finishMaintenance(maintenanceDone)
	}()

	if err := snapshotACPController(sess, cur); err != nil {
		return &RPCError{Code: ErrInternal, Message: "session config: snapshot before switch: " + err.Error()}
	}
	// Capture the adopt path and history only after Snapshot: a snapshot
	// conflict can retarget cur to a recovery branch (or adopt the newer disk
	// transcript), and a pre-snapshot capture would bind the rebuilt controller
	// back to the original file, re-conflicting on every later save. When that
	// recovery fired, sessionRecoveredHandler already moved sess.transcript
	// and the session lease to the recovery file, so prevPath, the session
	// bookkeeping, and the controller agree on one path here.
	// SessionPath is controller-locked, so reading it off sess.mu is safe.
	prevPath := cur.SessionPath()
	carried := cur.History()
	carriedGoal := ""
	if cur.GoalStatus() == control.GoalStatusRunning {
		carriedGoal = cur.Goal()
	}

	rebuildParams := SessionParams{
		Cwd:            cwd,
		MCPServers:     mcpServers,
		Sink:           sink,
		Model:          cfgState.Model,
		EffortOverride: cloneStringPtr(cfgState.EffortOverride),
		RuntimeProfile: cfgState.RuntimeProfile,
	}
	s.bindSessionPathHandlers(sess.id, &rebuildParams)
	// The rebuilt controller must keep the client-capability wiring (fs
	// overlay, host terminal) a model/effort switch would otherwise drop.
	s.bindClientIO(&rebuildParams, sess.id)
	newCtrl, err := s.factory.NewSession(ctx, rebuildParams)
	if err != nil {
		return &RPCError{Code: ErrInternal, Message: "session config: " + err.Error()}
	}
	newCtrl.EnableInteractiveApproval()
	runtimeState, err := s.sessionRuntimeState(ctx, SessionRuntimeStateParams{
		Cwd: cwd, Model: cfgState.Model, RuntimeProfile: cfgState.RuntimeProfile,
	})
	if err != nil {
		newCtrl.ReleaseResources()
		return &RPCError{Code: ErrInternal, Message: "session config: runtime state: " + err.Error()}
	}
	// The freshly built controller's own leading system message carries the
	// target profile's contract (see boot/token_profile.go); AdoptHistory below
	// replaces the whole history with carried, so splice that message in first
	// or the model keeps seeing the outgoing profile's contract after every
	// switch.
	if fresh := newCtrl.History(); len(fresh) > 0 && fresh[0].Role == provider.RoleSystem {
		if len(carried) > 0 && carried[0].Role == provider.RoleSystem {
			carried[0] = fresh[0]
		} else {
			carried = append([]provider.Message{fresh[0]}, carried...)
		}
	}
	newCtrl.AdoptHistory(carried, prevPath)
	// Re-apply all three independent session axes. A controller rebuild must not
	// turn Plan into tool approval, drop a running Goal, or reset Ask/Auto/Yolo.
	newCtrl.SetToolApprovalMode(toolApprovalMode)
	switch modeID {
	case sessionModePlan:
		newCtrl.SetPlanMode(true)
	case sessionModeGoal:
		newCtrl.SetPlanMode(false)
		if carriedGoal != "" {
			newCtrl.SetGoal(carriedGoal)
		}
	default:
		newCtrl.SetPlanMode(false)
	}
	// InheritLifecycleFrom wires two concrete controllers' turn/hook state; it's a
	// construction concern, not part of the driving port. cur is always the
	// *control.Controller the factory built for this session, so this is safe.
	if prev, ok := cur.(*control.Controller); ok {
		newCtrl.InheritLifecycleFrom(prev)
		// A rebuild must not force the user to re-approve tools already granted
		// for this session, or re-trust Plan-mode read-only commands already
		// trusted this session.
		newCtrl.RestoreSessionAuthorizations(prev.SessionAuthorizations())
	}
	// Persist before publishing the replacement. If this fails, the outgoing
	// controller and transcript still agree and remain fully usable; publishing
	// first would report a successful switch whose refreshed profile contract
	// disappears on restart. AdoptHistory preserves the loaded CAS baseline, so
	// this compatible leading-system rewrite is safe to snapshot here.
	if err := s.prepareACPReplacementAuthority(sess, newCtrl, cur, prevPath, "snapshot after switch"); err != nil {
		newCtrl.ReleaseResources()
		return &RPCError{Code: ErrInternal, Message: "session config: " + err.Error()}
	}

	sess.mu.Lock()
	if sess.deleted {
		sess.mu.Unlock()
		newCtrl.ReleaseResources()
		return &RPCError{Code: ErrInvalidRequest, Message: "session config: session is deleted"}
	}
	if sess.ctrl != cur {
		sess.mu.Unlock()
		newCtrl.ReleaseResources()
		return sessionConfigActiveWorkError("session changed while switching config; retry")
	}
	sess.ctrl = newCtrl
	sess.model = cfgState.Model
	sess.effortOverride = cloneStringPtr(cfgState.EffortOverride)
	sess.runtimeProfile = cfgState.RuntimeProfile
	sess.toolApprovalMode = toolApprovalMode
	sess.runtimeState = runtimeState
	sess.modeID = modeID
	sess.goalDraftMode = goalDraftMode
	if sess.transcript != "" && sessionFileExists(sess.transcript) {
		_ = saveACPMeta(sess.transcript, sess.metaLocked())
	}
	sess.mu.Unlock()
	sink.bindControllerPrompts(newCtrl, rebuildParams.MCPInteractions)

	cur.ReleaseResources()
	s.sendAvailableCommands(sess)
	sink.send(configOptionUpdate{SessionUpdate: "config_option_update", ConfigOptions: cfgState.ConfigOptions})
	return nil
}

func (s *service) applyPendingSessionConfig(ctx context.Context, sess *acpSession) error {
	var firstErr error
	for {
		if s.session(sess.id) != sess {
			return firstErr
		}
		// Claim the queue in the same serialization domain as explicit config
		// switches. Without this lock, a newer same-axis request can rebuild after
		// the clone below but before this apply starts, then the stale cloned delta
		// queues behind it and wins last instead of preserving request order.
		sess.stateChangeMu.Lock()
		didMaintenance := false
		sess.mu.Lock()
		if sess.deleted || len(sess.pendingConfig) == 0 {
			sess.mu.Unlock()
			sess.stateChangeMu.Unlock()
			return firstErr
		}
		deltas := clonePendingConfig(sess.pendingConfig)
		// Keep pendingConfig set while rebuilding: begin refuses new turns until
		// rebuildSession claims it together with raising maintenanceDone, so no
		// promptable instant is visible in between.
		sess.mu.Unlock()

		// Re-resolve against the session's current state rather than reusing
		// whatever baseline existed when each delta queued: another axis may have
		// finished rebuilding in the meantime, and replaying its old value here
		// would silently roll it back. All queued axes resolve into one state so a
		// single rebuild applies them together.
		cfgState, err := s.resolveSessionConfigDeltas(ctx, sess, deltas)
		if err != nil {
			sess.mu.Lock()
			if !sess.deleted && !sess.running && sess.maintenanceDone == nil {
				sess.pendingConfig = removePendingAxes(sess.pendingConfig, deltas)
			}
			sess.mu.Unlock()
			sess.stateChangeMu.Unlock()
			if firstErr != nil {
				s.reportPendingSessionConfigError(ctx, sess, err, "after failed maintenance")
				return firstErr
			}
			return err
		}

		err = s.rebuildSessionLocked(ctx, sess, cfgState, deltas, &didMaintenance)
		if err != nil && !didMaintenance {
			// Once this attempt failed nothing in flight is left to retry the
			// claimed axes, and begin refuses new turns while any are queued — drop
			// them so the session stays promptable. Once maintenance started, those
			// axes were already removed; anything queued now is a newer request and
			// must survive this failure.
			sess.mu.Lock()
			if !sess.deleted && !sess.running && sess.maintenanceDone == nil {
				sess.pendingConfig = removePendingAxes(sess.pendingConfig, deltas)
			}
			sess.mu.Unlock()
		}
		sess.stateChangeMu.Unlock()

		if err != nil {
			if firstErr == nil {
				firstErr = err
			} else {
				s.reportPendingSessionConfigError(ctx, sess, err, "after failed maintenance")
			}
			if !didMaintenance {
				return firstErr
			}
		}
		if !didMaintenance {
			return firstErr
		}
		// Requests can queue while NewSession/Snapshot runs. Iterate even when this
		// rebuild failed so their already-successful RPCs cannot leave the session
		// blocked. A loop keeps sustained config traffic from growing the call stack.
	}
}

func (s *service) reportPendingSessionConfigError(ctx context.Context, sess *acpSession, err error, when string) {
	if err == nil || sess == nil || sess.sink == nil {
		return
	}
	sess.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "session config switch failed " + when + ": " + err.Error()})
	// A queued request already announced its desired config to the client. Every
	// apply failure leaves the outgoing controller/config active, so always send
	// the live state back; otherwise snapshot/build/resolve failures leave the
	// picker claiming a switch that never happened.
	if current, stateErr := s.configStateForSession(ctx, sess); stateErr == nil {
		sess.sink.send(configOptionUpdate{SessionUpdate: "config_option_update", ConfigOptions: current.ConfigOptions})
	}
}

type activeSessionConfigWorkError struct {
	*RPCError
}

func (e *activeSessionConfigWorkError) Unwrap() error {
	return e.RPCError
}

func sessionConfigActiveWorkError(message string) error {
	return &activeSessionConfigWorkError{
		RPCError: &RPCError{Code: ErrInvalidRequest, Message: "session config: " + message},
	}
}

// sessionClose releases an active session. Unknown sessions are accepted as a
// no-op because closing is an idempotent resource cleanup request.
func (s *service) sessionClose(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionCloseParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/close: " + err.Error()}
	}
	if err := validateSessionID("session/close", p.SessionID); err != nil {
		return nil, err
	}
	if sess := s.takeSession(p.SessionID); sess != nil {
		sess.abortAndWait()
		sess.ctrl.Close()
		sess.releaseSessionLease()
	}
	return SessionCloseResult{}, nil
}

// sessionList returns ACP sessions known to this process or persisted as ACP
// sidecars. It deliberately ignores ordinary CLI timestamp sessions.
func (s *service) sessionList(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionListParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, &RPCError{Code: ErrInvalidParams, Message: "session/list: " + err.Error()}
		}
	}
	filterCwd := strings.TrimSpace(p.Cwd)
	if filterCwd != "" && !filepath.IsAbs(filterCwd) {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/list: cwd must be an absolute path"}
	}
	if strings.TrimSpace(p.Cursor) != "" {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/list: unsupported cursor"}
	}

	byID := map[string]SessionInfo{}
	if dir := s.sessionDir(); dir != "" {
		metas, err := listACPMetas(dir)
		if err != nil {
			return nil, &RPCError{Code: ErrInternal, Message: "session/list: " + err.Error()}
		}
		// A recovered session has two sidecars claiming the same id: the
		// active recovery transcript's own meta and the id-keyed redirect.
		// Reduce to one representative per id before filtering, so the entry
		// shown never carries the stale pre-recovery title/timestamps.
		best := map[string]acpSessionMeta{}
		for _, meta := range metas {
			cur, ok := best[meta.SessionID]
			if !ok || listMetaBeats(meta, cur) {
				best[meta.SessionID] = meta
			}
		}
		for _, meta := range best {
			info := meta.info(nil)
			if sessionInfoMatchesCwd(info, filterCwd) {
				byID[info.SessionID] = info
			}
		}
	}
	for _, sess := range s.liveSessions() {
		info := sess.info()
		if sessionInfoMatchesCwd(info, filterCwd) {
			byID[info.SessionID] = info
		}
	}

	sessions := make([]SessionInfo, 0, len(byID))
	for _, info := range byID {
		sessions = append(sessions, info)
	}
	sort.Slice(sessions, func(i, j int) bool {
		ti := parseSessionUpdatedAt(sessions[i].UpdatedAt)
		tj := parseSessionUpdatedAt(sessions[j].UpdatedAt)
		if ti.Equal(tj) {
			return sessions[i].SessionID < sessions[j].SessionID
		}
		return ti.After(tj)
	})
	return SessionListResult{Sessions: sessions}, nil
}

// sessionDelete removes a session from future list results. Deleting a missing
// session succeeds silently, matching ACP's idempotent delete guidance.
func (s *service) sessionDelete(_ context.Context, raw json.RawMessage) (any, error) {
	var p SessionDeleteParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &RPCError{Code: ErrInvalidParams, Message: "session/delete: " + err.Error()}
	}
	if err := validateSessionID("session/delete", p.SessionID); err != nil {
		return nil, err
	}

	path := ""
	var destroy control.SessionDestroyHandle
	var delayed bool
	if sess := s.takeSession(p.SessionID); sess != nil {
		sess.deleteAndWait()
		// The session is going away; drop its lease before removing files so
		// the lease sidecars retire with the release (they are not in
		// SessionSidecarFiles and would otherwise linger).
		sess.releaseSessionLease()
		path = sess.transcript
		destroy = sess.ctrl.BeginDestroySession(path)
		if result := destroy.Wait(); result.HasTimedOut() {
			if err := agent.MarkCleanupPending(path, "delete"); err != nil {
				go delayedDeleteSessionFiles(path, destroy)
				sess.ctrl.CloseAfterDestroy()
				return nil, &RPCError{Code: ErrInternal, Message: "session/delete: " + err.Error()}
			}
			go delayedDeleteSessionFiles(path, destroy)
			delayed = true
		}
		sess.ctrl.CloseAfterDestroy()
	}
	if path == "" {
		if dir := s.sessionDir(); dir != "" {
			path = resolveTranscriptPath(dir, p.SessionID)
		}
	}
	if path != "" && !delayed {
		if err := deleteSessionFiles(path); err != nil {
			return nil, &RPCError{Code: ErrInternal, Message: "session/delete: " + err.Error()}
		}
		if destroy.Finish != nil {
			destroy.Finish()
		}
	}
	// A recovered session lives in two files: the recovery transcript (deleted
	// above) and the id-keyed original holding the redirect. Remove the twin
	// too, or it resurfaces in session/list as a ghost that delete-by-id can
	// never reach again.
	if dir := s.sessionDir(); dir != "" {
		if idPath := transcriptPath(dir, p.SessionID); idPath != path {
			if err := deleteSessionFiles(idPath); err != nil {
				return nil, &RPCError{Code: ErrInternal, Message: "session/delete: " + err.Error()}
			}
		}
	}
	return SessionDeleteResult{}, nil
}

// sessionCancel aborts a session's in-flight turn, if any. It is a notification:
// no reply, and an unknown session is silently ignored.
func (s *service) sessionCancel(_ context.Context, raw json.RawMessage) {
	var p SessionCancelParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	if sess := s.session(p.SessionID); sess != nil {
		sess.abort()
	}
}

func (s *service) session(id string) *acpSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *service) takeSession(id string) *acpSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[id]
	delete(s.sessions, id)
	return sess
}

func (s *service) liveSessions() []*acpSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*acpSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess)
	}
	return out
}

func (s *service) sessionDir() string {
	if p, ok := s.factory.(SessionDirProvider); ok {
		if dir := strings.TrimSpace(p.SessionDir()); dir != "" {
			return dir
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if dir := sess.currentCtrl().SessionDir(); dir != "" {
			return dir
		}
	}
	return ""
}

func (s *service) sessionConfigState(ctx context.Context, p SessionConfigStateParams) (SessionConfigState, error) {
	if provider, ok := s.factory.(SessionConfigStateProvider); ok {
		return provider.SessionConfigState(ctx, p)
	}
	return SessionConfigState{}, nil
}

func (s *service) configStateForSession(ctx context.Context, sess *acpSession) (SessionConfigState, error) {
	state, err := s.sessionConfigState(ctx, sess.configStateParams())
	if err != nil {
		return SessionConfigState{}, err
	}
	// Fold in the live controller's extension catalog so plugin/... models
	// are discoverable on every config-state read, not only when current.
	state = enrichStateWithExtensionModels(state, sess.currentCtrl().ProviderCatalog())
	state = withToolApprovalConfig(state, sess.currentToolApprovalMode())
	return withQualityFloorConfig(state, sess.currentQualityFloor()), nil
}

func (s *acpSession) configStateParams() SessionConfigStateParams {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionConfigStateParams{
		Cwd:            s.cwd,
		Model:          s.model,
		EffortOverride: cloneStringPtr(s.effortOverride),
		RuntimeProfile: s.runtimeProfile,
	}
}

func (s *acpSession) currentToolApprovalMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return normalizeACPToolApprovalMode(s.toolApprovalMode)
}

func normalizeACPToolApprovalMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case control.ToolApprovalAuto:
		return control.ToolApprovalAuto
	case control.ToolApprovalYolo:
		return control.ToolApprovalYolo
	default:
		return control.ToolApprovalAsk
	}
}

func normalizeACPCollaborationMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case sessionModePlan:
		return sessionModePlan
	case sessionModeGoal:
		return sessionModeGoal
	default:
		return sessionModeNormal
	}
}

func withToolApprovalConfig(state SessionConfigState, mode string) SessionConfigState {
	mode = normalizeACPToolApprovalMode(mode)
	option := SessionConfigOption{
		ID:           "tool_approval",
		Name:         "Tool Approval",
		Category:     "tool_approval",
		Type:         "select",
		CurrentValue: mode,
		Options: []SessionConfigSelectOption{
			{Value: control.ToolApprovalAsk, Name: "Ask", Description: "Ask before permission-gated tool calls"},
			{Value: control.ToolApprovalAuto, Name: "Auto", Description: "Follow configured permission rules without fallback prompts"},
			{Value: control.ToolApprovalYolo, Name: "Yolo", Description: "Approve tool calls except protected decisions"},
		},
	}
	for i := range state.ConfigOptions {
		if normalizeConfigID(state.ConfigOptions[i].ID) == option.ID {
			state.ConfigOptions[i] = option
			return state
		}
	}
	state.ConfigOptions = append(state.ConfigOptions, option)
	return state
}

func findConfigOption(options []SessionConfigOption, id string) (SessionConfigOption, bool) {
	id = normalizeConfigID(id)
	for _, opt := range options {
		if normalizeConfigID(opt.ID) == id {
			return opt, true
		}
	}
	return SessionConfigOption{}, false
}

// validateDeprecatedModeValue accepts the historical execution-mode vocabulary
// so one-version-old clients get a precise error for garbage values while
// well-formed values succeed as no-ops.
func validateDeprecatedModeValue(value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "light", "economy", "eco", "lite", "save", "saving", "low", "minimal",
		"standard", "normal", "balanced", "full", "delivery", "deliver", "quality":
		return nil
	}
	return fmt.Errorf("invalid value %q for quality floor (accepted: standard, delivery; legacy light folds to standard)", value)
}

func normalizeConfigID(id string) string {
	switch strings.TrimSpace(id) {
	case "models":
		return "model"
	case "reasoning_effort", "thought_level":
		return "effort"
	case "profile", "runtime_profile", "token_mode":
		return "work_mode"
	case "approval", "approval_mode", "tool_approval_mode":
		return "tool_approval"
	default:
		return strings.TrimSpace(id)
	}
}

func configOptionHasValue(option SessionConfigOption, value string) bool {
	for _, opt := range option.Options {
		if opt.Value == value {
			return true
		}
	}
	return false
}

func configOptionCategory(option SessionConfigOption) string {
	if option.Category != "" {
		return option.Category
	}
	switch normalizeConfigID(option.ID) {
	case "model":
		return "model"
	case "effort":
		return "thought_level"
	case "work_mode":
		return "work_mode"
	case "tool_approval":
		return "tool_approval"
	default:
		return ""
	}
}

func cloneStringPtr(p *string) *string {
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

func clonePluginSpecs(in []plugin.Spec) []plugin.Spec {
	if len(in) == 0 {
		return nil
	}
	out := make([]plugin.Spec, len(in))
	copy(out, in)
	return out
}

func (s *service) resolveSessionCwd(cwd, sessionID string) (string, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd != "" {
		if !filepath.IsAbs(cwd) {
			return "", fmt.Errorf("cwd must be an absolute path")
		}
		return filepath.Clean(cwd), nil
	}
	if sessionID != "" {
		if meta, ok := s.loadMeta(sessionID); ok && meta.Cwd != "" {
			if !filepath.IsAbs(meta.Cwd) {
				return "", fmt.Errorf("stored cwd must be an absolute path")
			}
			return filepath.Clean(meta.Cwd), nil
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve cwd: %w", err)
	}
	return wd, nil
}

func (s *service) loadMeta(id string) (acpSessionMeta, bool) {
	dir := s.sessionDir()
	if dir == "" {
		return acpSessionMeta{}, false
	}
	meta, ok, err := loadACPMeta(resolveTranscriptPath(dir, id))
	if err != nil {
		return acpSessionMeta{}, false
	}
	return meta, ok
}

// closeAll tears down every open session (aborting any in-flight turn and
// stopping its MCP subprocesses) when the connection ends.
func (s *service) closeAll() {
	s.mu.Lock()
	sessions := s.sessions
	s.sessions = make(map[string]*acpSession)
	s.mu.Unlock()
	for _, sess := range sessions {
		sess.abortAndWait()
		sess.currentCtrl().Close()
		sess.releaseSessionLease()
	}
}

func (s *acpSession) persistAfterTurn(prompt string) {
	s.mu.Lock()
	if s.deleted {
		s.mu.Unlock()
		return
	}
	ctrl := s.ctrl
	s.mu.Unlock()

	_ = snapshotACPController(s, ctrl)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleted || s.ctrl != ctrl {
		return
	}
	if s.title == "" {
		s.title = previewTitle(prompt)
	}
	s.updatedAt = time.Now().UTC()
	if s.createdAt.IsZero() {
		s.createdAt = s.updatedAt
	}
	if s.transcript != "" && sessionFileExists(s.transcript) {
		_ = saveACPMeta(s.transcript, s.metaLocked())
	}
}

func (s *acpSession) meta() acpSessionMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metaLocked()
}

func (s *acpSession) metaLocked() acpSessionMeta {
	return acpSessionMeta{
		SessionID:         s.id,
		Cwd:               s.cwd,
		Model:             s.model,
		EffortOverride:    cloneStringPtr(s.effortOverride),
		RuntimeProfile:    s.runtimeProfile,
		ToolApprovalMode:  normalizeACPToolApprovalMode(s.toolApprovalMode),
		CollaborationMode: normalizeACPCollaborationMode(s.modeID),
		Title:             s.title,
		CreatedAt:         s.createdAt,
		UpdatedAt:         s.updatedAt,
		Status:            s.status.persisted(),
	}
}

func (s *acpSession) info() SessionInfo {
	meta := s.meta()
	ctrl := s.currentCtrl()
	extra := map[string]any{}
	if n := len(ctrl.History()); n > 0 {
		extra["messageCount"] = n
	}
	if len(extra) == 0 {
		extra = nil
	}
	return meta.info(extra)
}

func (s *service) sendAvailableCommands(sess *acpSession) {
	if sess == nil {
		return
	}
	ctrl := sess.currentCtrl()
	if ctrl == nil {
		return
	}
	cmds := availableCommandsFor(ctrl)
	if len(cmds) == 0 {
		return
	}
	sess.sink.send(availableCommandsUpdate{
		SessionUpdate:     "available_commands_update",
		AvailableCommands: cmds,
	})
}

func (s *service) resolveSlashPrompt(ctx context.Context, sess *acpSession, text string) string {
	line := strings.TrimSpace(text)
	if sess == nil || !strings.HasPrefix(line, "/") {
		return text
	}
	ctrl := sess.currentCtrl()
	if ctrl == nil {
		return text
	}
	if sent, ok := ctrl.CustomCommand(line); ok {
		return sent
	}
	if sent, ok := ctrl.RunSkill(line); ok {
		return sent
	}
	if sent, ok, err := ctrl.MCPPrompt(ctx, line); err == nil && ok {
		return sent
	}
	if sent, ok := invokeExtensionAction(ctx, ctrl, line); ok {
		return sent
	}
	return text
}

// invokeExtensionAction resolves a "/<plugin>:<action> args…" line against the
// handshake-declared extension actions and invokes it — the last resolution
// step in resolveSlashPrompt, after custom commands, skills, and MCP prompts.
// The extension's result message becomes the prompt text. A parse miss, an
// undeclared action, an invocation error, or an empty result all leave the
// line untouched (ok=false), matching how unknown slash commands fall through.
func invokeExtensionAction(ctx context.Context, ctrl acpController, line string) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", false
	}
	pluginID, actionID, ok := uihub.ParseSlashName(fields[0])
	if !ok {
		return "", false
	}
	declared := false
	for _, action := range ctrl.ExtensionActions() {
		if action.PluginID == pluginID && action.ActionID == actionID {
			declared = true
			break
		}
	}
	if !declared {
		return "", false
	}
	message, err := ctrl.InvokeExtensionAction(ctx, fields[0], control.ParseExtensionActionArgs(fields[1:]))
	if err != nil || strings.TrimSpace(message) == "" {
		return "", false
	}
	return message, true
}

type acpSessionMeta struct {
	SessionID         string                    `json:"sessionId"`
	Cwd               string                    `json:"cwd"`
	Model             string                    `json:"model,omitempty"`
	EffortOverride    *string                   `json:"effortOverride,omitempty"`
	RuntimeProfile    string                    `json:"runtimeProfile,omitempty"`
	ToolApprovalMode  string                    `json:"toolApprovalMode,omitempty"`
	CollaborationMode string                    `json:"collaborationMode,omitempty"`
	Title             string                    `json:"title,omitempty"`
	CreatedAt         time.Time                 `json:"createdAt"`
	UpdatedAt         time.Time                 `json:"updatedAt"`
	Status            *persistedStatusTelemetry `json:"status,omitempty"`
	// ActiveTranscript, when set on the id-keyed sidecar, is the basename of
	// the transcript this session currently lives in: a snapshot recovery
	// moved the live session onto a recovery branch and left this redirect
	// behind so restart-time lookups (resolveTranscriptPath) follow the
	// session instead of reopening the pre-recovery file.
	ActiveTranscript string `json:"activeTranscript,omitempty"`
}

func (m acpSessionMeta) info(extra map[string]any) SessionInfo {
	updatedAt := ""
	if !m.UpdatedAt.IsZero() {
		updatedAt = m.UpdatedAt.Format(time.RFC3339Nano)
	}
	return SessionInfo{
		SessionID: m.SessionID,
		Cwd:       m.Cwd,
		Title:     m.Title,
		UpdatedAt: updatedAt,
		Meta:      extra,
	}
}

func metadataForLoadedSession(path, id, cwd string, history []provider.Message) acpSessionMeta {
	now := time.Now().UTC()
	meta, ok, err := loadACPMeta(path)
	if err != nil || !ok {
		meta = acpSessionMeta{
			SessionID: id,
			Cwd:       cwd,
			Title:     titleFromHistory(history),
			CreatedAt: now,
			UpdatedAt: now,
		}
		if info, statErr := os.Stat(path); statErr == nil {
			meta.CreatedAt = info.ModTime().UTC()
			meta.UpdatedAt = info.ModTime().UTC()
		}
	}
	if meta.SessionID == "" {
		meta.SessionID = id
	}
	if cwd != "" {
		meta.Cwd = cwd
	}
	if meta.Title == "" {
		meta.Title = titleFromHistory(history)
	}
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	if meta.UpdatedAt.IsZero() {
		meta.UpdatedAt = meta.CreatedAt
	}
	return meta
}

func loadACPMeta(sessionPath string) (acpSessionMeta, bool, error) {
	path := acpMetaPath(sessionPath)
	if path == "" {
		return acpSessionMeta{}, false, nil
	}
	b, err := fileencoding.ReadFileUTF8(path)
	if err != nil {
		if os.IsNotExist(err) {
			return acpSessionMeta{}, false, nil
		}
		return acpSessionMeta{}, false, err
	}
	var meta acpSessionMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return acpSessionMeta{}, false, fmt.Errorf("decode ACP session metadata %s: %w", path, err)
	}
	return meta, true, nil
}

func saveACPMeta(sessionPath string, meta acpSessionMeta) error {
	path := acpMetaPath(sessionPath)
	if path == "" {
		return nil
	}
	now := time.Now().UTC()
	if meta.SessionID == "" {
		meta.SessionID = sessionIDFromTranscript(sessionPath)
	}
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	if meta.UpdatedAt.IsZero() {
		meta.UpdatedAt = meta.CreatedAt
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".acp-session.*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return fileutil.ReplaceFile(tmpPath, path)
}

func listACPMetas(dir string) ([]acpSessionMeta, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := []acpSessionMeta{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".acp.json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".acp.json")
		sessionPath := transcriptPath(dir, id)
		if agent.IsCleanupPending(sessionPath) {
			continue
		}
		if !sessionFileExists(sessionPath) {
			continue
		}
		meta, ok, err := loadACPMeta(sessionPath)
		if err != nil || !ok {
			continue
		}
		if meta.SessionID == "" {
			meta.SessionID = id
		}
		if meta.Cwd == "" {
			continue
		}
		out = append(out, meta)
	}
	return out, nil
}

func sessionFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func acpMetaPath(sessionPath string) string {
	if sessionPath == "" {
		return ""
	}
	return strings.TrimSuffix(sessionPath, filepath.Ext(sessionPath)) + ".acp.json"
}

func sessionIDFromTranscript(path string) string {
	base := filepath.Base(path)
	if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}

// listMetaBeats reports whether a should represent its session id in
// session/list over b. A meta without an ActiveTranscript redirect is the
// session's live transcript and always beats a redirect sidecar; between two
// of the same kind the later UpdatedAt wins.
func listMetaBeats(a, b acpSessionMeta) bool {
	aRedirect := strings.TrimSpace(a.ActiveTranscript) != ""
	bRedirect := strings.TrimSpace(b.ActiveTranscript) != ""
	if aRedirect != bRedirect {
		return !aRedirect
	}
	return a.UpdatedAt.After(b.UpdatedAt)
}

func sessionInfoMatchesCwd(info SessionInfo, filter string) bool {
	if filter == "" {
		return true
	}
	return filepath.Clean(info.Cwd) == filepath.Clean(filter)
}

func titleFromHistory(history []provider.Message) string {
	for _, m := range history {
		if agent.IsUserAuthoredTurnMessage(m) {
			if title := previewTitle(m.Content); title != "" {
				return title
			}
		}
	}
	return ""
}

func previewTitle(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len([]rune(text)) <= 80 {
		return text
	}
	runes := []rune(text)
	return string(runes[:77]) + "..."
}

func validateSessionID(method, id string) error {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return &RPCError{Code: ErrInvalidParams, Message: method + ": missing sessionId"}
	}
	if trimmed != id || trimmed == "." || trimmed == ".." || !isSafeSessionID(trimmed) {
		return &RPCError{Code: ErrInvalidParams, Message: method + ": invalid sessionId"}
	}
	return nil
}

func isSafeSessionID(id string) bool {
	for _, r := range id {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func parseSessionUpdatedAt(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func deleteSessionFiles(sessionPath string) error {
	paths := []string{
		sessionPath,
		acpMetaPath(sessionPath),
	}
	paths = append(paths, store.SessionSidecarFiles(sessionPath)...)
	for _, path := range paths {
		if path == "" {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if dir := checkpointPath(sessionPath); dir != "" {
		if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := agent.DeleteSubagentsByParent(filepath.Dir(sessionPath), agent.BranchID(sessionPath)); err != nil {
		return err
	}
	if err := jobs.RemoveArtifacts(sessionPath); err != nil {
		return err
	}
	return agent.ClearCleanupPending(sessionPath)
}

// ReconcileCleanupPending retries delayed ACP session cleanup left by a previous
// process, including ACP's own metadata sidecar.
func ReconcileCleanupPending(dir string) error {
	return agent.ReconcileCleanupPending(dir, func(item agent.CleanupPendingInfo) error {
		return deleteSessionFiles(item.SessionPath)
	})
}

func delayedDeleteSessionFiles(sessionPath string, destroy control.SessionDestroyHandle) {
	if destroy.WaitAll != nil {
		destroy.WaitAll()
	}
	if err := deleteSessionFiles(sessionPath); err != nil {
		slog.Warn("acp: delayed session delete failed", "path", sessionPath, "err", err)
	}
	if destroy.Finish != nil {
		destroy.Finish()
	}
}

func checkpointPath(sessionPath string) string {
	return store.SessionCheckpointDir(sessionPath)
}

// mcpSpecs converts ACP MCP server declarations to plugin.Spec.
func mcpSpecs(in []MCPServerSpec, cwd string) ([]plugin.Spec, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]plugin.Spec, 0, len(in))
	for _, m := range in {
		typ := strings.ToLower(strings.TrimSpace(m.Type))
		if typ == "" {
			typ = "stdio"
		}
		if strings.TrimSpace(m.Name) == "" {
			return nil, fmt.Errorf("MCP server name is required")
		}
		switch typ {
		case "stdio":
			if strings.TrimSpace(m.Command) == "" {
				return nil, fmt.Errorf("MCP server %q command is required", m.Name)
			}
		case "http", "streamable-http", "streamable_http", "sse":
			if strings.TrimSpace(m.URL) == "" {
				return nil, fmt.Errorf("MCP server %q url is required", m.Name)
			}
			if typ != "sse" {
				typ = "http"
			}
		default:
			return nil, fmt.Errorf("MCP server %q uses unsupported transport %q", m.Name, m.Type)
		}
		out = append(out, plugin.Spec{
			Name:          strings.TrimSpace(m.Name),
			Type:          typ,
			Command:       strings.TrimSpace(m.Command),
			Args:          append([]string(nil), m.Args...),
			Env:           mapString(m.Env),
			URL:           strings.TrimSpace(m.URL),
			Headers:       mapString(m.Headers),
			Dir:           cwd,
			WorkspaceRoot: cwd,
		})
	}
	return out, nil
}

func mapString(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

// newSessionID returns a random RFC 4122 v4 UUID string used to address a session.
func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
