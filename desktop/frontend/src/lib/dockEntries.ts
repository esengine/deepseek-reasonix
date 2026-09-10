// dockEntries lists the right-dock entry points shown in the floating launcher
// and used to map an entry id to its default tab: an id, a translated label
// key, and the default tab type each entry opens. Kept icon-free so the
// composition layer can import it synchronously without pulling lucide into
// the initial bundle — DockLauncher (lazy) attaches the icons.
//
// Remote (远程) is intentionally NOT listed: it stays reachable via the
// sidebar/status-bar switcher, the command palette and settings. DockLauncher
// further hides the changed (改动) entry for non-git projects, and the branch
// row only renders when the active workspace reports a git branch.

import type { TabType } from "../store/activityBar";

export interface DockEntryConfig {
  id: string;
  labelKey: string;
  defaultTab: TabType;
  /** Only offered when the shell exposes an embedded browser surface. */
  requiresBrowser?: boolean;
}

export const DOCK_ENTRIES: DockEntryConfig[] = [
  { id: "context", labelKey: "rightDock.overview", defaultTab: "context" },
  { id: "files", labelKey: "workspace.filesTab", defaultTab: "file" },
  { id: "changed", labelKey: "workspace.changedTab", defaultTab: "changed" },
  { id: "browser", labelKey: "rightDock.browser", defaultTab: "browser", requiresBrowser: true },
  { id: "trajectory", labelKey: "rightDock.trajectory", defaultTab: "trajectory" },
];

export function availableDockEntries(browserAvailable: boolean): DockEntryConfig[] {
  return DOCK_ENTRIES.filter((entry) => !entry.requiresBrowser || browserAvailable);
}
