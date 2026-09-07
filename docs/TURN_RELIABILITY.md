# Turn reliability and interruption recovery

Tracks #9825, #9805, #9683 and the replay half of #9566: work that already
happened must never be repeated, results that never reached the model must
never be invented, and a turn must always end in a durable terminal state.

The design extends what exists: the `turns.jsonl` lifecycle ledger, the
session events, `InterruptedTurnRecovery`, and the `executeBatch` executor.
No parallel persistence system is introduced.

## Tool run states

Every tool call ends in exactly one `ToolRunState` in the ledger and in the
stored tool message. The two are produced by one classifier so they cannot
disagree.

| Situation | State | Automatic retry |
|---|---|---|
| Cancelled or refused before the start barrier | `cancelled` / `not_started` | allowed |
| Tool returned success | `completed` | never |
| Tool returned an explicit error | `failed` | read-only or idempotent tools only |
| Started, result not confirmed (cancel, crash, unconfirmed write) | `unknown` | never |
| Unknown, but a postcondition check proves the effect | `completed` + satisfied write | never |

The `ToolStarted` event is the barrier. A call that crossed it and produced
no confirmed result is `unknown`, even when the batch was cancelled later.
`not_started` is kept for old records; new readers treat it like `cancelled`.

## Execution rules

- Read-only calls fan out in parallel; writers, `bash`, and proxies run one at
  a time in provider order.
- After a writer fails, is refused, or ends `unknown`, later writers in the
  batch are skipped with a dependency notice. Read-only diagnosis still runs.
- Cancellation classifies each remaining call by the start barrier: started
  calls become `unknown`, the rest `cancelled`.
- Arguments of interrupted calls stay in the local `ToolCallRecord`; they
  never enter the model-facing recovery block.

## Recovery handoff

The next real user turn carries a bounded `<interrupted-turn-recovery>` block
listing `completed_tools`, `failed_tools`, `cancelled_tools`,
`not_started_tools`, `outcome_unknown_tools`, write postconditions, and
whether partial assistant output was excluded. It contains names, IDs, and
effect summaries only.

An identical re-issue of an `unknown` side-effecting call is refused until
its effects are inspected; satisfied write postconditions short-circuit to
"already done". Failed calls keep their paired error result and are not
listed as unknown.

The controller stamps the ledger turn ID on the handoff and merges an
earlier handoff when a turn is interrupted twice.

## Resolving an unproven effect

A user who inspected the workspace can attest that an `unknown` call already
happened. That records who said so and when, moves the call out of the unknown
bucket, and clears the decision requirement once none remain. It is deliberately
not `completed`: the host never saw the result, so the model is still told to
verify current state and never repeat the call.

No tool result is synthesized. Attestation is refused for a call the host
already proved (completed, failed, cancelled) and while a turn is running.

The Desktop interruption card carries each call's proven state plus the local
argument receipt, so a long write can be inspected or re-issued by hand. Those
arguments are display state only and never enter the model-facing block.

## Terminal turn status

Every turn ends in `completed`, `failed`, `interrupted`, or
`recovery_required`. A cancellation escalates to `recovery_required` when any
side effect is unproven or when the turn died before producing anything
(`silentInterruption`). The Desktop reads this from the `TurnDone` event.

## Provider transcript gate

Before a request is frozen, the same normalized view the adapters send is
validated: undecodable arguments, missing or misordered results, and orphan
results are refused before they reach the wire. Normalization already
repairs truncated arguments and dangling calls; the gate turns a normalizer
regression into a local error instead of a provider 400.

## Restart recovery

Reopening a ledger whose last turn never reached a terminal event answers each
unanswered call from the durable record: a call with a persisted `tool_started`
barrier becomes `unknown`, one that only reached dispatch becomes `cancelled`.
The synthesized results carry no arguments or output, are tagged with a reopen
source so a second reopen reads the same evidence, and land the turn as
`recovery_required` when any call may have run — otherwise `interrupted`.

The controller reads that evidence when it strips the dead turn's tail, so the
handoff distinguishes "may already have committed" from "safe to plan again",
names the cause `runtime_restart`, and marks a turn that died before producing
anything as a silent interruption.

## Checkpoint identity

The first side-effecting call of a turn stamps its open checkpoint with the
turn ID, call ID, an argument digest, and the transcript digest, right after
its start barrier is durable. Later writers keep the first stamp, so a restore
names the attempt it protects rather than only the user turn.

## Status

Landed (Phase 1, state correctness):

- explicit run states with local argument receipts and the start barrier
- one classifier for ledger events and stored messages
- `unknown` engages the batch dependency barrier
- `failed_tools` in the recovery handoff; turn ID stamping
- `recovery_required` terminal escalation
- transcript gate before every provider request

Landed (Phase 2, persistence and recovery):

- reopen classifies orphaned calls by the persisted start barrier and escalates
  the terminal status when an effect is unproven
- the restart handoff inherits that evidence, its cause, and silent-interruption
  facts
- checkpoints carry the recovery identity of the first side-effecting call

Landed (Phase 3, partial — resolution and card data):

- user attestation for an unknown effect, with provenance and no fabricated
  result; refused for calls the host already proved
- structured interruption card data for Desktop, including local argument
  receipts
- ordinary tool interruption never forks a session; forking stays reserved for
  a genuine cross-process file conflict

Next:

- Phase 3 (remaining): the card UI itself, the read-only inspect action, and
  re-run (new attempt ID, original call ID never reused)
- Phase 4: postcondition checkers for `git commit` and `bash`, checkpoint GC,
  recovery metrics
