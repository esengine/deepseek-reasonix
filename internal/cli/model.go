package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/config"
	"reasonix/internal/extension/providerext"
	"reasonix/internal/i18n"
	"reasonix/internal/provider"
)

// runModelSubcommand handles "/model": with no argument it opens the configured
// model picker; "/model <ref>" switches the
// session to that model in place, carrying the conversation across. The actual
// controller build runs asynchronously so it cannot block the TUI event loop.
func (m *chatTUI) runModelSubcommand(input string) {
	args := tokenizeArgs(input) // args[0] == "/model"
	if len(args) < 2 {
		m.openModelPicker()
		return
	}
	ref := args[1]
	if m.buildController == nil {
		m.notice(i18n.M.ModelSwitchUnavailable)
		return
	}
	if m.runtimeSwitchBusy() {
		m.notice(i18n.M.ModelSwitchBusy)
		return
	}
	if m.modelSwitchPending {
		m.notice(i18n.M.RuntimeSwitchPending)
		return
	}
	if ref == m.modelRef {
		m.notice(fmt.Sprintf(i18n.M.ModelAlreadyOnFmt, ref))
		return
	}
	// Persist the user's choice to the user config.toml so the next
	// session starts on the same model instead of falling back to the global
	// default. Mirrors the pattern used by /theme (persistTheme), /effort, and
	// /language.
	m.persistModel(ref)
	if err := m.ctrl.Snapshot(); err != nil {
		m.notice("model: snapshot failed: " + err.Error())
	}
	// Capture the resume path and history only after Snapshot: a snapshot
	// conflict can retarget the controller to a recovery branch (or adopt the
	// newer disk transcript), and a pre-snapshot capture would bind the rebuilt
	// controller back to the original file, re-conflicting on every later save.
	carried := m.ctrl.History()
	prevPath := m.ctrl.SessionPath()
	// Move the lease before the rebuilt controller binds prevPath for writing
	// (AdoptHistory resumes there): after a snapshot retarget the lease still
	// guards the old path, and the async build must not open an unguarded
	// writer on the recovery branch.
	if err := m.rebindSessionLease(prevPath); err != nil {
		m.notice("model: " + sessionLeaseHeldNotice(err))
		return
	}
	m.notice(fmt.Sprintf(i18n.M.ModelSwitchingFmt, ref))

	// Capture old controller for cleanup after the async build succeeds.
	oldCtrl := m.ctrl
	build := m.buildController

	// Fire the build off the event loop; the result arrives as a tea.Cmd.
	// Both the build AND the old-controller close run in the goroutine so
	// neither blocks the bubbletea event loop. The old controller's Close
	// kills plugin subprocesses (incl. CodeGraph), which can disrupt the
	// terminal's cancelReader if called synchronously inside Update — so it
	// must happen here, before we hand the new controller back.
	m.modelSwitchPending = true
	m.pendingModelSwitch = func() tea.Msg {
		c, err := build(controllerBuildSpec{
			ModelRef:         ref,
			ToolApprovalMode: oldCtrl.ToolApprovalMode(),
			PlanMode:         oldCtrl.PlanMode(),
		}, carried, prevPath, oldCtrl)
		if err != nil {
			return modelSwitchMsg{ref: ref, err: err}
		}
		// Do NOT close the old controller here. Controller.Close() runs
		// SessionEnd hooks (arbitrary shell commands) and kills plugin
		// subprocesses — operations that corrupt bubbletea's terminal raw
		// mode when executed from a goroutine. Instead, pass the old
		// controller back in the message so the Update handler can defer
		// its cleanup as a tea.Cmd that runs after the next render.
		return modelSwitchMsg{
			ref:      ref,
			ctrl:     c,
			oldCtrl:  oldCtrl,
			label:    c.Label(),
			commands: c.Commands(),
			skills:   c.SlashSkills(),
			host:     c.Host(),
		}
	}
}

func (m *chatTUI) openModelPicker() {
	var catalog []provider.Descriptor
	if m.ctrl != nil {
		catalog = m.ctrl.ProviderCatalog()
	}
	refs := mergeExtensionModelRefs(modelRefs(), catalog)
	if len(refs) == 0 {
		m.notice("model: no configured chat models")
		return
	}
	items := make([]quickPickerItem, 0, len(refs))
	selected := 0
	for _, ref := range refs {
		parts := strings.SplitN(ref, "/", 2)
		description := ""
		if len(parts) == 2 {
			description = "Provider: " + parts[0]
		}
		status := ""
		if ref == m.modelRef {
			status = "active"
			selected = len(items)
		}
		items = append(items, quickPickerItem{ID: ref, Label: ref, Description: description, Status: status})
	}
	m.quickPick = &quickPicker{kind: quickPickerModel, title: "Select model", items: items, selected: selected, digitSelect: m.quickPickerYolo()}
}

// persistModel writes ref (a "provider/model" string) to default_model in the
// user config.toml so the next CLI launch starts on the same
// model. The in-memory switch is always allowed to proceed regardless of the
// outcome here, but every step (rejected by validation, save failed, or
// persisted successfully) reports back to the TUI notice channel so the user
// can see whether their /model choice will survive a restart. Run before
// Snapshot/ModelSwitchingFmt so the persistence outcome shows up first in
// the notice area.
func (m *chatTUI) persistModel(ref string) {
	path := config.UserConfigPath()
	if path == "" {
		return
	}
	// Serialize the load-modify-save against other in-process user-config
	// editors so concurrent writers don't drop each other's fields.
	unlock := config.LockUserConfigEdits()
	defer unlock()
	edit := config.LoadForEdit(path)
	if err := edit.SetDefaultModel(ref); err != nil {
		m.notice(fmt.Sprintf("model: persist refused: %v (ref=%s)", err, ref))
		return
	}
	if err := edit.SaveTo(path); err != nil {
		m.notice(fmt.Sprintf("model: persist save failed: %v (ref=%s, path=%s)", err, ref, path))
		return
	}
	m.notice(fmt.Sprintf("model: persisted (ref=%s, path=%s)", ref, path))
}

// modelRefs returns the configured provider/model refs for slash completion.
func modelRefs() []string {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	var out []string
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if !p.Configured() {
			continue
		}
		for _, model := range p.ChatModelList() {
			out = append(out, p.Name+"/"+model)
		}
	}
	return out
}

// mergeExtensionModelRefs folds the session's extension provider catalog into
// the config-backed picker list. Extension refs arrive fully namespaced
// (plugin/<plugin>/<provider>/<model>); entries already listed (or blank) are
// dropped so a claim-replaced config ref never appears twice. A nil catalog
// returns base unchanged — the no-extension path is untouched.
func mergeExtensionModelRefs(base []string, catalog []provider.Descriptor) []string {
	if len(catalog) == 0 {
		return base
	}
	seen := make(map[string]bool, len(base)+len(catalog))
	out := make([]string, 0, len(base)+len(catalog))
	for _, ref := range base {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	for _, d := range catalog {
		ref := strings.TrimSpace(d.Ref)
		if ref == "" || providerext.PluginRefOwner(ref) == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}

// providerNames returns the names of configured providers for slash completion.
func providerNames() []string {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	var out []string
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if !p.Configured() {
			continue
		}
		out = append(out, p.Name)
	}
	return out
}
