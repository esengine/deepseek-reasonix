# Reasonix Guide

Provider model capability metadata is documented in
[`MODEL_CAPABILITIES.md`](./MODEL_CAPABILITIES.md).

<a href="../README.md">README</a>
&nbsp;·&nbsp;
<a href="./GUIDE.zh-CN.md">简体中文</a>
&nbsp;·&nbsp;
<a href="./SPEC.md">Spec</a>

> Day-to-day configuration and usage. For the engineering contract and internals
> (data types, registries, package layout, roadmap), see the **[Spec](./SPEC.md)**.

## Contents

- [Configuration](#configuration)
- [Billing and display currency](./BILLING.md)
- [CLI reference](./CLI.md)
- [Environment variables](#environment-variables)
- [Web frontend](#web-frontend)
- [Configuration paths](./CONFIG_PATHS.md)
- [Reasoning language](./REASONING_LANGUAGE.md)
- [Task contracts and pause policy](./TASK_CONTRACT.md)
- [Custom OpenAI-compatible providers](#custom-openai-compatible-providers)
- [Desktop hooks](#desktop-hooks)
- [Keyboard shortcuts](#keyboard-shortcuts)
- [Permissions & sandbox](#permissions--sandbox)
- [Capability diagnostics](#capability-diagnostics)
- [Plugins (MCP)](#plugins-mcp)
- [Slash commands](#slash-commands)
- [Embedded documentation retrieval](#embedded-documentation-retrieval)
- [@ references](#-references)
- [Two-model collaboration](#two-model-collaboration)

## Configuration

Resolution order: **flag > `./reasonix.toml` > the user config file >
built-in defaults**. Starting with **Reasonix v1.8.1**, the user config lives at
`~/.reasonix/config.toml` on macOS/Linux and
`%AppData%\reasonix\config.toml` on Windows; see
[Configuration paths](./CONFIG_PATHS.md) for migration and related data paths.
Fields marked user/global only are not overridden by `./reasonix.toml`.
Provider entries name secrets with `api_key_env`, while the secret values live in
Reasonix's global `<Reasonix home>/.env`, shared by CLI and desktop. Project
`.env`, home `.env`, inherited shell environment variables, legacy credentials,
and the OS keyring are not provider-key runtime fallbacks; legacy credentials are
only migration sources. Project `.env` still feeds workspace-scoped,
non-provider `${VAR}` expansion for MCP/plugin settings without importing
provider keys or Reasonix control variables. See
[Configuration paths](./CONFIG_PATHS.md) for the full `config.toml` and `.env`
structure.

For the desktop and CLI usage of visible reasoning language, see
[Reasoning language](./REASONING_LANGUAGE.md).

```toml
default_model = "deepseek-flash"   # executor; set [agent].planner_model to add a planner
# language    = "zh"               # ui language; empty = auto-detect from $LANG / $REASONIX_LANG

[ui]
# shortcut_layout = "desktop"      # classic|desktop; compatibility setting
# cursor_shape = "bar"             # block|underline|bar; CLI/TUI text cursor
show_turn_usage = false             # hide per-request token/cost receipts in the TUI; default true

[agent]
reasoning_language = "auto"      # visible reasoning text: auto|zh|en
# plan_mode_read_only_commands = ["gh issue view"]   # legacy compatibility only; Plan bash now uses Permissions
# planner_model = "deepseek-pro"      # optional low-frequency planner
# subagent_model = "deepseek-pro"     # optional default for runAs=subagent skills
# subagent_models = { review = "deepseek-pro", security_review = "deepseek-pro" }
# max_subagent_depth = 2              # nested delegation depth; set 1 for the old single-layer boundary
# max_subagent_concurrency = 6        # session-wide sub-agent concurrency (task/fleet/skills)
# max_parallel_writers = 3            # concurrent writers with non-overlapping write_paths
# compact_ratio = 0.80             # sole auto trigger; presets 0.70 / 0.80 / 0.85
# max_output_tokens = 0            # auto: official DeepSeek omits the field (server 384K) until the window is tight
# max_output_tokens = 32768        # optional cost cap; still clipped to physical remaining
# max_output_tokens = 65536        # optional cost cap
# max_output_tokens = -1           # force-omit the wire field; compact if the known auto budget no longer fits
# max_output_tokens never changes compact_ratio; 0 is the provider auto value, not "skip local checks"

[[providers]]
name        = "deepseek-flash"
kind        = "anthropic"
base_url    = "https://api.deepseek.com/anthropic"
model       = "deepseek-v4-flash"
api_key_env = "DEEPSEEK_API_KEY"
web_search  = true
# also preset: deepseek-pro

[tools]
enabled = []   # omit/empty = all built-ins
bash_timeout_seconds = 120   # foreground safety cap; set 0 for no tool-local cap
mcp_startup_timeout_seconds = 30   # background initialize + tools/list safety cap
mcp_call_timeout_seconds = 300   # default MCP call safety cap; per-plugin/tool overrides may raise it

[environment]
enabled = true   # inject a stable startup summary of OS, shell, and common tools
offline = false  # set true when outbound network access is unavailable; prevents futile retries
# [environment.tools]
# go = "/opt/homebrew/bin/go"   # optional explicit trusted path; workspace-local paths are not auto-executed

[skills]
# paths = ["~/my-skills", "../shared/skills"]   # extra custom skill roots
# excluded_paths = ["~/.agents/skills"]         # hide convention roots without deleting folders
# disabled_skills = ["review"]                  # hide skills until /skill enable <name>

[permissions]
mode  = "ask"                                # writer fallback when no rule matches: ask|allow|deny
deny  = ["Bash(rm -rf*)", "Bash(git push*)"] # hard-blocked in every mode
allow = ["Bash(go test:*)"]                  # never prompted

[sandbox]
# workspace_root = ""          # file-writers confined here; empty = current dir
# allow_write    = ["/tmp"]    # extra dirs write_file/edit_file/multi_edit/move_file may touch
# forbid_read    = ["${HOME}/.ssh"]   # paths the agent must not read or list

[serve]
auth_mode = "none"             # none|token|password; use auth before binding beyond localhost
# token = ""                   # optional fixed token; empty token mode generates one at startup
# password_hash = ""           # bcrypt hash generated with reasonix serve --hash-password --password '...'
# behind_proxy = false         # true only behind a trusted reverse proxy

[[plugins]]
name    = "example"
command = "reasonix-plugin-example"
startup_timeout_seconds = 60   # optional initialize + tools/list cap
call_timeout_seconds = 600   # optional per-server MCP call timeout
tool_timeout_seconds = { "generate_video" = 1800 }   # optional raw MCP tool names
```

For the full schema and every field's contract, see [`SPEC.md` §5](./SPEC.md#5-configuration-toml).

Installed and project-configured MCP servers need no per-tool trust
list. The dedicated two-model Planner may use every non-destructive MCP tool,
even when the server omits `readOnlyHint`; strict read-only sub-agents still
require `readOnlyHint: true` and no `destructiveHint`.

`[agent].plan_mode_read_only_commands` is also retained for config round trips,
but the main Plan workflow no longer has a separate bash allowlist or trust
prompt. Bash classification and approval use the same Permissions rules in Plan
and Standard mode; the Sandbox remains the filesystem, process, and network
boundary. Dedicated planner and read-only subagent runners keep their own strict
read-only tool registry and foreground-command classifier.

### Environment variables

Most day-to-day settings belong in `config.toml` or the global Reasonix `.env`
described above. The variables below are process-level advanced switches; set
them before launching Reasonix. Project `.env` files are not a runtime source for
Reasonix control variables.

### CLI telemetry

The CLI can send a once-per-day anonymous active-install ping and bounded,
content-free event counters to `https://crash.reasonix.io`. Configure the
user-global policy with:

```bash
reasonix config telemetry          # print the effective mode
reasonix config telemetry auto     # default: local interactive TTY only
reasonix config telemetry on       # also allow local headless `reasonix run`
reasonix config telemetry off      # disable and delete pending counter files
```

On the first eligible release-build interactive session, Reasonix explains the
exact data boundary and asks once before any telemetry request. The prompt is
`[Y/n]`: pressing Enter, `y`, or `yes` stores `auto`; `n` or `no` stores `off`
and deletes pending counters. After the choice is saved, enabled reporting is
silent and the prompt is not shown again. If the preference cannot be saved,
nothing is uploaded.

Reporting is always disabled in CI, development builds, and when
`DO_NOT_TRACK` is set or `REASONIX_TELEMETRY=0`. Under `auto`, redirected/piped
or otherwise non-interactive sessions do not report. When no choice has been
saved yet, these ineligible sessions neither prompt nor report. Network failures
after consent are silent and never change stdout, stderr, or the process exit
code; unsent counters stay in a bounded local queue for a later invocation.

The ping contains a dedicated random 128-bit CLI install ID, CLI version, OS,
architecture, and the `cli` surface marker. Counter batches use that same ID for
daily active-install deduplication and contain only fixed buckets such as CLI
surface, permission/session mode, turn latency, finish reason, cache-hit
range, generic Provider/tool error class, compaction, recovery counters, and
normalized UI language. This ID is separate from the desktop install ID and is
not an account, hardware, repository, or session identifier.

Reasonix never uploads prompts, answers, reasoning, tool names/arguments/output,
paths, repositories/branches, session IDs, exact token or cost values,
Provider/model names, base URLs, or environment variables.

### CLI crash reports

An unhandled Go panic that reaches the CLI entrypoint is saved locally as a sanitized report under
`<Reasonix home>/cli-crash-reports`. Reasonix keeps at most 10 files with owner-only
permissions. The panic value is never serialized. Absolute source paths become
`<path>/<file>.go:<line>`, function arguments are removed, and the same secret,
token, email, and long-identifier scrubbers run both when saving and immediately
before sending.

Crash reports are never uploaded automatically. Review and manage them with:

```bash
reasonix report                 # preview newest; prompt before sending on a TTY
reasonix report list            # list local reports
reasonix report show [ID]       # preview without sending
reasonix report send [ID]       # explicit send; delete locally only after success
reasonix report delete [ID]     # delete without sending
```

Piped or redirected `reasonix report` calls only preview and never prompt or
send. The CLI telemetry setting does not auto-send or auto-delete
these separately reviewed reports. Runtime fatal throws, operating-system kills,
and panics in unwrapped background goroutines cannot be recovered by Go and do
not produce this local report.

## Web frontend

For local use, `reasonix web` starts the browser UI and opens it in your default
browser. Inside an interactive CLI session, `/web` snapshots the current session,
restores the terminal, and opens an explicit `/sessions/<id>#token=...` deep link.
Even a never-used session keeps its reserved ID without forcing an empty
transcript onto disk, so the first Web turn continues the same session identity.

```bash
cd your-project
reasonix web
```

Use `reasonix web --no-open` when you want to start the foreground Web server
and print its URL without opening a browser tab. The lower-level
`reasonix serve` command starts the same engine without opening a browser by
default. It remains the right entry point for remote development boxes,
supervisors, tunnels, reverse proxies, and shareable authenticated sessions.

`reasonix web` starts at `127.0.0.1:8787`, automatically tries 8788, 8789, and
so on when a port is busy (up to 100 retries), and defaults to a newly generated
token even when `[serve].auth_mode` is `none`. Each live process registers a
single-writer heartbeat file under `<Reasonix home>/server/instances/`; clean
shutdown removes its own file, while later instances lazily remove records whose
owner process is confirmed dead. Multiple Web instances can therefore share one
Reasonix home without overwriting registry state. The process stays attached to
the terminal; stop it with Ctrl-C.

An explicit `reasonix web --auth none` disables the default token and should be
used only when the listener is intentionally trusted. `reasonix serve` keeps its
backward-compatible, config-driven `auth_mode = "none"` default on
`127.0.0.1:8787`. If you bind Serve outside loopback, expose it through a tunnel,
or put it behind a reverse proxy, enable authentication before sharing the URL:

```bash
reasonix serve --auth token
reasonix serve --addr 0.0.0.0:8787 --auth token
reasonix serve --auth password --password 'temporary-password'
```

Token mode prints a share URL with `#token=...`; the Web page exchanges the
fragment for an HttpOnly cookie before starting API or SSE requests, keeping the
token out of request URLs, browser history, referrers, and access logs. Pass `--token` or set
`[serve].token` to reuse a stable token. Password mode requires either
`--password` at startup or a stored bcrypt hash:

```bash
reasonix serve --hash-password --password 'strong-password'

# <Reasonix home>/config.toml
[serve]
auth_mode = "password" # none|token|password
password_hash = "$2a$12$..."
behind_proxy = true    # only behind a trusted reverse proxy
```

The web UI exposes chat, tool approvals, session history, rewind/fork/summarize,
model and reasoning-effort controls, Goal, a live todo panel fed by the
`todo_write` tool, extension status/card/form/notification surfaces, and
provider balance when configured. Extension-hosted providers appear in the
model picker. Serve can keep several sessions active at once: creating or
resuming another session detaches a busy turn instead of cancelling it, and the
session list continues to report that background activity. Run `/reload` while
idle to fail-atomically reload extension sidecars and the runtime generation
without restarting Serve. Use `--model`, `--max-steps`, or `--resume` for
one-off launches; otherwise `serve` uses the user-global `default_model`.

If the selected Provider has no saved API key, a loopback-bound Serve still
starts and shows a Provider setup page instead of failing before the browser can
connect. After authentication, enter the key there; Reasonix writes it to this
host's global credential file with restricted permissions, rebuilds the active
controller in the same process, and opens the normal UI. The credential-writing
endpoint is disabled for non-loopback listeners. For a remote SSH window,
"this host" means the remote host reached through the SSH tunnel; the key is
not copied from the desktop machine.

## Editor integrations over ACP

`reasonix acp` exposes Reasonix as an ACP v1 stdio agent for editors and other
host clients. The dedicated **[ACP editor integration](./ACP.md)** guide covers
startup, capability negotiation, session lifecycle, independent model/work/
collaboration/approval controls, client filesystem and terminal capabilities,
MCP servers, permission requests, and the Reasonix mid-turn steering extension.

## Remote SSH

The remote module runs Reasonix on a remote host and reaches it over your own
SSH connection — VS Code Remote-SSH style. It bootstraps a persistent headless
`reasonix serve` on the remote host, forwards a local loopback port to it, and
opens the existing serve web client through that tunnel. The agent, its tools,
and its files all live on the remote host at full fidelity; nothing runs through
a lossy file proxy. V1 supports Linux and macOS remote hosts.

The dedicated **[Remote sessions](./REMOTE_SESSIONS.md)** guide covers host
configuration (`[remote]` in `config.toml`), SSH-config resolution and import,
the `reasonix remote` CLI, the remote serve bootstrap and its install ladder,
the remote session lifecycle and takeover, the desktop remote workspace, the
`remote` and `local-proxy` credential modes, connection failure semantics, and
troubleshooting.

## Custom OpenAI-compatible providers

In the desktop app, open **Settings -> Model -> Access -> Add model service ->
Custom provider** for proxies, aggregators, or self-hosted services that speak
the OpenAI-compatible chat API or Anthropic-compatible Messages API.

For common providers, choose **Add model service -> Recommended preset** instead.
New official DeepSeek entries use the Anthropic-compatible Messages endpoint by
default and enable provider-side `web_search`; the same `DEEPSEEK_API_KEY` works
for both protocols. On startup, Reasonix upgrades unmodified legacy
`deepseek-flash` / `deepseek-pro` entries that still use the official endpoint
and standard key/model settings. Customized official Chat Completions entries
stay unchanged and show an **Upgrade protocol** action in Settings. Proxy
endpoints, custom headers, model lists, and capability overrides are never
migrated automatically. Existing
separately named `deepseek-anthropic` entries remain compatible, but that
redundant preset is no longer offered for new access. Reasonix can prefill editable custom-provider entries for Kimi CN,
Kimi Global,
Kimi Coding Plan, MiMo API, MiMo Anthropic, MiMo Token Plan CN/SGP/AMS and their
Anthropic-compatible variants, MiniMax CN/Global API, MiniMax CN/Global
Anthropic, GLM CN, Z.AI Global, GLM/Z.AI Coding Plan OpenAI-compatible and
Anthropic-compatible endpoints, OpenCode Go, OpenCode Go Anthropic, OpenCode Go
DeepSeek Anthropic, OpenCode Go DeepSeek Responses, OpenCode Zen
Anthropic, Qwen/DashScope CN/Global, Qwen Coding Plan CN/Global
OpenAI-compatible and Anthropic-compatible endpoints, StepFun OpenAI-compatible
and Anthropic-compatible endpoints, NovitaAI, GMI Cloud, Vercel AI Gateway,
HuggingFace Router, ModelScope, NVIDIA NIM, KiloCode, and Ollama Cloud. Plan names describe
the access/payment route; they include CN/Global only when the provider exposes
distinct regional endpoints. Kimi Coding Plan is therefore a dedicated plan
endpoint, while Kimi direct API is split into CN and Global. The preset path
usually needs only the provider API key: the key value is stored in Reasonix home
`.env`, while `config.toml` stores the endpoint, model list, key
environment-variable name, context window, model capability metadata, proxy bypass
for China-only endpoints, MiniMax `reasoning_split`, GLM/MiniMax thinking
heuristics, Anthropic-compatible Bearer auth where needed, Ollama Cloud
max-effort support, and OpenCode Go per-model reasoning overrides. Official DeepSeek Anthropic, Responses, and Chat Completions catalogs also
include the multimodal SKUs `deepseek-flash` and `deepseek-v4-flash-vision-exp`. Settings derives image support from
model capability metadata. Each model also has an Image input Auto / On / Off
selector. For an ID-only relay list, unknown means unrecognized, not confirmed
text-only: select On after confirming support with the relay, then save. See the
[image input guide](MODEL_CAPABILITIES.md#set-image-input-for-a-relay-model).
Composer
and `@` user images are sent as official visual input using the three documented
shapes: inline base64 `data:` URLs for local files, `http(s)` image URLs as-is,
and Files API `file-api-` ids (local images over 32 MiB on official DeepSeek are
uploaded automatically). Chat Completions uses `image_url` or `file`, Anthropic
uses `image`+`source.base64|url|file`, and Responses uses `input_image`.
Flash and Pro stay text-only on the wire even when legacy configuration lists them, and tool screenshots
are not forwarded as image parts. The vision SKU uses the Flash rate card. The dedicated
OpenCode Go DeepSeek Anthropic and DeepSeek Responses presets expose the verified
Flash routes and enable provider-side `web_search` by default; the Responses
variant uses stateless context replay. The existing mixed OpenCode Go Anthropic
preset remains scoped to Qwen and MiniMax so server tools are not sent to
unverified models. DeepSeek Pro remains on the Chat Completions preset because
live Anthropic and Responses requests currently fail in the OpenCode Go upstream
conversion. The OpenCode Go preset includes its native `kimi-k3` subscription
route with image input,
`high`/`max` reasoning effort, and a 1,048,576-token context window. Existing untouched
OpenCode Go preset installs are upgraded automatically; edited model catalogs
are preserved. The Kimi CN and Kimi Global direct-API presets also include
`kimi-k3` with image input, a 1,048,576-token context window, and the official
`low`/`high`/`max` effort scale (default `max`). For the official K3 endpoints,
Reasonix preserves complete assistant messages across turns, sends output limits
as `max_completion_tokens`, and omits K3's fixed sampling parameters. Untouched
legacy Kimi direct-API catalogs are upgraded automatically without changing the
default model; custom catalogs and endpoints are preserved. After adding a
preset, open its provider card if you need to change models, headers, endpoint,
or compatibility settings.

Fill **API address** with the provider endpoint that should receive the standard
chat path. In this mode Reasonix previews and sends chat requests to:

```text
<API address>/chat/completions
```

Enable **Full URL** when the service gives you a complete request URL, for
example `https://gateway.example.com/v1/chat/completions`. Reasonix then sends
chat requests directly to that URL and does not append `/chat/completions`. The
preview under the field shows the exact request URL that will be used.

Model discovery uses the API address to try likely model-list URLs such as
`/models` and `/v1/models`. If the gateway requires a separate model-list
endpoint, open **Compatibility settings** and set `models_url`, for example
`https://gateway.example.com/v1/models`. If discovery is not available, fill the
model list manually.

**Full URL** still uses the OpenAI-compatible chat request body. It does not
switch the request schema to the OpenAI Responses API.

### Compatibility settings

The **Compatibility settings (usually leave unchanged)** section is for gateways
whose authentication, model-list endpoint, or reasoning/thinking request shape
differs from the normal OpenAI-compatible defaults. Leave these fields at their
defaults unless the provider documentation or a proxy error tells you otherwise.
For Anthropic-compatible services, such as some coding-plan endpoints, choose
**Anthropic-compatible** as the connection protocol before saving.

| Field | What it controls | When to change it |
| --- | --- | --- |
| `api_key_env` | The environment-variable name used for this provider's API key. Desktop-saved key values are stored in Reasonix home `.env` under this name; the TOML config stores only the name. | Change it when several providers need distinct keys, or leave it blank for a service that does not require an API key. |
| `models_url` | The URL used only for model discovery. Chat requests still use the API address or Full URL above. | Set it when `/models` or `/v1/models` is not where the gateway exposes its model list. |
| Extra request headers | Static HTTP headers, one `Header: value` per line. | Use for gateways such as OpenRouter that require `HTTP-Referer`, `X-Title`, or similar site headers. Keep bearer/API keys in the key field instead of duplicating them here. |
| Extra request body | A JSON object merged into the top-level chat request body. | Use only for provider-specific flags such as `{"enable_thinking": true}`. Reasonix still owns core fields such as `model`, `messages`, `tools`, `stream`, and `thinking`, and null values are rejected. |
| Authorization: Bearer | For Anthropic-compatible providers, sends the saved API key as `Authorization: Bearer <key>` instead of `x-api-key`. | Enable it only when the gateway documents Bearer auth, such as MiniMax Global or Vercel AI Gateway. |
| Model capability mode | Which reasoning request protocol Reasonix should use for this provider. | Keep **Auto-detect** unless the gateway is misdetected or the model docs require a specific reasoning format. |
| Thinking override | Provider-specific override for `thinking.type`. | Keep **Auto** unless the backend documents `enabled`, `disabled`, or `adaptive`. Unsupported values can make some OpenAI-compatible gateways reject the request. |
| Balance URL | Optional endpoint for wallet/balance lookup. | Set it when the provider exposes a balance endpoint and you want the desktop status bar to show it. |
| Context window | The provider-wide token budget Reasonix uses for automatic context cleanup. `0` disables automatic compaction. | Set it to the provider's model context limit; use a per-model override below when selected models differ. |

Each selected model also has an optional **Context window** input. Leave it blank
to inherit the provider-wide value, or enter a positive token count to override
that value for this model. This avoids premature compaction for long-context
models and provider errors for shorter-context models sharing the same endpoint.
Use the context-window limit from the model documentation, not the maximum output
tokens. For example, 128K commonly means `128000`; if the provider documents
`131072`, use that exact value. Values below 16384 show a non-blocking warning
because they can trigger frequent compaction and reduce cache hit rates.

Model capability mode options:

| Option | Effect |
| --- | --- |
| Auto-detect (recommended) | Reasonix chooses the request shape from model capability metadata and endpoint detection. |
| DeepSeek thinking | Uses DeepSeek-style thinking control, including `thinking.type` and DeepSeek-supported reasoning depth. |
| OpenAI reasoning | Uses the standard OpenAI-compatible `reasoning_effort` levels. |
| Plain chat | Sends no reasoning or thinking control fields. Use this for text-only proxies that reject reasoning parameters. |

Thinking override options:

| Option | Effect |
| --- | --- |
| Auto (provider default) | Does not write an explicit provider-level `thinking` override. Reasonix uses the provider/model default behavior. |
| Enabled | Sends `thinking.type = "enabled"` for compatible providers. |
| Disabled | Sends `thinking.type = "disabled"` for compatible providers. On DeepSeek-style providers this also avoids sending a reasoning depth hint. |
| Adaptive (self-adjusting) | Sends or preserves `thinking.type = "adaptive"` only for providers that document adaptive thinking, such as MiniMax-M3-style endpoints. |

Some OpenAI-compatible gateways require non-standard top-level request body
fields. Add them with `extra_body` on the provider entry:

```toml
[[providers]]
name        = "spark"
kind        = "openai"
base_url    = "https://maas-coding-api.cn-huabei-1.xf-yun.com/v2"
models      = ["xopglm52"]
api_key_env = "SPARK_API_KEY"
extra_body  = { enable_thinking = true }
```

`extra_body` is merged into the chat JSON request body. Reasonix keeps core
fields such as `model`, `messages`, `tools`, `stream`, and `thinking` under its
own control.

## Per-project user id (`cachecontext`)

To attribute each project separately (DeepSeek KV-cache isolation / abuse
tracking), set the top-level `cachecontext` per project instead of repeating the
provider entry. It is sent as the DeepSeek `user_id` (`metadata.user_id` on the
Anthropic endpoint, `user` on the OpenAI endpoint):

```toml
cachecontext = "my-project"
```

The value must match `^[a-zA-Z0-9_-]+$` (≤512 chars); anything else makes
DeepSeek return an HTTP 400. In the desktop app, set it in the **Project**
settings section of the project tab — it is not exposed in global settings.

## Desktop hooks

Desktop hooks run local commands at lifecycle events such as `SessionStart`,
`UserPromptSubmit`, `PreToolUse`, and `PreCompact`. A successful `SessionStart`
hook may write plain text to stdout, or return JSON with
`hookSpecificOutput.additionalContext`; Reasonix injects that text once into the
next real user turn as `<hook-context event="SessionStart">...</hook-context>`.
This is intended for plugin or workflow bootstrap context, including
Superpowers-style startup instructions, without baking that workflow into
Reasonix's system prompt.

Plugin packages can provide this startup context through
`hooks/session-start-codex` or a plugin-root `CLAUDE.md`. Claude-style
`.claude/settings.json` command hooks are also mapped to matching Reasonix hook
events.

The injected hook context is dynamic current-turn context. It does not change
the stable system prompt, memory prefix, or tool schema, though dynamic content
can still reduce cache reuse for that turn. The detailed desktop hook schema and
loading model are documented in [the Chinese desktop hooks guide](./DESKTOP_HOOKS.zh-CN.md).

## Keyboard shortcuts

Shortcuts are documented by client because users usually look for the keys that
work in the surface they are using. Desktop keeps its Plan toggle, while the CLI
cycles Ask, Auto, and Plan with `Shift+Tab`. Desktop uses `Cmd+Y` on macOS or
`Ctrl+Y` elsewhere for YOLO by default. If YOLO is rebound on Windows/Linux,
`Ctrl+Y` becomes the standard composer redo fallback. Desktop paste stays on the
platform paste key; in the CLI, terminal-native text paste and
application-owned image paste use separate shortcuts.

`[ui].shortcut_layout` is still accepted for old configs, but the shortcut
behavior below is unified across layouts.

For CLI/TUI text input, `[ui].cursor_shape` accepts `underline`, `block`, or
`bar`. The default is `bar`: it remains easy to locate without covering
double-width CJK characters in mixed-language input. Set it to `block` for a
traditional terminal cursor or `underline` for a lower-profile cursor. This
setting does not change desktop or web text fields.

### Desktop GUI

The Desktop Todo shelf derives its label from both `todo_write` and the owning
tab's runtime: active work is **In progress**, an approval or question is
**Waiting for input**, and an idle/restored current item is **Ready to continue**.
The latter exposes a **Continue** action that rechecks the captured tab before
sending, so a rapid tab switch cannot route stale work into another session.

Desktop shortcuts are managed from **Settings → Shortcuts**. Pick a configurable
row, press a new key combination, and Reasonix saves it for the desktop app.
Standard editing shortcuts such as Undo and Redo are shown as locked rows because
the WebView's native text history uses those platform chords. Conflicting
bindings are rejected so one shortcut never triggers two actions. Press `?` or
use the help button in the topic bar to open the shortcuts sheet; it is generated
from the same shortcut registry, so it reflects any custom bindings.

Global shortcuts:

| Key or control | What it does | Notes |
| --- | --- | --- |
| `Cmd+K` on macOS, `Ctrl+K` on Windows/Linux | Toggles the command palette | The palette focuses search when it opens; `Esc` closes it. |
| `Cmd+,` on macOS, `Ctrl+,` on Windows/Linux | Opens Settings | Use **Shortcuts** in Settings to customize desktop bindings. |
| `Cmd+W` on macOS, `Ctrl+W` on Windows/Linux | Closes the active top tab | The last tab is kept by the normal close-tab guard. |
| `Cmd+B` / `Ctrl+B` | Shows or hides the left sidebar | Same action as clicking the sidebar toggle. |
| `Cmd+Shift+B` / `Ctrl+Shift+B` | Expands or collapses the most recent shell output | Same action as clicking the collapsed shell-output hint. |
| `Cmd+1`-`Cmd+9` on macOS, `Ctrl+1`-`Ctrl+9` elsewhere | Jumps to the matching visible chat in the sidebar | Hold `Cmd`/`Ctrl` briefly to reveal the numbered badges. Existing custom shortcuts that already use the same key take precedence. |
| `Cmd++`, `Cmd+-`, `Cmd+0` on macOS; `Ctrl++`, `Ctrl+-`, `Ctrl+0` elsewhere | Increases, decreases, or resets text size | `=` is accepted for the plus key on keyboards that report it that way. |
| `?` | Opens the keyboard shortcuts sheet | The sheet shows the current effective desktop bindings. |

Composer shortcuts:

| Key or control | What it does | Notes |
| --- | --- | --- |
| `Enter` | Sends the current message | IME composition confirmation is left alone. |
| `Shift+Enter` | Inserts a newline | The composer keeps focus. |
| `Shift+Tab` | Toggles Plan on/off | Plan changes the workflow instruction; built-in writers keep the active Ask/Auto/YOLO and Sandbox boundary, while MCP writer/destructive targets stay hard-blocked for the whole planning phase. |
| `Cmd+Z` on macOS, `Ctrl+Z` on Windows/Linux | Undoes the latest composer edit | Native typing stays in the WebView history; Reasonix-managed paste, cut, folded blocks, and structured tokens are restored as complete transactions. |
| `Cmd+Shift+Z` on macOS, `Ctrl+Shift+Z` on Windows/Linux | Redoes the latest composer edit | On Windows/Linux, `Ctrl+Y` is also accepted after the YOLO shortcut has been rebound. |
| `Cmd+Y` / `Ctrl+Y` (default) | Toggles YOLO on/off | Turning YOLO off restores the previous Ask/Auto base when known. The current binding is shown in **Settings → Shortcuts**. |
| `Cmd+V` on macOS, `Ctrl+V` on Windows/Linux | Pastes clipboard content | Clipboard images are attached; images can also be dropped into the composer. On official DeepSeek, `deepseek-flash` and `deepseek-v4-flash` accept images natively; V4 Pro stays text-only. |
| Plain `Up` / `Down` at the prompt boundary | Recalls older or newer submitted prompts | Modified arrows and native text navigation stay with the textarea. |
| `Esc` while a turn is running | Cancels the running turn | If the turn has not produced a response yet, the draft is restored. |

Menus and controls:

| Key or control | What it does | Notes |
| --- | --- | --- |
| `Up` / `Down` in slash, `@`, or past-chat menus | Moves the highlighted item | Past-chat search uses the same navigation keys. |
| `Enter` / `Tab` in those menus | Accepts the highlighted item | Directory-like entries can keep the menu open for the next level. |
| `Esc` in those menus | Closes the current menu or returns from past-chat search | Regular typing continues after the menu closes. |
| Ask / Auto / YOLO approval controls | Picks the tool approval posture directly | Clicking these controls is unchanged by keyboard shortcuts. |
| Tool approval card | `Left` / `Right`, `Enter`, `1`-`4`, `Esc` | Move the highlighted action, confirm it, pick a numbered action, or deny. The default highlighted action is Allow once. |
| Plan approval card | `Left` / `Right`, `Enter`, `1`-`3`, `Esc` | Move between Revise plan, Start execution, and Exit plan. The default highlighted action is Start execution. |
| Plan control | Toggles Plan on/off | Same mode as `Shift+Tab`. |
| Goal item in the collaboration menu | Starts, views, or clears Goal | Goal is not in any keyboard cycle. |

### CLI / TUI

The composer uses theme-coloured top and bottom borders and a slim bar cursor by
default. Long drafts grow to the available maximum height; once they overflow,
wheel events inside the composer scroll the draft without moving the insertion
cursor, while wheel events in the transcript keep scrolling the conversation.
Use `/theme auto|light|dark` to select the background mode, or `/theme <style>`
to select one of the named accent palettes shown by bare `/theme`.

The responsive footer keeps the active Ask/Auto/Plan or YOLO posture and current
interaction state on the left. On wider terminals, model and effort
stay together on the right; a second row shows available Git identity, cache hit
rate, context use, compaction headroom, jobs, and balance. `ready` is the idle
composer state, not a model-health check. Pickers, approvals, image paste, shell
mode, and other active interactions replace it. Narrow terminals move, wrap, or
compact whole groups; visible labels follow `/language`.

Chat and transcript shortcuts:

| Key or command | What it does | Notes |
| --- | --- | --- |
| `Enter` | Sends the current message | While a turn is running, non-empty input is durably queued as a follow-up before the composer clears. |
| `Ctrl+Enter` or `/steer <text>` | Adds guidance to the active turn | The guidance is persisted first; if the turn cannot accept it, it remains a normal follow-up. |
| `Shift+Enter`, `Alt+Enter`, or `Ctrl+J` | Inserts a newline | Plain `Enter` is reserved for send/confirm. |
| Plain `Up` / `Down` while idle | Recalls older or newer submitted prompts | In a running turn, the same keys navigate queued follow-up feedback. |
| `PageUp` / `PageDown` | Scrolls the transcript | Works regardless of the current chat state. |
| `Ctrl+Home` / `Ctrl+End` | Jumps to the top or bottom of the transcript | Useful after long tool output. |
| `Ctrl+L` or `/cls` | Clears only the visible transcript | The LLM context, session file, tools, memory, and plugins stay loaded. Use `/clear` when you want to discard the conversation context. |
| `Esc` | Backs out of the current action | It un-sends a just-submitted turn before any reply, cancels a running turn, or clears non-empty input. |
| Double `Esc` on an empty idle composer | Opens the rewind picker | Same entry point as `/rewind`. |
| Transcript text selection | Copies transcript text | Releasing an in-app drag writes through the verified native clipboard path in a local session (`pbcopy` on macOS, the available Wayland/X11 tool on Linux, or the Windows clipboard). SSH falls back to OSC 52 and labels the fallback instead of claiming native success. `Ctrl+C`/`Super+C`/`Meta+C` or right-clicking the active selection copies it again. |
| Composer text selection | Selects, copies, or replaces draft text | Releasing an in-app drag copies the selection through the same verified clipboard path as transcript text. Typing or pasting replaces the selection; arrow keys collapse it. |
| Right-click with no active selection | Pastes clipboard text locally | In a local session with in-app mouse capture on, Reasonix reads text only and routes it through the normal bracketed-paste handling. Over SSH, use the terminal paste shortcut because the remote process cannot read the local clipboard; `/mouse` restores the terminal's native right-click menu. Right-click with an active selection still copies that selection. |
| `/mouse` | Toggles in-app mouse capture | Off hands the mouse back to your terminal, restoring its native click-drag selection and right-click context menu, at the cost of in-app drag-select, the transcript scrollbar, and wheel-scroll. Set `REASONIX_DISABLE_MOUSE=1` to start every session with it off. Remote (SSH) sessions start with capture off so native selection works out of the box; `REASONIX_DISABLE_MOUSE=0` forces capture on everywhere. Over SSH the TUI also enables synchronized output (mode 2026) so repaints do not flicker on the round trip; set `REASONIX_DISABLE_SYNC_OUTPUT=1` to opt out. |
| `Ctrl+C` | Copies, cancels, clears, or quits | Copies an active transcript or composer selection first. Otherwise it cancels a running turn, clears non-empty input, or quits on a second empty-composer press. |
| `Ctrl+D` | Quits the TUI | Immediate quit. |
| Your terminal's text-paste shortcut | Pastes text | Text stays on the terminal's bracketed-paste path (`Cmd+V` on macOS, commonly `Ctrl+Shift+V` on Linux, and the terminal's configured shortcut elsewhere). Reasonix consumes the resulting paste event and never probes for an image first. |
| `Ctrl+V` on macOS/Linux; `Alt+V` on Windows | Pastes a clipboard image | Image paste is a separate application action. The footer shows `Pasting image…` while the clipboard is read, then inserts an editable `[image #N]` token at the cursor. |
| `/paste-image` | Pastes a clipboard image | Command form of the same image-only action. |
| A line starting with `!` | Runs a shell command directly | The command runs locally without asking the model. |

`/queue list` shows bounded previews without loading full bodies. Use `/queue
show|edit|delete|move`, `/queue pause|resume`, and `/queue retry|refresh` to
inspect or manage pending work. After crash recovery the inbox is paused, so
review it and run `/queue resume` before dispatch continues. Each item is
limited to 4 MiB; a session accepts at most 64 items and 64 MiB total.

Mode and display shortcuts:

| Key or command | What it does | Notes |
| --- | --- | --- |
| `Shift+Tab` | Cycles Ask → Auto → Plan → Ask | YOLO remains outside this composer-mode cycle; the footer shows the active mode. |
| `Ctrl+Y` | Toggles YOLO on/off | Turning YOLO off restores the previous Ask/Auto base when known. Terminals that forward Command/Super may also send `Cmd+Y`, but `Ctrl+Y` is the reliable terminal shortcut. |
| `--yolo`, `--dangerously-skip-permissions` | Starts chat in YOLO | Same runtime mode as `Ctrl+Y`. |
| `/theme [auto|light|dark|style]` | Shows or switches the CLI theme | Bare `/theme` lists background modes and named accent palettes. The choice is saved to the user config; `REASONIX_THEME` and `REASONIX_THEME_STYLE` can override it for one run. |
| `Ctrl+O` | Toggles verbose reasoning display | Also available through `/verbose`. |
| `Ctrl+B` | Expands or collapses long shell output | Long shell-output hint lines can also be clicked in the transcript; text selection is handled in-app while the full-screen TUI has mouse reporting enabled. |
| `/goal <objective>`, `/goal status`, `/goal pause`, `/goal resume`, `/goal clear` | Starts, checks, pauses, resumes, or clears Goal | A Goal is unbounded unless `[agent].goal_token_budget` is set. |
| `/migrate`, `/migrate --from <legacy-dir>` | Retries legacy migration or imports sessions from a chosen v0.x source | Use `--from` for custom Windows v0.52 install/data directories; it imports sessions only. See [Configuration paths](./CONFIG_PATHS.md). |

Picker and approval shortcuts:

| Context | Keys | What they do |
| --- | --- | --- |
| Slash or `@` completion | `Up` / `Down`, `Ctrl+P` / `Ctrl+N`, `Tab` / `Enter`, `Esc` | Move, accept, or close the completion menu. |
| Tool approval prompt | `y`/`1`, `a`/`2`, `p`/`3`, `n`/`4`, `Enter`, `Esc`, `Ctrl+C` | Allow once, allow for session, persist allow, deny, accept default allow once, deny, or cancel the turn. |
| Ask question card | `Up`/`Down` or `j`/`k`, `Left`/`Right` or `h`/`l`, `Space`, `Enter`, `1`-`9`, `Esc`, `Ctrl+C` | Navigate answers/tabs, toggle multi-select answers, submit/activate, pick numbered options, dismiss, or cancel the turn. |
| Rewind picker | `Up`/`Down` or `j`/`k`, `Enter`, `b`, `c`, `d`, `f`, `s`, `u`, `Esc` | Choose a turn, apply both/conversation/code/fork/summarize actions, or go back/close. |
| Model, provider, or resume picker | `Up`/`Down` or `Ctrl+P`/`Ctrl+N`; `j`/`k` while search is empty; type to filter; `Enter`; `Esc` | Search, select an item, or close the picker. Once search input starts, `j`/`k` become query text. `/provider` opens that provider's model list. |
| MCP import picker | `Up`/`Down` or `j`/`k`, `Space`, `Enter`, `Esc` / `Ctrl+C` | Move, select servers, import selected servers, or cancel. |
| MCP manager | `Up`/`Down` or `j`/`k`, `Enter`, `Left`/`Right` or `h`/`l`, `r`, number keys, `q` / `Ctrl+C` | Navigate server lists/details, refresh, choose actions, or close. |
| `/clear` confirmation | Arrow keys or `j`/`k` / `Tab`, `Enter`, `y`, `n`, `Esc` / `Ctrl+C` | Toggle Clear/Cancel, confirm clear, or cancel. In YOLO mode `/clear` clears immediately without asking. |

Mode meanings:

| Mode | Meaning |
| --- | --- |
| Ask | Prompts for fallback writer approvals. |
| Auto | Auto-allows fallback approvals, including interactive `remember`/`forget`; explicit `ask` / `deny` rules still apply. |
| YOLO | Skips ordinary tool approval prompts, including `remember`/`forget`; `deny`, user `ask` questions, and plan approval prompts still wait. |
| Plan | Directs the model to plan first — a plan-first workflow, not an all-tools read-only mode. Built-in writers still follow the active Ask/Auto/YOLO rules and Sandbox; installed MCP writers, destructive targets, and readers from unauthorized servers are hard-blocked for the whole planning phase (approval cannot release them; they return once Plan exits), and explicit phase-only tools such as `complete_step` wait until approval. |
| Goal | Pursues a saved objective until complete, blocked, or cleared. |

## Permissions & sandbox

Permissions gate each tool call: `deny` > `ask` > `allow` > fallback. Bash and
file mutation tools require approval by default; read-only tools generally do
not. Approvals are stored and matched as permission rules, not button labels:
for example `Bash(npm run build)`, `Bash(npm run test:*)`, and `Edit(docs/**)`.
`reasonix` can grant Bash as an exact command or as a conservative command
prefix (for example `Bash(go test:*)`), while file-editing tools share session
edit grants and persist path-scoped rules such as `Edit(src/app.go)`.
Parameter/arithmetic expansions, assignments, heredocs, file redirects, and globs cannot reuse a bare
Bash, prefix, or glob allow; a user-approved reusable choice saves the whole
command as `Bash=<literal>`. They still follow normal fallback, so Auto executes
them without an extra prompt. Command/process substitution, a dynamic command
name, `eval`, `source`, shell `-c`, inline runtime code, and unparseable forms
require a human in interactive Ask/Auto. Headless Ask/Auto/DontAsk reject that
nested/indirect class unless an exact literal exists; YOLO may bypass it.
Advanced users can set `[permissions] allow_dynamic_bash = true` to let an
Allow fallback, including Auto, cover that class; explicit `ask` and `deny`
rules still take precedence.
Because a headless run has no approval UI, the default Ask posture also fails
closed on ordinary writer fallback and explicit ask rules. Use
`reasonix run --auto ...`, `-y`, or `--permission-mode auto` when unattended
automation should allow ordinary writer fallback; configured `ask` and `deny`
rules always remain authoritative.

Ask is not read-only: after approval, a writer can still run. Permissions decide
whether to allow or prompt; the Sandbox is the enforced capability boundary.
The sandbox remains a second boundary after authorization; confinement cannot
make ambiguous command parsing safe to authorize automatically.

Permissions are *policy* (which calls to allow / prompt). The **sandbox** is
*enforcement*: they are two layers. A permitted call still cannot write outside
the approved roots. The file-writers (`write_file` / `edit_file` / `multi_edit` / `move_file`)
refuse any path outside `[sandbox] workspace_root` (default: the current dir, so
edits stay in the project), resolving symlinks and `..` so a link can't tunnel
out. Writing outside the workspace is an interactive *extend write access*
approval (once / this session / add to the project-local config / deny), not a
sandbox escape. Bash must name those directories with `additional_write_dirs`
plus a `justification`; the host does not infer paths from the command text.
Headless `reasonix run` does not prompt: pass `--add-dir` or configure
`[sandbox].allow_write`. The whole home directory can be approved with a
high-risk warning; the filesystem root and Reasonix session/state paths cannot. `forbid_read` optionally hides sensitive files or directories from the agent's
read/list/search tools; use absolute paths or `${HOME}` / `${VAR}` references,
not `~`, because config expansion is environment-variable based. `bash` is
itself jailed by default when an OS sandbox is available (`[sandbox] bash`,
Seatbelt on macOS and bubblewrap on Linux):
commands may write only those same roots plus platform-specific command
temp/cache roots, cannot read configured `forbid_read` roots while the OS
sandbox is active, and reach the network only when `[sandbox] network` is set.
Reasonix always removes saved provider and bot credential variables from tool
subprocess environments and automatically adds its global credential `.env` to
the runtime read-deny boundary. Project `.env` files keep their existing
workspace-scoped behavior.

**Session-private temporary directory.** Within one logical chat session, Bash
commands share a private temporary directory so consecutive calls can exchange
files through `$TMPDIR` (and, on Linux under bubblewrap, through literal
`/tmp`). No user setup is required: Reasonix automatically exports `TMPDIR`,
`TMP`, and `TEMP` for Bash and client-owned ACP terminals. The directory is
created lazily, is never the host public temporary root, and is rotated on
`/new`, `/clear`, resume of another session, and branch switches.
Model/settings hot rebuilds keep the same directory. Temporary files are not
durable storage: resume across process restarts does not restore them, and
scripts that need long-lived data should write into the workspace or a
user-specified path.

Reasonix-generated and project scripts should use the standard temporary
environment variables rather than hard-coding `/tmp`; users should not set
these variables themselves. For example:

```sh
tmp_file="${TMPDIR:?}/result.json"
```

```powershell
$tmpFile = Join-Path $env:TEMP "result.json"
```

| Platform | `$TMPDIR` / `$TMP` / `$TEMP` | Literal `/tmp` |
| --- | --- | --- |
| Linux + bubblewrap | Virtual `/tmp` (bound to the private dir) | Shared for the session (not a fresh empty tmpfs each call) |
| macOS Seatbelt | Host path of the private dir (allowed by policy) | Host macOS temporary directory; scripts should use `$TMPDIR` |
| Windows (no OS Bash sandbox) | Host path of the private dir | Not promised to match (e.g. Git Bash `/tmp`) |

Independent sandboxes such as MCP servers keep their own isolation and do not
inherit the chat session's temporary directory. An approved sandbox-escape
command still receives the private temp environment variables, but on Linux its
literal `/tmp` is no longer mapped by bubblewrap.

**Windows note:** Reasonix does not ship an OS-level Bash sandbox on Windows.
The effective mode is fixed to `off`; even an older config containing
`bash = "enforce"` resolves to `off`, `reasonix doctor` flags the ignored value,
and the desktop selector is read-only. Bash commands therefore run unconfined,
while the dedicated file tools still enforce `workspace_root`, `allow_write`,
and `forbid_read` in process. Saved credential variables are still removed from
the child environment, but an approved unconfined shell runs as the user and is
not a security boundary for other user-readable files.

When no OS sandbox backend is available, `bash = "enforce"` refuses bash
execution instead of running unconfined. Install the platform sandbox backend
(bubblewrap/`bwrap` on Linux, `sandbox-exec` on macOS) or set
`[sandbox] bash = "off"` to explicitly restore the pre-1.16 unconfined shell
behavior. On Windows the compatible value is always `off`.

For coding-quality reports, run `reasonix doctor quality <branch-id-or-path>`
(add `--json` for structured output). This reads the selected session but emits
only content-free counts and profile categories: model family, runtime profile,
collaboration / approval modes, message and tool-call counts, verification and persisted
compaction-summary counts, plus desktop token/cache telemetry when available.
It omits transcript text, paths, session identifiers, tool arguments and output,
endpoints, and custom model names, so the result is suitable for a public issue
or Discussion. This differs from `reasonix doctor session`, whose support zip
contains the complete unredacted transcript and must remain in a trusted support
channel.

## Capability diagnostics

Use this when a skill, slash command, hook, plugin package, MCP server, or
`AGENTS.md` is missing, shadowed, disabled, or fails to start. Full flag
reference, JSON schema, and issue codes:
**[Capability diagnostics](./CAPABILITY_DIAGNOSTICS.md)**.

```bash
# Static (default): no network, no MCP child processes
reasonix doctor capabilities

# Machine-readable (stdout is pure JSON)
reasonix doctor capabilities --json

# Another workspace root
reasonix doctor capabilities --root /path/to/project

# Live MCP probe — only when you explicitly allow starting third-party servers
reasonix doctor capabilities --live --timeout 5s
```

| Surface | How |
| --- | --- |
| CLI | `reasonix doctor capabilities` (above) |
| Desktop | **Settings → Diagnostics** — refresh, copy redacted JSON, optional “include current session runtime” (reads the active tab Host only; does **not** start MCP) |
| Agent | `/reasonix-guide` (built-in inline skill) or ask naturally; it prefers static doctor JSON before `--live` |

Exit code `0` allows warnings/info; `1` means at least one `error` (or a live
start failure); `2` is bad flags. This is separate from `reasonix doctor`
(providers/sandbox) and `reasonix plugin doctor <name>` (one package).

## Plugins (MCP)

Reasonix is an MCP client. A `[[plugins]]` entry's `type` selects the transport:
`stdio` (default) launches a local subprocess (`command`/`args`/`env`); `http`
(Streamable HTTP) connects to a remote `url` with optional static `headers`
(`${VAR}` / `${VAR:-default}` expanded from the environment, so tokens stay out
of the file); `sse` connects to servers that still use the legacy persistent
GET + announced POST endpoint transport.

For a remote HTTP server without a static `Authorization` header, an
authentication challenge is shown as **Sign in**. Run
`reasonix mcp auth <name>` in the CLI, or click **Sign in** for that server in
the Desktop MCP panel. Reasonix performs OAuth metadata discovery, dynamic
client registration, PKCE S256 authorization, and refresh-token
rotation. Discovery and token requests use the same Reasonix network-proxy
settings as the MCP connection.

OAuth client and token state is kept outside the workspace in the server's
private Reasonix state directory, written with mode `0600`, and bound to the
full configured resource URL. An explicit static `Authorization` header always
takes precedence. **Clear authentication** removes only Reasonix's local OAuth
state; it does not sign out the third-party browser session. Reasonix opens the
browser only after an explicit sign-in action, never automatically from a
background tool-call failure. Removing the MCP server also removes its local
OAuth state unless a lower-priority declaration for the same resource becomes
effective.

Browse the official MCP Registry from **Settings → MCP servers → Browse
registry**, or use `reasonix mcp browse [query]` and
`reasonix mcp install <registry-name>`. Registry access is explicit and never
runs during startup. Entries that need secrets or required arguments are shown
as manual setup instead of being installed with an incomplete configuration;
query-specific cached results remain available during a registry outage.

The normal setup path is intentionally one step. Use Desktop's **Add and
connect**, `/mcp add`, or ask Reasonix to install a package or URL. These
explicit installs are saved to the user-global `config.toml` and are also
authorization: the server connects in the current session, and no second trust
step appears now or on the next startup. Servers declared by the current
project's `reasonix.toml` or `.mcp.json` remain in that project and are trusted
without a separate launch confirmation. Explicit deny rules still win. The
server's calls run
directly, including tools that declare `destructiveHint`. The dedicated Planner
still refuses destructive tools, and strict read-only sub-agents still expose
only hinted non-destructive readers.

MCP names are resolved once per workspace. Project declarations override
same-name global installs; inside a project, `reasonix.toml` overrides
`.mcp.json`. Editing updates the effective declaration in its original file,
and removing a higher-priority declaration reveals the next one instead of
deleting every same-name entry.

stdio servers keep one process for initialize, reads, and writes, so stateful
servers such as browsers retain sessions and open pages. Because an OS sandbox
is fixed when a process starts, this shared process uses the server's normal
process sandbox for every call; `readOnlyHint` and read-only sub-agent filtering
are dispatch policy, not a second per-call process sandbox.

Tools surface to the model as `mcp__<server>__<tool>`. A tool declaring MCP's
`readOnlyHint: true` joins parallel dispatch and the strict read-only tool
surfaces. Installing a server or declaring it in project configuration
authorizes the dedicated Planner to use all of its non-destructive
tools without another per-tool setting; strict read-only research sub-agents
receive only hinted non-destructive readers. Tools without the hint remain
write-capable for scheduling and mutation accounting. While planning, built-in
writers keep the ordinary permission posture. The dedicated Planner permits
authorized non-destructive MCP (including opaque writers) but hard-blocks
destructive or unauthorized targets; a single-model Plan without that dedicated
Planner keeps the older writer/destructive block until Plan exits.

Installing an MCP server is the authorization decision. After installation, all
of its tools run directly without a second server-level, per-tool, writer, or
destructive approval setting. Explicit global deny rules still win. The host
keeps `readOnlyHint` and `destructiveHint` internally for parallel scheduling,
Plan restrictions, strict read-only sub-agents, and cached-to-live safety
reclassification; these hints do not add user configuration.
Reasonix deliberately trusts an installed server to describe those hints
honestly. Planner/read-only filtering is therefore a workflow boundary for
trusted servers, not containment against a malicious MCP server; explicit deny
rules and the process sandbox remain host-controlled boundaries.

The retired `trusted_read_only_tools`, `default_tools_approval_mode`,
`tools.<raw>.approval_mode`, and `approvals_reviewer` fields are ignored when
loading older files and removed the next time Reasonix saves that MCP entry.

A server's **prompts** surface as `/mcp__<server>__<prompt>` slash commands
(positional args after the command); its **resources** are pulled in by writing
`@<server>:<uri>` in a message; `/mcp` lists connected servers and what each
exposes. `make build` also produces `bin/reasonix-plugin-example` — a runnable
reference stdio server (`echo`, `wordcount`, a `review` prompt, a style-guide
resource) you can copy.

```toml
[[plugins]]                       # local stdio server
name    = "example"
command = "reasonix-plugin-example"
# startup_timeout_seconds = 60    # optional initialize + tools/list cap
# call_timeout_seconds = 600       # optional per-server MCP call timeout
# tool_timeout_seconds = { "generate_video" = 1800 }   # optional raw MCP tool names

[[plugins]]                       # remote server over Streamable HTTP
name    = "stripe"
type    = "http"
url     = "https://mcp.stripe.com"
headers = { Authorization = "Bearer ${STRIPE_KEY}" }
```

Enabled MCP servers start connecting automatically in the background after a
session begins, so chat stays usable while tools come online. Use `/mcp` or the
desktop MCP panel to refresh status, reconnect a server, inspect failures, or
disable a server for the current session. For a read-only config/runtime health
report across skills, hooks, packages, and MCP (without changing settings), see
[Capability diagnostics](./CAPABILITY_DIAGNOSTICS.md)
(`reasonix doctor capabilities` or **Settings → Diagnostics**).

An interactive caller waits only briefly for a cold server. If that wait ends,
the shared startup continues in the background rather than being killed and
restarted; retry the tool after it comes online. `mcp_startup_timeout_seconds`
(default `30`) bounds the full launch, authorization, initialize, and
`tools/list` sequence. `mcp_call_timeout_seconds` applies only after the server
is connected. Either value can be overridden per server.

**Already have an `.mcp.json`?** Drop it in the project root and Reasonix
reads it as-is — the `mcpServers` spec (`command`/`args`/`env`, `type`/`url`/
`headers`, `${VAR}` expansion) maps field-for-field onto `[[plugins]]`. Both
sources are merged; on a name collision `reasonix.toml` wins.

```json
{
  "mcpServers": {
    "filesystem": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "/path"] },
    "stripe": { "type": "http", "url": "https://mcp.stripe.com", "headers": { "Authorization": "Bearer ${STRIPE_KEY}" } }
  }
}
```

**Upgrading from `0.x`?** Your old `~/.reasonix/config.json` is still read for its
`mcpServers` (honouring `mcpDisabled`) as a lowest-priority source, so MCP servers
keep working — move them into `reasonix.toml`'s `[[plugins]]` or a `.mcp.json` when
convenient.

## Slash commands

In an interactive `reasonix` session, built-in commands (`/compact`, `/context`, `/new`, `/clear`, `/rewind`,
`/tree`, `/branch`, `/switch`, `/todo`, `/model`, `/mcp`, `/skills`, `/hooks`,
`/memory`, `/goal`, `/output-style`, `/sandbox`, `/language`,
`/reasoning-language`, `/help`) run
locally — `/help` lists them all. Built-in **skills** such as `/init`,
`/explore`, `/test`, and `/reasonix-guide` also appear in the slash menu and via
`run_skill` (bodies load on demand; only the index line is cache-stable). Use
`/reasonix-guide` when you need config or capability troubleshooting; it points
at `reasonix doctor capabilities` (see
[Capability diagnostics](./CAPABILITY_DIAGNOSTICS.md)). `/new` starts a new
session while saving the previous transcript for history/resume; `/clear`
discards the current context without saving it — it asks for confirmation,
except in YOLO mode where it clears immediately (YOLO already opts out of
confirmations). `/tree`
shows saved conversation branches, `/branch [name]` forks the current
conversation tip, `/branch <turn> [name]` forks from an earlier checkpointed
turn, and `/switch <id|name>` loads another branch. **Custom commands** are
Markdown files under `.reasonix/commands/` (project) or `~/.reasonix/commands/`
(user) — `review.md` becomes `/review`, a subdirectory namespaces it
(`git/commit.md` → `/git:commit`). The body is a prompt template; invoking the
command sends it as a turn.

### Subagent profiles

Subagent profiles are manual Skills with `runAs: subagent` and
`invocation: manual`. They are stored in the same project/global Skill roots as
the desktop settings page, so profiles created on either surface are immediately
available to the other after the session refreshes. In interactive chat, invoke
one with `/<name> <task>`; Reasonix runs an isolated child loop and keeps only
the task and final answer in the parent conversation.

The headless CLI provides explicit management and execution commands without
changing the ordinary `reasonix run` task semantics:

```bash
reasonix subagent list
reasonix subagent create reviewer --description "Review changes" --prompt-file reviewer.md --tools read_file,grep,bash
reasonix subagent edit reviewer --effort high --model deepseek-pro
reasonix subagent try reviewer "review the current diff"   # always read-only
reasonix subagent run reviewer "review and fix the current diff"
reasonix subagent delete reviewer --yes
```

`create` defaults to project scope when a workspace is available and to global
scope otherwise; pass `--scope project|global` to choose explicitly. `edit`
changes only explicitly supplied fields, and an empty value such
as `--model=` or `--tools=` clears that field. The profile editors deliberately
refuse custom-path or richer hand-authored Skills so they cannot discard
frontmatter, references, or scripts; manage those files through the Skills
workflow instead. Built-in profiles have no editable file, so `edit` accepts
only `--model` and `--effort` for them and stores the same per-name overrides as
the desktop settings page.

See [Subagent profiles](./SUBAGENT_PROFILES.md) for the complete CLI reference,
Skill file format, model precedence, safety behavior, and troubleshooting.

Context Engine v2 separates two intentionally different layers:

- **Standing instructions** come from hierarchical `REASONIX.md`, `AGENTS.md`,
  and `CLAUDE.md` files. Put rules here when they must be present on every
  relevant turn. User-global files load first, then workspace and deeper target
  directories; within one directory, `.local.md` variants win.
- **Background memory** stores one durable fact per Markdown file. Each fact has
  an immutable ID, monotonic revision, timestamps, independent `type`
  (`user`, `feedback`, `project`, `reference`) and `scope` (`project`,
  `global`), plus freshness metadata. Facts may be stale, so they never outrank
  the current request or standing instructions.

Reasonix automatically recalls a small set of relevant facts before each real
user turn. It searches the raw user message, suppresses generic requests such as
"continue", prefers project facts over equivalent global fallbacks, down-ranks
stale facts, and appends at most four facts / 2,400 characters to the user turn.
This dynamic suffix does not rewrite the cache-stable system prompt or tool
schemas. Use `/memory recall` to see the selected IDs, scores, reasons,
freshness, budget, and suppression decision.

New, bounded, non-sensitive project/reference facts can be created
automatically with no setup or approval click. In Ask, global facts, user
preferences, feedback, updates, duplicates, sensitive/oversized content, and
every `forget` require explicit confirmation. Interactive Auto treats these
memory tools as normal fallback operations while preserving explicit `ask` and
`deny` rules; interactive YOLO bypasses memory ask prompts but still honors
deny. The storage layer makes the automatic create grant create-only, so it
cannot overwrite a fact that appears concurrently.
A top-level headless controller may use the same one-shot low-risk create path;
sub-agents and headless surfaces without the owning scoped controller fail closed.

`forget` archives rather than permanently deletes. Every update snapshots the
previous revision; restore and archive recovery always create a higher revision
instead of overwriting history:

```text
/memory instructions
/memory recall
/memory revisions <id-or-name>
/memory restore <id-or-name> <revision>
/memory archived
/memory recover <archive-path>
```

The desktop Context Center shows the same provenance, conflicts, revision
history, recall trace, and recovery actions. Opening its Suggestions tab scans
recent local user turns automatically; candidates are deduplicated against both
memory scopes and instruction bodies, but nothing is saved until the user
accepts it. Remote workspaces never fall back to local desktop memory or
sessions.

Legacy facts are upgraded in place with deterministic IDs and revision 1;
missing scope is inferred from the containing directory. Migration is
idempotent, old clients retain safe routing, and legacy Memory v5 transcripts
remain readable. For the complete behavior and privacy/cache contract, see
[`Context Engine v2`](SESSION_MEMORY_RETRIEVAL.md).

```markdown
---
description: Review the staged diff
argument-hint: [focus-area]
---
Review the staged diff. Focus on $ARGUMENTS, list bugs with file:line.
```

`$ARGUMENTS` expands to all space-separated args, `$1`…`$N` to positional ones.
MCP prompts also appear here as `/mcp__<server>__<prompt>`.

## Embedded documentation retrieval

Reasonix bundles the Markdown files from `docs/` and the reviewed
`release-notes/releases.json` catalog into each CLI and Desktop build. The
read-only `docs` tool searches that exact offline corpus with local BM25
retrieval and can read a complete matching section with source provenance. It
renders every release in both languages under paths such as
`changelog/v1.19.5.md` and `changelog/v1.19.5.zh-CN.md`, so questions about a
specific version, upgrades, fixes, or known risks work offline. The agent should
use the tool before web search or assumptions when a question concerns Reasonix
configuration, CLI/Desktop behavior, release history, permissions, MCP, memory,
recovery, providers, or maintainer workflows.

No setup, network connection, vector database, or embedding service is needed.
Search results prefer the query language while retaining explicit `en`,
`zh-CN`, audience, and catalog filters. The docs capability is exposed through
the unified `use_capability` surface for every task. Every result reports
the product version, immutable source revision, and corpus SHA-256 digest. Release
CI compiles the CLI and rejects publication unless that embedded manifest matches
the candidate's `docs/*.md`, `release-notes/releases.json`, and build identity. A
newer online `main-v2` page therefore cannot silently replace version-matched
local guidance or release history.

Use `/docs` to inspect the bundled corpus identity and usage examples without
calling a model. Use `/docs <question>` (for example,
`/docs 1.19.5 changelog`) to make Reasonix search the corpus locally first and
then pass the version-matched evidence to the currently configured AI for a
sourced answer. This command path does not depend on the model deciding to call
the `docs` tool, while ordinary natural-language questions may still use the
tool automatically. Existing custom commands and compatible plugin or skill
aliases keep ownership of `/docs`; when that happens, CLI and Desktop normally
expose the built-in corpus as `/reasonix:docs` instead. If that qualified name is
also already owned, Reasonix selects the next free `reasonix:`-qualified fallback
without displacing it. A remote Desktop uses the host's resolved command catalog,
so the displayed entry always matches what that host will execute.

Pull requests that change user-visible CLI, Desktop, configuration, provider,
permission, or tool behavior must declare whether embedded documentation was
updated. When no documentation change is needed, the declaration must explain
why the existing version-matched guidance remains correct.

## Goal

Goal is the unified runtime for long-running objectives. Reasonix keeps working
until the goal is complete, blocked, paused, or cleared. Ordinary chat never
changes collaboration mode implicitly; choose Goal in the composer or use
`/goal` to start a long-running objective.

Goal has no default model-round, cross-Run turn, wall-clock, or numeric
no-progress limit. It continues until completion, a genuine user/external
blocker, manual stop/pause, an unrecoverable external error, or an explicit
user-selected budget. To place an optional ceiling on an unattended loop, set:

```toml
[agent]
goal_token_budget = 20000000
```

The default is `0` (off). Reaching a positive token budget produces one summary
and a resumable `budget_spend` pause. `/goal resume` grants a fresh configured
slice while cumulative Goal statistics remain intact. Explicit positive
`max_steps`, task time, and task cost budgets remain available as well.
Progress is goal-scoped and novelty based:
new read/search results, mutations, verification, todo/signoff changes, and
reviews advance the goal; an exact tool/argument/result repeat does not.
Cumulative turns, tokens, real provider requests, and active work time are
tracked and shown as statistics; a token limit appears only when explicitly
configured. A paused goal keeps its todos, evidence
checkpoint, and runtime history — use `/goal resume` to continue, or `/goal
pause` to pause a running goal manually. `/goal status` shows turns, requests,
tokens, and work time. Repeated host failures, zero-evidence rounds, and Todo
stall thresholds inject a strategy redirect and reset their intervention epoch;
they do not pause the Goal. At the end of every goal turn
the model reports its disposition through the structured `update_goal` tool
(continue/complete/blocked); when no report arrives, an independent bounded
evaluator judges the turn once, and any evaluator failure pauses the goal
instead of continuing silently.

For complex work, write the objective as a
[task contract](./TASK_CONTRACT.md): Context, Request, Output format,
Constraints, and Pause policy. Goal mode treats those sections as the boundary
for autonomous work. It keeps going with sensible defaults unless the next step
requires an irreversible or externally visible operation, a scope change, or
information only the user can provide.

Legacy simple/write/research classes are still inferred for sidecar and CLI
compatibility, but they no longer select an execution quota. There is no
separate research runtime to configure. Goal state stays in the normal session sidecar, progress
comes only from novel host receipts, canonical todos, `complete_step`, review
and the evidence checkpoint, and completion is decided by closed-loop readiness
plus the bounded Goal evaluator. An `update_goal`
`completion.unverified` account is honored for checks the model could not run; a second
identical complete on the same leftover checks finishes the Goal instead of
looping. Legacy `.reasonix/autoresearch/<task-id>/` archives are
read-only: an explicit old path can be recovered as an ordinary Goal, but new
runs never create or update those directories. Deprecated budget flags are
accepted for compatibility but are hidden from help and completion.

### Ordered batch sign-offs

The host may process multiple `complete_step` calls from one provider tool-call
round. They must follow the canonical Todo order, and each step's work and
evidence must already exist before its sign-off call. The host advances the
Todo state after each successful call; skipped, pending, or out-of-order steps
remain rejected. This does not change the provider-visible tool schema.

## @ references

Embed `@` references in a message and Reasonix resolves them before sending, as
tagged context blocks: `@path/to/file` (or `@dir`) injects a local file's
contents (or a directory listing), and `@<server>:<uri>` injects an MCP
resource. A local path is only treated as a reference when it actually exists,
so ordinary `@mentions` stay literal. Typing `/` or `@` opens an autocomplete
menu — slash commands, or hierarchical file navigation (one directory level at a
time, descend into folders) plus MCP resources.

## Two-model collaboration

`reasonix setup` manages providers, model lists, credentials, connection tests,
and the default model. It stages changes until Save and exit, and synchronizes
provider access with the desktop app. See the [CLI reference](./CLI.md#configure-providers).
Running two models together (executor + planner, separate cache-stable sessions)
is a one-line edit afterwards — set `planner_model` to any other enabled provider:

```toml
[agent]
planner_model = "deepseek-pro"   # used as the low-frequency planner
```

The planner sees loaded `REASONIX.md` / `AGENTS.md` memory and a small read-only
research tool set, so it can inspect relevant files before handing a plan to the
executor. Writer and workflow tools remain executor-only.

Reasonix routes each turn deterministically without another classifier model.
Ordinary requests always stay with the executor. The dedicated planner runs
only for an explicit `plan first` / `先规划` request, an explicit wait-for-
approval boundary, an explicit `plan only` / `不要执行` request, or Goal
start. Wording such as "complex refactor" or "fix login" does not start the
planner. There is no automatic planning depth. Explicit Plan Mode
remains a separate host workflow on the executor and is never planned twice.
`just do it` / `直接改` also stays with the executor. Execution boundaries are
recognized across the request, not only at its beginning, while quoted
examples are ignored. Bare plan-first requests continue from the planner to
the executor automatically. Requests that explicitly say to wait for
confirmation pause at the host approval boundary and continue to the executor
after approval. Only an explicit `plan only` / `不要执行` request ends the
current turn with the plan persisted and no execution; a later user instruction
can continue in the same session. The phase detail records a privacy-safe route
and reason code for diagnosis without logging the user prompt.

The planner uses one stable system prompt. A small host-authored
`<planner-turn>` block names the explicit route and preserves the planner
prefix cache after the one-time prompt upgrade. The plan should separate
verified from candidate touchpoints and include non-goals, risks, acceptance
criteria, and command-level verification when evidence supports them. The
planner must call `submit_plan`; a prose reply without a submitted plan is a
protocol error. If a planner still does not finalize after its bounded
research and finalization round, the turn fails closed on every route and the
executor is not started. The incomplete planner turn is rolled back instead of
leaving an unusable continuation tail.

Ordinary clean finals end the turn. Goal, review, and guardian flows keep
their own continuation constraints. In Goal mode, if an active todo produces
no new completion, unique read, command, or mutation past the stall threshold,
the host forces a smaller step, different tool/approach, focused delegation,
or a real blocker report, then execution continues. Exact repeats do not count
as progress; new host-observed work renews the lease. Two-level task lists keep
the same single-current contract: the active level-1 sub-step is the one
`in_progress` item while its level-0 phase stays `pending`; sub-steps are worked
and signed off in order, and once every sub-step has completed the phase itself
becomes `in_progress` for its own final sign-off.

Existing `[agent].max_steps` and `planner_max_steps` keys remain syntactically
accepted during upgrades, but their values are ignored and removed with a
one-time notice. This prevents a stale hidden limit from truncating automatic
progress or inherited subagent work. Use the one-off CLI `--max-steps` flag when
an explicit run budget is needed; unattended bots retain `[bot].max_steps`,
where `0` means continuous execution and a positive value is explicit.

**An ordinary chat task has no limit of any kind by default** — not rounds, not
tokens, not time, not money. It runs until the model finishes, an adaptive
guard decides it stopped making progress, or you stop it.

An optional spend gate is available when you want one. It bounds a whole task
(every "continue" included, until you start unrelated work), and on crossing it
the task produces one tool-free summary and pauses; the work is saved and the
next message continues it.

```toml
[agent]
task_cost_budget = 5.0            # in the model's pricing currency
task_time_budget_minutes = 60     # wall clock across the whole task
```

Both are off unless set. In particular, `task_time_budget_minutes = 0` (and
legacy negative values) disables the time gate; only a positive value enables
it. Neither has a default, because a stop is a judgement
only you can make: no amount of money is portable across models — a budget
loose enough for a cheap model would land a frontier model within a couple of
answers — and a long task is as often the job you asked for as it is a runaway.

Cost applies only to a priced model. Without a price table that axis stays
inactive rather than reading the task as free; use the time axis for a free or
local model.

Rounds are deliberately not an axis. A turn that reaches a high round count
without spending much is one whose rounds are individually cheap and fast,
which is the case least worth interrupting. Use the one-off `--max-steps` flag
when you specifically want a run bounded by rounds.

Subagent skills inherit the executor model by default. Set `subagent_model` to
run them on another configured model, or use `subagent_models` to override only
specific skills such as `review` or `security_review`.

Subagents may delegate one more layer by default: the root session is depth 0,
first-layer subagents are depth 1, and the maximum `max_subagent_depth = 2`
means a depth-1 workflow can dispatch a depth-2 reviewer or implementer. Depth-2
subagents do not receive recursive agent/skill tools. Set
`agent.max_subagent_depth = 1` to restore the old single-layer boundary. This is
intended for workflows such as Superpowers where a workflow skill may dispatch a
reviewer subagent, while still avoiding unbounded recursion and background
fanout.

Use `read_only_task` when planning needs isolated, deeper research without
granting write-capable delegation. Use `read_only_skill` when the same need is
best expressed through an existing skill. Both run ephemeral read-only
subagents with only read-only research tools plus safe foreground bash, return
only the final answer, and do not create resumable subagent transcripts.
Read-only nested delegation may be available until `max_subagent_depth` is
reached, but writer-capable `task` / `run_skill` remain unavailable inside these
read-only child registries. Every task shares one tool surface: call
`use_capability` for `read_only_skill` and other optional tools. Subsequent
writer calls still pass through Permissions/Sandbox.

Every strict read-only child is built through one shared construction
pairing — `RunReadOnlySubAgentWithSession` / `NewReadOnlyAgent` — which marks
the child permanently read-only and applies a final registry filter. The filter
removes writers, destructive MCP targets, readers from unauthorized servers,
and every host-mutating tool. User-installed and project-configured servers are
authorized immediately. Eligible readers may still start on demand. These are
the strict read-only entrances:

| Entrance | Purpose |
| --- | --- |
| `read_only_task` | Isolated read-only research child from the main session |
| `parallel_tasks` (read-only) | Concurrent read-only research children |
| `fleet` with `read_only: true` | Parallel profile-aware batch (forced read-only per item) |
| `read_only_skill` | The same isolation driving an existing skill |
| `reasonix review` (CLI) | Read-only review of a diff or branch |
| Desktop preview/review subagents | Read-only desktop analysis surfaces |

In persisted sessions, `parallel_tasks` and `fleet` return a bounded preview
plus one `Subagent reference` per completed child instead of concatenating every
full answer into a truncation-prone tool result. The parent can call
`read_subagent_result` with that reference and page by `offset_bytes`; results
are scoped to the current conversation lineage and workspace. Headless runs
without a persisted parent session remain ephemeral and receive fair bounded
previews, but cannot mint durable references.

Persisted child results include `status` (`completed`, `partial`, `failed`, or
`cancelled`) and `retryable`. Partial or retryable failed runs retain a visible
answer and reference so the parent can inspect them with `read_subagent_result`
or continue the same `task`/`run_skill` transcript with `continue_from`.

The interactive two-model Planner uses a dedicated construction path
(`NewPlannerAgent`): it still blocks bash, file writers, and ordinary writers,
but may call authorized, non-destructive MCP through the fixed
`use_capability` proxy without requiring `readOnlyHint`. Direct `mcp__*`
schemas never enter the Planner tool list, so MCP install/connect churn does
not change the Planner cache prefix after the one-time schema upgrade. Missing
`readOnlyHint` no longer blocks the Planner; tools with `destructiveHint` are
zero-exec and should be written into the plan for the Executor.
In Balanced two-model sessions the Executor has its own frontend for the same
stable proxy, so an `auto_start=false` or destructive capability discovered by
the Planner remains callable by capability ID after handoff. Planner and
Executor ledgers/audits stay isolated and only the Host connection is shared.

Ordinary `task` / `fleet` sub-agents also get the same fixed proxy (session-
shared Host and connections, per-agent frontend/ledger) and may call installed
or project-configured MCP without `readOnlyHint`. Those calls use the trusted
MCP permission path (live authorization plus explicit deny only); writer and
destructive calls are still serialized, recorded as mutations, and subject to
closed-loop evidence/lease guards rather than Planner handoff. Strict
`read_only_task` / `read_only_skill` / review sub-agents share the stable proxy
schema and connection reuse but keep the strict execution gate
(`authorized && readOnlyHint && !destructiveHint`). Profile `allowed-tools`
MCP names convert to capability-id allowlists on the proxy; children never
inherit dynamic `mcp__*` schemas.

Inside a strict child, `use_capability` re-checks the resolved target before
commit/permission/hooks/execution. An unconnected eligible MCP reader may start
on demand from the current schema cache. Before `tools/call`, cached
`readOnlyHint`/`destructiveHint` facts are checked against the live
initialize/tools-list result; a reader-to-writer change or destructive promotion
means zero executions and a normal retry through the current boundary. A
schema-only change refreshes the cache for the next session without interrupting
the authorized call. Runtime enablement, authorization, and the complete
connection identity are checked again immediately before dispatch, so a
same-name client from another project/tab cannot be reused accidentally. An
unauthorized server cannot raise privileges there. This strict-child boundary
is narrower than the dedicated Planner: the Planner accepts authorized opaque
non-destructive MCP, while a strict child requires an explicit reader hint and
never exposes writers at all.

Reasonix uses **fact-driven execution**. Ordinary requests always enter the
executor. There is no automatic task mode. The one session role is the quality floor: standard (default) or delivery; facts can still raise it. Planner,
Goal, permission, sandbox, and the task contract are independent states.

Standard and Delivery do not perform general hidden final-readiness retries.
Delivery returns readiness gaps as recoverable results and exposes the existing
`Continue checks` action; the user must activate it before another recovery turn
starts. Standard keeps verification, review and sign-off gaps as completion
attention. Separately, a Standard execution turn that successfully writes one
current `in_progress` todo may continue inside the same foreground `Agent.Run`
when the trusted host knows the user asked for execution. This repair is excluded
from Plan, Goal, Delivery, read-only, recovery, cancellation, and queued-user-work
boundaries. It sends one fixed continuation prompt, permits a second only after a
new host receipt, and never exceeds two prompts. Goal and approved Plan retain
their own state-machine continuation. Historical canonical todos remain visible
but idle ones render as Ready to continue rather than In progress; their Continue
action targets the exact visible session. Provider-level stream/truncation
recovery remains independent of final-readiness recovery.

Every task shares the same provider-visible core tool surface: direct
read/bash/edit/write, background-shell lifecycle tools, `ask`/`compress` when
registered, and the stable `use_capability` proxy for optional tools (search,
MCP, skills, subagents, docs, web_fetch, and so on). Calling `use_capability`
never expands the top-level provider schema, so the prompt-cache tool prefix
stays stable across every task. The Harness minimal preset is not a task
complexity mode.

The model decides whether to investigate, write todos, or spawn a sub-agent.
The host then builds verification obligations from the actual tool call, the
real target path, and the execution receipt:

- A read-only call creates no obligation.
- A local docs, i18n, fixture, or style edit is advisory targeted verification.
- A single production-file edit is recoverable targeted verification plus
  diff review.
- Multi-file or unclear local writes require a todo and criteria first.
- Schema, migration, public-interface, auth, or destructive work becomes
  strict verification, review, and sign-off after the write is observed.
- Goal items and approved Plan criteria are always strict.
- Prompt words such as OAuth or token never create action risk by themselves.

Meta tools such as `task`, `run_skill`, and `review` are not counted as mutations
by themselves — only real child writes are. Read-only analysis remains available
without forcing a write.

For interactive frontends, Plan Mode is always an explicit user choice. Select
Plan in the desktop collaboration-mode control or cycle to Plan with
`Shift+Tab` in the CLI. Reasonix first drafts a plan, then waits for approval
before the workflow switches to implementation. Tool calls made while drafting
still use the current Permissions and Sandbox. Legacy `agent.auto_plan` and
`agent.auto_plan_classifier` values are ignored and removed from the user config
during upgrade. The visible reasoning language can be changed with
`/reasoning-language auto|zh|en` in the
session, or `reasonix config reasoning-language auto|zh|en` in a shell/script.
Pass `--local`
to the reasoning-language shell command only when you intentionally want a
project-local override.

The why behind separate sessions (keeping each model's prefix cache-stable) is in
[`SPEC.md` §3.5](./SPEC.md#35-two-model-collaboration-coordinator).
