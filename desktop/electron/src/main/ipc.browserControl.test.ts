import assert from "node:assert/strict";
import type { IpcMain } from "electron";
import { test } from "node:test";
import { IPC, type BrowserControlState, type IpcResult } from "../shared/ipc.js";
import { parseContract } from "./contract.js";
import { registerRendererIpc } from "./ipc.js";
import type { BrowserControlApi } from "./browserControlHost.js";

const silent = { info() {}, warn() {}, error() {} };
const STATE: BrowserControlState = { controlEnabled: true, ignoreCertificateErrors: false, writable: true, warning: null };

function fakeIpc() {
  const handlers = new Map<string, (event: unknown, ...args: unknown[]) => Promise<IpcResult>>();
  const ipcMain = {
    handle: (channel: string, fn: (event: unknown, ...args: unknown[]) => Promise<IpcResult>) => handlers.set(channel, fn),
    on: () => undefined,
  } as unknown as IpcMain;
  return { ipcMain, handlers };
}

function register(options: { browserControl?: BrowserControlApi; withGraphics?: boolean } = {}) {
  const { ipcMain, handlers } = fakeIpc();
  const trusted = { sender: { id: 1 }, senderFrame: {} };
  registerRendererIpc({
    ipcMain,
    contract: parseContract({ digest: "sha256:a", commands: ["ListTabs"] }),
    window: {
      isTrustedSender: (sender, frame) => sender === trusted.sender && frame === trusted.senderFrame,
      minimise() {},
      toggleMaximise() {},
      isMaximised: () => false,
      close() {},
      bounds: () => ({ x: 0, y: 0, width: 0, height: 0, maximised: false }),
      setTheme() {},
      setBackgroundColour() {},
      getAppZoom: async () => 1,
      setAppZoom: async () => 1,
      resetAppZoom: async () => 1,
    },
    serviceState: () => ({ phase: "ready" as const, generation: "g-test" }),
    invoke: async () => undefined,
    clipboard: { writeText: async () => undefined, readText: async () => "" },
    openExternal: async () => undefined,
    ...(options.browserControl ? { browserControl: options.browserControl } : {}),
    log: silent,
  });
  return { handlers, trusted };
}

function apiStub(calls: string[]): BrowserControlApi {
  return {
    state: () => STATE,
    setControlEnabled: async (enabled) => {
      calls.push(`enabled:${enabled}`);
      return { ...STATE, controlEnabled: enabled };
    },
    setIgnoreCertificateErrors: async (enabled) => {
      calls.push(`certificates:${enabled}`);
      return { ...STATE, ignoreCertificateErrors: enabled };
    },
    clearCache: async () => void calls.push("clearCache"),
    clearAllData: async () => void calls.push("clearAllData"),
    importChromeLogin: async () => {
      calls.push("import");
      return { ok: true, profile: "Default", cookies: 3, skipped: 1 };
    },
  };
}

test("browser control channels are sender-gated and typed", async () => {
  const calls: string[] = [];
  const { handlers, trusted } = register({ browserControl: apiStub(calls) });
  const get = handlers.get(IPC.browserControlGet);
  const setEnabled = handlers.get(IPC.browserControlSetEnabled);
  const setCertificates = handlers.get(IPC.browserControlSetIgnoreCertificateErrors);
  const clearCache = handlers.get(IPC.browserControlClearCache);
  const clearAll = handlers.get(IPC.browserControlClearAll);
  const importChrome = handlers.get(IPC.browserControlImportChrome);
  assert.ok(get && setEnabled && setCertificates && clearCache && clearAll && importChrome);

  assert.deepEqual(await get({ sender: { id: 9 }, senderFrame: trusted.senderFrame }), { ok: false, message: "untrusted sender" });
  assert.deepEqual(await get(trusted), { ok: true, value: STATE });
  assert.deepEqual(await setEnabled(trusted, false), { ok: true, value: { ...STATE, controlEnabled: false } });
  assert.deepEqual(await setEnabled(trusted, "no"), { ok: false, message: "controlEnabled must be boolean" });
  assert.deepEqual(await setCertificates(trusted, true), { ok: true, value: { ...STATE, ignoreCertificateErrors: true } });
  assert.deepEqual(await setCertificates(trusted, 1), { ok: false, message: "ignoreCertificateErrors must be boolean" });
  assert.deepEqual(await clearCache(trusted), { ok: true, value: undefined });
  assert.deepEqual(await clearAll(trusted), { ok: true, value: undefined });
  assert.deepEqual(await importChrome(trusted), { ok: true, value: { ok: true, profile: "Default", cookies: 3, skipped: 1 } });
  assert.deepEqual(calls, ["enabled:false", "certificates:true", "clearCache", "clearAllData", "import"]);
});

test("browser control channels fail closed without a shell store", async () => {
  const { handlers, trusted } = register();
  assert.deepEqual(await handlers.get(IPC.browserControlGet)!(trusted), { ok: true, value: null });
  const message = "browser control settings unavailable";
  assert.deepEqual(await handlers.get(IPC.browserControlSetEnabled)!(trusted, true), { ok: false, message });
  assert.deepEqual(await handlers.get(IPC.browserControlClearCache)!(trusted), { ok: false, message });
  assert.deepEqual(await handlers.get(IPC.browserControlImportChrome)!(trusted), { ok: false, message });
});
