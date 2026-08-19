# Reasonix 使用指南

Provider 模型能力元数据见
[`MODEL_CAPABILITIES.zh-CN.md`](./MODEL_CAPABILITIES.zh-CN.md)。

<a href="../README.zh-CN.md">README</a>
&nbsp;·&nbsp;
<a href="./GUIDE.md">English</a>
&nbsp;·&nbsp;
<a href="./SPEC.md">规格</a>

> 日常配置与使用。工程契约与内部实现（数据类型、registry、包结构、路线图）见
> **[规格 SPEC.md](./SPEC.md)**。

## 目录

- [配置](#配置)
- [计费与展示币种](./BILLING.zh-CN.md)
- [CLI 命令参考](./CLI.zh-CN.md)
- [环境变量](#环境变量)
- [Web 前端](#web-前端)
- [配置路径](./CONFIG_PATHS.zh-CN.md)
- [思考语言](./REASONING_LANGUAGE.zh-CN.md)
- [任务合约与暂停策略](./TASK_CONTRACT.zh-CN.md)
- [自定义 OpenAI-compatible provider](#自定义-openai-compatible-provider)
- [桌面端 Hooks](./DESKTOP_HOOKS.zh-CN.md)
- [快捷键](#快捷键)
- [权限与沙盒](#权限与沙盒)
- [能力诊断](#能力诊断)
- [插件（MCP）](#插件mcp)
- [斜杠命令](#斜杠命令)
- [内置文档检索](#内置文档检索)
- [@ 引用](#-引用)
- [双模型协同](#双模型协同)

## 配置

优先级：**flag > `./reasonix.toml` > 用户配置文件 > 内置默认值**。从
**Reasonix v1.8.1** 开始，用户配置位于 macOS/Linux 的
`~/.reasonix/config.toml`，Windows 为 `%AppData%\reasonix\config.toml`；迁移和相关数据路径见
[配置路径](./CONFIG_PATHS.zh-CN.md)。标注为“仅用户/全局”的字段（包括 agent 轮数上限）不会被 `./reasonix.toml` 覆盖。
Provider 通过 `api_key_env` 命名密钥，真实密钥值保存在 CLI 与桌面端共用的
Reasonix 全局 `<Reasonix home>/.env`。项目 `.env`、home `.env`、继承的 shell 环境变量、旧 credentials 和系统 keyring 都不再作为 provider key 的运行时 fallback；旧凭据只作为迁移来源读取。项目 `.env` 仍会作为当前 workspace 范围内的 MCP/plugin 非 provider `${VAR}` 展开来源，但不会导入 provider key 或 Reasonix 控制变量。全局 `config.toml` 和 `.env` 的完整结构见
[配置路径](./CONFIG_PATHS.zh-CN.md)。

桌面端和 CLI 端的可见思考语言设置，见 [思考语言](./REASONING_LANGUAGE.zh-CN.md)。
桌面端 Hooks 的 JSON 配置、事件 key 和 payload 字段，见 [桌面端 Hooks](./DESKTOP_HOOKS.zh-CN.md)。
`SessionStart` hook 可通过 stdout 或 `hookSpecificOutput.additionalContext` 把插件/工作流 bootstrap 内容一次性注入下一轮真实用户输入上下文，而不是写入稳定 system prompt。
插件包可通过 `hooks/session-start-codex` 或插件根目录 `CLAUDE.md` 提供该启动上下文；Claude 风格 `.claude/settings.json` command hooks 也会按同名事件映射到 Reasonix hooks。

```toml
default_model = "deepseek-flash"   # 执行器；设 [agent].planner_model 可加规划器
# language    = "zh"               # 界面语言；为空则按 $LANG / $REASONIX_LANG 自动检测

[ui]
# shortcut_layout = "desktop"      # classic|desktop；兼容旧配置
# cursor_shape = "bar"             # block|underline|bar；CLI/TUI 输入光标
show_turn_usage = false             # 隐藏 TUI 每轮 token/费用回执；默认 true

[agent]
reasoning_language = "auto"      # 可见思考过程语言：auto|zh|en
# plan_mode_read_only_commands = ["gh issue view"]   # 仅兼容旧配置；Plan bash 现由 Permissions 决定
# planner_model = "deepseek-pro"      # 可选的低频规划器
# vision_model = "auto"                # 可选；文本模型先用同服务商视觉模型生成隐藏图片摘要
# subagent_model = "deepseek-pro"     # runAs=subagent skill 的默认模型
# subagent_models = { review = "deepseek-pro", security_review = "deepseek-pro" }
# max_subagent_depth = 2              # 子代理嵌套委派深度；设为 1 可恢复旧的单层边界
# max_subagent_concurrency = 6        # 会话级子代理总并发（task/fleet/skills）
# max_parallel_writers = 3            # 互不重叠 write_paths 时的并行写入上限
# compact_ratio 是唯一自动维护阈值（默认 0.80；预设 0.70/0.80/0.85）
# max_output_tokens = 0            # 自动：官方 DeepSeek 空间充足时省略字段（服务端 384K），临界时裁剪
# max_output_tokens = 32768        # 可选控费上限，仍可按物理剩余继续下调
# max_output_tokens = 65536        # 可选控费上限
# max_output_tokens = -1           # 明确省略 wire 字段；已知自动预算放不下时压缩
# max_output_tokens 不参与 compact_ratio；0 是 Provider 自动值，不再表示“跳过本地检查”

[[providers]]
name        = "deepseek-flash"
kind        = "anthropic"
base_url    = "https://api.deepseek.com/anthropic"
model       = "deepseek-v4-flash"
api_key_env = "DEEPSEEK_API_KEY"
web_search  = true
# 还有预设：deepseek-pro

[tools]
enabled = []   # 省略/为空 = 全部内置工具
bash_timeout_seconds = 120   # 前台安全上限；设为 0 表示不设工具层超时
mcp_startup_timeout_seconds = 30   # 后台 initialize + tools/list 安全上限
mcp_call_timeout_seconds = 300   # MCP 调用默认安全上限；可用 plugin/tool 覆盖

[environment]
enabled = true   # 启动时把 OS、shell 和常见工具摘要稳定注入 prompt
offline = false  # 无出站网络时设为 true，避免 agent 无效重试网络请求
# [environment.tools]
# go = "/opt/homebrew/bin/go"   # 可选：显式可信路径；workspace 内路径不会在启动时自动执行

[skills]
# paths = ["~/my-skills", "../shared/skills"]   # 额外的自定义技能目录
# excluded_paths = ["~/.agents/skills"]         # 隐藏约定来源，不删除目录
# disabled_skills = ["review"]                  # 隐藏技能，直到 /skill enable <name>

[permissions]
mode  = "ask"                                # 无规则命中时 writer 的兜底：ask|allow|deny
deny  = ["Bash(rm -rf*)", "Bash(git push*)"] # 任何模式下都硬阻断
allow = ["Bash(go test:*)"]                  # 从不询问

[sandbox]
# workspace_root = ""          # 文件写工具被限制在此目录；留空 = 当前目录
# allow_write    = ["/tmp"]    # write_file/edit_file/multi_edit/move_file 额外可写的目录
# forbid_read    = ["${HOME}/.ssh"]   # agent 不可读取或列出的路径

[serve]
auth_mode = "none"             # none|token|password；绑定到非 localhost 前请先开启认证
# token = ""                   # 可选固定 token；token 模式为空时启动时自动生成
# password_hash = ""           # 用 reasonix serve --hash-password --password '...' 生成
# behind_proxy = false         # 只在可信反向代理后方设为 true

[[plugins]]
name    = "example"
command = "reasonix-plugin-example"
startup_timeout_seconds = 60   # 可选：initialize + tools/list 上限
call_timeout_seconds = 600   # 可选：单个 MCP server 的调用超时
tool_timeout_seconds = { "generate_video" = 1800 }   # 可选：raw MCP tool 名称
```

完整 schema 与每个字段的契约见 [`SPEC.md` §5](./SPEC.md#5-configuration-toml)。

已安装或由项目配置声明的 MCP server 不需要逐工具信任名单。独立双模型 Planner 可使用所有
非 destructive 工具，即使 server 没有声明 `readOnlyHint`；严格只读 subagent 仍要求
`readOnlyHint: true` 且无 `destructiveHint`。

`[agent].plan_mode_read_only_commands` 也继续参与配置 round-trip，但主 Plan 工作流不再维护独立的
bash allowlist 或信任提示。Plan 与常规模式使用相同的 Permissions 规则做 bash 分类和审批；Sandbox
仍是文件系统、进程和网络的强制边界。独立 planner 和显式只读 subagent runner 继续使用自己的严格
只读工具 registry 与前台命令分类器。

### 环境变量

多数日常设置应写在 `config.toml` 或前文提到的 Reasonix 全局 `.env` 中。下面这些变量是进程级高级开关；
需要在启动 Reasonix 之前设置。项目 `.env` 不是 Reasonix 控制变量的运行时来源。

### CLI 上报统计

CLI 可以向 `https://crash.reasonix.io` 发送每日最多一次的匿名活跃安装 ping，
以及有界、完全不含内容的事件计数。使用以下用户全局命令配置：

```bash
reasonix config telemetry          # 查看当前生效模式
reasonix config telemetry auto     # 默认：仅本机交互式 TTY
reasonix config telemetry on       # 也允许本机 headless `reasonix run`
reasonix config telemetry off      # 关闭并删除待发送计数文件
```

正式版 CLI 第一次在符合条件的交互式终端启动时，会先明确说明数据边界，并在任何
telemetry 请求之前只询问一次。提示为 `[Y/n]`：直接回车、输入 `y` 或 `yes` 会保存为
`auto`；输入 `n` 或 `no` 会保存为 `off` 并删除待发送计数。选择保存后不再提示，允许的
后续上报保持静默。如果偏好设置保存失败，则不会上传任何内容。

在 CI、开发构建中始终关闭；设置 `DO_NOT_TRACK` 或
`REASONIX_TELEMETRY=0` 也会关闭。`auto` 模式下，重定向、pipe 或其他非交互会话
不会上报。尚未保存选择时，这些不符合条件的会话既不会提示，也不会上报。授权后的
网络失败完全静默，不会改变 stdout、stderr 或进程退出码；未发送计数只会保存在有
数量和时效上限的本地队列中，等待后续启动重试。

ping 包含一个 CLI 专用的随机 128-bit 安装 ID、CLI 版本、OS、架构和 `cli` surface
标记。计数批次使用同一个 ID 做每日活跃安装去重，只包含固定 bucket，例如 CLI 模式、
运行配置档、权限/会话模式、turn 延迟、finish reason、cache hit 区间、通用
Provider/工具错误分类、compaction、恢复计数和归一化界面语言。这个 ID 与桌面端安装
ID 分离，不是账号、硬件、仓库或 session 标识。

Reasonix 绝不会上传 prompt、回答、reasoning、工具名/参数/输出、路径、仓库/分支、
session ID、精确 token/费用、Provider/model 名称、base URL 或环境变量。

### CLI 崩溃报告

当未处理的 Go panic 到达 CLI 入口调用栈时，Reasonix 会把脱敏报告保存在
`<Reasonix home>/cli-crash-reports`。最多保留 10 份，文件权限仅限当前用户读取。
panic 原文绝不会被序列化；绝对源码路径会变成 `<path>/<file>.go:<line>`，函数参数会被
移除，并且在本地保存和实际发送前都会再次清理密钥、token、邮箱及长标识符。

崩溃报告绝不会自动上传。使用以下命令审阅和管理：

```bash
reasonix report                 # 预览最新报告；TTY 中询问后才发送
reasonix report list            # 列出本地报告
reasonix report show [ID]       # 仅预览，不发送
reasonix report send [ID]       # 明确发送；成功后才删除本地副本
reasonix report delete [ID]     # 不发送，直接删除
```

通过 pipe 或重定向运行 `reasonix report` 时只会预览，不会询问或发送。CLI telemetry
设置不会自动发送或自动删除这些
需要单独审阅的报告。Go 无法恢复 runtime fatal throw、操作系统强制终止，以及未包装
后台 goroutine 中的 panic，因此这些情况不会生成本地报告。

## Web 前端

本机使用时，`reasonix web` 会启动浏览器 UI，并自动用默认浏览器打开。也可以在 CLI 交互会话中
执行 `/web`：Reasonix 会保存当前会话、恢复终端，然后打开明确的
`/sessions/<id>#token=...` 深链。即使会话尚未产生第一轮消息，也会延续已预留的 Session ID，
同时继续保持“空会话不提前写 transcript”的惰性落盘行为。

```bash
cd your-project
reasonix web
```

如果想启动前台 Web 服务并打印地址、但不自动新开浏览器标签页，可使用
`reasonix web --no-open`。底层的 `reasonix serve`
默认不会打开浏览器，继续用于远程开发机、进程托管、tunnel、反向代理和需要认证分享的场景。

`reasonix web` 从 `127.0.0.1:8787` 开始监听；端口占用时会依次尝试 8788、8789……，
最多递增重试 100 次。它默认启用自动生成的 Token，即使配置中的 `[serve].auth_mode`
是 `none` 也一样。每个运行实例都会在 `<Reasonix home>/server/instances/` 下写入自己的
单写者 heartbeat 文件；正常退出时只删除自己的文件，新实例则会惰性清理已确认进程死亡的记录。
因此多个 Web 实例可以共用同一个 Reasonix home，而不会相互覆盖登记状态。服务保持在前台运行，
按 Ctrl-C 停止。

显式传入 `reasonix web --auth none` 可以关闭默认 Token，只应在监听地址确定可信时使用。
`reasonix serve` 则保持向后兼容：默认监听 `127.0.0.1:8787`，认证模式仍由配置决定，空配置为
`auth_mode = "none"`。如果要绑定到非 loopback 地址、通过 tunnel 暴露，或放到反向代理后面，
请先开启认证再分享 URL：

```bash
reasonix serve --auth token
reasonix serve --addr 0.0.0.0:8787 --auth token
reasonix serve --auth password --password 'temporary-password'
```

Token 模式会在终端打印带 `#token=...` 的分享链接；Web 页面会先将 fragment 换成
HttpOnly Cookie，再启动 API 与 SSE 请求，从而避免 Token 进入请求 URL、浏览器历史、
Referrer 和访问日志。可通过 `--token` 或 `[serve].token`
复用固定 token。Password 模式必须在启动时传 `--password`，或在配置里保存 bcrypt hash：

```bash
reasonix serve --hash-password --password 'strong-password'

# <Reasonix home>/config.toml
[serve]
auth_mode = "password" # none|token|password
password_hash = "$2a$12$..."
behind_proxy = true    # 仅可信反向代理后方使用
```

Web UI 提供聊天、工具审批、会话历史、rewind/fork/summarize、模型与 reasoning effort 控件、
Goal、由 `todo_write` 工具驱动的实时 Todo 面板、扩展发布的 status/card/form/notification
界面，以及已配置 provider 的余额显示。扩展提供的模型也会进入模型选择器。Serve 可以同时维持
多个活动会话：新建或恢复其他会话时，正在执行的回合会转入后台而不是被取消，会话列表也会持续显示
其运行状态。空闲时运行 `/reload` 可在不重启 Serve 的情况下，以失败原子方式重载扩展 Sidecar
和运行时 generation。临时启动可用 `--model`、`--max-steps` 或 `--resume`；不传
`--model` 时，`serve` 使用用户全局 `default_model`。

如果当前 Provider 尚未保存 API Key，绑定在回环地址的 Serve 仍会启动，并先显示 Provider
配置页，而不是在浏览器连接前直接失败。通过 Serve 认证后可在该页输入 Key；Reasonix 会以受限
权限写入**当前主机**的全局凭据文件，在同一进程内重建 Controller，然后进入正常 Web UI。
凭据写入接口在非回环监听器上始终禁用。对于 SSH 远程窗口，“当前主机”指经 SSH 隧道访问的
远端主机；Key 不会从桌面本机自动复制过去。

## 通过 ACP 接入编辑器

`reasonix acp` 把 Reasonix 作为 ACP v1 stdio agent 提供给编辑器和其他 host 客户端。
独立的 **[ACP 编辑器接入](./ACP.zh-CN.md)** 文档集中说明启动方式、能力协商、会话生命周期、
彼此独立的模型/工作/协作/审批控制轴、客户端文件与 terminal 能力、MCP server、权限请求，
以及 Reasonix 的回合中引导扩展。

## 远程 SSH

远程模块让 Reasonix 在远端主机上运行,并通过你自己的 SSH 连接访问它 —— 即 VS Code
Remote-SSH 式的体验。它在远端主机上引导一个常驻的 headless `reasonix serve`,把本地一个
回环端口转发过去,再经隧道打开现有的 serve Web 客户端。agent、工具与文件全部原生运行在远端
主机上,保真度 100%,不经过有损的文件代理。V1 支持 Linux 与 macOS 远端主机。

独立的 **[远程会话系统](./REMOTE_SESSIONS.zh-CN.md)** 文档集中说明主机配置(`config.toml`
的 `[remote]` 段)、`ssh -G` 解析与别名导入、`reasonix remote` CLI、远端 serve 引导与
安装阶梯、远程会话生命周期与接管、桌面端远程工作、`remote` 与 `local-proxy` 凭据模式、
连接故障语义与故障排查。

## 自定义 OpenAI-compatible provider

在桌面端打开 **设置 -> 模型 -> 接入 -> 添加模型服务 -> 自定义供应商**，用于接入代理、
聚合平台或自建 OpenAI-compatible chat API / Anthropic-compatible Messages API 服务。

常用服务优先使用 **添加模型服务 -> 推荐预设**。新建的官方 DeepSeek provider 默认使用
Anthropic-compatible Messages 端点，并开启 provider 侧 `web_search`；两种协议都复用同一个
`DEEPSEEK_API_KEY`。启动时，Reasonix 会自动升级仍使用官方端点、标准密钥和标准模型设置且
未修改过的旧 `deepseek-flash` / `deepseek-pro` 条目。修改过的官方 Chat Completions 配置保持
原样，设置页会提供 **升级到推荐协议** 操作。代理地址、自定义 Headers、模型列表和能力覆盖
都不会自动迁移。已有单独命名的 `deepseek-anthropic` 条目继续兼容，但新增
接入不再展示这个重复预设。Reasonix 还可以预填以下可编辑的自定义 provider：
Kimi CN、Kimi Global、Kimi Coding Plan、MiMo API、MiMo Anthropic、MiMo Token Plan
CN/SGP/AMS 及其 Anthropic-compatible 变体、MiniMax CN/Global API、MiniMax
CN/Global Anthropic、GLM CN、Z.AI Global、GLM/Z.AI Coding Plan 的
OpenAI-compatible 与 Anthropic-compatible 端点、OpenCode Go、OpenCode Go
Anthropic、OpenCode Go DeepSeek Anthropic、OpenCode Go DeepSeek Responses、
OpenCode Zen Anthropic、Qwen/DashScope CN/Global、
Qwen Coding Plan
CN/Global 的 OpenAI-compatible 与 Anthropic-compatible 端点、StepFun
OpenAI-compatible 与 Anthropic-compatible 端点、NovitaAI、GMI Cloud、Vercel AI
Gateway、HuggingFace Router、ModelScope、NVIDIA NIM、KiloCode 和 Ollama Cloud。Plan 表示
访问/付费形态；只有服务商确实提供不同区域端点时，预设名才同时带 CN/Global。
因此 Kimi Coding Plan 是独立 plan 端点，Kimi 直连 API 才拆成 CN 和 Global。
预设路径通常只需要填写服务商 API Key：真实 key 会写入 Reasonix home `.env`，
`config.toml` 只保存端点、模型列表、key 环境变量名、上下文窗口、模型能力元数据、
中国区端点直连、MiniMax `reasoning_split`、GLM/MiniMax thinking heuristic、
Anthropic-compatible 网关需要的 Bearer 认证、Ollama Cloud max-effort 支持，
以及 OpenCode Go 的每模型 reasoning 覆盖。官方 DeepSeek 的 Anthropic、Responses 与
Chat Completions 目录还会带上多模态 SKU `deepseek-flash` 与 `deepseek-v4-flash-vision-exp`。设置页会按模型能力元数据
展示支持图片的模型，也提供逐模型“图片输入：自动 / 开启 / 关闭”。中转站只返回
模型 ID 时会显示“图片能力未识别”；向服务商确认支持后，选择开启并保存即可。
详见[图片输入指南](MODEL_CAPABILITIES.zh-CN.md#中转站模型使用指南)。
composer/`@` 用户图片会按官方文档的三种方式发出：本地小图走内联 base64 `data:` URL；
`http(s)` 图片链接原样作为 URL 传入；`file-api-` 引用走 Files API（官方 DeepSeek 上
超过 32 MiB 的本地图会自动上传）。Chat Completions 用 `image_url` 或 `file`，Anthropic
用 `image`+`source.base64|url|file`，Responses 用 `input_image`。Flash/Pro 即使旧配置列出
为视觉模型，线上仍是纯文本，工具截图也不会作为图片块转发。
视觉 SKU 使用 Flash 价卡。专用的 OpenCode Go DeepSeek Anthropic 与
DeepSeek Responses 预设接入已验证的 Flash 线路，并默认启用 provider 侧 `web_search`；
Responses 变体使用无状态上下文回放。原有混合 OpenCode Go Anthropic 预设仍只包含 Qwen
与 MiniMax，避免把服务端搜索工具发送给未验证模型。DeepSeek Pro 暂时仍只放在 Chat
Completions 预设中，因为真实 Anthropic
和 Responses 请求目前会在 OpenCode Go 的上游转换阶段失败。OpenCode Go 预设原生包含
订阅线路的 `kimi-k3`，并配置图像输入、`high`/`max` 推理强度和 1,048,576 token 上下文窗口。未修改过
模型目录的既有 OpenCode Go 预设会自动升级；用户编辑过的模型目录保持不变。
Kimi CN 和 Kimi Global 直连 API 预设也包含 `kimi-k3`，支持图像输入、1,048,576 token
上下文窗口以及官方 `low`/`high`/`max` 推理强度（默认 `max`）。对官方 K3 端点，Reasonix
会在多轮请求中保留完整 assistant message，使用 `max_completion_tokens` 传递输出上限，
并省略 K3 的固定采样参数。未修改过的旧版 Kimi 直连模型目录会自动升级且不会改变默认模型；
自定义模型目录和端点保持不变。添加后仍然可以打开 provider 卡片，继续修改模型、请求头、
端点或兼容设置。

**API 地址** 填写服务端点。默认模式下，Reasonix 会预览并把聊天请求发送到：

```text
<API 地址>/chat/completions
```

如果服务商给的是完整请求 URL，例如 `https://gateway.example.com/v1/chat/completions`，
开启 **完整 URL**。开启后 Reasonix 会直接使用该地址，不再追加 `/chat/completions`。
输入框下方的预览就是最终请求地址。

模型发现会基于 API 地址尝试 `/models`、`/v1/models` 等候选地址。如果网关要求单独的
模型列表端点，在 **兼容设置** 中填写 `models_url`，例如
`https://gateway.example.com/v1/models`。如果接口不支持模型发现，也可以手动填写模型列表。

**完整 URL** 仍使用 OpenAI-compatible chat 请求体；它不会切换成 OpenAI Responses API
的请求 schema。

### 兼容设置

**兼容设置（通常不用改）** 用于处理认证变量、模型发现地址、请求头、以及 reasoning/thinking
请求格式和普通 OpenAI-compatible 默认行为不一致的网关。除非服务商文档明确要求，或代理报错说明
不兼容，否则保持默认值即可。Kimi Coding Plan、MiniMax CN/Global Anthropic 这类 Anthropic-compatible 服务，
保存前在基础区域把接入协议切到 **Anthropic-compatible**。

| 字段 | 作用 | 什么时候改 |
| --- | --- | --- |
| `api_key_env` | 该 provider 使用的 API key 环境变量名。桌面端保存的真实 key 会写入 Reasonix home `.env` 的同名变量；TOML 配置里只保存变量名。 | 多个 provider 需要不同 key 时改名；服务不需要 API key 时可以留空。 |
| `models_url` | 只用于自动发现模型列表的 URL。聊天请求仍使用上方的 API 地址或完整 URL。 | `/models` 或 `/v1/models` 不是该网关模型列表地址时填写。 |
| 额外请求头 | 静态 HTTP header，一行一个 `Header: value`。 | OpenRouter 等网关要求 `HTTP-Referer`、`X-Title` 或类似站点来源 header 时使用。API key 仍放在上方密钥字段，不要重复写到这里。 |
| 额外请求体 | 合并到聊天请求体顶层的 JSON 对象。 | 仅用于服务商专用开关，例如 `{"enable_thinking": true}`。`model`、`messages`、`tools`、`stream`、`thinking` 等核心字段仍由 Reasonix 控制，且不接受 `null` 值。 |
| Authorization: Bearer | 对 Anthropic-compatible provider，把已保存的 API key 用 `Authorization: Bearer <key>` 发送，而不是 `x-api-key`。 | MiniMax Global、Vercel AI Gateway 等网关文档明确要求 Bearer 认证时开启。 |
| 模型能力模式 | 指定 Reasonix 对该 provider 使用哪种 reasoning 请求协议。 | 默认用“自动识别”。只有网关被误判，或模型文档要求特定 reasoning 格式时再切换。 |
| Thinking 覆盖 | provider 专用的 `thinking.type` 覆盖项。 | 默认用 Auto。只有后端文档明确支持 `enabled`、`disabled` 或 `adaptive` 时再手动指定；不支持的值可能让中转站拒绝请求。 |
| 余额查询 URL | 可选的钱包余额查询接口。 | 服务商提供余额接口，且希望桌面端状态栏显示余额时填写。 |
| 上下文窗口 | Reasonix 用于自动清理上下文的 provider 级 token 预算。`0` 表示禁用自动 compaction。 | 按该 provider 的模型上下文上限填写；所选模型规格不同时使用下方的逐模型覆盖。 |

每个已选模型还提供一个可选的 **上下文窗口** 输入框。留空时继承 provider
级设置；填写正整数时只覆盖该模型。这样，同一端点下的长上下文模型不会过早
compaction，短上下文模型也不会在 Reasonix 清理前被服务端拒绝。
这里应填写模型文档标注的上下文窗口，而不是最大输出 token。例如 128K 通常填
`128000`；如果服务商明确标注 `131072`，则按该精确值填写。小于 16384 时界面会
显示非阻断警告，因为过小的窗口可能导致频繁 compaction 并降低缓存命中率。

模型能力模式选项：

| 选项 | 作用 |
| --- | --- |
| 自动识别（推荐） | Reasonix 根据模型能力元数据和端点自动选择请求格式。 |
| DeepSeek 思考 | 使用 DeepSeek 风格的 thinking 控制，包括 `thinking.type` 和 DeepSeek 支持的推理深度。 |
| OpenAI reasoning | 使用标准 OpenAI-compatible 的 `reasoning_effort` 档位。 |
| 普通聊天（不发送思考参数） | 不发送 reasoning 或 thinking 控制字段。适合会拒绝 reasoning 参数的普通文本代理。 |

Thinking 覆盖选项：

| 选项 | 作用 |
| --- | --- |
| Auto（使用服务默认） | 不写 provider 级 `thinking` 覆盖，让 Reasonix 使用 provider/model 默认行为。 |
| Enabled（开启） | 对兼容 provider 发送 `thinking.type = "enabled"`。 |
| Disabled（关闭） | 对兼容 provider 发送 `thinking.type = "disabled"`。DeepSeek 风格 provider 下还会避免继续发送推理深度提示。 |
| Adaptive（自适应） | 仅在服务文档明确支持 adaptive thinking 时使用，例如 MiniMax-M3 风格端点；语义是发送或保留 `thinking.type = "adaptive"`。 |

## 按项目的用户 ID（`cachecontext`）

为了让每个项目单独归属（DeepSeek 的 KV 缓存隔离 / 滥用追踪），请在项目级配置里设置顶层
`cachecontext`，而不必重复 provider 条目。它会作为 DeepSeek `user_id` 发送（Anthropic 端点下是
`metadata.user_id`，OpenAI 端点下是 `user`）：

```toml
cachecontext = "my-project"
```

该值必须匹配 `^[a-zA-Z0-9_-]+$`（不超过 512 个字符），否则 DeepSeek 会返回 HTTP 400。桌面端可在
项目标签页的 **项目** 设置区里设置它，全局设置中不提供该字段。

当某个工作区未设置 `cachecontext` 时，Reasonix 会从 `<unix-user>:<repo-path>` 发送一个自动推导的值
（例如 `alice:/home/alice/my-project`），并按同样的 `^[a-zA-Z0-9_-]+$` 规则清洗，因此实际发送的 id 是
`alice--home-alice-my-project`。超长路径会保留 `<user>-` 前缀与路径尾部，并对超出的中间部分做哈希，
使值始终不超过 512 个字符。该自动值不会写入任何配置文件——只出现在 provider 请求中——因此只有显式
设置 `cachecontext` 才能固定它。

自动值中的用户名按以下优先级解析：仓库本地 `logname`（项目本地配置）、用户全局 `logname`（全局配置）、`user`
（`logname` 的旧别名）、`$LOGNAME` 环境变量、系统账户。若 `logname` 与 `user` 同时设置，
`logname` 生效并记录一条加载警告。

## 快捷键

这里按使用端来写，因为用户通常是先知道“我现在在桌面端/CLI”，再找对应按键。
桌面端仍用 `Shift+Tab` 切换 Plan；CLI 则用它在 Ask、Auto、Plan 之间循环。
桌面端默认用 macOS 的 `Cmd+Y` 或 Windows/Linux 的 `Ctrl+Y` 切换 YOLO；
如果在 Windows/Linux 上改绑了 YOLO，`Ctrl+Y` 会成为输入框的标准重做兼容键。
桌面端粘贴继续走系统快捷键；CLI 则把终端原生文本粘贴和应用接管的图片粘贴拆成不同快捷键。

`[ui].shortcut_layout` 仍被接受以兼容旧配置，但下面的快捷键行为已经跨布局统一。

CLI/TUI 文本输入可通过 `[ui].cursor_shape` 设置光标形状，支持 `underline`、`block`
和 `bar`。默认值是 `bar`：位置清晰，同时不会在中英混排输入时覆盖 CJK 双宽字符。
想使用传统终端块状光标可设为 `block`，偏好更弱的下划线光标可设为 `underline`。
该设置不影响桌面端或 Web 输入框。

### 桌面端 GUI

桌面端 Todo 面板会同时依据 `todo_write` 和所属标签页的运行态显示状态：真实执行时为「进行中」，
等待审批或回答时为「等待输入」，回合空闲或恢复历史后为「待继续」。后者提供「继续」按钮；发送前
会再次核对创建该按钮的标签页，因此快速切换标签页不会把旧待办误发到另一个会话。

桌面端快捷键在 **设置 → 快捷键** 中管理。选择可配置的行后按下新的组合键，Reasonix 会为桌面端保存该绑定。
撤销、重做等标准编辑快捷键会以锁定行展示，因为 WebView 的原生文本历史依赖这些平台组合键。
如果新组合键和已有动作冲突，会拒绝保存，避免一个快捷键触发两个动作。按 `?` 或点击 topic bar
里的帮助按钮可打开快捷键帮助表；帮助表由同一份快捷键 registry 生成，因此会同步显示自定义后的绑定。

全局快捷键：

| 按键或控件 | 作用 | 说明 |
| --- | --- | --- |
| macOS `Cmd+K`，Windows/Linux `Ctrl+K` | 打开或关闭命令面板 | 打开时会聚焦搜索框；`Esc` 关闭命令面板。 |
| macOS `Cmd+,`，Windows/Linux `Ctrl+,` | 打开设置 | 在设置里的 **快捷键** 页可自定义桌面端绑定。 |
| macOS `Cmd+W`，Windows/Linux `Ctrl+W` | 关闭当前顶部标签页 | 最后一个标签页仍由原有关闭保护保留。 |
| `Cmd+B` / `Ctrl+B` | 显示或隐藏左侧边栏 | 和点击侧边栏开关是同一个动作。 |
| `Cmd+Shift+B` / `Ctrl+Shift+B` | 展开或收起最近的 shell 输出 | 和点击折叠 shell 输出提示是同一个动作。 |
| macOS `Cmd+1`-`Cmd+9`，其它平台 `Ctrl+1`-`Ctrl+9` | 跳转到侧边栏中对应编号的可见对话 | 短暂按住 `Cmd`/`Ctrl` 会显示编号标记；已有自定义快捷键占用相同按键时，自定义动作优先生效。 |
| macOS `Cmd++`、`Cmd+-`、`Cmd+0`；其它平台 `Ctrl++`、`Ctrl+-`、`Ctrl+0` | 放大、缩小或重置文字大小 | 对把加号上报为 `=` 的键盘也兼容。 |
| `?` | 打开键盘快捷键帮助表 | 帮助表显示当前实际生效的桌面端绑定。 |

输入框快捷键：

| 按键或控件 | 作用 | 说明 |
| --- | --- | --- |
| `Enter` | 发送当前消息 | IME 组合输入确认不会被截获。 |
| `Shift+Enter` | 插入换行 | 输入框保持焦点。 |
| `Shift+Tab` | 切换 Plan 开/关 | Plan 只改变“先规划”的工作流；内置 writer 仍走当前 Ask/Auto/YOLO 与 Sandbox，MCP writer/destructive 目标在整个规划阶段保持硬阻断。 |
| macOS `Cmd+Z`，Windows/Linux `Ctrl+Z` | 撤销输入框中的最近一次编辑 | 普通键入继续由 WebView 原生历史管理；Reasonix 接管的粘贴、剪切、折叠块和结构化 token 会作为完整事务恢复。 |
| macOS `Cmd+Shift+Z`，Windows/Linux `Ctrl+Shift+Z` | 重做输入框中的最近一次编辑 | Windows/Linux 改绑 YOLO 后也可使用 `Ctrl+Y`。 |
| `Cmd+Y` / `Ctrl+Y`（默认） | 切换 YOLO 开/关 | 关闭 YOLO 时会尽量恢复之前的 Ask/Auto 基底；当前绑定可在 **设置 → 快捷键** 查看。 |
| macOS `Cmd+V`，Windows/Linux `Ctrl+V` | 粘贴剪贴板内容 | 剪贴板图片会作为附件加入；图片也可以拖进输入框。官方 DeepSeek 的 `deepseek-flash` 与 `deepseek-v4-flash` 原生支持图片；V4 Pro 仍是纯文本。 |
| 输入边界处的普通 `Up` / `Down` | 回放更旧或更新的已提交提示词 | 带修饰键的方向键和原生文本导航仍交给 textarea。 |
| 运行中按 `Esc` | 取消当前 turn | 如果后端尚未开始回复，会恢复草稿。 |

菜单与控件：

| 按键或控件 | 作用 | 说明 |
| --- | --- | --- |
| 斜杠、`@` 或 past-chat 菜单中的 `Up` / `Down` | 移动高亮项 | past-chat 搜索框使用同一套导航键。 |
| 这些菜单中的 `Enter` / `Tab` | 接受高亮项 | 类似目录的条目可能继续打开下一层菜单。 |
| 这些菜单中的 `Esc` | 关闭当前菜单或退出 past-chat 搜索 | 关闭后可继续正常输入。 |
| Ask / Auto / YOLO 审批控件 | 直接选择工具审批姿态 | 点击操作不受快捷键规则影响。 |
| 工具审批卡片 | `Left` / `Right`、`Enter`、`1`-`4`、`Esc` | 移动高亮动作、确认当前高亮、直接选择编号动作，或拒绝。默认高亮是“允许一次”。 |
| 计划审批卡片 | `Left` / `Right`、`Enter`、`1`-`3`、`Esc` | 在“修改计划 / 开始执行 / 退出计划”之间移动。默认高亮是“开始执行”。 |
| Plan 控件 | 切换 Plan 开/关 | 和 `Shift+Tab` 是同一个模式。 |
| 协作菜单里的 Goal | 启动、查看或清除 Goal | Goal 不进入任何快捷键循环。 |

### CLI / TUI

输入框上下边线使用当前主题强调色，默认光标为细竖线。长草稿会增长到可用的最大高度；
超过后，在输入框内滚轮只滚动草稿视图，不移动插入光标，在 transcript 区域滚轮仍滚动
对话。使用 `/theme auto|light|dark` 选择背景模式，也可运行不带参数的 `/theme` 查看
命名配色，再用 `/theme <style>` 选择强调色。

响应式底栏左侧保留当前 Ask/Auto/Plan 或 YOLO 姿态和交互状态；终端较宽时，模型、推理
强度作为一组靠右显示，第二行按可用性显示 Git 标识、缓存命中率、上下文占用、
压缩余量、后台任务和余额。“就绪”只表示输入框空闲，并不是模型健康检查；选择器、审批、
图片粘贴、shell 模式等活动会替换这个状态。窄终端会按完整信息组移动、换行或压缩。
标签和展示用的语言跟随 `/language`。

聊天与 transcript：

| 按键或命令 | 作用 | 说明 |
| --- | --- | --- |
| `Enter` | 发送当前消息 | turn 运行中输入非空内容时，会排队作为后续反馈。 |
| `Shift+Enter`、`Alt+Enter` 或 `Ctrl+J` | 插入换行 | 普通 `Enter` 保留给发送/确认。 |
| 空闲时普通 `Up` / `Down` | 回放更旧或更新的已提交提示词 | turn 运行中同一组按键用于导航排队反馈。 |
| `PageUp` / `PageDown` | 滚动 transcript | 不受当前聊天状态影响。 |
| `Ctrl+Home` / `Ctrl+End` | 跳到 transcript 顶部或底部 | 长工具输出后很有用。 |
| `Ctrl+L` 或 `/cls` | 只清空可见 transcript | LLM 上下文、session 文件、工具、记忆和插件都保持加载；想丢弃对话上下文时用 `/clear`。 |
| `Esc` | 退出当前最具体的动作 | 可在无回复前撤回刚提交的 turn、取消运行中的 turn，或清空非空输入。 |
| 空闲且输入为空时双击 `Esc` | 打开 rewind 选择器 | 和 `/rewind` 是同一个入口。 |
| transcript 文本选择 | 复制 transcript 文本 | 应用内拖选松开后，本地会话通过可验证的系统剪贴板路径写入（macOS `pbcopy`、Linux 可用的 Wayland/X11 工具、Windows 系统剪贴板）；SSH 才回退到 OSC 52，并明确标记为回退而不是宣称原生复制成功。`Ctrl+C`/`Super+C`/`Meta+C` 或右键当前选区可再次复制。 |
| 输入框文本选择 | 选中、复制或替换草稿文本 | 应用内拖选松开后，会通过与 transcript 相同的可验证剪贴板路径复制；输入或粘贴会替换选区，方向键会收起选区。 |
| 没有活动选区时右键 | 在本地会话粘贴剪贴板文本 | 本地会话开启鼠标接管时，Reasonix 只读取文本并交给正常的 bracketed-paste 处理。SSH 下远端进程无法读取本机剪贴板，请使用终端粘贴快捷键；`/mouse` 可恢复终端原生右键菜单。存在活动选区时，右键仍优先复制该选区。 |
| `/mouse` | 切换应用内鼠标接管 | 关闭后由终端处理原生拖选和右键菜单，但会失去应用内选区、滚动条和滚轮。可用 `REASONIX_DISABLE_MOUSE=1` 让每次会话默认关闭。SSH 远程会话默认即关闭，保证原生拖选/复制可用；`REASONIX_DISABLE_MOUSE=0` 可强制全局开启。SSH 下 TUI 还会开启同步输出（mode 2026）避免远端回传时整帧重绘闪烁；如需关闭可设置 `REASONIX_DISABLE_SYNC_OUTPUT=1`。 |
| `Ctrl+C` | 复制、取消、清空或退出 | 有 transcript 或输入框活动选区时优先复制；否则取消运行中的 turn、清空非空输入，或在空输入下连按两次退出。 |
| `Ctrl+D` | 退出 TUI | 立即退出。 |
| 终端的文本粘贴快捷键 | 粘贴文本 | 文本保持终端原生 bracketed-paste 路径：macOS 通常是 `Cmd+V`，Linux 通常是 `Ctrl+Shift+V`，其它环境使用终端自身配置。Reasonix 只消费收到的文本粘贴事件，不会先探测图片。 |
| macOS/Linux `Ctrl+V`；Windows `Alt+V` | 粘贴剪贴板图片 | 图片粘贴是独立的应用动作。读取期间底栏显示“正在粘贴图片…”，完成后在光标处插入可编辑的 `[image #N]` 标记。 |
| `/paste-image` | 粘贴剪贴板图片 | 与图片快捷键相同的纯图片命令入口。 |
| 以 `!` 开头的一行 | 直接运行 shell 命令 | 命令在本地执行，不经过模型。 |

模式与显示：

| 按键或命令 | 作用 | 说明 |
| --- | --- | --- |
| `Shift+Tab` | 按 Ask → Auto → Plan → Ask 循环 | YOLO 不进入这个输入模式循环；底部状态栏会显示当前模式。 |
| `Ctrl+Y` | 切换 YOLO 开/关 | 关闭 YOLO 时会尽量恢复之前的 Ask/Auto 基底。终端若能转发 Command/Super，也可能识别 `Cmd+Y`，但稳定可用的是 `Ctrl+Y`。 |
| `--yolo`、`--dangerously-skip-permissions` | 启动时进入 YOLO | 和 `Ctrl+Y` 是同一个运行时模式。 |
| `/theme [auto|light|dark|style]` | 查看或切换 CLI 主题 | 不带参数会列出背景模式和命名配色。选择会保存到用户配置；单次运行可用 `REASONIX_THEME` 和 `REASONIX_THEME_STYLE` 覆盖。 |
| `Ctrl+O` | 切换详细 reasoning 显示 | 也可通过 `/verbose` 使用。 |
| `Ctrl+B` | 展开或收起较长 shell 输出 | 较长 shell 输出的提示行也可点击；全屏 TUI 开启鼠标接管时，文本选区由应用内处理。 |
| `/goal <目标>`、`/goal status`、`/goal pause`、`/goal resume`、`/goal clear` | 启动、查看、暂停、恢复或清除 Goal | Goal 默认持续执行；只有用户显式预算会按数字暂停。 |
| `/migrate`、`/migrate --from <旧目录>` | 重试旧数据迁移，或从指定 v0.x 来源导入 sessions | Windows v0.52 自定义安装/数据目录用 `--from`；该形式只导入 sessions。详见[配置路径](./CONFIG_PATHS.zh-CN.md)。 |

选择器与审批：

| 上下文 | 按键 | 作用 |
| --- | --- | --- |
| 斜杠或 `@` 补全 | `Up` / `Down`、`Ctrl+P` / `Ctrl+N`、`Tab` / `Enter`、`Esc` | 移动、接受或关闭补全菜单。 |
| 工具审批提示 | `y`/`1`、`a`/`2`、`p`/`3`、`n`/`4`、`Enter`、`Esc`、`Ctrl+C` | 允许一次、本会话允许、持久允许、拒绝、默认允许一次、拒绝，或取消当前 turn。 |
| Ask 问题卡 | `Up`/`Down` 或 `j`/`k`、`Left`/`Right` 或 `h`/`l`、`Space`、`Enter`、`1`-`9`、`Esc`、`Ctrl+C` | 导航答案/问题标签、切换多选、提交/激活、选择编号选项、关闭，或取消当前 turn。 |
| Rewind 选择器 | `Up`/`Down` 或 `j`/`k`、`Enter`、`b`、`c`、`d`、`f`、`s`、`u`、`Esc` | 选择 turn，应用 both/conversation/code/fork/summarize 动作，或返回/关闭。 |
| 模型、provider 或 Resume 选择器 | `Up`/`Down` 或 `Ctrl+P`/`Ctrl+N`；搜索词为空时可用 `j`/`k`；输入文字过滤；`Enter`；`Esc` | 搜索、选择或关闭选择器；开始搜索后 `j`/`k` 会作为查询字符输入；`/provider` 会继续打开该 provider 的模型列表。 |
| MCP 导入选择器 | `Up`/`Down` 或 `j`/`k`、`Space`、`Enter`、`Esc` / `Ctrl+C` | 移动、勾选服务器、导入勾选服务器，或取消。 |
| MCP 管理器 | `Up`/`Down` 或 `j`/`k`、`Enter`、`Left`/`Right` 或 `h`/`l`、`r`、数字键、`q` / `Ctrl+C` | 导航服务器列表/详情、刷新、选择动作，或关闭。 |
| `/clear` 确认 | 方向键或 `j`/`k` / `Tab`、`Enter`、`y`、`n`、`Esc` / `Ctrl+C` | 在 Clear/Cancel 间切换、确认清空，或取消。YOLO 模式下 `/clear` 直接清空，不再询问。 |

模式含义：

| 模式 | 含义 |
| --- | --- |
| Ask | writer 兜底审批时询问。 |
| Auto | 自动放行兜底审批，包括交互式 `remember`/`forget`；显式 `ask` / `deny` 规则仍生效。 |
| YOLO | 跳过普通工具审批，包括 `remember`/`forget`；`deny`、用户 `ask` 问题和计划批准提示仍会等待。 |
| Plan | 要求模型先规划——这是 plan-first 工作流，不是全部工具只读。内置 writer 仍遵守当前 Ask/Auto/YOLO 与 Sandbox；已安装 MCP writer、destructive 目标与未信任 reader 在整个规划阶段硬阻断（审批不能放行，退出 Plan 后恢复）；`complete_step` 等显式阶段工具需等到计划批准后。 |
| Goal | 持续追一个已保存目标，直到完成、阻塞或清除。 |

## 权限与沙盒

权限逐次调用把关：`deny` > `ask` > `allow` > 兜底。Bash 和文件修改都要审核；
只读工具一般不需要。审核规则不是按“按钮文案”存，而是按权限规则匹配，比如
`Bash(npm run build)`、`Bash(npm run test:*)`、`Edit(docs/**)` 这种形式。
`reasonix` 会在 writer 调用前征求同意（普通工具为 `1` 本次 · `2` 本会话允许此范围 · `3` 总是允许此范围（保存） · `4` 拒绝；Bash 可额外选择命令前缀授权）；
其中 Bash 默认按具体命令记，也可按安全推导出的命令前缀记（如 `Bash(go test:*)`）；文件编辑类工具的本会话授权按编辑能力记，持久授权则写入 `Edit(<path>)` 文件路径规则；
参数/算术展开、赋值、不含嵌套执行的 heredoc、文件重定向和 glob 不能复用裸 `Bash`、前缀或 glob Allow；用户保存时写入整条 `Bash=<literal>`，但它们仍按普通 fallback 执行，因此 Auto 不会额外询问。命令/进程替换、动态命令名、`eval`、`source`、Shell `-c`、运行时内联代码和无法解析的结构默认强制人工；无头 Ask/Auto/DontAsk 会拒绝这类未精确授权的命令，YOLO 可以绕过。高级用户可设置 `[permissions] allow_dynamic_bash = true`，让 Allow fallback（包括 Auto）覆盖这类动态命令；显式 `ask` 与 `deny` 规则仍然优先。由于无头运行没有审批界面，默认 Ask 对普通 writer fallback 和显式 ask 规则也会 fail closed；无人值守自动化需要放行普通 writer 时，使用 `reasonix run --auto ...`、`-y` 或 `--permission-mode auto`。配置的 `ask` 与 `deny` 始终优先。

Ask 不是只读模式：writer 获得批准后仍会执行。Permissions 决定放行或询问，Sandbox 才是强制能力边界。
Sandbox 是授权之后的第二层边界，不能替代命令解析，也不能把无法证明静态安全的命令变成可自动授权命令。

权限是**策略**（哪些调用放行/询问），**沙盒**是**强制**：这是两层机制。已经放行的调用
仍然不能写出已批准的根目录。文件写工具
（`write_file` / `edit_file` / `multi_edit` / `move_file`）拒绝 `[sandbox] workspace_root`
之外的任何路径（默认当前目录，编辑不出项目），并解析符号链接与 `..`，使链接无法
打洞越界。写出工作区时走交互式「扩展写入范围」审批（仅本次 / 本会话 / 写入项目本地配置 / 拒绝），不会退化成无沙箱执行。Bash 必须用 `additional_write_dirs`
加上 `justification` 声明所需目录；宿主不会从命令文本猜测路径。无头 `reasonix run`
不会弹审批：请传 `--add-dir` 或配置 `[sandbox].allow_write`。整个用户主目录可以在
强警告后批准；文件系统根和 Reasonix 会话/状态目录不能通过动态流程批准。`forbid_read` 可选地隐藏敏感文件或目录，使 agent 的读文件、列目录和搜索工具不能读取或列出它们；
建议使用绝对路径或 `${HOME}` / `${VAR}`，不要写 `~`，因为配置只做环境变量展开。
`bash` 本身默认进 OS 沙盒（`[sandbox] bash`：macOS 使用 Seatbelt，Linux 使用 bubblewrap）：
命令只能写这些 root（外加平台按命令提供的临时/缓存 root），
OS 沙盒生效时也不能读取配置的 `forbid_read` roots，`[sandbox] network` 为真时才能联网。
Reasonix 始终会从工具子进程环境中移除已保存的 provider 与 bot 凭据变量，并自动把
全局凭据 `.env` 加入运行时禁读边界；项目 `.env` 仍保持现有的 workspace 范围行为。

**会话私有标准临时目录。**同一逻辑会话内的多条 Bash 命令共享一个私有临时目录，
因此连续调用可以通过 `$TMPDIR` 交换文件（在 Linux bubblewrap 下还可以通过字面
`/tmp`）。用户不需要设置：Reasonix 会自动为 Bash 和客户端托管的 ACP 终端注入
`TMPDIR`、`TMP`、`TEMP`。目录按需创建，不会回退到宿主公共临时目录；在 `/new`、
`/clear`、恢复另一会话、切换分支时旋转。模型或设置热重建会保留同一目录。临时文件
不是持久存储：跨进程 resume 不会恢复其中内容；需要长期保留的数据应写入工作区或
用户指定路径。

Reasonix 生成的脚本和项目脚本应使用标准临时目录变量，不要硬编码 `/tmp`；用户无需
自行设置这些变量。例如：

```sh
tmp_file="${TMPDIR:?}/result.json"
```

```powershell
$tmpFile = Join-Path $env:TEMP "result.json"
```

| 平台 | `$TMPDIR` / `$TMP` / `$TEMP` | 字面 `/tmp` |
| --- | --- | --- |
| Linux + bubblewrap | 虚拟 `/tmp`（绑定到私有目录） | 会话内共享（不再是每次新建的空 tmpfs） |
| macOS Seatbelt | 私有宿主目录路径（Seatbelt 允许写入） | 仍是 macOS 宿主临时目录；脚本应使用 `$TMPDIR` |
| Windows（无 OS 级 Bash 沙箱） | 私有宿主目录路径 | 不保证与该目录等价（例如 Git Bash 的 `/tmp`） |

MCP 等独立沙盒继续使用自己的隔离规范，不继承父会话临时目录。获得批准后绕过沙盒的
命令仍继承私有临时变量，但在 Linux 上其字面 `/tmp` 不再由 bwrap 映射。

**Windows 说明：**Reasonix 不在 Windows 上提供 OS 级 Bash 沙箱，生效模式固定为
`off`。旧配置即使写了 `bash = "enforce"` 也会解析为 `off`，`reasonix doctor`
会提示该设置被忽略，桌面设置中的选择器也为只读。Bash 命令会在不受 OS 沙箱限制的
环境中运行；专用文件工具仍会在进程内执行 `workspace_root`、`allow_write` 和
`forbid_read` 边界。已保存的凭据变量仍不会进入子进程环境，但获得批准的无沙箱 shell
以当前用户身份运行，不能作为保护其他用户可读文件的安全边界。

没有可用 OS 沙盒时，`bash = "enforce"` 会拒绝 bash 执行，不会无沙盒运行。
Windows 上兼容的值始终为 `off`。

反馈编码质量问题时，可运行 `reasonix doctor quality <branch-id-or-path>`（加
`--json` 输出结构化结果）。命令会读取指定 session，但只输出不含内容的计数与
Profile 分类：模型家族、运行模式、协作/审批模式、消息和工具调用数、验证与已持久化的
compaction 摘要数，以及可用时的桌面端 token/cache telemetry。结果不会包含对话正文、
路径、session 标识、工具参数与输出、服务端点或自定义模型名，适合粘贴到公开 Issue
或 Discussion。它不同于 `reasonix doctor session`：后者生成的支持 zip 含完整未脱敏
会话，只能在可信支持渠道分享。

## 能力诊断

当 skill、斜杠命令、Hook、插件包、MCP 或 `AGENTS.md` 缺失、被覆盖或启动失败时，用统一只读诊断。完整参数、JSON schema 与 issue code 见
**[能力诊断](./CAPABILITY_DIAGNOSTICS.zh-CN.md)**。

```bash
# 静态（默认）：无网络、不启动 MCP 子进程
reasonix doctor capabilities

# 机器可读（stdout 仅为合法 JSON）
reasonix doctor capabilities --json

# 指定工作区
reasonix doctor capabilities --root /path/to/project

# Live MCP 探测——仅在你明确允许启动第三方服务器时使用
reasonix doctor capabilities --live --timeout 5s
```

| 入口 | 用法 |
| --- | --- |
| CLI | 见上方 `reasonix doctor capabilities` |
| 桌面端 | **设置 → 诊断** — 刷新、复制脱敏 JSON、可选「包含当前会话运行状态」（只读活动标签 Host，**不**启动 MCP） |
| Agent | `/reasonix-guide`（内置 inline Skill）或自然语言描述症状；优先静态 doctor JSON，再问是否 `--live` |

退出码：`0` 允许 warning/info；`1` 表示存在 `error`（或 live 启动失败）；`2` 为参数错误。与 `reasonix doctor`（provider/沙箱）以及 `reasonix plugin doctor <name>`（单个插件包）相互独立。

## 插件（MCP）

Reasonix 是一个 MCP 客户端。`[[plugins]]` 的 `type` 选择传输：`stdio`（默认）启动本地子进
程（`command`/`args`/`env`）；`http`（Streamable HTTP）连接远程 `url`，可带静态
`headers`（`${VAR}` / `${VAR:-default}` 从环境展开，密钥不入文件）。
`sse` 则兼容仍使用持久 GET 与 server 公布 POST endpoint 的旧版远程 server。

远程 HTTP server 未配置静态 `Authorization` header 时，认证要求会显示为 **登录**。
CLI 可运行 `reasonix mcp auth <name>`，桌面端则在 MCP 面板点击该 server 的 **登录**。
Reasonix 会执行 OAuth 元数据发现、动态客户端注册、PKCE S256 授权与
refresh token 轮换；发现和 token 请求与 MCP 连接使用相同的 Reasonix 网络代理设置。

OAuth client 与 token 状态保存在工作区之外、该 server 私有的 Reasonix 状态目录中，文件权限
为 `0600`，并绑定完整的 resource URL。显式静态 `Authorization` header 始终优先。
**清除认证** 只删除 Reasonix 本地 OAuth 状态，不会退出第三方浏览器会话。Reasonix 仅在用户
主动点击或运行登录命令后打开浏览器，不会因后台工具调用失败而自动弹出浏览器。删除 MCP server
也会删除其本地 OAuth 状态；若删除后有同一 resource 的低优先级声明生效，则保留该状态。

可在 **设置 → MCP 服务器 → 浏览市场** 打开官方 MCP Registry，也可使用
`reasonix mcp browse [query]` 与 `reasonix mcp install <registry-name>`。Registry
只在用户显式浏览或安装时联网，不进入启动路径。需要 secret 或必填参数的条目只显示为手动配置，
不会写入不完整配置；Registry 故障时可回退到同一查询的缓存结果。

普通配置流程现在只有一步：使用桌面端的“添加并连接”、`/mcp add`，或直接让 Reasonix
安装一个 package 或 URL。此类主动安装统一写入用户全局 `config.toml`，安装本身就是授权：
server 会在当前会话连接，现在和下次启动都不会再弹出第二套信任步骤。当前项目
`reasonix.toml` 或 `.mcp.json` 中声明的 server 保留在项目配置中，同样默认可信，不需要额外
启动确认。显式 deny 仍然优先；包括声明
`destructiveHint` 的工具在内都可由普通 Executor 直接执行。独立 Planner 仍拒绝 destructive，
严格只读 subagent 仍只暴露带只读 hint 的非破坏工具。

MCP 名称按 workspace 解析：项目声明覆盖同名全局安装；项目内部以 `reasonix.toml` 高于
`.mcp.json`。编辑会写回当前生效声明的原文件；删除高优先级声明后，会显示并启用下一层同名
声明，而不会顺带删除其他作用域。

stdio server 从初始化到读写都复用同一个进程，因此浏览器等有状态 MCP 能保留会话和
已打开页面。由于进程启动后无法按调用切换 OS 沙箱，这个共享进程始终使用该 server 的普通
进程沙箱；`readOnlyHint` 与只读 subagent 过滤属于调用分发策略，不再对应第二个按调用隔离
的进程沙箱。

工具以 `mcp__<server>__<tool>` 暴露给模型，与 Claude Code 一致；声明 MCP `readOnlyHint: true`
的工具会参与并行调度并命中普通权限层的只读默认放行。用户安装或项目配置声明 server 后，
独立 Planner 即可使用该 server 的全部非 destructive 工具，不再需要逐工具设置；
严格只读研究 subagent 只获得带 `readOnlyHint` 的非破坏 reader。没有 `readOnlyHint` 的工具在调度和
mutation 记账上仍按 writer 处理。计划期间，内置 writer 仍走 Permissions/Sandbox；独立 Planner
允许已授权、非 destructive 的 MCP（包括缺少只读 hint 的 opaque writer），但在任何审批前硬阻断
destructive 或未授权目标；没有独立 Planner 的单模型 Plan 仍维持原有 writer/destructive 阻断。

安装 MCP server 本身就是授权决定。安装完成后，该 server 的所有工具都直接执行，不再存在
server、raw tool、writer 或 destructive 的第二套审批设置；显式全局 deny 规则仍然优先。
`readOnlyHint` 与 `destructiveHint` 只作为内部事实，用于并行调度、Plan 限制、严格只读
subagent 和缓存到实时安全分类复核，不增加用户配置。
Reasonix 明确信任已安装 server 会如实描述这些 hint。因此，planner/只读 subagent 的过滤是
面向可信 server 的工作流边界，不是针对恶意 MCP server 的隔离边界；显式 deny 与进程沙箱
仍由 host 控制。

旧的 `trusted_read_only_tools`、`default_tools_approval_mode`、
`tools.<raw>.approval_mode` 与 `approvals_reviewer` 字段在加载旧文件时会被忽略，并在 Reasonix
下次保存该 MCP 条目时自动移除。

服务器的 **prompts** 会暴露成 `/mcp__<server>__<prompt>` 斜杠命令（命令后空格分隔参
数）；**resources** 通过在消息里写 `@<server>:<uri>` 拉入；`/mcp` 列出已连接服务器及
各自暴露的内容。`make build` 还会产出 `bin/reasonix-plugin-example`——一个可直接运行的
stdio 参考实现（`echo`、`wordcount`、一个 `review` prompt、一个 style-guide 资源），
可照抄。

```toml
[[plugins]]                       # 本地 stdio 服务器
name    = "example"
command = "reasonix-plugin-example"
# startup_timeout_seconds = 60    # 可选：initialize + tools/list 上限
# call_timeout_seconds = 600       # 可选：单个 MCP server 的调用超时
# tool_timeout_seconds = { "generate_video" = 1800 }   # 可选：raw MCP tool 名称

[[plugins]]                       # 远程 Streamable HTTP 服务器
name    = "stripe"
type    = "http"
url     = "https://mcp.stripe.com"
headers = { Authorization = "Bearer ${STRIPE_KEY}" }
```

启用的 MCP 服务器会在会话开始后于后台自动连接，因此工具上线期间聊天仍可正常使用。
用 `/mcp` 或桌面端 MCP 面板可刷新状态、重连服务器、查看失败原因，或在当前会话内禁用某个服务器。
若要跨 skills / hooks / 插件包 / MCP 做只读健康检查（不改配置），见
[能力诊断](./CAPABILITY_DIAGNOSTICS.zh-CN.md)
（`reasonix doctor capabilities` 或 **设置 → 诊断**）。

交互调用方只会为冷启动短暂等待；即使等待结束，共享启动仍会在后台继续，不会被杀掉后反复重启，
服务器上线后重试工具即可。`mcp_startup_timeout_seconds`（默认 `30`）限制从进程启动、授权、
`initialize` 到 `tools/list` 的完整启动流程；`mcp_call_timeout_seconds` 只作用于连接成功后的
RPC 调用。两者都可按服务器覆盖。

**已有 Claude Code 的 `.mcp.json`？** 直接放到项目根目录，Reasonix 会原样读取——其
`mcpServers` 规范（`command`/`args`/`env`、`type`/`url`/`headers`、`${VAR}` 展开）
与 `[[plugins]]` 字段一一对应。两处来源会合并加载；同名时以 `reasonix.toml` 为准。

```json
{
  "mcpServers": {
    "filesystem": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "/path"] },
    "stripe": { "type": "http", "url": "https://mcp.stripe.com", "headers": { "Authorization": "Bearer ${STRIPE_KEY}" } }
  }
}
```

**从 `0.x` 升级？** 旧的 `~/.reasonix/config.json` 仍会被读取（读其 `mcpServers`、并遵从
`mcpDisabled`），作为最低优先级来源——所以 MCP 服务器照常可用；方便时再把它们挪进
`reasonix.toml` 的 `[[plugins]]` 或 `.mcp.json`。

## 斜杠命令

交互式 `reasonix` 会话里，内置命令（`/compact`、`/context`、`/new`、`/clear`、`/rewind`、`/tree`、`/branch`、`/switch`、`/todo`、`/model`、`/mcp`、`/skills`、`/hooks`、`/memory`、`/goal`、`/output-style`、`/sandbox`、`/language`、`/reasoning-language`、`/help`）在本地执行——`/help` 可列出全部。
内置 **Skill**（如 `/init`、`/explore`、`/test`、`/reasonix-guide`）也会出现在斜杠菜单，
并可通过 `run_skill` 调用（正文按需加载；只有索引行进入缓存稳定前缀）。配置或能力排障时
用 `/reasonix-guide`，它会引导运行 `reasonix doctor capabilities`（见
[能力诊断](./CAPABILITY_DIAGNOSTICS.zh-CN.md)）。
`/new` 会开启新会话，同时保存之前的 transcript 供历史记录和恢复使用；`/clear` 会丢弃当前上下文且不保存——默认需二次确认，但在 YOLO 模式下会立即清空（YOLO 本就承诺跳过确认）。
`/tree` 查看已保存的对话分支，`/branch [name]` 从当前对话末端分支，`/branch <turn> [name]`
从较早的 checkpoint 轮次分支，`/switch <id|name>` 切换到另一个分支。**自定义命令**
是放在 `.reasonix/commands/`（项目）或 `~/.reasonix/commands/`（用户）下的 Markdown 文件——
`review.md` 即 `/review`，子目录构成命名空间（`git/commit.md` → `/git:commit`）。文件正文
是 prompt 模板，调用即作为一轮对话发出。

### 子智能体 Profile

子智能体 profile 是带有 `runAs: subagent` 和 `invocation: manual` 的手动 Skill。
它与桌面设置页共用项目级/全局 Skill 目录，因此任一端创建的 profile 在会话刷新后都会被
另一端发现。交互式聊天里使用 `/<name> <任务>` 调用；Reasonix 会启动隔离子智能体，
父会话只保留任务和最终答案。

Headless CLI 提供显式管理和运行命令，同时不改变普通 `reasonix run` 的任务语义：

```bash
reasonix subagent list
reasonix subagent create reviewer --description "审查改动" --prompt-file reviewer.md --tools read_file,grep,bash
reasonix subagent edit reviewer --effort high --model deepseek-pro
reasonix subagent try reviewer "审查当前 diff"   # 始终只读
reasonix subagent run reviewer "审查并修复当前 diff"
reasonix subagent delete reviewer --yes
```

workspace 可用时，`create` 默认写入项目级目录，否则默认写入全局目录；可用
`--scope project|global` 明确选择。`edit` 只修改显式传入的字段，`--model=`、`--tools=`
这类空值会清除对应配置。Profile 编辑器会拒绝
custom path 或包含更多手写结构的 Skill，避免丢失 frontmatter、references 或 scripts；
这些文件仍应通过 Skills 工作流管理。内置 profile 没有可编辑文件，因此 `edit` 对它们只接受
`--model` 和 `--effort`，并写入与桌面设置页相同的按名称覆盖配置。

完整 CLI 参数、Skill 文件格式、模型优先级、安全行为和排障说明见
[子智能体 Profile](./SUBAGENT_PROFILES.zh-CN.md)。

Context Engine v2 把上下文分成两个用途不同的层：

- **常驻指令**来自分层加载的 `REASONIX.md`、`AGENTS.md` 和 `CLAUDE.md`。必须在每个
  相关回合都存在的规则应放在这里。用户全局文件先加载，再加载 workspace 和更深目标目录，
  同一目录内 `.local.md` 变体优先。
- **背景记忆**每个 Markdown 文件只保存一条持久事实。每条事实都有不变 ID、单调 revision、
  时间戳、相互独立的 `type`（`user`、`feedback`、`project`、`reference`）与
  `scope`（`project`、`global`），以及 freshness。事实可能过时，因此永远不能覆盖
  当前请求和常驻指令。

每个真实用户回合前，Reasonix 会自动召回一小组相关事实。它用原始用户消息搜索，抑制“继续”
这类泛化请求，在等价事实中优先项目级版本，对 stale 内容降权，并最多把四条事实 / 2,400
字符追加到本轮 user turn。这段动态后缀不会改写 cache-stable system prompt 或工具 schema。
运行 `/memory recall` 可查看选中的 ID、score、原因、freshness、预算和 suppressed 决定。

新的、有界、非敏感 project/reference 事实可以零配置自动创建，不弹审批。在 Ask 下，全局事实、
用户偏好、feedback、更新、重复项、敏感/超长内容，以及所有 `forget` 仍需显式确认。交互式 Auto
把这些记忆工具作为普通 fallback 处理，并保留显式 `ask` / `deny`；交互式 YOLO 会绕过记忆 ask
审批，但仍遵守 deny。存储层会把自动创建授权强制为 create-only，因此并发出现的新事实也不会被覆盖。顶层 headless controller 可使用同一条
一次性低风险创建路径；子智能体和不拥有该作用域 controller 的 headless surface 会 fail closed。

`forget` 只归档，不永久删除。每次更新都会快照上一 revision；恢复旧版本或 archive 时总会创建
更高的新 revision，不会覆盖历史：

```text
/memory instructions
/memory recall
/memory revisions <id-or-name>
/memory restore <id-or-name> <revision>
/memory archived
/memory recover <archive-path>
```

桌面 Context Center 展示相同的 provenance、冲突、revision history、recall trace 和恢复操作。
打开 Suggestions tab 会自动扫描近期本地用户回合；候选会与两个 scope 的记忆和指令正文去重，
但只有用户接受后才会保存。远程 workspace 绝不回退读取桌面机器的本地 memory 或 session。

旧事实会原地获得确定性 ID 和 revision 1；缺失 scope 时根据所在目录推导。Migration 幂等，
旧客户端仍能安全路由，旧 Memory v5 transcript 也继续可读。完整行为、隐私与 cache 契约见
[`Context Engine v2`](SESSION_MEMORY_RETRIEVAL.zh-CN.md)。

```markdown
---
description: Review the staged diff
argument-hint: [focus-area]
---
Review the staged diff. Focus on $ARGUMENTS, list bugs with file:line.
```

`$ARGUMENTS` 展开为全部空格分隔参数，`$1`…`$N` 为位置参数。MCP prompts 也以
`/mcp__<server>__<prompt>` 形式出现在这里。

## 内置文档检索

Reasonix 会把 `docs/` 中的 Markdown 文档和已审查的 `release-notes/releases.json` 更新日志
目录随 CLI 和桌面端一起编译发布。只读 `docs` 工具通过本地 BM25 检索这份与当前安装版本
完全一致的离线语料，并可按命中的 `section_id` 读取完整章节及来源。每个版本都会生成
`changelog/v1.19.5.md`、`changelog/v1.19.5.zh-CN.md` 这类中英文虚拟文档，因此可以离线
查询指定版本的新增功能、升级说明、修复和已知风险。涉及 Reasonix 配置、CLI/桌面端行为、
版本历史、权限、MCP、记忆、恢复、Provider 或维护流程的问题，Agent 应先查询这里，再考虑
联网搜索或凭经验回答。

普通路径不需要设置、联网、向量数据库或 embedding 服务。搜索会优先匹配提问语言，同时支持
显式 `en`、`zh-CN`、受众和目录筛选。Balanced 与 Delivery 默认暴露该工具；Economy 会在需要时
按需连接 `docs` 来源。每次返回都会给出产品版本、不可变源码 revision 与语料 SHA-256 digest。
发布 CI 会实际编译 CLI；只有编译后的清单与候选提交的 `docs/*.md`、
`release-notes/releases.json` 和构建身份完全一致时才允许发布。因此，更新较快的在线
`main-v2` 页面不会静默覆盖与本地版本匹配的说明或更新历史。

直接输入 `/docs` 会在本地显示内置语料的版本、revision、digest 和使用示例，不调用模型。
输入 `/docs <问题>`（例如 `/docs 1.19.5 更新日志`）时，Reasonix 会先在本地完成检索，再把
与当前版本匹配的证据交给当前配置的 AI 生成带来源的回答。这个命令路径不依赖模型是否主动
选择 `docs` 工具；普通自然语言问题仍可由模型自动调用该工具。已有自定义命令以及兼容插件或
Skill 别名会继续拥有 `/docs`；发生冲突时，CLI 与桌面端通常会改为通过 `/reasonix:docs` 暴露
内置语料。如果这个限定名也已被占用，Reasonix 会选择下一个空闲的 `reasonix:` 限定后备名，
不会覆盖原命令。远程桌面端使用主机解析后的命令目录，因此菜单显示的入口与主机实际执行目标
保持一致。

如果 Pull Request 修改了用户可见的 CLI、桌面端、配置、Provider、权限或工具行为，必须声明
是否已同步更新内置文档；如果无需更新，则必须说明现有的版本匹配说明为何仍然正确。

## Goal

Goal 是长期目标的统一运行机制。Reasonix 会持续推进，直到完成、阻塞、暂停或被清除。
普通聊天不会隐式改变协作模式；需要长目标时，请在输入框中明确选择 Goal，或使用 `/goal` 启动。

Goal 默认不设模型轮数、跨 Run turn 数、墙钟时长或数字式无进展上限。它会持续执行，直到完成、
确实只有用户/外部条件能解除阻塞、用户主动暂停/停止、发生不可恢复的外部错误，或耗尽用户显式预算。
如需给无人值守 Goal 增加可选 token 边界，可配置：

```toml
[agent]
goal_token_budget = 20000000
```

默认值 `0` 表示关闭。达到正数阈值后，Goal 会先生成一次总结再进入可恢复的 `budget_spend` 暂停；
`/goal resume` 会授予新的完整预算切片，但累计 turn、token、请求数和实际工作时间不会清零。
进展按 Goal 范围的新颖性计算：新的读取/搜索
结果、mutation、verification、todo/签收变化和 review 会推进目标；完全相同的工具、参数与
结果重复不会推进。相同宿主失败、零新增证据和 Todo 停滞的数字阈值只会注入纠偏提示、重置干预周期
并要求缩小步骤、切换策略或说明真实 blocker，不会暂停 Goal。未配置对应预算时，累计 turn、token、
真实 provider 请求数与实际工作时间只做统计展示。暂停会保留 Goal、todo、Delivery checkpoint 与运行历史——用
`/goal resume` 继续，`/goal pause` 可手动暂停运行中的目标；`/goal status` 只显示轮次、请求数、
token、可选的显式 token 阈值和工作时间。每个目标 turn 结束时，模型通过结构化的 `update_goal` 工具报告
continue/complete/blocked；没有报告时由独立的有界 evaluator 判定一次，任何 evaluator
故障都会安全暂停目标而不是静默继续。

复杂任务建议把目标写成[任务合约](./TASK_CONTRACT.zh-CN.md)：Context、Request、
Output format、Constraints 和 Pause policy。Goal 模式会把这些部分当作自主执行的边界；
除非下一步需要不可逆或对外可见操作、任务范围变化，或必须由用户提供信息，否则会继续采用合理默认值推进，并在最后汇报假设与结果。

旧的简单/写入/研究参数只作为兼容元数据解析，不再改变执行额度。Goal 状态只保存在普通会话 sidecar；进展只来自宿主工具 receipt、canonical todo、
`complete_step`、review 与 Delivery checkpoint 中的新证据，最终由 Delivery readiness 和有界 Goal
evaluator 判定。Light/Balanced 会接受 `update_goal` 里诚实申报的 `unverified` 检查缺口；同一检查缺口连续两次 `complete` 会结束 Goal，而不是继续验证循环。旧 `.reasonix/autoresearch/<task-id>/` 目录保持只读：显式引用旧路径时可恢复为
普通 Goal，但新版本不会创建或改写这些目录。旧预算 flags 仅为兼容继续接受，不再出现在帮助和补全中。

### 按顺序批量签收步骤

宿主可以在同一个 provider 工具调用轮次中处理多个 `complete_step`。这些调用必须严格遵循
canonical Todo 顺序，并且每一步的工作和证据都必须在对应签收之前已经产生。每次成功签收后，
宿主立即推进 Todo 状态；跳过、仍为 pending 或乱序的步骤仍会被拒绝。这不会改变 provider-visible
工具 Schema。

## @ 引用

在消息里写 `@` 引用，Reasonix 会在发送前解析成带标签的上下文块：`@path/to/file`（或
`@dir`）注入本地文件内容（或目录清单），`@<server>:<uri>` 注入 MCP 资源。本地路径**只有
真实存在**时才当作引用，普通 `@mention` 保持原文。敲 `/` 或 `@` 会弹出补全菜单——斜杠
命令，或**逐层**的文件导航（一次只列当前一层目录、可下钻进子目录）外加 MCP 资源。

## 双模型协同

`reasonix setup` 现在统一管理 provider、模型列表、凭据、连接测试和默认模型；所有修改
会暂存到“保存并退出”，并同步维护桌面端 provider access。完整用法见
[CLI 命令参考](./CLI.zh-CN.md#配置供应商)。若要让两个模型协同（执行器 + 规划器，
各自独立、缓存稳定的 session），向导后手动在 `reasonix.toml` 加一行即可：

```toml
[agent]
planner_model = "deepseek-pro"   # 作为低频规划器
```

Planner 会看到已加载的 `REASONIX.md` / `AGENTS.md` 记忆，并拿到一小组只读研究工具，
因此可以先检查相关文件再把计划交给执行器。写入类和流程类工具仍只给执行器使用。

Reasonix 会用确定性规则路由每一轮，不再调用额外的 classifier 模型。普通请求一律
直达 Executor。独立 Planner 只响应显式 `先规划` / `plan first`、显式等待批准、
显式 `只规划` / `不要执行`，或 Goal 启动。“复杂重构”“修复登录”这类措辞不会自动
启动 Planner，也没有 Light/Full 规划深度。显式 Plan Mode 仍是 executor 上的独立
宿主流程，不会发生双重规划。`直接改` / `just do it` 同样直达 Executor。执行边界
可出现在请求中的任意子句，不要求位于句首，同时会忽略引号内的示例。普通的“先规划”
会在规划完成后自动交接 Executor；明确要求“等我确认”的请求停在宿主审批边界，批准后
继续交接 Executor。只有明确的 `只规划` / `不要执行` 才以计划结束当前回合而不执行，
计划会写入同一会话，用户之后仍可继续要求 Executor 落地。阶段详情会记录不含用户原文
的 route 与 reason code，便于诊断。

Planner 使用同一个稳定的 system prompt，单轮只追加很小的 `<planner-turn>` 标明
显式路由，因此除本次 prompt 升级的一次缓存未命中外，不会持续破坏 Planner prefix
cache。计划应区分已验证与候选触点，并在证据支持时补充非目标、风险、验收标准和
命令级验证。Planner 必须调用 `submit_plan`，没有提交计划的普通文本视为协议错误。
若 Planner 在有界调研和最终总结轮后仍未给出最终计划，所有路由都 fail-closed，
不会降级到 Executor，并回滚不完整的 Planner 回合，避免留下无法继续的会话尾部。

普通 clean final 即结束回合。Goal、review、guardian 仍保留各自的 continuation
约束。Goal 中活跃 Todo 超过停滞阈值仍没有新的完成项、唯一读取、命令或修改时，
宿主会强制缩小步骤、换工具/方法、聚焦委派或报告真实阻塞，然后继续执行。完全重复
的操作不算进展，新的宿主可观测工作会自动续期。两级任务
列表保持同一"唯一当前项"契约：唯一的 `in_progress` 是活跃的 level-1 子步骤，其 level-0
阶段保持 `pending`；子步骤按顺序推进并签核，全部完成后阶段本身转为 `in_progress` 做
最后签核。

升级时仍可解析已有的 `[agent].max_steps` 和 `planner_max_steps`，但其值会被忽略，并在一次性
迁移提示后从配置中移除，避免隐藏的旧上限截断自动进度管理或子 Agent 的继承任务。确实需要
为单次运行设置预算时使用 CLI `--max-steps`；无人值守 Bot 仍保留 `[bot].max_steps`，其中 `0`
表示自动持续执行，正数表示用户显式上限。

**普通对话任务默认没有任何上限**——轮数、token、时长、花费都不限。它一直跑到模型自己
结束、自适应守卫判定它不再产生进展，或者你手动停止为止。

需要时可以自行开启花费闸门。它约束的是**整个任务**（包括每一次"继续"，直到你开始不相关的
新工作）；越过阈值时会产出一次不带工具的总结然后暂停，已完成的工作全部保留，下一条消息
即可继续。

```toml
[agent]
task_cost_budget = 5.0            # 模型定价货币
task_time_budget_minutes = 60     # 整个任务累计的墙钟时长
```

两个维度都默认关闭，也都没有默认值。`task_time_budget_minutes = 0`（以及兼容读取的负数）表示
关闭时间闸门，只有正数才启用显式时间预算。**该不该停是只有你能下的判断**：金额在不同模型之间
不可移植（对便宜模型足够宽松的额度，换成前沿模型可能问两句就触发），而任务跑得久，既可能
是失控，也可能就是你要的活。

成本维度只对有定价的模型生效：没有价目表时该维度直接不参与判断，而不是把任务读成免费；
免费或本地模型请改用时长维度。

轮数刻意不作为一个维度。能跑到很高轮数却没花多少钱的任务，说明它每一轮都又便宜又快，
这恰恰是最不该打断的情况。确实想按轮数限制某次运行时，用一次性的 `--max-steps`。

Subagent skills 默认继承执行器模型。设置 `subagent_model` 可让它们统一走另一个已配置
模型；设置 `subagent_models` 则只覆盖 `review`、`security_review` 等指定 skill。

Subagent 默认允许再委派一层：根会话是 depth 0，第一层 subagent 是 depth 1，
`max_subagent_depth = 2` 表示 depth 1 的 workflow 可以再派 depth 2 的 reviewer
或 implementer；depth 2 不再拿到递归 agent/skill 工具。设
`agent.max_subagent_depth = 1` 可恢复旧的单层边界。这主要用于 Superpowers 这类
workflow skill 派发 reviewer subagent 的场景，同时避免无限递归和后台 fanout。

当计划阶段需要**明确隔离为只读**的深度调研时，用 `read_only_task`；如果更适合复用已有 skill，
用 `read_only_skill`。两者都会启动
ephemeral 只读 subagent，只暴露只读研究工具和安全前台 bash，只返回最终答案，不创建
可续接的 subagent transcript。只读嵌套委派会在 `max_subagent_depth` 内可用，其内部仍不提供
可写的 `task` / `run_skill`。执行设定不再改变 provider 可见工具面；通过
`use_capability` 调度 `read_only_skill` 等可选能力，后续 writer 调用仍通过
Permissions/Sandbox。

所有严格只读子会话都经过同一对共享构造入口——`RunReadOnlySubAgentWithSession` /
`NewReadOnlyAgent`——两者都会把子会话标记为永久只读并做最终 registry 过滤：移除 writer、
destructive MCP 目标、来自未授权 server 的 reader，以及一切会改变 host capability 的工具。
用户安装和项目配置声明的 server 都会立即获得授权。符合条件的 reader 仍可按需启动。严格只读入口一览：

| 入口 | 用途 |
| --- | --- |
| `read_only_task` | 主会话派生的隔离只读调研子会话 |
| `parallel_tasks`（只读） | 并发只读调研子会话 |
| `fleet` 且 `read_only: true` | 可带 Profile 的并行批量（单项强制只读） |
| `read_only_skill` | 以既有 skill 驱动的同等隔离 |
| `reasonix review`（CLI） | 只读评审 diff 或分支 |
| 桌面端 preview/review 子代理 | 桌面端只读分析面 |

在持久化会话中，`parallel_tasks` 与 `fleet` 不再把所有完整答案拼成一个容易被截断的
工具结果，而是为每个已完成子 Agent 返回有界预览和独立的 `Subagent reference`。父 Agent
可用 `read_subagent_result` 按 `offset_bytes` 分页读取该引用对应的完整答案；读取范围受当前
会话 lineage 与工作区约束。没有持久化父会话的 headless 运行仍保持 ephemeral，只返回公平
分配的有界预览，不能生成持久引用。

已持久化的子 Agent 结果还会带有 `status`（`completed`、`partial`、`failed` 或
`cancelled`）和 `retryable`。部分完成或可重试的失败会保留最后一条可见回答与引用，父 Agent
可以用 `read_subagent_result` 查看，或通过 `task` / `run_skill` 的 `continue_from` 继续同一条
transcript。

交互式双模型 Planner 使用专用构造路径（`NewPlannerAgent`）：仍阻止 bash、文件写入与普通
writer，但可通过固定的 `use_capability` 代理调用已授权、非 destructive 的 MCP，不再要求
`readOnlyHint`。直接 `mcp__*` schema 永不进入 Planner 工具列表，因此 MCP 安装/连接变动
不会在一次性 schema 升级后继续改变 Planner 缓存前缀。缺少 `readOnlyHint` 不再阻止 Planner；
带 `destructiveHint` 的工具零执行，应写入方案交给 Executor。

普通 `task` / `fleet` 子 Agent 同样获得该固定代理（会话共享 Host/连接，每 Agent 独立
frontend/ledger），可调用已安装或项目配置 MCP，不要求 `readOnlyHint`。这些调用走可信 MCP
权限路径（实时授权复核 + 仅显式 deny）；writer/destructive 仍会串行、按 mutation 记账，并受
Delivery 证据/租约门禁约束，而不是 Planner 的 Executor handoff。严格 `read_only_task` /
`read_only_skill` / review 子 Agent 共享稳定代理 schema 与连接复用，但执行仍要求
`authorized && readOnlyHint && !destructiveHint`。Profile `allowed-tools` 中的 MCP 名称
会转换为代理上的 capability ID 白名单；子 Agent 从不继承动态 `mcp__*` schema。

在严格只读子会话内：`use_capability` 在 Commit/permission/hook/执行前会对解析出的
真实目标再次校验；未连接且符合条件的 MCP reader 可从当前 schema cache 按需启动，
initialize/tools-list 后会在 `tools/call` 前核对缓存与 live 的 `readOnlyHint`/
`destructiveHint`；reader 变 writer 或升级为 destructive 时零执行，普通重试会重新经过当前
边界。仅 schema 变化会静默刷新下一会话的缓存，不再中断已授权调用。分发前还会再次检查运行时
enable、授权与完整连接身份，因此共享 Host 中另一个项目/tab 的同名 client 不能被误复用。未授权
server 无法在这里提升权限。严格只读边界比独立 Planner 更窄：Planner 接受已授权的 opaque
非 destructive MCP，而严格只读子会话必须有明确 reader hint，且根本不暴露 writer。

Reasonix 使用**事实驱动执行**。普通请求一律进入 executor，没有自动任务模式；
唯一的会话角色是质量底线（standard/delivery），事实仍可能高于它。Plan、Goal、permission、sandbox 与任务合同是互相独立的状态。

Standard 和 Delivery 都不会执行通用的隐藏 final-readiness 重试。Delivery 把 readiness 缺口
作为可恢复结果返回并展示现有的「继续检查」入口，只有用户主动点击后才会启动恢复回合；
Standard 的验证、复核和签收缺口仍作为完成提示处理。除此之外，Standard 有一个同一前台
`Agent.Run` 内的 Todo 一致性保护：可信宿主确认用户要求执行、当前回合成功写入唯一
`in_progress` Todo 且写工具可用时，会追加一次固定续做提示；只有产生新的宿主 receipt 才允许
第二次，并且最多两次。Plan、Goal、Delivery、只读、恢复、取消和已有排队用户输入都会禁用该
保护。Goal 和已批准 Plan 继续由各自状态机控制连续执行；provider 层的流中断/截断恢复与
final-readiness 恢复相互独立。历史 canonical Todo 继续显示，但空闲时标记为「待继续」而不是
「进行中」；用户点击「继续」只会发送到当前可见会话，不会把历史 Todo 隐式变成后台任务。

所有任务共享同一套 provider 可见核心工具面（直接读/bash/编辑/写入、后台 shell
生命周期工具，以及稳定的 `use_capability` 代理）。可选工具（搜索、MCP、skills、
subagents、docs、web_fetch 等）通过 `use_capability` 调度，不会扩展 top-level
provider schema，因此任何任务都不会制造新的工具 schema 缓存前缀。Harness 的
minimal preset 不是任务复杂度模式。

模型按需决定是否调查、写 todo、调用子 Agent。宿主再根据具体 Tool Call、真实
目标路径和执行回执建立验证义务：

- 纯只读调用不产生义务。
- 文档、i18n、fixture、样式的局部修改只需 Advisory 定向验证。
- 单个生产文件修改是 Recoverable 定向验证加 diff review。
- 多文件或范围不清的本地写入，先要求 todo 和验收标准。
- Schema、迁移、公共接口、认证路径或破坏性操作，在实际写入后形成 Strict
  验证、复查和签收。
- Goal 项和已批准 Plan 的验收项全部为 Strict。
- 用户话里出现 OAuth、token 等词本身不会产生动作风险。

交互式前端中的计划模式始终由用户显式选择：桌面端在“协作方式”中选择计划模式，CLI 用
`Shift+Tab` 切换到 Plan。Reasonix 先生成计划，待用户批准后工作流才切换到实施；规划期间的
工具调用仍遵守当前 Permissions 与 Sandbox。旧的 `agent.auto_plan` 与
`agent.auto_plan_classifier` 会被忽略，并在升级时从用户配置中移除。可见思考语言可通过以下方式修改：
会话里用 `/reasoning-language auto|zh|en`，shell/脚本里用
`reasonix config reasoning-language auto|zh|en`。只有明确想为
reasoning-language 写项目级覆盖时，才给 shell 命令加 `--local`。

桌面端“协作方式”菜单里的计划模式与目标模式的使用方法与注意事项，
见 [`COLLABORATION_MODES.zh-CN.md`](./COLLABORATION_MODES.zh-CN.md)。没有自动
任务模式；唯一的会话角色是质量底线（standard/delivery），验证义务由宿主根据真实工具动作建立。

桌面端“工具权限”里的询问、自动和 Yolo 模式的区别与使用场景，
见 [`TOOL_APPROVAL_MODES.zh-CN.md`](./TOOL_APPROVAL_MODES.zh-CN.md)。

分离 session（让各模型前缀缓存稳定）背后的取舍见
[`SPEC.md` §3.5](./SPEC.md#35-two-model-collaboration-coordinator)。
