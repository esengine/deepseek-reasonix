package provider

import (
	"strconv"
	"testing"
)

func collectToolCallExchanges(msgs []Message) []ToolCallExchange {
	var out []ToolCallExchange
	WalkToolCallExchanges(msgs, func(exchange ToolCallExchange) bool {
		out = append(out, exchange)
		return true
	})
	return out
}

func TestWalkToolCallExchangesScopesRepeatedIDsToTheirTurns(t *testing.T) {
	msgs := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_0", Name: "read_file"}}},
		{Role: RoleTool, ToolCallID: "call_0", Name: "read_file", Content: "first"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_0", Name: "bash"}}},
		{Role: RoleTool, ToolCallID: "call_0", Name: "bash", Content: "second"},
	}

	got := collectToolCallExchanges(msgs)
	if len(got) != 2 {
		t.Fatalf("got %d exchanges, want 2: %+v", len(got), got)
	}
	if got[0].Call.Name != "read_file" || got[0].Result.Content != "first" || got[0].AssistantIndex != 0 || got[0].ResultIndex != 1 {
		t.Errorf("first exchange = %+v", got[0])
	}
	if got[1].Call.Name != "bash" || got[1].Result.Content != "second" || got[1].AssistantIndex != 2 || got[1].ResultIndex != 3 {
		t.Errorf("second exchange = %+v", got[1])
	}
}

func TestWalkToolCallExchangesMatchesUniqueIDsIndependently(t *testing.T) {
	msgs := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "a", Name: "first"},
			{ID: "b", Name: "second"},
			{ID: "c", Name: "missing"},
		}},
		{Role: RoleTool, ToolCallID: "b", Content: "B"},
		{Role: RoleTool, ToolCallID: "orphan", Content: "unused"},
		{Role: RoleTool, ToolCallID: "a", Content: "A-1"},
		{Role: RoleTool, ToolCallID: "a", Content: "A-2"},
	}

	got := collectToolCallExchanges(msgs)
	if len(got) != 1 {
		t.Fatalf("got %d exchanges, want only the unambiguous b result: %+v", len(got), got)
	}
	if got[0].Call.ID != "b" || got[0].Result.Content != "B" || got[0].ResultIndex != 1 {
		t.Errorf("exchange = %+v", got[0])
	}
}

func TestWalkToolCallExchangesFallsBackToPositionForAmbiguousCallIDs(t *testing.T) {
	tests := []struct {
		name  string
		calls []ToolCall
		tools []Message
	}{
		{
			name:  "empty IDs",
			calls: []ToolCall{{Name: "first"}, {Name: "second"}},
			tools: []Message{
				{Role: RoleTool, ToolCallID: "gateway-a", Content: "A"},
				{Role: RoleTool, ToolCallID: "gateway-b", Content: "B"},
			},
		},
		{
			name:  "duplicate IDs",
			calls: []ToolCall{{ID: "dup", Name: "first"}, {ID: "dup", Name: "second"}},
			tools: []Message{
				{Role: RoleTool, ToolCallID: "dup", Content: "A"},
				{Role: RoleTool, ToolCallID: "dup", Content: "B"},
			},
		},
		{
			name:  "mixed IDs with aligned unique anchor",
			calls: []ToolCall{{Name: "first"}, {ID: "known", Name: "second"}},
			tools: []Message{
				{Role: RoleTool, ToolCallID: "gateway-a", Name: "first", Content: "A"},
				{Role: RoleTool, ToolCallID: "known", Name: "second", Content: "B"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msgs := append([]Message{{Role: RoleAssistant, ToolCalls: tc.calls}}, tc.tools...)
			got := collectToolCallExchanges(msgs)
			if len(got) != 2 {
				t.Fatalf("got %d exchanges, want 2: %+v", len(got), got)
			}
			if got[0].Call.Name != "first" || got[0].Result.Content != "A" || got[1].Call.Name != "second" || got[1].Result.Content != "B" {
				t.Errorf("position pairing = %+v", got)
			}
		})
	}
}

func TestWalkToolCallExchangesFailsClosedOnIncompletePositionalTurn(t *testing.T) {
	msgs := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "dup"}, {ID: "dup"}}},
		{Role: RoleTool, ToolCallID: "dup", Content: "only one"},
	}
	if got := collectToolCallExchanges(msgs); len(got) != 0 {
		t.Fatalf("incomplete positional turn produced exchanges: %+v", got)
	}
}

func TestWalkToolCallExchangesFailsClosedOnContradictoryPositionalIdentity(t *testing.T) {
	tests := []struct {
		name    string
		calls   []ToolCall
		results []Message
	}{
		{
			name: "non-empty IDs disagree by position",
			calls: []ToolCall{
				{ID: "dup", Name: "read_file"},
				{ID: "dup", Name: "read_file"},
			},
			results: []Message{
				{Role: RoleTool, ToolCallID: "orphan", Name: "read_file", Content: "not owned by either call"},
				{Role: RoleTool, ToolCallID: "dup", Name: "read_file", Content: "error: denied"},
			},
		},
		{
			name: "duplicate IDs have crossed tool names",
			calls: []ToolCall{
				{ID: "dup", Name: "read_file"},
				{ID: "dup", Name: "bash"},
			},
			results: []Message{
				{Role: RoleTool, ToolCallID: "dup", Name: "bash", Content: "PASS"},
				{Role: RoleTool, ToolCallID: "dup", Name: "read_file", Content: "error: denied"},
			},
		},
		{
			name: "mixed IDs have a displaced unique anchor",
			calls: []ToolCall{
				{ID: "", Name: "read_file"},
				{ID: "known", Name: "read_file"},
			},
			results: []Message{
				{Role: RoleTool, ToolCallID: "known", Name: "read_file", Content: "known result"},
				{Role: RoleTool, ToolCallID: "", Name: "read_file", Content: "empty-ID result"},
			},
		},
		{
			name: "mixed IDs have a missing unique anchor",
			calls: []ToolCall{
				{ID: "", Name: "read_file"},
				{ID: "known", Name: "read_file"},
			},
			results: []Message{
				{Role: RoleTool, ToolCallID: "gateway", Name: "read_file", Content: "gateway result"},
				{Role: RoleTool, ToolCallID: "", Name: "read_file", Content: "empty-ID result"},
			},
		},
		{
			name: "mixed IDs have a duplicated unique anchor",
			calls: []ToolCall{
				{ID: "", Name: "read_file"},
				{ID: "known", Name: "read_file"},
			},
			results: []Message{
				{Role: RoleTool, ToolCallID: "known", Name: "read_file", Content: "first copy"},
				{Role: RoleTool, ToolCallID: "known", Name: "read_file", Content: "second copy"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msgs := append([]Message{{Role: RoleAssistant, ToolCalls: tc.calls}}, tc.results...)
			if got := collectToolCallExchanges(msgs); len(got) != 0 {
				t.Fatalf("contradictory positional batch produced exchanges: %+v", got)
			}
		})
	}
}

func TestWalkToolCallExchangesDoesNotCrossBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		boundary Message
	}{
		{name: "user", boundary: Message{Role: RoleUser, Content: "next"}},
		{name: "assistant", boundary: Message{Role: RoleAssistant, Content: "next"}},
		{name: "system", boundary: Message{Role: RoleSystem, Content: "next"}},
		{name: "local-only tool", boundary: Message{Role: RoleTool, ToolCallID: LocalOnlyToolID, LocalOnly: true}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msgs := []Message{
				{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "same", Name: "first"}}},
				tc.boundary,
				{Role: RoleTool, ToolCallID: "same", Content: "must stay orphaned"},
			}
			if got := collectToolCallExchanges(msgs); len(got) != 0 {
				t.Fatalf("paired across %s boundary: %+v", tc.name, got)
			}
		})
	}
}

func TestWalkToolCallExchangesSkipsLocalOnlyAssistantAndCanStop(t *testing.T) {
	msgs := []Message{
		{Role: RoleAssistant, LocalOnly: true, ToolCalls: []ToolCall{{ID: "hidden"}}},
		{Role: RoleTool, ToolCallID: "hidden", Content: "orphan"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a"}, {ID: "b"}}},
		{Role: RoleTool, ToolCallID: "a", Content: "A"},
		{Role: RoleTool, ToolCallID: "b", Content: "B"},
	}

	visits := 0
	WalkToolCallExchanges(msgs, func(exchange ToolCallExchange) bool {
		visits++
		return false
	})
	if visits != 1 {
		t.Fatalf("visitor called %d times after stopping, want 1", visits)
	}
}

func TestWalkToolCallExchangesSkipsLoadTimeInterruptedPlaceholders(t *testing.T) {
	tests := []struct {
		name  string
		calls []ToolCall
		want  int
	}{
		{
			name:  "unique IDs",
			calls: []ToolCall{{ID: "done", Name: "first"}, {ID: "missing", Name: "second"}},
			want:  1,
		},
		{
			name:  "duplicate IDs use position",
			calls: []ToolCall{{ID: "dup", Name: "first"}, {ID: "dup", Name: "second"}},
			want:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			loaded := NormalizeSessionMessages([]Message{
				{Role: RoleAssistant, ToolCalls: tc.calls},
				{Role: RoleTool, ToolCallID: tc.calls[0].ID, Name: tc.calls[0].Name, Content: "real result"},
			})
			got := collectToolCallExchanges(loaded)
			if len(got) != tc.want {
				t.Fatalf("normalized interrupted turn produced %d real exchanges, want %d: %+v\nnormalized: %+v", len(got), tc.want, got, loaded)
			}
			if tc.want == 0 {
				return
			}
			if got[0].Call.Name != "first" || got[0].Result.Content != "real result" {
				t.Errorf("exchange = %+v", got[0])
			}
		})
	}
}

func TestWalkToolCallExchangesHealthyLargeHistoryDoesNotAllocate(t *testing.T) {
	const turns = 10_000
	msgs := make([]Message, 0, turns*2)
	for i := 0; i < turns; i++ {
		id := "call_" + strconv.Itoa(i)
		msgs = append(msgs,
			Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: id, Name: "read_file"}}},
			Message{Role: RoleTool, ToolCallID: id, Name: "read_file", Content: "ok"},
		)
	}

	visits := 0
	visitor := func(ToolCallExchange) bool {
		visits++
		return true
	}
	allocs := testing.AllocsPerRun(5, func() {
		visits = 0
		WalkToolCallExchanges(msgs, visitor)
	})
	if visits != turns {
		t.Fatalf("visited %d exchanges, want %d", visits, turns)
	}
	if allocs != 0 {
		t.Fatalf("healthy aligned history allocated %.1f times, want 0", allocs)
	}
}

func BenchmarkWalkToolCallExchangesHealthyHistory(b *testing.B) {
	const turns = 10_000
	msgs := make([]Message, 0, turns*2)
	for i := 0; i < turns; i++ {
		id := "call_" + strconv.Itoa(i)
		msgs = append(msgs,
			Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: id}}},
			Message{Role: RoleTool, ToolCallID: id},
		)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		WalkToolCallExchanges(msgs, func(ToolCallExchange) bool { return true })
	}
}
