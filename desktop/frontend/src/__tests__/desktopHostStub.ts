// installDesktopHostStub installs a fake Electron preload host on
// window.reasonixDesktop so tests exercise the real desktopHost() path instead
// of the browser mock. Commands is a plain method table: the stub routes
// host.invoke through it, and mutating the table between calls is observed
// immediately (mirroring how the retired window.go seam behaved).
import type { AppBindings } from "../lib/bridge";
import type { DesktopBrowserHost } from "../lib/browserHost";
import type { BrowserControlApi, BrowserControlState, ChromeImportOutcome, ReasonixDesktopHost } from "../lib/desktopHost";

export interface DesktopHostStubOptions {
  /** Maps a dropped File to its native path, mirroring the preload. */
  getPathForFile?: (file: File) => string;
  /** Records native clipboard writes; clipboardWriteResult gates success. */
  clipboardWrites?: string[];
  clipboardWriteResult?: () => boolean;
  /** Value returned by native clipboard reads. */
  clipboardReadText?: string;
  /** Records native openExternal calls. */
  externalOpens?: string[];
  /** State the browser-control page starts from. */
  browserControl?: BrowserControlState;
  /** Records browser-control calls in order, e.g. "setEnabled:false". */
  browserControlCalls?: string[];
  /** Outcome of the Chrome sign-in-state import. */
  chromeImportOutcome?: ChromeImportOutcome;
}

function browserControlStub(options: DesktopHostStubOptions): BrowserControlApi {
  let state: BrowserControlState = options.browserControl ?? {
    controlEnabled: true,
    ignoreCertificateErrors: false,
    writable: true,
    warning: null,
  };
  const record = (call: string) => options.browserControlCalls?.push(call);
  return {
    get: () => Promise.resolve(state),
    setEnabled: (enabled) => {
      record(`setEnabled:${enabled}`);
      state = { ...state, controlEnabled: enabled };
      return Promise.resolve(state);
    },
    setIgnoreCertificateErrors: (enabled) => {
      record(`setIgnoreCertificateErrors:${enabled}`);
      state = { ...state, ignoreCertificateErrors: enabled };
      return Promise.resolve(state);
    },
    clearCache: () => {
      record("clearCache");
      return Promise.resolve();
    },
    clearAllData: () => {
      record("clearAllData");
      return Promise.resolve();
    },
    importChromeLogin: () => {
      record("importChromeLogin");
      return Promise.resolve(options.chromeImportOutcome ?? { ok: true, profile: "Default", cookies: 12, skipped: 0 });
    },
  };
}

export interface DesktopHostStub {
  /** The live command table; mutate it to change behavior mid-test. */
  readonly commands: Record<string, unknown>;
  /** Registered event handlers by name; emit() fans a payload out to them. */
  events: Map<string, Set<(...data: unknown[]) => void>>;
  emit(name: string, ...data: unknown[]): void;
  /** Swaps the whole command table (mirrors re-injecting the bindings). */
  replaceCommands(next: object): void;
  uninstall(): void;
}

export function installDesktopHostStub(commands: object, options: DesktopHostStubOptions = {}): DesktopHostStub {
  const ref = { current: commands as Record<string, unknown> };
  const events = new Map<string, Set<(...data: unknown[]) => void>>();
  const host: ReasonixDesktopHost = {
    kind: "electron",
    contract: {
      protocolVersion: 1,
      digest: "sha256:test",
      // Live view: tests mutating the command table between calls must be seen.
      get commands() {
        return Object.keys(ref.current);
      },
    },
    platform: { os: "darwin", arch: "arm64", versions: {} },
    invoke: (method, args) => {
      const fn = ref.current[method];
      if (typeof fn !== "function") return Promise.reject(new Error(`unstubbed desktop command ${method}`));
      return Promise.resolve((fn as (...a: unknown[]) => unknown)(...args));
    },
    on: (name, cb) => {
      let set = events.get(name);
      if (!set) {
        set = new Set();
        events.set(name, set);
      }
      set.add(cb);
      return () => set.delete(cb);
    },
    native: {
      openExternal: (url) => {
        options.externalOpens?.push(url);
        return Promise.resolve();
      },
      clipboard: {
        writeText: (text) => {
          if (options.clipboardWriteResult && !options.clipboardWriteResult()) return Promise.resolve(false);
          options.clipboardWrites?.push(text);
          return Promise.resolve(true);
        },
        readText: () => Promise.resolve(options.clipboardReadText ?? ""),
      },
      window: {
        setTheme: () => {},
        setBackgroundColour: () => {},
        getBounds: () => Promise.resolve({ x: 0, y: 0, width: 1280, height: 800, maximised: false }),
        isMaximised: () => Promise.resolve(false),
        minimise: () => {},
        toggleMaximise: () => {},
      close: () => {},
      // The Electron shell owns zoom natively; tests drive it through the
      // same command table the bridge path uses, so tables without zoom
      // commands keep the neutral default.
      getAppZoom: async () => {
        const fn = ref.current.GetDesktopZoomFactor as (() => Promise<number>) | undefined;
        return typeof fn === "function" ? await fn() : 1;
      },
      setAppZoom: async (factor: number) => {
        const fn = ref.current.SetDesktopZoomFactor as ((factor: number) => Promise<number>) | undefined;
        if (typeof fn === "function") await fn(factor);
        return factor;
      },
      resetAppZoom: async () => 1,
      },
      graphics: {
        get: () => Promise.resolve({ hardwareAcceleration: true, startupEnabled: true, override: "none" as const, restartRequired: false, writable: true, warning: null }),
        setHardwareAcceleration: async (enabled: boolean) => ({ hardwareAcceleration: enabled, startupEnabled: true, override: "none" as const, restartRequired: enabled !== true, writable: true, warning: null }),
      },
      getPathForFile: options.getPathForFile ?? (() => ""),
      browserControl: browserControlStub(options),
      onServiceState: () => () => {},
    },
    browser: undefined as unknown as DesktopBrowserHost,
  };
  const previous = window.reasonixDesktop;
  window.reasonixDesktop = host;
  return {
    get commands() {
      return ref.current;
    },
    events,
    emit(name, ...data) {
      for (const cb of [...(events.get(name) ?? [])]) cb(...data);
    },
    replaceCommands(next) {
      ref.current = next as Record<string, unknown>;
    },
    uninstall() {
      window.reasonixDesktop = previous;
    },
  };
}

// AppStub casts a partial method table for installDesktopHostStub; keep the
// cast local to the tests.
export type AppStubTable = Partial<AppBindings> & Record<string, unknown>;

// dispatchNativeFileDrop drives the Electron drop path (document-level
// dragover/drop listeners) the way Chromium does: a DOM event whose
// dataTransfer carries File objects the preload then maps to native paths.
export function dispatchNativeFileDrop(target: Element, files: File[]): void {
  // Chromium-style items: webkitGetAsEntry returns a file entry, so the
  // composer treats the drop as native (paths via the preload) instead of a
  // pathless browser file drop.
  const items = files.map((file) => ({
    kind: "file",
    type: file.type,
    getAsFile: () => file,
    webkitGetAsEntry: () => ({ isFile: true, isDirectory: false, name: file.name, fullPath: "/" + file.name }),
  }));
  const EventCtor = target.ownerDocument?.defaultView?.Event ?? Event;
  for (const type of ["dragover", "drop"]) {
    const event = new EventCtor(type, { bubbles: true, cancelable: true });
    Object.defineProperty(event, "dataTransfer", {
      value: { files, items, types: ["Files"], dropEffect: "none" },
    });
    target.dispatchEvent(event);
  }
}
