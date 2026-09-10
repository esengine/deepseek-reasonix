# Subagent profiles

Subagent profiles are reusable, explicitly invoked agents for focused work such
as code review, investigation, or documentation. Each profile is a manual Skill
with `runAs: subagent`: Reasonix starts an isolated child agent, gives it the
profile prompt and task, and returns only its final answer to the parent.

Profiles are shared by the desktop app, interactive CLI, and headless CLI. They
use the existing Skill file format and storage rather than a separate database.

## Create a profile

Create a project profile from a prompt file:

```bash
reasonix subagent create reviewer \
  --description "Review changes for correctness and regressions" \
  --prompt-file reviewer.md \
  --tools read_file,grep,bash \
  --model deepseek-pro \
  --effort high
```

With a workspace, `create` defaults to project scope. Outside a workspace it
defaults to global scope. Pass `--scope project` or `--scope global` to make the
choice explicit. Project profiles are stored under
`.reasonix/skills/<name>/SKILL.md`; global profiles are stored under the
Reasonix home Skill directory described in
[Configuration paths](./CONFIG_PATHS.md).

The prompt may come from `--prompt`, `--prompt-file PATH`,
`--prompt-file -`, or piped stdin:

```bash
printf '%s\n' 'Review the task and report only actionable findings.' | \
  reasonix subagent create reviewer --description "Code reviewer"
```

Names may contain letters, digits, `_`, `-`, and `.`. Reasonix refuses a name
that already belongs to another project, global, custom, or built-in Skill.

## Invoke a profile

In an interactive CLI or desktop chat, use a slash command:

```text
/reviewer review the current diff
```

This is a real isolated subagent run, not prompt text inserted into the parent
agent. The parent conversation retains the task and the child's final answer,
not the child's full working context. Review and security-review children also
receive a compact parent facts pack (confirmed decisions, evidence summary, file
anchors) and default to 8 steps plus a 2048 output-token cap.

The parent model can also select a profile at call time without listing profile
names in the tool schema (prompt-cache stability):

```text
task(profile="doc-rewriter", prompt="rewrite docs/01.md", write_paths=["docs/01.md"])
fleet(tasks=[
  {profile="doc-rewriter", prompt="rewrite docs/01.md", write_paths=["docs/01.md"]},
  {profile="doc-rewriter", prompt="rewrite docs/02.md", write_paths=["docs/02.md"]}
])
```

- `profile` on `task` / `fleet` items resolves a `runAs: subagent` Skill by name
  (explicit names may call `invocation: manual` profiles).
- The profile body becomes the **full** child system prompt — no implicit
  concise default is stacked on top.
- `write_paths` declares write targets so parallel writers can share one
  workspace. File claims must be disjoint to start together. Directory claims
  may start together and only serialize when they realize the same file.
  Writer tasks that omit `write_paths` start as a whole-workspace claim
  (serializing at start). After path-bound writes only, that reservation
  shrinks to the files touched; `bash`/MCP makes it whole-workspace again. In
  `fleet`, concurrent omitted claims queue in the scheduler instead of failing
  preflight; concurrent directory claims start together. Once a whole-workspace
  writer is queued, later writers cannot bypass it.
- Session defaults: `agent.max_subagent_concurrency = 6`,
  `agent.max_parallel_writers = 3` (both configurable 1–32; writers ≤ total).

For scripts and other headless use, choose an explicit command:

```bash
# Preview with read-only tools.
reasonix subagent try reviewer "review the current diff"

# Run with the normal permission and sandbox policy.
reasonix subagent run reviewer "review and fix the current diff"

# Read the task from stdin and cap tool-call rounds.
git diff | reasonix subagent run reviewer --max-steps 20
```

Put `run`/`try` flags before the task. Both commands also accept `--model REF`
and `--dir PATH`. `try` always selects the read-only runner. `run` uses the
normal isolated runner; permission `deny` rules and sandbox restrictions still
apply. Ordinary `reasonix run` remains a plain one-shot task entry point and
does not implicitly interpret `/<profile>` syntax.

## Manage profiles

```text
reasonix subagent list [--dir PATH]
reasonix subagent create <name> --description TEXT (--prompt TEXT | --prompt-file PATH)
  [--scope project|global] [--model REF] [--effort LEVEL]
  [--tools a,b] [--color NAME] [--dir PATH]
reasonix subagent edit <name> [--description TEXT]
  [--prompt TEXT | --prompt-file PATH] [--model REF] [--effort LEVEL]
  [--tools a,b] [--color NAME] [--dir PATH]
reasonix subagent delete <name> --yes [--dir PATH]
reasonix subagent try <name> [--model REF] [--max-steps N] [--dir PATH] <task>
reasonix subagent run <name> [--model REF] [--max-steps N] [--dir PATH] <task>
```

`edit` changes only fields supplied on the command line. Use an explicit empty
value to clear an optional field:

```bash
reasonix subagent edit reviewer --model= --effort= --tools= --color=
```

An omitted or empty tool list means the profile adds no tool allowlist; the
runner's normal availability, permission, sandbox, and read-only rules still
apply. `delete` requires `--yes` so it is never an implicit destructive action.

Built-in profiles have no writable Skill file. Their `edit` command accepts
only `--model` and `--effort`, storing the same per-profile overrides used by
desktop settings. Clearing either value removes that override.

## File format and advanced profiles

The CLI and desktop profile editors produce a compact Skill file like this:

```yaml
---
name: reviewer
description: Review changes for correctness and regressions
color: orange
invocation: manual
runAs: subagent
model: deepseek-pro
effort: high
read-only: true
allowed-tools: [read_file, grep, bash]
---
You are a focused code reviewer. Inspect the requested changes and return only
actionable findings, ordered by severity.
```

`invocation: manual` prevents automatic discovery in the model's
`session-context` Skills catalog; users can still invoke the profile explicitly. `allowed-tools` is a
profile-level allowlist, not a way to bypass permissions. `read-only: true`
forces the read-only tool registry (writer tools stripped); omitted/`false`
keeps the legacy writable default.

Skills discovered under a `.claude` convention directory get Claude Code tool
names in `allowed-tools` (`Read`, `Edit`, `AskUserQuestion`, ...) translated to
Reasonix tool names, and Claude model aliases (`sonnet`, `opus`, `haiku`,
`inherit`) fall back to the session model — the same mapping plugin-provided
Claude agents already receive.

You may hand-author richer `runAs: subagent` Skills, including custom Skill
paths and extra frontmatter. They can be listed and invoked, but the profile
editors deliberately refuse to edit or delete:

- profiles outside project/global scope;
- profiles whose `invocation` is not `manual`;
- files with frontmatter the editor does not manage; or
- Skill directories containing `references/` or `scripts/`.

This prevents a simplified editor from silently discarding advanced Skill
content. Manage those profiles as Skill files instead.

## Model and effort selection

The effective model and effort are selected in this order, from highest to
lowest priority:

1. per-profile entries in `agent.subagent_models` and
   `agent.subagent_efforts`;
2. this call's `model` / `effort` arguments on `task` or `fleet`;
3. the profile's `model` and `effort` frontmatter;
4. `agent.subagent_model` and `agent.subagent_effort` defaults;
5. the configured executor/default model and its default effort.

For example:

```toml
[agent]
subagent_model = "deepseek-pro"
subagent_effort = "high"
subagent_models = { reviewer = "deepseek/deepseek-v4-pro" }
subagent_efforts = { reviewer = "max" }
```

The `--model` flag on `subagent run` or `subagent try` selects the default model
used to initialize that headless command; profile-specific configuration still
has its documented precedence.

## Desktop and troubleshooting

Profiles created in desktop settings and with `reasonix subagent create` share
the same files. Refresh or start a new session after changing profiles so an
already-running session reloads the Skill registry.

If invocation reports an unknown or disabled profile, check
`reasonix subagent list`, the current `--dir`, and `skills.disabled_skills`. If
editing reports that a profile is custom or rich, edit its `SKILL.md` directly
instead of forcing it through the profile editor. Unknown model references and
invalid effort levels are rejected when Reasonix resolves the effective model.
