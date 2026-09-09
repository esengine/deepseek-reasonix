# App session ownership

[简体中文](APP_SESSION_OWNERSHIP.zh-CN.md)

Display identity, ordered snapshots, paging and recovery are described in
[Transcript projection](TRANSCRIPT_PROJECTION.md).

Session actions capture their source when invoked. A later tab change cannot
redirect a pending send, cancel, approval, model update, or navigation completion
to the newly selected session. Layout-committed command registrations publish
authority; replacement generations and unmount revoke old continuations.
Background cancellation resolves the canonical controller target rather than a
UI tab identifier. Missing or replaced targets produce a stale outcome.

Subscription scopes revoke queued deliveries before releasing registrations.
Terminal output uses reference-counted leases so an old cleanup cannot release
a newer subscriber. AppRuntime wires these owners to AppRuntimeView. App.tsx is a small composition
entry; the view receives committed commands and presentation data without
creating a second session authority.

Remote resume rejection completes behind the tab's publication fence. Session
identity, title, route, pending prompts and runtime state are restored before
the error becomes observable. HTTP rejection, busy, listing failure, missing
target and transport reconciliation share that completion owner. Generation,
client, selection and route ownership are rechecked before restoration.

Generation replacement, retirement, reconnect, host suspension and explicit
close follow the same per-tab publication order. Network handshakes and pump
waits remain outside the fence; map snapshots are revalidated after taking it.

## Remote bootstrap lock handoff

A remote server owner can release its directory between a competing exclusive
mkdir and the contender's Stat. The acquisition owner retries this missing
observation once, through exclusive mkdir again. Only Exists or structured
SFTP v3 generic failure qualifies; permission, transport and cancellation
errors remain terminal. A second consecutive missing observation fails closed,
because the protocol cannot distinguish repeated contention from a permanent
generic failure. Observing a live lock restores the normal context-bound wait.
This does not change the separate stale-lock reclamation policy.

`go test -race ./internal/remote/bootstrap` covers the release interleaving,
bounded permanent failure, cancellation and one-launch concurrent clients.

## Verification

`pnpm test:app-lifecycle` exercises source capture, committed publication,
supersession, A-to-B-to-A navigation, canonical background cancellation,
unmount, subscription disposal, and negative memory-protocol fixtures.
`pnpm test:app-browser` replays real local/remote navigation, send/Stop,
three layouts, and Composer/Workspace DOM identity. `pnpm test:all` discovers
the remaining frontend regression suites.

`cd desktop && go test -race . -run 'TestRemoteResumeFailure|TestOpenRemoteProjectTabRejectedResumeRestoresPreviousIdentity|TestRemoteRejectedResume'`
covers error-time identity, all rejection paths, lost ownership and publication
interleavings with retirement, reconnect, host suspension and close.

## Independent memory screening

The App memory workflow builds the requested clean commit once. Three isolated
runner jobs download that same build; each starts a new Chromium process and
executes 128 full, 128 windowed, 128 safety, and 512 mixed round trips. The
aggregate requires all 2,688 trips, all checkpoints and heap snapshot metadata,
three distinct shard identities, the same workflow attempt, source/build hashes,
Node/platform/architecture, fixture configuration, and browser version. Missing,
cancelled, mismatched, or failing shards cannot produce a passing final check.

The workflow runs for frontend changes and unknown paths. Known independent
backend and documentation paths may skip this mock-frontend soak; existing
platform CI continues to cover those paths. The stable `app-memory` job checks
that any skip was explicitly selected and its prerequisite states agree.

A `SHARD_PASS` is only one complete process. Aggregate `PASS` is automated
screening, not a whole-App memory-leak proof: heap-retainer analysis and a
mainline control comparison remain separate attribution work. Reports preserve
that pending status. PR-head evidence also does not replace integration and
native checks against the current target branch.
