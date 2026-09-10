import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { BrowserControlSettingsPage } from "../components/BrowserControlSettingsPage";
import { LocaleProvider } from "../lib/i18n";
import { installDesktopHostStub, type DesktopHostStubOptions } from "./desktopHostStub";

function ok(value: unknown, message: string) {
  if (!value) throw new Error(message);
}

function flush(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

async function waitFor(label: string, predicate: () => boolean) {
  for (let attempt = 0; attempt < 40; attempt += 1) {
    await act(async () => {
      await flush();
    });
    if (predicate()) return;
  }
  throw new Error(`timed out waiting for ${label}: ${document.body?.textContent?.slice(0, 400) ?? ""}`);
}

function installDom() {
  const dom = new JSDOM('<!doctype html><html><body><div id="root"></div></body></html>', {
    pretendToBeVisual: true,
    url: "http://localhost/",
  });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
  globalThis.Node = dom.window.Node;
  globalThis.HTMLElement = dom.window.HTMLElement;
  globalThis.HTMLButtonElement = dom.window.HTMLButtonElement;
  globalThis.HTMLInputElement = dom.window.HTMLInputElement;
  globalThis.Event = dom.window.Event;
  globalThis.MouseEvent = dom.window.MouseEvent;
  globalThis.localStorage = dom.window.localStorage;
  globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
  globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
  return dom;
}

async function renderPage(options: DesktopHostStubOptions = {}) {
  const stub = installDesktopHostStub({}, options);
  const rootEl = document.getElementById("root");
  if (!rootEl) throw new Error("missing root");
  const root = createRoot(rootEl);
  await act(async () => {
    root.render(React.createElement(LocaleProvider, null, React.createElement(BrowserControlSettingsPage)));
    await flush();
  });
  return { stub, root, rootEl };
}

function switchFor(rootEl: HTMLElement, label: string): HTMLInputElement {
  const found = Array.from(rootEl.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')).find(
    (input) => input.getAttribute("aria-label") === label,
  );
  ok(found, `missing switch for ${label}`);
  return found as HTMLInputElement;
}

function buttonFor(rootEl: HTMLElement, text: string): HTMLButtonElement {
  const found = Array.from(rootEl.querySelectorAll("button")).find((button) => (button.textContent || "").includes(text));
  ok(found, `missing button ${text}: ${rootEl.textContent ?? ""}`);
  return found as HTMLButtonElement;
}

async function click(target: HTMLElement) {
  await act(async () => {
    target.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await flush();
  });
}

console.log("browser control settings page");

{
  const calls: string[] = [];
  installDom();
  // English labels keep the button-text assertions stable.
  window.localStorage.setItem("reasonix-lang", "en");
  const { root, rootEl } = await renderPage({ browserControlCalls: calls });
  await waitFor("controls", () => rootEl.querySelectorAll('input[type="checkbox"]').length === 2);

  ok((rootEl.textContent || "").includes("Enable built-in browser control"), "control row must render");
  ok((rootEl.textContent || "").includes("Clear all browser data"), "data rows must render");

  const control = switchFor(rootEl, "Enable built-in browser control");
  ok(control.checked, "control starts enabled");
  await click(control);
  await waitFor("control toggle", () => calls.includes("setEnabled:false"));
  ok((rootEl.textContent || "").includes("Built-in browser control disabled"), "toggle must confirm with a notice");
  ok(!switchFor(rootEl, "Enable built-in browser control").checked, "switch must reflect the new state");

  const certificates = switchFor(rootEl, "Ignore certificate errors");
  ok(!certificates.checked, "certificate verification starts strict");
  await click(certificates);
  await waitFor("certificate toggle", () => calls.includes("setIgnoreCertificateErrors:true"));

  await click(buttonFor(rootEl, "Clear cache"));
  await waitFor("cache clear", () => calls.includes("clearCache"));
  ok((rootEl.textContent || "").includes("Built-in browser cache cleared"), "cache clear must confirm");

  await click(buttonFor(rootEl, "Import browser data"));
  await waitFor("import", () => calls.includes("importChromeLogin"));
  ok((rootEl.textContent || "").includes("Imported 12 cookies from Default"), "import must report what it copied");

  // Clearing everything is destructive, so the first click only arms the confirm.
  await click(buttonFor(rootEl, "Clear all"));
  ok(!calls.includes("clearAllData"), "first click must not clear anything");
  await click(buttonFor(rootEl, "Confirm clear"));
  await waitFor("clear all", () => calls.includes("clearAllData"));
  ok((rootEl.textContent || "").includes("Built-in browser data cleared"), "clear all must confirm");

  await act(async () => {
    root.unmount();
  });
  ok(calls.includes("setEnabled:false"), `recorded calls: ${calls.join(",")}`);
}

{
  installDom();
  window.localStorage.setItem("reasonix-lang", "en");
  const { root, rootEl } = await renderPage({ chromeImportOutcome: { ok: false, reason: "safe-storage-denied" } });
  await waitFor("import button", () => rootEl.querySelectorAll("button").length > 0);
  await click(buttonFor(rootEl, "Import browser data"));
  await waitFor("import error", () => (rootEl.textContent || "").includes("Chrome Safe Storage"));
  ok(rootEl.querySelector('[role="alert"]'), "a denied keychain prompt must surface as an alert");
  await act(async () => {
    root.unmount();
  });
}

{
  installDom();
  window.localStorage.setItem("reasonix-lang", "en");
  const rootEl = document.getElementById("root");
  if (!rootEl) throw new Error("missing root");
  const root = createRoot(rootEl);
  await act(async () => {
    root.render(React.createElement(LocaleProvider, null, React.createElement(BrowserControlSettingsPage)));
    await flush();
  });
  // No Electron preload is installed here, which is the browser/Serve shell.
  await waitFor("desktop-only notice", () => (rootEl.textContent || "").includes("desktop app"));
  ok(rootEl.querySelectorAll('input[type="checkbox"]').length === 0, "no browser control must render without the shell");
  await act(async () => {
    root.unmount();
  });
}
