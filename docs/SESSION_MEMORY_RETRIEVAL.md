# Context Engine v2: Instructions, Memory, and Retrieval

Context Engine v2 gives Reasonix two durable context layers with different
authority:

- **Standing instructions** define how the agent must work.
- **Background memory** stores facts that may help later but can become stale.

Keeping those layers separate is the central design rule. A fact should not
silently become a command, and a long-lived rule should not depend on retrieval
finding it at the right moment.

## Choose the right layer

| Put this in | Use it for | Examples |
| --- | --- | --- |
| `AGENTS.md`, `REASONIX.md`, or `CLAUDE.md` | Rules that must be present on every relevant turn | required test commands, repository boundaries, review conventions |
| Project memory | Durable facts that apply only to this workspace | release branch, non-obvious service constraint, project ticket URL |
| Global memory | A fact that should be available in every workspace | a user preference explicitly chosen as global |
| Session history | Original wording, tool output, or a decision that is not yet a stable fact | an error from yesterday, an abandoned approach |

Keep instruction files short. They are part of the cache-stable prompt prefix,
so every extra paragraph is carried by every turn. Store discoverable facts as
memory instead.

A minimal project file is usually enough:

```markdown
# Build and verify

- Run `go test ./...` before reporting completion.
- Do not edit generated files under `desktop/frontend/src/generated/`.
- Keep public API changes backward compatible.
```

In the CLI, `/remember <note>` and `# <note>` directly append a note to the
project instruction document. They are shortcuts for standing guidance, not the
agent's background-fact `remember` tool.

## Instruction resolution

Reasonix recognizes `REASONIX.md`, `AGENTS.md`, and `CLAUDE.md`, plus matching
`.local.md` variants. It first loads user-global instruction files from the
Reasonix home directory. It then walks from the workspace root to the target
path; at each directory it loads the normal files followed by that directory's
`.local.md` files.

Deeper directories beat broader directories, and a local variant beats normal
files in the same directory. Later entries therefore win when rules conflict.
The current user request remains the highest-authority user instruction. Files
with identical expanded content are deduplicated, preferring the more specific
source.

An instruction file can import another file with a standalone relative line:

```markdown
@docs/agent-testing.md
```

Imports are expanded deterministically, deduplicated, limited to five levels,
and confined to the directory owned by the source instruction file. Absolute
paths, parent escapes, symlink escapes, unreadable imports, and cycles are
rejected and surfaced as diagnostics rather than silently trusted.

Use the following command to see the actual result:

```text
/memory instructions
```

It reports load precedence, scope, target directory, imports, and diagnostics.
The desktop Context Center exposes the same provenance.

## Background fact model

Each fact is a Markdown file with:

- an immutable `id`;
- a monotonic `revision`;
- `created_at` and `updated_at` timestamps;
- a human-readable name, title, and description;
- an independent `type` and `scope`;
- optional search `keywords` — aliases and translations of key terms that let
  a paraphrased or cross-language query reach the fact;
- an optional `subject_key` — a dotted key naming the question the fact
  answers (`project.package_manager`, `user.response_style`);
- the Markdown body.

`type` classifies the content:

- `user`: user identity or preferences;
- `feedback`: guidance about how to work and why;
- `project`: project goals or constraints not already evident in the repository;
- `reference`: external resources such as URLs or ticket IDs.

`scope` controls reach:

- `project` is the safe default;
- `global` must be chosen explicitly.

Type does not imply scope. Project feedback remains project-local, and a global
reference remains a reference.

A subject key is the knowledge-conflict model: one scope holds at most one
active value per subject. Saving a second fact for a held subject is rejected
with the holder's id, so "npm → pnpm" becomes a revision of one fact instead
of two contradicting facts both staying active. `/memory subjects` lists the
keys in use; facts answering the same subject count as equivalent for
overrides and recall suppression regardless of their names and titles.

When equivalent project and global facts exist, automatic recall uses the
project fact. Both remain visible in Context Center and `/memory`, with the
override explained instead of deleting or hiding either source.

A third dimension, `activation`, is orthogonal to both: `relevant` (the
default) keeps a fact retrieval-only, while `pinned` snapshots its body into a
lower-priority `session-context` section before the next real user turn. Pinning
is an
explicit user choice (`/memory pin <id-or-name>`, or asking the assistant),
and total pinned bodies are capped at 1,500 characters — enforced when
pinning, with overflow directed to REASONIX.md/AGENTS.md instructions, where
always-binding rules belong. A fact is either pinned (in `session-context`) or
relevant (recallable): never both, never neither.

For compatibility, legacy globally scoped `user` and `feedback` facts that
predate the field stay pinned until explicitly unpinned. When an equivalent
project fact exists, it suppresses pinned global guidance before the background
snapshot is built, so project-over-global precedence does not depend on a later
recall match.

## Automatic recall

Before each real user turn, Reasonix searches active facts using the raw user
message. Host-added provider context is not fed back into the query. The selected
facts are appended to that user turn as a bounded, low-authority suffix; they do
not mutate the system prompt or tool schema.

Recall is conservative:

- generic turns such as "continue" do not trigger recall;
- distinctive lexical matches are ranked with BM25 (CJK text is matched by
  character bigrams, so a hit needs a real word overlap, not scattered common
  characters);
- project facts receive a small relevance preference;
- stale facts are down-ranked, not silently deleted;
- equivalent project facts suppress global fallbacks for that recall;
- global `user` / `feedback` facts already present as stable guidance are not
  duplicated by automatic recall;
- at most four facts and 2,400 characters are included by default;
- fact storage paths are omitted, and home-directory prefixes in snippets are
  replaced with `<local-home>`.

Freshness defaults depend on fact type:

| Type | Fresh | Current | Stale after |
| --- | ---: | ---: | ---: |
| `reference` | 14 days | 45 days | 45 days |
| `project` | 30 days | 180 days | 180 days |
| `user`, `feedback` | 90 days | 365 days | 365 days |

Type is a default, not a truth about volatility — a README location can hold
for years while a release branch dies in days. An explicit `volatility`
overrides the type windows: `volatile` (7 / 30 days), `stable` (90 / 365
days), or `evergreen` (never ages). Two optional timestamps refine it further:
`expires_at` is a hard boundary — past it the fact is `expired` and excluded
from automatic recall entirely (explicit search still finds it) — and
`last_verified_at`, stamped by `/memory verify <id-or-name>` or by the
assistant re-confirming a fact, renews the freshness clock without changing
what `updated_at` means.

Freshness is a warning and ranking signal, not a truth claim. Recalled text
explicitly tells the model that it may be wrong and cannot override the current
request or standing instructions.

Inspect the last decision with:

```text
/memory recall
```

The trace includes the query, selected IDs and revisions, scores, match reasons,
freshness, budget use, omitted count, and suppression reason.

The explicit read-only `memory` tool remains available for deeper `search`,
`read`, and `list` operations. The separate read-only `instruction_sources`
capability reports the exact host paths of the standing instruction files
active for the session; the cache-stable system prompt continues to use
provider-relative labels such as `user/AGENTS.md`. Use `history` instead when
exact wording or tool output matters.

## Safe writes and confirmation

The ordinary path is zero-configuration. Reasonix may automatically create a
new memory only when all of these conditions hold:

- the owning controller has the current project store (interactive or top-level
  headless, never a sub-agent);
- the type is explicitly `project` or `reference`;
- the scope is project or omitted;
- the operation is create-only, not an update;
- the body is within the automatic-write budget;
- no credential, secret, private key, or email address is detected;
- no fact with the same name, title, or description already exists.

The grant is one-shot and the storage layer enforces create-only semantics, so a
concurrent fact cannot be overwritten after assessment.

Under Ask, everything else still requires explicit confirmation:

- global facts;
- `user` preferences and `feedback`;
- updates to an existing ID or revision;
- possible duplicates;
- sensitive or oversized content;
- every `forget` operation.

Ask keeps those confirmations. Interactive Auto treats `remember` and `forget`
as normal policy fallback: default calls proceed, while explicit `ask` and
`deny` rules remain effective. Interactive YOLO skips memory ask prompts unless
an explicit deny rule matches. Guardian and permission hooks cannot approve
them for the user. A top-level headless controller may use only the same
one-shot low-risk create path above, including headless YOLO. Sub-agents and
headless surfaces without the owning scoped controller fail closed; all other
headless memory mutations still require an interactive confirmation surface.

Direct edits made by the user in Context Center, `/remember`, restore, and
recovery commands are already explicit user actions and do not add another
approval prompt.

## Revisions, archive, and recovery

Updating a fact creates an immutable snapshot of the previous revision. A stale
`expected_revision` is rejected instead of overwriting a newer edit.

Restoring an old revision does not rewind storage in place. Reasonix copies the
chosen content into a new, higher revision, preserving a monotonic audit trail:

```text
/memory revisions <id-or-name>
/memory restore <id-or-name> <revision>
```

`forget` removes a fact from active recall and moves it to `.archive/`. Recovery
accepts only an archive entry owned by the current store, rejects symlink and
path escapes, refuses ID/name collisions, and never overwrites an active file:

```text
/memory archived
/memory recover <archive-path>
```

Recovered content also becomes a new monotonic revision. Restore and recovery
mark the background snapshot dirty; one complete replacement `session-context`
is appended before the next real user turn.

## Zero-configuration suggestions

Opening the desktop Suggestions tab automatically scans recent local user turns.
There is no setup toggle. It proposes:

- durable memory candidates from explicit preferences, constraints, and project
  conventions;
- Skill candidates from repeated workflow patterns.

Scanning uses original user content, deduplicates against facts from both scopes
and loaded instruction bodies, and never writes by itself. Every candidate shows
evidence and must be explicitly accepted. Remote workspaces fail closed:
Reasonix does not fall back to local sessions or local memory when the remote
surface cannot provide the feature.

## Management surfaces

Bare `/memory` shows every active fact from both scopes, including ID, revision,
type, scope, freshness, and storage provenance. Structured completion is
available in CLI, desktop, and remote workspaces.

| Command | Result |
| --- | --- |
| `/memory` | Combined instruction, fact, and archive summary |
| `/memory instructions` | Precedence, directories, imports, diagnostics |
| `/memory recall` | Last automatic-recall trace |
| `/memory revisions <ref>` | Active fact and immutable history |
| `/memory restore <ref> <revision>` | Restore as a new revision |
| `/memory archived` | Archived facts and paths |
| `/memory recover <path>` | Recover an owned archive as a new revision |

Context Center provides the same model visually, including conflicts and
project-over-global explanations.

## Upgrade compatibility

Context Engine v2 upgrades existing stores without requiring setup:

- legacy facts without IDs receive deterministic `legacy-*` identities;
- missing revisions start at revision 1;
- missing scope is inferred from the containing project/global directory;
- migration is idempotent and writes the new metadata only once;
- compatibility routing fields keep older clients from moving facts to the
  wrong directory when versions share a state root;
- old `MEMORY.md` indexes are treated as derived data and rebuilt from fact
  files;
- legacy Memory v5 `<memory-compiler-execution>` transcript blocks remain
  readable, while the retired `[agent].memory_compiler` setting is removed.

No vector database, embedding service, setup wizard, or re-index command is
required.

## Cache and privacy contract

- Standing instructions join the stable system prefix at session start. The
  derived index and pinned guidance live in the versioned `session-context`
  snapshot and refresh before the next real user turn when their digest changes.
- Provider-visible instruction provenance uses stable `workspace/...` and
  `user/...` labels; absolute source and store paths stay in local diagnostics.
- Provider-visible memory tool results use stable `project/<name>.md` and
  `global/<name>.md` references. Those references round-trip directly through
  read, update, revision, and archive operations, including when both scopes
  contain the same name; Context Center and local recovery diagnostics retain
  the real storage paths.
- Dynamic recall is appended only to the current user turn.
- Diagnostics never enter provider requests.
- Automatic recall omits fact storage paths and redacts home-directory prefixes
  in snippets.
- External approval notifications receive the tool name, not memory contents.
- Remote management uses the remote controller's memory catalog and never reads
  the desktop machine's local store as a fallback.

This keeps the provider-visible prefix stable while making dynamic context
observable and recoverable.
