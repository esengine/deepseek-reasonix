# Desktop browser

[简体中文](DESKTOP_BROWSER.zh-CN.md)

The desktop browser is a native Chromium surface inside the Reasonix window
that the user and the agent operate together. Websites render in Electron
`WebContentsView`s owned by the shell; every agent capability goes through the
Go desktop service so that local and remote agents, approvals, cancellation,
evidence and operation records share one implementation. This document is the
contract between the browser panel, the shell's surface manager, the Go
`BrowserExecutor` and the tools the agent sees.

```text
agent tool call ─▶ Go BrowserExecutor ─▶ ledger.reserve ─▶ host/browser.* ─▶ WebContentsView
       ▲                   │                                     │
       └── result/evidence ┴──────────── ledger.settle ◀─────────┘
user input on the page ─▶ guest preload ─▶ shell: epoch++ ─▶ desktop/event browser:takeover
```

## Surfaces and trust

- The application window is trusted. Website views are not: sandbox on,
  context isolation on, no Node integration, no application preload, no
  `reasonix://` access. Their only preload observes trusted user input to
  request a take-over and exposes nothing to the page.
- Partitions: `persist:browser` is the shared login partition for one
  Reasonix data home; `temp:<id>` partitions are in-memory and discarded when
  their last tab closes. Remote Serve windows and MCP App frames use their
  own partitions and never share the browser partition.
- The `BrowserSurfaceManager` in the shell owns creation, visibility, bounds,
  focus and destruction. The React panel submits a layout rectangle; the shell
  validates it against the window and applies it. Any application overlay
  (dialogs, menus, command palette) sets a single overlay state that hides
  every native website view so a page can never paint over the app.
- One task owns its tabs. A tab carries `{tabID, taskID, sessionID, epoch,
  partition}`; switching the visible tab never retargets a running plan.

## Panel

The right workspace gains a browser panel: tab strip per task, address bar,
back/forward, reload, zoom, load errors with retry, download list, DevTools
toggle. Restored tabs keep only `{url, title}` for safe navigation entries;
no form state, credentials or replayable submissions are persisted. Tab
metadata and the operation log are new versioned files under the desktop
state directory (`browser/tabs-v1.json`, `browser/operations-v1.json`).

## Browser control settings

The Settings Centre page "Browser control" owns the switches below, all stored
in `browser-control.json` inside the shell's userData profile (`desktop-shell/`).

- **Built-in browser control** (`controlEnabled`, default on). The shell pushes
  it to Go with `desktop/browserControl`, and Go reads it when it builds a
  session: a new session registers no browser tool at all, while a running
  session keeps the tool set it started with.
- **Ignore certificate errors** (`ignoreCertificateErrors`, default off).
  Relaxes `setCertificateVerifyProc` for guest sessions only. It is applied when
  the guest-view factory prepares a partition and re-applied to every live guest
  session when the switch changes, so no restart is needed.
- **Clear built-in browser cache** clears the HTTP cache plus Cache Storage,
  Service Workers and the shader cache of `persist:browser`, keeping cookies and
  local site data.
- **Clear all browser data** additionally drops every storage type, which signs
  every site in the built-in browser out. In-memory `temp:<id>` partitions are
  unaffected.
- **Import Chrome sign-in state** reads the newest Chrome profile's `Cookies`
  database, decrypts each value (macOS: the login keychain's `Chrome Safe
  Storage` secret; Linux: the well-known `peanuts` password; Windows: the DPAPI
  master key from `Local State`) and writes it into `persist:browser`. Expired
  cookies, App-Bound (`v20`) values and rows that fail to decrypt are counted as
  skipped. Passwords are never read.

## Agent capabilities

Tools are registered through the existing capability registry as one
`browser` capability with these operations. Every write goes through the
normal approval policy (`ask`, `allow`, `deny`), the normal cancellation
context and the evidence trajectory; there is no separate browser approval
system.

| Tool | Reads/Writes | Purpose |
| --- | --- | --- |
| `browser_tabs` | read | list the task's tabs with URL, title, loading state |
| `browser_open` | write | open a tab (shared or temporary partition) at a URL |
| `browser_navigate` | write | navigate the bound tab (URL, back, forward, reload) |
| `browser_snapshot` | read | structural snapshot with element references |
| `browser_screenshot` | read | PNG of the viewport or an element, returned as an image |
| `browser_click` | write | click a referenced element (trusted mouse events at its centre) |
| `browser_type` | write | type text into a referenced element with trusted key events; optional submit |
| `browser_press` | write | press a key or chord |
| `browser_scroll` | write | scroll the viewport or an element |
| `browser_select` | write | choose options in a select |
| `browser_upload` | write | attach task files to a file input |
| `browser_download` | read | wait for or list downloads of the tab |
| `browser_close` | write | close a tab |

Snapshot format: an accessibility-style tree (`role "name" [state] ref=e12`)
produced in an isolated world of the main frame and each reachable frame.
References are bound to `{tabID, frameID, documentVersion}`; a navigation,
page replacement or take-over invalidates every earlier reference, and an
action with a stale reference returns `not_executed: stale reference` rather
than guessing. Inputs and clicks are dispatched as trusted input events
through the shell, never by assigning element values, so React-controlled
inputs, custom widgets and dynamic pages behave as they would for a user.

Screenshots and downloads never travel through control frames: the shell
writes them into the task's temporary directory that Go names in the request
and returns the path; Go turns the file into an image or file result through
the existing channels. Uploads read only files the task owns; remote tasks
stage files through the existing SFTP transfer and the task temporary
directory, so a remote path is never treated as a local one.

## Ownership, take-over and unknown writes

- A grant `{sessionID, taskID, runtimeGeneration, tabIDs, expiresAt}` is
  minted by Go when the task starts using the browser and revoked when the
  service restarts, the session changes, the connection generation changes or
  the task ends. The shell rejects `host/browser.*` calls whose grant is not
  current.
- The browser toolbar's **Take over** button immediately switches the tab to
  `human` mode and increments its epoch through trusted application IPC.
  It works even during the 750 ms window that suppresses echoed agent input;
  automatic keyboard, mouse and touch detection is best effort during that
  window. Pending and queued actions for the old epoch are cancelled and Go
  receives `browser:takeover`. **Resume** hands control back to the agent with
  another epoch change, requiring a fresh page read. Login pages, captchas and passkeys are always a user
  hand-over: while the tab is in `human` mode the agent cannot read or act on
  it.
- Every write reserves an operation `{operationID, sessionID, generation,
  tabID, epoch, documentToken, action, digest}` in the ledger before the
  shell executes it. The shell reports `executed` or `not_executed` with a
  reason; a lost reply, a crash or a service restart leaves the operation
  `unknown`. Unknown operations are shown to the user and are never replayed
  automatically; a reused `operationID` is rejected forever.
- Renderer crash of a website view cancels only that tab's actions and
  reloads the last safe URL in `human` mode. Application renderer crash
  pauses all browser actions until the UI re-attaches.

## Host calls

| Method | Purpose |
| --- | --- |
| `host/browser.grant` `revoke` | install or revoke a grant |
| `host/browser.tabs.list` `open` `close` `activate` `navigate` | tab lifecycle bound to a grant |
| `host/browser.snapshot` | structural snapshot for a tab, returns `documentToken` |
| `host/browser.act` | one reserved action; returns `{executed, reason, documentToken}` |
| `host/browser.screenshot` | capture to a task-owned file path |
| `host/browser.downloads` | list or wait for downloads of a tab |
| `host/browser.layout` | apply the panel rectangle and overlay state |

Events from the shell: `browser:tabs` (tab list changes), `browser:takeover`
(`{tabID, epoch, reason}`), `browser:download` (progress), `browser:crash`.

## Remote agents

A remote Reasonix agent reaches the local browser through the existing SSH
connection and forward manager as a restricted host RPC carrying the same
`BrowserExecutor` contract. Grants are bound to the remote connection
generation, session and task; disconnect, reconnect or session switch revokes
them. Browser grants and provider-proxy credentials are separate; no shared
token. Older remote Serve builds negotiate capabilities and simply do not
advertise the browser, keeping every existing remote feature.

The wire shape is one loopback HTTP broker per desktop. The bootstrap of a
fresh Serve injects `REASONIX_BROWSER_BROKER` / `REASONIX_BROWSER_TOKEN`
(process environment only) pointing at the reverse-forwarded broker; a reused
Serve is re-pointed through `POST /browser/broker` after the desktop rotates
the route. The broker mints one random bearer token per host connection
generation — registering a new generation replaces the host's old token — and
authenticates before dispatching to `browser.Executor` over
`/v1/browser/<method>`. Every request carries `X-Reasonix-Browser-Session`;
the broker resolves it to the one desktop tab that shows that session and
refuses anything else with `no_grant`. Screenshots and downloads the shell
writes on the desktop are staged onto the remote host through the existing
SFTP channel into a per-workspace scratch directory
(`~/.reasonix/browser-relay/<workspace>/`), so the serve's tools only ever
read paths local to them. A Serve started with a broker advertises `browser`
in the `X-Reasonix-Serve-Capabilities` header of the `/auth/token` handshake.

## Acceptance

Iframes, dynamic DOM, controlled inputs, popups, upload and download,
navigation history, temporary partitions, shared and isolated logins;
take-over before approval, after approval before dispatch, lost receipt after
execution, restart after crash, duplicate operation IDs; remote SSH drop,
generation change, stale grant, cross-session misrouting. The real tasks in
[the migration record](DESKTOP_SHELL_MIGRATION.md#acceptance-gates) close
the phase.
