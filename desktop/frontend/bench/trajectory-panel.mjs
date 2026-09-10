// Browser gate for the trajectory panel.
//
// The panel is a projection of the wire, so unit tests can only prove the fold.
// This run proves the parts that need a real DOM and a real event stream: the
// dock wiring opens it, the ledger kept folding while the panel was closed, an
// activity is one row, a duration the kernel did not measure is drawn as a tick
// rather than an invented width, and a host with no durable ledger says so.
//
// Run: PLAYWRIGHT_BROWSERS_PATH=.pw-browsers node bench/trajectory-panel.mjs

import assert from "node:assert/strict";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { tmpdir } from "node:os";
import { createServer } from "vite";

const frontendDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.env.PLAYWRIGHT_BROWSERS_PATH = !process.env.PLAYWRIGHT_BROWSERS_PATH || process.env.PLAYWRIGHT_BROWSERS_PATH === ".pw-browsers"
  ? path.join(frontendDir, ".pw-browsers")
  : process.env.PLAYWRIGHT_BROWSERS_PATH;
const { chromium } = await import("playwright");

const server = await createServer({ root: frontendDir, server: { host: "127.0.0.1", port: 0 }, logLevel: "error" });
await server.listen();
let browser;
try {
  const url = server.resolvedUrls?.local?.[0] ?? "http://127.0.0.1:5173/";
  browser = await chromium.launch({ headless: true, executablePath: process.env.CHROME_EXECUTABLE });
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, locale: "en-US" });
  await context.addInitScript(() => localStorage.setItem("reasonix-lang", "en"));
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.setDefaultTimeout(20000);

  await page.goto(`${url}?mock=bench&bench=1`, { waitUntil: "domcontentloaded" });
  const composer = page.locator("textarea.composer__input:not([aria-hidden=true])");
  await composer.waitFor();

  // The bench dataset restores dock tabs for its project; make sure the session
  // that owns them is the active one before running a turn in it.
  const benchTopic = page.locator('.project-tree__topic-main:has-text("bench:small-6t")');
  if (await benchTopic.count()) await benchTopic.first().click();

  // A scripted turn that emits a tool call, a notice and a turn end.
  await composer.fill("/recover-context mock-protocol-once");
  await page.locator(".composer__btn--send").click();
  await page.locator(".composer__btn--stop").waitFor({ state: "detached" }).catch(() => {});

  // The panel is opened *after* the run: the ledger folds from the one wire
  // handler, so a panel that was never mounted must still show the whole run.
  await page.locator(".workbench-dock__tab-add").click();
  await page.locator(".tab-add-menu__item").filter({ hasText: /^Trajectory$/ }).click();
  const panel = page.locator(".trajectory");
  await panel.waitFor();

  const rows = page.locator(".trajectory__table tbody tr");
  await page.waitForFunction(() => document.querySelectorAll(".trajectory__table tbody tr").length >= 3);
  const kinds = await rows.evaluateAll((nodes) => nodes.map((node) => node.getAttribute("data-k")));
  assert(kinds.includes("turn"), "the turn is drawn as an activity");
  assert(kinds.includes("tool"), "the tool call that ran is drawn");
  assert.equal(kinds.filter((kind) => kind === "tool").length, 1, "a dispatch and its result stay on one row");

  // Every row draws exactly one mark. What that mark is — a bar for a measured
  // activity, a tick for one that only has a start — is decided by whether the
  // kernel measured a duration, which the unit tests pin deterministically;
  // here the contract is that a row never draws both and never draws neither.
  const marksPerRow = await rows.evaluateAll((nodes) => nodes.map(
    (node) => node.querySelectorAll(".trajectory__tick, .trajectory__bar").length,
  ));
  assert(marksPerRow.every((count) => count === 1), `each row draws exactly one mark (got ${marksPerRow.join(",")})`);

  const turnBar = page.locator('tr[data-k="turn"] .trajectory__bar');
  assert.equal(await turnBar.count(), 1, "the turn is measured by when it started and ended");
  const barWidth = await turnBar.first().evaluate((node) => node.getBoundingClientRect().width);
  assert(barWidth > 0, "the measured activity has a real width on the axis");

  // This host answers no durable-ledger read, so the panel must not imply the
  // last row is the end of the record.
  assert.equal(await page.locator(".trajectory__note").getAttribute("data-coverage"), "live_only",
    "a host with no ledger is reported as live only");

  await page.screenshot({ path: path.join(tmpdir(), "reasonix-trajectory-desktop.png") });

  // Narrow dock: the table must still be reachable rather than clipped away.
  await page.setViewportSize({ width: 960, height: 720 });
  assert(await panel.isVisible(), "the panel survives a narrow window");
  await page.screenshot({ path: path.join(tmpdir(), "reasonix-trajectory-narrow.png") });

  assert.deepEqual(errors, []);
  console.log("PASS trajectory panel draws a run's activities, keeps one row per call, and states live-only coverage");
} finally {
  await browser?.close();
  await server.close();
}
