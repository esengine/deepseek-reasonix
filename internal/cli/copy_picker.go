package cli

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/i18n"
)

// copyPicker is an in-chat overlay for "/copy" that lets the user pick an
// assistant message to copy with ↑/↓ and confirm with Enter.  Esc closes it.
type copyPicker struct {
	parts []string // assistant Content lines (newest-first: index 0 = most recent)
	sel   int      // selected index
}

// openCopyPicker populates the picker from the session history and opens it.
func (m *chatTUI) openCopyPicker() {
	msgs := m.ctrl.History()
	parts := copyAssistantParts(msgs)
	if len(parts) == 0 {
		m.notice(i18n.M.SlashCopyEmpty)
		return
	}
	// Reverse so newest-first matches the selection order (0 = most recent).
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	m.copyPick = &copyPicker{parts: parts, sel: 0}
}

func (m chatTUI) handleCopyPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.copyPick
	if p == nil {
		return m, nil
	}
	switch msg.String() {
	case "up", "k":
		if p.sel > 0 {
			p.sel--
		}
	case "down", "j":
		if p.sel < len(p.parts)-1 {
			p.sel++
		}
	case "enter":
		return m.applyCopyPick()
	case "esc":
		m.copyPick = nil
	default:
		if idx, ok := numberKeyIndex(msg.String(), len(p.parts)); ok {
			p.sel = idx
			return m.applyCopyPick()
		}
	}
	return m, nil
}

func (m chatTUI) applyCopyPick() (tea.Model, tea.Cmd) {
	p := m.copyPick
	if p == nil || p.sel < 0 || p.sel >= len(p.parts) {
		return m, nil
	}
	text := p.parts[p.sel]
	m.copyPick = nil
	return m, copyToClipboard(text)
}

func (m chatTUI) renderCopyPicker() string {
	p := m.copyPick
	if p == nil {
		return ""
	}
	w := max(m.width, 10)
	var b strings.Builder
	b.WriteString(accent(i18n.M.SlashCopyListHeader) + "\n")
	for i, part := range p.parts {
		b.WriteString(rowLine(i == p.sel, i+1, "", firstLine(part), false) + "\n")
	}
	b.WriteString(dim("↑/↓ navigate · Enter copy · Esc cancel"))
	return choicePanelStyle.Width(w).Render(b.String())
}
