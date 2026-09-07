// Package i18n holds the CLI's translatable strings and a small detection
// helper. Architecture: a single Messages struct of exported string fields
// (plain text or fmt format strings, suffix *Fmt flags the latter). Each
// language declares one Messages value in its own file. Call sites read
// i18n.M.SomeField; for parameterised messages they pass it to fmt.Sprintf.
//
// Adding a field requires updating every messages_*.go file — drift is caught
// at test time by TestCatalogsComplete via reflection, so a missing translation
// fails CI instead of surfacing as a blank line at runtime.
//
// Scope (v1): CLI surface only — welcome, init wizard, chat REPL banner, usage,
// user-facing CLI errors. System prompts, internal error wrappers, and agent
// runtime telemetry stay English so model behaviour and developer logs are
// language-stable.
package i18n

import (
	"os"
	"strings"
)

// Messages is the catalogue of translatable CLI strings. Plain fields are
// printed verbatim; *Fmt fields are fmt format strings the caller passes to
// fmt.Sprintf. Catalogue values do not include trailing newlines — call sites
// add framing whitespace, so the same field works wherever it appears.
type Messages struct {
	// welcome / status screen
	WelcomeTitleFmt string // first-run box title — %s = product name (styled)
	NoConfigYet     string // first-run cue under the welcome box

	// `reasonix init` — points to the in-session /init skill + setup
	InitHint string

	// chat REPL
	ChatTip                string // tip line under the chat banner
	TurnCancelled          string // shown when Ctrl-C aborts the in-flight turn but the chat keeps running
	InterruptedRecovery    string // replay notice for a durable interrupted turn
	FinalReadinessRecovery string // replay hint for a durable final-readiness pause
	ReadinessContinuing    string // host is automatically finishing known readiness gaps
	RecoveryPaused         string // controlled Auto retry pause; user can continue in the next message
	CompletionUncertain    string // completion validator could not confirm the result; work is kept
	ReasoningReplayRepair  string // provider rejected replayed thinking blocks; history repaired and retried once
	// Host guard/recovery notices (event.Notice texts the fronts render verbatim).
	EmptyFinal                       string // empty_final: no visible answer; retrying
	ExecutorHandoff                  string // executor_handoff: answered without using tools
	ToolBudget                       string // tool_budget: tool-call round limit reached
	TaskBudget                       string // tool_budget variant: task spend budget reached
	LoopGuard                        string // loop_guard: no-progress tool loop
	ProgressGuard                    string // progress_guard: repeated work without new evidence
	SoftBudgetConverge               string // loop_guard: converging a long read-only investigation
	EvidenceNudge                    string // evidence_nudge: unverified mutations
	ReasoningGovernor                string // reasoning_governor engaged
	UnappliedSteerFmt                string // unapplied_steer — %s = the dropped guidance
	DeprecatedContextRetention       string // agent.keep / agent.recent_keep deprecation warning
	FinishReasonLength               string
	FinishReasonContentFilter        string
	FinishReasonRepetition           string
	StreamInterruptedIdleTimeout     string
	StreamInterruptedPrematureEOF    string
	StreamInterruptedConnectionReset string
	ToolOutputTruncatedFmt           string // %d = elided bytes, %d = original bytes
	IncompleteReadFinishBlocked      string
	ReadContinuationRequired         string
	IncompleteReadDetected           string
	ReadStrategyRequired             string
	ReadStrategyProgress             string
	ReadStrategyResolved             string
	ReadLocalSafetyPaged             string
	ReadCompleted                    string
	ReadRestrictedStrategyFmt        string // %d = estimated tokens, %d = token budget
	ContextRecoveryAdjustBudget      string
	ContextRecoveryCompacted         string
	PlannerFallback                  string
	PlannerSafetyFallback            string
	PlannerPlanAwaitingApproval      string
	PlannerPlanNotApproved           string
	PlannerPlanOnly                  string
	CapabilityProxyFmt               string // %s = display name, %s = resolved target
	ReceiptVerified                  string // end-of-turn receipt, nothing unproven
	ReceiptGapsHeader                string // end-of-turn receipt, header above the unproven list
	ReceiptRisksHeader               string // end-of-turn receipt, header above declared risks
	ReceiptMore                      string // end-of-turn receipt, "and N more" tail
	// ReceiptGapKinds maps a completion gap kind to its short human phrase.
	ReceiptGapKinds   map[string]string
	NoSessionToResume string // shown when --continue / --resume finds nothing
	ResumeRequiresTTY string // shown when --resume runs piped instead of on a terminal
	PickSessionLabel  string // header on the --resume picker

	// in-chat /resume command
	ResumeBusy          string // shown when /resume is used mid-turn
	ResumeBadIndexFmt   string // shown when /resume gets an out-of-range index (one %d)
	ResumeAlreadyActive string // shown when /resume targets the current session
	ResumedTitle        string // banner title after a /resume switch

	RenameUsage            string // /rename with no args
	RenameNoSession        string // /rename with no active session
	RenameDoneFmt          string // /rename succeeded (one %s = new title)
	ResumePickTitle        string // header in the interactive resume picker
	ResumePickHint         string // keyboard hint in the interactive resume picker
	ResumeRecoveryBadgeFmt string // recovery-copy badge — %s = short parent session id

	// chat TUI status line / approval banner.
	ChatThinking                           string // live reasoning marker label, e.g. "thinking…"
	ChatThoughtForFmt                      string // collapsed reasoning summary, "%d" = elapsed s
	ChatStatusThinkingFmt                  string // "%s thinking… (%ds · <cancel hint>)" — %s = spinner, %d = elapsed s
	TurnPhaseWorking                       string // host turn_phase label: working
	TurnPhaseChecking                      string // host turn_phase label: checking
	TurnPhaseVerifying                     string // host turn_phase label: verifying
	TurnPhaseReviewing                     string // host turn_phase label: reviewing
	CompletionSummaryBlocked               string // concise non-verbose alert for a blocked turn
	CompletionSummaryNeedsAttention        string // concise non-verbose alert for verification/review gaps
	ChatToolWorkingFmt                     string // "%s working · %ds" under a running tool — %s = spinner, %d = elapsed s
	ChatSubagentPhaseQueued                string // sub-agent progress phase label ("queued")
	ChatSubagentPhaseRunning               string // ("running")
	ChatSubagentPhaseReasoning             string // ("reasoning")
	ChatSubagentPhaseResponding            string // ("responding")
	ChatSubagentPhaseTool                  string // ("using tools")
	ChatSubagentPhaseRetrying              string // ("retrying")
	ChatSubagentPhaseCompleted             string // ("completed")
	ChatSubagentPhaseFailed                string // ("failed")
	ChatSubagentPhaseCancelled             string // ("cancelled")
	ChatSubagentProgressFmt                string // live progress line — %s = phase label, %d = elapsed s, %d = idle s ("%s · %ds · %ds ago")
	ChatSubagentProgressDoneFmt            string // terminal summary — %s = phase label, %d = duration s ("%s · %ds")
	ChatSubagentPreviewLabel               string // verbose preview marker ("▎")
	ChatStatusRetryingFmt                  string // "%s retrying (%d/%d)…" — %s = spinner, %d/%d = attempt/max
	ChatStatusCancellingFmt                string // "%s stopping… (%ds · Ctrl+C exits)" — %s = spinner, %d = elapsed s
	ChatStatusIdle                         string // shortcuts hint when idle
	ChatStatusYoloIdle                     string // shortcuts hint when idle in YOLO/bypass mode
	ChatStatusCycleHint                    string // plan-toggle shortcut hint shown when no modal prompt owns the status row
	ChatStatusCycleHintCompact             string // readable shortcut hint used by the persistent footer
	ChatTurnReceiptLabel                   string // compact per-turn usage receipt attached to the completed assistant response
	RateBandPeak                           string
	RateBandOffPeak                        string
	RateBandMixed                          string
	ChatStatusModelLabel                   string
	ChatStatusEffortLabel                  string
	ChatStatusPresetLabel                  string
	ChatStatusCacheLabel                   string
	ChatStatusContextLabel                 string
	ChatStatusCompactLabel                 string
	ChatStatusJobsLabel                    string
	ChatStatusBalanceLabel                 string
	ChatStatusCostLabel                    string
	ChatStatusCacheNowFmt                  string // cache status tag, "%s" = latest-turn hit rate with percent sign
	ChatStatusCacheAvgFmt                  string // cache status tag, "%s" = session-average hit rate with percent sign
	ChatStatusPlanApproval                 string // shortcuts hint while a plan is pending
	PlanApprovalPrompt                     string // one-line "plan above is ready" banner shown above the input
	PlanApprovalChoices                    string // start / revise / exit-without-executing choice list
	ChatStatusToolApproval                 string // shortcuts hint while a tool call awaits approval
	ToolApprovalPromptFmt                  string // approval banner — tool, subject suffix, source/intent detail, choices
	ToolApprovalChoices                    string // standard approval choice list
	BashPrefixChoices                      string // approval choice list when a bash prefix can be granted
	PlanModeReadOnlyCommandChoices         string // approval choice list for plan-mode read-only command trust
	FreshHumanApprovalChoices              string // approval choice list for prompts that cannot be remembered
	RecoveryApprovalChoices                string // one-shot Auto Guard decision list
	RecoveryPlanChangeChoices              string // material Auto plan transition decision list
	RecoveryPlanDecisionPrompt             string // neutral title for a material Auto plan transition
	RecoveryPlanBeforeFmt                  string // compact previous-plan line, one %s
	RecoveryPlanAfterFmt                   string // compact proposed-plan line, one %s
	RecoveryTaskGrantChoices               string // Auto Guard list with a current-task semantic grant
	SandboxEscapeApprovalChoices           string // approval choice list for OS sandbox escape prompts
	ApprovalNeededFmt                      string // notification text for a pending approval, tool only
	ApprovalNeededWithSubjectFmt           string // notification text for a pending approval with subject
	ToolApprovalSourceFmt                  string // "Source: %s" / "来源: %s"
	ToolApprovalBuiltIn                    string // built-in tool source label
	ToolApprovalImageUse                   string // image-understanding detail for understand_image-style tools
	ApprovalToolLabelBash                  string // user-facing label for bash approvals
	ApprovalToolLabelEditFile              string // user-facing label for edit_file approvals
	ApprovalToolLabelWriteFile             string // user-facing label for write_file approvals
	ApprovalToolLabelMultiEdit             string // user-facing label for multi_edit approvals
	ApprovalToolLabelMoveFile              string // user-facing label for move_file approvals
	ApprovalToolLabelWebFetch              string // user-facing label for web_fetch approvals
	ApprovalToolLabelRunSkill              string // user-facing label for run_skill approvals
	ApprovalToolLabelRemember              string // user-facing label for remember approvals
	ApprovalToolLabelForget                string // user-facing label for forget approvals
	ApprovalToolLabelSandboxEscape         string // user-facing label for OS sandbox escape approvals
	ApprovalToolLabelPlanModeReadOnly      string // user-facing label for plan-mode read-only command trust approvals
	MemoryApprovalSaveUpdate               string // subject prefix for remember approval
	MemoryApprovalBodyLabel                string // label before the body excerpt in remember approval
	MemoryApprovalArchiveFmt               string // subject for forget approval, %q = memory name
	PlanModeBashTrustSubjectFmt            string // subject for bash read-only prefix trust approval, prefix + command
	PlanModeBashTrustReason                string // reason for bash read-only prefix trust approval
	PlanModeBashTrustDeclined              string // model-facing denial after bash read-only prefix rejection
	SandboxEscapeSubjectFallback           string // fallback subject for a one-shot unconfined sandbox escape approval
	SandboxEscapeSubjectPrefix             string // subject prefix before the shell command for one-shot unconfined escape approval
	SandboxEscapeWrapReason                string // reason when no OS sandbox can wrap the command
	SandboxEscapeRuntimeReason             string // fallback reason when an OS sandbox cannot start the command
	SandboxEscapeDeclined                  string // model-facing denial when the user declines a one-shot unconfined retry
	ApprovalToolLabelConfigWrite           string // user-facing label for Reasonix-managed config write approvals
	ConfigWriteSubjectPrefix               string // subject prefix before the config file path for managed config write approval
	ConfigWriteReason                      string // reason shown for managed config write approval
	ConfigWriteDeclined                    string // model-facing denial when the user declines a managed config write
	ConfigWriteApprovalChoices             string // approval choice list for managed config write prompts
	WriteAccessApprovalChoices             string // four-choice list for extending writable roots
	WriteAccessHomeWarning                 string // high-risk warning when granting the whole home directory
	WriteAccessMergedPermissionHint        string // note that the same choice also grants ordinary tool permission
	WriteAccessProjectHint                 string // note that project persist edits reasonix.toml
	PermissionSavedFmt                     string // permission rule saved notice: path, rule
	PermissionAlreadyAllowedFmt            string // permission rule already covered notice: path, rule
	PermissionSaveFailedFmt                string // permission rule save failure notice: rule, error
	PlanModeReadOnlyCommandTrustSavedFmt   string // plan-mode bash read-only prefix saved notice: path, prefix
	PlanModeReadOnlyCommandTrustAlreadyFmt string // plan-mode bash read-only prefix already covered notice: path, prefix
	PlanModeReadOnlyCommandTrustFailedFmt  string // plan-mode bash read-only prefix save failure notice: prefix, error
	DiffFoldedFmt                          string // "… +%d more lines" footer when a writer diff is folded
	DiffFoldEnabledFmt                     string // notice when /diff-fold enables folding, %d = line limit
	DiffFoldDisabled                       string // notice when /diff-fold disables folding (shows all lines)

	// `ask` tool question card.
	AskTypeSomething   string // the "type your own answer" option label
	AskTypingHint      string // shown on that row while entering free text
	AskChatInstead     string // the "don't pick, just chat" option label
	ChatStatusQuestion string // shortcuts hint while a question card is open
	StatusResumePicker string // status tag while the resume picker is open (e.g. "select session")
	AskSubmitTitle     string // submit-tab title in the ask tool question card
	AskUnanswered      string // placeholder for an unanswered ask question
	AskSubmitHint      string // submit-tab keyboard hint
	ElicitURLHint      string // url-mode elicitation keyboard hint
	ElicitConfirmOnly  string // schema-less form elicitation hint
	ElicitUnanswered   string // placeholder for an unanswered elicitation field
	ElicitSubmit       string // elicitation submit row label
	ElicitSubmitHint   string // elicitation keyboard hint

	// output style listing (/output-style).
	OutputStyleNone           string // no styles available
	ThemeHeader               string // header above the /theme listing
	ThemeHint                 string // how to select a theme
	ThemeChangedFmt           string // "/theme <name>" succeeded
	ThemeUnknownFmt           string // "/theme <name>" unknown
	LanguageHeader            string // header above the /language listing
	LanguageHint              string // how to select a language
	LanguageChangedFmt        string // "/language <tag>" succeeded, %s = saved tag, %s = resolved tag
	CurrencyHeader            string // header above the /currency listing
	CurrencyHint              string // how to select a pricing currency
	CurrencyChangedFmt        string // "/currency <mode>" succeeded, %s = saved mode, %s = resolved currency
	CmdShowBalance            string // help text for /show-balance
	ShowBalanceHeader         string // header above the /show-balance status
	ShowBalanceHint           string // how to select a balance display mode
	ShowBalanceChangedFmt     string // "/show-balance <mode>" succeeded, %s = saved mode
	RuntimeRefreshBusy        string // runtime-affecting setting cannot change while work is active
	RuntimeRefreshUnavailable string // current session cannot rebuild after a runtime-affecting setting change

	// context compaction card (CompactionStarted / CompactionDone events).
	CompactionWorking string // shown while the summarizer runs
	CompactionTitle   string // card header before "· N messages · <trigger>"
	CompactionUnit    string // the noun counted, e.g. "messages"
	CompactionAuto    string // trigger label: reached the window threshold
	CompactionManual  string // trigger label: user ran /compact

	// extension structured-UI surfaces (ExtensionSurface / ExtensionStatus events).
	ExtFormFieldsHint string // form card: field values are collected through the usual prompts
	ExtRunActionFmt   string // card action hint, one %s = the /<plugin>:<action> slash name

	// chat TUI slash commands.
	SlashCompactFailed           string // "/compact" errored, prefixed before the underlying error
	SlashNewDone                 string // "/new" succeeded
	SlashNewFailed               string // "/new" errored
	SlashClearPrompt             string // "/clear" destructive confirmation prompt
	SlashClearDone               string // "/clear" succeeded
	SlashClearFailed             string // "/clear" errored
	SlashClsDone                 string // "/cls" succeeded
	SlashTodoCleared             string // "/todo" dismissed the pinned task list
	SlashUnknown                 string // shown when the user types an unrecognised "/cmd"
	SlashUnknownSentAsMessage    string // suffix: the unrecognised "/cmd" line was sent as a regular message
	SlashPromptEmpty             string // an MCP prompt returned no text to send
	SlashMCPNone                 string // /mcp when no MCP servers are connected
	CtrlCQuitHint                string // shown on first Ctrl+C while idle; second press exits
	CompHintSlash                string // key hint footer under the slash-command menu
	CompHintFile                 string // key hint footer under the @ file/resource menu
	MouseCopiedHint              string // transient status-line hint after a mouse/Ctrl+C selection copy
	ClipboardCopyOSC52Hint       string // copy was sent through OSC 52 because the session is remote
	ClipboardCopyFallbackHint    string // native clipboard failed and copy fell back to OSC 52
	ClipboardTextPasteRemoteHint string // mouse paste cannot read the user's local clipboard/PRIMARY selection over SSH
	ClipboardTextPasteFailedFmt  string // text clipboard read failed, one %v
	ClipboardImagePastingHint    string // shown while an image is being read from the system clipboard
	ClipboardPasteEmptyNotice    string
	ClipboardImagePasteFailedFmt string // image clipboard read failed, one %v
	MouseCaptureOnHint           string // "/mouse" turned in-app mouse handling back on
	MouseCaptureOffHint          string // "/mouse" released mouse capture to the terminal
	MouseCaptureTag              string // persistent status-line marker while mouse capture is off

	// shell execution (! prefix).
	ShellExecEmpty      string // bare "!" with no command
	ShellExecFailedFmt  string // "shell command failed: %v"
	ShellExecTimeoutFmt string // "shell command timed out (> %s)"
	ShellModeHint       string // status line hint when input starts with !

	// slash command + sub-command descriptions shown in the menu (CLI and desktop
	// share these via i18n.M, so both frontends localize identically).
	CmdNew              string // /new
	CmdClear            string // /clear
	CmdCls              string // /cls
	CmdCompact          string // /compact
	CmdContinueChecks   string // /continue-checks
	CmdContext          string // /context
	CmdRewind           string // /rewind
	CmdTree             string // /tree
	CmdBranch           string // /branch
	CmdSwitchBranch     string // /switch
	CmdResume           string // /resume
	CmdRename           string // /rename
	CmdModel            string // /model
	CmdStatus           string // /status
	CmdWorkMode         string // /work-mode
	CmdDocs             string // /docs
	CmdMemory           string // /memory
	CmdMigrate          string // /migrate
	CmdGoal             string // /goal
	CmdRemember         string // /remember
	CmdForget           string // /forget
	CmdMcp              string // /mcp
	CmdRemote           string // /remote
	CmdHooks            string // /hooks
	CmdPlugins          string // /plugins
	CmdPasteImage       string // /paste-image
	CmdOutputStyle      string // /output-style
	CmdTheme            string // /theme
	CmdLanguage         string // /language
	CmdCurrency         string // /currency
	CmdSkill            string // /skills
	CmdVerbose          string // /verbose
	CmdReloadCmd        string // /reload-cmd
	CmdReload           string // /reload
	CmdDiffFold         string // /diff-fold
	CmdSandbox          string // /sandbox
	CmdEffort           string // /effort
	CmdMouse            string // /mouse
	CmdReasonLang       string // /reasoning-language
	CmdHelp             string // /help
	CmdWeb              string // /web
	CmdTodo             string // /todo
	CmdQuit             string // /quit (also accepts /exit as hidden alias)
	CmdCopy             string // /copy
	CmdExport           string // /export
	SlashCopyDone       string // "/copy" succeeded
	SlashCopyEmpty      string // no assistant response to copy
	SlashCopyListHeader string // header shown before the numbered list
	SlashExportDoneFmt  string // "/export" succeeded, %s = file path
	SlashExportEmpty    string // no messages to export
	ArgSkillShow        string // /skills show
	ArgSkillNew         string // /skills new
	ArgSkillPaths       string // /skills paths
	ArgMcpAdd           string // /mcp add
	ArgMcpRemove        string // /mcp remove
	ArgMcpConnected     string // /mcp remove <server> tag
	ArgHooksList        string // /hooks list
	ArgModelCurrent     string // /model <ref> active tag
	ArgEffortAuto       string // /effort auto
	ArgEffortLow        string // /effort low
	ArgEffortMedium     string // /effort medium
	ArgEffortHigh       string // /effort high
	ArgEffortXHigh      string // /effort xhigh
	ArgEffortMax        string // /effort max
	ArgPresetStandard   string // /preset standard
	ArgPresetDelivery   string // /preset delivery
	ArgThemeCurrent     string // /theme <style> active tag
	ArgLanguageAuto     string // /language auto
	ArgLanguageEn       string // /language en
	ArgLanguageZh       string // /language zh

	// management listing notices (the Submit path: desktop / HTTP frontends)
	ListModelsHeaderFmt string // "models (active: %s)"
	ListModelsHint      string // how to switch
	ListMemorySaved     string // "saved memories"
	ListMemoryArchived  string // "archived memories"
	ListMemoryNone      string // no memory docs
	ListSkillsHeaderFmt string // "skills (%d)"
	ListSkillsNone      string // no skills
	ListHooksHeaderFmt  string // "hooks (%d active)"
	ListHooksNone       string // no hooks
	ListMcpHeader       string // "mcp servers"
	ListMcpNone         string // no mcp servers

	// in-chat memory/model/rewind notices.

	MemoryEditHint               string
	ForgetUsage                  string
	ForgetDoneFmt                string
	QuickRememberEmpty           string
	QuickRememberDoneFmt         string
	GoalEmpty                    string
	GoalCurrentFmt               string
	GoalSetFmt                   string
	GoalCleared                  string
	GoalNotRunning               string
	GoalNotPaused                string
	GoalPaused                   string
	GoalPausedReason             string
	GoalPausedFmt                string // %s = stop cause
	GoalRuntimeFmt               string // turns, requests, tokens, work duration
	GoalRuntimeLastReason        string
	ModelSwitchUnavailable       string
	ModelSwitchBusy              string
	ModelAlreadyOnFmt            string
	ModelSwitchingFmt            string
	ModelSwitchedFmt             string
	ModelListHeader              string
	RuntimeSwitchPending         string
	RuntimeReloadQueued          string // /reload queued behind active work; the idle drain runs it
	RuntimeReloaded              string // /reload completed (no generation available)
	RuntimeReloadedGenerationFmt string // /reload completed; %d is the runtime build generation
	WorkModeUsage                string
	// WorkModeDeprecatedNotice is shown once when a legacy /work-mode or
	// /profile command is used. Prefer /preset.
	WorkModeDeprecatedNotice string
	// QualityFloorApplied confirms a quality floor switch.
	QualityFloorApplied      string
	RewindNone               string
	RewindCodeConversation   string
	RewindConversationOnly   string
	RewindCodeOnly           string
	RewindFork               string
	RewindSummarizeFrom      string
	RewindSummarizeUpto      string
	RewindPickTitle          string
	RewindPickHint           string
	RewindRestoreTitleFmt    string
	RewindApplyHint          string
	RewindCoverageTitle      string
	RewindCoverageWarningFmt string
	RewindConfirmHint        string
	RewindUnavailableFmt     string
	RewindEmpty              string

	// skill picker overlay (/skills interactive panel in CLI TUI)
	SkillPickerAvailableFmt      string
	SkillPickerMatchingFmt       string // "%d matching · %d total" when searching
	SkillPickerHint              string
	SkillPickerDetailHint        string
	SkillPickerSearchEmpty       string
	SkillPickerSearchPlaceholder string
	SkillPickerSourceTitle       string
	SkillPickerSourceActiveFmt   string
	SkillPickerSourceHint        string
	SkillPickerDiagHidden        string
	SkillPickerDiagShown         string
	SkillPickerBuiltinSource     string
	SkillPickerRescanned         string
	SkillPickerNoDescription     string
	SkillPickerScopeProject      string
	SkillPickerScopeCustom       string
	SkillPickerScopeGlobal       string
	SkillPickerScopeBuiltin      string
	SkillPickerSubagent          string
	SkillPickerAvailableLabel    string
	SkillPickerDisabledLabel     string
	SkillPickerNoChanges         string
	SkillPickerSourceSkillsHint  string
	SkillPickerSourceSkillsEmpty string
	SkillPickerActionToggle      string
	SkillPickerActionDelete      string
	SkillPickerDeleteTitleFmt    string // "Delete skill %s?"
	SkillPickerDeleteConfirm     string
	SkillPickerDeleteCancel      string
	SkillPickerDeleteHint        string
	SkillPickerDeletedFmt        string // "deleted skill %s"
	SkillPickerMoreAboveFmt      string // "↑ %d more above"
	SkillPickerMoreBelowFmt      string // "↓ %d more below"
	SkillPickerTokenFmt          string // "~%d tok"
	SkillPickerDetailMetaFmt     string // "Scope: %s  Run as: %s"
	SkillPickerSkillsUnit        string // "skills" (used as "%d skills")
	SkillPickerLinesUnit         string // "lines" (used as "+N more lines")
	SkillPickerStatusLabel       string // shown in the TUI status bar while picker is open
	SkillPickerStatusOK          string // "ok" path status label
	SkillPickerStatusMissing     string // "missing" path status label
	SkillPickerStatusNotDir      string // "not-directory" path status label
	SkillPickerStatusUnreadable  string // "unreadable" path status label

	// init wizard
	EnterAPIKeysHeader       string // header before the per-env-var prompts
	WroteFileFmt             string // "Wrote %s" — used for reasonix.toml and .env both
	SetupComplete            string // success line at end of init
	SetupCancelled           string // shown when the user aborts the wizard
	TryHintFmt               string // "Try: %s" — %s = command to try (styled)
	NextHint                 string // non-interactive post-write hint
	ConfirmReconfigureFmt    string // "%s already exists. Reconfigure and overwrite?"
	NotOverwritingFmt        string // non-interactive overwrite refusal
	SetupManagerTitle        string
	SetupAddOpenAI           string
	SetupAddAnthropic        string
	SetupProviderExistsFmt   string
	SetupSaveExit            string
	SetupSaveExitDesc        string
	SetupCancel              string
	SetupCancelDesc          string
	SetupModelsUnit          string
	SetupKeySet              string
	SetupKeyMissing          string
	SetupDefaultBadge        string
	SetupProviderActionsFmt  string
	SetupEditProvider        string
	SetupUpdateKey           string
	SetupTestRefresh         string
	SetupSetDefault          string
	SetupRemoveProvider      string
	SetupBack                string
	SetupPromptModels        string
	SetupSharedKeyWarningFmt string
	SetupPromptAPIKeyFmt     string
	SetupSelectDefaultModel  string
	SetupConfirmRemoveFmt    string
	SetupSummaryTitle        string
	SetupSummaryAddedFmt     string
	SetupSummaryEditedFmt    string
	SetupSummaryRemovedFmt   string
	SetupSummaryDefaultFmt   string
	SetupSummaryKeysFmt      string
	SetupSummaryNoChanges    string
	SetupConfirmSave         string
	SetupConcurrentChangeFmt string

	// model fetching
	FetchingModelsFmt          string // "Fetching models for %s..."
	FetchModelsSuccessFmt      string // "Found %d models for %s"
	FetchModelsFailedFmt       string // "Failed to fetch models for %s: %v"
	FetchModelsUsingPresetsFmt string // "Live fetch unavailable for %s, using preset model list"
	SelectModelsLabel          string // "Select models to enable for %s"
	CustomFetchEmpty           string // "/models returned an empty list — falling back to manual entry"
	AnthropicFetchEmpty        string // "/models returned an empty list — Anthropic-compatible providers usually don't expose one, falling back to manual entry"
	APIKeyAlreadySetFmt        string // "reusing existing value for %s"
	APIKeyResetPromptFmt       string // "Re-enter %s?"
	InvalidAPIKeyEnvFmt        string // "%q is not a valid API Key variable name..."
	RepairedAPIKeyEnvFmt       string // "provider %s: replaced invalid api_key_env %q with %q"

	// custom provider
	CustomProviderDesc   string // "Add third-party OpenAI compatible model"
	CustomAddMethodLabel string // "Select add method"
	CustomMethodManual   string // "Enter model name manually"
	CustomMethodURL      string // "Fetch models from URL"
	CustomPromptModel    string // "Enter model name"
	CustomPromptBaseURL  string // "Enter Base URL"
	CustomPromptKeyEnv   string // "Enter API Key env var name"
	CustomPromptAPIKey   string // "Enter API Key"
	CustomPromptWindow   string // "Enter context window in tokens"
	CustomAddedFmt       string // "Added custom model: %s"

	// Anthropic compatible provider
	AnthropicProviderDesc          string // "Add Anthropic API compatible model"
	AnthropicAddMethodLabel        string // "Select add method"
	AnthropicMethodManual          string // "Enter model name manually"
	AnthropicMethodURL             string // "Fetch models from URL"
	AnthropicPromptModel           string // "Enter model name"
	AnthropicPromptBaseURL         string // "Enter Base URL"
	AnthropicPromptKeyEnv          string // "Enter API Key env var name"
	AnthropicPromptAPIKey          string // "Enter API Key"
	AnthropicAddedFmt              string // "Added Anthropic compatible model: %s"
	AnthropicFetchingModelsFmt     string // "Fetching models for %s..."
	AnthropicFetchModelsSuccessFmt string // "Found %d models for %s"
	AnthropicFetchModelsFailedFmt  string // "Failed to fetch models for %s: %v"
	AnthropicSelectModelsLabel     string // "Select models to enable for %s"

	// remote SSH module
	RemoteConnectingFmt       string // "connecting to %s…"
	RemoteConnectedFmt        string // "connected to %s"
	RemoteReconnectingFmt     string // "reconnecting to %s (attempt %d)…"
	RemoteDegradedFmt         string // "connected to %s but some forwards are down"
	RemoteDisconnected        string // "disconnected"
	RemoteServeReadyFmt       string // "remote serve ready: %s"
	RemoteHostKeyPromptFmt    string // "host %s key (%s): %s"
	RemotePassphrasePromptFmt string // "passphrase for %s:"
	RemotePasswordPromptFmt   string // "password for %s:"
	RemoteBootstrapStepFmt    string // "remote serve: %s %s"
	RemoteNoHostsHint         string // "no remote hosts configured; add one with `reasonix remote add`"

	// top-level / runAgent
	UnknownCommandFmt         string // "unknown command %q"
	UsageRunHint              string // "usage: reasonix run [--model NAME] <task>"
	ErrorPrefix               string // "error:" — prefix for fatal-error output
	ReconfigureOnUnknownModel string // shown when the configured model no longer resolves and setup is re-run
	WriteConfigErr            string // "write config:" — prefix for write failure
	WriteEnvErr               string // "write .env:" — prefix for env-write failure

	// provider HTTP error explanations — actionable, reason + fix per status code
	ProviderErrBadRequest          string // 400
	ProviderErrContextOverflowFmt  string // 400/413/422 shared-window overflow with numbers
	ProviderErrAuth                string // 401 — no key configured / sent
	ProviderErrAuthRejected        string // 401 — a key was sent but the server rejected it
	ProviderErrModelFormatMismatch string // provider rejected the model on the selected wire format
	ProviderErrOpenCodeGoGrokRoute string // recovery hint for OpenCode Go Grok routing
	ProviderErrQuotaExhaustedFmt   string // provider name, actual HTTP status
	ProviderErrReasonMissing       string
	SearchSourcesNotProvided       string
	ProtocolRecoveryLabel          string
	ProviderErrInsufficientBalance string // 402
	ProviderErrUnprocessable       string // 422
	ProviderErrInputSensitive      string // MiniMax 1026
	ProviderErrOutputSensitive     string // MiniMax 1027
	ProviderErrRateLimited         string // 429
	ProviderErrServer              string // 500
	ProviderErrServerBusy          string // 503

	// selection menus
	SelectOneHint      string // "(↑/↓ · Enter · q to cancel)"
	SelectManyHint     string // "(↑/↓ · Space · Enter · q)"
	SelectMoreAboveFmt string // "↑ %d more above"
	SelectMoreBelowFmt string // "↓ %d more below"
	SelectSearchHint   string // "/ to search · Esc to cancel"

	// /provider command
	CmdProvider          string // /provider
	ProviderListHeader   string // header for /provider list
	ProviderAlreadyOnFmt string // already on provider
	ProviderUnknownFmt   string // unknown provider
	ProviderPickLabel    string // label for provider model picker
	ProviderNoModelsFmt  string // provider has no models

	// `reasonix upgrade` / `reasonix update` — self-update
	UpgradeChecking            string // "Checking for updates…"
	UpgradeChannelDeprecated   string // legacy channel selection is ignored
	UpgradeDevBuild            string // dev builds cannot self-update
	UpgradeFetchFailed         string // "failed to check for updates: %v"
	UpgradeInvalidVersion      string // remote version not valid semver
	UpgradeAlreadyLatest       string // already on the latest version
	UpgradeForcing             string // "Reinstalling the same version…"
	UpgradeAvailableFmt        string // "Current: %s → Latest: %s"
	UpgradeNoAssetFmt          string // "no binary found for %s"
	UpgradeDownloadingFmt      string // "Downloading %s (%s)…"
	UpgradeDownloadFailed      string // "download failed: %v"
	UpgradeVerifying           string // "Verifying checksum…"
	UpgradeChecksumFailed      string // "could not fetch checksum file: %v"
	UpgradeChecksumMismatchFmt string // SHA256 mismatch detail
	UpgradeChecksumNotFoundFmt string // asset not listed in SHA256SUMS
	UpgradeExtractFailed       string // "failed to extract binary: %v"
	UpgradeApplying            string // "Replacing binary…"
	UpgradeApplyFailed         string // "failed to apply update: %v"
	UpgradeSuccessFmt          string // "Updated %s → %s"

	// `reasonix report` — local CLI crash review and explicit upload
	ReportNoPending           string
	ReportHeaderFmt           string
	ReportCapturedFmt         string
	ReportPreviewOnlyFmt      string
	ReportSendPrompt          string
	ReportKept                string
	ReportDeletedFmt          string
	ReportSentFmt             string
	ReportConfigFailedFmt     string
	ReportUploadFailedFmt     string
	ReportSentDeleteFailedFmt string
	ReportUsageBody           string

	// First eligible interactive CLI telemetry consent.
	CLITelemetryConsentNotice           string
	CLITelemetryConsentPrompt           string
	CLITelemetryConsentInvalid          string
	CLITelemetryConsentSaveFailedFmt    string
	CLITelemetryConsentCleanupFailedFmt string

	// usage / help
	UsageBody string // full multi-line help text
}

// ProviderStatusMessage returns an actionable explanation for a known provider
// HTTP status, or "" when the status has no specific guidance.
func (m Messages) ProviderStatusMessage(status int) string {
	switch status {
	case 400:
		return m.ProviderErrBadRequest
	case 401, 403:
		return m.ProviderErrAuth
	case 402:
		return m.ProviderErrInsufficientBalance
	case 422:
		return m.ProviderErrUnprocessable
	case 429:
		return m.ProviderErrRateLimited
	case 500:
		return m.ProviderErrServer
	case 503:
		return m.ProviderErrServerBusy
	}
	return ""
}

// M is the active catalogue. DetectLanguage replaces it; English is the
// default so any code path that runs before detection still has text.
var (
	M               = English
	currentLanguage = "en"
)

// CurrentLanguage returns the language tag installed by the latest
// DetectLanguage call. It lets frontends reuse the resolved locale without
// re-reading the environment and accidentally ignoring an explicit override.
func CurrentLanguage() string {
	return currentLanguage
}

// DetectLanguage selects a catalogue from override (e.g. cfg.Language) or the
// environment and installs it as M. Returns the resolved tag ("en", "zh") so
// callers can log or expose it.
//
// Priority: override > REASONIX_LANG > LC_ALL > LC_MESSAGES > LANG > "en".
func DetectLanguage(override string) string {
	for _, c := range append([]string{override}, envCandidates()...) {
		if tag := normalize(c); tag != "" {
			return setLanguage(tag)
		}
	}
	return setLanguage("en")
}

func envCandidates() []string {
	keys := []string{"REASONIX_LANG", "LC_ALL", "LC_MESSAGES", "LANG"}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = os.Getenv(k)
	}
	return out
}

func setLanguage(tag string) string {
	switch tag {
	case "zh-tw", "zh-TW":
		M = ChineseTraditional
		currentLanguage = "zh-TW"
	case "zh":
		M = Chinese
		currentLanguage = "zh"
	default:
		M = English
		currentLanguage = "en"
	}
	return currentLanguage
}

// normalize maps a locale string (e.g. "zh_CN.UTF-8", "zh-Hans-CN", "Chinese
// (China)") to a short tag this package knows about. Returns "" for empty or
// unrecognised input so DetectLanguage can fall through to the next candidate.
func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-") // zh_TW.UTF-8 → zh-tw.utf-8 (POSIX locales use underscores)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "zh-tw") || strings.HasPrefix(s, "zh-hant") || strings.Contains(s, "chinese traditional") || strings.Contains(s, "繁體") {
		return "zh-TW"
	}
	if strings.HasPrefix(s, "zh") || strings.Contains(s, "chinese") || strings.Contains(s, "中文") {
		return "zh"
	}
	if strings.HasPrefix(s, "en") || strings.Contains(s, "english") {
		return "en"
	}
	return ""
}
