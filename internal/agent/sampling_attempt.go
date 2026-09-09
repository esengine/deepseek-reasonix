package agent

import (
	"context"
	"fmt"

	"reasonix/internal/event"
	"reasonix/internal/provider"
)

// runSamplingAttempt records the exact HTTP-attempt delta for one replay of a
// frozen provider request. Providers outside SendWithRetry retain their own
// RequestCount, while pre-wire failures do not invent a billable request.
func (a *Agent) runSamplingAttempt(ctx context.Context, turn int, sink event.Sink, frozen *samplingRequest, attemptID string) streamedTurn {
	before := provider.RequestAttemptCount(ctx)
	result := a.streamWithFrozen(ctx, turn, event.WithMessageIdentity(sink, attemptID, attemptID), frozen, attemptID)
	result.messageID = attemptID
	if result.err == nil && isEmptyStreamResult(result.text, result.reasoning, result.calls, result.responsesItems, result.serverSearch) {
		result.err = fmt.Errorf("%w: model returned a completed response with no content", provider.ErrEmptyResponse)
	}
	delta := max(provider.RequestAttemptCount(ctx)-before, 0)
	result.usage = estimateFailedAttemptUsage(result.usage, *frozen, result, delta)
	if result.usage != nil {
		if delta > 0 {
			result.usage.RequestCount = delta
		}
	} else if delta > 0 {
		result.usage = &provider.Usage{RequestCount: delta, Unknown: true}
	}
	return result
}

func (a *Agent) samplingAttemptSinks() (*deferredStreamSink, event.Sink) {
	// Buffer when missing reasoning can reject or replace the attempt. Protocols
	// that adopt an empty fallback without retry must keep streaming live because
	// their first response always wins.
	replaySensitive := provider.RequiresToolCallReasoning(a.svc.prov) || provider.RequiresReasoningRoundTrip(a.svc.prov)
	if replaySensitive && !provider.AllowsEmptyReasoningFallback(a.svc.prov) {
		streamSink := newReasoningAwareStreamSink(a.svc.sink)
		return streamSink, streamSink
	}
	return nil, a.svc.sink
}

// recordSamplingAttempt keeps the latest single-request shape separate from
// the multi-attempt billable aggregate used for final accounting.
func (a *Agent) recordSamplingAttempt(billable *provider.Usage, result streamedTurn) (*provider.Usage, streamedTurn) {
	billable = mergeSamplingUsage(billable, result.usage)
	a.storeLatestRequestUsage(result.usage)
	latest := result
	latest.usage = finalizeSamplingUsage(billable, result.usage)
	return billable, latest
}
