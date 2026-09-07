package turnevent

import (
	"errors"
	"fmt"

	"reasonix/internal/event"
	"reasonix/internal/eventwire"
	"reasonix/internal/provider"
)

// reopenSource marks events Open synthesized for a turn the previous runtime
// never finished, so the evidence survives later reopens of the same file.
const reopenSource = "ledger_reopen"

// OrphanRecovery is what the ledger proved about the last turn when a runtime
// died mid-turn. It feeds the host's session-side recovery record; the ledger
// itself never replays a tool.
type OrphanRecovery struct {
	TurnID string
	Tools  []OrphanTool
}

// OrphanTool is one dispatched call the dead runtime never answered. Started
// reports whether it crossed the durable start barrier.
type OrphanTool struct {
	ID      string
	Name    string
	Started bool
}

// OrphanRecovery returns the reopen evidence for the most recent turn, or nil
// when that turn ended normally.
func (l *Ledger) OrphanRecovery() *OrphanRecovery {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active == "" {
		return nil
	}
	var out *OrphanRecovery
	for _, rec := range l.records {
		if rec.TurnID != l.active || rec.Source != reopenSource {
			continue
		}
		if out == nil {
			out = &OrphanRecovery{TurnID: l.active}
		}
		if rec.Kind == "tool_result" && rec.Event.Tool != nil {
			out.Tools = append(out.Tools, OrphanTool{
				ID: rec.Event.Tool.ID, Name: rec.Event.Tool.Name,
				Started: rec.Event.Tool.RunState == string(provider.ToolRunUnknown),
			})
		}
	}
	return out
}

// closeOrphanedTurnLocked answers every unanswered call with the state the
// start barrier proves, then lands the turn. A call that started may already
// have side effects, so the turn ends recovery_required, not interrupted.
func (l *Ledger) closeOrphanedTurnLocked(order []string, pending map[string]eventwire.Tool, started map[string]bool) error {
	if l.active == "" || l.terminal {
		return nil
	}
	requiresUser := false
	for _, id := range order {
		tool, ok := pending[id]
		if !ok {
			continue
		}
		state, why := provider.ToolRunCancelled, "cancelled: runtime restarted before the tool started"
		if started[id] {
			state, why = provider.ToolRunUnknown, "interrupted: runtime restarted after the tool started; outcome unknown"
			requiresUser = true
		}
		result := event.Event{Kind: event.ToolResult, TurnID: l.active, Source: reopenSource, Tool: event.Tool{
			ID: tool.ID, Name: tool.Name, ResolvedName: tool.ResolvedName,
			CapabilityID: tool.CapabilityID, ReadOnly: tool.ReadOnly, ParentID: tool.ParentID,
			RunState: string(state), Err: why,
		}}
		if err := l.appendReopenLocked(result, l.status); err != nil {
			return fmt.Errorf("recover orphaned tool %s in turn %s: %w", id, l.active, err)
		}
	}
	status := event.TurnInterrupted
	done := event.Event{Kind: event.TurnDone, TurnID: l.active, Source: reopenSource,
		Err: errors.New("runtime restarted before the turn reached a terminal event")}
	if requiresUser {
		status = event.TurnRecoveryRequired
		done.Recovery = &event.RecoveryStatus{Phase: "turn_recovery_required", Reason: "runtime_restart", TurnID: l.active, RequiresUser: true}
	}
	done.Status = status
	if err := l.appendReopenLocked(done, status); err != nil {
		return fmt.Errorf("recover orphaned turn %s: %w", l.active, err)
	}
	return nil
}

func (l *Ledger) appendReopenLocked(e event.Event, status event.TurnStatus) error {
	_, ok, err := l.appendLocked(e, status)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("append rejected")
	}
	return nil
}
