# ACP 编辑器接入

<a href="../README.zh-CN.md">README</a>
&nbsp;·&nbsp;
<a href="./ACP.md">English</a>
&nbsp;·&nbsp;
<a href="./GUIDE.zh-CN.md">使用指南</a>
&nbsp;·&nbsp;
<a href="https://agentclientprotocol.com/">ACP 规范</a>

Reasonix 实现了 Agent Client Protocol（ACP）v1，通过标准输入输出提供 NDJSON
JSON-RPC 2.0 agent。编辑器和其他 ACP host 负责启动进程、打开一个或多个工作区会话，
并接收流式消息、工具活动、计划、权限请求和配置更新。

会话状态 usage 可携带结构化 `costQuote`（原币、`originalTotals`、identity/官方区域价表
估值、`costComplete`、`displayComplete`、`displayStatus`、`billingMode`），同时保留镜像所选
展示估值的旧字段 `estimatedCost` / `currency`。
详见 [计费文档](./BILLING.zh-CN.md)。

## 启动 agent

ACP host 应启动以下命令之一：

```sh
reasonix acp
reasonix acp --model deepseek-pro
```

客户端未覆盖模型时，`--model` 用于选择启动模型。普通请求一律进入 executor，
没有自动任务模式；唯一的会话角色是质量底线（standard/delivery），验证义务由宿主根据真实工具动作建立。

标准输出专用于 ACP 消息，Reasonix 会把诊断写入标准错误，因此 host 不应合并这两个
流。尚未配置 provider 时先运行 `reasonix setup`；initialize 响应也会声明一个启动
`reasonix setup` 的 terminal authentication method。

## 初始化与能力协商

客户端应在打开会话前调用 `initialize`。Reasonix 会声明以下能力结构（省略无关字段）：

```json
{
  "protocolVersion": 1,
  "agentCapabilities": {
    "loadSession": true,
    "sessionCapabilities": {
      "list": {},
      "resume": {},
      "close": {},
      "delete": {}
    },
    "promptCapabilities": {
      "image": false,
      "audio": false,
      "embeddedContext": true
    },
    "mcpCapabilities": {
      "http": true,
      "sse": false
    },
    "_meta": {
      "reasonix.io": {
        "sessionSteer": {
          "method": "_reasonix.io/session/steer"
        }
      }
    }
  }
}
```

客户端声明 `fs.readTextFile`、`fs.writeTextFile` 或 `terminal` 后，Reasonix 会让
适用的文件操作经过编辑器的未保存 buffer，并让适用的前台命令在客户端持有的 terminal
中运行。读取、编辑、写入等全部文件工具都参与其中，因此一次编辑作用于编辑器当前显示
的内容，而不是磁盘上最后保存的副本。非 UTF-8 文件不适用：ACP 的文件方法只处理文本，
这类文件会留在本地的编码保持路径上，原有字符集不变。客户端没有声明这些能力时，常规
工作区工具会在 Reasonix 进程内本地运行。

## 会话生命周期

每个 ACP 会话都拥有独立的 Reasonix Controller、工作区根目录、模型、协作
模式、审批模式、MCP 集合和持久化 transcript，会话之间不会泄漏状态。

| 方法 | 行为 |
| --- | --- |
| `session/new` | 为绝对路径 `cwd` 打开会话并返回配置状态。 |
| `session/load` | 打开持久化 ACP 会话，并通过 `session/update` 通知回放 transcript。 |
| `session/resume` | 打开持久化会话，但不回放 transcript。 |
| `session/prompt` | 执行一轮任务，流式发送更新，最后返回停止原因。 |
| `session/cancel` | 取消活动回合；它是一条 notification。 |
| `session/list` | 列出活动和持久化 ACP 会话，可按绝对路径 `cwd` 过滤。 |
| `session/close` | 停止活动会话并释放资源，但不删除历史。 |
| `session/delete` | 停止会话并删除其持久化 ACP 历史。 |

`session/new`、`session/load` 和 `session/resume` 可以携带 `mcpServers`。
Reasonix 支持 stdio、Streamable HTTP 和 legacy SSE server。
stdio `env` 和 HTTP `headers` 支持 ACP 官方的
`[{"name":"...","value":"..."}]` 结构，同时继续接受旧版 object-map 结构。

## 会话控制

Reasonix 把互不相关的选择拆成独立控制轴，而不是混在一个 mode selector 中：

| 控制项 | 可选值 | 协议入口 |
| --- | --- | --- |
| 协作模式 | `normal`、`plan`、`goal` | `modes` 和 `session/set_mode` |
| 模型 | 已配置的 `provider/model` | id 为 `model` 的 `configOptions` |
| 推理强度 | provider 支持的等级或 `auto` | id 为 `effort` 的 `configOptions` |
| 工具审批 | `ask`、`auto`、`yolo` | id 为 `tool_approval` 的 `configOptions` |

模型、推理强度和工具审批统一使用 `session/set_config_option`。它的参数是
`sessionId`、`configId` 和 `value`，其中 `configId` 取 `configOptions` 中该选项的
`id`：

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "session/set_config_option",
  "params": {
    "sessionId": "session-id",
    "configId": "tool_approval",
    "value": "yolo"
  }
}
```

注意字段名是 `configId`，不是 `optionId`。返回值是刷新后的完整 `configOptions`
数组；id 未知时返回 `-32602 InvalidParams`。

切换模型或推理强度时会重建会话 Controller，同时保留历史和其他控制轴；
工具审批只更新 gate，不重建 Controller。

执行模式已移除。兼容期内，仍发送 `configId` 为 `agent_preset` 或 `work_mode`
（含旧别名 `profile`、`runtime_profile`、`token_mode`）的
`session/set_config_option` 请求会得到成功的空操作：不切换、不重建，返回值中的
`deprecatedNotice` 会说明自适应标准执行。

旧客户端仍可使用 `session/set_model`。`session/set_mode` 也继续接受 legacy 值
`default` 和 `auto`，分别表示“常规 + 询问”和“常规 + Yolo”；新客户端应使用上面的
独立 selector。

## Prompt、更新与审批

`session/prompt` 支持文本 block 和内嵌文本 resource，不声明图片或音频能力。执行回合
期间，Reasonix 可能发送：

- agent 消息和思考内容 chunk；
- pending 和 completed 工具调用更新；
- 从 `todo_write` 生成的完整计划更新；
- 可用的斜杠命令；
- 当前 mode 和配置项更新；
- 针对受权限控制工具及用户问题的 `session/request_permission` 请求。

Host 应让 `session/prompt` 请求保持打开，直到 Reasonix 返回停止原因；期间仍需同时处理
双向 request 和 notification。

提交 `/clear` 会在原地清空当前会话（新 transcript、同一 session id），与 CLI 的
`/clear` 一致。当会话的工具审批模式为 Yolo 时立即清空；Ask、Auto 与 Plan 模式下，
Reasonix 会先通过 `session/request_permission` 往返询问客户端，选择 `Clear the session`
确认清空，`Cancel` 则中止。该命令会以 `clear` 出现在 `available_commands_update` 中。

Reasonix 只会返回 ACP v1 规定的停止原因。工作已完成但仍需 final-readiness 检查时，
agent 会发送带 `[warning]` 的消息 chunk 并返回 `end_turn`；厂商状态仍保持
`readiness_paused`，便于 host 提供恢复入口。客户端取消始终返回 `cancelled`，即使被中断
的 runner 没有返回 error。显式模型轮数上限（`max_steps`）会发送 `[warning]`、返回
`max_turn_requests`，并记录 paused 厂商状态；host 的任务时间、token 或成本预算也会发送
`[warning]` 并记录 paused 状态，但因 ACP v1 没有任务预算专用停止原因而返回 `end_turn`。
完成校验器已移除。模型正常结束且没有工具调用时返回 `end_turn`；包含工具调用时继续进入
Agent 循环；真正的空响应会在 frozen request 边界重试。旧的
`completion_validation`、`completion_evaluator_model` 和
`REASONIX_COMPLETION_VALIDATION_MODE` 设置仍可读取，但会被忽略且不再由配置渲染器生成。
主机侧的就绪检查、预算、工具安全边界和恢复边界仍然有效。
其他 provider、工具或运行时失败会返回 JSON-RPC
`-32603 InternalError`，消息携带长度受限且已脱敏的原因；不会再用协议外的
`stopReason` 构造成功结果。

状态 phase 为 `readiness_paused` 时，可发送新的 `session/prompt`，并把可选 `action`
设为 `"final_readiness_recovery"`，以继续这一次检查。只发送 `/continue-checks` 文本 block
是兼容写法。两种方式都会消费一次持久化的 host checkpoint；普通 prompt 不会继承该
证据，出现更新的用户消息后再提交旧 action 会以 JSON-RPC `-32600 InvalidRequest` 被拒绝，
且不会发布或持久化虚假的状态回合。

## 回合中引导扩展

Reasonix 通过 ACP v1 厂商扩展提供回合中引导。它不是 ACP 核心方法，也不是仍未发布的
ACP v2 `session/inject` 提案。

### 发现能力

从以下位置读取方法名：

```text
agentCapabilities._meta["reasonix.io"].sessionSteer.method
```

不要假设该扩展一定存在，也不要调用无命名空间的 `session/steer`。ACP 为核心协议保留
所有不以下划线开头的方法名。

### 发送引导

在 `session/prompt` 仍处于活动状态时调用声明的方法：

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "_reasonix.io/session/steer",
  "params": {
    "sessionId": "session-id",
    "prompt": [
      {"type": "text", "text": "把用户名改成邮箱"}
    ]
  }
}
```

持久化会话会返回 item id 和 disposition：

```json
{"itemId":"inbox-item-id","disposition":"steer_accepted"}
```

Reasonix 会先完成持久化再返回。`steer_accepted` 表示活动回合已接受；
`queued_followup` 表示 admission 竞争失败或当前没有活动回合，同一条持久化消息会保留为
后续回合。无路径兼容会话可能不返回 `itemId`，但仍返回 `steer_accepted`。已应用的消息
会进入正常历史；回放 transcript 时显示用户原文，不显示内部 steer marker。

| 条件 | JSON-RPC 结果 |
| --- | --- |
| 活动 prompt 接受持久化引导 | `{"itemId":"...","disposition":"steer_accepted"}` |
| 引导已持久化但活动 admission 被拒绝 | `{"itemId":"...","disposition":"queued_followup"}` |
| session 不存在或 prompt 为空 | `-32602 InvalidParams` |
| 无路径兼容 session 没有活动 prompt | `-32600 InvalidRequest` |
| 客户端调用 `session/steer` | `-32601 MethodNotFound` |

收到 `InvalidRequest` 时，兼容会话没有把引导入队。

## 持久化 Session Inbox 扩展

从 `agentCapabilities._meta["reasonix.io"].sessionInbox` 发现带版本的队列。
Schema v1 在 `methods` map 中声明方法名；客户端应使用这里声明的名字，不要自行拼接
vendor method。

| Key | 用途 | 主要参数 |
| --- | --- | --- |
| `enqueue` | 持久化 follow-up 或 steer | `sessionId`、`text`，可选 `intent`、`idempotencyKey` |
| `list` | 读取元数据、容量、暂停和恢复状态 | `sessionId` |
| `get` | 按需读取一条完整 envelope | `sessionId`、`itemId` |
| `update` / `delete` | 编辑或删除待处理项 | `sessionId`、`itemId` |
| `move` | 调整待处理项顺序 | `sessionId`、`itemId`、从 0 开始的 `toIndex` |
| `setPaused` | 暂停或恢复派发 | `sessionId`、`paused` |
| `retry` / `refresh` | 重试不确定项或重新冻结引用 | `sessionId`、`itemId` |

`enqueue` 返回 `itemId`、`disposition`、`position`、`paused` 和 `idempotent`。
List 只返回预览和字节数，不返回正文。恢复出的 Inbox 默认暂停；客户端应先让用户检查，
再用 `setPaused: false` 恢复派发。

## 运行时重载与扩展表面

Reasonix 还在 `agentCapabilities._meta["reasonix.io"]` 中通告两个扩展点：

- `sessionReloadExtensions`——vendor method
  `_reasonix.io/session/reloadExtensions`。调用后按与 CLI `/reload`
  相同的失败原子语义重载该会话的 agent 运行时（扩展、工具、skills、
  commands、hooks、providers）：回合或重建进行中只排队一次
  （`{"queued": true}`），空闲后执行；否则原子重建并交换，重建失败时
  保留旧运行时。重载成功后 Reasonix 会推送新的
  `available_commands_update`。
- `extensionSurface`——结构化扩展 UI 能力。在 initialize `_meta` 中
  同样声明了 `reasonix.io.extensionSurface` 的客户端会收到结构化的
  扩展表面载荷；未声明的客户端收到等价文本 fallback（card/status 退
  化为 `agent_message_chunk`，扩展表单退化为权限请求），因此客户端
  不做任何处理也能保持兼容。

已安装插件声明的扩展 action 以 `/<plugin>:<action>` 出现在
`available_commands_update` 中，可像普通斜杠命令一样调用。

## 可选的 MCP 用户交互扩展

支持 MCP elicitation 的宿主在 `initialize.clientCapabilities` 中显式声明：

```json
{"_meta":{"reasonix.io":{"mcpInteraction":{"supported":true,"schemaVersion":1}}}}
```

Reasonix 在 `agentCapabilities._meta.reasonix.io.mcpInteraction` 返回对应能力，
方法为 `_reasonix.io/mcp/request_interaction`。协商成功的会话使用 interactive MCP
host profile；未声明或版本不匹配的客户端继续使用 core profile，不接收新增反向请求。
新建、加载与重建会话均遵循这一协商结果。

反向请求包含 `sessionId`、`promptId`、`turnId`、`server`、`mode`、`message`，
表单模式还包含 `requestedSchema`，URL 模式包含 `url` 和 `elicitationId`。
响应为 `{"action":"accept","content":{}}`、`{"action":"decline"}` 或
`{"action":"cancel"}`。宿主应按 schema 校验表单；URL 流程交给用户操作，
不得把登录凭据作为表单内容返回。不支持的交互应取消。

每次回答绑定原 controller 和 turn。取消、无效回答及被拒绝的 URL 均取消交互，
只有 `accept` 才使用 content。controller 先持久化决定，再释放 MCP 等待者。
这一扩展不替代 `session/request_permission`，也不改变工具权限策略。
协商出的 host profile 会影响 MCP capability/cache identity，transcript schema 不变。

## 兼容性与缓存行为

| 表面 | 旧版或非 Reasonix 客户端的行为 | 结论 |
| --- | --- | --- |
| 现有 ACP v1 方法 | 方法名和响应结构不变。 | 兼容 |
| Capability `_meta` | 可以忽略未知 metadata。 | 兼容 |
| 持久化 transcript | transcript schema 不变；Inbox 使用带版本的 sidecar。 | 兼容 |
| CLI、Desktop、Bot steer | 被拒绝的 steer 会保留为持久化 follow-up。 | 兼容 |

Steer 只会把用户请求的消息追加到正常会话历史，不改变 system prompt、工具 schema、工具
顺序或其他稳定的 provider prefix 字节。下一次 provider 请求必然包含这条新消息，和任何
普通新用户消息一样会改变新增后缀，但此前的稳定前缀仍可复用。

## 客户端接入检查清单

1. 启动 `reasonix acp`，分离 stdin、stdout 和 stderr。
2. 调用 `initialize`，同时遵守标准 capability 和 `_meta` capability。
3. 使用绝对工作区路径打开会话，并隔离保存各 session id。
4. Prompt 运行期间继续处理 agent 发往客户端的文件、terminal 和权限请求。
5. 只有在 Reasonix 声明 capability 且 prompt 活动时才显示 steer UI。
6. 按 steer `disposition` 分支；两种结果都已持久化，但只有 `steer_accepted`
   能影响活动回合。
7. 用 `session/close` 释放资源；只有用户明确要删除持久化历史时才调用
   `session/delete`。
