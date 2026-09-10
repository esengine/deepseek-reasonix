import { existsSync } from "node:fs";
import {
  applyBrowserSessionPolicy,
  clearBrowserCache,
  clearBrowserData,
  type BrowserControlStore,
  type BrowserSession,
} from "./browserControl.js";
import {
  ChromeImportError,
  defaultRunCommand,
  findChromeProfiles,
  importChromeCookies,
  chromeProfileRoot,
  type ChromeImportDeps,
  type CookieSink,
} from "./chromeImport.js";
import type { Logger } from "./log.js";
import type { ChromeImportOutcome, BrowserControlState } from "../shared/ipc.js";

export interface BrowserControlHostDeps {
  store: BrowserControlStore;
  sharedSession(): BrowserSession & { cookies: CookieSink };
  log: Logger;
  platform: NodeJS.Platform;
  home: string;
  env: NodeJS.ProcessEnv;
  run?(command: string, args: string[]): Promise<string>;
  list(path: string): string[];
  onControlEnabled(enabled: boolean): void;
}

// The renderer-facing surface: settings state plus the one-shot actions that
// only make sense against the browser partition.
export interface BrowserControlApi {
  state(): BrowserControlState;
  setControlEnabled(enabled: boolean): Promise<BrowserControlState>;
  setIgnoreCertificateErrors(enabled: boolean): Promise<BrowserControlState>;
  clearCache(): Promise<void>;
  clearAllData(): Promise<void>;
  importChromeLogin(): Promise<ChromeImportOutcome>;
}

export class BrowserControlHost implements BrowserControlApi {
  private readonly sessions = new Map<string, BrowserSession>();

  constructor(private readonly deps: BrowserControlHostDeps) {}

  state(): BrowserControlState {
    return this.deps.store.current;
  }

  // Every guest partition is the built-in browser, so the certificate policy
  // follows each one as it appears; the app window never goes through here.
  trackSession(partition: string, session: BrowserSession): void {
    this.sessions.set(partition, session);
    applyBrowserSessionPolicy(session, this.deps.store.current.ignoreCertificateErrors);
  }

  async setControlEnabled(enabled: boolean): Promise<BrowserControlState> {
    const state = await this.deps.store.setControlEnabled(enabled);
    this.deps.log.info(`browser control enabled=${state.controlEnabled}`);
    this.deps.onControlEnabled(state.controlEnabled);
    return state;
  }

  async setIgnoreCertificateErrors(enabled: boolean): Promise<BrowserControlState> {
    const state = await this.deps.store.setIgnoreCertificateErrors(enabled);
    for (const session of this.sessions.values()) applyBrowserSessionPolicy(session, state.ignoreCertificateErrors);
    this.deps.log.info(`browser certificate verification relaxed=${state.ignoreCertificateErrors}`);
    return state;
  }

  async clearCache(): Promise<void> {
    await clearBrowserCache(this.deps.sharedSession());
    this.deps.log.info("browser cache cleared");
  }

  async clearAllData(): Promise<void> {
    await clearBrowserData(this.deps.sharedSession());
    this.deps.log.info("browser data cleared");
  }

  async importChromeLogin(): Promise<ChromeImportOutcome> {
    const root = chromeProfileRoot(this.deps.platform, this.deps.home, this.deps.env);
    if (!existsSync(root)) {
      this.deps.log.warn(`chrome import: no Chrome data at ${root}`);
      return { ok: false, reason: "chrome-missing" };
    }
    const profiles = findChromeProfiles(root, this.deps.list);
    if (profiles.length === 0) {
      this.deps.log.warn(`chrome import: no profile with cookies under ${root}`);
      return { ok: false, reason: "profile-not-found" };
    }
    const deps: ChromeImportDeps = {
      platform: this.deps.platform,
      home: this.deps.home,
      env: this.deps.env,
      cookies: this.deps.sharedSession().cookies,
      run: this.deps.run ?? defaultRunCommand,
      log: this.deps.log,
    };
    try {
      const summary = await importChromeCookies(deps, root, profiles);
      return { ok: true, ...summary };
    } catch (error) {
      const reason = error instanceof ChromeImportError ? error.code : "cookies-unreadable";
      this.deps.log.warn(`chrome import failed (${reason}): ${error instanceof Error ? error.message : String(error)}`);
      return { ok: false, reason };
    }
  }
}
