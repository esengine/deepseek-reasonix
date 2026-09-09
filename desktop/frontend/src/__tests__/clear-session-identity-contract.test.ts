// Run: npx tsx src/__tests__/clear-session-identity-contract.test.ts
//
// clearSession must fence transcript identity so "clear → immediately switch
// mode/layout" cannot re-paint the destroyed session from TranscriptStore or
// a hydrate started with stale meta.sessionPath.

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const controller = readFileSync(join(root, "lib/useController.ts"), "utf8");
const store = readFileSync(join(root, "lib/transcriptStore.ts"), "utf8");
const types = readFileSync(join(root, "lib/types.ts"), "utf8");
const historyTypes = readFileSync(join(root, "lib/historyTypes.ts"), "utf8");

assert.match(types + historyTypes, /export interface SessionClearResult/, "backend clear result is typed");
assert.match(types, /sessionGeneration/, "sessionGeneration is part of identity");
assert.match(controller, /ClearSessionForTab/, "clear uses tab-scoped clear API");
assert.match(controller, /evictTab\(tabId\)/, "clear evicts TranscriptStore for the tab");
assert.match(controller, /sessionGeneration:\s*cleared\.sessionGeneration/, "clear applies returned generation");
assert.match(controller, /sessionPath:\s*cleared\.sessionPath/, "clear applies returned path");
assert.match(controller, /optimistic_meta[\s\S]*reset/, "meta is updated before reset so identity survives");
assert.match(controller, /hydrateIdentityCurrent\(/, "hydrate rejects path/generation identity drift");
assert.match(store, /evictTab\(tabId/, "TranscriptStore can drop a tab's resident sessions");

console.log("  PASS  clear-session identity contract");
