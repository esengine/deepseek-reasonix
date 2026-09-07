# Reasonix CLI Reference

<a href="../README.md">README</a>
&nbsp;·&nbsp;
<a href="./CLI.zh-CN.md">简体中文</a>
&nbsp;·&nbsp;
<a href="./GUIDE.md">Guide</a>

This reference covers interactive sessions, one-shot automation, session
resume, permission flags, and the most useful in-session commands. For provider
configuration, plugins, and sandbox policy, see the [Guide](./GUIDE.md).

## Start a session

```sh
reasonix
reasonix --model deepseek-pro
reasonix --effort high
reasonix --dir /path/to/project
```

Ordinary requests always enter the executor. There is no automatic simple /
light / full task mode to pick. The dedicated planner runs only for an
explicit Plan, an approval boundary, or Goal start.

Running `reasonix` without a subcommand starts the interactive terminal UI. Use
`reasonix setup` first when no provider is configured.

| Flag | Purpose |
| --- | --- |
| `--model NAME` | Select a configured provider or `provider/model` reference. |
| `--effort LEVEL` | Override reasoning effort for this session. |
| `--max-steps N` | Set a one-off maximum tool-call round budget; `0` uses automatic execution. |
| `--dir PATH` | Change the workspace root before loading config and tools. |
| `--add-dir PATH` | Add another writable tool directory; repeat for multiple directories. |
| `-c`, `--continue` | Resume the most recent session. |
| `-r`, `--resume [QUERY]` | Open the session picker, or resume a matching session. |
| `--copy` | Continue in a writable copy of the resumed session. |
| `--allowed-tools RULES` | Add session-only permission allow rules. Repeatable; `--allowedTools` is an alias. |
| `--permission-mode MODE` | Start with a specific permission posture. |
| `--yolo` | Start in YOLO mode; alias for `--dangerously-skip-permissions`. |

Flags may appear before or after the prompt where applicable.

## Update the native CLI

```sh
reasonix upgrade                  # install the latest official release
reasonix upgrade --check          # report the target without installing
reasonix upgrade --force          # reinstall the current official release
```

The updater selects only strict `vX.Y.Z` non-prerelease GitHub Releases. During
the 1.x compatibility period, old channel arguments and `--channel` are still
accepted, but resolve to the same official release and print a deprecation
notice. Legacy `[cli].update_channel` values are ignored and removed the next
time Reasonix saves the configuration. The `reasonix update` alias behaves the
same way.

## Configure providers

```sh
reasonix setup                    # manage the user-global config
reasonix setup --local            # manage ./reasonix.toml
reasonix setup /path/to/config.toml
```

In an interactive terminal, `reasonix setup` is a staged provider manager. It
lists configured providers and lets you:

- add OpenAI-compatible or Anthropic-compatible providers;
- edit endpoints and model lists;
- update API keys or test the connection and refresh models;
- choose the default model; and
- remove providers.

Choose **Save and exit** to review and confirm the pending operations. Canceling
discards them. Setup reloads the latest config while saving: unrelated desktop
or CLI changes are retained, while an overlapping change is reported as a
conflict instead of being overwritten.

Provider definitions contain only the `api_key_env` variable name. Key values
are stored in the shared Reasonix home `.env`, even with `--local`. When a
variable name is already used by another provider, setup asks whether to share
that credential; choose a different variable name when the providers use
different keys. Providers added or removed through setup are also added to or
removed from desktop provider access, so the same models are available in the
desktop app.

### Configure fee display currency

Use the user-global command to inspect or select the display currency:

```sh
reasonix config currency             # show the saved and resolved currency
reasonix config currency auto        # wallet hint, then original price currency
reasonix config currency CNY
reasonix config currency USD
```

`auto` remains unresolved in configuration. With one valid wallet currency it
can become a runtime session hint; otherwise CLI uses the original currency or
sorted currency buckets. Language and host locale never select a price table.
The preference is user-global and cannot be overridden by project
`reasonix.toml`; `--local` is therefore not supported. Custom prices are preserved.

In an interactive session, `/currency` shows the saved and resolved values, and
`/currency auto|CNY|USD` changes the preference and refreshes the current
runtime without discarding the conversation.

In an interactive session, `/show-balance` lists the three display modes with
the current one marked, and `/show-balance all|part|no` pins how the status bar
renders the wallet balance: `all` shows the full amount, `part` masks every
digit above the hundreds place behind a leading `*` (the hundreds place stays
zero-padded to three digits), and `no` hides the amount entirely as `***`.
The preference is saved to the user-global `[billing].show_balance` and
survives sessions. `all` is the default, so unconfigured setups keep the full
balance visible. Aliases `full`, `partial`/`mask`, and `off`/`hide`/`none` are
accepted.

### Configure automatic compaction

The desktop app and CLI share the user-global automatic compaction threshold.
Inspect the effective percentage and its source, set the global default, or add
a project override:

```sh
reasonix config compact-ratio              # show effective value and source
reasonix config compact-ratio 75           # set the user-global default
reasonix config compact-ratio --local 75   # override in ./reasonix.toml
```

The editable range is 30–85%, with 80% as the built-in default. Lower values
compact earlier, may increase summary calls and cost, and may reduce
prompt-prefix cache reuse; higher values retain more context before compaction.
Below the threshold, complete tool results may
increase ordinary request cost; at pressure they are durably pruned before the
cache-aligned summary runs. Project `reasonix.toml` takes precedence over
the user config. Changes apply to new CLI sessions; an already-running session
keeps the threshold it loaded at startup.

## One-shot and automation

Use `-p` / `--print` when a script needs only the final answer:

```sh
reasonix -p "summarize this repository"
reasonix -p "summarize this repository" --output-format json
reasonix run "implement the TODOs in main.go"
reasonix run --auto "implement the TODOs in main.go"
echo "explain this code" | reasonix run
```

`reasonix run` keeps the normal streamed terminal presentation unless `-p` or a
structured output format is selected. It also accepts `--model`,
`--max-steps`, `--effort`, `--dir`, `--add-dir`,
`--continue`, `--resume QUERY`, `--copy`, `--allowed-tools`, `--permission-mode`,
and `--auto` / `-y` (an alias for `--permission-mode auto`).

### Benchmark arms

`--ablate` switches whole subsystems off so a benchmark can attribute a change
in success rate to one of them. It accepts a comma-separated list of `evidence`,
`planner`, `subagent`, `retrieval` and `compaction`, plus `none` (the default,
everything on) and `all`. Sub-agents inherit the parent's arm, and the arm name
is written to the `--metrics` file so a recorded run is self-describing.

```sh
reasonix run --ablate evidence,planner --metrics run.json "fix the failing test"
```

This is a measurement tool, not a tuning knob: switching a subsystem off makes
Reasonix worse at the work it was added for.

### Trajectory recording

`--trajectory PATH` appends the run's full event stream — tool dispatches and
results with absolute start/end times, reasoning, retries, readiness and
recovery decisions — as one timestamped, sequenced JSONL record per event, so
a run can be replayed and its time attributed offline (tool execution vs. the
model thinking between calls). Records reuse the shared `eventwire` JSON
contract under an `event` key, wrapped in `schema_version`, `seq`, and `ts`
(unix ms). Every completed line survives a killed run. Unlike `--events-jsonl`,
the file contains prompts, tool arguments, and reasoning: treat it with the
same care as a session transcript.

```sh
reasonix run --metrics run.json --trajectory run.trajectory.jsonl "fix the failing test"
```

### Output formats

| Format | Behavior |
| --- | --- |
| `text` | Human-readable text. With `-p`, prints only the final answer. |
| `json` | Emits one final result object. |
| `stream-json` | Emits one shared `eventwire` JSON object per line, followed by the final result object. |

```sh
reasonix -p "list the risky changes" --output-format text
reasonix -p "summarize the diff" --output-format json
reasonix run "run the tests" --output-format stream-json
```

The final structured object has this shape:

```json
{
  "type": "result",
  "subtype": "success",
  "is_error": false,
  "duration_ms": 123,
  "num_turns": 1,
  "result": "...",
  "session_id": "...",
  "total_cost": 0,
  "currency": "USD",
  "total_cost_usd": 0,
  "usage": {
    "input_tokens": 0,
    "output_tokens": 0,
    "cache_read_input_tokens": 0,
    "cache_creation_input_tokens": 0
  }
}
```

`total_cost` is present only when a single `selected` display amount exists (ISO
code in `currency`). Prefer the structured `cost_quote` field when present: it
carries the original estimate, `original_totals`, occurrence-time valuations
(`official_table` for dual-region public prices), `cost_complete`,
`display_complete`, `display_status`, and `billing_mode` (`payg` or `subscription_equivalent` for
pay-as-you-go equivalent estimates such as MiMo Token Plan).

`total_cost_usd` remains a numeric compatibility alias when `total_cost` exists
and does **not** imply USD. Mixed original currencies no longer fail the run:
`cost_complete` remains true when usage/pricing facts are known,
`display_complete` is false, and `original_costs`/`original_totals` list per-ISO
totals so clients never invent a cross-currency sum.

Global display preference is `[billing].display_currency` (`auto|CNY|USD`);
legacy `[desktop].currency` still migrates. Provider list prices use each
entry's frozen `billing_currency` and are never rewritten by display switches.
Diagnose with `reasonix doctor billing`.

Execution failures use `subtype: "error_during_execution"` and
`is_error: true`. Structured modes keep runtime errors in JSON instead of also
printing a duplicate human-readable error.

The completion validator has been removed. A clean model stop without tool
calls ends the turn directly; a response with tools continues through the tool
loop, and a truly empty response is retried at the frozen-request boundary.
Legacy `completion_validation`, `completion_evaluator_model`, and
`REASONIX_COMPLETION_VALIDATION_MODE` settings remain readable but are ignored
and are no longer emitted by the config renderer. Host-owned readiness, budget,
tool-safety, and recovery boundaries remain active.

### Redacted machine interfaces

Use the dedicated event flag when an automation needs lifecycle telemetry but
must not receive prompts, reasoning, tool arguments, tool output, or approval
text:

```sh
reasonix run --events-jsonl "run the focused tests"
```

Every line has `schema_version`, `sequence`, and `kind`; the final line is
`kind: "run_done"`. `--events-jsonl` is intentionally separate from the richer
`--output-format stream-json` contract and cannot be combined with
`--output-format`.

The following read-only commands expose persisted state without transcript,
label, command, output, path, PID, or host-name content. Here, read-only means
the commands do not mutate transcript, runtime, recovery, or query state. The
first redacted-machine invocation may initialize a private identity key in the
Reasonix user-state directory:

```sh
reasonix session list --json [--dir SESSION_DIR | --project-root PATH]
reasonix session show <machine-session-id> --json [--dir SESSION_DIR | --project-root PATH]
reasonix session status <machine-session-id> --json [--dir SESSION_DIR | --project-root PATH]
reasonix session recovery [<machine-session-id>] --json [--dir SESSION_DIR | --project-root PATH]
reasonix task list --json [--dir SESSION_DIR | --project-root PATH] [--session MACHINE_SESSION_ID]
reasonix task show <task-id> --json [--dir SESSION_DIR | --project-root PATH] [--session MACHINE_SESSION_ID]
reasonix task monitor list --json [--dir PROJECT_DIR]
reasonix task monitor status <task-id> --json [--dir PROJECT_DIR]
reasonix task monitor events <task-id> --json|--jsonl [--dir PROJECT_DIR] [--after N] [--follow]
reasonix hook list --json [--project-root PATH] [--home-dir PATH]
reasonix hook status --json [--project-root PATH] [--home-dir PATH]
```

For `session` and `task`, `--dir` explicitly selects the session storage
directory, while `--project-root` resolves the selected project's session
store. The two options cannot be combined. Without either option, Reasonix
selects the current project's session store.
For `hook`, `--dir` is an alias for `--project-root`.
`hook list` reports `active` or `invalid`; `invalid` means the
configured event cannot execute because its event, command/context source, or
tool-event matcher is unusable. Matchers on non-tool events are ignored.

Machine session IDs are keyed opaque hashes, not transcript file names. They
remain stable for the same session and Reasonix user-state directory, while a
different installation key produces unrelated IDs and prevents offline guesses
from timestamps or model labels. Preserve the private identity key when moving
the Reasonix state directory if automation depends on existing machine IDs.
Task `finished_at` is empty while a task is running, and
`artifact_complete=true` is emitted only for a terminal task whose persisted
artifact exists. A `running` record without a live session lease is reported as
`interrupted`; opening that session also repairs the persisted lifecycle state.

Schema compatibility rules for version 1:

- consumers must ignore unknown fields;
- fields are not removed or retyped within the same schema version;
- empty collections are encoded as `[]`;
- argument errors exit with status `2`, state/query errors with status `1`;
- machine-command errors are JSON objects with a stable `error.code`.

## Resume sessions

```sh
reasonix --continue
reasonix --resume
reasonix --resume provider-config
reasonix --resume <session-id>
reasonix --resume provider-config --copy
```

- `--continue` resumes the newest saved session immediately.
- Bare `--resume` opens the searchable picker in an interactive terminal.
- `--resume QUERY` accepts an exact session ID or path, or a unique title or
  preview substring. Missing and ambiguous matches fail with a descriptive
  error.
- `--resume=true` and `--resume=false` remain accepted for compatibility.
- `--copy` leaves the original transcript untouched and continues in a new
  writable session. Use it when another Reasonix process owns the original.

For one-shot runs, `reasonix run --resume QUERY "task"` accepts a session file
path, a session ID, or an opaque machine session ID from `--events-jsonl` /
`reasonix session show --json`. Session leases prevent the desktop app and CLI
from writing the same transcript concurrently.

## Permissions

```sh
reasonix --permission-mode plan
reasonix --permission-mode acceptEdits
reasonix run -y "apply the requested changes"
reasonix -p "run the focused tests" --allowed-tools "Bash(go test ./...)"
reasonix --allowed-tools "Bash(git *) Edit"
reasonix --allowed-tools "Bash(go test ./...)" --allowed-tools read_file
```

| Mode | Behavior |
| --- | --- |
| `manual`, `ask` | Ask for ordinary approval decisions. |
| `auto` | Automatically approve normal fallback operations, including interactive `remember`/`forget`, while preserving explicit ask and deny rules. |
| `acceptEdits` | Allow file-editing tools; this is not full Auto mode. |
| `dontAsk` | Deny unapproved requests without opening an approval prompt. |
| `plan` | Start the plan-first workflow; tool calls still use the active permissions and sandbox. |
| `bypassPermissions` | Bypass approval prompts; equivalent to YOLO. |

For unattended execution with ordinary writer fallback enabled, use
`reasonix run --auto ...` (or `-y`). The alias cannot be combined with an
explicit `--permission-mode` value.

`[permissions] allow_dynamic_bash = true` is an advanced opt-in that lets an
Allow fallback, including Auto, cover command/process substitution, dynamic
command names, shell `-c`, and other nested/indirect Bash forms. The default is
`false`; explicit `ask` and `deny` rules still take precedence.

`--allowed-tools` is a session permission override, not a provider tool-schema
filter. Rules may be comma- or space-separated, and the flag is repeatable.
Configured deny rules always win over command-line allow rules.

In non-interactive runs (`reasonix run` / `-p`) there is no prompt to answer, so
approval modes resolve without blocking. The default `ask` / `manual` posture
fails closed for explicit Ask decisions and ordinary writer fallback; readers
still run. `acceptEdits` allows its named file-edit tools, while other Ask
decisions fail closed. `auto` allows ordinary writer fallback but still denies
an explicit ask rule; select it with `--permission-mode auto`, `--auto`, or
`-y`. `dontAsk` denies unapproved writers.
`bypassPermissions` runs ordinary calls despite ask rules and writer fallback,
but configured deny rules, the sandbox, and tools that require fresh human
approval (plan, sandbox escape, managed config write) still apply. Interactive
Auto auto-allows the default `remember`/`forget` fallback while preserving
explicit ask and deny rules; interactive YOLO bypasses memory ask prompts but
still honors deny. In every headless mode, the owning
top-level controller may still create a bounded, non-sensitive, create-only
project or reference memory; all other memory mutations remain denied without a
human.

## Additional directories

```sh
reasonix --add-dir ../shared
reasonix -p "update both projects" \
  --add-dir ../frontend \
  --add-dir ../backend
```

Relative paths resolve from the workspace root and must already exist as
directories. Reasonix resolves symlinks, removes duplicates, and extends the
file-writer and sandboxed Bash write boundaries for the session. These additions
are runtime-only and are not written to configuration.

## Interactive controls

The `/model`, `/provider`, and `/resume` commands use searchable pickers.
Approval prompts use the same row-selection behavior while retaining their
single-key shortcuts.

| Key | Action |
| --- | --- |
| `Up` / `Down`, `Ctrl+P` / `Ctrl+N` | Move through picker or approval rows. |
| `j` / `k` | Move while the search is empty; after search input starts, enter `j` / `k` as query text. |
| Type | Filter a searchable picker. |
| `Enter` | Select the highlighted row. |
| `Esc` | Cancel the current picker or approval. |
| `y` / `a` / `p` / `n`, number keys | Use the matching approval action. |
| `Shift+Tab` | Cycle `Ask → Auto → Plan → Ask`. |
| `Ctrl+Y` | Toggle YOLO independently of the composer-mode cycle. |

The responsive footer keeps interaction state on the left and, when space
allows, places model and effort on the right. Its second row shows
available repository and session telemetry such as cache hit rate, context use,
compaction headroom, background jobs, and balance. `ready` means the composer is
idle; that slot changes when a picker, approval, image paste, shell mode, or
other interaction needs attention. Narrow terminals move or compact complete
groups instead of cutting labels in half. Visible labels follow `/language`.

Use `/theme auto|light|dark` to select the terminal background mode, or choose a
named accent from `/theme`. Both composer borders, the insertion cursor,
selection, scrollbar, and footer use the active CLI theme. See
[Keyboard shortcuts](./GUIDE.md#keyboard-shortcuts) for transcript navigation,
multiline input, rewind, and clipboard controls.

Clipboard actions are deliberately split by content type. Local transcript
and composer selections use the native system clipboard and report success only
after that write completes; SSH falls back to an explicitly labelled OSC 52
request. Text paste remains the terminal's bracketed-paste action (`Cmd+V` on
macOS and the terminal's configured shortcut elsewhere). While Reasonix owns the
mouse in a local session, right-click with no selection reads clipboard text
through the same paste path; right-click with a selection copies it. Over SSH,
use the terminal paste shortcut because the remote process cannot read the local
clipboard; `/mouse` restores the terminal's native right-click menu. Image paste
is application-owned: use `Ctrl+V` on macOS/Linux, `Alt+V` on Windows, or
`/paste-image`; the footer shows `Pasting image…` until the attachment token is
ready. Where the terminal forwards that shortcut instead of pasting itself, a
clipboard holding no image falls back to a text paste, so the key never swallows
plain text.

## In-session commands

Type `/help` in an interactive session for the complete command list. Slash
completion, help, dispatch, and aliases are generated from the same registry, so
the displayed list matches the commands the TUI accepts.

| Command | Purpose |
| --- | --- |
| `/continue-checks [guidance]` | Resume the immediately preceding paused task-completion check while preserving its verified tool evidence. The command is one-shot and refuses stale cards after another user turn. |
| `/model` | Search configured models and switch the active model. |
| `/provider` | Choose a provider, then choose one of its configured models. |
| `/resume` | Search recent sessions and switch to one. |
| `/status` | Show model, effort, cache, Git, background jobs, and balance details. |
| `/theme [auto\|light\|dark\|style]` | View or change the CLI background mode and accent palette. |
| `/currency [auto\|CNY\|USD]` | View or change the user-global fee display currency and refresh the runtime. |
| `/show-balance [all\|part\|no]` | View or change how the status bar renders the wallet balance (full, masked, or hidden). |
| `/paste-image` | Read a clipboard image and insert an editable attachment token. |
| `/mouse` | Toggle in-app mouse selection, scrollbar, and wheel handling; SSH sessions start with capture off so the terminal's native selection works. |
| `/effort` | View or change reasoning effort. |
| `/preset [standard\|delivery]` | Switch the session quality floor; delivery turns on delivery completion gates and shows a PRESET tag in the status line. |
| `/output-style` | Select an answer style. |
| `/verbose` | Toggle expanded reasoning display. |
| `/sandbox` | Inspect sandbox status. |
| `/goal [objective]` | Start a continuous goal, or inspect its runtime statistics. |
| `/goal status` | Show the active goal plus turns, requests, tokens, work time, and the last continuation/evaluator reason. |
| `/goal pause` | Pause the running goal (keeps todos, Delivery checkpoint, and runtime history). |
| `/goal resume` | Resume a manually paused or genuinely blocked goal without changing a numeric quota. |
| `/goal clear` | End goal mode permanently. |
| `/docs [question]` | Show the embedded corpus identity, or search it locally and ask the configured AI to answer from version-matched evidence. |
| `/reasonix:docs [question]` | Preferred built-in fallback when an existing custom command or compatible plugin/skill alias owns `/docs`; if this spelling is also owned, the menu selects the next free `reasonix:`-qualified name without displacing it. |
| `/mcp`, `/skills`, `/hooks` | Inspect and manage extensions. |
| `/remember <note>` | Append a standing note to the project instruction document; `# <note>` is a shortcut. |
| `/memory [subcommand]` | Inspect instructions, memory provenance, recall, revisions, and recovery. |
| `/rewind` | Restore conversation and/or code to an earlier turn. |
| `/tree`, `/branch`, `/switch` | Inspect or navigate conversation branches. |
| `/reload` | Reload the agent runtime (extensions, tools, skills, commands, hooks, providers) while keeping the session. Queued once while a turn runs, then fail-atomic: a failed rebuild keeps the current runtime. |

Switching model or effort rebuilds the runtime while preserving the
active conversation, session-scoped permission overrides, additional directory
access, and session ownership. `/reload` uses the same fail-atomic rebuild.
Execution modes no longer exist: planning, verification, and review strength
follow task risk per turn.

## Session catalog diagnostics

The desktop session catalog is a disposable SQLite query projection; transcript
JSONL and sidecars remain authoritative. Inspect it read-only or replace only
the projection:

```sh
reasonix doctor sessions [--json]
reasonix sessions reindex [--json]
reasonix sessions reindex --dir /path/to/sessions --dir /another/path
```

Without `--dir`, reindex includes global sessions and all projects saved by the
desktop app. See [Session Catalog and Desktop Startup](./SESSION_CATALOG.md) for
failure, migration, and data-safety guarantees.

History search uses a separate disposable projection:

```sh
reasonix doctor catalogs [--json]
reasonix catalogs reindex history [--dir PATH ...] [--json]
```

See [History Search Catalog](./HISTORY_SEARCH_CATALOG.md).
Usage statistics use a separate disposable rollup projection:
reasonix catalogs reindex usage [--json]
See [Usage Catalog](./USAGE_CATALOG.md).

Inspect or rebuild the disposable task projection independently:

```sh
reasonix doctor catalogs [--json]
reasonix catalogs reindex tasks [--project PATH ...] [--json]
```

See [Task Catalog](./TASK_CATALOG.md) for the authoritative FileStore boundary,
cross-project routing, and rebuild behavior.

### Memory diagnostics and recovery

Bare `/memory` shows all active project/global facts without hiding same-name
entries. Facts include their stable ID, revision, scope, type, freshness, and
description. Slash completion offers the available subcommands, active IDs and
names, and owned archive paths.

| Command | Purpose |
| --- | --- |
| `/memory instructions` | Show resolved instruction precedence, directories, imports, and diagnostics. |
| `/memory recall` | Explain the latest automatic recall query, hits, scores, reasons, freshness, and budget. |
| `/memory revisions <id-or-name>` | Show the active revision and immutable history. |
| `/memory restore <id-or-name> <revision>` | Restore old content as a new monotonic revision. |
| `/memory archived` | List archived facts and their owned paths. |
| `/memory recover <archive-path>` | Recover an archive as a new revision without overwriting active data. |

These commands run against the active session controller. When the session
lives on a remote host (`reasonix remote connect` / a desktop remote web
window), they use the remote memory catalog and never fall back to local
desktop memory. See [Context Engine v2](./SESSION_MEMORY_RETRIEVAL.md) for
authority, automatic recall, write confirmation, and migration behavior.
