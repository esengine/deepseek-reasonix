package agent

import (
	"context"
	"fmt"
	"strings"

	"reasonix/internal/event"
	"reasonix/internal/i18n"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func (a *Agent) emitIncompleteReadNotice(code, text, detail string) {
	if a == nil || a.svc.sink == nil {
		return
	}
	level := event.LevelWarn
	if code == event.NoticeCodeReadCompleted || code == event.NoticeCodeReadStrategyProgress || code == event.NoticeCodeReadStrategyResolved {
		level = event.LevelInfo
	}
	a.svc.sink.Emit(event.Event{Kind: event.Notice, Level: level, Code: code, Text: text, Detail: detail})
}

// resolveIncompleteReadToolRoundBoundary runs after every tool result is stored,
// commits validated receipts, and counts at most one violation per model round.
func (a *Agent) resolveIncompleteReadToolRoundBoundary(ctx context.Context, state *turnRuntime, usage *provider.Usage) (cont bool, err error, handled bool) {
	if a.readPipelineActive() {
		instruction, err := a.readContinuation(false)
		if instruction != "" {
			a.sess.conversation.Add(HostGeneratedUserMessage(a.withTurnPreferences(instruction)))
		}
		if ctx.Err() != nil {
			return false, ctx.Err(), true
		}
		return instruction != "", err, instruction != "" || err != nil
	}
	round := state.incompleteReads.finishToolRound()
	for _, observed := range round.record {
		a.recordModelTextObservationValue(observed)
	}
	for _, readID := range round.resolvedIDs {
		a.emitIncompleteReadNotice(event.NoticeCodeReadStrategyResolved, i18n.M.ReadStrategyResolved, "read_id="+readID)
	}
	if round.pause != nil {
		a.contextManager().ObserveUsage(usage)
		return false, round.pause, true
	}
	instruction := state.incompleteReads.nextInstruction()
	if instruction != "" {
		a.sess.conversation.Add(HostGeneratedUserMessage(a.withTurnPreferences(instruction)))
		a.emitIncompleteReadNotice(event.NoticeCodeReadContinuationRequired, i18n.M.ReadContinuationRequired, "read continuation instruction appended")
	}
	if ctx.Err() != nil {
		a.recordInterruptedDisplay("", "", nil, true, ctx.Err(), state.workDurationMs())
		return false, ctx.Err(), true
	}
	if instruction == "" {
		if len(round.resolvedIDs) > 0 {
			a.contextManager().ObserveUsage(usage)
			return true, nil, true
		}
		return false, nil, false
	}
	a.contextManager().ObserveUsage(usage)
	if axis, detail := a.task.budget.exceeded(a.taskBudgetLimit(ctx)); axis != "" {
		return false, &IncompleteReadError{Reason: fmt.Sprintf("the %s budget ended while a required read_file continuation was still pending: %s", axis, detail)}, true
	}
	return true, nil, true
}

func (a *Agent) boundIncompleteReadAwareResult(plan *toolCallPlan, result string) (body, truncMsg, original string, readObserver bool) {
	if plan == nil {
		return result, "", "", false
	}
	if plan.evidenceName == "read_file" {
		_, readObserver = plan.runTool.(tool.ModelTextObserver)
		if !readObserver {
			_, readObserver = plan.execTool.(tool.ModelTextObserver)
		}
	}
	_, structuredReader := plan.execTool.(tool.ReadEnvelopeProvider)
	if a.readPipelineActive() && structuredReader {
		// Structured readers are deduplicated only in the ordered finalizer,
		// against original text present in the frozen model request.
		body, truncMsg = truncateToolOutputFor(result, plan.call.Name, plan.call.ID)
		if body != result {
			original = result
		}
	} else {
		body, truncMsg, original = a.boundProviderVisibleResult(result, plan.call.Name, plan.call.ID)
	}
	readArgs, _ := parseReadFileArgs(plan.execArgs)
	if a.readPipelineActive() && plan.evidenceName == "read_file" && (plan.readTaskID != "" || readArgs.fullRead()) {
		budget := a.readAutoRecoveryBudgetFor()
		if budget.known && a.estimatedReadResultTokens(body) > budget.maxTokens {
			lo, hi := 0, len(body)
			for lo < hi {
				mid := lo + (hi-lo+1)/2
				if a.estimatedReadResultTokens(body[:mid]) <= budget.maxTokens {
					lo = mid
				} else {
					hi = mid - 1
				}
			}
			end := strings.LastIndexByte(body[:lo], '\n')
			if end < 0 {
				body = ""
			} else {
				body = body[:end+1]
			}
			original = result
			truncMsg = ""
		}
	}
	return body, truncMsg, original, readObserver
}

func deferredIncompleteReadOutcome(plan *toolCallPlan, rawOutput string, readObserver, visibleFull bool) *incompleteReadDeferred {
	if plan == nil || !(readObserver || plan.evidenceName == "session_tool_result" || (plan.evidenceName == "grep" && plan.incompleteReadRoot != "")) {
		return nil
	}
	return &incompleteReadDeferred{plan: plan, rawOutput: rawOutput, readObserver: readObserver, visibleFull: visibleFull}
}

func readStrategyPreview(raw, readID string, totalTokens, limitTokens int) string {
	const maxPreview = 8 * 1024
	headLimit := min(len(raw), maxPreview-1024)
	head := snapToRuneBoundary(raw, 0, max(0, headLimit))
	if newline := strings.LastIndexByte(head, '\n'); newline >= 0 {
		head = head[:newline+1]
	}
	return fmt.Sprintf("%s\n[PARTIAL READ: read_id=%s; complete content does not fit the dynamic context budget (estimated_tokens=%d budget_tokens=%d). This prefix is not whole-file evidence. Prefer grep plus narrow read_file windows; independent work may continue when the visible prefix is enough.]\n", head, readID, totalTokens, limitTokens)
}

// finalizeIncompleteReadOutcome is called by executeBatch.finalize in provider
// order, never from parallel execution goroutines.
func (a *Agent) finalizeIncompleteReadOutcome(ctx context.Context, deferred *incompleteReadDeferred, out *toolOutcome) {
	if a.readPipelineActive() {
		if deferred != nil && deferred.plan != nil && out != nil && deferred.plan.evidenceName == "read_file" {
			// Parallel readers are clipped again in the ordered finalizer, after
			// earlier deliveries consume their share of the common context.
			body, _, original, _ := a.boundIncompleteReadAwareResult(deferred.plan, deferred.rawOutput)
			out.output, out.rawOutput = body, original
			out.truncated, out.truncMsg = original != "", ""
		}
		return
	}
	if a == nil || deferred == nil || deferred.plan == nil || out == nil {
		return
	}
	plan := deferred.plan
	var transition incompleteReadTransition
	switch plan.evidenceName {
	case "read_file":
		observed, ok := modelTextObservationFor(plan, deferred.rawOutput)
		if ok && observed.Snapshot == "" {
			// The legacy owner stamps the same source version the pipeline owner
			// does, so the evidence gate never reads its windows as unversioned
			// and blocks every later write to the file.
			if env, valid := a.finalizedReadEnvelope(ctx, plan.call, *out); valid {
				observed.Snapshot = env.Source.Snapshot
			}
		}
		transition = a.turn.incompleteReads.observeReadFile(plan, deferred.rawOutput, out.output, observed, ok, a.estimatedReadResultTokens(deferred.rawOutput), a.readAutoRecoveryBudgetFor())
	case "session_tool_result":
		transition = a.turn.incompleteReads.observeResultPage(plan, deferred.rawOutput, a.retainedReadPageMatches(plan, deferred.rawOutput))
	case "grep":
		transition = a.turn.incompleteReads.observeStrategySearch(plan, deferred.rawOutput, deferred.visibleFull)
	default:
		return
	}
	for _, observed := range transition.record {
		a.recordModelTextObservationValue(observed)
	}
	if transition.strategyRequired {
		out.output = readStrategyPreview(deferred.rawOutput, transition.readID, transition.totalTokens, transition.limitTokens)
		out.rawOutput = deferred.rawOutput
		out.truncated = true
		out.truncMsg = fmt.Sprintf(i18n.M.ReadRestrictedStrategyFmt, transition.totalTokens, transition.limitTokens)
		a.emitIncompleteReadNotice(event.NoticeCodeReadStrategyRequired, i18n.M.ReadStrategyRequired, fmt.Sprintf("read_id=%s bytes=%d tokens=%d budget_tokens=%d", transition.readID, transition.totalBytes, transition.totalTokens, transition.limitTokens))
	}
	if transition.detected {
		a.emitIncompleteReadNotice(event.NoticeCodeIncompleteReadDetected, i18n.M.IncompleteReadDetected, "read_id="+transition.readID)
	}
	if transition.strategyProgress {
		a.emitIncompleteReadNotice(event.NoticeCodeReadStrategyProgress, i18n.M.ReadStrategyProgress, "read_id="+transition.readID)
	}
	if transition.localSafetyPaged {
		a.emitIncompleteReadNotice(event.NoticeCodeReadLocalSafetyPaged, i18n.M.ReadLocalSafetyPaged, "read_id="+transition.readID)
	}
	if transition.completed {
		a.emitIncompleteReadNotice(event.NoticeCodeReadCompleted, i18n.M.ReadCompleted, "read_id="+transition.readID)
	}
}
