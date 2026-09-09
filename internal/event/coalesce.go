package event

import (
	"reflect"
	"strings"
	"sync"
	"time"

	"reasonix/internal/evidence"
	"reasonix/internal/nilutil"
)

// coalesceMaxBytes bounds a merged delta so one event never carries an
// unbounded payload across a frontend bridge.
const coalesceMaxBytes = 16 << 10

// DefaultStreamDeltaWindow caps how often coalesced streaming deltas cross
// into a frontend: at most one merged event per window under load — about one
// per display frame — so a fast provider (hundreds of chunks/sec) cannot
// flood a webview bridge, an SSE stream, or a terminal redraw loop.
const DefaultStreamDeltaWindow = 16 * time.Millisecond

// Coalesce wraps inner so bursts of consecutive streaming deltas — Text or
// Reasoning events carrying nothing but a Text payload — merge into one event.
// The first delta of a burst forwards immediately (time-to-first-token is
// unchanged); later deltas buffer at most window, flushing earlier on any
// other event (total order preserved), a kind switch, or coalesceMaxBytes.
func Coalesce(inner Sink, window time.Duration) Sink {
	if nilutil.IsNil(inner) {
		return Discard
	}
	if window <= 0 {
		return inner
	}
	return &coalescer{inner: inner, window: window}
}

type coalescer struct {
	inner  Sink
	window time.Duration

	// mu guards buffering state and the outbound queue; inner.Emit is never
	// called under mu. A single drainer forwards FIFO, so a sink that
	// synchronously re-enters Emit enqueues and returns instead of deadlocking.
	mu          sync.Mutex
	kind        Kind
	source      string
	messageID   string
	attemptID   string
	buf         strings.Builder
	pending     bool
	timer       *time.Timer
	lastForward time.Time
	queue       []coalescedEvent
	draining    bool
}

type coalescedEvent struct {
	event   Event
	done    chan error
	forward func()
}

var _ OptionalSinkCapabilities = (*coalescer)(nil)
var _ CheckedSink = (*coalescer)(nil)

// isStreamDelta reports whether e is a pure streaming delta: merging is only
// safe when no other field carries meaning. The zero-probe comparison keeps
// this true by construction as Event grows fields.
func isStreamDelta(e Event) bool {
	if (e.Kind != Text && e.Kind != Reasoning) || e.Text == "" {
		return false
	}
	probe := e
	probe.Text = ""
	probe.Source = ""
	probe.MessageID = ""
	probe.AttemptID = ""
	return reflect.DeepEqual(probe, Event{Kind: e.Kind})
}

func (c *coalescer) Emit(e Event) {
	_ = c.enqueue(e, false)
}

// EmitChecked is a synchronous ordering barrier. Buffered deltas are written
// before e, and it returns only after the durable inner sink has acknowledged
// e. Regular streaming Emit calls remain non-blocking while a drainer is
// active; they learn asynchronous failures through the lifecycle sink's
// poisoned-ledger state.
func (c *coalescer) EmitChecked(e Event) error {
	return c.enqueue(e, true)
}

func (c *coalescer) enqueue(e Event, checked bool) error {
	var done chan error
	if checked {
		done = make(chan error, 1)
	}
	c.mu.Lock()
	if checked && isStreamDelta(e) {
		c.enqueueFlushLocked()
		c.queue = append(c.queue, coalescedEvent{event: e, done: done})
		c.drainAndUnlock()
		return <-done
	}
	if !isStreamDelta(e) {
		c.enqueueFlushLocked()
		c.queue = append(c.queue, coalescedEvent{event: e, done: done})
		c.drainAndUnlock()
		if done != nil {
			return <-done
		}
		return nil
	}
	if c.pending && (c.kind != e.Kind || c.source != e.Source || c.messageID != e.MessageID || c.attemptID != e.AttemptID) {
		c.enqueueFlushLocked()
	}
	if !c.pending && time.Since(c.lastForward) >= c.window {
		c.lastForward = time.Now()
		c.queue = append(c.queue, coalescedEvent{event: e, done: done})
		c.drainAndUnlock()
		if done != nil {
			return <-done
		}
		return nil
	}
	if !c.pending {
		c.pending = true
		c.kind = e.Kind
		c.source = e.Source
		c.messageID = e.MessageID
		c.attemptID = e.AttemptID
		if c.timer == nil {
			c.timer = time.AfterFunc(c.window, c.flush)
		} else {
			c.timer.Reset(c.window)
		}
	}
	c.buf.WriteString(e.Text)
	if c.buf.Len() >= coalesceMaxBytes {
		c.enqueueFlushLocked()
	}
	c.drainAndUnlock()
	if done != nil {
		return <-done
	}
	return nil
}

func (c *coalescer) flush() {
	c.mu.Lock()
	c.enqueueFlushLocked()
	c.drainAndUnlock()
}

// enqueueFlushLocked moves the buffered delta (if any) onto the outbound queue.
func (c *coalescer) enqueueFlushLocked() {
	if !c.pending {
		return
	}
	c.timer.Stop()
	c.queue = append(c.queue, coalescedEvent{event: Event{Kind: c.kind, Text: c.buf.String(), Source: c.source, MessageID: c.messageID, AttemptID: c.attemptID}})
	c.buf.Reset()
	c.pending = false
	c.source = ""
	c.lastForward = time.Now()
}

// drainAndUnlock forwards queued events in FIFO order and releases mu. Exactly
// one goroutine drains at a time; others enqueue and return.
func (c *coalescer) drainAndUnlock() {
	if c.draining || len(c.queue) == 0 {
		c.mu.Unlock()
		return
	}
	c.draining = true
	for len(c.queue) > 0 {
		batch := c.queue
		c.queue = nil
		c.mu.Unlock()
		for _, item := range batch {
			if item.forward != nil {
				item.forward()
				continue
			}
			err := EmitChecked(c.inner, item.event)
			if item.done != nil {
				item.done <- err
				close(item.done)
			}
		}
		c.mu.Lock()
	}
	c.draining = false
	c.mu.Unlock()
}

// Optional sink capabilities flush first so audits never overtake a buffered
// delta, then forward to inner sinks that opt in.

func (c *coalescer) enqueueCapability(forward func()) {
	c.mu.Lock()
	c.enqueueFlushLocked()
	c.queue = append(c.queue, coalescedEvent{forward: forward})
	c.drainAndUnlock()
}

func (c *coalescer) RecordDelegationAudit(a evidence.DelegationAudit) {
	c.enqueueCapability(func() { RecordDelegationAudit(c.inner, a) })
}

func (c *coalescer) RecordReadinessAudit(a evidence.ReadinessAudit) {
	c.enqueueCapability(func() { RecordReadinessAudit(c.inner, a) })
}

func (c *coalescer) RecordAnchorSafetyAudit(a AnchorSafetyAudit) {
	c.enqueueCapability(func() { RecordAnchorSafetyAudit(c.inner, a) })
}

func (c *coalescer) RecordTurnCompletion() {
	c.enqueueCapability(func() { RecordTurnCompletion(c.inner) })
}

func (c *coalescer) RecordProtocolRecovery(a ProtocolRecoveryAudit) {
	c.enqueueCapability(func() { RecordProtocolRecovery(c.inner, a) })
}

func (c *coalescer) RecordContractShadow(a ContractShadowAudit) {
	c.enqueueCapability(func() { RecordContractShadow(c.inner, a) })
}

func (c *coalescer) RecordCompletionReport(a CompletionReportAudit) {
	c.enqueueCapability(func() { RecordCompletionReport(c.inner, a) })
}

func (c *coalescer) RecordOutcomeProgress(sample evidence.OutcomeSample) {
	c.enqueueCapability(func() { RecordOutcomeProgress(c.inner, sample) })
}

func (c *coalescer) RecordMemoryRecall(a MemoryRecallAudit) {
	c.enqueueCapability(func() { RecordMemoryRecall(c.inner, a) })
}

func (c *coalescer) RecordDelegationAdmission(a DelegationAdmissionAudit) {
	c.enqueueCapability(func() { RecordDelegationAdmission(c.inner, a) })
}

func (c *coalescer) RecordWorkspaceMutation(m WorkspaceMutation) {
	c.enqueueCapability(func() { RecordWorkspaceMutation(c.inner, m) })
}

func (c *coalescer) RecordRunBudget(sample RunBudgetSample) {
	c.enqueueCapability(func() { RecordRunBudget(c.inner, sample) })
}

func (c *coalescer) RecordSubagentLifecycle(info SubagentLifecycleInfo) {
	c.enqueueCapability(func() { RecordSubagentLifecycle(c.inner, info) })
}
