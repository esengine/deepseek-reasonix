package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/i18n"
	"reasonix/internal/provider"
	"reasonix/internal/runtimepolicy"
	"reasonix/internal/tool"
)

// streamedTurn is one provider completion collected by stream. Keeping the
// result together makes the missing-reasoning recovery path explicit: the
// first, malformed completion is never committed before a safe replacement is
// available, and a failed recovery can still fall back to the complete first
// response without re-running any tool.
type streamedTurn struct {
	messageID          string
	text               string
	reasoning          string
	signature          string
	reasoningID        string
	reasoningStatus    string
	reasoningComplete  bool
	reasoningState     provider.ReasoningState
	thinkingBlocks     []provider.ThinkingBlock
	calls              []provider.ToolCall
	responsesItems     []json.RawMessage
	serverSearch       []provider.ServerSearchCall
	usage              *provider.Usage
	interrupted        bool
	partialToolStarted bool
	partialCalls       []provider.ToolCall
	maxArgChars        int // peak streaming tool-arg size for failed-attempt estimates
	err                error
}

func (s streamedTurn) assistantMessage() provider.Message {
	return provider.Message{
		ID:   s.messageID,
		Role: provider.RoleAssistant, Content: s.text, ReasoningContent: s.reasoning,
		ReasoningState: s.reasoningState, ThinkingBlocks: s.thinkingBlocks,
		ReasoningSignature: s.signature, ReasoningID: s.reasoningID, ReasoningStatus: s.reasoningStatus,
		ToolCalls: s.calls, ResponsesItems: s.responsesItems, ServerSearch: s.serverSearch,
	}
}

// beginRunTurn handles evidence scope, delivery classification, background-job
// evidence re-lease, and the initial user-turn persistence. Callers still own
// all Run-level defers (workspace lease, evidence commit, delivery checkpoint,
// steer queue, active-turn timestamp).
func (a *Agent) beginRunTurn(ctx context.Context, input string, pinned pinnedRevisionPlan) (rawInput string, state *turnRuntime) {
	rawInput = RawUserInput(ctx, input)
	providerInput := input
	// A fresh user turn starts from zeroed per-turn host state; the new turn's
	// values are computed below. Cross-turn state (checkpoint, scope, failure
	// budgets) lives in taskRuntime and is reconciled there.
	a.stragglers.drain(ctx, parallelStragglerGrace)
	a.turn = turnRuntime{}
	a.turn.readShadow = newReadShadowState(a.readCoordinatorShadow)
	a.turn.incompleteReads.legacyImplicitFullReads = a.legacyImplicitFullReads
	a.reads.runGen++
	a.reads.tasks = newReadTasks(a.sess.path, a.reads.runGen)
	a.reads.deliveries = make(map[string]readDelivery)
	a.reads.visible = nil
	a.resetStructuralRunGuards()
	scope, scoped := DeliveryExecutionScopeFromContext(ctx)
	preserveEvidence, readinessRecovered := a.beginFinalReadinessRecovery()
	a.turn.readinessRecovered = readinessRecovered
	if a.task.ledger != nil {
		switch {
		case preserveEvidence:
			a.task.ledger.ResetBackgroundLeases()
		case scoped && a.task.scopeID == scope.ID:
			a.task.ledger.ResetBackgroundLeases()
		default:
			a.resetTurnEvidence()
		}
	}
	if scoped {
		a.task.scopeID = scope.ID
	} else if !preserveEvidence {
		a.task.scopeID = ""
	}
	a.turn.deliveryScopeActive = scoped
	if scoped && a.task.checkpoint.ScopeID != scope.ID {
		a.task.checkpoint = evidence.DeliveryCheckpoint{ScopeID: scope.ID}
	}
	a.leasePendingBackgroundEvidence(ctx)
	a.turn.deliveryCriteriaEstablished = a.hasIncompleteCanonicalCriteria() ||
		(a.task.ledger != nil && a.task.ledger.HasSuccessfulTodoWrite()) ||
		(scoped && a.task.checkpoint.CriteriaEstablished)
	// Classify delivery expectations from the task text. Sub-agent spawners
	// pass the pristine task through Options.ClassifierTaskText (a trusted
	// host channel) because their Run input carries host framing whose
	// incidental verbs — "file tools resolve relative paths" — once classified
	// every workspace-wrapped subagent prompt as a mutation request and
	// deadlocked read-only subagents. Without the override the raw input is
	// classified verbatim: stripping user-controllable markup here would let
	// input dressed up as host framing disarm the delivery gates.
	a.turn.turnInput = a.classifierTaskText
	if scoped && strings.TrimSpace(scope.TaskText) != "" {
		a.turn.turnInput = scope.TaskText
	} else if strings.TrimSpace(a.turn.turnInput) == "" {
		a.turn.turnInput = rawInput
	}
	a.turn.recoveryTaskSummary = boundedRecoveryTaskSummary(a.turn.turnInput)
	if constraints, ok := runtimepolicy.FromContext(ctx); ok {
		a.turn.constraints = constraints
	} else {
		a.turn.constraints = runtimepolicy.ParseConstraints(runtimepolicy.StripQuotedConstraints(a.turn.turnInput))
		if a.planMode.Load() {
			a.turn.constraints.PlanModeReadOnly = true
			a.turn.constraints.ForbidMutation = true
		}
	}
	if inherited, ok := runtimepolicy.InheritedFromContext(ctx); ok && !a.readOnlyExecution {
		a.turn.constraints = mergeInheritedConstraints(a.turn.constraints, inherited.Constraints)
		if inherited.PlanReadOnly {
			a.turn.constraints.PlanModeReadOnly = true
			a.turn.constraints.ForbidMutation = true
		}
	} else if a.inheritedExec != nil && !a.readOnlyExecution {
		a.turn.constraints = mergeInheritedConstraints(a.turn.constraints, a.inheritedExec.Constraints)
		if a.inheritedExec.PlanReadOnly {
			a.turn.constraints.PlanModeReadOnly = true
			a.turn.constraints.ForbidMutation = true
		}
	}
	a.recordRebuildAuthorization()
	a.turn.engine = runtimepolicy.NewEngine(a.turn.constraints)
	a.rebuildTurnContract()
	// A cancelled/error turn leaves a provider-excluded recovery record at the
	// transcript tail. Fold its bounded facts into this new user turn exactly
	// once; the user's raw text remains the source above.
	a.ensureUnreplayableHistoryRecovery()
	providerInput = withInterruptedRecovery(providerInput, a.verifyInterruptedWrites(ctx, a.pendingInterruptedRecovery()))
	a.task.prepareScope(scoped, scope.ID)
	a.svc.sink.Emit(event.Event{Kind: event.TurnStarted})
	a.emitTurnPhase(event.TurnPhaseWorking)
	input = a.prepareProviderTurn(ctx, providerInput)
	userCreatedAt := time.Now().UnixMilli()
	a.activeTurnCreatedAt.Store(userCreatedAt)
	rawContent := rawInput
	if rawContent == "" {
		rawContent = a.turn.turnInput
	}
	userMessage := provider.Message{
		ID:   turnUserMessageID(ctx, a.sess.conversation),
		Role: provider.RoleUser, Origin: inputMessageOrigin(ctx), Content: input, RawContent: rawContent,
		Images: userImages(ctx), VisionSummary: VisionSummaryFromContext(ctx), CreatedAt: userCreatedAt,
	}
	a.appendPinnedRevisionAndUser(ctx, pinned, userMessage)
	emitAdmittedUserMessage(a.svc.sink, userMessage)

	// The loop fields join the classification computed above rather than
	// opening a second object: one turn, one turnRuntime. The zero values the
	// old literal spelled out are already there from the reset at the top.
	state = &a.turn
	state.seenTodoProgress = make(map[string]struct{})
	state.executorHandoff = a.executorHandoffGuard && strings.Contains(input, executorHandoffMarker)
	state.input = input
	state.budget = runBudget{started: time.Now()}
	state.todoProgress, state.trackingTodoProgress = a.canonicalTodoProgress()
	if a.task.ledger != nil {
		for _, sig := range a.task.ledger.SuccessfulProgressSignaturesSince(0) {
			state.seenTodoProgress[sig] = struct{}{}
		}
	}
	return rawInput, state
}

// runToolLoop owns the main tool-round budget and dispatches each streamed
// assistant turn into final-response or tool-round handling.
func (a *Agent) runToolLoop(ctx context.Context, state *turnRuntime) (runErr error) {
	defer func() { a.finishReadRun(runErr) }()
	releaseMCPListObserver := a.activateMCPListObserver()
	defer func() {
		a.recordReadonlySoftBudgetSample(state, runErr)
		releaseMCPListObserver()
	}()
	ctx = a.withAgentContext(ctx)
	truncatedRounds := 0
	for step := 0; state.runMaxSteps <= 0 || step < state.runMaxSteps || state.graceRound || state.recoveryGraceRound || state.incompleteReads.hasPending(); step++ {
		// Consume a queued steer and persist it to the session so it
		// survives tab switches and history replay. The model sees it as
		// guidance (with a prefix), not a new task. One cache miss per
		// steer is unavoidable — the model must see the new instruction.
		if text, itemID, ok := a.consumeSteer(); ok {
			a.sess.conversation.Add(provider.Message{
				Role: provider.RoleUser, Origin: provider.MessageOriginUser,
				Content: a.withTurnPreferences(midTurnSteerMessage(text)), RawContent: text,
			})
			a.svc.sink.Emit(event.Event{Kind: event.Steer, Text: text, ItemID: itemID})
		} else if itemID != "" {
			// Loader failed after dequeue: durable entry stays for inspection
			// (unapplied path marks uncertain + pause via the notice sink).
			a.RecordUnappliedSteer("(body load failed)", itemID)
		}
		schemas := a.providerToolSchemas()
		prefixShape := a.capturePrefixShape(schemas)
		prevPrefixShape := a.sess.lastPrefixShape
		if !a.sess.haveLastPrefixShape {
			prevPrefixShape = prefixShape
		}
		// Drain reasons queued since the previous capture (compaction,
		// snip/prune, rewind, guardian merge) so CompareShape can attribute
		// any prefix change to the operation that actually caused it, instead
		// of a generic rewrite signal that also fires on local-only metadata
		// edits.
		contentReasons := a.sess.conversation.DrainContentRewriteReasons()

		// Prefix shape is captured once before sampling and frozen for the
		// whole attempt lifecycle — stream retries must not rewrite session
		// history mid-round, so the shape stays stable across body replays.
		streamed := a.streamWithSamplingRecovery(ctx, step+1)
		text, reasoning, calls, usage := streamed.text, streamed.reasoning, streamed.calls, streamed.usage
		partialCalls, err := streamed.partialCalls, streamed.err
		cacheDiagnostics := CompareShape(prevPrefixShape, prefixShape, usage, contentReasons)
		a.attachSessionContextDiagnostics(&cacheDiagnostics)
		if err != nil {
			quote := a.emitTurnUsage(usage, &cacheDiagnostics)
			a.observeRunBudget(state, usage, quote)
			if msg, ok := finishReasonMessage(usage); ok {
				a.svc.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: msg})
			}
			// Exhausted stream retries (or a non-retryable error): persist one
			// bounded LocalOnly recovery record for the next real user message.
			// Intermediate failed attempts never wrote session state.
			a.recordInterruptedDisplay(text, reasoning, partialCalls, true, err, state.workDurationMs())
			// A broken provider stream can otherwise look like a silent hang
			// followed only by the generic interrupted-turn notice (#9560).
			if code, msg := streamInterruptNotice(err); msg != "" {
				a.svc.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Code: code, Text: msg})
			}
			return err
		}
		a.sess.lastPrefixShape = prefixShape
		a.sess.haveLastPrefixShape = true
		quote := a.emitTurnUsage(usage, &cacheDiagnostics)
		a.observeRunBudget(state, usage, quote)
		if msg, ok := finishReasonMessage(usage); ok {
			a.svc.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: msg})
		}

		// Commit boundary: only a clean terminal attempt reaches here.
		// Keep reasoning_content on the assistant turn for display and session
		// archive. Most OpenAI-compatible backends do not replay it; providers
		// with an explicit round-trip contract retain the raw provider text.
		calls = a.withPreviewFileDiffs(ctx, calls)
		assistant := streamed.assistantMessage()
		assistant.ToolCalls = calls
		assistant.WorkDurationMs = state.workDurationMs()
		a.sess.conversation.Add(assistant)

		if len(calls) == 0 {
			cont, ferr := a.handleFinalResponse(ctx, state, text, reasoning, usage)
			if !cont {
				return ferr
			}
			continue
		}

		if usage != nil && usage.FinishReason == "length" {
			truncatedRounds++
			if err := a.recordTruncatedToolResults(withMessageIdentity(ctx, streamed.messageID), calls); err != nil {
				return err
			}
			if truncatedRounds > maxStreamRecoveries {
				return fmt.Errorf("tool arguments remained truncated after three recovery rounds")
			}
			continue
		}
		truncatedRounds = 0

		// Invariant: executeBatch only ever receives tool calls from a
		// committed sampling attempt (clean terminal + response intercept).
		cont, terr := a.handleToolRound(withMessageIdentity(ctx, streamed.messageID), state, step, text, reasoning, calls, usage)
		if !cont {
			return terr
		}
	}
	// Only reached when a positive maxSteps guard is configured. The work so far
	// is already in the session, so the user can just send another message to pick
	// up where it left off.
	return a.gracePause(state)
}

func (a *Agent) emitProtocolRetry(attempt int, hasFallback bool) {
	maxAttempts := 1
	if hasFallback {
		maxAttempts = 2
	}
	a.svc.sink.Emit(event.Event{
		Kind: event.Retrying, RetryAttempt: attempt, RetryMax: maxAttempts,
		RetryScope: event.RetryScopeProtocol,
	})
}

func (a *Agent) emitStreamAttempt(id string, action event.StreamAttemptAction, attempt int, reason string, err error) {
	if reason == "" && err != nil {
		reason = provider.StreamInterruptReason(err)
	}
	a.svc.sink.Emit(event.Event{
		Kind:      event.StreamAttempt,
		MessageID: id,
		AttemptID: id,
		StreamAttempt: event.StreamAttemptInfo{
			ID: id, Action: action, Attempt: attempt, Max: maxSamplingAttempts, Reason: reason,
		},
	})
}

func newStreamAttemptID(_ int) string {
	// A successful attempt retains this local identity when its message is
	// committed. Failed attempts have distinct identities and cannot alias it.
	return NewMessageID()
}

// streamRetrySleep is the body-retry backoff. Tests replace it with a no-op so
// recovery suites stay fast while production keeps the Codex-shaped delays.
var streamRetrySleep = sleepStreamRetryBackoff

// sleepStreamRetryBackoff waits ~0.5s, 1s, 2s, 4s, 8s with small jitter.
// Returns false when ctx is cancelled during the wait.
func sleepStreamRetryBackoff(ctx context.Context, attempt int) bool {
	return recoverySleep(ctx, time.Duration(1<<min(max(attempt-1, 0), 2))*2*time.Second)
}

var recoverySleep = sleepRecovery

func sleepRecovery(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// handleFinalResponse processes a no-tool assistant turn: recovery pause,
// readiness boundary, empty-final retry, executor handoff nudge, steer drain,
// and final compaction. cont=true continues the tool loop; cont=false returns
// err from Run (err may be nil for a clean final answer).
func (a *Agent) handleFinalResponse(ctx context.Context, state *turnRuntime, text, reasoning string, usage *provider.Usage) (cont bool, err error) {
	if a.readPipelineActive() {
		instruction, pause := a.readContinuation(true)
		if pause != nil {
			// The legacy twin observes the same usage before returning its
			// pause; skipping it here would drop the final round's accounting.
			a.contextManager().ObserveUsage(usage)
			return false, pause
		}
		if instruction != "" {
			a.sess.conversation.Add(HostGeneratedUserMessage(a.withTurnPreferences(instruction)))
			return true, nil
		}
	}
	// A partial read is a host-owned protocol state, not advisory prose. Refuse
	// a candidate final before every ordinary readiness/validator path so a
	// model cannot silently answer from the visible prefix alone.
	if instruction, pause := a.legacyReadFinal(state); pause != nil {
		a.contextManager().ObserveUsage(usage)
		return false, pause
	} else if instruction != "" {
		a.sess.conversation.Add(HostGeneratedUserMessage(a.withTurnPreferences(instruction)))
		a.emitIncompleteReadNotice(
			event.NoticeCodeReadContinuationRequired,
			i18n.M.IncompleteReadFinishBlocked,
			"final answer blocked pending read continuation",
		)
		a.contextManager().ObserveUsage(usage)
		return true, nil
	}
	// Recovery finalization produced a summary. Keep it in the session,
	// but still pause so Goal auto-continue cannot open another Run with
	// a fresh finalization round. turn_done reports recovery_paused.
	if state.recoveryGraceRound {
		a.contextManager().ObserveUsage(usage)
		reason := ""
		if ctrl := a.recoveryEpisodeControl(); ctrl != nil {
			_, _ = ctrl.ConsumeFinalization(a.recovery.taskID)
		}
		return false, &RecoveryPauseError{
			Message:    "Automatic retries paused. Reasonix stopped repeated attempts and kept completed work. Send \"continue\" to start a fresh attempt, or add instructions to change direction.",
			StopReason: reason,
		}
	}
	readiness := a.finalReadinessCheckFor()
	if state.graceRound && (readiness.reason != "" || !hasVisibleFinalAnswer(text)) {
		a.contextManager().ObserveUsage(usage)
		return false, a.gracePause(state)
	}
	if state.graceRound {
		// Explicit max_steps and spend budgets are user-selected boundaries.
		// Preserve the summary, then return a resumable pause so Goal does not
		// immediately open another Run and silently bypass the chosen limit.
		a.contextManager().ObserveUsage(usage)
		return false, a.gracePause(state)
	}
	if readiness.reason != "" {
		// Standard ends with its answer/quality summary. Delivery and Goal hand
		// the structured gap to the controller, which exposes an explicit recovery
		// action or lets the Goal FSM decide whether to continue.
		if a.readinessPauseActive(readiness) {
			event.RecordReadinessAudit(a.svc.sink, readiness.audit(evidence.ReadinessErrored, false))
			a.pending.finalReadinessRecovery = true
			a.persistFinalReadinessRecovery(readiness.missingIDs())
			return false, &FinalReadinessError{
				Attempts:          1,
				Reason:            readiness.reason,
				Missing:           readiness.missingIDs(),
				ContinuationClass: readiness.continuationClass(),
				ProgressKey:       readiness.progressSignature(),
			}
		}
		event.RecordReadinessAudit(a.svc.sink, readiness.audit(evidence.ReadinessAllowed, a.turn.readinessRecovered))
	}
	if !hasVisibleFinalAnswer(text) {
		// Harness-style termination accepts a reasoning-only clean stop. Only
		// explicit internal callers that require visible output retain the
		// bounded synthetic retry below. A truly empty response is classified
		// before this function and retried with the frozen provider request.
		if a.requireVisibleFinal {
			state.terminal.emptyFinalBlocks++
			if state.terminal.emptyFinalBlocks >= maxEmptyFinalBlocks {
				return false, fmt.Errorf("model finished without a visible final answer %d times", state.terminal.emptyFinalBlocks)
			}
			a.svc.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Code: event.NoticeCodeEmptyFinal, Text: emptyFinalNotice(), Detail: emptyFinalNoticeDetail(a.svc.prov.Name(), usage, len(reasoning))})
			a.sess.conversation.Add(HostGeneratedUserMessage(a.withTurnPreferences(emptyFinalRetryMessage())))
			a.contextManager().ObserveUsage(usage)
			return true, nil
		}
	}
	if a.hostContinuationEnabled(ctx) && state.executorHandoff && !state.usedAnyTool && state.terminal.handoffNudges < maxExecutorHandoffNudges && shouldNudgeExecutorHandoff(state.input, text) {
		state.terminal.handoffNudges++
		a.svc.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Code: event.NoticeCodeExecutorHandoff, Text: executorHandoffNoticeText(), Detail: "executor answered without taking any action; nudging it to use its tools"})
		a.sess.conversation.Add(HostGeneratedUserMessage(a.withTurnPreferences(executorHandoffRetryMessage())))
		a.contextManager().ObserveUsage(usage)
		return true, nil
	}
	if a.continueStandardTodo(ctx, state) {
		a.contextManager().ObserveUsage(usage)
		return true, nil
	}
	if readiness.applies || a.turn.readinessRecovered {
		event.RecordReadinessAudit(a.svc.sink, readiness.audit(evidence.ReadinessAllowed, a.turn.readinessRecovered))
	}
	a.emitTurnShadows(a.turn.turnInput)
	if !a.closeSteerIntakeIfIdle() {
		return true, nil
	}
	// A final-answer turn skips compaction, so a large context
	// carries into the next turn un-folded and can overflow the model window.
	// No-op below the trigger, so normal turns keep their warm cache.
	a.contextManager().ObserveUsage(usage)
	return false, nil // model gave a final answer
}

// handleToolRound executes a tool batch, persists tool messages, handles
// cancellation, todo stall tracking, recovery finalization pause, and the
// max-steps grace round. cont=true continues the tool loop; cont=false returns
// err from Run.
func (a *Agent) handleToolRound(ctx context.Context, state *turnRuntime, step int, text, reasoning string, calls []provider.ToolCall, usage *provider.Usage) (cont bool, err error) {
	state.terminal.emptyFinalBlocks = 0
	state.usedAnyTool = true
	unavailableContextTools := a.unavailableContextualToolCalls(ctx, calls)
	if len(unavailableContextTools) > 0 && state.terminal.contextToolRepairs > 0 {
		// Second violation ends the batch: every call is paired, none executes,
		// and same-turn answer text cannot bypass — it predates the host error.
		msg := fmt.Sprintf("blocked: context-unavailable tools were called again after the repair instruction: %s", strings.Join(unavailableContextTools, ", "))
		a.pairUnexecutedGraceCalls(calls, msg)
		a.contextManager().ObserveUsage(usage)
		return false, &CompletionUncertainError{
			Cause:  CompletionUncertainContextTool,
			Detail: strings.Join(unavailableContextTools, ", "),
		}
	}

	boundaryFinalizer := a.allowsBoundaryTurnFinalizer(ctx, state, calls)
	if boundaryErr, stop := a.stopUnexecutedBoundaryCalls(ctx, state, calls, usage); stop {
		return false, boundaryErr
	}

	receiptMark := 0
	if a.task.ledger != nil {
		receiptMark = a.task.ledger.Len()
	}
	batch := a.executeBatch(ctx, state, calls)
	if batch.err != nil {
		// Any completed results are already stored; a failed durability barrier
		// prevents starting the next tool.
		return false, batch.err
	}
	if cont, boundaryErr, handled := a.resolveIncompleteReadToolRoundBoundary(ctx, state, usage); handled {
		return cont, boundaryErr
	}
	if a.successfulTurnFinalizer(ctx, calls, batch) {
		// submit_plan is the planner's data-bearing final answer. Its paired tool
		// result is stored, so another acknowledgement adds no host value and can
		// turn a valid bounded plan into a max-steps pause.
		a.contextManager().ObserveUsage(usage)
		return false, nil
	}
	if boundaryFinalizer {
		// The one allowed boundary finalizer ran but was rejected or blocked.
		// Preserve the one-grace-round contract instead of opening an unbounded
		// loop of malformed terminal submissions.
		a.contextManager().ObserveUsage(usage)
		return false, a.gracePause(state)
	}
	if len(unavailableContextTools) > 0 {
		// First violation: legal tools already ran once. Co-streamed answer
		// text cannot skip repair; only a later clean round may validate.
		state.terminal.contextToolRepairs++
		nudge := fmt.Sprintf("The following tools are unavailable in the current workflow phase: %s. Do not call them again. Respond to the user's request with visible answer text now; call a different tool only if it is still needed to complete the request.", strings.Join(unavailableContextTools, ", "))
		a.sess.conversation.Add(HostGeneratedUserMessage(a.withTurnPreferences(nudge)))
	}
	a.trackTodoProgress(ctx, state, receiptMark)

	// The prompt only grows from here; compact before the next turn so it
	// stays within the model's window.
	a.contextManager().ObserveUsage(usage)

	// When Auto recovery exhausts its Episode budget, offer exactly one
	// summarize-only finalization round. Successful summary ends cleanly;
	// further tool calls surface RecoveryPauseError.
	if batch.recoveryStopTurn && !state.recoveryGraceRound {
		state.recoveryGraceRound = true
		if ctrl := a.recoveryEpisodeControl(); ctrl != nil {
			ctrl.MarkFinalizationOffered(a.recovery.taskID)
		}
		nudge := "Auto recovery has reached its limit for this turn. Do not call any more tools. Summarize what was completed, what failed, and what the user should do next. The user can continue in the next message."
		a.sess.conversation.Add(HostGeneratedUserMessage(a.withTurnPreferences(nudge)))
		return true, nil
	}

	// Spend is checked before rounds: it is the axis a runaway is actually
	// reported in, so on the turns both would catch it should be the one named.
	if axis, detail := a.task.budget.exceeded(a.taskBudgetLimit(ctx)); axis != "" {
		if state.incompleteReads.hasPending() {
			return false, &IncompleteReadError{Reason: fmt.Sprintf("the %s budget ended while a required read_file continuation was still pending: %s", axis, detail)}
		}
		a.armFinalizationRound(ctx, state, landCause{kind: "task_budget", axis: axis, detail: detail})
		return true, nil
	}
	// A bounded read-recovery sequence is a correctness repair, so it may use
	// extra tool rounds beyond max_steps. Hard spend budgets above still win.
	if state.incompleteReads.hasPending() {
		return true, nil
	}
	if state.runMaxSteps > 0 && step+1 >= state.runMaxSteps {
		a.armFinalizationRound(ctx, state, landCause{kind: "max_steps", detail: fmt.Sprintf(
			"budget (%s=%d) exhausted: one grace round to finalize", state.runMaxStepsKey, state.runMaxSteps)})
	}
	return true, nil
}

func (a *Agent) pairUnexecutedGraceCalls(calls []provider.ToolCall, msg string) {
	for _, call := range calls {
		a.sess.conversation.Add(provider.Message{Role: provider.RoleTool, Content: msg, ToolCallID: call.ID, Name: call.Name})
	}
}

func (a *Agent) unavailableContextualToolCalls(ctx context.Context, calls []provider.ToolCall) []string {
	if len(calls) == 0 {
		return nil
	}
	if a == nil || a.svc.tools == nil {
		return nil
	}
	names := make([]string, 0, len(calls))
	seen := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		t, canonical, ambiguous := a.svc.tools.ResolveCall(call.Name)
		if t == nil || len(ambiguous) > 0 {
			continue
		}
		contextual, ok := t.(tool.ContextualTool)
		if !ok || contextual.ProviderVisible(ctx) {
			continue
		}
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		names = append(names, canonical)
	}
	return names
}
