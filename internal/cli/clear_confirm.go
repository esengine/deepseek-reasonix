package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/i18n"
)

type clearConfirm struct {
	confirm int // 0 = clear, 1 = cancel
}

func (m chatTUI) handleClearConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "down", "left", "right", "j", "k", "tab", "shift+tab":
		if m.clearConfirm.confirm == 0 {
			m.clearConfirm.confirm = 1
		} else {
			m.clearConfirm.confirm = 0
		}
	case "y", "Y":
		return m.confirmClearContext()
	case "n", "N", "esc", "ctrl+c":
		m.clearConfirm = nil
	case "enter":
		if m.clearConfirm.confirm == 0 {
			return m.confirmClearContext()
		}
		m.clearConfirm = nil
	default:
		if idx, ok := numberKeyIndex(msg.String(), 2); ok {
			if idx == 0 {
				return m.confirmClearContext()
			}
			m.clearConfirm = nil
		}
	}
	return m, nil
}

func (m *chatTUI) clearContext() tea.Cmd {
	m.clearConfirm = nil
	if err := m.ctrl.ClearSession(); err != nil {
		m.notice(fmt.Sprintf("%s: %v", i18n.M.SlashClearFailed, err))
		return nil
	}
	m.followSessionLease()
	m.resetFreshContextView(true)
	m.notice(i18n.M.SlashClearDone)
	return tea.ClearScreen
}

func (m chatTUI) confirmClearContext() (tea.Model, tea.Cmd) {
	return m, m.clearContext()
}

func (m *chatTUI) resetFreshContextView(clearTranscript bool) {
	m.finalizeStreamed()
	m.pending.Reset()
	m.reasoning.Reset()
	m.todoArgs = ""
	m.chooser = nil
	m.pendingApproval = nil
	m.bubblePending = false
	m.turnDiscarded = false
	m.sessionCostQuote = nil
	if clearTranscript {
		m.clearTranscriptDisplay()
		m.sessionSwitch = true
	} else {
		m.commitLine("")
	}
	m.commitTranscriptSource(transcriptSource{kind: transcriptSourceBanner})
	m.transcriptDirty = true
	m.forceGotoBottom = true
}

func (m chatTUI) renderClearConfirm() string {
	if m.clearConfirm == nil {
		return ""
	}
	w := max(viewWidth(m.width), 40)
	var b strings.Builder
	b.WriteString(i18n.M.SlashClearPrompt + "\n")
	b.WriteString(viewMeta("This deletes the current transcript from local history and keeps only the system prompt.") + "\n\n")
	b.WriteString(rowLine(m.clearConfirm.confirm == 0, 1, "", "Clear", false) + "\n")
	b.WriteString(rowLine(m.clearConfirm.confirm == 1, 2, "", "Cancel", false))
	return choicePanelStyle.Width(w).Render(b.String())
}
