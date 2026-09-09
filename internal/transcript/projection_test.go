package transcript

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"reasonix/internal/event"
	"reasonix/internal/eventwire"
	"reasonix/internal/turnevent"
)

var testIdentity = Identity{SessionID: "session", HeadID: "head", RuntimeEpoch: "runtime", RewriteEpoch: 1}

func projectEvent(t *testing.T, p *Projection, seq uint64, e event.Event) {
	t.Helper()
	w := eventwire.ToWire(e)
	status := event.TurnInProgress
	if e.Kind == event.TurnDone {
		status = event.TurnCompleted
	}
	if err := p.Apply(turnevent.Envelope{SessionID: "session", RuntimeEpoch: "runtime", TurnID: "turn", Sequence: seq, Kind: w.Kind, Status: status, Event: w}); err != nil {
		t.Fatal(err)
	}
}

func snapshot(t *testing.T, p *Projection) Snapshot {
	t.Helper()
	s, err := p.Snapshot(PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestProjectionSnapshotCoverageMatchesEveryStreamCut(t *testing.T) {
	p, err := NewProjection(testIdentity, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	events := []event.Event{
		{Kind: event.UserMessage, MessageID: "user", Text: "question"},
		{Kind: event.StreamAttempt, MessageID: "failed", AttemptID: "failed", StreamAttempt: event.StreamAttemptInfo{ID: "failed", Action: event.StreamAttemptBegin}},
		{Kind: event.Text, MessageID: "failed", AttemptID: "failed", Text: "rejected"},
		{Kind: event.Message, MessageID: "failed", AttemptID: "failed", Text: "rejected full response"},
		{Kind: event.StreamAttempt, MessageID: "failed", AttemptID: "failed", StreamAttempt: event.StreamAttemptInfo{ID: "failed", Action: event.StreamAttemptDiscard}},
		{Kind: event.StreamAttempt, MessageID: "answer", AttemptID: "answer", StreamAttempt: event.StreamAttemptInfo{ID: "answer", Action: event.StreamAttemptBegin}},
		{Kind: event.Reasoning, MessageID: "answer", AttemptID: "answer", Text: "think"},
		{Kind: event.Text, MessageID: "answer", AttemptID: "answer", Text: "same"},
		{Kind: event.Text, MessageID: "answer", AttemptID: "answer", Text: " answer"},
		{Kind: event.Message, MessageID: "answer", AttemptID: "answer", Text: "same answer", Reasoning: "think"},
		{Kind: event.StreamAttempt, MessageID: "answer", AttemptID: "answer", StreamAttempt: event.StreamAttemptInfo{ID: "answer", Action: event.StreamAttemptCommit}},
		{Kind: event.ToolDispatch, MessageID: "answer", Tool: event.Tool{ID: "call", Name: "read_file", Args: `{}`}},
		{Kind: event.ToolResult, MessageID: "answer", Tool: event.Tool{ID: "call", Name: "read_file", Output: "result"}},
		{Kind: event.Message, MessageID: "second", Text: "same answer"},
		{Kind: event.TurnDone},
	}
	var cuts []Snapshot
	cuts = append(cuts, snapshot(t, p))
	for i, e := range events {
		projectEvent(t, p, uint64(i+1), e)
		cut := snapshot(t, p)
		if cut.CoveredThroughSeq != uint64(i+1) {
			t.Fatalf("cut %d has coverage %d", i, cut.CoveredThroughSeq)
		}
		cuts = append(cuts, cut)
	}
	want := cuts[len(cuts)-1].Records
	if len(want) != 4 || want[1].Message.Content != "same answer" || want[3].Message.Content != "same answer" {
		t.Fatalf("final projection: %+v", want)
	}
	for cut, state := range cuts {
		var baseline []Message
		for _, record := range state.Records {
			baseline = append(baseline, record.Message)
		}
		restored, err := NewProjection(testIdentity, baseline, state.CoveredThroughSeq)
		if err != nil {
			t.Fatalf("cut %d: %v", cut, err)
		}
		for i := cut; i < len(events); i++ {
			projectEvent(t, restored, uint64(i+1), events[i])
		}
		if got := snapshot(t, restored).Records; !reflect.DeepEqual(got, want) {
			t.Fatalf("cut %d changed the final projection\ngot: %#v\nwant: %#v", cut, got, want)
		}
	}
}

func TestProjectionRejectsWrongIdentityAndGapWithoutChangingSnapshot(t *testing.T) {
	p, _ := NewProjection(testIdentity, nil, 0)
	before := snapshot(t, p)
	for _, envelope := range []turnevent.Envelope{
		{SessionID: "another", RuntimeEpoch: "runtime", Sequence: 1},
		{SessionID: "session", RuntimeEpoch: "old-runtime", Sequence: 1},
		{SessionID: "session", RuntimeEpoch: "runtime", Sequence: 2},
	} {
		if err := p.Apply(envelope); err == nil {
			t.Fatal("invalid envelope accepted")
		}
		if after := snapshot(t, p); !reflect.DeepEqual(after, before) {
			t.Fatal("rejected event changed projection")
		}
	}
}

func TestProjectionPagingContentAndImmutability(t *testing.T) {
	body := strings.Repeat("你好🧪", 20000)
	rows := []Message{{MessageID: "u", Role: "user", Content: "question"}, {MessageID: "a", Role: "assistant", Content: body, ToolCalls: []ToolCall{{ID: "call", Name: "write_file", Arguments: body}}}}
	p, err := NewProjection(testIdentity, rows, 0)
	if err != nil {
		t.Fatal(err)
	}
	rows[1].ToolCalls[0].Name = "caller mutated input"
	s, err := p.Snapshot(PageRequest{Records: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasOlder || s.Before != 1 || len(s.Records) != 1 || len(s.Records[0].Refs) != 2 {
		t.Fatalf("bounded page: %+v", s)
	}
	if s.Records[0].Message.ToolCalls[0].Name != "write_file" {
		t.Fatal("input alias retained")
	}
	for _, ref := range s.Records[0].Refs {
		var full strings.Builder
		for offset := 0; ; {
			chunk, err := p.Content(ContentRequest{ContentRef: ref, Offset: offset})
			if err != nil {
				t.Fatal(err)
			}
			if !utf8.ValidString(chunk.Data) || len(chunk.Data) > contentChunkBytes {
				t.Fatal("invalid content chunk")
			}
			full.WriteString(chunk.Data)
			if chunk.Done {
				break
			}
			offset = chunk.NextOffset
		}
		if full.String() != body {
			t.Fatal("content was truncated or duplicated")
		}
	}
	s.Records[0].Message.ToolCalls[0].Name = "caller mutated snapshot"
	if next := snapshot(t, p); next.Records[1].Message.ToolCalls[0].Name != "write_file" {
		t.Fatal("snapshot alias retained")
	}
	older, err := p.Snapshot(PageRequest{SnapshotID: s.SnapshotID, Before: s.Before})
	if err != nil || len(older.Records) != 1 || older.Records[0].ID != "m:u" {
		t.Fatalf("older page: %+v %v", older, err)
	}
	projectEvent(t, p, 1, event.Event{Kind: event.Text, MessageID: "b", Text: "new"})
	retained, err := p.Snapshot(PageRequest{SnapshotID: s.SnapshotID, Before: s.Before})
	if err != nil || retained.Stale || len(retained.Records) != 1 || retained.Records[0].ID != "m:u" {
		t.Fatal("stream mutation invalidated immutable page")
	}
	retainedChunk, err := p.Content(ContentRequest{ContentRef: s.Records[0].Refs[0]})
	if err != nil || retainedChunk.Stale || retainedChunk.Data == "" {
		t.Fatal("stream mutation invalidated immutable content")
	}
	for seq := uint64(2); seq <= 4; seq++ {
		projectEvent(t, p, seq, event.Event{Kind: event.Text, MessageID: "b", Text: "more"})
		snapshot(t, p)
	}
	stale, err := p.Snapshot(PageRequest{SnapshotID: s.SnapshotID, Before: s.Before})
	if err != nil || !stale.Stale || len(stale.Records) != 0 {
		t.Fatal("old page was combined with new state")
	}
	chunk, err := p.Content(ContentRequest{ContentRef: s.Records[0].Refs[0]})
	if err != nil || !chunk.Stale {
		t.Fatal("old content ref did not become stale")
	}
}

func TestProjectionConcurrentSnapshotNeverClaimsUnappliedText(t *testing.T) {
	p, _ := NewProjection(testIdentity, nil, 0)
	var wg sync.WaitGroup
	wg.Go(func() {
		for seq := uint64(1); seq <= 100; seq++ {
			projectEvent(t, p, seq, event.Event{Kind: event.Text, MessageID: "a", Text: "x"})
		}
	})
	for range 100 {
		s := snapshot(t, p)
		if len(s.Records) > 0 && len(s.Records[0].Message.Content) != int(s.CoveredThroughSeq) {
			t.Fatalf("coverage %d does not cover text", s.CoveredThroughSeq)
		}
		if _, err := json.Marshal(s); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
