package event

import (
	"errors"
	"testing"
	"time"
)

func TestMessageIdentitySurvivesCoalescingAndAttemptBoundary(t *testing.T) {
	inner := &coalesceRecordSink{}
	coalesced := Coalesce(inner, time.Hour)
	first := WithMessageIdentity(coalesced, "message-1", "attempt-1")
	second := WithMessageIdentity(coalesced, "message-2", "attempt-2")
	first.Emit(Event{Kind: Reasoning, Text: "a"})
	first.Emit(Event{Kind: Reasoning, Text: "b"})
	first.Emit(Event{Kind: Reasoning, Text: "c"})
	second.Emit(Event{Kind: Reasoning, Text: "d"})
	if err := EmitChecked(second, Event{Kind: ToolDispatch}); err != nil {
		t.Fatal(err)
	}
	got := inner.snapshot()
	if len(got) != 4 || got[0].Text != "a" || got[1].Text != "bc" || got[2].Text != "d" {
		t.Fatalf("coalesced output: %+v", got)
	}
	for i, e := range got {
		messageID, attemptID := "message-1", "attempt-1"
		if i >= 2 {
			messageID, attemptID = "message-2", "attempt-2"
		}
		if e.MessageID != messageID || e.AttemptID != attemptID {
			t.Fatalf("event %d lost ownership: %+v", i, e)
		}
	}
}

func TestMessageIdentityPreservesCheckedFailureAndExistingOwner(t *testing.T) {
	want := errors.New("durability failure")
	inner := &checkedRecordSink{err: want}
	sink := WithMessageIdentity(inner, "outer", "outer-attempt")
	if err := EmitChecked(sink, Event{Kind: ToolDispatch}); !errors.Is(err, want) {
		t.Fatalf("durability failure = %v", err)
	}
	sink.Emit(Event{Kind: Message, MessageID: "inner", AttemptID: "inner-attempt"})
	sink.Emit(Event{Kind: TurnDone})
	got := inner.snapshot()
	if len(got) != 2 || got[0].MessageID != "inner" || got[0].AttemptID != "inner-attempt" || got[1].MessageID != "" {
		t.Fatalf("identity ownership changed: %+v", got)
	}
}
