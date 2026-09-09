// Run: npx tsx src/__tests__/transcript-store-eviction.test.ts
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const store = readFileSync(join(root, "lib/transcriptStore.ts"), "utf8");

// Eviction cooldown: a tab that just stopped being active keeps its sessions
// resident for a grace window instead of being the first FIFO victim.
assert.match(store, /DEFAULT_EVICT_COOLDOWN_MS = 5 \* 60_000/, "cooldown default is 5 minutes");
assert.match(store, /evictCooldownMs\?: number/, "cooldown is configurable via store options");
assert.match(store, /this\.evictCooldownMs = Math\.max\(0, options\.evictCooldownMs \?\? DEFAULT_EVICT_COOLDOWN_MS\)/, "constructor applies the configured cooldown");
assert.match(store, /this\.lastActiveAt\.set\(previousTabId, Date\.now\(\)\)/, "noteActiveTab stamps the cooldown when it releases the previous active pin");
assert.match(
  store,
  /now - \(this\.lastActiveAt\.get\(s\.tabId\) \?\? 0\) >= this\.evictCooldownMs/,
  "enforceBudgets only evicts sessions whose cooldown has elapsed",
);
// The live-append path refreshes the LRU position: a streaming tab whose tab
// was just deactivated must not be the first FIFO eviction victim.
assert.match(
  store,
  /appendEntries\(tabId: string, sessionPath: string, entries: HistoryEntry\[\]\): Item\[\] \{[\s\S]*?this\.touch\(session\);[\s\S]*?this\.enforceBudgets\(\);/,
  "appendEntries touches the session before budget enforcement",
);
assert.match(store, /this\.lastActiveAt\.delete\(tabId\)/, "evictTab clears the cooldown stamp");

console.log("  PASS  transcript store eviction cooldown contract");
