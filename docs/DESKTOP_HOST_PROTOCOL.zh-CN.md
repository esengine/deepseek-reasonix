# 桌面宿主协议

[English](DESKTOP_HOST_PROTOCOL.md)

Electron 壳与 Go 桌面服务是两个进程，通过服务进程 stdio 上的一条私有、带版本的
JSON-RPC 2.0 连接协作。本文是双方共同实现的契约：Go 侧拥有全部桌面业务命令，
Electron 侧拥有全部原生界面。任何一方都不得绕过契约：React 界面不触碰 Electron
或 Go 全局对象，Go 业务代码不链接任何壳工具包。

```text
React 渲染进程 ──preload 类型化 IPC──▶ Electron 主进程 ──stdio JSON-RPC──▶ Go 桌面服务
                                          ▲                                   │
                                          └────── host/* 反向请求 ────────────┘
```

## 传输

- 帧格式：按行分隔的 JSON-RPC 2.0（`rpcwire` 严格模式）。一行一帧，UTF-8，不允许批量数组。
- Go 服务以 `reasonix-desktop --host-rpc` 启动。stdout 只承载协议帧，stderr 承载日志。
  壳在 `desktop/shutdown` 之后关闭 stdin 以请求退出。
- 限制：双向单帧 64 MiB，服务端最多 512 个并发入站处理器，30 秒写入停滞看门狗。
  大体积二进制数据从不进入帧，而是走下文的资源源。
- 壳发出的每个请求都在独立 goroutine 上执行，与已退役的进程内壳的绑定调用一致。只有
  `desktop/event` 帧保证顺序，它们由服务端的单一队列写出。

## 握手

新连接上的第一个请求必须是 `desktop/hello`，否则以 `-32002 not_ready` 失败。

```jsonc
// 壳 → 服务
{"method":"desktop/hello","params":{
  "protocolVersion": 1,
  "contractDigest": "sha256:…",       // 壳包内嵌的契约摘要
  "build": {"version":"v1.30.0","channel":"stable","commit":"abc123"},
  "host": {"name":"electron","version":"44.2.0","chrome":"152.0.0","platform":"darwin","arch":"arm64"},
  "instance": {"home":"/Users/…/.reasonix","dev":false}
}}
// 服务 → 壳
{"result":{
  "protocolVersion": 1,
  "contractDigest": "sha256:…",
  "service": {"version":"v1.30.0","channel":"stable","commit":"abc123","pid":4242},
  "runtimeGeneration": "g-01J…",       // 每个服务进程唯一
  "resources": {"origin":"http://127.0.0.1:51234","token":"…"},
  "window": {"width":1280,"height":820,"minWidth":760,"minHeight":480,"frameless":false,"zoomFactor":1}
}}
```

`window` 是 Go 根据保存状态和平台规则得到的主窗口初始几何；壳按它隐藏创建窗口，
Go 随后在 `domReady` 中通过 `host/window.*` 定位、最大化并显示。

失败码都是终止性的：壳显示真实错误，提供“打开日志”和“退出”，绝不回退到浏览器 mock。

| 代码 | 名称 | 含义 |
| --- | --- | --- |
| `-32001` | `protocol_mismatch` | `protocolVersion` 不一致 |
| `-32003` | `contract_mismatch` | 命令/事件摘要不一致（混装） |
| `-32004` | `build_mismatch` | 壳与服务版本不同且都不是 `dev` |
| `-32005` | `instance_mismatch` | 壳的规范数据目录与服务的不一致 |
| `-32002` | `not_ready` | hello 成功前的请求 |

`runtimeGeneration` 标记该服务进程发出的每个事件、每个审批和浏览器授权。服务重启
后产生新的世代；壳丢弃任何旧世代标记的内容。

## 生命周期请求（壳 → 服务）

| 方法 | 参数 | 结果 | Go 负责者 |
| --- | --- | --- | --- |
| `desktop/start` | `{}` | `{}` | `App.startup` |
| `desktop/domReady` | `{}` | `{}` | `App.domReady` |
| `desktop/rendererAttached` | `{"rendererGeneration":n}` | `{}` | 前端心跳/就绪 |
| `desktop/beforeClose` | `{"reason":"window"\|"quit"\|"tray"\|"updater"}` | `{"prevent":bool}` | `App.beforeClose` |
| `desktop/shutdown` | `{}` | `{}` | `App.shutdown` |
| `desktop/hostEvent` | `{"name":string,"payload":any}` | `{}` | 第二实例、托盘打开/退出、菜单动作 |
| `desktop/browserControl` | `{"enabled":bool}` | `{}` | 内置浏览器开关，构建会话时读取 |

顺序：`hello` → `start` → 窗口加载 → `domReady` →（每次渲染进程挂载后 `rendererAttached`）
→ … → `beforeClose` →（`shutdown` → 关闭 stdin → 退出）。无论是否调用过 `shutdown`，
stdin 关闭后服务都会自行退出，因此壳突然死亡不会留下无头 Go 进程。

## 业务命令

```jsonc
{"method":"desktop/invoke","params":{"method":"OpenProjectTab","args":["/path", true]}}
{"result": {...}}                                  // 方法的 JSON 结果，void 为 null
{"error":{"code":-32000,"message":"<错误文本>","data":{"method":"OpenProjectTab"}}}
```

`method` 必须是契约注册表接受的 Go `App` 导出方法。签名沿用已退役壳的规则：任意可
JSON 序列化的参数，结果为 `()`、`(T)`、`(error)` 或 `(T, error)`。注册表在构建期拒绝
其他形态，因此接口面不可能出现壳无法调用的方法。壳在转发前按内嵌命令表校验
`method`；未知名称以 `-32601` 失败。

生成的契约（`cd desktop && go run . -emit-contract frontend/src/generated`）是唯一
事实来源：它输出 JSON 契约、摘要、TypeScript 命令表和渲染进程使用的 DTO 类型声明。
检入的输出漂移时桌面 Go 测试失败。

每个命令还记录源码模块 `domain`、准确的 `owner`（例如 `App.OpenProjectTab`）、
仓库相对路径 `sources`、`scope` 和 `cancellation`，这些字段共同参与摘要。
生成器扫描所有平台声明并输出 `desktop/host_command_owners.generated.json`；宿主内嵌
该文件，要求每个反射命令都有匹配元数据。Scope 记录原方法的命名 wire `inputs`
（遗留无名参数使用 `argN`）和 `resolver`；无参数命令使用 `owner-state`，其余使用
`owner-inputs`。这些字段描述来源和分派边界；输入校验、标签/会话选择及权限检查仍由
原有 App 方法负责。

当前 App 命令声明 `before-dispatch`：宿主在解码前及真正分派前检查取消；同步写入
开始后，即使收到取消也保留原方法的结果，不承诺中断已分派的方法。宿主方法可通过
首个 Go 参数 `context.Context` 声明 `cooperative-context`；宿主注入请求 context，
它不属于 JSON 参数或生成的 DTO。方法自身必须配合取消。业务 Stop/Cancel 命令继续
沿用已有 owner 和语义。

## 事件（服务 → 壳 → 渲染进程）

```jsonc
{"method":"desktop/event","params":{"seq":1093,"generation":"g-01J…","name":"agent:event","args":[{...}]}}
```

`args` 保留原事件桥的可变参数载荷，多数事件只有一个元素。壳把该帧经
`reasonix:event` 通道转给渲染进程；preload 的 `on(name, cb)` 按 `name` 过滤并调用
`cb(...args)`。序号在同一世代内严格递增，重新挂载的渲染进程可据此发现缺口并重新
快照，而不是信任陈旧状态。

服务监督器和 preload 都拒绝重复、倒序帧；preload 还根据当前服务状态拒绝旧世代帧，
并在 React 订阅前接入传输。世代变化、序号缺口或订阅期间遗漏会触发壳内事件
`desktop:resync`（`generation`、`reason`、`expectedSeq`、`actualSeq`），它不属于 Go
业务事件。运行状态通过 `SyncRuntimeState` 重读；已挂载控制器重读 `ListTabs`，复用
现有 `TurnEventsForTab` 日志与待审批提示恢复投影。异步读取受后续恢复请求、会话身份
和导航变更约束，不重放业务调用。服务重启后复用仍存活的应用渲染进程，保留未发送草稿。

当前保证范围是核心运行状态、会话元数据、持久化轮次事件和待审批提示。终端虽有有界
输出快照，但没有原子输出游标，因此缺口后的终端会明确标记输出可能不完整，不将无法
确定边界的快照合并进实时输出。扩展输出、文件监听等独立事件流仍需各自的重新快照契约，
不在上述核心恢复保证范围内。

## 原生宿主调用（服务 → 壳）

这些调用替代 Go 中对壳工具包的直接调用。每一项对应 Go `nativeHost` 接口的一个方法；
Wails 实现已随 Electron 壳落地删除。

| 方法 | 参数 | 结果 |
| --- | --- | --- |
| `host/window.show` | `{"reason":string}` | `{}` |
| `host/window.hide` | `{}` | `{}` |
| `host/app.hide` | `{}` | `{}`（macOS 应用级隐藏） |
| `host/window.maximise` `unmaximise` `minimise` `unminimise` `toggleMaximise` `center` | `{}` | `{}` |
| `host/window.isMaximised` `isMinimised` | `{}` | `{"value":bool}` |
| `host/window.setPosition` | `{"x":n,"y":n}` | `{}` |
| `host/window.setTitle` | `{"title":string}` | `{}` |
| `host/screen.list` | `{}` | `{"screens":[{"x","y","width","height","scale","primary"}]}` |
| `host/dialog.openDirectory` | `{"title","defaultDirectory"}` | `{"path":string}`（`""` 表示取消） |
| `host/dialog.openFile` | `{"title","defaultDirectory","filters":[{"displayName","pattern"}],"multiple":bool}` | `{"paths":[]}` |
| `host/dialog.saveFile` | `{"title","defaultDirectory","defaultFilename","filters"}` | `{"path":string}` |
| `host/dialog.message` | `{"type":"info"\|"warning"\|"error"\|"question","title","message","buttons":[],"defaultButton","cancelButton"}` | `{"button":string}` |
| `host/shell.openExternal` | `{"url":string}` | `{}` |
| `host/app.quit` | `{}` | `{}` |
| `host/app.relaunch` | `{"args":[],"execPath"?:string}` | `{}` |
| `host/devtools.toggle` | `{}` | `{}` |
| `host/remoteWindow.open` | `{"hostKey","url","title"}` | `{"windowId":string}` |
| `host/remoteWindow.navigate` | `{"hostKey","url","title"}` | `{}` |
| `host/remoteWindow.focus` `close` | `{"hostKey"}` | `{}` |
| `host/tray.ensure` | `{"openTitle","openTooltip","quitTitle","quitTooltip","tooltip"}` | `{"ready":bool,"reason":string}` |

`host/shell.openExternal` 只接受 `http:`、`https:` 和 `mailto:` URL。
包括 `file:`、`javascript:`、`data:` 在内的其他协议会在 Electron 宿主边界
被拒绝，不会交给系统打开器执行。
| `host/tray.destroy` | `{}` | `{}` |
| `host/browser.grant` `revoke` | `{"grantId","tabId","sessionId"}` / `{"grantId"}` | `{}` |
| `host/browser.tabs.list` | `{"grantId"}` | `{"tabs":[{"id","url","title","loading","temporary"}]}` |
| `host/browser.tabs.open` | `{"grantId","url","temporary"}` | tab |
| `host/browser.tabs.navigate` | `{"grantId","tabId","url","action"}` | tab |
| `host/browser.tabs.close` | `{"grantId","tabId"}` | `{}` |
| `host/browser.snapshot` | `{"grantId","tabId","selector"}` | `{"documentToken","url","title","tree","refs"}` |
| `host/browser.act` | `{"grantId","operationId","tabId","documentToken","action","ref","text","keys","options","files","submit","deltaX","deltaY"}` | `{"executed","reason","documentToken"}` |
| `host/browser.screenshot` | `{"grantId","tabId","ref","fullPage","directory"}` | `{"path","mime","width","height"}` |
| `host/browser.downloads` | `{"grantId","tabId","waitForMs"}` | `{"downloads":[{"id","url","path","state","bytes"}]}` |

浏览器调用以 `-32010`（引用过期）、`-32011`（用户已接管该标签）或 `-32012`（没有当前授权）
失败；Go 执行器把它们映射到内核哨兵错误，并把操作结果记入账本。授权的 `tabId` 是桌面
标签（任务）；该授权下打开的浏览器标签归属于它。

宿主事件（`desktop/hostEvent`）：`tray.open`、`tray.quit`、`secondInstance`（`payload` 携带
原始 argv）、`menu.showWindow`、`remoteWindow.closed`（`{"hostKey"}`）、`browser.takeover`
（`{"tabId","epoch","reason"}`）。

对话框结果从不暴露文件内容；它们只返回路径，再由 Go 经现有工作区和媒体检查授权。

## 资源源

服务在回环端口上监听，承载现有的授权资源处理器（`/__reasonix_workspace_media/…`、
`/__reasonix_theme_asset/…`、远程 markdown 图片代理）。壳从受限的 `reasonix://app/`
scheme 提供打包界面，只把上述前缀转发到资源源，并在主进程中附加
`Authorization: Bearer <token>`。token 从不到达渲染进程、网站视图、远程窗口或
MCP App 框架。Go 保留今天的全部文件身份与 TTL 检查。

## 渲染进程 preload 接口

可信 preload 只暴露一个对象 `window.reasonixDesktop`：

```ts
interface ReasonixDesktopHost {
  readonly kind: "electron";
  readonly contract: { protocolVersion: number; digest: string; commands: readonly string[] };
  readonly platform: { os: "darwin" | "windows" | "linux"; arch: string; versions: Record<string, string> };
  invoke(method: string, args: unknown[]): Promise<unknown>;
  on(name: string, cb: (...args: unknown[]) => void): () => void;
  native: {
    openExternal(url: string): Promise<void>;
    clipboard: { writeText(text: string): Promise<boolean>; readText(): Promise<string> };
    window: {
      setTheme(theme: "system" | "light" | "dark"): void;
      setBackgroundColour(r: number, g: number, b: number, a: number): void;
      getBounds(): Promise<{ x: number; y: number; width: number; height: number; maximised: boolean }>;
      isMaximised(): Promise<boolean>;
      minimise(): void; toggleMaximise(): void; close(): void;
    };
    getPathForFile(file: File): string;          // 原生拖放路径
    onServiceState(cb: (state: ServiceState) => void): () => void;
    browserControl: {                            // 内置浏览器的设置页
      get(): Promise<BrowserControlState | null>;
      setEnabled(enabled: boolean): Promise<BrowserControlState>;
      setIgnoreCertificateErrors(enabled: boolean): Promise<BrowserControlState>;
      clearCache(): Promise<void>;               // 保留 Cookie 与站点数据
      clearAllData(): Promise<void>;             // Cookie、站点数据与缓存
      importChromeLogin(): Promise<ChromeImportOutcome>;
    };
  };
  browser: {                                       // 用户驱动的浏览器面板；Agent 调用经 Go
    list(): Promise<BrowserTabView[]>;
    open(url: string, opts?: { temporary?: boolean; taskId?: string }): Promise<BrowserTabView>;
    close(tabId: string): Promise<void>;
    activate(tabId: string | null): Promise<void>;
    navigate(tabId: string, target: { url?: string; action?: "back" | "forward" | "reload" | "stop" }): Promise<void>;
    setZoom(tabId: string, factor: number): Promise<void>;
    toggleDevTools(tabId: string): Promise<void>;
    resume(tabId: string): Promise<void>;          // 把接管的标签交还给 Agent
    setLayout(rect: { x: number; y: number; width: number; height: number } | null): void;
    setOverlay(active: boolean): void;             // 应用覆盖层隐藏所有网站视图
    onTabs(cb: (tabs: BrowserTabView[]) => void): () => void;
    onDownload(cb: (download: BrowserDownloadView) => void): () => void;
  };
}
```

`BrowserTabView` 为 `{ id, taskId, url, title, loading, canGoBack, canGoForward,
temporary, mode: "agent" | "human", epoch, zoom, error }`，`BrowserDownloadView` 为
`{ id, tabId, url, filename, path, state, received, total }`。网站视图位于
`persist:browser`（共享登录）或 `temp:<id>` 分区，永远不会获得应用 preload。

`ServiceState` 为 `{ phase: "starting" | "ready" | "restarting" | "failed" | "exited"; generation: string; error?: string }`。
业务组件只导入类型化 SDK，从不直接使用该对象；只有桥接适配层读取它。

`BrowserControlState` 为 `{ controlEnabled, ignoreCertificateErrors, writable,
warning: "invalid-config" | "unreadable-config" | "unsupported-version" | null }`，
`ChromeImportOutcome` 为 `{ ok: true, profile, cookies, skipped }` 或
`{ ok: false, reason }`，`reason` 取值 `chrome-missing`、`profile-not-found`、
`cookies-unreadable`、`safe-storage-denied`、`safe-storage-unavailable`、
`unsupported-platform`。

## 安全边界

- 应用窗口：sandbox 开启，context isolation 开启，Node integration 关闭，只加载
  `reasonix://app`，使用上述 preload。
- 网站视图、远程 Serve 窗口和 MCP App 框架：独立 session，没有应用 preload，不能访问
  `reasonix://`，不能触达 `host/*`。
- IPC 处理器只接受来自应用窗口 `webContents` 的请求，其他发送者被拒绝并记录。
- 内嵌契约之外的 `desktop/invoke` 名称在到达 Go 之前失败。
