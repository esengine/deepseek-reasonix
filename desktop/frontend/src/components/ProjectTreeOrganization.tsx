import { useCallback, useEffect, useRef, useState, lazy, Suspense, type DragEvent, type HTMLAttributes, type ReactNode } from "react";
import { Archive, FolderMinus, Pencil } from "lucide-react";
import { app } from "../lib/bridge";
import { asArray } from "../lib/array";
import type { Translator } from "../lib/i18n";
import { isTopicNode, projectTreeTopicArchiveBlocked } from "../lib/projectTreeTopic";
import type { ProjectTreeRefresh } from "../lib/projectTreeArchive";
import type { ProjectNode, ProjectTreeOrganizationBindings, SessionGroup } from "../lib/types";
import { ContextMenu, contextMenuPointFromEvent, type ContextMenuItem, type ContextMenuPoint } from "./ContextMenu";

export type ProjectDropPosition = "before" | "after";

export const GLOBAL_PROJECT_ORDER_KEY = "__global__";
const TOPIC_DRAG_TYPE = "application/x-reasonix-topic-id";

function projectOrderKey(node: ProjectNode): string {
  if (node.kind === "global_folder") return GLOBAL_PROJECT_ORDER_KEY;
  if (node.kind === "project" && node.root) return node.root;
  return "";
}

export function projectTreeProjectRoots(nodes: ProjectNode[]): string[] {
  return nodes.map(projectOrderKey).filter((key) => key !== "");
}

export function reorderedProjectRoots(
  nodes: ProjectNode[],
  draggedRoot: string,
  targetRoot: string,
  position: ProjectDropPosition,
): string[] {
  const roots = projectTreeProjectRoots(nodes);
  if (draggedRoot === targetRoot || !roots.includes(draggedRoot) || !roots.includes(targetRoot)) return roots;
  const next = roots.filter((root) => root !== draggedRoot);
  const targetIndex = next.indexOf(targetRoot);
  if (targetIndex < 0) return roots;
  next.splice(position === "before" ? targetIndex : targetIndex + 1, 0, draggedRoot);
  return next;
}

export function applyProjectOrder(nodes: ProjectNode[], roots: string[]): ProjectNode[] {
  const entries = nodes.map((node): [string, ProjectNode] => [projectOrderKey(node), node]).filter(([key]) => key !== "");
  const byRoot = new Map(entries);
  const ordered = roots.map((root) => byRoot.get(root)).filter((node): node is ProjectNode => Boolean(node));
  const orderedKeys = new Set(roots);
  return [...nodes.filter((node) => !orderedKeys.has(projectOrderKey(node))), ...ordered];
}

export function manualTopicOrder(a: ProjectNode, b: ProjectNode): number {
  const aOrder = typeof a.sortOrder === "number" && a.sortOrder >= 0 ? a.sortOrder : Number.MAX_SAFE_INTEGER;
  const bOrder = typeof b.sortOrder === "number" && b.sortOrder >= 0 ? b.sortOrder : Number.MAX_SAFE_INTEGER;
  return aOrder === bOrder ? 0 : aOrder - bOrder;
}

export function projectTreeOrganizationKey(node: ProjectNode): string {
  return node.kind === "global_folder" || node.kind === "global_topic" ? "global|" : `project|${node.root ?? ""}`;
}

function splitOrganizationKey(key: string): { scope: "global" | "project"; root: string } {
  return key === "global|" ? { scope: "global", root: "" } : { scope: "project", root: key.slice("project|".length) };
}

export function reorderedTopicIDs(
  nodes: ProjectNode[],
  scope: "global" | "project",
  root: string,
  draggedID: string,
  targetID: string,
  position: ProjectDropPosition,
): string[] | null {
  const parent = nodes.find((node) => projectTreeOrganizationKey(node) === (scope === "global" ? "global|" : `project|${root}`));
  if (!parent) return null;
  const ids = asArray(parent.children)
    .filter((node) => isTopicNode(node) && !node.runtimeOnly && node.topicId)
    .map((node) => node.topicId as string);
  if (draggedID === targetID || !ids.includes(draggedID) || !ids.includes(targetID)) return null;
  const rest = ids.filter((id) => id !== draggedID);
  const targetIndex = rest.indexOf(targetID);
  const insertAt = position === "before" ? targetIndex : targetIndex + 1;
  return [...rest.slice(0, insertAt), draggedID, ...rest.slice(insertAt)];
}

type TopicRowDragProps = Pick<HTMLAttributes<HTMLDivElement>, "draggable" | "onDragStart" | "onDragOver" | "onDragLeave" | "onDrop" | "onDragEnd">;

export interface ProjectTreeOrganizationController {
  topicRow(node: ProjectNode, disabled: boolean): { className: string; props: TopicRowDragProps };
  topicMenuItems(node: ProjectNode, t: Translator): ContextMenuItem[];
  createGroup(folder: ProjectNode, title: string): void;
  groupsFor(folder: ProjectNode): SessionGroup[];
  groupCollapsed(key: string, id: string): boolean;
  toggleGroup(key: string, id: string): void;
  renameGroup(key: string, id: string, title: string): void;
  deleteGroup(key: string, id: string): void;
  canDropTopicInto(key: string): boolean;
  dropTopicInto(key: string, groupID: string): void;
}

export function useProjectTreeOrganization({
  tree,
  refresh,
  onTopicsChanged,
  organizationRevision = 0,
  bindings = app,
}: {
  tree: ProjectNode[];
  refresh: ProjectTreeRefresh;
  onTopicsChanged?: () => Promise<void> | void;
  organizationRevision?: number;
  bindings?: ProjectTreeOrganizationBindings;
}): ProjectTreeOrganizationController {
  const [dragTopicID, setDragTopicID] = useState<string | null>(null);
  const [dropTopic, setDropTopic] = useState<{ topicID: string; position: ProjectDropPosition } | null>(null);
  const dragContextRef = useRef<{ scope: "global" | "project"; root: string } | null>(null);
  const [groupsByKey, setGroupsByKey] = useState<Record<string, SessionGroup[]>>({});
  const groupsRef = useRef(groupsByKey);
  const mountedRef = useRef(false);
  const loadedGroupsRef = useRef(new Set<string>());
  const loadingGroupsRef = useRef(new Set<string>());
  const groupMutationVersionsRef = useRef<Record<string, number>>({});
  const groupLoadSequencesRef = useRef<Record<string, number>>({});
  const groupSaveChainsRef = useRef(new Map<string, Promise<void>>());
  const organizationRevisionRef = useRef(organizationRevision);
  const [collapsedGroups, setCollapsedGroups] = useState(new Set<string>());

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  const setKeyGroups = useCallback((key: string, groups: SessionGroup[]) => {
    groupsRef.current = { ...groupsRef.current, [key]: groups };
    setGroupsByKey(groupsRef.current);
  }, []);

  const loadGroups = useCallback((key: string, force = false) => {
    if (!force && (loadedGroupsRef.current.has(key) || loadingGroupsRef.current.has(key))) return;
    const sequence = (groupLoadSequencesRef.current[key] ?? 0) + 1;
    groupLoadSequencesRef.current[key] = sequence;
    const mutationVersion = groupMutationVersionsRef.current[key] ?? 0;
    loadingGroupsRef.current.add(key);
    const { scope, root } = splitOrganizationKey(key);
    const read = typeof bindings.GetProjectGroups === "function"
      ? bindings.GetProjectGroups(scope, root)
      : bindings.ListProjectGroups(scope, root).then((groups) => ({ groups, revision: 0, applied: true }));
    void read.then((snapshot) => {
      if (!mountedRef.current || groupLoadSequencesRef.current[key] !== sequence) return;
      if ((groupMutationVersionsRef.current[key] ?? 0) !== mutationVersion) return;
      // Never replace an optimistic state while its semantic mutations are
      // queued. The CAS path reads the newest server snapshot before applying.
      if (groupSaveChainsRef.current.has(key)) return;
      loadedGroupsRef.current.add(key);
      setKeyGroups(key, asArray(snapshot.groups));
    }).catch(() => {}).finally(() => {
      if (groupLoadSequencesRef.current[key] === sequence) loadingGroupsRef.current.delete(key);
    });
  }, [bindings, setKeyGroups]);

  useEffect(() => {
    const force = organizationRevisionRef.current !== organizationRevision;
    organizationRevisionRef.current = organizationRevision;
    for (const folder of tree) {
      if (folder.kind !== "project" && folder.kind !== "global_folder") continue;
      const key = projectTreeOrganizationKey(folder);
      loadGroups(key, force);
    }
  }, [loadGroups, organizationRevision, tree]);

  const persistGroups = useCallback((key: string, update: (groups: SessionGroup[]) => SessionGroup[], legacyGroups: SessionGroup[]) => {
    const version = groupMutationVersionsRef.current[key] ?? 0;
    const { scope, root } = splitOrganizationKey(key);
    const previous = groupSaveChainsRef.current.get(key) ?? Promise.resolve();
    let settledGroups: SessionGroup[] | null = null;
    const pending = previous.catch(() => {}).then(async () => {
      if (typeof bindings.GetProjectGroups !== "function" || typeof bindings.SaveSessionGroupsVersioned !== "function") {
        await bindings.SaveSessionGroups(scope, root, legacyGroups);
        return;
      }
      for (let attempt = 0; attempt < 5; attempt++) {
        const current = await bindings.GetProjectGroups(scope, root);
        const next = update(asArray(current.groups));
        const saved = await bindings.SaveSessionGroupsVersioned(scope, root, current.revision, next);
        if (saved.applied) {
          settledGroups = asArray(saved.groups);
          return;
        }
      }
      throw new Error("session groups changed too frequently; retrying from server state");
    });
    groupSaveChainsRef.current.set(key, pending);
    void pending.then(() => {
      if (mountedRef.current && (groupMutationVersionsRef.current[key] ?? 0) === version && settledGroups) {
        setKeyGroups(key, settledGroups);
      }
    }).catch(() => {
      if ((groupMutationVersionsRef.current[key] ?? 0) !== version) return;
      if (groupSaveChainsRef.current.get(key) === pending) groupSaveChainsRef.current.delete(key);
      loadedGroupsRef.current.delete(key);
      loadGroups(key, true);
    }).finally(() => {
      if (groupSaveChainsRef.current.get(key) === pending) groupSaveChainsRef.current.delete(key);
    });
  }, [bindings, loadGroups, setKeyGroups]);

  const mutateGroups = useCallback((key: string, update: (groups: SessionGroup[]) => SessionGroup[]) => {
    const next = update(groupsRef.current[key] ?? []);
    groupMutationVersionsRef.current[key] = (groupMutationVersionsRef.current[key] ?? 0) + 1;
    loadedGroupsRef.current.add(key);
    setKeyGroups(key, next);
    persistGroups(key, update, next);
  }, [persistGroups, setKeyGroups]);

  const clearTopicDrag = useCallback(() => {
    dragContextRef.current = null;
    setDragTopicID(null);
    setDropTopic(null);
  }, []);

  useEffect(() => {
    if (!dragTopicID) return;
    window.addEventListener("dragend", clearTopicDrag);
    window.addEventListener("drop", clearTopicDrag);
    window.addEventListener("blur", clearTopicDrag);
    return () => {
      window.removeEventListener("dragend", clearTopicDrag);
      window.removeEventListener("drop", clearTopicDrag);
      window.removeEventListener("blur", clearTopicDrag);
    };
  }, [clearTopicDrag, dragTopicID]);

  const topicRow = useCallback((node: ProjectNode, disabled: boolean) => {
    const topicID = node.topicId ?? "";
    const key = projectTreeOrganizationKey(node);
    const draggable = !disabled && !node.runtimeOnly && topicID !== "";
    const sameScope = dragContextRef.current !== null && key === (dragContextRef.current.scope === "global" ? "global|" : `project|${dragContextRef.current.root}`);
    const className = sameScope && dropTopic?.topicID === topicID && dragTopicID !== topicID ? ` project-tree__topic--drop-${dropTopic.position}` : "";
    const props: TopicRowDragProps = { draggable };
    if (!draggable) return { className, props };
    props.onDragStart = (event) => {
      const context = splitOrganizationKey(key);
      dragContextRef.current = context;
      event.dataTransfer.setData(TOPIC_DRAG_TYPE, topicID);
      event.dataTransfer.setData("text/plain", topicID);
      event.dataTransfer.effectAllowed = "move";
      setDragTopicID(topicID);
    };
    props.onDragOver = (event) => {
      if (!sameScope || !dragTopicID || dragTopicID === topicID) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = "move";
      const rect = event.currentTarget.getBoundingClientRect();
      const position = event.clientY < rect.top + rect.height / 2 ? "before" : "after";
      setDropTopic((current) => current?.topicID === topicID && current.position === position ? current : { topicID, position });
    };
    props.onDragLeave = () => setDropTopic((current) => current?.topicID === topicID ? null : current);
    props.onDrop = (event: DragEvent<HTMLDivElement>) => {
      event.preventDefault();
      const draggedID = event.dataTransfer.getData(TOPIC_DRAG_TYPE) || dragTopicID;
      const context = dragContextRef.current;
      if (draggedID && context && sameScope) {
        const rect = event.currentTarget.getBoundingClientRect();
        const position = event.clientY < rect.top + rect.height / 2 ? "before" : "after";
        const ordered = reorderedTopicIDs(tree, context.scope, context.root, draggedID, topicID, position);
        if (ordered) void bindings.ReorderTopics(context.scope, context.root, ordered).then(() => refresh()).then(() => onTopicsChanged?.()).catch(() => refresh());
      }
      clearTopicDrag();
    };
    props.onDragEnd = clearTopicDrag;
    return { className, props };
  }, [bindings, clearTopicDrag, dragTopicID, dropTopic, onTopicsChanged, refresh, tree]);

  const removeTopicFromGroups = useCallback((node: ProjectNode) => {
    const topicID = node.topicId;
    if (!topicID) return;
    const key = projectTreeOrganizationKey(node);
    mutateGroups(key, (groups) => groups.map((group) => ({ ...group, topicIds: (group.topicIds ?? []).filter((id) => id !== topicID) })));
  }, [mutateGroups]);

  return {
    topicRow,
    topicMenuItems(node, t) {
      const topicID = node.topicId;
      if (!topicID || !(groupsRef.current[projectTreeOrganizationKey(node)] ?? []).some((group) => group.topicIds?.includes(topicID))) return [];
      return [{ key: "remove-from-group", icon: <FolderMinus size={13} />, label: t("projectTree.removeFromGroup"), onSelect: () => removeTopicFromGroups(node) }];
    },
    createGroup(folder, title) {
      const key = projectTreeOrganizationKey(folder);
      const suffix = typeof crypto !== "undefined" && crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(16).slice(2)}`;
      mutateGroups(key, (groups) => [...groups, { id: `group-${suffix}`, title, topicIds: [] }]);
    },
    groupsFor(folder) { return groupsByKey[projectTreeOrganizationKey(folder)] ?? []; },
    groupCollapsed(key, id) { return collapsedGroups.has(`${key}|${id}`); },
    toggleGroup(key, id) {
      setCollapsedGroups((current) => {
        const next = new Set(current), collapseKey = `${key}|${id}`;
        if (next.has(collapseKey)) next.delete(collapseKey); else next.add(collapseKey);
        return next;
      });
    },
    renameGroup(key, id, title) {
      const trimmed = title.trim();
      mutateGroups(key, (groups) => trimmed ? groups.map((group) => group.id === id ? { ...group, title: trimmed } : group) : groups.filter((group) => group.id !== id));
    },
    deleteGroup(key, id) { mutateGroups(key, (groups) => groups.filter((group) => group.id !== id)); },
    canDropTopicInto(key) {
      const context = dragContextRef.current;
      return Boolean(dragTopicID && context && key === (context.scope === "global" ? "global|" : `project|${context.root}`));
    },
    dropTopicInto(key, groupID) {
      const topicID = dragTopicID;
      if (!topicID) return;
      mutateGroups(key, (groups) => groups.map((group) => {
        const withoutTopic = (group.topicIds ?? []).filter((id) => id !== topicID);
        return group.id === groupID ? { ...group, topicIds: [...withoutTopic, topicID] } : { ...group, topicIds: withoutTopic };
      }));
      clearTopicDrag();
    },
  };
}

export function ProjectTreeGroupRows({
  folder,
  children,
  depth,
  section,
  visible,
  organization,
  renderNode,
  t,
}: {
  folder: ProjectNode;
  children: ProjectNode[];
  depth: number;
  section: "pinned" | "projects";
  visible: boolean;
  organization: ProjectTreeOrganizationController;
  renderNode: (node: ProjectNode, depth: number, section: "pinned" | "projects", visible: boolean) => ReactNode;
  t: Translator;
}) {
  const [menuGroup, setMenuGroup] = useState<string | null>(null);
  const [menuPoint, setMenuPoint] = useState<ContextMenuPoint | null>(null);
  const [editingGroup, setEditingGroup] = useState<string | null>(null);
  const [groupDraft, setGroupDraft] = useState("");
  const key = projectTreeOrganizationKey(folder);
  const groups = organization.groupsFor(folder);
  const groupedIDs = new Set(groups.flatMap((group) => group.topicIds ?? []));
  const commitRename = (id: string) => {
    organization.renameGroup(key, id, groupDraft);
    setEditingGroup(null);
  };
  return <>
    {children.filter((child) => !groupedIDs.has(child.topicId ?? "")).map((child) => renderNode(child, depth, section, visible))}
    {groups.map((group) => {
      const collapsed = organization.groupCollapsed(key, group.id);
      const members = children.filter((child) => group.topicIds?.includes(child.topicId ?? ""));
      const canDrop = organization.canDropTopicInto(key);
      return <div key={group.id} className={`project-tree__group${collapsed ? " project-tree__group--collapsed" : ""}`}>
        <div
          role="button"
          tabIndex={0}
          className={`project-tree__group-main${canDrop ? " project-tree__group-main--drop-target" : ""}`}
          style={{ paddingLeft: 14 + depth * 16 }}
          title={group.title}
          onClick={() => organization.toggleGroup(key, group.id)}
          onKeyDown={(event) => {
            if (editingGroup === group.id) return;
            if (event.key === "Enter" || event.key === " ") organization.toggleGroup(key, group.id);
          }}
          onContextMenu={(event) => {
            event.preventDefault();
            setMenuGroup(group.id);
            setMenuPoint(contextMenuPointFromEvent(event));
          }}
          onDragOver={(event) => {
            if (!canDrop) return;
            event.preventDefault();
            event.dataTransfer.dropEffect = "move";
          }}
          onDrop={(event) => {
            event.preventDefault();
            if (canDrop) organization.dropTopicInto(key, group.id);
          }}
        >
          <span className="project-tree__group-chevron" aria-hidden="true">{collapsed ? "▸" : "▾"}</span>
          {editingGroup === group.id ? <input
            autoFocus
            className="project-tree__group-input"
            value={groupDraft}
            onChange={(event) => setGroupDraft(event.target.value)}
            onFocus={(event) => event.target.select()}
            onKeyDown={(event) => {
              if (event.key === "Enter") commitRename(group.id);
              if (event.key === "Escape") setEditingGroup(null);
            }}
            onBlur={() => commitRename(group.id)}
            onClick={(event) => event.stopPropagation()}
          /> : <span className="project-tree__group-title">{group.title}</span>}
          <span className="project-tree__group-count">{members.length}</span>
        </div>
        {menuGroup === group.id && <ContextMenu
          open
          point={menuPoint}
          items={[
            { key: "rename", icon: <Pencil size={13} />, label: t("projectTree.renameGroup"), onSelect: () => { setEditingGroup(group.id); setGroupDraft(group.title); setMenuGroup(null); } },
            { key: "delete", icon: <Archive size={13} />, label: t("projectTree.deleteGroup"), danger: true, onSelect: () => { organization.deleteGroup(key, group.id); setMenuGroup(null); } },
          ]}
          minWidth={178}
          ariaLabel={t("projectTree.renameGroup")}
          onClose={() => setMenuGroup(null)}
        />}
        {!collapsed && members.length > 0 && (
          <div
            className={`project-tree__group-children${canDrop ? " project-tree__group-children--drop-target" : ""}`}
            onDragOver={(event) => {
              if (!canDrop) return;
              event.preventDefault();
              event.dataTransfer.dropEffect = "move";
            }}
            onDrop={(event) => {
              event.preventDefault();
              if (canDrop) organization.dropTopicInto(key, group.id);
            }}
          >
            {members.map((child) => renderNode(child, depth, section, visible))}
          </div>
        )}
      </div>;
    })}
  </>;
}

export function projectTreeFolderHasActiveRuntime(folder: ProjectNode): boolean {
  return asArray(folder.children).some(projectTreeTopicArchiveBlocked);
}

const FolderActivity = lazy(() => import("./ProjectTreeFolderActivity"));
export function ProjectTreeFolderActivity({ folder }: { folder: ProjectNode }) {
  return <Suspense fallback={null}><FolderActivity folder={folder} /></Suspense>;
}
