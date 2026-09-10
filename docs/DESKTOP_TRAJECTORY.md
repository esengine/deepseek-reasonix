# Desktop trajectory

[简体中文](DESKTOP_TRAJECTORY.zh-CN.md)

The trajectory panel is the desktop's activity ledger: what a run actually did,
in order, and how long each activity took. It is a read-only projection of the
same wire events the transcript consumes, so it opens no second event path and
never changes what the model sees.

```text
tab controller ─▶ event sink ─▶ one wire handler ─┬─▶ transcript reducer
                                                  └─▶ trajectory ledger ─▶ panel
durable ledger ─▶ TurnEventsForTab ─▶ gap repair ─┘   (same handler)
```

## Panel

The right workspace gains a Trajectory tab (`rightDock.trajectory`), reachable
from the dock's add menu, the tab picker and the launcher. The table has one row
per activity: sequence, start offset, a bar on the shared time axis, the record
kind, and the payload. A bar starts where the activity started and is as long as
it ran, so overlapping bars are activities that ran in parallel — a turn covers
the rounds and calls inside it.

Kinds: `turn`, `user`, `assistant`, `model_round`, `tool`, `usage`, `approval`,
`ask`, `guardian`, `compaction`, `maintenance`, `recovery`, `phase`, `steer`,
`completion`, `notice`. Transcript content (`text`, `reasoning`) and low-signal
frames belong to the transcript and are not rows; unknown kinds are ignored so a
newer host cannot break the view.

## One activity is one row

An event that starts an activity opens a row, and the event that ends it edits
that row, so a tool dispatch and its result cannot appear twice. A partial
dispatch (arguments still streaming) is not a second call, and progress belongs
to the call it is already on. A frame that names an activity nobody opened is
dropped rather than drawn as a new activity.

## Times

Every row records where its time came from. `kernel` means the host measured it:
a replayed envelope's `createdAt`, a turn's `turnStartedAt`, or a tool's
`startedAt`/`durationMs`. `receipt` means this client stamped the event as it
arrived, corrected once per tab by the offset between the two clocks. A row with
no measured duration is a tick, never a bar of invented width, and an activity
still running is never given a completion time it has not reached.

## Coverage

The footer states what the rows cover, because the last row of a truncated
record and the last row of a short session look identical:

- `complete` — the durable record was replayed from its first event.
- `compacted` — the host folded earlier events away (the turn-event ledger
  compacts at 8 MiB / 4096 events), so the table starts where replay still can.
- `live_only` — no durable record was read; the rows are only what this
  connection saw. A host without the `TurnEventsForTab` binding reports this.
- `unread` — the coverage read has not landed yet. It is not a claim of
  completeness.

A locally capped prefix is disclosed separately: the panel keeps at most 5000
rows per tab, and the host may still hold what it dropped.

## Export

The toolbar exports the rows as JSON: coverage, axis span, and per row
`seq/at/dur/kind/tool/turnId/stamped/open/text/detail`. The file carries its own
coverage because it outlives the window that wrote it. It is written through the
shell's export picker (`PickExportFile` + `SaveExportFile`).

## Limits

- Not virtualized: the panel renders every row it holds, bounded by the local
  cap. Long sessions are the case to watch.
- No zoom, drag-select or search yet; the axis is read-only.
- Activities without a host timestamp (model rounds, usage, notices, compaction,
  phases) are positioned by receipt time, so their spans carry delivery latency.
  Turn and tool spans do not.

## Verification

`pnpm test:trajectory` (fold semantics, coverage states, export payload),
`pnpm typecheck`, `pnpm lint:hooks`, `pnpm check:css`, `pnpm check:app-layers`,
`pnpm check:scroll-writer`, `pnpm check:bundle`, and
`node bench/trajectory-panel.mjs`, which drives a scripted turn in a real browser
and asserts that the panel opens from the dock, that a dispatch and its result
stay on one row, and that a host with no durable ledger reports `live_only`.
