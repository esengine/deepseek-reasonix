# 桌面端 Hooks 使用说明

<a href="../README.zh-CN.md">README</a>
&nbsp;·&nbsp;
<a href="./GUIDE.zh-CN.md">使用指南</a>
&nbsp;·&nbsp;
<a href="./SPEC.md">规格</a>

Hooks 让 Reasonix 在会话、用户输入、工具调用、模型返回、压缩上下文等节点执行本地 shell 命令。桌面端在“设置 -> Hooks”里提供图形化编辑入口，本质上读写同一份 `settings.json`。

> Hook 命令会在本机执行 shell。全局和项目 hooks 都会从各自配置位置自动加载。

## 快速开始

1. 打开桌面端“设置 -> Hooks”。
2. 选择范围：
   - “全局”：保存到 `<Reasonix home>/settings.json`，始终加载；Windows 默认是 `%APPDATA%\reasonix\settings.json`，macOS/Linux 默认是 `~/.reasonix/settings.json`。
   - “项目”：保存到当前工作区的 `.reasonix/settings.json`，打开项目时自动加载。
3. 在 JSON 配置框里编辑 `hooks`。
4. 保存后，重启桌面端，让新配置进入会话。`/new` 只开启新对话，不会重新读取 hooks 配置。

示例：

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "match": "bash",
        "command": "node .reasonix/hooks/check-bash.js",
        "description": "Block dangerous shell commands",
        "timeout": 5000
      }
    ],
    "Stop": [
      {
        "command": "echo Reasonix turn finished"
      }
    ]
  }
}
```

## 配置文件位置

| 范围 | 文件 | 加载方式 | 加载顺序 |
| --- | --- | --- | --- |
| 全局 | `<Reasonix home>/settings.json` | 自动 | 项目 hooks 之后 |
| 项目 | `<workspace>/.reasonix/settings.json` | 自动 | 全局 hooks 之前 |

同一个事件下，项目 hooks 先运行，全局 hooks 后运行；同一范围内按数组顺序运行。阻塞型事件遇到第一个阻塞 hook 后，会停止继续执行后面的 hook。

## 配置 JSON 格式

推荐写法是一个带 `hooks` 字段的对象：

```json
{
  "hooks": {
    "PreToolUse": [
      { "match": "bash", "command": "node .reasonix/hooks/pre-tool.js" }
    ],
    "UserPromptSubmit": [
      { "command": "node ~/.reasonix/hooks/check-prompt.js" }
    ],
    "Stop": [
      { "command": "osascript -e 'display notification \"Turn done\" with title \"Reasonix\"'" }
    ]
  }
}
```

桌面端 JSON 编辑器也接受两种便捷输入，保存前会格式化回 `{"hooks": ...}`：

```json
{
  "PreToolUse": [
    { "match": "bash", "command": "node .reasonix/hooks/pre-tool.js" }
  ],
  "Stop": [
    { "command": "echo done" }
  ]
}
```

```json
[
  { "event": "PreToolUse", "match": "bash", "command": "node .reasonix/hooks/pre-tool.js" },
  { "event": "Stop", "command": "echo done" }
]
```

每个 hook 对象支持这些字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `command` | string | 必填。通过平台 shell 执行的命令。空字符串会被忽略。 |
| `match` | string | 仅 `PreToolUse`、`PostToolUse` 使用。锚定正则，空字符串或 `*` 表示匹配所有工具。 |
| `description` | string | 可选。显示在 hooks 列表或设置页里的说明。 |
| `timeout` | number | 可选。毫秒数。未设置时，阻塞型事件默认 5000ms，其它事件默认 30000ms。 |
| `cwd` | string | 可选。覆盖 hook 命令工作目录。默认使用当前会话的 `cwd`。 |

`match` 是锚定正则：`"file"` 不会匹配 `read_file`，需要写成 `".*file"`。正则非法时该 hook 不会触发。

`command` 默认通过平台 shell 执行：macOS/Linux 使用 `sh -c`，Windows 使用
`cmd /c`。Windows 上如果命令本身指向带 POSIX shell shebang 的脚本文件，Reasonix
会自动改用 Git Bash；如果 Windows hook 自己显式写了裸命令 `sh -c` 或 `bash -c`，Reasonix
会查找 Git for Windows 自带的 Bash 并直接使用它；带目录的显式解释器路径保持不变。
通过 `[tools.shell]` 配置的自定义 Bash 路径同样会被 Hook 复用；找不到 Git Bash 时，
插件 Doctor 和能力诊断会提前显示可操作的依赖提示。Hook stdout/stderr 中的 Windows 旧代码页
文本会转换为 UTF-8，避免中文错误信息显示成乱码。stdin 是 Reasonix 写入的一行 JSON，
见下面的 payload 表。

## 配置里的事件 key

下面这些字符串就是 `hooks` 对象里的事件 key，也是在数组写法中 `event` 字段的取值：

| 事件 key | 触发时机 | 是否可阻塞 | stdout 特殊作用 |
| --- | --- | --- | --- |
| `PreToolUse` | 工具权限已通过、工具真正执行前 | 是 | 无特殊作用 |
| `PostToolUse` | 工具执行后，不论成功或失败 | 否 | 无特殊作用 |
| `UserPromptSubmit` | 用户输入提交后、本轮模型调用前 | 是 | 无特殊作用 |
| `Stop` | 一轮对话结束后 | 否 | 无特殊作用 |
| `PostLLMCall` | 模型流式返回完成后，reasoning 入库前 | 否 | exit 0 且 stdout 非空时，用 stdout 替换展示的 reasoning |
| `SessionStart` | 会话第一次变为活跃，或 `/new`、清空后新会话开始 | 否 | stdout 会作为下一轮模型上下文注入 |
| `SessionEnd` | 会话关闭、切换、`/new`、清空或控制器释放时 | 否 | 无特殊作用 |
| `SubagentStop` | 前台 `task` 子代理完成后 | 否 | 无特殊作用 |
| `Notification` | 需要用户注意时，例如等待工具审批或等待回答提问 | 否 | 无特殊作用 |
| `PreCompact` | 上下文压缩开始前 | 否 | stdout 会追加为压缩摘要的额外指导 |

只有 `PreToolUse` 和 `UserPromptSubmit` 是阻塞型事件。阻塞型事件中，命令 `exit 2` 或超时会阻断后续执行。

## Hook 命令收到的 payload

Reasonix 会把一行 JSON 写入 hook 命令的 stdin。所有 payload 都至少有：

| key | 类型 | 说明 |
| --- | --- | --- |
| `event` | string | 当前事件 key。 |
| `cwd` | string | 当前会话工作目录，也就是 hook 默认执行目录。 |

其它 key 按事件出现；空值会被省略。

| 事件 key | 额外 payload key | 示例 |
| --- | --- | --- |
| `PreToolUse` | `toolName`, `toolArgs` | `{"event":"PreToolUse","cwd":"/repo","toolName":"bash","toolArgs":{"command":"go test ./..."}}` |
| `PostToolUse` | `toolName`, `toolArgs`, `toolResult` | `{"event":"PostToolUse","cwd":"/repo","toolName":"bash","toolArgs":{"command":"go test ./..."},"toolResult":"ok"}` |
| `UserPromptSubmit` | `prompt`, `turn` | `{"event":"UserPromptSubmit","cwd":"/repo","prompt":"修复测试","turn":1}` |
| `Stop` | `lastAssistantText`, `turn` | `{"event":"Stop","cwd":"/repo","lastAssistantText":"已修复","turn":1}` |
| `PostLLMCall` | `reasoning`, `turn` | `{"event":"PostLLMCall","cwd":"/repo","reasoning":"raw reasoning","turn":1}` |
| `SessionStart` | 无 | `{"event":"SessionStart","cwd":"/repo"}` |
| `SessionEnd` | 无 | `{"event":"SessionEnd","cwd":"/repo"}` |
| `SubagentStop` | `lastAssistantText` | `{"event":"SubagentStop","cwd":"/repo","lastAssistantText":"子代理结论"}` |
| `Notification` | `message`, `notificationType` | `{"event":"Notification","cwd":"/repo","message":"approval needed: bash go test ./...","notificationType":"permission_prompt"}`；等待回答提问时 `notificationType` 为 `question_prompt` |
| `PreCompact` | `trigger` | `{"event":"PreCompact","cwd":"/repo","trigger":"manual"}` |

`toolArgs` 是工具参数的原始 JSON。比如 `bash` 通常会带 `{"command":"..."}`，其它工具会按自己的 schema 传入。`Notification.message` 会做必要的隐私收敛，例如记忆审批只包含工具名，不把记忆正文发给外部通知 hook。

## 退出码和输出

| 结果 | 阻塞型事件 | 非阻塞型事件 |
| --- | --- | --- |
| exit 0 | 通过 | 通过 |
| exit 2 | 阻塞 | 警告 |
| 其它非零退出码 | 警告，不阻塞 | 警告 |
| 超时 | 阻塞 | 警告 |
| 命令启动失败 | 错误提示，不阻塞 | 错误提示 |

stdout 和 stderr 会被捕获、去掉首尾空白，并限制单路输出最多 256KB。非通过结果会显示为 warning，优先展示 stderr，其次展示 stdout。

特殊 stdout 行为：

- `PostLLMCall`：exit 0 且 stdout 非空时，stdout 会替换用户看到的 reasoning。若 provider 的 reasoning 带签名，Reasonix 会保留原始 signed reasoning 用于后续请求，同时仍展示 hook 转换后的文本。
- `SessionStart`：exit 0 且 stdout 非空时，stdout 会作为一次性 `<hook-context event="SessionStart">` 注入下一轮真实用户输入。纯文本 stdout 会原样作为上下文；也可以输出 Claude Code / Codex 兼容 JSON：

  ```json
  {
    "hookSpecificOutput": {
      "hookEventName": "SessionStart",
      "additionalContext": "Load the workspace conventions before editing."
    }
  }
  ```

  `hookEventName` 必须与当前事件一致。该上下文不会写入 system prompt、工具 schema 或项目记忆；它只影响下一轮模型请求。单个 hook 上下文最多保留约 10000 字符，总量最多约 20000 字符，超出会截断并标记。
- `PreCompact`：所有非空 stdout 会按换行拼接，作为本次压缩摘要的额外指导。
- 其它事件：stdout 只在非通过结果中作为提示文本使用，不会自动进入模型上下文。

## 示例：SessionStart 注入启动上下文

```json
{
  "hooks": {
    "SessionStart": [
      {
        "command": "printf '%s\\n' '{\"hookSpecificOutput\":{\"hookEventName\":\"SessionStart\",\"additionalContext\":\"Before coding, check the available skills and follow matching workflows.\"}}'"
      }
    ]
  }
}
```

这适合把插件或工作流的 bootstrap 说明带入会话。比如 Superpowers 不需要内置到 Reasonix；可以让它自己的 `hooks/session-start-codex` 在 `SessionStart` 输出 `additionalContext`，或让插件根目录 `CLAUDE.md` 被插件包兼容层直接作为 `SessionStart` 上下文读取，Reasonix 会在下一轮把这段说明注入模型上下文。插件包兼容层也会读取 `.claude/settings.json` 里的 command hooks，并按同名事件映射到 Reasonix hooks。Reasonix 默认允许 `max_subagent_depth = 2`，因此 Superpowers 的父会话或第一层 workflow subagent 可以再派发 reviewer/implementer subagent；第二层不会继续获得递归委派工具。若要恢复旧的单层边界，设 `agent.max_subagent_depth = 1`。这会改变子代理可见工具面，可能影响子代理请求的 prompt cache，但不会把 Superpowers 写进 Reasonix 的稳定 system prompt。

## 示例：阻止危险 bash 命令

`.reasonix/settings.json`：

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "match": "bash",
        "command": "node .reasonix/hooks/block-dangerous-bash.js",
        "description": "Block risky bash commands",
        "timeout": 3000
      }
    ]
  }
}
```

`.reasonix/hooks/block-dangerous-bash.js`：

```js
const fs = require("fs");

const payload = JSON.parse(fs.readFileSync(0, "utf8"));
const command = payload.toolArgs?.command || "";

if (/\brm\s+-rf\b/.test(command) || /\bgit\s+push\b/.test(command)) {
  console.error(`blocked dangerous command: ${command}`);
  process.exit(2);
}
```

`exit 2` 会让 `PreToolUse` 阻断该工具调用，并把错误信息反馈给界面和模型。

## 示例：压缩前追加摘要重点

```json
{
  "hooks": {
    "PreCompact": [
      {
        "command": "printf '%s\n' 'Keep exact user decisions, file paths, and unresolved TODOs.'"
      }
    ]
  }
}
```

当自动压缩或 `/compact` 触发时，stdout 会加入摘要指令。

## 排障

- 保存后当前会话没有变化：Hooks 在会话构建时加载。重启桌面端后才会重新读取配置；`/new` 只开启新对话，不会重新加载 hooks。
- 项目 hooks 不执行：确认当前是项目工作区、配置保存在 `.reasonix/settings.json`，并重启 Reasonix 重新加载。也可用只读诊断：`reasonix doctor capabilities` 或桌面端 **设置 → 诊断**（见 [能力诊断](./CAPABILITY_DIAGNOSTICS.zh-CN.md)），关注 `hook.invalid_matcher` / `hook.malformed_settings`。
- `match` 没生效：它只对 `PreToolUse` 和 `PostToolUse` 生效，并且是锚定正则。
- JSON 报 unknown hook event：事件 key 必须完全等于上表的大小写。
- hook 输出太长：每路 stdout/stderr 最多捕获 256KB，超出会截断并显示截断提示。
