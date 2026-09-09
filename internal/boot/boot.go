// Package boot assembles a ready-to-drive control.Controller from configuration:
// it loads config, resolves the model(s), builds the tool registry (built-ins +
// plugins), wires the permission gate, and constructs the executor — optionally
// wrapping it in a two-model Coordinator. It is the one place that turns "what the
// user configured" into "a Controller a frontend can drive", so every frontend —
// the terminal TUI, the HTTP/SSE server, the desktop webview — shares the exact
// same assembly instead of each re-deriving it. Frontends pass only a sink and a
// couple of run knobs; everything else comes from config.
package boot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"reasonix/internal/ablation"
	"reasonix/internal/agent"
	"reasonix/internal/agentpreset"
	"reasonix/internal/billing"
	"reasonix/internal/capability"
	"reasonix/internal/command"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/environment"
	"reasonix/internal/event"
	"reasonix/internal/extension"
	"reasonix/internal/extension/dispatch"
	"reasonix/internal/extension/protocol"
	"reasonix/internal/extension/providerext"
	"reasonix/internal/extension/sidecar"
	"reasonix/internal/extension/uihub"
	"reasonix/internal/guardian"
	"reasonix/internal/history"
	"reasonix/internal/hook"
	"reasonix/internal/imageinput"
	"reasonix/internal/installsource"
	"reasonix/internal/instruction"
	"reasonix/internal/jobs"
	"reasonix/internal/lsp"
	"reasonix/internal/mcplaunch"
	"reasonix/internal/memory"
	"reasonix/internal/migration"
	"reasonix/internal/netclient"
	"reasonix/internal/outputstyle"
	"reasonix/internal/permission"
	"reasonix/internal/plugin"
	"reasonix/internal/productdocs"
	"reasonix/internal/provider"
	"reasonix/internal/recovery"
	"reasonix/internal/sandbox"
	"reasonix/internal/secrets"
	"reasonix/internal/sessioncontext"
	"reasonix/internal/sessiontemp"
	"reasonix/internal/skill"
	"reasonix/internal/stats"
	"reasonix/internal/taskmonitor"
	"reasonix/internal/tool"
	"reasonix/internal/tool/builtin"
	"reasonix/internal/tool/sessiontool"
	"reasonix/internal/workspacelease"
)

// ErrUnknownModel is returned by Build when the configured model can't be
// resolved to a provider — e.g. a default_model left over from a renamed or
// removed provider. Callers can detect it (errors.Is) to re-run setup.
var ErrUnknownModel = errors.New("unknown model")

func agentKeepPolicy(keep []string) agent.KeepPolicy {
	if keep == nil {
		return agent.KeepErrors | agent.KeepUserMarked
	}
	var p agent.KeepPolicy
	for _, k := range keep {
		switch strings.TrimSpace(k) {
		case "errors":
			p |= agent.KeepErrors
		case "user_marked":
			p |= agent.KeepUserMarked
		}
	}
	return p
}

// Options carries the per-run knobs a frontend chooses; everything else is
// read from configuration. Model "" falls back to default_model; MaxSteps 0
// uses automatic execution; RequireKey fails fast on a missing key.
type Options struct {
	// ModelSettings supplies an immutable desktop credential-proxy resolver.
	// The bundle contains virtual tunnel credentials only and stays in memory.
	ModelSettings *config.ModelRuntimeSettings
	Model         string
	MaxSteps      int
	MaxStepsKey   string
	RequireKey    bool
	Sink          event.Sink
	// EffortOverride is a session-local reasoning effort override. Nil means use
	// the resolved provider config; a non-nil empty string means provider default.
	EffortOverride *string
	// PermissionAllow adds process-local allow rules (for example CLI
	// --allowed-tools). They override configured ask rules but never deny rules
	// and are not persisted.
	PermissionAllow []string
	// AdditionalDirs grants this session's file writers and sandboxed shell
	// access to extra directories without changing persisted sandbox config.
	AdditionalDirs []string
	// Stderr is the writer for diagnostic warnings and plugin subprocess
	// stderr output. When nil, defaults to os.Stderr. Interactive terminal
	// frontends must provide a private diagnostic writer (or io.Discard) so
	// background output cannot corrupt the TUI's terminal raw mode.
	Stderr io.Writer
	// WorkspaceRoot is the project root directory for config, skills, memory,
	// commands, hooks, and tool confinement. When empty, the current working
	// directory is used (CLI default). Desktop tabs pass their project root here
	// so each tab loads its own config/skills/hooks without changing the process
	// cwd — enabling concurrent multi-project sessions.
	WorkspaceRoot string
	// StatsSource labels this frontend's usage records (desktop/cli/serve).
	// Empty disables usage recording for this controller.
	StatsSource string
	// FileBranchesOnly keeps fork/branch/switch/rewind on separate session
	// files instead of heads inside a schema-2 log.
	FileBranchesOnly bool
	TaskStore        taskmonitor.WriteStore // Authoritative store, never a SQLite catalog.
	// OnConfigLoadWarnings accepts resilient-loader warnings. Returning true
	// lets boot suppress the duplicate migration diagnostic.
	OnConfigLoadWarnings func([]string) bool
	// ExtraPlugins are session-scoped MCP servers supplied by a host transport
	// (for example ACP session/new). They are connected eagerly for this
	// controller but are not persisted to reasonix.toml.
	ExtraPlugins []plugin.Spec
	// AgentPreset and TokenMode seed the session quality floor. Delivery (or
	// its aliases) raises it to delivery; light and its aliases fold to
	// standard; unknown values keep the standard default.
	AgentPreset string
	TokenMode   string
	// SessionDir overrides where persisted chat transcripts are written. When
	// empty, the shared CLI/global session directory is used.
	SessionDir string
	// SharedHost is an optional plugin.Host shared across controllers for the
	// same workspace root. When set, boot.Build reuses its running clients
	// instead of creating new subprocesses, and the caller manages the host's
	// lifecycle. When nil, Build creates and owns a new host as before.
	SharedHost *plugin.Host
	// MCPHostProfile is the capability surface for hosts Build creates;
	// ignored when SharedHost is set (it fixed its own profile).
	MCPHostProfile plugin.HostProfile
	// CleanupPendingReconciler retries delayed physical cleanup for session
	// artifacts left by a previous process. Nil uses the core physical-delete
	// reconciler; frontends with different deletion semantics can override it.
	CleanupPendingReconciler func(sessionDir string) error
	// ApprovalTimeout bounds how long a tool-approval or ask prompt blocks for a
	// user decision. Zero (default) waits forever — correct for an interactive
	// terminal. Headless/bot frontends pass a positive value so an unanswered
	// prompt can't wedge the session indefinitely (#4626, #4402).
	ApprovalTimeout time.Duration
	// HeadlessApprovalMode selects the non-interactive tool-approval contract
	// (control.ToolApprovalAuto/DontAsk/Yolo) applied to every headless-only gate
	// this boot constructs: the top-level executor, task/read_only_task,
	// writer-capable skill sub-agents, and the planner runner. Empty (or "ask")
	// keeps the default fail-closed headless gate. Callers that later call
	// Controller.ApplyHeadlessApprovalMode with a
	// different mode than they passed here should also pass it here, or
	// sub-agent gates will not match the parent executor's mode.
	HeadlessApprovalMode string
	// Session recovery and transition hooks let frontends keep local ownership metadata aligned.
	SessionRecoveryMeta func(control.SessionRecoveryRequest) agent.BranchMeta
	OnSessionRecovered  func(control.SessionRecoveryInfo) error
	OnSessionTransition func(control.SessionTransitionInfo) error
	BeforeInboxDispatch func(*control.Controller) (func(), error)
	// OnSessionTitleChanged lets a host project the canonical BranchMeta title
	// into compatibility indexes and refresh notifications after the current
	// conversation renames itself through set_session_title.
	OnSessionTitleChanged sessiontool.TitleChangedFunc
	// SubagentParentLive reports whether this process currently owns or is
	// building the parent session. Desktop uses it to avoid probing a live tab's
	// lease during stale-subagent cleanup. Nil preserves lease-only cleanup.
	SubagentParentLive func(sessionPath string) bool
	// FileOverlay and TerminalRunner let a host transport (ACP) serve file
	// content from editor buffers and run foreground bash in a host terminal.
	// Both only change where tool I/O happens — tool names, descriptions, and
	// schemas stay byte-identical, so the provider-visible surface is unchanged.
	FileOverlay    builtin.FileOverlay
	TerminalRunner builtin.TerminalRunner
	// ProviderResolver routes every model role through a caller-owned provider
	// catalog. Nil preserves local behavior.
	ProviderResolver provider.Resolver
	// Ablation switches subsystems off for a benchmark arm, and is also the
	// process-local hard override supervised ACP workers use to force the planner
	// off. It wins over user/project configuration without mutating config or
	// changing the provider-visible prompt/tool surface. The zero value runs
	// everything.
	Ablation ablation.Set
	// SandboxNetworkOverride and WorkspaceOnly are process-local hard bounds for
	// supervised ACP workers. Nil/false preserve normal Reasonix config.
	SandboxNetworkOverride *bool
	SandboxBashOverride    string
	WorkspaceOnly          bool
	PinnedContextLoader    control.PinnedContextLoader
	SessionTemp            *sessiontemp.Manager // session-private temp manager; Rebuild reuses old's
	RuntimeReload
	// deferPublish keeps a replacement generation private until migration and
	// commit succeed. Cold BuildRuntime leaves this false and publishes at boot.
	deferPublish bool
}

func recoveryHeadlessMode(opts Options) bool {
	return strings.TrimSpace(opts.HeadlessApprovalMode) != ""
}

// build is the assembly body behind BuildRuntime (and the Build compat
// wrapper): it loads config, resolves the model(s), wires the full runtime,
// and freezes the extension kernel snapshot from the objects it just
// assembled. The returned controller owns plugin subprocesses; call Close
// (via Controller.Close) to release them.
func build(ctx context.Context, opts Options) (*BuildResult, error) {
	ctx, opts, owner, fileWriteReceipt := bindRuntimeOwner(ctx, opts)
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	root := resolveWorkspaceRoot(opts.WorkspaceRoot)
	additionalDirs, err := normalizeAdditionalDirs(root, opts.AdditionalDirs)
	if err != nil {
		return nil, err
	}
	// Import v1/v0.5 config before Load so this boot sees the new config + ~/.env.
	// CLI Run also calls this before config-only commands; keep a shared fallback.
	migrated, migErr := config.MigrateLegacyIfNeededForRoot(root)
	deepSeekProtocolMigrated, deepSeekProtocolMigErr := config.ApplyUserConfigUpgradesOnStartup(config.UserConfigPath())
	stepLimitsMigrated, stepLimitMigErr := config.MigrateLegacyAgentStepLimitsForRoot(root)
	redactToolOutputMigrated, redactToolOutputMigErr := config.MigrateLegacyRedactToolOutputForRoot(root)
	memoryCompilerMigrated, memoryCompilerMigErr := config.MigrateLegacyMemoryCompilerForRoot(root)
	multiThresholdMigrated, multiThresholdMigErr := config.MigrateLegacyMultiThresholdCompactionForRoot(root)
	config.MigrateLegacyMCPTiersForRoot(root)
	cfg, err := config.LoadModelRuntimeSnapshot(root, opts.Model)
	if err != nil {
		return nil, err
	}
	if err := opts.ModelSettings.Apply(cfg, root); err != nil {
		return nil, err
	}
	deepSeekProtocolMigErr = deepSeekProtocolMigrationNoticeError(handleConfigLoadWarnings(opts, cfg), deepSeekProtocolMigErr)
	// Arm the credential-protection layers from the user-global [secrets]
	// section before any tool, hook, or plugin subprocess can spawn. Package
	// globals are correct here because [secrets] is user-global (project
	// reasonix.toml cannot override it), so concurrent workspaces agree.
	secrets.SetFilterSubprocessEnv(cfg.Secrets.FilterSubprocessEnv)
	secrets.SetProtectSensitiveFiles(cfg.Secrets.ProtectSensitiveFiles)
	secrets.RegisterCredentialEnvKeys(cfg.CredentialEnvNames())

	// Serialize the frontend's sink once: background jobs (below) emit from their
	// own goroutines, which can overlap a running turn's emission, so every emitter
	// shares this synchronized sink. It is created before extension preflight so
	// sidecar warnings and host/ui/* publishes land on the same channel as every
	// later notice. The job manager is session-scoped — its jobs outlive a turn
	// and are cancelled by Controller.Close.
	//
	// CostQuote must run before every host consumer (stats recorder, CLI
	// metrics via opts.Sink, ACP/eventwire bridges, Desktop) so all see the
	// same occurrence-time quote. Order from the agent:
	//   GoalUsageTee → Sync → CostQuote → [Recorder] → frontend
	// The controller coalesces before its ledger so every consumer shares boundaries.
	quoteCtx := &event.QuoteContext{
		DisplayRequest: billing.DisplayRequest{
			Currency: cfg.ExplicitDisplayCurrency(),
			Source:   billing.DisplaySourceExplicit,
		},
		BillingModeForModel: func(modelRef string) string {
			entry, ok := cfg.ResolveModel(modelRef)
			if !ok {
				return ""
			}
			return entry.ProviderBillingMode()
		},
		PricingContextForModel: func(modelRef string) billing.PricingContext {
			entry, ok := cfg.ResolveModel(modelRef)
			if !ok {
				return billing.PricingContext{}
			}
			return entry.PricingContextForModel(entry.Model)
		},
	}
	// Innermost: frontend sink (CLI metrics/ACP/Desktop bridge live here).
	quoted := opts.Sink
	// Record billable usage after quoting so history JSONL can store CostQuote.
	if source := strings.TrimSpace(opts.StatsSource); source != "" {
		quoted = stats.NewRecorder(quoted, config.StatsDir(), source)
	}
	quoted = event.NewCostQuoteSink(quoted, quoteCtx)
	sink := event.Sync(quoted)

	// Both sink wraps must complete BEFORE the extension UI hub closes over the
	// sink variable: a sidecar publish during preflight lands on this closure
	// from a wire-handler goroutine, and any later reassignment races it.
	// Goal token-budget accounting: the controller detects this tee and
	// attributes billable usage to the active goal turn's recorder. Both the
	// tee must ride the shared sink agents emit into directly.
	sink = control.NewGoalUsageTee(sink)

	// Extension preflight (stages 5b/7): start the installed, enabled v2 runtime
	// packages ONCE, here, before model resolution, so plugin-namespaced refs
	// (plugin/<plugin>/<provider>/<model>) resolve on the very first boot and the
	// same sidecar generation feeds the executor, planner, guardian, sub-agents,
	// the snapshot assembly, and the frontend catalog. With no runtime package
	// installed preflight is a no-op and the whole build below takes the
	// untouched pre-sidecar path. The generation moves up with it: the sidecar
	// handshake's session context carries this build's generation, and a fresh
	// controller has no session path yet, so the session ID is generation-scoped
	// (the handshake only requires a stable, non-empty identity).
	generation := nextRuntimeGeneration()
	sessionID := fmt.Sprintf("boot-%d", generation)
	proxySpec := cfg.NetworkProxySpec()
	extWarn := func(msg string) {
		redacted := secrets.RedactCredentials(msg)
		slog.Warn("boot: extension runtime: "+redacted, "root", root)
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: redacted})
	}
	// Stage 8a: the host extension UI hub serves every sidecar's host/ui/* calls
	// for this generation — publications become frontend events through the
	// controller sink, blocking prompts ride the controller's Ask channel. The
	// controller only exists after control.New below, so both seams indirect
	// through ctrlRef; traffic before that (a sidecar publishing during its
	// handshake) falls back to the same sink directly, matching the emission the
	// controller would have made.
	var ctrlRef atomic.Pointer[control.Controller]
	// Readiness signals for gateExtensionUIRequest: a sidecar may legally
	// issue host/ui/request right after extension/initialized, before the
	// controller exists. ready closes at ctrlRef.Store; failed closes on any
	// build error before the RuntimeSet takes ownership (the pendingMgr defer
	// below), so a startup request never hangs a dying build.
	controllerReady := make(chan struct{})
	controllerBuildFailed := make(chan struct{})
	extUIHub := uihub.New(uihub.Options{
		SessionID:  sessionID,
		Generation: generation,
		Owner:      owner,
		Emit: func(ev event.Event) {
			if c := ctrlRef.Load(); c != nil {
				c.EmitExtensionEvent(ev)
				return
			}
			sink.Emit(ev)
		},
		Request: func(reqCtx context.Context, req uihub.HubRequest) (map[string]any, bool, error) {
			return gateExtensionUIRequest(reqCtx, ctrlRef.Load, controllerReady, controllerBuildFailed,
				func(c *control.Controller) (map[string]any, bool, error) {
					return uihub.AskRequestFunc(c.Ask)(reqCtx, req)
				})
		},
		Warn: func(msg string) {
			slog.Warn("boot: extension UI hub: "+msg, "root", root)
		},
	})
	extensionMgr, err := preflightExtensionRuntimes(ctx, config.ReasonixHomeDir(), extensionBoot{
		session:   protocol.SessionContext{SessionID: sessionID, WorkspaceRoot: root, Provider: entry.Kind, Model: modelRef, Generation: generation},
		ui:        extUIHub,
		onWarning: extWarn,
	}, opts.Extensions, planForPreflight(opts, generation))
	if err != nil {
		return nil, fmt.Errorf("boot: %w", err)
	}
	// Until the RuntimeSet takes ownership at snapshot assembly, every error
	// path between here and there must retire the preflighted sidecars — no
	// process may outlive a failed build.
	pendingMgr := extensionMgr
	defer func() {
		if pendingMgr != nil {
			close(controllerBuildFailed)
			_ = pendingMgr.Close()
		}
	}()

	// The build's provider resolution base: the caller-owned broker when
	// injected, the local config-backed resolver otherwise. When a started
	// sidecar declares providers, fold them in NOW (stage 7) with the
	// provider:<ref> slot claims from the same manifest data the kernel's
	// ReplaceClaims pass uses, so first-boot model resolution sees them. A
	// conflict with the base catalog that lacks the plugin's claim is fatal,
	// the same class as a required runtime that cannot start: booting without
	// the declared provider would silently change what the session is.
	modelCapabilities := config.NewModelCapabilityResolver()
	baseResolver := opts.ProviderResolver
	if baseResolver == nil {
		baseResolver = NewLocalProviderResolverWithCapabilities(cfg, proxySpec, modelCapabilities)
	}
	effectiveResolver := opts.ProviderResolver
	if effectiveResolver == nil {
		effectiveResolver = baseResolver
	}
	var extensionResolver provider.Resolver
	if extensionMgr != nil {
		declares := false
		for _, client := range extensionMgr.Clients() {
			if len(client.Handshake().Providers) > 0 {
				declares = true
				break
			}
		}
		if declares {
			claims, claimsErr := resolveReplacementClaims(extensionMgr.Contributions())
			if claimsErr != nil {
				return nil, fmt.Errorf("boot: %w", claimsErr)
			}
			merged, mergeErr := mergeSidecarProviders(baseResolver, extensionMgr, claims, owner)
			if mergeErr != nil {
				return nil, fmt.Errorf("boot: %w", mergeErr)
			}
			installSidecarStreamRouters(extensionMgr, merged)
			effectiveResolver = merged
			extensionResolver = merged
		}
	}

	// Fall through a keyless default_model to the next configured chat model
	// instead of hard-failing every command on "missing env X_API_KEY" (issue
	// #6996). The fallback only kicks in when the caller did not pass an
	// explicit opts.Model; explicit choices still fail loudly.
	modelName := opts.Model
	if modelName == "" {
		if resolved, _, ok := cfg.ResolveNewSessionChatModel(); ok {
			modelName = resolved
		}
	}
	// opts.AgentPreset/opts.TokenMode now seed the session quality floor (see
	// the SetQualityFloor call after control.New); light folds to standard.
	keepPolicy := agentKeepPolicy(cfg.Agent.Keep)
	// Entry resolution: the caller-owned broker is authoritative for every
	// ref; the extension-merged resolver only owns plugin refs — a config ref
	// keeps the full config entry (kind, endpoint, credentials, balance URL,
	// missing-key notice), exactly as without extensions installed.
	entryResolver := opts.ProviderResolver
	if entryResolver == nil && extensionResolver != nil && providerext.PluginRefOwner(modelName) != "" {
		entryResolver = extensionResolver
	}
	entry, modelRef, err := resolveModelEntry(entryResolver, cfg, modelName)
	if err != nil {
		return nil, err
	}
	if opts.EffortOverride != nil {
		entry.Effort = *opts.EffortOverride
		if entry.Kind == "anthropic" && strings.TrimSpace(entry.Effort) != "" && strings.TrimSpace(entry.Thinking) == "" {
			entry.Thinking = "adaptive"
		}
	}
	// RequireKey fails fast on a missing credential (run/serve); plugin-
	// namespaced refs carry no config credential — the extension provider holds
	// its own keys — so the merged resolver's resolution is their only gate.
	if opts.RequireKey && opts.ProviderResolver == nil && providerext.PluginRefOwner(modelName) == "" {
		if err := cfg.Validate(modelName); err != nil {
			return nil, err
		}
	}

	if migErr != nil {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "Config migration did not complete.", Detail: "config migration from ~/.reasonix failed: " + migErr.Error()})
	} else if migrated != nil {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: migrated.Notice()})
	}
	if deepSeekProtocolMigrated {
		sink.Emit(event.Event{
			Kind:   event.Notice,
			Level:  event.LevelInfo,
			Text:   "User configuration was upgraded.",
			Detail: "Legacy built-in DeepSeek defaults now use Chat Completions with independent web search. Explicit custom routes remain unchanged. Protocol changes start a new provider cache prefix; later requests rebuild normal prefix-cache reuse.",
		})
	} else if deepSeekProtocolMigErr != nil {
		sink.Emit(event.Event{
			Kind:   event.Notice,
			Level:  event.LevelWarn,
			Text:   "DeepSeek protocol migration did not complete.",
			Detail: deepSeekProtocolMigErr.Error(),
		})
	}
	if stepLimitsMigrated || cfg.IgnoredLegacyAgentStepLimits() {
		level := event.LevelInfo
		text := "Deprecated agent step limits were removed."
		detail := "[agent].max_steps and planner_max_steps are no longer used; Reasonix now manages interactive progress automatically. " +
			"Use the CLI --max-steps flag for a one-off run or [bot].max_steps for unattended bot sessions."
		if stepLimitMigErr != nil {
			level = event.LevelWarn
			text = "Deprecated agent step limits were ignored."
			detail += " The old keys were ignored but could not be removed: " + stepLimitMigErr.Error()
		}
		sink.Emit(event.Event{
			Kind:   event.Notice,
			Level:  level,
			Text:   text,
			Detail: detail,
		})
	} else if stepLimitMigErr != nil {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "Deprecated agent step-limit migration did not complete.", Detail: stepLimitMigErr.Error()})
	}
	if redactToolOutputMigrated || redactToolOutputMigErr != nil {
		level := event.LevelInfo
		text := "Deprecated redact_tool_output setting was removed."
		detail := "[secrets].redact_tool_output no longer has any effect: ordinary model/tool content and local session/job artifacts now preserve their original text. Explicit diagnostics and reasonix doctor redact-sessions still redact credential values."
		if redactToolOutputMigErr != nil {
			level = event.LevelWarn
			text = "Deprecated redact_tool_output setting was ignored."
			detail += " The old key could not be removed: " + redactToolOutputMigErr.Error()
		}
		sink.Emit(event.Event{Kind: event.Notice, Level: level, Text: text, Detail: detail})
	}
	if memoryCompilerMigrated || memoryCompilerMigErr != nil {
		level := event.LevelInfo
		text := "Deprecated memory_compiler setting was removed."
		detail := "The Memory v5 execution compiler has been removed from Reasonix: [agent].memory_compiler no longer has any effect, user turns are never replaced by compiled execution contracts, and no compiler state is written. Old transcripts containing compiled turns still display normally."
		if memoryCompilerMigErr != nil {
			level = event.LevelWarn
			text = "Deprecated memory_compiler setting was ignored."
			detail += " The old key could not be removed: " + memoryCompilerMigErr.Error()
		}
		sink.Emit(event.Event{Kind: event.Notice, Level: level, Text: text, Detail: detail})
	}
	if multiThresholdMigrated || multiThresholdMigErr != nil {
		level := event.LevelInfo
		text := "上下文维护已简化为单一自动压缩阈值。"
		detail := "Context maintenance now uses a single automatic compact_ratio (default 0.80). soft_compact_ratio, tool_result_snip_ratio, compact_force_ratio, cold_resume_prune, and context_editing were removed from config."
		if multiThresholdMigErr != nil {
			level = event.LevelWarn
			text = "Deprecated multi-threshold compaction keys were ignored."
			detail += " The old keys could not be removed: " + multiThresholdMigErr.Error()
		}
		sink.Emit(event.Event{Kind: event.Notice, Level: level, Text: text, Detail: detail})
	}
	migration.MigrateLegacyMemorySources(sink)
	migration.MigrateLegacySessionSources(sink)
	if ignored := cfg.IgnoredProjectDefaultModel(); ignored != "" {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "Ignored the project config's default_model.", Detail: fmt.Sprintf("./reasonix.toml sets default_model = %q but no configured provider serves it; using %q from your user config instead. Edit or remove that default_model line to silence this notice.", ignored, cfg.DefaultModel)})
	}

	// A resolvable model whose API key env is unset would otherwise build fine
	// (RequireKey is false so the UI stays reachable) and then fail silently on the
	// first request, showing as an empty/dead model. Surface the cause up front.
	if !opts.RequireKey && entry.RequiresAPIKey() && entry.APIKey() == "" {
		sink.Emit(event.Event{Kind: event.Notice, Text: "Selected model is missing its API key.", Detail: fmt.Sprintf("model %q is selected but its API key %s is not set — requests will fail until you set it", modelName, entry.APIKeyEnv)})
	}
	// Every role setting lazily acquires a workspace write lease on the first
	// real writer. Read-only turns never take the lease.
	var workspaceLease *workspacelease.Owner
	jobOptions := []jobs.Option{
		jobs.WithStalledWarningAfter(time.Duration(cfg.BackgroundJobStalledWarningSeconds()) * time.Second),
		jobs.WithSessionOwnershipProbe(agent.SessionLeaseHeldByCurrentRuntime),
	}
	workspaceLease, err = workspacelease.New(root, config.WorkspaceLeaseDir(), func() {
		sink.Emit(event.Event{
			Kind:   event.Notice,
			Level:  event.LevelInfo,
			Code:   event.NoticeCodeWorkspaceLease,
			Text:   "Another session is writing to this workspace; this session will continue automatically when it is safe.",
			Detail: "workspace write lease is busy; read-only work remains concurrent",
		})
	})
	if err != nil {
		return nil, fmt.Errorf("initialize workspace write lease: %w", err)
	}
	jobOptions = append(jobOptions, jobs.WithJobStartObserver(workspaceLease.RetainUntil))
	jm := jobs.NewManager(sink, jobOptions...)
	sessionDir := opts.SessionDir
	if sessionDir == "" {
		sessionDir = config.SessionDir()
	}
	reconcileCleanupPending := opts.CleanupPendingReconciler
	if reconcileCleanupPending == nil {
		reconcileCleanupPending = control.ReconcileCleanupPending
	}
	if err := reconcileCleanupPending(sessionDir); err != nil {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "cleanup-pending reconciliation failed: " + err.Error()})
	}

	// proxySpec was computed during extension preflight (the merged resolver's
	// local base needs it); validate it before any provider construction.
	if err := netclient.Validate(proxySpec); err != nil {
		return nil, err
	}
	balanceClient, err := netclient.NewHTTPClient(proxySpec, netclient.TransportOptions{})
	if err != nil {
		return nil, err
	}
	execProv, err := resolveProvider(effectiveResolver, cfg, proxySpec, provider.Selection{Ref: modelRef, Effort: opts.EffortOverride})
	if err != nil {
		return nil, err
	}
	shell := sandbox.ResolveShell(cfg.Tools.Shell.Prefer, cfg.Tools.Shell.Path, stderr)

	sysPrompt, err := cfg.ResolveSystemPromptForRoot(root)
	if err != nil {
		if !config.IsMissingSystemPromptFile(err) {
			return nil, err
		}
		// A stale missing prompt file must not block startup: warn and fall back
		// to the inline (or built-in default) system prompt. Other read failures
		// stay fatal so Reasonix never runs without explicitly configured policy.
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: err.Error() + "; falling back to inline/default system prompt"})
		sysPrompt = cfg.InlineSystemPrompt()
	}
	// Output style: fold the selected persona/tone block into the base prompt
	// before language/memory/skills append, so a "replace" style (keep-coding
	// false) still keeps those. Applied once, into the cache-stable prefix.
	if st, ok := outputstyle.Resolve(cfg.Agent.OutputStyle, outputstyle.Dirs()); ok {
		sysPrompt = outputstyle.Apply(sysPrompt, st)
	}
	sysPrompt = appendCorePolicies(sysPrompt)
	sysPrompt += "\n\n" + sessioncontext.PolicyBlock()
	sessionContextStatic := sessioncontext.Sections{Workspace: currentWorkspacePromptLine(root)}
	// Execution modes no longer exist. Host obligations are fact-driven and
	// never rewrite the cache-stable system prefix or tool schemas.
	if cfg.EnvironmentEnabled() {
		shellLabel := resolvedShellLabel(shell, cfg.Tools.Shell.Path)
		envSection := environment.FormatSection(
			environment.RunProbesWithOptions(ctx, environment.DefaultProbes(), environment.ProbeOptions{
				Overrides: cfg.Environment.Tools,
				DenyRoots: []string{root},
				// Persist probe results across restarts so transient probe flaps do
				// not generate needless session-context replacements.
				SnapshotDir: config.CacheDir(),
			}),
			runtime.GOOS+"/"+runtime.GOARCH,
			shellLabel,
			cfg.Environment.Tools,
		)
		sessionContextStatic.Environment = envSection
	}
	sessionContextStatic.Environment = appendOfflineEnvironmentNote(sessionContextStatic.Environment, cfg.Environment.Offline)

	// Stable memory policy and REASONIX.md / AGENTS.md standing instructions
	// enter the system prompt. Pinned facts and the background index remain in
	// the controller-owned session-context snapshot.
	if _, err := memory.StoreFor(config.MemoryUserDir(), root).MigrateV2(); err != nil {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: "Memory metadata migration did not complete.", Detail: err.Error()})
	}
	mem := memory.Load(memory.Options{CWD: root, UserDir: config.MemoryUserDir()})
	projectChecks := instruction.ExtractHostChecks(mem.Docs)
	sysPrompt = memory.Compose(sysPrompt, mem)

	implicitSkillInvocation := cfg.ImplicitSkillInvocationEnabled()
	// Skills: rediscovery skipped on no-op/interceptor/UI rebuilds when
	// ReuseAssembly is retained from the previous BuildResult.
	var skillStore *skill.Store
	var skills []skill.Skill
	var allSkillStore *skill.Store
	var allSkills []skill.Skill
	canReuseSkills := opts.ReuseAssembly != nil && shouldReuseDiscovery(opts.PreviousPlan) &&
		opts.ReuseAssembly.ImplicitSkillInvocation == implicitSkillInvocation
	if canReuseSkills {
		skills = opts.ReuseAssembly.Skills
		allSkills = skills
		skillStore = skill.New(skill.Options{ProjectRoot: root, Stderr: io.Discard})
		allSkillStore = skillStore
		if s := strings.TrimSpace(opts.ReuseAssembly.SystemPrompt); s != "" {
			sysPrompt = s
		}
	} else {
		skillStore = skill.New(skill.Options{
			ProjectRoot: root, CustomPaths: cfg.SkillCustomPaths(), PluginPaths: cfg.PluginPackageSkillOwners(),
			PluginAgentPaths: cfg.PluginPackageAgentOwners(), ExcludedPaths: cfg.SkillExcludedPaths(),
			DisabledNames: cfg.DisabledSkillNames(), MaxDepth: cfg.SkillMaxDepth(), Stderr: opts.Stderr,
		})
		skillStore.ConfigureInvocationPolicy("", nil)
		skills = skillStore.List()
		allSkillStore = skill.New(skill.Options{ProjectRoot: root, CustomPaths: cfg.SkillCustomPaths(), PluginPaths: cfg.PluginPackageSkillOwners(), PluginAgentPaths: cfg.PluginPackageAgentOwners(), ExcludedPaths: cfg.SkillExcludedPaths(), MaxDepth: cfg.SkillMaxDepth(), Stderr: io.Discard})
		allSkills = allSkillStore.List()
		if implicitSkillInvocation {
			sysPrompt += "\n\n" + skill.InvocationPolicyBlock()
		}
	}
	sysPrompt = config.ApplyOfficialDeepSeekV4ProPersona(sysPrompt, entry)

	reg := tool.NewRegistry()
	writeRoots := cfg.WriteRootsForRoot(root)
	writeRoots = appendUniquePaths(writeRoots, additionalDirs...)
	if opts.WorkspaceOnly {
		writeRoots = []string{root}
	}
	networkEnabled := cfg.Sandbox.Network
	if opts.SandboxNetworkOverride != nil {
		networkEnabled = *opts.SandboxNetworkOverride
	}
	bashMode := cfg.BashMode()
	if override := strings.TrimSpace(opts.SandboxBashOverride); override != "" {
		bashMode = override
	}
	forbidReadRoots := RuntimeForbidReadRoots(cfg, root)
	// managedConfig names the Reasonix-owned config FILES (config.toml,
	// compatibility TOMLs, legacy v0.x config.json) the file-writers may repair
	// outside the workspace after a fresh per-write human approval. The bash
	// OS-sandbox write roots deliberately stay unwidened: config repair goes
	// through the approval-gated file tools, not raw shell writes.
	managedConfig := builtin.NewManagedConfigPaths(config.ReasonixManagedConfigPaths())
	bashSpec := sandbox.Spec{Mode: bashMode, WriteRoots: writeRoots, ForbidReadRoots: forbidReadRoots, Network: networkEnabled}
	bashSpec.Shell = shell
	// The session-data guard blocks agent writes into Reasonix's own session
	// stores (they race the app's saves and surface as conflict-copy loops);
	// explicit allow_write entries stay a sanctioned escape hatch.
	allowWriteRoots := cfg.AllowWriteRoots()
	if opts.WorkspaceOnly {
		allowWriteRoots = nil
	}
	sessionGuard := builtin.NewSessionDataGuard(config.MemoryUserDir(), allowWriteRoots)
	writeRootSet := sandbox.NewWritableRootSet(writeRoots)
	bashSpec.ProtectedWriteRoots = sandbox.ProtectedWriteRoots(config.MemoryUserDir())
	if bashSpec.Mode == "enforce" && !sandbox.Available() {
		fmt.Fprintln(stderr, "warning: "+sandbox.UnavailableMessage())
	}
	if autoShellPrefer(cfg.Tools.Shell.Prefer) && shell.Kind == sandbox.ShellPowerShell {
		fmt.Fprintln(stderr, "warning: bash not found on PATH; the shell tool will run commands under Windows PowerShell. Install Git for Windows or WSL to use bash, or set [tools.shell] prefer=\"powershell\" to silence this.")
	}
	searchSpec := builtin.ResolveSearch(cfg.Tools.Search.Engine, cfg.Tools.Search.RgPath, stderr)
	bashTimeout := time.Duration(cfg.BashTimeoutSeconds()) * time.Second
	enabledBuiltins := cfg.Tools.Enabled
	readPathResolver := builtin.NewPathResolver()
	// Session-private temporary directory manager for Bash/grep. Rebuild
	// reuses the previous Controller's Manager; a fresh build creates one
	// here so tools and the Controller share the same instance from boot.
	sessionTemp := opts.SessionTemp
	if sessionTemp == nil {
		sessionTemp = sessiontemp.New()
	}
	// Register the full built-in inventory for use_capability dispatch. The
	// provider-visible surface is narrowed later via SetProviderVisibleTools.
	addBuiltins(reg, enabledBuiltins, writeRoots, writeRootSet, bashSpec, bashTimeout, searchSpec, stderr, root, proxySpec, forbidReadRoots, readPathResolver, sessionGuard, managedConfig, opts.FileOverlay, opts.TerminalRunner, sessionTemp, fileWriteReceipt)
	addWebSearch(reg, cfg, entry, proxySpec, sink)
	// Use the caller-supplied shared host when set, so controllers for the same
	// workspace root reuse running MCP processes (e.g. one CodeGraph daemon
	// instead of one per tab). Otherwise construct a private host per controller.
	pluginHost := opts.SharedHost
	if pluginHost == nil {
		pluginHost = plugin.NewHostWithProfile(opts.MCPHostProfile)
	}

	// Enabled MCP servers enter the tool catalog at boot. Cached schemas
	// register placeholders without starting processes; cache-miss servers get
	// a single background catalog discovery. First real tool call uses
	// EnsureConnected so parent/child/tab runtimes share one process.
	pluginSpecOptions := PluginSpecOptions{
		DefaultStartupTimeout: time.Duration(cfg.MCPStartupTimeoutSeconds()) * time.Second,
		DefaultCallTimeout:    time.Duration(cfg.MCPCallTimeoutSeconds()) * time.Second,
		LaunchManager:         mcplaunch.ForWorkspace(config.ReasonixHomeDir(), root),
		ConfigSource:          "workspace_config",
		StateHome:             config.ReasonixHomeDir(),
		WriterRoots:           writeRoots,
		ForbidReadRoots:       forbidReadRoots,
		Network:               networkEnabled,
		PackageOwners:         pluginPackageOwners(cfg),
		OAuthHTTPClient:       balanceClient,
	}
	autoStartEntries := cfg.EnabledPlugins(root, config.DefaultMCPActivationStore())
	enabledMCPNames := make(map[string]bool, len(autoStartEntries))
	for _, enabled := range autoStartEntries {
		if name := strings.TrimSpace(enabled.Name); name != "" {
			enabledMCPNames[name] = true
		}
	}
	// Legacy eager/background tiers are still parsed for config compatibility
	// but no longer change process start timing. Keep the partition only so
	// demotion notices remain meaningful for chronically slow eager configs.
	eagerEntries, bgEntries := partitionByTier(autoStartEntries)
	extraSpecs := applyDefaultMCPStartupTimeout(
		applyDefaultMCPCallTimeout(
			applyKnownPluginOverrides(opts.ExtraPlugins, root),
			pluginSpecOptions.DefaultCallTimeout,
		),
		pluginSpecOptions.DefaultStartupTimeout,
	)
	for i := range extraSpecs {
		if strings.TrimSpace(extraSpecs[i].WorkspaceRoot) == "" {
			extraSpecs[i].WorkspaceRoot = root
		}
		if extraSpecs[i].LaunchManager == nil {
			extraSpecs[i].LaunchManager = pluginSpecOptions.LaunchManager
		}
		if strings.TrimSpace(extraSpecs[i].ConfigSource) == "" {
			extraSpecs[i].ConfigSource = "host_session"
		}
		if !extraSpecs[i].RequireLaunchApproval {
			// Session-scoped MCP specs arrive through an explicit host/user action
			// (for example ACP session/new), so they follow installed-server
			// authorization without another per-tool or per-session prompt.
			extraSpecs[i].Authorized = true
		}
		applyMCPIsolation(&extraSpecs[i], root, pluginSpecOptions)
	}
	// Auto-demote: any eager plugin that has been chronically slow (recent
	// samples repeatedly hit the blocking startup budget) drops to background
	// for this session. The user keeps eager intent, just doesn't pay for it
	// on a server that's been misbehaving. A notice surfaces the demotion.
	var demoteMessages []string
	budget := plugin.DefaultStartupBudget()
	kept := eagerEntries[:0]
	for _, e := range eagerEntries {
		rec := plugin.Recommend(e.Name, budget, 0)
		if rec.Demote {
			demoteMessages = append(demoteMessages, rec.Reason)
			bgEntries = append(bgEntries, e)
			continue
		}
		kept = append(kept, e)
	}
	eagerEntries = kept

	eagerSpecs := PluginSpecsForRootWithOptions(eagerEntries, root, pluginSpecOptions)
	bgSpecs := PluginSpecsForRootWithOptions(bgEntries, root, pluginSpecOptions)

	eagerSpecs = append(eagerSpecs, extraSpecs...)

	// Apply caller-supplied stderr override to every spec across tiers.
	if opts.Stderr != nil {
		for i := range eagerSpecs {
			eagerSpecs[i].Stderr = opts.Stderr
		}
		for i := range bgSpecs {
			bgSpecs[i].Stderr = opts.Stderr
		}
	}

	// Host-session ExtraPlugins (for example ACP session servers) are explicit
	// for this controller and still take a short readiness probe so recovery and
	// session-scoped servers are deterministic. User/project config MCP stays
	// catalog-first and process-idle until first real tool call.
	if len(extraSpecs) > 0 {
		for _, s := range extraSpecs {
			if pluginHost.HasClient(s.Name) {
				if tools, err := pluginHost.ToolsFor(ctx, s.Name); err == nil {
					for _, t := range tools {
						reg.Add(t)
					}
					continue
				}
			}
			addCtx, addCancel := context.WithTimeout(ctx, 5*time.Second)
			tools, err := pluginHost.EnsureConnectedWithLifecycle(ctx, addCtx, s, 0)
			addCancel()
			if err != nil {
				if plugin.IsServerAlreadyConnected(err) {
					if tools, err2 := pluginHost.ToolsFor(ctx, s.Name); err2 == nil {
						for _, t := range tools {
							reg.Add(t)
						}
						continue
					}
				}
				// Leave a catalog entry for diagnostics; failures surface in /mcp.
				cs, _ := plugin.LoadCachedSchemaForSpec(s)
				for _, t := range plugin.LazyToolset(s, cs, pluginHost, reg, ctx, false) {
					reg.Add(t)
				}
				sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn,
					Text: "An MCP server failed to start.", Detail: fmt.Sprintf("mcp %s: %v", s.Name, err)})
				continue
			}
			for _, t := range tools {
				reg.Add(t)
			}
		}
	}

	// Configured enabled MCP: cache-hit placeholders without starting processes;
	// cache-miss servers get one background catalog discovery.
	registerEnabledMCP := func(specs []plugin.Spec) {
		for _, s := range specs {
			if pluginHost.HasClient(s.Name) {
				tools, err := pluginHost.ToolsFor(ctx, s.Name)
				if err == nil {
					for _, t := range tools {
						reg.Add(t)
					}
					continue
				}
			}
			cs, _ := plugin.LoadCachedSchemaForSpec(s)
			// Only kick a process for catalog discovery when no usable schema is
			// cached. Cache-hit sessions stay process-idle until first tool call.
			kick := cs == nil || len(cs.Tools) == 0
			for _, t := range plugin.LazyToolset(s, cs, pluginHost, reg, ctx, kick) {
				reg.Add(t)
			}
		}
	}
	// eagerSpecs already includes extraSpecs; avoid double
	// registration of host-session servers that connected above.
	configSpecs := append(append([]plugin.Spec{}, eagerSpecs...), bgSpecs...)
	if len(extraSpecs) > 0 {
		extraNames := map[string]bool{}
		for _, s := range extraSpecs {
			extraNames[s.Name] = true
		}
		filtered := configSpecs[:0]
		for _, s := range configSpecs {
			if extraNames[s.Name] {
				continue
			}
			filtered = append(filtered, s)
		}
		configSpecs = filtered
	}
	registerEnabledMCP(configSpecs)

	for _, msg := range demoteMessages {
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: msg})
	}

	cleanup := pluginHost.Close
	if opts.SharedHost != nil {
		// The caller owns the shared host's lifecycle; the controller must not
		// close it. A no-op cleanup keeps Controller.Close happy without
		// shutting down MCP processes that other controllers still use.
		cleanup = func() {}
	}

	// addTools registers tools on reg and returns the names that were added.
	addTools := func(reg *tool.Registry, tools []tool.Tool) []string {
		names := make([]string, 0, len(tools))
		for _, t := range tools {
			if t == nil {
				continue
			}
			reg.Add(t)
			names = append(names, t.Name())
		}
		return names
	}

	// LSP tools resolve their servers on PATH and spawn lazily on first query, so
	// registering them is cheap even when no server is installed (a query then
	// returns an install hint). The manager is session-scoped; chain its shutdown
	// into the controller's cleanup so servers stop with the session, not the turn.
	var lspMgr *lsp.Manager
	lspToolsAdded := false
	addLSPTools := func() []string {
		if lspMgr == nil || lspToolsAdded {
			return nil
		}
		lspToolsAdded = true
		return addTools(reg, lsp.Tools(lspMgr))
	}
	if cfg.LSP.Enabled {
		lspMgr = lsp.NewManager(root, LSPSpecs(cfg.LSP))
		addLSPTools()
		prev := cleanup
		cleanup = func() { prev(); lspMgr.Close() }
	}

	maxSteps := max(opts.MaxSteps, 0)
	subagentStore, err := newSubagentStore(sessionDir, opts.SubagentParentLive)
	if err != nil {
		return nil, err
	}
	if subagentStore != nil {
		subagentStore.WithDestroyedChecker(jm.IsDestroying)
	}

	// Permission policy gates every tool call. With no HeadlessApprovalMode
	// (interactive bootstrap), the temporary gate preserves the legacy behavior
	// until chat/desktop installs an interactive gate. A real headless caller
	// such as `reasonix run` always supplies a mode: Ask fails closed, Auto
	// allows ordinary writer fallbacks, and DontAsk denies them (#6927).
	// The selected contract is also applied to sub-agents, so they cannot be a
	// weaker path around the parent gate.
	// Sub-agents always run headless: they have no UI to answer a prompt, so they
	// inherit this same gate.
	policy := permission.New(cfg.Permissions.Mode, cfg.Permissions.Allow, cfg.Permissions.Ask, cfg.Permissions.Deny).
		WithAllowDynamicBashFallback(cfg.Permissions.AllowDynamicBash).
		WithSessionAllow(opts.PermissionAllow)
	headlessGate := control.NewSharedHeadlessGate(policy, opts.HeadlessApprovalMode)

	var resolvedHooks []hook.ResolvedHook
	if opts.ReuseAssembly != nil && shouldReuseDiscovery(opts.PreviousPlan) {
		resolvedHooks = opts.ReuseAssembly.Hooks
	} else {
		resolvedHooks = hook.Load(hook.LoadOptions{ProjectRoot: root})
	}
	hookRuntime := hook.RuntimeOptions{}
	if shell.Kind == sandbox.ShellBash {
		hookRuntime.BashPath = shell.Path
	}
	hookRunner := hook.NewRunner(
		resolvedHooks, root, hook.NewDefaultSpawner(hookRuntime),
		func(msg string) { sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelWarn, Text: msg}) },
	)
	// The `task` tool spawns sub-agents that reuse the parent's provider and
	// tool registry. Wired here after the built-ins / plugins are loaded so
	// sub-agents inherit the full tool set (minus `task` itself, to keep
	// nesting out of the picture). It registers into the same reg the
	// executor uses, so the model surfaces it like any other tool.
	resolveSubagentProvider := func(modelRef, effort string) (provider.Provider, *provider.Pricing, int, error) {
		me := *entry
		selectedRef := modelRefFromEntry(entry)
		if strings.TrimSpace(modelRef) != "" {
			if resolved, ok := cfg.ResolveModel(modelRef); ok {
				me = *resolved
				selectedRef = modelRefFromEntry(resolved)
			} else if effectiveResolver != nil {
				me = *syntheticEntryFromResolver(effectiveResolver, modelRef)
				selectedRef = modelRef
			} else {
				return nil, nil, 0, fmt.Errorf("unknown model %q", modelRef)
			}
		}
		var effortOverride *string
		if strings.TrimSpace(effort) != "" {
			normalized, err := config.NormalizeEffort(&me, effort)
			if err != nil {
				if effectiveResolver == nil {
					return nil, nil, 0, err
				}
				normalized = effort
			}
			me.Effort = normalized
			effortOverride = &normalized
			if me.Kind == "anthropic" && strings.TrimSpace(me.Effort) != "" && strings.TrimSpace(me.Thinking) == "" {
				me.Thinking = "adaptive"
			}
		}
		p, err := resolveProvider(effectiveResolver, cfg, proxySpec, provider.Selection{Ref: selectedRef, Effort: effortOverride})
		if err != nil {
			return nil, nil, 0, err
		}
		return p, me.Price, me.ContextWindow, nil
	}
	subagentIdentity := func(modelRef, effort string) (string, string) {
		return subagentEffectiveIdentity(cfg, opts.ProviderResolver, modelName, entry, modelRef, effort)
	}
	taskModel := firstNonEmpty(cfg.Agent.SubagentModels["task"], cfg.Agent.SubagentModel)
	taskEffort := firstNonEmpty(cfg.Agent.SubagentEfforts["task"], cfg.Agent.SubagentEffort)
	maxSubagentDepth := agent.NormalizeMaxSubagentDepth(cfg.Agent.MaxSubagentDepth)
	maxSubagentConcurrency, maxParallelWriters := agent.NormalizeConcurrencyLimits(
		cfg.Agent.MaxSubagentConcurrency, cfg.Agent.MaxParallelWriters,
	)
	subagentScheduler := agent.NewSubagentScheduler(maxSubagentConcurrency, maxParallelWriters)
	profileLookup := func(name string) (agent.ProfileDefinition, bool) {
		sk, ok := skillStore.Read(name)
		if !ok || sk.RunAs != skill.RunSubagent {
			return agent.ProfileDefinition{}, false
		}
		return agent.ProfileFromSkill(skillStore.Prepare(sk)), true
	}
	profileConfigModel := func(profile string) string {
		for _, key := range SubagentModelKeys(profile) {
			if m := strings.TrimSpace(cfg.Agent.SubagentModels[key]); m != "" {
				return m
			}
		}
		return ""
	}
	profileConfigEffort := func(profile string) string {
		for _, key := range SubagentModelKeys(profile) {
			if e := strings.TrimSpace(cfg.Agent.SubagentEfforts[key]); e != "" {
				return e
			}
		}
		return ""
	}
	bashSandboxEnforced := bashSpec.Enforce
	taskToolAdded := false
	readOnlyTaskToolAdded := false
	var taskTool *agent.TaskTool
	// capRuntime is assigned after MCP specs load; closures capture the variable
	// so task tools created later still receive the session-shared substrate.
	var capRuntime *agent.MCPCapabilityRuntime
	visionProviderResolver := func(ref string) (provider.Provider, error) {
		ve, ok := resolveOptionalEntry(effectiveResolver, cfg, strings.TrimSpace(ref))
		if !ok || ve == nil || strings.TrimSpace(ve.Model) == "" {
			return nil, fmt.Errorf("unknown vision model %q", ref)
		}
		return resolveProvider(effectiveResolver, cfg, proxySpec, provider.Selection{Ref: modelRefFromEntry(ve)})
	}
	visionModelSelector := func(currentRef, _ string) (string, bool) {
		current, ok := resolveOptionalEntry(effectiveResolver, cfg, strings.TrimSpace(currentRef))
		if !ok || current == nil {
			return "", false
		}
		for i := range cfg.Providers {
			p := &cfg.Providers[i]
			if p.Name != current.Name || !p.Configured() {
				continue
			}
			models := p.ModelList()
			ordered := make([]string, 0, len(models))
			if d := p.DefaultModel(); d != "" {
				ordered = append(ordered, d)
			}
			for _, model := range models {
				if model != "" && model != p.DefaultModel() {
					ordered = append(ordered, model)
				}
			}
			for _, model := range ordered {
				candidate, found := cfg.ResolveModel(p.Name + "/" + model)
				if found && candidate.Configured() && modelCapabilities.Resolve(candidate).State == config.CapabilitySupported {
					return candidate.Name + "/" + candidate.Model, true
				}
			}
		}
		return "", false
	}

	imageConfig := &imageinput.Config{Model: cfg.Agent.VisionModel, Resolve: visionProviderResolver, Select: visionModelSelector}
	newTaskTool := func() *agent.TaskTool {
		return agent.NewTaskToolWithOptions(agent.TaskToolOptions{
			ImageInput:          imageConfig,
			Provider:            execProv,
			Pricing:             entry.Price,
			QuoteContext:        quoteCtx,
			ParentRegistry:      reg,
			MaxSteps:            maxSteps,
			ContextWindow:       entry.ContextWindow,
			RecentKeep:          cfg.Agent.RecentKeep,
			SoftCompactRatio:    cfg.Agent.SoftCompactRatio,
			ToolResultSnipRatio: cfg.Agent.ToolResultSnipRatio,
			CompactRatio:        cfg.Agent.CompactRatio,
			CompactForceRatio:   cfg.Agent.CompactForceRatio,
			ContextEditing:      cfg.Agent.ContextEditing,
			Temperature:         cfg.Agent.Temperature,
			ArchiveDir:          config.ArchiveDir(),
			SysPrompt:           "",
			Gate:                headlessGate,
			KeepPolicy:          keepPolicy,
			SubagentModel:       taskModel,
			SubagentEffort:      taskEffort,
			ResolveProvider:     resolveSubagentProvider,
		}).
			WithTranscripts(subagentStore, root, modelName, entry.Effort).
			WithTranscriptIdentityResolver(subagentIdentity).
			WithMaxSubagentDepth(maxSubagentDepth).
			WithAblation(opts.Ablation).
			WithWorkspaceLease(workspaceLease).
			WithScheduler(subagentScheduler).
			WithProfileLookup(profileLookup).
			WithProfileConfigResolvers(profileConfigModel, profileConfigEffort).
			WithBashSandboxEnforced(bashSandboxEnforced).
			WithCapabilityRuntime(capRuntime).
			WithWriteRoots(writeRootSet)
	}
	addTaskTool := func() string {
		if opts.Ablation.Off(ablation.Subagent) {
			return "task tool is disabled for this run."
		}
		if taskToolAdded {
			return "task tool is already enabled."
		}
		taskToolAdded = true
		if taskTool == nil {
			taskTool = newTaskTool()
		}
		// The registry exports schemas in stable name order. Keep this surface
		// static: profile names and result refs never enter provider-visible
		// schemas, and the result reader does not change between turns.
		reg.Add(taskTool)
		reg.Add(agent.NewParallelTasksTool(taskTool, reg))
		reg.Add(agent.NewFleetTool(taskTool))
		reg.Add(agent.NewSubagentResultTool(taskTool))
		return "enabled task."
	}
	addReadOnlyTaskTool := func() string {
		if opts.Ablation.Off(ablation.Subagent) {
			return "read_only_task tool is disabled for this run."
		}
		if readOnlyTaskToolAdded {
			return "read_only_task tool is already enabled."
		}
		readOnlyTaskToolAdded = true
		if taskTool == nil {
			taskTool = newTaskTool()
		}
		reg.Add(agent.NewReadOnlyTaskTool(taskTool))
		return "enabled read_only_task."
	}
	addTaskTool()
	addReadOnlyTaskTool()

	// Product documentation, session, and memory tools are always present on the
	// unified host registry for every role setting. Provider-visible surface stays
	// lean via use_capability; these tools are dispatchable without schema growth.
	docsToolAdded := false
	addDocsTool := func() string {
		if docsToolAdded {
			return "docs is already enabled."
		}
		docsToolAdded = true
		reg.Add(productdocs.NewTool())
		return "enabled docs."
	}
	sessionToolsAdded := false
	addSessionTools := func() string {
		if sessionToolsAdded {
			return "sessions are already enabled."
		}
		sessionToolsAdded = true
		// history and memory are the BM25-backed surfaces; the ablation arm drops
		// only those two and leaves the direct-access tools alone, so a lost solve
		// is attributable to retrieval and not to a missing session reader.
		if opts.Ablation.Off(ablation.Retrieval) {
			reg.Add(sessiontool.NewListSessionsTool(sessionDir))
			reg.Add(sessiontool.NewReadSessionTool(sessionDir))
			return "enabled list_sessions, read_session."
		}
		reg.Add(history.NewIndexedTool(history.Options{SessionDir: sessionDir, GlobalSessionDir: config.SessionDir(), ArchiveDir: config.ArchiveDir()}))
		reg.Add(sessiontool.NewListSessionsTool(sessionDir))
		reg.Add(sessiontool.NewReadSessionTool(sessionDir))
		return "enabled history, list_sessions, read_session."
	}
	memoryToolsAdded := false
	addMemoryTools := func() string {
		if memoryToolsAdded {
			return "memory tools are already enabled."
		}
		memoryToolsAdded = true
		if opts.Ablation.Off(ablation.Retrieval) {
			reg.Add(memory.NewRememberTool(mem.Store))
			reg.Add(memory.NewForgetTool(mem.Store))
			return "enabled remember, forget."
		}
		reg.Add(memory.NewRecallTool(mem.Store))
		reg.Add(memory.NewRememberTool(mem.Store))
		reg.Add(memory.NewForgetTool(mem.Store))
		return "enabled memory, remember, forget."
	}
	addDocsTool()
	addSessionTools()
	addMemoryTools()

	// The `ask` tool puts structured multiple-choice questions to the user. It
	// reaches them through the Asker on the call context, which interactive
	// frontends wire to the controller (EnableInteractiveApproval); a headless run
	// has none, so ask resolves to "decide for yourself".
	registerInteractiveAgentTools(reg)

	// Skill tools: read_only_skill is a narrow explicitly read-only entry point; the
	// full skills source adds run_skill / install_skill plus the dedicated
	// subagent wrappers (explore / research / review / security_review). Read-only
	// subagent skills run ephemerally with the same registry boundary as
	// read_only_task, so they cannot write, install, mutate memory, resume/fork
	// transcripts, or delegate further.
	//
	subagentSkillOptions := newSubagentSkillOptionsFactory(cfg.Agent, quoteCtx, headlessGate, keepPolicy, maxSubagentDepth, opts.Ablation, workspaceLease, writeRootSet)
	readOnlySkillRunner := func(sctx context.Context, sk skill.Skill, task string, runOpts skill.SubagentRunOptions) (string, error) {
		if strings.TrimSpace(runOpts.ContinueFrom) != "" || strings.TrimSpace(runOpts.ForkFrom) != "" {
			return "", fmt.Errorf("read_only_skill does not support continue_from/fork_from")
		}
		releaseSlot, err := subagentScheduler.Acquire(sctx, agent.AcquireRequest{
			Writer: false,
			Nested: agent.SubagentDepth(sctx) > 0,
			Label:  sk.Name,
		})
		if err != nil {
			return "", err
		}
		defer releaseSlot()
		sk = skill.WithCodeGraphTools(sk, skill.CodeGraphReadTools(reg))
		prov, price, ctxWin := execProv, entry.Price, entry.ContextWindow
		modelRef := subagentModelRef(cfg, sk)
		effortRef := subagentEffortRef(cfg, sk)
		if modelRef != "" || effortRef != "" {
			p, pr, cw, err := resolveSubagentProvider(modelRef, effortRef)
			if err != nil {
				return "", fmt.Errorf("read-only subagent skill %q profile: %w", sk.Name, err)
			}
			prov, price, ctxWin = p, pr, cw
		}
		childDepth := agent.SubagentDepth(sctx) + 1
		if childDepth > maxSubagentDepth {
			return "", fmt.Errorf("subagent delegation depth limit reached (max_subagent_depth=%d)", maxSubagentDepth)
		}
		subReg := agent.ReadOnlySubagentToolRegistryForDepthWithRuntime(reg, sk.AllowedTools, childDepth, maxSubagentDepth, capRuntime)
		if subReg.Len() == 0 {
			return "", fmt.Errorf("read_only_skill: skill %q has no read-only tools available", sk.Name)
		}
		switch sk.Name {
		case "review", "security-review", "security_review":
			agent.AttachReviewReportTool(subReg)
		}
		steps := maxSteps
		if steps > 0 {
			if steps /= 2; steps < 5 {
				steps = 5
			}
		}
		// Custom and named built-in profiles fully control their system prompt
		// (no implicit concise/DefaultReadOnlyTaskSystemPrompt overlay).
		sysPrompt := strings.TrimSpace(sk.Body)
		if sysPrompt == "" {
			sysPrompt = agent.DefaultReadOnlyTaskSystemPrompt
		}
		task, runOptions := reviewSubagentSkillOptions(sctx, sk.Name, task, steps, price, ctxWin, childDepth, subagentSkillOptions)
		usageModelRef, _ := subagentIdentity(modelRef, effortRef)
		runOptions.ModelRef = usageModelRef
		// Review gates consume typed, host-verifiable reports so a review
		// cannot end in unverifiable prose. Review skills run only for
		// mid/high-risk work under the standard policy.
		runOptions.RequireReviewReportKind = agent.ReviewReportKindForSkill(sk.Name)
		// Provider serializers decide whether these images are wire-visible from
		// the child model's own vision capability. Text-only children retain the
		// attachment metadata locally but never receive image parts on the wire.
		childCtx := agent.WithUserImages(sctx, agent.SubagentImageCandidates(sctx))
		return runReadOnlySkillSession(childCtx, prov, subReg, task, runOptions, agent.NestedSink(sctx, event.Discard), sysPrompt, agent.RunReadOnlySubAgentWithSession)
	}
	// Writer-capable subagent skills reuse the sub-agent machinery via this
	// runner: an isolated loop with the skill body as system prompt, a tool set
	// scoped to the skill's allowed-tools (minus recursive meta-tools), optional
	// per-skill model, and resumable transcripts when the parent session supports
	// them. Its tool activity nests under the invoking call, like `task`.
	skillRunner := func(sctx context.Context, sk skill.Skill, task string, runOpts skill.SubagentRunOptions) (string, error) {
		// Writer skills without write_paths claim the whole workspace so they
		// cannot race fleet/task writers that declared disjoint paths.
		acq := agent.AcquireRequest{
			Writer: !sk.ReadOnly,
			Nested: agent.SubagentDepth(sctx) > 0,
			Label:  sk.Name,
		}
		if !sk.ReadOnly {
			whole, werr := agent.WholeWorkspaceWriteClaim(root)
			if werr != nil {
				return "", fmt.Errorf("subagent skill %q write claim: %w", sk.Name, werr)
			}
			acq.WritePaths = whole
		}
		releaseSlot, err := subagentScheduler.Acquire(sctx, acq)
		if err != nil {
			return "", err
		}
		defer releaseSlot()
		sk = skill.WithCodeGraphTools(sk, skill.CodeGraphReadTools(reg))
		prov, price, ctxWin := execProv, entry.Price, entry.ContextWindow
		modelRef := subagentModelRef(cfg, sk)
		effortRef := subagentEffortRef(cfg, sk)
		if modelRef != "" || effortRef != "" {
			p, pr, cw, err := resolveSubagentProvider(modelRef, effortRef)
			if err != nil {
				return "", fmt.Errorf("subagent skill %q profile: %w", sk.Name, err)
			}
			prov, price, ctxWin = p, pr, cw
		}
		childDepth := agent.SubagentDepth(sctx) + 1
		if childDepth > maxSubagentDepth {
			return "", fmt.Errorf("subagent delegation depth limit reached (max_subagent_depth=%d)", maxSubagentDepth)
		}
		// A read-only skill (builtin review/security-review, or frontmatter
		// `read-only: true`) gets its promise enforced at the tool boundary:
		// writer tools are stripped and bash runs under the read-only
		// command policy. Transcripts recorded against the writer-capable
		// registry stop matching on continue_from (schema-hash check reports
		// the mismatch).
		subReg, childWriteRoots := skillSubagentRegistry(sk, reg, childDepth, maxSubagentDepth, capRuntime, writeRootSet)
		// Delivery risk gates require structured review_report from review
		// subagents only — never expose it on the parent tool surface.
		switch sk.Name {
		case "review", "security-review", "security_review":
			agent.AttachReviewReportTool(subReg)
		}
		continueFrom := strings.TrimSpace(runOpts.ContinueFrom)
		legacyForkFrom := strings.TrimSpace(runOpts.ForkFrom)
		if continueFrom != "" && legacyForkFrom != "" {
			return "", fmt.Errorf("continue_from and fork_from are mutually exclusive; pass only continue_from")
		}
		parentID, parentSink, _, _ := agent.CallContext(sctx)
		if runOpts.HostInitiated {
			parentID = ""
		}
		parentSession := agent.ParentSession(sctx)
		var run *agent.SubagentRun
		if subagentStore == nil || parentSession == "" {
			// Headless runs (e.g. `reasonix run`) have no persistent session to
			// own a transcript. Run the skill sub-agent ephemerally, as before
			// persisted transcripts existed, instead of failing. Continuation needs
			// a persisted owner, so it errors here.
			if continueFrom != "" || legacyForkFrom != "" {
				return "", fmt.Errorf("subagent continuation requires a persisted session; none is active in this run")
			}
			run = agent.EphemeralSubagentRun(sk.Body)
		} else {
			identityModel, identityEffort := subagentIdentity(modelRef, effortRef)
			spec := agent.SubagentSpec{
				Kind:             "skill",
				Name:             sk.Name,
				WorkspaceRoot:    root,
				ParentSession:    parentSession,
				ParentToolCallID: parentID,
				SystemPrompt:     sk.Body,
				Registry:         subReg,
				Model:            identityModel,
				Effort:           identityEffort,
			}
			var prepErr error
			if continueFrom != "" {
				run, prepErr = subagentStore.PrepareContinue(continueFrom, spec)
			} else if legacyForkFrom != "" {
				run, prepErr = subagentStore.PrepareLegacyForkFrom(legacyForkFrom, spec)
			} else {
				run, prepErr = subagentStore.PrepareFresh(spec)
			}
			if prepErr != nil {
				return "", prepErr
			}
		}
		defer run.Release()
		steps := maxSteps
		if steps > 0 {
			if steps /= 2; steps < 5 {
				steps = 5
			}
		}
		task, runOptions := reviewSubagentSkillOptions(sctx, sk.Name, task, steps, price, ctxWin, childDepth, subagentSkillOptions)
		runOptions.WriteRoots = childWriteRoots
		usageModelRef, _ := subagentIdentity(modelRef, effortRef)
		runOptions.ModelRef = usageModelRef
		announceSkillSubagentStart(parentSink, parentID, sk.Name, usageModelRef, effortRef, run, continueFrom != "" || legacyForkFrom != "")
		// Review gates consume typed, host-verifiable reports so a review
		// cannot end in unverifiable prose. Review skills run only for
		// mid/high-risk work under the standard policy.
		runOptions.RequireReviewReportKind = agent.ReviewReportKindForSkill(sk.Name)
		var answer string
		// The child provider owns the final vision decision, as in read-only runs.
		childCtx := agent.WithUserImages(sctx, agent.SubagentImageCandidates(sctx))
		agent.EmitSubagentLifecycle(parentSink, "child_running", parentID, sk.Name, usageModelRef, effortRef, run, nil)
		if sk.ReadOnly {
			answer, err = agent.RunReadOnlySubAgentWithSession(childCtx, prov, subReg, run.Session, task,
				runOptions, agent.NestedSink(sctx, event.Discard))
		} else {
			answer, err = agent.RunSubAgentWithSession(childCtx, prov, subReg, run.Session, task,
				runOptions, agent.NestedSink(sctx, event.Discard))
		}
		if err != nil {
			return finishSkillSubagentFailure(sctx, taskTool, subagentStore, parentSink, parentID, sk.Name, usageModelRef, effortRef, task, run, err)
		}
		if err := saveSubagentCompleted(subagentStore, run); err != nil {
			return finishSkillSubagentFailure(sctx, taskTool, subagentStore, parentSink, parentID, sk.Name, usageModelRef, effortRef, task, run, err)
		}
		agent.EmitSubagentLifecycle(parentSink, "child_completed", parentID, sk.Name, usageModelRef, effortRef, run, &agent.SubagentOutcome{Status: agent.SubagentOutcomeCompleted, FinalAnswer: answer})
		return agent.FormatSubagentRunResult(answer, run, false), nil
	}
	skillProfile := func(sk skill.Skill) *event.Profile {
		model, effort := subagentModelRef(cfg, sk), subagentEffortRef(cfg, sk)
		if model == "" && effort == "" {
			return nil
		}
		return &event.Profile{Model: model, Effort: effort}
	}
	var cmds []command.Command
	if opts.ReuseAssembly != nil && shouldReuseDiscovery(opts.PreviousPlan) {
		cmds = opts.ReuseAssembly.Commands
	} else {
		cmds, _ = command.LoadRoots(config.CommandRootsForRoot(root)...)
	}
	slashCommandAdded := false
	slashCommandIncludesSkills := false
	addSlashCommandTool := func(includeSkills bool) string {
		if slashCommandAdded && (!includeSkills || slashCommandIncludesSkills) {
			return "slash commands are already enabled."
		}
		// Expose loaded slash commands to the model via slash_command. In economy
		// mode skills join this list only after the skills source is enabled.
		var slashEntries []command.SlashEntry
		if includeSkills && implicitSkillInvocation {
			for _, sk := range skillStore.SlashList() {
				slashEntries = append(slashEntries, command.SlashEntry{
					Name:        sk.SlashName(),
					Description: sk.Description,
					Render:      func(args []string) string { return skillStore.Render(sk, strings.Join(args, " ")) },
				})
			}
		}
		for _, cmd := range cmds {
			if cmd.Hidden {
				continue
			}

			slashEntries = append(slashEntries, command.SlashEntry{
				Name:        cmd.Name,
				Description: cmd.Description,
				ArgHint:     cmd.ArgHint,
				Render:      func(args []string) string { return cmd.Render(args) },
			})
		}
		reg.Add(command.NewSlashCommandTool(slashEntries))
		slashCommandAdded = true
		slashCommandIncludesSkills = slashCommandIncludesSkills || includeSkills
		return "enabled slash_command."
	}
	installSourceAdded := false
	addInstallSourceTool := func() string {
		if installSourceAdded {
			return "install_source is already enabled."
		}
		installSourceAdded = true
		reg.Add(installsource.NewTool(installsource.Options{
			ProjectRoot: root,
			HTTPClient:  balanceClient,
			ConnectMCP: func(e config.PluginEntry) (installsource.MCPConnectResult, error) {
				spec := pluginSpecFromEntryWithOptions(e, root, pluginSpecOptions)
				if opts.Stderr != nil {
					spec.Stderr = opts.Stderr
				}
				// Applying an install plan is already an explicit user decision.
				// Project-scoped installs retain project provenance, but record the
				// exact durable launch grant now so neither this connection nor the
				// next session asks the user to authorize the same install again.
				launchAuthorized := false
				if spec.RequireLaunchApproval {
					if err := plugin.AuthorizeSpecLaunch(ctx, spec); err != nil {
						return installsource.MCPConnectResult{}, err
					}
					launchAuthorized = true
				}
				tools, err := pluginHost.Add(ctx, spec)
				if err != nil {
					// The install did not complete, so do not retain consent for a
					// server that never connected. Replacement rollback reauthorizes
					// the previous project entry before reconnecting it.
					if launchAuthorized && spec.LaunchManager != nil {
						_ = spec.LaunchManager.Revoke(spec.Name)
					}
					return installsource.MCPConnectResult{}, err
				}
				reg.RemovePrefix(plugin.ToolPrefix(spec.Name))
				for _, t := range tools {
					reg.Add(t)
				}
				// Disconnect closes the server and drops its namespaced tools.
				// Used by the install_source rollback path when SaveTo fails.
				disconnect := func() {
					if prefix, ok := pluginHost.Remove(spec.Name); ok {
						reg.RemovePrefix(prefix)
					}
					if spec.LaunchManager != nil {
						_ = spec.LaunchManager.Revoke(spec.Name)
					}
				}
				return installsource.MCPConnectResult{
					ToolCount:  len(tools),
					Disconnect: disconnect,
				}, nil
			},
			OnDisconnect: func(serverName string) bool {
				if prefix, ok := pluginHost.Remove(serverName); ok {
					reg.RemovePrefix(prefix)
					return true
				}
				return false
			},
		}))
		return "enabled install_source."
	}
	readOnlySkillToolsAdded := false
	addReadOnlySkillTools := func() string {
		if !implicitSkillInvocation {
			return "automatic skill invocation is disabled; use an explicit /skill command instead."
		}
		if readOnlySkillToolsAdded {
			return "read_only_skill tool is already enabled.\n\n" + skill.ReadOnlyIndexBlock(skills)
		}
		readOnlySkillToolsAdded = true
		reg.Add(skill.NewReadOnlySkillTool(skillStore, gateSubagentArm(opts.Ablation, readOnlySkillRunner), skillProfile))
		return "enabled read_only_skill. Use read_only_skill for inline skills or read-only subagent skills on the next model request.\n\n" + skill.ReadOnlyIndexBlock(skills)
	}
	skillToolsAdded := false
	addSkillTools := func() string {
		if !implicitSkillInvocation {
			return "automatic skill invocation is disabled; use an explicit /skill command instead."
		}
		if skillToolsAdded {
			return "skills are already enabled.\n\n" + skill.IndexBlock(skills)
		}
		skillToolsAdded = true
		addReadOnlySkillTools()
		reg.Add(skill.NewRunSkillTool(skillStore, gateSubagentArm(opts.Ablation, skillRunner), skillProfile))
		reg.Add(skill.NewReadSkillTool(skillStore))
		reg.Add(skill.NewInstallSkillTool(skillStore, nil))
		for _, t := range builtinSubagentTools(opts.Ablation, skillStore, skillRunner, skillProfile) {
			reg.Add(t)
		}
		addSlashCommandTool(implicitSkillInvocation)
		return "enabled skills. Use run_skill/read_skill/read_only_skill or the dedicated skill tools on the next model request.\n\n" + skill.IndexBlock(skills)
	}
	addInstallSourceTool()
	if implicitSkillInvocation {
		addSkillTools()
	} else {
		addSlashCommandTool(false)
	}

	// Session-shared MCP runtime: Host, specs, and connection snapshots. Each
	// agent gets its own use_capability frontend (ledger/audit isolation) while
	// reusing processes. Delivery puts a frontend on the executor registry;
	// dual-model Planner and all task/fleet sub-agents get their own frontends
	// without inheriting dynamic mcp__* schemas.
	var capLedger *capability.Ledger
	var capAudit *capability.Audit
	capEntries, capSpecs := capabilityServerInventory(cfg.Plugins, root, pluginSpecOptions, extraSpecs, enabledMCPNames)
	cachedTools, cacheKeyOK := capability.LoadCachedToolsForSpecs(capSpecs, pluginHost.Profile())
	skillStore.ConfigureToolBindings(func(sk skill.Skill) []tool.MCPBinding {
		return skillMCPBindings(sk, reg, capSpecs, cachedTools, cacheKeyOK)
	})
	var capProxy *agent.UseCapabilityTool
	// Catalog closes over capRuntime so proxy-connected tools stay routable.
	// Use AllContractEntries so tool: capabilities include non-provider-visible
	// tools that use_capability can still dispatch.
	catalogFn := func() capability.Catalog {
		conn := map[string]bool{}
		failedNow := map[string]string{}
		if pluginHost != nil {
			for _, n := range pluginHost.ServerNames() {
				conn[n] = true
			}
			for _, failure := range pluginHost.Failures() {
				failedNow[failure.Name] = failure.Error
			}
		}
		catOpts := capability.CatalogOptions{
			Tools:       reg.AllContractEntries(),
			Skills:      skillStore.List(),
			Plugins:     cfg.Plugins,
			Connected:   conn,
			Failed:      failedNow,
			CachedTools: cachedTools,
			CacheKeyOK:  cacheKeyOK,
		}
		if capRuntime != nil {
			catOpts.Plugins, catOpts.CachedTools, catOpts.CacheKeyOK, catOpts.Disabled, catOpts.ProxyTools = capRuntime.CapabilityCatalogState()
		}
		return capability.BuildCatalog(catOpts)
	}
	// Always build the capability runtime and provider-visible use_capability
	// proxy so all three role settings share one tool schema.
	capRuntime = agent.NewMCPCapabilityRuntime(ctx, pluginHost, capSpecs, reg, catalogFn)
	capRuntime.ConfigureServers(capEntries, capSpecs, enabledMCPNames)
	capLedger = capability.NewLedger()
	capAudit = &capability.Audit{}
	capProxy = capRuntime.NewFrontend(capLedger, capAudit)
	reg.Add(capProxy)
	skillStore.ConfigureInvocationPolicy("", func(requires []string) []string {
		connected := map[string]bool{}
		failedNow := map[string]string{}
		if pluginHost != nil {
			for _, name := range pluginHost.ServerNames() {
				connected[name] = true
			}
			for _, failure := range pluginHost.Failures() {
				failedNow[failure.Name] = failure.Error
			}
		}
		catOpts := capability.CatalogOptions{
			Tools:       reg.AllContractEntries(),
			Skills:      skillStore.List(),
			Plugins:     cfg.Plugins,
			Connected:   connected,
			Failed:      failedNow,
			CachedTools: cachedTools,
			CacheKeyOK:  cacheKeyOK,
		}
		if capRuntime != nil {
			catOpts.Plugins, catOpts.CachedTools, catOpts.CacheKeyOK, catOpts.Disabled, catOpts.ProxyTools = capRuntime.CapabilityCatalogState()
		}
		catalog := capability.BuildCatalog(catOpts)
		_, missing := catalog.RequiresReady(requires)
		return missing
	})

	execSess := newObservedSession(sysPrompt)
	executor := agent.New(execProv, reg, execSess, agent.Options{
		ImageInput:   imageConfig,
		MaxSteps:     maxSteps,
		MaxStepsKey:  opts.MaxStepsKey,
		Temperature:  cfg.Agent.Temperature,
		TaskBudget:   taskBudgetFromConfig(cfg),
		Pricing:      entry.Price,
		QuoteContext: quoteCtx,
		ModelRef:     modelRef,
		Gate:         headlessGate,
		Hooks:        hookRunner,
		Jobs:         jm,
		// Parent write reservation at the executor entry covers all writers
		// (including late Economy/MCP adds) without wrapping tool schemas.
		WriteScheduler:               subagentScheduler,
		WriteWorkspaceRoot:           root,
		SessionTemp:                  sessionTemp,
		WriteRoots:                   writeRootSet,
		HomeDir:                      userHomeDir(),
		StateRoot:                    config.MemoryUserDir(),
		ProjectChecks:                projectChecks,
		Ablation:                     opts.Ablation,
		WorkspaceLease:               workspaceLease,
		CapabilityLedger:             capLedger,
		CapabilityAudit:              capAudit,
		ContextWindow:                entry.ContextWindow,
		MaxOutputTokens:              entry.MaxOutputTokens,
		SoftCompactRatio:             cfg.Agent.SoftCompactRatio,
		ToolResultSnipRatio:          cfg.Agent.ToolResultSnipRatio,
		CompactRatio:                 cfg.Agent.CompactRatio,
		CompactForceRatio:            cfg.Agent.CompactForceRatio,
		ContextEditing:               cfg.Agent.ContextEditing,
		RecentKeep:                   cfg.Agent.RecentKeep,
		ArchiveDir:                   config.ArchiveDir(),
		KeepPolicy:                   keepPolicy,
		ReasoningLanguage:            config.ReasoningLanguageForEntry(entry, cfg.ReasoningLanguage()),
		PlanModeReadOnlyCommands:     cfg.Agent.PlanModeReadOnlyCommands,
		LegacyAnchorSafetyGate:       cfg.Agent.LegacyAnchorSafetyGate,
		SubagentDepth:                0,
		MaxSubagentDepth:             maxSubagentDepth,
		MissingReasoningWarnStateDir: config.MissingReasoningWarnStateDir(),
	}, sink)
	reg.Add(sessiontool.NewSetSessionTitleTool(sessionDir, executor.SessionPath, opts.OnSessionTitleChanged))

	var runner agent.Runner = executor
	label := entry.Model
	// Two-model collaboration: a distinct planner_model wraps the executor in a
	// Coordinator with its own session, kept separate for cache stability. The
	// planner gets the same standing memory context and a filtered read-only
	// research tool set, so it can inspect rules/code without side effects.
	pm := effectivePlannerModel(cfg, opts)
	pe, plannerResolved := resolveOptionalEntry(effectiveResolver, cfg, pm)
	if pm != "" && !plannerResolved {
		return nil, fmt.Errorf("planner_model %q is not a configured provider", pm)
	}
	if pm != "" && plannerResolved {
		plannerProv, err := resolveProvider(effectiveResolver, cfg, proxySpec, provider.Selection{Ref: modelRefFromEntry(pe)})
		if err != nil {
			return nil, fmt.Errorf("planner_model %q: %w", pm, err)
		}
		plannerContext := mem.SystemBlock()
		if implicitSkillInvocation {
			plannerContext = strings.TrimSpace(plannerContext + "\n\n" + skill.ReadOnlyInvocationPolicyBlock())
		}
		plannerSess := agent.NewSession(agent.PlannerPromptWithContext(plannerContext))
		// Planner owns an independent ledger/audit and use_capability frontend
		// so its MCP calls cannot satisfy or poison Executor Delivery gates.
		plannerLedger := capability.NewLedger()
		plannerAudit := &capability.Audit{}
		plannerTools := agent.PlannerToolRegistry(reg)
		if capRuntime != nil {
			// Replace any cloned parent frontend with one bound to the
			// planner ledger (PlannerToolRegistry clones with nil ledger).
			if _, ok := plannerTools.Get("use_capability"); ok {
				plannerTools.RemovePrefix("use_capability")
			}
			plannerTools.Add(capRuntime.NewFrontend(plannerLedger, plannerAudit))
		}
		plannerOpts := agent.Options{
			ImageInput:                   imageConfig,
			MaxSteps:                     0,
			Gate:                         headlessGate,
			ModelRef:                     modelRefFromEntry(pe),
			QuoteContext:                 quoteCtx,
			ContextWindow:                pe.ContextWindow,
			SoftCompactRatio:             cfg.Agent.SoftCompactRatio,
			ToolResultSnipRatio:          cfg.Agent.ToolResultSnipRatio,
			CompactRatio:                 cfg.Agent.CompactRatio,
			CompactForceRatio:            cfg.Agent.CompactForceRatio,
			ContextEditing:               cfg.Agent.ContextEditing,
			RecentKeep:                   cfg.Agent.RecentKeep,
			ArchiveDir:                   config.ArchiveDir(),
			KeepPolicy:                   keepPolicy,
			ReasoningLanguage:            config.ReasoningLanguageForEntry(pe, cfg.ReasoningLanguage()),
			PlanModeReadOnlyCommands:     cfg.Agent.PlanModeReadOnlyCommands,
			CapabilityLedger:             plannerLedger,
			CapabilityAudit:              plannerAudit,
			MissingReasoningWarnStateDir: config.MissingReasoningWarnStateDir(),
			WriteRoots:                   writeRootSet,
			HomeDir:                      userHomeDir(),
			StateRoot:                    config.MemoryUserDir(),
		}
		runner = agent.NewCoordinatorWithPlannerPolicy(plannerProv, plannerSess, pe.Price, plannerTools, plannerOpts, executor, cfg.Agent.Temperature, sink, control.NewPlannerPolicy())
		label = entry.Model + " + planner " + pe.Model
	}
	imageEnabled := modelCapabilities.Resolve(entry).State == config.CapabilitySupported
	if infoProvider, ok := execProv.(provider.ModelInfoProvider); ok {
		imageEnabled = infoProvider.ModelInfo().SupportsInput(provider.ModalityImage)
	}
	imageSnapshot := config.ModelCapabilitySnapshot(cfg, modelCapabilities)
	ctrlOpts := control.Options{
		ModelSettingsRevision:          cfg.ModelRuntimeFingerprint(modelRef),
		ModelSettingsCurrent:           runtimeModelSettingsReader(root, modelName, modelRef, opts.ModelSettings),
		FrozenImageInput:               &imageEnabled,
		ImageCapabilityChanged:         runtimeImageCapabilityReader(root, modelName, imageSnapshot, opts.ModelSettings),
		TaskBudget:                     taskBudgetFromConfig(cfg),
		GoalTokenBudget:                cfg.Agent.GoalTokenBudget,
		Runner:                         runner,
		Executor:                       executor,
		Sink:                           sink,
		Policy:                         policy,
		SubagentGate:                   headlessGate,
		Label:                          label,
		ModelRef:                       modelRef,
		VisionModel:                    cfg.Agent.VisionModel,
		VisionProviderResolver:         visionProviderResolver,
		VisionModelSelector:            visionModelSelector,
		ModelCapabilityResolver:        modelCapabilities.Resolve,
		SystemPrompt:                   sysPrompt,
		PinnedContextLoader:            opts.PinnedContextLoader,
		SessionDir:                     sessionDir,
		Host:                           pluginHost,
		Commands:                       cmds,
		Skills:                         skills,
		AllSkills:                      allSkills,
		SkillStore:                     skillStore,
		AllSkillStore:                  allSkillStore,
		DisableImplicitSkillInvocation: !implicitSkillInvocation,
		SkillRunner:                    skillRunner,
		ReadOnlySkillRunner:            readOnlySkillRunner,
		SkillProfile:                   skillProfile,
		Hooks:                          hookRunner,
		Memory:                         mem,
		// Indirection: the cleanup variable gains the extension runtime set at
		// the end of build (snapshot assembly runs after control.New), and the
		// controller must observe the final chain at Close time.
		Cleanup:               func() { cleanup() },
		BalanceURL:            entry.BalanceURL,
		BalanceKey:            entry.APIKey(),
		BalanceClient:         balanceClient,
		Jobs:                  jm,
		TaskStore:             opts.TaskStore,
		WorkspaceLease:        workspaceLease,
		Registry:              reg,
		PluginCtx:             ctx,
		MCPDefaultCallTimeout: pluginSpecOptions.DefaultCallTimeout,
		MCPConfigureSpec: func(spec *plugin.Spec) {
			if spec == nil {
				return
			}
			spec.LaunchManager = pluginSpecOptions.LaunchManager
			if strings.TrimSpace(spec.ConfigSource) == "" {
				spec.ConfigSource = pluginSpecOptions.ConfigSource
			}
			if spec.DefaultStartupTimeout <= 0 {
				spec.DefaultStartupTimeout = pluginSpecOptions.DefaultStartupTimeout
			}
			applyMCPIsolation(spec, root, pluginSpecOptions)
		},
		CapabilityRuntime:      capRuntime,
		WorkspaceRoot:          root,
		ExternalFolderToolRefs: readPathResolver,
		ResponseLanguage:       cfg.ResponseLanguage(),
		ReasoningLanguage:      config.ReasoningLanguageForEntry(entry, cfg.ReasoningLanguage()),
		SessionContextStatic:   sessionContextStatic,
		DisableColdResumePrune: !cfg.ColdResumePruneEnabled(),
		FileBranchesOnly:       opts.FileBranchesOnly,
		Shell:                  shell,
		ApprovalTimeout:        opts.ApprovalTimeout,
		Ablation:               opts.Ablation,
		WriteRoots:             writeRootSet,
		BashSandboxEnforced:    bashSpec.Enforce() && sandbox.Available(),
		OnPersistWriteAccess:   projectWriteAccessPersister(root),
		OnRemember: func(rule string) control.RememberResult {
			return rememberPermissionRule(root, rule)
		},
		OnRememberPlanModeReadOnlyCommand: func(prefix string) control.PlanModeReadOnlyCommandTrustResult {
			return rememberPlanModeReadOnlyCommand(root, prefix)
		},
		SessionRecoveryMeta: opts.SessionRecoveryMeta,
		OnSessionRecovered:  opts.OnSessionRecovered,
		OnSessionTransition: opts.OnSessionTransition,
		BeforeInboxDispatch: opts.BeforeInboxDispatch,
		// The merged catalog lets frontends enumerate sidecar providers.
		ProviderResolver:  extensionResolver,
		RuntimeGeneration: generation,
		RuntimeOwner:      owner,
		// Share the Manager already bound into bash/grep so tools and the
		// Controller observe the same temporary generation across rebuilds.
		SessionTemp: sessionTemp,
	}
	if opts.ModelSettings != nil {
		ctrlOpts.ModelSettingsSourceRevision = opts.ModelSettings.Revision
	}
	// Guardian: when guardian_model is configured, spawn an LLM safety reviewer
	// that can auto-allow safe Ask decisions and annotate risky ones before
	// escalating to the human approval prompt.
	if guardianModel := cfg.Agent.GuardianModel; guardianModel != "" {
		ge, ok := resolveOptionalEntry(effectiveResolver, cfg, guardianModel)
		if !ok {
			return nil, fmt.Errorf("guardian_model %q is not a configured provider", guardianModel)
		}
		pProv, err := resolveProvider(effectiveResolver, cfg, proxySpec, provider.Selection{Ref: modelRefFromEntry(ge)})
		if err != nil {
			return nil, fmt.Errorf("guardian_model %q: %w", guardianModel, err)
		}
		guardianReg := agent.FilterReadOnlyRegistry(reg, agent.SubagentMetaTools()...)
		ctrlOpts.Guardian = guardian.NewSession(pProv, guardianReg, guardian.PolicyPrompt(), modelRefFromEntry(ge), cfg.Agent.GuardianTemperature, ge.Price, sink)
		sink.Emit(event.Event{Kind: event.Notice, Level: event.LevelInfo, Text: fmt.Sprintf("guardian enabled · model=%s", ge.Model)})
	}
	// Recovery reviewer is explicit: empty recovery_model leaves rule-only
	// recovery. A configured but unusable model is a configuration error.
	if recoveryModel := strings.TrimSpace(cfg.Agent.RecoveryModel); recoveryModel != "" {
		if extensionResolver != nil && providerext.PluginRefOwner(recoveryModel) != "" {
			re, ok := resolveOptionalEntry(extensionResolver, cfg, recoveryModel)
			if !ok {
				return nil, fmt.Errorf("recovery_model %q is not a configured provider", recoveryModel)
			}
			rProv, err := extensionResolver.Resolve(provider.Selection{Ref: modelRefFromEntry(re)})
			if err != nil {
				return nil, fmt.Errorf("recovery_model %q: %w", recoveryModel, err)
			}
			ctrlOpts.RecoveryReviewer = recovery.NewSessionWithSink(rProv, re.Price, modelRefFromEntry(re), sink)
		} else {
			re, ok := cfg.ResolveModel(recoveryModel)
			if !ok {
				return nil, fmt.Errorf("recovery_model %q is not a configured provider", recoveryModel)
			}
			rProv, err := NewProviderWithProxy(re, proxySpec)
			if err != nil {
				return nil, fmt.Errorf("recovery_model %q: %w", recoveryModel, err)
			}
			ctrlOpts.RecoveryReviewer = recovery.NewSessionWithSink(rProv, re.Price, modelRefFromEntry(re), sink)
		}
	}
	// HeadlessApprovalMode is an explicit declaration that this frontend has
	// no decision channel (`reasonix run`). ApprovalTimeout is not a proxy for
	// that capability: bots have a bounded timeout and can still answer cards.
	ctrlOpts.RecoveryHeadless = recoveryHeadlessMode(opts)
	// Goal evaluator is not implied by the main model, guardian, or recovery
	// reviewer. Controllers that want one inject it explicitly; otherwise Goal
	// uses the deterministic host policy.
	ctrl := control.New(ctrlOpts)
	// The role inputs set the session quality floor: delivery/deliver/quality
	// raise it, light and its aliases fold to standard, unknown stays default.
	if p, err := agentpreset.Normalize(firstNonEmpty(opts.AgentPreset, opts.TokenMode)); err == nil && p == agentpreset.Delivery {
		_ = ctrl.SetQualityFloor(string(p))
	}
	// Publish the controller to the extension UI hub's indirection: from here
	// on, host/ui/* publishes ride ctrl.EmitExtensionEvent and blocking prompts
	// ride ctrl.Ask, exactly as if the hub had been built after control.New.
	ctrlRef.Store(ctrl)
	close(controllerReady)
	// Share the recovery checkpoint with task/fleet sub-agents so background
	// writers observe the same failure state as the root agent.
	if taskTool != nil {
		if g := ctrl.Executor(); g != nil {
			taskTool.WithRecoveryGate(g.RecoveryGate())
		}
	}
	if capRuntime != nil {
		ctrl.SetCapabilityProxyTools(capRuntime.ConnectedProxyTools)
	}
	// Task tools created before capRuntime assignment still need the runtime if
	// they were built early; re-bind when present.
	if taskTool != nil && capRuntime != nil {
		taskTool.WithCapabilityRuntime(capRuntime)
	}
	// Build one role-neutral semantic router so an in-place switch never needs a
	// controller rebuild. Host constraints and live capability routing decide whether a turn may call
	// it; construction alone does not add a provider request.
	var router *capability.SemanticRouter
	if modelRef := strings.TrimSpace(cfg.Agent.SubagentModels["capability-router"]); modelRef != "" {
		effortRef := strings.TrimSpace(cfg.Agent.SubagentEfforts["capability-router"])
		if p, price, _, err := resolveSubagentProvider(modelRef, effortRef); err == nil && p != nil {
			usageModelRef, _ := subagentIdentity(modelRef, effortRef)
			router = &capability.SemanticRouter{Provider: p, Sink: sink, Model: usageModelRef, Pricing: price, QuoteContext: quoteCtx, Audit: capAudit}
		}
	}
	if router == nil {
		router = &capability.SemanticRouter{Provider: execProv, Sink: sink, Model: modelRef, Pricing: entry.Price, QuoteContext: quoteCtx, Audit: capAudit}
	}
	ctrl.WireCapabilityRouting(cfg.Plugins, capSpecs, router, capAudit)
	ctrl.SetCapabilityProxyRouting(true)

	// Provider-visible tool surface is identical for every role setting before
	// the extension snapshot freezes registry schemas for cache diagnostics.
	applyUnifiedProviderToolSurface(reg)

	// Freeze the extension kernel's snapshot of exactly what this build wired.
	// The snapshot is assembled from the in-hand objects above — discovery
	// never re-runs — and assembly must never fail the boot: a kernel error
	// degrades to a nil snapshot (logged) while the controller behaves exactly
	// as before. The sidecar Manager comes from preflight (started once,
	// before model resolution); assembly takes over its ownership and freezes
	// the same generation the sidecars were handshaken with. The frozen
	// provider catalog is the BASE catalog, exactly as before the preflight
	// refactor: sidecar providers enter the snapshot through the Manager's own
	// contributions, not through the legacy provider list.
	mcpSpecs := enabledMCPSpecs(configSpecs, extraSpecs)
	snap, runtimeSet, extensionDispatcher, snapErr := assembleLegacySnapshot(ctx, legacyAssembly{
		systemPrompt: sysPrompt,
		registry:     reg,
		skills:       skills,
		commands:     cmds,
		hooks:        resolvedHooks,
		mcpSpecs:     mcpSpecs,
		providers:    baseResolver.Catalog(),
	}, generation, extensionBoot{
		session:            protocol.SessionContext{SessionID: sessionID, WorkspaceRoot: root, Provider: entry.Kind, Model: modelRef, Generation: generation},
		ui:                 extUIHub,
		onWarning:          extWarn,
		skipPromptStrategy: shouldSkipPromptStrategy(opts.PreviousPlan),
		previousDispatcher: opts.PreviousDispatcher,
	}, extensionMgr)
	// Ownership of the preflighted Manager transferred to assembly on every
	// path: it was either closed inside or registered into the RuntimeSet.
	pendingMgr = nil
	if snapErr != nil {
		// These assembly failures are fatal rather than degradable: two
		// runtimes claiming the same replacement slot (the kernel's
		// ReplaceClaims verdict) and a failed system_prompt.build strategy
		// ruling (the slot owner is required-class, so dispatch surfaces its
		// failure as one of these types) mean the extension contract the user
		// installed cannot be honored; booting without it would silently
		// change what the session is. (A required runtime that cannot start
		// fails earlier, in preflight, with the same fatality.)
		var requiredErr *sidecar.RequiredStartError
		var slotErr *extension.SlotConflictError
		var blockErr *dispatch.BlockError
		var failureErr *dispatch.FailureError
		var violationErr *dispatch.ViolationError
		if errors.As(snapErr, &requiredErr) || errors.As(snapErr, &slotErr) ||
			errors.As(snapErr, &blockErr) || errors.As(snapErr, &failureErr) || errors.As(snapErr, &violationErr) {
			ctrl.ReleaseResources()
			return nil, fmt.Errorf("boot: %w", snapErr)
		}
		slog.Warn("boot: extension snapshot assembly failed; continuing without a runtime snapshot", "err", snapErr)
		runtimeSet = extension.NewRuntimeSet(generation)
		// Assembly retired the preflighted Manager on the error path; the
		// controller must not bind a hub or expose a manager whose sidecars
		// are already shut down.
		extensionMgr = nil
	}
	// The stage-7 provider merge happened at preflight, before model
	// resolution; BuildResult.ProviderResolver exposes that same merged
	// resolver (the base when no sidecar declared providers).
	providerResolver := baseResolver
	if extensionResolver != nil {
		providerResolver = extensionResolver
	}
	cleanup = wireRuntimeScopeCleanup(runtimeSet, cleanup, opts.SharedHost, pluginHost, lspMgr, opts.SessionTemp)
	ctrl.SetExtensions(extensionDispatcher)
	if extensionMgr == nil {
		extUIHub = nil
	} else {
		ctrl.SetExtensionUI(extUIHub)
	}
	if providerResolver != nil {
		ctrl.SetProviderResolver(providerResolver)
	}
	// Stage 6b2 system-prompt handoff: the 6b1 strategy pass may have replaced
	// the prompt while the snapshot was freezing, but the executor session was
	// built earlier with the host-composed prompt. Swap in a fresh session
	// carrying the final prompt now — before any turn or history resume, so
	// the live session and the frozen snapshot describe the same session.
	if snap != nil {
		if final := snap.SystemPrompt(); final != sysPrompt {
			ctrl.ApplyExtensionSystemPrompt(final)
		}
	}
	assembly := &ReusedAssembly{
		SystemPrompt:            sysPrompt,
		Skills:                  skills,
		Commands:                cmds,
		Hooks:                   resolvedHooks,
		Registry:                reg,
		ImplicitSkillInvocation: implicitSkillInvocation,
	}
	return finalizeBuildResult(&BuildResult{Controller: ctrl, Snapshot: snap, Runtime: runtimeSet, Owner: owner, Extensions: extensionMgr, Dispatcher: extensionDispatcher, ExtensionUI: extUIHub, ProviderResolver: providerResolver, BaseProviderResolver: baseResolver, Assembly: assembly}, !opts.deferPublish), nil
}

// applyUnifiedProviderToolSurface restricts Schemas/ContractEntries to the
// shared core + host-control tools. use_capability can still Get every
// registered tool, including those hidden from the provider schema.
func applyUnifiedProviderToolSurface(reg *tool.Registry) {
	if reg == nil {
		return
	}
	allow := make([]string, 0, 16)
	for _, name := range UnifiedProviderToolNames() {
		if _, ok := reg.Get(name); ok {
			allow = append(allow, name)
		}
	}
	// Always keep use_capability if somehow only that remains.
	if len(allow) == 0 {
		if _, ok := reg.Get("use_capability"); ok {
			allow = []string{"use_capability"}
		}
	}
	reg.SetProviderVisibleTools(allow)
}

// effectivePlannerModel centralizes planner precedence. Every role setting
// builds the configured planner so later in-place switches retain the same
// runtime; explicit Plan, approval, or Goal start decides whether it is invoked.
func effectivePlannerModel(cfg *config.Config, opts Options) string {
	if cfg == nil || opts.Ablation.Off(ablation.Planner) {
		return ""
	}
	return strings.TrimSpace(cfg.Agent.PlannerModel)
}

func rememberPermissionRule(workspaceRoot, rule string) control.RememberResult {
	path := rememberPermissionConfigPath(workspaceRoot)
	result := control.RememberResult{Rule: strings.TrimSpace(rule), Path: path}
	unlock, err := config.LockConfigFileEdits(path)
	if err != nil {
		slog.Warn("lock config for permission rule", "path", path, "err", err)
		result.Err = err
		return result
	}
	defer unlock()

	edit, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		slog.Warn("load config for permission rule", "path", path, "err", err)
		result.Err = err
		return result
	}
	if coveredBy := coveredPermissionRule(edit.Permissions.Allow, result.Rule); coveredBy != "" {
		result.CoveredBy = coveredBy
		return result
	}
	edit.Permissions.Allow = pruneCoveredPermissionRules(edit.Permissions.Allow, result.Rule)
	if err := edit.AddPermissionRule("allow", rule); err != nil {
		slog.Warn("persist permission rule", "rule", rule, "err", err)
		result.Err = err
		return result
	}
	if err := config.WritePermissionsAllow(path, edit.Permissions.Allow); err != nil {
		slog.Warn("save config after permission rule", "err", err)
		result.Err = err
		return result
	}
	result.Saved = true
	return result
}

func rememberPermissionConfigPath(workspaceRoot string) string {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot != "" {
		return filepath.Join(workspaceRoot, "reasonix.toml")
	}
	path := config.SourcePath()
	if path == "" {
		path = "reasonix.toml" // match Config.Save() fallback
	}
	return path
}

func rememberPlanModeReadOnlyCommand(workspaceRoot, prefix string) control.PlanModeReadOnlyCommandTrustResult {
	prefix = strings.TrimSpace(prefix)
	path := rememberPermissionConfigPath(workspaceRoot)
	result := control.PlanModeReadOnlyCommandTrustResult{Prefix: prefix, Path: path}
	if prefix == "" {
		result.Err = fmt.Errorf("empty plan-mode read-only command prefix")
		return result
	}
	unlock, err := config.LockConfigFileEdits(path)
	if err != nil {
		result.Err = err
		return result
	}
	defer unlock()
	edit, err := config.LoadForEditReadOnlyStrict(path)
	if err != nil {
		result.Err = err
		return result
	}
	if coveredBy := coveredPlanModeReadOnlyCommand(edit.Agent.PlanModeReadOnlyCommands, prefix); coveredBy != "" {
		result.CoveredBy = coveredBy
		return result
	}
	edit.Agent.PlanModeReadOnlyCommands = append(edit.Agent.PlanModeReadOnlyCommands, prefix)
	if err := edit.SaveTo(path); err != nil {
		slog.Warn("persist plan-mode read-only command trust", "prefix", prefix, "err", err)
		result.Err = err
		return result
	}
	result.Saved = true
	return result
}

func coveredPlanModeReadOnlyCommand(existing []string, candidate string) string {
	candidateFields := strings.Fields(strings.TrimSpace(candidate))
	if len(candidateFields) == 0 {
		return ""
	}
	for _, item := range existing {
		itemFields := strings.Fields(strings.TrimSpace(item))
		if len(itemFields) == 0 || len(itemFields) > len(candidateFields) {
			continue
		}
		matches := true
		for i, field := range itemFields {
			if candidateFields[i] != field {
				matches = false
				break
			}
		}
		if matches {
			return strings.Join(itemFields, " ")
		}
	}
	return ""
}

func coveredPermissionRule(rules []string, rule string) string {
	for _, existing := range rules {
		if permission.RuleCoversString(existing, rule) {
			return strings.TrimSpace(existing)
		}
	}
	return ""
}

func pruneCoveredPermissionRules(rules []string, rule string) []string {
	out := rules[:0]
	for _, existing := range rules {
		if strings.TrimSpace(existing) == "" || permission.RuleCoversString(rule, existing) {
			continue
		}
		out = append(out, existing)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func subagentModelRef(cfg *config.Config, sk skill.Skill) string {
	if cfg != nil {
		for _, key := range SubagentModelKeys(sk.Name) {
			if m := strings.TrimSpace(cfg.Agent.SubagentModels[key]); m != "" {
				return m
			}
		}
	}
	if m := strings.TrimSpace(sk.Model); m != "" {
		return m
	}
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Agent.SubagentModel)
}

func subagentEffortRef(cfg *config.Config, sk skill.Skill) string {
	if cfg != nil {
		for _, key := range SubagentModelKeys(sk.Name) {
			if e := strings.TrimSpace(cfg.Agent.SubagentEfforts[key]); e != "" {
				return e
			}
		}
	}
	if e := strings.TrimSpace(sk.Effort); e != "" {
		return e
	}
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Agent.SubagentEffort)
}

// SubagentModelKeys returns the cfg.Agent.SubagentModels/SubagentEfforts map
// keys that resolve for a subagent name, in precedence order: the exact name
// first, then its underscore/hyphen alias variants (the dedicated tool
// security_review dispatches the skill security-review, so either spelling in
// config must reach it). Any surface that reads OR clears these maps must
// iterate this same key set — an exact-key delete leaves an alias entry
// silently active.
func SubagentModelKeys(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	keys := []string{name}
	for _, alias := range []string{
		strings.ReplaceAll(name, "-", "_"),
		strings.ReplaceAll(name, "_", "-"),
	} {
		if alias == "" {
			continue
		}
		seen := slices.Contains(keys, alias)
		if !seen {
			keys = append(keys, alias)
		}
	}
	return keys
}

func currentWorkspacePromptLine(root string) string {
	if root == "" {
		return ""
	}
	return "Current workspace: " + strconv.Quote(root)
}

func resolveWorkspaceRoot(explicit string) string {
	if explicit != "" {
		return explicit
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if root, ok := nearestGitRoot(wd); ok {
		return root
	}
	return wd
}

func normalizeAdditionalDirs(root string, dirs []string) ([]string, error) {
	if len(dirs) == 0 {
		return nil, nil
	}
	base := strings.TrimSpace(root)
	if base == "" {
		base = "."
	}
	if !filepath.IsAbs(base) {
		abs, err := filepath.Abs(base)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace root: %w", err)
		}
		base = abs
	}

	var out []string
	for _, raw := range dirs {
		dir := strings.TrimSpace(raw)
		if dir == "" {
			continue
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(base, dir)
		}
		dir, err := filepath.Abs(filepath.Clean(dir))
		if err != nil {
			return nil, fmt.Errorf("resolve additional directory %q: %w", raw, err)
		}
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return nil, fmt.Errorf("resolve additional directory %q: %w", raw, err)
		}
		info, err := os.Stat(real)
		if err != nil {
			return nil, fmt.Errorf("inspect additional directory %q: %w", raw, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("additional path %q is not a directory", raw)
		}
		out = appendUniquePaths(out, filepath.Clean(real))
	}
	return out, nil
}

func appendUniquePaths(base []string, extra ...string) []string {
	out := append([]string(nil), base...)
	seen := make(map[string]struct{}, len(out)+len(extra))
	for _, path := range out {
		seen[pathComparisonKey(path)] = struct{}{}
	}
	for _, path := range extra {
		path = filepath.Clean(path)
		key := pathComparisonKey(path)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, path)
	}
	return out
}

// RuntimeForbidReadRoots returns the configured deny roots plus Reasonix's
// global credential FILE when it exists. It also registers the corresponding
// credential environment names for subprocess filtering. Runtime tool
// assemblers outside Build must use this helper instead of reading the config
// roots directly.
//
// Provider and bot credentials are loaded into the parent process from this
// file, so readers, shell commands, and MCP servers must not be able to recover
// them even when the optional broad sensitive-file denylist is off. Project
// .env files retain their existing behavior.
func RuntimeForbidReadRoots(cfg *config.Config, root string) []string {
	if cfg == nil {
		return nil
	}
	secrets.RegisterCredentialEnvKeys(cfg.CredentialEnvNames())
	base := cfg.ForbidReadRootsForRoot(root)
	credentialPath := strings.TrimSpace(config.UserCredentialsPath())
	if credentialPath == "" {
		return append([]string(nil), base...)
	}
	info, err := os.Stat(credentialPath)
	if err != nil || info.IsDir() {
		return append([]string(nil), base...)
	}
	if real, err := filepath.EvalSymlinks(credentialPath); err == nil {
		credentialPath = real
	}
	return appendUniquePaths(base, credentialPath)
}

func pathComparisonKey(path string) string {
	path = filepath.Clean(path)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func nearestGitRoot(start string) (string, bool) {
	dir, err := filepath.Abs(start)
	if err != nil {
		dir = filepath.Clean(start)
	}
	for {
		if isGitMarker(filepath.Join(dir, ".git")) {
			return dir, true
		}
		next := filepath.Dir(dir)
		if next == dir {
			return "", false
		}
		dir = next
	}
}

func isGitMarker(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && (fi.IsDir() || fi.Mode().IsRegular())
}

func newSubagentStore(sessionDir string, parentLive func(sessionPath string) bool) (*agent.SubagentStore, error) {
	sessionDir = strings.TrimSpace(sessionDir)
	if sessionDir == "" {
		return nil, nil
	}
	store := agent.NewSubagentStore(filepath.Join(sessionDir, "subagents")).WithParentSessionProbe(parentLive)
	if _, err := store.CleanupStaleRunning(); err != nil {
		return nil, fmt.Errorf("cleanup stale subagents: %w", err)
	}
	return store, nil
}

func subagentEffectiveIdentity(cfg *config.Config, resolver provider.Resolver, baseModelRef string, base *config.ProviderEntry, modelRef, effort string) (string, string) {
	var entry config.ProviderEntry
	if base != nil {
		entry = *base
	}
	ref := strings.TrimSpace(modelRef)
	explicit := ref != ""
	if !explicit {
		ref = strings.TrimSpace(baseModelRef)
	}
	if explicit && cfg != nil && ref != "" {
		if resolved, ok := cfg.ResolveModel(ref); ok {
			entry = *resolved
		} else if resolved := syntheticEntryFromResolver(resolver, ref); strings.TrimSpace(resolved.Name) != "" {
			entry = *resolved
		} else {
			entry.Model = ref
		}
	} else if explicit {
		if resolved := syntheticEntryFromResolver(resolver, ref); strings.TrimSpace(resolved.Name) != "" {
			entry = *resolved
		} else {
			entry.Model = ref
		}
	} else if base == nil && ref != "" {
		if resolved := syntheticEntryFromResolver(resolver, ref); strings.TrimSpace(resolved.Name) != "" {
			entry = *resolved
		} else if cfg != nil {
			if resolved, ok := cfg.ResolveModel(ref); ok {
				entry = *resolved
			}
		}
	}
	if rawEffort := strings.TrimSpace(effort); rawEffort != "" {
		if normalized, err := config.NormalizeEffort(&entry, rawEffort); err == nil {
			entry.Effort = normalized
		} else {
			entry.Effort = rawEffort
		}
	}
	modelID := strings.TrimSpace(entry.Name)
	model := strings.TrimSpace(entry.Model)
	if modelID != "" && model != "" {
		modelID += "/" + model
	} else if model != "" {
		modelID = model
	} else if modelID == "" {
		modelID = ref
	}
	return modelID, strings.TrimSpace(config.EffectiveEffort(&entry))
}

// addBuiltins adds enabled built-in tools to reg. An empty list means all of
// them. writeRoots confines the file-writing built-ins to the workspace: after
// the (unconfined) defaults are added, each enabled writer is replaced by an
// instance bound to writeRoots (preserving registry order).
// forbidReadRoots confines the read/list/search built-ins so they cannot peek at
// the listed directories.
// When workDir is non-empty, tools resolve relative paths against it instead of
// the process cwd, enabling concurrent multi-project sessions.
// sessionGuard blocks writer-tool targets inside Reasonix's own session stores
// and makes bash warn when a command references them. managedConfig names the
// Reasonix-owned config files writable outside writeRoots after a fresh
// per-write human approval.
func addBuiltins(reg *tool.Registry, enabled, writeRoots []string, writeRootSet *sandbox.WritableRootSet, bashSpec sandbox.Spec, bashTimeout time.Duration, searchSpec builtin.SearchSpec, stderr io.Writer, workDir string, proxySpec netclient.ProxySpec, forbidReadRoots []string, readPathResolver *builtin.PathResolver, sessionGuard builtin.SessionDataGuard, managedConfig builtin.ManagedConfigPaths, overlay builtin.FileOverlay, terminal builtin.TerminalRunner, sessionTemp *sessiontemp.Manager, fileWriteReceipt func(path string, hadPrior bool, prior []byte)) {
	// If a workspace directory is set, use workspace-bound tools that resolve
	// paths relative to that directory. Otherwise fall back to the process-cwd
	// compile-time builtins.
	if workDir != "" {
		ws := builtin.Workspace{Dir: workDir, WriteRoots: writeRoots, WriteRootSet: writeRootSet, ForbidReadRoots: forbidReadRoots, Bash: bashSpec, BashTimeout: bashTimeout, Search: searchSpec, ProxySpec: proxySpec, ReadPaths: readPathResolver, SessionGuard: sessionGuard, ManagedConfig: managedConfig, FileOverlay: overlay, Terminal: terminal, SessionTemp: sessionTemp, FileWriteReceipt: fileWriteReceipt}
		for _, t := range ws.Tools(enabled...) {
			reg.Add(t)
		}
		return
	}

	if len(enabled) == 0 {
		for _, t := range tool.Builtins() {
			reg.Add(t)
		}
	} else {
		for _, name := range enabled {
			if t, ok := tool.LookupBuiltin(name); ok {
				reg.Add(t)
			} else {
				fmt.Fprintf(stderr, "warning: unknown built-in tool %q\n", name)
			}
		}
	}
	// Replace the unconfined defaults with confined instances (registry order is
	// preserved on replace): file-writers bound to the workspace, read tools
	// bound to forbid-read roots, bash to the OS sandbox, web_fetch to the proxy.
	// Only replace tools actually enabled/present.
	bashTool := builtin.ConfineBash(bashSpec, sessionGuard, bashTimeout)
	if rebound, ok := builtin.BindSessionTemp(bashTool, sessionTemp); ok {
		bashTool = rebound
	}
	searchTool := builtin.ConfineSearch(searchSpec, bashSpec, forbidReadRoots)
	if rebound, ok := builtin.BindSessionTemp(searchTool, sessionTemp); ok {
		searchTool = rebound
	}
	writers := builtin.ConfineWriters(writeRoots, sessionGuard, managedConfig)
	for i, writer := range writers {
		writers[i] = builtin.BindFileWriteReceipt(writer, fileWriteReceipt)
	}
	confined := append(writers,
		bashTool,
		searchTool,
		builtin.ConfineWebFetch(proxySpec))
	confined = append(confined, builtin.ConfineReaders(forbidReadRoots)...)
	for i, tl := range confined {
		confined[i] = builtin.BindWriteRootSet(tl, writeRootSet)
	}
	for _, t := range confined {
		if _, ok := reg.Get(t.Name()); ok {
			reg.Add(t)
		}
	}
}

// partitionByTier splits configured plugin entries into eager (block boot until
// ready) and background (placeholder + start spawn now). Entries with an empty,
// legacy lazy, or unrecognised tier land in background.
func partitionByTier(entries []config.PluginEntry) (eager, bg []config.PluginEntry) {
	for _, e := range entries {
		switch e.ResolvedTier() {
		case "eager":
			eager = append(eager, e)
		default:
			bg = append(bg, e)
		}
	}
	return eager, bg
}

// PluginSpecs maps configured plugin entries to plugin.Spec, expanding ${VAR}
// references. Exported so custom assemblers can connect the config's plugins
// alongside their own (e.g. ACP's per-session MCP servers).
func PluginSpecs(entries []config.PluginEntry) []plugin.Spec {
	return PluginSpecsForRoot(entries, "")
}

// PluginSpecsForRoot maps configured plugin entries to plugin.Spec and applies
// workspace-aware compatibility overrides for known cwd-sensitive servers.
func PluginSpecsForRoot(entries []config.PluginEntry, workspaceRoot string) []plugin.Spec {
	return PluginSpecsForRootWithOptions(entries, workspaceRoot, PluginSpecOptions{})
}

// PluginSpecsForRootWithOptions maps configured plugin entries to plugin.Spec
// and injects runtime policy such as the global MCP call timeout.
func PluginSpecsForRootWithOptions(entries []config.PluginEntry, workspaceRoot string, opts PluginSpecOptions) []plugin.Spec {
	specs := make([]plugin.Spec, len(entries))
	for i, e := range entries {
		specs[i] = pluginSpecFromEntryWithOptions(e, workspaceRoot, opts)
	}
	return specs
}

func pluginSpecFromEntryWithOptions(e config.PluginEntry, workspaceRoot string, opts PluginSpecOptions) plugin.Spec {
	e = e.ExpandedPlugin() // resolve ${VAR} / ${VAR:-default} from the environment
	configSource := strings.TrimSpace(string(e.Source))
	if configSource == "" {
		configSource = opts.ConfigSource
	}
	spec := plugin.ApplyKnownOverrides(plugin.Spec{
		Name:                  e.Name,
		Package:               strings.TrimSpace(opts.PackageOwners[e.Name]),
		Type:                  e.Type,
		Command:               e.Command,
		Args:                  e.Args,
		Env:                   e.Env,
		URL:                   e.URL,
		Headers:               e.Headers,
		DefaultStartupTimeout: opts.DefaultStartupTimeout,
		StartupTimeout:        secondsDuration(e.StartupTimeoutSeconds),
		DefaultCallTimeout:    opts.DefaultCallTimeout,
		CallTimeout:           secondsDuration(e.CallTimeoutSeconds),
		ToolTimeouts:          toolTimeoutDurations(e.ToolTimeoutSeconds),
		WorkspaceRoot:         strings.TrimSpace(workspaceRoot),
		LaunchManager:         opts.LaunchManager,
		ConfigSource:          configSource,
		Authorized:            e.Source.UserAuthorized(),
		OAuthHTTPClient:       opts.OAuthHTTPClient,
	}, workspaceRoot)
	if e.Source.ProjectScoped() && strings.TrimSpace(spec.Dir) == "" {
		spec.Dir = workspaceRoot
	}
	applyMCPIsolation(&spec, workspaceRoot, opts)
	return spec
}

func pluginPackageOwners(cfg *config.Config) map[string]string {
	out := map[string]string{}
	if cfg == nil {
		return out
	}
	for _, configured := range cfg.Plugins {
		if owner, ok := cfg.PluginPackageOwner(configured.Name); ok {
			out[configured.Name] = owner
		}
	}
	return out
}

func skillMCPBindings(sk skill.Skill, reg *tool.Registry, specs []plugin.Spec, cachedTools map[string][]plugin.CachedTool, cacheKeyOK map[string]bool) []tool.MCPBinding {
	var out []tool.MCPBinding
	liveServers := map[string]bool{}
	if reg != nil {
		bindings := reg.MCPBindings()
		out = make([]tool.MCPBinding, 0, len(bindings))
		for _, binding := range bindings {
			liveServers[binding.Server] = true
		}
		out = append(out, skill.ToolBindingsForSkill(sk, bindings)...)
	}
	// A valid cached schema also supplies stable bindings for an on-demand
	// package server before it is connected. The skill can then route through
	// use_capability without inventing Reasonix's canonical name.
	for _, spec := range specs {
		if spec.Package != sk.Plugin || liveServers[spec.Name] || !cacheKeyOK[spec.Name] {
			continue
		}
		for _, cached := range cachedTools[spec.Name] {
			visible := cached.Name
			if spec.StripRawPrefix != "" {
				visible = strings.TrimPrefix(visible, spec.StripRawPrefix)
			}
			out = append(out, tool.MCPBinding{
				Package:      spec.Package,
				Server:       spec.Name,
				RawName:      cached.Name,
				VisibleName:  visible,
				CallableName: plugin.ModelToolName(spec.Name, visible),
				CapabilityID: "mcp-tool:" + spec.Name + "/" + cached.Name,
			})
		}
	}
	return out
}

func applyMCPIsolation(spec *plugin.Spec, workspaceRoot string, opts PluginSpecOptions) {
	if spec == nil {
		return
	}
	// Authorized user MCP defaults to trusted host process mode. Confined mode
	// is opt-in for internal managed deployments/tests and is never selected by
	// ordinary install paths.
	if spec.ProcessMode == "" {
		spec.ProcessMode = plugin.MCPProcessHost
	}
	if strings.TrimSpace(opts.StateHome) == "" {
		return
	}
	stateDir := plugin.MCPStateDir(opts.StateHome, workspaceRoot, spec.Name)
	spec.StateDir = stateDir
	if spec.ResolvedProcessMode() != plugin.MCPProcessConfined {
		// Host mode still gets a private state/cache/temp tree; only the OS
		// command sandbox is omitted so local app integrations keep working.
		return
	}
	writerRoots := appendUniquePaths([]string{stateDir}, opts.WriterRoots...)
	readerRoots := []string{workspaceRoot}
	if home, err := os.UserHomeDir(); err == nil {
		readerRoots = appendUniquePaths(readerRoots, home)
	}
	spec.Sandbox = sandbox.Spec{
		Mode: "enforce", WriteRoots: writerRoots,
		ReadRoots:              readerRoots,
		AppContainerWriteRoots: append([]string(nil), writerRoots...),
		ForbidReadRoots:        append([]string(nil), opts.ForbidReadRoots...),
		Network:                opts.Network, MinimalWrites: true,
	}
}

func secondsDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func toolTimeoutDurations(seconds map[string]int) map[string]time.Duration {
	if len(seconds) == 0 {
		return nil
	}
	out := make(map[string]time.Duration, len(seconds))
	for name, sec := range seconds {
		name = strings.TrimSpace(name)
		if name == "" || sec <= 0 {
			continue
		}
		out[name] = time.Duration(sec) * time.Second
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func applyKnownPluginOverrides(specs []plugin.Spec, workspaceRoot string) []plugin.Spec {
	out := make([]plugin.Spec, len(specs))
	for i, spec := range specs {
		out[i] = plugin.ApplyKnownOverrides(spec, workspaceRoot)
	}
	return out
}

func applyDefaultMCPCallTimeout(specs []plugin.Spec, timeout time.Duration) []plugin.Spec {
	if len(specs) == 0 || timeout <= 0 {
		return specs
	}
	out := make([]plugin.Spec, len(specs))
	for i, spec := range specs {
		out[i] = spec
		if out[i].DefaultCallTimeout <= 0 {
			out[i].DefaultCallTimeout = timeout
		}
	}
	return out
}

func applyDefaultMCPStartupTimeout(specs []plugin.Spec, timeout time.Duration) []plugin.Spec {
	if len(specs) == 0 || timeout <= 0 {
		return specs
	}
	out := make([]plugin.Spec, len(specs))
	for i, spec := range specs {
		out[i] = spec
		if out[i].DefaultStartupTimeout <= 0 {
			out[i].DefaultStartupTimeout = timeout
		}
	}
	return out
}

// autoShellPrefer reports whether [tools.shell] left the interpreter to
// auto-detection, so the "fell back to PowerShell" hint is suppressed once the
// user has explicitly chosen a shell.
func autoShellPrefer(prefer string) bool {
	p := strings.ToLower(strings.TrimSpace(prefer))
	return p == "" || p == "auto"
}

// MCPStartupNotice formats the warning shown when configured MCP servers failed
// to connect, naming the first few; ok is false when none failed.
func MCPStartupNotice(failures []plugin.Failure) (text, detail string, ok bool) {
	if len(failures) == 0 {
		return "", "", false
	}
	names := make([]string, 0, min(len(failures), 3))
	details := make([]string, 0, len(failures))
	for i, f := range failures {
		if i >= 3 {
			continue
		}
		names = append(names, f.Name)
	}
	for _, f := range failures {
		line := f.Name
		if strings.TrimSpace(f.Error) != "" {
			line += ": " + strings.TrimSpace(f.Error)
		}
		details = append(details, line)
	}
	more := ""
	if len(failures) > len(names) {
		more = fmt.Sprintf(" (+%d more)", len(failures)-len(names))
	}
	return "Some MCP servers failed to start; run /mcp for details.", fmt.Sprintf("%d MCP server(s) failed to start: %s%s\n%s",
		len(failures), strings.Join(names, ", "), more, strings.Join(details, "\n")), true
}

func providerNames(cfg *config.Config) string {
	names := make([]string, len(cfg.Providers))
	for i, p := range cfg.Providers {
		names[i] = p.Name
	}
	return strings.Join(names, "/")
}
