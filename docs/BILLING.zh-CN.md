# 计费、展示币种与费用报价

Reasonix 将三类事实分开：

1. `original`：按公开/自定义价表计算的原币估算，不是发票或实际扣款。
2. `valuations`：调用发生时记录的 `identity`，以及可用时同模型另一官方区域的
   `official_table` 估算。
3. 钱包余额：供应商接口返回的原币事实。

运行时不再下载、缓存或刷新 FX，也不换算钱包。旧 JSON 中的 `fx`、
`rateSnapshot` 仍可读取为历史估值；新报价永远不会生成它们。

```toml
[billing]
display_currency = "auto"   # auto | CNY | USD
show_balance = "all"        # all | part | no — 状态栏钱包余额显示方式

[[providers]]
billing_currency = "USD"    # 价表基准币种，不代表实际结算币种
billing_mode = "payg"       # payg | subscription_equivalent
```

旧 `[desktop].currency` 仍可读并迁移到 `[billing].display_currency`。
`auto` 在配置层保持未解析：单一有效钱包币种可以成为当前 tab/session 的运行时
提示；否则单一原币直接展示，混币则按币种分桶。语言、浏览器 locale、主机区域都不再
改变价表。

`[billing].show_balance` 决定 CLI 与桌面状态栏如何渲染钱包余额：
`all`（默认）完整显示；`part` 将百位以上的数字用一个前导 `*` 遮挡；
`no` 用 `***` 完全隐藏金额。桌面悬停详情与 serve API 始终保留完整余额。

## CostQuote

`usage.costQuote` 是所有主机表面的规范 usage 载荷：

| 字段 | 含义 |
| --- | --- |
| `original` | 原币价表估算 |
| `originalTotals[]` | 混币聚合时按 ISO 排序的原币桶 |
| `valuations.*.basis` | 新报价只有 `identity` 或 `official_table` |
| `selected` | 只有形成单一展示总额时才存在 |
| `costComplete` | usage 与价表事实完整 |
| `displayComplete` | 已形成请求的单币种展示总额 |
| `complete` | 兼容别名，始终镜像 `displayComplete` |
| `displayStatus` | `matched`、`fallback_original`、`bucketed`、`unavailable` |
| `aggregateMode` | `single_currency`、`common_valuation`、`currency_buckets` |
| `rateBand` | DeepSeek 发生时刻档位：`peak`、`off_peak`，聚合时可为 `mixed` |
| `ratedAt` | 用于选择时间费率的请求完成时刻（UTC） |

目标币种缺失但所有原币相同时，使用 `fallback_original` 展示原币；混币输出
`originalTotals`，不写伪造的 0。只有缺少 usage/价格时才是 `unavailable` 并显示 `—`。
旧标量别名（`cost`、`costUsd`、`total_cost`）仅在存在 `selected` 时双写。

## DeepSeek 峰谷计价

从北京时间 2026-08-17 00:00 起，DeepSeek 官方 OpenAI、Responses 与
Anthropic 端点上的 V4 Flash、`deepseek-v4-flash-vision-exp`（价卡与 Flash 相同）
和 V4 Pro 按请求发生时刻计价。北京时间 09:00–12:00、14:00–18:00 为高峰，区间左闭右开，
其余为低峰。由于供应商不提供逐 token 计费时刻，Reasonix 使用取得 usage 的请求完成时刻，
并继续把报价标记为估算。发给视觉 SKU 的图片按供应商 usage 计入输入 token。

配置中保存的价格仍是高峰基准价。只有 PAYG 且完整价格精确匹配官方高峰基准价时才启用
动态档位；自定义端点、自定义价格和未知模型仍使用静态费率。已经写入 session、ledger、
stats 的历史报价不会回填或重算。

## 钱包与诊断

钱包余额不换算、不跨币种求和。显式目标有对应钱包时显示该钱包；没有时显示真实币种
并加 ISO 前缀。自动模式只有一个有效钱包币种时才作为运行时费用提示；多钱包、未知币种
或请求失败不会影响费用事实。

```sh
reasonix doctor billing
reasonix doctor billing --json
```

兼容保留的 `fx` 对象固定为 `enabled=false`、无缓存；正文同时展示自动选择策略、价表
基准币种和官方目录匹配情况。
