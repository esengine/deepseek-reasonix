# Reasonix CLI 命令参考

<a href="../README.zh-CN.md">README</a>
&nbsp;·&nbsp;
<a href="./CLI.md">English</a>
&nbsp;·&nbsp;
<a href="./GUIDE.zh-CN.md">使用指南</a>

本文介绍交互式会话、一次性自动化、会话恢复、权限参数和常用会话内命令。Provider
配置、插件和沙盒策略见[使用指南](./GUIDE.zh-CN.md)。

## 启动会话

```sh
reasonix
reasonix --model deepseek-pro
reasonix --effort high
reasonix --dir /path/to/project
```

不带子命令运行 `reasonix` 会进入交互式终端界面。尚未配置 provider 时，先运行
`reasonix setup`。

| 参数 | 用途 |
| --- | --- |
| `--model NAME` | 选择已配置的 provider 或 `provider/model` 引用。 |
| `--effort LEVEL` | 覆盖当前会话的 reasoning effort。 |
| `--max-steps N` | 为本次运行设置工具调用轮数上限；`0` 使用自动执行。 |
| `--dir PATH` | 加载配置和工具前切换 workspace 根目录。 |
| `--add-dir PATH` | 增加一个允许工具写入的目录；可重复传入。 |
| `-c`、`--continue` | 恢复最近一次会话。 |
| `-r`、`--resume [QUERY]` | 打开会话选择器，或恢复匹配的会话。 |
| `--copy` | 复制要恢复的会话，并在可写副本中继续。 |
| `--allowed-tools RULES` | 增加仅当前会话生效的权限 allow 规则；可重复传入，`--allowedTools` 是别名。 |
| `--permission-mode MODE` | 以指定的权限姿态启动。 |
| `--yolo` | 以 YOLO 模式启动；是 `--dangerously-skip-permissions` 的别名。 |

适用时，参数可以放在 prompt 前面或后面。

## 更新原生 CLI

```sh
reasonix upgrade                  # 安装最新正式版
reasonix upgrade --check          # 只报告目标版本
reasonix upgrade --force          # 重新安装当前正式版
```

更新器只选择严格的 `vX.Y.Z` 非 prerelease GitHub Release。1.x 兼容期内，旧渠道
位置参数与 `--channel` 仍可使用，但都会解析到同一正式版并打印废弃提示。历史
`[cli].update_channel` 值不再影响更新，并会在 Reasonix 下次保存配置时移除。别名
`reasonix update` 的行为完全相同。

## 配置供应商

```sh
reasonix setup                    # 管理用户全局配置
reasonix setup --local            # 管理 ./reasonix.toml
reasonix setup /path/to/config.toml
```

在交互式终端中，`reasonix setup` 是一个暂存式供应商管理器。它会列出已配置的
provider，并支持：

- 添加 OpenAI-compatible 或 Anthropic-compatible provider；
- 编辑 endpoint 和模型列表；
- 更新 API Key，或测试连接并刷新模型；
- 设置默认模型；
- 删除 provider。

选择“保存并退出”后会先展示并确认待执行操作；取消会丢弃本次修改。保存时 setup 会重新
加载最新配置：桌面端或其他 CLI 产生的不相关修改会被保留，改到同一项时则报告冲突，
不会直接覆盖。

Provider 定义只保存 `api_key_env` 变量名。即使使用 `--local`，Key 的真实值也始终保存
在 CLI 与桌面端共用的 Reasonix 全局 `.env` 中。如果变量名已被其他 provider 使用，
setup 会询问是否共享该凭据；两个 provider 使用不同 Key 时，应改用不同变量名。通过
setup 添加或删除 provider 时，也会同步维护桌面端 provider access，因此相同模型可以
直接在桌面端使用。

### 配置费用展示币种

使用用户全局货币命令查看或选择费用展示币种：

```sh
reasonix config currency             # 显示已保存值和最终解析结果
reasonix config currency auto        # 钱包币种优先，否则原币价表币种
reasonix config currency CNY
reasonix config currency USD
```

`auto` 在配置中保持未解析。只有一个有效钱包币种时，它才会成为当前运行时提示；否则
CLI 使用原币或按 ISO 排序的币种桶。语言和主机 locale 不再选择价表。该偏好只保存在
用户全局配置中，项目 `reasonix.toml` 无法覆盖，因此不支持 `--local`。自定义价格不会被修改。

在交互式会话中，`/currency` 显示已保存值和最终解析结果；
`/currency auto|CNY|USD` 会修改偏好并刷新当前运行时，同时保留当前对话。

在交互式会话中，`/show-balance` 列出三种显示模式并标记当前项；
`/show-balance all|part|no` 设置状态栏的钱包余额显示方式：`all` 完整显示，
`part` 将百位以上的数字用一个前导 `*` 遮挡（百位及以下固定三位、不足补零），
`no` 完全不显示金额（显示 `***`）。偏好写入用户全局 `[billing].show_balance`，
跨会话持久生效。默认 `all`，未配置时保持完整显示。支持别名 `full`、
`partial`/`mask`、`off`/`hide`/`none`。

### 配置自动压缩阈值

桌面端与 CLI 共用用户全局的自动压缩阈值。可以查看当前生效值及来源、修改全局默认值，
或为当前项目添加覆盖：

```sh
reasonix config compact-ratio              # 查看生效值及来源
reasonix config compact-ratio 75           # 设置用户全局默认值
reasonix config compact-ratio --local 75   # 写入 ./reasonix.toml 项目覆盖
```

可设置范围为 30–85%，内置默认值为 80%。数值越低越早压缩，可能增加摘要调用和成本，
也可能降低 prompt prefix 缓存复用率；数值越高则会在压缩前保留更多上下文。阈值以下完整工具结果可能增加普通请求成本；
达到压力后会先持久剪枝，再运行 cache-aligned 摘要。项目 `reasonix.toml` 的优先级高于
用户全局配置。修改会应用于新启动的 CLI 会话；已经运行的会话继续使用启动时加载的阈值。

## 一次性运行与自动化

脚本只需要最终回答时，使用 `-p` / `--print`：

```sh
reasonix -p "总结这个仓库"
reasonix -p "总结这个仓库" --output-format json
reasonix run "实现 main.go 里的 TODO"
reasonix run --auto "实现 main.go 里的 TODO"
echo "解释这段代码" | reasonix run
```

未使用 `-p` 或结构化输出格式时，`reasonix run` 保持正常的终端流式展示。它也接受
`--model`、`--max-steps`、`--effort`、`--dir`、
`--add-dir`、`--continue`、`--resume QUERY`、`--copy`、`--allowed-tools` 和
`--permission-mode`，以及作为 `--permission-mode auto` 别名的 `--auto` / `-y`。

### 基准对照组

`--ablate` 用于整体关闭某个子系统，让基准测试能把成功率的变化归因到它身上。取值是
`evidence`、`planner`、`subagent`、`retrieval`、`compaction` 的逗号分隔组合，另外还接受
`none`（默认，全部启用）和 `all`。子代理继承父代理的对照组配置，对照组名称会写入
`--metrics` 文件，因此记录下来的每次运行都能自证跑的是哪一组。

```sh
reasonix run --ablate evidence,planner --metrics run.json "修复失败的测试"
```

这是测量工具，不是调优开关：关掉某个子系统只会让 Reasonix 在它本来负责的工作上变差。

### 轨迹记录

`--trajectory PATH` 会把整次运行的完整事件流——带绝对起止时间的工具调用与结果、
思考内容、重试、就绪与恢复决策——按每个事件一行的方式追加为带时间戳和序号的
JSONL 记录，便于离线回放并归因时间去向（工具执行 vs. 两次调用之间的模型思考）。
记录复用共享的 `eventwire` JSON 契约（放在 `event` 键下），外层包
`schema_version`、`seq` 和 `ts`（unix 毫秒）。运行被杀死时已写完的行全部保留。
与 `--events-jsonl` 不同，该文件包含提示词、工具参数和思考内容：请像对待会话
转录一样谨慎处理。

```sh
reasonix run --metrics run.json --trajectory run.trajectory.jsonl "修复失败的测试"
```

### 输出格式

| 格式 | 行为 |
| --- | --- |
| `text` | 人类可读文本；配合 `-p` 时只输出最终回答。 |
| `json` | 输出一个最终结果对象。 |
| `stream-json` | 每行输出一个共用 `eventwire` JSON 对象，最后再输出最终结果对象。 |

```sh
reasonix -p "列出有风险的改动" --output-format text
reasonix -p "总结 diff" --output-format json
reasonix run "运行测试" --output-format stream-json
```

最终结构化对象的格式如下：

```json
{
  "type": "result",
  "subtype": "success",
  "is_error": false,
  "duration_ms": 123,
  "num_turns": 1,
  "result": "...",
  "session_id": "...",
  "total_cost": 0,
  "currency": "CNY",
  "total_cost_usd": 0,
  "usage": {
    "input_tokens": 0,
    "output_tokens": 0,
    "cache_read_input_tokens": 0,
    "cache_creation_input_tokens": 0
  }
}
```

`total_cost` 仅在形成单一 `selected` 展示金额时存在（ISO 代码见 `currency`）。有
`cost_quote` 时优先读它：含原币费用、`original_totals`、发生时的官方双区域
`official_table` 估值、`cost_complete`、`display_complete`、`display_status`，以及
`billing_mode`（`payg` 或 `subscription_equivalent`，后者表示如 MiMo Token Plan
的「按量等效估算」）。

`total_cost_usd` 仅为兼容别名，数值镜像 `total_cost`，**不表示一定是美元**。混用
多种原币时不会再报错：若 usage/价表事实完整则 `cost_complete=true`、
`display_complete=false`，并用 `original_costs`/`original_totals` 给出各原币明细，
绝不伪造跨币种合计。

全局展示偏好为 `[billing].display_currency`（`auto|CNY|USD`）；旧
`[desktop].currency` 仍会迁移。供应商原币价表由各条目冻结的 `billing_currency`
决定，切换展示币种不会改写价表。可用 `reasonix doctor billing` 排查。

执行失败时使用 `subtype: "error_during_execution"` 和 `is_error: true`。
结构化模式会把运行时错误保留在 JSON 中，不再额外重复输出一份人类可读错误。

完成校验器已移除。模型正常结束且没有工具调用时，当前轮次直接结束；包含工具调用时，
继续进入工具循环；真正的空响应会在 frozen request 边界重试。旧的
`completion_validation`、`completion_evaluator_model` 和
`REASONIX_COMPLETION_VALIDATION_MODE` 设置仍可读取，但会被忽略，配置渲染器也不再生成；
主机侧的就绪检查、预算、工具安全边界和恢复边界仍然有效。

### 脱敏机器接口

自动化只需要生命周期遥测、不能接收 prompt、reasoning、工具参数/输出或审批文本时，
使用独立的事件参数：

```sh
reasonix run --events-jsonl "运行 focused tests"
```

每行都包含 `schema_version`、`sequence` 和 `kind`，最后一行为
`kind: "run_done"`。`--events-jsonl` 与包含更多内容的
`--output-format stream-json` 是两个独立契约，不能和 `--output-format`
组合使用。

以下只读命令可以查询持久化状态，但不会输出 transcript、label、command、output、路径、
PID 或 hostname。这里的“只读”是指不会修改 transcript、runtime、recovery 或被查询的
状态；首次使用脱敏机器接口时，Reasonix 可能会在用户状态目录初始化一个私有身份密钥：

```sh
reasonix session list --json [--dir SESSION_DIR | --project-root PATH]
reasonix session show <machine-session-id> --json [--dir SESSION_DIR | --project-root PATH]
reasonix session status <machine-session-id> --json [--dir SESSION_DIR | --project-root PATH]
reasonix session recovery [<machine-session-id>] --json [--dir SESSION_DIR | --project-root PATH]
reasonix task list --json [--dir SESSION_DIR | --project-root PATH] [--session MACHINE_SESSION_ID]
reasonix task show <task-id> --json [--dir SESSION_DIR | --project-root PATH] [--session MACHINE_SESSION_ID]
reasonix task monitor list --json [--dir PROJECT_DIR]
reasonix task monitor status <task-id> --json [--dir PROJECT_DIR]
reasonix task monitor events <task-id> --json|--jsonl [--dir PROJECT_DIR] [--after N] [--follow]
reasonix hook list --json [--project-root PATH] [--home-dir PATH]
reasonix hook status --json [--project-root PATH] [--home-dir PATH]
```

对于 `session` 和 `task`，`--dir` 明确指定 session 存储目录，`--project-root`
则解析指定项目的 session store；两者不能同时使用。都未指定时，Reasonix 选择当前
项目的 session store。对于 `hook`，`--dir` 是 `--project-root` 的别名。
`hook list` 的状态值为 `active` 或 `invalid`；`invalid` 表示配置的
event 因事件名、命令/context 来源或工具事件 matcher 无效而无法执行。非工具事件
会忽略 matcher。

机器 session ID 是带密钥的 opaque hash，不是 transcript 文件名。在同一个 Reasonix
用户状态目录中，同一 session 的 ID 保持稳定；不同安装密钥会生成互不关联的 ID，无法再
根据时间戳或模型 label 离线猜测。迁移 Reasonix 状态目录时，如果自动化依赖已有 machine
ID，需要一并保留该私有身份密钥。任务仍在运行时
`finished_at` 为空；只有任务已经结束并且持久化产物存在时，才会输出
`artifact_complete=true`。没有 live session lease 的 `running` 记录会显示为
`interrupted`；再次打开该 session 时也会自动修复持久化生命周期状态。

Schema version 1 的兼容规则：

- 消费端必须忽略未知字段；
- 同一 schema version 内不会删除字段或改变字段类型；
- 空集合编码为 `[]`；
- 参数错误退出码为 `2`，状态/查询错误退出码为 `1`；
- 机器命令错误是带稳定 `error.code` 的 JSON 对象。

## 恢复会话

```sh
reasonix --continue
reasonix --resume
reasonix --resume provider-config
reasonix --resume <session-id>
reasonix --resume provider-config --copy
```

- `--continue` 立即恢复最新保存的会话。
- 在交互式终端中，单独使用 `--resume` 会打开可搜索选择器。
- `--resume QUERY` 接受精确 session ID 或路径，也支持唯一匹配标题或预览内容的
  子串。没有匹配或匹配不唯一时会返回明确错误。
- 为保持兼容，仍接受 `--resume=true` 和 `--resume=false`。
- `--copy` 不修改原 transcript，而是在新的可写会话中继续。原会话已被另一个
  Reasonix 进程占用时可以使用它。

一次性运行可用 `reasonix run --resume QUERY "任务"`，支持 session 文件路径、
session ID，或来自 `--events-jsonl` / `reasonix session show --json` 的不透明
machine session ID。Session lease 会阻止桌面端和 CLI 同时写入同一个 transcript。

## 权限

```sh
reasonix --permission-mode plan
reasonix --permission-mode acceptEdits
reasonix -p "运行指定测试" --allowed-tools "Bash(go test ./...)"
reasonix --allowed-tools "Bash(git *) Edit"
reasonix --allowed-tools "Bash(go test ./...)" --allowed-tools read_file
```

| 模式 | 行为 |
| --- | --- |
| `manual`、`ask` | 普通权限决策会弹出审批。 |
| `auto` | 自动批准普通 fallback 操作，包括交互式 `remember`/`forget`，同时保留显式 ask 和 deny 规则。 |
| `acceptEdits` | 允许文件编辑工具；不等同于完整 Auto 模式。 |
| `dontAsk` | 未预先允许的请求直接拒绝，不弹出审批。 |
| `plan` | 以只读 Plan 模式启动交互式会话。 |
| `bypassPermissions` | 跳过审批；等同于 YOLO。 |

无人值守执行需要放行普通 writer fallback 时，使用 `reasonix run --auto ...`
（或 `-y`）。这个别名不能和显式 `--permission-mode` 同时使用。

`--allowed-tools` 是会话权限覆盖，不是 provider tool schema 过滤器。规则可以用逗号
或空格分隔，也可重复传入参数。配置中的 deny 规则始终优先于命令行 allow 规则。

在非交互运行（`reasonix run` / `-p`）下没有可应答的审批，各模式都以非阻塞方式解析。
默认 `ask` / `manual` 对显式 Ask 决策和普通 writer fallback 失败关闭，只读调用仍会执行；
`acceptEdits` 放行其列出的文件编辑工具，其他 Ask 决策失败关闭；`auto` 放行普通 writer
fallback，但仍拒绝显式 ask 规则；`dontAsk` 拒绝未批准的 writer；`bypassPermissions`
可越过普通 ask 与 writer fallback，但配置的 deny、Sandbox，以及始终需要人工新鲜批准的
工具（plan、沙箱逃逸、受管配置写入）仍然生效。交互式 Auto 会放行
`remember`/`forget` 的默认 fallback，但保留显式 ask 和 deny；交互式 YOLO 会绕过记忆 ask
审批，但仍遵守 deny。
在所有无头模式下，拥有当前项目 store 的顶层 controller 仍可创建有界、非敏感、
create-only 的 project/reference 记忆；其他记忆变更在无人确认时仍会被拒绝。

## 附加目录

```sh
reasonix --add-dir ../shared
reasonix -p "同时更新两个项目" \
  --add-dir ../frontend \
  --add-dir ../backend
```

相对路径从 workspace 根目录解析，并且必须是已存在的目录。Reasonix 会解析符号链接、
去重，并在当前会话中扩展文件写入工具和沙盒 Bash 的写入边界。这些目录只在运行时生效，
不会写入配置。

## 交互操作

`/model`、`/provider` 和 `/resume` 使用可搜索选择器。审批提示也使用相同的行选择
交互，同时保留原有单键快捷操作。

| 按键 | 操作 |
| --- | --- |
| `Up` / `Down`、`Ctrl+P` / `Ctrl+N` | 在选择器或审批行之间移动。 |
| `j` / `k` | 搜索词为空时移动；开始搜索后作为普通 `j` / `k` 字符输入。 |
| 输入文字 | 过滤可搜索选择器。 |
| `Enter` | 选择当前高亮项。 |
| `Esc` | 取消当前选择器或审批。 |
| `y` / `a` / `p` / `n`、数字键 | 执行对应的审批动作。 |
| `Shift+Tab` | 按 `Ask → Auto → Plan → Ask` 循环。 |
| `Ctrl+Y` | 独立切换 YOLO，不进入安全模式循环。 |

响应式底栏左侧显示当前交互状态；空间足够时，右侧显示模型和推理强度。第二行按
可用性显示仓库与会话遥测，例如缓存命中率、上下文占用、压缩余量、后台任务和余额。
“就绪”表示输入框当前空闲；进入选择器、审批、图片粘贴、shell 模式等需要用户关注的状态
时，这个位置会切换。窄终端会移动或压缩完整信息组，不会从中间截断标签。可见标签和执行
设定值会跟随 `/language`。

使用 `/theme auto|light|dark` 选择终端背景模式，也可以从 `/theme` 列出的命名配色中选择
强调色。输入框上下边线、插入光标、选区、滚动条和底栏都会使用当前 CLI 主题。Transcript
导航、多行输入、rewind 和剪贴板操作见[快捷键](./GUIDE.zh-CN.md#快捷键)。

剪贴板操作按内容类型明确分开。本地 transcript 和输入框选区写入系统剪贴板，并且只有写入
成功后才提示完成；SSH 会回退到明确标记的 OSC 52 请求。文本粘贴继续走终端的
bracketed-paste 动作（macOS 通常为 `Cmd+V`，其它平台使用终端自身配置）。Reasonix 接管
本地会话的鼠标时，没有选区的右键会读取剪贴板文本并走同一粘贴路径，有选区时右键优先复制。
SSH 下远端进程无法读取本机剪贴板，请使用终端粘贴快捷键；`/mouse` 可恢复终端原生右键菜单。
图片粘贴由 Reasonix 接管：macOS/Linux 使用 `Ctrl+V`，Windows 使用 `Alt+V`，也可运行
`/paste-image`；附件标记准备完成前，底栏会显示“正在粘贴图片…”。若终端把该快捷键转发给
应用而不是自行粘贴，剪贴板中没有图片时会回退为文本粘贴，因此该按键不会吞掉纯文本。

## 会话内命令

在交互式会话中输入 `/help` 可查看完整命令列表。斜杠补全、帮助、dispatch 和别名来自
同一份 registry，因此界面展示与 TUI 实际接受的命令保持一致。

| 命令 | 用途 |
| --- | --- |
| `/continue-checks [补充要求]` | 继续紧邻上一轮、已暂停的任务收尾检查，并保留其工具证据。该操作仅可消费一次；出现新的用户消息后，旧卡片会被拒绝。 |
| `/model` | 搜索已配置模型并切换当前模型。 |
| `/provider` | 选择 provider，再选择该 provider 下的模型。 |
| `/resume` | 搜索最近会话并切换。 |
| `/status` | 显示模型、effort、cache、Git、后台任务和余额信息。 |
| `/theme [auto\|light\|dark\|style]` | 查看或切换 CLI 背景模式和强调色。 |
| `/currency [auto\|CNY\|USD]` | 查看或切换用户全局费用展示币种，并刷新当前运行时。 |
| `/show-balance [all\|part\|no]` | 查看或设置状态栏钱包余额显示方式（完整、遮挡或隐藏）。 |
| `/paste-image` | 读取剪贴板图片并插入可编辑的附件标记。 |
| `/mouse` | 切换应用内鼠标选区、滚动条和滚轮处理；SSH 会话默认关闭接管，保证终端原生选区可用。 |
| `/effort` | 查看或切换 reasoning effort。 |
| `/preset [standard\|delivery]` | 切换会话质量底线；delivery 开启交付级完成门槛，并在状态栏显示 PRESET 标记。 |
| `/output-style` | 选择回答风格。 |
| `/verbose` | 切换详细 reasoning 显示。 |
| `/sandbox` | 查看沙盒状态。 |
| `/goal` | 启动、查看或清除长周期 Goal。 |
| `/docs [问题]` | 显示内置语料身份，或先本地检索，再让当前配置的 AI 根据版本匹配证据回答。 |
| `/reasonix:docs [问题]` | 当已有自定义命令或兼容插件/Skill 别名占用 `/docs` 时优先使用的内置后备入口；若这个名称也已被占用，菜单会选择下一个空闲的 `reasonix:` 限定名，不覆盖原命令。 |
| `/mcp`、`/skills`、`/hooks` | 查看和管理扩展。 |
| `/remember <note>` | 把常驻 note 追加到项目指令文档；`# <note>` 是快捷方式。 |
| `/memory [subcommand]` | 查看指令、记忆 provenance、召回、revision 与恢复。 |
| `/rewind` | 把对话和/或代码恢复到更早的 turn。 |
| `/tree`、`/branch`、`/switch` | 查看或切换会话分支。 |
| `/reload` | 重载 agent 运行时（扩展、工具、skills、commands、hooks、providers），保留当前会话。回合运行中只排队一次；失败原子——重建失败时当前运行时不受影响。 |

切换模型或 effort 会重建运行时，同时保留当前对话、会话级权限覆盖、附加目录
访问权限和 session ownership。`/reload` 使用同一套失败原子重建语义。
普通请求一律进入 executor，没有自动任务模式。唯一的会话角色是质量底线：standard（默认）或 delivery；事实仍可能高于它。
独立 Planner 只响应显式 Plan、批准边界和 Goal 启动。

用量统计使用独立的可丢弃 rollup 投影：
reasonix catalogs reindex usage [--json]
详见 [用量 Catalog](./USAGE_CATALOG.zh-CN.md)。

可以独立检查或重建可丢弃的 Task 投影：

```sh
reasonix doctor catalogs [--json]
reasonix catalogs reindex tasks [--project PATH ...] [--json]
```

权威 FileStore 边界、跨项目路由和重建行为见
[Task Catalog](./TASK_CATALOG.zh-CN.md)。

### 记忆诊断与恢复

直接运行 `/memory` 会显示全部 project/global active facts，不会隐藏跨 scope 的同名条目。
每条事实包含稳定 ID、revision、scope、type、freshness 和 description。斜杠补全会提供
可用子命令、active ID/name，以及当前 store 拥有的 archive path。

| 命令 | 用途 |
| --- | --- |
| `/memory instructions` | 显示解析后的指令 precedence、目录、imports 和 diagnostics。 |
| `/memory recall` | 解释最近一次自动召回的 query、hits、score、原因、freshness 和预算。 |
| `/memory revisions <id-or-name>` | 显示 active revision 与不可变历史。 |
| `/memory restore <id-or-name> <revision>` | 把旧内容恢复为一个单调递增的新 revision。 |
| `/memory archived` | 列出 archive facts 及其受管路径。 |
| `/memory recover <archive-path>` | 不覆盖 active data，把 archive 恢复为新 revision。 |

这些命令始终作用于当前 session controller。当会话位于远端主机上（`reasonix remote
connect` 或桌面的远程网页窗口）时，它们使用远程 memory catalog，绝不回退读取桌面本机
记忆。权限、自动召回、写入确认和迁移行为见
[Context Engine v2](./SESSION_MEMORY_RETRIEVAL.zh-CN.md)。

契约见 [Session Catalog and Desktop Startup](./SESSION_CATALOG.md)。

用量统计使用独立的可丢弃 rollup 投影：

```sh
reasonix doctor catalogs [--json]
reasonix catalogs reindex usage [--json]
```

```

详见 [用量 Catalog](./USAGE_CATALOG.zh-CN.md)。

### 记忆诊断与恢复

直接运行 `/memory` 会显示全部 project/global active facts，不会隐藏跨 scope 的同名条目。
每条事实包含稳定 ID、revision、scope、type、freshness 和 description。斜杠补全会提供
