# Agent Core Simplification

This document tracks the agent-core simplification effort: the behavioral
contract of the simplified loop, the metrics used to compare before/after, and
where each contract item is tested. The Chinese version lives in
`AGENT_CORE_SIMPLIFICATION.zh-CN.md`.

## Target loop

```text
build request
  -> provider stream
  -> clean final: done
  -> tool call: execute, next step
  -> request error: unified retry
  -> unhandled error: explicit failure
```

## Product decisions

- Normal requests are executor-only; the planner is opt-in (`planner_model`).
- Normal agents ship with synthetic continuation disabled; Goal, review,
  guardian, and typed-report flows keep their own constraints.
- Compaction defaults to a single summary; chunked/tree-reduce recovery is
  explicit only (manual `/compact` and marked recovery workflows).
- Final readiness, tool safety, cancellation, and budgets stay as hard
  boundaries. `read_file` pagination / unpaid coverage is a notice; unpaid
  pages do not freeze undeclared tools or refuse a final answer. Path-scoped
  write evidence and host Stop (budget/policy stall) remain.
- Old configs and old session state stay readable for one release; the new
  runtime never executes the old fallbacks.

## Metrics baseline

Reuse the existing usage and e2ebench instrumentation; no new fallback
telemetry is added. Capture before/after per phase with:

- `reasonix run --metrics <path>` — per-run `RunMetrics`: token/cost totals,
  `usage_by_source` (executor/planner/subagent/compaction/... request calls),
  `retries`, `compactions`, `steps`.
- `go run ./cmd/e2ebench -task <task> -json` — per-task request counts,
  `usage_by_source`, trajectory digest (stream retries, reasoning replays,
  empty-final retries, TTFT, requests by source), wall time, cache hit/miss.

| Metric | Source |
|---|---|
| model requests per normal turn | `usage_by_source["executor"].Calls` / trajectory `ExecutorRequests` |
| planner requests | `usage_by_source["planner"].Calls` / trajectory `PlannerRequests` |
| reviewer/evaluator/guardian requests | `usage_by_source["recovery_reviewer"|"goal_evaluator"]`, guardian assessment usage |
| synthetic continuations | executor requests per clean turn above 1; trajectory `EmptyFinalRetries` |
| stream retries | trajectory `StreamRetries` / `RunMetrics.Retries` |
| compaction requests and summary spans | `RunMetrics.Compactions`, compaction telemetry notice (`spans=`, `reqs=`) |
| first text latency | trajectory `TTFTMs` |
| turn latency | `RunMetrics.DurationMs` / bench `WallMs` |
| tool execution success | bench task solved rate / `SolvedThenBroken` |
| protocol-error terminations | trajectory retry-exhausted outcomes |

Compare at least: normal Q&A, normal code edit, long-context/tool-heavy
(`context-pressure` tasks). Goal per phase: a normal request is one executor
request chain, no implicit planner/reviewer/evaluator requests, no extra
continuation after a clean final, default compaction never enters multi-span
summaries, and hard-safety failure rates do not increase.

## Contract tests

New consolidated suite: `internal/agent/agent_contract_test.go`.

| Contract item | Test |
|---|---|
| clean final makes exactly one model request | `TestContractCleanFinalMakesOneModelRequest` |
| tool call executes, loop advances | `TestContractToolCallAdvancesToNextStep` |
| thinking survives the unified retry (frozen request) | `TestContractThinkingSurvivesUnifiedRetry` |
| no long-lived fallback state after retry exhaustion | `TestContractNoLongLivedFallbackStateAfterRetryExhaustion` |
| clean final adds no synthetic continuation | `TestContractCleanFinalAddsNoSyntheticContinuation` |
| reasoning-only clean stop completes | `TestRunAcceptsReasoningOnlyFinalAnswer` |
| zero content retries via unified `EMPTY_RESPONSE` path | `TestRunRetriesZeroContentWithTheSameFrozenRequest` |
| exhausted retries return an explicit protocol error | `TestRunStopsAfterExhaustedZeroContentRetriesWithoutCommittingEmptyMessages` |
| strict-provider missing reasoning: one frozen-request retry | `TestRunSilentlyRecoversMissingToolCallReasoning` and the replay suites in `loop_e2e_test.go`/`retry_e2e_test.go` (#9776 repair) |
| read_file pagination notice (no write/final freeze) | `internal/boot/effect_read_file_test.go`, `incomplete_read_test.go`, `read_pipeline_regression_test.go` |
| final readiness | `final_readiness_test.go` |
| cancellation | `cancel_test.go` |
| task/token/cost budgets | `run_budget_test.go` |
| tool permission/malformed args | `argument_validation_test.go`, gate tests |
| ordinary request skips planner | `TestCoordinatorOrdinaryRequestDoesNotCallPlanner`, `TestDecidePlannerRouteExplicitOnly` |
| planner prose without `submit_plan` fails | `TestCoordinatorPlanAndExecuteRequiresSubmittedPlan` |
| planner failure does not run executor | `TestCoordinatorFailsClosedWhenPlannerFails` |
| unusable `planner_model` is a config error | `TestBuildFailsWhenPlannerModelIsUnresolvable` |
| ordinary Agent adds no todo continuation | `TestStandardTodoContinuationDisabledByDefault` |
| pressure compaction stays single-summary | `TestPressureCompactionDoesNotCallChunkedFold` |
| missing guardian/recovery models fail closed | `TestBuildFailsWhenGuardianModelIsUnresolvable`, `TestBuildFailsWhenRecoveryModelIsUnresolvable` |

## Gates

Every phase runs:

```bash
go test -count=1 ./internal/agent/... ./internal/control/... ./internal/config/... ./internal/boot/...
go test -race ./internal/agent/... ./internal/provider/...
go vet ./...
go run ./tools/repolint
```
