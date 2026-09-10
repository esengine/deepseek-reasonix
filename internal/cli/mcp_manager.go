package cli

import (
	"sort"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/config"
	"reasonix/internal/mcpdiag"
	"reasonix/internal/plugin"
)

const (
	mcpListMaxRows = 10
	mcpToolMaxRows = 14
)

type mcpStage int

const (
	mcpStageList mcpStage = iota
	mcpStageDetail
	mcpStageTools
	mcpStageLogs
	mcpStageMode
	mcpStageConfirmRemove
	mcpStageConfirmClearAuth
)

type mcpManager struct {
	stage    mcpStage
	snapshot mcpSnapshot
	sel      int
	name     string
	action   int
	mode     int
	confirm  int
}

type mcpSnapshot struct {
	servers    []mcpServerView
	configPath string
	err        string
}

type mcpServerView struct {
	Name       string
	Transport  string
	Status     string
	BuiltIn    bool
	Configured bool
	AutoStart  bool
	Tier       string
	Command    string
	Args       []string
	URL        string
	EnvKeys    []string
	Tools      int
	Prompts    int
	Resources  int
	HasTools   bool
	Error      string
	ToolList   []plugin.ToolInfo
	AuthStatus string
	AuthURL    string
	Source     config.MCPConfigSource
	ConfigPath string

	authConfigured bool
}

type mcpAction string

const (
	mcpActionViewTools mcpAction = "view-tools"
	mcpActionMode      mcpAction = "mode"
	mcpActionEdit      mcpAction = "edit"
	mcpActionConnect   mcpAction = "connect"
	mcpActionAuth      mcpAction = "auth"
	mcpActionClearAuth mcpAction = "clear-auth"
	mcpActionLogs      mcpAction = "logs"
	mcpActionDisable   mcpAction = "disable"
	mcpActionRemove    mcpAction = "remove"
)

type mcpActionItem struct {
	kind  mcpAction
	label string
}

type mcpExternalDoneMsg struct {
	label  string
	target string
	server string
	err    error
}

var mcpTierChoices = []string{"background", "eager"}

func (m *chatTUI) openMCPManager(name string) {
	m.mcp = &mcpManager{stage: mcpStageList, snapshot: m.buildMCPSnapshot()}
	if name != "" {
		m.mcp.selectName(name)
		m.mcp.stage = mcpStageDetail
	}
	m.mcp.clamp()
}

func (m *chatTUI) refreshMCPManager() {
	if m.mcp == nil {
		return
	}
	m.mcp.snapshot = m.buildMCPSnapshot()
	m.mcp.clamp()
}

func (m chatTUI) handleMCPManagerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.mcp
	if p == nil {
		return m, nil
	}
	switch msg.String() {
	case "ctrl+c", "q":
		m.mcp = nil
		return m, nil
	case "esc", "left", "h":
		switch p.stage {
		case mcpStageList:
			m.mcp = nil
			return m, nil
		case mcpStageDetail:
			p.stage = mcpStageList
			p.action = 0
			return m, nil
		default:
			p.stage = mcpStageDetail
			p.action = 0
			if p.name == "" {
				p.stage = mcpStageList
			}
			return m, nil
		}
	}

	switch p.stage {
	case mcpStageList:
		switch msg.String() {
		case "up", "k":
			if p.sel > 0 {
				p.sel--
			}
		case "down", "j":
			if p.sel < len(p.snapshot.servers)-1 {
				p.sel++
			}
		case "r":
			p.snapshot = m.buildMCPSnapshot()
		case "enter", "right", "l":
			if len(p.snapshot.servers) > 0 {
				p.name = p.snapshot.servers[p.sel].Name
				p.stage = mcpStageDetail
				p.action = 0
			}
		}
	case mcpStageDetail:
		v, ok := p.selectedServer()
		if !ok {
			p.stage = mcpStageList
			return m, nil
		}
		actions := mcpActionsFor(v, p.snapshot.configPath)
		switch msg.String() {
		case "up", "k":
			if p.action > 0 {
				p.action--
			}
		case "down", "j":
			if p.action < len(actions)-1 {
				p.action++
			}
		case "enter":
			if len(actions) > 0 {
				return m.applyMCPAction(v, actions[p.action].kind)
			}
		default:
			if idx, ok := numberKeyIndex(msg.String(), len(actions)); ok {
				p.action = idx
				return m.applyMCPAction(v, actions[p.action].kind)
			}
		}
	case mcpStageMode:
		switch msg.String() {
		case "up", "k":
			if p.mode > 0 {
				p.mode--
			}
		case "down", "j":
			if p.mode < len(mcpTierChoices)-1 {
				p.mode++
			}
		case "enter":
			return m.applyMCPMode(mcpTierChoices[p.mode])
		default:
			if idx, ok := numberKeyIndex(msg.String(), len(mcpTierChoices)); ok {
				p.mode = idx
				return m.applyMCPMode(mcpTierChoices[p.mode])
			}
		}
	case mcpStageConfirmRemove:
		switch msg.String() {
		case "up", "k", "down", "j":
			if p.confirm == 0 {
				p.confirm = 1
			} else {
				p.confirm = 0
			}
		case "y":
			p.confirm = 0
			return m.removeSelectedMCP()
		case "n":
			p.stage = mcpStageDetail
		case "enter":
			if p.confirm == 0 {
				return m.removeSelectedMCP()
			}
			p.stage = mcpStageDetail
		default:
			if idx, ok := numberKeyIndex(msg.String(), 2); ok {
				if idx == 0 {
					p.confirm = 0
					return m.removeSelectedMCP()
				}
				p.stage = mcpStageDetail
			}
		}
	case mcpStageConfirmClearAuth:
		switch msg.String() {
		case "up", "k", "down", "j":
			if p.confirm == 0 {
				p.confirm = 1
			} else {
				p.confirm = 0
			}
		case "y":
			p.confirm = 0
			return m.clearSelectedMCPAuthentication()
		case "n":
			p.stage = mcpStageDetail
		case "enter":
			if p.confirm == 0 {
				return m.clearSelectedMCPAuthentication()
			}
			p.stage = mcpStageDetail
		default:
			if idx, ok := numberKeyIndex(msg.String(), 2); ok {
				if idx == 0 {
					p.confirm = 0
					return m.clearSelectedMCPAuthentication()
				}
				p.stage = mcpStageDetail
			}
		}
	}
	return m, nil
}

func (p *mcpManager) clamp() {
	if p.sel < 0 {
		p.sel = 0
	}
	if n := len(p.snapshot.servers); n > 0 && p.sel >= n {
		p.sel = n - 1
	}
	if p.name != "" {
		p.selectName(p.name)
	}
	if p.action < 0 {
		p.action = 0
	}
	if p.mode < 0 {
		p.mode = 0
	}
	if p.mode >= len(mcpTierChoices) {
		p.mode = len(mcpTierChoices) - 1
	}
	if p.confirm < 0 || p.confirm > 1 {
		p.confirm = 0
	}
}

func (p *mcpManager) selectName(name string) bool {
	for i, s := range p.snapshot.servers {
		if s.Name == name {
			p.sel = i
			p.name = name
			return true
		}
	}
	return false
}

func (p *mcpManager) selectedServer() (mcpServerView, bool) {
	if p.name != "" {
		for _, s := range p.snapshot.servers {
			if s.Name == p.name {
				return s, true
			}
		}
	}
	if p.sel >= 0 && p.sel < len(p.snapshot.servers) {
		return p.snapshot.servers[p.sel], true
	}
	return mcpServerView{}, false
}

func (m chatTUI) buildMCPSnapshot() mcpSnapshot {
	workspace := m.mcpWorkspaceRoot()
	snap := mcpSnapshot{configPath: config.UserConfigPath()}
	cfg, err := config.LoadForRoot(workspace)
	if err != nil {
		snap.err = err.Error()
	}
	configured := map[string]config.PluginEntry{}
	var configuredEntries []config.PluginEntry
	if cfg != nil {
		configuredEntries = append(configuredEntries, cfg.Plugins...)
		for _, p := range configuredEntries {
			configured[p.Name] = p
		}
	}
	seen := map[string]bool{}
	if m.host != nil {
		for _, s := range m.host.Servers() {
			v := mcpServerView{
				Name: s.Name, Transport: fallbackText(s.Transport, "stdio"), Status: "connected",
				Tools: s.Tools, Prompts: s.Prompts, Resources: s.Resources,
				HasTools: s.HasTools,
				ToolList: append([]plugin.ToolInfo(nil), s.ToolList...),
			}
			if p, ok := configured[s.Name]; ok {
				v = withMCPPluginConfig(v, p, workspace)
			}
			snap.servers = append(snap.servers, v)
			seen[s.Name] = true
		}
		for _, f := range m.host.Failures() {
			v := mcpServerView{
				Name: f.Name, Transport: fallbackText(f.Transport, "stdio"), Status: "failed",
				Error: f.Error,
			}
			if p, ok := configured[f.Name]; ok {
				v = withMCPPluginConfig(v, p, workspace)
			}
			snap.servers = append(snap.servers, v)
			seen[f.Name] = true
		}
		for _, name := range m.host.ConnectingServers() {
			if seen[name] {
				continue
			}
			v := mcpServerView{Name: name, Status: "initializing"}
			if p, ok := configured[name]; ok {
				v = withMCPPluginConfig(v, p, workspace)
			}
			snap.servers = append(snap.servers, v)
			seen[name] = true
		}
	}
	for _, p := range configuredEntries {
		if seen[p.Name] {
			continue
		}
		v := mcpServerView{Name: p.Name}
		switch {
		case m.mcpDisabled[p.Name] || !p.ShouldAutoStart():
			v.Status = "disabled"
		default:
			v.Status = "deferred"
		}
		v = withMCPPluginConfig(v, p, workspace)
		snap.servers = append(snap.servers, v)
		seen[p.Name] = true
	}
	sort.SliceStable(snap.servers, func(i, j int) bool {
		return mcpServerGroupRank(snap.servers[i]) < mcpServerGroupRank(snap.servers[j])
	})
	return snap
}

func (m chatTUI) mcpWorkspaceRoot() string {
	if m.ctrl != nil && strings.TrimSpace(m.ctrl.WorkspaceRoot()) != "" {
		return m.ctrl.WorkspaceRoot()
	}
	return mcpCLIWorkspaceRoot()
}

func withMCPPluginConfig(v mcpServerView, p config.PluginEntry, workspace string) mcpServerView {
	transport := strings.ToLower(strings.TrimSpace(p.Type))
	if transport == "" {
		transport = "stdio"
	}
	v.Transport = transport
	v.Configured = true
	v.AutoStart = p.ShouldAutoStart()
	v.Tier = p.ResolvedTier()
	v.Command = p.Command
	v.Args = append([]string(nil), p.Args...)
	v.URL = p.URL
	v.Source = p.Source
	v.ConfigPath = config.MCPConfigPathForEntry(workspace, p)
	v.authConfigured = mcpdiag.HasAuthConfig(p.Headers, p.Env, p.URL)
	if len(p.Env) > 0 {
		v.EnvKeys = make([]string, 0, len(p.Env))
		for k := range p.Env {
			v.EnvKeys = append(v.EnvKeys, k)
		}
		sort.Strings(v.EnvKeys)
	}
	auth := mcpdiag.DiagnoseAuth(v.Transport, v.Status, v.Error, v.URL, v.authConfigured)
	v.AuthStatus = auth.Status
	v.AuthURL = auth.URL
	return v
}

func mcpServerGroupRank(v mcpServerView) int {
	switch {
	case v.BuiltIn || v.Source == config.MCPSourcePluginPackage:
		return 0
	case v.Source.ProjectScoped():
		return 1
	default:
		return 2
	}
}

func visibleRange(total, sel, limit int) (int, int) {
	if limit <= 0 || total <= limit {
		return 0, total
	}
	if sel < 0 {
		sel = 0
	}
	if sel >= total {
		sel = total - 1
	}
	start := max(sel-limit/2, 0)
	if start+limit > total {
		start = total - limit
	}
	return start, start + limit
}

func numberKeyIndex(s string, limit int) (int, bool) {
	if len(s) != 1 || s[0] < '1' || s[0] > '9' {
		return 0, false
	}
	idx := int(s[0] - '1')
	return idx, idx < limit
}

func fallbackText(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func titleText(s string) string {
	if s == "" {
		return "MCP"
	}
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && size == 0 {
		return s
	}
	return strings.ToUpper(string(r)) + s[size:]
}
