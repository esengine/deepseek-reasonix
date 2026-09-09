package provider

// ToolCallExchange is one unambiguous tool call/result pair from a single
// assistant turn. AssistantIndex and ResultIndex refer to the input transcript
// passed to WalkToolCallExchanges.
type ToolCallExchange struct {
	Call           ToolCall
	Result         Message
	AssistantIndex int
	ResultIndex    int
}

// WalkToolCallExchanges visits tool pairs from each assistant's immediately
// following contiguous, non-local result run; boundaries and orphans never pair.
// Unique non-empty call IDs match exactly one same-ID result, including out of order.
// Empty/duplicate call IDs require equal counts and pair by position; an interrupted
// placeholder invalidates that ambiguous batch. Returning false stops the walk.
func WalkToolCallExchanges(msgs []Message, visit func(ToolCallExchange) bool) {
	for assistantIndex := 0; assistantIndex < len(msgs); assistantIndex++ {
		assistant := msgs[assistantIndex]
		if assistant.LocalOnly || assistant.Role != RoleAssistant || len(assistant.ToolCalls) == 0 {
			continue
		}

		resultEnd := assistantIndex + 1
		for resultEnd < len(msgs) && msgs[resultEnd].Role == RoleTool && !msgs[resultEnd].LocalOnly {
			resultEnd++
		}
		results := msgs[assistantIndex+1 : resultEnd]
		if toolCallResultsAligned(assistant.ToolCalls, results) {
			if !walkPositionalToolCallExchanges(assistant.ToolCalls, results, assistantIndex, toolCallIDsAmbiguous(assistant.ToolCalls), visit) {
				return
			}
		} else if idDistinct(assistant.ToolCalls) {
			if !walkIDToolCallExchanges(assistant.ToolCalls, results, assistantIndex, visit) {
				return
			}
		} else if len(results) == len(assistant.ToolCalls) {
			if !walkPositionalToolCallExchanges(assistant.ToolCalls, results, assistantIndex, true, visit) {
				return
			}
		}

		// Results in this run cannot belong to another assistant turn.
		assistantIndex = resultEnd - 1
	}
}

func toolCallResultsAligned(calls []ToolCall, results []Message) bool {
	if len(calls) != len(results) {
		return false
	}
	for i, call := range calls {
		if results[i].ToolCallID != call.ID {
			return false
		}
	}
	return true
}

// toolCallIDsAmbiguous is allocation-free because it runs on the common,
// already-aligned path and tool batches are small. The slow, non-aligned path
// uses idDistinct's map instead of repeating this quadratic scan.
func toolCallIDsAmbiguous(calls []ToolCall) bool {
	for i, call := range calls {
		if call.ID == "" {
			return true
		}
		for j := 0; j < i; j++ {
			if calls[j].ID == call.ID {
				return true
			}
		}
	}
	return false
}

func walkPositionalToolCallExchanges(calls []ToolCall, results []Message, assistantIndex int, failBatchOnPlaceholder bool, visit func(ToolCallExchange) bool) bool {
	if failBatchOnPlaceholder {
		if !positionalToolCallResultsCompatible(calls, results) {
			return true
		}
		for _, result := range results {
			if result.Content == interruptedToolResult {
				return true
			}
		}
	}
	for callIndex, call := range calls {
		if results[callIndex].Content == interruptedToolResult {
			continue
		}
		if !visit(ToolCallExchange{
			Call:           call,
			Result:         results[callIndex],
			AssistantIndex: assistantIndex,
			ResultIndex:    assistantIndex + 1 + callIndex,
		}) {
			return false
		}
	}
	return true
}

// positionalToolCallResultsCompatible rejects structural evidence that an
// ambiguous batch was reordered or contains a replacement/orphan result. Empty
// fields remain compatible because gateways and old sessions can omit them.
func positionalToolCallResultsCompatible(calls []ToolCall, results []Message) bool {
	for i, call := range calls {
		result := results[i]
		if call.ID != "" && result.ToolCallID != "" && call.ID != result.ToolCallID {
			return false
		}
		if call.Name != "" && result.Name != "" && call.Name != result.Name {
			return false
		}
	}

	// Every unique non-empty call ID is a reliable positional anchor. Missing,
	// duplicated, or displaced results contradict ownership even when empty
	// positions cannot be compared directly.
	for callIndex, call := range calls {
		if call.ID == "" {
			continue
		}
		callCount, resultCount, resultIndex := 0, 0, -1
		for i := range calls {
			if calls[i].ID == call.ID {
				callCount++
			}
			if results[i].ToolCallID == call.ID {
				resultIndex = i
				resultCount++
			}
		}
		if callCount == 1 && (resultCount != 1 || resultIndex != callIndex) {
			return false
		}
	}
	return true
}

func walkIDToolCallExchanges(calls []ToolCall, results []Message, assistantIndex int, visit func(ToolCallExchange) bool) bool {
	type resultMatch struct {
		count int
		index int
	}
	matches := make(map[string]resultMatch, len(results))
	for i, result := range results {
		match := matches[result.ToolCallID]
		match.count++
		match.index = i
		matches[result.ToolCallID] = match
	}

	for _, call := range calls {
		match := matches[call.ID]
		if match.count != 1 {
			continue
		}
		if results[match.index].Content == interruptedToolResult {
			continue
		}
		if !visit(ToolCallExchange{
			Call:           call,
			Result:         results[match.index],
			AssistantIndex: assistantIndex,
			ResultIndex:    assistantIndex + 1 + match.index,
		}) {
			return false
		}
	}
	return true
}
