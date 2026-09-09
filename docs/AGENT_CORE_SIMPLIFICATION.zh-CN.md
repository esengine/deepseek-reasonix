# Agent 核心精简

本文档记录 Agent 核心精简改造:精简后循环的行为契约、用于前后对比的指标、
以及每个契约项的测试位置。英文版见 `AGENT_CORE_SIMPLIFICATION.md`。

## 目标循环

```text
构造请求
  → provider stream
  → clean final:结束
  → tool call:执行并进入下一 step
  → request error:统一 retry
  → 未处理错误:明确失败
```

## 产品取舍

- 普通请求默认 executor-only;planner 改为显式启用(`planner_model`)。
- 普通 Agent 默认关闭 synthetic continuation;Goal、review、guardian、
  typed report 等显式流程保留自己的约束。
- compaction 默认单次摘要;chunked/tree-reduce 高级恢复仅限显式场景
  (手动 `/compact` 与明确标记的 recovery workflow)。
- final readiness、工具安全、取消和预算继续作为硬边界。`read_file` 分页/未付清
  覆盖只是提示：未付清页不会冻结未声明目标的工具，也不会拒绝最终回答。路径级
  写证据与主机 Stop（预算/策略停机）仍然保留。
- 旧配置/旧状态保留一版读取兼容;新运行时不再执行旧 fallback。

## 指标基线

复用现有 usage 与 e2ebench 埋点,不新增专用 fallback telemetry。每个阶段
前后对比使用:

- `reasonix run --metrics <path>`:单次运行 `RunMetrics`:token/成本、
  `usage_by_source`(executor/planner/subagent/compaction/... 的请求调用数)、
  `retries`、`compactions`、`steps`。
- `go run ./cmd/e2ebench -task <task> -json`:每任务请求数、
  `usage_by_source`、trajectory 摘要(stream retry、reasoning replay、
  empty-final retry、TTFT、按 source 的请求数)、墙钟时间、cache 命中。

| 指标 | 来源 |
|---|---|
| 每个普通 turn 的模型请求数 | `usage_by_source["executor"].Calls` / trajectory `ExecutorRequests` |
| planner 请求数 | `usage_by_source["planner"].Calls` / trajectory `PlannerRequests` |
| reviewer/evaluator/guardian 请求数 | `usage_by_source["recovery_reviewer"|"goal_evaluator"]`、guardian assessment usage |
| synthetic continuation 次数 | clean turn 的 executor 请求数超出 1 的部分;trajectory `EmptyFinalRetries` |
| stream retry 次数 | trajectory `StreamRetries` / `RunMetrics.Retries` |
| compaction 请求数与 summary spans | `RunMetrics.Compactions`、compaction telemetry 通知(`spans=`、`reqs=`) |
| 首个文本事件延迟 | trajectory `TTFTMs` |
| 总 turn 延迟 | `RunMetrics.DurationMs` / bench `WallMs` |
| tool 执行成功率 | bench 任务 solved 率 / `SolvedThenBroken` |
| 因协议错误终止次数 | trajectory retry-exhausted 结果 |

至少对比三类场景:普通问答、普通代码修改、长上下文/工具密集
(`context-pressure` 任务)。每阶段目标:普通请求一条 executor 请求链,无
隐式 planner/reviewer/evaluator 请求,clean final 无额外 continuation,
默认 compaction 不进入多段 summary,硬安全失败率不上升。

## 契约测试

新增的汇总套件:`internal/agent/agent_contract_test.go`。

| 契约项 | 测试 |
|---|---|
| clean final 恰好一次模型请求 | `TestContractCleanFinalMakesOneModelRequest` |
| tool call 执行后进入下一 step | `TestContractToolCallAdvancesToNextStep` |
| thinking 在统一 retry 中保留(冻结请求) | `TestContractThinkingSurvivesUnifiedRetry` |
| retry 耗尽后不残留长期 fallback 状态 | `TestContractNoLongLivedFallbackStateAfterRetryExhaustion` |
| clean final 不追加 synthetic continuation | `TestContractCleanFinalAddsNoSyntheticContinuation` |
| reasoning-only clean stop 直接完成 | `TestRunAcceptsReasoningOnlyFinalAnswer` |
| 完全零内容走统一 `EMPTY_RESPONSE` retry | `TestRunRetriesZeroContentWithTheSameFrozenRequest` |
| retry 耗尽返回明确协议错误 | `TestRunStopsAfterExhaustedZeroContentRetriesWithoutCommittingEmptyMessages` |
| strict provider 缺失 reasoning 只做一次冻结请求重试 | `TestRunSilentlyRecoversMissingToolCallReasoning` 及 `loop_e2e_test.go`/`retry_e2e_test.go` 的 replay 套件(#9776 修复) |
| read_file 分页提示（不冻写/收尾） | `internal/boot/effect_read_file_test.go`、`incomplete_read_test.go`、`read_pipeline_regression_test.go` |
| final readiness | `final_readiness_test.go` |
| 取消 | `cancel_test.go` |
| task/token/cost 预算 | `run_budget_test.go` |
| 工具权限/畸形参数 | `argument_validation_test.go`、gate 相关测试 |
| 普通请求不调用 planner | `TestCoordinatorOrdinaryRequestDoesNotCallPlanner`、`TestDecidePlannerRouteExplicitOnly` |
| planner 无 `submit_plan` 的散文失败 | `TestCoordinatorPlanAndExecuteRequiresSubmittedPlan` |
| planner 失败不跑 executor | `TestCoordinatorFailsClosedWhenPlannerFails` |
| 不可用的 `planner_model` 是配置错误 | `TestBuildFailsWhenPlannerModelIsUnresolvable` |
| 普通 Agent 不追加 todo continuation | `TestStandardTodoContinuationDisabledByDefault` |
| 普通 compaction 保持单次摘要 | `TestPressureCompactionDoesNotCallChunkedFold` |
| 缺失的 guardian/recovery 模型 fail-closed | `TestBuildFailsWhenGuardianModelIsUnresolvable`、`TestBuildFailsWhenRecoveryModelIsUnresolvable` |

## 门禁

每个阶段执行:

```bash
go test -count=1 ./internal/agent/... ./internal/control/... ./internal/config/... ./internal/boot/...
go test -race ./internal/agent/... ./internal/provider/...
go vet ./...
go run ./tools/repolint
```
