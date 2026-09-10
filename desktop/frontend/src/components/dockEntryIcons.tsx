// Icons for the dock's entry points, shared by the floating launcher and the
// tab picker. Kept out of lib/dockEntries so that module stays icon-free for
// the synchronous composition imports; both consumers here are lazy.
import { Activity, ChartNoAxesGantt, FileDiff, FileText } from "lucide-react";
import type { ComponentType } from "react";
import type { TabType } from "../store/activityBar";

export const DOCK_ENTRY_ICONS: Record<TabType, ComponentType<{ size?: number | string; className?: string }>> = {
  file: FileText,
  changed: FileDiff,
  context: Activity,
  remote: FileText,
  browser: FileText,
  trajectory: ChartNoAxesGantt,
};
