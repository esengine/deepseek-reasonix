package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	fileencoding "reasonix/internal/fileutil/encoding"
)

// legacyConfig is the subset of the v0.x (~/.reasonix/config.json) schema this
// import carries forward. Fields absent here are dropped on purpose: desktop tab
// state is frontend-owned, and skills already live in the shared ~/.reasonix/skills
// root that v1+ also scans, so they need no migration.
type legacyConfig struct {
	APIKey      string                       `json:"apiKey"`
	BaseURL     string                       `json:"baseUrl"`
	Model       string                       `json:"model"`
	Lang        string                       `json:"lang"`
	MCP         []string                     `json:"mcp"` // pre-mcpServers `--mcp`-format strings
	MCPServers  map[string]legacyMCPServer   `json:"mcpServers"`
	MCPEnv      map[string]map[string]string `json:"mcpEnv"`
	MCPDisabled []string                     `json:"mcpDisabled"`
	QQ          legacyQQConfig               `json:"qq"`
}

type legacyMCPServer struct {
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
	Transport string            `json:"transport"`
	Type      string            `json:"type"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Disabled  bool              `json:"disabled"`
}

type legacyQQConfig struct {
	AppID       string   `json:"appId"`
	AppSecret   string   `json:"appSecret"`
	Sandbox     bool     `json:"sandbox"`
	Enabled     bool     `json:"enabled"`
	OwnerOpenID string   `json:"ownerOpenId"`
	Allowlist   []string `json:"allowlist"`
}

// MigrationResult summarizes a one-time legacy import for the boot-time notice.
type MigrationResult struct {
	From     string
	To       string
	KeyToEnv bool
	Plugins  int
	Warnings []string
}

// MCPGlobalMigrationResult summarizes the v1.9.1 MCP backfill that lifts MCP
// servers from legacy and project-local sources into the user-global config.
type MCPGlobalMigrationResult struct {
	To      string
	Added   int
	Sources int
}

func (r *MigrationResult) Notice() string {
	var b strings.Builder
	fmt.Fprintf(&b, "migrated your previous configuration: %s → %s", r.From, r.To)
	if r.Plugins > 0 {
		fmt.Fprintf(&b, " (%d MCP server(s))", r.Plugins)
	}
	if r.KeyToEnv {
		b.WriteString("; API key saved to reasonix's credentials store")
	}
	b.WriteString(". The old files were left untouched.")
	for _, w := range r.Warnings {
		b.WriteString("\n  note: " + w)
	}
	return b.String()
}

// MigrateLegacyIfNeeded performs a one-time, non-destructive import of older
// installs into the current user config when the latter does not exist yet. It
// checks v1-era TOML first, then v0.5/v0.x ~/.reasonix/config.json, and never
// modifies or deletes the legacy files. Returns nil when there is nothing to
// migrate, or when the current user config already exists.
func MigrateLegacyIfNeeded() (*MigrationResult, error) {
	return MigrateLegacyIfNeededForRoot(".")
}

func MigrateLegacyIfNeededForRoot(root string) (*MigrationResult, error) {
	if IsolatedHomeDir() != "" {
		return nil, nil
	}
	credErr := migrateLegacyCredentialsIfNeededForRoot(root)
	dest := userConfigPath()
	if dest == "" {
		return nil, credErr
	}
	unlock, err := LockConfigFileEdits(dest)
	if err != nil {
		return nil, errors.Join(credErr, err)
	}
	defer unlock()
	if _, err := os.Stat(dest); err == nil {
		return nil, credErr
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, credErr
	}
	if res, err := migrateLegacyTOMLIfNeeded(dest, home); res != nil || err != nil {
		if err == nil {
			err = credErr
		}
		return res, err
	}
	src := filepath.Join(home, ".reasonix", "config.json")
	data, err := fileencoding.ReadFileUTF8(src)
	if err != nil {
		return nil, nil
	}
	var legacy legacyConfig
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}) // tolerate a UTF-8 BOM (some editors add one)
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("parse legacy config %s: %w", src, err)
	}

	cfg := Default()
	res := &MigrationResult{From: src, To: dest}
	if legacy.Lang != "" {
		cfg.Language = legacy.Lang
		_ = cfg.SetDesktopLanguage(legacy.Lang)
	}
	if legacy.Model != "" {
		if entry, ok := cfg.ResolveModel(legacy.Model); ok {
			cfg.DefaultModel = entry.Name + "/" + entry.Model
		} else {
			cfg.DefaultModel = legacy.Model
		}
	}
	migrateLegacyBaseURL(cfg, legacy.BaseURL)
	cfg.Plugins = legacyPlugins(legacy)
	res.Plugins = len(cfg.Plugins)

	var envLines []string
	if key := strings.TrimSpace(legacy.APIKey); key != "" {
		envLines = append(envLines, "DEEPSEEK_API_KEY="+key)
		res.KeyToEnv = true
		if base := strings.TrimSpace(legacy.BaseURL); base != "" && !strings.Contains(base, "deepseek.com") {
			res.Warnings = append(res.Warnings, "your previous base_url was "+base+
				" — it was applied to the built-in DeepSeek providers; verify models if this endpoint is not DeepSeek-compatible")
		}
	}
	if qqSecret := strings.TrimSpace(legacy.QQ.AppSecret); qqSecret != "" {
		envLines = append(envLines, "QQ_BOT_APP_SECRET="+qqSecret)
		res.Warnings = append(res.Warnings, "your previous QQ Bot App Secret was saved to reasonix's credentials store")
	}
	migrateLegacyQQConfig(cfg, legacy.QQ)

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}
	if err := cfg.WriteFile(dest); err != nil {
		return nil, fmt.Errorf("write %s: %w", dest, err)
	}
	if len(envLines) > 0 {
		if err := writeCredentialsEnv(home, envLines); err != nil {
			return res, fmt.Errorf("write credentials: %w", err)
		}
	}
	return res, credErr
}

func MigrateLegacyCredentialsForRoot(root string) error {
	if IsolatedHomeDir() != "" {
		return nil
	}
	return migrateLegacyCredentialsIfNeededForRoot(root)
}

// MigrateMCPToUserConfigOnUpgrade runs a one-time best-effort backfill for the
// v1.9.1 desktop/CLI upgrade: MCP servers found in legacy TOML, known project
// roots, and legacy v0.x JSON are copied into the user-global config so the MCP
// settings page is stable across Global/project tabs. Existing global entries win
// on name collisions, and source files are left untouched.
func MigrateMCPToUserConfigOnUpgrade(projectRoots []string) (*MCPGlobalMigrationResult, error) {
	dest := userConfigPath()
	if dest == "" {
		return nil, nil
	}
	unlock, err := LockConfigFileEdits(dest)
	if err != nil {
		return nil, err
	}
	defer unlock()

	marker := mcpGlobalMigrationMarkerPath()
	if marker == "" {
		return nil, nil
	}
	if _, err := os.Stat(marker); err == nil {
		return nil, nil
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	res, err := migrateMCPToUserConfig(projectRoots)
	if err != nil {
		return res, err
	}
	if res == nil {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		return res, err
	}
	if err := os.WriteFile(marker, []byte("v1\n"), 0o644); err != nil {
		return res, err
	}
	return res, nil
}

func migrateMCPToUserConfig(projectRoots []string) (*MCPGlobalMigrationResult, error) {
	dest := userConfigPath()
	if dest == "" {
		return nil, nil
	}
	userCfg, err := loadForEditStrict(dest, true, true)
	if err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(userCfg.Plugins))
	for _, p := range userCfg.Plugins {
		if name := strings.TrimSpace(p.Name); name != "" {
			have[name] = true
		}
	}

	result := &MCPGlobalMigrationResult{To: dest}
	addEntries := func(entries []PluginEntry) {
		if len(entries) == 0 {
			return
		}
		result.Sources++
		for _, entry := range entries {
			entry, _ = NormalizePluginCommandLine(entry)
			name := strings.TrimSpace(entry.Name)
			if name == "" || have[name] || validatePlugin(entry) != nil {
				continue
			}
			entry.Source = MCPSourceUserConfig
			userCfg.Plugins = append(userCfg.Plugins, entry)
			have[name] = true
			result.Added++
		}
	}

	home, _ := os.UserHomeDir()
	for _, path := range mcpMigrationLegacyTOMLPaths(dest, home) {
		addEntries(loadPluginEntriesFromTOML(path))
	}
	for _, root := range normalizedMCPMigrationRoots(projectRoots) {
		addEntries(append(loadPluginEntriesFromTOML(filepath.Join(root, projectConfigFile)), loadPluginEntriesFromTOML(filepath.Join(root, projectConfigLocal))...))
		if entries, err := loadMCPJSON(filepath.Join(root, mcpJSONFile)); err == nil {
			addEntries(entries)
		}
	}
	addEntries(loadLegacyConfigPlugins(legacyConfigPath()))

	if result.Sources == 0 {
		return nil, nil
	}
	if result.Added > 0 {
		if err := userCfg.SaveTo(dest); err != nil {
			return result, err
		}
	}
	return result, nil
}

func mcpGlobalMigrationMarkerPath() string {
	dir := userSupportDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "mcp-global-migration-v1")
}

func mcpGlobalMigrationComplete() bool {
	marker := mcpGlobalMigrationMarkerPath()
	if marker == "" {
		return false
	}
	_, err := os.Stat(marker)
	return err == nil
}

func mcpMigrationLegacyTOMLPaths(dest, home string) []string {
	var paths []string
	for _, path := range legacyTOMLPaths(dest, home) {
		if path == "" || samePath(path, dest) {
			continue
		}
		paths = append(paths, path)
	}
	return paths
}

func loadPluginEntriesFromTOML(path string) []PluginEntry {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	var cfg Config
	if _, err := decodeTOMLFile(path, &cfg); err != nil {
		return nil
	}
	out := make([]PluginEntry, 0, len(cfg.Plugins))
	for _, p := range cfg.Plugins {
		p, _ = NormalizePluginCommandLine(p)
		out = append(out, p)
	}
	return out
}

func loadLegacyConfigPlugins(path string) []PluginEntry {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	data, err := fileencoding.ReadFileUTF8(path)
	if err != nil {
		return nil
	}
	var legacy legacyConfig
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil
	}
	return legacyPlugins(legacy)
}

func normalizedMCPMigrationRoots(roots []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
		root = filepath.Clean(root)
		if seen[root] {
			continue
		}
		seen[root] = true
		out = append(out, root)
	}
	return out
}

func migrateLegacyCredentialsIfNeededForRoot(root string) error {
	missing := map[string]string{}
	// File import ignores keyring markers: a marker only means "do not re-probe
	// keyring for this env name", never "skip legacy credential files".
	skipStore := func(key string) bool {
		return credentialCurrentStoreHasKey(key) || credentialCurrentStoreClearedKey(key)
	}
	// Prefer legacy credential files first so a healthy file import does not
	// depend on Secret Service / D-Bus (#7507).
	for _, src := range legacyCredentialsPaths() {
		if src == "" {
			continue
		}
		data, err := fileencoding.ReadFileUTF8(src)
		if err != nil {
			continue
		}
		assignments := parseCredentialLines(strings.Split(string(data), "\n"))
		for key, value := range assignments {
			if _, exists := missing[key]; !exists && !skipStore(key) {
				missing[key] = value
			}
		}
	}
	keys := credentialEnvNamesForRoot(root)
	needKeyring := make([]string, 0, len(keys))
	for _, key := range keys {
		if skipStore(key) {
			continue
		}
		if _, exists := missing[key]; exists {
			continue
		}
		// Marker only filters keyring probes.
		if legacyKeyringMigrationDone(key) {
			continue
		}
		needKeyring = append(needKeyring, key)
	}
	if len(needKeyring) > 0 {
		outcomes := lookupLegacyKeyringBatch(needKeyring, legacyKeyringLookupTimeout)
		for _, key := range needKeyring {
			o := outcomes[key]
			switch o.Status {
			case legacyKeyringFound:
				// Secret was stored by the probe path (helper or in-process).
				// Do not trust Value from the parent-visible outcome map.
			case legacyKeyringAbsent:
				// Confirmed empty probe only — never on error/timeout.
				_ = markLegacyKeyringMigrationDone(key)
			case legacyKeyringError, legacyKeyringTimeout:
				// Leave unmarked so the next launch retries.
			default:
				// Unknown status: treat as timeout (no marker).
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	_, err := StoreCredentialLines(credentialLines(missing))
	return err
}

func legacyKeyringMigrationMarkerPath(key string) string {
	home := ReasonixHomeDir()
	key = strings.TrimSpace(key)
	if strings.TrimSpace(home) == "" || key == "" {
		return ""
	}
	// Env var names are identifiers, not secrets. RawURL base64 is collision-free
	// and filesystem-safe without hashing secret material.
	name := base64.RawURLEncoding.EncodeToString([]byte(key))
	return filepath.Join(home, "state", "legacy-keyring-checked", name)
}

func legacyKeyringMigrationDone(key string) bool {
	path := legacyKeyringMigrationMarkerPath(key)
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func markLegacyKeyringMigrationDone(key string) error {
	path := legacyKeyringMigrationMarkerPath(key)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("v1\n"), 0o644)
}

func credentialLines(assignments map[string]string) []string {
	keys := make([]string, 0, len(assignments))
	for key := range assignments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+assignments[key])
	}
	return lines
}

func migrateLegacyQQConfig(cfg *Config, legacy legacyQQConfig) {
	if cfg == nil || !legacyQQConfigured(legacy) {
		return
	}
	cfg.Bot.Enabled = cfg.Bot.Enabled || legacy.Enabled
	cfg.Bot.QQ.Enabled = legacy.Enabled
	cfg.Bot.QQ.AppID = strings.TrimSpace(legacy.AppID)
	cfg.Bot.QQ.AppSecretEnv = "QQ_BOT_APP_SECRET"
	cfg.Bot.QQ.Sandbox = legacy.Sandbox
	cfg.Bot.Allowlist.Enabled = true
	cfg.Bot.Allowlist.QQUsers = mergeUniqueTrimmed(cfg.Bot.Allowlist.QQUsers, legacy.OwnerOpenID)
	cfg.Bot.Allowlist.QQUsers = mergeUniqueTrimmed(cfg.Bot.Allowlist.QQUsers, legacy.Allowlist...)
}

func legacyQQConfigured(legacy legacyQQConfig) bool {
	return legacy.Enabled ||
		strings.TrimSpace(legacy.AppID) != "" ||
		strings.TrimSpace(legacy.AppSecret) != "" ||
		strings.TrimSpace(legacy.OwnerOpenID) != "" ||
		len(legacy.Allowlist) > 0 ||
		legacy.Sandbox
}

func mergeUniqueTrimmed(base []string, values ...string) []string {
	seen := make(map[string]bool, len(base)+len(values))
	out := make([]string, 0, len(base)+len(values))
	for _, value := range append(base, values...) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func migrateLegacyTOMLIfNeeded(dest, home string) (*MigrationResult, error) {
	for _, src := range legacyTOMLPaths(dest, home) {
		if src == "" || filepath.Clean(src) == filepath.Clean(dest) {
			continue
		}
		if _, err := os.Stat(src); err != nil {
			continue
		}
		cfg := Default()
		if err := mergeFile(cfg, src); err != nil {
			return nil, fmt.Errorf("parse legacy config %s: %w", src, err)
		}
		cfg.ConfigVersion = Default().ConfigVersion
		if strings.TrimSpace(cfg.Desktop.CloseBehavior) == "" && strings.TrimSpace(cfg.UI.CloseBehavior) != "" {
			cfg.Desktop.CloseBehavior = cfg.DesktopCloseBehavior()
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return nil, fmt.Errorf("create config dir: %w", err)
		}
		if err := cfg.WriteFile(dest); err != nil {
			return nil, fmt.Errorf("write %s: %w", dest, err)
		}
		res := &MigrationResult{From: src, To: dest, Plugins: len(cfg.Plugins)}
		legacyDir := filepath.Dir(src)
		newDir := filepath.Dir(dest)
		if !samePath(legacyDir, newDir) {
			if warnings := migrateSupportData(legacyDir, newDir); len(warnings) > 0 {
				res.Warnings = append(res.Warnings, warnings...)
			}
		}
		return res, nil
	}
	return nil, nil
}

func legacyTOMLPaths(dest, home string) []string {
	seen := map[string]bool{}
	var paths []string
	add := func(path string) {
		if path == "" {
			return
		}
		path = filepath.Clean(path)
		if seen[path] {
			return
		}
		seen[path] = true
		paths = append(paths, path)
	}
	if legacy := legacyUserConfigPath(); legacy != "" {
		add(legacy)
	}
	for _, legacy := range legacyXDGConfigPaths() {
		add(legacy)
		add(filepath.Join(filepath.Dir(legacy), "reasonix.toml"))
	}
	add(filepath.Join(filepath.Dir(dest), "reasonix.toml"))
	if home != "" {
		add(filepath.Join(home, ".reasonix", "reasonix.toml"))
	}
	return paths
}

func migrateLegacyBaseURL(cfg *Config, baseURL string) {
	baseURL = strings.TrimSpace(baseURL)
	if cfg == nil || baseURL == "" {
		return
	}
	officialDeepSeek := isOfficialDeepSeekOpenAIEndpoint(baseURL)
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.APIKeyEnv != "DEEPSEEK_API_KEY" {
			continue
		}
		if officialDeepSeek {
			// v0.x stored the official OpenAI-compatible root (or /v1). The
			// current built-in provider is Anthropic, whose documented endpoint
			// has a distinct /anthropic prefix.
			p.Kind = "anthropic"
			p.BaseURL = deepSeekAnthropicBaseURL
			continue
		}
		// A non-official v0.x base URL was an OpenAI-compatible endpoint. Keep
		// that wire protocol instead of applying the new Anthropic defaults to a
		// custom gateway that may not implement Messages API.
		p.Kind = "openai"
		p.BaseURL = baseURL
		p.Thinking = ""
		p.WebSearch = nil
		p.SupportedEfforts = nil
		p.DefaultEffort = ""
		p.ModelOverrides = nil
	}
}

func legacyPlugins(legacy legacyConfig) []PluginEntry {
	disabled := make(map[string]bool, len(legacy.MCPDisabled))
	for _, n := range legacy.MCPDisabled {
		disabled[n] = true
	}
	var out []PluginEntry
	index := make(map[string]int)
	add := func(pe PluginEntry, off bool) {
		if off {
			v := false
			pe.AutoStart = &v
		}
		pe, _ = NormalizePluginCommandLine(pe)
		if j, dup := index[pe.Name]; dup {
			out[j] = pe // mcpServers overrides the `mcp` list on a name collision, matching v0.x
			return
		}
		index[pe.Name] = len(out)
		out = append(out, pe)
	}
	for i, raw := range legacy.MCP {
		pe, ok := parseLegacyMCPSpec(raw)
		if !ok {
			continue
		}
		if pe.Name == "" {
			pe.Name = anonymousMCPName(i)
		} else if pe.Command != "" {
			pe.Env = mergeEnv(nil, legacy.MCPEnv[pe.Name])
		}
		add(pe, disabled[pe.Name])
	}
	names := make([]string, 0, len(legacy.MCPServers))
	for n := range legacy.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		s := legacy.MCPServers[name]
		pe := PluginEntry{
			Name:    name,
			Type:    normalizeTransport(firstNonEmpty(s.Type, s.Transport)),
			Command: s.Command,
			Args:    s.Args,
			Env:     mergeEnv(s.Env, legacy.MCPEnv[name]),
			URL:     s.URL,
			Headers: s.Headers,
		}
		add(pe, s.Disabled || disabled[name])
	}
	return out
}

// normalizeTransport maps the v0.x transport names to v1+ plugin types. stdio is
// the default, so it returns "" (RenderTOML then omits the field).
func normalizeTransport(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "http", "streamable-http":
		return "http"
	case "sse":
		return "sse"
	default:
		return ""
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// mergeEnv overlays the per-server env map onto the spec's own env (overlay wins,
// matching v0.x mcpEnv precedence). Returns nil when both are empty.
func mergeEnv(base, overlay map[string]string) map[string]string {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(overlay))
	maps.Copy(out, base)
	maps.Copy(out, overlay)
	return out
}

// writeCredentialsEnv merges lines into Reasonix's global .env
// and pins them into the current process env so the just-built session resolves
// the key without a restart. Falls back to ~/.env only when Reasonix home can't
// be resolved — never a project .env, so a migration keeps secrets out of the
// user's project tree.
func writeCredentialsEnv(home string, lines []string) error {
	if _, err := StoreCredentialLines(lines); err != nil {
		if UserCredentialsPath() == "" && home != "" {
			return os.WriteFile(filepath.Join(home, ".env"), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
		}
		return err
	}
	return nil
}

func migrateSupportData(legacyDir, newDir string) []string {
	var warnings []string
	// settings.json carries the global hooks; leaving it out silently emptied
	// them for anyone whose home moved (#4652).
	items := []string{"sessions", "projects", "skills", "archive", "hooks.json", "settings.json"}
	for _, item := range items {
		src := filepath.Join(legacyDir, item)
		fi, err := os.Stat(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			warnings = append(warnings, fmt.Sprintf("failed to read legacy item %s: %v", item, err))
			continue
		}
		dst := filepath.Join(newDir, item)
		if fi.IsDir() {
			if err := copyDir(src, dst); err != nil {
				warnings = append(warnings, fmt.Sprintf("failed to migrate directory %s: %v", item, err))
			} else {
				warnings = append(warnings, fmt.Sprintf("successfully migrated directory %s", item))
			}
		} else {
			if _, err := os.Stat(dst); err == nil {
				// A file already written at the destination is newer than the
				// legacy copy; never overwrite user state during migration.
				warnings = append(warnings, fmt.Sprintf("kept existing file %s", item))
				continue
			}
			if err := copyFile(src, dst); err != nil {
				warnings = append(warnings, fmt.Sprintf("failed to migrate file %s: %v", item, err))
			} else {
				warnings = append(warnings, fmt.Sprintf("successfully migrated file %s", item))
			}
		}
	}
	return warnings
}

func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	parentMode := os.FileMode(0o755)
	if info.Mode().Perm()&0o077 == 0 {
		parentMode = 0o700
	}
	if err := os.MkdirAll(filepath.Dir(dst), parentMode); err != nil {
		return err
	}

	perm := info.Mode().Perm()
	if perm == 0 {
		perm = 0o600
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	return os.Chmod(dst, perm)
}

func copyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	perm := info.Mode().Perm()
	if perm == 0 {
		perm = 0o700
	}
	if err := os.MkdirAll(dst, perm); err != nil {
		return err
	}
	if err := os.Chmod(dst, perm); err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}
