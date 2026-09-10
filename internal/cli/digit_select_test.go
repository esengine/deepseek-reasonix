package cli

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/checkpoint"
	"reasonix/internal/control"
	"reasonix/internal/event"
)

func digitKey(d rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: d, Text: string(d)} }

func newDigitTestAsk(multi bool) event.Ask {
	return event.Ask{
		ID: "ask-1",
		Questions: []event.AskQuestion{{
			ID:     "q1",
			Prompt: "Pick",
			Multi:  multi,
			Options: []event.AskOption{
				{Label: "Alpha"}, {Label: "Beta"}, {Label: "Gamma"},
			},
		}},
	}
}

func TestNumberKeyIndex(t *testing.T) {
	cases := []struct {
		key   string
		limit int
		idx   int
		ok    bool
	}{
		{"1", 3, 0, true},
		{"3", 3, 2, true},
		{"9", 9, 8, true},
		{"4", 3, 0, false},
		{"0", 3, 0, false},
		{"a", 3, 0, false},
		{"10", 3, 0, false},
		{"ctrl+1", 3, 0, false},
	}
	for _, c := range cases {
		idx, ok := numberKeyIndex(c.key, c.limit)
		if ok != c.ok || (ok && idx != c.idx) {
			t.Errorf("numberKeyIndex(%q, %d) = (%d, %v), want (%d, %v)", c.key, c.limit, idx, ok, c.idx, c.ok)
		}
	}
}

func TestChooserDigitSelectsAndSubmitsSingle(t *testing.T) {
	m := newTestChatTUI()
	m.ctrl = control.New(control.Options{})
	m.chooser = newChooser(newDigitTestAsk(false))

	next, _ := m.handleChooserKey(digitKey('2'))
	got := next.(chatTUI)
	if got.chooser != nil {
		t.Fatal("digit on a single-question single-select prompt should submit and close it")
	}
}

func TestChooserDigitTogglesMultiSelect(t *testing.T) {
	m := newTestChatTUI()
	m.chooser = newChooser(newDigitTestAsk(true))

	next, _ := m.handleChooserKey(digitKey('2'))
	got := next.(chatTUI)
	if got.chooser == nil {
		t.Fatal("digit on a multi-select question should not close the prompt")
	}
	if !got.chooser.sel[0][1] {
		t.Fatal("digit 2 should toggle option 2 on")
	}
	if got.chooser.cursor != 1 {
		t.Fatalf("digit 2 should move the cursor to the toggled row, got %d", got.chooser.cursor)
	}

	next, _ = got.handleChooserKey(digitKey('2'))
	got = next.(chatTUI)
	if got.chooser.sel[0][1] {
		t.Fatal("pressing the digit again should toggle option 2 back off")
	}
}

func TestChooserDigitOutOfRangeIsIgnored(t *testing.T) {
	m := newTestChatTUI()
	m.chooser = newChooser(newDigitTestAsk(false))

	next, _ := m.handleChooserKey(digitKey('7'))
	got := next.(chatTUI)
	if got.chooser == nil {
		t.Fatal("an out-of-range digit should leave the prompt open")
	}
}

func TestClearConfirmDigitCancel(t *testing.T) {
	m := newTestChatTUI()
	m.clearConfirm = &clearConfirm{}

	next, _ := m.handleClearConfirmKey(digitKey('2'))
	if next.(chatTUI).clearConfirm != nil {
		t.Fatal("digit 2 should cancel the /clear confirmation")
	}
}

func TestCopyPickerDigitCopies(t *testing.T) {
	m := newTestChatTUI()
	m.copyPick = &copyPicker{parts: []string{"one", "two"}}

	next, cmd := m.handleCopyPickerKey(digitKey('2'))
	got := next.(chatTUI)
	if got.copyPick != nil {
		t.Fatal("digit 2 should pick the part and close the copy picker")
	}
	if cmd == nil {
		t.Fatal("digit selection should return the clipboard command")
	}
}

func TestRewindDigitJumpsToTurn(t *testing.T) {
	m := newTestChatTUI()
	m.rewind = &rewindPicker{
		metas: []checkpoint.Meta{{Turn: 0}, {Turn: 1}, {Turn: 2}},
		sel:   2,
	}

	next, _ := m.handleRewindKey(digitKey('2'))
	r := next.(chatTUI).rewind
	if r.sel != 1 {
		t.Fatalf("digit 2 should select the row for turn 2, got sel %d", r.sel)
	}
	if r.stage != 1 {
		t.Fatalf("digit selection should advance to the scope stage, got stage %d", r.stage)
	}
}

func TestSkillPickerConfirmDeleteDigitCancel(t *testing.T) {
	m := newTestChatTUI()
	m.skillPick = &skillPicker{mode: pickerConfirmDelete}

	next, _ := m.handleSkillPickerConfirmDeleteKey(digitKey('2'))
	if got := next.(chatTUI).skillPick; got.mode != pickerDetail {
		t.Fatalf("digit 2 should cancel back to the detail view, got mode %q", got.mode)
	}
}

func TestMCPConfirmRemoveDigitCancel(t *testing.T) {
	m := newTestChatTUI()
	m.mcp = &mcpManager{stage: mcpStageConfirmRemove}

	next, _ := m.handleMCPManagerKey(digitKey('2'))
	if got := next.(chatTUI).mcp; got.stage != mcpStageDetail {
		t.Fatalf("digit 2 should cancel back to the detail stage, got stage %v", got.stage)
	}
}
