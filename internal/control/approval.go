package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/i18n"
	"reasonix/internal/permission"
)

// Approve answers a pending ApprovalRequest by ID. It remains the compatibility
// bridge for clients that do not yet call the scope-aware resolver directly.
func (c *Controller) Approve(id string, allow, session, persist bool) {
	_ = c.approveChecked(id, allow, session, persist)
}

func (c *Controller) approveChecked(id string, allow, session, persist bool) error {
	if pending := c.approval.peek(id); pending.reply != nil && pending.kind == writeAccessKind {
		return c.ResolveApproval(id, allow, scopeFromApprove(allow, session, persist))
	}
	c.mu.Lock()
	gate := c.recoveryGate
	c.mu.Unlock()
	if gate != nil && gate.HasApproval(id) {
		action := agent.RecoveryActionRevise
		if allow {
			action = agent.RecoveryActionContinue
		}
		return c.ResolveRecovery(id, action, "")
	}
	pending, ok, err := c.approval.resolveAfter(id, func(p pendingApproval) error {
		return c.emitTurnEventChecked(event.Event{Kind: event.PromptAnswered, ItemID: id, Status: event.TurnInProgress})
	})
	if err != nil {
		return err
	}
	if !ok || pending.reply == nil {
		return nil
	}
	c.promptOwner.Remove(id)
	outcome := "deny"
	if pending.tool == planApprovalTool {
		outcome = string(PlanDecisionRevisePlan)
		if allow {
			outcome = string(PlanDecisionStartExecution)
		}
	} else if allow {
		switch {
		case persist:
			outcome = "allow_persistent"
		case session:
			outcome = "allow_session"
		default:
			outcome = "allow_once"
		}
	}
	c.recordDecisionReceipt(pending, outcome)
	pending.reply <- approvalReply{allow: allow, session: session, persist: persist}
	return nil
}

// approvalManager owns the approval/ask prompt bookkeeping and the runtime
// approval posture, behind its own locks and off the controller's c.mu. It is a
// strict leaf: its methods only touch its own state and never call back into the
// Controller. The Controller keeps the I/O orchestration (emitting events,
// firing hooks, rebuilding the executor gate) that needs its other collaborators
// — approval, unlike the goal FSM, blocks on user input and has side effects, so
// only the bookkeeping is extracted, not the orchestration.
type approvalManager struct {
	// policy is the immutable base permission policy, captured at construction.
	// Used to decide whether a tool call would auto-approve under the writer
	// fallback (autoApprovalWouldAllowLocked); the Controller keeps its own copy
	// for building the executor gate.
	policy permission.Policy

	// mu guards the prompt maps and posture fields; every critical section under
	// it is short and non-blocking.
	mu                       sync.Mutex
	approvals                map[string]pendingApproval
	asks                     map[string]pendingAsk
	approvalResolutions      map[string]*promptResolution
	askResolutions           map[string]*promptResolution
	granted                  map[string]bool
	planModeReadOnlyCommands map[string]bool
	nextID                   int
	// toolApprovalMode is the runtime approval posture: "ask" prompts, "auto"
	// lets the policy auto-approve the writer fallback while preserving ask/deny
	// rules, and "yolo" skips ordinary tool prompts while deny rules and fresh
	// decisions remain enforced.
	toolApprovalMode string
	// approvalTimeout bounds how long requestApproval/Ask block on a user
	// decision. Zero means wait indefinitely (correct for an interactive
	// terminal); bot/headless frontends set it so a walked-away user can't wedge
	// the session forever (#4626, #4402). Write-once at construction.
	approvalTimeout time.Duration
	// planAutoApprove auto-allows the ordinary writer fallback while a
	// just-approved plan executes. Explicit ask/deny rules and fresh decisions
	// remain authoritative, matching Auto rather than YOLO semantics.
	planAutoApprove bool

	// promptMu serializes outstanding prompts so at most one user decision is in
	// flight. Held across the blocking wait, so it must never be taken by the
	// resolve paths (Approve/AnswerQuestion). sink.Emit also runs under it (Ask,
	// requestApproval): Sink implementations must not block and must not call
	// back into Ask or the tool-approval chain, or they deadlock the prompt.
	promptMu sync.Mutex
	// promptEmitMu serializes prompt registration and emission with an SSE
	// attach handoff. It is separate from promptMu because promptMu remains
	// held while waiting for the user's answer.
	promptEmitMu sync.Mutex

	// mcpInteractions holds pending MCP elicitations, guarded by mu and
	// grouped so the struct-state ratchet grows by one field.
	mcpInteractions mcpInteractionState
}

type promptResolution struct {
	done     chan struct{}
	joined   chan struct{}
	joinOnce sync.Once
	err      error
}

func newPromptResolution() *promptResolution {
	return &promptResolution{done: make(chan struct{}), joined: make(chan struct{})}
}

func (r *promptResolution) wait() error {
	if r == nil {
		return nil
	}
	r.joinOnce.Do(func() { close(r.joined) })
	<-r.done
	return r.err
}

func newApprovalManager(policy permission.Policy, mode string, timeout time.Duration) approvalManager {
	return approvalManager{
		policy:                   policy,
		approvals:                map[string]pendingApproval{},
		asks:                     map[string]pendingAsk{},
		approvalResolutions:      map[string]*promptResolution{},
		askResolutions:           map[string]*promptResolution{},
		granted:                  map[string]bool{},
		planModeReadOnlyCommands: map[string]bool{},
		toolApprovalMode:         mode,
		approvalTimeout:          timeout,
	}
}

// NewHeadlessPermissionGate builds the legacy bootstrap gate used before a
// frontend declares its approval posture. Interactive frontends replace it
// before running; callers that are actually headless must pass a non-empty mode
// through BuildHeadlessApprovalGate.
func NewHeadlessPermissionGate(policy permission.Policy) *freshHumanHeadlessGate {
	return &freshHumanHeadlessGate{gate: permission.NewGate(policy, nil)}
}

// BuildHeadlessApprovalGate constructs the non-interactive gate for a given
// approval mode, matching the contract ApplyHeadlessApprovalMode installs on a
// running controller's parent executor. boot uses this as the single
// construction point for every headless-only gate — the top-level executor,
// the `task`/`read_only_task` sub-agent, writer-capable skill sub-agents
// (run_skill/install_skill), and the planner runner — so all of them share the
// CLI-selected headless approval mode instead of only the parent executor
// getting it while the rest silently keep the mode-unaware default, which let
// a task sub-agent run a write an explicit ask
// rule was supposed to deny under auto.
func BuildHeadlessApprovalGate(policy permission.Policy, mode string) *freshHumanHeadlessGate {
	// An empty mode is the boot-time placeholder used by interactive frontends
	// before they install their real gate. Keep that compatibility path distinct
	// from an explicit headless Ask posture, which has nobody to approve it.
	if strings.TrimSpace(mode) == "" {
		return NewHeadlessPermissionGate(policy)
	}
	switch normalizeToolApprovalMode(mode) {
	case ToolApprovalYolo:
		policy.Mode = permission.Allow
		return &freshHumanHeadlessGate{gate: permission.NewGate(policy, nil), dynamicBashBypass: true}
	case ToolApprovalAuto:
		policy.Mode = permission.Allow
		return &freshHumanHeadlessGate{gate: permission.NewGate(policy, denyPermissionApprover{})}
	case ToolApprovalDontAsk:
		policy.Mode = permission.Deny
		return &freshHumanHeadlessGate{gate: permission.NewGate(policy, denyPermissionApprover{})}
	default:
		policy.Mode = permission.Ask
		return &freshHumanHeadlessGate{gate: permission.NewGate(policy, denyPermissionApprover{})}
	}
}

// SharedHeadlessGate is a mutable, concurrency-safe holder for the
// non-interactive gate that every headless-only sub-agent surface shares —
// `task`/`read_only_task`, writer-capable skill sub-agents, and the planner
// runner. Those surfaces capture their gate once at construction with no
// rebuild hook of their own, unlike the parent executor's gate (rebuilt in
// place via Agent.SetGate on every SetToolApprovalMode/
// ApplyHeadlessApprovalMode call). Every consumer holds this same pointer and
// reads through Check, so a runtime approval-mode switch (interactive
// Shift+Tab, or a headless --permission-mode passed at boot) only needs to
// call Update here to keep sub-agents on the same contract as the parent
// instead of silently pinning them to whatever mode was active when they were
// first constructed.
type SharedHeadlessGate struct {
	mu     sync.RWMutex
	policy permission.Policy
	gate   *freshHumanHeadlessGate
}

// NewSharedHeadlessGate builds a shared gate holder from the base policy and
// the initial approval mode (see BuildHeadlessApprovalGate for the mode
// contract).
func NewSharedHeadlessGate(policy permission.Policy, mode string) *SharedHeadlessGate {
	g := &SharedHeadlessGate{policy: policy}
	g.Update(mode)
	return g
}

// Update rebuilds the held gate for a new approval mode. Safe to call
// concurrently with Check (a turn may be mid-flight on another goroutine when
// the user switches modes).
func (g *SharedHeadlessGate) Update(mode string) {
	next := BuildHeadlessApprovalGate(g.policy, mode)
	g.mu.Lock()
	g.gate = next
	g.mu.Unlock()
}

func (g *SharedHeadlessGate) Check(ctx context.Context, toolName string, args json.RawMessage, readOnly bool) (bool, string, error) {
	g.mu.RLock()
	gate := g.gate
	g.mu.RUnlock()
	return gate.Check(ctx, toolName, args, readOnly)
}

func (g *SharedHeadlessGate) ExplicitlyDenies(toolName string, args json.RawMessage) bool {
	g.mu.RLock()
	gate := g.gate
	g.mu.RUnlock()
	return gate.ExplicitlyDenies(toolName, args)
}

type freshHumanHeadlessGate struct {
	gate                    *permission.Gate
	dynamicBashBypass       bool
	allowLowRiskFreshAction func(toolName string, args json.RawMessage) bool
}

func (g *freshHumanHeadlessGate) Check(ctx context.Context, toolName string, args json.RawMessage, readOnly bool) (bool, string, error) {
	if RequiresFreshHumanApprovalTool(toolName) {
		if !g.gate.ExplicitlyDenies(toolName, args) &&
			g.allowLowRiskFreshAction != nil &&
			g.allowLowRiskFreshAction(toolName, args) {
			return true, "", nil
		}
		return false, "this tool requires fresh human approval and cannot run in a non-interactive session. Use an interactive session or a user-initiated memory command.", nil
	}
	if strings.EqualFold(toolName, "bash") && permission.BashSubjectRequiresExplicitApproval(permission.Subject(args)) {
		if g.gate.Policy.Decide(toolName, readOnly, args) != permission.Allow && !g.dynamicBashBypass {
			return false, "this dynamic shell command requires human approval and cannot run in a non-interactive session. Inline interpreter code (python -c, node -e) is blocked because the host cannot audit it; write the code to a file with write_file and run that file instead (e.g. `python repro.py`), or use read_file/grep for inspection. The user can also switch to an interactive session or YOLO mode.", nil
		}
	}
	return g.gate.Check(ctx, toolName, args, readOnly)
}

func (g *freshHumanHeadlessGate) ExplicitlyDenies(toolName string, args json.RawMessage) bool {
	return g.gate.Policy.ExplicitlyDenies(toolName, args)
}

// preApproved reports whether a tool call can skip the prompt — either the
// posture bypasses it (YOLO / plan-execution window) or a session grant already
// covers the scope.
func (a *approvalManager) preApproved(tool, subject string, args json.RawMessage) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bypassAllowsLocked(tool, subject, args) || a.sessionGrantAllowsLocked(tool, subject)
}

// preApprovedForDecision reports whether a prompt can be skipped for a decision
// class. Fresh user decisions may reuse an explicit session grant, but they are
// never answered by YOLO/full-access or the approved-plan execution window.
func (a *approvalManager) preApprovedForDecision(tool, subject string, args json.RawMessage, fresh bool) bool {
	return a.preApprovedForDecisionOptions(tool, subject, args, fresh, false)
}

func (a *approvalManager) preApprovedForDecisionOptions(tool, subject string, args json.RawMessage, fresh, requireHuman bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if fresh {
		return a.sessionGrantAllowsLocked(tool, subject)
	}
	if requireHuman {
		return a.toolApprovalMode == ToolApprovalYolo || a.sessionGrantAllowsLocked(tool, subject)
	}
	return a.bypassAllowsLocked(tool, subject, args) || a.sessionGrantAllowsLocked(tool, subject)
}

func (a *approvalManager) preApprovedForRequiredHuman(tool, subject string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.toolApprovalMode == ToolApprovalYolo || a.sessionGrantAllowsLocked(tool, subject)
}

// register allocates an approval ID, records the pending prompt, and returns the
// reply channel the resolve path will signal.
func (a *approvalManager) register(tool, subject, reason string) (string, chan approvalReply) {
	return a.registerWithInput(tool, subject, reason, nil)
}

func (a *approvalManager) registerWithInput(tool, subject, reason string, rawInput json.RawMessage) (string, chan approvalReply) {
	return a.registerDecisionWithInput(tool, subject, reason, rawInput, false, false)
}

// registerDecision allocates an approval ID for either an ordinary tool
// permission or a fresh user decision. Fresh decisions are not auto-drained when
// the user switches to auto/yolo tool approval while the prompt is visible.
func (a *approvalManager) registerDecision(tool, subject, reason string, fresh, requireHuman bool) (string, chan approvalReply) {
	return a.registerDecisionWithInput(tool, subject, reason, nil, fresh, requireHuman)
}

func (a *approvalManager) registerDecisionWithInput(tool, subject, reason string, rawInput json.RawMessage, fresh, requireHuman bool) (string, chan approvalReply) {
	return a.registerDecisionKindWithInput(tool, subject, reason, rawInput, fresh, requireHuman, "", nil)
}

// registerDecisionKind is registerDecision with optional Kind/Recovery payload
// so Auto Guard cards survive ReplayPendingPrompts.
func (a *approvalManager) registerDecisionKind(tool, subject, reason string, fresh, requireHuman bool, kind string, rec *event.RecoveryApproval) (string, chan approvalReply) {
	return a.registerDecisionKindWithInput(tool, subject, reason, nil, fresh, requireHuman, kind, rec)
}

func (a *approvalManager) registerDecisionKindWithInput(tool, subject, reason string, rawInput json.RawMessage, fresh, requireHuman bool, kind string, rec *event.RecoveryApproval) (string, chan approvalReply) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextID++
	id := strconv.Itoa(a.nextID)
	reply := make(chan approvalReply, 1)
	autoDrain := false
	if !fresh && !requireHuman {
		autoDrain = a.autoApprovalWouldAllowLocked(tool, subject, rawInput)
	}
	a.approvals[id] = pendingApproval{
		id:   id,
		tool: tool, subject: subject, reason: reason, rawInput: append(json.RawMessage(nil), rawInput...), fresh: fresh, requireHuman: requireHuman,
		autoDrain: autoDrain, kind: kind, recovery: rec, reply: reply,
	}
	return id, reply
}

func (a *approvalManager) registerWriteAccess(tool, subject, reason string, rawInput json.RawMessage, payload *event.WriteAccessApproval) (string, chan approvalReply) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextID++
	id := strconv.Itoa(a.nextID)
	reply := make(chan approvalReply, 1)
	a.approvals[id] = pendingApproval{
		id: id, tool: tool, subject: subject, reason: reason,
		rawInput: append(json.RawMessage(nil), rawInput...),
		fresh:    true, requireHuman: true, kind: writeAccessKind,
		writeAccess: event.NormalizeWriteAccessApproval(payload), reply: reply,
	}
	return id, reply
}

func (a *approvalManager) peek(id string) pendingApproval {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.approvals[id]
}

// grantSession records a session-scoped grant so future calls in the same scope
// short-circuit.
func (a *approvalManager) grantSession(tool, subject string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.granted[permission.SessionGrantRuleForScope(tool, subject)] = true
}

func (a *approvalManager) planModeReadOnlyCommandTrusted(prefix string) bool {
	prefix = normalizePlanModeReadOnlyCommandPrefix(prefix)
	if prefix == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.planModeReadOnlyCommands[prefix]
}

func (a *approvalManager) grantPlanModeReadOnlyCommand(prefix string) {
	prefix = normalizePlanModeReadOnlyCommandPrefix(prefix)
	if prefix == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.planModeReadOnlyCommands[prefix] = true
}

// SessionAuthorizations is the same-session tool-grant and Plan-mode
// read-only command trust state a controller rebuild must carry forward; see
// Controller.SessionAuthorizations / RestoreSessionAuthorizations.
type SessionAuthorizations struct {
	Grants                   []string
	PlanModeReadOnlyCommands []string
	WriteRoots               []string
}

func (a *approvalManager) snapshotSessionAuthorizations() SessionAuthorizations {
	a.mu.Lock()
	defer a.mu.Unlock()
	auth := SessionAuthorizations{
		Grants:                   make([]string, 0, len(a.granted)),
		PlanModeReadOnlyCommands: make([]string, 0, len(a.planModeReadOnlyCommands)),
	}
	for rule := range a.granted {
		auth.Grants = append(auth.Grants, rule)
	}
	for prefix := range a.planModeReadOnlyCommands {
		auth.PlanModeReadOnlyCommands = append(auth.PlanModeReadOnlyCommands, prefix)
	}
	return auth
}

func (a *approvalManager) restoreSessionAuthorizations(auth SessionAuthorizations) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, rule := range auth.Grants {
		a.granted[rule] = true
	}
	for _, prefix := range auth.PlanModeReadOnlyCommands {
		a.planModeReadOnlyCommands[prefix] = true
	}
}

// cancel drops a pending approval (timeout/abort path).
func (a *approvalManager) cancel(id string) {
	a.mu.Lock()
	delete(a.approvals, id)
	a.cancelApprovalResolutionLocked(id)
	a.mu.Unlock()
}

// resolve removes and returns the pending approval for id (Approve path).
func (a *approvalManager) resolve(id string) pendingApproval {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.approvals[id]
	delete(a.approvals, id)
	a.cancelApprovalResolutionLocked(id)
	return p
}

func (a *approvalManager) resolveAfter(id string, persist func(pendingApproval) error) (pendingApproval, bool, error) {
	a.mu.Lock()
	p, ok := a.approvals[id]
	if !ok {
		a.mu.Unlock()
		return pendingApproval{}, false, nil
	}
	if inFlight := a.approvalResolutions[id]; inFlight != nil {
		a.mu.Unlock()
		return pendingApproval{}, false, inFlight.wait()
	}
	attempt := newPromptResolution()
	a.approvalResolutions[id] = attempt
	a.mu.Unlock()
	if persist != nil {
		if err := persist(p); err != nil {
			a.mu.Lock()
			a.finishApprovalResolutionLocked(id, attempt, err)
			a.mu.Unlock()
			return pendingApproval{}, false, err
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	current, ok := a.approvals[id]
	if !ok || a.approvalResolutions[id] != attempt || current.reply != p.reply {
		if a.approvalResolutions[id] == attempt {
			a.finishApprovalResolutionLocked(id, attempt, context.Canceled)
		}
		return pendingApproval{}, false, attempt.err
	}
	delete(a.approvals, id)
	a.finishApprovalResolutionLocked(id, attempt, nil)
	return p, true, nil
}

func (a *approvalManager) finishApprovalResolutionLocked(id string, attempt *promptResolution, err error) {
	if attempt == nil || a.approvalResolutions[id] != attempt {
		return
	}
	delete(a.approvalResolutions, id)
	attempt.err = err
	close(attempt.done)
}

func (a *approvalManager) cancelApprovalResolutionLocked(id string) {
	if attempt := a.approvalResolutions[id]; attempt != nil {
		a.finishApprovalResolutionLocked(id, attempt, context.Canceled)
	}
}

func (a *approvalManager) resolveToolAfter(id, tool string, persist func(pendingApproval) error) (pendingApproval, bool, error) {
	p := a.peek(id)
	if p.reply == nil || p.tool != tool {
		return pendingApproval{}, false, nil
	}
	return a.resolveAfter(id, persist)
}

// registerAsk allocates an ask ID, records the pending question batch, and
// returns the reply channel. The ask starts queued: registering before the
// prompt lock is what makes a question waiting behind another prompt visible
// at all, instead of existing only inside a blocked goroutine.
func (a *approvalManager) registerAsk(questions []event.AskQuestion) (string, chan []event.AskAnswer) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextID++
	id := strconv.Itoa(a.nextID)
	reply := make(chan []event.AskAnswer, 1)
	a.asks[id] = pendingAsk{questions: questions, reply: reply, queued: true}
	return id, reply
}

// markAskEmitted clears the queued flag once the ask has reached a frontend,
// which is what makes it eligible for replay.
func (a *approvalManager) markAskEmitted(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p, ok := a.asks[id]; ok {
		p.queued = false
		a.asks[id] = p
	}
}

// queuedAsks reports asks registered but not yet shown.
func (a *approvalManager) queuedAsks() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, p := range a.asks {
		if p.queued {
			n++
		}
	}
	return n
}

// cancelAsk drops a pending ask (timeout/abort path).
func (a *approvalManager) cancelAsk(id string) {
	a.mu.Lock()
	delete(a.asks, id)
	a.cancelAskResolutionLocked(id)
	a.mu.Unlock()
}

func (a *approvalManager) resolveAskAfter(id string, persist func(pendingAsk) error) (pendingAsk, bool, error) {
	a.mu.Lock()
	p, ok := a.asks[id]
	if !ok {
		a.mu.Unlock()
		return pendingAsk{}, false, nil
	}
	if inFlight := a.askResolutions[id]; inFlight != nil {
		a.mu.Unlock()
		return pendingAsk{}, false, inFlight.wait()
	}
	attempt := newPromptResolution()
	a.askResolutions[id] = attempt
	a.mu.Unlock()
	if persist != nil {
		if err := persist(p); err != nil {
			a.mu.Lock()
			a.finishAskResolutionLocked(id, attempt, err)
			a.mu.Unlock()
			return pendingAsk{}, false, err
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	current, ok := a.asks[id]
	if !ok || a.askResolutions[id] != attempt || current.reply != p.reply {
		if a.askResolutions[id] == attempt {
			a.finishAskResolutionLocked(id, attempt, context.Canceled)
		}
		return pendingAsk{}, false, attempt.err
	}
	delete(a.asks, id)
	a.finishAskResolutionLocked(id, attempt, nil)
	return p, true, nil
}

func (a *approvalManager) finishAskResolutionLocked(id string, attempt *promptResolution, err error) {
	if attempt == nil || a.askResolutions[id] != attempt {
		return
	}
	delete(a.askResolutions, id)
	attempt.err = err
	close(attempt.done)
}

func (a *approvalManager) cancelAskResolutionLocked(id string) {
	if attempt := a.askResolutions[id]; attempt != nil {
		a.finishAskResolutionLocked(id, attempt, context.Canceled)
	}
}

// clearAll drops every in-flight prompt without signaling — the cancel path,
// where blocked waiters unblock via their cancelled context instead.
func (a *approvalManager) clearAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	clear(a.approvals)
	clear(a.asks)
	clear(a.mcpInteractions.pending)
	for id := range a.approvalResolutions {
		a.cancelApprovalResolutionLocked(id)
	}
	for id := range a.askResolutions {
		a.cancelAskResolutionLocked(id)
	}
	for id := range a.mcpInteractions.resolutions {
		a.finishMCPInteractionResolutionLocked(id, a.mcpInteractions.resolutions[id], context.Canceled)
	}
}

// clearKind drops pending approvals of one specialized kind. Session recovery
// state uses this during rotations so a card from the previous session cannot
// be answered against the newly active one.
func (a *approvalManager) clearKind(kind string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, pending := range a.approvals {
		if pending.kind == kind {
			delete(a.approvals, id)
			a.cancelApprovalResolutionLocked(id)
		}
	}
}

// hasPending reports whether any prompt is awaiting a user decision.
func (a *approvalManager) hasPending() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.approvals) > 0 || len(a.asks) > 0 || len(a.mcpInteractions.pending) > 0
}

// mode returns the normalized runtime approval posture.
func (a *approvalManager) mode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return normalizeToolApprovalMode(a.toolApprovalMode)
}

// setMode applies a (pre-normalized) posture and drains any pending approvals
// the new posture should auto-allow, returning them for the caller to signal
// {allow:true} after unlocking.
func (a *approvalManager) setMode(mode string) []drainedApproval {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.toolApprovalMode = mode
	switch mode {
	case ToolApprovalAuto:
		return a.drainLocked(false)
	case ToolApprovalYolo:
		return a.drainLocked(true)
	}
	return nil
}

// setPlanAutoApprove toggles the just-approved-plan execution window.
func (a *approvalManager) setPlanAutoApprove(on bool) {
	a.mu.Lock()
	a.planAutoApprove = on
	a.mu.Unlock()
}

// waitContext bounds the blocking wait by approvalTimeout when set.
func (a *approvalManager) waitContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if a.approvalTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, a.approvalTimeout)
}

// snapshotPrompts copies the in-flight prompts for re-emission to a reconnected
// frontend (ReplayPendingPrompts).
func (a *approvalManager) snapshotPrompts() ([]event.Approval, []event.Ask) {
	a.mu.Lock()
	defer a.mu.Unlock()
	approvals := make([]event.Approval, 0, len(a.approvals))
	for id, p := range a.approvals {
		approvals = append(approvals, event.Approval{
			ID: id, Tool: p.tool, Subject: p.subject, Reason: p.reason, RawInput: append(json.RawMessage(nil), p.rawInput...), Fresh: p.fresh,
			Kind: p.kind, Recovery: p.recovery, WriteAccess: event.NormalizeWriteAccessApproval(p.writeAccess),
		})
	}
	asks := make([]event.Ask, 0, len(a.asks))
	for id, p := range a.asks {
		// A queued ask has never been shown; replaying it would put a question
		// on screen ahead of the prompt it is waiting behind.
		if p.queued {
			continue
		}
		asks = append(asks, event.Ask{ID: id, Questions: p.questions})
	}
	return approvals, asks
}

func normalizePlanModeReadOnlyCommandPrefix(prefix string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(prefix)), " ")
}

// decision helpers (caller holds a.mu)

func (a *approvalManager) bypassAllowsLocked(tool, subject string, args json.RawMessage) bool {
	if isMemoryApprovalTool(tool) {
		switch a.toolApprovalMode {
		case ToolApprovalYolo:
			return true
		case ToolApprovalAuto:
			return a.autoApprovalWouldAllowLocked(tool, subject, args)
		}
	}
	if requiresFreshApprovalTool(tool) {
		return false
	}
	if a.toolApprovalMode == ToolApprovalYolo {
		return true
	}
	if !a.planAutoApprove {
		return false
	}
	policy := a.policy
	policy.Mode = permission.Allow
	if len(args) > 0 {
		return policy.Decide(tool, false, args) == permission.Allow
	}
	return policy.DecideSubject(tool, false, subject) == permission.Allow
}

func (a *approvalManager) autoApprovalWouldAllowLocked(tool, subject string, args json.RawMessage) bool {
	if requiresFreshApprovalTool(tool) && !isMemoryApprovalTool(tool) {
		return false
	}
	policy := a.policy
	policy.Mode = permission.Allow
	if len(args) > 0 {
		return policy.Decide(tool, false, args) == permission.Allow
	}
	return policy.DecideSubject(tool, false, subject) == permission.Allow
}

func (a *approvalManager) sessionGrantAllowsLocked(tool, subject string) bool {
	if requiresFreshApprovalTool(tool) && !allowsFreshSessionGrantTool(tool) {
		return false
	}
	for rule := range a.granted {
		if permission.RuleMatchesString(rule, tool, subject) {
			return true
		}
	}
	return false
}

// drainedApproval is a pending approval removed by a posture switch, keeping
// its prompt id so frontends can dismiss exactly the prompts the new posture
// resolved (plan/sandbox/config prompts stay pending and must stay visible).
type drainedApproval struct {
	id    string
	reply chan approvalReply
}

// drainLocked removes every pending approval the new posture should auto-allow
// and returns them; caller holds a.mu and sends {allow:true} after unlocking.
func (a *approvalManager) drainLocked(includeExplicitAsk bool) []drainedApproval {
	pending := make([]drainedApproval, 0, len(a.approvals))
	for id, approval := range a.approvals {
		memoryBypass := isMemoryApprovalTool(approval.tool) && (a.toolApprovalMode == ToolApprovalYolo ||
			a.toolApprovalMode == ToolApprovalAuto && approval.autoDrain)
		if approval.kind == writeAccessKind {
			continue
		}
		if (approval.fresh || requiresFreshApprovalTool(approval.tool)) && !memoryBypass {
			continue
		}
		if approval.requireHuman && !includeExplicitAsk {
			continue
		}
		if !includeExplicitAsk && !approval.autoDrain {
			continue
		}
		delete(a.approvals, id)
		pending = append(pending, drainedApproval{id: id, reply: approval.reply})
	}
	return pending
}

// pure approval helpers

func normalizeToolApprovalMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ToolApprovalAuto, "approve", "allow":
		return ToolApprovalAuto
	case "dontask", "dont-ask", "deny":
		return ToolApprovalDontAsk
	case ToolApprovalYolo, "full", "full-access", "bypass":
		return ToolApprovalYolo
	default:
		return ToolApprovalAsk
	}
}

// RequiresFreshHumanApprovalTool reports tools that session grants,
// Guardian/hooks, and headless nil approvers cannot authorize. Interactive Auto
// treats remember/forget as normal policy fallback, while interactive YOLO may
// also bypass explicit memory ask rules. A controller that owns the scoped
// memory store may still auto-allow a bounded create-only project memory.
func RequiresFreshHumanApprovalTool(tool string) bool {
	switch tool {
	case planApprovalTool, memoryRememberTool, memoryForgetTool, SandboxEscapeApprovalTool, ManagedConfigWriteApprovalTool:
		return true
	default:
		return false
	}
}

func isMemoryApprovalTool(tool string) bool {
	switch tool {
	case memoryRememberTool, memoryForgetTool:
		return true
	default:
		return false
	}
}

func requiresFreshApprovalTool(tool string) bool {
	return RequiresFreshHumanApprovalTool(tool)
}

func allowsFreshSessionGrantTool(tool string) bool {
	switch tool {
	case SandboxEscapeApprovalTool, ManagedConfigWriteApprovalTool:
		return true
	default:
		return false
	}
}

func approvalNotificationText(tool, subject string) string {
	if requiresFreshApprovalTool(tool) {
		return fmt.Sprintf(i18n.M.ApprovalNeededFmt, tool)
	}
	if subject == "" {
		return fmt.Sprintf(i18n.M.ApprovalNeededFmt, tool)
	}
	return fmt.Sprintf(i18n.M.ApprovalNeededWithSubjectFmt, tool, subject)
}

func askNotificationText(questions []event.AskQuestion) string {
	if len(questions) == 0 {
		return fmt.Sprintf(i18n.M.AnswerNeededFmt, "")
	}
	return fmt.Sprintf(i18n.M.AnswerNeededFmt, questions[0].Prompt)
}

func permissionRequestHookPayload(tool, subject string, args json.RawMessage) (string, json.RawMessage, bool) {
	switch tool {
	case planApprovalTool:
		return "", nil, false
	case memoryRememberTool, memoryForgetTool:
		return "", nil, true
	default:
		return subject, args, true
	}
}
