// TabAddMenu is the dropdown opened by the + button in the dock's tab bar.
// It lists the addable tab types (files / changed / overview / instructions);
// selecting one appends that tab and activates it. Every type can be added
// repeatedly, so the list shows no current-selection checkmark. Other panel
// types (terminal, browser, remote) are not exposed yet.

import { useEffect, useLayoutEffect, useRef } from "react";
import type { RefObject } from "react";
import { useT } from "../../lib/i18n";
import { Activity, ChartNoAxesGantt, Compass, FileDiff, FileText } from "lucide-react";
import type { ComponentType } from "react";
import { desktopHost } from "../../lib/desktopHost";
import type { TabType } from "../../store/activityBar";

interface AddableTab {
  type: TabType;
  labelKey: string;
  icon: ComponentType<{ size?: number | string; className?: string }>;
}

const ADDABLE_TABS: AddableTab[] = [
  { type: "file", labelKey: "workspace.filesTab", icon: FileText },
  { type: "changed", labelKey: "workspace.changedTab", icon: FileDiff },
  { type: "context", labelKey: "rightDock.overview", icon: Activity },
  { type: "trajectory", labelKey: "rightDock.trajectory", icon: ChartNoAxesGantt },
];

const BROWSER_TAB: AddableTab = { type: "browser", labelKey: "rightDock.browser", icon: Compass };

interface TabAddMenuProps {
  onPick: (type: TabType, label: string) => void;
  onClose: () => void;
  /** The + button this menu anchors to (opens under it, left-aligned). */
  anchorRef: RefObject<HTMLButtonElement | null>;
}

export function TabAddMenu({ onPick, onClose, anchorRef }: TabAddMenuProps) {
  const t = useT();
  const menuRef = useRef<HTMLDivElement>(null);
  // Anchor the menu under the + button, its left edge aligned to the button's.
  // Coordinates are relative to the dock; when the menu would overflow the
  // dock (narrow dock / window near the screen edge), flip the alignment
  // inward so it stays fully visible.
  useLayoutEffect(() => {
    const el = menuRef.current;
    const anchor = anchorRef.current;
    const dock = el?.closest(".tab-container");
    if (!el || !anchor || !dock) return;
    const menuRect = el.getBoundingClientRect();
    const anchorRect = anchor.getBoundingClientRect();
    const dockRect = dock.getBoundingClientRect();
    const gap = 4;
    let left = anchorRect.left - dockRect.left;
    let top = anchorRect.bottom - dockRect.top + gap;
    if (left + menuRect.width > dockRect.width - gap) {
      left = dockRect.width - menuRect.width - gap;
    }
    if (top + menuRect.height > dockRect.height - gap) {
      top = Math.max(gap, anchorRect.top - dockRect.top - menuRect.height - gap);
    }
    el.style.left = `${left}px`;
    el.style.top = `${top}px`;
  }, [anchorRef]);

  // Close on outside click (anywhere except the menu itself or the + button)
  // or Escape. The menu is only mounted while open, so listeners die with it.
  useEffect(() => {
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target;
      if (!(target instanceof Node)) return;
      if (menuRef.current?.contains(target)) return;
      if (anchorRef.current?.contains(target)) return;
      onClose();
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("pointerdown", onPointerDown);
    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("pointerdown", onPointerDown);
      window.removeEventListener("keydown", onKeyDown);
    };
  }, [anchorRef, onClose]);

  return (
    <div className="tab-add-menu" ref={menuRef} role="menu" aria-label={t("rightDock.addTab")}>
      {(desktopHost().browser !== undefined ? [...ADDABLE_TABS, BROWSER_TAB] : ADDABLE_TABS).map((item) => (
        <button
          key={item.type}
          type="button"
          role="menuitem"
          className="tab-add-menu__item"
          onClick={() => {
            onPick(item.type, t(item.labelKey as never));
            onClose();
          }}
        >
          <item.icon size={14} />
          <span>{t(item.labelKey as never)}</span>
        </button>
      ))}
    </div>
  );
}
