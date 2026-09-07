import type { Root, Text } from "mdast";
import type { InlineMath } from "mdast-util-math";
import { visit } from "unist-util-visit";
import { classifyInlineMath } from "./mathClassify";
import { latexNormalizeForKatex } from "./latexNormalize";
import {
  resolveProtectedInlineMathSource,
  restoreProtectedInlineMathSource,
} from "./mathNormalize";

type HastLikeNode = {
  type: string;
  value?: string;
  children?: HastLikeNode[];
};

type MathNodeData = {
  hChildren?: HastLikeNode[];
  hProperties?: Record<string, unknown>;
};

type ParentWithChildren = {
  children: Array<{ type: string; value?: unknown; position?: unknown }>;
};

type VFileLike = {
  value: unknown;
};

function originalSource(node: InlineMath, file: VFileLike): string {
  const source = String(file.value ?? "");
  const start = node.position?.start.offset;
  const end = node.position?.end.offset;
  if (typeof start === "number" && typeof end === "number") {
    const span = source.slice(start, end);
    if (span.startsWith("$") && span.endsWith("$")) {
      return `$${restoreProtectedInlineMathSource(span.slice(1, -1))}$`;
    }
    return restoreProtectedInlineMathSource(span);
  }
  return `$${restoreProtectedInlineMathSource(node.value)}$`;
}

function inlineMathSources(node: InlineMath, file: VFileLike): {
  source: string;
  rendered: string;
} {
  const parsed = resolveProtectedInlineMathSource(node.value);
  const fileSource = String(file.value ?? "");
  const start = node.position?.start.offset;
  const end = node.position?.end.offset;
  if (typeof start !== "number" || typeof end !== "number") return parsed;

  const span = fileSource.slice(start, end);
  if (!span.startsWith("$") || !span.endsWith("$")) return parsed;
  return {
    source: resolveProtectedInlineMathSource(span.slice(1, -1)).source,
    rendered: parsed.rendered,
  };
}

function setMathValue(
  node: { value: string; data?: unknown },
  source: string,
  value: string,
): void {
  node.value = value;

  // mdast-util-math caches hast children while parsing. Keep that rendering
  // payload in sync with node.value when a later remark plugin normalises it,
  // while carrying the unnormalised TeX through to the rehype stage.
  const data = node.data as MathNodeData | undefined;
  if (data) {
    data.hProperties ??= {};
    data.hProperties.dataLatexSource = source;
  }
  const hChildren = data?.hChildren;
  const updateFirstText = (children: HastLikeNode[] | undefined): boolean => {
    if (!children) return false;
    for (const child of children) {
      if (child.type === "text") {
        child.value = value;
        return true;
      }
      if (updateFirstText(child.children)) return true;
    }
    return false;
  };
  updateFirstText(hChildren);
}

/**
 * Semantic policy layered after remark-math has parsed Markdown boundaries.
 * Code spans/fences never become math nodes, so this plugin only decides how
 * already-parsed inline math should render.
 */
export function remarkMathPolicy() {
  return (tree: Root, file: VFileLike) => {
    visit(tree, "inlineMath", (node, index, parent) => {
      if (typeof index !== "number" || !parent) return;

      const typedNode = node as InlineMath;
      const typedParent = parent as ParentWithChildren;
      const { source, rendered } = inlineMathSources(typedNode, file);
      const decision = classifyInlineMath(source.trim());

      if (decision === "math") {
        setMathValue(typedNode, source, latexNormalizeForKatex(rendered));
        return;
      }

      const value = originalSource(typedNode, file);
      typedParent.children[index] = { type: "text", value } satisfies Text;
    });

    visit(tree, "math", (node) => {
      const { source, rendered } = resolveProtectedInlineMathSource(node.value);
      setMathValue(node, source, latexNormalizeForKatex(rendered));
    });
  };
}
