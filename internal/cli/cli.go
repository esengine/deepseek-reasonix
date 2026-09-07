// Package cli implements reasonix's command-line entry: subcommand routing, flag
// parsing, assembly from config, and exit codes. The core is config-driven —
// providers and tools are resolved from configuration, not hardcoded.
package cli

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"reasonix/internal/ablation"
	"reasonix/internal/agent"
	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/extension/providerext"
	fileencoding "reasonix/internal/fileutil/encoding"
	"reasonix/internal/i18n"
	"reasonix/internal/netclient"
	"reasonix/internal/notify"
	"reasonix/internal/plugin"
	"reasonix/internal/provider"
	"reasonix/internal/provider/openai"
	"reasonix/internal/serve"
	"reasonix/internal/sessiontemp"
	"reasonix/internal/telemetry"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/pflag"
	"golang.org/x/term"
)

var (
	runInteractiveSession = chatREPL
	cliIsInteractive      = isInteractive
	runWebCommand         = runWeb
	openBrowserURL        = openInBrowser
)

// Run is the CLI entry point; it returns a process exit code.
// Prefer RunWithBuildInfo when git commit / build time are available from ldflags.
func Run(args []string, version string) int {
	return RunWithBuildInfo(args, BuildInfo{Version: version})
}

// RunWithBuildInfo is the full CLI entry with optional build metadata for
// `reasonix version --verbose` / `--json`.
func RunWithBuildInfo(args []string, info BuildInfo) int {
	info = info.withDefaults()
	version := info.Version
	// Usage recording is asynchronous so provider/UI paths never wait on disk.
	// Drain accepted records and fence the projection worker before returning.
	// An embedded Run may outlive one invocation and remove its CacheDir.
	defer closeCLIUsageCatalogs()
	// Pick the UI language up front so even pre-config paths (the first-run
	// welcome banner) come through localized. Env-only first; if a config
	// exists and pins a language, that wins.
	i18n.DetectLanguage("")
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	if cmd == "--acp" {
		cmd = "acp"
	}
	// -p/--print is one-shot print mode. reasonix has no interactive -p, so a
	// print flag anywhere in a leading flag run (no explicit subcommand) routes
	// the whole set to `run --print` — `reasonix --model X -p "task"` works, not
	// only `reasonix -p ...`.
	if cmd == "-p" || cmd == "--print" || (isDefaultInteractiveFlag(cmd) && hasLeadingPrintFlag(args)) {
		args = append([]string{"run", "--print"}, stripLeadingPrintFlag(args)...)
		cmd = "run"
	}
	if len(args) > 0 && isDefaultInteractiveFlag(cmd) {
		cmd = ""
	}
	doctorRepair := isDoctorRepairCommand(args)
	if shouldMigrateLegacyConfigForCLI(cmd) && !doctorRepair {
		migrateLegacyConfigForCLI()
	}
	if !doctorRepair {
		if cfg, err := config.Load(); err == nil {
			if cfg.Language != "" {
				i18n.DetectLanguage(cfg.Language)
			}
		}
	}

	if len(args) == 0 && cliIsInteractive() {
		return runInteractiveSession(nil, version)
	}
	if len(args) == 0 {
		configureCLIThemeFromConfigForTTYOutput()
		usage()
		return 0
	}
	if cmd == "" {
		return runInteractiveSession(args, version)
	}

	rest := args[1:]
	switch cmd {
	case "run":
		return runAgent(rest, version)
	case "chat", "code": // "code" is the v0.x name for the interactive session
		return runInteractiveSession(rest, version)
	case "serve":
		return runServe(rest)
	case "web":
		return runWebCommand(rest)
	case "setup":
		configureCLIThemeFromConfigForTTYOutput()
		return setupConfig(rest)
	case "config":
		configureCLIThemeFromConfig()
		return configCommand(rest)
	case "init":
		// Project memory (AGENTS.md) is model-generated in-session — `/init` runs
		// the codebase analysis. This CLI entry just points there (and to `setup`
		// for config), so `reasonix init` isn't a dead end.
		configureCLIThemeFromConfig()
		return initHint()
	case "acp":
		configureCLIThemeFromConfig()
		return acpCommand(rest, version)
	case "mcp":
		configureCLIThemeFromConfig()
		return mcpCommand(rest)
	case "remote":
		configureCLIThemeFromConfig()
		return remoteCommand(rest, version)
	case "plugin":
		configureCLIThemeFromConfig()
		return pluginCommand(rest)
	case "subagent":
		configureCLIThemeFromConfigForTTYOutput()
		return subagentCommand(rest)
	case "doctor":
		if !doctorRepair {
			configureCLIThemeFromConfig()
		}
		return doctorCommand(rest, version)
	case "report":
		configureCLIThemeFromConfig()
		return reportCommand(rest)
	case "session", "sessions", "catalogs":
		return runSessionOrCatalogCommand(cmd, rest)
	case "hook", "hooks":
		configureCLIThemeFromConfig()
		return hookCommand(rest)
	case "task":
		configureCLIThemeFromConfig()
		return taskCommand(rest)
	case "review":
		configureCLIThemeFromConfig()
		return reviewCommand(rest)
	case "bot":
		configureCLIThemeFromConfig()
		return botCommand(rest, version)
	case "upgrade", "update":
		configureCLIThemeFromConfig()
		return upgradeCommand(rest, version)
	case "version":
		// Detailed identity: version --verbose / --json. Top-level --version/-v
		// stay single-line for script compatibility (Integration D/E).
		return versionCommand(rest, info, true)
	case "--version", "-v":
		return versionCommand(nil, info, false)
	case "completion":
		return completionCommand(rest)
	case "docs-manifest":
		return docsManifestCommand(rest, version)
	case "help", "--help", "-h":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, i18n.M.UnknownCommandFmt+"\n\n", cmd)
		usage()
		return 2
	}
}

func isDoctorRepairCommand(args []string) bool {
	return len(args) > 1 && args[0] == "doctor" && args[1] == "repair"
}

func isDefaultInteractiveFlag(arg string) bool {
	switch arg {
	case "--model", "--max-steps", "--continue", "-c", "--resume", "-r", "--copy", "--dangerously-skip-permissions", "--yolo", "--permission-mode", "--effort", "--dir", "--add-dir", "--allowed-tools", "--allowedTools", "--profile", "--preset":
		return true
	}
	if name, _, ok := strings.Cut(arg, "="); ok && isDefaultInteractiveFlag(name) {
		return true
	}
	return false
}

func shouldMigrateLegacyConfigForCLI(cmd string) bool {
	switch cmd {
	case "", "run", "chat", "code", "serve", "web", "setup", "config", "init", "acp", "mcp", "remote", "plugin", "subagent", "doctor", "bot", "upgrade", "update":
		return true
	default:
		return false
	}
}

func migrateLegacyConfigForCLI() {
	if _, err := config.MigrateLegacyIfNeeded(); err != nil {
		fmt.Fprintln(os.Stderr, "warning: config migration failed:", err)
	}
	if _, err := config.ApplyUserConfigUpgradesOnStartup(config.UserConfigPath()); err != nil {
		fmt.Fprintln(os.Stderr, "warning: config upgrade failed:", err)
	}
}

func migrateMCPConfigForCLIWorkspace() {
	if wd, err := os.Getwd(); err == nil {
		if _, err := config.MigrateMCPToUserConfigOnUpgrade([]string{wd}); err != nil {
			fmt.Fprintln(os.Stderr, "warning: MCP config migration failed:", err)
		}
	}
}

func configureCLIThemeFromConfig() {
	if cfg, err := config.Load(); err == nil {
		configureCLIThemeWithStyle(cfg.UITheme(), cfg.UIThemeStyle())
		cliCursorShape = cfg.UICursorShape()
	} else {
		configureCLITheme("auto")
		cliCursorShape = "bar"
	}
}

func configureCLIThemeFromConfigForTTYOutput() {
	if isTTY(os.Stdout) {
		withTerminalProbe(configureCLIThemeFromConfig)
		return
	}
	configureCLIThemeFromConfig()
}

// setupProfile builds a ready-to-drive Controller from config via boot.Build.
// The assembly (model resolution, tool registry, permission gate, two-model
// Coordinator) lives in internal/boot, shared with the desktop frontend.
// requireKey forces the executor's API key to be present (used by run); chat
// passes false so the session UI is reachable before a key is set.
func setupProfile(ctx context.Context, modelName string, maxStepsOverride int, requireKey bool, sink event.Sink, workspaceRoot string) (*control.Controller, error) {
	return setupProfileWithOverrides(ctx, modelName, maxStepsOverride, requireKey, sink, cliBuildOverrides{WorkspaceRoot: workspaceRoot})
}

type cliBuildOverrides struct {
	Preset               string
	Effort               *string
	PermissionAllow      []string
	AdditionalDirs       []string
	WorkspaceRoot        string
	HeadlessApprovalMode string
	Stderr               io.Writer
	OnSessionRecovered   func(control.SessionRecoveryInfo) error
	Ablation             ablation.Set
	// InteractiveHost marks human-in-the-loop entries (chat TUI); print mode
	// and bots stay on core-v1.
	InteractiveHost bool
	// SessionTemp carries the previous Controller's private temporary directory
	// manager across model/profile rebuilds so temporary files survive.
	SessionTemp *sessiontemp.Manager
}

// sessionTempFromCLIController returns the logical-session private temporary
// directory manager for a same-session CLI controller rebuild. Nil keeps fresh
// builds on control.New's normal new-manager path.
func sessionTempFromCLIController(ctrl control.SessionAPI) *sessiontemp.Manager {
	prev, ok := ctrl.(*control.Controller)
	if !ok || prev == nil {
		return nil
	}
	return prev.SessionTemp()
}

func setupProfileWithOverrides(ctx context.Context, modelName string, maxStepsOverride int, requireKey bool, sink event.Sink, overrides cliBuildOverrides) (*control.Controller, error) {
	migrateMCPConfigForCLIWorkspace()
	return boot.Build(ctx, cliProfileBuildOptions(modelName, maxStepsOverride, requireKey, sink, overrides))
}

func cliProfileBuildOptions(modelName string, maxStepsOverride int, requireKey bool, sink event.Sink, overrides cliBuildOverrides) boot.Options {
	opts := boot.Options{
		Model:                modelName,
		MaxSteps:             maxStepsOverride,
		MaxStepsKey:          "--max-steps",
		RequireKey:           requireKey,
		Sink:                 sink,
		SessionDir:           resolveCLISessionDir(),
		AgentPreset:          overrides.Preset,
		WorkspaceRoot:        overrides.WorkspaceRoot,
		EffortOverride:       overrides.Effort,
		PermissionAllow:      overrides.PermissionAllow,
		AdditionalDirs:       overrides.AdditionalDirs,
		HeadlessApprovalMode: overrides.HeadlessApprovalMode,
		StatsSource:          "cli",
		Stderr:               overrides.Stderr,
		OnSessionRecovered:   overrides.OnSessionRecovered,
		Ablation:             overrides.Ablation,
		SessionTemp:          overrides.SessionTemp,
	}
	opts.MCPHostProfile = plugin.HostProfileForInteractive(overrides.InteractiveHost)
	return opts
}

type cliPermissionMode struct {
	approval string
	plan     bool
	allow    []string
}

func parsePermissionMode(value string) (cliPermissionMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "default", "ask":
		return cliPermissionMode{approval: control.ToolApprovalAsk}, nil
	case "auto":
		return cliPermissionMode{approval: control.ToolApprovalAuto}, nil
	case "acceptedits", "accept-edits":
		return cliPermissionMode{approval: control.ToolApprovalAsk, allow: []string{
			"write_file", "edit_file", "multi_edit", "move_file", "notebook_edit", "delete_range", "delete_symbol",
		}}, nil
	case "manual":
		return cliPermissionMode{approval: control.ToolApprovalAsk}, nil
	case "dontask", "dont-ask":
		return cliPermissionMode{approval: control.ToolApprovalDontAsk}, nil
	case "plan":
		return cliPermissionMode{approval: control.ToolApprovalAsk, plan: true}, nil
	case "bypasspermissions", "bypass-permissions", "yolo":
		return cliPermissionMode{approval: control.ToolApprovalYolo}, nil
	default:
		return cliPermissionMode{}, fmt.Errorf("unknown permission mode %q (want manual, ask, auto, acceptEdits, dontAsk, plan, or bypassPermissions)", value)
	}
}

func resolveRunPermissionMode(value string, auto, modeExplicit bool) (string, error) {
	if !auto {
		return value, nil
	}
	if modeExplicit {
		return "", errors.New("--auto/-y cannot be combined with --permission-mode")
	}
	return "auto", nil
}

func applyPermissionMode(ctrl *control.Controller, mode cliPermissionMode) {
	if ctrl == nil {
		return
	}
	ctrl.SetToolApprovalMode(mode.approval)
	ctrl.SetPlanMode(mode.plan)
}

// resolveCLISessionDir returns the session dir for CLI invocations. When the
// current working directory maps to a project session dir, the project dir is
// used so /resume shows project history. Falls back to the global session dir.
func resolveCLISessionDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return config.SessionDir()
	}
	if projDir := config.ProjectSessionDir(cwd); projDir != "" && projDir != config.SessionDir() {
		return projDir
	}
	return config.SessionDir()
}

// setupQuietProfile is like setupProfile but guarantees plugin subprocess
// stderr stays off the terminal. Interactive callers provide the private TUI
// diagnostic writer; other callers fall back to io.Discard.
func setupQuietProfile(ctx context.Context, modelName string, maxStepsOverride int, requireKey bool, sink event.Sink, overrides cliBuildOverrides) (*control.Controller, error) {
	if overrides.Stderr == nil {
		overrides.Stderr = io.Discard
	}
	return boot.Build(ctx, cliProfileBuildOptions(modelName, maxStepsOverride, requireKey, sink, overrides))
}

// parseRuntimeProfile validates a role-flag value and maps it onto the
// session quality floor: light folds to standard silently, delivery sets the
// delivery floor.
func parseRuntimeProfile(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "balanced", "standard", boot.TokenModeFull:
		return "standard", nil
	case "economy", "light", "lite", "eco":
		return "standard", nil
	case boot.TokenModeDelivery, "deliver", "quality":
		return "delivery", nil
	default:
		return "", fmt.Errorf("unknown execution setting %q (accepted: standard, delivery; legacy light folds to standard)", value)
	}
}

// chdirTo honours --dir: it switches the working directory before anything reads
// it, so config discovery, the sandbox root, and file tools all resolve from the
// chosen project root. Returns 2 (already reported) on failure, 0 otherwise.
func chdirTo(dir string) int {
	if dir == "" {
		return 0
	}
	if err := os.Chdir(dir); err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	return 0
}

// workspaceRootForDir returns the explicit project root to pin when --dir was
// given. It runs after chdirTo has already switched into dir, so the process
// working directory is the resolved root. An empty dir means no override (fall
// back to git-root detection). A Getwd failure is returned rather than swallowed:
// silently reverting to "" would re-trigger git-root/default resolution and break
// the explicit --dir guarantee, so the caller must fail loudly instead.
func workspaceRootForDir(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve --dir workspace root: %w", err)
	}
	return wd, nil
}

func modelForResumePath(modelName, resumePath string, cfg *config.Config) string {
	if strings.TrimSpace(modelName) != "" || strings.TrimSpace(resumePath) == "" {
		return modelName
	}
	sessionModel, ok := agent.LoadSessionModel(resumePath)
	if !ok {
		return modelName
	}
	if cfg == nil {
		return sessionModel
	}
	if _, ok := cfg.ResolveModel(sessionModel); !ok {
		return modelName
	}
	return sessionModel
}

func loadResumableSession(path string) (*agent.Session, error) {
	if agent.IsCleanupPending(path) {
		return nil, fmt.Errorf("session is pending cleanup")
	}
	return agent.LoadSession(path)
}

var newNotificationSender = func() notify.Sender { return notify.NewPlatformSender() }

// withNotifications adds system notifications to CLI event streams when configured.
func withNotifications(sink event.Sink, cfg *config.Config) event.Sink {
	if cfg == nil || !cfg.Notifications.Enabled {
		return sink
	}
	return notify.NewSink(sink, newNotificationSender(), cfg.Notifications)
}

// registerContinueFlag registers --continue with its -c shorthand. The
// shorthand must go through BoolP (pflag shorthand), not BoolVar: BoolVar
// registers "c" as a long flag name, which leaves "-c" unparseable
// ("unknown shorthand flag: 'c' in -c") while accidentally accepting "--c".
func registerContinueFlag(fs *pflag.FlagSet) *bool {
	return fs.BoolP("continue", "c", false, "resume the most recent saved session")
}

func runAgent(args []string, version string) int {
	defer closeCLIUsageCatalogs()
	args, deprecatedMode, err := consumeDeprecatedModeFlags(args, "profile", "preset")
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	fs := pflag.NewFlagSet("run", pflag.ContinueOnError)
	fs.SetInterspersed(true)
	model := fs.String("model", "", "provider name (default: config default_model)")
	maxSteps := fs.Int("max-steps", 0, "one-off max tool-call rounds (0 = automatic)")
	showThinking := fs.Bool("show-thinking", false, "show thinking text instead of the collapsed thinking marker")
	metricsPath := fs.String("metrics", "", "write a JSON token/cache/cost summary of the run to this path")
	trajectoryPath := fs.String("trajectory", "", "append a timestamped JSONL trajectory of the run's full event stream (tool calls, reasoning, decisions) to this path")
	ablateFlag := fs.String("ablate", "", "benchmark arm: comma-separated subsystems to switch off (evidence, planner, subagent, retrieval, compaction; none|all)")
	dir := fs.String("dir", "", "change to this directory first (project root); config, sandbox and file tools resolve from here")
	cont := registerContinueFlag(fs)
	resume := fs.String("resume", "", "resume by session file path, session ID, or machine session ID (takes precedence over --continue)")
	copySession := fs.Bool("copy", false, "with --resume/--continue: duplicate the session and continue in the copy (escape hatch when the original is held by another Reasonix process)")
	takeover := fs.Bool("takeover", false, "with --resume/--continue: when a resident serve on this machine holds the session, take it over instead of refusing")
	effort := fs.String("effort", "", "session reasoning effort override")
	permissionMode := fs.String("permission-mode", "ask", "permission mode: manual | ask | auto | acceptEdits | dontAsk | plan | bypassPermissions")
	autoApprove := fs.BoolP("auto", "y", false, "explicitly auto-approve ordinary writer fallbacks (alias for --permission-mode auto)")
	printOnly := fs.BoolP("print", "p", false, "print only the final response")
	eventsJSONL := fs.Bool("events-jsonl", false, "emit a redacted structured event stream as JSONL")
	outputFormat := fs.String("output-format", "text", "output format: text | json | stream-json")
	var additionalDirs []string
	fs.StringArrayVar(&additionalDirs, "add-dir", nil, "allow tool access to an additional directory (repeatable)")
	var allowedToolValues []string
	fs.StringArrayVar(&allowedToolValues, "allowed-tools", nil, "comma or space-separated permission rules to allow")
	fs.StringArrayVar(&allowedToolValues, "allowedTools", nil, "alias for --allowed-tools")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	resolvedPermissionMode, err := resolveRunPermissionMode(*permissionMode, *autoApprove, fs.Changed("permission-mode"))
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	*permissionMode = resolvedPermissionMode
	allowedTools, err := splitAllowedToolRules(allowedToolValues)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	format, err := parseRunOutputFormat(*outputFormat)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	if *eventsJSONL {
		if fs.Changed("output-format") {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, "--events-jsonl cannot be combined with --output-format")
			return 2
		}
		format = runOutputEventsJSONL
	}
	if err := acceptDeprecatedModeFlag(deprecatedMode); err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	ablated, err := ablation.Parse(*ablateFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	permissions, err := parsePermissionMode(*permissionMode)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	if permissions.plan {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, "--permission-mode plan requires an interactive session")
		return 2
	}
	allowedTools = uniqueStrings(append(allowedTools, permissions.allow...))
	if rc := chdirTo(*dir); rc != 0 {
		return rc
	}
	workspaceRoot, err := workspaceRootForDir(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	cfg, _ := config.Load()
	configureCLIThemeFromConfigForTTYOutput()

	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		prompt = readStdin()
	}
	if prompt == "" {
		fmt.Fprintln(os.Stderr, i18n.M.UsageRunHint)
		return 2
	}
	var machineIdentityKey []byte
	if format == runOutputEventsJSONL {
		machineIdentityKey, err = loadMachineIdentityKey()
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, "machine identity is unavailable")
			return 1
		}
	}

	// Resolve the resume target up front so --copy and the session lease can be
	// handled before any heavy assembly. --resume takes precedence over
	// --continue, matching the Resume call below. Accept file paths, branch
	// IDs, preview text, and opaque machine session IDs (#7429).
	resumePath := strings.TrimSpace(*resume)
	if resumePath != "" {
		resolved, err := resolveSessionQuery(resolveCLISessionDir(), resumePath)
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			return 1
		}
		resumePath = resolved
	}
	if resumePath == "" && *cont {
		sessionDir := resolveCLISessionDir()
		reclaimCLIRecoveryBranches(sessionDir)
		session, ok := mostRecentSession(sessionDir)
		if !ok {
			fmt.Fprintln(os.Stderr, i18n.M.NoSessionToResume)
			return 1
		}
		resumePath = session.Path
	}
	if *copySession && resumePath == "" {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, "--copy requires --resume or --continue")
		return 2
	}
	if *copySession {
		copied, err := copySessionForWriting(resumePath)
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			return 1
		}
		// Keep structured (json/stream-json) and --print stdout a single
		// machine-readable payload: the human copy notice goes to stderr there.
		// Plain text runs keep it on stdout, where callers scrape the copied path.
		if format == runOutputText && !*printOnly {
			fmt.Printf("continuing in a session copy: %s\n", copied)
		} else {
			fmt.Fprintf(os.Stderr, "continuing in a session copy: %s\n", copied)
		}
		resumePath = copied
	}
	sessionMode := cliTelemetrySessionMode(*cont, strings.TrimSpace(*resume) != "", *copySession)
	reporter := startCLITelemetry(cfg, telemetry.Options{
		Version: version, Interactive: false, CLIMode: "run",
		PermissionMode: *permissionMode, SessionMode: sessionMode,
	})

	// Own the session file for the lifetime of this run so a desktop window (or
	// another CLI) writing the same session is refused up front instead of
	// silently double-writing. Released after the controller closes.
	leases := control.NewSessionLeaseKeeper()
	defer leases.Release()
	takeoverManager := newCLITakeoverManager(nil, leases)
	defer func() {
		if err := takeoverManager.Close(); err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		}
	}()
	var resumeSession *agent.Session
	var takeoverBinding *cliTakeoverBinding
	if resumePath != "" {
		var err error
		resumeSession, err = bindAndLoadCLIResume(leases, resumePath, loadResumableSession)
		if errors.Is(err, agent.ErrSessionLeaseHeld) && *takeover {
			takeoverBinding, err = cliTakeoverHeldSession(resumePath, err, leases, takeoverManager)
			if err != nil {
				fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
				return 1
			}
			resumeSession, err = cliPrepareTakeoverCandidate(takeoverBinding, leases)
			if err != nil {
				_ = cliReturnFailedTakeover(takeoverBinding, leases, takeoverManager)
				fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
				return 1
			}
		}
		if err != nil {
			if errors.Is(err, agent.ErrSessionLeaseHeld) {
				fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, sessionLeaseResumeRefusal(err))
			} else {
				fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			}
			return 1
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	started := time.Now()

	chain, err := buildRunSink(format, *printOnly, *showThinking, *metricsPath, *trajectoryPath, cfg, reporter)
	if err != nil {
		_ = cliReturnFailedTakeover(takeoverBinding, leases, takeoverManager)
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	takeoverManager.SetInner(chain.sink)
	chain.sink = takeoverManager
	sink, resultOutput, metrics := chain.sink, chain.resultOutput, chain.metrics
	if resumePath != "" {
		*model = modelForResumePath(*model, resumePath, cfg)
	}
	var effortOverride *string
	if strings.TrimSpace(*effort) != "" {
		effortOverride = effort
	}
	// `reasonix run` is headless: there is no key loop to answer approval or ask
	// prompts, and the approval timeout defaults to infinite. Installing the
	// interactive approver/asker here would let an Ask rule, the `ask` tool, or a
	// sandbox/config approval wedge the run forever. Map the mode onto a
	// non-blocking headless gate instead — passed into boot.Build so every
	// headless-only gate it constructs (task/read_only_task, writer-capable
	// skill sub-agents, the planner runner) gets the same contract as the parent
	// executor, not just the top-level one. Default/ask fails closed because no
	// UI can answer; unattended writes require explicit --auto/-y,
	// --permission-mode auto, or yolo.
	overrides := cliBuildOverrides{
		Preset:               deprecatedMode,
		Effort:               effortOverride,
		PermissionAllow:      allowedTools,
		AdditionalDirs:       additionalDirs,
		WorkspaceRoot:        workspaceRoot,
		HeadlessApprovalMode: permissions.approval,
		OnSessionRecovered:   cliSessionRecoveredHandler(leases),
		Ablation:             ablated,
	}
	ctrl, err := setupProfileWithOverrides(ctx, *model, *maxSteps, true, sink, overrides)
	if err != nil {
		_ = cliReturnFailedTakeover(takeoverBinding, leases, takeoverManager)
		if resultOutput != nil && format != runOutputText {
			if encodeErr := resultOutput.Finalize("", started, err); encodeErr != nil {
				fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, encodeErr)
			}
			return 1
		}
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	defer ctrl.Close()
	takeoverManager.AttachController(ctrl)
	SetTaskJobKiller(ctrlKillerAdapter{ctrl})
	ctrl.ApplyHeadlessApprovalMode(permissions.approval)

	// --resume: load a specific session file (non-interactive, meant for
	// MCP/API callers that manage their own per-project session). Takes
	// precedence over --continue.
	// --continue: resume the most recent saved session.
	if resumePath != "" {
		if err := takeoverBinding.commitPrevious(takeoverManager); err != nil {
			_ = cliReturnFailedTakeover(takeoverBinding, leases, takeoverManager)
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			return 1
		}
		ctrl.Resume(resumeSession, resumePath)
	}
	if ctrl.SessionPath() == "" && ctrl.SessionDir() != "" {
		ctrl.SetFreshSessionPath(agent.NewSessionPath(ctrl.SessionDir(), ctrl.Label()))
	}
	// Fresh sessions take the lease too (defensive: the path is brand new); a
	// resumed path is already held, making this a no-op.
	if err := rebindCLIControllerAuthority(leases, ctrl); err != nil {
		_ = cliReturnFailedTakeover(takeoverBinding, leases, takeoverManager)
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, control.SessionInUseMessage(err)+"; "+control.SessionLeaseCloseHint)
		return 1
	}
	if takeoverBinding != nil {
		takeoverManager.Activate(takeoverBinding)
	}
	reclaimCLIRecoveryBranches(ctrl.SessionDir())

	runErr := ctrl.Run(ctx, prompt)
	reporter.RecordRecovery(ctrl.DrainRecoveryMetrics())
	completion := classifyRunCompletion(runErr)
	if cfg != nil {
		notify.SendEvent(newNotificationSender(), cfg.Notifications, event.Event{
			Kind:    event.TurnDone,
			Err:     runErr,
			Outcome: completion.outcome,
		})
	}
	if metrics != nil {
		// Snapshot under the sink's lock: a background job can still be emitting
		// into it while this goroutine assembles the final record.
		final := metrics.Snapshot()
		final.DurationMs = time.Since(started).Milliseconds()
		final.Outcome = completion.class
		final.Arm = ablated.Arm()
		if exec := ctrl.Executor(); exec != nil {
			if audit := exec.CapabilityAudit(); audit != nil {
				snap := audit.Snapshot()
				final.MergeCapabilityAudit(&snap)
			}
		}
		if err := writeMetrics(*metricsPath, final); err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		}
	}
	if chain.trajectory != nil {
		if err := chain.trajectory.Close(); err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		}
	}
	if resultOutput != nil {
		sessionID := runOutputSessionID(format, agent.BranchID(ctrl.SessionPath()), machineIdentityKey)
		if err := resultOutput.Finalize(sessionID, started, runErr); err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			return 1
		}
	}
	if runErr != nil {
		reportRunFailure(os.Stderr, format, resultOutput != nil, completion, runErr)
		return completion.exitCode
	}
	return completion.exitCode
}

func runServeWithOptions(args []string, opts serveRunOptions) int {
	if opts.command == "" {
		opts.command = "serve"
	}
	args, deprecatedMode, err := consumeDeprecatedModeFlags(args, "profile", "preset")
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	fs := flag.NewFlagSet(opts.command, flag.ContinueOnError)
	model := fs.String("model", "", "provider name (default: config default_model)")
	maxSteps := fs.Int("max-steps", 0, "one-off max tool-call rounds (0 = automatic)")
	addr := fs.String("addr", "127.0.0.1:8787", "listen address")
	resume := fs.String("resume", "", "resume a saved session file")
	sessionIDValue := ""
	sessionID := &sessionIDValue
	if opts.command == "web" {
		sessionID = fs.String("session-id", "", "bind a fresh Web session identity (used by /web handoff)")
	}
	authHelp := "auth mode: none, token, or password (default: config/none)"
	if opts.command == "web" {
		authHelp = "auth mode: none, token, or password (default: generated token)"
	}
	auth := fs.String("auth", "", authHelp)
	token := fs.String("token", "", "pre-shared token for auth=token (auto-generated if empty)")
	password := fs.String("password", "", "password for auth=password (use --hash-password to store a hash instead)")
	hashPassword := fs.Bool("hash-password", false, "print a bcrypt hash of --password and exit")
	behindProxy := fs.Bool("behind-proxy", false, "trust X-Forwarded-For / X-Forwarded-Proto headers from a reverse proxy")
	portFile := fs.String("port-file", "", "write the actual bound listen address (host:port) to this file after binding")
	tokenFile := fs.String("token-file", "", "read the auth=token pre-shared token from this file (overrides --token; keeps the secret out of argv)")
	pidFile := fs.String("pid-file", "", "write the server process id to this file")
	registerServeCapabilityFlags(fs)
	openBrowser := fs.Bool("open", opts.openBrowser, "open the Web UI in the default browser")
	noOpen := fs.Bool("no-open", false, "do not open the Web UI in the default browser")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	authExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "auth" {
			authExplicit = true
		}
	})
	if *resume != "" && *sessionID != "" {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, "--resume and --session-id cannot be used together")
		return 2
	}
	if *sessionID != "" {
		if err := validateWebSessionID(*sessionID); err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			return 2
		}
	}
	if err := acceptDeprecatedModeFlag(deprecatedMode); err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}

	// --hash-password: generate a bcrypt hash and exit.
	if *hashPassword {
		if *password == "" {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, "--hash-password requires --password")
			return 1
		}
		h, err := serve.HashPassword(*password)
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			return 1
		}
		fmt.Println(h)
		return 0
	}

	ctx := context.Background()
	bc, sessionTag, cfg := newServeBootstrap()

	// Build serve config, merging CLI flags over config file.
	serveCfg := serveConfigWithCommandDefaults(opts.command, authExplicit, cfg.Serve)
	// `reasonix web` is a local browser entry point and defaults to a freshly
	// generated token. `reasonix serve` keeps its existing config-driven default,
	// and an explicit --auth always wins for both commands.
	if *auth != "" {
		serveCfg.AuthMode = *auth
	}
	if *token != "" {
		serveCfg.Token = *token
	}
	if *tokenFile != "" {
		tok, err := readServeTokenFile(*tokenFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			return 1
		}
		serveCfg.Token = tok
	}
	if *behindProxy {
		serveCfg.BehindProxy = true
	}
	mode, err := serve.NormalizeAuthMode(serveCfg.AuthMode)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	serveCfg.AuthMode = mode
	if *password != "" && serveCfg.AuthMode == "password" {
		// Hash the password at startup so the config never stores plaintext.
		// If a PasswordHash is already set in config, the CLI password overrides it.
		h, err := serve.HashPassword(*password)
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, "failed to hash password:", err)
			return 1
		}
		serveCfg.PasswordHash = h
	}
	if serveCfg.AuthMode == "password" && strings.TrimSpace(serveCfg.PasswordHash) == "" {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, "auth mode password requires --password or serve.password_hash")
		return 1
	}

	// Own the active session file for the server's lifetime; the serve
	// handlers that rebind sessions (/resume, /new, /fork) move the lease
	// through the same keeper. Released after the controller closes.
	leases := control.NewSessionLeaseKeeper()
	defer leases.Release()
	var resumeSession *agent.Session
	if *resume != "" {
		if err := leases.Rebind(*resume); err != nil {
			if errors.Is(err, agent.ErrSessionLeaseHeld) {
				fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, control.SessionInUseMessage(err)+"; "+control.SessionLeaseCloseHint)
			} else {
				fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			}
			return 1
		}
		var err error
		resumeSession, err = loadResumableSession(*resume)
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			return 1
		}
	}
	*model = modelForResumePath(*model, *resume, cfg)
	// Serve always resolves an implicit model from the user-global config,
	// ignoring project-level default_model overrides. Explicit flags and
	// resumable session models remain strict and are preserved verbatim.
	*model = resolveServeModel(*model)
	// Keep the browser reachable when the selected provider has no saved key.
	// The loopback-only provider setup surface stores the missing credential and
	// rebuilds this controller in place before the normal web UI is exposed.
	ctrl, serveBuildOpts, err := setupCLIMultiSessionProfile(ctx, *model, *maxSteps, deprecatedMode, sessionTag, leases)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	defer ctrl.Close()
	SetTaskJobKiller(ctrlKillerAdapter{ctrl})

	// Auto-save target: reuse the resumed file, else a fresh one — same as chat.
	if *resume != "" {
		ctrl.Resume(resumeSession, *resume)
	} else if *sessionID != "" {
		freshPath, err := freshWebSessionPath(ctrl.SessionDir(), *sessionID)
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			return 1
		}
		ctrl.SetFreshSessionPath(freshPath)
	}
	ctrl.EnsureSessionPath()
	// Fresh sessions take the lease too (defensive: the path is brand new); a
	// resumed path is already held, making this a no-op.
	if err := rebindCLIControllerAuthority(leases, ctrl); err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, control.SessionInUseMessage(err)+"; "+control.SessionLeaseCloseHint)
		return 1
	}

	srv := newCLIMultiSessionServer(ctrl, bc, sessionTag, serveCfg, leases, serveBuildOpts)
	defer srv.Close()
	return runServeFrontend(ctrl, srv, serveCfg, serveFrontendOptions{
		command: opts.command, address: *addr,
		portFile: *portFile, tokenFile: *tokenFile, pidFile: *pidFile,
		openBrowser: *openBrowser && !*noOpen,
		hasSession:  *resume != "" || *sessionID != "",
	})
}

// chatREPL is an interactive session: a single persistent agent/session and a
// prompt loop that keeps conversation context across turns. Exit with
// 'exit'/'quit' or Ctrl-D.
func chatREPL(args []string, version string) int {
	args, deprecatedMode, err := consumeDeprecatedModeFlags(args, "profile", "preset")
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	fs := pflag.NewFlagSet("reasonix", pflag.ContinueOnError)
	fs.SetInterspersed(true)
	model := fs.String("model", "", "provider name (default: config default_model)")
	maxSteps := fs.Int("max-steps", 0, "one-off max tool-call rounds (0 = automatic)")
	cont := registerContinueFlag(fs)
	resume := fs.StringP("resume", "r", "", "resume by session ID/query, or open the picker when no value is given")
	fs.Lookup("resume").NoOptDefVal = resumePickerSentinel
	copySession := fs.Bool("copy", false, "with --resume/--continue: duplicate the selected session and continue in the copy (escape hatch when the original is held by another Reasonix process)")
	yolo := fs.Bool("dangerously-skip-permissions", false, "YOLO: auto-approve approval-gated tool calls this session; same runtime mode as Ctrl+Y")
	fs.BoolVar(yolo, "yolo", false, "alias for --dangerously-skip-permissions")
	dir := fs.String("dir", "", "change to this directory first (project root); config, sandbox and file tools resolve from here")
	effort := fs.String("effort", "", "session reasoning effort override")
	permissionMode := fs.String("permission-mode", "ask", "permission mode: manual | ask | auto | acceptEdits | dontAsk | plan | bypassPermissions")
	var additionalDirs []string
	fs.StringArrayVar(&additionalDirs, "add-dir", nil, "allow tool access to an additional directory (repeatable)")
	var allowedToolValues []string
	fs.StringArrayVar(&allowedToolValues, "allowed-tools", nil, "comma or space-separated permission rules to allow")
	fs.StringArrayVar(&allowedToolValues, "allowedTools", nil, "alias for --allowed-tools")
	if code, ok := parseCommandFlags(fs, normalizeOptionalResumeArg(args)); !ok {
		return code
	}
	allowedTools, err := splitAllowedToolRules(allowedToolValues)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	if err := acceptDeprecatedModeFlag(deprecatedMode); err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	permissions, err := parsePermissionMode(*permissionMode)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	allowedTools = uniqueStrings(append(allowedTools, permissions.allow...))
	if rc := chdirTo(*dir); rc != 0 {
		return rc
	}
	workspaceRoot, err := workspaceRootForDir(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	// Bubble Tea owns the terminal from the resume picker through controller
	// shutdown. Start diagnostics before config/controller work so hangs leave a
	// non-zero log with milestones (#7435, #7507).
	diagnostics := startTUIDiagnostics(config.ReasonixHomeDir())
	defer diagnostics.Close()
	diagnostics.Milestone("config_load_begin")
	cfg, err := config.Load()
	if err == nil {
		configureCLIThemeWithStyle(cfg.UITheme(), cfg.UIThemeStyle())
		cliCursorShape = cfg.UICursorShape()
	}
	diagnostics.Milestone("config_load_done")

	// Decide whether we're starting fresh or resuming. --resume opens an
	// interactive picker; --continue / -c jumps straight into the newest.
	var resumePath string
	resumeValue := strings.TrimSpace(*resume)
	switch strings.ToLower(resumeValue) {
	case "true":
		resumeValue = resumePickerSentinel
	case "false":
		resumeValue = ""
	}
	switch {
	case resumeValue == resumePickerSentinel:
		path, rc := pickSessionToResume()
		if rc != 0 {
			return rc
		}
		resumePath = path
	case resumeValue != "":
		path, err := resolveSessionQuery(resolveCLISessionDir(), resumeValue)
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			return 1
		}
		resumePath = path
	case *cont:
		sessionDir := resolveCLISessionDir()
		reclaimCLIRecoveryBranches(sessionDir)
		session, ok := mostRecentSession(sessionDir)
		if !ok {
			fmt.Fprintln(os.Stderr, i18n.M.NoSessionToResume)
			return 1
		}
		resumePath = session.Path
	}
	if *copySession && resumePath == "" {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, "--copy requires --resume or --continue")
		return 2
	}
	if *copySession {
		copied, err := copySessionForWriting(resumePath)
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			return 1
		}
		fmt.Printf("continuing in a session copy: %s\n", copied)
		resumePath = copied
	}
	sessionMode := cliTelemetrySessionMode(*cont, resumeValue != "", *copySession)
	reporter := startCLITelemetry(cfg, telemetry.Options{
		Version: version, Interactive: isInteractive(), CLIMode: "tui",
		PermissionMode: *permissionMode, SessionMode: sessionMode,
	})

	// Own the active session file for the TUI's lifetime; in-TUI switches
	// (/resume, /switch, /new, ...) move the lease with the active path.
	// Refusing a held resume target up front is what keeps a desktop window
	// and this chat from silently double-writing one transcript.
	leases := control.NewSessionLeaseKeeper()
	defer leases.Release()
	takeoverManager := newCLITakeoverManager(nil, leases)
	defer func() {
		if err := takeoverManager.Close(); err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		}
	}()
	var takeoverBinding *cliTakeoverBinding
	var startupResumeSession *agent.Session
	if resumePath != "" {
		startupResumeSession, err = bindAndLoadCLIResume(leases, resumePath, loadResumableSession)
		if errors.Is(err, agent.ErrSessionLeaseHeld) && cliSessionTakeoverCandidate(err) && promptSessionTakeover(err) {
			takeoverBinding, err = cliTakeoverHeldSession(resumePath, err, leases, takeoverManager)
			if err != nil {
				fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
				return 1
			}
			startupResumeSession, err = cliPrepareTakeoverCandidate(takeoverBinding, leases)
			if err != nil {
				_ = cliReturnFailedTakeover(takeoverBinding, leases, takeoverManager)
				fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
				return 1
			}
		}
		if err != nil {
			if errors.Is(err, agent.ErrSessionLeaseHeld) {
				fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, sessionLeaseResumeRefusal(err))
			} else {
				fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			}
			return 1
		}
	}

	ctx := context.Background()
	*model = modelForResumePath(*model, resumePath, cfg)

	// Plumb the controller's typed event stream through a channel so each event
	// can become a tea.Msg inside the TUI's update loop. Buffered generously:
	// streaming bursts (tool results, long answers) shouldn't backpressure the
	// agent goroutine.
	eventCh := make(chan event.Event, 1024)

	var sink event.Sink = &eventSink{ch: eventCh}
	sink = withNotifications(sink, cfg)
	sink = reporter.Wrap(sink)
	takeoverManager.SetInner(sink)
	sink = takeoverManager
	var effortOverride *string
	if strings.TrimSpace(*effort) != "" {
		effortOverride = effort
	}
	overrides := cliBuildOverrides{
		Preset:             deprecatedMode,
		Effort:             effortOverride,
		PermissionAllow:    allowedTools,
		AdditionalDirs:     additionalDirs,
		WorkspaceRoot:      workspaceRoot,
		InteractiveHost:    true,
		Stderr:             diagnostics.Writer(),
		OnSessionRecovered: cliSessionRecoveredHandler(leases),
	}
	diagnostics.Milestone("controller_build_begin")
	ctrl, err := setupProfileWithOverrides(ctx, *model, *maxSteps, false, sink, overrides)
	if err != nil && errors.Is(err, boot.ErrUnknownModel) && isInteractive() && config.SourcePath() == "" {
		// True first run whose default model can't resolve: guide setup, then retry.
		// With a config present, fall through to the descriptive error — re-running
		// the wizard would overwrite the user's config (#2856).
		fmt.Fprintln(os.Stderr, i18n.M.ReconfigureOnUnknownModel)
		if rc := interactiveSetup(defaultConfigTarget(), defaultEnvTarget()); rc != 0 {
			_ = cliReturnFailedTakeover(takeoverBinding, leases, takeoverManager)
			return rc
		}
		ctrl, err = setupProfileWithOverrides(ctx, *model, *maxSteps, false, sink, overrides)
	}
	if err != nil {
		_ = cliReturnFailedTakeover(takeoverBinding, leases, takeoverManager)
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	diagnostics.Milestone("controller_build_done")

	// Decide where this conversation's auto-save lands. A resume reuses the
	// file so closing/reopening keeps appending to the same history; a fresh
	// session lands in a new file stamped with the model name.
	if resumePath != "" {
		if err := takeoverBinding.commitPrevious(takeoverManager); err != nil {
			_ = cliReturnFailedTakeover(takeoverBinding, leases, takeoverManager)
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			return 1
		}
		ctrl.Resume(startupResumeSession, resumePath)
	}
	ctrl.EnsureSessionPath()
	// Fresh sessions take the lease too (defensive: the path is brand new); a
	// resumed path is already held, making this a no-op.
	if err := rebindCLIControllerAuthority(leases, ctrl); err != nil {
		_ = cliReturnFailedTakeover(takeoverBinding, leases, takeoverManager)
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, control.SessionInUseMessage(err)+"; "+control.SessionLeaseCloseHint)
		return 1
	}
	reclaimCLIRecoveryBranches(ctrl.SessionDir())

	// Surface a missing-key warning inside the TUI banner so the first message
	// failing is at least pre-announced; the user can still enter chat.
	// resolveModelForCLI transparently falls through a keyless default to the
	// next configured provider (issue #6996). Validating the final ref is a
	// no-op for that configured fallback and preserves the warning when every
	// eligible chat provider is still keyless.
	missing := ""
	if cfg, loadErr := config.Load(); loadErr == nil {
		name, _, err := resolveModelForCLI(*model, cfg)
		switch {
		case err != nil:
			missing = err.Error()
		case name != "" && providerext.PluginRefOwner(name) != "":
			// Plugin-namespaced refs hold no config credential; boot's merged
			// resolver already gated them, and there is no key env to warn about.
		case name != "":
			if vErr := cfg.Validate(name); vErr != nil {
				missing = vErr.Error()
			}
		}
	}

	// Initial terminal width — the TUI re-flows on every WindowSizeMsg so
	// this is just a starting estimate before the first resize event lands.
	termW := 80
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		termW = w
	}

	// Route "ask" decisions to the TUI: the controller emits an ApprovalRequest
	// event and blocks until the user answers via ctrl.Approve. Sub-agents (the
	// task tool) keep their headless gate from setup — no UI to prompt through.
	ctrl.EnableInteractiveApproval()
	applyPermissionMode(ctrl, permissions)
	// YOLO: skip ordinary tool approval requests for the session (deny rules and
	// fresh reviews still apply; ask questions and plan approvals still wait).
	if *yolo {
		ctrl.SetAutoApproveTools(true)
	}

	m := newChatTUI(ctrl, missing, eventCh, termW)
	m.diagnostics = diagnostics
	m.updateWatchdogStatusProvider()
	m.planMode = permissions.plan
	m.leases = leases
	m.takeover = takeoverManager
	takeoverManager.AttachController(ctrl)
	if takeoverBinding != nil {
		takeoverManager.Activate(takeoverBinding)
	}
	if cfg != nil {
		m.outputStyle = cfg.Agent.OutputStyle    // shown as the active entry in /output-style
		m.statuslineCmd = cfg.Statusline.Command // custom status-line command, "" = built-in row
		m.showReasoning = cfg.UI.ShowReasoning   // /verbose persistence: start with config default
		m.showTurnUsage = cfg.UI.ShowTurnUsage   // retain usage accounting even when transcript receipts are hidden
		m.cfg = cfg
	}

	// /model support: a pure builder the TUI calls to rebuild on a different
	// model (carrying the conversation). It must NOT touch the running model —
	// runModelSubcommand performs the swap on the live copy. The same stable sink
	// feeds the new controller, so events keep flowing to this TUI.
	m.buildController = func(spec controllerBuildSpec, carry []provider.Message, resumePath string, oldCtrl control.SessionAPI) (*control.Controller, error) {
		effectiveOverrides := overrides
		if spec.EffortOverride != nil {
			effectiveOverrides.Effort = spec.EffortOverride
		}
		// Keep the logical-session private temporary directory across model /
		// profile switches (Issue #7575).
		effectiveOverrides.SessionTemp = sessionTempFromCLIController(oldCtrl)
		c, err := setupQuietProfile(ctx, spec.ModelRef, *maxSteps, false, sink, effectiveOverrides)
		if err != nil {
			return nil, err
		}
		if spec.EffortOverride != nil {
			overrides.Effort = spec.EffortOverride
		}
		// Keep the carried conversation in its existing file so the switch doesn't
		// orphan a duplicate (#2807).
		path := agent.ContinueSessionPath(resumePath, c.SessionDir(), c.Label())
		if err := adoptCarriedHistoryPreservingProfileAndGrants(c, carry, path, oldCtrl); err != nil {
			c.Close()
			return nil, err
		}
		c.EnableInteractiveApproval()
		c.SetPlanMode(spec.PlanMode)
		if spec.ToolApprovalMode != "" {
			c.SetToolApprovalMode(spec.ToolApprovalMode)
		}
		return c, nil
	}
	// /reload support: rebuild the runtime through boot.Rebuild so tools,
	// skills, commands, hooks, MCP servers, and providers are discovered fresh
	// while the boot layer migrates the session (history, approval grants,
	// goal/recovery state, lifecycle). Same construction inputs as
	// buildController so the replacement matches this session's launch wiring;
	// the CLI holds no SharedHost, so each rebuild owns its plugin host.
	m.bindRuntimeRebuilder(*maxSteps, sink, *yolo, overrides, cliProfileBuildOptions)
	if effortOverride != nil {
		m.effortLevel = *effortOverride
	}
	if effortOverride == nil {
		m.refreshEffortStatus()
	}

	if m.nativeScrollback {
		prepareNativeScrollback(os.Stdout, m.bottomRows())
	}

	// Non-Termux terminals use an alt-screen transcript viewport. Termux stays
	// in the normal buffer so native touch scrollback and soft-keyboard focus
	// keep working; finalized transcript lines are emitted via tea.Println.
	diagnostics.Milestone("terminal_takeover_begin")
	p := tea.NewProgram(m)
	takeoverManager.SetYieldCallback(func() { p.Send(tuiShutdownMsg{}) })
	diagnostics.StartWatchdog(p)
	// SSH drop (SIGHUP) or service stop (SIGTERM): persist the conversation
	// before the terminal goes away, then unwind through the normal close path
	// so resume picks up the interrupted session (#3772).
	hangup := make(chan os.Signal, 1)
	signal.Notify(hangup, syscall.SIGHUP, syscall.SIGTERM)
	go func() {
		for range hangup {
			p.Send(tuiShutdownMsg{})
		}
	}()
	final, runErr := p.Run()
	signal.Stop(hangup)
	diagnostics.Milestone("terminal_released")
	if err := takeoverManager.Close(); err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		if runErr == nil {
			runErr = err
		}
	}
	// Close the active controller plus any retired ones from /model switches.
	// Retired controllers were stashed rather than closed at switch time
	// because Controller.Close() runs SessionEnd hooks and kills plugin
	// subprocesses — operations that corrupt bubbletea's terminal raw mode
	// when executed while the TUI is alive.
	var launchWeb bool
	var launchWebPath, launchWebSessionID, launchWebModelRef string
	if fm, ok := final.(chatTUI); ok {
		reportShutdownFailure(fm.shutdownErr)
		launchWeb = fm.launchWebOnExit
		for _, oc := range fm.oldControllers {
			if c, ok := oc.(*control.Controller); ok {
				reporter.RecordRecovery(c.DrainRecoveryMetrics())
			}
			oc.Close()
		}
		if fm.ctrl != nil {
			launchWebPath = fm.launchWebResumePath
			launchWebSessionID = fm.launchWebSessionID
			launchWebModelRef = fm.launchWebModelRef
			if c, ok := fm.ctrl.(*control.Controller); ok {
				reporter.RecordRecovery(c.DrainRecoveryMetrics())
			}
			fm.ctrl.Close()
		} else {
			reporter.RecordRecovery(ctrl.DrainRecoveryMetrics())
			ctrl.Close()
		}
	} else {
		reporter.RecordRecovery(ctrl.DrainRecoveryMetrics())
		ctrl.Close()
	}
	if runErr != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, runErr)
		return 1
	}
	if launchWeb {
		// The Web runtime resumes a materialized TUI transcript or binds the exact
		// reserved identity for a never-used session. Release the TUI lease before
		// rebuilding the controller or the handoff would correctly reject its own
		// session as already in use. The deferred Release remains as a harmless
		// final guard for every other return path.
		leases.Release()
		return runWebCommand(webHandoffArgs(launchWebPath, launchWebSessionID, launchWebModelRef))
	}
	return 0
}

// adoptCarriedHistoryPreservingProfileAndGrants resumes c on the carried
// conversation the way buildController's callers expect: the freshly built
// c already has its own leading system message for the target profile (see
// boot/token_profile.go), but AdoptHistory below would otherwise replace the
// whole history — including that message — with carry's outgoing one, so the
// switch splices the new leading message in first. It also carries forward
// oldCtrl's same-session "Allow for this session" tool grants and Plan-mode
// read-only command trust, which a rebuild would otherwise silently drop,
// forcing the user to re-approve things already granted this session.
func adoptCarriedHistoryPreservingProfileAndGrants(c *control.Controller, carry []provider.Message, path string, oldCtrl control.SessionAPI) error {
	if fresh := c.History(); len(fresh) > 0 && fresh[0].Role == provider.RoleSystem {
		if len(carry) > 0 && carry[0].Role == provider.RoleSystem {
			carry[0] = fresh[0]
		} else {
			carry = append([]provider.Message{fresh[0]}, carry...)
		}
	}
	c.AdoptHistory(carry, path)
	if prev, ok := oldCtrl.(*control.Controller); ok {
		c.RestoreSessionAuthorizations(prev.SessionAuthorizations())
	}
	// Persist the adopted history now: the splice above only refreshed the new
	// controller's memory and nothing saves again until the next turn ends, so
	// quitting right after the switch and resuming would otherwise revive the
	// outgoing profile's contract from disk.
	if path != "" {
		if err := c.Snapshot(); err != nil {
			return fmt.Errorf("snapshot after runtime switch: %w", err)
		}
	}
	return nil
}

func prepareNativeScrollback(w io.Writer, rows int) {
	// Clear the terminal's scrollback history so a reopened chat starts
	// with a clean slate (Termux stays in the normal buffer, so prior
	// output would otherwise remain visible above the banner).
	fmt.Fprint(w, "\x1B[3J\x1B[2J\x1B[H")
	reserveNativeScrollbackFrame(w, rows)
}

func reserveNativeScrollbackFrame(w io.Writer, rows int) {
	for range rows {
		fmt.Fprintln(w)
	}
}

// setupTargets is where the wizard writes: the TOML config and the credential
// store. Keys always go to Reasonix's global .env so they
// never land in a project's own .env; only the config location is project-local
// under --local.
type setupTargets struct {
	config string
	env    string
}

// defaultConfigTarget is the user-global config file, falling back to a
// project-local reasonix.toml only when the user config dir can't be resolved.
func defaultConfigTarget() string {
	if p := config.UserConfigPath(); p != "" {
		return p
	}
	return "reasonix.toml"
}

// defaultEnvTarget is the display target for the reasonix-owned global
// Reasonix global .env.
func defaultEnvTarget() string {
	return config.CredentialsTargetDescription()
}

// resolveSetupTargets picks where `reasonix setup` writes. Keys always go to the
// global env. The config goes to the user-global dir by default, to ./reasonix.toml
// under --local, or to an explicit path argument when given.
func setupUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: reasonix setup [--local|-l] [path]")
	fmt.Fprintln(w, "Interactive configuration wizard. Writes a reasonix config and stores the")
	fmt.Fprintln(w, "provider API key in Reasonix's credential file, never in the config itself.")
	fmt.Fprintln(w, "  --local, -l   write ./reasonix.toml instead of the user-global config")
	fmt.Fprintln(w, "  path          write the config to this path instead of the default")
}

func resolveSetupTargets(args []string) setupTargets {
	t := setupTargets{config: defaultConfigTarget(), env: defaultEnvTarget()}
	for _, a := range args {
		switch a {
		case "--local", "-l":
			t.config = "reasonix.toml"
		default:
			t.config = a
		}
	}
	return t
}

// displayPath shortens a home-relative path to ~/… for readable wizard output.
func displayPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

// setupConfig runs the configuration wizard (the `reasonix setup` command),
// writing config.toml to the user-global dir (or ./reasonix.toml under --local)
// and API keys to Reasonix's global .env — never a project's own .env.
// Project memory is a separate concern — the in-session `/init` skill generates
// AGENTS.md (see initHint).
func setupConfig(args []string) int {
	if commandHelpRequested(args, len(args)) {
		setupUsage(os.Stdout)
		return 0
	}
	// resolveSetupTargets treats every unrecognized argument as the config path,
	// so a mistyped flag — or --help — silently becomes the file it writes.
	// Reject dashed arguments here instead.
	for _, a := range args {
		if a == "--local" || a == "-l" {
			continue
		}
		if strings.HasPrefix(a, "-") {
			fmt.Fprintf(os.Stderr, "unknown setup flag %q\n\n", a)
			setupUsage(os.Stderr)
			return 2
		}
	}
	t := resolveSetupTargets(args)
	path := t.config
	if _, err := os.Stat(path); err == nil {
		// Non-interactive must not clobber an existing config silently. On a TTY,
		// setup is a non-destructive configuration manager, so opening an existing
		// file no longer needs an overwrite confirmation.
		if !isInteractive() {
			fmt.Fprintf(os.Stderr, i18n.M.NotOverwritingFmt+"\n", path)
			return 1
		}
	}

	// Interactive wizard on a TTY; fall back to the annotated default when piped.
	if isInteractive() {
		rc := interactiveSetup(t.config, t.env)
		if rc == 0 {
			fmt.Printf(i18n.M.TryHintFmt+"\n", bold("reasonix"))
		}
		return rc
	}
	return writeDefaultConfig(t.config)
}

func confirmReconfigureExistingConfig(path string, in *bufio.Scanner, w io.Writer) bool {
	ans := ask(in, w, fmt.Sprintf(i18n.M.ConfirmReconfigureFmt, path), "y/N")
	return ans == "y" || ans == "Y"
}

func writeDefaultConfig(path string) int {
	unlock, err := config.LockConfigFileEdits(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.WriteConfigErr, err)
		return 1
	}
	defer unlock()
	if _, err := os.Lstat(path); err == nil {
		fmt.Fprintf(os.Stderr, i18n.M.NotOverwritingFmt+"\n", path)
		return 1
	} else if !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, i18n.M.WriteConfigErr, err)
		return 1
	}
	c := config.Default()
	if err := c.SaveTo(path); err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.WriteConfigErr, err)
		return 1
	}
	fmt.Printf(i18n.M.WroteFileFmt+"\n", displayPath(path))
	fmt.Println(i18n.M.NextHint)
	return 0
}

// initHint handles `reasonix init`. Unlike a config scaffold, project memory is
// model-generated by analyzing the codebase, so it lives as the in-session
// `/init` skill rather than a CLI command. This entry just points the user there
// (and to `reasonix setup` for config) so the verb isn't a dead end.
func initHint() int {
	fmt.Println(i18n.M.InitHint)
	return 0
}

// interactiveSetup opens the staged provider manager. Nothing is written until
// the user explicitly chooses Save and exit; q/Ctrl-C leaves both config and
// credentials untouched.
func interactiveSetup(configPath, envPath string) int {
	// Seed from the existing config when reconfiguring, so a re-run to fix a key
	// preserves the user's providers / agent settings instead of resetting to
	// defaults. First run (no file) falls back to the built-in defaults.
	cfg, err := config.LoadForEditReadOnlyStrict(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.WriteConfigErr, err)
		return 1
	}
	session := newProviderSetupSessionForPath(cfg, configPath)
	lang, err := selectLanguage()
	if err != nil {
		fmt.Fprintln(os.Stderr, "\nsetup cancelled.")
		return 1
	}
	session.setLanguage(lang)
	session.applyDeepSeekOfficialDefaultPricing()
	session.resetProviderSummaryBaseline()
	i18n.DetectLanguage(lang)

	// Now that the catalogue matches the user's choice, show the welcome banner
	// in their language before any substantive prompt.
	fmt.Println()
	fmt.Print(boxed([]string{
		accent("◆") + " " + fmt.Sprintf(i18n.M.WelcomeTitleFmt, bold("reasonix")),
		"",
		dim(i18n.M.NoConfigYet),
	}))
	fmt.Println()

	return runProviderSetupManager(session, configPath, envPath)
}

// pickSessionToResume scans the session dir, takes the 10 most recent, and
// shows a single-choice menu with timestamp + turn count + first user
// message so the user can pick one. Returns the chosen path and a process
// exit code (non-zero when there's nothing to pick or the user cancelled).
func pickSessionToResume() (string, int) {
	sessionDir := resolveCLISessionDir()
	reclaimCLIRecoveryBranches(sessionDir)
	sessions := recentSessions(sessionDir)
	if len(sessions) == 0 {
		fmt.Fprintln(os.Stderr, i18n.M.NoSessionToResume)
		return "", 1
	}
	if !isInteractive() {
		fmt.Fprintln(os.Stderr, i18n.M.ResumeRequiresTTY)
		return "", 1
	}
	items := make([]menuItem, len(sessions))
	for i, s := range sessions {
		when := s.ModTime.Local().Format("01-02 15:04")
		items[i] = menuItem{
			name: when,
			desc: sessionSummary(s),
		}
	}
	idx, err := selectOne(i18n.M.PickSessionLabel, items)
	if err != nil {
		return "", 1
	}
	return sessions[idx].Path, 0
}

// selectLanguage is the wizard's first prompt: it shows the two UI languages
// in their native form and pre-selects the env-detected one (so a single Enter
// confirms the auto-detection, a single arrow + Enter picks the other). The
// label is bilingual because we don't yet know which catalogue to trust.
func selectLanguage() (string, error) {
	detected := i18n.DetectLanguage("")
	items := []menuItem{{name: "English"}, {name: "中文 (简体)"}}
	tags := []string{"en", "zh"}
	if detected == "zh" {
		items[0], items[1] = items[1], items[0]
		tags[0], tags[1] = tags[1], tags[0]
	}
	idx, err := selectOne("Language · 语言", items)
	if err != nil {
		return "", err
	}
	return tags[idx], nil
}

// familyStaticModels unions the preset model lists of every entry in the family,
// preserving order and dropping duplicates. It is the fallback offered when the
// live /models probe fails, so a family with separate flash/pro preset entries
// still surfaces both rather than only the first member's model.
func familyStaticModels(providers []config.ProviderEntry, idxs []int) []string {
	var out []string
	seen := map[string]bool{}
	for _, i := range idxs {
		for _, m := range providers[i].ModelList() {
			if m != "" && !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out
}

// fetchOrFallback tries the OpenAI-compatible GET /models endpoint
// (honoring the entry's ModelsURL when set) and returns the live model IDs.
// On any failure — no base URL, no key set yet (the key is collected in a
// later wizard step), network/auth error, or a vendor without /models — it
// silently returns the preset's static model list so the wizard can always
// present something. The fetch has a 10s timeout and is best-effort.
func fetchOrFallback(probe *config.ProviderEntry, famName string, proxy netclient.ProxySpec) []string {
	static := probe.ModelList()
	if probe.BaseURL == "" {
		return static
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	models, err := probe.FetchModelsWithProxy(ctx, proxy)
	if err != nil || len(models) == 0 {
		if len(static) > 0 {
			fmt.Fprintf(os.Stderr, "  %s\n", dim(fmt.Sprintf(i18n.M.FetchModelsUsingPresetsFmt, famName)))
		}
		return static
	}
	fmt.Printf("  %s\n", green(fmt.Sprintf(i18n.M.FetchModelsSuccessFmt, len(models), famName)))
	return models
}

// fetchModelListCompat walks the full set of model-list URL candidates a given
// base URL can resolve to (root, /v1, known OpenAI/Anthropic compat suffixes)
// and returns the first successful fetch. This is the wizard-time probe for a
// *user-supplied* custom provider — its baseURL is whatever the user pasted,
// and "whatever they pasted" might be https://x.com (root, probe /v1/models)
// or https://x.com/v1 (versioned, probe /v1/models directly). Previously the
// wizard hardcoded `baseURL + "/models"`, which works for OpenAI-shape URLs
// but silently fails for Anthropic-shape roots and the reverse — so the
// wizard's idea of "what models exist" diverged from the chat client's actual
// endpoint. Returning the empty slice (not an error) on full miss lets the
// wizard fall through to a manual text input without an error message.
func fetchModelListCompat(ctx context.Context, baseURL, apiKey string, proxy netclient.ProxySpec) ([]string, error) {
	candidates, err := config.BuildModelFetchURLs(baseURL, "")
	if err != nil {
		return nil, err
	}
	var lastErr error
	var firstHardErr error
	for _, u := range candidates {
		models, err := openai.FetchModelsWithOptions(ctx, u, apiKey, openai.FetchModelsOptions{Proxy: proxy})
		if err == nil {
			return models, nil
		}
		lastErr = err
		if !openai.IsModelFetchEndpointMiss(err) && firstHardErr == nil {
			firstHardErr = err
		}
	}
	if firstHardErr != nil {
		return nil, firstHardErr
	}
	if lastErr != nil {
		slog.Debug("model-list probe: all candidates missed", "base_url", baseURL, "err", lastErr)
	}
	return nil, nil
}

// buildFamilyEntry returns a single ProviderEntry exposing the user's
// selected models under one entry. It preserves the preset's API key env,
// base URL, kind, context window, pricing, and effort — the things that
// vary per vendor but not per model. The Default pointer is reset to the
// first selected model if it would otherwise reference a model the user
// didn't pick (or was empty).
// buildFamilyEntries splits the user's selection back across the family's preset
// members so each model keeps its own entry — and therefore its own pricing,
// context window, and balance URL. A family like DeepSeek ships flash and pro as
// separate presets with different prices; collapsing them into one entry would
// bill pro at flash's rate. Models the live /models list returned that match no
// preset (a new SKU) fall under the probe entry. Member order is preserved;
// within a member, selection order is preserved.
func buildFamilyEntries(probe config.ProviderEntry, members []config.ProviderEntry, selected []string) []config.ProviderEntry {
	tmpl := map[string]config.ProviderEntry{probe.Name: probe}
	ownerName := map[string]string{}
	for _, m := range members {
		tmpl[m.Name] = m
		for _, id := range m.ModelList() {
			ownerName[id] = m.Name
		}
	}
	var order []string
	groups := map[string][]string{}
	for _, sm := range selected {
		name, ok := ownerName[sm]
		if !ok {
			name = probe.Name
		}
		if _, seen := groups[name]; !seen {
			order = append(order, name)
		}
		groups[name] = append(groups[name], sm)
	}
	out := make([]config.ProviderEntry, 0, len(order))
	for _, name := range order {
		out = append(out, buildFamilyEntry(tmpl[name], groups[name]))
	}
	return out
}

func buildFamilyEntry(probe config.ProviderEntry, selected []string) config.ProviderEntry {
	entry := probe
	entry.Models = selected
	entry.Model = selected[0]
	if entry.Default == "" || !containsString(selected, entry.Default) {
		entry.Default = selected[0]
	}
	return entry
}

func containsString(xs []string, v string) bool {
	return slices.Contains(xs, v)
}

// filterStaleCustomEntries drops the wizard's own magic-name entries
// (Name="custom" with Kind="openai" or Name="anthropic" with Kind="anthropic")
// that older versions of the wizard wrote into reasonix.toml. They collide
// with the wizard's "custom" / "anthropic" menu items on re-run, showing up
// as duplicate broken entries. The new wizard writes host-derived slugs
// (e.g. "custom-token-sensenova-cn") so a hit on the magic name is
// unambiguously stale. The returned slice is the dropped set so the caller
// can warn the user to clean up reasonix.toml by hand.
func filterStaleCustomEntries(providers []config.ProviderEntry) (kept, dropped []config.ProviderEntry) {
	for _, p := range providers {
		if p.Name == "custom" && p.Kind == "openai" {
			dropped = append(dropped, p)
			continue
		}
		if p.Name == "anthropic" && p.Kind == "anthropic" {
			dropped = append(dropped, p)
			continue
		}
		kept = append(kept, p)
	}
	return
}

// providerSlug derives a stable, human-readable entry name for a custom
// OpenAI / Anthropic-compatible provider from its base URL, e.g.
// "custom-token-sensenova-cn" or "anthropic-api-anthropic-com". We can't
// reuse the wizard's menu-item labels ("custom" / "anthropic") because
// those would collide with the menu item itself and end up rendered as
// duplicate provider entries on subsequent re-runs of `reasonix setup`.
// The host-based slug also gives users a meaningful name to grep for in
// reasonix.toml. Falls back to a short sha1 of the raw URL when the URL
// doesn't parse, so even malformed input still produces a unique name.
func providerSlug(kind, baseURL string) string {
	var host string
	if u, err := url.Parse(baseURL); err == nil {
		host = u.Host
	}
	if host == "" {
		sum := sha1.Sum([]byte(baseURL))
		return kind + "-" + hex.EncodeToString(sum[:4])
	}
	host = strings.ToLower(strings.TrimPrefix(host, "www."))
	var b strings.Builder
	prevDash := false
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	slug := strings.TrimRight(b.String(), "-")
	if slug == "" {
		sum := sha1.Sum([]byte(baseURL))
		return kind + "-" + hex.EncodeToString(sum[:4])
	}
	return kind + "-" + slug
}

func apiKeyEnvFromProviderName(name string) string {
	stem := strings.ToUpper(strings.TrimSpace(name))
	stem = strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, stem)
	stem = strings.Trim(stem, "_")
	if stem == "" {
		return "CUSTOM_" + fnv1a32Hex(name) + "_API_KEY"
	}
	if stem[0] >= '0' && stem[0] <= '9' {
		stem = "CUSTOM_" + stem
	}
	return stem + "_API_KEY"
}

type providerKeyEnvRepair struct {
	provider string
	old      string
	new      string
}

func repairInvalidProviderKeyEnvs(providers []config.ProviderEntry) ([]config.ProviderEntry, []providerKeyEnvRepair) {
	providers = append([]config.ProviderEntry(nil), providers...)
	var repairs []providerKeyEnvRepair
	for i := range providers {
		old := strings.TrimSpace(providers[i].APIKeyEnv)
		if old == "" || config.IsValidCredentialKey(old) {
			continue
		}
		keyEnv := apiKeyEnvFromProviderName(providers[i].Name)
		providers[i].APIKeyEnv = keyEnv
		repairs = append(repairs, providerKeyEnvRepair{provider: providers[i].Name, old: old, new: keyEnv})
	}
	return providers, repairs
}

func promptAPIKeyEnvName(in *bufio.Scanner, w io.Writer, label, def string) string {
	for {
		keyEnv := ask(in, w, label, def)
		if config.IsValidCredentialKey(keyEnv) {
			return keyEnv
		}
		fmt.Fprintf(w, i18n.M.InvalidAPIKeyEnvFmt+"\n", keyEnv)
	}
}

func fnv1a32Hex(s string) string {
	hash := uint32(0x811c9dc5)
	for _, unit := range utf16.Encode([]rune(strings.TrimSpace(s))) {
		hash ^= uint32(unit)
		hash *= 0x01000193
	}
	return fmt.Sprintf("%08x", hash)
}

// providerFamily is a wizard-only grouping of provider SKUs by vendor; it does
// not exist in config because users editing reasonix.toml deal with SKU names
// directly.
type providerFamily struct {
	key  string
	name string
	desc string
}

func familyOf(name string) providerFamily {
	switch {
	case strings.HasPrefix(name, "deepseek"):
		return providerFamily{key: "deepseek", name: "DeepSeek", desc: "fast & cheap, plus a stronger Pro SKU"}
	default:
		return providerFamily{key: name, name: name}
	}
}

type providerPromptResult struct {
	entries     []config.ProviderEntry
	credentials map[string]string
}

func newProviderPromptResult(entries []config.ProviderEntry, key, value string) providerPromptResult {
	result := providerPromptResult{entries: entries}
	if key != "" && value != "" {
		result.credentials = map[string]string{key: value}
	}
	return result
}

// promptCustomProvider handles the custom provider entry flow.
func promptCustomProvider(proxy netclient.ProxySpec) (providerPromptResult, error) {
	methodIdx, err := selectOne(i18n.M.CustomAddMethodLabel, []menuItem{
		{name: i18n.M.CustomMethodManual},
		{name: i18n.M.CustomMethodURL},
	})
	if err != nil {
		return providerPromptResult{}, err
	}
	if methodIdx == 0 {
		return promptCustomProviderManual()
	}
	return promptCustomProviderFromURL(proxy)
}

// promptCustomProviderManual handles manual model entry.
func promptCustomProviderManual() (providerPromptResult, error) {
	return promptCustomProviderManualWith(bufio.NewScanner(os.Stdin), "", "", "")
}

// promptCustomProviderManualWith is the shared backend for manual entry.
// Pre-filled values (baseURL, keyEnv, apiKey) are reused as-is when non-empty
// so the URL-fetch flow can fall through to manual entry without re-asking
// the user for information they've already typed. An empty apiKey is allowed
// — the key step happens later in the wizard and Reasonix's global .env is updated then.
func promptCustomProviderManualWith(in *bufio.Scanner, baseURL, keyEnv, apiKey string) (providerPromptResult, error) {
	fmt.Println()
	if baseURL == "" {
		baseURL = ask(in, os.Stdout, i18n.M.CustomPromptBaseURL, "")
		if baseURL == "" {
			return providerPromptResult{}, fmt.Errorf("base URL is required")
		}
	}
	providerName := providerSlug("custom", baseURL)
	modelName := ask(in, os.Stdout, i18n.M.CustomPromptModel, "")
	if modelName == "" {
		return providerPromptResult{}, fmt.Errorf("model name is required")
	}
	if keyEnv == "" {
		keyEnv = promptAPIKeyEnvName(in, os.Stdout, i18n.M.CustomPromptKeyEnv, apiKeyEnvFromProviderName(providerName))
	} else if !config.IsValidCredentialKey(keyEnv) {
		return providerPromptResult{}, fmt.Errorf("invalid API key variable name %q", keyEnv)
	}
	if apiKey == "" {
		apiKey = ask(in, os.Stdout, i18n.M.CustomPromptAPIKey, "")
	}
	entry := config.ProviderEntry{
		Name: providerName, Kind: "openai", BaseURL: baseURL,
		Model: modelName, APIKeyEnv: keyEnv, ContextWindow: askContextWindow(in, os.Stdout),
	}
	fmt.Printf("  %s\n", green(fmt.Sprintf(i18n.M.CustomAddedFmt, entry.Name+"/"+modelName)))
	return newProviderPromptResult([]config.ProviderEntry{entry}, keyEnv, apiKey), nil
}

// promptCustomProviderFromURL tries the OpenAI-compatible GET /models
// endpoint and shows a checkbox of the returned models. If the call fails
// (network error, auth failure, or a vendor without /models) it falls
// through to manual entry, reusing the URL and key the user already typed.
func promptCustomProviderFromURL(proxy netclient.ProxySpec) (providerPromptResult, error) {
	in := bufio.NewScanner(os.Stdin)
	fmt.Println()

	baseURL := ask(in, os.Stdout, i18n.M.CustomPromptBaseURL, "")
	if baseURL == "" {
		return providerPromptResult{}, fmt.Errorf("base URL is required")
	}
	providerName := providerSlug("custom", baseURL)
	keyEnv := promptAPIKeyEnvName(in, os.Stdout, i18n.M.CustomPromptKeyEnv, apiKeyEnvFromProviderName(providerName))
	apiKey := ask(in, os.Stdout, i18n.M.CustomPromptAPIKey, "")

	fmt.Printf("  %s\n", dim(fmt.Sprintf(i18n.M.FetchingModelsFmt, "custom")))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	models, err := fetchModelListCompat(ctx, baseURL, apiKey, proxy)
	if err != nil || len(models) == 0 {
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s\n", dim(fmt.Sprintf(i18n.M.FetchModelsFailedFmt, "custom", err)))
		} else {
			fmt.Fprintf(os.Stderr, "  %s\n", dim(i18n.M.CustomFetchEmpty))
		}
		return promptCustomProviderManualWith(in, baseURL, keyEnv, apiKey)
	}
	fmt.Printf("  %s\n", green(fmt.Sprintf(i18n.M.FetchModelsSuccessFmt, len(models), "custom")))

	items := make([]menuItem, len(models))
	for i, m := range models {
		items[i] = menuItem{name: m}
	}
	idxs, err := selectMany(fmt.Sprintf(i18n.M.SelectModelsLabel, "custom"), items)
	if err != nil || len(idxs) == 0 {
		return providerPromptResult{}, fmt.Errorf("no models selected")
	}
	var selected []string
	for _, i := range idxs {
		selected = append(selected, models[i])
	}
	entry := config.ProviderEntry{
		Name: providerName, Kind: "openai", BaseURL: baseURL,
		Models: selected, Model: selected[0], APIKeyEnv: keyEnv, ContextWindow: askContextWindow(in, os.Stdout),
	}
	fmt.Printf("  %s\n", green(fmt.Sprintf(i18n.M.CustomAddedFmt, entry.Name+"/"+selected[0])))
	return newProviderPromptResult([]config.ProviderEntry{entry}, keyEnv, apiKey), nil
}

// promptAnthropicProvider handles the Anthropic compatible provider entry flow.
func promptAnthropicProvider(proxy netclient.ProxySpec) (providerPromptResult, error) {
	methodIdx, err := selectOne(i18n.M.AnthropicAddMethodLabel, []menuItem{
		{name: i18n.M.AnthropicMethodManual},
		{name: i18n.M.AnthropicMethodURL},
	})
	if err != nil {
		return providerPromptResult{}, err
	}
	if methodIdx == 0 {
		return promptAnthropicProviderManual()
	}
	return promptAnthropicProviderFromURL(proxy)
}

// promptAnthropicProviderManual handles manual model entry.
func promptAnthropicProviderManual() (providerPromptResult, error) {
	return promptAnthropicProviderManualWith(bufio.NewScanner(os.Stdin), "", "", "")
}

// promptAnthropicProviderManualWith is the shared backend for manual entry
// of an Anthropic-compatible custom provider. Pre-filled values (baseURL,
// keyEnv, apiKey) are reused as-is when non-empty so the URL-fetch flow
// can fall through to manual entry without re-asking the user.
func promptAnthropicProviderManualWith(in *bufio.Scanner, baseURL, keyEnv, apiKey string) (providerPromptResult, error) {
	fmt.Println()
	if baseURL == "" {
		baseURL = ask(in, os.Stdout, i18n.M.AnthropicPromptBaseURL, "")
		if baseURL == "" {
			return providerPromptResult{}, fmt.Errorf("base URL is required")
		}
	}
	modelName := ask(in, os.Stdout, i18n.M.AnthropicPromptModel, "")
	if modelName == "" {
		return providerPromptResult{}, fmt.Errorf("model name is required")
	}
	if keyEnv == "" {
		keyEnv = promptAPIKeyEnvName(in, os.Stdout, i18n.M.AnthropicPromptKeyEnv, "ANTHROPIC_API_KEY")
	} else if !config.IsValidCredentialKey(keyEnv) {
		return providerPromptResult{}, fmt.Errorf("invalid API key variable name %q", keyEnv)
	}
	if apiKey == "" {
		apiKey = ask(in, os.Stdout, i18n.M.AnthropicPromptAPIKey, "")
	}
	entry := config.ProviderEntry{
		Name: providerSlug("anthropic", baseURL), Kind: "anthropic", BaseURL: baseURL,
		Model: modelName, APIKeyEnv: keyEnv, ContextWindow: askContextWindow(in, os.Stdout),
	}
	fmt.Printf("  %s\n", green(fmt.Sprintf(i18n.M.AnthropicAddedFmt, entry.Name+"/"+modelName)))
	return newProviderPromptResult([]config.ProviderEntry{entry}, keyEnv, apiKey), nil
}

// promptAnthropicProviderFromURL tries the OpenAI-compatible GET /models
// endpoint (some Anthropic-compatible proxies do expose one). Most don't
// — Anthropic's own API has no public model list — so on any failure the
// flow falls through to manual entry with the URL/key already filled in,
// rather than aborting the wizard.
func promptAnthropicProviderFromURL(proxy netclient.ProxySpec) (providerPromptResult, error) {
	in := bufio.NewScanner(os.Stdin)
	fmt.Println()

	baseURL := ask(in, os.Stdout, i18n.M.AnthropicPromptBaseURL, "")
	if baseURL == "" {
		return providerPromptResult{}, fmt.Errorf("base URL is required")
	}
	keyEnv := promptAPIKeyEnvName(in, os.Stdout, i18n.M.AnthropicPromptKeyEnv, "ANTHROPIC_API_KEY")
	apiKey := ask(in, os.Stdout, i18n.M.AnthropicPromptAPIKey, "")

	fmt.Printf("  %s\n", dim(fmt.Sprintf(i18n.M.AnthropicFetchingModelsFmt, "anthropic")))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	models, err := fetchModelListCompat(ctx, baseURL, apiKey, proxy)
	if err != nil || len(models) == 0 {
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s\n", dim(fmt.Sprintf(i18n.M.AnthropicFetchModelsFailedFmt, "anthropic", err)))
		} else {
			fmt.Fprintf(os.Stderr, "  %s\n", dim(i18n.M.AnthropicFetchEmpty))
		}
		return promptAnthropicProviderManualWith(in, baseURL, keyEnv, apiKey)
	}
	fmt.Printf("  %s\n", green(fmt.Sprintf(i18n.M.AnthropicFetchModelsSuccessFmt, len(models), "anthropic")))

	items := make([]menuItem, len(models))
	for i, m := range models {
		items[i] = menuItem{name: m}
	}
	idxs, err := selectMany(fmt.Sprintf(i18n.M.AnthropicSelectModelsLabel, "anthropic"), items)
	if err != nil || len(idxs) == 0 {
		return providerPromptResult{}, fmt.Errorf("no models selected")
	}
	var selected []string
	for _, i := range idxs {
		selected = append(selected, models[i])
	}
	entry := config.ProviderEntry{
		Name: providerSlug("anthropic", baseURL), Kind: "anthropic", BaseURL: baseURL,
		Models: selected, Model: selected[0], APIKeyEnv: keyEnv, ContextWindow: askContextWindow(in, os.Stdout),
	}
	fmt.Printf("  %s\n", green(fmt.Sprintf(i18n.M.AnthropicAddedFmt, entry.Name+"/"+selected[0])))
	return newProviderPromptResult([]config.ProviderEntry{entry}, keyEnv, apiKey), nil
}

func groupByFamily(providers []config.ProviderEntry) ([]string, map[string][]int, map[string]providerFamily) {
	var order []string
	members := map[string][]int{}
	info := map[string]providerFamily{}
	for i, p := range providers {
		f := familyOf(p.Name)
		if _, seen := members[f.key]; !seen {
			order = append(order, f.key)
			info[f.key] = f
		}
		members[f.key] = append(members[f.key], i)
	}
	return order, members, info
}

// withBuiltinFamilies guarantees the wizard always offers the built-in DeepSeek
// family even when the loaded config replaced the defaults.
// Built-in entries whose exact name already exists in the user's config are
// kept as-is (preserving customizations); missing built-in entries within an
// existing family are appended so the model picker always shows the full
// catalogue rather than only the previously selected subset.
func withBuiltinFamilies(providers []config.ProviderEntry) []config.ProviderEntry {
	return withBuiltinFamiliesForLanguage(providers, "")
}

func withBuiltinFamiliesForLanguage(providers []config.ProviderEntry, pricingLanguage string) []config.ProviderEntry {
	haveName := map[string]bool{}
	for _, p := range providers {
		haveName[p.Name] = true
	}
	defaults := config.Default()
	defaults.Language = pricingLanguage
	defaults.ApplyDeepSeekOfficialDefaultPricing()
	for _, bp := range defaults.Providers {
		if !haveName[bp.Name] {
			providers = append(providers, bp)
		}
	}
	return providers
}

// providersWithMissingKeys returns the providers the active configuration
// actually references (default/planner/subagent models) whose api_key_env is
// declared but not set. Merely-available providers stay silent; the chat banner
// still warns if users later switch to a model whose key is missing.
// configureKeys dedupes shared envs, so duplicates are fine to leave in.
func providersWithMissingKeys(cfg *config.Config) []config.ProviderEntry {
	if cfg == nil {
		return nil
	}
	refs := []string{
		cfg.DefaultModel,
		cfg.Agent.PlannerModel,
		cfg.Agent.SubagentModel,
	}
	if len(cfg.Agent.SubagentModels) > 0 {
		keys := make([]string, 0, len(cfg.Agent.SubagentModels))
		for key := range cfg.Agent.SubagentModels {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			refs = append(refs, cfg.Agent.SubagentModels[key])
		}
	}

	var out []config.ProviderEntry
	seen := map[string]bool{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		p, ok := cfg.ResolveModel(ref)
		if !ok || p.APIKeyEnv == "" || os.Getenv(p.APIKeyEnv) != "" || seen[p.APIKeyEnv] {
			continue
		}
		seen[p.APIKeyEnv] = true
		out = append(out, *p)
	}
	return out
}

// configureKeys reconciles each enabled provider's API key with the
// environment. For every distinct api_key_env: if the variable is already set,
// setup asks whether to re-enter it; Enter keeps and re-pins the existing value.
// Otherwise the user is asked once per env var (deduped across providers that
// share one, e.g. both DeepSeek models). Returns KEY=value lines for the
// Reasonix global .env. Re-pinning keeps hand-edited or previously saved values
// aligned with the user's latest setup choice.
func configureKeys(selected []config.ProviderEntry, r io.Reader, w io.Writer) []string {
	in := bufio.NewScanner(r)
	fmt.Fprintln(w, "\n"+i18n.M.EnterAPIKeysHeader)

	seen := map[string]bool{}
	var envLines []string
	for _, p := range selected {
		if p.APIKeyEnv == "" || seen[p.APIKeyEnv] {
			continue
		}
		seen[p.APIKeyEnv] = true

		if cur := os.Getenv(p.APIKeyEnv); cur != "" {
			reset := ask(in, w, "  "+fmt.Sprintf(i18n.M.APIKeyResetPromptFmt, p.APIKeyEnv), "y/N")
			if reset == "y" || reset == "Y" {
				if key := ask(in, w, "  "+p.APIKeyEnv, ""); key != "" {
					envLines = append(envLines, p.APIKeyEnv+"="+key)
					continue
				}
			}
			fmt.Fprintf(w, "  %s %s\n", green("✓"), fmt.Sprintf(i18n.M.APIKeyAlreadySetFmt, p.APIKeyEnv))
			envLines = append(envLines, p.APIKeyEnv+"="+cur)
			continue
		}

		if key := ask(in, w, "  "+p.APIKeyEnv, ""); key != "" {
			envLines = append(envLines, p.APIKeyEnv+"="+key)
		}
	}
	return envLines
}

// ask prints a prompt to w and returns the entered line, or def if input is empty.
func ask(in *bufio.Scanner, w io.Writer, label, def string) string {
	if def != "" {
		fmt.Fprintf(w, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(w, "%s: ", label)
	}
	if !in.Scan() {
		return def
	}
	if v := strings.TrimSpace(in.Text()); v != "" {
		return v
	}
	return def
}

// isInteractive reports whether we're attached to a real terminal on both
// stdin and stdout — required for prompting. Redirected or piped I/O is not
// interactive, so wizards never block or auto-default in scripts and CI.
func isInteractive() bool {
	return isTTY(os.Stdin) && isTTY(os.Stdout)
}

func isTTY(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// appendEnv merges KEY=value lines into a .env file. Existing assignments of
// any key that's about to be written are dropped first, then the new values
// are appended — so re-running `reasonix setup` with a corrected key replaces the
// stale one instead of stacking duplicates. The new values are also
// pinned into the current process env so a chat session started right after
// init picks up the fresh keys without a restart.
func appendEnv(path string, lines []string) error {
	target := map[string]bool{}
	for _, l := range lines {
		if k, _, ok := strings.Cut(l, "="); ok {
			target[strings.TrimSpace(k)] = true
		}
	}

	var kept []string
	if data, err := fileencoding.ReadFileUTF8(path); err == nil {
		for raw := range strings.SplitSeq(string(data), "\n") {
			trimmed := strings.TrimSpace(raw)
			check := strings.TrimPrefix(trimmed, "export ")
			if k, _, ok := strings.Cut(check, "="); ok && target[strings.TrimSpace(k)] {
				continue
			}
			kept = append(kept, raw)
		}
		// strings.Split on a string ending with \n leaves a trailing empty
		// element; trim it so we don't grow a blank line on every rewrite.
		if n := len(kept); n > 0 && kept[n-1] == "" {
			kept = kept[:n-1]
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	var b strings.Builder
	for _, l := range kept {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
		if k, v, ok := strings.Cut(l, "="); ok {
			os.Setenv(strings.TrimSpace(k), v)
		}
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// readStdin reads piped input if present; an interactive terminal yields "".
func readStdin() string {
	stat, err := os.Stdin.Stat()
	if err != nil || stat.Mode()&os.ModeCharDevice != 0 {
		return ""
	}
	data, _ := io.ReadAll(os.Stdin)
	return strings.TrimSpace(string(data))
}

func usage() {
	fmt.Print(i18n.M.UsageBody)
}

type ctrlKillerAdapter struct{ ctrl *control.Controller }

func (a ctrlKillerAdapter) Kill(sessionID, id string) bool {
	if sessionID != "" && agent.BranchID(a.ctrl.SessionPath()) != sessionID {
		return false
	}
	return a.ctrl.CancelJob(id)
}

func configCommand(args []string) int {
	if len(args) == 0 {
		configUsage()
		return 2
	}
	switch args[0] {
	case "auto-plan":
		return configAutoPlanCompatibilityCommand(args[1:])
	case "reasoning-language":
		return configReasoningLanguageCommand(args[1:])
	case "compact-ratio":
		return configCompactRatioCommand(args[1:])
	case "currency":
		return configCurrencyCommand(args[1:])
	case "telemetry":
		return configTelemetryCommand(args[1:])
	default:
		configUsage()
		return 2
	}
}

func configCurrencyCommand(args []string) int {
	fs := flag.NewFlagSet("config currency", flag.ContinueOnError)
	local := fs.Bool("local", false, "unsupported; pricing currency is user-level only")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if *local {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, "currency is user-level only; --local is not supported")
		return 2
	}
	rest := fs.Args()
	if len(rest) > 1 {
		configCurrencyUsage()
		return 2
	}
	if len(rest) == 0 {
		cfg, err := config.LoadForRootReadOnly(".")
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			return 1
		}
		fmt.Printf("currency = %q (display: %s)\n", pricingCurrencyDisplay(cfg.DisplayCurrencyPref()), cfg.ResolveDisplayCurrency())
		return 0
	}
	mode, err := parseCLIPricingCurrency(rest[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	path := config.UserConfigPath()
	if path == "" {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, "cannot resolve user config path")
		return 1
	}
	unlock := config.LockUserConfigEdits()
	defer unlock()
	cfg := config.LoadForEdit(path)
	if err := cfg.SetDisplayCurrency(mode); err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	resolved := cfg.ResolveDisplayCurrency()
	if err := cfg.SaveTo(path); err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	fmt.Printf("currency = %q (display: %s, %s)\n", pricingCurrencyDisplay(mode), resolved, displayPath(path))
	return 0
}

var (
	cleanupCLITelemetry        = telemetry.Cleanup
	startCLITelemetryReporter  = telemetry.Start
	persistCLITelemetryConsent = func(mode string) error {
		path := config.UserConfigPath()
		if strings.TrimSpace(path) == "" {
			return errors.New("cannot resolve config path")
		}
		unlock := config.LockUserConfigEdits()
		defer unlock()
		cfg, err := config.LoadForEditReadOnlyStrict(path)
		if err != nil {
			return err
		}
		if err := cfg.SetCLITelemetryMode(mode); err != nil {
			return err
		}
		return cfg.SaveTo(path)
	}
)

func configTelemetryCommand(args []string) int {
	fs := flag.NewFlagSet("config telemetry", flag.ContinueOnError)
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	rest := fs.Args()
	if len(rest) > 1 {
		configTelemetryUsage()
		return 2
	}
	if len(rest) == 0 {
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			return 1
		}
		fmt.Printf("cli_metrics = %q\n", cfg.CLITelemetryMode())
		return 0
	}
	path := config.UserConfigPath()
	if path == "" {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, "cannot resolve config path")
		return 1
	}
	unlock := config.LockUserConfigEdits()
	defer unlock()
	cfg := config.LoadForEdit(path)
	if err := cfg.SetCLITelemetryMode(rest[0]); err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	if err := cfg.SaveTo(path); err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	if cfg.CLITelemetryMode() == "off" {
		if err := cleanupCLITelemetry(config.ReasonixHomeDir()); err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, "telemetry disabled, but pending metrics could not be deleted:", err)
			return 1
		}
	}
	fmt.Printf("cli_metrics = %q (%s)\n", cfg.CLITelemetryMode(), displayPath(path))
	return 0
}

// configAutoPlanCompatibilityCommand preserves the released shell interface
// without restoring Automatic Plan Mode. Reading and writing "off" are safe
// no-ops; every attempt to enable the retired feature is rejected.
func configAutoPlanCompatibilityCommand(args []string) int {
	fs := flag.NewFlagSet("config auto-plan", flag.ContinueOnError)
	local := fs.Bool("local", false, "unsupported; automatic plan mode is retired")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	if *local {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, "auto-plan is user-level only; --local is not supported")
		return 2
	}
	rest := fs.Args()
	if len(rest) > 1 {
		configAutoPlanCompatibilityUsage()
		return 2
	}
	if len(rest) == 0 {
		fmt.Println(`auto_plan = "off"`)
		return 0
	}
	cfg := config.Default()
	if err := cfg.SetAutoPlan(rest[0]); err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	fmt.Println(`auto_plan = "off"`)
	return 0
}

func configReasoningLanguageCommand(args []string) int {
	fs := flag.NewFlagSet("config reasoning-language", flag.ContinueOnError)
	local := fs.Bool("local", false, "write ./reasonix.toml instead of the user config")
	if code, ok := parseCommandFlags(fs, args); !ok {
		return code
	}
	rest := fs.Args()
	if len(rest) > 1 {
		configReasoningLanguageUsage()
		return 2
	}
	if len(rest) == 0 {
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			return 1
		}
		fmt.Printf("reasoning_language = %q\n", cliReasoningLanguageMode(cfg.ReasoningLanguage()))
		return 0
	}
	mode, err := parseCLIReasoningLanguage(rest[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	path := config.UserConfigPath()
	if *local {
		path = "reasonix.toml"
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, "cannot resolve config path")
		return 1
	}
	unlock, err := config.LockConfigFileEdits(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	defer unlock()
	if *local {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			lang, err := config.SaveMinimalProjectReasoningLanguage(path, mode)
			if err != nil {
				fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
				return 1
			}
			fmt.Printf("reasoning_language = %q (%s)\n", lang, displayPath(path))
			return 0
		} else if err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			return 1
		}
	}
	cfg, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	if err := cfg.SetReasoningLanguage(mode); err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	if err := cfg.SaveTo(path); err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	fmt.Printf("reasoning_language = %q (%s)\n", cfg.ReasoningLanguage(), displayPath(path))
	return 0
}

func configCompactRatioCommand(args []string) int {
	fs := flag.NewFlagSet("config compact-ratio", flag.ContinueOnError)
	local := fs.Bool("local", false, "write ./reasonix.toml instead of the user config")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) > 1 {
		configCompactRatioUsage()
		return 2
	}
	if len(rest) == 0 {
		cfg, err := config.LoadForRootReadOnly(".")
		if err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			return 1
		}
		fmt.Printf("compact_ratio = %s (%s)\n", formatCompactRatioPercent(cfg.Agent.CompactRatio), compactRatioSource())
		return 0
	}
	percent, err := strconv.ParseFloat(strings.TrimSpace(rest[0]), 64)
	minPercent := config.CompactRatioMin * 100
	maxPercent := config.CompactRatioMax * 100
	if err != nil || math.IsNaN(percent) || math.IsInf(percent, 0) || percent < minPercent || percent > maxPercent {
		fmt.Fprintf(os.Stderr, "%s compact ratio must be a percentage between %.0f and %.0f\n", i18n.M.ErrorPrefix, minPercent, maxPercent)
		return 2
	}
	ratio := percent / 100
	path := config.UserConfigPath()
	scope := "user"
	if *local {
		path = "reasonix.toml"
		scope = "project"
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, "cannot resolve config path")
		return 1
	}
	unlock, err := config.LockConfigFileEdits(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	defer unlock()
	if *local {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			saved, err := config.SaveMinimalProjectCompactRatio(path, ratio)
			if err != nil {
				fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
				return 1
			}
			fmt.Printf("compact_ratio = %s (%s: %s)\n", formatCompactRatioPercent(saved), scope, displayPath(path))
			return 0
		} else if err != nil {
			fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
			return 1
		}
	}
	cfg, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	if err := cfg.SetCompactRatio(ratio); err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 2
	}
	if err := cfg.SaveTo(path); err != nil {
		fmt.Fprintln(os.Stderr, i18n.M.ErrorPrefix, err)
		return 1
	}
	fmt.Printf("compact_ratio = %s (%s: %s)\n", formatCompactRatioPercent(cfg.Agent.CompactRatio), scope, displayPath(path))
	return 0
}

func compactRatioSource() string {
	if config.ConfigFileDefinesCompactRatio("reasonix.toml") {
		return "project: " + displayPath("reasonix.toml")
	}
	if path := config.UserConfigPath(); path != "" && config.ConfigFileDefinesCompactRatio(path) {
		return "user: " + displayPath(path)
	}
	return "built-in default"
}

func formatCompactRatioPercent(ratio float64) string {
	value := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", ratio*100), "0"), ".")
	return value + "%"
}

func configUsage() {
	fmt.Print(`Usage:
  reasonix config reasoning-language [--local] [auto|zh|en]
  reasonix config compact-ratio [--local] [30..85]
  reasonix config currency [auto|CNY|USD]
  reasonix config telemetry [auto|on|off]
`)
}

func configTelemetryUsage() {
	fmt.Print(`Usage:
  reasonix config telemetry [auto|on|off]
`)
}

func configCompactRatioUsage() {
	fmt.Print(`Usage:
  reasonix config compact-ratio [--local] [30..85]
`)
}

func startCLITelemetry(cfg *config.Config, opts telemetry.Options) *telemetry.Reporter {
	return startCLITelemetryWithIO(cfg, opts, os.Stdin, os.Stdout, os.Stderr)
}

func startCLITelemetryWithIO(cfg *config.Config, opts telemetry.Options, in io.Reader, out, errOut io.Writer) *telemetry.Reporter {
	if cfg == nil {
		cfg = config.Default()
	}
	opts.Mode = cfg.CLITelemetryMode()
	opts.HomeDir = config.ReasonixHomeDir()
	opts.Proxy = cfg.NetworkProxySpec()
	opts.Language = cfg.Language

	if cfg.CLITelemetryConfigured() || !telemetry.Enabled(opts.Mode, opts.Version, opts.Interactive) {
		return startCLITelemetryReporter(opts)
	}

	fmt.Fprintln(out, i18n.M.CLITelemetryConsentNotice)
	scanner := bufio.NewScanner(in)
	mode := ""
	for mode == "" {
		answer := strings.ToLower(strings.TrimSpace(ask(scanner, out, i18n.M.CLITelemetryConsentPrompt, "Y/n")))
		switch answer {
		case "y", "yes", "y/n":
			mode = "auto"
		case "n", "no":
			mode = "off"
		default:
			fmt.Fprintln(out, i18n.M.CLITelemetryConsentInvalid)
		}
	}

	if err := persistCLITelemetryConsent(mode); err != nil {
		fmt.Fprintf(errOut, i18n.M.CLITelemetryConsentSaveFailedFmt+"\n", err)
		return nil
	}
	cfg.Telemetry.CLIMetrics = mode
	opts.Mode = mode
	if mode == "off" {
		if err := cleanupCLITelemetry(opts.HomeDir); err != nil {
			fmt.Fprintf(errOut, i18n.M.CLITelemetryConsentCleanupFailedFmt+"\n", err)
		}
		return nil
	}
	return startCLITelemetryReporter(opts)
}

func cliTelemetrySessionMode(cont, resume, copySession bool) string {
	switch {
	case copySession:
		return "copy"
	case resume:
		return "resume"
	case cont:
		return "continue"
	default:
		return "fresh"
	}
}

func configAutoPlanCompatibilityUsage() {
	fmt.Print(`Usage:
  reasonix config auto-plan [off]
`)
}

func configReasoningLanguageUsage() {
	fmt.Print(`Usage:
  reasonix config reasoning-language [--local] [auto|zh|en]
`)
}

func configCurrencyUsage() {
	fmt.Print(`Usage:
  reasonix config currency [auto|CNY|USD]
`)
}
