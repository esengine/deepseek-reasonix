// desktopHost is the only module allowed to touch the shell global: Electron's
// window.reasonixDesktop (preload). scripts/check-desktop-host-boundary.mjs
// enforces that boundary.
import type { AppBindings } from "./bridge";
import type { DesktopBrowserHost } from "./browserHost";

export type DesktopHostKind = "electron" | "none";
export type WindowTheme = "system" | "light" | "dark";

export interface WindowBounds {
  x: number;
  y: number;
  width: number;
  height: number;
  maximised: boolean;
}

export interface ServiceState {
  phase: "starting" | "ready" | "restarting" | "failed" | "exited";
  generation: string;
  error?: string;
}
export interface GraphicsSettingsState {
  hardwareAcceleration: boolean; startupEnabled: boolean;
  override: "none" | "environment" | "command-line"; restartRequired: boolean;
  writable: boolean; warning: "invalid-config" | "unreadable-config" | "unsupported-version" | null;
}

export interface BrowserControlState {
  controlEnabled: boolean;
  ignoreCertificateErrors: boolean;
  writable: boolean;
  warning: "invalid-config" | "unreadable-config" | "unsupported-version" | null;
}

export type ChromeImportFailure =
  | "chrome-missing"
  | "profile-not-found"
  | "cookies-unreadable"
  | "safe-storage-denied"
  | "safe-storage-unavailable"
  | "unsupported-platform";

export type ChromeImportOutcome =
  | { ok: true; profile: string; cookies: number; skipped: number }
  | { ok: false; reason: ChromeImportFailure };

export interface BrowserControlApi {
  get(): Promise<BrowserControlState | null>;
  setEnabled(enabled: boolean): Promise<BrowserControlState>;
  setIgnoreCertificateErrors(enabled: boolean): Promise<BrowserControlState>;
  clearCache(): Promise<void>;
  clearAllData(): Promise<void>;
  importChromeLogin(): Promise<ChromeImportOutcome>;
}

// Mirrors docs/DESKTOP_HOST_PROTOCOL.md "Renderer preload API".
export interface ReasonixDesktopHost {
  readonly kind: "electron";
  readonly contract: { protocolVersion: number; digest: string; commands: readonly string[] };
  readonly platform: { os: "darwin" | "windows" | "linux"; arch: string; versions: Record<string, string> };
  invoke(method: string, args: unknown[]): Promise<unknown>;
  on(name: string, cb: (...args: unknown[]) => void): () => void;
  native: {
    openExternal(url: string): Promise<void>;
    clipboard: { writeText(text: string): Promise<boolean>; readText(): Promise<string> };
    window: {
      setTheme(theme: WindowTheme): void;
      setBackgroundColour(r: number, g: number, b: number, a: number): void;
      getBounds(): Promise<WindowBounds>;
      isMaximised(): Promise<boolean>;
      minimise(): void;
      toggleMaximise(): void;
      close(): void;
      getAppZoom(): Promise<number>;
      setAppZoom(factor: number): Promise<number>;
      resetAppZoom(): Promise<number>;
    };
    graphics: { get(): Promise<GraphicsSettingsState>; setHardwareAcceleration(enabled: boolean): Promise<GraphicsSettingsState> };
    browserControl: BrowserControlApi;
    getPathForFile(file: File): string;
    onServiceState(cb: (state: ServiceState) => void): () => void;
  };
  browser: DesktopBrowserHost;
}

declare global {
  interface Window {
    reasonixDesktop?: ReasonixDesktopHost;
  }
}

export interface DesktopHost {
  kind: DesktopHostKind;
  app: AppBindings | undefined;
  events: { on(name: string, cb: (...args: unknown[]) => void): () => void };
  native: {
    openExternal(url: string): void;
    clipboardWriteText(text: string): Promise<boolean>;
    clipboardReadText(): Promise<string>;
    setWindowTheme(theme: WindowTheme): void;
    setWindowBackground(r: number, g: number, b: number, a: number): void;
    getWindowBounds(): Promise<WindowBounds> | undefined;
    getAppZoom(): Promise<number>;
    setAppZoom(factor: number): Promise<number>;
    resetAppZoom(): Promise<number>;
    graphics: { get(): Promise<GraphicsSettingsState>; setHardwareAcceleration(enabled: boolean): Promise<GraphicsSettingsState> };
    browserControl: BrowserControlApi;
    onFilesDropped(cb: (paths: string[]) => void): () => void;
    getPathForFile?(file: File): string;
    onServiceState(cb: (state: ServiceState) => void): () => void;
  };
  /** Native website views; only the Electron shell provides them. */
  browser?: DesktopBrowserHost;
}

function dataTransferLooksLikeFileDrag(dt: DataTransfer | null): boolean {
  if (!dt) return false;
  if (dt.files?.length > 0) return true;
  return Array.from(dt.types ?? []).includes("Files");
}

const noop = () => {};
const win = () => (typeof window === "undefined" ? undefined : window);

// The bare browser has no shell, so the browser-control page degrades to its
// "desktop only" notice instead of pretending the actions exist.
const unavailableBrowserControl: BrowserControlApi = {
  get: async () => null,
  setEnabled: async () => { throw new Error("browser control settings unavailable"); },
  setIgnoreCertificateErrors: async () => { throw new Error("browser control settings unavailable"); },
  clearCache: async () => { throw new Error("browser control settings unavailable"); },
  clearAllData: async () => { throw new Error("browser control settings unavailable"); },
  importChromeLogin: async () => ({ ok: false, reason: "unsupported-platform" }),
};

// The bare browser (Serve product, dev server, tests) has no shell: every
// native call degrades to a no-op and there are no bound commands.
const serverHost: DesktopHost = {
  kind: "none",
  app: undefined,
  events: { on: () => noop },
  native: {
    openExternal: (url) => {
      win()?.open(url, "_blank", "noopener");
    },
    clipboardWriteText: async () => false,
    clipboardReadText: async () => "",
    setWindowTheme: noop,
    setWindowBackground: noop,
    getWindowBounds: () => undefined,
    onFilesDropped: () => noop,
    onServiceState: () => noop,
    getAppZoom: async () => 1,
    setAppZoom: async () => 1,
    resetAppZoom: async () => 1,
    graphics: { get: async () => ({ hardwareAcceleration: true, startupEnabled: true, override: "none", restartRequired: false, writable: false, warning: null }), setHardwareAcceleration: async () => { throw new Error("graphics settings unavailable"); } },
    browserControl: unavailableBrowserControl,
  },
};

let electronHost: DesktopHost | undefined;
let electronHostFor: ReasonixDesktopHost | undefined;
const dropListeners = new Set<(paths: string[]) => void>();

const insideDropTarget = (target: EventTarget | null) =>
  typeof (target as Element | null)?.closest === "function" &&
  (target as Element).closest("[data-native-drop-target]") !== null;

// Chromium hands the renderer real File objects; paths come from the preload.
// The handlers install per document (a reloaded renderer gets a fresh one) and
// resolve the host at dispatch time so a service restart never goes stale.
let dropHandlersInstalledOn: Document | undefined;
const installElectronDropHandlers = () => {
  const doc = win()?.document;
  if (!doc || dropHandlersInstalledOn === doc) return;
  dropHandlersInstalledOn = doc;
  doc.addEventListener("dragover", (e) => {
    if (!dataTransferLooksLikeFileDrag(e.dataTransfer)) return;
    e.preventDefault();
    if (!insideDropTarget(e.target) && e.dataTransfer) e.dataTransfer.dropEffect = "none";
  });
  doc.addEventListener("drop", (e) => {
    const host = win()?.reasonixDesktop;
    if (!host || !dataTransferLooksLikeFileDrag(e.dataTransfer)) return;
    e.preventDefault();
    if (!insideDropTarget(e.target) || !e.dataTransfer) return;
    const paths = Array.from(e.dataTransfer.files).map((file) => host.native.getPathForFile(file)).filter((path) => path !== "");
    if (paths.length > 0) for (const cb of [...dropListeners]) cb(paths);
  });
};

const electronHostFrom = (host: ReasonixDesktopHost): DesktopHost => {
  if (electronHost && electronHostFor === host) return electronHost;
  electronHostFor = host;
  electronHost = {
    kind: "electron",
    // contract.commands is read live so a replaced preload (service restart)
    // never leaves the proxy pointing at a stale command set.
    app: new Proxy({} as AppBindings, {
      get: (_target, prop) =>
        typeof prop === "string" && host.contract.commands.includes(prop) ? (...args: unknown[]) => host.invoke(prop, args) : undefined,
    }),
    events: { on: (name, cb) => host.on(name, cb) },
    native: {
      openExternal: (url) => void host.native.openExternal(url).catch((err: unknown) => console.warn("openExternal failed", err)),
      clipboardWriteText: (text) => host.native.clipboard.writeText(text),
      clipboardReadText: () => host.native.clipboard.readText(),
      setWindowTheme: (theme) => host.native.window.setTheme(theme),
      setWindowBackground: (r, g, b, a) => host.native.window.setBackgroundColour(r, g, b, a),
      getWindowBounds: () => host.native.window.getBounds(),
      getAppZoom: () => host.native.window.getAppZoom(),
      setAppZoom: (factor) => host.native.window.setAppZoom(factor),
    resetAppZoom: () => host.native.window.resetAppZoom(),
      graphics: host.native.graphics,
      browserControl: host.native.browserControl,
      onFilesDropped: (cb) => {
        installElectronDropHandlers();
        dropListeners.add(cb);
        return () => {
          dropListeners.delete(cb);
        };
      },
      getPathForFile: (file) => host.native.getPathForFile(file),
      onServiceState: (cb) => host.native.onServiceState(cb),
    },
    browser: host.browser,
  };
  return electronHost;
};

// Resolved at call time, never cached by callers: the preload may install
// window.reasonixDesktop after this module first evaluates, and the browser
// dev mock must only win when no shell is present.
export function desktopHost(): DesktopHost {
  if (typeof window === "undefined") return serverHost;
  const electron = window.reasonixDesktop;
  if (electron) return electronHostFrom(electron);
  return serverHost;
}
