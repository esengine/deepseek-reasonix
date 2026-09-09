# 工具合约

<a href="./TOOL_CONTRACT.md">English</a>

本文记录 Reasonix 编译期内置工具的 provider-visible 合约。运行时 registry 使用同一条 canonical schema 路径；测试会校验这里列出的工具名、read-only 标记和 schema 快照不会漂移。

| 工具 | Read-only | 说明 |
| --- | --- | --- |
| `bash` | false | 执行 shell 命令并返回 stdout/stderr。构建、测试、git、包管理器等使用它；读写查找文件优先使用专用工具。 |
| `bash_output` | true | 读取后台 `bash` 或 `task` job 自上次读取后的新增输出和状态。 |
| `code_index` | true | 轻量内置代码符号索引；优先使用 `lsp_*` 或代码图 MCP，缺失时用它兜底。 |
| `complete_step` | true | 用证据记录已批准计划中一个步骤的完成情况。 |
| `compress` | true | 压缩当前模型可见对话中选定的范围，不删除可见历史。仅在用户明确要求压缩上下文时使用；锚点必须是某条真实用户消息中唯一、精确的原文片段。 |
| `delete_range` | false | 用精确 start/end 文本锚点删除文件中的连续范围。 |
| `delete_symbol` | false | 用 Go AST 删除 Go 源文件中的命名符号。 |
| `edit_file` | false | 将文件中的唯一精确字符串替换为另一个字符串。 |
| `glob` | true | 查找匹配 glob pattern 的文件。无依赖的 glob 应同轮下发。 |
| `grep` | true | 在文件或目录下按正则搜索文本。无依赖的搜索应同轮下发。 |
| `kill_shell` | false | 终止后台 `bash` 或 `task` job。 |
| `ls` | true | 列出目录条目，可递归。无依赖的目录读取应同轮下发。 |
| `move_file` | false | 移动或重命名文件。 |
| `multi_edit` | false | 对单个文件原子应用多个编辑。 |
| `notebook_edit` | false | 编辑 Jupyter notebook 的单个 cell。 |
| `read_file` | true | 按可分页的行号格式读取文本文件。`intent` 声明意图：`inspect`（无范围时的默认，有界预览）、`range`（有 offset/limit 时的默认，指定窗口）、`full`（扫描全文并分页到结尾）。续页时把结果里的 `cursor` 原样传回，由宿主定位到确切的下一位，无需自行计算 offset。无依赖的读取应同轮下发。 |
| `todo_write` | true | 记录并替换当前工作的结构化任务列表。 |
| `view_image` | true | 按路径读取本地 PNG、JPEG、GIF 或 WebP，通过结构化图片通道交给视觉模型。最大 3 MiB、4000 万像素，沿用读取权限。 |
| `wait` | true | 等待后台 job 完成并返回最终输出。 |
| `web_fetch` | true | 通过 HTTP/HTTPS 获取 URL 文本内容。 |
| `write_file` | false | 写入文件内容，必要时创建父目录。 |

## Schema 快照

完整 canonical schema 不在文档中手写，避免文档和代码手工漂移。运行：

```bash
go test ./internal/tool -run TestBuiltinToolContractDocumentation
```

该测试会用 `tool.BuiltinContractEntries` 校验每个内置工具都有文档行、read-only 标记、非空 description 和 canonical JSON schema。

## 默认 Full Boot Surface

默认 full-token boot 会发送上面的内置工具，并额外发送 session、memory、skill、subagent、LSP、install 和 slash-command 工具：

每个会话都使用这套 Executor 工具面，并额外提供稳定代理 `use_capability`
（list/inspect/call/decline），用于在不改变 provider 可见 Schema 的前提下发现和调用按需
MCP（含 `auto_start=false`）。宿主根据真实工具动作建立验证义务：后续相关写入会使旧的
验证、复查和签收重新变为未满足；Goal 项和已批准 Plan 的验收项为 Strict；`complete_step`
必须引用最后一次相关写入之后的证据。Skill/MCP 的 require/prefer 路由受门禁约束（只读回答
同样不能跳过 require 能力）；触及认证、Schema 或破坏性路径后，结构化 review 的
`reviewed_paths` 必须有宿主观测到的 read/diff 证据。

## 统一 Boot 工具面

每个会话都使用同一套 provider 可见核心工具和同一个 `use_capability` 代理。

双模型 Planner 与全部 task/fleet 子 Agent 同样使用 `use_capability`（且从不暴露直接
`mcp__*` schema）。Planner 与普通可写子 Agent 可调用已安装或项目配置 MCP，不要求
`readOnlyHint`；Planner 将 `destructiveHint` 留给 Executor，普通子 Agent 走可信 MCP 路径
（实时授权复核 + 仅显式 deny）。writer/destructive 调用仍会串行并按 mutation 记录，继续受
证据、工作区租约和闭环门禁约束。严格只读子 Agent 共享同一代理 schema 与 Host 连接，但执行仍要求 `readOnlyHint` 且
非 destructive。双模型会给 Planner 与 Executor 分别挂载独立代理 frontend，确保规划阶段
发现的 capability 在 handoff 后仍可直接调用；两者 ledger/audit 隔离，但共享 Host 连接。
单模型会话不启用独立 Planner。

`use_capability` 的解析阶段无副作用：`action=list` 只返回已配置 MCP 服务器的精简排序摘要，
不会展开每个缓存工具的 description，也不会启动服务器；需要某个已启用服务器的实时或缓存工具目录时，
对对应 `mcp-server:<name>` 使用 `action=inspect`，同样不会启动服务器。对未连接服务器的
`action=call` 只生成惰性目标；Plan 只会对真实目标重新检查显式阶段 opt-out，服务器进程只在
权限门禁与 PreToolUse Hook 放行之后才启动。按需启动的
子进程随会话存活（不会随单次调用结束而退出）；`action=inspect` 对已连接服务器列出实时工具，未连接
时只读取缓存 schema，绝不启动进程。无 schema 缓存的服务器首次发现走 `mcp-server:` id 的
`action=call`：解析为受门禁保护的连接目标（权限名为独立的
`mcp_connect__<server>`；例如精确拒绝规则 `deny = ["mcp_connect__github"]`
会在进程启动前拦截），放行后连接并返回实时工具目录。MCP 工具名规则仍为精确匹配，
`mcp__github__*` 不是工具名通配规则。安装 MCP 即授权 Planner 使用其非 destructive 工具；
第三方若错误省略 `destructiveHint`，远程副作用属于用户安装信任范围。每次 connect 或
`tools/call` 前，frontend 都会再次复核当前 runtime 的 enable、授权与精确 Host 连接身份；另一个
项目/tab 在共享 Host 上的同名 client 会在进程、网络或工具分发前被拒绝。

固定代理的 provider 可见 name、description、schema 与顺序不会随 MCP inventory 变化。

frontend 绑定当前会话 reader 后，同一个固定代理还会列出只读能力
`session:tool_result`。它按 UTF-8 字节偏移分页读取某条工具结果的本地完整副本，不新增
top-level schema。调用必须提供 `tool_call_id`；新截断标记还会给出稳定 `result_ref`，重复
call ID 时必须用它消除歧义。`offset` 默认 0，`limit` 默认 16KiB、最大 24KiB。响应先返回
`result_ref`、实际 offset、`next_offset`、`total_bytes`、完整 SHA-256 与 `complete`，随后是
原文页。reader 只绑定当前 Agent session，clone capability frontend 时不会继承父 reader。
已经拥有 `use_capability` 的受限子 Agent 只能读取自己的结果；allowed-tools 配置若完全没有
该代理，不会为了回读而扩大工具面。分页是可选的：截断或窗口读取不是主机侧债务，
`git commit` 等未声明目标的工具或最终回答可以紧接着进行。路径级写证据仍要求先读
被编辑的文件。

`read_file` 本身按固定默认 2000 行分页（另有 8 MiB 本地安全上限）。还有后续行时，工具
会追加 `PARTIAL view` 尾注，写明可见范围、文件总行数（超出有界预读后显示为 `N+` 下界）
和下一次 `offset`。可见窗口够用时不必读完全文。

`ask`, `docs`, `explore`, `fleet`, `forget`, `history`, `install_skill`, `install_source`,
`list_sessions`, `lsp_definition`, `lsp_diagnostics`, `lsp_hover`,
`lsp_references`, `memory`, `parallel_tasks`, `read_only_skill`,
`read_only_task`, `read_session`, `read_skill`, `read_subagent_result`, `remember`, `research`,
`review`, `run_skill`, `security_review`, `slash_command`, `task`.

`parallel_tasks` 与 `fleet` 会为每个已持久化子 Agent 返回公平分配的预览和稳定的
`Subagent reference`，使合并结果始终低于单工具输出上限。`read_subagent_result`
按 UTF-8 字节偏移分页读取某个引用对应的完整最终答案，因此长篇并行调研无需一次性全部
注入父会话也不会丢失。引用只允许在当前会话 lineage 和工作区内读取。

已持久化的子 Agent 结果还会携带明确的 `status`（`completed`、`partial`、`failed` 或
`cancelled`）和 `retryable` 标志。部分完成或失败的子 Agent 可能仍带有最后一条可见回答和
引用：用 `read_subagent_result` 查看，用原有 `task` / `run_skill` 的 `continue_from` 参数
继续可重试的任务。`session:tool_result` 只用于普通工具输出，不用于读取子 Agent transcript。

`use_capability`（`action` = `list` | `inspect` | `call` | `decline`）在 provider
可见工具面上始终存在（没有按任务复杂度切换的工具档位）。可选工具仍在 host
registry 中供调度，但不会展开到 top-level provider schema；模型通过 `use_capability`
调用，避免缓存前缀因 schema 变化而失效。

`internal/boot.TestBootToolContractMatchesProviderVisibleSurface` 会校验真实 boot registry 合约和 provider request 一致，包括 read-only 标记和 canonical schema。

## 统一启动工具面（所有任务）

每个任务共享同一套精简的 provider 可见核心：直接编码工具、后台 shell 生命周期工具，
以及稳定的能力代理：

`bash`, `bash_output`, `edit_file`, `kill_shell`, `read_file`, `view_image`,
`wait`, `write_file`, `compress`（若注册），以及 `use_capability`。

可选工具（`glob`、`grep`、`ls`、`web_fetch`、MCP、skills、subagents、docs、会话历史、
记忆写入、workflow 等）仍在 host registry 中可调度；模型通过 `use_capability` 列举、
检查、调用或拒绝它们，且不会改变 provider 工具列表。改变的是宿主根据真实动作建立的
验证义务，而不是 provider 可见工具集合。已退役的 `connect_tool_source` 不再注册。

## 参数错误与恢复

宿主在 extension 拦截、权限审批、hook、写入租约、子代理执行和工具分发前校验真实目标参数。
extension 替换调用后仍须重新解析和校验。参数不合法属于“工具未执行”的普通错误，不是权限拒绝；
修正参数后，任意后续调用都可以再次尝试，无需 inspect 或新用户轮次解锁。
正确调用仍须通过正常的权限与执行检查。

错误保留目标工具名、schema 指纹、违规字段路径和
`argument_validation:<tool>:<fingerprint>:<category>` 诊断标识。
反馈明确参数应位于直接工具的输入根对象，还是 capability 调用的 `arguments` 内。
只有单层包装的内层对象符合真实契约（含条件校验）时，才可能给出不含参数值的多余
`arguments` 包装提示。这只是建议：诊断不会自动拆包、转换类型、补字段或执行参数。
合法的 `arguments` 字段及 skill 嵌套契约保持不变，空值/null 的既有校验兼容也保持不变。

capability 解析前返回的输入错误，在外层 schema 能确认违规时也获得统一反馈。
成功解析的调用不会新增外层校验门；目标不可用和授权错误保留自己的原因。
宿主 schema 编译失败属于配置问题，不要求模型改写参数修复；第三方 MCP 的既有
schema 编译失败回退策略不变。

`inspect` 继续用于查询契约，不再承担解锁职责。schema 专用错误计数和第三次失败锁定已移除。
连续三个等价失败批次由现有通用 storm breaker 给出软性收敛提示；同一批次多个调用不累计为多个轮次，
出现成功结果时按既有行为重置失败序列。只有参数错误时，反馈要求纠正参数，而不是禁止绕过权限。
仍无法纠正时，模型可以说明“工具参数生成失败”及未完成工作，这不代表任务已完成。
真实权限、Plan、hook 和写入循环限制仍然有效。

收敛依靠提示。当 `MaxSteps=0` 且未配置显式预算时，不保证固定轮次内强制停止；
用户配置的轮次/支出限制及取消机制仍然有效。不新增修复模型请求、供应商开关或工具 schema 变化。
错误反馈限制为 4 KiB，追加在失败工具结果中，不重写此前消息或稳定的 provider 前缀。
新增反馈会消耗上下文 token；历史错误文本保留原样。

参数校验、失败、跳过及远程分发计数保持原有含义，内部包装诊断不重复计数。
metrics 中旧的 `capability_loop_guard.RepeatFailures` 和 `BlockedCalls` 字段继续保留兼容，
但新运行不再递增它们，也不将它们重新解释为 storm 干预次数；后者仍使用现有 `loop_guard` Notice。
无需迁移会话或配置；降级会恢复旧版错误恢复行为，但不改变已存储会话。
