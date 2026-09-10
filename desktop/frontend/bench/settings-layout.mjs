#!/usr/bin/env node
import assert from "node:assert/strict";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { startPreviewServer } from "./vite-preview-server.mjs";
import { chooseAppLayout } from "./app-page-actions.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.env.PLAYWRIGHT_BROWSERS_PATH = !process.env.PLAYWRIGHT_BROWSERS_PATH || process.env.PLAYWRIGHT_BROWSERS_PATH === ".pw-browsers"
  ? path.join(root, ".pw-browsers") : process.env.PLAYWRIGHT_BROWSERS_PATH;
const engines = await import("playwright");
const port = Number(process.env.REASONIX_SETTINGS_PORT ?? 4679);
const preview = await startPreviewServer(root, port);
const themes = ["graphite", "aurora", "slate", "carbon", "nocturne", "amber"];
const sizes = [1600, 1100, 900, 700, 400];
const assignmentRows = 5;
let cases = 0;

async function settle(page) {
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
}

async function verifySaveBars(page, context) {
  for (const [tab, fields] of [["Hooks", ".hooks-json-panel__textarea"], ["Network", ".settings-page--network input"]]) {
    await page.getByRole("navigation", { name: "Settings", exact: true }).getByRole("button", { name: tab, exact: true }).click();
    if (tab === "Network") await page.getByRole("button", { name: "custom", exact: true }).click();
    await page.locator(fields).first().waitFor();
    for (const zoom of [1, 1.5]) {
      await page.evaluate(zoom => { document.documentElement.style.zoom = String(zoom); }, zoom);
      for (const [width, height] of [[1100, 700], [900, 600], [400, 600]]) {
        await page.setViewportSize({ width, height });
        for (const fraction of [0, 0.5, 1]) {
          await page.locator(".settings-center__content").evaluate((el, fraction) => {
            el.scrollTop = (el.scrollHeight - el.clientHeight) * fraction;
          }, fraction);
          await settle(page);
          const overlap = await page.evaluate(fields => {
            const bar = document.querySelector(".settings-save-bar").getBoundingClientRect();
            return [...document.querySelectorAll(fields)].some(el => {
              const field = el.getBoundingClientRect();
              return field.top < bar.bottom && field.bottom > bar.top;
            });
          }, fields);
          assert.equal(overlap, false, `${context}/${tab}/${width}x${height}/${zoom}x/${fraction}: save bar never covers form fields`);
        }
        const cancel = page.locator(".settings-save-bar").getByRole("button", { name: "Cancel", exact: true });
        await cancel.focus();
        const reachable = await cancel.evaluate(el => {
          const r = el.getBoundingClientRect();
          return el.contains(document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2));
        });
        assert.ok(reachable, `${context}/${tab}/${width}x${height}/${zoom}x: save bar remains reachable by keyboard`);
      }
    }
    await page.evaluate(() => { document.documentElement.style.zoom = "1"; });
    await page.setViewportSize({ width: 1600, height: 1100 });
  }
  console.log(`PASS ${context} Hooks and Network save bars`);
}

function geometry() {
  const rect = el => {
    const r = el.getBoundingClientRect();
    return { left: r.left, right: r.right, top: r.top, bottom: r.bottom };
  };
  const group = document.querySelector(".model-preferences");
  const head = group?.querySelector(".model-assignment-head");
  const general = document.querySelector(".settings-page--general");
  const nav = document.querySelector(".settings-center__nav");
  const search = nav.querySelector(".settings-center__search");
  const navgroups = nav.querySelector(".settings-center__navgroups");
  return {
    navigation: {
      searchVisible: getComputedStyle(search).display !== "none",
      search: rect(search),
      list: rect(navgroups),
      listOverflowY: getComputedStyle(navgroups).overflowY,
      outerScroll: nav.scrollTop,
    },
    pageWidth: general?.clientWidth,
    generalContainer: general && getComputedStyle(general).containerName,
    soundColumns: general && [...general.querySelectorAll(".settings-sound-row")].map(row => getComputedStyle(row).gridTemplateColumns.split(" ").length),
    statusColumns: general && getComputedStyle(general.querySelector(".status-bar-items-setting")).gridTemplateColumns.split(" ").length,
    width: group?.clientWidth,
    headVisible: head && getComputedStyle(head).display !== "none",
    headers: head && [...head.children].map(rect),
    rows: group && [...group.querySelectorAll(".model-assignment-row")].map(row => ({
      bounds: rect(row),
      label: rect(row.querySelector(".settings-field__copy")),
      picker: rect(row.querySelector(".settings-model-picker")),
      connection: rect(row.querySelector(".model-assignment-connection")),
      columns: getComputedStyle(row).gridTemplateColumns.split(" ").length,
    })),
  };
}

try {
  const targets = (process.env.REASONIX_SETTINGS_BROWSERS ?? "chromium").split(",")
    .flatMap(engine => ["windows", "darwin", "linux"].map(platform => [engine, platform]));
  for (const [engineName, platform] of targets) {
    const browser = await engines[engineName].launch({ headless: true });
    try {
      const page = await browser.newPage({ locale: "en-US", viewport: { width: 1600, height: 1100 } });
      const errors = [];
      page.on("pageerror", error => errors.push(error.message));
      await page.goto(`http://127.0.0.1:${port}/?mock=deepseek_upgrade&bench=1&platform=${platform}`, { waitUntil: "domcontentloaded" });
      await page.locator("textarea.composer__input:not([aria-hidden=true])").waitFor();
      for (const layout of [["Creation", "app--creation"], ["Workbench", "app--workbench"]]) {
        await page.setViewportSize({ width: 1600, height: 1100 });
        await page.evaluate(() => { document.documentElement.style.zoom = "1"; });
        await chooseAppLayout(page, ...layout);
        await page.locator('button:has(svg.lucide-settings)').last().click();
        await page.locator(".settings-page--general").waitFor();
        await page.getByRole("button", { name: "Expand sound settings", exact: true }).click();
        for (const width of sizes) {
          await page.setViewportSize({ width, height: 1100 });
          await settle(page);
          const g = await page.evaluate(geometry);
          assert.equal(g.generalContainer, "settings-general", "general page retains its responsive container");
          if (g.pageWidth <= 440) {
            assert.ok(g.soundColumns.length > 0, "sound controls are present");
            assert.ok(g.soundColumns.every(columns => columns === 1), "narrow sound controls stack within the general page");
          }
          assert.equal(g.statusColumns, 1, `status bar editor owns one full-width column at ${width}px`);
        }
        console.log(`PASS ${engineName}/${platform}/${layout[0]} general settings`);
        await page.setViewportSize({ width: 1600, height: 1100 });
        await verifySaveBars(page, `${engineName}/${platform}/${layout[0]}`);
        await page.getByRole("button", { name: "Model preferences", exact: true }).click();
        await page.locator(".model-assignment-row").first().waitFor();
        for (const theme of themes) {
          for (const zoom of [1, 1.5]) {
            await page.evaluate(({ theme, zoom }) => {
              document.documentElement.dataset.themeStyle = theme;
              document.documentElement.style.zoom = String(zoom);
            }, { theme, zoom });
            for (const width of sizes) {
              await page.setViewportSize({ width, height: 1100 });
              await settle(page);
              const g = await page.evaluate(geometry);
              const context = `${engineName}/${platform}/${layout[0]}/${theme}/${width}px/${zoom}x`;
              if (g.navigation.searchVisible) {
                assert.ok(g.navigation.list.top >= g.navigation.search.bottom, `${context}: navigation viewport stays below search`);
                assert.equal(g.navigation.listOverflowY, "auto", `${context}: navigation list owns vertical scrolling`);
                assert.equal(g.navigation.outerScroll, 0, `${context}: search container never scrolls with navigation`);
              }
              assert.equal(g.rows.length, assignmentRows, `${context}: all assignments remain visible`);
              const columns = g.width > 780 ? 3 : g.width > 440 ? 2 : 1;
              assert.equal(g.headVisible, columns === 3, `${context}: header follows content width`);
              for (const row of g.rows) {
                assert.equal(row.columns, columns, `${context}: assignment owns its columns`);
                assert.ok(row.picker.left >= row.bounds.left - 1 && row.picker.right <= row.bounds.right + 1, `${context}: picker stays within row`);
                assert.ok(row.connection.right <= row.bounds.right + 1, `${context}: connection stays within row`);
                if (columns === 3) {
                  assert.ok(Math.abs(row.picker.left - g.headers[1].left) < 2, `${context}: model aligns with header`);
                  assert.ok(Math.abs(row.connection.left - g.headers[2].left) < 2, `${context}: connection aligns with header`);
                  assert.ok(row.connection.top < row.picker.bottom && row.connection.bottom > row.picker.top, `${context}: connection stays on same row`);
                } else {
                  assert.ok(row.connection.top >= row.picker.bottom - 1, `${context}: connection follows its model`);
                  assert.ok(Math.abs(row.connection.left - row.picker.left) < 2, `${context}: stacked connection aligns with model`);
                }
              }
              cases++;
            }
          }
        }
        await page.evaluate(() => { document.documentElement.style.zoom = "1"; });
        await page.setViewportSize({ width: 1600, height: 1100 });
        const search = page.getByRole("textbox", { name: "Search settings", exact: true });
        const searchBefore = await search.boundingBox();
        const navItems = page.locator(".settings-center__navitem");
        // Derived, not a literal: the contract is "clearing search restores
        // every item", so the page owns the total and a new settings page does
        // not have to edit this benchmark.
        const navItemCount = await navItems.count();
        await navItems.last().scrollIntoViewIfNeeded();
        await settle(page);
        const scrolled = await page.evaluate(geometry);
        assert.ok(scrolled.navigation.list.top >= scrolled.navigation.search.bottom, "scrolled navigation stays below search");
        assert.equal(scrolled.navigation.outerScroll, 0, "revealing the last tab does not scroll search");
        assert.deepEqual(await search.boundingBox(), searchBefore, "search stays fixed when revealing the last tab");
        await search.fill("no-such-setting-regression");
        await page.locator(".settings-center__navempty").waitFor();
        await page.getByRole("button", { name: "Clear settings search", exact: true }).click();
        assert.equal(await navItems.count(), navItemCount, "clearing search restores every navigation item");
        const picker = page.getByRole("button", { name: "Default model", exact: true });
        await picker.click();
        await page.getByRole("listbox", { name: "Default model", exact: true }).waitFor();
        await page.keyboard.press("Escape");
        await page.getByRole("listbox", { name: "Default model", exact: true }).waitFor({ state: "detached" });
        console.log(`PASS ${engineName}/${platform}/${layout[0]} navigation, assignments and picker`);
        await page.locator(".settings-screen .management-screen__back").click();
      }
      assert.deepEqual(errors, [], `${engineName}/${platform}: no runtime errors`);
    } finally { await browser.close(); }
  }
  console.log(`PASS settings layout: ${cases} real-page theme/layout/width/zoom cases plus general settings and picker interaction`);
} finally { await new Promise((resolve, reject) => preview.httpServer.close(error => error ? reject(error) : resolve())); }
