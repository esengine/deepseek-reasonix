import { existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import type { BrowserControlState, BrowserControlWarning } from "../shared/ipc.js";

export interface BrowserControlBootstrap {
  state: BrowserControlState;
  configPath: string;
}

export interface BrowserControlPatch {
  controlEnabled?: boolean;
  ignoreCertificateErrors?: boolean;
}

type Stored = { version: 1; controlEnabled: boolean; ignoreCertificateErrors: boolean };

// Cache-only clearing keeps cookies and local storage, so a signed-in site stays
// signed in; "all" additionally drops every storage type Electron knows.
export const BROWSER_CACHE_STORAGES: readonly string[] = ["shadercache", "cachestorage", "serviceworkers"];

// The subset of Electron's Session the browser policy needs. Structural so the
// unit tests can drive it with a fake instead of a live shell.
export interface BrowserSession {
  setCertificateVerifyProc(proc: ((request: unknown, callback: (result: number) => void) => void) | null): void;
  clearCache(): Promise<void>;
  clearStorageData(options?: { storages?: string[] }): Promise<void>;
}

function parseStored(path: string): { value: Stored | null; warning: BrowserControlWarning | null } {
  if (!existsSync(path)) return { value: null, warning: null };
  try {
    const parsed = JSON.parse(readFileSync(path, "utf8")) as Record<string, unknown>;
    if (parsed.version !== 1) return { value: null, warning: "unsupported-version" };
    if (typeof parsed.controlEnabled !== "boolean" || typeof parsed.ignoreCertificateErrors !== "boolean") {
      return { value: null, warning: "invalid-config" };
    }
    return { value: parsed as Stored, warning: null };
  } catch {
    return { value: null, warning: "unreadable-config" };
  }
}

function defaultStored(): Stored {
  // Control defaults on: the desktop browser is already the agent's capability,
  // so hiding it behind an opt-in would change existing behaviour.
  return { version: 1, controlEnabled: true, ignoreCertificateErrors: false };
}

export function loadBrowserControlBootstrap(dataHome: string): BrowserControlBootstrap {
  const configPath = join(dataHome, "browser-control.json");
  const parsed = parseStored(configPath);
  const stored = parsed.value ?? defaultStored();
  return {
    configPath,
    state: {
      controlEnabled: stored.controlEnabled,
      ignoreCertificateErrors: stored.ignoreCertificateErrors,
      writable: parsed.warning !== "unsupported-version",
      warning: parsed.warning,
    },
  };
}

export class BrowserControlStore {
  private stored: Stored;
  private state: BrowserControlState;
  private writeQueue: Promise<void> = Promise.resolve();

  constructor(private readonly path: string, bootstrap: BrowserControlBootstrap) {
    this.stored = parseStored(path).value ?? defaultStored();
    this.state = bootstrap.state;
  }

  get current(): BrowserControlState {
    return this.state;
  }

  setControlEnabled(enabled: boolean): Promise<BrowserControlState> {
    return this.patch({ controlEnabled: enabled });
  }

  setIgnoreCertificateErrors(enabled: boolean): Promise<BrowserControlState> {
    return this.patch({ ignoreCertificateErrors: enabled });
  }

  private async patch(patch: BrowserControlPatch): Promise<BrowserControlState> {
    for (const [key, value] of Object.entries(patch)) {
      if (typeof value !== "boolean") throw new Error(`${key} must be boolean`);
    }
    if (!this.state.writable) throw new Error("browser control settings are read-only");
    const next: Stored = { ...this.stored, ...patch, version: 1 };
    const write = this.writeQueue.catch(() => undefined).then(() => {
      mkdirSync(dirname(this.path), { recursive: true });
      const existing = parseStored(this.path);
      if (existing.warning) {
        let backup = `${this.path}.invalid`;
        let suffix = 1;
        while (existsSync(backup)) backup = `${this.path}.invalid.${suffix++}`;
        renameSync(this.path, backup);
      }
      const temp = `${this.path}.tmp`;
      writeFileSync(temp, `${JSON.stringify(next)}\n`, "utf8");
      renameSync(temp, this.path);
    });
    this.writeQueue = write.catch(() => undefined);
    await write;
    this.stored = next;
    this.state = { ...this.state, ...patch, warning: null };
    return this.state;
  }
}

// Certificate policy belongs to the browser partition alone; the app window and
// every other Electron session keep Chromium's default verification.
export function applyBrowserSessionPolicy(target: BrowserSession, ignoreCertificateErrors: boolean): void {
  if (!ignoreCertificateErrors) {
    target.setCertificateVerifyProc(null);
    return;
  }
  target.setCertificateVerifyProc((_request, callback) => callback(0));
}

export async function clearBrowserCache(target: BrowserSession): Promise<void> {
  await target.clearCache();
  await target.clearStorageData({ storages: [...BROWSER_CACHE_STORAGES] });
}

export async function clearBrowserData(target: BrowserSession): Promise<void> {
  await target.clearCache();
  await target.clearStorageData();
}
