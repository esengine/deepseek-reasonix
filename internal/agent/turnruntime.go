package agent

import (
	"reasonix/internal/completion"
	"reasonix/internal/provider"
	"reasonix/internal/runtimepolicy"
)

// turnRuntime is the host state for exactly one Agent.Run. beginRunTurn builds
// it in a single assignment, so a field added here starts the next turn zeroed.
// State an external caller arms before a Run lives in pendingTurn; state that
// outlives the Run lives in taskRuntime or sessionRuntime.
type turnRuntime struct {
	writeRecovery   map[string]provider.ToolCall // unresolved prior effects; reverified before reuse
	unknownRecovery map[string]provider.ToolCall // all unresolved calls, including non-file side effects
	runMaxSteps     int
	runMaxStepsKey  string

	terminal           terminalProtocolState
	usedAnyTool        bool
	graceRound         bool
	recoveryGraceRound bool

	todoProgress         int
	trackingTodoProgress bool
	todoStallRounds      int
	seenTodoProgress     map[string]struct{}
	// standardTodoContinuations is the bounded same-Run repair for a Standard
	// execution turn that wrote an active todo and then tried to stop. The
	// fingerprint gates the optional second nudge on new host-observed work.
	standardTodoContinuations int
	standardTodoProgress      string

	executorHandoff bool
	input           string
	workDurationMs  func() int64

	// budget is the turn's spend axis: tokens, money, wall clock.
	budget runBudget
	// landCause records why the grace round was armed, so the pause the Run
	// ends with names the axis that actually stopped it.
	landCause landCause

	// turnInput is this run's task text. The contract is rebuilt from it and
	// the ledger whenever a live view is needed, so one replay serves both the
	// per-round observation and the end-of-turn record.
	turnInput string
	// completion is the report built as the turn ends; the host reads it while
	// emitting TurnDone, before the next turn resets this state.
	completion *completion.Report
	// deliveryCriteriaEstablished may inherit an unfinished canonical task
	// list on continuation, but the flag itself is recomputed every turn.
	deliveryCriteriaEstablished bool
	deliveryScopeActive         bool
	// readinessRecovered marks a run that started with evidence preserved from
	// (or a pending recovery of) a prior readiness failure, so the final
	// allowed audit can report Recovered=true.
	readinessRecovered bool

	// recoveryTaskSummary is the bounded task text for this Agent.Run. It lets
	// a shared recovery gate review sub-agent mutations against the child
	// task, rather than the root controller transcript.
	recoveryTaskSummary string

	// blockedTurnStreak counts consecutive rounds the host blocked outright.
	// stormSig catches fixation on one call shape; this catches rotation
	// between blocked shapes, which is zero progress all the same.
	blockedTurnStreak int

	// loopGuardArmed stands final readiness down after a loop guard fired:
	// demanding receipts the blocker prevents would restart the loop. The mark
	// is the pre-batch ledger count, so later progress revokes the pass.
	loopGuardArmed       bool
	loopGuardReceiptMark int

	// repeatSuccessCounts catches the shape stormSig cannot see: the same write
	// succeeding over and over leaves no error for a failure-only breaker.
	repeatSuccessCounts map[string]int
	loop                turnLoopState
	softBudgetMutation  bool

	// constraints and engine are frozen at the start of the Run.
	constraints runtimepolicy.Constraints
	engine      *runtimepolicy.Engine

	// reviewWarnings are warn-level findings to surface in the final summary.
	reviewWarnings []string

	// stormSig keys on (tool, error/blocker), NOT (tool, args): a stuck model
	// reworks arguments cosmetically while the host returns the same refusal,
	// so keying on args misses the loop entirely. See applyStormBreaker.
	stormSig   string
	stormCount int

	// progress escalates adaptively on consecutive zero-evidence-gain rounds;
	// see progress_guard.go.
	progress progressGuard

	// lastReasoning is the previous executor round's reasoning-token spend,
	// read by the governor trigger (live policy and fork capture alike).
	lastReasoning int

	// incompleteReads tracks unread read_file results within one Agent.Run; a
	// fresh user turn may choose a different strategy, but this run cannot write
	// or finish from a silent partial read.
	incompleteReads incompleteReadState

	phase phaseClock

	// sessionContext is the content-free diagnostic for the snapshot selected
	// before this real user turn. It is attached to Usage events only.
	sessionContext turnContextDiagnostics
}

// terminalProtocolState groups the run's terminal-protocol bookkeeping: the
type terminalProtocolState struct {
	// emptyFinalBlocks counts consecutive reasoning-only stops retried for a
	// visible final answer.
	emptyFinalBlocks int
	// handoffNudges counts executor-handoff repairs sent this run.
	handoffNudges int
	// contextToolRepairs counts contextual-tool repair rounds; a second
	// violation after a repair ends the run in a recoverable pause.
	contextToolRepairs int
}

// pendingTurn is what someone outside the Run arms for the next one: a
// sub-agent spawner, the turn that just failed readiness, or the fork capture.
// It is deliberately not in turnRuntime — beginRunTurn builds that fresh, and
// state armed before it exists would be wiped by the same assignment that makes
// turnRuntime safe.
type pendingTurn struct {
	// preserveEvidence makes the next Run keep the turn evidence ledger instead
	// of resetting it, so a review_report completion nudge can cite the read
	// receipts the subagent already earned. Consumed by that Run.
	preserveEvidence bool
	// finalReadinessRecovery is armed after final readiness fails. An explicit
	// host action preserves receipts once; an ordinary turn resets evidence.
	finalReadinessRecovery bool
	// finalReadinessRecoveryPrepared prevents the durable marker fallback from
	// being consumed twice before the prepared Run starts.
	finalReadinessRecoveryPrepared bool
	// forkRestore, when armed, swaps the frozen fork-bundle conversation in
	// right after beginRunTurn — the counterfactual-continuation seam.
	forkRestore func(*turnRuntime)
}
