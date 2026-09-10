package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/config"
	"reasonix/internal/i18n"
)

// runProviderCommand handles "/provider": with no argument it opens the provider
// picker; "/provider <name>" switches to that
// provider's default model (or prompts the user to pick one when multiple models
// are configured).
func (m *chatTUI) runProviderCommand(input string) {
	args := tokenizeArgs(input) // args[0] == "/provider"
	if len(args) < 2 {
		m.openProviderPicker()
		return
	}
	name := args[1]
	m.switchToProvider(name)
}

func (m *chatTUI) openProviderPicker() {
	cfg, err := config.Load()
	if err != nil {
		m.notice("provider: " + err.Error())
		return
	}
	curProvider := strings.SplitN(m.modelRef, "/", 2)[0]
	var items []quickPickerItem
	selected := 0
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if !p.Configured() {
			continue
		}
		models := p.ChatModelList()
		if len(models) == 0 {
			models = p.ModelList()
		}
		status := ""
		if p.Name == curProvider {
			status = "active"
			selected = len(items)
		}
		items = append(items, quickPickerItem{
			ID: p.Name, Label: p.Name,
			Description: fmt.Sprintf("%s · %d model(s)", p.Kind, len(models)), Status: status,
		})
	}
	if len(items) == 0 {
		m.notice("provider: no configured providers")
		return
	}
	m.quickPick = &quickPicker{kind: quickPickerProvider, title: "Select provider", items: items, selected: selected, digitSelect: m.quickPickerYolo()}
}

// switchToProvider switches the session to the named provider's default model.
// If the provider has multiple models, it shows an interactive picker (in the
// setup/CLI style) if running in a TTY, or falls back to a notice listing
// available models.
func (m *chatTUI) switchToProvider(name string) {
	cfg, err := config.Load()
	if err != nil {
		m.notice("provider: " + err.Error())
		return
	}
	var entry *config.ProviderEntry
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.Name == name && p.Configured() {
			entry = p
			break
		}
	}
	if entry == nil {
		m.notice(fmt.Sprintf(i18n.M.ProviderUnknownFmt, name))
		return
	}

	// Determine current provider.
	curProvider := ""
	if parts := strings.SplitN(m.modelRef, "/", 2); len(parts) == 2 {
		curProvider = parts[0]
	}

	models := entry.ChatModelList()
	if len(models) == 0 {
		models = entry.ModelList()
	}
	if len(models) == 0 {
		m.notice(fmt.Sprintf(i18n.M.ProviderNoModelsFmt, name))
		return
	}

	// If only one model, switch directly.
	if len(models) == 1 {
		ref := entry.Name + "/" + models[0]
		if entry.Name == curProvider && models[0] == "" {
			m.notice(fmt.Sprintf(i18n.M.ProviderAlreadyOnFmt, name))
			return
		}
		m.runModelSubcommand("/model " + ref)
		return
	}

	items := make([]quickPickerItem, 0, len(models))
	selected := 0
	for _, model := range models {
		ref := entry.Name + "/" + model
		status := ""
		if ref == m.modelRef {
			status = "active"
			selected = len(items)
		}
		items = append(items, quickPickerItem{ID: ref, Label: model, Description: entry.Name, Status: status})
	}
	m.quickPick = &quickPicker{
		kind: quickPickerProviderModel, title: fmt.Sprintf(i18n.M.ProviderPickLabel, name),
		items: items, selected: selected, digitSelect: m.quickPickerYolo(),
	}
}

func (m chatTUI) handleQuickPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.quickPick
	if p == nil {
		return m, nil
	}
	result := p.handleKey(msg)
	if result.cancelled {
		m.quickPick = nil
		return m, nil
	}
	if result.choice == nil {
		return m, nil
	}
	kind := p.kind
	choice := *result.choice
	m.quickPick = nil
	switch kind {
	case quickPickerModel, quickPickerProviderModel:
		m.runModelSubcommand("/model " + choice.ID)
		if m.pendingModelSwitch != nil {
			return m, m.pendingModelSwitch
		}
	case quickPickerProvider:
		m.switchToProvider(choice.ID)
		if m.pendingModelSwitch != nil {
			return m, m.pendingModelSwitch
		}
	}
	return m, nil
}

func (m chatTUI) renderQuickPicker() string {
	if m.quickPick == nil {
		return ""
	}
	return m.quickPick.render(m.width)
}
