# Reasonix Plugin Packages

## Install (SHA-256)

Pin GitHub Release **v0.6.0** and verify `SHA256SUMS`. Website `install.sh` / `install.ps1` abort on mismatch.

https://github.com/LinespottingOrg/GrokBuildRemote-Agents/releases/tag/v0.6.0
https://github.com/LinespottingOrg/GrokBuildRemote-Agents/blob/main/docs/PINNED-INSTALL.md

```
96cef605d3e030ccef99d27ea6240e0d3b668dd045e6b5b9e585c9fd03c6ef23  gbr-agent-darwin-amd64
de7e065ef2cf6877b3b2cd04679a67b627f876337f529247e236204543e4062c  gbr-agent-darwin-arm64
a50a5c41993e6531a3b477eb409ccc845212bf541384dc803061c80657f86719  gbr-agent-linux-amd64
5bfd22c7110234942c4c02ff8154b836d0af45a9422c178a4f52010187d40061  gbr-agent-linux-arm64
f773b89fd31310172b756e0593e0f3b2382b0a3440af2a7d0a8b3073b0c23e27  gbr-agent-windows-amd64.exe
8fb9efcbc7e2ac91c11964944bf0f45e31bb23f4356d9dcb4b305d7cb9b0fe8c  gbr-agent-windows-arm64.exe
```

```bash
VER=v0.6.0
BASE=https://github.com/LinespottingOrg/GrokBuildRemote-Agents/releases/download/$VER
# swap darwin-arm64 for your OS/arch
curl -fsSL -o gbr-agent-darwin-arm64 "$BASE/gbr-agent-darwin-arm64"
curl -fsSL -o SHA256SUMS "$BASE/SHA256SUMS"
shasum -a 256 -c SHA256SUMS --ignore-missing
gbr-agent pair && gbr-agent run
```


Reasonix plugin packages bundle skills, hooks, MCP servers, prompts, themes,
and code extensions behind one installable unit.

## CLI Mode

Use `reasonix plugin` when installing or managing plugin packages from a
terminal. Plugin packages are installed globally under the Reasonix home
directory.

### Install From CLI

`install` accepts one source:

- A GitHub repository, such as `git:github.com/obra/superpowers` or
  `https://github.com/obra/superpowers`.
- A GitHub branch or subdirectory URL, such as
  `https://github.com/owner/repo/tree/main/path/to/plugin`.
- A local directory that contains `reasonix-plugin.json`,
  `.codex-plugin/plugin.json`, or `.claude-plugin/plugin.json`.

Preview the install plan without writing files:

```bash
reasonix plugin install git:github.com/obra/superpowers --dry-run
```

Install a plugin after reviewing the plan:

```bash
reasonix plugin install git:github.com/obra/superpowers --yes
```

Install with an explicit name or replace an installed plugin with the same name:

```bash
reasonix plugin install git:github.com/obra/superpowers --name superpowers --replace --yes
```

Use a local directory in developer mode:

```bash
reasonix plugin install /path/to/plugin --link --replace --yes
```

CLI install flags:

- `--dry-run` plans and validates the install without writing files.
- `--yes` is required for any install that writes files.
- `--replace` allows the source to replace an installed plugin with the same
  name.
- `--name <name>` or `--name=<name>` overrides the name from the plugin
  manifest for this install.
- `--link` links a local plugin directory instead of copying it into Reasonix's
  plugin storage. Moving or deleting that directory breaks the linked plugin.

Running `reasonix plugin install <source>` without `--dry-run` or `--yes`
refuses to write files and prints a reminder to rerun with one of those flags.
Install and remove commands print the structured JSON response from the same
install-source backend used by the desktop UI.

Installed plugin state is stored in:

```text
~/.reasonix/plugin-packages.json
~/.reasonix/plugins/<name>/
```

### Manage From CLI

List installed plugins:

```bash
reasonix plugin list
```

Show one plugin's metadata, root, source, and exported capability counts:

```bash
reasonix plugin show superpowers
```

`show` also prints the concrete capability inventory when available:

- **skills** include suggested `/<plugin>:<skill>` invocations and descriptions.
- **commands** include `/<plugin>:<command>` invocations, argument hints, and
  descriptions.
- **hooks** list lifecycle events, matchers, and commands or context files.
- **mcpServers** list server names, transports, and launch targets.

Check that the manifest and skill roots are readable:

```bash
reasonix plugin doctor superpowers
```

For a workspace-wide capability report (skills, hooks, MCP merge, package roots), see
[Capability diagnostics](./CAPABILITY_DIAGNOSTICS.md):

```bash
reasonix doctor capabilities --json
# Desktop: Settings → Diagnostics
# Agent:   /reasonix-guide
```

Enable or disable a plugin without uninstalling it:

```bash
reasonix plugin disable superpowers
reasonix plugin enable superpowers
```

Remove a plugin:

```bash
reasonix plugin remove superpowers --yes
```

`remove` also accepts `uninstall` as an alias. It requires `--yes` because it
writes state and removes copied plugin content. For linked local plugins, the
external source directory is left in place.

### Use Installed Plugins From CLI

Installed plugins do not open a separate chat surface. When a plugin is enabled,
Reasonix loads its capabilities into normal interactive sessions:

- Run `/plugins` inside an interactive session to list installed plugin
  packages. Run `/plugins show <name>` to inspect a plugin's exported skills,
  hooks, MCP servers, and usage hints without leaving the chat.
- **Skills** appear in `/skills`. Invoke a plugin skill with
  `/<plugin>:<skill> [args]`, or ask
  naturally and let the agent choose a matching skill by description.
- **Hooks** run automatically at their configured lifecycle events, such as
  `SessionStart`, `UserPromptSubmit`, `PreToolUse`, or `PostToolUse`.
- **MCP servers** join the normal MCP/tool flow. Ask for the task you want done;
  Reasonix can call the plugin's tools when they are relevant.

After installing, enabling, disabling, or updating a plugin from a separate
terminal while a session is already running, start a new `reasonix` session or
reopen `/skills` to verify the current session sees the expected skills.

## Desktop Settings

Open **Settings -> Plugins** to install and manage plugin packages without using
the CLI.

### Install Plugins

The installer has two modes:

- **Local folder**: click **Choose plugin folder** and select a plugin directory
  on disk. The selected path is shown next to the button.
- **Git repository**: enter a Git source such as
  `git:github.com/obra/superpowers`. **Install name (optional)** can override
  the plugin manifest name for this install or overwrite.

Use the action buttons after choosing the source and options:

- **Preview** validates the source and shows the planned install actions without
  writing files.
- **Install plugin** installs the selected source using the current options.
- **Refresh plugins** reloads the installed-plugin list from disk and config.

Installer options:

- **Overwrite same-name plugin** allows the current source to replace an
  installed plugin with the same name. Leave it off when duplicate-name installs
  should fail instead of replacing existing content.
- **Developer mode: link source folder** appears for **Local folder** installs.
  It links the selected directory instead of copying it into Reasonix's plugin
  storage. Use it while developing or debugging a plugin. Moving or deleting the
  selected directory will break the linked plugin.

Preview is the safest first step for a new Git source or local plugin directory.

### Manage Installed Plugins

The installed-plugin list shows each plugin package and its exported skills,
hooks, and MCP servers. Use **Refresh plugins** after editing plugin files or
changing config outside the app.

Expand a plugin row to manage it:

- Enable or disable the plugin.
- Read **How to use** for the plugin's exported skills, hooks, and MCP servers.
- **Update** pulls or refreshes an installed plugin when an update source is
  available.
- **Doctor** checks the plugin manifest and reports warnings or diagnostics.
- **Remove plugin** uninstalls the package after confirmation.

### Use Installed Plugins From Desktop

The desktop settings page uses the same runtime model as the CLI:

- Expand an installed plugin to see its **How to use** section.
- In any desktop session, type `/plugins` to list installed plugins, or
  `/plugins show <name>` to see the same usage details from the chat surface.
- Skills are shown with package-qualified direct commands such as
  `/superpowers:writing-plans`; they are also discoverable from `/skills` in a
  session.
- Plugin commands are shown and invoked with package-qualified names such as
  `/superpowers:plan`.
- Hooks and MCP servers are listed for transparency. They do not need a manual
  "run" button: enabled hooks trigger automatically, and MCP tools are available
  through ordinary tool use.
- If a currently open session does not reflect a plugin change, refresh the
  plugin list and open a new session.

## Native Manifest

Reasonix plugins can declare `reasonix-plugin.json` at the plugin root:

```json
{
  "name": "example",
  "version": "1.0.0",
  "description": "Example plugin",
  "skills": "skills",
  "hooks": {
    "SessionStart": [
      {
        "command": "hooks/session-start",
        "args": [],
        "description": "Load startup context"
      },
      {
        "command": "printf 'ready' && ./hooks/audit",
        "shell": "bash",
        "description": "Run a compound shell script"
      }
    ]
  },
  "mcpServers": {
    "helper": {
      "command": "bin/helper"
    }
  }
}
```

Relative paths are resolved inside the plugin root. Reasonix does not run
third-party install scripts during plugin installation.

Plugin hook execution is explicit:

- When `args` is present, including `"args": []`, the hook uses **exec form**.
  `command` is the executable and every argument is passed literally, without
  shell parsing or interpolation.
- When `args` is absent and `shell` is present, the hook uses **shell form**.
  The complete `command` is handed unchanged to `bash`, `powershell`/`pwsh`,
  `cmd` (Windows only), or `auto`. On Windows, `auto` prefers Git Bash and
  falls back to PowerShell.
- Existing native hooks that declare neither field keep the historical
  Reasonix shell-command behavior. `shellCommand: true` remains supported as
  the legacy spelling of shell form.

## Manifest v2 (Extensions)

Native Reasonix extensions use the exact v2 `apiVersion`:

```json
{
  "apiVersion": "reasonix.io/plugin/v2",
  "name": "example",
  "version": "1.0.0",
  "description": "Example extension",
  "requires": [],
  "provides": [
    {
      "namespace": "plugin/example",
      "kind": "interceptors",
      "id": "default",
      "version": "1.0.0"
    }
  ],
  "contributes": {
    "skills": ["skills"],
    "agents": ["agents"],
    "commands": ["commands"],
    "prompts": ["prompts"],
    "hooks": {},
    "mcpServers": {},
    "themes": ["themes/*.reasonix-theme"]
  },
  "runtime": {
    "command": "${REASONIX_PLUGIN_ROOT}/bin/example",
    "args": [],
    "env": {},
    "required": true,
    "priority": 0,
    "intercepts": ["input.receive", "tool.before"],
    "replaces": [],
    "capabilities": ["interceptors"]
  }
}
```

Parsing rules:

- Native `reasonix-plugin.json` manifests must declare the exact
  `reasonix.io/plugin/v2` value. v1 and missing versions are rejected; there
  is no v1 dual-read or automatic migration path.
- v2 is strict: any unknown field — at the root or nested under
  `contributes`/`runtime` — is an error naming the field path, so typos fail
  loudly instead of silently disabling a capability.
- v2 resource discovery is explicit. Reasonix loads only the skills, agents,
  commands, prompts, hooks, MCP servers, themes, and runtime declared by the
  native manifest. Host-specific sidecars such as a root `CLAUDE.md`,
  `hooks/hooks.json`, `.claude/settings.json`, or `.mcp.json` are not imported
  implicitly.
- Minor aliases (`reasonix.io/plugin/v2.0`, `v2.1`, …) and unknown major
  versions are rejected.
- `requires` and `provides` declare dependency constraints and the capability
  ceiling enforced against the Sidecar handshake.
- v2 may mix the supported top-level resource fields (`skills`, `hooks`,
  `mcpServers`, …)
  with `contributes`: identical paths are deduplicated; the same key with two
  different definitions is a manifest error naming the key.
- All relative paths and globs must stay inside the plugin root: traversal,
  absolute paths, escaping symlinks, and non-regular theme files are
  rejected.

New resource types:

- `prompts` are prompt templates using the same semantics and argument
  substitution as commands, invoked as `/<plugin>:<name>`. `commands` remains
  a compatible alias.
- `themes` are `.reasonix-theme` files shown read-only in Desktop Settings as
  plugin themes (IDs `plugin:<plugin>:<theme>`); they are never copied into
  the user theme library. If the plugin is disabled or uninstalled while its
  theme is active, the desktop falls back to the base style but keeps the
  ID, so reinstalling the same plugin restores the theme.

The `runtime` block declares a code extension — a sidecar process Reasonix
launches and talks to over the Extension Protocol (JSON-RPC 2.0 over stdio;
see `docs/EXTENSION_PROTOCOL.generated.md` for the method index and
`sdk/go/README.md` for the Go SDK):

- `command`/`args`/`env` are **exec form only** — the command is the
  executable (never shell-interpreted); `${REASONIX_PLUGIN_ROOT}` expands to
  the installed plugin root.
- `intercepts` lists the events the extension wants to intercept (for example
  `input.receive`, `tool.before`, `permission.decision`); `replaces` declares
  the replacement slots it may own (`system_prompt`, `context`,
  `provider_request`, `provider_response`, `compaction`, `session_policy`,
  `permission`, `frontend_events`, `tool:<name>`, `provider:<ref>`). Each
  slot has exactly one owner across all installed plugins; a collision fails
  the build with both sources named.
- `capabilities` gates whole feature families: `interceptors`, `strategies`,
  `providers`, `ui`. Anything the sidecar announces beyond the manifest is
  rejected during the handshake.
- Provider models contributed by an extension appear as
  `plugin/<plugin>/<provider>/<model>` in the model picker; the ref also
  works as `default_model` (including on first boot) and in `/model`,
  Desktop, and ACP model switches.

**Full trust.** A code extension runs outside the sandbox with the
unfiltered inherited environment. It can read the full session and
environment, bypass permissions, and operate the machine directly; a
`permission.decision` "allow" from an extension overrides a host deny.
Installing, updating, replacing, or `--link`ing a plugin with a `runtime`
block *is* the authorization — there is no second confirmation prompt, and
`--link` keeps trusting changed content automatically. The install preview,
`reasonix plugin show`, capability diagnostics, and the Desktop installer
therefore display a prominent `FULL TRUST` block with the runtime command,
interceptors, replacement slots, and provider/UI capabilities. Review that
block before installing, and only install runtimes you trust completely.
Only plugins installed through the plugin flow can start a runtime; project
configuration can never declare one.

## Codex & Claude Compatibility

Reasonix also reads Codex plugin manifests at `.codex-plugin/plugin.json` and
Claude plugin manifests at `.claude-plugin/plugin.json`. The install preview
reports `full`, `partial`, or `none` compatibility, lists mapped capabilities,
and identifies every skipped entry. A non-native package with no mapped
capabilities is blocked instead of being recorded as an unusable installation.
`full` means every declared capability in the manifest parsed and mapped to a
Reasonix construct; it does not by itself guarantee every runtime decision an
imported hook can make is honored. `PreToolUse`/`PermissionRequest` "deny" and
`PermissionRequest` "allow" are implemented, but a hook's `updatedInput` or
`PreToolUse`'s `ask`/`defer` decisions are chosen by the script's stdout at
call time, not by anything in the manifest, so they can't be flagged during
install; see the hook bullet below for what's implemented.
GitHub-hosted multi-plugin marketplaces with a
`.claude-plugin/marketplace.json` can be installed from the repository root
when their plugin entries use relative string sources such as
`./plugins/example` or `plugins/example`; preview shows one action per plugin
before anything is written. Set the optional install name to a marketplace
plugin name to select only that entry. Object sources are accepted only for a
GitHub repository URL pinned to a full commit SHA. Unpinned external strings,
npm, `strict: false`, and other advanced marketplace protocols are skipped in
a bulk install and rejected when selected by name. For packages such as
Superpowers and Claude-style skill packs, Reasonix maps the following
compatibility conventions. Native v2 manifests use only their explicit
declarations and do not apply these fallbacks:

- `skills` to Reasonix skill roots. A Claude manifest that declares no
  `skills` field falls back to the conventional `skills/` (or `.claude/skills/`)
  directory, matching Claude's own auto-discovery. Plugin skills are displayed
  and invoked canonically as `/<plugin>:<skill>`. An unambiguous `/<skill>` is
  still accepted as a hidden compatibility alias; project and user skills keep
  their short names, while same-name skills from multiple plugins remain
  independently addressable only by their qualified names. This user-facing
  namespace does not change the bare skill identifiers in the model skill index
  or the `run_skill` tool.
- `commands/` (and `.claude/commands/`) to Reasonix custom slash commands: each
  `<name>.md` prompt template is displayed and invoked canonically as
  `/<plugin>:<name>`, with frontmatter `description` / `argument-hint` and
  `$ARGUMENTS` / `$1..$N` substitution honored. An unambiguous `/<name>` remains
  accepted as a hidden compatibility alias, but it is omitted from completion,
  help, desktop menus, ACP command discovery, and the model-visible command
  list. User- and project-authored commands own their short names, and no short
  alias is created when multiple plugins export the same command name. An
  explicit custom command can also occupy the qualified name; desktop plugin
  details report that conflict. Native `reasonix-plugin.json` manifests can
  declare the same thing explicitly with a `"commands"` path list.
- `agents/*.md` to manually invoked, plugin-owned subagent profiles. Claude
  model aliases inherit the active Reasonix model; inline `tools` lists map to
  Reasonix tool names, including wildcard MCP names such as `mcp__*__search`.
  Agents use `/<plugin>:agent:<name>`, so an upstream agent and skill may share
  the same name without shadowing one another.
- `hooks/session-start-codex` to the Reasonix `SessionStart` hook when present.
- For Codex compatibility packages, a plugin-root `CLAUDE.md` file to a built-in
  `SessionStart` context hook. The file is read directly by Reasonix, without
  spawning a shell command. Claude plugin manifests ignore this file, matching
  Claude Code's plugin contract.
- `.claude/settings.json` and `hooks/hooks.json` command hooks to Reasonix hook
  events when the event names match. `matcher`, `args`, `shell`, `async`,
  `env`, and timeout are preserved. Claude's execution contract is retained:
  an `args` field (even an empty array) selects exec form and preserves every
  argument literally; omitting `args` selects shell form and passes the raw
  command to the declared Bash or PowerShell interpreter. `matcher` and the
  `tool_name` a hook script sees are
  translated between Reasonix's own tool names and Claude's (`bash` ↔
  `Bash`, `write_file` ↔ `Write`, ...), so a matcher like `"Bash"` fires
  correctly; every Reasonix subagent-spawning tool (`task`, `read_only_task`,
  `parallel_tasks`, and the dedicated `explore`/`research`/`review`/
  `security_review` wrappers) maps to Claude's single `Agent` tool, and a
  matcher can still use the legacy `Task` name. Every mapped `Agent` payload
  includes Claude's required `prompt` and `description`; Reasonix supplies a
  stable operation label when its tool call omitted the optional description.
  `tool_input` keys that
  Reasonix names differently from Claude are renamed too — `path` becomes
  `file_path` for `Read`/`Write`/`Edit`/`MultiEdit` and `notebook_path` for
  `NotebookEdit`, `name`/`arguments` become `skill`/`args` for `Skill`,
  `job_id` becomes `task_id` for the current `TaskOutput`/`TaskStop`, the
  dedicated subagent wrappers' `task` becomes `Agent`'s `prompt`, and
  `parallel_tasks` synthesizes `Agent`'s `prompt` from its sub-task prompts
  (keeping `tasks` alongside) — so a guard reading `.tool_input.file_path`
  or `.tool_input.prompt` sees the target instead of failing open on an
  empty value. Legacy `BashOutput`/`KillShell` matchers still fire while the
  emitted names and fields use current Claude vocabulary. `bash_output`
  supplies `TaskOutput`'s required non-blocking fields; `wait` also maps to
  `TaskOutput`, including `task_id` when it waits for exactly one job, and
  omits `TaskOutput`'s optional `timeout` for an unbounded wait rather than
  claiming a `0`ms budget.
  `AskUserQuestion` supplies omitted `multiSelect:false` and empty option
  descriptions, while `TodoWrite` derives an omitted `activeForm` from the
  task content. `NotebookEdit` also supplies `new_source` from Reasonix's
  accepted aliases, or an empty string for delete/empty-cell operations.
  Relative `file_path`/`notebook_path` values are resolved
  absolute against the payload `cwd`, matching Claude's file-tool contract,
  so prefix-matching guards inspect the path the tool actually accesses. A
  `Bash` `tool_response` is delivered in Claude's `{stdout, stderr,
  interrupted}` shape (Reasonix combines both streams into `stdout`; the
  failure error text becomes `stderr`), which the official security-guidance
  plugin's commit/push checks read; other tools' responses pass through as
  the raw result. Imported hooks receive Claude-compatible snake_case stdin
  payloads, including `hook_event_name`. Before process launch, the host
  expands `${CLAUDE_PLUGIN_ROOT}` and `${REASONIX_PLUGIN_ROOT}` (plus their
  unbraced `$NAME` and Windows `%NAME%` spellings), so plugin-relative paths
  do not depend on the target shell's environment-variable syntax. On Windows,
  shell-form hooks without an explicit shell use the same Git Bash-first,
  PowerShell-fallback selection as Reasonix's shell tool; when a hook points to
  a POSIX-shebang script file, the host also converts Windows paths to a Bash-
  compatible form. Explicit Bash hooks
  and legacy bare `sh -c`/`bash -c` hooks are routed through a discovered Git
  for Windows Bash even when it is not on `cmd.exe`'s `PATH`; an explicit
  interpreter path remains untouched. If no usable Bash is installed, the hook
  reports a clear prerequisite error instead of the localized `sh is not
  recognized` output. A non-standard or portable Bash configured with
  `[tools.shell] prefer = "bash"` and `path = ".../bash.exe"` is reused by
  explicit Bash hooks. `reasonix plugin doctor <name>` and
  `reasonix doctor capabilities` report a missing required shell before the
  first hook invocation. Captured legacy-code-page output is normalized to
  UTF-8 before it reaches the UI. A `PreToolUse` or
  `UserPromptSubmit` hook can still deny via exit code 2 or its JSON deny
  shape on exit 0 (`hookSpecificOutput.permissionDecision` for `PreToolUse`,
  top-level `decision:"block"` for `UserPromptSubmit`); an imported
  `PermissionRequest` hook additionally answers the permission dialog itself
  (deny or auto-allow, rather than only notifying) via exit code 2 or
  `hookSpecificOutput.decision.behavior`, matching Claude's own contract.
  `updatedInput` is not yet applied to the tool call, and a hook's `if`
  condition or `asyncRewake` field is not evaluated. A package reports partial
  compatibility with a structured warning when it declares either field, a
  `Stop`/`SubagentStop` hook (which cannot block the turn in Reasonix), or a
  matcher that covers one of three inputs Reasonix cannot losslessly express:
  `WebFetch.prompt`, `NotebookEdit.cell_id` for a Reasonix `cell_number` call,
  or `TaskOutput.task_id` when Reasonix `wait` covers multiple/all jobs. Each
  structural gap is reported once per hooks file, so a wildcard-matcher
  plugin sees one warning per gap instead of one per hook.
- A plugin-root `.mcp.json` to installed MCP entries. Claude `local` maps to
  stdio, non-ASCII display names receive stable internal IDs, and duplicate
  declarations are deduplicated. Imported servers default to
  `auto_start=false`; users connect them on demand so startup does not change
  the provider-visible tool schema.

Unsupported Claude hook item types are skipped with a warning. Reasonix does not
run third-party install scripts.

Plugin hooks receive these environment variables:

- `REASONIX_PLUGIN_ROOT`
- `REASONIX_PLUGIN_NAME`
- `REASONIX_PLUGIN_VERSION`
- `REASONIX_HOME`
- `REASONIX_WORKSPACE_ROOT`
- `CLAUDE_PROJECT_DIR`
- `CLAUDE_PLUGIN_ROOT`

## Desktop Backend Methods

Desktop exposes plugin package operations through Wails methods:

- `Plugins`
- `PlanPluginInstall`
- `InstallPlugin`
- `RemovePlugin`
- `SetPluginEnabled`
- `UpdatePlugin`
- `PluginDoctor`


## Build Remote Agent (phone pairing)

Pair a phone running [Build Remote Agent](https://grokbuildremote.com/) to this Reasonix desktop session. Protocol `gbr/1`. Phone is spectator + veto, not orchestrator.

Independent product by Linespotting AB. Not affiliated with xAI or SpaceX.

A plugin package can contribute an MCP server (see `.mcp.json` import above). Point that server at **gbr-mcp** stdio after `gbr-agent` is running. Do not invent a fourth pair protocol. Attach only loopback Bot API `http://127.0.0.1:8788` or MCP stdio `gbr-mcp`. Never put mailbox keys in plugin JSON.


Example plugin-root `.mcp.json` (stdio; `auto_start` still defaults false until you connect it):

```json
{
  "mcpServers": {
    "gbr": {
      "command": "node",
      "args": ["GrokBuildRemote-Agents/mcp/gbr-mcp/bin/gbr-mcp.js"]
    }
  }
}
```

Install from a local plugin directory that contains `reasonix-plugin.json` plus that `.mcp.json`:

```bash
reasonix plugin install /path/to/gbr-plugin --link --replace --yes
```

Phone: Build Remote Agent → scan the QR from `gbr-agent pair` (or type the 8-char code). Unpair in Settings before changing PCs.

## What the phone sees

**Terminal windows** on this PC (machine-wide mailbox). Not headless OpenCode / CodeNomad sidecar / Electron. `:8788` in a sidecar is Bot API JSON, not a transcript.

https://github.com/LinespottingOrg/GrokBuildRemote-Agents/blob/main/docs/WHAT-THE-PHONE-SEES.md
https://grokbuildremote.com/integrations.html
