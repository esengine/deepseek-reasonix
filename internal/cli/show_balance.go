package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/config"
	"reasonix/internal/i18n"
)

// runShowBalanceCommand handles "/show-balance [all|part|no]". Without an
// argument it prints the current mode; with one it persists the mode to the
// user config (so it survives sessions) and refreshes the status-bar readout.
func (m *chatTUI) runShowBalanceCommand(input string) tea.Cmd {
	args := tokenizeArgs(input)
	if len(args) > 2 {
		m.notice(i18n.M.ShowBalanceHint)
		return nil
	}
	if len(args) < 2 {
		cfg, err := config.Load()
		if err != nil {
			m.notice("show-balance: " + err.Error())
			return nil
		}
		m.notice(i18n.M.ShowBalanceHeader + "\n" +
			describeShowBalanceModes(string(cfg.ShowBalanceMode())) + "\n" +
			i18n.M.ShowBalanceHint)
		return nil
	}
	mode, err := parseShowBalanceArg(args[1])
	if err != nil {
		m.notice(err.Error())
		return nil
	}

	path := config.UserConfigPath()
	if path == "" {
		m.notice("show-balance: cannot resolve user config path")
		return nil
	}
	if err := func() error {
		unlock := config.LockUserConfigEdits()
		defer unlock()
		edit := config.LoadForEdit(path)
		if err := edit.SetShowBalanceMode(mode); err != nil {
			return err
		}
		return edit.SaveTo(path)
	}(); err != nil {
		m.notice("show-balance: " + err.Error())
		return nil
	}

	m.notice(fmt.Sprintf(i18n.M.ShowBalanceChangedFmt, mode))
	if m.ctrl == nil {
		return nil
	}
	// Re-fetch the balance so the status bar reflects the new mode at once.
	return fetchBalance(m.ctrl)
}

func parseShowBalanceArg(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "all", "full":
		return "all", nil
	case "part", "partial", "mask":
		return "part", nil
	case "no", "none", "off", "hide":
		return "no", nil
	default:
		return "", fmt.Errorf("show-balance %q: must be all|part|no", value)
	}
}

func describeShowBalanceModes(current string) string {
	items := []string{"all", "part", "no"}
	var b strings.Builder
	for _, item := range items {
		marker := "  "
		if item == current {
			marker = "• "
		}
		fmt.Fprintf(&b, "%s%s\n", marker, item)
	}
	return strings.TrimRight(b.String(), "\n")
}
