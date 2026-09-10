# Design: Checkpoints & Rewind

Status: **Phase 1 + 2 implemented** — snapshot store, capture seam, the Esc-Esc /
`/rewind` CLI picker, and the desktop hover-rewind, with the full Claude Code menu:
restore code / conversation / both, fork-from-here, and summarize from / up to
here. Snapshot-based and aligned with Claude Code. An optional git-backed mode is
the remaining (lower-priority) follow-up. Tracks the most requested missing
capability from v1 — an edit safety net / undo.

This document describes rewind snapshots. For the autonomous-run rule about when
the agent should pause and ask the user, see
[`TASK_CONTRACT.md`](TASK_CONTRACT.md).

For the read-only per-turn diff and check results built on these snapshots, see
[Turn results](TURN_RESULTS.md).

## Goal

Let a user rewind a session to a previous point and restore **code**,
**conversation**, or **both** — without touching their git history. Aligned with
Claude Code's rewind (Esc-Esc / `/rewind`), driven identically from the CLI and
the desktop.

## Mechanism: file snapshots, not git

Like Claude Code (and v1's `checkpoints.ts`), checkpoints are **file snapshots**,
independent of git:

- **Zero git pollution** — never commits, stages, or touches `.git/`. Works in a
  non-git directory.
- **Tracks only previewable edit-tool changes** — `write_file` / `edit_file` / `multi_edit`.
  File moves via `move_file` follow the same workspace permission boundary, but
  are not yet represented in checkpoint previews.
  `bash` side effects are **not** tracked (no way to know what a shell command
  touched), exactly as Claude Code. Risky bash is already permission-gated.
- Full pre-edit content snapshots (simple; storage bounded by retention, below).

An optional **git-backed mode** (v1's `auto-git-rollback`) is a possible Phase 2
for users who want git-level safety; it is explicitly out of scope here.

## Anchors & capture

- **One checkpoint per user turn.** A checkpoint opens when a turn starts
  (`Controller.Send` / `runTurn`), labelled with the user prompt.
- **Pre-edit snapshot.** In `agent.(*Agent).executeOne`, before running a tool
  whose `ReadOnly()` is false and which implements `tool.Previewer`, call
  `Preview(args)` → `diff.Change{Path, Kind, OldText}` and record a snapshot of
  that file into the active checkpoint. `tool.Previewer` already exists and the
  file-writers implement it, so this is one centralized seam — no per-tool code.
  - Dedup per path per turn: only the **first** touch is snapshotted (that is the
    file's turn-start content).
  - `Kind == create` (file did not exist) → store `Content = nil` so a restore
    *deletes* it. `modify`/`delete` → store `OldText`.
  - `bash` has no `Previewer`, so it is naturally excluded — matching the
    "edit-tools only" contract.

## Data model

```go
type FileSnap struct {
    Path    string  // workspace-relative
    Content *string // nil → file did not exist at the anchor (restore deletes it)
}

type Checkpoint struct {
    Turn   int        // user-message index this anchors (0-based)
    Time   time.Time
    Prompt string     // user message text — the picker label
    Files  []FileSnap // distinct files touched during this turn, turn-start state
}
```

## Storage

- **Sidecar to the session**, under `config.SessionDir()`: `<session-id>.ckpt/`.
  It is separate from the message JSONL (`agent.Session.Save`), so the session
  format is unchanged.
- **Persists across sessions** — resuming a session re-loads its checkpoints, so
  rewind works after a restart (Claude Code parity).
- **Schema v3 layout**: each turn is a directory:
  `turns/<turn>/meta.json` plus raw `files/NNNN.before` payloads. New captures do
  not duplicate preimages in the content-addressed blob store. v1/v2 JSON and
  blobs remain readable for upgrade compatibility; transaction/undo payloads
  may still use blobs. Each v3 turn also writes a payload-free v2 compatibility
  marker (`turn-<turn>.json`). A previous Reasonix version can therefore keep
  turn numbering monotonic after a downgrade, but cannot restore the v3 file
  payload represented by that marker. The marker is also the v3 turn's liveness
  record: if an older reader truncates the marker, a later upgrade ignores the
  leftover directory instead of resurrecting the future turn.
- **Retention**: keep the newest 100 v3 turn directories by default and remove
  an expired turn as one directory. Raw v3 preimages also have a soft 1 GiB
  budget; the current or transaction-protected turn may temporarily exceed it,
  and older whole turns are removed once they are unprotected. Legacy blobs use
  the same budget value in their separate compatibility store. Both limits are
  configurable via `[checkpoints]` (`retain_turns`, `blob_quota_bytes`); an
  omitted or non-positive value keeps the default, so `retain_turns = 0` does
  not disable retention. Session cleanup removes the whole sidecar.

## Controller API (the one seam both frontends drive)

Checkpoints live on `control.Controller`, beside `SetPlanMode` / `Compact` /
`NewSession`, so the terminal TUI, the desktop webview, and the HTTP/SSE server
drive rewind identically and none re-implement it.

```go
type RewindScope int // Code | Conversation | Both

func (c *Controller) Checkpoints() []CheckpointMeta
func (c *Controller) PrepareRewind(turn int, scope RewindScope) (RewindPlan, error)
func (c *Controller) CommitRewind(planID string) (RewindResult, error)
func (c *Controller) CommitRewindInPlace(planID string) (RewindResult, error)
func (c *Controller) UndoRewind(transactionID string) (RewindResult, error)
```

- **Code**: for every checkpoint from `turn` to the latest, take the earliest
  `FileSnap` per path and restore each file to that content (delete if `nil`) —
  i.e. undo all edits made at or after `turn`. Path-escape re-checked against the
  live workspace root.
- **Conversation**: fork a `rewind` head of the same session log at the turn
  boundary; a format-1 session forks a new session file instead. The previous
  chain is never truncated. See [`SESSION_OWNERSHIP.md`](SESSION_OWNERSHIP.md).
- **Both**: fork first, then restore files. A file conflict keeps the new
  head and reports `partial=true`.
- `CommitRewind` leaves the controller where it was and returns the new head
  (or fork path) in `Branch`; `CommitRewindInPlace` moves the controller onto
  the rewound conversation. Desktop tabs and the terminal use the in-place
  form, so the same tab or screen shows the rewound transcript.
- `UndoRewind` restores the file after-images. When the controller sits on a
  rewind head that received nothing since, it returns to the parent head and
  retires the empty rewind head; a continued rewind head stays as a version.

A `Rewound` event (or reuse of a history-replace event) lets every frontend
re-render uniformly.

## CLI UX (aligned with Claude Code)

- **`Esc Esc`** with an empty composer, or **`/rewind`**, opens a picker listing
  each user turn (time + which files it changed). `chat_tui` already tracks the
  double-Esc timing.
- Select a turn → sub-menu: **`[code+conversation] [conversation] [code] [cancel]`**.
- On a conversation/both restore, the terminal replays the rewound head in
  place and prefills the selected prompt into the composer; the previous chain
  stays listed under `/branch`.

## Desktop UX (aligned with the VS Code extension)

- Each user message in the transcript gets a hover **rewind** control → menu:
  **rewind code / rewind conversation / both / fork-from-here**.
- It calls the same prepare/commit rewind API over the desktop host protocol; the controller's
  event stream pushes the restored state and React re-renders. No rewind logic in
  the frontend.
- Conversation rewind and fork-from-here keep the current tab and switch it to
  the new head; the previous chain remains under *View versions*. Only an
  isolated-worktree fork opens a new tab, because it copies the session into
  the new workspace.

## Non-goals & edge cases

- **bash / external side effects** (`rm`, `mv`, DB writes, deploys) are not
  tracked — rewind cannot undo them (Claude Code parity).
- **External edits between turns**: restore compares the current existence,
  SHA-256, and mode with Reasonix's last after-image. A mismatch is reported as
  a conflict and is not overwritten.
- **Deletions**: an edit-tool deletion is restorable (snapshot has the content); a
  `bash rm` is not.
- **Large files**: full snapshots, with a 32 MiB per-file capture limit. The
  turn-count and soft byte budgets bound retained history; a protected or
  current turn may temporarily exceed the byte budget.

## Phasing

1. **Phase 1**: snapshot store + `executeOne` capture seam + controller
   prepare/commit (code/conversation/both) + CLI picker (Esc-Esc + `/rewind`).
2. **Phase 2**: desktop hover-rewind UI; "fork from here"; "summarize from/up to
   here"; optional git-backed mode.

## Open questions

- Snapshot on `/compact` and on `NewSession` boundaries?
