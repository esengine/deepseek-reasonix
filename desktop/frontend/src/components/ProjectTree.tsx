import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { CSSProperties, DragEvent as ReactDragEvent, KeyboardEvent as ReactKeyboardEvent, MouseEvent as ReactMouseEvent } from "react";
import { Archive, ArrowDown, Pencil, Plus, Folder, FolderPlus, Search, BriefcaseBusiness, Copy, FolderOpen, XCircle, Check, GitMerge, ListCollapse, ListRestart, MessageSquare, Clock, Pin, MoreHorizontal, Minimize2, Maximize2, GitBranch, Sparkles, Cloud } from "lucide-react";
import { asArray } from "../lib/array";
import { useToast } from "../lib/toast";
import { app } from "../lib/bridge";
import { onProjectTreeChangedV2 } from "../lib/sessionCatalogBridge";
import { sessionCatalogNotice } from "../lib/sessionCatalogPresentation";
import { isRuntimeSessionNode, isTopicNode, loadWorkbenchOrganizeMode, loadWorkbenchSortMode, mergeIncompleteProjectTopicPage, mergeProjectTopicPage, projectTreeDedupedExactTime, projectTreeEventAffectsFolder, projectTreeFolderDisclosure, projectTreeReadActivityKey, projectTreeRevisionIsFresh, projectTreeShellChildren, projectTreeShellSignature, projectTreeShouldApplyShellSnapshot, projectTreeShouldRenderTopicActions, projectTreeShouldSuppressOpenForRename, projectTreeTopicArchiveBlocked, projectTreeTopicHasUnreadActivity, projectTreeTopicHoverCardModel, projectTreeTopicMenuOffersPin, projectTreeTopicMetaLine, projectTreeTopicOpenRequest, projectTreeTopicPageIsFresh, projectTreeTopicPageSignature, projectTreeWithoutTopic, projectTreeWithTopicTitle, topicActivityAt, topicActivityDateLabel, topicActivityLabel, topicIsActive, topicStatus, topicStatusLabel, topicUnknownTimeLabel, WORKBENCH_ORGANIZE_KEY, WORKBENCH_SORT_KEY, type ProjectTreePendingTopicOpen, type ProjectTreeReadActivity, type ProjectTreeTopicHoverCard, type WorkbenchOrganizeMode, type WorkbenchSortMode } from "../lib/projectTreeTopic";
export * from "../lib/projectTreeTopic";
import { arrangeClassicProjectTree, arrangeWorkbenchTree, classicTopicWindow, CLASSIC_TOPIC_PREVIEW_LIMIT, splitPinnedProjectTree, type PinnedTreeSections } from "../lib/projectTreePresentation";
export * from "../lib/projectTreePresentation";
import type { ProjectNode, SessionCatalogStatus } from "../lib/types";
import { topicActivityTime } from "../lib/session";
import { useT, type Translator } from "../lib/i18n";
import { PROJECT_COLOR_OPTIONS, projectColorValue } from "../lib/projectColors";
import { projectTreeSessionArchiveTargetKey, projectTreeTopicArchiveTargetKey, projectTreeWithoutTopics, reloadProjectTreeTopics, useProjectTreeArchiveController, type ProjectTreeRefresh, type ProjectTreeRefreshOptions } from "../lib/projectTreeArchive";
import { topicShortcutLabel, type TopicShortcutEntry } from "../lib/topicShortcuts";
import { ContextMenu, contextMenuPointFromEvent, type ContextMenuItem, type ContextMenuPoint } from "./ContextMenu";
import { Tooltip } from "./Tooltip";
import { WorktreeBadge } from "./WorktreeBadge";
import { useProjectCreation } from "./useProjectCreation";
import { useProjectTreeRuntimeProjection } from "../lib/useProjectTreeRuntimeProjection";
import { useProjectTreeFrontendDiagnostics, type ProjectTreeDiagnosticSnapshot } from "../lib/useProjectTreeFrontendDiagnostics";
import { summarizeProjectTreeSessions } from "../lib/projectTreeDiagnostics";
import { GLOBAL_PROJECT_ORDER_KEY, ProjectTreeFolderActivity, ProjectTreeGroupRows, applyProjectOrder, projectTreeProjectRoots, reorderedProjectRoots, useProjectTreeOrganization, type ProjectDropPosition } from "./ProjectTreeOrganization";
import { ProjectTreeSessionArchiveMenu } from "./ProjectTreeSessionArchiveMenu";
import { ProjectTreeHeaderAddControl, ProjectTreeRemoteAction, projectTreeHeaderAddItems } from "./ProjectTreeAddControls";
import { activeRemoteProjectAncestorKeys, buildRemoteProjectMenuItems, useRemoteRuntimeTree, openRemoteSessionNode, remoteProjectKey, remoteServeBadgeState, renameRemoteProjectTitle, RemoteProjectEmptyState, useRemoteProjectGroups, useRemoteSessionActions } from "./ProjectTreeRemoteGroups";
import type { ProjectTreeProps } from "./ProjectTreeProps";

function projectNodeKey(node: ProjectNode, depth: number): string {
  return node.key || `${node.kind}-${node.root ?? ""}-${node.topicId ?? ""}-${node.sessionPath ?? ""}-${depth}`;
}

type WorkbenchHeaderMenu = "more" | "add" | null;

type CollapseSnapshot = {
  expanded: Set<string>;
  manuallyCollapsed: Set<string>;
};

const READ_ACTIVITY_KEY = "projectTree:readActivity";
const READ_ACTIVITY_BASELINE_KEY = "projectTree:readActivityBaselineAt";

function loadReadActivity(): ProjectTreeReadActivity {
  try {
    const raw = localStorage.getItem(READ_ACTIVITY_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw) as Record<string, unknown>;
    const out: ProjectTreeReadActivity = {};
    for (const [key, value] of Object.entries(parsed)) {
      if (typeof value === "number" && Number.isFinite(value)) out[key] = value;
    }
    return out;
  } catch {
    return {};
  }
}

function saveReadActivity(readActivity: ProjectTreeReadActivity) {
  try {
    localStorage.setItem(READ_ACTIVITY_KEY, JSON.stringify(readActivity));
  } catch {
    /* localStorage unavailable */
  }
}

function loadReadActivityBaselineAt(): number {
  try {
    const parsed = Number(localStorage.getItem(READ_ACTIVITY_BASELINE_KEY));
    if (Number.isFinite(parsed) && parsed > 0) return parsed;
    const now = Date.now();
    localStorage.setItem(READ_ACTIVITY_BASELINE_KEY, String(now));
    return now;
  } catch {
    return Date.now();
  }
}

function collapsibleFolderKeys(nodes: ProjectNode[], depth = 0): string[] {
  const keys: string[] = [];
  for (const node of nodes) {
    if (!node) continue;
    const children = asArray(node.children);
    if ((node.kind === "project" || node.kind === "global_folder") && children.length > 0) {
      keys.push(projectNodeKey(node, depth));
    }
    keys.push(...collapsibleFolderKeys(children, depth + 1));
  }
  return keys;
}

export function activeSessionAncestorKeys(
  nodes: ProjectNode[],
  activeScope?: string,
  activeWorkspaceRoot?: string,
  activeTopicId?: string,
  activeSessionPath?: string,
): string[] {
  const walk = (nodeList: ProjectNode[], ancestors: string[]): string[] | null => {
    for (const node of nodeList) {
      if (!node) continue;
      if (topicIsActive(node, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath)) return ancestors;
      const children = asArray(node.children);
      if (children.length > 0) {
        const next = walk(children, [...ancestors, projectNodeKey(node, ancestors.length)]);
        if (next) return next;
      }
    }
    return null;
  };
  const found = walk(nodes, []);
  if (found) return found;
  // Shell-only snapshots no longer embed topics. Expand the matching project or
  // Global folder by workspace identity so the first lazy page can load.
  const scope = (activeScope ?? "").trim();
  const root = (activeWorkspaceRoot ?? "").trim();
  for (const node of nodes) {
    if (!node) continue;
    if (scope === "global" && node.kind === "global_folder") {
      return [projectNodeKey(node, 0)];
    }
    if (node.kind === "project" && root && (node.root === root || node.root === activeWorkspaceRoot)) {
      return [projectNodeKey(node, 0)];
    }
    if (!scope && !root && activeTopicId && (node.kind === "project" || node.kind === "global_folder")) {
      // Active topic without resolved scope still needs a folder open path.
      return [projectNodeKey(node, 0)];
    }
  }
  return [];
}

export function defaultExpandedProjectTreeKeys(
  nodes: ProjectNode[],
  activeScope?: string,
  activeWorkspaceRoot?: string,
  activeTopicId?: string,
  activeSessionPath?: string,
): string[] {
  return activeSessionAncestorKeys(nodes, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath);
}

// Global rows use the same project tree recipe; the fallback supplies their non-workspace accent.
function projectAccentStyle(color?: string, fallbackValue?: string): CSSProperties | undefined {
  const value = projectColorValue(color) || fallbackValue;
  if (!value) return undefined;
  return { "--project-accent": value } as CSSProperties;
}

function colorMenuLabel(label: string, color?: string, active = false) {
  const value = projectColorValue(color);
  return (
    <span className="project-tree__color-option">
      <span
        className="project-tree__color-swatch"
        style={value ? ({ "--project-accent": value } as CSSProperties) : undefined}
        aria-hidden="true"
      />
      <span>{label}</span>
      {active && <Check className="project-tree__color-check" size={12} />}
    </span>
  );
}

function menuLabelWithCheck(label: string, checked: boolean) {
  return (
    <span className="context-menu__label-with-check">
      <span className="context-menu__label-text">{label}</span>
      {checked && <Check className="context-menu__check" size={13} aria-hidden="true" />}
    </span>
  );
}

function revealLabelKey(platform: string): "projectTree.revealInFinder" | "projectTree.revealInExplorer" | "projectTree.revealInFileManager" {
  if (platform === "darwin") return "projectTree.revealInFinder";
  if (platform === "windows") return "projectTree.revealInExplorer";
  return "projectTree.revealInFileManager";
}

function projectColorLabel(t: Translator, color?: string): string {
  switch (color) {
    case "red": return t("projectTree.colorRed");
    case "orange": return t("projectTree.colorOrange");
    case "amber": return t("projectTree.colorAmber");
    case "green": return t("projectTree.colorGreen");
    case "teal": return t("projectTree.colorTeal");
    case "blue": return t("projectTree.colorBlue");
    case "purple": return t("projectTree.colorPurple");
    case "pink": return t("projectTree.colorPink");
    default: return t("projectTree.colorDefault");
  }
}

export function ProjectTree({
  activeScope,
  activeWorkspaceRoot,
  activeTopicId,
  activeSessionPath,
  activeRemote,
  imTopicSources = {},
  variant = "classic",
  onOpenTopic,
  onAddProject,
  onCreateTopic,
  onCreateIsolatedWorktree,
  onRenameTopic,
  onTopicsChanged,
  refreshSignal,
  timeFilter,
  onTimeFilterChange,
  searchExpanded = true,
  searchFocusSignal = 0,
  showShortcutBadges = false,
  shortcutPlatform,
  onVisibleTopicsChange,
}: ProjectTreeProps) {
  const t = useT();
  const { showToast } = useToast();
  const compactTopics = variant === "workbench";
  const creationTopics = variant === "creation";
  const [tree, setTree] = useState<ProjectNode[]>([]);
  const treeRef = useRef<ProjectNode[]>([]);
  const latestRevisionRef = useRef(0);
  const [organizationRevision, setOrganizationRevision] = useState(0);
  const topicRevisionRef = useRef<Record<string, number>>({});
  const topicCompletePageRef = useRef<Record<string, { signature: string; revision: number }>>({});
  const [catalogStatus, setCatalogStatus] = useState<SessionCatalogStatus>({
    state: "opening", revision: 0, indexed: 0, total: 0, repairPending: 0,
    repairActive: 0, repairDeferred: 0, repairBlocked: 0,
  });
  const catalogStatusGenerationRef = useRef(0), rebuildingCatalogRef = useRef(false), catalogRebuildFailedRef = useRef(false);
  const [topicPageState, setTopicPageState] = useState<Record<string, { nextCursor?: string; loading: boolean }>>({});
  const topicPageStateRef = useRef(topicPageState);
  const updateTopicPageState = useCallback((key: string, next: { nextCursor?: string; loading: boolean }) => {
    setTopicPageState((current) => {
      const updated = { ...current, [key]: next };
      topicPageStateRef.current = updated;
      return updated;
    });
  }, []);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [manuallyCollapsed, setManuallyCollapsed] = useState<Set<string>>(new Set());
  const [creatingProject, setCreatingProject] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [editingTopic, setEditingTopic] = useState<string | null>(null);
  const [topicDraft, setTopicDraft] = useState("");
  const [menuNodeKey, setMenuNodeKey] = useState<string | null>(null);
  const [menuProject, setMenuProject] = useState<{ key: string; root: string; path: string; scope: "global" | "project"; label: string } | null>(null);
  const [menuPoint, setMenuPoint] = useState<ContextMenuPoint | null>(null);
  const [editingProject, setEditingProject] = useState<{ key: string; root: string } | null>(null);
  const [projectDraft, setProjectDraft] = useState("");
  const [isolatingProject, setIsolatingProject] = useState<string | null>(null);
  const [worktreeAvailability, setWorktreeAvailability] = useState<Record<string, { available: boolean; reason?: string }>>({});
  const [confirmArchiveTarget, setConfirmArchiveTarget] = useState<string | null>(null);
  const [confirmRemoveProject, setConfirmRemoveProject] = useState<string | null>(null);
  const [dragProjectRoot, setDragProjectRoot] = useState<string | null>(null);
  const [dropProject, setDropProject] = useState<{ root: string; position: ProjectDropPosition } | null>(null);
  const [collapseSnapshot, setCollapseSnapshot] = useState<CollapseSnapshot | null>(null);
  const [platform, setPlatform] = useState("");
  const [workbenchHeaderMenu, setWorkbenchHeaderMenu] = useState<WorkbenchHeaderMenu>(null);
  const [workbenchOrganizeMode, setWorkbenchOrganizeMode] = useState<WorkbenchOrganizeMode>(loadWorkbenchOrganizeMode);
  const [workbenchSortMode, setWorkbenchSortMode] = useState<WorkbenchSortMode>(loadWorkbenchSortMode);
  const workbenchSortModeRef = useRef(workbenchSortMode);
  const [readActivity, setReadActivity] = useState<ProjectTreeReadActivity>(loadReadActivity);
  const [readBaselineAt] = useState(loadReadActivityBaselineAt);
  const filterRef = useRef<HTMLDivElement>(null);
  const filterTriggerRef = useRef<HTMLButtonElement>(null);
  const searchInputRef = useRef<HTMLInputElement>(null);
  const topicIndexRef = useRef(0);
  const visibleTopicsCollectorRef = useRef<TopicShortcutEntry[]>([]);
  const [filterMenuOpen, setFilterMenuOpen] = useState(false);
  const [showAllTopics, setShowAllTopics] = useState<Set<string>>(new Set());
  const [hoverCard, setHoverCard] = useState<{ key: string; card: ProjectTreeTopicHoverCard; left: number; top: number } | null>(null);
  const [aiRenamingTopic, setAiRenamingTopic] = useState<string | null>(null);
  const hoverCardTimerRef = useRef<number | null>(null);
  const creatingRef = useRef(false);
  const closeMenu = useCallback(() => {
    setMenuNodeKey(null);
    setMenuProject(null);
    setMenuPoint(null);
    setConfirmArchiveTarget(null);
    setConfirmRemoveProject(null);
    setWorkbenchHeaderMenu(null);
  }, []);
  const topicLoadSeqRef = useRef<Record<string, number>>({});
  const topicLoadErrorRef = useRef<Record<string, string>>({});
  const refreshRef = useRef<ProjectTreeRefresh>(async () => {});
  const { trashingTopics, trashingSessions, currentArchiveTombstones, trashTopic, trashSession } = useProjectTreeArchiveController({
    treeRef, topicLoadSeqRef, topicPageStateRef, updateTopicPageState, refreshRef,
    optimisticallyRemoveTopic: (topicId) => setTree((current) => projectTreeWithoutTopic(current, topicId)),
    closeMenu, onTopicsChanged, showToast,
  });
  const applyRuntimeProjection = useProjectTreeRuntimeProjection(setTree, currentArchiveTombstones);
  const clickTimerRef = useRef<ProjectTreePendingTopicOpen | null>(null);
  useEffect(() => {
    return () => {
      if (clickTimerRef.current !== null) clearTimeout(clickTimerRef.current.timer);
      if (hoverCardTimerRef.current !== null) window.clearTimeout(hoverCardTimerRef.current);
    };
  }, []);
  const manuallyCollapsedRef = useRef(manuallyCollapsed);

  const cancelHoverCard = useCallback(() => {
    if (hoverCardTimerRef.current !== null) {
      window.clearTimeout(hoverCardTimerRef.current);
      hoverCardTimerRef.current = null;
    }
    setHoverCard((current) => (current === null ? current : null));
  }, []);

  const toggleShowAllTopics = useCallback((key: string) => {
    setShowAllTopics((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }, []);

  const updateManuallyCollapsed = useCallback((updater: (prev: Set<string>) => Set<string>) => {
    setManuallyCollapsed((prev) => {
      const next = updater(prev);
      manuallyCollapsedRef.current = next;
      return next;
    });
  }, []);

  const loadProjectTopicsRef = useRef<(project: ProjectNode, append?: boolean) => Promise<void>>(async () => {});

  const loadProjectTopics = useCallback(async (project: ProjectNode, append = false) => {
    if ((project.kind !== "project" && project.kind !== "global_folder") || project.remote) return;
    const key = project.key;
    const pageState = topicPageStateRef.current[key];
    const cursor = append ? pageState?.nextCursor ?? "" : "";
    if (append && !cursor) return;
    const sortMode = creationTopics ? "updated" : workbenchSortModeRef.current;
    const limit = timeFilter === "10" ? 10 : timeFilter === "20" ? 20 : 50;
    const requestSignature = projectTreeTopicPageSignature(query, timeFilter, sortMode, limit);
    // Last-query-wins: stale completions cannot overwrite a newer first page.
    const seq = (topicLoadSeqRef.current[key] ?? 0) + 1;
    topicLoadSeqRef.current[key] = seq;
    updateTopicPageState(key, { ...pageState, loading: true });
    try {
      const page = await app.ListProjectTopics({
        scope: project.kind === "global_folder" ? "global" : "project",
        workspaceRoot: project.kind === "global_folder" ? "" : project.root ?? "",
        cursor,
        limit,
        query: query.trim(),
        timeFilter: timeFilter === "10" || timeFilter === "20" || timeFilter === "all" ? "" : timeFilter,
        sortMode,
      });
      if (topicLoadSeqRef.current[key] !== seq) return;
      delete topicLoadErrorRef.current[key];
      if (!projectTreeTopicPageIsFresh(topicRevisionRef.current, key, page.revision)) {
        updateTopicPageState(key, { ...topicPageStateRef.current[key], loading: false });
        return;
      }
      topicRevisionRef.current[key] = Math.max(topicRevisionRef.current[key] ?? 0, page.revision);
      const items = projectTreeWithoutTopics(asArray(page.items), currentArchiveTombstones());
      const completeBaseline = topicCompletePageRef.current[key];
      const preserveCompletePage = page.complete === false && completeBaseline?.signature === requestSignature;
      if (page.complete !== false) {
        topicCompletePageRef.current[key] = { signature: requestSignature, revision: page.revision };
      }
      setTree((current) => applyRuntimeProjection(current.map((node) => {
        if (node.key !== key) return node;
        const children = preserveCompletePage
          ? mergeIncompleteProjectTopicPage(asArray(node.children), items)
          : mergeProjectTopicPage(asArray(node.children), items, append);
        return children === node.children ? node : { ...node, children };
      })));
      updateTopicPageState(key, preserveCompletePage
        ? { ...topicPageStateRef.current[key], loading: false }
        : { nextCursor: page.nextCursor, loading: false });
    } catch (error) {
      if (topicLoadSeqRef.current[key] !== seq) return;
      updateTopicPageState(key, { ...topicPageStateRef.current[key], loading: false });
      const message = error instanceof Error ? error.message : String(error);
      if (topicLoadErrorRef.current[key] !== message) {
        topicLoadErrorRef.current[key] = message;
        showToast(message, "error", { durationMs: 6000 });
      }
    }
  }, [applyRuntimeProjection, creationTopics, currentArchiveTombstones, query, showToast, timeFilter, updateTopicPageState]);
  loadProjectTopicsRef.current = loadProjectTopics;

  const selectWorkbenchSortMode = useCallback((sortMode: WorkbenchSortMode) => {
    if (workbenchSortModeRef.current === sortMode) {
      closeMenu();
      setFilterMenuOpen(false);
      return;
    }
    // Invalidate before scheduling React state so an already-resolved older
    // request cannot write back during the render/effect gap. Its pagination
    // cursor belongs to the old order and must not be reused by the new query.
    workbenchSortModeRef.current = sortMode;
    for (const key in topicLoadSeqRef.current) topicLoadSeqRef.current[key] += 1;
    topicPageStateRef.current = {};
    setTopicPageState({});
    setWorkbenchSortMode(sortMode);
    closeMenu();
    setFilterMenuOpen(false);

    const filtering = query.trim() !== "" || timeFilter !== "all";
    for (const project of treeRef.current) {
      const key = projectNodeKey(project, 0);
      if (filtering || expanded.has(key)) void loadProjectTopics(project);
    }
  }, [closeMenu, expanded, loadProjectTopics, query, timeFilter]);
  // Snapshot carries project shells plus lightweight pinned topic shells.
  // Preserve already loaded pages by project key while reconciling pins, so a
  // metadata refresh does not collapse or blank the sidebar.
  const refresh = useCallback(async (options?: ProjectTreeRefreshOptions) => {
    const reloadRequestedProjects = (projects: ProjectNode[]) => reloadProjectTreeTopics(projects, options, loadProjectTopicsRef.current), catalogStatusGeneration = catalogStatusGenerationRef.current;
    try {
      const snapshot = await app.GetProjectTreeSnapshot();
      const rev = snapshot.revision ?? 0, empty = treeRef.current.length === 0;
      if (!projectTreeShouldApplyShellSnapshot({ currentRevision: latestRevisionRef.current, incomingRevision: rev, treeEmpty: empty })) {
        await reloadRequestedProjects(treeRef.current);
        return;
      }
      if (projectTreeRevisionIsFresh(latestRevisionRef.current, rev)) latestRevisionRef.current = Math.max(latestRevisionRef.current, rev);
      const projects = asArray(snapshot.projects);
      if (!catalogRebuildFailedRef.current && catalogStatusGeneration === catalogStatusGenerationRef.current) setCatalogStatus(snapshot.catalog);
      setTree((current) => applyRuntimeProjection(projects.map((project) => {
        const previous = current.find((node) => node.key === project.key);
        // Topic pages reload asynchronously. Keep the last painted children
        // until their replacement arrives so a mutation cannot blank every
        // expanded folder for the duration of a catalog scan.
        return { ...project, children: projectTreeShellChildren(previous?.children, project.children) };
      })));
      await reloadRequestedProjects(projects);
    } catch {
      // A shell snapshot is metadata-only. If it fails, the resident folder
      // identity can still drive the requested canonical topic reload.
      await reloadRequestedProjects(treeRef.current);
    }
  }, [applyRuntimeProjection]);
  refreshRef.current = refresh;
  const { openRemoteProject, openRemoteWindow, remoteSessions, setRemoteSessions, remoteServers, remoteGroupBusy, remoteGroupError, ensureRemoteGroupSessions, refreshRemoteSessions } = useRemoteProjectGroups(tree, showToast, expanded, query);
  const treeWithRemoteSessions = useRemoteRuntimeTree(tree, remoteSessions, t);
  const remoteSessionActions = useRemoteSessionActions(remoteSessions, refreshRemoteSessions, (error) => showToast(error instanceof Error ? error.message : String(error), "error"));
  const { addingProject, handleAddProject, openBlankProjectFlow, blankProjectFlow, openRemoteConnectFlow, remoteConnectFlow } = useProjectCreation({
    onAddProject,
    onRefresh: refresh,
    showToast,
  });

  const rebuildSessionCatalog = useCallback(async () => {
    if (rebuildingCatalogRef.current || catalogStatus.canRebuild !== true) return;
    rebuildingCatalogRef.current = true; catalogRebuildFailedRef.current = false; catalogStatusGenerationRef.current += 1;
    setCatalogStatus({ ...catalogStatus, state: "rebuilding", canRebuild: false });
    try {
      await app.RebuildSessionCatalog(); catalogStatusGenerationRef.current += 1;
      await refresh();
    } catch {
      catalogRebuildFailedRef.current = true; catalogStatusGenerationRef.current += 1; setCatalogStatus(catalogStatus);
    } finally {
      rebuildingCatalogRef.current = false;
    }
  }, [catalogStatus, refresh]);

  useEffect(() => {
    treeRef.current = tree;
  }, [tree]);

  useEffect(() => {
    manuallyCollapsedRef.current = manuallyCollapsed;
  }, [manuallyCollapsed]);

  const searchVisible = searchExpanded || query.trim().length > 0;

  useEffect(() => {
    if (!searchVisible || searchFocusSignal <= 0) return;
    searchInputRef.current?.focus();
  }, [searchFocusSignal, searchVisible]);

  useEffect(() => {
    void refresh();
  }, [refresh, refreshSignal]);

  useEffect(() => onProjectTreeChangedV2((event) => {
    // A stale or missed revision means the tree may have drifted from the
    // catalog; refetch the full snapshot instead of dropping the event.
    if (!projectTreeRevisionIsFresh(latestRevisionRef.current, event.revision)) {
      void refresh();
      return;
    }
    latestRevisionRef.current = Math.max(latestRevisionRef.current, event.revision);
    if (event.reason === "metadata") setOrganizationRevision((current) => Math.max(current, event.revision));
    const catalogStatusGeneration = catalogStatusGenerationRef.current;
    void app.GetSessionCatalogStatus().then((status) => { if (!catalogRebuildFailedRef.current && catalogStatusGeneration === catalogStatusGenerationRef.current) setCatalogStatus(status); }).catch(() => {});
    if (treeRef.current.length === 0) { void refresh(); return; } // race: event before shell
    const affected = asArray(event.roots);
    for (const project of treeRef.current) {
      const key = projectNodeKey(project, 0);
      if (!expanded.has(key)) continue;
      if (projectTreeEventAffectsFolder(project, affected)) void loadProjectTopics(project);
    }
  }), [expanded, loadProjectTopics, refresh]);
  // Debounce query/timeFilter reloads so typing does not stampede the catalog.
  // Dependency is the project-shell signature, not tree: topic page loads
  // rewrite children and would otherwise re-arm this effect in a loop.
  const projectShellSignature = useMemo(() => projectTreeShellSignature(tree), [tree]);
  useEffect(() => {
    const filtering = query.trim() !== "" || timeFilter !== "all";
    const timer = setTimeout(() => {
      for (const project of treeRef.current) {
        const key = projectNodeKey(project, 0);
        if (filtering || expanded.has(key)) void loadProjectTopics(project);
      }
    }, 200);
    return () => clearTimeout(timer);
  }, [expanded, loadProjectTopics, projectShellSignature, query, timeFilter]);

  // Following the active topic is a view concern over the tree already held.
  useEffect(() => {
    const collapsed = manuallyCollapsedRef.current;
    const keys = (activeRemote
      ? activeRemoteProjectAncestorKeys(treeWithRemoteSessions, activeRemote, projectNodeKey)
      : defaultExpandedProjectTreeKeys(treeWithRemoteSessions, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath))
      .filter((key) => !collapsed.has(key));
    // Returning prev unchanged keeps a switch that expands nothing new from
    // re-rendering the tree at all.
    setExpanded((prev) => (keys.every((key) => prev.has(key)) ? prev : new Set([...prev, ...keys])));
    // Active remote groups get the same explicit cold start as a click.
    for (const node of treeWithRemoteSessions) {
      if (!node.remote) continue;
      if (!keys.includes(projectNodeKey(node, 0))) continue;
      if (!remoteSessions[remoteProjectKey(node.remote)]?.length) void ensureRemoteGroupSessions(node.remote.hostId, node.remote.workspace);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tree, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath, activeRemote]);

  const markNodeRead = useCallback((node: ProjectNode) => {
    const key = projectTreeReadActivityKey(node);
    const activityAt = topicActivityAt(node);
    if (!key || activityAt <= 0) return;
    setReadActivity((prev) => {
      const readAt = Math.max(activityAt, Date.now());
      if ((prev[key] ?? 0) >= readAt) return prev;
      const next = { ...prev, [key]: readAt };
      saveReadActivity(next);
      return next;
    });
  }, []);

  useEffect(() => {
    const markActive = (nodes: ProjectNode[]) => {
      for (const node of nodes) {
        if (topicIsActive(node, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath)) markNodeRead(node);
        markActive(asArray(node.children));
      }
    };
    markActive(treeWithRemoteSessions);
  }, [activeScope, activeSessionPath, activeTopicId, activeWorkspaceRoot, markNodeRead, treeWithRemoteSessions]);

  useEffect(() => {
    try {
      localStorage.setItem(WORKBENCH_ORGANIZE_KEY, workbenchOrganizeMode);
    } catch {
      /* ignore */
    }
  }, [workbenchOrganizeMode]);

  useEffect(() => {
    try {
      localStorage.setItem(WORKBENCH_SORT_KEY, workbenchSortMode);
    } catch {
      /* ignore */
    }
  }, [workbenchSortMode]);

  useEffect(() => {
    let cancelled = false;
    void app.Platform().then((value) => {
      if (!cancelled) setPlatform(value);
    }).catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  // Close the time-filter menu on outside click or Escape; move focus into the
  // menu on open and back to the trigger on Escape so it is keyboard-operable.
  useEffect(() => {
    if (!filterMenuOpen) return;
    const onMouseDown = (e: MouseEvent) => {
      if (filterRef.current && !filterRef.current.contains(e.target as Node)) setFilterMenuOpen(false);
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setFilterMenuOpen(false);
        filterTriggerRef.current?.focus();
      }
    };
    document.addEventListener("mousedown", onMouseDown);
    document.addEventListener("keydown", onKeyDown);
    const menu = filterRef.current?.querySelector<HTMLElement>(".project-tree__time-filter-menu");
    (menu?.querySelector<HTMLButtonElement>(".project-tree__time-filter-opt--on") ??
      menu?.querySelector<HTMLButtonElement>('[role="menuitem"]'))?.focus();
    return () => {
      document.removeEventListener("mousedown", onMouseDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [filterMenuOpen]);

  const moveMenuFocus = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp" && e.key !== "Home" && e.key !== "End") return;
    e.preventDefault();
    const items = Array.from(e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="menuitem"]'));
    if (items.length === 0) return;
    const current = items.indexOf(document.activeElement as HTMLButtonElement);
    const next = e.key === "Home" ? 0
      : e.key === "End" ? items.length - 1
      : e.key === "ArrowDown" ? (current + 1 + items.length) % items.length
      : (current - 1 + items.length) % items.length;
    items[next]?.focus();
  };

  const toggleExpand = (key: string, project?: ProjectNode) => {
    const willCollapse = expanded.has(key);
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
    updateManuallyCollapsed((prev) => {
      const next = new Set(prev);
      if (willCollapse) next.add(key);
      else next.delete(key);
      return next;
    });
    if (!willCollapse && project) void loadProjectTopics(project);
  };

  const folderKeys = useMemo(() => collapsibleFolderKeys(tree), [tree]);
  const searchActive = query.trim().length > 0;
  const hasExpandedFolders = !searchActive && folderKeys.some((key) => expanded.has(key));
  const canRestoreCollapsedView = collapseSnapshot !== null;
  const canToggleCollapsedView = !searchActive && folderKeys.length > 0 && (hasExpandedFolders || canRestoreCollapsedView);
  const collapseToggleLabel = t(canRestoreCollapsedView ? "projectTree.restoreCollapsedTooltip" : "projectTree.collapseAllTooltip");
  const workbenchCollapseToggleLabel = t(canRestoreCollapsedView ? "projectTree.restoreCollapsedWorkbench" : "projectTree.collapseAllWorkbench");

  const toggleCollapsedView = useCallback(() => {
    if (searchActive || folderKeys.length === 0) return;
    if (collapseSnapshot) {
      const currentFolderKeys = new Set(folderKeys);
      setExpanded(() => {
        const next = new Set<string>();
        for (const key of collapseSnapshot.expanded) {
          if (currentFolderKeys.has(key)) next.add(key);
        }
        return next;
      });
      updateManuallyCollapsed(() => {
        const next = new Set<string>();
        for (const key of collapseSnapshot.manuallyCollapsed) {
          if (currentFolderKeys.has(key)) next.add(key);
        }
        return next;
      });
      setCollapseSnapshot(null);
      return;
    }
    if (!hasExpandedFolders) return;
    setCollapseSnapshot({
      expanded: new Set(expanded),
      manuallyCollapsed: new Set(manuallyCollapsed),
    });
    setExpanded((prev) => {
      let changed = false;
      const next = new Set(prev);
      for (const key of folderKeys) {
        if (next.delete(key)) changed = true;
      }
      return changed ? next : prev;
    });
    updateManuallyCollapsed((prev) => {
      let changed = false;
      const next = new Set(prev);
      for (const key of folderKeys) {
        if (!next.has(key)) {
          next.add(key);
          changed = true;
        }
      }
      return changed ? next : prev;
    });
  }, [collapseSnapshot, expanded, folderKeys, hasExpandedFolders, manuallyCollapsed, searchActive, updateManuallyCollapsed]);

  const openWorkbenchHeaderMenu = (
    event: ReactMouseEvent<HTMLElement> | ReactKeyboardEvent<HTMLElement>,
    menu: Exclude<WorkbenchHeaderMenu, null>,
  ) => {
    event.preventDefault();
    event.stopPropagation();
    setMenuNodeKey(null);
    setMenuProject(null);
    setConfirmArchiveTarget(null);
    setConfirmRemoveProject(null);
    setFilterMenuOpen(false);
    setMenuPoint(contextMenuPointFromEvent(event));
    setWorkbenchHeaderMenu((value) => (value === menu ? null : menu));
  };
  const handleCreateTopic = async (scope: string, workspaceRoot: string, key: string) => {
    if (creatingRef.current) return;
    creatingRef.current = true;
    setCreatingProject(key);
    setMenuProject(null);
    setMenuPoint(null);
    setExpanded((prev) => {
      const next = new Set(prev);
      next.add(key);
      return next;
    });
    updateManuallyCollapsed((prev) => {
      if (!prev.has(key)) return prev;
      const next = new Set(prev);
      next.delete(key);
      return next;
    });
    try {
      if (onCreateTopic) {
        await onCreateTopic(scope, workspaceRoot);
        await refresh();
        await onTopicsChanged?.();
        return;
      }
      const targetRoot = scope === "project" ? workspaceRoot : "";
      const topic = await app.CreateTopic(scope, targetRoot, "");
      await refresh();
      await onTopicsChanged?.();
      await onOpenTopic(scope, targetRoot, topic.id);
    } catch {
      /* ignore */
    } finally {
      creatingRef.current = false;
      setCreatingProject(null);
    }
  };

  const handleCreateIsolatedWorktree = async (workspaceRoot: string) => {
    if (!workspaceRoot || isolatingProject) return;
    setIsolatingProject(workspaceRoot);
    closeMenu();
    try {
      await onCreateIsolatedWorktree?.(workspaceRoot);
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err), "error", { durationMs: 6000 });
    } finally {
      setIsolatingProject(null);
    }
  };
  const trashTopicAny = (topicId: string) => remoteSessionActions.remove(topicId, () => trashTopic(topicId));
  const startRenameTopic = (node: ProjectNode, label: string) => {
    setMenuNodeKey(null);
    setMenuProject(null);
    setMenuPoint(null);
    setConfirmArchiveTarget(null);
    setEditingTopic(node.topicId ?? null);
    setTopicDraft(label);
  };

  const startRenameProject = (key: string, root: string, label: string) => {
    setMenuProject(null);
    setMenuNodeKey(null);
    setMenuPoint(null);
    setConfirmRemoveProject(null);
    setEditingProject({ key, root });
    setProjectDraft(label);
  };

  const commitRenameTopic = async (topicId: string) => {
    const title = topicDraft.trim();
    setEditingTopic(null);
    if (!title) return;
    try {
      if (await remoteSessionActions.mutate(topicId, (remote) => app.RenameRemoteProjectSession(remote.hostId, remote.workspace, remote.name, title))) return;
      if (onRenameTopic) await onRenameTopic(topicId, title);
      else await app.RenameTopic(topicId, title);
      // Paint the new label immediately; the catalog event round-trip can lag.
      setTree((current) => applyRuntimeProjection(projectTreeWithTopicTitle(current, topicId, title)));
      await refresh();
      if (!onRenameTopic) await onTopicsChanged?.();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err), "error");
    }
  };

  const aiRenameSession = async (topicId: string) => {
    setAiRenamingTopic(topicId);
    try {
      const title = await app.AIRenameSession(topicId);
      await refresh();
      await onTopicsChanged?.();
      if (title) showToast(t("projectTree.aiRenameDone", { title }));
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err), "error");
    } finally {
      setAiRenamingTopic(null);
    }
  };

  const commitRenameProject = async (root: string) => {
    const title = projectDraft.trim();
    setEditingProject(null);
    if (!title) return;
    try {
      if (!await renameRemoteProjectTitle(root, title)) await app.RenameProject(root, title);
      await refresh();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err), "error");
    }
  };

  const setTopicPinned = async (topicId: string, pinned: boolean) => {
    setMenuNodeKey(null);
    setMenuPoint(null);
    try {
      if (await remoteSessionActions.mutate(topicId, (remote) => app.SetRemoteSessionPinned(remote.hostId, remote.workspace, remote.name, pinned))) return;
      await app.SetTopicPinned(topicId, pinned);
      await refresh();
      await onTopicsChanged?.();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err), "error");
    }
  };

  const setProjectPinned = async (workspaceRoot: string, pinned: boolean) => {
    if (!workspaceRoot) return;
    try {
      await app.SetProjectPinned(workspaceRoot, pinned);
      setMenuProject(null);
      setMenuPoint(null);
      await refresh();
      await onTopicsChanged?.();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err), "error");
    }
  };

  const copyProjectPath = async (path: string) => {
    if (!path) return;
    try {
      await navigator.clipboard?.writeText(path);
    } catch {
      /* ignore */
    }
  };

  const removeProject = async (path: string) => {
    if (!path) return;
    try {
      await app.RemoveWorkspace(path);
      setMenuProject(null);
      setMenuPoint(null);
      setConfirmRemoveProject(null);
      await refresh();
    } catch (err) {
      showToast(err instanceof Error ? err.message : String(err), "error");
    }
  };

  const setProjectColor = async (path: string, color: string) => {
    try {
      await app.SetProjectColor(path, color);
      setMenuProject(null);
      setMenuPoint(null);
      await refresh();
      await onTopicsChanged?.();
    } catch {
      /* ignore */
    }
  };

  const visibleTree = useMemo(() => {
    const q = query.trim().toLowerCase();
    // Time filter: compute cutoff timestamp.
    const diff = timeFilter === "1h" ? 60 * 60 * 1000
      : timeFilter === "3h" ? 3 * 60 * 60 * 1000
      : timeFilter === "5h" ? 5 * 60 * 60 * 1000
      : timeFilter === "1d" ? 24 * 60 * 60 * 1000
      : 0;
    const nthLatestActivity = (n: number): number | null => {
      const times = new Set<number>();
      const collect = (nodes: ProjectNode[]) => {
        for (const node of nodes) {
          if (node.kind === "topic" || node.kind === "global_topic") times.add(topicActivityTime(node));
          collect(asArray(node.children));
        }
      };
      collect(treeWithRemoteSessions);
      const sorted = [...times].sort((a, b) => b - a);
      return sorted.length === 0 ? null : sorted[Math.min(n, sorted.length) - 1];
    };
    const cutoff: number | null = timeFilter === "all" ? null
      : timeFilter === "10" ? nthLatestActivity(10)
      : timeFilter === "20" ? nthLatestActivity(20)
      : Date.now() - diff;
    const topicMatchesTime = (node: ProjectNode) => {
      if (cutoff === null) return true;
      return topicActivityTime(node) >= cutoff;
    };
    const matchesQuery = (node: ProjectNode) =>
      [node.label, node.root, node.topicId].some((value) => (value ?? "").toLowerCase().includes(q));
    const filterNode = (node: ProjectNode): ProjectNode | null => {
      // For folder nodes: always show when time filter is active (so the tree structure remains navigable).
      const isFolder = node.kind === "project" || node.kind === "global_folder";
      const children = asArray(node.children)
        .map(filterNode)
        .filter((child): child is ProjectNode => child !== null);
      if (isFolder) {
        if (cutoff !== null && children.length === 0 && !matchesQuery(node) && q === "") return null;
        if (children.length > 0 || matchesQuery(node)) return { ...node, children };
        if (q) return null;
        // With only time filter, show folder if it has any child that matches the time.
        const hasTimeMatch = asArray(node.children).some((c) => topicMatchesTime(c));
        return hasTimeMatch ? { ...node, children: asArray(node.children).filter(topicMatchesTime) } : null;
      }
      if (!q && cutoff === null) return node;
      if (cutoff !== null && !topicMatchesTime(node)) return null;
      if (q && !matchesQuery(node)) return null;
      return node;
    };
    const filtered = treeWithRemoteSessions
      .map(filterNode)
      .filter((node): node is ProjectNode => node !== null);
    if (compactTopics) return arrangeWorkbenchTree(filtered, workbenchOrganizeMode, workbenchSortMode);
    if (creationTopics) return arrangeWorkbenchTree(filtered, "project", "updated");
    return arrangeClassicProjectTree(filtered, workbenchSortMode);
  }, [compactTopics, creationTopics, query, timeFilter, treeWithRemoteSessions, workbenchOrganizeMode, workbenchSortMode]);

  const pinnedTreeSections = useMemo<PinnedTreeSections>(() => {
    if (creationTopics) return { pinned: [], projects: visibleTree };
    return splitPinnedProjectTree(visibleTree, workbenchSortMode, compactTopics);
  }, [compactTopics, creationTopics, visibleTree, workbenchSortMode]);

  const classicTopics = !compactTopics && !creationTopics;
  const classicTruncationActive = classicTopics && query.trim() === "" && timeFilter === "all";

  const projectLabelByRoot = useMemo(() => {
    const map = new Map<string, string>();
    for (const nodeItem of tree) {
      if (!nodeItem) continue;
      if (nodeItem.kind === "project" && nodeItem.root) map.set(nodeItem.root, nodeItem.label || nodeItem.root);
      if (nodeItem.kind === "global_folder") map.set(GLOBAL_PROJECT_ORDER_KEY, nodeItem.label || "Global");
    }
    return map;
  }, [tree]);

  const scheduleHoverCard = useCallback((element: HTMLElement, rowKey: string, node: ProjectNode) => {
    if (hoverCardTimerRef.current !== null) window.clearTimeout(hoverCardTimerRef.current);
    hoverCardTimerRef.current = window.setTimeout(() => {
      hoverCardTimerRef.current = null;
      if (!element.isConnected) return;
      if (menuNodeKey || menuProject || editingTopic || editingProject || dragProjectRoot) return;
      const rect = element.getBoundingClientRect();
      const globalScope = node.kind === "global_topic" || node.kind === "global_session";
      const projectLabel = globalScope
        ? projectLabelByRoot.get(GLOBAL_PROJECT_ORDER_KEY) ?? "Global"
        : projectLabelByRoot.get(node.root ?? "") ?? "";
      setHoverCard({
        key: rowKey,
        card: projectTreeTopicHoverCardModel(node, t, projectLabel),
        left: rect.right + 10,
        top: Math.max(8, Math.min(rect.top, window.innerHeight - 150)),
      });
    }, 350);
  }, [menuNodeKey, menuProject, editingTopic, editingProject, dragProjectRoot, projectLabelByRoot, t]);

  // Opening an old session from history can land on a topic hidden behind the
  // classic show-more window; reveal that folder so the active row stays visible.
  useEffect(() => {
    if (!classicTruncationActive) return;
    const revealKeys: string[] = [];
    for (const nodeItem of visibleTree) {
      if (!nodeItem || (nodeItem.kind !== "project" && nodeItem.kind !== "global_folder")) continue;
      const children = asArray(nodeItem.children);
      const activeIndex = children.findIndex((child) =>
        topicIsActive(child, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath) ||
        asArray(child.children).some((grand) => topicIsActive(grand, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath)));
      if (activeIndex >= CLASSIC_TOPIC_PREVIEW_LIMIT) revealKeys.push(projectNodeKey(nodeItem, 0));
    }
    if (revealKeys.length === 0) return;
    setShowAllTopics((prev) => {
      if (revealKeys.every((key) => prev.has(key))) return prev;
      const next = new Set(prev);
      for (const key of revealKeys) next.add(key);
      return next;
    });
  }, [classicTruncationActive, visibleTree, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath]);

  const projectDragEnabled = query.trim() === "";

  const commitProjectReorder = useCallback(async (draggedRoot: string, targetRoot: string, position: ProjectDropPosition) => {
    const nextRoots = reorderedProjectRoots(tree, draggedRoot, targetRoot, position);
    const currentRoots = projectTreeProjectRoots(tree);
    if (nextRoots.join("\n") === currentRoots.join("\n")) return;
    setTree((current) => applyProjectOrder(current, nextRoots));
    try {
      await app.ReorderProjects(nextRoots);
      await refresh();
      await onTopicsChanged?.();
    } catch {
      await refresh();
    }
  }, [onTopicsChanged, refresh, tree]);

  const organization = useProjectTreeOrganization({ tree, refresh, onTopicsChanged, organizationRevision });

  const clearProjectDrag = useCallback(() => {
    setDragProjectRoot(null);
    setDropProject(null);
  }, []);

  useEffect(() => {
    if (!dragProjectRoot) return;
    window.addEventListener("dragend", clearProjectDrag);
    window.addEventListener("drop", clearProjectDrag);
    window.addEventListener("blur", clearProjectDrag);
    return () => {
      window.removeEventListener("dragend", clearProjectDrag);
      window.removeEventListener("drop", clearProjectDrag);
      window.removeEventListener("blur", clearProjectDrag);
    };
  }, [clearProjectDrag, dragProjectRoot]);

  const activeAncestorKeys = useMemo(
    () => activeRemote ? activeRemoteProjectAncestorKeys(treeWithRemoteSessions, activeRemote, projectNodeKey) : activeSessionAncestorKeys(treeWithRemoteSessions, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath),
    [activeRemote, activeScope, activeSessionPath, activeTopicId, activeWorkspaceRoot, treeWithRemoteSessions],
  );
  useEffect(() => {
    if (activeAncestorKeys.length === 0) return;
    setExpanded((prev) => {
      let changed = false;
      const next = new Set(prev);
      for (const key of activeAncestorKeys) {
        if (manuallyCollapsed.has(key) || next.has(key)) continue;
        next.add(key);
        changed = true;
      }
      return changed ? next : prev;
    });
  }, [activeAncestorKeys, manuallyCollapsed]);

  const projectTreeDiagnosticSnapshot = useMemo<ProjectTreeDiagnosticSnapshot>(() => {
    const sessionSummary = summarizeProjectTreeSessions({
      tree,
      visibleTree,
      expanded,
      showAllTopics,
      classicTruncationActive,
      queryActive: query.trim().length > 0,
      timeFilterActive: timeFilter !== "all",
      projectNodeKey,
      isActive: (node) => topicIsActive(node, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath),
      isUnread: (node) => projectTreeTopicHasUnreadActivity(node, readActivity, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath, readBaselineAt),
    });
    return {
      ...sessionSummary,
      directoryState: catalogStatus.state,
      scope: activeScope === "global" ? "global" : activeScope ? "project" : "unknown",
      variant,
      timeFilter,
      queryActive: query.trim().length > 0,
      timeFilterActive: timeFilter !== "all",
      catalogPartial: catalogStatus.state !== "ready"
        || (catalogStatus.repairActive ?? 0) > 0
        || (catalogStatus.unindexedTargetCount ?? 0) > 0
        || Boolean(catalogStatus.lastError),
      catalogRebuilding: catalogStatus.state === "rebuilding",
      catalogRevision: catalogStatus.revision,
      catalogIndexed: catalogStatus.indexed,
      catalogTotal: catalogStatus.total,
      unloadedSessions: Math.max(0, catalogStatus.total - sessionSummary.workspaceSessions),
      repairPending: catalogStatus.repairPending,
      treeRevision: latestRevisionRef.current,
      organizationRevision,
    };
  }, [activeScope, activeSessionPath, activeTopicId, activeWorkspaceRoot, catalogStatus, classicTruncationActive, expanded, organizationRevision, query, readActivity, readBaselineAt, showAllTopics, timeFilter, tree, variant, visibleTree]);

  useProjectTreeFrontendDiagnostics(projectTreeDiagnosticSnapshot);

  const renderNode = (node: ProjectNode | null | undefined, depth: number, section: "pinned" | "projects" = "projects", isVisible = true) => {
    if (!node) return null;
    const key = projectNodeKey(node, depth);
    const children = asArray(node.children);
    const isExpanded = query.trim() ? true : expanded.has(key);
    const hasChildren = children.length > 0;
    // Snapshot rows are shells with no children until the first page is loaded,
    // so every project folder must remain expandable while indexing.
    const folderDisclosure = projectTreeFolderDisclosure(hasChildren, isExpanded, true);

    if (isTopicNode(node) || isRuntimeSessionNode(node)) {
      const isSessionNode = isRuntimeSessionNode(node);
      const openRequest = projectTreeTopicOpenRequest(node);
      const scope = openRequest?.scope ?? "project";
      const scopeClass = scope === "global" ? " project-tree__topic--global" : " project-tree__topic--project";
      const accentStyle = projectAccentStyle(node.projectColor, scope === "global" ? "var(--project-tree-global-accent)" : undefined);
      const active = topicIsActive(node, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath);
      const label = (node.label || node.topicId || "Untitled").replace(/^●\s*/, "");
      const activityAt = node.lastActivityAt || node.createdAt || 0;
      // Every variant is a single-line row with the activity time on the right;
      // classic moved there too so turns and the exact date live in the hover
      // preview card and the accessible label instead of a second meta line.
      const sideTimeVisible = true;
      const timeLabel = activityAt ? topicActivityLabel(activityAt, t, true) : topicUnknownTimeLabel(node, t);
      const exactTimeLabel = activityAt ? topicActivityDateLabel(activityAt) : "";
      const metaFull = projectTreeTopicMetaLine(node, t, compactTopics);
      const status = topicStatus(node);
      const statusLabel = topicStatusLabel(node, t);
      const archiveBlocked = projectTreeTopicArchiveBlocked(node);
      const waitingConfirmation = status === "waiting_confirmation";
      // Compact workbench: waiting shows an amber "待确认" pill instead of a
      // spinning orange dot, and that pill replaces the relative time so the
      // paused-for-user state is scannable in the background tab list.
      const showStatusInSide = status === "thinking" || status === "streaming" || status === "waiting_confirmation" || status === "background_job";
      const showWaitingPill = waitingConfirmation || status === "finishing" || status === "cancelling" || status === "unknown";
      const showSideTime = sideTimeVisible && !showWaitingPill;
      const unread = projectTreeTopicHasUnreadActivity(node, readActivity, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath, readBaselineAt);
      const topicId = node.topicId ?? "";
      const topicTrashing = trashingTopics.has(topicId);
      const sessionPath = node.sessionPath?.trim() ?? "";
      const sessionTrashing = Boolean(sessionPath) && trashingSessions.has(sessionPath);
      const archiveTargetKey = isSessionNode ? projectTreeSessionArchiveTargetKey(sessionPath) : projectTreeTopicArchiveTargetKey(scope, node.root ?? "", topicId);
      const imSource = scope === "global" && topicId ? imTopicSources[topicId] : undefined;
      const imSourceLabel = imSource?.label || "";
      const imSourceTitle = imSourceLabel ? t("msg.fromIm", { source: imSourceLabel }) : "";
      const imSourcePlatform = (imSource?.platform || "im").replace(/[^a-z0-9_-]/gi, "").toLowerCase() || "im";
      const recoveryLabel = node.recoveryState === "recovery_only"
        ? t("projectTree.recoveryOnly")
        : node.recovered
          ? t("projectTree.recovered")
          : "";
      const title = [node.preview || "", label, recoveryLabel, imSourceTitle, statusLabel, metaFull, projectTreeDedupedExactTime(metaFull, exactTimeLabel)].filter(Boolean).join(" · ");
      const topicMenuOpen = menuNodeKey === key;
      const pinned = Boolean(node.pinned);
      const pinLabel = t(pinned ? "projectTree.unpinTopic" : "projectTree.pinTopic");
      const openTopicMenu = (event: ReactMouseEvent<HTMLElement> | ReactKeyboardEvent<HTMLElement>) => {
        event.preventDefault();
        event.stopPropagation();
        setMenuProject(null);
        setConfirmRemoveProject(null);
        setMenuPoint(contextMenuPointFromEvent(event));
        setMenuNodeKey(key);
        setConfirmArchiveTarget(null);
      };
      const topicMenuItems: ContextMenuItem[] = [
        ...organization.topicMenuItems(node, t),
        ...(isSessionNode && node.sessionPath && !node.running
          ? [
              {
                key: "consolidate-recovery-copies",
                icon: <GitMerge size={13} />,
                label: t("projectTree.consolidateRecoveryCopies"),
                onSelect: () => {
                  void (async () => {
                    try {
                      const report = await app.ConsolidateTopicRecoveryCopies(scope, openRequest?.workspaceRoot ?? node.root ?? "", topicId);
                      if (report.blockedByDivergence) {
                        showToast(
                          t("projectTree.consolidateBlocked", { winner: report.winnerMessageCount, main: report.mainMessageCount }),
                          "warn",
                          {
                            actionLabel: t("projectTree.consolidateForceAction"),
                            onAction: () => {
                              void (async () => {
                                try {
                                  const forced = await app.ForceConsolidateTopicRecoveryCopies(scope, openRequest?.workspaceRoot ?? node.root ?? "", topicId);
                                  showToast(
                                    t("projectTree.consolidateDone", { messages: forced.mainMessageCount, count: forced.trashed.length }),
                                    "info",
                                  );
                                  await refresh();
                                  await onTopicsChanged?.();
                                } catch (err) {
                                  showToast(err instanceof Error ? err.message : String(err), "error");
                                }
                              })();
                            },
                          },
                        );
                        return;
                      }
                      if (report.promoted) {
                        showToast(
                          t("projectTree.consolidateDone", { messages: report.mainMessageCount, count: report.trashed.length }),
                          "info",
                        );
                      } else if (report.trashed.length > 0) {
                        showToast(t("projectTree.consolidateFolded", { count: report.trashed.length }), "info");
                      } else {
                        showToast(t("projectTree.consolidateNothing"), "info");
                      }
                      await refresh();
                      await onTopicsChanged?.();
                    } catch (err) {
                      showToast(err instanceof Error ? err.message : String(err), "error");
                    }
                  })();
                },
              },
            ]
          : []),
        ...(projectTreeTopicMenuOffersPin(variant)
          ? [
              {
                key: pinned ? "unpin" : "pin",
                icon: <Pin size={13} />,
                label: pinLabel,
                onSelect: () => void setTopicPinned(topicId, !pinned),
              },
            ]
          : []),
        {
          key: "rename",
          icon: <Pencil size={13} />,
          label: t("projectTree.renameTopic"),
          onSelect: () => startRenameTopic(node, label),
        },
        {
          key: "aiRename",
          icon: <Sparkles size={13} />,
          label: aiRenamingTopic === topicId ? t("projectTree.aiRenamingTopic") : t("projectTree.aiRenameTopic"),
          disabled: aiRenamingTopic !== null || Boolean(node.remoteSession),
          onSelect: () => void aiRenameSession(topicId),
        },
        {
          key: "consolidate-recovery-copies",
          icon: <GitMerge size={13} />,
          label: t("projectTree.consolidateRecoveryCopies"),
          disabled: !node.sessionPath || node.running,
          onSelect: () => {
            const sessionPath = node.sessionPath;
            if (!sessionPath) return;
            void (async () => {
              try {
                const report = await app.ConsolidateSessionRecoveryCopies(sessionPath);
                if (report.promoted) {
                  showToast(
                    t("projectTree.consolidateDone", { messages: report.mainMessageCount, count: report.trashed.length }),
                    "info",
                  );
                } else if (report.trashed.length > 0) {
                  showToast(t("projectTree.consolidateFolded", { count: report.trashed.length }), "info");
                } else {
                  showToast(t("projectTree.consolidateNothing"), "info");
                }
                await refresh();
                await onTopicsChanged?.();
              } catch (err) {
                showToast(err instanceof Error ? err.message : String(err), "error");
              }
            })();
          },
        },
        {
          key: "trash",
          icon: <Archive className={topicTrashing ? "project-tree__archive-spinner" : undefined} size={13} />,
          label: confirmArchiveTarget === archiveTargetKey ? t("history.confirmMoveToTrash") : t("history.moveToTrash"),
          disabled: archiveBlocked || topicTrashing,
          danger: true,
          onSelect: () => {
            if (confirmArchiveTarget === archiveTargetKey) void trashTopicAny(topicId);
            else setConfirmArchiveTarget(archiveTargetKey);
          },
        },
      ];
      if (!isSessionNode && editingTopic === topicId) {
        return (
          <div
            key={key}
            className={`project-tree__topic project-tree__topic--editing${active ? " project-tree__topic--active" : ""}${imSource ? " project-tree__topic--im-source" : ""}${!classicTopics && metaFull ? " project-tree__topic--has-meta" : ""}`}
            style={{ paddingLeft: 14 + depth * 16 }}
          >
            <input
              autoFocus
              className="project-tree__topic-input"
              value={topicDraft}
              onChange={(event) => setTopicDraft(event.target.value)}
              onFocus={(event) => event.target.select()}
              onKeyDown={(event) => {
                if (event.key === "Enter") void commitRenameTopic(topicId);
                if (event.key === "Escape") setEditingTopic(null);
              }}
              onBlur={() => void commitRenameTopic(topicId)}
            />
          </div>
        );
      }
      const shortcutIndex = showShortcutBadges && isVisible && topicIndexRef.current < 9 ? topicIndexRef.current + 1 : 0;
      if (shortcutIndex > 0) topicIndexRef.current++;
      // Collect visible topics in render order for shortcut navigation
      if (openRequest && isVisible) {
        visibleTopicsCollectorRef.current.push({
          scope: openRequest.scope,
          workspaceRoot: openRequest.workspaceRoot,
          topicId: openRequest.topicId,
          sessionPath: openRequest.sessionPath,
        });
      }
      const topicDrag = organization.topicRow(node, section === "pinned" || isSessionNode || Boolean(query || menuNodeKey || editingTopic || dragProjectRoot || creatingProject));
      const row = (
        <div
          className={`project-tree__topic${scopeClass}${isSessionNode ? " project-tree__topic--session" : ""}${active ? " project-tree__topic--active" : ""}${node.running ? " project-tree__topic--running" : ""}${status ? ` project-tree__topic--status-${status}` : ""}${unread ? " project-tree__topic--unread" : ""}${!isSessionNode && pinned ? " project-tree__topic--pinned" : ""}${topicMenuOpen ? " project-tree__topic--menu-open" : ""}${topicDrag.className}${sideTimeVisible && (timeLabel || showStatusInSide || showWaitingPill) ? " project-tree__topic--with-side" : metaFull ? " project-tree__topic--has-meta" : ""}${imSource ? " project-tree__topic--im-source" : ""}${shortcutIndex > 0 ? " project-tree__topic--show-shortcut" : ""}`}
          style={accentStyle}
          {...topicDrag.props}
          onContextMenu={openTopicMenu}
          onMouseEnter={classicTopics ? (event) => scheduleHoverCard(event.currentTarget, key, node) : undefined}
          onMouseLeave={classicTopics ? cancelHoverCard : undefined}
          onMouseDown={classicTopics ? cancelHoverCard : undefined}
        >
          <button
            type="button"
            className="project-tree__topic-main"
            title={classicTopics ? undefined : title}
            aria-label={classicTopics ? title : undefined}
            style={{ paddingLeft: 14 + depth * 16 }}
            onClick={() => {
              const remote = node.remoteSession ?? remoteSessionActions.resolve(topicId);
              if (!openRequest && !remote) return;
              const nextClick = { rowKey: key, canRename: !isSessionNode };
              const pending = clickTimerRef.current;
              if (pending !== null) {
                clearTimeout(pending.timer);
                clickTimerRef.current = null;
                if (projectTreeShouldSuppressOpenForRename(pending, nextClick)) return;
              }
              const timer = setTimeout(() => {
                if (clickTimerRef.current?.timer === timer) clickTimerRef.current = null;
                markNodeRead(node);
                if (!openRemoteSessionNode(remote, openRemoteProject) && openRequest) {
                  onOpenTopic(openRequest.scope, openRequest.workspaceRoot, openRequest.topicId, openRequest.sessionPath);
                }
              }, 200);
              clickTimerRef.current = { ...nextClick, timer };
            }}
            onKeyDown={(event) => {
              if (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10")) {
                openTopicMenu(event);
              }
            }}
            onDoubleClick={(event) => {
              if (isSessionNode) return;
              event.stopPropagation();
              if (clickTimerRef.current !== null && clickTimerRef.current.rowKey === key) {
                clearTimeout(clickTimerRef.current.timer);
                clickTimerRef.current = null;
              }
              startRenameTopic(node, label);
            }}
          >
            <span className="project-tree__topic-copy">
              <span className="project-tree__topic-heading">
                <span className="project-tree__topic-label">{label}</span>
                {recoveryLabel && <span className="project-tree__topic-recovery" title={recoveryLabel}>{recoveryLabel}</span>}
                {imSource && (
                  <span
                    className={`project-tree__topic-im project-tree__topic-im--${imSourcePlatform}`}
                    title={imSourceTitle}
                    aria-label={imSourceTitle}
                  >
                    <MessageSquare size={11} />
                    <span>{imSourceLabel}</span>
                  </span>
                )}
                {!compactTopics && statusLabel && (!classicTopics || status === "paused" || status === "awaiting_delivery" || status === "error") && (
                  <span className={`project-tree__topic-status project-tree__topic-status--${status}`}>{statusLabel}</span>
                )}
              </span>
            </span>
            {sideTimeVisible && (
              <span className={`project-tree__topic-side${!timeLabel && !showStatusInSide && !showWaitingPill ? " project-tree__topic-side--empty" : ""}`}>
                {showWaitingPill && statusLabel ? (
                  <span
                    className="project-tree__topic-waiting-pill"
                    title={statusLabel}
                  >
                    {statusLabel}
                  </span>
                ) : (
                  <>
                    {showStatusInSide && (
                      <span
                        className={`project-tree__topic-state project-tree__topic-state--${status}`}
                        title={statusLabel}
                        aria-hidden="true"
                      />
                    )}
                    {showSideTime && timeLabel && (
                      <span className="project-tree__topic-time" aria-hidden="true">{timeLabel}</span>
                    )}
                  </>
                )}
              </span>
            )}
            {compactTopics && statusLabel && !showWaitingPill && (
              <span className="sr-only">
                {statusLabel}
              </span>
            )}
            {compactTopics && metaFull && (
              <span className="sr-only">
                {metaFull}
              </span>
            )}
          </button>
          {unread && <span className="project-tree__topic-unread-dot" aria-hidden="true" />}
          {projectTreeShouldRenderTopicActions(isSessionNode, variant, unread) && !(node.remoteSession && !node.remoteSession.name) && (
            <span
              className="project-tree__topic-actions"
              aria-label={t("projectTree.topicActions")}
              onMouseEnter={classicTopics ? cancelHoverCard : undefined}
              onFocus={classicTopics ? cancelHoverCard : undefined}
            >
              <Tooltip label={pinLabel} side="top" className="project-tree__topic-action-slot">
                <button
                  className={`project-tree__topic-action${pinned ? " project-tree__topic-action--pinned" : ""}`}
                  type="button"
                  aria-label={pinLabel}
                  aria-pressed={pinned}
                  onClick={(event) => {
                    event.preventDefault();
                    event.stopPropagation();
                    void setTopicPinned(topicId, !pinned);
                  }}
                >
                  <Pin size={15} aria-hidden="true" />
                </button>
              </Tooltip>
              <Tooltip label={t("projectTree.archiveTopic")} side="top" className="project-tree__topic-action-slot">
                <button
                  className={`project-tree__topic-action project-tree__topic-action--archive${topicTrashing ? " project-tree__topic-action--busy" : ""}`}
                  type="button"
                  aria-label={t("projectTree.archiveTopic")}
                  aria-busy={topicTrashing} disabled={archiveBlocked || topicTrashing}
                  onClick={(event) => {
                    event.preventDefault();
                    event.stopPropagation();
                    void trashTopicAny(topicId);
                  }}
                >
                  <Archive className={topicTrashing ? "project-tree__archive-spinner" : undefined} size={15} aria-hidden="true" />
                </button>
              </Tooltip>
            </span>
          )}
          {isSessionNode ? (
            <ProjectTreeSessionArchiveMenu
              open={topicMenuOpen} point={menuPoint} sessionPath={sessionPath} blocked={archiveBlocked || topicTrashing} busy={sessionTrashing} confirmed={confirmArchiveTarget === archiveTargetKey}
              onConfirm={() => setConfirmArchiveTarget(archiveTargetKey)} onTrash={() => { setConfirmArchiveTarget(null); void trashSession(sessionPath); }} onClose={closeMenu} />
          ) : <ContextMenu open={topicMenuOpen} point={menuPoint} items={topicMenuItems} minWidth={178} ariaLabel={t("projectTree.topicActions")} onClose={closeMenu} />}
          {shortcutIndex > 0 && (
            <span className="project-tree__topic-shortcut" aria-hidden="true">
              {topicShortcutLabel(shortcutIndex, shortcutPlatform)}
            </span>
          )}
        </div>
      );
      return (
        <div key={key}>
          {row}
          {hasChildren && (
            <div className={`project-tree__children${isExpanded ? " project-tree__children--expanded" : ""}`}>
              <div className="project-tree__children-inner">
                {children.map((child) => renderNode(child, depth + 1, section, isVisible && isExpanded))}
              </div>
            </div>
          )}
        </div>
      );
    }

    const scope = node.kind === "global_folder" ? "global" : "project";
    const scopeClass = scope === "global" ? " project-tree__folder--global" : " project-tree__folder--project";
    const pinnedClass = node.pinned ? " project-tree__folder--pinned" : "";
    const accentStyle = projectAccentStyle(node.projectColor, scope === "global" ? "var(--project-tree-global-accent)" : undefined);
    const projectRoot = scope === "global" ? "" : node.root ?? "";
    const projectDragKey = scope === "global" ? GLOBAL_PROJECT_ORDER_KEY : projectRoot;
    const projectPath = node.root ?? "";
    const colorTargetRoot = scope === "global" ? "" : projectPath;
    const projectLabel = node.label || (scope === "global" ? "Global" : "Untitled");
    const projectPinned = Boolean(node.pinned);
    const projectActive = node.remote ? Boolean(activeRemote && remoteProjectKey(activeRemote) === remoteProjectKey(node.remote)) : activeScope === scope && (scope === "global" || activeWorkspaceRoot === node.root);
    const projectMenuOpen = menuProject?.key === key;
    const activeTopicInProject = Boolean(activeTopicId) && activeScope === scope && (scope === "global" || activeWorkspaceRoot === projectRoot);
    const sourceProjectNode = tree.find((candidate) => scope === "global"
      ? candidate.kind === "global_folder"
      : candidate.kind === "project" && candidate.root === projectRoot);
    const activeTopicArchiveBlocked = asArray(sourceProjectNode?.children).some((candidate) =>
      isTopicNode(candidate) && candidate.topicId === activeTopicId && projectTreeTopicArchiveBlocked(candidate));
    const draggableProject = section !== "pinned" && projectDragEnabled && depth === 0 && Boolean(projectDragKey) && editingProject?.key !== key;
    const projectDropPosition = dropProject?.root === projectDragKey ? dropProject?.position ?? null : null;
    const handleProjectDragStart = (event: ReactDragEvent<HTMLElement>) => {
      if (!draggableProject) return;
      const target = event.target;
      if (target instanceof Element && target.closest(".project-tree__action-slot,.project-tree__folder-action-slot")) {
        event.preventDefault();
        return;
      }
      event.dataTransfer.effectAllowed = "move";
      event.dataTransfer.setData("text/plain", projectDragKey);
      setDragProjectRoot(projectDragKey);
      setDropProject(null);
    };
    const handleProjectDragOver = (event: ReactDragEvent<HTMLDivElement>) => {
      if (!draggableProject || !dragProjectRoot || dragProjectRoot === projectDragKey) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = "move";
      const rect = event.currentTarget.getBoundingClientRect();
      const position: ProjectDropPosition = event.clientY < rect.top + rect.height / 2 ? "before" : "after";
      setDropProject((current) => {
        if (current?.root === projectDragKey && current?.position === position) return current;
        return { root: projectDragKey, position };
      });
    };
    const handleProjectDrop = (event: ReactDragEvent<HTMLDivElement>) => {
      if (!draggableProject) return;
      const draggedRoot = dragProjectRoot || event.dataTransfer.getData("text/plain");
      const position = dropProject?.root === projectDragKey ? dropProject?.position ?? "after" : "after";
      event.preventDefault();
      clearProjectDrag();
      if (draggedRoot && draggedRoot !== projectDragKey) void commitProjectReorder(draggedRoot, projectDragKey, position);
    };
    const openProjectMenu = (event: ReactMouseEvent<HTMLElement> | ReactKeyboardEvent<HTMLElement>) => {
      event.preventDefault();
      event.stopPropagation();
      setMenuNodeKey(null);
      setConfirmArchiveTarget(null);
      setMenuPoint(contextMenuPointFromEvent(event));
      setMenuProject({ key, root: projectRoot, path: projectPath, scope, label: projectLabel });
      setConfirmRemoveProject(null);
      if (scope === "project" && projectRoot) {
        void app.IsolatedWorktreeAvailability(projectRoot).then((availability) => {
          setWorktreeAvailability((current) => ({
            ...current,
            [projectRoot]: { available: availability.available, reason: availability.reason },
          }));
        }).catch(() => {});
      }
    };
    const isolationAvailability = worktreeAvailability[projectRoot];
    const activeTopicArchiveTarget = activeTopicId ? projectTreeTopicArchiveTargetKey(scope, projectRoot, activeTopicId) : "";
    const isolatedWorkspaceItems: ContextMenuItem[] = scope === "project"
      ? [{
          key: "isolated-delivery-workspace",
          icon: <GitBranch size={13} />,
          label: (
            <span title={isolationAvailability?.reason || t("projectTree.createWorktreeHint")}>
              {isolatingProject === projectRoot ? t("projectTree.creatingWorktree") : t("projectTree.createWorktree")}
            </span>
          ),
          disabled: isolatingProject !== null || isolationAvailability?.available === false,
          onSelect: () => { void handleCreateIsolatedWorktree(projectRoot); },
        }]
      : [];
    const remoteProjectMenuItems = node.remote ? buildRemoteProjectMenuItems({ ref: node.remote, t, closeMenu, openRemoteProject, openRemoteWindow, setRemoteSessions, refresh, showToast }) : [];
    const projectMenuItems: ContextMenuItem[] = [
      {
        key: "new-group",
        icon: <FolderPlus size={13} />,
        label: t("projectTree.newGroup"),
        onSelect: () => organization.createGroup(node, t("projectTree.newGroup")),
      },
      {
        key: "new-session",
        icon: <Plus size={13} />,
        label: t("projectTree.newTopic"),
        onSelect: () => {
          void handleCreateTopic(scope, projectPath, key);
        },
      },
      ...isolatedWorkspaceItems,
      {
        key: "rename",
        icon: <Pencil size={13} />,
        label: t("projectTree.renameProject"),
        onSelect: () => startRenameProject(key, projectRoot, projectLabel),
      },
      { type: "separator" as const, key: "color-separator" },
      ...PROJECT_COLOR_OPTIONS.map((option): ContextMenuItem => ({
        key: `color-${option.key || "default"}`,
        label: colorMenuLabel(projectColorLabel(t, option.key), option.key, (node.projectColor || "") === option.key),
        onSelect: () => {
          void setProjectColor(colorTargetRoot, option.key);
        },
      })),
      { type: "separator" as const, key: "path-separator" },
      {
        key: "reveal",
        icon: <FolderOpen size={13} />,
        label: t(revealLabelKey(platform)),
        disabled: !projectPath,
        onSelect: () => {
          void app.RevealPath(projectPath).catch(() => {});
          closeMenu();
        },
      },
      {
        key: "copy-path",
        icon: <Copy size={13} />,
        label: t("projectTree.copyPath"),
        disabled: !projectPath,
        onSelect: () => {
          void copyProjectPath(projectPath);
          closeMenu();
        },
      },
      ...(scope === "project"
        ? [
            { type: "separator" as const, key: "remove-separator" },
            {
              key: "remove",
              icon: <XCircle size={13} />,
              label: confirmRemoveProject === key ? t("projectTree.confirmRemoveProject") : t("projectTree.removeProject"),
              danger: true,
              onSelect: () => {
                if (confirmRemoveProject === key) void removeProject(projectPath);
                else setConfirmRemoveProject(key);
              },
            },
          ]
        : []),
    ];
    const workbenchProjectMenuItems: ContextMenuItem[] = [
      ...(scope === "project"
        ? [
            {
              key: projectPinned ? "unpin-project" : "pin-project",
              icon: <Pin size={13} />,
              label: t(projectPinned ? "projectTree.unpinProject" : "projectTree.pinProject"),
              onSelect: () => {
                void setProjectPinned(projectRoot, !projectPinned);
              },
            },
          ]
        : []),
      ...isolatedWorkspaceItems,
      {
        key: "reveal",
        icon: <FolderOpen size={13} />,
        label: t(revealLabelKey(platform)),
        disabled: !projectPath,
        onSelect: () => {
          void app.RevealPath(projectPath).catch(() => {});
          closeMenu();
        },
      },
      {
        key: "rename",
        icon: <Pencil size={13} />,
        label: t("projectTree.renameProjectWorkbench"),
        onSelect: () => startRenameProject(key, projectRoot, projectLabel),
      },
      {
        key: "archive-active-topic",
        icon: <Archive className={activeTopicId && trashingTopics.has(activeTopicId) ? "project-tree__archive-spinner" : undefined} size={13} />,
        label: activeTopicArchiveTarget && confirmArchiveTarget === activeTopicArchiveTarget
          ? t("history.confirmMoveToTrash")
          : t("projectTree.archiveConversation"),
        disabled: !activeTopicInProject || !activeTopicId || activeTopicArchiveBlocked || Boolean(activeTopicId && trashingTopics.has(activeTopicId)),
        danger: true,
        onSelect: () => {
          if (!activeTopicId) return;
          if (confirmArchiveTarget === activeTopicArchiveTarget) void trashTopic(activeTopicId);
          else setConfirmArchiveTarget(activeTopicArchiveTarget);
        },
      },
      ...(scope === "project"
        ? [
            { type: "separator" as const, key: "remove-separator" },
            {
              key: "remove",
              icon: <XCircle size={13} />,
              label: confirmRemoveProject === key ? t("projectTree.confirmRemoveProjectShort") : t("projectTree.removeProjectShort"),
              danger: true,
              onSelect: () => {
                if (confirmRemoveProject === key) void removeProject(projectPath);
                else setConfirmRemoveProject(key);
              },
            },
          ]
        : []),
    ];

    const folderShowAll = showAllTopics.has(key);
    const { visible: windowedChildren, hiddenCount } = classicTruncationActive
      ? classicTopicWindow(children, folderShowAll)
      : { visible: children, hiddenCount: 0 };
    const windowToggleVisible = classicTruncationActive && (hiddenCount > 0 || (folderShowAll && children.length > CLASSIC_TOPIC_PREVIEW_LIMIT));
    const backendPage = topicPageState[key];
    const renderFolderChildren = () => {
      if (!hasChildren) {
        const remoteGroupKey = node.remote ? remoteProjectKey(node.remote) : "";
        const remoteBusy = Boolean(remoteGroupBusy[remoteGroupKey]);
        const remoteError = remoteGroupError[remoteGroupKey] || "";
        if (node.remote) return <RemoteProjectEmptyState
          busy={remoteBusy} error={remoteError} ready={remoteServers[node.remote.hostId]?.[node.remote.workspace]?.state === "ready"}
          isExpanded={isExpanded} depth={depth} classicTopics={classicTopics} t={t} onEnsure={() => ensureRemoteGroupSessions(node.remote!.hostId, node.remote!.workspace)}
        />;
        // While the first topic page is still loading (cold start, catalog
        // reconcile in flight), show a skeleton instead of a blank folder or
        // a premature "no topics" placeholder.
        if (backendPage?.loading) {
          return (
            <div className={`project-tree__children${isExpanded ? " project-tree__children--expanded" : ""}`}>
              <div className="project-tree__children-inner">
                <div className="project-tree__skeleton" style={{ paddingLeft: 14 + (depth + 1) * 16 }} aria-hidden="true">
                  <span className="project-tree__skeleton-bar" />
                  <span className="project-tree__skeleton-bar project-tree__skeleton-bar--short" />
                  <span className="project-tree__skeleton-bar" />
                  <span className="project-tree__skeleton-bar project-tree__skeleton-bar--short" />
                </div>
              </div>
            </div>
          );
        }
        if (!classicTopics) return null;
        return (
          <div className={`project-tree__children${isExpanded ? " project-tree__children--expanded" : ""}`}>
            <div className="project-tree__children-inner">
              <div className="project-tree__topic-placeholder" style={{ paddingLeft: 14 + (depth + 1) * 16 }}>
                {t("projectTree.noTopics")}
              </div>
            </div>
          </div>
        );
      }
      return (
        <div className={`project-tree__children${isExpanded ? " project-tree__children--expanded" : ""}`}>
          <div className="project-tree__children-inner">
            <ProjectTreeGroupRows folder={node} children={windowedChildren} depth={depth + 1} section={section} visible={isVisible && isExpanded} organization={organization} renderNode={renderNode} t={t} />
            {windowToggleVisible && (
              <button
                type="button"
                className="project-tree__topic-window-toggle"
                style={{ paddingLeft: 14 + (depth + 1) * 16 }}
                onClick={() => toggleShowAllTopics(key)}
              >
                {hiddenCount > 0 ? t("projectTree.showMoreTopics", { n: hiddenCount }) : t("projectTree.showFewerTopics")}
              </button>
            )}
            {backendPage?.nextCursor && (
              <button
                type="button"
                className="project-tree__topic-window-toggle"
                style={{ paddingLeft: 14 + (depth + 1) * 16 }}
                disabled={backendPage.loading}
                onClick={() => void loadProjectTopics(node, true)}
              >
                {backendPage.loading ? t("projectTree.indexing") : t("projectTree.loadMore")}
              </button>
            )}
          </div>
        </div>
      );
    };

    if (editingProject?.key === key) {
      return (
        <div key={key} className="project-tree__project-wrapper">
          <div
            className={`project-tree__folder project-tree__folder--editing${projectActive ? " project-tree__folder--active" : ""}`}
            style={{ paddingLeft: 8 + depth * 16 }}
          >
            <input
              autoFocus
              className="project-tree__folder-input"
              value={projectDraft}
              onChange={(event) => setProjectDraft(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") void commitRenameProject(projectRoot);
                if (event.key === "Escape") setEditingProject(null);
              }}
              onBlur={() => void commitRenameProject(projectRoot)}
            />
          </div>
          {renderFolderChildren()}
        </div>
      );
    }

    return (
      <div key={key} className="project-tree__project-wrapper">
        <div
          className={`project-tree__folder${scopeClass}${pinnedClass}${draggableProject ? " project-tree__folder--draggable" : ""}${projectActive ? " project-tree__folder--active" : ""}${projectMenuOpen ? " project-tree__folder--menu-open" : ""}${dragProjectRoot === projectDragKey ? " project-tree__folder--dragging" : ""}${projectDropPosition ? ` project-tree__folder--drop-${projectDropPosition}` : ""}`}
          style={accentStyle}
          draggable={draggableProject}
          aria-grabbed={draggableProject ? dragProjectRoot === projectRoot : undefined}
          onDragStart={handleProjectDragStart}
          onDragOver={handleProjectDragOver}
          onDragLeave={(event) => {
            if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setDropProject(null);
          }}
          onDrop={handleProjectDrop}
          onDragEnd={clearProjectDrag}
          onContextMenu={openProjectMenu}
        >
          <button
            type="button"
            className="project-tree__folder-main"
            style={{ paddingLeft: 8 + depth * 16 }}
            onClick={() => {
              if (node.remote && !folderDisclosure.canExpand) return void openRemoteProject(node.remote, { focus: true });
              if (folderDisclosure.canExpand) {
                const willExpand = !expanded.has(key);
                toggleExpand(key, node);
                if (node.remote && willExpand) { void ensureRemoteGroupSessions(node.remote.hostId, node.remote.workspace); }
              }
            }}
            onKeyDown={(event) => {
              if (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10")) {
                openProjectMenu(event);
              }
            }}
            aria-expanded={folderDisclosure.ariaExpanded}
          >
            <span className={folderDisclosure.iconStackClassName}>
              {node.remote ? <Cloud size={14} className="project-tree__folder-icon" /> : folderDisclosure.isOpen ? <FolderOpen size={14} className="project-tree__folder-icon" /> : <Folder size={14} className="project-tree__folder-icon" />}
            </span>
            <span className="project-tree__folder-color" aria-hidden="true" />
            <span className={`project-tree__folder-label${!hasChildren ? " project-tree__folder-label--empty" : ""}`}>
              {projectLabel}
              {node.isolatedWorktree && <WorktreeBadge size={11} />}
              {node.remote ? <span className={`project-tree__remote-badge project-tree__remote-badge--${remoteServeBadgeState(remoteServers[node.remote.hostId]?.[node.remote.workspace], remoteGroupBusy[remoteProjectKey(node.remote)])}`} aria-hidden="true" /> : null}
            </span>
            <ProjectTreeFolderActivity folder={node} />
          </button>
          {compactTopics && (
            <Tooltip label={t("projectTree.projectActions")} className="project-tree__folder-action-slot">
              <button
                type="button"
                className="project-tree__folder-action project-tree__folder-action--menu"
                aria-label={t("projectTree.projectActions")}
                aria-haspopup="menu"
                aria-expanded={projectMenuOpen}
                onClick={(e) => {
                  openProjectMenu(e);
                }}
              >
                <MoreHorizontal size={16} aria-hidden="true" />
              </button>
            </Tooltip>
          )}
          {!node.remote && <Tooltip label={t("projectTree.newTopicTooltip")} className={compactTopics ? "project-tree__folder-action-slot" : "project-tree__action-slot"}>
            <button
              type="button"
              className={compactTopics
                ? `project-tree__folder-action project-tree__folder-action--create${creatingProject === key ? " project-tree__folder-action--active" : ""}`
                : `project-tree__new-topic${creatingProject === key ? " project-tree__new-topic--active" : ""}`}
              aria-label={t("projectTree.newTopicTooltip")}
              disabled={creatingProject !== null}
              onClick={(e) => {
                e.stopPropagation();
                void handleCreateTopic(scope, projectPath, key);
              }}
            >
              {compactTopics ? <Plus size={15} aria-hidden="true" /> : <Plus size={12} aria-hidden="true" />}
            </button>
          </Tooltip>}
          <ContextMenu
            open={projectMenuOpen}
            point={menuPoint}
            items={node.remote ? remoteProjectMenuItems : compactTopics ? workbenchProjectMenuItems : projectMenuItems}
            minWidth={compactTopics ? 206 : 212}
            ariaLabel={t("projectTree.projectActions")}
            onClose={closeMenu}
          />
        </div>
        {renderFolderChildren()}
      </div>
    );
  };

  const workbenchHeaderMoreItems: ContextMenuItem[] = [
    {
      key: "archive-all",
      icon: <Archive size={13} />,
      label: t("projectTree.archiveAllConversations"),
      disabled: true,
      onSelect: () => {},
    },
    { type: "separator", key: "organize-separator" },
    {
      key: "organize-heading",
      icon: <Folder size={13} />,
      label: t("projectTree.organizeSidebar"),
      disabled: true,
      variant: "section",
      onSelect: () => {},
    },
    {
      key: "organize-project",
      icon: <Folder size={13} />,
      label: menuLabelWithCheck(t("projectTree.organizeByProject"), workbenchOrganizeMode === "project"),
      onSelect: () => {
        setWorkbenchOrganizeMode("project");
        closeMenu();
      },
    },
    {
      key: "organize-recent",
      icon: <Folder size={13} />,
      label: menuLabelWithCheck(t("projectTree.organizeRecentProjects"), workbenchOrganizeMode === "recent"),
      onSelect: () => {
        setWorkbenchOrganizeMode("recent");
        closeMenu();
      },
    },
    {
      key: "organize-time",
      icon: <Clock size={13} />,
      label: menuLabelWithCheck(t("projectTree.organizeByTime"), workbenchOrganizeMode === "time"),
      onSelect: () => {
        setWorkbenchOrganizeMode("time");
        closeMenu();
      },
    },
    {
      key: "move-section-down",
      icon: <ArrowDown size={13} />,
      label: t("projectTree.moveSectionDown"),
      disabled: true,
      onSelect: () => {},
    },
    { type: "separator", key: "sort-separator" },
    {
      key: "sort-heading",
      icon: <Clock size={13} />,
      label: t("projectTree.sortCriteria"),
      disabled: true,
      variant: "section",
      onSelect: () => {},
    },
    {
      key: "sort-created",
      icon: <Clock size={13} />,
      label: menuLabelWithCheck(t("projectTree.sortByCreatedAt"), workbenchSortMode === "created"),
      onSelect: () => {
        selectWorkbenchSortMode("created");
      },
    },
    {
      key: "sort-updated",
      icon: <Pencil size={13} />,
      label: menuLabelWithCheck(t("projectTree.sortByUpdatedAt"), workbenchSortMode === "updated"),
      onSelect: () => {
        selectWorkbenchSortMode("updated");
      },
    },
  ];

  const addItemCallbacks = {
    onBlank: () => { closeMenu(); openBlankProjectFlow(); },
    onLocal: () => { closeMenu(); void handleAddProject(); },
    onRemote: () => { closeMenu(); openRemoteConnectFlow(); },
  };
  const classicHeaderAddItems = projectTreeHeaderAddItems({
    localLabel: t("projectTree.addProjectTooltip"), remoteLabel: t("projectTree.remoteConnection"), disabled: addingProject, ...addItemCallbacks,
  });
  const workbenchHeaderAddItems = projectTreeHeaderAddItems({
    blankLabel: t("projectTree.createBlankProject"), localLabel: t("projectTree.useExistingFolder"), remoteLabel: t("projectTree.remoteConnection"), disabled: addingProject, ...addItemCallbacks,
  });

  const timeFilterBadge = timeFilter !== "all" ? (timeFilter === "1d" ? "24h" : timeFilter) : "";
  const timeFilterDisplayLabel = timeFilter === "all" ? t("projectTree.timeFilterAll")
    : timeFilter === "10" ? t("projectTree.timeFilter10")
    : timeFilter === "20" ? t("projectTree.timeFilter20")
    : timeFilter === "1h" ? t("projectTree.timeFilter1h")
    : timeFilter === "3h" ? t("projectTree.timeFilter3h")
    : timeFilter === "5h" ? t("projectTree.timeFilter5h")
    : t("projectTree.timeFilter1d");
  const renderTimeFilterControl = (mode: "classic" | "workbench") => {
    const workbench = mode === "workbench";
    const active = timeFilter !== "all";
    // The classic menu also hosts the sort-criteria section, so its label
    // covers both; creation reuses the classic control but stays filter-only.
    const filterOnlyLabel = variant === "classic" ? t("projectTree.filterAndSort") : t("projectTree.timeFilter");
    const controlLabel = workbench ? `${t("projectTree.timeFilter")}: ${timeFilterDisplayLabel}` : filterOnlyLabel;
    const buttonClassName = workbench
      ? `project-tree__header-icon-btn project-tree__header-icon-btn--filter${active ? " project-tree__header-icon-btn--active" : ""}`
      : `project-tree__header-action-btn${active ? " project-tree__header-action-btn--active" : ""}`;
    return (
      <Tooltip
        label={controlLabel}
        className={`project-tree__action-slot project-tree__header-action-slot project-tree__header-action-slot--filter${workbench ? " project-tree__header-action-slot--workbench-filter" : ""}`}
      >
        <div ref={filterRef} className="project-tree__time-filter">
          <button
            ref={filterTriggerRef}
            type="button"
            className={buttonClassName}
            aria-label={controlLabel}
            aria-haspopup="menu"
            aria-expanded={filterMenuOpen}
            onClick={() => {
              setWorkbenchHeaderMenu(null);
              setMenuPoint(null);
              setFilterMenuOpen(!filterMenuOpen);
            }}
          >
            <Clock size={workbench ? 15 : 14} aria-hidden="true" />
            {timeFilterBadge && (
              <span className="project-tree__time-filter-label">
                {timeFilterBadge}
              </span>
            )}
          </button>
          {filterMenuOpen && (
            <div className="project-tree__time-filter-menu" role="menu" aria-label={filterOnlyLabel} onKeyDown={moveMenuFocus}>
              <button
                type="button"
                className={`project-tree__time-filter-opt${timeFilter === "all" ? " project-tree__time-filter-opt--on" : ""}`}
                onClick={() => { onTimeFilterChange("all"); setFilterMenuOpen(false); }}
                role="menuitem"
              >
                {t("projectTree.timeFilterAll")}
              </button>
              <div className="project-tree__time-filter-sep" role="separator" />
              <button
                type="button"
                className={`project-tree__time-filter-opt${timeFilter === "10" ? " project-tree__time-filter-opt--on" : ""}`}
                onClick={() => { onTimeFilterChange("10"); setFilterMenuOpen(false); }}
                role="menuitem"
              >
                {t("projectTree.timeFilter10")}
              </button>
              <button
                type="button"
                className={`project-tree__time-filter-opt${timeFilter === "20" ? " project-tree__time-filter-opt--on" : ""}`}
                onClick={() => { onTimeFilterChange("20"); setFilterMenuOpen(false); }}
                role="menuitem"
              >
                {t("projectTree.timeFilter20")}
              </button>
              <div className="project-tree__time-filter-sep" role="separator" />
              <button
                type="button"
                className={`project-tree__time-filter-opt${timeFilter === "1h" ? " project-tree__time-filter-opt--on" : ""}`}
                onClick={() => { onTimeFilterChange("1h"); setFilterMenuOpen(false); }}
                role="menuitem"
              >
                {t("projectTree.timeFilter1h")}
              </button>
              <button
                type="button"
                className={`project-tree__time-filter-opt${timeFilter === "3h" ? " project-tree__time-filter-opt--on" : ""}`}
                onClick={() => { onTimeFilterChange("3h"); setFilterMenuOpen(false); }}
                role="menuitem"
              >
                {t("projectTree.timeFilter3h")}
              </button>
              <button
                type="button"
                className={`project-tree__time-filter-opt${timeFilter === "5h" ? " project-tree__time-filter-opt--on" : ""}`}
                onClick={() => { onTimeFilterChange("5h"); setFilterMenuOpen(false); }}
                role="menuitem"
              >
                {t("projectTree.timeFilter5h")}
              </button>
              <button
                type="button"
                className={`project-tree__time-filter-opt${timeFilter === "1d" ? " project-tree__time-filter-opt--on" : ""}`}
                onClick={() => { onTimeFilterChange("1d"); setFilterMenuOpen(false); }}
                role="menuitem"
              >
                {t("projectTree.timeFilter1d")}
              </button>
              {variant === "classic" && (
                <>
                  <div className="project-tree__time-filter-sep" role="separator" />
                  <div className="project-tree__time-filter-title">{t("projectTree.sortCriteria")}</div>
                  <button
                    type="button"
                    className={`project-tree__time-filter-opt${workbenchSortMode === "updated" ? " project-tree__time-filter-opt--on" : ""}`}
                    onClick={() => selectWorkbenchSortMode("updated")}
                    role="menuitem"
                  >
                    {t("projectTree.sortByUpdatedAt")}
                  </button>
                  <button
                    type="button"
                    className={`project-tree__time-filter-opt${workbenchSortMode === "created" ? " project-tree__time-filter-opt--on" : ""}`}
                    onClick={() => selectWorkbenchSortMode("created")}
                    role="menuitem"
                  >
                    {t("projectTree.sortByCreatedAt")}
                  </button>
                </>
              )}
            </div>
          )}
        </div>
      </Tooltip>
    );
  };

  const renderProjectHeader = (mode: "classic" | "workbench") => (
    <div className="project-tree__header">
      <span className="project-tree__header-title">
        <BriefcaseBusiness className="project-tree__header-icon" size={13} />
        {t("projectTree.workspaceTitle")}
      </span>
      <span className="project-tree__header-actions">
        {mode === "workbench" ? (
          <>
            {renderTimeFilterControl("workbench")}
            <Tooltip label={workbenchCollapseToggleLabel} className="project-tree__header-action-slot">
              <button
                type="button"
                className="project-tree__header-icon-btn"
                aria-label={workbenchCollapseToggleLabel}
                disabled={!canToggleCollapsedView}
                onClick={toggleCollapsedView}
              >
                {canRestoreCollapsedView ? <Maximize2 size={15} aria-hidden="true" /> : <Minimize2 size={15} aria-hidden="true" />}
              </button>
            </Tooltip>
            <span className="project-tree__header-menu-wrap">
              <Tooltip label={t("projectTree.moreActions")} className="project-tree__header-action-slot">
                <button
                  type="button"
                  className={`project-tree__header-icon-btn${workbenchHeaderMenu === "more" ? " project-tree__header-icon-btn--active" : ""}`}
                  aria-label={t("projectTree.moreActions")}
                  aria-haspopup="menu"
                  aria-expanded={workbenchHeaderMenu === "more"}
                  onClick={(event) => {
                    openWorkbenchHeaderMenu(event, "more");
                  }}
                >
                  <MoreHorizontal size={16} aria-hidden="true" />
                </button>
              </Tooltip>
              <ContextMenu
                open={workbenchHeaderMenu === "more"}
                point={menuPoint}
                items={workbenchHeaderMoreItems}
                minWidth={222}
                ariaLabel={t("projectTree.moreActions")}
                onClose={closeMenu}
              />
            </span>
            <span className="project-tree__header-menu-wrap">
              <Tooltip label={t("projectTree.addProjectTooltip")} className="project-tree__header-action-slot">
                <button
                  type="button"
                  className={`project-tree__header-icon-btn${workbenchHeaderMenu === "add" ? " project-tree__header-icon-btn--active" : ""}`}
                  aria-label={t("projectTree.addProjectTooltip")}
                  aria-haspopup="menu"
                  aria-expanded={workbenchHeaderMenu === "add"}
                  disabled={addingProject}
                  onClick={(event) => {
                    openWorkbenchHeaderMenu(event, "add");
                  }}
                >
                  <FolderPlus size={16} aria-hidden="true" />
                </button>
              </Tooltip>
              <ContextMenu
                open={workbenchHeaderMenu === "add"}
                point={menuPoint}
                items={workbenchHeaderAddItems}
                minWidth={206}
                ariaLabel={t("projectTree.addProjectTooltip")}
                onClose={closeMenu}
              />
            </span>
          </>
        ) : (
          <>
            {renderTimeFilterControl("classic")}
            <Tooltip label={collapseToggleLabel} className="project-tree__action-slot project-tree__header-action-slot project-tree__action-slot--collapse">
              <button
                type="button"
                className={`project-tree__collapse-all${canRestoreCollapsedView ? " project-tree__collapse-all--restore" : ""}`}
                aria-label={collapseToggleLabel}
                aria-pressed={canRestoreCollapsedView}
                disabled={!canToggleCollapsedView}
                onClick={toggleCollapsedView}
              >
                {canRestoreCollapsedView ? <ListRestart size={14} /> : <ListCollapse size={14} />}
              </button>
            </Tooltip>
            <ProjectTreeHeaderAddControl
              open={workbenchHeaderMenu === "add"} point={menuPoint} items={classicHeaderAddItems}
              label={t("projectTree.addProjectTooltip")} disabled={addingProject}
              onOpen={(event) => openWorkbenchHeaderMenu(event, "add")} onClose={closeMenu}
            />
          </>
        )}
      </span>
    </div>
  );

  const renderEmptyState = () => {
    if (query.trim()) return <div className="project-tree__empty">{t("projectTree.emptyNoMatch")}</div>;
    if (timeFilter !== "all") {
      return (
        <div className="project-tree__empty">{t("projectTree.emptyNoTimeFilterMatch")}
          <button
            type="button"
            className="project-tree__empty-primary"
            onClick={() => onTimeFilterChange("all")}
          >
            {t("projectTree.clearTimeFilter")}
          </button>
        </div>
      );
    }
    return (
      <div className="project-tree__empty-state">
        <div className="project-tree__empty project-tree__empty--subtle">{t("projectTree.emptyNoProjects")}</div>
        <button
          type="button"
          className="project-tree__empty-primary"
          onClick={() => void handleAddProject()}
          disabled={addingProject}
        >
          <FolderPlus size={14} />
          <span>{t("projectTree.addProjectTooltip")}</span>
        </button>
        <ProjectTreeRemoteAction label={t("projectTree.remoteConnection")} disabled={addingProject} onClick={openRemoteConnectFlow} />
      </div>
    );
  };

  const hasTreeRows = pinnedTreeSections.pinned.length > 0 || pinnedTreeSections.projects.length > 0;

  // Report visible topics to parent after render so shortcuts match sidebar order.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => {
    onVisibleTopicsChange?.(visibleTopicsCollectorRef.current);
  });

  // Reset topic index counter and visible topics collector before each render.
  topicIndexRef.current = 0;
  visibleTopicsCollectorRef.current = [];
  const catalogNotice = sessionCatalogNotice(catalogStatus);
  const catalogNoticeText = catalogNotice === "indexing"
    ? (catalogStatus.total <= 0 ? t("projectTree.indexing")
      : t("projectTree.indexingProgress", { done: catalogStatus.indexed, total: catalogStatus.total }))
    : catalogNotice === "repair-active"
      ? t("projectTree.repairActive", { count: catalogStatus.repairActive ?? catalogStatus.repairPending })
      : catalogNotice === "repair-deferred"
        ? t("projectTree.repairDeferred")
        : catalogNotice === "repair-blocked"
          ? t("projectTree.repairBlocked", { count: catalogStatus.repairBlocked ?? catalogStatus.repairPending })
          : `${t("projectTree.indexing")} — ${t("task.state.failed")}`;

  return (
    <div className="project-tree">
      {searchVisible && (
        <label className="project-tree__search">
          <Search size={14} />
          <input
            ref={searchInputRef}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t("projectTree.searchPlaceholder")}
          />
        </label>
      )}
      {catalogNotice && (
        <div className="project-tree__catalog-progress" role="status">
          <span>{catalogNoticeText}</span>
          {catalogNotice === "rebuild" && (
            <button type="button" className="project-tree__catalog-rebuild" onClick={() => void rebuildSessionCatalog()}>
              {t("projectTree.rebuildCatalog")}
            </button>
          )}
        </div>
      )}
      {compactTopics ? (
        <>
          {renderProjectHeader("workbench")}
          <div className="project-tree__list project-tree__list--workbench">
            {!hasTreeRows ? (
              renderEmptyState()
            ) : (
              <>
                {pinnedTreeSections.pinned.length > 0 && (
                  <div className="project-tree__section project-tree__section--pinned">
                    <div className="project-tree__section-title project-tree__section-title--pinned">
                      <Pin size={14} className="project-tree__section-title-icon" aria-hidden="true" />
                      <span>{t("projectTree.pinnedTitle")}</span>
                    </div>
                    {pinnedTreeSections.pinned.map((node) => renderNode(node, 0, "pinned"))}
                  </div>
                )}
                <div className="project-tree__section project-tree__section--projects">
                  {pinnedTreeSections.projects.map((node) => renderNode(node, 0, "projects"))}
                </div>
              </>
            )}
          </div>
        </>
      ) : (
        <>
          {renderProjectHeader("classic")}
          <div className="project-tree__list" onScroll={cancelHoverCard}>
            {!hasTreeRows ? (
              renderEmptyState()
            ) : (
              <>
                {pinnedTreeSections.pinned.length > 0 && (
                  <div className="project-tree__section project-tree__section--pinned">
                    <div className="project-tree__section-title">{t("projectTree.pinnedTitle")}</div>
                    {pinnedTreeSections.pinned.map((node) => renderNode(node, 1, "pinned"))}
                  </div>
                )}
                <div className="project-tree__section project-tree__section--projects">
                  {pinnedTreeSections.projects.map((node) => renderNode(node, 0, "projects"))}
                </div>
              </>
            )}
          </div>
        </>
      )}
      {hoverCard && createPortal(
        <div
          className="project-tree__hover-card"
          style={{ left: hoverCard.left, top: hoverCard.top }}
          aria-hidden="true"
        >
          <div className="project-tree__hover-card-title">{hoverCard.card.title}</div>
          {hoverCard.card.statusLabel && (
            <div className="project-tree__hover-card-status">{hoverCard.card.statusLabel}</div>
          )}
          <div className="project-tree__hover-card-meta">
            {[hoverCard.card.metaLine, hoverCard.card.exactTime].filter(Boolean).join(" · ")}
          </div>
          {hoverCard.card.projectLabel && (
            <div className="project-tree__hover-card-project">
              <Folder size={12} aria-hidden="true" />
              <span>{hoverCard.card.projectLabel}</span>
            </div>
          )}
        </div>,
        document.body,
      )}
      {blankProjectFlow}
      {remoteConnectFlow}
    </div>
  );
}
