import { contextBridge, ipcRenderer, webUtils } from "electron";
import { DesktopEventStream, MissedEventSubscriptions } from "../shared/eventStream.js";
import {
  hostOS,
  IPC,
  type BrowserControlState,
  type BrowserDownloadView,
  type BrowserLayoutRect,
  type BrowserNavigateTarget,
  type BrowserOpenOptions,
  type BrowserTabView,
  type ChromeImportOutcome,
  type ContractInfo,
  type IpcResult,
  type ServiceState,
  type WindowTheme,
} from "../shared/ipc.js";
import type { GraphicsSettingsState } from "../main/graphics.js";

type Listener = (...args: unknown[]) => void;

function isResult(value: unknown): value is IpcResult {
  return typeof value === "object" && value !== null && typeof (value as { ok?: unknown }).ok === "boolean";
}

function unwrap(value: unknown): unknown {
  if (!isResult(value)) throw new Error("malformed reply from the desktop shell");
  if (value.ok) return value.value;
  throw new Error(value.message);
}

async function call(channel: string, ...args: unknown[]): Promise<unknown> {
  return unwrap(await ipcRenderer.invoke(channel, ...args));
}

function fire(channel: string, ...args: unknown[]): void {
  void call(channel, ...args).catch((error: unknown) => console.warn(`[reasonixDesktop] ${channel} failed`, error));
}

function readContract(): ContractInfo {
  const raw = ipcRenderer.sendSync(IPC.contract) as unknown;
  if (typeof raw !== "object" || raw === null) return { protocolVersion: 1, digest: "", commands: Object.freeze([]) };
  const record = raw as { protocolVersion?: unknown; digest?: unknown; commands?: unknown };
  const commands = Array.isArray(record.commands) ? record.commands.filter((name): name is string => typeof name === "string") : [];
  return Object.freeze({
    protocolVersion: typeof record.protocolVersion === "number" ? record.protocolVersion : 1,
    digest: typeof record.digest === "string" ? record.digest : "",
    commands: Object.freeze(commands),
  });
}

const listeners = new Map<string, Set<Listener>>();
const missedSubscriptions = new MissedEventSubscriptions();

function emit(name: string, args: unknown[]): void {
  const set = listeners.get(name);
  if (!set) { if (name !== "desktop:resync") missedSubscriptions.add(name); return; }
  for (const listener of [...set]) {
    try {
      listener(...args);
    } catch (error) {
      console.error(`[reasonixDesktop] listener for ${name} failed`, error);
    }
  }
}

const eventStream = new DesktopEventStream((frame) => emit(frame.name, frame.args), (recovery) => {
  if (recovery.reason === "generation") missedSubscriptions.clear();
  emit("desktop:resync", [recovery]);
});
// Bind before React mounts, so unsubscribed event names still advance the
// transport cursor and cannot hide missing frames from later subscribers.
ipcRenderer.on(IPC.event, (_event, frame: unknown) => eventStream.accept(frame));

function on(name: string, listener: Listener): () => void {
  let set = listeners.get(name);
  if (!set) {
    set = new Set();
    listeners.set(name, set);
  }
  set.add(listener);
  if (name === "desktop:resync" && eventStream.recovery) {
    queueMicrotask(() => {
      if (set.has(listener) && eventStream.recovery) listener(eventStream.recovery);
    });
  } else if (missedSubscriptions.consume(name)) {
    eventStream.requestRecovery("subscription");
  }
  return () => {
    set.delete(listener);
    if (set.size === 0) listeners.delete(name);
  };
}

let lastServiceState: ServiceState | null = null;
const serviceStateListeners = new Set<(state: ServiceState) => void>();
ipcRenderer.on(IPC.serviceState, (_event, state: ServiceState) => {
  lastServiceState = state;
  eventStream.observeState(state);
  for (const listener of [...serviceStateListeners]) listener(state);
});

// The stream must know its generation even when the app has no service-state
// subscriber. A newer push wins over this initial asynchronous snapshot.
void call(IPC.serviceStateGet).then((state) => {
  if (lastServiceState) return;
  lastServiceState = state as ServiceState;
  eventStream.observeState(lastServiceState);
  for (const listener of [...serviceStateListeners]) listener(lastServiceState);
}).catch(() => undefined);

// A renderer that mounts after the service became ready never saw the push;
// the first subscriber pulls the current state so nobody waits on a past event.
function onServiceState(listener: (state: ServiceState) => void): () => void {
  serviceStateListeners.add(listener);
  if (lastServiceState) listener(lastServiceState);
  else {
    void call(IPC.serviceStateGet).then((state) => {
      if (lastServiceState || !serviceStateListeners.has(listener)) return;
      lastServiceState = state as ServiceState;
      eventStream.observeState(lastServiceState);
      listener(lastServiceState);
    }).catch(() => undefined);
  }
  return () => {
    serviceStateListeners.delete(listener);
  };
}

let lastTabs: BrowserTabView[] | null = null;
const tabListeners = new Set<(tabs: BrowserTabView[]) => void>();
ipcRenderer.on(IPC.browserTabs, (_event, tabs: BrowserTabView[]) => {
  lastTabs = Array.isArray(tabs) ? tabs : [];
  for (const listener of [...tabListeners]) listener(lastTabs);
});

// Fires immediately with the current list so a panel that mounts late never
// waits for the next change.
function onTabs(listener: (tabs: BrowserTabView[]) => void): () => void {
  tabListeners.add(listener);
  if (lastTabs) listener(lastTabs);
  else {
    void call(IPC.browserList).then((tabs) => {
      if (lastTabs || !tabListeners.has(listener)) return;
      lastTabs = Array.isArray(tabs) ? (tabs as BrowserTabView[]) : [];
      listener(lastTabs);
    }).catch(() => undefined);
  }
  return () => {
    tabListeners.delete(listener);
  };
}

const downloadListeners = new Set<(download: BrowserDownloadView) => void>();
ipcRenderer.on(IPC.browserDownload, (_event, download: BrowserDownloadView) => {
  for (const listener of [...downloadListeners]) listener(download);
});

function onDownload(listener: (download: BrowserDownloadView) => void): () => void {
  downloadListeners.add(listener);
  return () => {
    downloadListeners.delete(listener);
  };
}

const browser = {
  list: () => call(IPC.browserList) as Promise<BrowserTabView[]>,
  open: (url: string, opts?: BrowserOpenOptions) => call(IPC.browserOpen, url, opts ?? {}) as Promise<BrowserTabView>,
  close: (tabId: string) => call(IPC.browserClose, tabId).then(() => undefined),
  activate: (tabId: string | null) => call(IPC.browserActivate, tabId).then(() => undefined),
  navigate: (tabId: string, target: BrowserNavigateTarget) => call(IPC.browserNavigate, tabId, target).then(() => undefined),
  setZoom: (tabId: string, factor: number) => call(IPC.browserSetZoom, tabId, factor).then(() => undefined),
  toggleDevTools: (tabId: string) => call(IPC.browserToggleDevTools, tabId).then(() => undefined),
  resume: (tabId: string) => call(IPC.browserResume, tabId).then(() => undefined),
  takeover: (tabId: string) => call(IPC.browserUserTakeover, tabId).then(() => undefined),
  setLayout: (rect: BrowserLayoutRect | null) => fire(IPC.browserSetLayout, rect),
  setOverlay: (active: boolean) => fire(IPC.browserSetOverlay, active),
  onTabs,
  onDownload,
};

contextBridge.exposeInMainWorld("reasonixDesktop", {
  kind: "electron",
  contract: readContract(),
  platform: {
    os: hostOS(process.platform),
    arch: process.arch,
    versions: { electron: process.versions.electron ?? "", chrome: process.versions.chrome ?? "", node: process.versions.node ?? "" },
  },
  invoke: (method: string, args: unknown[]) => call(IPC.invoke, method, Array.isArray(args) ? args : []),
  on,
  native: {
    openExternal: (url: string) => call(IPC.openExternal, url).then(() => undefined),
    clipboard: {
      writeText: (text: string) => call(IPC.clipboardWrite, text).then((ok) => ok === true),
      readText: () => call(IPC.clipboardRead).then((text) => (typeof text === "string" ? text : "")),
    },
    window: {
      setTheme: (theme: WindowTheme) => fire(IPC.windowSetTheme, theme),
      setBackgroundColour: (r: number, g: number, b: number, a: number) => fire(IPC.windowSetBackground, r, g, b, a),
      getBounds: () => call(IPC.windowGetBounds),
      isMaximised: () => call(IPC.windowIsMaximised).then((value) => value === true),
      minimise: () => fire(IPC.windowMinimise),
      toggleMaximise: () => fire(IPC.windowToggleMaximise),
      close: () => fire(IPC.windowClose),
      getAppZoom: () => call(IPC.appZoomGet),
      setAppZoom: (factor: number) => call(IPC.appZoomSet, factor),
      resetAppZoom: () => call(IPC.appZoomReset),
    },
    graphics: {
      get: () => call(IPC.graphicsGet) as Promise<GraphicsSettingsState>,
      setHardwareAcceleration: (enabled: boolean) => call(IPC.graphicsSet, enabled) as Promise<GraphicsSettingsState>,
    },
    browserControl: {
      get: () => call(IPC.browserControlGet) as Promise<BrowserControlState | null>,
      setEnabled: (enabled: boolean) => call(IPC.browserControlSetEnabled, enabled) as Promise<BrowserControlState>,
      setIgnoreCertificateErrors: (enabled: boolean) =>
        call(IPC.browserControlSetIgnoreCertificateErrors, enabled) as Promise<BrowserControlState>,
      clearCache: () => call(IPC.browserControlClearCache).then(() => undefined),
      clearAllData: () => call(IPC.browserControlClearAll).then(() => undefined),
      importChromeLogin: () => call(IPC.browserControlImportChrome) as Promise<ChromeImportOutcome>,
    },
    getPathForFile: (file: File) => webUtils.getPathForFile(file),
    onServiceState,
  },
  browser,
});
