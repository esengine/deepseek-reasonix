// Run: tsx src/__tests__/project-tree-organization-drop-zones.test.tsx
// Regression test: dropping a topic into the expanded group's member area
// (project-tree__group-children) must be handled, not only the group header
// row (project-tree__group-main). See bug: dragging onto member rows / gaps
// inside an expanded group did not add the topic to the group.

import { JSDOM } from "jsdom";
import React, { StrictMode } from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { ProjectTreeGroupRows, useProjectTreeOrganization } from "../components/ProjectTreeOrganization";
import type { ProjectNode, ProjectTreeOrganizationBindings, SessionGroup } from "../lib/types";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  process.stdout.write(`  ${value ? "PASS" : "FAIL"}  ${label}\n`);
  if (value) passed += 1; else failed += 1;
}

function installDom(): JSDOM {
  const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", {
    pretendToBeVisual: true,
    url: "http://localhost/",
  });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  globalThis.Node = dom.window.Node;
  globalThis.Element = dom.window.Element;
  globalThis.HTMLElement = dom.window.HTMLElement;
  globalThis.Event = dom.window.Event;
  globalThis.MouseEvent = dom.window.MouseEvent;
  return dom;
}

const tree: ProjectNode[] = [{ key: "global_folder", kind: "global_folder", label: "Global", root: "", children: [] }];

const members: ProjectNode[] = [
  { key: "global_topic_m1", kind: "global_topic", label: "M1", topicId: "m1", root: "", children: [] },
  { key: "global_topic_m2", kind: "global_topic", label: "M2", topicId: "m2", root: "", children: [] },
];

function renderNode(node: ProjectNode): React.ReactNode {
  return <div data-testid="topic-row" data-topic-id={node.topicId}>{node.label}</div>;
}

function makeBindings(groups: SessionGroup[]): ProjectTreeOrganizationBindings {
  return {
    ReorderTopics: async () => {},
    ListProjectGroups: async () => structuredClone(groups),
    SaveSessionGroups: async () => {},
  };
}

function Harness({
  groups,
  onDropInto,
}: {
  groups: SessionGroup[];
  onDropInto: () => void;
}) {
  const organization = useProjectTreeOrganization({ tree, refresh: async () => {}, bindings: makeBindings(groups) });
  // Force the group to be a valid drop target regardless of the (unset) drag
  // context, so the children area handlers are exercised in isolation.
  (organization as unknown as { canDropTopicInto: (key: string) => boolean }).canDropTopicInto = () => true;
  // Observe whether the group drop handler fires.
  (organization as unknown as { dropTopicInto: (key: string, id: string) => void }).dropTopicInto = () => { onDropInto(); };
  return <ProjectTreeGroupRows
    folder={tree[0]!}
    children={members}
    depth={0}
    section="projects"
    visible
    organization={organization}
    renderNode={renderNode}
    t={(key) => key}
  />;
}

async function flush() {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

async function mount(groups: SessionGroup[], onDropInto: () => void) {
  const dom = installDom();
  const root = createRoot(document.getElementById("root")!);
  await act(async () => {
    root.render(<StrictMode><Harness groups={groups} onDropInto={onDropInto} /></StrictMode>);
    await flush();
  });
  return { dom, root };
}

async function cleanup(dom: JSDOM, root: ReturnType<typeof createRoot>) {
  await act(async () => root.unmount());
  dom.window.close();
}

console.log("\nproject tree organization drop zones");

{
  const groups: SessionGroup[] = [{ id: "g1", title: "Group", topicIds: ["m1", "m2"] }];
  let dropCalls = 0;
  const { dom, root } = await mount(groups, () => { dropCalls += 1; });

  // Wait for ListProjectGroups to load so the group row (with its children
  // container) renders.
  let childrenArea: HTMLElement | null = null;
  for (let attempt = 0; attempt < 30 && !childrenArea; attempt += 1) {
    await act(async () => { await flush(); });
    childrenArea = document.querySelector<HTMLElement>(".project-tree__group-children");
  }
  ok(Boolean(childrenArea), "expanded group renders a children container");
  if (!childrenArea) {
    await cleanup(dom, root);
    process.stdout.write(`\nproject-tree-organization-drop-zones: ${passed} passed, ${failed} failed\n`);
    process.exit(1);
  }

  // A drop on the member area must be handled (preventDefault + dropTopicInto).
  const drop = new (globalThis.MouseEvent || globalThis.Event)("drop", { bubbles: true, cancelable: true });
  childrenArea.dispatchEvent(drop);
  await act(flush);
  ok(dropCalls === 1, "drop on the expanded member area is handled once");

  // A drop on the group header row still works.
  const header = document.querySelector<HTMLElement>(".project-tree__group-main");
  ok(Boolean(header), "group header row renders");
  if (header) {
    header.dispatchEvent(new (globalThis.MouseEvent || globalThis.Event)("drop", { bubbles: true, cancelable: true }));
    await act(flush);
  }
  ok(dropCalls === 2, "drop on the group header row is still handled");

  await cleanup(dom, root);
}

process.stdout.write(`\nproject-tree-organization-drop-zones: ${passed} passed, ${failed} failed\n`);
if (failed > 0) process.exit(1);
