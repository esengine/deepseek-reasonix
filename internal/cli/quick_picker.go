package cli

import (
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/control"
)

const quickPickerMaxVisible = 8

// quickPickerYolo reports whether the session's approval mode is YOLO; pickers
// opened in that mode select rows by bare digit.
func (m chatTUI) quickPickerYolo() bool {
	return m.ctrl != nil && m.ctrl.ToolApprovalMode() == control.ToolApprovalYolo
}

type quickPickerKind string

const (
	quickPickerModel         quickPickerKind = "model"
	quickPickerProvider      quickPickerKind = "provider"
	quickPickerProviderModel quickPickerKind = "provider-model"
	quickPickerResume        quickPickerKind = "resume"
)

type quickPickerItem struct {
	ID          string
	Label       string
	Description string
	Status      string
}

// quickPicker is the shared single-choice overlay used by commands that need
// Claude Code-style searchable lists inside Bubble Tea's event loop. In YOLO
// mode digitSelect is set: a lone digit picks the matching row immediately,
// since the mode already opts out of confirmations. Otherwise digits filter.
type quickPicker struct {
	kind     quickPickerKind
	title    string
	hint     string
	items    []quickPickerItem
	query    string
	selected int // index in filteredItems, not items
	// digitSelect commits a bare 1..9 key to that row when the list is
	// unfiltered; set only when the active approval mode is YOLO.
	digitSelect bool
}

type quickPickerResult struct {
	choice    *quickPickerItem
	cancelled bool
}

func (p *quickPicker) filteredItems() []quickPickerItem {
	if p == nil || strings.TrimSpace(p.query) == "" {
		if p == nil {
			return nil
		}
		return p.items
	}
	query := strings.ToLower(strings.TrimSpace(p.query))
	out := make([]quickPickerItem, 0, len(p.items))
	for _, item := range p.items {
		haystack := strings.ToLower(item.Label + " " + item.Description + " " + item.Status)
		if strings.Contains(haystack, query) {
			out = append(out, item)
		}
	}
	return out
}

func (p *quickPicker) handleKey(msg tea.KeyPressMsg) quickPickerResult {
	if p == nil {
		return quickPickerResult{}
	}
	items := p.filteredItems()
	key := msg.String()
	// Match Claude Code's searchable menus: bare j/k navigate until a search
	// starts, then become ordinary query characters. Arrows and Ctrl+P/N always
	// remain available for navigation.
	if p.query == "" {
		switch key {
		case "k":
			key = "up"
		case "j":
			key = "down"
		}
	}
	switch key {
	case "esc":
		return quickPickerResult{cancelled: true}
	case "up", "ctrl+p":
		if p.selected > 0 {
			p.selected--
		}
	case "down", "ctrl+n":
		if p.selected < len(items)-1 {
			p.selected++
		}
	case "enter":
		if p.selected >= 0 && p.selected < len(items) {
			choice := items[p.selected]
			return quickPickerResult{choice: &choice}
		}
	case "backspace":
		if p.query != "" {
			_, n := utf8.DecodeLastRuneInString(p.query)
			p.query = p.query[:len(p.query)-n]
			p.selected = 0
		}
	default:
		text := msg.Text
		if text == "" {
			s := msg.String()
			if len(s) == 1 && s[0] >= 32 && s[0] < 127 {
				text = s
			}
		}
		// In YOLO mode digits pick the numbered row directly (the banner does
		// the same); digits still filter once a query is active, so typing a
		// digit to search can't commit row N.
		if p.digitSelect && p.query == "" && len(text) == 1 && text[0] >= '1' && text[0] <= '9' {
			if idx := int(text[0] - '1'); idx < len(items) {
				choice := items[idx]
				return quickPickerResult{choice: &choice}
			}
		}
		if text != "" {
			p.query += text
			p.selected = 0
		}
	}
	return quickPickerResult{}
}

func (p *quickPicker) render(width int) string {
	if p == nil {
		return ""
	}
	w := max(width, 10)
	contentWidth := max(w-8, 12)
	items := p.filteredItems()
	if p.selected >= len(items) {
		p.selected = max(len(items)-1, 0)
	}

	var b strings.Builder
	b.WriteString(accent(p.title) + "\n")
	if p.query != "" {
		b.WriteString("  " + dim("Search: ") + p.query + "\n")
	}
	if len(items) == 0 {
		b.WriteString(dim("  No matches") + "\n")
	} else {
		start, end := quickPickerWindow(len(items), p.selected)
		if start > 0 {
			b.WriteString(dim("  ↑ more") + "\n")
		}
		for i := start; i < end; i++ {
			item := items[i]
			label := ansi.Truncate(item.Label, contentWidth, "…")
			if item.Status != "" {
				label += " " + dim("("+item.Status+")")
			}
			b.WriteString(rowLine(i == p.selected, i+1, "", label, item.Status == "active") + "\n")
			if item.Description != "" {
				b.WriteString(dim("     "+ansi.Truncate(item.Description, contentWidth, "…")) + "\n")
			}
		}
		if end < len(items) {
			b.WriteString(dim("  ↓ more") + "\n")
		}
	}
	hint := p.hint
	if hint == "" {
		hint = "Type to filter · ↑/↓ navigate · Enter select · Esc cancel"
	}
	b.WriteString(dim(hint))
	return choicePanelStyle.Width(w).Render(b.String())
}

func quickPickerWindow(total, selected int) (int, int) {
	if total <= quickPickerMaxVisible {
		return 0, total
	}
	start := max(selected-quickPickerMaxVisible/2, 0)
	if maxStart := total - quickPickerMaxVisible; start > maxStart {
		start = maxStart
	}
	return start, start + quickPickerMaxVisible
}
