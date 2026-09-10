package agent

// The host's canonical task list: the state that outlives a turn because it
// never rides in the prompt, so a later turn still sees an unfinished plan.

import (
	"encoding/json"
	"strconv"
	"strings"

	"reasonix/internal/evidence"
)

// SeedTodoState initializes the canonical task list from a host-generated
// starter list, such as an approved plan. A new host seed replaces stale state
// from earlier work so complete_step matches the plan the UI just displayed.
func (a *Agent) SeedTodoState(todos []evidence.TodoItem) {
	if len(todos) == 0 {
		return
	}
	a.setTodoState(todos)
	a.clearDeferredTodoCompletions()
}

// ReplaceTodoState mirrors a host-generated todo list into the canonical state.
// It is used when the host, rather than the model, owns the full state transition.
func (a *Agent) ReplaceTodoState(todos []evidence.TodoItem) {
	a.setTodoState(todos)
	a.clearDeferredTodoCompletions()
	a.recordTodoState(a.CanonicalTodoState())
}

// CanonicalTodoState returns a copy of the host-reconstructed task list.
func (a *Agent) CanonicalTodoState() []evidence.TodoItem {
	a.sess.todoMu.Lock()
	defer a.sess.todoMu.Unlock()
	return append([]evidence.TodoItem(nil), a.sess.todoState...)
}

// CurrentTaskTodoState returns only the latest successful todo_write retained
// in the current evidence ledger. Unlike CanonicalTodoState, it never falls
// back to a prior user turn.
func (a *Agent) CurrentTaskTodoState() []evidence.TodoItem {
	if a == nil || a.task.ledger == nil {
		return nil
	}
	todos, ok := a.task.ledger.LatestTodos()
	if !ok {
		return nil
	}
	return append([]evidence.TodoItem(nil), todos...)
}

// consumeTodoOnlyReadinessMarkerIfResolved retires a pending final-readiness
// marker whose only gap was unfinished todos once the canonical list shows
// every item completed, so a reload no longer replays the stale wrap-up card.
// In-turn consumption stays with beginFinalReadinessRecovery (next user turn).
func (a *Agent) consumeTodoOnlyReadinessMarkerIfResolved() {
	if a == nil || a.sess.conversation == nil {
		return
	}
	a.sess.todoMu.Lock()
	state := append([]evidence.TodoItem(nil), a.sess.todoState...)
	a.sess.todoMu.Unlock()
	if len(state) == 0 || len(evidence.IncompleteTodos(state)) > 0 {
		return
	}
	marker := a.pendingFinalReadinessRecovery()
	if marker == nil || len(marker.Missing) == 0 {
		return
	}
	for _, id := range marker.Missing {
		if id != "todo" {
			return
		}
	}
	a.sess.conversation.ConsumeFinalReadinessRecovery()
}

func (a *Agent) incompleteCanonicalTodos() ([]evidence.TodoStepMatch, bool) {
	a.sess.todoMu.Lock()
	defer a.sess.todoMu.Unlock()
	if len(a.sess.todoState) == 0 {
		return nil, false
	}
	return evidence.IncompleteTodos(a.sess.todoState), true
}

func (a *Agent) hasIncompleteCanonicalCriteria() bool {
	a.sess.todoMu.Lock()
	defer a.sess.todoMu.Unlock()
	return len(a.sess.todoState) > 0 && len(evidence.IncompleteTodos(a.sess.todoState)) > 0
}

type deferredTodoCompletion struct {
	level int
}

func (a *Agent) clearDeferredTodoCompletions() {
	if a == nil {
		return
	}
	a.sess.todoMu.Lock()
	a.sess.deferredTodoCompletions = nil
	a.sess.todoMu.Unlock()
}

// runtimeTodoKey is intentionally not a persisted/public identity protocol:
// step_id is preferred, and id-less items use only level plus normalized
// content for the lifetime of this Agent.
func runtimeTodoKey(todo evidence.TodoItem) (string, bool) {
	if id := strings.TrimSpace(todo.StepID); id != "" {
		return "id:" + id, true
	}
	content := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(todo.Content)), ""))
	if content == "" {
		return "", false
	}
	return "text:" + strconv.Itoa(todo.Level) + ":" + content, true
}

func runtimeTodoIndex(todos []evidence.TodoItem) (map[string]int, map[string]bool) {
	index := make(map[string]int, len(todos))
	duplicates := make(map[string]bool)
	for i, todo := range todos {
		key, ok := runtimeTodoKey(todo)
		if !ok {
			continue
		}
		if _, exists := index[key]; exists {
			duplicates[key] = true
			delete(index, key)
			continue
		}
		if duplicates[key] {
			continue
		}
		index[key] = i
	}
	return index, duplicates
}

func (a *Agent) pruneDeferredTodoCompletionsLocked() {
	if len(a.sess.deferredTodoCompletions) == 0 {
		return
	}
	index, duplicates := runtimeTodoIndex(a.sess.todoState)
	for key, deferred := range a.sess.deferredTodoCompletions {
		i, ok := index[key]
		if !ok || duplicates[key] || canonicalTodoStatus(a.sess.todoState[i].Status) == "completed" || a.sess.todoState[i].Level != deferred.level {
			delete(a.sess.deferredTodoCompletions, key)
		}
	}
	if len(a.sess.deferredTodoCompletions) == 0 {
		a.sess.deferredTodoCompletions = nil
	}
}

// acceptTodoWrite updates the runtime canonical list and records only the
// narrow repaired completions. The caller keeps the original tool arguments;
// this return value is for receipts/events that need the canonical view.
func (a *Agent) acceptTodoWrite(todos []evidence.TodoItem) []evidence.TodoItem {
	if a == nil {
		return evidence.NormalizeSerialTodos(todos)
	}
	previous := a.CanonicalTodoState()
	canonical := evidence.NormalizeSerialTodos(todos)
	var deferred []evidence.TodoItem
	if evidence.ValidateSerialTodos(todos) != nil {
		if repaired, pending, ok := evidence.RepairSerialTodoUpdateWithDeferred(previous, todos); ok {
			canonical = repaired
			deferred = pending
		}
	}

	a.sess.todoMu.Lock()
	a.sess.todoState = append([]evidence.TodoItem(nil), canonical...)
	if len(canonical) == 0 {
		a.sess.deferredTodoCompletions = nil
	} else {
		for _, candidate := range deferred {
			key, ok := runtimeTodoKey(candidate)
			if !ok {
				continue
			}
			index, duplicates := runtimeTodoIndex(a.sess.todoState)
			i, found := index[key]
			if !found || duplicates[key] || canonicalTodoStatus(a.sess.todoState[i].Status) != "pending" {
				continue
			}
			if a.sess.deferredTodoCompletions == nil {
				a.sess.deferredTodoCompletions = make(map[string]deferredTodoCompletion)
			}
			a.sess.deferredTodoCompletions[key] = deferredTodoCompletion{level: a.sess.todoState[i].Level}
		}
		a.pruneDeferredTodoCompletionsLocked()
	}
	result := append([]evidence.TodoItem(nil), a.sess.todoState...)
	a.sess.todoMu.Unlock()
	return result
}

func nextSerialTodoIndex(todos []evidence.TodoItem) int {
	for i, todo := range todos {
		if canonicalTodoStatus(todo.Status) != "in_progress" {
			continue
		}
		if sub, ok := evidence.FirstUnfinishedSubStep(todos, i); ok && sub >= 0 {
			return sub
		}
		return i
	}
	return -1
}

func (a *Agent) consumeDeferredTodoCompletionsLocked() []string {
	if len(a.sess.todoState) == 0 || len(a.sess.deferredTodoCompletions) == 0 {
		return nil
	}
	working := append([]evidence.TodoItem(nil), a.sess.todoState...)
	deferred := make(map[string]deferredTodoCompletion, len(a.sess.deferredTodoCompletions))
	for key, item := range a.sess.deferredTodoCompletions {
		deferred[key] = item
	}
	consumed := make([]string, 0)
	for range working {
		index := nextSerialTodoIndex(working)
		if index < 0 {
			break
		}
		key, ok := runtimeTodoKey(working[index])
		item, exists := deferred[key]
		if !ok || !exists || item.level != working[index].Level {
			break
		}
		before := append([]evidence.TodoItem(nil), working...)
		if !evidence.AdvanceSerialTodo(working, index) || evidence.ValidateSerialTodos(working) != nil {
			working = before
			break
		}
		delete(deferred, key)
		consumed = append(consumed, key)
	}
	a.sess.todoState = working
	if len(deferred) == 0 {
		a.sess.deferredTodoCompletions = nil
	} else {
		a.sess.deferredTodoCompletions = deferred
	}
	return consumed
}

func canonicalTodoArgs(todos []evidence.TodoItem) string {
	if todos == nil {
		todos = []evidence.TodoItem{}
	}
	args, err := json.Marshal(struct {
		Todos []evidence.TodoItem `json:"todos"`
	}{Todos: todos})
	if err != nil {
		return ""
	}
	return string(args)
}

// recordTodoState logs the host-advanced list as a synthetic todo_write receipt
// so the per-turn final gate (which reads the ledger's latest todo_write) sees
// the advance — the model no longer has to re-send a todo_write to mark the
// completion. It bypasses the todo_write tool, so the completion-transition
// guard never runs on it.
func (a *Agent) recordTodoState(todos []evidence.TodoItem) {
	if a.task.ledger == nil {
		return
	}
	args, err := json.Marshal(map[string]any{"todos": todos})
	if err != nil {
		return
	}
	a.task.ledger.Record(evidence.ReceiptFromToolCall("todo_write", json.RawMessage(args), true, true))
}

func canonicalTodoStatus(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "pending"
	}
	return s
}
