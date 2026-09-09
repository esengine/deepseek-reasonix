# Tool Contract

<a href="./TOOL_CONTRACT.zh-CN.md">简体中文</a>

This document records the provider-visible contract for Reasonix compile-time built-in tools. It is generated from the same canonical schema path used by the runtime registry.

| Tool | Read-only | Description |
| --- | --- | --- |
| `bash` | false | Execute a command in the shell and return combined stdout/stderr. Use for builds, tests, git, package managers, etc. To search/read/list/edit/move files, prefer the dedicated tools (grep, read_file, ls, glob, edit_file, move_file) over shell grep/cat/ls/find/sed/mv/Move-Item - they behave identically on every OS. For symbol search or architecture questions, prefer LSP/read tools and targeted grep before shell commands. |
| `bash_output` | true | Read new output from a background job started with bash(run_in_background=true) or task(run_in_background=true). Returns the output produced since the last bash_output call for that job, plus its status (running/done/failed/killed). Does not block. |
| `code_index` | true | Lightweight built-in code symbol index. Prefer lsp_* for language semantics and installed code graph MCP tools for call graph, impact, and architecture relationships; use this as the local fallback for file outlines and symbol definition candidates, then verify with read_file or grep. |
| `complete_step` | true | Record the evidence-backed completion of ONE step of an approved plan. Call it as you finish each step instead of silently moving on: it signs the step off with PROOF it is done - the verification you ran (command + result), the diff/files you changed, or a manual check. A completion with no evidence is REJECTED, so don't claim a step is done until you can show why. The host advances the task list for you when you sign off - it marks this step completed and moves the next to in_progress, so you don't need a separate todo_write to mark completions. Fields: `step` (which step - its title or number, matching the task list), `result` (what is now true/changed), `evidence` (>=1 item, each with `kind` = verification\|diff\|files\|manual and a `summary`, plus optional `command`/`paths`), and optional `notes`. |
| `compress` | true | Compress a selected part of the current model-visible conversation without deleting visible history. Use only when the user explicitly asks for context compression. Choose `before` to summarize everything before the uniquely matched user turn while keeping that turn and later context, or `after` to summarize from that turn through the last completed turn while keeping the active turn. The anchor must be an exact, unique excerpt from a real user message; use a longer excerpt if the tool reports multiple matches. |
| `delete_range` | false | Delete a contiguous text range from a file using exact start/end text anchors. Each anchor must match exactly one line. Returns unified diff on success. Use for large deletions - smaller changes should use edit_file. |
| `delete_symbol` | false | Delete a named symbol (function, method, type, interface, const, var) from a Go source file using AST parsing. For non-Go files, use delete_range with manual anchors. |
| `edit_file` | false | Replace an exact string in a file with another. old_string must occur exactly once; add surrounding context to disambiguate. Use for targeted edits instead of rewriting the whole file. |
| `glob` | true | Find files matching a glob pattern (e.g. "*.go", "internal/*/*.go", "**/*.test.ts"). Supports shell metacharacters * ? [] and the recursive ** pattern. Independent globs with no data dependency should be issued in the same round. |
| `grep` | true | Search for a regular expression in a file, or recursively under a directory (skips hidden files and files matched by .gitignore). Returns matching lines as path:line:text, capped at 200 matches. Independent searches with no data dependency should be issued in the same round. |
| `kill_shell` | false | Terminate a running background job (bash or task) started with run_in_background. A no-op if the job has already finished or the id is unknown. |
| `ls` | true | List the entries of a directory. Directories are shown with a trailing slash; files show their byte size. Set recursive=true to list all nested files depth-first (skips .git/node_modules). Independent directory reads with no data dependency should be issued in the same round. |
| `move_file` | false | Move or rename a file from source_path to destination_path. Creates the destination parent directory as needed. Use instead of shell mv, Move-Item, or ren for file moves so workspace confinement and file-edit permissions apply. |
| `multi_edit` | false | Apply a list of edits to a single file atomically: each edit runs against the result of the previous one, all in memory; the file is rewritten only if every edit succeeds. Cheaper and safer than chaining edit_file calls - a failure in step 3 leaves the file untouched instead of half-edited. |
| `notebook_edit` | false | Edit one cell of a Jupyter notebook (.ipynb). Target a cell by 0-based cell_number (or cell_id). edit_mode: "replace" (default) swaps the cell's source; "insert" adds a new cell after cell_number (use -1 to prepend at the top), taking cell_type and new_source; "delete" removes the cell. cell_type is "code" or "markdown" (required for insert). Editing a code cell clears its outputs. Prefer this over edit_file for notebooks - it keeps the JSON valid. |
| `read_file` | true | Read a text file with optional line offset/limit. Output prefixes each line with its 1-based number so subsequent edit_file calls can target exact lines. `intent` states why: `inspect` (default with no window, a bounded preview), `range` (default with offset/limit, one explicit window), `full` (scan the whole file, paging to the end). Use `offset` and `limit` to page through large files; the tool reports pagination hints in a trailer. Pass the `cursor` from a continuation result back unchanged to resume at the host's exact next position instead of computing an offset. Independent reads with no data dependency should be issued in the same round. |
| `todo_write` | true | Record and update a structured task list for the current work. Send the COMPLETE list every call - it replaces the previous one. Use it to plan multi-step work and show progress: keep exactly one item in_progress at a time, and flip an item to completed the moment it's done (don't batch completions). Skip it for trivial single-step tasks. |
| `update_goal` | true | Report this turn's disposition for the active goal: `continue` (work is ongoing - give a concrete next_action), `complete` (the request is done and verification was attempted or reported unavailable), or `blocked` (only the user can unblock). An optional `completion` account may accompany `complete`: `verified` commands are reconciled against the session's real receipts, while `unverified` and `risks` are declarations the host cannot infer and do not block Light/Balanced completion. The host validates the claim against Delivery acceptance criteria and budget and decides whether to continue automatically. Outside an active goal turn the call fails closed without changing any state. |
| `view_image` | true | Read a local PNG, JPEG, GIF, or WebP image by path and return visual content through native vision or the configured image-understanding model. Use this for image paths instead of read_file. Maximum file size: 3 MiB; maximum dimensions: 40 million pixels. |
| `wait` | true | Block until background jobs finish, then return each job's status and final output/answer. Use to collect the result of a task(run_in_background) or bash(run_in_background) before continuing. Omit job_ids to wait for every running job. |
| `web_fetch` | true | Fetch a URL over HTTPS/HTTP and return its text content. HTML pages are reduced to readable text; JSON / plain text / markdown bodies come back verbatim. Use to read documentation pages, API responses, or source files hosted somewhere the local filesystem can't reach. |
| `write_file` | false | Write content to a file at the given path (overwriting existing content). Creates parent directories as needed. |

## Schema Snapshot

The exact canonical schemas are intentionally tested in code rather than copied by hand here. Run:

```bash
go test ./internal/tool -run TestBuiltinToolContractDocumentation
```

The test checks that every registered built-in tool has a documented name, read-only flag, description row, and canonical schema generated by `tool.BuiltinContractEntries`.

## Default Full Boot Surface

In a default full-token boot, Reasonix sends the built-in tools above plus the
session, memory, skill, subagent, LSP, install, and slash-command tools below:

Every session uses this exact executor tool surface plus one stable
proxy, `use_capability`, so optional MCP servers (including `auto_start=false`)
can be inspected and called without changing provider-visible schemas
mid-session. The host also
enforces a risk-adaptive execution contract: state-changing and
verification commands need acceptance criteria when the turn is closed-loop;
changed work cannot finalize
without post-change review, verification, and an evidence-backed
`complete_step` sign-off; Skill/MCP `require`/`prefer` routes are gated with
host-proven evidence (including read-only answers — ordinary reads never skip
a required capability); and medium/high-risk mutations force structured
`review` / `security_review` results via the review-only `review_report` tool,
whose `reviewed_paths` must be backed by host-observed read/diff receipts.

## Unified Boot Surface

Every session uses the same provider-visible core tools and the same
`use_capability` proxy.

The two-model Planner and all task/fleet sub-agents also use `use_capability`
(and never direct `mcp__*` schemas). Planner and ordinary writer-capable
sub-agents may call installed or project-configured MCP without
`readOnlyHint`; Planner leaves `destructiveHint` tools for the Executor, while
ordinary sub-agents use the trusted MCP path (live authorization plus explicit
deny only). Writer/destructive calls are still serialized and recorded as
mutations for evidence, workspace leases, and closed-loop guards. Strict read-only sub-agents
share the same proxy schema and Host connections but still require
`readOnlyHint` and non-destructive at execution time. Dual-model
attaches independent proxy frontends to both Planner and Executor so a
capability discovered during planning remains directly callable after handoff;
their ledgers/audits are isolated while Host connections are shared. A
single-model session has no independent Planner.

`use_capability` is a fixed-schema proxy: prefer `search(query)` then `inspect`
one exact id then `call`. `action=list` is a compact diagnostic inventory.
Independent `list`/`search`/`inspect` calls are read-only and may be issued
together. Resolution is side-effect free: `action=list` returns compact,
sorted configured MCP server summaries without expanding every cached tool
description or starting any server. Use `action=inspect` on one enabled
`mcp-server:<name>` to read that server's live or cached tool directory without
starting it. `action=call` on a
not-yet-connected server resolves to a deferred target, Plan re-checks only an
explicit phase opt-out on the real target, and the server process starts only
after the permission gate and PreToolUse hooks approve the call. On-demand children
share the session lifetime (they outlive the starting call and exit with the
session); `action=inspect` lists live tools for connected servers and cached
schemas otherwise, never starting a process. First discovery of a server with
no schema cache goes through `action=call` on the `mcp-server:` id itself: it
resolves to a gated connect (permission name = the server's dedicated
`mcp_connect__<server>` identity, so an exact rule such as
`deny = ["mcp_connect__github"]` blocks process startup) that connects after
approval and returns the live tool directory. MCP tool rules remain exact;
`mcp__github__*` is not a tool-name glob. Installing an MCP authorizes the
Planner to use its non-destructive tools; third-party servers that omit
`destructiveHint` are treated as user-install trust. Before every connect or
`tools/call`, the frontend re-checks the current runtime enablement,
authorization, and exact Host connection identity; another project/tab's
same-name shared client is rejected without process, network, or tool dispatch.

The fixed proxy's provider-visible name, description, schema, and ordering do
not change when MCP inventory changes.

When the current frontend has a session reader, the same fixed proxy also lists
the read-only `session:tool_result` capability. It pages the complete local copy
of one tool result by UTF-8 byte offset without adding a top-level schema. Calls
require `tool_call_id`; new truncation markers also provide a stable
`result_ref`, which is required to disambiguate repeated call IDs. `offset`
defaults to 0, `limit` defaults to 16KiB and is capped at 24KiB. Each response
starts with `result_ref`, actual offset, `next_offset`, `total_bytes`, full
SHA-256, and `complete`, followed by the raw page. The reader is bound to the
current Agent session and is not inherited from a parent when a capability
frontend is cloned. A restricted child that already has `use_capability` may
read only its own results; an allowed-tools profile without the proxy is not
widened. Paging is optional: a truncated or windowed result is not unpaid host
debt, and undeclared tools such as `git commit` or a final answer may follow
immediately. Path-scoped write evidence still requires a fresh read of the
file being edited.

`read_file` itself pages at a fixed 2000-line default (and an 8 MiB local
safety cap). When more lines remain, the tool appends a `PARTIAL view` trailer
naming the visible range, the file's total line count (a `N+` lower bound once
the bounded look-ahead is exhausted), and the next `offset`. A partial window
is sufficient when it covers the work.

`ask`, `docs`, `explore`, `fleet`, `forget`, `history`, `install_skill`, `install_source`,
`list_sessions`, `lsp_definition`, `lsp_diagnostics`, `lsp_hover`,
`lsp_references`, `memory`, `parallel_tasks`, `read_only_skill`,
`read_only_task`, `read_session`, `read_skill`, `read_subagent_result`, `remember`, `research`,
`review`, `run_skill`, `security_review`, `slash_command`, `task`.

`parallel_tasks` and `fleet` keep their combined result below the single-tool
output limit by returning a fair preview and a stable `Subagent reference` for
every persisted child. `read_subagent_result` pages through one referenced
final answer by UTF-8 byte offset, so long parallel research remains lossless
without injecting every report into the parent context at once. References are
restricted to the current conversation lineage and workspace.

Persisted child results also carry an explicit `status` (`completed`, `partial`,
`failed`, or `cancelled`) and `retryable` flag. A partial or failed child may
include its last visible answer and reference; use `read_subagent_result` for
inspection and the original `task`/`run_skill` `continue_from` parameter for a
retryable continuation. `session:tool_result` is only for ordinary tool output.

`use_capability` (`action` = `list` | `inspect` | `call` | `decline`) is on the
provider-visible surface for every task. Host verification obligations come
from real tool actions, not from preclassifying the prompt. Optional tools stay registered for host dispatch but are not
expanded into the top-level provider schema; the model reaches them through
`use_capability` without cache-breaking schema churn.

`internal/boot.TestBootToolContractMatchesProviderVisibleSurface` verifies the
actual boot registry contract against the provider request, including read-only
flags and canonical schemas.

## Unified Boot Surface (every task)

Every task starts with the same lean provider-visible core: direct
coding tools, background-shell lifecycle tools, and the stable capability proxy:

`bash`, `bash_output`, `edit_file`, `kill_shell`, `read_file`, `view_image`,
`wait`, `write_file`, `compress` (when registered), and `use_capability`.

Optional tools (`glob`, `grep`, `ls`, `web_fetch`, MCP, skills, subagents, docs,
session history, memory mutation, workflow, and so on) remain in the host
registry for dispatch. The model lists, inspects, calls, or declines them via
`use_capability` without changing the provider tool list. Task risk changes host
planning, verification, and review policy, not which tools appear on the
provider-visible surface. The retired `connect_tool_source` path is no longer registered.

## Invalid arguments and recovery

The host validates concrete tool arguments before extension interception,
permission prompts, hooks, write leases, subagent execution, or tool dispatch.
An extension replacement is resolved and validated again. Invalid arguments are
an unexecuted tool error, not a permission refusal: correcting the input can
succeed on any subsequent call without an inspect action or a new user turn.
Normal permission and execution checks still apply to a corrected call.

Errors retain the target name, schema fingerprint, violation paths, and
`argument_validation:<tool>:<fingerprint>:<category>` diagnostic signature.
Feedback identifies whether parameters belong at the direct tool's input root
or inside a capability call's `arguments`. A conservative, value-free hint may
identify a single redundant `arguments` wrapper when the inner object satisfies
the concrete contract, including conditional validation. This is advice only:
the host never unwraps, coerces, fills, or executes the supplied parameters as
part of diagnosis. Legitimate `arguments` fields and nested skill contracts are
preserved. Empty/null validation compatibility remains unchanged.

Input errors returned before capability resolution also receive contract
feedback when the outer schema establishes the error. Successful resolution is
not subjected to a new envelope gate; unavailable targets and authorization
errors keep their own reasons. A malformed host schema is a configuration
problem, not something the model can fix by rewriting arguments. Existing
third-party MCP schema-compilation fallback remains unchanged.

`inspect` remains a contract discovery operation, not an unlock requirement.
There is no schema-specific error counter or third-failure tool lock. The shared
storm breaker gives a soft convergence hint after three consecutive equivalent
failed batches; multiple calls in one batch do not add multiple rounds, and a
successful result resets the existing failure streak. Parameter-only failures
receive correction advice rather than instructions about bypassing permissions.
If correction remains unsuccessful, the model may report tool argument
generation failure and unfinished work. This does not mark the work completed.
Real permission, Plan-mode, hook, and write-loop restrictions remain enforced.

Convergence is advisory. With `MaxSteps=0` and no explicit budget, there is no
fixed-round hard stop; configured step/spend limits and cancellation still work.
No extra repair-model request, provider-specific switch, or tool-schema change
is introduced. Feedback is bounded to 4 KiB and appended to the failed tool
result without rewriting prior messages or the stable provider prefix. Additional
feedback consumes context tokens; historical error messages are left intact.

Argument validation/failure/skip/remote-dispatch counters retain their meaning;
internal wrapper checks do not count as additional calls. The legacy
`capability_loop_guard.RepeatFailures` and `BlockedCalls` fields remain in metrics
for compatibility but are no longer incremented by new runs. They are not
repurposed as storm-intervention counters; existing `loop_guard` notices describe
those interventions. No session/config migration is required. Downgrading restores
the older error-recovery behavior without changing the stored conversation.
