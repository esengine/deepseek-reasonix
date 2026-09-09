# Proposal: Automatic reconciliation of session recovery branches

**Status:** proposal / discussion
**Related issues:** #9468 (history identity check swallows the reload fallback),
#8823 (UI loads stale main session instead of the recovery canonical branch)

## Problem

When a Reasonix session hits a **snapshot conflict** it forks the unsaved
changes into a `*-recovery-<hash>` branch (`RecoveryBranchDefaultName` in
`internal/agent/save.go`, `forked_recovery_branch` recorded in
`<session>.conflicts.jsonl`). Long-running sessions that are touched by a
stale runtime can fork **repeatedly** over time, leaving many recovery
branches whose `meta.revision` / canonical identity diverge from the main
session.

The accumulation itself drives two user-visible failures:

1. **Identity drift (frontend)** — the tab holds an old
   `sessionRevision`/`sessionDigest`; the backend now resolves to a forked
   (different) revision. `useController.ts loadOlderHistory` ran fingerprint
   rejection before the backend `result.kind === "reload"` fallback, so
   scrolling to older history dead-ended at "earlier conversation could not be
   loaded" with no recovery. (Fixed for the reload case in #9468.)
2. **Stale canonical selection** — while the fork is unresolved
   (`recovery_state` empty, unresolved count > 0), the desktop may keep loading
   the stale main transcript instead of the fuller recovery canonical branch
   (see #8823).

This proposal addresses the **upstream root cause**: recovery branches are
created but never **systematically reconciled**, so they accumulate
indefinitely and keep the workspace in a perpetual divergent state.

## Goals

- Automatically **resolve and consolidate** recovery branches once they are no
  longer "live" (the runtime that forked them is gone) without data loss.
- Establish a **canonical winner** and converge the tab to it, removing the
  stale-main confusion (#8823).
- Bound the number of retained branches so identity drift cannot accrue unboundedly.
- Remain conservative: never delete content, always keep a rollback artifact.

## Non-goals

- Redesigning the `*recovery*` fork mechanism itself (that stays).
- Changing how `snapshot` conflicts are detected.
- Cloud/remote reconciliation.

## Design sketch

### 1. Canonical resolution
For a topic with pending recovery branches, define the **canonical transcript**
as the branch (including the main file) that is newest **and** most complete
(max `message_count`, then max `revision`). Reuse the identity/fingerprint
helpers already used by the history slice (`agent.SessionContentIdentity`,
`ValidateSessionDisplayIndex`, `CanonicalSessionPath`).

### 2. Reconciliation entry points
- On desktop **startup** (after session catalog is listed) and on a **config**
  write (the fork is last written), sweep topics with
  `recovery_state == ""` and `recovery_branch_count > 0`.
- Idle throttle: only reconcile one topic per tick to keep startup cheap.

### 3. Steps per topic
1. List `basename-recovery-*` sidecars + the main `.jsonl`.
2. Pick the canonical winner (per §1).
3. If a recovery branch wins over the main file, promote it: atomically swap the
   main transcript, republish `display-index.json` / `.meta` with the winner's
   revision/digest, and record the move in `conflicts.jsonl` (outcome
   `resolved_canonical_win`).
4. Move every **loser** branch and intermediate fork sidecars to a
   per-topic trash (`.trash/recovery-reconciled-<ts>/`) rather than deleting,
   so nothing is lost.
5. Persist `recovery_state="resolved"`, clear `recovery_unresolved_count`, and
   bump a per-session marker mtime so open tabs detect the identity change and
   reload (leveraging #9468's reload path).

### 4. Discovery of a true canonical across the runtime boundary
The stale-runtime case forked after a crash; on next launch there is no live
controller to reconcile against the disk. Use a **cold** reconciliation path
(analogous to `coldHistorySlice`) that streams the transcript and compares
messages/revisions, so a crashed runtime's recovery branch can still be folded
in during startup without needing the process that created it.

## Acceptance criteria

- After a forced crash that leaves `forked_recovery_branch`, on next desktop
  launch the topic converges to a single canonical transcript; the sidebar and
  transcript show the full (recovery-won) content, not the stale main.
- Scrolling older history on such sessions succeeds (no "earlier conversation
  could not be loaded"), because the tab now shares the canonical identity.
- Loser branches are retained under `.trash` (rollback), never hard-deleted.
- Long-lived topics stop accumulating branches across restarts.

## Risks & mitigations

- **Wrong winner** → always keep losers in `.trash`; selection prefers max
  message_count so truncation is never chosen over a fuller branch.
- **Concurrent runtime** → reconciliation runs under the existing per-session
  lease; skip topics whose lease is held.
- **Perf** → one topic per tick, cold path streams (constant memory), mirroring
  `ScanSessionDisplayIndex`.

## 总结

本提案解决 recovery 分支长期累积导致的身份漂移（stale-main 加载 + 翻历史
失败）的上游根因：在桌面版启动/配置写入时自动对每 topic 做 canonical 合并，
失败分支进 `.trash` 兜底、赢者原子晋升并刷新索引与 tab 身份；配合 #9468 的
reload 修复，让超长分叉会话最终收敛到一致身份、翻历史不再"加载失败"。
