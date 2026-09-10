# 配置路径

从 **Reasonix v1.8.1** 开始，Reasonix 使用一个用户可见的全局目录存放配置和用户状态。CLI 与桌面端共用这个目录。

## Reasonix Home

| 平台 | Reasonix home |
| --- | --- |
| macOS | `~/.reasonix` |
| Linux | `~/.reasonix` |
| Windows | `%APPDATA%\reasonix` |

可以设置 `REASONIX_HOME` 覆盖 Reasonix home，主要用于测试、CI 或便携安装。普通用户通常不需要设置。

设置 `REASONIX_HOME` 后，运行时会变成完整自包含模式：配置、状态、缓存和数据都会位于该目录树下。
Legacy 迁移、OS home 约定目录扫描以及其他 fallback 路径都会跳过，避免从系统级正式安装带入或写回数据。

高级测试或便携安装可以设置 `REASONIX_STATE_HOME` 来移动 sessions、archive、memory 等运行状态。
它不会移动全局配置或 provider 凭据；这些仍然位于 `REASONIX_HOME` 下。如果旧版本曾把 provider key
写到 `REASONIX_STATE_HOME/.env`，Reasonix 会在 `<Reasonix home>/.env` 缺少对应 key 时非破坏性导入。

## 目录内容

| 数据 | 路径 |
| --- | --- |
| 全局配置 | `<Reasonix home>/config.toml` |
| 全局 provider 凭据 | `<Reasonix home>/.env` |
| 旧 credentials 导入来源 | `<Reasonix home>/credentials` |
| 全局斜杠命令 | `<Reasonix home>/commands/` |
| 全局 skills | `<Reasonix home>/skills/` |
| 全局 hooks | `<Reasonix home>/settings.json` |
| 远程 SSH 托管 known_hosts | `<Reasonix home>/remote/known_hosts` |
| 会话 | `<state root>/sessions/` |
| 归档 | `<state root>/archive/` |
| 记忆 | `<state root>/memory/` 与 `<state root>/projects/` |
| 全局 Desktop Topic 元数据 | `<state root>/desktop/topic-state-v1.sqlite` |
| 项目 Desktop Topic 元数据 | `<state root>/projects/<workspace slug>/desktop/topic-state-v1.sqlite` |
| 可丢弃的会话 Catalog | `<cache root>/session-catalog/v6.sqlite` |
| 可丢弃的 Task Catalog | `<cache root>/task-catalog/v1.sqlite` |

`<state root>` 默认等于 `<Reasonix home>`；只有设置 `REASONIX_STATE_HOME`
时才会不同。

Desktop Topic 的标题、标题来源、创建时间和自动标题状态以这些 SQLite 文件为权威存储。
首次访问时，Desktop 会导入项目 `.reasonix/` 目录（或全局 Reasonix 目录）中的旧
`desktop-topic-*.json`。检测到旧文件的 scope 会继续镜像旧格式以支持降级；全新 scope
不会创建这些 JSON。旧文件不会被删除，项目本地 settings、skills、commands、attachments
以及 `reasonix.toml` 均不受影响。

会话 Catalog 是可重建的查询投影，不是用户数据；JSONL、event log、
metadata sidecar 和 `desktop-projects.json` 仍是权威数据。详见
[Session Catalog and Desktop Startup](./SESSION_CATALOG.zh-CN.md)。Task snapshot 和
event log 也仍是权威数据；可重建的跨项目投影见
[Task Catalog](./TASK_CATALOG.zh-CN.md)。

全局用户配置文件名是 `config.toml`。项目本地配置文件仍叫 `reasonix.toml`。
如果有人说“全局 reasonix.toml”，通常指的是 `<Reasonix home>/config.toml`。

## 全局 `config.toml`

`<Reasonix home>/config.toml` 存放 CLI 与桌面端共用的非密钥配置。它可以包含
Reasonix 写入用户配置的 provider、plugin、UI、desktop、tool、skill、sandbox、
bot 和 agent 设置。Provider 条目只保存 `api_key_env` 里的凭据变量名，不保存真实密钥值。

已保存的 provider 与 bot 凭据变量不会进入任何由模型控制的子进程环境。Reasonix 的
文件读取工具、受沙盒保护的 shell 命令和 MCP server 也无法读取全局凭据 `.env`；
项目自身的普通 `.env` 可见性保持不变。Windows 的 shell 命令仍不具备 OS 级沙箱，
详见《使用指南》，因此只应为可信任务批准 shell 权限。

示例：

```toml
config_version = 1
default_model = "deepseek/deepseek-v4-flash"
language = "zh"
credentials_store = "auto"   # 旧兼容字段；provider key 保存在 .env

[ui]
theme = "auto"
cursor_shape = "bar"         # CLI/TUI 输入光标：underline|block|bar
show_turn_usage = false       # 隐藏 TUI 每轮 token/费用回执；默认 true

[desktop]
provider_access = ["deepseek"]

[[providers]]
name        = "deepseek"
kind        = "anthropic"
base_url    = "https://api.deepseek.com/anthropic"
models      = ["deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-flash-vision-exp"]
default     = "deepseek-v4-flash"
api_key_env = "DEEPSEEK_API_KEY"
web_search  = true

[[plugins]]
name    = "example"
command = "example-mcp-server"
```

`[desktop].provider_access` 与 `default_model` 在桌面端启动时会联动（完整
说明见 [SPEC §3.1.1](SPEC.zh-CN.md#311-桌面端模型访问与无密钥默认模型回退)）。
如果 `default_model` 解析到的 provider 不在 `provider_access` 列表里，新
会话会静默回退到列表内第一个已配置且有 key 的 chat model。如果启动时看到
非预期的模型替换，把 default 使用的 provider 加进列表里，或者把整个字段
删掉让所有已配置的 provider 都可用。

不要把 API key 的真实值写进 `config.toml`。这个文件是普通配置：可以查看、编辑、
迁移，也可以在常规脱敏后用于诊断。密钥值属于下面的全局 `.env`。

`[ui].cursor_shape` 只影响 CLI/TUI 的输入框。默认值 `bar` 清晰可见，同时不会覆盖
CJK 双宽字符；如果偏好其它形状，可以设为 `block` 或 `underline`。

`[ui].show_turn_usage = false` 会隐藏 TUI transcript 中每次模型请求完成后的 token 与
费用回执；统计和运行中状态仍正常更新。默认值为 `true`。

### 自定义 provider 的 `api_key_env` 命名

通过桌面端设置或 `reasonix setup` 添加自定义 provider 时，Reasonix 会把生成的
`api_key_env` 保存到 `config.toml`，并把真实密钥值写入全局 `.env` 中同名的 key。
生成结果是稳定的，因此同一个 provider 重启后仍会读取同一个凭据槽位。

Reasonix 会根据 provider 名称生成默认值。能规范化成 ASCII 的名称会得到可读的
env 名，例如 `LOCAL_GATEWAY_API_KEY`；如果名称全部由中文等非 ASCII 字符组成，则会
生成带稳定 hash 后缀的名称，例如 `CUSTOM_d39b9067_API_KEY`，避免多个中文 provider
都共用 `CUSTOM_API_KEY`。如果名称以数字开头，则会添加 `CUSTOM_` 前缀以保证生成的
环境变量名合法；例如 `9router` 会生成 `CUSTOM_9ROUTER_API_KEY`。

CLI 的自定义 provider 向导会先根据 base URL 生成 provider 名称，再套用同一套
provider-name 规则。例如 `https://token.sensenova.cn/v1` 会生成 provider 名
`custom-token-sensenova-cn`，默认 key env 是 `CUSTOM_TOKEN_SENSENOVA_CN_API_KEY`。
直接回车会接受这个默认值；如果你确实想让多个 provider 共用一个凭据，也可以手动输入
`CUSTOM_API_KEY` 或其他自定义 env 名。

升级时不会自动改写已有配置。旧配置中已经使用 `CUSTOM_API_KEY` 的自定义 provider 会继续
读取这个 key。若多个旧自定义 provider 已经意外共用了 `CUSTOM_API_KEY`，需要手动把各自的
`api_key_env` 改成不同名称，并重新保存对应的 API key。

### 自定义 provider 的端点 URL

桌面端自定义 provider 表单会把「API 地址」作为完整请求地址写入 `request_url`，
Reasonix 不会追加或改写路径。已有 TOML 配置不会被重新解释：旧 `chat_url` 继续
保持原来的 OpenAI 专用行为，Anthropic 和 Responses 仍会根据 `base_url` 推导请求
路径；只有用户在新版桌面端明确保存该 provider 后，才会写入并启用 `request_url`。
保存 OpenAI-compatible provider 时还会把完整地址同步到旧 `chat_url`，使旧版本
继续使用同一请求目标。旧版本无法识别 Anthropic 或 Responses 的任意自定义请求路径。
模型发现需要单独地址时可设置 `models_url`；否则 Reasonix 会继续从 `base_url`
推测模型发现地址。

## 全局 `.env`

`<Reasonix home>/.env` 是 Reasonix 保存的 provider API key 的唯一运行时来源。
setup 向导、桌面端设置页、CLI 缺 key 提示以及删除 provider key 的操作，都会通过同一套凭据 helper 读写这个文件。

结构：

```dotenv
DEEPSEEK_API_KEY=sk-...
GEMINI_API_KEY=...
ANTHROPIC_API_KEY=...
# reasonix-cleared OLD_API_KEY
```

规则：

- 每行一个 `KEY=value`；
- 空行和 `#` 注释会被忽略；
- 读取时接受 `export KEY=value` 和带引号的值；
- Reasonix 写入时会拒绝多行值；
- key 必须是类似 `DEEPSEEK_API_KEY` 的 shell 风格变量名；
- `# reasonix-cleared KEY` 是删除 key 后写入的非密钥标记，用来防止旧存储把它静默迁回；
- 在操作系统支持的情况下，Reasonix 会用受限权限写入该文件。

Provider 请求只会从这个全局 `.env` 解析 key。项目 `.env`、home `.env`、继承的 shell
环境变量、旧 `credentials` 文件和系统 keyring 都不再作为运行时 provider key fallback。项目 `.env`、home `.env` 和继承的 shell 环境变量不会自动导入到全局凭据文件。
旧 `credentials` 文件和旧 keyring 条目只会在新全局 `.env` 缺少对应 key 时作为非破坏性迁移来源读取。
项目 `.env` 仍会作为当前 workspace 范围内的非 provider 变量展开来源，例如 MCP/plugin 的 env、headers、URL、command 和 args 中的 `${VAR}`；这些值不会写入进程环境，`REASONIX_HOME`、`REASONIX_STATE_HOME`、`XDG_CONFIG_HOME` 等 Reasonix 控制变量也会被忽略。

缓存仍放在系统缓存目录，例如 macOS 的 `~/Library/Caches/reasonix`、
Linux 的 `$XDG_CACHE_HOME/reasonix` 或 `~/.cache/reasonix`、Windows 的
`%LOCALAPPDATA%\reasonix\cache`。可以设置 `REASONIX_CACHE_HOME` 覆盖缓存根目录。
设置 `REASONIX_HOME` 后，缓存会放在 `$REASONIX_HOME/cache`；如果同时设置
`REASONIX_CACHE_HOME`，后者优先。

## 配置优先级

运行时配置按下面顺序解析：

```text
命令行参数
> 项目 ./reasonix.toml
> 全局 <Reasonix home>/config.toml
> 兼容读取的旧全局配置
> 内置默认值
```

写配置时始终写入新的全局路径：

```text
macOS/Linux: ~/.reasonix/config.toml
Windows:     %APPDATA%\reasonix\config.toml
```

## 旧路径迁移

从 **v1.8.1** 开始，Reasonix 启动时会在第一次加载配置前自动检查旧路径。迁移是同步、一次性、非破坏性的：旧文件会被复制或转换到 Reasonix home，原文件保留。

旧配置来源包括：

```text
~/Library/Application Support/reasonix/config.toml
~/.config/reasonix/config.toml
~/.reasonix/reasonix.toml
~/.reasonix/config.json
```

旧 credentials、memory 文件和 sessions 也会在新目标不存在时导入到 Reasonix home。
旧 provider key 只会在 `<Reasonix home>/.env` 尚未包含同名 key 时复制进去。若新的全局配置已经存在，则新配置优先；旧配置只作为兼容 fallback 保留。

从 **v1.9.1** 开始，Reasonix 还会在升级时把已知旧路径、legacy `config.json`、
桌面端已登记项目和恢复 tabs 对应项目里的 MCP 配置汇总补齐到全局
`<Reasonix home>/config.toml`。已有的全局 `[[plugins]]` 按名称优先，不会被旧
配置或项目配置覆盖；源文件会保留不变。该补齐会写入一次性 marker，避免用户之后
主动删除某个全局 MCP 时又被旧项目配置反复恢复。

## 手动补救迁移

如果 Reasonix 已经创建了新的 home 目录，但当时旧数据还不在可扫描路径里；或者先打开了桌面端，导致自动迁移没有把旧路径数据补齐，可以在任一前端运行补救命令：

```text
/migrate
```

在 CLI TUI 中，把 `/migrate` 输入到聊天输入框。在桌面端中，把同一个命令输入到 composer。命令会显示进度提示：

1. 检查旧配置和 credentials；
2. 扫描已知旧 memory 位置；
3. 扫描已知旧 sessions 目录；
4. 导入尚未迁移过的 memory 文件和 sessions；
5. 输出最终汇总。

如果旧 v0.x sessions 不在上述已知旧路径里，例如 Windows v0.52 安装时选择了自定义安装/数据目录，可以显式指定旧目录：

```text
/migrate --from "D:\OldReasonix"
```

显式形式只导入 sessions。这个路径可以是旧安装目录、`.reasonix`/数据目录，或者
`sessions` 目录本身；Reasonix 会在该根目录下检查常见布局，并使用按来源目录区分的
marker，因此之前已经运行过普通 `/migrate` 也不会挡住这次后补导入。

该补救命令仍然是非破坏性的。它不会覆盖已有的
`<Reasonix home>/config.toml`；如果新配置已经存在，需要手动把旧配置里缺失的设置复制过去。旧 memory 文件只会在目标文件不存在时复制。它也会尊重 session 导入 marker，因此已经迁移过、之后又被用户删除的会话，不会在后续 `/migrate` 中被重新恢复。

版本限制：

- 自动迁移从 **v1.8.1** 开始。
- `/migrate` 只存在于包含该命令的 Go 版 Reasonix 构建中。如果 Reasonix 提示 `unknown command`，请先升级后再运行。
- legacy `0.x` TypeScript 线没有这个命令。
- 普通 `/migrate` 只会重新扫描上面列出的旧路径。只有确认某个目录是 v0.x session 来源时，才使用 `/migrate --from <path>`；它不是备份恢复工具或降级导入工具。
