package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/provider"
	"reasonix/internal/readcoord"
	"reasonix/internal/tool"
)

func (a *Agent) readPipelineActive() bool {
	return a != nil && a.turn.readShadow.enabled && !a.legacyImplicitFullReads
}

func (a *Agent) legacyReadFinal(state *turnRuntime) (string, *IncompleteReadError) {
	if a.readPipelineActive() {
		return "", nil
	}
	return state.incompleteReads.blockFinal()
}

func (a *Agent) issueReadContinuation(tr readcoord.Transition, env tool.ReadResultEnvelope) {
	if a.reads.tasks == nil || env.Source.Snapshot == "" {
		return
	}
	next := 0
	if len(tr.Missing) > 0 {
		next = tr.Missing[0].Start
	} else if len(tr.Covered) > 0 {
		next = tr.Covered[len(tr.Covered)-1].End
	}
	end := 0
	if ob, ok := a.turn.readShadow.coord.Get(tr.Key); ok && len(ob.Requirement.Ranges) > 0 {
		end = ob.Requirement.Ranges[len(ob.Requirement.Ranges)-1].End
	}
	cursor := tool.ReadCursor{ReadID: tr.Key, Path: env.Source.CanonicalPath, Snapshot: env.Source.Snapshot, SessionID: a.reads.tasks.sessionID, RunGen: a.reads.tasks.generation, NextStart: next, RequestEnd: end}
	cursor.Binding = a.reads.tasks.binding
	env.NextCursor = tool.EncodeReadCursor(cursor)
	a.reads.tasks.remember(tr.Key, env)
}

func tReadObserver(a *Agent, call provider.ToolCall) (tool.ModelTextObserver, bool) {
	t, _, _ := a.svc.tools.ResolveCall(call.Name)
	o, ok := t.(tool.ModelTextObserver)
	return o, ok
}

func (a *Agent) readTimeRemaining(id string) time.Duration {
	remaining := readcoord.DefaultPolicy().MaxActiveTime
	if a.readPipelineActive() && id != "" {
		if ob, ok := a.turn.readShadow.coord.Get(id); ok {
			remaining -= ob.ActiveTime
		}
	}
	return max(time.Millisecond, remaining)
}

func (a *Agent) gateReadOperation(_ context.Context, plan *toolCallPlan) (string, bool) {
	if !a.readPipelineActive() {
		return a.turn.incompleteReads.gate(plan)
	}
	if plan.evidenceName != "read_file" {
		return "", false
	}
	path := readPathArg(plan.execArgs)
	if resolver, ok := plan.execTool.(tool.ReadPathResolver); ok {
		resolved, err := resolver.ResolveReadPath(plan.execArgs)
		if err != nil {
			return "error: " + err.Error(), true
		}
		path = resolved
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.writeWorkspaceRoot, path)
	}
	path = filepath.Clean(path)
	for _, ob := range a.turn.readShadow.coord.Snapshot() {
		if ob.State.Terminal() || (ob.Key != plan.readTaskID && ob.Scope.CanonicalPath != path) {
			continue
		}
		if ob.State == readcoord.StateBlocked || ob.State == readcoord.StateNeedsScope {
			args, _ := parseReadFileArgs(plan.execArgs)
			if plan.readTaskID == "" && args.LimitExplicit {
				return "", false
			}
			return "blocked: automatic read is paused; inspect an explicit range or report the unresolved full-file requirement", true
		}
		plan.readTaskID = ob.Key
		return "", false
	}
	return "", false
}

// readContinuation owns the execution decision. Status rendering is only a
// projection of this state; the legacy machine is used solely in rollback mode.
//
// A NeedsMore coverage gap is advisory: tool rounds may get a continuation
// hint, but a final answer is not refused. Only an already-attached host Stop
// (budget / policy stall) pauses the run.
func (a *Agent) readContinuation(final bool) (string, error) {
	if final {
		var paused []string
		for _, ob := range a.turn.readShadow.coord.Snapshot() {
			if ob.State.Terminal() || ob.Stop == nil {
				continue
			}
			paused = append(paused, fmt.Sprintf("%s: %s; %s", ob.Scope.CanonicalPath, ob.Stop.Detail, ob.Stop.Recovery))
		}
		if len(paused) > 0 {
			return "", &IncompleteReadError{Reason: strings.Join(paused, "; ")}
		}
		return "", nil
	}
	for _, ob := range a.turn.readShadow.coord.Snapshot() {
		if ob.State.Terminal() || ob.Stop != nil {
			continue
		}
		a.reads.tasks.mu.Lock()
		task := a.reads.tasks.byID[ob.Key]
		a.reads.tasks.mu.Unlock()
		if !task.issued || task.snapshot == "" {
			tr, _ := a.turn.readShadow.coord.Narrow(ob.Key, readcoord.Block{Code: "no_cursor", Detail: "no verifiable next page is available", Recovery: "inspect a narrower range and report that the full review remains incomplete"})
			a.emitReadStatus(tr, tool.ReadResultEnvelope{Intent: ob.Requirement.Intent})
			continue
		}
		path := task.argumentPath
		if path == "" {
			path = task.path
		}
		args, _ := json.Marshal(map[string]any{"path": path, "cursor": tool.EncodeReadCursor(task.cursor)})
		prefix := "Continue the outstanding read with read_file "
		if _, owed := a.turn.readShadow.pivots[ob.Key]; owed {
			prefix = "The last two pages added no content. Change strategy: use the host's exact next window instead of repeating the previous page. Call read_file "
			delete(a.turn.readShadow.pivots, ob.Key)
		}
		return prefix + string(args) + ". Independent work may continue. A partial window is enough when it covers the work; do not claim a complete review until the requirement is satisfied.", nil
	}
	return "", nil
}

func (a *Agent) closeReadStatuses() {
	if !a.readPipelineActive() {
		return
	}
	for _, ob := range a.turn.readShadow.coord.Snapshot() {
		if tr, ok := a.turn.readShadow.coord.Cancel(ob.Key); ok {
			a.emitReadStatus(tr, tool.ReadResultEnvelope{Intent: ob.Requirement.Intent})
		}
	}
}

func (a *Agent) observeFailedRead(call provider.ToolCall, out toolOutcome) {
	target, name, _ := a.svc.tools.ResolveCall(call.Name)
	if name != "read_file" {
		return
	}
	path := readPathArg(json.RawMessage(call.Arguments))
	if resolver, ok := target.(tool.ReadPathResolver); ok {
		if resolved, err := resolver.ResolveReadPath(json.RawMessage(call.Arguments)); err == nil {
			path = resolved
		}
	}
	for _, ob := range a.turn.readShadow.coord.Snapshot() {
		if ob.State.Terminal() || ob.Stop != nil || (ob.Key != out.readTaskID && ob.Scope.CanonicalPath != path) {
			continue
		}
		if strings.Contains(out.errMsg, "source changed") || strings.Contains(out.errMsg, "deadline exceeded") {
			tr, _ := a.turn.readShadow.coord.Fail(ob.Key, readcoord.Block{Code: "read_failed", Detail: out.errMsg, Recovery: "inspect a fresh explicit range; the previous full read is incomplete"})
			a.emitReadStatus(tr, tool.ReadResultEnvelope{Intent: ob.Requirement.Intent})
		} else {
			// Rejected/repeated calls deliver no source lines. They still count
			// toward the same no-progress ladder instead of bypassing its bounds.
			a.observeReadShadow(tool.ReadResultEnvelope{ReadID: ob.Key, Source: tool.ReadResultSource{CanonicalPath: ob.Scope.CanonicalPath, Snapshot: ob.Version}, Intent: ob.Requirement.Intent}, out.readActiveMillis)
		}
	}
}

// Re-evaluate requirements against the batch's frozen evidence boundary. A
// historical failure is not itself a live obligation, and a same-batch read
// cannot clear one before the provider receives that read.
func (a *Agent) outstandingReadEvidence(ctx context.Context, boundary uint64) []string {
	s := &a.turn.evidenceBlocked
	s.mu.Lock()
	calls := maps.Clone(s.calls)
	s.mu.Unlock()
	for path, call := range calls {
		target, _, _ := a.svc.tools.ResolveCall(call.Name)
		if a.checkOperationEvidence(ctx, call, target, boundary).Satisfied {
			s.mu.Lock()
			if latest := s.calls[path]; latest.ID == call.ID && latest.Arguments == call.Arguments {
				delete(s.paths, path)
				delete(s.calls, path)
			}
			s.mu.Unlock()
		}
	}
	// Only writers already blocked for missing path evidence count here; open
	// readcoord obligations must not freeze undeclared tools like git commit.
	return s.snapshot()
}
