# 迁移到 Reasonix 1.0（Go 重写版）

<a href="./MIGRATING.md">English</a>

Reasonix 1.0 是一次从零开始的 **Go 重写**。它使用全新的代码库，并不是 `0.x` TypeScript 版本的增量升级。本文说明两个版本的差异以及迁移方法。

## 摘要

| | 旧版（v1） | Reasonix 1.0+（v2） |
| --- | --- | --- |
| 语言 | TypeScript / Node.js | Go |
| 分支 | [`v1`](https://github.com/esengine/DeepSeek-Reasonix/tree/v1)（仅维护） | `main-v2`（默认、活跃开发） |
| 版本 | `0.x`（最高 v0.54.x） | `1.0.0`+ |
| 安装 | `npm i -g reasonix@0.53.2`（固定到某个 `0.x` 版本） | `npm i -g reasonix`；也可使用 release 归档或源码构建 |
| 代码智能 | embedding 语义搜索 + tree-sitter 符号索引 | LSP 辅助代码读取，以及 grep/read_file/glob；语义索引尚未移植 |

“v1”和“v2”表示代码库代际，而不是 semver 主版本：v1 从未发布 1.0，因此 Go 重写版使用 `1.x` 版本号。

## 安装 1.0

`npm` 仍是主要安装渠道。npm 包会下载预编译的 Go 二进制文件，方式与 esbuild/biome 类似；二进制本身是独立的 Go 可执行文件，npm 不是运行时依赖。

**`npm i -g reasonix` 会安装当前正式的 `1.x` 版本。** npm 的 `latest` 标签已从 `1.17.5` 起切换到 Go 版本。以后不再公开发布候选版本；旧 `next` 与 `canary` 标签仅作为兼容别名，始终指向同一个正式版本。旧版 `0.x` 仍可通过固定版本安装：

```sh
npm i -g reasonix          # 当前正式的 1.x
npm i -g reasonix@0.53.2   # 固定到旧版 TypeScript 构建
```

每个 GitHub release 都附带预编译归档（`reasonix-<os>-<arch>.tar.gz` / `.zip`）和桌面安装包。它们与 npm 是不同的安装渠道：桌面安装包不会改动通过 `npm i -g` 安装的 CLI，因此 shell 中的 npm `0.53` 与 `1.x` 桌面应用可以共存，并不冲突。

也可以从源码构建：

```sh
git clone https://github.com/esengine/DeepSeek-Reasonix   # 默认分支 main-v2（Go）
cd DeepSeek-Reasonix && make build                        # -> bin/reasonix(.exe)
```

## 配置

| 旧版 | Reasonix 1.0 |
| --- | --- |
| TypeScript 配置文件 | 项目使用项目本地配置；从 v1.8.1 起，全局配置为 Reasonix home 下的 `config.toml`（macOS/Linux：`~/.reasonix/`；Windows：`%AppData%\reasonix\`）。参见 `reasonix.example.toml` 和[配置路径](./CONFIG_PATHS.zh-CN.md) |
| 环境变量 / API key | provider 配置保留 `api_key_env`；保存的 key 位于 Reasonix home 的 `.env`（`DEEPSEEK_API_KEY`、`MIMO_API_KEY` 等） |
| 项目记忆 | `REASONIX.md`（含自动记忆），兼容 Claude Code |
| MCP server | 在项目本地配置中使用 `[[plugins]]`，或直接读取 Claude Code 的 `.mcp.json` |

首次启动时，v1.8.1+ 会执行一次非破坏性导入。它会读取以下旧配置：

- `~/Library/Application Support/reasonix/config.toml`
- `~/.config/reasonix/config.toml`
- `~/.reasonix/reasonix.toml`
- v0.x 的 `~/.reasonix/config.json`

导入内容包括 API key、base URL、语言和 MCP server；缺失的旧凭据会迁移到 `<Reasonix home>/.env`，旧会话也会从历史目录导入。原文件不会被修改，Reasonix 会在导入后显示启动提示。

会话会根据 v0.x sidecar 元数据回到原工作区，并沿用旧摘要作为标题；工作区已不存在的会话会进入全局会话目录。可通过 `--resume` 或历史面板恢复这些会话。自动配置导入仅在尚未存在 v1.8.1+ 配置时运行；若新配置已经生成，请手动补入缺失值。

如果首次启动时旧路径尚不可用，可在交互式会话中运行 `/migrate`。若看到 `unknown command`，请先升级到包含该命令的 Go 版本。该命令会扫描旧配置、凭据、记忆和会话，并仅导入尚未导入的内容；它不会覆盖已有 `config.toml` 或记忆文件，也不会绕过会话导入标记。

若旧 v0.x 会话位于自定义 Windows 目录，可指定来源：

```text
/migrate --from "D:\OldReasonix"
```

完整路径和限制见[配置路径](./CONFIG_PATHS.zh-CN.md)。

### Desktop Topic 元数据

Desktop 会自动把四个旧 `desktop-topic-*.json` 索引迁移到 Reasonix 状态根目录下按
scope 隔离的权威 SQLite 数据库。已存在旧文件的 scope 会继续同步 JSON，因此正常降级到
前一版本 Desktop 时仍能读取标题和时间；全新 scope 只写 SQLite，旧版 Desktop 无法直接
读取这部分状态：现有 session metadata 可能恢复标题，但不保证创建时间和自动标题阶段完整。
迁移不会删除旧文件，也不会修改项目拥有的 `.reasonix` 资产。不支持新旧 Desktop 同时写
同一个工作区。

## Context Engine v2 升级

指令与记忆升级会自动完成，不需要 setup mode、re-index 命令或新配置：

| 现有数据 | 升级行为 |
| --- | --- |
| `REASONIX.md`、`AGENTS.md`、`CLAUDE.md` | 作为常驻指令加载，并附带来源、目录、precedence、imports 和 diagnostics；原文件名继续有效。 |
| 嵌套指令文件 | 从 workspace root 解析到当前目标路径；同一目录内 `.local.md` 优先，更深目录仍高于更浅目录。 |
| 没有 `id` / `revision` 的旧事实 | 获得确定性的 scope-aware `legacy-*` ID，并从 revision 1 开始；migration 幂等。 |
| 没有 `metadata.scope` 的旧事实 | 根据原本拥有该文件的 project/global 目录推导 scope。 |
| 现有 `MEMORY.md` | 作为派生 index，根据 active fact 文件重建；陈旧手写条目不会变成事实。 |
| 现有 active facts | 保持 active，之后发生修改时才开始产生 revision history。 |
| 现有 archive entries | 继续排除在 recall 外，可从 Context Center 或 `/memory recover` 显式恢复。 |
| 旧 Memory v5 transcript | 继续可读；preview 会从 `<memory-compiler-execution>` 恢复原始用户提示。 |
| `[agent].memory_compiler` | 已退役，由既有一次性配置 migration 清除。 |

升级后第一次当前版本启动会补齐缺失的 identity/time metadata，不修改 fact body。若新旧版本
共享同一 state root，兼容路由字段会避免旧客户端把事实移到错误 scope 目录。

升级后请使用诊断命令，不要手工修改 migration state：

```text
/memory
/memory instructions
/memory recall
/memory revisions <id-or-name>
/memory archived
```

新的相关事实会自动召回。只有有界、非敏感、纯创建的 project/reference 事实可以免确认保存；
全局事实、偏好、feedback、更新、重复项、敏感内容和所有归档操作仍是显式用户决定。桌面
Suggestions tab 会自动扫描，但候选在用户接受前绝不会写入。

完整 precedence、freshness、恢复、cache、隐私与远程 workspace 契约见
[Context Engine v2](./SESSION_MEMORY_RETRIEVAL.zh-CN.md)。

## 保持不变的部分

agent 核心延续了原有能力：循环、读写编辑与 glob/grep/bash 等工具、子智能体（`task`、explore/research/review）、Skill、Hook、Plan 模式、MCP 客户端，以及针对 DeepSeek 前缀缓存的设计。

## 主要变化

- **代码智能**：Go 重写版通过 LSP 辅助代码读取，并结合 `grep`、`read_file` 和 `glob` 理解本地代码。v1 的语义搜索与 tree-sitter 符号索引尚未移植，CodeGraph 也不再以内置 MCP server 形式提供。
- **Plan 模式**：新增 `complete_step`，用于基于证据确认步骤完成。
- **MCP 项目身份与 schema 缓存 URL 感知凭据**：userinfo 和 token/api_key/password 等查询值不会进入项目运行身份摘要或 schema 缓存键，因此轮换凭据不会改变项目运行时/缓存身份。用户安装的 server 不计算项目身份摘要；已配置 MCP 不再需要旧的启动或逐工具授权回执。
- **MCP 添加后即可使用**：用户通过桌面端、CLI、全局配置、旧配置导入或主动安装插件包添加的 server 默认可信，全局安装统一写入 `config.toml`。仓库内项目本地配置 / `.mcp.json` 声明保留在项目中，同样无需额外启动确认。同名时项目覆盖全局，项目内部项目本地配置高于 `.mcp.json`。打开陌生仓库等同于接受其中可执行的项目配置；启动 Reasonix 前应检查 `.reasonix/settings.json`、项目本地配置和 `.mcp.json`。如果仓库引发异常的 MCP 或 Hooks 行为，可用安全模式重新启动，在恢复期间禁用这些外部集成。
- **stdio MCP 连接持久化**：writer 调用不再创建新进程，浏览器或会话类 server 的状态可以保留。
- **Plan 与权限策略相互独立**：普通内置工具和 Bash 仍遵循 Ask/Auto/YOLO 与 Sandbox；已安装或代理解析的 MCP 写入/破坏性工具，以及来自未授权 server 的读取工具，在整个规划阶段保持阻止。`complete_step` 等执行阶段工具也要等计划获批后才能使用。
- `plan_mode_read_only_commands` 仍可解析和保存，以兼容旧配置，但不再决定主 Plan 流程能否调用工具。安装或通过项目配置声明 MCP server 后，其非破坏性的 `readOnlyHint` 工具会自动进入 planner 与只读子智能体，不需要逐工具信任配置。
- 使用 `read_only_task` / `read_only_skill` 创建技术上只读的子智能体；普通 `task` / `run_skill` 仍可写入，并受权限与 Sandbox 控制。未声明 `readOnlyHint` 的 MCP 工具仍按 writer 处理。
- `default_tools_approval_mode`、`tools.<raw>.approval_mode` 和 `approvals_reviewer` 已停用，加载时忽略并在下次保存时移除；安装或通过项目配置声明 server 后，其所有工具直接可用。
- **Web Dashboard 仍然可用，桌面端更推荐**：需要浏览器访问时，可运行
  `reasonix serve` 启动本地 Web UI；日常可视化使用优先选择 Electron 桌面端，
  终端工作流继续使用 CLI/TUI。
- 一些细粒度 v1 工具被合并，例如文件管理操作改由 `bash` 完成；少数工具尚未移植，进度在 Discussions 中跟踪。

## 文件编码

Reasonix 1.0 支持读取和编辑 UTF-8、UTF-8 BOM、UTF-16 LE/BE 与 GB18030（GBK 的超集），与 v1 行为一致。

- `read_file` 会把受支持编码解码为 UTF-8 后提供给模型。
- `edit_file` 和 `multi_edit` 会保留文件原编码；编辑 GB18030 文件后仍以 GB18030 保存。
- `write_file` 始终写入 UTF-8。
- `grep` 会在匹配前解码，因此正则表达式可用于非 UTF-8 文件。

## 报告问题

Issue 和 PR 按代码线标记：**`v2`** 表示 Go 版（`main-v2`），**`v3`** 表示 Studio（`studio`）。请按实际使用版本提交报告。

如有问题，请发起 [Discussion](https://github.com/esengine/DeepSeek-Reasonix/discussions)。
