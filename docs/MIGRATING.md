# Migrating to Reasonix 1.0 (the Go rewrite)

Reasonix 1.0 is a **ground-up rewrite in Go**. It is a new codebase, not an
incremental upgrade of the `0.x` TypeScript releases. This guide explains what
changed and how to move over.

## TL;DR

| | Legacy (v1) | Reasonix 1.0+ (v2) |
|---|---|---|
| Language | TypeScript / Node | Go |
| Branch | [`v1`](https://github.com/esengine/DeepSeek-Reasonix/tree/v1) (maintenance only) | `main-v2` (default, active) |
| Versions | `0.x` (up to v0.54.x) | `1.0.0`+ |
| Install | `npm i -g reasonix@0.53.2` (pin a `0.x` version) | `npm i -g reasonix` — `latest` points at the current official `1.x` release; or a release archive / `go build` |
| Code intelligence | embedding semantic search + tree-sitter symbols | LSP-assisted code reading plus grep/read_file/glob; semantic index is not yet ported |

"v1" and "v2" are **codebase generations**, not semver: the v1 line never reached
1.0, so the Go rewrite takes the `1.x` major.

## Installing 1.0

`npm` stays the primary channel — the package wraps the prebuilt Go binary (the
same way esbuild/biome ship native binaries via npm). The binary itself is a
standalone Go executable; npm is only the installer, not a runtime dependency.

**`npm i -g reasonix` installs the current official `1.x` release.** npm's `latest` tag
moved to the Go line with `1.17.5` — the earlier "`latest` stays pinned to
`0.x`" migration guard silently downgraded `npm update -g` users once 1.x went
official (#5822), so it was retired. Public release candidates are no longer
published. The legacy `next` and `canary` tags are compatibility aliases for
the same official version; `0.x` stays installable by pinning:

```sh
npm i -g reasonix          # current official 1.x release
npm i -g reasonix@0.53.2   # pin the legacy TS build
```

Prebuilt archives (`reasonix-<os>-<arch>.tar.gz` / `.zip`) and the desktop
installer are attached to each GitHub release. These are a **separate channel**
from npm: the installer drops a standalone desktop/binary build and does not
touch a CLI you installed with `npm i -g`, so the two coexist — an npm `0.53` in
your shell alongside a `1.x` desktop app is expected, not a conflict. Or build
from source:

```sh
git clone https://github.com/esengine/DeepSeek-Reasonix   # default: main-v2 (Go)
cd DeepSeek-Reasonix && make build                        # -> bin/reasonix(.exe)
```

## Configuration

| Legacy | Reasonix 1.0 |
|---|---|
| TS config files | the project-local config / `config.toml` in Reasonix home (`~/.reasonix/` on macOS/Linux; `%AppData%\reasonix\` on Windows) from v1.8.1 — see `reasonix.example.toml` and [Configuration paths](./CONFIG_PATHS.md) |
| env / API keys | Provider config keeps `api_key_env`; saved key values live in Reasonix home `.env` (`DEEPSEEK_API_KEY`, `MIMO_API_KEY`, …) |
| project memory | `REASONIX.md` (+ auto-memory), Claude-Code-compatible |
| MCP servers | `[[plugins]]` in the project-local config, or a Claude-Code `.mcp.json` (read as-is) |

On first launch, v1.8.1+ runs a one-time, **non-destructive** import: it reads
legacy config from `~/Library/Application Support/reasonix/config.toml`,
`~/.config/reasonix/config.toml`, `~/.reasonix/reasonix.toml`, or v0.x
`~/.reasonix/config.json` (API key, base URL, language, MCP servers), migrates
legacy credentials into `<Reasonix home>/.env` when a key is missing there, and
imports past sessions from legacy session directories. Old files are left
untouched, and Reasonix prints a boot notice when it imports data. Each session lands in the
workspace it belonged to (read from its v0.x sidecar meta, summary carried over
as the title), so the desktop sidebar lists it under the right project; sessions
whose workspace no longer exists land in the global session dir. Imported
sessions resume with `--resume` or the history panel. The config import only
runs when no v1.8.1+ config exists yet — if v1.8.1+ wrote its config before your
legacy data was in place nothing is overwritten, so copy any missing values
across by hand.

If the automatic pass missed data because you opened a v1.8.1+ CLI/desktop build
before the old paths were available, run `/migrate` from an interactive session.
The command is available only in Go-based Reasonix builds that include it; if you
see `unknown command`, upgrade first. It prints progress while it checks legacy
config and credentials, scans legacy memory and session directories, imports
memory files and sessions that were not previously imported, and summarizes the
result. `/migrate` keeps the same safety rules as startup migration: it does not
overwrite an existing `config.toml` or memory file, it respects session import
markers, and it is not available in the legacy 0.x TypeScript line. If the old
v0.x sessions are in a custom Windows install/data directory, use
`/migrate --from "D:\OldReasonix"` to import sessions from that explicit source.
See
[Configuration paths](./CONFIG_PATHS.md) for the full path list and limitations.

### Desktop topic metadata

Desktop automatically migrates its four legacy `desktop-topic-*.json` indexes
to an authoritative per-scope SQLite database under the Reasonix state root.
Existing scopes keep the JSON files synchronized so a normal downgrade to the
previous Desktop can still read titles and timestamps. Fresh scopes write only
SQLite. An older Desktop cannot read that SQLite-only state directly: existing
session metadata may recover titles, but creation times and automatic-title
stages are not guaranteed. The migration never deletes the legacy files or
changes project-owned `.reasonix` assets. Running old and new Desktop versions
concurrently against the same workspace is not supported.

## Context Engine v2 upgrade

Instruction and memory upgrades are automatic and do not require a setup mode,
re-index command, or new configuration:

| Existing data | Upgrade behavior |
| --- | --- |
| `REASONIX.md`, `AGENTS.md`, `CLAUDE.md` | Loaded as standing instructions with source, directory, precedence, imports, and diagnostics. Existing file names remain valid. |
| Nested instruction files | Resolve from workspace root to the active target path; within one directory, `.local.md` wins. Deeper directories still outrank broader ones. |
| Legacy fact without `id` / `revision` | Receives a deterministic scope-aware `legacy-*` ID and starts at revision 1. Migration is idempotent. |
| Legacy fact without `metadata.scope` | Scope is inferred from the project/global directory that already owns the file. |
| Existing `MEMORY.md` | Treated as a derived index and rebuilt from active fact files; hand-carried stale entries do not become facts. |
| Existing active facts | Remain active and gain revision history only when subsequently changed. |
| Existing archive entries | Remain excluded from recall and can be recovered explicitly from Context Center or `/memory recover`. |
| Old Memory v5 transcript | Remains readable; previews recover the original user prompt from `<memory-compiler-execution>`. |
| `[agent].memory_compiler` | Retired and removed by the existing one-time config migration. |

The first current boot persists missing identity/timestamp metadata without
changing the fact body. Compatibility routing fields keep older Reasonix
clients from moving a fact into the wrong scope directory if versions share the
same state root.

After upgrading, use these diagnostics instead of editing migration state:

```text
/memory
/memory instructions
/memory recall
/memory revisions <id-or-name>
/memory archived
```

New relevant facts are recalled automatically. Only bounded, non-sensitive,
create-only project/reference facts may be saved without a confirmation;
global facts, preferences, feedback, updates, duplicates, sensitive content,
and every archive operation remain explicit user decisions. The desktop
Suggestions tab scans automatically but never saves a candidate until accepted.

See [Context Engine v2](./SESSION_MEMORY_RETRIEVAL.md) for the full precedence,
freshness, recovery, cache, privacy, and remote-workspace contract.

## What's the same

The agent core carries over: the loop, tools (read/write/edit/glob/grep/bash/…),
subagents (`task`, explore/research/review), skills, hooks, plan mode, MCP client,
and DeepSeek prefix-cache–oriented design.

## What's different

- **Code intelligence**: the Go rewrite uses LSP-assisted code reading plus
  `grep` / `read_file` / `glob` for local understanding. The legacy v1 semantic
  search + tree-sitter symbol index is not bundled in v2 yet, and CodeGraph is no
  longer shipped as an internal MCP server.
- **Plan mode** + `complete_step` (evidence-backed step sign-off).
- **MCP project identity and schema-cache URLs are credential-aware**: userinfo
  and credential query values (token, api_key, password, ...) do not enter the
  project launch identity digest or schema cache key, so credential rotation
  keeps the same project runtime/cache identity. User-installed servers do not
  compute a project identity digest. Legacy launch/tool authorization receipts
  are no longer required by configured MCP servers.
- **MCP setup is now add-and-use.** Servers added by the user (Desktop, CLI,
  user config, legacy user import, or a user-installed plugin package) are
  trusted immediately and global installs persist to `config.toml`. Repository
  project-local config / `.mcp.json` servers stay project-scoped and are trusted
  without a separate launch confirmation. Project entries override same-name
  global entries; the project-local config overrides `.mcp.json` inside the project.
  Treat opening an unfamiliar repository as opting into executable project
  configuration: review `.reasonix/settings.json`, the project-local config, and
  `.mcp.json` before starting Reasonix. If a repository causes unexpected MCP
  or Hook behavior, close that workspace and correct or remove the project-local
  entries before reopening it.
- **stdio MCP connections are persistent.** This fixes stateful servers that
  lost browser/session state when writer calls received a fresh process.
- **Plan mode and permission policy are now independent**: Plan directs the
  model to plan first. Ordinary built-in and Bash calls still use the active
  Ask/Auto/YOLO rules and Sandbox, while installed MCP and proxy-resolved MCP
  writer/destructive targets plus readers from unauthorized servers stay hard-blocked for the
  whole planning phase. Explicit execution-phase tools such as `complete_step` also
  remain unavailable until plan approval. `plan_mode_read_only_commands` is
  still parsed and round-tripped for old configs, but it no longer controls
  main Plan availability. Installed or project-configured servers contribute their
  non-destructive `readOnlyHint` tools to planner/read-only registries
  automatically. Use `read_only_task` /
  `read_only_skill` when a child must be technically restricted to read-only;
  ordinary `task` / `run_skill` calls remain writer-capable and permission-gated
  in Plan. Installed MCP tools use the server's `readOnlyHint` for ordinary
  dispatch. Tools without the hint remain writer-classified. The retired
  `default_tools_approval_mode`, `tools.<raw>.approval_mode`, and
  `approvals_reviewer` fields are ignored and removed on the next save; installing
  or explicitly authorizing a server now makes all of its tools directly usable.
- **Read-only subagent research**: use `read_only_task` for generic isolated
  research in plan mode, or `read_only_skill` when the work should follow an
  existing skill. Both expose only read-only tools and safe foreground bash, do
  not write resumable transcripts, and keep writer-capable `task` / `run_skill`
  out of those explicitly read-only child registries. Ordinary writer-capable
  delegation in Plan uses Permissions/Sandbox.
- **Web dashboard remains available; desktop is recommended**: run
  `reasonix serve` when a local browser UI is useful. For the primary visual
  experience, prefer the desktop app; CLI/TUI remains the terminal-native
  path.
- Some granular v1 tools are intentionally consolidated (e.g. file-management ops
  go through `bash`); a few v1 tools are not yet ported (tracked on Discussions).

## File encoding

Reasonix 1.0 supports reading and editing files in UTF-8, UTF-8 BOM, UTF-16
LE/BE, and GB18030 (a superset of GBK). This matches v1's behavior.

- `read_file` decodes any supported encoding to UTF-8 for the model.
- `edit_file` and `multi_edit` preserve the file's original encoding — if you
  edit a GB18030 file, it stays GB18030 on disk.
- `write_file` always writes UTF-8 (the model's output encoding).
- `grep` decodes before matching, so regex patterns work on non-UTF-8 files.

## Reporting issues

Issues and PRs are labelled by line: **`v2`** (Go, `main-v2`) and **`v3`**
(Studio, `studio`). File new reports against the line you're using.

Questions? Open a [Discussion](https://github.com/esengine/DeepSeek-Reasonix/discussions).
