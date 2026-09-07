package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/sessioninbox"
	"reasonix/internal/turnevent"
)

// turnEventSink persists lifecycle envelopes before frontend publication.
// Provider-facing transcript messages remain a separate artifact.
type turnEventSink struct {
	event.AuditForwarder
	innerMu sync.RWMutex
	inner   event.Sink
	stream  event.Sink
	c       *Controller
	publish atomic.Int32
}

type turnEventDurableSink struct{ owner *turnEventSink }

// turnEventState has an independent lock so ledger I/O never holds c.mu.
type turnEventState struct {
	mu     sync.RWMutex
	ledger *turnevent.Ledger
	err    error
}

func newTurnEventSink(inner event.Sink, c *Controller) *turnEventSink {
	s := &turnEventSink{inner: inner, c: c}
	s.stream = event.Coalesce(&turnEventDurableSink{owner: s}, event.DefaultStreamDeltaWindow)
	s.AuditForwarder = event.AuditForwarder{Inner: s.stream}
	return s
}

func (s *turnEventSink) InboxChanged(snap sessioninbox.InboxSnapshot) {
	if s != nil {
		notifyInboxChanged(s.innerSnapshot(), snap)
	}
}

var _ event.OptionalSinkCapabilities = (*turnEventSink)(nil)
var _ event.CheckedSink = (*turnEventSink)(nil)
var _ event.OptionalSinkCapabilities = (*turnEventDurableSink)(nil)
var _ event.CheckedSink = (*turnEventDurableSink)(nil)

func (s *turnEventSink) Emit(e event.Event) {
	if s == nil {
		return
	}
	if s.c != nil {
		if ledger := s.c.turnEventLedger(); ledger != nil {
			ledger.ObserveRawEvent(e)
		}
	}
	if turnEventSynchronousBarrier(e.Kind) {
		if err := event.EmitChecked(s.stream, e); err != nil {
			s.fail(err)
		}
		return
	}
	s.stream.Emit(e)
}

func turnEventSynchronousBarrier(kind event.Kind) bool {
	switch kind {
	case event.ToolDispatch, event.ToolResult, event.AskRequest, event.ApprovalRequest,
		event.MCPInteractionRequest, event.PromptAnswered, event.TurnStatusChanged,
		event.TurnStarted, event.TurnDone:
		return true
	default:
		return false
	}
}

func (s *turnEventSink) EmitChecked(e event.Event) error {
	if s == nil {
		return nil
	}
	if s.c != nil {
		if ledger := s.c.turnEventLedger(); ledger != nil {
			ledger.ObserveRawEvent(e)
		}
	}
	var err error
	if s.publish.Load() > 0 && e.Kind == event.PromptAnswered {
		// A frontend may answer during prompt publication, so the coalescer cannot
		// wait on itself. Only that already-ordered PromptAnswered barrier may use
		// this re-entrant path; other checked events preserve coalescer ordering.
		err = (&turnEventDurableSink{owner: s}).EmitChecked(e)
	} else {
		err = event.EmitChecked(s.stream, e)
	}
	if err != nil {
		s.fail(err)
	}
	return err
}

func (s *turnEventSink) fail(err error) {
	if s != nil && s.c != nil && err != nil {
		s.c.failTurnEventLedger(err)
	}
}

func (s *turnEventSink) innerSnapshot() event.Sink {
	if s == nil {
		return nil
	}
	s.innerMu.RLock()
	defer s.innerMu.RUnlock()
	return s.inner
}

func (s *turnEventSink) setInner(inner event.Sink) {
	if s == nil {
		return
	}
	s.innerMu.Lock()
	s.inner = inner
	s.innerMu.Unlock()
}

func (s *turnEventSink) publishInner(e event.Event) {
	inner := s.innerSnapshot()
	if inner == nil {
		return
	}
	s.publish.Add(1)
	defer s.publish.Add(-1)
	inner.Emit(e)
}

// emitChecked persists before publish and returns durability failures to the
// admission boundary. It also suppresses the executor's duplicate TurnStarted
// because the controller has already committed that transition before the
// provider goroutine is launched.
func (s *turnEventSink) persistAndPublish(e event.Event) error {
	if s == nil || s.c == nil {
		return nil
	}
	if e.RecoveryCheckpoint {
		return s.c.checkpointToolTranscript()
	}
	ledger := s.c.turnEventLedger()
	if ledger == nil {
		s.publishInner(e)
		return nil
	}
	// Outside-turn notices are not lifecycle records and must pass through after
	// bootstrap or a terminal event.
	if ledger.ActiveTurnID() == "" {
		s.publishInner(e)
		return nil
	}
	if e.Kind == event.TurnStarted && ledger.CurrentStatus() == event.TurnInProgress {
		return nil
	}
	status := e.Status
	if status == "" {
		status = ledger.CurrentStatus()
	}
	switch e.Kind {
	case event.TurnStarted:
		status = event.TurnInProgress
	case event.AskRequest, event.ApprovalRequest, event.MCPInteractionRequest:
		status = event.TurnWaitingUser
	case event.TurnDone:
		status = terminalTurnStatus(e)
		if s.c.executor != nil && s.c.executor.Session() != nil {
			session := s.c.executor.Session()
			digest, digestErr := session.ContentDigest()
			if digestErr != nil {
				slog.Warn("controller: compute terminal transcript digest", "err", digestErr)
			} else {
				ledger.SetTranscriptSnapshot(int64(session.TranscriptVersion()), digest)
			}
		}
	case event.TurnStatusChanged:
		// The emitter supplied the exact transition in e.Status.
	}
	if e.WriteIntent || e.Kind == event.ToolResult || (e.Kind == event.ToolDispatch && !e.Tool.Partial && !e.Tool.ReadOnly) {
		if err := s.c.checkpointToolTranscript(); err != nil {
			return fmt.Errorf("checkpoint tool transcript: %w", err)
		}
	}
	if e.WriteIntent {
		return nil
	}
	stamped, ok, err := ledger.Append(e, status)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if e.Kind == event.ToolStarted && !e.Tool.ReadOnly {
		s.c.stampCheckpointIdentity(e, stamped.TurnID)
	}
	s.publishInner(stamped)
	if e.Kind == event.TurnDone && !ledger.ProjectionAckRequired() {
		if err := ledger.AcknowledgeProjection(stamped.TurnID); err != nil {
			return err
		}
	}
	return nil
}

func (s *turnEventDurableSink) Emit(e event.Event) {
	_ = s.EmitChecked(e)
}

func (s *turnEventDurableSink) EmitChecked(e event.Event) error {
	if s == nil || s.owner == nil {
		return nil
	}
	err := s.owner.persistAndPublish(e)
	if err == nil {
		return nil
	}
	// Async stream callers cannot observe checked errors. Fail the Turn here so
	// a poisoned WAL immediately cancels provider, prompt, and process work.
	slog.Error("controller: append turn event ledger", "err", err, "kind", e.Kind)
	s.owner.fail(err)
	if e.Kind == event.TurnDone {
		// The durable terminal failed, so publish a sequence-free control-plane
		// failure only to release UI state. It is never treated as ledger truth.
		e.Err = errors.Join(e.Err, err)
		e.Status = event.TurnFailed
		if inner := s.owner.innerSnapshot(); inner != nil {
			inner.Emit(e)
		}
	}
	return err
}

func (s *turnEventDurableSink) inner() event.Sink {
	if s == nil || s.owner == nil {
		return nil
	}
	return s.owner.innerSnapshot()
}

func (s *turnEventDurableSink) RecordDelegationAudit(a evidence.DelegationAudit) {
	event.RecordDelegationAudit(s.inner(), a)
}
func (s *turnEventDurableSink) RecordReadinessAudit(a evidence.ReadinessAudit) {
	event.RecordReadinessAudit(s.inner(), a)
}
func (s *turnEventDurableSink) RecordAnchorSafetyAudit(a event.AnchorSafetyAudit) {
	event.RecordAnchorSafetyAudit(s.inner(), a)
}
func (s *turnEventDurableSink) RecordTurnCompletion() { event.RecordTurnCompletion(s.inner()) }
func (s *turnEventDurableSink) RecordContractShadow(a event.ContractShadowAudit) {
	event.RecordContractShadow(s.inner(), a)
}
func (s *turnEventDurableSink) RecordCompletionReport(a event.CompletionReportAudit) {
	event.RecordCompletionReport(s.inner(), a)
}
func (s *turnEventDurableSink) RecordMemoryRecall(a event.MemoryRecallAudit) {
	event.RecordMemoryRecall(s.inner(), a)
}
func (s *turnEventDurableSink) RecordDelegationAdmission(a event.DelegationAdmissionAudit) {
	event.RecordDelegationAdmission(s.inner(), a)
}
func (s *turnEventDurableSink) RecordOutcomeProgress(a evidence.OutcomeSample) {
	event.RecordOutcomeProgress(s.inner(), a)
}
func (s *turnEventDurableSink) RecordProtocolRecovery(a event.ProtocolRecoveryAudit) {
	event.RecordProtocolRecovery(s.inner(), a)
}
func (s *turnEventDurableSink) RecordWorkspaceMutation(a event.WorkspaceMutation) {
	event.RecordWorkspaceMutation(s.inner(), a)
}
func (s *turnEventDurableSink) RecordRunBudget(a event.RunBudgetSample) {
	event.RecordRunBudget(s.inner(), a)
}
func (s *turnEventDurableSink) RecordSubagentLifecycle(a event.SubagentLifecycleInfo) {
	event.RecordSubagentLifecycle(s.inner(), a)
}

func terminalTurnStatus(e event.Event) event.TurnStatus {
	var uncertain *agent.CompletionUncertainError
	if errors.As(e.Err, &uncertain) {
		return event.TurnRecoveryRequired
	}
	// An unproven side effect or a turn that died before producing anything
	// is not merely interrupted: the next write-capable turn needs a decision.
	if r := e.Recovery; r != nil && (r.RequiresUser || r.Silent) {
		return event.TurnRecoveryRequired
	}
	if e.Cancelled || errors.Is(e.Err, context.Canceled) {
		return event.TurnInterrupted
	}
	if e.Err != nil {
		return event.TurnFailed
	}
	return event.TurnCompleted
}

func (c *Controller) turnEventLedger() *turnevent.Ledger {
	if c == nil {
		return nil
	}
	c.turnEvents.mu.RLock()
	defer c.turnEvents.mu.RUnlock()
	return c.turnEvents.ledger
}

func (c *Controller) turnEventLedgerError() error {
	if c == nil {
		return nil
	}
	c.turnEvents.mu.RLock()
	defer c.turnEvents.mu.RUnlock()
	return c.turnEvents.err
}

func (c *Controller) prepareTurnAdmission(body func(context.Context) error) func(context.Context) error {
	admissionErr := c.turnEventLedgerError()
	if ledger := c.turnEventLedger(); admissionErr == nil && ledger != nil {
		if _, err := ledger.Begin(); err != nil {
			admissionErr = err
		} else if err := c.emitTurnEventChecked(event.Event{Kind: event.TurnStatusChanged, Status: event.TurnQueued}); err != nil {
			admissionErr = err
		} else if err := c.emitTurnEventChecked(event.Event{Kind: event.TurnStarted, Status: event.TurnInProgress}); err != nil {
			admissionErr = err
		}
	}
	if admissionErr == nil {
		return body
	}
	slog.Error("controller: persist turn admission", "err", admissionErr)
	return func(context.Context) error { return fmt.Errorf("persist turn admission: %w", admissionErr) }
}

func (c *Controller) applyTurnDoneProtocol(done event.Event, cancelRequested bool) event.Event {
	if cancelRequested {
		// Interruption is a terminal state, not a send failure; partial text is
		// already display-only by this point.
		done.Err = nil
	}
	return done
}

func (c *Controller) turnEventRuntimeStatus() (string, event.TurnStatus, uint64, uint64) {
	ledger := c.turnEventLedger()
	if ledger == nil {
		return "", "", 0, 0
	}
	latest, replayAfter := ledger.ProjectionCursor()
	return ledger.ActiveTurnID(), ledger.CurrentStatus(), latest, replayAfter
}

func (c *Controller) rebindTurnEvents(sessionPath string) {
	if c == nil {
		return
	}
	ledger, err := turnevent.Open(sessionPath, agent.BranchID(sessionPath))
	if err != nil {
		// Normalize platform-specific open errors behind the same storage
		// sentinel used by append failures. Keep the original error in the
		// chain so unsupported-schema callers can still inspect its type.
		err = fmt.Errorf("%w: %w", turnevent.ErrTurnLedgerUnavailable, err)
		slog.Warn("controller: open turn event ledger", "err", err, "session", agent.BranchID(sessionPath))
		c.turnEvents.mu.Lock()
		c.turnEvents.ledger = nil
		c.turnEvents.err = err
		c.turnEvents.mu.Unlock()
		return
	}
	c.turnEvents.mu.Lock()
	previous := c.turnEvents.ledger
	c.turnEvents.ledger = ledger
	c.turnEvents.err = nil
	c.turnEvents.mu.Unlock()
	if previous != nil && previous != ledger {
		if closeErr := previous.Close(); closeErr != nil {
			slog.Warn("controller: close previous turn event ledger", "err", closeErr)
		}
	}
}

func (c *Controller) failTurnEventLedger(err error) {
	if c == nil || err == nil {
		return
	}
	c.turnEvents.mu.Lock()
	if c.turnEvents.err == nil {
		c.turnEvents.err = err
	}
	c.turnEvents.mu.Unlock()
	c.mu.Lock()
	cancel := c.cancel
	if cancel != nil {
		c.canceling = true
	}
	c.mu.Unlock()
	if cancel != nil {
		// Cancel and prompt resolvers may hold promptResolveMu while this
		// synchronous failure callback runs. Use the owners' internal locks to
		// invalidate pending resolutions without reentering the submission lock.
		c.promptOwner.CancelAll()
		c.approval.clearAll()
		cancel()
	}
}

func (c *Controller) emitTurnStatus(status event.TurnStatus) {
	if c == nil || status == "" {
		return
	}
	c.sink.Emit(event.Event{Kind: event.TurnStatusChanged, Status: status})
}

// emitTurnEventChecked reaches the lifecycle sink below the inbox observer so
// admission can fail closed on disk errors instead of starting an unledgered
// provider request. Lifecycle events do not participate in inbox notice logic.
func (c *Controller) emitTurnEventChecked(e event.Event) error {
	if c == nil {
		return nil
	}
	if e.ItemID != "" && e.TurnID == "" {
		if identity, ok := c.promptOwner.Identity(e.ItemID); ok {
			e.TurnID = identity.TurnID
			e.PromptKind = string(identity.Kind)
		}
	} else if e.ItemID != "" && e.PromptKind == "" {
		if identity, ok := c.promptOwner.Identity(e.ItemID); ok {
			e.PromptKind = string(identity.Kind)
		}
	}
	return event.EmitChecked(c.sink, e)
}

// SetTurnEventRoutingMetadata attaches desktop routing identity to lifecycle
// envelopes only. It never changes provider-visible prompts or tool schemas.
func (c *Controller) SetTurnEventRoutingMetadata(runtimeEpoch, submissionID string) {
	c.promptResolveMu.Lock()
	c.promptRuntimeEpoch = runtimeEpoch
	c.promptResolveMu.Unlock()
	if ledger := c.turnEventLedger(); ledger != nil {
		ledger.RequireProjectionAck(true)
		ledger.SetRoutingMetadata(runtimeEpoch, submissionID)
	}
}

// TurnEventsAfter returns the durable lifecycle suffix used by reconnecting
// frontends to close sequence gaps.
func (c *Controller) TurnEventsAfter(after uint64) ([]turnevent.Envelope, error) {
	ledger := c.turnEventLedger()
	if ledger == nil {
		return []turnevent.Envelope{}, nil
	}
	return ledger.EventsAfter(after)
}

func (c *Controller) TurnEventReplay(after uint64) (turnevent.ReplayView, error) {
	ledger := c.turnEventLedger()
	if ledger == nil {
		return turnevent.ReplayView{Events: []turnevent.Envelope{}}, nil
	}
	return ledger.Replay(after)
}

func (c *Controller) AcknowledgeTurnProjection(turnID string) error {
	ledger := c.turnEventLedger()
	if ledger == nil {
		return nil
	}
	return ledger.AcknowledgeProjection(turnID)
}

func (c *Controller) ObserveTurnProjectionRetry() {
	if ledger := c.turnEventLedger(); ledger != nil {
		ledger.ObserveProjectionRetry()
	}
}

func (c *Controller) PendingTurnProjections() []turnevent.PendingProjection {
	ledger := c.turnEventLedger()
	if ledger == nil {
		return []turnevent.PendingProjection{}
	}
	return ledger.PendingProjections()
}

func (c *Controller) TurnEventMetrics() turnevent.MetricsSnapshot {
	ledger := c.turnEventLedger()
	if ledger == nil {
		return turnevent.MetricsSnapshot{}
	}
	return ledger.MetricsSnapshot()
}

func (c *Controller) DrainTurnEventMetrics() turnevent.MetricsSnapshot {
	ledger := c.turnEventLedger()
	if ledger == nil {
		return turnevent.MetricsSnapshot{}
	}
	return ledger.DrainMetrics()
}

// TurnIDForSubmission exposes the synchronous admission receipt without
// depending on whether the provider is still running when Wails returns.
func (c *Controller) TurnIDForSubmission(submissionID string) string {
	ledger := c.turnEventLedger()
	if ledger == nil {
		return ""
	}
	return ledger.TurnIDForSubmission(submissionID)
}
