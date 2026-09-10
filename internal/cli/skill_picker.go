package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"reasonix/internal/i18n"
	"reasonix/internal/skill"
)

var scopePriority = map[skill.Scope]int{
	skill.ScopeProject: 0,
	skill.ScopeCustom:  1,
	skill.ScopeGlobal:  2,
	skill.ScopeBuiltin: 3,
}

type skillPickerMode string

const (
	pickerSkills        skillPickerMode = "skills"
	pickerSources       skillPickerMode = "sources"
	pickerSourceSkills  skillPickerMode = "source-skills"
	pickerDetail        skillPickerMode = "detail"
	pickerConfirmDelete skillPickerMode = "confirm-delete"
)

type skillPicker struct {
	mode            skillPickerMode
	skills          []skill.Skill
	roots           []skillRootLine
	enabled         map[string]bool
	originalEnabled map[string]bool
	query           string
	sel             int
	sourceSel       int
	sourceSkillSel  int
	showDiagnostics bool
	searchActive    bool
	detailSkill     skill.Skill
	detailBack      skillPickerMode
	detailAction    int
	confirm         int
	deleteSkill     skill.Skill
}

type skillRootLine struct {
	dir        string
	scope      skill.Scope
	status     skill.PathStatus
	skills     int
	configured bool
	diagnostic bool
}

func (m *chatTUI) openSkillPicker() {
	st := m.skillStore()
	skills := st.List()
	if m.ctrl != nil {
		skills = m.ctrl.AllSkills()
	}
	if len(skills) == 0 {
		m.notice(i18n.M.ListSkillsNone)
		return
	}
	sorted := sortedSkills(skills)
	enabled := map[string]bool{}
	original := map[string]bool{}
	disabled := m.disabledSkillNames()
	for _, sk := range sorted {
		on := !disabled[sk.Name]
		enabled[sk.Name] = on
		original[sk.Name] = on
	}
	m.skillPick = &skillPicker{
		mode:            pickerSkills,
		skills:          sorted,
		roots:           skillRootLines(st, sorted),
		enabled:         enabled,
		originalEnabled: original,
	}
}

func (m chatTUI) handleSkillPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.skillPick
	if p == nil {
		return m, nil
	}

	if p.searchActive {
		switch msg.String() {
		case "esc":
			p.searchActive = false
			return m, nil
		case "enter":
			return m.saveSkillPick()
		case "backspace":
			if len(p.query) > 0 {
				p.query = p.query[:len(p.query)-1]
				p.sel = clampSel(p.sel, p.filteredSkills())
			}
			return m, nil
		case "up", "k":
			if p.sel > 0 {
				p.sel--
			}
			return m, nil
		case "down", "j":
			filtered := p.filteredSkills()
			if p.sel < len(filtered)-1 {
				p.sel++
			}
			return m, nil
		default:
			if t := msg.Text; t != "" {
				p.query += t
			} else if s := msg.String(); len(s) == 1 && s[0] >= 32 && s[0] < 127 {
				p.query += s
			}
			p.sel = clampSel(p.sel, p.filteredSkills())
			return m, nil
		}
	}

	switch p.mode {
	case pickerSkills:
		return m.handleSkillPickerSkillsKey(msg)
	case pickerSources:
		return m.handleSkillPickerSourcesKey(msg)
	case pickerSourceSkills:
		return m.handleSkillPickerSourceSkillsKey(msg)
	case pickerDetail:
		return m.handleSkillPickerDetailKey(msg)
	case pickerConfirmDelete:
		return m.handleSkillPickerConfirmDeleteKey(msg)
	}
	return m, nil
}

func (m chatTUI) handleSkillPickerSkillsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.skillPick
	switch msg.String() {
	case "esc":
		m.skillPick = nil
	case "up", "k":
		if p.sel > 0 {
			p.sel--
		}
	case "down", "j":
		if p.sel < len(p.skills)-1 {
			p.sel++
		}
	case "enter":
		return m.saveSkillPick()
	case " ", "space":
		p.toggleSelectedSkill()
	case "right", "l":
		if sk, ok := p.selectedSkill(); ok {
			p.openDetail(sk, pickerSkills)
		}
	case "/":
		p.searchActive = true
		p.query = ""
	case "s":
		p.mode = pickerSources
		p.searchActive = false
		p.sourceSel = 0
	case "r":
		m.rescanSkills()
	}
	return m, nil
}

func (m chatTUI) handleSkillPickerSourcesKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.skillPick
	visible := p.visibleRoots()
	switch msg.String() {
	case "esc":
		m.skillPick = nil
	case "up", "k":
		if p.sourceSel > 0 {
			p.sourceSel--
		}
	case "down", "j":
		if p.sourceSel < len(visible)-1 {
			p.sourceSel++
		}
	case "enter", "right", "l":
		if len(visible) > 0 {
			p.mode = pickerSourceSkills
			p.sourceSkillSel = 0
		}
	case "d":
		p.showDiagnostics = !p.showDiagnostics
		p.sourceSel = clampSel(p.sourceSel, visible)
	case "s":
		p.mode = pickerSkills
		p.sourceSel = 0
		p.showDiagnostics = false
	case "r":
		m.rescanSkills()
	default:
		if idx, ok := numberKeyIndex(msg.String(), len(visible)); ok {
			p.sourceSel = idx
			p.mode = pickerSourceSkills
			p.sourceSkillSel = 0
		}
	}
	return m, nil
}

func (m chatTUI) handleSkillPickerSourceSkillsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.skillPick
	skills := p.selectedRootSkills()
	switch msg.String() {
	case "esc", "left", "h":
		p.mode = pickerSources
	case "up", "k":
		if p.sourceSkillSel > 0 {
			p.sourceSkillSel--
		}
	case "down", "j":
		if p.sourceSkillSel < len(skills)-1 {
			p.sourceSkillSel++
		}
	case "enter", "right", "l":
		if p.sourceSkillSel >= 0 && p.sourceSkillSel < len(skills) {
			p.openDetail(skills[p.sourceSkillSel], pickerSourceSkills)
		}
	case " ", "space":
		if p.sourceSkillSel >= 0 && p.sourceSkillSel < len(skills) {
			p.toggleSkill(skills[p.sourceSkillSel].Name)
		}
	}
	p.sourceSkillSel = clampSel(p.sourceSkillSel, skills)
	return m, nil
}

func (m chatTUI) handleSkillPickerDetailKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.skillPick
	actions := skillActionsFor(p.detailSkill)
	p.detailAction = clampInt(p.detailAction, len(actions))
	switch msg.String() {
	case "esc", "left", "h":
		p.mode = p.detailBack
		p.detailAction = 0
	case "up", "k":
		if p.detailAction > 0 {
			p.detailAction--
		}
	case "down", "j":
		if p.detailAction < len(actions)-1 {
			p.detailAction++
		}
	case "enter":
		if len(actions) > 0 {
			return m.applySkillAction(p.detailSkill, actions[p.detailAction])
		}
	case " ", "space":
		p.toggleSkill(p.detailSkill.Name)
	default:
		if idx, ok := numberKeyIndex(msg.String(), len(actions)); ok {
			p.detailAction = idx
			return m.applySkillAction(p.detailSkill, actions[idx])
		}
	}
	p.detailAction = clampInt(p.detailAction, len(actions))
	return m, nil
}

func (m chatTUI) handleSkillPickerConfirmDeleteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.skillPick
	switch msg.String() {
	case "up", "k", "down", "j":
		if p.confirm == 0 {
			p.confirm = 1
		} else {
			p.confirm = 0
		}
	case "y":
		p.confirm = 0
		return m.deleteSkillPick(p.deleteSkill)
	case "n", "esc":
		p.mode = pickerDetail
	case "enter":
		if p.confirm == 0 {
			return m.deleteSkillPick(p.deleteSkill)
		}
		p.mode = pickerDetail
	default:
		if idx, ok := numberKeyIndex(msg.String(), 2); ok {
			if idx == 0 {
				p.confirm = 0
				return m.deleteSkillPick(p.deleteSkill)
			}
			p.mode = pickerDetail
		}
	}
	return m, nil
}

func (m chatTUI) saveSkillPick() (tea.Model, tea.Cmd) {
	p := m.skillPick
	if p == nil {
		return m, nil
	}
	changes := p.changedEnabled()
	m.skillPick = nil
	if len(changes) == 0 {
		m.notice(i18n.M.SkillPickerNoChanges)
		return m, nil
	}
	m.skillSaveEnabledChanges(changes)
	if m.pendingModelSwitch != nil {
		return m, m.pendingModelSwitch
	}
	return m, nil
}

func (m chatTUI) deleteSkillPick(sk skill.Skill) (tea.Model, tea.Cmd) {
	p := m.skillPick
	target, ok, err := skillDeleteTarget(sk)
	if err != nil {
		m.notice("skill delete: " + err.Error())
		if p != nil {
			p.mode = pickerDetail
		}
		return m, nil
	}
	if !ok {
		m.notice("skill delete: built-in skills cannot be removed")
		if p != nil {
			p.mode = pickerDetail
		}
		return m, nil
	}
	if err := os.RemoveAll(target); err != nil {
		m.notice("skill delete: " + err.Error())
		if p != nil {
			p.mode = pickerDetail
		}
		return m, nil
	}
	if p != nil {
		delete(p.enabled, sk.Name)
		delete(p.originalEnabled, sk.Name)
		p.mode = pickerSkills
		p.detailAction = 0
	}
	m.notice(fmt.Sprintf(i18n.M.SkillPickerDeletedFmt, sk.Name))
	m.refreshSkillPickerData()
	m.scheduleSkillSessionRefresh("skill delete", "deleted skill "+sk.Name+" — refreshing session")
	return m, m.pendingModelSwitch
}

func (m *chatTUI) rescanSkills() {
	m.refreshSkillPickerData()
	m.notice(i18n.M.SkillPickerRescanned)
}

func (m *chatTUI) refreshSkillPickerData() {
	st := m.skillStore()
	skills := st.List()
	m.skills = skills
	m.invalidateSlashCatalog()
	if m.skillPick != nil {
		sorted := sortedSkills(skills)
		m.skillPick.skills = sorted
		m.skillPick.roots = skillRootLines(st, sorted)
		m.skillPick.syncEnabledMaps(sorted, m.disabledSkillNames())
		if m.skillPick.searchActive && m.skillPick.query != "" {
			m.skillPick.sel = clampSel(m.skillPick.sel, m.skillPick.filteredSkills())
		} else {
			m.skillPick.sel = clampSel(m.skillPick.sel, sorted)
		}
		m.skillPick.sourceSel = clampSel(m.skillPick.sourceSel, m.skillPick.visibleRoots())
		m.skillPick.sourceSkillSel = clampSel(m.skillPick.sourceSkillSel, m.skillPick.selectedRootSkills())
	}
}

func (p *skillPicker) filteredSkills() []skill.Skill {
	if p.query == "" {
		return p.skills
	}
	q := strings.ToLower(p.query)
	var out []skill.Skill
	for _, s := range p.skills {
		if strings.Contains(strings.ToLower(s.SlashName()), q) ||
			strings.Contains(strings.ToLower(s.Plugin), q) ||
			strings.Contains(strings.ToLower(s.Description), q) {
			out = append(out, s)
		}
	}
	return out
}

func (p *skillPicker) visibleRoots() []skillRootLine {
	if p.showDiagnostics {
		return p.roots
	}
	var out []skillRootLine
	for _, r := range p.roots {
		if !r.diagnostic || r.configured || r.skills > 0 {
			out = append(out, r)
		}
	}
	return out
}

func (p *skillPicker) selectedSkill() (skill.Skill, bool) {
	if p == nil {
		return skill.Skill{}, false
	}
	skills := p.skills
	if p.searchActive && p.query != "" {
		skills = p.filteredSkills()
	}
	if len(skills) == 0 {
		return skill.Skill{}, false
	}
	p.sel = clampSel(p.sel, skills)
	return skills[p.sel], true
}

func (p *skillPicker) toggleSelectedSkill() {
	if sk, ok := p.selectedSkill(); ok {
		p.toggleSkill(sk.Name)
	}
}

func (p *skillPicker) openDetail(sk skill.Skill, back skillPickerMode) {
	p.detailSkill = sk
	p.detailBack = back
	p.detailAction = 0
	p.mode = pickerDetail
}

func (p *skillPicker) skillEnabled(name string) bool {
	if p == nil || p.enabled == nil {
		return true
	}
	if enabled, ok := p.enabled[name]; ok {
		return enabled
	}
	return true
}

func (p *skillPicker) toggleSkill(name string) {
	if p.enabled == nil {
		p.enabled = map[string]bool{}
	}
	if p.originalEnabled == nil {
		p.originalEnabled = map[string]bool{}
	}
	if _, ok := p.originalEnabled[name]; !ok {
		p.originalEnabled[name] = p.skillEnabled(name)
	}
	p.enabled[name] = !p.skillEnabled(name)
}

func (p *skillPicker) changedEnabled() map[string]bool {
	changes := map[string]bool{}
	if p == nil {
		return changes
	}
	for name, enabled := range p.enabled {
		original, ok := p.originalEnabled[name]
		if !ok {
			original = true
		}
		if enabled != original {
			changes[name] = enabled
		}
	}
	return changes
}

func (p *skillPicker) syncEnabledMaps(skills []skill.Skill, disabled map[string]bool) {
	nextEnabled := map[string]bool{}
	nextOriginal := map[string]bool{}
	for _, sk := range skills {
		enabled := !disabled[sk.Name]
		if p.enabled != nil {
			if existing, ok := p.enabled[sk.Name]; ok {
				enabled = existing
			}
		}
		original := !disabled[sk.Name]
		if p.originalEnabled != nil {
			if existing, ok := p.originalEnabled[sk.Name]; ok {
				original = existing
			}
		}
		nextEnabled[sk.Name] = enabled
		nextOriginal[sk.Name] = original
	}
	p.enabled = nextEnabled
	p.originalEnabled = nextOriginal
}

func (p *skillPicker) selectedRoot() (skillRootLine, bool) {
	if p == nil {
		return skillRootLine{}, false
	}
	roots := p.visibleRoots()
	if len(roots) == 0 {
		return skillRootLine{}, false
	}
	p.sourceSel = clampSel(p.sourceSel, roots)
	return roots[p.sourceSel], true
}

func (p *skillPicker) selectedRootSkills() []skill.Skill {
	root, ok := p.selectedRoot()
	if !ok {
		return nil
	}
	var out []skill.Skill
	for _, sk := range p.skills {
		if skillInRoot(sk, root) {
			out = append(out, sk)
		}
	}
	return out
}

func skillInRoot(sk skill.Skill, root skillRootLine) bool {
	if root.scope == skill.ScopeBuiltin {
		return sk.Scope == skill.ScopeBuiltin
	}
	if sk.Scope != root.scope || sk.Path == "" || sk.Scope == skill.ScopeBuiltin {
		return false
	}
	cleanPath := filepath.Clean(sk.Path)
	cleanRoot := filepath.Clean(root.dir)
	prefix := cleanRoot + string(filepath.Separator)
	return cleanPath == cleanRoot || strings.HasPrefix(cleanPath, prefix)
}

func skillRootLines(st *skill.Store, skills []skill.Skill) []skillRootLine {
	storeRoots := st.Roots()
	lines := make([]skillRootLine, len(storeRoots))
	for i, r := range storeRoots {
		lines[i] = skillRootLine{
			dir:    r.Dir,
			scope:  r.Scope,
			status: r.Status,
		}
	}
	for _, s := range skills {
		if s.Scope == skill.ScopeBuiltin {
			continue
		}
		cleanPath := filepath.Clean(s.Path)
		for i := range lines {
			if lines[i].scope != s.Scope {
				continue
			}
			// Trailing separator: directory-boundary match so /skills can't match /skills-extra.
			prefix := filepath.Clean(lines[i].dir) + string(filepath.Separator)
			if strings.HasPrefix(cleanPath, prefix) {
				lines[i].skills++
				break
			}
		}
	}
	for i := range lines {
		if lines[i].scope == skill.ScopeCustom {
			lines[i].configured = true
		} else {
			lines[i].diagnostic = true
		}
	}
	builtinCount := 0
	for _, s := range skills {
		if s.Scope == skill.ScopeBuiltin {
			builtinCount++
		}
	}
	if builtinCount > 0 {
		lines = append(lines, skillRootLine{
			dir:        i18n.M.SkillPickerBuiltinSource,
			scope:      skill.ScopeBuiltin,
			status:     skill.StatusOK,
			skills:     builtinCount,
			diagnostic: true,
		})
	}
	return lines
}

type skillActionKind string

const (
	skillActionToggle skillActionKind = "toggle"
	skillActionDelete skillActionKind = "delete"
)

type skillActionItem struct {
	kind  skillActionKind
	label string
}

func skillActionsFor(s skill.Skill) []skillActionItem {
	actions := []skillActionItem{{
		kind:  skillActionToggle,
		label: i18n.M.SkillPickerActionToggle,
	}}
	if _, ok, _ := skillDeleteTarget(s); ok {
		actions = append(actions, skillActionItem{
			kind:  skillActionDelete,
			label: i18n.M.SkillPickerActionDelete,
		})
	}
	return actions
}

func (m chatTUI) applySkillAction(sk skill.Skill, action skillActionItem) (tea.Model, tea.Cmd) {
	p := m.skillPick
	if p == nil {
		return m, nil
	}
	switch action.kind {
	case skillActionToggle:
		p.toggleSkill(sk.Name)
	case skillActionDelete:
		p.deleteSkill = sk
		p.confirm = 1
		p.mode = pickerConfirmDelete
	}
	return m, nil
}

func skillDeleteTarget(s skill.Skill) (string, bool, error) {
	if s.Scope == skill.ScopeBuiltin {
		return "", false, nil
	}
	path := strings.TrimSpace(s.Path)
	if path == "" || path == "(builtin)" {
		return "", false, nil
	}
	clean := filepath.Clean(path)
	info, err := os.Stat(clean)
	if err != nil {
		return "", false, err
	}
	if filepath.Base(clean) == skill.SkillFile {
		dir := filepath.Dir(clean)
		if dir == "." || dir == string(filepath.Separator) {
			return "", false, fmt.Errorf("refusing to remove unsafe skill directory %q", dir)
		}
		return dir, true, nil
	}
	if info.Mode().IsRegular() {
		return clean, true, nil
	}
	return "", false, fmt.Errorf("skill path is not removable: %s", clean)
}

func skillDeleteTargetLabel(s skill.Skill) string {
	target, ok, _ := skillDeleteTarget(s)
	if !ok {
		return ""
	}
	return target
}

func sortedSkills(skills []skill.Skill) []skill.Skill {
	sorted := make([]skill.Skill, len(skills))
	copy(sorted, skills)
	sort.SliceStable(sorted, func(i, j int) bool {
		pi := scopePriority[sorted[i].Scope]
		pj := scopePriority[sorted[j].Scope]
		if pi != pj {
			return pi < pj
		}
		return sorted[i].Name < sorted[j].Name
	})
	return sorted
}

func clampSel[T any](sel int, items []T) int {
	if len(items) == 0 {
		return 0
	}
	if sel < 0 {
		return 0
	}
	if sel >= len(items) {
		return len(items) - 1
	}
	return sel
}

func clampInt(sel, total int) int {
	if total <= 0 {
		return 0
	}
	if sel < 0 {
		return 0
	}
	if sel >= total {
		return total - 1
	}
	return sel
}
