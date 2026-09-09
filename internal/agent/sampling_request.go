package agent

import (
	"context"
	"encoding/json"
	"strings"

	"reasonix/internal/provider"
)

// samplingRequest is a once-prepared, frozen provider request for one model
// round. All stream retries replay this exact payload — no synthetic recovery
// messages, no schema reorder, no previous_response_id drift from failed attempts.
type samplingRequest struct {
	req provider.Request
}

func isEmptyStreamResult(text, reasoning string, calls []provider.ToolCall, responsesItems []json.RawMessage, serverSearch []provider.ServerSearchCall) bool {
	return strings.TrimSpace(text) == "" &&
		strings.TrimSpace(reasoning) == "" &&
		len(calls) == 0 &&
		len(responsesItems) == 0 &&
		len(serverSearch) == 0
}

// modelInputMessages derives the stable provider-visible view from durable
// storage. Tool Content is the first-visible bounded result; RawContent stays
// local and is available only through the explicit session result reader.
func modelInputMessages(msgs []provider.Message) []provider.Message {
	return provider.ModelMessages(msgs)
}

// normalizeModelRequestMessages is shared by ordinary sampling and compaction
// replay so their cacheable prefix has the same role projection and metadata
// cleanup. Interceptors deliberately remain outside this helper.
func (a *Agent) normalizeModelRequestMessages(msgs []provider.Message) []provider.Message {
	requestMessages := a.providerProjectionMessages(modelInputMessages(msgs))
	// ModelMessages intentionally has a zero-copy fast path for clean input.
	// Detach before removing local metadata from the request-only representation.
	requestMessages = append([]provider.Message(nil), requestMessages...)
	for i := range requestMessages {
		requestMessages[i].CreatedAt = 0
		if requestMessages[i].Role == provider.RoleUser {
			requestMessages[i].Content = reTrailingExecutionPolicy.ReplaceAllString(requestMessages[i].Content, "")
		}
	}
	return requestMessages
}

func (a *Agent) streamProviderRequest(ctx context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	ch, err := a.svc.prov.Stream(ctx, req)
	if err != nil {
		if limit := provider.AsOutputLimitError(err); !provider.ManagedRecovery(ctx) && limit != nil && req.MaxTokens > limit.MaxOutputTokens {
			a.learnOutputBudget(limit.MaxOutputTokens)
			retryReq := req
			retryReq.MaxTokens = limit.MaxOutputTokens
			return a.svc.prov.Stream(ctx, retryReq)
		}
		return nil, err
	}
	// HTTP-level output-limit errors are returned before a stream channel is
	// created by SendWithRetry. Preserve the original channel directly so
	// cancellation and live chunk timing remain unchanged.
	return ch, nil
}

// prepareSamplingRequest freezes one model-round request (preflight + interceptors).
// Output budgets are resolved only here and never change the compact_ratio
// trigger. Physical overflow may attempt at most one recovery summary.
func (a *Agent) prepareSamplingRequest(ctx context.Context) (samplingRequest, error) {
	frozen, err := a.buildSamplingRequest(ctx, CompactionTriggerPressure)
	if err != nil {
		return samplingRequest{}, err
	}
	if err := a.applyAdmissionToRequest(&frozen.req); err != nil {
		// One-shot physical overflow recovery. Do not loop.
		startProjectionVersion := a.currentProjectionVersion()
		if _, perr := a.contextManager().Prepare(ctx, ContextPreparePolicy{
			Trigger: CompactionTriggerOverflow,
			Force:   true,
		}); perr != nil {
			return samplingRequest{}, err
		}
		if a.currentProjectionVersion() <= startProjectionVersion {
			return samplingRequest{}, err
		}
		rebuilt, rerr := a.buildSamplingRequest(ctx, CompactionTriggerPressure)
		if rerr != nil {
			return samplingRequest{}, rerr
		}
		if aerr := a.applyAdmissionToRequest(&rebuilt.req); aerr != nil {
			return samplingRequest{}, aerr
		}
		shape := a.requestCalibrationShape(rebuilt.req)
		a.sess.output.activeReqShape.Store(&shape)
		return samplingRequest{req: freezeProviderRequest(rebuilt.req)}, nil
	}
	shape := a.requestCalibrationShape(frozen.req)
	a.sess.output.activeReqShape.Store(&shape)
	return samplingRequest{req: freezeProviderRequest(frozen.req)}, nil
}

func (a *Agent) buildSamplingRequest(ctx context.Context, trigger string) (samplingRequest, error) {
	// CreatedAt is durable UI metadata, not model input. Strip it from the
	// transport copy so wall-clock differences never invalidate the provider's
	// prompt-cache prefix (and custom providers cannot accidentally send it).
	prepared, err := a.contextManager().Prepare(ctx, ContextPreparePolicy{Trigger: trigger})
	if err != nil {
		return samplingRequest{}, err
	}
	requestMessages := a.normalizeModelRequestMessages(prepared.Messages)
	// context.prepare: extensions may rewrite the message copy feeding THIS
	// request. The session log is never touched — the replacement is
	// ephemeral, so the next request starts from the unmodified history.
	requestMessages, err = a.interceptContextPrepare(ctx, requestMessages)
	if err != nil {
		return samplingRequest{}, err
	}
	req := provider.Request{
		Messages:       requestMessages,
		Tools:          a.providerToolSchemas(),
		MaxTokens:      a.maxOutputTokens,
		Temperature:    provider.OptionalTemperature(a.temperature),
		ResponseFormat: responseFormatFromRequest(ctx),
		EffortOverride: a.effortOverrideForRequest(),
	}
	if provider.NativeToolSearchEnabled(a.svc.prov) {
		req.ToolSearch = &provider.ToolSearch{Enabled: true}
	}
	// provider.request: the fully assembled request gets one last ruling
	// (revalidated by the payload registry) before it goes on the wire.
	req, err = a.interceptProviderRequest(ctx, req)
	if err != nil {
		return samplingRequest{}, err
	}
	return samplingRequest{req: req}, nil
}

// providerProjectionMessages applies provider-specific role compatibility to a
// request copy. Projection sidecars retain logical user-turn boundaries so
// explicit range compression can continue to resolve anchors across calls.
func (a *Agent) providerProjectionMessages(msgs []provider.Message) []provider.Message {
	if a != nil {
		strongCutoff := a.sess.reasoningReplayStrongProjection
		if strongCutoff > 0 && a.strictAlternatingRoles {
			// The cutoff is measured after role coalescing on the repaired
			// request, so apply the same outbound shape before slicing it.
			msgs = coalesceProjectionUserRuns(msgs)
		}
		if strongCutoff > 0 {
			// A repaired thinking-400 conversation keeps the stripped
			// projection only for the history that caused the rejection.
			resolvedCutoff := resolveReasoningReplayPrefix(msgs, strongCutoff, a.sess.reasoningReplayStrongProjectionAnchor)
			if resolvedCutoff > 0 {
				if repaired, changed := provider.ProjectReasoningStrippedMessagesPrefix(a.svc.prov, msgs, resolvedCutoff); changed {
					msgs = a.replayRecoveryFacts(msgs[:resolvedCutoff], repaired)
				}
			} else {
				// The canonical shape no longer contains the repair anchor
				// (for example after rewind). Do not silently disable all
				// provider projection; re-arm from the current history.
				a.sess.clearReasoningReplayStrongProjection()
				if repaired, changed := provider.ProjectReplaySafeMessages(a.svc.prov, msgs); changed {
					msgs = repaired
				}
			}
		} else if repaired, changed := provider.ProjectReplaySafeMessages(a.svc.prov, msgs); changed {
			msgs = repaired
		}
		if a.strictAlternatingRoles && a.sess.reasoningReplayStrongProjection <= 0 {
			return coalesceProjectionUserRuns(msgs)
		}
	}
	return msgs
}

// freezeProviderRequest deep-copies the provider-visible request surface so
// retries share identical messages, tools order, temperature, and format.
func freezeProviderRequest(req provider.Request) provider.Request {
	out := req
	if len(req.Messages) > 0 {
		out.Messages = append([]provider.Message(nil), req.Messages...)
		for i := range out.Messages {
			out.Messages[i].ThinkingBlocks = append([]provider.ThinkingBlock(nil), out.Messages[i].ThinkingBlocks...)
			if len(out.Messages[i].ToolCalls) > 0 {
				out.Messages[i].ToolCalls = append([]provider.ToolCall(nil), out.Messages[i].ToolCalls...)
			}
			if len(out.Messages[i].Images) > 0 {
				out.Messages[i].Images = append([]string(nil), out.Messages[i].Images...)
			}
			if len(out.Messages[i].ResponsesItems) > 0 {
				items := make([]json.RawMessage, len(out.Messages[i].ResponsesItems))
				for j, item := range out.Messages[i].ResponsesItems {
					items[j] = append(json.RawMessage(nil), item...)
				}
				out.Messages[i].ResponsesItems = items
			}
			if len(out.Messages[i].ServerSearch) > 0 {
				searches := make([]provider.ServerSearchCall, len(out.Messages[i].ServerSearch))
				for j, search := range out.Messages[i].ServerSearch {
					searches[j] = search
					if len(search.Results) > 0 {
						searches[j].Results = append([]provider.ServerSearchHit(nil), search.Results...)
					}
					if len(search.Raw) > 0 {
						searches[j].Raw = append(json.RawMessage(nil), search.Raw...)
					}
				}
				out.Messages[i].ServerSearch = searches
			}
		}
	}
	if len(req.Tools) > 0 {
		out.Tools = make([]provider.ToolSchema, len(req.Tools))
		for i, schema := range req.Tools {
			out.Tools[i] = schema
			if len(schema.Parameters) > 0 {
				out.Tools[i].Parameters = append(json.RawMessage(nil), schema.Parameters...)
			}
		}
	}
	if req.Temperature != nil {
		t := *req.Temperature
		out.Temperature = &t
	}
	if req.ResponseFormat != nil {
		rf := *req.ResponseFormat
		out.ResponseFormat = &rf
	}
	return out
}
