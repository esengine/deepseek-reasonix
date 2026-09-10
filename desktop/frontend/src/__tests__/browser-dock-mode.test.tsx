// Run: tsx src/__tests__/browser-dock-mode.test.tsx
import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";

import { WorkspaceDockRegion, type WorkspaceDockRegionProps } from "../app-shell/WorkspaceDockRegion";
import type { DesktopBrowserHost } from "../lib/browserHost";
import type { ReasonixDesktopHost } from "../lib/desktopHost";
import { LocaleProvider, type Translator } from "../lib/i18n";
import { availableDockEntries } from "../lib/dockEntries";
import { useLayoutStore, type RightDockMode } from "../store/layout";
import { useActivityBarStore } from "../store/activityBar";

const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost", pretendToBeVisual: true });
class TestResizeObserver { observe() {} unobserve() {} disconnect() {} }
Object.assign(globalThis, { window: dom.window, document: dom.window.document, localStorage: dom.window.localStorage,
  IS_REACT_ACT_ENVIRONMENT: true, ResizeObserver: TestResizeObserver });
(dom.window as unknown as { ResizeObserver: unknown }).ResizeObserver = TestResizeObserver;

const noop = () => {};
const browser: DesktopBrowserHost = {
  list: async () => [], open: async () => { throw new Error("unused"); }, close: async () => {}, activate: async () => {},
  navigate: async () => {}, setZoom: async () => {}, toggleDevTools: async () => {}, resume: async () => {}, takeover: async () => {},
  setLayout: noop, setOverlay: noop, onTabs: () => noop, onDownload: () => noop,
};
const electron: ReasonixDesktopHost = {
  kind: "electron",
  contract: { protocolVersion: 1, digest: "test", commands: [] },
  platform: { os: "linux", arch: "x64", versions: {} },
  invoke: async () => undefined,
  on: () => noop,
  native: {
    openExternal: async () => {},
    clipboard: { writeText: async () => true, readText: async () => "" },
    window: { setTheme: noop, setBackgroundColour: noop, getBounds: async () => ({ x: 0, y: 0, width: 0, height: 0, maximised: false }),
      isMaximised: async () => false, minimise: noop, toggleMaximise: noop, close: noop },
    getPathForFile: () => "",
    onServiceState: () => noop,
  },
  browser,
};

const modes: RightDockMode[] = [];
const picks: string[] = [];
const props = (mode: RightDockMode): WorkspaceDockRegionProps => ({
  visible: true, overlay: false, mode, creation: false, showContext: true,
  t: ((key: string) => key) as Translator,
  onPickEntry: (entryId: string) => { picks.push(entryId); },
  remote: {} as WorkspaceDockRegionProps["remote"], context: {} as WorkspaceDockRegionProps["context"],
  trajectory: {} as WorkspaceDockRegionProps["trajectory"],
  workspace: { tabId: "A" } as WorkspaceDockRegionProps["workspace"], workspaceKey: "k",
});
const root = createRoot(document.getElementById("root")!);
const paint = (mode: RightDockMode) => act(async () => root.render(<LocaleProvider><WorkspaceDockRegion {...props(mode)} /></LocaleProvider>));
const tabLabels = () => [...document.querySelectorAll(".workbench-dock__tab-label")].map((el) => el.textContent);
const settle = () => act(async () => { await new Promise((resolve) => setTimeout(resolve, 20)); });

console.log("\nbrowser dock mode");
try {
  await paint("files");
  assert.deepEqual(tabLabels(), [], "an empty tab list renders no dock tabs");
  assert.equal(availableDockEntries(false).some((entry) => entry.defaultTab === "browser"), false,
    "no shell host: the browser entry is not offered");
  assert.deepEqual(modes, [], "an empty dock issues no mode command");

  // With no tab open the dock offers the tab picker instead of a blank panel.
  const picker = document.querySelector(".tab-picker");
  assert.ok(picker, "an empty dock renders the tab picker");
  const pickerEntries = [...picker!.querySelectorAll<HTMLButtonElement>(".tab-picker__item")];
  assert.deepEqual(pickerEntries.map((button) => button.textContent), ["Overview", "Files", "Changes", "Trajectory"],
    "the picker lists every openable view, browser excluded without a shell host");
  await act(async () => pickerEntries[0].click());
  assert.deepEqual(picks, ["context"], "picking an entry opens that view through the shared command");

  window.reasonixDesktop = electron;
  assert.equal(availableDockEntries(true).some((entry) => entry.defaultTab === "browser"), true,
    "the Electron host offers the browser entry");

  await act(async () => { useActivityBarStore.getState().addTab("browser", "Browser"); });
  await paint("browser");
  await settle();
  assert.deepEqual(tabLabels(), ["Browser"], "the browser tab renders in the dock");
  assert.ok(document.querySelector(".browser-panel"), "the browser tab mounts the lazy panel in the dock body");
  assert.ok(document.querySelector(".workbench-dock--browser"), "the dock carries the mode modifier");

  useLayoutStore.getState().setRightDockMode("browser");
  assert.equal(useLayoutStore.getState().rightDockMode, "browser", "the layout store accepts the browser dock mode");

  const browserTabId = useActivityBarStore.getState().tabs[0].id;
  await act(async () => { useActivityBarStore.getState().closeTab(browserTabId); });
  await paint("files");
  await settle();
  assert.equal(document.querySelector(".browser-panel"), null, "closing the browser tab unmounts the panel");

  delete window.reasonixDesktop;
  await act(async () => root.unmount());
  console.log("browser dock mode: gating, tab render and lazy mount passed");
} finally {
  dom.window.close();
}
