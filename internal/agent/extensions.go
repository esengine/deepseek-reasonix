package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"reasonix/internal/event"
	"reasonix/internal/extension"
	"reasonix/internal/extension/dispatch"
	"reasonix/internal/extension/providerconv"
	"reasonix/internal/provider"
)

// Extension Protocol v2 agent-side wiring. The agent consults the
// frozen dispatcher at the nine agent-loop intercept points:
//
//	agent.before_start   Run, before the turn is appended (block aborts the run)
//	context.prepare      stream, on the request message copy (never the session)
//	provider.request     stream, after the request is fully assembled
//	provider.response    stream, after a successful stream, before persisting
//	tool.before          executeOne, right after the call parses
//	permission.decision  executeOne, at the permission gate (allow/deny rulings)
//	tool.after           executeOne, after Execute returns (success or error)
//	compaction.prepare   compact, before the fold is archived and summarized
//	compaction.complete  compact, after the summary is produced, before persist
//
// Decision semantics are uniform: continue passes the payload through;
// replace substitutes it (strictly re-decoded and revalidated by the
// dispatcher, then checked against host invariants here); block fails the
// local operation — the run (before_start), the provider request (context/
// provider.request), the turn (provider.response), the tool call
// (tool.before/after, permission.decision), or the compaction pass — with the
// redacted reason. allow/deny is terminal at permission.decision only, where
// the host verdict is computed FIRST and combined: an extension allow
// overrides a host deny (full-trust contract, audited), an extension deny
// overrides a host allow, and continue leaves the host decision standing.
//
// Error policy follows the dispatcher: a required extension's failure fails
// the local operation; an optional extension's failure is warned about once
// and skipped. A nil dispatcher (no runtime packages installed) passes
// every point through untouched, so behavior stays byte-identical to the
// pre-dispatch path.
//
// Two-phase ruling at slot-mapped points: points that map to a replacement
// slot (context.prepare → context, provider.request → provider_request,
// provider.response → provider_response, compaction.prepare/complete →
// compaction, permission.decision → permission) first walk the intercept
// chain, then give the slot's OWNER the final say through RunStrategy over
// the (possibly interceptor-modified) payload. An owner that also declared
// the point under intercepts participates in both phases, in exactly this
// order — chain interceptor first, slot strategy last — so its strategy
// ruling is always the final replacement phase. A chain block short-circuits
// the strategy phase (the operation is already stopped). The owner is
// required-class by definition: its block, timeout, error, or contract
// violation is fatal to the local operation. Replaced values are adopted only
// when a replace ruling actually changed the payload, so a no-replacement
// walk (including an unowned slot) keeps the original values byte-identically.
//
// Ephemerality is the cache contract: context.prepare and provider.request
// replacements shape only the request being assembled — a.session.Messages is
// never mutated — while a provider.response replacement is persisted as the
// visible assistant turn (that IS the user's transcript), and tool.after
// replacements become the tool result the model reads.
//
// Observer model: after every completed intercept walk (blocked or not) the
// agent fires the point's fire-and-forget Event with the final payload, so
// observation-only extensions see exactly what the host acted on. A
// required-extension failure skips the event — the operation itself failed.

// extensionBlockedError reports an extension's block ruling as an operation
// failure. The reason is already credential-redacted by the dispatcher.
func extensionBlockedError(point extension.InterceptorPoint, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "no reason given"
	}
	return fmt.Errorf("extension blocked %s: %s", point, reason)
}

// extensionBlockReason normalizes a block reason for tool-result surfaces.
func extensionBlockReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "blocked by extension"
	}
	return reason
}

// strategyReplaced runs the replacement-slot owner's strategy for the point
// and reports whether a replace ruling actually changed the payload (the
// adoption signal for the caller's converted values). An unowned slot no-ops
// inside RunStrategy, so the fast path costs one comparison. The owner is
// required-class: a block, timeout, error, or contract violation is returned
// as a fatal error for the local operation.
func strategyReplaced(ctx context.Context, d *dispatch.Dispatcher, slot extension.Slot, point extension.InterceptorPoint, payloadPtr any) (bool, error) {
	before := reflect.ValueOf(payloadPtr).Elem().Interface()
	if err := d.RunStrategy(ctx, slot, point, payloadPtr); err != nil {
		return false, err
	}
	return !reflect.DeepEqual(before, reflect.ValueOf(payloadPtr).Elem().Interface()), nil
}

// interceptAgentStart runs agent.before_start at the top of Run. A block (or
// a required extension's failure) aborts the run before the user turn is
// appended; the error surfaces like a normal run error.
func (a *Agent) interceptAgentStart(ctx context.Context) error {
	d := a.svc.extensions
	if d == nil {
		return nil
	}
	providerCtx := a.withAgentContext(ctx)
	payload := dispatch.AgentStartPayload{
		Model:     a.svc.prov.Name(),
		ToolCount: len(a.svc.tools.SchemasForContext(providerCtx)),
		SessionID: ParentSession(ctx),
	}
	result, err := d.Intercept(ctx, extension.PointAgentBeforeStart, &payload)
	if err != nil {
		return err
	}
	d.Event(extension.PointAgentBeforeStart, payload)
	if result.Blocked {
		return extensionBlockedError(extension.PointAgentBeforeStart, result.BlockReason)
	}
	return nil
}

// interceptContextPrepare runs context.prepare on the request message copy.
// The returned slice feeds only this provider request: the session log is
// never touched, so a replacement is invisible to the next turn (and to the
// prompt-cache prefix) — ephemerality is the cache contract.
func (a *Agent) interceptContextPrepare(ctx context.Context, messages []provider.Message) ([]provider.Message, error) {
	d := a.svc.extensions
	if d == nil {
		return messages, nil
	}
	payload := dispatch.ContextPayload{Messages: providerconv.MessagesToProtocol(messages)}
	result, err := d.Intercept(ctx, extension.PointContextPrepare, &payload)
	if err != nil {
		return nil, err
	}
	if result.Blocked {
		d.Event(extension.PointContextPrepare, payload)
		return nil, extensionBlockedError(extension.PointContextPrepare, result.BlockReason)
	}
	// The context slot owner gets the final say over the chain-walked payload.
	replaced, err := strategyReplaced(ctx, d, extension.SlotContext, extension.PointContextPrepare, &payload)
	if err != nil {
		return nil, err
	}
	d.Event(extension.PointContextPrepare, payload)
	if len(result.Applied) > 0 || replaced {
		return providerconv.MessagesFromProtocol(payload.Messages), nil
	}
	return messages, nil
}

// interceptProviderRequest runs provider.request on the fully assembled
// request (post CreatedAt-strip). A replacement is revalidated by the payload
// registry (tool parameter schemas must be JSON objects, messages/tools must
// be arrays) before it may substitute the request being sent.
func (a *Agent) interceptProviderRequest(ctx context.Context, req provider.Request) (provider.Request, error) {
	d := a.svc.extensions
	if d == nil {
		return req, nil
	}
	payload := dispatch.ProviderRequestPayload{Request: providerconv.RequestToProtocol(req, a.svc.prov.Name(), a.modelRef)}
	result, err := d.Intercept(ctx, extension.PointProviderRequest, &payload)
	if err != nil {
		return provider.Request{}, err
	}
	if result.Blocked {
		d.Event(extension.PointProviderRequest, payload)
		return provider.Request{}, extensionBlockedError(extension.PointProviderRequest, result.BlockReason)
	}
	// The provider_request slot owner gets the final say over the
	// chain-walked payload.
	replaced, err := strategyReplaced(ctx, d, extension.SlotProviderRequest, extension.PointProviderRequest, &payload)
	if err != nil {
		return provider.Request{}, err
	}
	d.Event(extension.PointProviderRequest, payload)
	if len(result.Applied) > 0 || replaced {
		return providerconv.RequestFromProtocol(payload.Request), nil
	}
	return req, nil
}

// interceptProviderResponse runs provider.response after the stream completed
// successfully, before the assistant turn is persisted. A replacement is
// persisted as the visible turn — the user's transcript and the model's own
// history on the next request. The live text/reasoning deltas already
// streamed to the frontend are not retroactively changed; the closing Message
// event and the session carry the replaced values. Session-level cache
// counters keep the provider's real usage (they were accumulated while
// streaming); a replaced Usage drives only this turn's Usage event and
// compaction decision. A block fails the turn with the redacted reason.
func (a *Agent) interceptProviderResponse(ctx context.Context, text, reasoning, signature string, calls []provider.ToolCall, usage *provider.Usage) (string, string, string, []provider.ToolCall, *provider.Usage, error) {
	d := a.svc.extensions
	if d == nil {
		return text, reasoning, signature, calls, usage, nil
	}
	payload := dispatch.ProviderResponsePayload{
		Text:      text,
		Reasoning: reasoning,
		Signature: signature,
		Calls:     providerconv.ToolCallsToProtocol(calls),
		Usage:     providerconv.UsageToProtocol(usage),
	}
	result, err := d.Intercept(ctx, extension.PointProviderResponse, &payload)
	if err != nil {
		return "", "", "", nil, nil, err
	}
	if result.Blocked {
		d.Event(extension.PointProviderResponse, payload)
		return "", "", "", nil, nil, extensionBlockedError(extension.PointProviderResponse, result.BlockReason)
	}
	// The provider_response slot owner gets the final say over the
	// chain-walked payload.
	replaced, err := strategyReplaced(ctx, d, extension.SlotProviderResponse, extension.PointProviderResponse, &payload)
	if err != nil {
		return "", "", "", nil, nil, err
	}
	d.Event(extension.PointProviderResponse, payload)
	if len(result.Applied) > 0 || replaced {
		return payload.Text, payload.Reasoning, payload.Signature,
			providerconv.ToolCallsFromProtocol(payload.Calls), providerconv.UsageFromProtocol(payload.Usage), nil
	}
	return text, reasoning, signature, calls, usage, nil
}

// interceptToolBefore runs tool.before after the host resolved and validated
// the concrete target. A block
// fails the call with the reason as the tool-result error (mirroring a
// PreToolUse hook block). A replacement substitutes the provider-visible name
// and arguments, but only after host revalidation — the arguments must decode
// as a JSON object and the name must still resolve in the registry — and the
// substituted call is then re-parsed so policy, permission, and evidence all
// see the call that will actually execute. An invalid replacement fails the
// call with a contract-violation error result.
func (a *Agent) interceptToolBefore(ctx context.Context, plan *toolCallPlan) (toolOutcome, bool) {
	d := a.svc.extensions
	if d == nil {
		return toolOutcome{}, false
	}
	payload := dispatch.ToolBeforePayload{Name: plan.call.Name, Arguments: plan.call.Arguments}
	result, err := d.Intercept(ctx, extension.PointToolBefore, &payload)
	if err != nil {
		msg := fmt.Sprintf("error: %v", err)
		return toolOutcome{output: msg, errMsg: firstLine(err.Error())}, true
	}
	d.Event(extension.PointToolBefore, payload)
	if result.Blocked {
		reason := extensionBlockReason(result.BlockReason)
		return toolOutcome{output: "blocked: " + reason, blocked: true, errMsg: "blocked by extension"}, true
	}
	if len(result.Applied) == 0 {
		return toolOutcome{}, false
	}
	plugin := result.Applied[len(result.Applied)-1]
	violation := func(detail string) (toolOutcome, bool) {
		msg := fmt.Sprintf("extension %s violated the intercept contract at %s: %s", plugin, extension.PointToolBefore, detail)
		return toolOutcome{output: "error: " + msg, errMsg: msg}, true
	}
	trimmed := strings.TrimSpace(payload.Arguments)
	if trimmed == "" || trimmed[0] != '{' {
		return violation("arguments must decode as a JSON object")
	}
	t, _, ambiguous := a.svc.tools.ResolveCall(payload.Name)
	if t == nil || len(ambiguous) > 0 {
		return violation(fmt.Sprintf("substituted tool name %q does not resolve in the registry", payload.Name))
	}
	plan.call.Name = payload.Name
	plan.call.Arguments = payload.Arguments
	return toolOutcome{}, false
}

// interceptExtensionPermission runs permission.decision at the gate point.
// The host decision is computed first and rides the payload; the extension
// ruling combines with it: allow overrides a host deny (the full-trust
// contract — the dispatcher records the audit note, surfaced here as a
// warning notice), deny or block overrides a host allow, continue leaves the
// host decision standing. allow is updated in place; early=true carries the
// blocked outcome.
func (a *Agent) interceptExtensionPermission(ctx context.Context, plan *toolCallPlan, allow *bool) (toolOutcome, bool) {
	d := a.svc.extensions
	if d == nil {
		return toolOutcome{}, false
	}
	hostDecision := "deny"
	if *allow {
		hostDecision = "allow"
	}
	payload := dispatch.PermissionPayload{
		Name:         plan.permName,
		Arguments:    string(plan.permArgs),
		ReadOnly:     plan.readOnly,
		HostDecision: hostDecision,
	}
	result, err := d.Intercept(ctx, extension.PointPermissionDecision, &payload)
	if err != nil {
		return toolOutcome{
			output:  fmt.Sprintf("blocked: %v", err),
			blocked: true,
			errMsg:  "blocked by extension permission policy",
		}, true
	}
	// The permission slot owner gets the final say after the chain walk. Its
	// effective rulings here are continue (the chain/host combination stands)
	// and block (veto); a replace adjusts only the payload observers see —
	// allow/deny remains the chain's terminal mechanism.
	if !result.Blocked {
		if serr := d.RunStrategy(ctx, extension.SlotPermission, extension.PointPermissionDecision, &payload); serr != nil {
			reason := serr.Error()
			var blockErr *dispatch.BlockError
			if errors.As(serr, &blockErr) {
				reason = extensionBlockReason(blockErr.Reason)
			}
			return toolOutcome{
				output:  "blocked: " + reason,
				blocked: true,
				errMsg:  "blocked by extension permission policy",
			}, true
		}
	}
	d.Event(extension.PointPermissionDecision, payload)
	for _, note := range result.Audit {
		a.svc.sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: note})
	}
	switch {
	case result.Blocked:
		reason := extensionBlockReason(result.BlockReason)
		return toolOutcome{
			output:  "blocked: " + reason,
			blocked: true,
			errMsg:  "blocked by extension permission policy",
		}, true
	case result.Permission != nil && !*result.Permission:
		return toolOutcome{
			output:  "blocked: denied by extension permission policy",
			blocked: true,
			errMsg:  "blocked by extension permission policy",
		}, true
	case result.Permission != nil && *result.Permission:
		*allow = true
	}
	return toolOutcome{}, false
}

// interceptToolAfter runs tool.after on the executed result. A replacement
// substitutes the visible result string and the error flag — clearing IsError
// converts a failed call into a success with the replaced text, setting it
// converts a success into an error result carrying the replaced text. A block
// (or a required extension's failure) converts the call to an error tool
// result with the reason; the tool itself already ran.
func (a *Agent) interceptToolAfter(ctx context.Context, call provider.ToolCall, result string, err error) (string, error) {
	d := a.svc.extensions
	if d == nil {
		return result, err
	}
	payload := dispatch.ToolAfterPayload{
		Name:      call.Name,
		Arguments: call.Arguments,
		Result:    result,
		IsError:   err != nil,
	}
	res, ierr := d.Intercept(ctx, extension.PointToolAfter, &payload)
	if ierr != nil {
		return "", ierr
	}
	d.Event(extension.PointToolAfter, payload)
	if res.Blocked {
		return "", errors.New(extensionBlockReason(res.BlockReason))
	}
	if len(res.Applied) > 0 {
		result = payload.Result
		switch {
		case payload.IsError && err == nil:
			err = errors.New("extension replaced this tool result with an error")
		case !payload.IsError:
			err = nil
		}
	}
	return result, err
}

// interceptCompactionPrepare runs compaction.prepare before the fold is
// archived and summarized, colocated with the PreCompact hook so the payload's
// Guidance is the hook-contributed guidance (plus any /compact focus text). A
// replacement's messages and guidance drive only this compaction pass; a
// block skips the pass with the reason surfaced through the caller's notice.
func (a *Agent) interceptCompactionPrepare(ctx context.Context, fold []provider.Message, guidance string) ([]provider.Message, string, error) {
	d := a.svc.extensions
	if d == nil {
		return fold, guidance, nil
	}
	payload := dispatch.CompactionPreparePayload{
		Messages: providerconv.MessagesToProtocol(fold),
		Guidance: guidance,
	}
	originalMessages, err := json.Marshal(payload.Messages)
	if err != nil {
		return nil, "", err
	}
	result, err := d.Intercept(ctx, extension.PointCompactionPrepare, &payload)
	if err != nil {
		return nil, "", err
	}
	if result.Blocked {
		d.Event(extension.PointCompactionPrepare, payload)
		return nil, "", extensionBlockedError(extension.PointCompactionPrepare, result.BlockReason)
	}
	// The compaction slot owner gets the final say over the chain-walked fold
	// and guidance.
	replaced, err := strategyReplaced(ctx, d, extension.SlotCompaction, extension.PointCompactionPrepare, &payload)
	if err != nil {
		return nil, "", err
	}
	d.Event(extension.PointCompactionPrepare, payload)
	if len(result.Applied) > 0 || replaced {
		preparedMessages, marshalErr := json.Marshal(payload.Messages)
		if marshalErr != nil {
			return nil, "", marshalErr
		}
		if bytes.Equal(preparedMessages, originalMessages) {
			return fold, payload.Guidance, nil
		}
		return providerconv.MessagesFromProtocol(payload.Messages), payload.Guidance, nil
	}
	return fold, guidance, nil
}

// interceptCompactionComplete runs compaction.complete after the summary is
// produced (including the mechanical-fold fallback), before it is written
// into the session. A replacement is persisted as the summary; a block skips
// the pass.
func (a *Agent) interceptCompactionComplete(ctx context.Context, summary string) (string, error) {
	d := a.svc.extensions
	if d == nil {
		return summary, nil
	}
	payload := dispatch.CompactionCompletePayload{Summary: summary}
	result, err := d.Intercept(ctx, extension.PointCompactionComplete, &payload)
	if err != nil {
		return "", err
	}
	if result.Blocked {
		d.Event(extension.PointCompactionComplete, payload)
		return "", extensionBlockedError(extension.PointCompactionComplete, result.BlockReason)
	}
	// The compaction slot owner gets the final say over the chain-walked
	// summary.
	replaced, err := strategyReplaced(ctx, d, extension.SlotCompaction, extension.PointCompactionComplete, &payload)
	if err != nil {
		return "", err
	}
	d.Event(extension.PointCompactionComplete, payload)
	if len(result.Applied) > 0 || replaced {
		return payload.Summary, nil
	}
	return summary, nil
}
