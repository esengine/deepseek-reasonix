# Context Engine v2：指令、记忆与检索

Context Engine v2 为 Reasonix 提供两个权限不同的持久上下文层：

- **常驻指令**定义智能体必须怎样工作。
- **背景记忆**保存未来可能有用、但也可能过时的事实。

把两者分开是最重要的设计原则：一个事实不应静默升级成命令；一条长期规则也不应依赖
检索是否恰好命中。

## 选择正确的层

| 放在哪里 | 适合存放 | 示例 |
| --- | --- | --- |
| `AGENTS.md`、`REASONIX.md` 或 `CLAUDE.md` | 每个相关回合都必须存在的规则 | 必跑测试、仓库边界、评审约定 |
| 项目记忆 | 只适用于当前 workspace 的持久事实 | 发布分支、代码中看不出的服务约束、项目工单 URL |
| 全局记忆 | 明确需要在所有 workspace 可用的事实 | 用户显式选择为全局的偏好 |
| 会话历史 | 原始措辞、工具输出，或尚未沉淀为稳定事实的决定 | 昨天的报错、已放弃的方案 |

指令文件应保持简短。它们属于 cache-stable prompt prefix，多写的一段内容会被每个回合携带。
可以按需发现的事实应放进记忆。

一个最小项目文件通常就够了：

```markdown
# Build and verify

- Run `go test ./...` before reporting completion.
- Do not edit generated files under `desktop/frontend/src/generated/`.
- Keep public API changes backward compatible.
```

在 CLI 中，`/remember <note>` 和 `# <note>` 会直接把内容追加到项目指令文档。它们是
常驻指导的快捷方式，不是 agent 用来创建背景事实的 `remember` tool。

## 指令解析

Reasonix 识别 `REASONIX.md`、`AGENTS.md`、`CLAUDE.md`，以及对应的 `.local.md`
变体。它先加载 Reasonix home 下的用户全局指令，再从 workspace root 逐级走到目标路径；
在每个目录内，先加载普通文件，再加载该目录的 `.local.md`。

更深目录高于更浅目录；同一目录内 local 变体高于普通文件，因此越靠后的条目冲突时优先。
用户当前请求始终是最高优先级的用户指令。展开后正文完全相同的文件会去重，并保留更具体
的来源。

指令文件可用独占一行的相对路径导入另一个文件：

```markdown
@docs/agent-testing.md
```

导入按确定性顺序展开、去重，最多五层，并被限制在源指令文件拥有的目录内。绝对路径、
父目录逃逸、符号链接逃逸、不可读文件和循环引用都会被拒绝并形成诊断，不会被静默信任。

用下面的命令查看真实解析结果：

```text
/memory instructions
```

它会显示加载优先级、scope、目标目录、imports 和 diagnostics。桌面 Context Center
展示同一套 provenance。

## 背景事实模型

每条事实都是一个 Markdown 文件，包含：

- 不变的 `id`；
- 单调递增的 `revision`；
- `created_at`、`updated_at` 时间；
- 便于阅读的 name、title 和 description；
- 相互独立的 `type` 与 `scope`；
- 可选的检索 `keywords` —— 关键词的同义词与双语别名，让换一种说法或换一种
  语言的提问也能命中这条事实；
- 可选的 `subject_key` —— 点号分隔的键，标明这条事实回答的是哪个问题
  （`project.package_manager`、`user.response_style`）；
- Markdown 正文。

`type` 表示内容类别：

- `user`：用户身份或偏好；
- `feedback`：关于怎样工作以及原因的反馈；
- `project`：代码库本身无法直接得出的项目目标或约束；
- `reference`：URL、工单 ID 等外部资源。

`scope` 表示生效范围：

- `project` 是安全默认值；
- `global` 必须显式选择。

type 不推导 scope。项目反馈仍只属于项目，全局 reference 仍然是 reference。

subject key 是知识冲突模型：同一 scope 内每个 subject 至多一个 active 值。对已被
占用的 subject 再存新事实会被拒绝并给出持有者的 id——于是 "npm → pnpm" 成为同一
事实的新 revision，而不是两条互相矛盾、同时活跃的事实。`/memory subjects` 列出在用
的 key；回答同一 subject 的事实在覆盖与召回抑制中视为等价，与它们的 name/title 无关。

当等价的项目事实和全局事实同时存在时，自动召回使用项目事实。Context Center 和
`/memory` 仍展示两者，并解释覆盖关系，而不是删除或隐藏任何来源。

第三个维度 `activation` 与前两者正交：`relevant`（默认）表示事实只走检索；`pinned`
表示正文在下一真实用户回合前快照进低优先级 `session-context`。pin 必须是用户显式选择（`/memory pin
<id-or-name>`，或明确要求助手），且 pinned 正文总量上限 1,500 字符——在 pin 时强制
执行，超限会提示把"永远必须遵守的规则"移入 REASONIX.md/AGENTS.md instructions。
一个事实要么 pinned（在 `session-context` 里）、要么 relevant（可被召回）：不会两者皆是，也不会
两者皆非。

为兼容旧数据，早于该字段的全局 `user`/`feedback` 事实保持 pinned，直到显式 unpin。
存在等价项目事实时，它会在背景快照构建前屏蔽对应的全局 pinned 指导，因此"项目覆盖
全局"不依赖后续查询是否恰好触发召回。

## 自动召回

每个真实用户回合开始前，Reasonix 会用原始用户消息搜索 active facts。宿主追加给 provider
的上下文不会反过来污染查询。选中的事实作为有预算、低权限的后缀追加到本轮 user turn，
不会修改 system prompt 或工具 schema。

召回策略刻意保守：

- “继续”这类泛化回合不触发召回；
- 用 BM25 排序有区分度的词法命中（CJK 文本按双字 bigram 匹配，命中需要真实的
  词语重叠，零散的常用字不算）；
- 项目事实有轻微相关性加权；
- 过期事实只降权，不静默删除；
- 本轮存在等价项目事实时，不再注入对应的全局 fallback；
- 已经作为稳定指导存在的全局 `user` / `feedback` 不会被自动召回重复注入；
- 默认最多四条事实、2,400 字符；
- provider 可见块不包含 fact storage path，snippet 中的 home directory 前缀会替换为
  `<local-home>`。

freshness 默认按事实类型计算：

| 类型 | fresh | current | 超过多久为 stale |
| --- | ---: | ---: | ---: |
| `reference` | 14 天 | 45 天 | 45 天 |
| `project` | 30 天 | 180 天 | 180 天 |
| `user`、`feedback` | 90 天 | 365 天 | 365 天 |

类型只是默认值，不代表事实的真实易变性——README 地址可能三年不变，release 分支可能
三天就失效。显式 `volatility` 会覆盖类型窗口：`volatile`（7 / 30 天）、`stable`
（90 / 365 天）、`evergreen`（永不老化）。两个可选时间戳进一步细化：`expires_at`
是硬边界——过期后事实状态为 `expired`，完全不再被自动召回（显式搜索仍可见）；
`last_verified_at` 由 `/memory verify <id-or-name>` 或助手重新确认事实时打戳，
在不改变 `updated_at` 含义的前提下续期新鲜度时钟。

freshness 是提醒和排序信号，不代表事实真假。召回文本会明确告诉模型：内容可能错误或过期，
不能覆盖当前请求和常驻指令。

查看最近一次决定：

```text
/memory recall
```

trace 包含 query、选中的 ID/revision、score、命中原因、freshness、预算使用量、
omitted 数量和 suppressed 原因。

需要更深检索时仍可使用只读 `memory` tool 的 `search`、`read`、`list`。独立的只读
`instruction_sources` capability 会返回当前会话生效的常驻指令文件的精确主机路径；缓存稳定的
system prompt 仍使用 `user/AGENTS.md` 这类 provider 相对标签。需要原始措辞或工具输出时，应
使用 `history`。

## 安全写入与确认

普通路径零配置。只有同时满足以下条件时，Reasonix 才可以自动创建一条新记忆：

- 当前父 controller 拥有本项目 memory store（可以是交互式，也可以是顶层 headless，但不能是子智能体）；
- type 被显式标为 `project` 或 `reference`；
- scope 为 project 或省略；
- 操作是纯创建，不是更新；
- 正文不超过自动写入预算；
- 未检测到凭据、secret、私钥或邮箱；
- 不存在同名、同 title 或同 description 的事实。

授权是一次性的，存储层还会强制 create-only，因此评估后并发出现的事实也不会被覆盖。

在 Ask 下，其余情况仍需显式确认：

- 全局事实；
- `user` 偏好和 `feedback`；
- 更新已有 ID 或 revision；
- 可能重复的内容；
- 敏感或超长内容；
- 所有 `forget` 操作。

Ask 保留这些确认。交互式 Auto 把 `remember`/`forget` 作为普通策略 fallback：默认调用直接
执行，显式 `ask` / `deny` 仍生效。交互式 YOLO 会绕过记忆 ask 审批，除非命中显式 deny。
Guardian 和 permission hook 不能替用户批准。顶层
headless controller（包括无头 YOLO）只能使用上述同一个一次性低风险创建路径；子智能体以及不拥有该作用域
controller 的 headless surface 会 fail closed，其他无头记忆变更仍必须有交互式确认界面。

用户直接在 Context Center、`/remember`、restore 或 recover 命令中发起的操作，本身就是
显式用户动作，不会再增加一次审批。

## Revision、归档与恢复

更新事实时，旧版本会先保存为不可变快照。过期的 `expected_revision` 会被拒绝，不会覆盖
更新后的内容。

恢复旧 revision 不会原地倒退存储，而是把所选内容复制成一个更高的新 revision，保持
单调审计链：

```text
/memory revisions <id-or-name>
/memory restore <id-or-name> <revision>
```

`forget` 会把事实移出 active recall 并放入 `.archive/`。恢复只接受当前 store 拥有的
archive entry，拒绝符号链接和路径逃逸，拒绝 ID/name 冲突，也绝不覆盖 active file：

```text
/memory archived
/memory recover <archive-path>
```

恢复出的内容同样成为一个更高的新 revision。Restore 和 recover 会通过一次 turn-tail note
立即作用于当前会话，并在下次会话自然进入稳定 prefix。

## 零配置建议

打开桌面端 Suggestions tab 时，会自动扫描近期本地用户回合，不需要设置开关。它会提出：

- 从明确偏好、约束和项目约定中提取的长期记忆候选；
- 从重复工作流模式中提取的 Skill 候选。

扫描使用原始用户内容，并与两个 scope 的 facts 和已加载指令正文去重；扫描本身绝不写入。
每个候选都展示 evidence，必须由用户显式接受。远程 workspace 会 fail closed：远端不提供
能力时，Reasonix 不会回退读取桌面机器的本地 session 或 memory。

## 管理界面

直接运行 `/memory` 会显示两个 scope 的全部 active facts，包括 ID、revision、type、
scope、freshness 与存储来源。CLI、Desktop 和 remote workspace 都提供结构化补全。

| 命令 | 结果 |
| --- | --- |
| `/memory` | 指令、事实和 archive 综合摘要 |
| `/memory instructions` | precedence、目录、imports、diagnostics |
| `/memory recall` | 最近一次自动召回 trace |
| `/memory revisions <ref>` | active fact 与不可变历史 |
| `/memory restore <ref> <revision>` | 恢复为一个新 revision |
| `/memory archived` | archive facts 与路径 |
| `/memory recover <path>` | 把当前 store 拥有的 archive 恢复为新 revision |

Context Center 用图形界面展示同一模型，还会显示冲突和 project-over-global 解释。

## 升级兼容

Context Engine v2 会自动升级旧 store，不需要设置：

- 没有 ID 的旧事实获得确定性的 `legacy-*` identity；
- 缺少 revision 的事实从 revision 1 开始；
- 缺少 scope 时，根据所在 project/global 目录推导；
- migration 幂等，只写入一次新 metadata；
- 新旧版本共享 state root 时，兼容路由字段可避免旧客户端把事实移错目录；
- 旧 `MEMORY.md` 作为派生数据处理，并根据事实文件重建；
- 旧 Memory v5 `<memory-compiler-execution>` transcript 仍可读取，退役的
  `[agent].memory_compiler` 设置会被移除。

不需要 vector database、embedding service、setup wizard 或手动 re-index 命令。

## Cache 与隐私契约

- 常驻指令和派生 memory index 在会话开始时进入稳定 prefix。
- Provider 可见的指令 provenance 只使用稳定的 `workspace/...` 与 `user/...` 标签；绝对来源路径
  和 store 路径仅保留在本地诊断中。
- Provider 可见的 memory tool result 只使用稳定的 `project/<name>.md` 与
  `global/<name>.md` 引用。这些引用可直接用于 read、update、revision 和 archive；即使两个
  scope 中存在同名事实，也会精确定位；Context Center 和本地恢复诊断仍保留真实存储路径。
- 动态召回和会话中途改动只追加到当前 user turn。
- diagnostics 不进入 provider request。
- 自动召回不暴露 fact storage path，并替换 snippet 中的 home directory 前缀。
- 外部审批通知只收到工具名，不收到记忆正文。
- 远程管理只使用远程 controller 的 memory catalog，绝不回退读取桌面本机 store。

这样既保持 provider-visible prefix 稳定，也让动态上下文可解释、可恢复。
