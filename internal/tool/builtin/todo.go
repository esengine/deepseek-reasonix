package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"reasonix/internal/evidence"
	"reasonix/internal/tool"
)

func init() { tool.RegisterBuiltin(todoWrite{}) }

// todoWrite records the agent's running task list. It has no host side effects —
// the full list lives in the call's args (the model re-sends it whole on every
// update), which a frontend renders as a checklist. Execute validates serial
// shape and stable identities, then acks with a count. Progress is not a
// delivery receipt: complete_step remains the optional evidence sign-off.
type todoWrite struct{}

type todoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm,omitempty"`
	Level      int    `json:"level,omitempty"`
	StepID     string `json:"step_id,omitempty"`
}

func (todoWrite) Name() string { return "todo_write" }

func (todoWrite) Description() string {
	return "Record and update a structured task list for the current work. Send the COMPLETE list every call — it replaces the previous one. Use it to plan multi-step work and show progress: keep exactly one item in_progress at a time, and flip an item to completed the moment it's done (don't batch completions). Skip it for trivial single-step tasks. The list is two-level: a `level` 0 item is a PHASE (a milestone) and the `level` 1 items after it are its concrete sub-steps; omit `level` (0) for a flat list. Each item has `content` (imperative, e.g. \"Add the parser\"), `status` (pending|in_progress|completed), `activeForm` (present-continuous shown while in progress, e.g. \"Adding the parser\"), optional `level` (0 phase | 1 sub-step), and `step_id` — an item's stable identity. COPY `step_id` VERBATIM for every item that already has one: it is how a completion stays attached to its step when you retitle it, insert a step above it, or reorder the list. Give a new item a fresh unique id (e.g. \"plan_step_07\"); never reuse or renumber an existing one."
}

func (todoWrite) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "todos":{
    "type":"array",
    "description":"The complete task list, in order. Replaces any previous list.",
    "items":{
      "type":"object",
      "properties":{
        "content":{"type":"string","description":"Imperative description of the task."},
        "status":{"type":"string","enum":["pending","in_progress","completed"],"description":"Task state. Keep at most one in_progress."},
        "activeForm":{"type":"string","description":"Present-continuous form shown while the task is in progress (e.g. \"Running tests\")."},
        "level":{"type":"integer","enum":[0,1],"description":"Nesting level: 0 = phase/milestone, 1 = a sub-step of the phase above it. Omit for a flat list."},
        "step_id":{"type":"string","description":"Stable identity for this item, e.g. \"plan_step_02\". Copy it verbatim from the item's previous entry so completions stay attached across retitles, insertions, and reordering; use a fresh unique id for a genuinely new item."}
      },
      "required":["content","status"]
    }
  }
},
"required":["todos"]
}`)
}

// ReadOnly is true: todo_write only records a list (no filesystem or process
// effect), so it never needs approval and stays available in plan mode — where
// laying out a plan as todos is exactly the point.
func (todoWrite) ReadOnly() bool { return true }

func (todoWrite) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Todos []todoItem `json:"todos"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	var done, active, pending int
	for i, t := range p.Todos {
		if t.Content == "" {
			return "", fmt.Errorf("todo %d: content is required", i+1)
		}
		if t.Level < 0 || t.Level > 1 {
			return "", fmt.Errorf("todo %d: invalid level %d (want 0 phase | 1 sub-step)", i+1, t.Level)
		}
		switch t.Status {
		case "completed":
			done++
		case "in_progress":
			active++
		case "pending", "":
			pending++
		default:
			return "", fmt.Errorf("todo %d: invalid status %q (want pending|in_progress|completed)", i+1, t.Status)
		}
	}
	if err := evidence.ValidateSerialTodos(toEvidenceTodos(p.Todos)); err != nil {
		return "", err
	}
	if err := verifyUniqueStepIDs(p.Todos); err != nil {
		return "", err
	}
	if !tool.HasPlanReplacementAuthorization(ctx) {
		if err := verifyTodoCurrentContinuity(ctx, p.Todos); err != nil {
			return "", err
		}
		if err := verifyStepIDsPreserved(ctx, p.Todos); err != nil {
			return "", err
		}
	}
	if err := verifyCompletedTodoPositions(ctx, p.Todos); err != nil {
		return "", err
	}
	return fmt.Sprintf("Todos updated: %d total — %d completed, %d in progress, %d pending.",
		len(p.Todos), done, active, pending), nil
}

// verifyUniqueStepIDs keeps a step id an identity: two items claiming the same
// id would make completion attribution ambiguous again, which is the whole
// problem ids exist to remove.
func verifyUniqueStepIDs(todos []todoItem) error {
	seen := make(map[string]int, len(todos))
	for i, todo := range todos {
		id := strings.TrimSpace(todo.StepID)
		if id == "" {
			continue
		}
		if prev, ok := seen[id]; ok {
			return fmt.Errorf("todo %d %q reuses step_id %q, already claimed by todo %d; give a new item its own id", i+1, todo.Content, id, prev+1)
		}
		seen[id] = i
	}
	return nil
}

// verifyStepIDsPreserved rejects a rewrite that keeps a step but drops the id it
// arrived with. Without this the model can silently return the list to
// title-and-position identity, which is exactly what a replan invalidates.
func verifyStepIDsPreserved(ctx context.Context, todos []todoItem) error {
	previous := todoBaseline(ctx)
	if len(previous) == 0 {
		return nil
	}
	next := toEvidenceTodos(todos)
	for _, todo := range previous {
		if todo.StepID == "" {
			continue
		}
		if _, ok := evidence.MatchStepID(todo.StepID, next); ok {
			continue
		}
		match, found := evidence.MatchTodoIdentity(todo, next)
		if !found || match.StepID != "" {
			continue
		}
		return fmt.Errorf("todo %d %q dropped its step_id %q; re-send it with step_id %q so its completion stays attached across retitles and reordering", match.Index, match.Content, todo.StepID, todo.StepID)
	}
	return nil
}

func verifyTodoCurrentContinuity(ctx context.Context, todos []todoItem) error {
	previous := todoBaseline(ctx)
	if len(previous) == 0 {
		return nil
	}
	next := toEvidenceTodos(todos)
	if len(next) == 0 {
		return fmt.Errorf("current todo cannot be cleared while the plan is active; get host approval to replace the plan")
	}
	for i, todo := range previous {
		if strings.TrimSpace(todo.Status) != "in_progress" {
			continue
		}
		match, found := evidence.MatchTodoIdentity(todo, next)
		if !found {
			return fmt.Errorf("current todo %d %q cannot be removed or replaced while it is in_progress; mark it completed or get host approval to replace the plan", i+1, todo.Content)
		}
		if match.Status == "pending" || match.Status == "" {
			return fmt.Errorf("current todo %d %q cannot move back to pending; keep it in_progress, mark it completed, or get host approval to replace the plan", i+1, todo.Content)
		}
	}
	return nil
}

func verifyCompletedTodoPositions(ctx context.Context, todos []todoItem) error {
	previous := todoBaseline(ctx)
	if len(previous) == 0 {
		return nil
	}
	for i, todo := range todos {
		if todo.Status != "completed" {
			continue
		}
		match, found := evidence.MatchTodoIdentity(toEvidenceTodo(todo), previous)
		if !found || match.Index != i+1 {
			return fmt.Errorf("completed todo %d %q cannot be inserted, duplicated, or reordered; preserve the completed prefix; canonical todo snapshot: %s", i+1, todo.Content, canonicalTodoSnapshot(previous))
		}
	}
	if len(evidence.IncompleteTodos(previous)) > 0 && !evidence.PreservesCompletedTodoPositions(previous, toEvidenceTodos(todos)) {
		return fmt.Errorf("completed task history cannot be removed, changed, or reordered while the plan is active; preserve every completed item at its original position; canonical todo snapshot: %s", canonicalTodoSnapshot(previous))
	}
	return nil
}

func canonicalTodoSnapshot(todos []evidence.TodoItem) string {
	snapshot, err := json.Marshal(todos)
	if err != nil {
		return "<unavailable>"
	}
	return string(snapshot)
}

func todoBaseline(ctx context.Context) []evidence.TodoItem {
	if ledger, ok := evidence.FromContext(ctx); ok {
		if previous, ok := ledger.LatestTodos(); ok && len(previous) > 0 {
			return previous
		}
	}
	previous, _ := evidence.TodoStateFromContext(ctx)
	return previous
}

func toEvidenceTodos(todos []todoItem) []evidence.TodoItem {
	out := make([]evidence.TodoItem, 0, len(todos))
	for _, t := range todos {
		out = append(out, toEvidenceTodo(t))
	}
	return out
}

func toEvidenceTodo(todo todoItem) evidence.TodoItem {
	return evidence.TodoItem{
		Content:    todo.Content,
		Status:     todo.Status,
		ActiveForm: todo.ActiveForm,
		Level:      todo.Level,
		StepID:     strings.TrimSpace(todo.StepID),
	}
}
