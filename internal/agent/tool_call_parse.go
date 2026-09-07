package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/evidence"
	"reasonix/internal/tool"
)

// parseToolCall resolves the canonical tool, rejects ambiguity/unknown tools,
// and applies repeat-success and stale-anchor guards.
func (a *Agent) parseToolCall(ctx context.Context, turn *turnRuntime, plan *toolCallPlan) (toolOutcome, bool) {
	t, canonicalName, ambiguous := a.svc.tools.ResolveCall(plan.call.Name)
	if len(ambiguous) > 0 {
		msg := fmt.Sprintf("ambiguous MCP tool reference %q; use one of: %s", plan.call.Name, strings.Join(ambiguous, ", "))
		return toolOutcome{
			output: "error: " + msg,
			errMsg: msg,
		}, true
	}
	if t == nil {
		if server, ok := completedMCPConnect(a.svc.tools, plan.call.Name); ok {
			return toolOutcome{
				output: fmt.Sprintf("MCP server %q is connected; its real tools are now available", server),
			}, true
		}
		return toolOutcome{
			output: fmt.Sprintf("error: unknown tool %q", plan.call.Name),
			errMsg: fmt.Sprintf("unknown tool %q", plan.call.Name),
		}, true
	}
	recoveryCall := plan.call
	recoveryCall.Name = canonicalName
	if out, handled := recoverPreviousWrite(ctx, turn, recoveryCall, t); handled {
		return out, true
	}
	if out, handled := recoverPreviousUnknown(turn, recoveryCall, t); handled {
		return out, true
	}

	if out, blocked := a.repeatedSuccessBlock(plan.call, t); blocked {
		return toolOutcome{
			output:  out,
			blocked: true,
			errMsg:  loopGuardBlockErrMsg,
		}, true
	}
	if out, blocked := a.repeatedFailureBlock(ctx, plan.call, t); blocked {
		return toolOutcome{
			output:  out,
			blocked: true,
			errMsg:  loopGuardBlockErrMsg,
		}, true
	}
	if out, blocked := a.staleAnchorEditBlock(ctx, plan.call); blocked {
		return toolOutcome{
			output:  out,
			blocked: true,
			errMsg:  "blocked: fresh read required",
		}, true
	}
	plan.tool = t
	plan.canonicalName = canonicalName
	plan.permName = canonicalName
	plan.permArgs = json.RawMessage(plan.call.Arguments)
	plan.execTool = t
	plan.execArgs = json.RawMessage(plan.call.Arguments)
	plan.evidenceName = canonicalName
	plan.evidenceArgs = json.RawMessage(plan.call.Arguments)
	plan.readOnly = t.ReadOnly()
	if canonicalName == "bash" {
		var permissionReader bool
		plan.effects, permissionReader = evidence.ClassifyBashToolCall(plan.execArgs)
		if permissionReader {
			// Carry the resolved read-only classification without changing the schema.
			plan.readOnly = true
			plan.resolvedMeta = &tool.ResolvedCall{TargetName: canonicalName, ReadOnly: true}
		}
	} else {
		plan.effects = evidence.ClassifyToolCall(plan.evidenceName, plan.evidenceArgs, plan.readOnly)
	}
	return toolOutcome{}, false
}
