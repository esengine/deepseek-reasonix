package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/agent"
	"reasonix/internal/i18n"
)

// resumePicker is an in-chat overlay for "/resume" that lets the user pick a
// saved session by navigating with ↑/↓ and confirming with Enter. It mirrors
// the rewindPicker pattern: keys route through handleResumePickerKey and it
// renders via renderResumePicker while m.resumePick is set.
type resumePicker struct {
	entries []resumeEntry
	sel     int // selected index
	active  int // index of the currently-active session (-1 when none)
	quick   *quickPicker
}

// openResumePicker populates the picker from the session directory and opens it.
// A no-op (with a notice) when there are no saved sessions.
func (m *chatTUI) openResumePicker() {
	reclaimCLIRecoveryBranches(m.ctrl.SessionDir())
	entries := resumeEntries(m.ctrl.SessionDir())
	if len(entries) == 0 {
		m.notice(i18n.M.NoSessionToResume)
		return
	}
	active := m.ctrl.SessionPath()
	activeIdx := -1
	for i, entry := range entries {
		if entry.session.Path == active {
			activeIdx = i
			break
		}
	}
	// Default selection: the first session after the active one, else 0.
	sel := 0
	if activeIdx >= 0 && activeIdx+1 < len(entries) {
		sel = activeIdx + 1
	}
	items := make([]quickPickerItem, 0, len(entries))
	for i, entry := range entries {
		status := ""
		if i == activeIdx {
			status = "active"
		}
		label := sessionPickerLabel(entry.session)
		description := entry.session.ModTime.Local().Format("2006-01-02 15:04")
		if entry.project != "" {
			label = fmt.Sprintf("[%s] %s", entry.project, label)
			description = entry.project + " · " + description
		}
		items = append(items, quickPickerItem{
			ID: entry.session.Path, Label: label,
			Description: description, Status: status,
		})
	}
	m.resumePick = &resumePicker{
		entries: entries, sel: sel, active: activeIdx,
		quick: &quickPicker{kind: quickPickerResume, title: i18n.M.ResumePickTitle, items: items, selected: sel},
	}
}

func (m chatTUI) handleResumePickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	r := m.resumePick
	if r == nil {
		return m, nil
	}
	if r.quick != nil {
		result := r.quick.handleKey(msg)
		r.sel = r.quick.selected
		if result.cancelled {
			m.resumePick = nil
			return m, nil
		}
		if result.choice != nil {
			for i, entry := range r.entries {
				if entry.session.Path == result.choice.ID {
					r.sel = i
					break
				}
			}
			return m.applyResumePick()
		}
		return m, nil
	}
	switch msg.String() {
	case "up", "k":
		if r.sel > 0 {
			r.sel--
		}
	case "down", "j":
		if r.sel < len(r.entries)-1 {
			r.sel++
		}
	case "enter":
		return m.applyResumePick()
	case "esc":
		m.resumePick = nil
	default:
		if idx, ok := numberKeyIndex(msg.String(), len(r.sessions)); ok {
			r.sel = idx
			return m.applyResumePick()
		}
	}
	return m, nil
}

func (m chatTUI) applyResumePick() (tea.Model, tea.Cmd) {
	r := m.resumePick
	if r == nil || r.sel < 0 || r.sel >= len(r.entries) {
		return m, nil
	}
	target := r.entries[r.sel].session
	m.resumePick = nil
	if target.Path == m.ctrl.SessionPath() {
		m.notice(i18n.M.ResumeAlreadyActive)
		return m, nil
	}
	if m.ctrl.Running() {
		m.notice(i18n.M.ResumeBusy)
		return m, nil
	}
	// Snapshot before moving the lease: the outgoing session must be written
	// while this process still owns it.
	if err := m.ctrl.Snapshot(); err != nil {
		m.notice("resume: snapshot current session: " + err.Error())
		return m, nil
	}
	m.followSessionLease()
	if err := m.commitSessionSwitch(target.Path); err != nil {
		m.notice("resume: " + sessionLeaseHeldNotice(err))
		if cliSessionTakeoverCandidate(err) {
			m.pendingTakeoverPath = target.Path
			m.notice("run /takeover to take this session over from the resident serve")
		}
		return m, nil
	}
	m.replayActiveBranch(i18n.M.ResumedTitle)
	return m, nil
}

func (m chatTUI) renderResumePicker() string {
	r := m.resumePick
	if r == nil {
		return ""
	}
	if r.quick != nil {
		return r.quick.render(m.width)
	}
	w := max(m.width, 10)
	var b strings.Builder
	b.WriteString(accent(i18n.M.ResumePickTitle) + "\n")
	for i, entry := range r.entries {
		label := sessionPickerLabel(entry.session)
		if entry.project != "" {
			label = fmt.Sprintf("[%s] %s", entry.project, label)
		}
		if i == r.active {
			label = dim(label) + " " + dim("(active)")
		}
		b.WriteString(rowLine(i == r.sel, i+1, "", label, false) + "\n")
	}
	b.WriteString(dim(i18n.M.ResumePickHint))
	return choicePanelStyle.Width(w).Render(b.String())
}

// sessionPickerLabel is the "N turns · display title" line, truncated to fit.
// Explicit session renames win, then topic titles, then the raw preview.
func sessionPickerLabel(s agent.SessionInfo) string {
	preview := s.CustomTitle
	if preview == "" {
		preview = s.TopicTitle
	}
	if preview == "" {
		preview = s.Preview
	}
	if preview == "" {
		preview = "(no user message yet)"
	}
	return recoverySessionBadge(s) + fmt.Sprintf("%d turns · %s", s.Turns, ansi.Truncate(preview, 60, "…"))
}
