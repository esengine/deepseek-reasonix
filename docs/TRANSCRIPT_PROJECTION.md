# Transcript projection

[简体中文](TRANSCRIPT_PROJECTION.zh-CN.md)

Local Desktop and current Serve sessions share `internal/transcript`. The
controller appends each durable envelope, commits its display projection, then
publishes the event outside the commit lock. Planner output, executor output,
prompts and terminal events use this boundary. Provider history remains the
input to model requests; display records never enter those requests.

## Identity and recovery

The controller reserves a user message ID before planning. Optimistic user
items reconcile by submission ID and keep their mounted key. A sampling attempt
reserves its assistant message ID before its first delta; successful persistence
uses that ID, and discard removes only that attempt's owned records. Tool cards
use tool call IDs. Equal bodies are allowed and are not duplicate identities.

Terminal projection checkpoints live in the session's
`.transcript-projection.json` sidecar. The checkpoint records the provider prefix
count and digest, session head/rewrite identity, rows, runtime, and covered event
sequence. A failed checkpoint write keeps the WAL projection acknowledgement
pending. Recovery restores a matching checkpoint and replays its retained
suffix without executing tools. It does not seed an autosaved in-flight tail
and then append that same tail again. Existing display sidecars remain readable;
only their legacy migration path may use the old user-hash/occurrence mapping.

## Snapshot protocol

Desktop exposes `TranscriptSnapshotForTab`, `TranscriptPageForTab`,
`TranscriptContentForTab`, and `TranscriptReplayForTab`. Serve exposes GET
`/transcript/snapshot`, `/transcript/page`, `/transcript/content`, and
`/transcript/replay` behind its existing authentication and host checks.

Each snapshot includes protocol version, snapshot ID, session/head/rewrite/runtime
identity, projection revision, covered-through sequence, records, active owners,
and pending runtime prompts. Page and content requests carry the same snapshot
ID. Replay carries the identity and the last committed sequence. An expired cut
returns `stale`; a replay identity or retained-range mismatch requires a new
snapshot. Tab and remote-client generations are checked after asynchronous reads.

The frontend suspends ordered ingress before requesting the snapshot. Mutable
owners' referenced prefixes resolve before rows, runtime and attempt state commit
in one reducer transaction. Only then does event coverage advance. Queued and
replayed events share one commit entry. Status polling supplies replay hints and
ancillary job counts; it cannot install a second transcript/runtime prefix.

Older pages merge by record/item identity and backend order, including an active
user retained before the newest page. Delayed content patches check both the cut
and intervening item mutations. A page cannot resurrect a discarded attempt.

## Bounds and compatibility

Pages default to 120 records and 512 KiB, with a 2 MiB response ceiling. Large
string fields use 4 KiB previews and UTF-8-safe 64 KiB content chunks. Chunk reads
traverse typed fields directly instead of serializing the entire payload.
Snapshots retain at most three cuts under a 64 MiB estimated budget. The current
cut is pinned: a larger session remains readable and evicts older cuts. Settled
strings are shared, while immutable metadata and active prefixes are retained.

The client retains unresolved previews only; successful expansion releases that
record cache. Active/running tabs are pinned. Inactive caches have a three-tab,
32 MiB budget. Live gap buffers have a 1,024-event/8 MiB bound; overflow retains
the high-water mark and recovers the suffix from the ledger.

An old Serve returning an unsupported endpoint/protocol or its HTML index stays
connected through the legacy path. The UI states that synchronization is limited
and reconnects may have missing or repeated content. Capability discovery never
upgrades or restarts Serve. Modern hydration uses metadata-only ancillary reads,
so it does not also download the old full `/history` payload.

## Validation

Root-module tests cover attempt identity, checkpoint write failure, autosave
recovery, immutable cuts, nested content references, large sessions, and HTTP
session binding. Desktop tests cover planner cancellation, old-Serve discovery,
late remote responses, and metadata-only reads. The frontend snapshot client
tests exercise prefix/suffix races, optimistic keys, page ordering, tool pairs,
discard tombstones, and stale content writes through the real reducer.

Run the root and Desktop Go suites separately. Run frontend `test:typecheck`,
`test:transcript`, `test:remote`, `test:stream`, and the production build. Browser
transcript and app-memory checks, together with native Windows/WebView2 replay,
remain separate acceptance gates; unit tests or cross-compilation do not replace
native evidence.
