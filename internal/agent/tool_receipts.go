package agent

import (
	"encoding/json"

	"reasonix/internal/event"
	"reasonix/internal/evidence"
	"reasonix/internal/provider"
	"reasonix/internal/taskcontract"
	"reasonix/internal/tool"
)

// recordToolReceipts files the turn-scoped evidence for one executed call:
// always the model-visible call for audit, plus the real target's attributes
// for mutation/read classification when a proxy resolved elsewhere.
func (a *Agent) finalizeObservedToolReceipts(plan *toolCallPlan, result string, execution *tool.ShellExecution, err error) evidence.Receipt {
	a.observeAfterMutation(plan)
	plan.mutationAfterDone = true
	return a.recordToolReceipts(plan, result, execution, err)
}

// emitTodoResultPreview flips the todo_write card to done the moment the call
// executes without publishing a second terminal result. Batch ToolResult
// events still wait for the whole provider batch and remain the only terminal
// events observed by append-only sinks.
func (a *Agent) emitTodoResultPreview(call provider.ToolCall, output, args string) {
	if a == nil || a.svc.sink == nil {
		return
	}
	if args == "" {
		args = call.Arguments
	}
	a.svc.sink.Emit(event.Event{
		Kind: event.ToolResultPreview,
		Tool: event.Tool{ID: call.ID, Name: call.Name, Args: args, ReadOnly: true, Output: output},
	})
}

func (a *Agent) recordToolReceipts(plan *toolCallPlan, result string, execution *tool.ShellExecution, err error) evidence.Receipt {
	if a.task.ledger == nil {
		return evidence.Receipt{}
	}
	call := plan.call
	args := json.RawMessage(call.Arguments)
	// The session floor in force at write time is a fact of the write: it
	// rides the receipt so the per-turn contract replay re-derives the same
	// floor obligations even after the floor changes.
	floorStamp := a.turn.constraints.PolicyFloor.String()
	if floorStamp == taskcontract.PolicyFloorNone.String() {
		floorStamp = ""
	}
	// Every receipt carries the operation it belongs to, so a later citation
	// resolves an ID the host issued instead of a command string the model
	// retyped, and a repeated failure is attributable to one intended change.
	operationID := plan.operationID()
	switch {
	case call.Name == "complete_step":
		rec := evidence.ReceiptFromToolCall(call.Name, args, err == nil, plan.readOnly)
		a.stampReceiptDeliveryScope(&rec)
		rec.PolicyFloor = floorStamp
		rec.OperationID = operationID
		rec = a.task.ledger.Record(rec)
		a.commitToolReceipt(rec)
		if err == nil {
			a.advanceCanonicalTodo(rec.Step)
		}
		return rec
	case plan.evidenceName != call.Name:
		proxy := evidence.ReceiptFromToolCall(call.Name, args, err == nil, true)
		proxy.ToolCallID = call.ID
		a.task.ledger.Record(proxy)
		rec := evidence.ReceiptFromToolCall(plan.evidenceName, plan.evidenceArgs, err == nil, plan.readOnly)
		rec.ToolCallID = call.ID
		rec.Mutation = plan.effects.ContentMutation
		a.stampReceiptDeliveryScope(&rec)
		rec.PolicyFloor = floorStamp
		decorateExecutionReceipt(&rec, result, execution)
		rec.OperationID = operationID
		rec = a.task.ledger.Record(rec)
		a.commitToolReceipt(rec)
		a.recordOperationOutcome(plan, rec, err)
		return rec
	default:
		rec := evidence.ReceiptFromToolCall(call.Name, args, err == nil, plan.tool.ReadOnly())
		rec.ToolCallID = call.ID
		rec.Mutation = plan.effects.ContentMutation
		canonicalArgs := ""
		if err == nil && call.Name == "todo_write" {
			canonical := a.acceptTodoWrite(rec.Todos)
			rec.Todos = canonical
			canonicalArgs = canonicalTodoArgs(canonical)
		}
		a.stampReceiptDeliveryScope(&rec)
		rec.PolicyFloor = floorStamp
		decorateExecutionReceipt(&rec, result, execution)
		rec.OperationID = operationID
		rec = a.task.ledger.Record(rec)
		a.commitToolReceipt(rec)
		a.recordOperationOutcome(plan, rec, err)
		if err == nil && call.Name == "todo_write" {
			if len(rec.Todos) > 0 {
				a.turn.deliveryCriteriaEstablished = true
			}
			a.emitTodoResultPreview(call, result, canonicalArgs)
		}
		return rec
	}
}
