package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"mvdan.cc/sh/v3/syntax"

	"reasonix/internal/i18n"
	"reasonix/internal/jobs"
	"reasonix/internal/proc"
	"reasonix/internal/sandbox"
	"reasonix/internal/secrets"
	"reasonix/internal/sessiontemp"
	"reasonix/internal/shellparse"
	"reasonix/internal/shellrun"
	"reasonix/internal/tool"
)

const (
	bashWaitDelay = 5 * time.Second
)

func init() { tool.RegisterBuiltin(bash{}) }

var bashShellPATH = cachedBashShellPATH

var (
	bashSandboxCommand             = sandbox.Command
	bashSandboxEscapePromptEnabled = func() bool { return runtime.GOOS == "windows" }
)

// cachedBashShellPATH memoizes the login-shell PATH probe per login shell so a
// shell isn't spawned on every bash tool call (the probe runs up to three
// interactive-login shells with a 2s timeout each). Empty results are cached too,
// so a host without a usable login shell doesn't re-probe each command.
var (
	bashPathMu    sync.Mutex
	bashPathCache = map[string]string{}
)

func cachedBashShellPATH(ctx context.Context) string {
	key := loginShell()
	bashPathMu.Lock()
	if v, ok := bashPathCache[key]; ok {
		bashPathMu.Unlock()
		return v
	}
	bashPathMu.Unlock()

	v := defaultBashShellPATH(ctx)

	bashPathMu.Lock()
	bashPathCache[key] = v
	bashPathMu.Unlock()
	return v
}

// bash runs a shell command. sb, when it enforces, wraps the command in an OS
// sandbox; the zero value registered at init runs unconfined and is overridden
// per run by ConfineBash. shell is the resolved interpreter (real bash, or
// PowerShell on a Windows host without bash); the zero value resolves lazily.
// workDir, when non-empty, is the directory the command runs in (cmd.Dir);
// empty uses the process cwd. timeout optionally caps foreground commands;
// zero or negative means no tool-local cap, while parent context cancellation
// still kills the process tree. guard appends a warning to the output of
// commands that reference Reasonix's own session stores (see SessionDataGuard).
// sessionTemp, when non-nil, supplies the logical-session private temporary
// directory shared across Bash calls (see package sessiontemp). A Manager on
// the execution context overrides this for sub-agent isolation.
type bash struct {
	sb      sandbox.Spec
	rootSet *sandbox.WritableRootSet
	shell   sandbox.Shell
	guard   SessionDataGuard
	workDir string
	timeout time.Duration
	// terminal, when non-nil, runs foreground commands in a host-owned terminal
	// (ACP terminal/*). Only consulted when the local OS sandbox is not
	// enforcing — a host terminal cannot honor the confinement configuration —
	// and never for background jobs, which need the local job manager.
	terminal    TerminalRunner
	sessionTemp *sessiontemp.Manager
}

type bashParams struct {
	Command                     string   `json:"command"`
	RunInBackground             bool     `json:"run_in_background"`
	PreserveBackgroundProcesses bool     `json:"preserve_background_processes"`
	AdditionalWriteDirs         []string `json:"additional_write_dirs,omitempty"`
	Justification               string   `json:"justification,omitempty"`
}

func (bash) Name() string { return "bash" }

func (b bash) Description() string {
	sh := b.resolved()
	if sh.Kind == sandbox.ShellPowerShell {
		shellName := "Windows PowerShell"
		chaining := "';' runs both regardless; 'if ($?) { ... }' is conditional. '&&' and '||' are NOT parsed."
		if sh.SupportsChaining() {
			shellName = "PowerShell 7 (pwsh)"
			chaining = "'&&' and '||' are parsed for conditional chaining; ';' runs both regardless."
		}
		return fmt.Sprintf("Execute a command in the shell and return combined stdout/stderr. "+
			"NOTE: bash is not available on this host — commands run under %s, so write PowerShell, not bash:\n"+
			"  - chaining: %s\n"+
			"  - redirect/vars: $null not /dev/null; $env:VAR not $VAR; '2>$null' drops stderr.\n"+
			"  - file ops: Get-ChildItem (ls), Get-Content (cat), Remove-Item -Recurse -Force (rm -rf), Copy-Item (cp), Select-String (grep).\n"+
			"  - no head/tail/which/touch: use Select-Object -First/-Last N, (Get-Command x).Source, New-Item.\n"+
			"  - multi-line text to a native exe (e.g. git commit -m): use a single-quoted here-string @'...'@ (closing '@ at column 0)."+
			bashToolSteer, shellName, chaining)
	}
	return "Execute a command in the shell and return combined stdout/stderr. " +
		"To write outside the workspace, pass additional_write_dirs with the smallest concrete directories (no globs; absolute, workspace-relative, ~, or ${HOME}) and a justification. " +
		"The host will not infer write paths from the command text." + bashToolSteer
}

// bashToolSteer points the model at the cross-platform built-in tools instead of
// shell utilities, so it doesn't reach for grep/cat/ls/find (absent or different
// on native Windows) when a native tool already does the job everywhere.
const bashToolSteer = " Use for builds, tests, git, package managers, etc. To search/read/list/edit/move files, prefer the dedicated tools (grep, read_file, ls, glob, edit_file, move_file) over shell grep/cat/ls/find/sed/mv/Move-Item — they behave identically on every OS. For symbol search or architecture questions, prefer LSP/read tools and targeted grep before shell commands."

// resolved returns the bound shell, resolving lazily for the zero-value instance
// (e.g. a registry that never went through ConfineBash).
func (b bash) resolved() sandbox.Shell {
	if b.shell.Path != "" {
		return b.shell
	}
	if b.sb.Shell.Path != "" {
		return b.sb.Shell
	}
	return sandbox.ResolveShell("", "", nil)
}

// ReadOnly is false: bash's effect cannot be inferred from args (rm, curl,
// git commit, etc. are all reachable). Conservative even when a particular
// command happens to be read-only — the agent batch decision can't tell.
func (bash) ReadOnly() bool { return false }

// ToolInterruptBehavior returns InterruptBehaviorContinue: when the user
// submits a new message while bash is executing, the process keeps running
// in the background instead of being killed. This implements the Claude
// Code-style "steer in real-time" UX.
func (bash) ToolInterruptBehavior() tool.InterruptBehavior {
	return tool.InterruptBehaviorContinue
}

// SnipHint keeps both ends of command output equally: a build/test run's
// failure usually sits at the tail while the command and early context sit at
// the head, so neither end can be favored.
func (bash) SnipHint() tool.SnipHint {
	return tool.SnipHint{Head: 40, Tail: 40, HeadChars: 8000, TailChars: 8000}
}

// Execute is the compatibility wrapper: all structured metadata is produced by
// ExecuteDetailed and discarded here so plugin/hook callers keep the old shape.
func (b bash) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	res, err := b.ExecuteDetailed(ctx, args)
	return res.Output, err
}

// ExecutionDescriptor returns shell identity for the bound interpreter without
// launching a process. Invalid args still yield a descriptor from the shell.
func (b bash) ExecutionDescriptor(args json.RawMessage) *tool.ShellExecution {
	return shellrun.DescriptorFromShell(b.resolved())
}

// ExecuteDetailed runs the shell command and returns structured execution
// metadata for host UI / session persistence. Provider-visible output stays in
// DetailedResult.Output; metadata never enters tool schemas.
func (b bash) ExecuteDetailed(ctx context.Context, args json.RawMessage) (tool.DetailedResult, error) {
	start := time.Now()
	ex := shellrun.DescriptorFromShell(b.resolved())
	ex.State = tool.ShellStateRunning
	ex.MutationRisk = tool.ShellMutationUnknown
	ex.Verification = tool.ShellVerificationNotVerification

	var p bashParams
	if err := json.Unmarshal(args, &p); err != nil {
		return bashPreflightFailure(ex, start, fmt.Errorf("invalid args: %w", err))
	}
	if err := validateBashParams(p); err != nil {
		return bashPreflightFailure(ex, start, err)
	}

	sh := b.resolved()
	if !sh.SupportsChaining() && (hasUnquotedSeq(p.Command, "&&") || hasUnquotedSeq(p.Command, "||")) {
		ex.State = tool.ShellStateNotRun
		ex.FailurePhase = tool.ShellPhasePreflight
		ex.MutationRisk = tool.ShellMutationNotStarted
		ex.DurationMs = time.Since(start).Milliseconds()
		return tool.DetailedResult{Execution: ex}, fmt.Errorf("this shell is Windows PowerShell, which does not parse '&&' or '||'. " +
			"Sequence with ';' (both run regardless of the first's result), use 'if ($?) { ... }' for " +
			"conditional chaining, or issue the commands as separate calls")
	}

	// Pin the session-private temporary generation before any launch path so
	// foreground, background, and host-terminal runs share one directory, and
	// so a failed start still releases the lease.
	prepared, lease, err := b.prepareLaunch(ctx, sh, p.Command, args)
	if err != nil {
		ex.State = tool.ShellStateNotRun
		ex.FailurePhase = tool.ShellPhaseAuthorization
		if strings.Contains(err.Error(), "session temporary") {
			ex.FailurePhase = tool.ShellPhaseLaunch
		}
		ex.MutationRisk = tool.ShellMutationNotStarted
		ex.DurationMs = time.Since(start).Milliseconds()
		return tool.DetailedResult{Execution: ex}, err
	}
	// Background jobs take ownership of the lease until the job goroutine ends.
	// Foreground/terminal paths release after the process exits.
	releaseLease := true
	defer func() {
		if releaseLease && lease != nil {
			lease.Release()
		}
	}()

	// A host-owned terminal runs the command where the user watches it live.
	// Never when the OS sandbox is enforcing (the host cannot honor the local
	// confinement config), never when [secrets].filter_subprocess_env is on
	// (the host terminal spawns with its own unfiltered environment, which
	// would leak the credentials the user asked to strip), and never for
	// background jobs. ok=false falls back to local execution unchanged.
	if b.terminal != nil && !p.RunInBackground && !b.sb.Enforce() && !secrets.FilterSubprocessEnv() {
		envMap := sandbox.SessionTempEnvMap(prepared.SessionTemp, prepared.LinuxSandboxed)
		if out, ok, termErr := b.terminal.RunCommand(ctx, p.Command, b.workDir, b.timeout, envMap); ok {
			out = appendSessionDataHint(out, b.guard.CommandHint(b.workDir, p.Command))
			applyTerminalResult(ex, termErr)
			ex.DurationMs = time.Since(start).Milliseconds()
			return tool.DetailedResult{Output: out, Execution: ex}, termErr
		}
	}

	argv, wrapped := prepared.Argv, prepared.Wrapped
	cmdEnv := applyEnvOverrides(bashCommandEnv(ctx), prepared.EnvOverrides)

	if p.RunInBackground {
		jm, ok := jobs.FromContext(ctx)
		if !ok {
			ex.State = tool.ShellStateNotRun
			ex.FailurePhase = tool.ShellPhaseDependency
			ex.MutationRisk = tool.ShellMutationNotStarted
			ex.DurationMs = time.Since(start).Milliseconds()
			return tool.DetailedResult{Execution: ex}, fmt.Errorf("background execution is not available in this context")
		}
		workDir := b.workDir
		// Transfer lease ownership to the job closure; it releases when the
		// background process ends (including start failures inside the job).
		jobLease := lease
		releaseLease = false
		// The job runs under the manager's session context (no foreground timeout), so it
		// survives this turn; its combined output streams to the job buffer.
		job := jm.StartForSession(jobs.SessionFromContext(ctx), "bash", commandPreview(p.Command), func(jobCtx context.Context, out io.Writer) (string, error) {
			if jobLease != nil {
				defer jobLease.Release()
			}
			cmd := proc.CommandContext(jobCtx, argv[0], argv[1:]...)
			cmd.Dir = workDir
			cmd.Env = cmdEnv
			cmd.WaitDelay = bashWaitDelay
			cmd.Stdout = out
			cmd.Stderr = out
			tracked, runErr := runShellProcess(jobCtx, cmd, sh, p.Command, shouldTrackShellProcess(wrapped, sh, p.Command, p.PreserveBackgroundProcesses))
			if shouldReapAfterRun(jobCtx, sh, p.Command, p.PreserveBackgroundProcesses) {
				reapShellProcess(cmd, tracked) // reap process-group stragglers the job left running (#3702)
			}
			return "", normalizeBashRunError(jobCtx, runErr, p.PreserveBackgroundProcesses)
		})
		msg := fmt.Sprintf("Started background job %q. It keeps running across turns; read new output with bash_output(job_id=%q), wait for it with wait, or stop it with kill_shell(job_id=%q).", job.ID, job.ID, job.ID)
		// Background start is not a completed execution: completion is reported
		// later by bash_output/wait. Do not masquerade as success with exit 0.
		ex.State = tool.ShellStateBackgroundStarted
		ex.MutationRisk = tool.ShellMutationUnknown
		ex.DurationMs = time.Since(start).Milliseconds()
		return tool.DetailedResult{
			Output:    appendSessionDataHint(msg, b.guard.CommandHint(b.workDir, p.Command)),
			Execution: ex,
		}, nil
	}

	out, runEx, err := b.runForegroundDetailed(ctx, p, sh, argv, wrapped, cmdEnv)
	mergeRunInto(ex, runEx)
	ex.DurationMs = time.Since(start).Milliseconds()
	return tool.DetailedResult{
		Output:    b.appendWriteHints(ctx, out, err, p, wrapped),
		Execution: ex,
	}, err
}

func applyTerminalResult(ex *tool.ShellExecution, err error) {
	if ex == nil {
		return
	}
	if err == nil {
		ex.State = tool.ShellStateCompleted
		ex.ExitCode = tool.IntPtr(0)
		ex.MutationRisk = tool.ShellMutationMayHaveCompleted
		return
	}
	if errors.Is(err, context.Canceled) {
		ex.State = tool.ShellStateCancelled
		ex.FailurePhase = tool.ShellPhaseCancellation
		ex.MutationRisk = tool.ShellMutationMayBePartial
		return
	}
	var timeoutErr TerminalTimeoutError
	if errors.As(err, &timeoutErr) || errors.Is(err, context.DeadlineExceeded) {
		ex.State = tool.ShellStateTimedOut
		ex.FailurePhase = tool.ShellPhaseTimeout
		ex.MutationRisk = tool.ShellMutationMayBePartial
		return
	}
	var exitErr TerminalExitError
	if errors.As(err, &exitErr) {
		code := exitErr.Code
		ex.ExitCode = &code
		ex.State = tool.ShellStateFailed
		ex.FailurePhase = tool.ShellPhaseExecution
		ex.MutationRisk = tool.ShellMutationMayBePartial
		return
	}
	// Legacy plain errors from older host runners.
	ex.State = tool.ShellStateFailed
	ex.FailurePhase = tool.ShellPhaseExecution
	ex.MutationRisk = tool.ShellMutationMayBePartial
}

func mergeRunInto(dst *tool.ShellExecution, src *tool.ShellExecution) {
	if dst == nil || src == nil {
		return
	}
	dst.State = src.State
	dst.FailurePhase = src.FailurePhase
	dst.ExitCode = src.ExitCode
	dst.OutputTail = src.OutputTail
	if src.MutationRisk != "" {
		dst.MutationRisk = src.MutationRisk
	}
}

// prepareLaunch acquires a session-temp lease (when a Manager is available),
// builds the sandboxed argv, and applies sandbox-escape approval. The caller
// owns the returned lease and must Release it after the process exits.
func (b bash) prepareLaunch(ctx context.Context, sh sandbox.Shell, command string, rawArgs json.RawMessage) (sandbox.Prepared, *sessiontemp.Lease, error) {
	var lease *sessiontemp.Lease
	sessionDir := ""
	if m := b.sessionTempManager(ctx); m != nil {
		l, err := m.Acquire()
		if err != nil {
			return sandbox.Prepared{}, nil, fmt.Errorf("session temporary directory: %w", err)
		}
		lease = l
		sessionDir = l.Dir()
	}

	// bashSandboxCommand is injectable for tests; production points at
	// sandbox.Command. Attach SessionTemp so Linux bwrap binds the private dir.
	spec := b.specForCall(ctx)
	spec.SessionTemp = sessionDir
	argv, wrapped := bashSandboxCommand(spec, sh, command)
	linuxSB := wrapped && sessionDir != "" && runtime.GOOS == "linux"
	prepared := sandbox.Prepared{
		Argv:           argv,
		Wrapped:        wrapped,
		SessionTemp:    sessionDir,
		EnvOverrides:   sandbox.SessionTempEnv(sessionDir, linuxSB),
		LinuxSandboxed: linuxSB,
	}

	if b.sb.Enforce() && bashSandboxEscapeSessionAllowed(ctx, command, rawArgs) {
		prepared.Argv = unconfinedShellArgv(sh, command)
		prepared.Wrapped = false
		// Escaped commands still inherit private temp env vars pointing at the
		// host private directory (no virtual /tmp mapping).
		prepared.LinuxSandboxed = false
		prepared.EnvOverrides = sandbox.SessionTempEnv(sessionDir, false)
	} else if b.sb.Enforce() && !prepared.Wrapped {
		allow, reason, err := approveBashSandboxEscape(ctx, command, rawArgs, i18n.M.SandboxEscapeWrapReason)
		if err != nil {
			if lease != nil {
				lease.Release()
			}
			return sandbox.Prepared{}, nil, err
		}
		if !allow {
			if lease != nil {
				lease.Release()
			}
			if reason != "" {
				return sandbox.Prepared{}, nil, fmt.Errorf("%s", reason)
			}
			return sandbox.Prepared{}, nil, fmt.Errorf("%s", sandbox.UnavailableMessage())
		}
		prepared.Argv = unconfinedShellArgv(sh, command)
		prepared.Wrapped = false
		prepared.LinuxSandboxed = false
		prepared.EnvOverrides = sandbox.SessionTempEnv(sessionDir, false)
	}
	return prepared, lease, nil
}

func (b bash) sessionTempManager(ctx context.Context) *sessiontemp.Manager {
	if m := sessiontemp.FromContext(ctx); m != nil {
		return m
	}
	return b.sessionTemp
}

func applyEnvOverrides(env, overrides []string) []string {
	for _, kv := range overrides {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			continue
		}
		env = setEnvValue(env, key, value)
	}
	return env
}

// appendSessionDataHint appends the session-data guard warning to command
// output; with no output the hint stands alone. An empty hint is a no-op.
func appendSessionDataHint(out, hint string) string {
	if hint == "" {
		return out
	}
	if strings.TrimSpace(out) == "" {
		return hint
	}
	return out + "\n\n" + hint
}

func unconfinedShellArgv(sh sandbox.Shell, command string) []string {
	argv, _ := sandbox.Command(sandbox.Spec{}, sh, command)
	return argv
}

func approveBashSandboxEscape(ctx context.Context, command string, args json.RawMessage, reason string) (bool, string, error) {
	if !bashSandboxEscapePromptEnabled() {
		return false, "", nil
	}
	approver, ok := sandbox.EscapeApproverFrom(ctx)
	if !ok {
		return false, "", nil
	}
	return approver.ApproveSandboxEscape(ctx, sandbox.EscapeRequest{
		Command: command,
		Args:    append(json.RawMessage(nil), args...),
		Reason:  reason,
	})
}

func bashSandboxEscapeSessionAllowed(ctx context.Context, command string, args json.RawMessage) bool {
	if !bashSandboxEscapePromptEnabled() {
		return false
	}
	approver, ok := sandbox.EscapeApproverFrom(ctx)
	if !ok {
		return false
	}
	checker, ok := approver.(sandbox.EscapeSessionChecker)
	if !ok {
		return false
	}
	return checker.SandboxEscapeSessionAllowed(ctx, sandbox.EscapeRequest{
		Command: command,
		Args:    append(json.RawMessage(nil), args...),
		Reason:  i18n.M.SandboxEscapeRuntimeReason,
	})
}

// runForegroundDetailed uses the shared shellrun collector so model bash and
// user !command share exit-code / phase / output-tail classification.
func (b bash) runForegroundDetailed(ctx context.Context, p bashParams, sh sandbox.Shell, argv []string, wrapped bool, cmdEnv []string) (string, *tool.ShellExecution, error) {
	ex := shellrun.DescriptorFromShell(sh)
	var progress func(string)
	if emit, ok := tool.ProgressFrom(ctx); ok {
		progress = emit
	}
	track := shouldTrackShellProcess(wrapped, sh, p.Command, p.PreserveBackgroundProcesses)
	res := shellrun.RunForeground(ctx, shellrun.Request{
		Argv:              argv,
		Dir:               b.workDir,
		Env:               cmdEnv,
		Timeout:           b.foregroundTimeout(),
		WaitDelay:         bashWaitDelay,
		CommandPreview:    commandPreview(p.Command),
		ShellKind:         sh.Kind.String(),
		ShellPath:         sh.Path,
		Source:            "bash_tool",
		Track:             track,
		PreserveWaitDelay: p.PreserveBackgroundProcesses,
		Progress:          progress,
	})
	// A foreground command that spawned a lingering child (e.g. `bazel run`'s
	// server) leaves it in the process group; Wait only reaped the shell leader.
	// Kill the group so those don't accumulate into an OOM (#3702). On cancel/
	// timeout the command's Cancel path already did this; this covers normal exit.
	// shellrun owns the tool-local timeout context, so treat timed_out/cancelled
	// as ctx.Err()!=nil for the reap decision.
	reapCtx := ctx
	if res.State == tool.ShellStateTimedOut || res.State == tool.ShellStateCancelled || ctx.Err() != nil {
		// Force reap on forced stops even when preserve_background_processes is set.
		reapShellProcess(res.Cmd, res.Tracked)
	} else if shouldReapAfterRun(reapCtx, sh, p.Command, p.PreserveBackgroundProcesses) {
		reapShellProcess(res.Cmd, res.Tracked)
	}

	ex.State = res.State
	ex.FailurePhase = res.FailurePhase
	ex.ExitCode = res.ExitCode
	ex.OutputTail = res.OutputTail
	switch res.State {
	case tool.ShellStateCompleted:
		ex.MutationRisk = tool.ShellMutationMayHaveCompleted
	case tool.ShellStateNotRun:
		ex.MutationRisk = tool.ShellMutationNotStarted
	case tool.ShellStateFailed:
		if res.FailurePhase == tool.ShellPhaseLaunch || res.FailurePhase == tool.ShellPhasePreflight {
			ex.MutationRisk = tool.ShellMutationNotStarted
		} else {
			ex.MutationRisk = tool.ShellMutationMayBePartial
		}
	case tool.ShellStateTimedOut, tool.ShellStateCancelled:
		ex.MutationRisk = tool.ShellMutationMayBePartial
	default:
		ex.MutationRisk = tool.ShellMutationUnknown
	}
	return res.Combined, ex, res.Err
}

func normalizeBashRunError(ctx context.Context, err error, preserveBackgroundProcesses bool) error {
	if preserveBackgroundProcesses && ctx.Err() == nil && errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	return err
}

func shouldReapAfterRun(ctx context.Context, sh sandbox.Shell, command string, preserveBackgroundProcesses bool) bool {
	if ctx.Err() != nil {
		return true
	}
	if preserveBackgroundProcesses {
		return false
	}
	return !sh.Kind.IsPOSIX() || !hasExplicitBackgroundKeepalive(command)
}

// hasExplicitBackgroundKeepalive detects common shell-level daemonization intent
// without letting a plain "cmd &" bypass #3702's stray process cleanup.
func hasExplicitBackgroundKeepalive(command string) bool {
	file, err := shellparse.ParseBash(command)
	if err != nil {
		return false
	}

	hasBackground := false
	hasKeepaliveCommand := false
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.Stmt:
			if n.Background {
				hasBackground = true
			}
		case *syntax.CallExpr:
			name, ok := staticShellCallName(n)
			if !ok {
				break
			}
			switch name {
			case "disown", "nohup", "setsid":
				hasKeepaliveCommand = true
			}
		}
		return !(hasBackground && hasKeepaliveCommand)
	})
	return hasBackground && hasKeepaliveCommand
}

func (b bash) foregroundTimeout() time.Duration {
	if b.timeout <= 0 {
		return 0
	}
	return b.timeout
}

func shouldTrackShellProcess(wrapped bool, sh sandbox.Shell, command string, preserveBackgroundProcesses bool) bool {
	if preserveBackgroundProcesses {
		return false
	}
	if runtime.GOOS == "windows" && wrapped {
		return false
	}
	return !sh.Kind.IsPOSIX() || !hasExplicitBackgroundKeepalive(command)
}

func runShellProcess(ctx context.Context, cmd *exec.Cmd, sh sandbox.Shell, command string, track bool) (*proc.TrackedCommand, error) {
	return proc.RunCommand(ctx, cmd, proc.RunOptions{
		Track:           track,
		CancelWaitGrace: bashWaitDelay + time.Second,
		Source:          "bash_tool",
		ShellKind:       sh.Kind.String(),
		ShellPath:       sh.Path,
		CommandPreview:  commandPreview(command),
	})
}

func reapShellProcess(cmd *exec.Cmd, tracked *proc.TrackedCommand) {
	if tracked != nil {
		tracked.Kill()
		return
	}
	proc.KillTree(cmd)
}

// hasUnquotedSeq reports whether seq appears in s outside any single- or
// double-quoted span, so a literal "a && b" string argument doesn't trip the
// PowerShell chaining guard.
func hasUnquotedSeq(s, seq string) bool {
	var quote byte
	for i := range len(s) {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if strings.HasPrefix(s[i:], seq) {
			return true
		}
	}
	return false
}

func staticShellCallName(call *syntax.CallExpr) (string, bool) {
	for _, arg := range call.Args {
		word, ok := shellparse.StaticWord(arg)
		if !ok {
			return "", false
		}
		if shellparse.IsAssignment(word) {
			continue
		}
		base := shellparse.WordBase(word)
		if base == "command" || base == "env" {
			continue
		}
		return base, true
	}
	return "", false
}

// commandPreview is a short single-line label for a background bash job, surfaced
// in the status bar and completion notices.
func commandPreview(cmd string) string {
	cmd = strings.TrimSpace(strings.ReplaceAll(cmd, "\n", " "))
	const max = 48
	r := []rune(cmd)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return cmd
}

func bashCommandEnv(ctx context.Context) []string {
	env := secrets.ProcessEnv()
	if runtime.GOOS == "windows" {
		return env
	}
	currentPath, _ := envValue(env, "PATH")
	if shellPath := strings.TrimSpace(bashShellPATH(ctx)); shellPath != "" {
		if merged := mergePathLists(shellPath, currentPath); merged != currentPath {
			env = setEnvValue(env, "PATH", merged)
		}
	}
	return env
}

func defaultBashShellPATH(ctx context.Context) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	shell := loginShell()
	if shell == "" {
		return ""
	}
	const marker = "__REASONIX_BASH_PATH__="
	script := "printf '\\n" + marker + "%s\\n' \"$PATH\""
	for _, args := range [][]string{
		{"-l", "-i", "-c", script},
		{"-l", "-c", script},
		{"-c", script},
	} {
		out := runShellPATHCommand(ctx, shell, args)
		if path := parseShellPATH(out, marker); path != "" {
			return path
		}
	}
	return ""
}

func loginShell() string {
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		if hasPathSeparator(shell) {
			if isExecutableFile(shell) {
				return shell
			}
		} else if p, err := exec.LookPath(shell); err == nil {
			return p
		}
	}
	for _, shell := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
		if isExecutableFile(shell) {
			return shell
		}
	}
	return ""
}

func runShellPATHCommand(parent context.Context, shell string, args []string) []byte {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	cmd := proc.CommandContext(ctx, shell, args...)
	// Explicit env so the login-shell probe honors [secrets]
	// filter_subprocess_env instead of inheriting the full environment.
	cmd.Env = secrets.ProcessEnv()
	proc.PrepareShellPATHProbe(cmd)
	cmd.Stdin = strings.NewReader("")
	out, _ := cmd.CombinedOutput()
	return out
}

func parseShellPATH(out []byte, marker string) string {
	lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
	for _, line := range slices.Backward(lines) {
		if rest, ok := strings.CutPrefix(line, marker); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func hasPathSeparator(s string) bool {
	return strings.ContainsAny(s, `/\`)
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}

func setEnvValue(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if ok && envKeyEqual(k, key) {
			if !replaced {
				out = append(out, key+"="+value)
				replaced = true
			}
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, key+"="+value)
	}
	return out
}

func envValue(env []string, key string) (string, bool) {
	for _, entry := range slices.Backward(env) {
		k, v, ok := strings.Cut(entry, "=")
		if ok && envKeyEqual(k, key) {
			return v, true
		}
	}
	return "", false
}

func envKeyEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func mergePathLists(primary, secondary string) string {
	var out []string
	seen := map[string]bool{}
	add := func(path string) {
		for _, part := range filepath.SplitList(path) {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			out = append(out, part)
		}
	}
	add(primary)
	add(secondary)
	return strings.Join(out, string(os.PathListSeparator))
}
