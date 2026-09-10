# 桌面浏览器

[English](DESKTOP_BROWSER.md)

桌面浏览器是 Reasonix 窗口内由用户与 Agent 共同操作的原生 Chromium 表面。网站在壳
拥有的 Electron `WebContentsView` 中渲染；Agent 的每项能力都经过 Go 桌面服务，因此
本地与远程 Agent、审批、取消、证据与操作记录共享同一实现。本文是浏览器面板、壳的
表面管理器、Go `BrowserExecutor` 与 Agent 可见工具之间的契约。

```text
Agent 工具调用 ─▶ Go BrowserExecutor ─▶ ledger.reserve ─▶ host/browser.* ─▶ WebContentsView
       ▲                    │                                     │
       └── 结果 / 证据 ──────┴─────────── ledger.settle ◀──────────┘
用户在页面输入 ─▶ guest preload ─▶ 壳：epoch++ ─▶ desktop/event browser:takeover
```

## 表面与信任

- 应用窗口可信。网站视图不可信：sandbox 开启，context isolation 开启，无 Node
  integration，无应用 preload，不能访问 `reasonix://`。它们唯一的 preload 只观察可信
  用户输入以请求接管，不向页面暴露任何东西。
- 分区：`persist:browser` 是同一 Reasonix 数据目录下共享的登录分区；`temp:<id>` 分区
  只存在于内存，最后一个标签关闭即丢弃。远程 Serve 窗口与 MCP App 框架使用各自分区，
  从不与浏览器分区共享。
- 壳中的 `BrowserSurfaceManager` 拥有创建、可见性、边界、焦点与销毁。React 面板提交
  布局矩形，壳按窗口校验后应用。任何应用覆盖层（对话框、菜单、命令面板）设置单一
  覆盖状态，隐藏所有原生网站视图，页面永远不能盖住应用。
- 一个任务拥有自己的标签。标签携带 `{tabID, taskID, sessionID, epoch, partition}`；
  切换可见标签不会把运行中的计划转到别的标签。

## 面板

右侧工作区加入浏览器面板：任务内标签栏、地址栏、前进/后退、刷新、缩放、可重试的
加载错误、下载列表、DevTools 开关。恢复的标签只保留安全导航条目 `{url, title}`；不
持久化表单状态、凭据或可重放提交。标签元数据与操作日志是桌面状态目录下新增的带
版本文件（`browser/tabs-v1.json`、`browser/operations-v1.json`）。

## 浏览器控制设置

设置中心的「浏览器控制」页管理下列开关，取值都保存在壳 userData 配置目录
（`desktop-shell/`）下的 `browser-control.json`。

- **内置浏览器控制**（`controlEnabled`，默认开启）。壳通过 `desktop/browserControl`
  推送给 Go，Go 在构建会话时读取：新会话完全不注册浏览器工具，正在运行的会话保持
  启动时的工具集。
- **忽略证书校验**（`ignoreCertificateErrors`，默认关闭）。只对访客会话放宽
  `setCertificateVerifyProc`：访客视图工厂准备分区时应用，开关变化时对全部存活的访客
  会话重新应用，无需重启。应用窗口与远程窗口仍使用 Chromium 默认校验。
- **清除内置浏览器缓存**：清除 `persist:browser` 的 HTTP 缓存以及 Cache Storage、
  Service Worker 和着色器缓存，保留 Cookie 与本地站点数据。
- **清除全部浏览器数据**：额外清空所有存储类型，内置浏览器中的所有站点都会退出登录；
  内存态 `temp:<id>` 分区不受影响。
- **导入 Chrome 登录状态**：读取最近使用的 Chrome Profile 的 `Cookies` 数据库，逐条
  解密（macOS 用登录钥匙串中的 `Chrome Safe Storage`，Linux 用众所周知的 `peanuts`
  口令，Windows 用 `Local State` 中的 DPAPI 主密钥）后写入 `persist:browser`。已过期
  的 Cookie、App-Bound（`v20`）取值与解密失败的行计入跳过；不会读取密码。

## Agent 能力

工具通过现有 capability 注册表注册为一个 `browser` 能力，包含下列操作。每个写操作
都走正常审批策略（`ask`、`allow`、`deny`）、正常取消上下文与证据轨迹；没有独立的
浏览器审批系统。

| 工具 | 读/写 | 用途 |
| --- | --- | --- |
| `browser_tabs` | 读 | 列出任务标签及 URL、标题、加载状态 |
| `browser_open` | 写 | 在共享或临时分区打开标签到某 URL |
| `browser_navigate` | 写 | 导航绑定标签（URL、后退、前进、刷新） |
| `browser_snapshot` | 读 | 带元素引用的结构快照 |
| `browser_screenshot` | 读 | 视口或元素的 PNG，作为图像返回 |
| `browser_click` | 写 | 点击引用元素（在其中心派发可信鼠标事件） |
| `browser_type` | 写 | 用可信键盘事件向引用元素输入文本；可选提交 |
| `browser_press` | 写 | 按键或组合键 |
| `browser_scroll` | 写 | 滚动视口或元素 |
| `browser_select` | 写 | 选择下拉选项 |
| `browser_upload` | 写 | 把任务文件附加到文件输入 |
| `browser_download` | 读 | 等待或列出标签的下载 |
| `browser_close` | 写 | 关闭标签 |

快照格式：在主框架及每个可达框架的隔离世界中生成的无障碍风格树
（`role "name" [state] ref=e12`）。引用绑定 `{tabID, frameID, documentVersion}`；导航、
页面替换或接管使所有早先引用失效，带过期引用的动作返回 `not_executed: stale reference`
而不是猜测。输入与点击由壳以可信输入事件派发，从不给元素赋值，因此 React 受控输入、
自定义控件与动态页面的行为与真实用户一致。

截图与下载从不经过控制帧：壳把它们写入 Go 在请求中指定的任务临时目录并返回路径；
Go 通过现有通道把文件转成图像或文件结果。上传只读取任务拥有的文件；远程任务通过
现有 SFTP 传输与任务临时目录中转，远程路径永远不会被当作本机路径。

## 所有权、接管与未知写入

- 授权 `{sessionID, taskID, runtimeGeneration, tabIDs, expiresAt}` 在任务开始使用浏览器
  时由 Go 签发，并在服务重启、会话变化、连接世代变化或任务结束时撤销。壳拒绝授权
  不是当前的 `host/browser.*` 调用。
- 浏览器工具栏的“接管”按钮通过可信应用 IPC 立即将标签切为 `human` 模式并递增 epoch。
  即使处于抑制 Agent 输入回声的 750 毫秒窗口内，显式接管仍立即生效；页面键盘、鼠标或
  触控输入的自动检测在该窗口内仅尽力而为。旧 epoch 的待执行与排队动作被取消，Go 收到
  `browser:takeover`。“恢复”将控制权交回 Agent 并再次递增 epoch，要求重新读取页面。
  登录页、验证码与 passkey 始终交由用户：标签处于 `human` 模式时 Agent 不能读取或操作它。
- 每个写操作在壳执行前先在账本中预留 `{operationID, sessionID, generation, tabID, epoch,
  documentToken, action, digest}`。壳报告 `executed` 或带原因的 `not_executed`；回复丢失、
  崩溃或服务重启使操作保持 `unknown`。未知操作展示给用户，绝不自动重放；重复的
  `operationID` 永久被拒绝。
- 网站视图渲染进程崩溃只取消该标签的动作，并以 `human` 模式重载最后的安全 URL。应用
  渲染进程崩溃暂停所有浏览器动作，直到界面重新挂载。

## 宿主调用

| 方法 | 用途 |
| --- | --- |
| `host/browser.grant` `revoke` | 安装或撤销授权 |
| `host/browser.tabs.list` `open` `close` `activate` `navigate` | 绑定授权的标签生命周期 |
| `host/browser.snapshot` | 标签结构快照，返回 `documentToken` |
| `host/browser.act` | 一个已预留的动作；返回 `{executed, reason, documentToken}` |
| `host/browser.screenshot` | 捕获到任务拥有的文件路径 |
| `host/browser.downloads` | 列出或等待标签的下载 |
| `host/browser.layout` | 应用面板矩形与覆盖状态 |

壳发出的事件：`browser:tabs`（标签列表变化）、`browser:takeover`（`{tabID, epoch, reason}`）、
`browser:download`（进度）、`browser:crash`。

## 远程 Agent

远程 Reasonix Agent 通过现有 SSH 连接与转发管理器，以承载同一 `BrowserExecutor` 契约的
受限 Host RPC 使用本机浏览器。授权绑定远程连接世代、会话与任务；断线、重连或切换会话
即撤销。浏览器授权与 provider 代理凭据分开；没有共享 token。旧的远程 Serve 构建通过
能力协商不宣告浏览器，保留全部现有远程功能。

线上形态是每台桌面一个 loopback HTTP broker。全新 Serve 的 bootstrap 把
`REASONIX_BROWSER_BROKER` / `REASONIX_BROWSER_TOKEN`（仅进程环境）指向经反向转发的
broker；被复用的 Serve 在桌面轮换路由后通过 `POST /browser/broker` 重新指向。broker 为
每个主机连接世代铸一个随机 bearer token——注册新世代即替换主机旧 token——鉴权后才把
`/v1/browser/<method>` 派发给 `browser.Executor`。每个请求携带
`X-Reasonix-Browser-Session`；broker 把它解析到唯一展示该会话的桌面标签，其余一律以
`no_grant` 拒绝。壳在桌面写出的截图与下载经现有 SFTP 通道中转进远程主机的每工作区
暂存目录（`~/.reasonix/browser-relay/<workspace>/`），serve 侧工具只读对它们而言是
本机的路径。带 broker 启动的 Serve 在 `/auth/token` 握手的
`X-Reasonix-Serve-Capabilities` 响应头中宣告 `browser`。

## 验收

iframe、动态 DOM、受控输入、弹窗、上传与下载、导航历史、临时分区、共享与隔离登录；
审批前接管、审批后派发前接管、执行后回执丢失、崩溃后重启、重复 operation ID；远程
SSH 断连、世代变化、过期授权、跨会话误路由。[迁移记录](DESKTOP_SHELL_MIGRATION.zh-CN.md#验收门槛)
中的真实任务关闭本阶段。
