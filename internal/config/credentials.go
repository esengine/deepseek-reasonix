package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"

	"reasonix/internal/fileutil"
	fileencoding "reasonix/internal/fileutil/encoding"
)

const (
	CredentialsStoreAuto    = "auto"
	CredentialsStoreKeyring = "keyring"
	CredentialsStoreFile    = "file"

	credentialsKeyringService = "reasonix"
	credentialClearedPrefix   = "# reasonix-cleared "
)

const (
	CredentialSourceEnvironment = "environment"
	CredentialSourceProjectEnv  = "project_env"
	CredentialSourceCredentials = "credentials"
	CredentialSourceHomeEnv     = "home_env"
	CredentialSourceLegacy      = "legacy_credentials"
)

type CredentialSource struct {
	Kind  string `json:"kind"`
	Path  string `json:"path,omitempty"`
	Label string `json:"label,omitempty"`
}

type CredentialResolution struct {
	Name     string             `json:"name"`
	Set      bool               `json:"set"`
	Value    string             `json:"-"`
	Source   CredentialSource   `json:"source,omitempty"`
	Shadowed []CredentialSource `json:"shadowed,omitempty"`
}

type trackedCredentialSource struct {
	source CredentialSource
	value  string
}

var credentialSourceTracker = struct {
	sync.Mutex
	byKey map[string]trackedCredentialSource
}{byKey: map[string]trackedCredentialSource{}}

// userCredentialEditMu serializes Reasonix-owned credential-store writes.
// LockUserCredentialEdits also takes a path-derived advisory file lock so a
// Desktop window, CLI process, or background catalog save can share one
// compare-and-apply boundary with credential rotation.
var userCredentialEditMu sync.Mutex

var storedCredentialValueLookup = storedCredentialValue

// legacyKeyringProbeLookup is the test-facing single-key hook returning the full
// four-state outcome under a caller-owned context (shared 1s migration budget).
var legacyKeyringProbeLookup = legacyKeyringProbe

// legacyKeyringLookupTimeout is the shared budget for one legacy keyring scan.
var legacyKeyringLookupTimeout = time.Second

// CredentialResolver resolves credentials repeatedly for one caller-owned view
// build. It keeps expensive global credential-store lookups bounded to one per
// key while preserving the same source/shadow reporting as the one-shot helpers.
type CredentialResolver struct {
	root string

	mu               sync.Mutex
	globalFirstCache map[string]CredentialResolution
}

// NewCredentialResolverForRoot returns a resolver scoped to a workspace root.
func NewCredentialResolverForRoot(root string) *CredentialResolver {
	return &CredentialResolver{root: resolveRoot(root)}
}

// ResolveGlobalFirst resolves key from Reasonix's global .env only. Repeated
// calls for the same key reuse the first result so UI views with multiple
// provider entries sharing api_key_env stay consistent.
func (r *CredentialResolver) ResolveGlobalFirst(key string) CredentialResolution {
	key = strings.TrimSpace(key)
	if key == "" {
		return CredentialResolution{Name: key}
	}
	if r == nil {
		return resolveCredentialForRootGlobalFirst(".", key)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.globalFirstCache == nil {
		r.globalFirstCache = map[string]CredentialResolution{}
	}
	if cached, ok := r.globalFirstCache[key]; ok {
		return cloneCredentialResolution(cached)
	}
	res := resolveCredentialForRootGlobalFirst(r.root, key)
	r.globalFirstCache[key] = cloneCredentialResolution(res)
	return res
}

func cloneCredentialResolution(res CredentialResolution) CredentialResolution {
	if len(res.Shadowed) > 0 {
		res.Shadowed = append([]CredentialSource(nil), res.Shadowed...)
	}
	return res
}

func normalizeCredentialsStore(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case CredentialsStoreKeyring:
		return CredentialsStoreKeyring
	case CredentialsStoreFile:
		return CredentialsStoreFile
	default:
		return CredentialsStoreAuto
	}
}

func credentialsStoreMode() string {
	if mode := strings.TrimSpace(os.Getenv("REASONIX_CREDENTIALS_STORE")); mode != "" {
		return normalizeCredentialsStore(mode)
	}
	var partial struct {
		CredentialsStore string `toml:"credentials_store"`
	}
	if path := userConfigLoadPath(); path != "" {
		_, _ = decodeTOMLFile(path, &partial)
	}
	return normalizeCredentialsStore(partial.CredentialsStore)
}

func credentialEnvNamesForRoot(root string) []string {
	root = resolveRoot(root)
	cfg := Default()

	projectTOML := ProjectConfigPath(root)
	if uc := userConfigLoadPath(); uc != "" {
		_ = mergeFile(cfg, uc)
	}
	_ = mergeFile(cfg, projectTOML)
	var tomlSources []string
	if uc := userConfigLoadPath(); uc != "" {
		tomlSources = append(tomlSources, uc)
	}
	tomlSources = append(tomlSources, projectTOML)
	if providers, _, _, ok, err := mergeTOMLProviders(tomlSources); err == nil && ok {
		cfg.Providers = providers
	}

	return credentialEnvNamesFromConfig(cfg)
}

func credentialEnvNamesFromConfig(cfg *Config) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, p := range cfg.Providers {
		add(p.APIKeyEnv)
	}
	add(cfg.Bot.QQ.AppSecretEnv)
	add(cfg.Bot.Feishu.AppSecretEnv)
	add(cfg.Bot.Weixin.TokenEnv)
	for _, conn := range cfg.Bot.Connections {
		add(conn.Credential.AppSecretEnv)
		add(conn.Credential.TokenEnv)
	}
	for _, h := range cfg.Remote.Hosts {
		add(h.PassphraseEnv)
		add(h.PasswordEnv)
	}
	sort.Strings(out)
	return out
}

// CredentialEnvNames returns every environment-variable name whose value can
// be loaded from Reasonix's global credential store. This includes configured
// provider/bot keys and stored keys that are no longer referenced by the
// current config: loadCredentialStoreForRoot loads the whole credential file,
// so stale entries must remain outside child-process environments too.
func (c *Config) CredentialEnvNames() []string {
	names := credentialEnvNamesFromConfig(c)
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		seen[name] = true
	}
	if file, ok := readDotEnvFile(UserCredentialsPath()); ok {
		for name := range file.Values {
			name = strings.TrimSpace(name)
			if !isCredentialKey(name) || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func resolveProviderCredentialsForRoot(root string, cfg *Config) {
	if cfg == nil || len(cfg.Providers) == 0 {
		return
	}
	resolver := NewCredentialResolverForRoot(root)
	for i := range cfg.Providers {
		resolveProviderCredentialWithResolver(&cfg.Providers[i], resolver)
		// Stamp the workspace cachecontext onto every entry; when none is
		// configured, fall back to the auto "<user>:<repo>" default. Same
		// derivation feeds the OpenRouter session_id (≤256).
		cfg.Providers[i].cacheContext = cfg.EffectiveCacheContext(root)
		cfg.Providers[i].sessionContext = cfg.EffectiveSessionContext(root)
	}
}

func resolveProviderCredentialWithResolver(entry *ProviderEntry, resolver *CredentialResolver) {
	if entry == nil {
		return
	}
	key := strings.TrimSpace(entry.APIKeyEnv)
	if key == "" {
		entry.resolvedAPIKey = ""
		entry.resolvedSource = CredentialSource{}
		return
	}
	if resolver == nil {
		resolver = NewCredentialResolverForRoot(".")
	}
	res := resolver.ResolveGlobalFirst(key)
	if !res.Set || res.Value == "" {
		entry.resolvedAPIKey = ""
		entry.resolvedSource = CredentialSource{}
		return
	}
	entry.resolvedAPIKey = res.Value
	entry.resolvedSource = res.Source
}

func (e *ProviderEntry) ResolveAPIKeyForRoot(root string) {
	resolveProviderCredentialWithResolver(e, NewCredentialResolverForRoot(root))
}

func loadCredentialStoreForRoot(root string) {
	names := credentialEnvNamesForRoot(root)
	if len(names) == 0 {
		return
	}
	if p := UserCredentialsPath(); p != "" {
		loadDotEnvFileAs(p, CredentialSource{Kind: CredentialSourceCredentials, Path: p, Label: "Reasonix credentials (.env)"})
	}
}

// StoreCredentialLines stores KEY=value assignments in Reasonix's global .env
// and pins them into the current process environment.
func StoreCredentialLines(lines []string) (string, error) {
	assignments := parseCredentialLines(lines)
	if len(assignments) == 0 {
		return CredentialsTargetDescription(), nil
	}
	unlock, err := LockUserCredentialEdits()
	if err != nil {
		return "", err
	}
	defer unlock()
	return storeCredentialAssignmentsLocked(assignments)
}

// storeCredentialIfAbsentAndNotCleared writes key=value only when the current
// credential store still lacks a value and a cleared tombstone for key. The
// re-check and write share LockUserCredentialEdits so a concurrent settings
// save or tombstone cannot be overwritten by a stale keyring import.
// stored is false when the write was skipped because the key is already present
// or cleared; err is non-nil only on lock/IO failures.
func storeCredentialIfAbsentAndNotCleared(key, value string) (stored bool, err error) {
	key = strings.TrimSpace(key)
	if key == "" || !isCredentialKey(key) {
		return false, nil
	}
	if strings.ContainsAny(value, "\r\n") {
		return false, fmt.Errorf("credential value for %s contains a newline", key)
	}
	unlock, err := LockUserCredentialEdits()
	if err != nil {
		return false, err
	}
	defer unlock()
	if credentialCurrentStoreHasKey(key) || credentialCurrentStoreClearedKey(key) {
		return false, nil
	}
	if _, err := storeCredentialAssignmentsLocked(map[string]string{key: value}); err != nil {
		return false, err
	}
	return true, nil
}

func storeCredentialAssignmentsLocked(assignments map[string]string) (string, error) {
	if err := storeCredentialsInFile(UserCredentialsPath(), assignments); err != nil {
		return "", err
	}
	pinCredentialAssignments(assignments)
	return UserCredentialsPath(), nil
}

func SetCredential(key, value string) (string, error) {
	key = strings.TrimSpace(key)
	if !isCredentialKey(key) {
		return "", fmt.Errorf("invalid credential key %q", key)
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("credential value for %s contains a newline", key)
	}
	return StoreCredentialLines([]string{key + "=" + value})
}

// SetCredentialIfRevision stores one credential only when the global
// credential file still has expectedRevision. The comparison and write share
// the same process and advisory file lock, preventing a stale setup page in one
// Reasonix process from overwriting a credential saved by another process.
func SetCredentialIfRevision(key, value, expectedRevision string) (string, bool, error) {
	key = strings.TrimSpace(key)
	if !isCredentialKey(key) {
		return "", false, fmt.Errorf("invalid credential key %q", key)
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", false, fmt.Errorf("credential value for %s contains a newline", key)
	}
	assignments := parseCredentialLines([]string{key + "=" + value})
	if len(assignments) != 1 {
		return "", false, fmt.Errorf("invalid credential assignment for %s", key)
	}

	unlock, err := LockUserCredentialEdits()
	if err != nil {
		return "", false, err
	}
	defer unlock()
	if expectedRevision == "" || CredentialStoreRevision() != expectedRevision {
		return CredentialsTargetDescription(), false, nil
	}
	path, err := storeCredentialAssignmentsLocked(assignments)
	if err != nil {
		return "", false, err
	}
	return path, true, nil
}

// IsValidCredentialKey reports whether key can be stored in Reasonix's dotenv
// credential file and exposed as an environment variable.
func IsValidCredentialKey(key string) bool {
	return isCredentialKey(strings.TrimSpace(key))
}

func RemoveCredential(key string) error {
	key = strings.TrimSpace(key)
	if key == "" || !isCredentialKey(key) {
		return nil
	}
	unlock, err := LockUserCredentialEdits()
	if err != nil {
		return err
	}
	defer unlock()
	if path := UserCredentialsPath(); path != "" {
		if err := removeCredentialFromFile(path, key); err != nil {
			return err
		}
	}
	return os.Unsetenv(key)
}

// LockUserCredentialEdits serializes credential-store compare/write
// transactions in this process and across Reasonix processes. When both the
// user config and credential store are needed, acquire LockUserConfigEdits
// first, then this lock.
func LockUserCredentialEdits() (func(), error) {
	userCredentialEditMu.Lock()
	path := UserCredentialsPath()
	if strings.TrimSpace(path) == "" {
		userCredentialEditMu.Unlock()
		return nil, fmt.Errorf("credentials store unavailable")
	}
	unlockFile, err := acquireConfigFileEditLockWithTimeout(path, configEditLockTimeout)
	if err != nil {
		userCredentialEditMu.Unlock()
		return nil, fmt.Errorf("lock credential edits: %w", err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			unlockFile()
			userCredentialEditMu.Unlock()
		})
	}, nil
}

// CredentialStoreRevision returns a content-derived revision for the current
// Reasonix credential store. Callers performing compare-and-apply must hold
// LockUserCredentialEdits from this read through their commit.
func CredentialStoreRevision() string {
	path := UserCredentialsPath()
	if strings.TrimSpace(path) == "" {
		return "unavailable"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "missing"
		}
		return "unreadable"
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func CredentialIsSet(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	return CredentialStored(key)
}

func CredentialStored(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	return envFileHasValue(UserCredentialsPath(), key)
}

func credentialCurrentStoreHasKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	return envFileHasValue(UserCredentialsPath(), key)
}

func credentialCurrentStoreClearedKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	return envFileHasClearedKey(UserCredentialsPath(), key)
}

func CredentialsTargetDescription() string {
	return UserCredentialsPath()
}

func parseCredentialLines(lines []string) map[string]string {
	out := map[string]string{}
	for _, raw := range lines {
		if strings.ContainsAny(raw, "\r\n") {
			continue
		}
		values, err := godotenv.Unmarshal(raw)
		if err != nil {
			continue
		}
		for key, value := range values {
			key = strings.TrimSpace(key)
			if !isCredentialKey(key) || strings.ContainsAny(value, "\r\n") {
				continue
			}
			out[key] = value
		}
	}
	return out
}

func pinCredentialAssignments(assignments map[string]string) {
	for key, value := range assignments {
		_ = os.Setenv(key, value)
		recordCredentialSource(key, value, CredentialSource{Kind: CredentialSourceCredentials, Path: UserCredentialsPath(), Label: "Reasonix credentials (.env)"})
	}
}

func recordExistingCredentialSource(key string) {
	key = strings.TrimSpace(key)
	value := os.Getenv(key)
	if key == "" || value == "" {
		return
	}
	credentialSourceTracker.Lock()
	defer credentialSourceTracker.Unlock()
	if current, ok := credentialSourceTracker.byKey[key]; ok && current.value == value {
		return
	}
	if _, ok := credentialSourceTracker.byKey[key]; ok {
		return
	}
	credentialSourceTracker.byKey[key] = trackedCredentialSource{
		source: CredentialSource{Kind: CredentialSourceEnvironment, Label: "environment variable"},
		value:  value,
	}
}

func recordCredentialSource(key, value string, source CredentialSource) {
	key = strings.TrimSpace(key)
	if key == "" || value == "" {
		return
	}
	source.Label = credentialSourceLabel(source)
	credentialSourceTracker.Lock()
	credentialSourceTracker.byKey[key] = trackedCredentialSource{source: source, value: value}
	credentialSourceTracker.Unlock()
}

func trackedCredential(key, value string) (CredentialSource, bool) {
	credentialSourceTracker.Lock()
	defer credentialSourceTracker.Unlock()
	current, ok := credentialSourceTracker.byKey[key]
	if !ok || current.value != value {
		return CredentialSource{}, false
	}
	return current.source, true
}

func credentialSourceLabel(source CredentialSource) string {
	if strings.TrimSpace(source.Label) != "" {
		return source.Label
	}
	switch source.Kind {
	case CredentialSourceProjectEnv:
		return "project .env"
	case CredentialSourceCredentials:
		return "Reasonix credentials"
	case CredentialSourceHomeEnv:
		return "home .env"
	case CredentialSourceLegacy:
		return "legacy Reasonix credentials"
	case CredentialSourceEnvironment:
		return "environment variable"
	default:
		return ""
	}
}

func ResolveCredential(key string) CredentialResolution {
	return ResolveCredentialForRoot(".", key)
}

func ResolveCredentialForRoot(root, key string) CredentialResolution {
	key = strings.TrimSpace(key)
	res := CredentialResolution{Name: key}
	if key == "" {
		return res
	}
	value := os.Getenv(key)
	if value == "" {
		return res
	}
	res.Set = true
	res.Value = value
	if source, ok := trackedCredential(key, value); ok {
		res.Source = source
	} else if source, ok := inferCredentialSource(root, key, value); ok {
		res.Source = source
	} else {
		res.Source = CredentialSource{Kind: CredentialSourceEnvironment, Label: credentialSourceLabel(CredentialSource{Kind: CredentialSourceEnvironment})}
	}
	res.Source.Label = credentialSourceLabel(res.Source)
	res.Shadowed = shadowedCredentialSources(root, key, value, res.Source)
	return res
}

func ResolveCredentialForRootGlobalFirst(root, key string) CredentialResolution {
	key = strings.TrimSpace(key)
	return NewCredentialResolverForRoot(root).ResolveGlobalFirst(key)
}

func resolveCredentialForRootGlobalFirst(root, key string) CredentialResolution {
	root = resolveRoot(root)
	res := CredentialResolution{Name: key}
	if key == "" {
		return res
	}
	if value, source, ok := storedCredentialValueLookup(key); ok {
		res.Set = true
		res.Value = value
		res.Source = source
		res.Source.Label = credentialSourceLabel(res.Source)
		res.Shadowed = shadowedCredentialSources(root, key, value, res.Source)
		return res
	}
	return res
}

func storedCredentialValue(key string) (string, CredentialSource, bool) {
	if p := UserCredentialsPath(); p != "" {
		if value, ok := envFileValue(p, key); ok && value != "" {
			return value, CredentialSource{Kind: CredentialSourceCredentials, Path: p, Label: "Reasonix credentials (.env)"}, true
		}
	}
	return "", CredentialSource{}, false
}

func inferCredentialSource(root, key, value string) (CredentialSource, bool) {
	for _, candidate := range credentialSourceCandidates(root) {
		if v, ok := envFileValue(candidate.Path, key); ok && v == value {
			candidate.Label = credentialSourceLabel(candidate)
			return candidate, true
		}
	}
	return CredentialSource{}, false
}

func shadowedCredentialSources(root, key, activeValue string, active CredentialSource) []CredentialSource {
	var out []CredentialSource
	for _, candidate := range credentialSourceCandidates(root) {
		if sameCredentialSource(candidate, active) {
			continue
		}
		if v, ok := envFileValue(candidate.Path, key); ok && v != activeValue {
			candidate.Label = credentialSourceLabel(candidate)
			out = append(out, candidate)
		}
	}
	return out
}

func credentialSourceCandidates(root string) []CredentialSource {
	root = resolveRoot(root)
	var out []CredentialSource
	dotEnvPath := ".env"
	if root != "" && root != "." {
		dotEnvPath = filepath.Join(root, ".env")
	}
	out = append(out, CredentialSource{Kind: CredentialSourceProjectEnv, Path: dotEnvPath})
	if p := UserCredentialsPath(); p != "" {
		out = append(out, CredentialSource{Kind: CredentialSourceCredentials, Path: p})
	}
	if IsolatedHomeDir() == "" {
		if home, err := os.UserHomeDir(); err == nil {
			out = append(out, CredentialSource{Kind: CredentialSourceHomeEnv, Path: filepath.Join(home, ".env")})
		}
	}
	return out
}

func sameCredentialSource(a, b CredentialSource) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Path == "" || b.Path == "" {
		return a.Path == b.Path
	}
	return samePath(a.Path, b.Path)
}

func storeCredentialsInFile(path string, assignments map[string]string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("credentials store unavailable")
	}
	lines, err := readCredentialFileLines(path)
	if err != nil {
		return err
	}
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if key, ok := credentialClearedLineKey(line); ok {
			if _, hit := assignments[key]; hit {
				continue
			}
		}
		filtered = append(filtered, line)
	}
	lines = filtered
	replaced := map[string]bool{}
	for i, line := range lines {
		key, ok := credentialLineKey(line)
		if !ok {
			continue
		}
		if value, hit := assignments[key]; hit {
			lines[i] = formatCredentialLine(key, value)
			replaced[key] = true
		}
	}
	keys := make([]string, 0, len(assignments))
	for key := range assignments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !replaced[key] {
			lines = append(lines, formatCredentialLine(key, assignments[key]))
		}
	}
	return writeCredentialFileLines(path, lines)
}

func formatCredentialLine(key, value string) string {
	if isBareDotEnvValue(value) {
		return key + "=" + value
	}
	line, err := godotenv.Marshal(map[string]string{key: value})
	if err != nil {
		return key + "=" + value
	}
	return line
}

func isBareDotEnvValue(value string) bool {
	if value == "" {
		return true
	}
	return !strings.ContainsAny(value, " \t\r\n#'\"\\")
}

func removeCredentialFromFile(path, key string) error {
	lines, err := readCredentialFileLines(path)
	if err != nil {
		return err
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if k, ok := credentialLineKey(line); ok && k == key {
			continue
		}
		if k, ok := credentialClearedLineKey(line); ok && k == key {
			continue
		}
		out = append(out, line)
	}
	out = append(out, credentialClearedPrefix+key)
	return writeCredentialFileLines(path, out)
}

func readCredentialFileLines(path string) ([]string, error) {
	data, err := fileencoding.ReadFileUTF8(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}

func writeCredentialFileLines(path string, lines []string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("credentials store unavailable")
	}
	out := ""
	if len(lines) > 0 {
		out = strings.Join(lines, "\n") + "\n"
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, "credentials.*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(out); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := fileutil.ReplaceFile(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

func credentialLineKey(line string) (string, bool) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(line), "export ")
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	key, _, ok := strings.Cut(trimmed, "=")
	key = strings.TrimSpace(key)
	return key, ok && isCredentialKey(key)
}

func credentialClearedLineKey(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, credentialClearedPrefix) {
		return "", false
	}
	key := strings.TrimSpace(strings.TrimPrefix(trimmed, credentialClearedPrefix))
	return key, isCredentialKey(key)
}

func isCredentialKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func envFileHasValue(path, key string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	value, ok := envFileValue(path, key)
	return ok && strings.TrimSpace(value) != ""
}

func envFileHasClearedKey(path, key string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	lines, err := readCredentialFileLines(path)
	if err != nil {
		return false
	}
	for _, line := range lines {
		if k, ok := credentialClearedLineKey(line); ok && k == key {
			return true
		}
	}
	return false
}
