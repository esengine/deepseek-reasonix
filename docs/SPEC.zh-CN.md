# Reasonix 工程规格

<a href="./SPEC.md">English</a>

> Reasonix 是一个 coding agent：由极薄的 harness 驱动多个模型，所有能力都由配置和插件提供。本文是工程契约，代码应遵循它；需要改变行为时，应先更新契约，再修改代码。

英文原文是规范性版本；本文按相同章节提供中文说明，代码标识符、配置键和协议名保持原样。

## 1. 设计原则

1. **配置与插件驱动。** 核心只依赖接口；具体模型和工具通过 registry 按名称解析、在配置中声明，或由插件注入，不硬编码 `switch model`。
2. **单一静态二进制。** 使用 `CGO_ENABLED=0`，一条命令完成跨平台编译，CLI 开箱即用。
3. **精简依赖。** 默认使用标准库。第三方依赖必须是纯 Go、足够轻量，且不能破坏单二进制、跨平台和分发体验；TOML parser 是当前唯一接受的基础依赖。
4. **两级扩展。** 编译期 built-in 通过 `init()` 自注册；运行时外部插件以 stdio JSON-RPC 子进程或 MCP 兼容传输接入。
5. **接口优先、registry 驱动。** `Provider` 与 `Tool` 都是接口。
6. **持续演进，不过度设计。**

所有代码、注释、面向用户的字符串、工具描述、system prompt 和英文规范以英语为主；README 同时维护英文版 `README.md` 与中文版 `README.zh-CN.md`。

## 2. 目录与依赖方向

```text
reasonix/
├── go.mod / go.sum
├── Makefile
├── README.md / README.zh-CN.md
├── reasonix.example.toml
├── docs/SPEC.md / docs/SPEC.zh-CN.md
├── cmd/reasonix/main.go
├── cmd/reasonix-plugin-example/
└── internal/
    ├── cli/
    ├── config/
    ├── provider/
    │   └── openai/
    ├── tool/
    │   └── builtin/
    ├── permission/
    ├── command/
    ├── plugin/
    ├── remote/
    │   ├── forward/
    │   ├── sftpfs/
    │   └── bootstrap/
    └── agent/
```

核心依赖方向保持无环：

```text
cli → {agent, plugin, config} → {tool, provider}
```

`provider/openai`、`tool/builtin` 等 built-in 子包导入父包完成自注册，父包不反向导入子包。Remote-SSH 采用 `cli → remote/bootstrap → remote` 的分层，`remote` 及其子包不依赖 `cli`、`agent` 或 `serve`；host key 和 secret prompt 等交互都通过 callback 暴露，供桌面端复用。

## 3. 核心抽象

### 3.1 Provider 与 registry（`internal/provider`）

```go
type Provider interface {
    Name() string
    Stream(ctx context.Context, req Request) (<-chan Chunk, error)
}

type Factory func(cfg Config) (Provider, error)

func Register(kind string, f Factory)
func New(kind string, cfg Config) (Provider, error)
```

- `openai` kind 实现 OpenAI-compatible `/chat/completions`。
- OpenAI-compatible vendor 只是 `kind = "openai"` 的不同配置实例，通过 `base_url`、`model`、`api_key_env` 区分；新增兼容模型通常只需改配置。
- 一个 provider 表示一个 vendor endpoint，可通过 `models` 暴露多个模型，并以 `default` 指定默认项。设置 `request_url` 时，OpenAI-compatible、Anthropic-compatible 和 Responses provider 都会原样使用该完整请求地址；旧 `chat_url` 只保留 OpenAI 历史兼容语义。`default_model`、`--model` 和桌面端模型选择器都经 `Config.ResolveModel` 解析，可接受 provider 名、裸模型名或 `provider/model`。
- `context_window` 是 provider 级默认值；`model_overrides.<model>.context_window` 可覆盖单个模型。
- `max_output_tokens` 是独立的本轮输出上限，不由客户端 reasoning 字节上限换算，也不参与 `compact_ratio`。`0` 是 Provider 自动值（官方 DeepSeek 384K / OpenCode 元数据），不再表示跳过本地检查；空间充足时官方 DeepSeek 仍省略字段，临界时裁剪。正数为用户显式控费上限。负数为明确省略；安全不足时压缩。`budget_tokens` 在官方 Anthropic 兼容层会被忽略。混合网关可用 `model_overrides.<model>.max_output_tokens` 覆盖单个模型。
- streaming tool-call delta 在 provider 内按 index 聚合，只向上层发出完整 `ToolCall`。

### 3.2 Tool 与 registry（`internal/tool`）

```go
type Tool interface {
    Name() string
    Description() string
    Schema() json.RawMessage
    Execute(ctx context.Context, args json.RawMessage) (string, error)
}
```

- built-in tool 通过 `tool.RegisterBuiltin` 注册到进程级集合。
- 每次运行创建独立 `*Registry`，由启用的 built-in 与插件工具组成；agent 只看到该 registry。
- tool schema 在插入 registry 时 canonicalize；内置契约见[工具合约](./TOOL_CONTRACT.zh-CN.md)，测试会校验文档与 canonical schema 不漂移。
- `Execute` 自行解析原始 JSON 参数。错误作为结果返回给模型，让模型有机会自我修正，而不是直接终止进程。

### 3.3 插件与 MCP（`internal/plugin`）

外部插件是配置中声明的 MCP server。协议统一为 JSON-RPC 2.0。Reasonix 保留产品层客户端，协议协商、请求关联、取消、分页与传输 framing 交给官方 MCP Go SDK；每个 server 的工具、Prompt 与 Resource 共用同一个并发安全会话：

- `stdio`：本地持久子进程，每行一条 JSON 消息。
- `http` / `streamable-http`：初始化后立即建立长期 GET/SSE 监听，POST 承载客户端请求；继续兼容 POST-only 与 sessionless server。`Mcp-Session-Id` 会用于后续 GET、POST 和有界关闭 DELETE。配置 header 仅发送到原 endpoint 的同源请求，跨域重定向不会携带敏感 header。未配置静态 `Authorization` header 时，用户可发起 OAuth：客户端按 Protected Resource Metadata / Authorization Server Metadata 发现端点，使用动态客户端注册、PKCE S256、loopback callback、resource indicator 与 refresh token 轮换。客户端凭据和 token 以 `0600` 权限保存在工作区之外的 Reasonix 私有 MCP 状态目录，并绑定到配置的 resource URL；URL 改变后不会复用旧 token。OAuth 发现、注册和 token 请求遵守 Reasonix 解析后的网络代理设置。删除声明时会清理该状态；若之后生效的 fallback 使用同一 OAuth resource，则保留该状态。
- `sse`：兼容旧版 2024-11-05 HTTP+SSE；持久 GET 接收 server 公布的相对 POST endpoint、JSON-RPC 响应与 server 消息。为避免静态 header 泄漏，会拒绝跨域 endpoint。

`${VAR}` 与 `${VAR:-default}` 可用于 `command`、`args`、`env`、`url` 和 `headers`，使 secret 留在环境中。生命周期为 `initialize` → `notifications/initialized` → `tools/list`，调用使用 `tools/call`。

存在工作区根目录时，初始化会声明 `roots` 能力，并用文件 URI 响应 `roots/list`。`tools/call` 会附带逐调用 `_meta.progressToken`；匹配的 `notifications/progress` 会进入现有工具进度事件链路。

每个 server 由 generation-aware 会话监督器管理：只有初始化且监听就绪的会话才会发布；已建立会话返回 404 时，并发调用只共享一次重建且最多重放一次。由于 server 可能已经执行，未知断流不会自动重放工具调用。后台终止性断流只执行有界退避重连，旧 generation 的回调不能覆盖新会话。工具、Prompt、Resource 列表都会消费全部 cursor 页面；`/mcp` 与桌面端只显示协议、监听阶段、重连次数和脱敏错误类别，不暴露 session ID。

远程工具适配为 `Tool`，命名为 `mcp__<server>__<tool>`。`annotations.readOnlyHint` 映射为 `Tool.ReadOnly()`，默认 false；只有显式声明为只读的工具才进入并行读取与默认只读权限路径。MCP prompt 暴露为 slash command，resource 可通过 `@<server>:<uri>` 引用。

### 3.4 Agent loop（`internal/agent`）

`Session` 保存 `[]Message`。`Run(ctx, input)` 的主循环为：

1. 构建包含历史消息和 tool schema 的 `Request`。
2. 调用 `provider.Stream` 并实时输出 text delta。
3. 收集完整 tool call；若没有 tool call，则本回合结束。
4. 执行 built-in 或 plugin tool，把结果加入会话后继续，直到完成或达到安全边界。

`ctx` 贯穿调用链，Ctrl-C 可以取消进行中的请求。`Agent` 与 `Coordinator` 都实现 `Runner`，因此 CLI 不需要区分单模型或双模型执行。

### 3.5 双模型协作（`Coordinator`）

当配置了 `agent.planner_model` 时，planner 与 executor 使用独立 session。未配置时保持
executor-only；已配置但模型不可用是配置错误，不会静默改走 executor：

图片理解兜底由 `agent.vision_model` 控制：空值保持现有行为，`auto` 只在当前执行器
服务商内选择视觉模型，显式 `provider/model` 可跨服务商选择。视觉执行器先生成版本化的
图片描述/OCR 摘要，摘要作为隐藏的当前用户回合内容持久化；当前模型本身支持图片时
直接发送图片，不额外执行摘要请求。

- 宿主使用原始用户文本和可信回合元数据做确定性路由，默认 executor-only；不调用
  classifier 模型，不从措辞、文件数量或关键词推断复杂度，也不从 controller 注入的
  prompt block 猜测宿主状态。独立 Planner 只响应显式先规划 / 规划再执行、显式等待批准、
  显式只规划，或显式 Goal 启动；没有 Light/Full 规划深度。阶段详情只记录不含用户原文
  的 route/reason；
- 显式 Plan Mode 由 executor 驱动，不会再启动第二个 Planner；synthetic turn、上下文
  短回复和普通请求一律直达 Executor；
- Planner 使用同一个稳定 system prompt，单轮只追加很小的 `<planner-turn>` 标明显式
  路由。计划应区分已验证与候选触点，并在证据支持时补充非目标、风险、验收标准和
  命令级验证。`submit_plan` 是唯一交付通道，没有提交计划的普通文本视为 planner
  协议错误；若 Planner 在有界调研和最终总结轮后仍未收敛，所有路由都 fail-closed，
  不会降级到 Executor；不完整的 Planner 回合会被回滚，不暴露成无法继续的手动续跑；
- 普通“先规划”在计划完成后直接交接 Executor；plan-for-approval 只用于明确要求等待
  确认的请求，由宿主强制审批边界，批准后交接 Executor；headless 场景会保存计划供后续
  回合继续；明确 plan-only 会保存计划并结束当前回合；上述两种执行边界下 Planner 失败
  都不能降级执行；这些边界可位于任务子句之后，引号内的示例不改变路由；
- executor 在另一 session 中验证候选假设，并使用完整工具执行计划；
- 两条会话互不混合，prompt prefix 都只追加增长，避免切换模型破坏 prefix cache。

### 3.6 上下文管理（内容驱动摘要）

长任务会填满模型窗口。Reasonix 保持 **cache-first、append-only** 的 canonical
transcript，仅在唯一自动阈值被跨越时安装 provider 可见的短 **checkpoint**。

- 每个 provider 声明 `context_window`（tokens）。唯一自动触发值是
  `agent.compact_ratio`（默认 **0.80**；预设 0.70 / 0.80 / 0.85；范围 0.30–0.85）。
  数值越低越早压缩，可能增加摘要成本或降低 prompt prefix 缓存复用。
  `triggerTokens = floor(context_window × compact_ratio)`。
- **阈值以下**普通请求保持 append-only，不写 projection。所有 provider 请求只使用
  持久化且有界的 tool `Content`；本地 `RawContent` 不会进入 sampling、重试、摘要或 replay。
- **达到阈值**后，单飞维护事务先持久剪枝：所有超过 8192 个 Unicode code point
  的工具结果变为 `4096 头部 + "[... tool result middle pruned ...]" + 1024 尾部`。
  若已解除压力则不调摘要模型；否则将连续旧前缀摘要，并仅原样保留最近
  **16%** 窗口，边界不拆分 assistant tool-call/tool-result 组。
- 摘要请求复用原 system、选中消息前缀和普通请求的 tools schema，只在最后追加
  user compaction instruction，以复用 provider KV Cache。输出上限为 **8192 tokens**，
  前缀规划另在其下预留窗口的 **5%**（至少 256 tokens）作为估算余量。
  pressure 最多两次成功摘要，overflow 最多一次摘要且原请求最多重试一次。
  overflow 救援可折叠当前 turn 已完成的轮次，最新两轮原样保留。
- 每次摘要回复（成功或 provider 超窗）都把真实 prompt 数回灌估算器。摘要请求
  本身被 provider 拒绝时，先按修正后的估算重新规划更小前缀（最多两次），再以
  有界转录形式发送一次（工具结果截到 2000 字符、不带 tools schema）；手动压缩
  随后可走分片路径。自动尝试失败后，同一 turn 内暂停重试，直到视图较该次尝试
  再增长窗口的 5%，因此单个 turn 的重试次数有界。
- 候选必须严格小于被替换请求。摘要 timeout/error/空输出/token cap 都不会伪造
  机械 digest；硬上限以下沿用最近的持久投影。硬上限或 overflow 下摘要无法形成时，
  改为有损的 `truncate` 投影：先抹去最旧的工具结果，再丢弃最旧的回放单元，
  留下明确标记，直到视图回到阈值以下；只有连这样也回收不够时才返回
  `ErrCompactionRequired`。
- 用户可用 `reasonix config compact-ratio [--local] [VALUE]` 查看或修改阈值。
  项目配置优先于桌面与新 CLI 会话共用的用户全局配置。UI 始终展示**实际生效**值。
- `max_output_tokens` 是独立的**本轮**输出上限，**绝不**改变 `triggerTokens` / `compact_ratio`。
  - `0` 是 Provider 自动值。本地准入使用 Provider 能力（官方 DeepSeek 384K、OpenCode Go 模型表，或 400 学到的 completion）。它不再表示“跳过本地输出检查”。
  - 官方 DeepSeek Chat/Responses 在剩余共享窗口还能放下 384K 自动预算时继续省略字段，只在临界时注入裁剪值。官方 Anthropic 兼容层因 `max_tokens` 必填，仍发送 384K 或裁剪值。
  - 官方 OpenCode Go 预设会主动发送 `min(模型上限, 物理剩余)`，使用通用 `max_tokens` / `max_output_tokens`。第三方兼容 API 在可信上下文 400 之前不假设共享窗口。
  - 正数是用户显式控费上限，仍可按物理剩余继续下调。负数表示明确省略可选 wire 字段；已知自动预算放不下时压缩，而不是覆盖用户选择。
- canonical 工具存储保持向后兼容：`Content` 是稳定的 provider 可见 ≤32KB 表示，
  `RawContent` 保存本地完整原文。只有模型显式分页调用 `use_capability` 的
  `session:tool_result` 后，完整结果页才会进入上下文；sampling、流重试、摘要与 projection
  replay 均使用同一份有界 `Content`。prune projection 不改写两个 canonical 字段。
- 自动维护只在 `ContextManager.Prepare` 中规划一次，输入为当前 projection 加上
  append-only canonical tail；canonical 永不改写。后续阈值合并
  **上一摘要 + 新增历史** 为单条 digest（无 multi-span、无应用层重试）。
  失败以 generation 为边界记录 `blocked`/`failed`，同 generation 不自动再付费；
  手动 `compress` 可重试。
- 旧多阈值键（`soft_compact_ratio`、`tool_result_snip_ratio`、
  `compact_force_ratio`、`cold_resume_prune`、`context_editing`）在普通启动时删除，
  运行时忽略。不再使用 provider 原生 tool clearing；所有 provider 走本地 summary
  checkpoint。
- `keep` / `recent_keep` 仍可读取并 round-trip，但已弃用且不参与压缩。旧 user、失败
  工具结果和 `[[keep]]` 都进入摘要前缀。重启只恢复既有 checkpoint。
- 完整历史保留在会话 transcript 中；`history` tool 提供 BM25 检索。新 checkpoint
  不再创建 prune archive。

`history` tool 支持对 session 与归档进行 BM25 搜索；`memory` tool 用于检索自动记忆，
`remember` 与 `forget` 负责写入和归档。只读 `instruction_sources` capability 按需返回当前
会话生效的常驻指令文件的精确主机路径，而 provider 可见的指令标题继续使用稳定、与根目录
无关的来源标签，并把精确路径问题引导到该 capability。每个真实用户回合前，Reasonix 会用原始用户消息执行
有预算的 BM25 自动召回，把命中作为低权限 user-turn 后缀追加；泛化请求会被抑制，等价事实优先
项目级版本，stale 内容会降权。这不会修改稳定 system prompt 或工具 schema。

拥有当前项目 store 的父 controller（包括顶层 headless）只有在新事实有界、非敏感、纯创建，且明确属于 project/reference 时才能
免确认保存。Ask 下，全局事实、偏好、feedback、更新、重复项、敏感/超长内容和所有 `forget` 仍需
新鲜人工确认。交互式 Auto 把 `remember`/`forget` 作为普通策略 fallback，默认放行并保留显式
`ask` / `deny`；交互式 YOLO 会绕过记忆 ask 审批，除非命中显式 deny。
Guardian、permission hook 仍不能代为批准；子智能体和不拥有该作用域 controller 的 headless surface
会 fail closed，无头 YOLO 也只保留上述 create-only 例外。事实带有不变 ID、单调 revision、时间、type 与 scope；更新先快照旧版本，
restore 与 archive recovery 会创建更高 revision，并拒绝路径逃逸、符号链接、冲突和覆盖。
详细约定见 [`SESSION_MEMORY_RETRIEVAL.zh-CN.md`](SESSION_MEMORY_RETRIEVAL.zh-CN.md)。

### 3.7 权限

权限层按单次 tool call 返回 `Allow`、`Ask` 或 `Deny`：

```go
type Decision int
const (Allow Decision = iota; Ask; Deny)

type Policy struct { Mode Decision; Allow, Ask, Deny []Rule }
func (p Policy) Decide(toolName string, readOnly bool, args json.RawMessage) Decision
```

- rule 可以是 `Tool` 或 `Tool(specifier)`，例如 `Bash(go test:*)`、`Edit(docs/**)`；`Bash=<literal>` 是整条 Bash 命令的精确授权格式，其中 glob 与 Shell 元字符都按普通字符匹配。
- 优先级为 `deny > ask > allow > fallback`；只读工具 fallback 为 Allow，写工具 fallback 使用 `Mode`。
- 交互模式中的 Ask 由用户选择单次允许、session scope 允许、持久允许或拒绝；显式 Deny 在所有模式下都不可绕过。
- 非交互 `reasonix run` 与无头子智能体没有审批界面：默认 Ask/manual 对普通 writer fallback 与显式 ask 规则失败关闭；Auto 只放行普通 writer fallback，显式 ask 仍拒绝；YOLO 可越过普通 Ask，但不能越过 deny、Sandbox，或计划/沙箱逃逸/受管配置写入这类强制新鲜人工审批。交互式 Auto 会放行 `remember`/`forget` 的默认 fallback 并保留显式 ask/deny；交互式 YOLO 会绕过记忆 ask 审批但仍遵守 deny。所有无头模式对其余记忆变更仍 fail closed，只保留有界 create-only project/reference 例外。无人值守自动化需要普通 writer 自主执行时，使用现有的 `--auto` / `-y`。
- 动态 Bash 分两级：参数/算术展开、赋值、不含嵌套执行的 heredoc、普通文件重定向与 Shell glob 不能复用裸 `Bash`、前缀或 glob Allow，保存时只生成 `Bash=<literal>`，但仍遵循普通 fallback，因此 Auto 与获批计划窗口可无提示执行。命令/进程替换、动态命令名、无法解析结构，以及 `eval`、`source`、Shell `-c`、PowerShell/cmd 命令字符串、运行时内联代码参数属于嵌套/间接执行；默认情况下交互 Ask/Auto 必须人工批准，Guardian 与 hook allow 不能代替，无头 Ask/Auto/DontAsk 直接拒绝，只有完全相同的 literal 或 YOLO 可以绕过。高级用户可设置 `[permissions] allow_dynamic_bash = true`，让 Allow fallback（包括 Auto）覆盖这类动态命令；显式 `ask` 与 `deny` 规则仍然优先。
- 安装 MCP server 即授权其全部工具，不再有 server、raw tool、writer 或 destructive 的第二套审批策略；项目 `reasonix.toml` 与 `.mcp.json` 声明同样默认可信，不需要额外启动确认，显式全局 `deny` 仍然优先。全局安装写入用户 `config.toml`，项目声明保留在原项目文件；同名时项目覆盖全局，项目内部 `reasonix.toml` 高于 `.mcp.json`。编辑写回当前生效来源，删除高优先级声明后露出下一层。`readOnlyHint` 与 `destructiveHint` 仅用于调度、Plan/严格只读边界及缓存到实时安全分类复核，不会新增逐调用审批。严格只读子智能体 registry 仍仅暴露已授权且 `readOnlyHint: true`、无 `destructiveHint` 的 MCP；双模型 Planner 通过固定 `use_capability` 代理（从不暴露直接 `mcp__*` schema）调用已授权、非 destructive 的 MCP，不再要求 `readOnlyHint`，destructive 工具留给 Executor。Balanced 双模型的 Executor 使用独立 frontend 复用同一稳定代理，因此 Planner 发现的 capability ID 可在 handoff 后直接执行，同时保持两侧 ledger/audit 隔离。分发前代理会再次复核当前 controller 的 enable、授权和完整运行时连接身份；共享 Host 中仅 server 同名不构成复用权限。
- Plan 是协作流程，不等于全工具只读。普通 built-in 与 Bash 仍走 Ask/Auto/YOLO 和 Sandbox；独立双模型 Planner 允许已授权、非 destructive 的 MCP（即使没有 `readOnlyHint`），但在规划阶段持续阻止 destructive 与未授权目标；没有独立 Planner 的单模型 Plan 仍阻止 MCP writer/destructive。
- Plan 只能由用户显式选择进入，与当前工具审批姿态相互独立；普通聊天不会自动切换到 Plan。Auto/YOLO 不会回答 `ask`，也不会替用户批准 `exit_plan_mode`，获批计划的短期自动执行窗口也不会自动批准后续计划或嵌套/间接 Bash。
- 桌面端协作模式分为 `normal`、`plan` 和 `goal`。Goal 默认不设模型轮数、跨 Run turn 数、墙钟时长或数字式无进展边界，会持续推进直到完成、真实用户/外部阻塞、用户停止/暂停、不可恢复外部错误或用户显式预算耗尽。相同宿主失败、零新增证据与 Todo 停滞阈值只触发重新规划，不产生 `goal_run_budget` 或 `goal_stuck` 暂停。Goal 范围的新颖证据允许新的读取/搜索结果推进任务，但拒绝完全相同的工具、参数和结果重复。未配置相应预算时，累计 turn、token、真实 provider 请求数和实际工作时间只做观测。正数 `[agent].goal_token_budget`、`max_steps`、时间或成本预算仍是用户可选的可恢复边界；Goal token 预算默认 `0`（关闭），从 `budget_spend` 恢复会授予新的预算切片且不清零累计统计；`task_time_budget_minutes = 0`（以及兼容的负数）表示关闭时间边界。旧简单/写入/研究参数仅为兼容元数据，所有目标共用同一个 Goal FSM、宿主 receipt、Delivery readiness 和有界 evaluator。普通聊天不会隐式切换协作模式；旧 `.reasonix/autoresearch/.../` 目录只读，显式旧路径可恢复为普通 Goal。

### 3.8 Slash command

Slash command 分为三类：

- built-in action：`/compact`、`/new`、`/clear`、`/effort`、`/mcp`、`/help`；
- `.reasonix/commands/*.md` 与用户配置目录中的自定义命令；
- MCP prompt：`/mcp__<server>__<prompt>`。

自定义命令支持简单 frontmatter、`$ARGUMENTS`、`$1…$N` 和 `$$`。加载失败的单个命令会被跳过，不应使应用整体退出。

Bubble Tea TUI 的 modal overlay 必须隐藏 composer；slash/`@` autocomplete 等 input-owned overlay 保留 composer。新增 overlay 时必须更新 `chat_tui.hideComposer()` 与 layout test。

### 3.9 `@` 引用

- `@<server>:<uri>` 读取 MCP resource；
- `@<path>` 仅在本地路径真实存在时读取文件或目录，普通 `@mention` 与邮箱保持原文本；
- 文件内容有大小限制，binary 只标记不展开；目录按深度优先列出并跳过 `.git`、`node_modules` 等噪音；
- 解析异步进行，失败显示 notice 但不阻止本回合；
- autocomplete 每次只读取一层目录，避免在大型目录中递归遍历。

### 3.10 子智能体 Profile

子智能体 Profile 是带 `runAs: subagent` 的 Skill。桌面端和 CLI 只允许修改简单、手动调用的 project/global profile；包含 `references/`、`scripts/` 或非托管 frontmatter 的丰富 Skill 不会被编辑器扁平化覆盖。

`reasonix subagent try` 使用只读 Skill runner；`reasonix subagent run` 使用常规权限与 Sandbox。`task` 支持 `profile`、`model`、`effort` 和 `write_paths`；`fleet` 在 session scheduler 上并发调度多个任务。详见[子智能体 Profile](./SUBAGENT_PROFILES.zh-CN.md)。

Profile 描述的是 worker，不是一次运行。委派由五个彼此独立的概念构成：profile 说明这个 worker 怎么思考，`TaskSpec` 说明本次要什么，`CapabilityGrant` 说明本次能碰什么，`ContextCapsule` 说明从什么上下文起步，`SchedulerPolicy` 说明何时以及怎么运行。字段归属于**决定其取值**的那一方，因此 profile 可以携带能力**上界**（`allowed-tools`、`read-only`），但绝不能携带 `max_turns`、`write_paths`、重试或验证策略这类按次取值——它们由任务或调度决定。Skill frontmatter 可以继续变胖；`agent.ProfileFromSkill` 是唯一的收窄点，路由元数据（triggers、auto-use、cost、freshness）到此为止，因为它决定的是**何时**选中一个 worker，而不是它怎么思考。`internal/agent/profile_boundary_test.go` 会在任何一次拓宽时失败。

### 3.11 子智能体以 host 裁决过的结论收尾

写入型子智能体通过调用 `complete_subtask` 结束运行，提交 `status`、`summary`、它被要求满足的 `acceptance_criteria`（每条附上实际跑过的命令或改动的路径），以及尚未解决的 `unresolved`。纯散文仍然接受，但它不再是父智能体据以判断的接口。

提交的 status 是主张，不是判决。在父智能体看到之前，host 会用自己的 receipt 核对每一条引用：`verification` 必须指向 host 记录为执行过的命令，`diff`/`files` 必须指向 host 观测到读写过的路径，而 `manual` 永远不能自证。receipt 无法背书的条目一律降级为 `unsatisfied`，含有此类条目的报告不能保持 `complete`，且降级连同原因一并打印。host 只会下调，永不上调。

因此父智能体收到的顺序是：裁决后的 status 与条目、子智能体自己的散文、host 关于改了什么和跑了什么的 receipt。

### 3.12 写入声明是强制执行的，不是建议

`write_paths` 是调度与强制执行共用的同一个真相来源。写入型子智能体声明了显式路径后，host 会在子智能体启动前把它的工具注册表绑定到该声明：

- 支持路径参数的内建写工具（`write_file`、`edit_file`、`multi_edit`、`move_file`、`notebook_edit`、`delete_range`、`delete_symbol`）拒绝声明之外的任何路径，`move_file` 的源和目标两端都检查；
- 路径先解析到最深的存在祖先并展开 symlink 后再比较，因此 `..` 穿越和声明目录内指向外部的 symlink 都无法把写入洗白；
- 仅当 OS sandbox 能把 `bash` 的写根重绑到该声明时才保留 `bash`，否则直接从子智能体的注册表中移除；
- MCP 一律经 `use_capability`，它在解析阶段——任何 MCP 进程启动之前——拒绝所有未被证明为只读的目标；
- host 无法路径化约束的写工具（自定义、未知）被丢弃；
- 运行结束后，host 用自己记录的变更与声明比对，任何越界路径都会写进该子智能体的 host receipts 交还给父智能体。

省略 `write_paths` 并不等于不受约束：该次运行开工时声明整个 workspace，因此不能和其他 writer 同时开工。若整段只有路径型写入，调度预留会收窄到已写文件，父代理或兄弟任务可以写其他文件。`bash` / MCP 一旦产生 workspace 变更，预留重新变为整区。目录声明可以同时开工，只有落盘到同一文件时才互斥。能力上界（sandbox / `AllowsPath`）仍是声明本身，不会随预留收窄。离开 workspace 的写入仍会被记为越界。

声明路径换来的是并行能力；代价是在 OS sandbox 无法强制写根的宿主上失去 `bash`。

### 3.13 子智能体的上下文继承是显式的

子智能体不隐式继承任何东西。它拿到的恰好是这些：

| 交给子智能体的 | 来源 |
| --- | --- |
| 系统提示 | `DefaultTaskSystemPrompt`、`DefaultReadOnlyTaskSystemPrompt` 或 profile body——不再合成任何其他内容 |
| workspace 根目录 | 首个 user turn 里的 `<workspace-context>` |
| 任务文本 | user turn 本身 |
| 完成契约 | 追加在写入型子智能体的任务 turn 后（见 §3.11） |
| 委派提示 | 嵌套子智能体全新会话上的 `<subagent-context>` |
| plan-mode 标记、推理/回复语言 | 运行选项（设置时） |
| 既有 transcript | 仅通过 `continue_from` / `fork_from` |

按设计**不继承**：`REASONIX.md`、`AGENTS.md`、`CLAUDE.md`、项目与全局记忆（memory queue 被关闭，子智能体也无法写入记忆）、父对话、当前 Goal、planner 输出、同级子智能体的结果。今天要让一条约束抵达子智能体，只能写进它的 profile body 或任务文本——不存在环境通道。

每次运行都会在其 transcript sidecar 中记录一份 `ContextCapsule`：workspace、系统提示来源与哈希、解析后的工具范围与 schema 哈希、model 与 effort、父会话与父工具调用 id、续接的 transcript，以及一个所有字段均为 false 的 `inherited` 块。`capsuleHash` 是它的稳定标识，因此"为什么这个 reviewer 没看到那条约束"可以从记录回答，两次行为不同的运行也可以直接比对而不是猜。capsule 只保存引用与摘要——绝不复制父上下文，这正是委派保持低成本、子前缀保持可缓存的原因。

### 3.14 fleet 是一张小依赖图

fleet item 可以声明 `id` 与 `depends_on`。图的词汇就这么多：没有条件、没有表达式、没有动态扩散。它足以表达

```
research ──▶ implement backend ──┐
        └──▶ implement frontend ─┴──▶ integration test ──▶ review
```

id 默认取 1 起的序号。重复 id、指向不存在任务的 id、自环、成环都会在 preflight 失败——一个注定跑不完的 fleet 绝不会开始。依赖完成后条目立即启动；彼此无序的条目仍按既有 session scheduler 并发。

依赖是图的性质，不是任务的性质：它只存在于 fleet plan 中，绝不进入 `ProfileExecSpec`。这正是让 `depends_on` 不至于成为某种 workflow 语言第一个关键字的原因。

图恰好在该放松的地方放松了写声明 preflight：只有**可能同时运行**的条目才需要互不重叠的 `write_paths`；`implement → review` 这对被边串行化，可以共享路径——扁平 fleet 无法表达这一点。

失败处理只有一个开关。失败或被跳过的任务永远会跳过其整条下游分支——在坏输入上跑依赖项，只会换来父智能体必须丢弃的结果。除非设置 `fail_fast`，独立分支继续推进；`fail_fast` 停止的是**启动**新任务，已在运行的任务留待自然结束，因此写入者绝不会被中途丢弃。

### 3.15 只有一个子智能体构造原语

对外能派生子智能体的 API 很多——`task`、`read_only_task`、`fleet`、`parallel_tasks`、`run_skill`、`/<profile>`、`reasonix subagent run|try`、桌面端预览。它们背后的执行原语必须只有一个：每个入口把请求编译成 `ProfileExecSpec`，交给 `TaskTool.RunProfileSpec`——那是唯一解析深度、工具范围、权限、sandbox、写声明、调度槽位、MCP 前端、transcript 与 capsule、evidence ledger 以及完成契约的地方。

这不是风格偏好。散落在多条构造路径上的安全边界，只要被漏掉一次就够了：此前预览路径构造出未受约束的文件工具、profile 编辑器保存时丢掉 `read-only`，都是某一个入口少套了一层。

必须不持久化 transcript 的入口用 `ContextRequest.Ephemeral` 声明，而不是自己造一个 session——它的承诺是 spec 上的一个字段，而不是第二条构造路径。

`internal/agent/spawn_boundary_test.go` 登记了仍然直接调用底层 runner 的文件，出现新的就失败。剩余条目——`internal/boot`（skill runners）、`internal/cli/review.go`、`desktop/subagents_app.go`——是已知负债，不是先例。

### 3.16 MCP 并发：read-only 不等于 stateless

子智能体共享一个 session Host 及其连接，各自持有独立的 `use_capability` 前端与 ledger。对 stdio 服务器而言，这意味着它们共享同一个进程——以及那个进程的会话状态。

read-only 并不蕴含 stateless。浏览器类服务器会打开页面、切换标签、滚动；这些工具**完全可能诚实地声明 `readOnly`**（确实没有任何东西落到文件系统），但两个子智能体并发调用它，就会在彼此都看不见的状态上交错。写声明在这里帮不上忙——根本没有可声明的东西。

因此每个已配置服务器带一条并发策略：

```toml
[[mcp.servers]]
name = "browser"
concurrency = "serial"   # parallel（默认）| serial
```

`serial` 表示整个 session 内该服务器同一时刻只跑一次调用，无论由哪个子智能体发起。闸门放在共享 runtime 上，因为被交错的那个进程正好就是这个作用域共享的；排队中的调用仍然响应自身的取消。名字看起来是已知有状态的服务器（browser、playwright、puppeteer、chrome、chromium、selenium）默认 `serial`；显式配置永远优先，其余一律保持 parallel，共享 Host 的性能取舍不变。

这是刻意保守的第一版：**一个服务器一条策略，而非按 capability**。按工具的 `parallel_safe` / `exclusive` 提示与显式 `concurrency_key` 分组是后续细化，等真实服务器暴露出同一服务器内工具确有差异时再做。

### 3.17 度量委派是否真的划算

编排容易加、难证明：agent 越多 token 一定越多，而多烧的 token 本身就可能看起来像"变好了"。因此对比实验臂必须**固定模型**并读取 host 记录的事实，而不是散文。

`reasonix run --json` 在既有的 token / cache / 成本 / 耗时之外，额外输出每次运行的委派计数：

| 计数 | 回答什么 |
| --- | --- |
| `subagent_runs`、`subagent_nested_runs` | 实际跑成了什么形状（而非配置成什么） |
| `tool_calls` − `subagent_tool_calls` | 父/子工作量切分 |
| `subagent_mutations`、`duplicate_work_paths` | 是否有两个子智能体重做了同一个文件 |
| `completion_reports`、`completions_prose_only` | 多少运行以可检验的主张收尾 |
| `false_completions`、`criterion_downgrades` | host 拒绝背书的主张 |
| `write_scope_violations` | 逃出声明的写入 |

控制轴目前是**不完整**的，而这正是这些计数暴露出来的：`--ablate subagent` 移除 `task`、`read_only_task`、`fleet`、`parallel_tasks`，但运行仍可通过 `runAs=subagent` 的 profile skill 委派——实测中 `no-subagent` 臂就把一次子运行花在了 `explore` 上。因此该臂应理解为"无 task 工具委派"，而非"单 agent"；实际发生了什么要读 `subagent_runs`，不要相信臂的标签。嵌套深度由 `agent.max_subagent_depth` 控制。

`false_completions` 是其中最关键的一个。它来自 §3.11 的裁决，因此度量的是 **host 拒绝背书**的主张，而不是某个评审者的观感——它是区分"fleet 更快完成了"与"fleet 声称完成了"的唯一数字。

读这些数字时必须对照实测的**噪声底**：同一个臂在同一批任务上重复跑一次，逐任务 token 用量的中位差为 19%、最大 54%，而该次实验里两臂之间的总差异只有 2.5%。因此每格只跑一次，对委派得不出任何结论——效应必须先高过方差才算效应。要么预算足够的重复次数，要么只在 `subagent_runs` 显示确实发生了委派的任务上比较——那次实验里六道题只有一道发生了委派。

目前实测到的结论（单一模型、四种任务形状，每种都用中立 prompt 与强制委派孪生题在**同一份工作**上对比）：三个独立模块各一行修改 3.8x tokens；24 文件搜索 1.5x tokens / 2.2x wall；36 文件三包迁移 2.6x tokens / 4.1x wall；三个真正异质的分支——理论上委派最有胜算的形状——三次重复下 2.4x tokens / 3.7x wall。成功率四种形状全部 100%；强制臂的离散度约为中立臂的两倍——委派同时买来了方差。

子 agent 的 token 数字要小心解读：实测 27 个子运行平均每个 13.4 万 tokens，但那是 9.3 次模型调用上**同一份约 1.4 万上下文被反复重发**的累计值，不是 13.4 万条新内容。在约 90% 缓存命中下，一个子 agent 的真实价格平均为 **¥0.017**。真正有意义的是上面那个 2–4 倍——因为两臂用同一种口径计数；单个子 agent 的累计值不是一个可以拿来和"分支工作量"比大小的阈值。

委派为何罕见，可以从同一批运行里得到答案，而答案**不是"模型权衡后拒绝"**。在 33 次可委派的运行中只有 15% 发生了委派，bash 与全部委派类调用之比是 10:1。记录下来的推理显示，模型反复权衡的是**怎么高效地读**——"that's 25 files... read them in parallel batches... I can read multiple files at once"——而这道题正是为 `explore` 设计的，委派却从未进入它的决策空间。

三个原因可以解释，其中只有一个算缺陷。基础系统提示从未提及委派；所有提及都在技能索引里，且每一处都是刹车（"the heavy path... only when the task genuinely needs context-heavy work, not on weak relevance"），紧挨着的却是对内联技能的油门（"even plausibly relevant... cheap"）。`task` 工具的描述只说它做什么，从不说何时该用它。而模型本就拥有更便宜的并行——**一次往返里发起多个工具调用，不复制任何上下文**——它正是按这个在推理。

考虑到实测的 2.4–4.5 倍代价，"刹车"是正确的默认值；真正的缺口是**没有任何机制能识别出委派确实划算的那少数情况**。强行委派并不能补上这个缺口：在强制 fleet 的那次运行里，父智能体在派发之前就已经在自己的推理中得出了全部三个修复，子智能体只是重新读了一遍代码去执行父智能体已经想好的编辑。**委派转移的是打字，不是思考。**

有一个假设是**未被证伪、而是没测成**：委派的隔离价值应当在父智能体真正被"读过的东西"拖累时才显现。这里没能制造出这种压力——把工作区 `compact_ratio` 压到 0.5% 仍然是零次压缩，因为 agent 靠写脚本而不是靠读把会话维持得很小，而这恰恰就是它赢下每一次对比的同一个行为。要制造上下文压力，需要一道无法被脚本化绕开的题，当前语料里还没有。

其中最有启发的是迁移那道题：不干预时 agent 只读了一个文件、写了个脚本，28 秒改完 108 处调用点；一旦按包切成三份，没有任何一个分支能看见那个一次解决全部三包的变换。**"看起来像并行形状"不构成"切开更便宜"的证据。**

尚未度量、且刻意不伪造的一项：handoff 后返工需要整次运行的变更时序，它属于驱动实验臂的 harness，而不属于记录单次运行的仪器。

## 4. 数据类型

provider 层的核心类型包括 `Role`、`Message`、`ToolCall`、`ToolSchema`、`Request` 和 streaming `Chunk`。`Message` 保留 `tool_calls`、`tool_call_id` 与 `name`；`Chunk` 区分 text、tool call、done 和 error。字段定义以英文规范及 `internal/provider` 源码为准。

## 5. 配置

配置优先级：

```text
flag > ./reasonix.toml > 用户 config.toml > 内置默认值
```

从 v1.8.1 起，用户配置位于 macOS/Linux 的 `~/.reasonix/config.toml` 或 Windows 的 `%AppData%\reasonix\config.toml`。provider key 保存在 Reasonix home 的 `.env`；项目 `.env` 只用于 workspace 范围的非 provider 变量展开。完整路径见[配置路径](./CONFIG_PATHS.zh-CN.md)。

```toml
default_model = "deepseek"

[agent]
temperature = 0.0
reasoning_language = "auto"

[[providers]]
name           = "deepseek"
kind           = "anthropic"
base_url       = "https://api.deepseek.com/anthropic"
# request_url  = "https://proxy.example.com/anthropic/v1/messages" # 可选：完整请求地址
models         = ["deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-flash-vision-exp"]
default        = "deepseek-v4-flash"
# vision_models = ["deepseek-v4-flash-vision-exp"]  # 旧配置兼容；设置页根据模型能力元数据展示图片支持
# 官方 DeepSeek 视觉支持内联 base64、http(s) 图片 URL、以及 Files API file_id。
api_key_env    = "DEEPSEEK_API_KEY"
web_search     = true
context_window = 1000000
# max_output_tokens = 0              # 推荐：官方 DeepSeek 省略字段（服务端 384K）
# max_output_tokens = 32768          # 可选控费上限
# max_output_tokens = 65536          # 可选控费上限
# max_output_tokens = 131072         # 可选控费上限

[tools]
enabled = []
bash_timeout_seconds = 120
mcp_startup_timeout_seconds = 30
mcp_call_timeout_seconds = 300

[permissions]
mode  = "ask"
deny  = ["Bash(rm -rf*)", "Bash(git push*)"]
allow = ["Bash(go test:*)", "Bash(git status:*)"]

[sandbox]
# workspace_root = ""
# allow_write = ["/tmp"]
# forbid_read = ["${HOME}/.ssh"]

[serve]
auth_mode = "none"
```

原生 CLI 更新器始终安装最新的严格 `vX.Y.Z` 正式版。1.x 期间仍解析旧渠道配置与
参数，但统一指向正式版，并在后续保存配置时省略这些字段。

`[sandbox]` 是权限策略之下的强制执行层。权限策略和沙箱边界是两层机制。交互会话可以用「扩展写入范围」审批（仅本次 / 本会话 / 写入项目 `reasonix.toml` / 拒绝）按需扩大可写根；文件工具会自动申请目标父目录，Bash 必须声明 `additional_write_dirs` 和 `justification`。无头 `reasonix run` 缺少目录时 fail closed，请使用 `--add-dir` 或 `[sandbox].allow_write`。`${HOME}` 可在强警告后批准；文件系统根和 Reasonix 会话/状态目录不能通过动态流程批准。file writer 默认限制在 workspace root、Reasonix 用户配置目录和 `allow_write`；`forbid_read` 可阻止读取敏感路径。macOS 使用 Seatbelt，Linux 使用 bubblewrap；若声明 enforce 但平台 backend 不可用，Bash 应拒绝执行而不是静默降级。Windows 当前没有 OS 级 Bash sandbox，file tool 的路径限制仍然生效。

`[serve]` 控制 `reasonix serve` 的 browser frontend。默认 `auth_mode = "none"` 仅适合 loopback；暴露到其他机器时必须使用 token 或 password。只有位于可信 reverse proxy 后方时才能启用 `behind_proxy`。

项目根目录的 `.mcp.json` 可使用 Claude Code 的 `mcpServers` schema；与 `reasonix.toml` 同名时，以后者为准。

MCP 启动与单次工具调用使用不同生命周期。调用方只短暂等待冷启动，而共享的进程启动、授权、
`initialize`、`tools/list` 可在后台继续，最长由 `mcp_startup_timeout_seconds`（默认 `30`）
限制；单个服务器可用 `startup_timeout_seconds` 覆盖。MCP 调用超时只在连接就绪后开始计算。

## 6. 错误处理

- library code 使用 `fmt.Errorf("...: %w", err)` 包装并返回错误，不打印也不调用 `os.Exit`；
- 只有 `cli` / `main` 决定 exit code 和面向用户的信息；
- tool error 返回给模型，不直接终止 agent loop；
- network layer 应对 429 / 5xx 使用有界指数退避。

## 7. 代码风格

- `gofmt`、`go vet` 必须通过；
- package name 使用小写，exported identifier 必须有文档；
- 注释解释“为什么”，而不只是复述“做了什么”；
- 避免过早抽象，优先清晰直接的实现。

## 8. 分发

- 构建：`CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=$(VERSION)" -o reasonix ./cmd/reasonix`
- 目标矩阵：`darwin|linux|windows × amd64|arm64`
- 版本通过 ldflags 注入，来源为 `git describe --tags --always`
- 支持预编译二进制、`go install` 与 Homebrew。

## 9. 路线图（当前范围之外）

- 完成 Sandbox Phase 1 的 escape prompt：检测 sandbox 不可用或拒绝时，提供一次明确、受权限控制的非 sandbox 重试。
- MCP long tail：`headersHelper`、更多 `.mcp.json` scope、tool-search 延迟加载、`list_changed`、channel、elicitation、root，以及可提供 provider 的插件。
- 增加 Anthropic-native provider kind，用于验证 registry 不依赖单一 wire format，并支持原生 prompt cache control。
- 把“始终允许”规则持久化到项目配置，以及为 `reasonix run` 提供 session 级权限覆盖。
