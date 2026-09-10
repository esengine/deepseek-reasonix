import assert from "node:assert/strict";
import { existsSync, mkdtempSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import {
  applyBrowserSessionPolicy,
  clearBrowserCache,
  clearBrowserData,
  loadBrowserControlBootstrap,
  BrowserControlStore,
  type BrowserSession,
} from "./browserControl.js";

const home = () => mkdtempSync(join(tmpdir(), "reasonix-browser-control-"));

type FakeSession = BrowserSession & {
  certificateProcs: (((request: unknown, callback: (result: number) => void) => void) | null)[];
  cleared: { cache: number; storage: ({ storages?: string[] } | undefined)[] };
};

function fakeSession(): FakeSession {
  return {
    certificateProcs: [],
    cleared: { cache: 0, storage: [] },
    setCertificateVerifyProc(proc) {
      this.certificateProcs.push(proc);
    },
    async clearCache() {
      this.cleared.cache++;
    },
    async clearStorageData(options) {
      this.cleared.storage.push(options);
    },
  };
}

test("defaults keep browser control on and certificate verification strict", () => {
  const bootstrap = loadBrowserControlBootstrap(home());
  assert.equal(bootstrap.state.controlEnabled, true);
  assert.equal(bootstrap.state.ignoreCertificateErrors, false);
  assert.equal(bootstrap.state.writable, true);
  assert.equal(existsSync(bootstrap.configPath), false);
});

test("persists both switches and rereads them", async () => {
  const root = home();
  const bootstrap = loadBrowserControlBootstrap(root);
  const store = new BrowserControlStore(bootstrap.configPath, bootstrap);
  await store.setControlEnabled(false);
  await store.setIgnoreCertificateErrors(true);
  assert.deepEqual(JSON.parse(readFileSync(bootstrap.configPath, "utf8")), {
    version: 1,
    controlEnabled: false,
    ignoreCertificateErrors: true,
  });
  const reread = new BrowserControlStore(bootstrap.configPath, loadBrowserControlBootstrap(root));
  assert.equal(reread.current.controlEnabled, false);
  assert.equal(reread.current.ignoreCertificateErrors, true);
});

test("rejects non-boolean patches and keeps the file untouched", async () => {
  const root = home();
  const bootstrap = loadBrowserControlBootstrap(root);
  const store = new BrowserControlStore(bootstrap.configPath, bootstrap);
  await assert.rejects(() => store.setControlEnabled("yes" as unknown as boolean));
  assert.equal(existsSync(bootstrap.configPath), false);
});

test("an invalid file is backed up and replaced by a valid one", async () => {
  const root = home();
  const path = join(root, "browser-control.json");
  writeFileSync(path, JSON.stringify({ version: 1, controlEnabled: "on", ignoreCertificateErrors: false }));
  const bootstrap = loadBrowserControlBootstrap(root);
  assert.equal(bootstrap.state.warning, "invalid-config");
  const store = new BrowserControlStore(path, bootstrap);
  await store.setControlEnabled(false);
  assert.ok(readdirSync(root).some((name) => name.startsWith("browser-control.json.invalid")));
  assert.equal(JSON.parse(readFileSync(path, "utf8")).controlEnabled, false);
  assert.equal(store.current.warning, null);
});

test("a newer file version is read-only", async () => {
  const root = home();
  const path = join(root, "browser-control.json");
  writeFileSync(path, JSON.stringify({ version: 9, controlEnabled: true, ignoreCertificateErrors: false }));
  const bootstrap = loadBrowserControlBootstrap(root);
  assert.equal(bootstrap.state.warning, "unsupported-version");
  assert.equal(bootstrap.state.writable, false);
  await assert.rejects(() => new BrowserControlStore(path, bootstrap).setControlEnabled(false));
});

test("the certificate policy is scoped to the browser session it is applied to", () => {
  const relaxed = fakeSession();
  applyBrowserSessionPolicy(relaxed, true);
  const proc = relaxed.certificateProcs[0];
  assert.equal(typeof proc, "function");
  let result = -1;
  proc?.({}, (value) => {
    result = value;
  });
  assert.equal(result, 0);

  const strict = fakeSession();
  applyBrowserSessionPolicy(strict, false);
  assert.deepEqual(strict.certificateProcs, [null]);
});

test("cache clearing keeps sign-in state while clearing everything drops it", async () => {
  const cacheOnly = fakeSession();
  await clearBrowserCache(cacheOnly);
  assert.equal(cacheOnly.cleared.cache, 1);
  assert.deepEqual(cacheOnly.cleared.storage, [{ storages: ["shadercache", "cachestorage", "serviceworkers"] }]);

  const everything = fakeSession();
  await clearBrowserData(everything);
  assert.equal(everything.cleared.cache, 1);
  assert.deepEqual(everything.cleared.storage, [undefined]);
});
