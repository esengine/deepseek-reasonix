# Desktop host protocol

[简体中文](DESKTOP_HOST_PROTOCOL.zh-CN.md)

The Electron shell and the Go desktop service are two processes joined by one
private, versioned JSON-RPC 2.0 connection over the service's stdio. This
document is the contract both sides implement. The Go side owns every desktop
business command; the Electron side owns every native surface. Neither side
may reach around the contract: the React UI never touches Electron or Go
globals, and Go business code never links a shell toolkit.

```text
React renderer ──typed IPC (preload)──▶ Electron main ──stdio JSON-RPC──▶ Go desktop service
                                          ▲                                   │
                                          └────── host/* reverse requests ────┘
```

## Transport

- Framing: newline-delimited JSON-RPC 2.0 (`rpcwire` strict mode). One frame
  per line, UTF-8, no batch arrays.
- The Go service is started as `reasonix-desktop --host-rpc`. Its stdout carries
  only protocol frames; stderr carries logs. The shell closes stdin to request
  exit after `desktop/shutdown`.
- Limits: 64 MiB per inbound frame on both sides, 512 concurrent inbound
  handlers on the service, 30 s write-stall watchdog. Large binary payloads never
  travel in frames; they use the resource origin below.
- Every request the shell makes runs on its own goroutine, exactly as the
  retired in-process shell
  bound calls did. Ordering is only guaranteed for `desktop/event` frames,
  which the service writes from one queue.

## Handshake

The first request on a fresh connection must be `desktop/hello`. Anything else
fails with `-32002 not_ready`.

```jsonc
// shell → service
{"method":"desktop/hello","params":{
  "protocolVersion": 1,
  "contractDigest": "sha256:…",       // digest embedded in the shell bundle
  "build": {"version":"v1.30.0","channel":"stable","commit":"abc123"},
  "host": {"name":"electron","version":"44.2.0","chrome":"152.0.0","platform":"darwin","arch":"arm64"},
  "instance": {"home":"/Users/…/.reasonix","dev":false}
}}
// service → shell
{"result":{
  "protocolVersion": 1,
  "contractDigest": "sha256:…",
  "service": {"version":"v1.30.0","channel":"stable","commit":"abc123","pid":4242},
  "runtimeGeneration": "g-01J…",       // new for every service process
  "resources": {"origin":"http://127.0.0.1:51234","token":"…"},
  "window": {"width":1280,"height":820,"minWidth":760,"minHeight":480,"frameless":false,"zoomFactor":1}
}}
```

`window` is the initial main-window geometry Go derives from the saved state
and platform rules; the shell creates the window hidden with it and Go later
positions, maximises and shows it through `host/window.*` from `domReady`.

Failure codes are terminal: the shell shows the real error and offers
"open logs" and "quit". It never falls back to the browser mock.

| Code | Name | Meaning |
| --- | --- | --- |
| `-32001` | `protocol_mismatch` | `protocolVersion` differs |
| `-32003` | `contract_mismatch` | command/event digest differs (mixed install) |
| `-32004` | `build_mismatch` | shell and service versions differ and neither is `dev` |
| `-32005` | `instance_mismatch` | the shell's canonical data home differs from the service's |
| `-32002` | `not_ready` | request before a successful hello |

`runtimeGeneration` tags every event and every approval or browser grant
minted by this service process. A restarted service issues a new generation;
the shell discards anything tagged with an old one.

## Lifecycle requests (shell → service)

| Method | Params | Result | Go owner |
| --- | --- | --- | --- |
| `desktop/start` | `{}` | `{}` | `App.startup` |
| `desktop/domReady` | `{}` | `{}` | `App.domReady` |
| `desktop/rendererAttached` | `{"rendererGeneration":n}` | `{}` | frontend heartbeat/readiness |
| `desktop/beforeClose` | `{"reason":"window"\|"quit"\|"tray"\|"updater"}` | `{"prevent":bool}` | `App.beforeClose` |
| `desktop/shutdown` | `{}` | `{}` | `App.shutdown` |
| `desktop/hostEvent` | `{"name":string,"payload":any}` | `{}` | second instance, tray open/quit, menu actions |
| `desktop/browserControl` | `{"enabled":bool}` | `{}` | built-in browser switch, read when a session is built |

Order: `hello` → `start` → window load → `domReady` → (`rendererAttached` after
each renderer mount) → … → `beforeClose` → (`shutdown` → stdin close → exit).
The service exits on its own when stdin closes, whether or not `shutdown` was
called, so an abrupt shell death never leaves a headless Go process behind.

## Business commands

```jsonc
{"method":"desktop/invoke","params":{"method":"OpenProjectTab","args":["/path", true]}}
{"result": {...}}                                  // the method's JSON result, null for void
{"error":{"code":-32000,"message":"<error text>","data":{"method":"OpenProjectTab"}}}
```

`method` must name an exported method of the Go `App` value that the contract
registry accepted. Signatures follow the rules the retired shell used: any
  JSON-serialisable
parameters and a result of `()`, `(T)`, `(error)` or `(T, error)`. The
registry rejects anything else at build time, so the surface can never gain a
method the shell cannot call. The shell validates `method` against the
embedded command list before forwarding. Unknown names fail with `-32601`.

The generated contract (`cd desktop && go run . -emit-contract frontend/src/generated`)
is the single source of truth: it emits the JSON contract, its digest, the
TypeScript command table and the DTO type declarations consumed by the
renderer. A desktop Go test fails when the checked-in output drifts.

Each command also records its source-module `domain`, exact `owner` (for
example `App.OpenProjectTab`), repository-relative `sources`, `scope` and
`cancellation`; these fields are included in the digest. The generator scans
all platform declarations and writes `desktop/host_command_owners.generated.json`,
which the host embeds and validates against every reflected command. Scope
records the owner's named wire `inputs` (`argN` for unnamed legacy parameters)
and `resolver`; zero-input commands use `owner-state`, others `owner-inputs`.
These are provenance and dispatch boundaries. Input validation, tab/session
selection and access checks remain in the existing App method.

Current App commands declare `before-dispatch`: the host checks cancellation
before decoding and immediately before dispatch, then preserves the method's
result even if cancellation arrives during a synchronous write. They do not
promise interruption after dispatch. A host method may opt into
`cooperative-context` with a leading Go `context.Context`; the host injects
the request context and excludes it from JSON arguments and generated DTOs.
The method must cooperate with cancellation. Business Stop/Cancel commands
continue to use their existing owners and semantics.

## Events (service → shell → renderer)

```jsonc
{"method":"desktop/event","params":{"seq":1093,"generation":"g-01J…","name":"agent:event","args":[{...}]}}
```

`args` preserves the variadic payload of the previous event bridge; most
events carry one element. The shell forwards the frame to the renderer on the
`reasonix:event` channel; the preload API `on(name, cb)` filters by `name` and
calls `cb(...args)`. Sequence numbers are strictly increasing per generation
so a renderer that re-attaches can detect a gap and re-snapshot instead of
trusting stale state.

Both the service supervisor and preload reject duplicate or out-of-order
frames; the preload also rejects old generations using the current service
state. It binds the transport before React subscribes. A generation change,
sequence gap or missed subscription raises the shell-local `desktop:resync`
event (`generation`, `reason`, `expectedSeq`, `actualSeq`), which is not a Go
business event. Runtime state is re-read through `SyncRuntimeState`; mounted
controllers re-read `ListTabs` and use the existing `TurnEventsForTab` ledger
and pending-prompt presentation to repair their projection. Reads are fenced
against newer recovery requests and session/navigation changes. No business
mutation is replayed, and a surviving application renderer is reattached
after a service restart without reloading its unsent drafts.

This recovery currently covers core runtime state, session metadata, durable
turn events and pending prompts. Terminal output has a bounded snapshot but
no atomic output cursor, so an affected terminal is visibly marked incomplete
instead of merging an ambiguous snapshot into live output. Extension output,
file-watch and other independent event streams still need capability-specific
resnapshot contracts; they are not covered by this core recovery guarantee.

## Native host calls (service → shell)

These replace direct shell-toolkit calls in Go. Each maps to one method of the
Go `nativeHost` interface; the Wails implementation was retired when the Electron shell
landed.

| Method | Params | Result |
| --- | --- | --- |
| `host/window.show` | `{"reason":string}` | `{}` |
| `host/window.hide` | `{}` | `{}` |
| `host/app.hide` | `{}` | `{}` (macOS application hide) |
| `host/window.maximise` `unmaximise` `minimise` `unminimise` `toggleMaximise` `center` | `{}` | `{}` |
| `host/window.isMaximised` `isMinimised` | `{}` | `{"value":bool}` |
| `host/window.setPosition` | `{"x":n,"y":n}` | `{}` |
| `host/window.setTitle` | `{"title":string}` | `{}` |
| `host/screen.list` | `{}` | `{"screens":[{"x","y","width","height","scale","primary"}]}` |
| `host/dialog.openDirectory` | `{"title","defaultDirectory"}` | `{"path":string}` (`""` = cancelled) |
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

`host/shell.openExternal` accepts only `http:`, `https:`, and `mailto:` URLs.
Other schemes, including `file:`, `javascript:`, and `data:`, are rejected at
the Electron host boundary before the system opener is invoked.
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

Browser calls fail with `-32010` (stale reference), `-32011` (the user took the
tab over) or `-32012` (no current grant); the Go executor maps them onto the
kernel sentinels and records the operation outcome in its ledger. Grant
`tabId` is the desktop tab (the task); browser tabs opened under that grant
belong to it.

Host events (`desktop/hostEvent`): `tray.open`, `tray.quit`, `secondInstance`
(raw argv in `payload`), `menu.showWindow`, `remoteWindow.closed`
(`{"hostKey"}`), `browser.takeover` (`{"tabId","epoch","reason"}`).

Dialog results never expose file contents; they return paths that Go then
authorises through the existing workspace and media checks.

## Resource origin

The service listens on a loopback port for the existing authorised asset
handlers (`/__reasonix_workspace_media/…`, `/__reasonix_theme_asset/…`, the
remote markdown image proxy). The shell serves the packaged UI from the
privileged `reasonix://app/` scheme and forwards only those prefixes to the
resource origin, adding `Authorization: Bearer <token>` in the main process.
The token never reaches the renderer, a website view, a remote window or an
MCP App frame. Go keeps every file-identity and TTL check it has today.

## Renderer preload API

The trusted preload exposes exactly one object, `window.reasonixDesktop`:

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
    getPathForFile(file: File): string;          // native drop paths
    onServiceState(cb: (state: ServiceState) => void): () => void;
    browserControl: {                            // settings page for the built-in browser
      get(): Promise<BrowserControlState | null>;
      setEnabled(enabled: boolean): Promise<BrowserControlState>;
      setIgnoreCertificateErrors(enabled: boolean): Promise<BrowserControlState>;
      clearCache(): Promise<void>;               // keeps cookies and site data
      clearAllData(): Promise<void>;             // cookies, site data and cache
      importChromeLogin(): Promise<ChromeImportOutcome>;
    };
  };
  browser: {                                       // user-driven browser panel; agent calls go through Go
    list(): Promise<BrowserTabView[]>;
    open(url: string, opts?: { temporary?: boolean; taskId?: string }): Promise<BrowserTabView>;
    close(tabId: string): Promise<void>;
    activate(tabId: string | null): Promise<void>;
    navigate(tabId: string, target: { url?: string; action?: "back" | "forward" | "reload" | "stop" }): Promise<void>;
    setZoom(tabId: string, factor: number): Promise<void>;
    toggleDevTools(tabId: string): Promise<void>;
    resume(tabId: string): Promise<void>;          // hand a taken-over tab back to the agent
    setLayout(rect: { x: number; y: number; width: number; height: number } | null): void;
    setOverlay(active: boolean): void;             // app overlays hide every website view
    onTabs(cb: (tabs: BrowserTabView[]) => void): () => void;
    onDownload(cb: (download: BrowserDownloadView) => void): () => void;
  };
}
```

`BrowserTabView` is `{ id, taskId, url, title, loading, canGoBack, canGoForward,
temporary, mode: "agent" | "human", epoch, zoom, error }` and
`BrowserDownloadView` is `{ id, tabId, url, filename, path, state, received,
total }`. Website views live in `persist:browser` (shared logins) or
`temp:<id>` partitions and never receive the application preload.

`ServiceState` is `{ phase: "starting" | "ready" | "restarting" | "failed" | "exited"; generation: string; error?: string }`.
Business components import the typed SDK, never this object; only the bridge
adapter reads it.

`BrowserControlState` is `{ controlEnabled, ignoreCertificateErrors, writable,
warning: "invalid-config" | "unreadable-config" | "unsupported-version" | null }`
and `ChromeImportOutcome` is either `{ ok: true, profile, cookies, skipped }` or
`{ ok: false, reason }` with `reason` one of `chrome-missing`,
`profile-not-found`, `cookies-unreadable`, `safe-storage-denied`,
`safe-storage-unavailable`, `unsupported-platform`.

## Security boundaries

- The application window: sandbox on, context isolation on, Node integration
  off, `reasonix://app` only, preload above.
- Website views, remote Serve windows and MCP App frames: separate sessions,
  no preload from the application, no `reasonix://` access, no `host/*` reach.
- IPC handlers accept requests only from the application window's
  `webContents`. Any other sender is rejected and logged.
- `desktop/invoke` names outside the embedded contract fail before reaching Go.
