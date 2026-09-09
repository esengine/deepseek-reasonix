// Run: tsx src/__tests__/mermaid-rendering.test.tsx

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import ReactMarkdown from "react-markdown";
import { splitStableMarkdownSections, splitStreamingTailFence, streamingCommitTarget, streamingMarkdownCommitInterval, useRenderedMarkdownText } from "../components/Markdown";
import MermaidDiagram from "../components/MermaidDiagram";
import {
  __setMermaidPanZoomFactoryForTest,
  __setMermaidRenderAdapterForTest,
  isOpenableMermaidHref,
  isSafeMermaidHref,
  safelyRunPanZoom,
  safelySyncPanZoom,
  sanitizeMermaidSvg,
} from "../components/MermaidDiagram";
import { createMermaidPanZoom } from "../components/mermaidPanZoom";
import { LocaleProvider } from "../lib/i18n";
import { REMOTE_MARKDOWN_IMAGE_PATH } from "../lib/markdownImage";

const testDir = dirname(fileURLToPath(import.meta.url));
const styles = readFileSync(resolve(testDir, "../styles.css"), "utf8");
const markdownRendererSource = readFileSync(resolve(testDir, "../components/MarkdownRenderer.tsx"), "utf8");
const markdownComponentsSource = readFileSync(resolve(testDir, "../components/markdownComponents.tsx"), "utf8");
const markdownSource = readFileSync(resolve(testDir, "../components/Markdown.tsx"), "utf8");
const mermaidDiagramSource = readFileSync(resolve(testDir, "../components/MermaidDiagram.tsx"), "utf8");
const messageSource = readFileSync(resolve(testDir, "../components/Message.tsx"), "utf8");

let passed = 0;
let failed = 0;

function ok(value: unknown, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

function eq(actual: unknown, expected: unknown, label: string) {
  if (actual === expected) ok(true, label);
  else ok(false, `${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
}

function flushTimers(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

async function waitFor(label: string, predicate: () => boolean) {
  for (let i = 0; i < 30; i += 1) {
    if (predicate()) return;
    await act(async () => {
      await flushTimers();
    });
  }
  ok(false, label);
}

function installDom() {
  const dom = new JSDOM("<!doctype html><html><head></head><body><div id=\"root\"></div></body></html>", {
    pretendToBeVisual: true,
    url: "http://localhost/",
  });

  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
  globalThis.Node = dom.window.Node;
  globalThis.Element = dom.window.Element;
  globalThis.HTMLElement = dom.window.HTMLElement;
  globalThis.SVGElement = dom.window.SVGElement;
  globalThis.Event = dom.window.Event;
  globalThis.KeyboardEvent = dom.window.KeyboardEvent;
  globalThis.MouseEvent = dom.window.MouseEvent;
  globalThis.DOMParser = dom.window.DOMParser;
  globalThis.XMLSerializer = dom.window.XMLSerializer;
  globalThis.MutationObserver = dom.window.MutationObserver;
  globalThis.localStorage = dom.window.localStorage;
  globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
  globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
  Object.defineProperty(dom.window.HTMLElement.prototype, "getBoundingClientRect", {
    configurable: true,
    value: () => ({ x: 0, y: 0, width: 640, height: 360, top: 0, right: 640, bottom: 360, left: 0, toJSON: () => ({}) }),
  });
  Object.defineProperty(dom.window, "matchMedia", {
    configurable: true,
    value: (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
      addListener: () => undefined,
      removeListener: () => undefined,
      dispatchEvent: () => false,
    }),
  });
  globalThis.matchMedia = dom.window.matchMedia;

  const style = document.createElement("style");
  style.textContent = styles;
  document.head.appendChild(style);

  return dom;
}

{
  const dom = installDom();
  const container = document.createElement("div");
  const throwing = {
    destroy: () => {},
    resize: () => { throw new DOMException("matrix is not invertible", "InvalidStateError"); },
    fit: () => { throw new DOMException("matrix is not invertible", "InvalidStateError"); },
    center: () => {},
    zoomIn: () => { throw new DOMException("matrix is not invertible", "InvalidStateError"); },
    zoomOut: () => {},
    reset: () => {},
  };
  eq(safelySyncPanZoom(throwing, container), false, "matrix failures are contained during pan/zoom layout sync");
  eq(safelyRunPanZoom(throwing, () => throwing.zoomIn()), false, "matrix failures are contained during toolbar actions");
  Object.defineProperty(container, "getBoundingClientRect", {
    value: () => ({ x: 0, y: 0, width: 0, height: 0, top: 0, right: 0, bottom: 0, left: 0, toJSON: () => ({}) }),
  });
  eq(safelySyncPanZoom(throwing, container), false, "zero-sized layouts are deferred before SVG matrix work");
  dom.window.close();
}

// Inline pan/zoom (replaces svg-pan-zoom, #8068): transform math on one
// viewport <g>, zoom clamped to 0.3–8, wheel/drag/dblclick interactions.
{
  const dom = installDom();
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 160 80");
  const defs = document.createElementNS("http://www.w3.org/2000/svg", "defs");
  const marker = document.createElementNS("http://www.w3.org/2000/svg", "marker");
  marker.setAttribute("id", "arrow");
  defs.appendChild(marker);
  svg.appendChild(defs);
  const content = document.createElementNS("http://www.w3.org/2000/svg", "g");
  content.setAttribute("id", "drawing");
  svg.appendChild(content);
  // 640x360 layout box: the meet mapping fits at scale 4, origin (0, 20).
  Object.defineProperty(svg, "getBoundingClientRect", {
    configurable: true,
    value: () => ({ x: 0, y: 0, width: 640, height: 360, top: 0, right: 640, bottom: 360, left: 0, toJSON: () => ({}) }),
  });
  document.body.appendChild(svg);

  const panZoom = createMermaidPanZoom(svg, { minZoom: 0.3, maxZoom: 8, zoomScaleSensitivity: 0.3 });
  const viewport = () => svg.querySelector("g[data-mermaid-pan-zoom-viewport]");
  ok(svg.querySelector("g[data-mermaid-pan-zoom-viewport] g#drawing"), "inline pan/zoom wraps the drawing in one viewport group");
  ok(defs.parentNode === svg, "SVG definitions stay outside the transformed viewport");
  ok(!viewport()?.querySelector("defs"), "the viewport does not transform SVG definitions");
  ok(svg.querySelector("defs marker#arrow") === marker, "definition children remain available by ID");

  panZoom.fit();
  eq(viewport()?.getAttribute("transform"), "translate(0 0) scale(1)", "fit keeps the browser meet mapping at unit scale");

  svg.dispatchEvent(new dom.window.WheelEvent("wheel", { clientX: 320, clientY: 180, deltaY: -120, bubbles: true, cancelable: true }));
  eq(viewport()?.getAttribute("transform"), "translate(-24 -12) scale(1.3)", "wheel zooms in around the cursor");

  for (let i = 0; i < 40; i += 1) {
    svg.dispatchEvent(new dom.window.WheelEvent("wheel", { clientX: 320, clientY: 180, deltaY: 120, bubbles: true, cancelable: true }));
  }
  ok(viewport()?.getAttribute("transform")?.endsWith("scale(0.3)"), "wheel zoom-out clamps at the minimum zoom");

  panZoom.reset();
  eq(viewport()?.getAttribute("transform"), "translate(0 0) scale(1)", "reset restores the fitted unit transform");
  for (let i = 0; i < 40; i += 1) {
    svg.dispatchEvent(new dom.window.WheelEvent("wheel", { clientX: 320, clientY: 180, deltaY: -120, bubbles: true, cancelable: true }));
  }
  ok(viewport()?.getAttribute("transform")?.endsWith("scale(8)"), "wheel zoom-in clamps at the maximum zoom");

  panZoom.reset();
  svg.dispatchEvent(new dom.window.MouseEvent("pointerdown", { button: 0, clientX: 100, clientY: 100, bubbles: true }));
  svg.dispatchEvent(new dom.window.MouseEvent("pointermove", { clientX: 130, clientY: 110, bubbles: true }));
  eq(viewport()?.getAttribute("transform"), "translate(7.5 2.5) scale(1)", "pointer drag pans the drawing in screen pixels");
  svg.dispatchEvent(new dom.window.MouseEvent("pointerup", { clientX: 130, clientY: 110, bubbles: true }));

  panZoom.destroy();
  svg.dispatchEvent(new dom.window.WheelEvent("wheel", { clientX: 320, clientY: 180, deltaY: -120, bubbles: true, cancelable: true }));
  eq(viewport()?.getAttribute("transform"), "translate(7.5 2.5) scale(1)", "destroy detaches the interaction listeners");

  ok(!mermaidDiagramSource.includes("svg-pan-zoom"), "MermaidDiagram no longer imports svg-pan-zoom");
  ok(!styles.includes("svg-pan-zoom"), "styles no longer carry svg-pan-zoom hooks");
  dom.window.close();
}

function parseSvg(svg: string): Document {
  return new DOMParser().parseFromString(svg, "image/svg+xml");
}

console.log("\nmermaid rendering");

{
  ok(markdownSource.includes("requestAnimationFrame"), "streaming markdown commits on an animation frame");
  ok(markdownSource.includes("streamingMarkdownCommitInterval"), "streaming markdown applies an adaptive parse budget");
  ok(markdownSource.includes('className="md md--stream-tail"'), "streaming markdown exposes an immediate lightweight tail");
  ok(markdownSource.includes("requestIdleCallback"), "large Markdown finalization waits for browser idle time");
  ok(markdownSource.includes("reasonix:markdown-finalize"), "large Markdown finalization emits a performance measure");
  ok(markdownSource.includes("splitStableMarkdownSections"), "large Markdown retains completed top-level sections");
  ok(markdownRendererSource.includes("bare = false"), "stable Markdown sections share one semantic container");
  ok(
    styles.includes(".md > :where(") && styles.includes("contain-intrinsic-size: auto 72px"),
    "Markdown blocks skip offscreen layout while preserving learned intrinsic sizes",
  );
  ok(markdownSource.includes("streaming?: boolean"), "Markdown exposes an explicit streaming state");
  ok(messageSource.includes("streaming={item.streaming}"), "assistant messages pass streaming state to Markdown");
  ok(
    markdownComponentsSource.includes('lazy(() => import("./MermaidDiagram"))'),
    "the shared components map lazy-loads the Mermaid renderer",
  );
  ok(
    markdownComponentsSource.includes('lang === "mermaid"'),
    "the shared components map routes mermaid fenced code blocks to the Mermaid renderer",
  );
}

{
  const section = (index: number) => `# Section ${index}\n\n${`paragraph-${index} `.repeat(700)}\n\n`;
  const document = Array.from({ length: 8 }, (_, index) => section(index)).join("");
  const chunks = splitStableMarkdownSections(document);
  ok(chunks.length >= 4, "large headed Markdown is divided into bounded stable chunks");
  eq(chunks.join(""), document, "stable Markdown chunking preserves every source byte");

  const appended = splitStableMarkdownSections(document + section(8));
  eq(appended.slice(0, chunks.length).join(""), document, "appending a section leaves all completed chunks unchanged");

  const fenced = `${section(0)}\`\`\`text\n# not a heading\n${"fenced content\n".repeat(900)}\`\`\`\n\n${section(1)}`;
  const fencedChunks = splitStableMarkdownSections(fenced);
  eq(fencedChunks.join(""), fenced, "fenced Markdown chunking preserves source bytes");
  ok(!fencedChunks.some((chunk) => chunk.startsWith("# not a heading")), "headings inside fenced code never become section boundaries");

  const referenced = `${document}\n[shared]: https://example.com\n\nUse [shared].\n`;
  eq(splitStableMarkdownSections(referenced).length, 1, "cross-section references use one semantic Markdown renderer");

  const nestedFence = `1. item\n\n   ${"long paragraph ".repeat(1_000)}\n\n   \`\`\`text\n   code\n   \`\`\`\n\n2. next\n`;
  const nestedChunks = splitStableMarkdownSections(nestedFence);
  eq(nestedChunks.length, 1, "list containers stay in one semantic Markdown renderer");
  const renderMarkdown = (source: string) => renderToStaticMarkup(<ReactMarkdown>{source}</ReactMarkdown>);
  eq(
    nestedChunks.map(renderMarkdown).join(""),
    renderMarkdown(nestedFence),
    "stable Markdown optimization preserves nested list and fence DOM semantics",
  );
}

{
  eq(streamingMarkdownCommitInterval(1_000), 50, "short streaming Markdown uses the 50ms parse budget");
  eq(streamingMarkdownCommitInterval(8_000), 150, "medium streaming Markdown uses the 150ms parse budget");
  eq(streamingMarkdownCommitInterval(32_000), 300, "long streaming Markdown uses the 300ms parse budget");

  eq(streamingCommitTarget("intro\n\npartial paragraph"), "intro\n\n", "commit target stops at the last completed block");
  eq(streamingCommitTarget("no blank line yet"), "", "no completed block means nothing to parse yet");
  eq(streamingCommitTarget("done\n\nalso done\n\n"), "done\n\nalso done\n\n", "trailing boundary commits everything");
  eq(streamingCommitTarget("t\n\n```js\nstreaming code"), "t\n\n", "an open fence stays in the tail for code-styled streaming");
  eq(streamingCommitTarget("t\n\n$$\n\\int_0^1"), "t\n\n$$\n\\int_0^1", "open display math keeps the whole text parsed");
  eq(streamingCommitTarget("t\n\n```\nc\n```\nafter"), "t\n\n```\nc\n```\n", "a closed fence promotes immediately without waiting for a blank line");
  eq(streamingCommitTarget("t\n\n$$\nx\n$$\nafter"), "t\n\n$$\nx\n$$\n", "closed display math promotes immediately");
  eq(streamingCommitTarget("para\n## Next sec"), "para\n", "a partial heading line completes the paragraph before it");
  eq(streamingCommitTarget("para\n## Done\ntail"), "para\n## Done\n", "a terminated heading promotes itself as a complete block");

  // GFM table rows commit per terminated line.
  eq(streamingCommitTarget("| a | b |\n|---|---|\n| 1 | 2 |\n"), "| a | b |\n|---|---|\n| 1 | 2 |\n", "completed table rows commit immediately");
  eq(streamingCommitTarget("| a | b |\n|---|---|\n| 1 | 2 |"), "| a | b |\n|---|---|\n", "incomplete last row stays in tail");
  eq(streamingCommitTarget("| a | b |\n|---|---|\n| 1 | 2 |\n\nnext"), "| a | b |\n|---|---|\n| 1 | 2 |\n\n", "blank line closes table");
  eq(streamingCommitTarget("text\n\n| h1 | h2 |\n|---|---|\n| r1 | r1 |\n| r2 | r2 |\nmore"), "text\n\n| h1 | h2 |\n|---|---|\n| r1 | r1 |\n| r2 | r2 |\n", "table rows commit, non-pipe tail stays");

  const searchItem = (title: string, url: string) => `- **${title}**\n  <${url}>`;
  const searchDump = [
    searchItem("新闻本文", "https://example.com/a"),
    searchItem("Bitcoin (BTC) Price", "https://example.com/b?utm_source=x"),
    searchItem("KuCoin", "https://example.com/c"),
  ].join("\n");
  eq(
    streamingCommitTarget(searchDump),
    `${searchItem("新闻本文", "https://example.com/a")}\n${searchItem("Bitcoin (BTC) Price", "https://example.com/b?utm_source=x")}\n`,
    "a later list marker commits prior tight list items, including indented URL continuations",
  );
  eq(
    streamingCommitTarget(`${searchItem("新闻本文", "https://example.com/a")}\n- **Bit`),
    `${searchItem("新闻本文", "https://example.com/a")}\n`,
    "an in-progress list marker still commits the previous completed item",
  );
  eq(
    streamingCommitTarget(searchItem("新闻本文", "https://example.com/a")),
    "",
    "a single list item stays in the tail until the next item or a blank line",
  );
  eq(
    streamingCommitTarget("1. alpha\n2. beta\n3. gamma"),
    "1. alpha\n2. beta\n",
    "ordered list markers complete prior items the same way",
  );
  eq(
    streamingCommitTarget("- [ ] one\n- [x] two\n- [ ] three"),
    "- [ ] one\n- [x] two\n",
    "task-list markers complete prior items the same way",
  );
  eq(
    streamingCommitTarget("intro paragraph\n- first item\n  continued"),
    "intro paragraph\n",
    "a list marker completes the paragraph before it without taking the new item",
  );
  eq(
    streamingCommitTarget("- parent\n  - child still typing"),
    "- parent\n",
    "a nested list marker completes the parent item and leaves the child in the tail",
  );
  eq(
    streamingCommitTarget("not a heading\n---\nstill setext"),
    "",
    "a setext underline is not treated as a list marker",
  );
  eq(
    streamingCommitTarget("*not-a-list*\nstill paragraph"),
    "",
    "emphasis without a marker space is not a list item",
  );
  eq(
    streamingCommitTarget("t\n\n```\n- not a list\n- also not\n"),
    "t\n\n",
    "list markers inside an open fence do not create commit boundaries",
  );
  eq(
    streamingCommitTarget("- one\n- two\n\n"),
    "- one\n- two\n\n",
    "a blank line after a list still commits the whole list",
  );
  const searchHtml = renderToStaticMarkup(<ReactMarkdown remarkPlugins={[]}>{searchDump}</ReactMarkdown>);
  ok(searchHtml.includes("<ul>") && searchHtml.includes("<li>") && searchHtml.includes("新闻本文"),
    "the search-list source still renders as a list in the final Markdown tree");
}

{
  const dom = installDom();
  const rootEl = document.getElementById("root");
  if (!rootEl) throw new Error("missing root");
  let nextFrameID = 1;
  let pendingFrame: FrameRequestCallback | undefined;
  globalThis.requestAnimationFrame = ((callback: FrameRequestCallback) => {
    pendingFrame = callback;
    return nextFrameID++;
  }) as typeof requestAnimationFrame;
  globalThis.cancelAnimationFrame = (() => {
    pendingFrame = undefined;
  }) as typeof cancelAnimationFrame;

  const root = createRoot(rootEl);
  function MarkdownTextProbe({ text, streaming }: { text: string; streaming: boolean }) {
    return <div>{useRenderedMarkdownText(text, streaming)}</div>;
  }
  await act(async () => {
    root.render(<MarkdownTextProbe text="start" streaming />);
    await flushTimers();
  });
  eq(rootEl.textContent, "start", "streaming Markdown starts from the current text");

  await act(async () => {
    root.render(<MarkdownTextProbe text="start middle" streaming />);
    await flushTimers();
    root.render(<MarkdownTextProbe text="start middle end" streaming />);
    await new Promise((resolve) => setTimeout(resolve, 60));
  });
  eq(pendingFrame, undefined, "an in-progress block never schedules a parse commit");
  eq(rootEl.textContent, "start", "the growing block rides the tail, not the parsed prefix");

  await act(async () => {
    root.render(<MarkdownTextProbe text={"start middle end\n\nnext block"} streaming />);
    await new Promise((resolve) => setTimeout(resolve, 60));
  });
  const frame = pendingFrame;
  await act(async () => {
    frame?.(performance.now());
    await flushTimers();
  });
  eq(rootEl.textContent, "start middle end\n\n", "a completed block commits up to its boundary");

  pendingFrame = undefined;
  await act(async () => {
    root.render(<MarkdownTextProbe text={"start middle end\n\nnext block grows"} streaming />);
    await flushTimers();
  });
  eq(pendingFrame, undefined, "tail growth alone schedules no further parse");

  await act(async () => {
    root.render(<MarkdownTextProbe text={"...\nreplacement window"} streaming />);
    await flushTimers();
  });
  eq(rootEl.textContent, "", "a rolling Markdown window drops its stale parsed prefix before paint");

  await act(async () => {
    root.render(<MarkdownTextProbe text="complete" streaming={false} />);
  });
  eq(rootEl.textContent, "complete", "short stream finalization still commits immediately");

  pendingFrame = undefined;
  const firstSearch = "- **新闻本文**\n  <https://example.com/a>\n";
  const secondSearch = `${firstSearch}- **Bitcoin**\n  <https://example.com/b>`;
  await act(async () => {
    root.render(<MarkdownTextProbe text={firstSearch} streaming />);
    await flushTimers();
  });
  eq(rootEl.textContent, "", "a single tight list item stays off the parsed prefix");
  await act(async () => {
    root.render(<MarkdownTextProbe text={secondSearch} streaming />);
    await new Promise((resolve) => setTimeout(resolve, 60));
  });
  const listFrame = pendingFrame;
  await act(async () => {
    listFrame?.(performance.now());
    await flushTimers();
  });
  eq(rootEl.textContent, firstSearch, "a later list item commits the previous tight item into the parsed prefix");

  await act(async () => root.unmount());
  dom.window.close();
}

{
  const dom = installDom();
  const rootEl = document.getElementById("root");
  if (!rootEl) throw new Error("missing root");
  let pendingIdle: (() => void) | undefined;
  Object.defineProperty(dom.window, "requestIdleCallback", {
    configurable: true,
    value: (callback: () => void) => {
      pendingIdle = callback;
      return 1;
    },
  });
  Object.defineProperty(dom.window, "cancelIdleCallback", {
    configurable: true,
    value: () => {
      pendingIdle = undefined;
    },
  });
  Object.defineProperty(dom.window, "setTimeout", {
    configurable: true,
    value: (callback: TimerHandler) => {
      if (typeof callback === "function") callback();
      return 1;
    },
  });
  Object.defineProperty(dom.window, "clearTimeout", {
    configurable: true,
    value: () => undefined,
  });

  const root = createRoot(rootEl);
  const streamed = "a".repeat(8_100);
  const finalText = `${streamed} final`;
  function MarkdownTextProbe({ text, streaming }: { text: string; streaming: boolean }) {
    return <div>{useRenderedMarkdownText(text, streaming)}</div>;
  }
  await act(async () => {
    root.render(<MarkdownTextProbe text={streamed} streaming />);
    await flushTimers();
  });
  await act(async () => {
    root.render(<MarkdownTextProbe text={finalText} streaming={false} />);
    await flushTimers();
  });
  eq(rootEl.textContent, streamed, "large Markdown keeps its committed content while finalization waits for idle");
  ok(Boolean(pendingIdle), "large Markdown schedules one idle finalization callback");

  await act(async () => {
    pendingIdle?.();
    await flushTimers();
  });
  eq(rootEl.textContent, finalText, "idle finalization commits the complete large Markdown text");

  await act(async () => root.unmount());
  dom.window.close();
}

{
  const dom = installDom();
  Object.defineProperty(dom.window, "runtime", { configurable: true, value: {} });
  const dirtySvg = `
    <svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" onload="steal()">
      <script>alert(1)</script>
      <a id="safe" href="https://example.com/diagram"><text>safe</text></a>
      <a id="unsafe" href="javascript:alert(1)"><text>bad</text></a>
      <a id="unsafe-xlink" xlink:href="data:text/html,boom"><text>bad</text></a>
      <image id="remote-image" href="https://images.example.com/diagram.png" />
      <use id="external-use" href="https://images.example.com/icons.svg#node" />
      <g onclick="steal()"><text>node</text></g>
    </svg>`;
  const sanitized = sanitizeMermaidSvg(dirtySvg);
  const doc = parseSvg(sanitized);

  ok(!doc.documentElement.hasAttribute("onload"), "sanitizer strips event attributes from the root SVG");
  ok(!doc.querySelector("script"), "sanitizer removes script nodes");
  ok(doc.querySelector("#safe")?.getAttribute("href") === "https://example.com/diagram", "sanitizer keeps safe external links");
  ok(!doc.querySelector("#unsafe")?.hasAttribute("href"), "sanitizer removes javascript links");
  ok(!doc.querySelector("#unsafe-xlink")?.hasAttribute("xlink:href"), "sanitizer removes data xlink links");
  ok(
    doc.querySelector("#remote-image")?.getAttribute("href")?.startsWith(`${REMOTE_MARKDOWN_IMAGE_PATH}?url=`) === true,
    "sanitizer routes Mermaid image resources through the backend proxy",
  );
  ok(!doc.querySelector("#external-use")?.hasAttribute("href"), "sanitizer removes external SVG use resources");
  ok(!doc.querySelector("g")?.hasAttribute("onclick"), "sanitizer strips event attributes from child nodes");
  ok(isSafeMermaidHref("https://example.com/a"), "https Mermaid links are safe");
  ok(isSafeMermaidHref("mailto:hello@example.com"), "mailto Mermaid links are safe");
  ok(!isSafeMermaidHref("file:///tmp/private"), "file Mermaid links are not safe");
  ok(!isOpenableMermaidHref("#internal"), "fragment Mermaid links are not opened externally");

  dom.window.close();
}

{
  const dom = installDom();
  const openedUrls: string[] = [];
  dom.window.open = ((url: string | URL | undefined) => {
    if (url) openedUrls.push(String(url));
    return null;
  }) as Window["open"];

  const renders: Array<{ definition: string; theme: string }> = [];
  const panZoomCalls: string[] = [];

  __setMermaidRenderAdapterForTest(async (_svgId, definition, theme, signal) => {
    if (signal.aborted) throw new DOMException("Aborted", "AbortError");
    renders.push({ definition, theme });
    return `
      <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 160 80" onload="steal()">
        <script>alert(1)</script>
        <a id="safe-link" href="https://example.com/diagram"><text>Open</text></a>
        <a id="unsafe-link" href="vbscript:msgbox(1)"><text>Blocked</text></a>
        <g class="node"><text>Rendered Mermaid</text></g>
      </svg>`;
  });

  __setMermaidPanZoomFactoryForTest(() => ({
    destroy: () => { panZoomCalls.push("destroy"); },
    resize: () => { panZoomCalls.push("resize"); },
    fit: () => { panZoomCalls.push("fit"); },
    center: () => { panZoomCalls.push("center"); },
    zoomIn: () => { panZoomCalls.push("zoomIn"); },
    zoomOut: () => { panZoomCalls.push("zoomOut"); },
    reset: () => { panZoomCalls.push("reset"); },
  }));

  const rootEl = document.getElementById("root");
  if (!rootEl) throw new Error("missing root");
  const root = createRoot(rootEl);

  await act(async () => {
    root.render(
      <LocaleProvider>
        <div className="chat-pane">
          <MermaidDiagram definition={"graph TD\nA-->B"} />
        </div>
      </LocaleProvider>,
    );
    await flushTimers();
  });

  await waitFor("Mermaid preview SVG rendered in DOM", () => Boolean(document.querySelector(".mermaid-diagram__preview svg")));
  ok(document.querySelector(".mermaid-diagram__toolbar"), "Mermaid renderer shows its toolbar");
  eq(renders.length, 1, "Mermaid renderer calls the render adapter once");
  eq(renders[0]?.definition, "graph TD\nA-->B", "Mermaid renderer passes the diagram definition to Mermaid");
  ok(document.querySelector("#safe-link"), "safe SVG link remains in the rendered DOM");
  ok(!document.querySelector("#unsafe-link")?.hasAttribute("href"), "unsafe SVG link href is stripped in the rendered DOM");
  ok(!document.querySelector(".mermaid-diagram__preview svg")?.hasAttribute("onload"), "rendered SVG root event handler is stripped");
  ok(!document.querySelector(".mermaid-diagram__preview script"), "rendered SVG script nodes are removed");

  await waitFor("pan zoom instance initialized", () => panZoomCalls.includes("fit") && panZoomCalls.includes("center"));

  const zoomIn = document.querySelector<HTMLButtonElement>('button[aria-label="Zoom in"]');
  const zoomOut = document.querySelector<HTMLButtonElement>('button[aria-label="Zoom out"]');
  const reset = document.querySelector<HTMLButtonElement>('button[aria-label="Reset zoom"]');
  zoomIn?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  zoomOut?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  reset?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  ok(panZoomCalls.includes("zoomIn"), "zoom in button calls the pan zoom instance");
  ok(panZoomCalls.includes("zoomOut"), "zoom out button calls the pan zoom instance");
  ok(panZoomCalls.includes("reset"), "reset zoom button calls the pan zoom instance");

  document.querySelector("#safe-link text")?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  eq(openedUrls[0], "https://example.com/diagram", "SVG links open through the external browser bridge");
  document.querySelector("#unsafe-link text")?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  eq(openedUrls.length, 1, "unsafe SVG links do not open externally");

  await act(async () => {
    document.querySelector<HTMLButtonElement>('button[aria-label="Show diagram source"]')
      ?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    await flushTimers();
  });
  ok(document.querySelector(".mermaid-diagram__code")?.textContent?.includes("graph TD"), "source tab shows the Mermaid definition");

  await act(async () => {
    document.querySelector<HTMLButtonElement>('button[aria-label="Open fullscreen"]')
      ?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    await flushTimers();
  });
  ok(document.querySelector(".chat-pane > .mermaid-diagram--fullscreen"), "fullscreen diagram portals into the chat pane");

  await act(async () => {
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    await flushTimers();
  });
  ok(!document.querySelector(".chat-pane > .mermaid-diagram--fullscreen"), "Escape closes the Mermaid fullscreen portal");

  await act(async () => {
    root.unmount();
  });
  __setMermaidRenderAdapterForTest(null);
  __setMermaidPanZoomFactoryForTest(undefined);
  dom.window.close();
}

{
  eq(splitStreamingTailFence("still typing"), null, "a plain tail has no code fence split");
  eq(splitStreamingTailFence("```\nc\n```\nafter"), null, "a closed fence leaves no open code tail");
  const split = splitStreamingTailFence("```js\nconst a = 1;\nconst b");
  eq(split?.head, "", "an open fence at the tail start has no plain head");
  eq(split?.lang, "js", "the open fence split keeps the info-string language");
  eq(split?.code, "const a = 1;\nconst b", "the open fence split drops the opener line from the code body");
  eq(splitStreamingTailFence("para\n\n```\nx")?.head, "para\n\n", "text before the open fence stays plain");
}

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
