import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import React, { act } from "react";
import { JSDOM } from "jsdom";

const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost", pretendToBeVisual: true });
Object.assign(globalThis, {
  window: dom.window, document: dom.window.document, localStorage: dom.window.localStorage,
  HTMLElement: dom.window.HTMLElement, Element: dom.window.Element, Node: dom.window.Node,
  CustomEvent: dom.window.CustomEvent, IS_REACT_ACT_ENVIRONMENT: true,
  requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window),
  cancelAnimationFrame: dom.window.cancelAnimationFrame.bind(dom.window),
});
const css = readFileSync(new URL("../styles.css", import.meta.url), "utf8");
const style = document.createElement("style");
style.textContent = [...css.matchAll(/[^{}]*\.project-tree__search[^{}]*\{[^{}]*\}/g)].map(match => match[0]).join("\n");
document.head.append(style);
const { ProjectTree } = await import("../components/ProjectTree");
const { LocaleProvider } = await import("../lib/i18n");
const { createRoot } = await import("react-dom/client");
const root = createRoot(document.getElementById("root")!);
const render = async (variant: "creation" | "workbench") => {
  await act(async () => root.render(
    <LocaleProvider><aside className={`sidebar--${variant}`}><ProjectTree variant={variant}
      timeFilter="all" onTimeFilterChange={() => {}} onOpenTopic={() => {}} onAddProject={async () => {}} />
    </aside></LocaleProvider>,
  ));
};
const input = () => document.querySelector<HTMLInputElement>(".project-tree__search input")!;
const setQuery = async (value: string) => {
  await act(async () => {
    Object.getOwnPropertyDescriptor(dom.window.HTMLInputElement.prototype, "value")!.set!.call(input(), value);
    input().dispatchEvent(new dom.window.Event("input", { bubbles: true }));
  });
};
const visible = () => dom.window.getComputedStyle(input().parentElement!).display !== "none";
try {
  await render("creation");
  await setQuery("needle");
  await render("workbench");
  assert.equal(input().value, "needle", "switching layouts preserves the current search");
  assert.ok(visible(), "an active search remains visible and editable in workbench");
  await setQuery("other");
  assert.equal(input().value, "other");
  assert.ok(visible(), "editing the active query keeps the input visible");
  await setQuery("");
  assert.equal(visible(), false, "clearing the query restores the compact workbench layout");
  await render("creation");
  assert.equal(input().value, "", "the cleared query stays cleared after switching back");
  assert.ok(visible(), "creation search remains available with no query");
  await setQuery("   ");
  await render("workbench");
  assert.equal(visible(), false, "whitespace alone does not activate filtering");
  console.log("PASS project tree search survives layout switches and can be cleared");
} finally {
  await act(async () => root.unmount());
  dom.window.close();
}
