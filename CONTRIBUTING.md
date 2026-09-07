# Contributing to Reasonix

Thank you for your interest in contributing to Reasonix! This guide covers
everything you need to get started.

## Prerequisites

- **Go 1.25+** — the project targets the latest stable Go release
- **Git** — for version control
- **Node.js** (optional) — only if you work on the desktop app (`desktop/`)
- **Wails CLI** (desktop only) — run `make wails-install` so the CLI matches
  `desktop/go.mod`

## Getting started

```bash
git clone https://github.com/esengine/DeepSeek-Reasonix.git
cd DeepSeek-Reasonix
go build ./cmd/reasonix    # builds the CLI binary
go test ./...              # runs the full test suite
```

## Project structure

| Directory | Purpose |
|-----------|---------|
| `cmd/reasonix` | CLI entry point |
| `internal/agent` | Agent loop, session, coordinator |
| `internal/cli` | TUI, subcommands, setup wizard |
| `internal/control` | Transport-agnostic controller |
| `internal/config` | TOML configuration loading |
| `internal/tool/builtin` | Built-in tools (bash, read_file, …) |
| `internal/provider` | Model-backend abstraction |
| `internal/provider/openai` | OpenAI-compatible provider |
| `internal/plugin` | MCP client (stdio + HTTP) |
| `internal/event` | Typed event stream |
| `internal/hook` | Shell hooks (PreToolUse, …) |
| `internal/memory` | REASONIX.md hierarchy + auto-memory |
| `internal/skill` | Skill discovery from Markdown |
| `internal/sandbox` | OS-level sandboxing |
| `internal/serve` | HTTP/SSE server frontend |
| `internal/checkpoint` | Snapshot-based rewind |
| `desktop/` | Wails-based desktop app (separate Go module) |
| `docs/` | Engineering spec, migration guide |

### Dependency direction

```
cli → {agent, plugin, config} → {tool, provider}
```

Built-in subpackages import their parent to self-register via `init()`.
Parents never import children.

## Development workflow

### Building

```bash
make build          # go build ./...
make test           # go test ./...
make vet            # go vet ./...
make fmt            # gofmt -w .
make hooks          # install git hooks (pre-push: go vet)
make cross          # cross-compile for all 6 targets
```

### Isolated development environment

A source-built binary shares no on-disk state with a stable release when launched
with `REASONIX_HOME` set. This gives each build its own self-contained directory
tree — config, credentials, sessions, cache, skills, commands, hooks, and
desktop tab state — so the two builds never interfere:

**CLI**

```bash
REASONIX_HOME=/tmp/reasonix-dev go run ./cmd/reasonix
# or after building:
#   REASONIX_HOME=/tmp/reasonix-dev ./bin/reasonix
```

**Desktop**

```bash
cd desktop && wails build
REASONIX_HOME=/tmp/reasonix-dev-isolated build/bin/reasonix-desktop
```

On Windows, use `$env:REASONIX_HOME` in PowerShell or `set REASONIX_HOME=` in
Command Prompt; the binary extension is `.exe`.

The directory is empty on first launch; the app behaves exactly like a fresh
install. Every subsequent write — config saves, credential storage, session
logs — stays under `REASONIX_HOME`. Legacy migration, OS-home convention
directory scanning, and all other fallback paths are skipped so no production
data leaks in or out.

### Cache-first review gate

Reasonix treats high prompt-cache hit rate as product behavior. Changes that
touch provider-visible system prompt construction, memory prefix, output styles,
skill index behavior, default tool surfaces, tool schemas, provider request
serialization, compaction, or MCP/tool registration need explicit cache review.

For these changes:

- Keep system prompt changes low-frequency and require explicit review.
- Fill the PR body `Cache-impact:` line with `none`, `low`, `medium`, or `high`
  plus the reason.
- Fill the PR body `Cache-guard:` line with the focused guard test/command added
  or run, or explain why an existing guard covers the change.
- Fill `System-prompt-review:` when system prompt, memory prefix, output style,
  or skill index behavior changes.
- Prefer focused guard tests near the changed surface; `scripts/cache-guard.sh`
  remains the broader release-level cache-hit check.

CI enforces this metadata for cache-sensitive paths so prompt/tool prefix churn
is called out before review.

### Running tests

```bash
go test ./...                           # all tests
go test ./internal/agent/ -v            # verbose, one package
go test ./internal/tool/builtin/ -run TestGrep  # one test
```

Desktop transcript scroll changes follow the discipline in
`desktop/AGENTS.md` (single scroll writer, deterministic race tests via
`pnpm test:transcript` in `desktop/frontend/`).

### Code style

- `gofmt` is enforced by CI — format before committing
- Follow existing patterns: wrap errors with `fmt.Errorf("...: %w", err)`
- Library code never calls `os.Exit` or prints to stdout/stderr
- Only `cli/` and `main/` decide exit codes and user-facing messages
- Exported identifiers must have doc comments

### Commit messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(glob): add ** recursive pattern support
fix: replace silent error discards with structured logging
test(event): add comprehensive unit tests for event package
docs: add CONTRIBUTING.md
ci: add golangci-lint and govulncheck
```

## Adding a new built-in tool

1. Create `internal/tool/builtin/mytool.go`
2. Implement the `tool.Tool` interface: `Name()`, `Description()`, `Schema()`, `ReadOnly()`, `Execute()`
3. Register via `func init() { tool.RegisterBuiltin(myTool{}) }`
4. Add tests in `internal/tool/builtin/builtin_test.go` or a separate `mytool_test.go`
5. The tool is automatically available — `main` blank-imports `builtin`

## Adding a new model provider

(For MCP tool servers see `internal/plugin` instead — that's a different layer.)

1. Create `internal/provider/myprovider/`
2. Implement `provider.Provider`: `Name()`, `Stream()`
3. Register via `func init() { provider.Register("mykind", New) }`
4. The provider is available from config with `kind = "mykind"`

## Adding i18n strings

1. Add the field to `internal/i18n/i18n.go` (`Messages` struct)
2. Add the value in every existing catalogue — `internal/i18n/messages_en.go`,
   `messages_zh.go`, and `messages_zh_tw.go`
3. Adding a new locale means a new `internal/i18n/messages_<tag>.go` file with a
   full `Messages` value, plus a `setLanguage`/`normalize` case in `i18n.go`,
   the `/language` option in `internal/cli/language.go` and
   `internal/control/slash.go`, and an entry in the catalog maps of
   `i18n_test.go` and `catalog_parity_test.go`. Follow the es-419 (neutral
   Latin American) conventions of `messages_es.go` as the Spanish baseline.
4. The `TestCatalogsComplete`, placeholder, and code-token parity tests fail if
   a locale misses a field or drifts from the English baseline

## Submitting changes

1. Fork the repository
2. Create a feature branch from `main-v2`
3. Make your changes with tests
4. Ensure `go test ./...` passes
5. Ensure `gofmt -l .` shows no changes
6. Submit a pull request to `main-v2`

## Reporting issues

Open an issue on GitHub with:
- Steps to reproduce
- Expected vs actual behavior
- Go version and OS
- Relevant logs or error messages

## License

By contributing, you agree that your contributions will be licensed under the
same license as the project.
