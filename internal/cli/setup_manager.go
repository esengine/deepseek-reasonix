package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/i18n"
	"reasonix/internal/netclient"
)

type providerSetupSession struct {
	cfg                *config.Config
	originalProviders  map[string]config.ProviderEntry
	originalDefault    string
	pendingCredentials map[string]string
	removed            map[string]bool
	accessDeclared     bool
	projectScoped      bool
	declaredProviders  []string
	operations         []providerSetupOperation
}

const setupManagerContinue = 2

type providerSetupOperationKind uint8

const (
	setupOpProvider providerSetupOperationKind = iota
	setupOpDefaultModel
	setupOpLanguage
	setupOpMaterializeAccess
	setupOpAccessMembership
)

type providerSetupOperation struct {
	kind           providerSetupOperationKind
	providerName   string
	beforeProvider *config.ProviderEntry
	afterProvider  *config.ProviderEntry
	beforeString   string
	afterString    string
	accessName     string
	projectScoped  bool
	beforeBool     bool
	afterBool      bool
}

type providerSetupConflictError struct {
	field string
}

type providerSetupFileSnapshot struct {
	exists bool
	body   []byte
}

func (e *providerSetupConflictError) Error() string {
	return e.field
}

func providerSetupEntryPtr(entry config.ProviderEntry) *config.ProviderEntry {
	copy := config.ProviderEntryConfigSnapshot(entry)
	return &copy
}

func readProviderSetupFileSnapshot(path string) (providerSetupFileSnapshot, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return providerSetupFileSnapshot{}, nil
		}
		return providerSetupFileSnapshot{}, err
	}
	return providerSetupFileSnapshot{exists: true, body: body}, nil
}

func providerSetupFileSnapshotEqual(a, b providerSetupFileSnapshot) bool {
	return a.exists == b.exists && bytes.Equal(a.body, b.body)
}

func newProviderSetupSession(cfg *config.Config) *providerSetupSession {
	s := &providerSetupSession{
		cfg:                cfg,
		originalProviders:  make(map[string]config.ProviderEntry, len(cfg.Providers)),
		originalDefault:    cfg.DefaultModel,
		pendingCredentials: map[string]string{},
		removed:            map[string]bool{},
	}
	for _, p := range cfg.Providers {
		s.originalProviders[p.Name] = p
	}
	return s
}

func newProviderSetupSessionForPath(cfg *config.Config, path string) *providerSetupSession {
	s := newProviderSetupSession(cfg)
	s.projectScoped = !config.IsUserConfigPath(path)
	declarations, err := config.InspectConfigFileDeclarations(path)
	if err != nil {
		// LoadForEdit already reports malformed/unreadable config and falls back;
		// keep the conservative policy here so setup never enables hidden siblings.
		s.accessDeclared = true
		return s
	}
	s.accessDeclared = declarations.DesktopProviderAccessDeclared
	s.declaredProviders = declarations.ProviderNames
	return s
}

func (s *providerSetupSession) recordProviderMutation(name string, before, after *config.ProviderEntry) {
	s.operations = append(s.operations, providerSetupOperation{
		kind:           setupOpProvider,
		providerName:   name,
		beforeProvider: before,
		afterProvider:  after,
	})
}

func (s *providerSetupSession) setLanguage(language string) {
	if s.cfg.Language == language {
		return
	}
	s.operations = append(s.operations, providerSetupOperation{
		kind:         setupOpLanguage,
		beforeString: s.cfg.Language,
		afterString:  language,
	})
	s.cfg.Language = language
}

func (s *providerSetupSession) applyDeepSeekOfficialDefaultPricing() {
	before := make(map[string]config.ProviderEntry, len(s.cfg.Providers))
	for _, provider := range s.cfg.Providers {
		before[provider.Name] = provider
	}
	s.cfg.ApplyDeepSeekOfficialDefaultPricing()
	for i := range s.cfg.Providers {
		after := s.cfg.Providers[i]
		previous, existed := before[after.Name]
		if existed && config.ProviderEntriesConfigEqual(previous, after) {
			delete(before, after.Name)
			continue
		}
		var previousPtr *config.ProviderEntry
		if existed {
			previousPtr = providerSetupEntryPtr(previous)
		}
		s.recordProviderMutation(after.Name, previousPtr, providerSetupEntryPtr(after))
		delete(before, after.Name)
	}
	for name, previous := range before {
		s.recordProviderMutation(name, providerSetupEntryPtr(previous), nil)
	}
}

func (s *providerSetupSession) resetProviderSummaryBaseline() {
	s.originalProviders = make(map[string]config.ProviderEntry, len(s.cfg.Providers))
	for _, provider := range s.cfg.Providers {
		s.originalProviders[provider.Name] = provider
	}
}

func (s *providerSetupSession) upsert(entries []config.ProviderEntry) error {
	for _, entry := range entries {
		var before *config.ProviderEntry
		if current, ok := s.cfg.Provider(entry.Name); ok {
			before = providerSetupEntryPtr(*current)
		}
		if err := s.cfg.UpsertProvider(entry); err != nil {
			return err
		}
		current, _ := s.cfg.Provider(entry.Name)
		if before == nil || !config.ProviderEntriesConfigEqual(*before, *current) {
			s.recordProviderMutation(entry.Name, before, providerSetupEntryPtr(*current))
		}
		delete(s.removed, entry.Name)
		s.repairDanglingDefaultFor(*current)
	}
	return nil
}

// repairDanglingDefaultFor re-points default_model at the provider's own default
// when an edit or model refresh dropped the exact model the ref named, mirroring
// the repair RemoveProvider performs on removal.
func (s *providerSetupSession) repairDanglingDefaultFor(p config.ProviderEntry) {
	if !config.ModelRefsProvider(s.cfg.DefaultModel, p.Name) || len(p.ModelList()) == 0 {
		return
	}
	if _, ok := s.cfg.ResolveModel(s.cfg.DefaultModel); ok {
		return
	}
	if err := s.setDefaultModel(p.Name); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func (s *providerSetupSession) add(entries []config.ProviderEntry) error {
	seen := make(map[string]bool, len(s.cfg.Providers)+len(entries))
	for _, provider := range s.cfg.Providers {
		seen[provider.Name] = true
	}
	for _, entry := range entries {
		if seen[entry.Name] {
			return fmt.Errorf(i18n.M.SetupProviderExistsFmt, entry.Name)
		}
		seen[entry.Name] = true
	}
	return s.upsert(entries)
}

func (s *providerSetupSession) remove(name string) error {
	current, ok := s.cfg.Provider(name)
	if !ok {
		return fmt.Errorf("remove provider: no provider %q", name)
	}
	before := providerSetupEntryPtr(*current)
	if err := s.cfg.RemoveProvider(name); err != nil {
		return err
	}
	s.recordProviderMutation(name, before, nil)
	s.removeProviderAccess(name)
	if _, existed := s.originalProviders[name]; existed {
		s.removed[name] = true
	}
	return nil
}

func (s *providerSetupSession) addProviderAccess(entries []config.ProviderEntry) {
	if len(entries) == 0 {
		return
	}
	before := append([]string(nil), s.cfg.Desktop.ProviderAccess...)
	// Preserve the legacy "undeclared means infer all configured providers"
	// behavior before turning provider_access into an explicit list. Project
	// setup only seeds providers declared by that project; cfg also contains
	// built-in defaults, which must not override the user's global access policy.
	if !s.accessDeclared && len(s.cfg.Desktop.ProviderAccess) == 0 {
		if s.projectScoped {
			for _, name := range s.declaredProviders {
				provider, ok := s.cfg.Provider(name)
				if ok && provider.Configured() && len(provider.ModelList()) > 0 {
					s.cfg.Desktop.ProviderAccess = append(s.cfg.Desktop.ProviderAccess, name)
				}
			}
		} else {
			config.NormalizeLegacyDesktopProviderAccess(s.cfg)
		}
		s.accessDeclared = true
		s.operations = append(s.operations, providerSetupOperation{
			kind:          setupOpMaterializeAccess,
			projectScoped: s.projectScoped,
		})
		before = append([]string(nil), s.cfg.Desktop.ProviderAccess...)
	}
	seen := make(map[string]bool, len(s.cfg.Desktop.ProviderAccess)+len(entries))
	for _, name := range s.cfg.Desktop.ProviderAccess {
		name = strings.TrimSpace(name)
		if name != "" {
			seen[name] = true
		}
	}
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		if name == "" || seen[name] {
			continue
		}
		s.cfg.Desktop.ProviderAccess = append(s.cfg.Desktop.ProviderAccess, name)
		seen[name] = true
	}
	s.accessDeclared = true
	s.recordAccessTransition(before)
}

func (s *providerSetupSession) removeProviderAccess(name string) {
	name = strings.TrimSpace(name)
	if name == "" || len(s.cfg.Desktop.ProviderAccess) == 0 {
		return
	}
	before := append([]string(nil), s.cfg.Desktop.ProviderAccess...)
	out := s.cfg.Desktop.ProviderAccess[:0]
	for _, current := range s.cfg.Desktop.ProviderAccess {
		if strings.TrimSpace(current) != name {
			out = append(out, current)
		}
	}
	s.cfg.Desktop.ProviderAccess = out
	s.recordAccessTransition(before)
}

func (s *providerSetupSession) recordAccessTransition(before []string) {
	beforeSet := make(map[string]bool, len(before))
	afterSet := make(map[string]bool, len(s.cfg.Desktop.ProviderAccess))
	var order []string
	seen := map[string]bool{}
	for _, names := range [][]string{before, s.cfg.Desktop.ProviderAccess} {
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if !seen[name] {
				seen[name] = true
				order = append(order, name)
			}
		}
	}
	for _, name := range before {
		name = strings.TrimSpace(name)
		if name != "" {
			beforeSet[name] = true
		}
	}
	for _, name := range s.cfg.Desktop.ProviderAccess {
		name = strings.TrimSpace(name)
		if name != "" {
			afterSet[name] = true
		}
	}
	for _, name := range order {
		if beforeSet[name] == afterSet[name] {
			continue
		}
		s.operations = append(s.operations, providerSetupOperation{
			kind:       setupOpAccessMembership,
			accessName: name,
			beforeBool: beforeSet[name],
			afterBool:  afterSet[name],
		})
	}
}

func (s *providerSetupSession) setCredential(key, value string) error {
	key = strings.TrimSpace(key)
	if !config.IsValidCredentialKey(key) {
		return fmt.Errorf("invalid API key variable name %q", key)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("API key for %s contains a newline", key)
	}
	s.pendingCredentials[key] = value
	return nil
}

func (s *providerSetupSession) setDefaultModel(model string) error {
	before := s.cfg.DefaultModel
	if err := s.cfg.SetDefaultModel(model); err != nil {
		return err
	}
	if before != s.cfg.DefaultModel {
		s.operations = append(s.operations, providerSetupOperation{
			kind:         setupOpDefaultModel,
			beforeString: before,
			afterString:  s.cfg.DefaultModel,
		})
	}
	return nil
}

// providerUsable reports whether the provider would be selectable once this
// session saves: it lists models and either needs no key or has one resolvable
// from the credential store or staged in this session.
func (s *providerSetupSession) providerUsable(p *config.ProviderEntry) bool {
	if p == nil || len(p.ModelList()) == 0 {
		return false
	}
	return p.Configured() || s.pendingCredentials[p.APIKeyEnv] != ""
}

// defaultModelUsable reports whether default_model resolves to a provider the
// user could actually run once this session saves.
func (s *providerSetupSession) defaultModelUsable() bool {
	entry, ok := s.cfg.ResolveModel(s.cfg.DefaultModel)
	return ok && s.providerUsable(entry)
}

// promoteDefaultToNewProviders keeps the wizard's first-run contract: when the
// current default_model cannot run (unresolvable, or its key is neither stored
// nor staged), point it at the first usable provider the user just added, so a
// first run that only configures a custom provider boots on that provider
// instead of failing on the built-in default's missing key. A usable default is
// never hijacked.
func (s *providerSetupSession) promoteDefaultToNewProviders(entries []config.ProviderEntry) {
	if s.defaultModelUsable() {
		return
	}
	for _, entry := range entries {
		current, ok := s.cfg.Provider(entry.Name)
		if !ok || !s.providerUsable(current) {
			continue
		}
		if err := s.setDefaultModel(current.Name); err == nil {
			return
		}
	}
}

func (s *providerSetupSession) credentialLines() []string {
	keys := make([]string, 0, len(s.pendingCredentials))
	for key := range s.pendingCredentials {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+s.pendingCredentials[key])
	}
	return lines
}

func (s *providerSetupSession) summary() []string {
	var added, edited []string
	for _, p := range s.cfg.Providers {
		old, existed := s.originalProviders[p.Name]
		switch {
		case !existed:
			added = append(added, p.Name)
		case !providerSetupEqual(old, p):
			edited = append(edited, p.Name)
		}
	}
	var out []string
	if len(added) > 0 {
		out = append(out, fmt.Sprintf(i18n.M.SetupSummaryAddedFmt, strings.Join(added, ", ")))
	}
	if len(edited) > 0 {
		out = append(out, fmt.Sprintf(i18n.M.SetupSummaryEditedFmt, strings.Join(edited, ", ")))
	}
	if len(s.removed) > 0 {
		names := make([]string, 0, len(s.removed))
		for name := range s.removed {
			names = append(names, name)
		}
		sort.Strings(names)
		out = append(out, fmt.Sprintf(i18n.M.SetupSummaryRemovedFmt, strings.Join(names, ", ")))
	}
	if s.cfg.DefaultModel != s.originalDefault {
		out = append(out, fmt.Sprintf(i18n.M.SetupSummaryDefaultFmt, s.cfg.DefaultModel))
	}
	if len(s.pendingCredentials) > 0 {
		out = append(out, fmt.Sprintf(i18n.M.SetupSummaryKeysFmt, len(s.pendingCredentials)))
	}
	if len(out) == 0 {
		out = append(out, i18n.M.SetupSummaryNoChanges)
	}
	return out
}

func providerSetupEqual(a, b config.ProviderEntry) bool {
	// Render-level equality is unnecessary here: the manager only changes these
	// fields, while advanced provider fields are preserved by editing a copy.
	return a.Name == b.Name && a.Kind == b.Kind && a.BaseURL == b.BaseURL &&
		a.Model == b.Model && strings.Join(a.Models, "\x00") == strings.Join(b.Models, "\x00") &&
		a.Default == b.Default && a.APIKeyEnv == b.APIKeyEnv
}

func runProviderSetupManager(s *providerSetupSession, configPath, envPath string) int {
	cfg := s.cfg
	repaired, repairs := repairInvalidProviderKeyEnvs(cfg.Providers)
	for i := range repaired {
		if config.ProviderEntriesConfigEqual(cfg.Providers[i], repaired[i]) {
			continue
		}
		before := providerSetupEntryPtr(cfg.Providers[i])
		cfg.Providers[i] = repaired[i]
		s.recordProviderMutation(repaired[i].Name, before, providerSetupEntryPtr(repaired[i]))
	}
	for _, repair := range repairs {
		fmt.Fprintf(os.Stderr, "  %s\n", dim(fmt.Sprintf(i18n.M.RepairedAPIKeyEnvFmt, repair.provider, repair.old, repair.new)))
	}
	for {
		items := providerManagerItems(s)
		idx, err := selectOne(i18n.M.SetupManagerTitle, items)
		if err != nil {
			fmt.Fprintln(os.Stderr, "\n"+i18n.M.SetupCancelled)
			return 1
		}
		providerCount := len(cfg.Providers)
		switch idx {
		case providerCount:
			if !addProviderToSession(s, false) {
				continue
			}
		case providerCount + 1:
			if !addProviderToSession(s, true) {
				continue
			}
		case providerCount + 2:
			rc := saveProviderSetupSession(s, configPath, envPath)
			if rc == setupManagerContinue {
				continue
			}
			return rc
		case providerCount + 3:
			fmt.Println(i18n.M.SetupCancelled)
			return 1
		default:
			manageProvider(s, idx)
		}
	}
}

func providerManagerItems(s *providerSetupSession) []menuItem {
	cfg := s.cfg
	items := make([]menuItem, 0, len(cfg.Providers)+4)
	for _, p := range cfg.Providers {
		models := p.ModelList()
		keyStatus := i18n.M.SetupKeyMissing
		if p.APIKeyEnv == "" || config.CredentialIsSet(p.APIKeyEnv) || s.pendingCredentials[p.APIKeyEnv] != "" {
			keyStatus = i18n.M.SetupKeySet
		}
		desc := fmt.Sprintf("%s · %d %s · %s", p.Kind, len(models), i18n.M.SetupModelsUnit, keyStatus)
		if cfg.DefaultModel == p.Name || config.ModelRefsProvider(cfg.DefaultModel, p.Name) {
			desc += " · " + i18n.M.SetupDefaultBadge
		}
		items = append(items, menuItem{name: p.Name, desc: desc})
	}
	return append(items,
		menuItem{name: i18n.M.SetupAddOpenAI, desc: i18n.M.CustomProviderDesc},
		menuItem{name: i18n.M.SetupAddAnthropic, desc: i18n.M.AnthropicProviderDesc},
		menuItem{name: i18n.M.SetupSaveExit, desc: i18n.M.SetupSaveExitDesc},
		menuItem{name: i18n.M.SetupCancel, desc: i18n.M.SetupCancelDesc},
	)
}

func addProviderToSession(s *providerSetupSession, anthropic bool) bool {
	var result providerPromptResult
	var err error
	var proxy netclient.ProxySpec
	if s != nil && s.cfg != nil {
		proxy = s.cfg.NetworkProxySpec()
	}
	if anthropic {
		result, err = promptAnthropicProvider(proxy)
	} else {
		result, err = promptCustomProvider(proxy)
	}
	if err != nil {
		if !errors.Is(err, errCancelled) {
			fmt.Fprintln(os.Stderr, err)
		}
		return false
	}
	for _, entry := range result.entries {
		if !confirmSharedCredential(s.cfg, entry, "") {
			return false
		}
	}
	if err := s.add(result.entries); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return false
	}
	s.addProviderAccess(result.entries)
	for key, value := range result.credentials {
		if err := s.setCredential(key, value); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return false
		}
	}
	// After the new keys are staged, so usability sees them.
	s.promoteDefaultToNewProviders(result.entries)
	return true
}

func manageProvider(s *providerSetupSession, providerIndex int) {
	if providerIndex < 0 || providerIndex >= len(s.cfg.Providers) {
		return
	}
	p := s.cfg.Providers[providerIndex]
	idx, err := selectOne(fmt.Sprintf(i18n.M.SetupProviderActionsFmt, p.Name), []menuItem{
		{name: i18n.M.SetupEditProvider},
		{name: i18n.M.SetupUpdateKey},
		{name: i18n.M.SetupTestRefresh},
		{name: i18n.M.SetupSetDefault},
		{name: i18n.M.SetupRemoveProvider},
		{name: i18n.M.SetupBack},
	})
	if err != nil || idx == 5 {
		return
	}
	switch idx {
	case 0:
		editProvider(s, p)
	case 1:
		updateProviderKey(s, p)
	case 2:
		testAndRefreshProvider(s, p)
	case 3:
		setDefaultProvider(s, p)
	case 4:
		removeProviderFromSession(s, p)
	}
}

func editProvider(s *providerSetupSession, current config.ProviderEntry) {
	in := bufio.NewScanner(os.Stdin)
	edited := current
	edited.BaseURL = ask(in, os.Stdout, i18n.M.CustomPromptBaseURL, current.BaseURL)
	models := ask(in, os.Stdout, i18n.M.SetupPromptModels, strings.Join(current.ModelList(), ","))
	edited.Models = splitModels(models)
	if len(edited.Models) == 1 {
		edited.Model = edited.Models[0]
	} else {
		edited.Model = ""
	}
	if len(edited.Models) > 0 && !containsString(edited.Models, edited.Default) {
		edited.Default = edited.Models[0]
	}
	edited.APIKeyEnv = promptOptionalAPIKeyEnvName(in, os.Stdout, i18n.M.CustomPromptKeyEnv, current.APIKeyEnv)
	if !confirmSharedCredential(s.cfg, edited, current.Name) {
		return
	}
	if err := s.upsert([]config.ProviderEntry{edited}); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func promptOptionalAPIKeyEnvName(in *bufio.Scanner, w io.Writer, label, def string) string {
	for {
		key := ask(in, w, label, def)
		if key == "" || config.IsValidCredentialKey(key) {
			return key
		}
		fmt.Fprintf(w, i18n.M.InvalidAPIKeyEnvFmt+"\n", key)
	}
}

func splitModels(raw string) []string {
	seen := map[string]bool{}
	var models []string
	for model := range strings.SplitSeq(raw, ",") {
		model = strings.TrimSpace(model)
		if model != "" && !seen[model] {
			seen[model] = true
			models = append(models, model)
		}
	}
	return models
}

func confirmSharedCredential(cfg *config.Config, candidate config.ProviderEntry, ignoreName string) bool {
	if candidate.APIKeyEnv == "" {
		return true
	}
	for _, p := range cfg.Providers {
		if p.Name == ignoreName || p.Name == candidate.Name || p.APIKeyEnv != candidate.APIKeyEnv || p.BaseURL == candidate.BaseURL {
			continue
		}
		in := bufio.NewScanner(os.Stdin)
		answer := ask(in, os.Stdout, fmt.Sprintf(i18n.M.SetupSharedKeyWarningFmt, candidate.APIKeyEnv, p.Name, p.BaseURL), "y/N")
		return answer == "y" || answer == "Y"
	}
	return true
}

func updateProviderKey(s *providerSetupSession, p config.ProviderEntry) {
	in := bufio.NewScanner(os.Stdin)
	keyEnvChanged := false
	if p.APIKeyEnv == "" {
		p.APIKeyEnv = promptAPIKeyEnvName(in, os.Stdout, i18n.M.CustomPromptKeyEnv, apiKeyEnvFromProviderName(p.Name))
		keyEnvChanged = true
	}
	if !confirmSharedCredential(s.cfg, p, p.Name) {
		return
	}
	if keyEnvChanged {
		if err := s.upsert([]config.ProviderEntry{p}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
	}
	value := askSecret(in, os.Stdout, fmt.Sprintf(i18n.M.SetupPromptAPIKeyFmt, p.APIKeyEnv))
	if value == "" {
		return
	}
	if err := s.setCredential(p.APIKeyEnv, value); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func testAndRefreshProvider(s *providerSetupSession, p config.ProviderEntry) {
	restore := temporarilySetCredential(p.APIKeyEnv, s.pendingCredentials[p.APIKeyEnv])
	defer restore()
	p.ResolveAPIKeyFromProcessEnvForProbe()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var proxy netclient.ProxySpec
	if s.cfg != nil {
		proxy = s.cfg.NetworkProxySpec()
	}
	models, err := p.FetchModelsWithProxy(ctx, proxy)
	if err != nil {
		fmt.Fprintf(os.Stderr, i18n.M.FetchModelsFailedFmt+"\n", p.Name, err)
		return
	}
	if len(models) == 0 {
		fmt.Fprintln(os.Stderr, i18n.M.CustomFetchEmpty)
		return
	}
	items := make([]menuItem, len(models))
	for i, model := range models {
		items[i] = menuItem{name: model}
	}
	idxs, err := selectMany(fmt.Sprintf(i18n.M.SelectModelsLabel, p.Name), items)
	if err != nil || len(idxs) == 0 {
		return
	}
	selected := make([]string, 0, len(idxs))
	for _, idx := range idxs {
		selected = append(selected, models[idx])
	}
	p.Models = selected
	p.Model = ""
	if !containsString(selected, p.Default) {
		p.Default = selected[0]
	}
	if err := s.upsert([]config.ProviderEntry{p}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	fmt.Printf("  %s\n", green(fmt.Sprintf(i18n.M.FetchModelsSuccessFmt, len(models), p.Name)))
}

func temporarilySetCredential(key, value string) func() {
	if key == "" || value == "" {
		return func() {}
	}
	old, existed := os.LookupEnv(key)
	_ = os.Setenv(key, value)
	return func() {
		if existed {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	}
}

func setDefaultProvider(s *providerSetupSession, p config.ProviderEntry) {
	models := p.ModelList()
	if len(models) == 0 {
		return
	}
	items := make([]menuItem, len(models))
	for i, model := range models {
		items[i] = menuItem{name: model}
	}
	idx, err := selectOne(i18n.M.SetupSelectDefaultModel, items)
	if err != nil {
		return
	}
	if err := s.setDefaultModel(p.Name + "/" + models[idx]); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func removeProviderFromSession(s *providerSetupSession, p config.ProviderEntry) {
	in := bufio.NewScanner(os.Stdin)
	answer := ask(in, os.Stdout, fmt.Sprintf(i18n.M.SetupConfirmRemoveFmt, p.Name), "y/N")
	if answer != "y" && answer != "Y" {
		return
	}
	if err := s.remove(p.Name); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func (s *providerSetupSession) replayOperations(cfg *config.Config, accessDeclared *bool, declaredProviders []string) error {
	for _, operation := range s.operations {
		switch operation.kind {
		case setupOpProvider:
			current, exists := cfg.Provider(operation.providerName)
			if operation.beforeProvider == nil {
				if exists {
					return &providerSetupConflictError{field: fmt.Sprintf("provider %q", operation.providerName)}
				}
			} else if !exists || !config.ProviderEntriesConfigEqual(*current, *operation.beforeProvider) {
				return &providerSetupConflictError{field: fmt.Sprintf("provider %q", operation.providerName)}
			}
			if operation.afterProvider == nil {
				if err := cfg.RemoveProvider(operation.providerName); err != nil {
					return fmt.Errorf("replay remove provider %q: %w", operation.providerName, err)
				}
			} else if err := cfg.UpsertProviderPreservingRuntime(*operation.afterProvider); err != nil {
				return fmt.Errorf("replay provider %q: %w", operation.providerName, err)
			}
		case setupOpDefaultModel:
			if cfg.DefaultModel != operation.beforeString {
				return &providerSetupConflictError{field: "default_model"}
			}
			if err := cfg.SetDefaultModel(operation.afterString); err != nil {
				return fmt.Errorf("replay default_model: %w", err)
			}
		case setupOpLanguage:
			if cfg.Language != operation.beforeString {
				return &providerSetupConflictError{field: "language"}
			}
			cfg.Language = operation.afterString
		case setupOpMaterializeAccess:
			if *accessDeclared {
				return &providerSetupConflictError{field: "desktop.provider_access"}
			}
			cfg.Desktop.ProviderAccess = nil
			if operation.projectScoped {
				for _, name := range declaredProviders {
					provider, ok := cfg.Provider(name)
					if ok && provider.Configured() && len(provider.ModelList()) > 0 {
						cfg.Desktop.ProviderAccess = append(cfg.Desktop.ProviderAccess, name)
					}
				}
			} else {
				config.NormalizeLegacyDesktopProviderAccess(cfg)
			}
			*accessDeclared = true
			if cfg.Desktop.ProviderAccess == nil {
				cfg.Desktop.ProviderAccess = []string{}
			}
		case setupOpAccessMembership:
			current := providerSetupAccessContains(cfg.Desktop.ProviderAccess, operation.accessName)
			if current != operation.beforeBool {
				return &providerSetupConflictError{field: fmt.Sprintf("desktop.provider_access[%q]", operation.accessName)}
			}
			if operation.afterBool {
				cfg.Desktop.ProviderAccess = append(cfg.Desktop.ProviderAccess, operation.accessName)
			} else {
				out := cfg.Desktop.ProviderAccess[:0]
				for _, name := range cfg.Desktop.ProviderAccess {
					if strings.TrimSpace(name) != operation.accessName {
						out = append(out, name)
					}
				}
				cfg.Desktop.ProviderAccess = out
			}
		default:
			return fmt.Errorf("unknown provider setup operation %d", operation.kind)
		}
	}
	return nil
}

func providerSetupAccessContains(names []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, name := range names {
		if strings.TrimSpace(name) == want {
			return true
		}
	}
	return false
}

func commitProviderSetupSession(s *providerSetupSession, configPath string) (bool, error) {
	if len(s.operations) == 0 {
		return false, nil
	}
	unlock, err := config.LockConfigFileEdits(configPath)
	if err != nil {
		return false, err
	}
	defer unlock()

	before, err := readProviderSetupFileSnapshot(configPath)
	if err != nil {
		return false, err
	}
	declarations, err := config.InspectConfigFileDeclarations(configPath)
	if err != nil {
		return false, err
	}
	fresh, err := config.LoadForEditReadOnlyStrict(configPath)
	if err != nil {
		return false, err
	}
	accessDeclared := declarations.DesktopProviderAccessDeclared
	if err := s.replayOperations(fresh, &accessDeclared, declarations.ProviderNames); err != nil {
		return false, err
	}
	current, err := readProviderSetupFileSnapshot(configPath)
	if err != nil {
		return false, err
	}
	if !providerSetupFileSnapshotEqual(before, current) {
		return false, &providerSetupConflictError{field: "configuration file"}
	}
	if err := fresh.SaveTo(configPath); err != nil {
		return false, err
	}
	return true, nil
}

func saveProviderSetupSession(s *providerSetupSession, configPath, envPath string) int {
	fmt.Println()
	fmt.Println(i18n.M.SetupSummaryTitle)
	for _, line := range s.summary() {
		fmt.Println("  " + line)
	}
	in := bufio.NewScanner(os.Stdin)
	answer := ask(in, os.Stdout, i18n.M.SetupConfirmSave, "Y/n")
	if answer == "n" || answer == "N" {
		return setupManagerContinue
	}
	configWritten, err := commitProviderSetupSession(s, configPath)
	if err != nil {
		var conflict *providerSetupConflictError
		if errors.As(err, &conflict) {
			fmt.Fprintf(os.Stderr, i18n.M.SetupConcurrentChangeFmt+"\n", conflict.field)
		} else {
			fmt.Fprintln(os.Stderr, i18n.M.WriteConfigErr, err)
		}
		return 1
	}
	if configWritten {
		fmt.Printf("\n%s %s\n", green("✓"), fmt.Sprintf(i18n.M.WroteFileFmt, displayPath(configPath)))
	}
	if lines := s.credentialLines(); len(lines) > 0 {
		target, err := config.StoreCredentialLines(lines)
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.WriteEnvErr, err)
			return 1
		}
		if target == "" {
			target = envPath
		}
		fmt.Printf("%s %s\n", green("✓"), fmt.Sprintf(i18n.M.WroteFileFmt, displayPath(target)))
	}
	fmt.Printf("\n%s %s\n", accent("◆"), i18n.M.SetupComplete)
	return 0
}
