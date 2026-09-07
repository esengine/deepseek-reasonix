package agent

import (
	"strings"
	"testing"

	"reasonix/internal/evidence"
	"reasonix/internal/event"
)

type noticeSink struct{ notices []event.Event }

func newNoticeSink() *noticeSink { return &noticeSink{} }

func (s *noticeSink) Emit(e event.Event) {
	if e.Kind == event.Notice {
		s.notices = append(s.notices, e)
	}
}

// A signed-off step the canonical list does not know used to no-op silently
// (#9249): with a list stale from an earlier session generation, every sign-off
// mismatched, the model read the todo system as locked, and it narrated the
// workaround instead of repairing the list. The mismatch now says what
// happened and what repairs it.
func TestAdvanceCanonicalTodoUnmatchedStepEmitsStaleListNotice(t *testing.T) {
	sink := newNoticeSink()
	a := &Agent{
		svc:  agentServices{sink: sink},
		sess: sessionRuntime{todoState: []evidence.TodoItem{{StepID: "old-1", Content: "stale item", Status: "in_progress"}}},
	}

	a.advanceCanonicalTodo("new-step-9")

	if a.sess.todoState[0].Status != "in_progress" {
		t.Fatalf("unmatched sign-off must not mutate the list: %+v", a.sess.todoState[0])
	}
	if len(sink.notices) != 1 {
		t.Fatalf("notices = %d, want exactly one stale-list notice", len(sink.notices))
	}
	n := sink.notices[0]
	if !strings.Contains(n.Text, "todo_write") || !strings.Contains(n.Text, "stale") {
		t.Fatalf("notice does not guide the repair: %q", n.Text)
	}
	if !strings.Contains(n.Detail, "new-step-9") {
		t.Fatalf("notice does not name the unmatched step: %q", n.Detail)
	}
}

// A matching sign-off stays exactly as quiet as before — the notice is only
// for the dead end.
func TestAdvanceCanonicalTodoMatchedStepStaysSilent(t *testing.T) {
	sink := newNoticeSink()
	a := &Agent{
		svc:  agentServices{sink: sink},
		sess: sessionRuntime{todoState: []evidence.TodoItem{{Content: "sync branch", Status: "in_progress"}}},
	}

	a.advanceCanonicalTodo("sync branch")

	if a.sess.todoState[0].Status != "completed" {
		t.Fatalf("matched sign-off did not complete: %+v", a.sess.todoState[0])
	}
	if len(sink.notices) != 0 {
		t.Fatalf("matched sign-off emitted %d notices, want none", len(sink.notices))
	}
}

// An empty canonical list has nothing to be stale — still silent.
func TestAdvanceCanonicalTodoEmptyListStaysSilent(t *testing.T) {
	sink := newNoticeSink()
	a := &Agent{svc: agentServices{sink: sink}}

	a.advanceCanonicalTodo("anything")

	if len(sink.notices) != 0 {
		t.Fatalf("empty list emitted %d notices, want none", len(sink.notices))
	}
}
