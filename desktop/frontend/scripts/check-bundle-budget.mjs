import { readFileSync, readdirSync, statSync } from "node:fs";
import { basename, resolve } from "node:path";
import { gzipSync } from "node:zlib";

const distDir = resolve("dist");
const indexPath = resolve(distDir, "index.html");
const html = readFileSync(indexPath, "utf8");

function gzipBytes(path) {
  return gzipSync(readFileSync(path), { level: 9 }).byteLength;
}

function initialAssetPaths(extension) {
  const pattern = extension === ".js"
    ? /<(?:script|link)\b[^>]+(?:src|href)=["']([^"']+\.js)["'][^>]*>/g
    : /<link\b[^>]+href=["']([^"']+\.css)["'][^>]*>/g;
  return [...new Set([...html.matchAll(pattern)].map((match) => resolve(distDir, match[1])))];
}

function formatKiB(bytes) {
  return `${(bytes / 1024).toFixed(1)} KiB`;
}

function assertBudget(label, actual, budget) {
  if (actual > budget) {
    throw new Error(`${label} is ${formatKiB(actual)}; budget is ${formatKiB(budget)}`);
  }
  process.stdout.write(`  PASS  ${label}: ${formatKiB(actual)} / ${formatKiB(budget)}\n`);
}

const initialJS = initialAssetPaths(".js");
const initialCSS = initialAssetPaths(".css");
if (!initialJS.length) throw new Error("no initial JavaScript assets found in dist/index.html");
// initial CSS 允许为空：styles.css 走 ?url 延迟加载，feature 样式走 lazy chunk。

// main.tsx intentionally loads styles.css before mounting React so the inline
// boot shell can paint without waiting for the full application stylesheet.
// Vite emits that entry as styles-<hash>.css; keep it in the startup budget
// while also proving it never drifts back into the render-blocking HTML path.
const appShellCSS = readdirSync(resolve(distDir, "assets"))
  .filter((name) => /^styles-.+\.css$/.test(name))
  .map((name) => resolve(distDir, "assets", name));
if (appShellCSS.length !== 1) {
  throw new Error(`expected exactly one deferred app-shell stylesheet, found ${appShellCSS.length}`);
}
if (initialCSS.some((path) => appShellCSS.includes(path))) {
  throw new Error("app-shell stylesheet must not block the inline boot shell's first paint");
}

const initialJSGzip = initialJS.reduce((total, path) => total + gzipBytes(path), 0);
const initialCSSGzip = initialCSS.reduce((total, path) => total + gzipBytes(path), 0);
const appShellCSSGzip = appShellCSS.reduce((total, path) => total + gzipBytes(path), 0);
const largestInitialJS = Math.max(...initialJS.map(gzipBytes));
const largestInitialJSRaw = Math.max(...initialJS.map((path) => statSync(path).size));
const localeChunks = readdirSync(resolve(distDir, "assets"))
  .filter((name) => /^(?:zh|zh-TW|es)-.+\.js$/.test(name))
  .map((name) => resolve(distDir, "assets", name));

console.log("\nbundle budgets");
// React Virtuoso replaces the transcript's custom measurement/anchor engine.
// Its production runtime adds 16.9 KiB gzip (4.2%) over the 402 KiB baseline.
// This exceptional overrun is locally attributable and trades ~1400 lines of
// competing state machines for a maintained library. Native-tail finish helpers
// then sat on the 423.5 KiB gate (Windows CI: 423.5 / 423.5); this 0.5 KiB
// raise (0.12%) absorbs that leave-cancel / remasure-once code without
// widening the original Virtuoso exception. The project-tree archive race
// guards add 611 bytes gzip over main-v2's 423.988 KiB startup path after the
// blank-project flow landed; project-topic sort invalidation and request
// ordering add another bounded 0.2 KiB. Retain both owner boundaries with a
// narrowly rounded 1 KiB ratchet.
// Diagnostic builds intentionally keep content-free row geometry and scroll
// transition probes in the initial transcript path. Stable builds retain the
// existing production ratchet. Per-row measurement versions and a bounded
// recovery probe add less than 0.1% gzip; retain them with a 0.5 KiB (0.118%)
// production ratchet rather than weakening either recovery contract. The
// bounded allowance also covers small gzip drift from the embedded build SHA.
// Reader extent stabilization adds 1.2 KiB gzip (0.28%) in production for its
// bounded input, collapse, rebound, and ownership transaction. Retain it with
// a 1.5 KiB (0.35%) ratchet instead of weakening the Windows scroll invariant.
// Complete-history navigation adds 0.3 KiB gzip (0.070%) to that production
// path while keeping its 1.68 KiB question rail lazy-loaded. Test diagnostics
// plus the navigation owner add 0.7 KiB gzip (0.164%) over the merged test gate.
// DingTalk channel status and locale wiring move the current-base production
// build from 427.2 to 427.7 KiB and test from 428.6 to 429.1 KiB. The unified
// state-aware geometry contract, session diagnostics counters, and guarded
// native-scroll probes add 2.4 KiB gzip to the initial path. The current
// main-v2 merge adds another 0.3 KiB of deterministic shared startup code.
// Keep the increase explicit and bounded instead of hiding it in a broad
// percentage ratchet.
// The retained-transcript surface adds a small, bounded navigation owner to
// the startup path (overlay state + stale-completion guard). Keep the increase
// explicit and narrow; the measured build is 431.1 KiB gzip.
// The web-search tool card now resolves the same display projection lazily so
// its filtered count matches the assistant Sources panel. The measured build
// is 431.509 KiB gzip; keep 0.1 KiB of explicit headroom for hash/toolchain
// drift instead of relying on a rounded equality.
// Remote onboarding [0.5/3] adds project-group and credential-chain wiring on
// top of the lazy wizard. Exact-turn routing, the extracted event-gap
// projector, checkpoint resets, and the navigation surface transaction bring
// the current main-v2 path to 437.36 KiB gzip.
// The full remote-session surface adds the lazy transcript bridge and tab
// lifecycle on top of [0.5/3]. Keep the measured stack's narrow ratchet.
// Remote approval hardening adds authoritative composer-profile hydration,
// scoped rewind dispatch, and attachment/inbox fences to the always-mounted
// remote hook. The measured production path is 438.38 KiB gzip after keeping
// the integration modules below repolint's ownership ceilings; retain 0.12 KiB
// toolchain headroom with a bounded 1.4 KiB ratchet.
// Remote status isolation keeps the always-mounted status bar on the active
// remote transcript and routes job cancellation to that host. Parsers and
// retry policy remain lazy; the measured selector adds under 0.1 KiB gzip.
// Remote runtime parity adds scoped approvals, status-only reconciliation,
// session quality-floor routing, dropped-frame reconciliation, and remote
// runtime-command dispatch. The measured initial path is 439.60 KiB;
// retain 0.10 KiB of bounded toolchain headroom.
// Closing the remaining review gaps adds generation-fenced hydration plus
// remote-only tool payload, Todo, and terminal isolation. The measured path is
// 439.74 KiB; retain 0.06 KiB of headroom with a 0.1 KiB ratchet.
// The final remote-runtime parity pass adds remote run-strip telemetry,
// explicit session verbs, and specialized plan decisions. The measured path
// is 440.02 KiB. The current main-v2 turn-event, finish-protocol, and session
// repair runtime then moves the combined path to 445.097 KiB; retain 0.103 KiB
// of bounded build/toolchain headroom.
// Atomic remote profile changes, exact approval draining, and generation-safe
// history handoff bring the measured path to 445.228 KiB. Retain 0.072 KiB of
// headroom with the smallest existing decimal ratchet.
// Direct pending-prompt recovery and authoritative remote Goal state bring the
// measured path to 445.473 KiB. Retain 0.027 KiB of bounded headroom.
// Restored remote shells now activate their backend session immediately and
// keep disconnected state out of the mounted surface. The merged production
// path measures 445.614 KiB; retain 0.086 KiB of bounded build/toolchain
// headroom with the smallest existing decimal ratchet.
// Runtime-aware Todo presentation plus exact-tab continuation adds 0.3 KiB gzip
// to the always-mounted footer path. Keep the state/routing guard with a narrow
// ratchet rather than showing idle restored work as actively running. The
// combined path measures 445.9 KiB; retain 0.1 KiB of toolchain headroom.
// Transcript surface ownership and the token-fenced unloaded-question commit
// move the exact main-v2 baseline from 445.865 to 447.587 KiB gzip (+0.39%).
// The final 0.266 KiB retains jump ownership through paint-ready instead of
// allowing a native scrollend to release it. Keep only 0.213 KiB headroom;
// native validation hosts and test fixtures stay outside the production graph.
// Cross-platform shell inventory, current-session vs after-reload rows,
// manual repair guidance, and exact download-host allowlisting move the merged
// path from 448.692 to 449.758 KiB (+1.066 KiB). Retain 0.142 KiB of bounded
// build/toolchain headroom.
// The reader transaction contract (geometry revisions, generation-fenced
// writer requests, gesture travel proof, stabilized-shrink extent acceptance,
// and the blank-rebound prepaint lane) adds a measured 3.978 KiB gzip on the
// merged main-v2 baseline. MCP elicitation and the inline Apps lifecycle remain
// on that startup graph; the combined path measures 455.0 KiB. Retain 0.2 KiB
// of bounded build/toolchain headroom.
// Generic elicitation validation adds field-specific localized accessibility
// copy to the English startup dictionary. The interaction code and CSS remain
// lazy; the measured path is 455.437 KiB. Retain 0.163 KiB of headroom.
// Stream-failure visibility (#9560) adds the last-discard reason and one
// terminal-notice dedupe flag, while provider no_proxy copy now states the
// custom-proxy precedence. The merged path measures 455.9 KiB; retain 0.1 KiB
// of bounded build/toolchain headroom.
// Exhausted tail repair now releases ownership so jump-bottom remains usable
// after a stranded native WebView extent. The WebView2 reachable-tail clamp
// then absorbs a second post-quiet extent without an unbounded write loop.
// The combined path measures 456.316 KiB; retain 0.084 KiB with the smallest
// one-decimal ratchet.
// The generation-bound history-prepend lease adds stable-key reader anchoring,
// full mounted coverage, and one final arbiter-owned correction. The measured
// path is 457.406 KiB after extracting the lease owner to satisfy repolint.
// Latest-base transcript settle ownership measures 457.523 KiB with this UX.
// Isolated conversation forks and their extracted browser mock adapter bring
// the combined tree to 458.158 KiB; completion uncertainty adds a terminal
// outcome and notice without exposing evaluator audits to the frontend,
// measuring 458.287 KiB gzip.
// Transactional Ask resolution and authoritative rejected-submit recovery add
// 0.3 KiB gzip to the initial controller path. Retain the exact turn fence,
// bounded ListTabs retry, and stale-prompt guard.
// Session-catalog repair presentation stays in the lazy project-tree chunk;
// compact shared helpers keep the combined initial path within the same gate.
// Merge-Back adds identity-bound inspection, navigation, and retained-recovery
// orchestration on top. The merged stable build measures 461.338 KiB and the
// test channel measures 461.323 KiB. Deferring selection ownership until a
// real range exists (#9703/#9711) and adding the session takeover banners
// move the combined path to 462.2 KiB. Local spectator reclaim adds the
// desktop-vs-remote command branch. Sticky Context's session-scoped file chips
// bring the merged stable path to 462.587 KiB. Windows' embedded build metadata
// lands just above the rounded 462.6 KiB boundary; retain one cross-platform
// decimal step without widening any chunk or raw gate.
// Reading the applied item-list transform (instead of the remembered offset)
// keeps the reader/anchor visual guards from compounding under reduced-motion
// WebView2; the merged path measures 462.827 KiB. Retain one decimal step.
// Generation-bound native-thumb transactions and the rebased custom-scrollbar
// drag add 0.3 KiB gzip; the merged path measures 463.102 KiB.
// Absorbing content-preserving block-window prepends into the active reader
// transaction adds 0.2 KiB gzip on top; the merged path measures 463.292 KiB,
// 8 bytes under the next decimal. Retain one cross-platform decimal step.
// The subagent outcome envelope, partial-state card, and history hydration add
// 0.5 KiB gzip on the initial path. The model-capability resolver and its
// read-only provider badges add a measured 0.2 KiB including gzip/toolchain
// rounding. The integrated management shell, image capability controls, and
// upstream updater refresh measure 465.4 KiB gzip (base: 464.7 KiB).
// Keep the next decimal ceiling and leave feature editors lazy.
// Durable protocol recovery controls and search-source status add 1.2 KiB
// over the same-environment main-v2 build (465.4 -> 466.6 KiB gzip).
// Keep one decimal of cross-platform headroom for this measured shell change.
// Integrating main-v2 rich-link menus measures 466.905 KiB combined.
// The AskCard session-draft wiring adds a bounded 30-byte gzip drift on the
// initial route. The session-runtime ordering fence adds 56 bytes and
// cross-platform zlib rounding reaches the same startup path; retain the
// explicit budget rather than failing on a rounded 467.0 KiB display value.
// The latest main-v2 session-runtime fence and exact prompt protocol measure
// 468.2 KiB here; retain a 0.1 KiB ceiling for platform zlib rounding.
// Mainline provider/settings integration measures 469.230 KiB gzip.
// Spanish (es) wiring — Locale union, LANGUAGE_PREFS, SPINNER_WORDS.es and the
// /language picker label — adds ~0.2 KiB gzip on top of that base; the
// 3,527-key es.ts dictionary stays in a lazy on-demand chunk. Retain a
// 0.1 KiB ceiling for platform/zlib rounding.
const initialJSBudgetKiB = 469.5;
assertBudget("initial JavaScript gzip", initialJSGzip, initialJSBudgetKiB * 1024);
assertBudget("largest initial JavaScript chunk gzip", largestInitialJS, 280 * 1024);
// Render-blocking CSS is intentionally absent: styles.css loads deferred via
// ?url, and feature styles (heartbeat) live in lazy chunks loaded on demand.
// An empty initial CSS list is the desired state, not a build error.
if (initialCSS.length > 0) {
  assertBudget("render-blocking CSS gzip", initialCSSGzip, 4 * 1024);
} else {
  process.stdout.write("  PASS  render-blocking CSS: none (all styles deferred)\n");
}
// Extension surfaces, Task Monitor, and compact decision receipts share the
// application stylesheet loaded before React mounts. Keep their combined
// allowance bounded even though the file is no longer render-blocking.
// Navigation overlay styles add a bounded 0.1 KiB to the deferred shell.
// The cleaned source panel adds 0.1 KiB gzip to the deferred shell on top of
// the retained-transcript navigation allowance; keep the ratchet explicit.
// The navigation mask's stable composer footprint and remote tab/surface
// states bring the merged shell to roughly 115.7 KiB gzip.
// The one-row model configuration list, responsive stacking, Automation's
// shared title-safe shell, and the shared harness decision surface measure
// 116.9 KiB gzip while reusing existing layout primitives. Retain a bounded
// 0.1 KiB headroom ratchet.
// Mainline provider/settings and recovery styles measure 119.435 KiB gzip.
assertBudget("deferred app-shell CSS gzip", appShellCSSGzip, 119.5 * 1024);
if (localeChunks.length !== 3) {
  throw new Error(`expected 3 on-demand locale chunks (zh, zh-TW, es), found ${localeChunks.length}`);
}
for (const path of localeChunks) {
  const name = basename(path);
  // Task Monitor, billing, indexed history, Task Center, Extension UI, and
  // runtime controls plus execution-setting receipts add localized copy. The
  // write-access approval card adds four scoped actions and a home-risk
  // warning (~0.15 KiB gzip, +0.27% over the old 54.75 gate). Context
  // compaction settings add 40 bytes gzip of policy guidance to simplified
  // Chinese, while scheduled billing adds compact rate-band labels/tooltips.
  // The three StepFun presets add localized names/descriptions (~0.1 KiB
  // gzip); the two pay-as-you-go presets add the same again. The delivery
  // floor segmented control adds two labels plus one explanatory tooltip,
  // measured at 23 B gzip for zh and 8 B for zh-TW. Completion receipts add
  // six short status labels in each locale, requiring another 0.2 KiB per
  // language. DingTalk setup and mention guidance add at most 0.2 KiB more
  // (0.36%); retain the complete security and group-chat copy instead of
  // abbreviating user-facing instructions to fit the old locale ratchet.
  // Recovery-copy and catalog-only sidebar labels can move the simplified
  // Chinese chunk across the rounded 55.9 KiB boundary on CI's Node/zlib;
  // retain a narrow 0.1 KiB headroom rather than making gzip output a
  // platform-dependent gate. The OpenCode one-key setup adds product-level
  // connection, fallback, and legacy-state copy while removing protocol
  // choices from the primary UI; keep that complete guidance with a bounded
  // 0.4–0.5 KiB locale-only ratchet.
  // Git-Bash installation guidance adds localized copy across dialects.
  // MCP elicitation adds fourteen short labels per locale (~40 B gzip).
  // Generic schema validation adds complete field-error, privacy, and safe-
  // fallback copy. Measured chunks are 58.574 KiB zh and 59.368 KiB zh-TW;
  // retain roughly 0.13 KiB of platform headroom for each.
  // Stream-failure diagnostics add five strings per dialect. Together with the
  // reachable-tail recovery copy, the merged chunks measure 58.923 KiB zh and
  // 59.710 KiB zh-TW. The isolated-fork guidance brings the measured chunks
  // to 59.1 KiB zh and 59.9 KiB zh-TW; retain a narrow one-decimal ratchet.
  // Merge-Back lifecycle and recovery guidance measure 59.819 KiB zh and
  // 60.612 KiB zh-TW; retain only the next one-decimal ceiling for each.
  // The retained-recovery receipt and copy action move zh to 59.911 KiB;
  // session-catalog recovery guidance on the merged base moves zh-TW to
  // 60.757 KiB; retain only its exact one-decimal ceiling.
  // Session takeover adds ~20 locale keys per dialect (banners, dialog,
  // reclaim), while Sticky Context adds file-state and limit diagnostics. The
  // merged stable chunks measure 60.395 KiB zh and 61.232 KiB zh-TW; retain
  // only the next one-decimal ceiling for each dialect.
  // The outcome card adds one short localized status label per dialect. CI's
  // Windows zlib measured zh at 60.4 KiB exactly; capability-status copy adds
  // a small 0.1 KiB ratchet, so retain the next decimal ceiling rather than
  // dropping the unknown-state explanation.
  // Image input mode, provenance and unknown-state guidance measure 60.724 KiB
  // zh and 61.570 KiB zh-TW. Keep the next decimal ceiling per locale.
  // Protocol recovery and source-availability copy measure 60.927 KiB zh
  // and 61.789 KiB zh-TW (base: 60.8 / 61.6 rounded).
  // Rich-link action copy on the current base brings these to
  // 61.027/61.881 KiB; retain bounded cross-platform headroom.
  // Recovery retry copy reaches the rounded 61.1 KiB boundary on Node/zlib
  // toolchains; keep the next one-decimal ceiling for cross-platform CI.
  // The #9889/#9890 series adds recovery-wait, dialog-failure, and stall copy:
  // zh-TW measures 63492 B (62.004 KiB) with the four PRs merged together.
  // Integrated settings and ownership copy measures 61.415 / 62.212 KiB.
  // The Spanish locale ships the same full 3,527-key dictionary (grown to
  // 3,627 with the upstream catalog keys); Spanish prose is longer than Chinese
  // per key, so the measured es chunk is 62.2 KiB gzip on this base.
  // Retain a 0.1 KiB headroom at the next one-decimal ceiling.
  const budget = name.startsWith("zh-TW-") ? 62.3 * 1024
    : name.startsWith("zh-") ? 61.5 * 1024
    : 62.3 * 1024;
  assertBudget(`${name} gzip`, gzipBytes(path), budget);
}

const rawInitialBytes = [...initialJS, ...initialCSS, ...appShellCSS]
  .reduce((total, path) => total + statSync(path).size, 0);
// The maintained Virtuoso engine adds 49.1 KiB raw (2.2%) over the previous
// 2268.7 KiB gate. Navigation remains inside the 2341 KiB production ceiling;
// its combined diagnostic wiring adds 2.2 KiB (0.094%) to the test channel.
// DingTalk startup wiring moves current-base production from 2341.0 to 2343.6
// KiB and test from 2346.2 to 2348.8 KiB; the pinned heading adds 0.5 KiB raw
// (0.021%). The workspace panel rework (change-row hover/revert, status badges,
// More menu, completion summary) makes the latest-base merge 2353.1 KiB in
// production and test channels both measure 2357.92 KiB after project-group
// wiring. Exact-turn routing, checkpoint resets, and failure-atomic navigation
// bring the current main-v2 path to 2379.22 KiB. The remote approval
// fences, extracted ownership modules, and remote status-bar isolation bring
// the measured initial payload to 2380.9 KiB; retain 0.1 KiB of bounded
// raw/toolchain headroom. Scoped remote approvals, status reconciliation, and
// runtime command dispatch bring the measured payload to 2382.9 KiB. The
// remaining review fences measure 2383.2 KiB; retain 0.1 KiB of headroom.
// Final remote-runtime parity measures 2384.4 KiB raw. The current main-v2
// runtime additions bring the combined path to 2404.364 KiB. The final merged
// restored-shell activation and disconnected-state revival path measures
// 2404.898 KiB; retain 0.102 KiB of bounded headroom alongside the gzip
// ratchet above.
// Runtime-aware Todo status and exact-tab continuation then add to the same
// initial path. The combined payload measures 2406.2 KiB; retain 0.1 KiB of
// raw/toolchain headroom for both owners.
// The same transcript transaction measures 2413.012 KiB raw (+0.28%) against
// the 2406.204 KiB baseline. Retain 0.188 KiB of bounded headroom.
// The notification-volume control adds one persisted master gain, per-source
// loudness trims, and its accessible Settings surface. Current main-v2 moves
// from 2413.183 to 2414.879 KiB raw (+1.696 KiB); retain 0.121 KiB of bounded
// headroom.
// Owner-lifecycle reasoning disclosure, pre-paint tail pinning, and the live
// footer growth floor then add 2.390 KiB after extracting ownership modules
// below repolint's source ceilings. Lifecycle fencing adds 0.258 KiB; the
// combined path measures 2417.526 KiB. Retain 0.074 KiB while preventing
// phase-boundary reverse flashes and cross-surface floor leaks.
// The same shell-support surface moves the merged path from 2417.526 to
// 2422.371 KiB raw (+4.845 KiB). Retain 0.129 KiB of bounded headroom without
// widening unrelated chunk ceilings.
// The WebView2 extent rebound prepaint handoff adds 0.204 KiB raw so a native
// scroll delivery can restore mounted coverage before the next visible frame.
// Retain 0.096 KiB of headroom without widening gzip or chunk ceilings.
// The reader transaction contract then adds a measured 15.317 KiB raw on the
// merged main-v2 baseline (including its own prepaint port). MCP elicitation
// and Apps add their bounded payload on the shared graph; the combined path
// measures 2442.6 KiB. Retain 0.4 KiB of bounded build/toolchain headroom.
// The browser MCP interaction preview adds 0.6 KiB of route wiring while its
// 0.75 KiB form fixture and lifecycle remain lazy. The combined path measures
// 2443.2 KiB; retain 0.1 KiB of bounded build/toolchain headroom.
// Generic field copy adds 1.134 KiB raw to the startup dictionary; all schema
// parsing, rendering, and CSS remain lazy. The measured path is 2444.334 KiB;
// retain 0.166 KiB of bounded build/toolchain headroom.
// The off-flow composer measurement mirror adds 0.472 KiB raw while removing
// live-textarea layout mutation. The merged path measures 2444.806 KiB; retain
// 0.194 KiB of bounded toolchain headroom without widening gzip/chunk gates.
// Stream-failure visibility and corrected proxy guidance bring the merged path
// to 2446.6 KiB; retain the smallest existing decimal ratchet.
// The stranded-tail recovery transition plus the WebView2 reachable-tail clamp
// bring the measured initial payload to 2447.953 KiB. Retain 0.047 KiB with
// the smallest one-decimal ratchet.
// The extracted history-prepend owner adds 3.953 KiB of bounded transaction
// state and stable-key coverage checks. Together with the compact
// session-version host, they measure 2452.7 KiB; the recovery coordinator and
// dialog remain lazy. Completion uncertainty adds a distinct terminal notice
// and localized startup copy without collapsing into recovery-paused UX.
// 2454.719 KiB on the release toolchain. Completion uncertainty brings the
// final merged payload to 2455.154 KiB.
// Ask turn fencing, rejection reconciliation, and the localized submit-failure
// notice measure 2456.044 KiB raw; retain 0.056 KiB of one-decimal headroom.
// Merge-Back's startup ownership and failure-atomic navigation fence add the
// remaining bounded payload. The retained recovery receipt makes the stable
// path 2465.105 KiB raw; the merged test channel measures 2464.979 KiB.
// Session takeover banners and #9703/#9711's provisional-selection handoff
// combine with Sticky Context's pinned-file state at 2469.125 KiB raw on the
// merged stable path. Retain only the next one-decimal ceiling.
// The passive reader-anchor lease for delayed WebView2 range commits measures
// 2469.347 KiB raw (+0.222 KiB, +0.009%). Retain only the next one-decimal
// ceiling; gzip and largest-chunk budgets remain unchanged.
// Reading the applied item-list transform for the reader/anchor visual guards
// adds 0.5 KiB raw on top; the merged path measures 2469.815 KiB.
// The scrollbar generation fence and drag rebase add 1.1 KiB raw; the merged
// path measures 2470.932 KiB.
// The reader-transaction offset absorption adds 0.8 KiB raw on top; the merged
// path measures 2471.741 KiB. Controller-owned management dispositions and
// optimistic management settlement add 0.6 KiB raw; retain the smallest
// one-decimal ceiling with bounded headroom.
// The outcome card and history hydration add 2.3 KiB raw on the initial path
// (2474.0 KiB measured in CI). Keep this narrowly attributable ratchet rather
// than removing persisted-result visibility or changing chunk ownership.
// On the current main-v2 base, the combined measured path is 2474.6 KiB;
// the model-capability helper and localized status copy add 0.9 KiB; retain
// the smallest bounded cross-platform ceiling.
// Retain the upstream updater ceiling and independent chunk gates.
// Recovery controls add 3.6 KiB raw over the measured 2480.9 KiB base;
// current payload is 2484.509 KiB. Retain only bounded toolchain headroom.
// With the current-base rich-link menus: 2485.715 KiB raw.
// The shared harness decision surface adds a bounded startup stylesheet
// payload. The current base plus exact prompt identity and stale-card recovery
// measure 2496.4 KiB locally; retain the smallest bounded ceiling.
// The context truncation-rescue notice and its three locale strings measure
// 2496.6 KiB; retain the smallest bounded ceiling.
// Deferred presentation measured 2381.4 KiB before first-materialization
// preloading. Mainline Stop brings the payload to 2381.8 KiB;
// Mainline recovery UI brings it to 2384.5 KiB, and tool elapsed/liveness
// UI to 2384.9 KiB. Retain 0.2 KiB headroom.
// Mainline provider/settings integration measures 2398.2 KiB in the
// extracted shell. Retain the same bounded 0.2 KiB build headroom.
// Spanish (es) wiring adds ~0.4 KiB raw on top (the dictionary itself is the
// lazy es-*.js chunk, budgeted separately). Retain the same 0.2 KiB headroom.
const rawInitialBudgetKiB = 2_398.8;
assertBudget("initial raw JavaScript and CSS", rawInitialBytes, rawInitialBudgetKiB * 1024);
assertBudget("largest initial JavaScript chunk raw", largestInitialJSRaw, 1_000 * 1024);
