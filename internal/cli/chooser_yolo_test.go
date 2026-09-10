package cli

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/control"
	"reasonix/internal/event"
)

// TestChooserYoloAutoSubmitsFullyAnsweredBatch pins that in YOLO mode answering
// the last question of a multi-question ask submits the whole batch as soon as
// the Submit tab is reached, with no extra confirmation Enter.
func TestChooserYoloAutoSubmitsFullyAnsweredBatch(t *testing.T) {
	ask := twoQuestionAsk()
	ctrl := control.New(control.Options{})
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)
	m.ctrl.SetToolApprovalMode(control.ToolApprovalYolo)
	m.chooser = newChooser(ask)

	// Answer q1: single-select commits and advances to q2, not the Submit tab.
	mm, _ := m.handleChooserKey(tea.KeyPressMsg{Code: '1'})
	cm := mm.(chatTUI)
	if cm.chooser == nil || cm.chooser.tab != 1 {
		t.Fatalf("q1 answer must advance to q2, tab=%v chooser=%v", tabOf(cm), cm.chooser != nil)
	}

	// Answer q2: reaching the Submit tab fully answered auto-submits.
	mm, _ = cm.handleChooserKey(tea.KeyPressMsg{Code: '1'})
	if mm.(chatTUI).chooser != nil {
		t.Fatal("YOLO must auto-submit a fully answered batch at the Submit tab")
	}
}

// TestChooserYoloWaitsOnIncompleteBatch pins that YOLO auto-submit only fires
// when nothing is unanswered: a question skipped along the way still parks on
// the Submit tab for review.
func TestChooserYoloWaitsOnIncompleteBatch(t *testing.T) {
	ask := twoQuestionAsk()
	ctrl := control.New(control.Options{})
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)
	m.ctrl.SetToolApprovalMode(control.ToolApprovalYolo)
	m.chooser = newChooser(ask)

	// Skip q1 with →, answer only q2.
	mm, _ := m.handleChooserKey(tea.KeyPressMsg{Code: tea.KeyRight})
	cm := mm.(chatTUI)
	if cm.chooser == nil || cm.chooser.tab != 1 {
		t.Fatalf("right arrow must move to q2, tab=%v", tabOf(cm))
	}
	mm, _ = cm.handleChooserKey(tea.KeyPressMsg{Code: '1'})
	cm = mm.(chatTUI)
	if cm.chooser == nil || !cm.chooser.onSubmitTab() {
		t.Fatal("advance with q1 unanswered must land on the Submit tab, not submit")
	}
}

// TestChooserNonYoloStillWaitsOnSubmit pins that outside YOLO the Submit tab
// still needs its explicit Enter even when the batch is fully answered.
func TestChooserNonYoloStillWaitsOnSubmit(t *testing.T) {
	ask := twoQuestionAsk()
	ctrl := control.New(control.Options{})
	m := newChatTUI(ctrl, "", make(chan event.Event, 1), 80)
	m.chooser = newChooser(ask)

	mm, _ := m.handleChooserKey(tea.KeyPressMsg{Code: '1'})
	cm := mm.(chatTUI)
	mm, _ = cm.handleChooserKey(tea.KeyPressMsg{Code: '1'})
	cm = mm.(chatTUI)
	if cm.chooser == nil || !cm.chooser.onSubmitTab() {
		t.Fatal("non-YOLO must stop at the Submit tab for the confirmation Enter")
	}

	// The Enter on the Submit tab then commits.
	mm, _ = cm.handleChooserKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if mm.(chatTUI).chooser != nil {
		t.Fatal("Enter on the Submit tab must submit the batch")
	}
}

func twoQuestionAsk() event.Ask {
	return event.Ask{
		ID: "ask-1",
		Questions: []event.AskQuestion{
			{ID: "q1", Prompt: "One", Options: []event.AskOption{{Label: "Option A"}}},
			{ID: "q2", Prompt: "Two", Options: []event.AskOption{{Label: "Option B"}}},
		},
	}
}

func tabOf(m chatTUI) int {
	if m.chooser == nil {
		return -1
	}
	return m.chooser.tab
}
