package cli

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/checkpoint"
	"reasonix/internal/control"
	"reasonix/internal/i18n"
)

// rewindPicker is the in-chat overlay for Esc-Esc / "/rewind". Stage 0 lists the
// session's turns (one checkpoint each); stage 1 picks what to restore for the
// chosen turn; stage 2 explicitly confirms file restore when checkpoint coverage
// is partial. It mirrors the chooser overlay: keys route through handleRewindKey
// and it renders via renderRewind while m.rewind is set.
type rewindPicker struct {
	metas       []checkpoint.Meta
	sel         int // selected turn (index into metas)
	stage       int // 0 = pick turn, 1 = pick scope, 2 = confirm partial coverage
	scope       int // index into rewindActions (stage 1)
	pendingPlan checkpoint.RewindPlan
}

var rewindActions = []struct {
	kind  string // "scope" | "fork" | "summ-from" | "summ-upto"
	scope control.RewindScope
}{
	{"scope", control.RewindBoth},
	{"scope", control.RewindConversation},
	{"scope", control.RewindCode},
	{"fork", 0},
	{"summ-from", 0},
	{"summ-upto", 0},
}

// openRewind populates the picker from the session's checkpoints, selecting the
// most recent turn. A no-op (with a notice) when there is nothing to rewind.
func (m *chatTUI) openRewind() {
	metas := m.ctrl.Checkpoints()
	if len(metas) == 0 {
		m.notice(i18n.M.RewindNone)
		return
	}
	m.rewind = &rewindPicker{metas: metas, sel: len(metas) - 1}
}

func (m chatTUI) handleRewindKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	r := m.rewind
	switch msg.String() {
	case "esc":
		switch r.stage {
		case 2:
			r.stage = 1
			r.pendingPlan = checkpoint.RewindPlan{}
		case 1:
			r.stage = 0
		default:
			m.rewind = nil
		}
	case "up", "k":
		if r.stage == 0 {
			if r.sel > 0 {
				r.sel--
			}
		} else if r.scope > 0 {
			r.scope--
		}
	case "down", "j":
		if r.stage == 0 {
			if r.sel < len(r.metas)-1 {
				r.sel++
			}
		} else if r.scope < len(rewindActions)-1 {
			r.scope++
		}
	case "enter":
		switch r.stage {
		case 0:
			r.stage = 1
		case 1:
			return m.applyRewind()
		default:
			return m.commitPreparedRewind()
		}
	case "y":
		if r.stage == 2 {
			return m.commitPreparedRewind()
		}
	case "b":
		if r.stage == 1 {
			r.scope = 0
			return m.applyRewind()
		}
	case "c":
		if r.stage == 1 {
			r.scope = 1
			return m.applyRewind()
		}
	case "d":
		if r.stage == 1 {
			r.scope = 2
			return m.applyRewind()
		}
	case "f":
		if r.stage == 1 {
			r.scope = 3
			return m.applyRewind()
		}
	case "s":
		if r.stage == 1 {
			r.scope = 4
			return m.applyRewind()
		}
	case "u":
		if r.stage == 1 {
			r.scope = 5
			return m.applyRewind()
		}
	default:
		if n, ok := numberKeyIndex(msg.String(), 9); ok {
			switch r.stage {
			case 0:
				// Turn rows are numbered by turn (Turn+1), not list position.
				for i, meta := range r.metas {
					if meta.Turn == n {
						r.sel = i
						r.stage = 1
						break
					}
				}
			case 1:
				if n < len(rewindActions) {
					r.scope = n
					return m.applyRewind()
				}
			}
		}
	}
	return m, nil
}

func (m chatTUI) applyRewind() (tea.Model, tea.Cmd) {
	r := m.rewind
	meta := r.metas[r.sel]
	act := rewindActions[r.scope]
	// The controller emits notices for operation errors and committed rewinds.
	// A prepared-but-disabled plan has no controller error, so this picker reports
	// that precheck result itself below.
	switch act.kind {
	case "fork":
		m.rewind = nil
		if _, err := m.ctrl.Fork(meta.Turn); err == nil {
			m.followSessionLease()
			m.replayActiveBranch(fmt.Sprintf("branched from turn %d", meta.Turn+1))
		}
		return m, nil // the controller is on the fork now
	case "summ-from":
		m.rewind = nil
		_ = m.ctrl.SummarizeFrom(context.Background(), meta.Turn)
		return m, nil
	case "summ-upto":
		m.rewind = nil
		_ = m.ctrl.SummarizeUpTo(context.Background(), meta.Turn)
		return m, nil
	}
	plan, err := m.ctrl.PrepareRewind(meta.Turn, act.scope)
	if err != nil {
		m.rewind = nil
		return m, nil
	}
	if !rewindPlanCanApply(plan) {
		reason := strings.TrimSpace(plan.DisabledReason)
		if reason == "" {
			reason = "precheck failed"
		}
		m.notice(fmt.Sprintf(i18n.M.RewindUnavailableFmt, reason))
		m.rewind = nil
		return m, nil
	}
	if control.RewindPlanRequiresConfirmation(plan) {
		r.pendingPlan = plan
		r.stage = 2
		return m, nil
	}
	r.pendingPlan = plan
	return m.commitPreparedRewind()
}

func rewindPlanCanApply(plan checkpoint.RewindPlan) bool {
	switch plan.Scope {
	case checkpoint.RewindCode:
		return plan.CanFiles
	case checkpoint.RewindConversation:
		return plan.CanConversation
	case checkpoint.RewindBoth:
		return plan.CanFiles && plan.CanConversation
	default:
		return false
	}
}

func (m chatTUI) commitPreparedRewind() (tea.Model, tea.Cmd) {
	r := m.rewind
	if r == nil || r.pendingPlan.PlanID == "" {
		return m, nil
	}
	meta := r.metas[r.sel]
	scope := control.RewindScope(r.pendingPlan.Scope)
	planID := r.pendingPlan.PlanID
	m.rewind = nil
	result, err := m.ctrl.CommitRewindInPlace(planID)
	if err != nil || !result.OK {
		return m, nil
	}
	if result.ConversationForked {
		// The controller is already on the rewound conversation: a head of the
		// same log, or the fork file of a schema-1 session. Only the lease and
		// the transcript view still follow it.
		m.followSessionLease()
		m.replayActiveBranch(fmt.Sprintf("rewound to turn %d", meta.Turn+1))
	}
	// Conversation rewind activates the fork and prefills the selected prompt
	// for editing. Code-only rewind keeps the current transcript on screen.
	if scope != control.RewindCode && strings.TrimSpace(meta.Prompt) != "" {
		m.input.SetValue(meta.Prompt)
		m.growInputToFit()
	}
	return m, nil
}

func (m chatTUI) renderRewind() string {
	r := m.rewind
	if r == nil {
		return ""
	}
	w := max(m.width, 10)
	var b strings.Builder
	if r.stage == 0 {
		b.WriteString(accent(i18n.M.RewindPickTitle) + "\n")
		// Long sessions list one row per turn; window it like quickPicker so
		// the overlay never outgrows the terminal (no scrolling viewport).
		start, end := quickPickerWindow(len(r.metas), r.sel)
		if start > 0 {
			b.WriteString(dim("  ↑ more") + "\n")
		}
		for i := start; i < end; i++ {
			meta := r.metas[i]
			b.WriteString(rowLine(i == r.sel, meta.Turn+1, "", turnLabel(meta, w), false) + "\n")
		}
		if end < len(r.metas) {
			b.WriteString(dim("  ↓ more") + "\n")
		}
		b.WriteString(dim(i18n.M.RewindPickHint))
		return choicePanelStyle.Width(w).Render(b.String())
	}
	meta := r.metas[r.sel]
	if r.stage == 2 {
		b.WriteString(accent(i18n.M.RewindCoverageTitle) + "\n")
		b.WriteString(fmt.Sprintf(i18n.M.RewindCoverageWarningFmt, len(r.pendingPlan.CoverageGaps)) + "\n")
		b.WriteString(dim(fmt.Sprintf(i18n.M.RewindRestoreTitleFmt, meta.Turn+1)+oneLine(meta.Prompt, 48)) + "\n")
		b.WriteString(dim(i18n.M.RewindConfirmHint))
		return choicePanelStyle.Width(w).Render(b.String())
	}
	b.WriteString(accent(fmt.Sprintf(i18n.M.RewindRestoreTitleFmt, meta.Turn+1)) + dim(oneLine(meta.Prompt, 48)) + "\n")
	for i := range rewindActions {
		b.WriteString(rowLine(i == r.scope, i+1, "", rewindActionLabel(i), false) + "\n")
	}
	b.WriteString(dim(i18n.M.RewindApplyHint))
	return choicePanelStyle.Width(w).Render(b.String())
}

func rewindActionLabel(i int) string {
	switch i {
	case 0:
		return i18n.M.RewindCodeConversation
	case 1:
		return i18n.M.RewindConversationOnly
	case 2:
		return i18n.M.RewindCodeOnly
	case 3:
		return i18n.M.RewindFork
	case 4:
		return i18n.M.RewindSummarizeFrom
	case 5:
		return i18n.M.RewindSummarizeUpto
	default:
		return ""
	}
}

func turnLabel(meta checkpoint.Meta, w int) string {
	label := oneLine(meta.Prompt, max(20, w-30))
	if n := len(meta.Paths); n > 0 {
		s := ""
		if n != 1 {
			s = "s"
		}
		label += dim(fmt.Sprintf("  (%d file%s)", n, s))
	}
	return label
}

// oneLine flattens s to a single line and truncates it to display width n.
func oneLine(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if s == "" {
		return i18n.M.RewindEmpty
	}
	return ansi.Truncate(s, n, "…")
}
