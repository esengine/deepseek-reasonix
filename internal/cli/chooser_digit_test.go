package cli

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/event"
)

// TestChooserDigitKeysReachSpecialRows pins that number keys cover every
// numbered row — a real option, the "Type something" free-text row, and the
// "Chat about this" dismissal row — matching what Enter does on the same row.
func TestChooserDigitKeysReachSpecialRows(t *testing.T) {
	ask := event.Ask{
		ID: "ask-1",
		Questions: []event.AskQuestion{{
			ID:     "q1",
			Prompt: "Pick one",
			Options: []event.AskOption{
				{Label: "Option A"},
				{Label: "Option B"},
			},
		}},
	}
	ctrl := control.New(control.Options{})
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)

	// Digit 1 = Option A (commits the single-select).
	m.chooser = newChooser(ask)
	mm, _ := m.handleChooserKey(tea.KeyPressMsg{Code: '1'})
	if mm.(chatTUI).chooser != nil {
		t.Fatal("digit 1 must select Option A and advance")
	}

	// Digit 3 = "Type something" (len(options)+1): opens free-text entry.
	m.chooser = newChooser(ask)
	mm, _ = m.handleChooserKey(tea.KeyPressMsg{Code: '3'})
	cm := mm.(chatTUI)
	if cm.chooser == nil || !cm.chooser.typing {
		t.Fatal("digit 3 must open the Type-something free-text row")
	}

	// Digit 4 = "Chat about this" (len(options)+2): dismisses the prompt.
	m.chooser = newChooser(ask)
	mm, _ = m.handleChooserKey(tea.KeyPressMsg{Code: '4'})
	if mm.(chatTUI).chooser != nil {
		t.Fatal("digit 4 must dismiss the chooser (chat instead)")
	}
}
