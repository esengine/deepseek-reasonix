export const IPC = {
  contract: "reasonix:contract",
  invoke: "reasonix:invoke",
  event: "reasonix:event",
  serviceState: "reasonix:service-state",
  serviceStateGet: "reasonix:service-state:get",
  openExternal: "reasonix:native:open-external",
  clipboardWrite: "reasonix:native:clipboard-write",
  clipboardRead: "reasonix:native:clipboard-read",
  windowMinimise: "reasonix:native:window-minimise",
  windowToggleMaximise: "reasonix:native:window-toggle-maximise",
  windowIsMaximised: "reasonix:native:window-is-maximised",
  windowClose: "reasonix:native:window-close",
  windowGetBounds: "reasonix:native:window-get-bounds",
  windowSetTheme: "reasonix:native:window-set-theme",
  windowSetBackground: "reasonix:native:window-set-background",
  appZoomGet: "reasonix:native:app-zoom-get",
  appZoomSet: "reasonix:native:app-zoom-set",
  appZoomReset: "reasonix:native:app-zoom-reset",
  graphicsGet: "reasonix:native:graphics-get",
  graphicsSet: "reasonix:native:graphics-set",
  browserControlGet: "reasonix:native:browser-control-get",
  browserControlSetEnabled: "reasonix:native:browser-control-set-enabled",
  browserControlSetIgnoreCertificateErrors: "reasonix:native:browser-control-set-ignore-certificate-errors",
  browserControlClearCache: "reasonix:native:browser-control-clear-cache",
  browserControlClearAll: "reasonix:native:browser-control-clear-all",
  browserControlImportChrome: "reasonix:native:browser-control-import-chrome",
  browserList: "reasonix:browser:list",
  browserOpen: "reasonix:browser:open",
  browserClose: "reasonix:browser:close",
  browserActivate: "reasonix:browser:activate",
  browserNavigate: "reasonix:browser:navigate",
  browserSetZoom: "reasonix:browser:set-zoom",
  browserToggleDevTools: "reasonix:browser:toggle-devtools",
  browserResume: "reasonix:browser:resume",
  browserUserTakeover: "reasonix:browser:user-takeover",
  browserSetLayout: "reasonix:browser:set-layout",
  browserSetOverlay: "reasonix:browser:set-overlay",
  browserTabs: "reasonix:browser:tabs",
  browserDownload: "reasonix:browser:download",
  browserTakeover: "reasonix:browser:takeover",
} as const;

export type ServicePhase = "starting" | "ready" | "restarting" | "failed" | "exited";

export interface ServiceState {
  phase: ServicePhase;
  generation: string;
  error?: string;
}

export interface ContractInfo {
  protocolVersion: number;
  digest: string;
  commands: readonly string[];
}

export interface EventFrame {
  seq: number;
  generation: string;
  name: string;
  args: unknown[];
}

export type IpcResult = { ok: true; value: unknown } | { ok: false; message: string };

export interface WindowBounds {
  x: number;
  y: number;
  width: number;
  height: number;
  maximised: boolean;
}

export type WindowTheme = "system" | "light" | "dark";

export type HostOS = "darwin" | "windows" | "linux";

export type BrowserControlWarning = "invalid-config" | "unreadable-config" | "unsupported-version";

export interface BrowserControlState {
  controlEnabled: boolean;
  ignoreCertificateErrors: boolean;
  writable: boolean;
  warning: BrowserControlWarning | null;
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

export function hostOS(platform: string): HostOS {
  if (platform === "darwin") return "darwin";
  if (platform === "win32") return "windows";
  return "linux";
}

export type BrowserTabMode = "agent" | "human";

export interface BrowserTabView {
  id: string;
  taskId: string;
  url: string;
  title: string;
  loading: boolean;
  canGoBack: boolean;
  canGoForward: boolean;
  temporary: boolean;
  mode: BrowserTabMode;
  epoch: number;
  zoom: number;
  active: boolean;
  error: { code: number; description: string } | null;
}

export type BrowserDownloadState = "progressing" | "completed" | "cancelled" | "interrupted";

export interface BrowserDownloadView {
  id: string;
  tabId: string;
  url: string;
  filename: string;
  path: string;
  state: BrowserDownloadState;
  received: number;
  total: number;
}

export interface BrowserLayoutRect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface BrowserOpenOptions {
  temporary?: boolean;
  taskId?: string;
}

export type BrowserNavigateAction = "back" | "forward" | "reload" | "stop";

export interface BrowserNavigateTarget {
  url?: string;
  action?: BrowserNavigateAction;
}

export type BrowserTakeoverKind = "mousedown" | "keydown" | "wheel" | "touchstart" | "pointerdown";
