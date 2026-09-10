import assert from "node:assert/strict";
import { createCipheriv, pbkdf2Sync } from "node:crypto";
import { mkdirSync, mkdtempSync, readdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { test } from "node:test";
import { BrowserControlStore, loadBrowserControlBootstrap, type BrowserSession } from "./browserControl.js";
import { BrowserControlHost } from "./browserControlHost.js";
import type { ChromeCookie } from "./chromeImport.js";

const log = { info: () => {}, warn: () => {}, error: () => {} };

function fakeSession() {
  return {
    certificateProcs: [] as (((request: unknown, callback: (result: number) => void) => void) | null)[],
    storageClears: [] as ({ storages?: string[] } | undefined)[],
    cacheClears: 0,
    cookies: { set: async (_cookie: ChromeCookie) => {} },
    setCertificateVerifyProc(proc: ((request: unknown, callback: (result: number) => void) => void) | null) {
      this.certificateProcs.push(proc);
    },
    async clearCache() {
      this.cacheClears++;
    },
    async clearStorageData(options?: { storages?: string[] }) {
      this.storageClears.push(options);
    },
  };
}

function hostFor(root: string, overrides: Partial<Parameters<typeof buildHost>[0]> = {}) {
  return buildHost({ root, ...overrides });
}

function buildHost(input: {
  root: string;
  platform?: NodeJS.Platform;
  home?: string;
  run?: (command: string, args: string[]) => Promise<string>;
  list?: (path: string) => string[];
  sessions?: ReturnType<typeof fakeSession>[];
  pushed?: boolean[];
}) {
  const bootstrap = loadBrowserControlBootstrap(input.root);
  const store = new BrowserControlStore(bootstrap.configPath, bootstrap);
  const shared = fakeSession();
  const pushed = input.pushed ?? [];
  const host = new BrowserControlHost({
    store,
    sharedSession: () => shared as unknown as BrowserSession & { cookies: { set(cookie: ChromeCookie): Promise<void> } },
    log,
    platform: input.platform ?? "darwin",
    home: input.home ?? input.root,
    env: {},
    run: input.run ?? (async () => "safe-storage-password"),
    list: input.list ?? ((path: string) => readdirSync(path)),
    onControlEnabled: (enabled) => void pushed.push(enabled),
  });
  return { host, shared, pushed, store };
}

function chromeHome(): string {
  const home = mkdtempSync(join(tmpdir(), "reasonix-chrome-home-"));
  const profile = join(home, "Library", "Application Support", "Google", "Chrome", "Default");
  mkdirSync(profile, { recursive: true });
  const key = pbkdf2Sync("safe-storage-password", "saltysalt", 1003, 16, "sha1");
  const cipher = createCipheriv("aes-128-cbc", key, Buffer.alloc(16, 0x20));
  const value = Buffer.concat([Buffer.from("v10"), cipher.update("token", "utf8"), cipher.final()]);
  const database = new DatabaseSync(join(profile, "Cookies"));
  database.exec(`CREATE TABLE cookies (
    host_key TEXT, name TEXT, encrypted_value BLOB, path TEXT,
    expires_utc INTEGER, is_secure INTEGER, is_httponly INTEGER, samesite INTEGER)`);
  database
    .prepare("INSERT INTO cookies VALUES (?, ?, ?, ?, ?, ?, ?, ?)")
    .run(".example.test", "sid", value, "/", 13_600_000_000_000_000, 1, 1, 1);
  database.close();
  return home;
}

test("the control switch persists and is pushed to the host", async () => {
  const root = mkdtempSync(join(tmpdir(), "reasonix-browser-control-"));
  const { host, pushed } = hostFor(root);
  assert.equal(host.state().controlEnabled, true);
  assert.deepEqual(pushed, []);
  await host.setControlEnabled(false);
  assert.equal(host.state().controlEnabled, false);
  assert.deepEqual(pushed, [false]);
  assert.equal(new BrowserControlStore(join(root, "browser-control.json"), loadBrowserControlBootstrap(root)).current.controlEnabled, false);
});

test("the certificate policy follows every guest session, including later ones", async () => {
  const root = mkdtempSync(join(tmpdir(), "reasonix-browser-control-"));
  const { host } = hostFor(root);
  const first = fakeSession();
  host.trackSession("persist:browser", first as unknown as BrowserSession);
  assert.deepEqual(first.certificateProcs, [null]);

  await host.setIgnoreCertificateErrors(true);
  assert.equal(typeof first.certificateProcs[1], "function");

  const second = fakeSession();
  host.trackSession("temp:tab-2", second as unknown as BrowserSession);
  assert.equal(typeof second.certificateProcs[0], "function");

  await host.setIgnoreCertificateErrors(false);
  assert.deepEqual(first.certificateProcs[2], null);
  assert.deepEqual(second.certificateProcs[1], null);
});

test("cache and data actions target the built-in browser partition", async () => {
  const root = mkdtempSync(join(tmpdir(), "reasonix-browser-control-"));
  const { host, shared } = hostFor(root);
  await host.clearCache();
  await host.clearAllData();
  assert.equal(shared.cacheClears, 2);
  assert.deepEqual(shared.storageClears, [{ storages: ["shadercache", "cachestorage", "serviceworkers"] }, undefined]);
});

test("import reports missing Chrome and missing profiles separately", async () => {
  const empty = mkdtempSync(join(tmpdir(), "reasonix-browser-control-"));
  const missing = hostFor(empty);
  assert.deepEqual(await missing.host.importChromeLogin(), { ok: false, reason: "chrome-missing" });

  const home = mkdtempSync(join(tmpdir(), "reasonix-browser-control-"));
  mkdirSync(join(home, "Library", "Application Support", "Google", "Chrome"), { recursive: true });
  const noProfile = hostFor(home);
  assert.deepEqual(await noProfile.host.importChromeLogin(), { ok: false, reason: "profile-not-found" });
});

test("import copies cookies into the built-in browser session", async () => {
  const home = chromeHome();
  const seen: ChromeCookie[] = [];
  const { host, shared } = hostFor(home);
  shared.cookies.set = async (cookie: ChromeCookie) => void seen.push(cookie);
  assert.deepEqual(await host.importChromeLogin(), { ok: true, profile: "Default", cookies: 1, skipped: 0 });
  assert.equal(seen.length, 1);
  assert.equal(seen[0].name, "sid");
  assert.equal(seen[0].value, "token");
});

test("a denied keychain prompt becomes a typed outcome", async () => {
  const home = chromeHome();
  const { host } = hostFor(home, { run: async () => Promise.reject(new Error("User interaction is not allowed.")) });
  assert.deepEqual(await host.importChromeLogin(), { ok: false, reason: "safe-storage-denied" });
});
