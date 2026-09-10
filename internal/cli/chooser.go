package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/i18n"
)

// chooser is the in-chat multiple-choice prompt the `ask` tool raises — the CLI's
// question card. It holds the questions, the per-question selections, and the
// cursor; chatTUI routes keystrokes to it while it's active
// (m.chooser != nil) and renders it pinned above the input. One AskRequest can
// carry several questions, shown as tabs (←/→) plus a final Submit tab.
type chooser struct {
	id        string
	questions []event.AskQuestion
	tab       int            // 0..len-1: a question; len: the Submit tab
	cursor    int            // highlighted row within the current question
	sel       []map[int]bool // chosen option indices, per question
	custom    []string       // free-typed answer, per question ("" = none)
	typing    bool           // entering a free-text answer (keys go to the textarea)
}

func newChooser(a event.Ask) *chooser {
	c := &chooser{
		id:        a.ID,
		questions: a.Questions,
		sel:       make([]map[int]bool, len(a.Questions)),
		custom:    make([]string, len(a.Questions)),
	}
	for i := range c.sel {
		c.sel[i] = map[int]bool{}
	}
	return c
}

func (c *chooser) onSubmitTab() bool { return c.tab >= len(c.questions) }

// rowCount is the rows of the current question: one per option, then a "Type
// something" row and a "Chat about this" row.
func (c *chooser) rowCount() int {
	if c.onSubmitTab() {
		return 0
	}
	return len(c.questions[c.tab].Options) + 2
}

func (c *chooser) answered(i int) bool { return len(c.sel[i]) > 0 || c.custom[i] != "" }

func (c *chooser) allAnswered() bool {
	for i := range c.questions {
		if !c.answered(i) {
			return false
		}
	}
	return true
}

// answers builds the AskAnswer list from the current selections (custom text wins
// when set).
func (c *chooser) answers() []event.AskAnswer {
	out := make([]event.AskAnswer, len(c.questions))
	for i, q := range c.questions {
		var sel []string
		if c.custom[i] != "" {
			sel = []string{c.custom[i]}
		} else {
			for j := range q.Options {
				if c.sel[i][j] {
					sel = append(sel, q.Options[j].Label)
				}
			}
		}
		out[i] = event.AskAnswer{QuestionID: q.ID, Selected: sel}
	}
	return out
}

// chatTUI integration

// handleChooserKey routes a keystroke to the active chooser (when not in free-text
// mode — that's handled in Update by the textarea). Selecting an option in a
// single-select question advances; on the last/only question it submits.
func (m chatTUI) handleChooserKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	c := m.chooser
	switch msg.String() {
	case "ctrl+c":
		m.ctrl.Cancel()
		m.chooser = nil
		return m, nil
	case "esc":
		return m.chooserAnswer(nil) // dismiss → empty answer
	case "left", "h":
		if c.tab > 0 {
			c.tab--
			c.cursor = 0
		}
		return m, nil
	case "right", "l":
		if c.tab < len(c.questions) {
			c.tab++
			c.cursor = 0
		}
		return m.chooserAutoSubmitIfDone()
	}

	if c.onSubmitTab() {
		switch msg.String() {
		case "enter":
			return m.chooserAnswer(c.answers())
		case "up", "k", "down", "j":
			c.tab = len(c.questions) - 1 // step back into the last question
			c.cursor = 0
		}
		return m, nil
	}

	q := c.questions[c.tab]
	switch msg.String() {
	case "up", "k":
		if c.cursor > 0 {
			c.cursor--
		}
	case "down", "j":
		if c.cursor < c.rowCount()-1 {
			c.cursor++
		}
	case " ", "space":
		if c.cursor < len(q.Options) && q.Multi {
			c.sel[c.tab][c.cursor] = !c.sel[c.tab][c.cursor]
			c.custom[c.tab] = ""
		}
	case "enter":
		return m.chooserActivate(c.cursor)
	default:
		// number keys 1..9 jump to / pick an option
		if s := msg.String(); len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
			if idx := int(s[0] - '1'); idx < len(q.Options) {
				return m.chooserActivate(idx)
			}
		}
	}
	return m, nil
}

// chooserActivate acts on the row: a normal option toggles (multi) or selects and
// advances (single); the "Type something" row opens free-text entry; the "Chat
// about this" row dismisses the prompt so the user can just talk.
func (m chatTUI) chooserActivate(row int) (tea.Model, tea.Cmd) {
	c := m.chooser
	q := c.questions[c.tab]
	switch {
	case row < len(q.Options):
		if q.Multi {
			// Space toggles; Enter confirms current selections and advances.
			// (Toggling is handled in handleChooserKey; we only arrive here
			// via Enter or number keys, both of which should commit.)
			return m.chooserAdvance()
		}
		c.sel[c.tab] = map[int]bool{row: true}
		c.custom[c.tab] = ""
		return m.chooserAdvance()
	case row == len(q.Options): // Type something
		c.typing = true
		c.cursor = row
		m.resetComposerInput()
		m.input.SetHeight(1)
		m.refreshInputPlaceholder()
		return m, nil
	default: // Chat about this
		return m.chooserAnswer(nil)
	}
}

// chooserAdvance moves to the next question, or the Submit tab; a single-question
// prompt submits straight away.
func (m chatTUI) chooserAdvance() (tea.Model, tea.Cmd) {
	c := m.chooser
	if len(c.questions) == 1 {
		return m.chooserAnswer(c.answers())
	}
	if c.tab < len(c.questions) {
		c.tab++
		c.cursor = 0
	}
	return m.chooserAutoSubmitIfDone()
}

// chooserAutoSubmitIfDone commits immediately when the Submit tab is reached in
// YOLO mode with every question answered: YOLO skips confirmations, so the
// Submit-tab Enter on an already-complete batch would be pure ceremony.
func (m chatTUI) chooserAutoSubmitIfDone() (tea.Model, tea.Cmd) {
	if m.chooser.onSubmitTab() && m.chooser.allAnswered() && m.ctrl.ToolApprovalMode() == control.ToolApprovalYolo {
		return m.chooserAnswer(m.chooser.answers())
	}
	return m, nil
}

// chooserAnswer resolves the prompt with the given answers (nil = dismissed) and
// clears it; the blocked `ask` tool unblocks and the turn continues.
func (m chatTUI) chooserAnswer(answers []event.AskAnswer) (tea.Model, tea.Cmd) {
	m.ctrl.AnswerQuestion(m.chooser.id, answers)
	m.chooser = nil
	m.refreshInputPlaceholder()
	return m, nil
}

// renderChooser draws the pinned question card: a tab strip (when more than one
// question), the current question's prompt and options, and the Type-something /
// Chat-about-this rows. On the Submit tab it shows a review of the picks.
func (m chatTUI) renderChooser() string {
	c := m.chooser
	if c == nil {
		return ""
	}
	w := max(m.width, 10)
	var b strings.Builder

	if len(c.questions) > 1 {
		b.WriteString(m.chooserTabs() + "\n\n")
	}

	if c.onSubmitTab() {
		b.WriteString(accent(i18n.M.AskSubmitTitle) + "\n")
		for i, q := range c.questions {
			label := headerOr(q, i)
			ans := dim(i18n.M.AskUnanswered)
			if a := c.answers()[i]; len(a.Selected) > 0 {
				ans = strings.Join(a.Selected, ", ")
			}
			fmt.Fprintf(&b, "  %s: %s\n", dim(label), ans)
		}
		b.WriteString(dim(i18n.M.AskSubmitHint))
		return choicePanelStyle.Width(w).Render(b.String())
	}

	q := c.questions[c.tab]
	b.WriteString(accent("? ") + q.Prompt + "\n")
	for j, opt := range q.Options {
		b.WriteString(m.chooserOptionRow(j, opt, q.Multi) + "\n")
	}
	// Type something
	typeRow := len(q.Options)
	typeLabel := i18n.M.AskTypeSomething
	if c.custom[c.tab] != "" {
		typeLabel = c.custom[c.tab]
	} else if c.typing {
		typeLabel = i18n.M.AskTypingHint
	}
	b.WriteString(rowLine(c.cursor == typeRow, typeRow+1, "", typeLabel, c.typing && c.custom[c.tab] == "") + "\n")
	// Chat about this
	b.WriteString(dim(strings.Repeat("─", min(w-2, 40))) + "\n")
	chatRow := typeRow + 1
	b.WriteString(rowLine(c.cursor == chatRow, chatRow+1, "", i18n.M.AskChatInstead, false))

	return choicePanelStyle.Width(w).Render(b.String())
}

func (m chatTUI) chooserTabs() string {
	c := m.chooser
	parts := make([]string, 0, len(c.questions)+1)
	for i, q := range c.questions {
		mark := "☐"
		if c.answered(i) {
			mark = "✔"
		}
		label := mark + " " + headerOr(q, i)
		if i == c.tab {
			label = reverse(" " + label + " ")
		} else {
			label = dim(label)
		}
		parts = append(parts, label)
	}
	smark := "☐"
	if c.allAnswered() {
		smark = "✔"
	}
	submit := smark + " Submit"
	if c.onSubmitTab() {
		submit = reverse(" " + submit + " ")
	} else {
		submit = dim(submit)
	}
	return dim("← ") + strings.Join(parts, "  ") + "  " + submit + dim(" →")
}

// chooserOptionRow renders one option line (with its description underneath).
func (m chatTUI) chooserOptionRow(j int, opt event.AskOption, multi bool) string {
	c := m.chooser
	box := ""
	if multi {
		box = "☐ "
		if c.sel[c.tab][j] {
			box = "☑ "
		}
	}
	line := rowLine(c.cursor == j, j+1, box, opt.Label, false)
	if opt.Description != "" {
		line += "\n" + dim("       "+opt.Description)
	}
	return line
}

// rowLine formats a selectable row: "❯ N. <box><label>", highlighted when current.
func rowLine(cur bool, num int, box, label string, active bool) string {
	prefix := "  "
	if cur {
		prefix = accent("❯ ")
	}
	body := fmt.Sprintf("%d. %s%s", num, box, label)
	if cur {
		body = bold(body)
	} else if active {
		body = yellow(body)
	} else {
		body = dim(body)
	}
	return prefix + body
}

func headerOr(q event.AskQuestion, i int) string {
	if q.Header != "" {
		return q.Header
	}
	return fmt.Sprintf("Q%d", i+1)
}

// choicePanelStyle frames the question card, matching the input box's top/bottom
// rule but in the accent colour.
var choicePanelStyle lipgloss.Style
