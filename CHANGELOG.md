# Changelog

All notable changes to the Go line (Reasonix 1.0+) are recorded here. The legacy
`0.x` TypeScript history lives on the [`v1`](https://github.com/esengine/DeepSeek-Reasonix/tree/v1)
branch.

## Unreleased

### Added

- **MCP 2026-07-28 protocol:** multi-round-trip form/URL elicitation across
  Desktop, CLI TUI, and serve; headless entries stay on the core surface and
  cancel unanswered requests instead of guessing.
- **MCP Apps 2026-01-26 (Desktop):** inline app surfaces in tool cards behind
  a per-server double-iframe sandbox, app-tool visibility metadata, bounded
  aggregate local presentations, tab-bound AppBridge routing and teardown,
  immutable digest-bound resource snapshots, and confirmed external links;
  local rich results, instance-gated app tool calls, and the four-layer
  capability matrix in MCP status.
- **Profile-scoped MCP schema caches:** capability-declaring hosts keep their
  own `v3` cache files so catalogs negotiated under different client
  capabilities never cross-read.

### Changed

- **Fact-driven execution:** Ordinary requests always enter the executor.
  There is no automatic simple / light / full task mode and no per-turn
  `TaskPolicy` classification. The planner runs only for an explicit Plan,
  an approval boundary, or Goal start. The host builds verification
  obligations from concrete tool effects and receipts. Plan, Goal,
  permission, and sandbox stay independent. Tool schemas and the executor
  system prefix stay byte-stable. Historical `<execution-policy>` tags remain
  readable on old sessions and are stripped from new provider context.
  Old `--preset`/`--profile` compatibility no-ops are unchanged.

- **Remote connect wizard host picker:** Step 1's host field now opens the
  saved SSH connections through an explicit chevron dropdown on the input's
  right edge instead of the old focus-triggered popup. The dropdown lists
  every saved connection unfiltered, appends non-standard ports to each row,
  leads with a "saved SSH connections" caption, and closes on pick, arrow
  toggle, Escape (before the Escape that exits the wizard), or an outside
  pointer press. The arrow is hidden while no hosts are saved and disabled
  while a connection is busy.

### Fixed

- **Interrupted turns:** a tool call cancelled after its start barrier is now
  recorded as `unknown`, never `not_started`; an unknown side effect stops
  later writers in the same batch; explicit failures stay `failed` instead of
  reading as unknown in recovery; cancellation with an unproven side effect or
  a silent interruption ends the turn as `recovery_required`; every provider
  request passes a final transcript gate (#9825, #9566).
- **回合中断：** 越过起始屏障后被取消的工具调用记为 `unknown`，不再是
  `not_started`；unknown 副作用会阻止同一 batch 的后续写入；明确失败保持
  `failed`，恢复块不再把它当作 unknown；存在未证明副作用或静默中断的取消以
  `recovery_required` 结束回合；每次 provider 请求前都经过转录门（#9825、#9566）。

- **Restart after a crash:** reopening a session ledger now answers each
  unanswered tool call from its persisted start barrier — started calls read as
  `unknown`, never-started ones as `cancelled` — escalates the recovered turn to
  `recovery_required` when an effect is unproven, and hands that evidence to the
  next turn instead of treating every interrupted call alike. Checkpoints record
  the interrupted attempt's identity (#9825).
- **崩溃后重启：** 重开会话账本时，按持久化的起始屏障答复每个未答复的工具调用
  ——已启动读作 `unknown`，从未启动读作 `cancelled`——效果未证明时把恢复出的回合
  升级为 `recovery_required`，并把该证据交给下一回合，而不是把所有被中断的调用
  一视同仁。checkpoint 记录被中断尝试的身份（#9825）。

- **Resolving an interrupted tool:** a user who inspected the workspace can now
  mark an unverified effect as already done — recorded with provenance, never as
  a fabricated tool result, and refused for calls the host already proved. Session
  loads no longer mask that proof: the placeholder backfilled for an unanswered
  call is no longer read as evidence of an unknown outcome. Ordinary tool
  interruption never forks a session (#9825).
- **解决被中断的工具：** 检查过工作区的用户现在可以把未经验证的副作用标记为已完成
  ——记录出处、绝不伪造工具结果，对宿主已证明的调用则拒绝。会话加载不再掩盖该证明：
  为未答复调用回填的占位结果不再被当作 unknown 结果的证据。普通工具中断永不 fork
  会话（#9825）。

- **Relay image input:** ID-only or invalid model metadata now stays unknown.
  Both Desktop model editors expose per-model Auto / On / Off overrides, with
  official protocol limits retained. A separate V2 discovery cache rejects stale
  results; saved settings and runtime image serialization share one resolver and
  apply at Controller rebuild boundaries. Legacy configuration remains readable.
- **中转站图片输入：** 缺失或无效的模型能力显示“图片能力未识别”，两个编辑入口
  均可逐模型选择“自动 / 开启 / 关闭”。独立 V2 缓存隔离旧错误声明并防止陈旧结果
  覆盖；保存设置与实际图片请求统一解析，在 Controller 重建边界生效，兼容旧配置。

- **Deterministic natural-turn completion:** removed the extra completion
  validator model request. Clean model stops now finish from provider/tool state;
  true zero-content responses retry the frozen request at the Agent step
  boundary, while explicit host-owned readiness and safety gates remain active.
  Legacy completion-validator configuration and `completion_uncertain` event
  values remain readable for compatibility but are no longer produced by the
  validator path.

- **serve Host-header allowlist:** `reasonix serve` now rejects requests whose
  `Host` is neither loopback nor the actual listen address (HTTP 421), closing
  the DNS-rebinding bypass of the JSON content-type CSRF guard — a rebind page
  becomes same-origin with the loopback listener and could previously drive
  `/bypass`, `/submit`, and read `/history`. `behind_proxy` deployments and
  wildcard/non-loopback binds are exempt. The non-loopback plaintext-HTTP
  startup warning now also fires — loudest — for the unauthenticated `auth =
  none` case that used to stay silent.

- **Preview read confinement:** `write_file` / `edit_file` / `multi_edit`
  previews now apply the same `confinePreview` boundary as `delete_range` /
  `delete_symbol`. A model-supplied absolute path outside the workspace roots
  previously read the file (rendering its contents into the approval card and
  session log) even though Execute would refuse the write.

- **Clean-filter hardening on internal diffs:** gitcmd diff invocations now
  neutralize every `filter.<driver>` defined in the repository's local
  `.git/config` (`clean=` emptied, `required` forced off), so viewing a changed
  file's diff can no longer execute a repository-configured clean filter via
  `.gitattributes`. Emptied filters are identity pass-throughs: the diff still
  renders the real working-tree change.

- **install_source proxy SSRF parity:** the install_source SSRF dial guard now
  also validates the request destination (IP literals) at the RoundTripper
  boundary, so a configured HTTP/HTTPS proxy can no longer forward a blocked
  target (cloud metadata, RFC1918, link-local, CGNAT) that the dial-time check
  never sees — matching web_fetch's proxy-path behavior.

- **awk approval classification:** the bash indirect-execution classifier now
  treats `awk`/`gawk`/`mawk`/`nawk` with an inline program (anything not read
  via `-f`/`--file`) like `python -c`: it always requires human approval and
  can never be covered by a remembered reusable prefix rule. `awk
  'BEGIN{system("…")}'` previously fell through to the reusable class.

- **cargo check/doc read-only correction:** the legacy read-only command table
  no longer lists `cargo check` / `cargo doc` as permission readers — cargo
  executes the crate's `build.rs` for both. The effect classifier already
  billed them as code-executing writers; the stale table entry (and its test)
  now agree. Only `cargo search` remains read-only.

- **Compact MCP discovery:** `use_capability(action=list)` now returns one
  compact summary per configured MCP server instead of expanding every cached
  tool description, including tools from disabled servers. Inspecting one
  enabled `mcp-server:<name>` still returns its live or cached directory
  without starting it, while direct known-ID calls, routing, authorization,
  and the fixed provider-visible tool schema remain unchanged.

- **Project MCP session reliability:** The MCP client now uses the official Go
  SDK for stdio, legacy SSE, and Streamable HTTP while retaining Reasonix's
  existing configuration, OAuth, process isolation, and schema-cache contracts.
  Streamable HTTP opens its long-lived GET/SSE listener immediately after
  initialization, so JetBrains project-level `.mcp.json` servers no longer lose
  their pending session before the first tool call. Lost sessions converge on
  one bounded rebuild and one replay, read-only surfaces consume every cursor
  page, prompts/resources share the tool session, and shutdown terminates HTTP
  sessions and local processes. MCP calls also accept a single JSON-object
  string in `use_capability.arguments`, while rejecting arrays, scalars, invalid
  JSON, and nested encoded strings. `/mcp` and Desktop expose redacted protocol,
  listening, reconnect, and error-category diagnostics without session IDs.

- **v1.24.2 session snapshot & recovery root fix:** Keep PR #7982's WAL/CAS/lease
  safety foundation, but replace process-level "I hold a lease" ownership with a
  generation-bound `SessionWriteAuthority`. Same-revision tool-preview/load
  reshapes no longer false-diverge; recovery files are bounded to one path per
  writer/lineage; empty checkpoints heal from their own WAL; projection lineage
  rebinds across upgrade/model switch and inherits across recovery forks without
  changing provider-visible prompt bytes. Catalog upgrades to disposable
  `session-catalog/v3.sqlite` with recovery lineage roles
  (`normal|covered_copy|adopted|diverged`); covered idle copies move to the
  recoverable `.trash` using a 15-minute idle threshold applied on two early
  sweeps (at startup and ~20 minutes later), then a 24-hour threshold on the
  6-hour background ticker; independent diverged branches stay and are listed
  for user choice. v1/v2 catalogs are
  left byte-unchanged for coexistence/downgrade.
  **v1.24.1** only hid/reclaimed already-created covered copies and fixed Windows
  flash-window startup; **v1.24.2** stops the misclassification source and repairs
  existing user directories without rewriting authoritative JSONL/WAL/sidecar data.

- Goal now runs continuously by default: the former 16-round per-Run boundary,
  10/20/40 cross-Run quotas, default wall-clock budget, and numeric
  no-progress/Todo-stall pauses no longer stop valid work. Progress guards still
  detect repeated host outcomes and zero-evidence work, but redirect the model
  to re-plan instead of producing `goal_run_budget` or `goal_stuck`. Explicit
  `[agent].goal_token_budget`, `--max-steps`, positive time/cost budgets, manual
  pause/stop, genuine user/external blockers, and evaluator fail-closed behavior
  remain available. The Goal token budget defaults to `0` (off); resuming its
  `budget_spend` pause grants a fresh slice without clearing cumulative usage.
  Goal status reports turns, provider requests, tokens, the optional configured
  token threshold, and cumulative active work time. Bot `max_steps` also
  defaults to `0` (continuous), while positive user configuration is enforced.

- Removed numeric Goal pauses in existing sidecars automatically normalize to
  `running` without sending a model request. Active Goal sidecars write
  `turnsLimit: -1` as a downgrade-safe unlimited sentinel while public runtime
  APIs retain deprecated limit fields as `0`. The migration preserves unknown
  fields, todos, checkpoints, usage, evidence, and historical metadata.

- Goal is now the sole long-task runtime. Historical AutoResearch sidecars
  migrate transactionally into Goals with research compatibility metadata. Invalid archives block
  fail closed and remain read-only, retaining the task id and compatibility mode
  for a restart or `/goal resume` retry; successful Goal-only sidecars omit the
  old task id and write an explicit downgrade fence so previous readers cannot
  reactivate the removed runtime.

- Context-dependent workflow tools now share one host-side execution boundary.
  Goal, Plan sign-off, and background-job calls cannot reach permissions,
  hooks, leases, or Execute outside their owning context; mixed batches execute
  valid calls once and stop safely after one repair. Child agents also isolate
  inherited Goal, Jobs, and live memory queues, while persisted tool identity
  records the effective child schema projection.

- **Issue #7575:** Linux Bash under bubblewrap no longer mounts a fresh empty
  `--tmpfs /tmp` on every call. Consecutive commands in the same logical session
  now share a private temporary directory (bound at `/tmp` on Linux, exported via
  `TMPDIR`/`TMP`/`TEMP` on all platforms) without exposing the host public
  temporary root. `/new`, `/clear`, resume of another session, and branch
  switches rotate the directory; model/settings hot rebuilds keep it. Sub-agent
  runs get independent directories. Temporary files are not durable across process
  restarts.

### Added

- Added `[ui].show_turn_usage` so CLI/TUI users can hide per-request token and
  cost receipts from transcript scrollback without disabling usage accounting.

## [1.20.0] — 2026-08-05

Extension kernel, Task Monitor, and safer Goal completion.

Compact decision surfaces, local decision receipts, unified extension kernel,
native Task Monitor, bounded sub-agent progress, Goal fail-closed completion,
MiMo and DashScope Responses fixes, SSH remote access simplification, and
multiple Desktop stability improvements.

### Highlights

- **Unified Extension Kernel and Extension Protocol v1**: Immutable runtime
  snapshots, fail-atomic reload, Plugin Manifest v1 (prompts, themes, full-trust
  code runtimes), stable JSON-RPC sidecar protocol, interceptor dispatch,
  streaming provider adapter, structured UI, and Go SDK.
- **Native Task Monitor**: Monitor agent tasks natively in CLI and Desktop with
  lifecycle semantics and session-scoped summary view.
- **Bounded Sub-agent Progress Forwarding**: Forward structured progress for
  `task`, `parallel_tasks`, and `fleet` without flooding the parent stream.
  Renders nested lifecycle cards in Desktop and stable per-child transcript
  slots in CLI.
- **Goal Completion Fail-Closed**: Replace free-form Goal footer markers with a
  stable `update_goal` tool and epoch-scoped per-turn reports. Centralized
  completion logic with bounded evaluator, progress-aware budgets, and
  pause/resume controls.
- **Ablation Subsystem Switches**: Switch subsystems off behind one shared
  vocabulary for controlled experiments. Includes planner, subagent, retrieval,
  evidence, and compaction.
- **Benchmark Cost per Solved Task**: Report cost per solved task, tokens per
  solved, median wall time, and failure-class breakdown in e2e reports.
- **Compact Decision Surfaces and Local Receipts**: Compact footer decision-card
  layout with bounded scroll, dense action rows, and overflow disclosure.
  Record bounded Ask, approval, and recovery decisions as local transcript
  receipts.
- **Simplified SSH Remote Access**: Remove Remote Workbench protocol and
  stacks; reuse CLI/Serve remote model. Desktop opens per-host native web child
  windows via SSH. Keyless remote Serve setup with loopback-only page.
- **Model Usage Charts with Primer Palette**: Replace monochrome accent ramp
  with GitHub Primer data-viz two-set categorical palette. Fix donut overflow
  on hover and keyboard accessibility.
- **Cross-platform Extension and Task Monitor Reliability**: Make
  content-reference eviction deterministic, reject Unix and Windows absolute
  plugin paths consistently, stabilize parallel-task cancellation, and restore
  reliable Windows validation for Task Monitor and remote provider setup.
- **MiMo and DashScope Responses Wire Alignment**: Fix multi-turn tool loops,
  reasoning round-trip, JSON output for MiMo; fix DashScope second-turn 400
  error, all-zero usage suppression, and vendor-aware cache TTL.
- **Desktop Stability Fixes**: Recover stuck updates and legacy WebKit, contain
  macOS alias repair startup crashes, keep composer overflow stacks readable,
  and harden account verification and community flows.
- **Remote Web Recovery After SSH Drops**: Add integration regression test for
  SSH drop, forward recovery, and window reload. Document transient outage
  behavior.
- **CI: Auto-minimize Activity-Farming Spam Comments**: Detect and minimize
  template spam comments from non-contributor accounts based on structural
  signals.

### Added

- Added Extension Protocol v1 and the unified extension kernel: installed or
  linked sidecars can contribute tools, skills, commands, hooks, MCP servers,
  providers, interceptors, and structured UI surfaces through a versioned
  NDJSON contract and the public Go SDK. CLI, Desktop, ACP, and Serve support
  fail-atomic runtime reloads; Serve also renders extension surfaces and lists
  extension-hosted providers without exposing credentials.
- Added the structured Goal completion protocol: the always-registered
  `update_goal` tool (continue/complete/blocked with reason and next_action)
  replaces the `[goal:*]` footer markers. The Goal FSM is now the exclusive
  cross-turn decision point and validates every complete claim against Delivery
  readiness; when the model submits no report, an independent bounded evaluator
  (recovery_model → guardian_model → main model, no tools/history, usage
  attributed to `goal-evaluator`) judges the turn once, and any evaluator
  failure pauses the goal instead of continuing silently.
- Added Goal budget classes with safe pauses: simple 10 turns / 200k tokens,
  write 20 turns / 400k tokens, AutoResearch 40 turns / 800k tokens, and a
  4-turn no-host-verifiable-progress gate. Pauses keep all Goal state; `/goal
  resume` continues and adds one slice of the current class when the pause was
  budget-related. `/goal status` shows the full turn/token/no-progress runtime,
  and `/goal pause` manually suspends a running Goal.
- Added the `goalRuntime` nested view to the desktop Meta, the remote protocol
  (`session/goal/pause` operation, `goalRuntime` DTO on session meta), and the
  ACP status payload; the desktop Composer goal menu shows the runtime summary
  with distinct pause/end/resume actions.

### Changed

- Delivery no longer retries final-answer readiness with hidden model messages:
  a plain Delivery run ends on the first unsatisfied final answer and surfaces
  the recovery card, while a Goal + Delivery run has the Goal FSM absorb the
  failure and continue under budget with the missing requirements as the next
  turn's prompt. Historical `[goal:*]` footers are stripped from old transcripts
  for display only and never participate in state decisions.
- Added a **Remote SSH** module (VS Code Remote-SSH style): a user-global
  `[remote]` host config, `reasonix remote` CLI (add/list/remove/import/test/
  connect/status/forward/serve/fs) and `/remote` slash command, an SSH transport
  with trust-on-first-use host-key verification, keepalive + exponential-backoff
  reconnect, `-L`/`-R` port forwarding, and SFTP file access. `connect`
  bootstraps a persistent `reasonix serve` on the remote host and tunnels its
  loopback port so the full agent runs remotely. The desktop app adds a
  **Settings -> Remote SSH** host manager, a remote file browser/editor, a
  port-forwarding panel, and a status-bar connection chip. Linux/macOS remotes.
- Added `reasonix serve --port-file/--token-file/--pid-file` so a supervised
  headless serve can bind an ephemeral port and read its auth token from a file
  (keeping it out of `ps`).
- Added an authenticated, loopback-only Provider setup page for `reasonix
  serve`. A Serve whose selected Provider is missing its API key now remains
  reachable, stores the submitted key in that host's Reasonix credential file,
  and rebuilds the active controller in place without restarting Serve.
- Added Claude Code-style searchable CLI pickers for models, providers, and
  sessions, with arrow, Vim, and `Ctrl+P` / `Ctrl+N` navigation.
- Added `-p` / `--print`, `text`, `json`, and `stream-json` output modes for
  one-shot use and automation.
- Added session-scoped `--allowed-tools`, repeatable `--add-dir`, Claude-compatible
  permission modes, flexible `--resume [QUERY]`, and the `--copy` resume escape
  hatch.
- Added `/status` details for the active model, effort, cache, Git state,
  background jobs, work profile, and provider balance where available.
- Remote SSH workspaces now open as a standalone remote web window again.
  Opening a workspace from the status bar or the Remote Server tab starts or
  reuses the remote `reasonix serve`, tunnels its loopback port, and opens the
  Serve web client in a dedicated per-host window. The remote web page uses
  the provider configuration and API keys on the **remote** host; the desktop
  no longer exposes its local providers to remote hosts. If the selected remote
  Provider is missing its API key, the window opens a setup page that saves the
  key only on that host and then opens the normal Serve UI. The Remote Workbench
  protocol, its Provider Broker, and the same-window remote projection were
  removed. Legacy mirror and provider-trust files are not deleted
  automatically; Settings -> Remote SSH shows a cleanup card when they exist.
  The hidden `remote attach-workspace`, `remote runtime-workbench`, and
  `remote workbench-build-id` commands now fail with a pointer to
  `reasonix remote connect <host> --open`.
- Automatic Plan Mode has been retired. Plan Mode is now always entered through
  an explicit user choice, and the one-time config v5 upgrade removes legacy
  `agent.auto_plan` and `agent.auto_plan_classifier` values so upgraded users
  receive the same behavior as new users.
- `Shift+Tab` now cycles CLI safe modes from Ask to Auto to Plan, while YOLO
  remains an independent `Ctrl+Y` toggle.
- Model, provider, resume, and approval menus now use consistent row selection;
  slash completion, help, aliases, and dispatch share one command registry.
- The full-screen CLI composer now uses theme-accented borders and a slim bar
  cursor by default, grows within the available terminal height, scrolls long
  drafts independently, and preserves selections across explicit image paste.
- The persistent CLI footer now uses a responsive, theme-aware layout for
  interaction state, model, effort, localized work mode, Git identity, cache,
  context, compaction headroom, jobs, and balance. Narrow terminals move or
  compact complete groups instead of clipping labels.
- CLI clipboard actions now separate terminal-native text paste from explicit
  image paste: `Ctrl+V` on macOS/Linux, `Alt+V` on Windows, or `/paste-image`.
  Local transcript copy verifies the native clipboard write, while SSH uses a
  clearly labelled OSC 52 fallback.
- Runtime rebuilds after model, effort, or work-mode changes now preserve the
  conversation, session permission overrides, additional directories, and
  session lease ownership.
- Agent execution now monitors host-observed Todo progress automatically. A
  stalled current item receives a recovery nudge after 8 tool-call rounds with
  no new completion, unique read, command, or mutation, and pauses with saved
  work after 16. Exact repeats do not renew the progress lease; real work does.
  Two-level task lists keep the single in_progress contract: the active
  sub-step is the only current item while its phase stays pending, and the
  phase becomes in_progress to sign off only after all of its sub-steps are
  completed. A level-1 sub-step with no phase header above it is rejected.
  Executor and planner rounds now use automatic progress management. Retired
  `[agent].max_steps` and `planner_max_steps` keys remain parseable for upgrades,
  but are ignored and removed by a one-time migration so stale hidden limits
  cannot truncate new behavior. One-off CLI and unattended bot limits remain.

### Fixed

- Fixed long parallel sub-agent research being silently lost when combined
  `parallel_tasks` or `fleet` answers exceeded the single-tool output limit.
  Persisted sessions now keep each child transcript independently, return a
  bounded fair preview plus stable reference for every result, and page full
  answers through the conversation-scoped `read_subagent_result` tool.
- Fixed Remote Workbench failing with only `initialize: workbench-desktop:
  connection closed` on fresh or cross-platform SSH hosts. Desktop now proves
  the exact Host CLI Build ID, provisions the matching verified release without
  requiring remote npm, runs the managed binary explicitly, and preserves a
  safe structured bootstrap error when the remote command exits early.
- Hardened Bash permission reuse for dynamic and indirect execution. Parameter/arithmetic expansions,
  assignments, redirects, heredocs, and globs can only be remembered as exact
  `Bash=<literal>` rules, while still using Auto's normal fallback. Nested or
  indirect execution now requires a human in interactive Ask/Auto and fails
  closed in headless Ask/Auto/DontAsk. Broad Bash rules, Guardian/hook allows,
  and the approved-plan window can no longer silently authorize that stricter
  class; YOLO remains the explicit full-access bypass and sandbox enforcement
  is unchanged.
- Fixed Desktop sessions incorrectly locking themselves during Goal + Delivery
  mode changes, controller rebuilds, duplicate-tab restore, and background
  reattachment. Desktop now keeps one process-local runtime owner per canonical
  session, fences stale controller events by runtime epoch, blocks sends until
  that runtime is ready, and scopes single-instance ownership to
  `REASONIX_HOME` instead of the executable path. Switching saved sessions is
  now transactional: a target build, restore, or lease failure leaves the
  current controller, lease, path, mode profile, and runtime epoch untouched.
- Stabilized the desktop rich composer caret after skill and plugin invocation
  tags. DOM→model and model→DOM selection mapping now treat invocation chips as
  zero-length atoms while still counting user text that lands inside the NBSP
  caret anchor (common on Windows WebView2), restore both selection ends, and
  recover the insertion point from a `beforeinput` snapshot when the browser
  temporarily loses selection — so mid-text edits no longer jump to the end.
- Isolated the Windows desktop WebView2 shell from stale system proxies, so an
  exited proxy client cannot leave the embedded UI hidden during startup. If
  WebView2 still does not reach DOM-ready within 15 seconds, Reasonix now shows
  the native window with a recovery prompt instead of appearing not to launch.
  Remote Markdown images are fetched by the backend with Reasonix's proxy
  configuration instead of bypassing that proxy through the isolated WebView.
- Restored captured-mouse right-click text paste, made composer drag selection
  copy through the verified native clipboard path, and kept non-Git footer
  telemetry left-aligned without reserving an empty data band.
- Restored stateful MCP behavior after the v1.17.13 regression: user-added
  servers work without extra trust settings (including delivery-mode on-demand
  calls), repository-provided servers use one exact launch confirmation, and
  stdio tools reuse one persistent process so browser sessions survive across
  calls without repeated startup latency. The former trust/reverify/catalog
  management UI and CLI are removed.
- Localized persistent-footer labels and displayed work-mode values in English,
  Simplified Chinese, and Traditional Chinese, while keeping command arguments
  stable.
- Restored the `0.53` content boundary: model output, tool output, session
  transcripts, recovery branches, and background-job artifacts retain their
  original text instead of being rewritten by heuristic secret redaction.
  Credential masking remains in key-entry summaries and explicit diagnostic or
  session-cleanup paths. Transcript-bearing session/job sidecars are kept
  private (`0600`, with private job directories), and the retired
  `redact_tool_output` setting is removed with a one-time upgrade notice.

### Notes

- Full bilingual release notes:
  <https://reasonix.io/changelog/v1.20.0/> ·
  [GitHub release](https://github.com/esengine/DeepSeek-Reasonix/releases/tag/desktop-v1.20.0).
- The detailed entries below accumulated on `main-v2` after 1.0.0 and shipped
  across 1.1.0–1.20.0; per-version attribution lives in the per-version release
  notes linked above.

## 1.1.0 – 1.19.7

Per-version entries for the intermediate releases are published in the
[bilingual release notes](https://reasonix.io/changelog/) and on the
[GitHub releases page](https://github.com/esengine/DeepSeek-Reasonix/releases).

## [1.0.0] — 2026-06-03

First stable release — a **ground-up rewrite in Go**. Not an upgrade of the `0.x`
TypeScript line; a new codebase that becomes the default (`main-v2`).

### Highlights

- **Go kernel**: a single static binary (CGO-free), cross-compiled for
  darwin/linux/windows on amd64 + arm64. Distributed via npm (the package wraps
  the native binary), Homebrew (`esengine/reasonix` tap), and release archives;
  no Node runtime needed to run it.
- **Agent core**: the loop, built-in tools (read/write/edit/multi_edit/glob/grep/
  ls/bash/web_fetch/todo_write), permission gate, sandboxed bash, and the
  DeepSeek prefix-cache–oriented design.
- **Subagents**: `task` plus explore/research/review/security_review skill agents.
- **Skills & hooks**: Claude-Code-style skills (`internal/skill`) and hooks
  (`internal/hook`), symlink-aware and slash-integrated.
- **MCP client**: connect external servers over stdio / Streamable HTTP; reads
  `[[plugins]]` and a Claude-Code `.mcp.json`.
- **Code intelligence via CodeGraph**: a tree-sitter symbol/call graph
  (`codegraph_*` tools) replaces embedding semantic search — no embedding service
  or API cost. Fetched into a local cache on first use (or `reasonix codegraph
  install`) and indexed in the background, so installs and startup stay fast.
- **Plan mode** with evidence-backed step sign-off (`complete_step`).
- **Memory**: `REASONIX.md` hierarchy + auto-memory, folded into the cache-stable
  prefix.
- **ACP** (`reasonix acp`) and an HTTP/SSE server frontend; desktop app (Wails).

### Fixed

- **File encoding support restored** — GBK/GB18030 (and other non-UTF-8) files
  can now be read, edited, and grepped correctly. The v2 rewrite had dropped
  v1's encoding detection; files in CJK Windows charsets were silently misread
  or rejected as binary. The read/edit/write round-trip now preserves the
  original file encoding. (#2637)

### Notes

- Versions: the legacy TypeScript line stays in `0.x`; the Go line starts at
  `1.0.0`. See [docs/MIGRATING.md](docs/MIGRATING.md).
- Release archives ship a bare binary; CodeGraph is fetched on first use. Windows
  support for the fetched runtime is unverified — install `codegraph` on PATH if
  the auto-fetch doesn't resolve there.

[1.20.0]: https://github.com/esengine/DeepSeek-Reasonix/releases/tag/desktop-v1.20.0
[1.0.0]: https://github.com/esengine/DeepSeek-Reasonix/releases/tag/v1.0.0
