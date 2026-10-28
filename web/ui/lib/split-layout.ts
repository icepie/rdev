/* Port of bb's split-layout lib (apps/app/src/lib/split-layout), trimmed to
 * rdev's needs: a pane holds a thread id (or null = the connection's current
 * thread), there are no plugin panes, and persistence is plain localStorage
 * JSON instead of jotai atoms. Ops are pure: every function returns a new
 * layout. */

export interface PaneNode {
  type: "pane";
  paneId: string;
  /* null = follow the connection's current thread (the primary pane). */
  threadId: string | null;
}

export interface SplitNode {
  type: "split";
  dir: "row" | "col";
  /* Child fractions, same length as children, sums to 1. */
  sizes: number[];
  children: LayoutNode[];
}

export type LayoutNode = PaneNode | SplitNode;

export interface SplitLayout {
  root: LayoutNode;
  focusedPaneId: string;
}

export type SplitSide = "left" | "right" | "top" | "bottom";
export type SplitPath = readonly number[];

export const MAX_PANES = 8;

const MIN_SIZE = 0.15;
const MAX_SIZE = 0.85;

export function clampSplitPairFraction(fraction: number): number {
  return Math.min(MAX_SIZE, Math.max(MIN_SIZE, fraction));
}

export function singlePaneLayout(threadId: string | null = null): SplitLayout {
  return { root: { type: "pane", paneId: "p1", threadId }, focusedPaneId: "p1" };
}

export function listPanes(root: LayoutNode): PaneNode[] {
  if (root.type === "pane") return [root];
  return root.children.flatMap(listPanes);
}

export function countPanes(root: LayoutNode): number {
  return root.type === "pane" ? 1 : root.children.reduce((n, c) => n + countPanes(c), 0);
}

export function findPane(root: LayoutNode, paneId: string): PaneNode | null {
  if (root.type === "pane") return root.paneId === paneId ? root : null;
  for (const c of root.children) {
    const hit = findPane(c, paneId);
    if (hit) return hit;
  }
  return null;
}

export function findPaneByThread(root: LayoutNode, threadId: string): PaneNode | null {
  for (const p of listPanes(root)) {
    if (p.threadId === threadId) return p;
  }
  return null;
}

function replacePaneNode(node: LayoutNode, paneId: string, replacement: LayoutNode): LayoutNode {
  if (node.type === "pane") return node.paneId === paneId ? replacement : node;
  return { ...node, children: node.children.map((c) => replacePaneNode(c, paneId, replacement)) };
}

function nextPaneId(root: LayoutNode): string {
  let max = 0;
  for (const p of listPanes(root)) {
    const m = /^p(\d+)$/.exec(p.paneId);
    if (m) max = Math.max(max, parseInt(m[1], 10));
  }
  return "p" + (max + 1);
}

function splitDirection(side: SplitSide): SplitNode["dir"] {
  return side === "left" || side === "right" ? "row" : "col";
}

function insertPane(root: LayoutNode, targetPaneId: string, side: SplitSide, pane: PaneNode): LayoutNode {
  const dir = splitDirection(side);
  const first = side === "left" || side === "top";
  const wrap = (target: LayoutNode): LayoutNode => {
    if (target.type === "split" && target.dir === dir) {
      const idx = target.children.findIndex((c) => c.type === "pane" && c.paneId === targetPaneId);
      if (idx >= 0) {
        const share = target.sizes[idx] / 2;
        const sizes = [...target.sizes];
        sizes[idx] = share;
        sizes.splice(first ? idx : idx + 1, 0, share);
        const children = [...target.children];
        children.splice(first ? idx : idx + 1, 0, pane);
        return { ...target, sizes, children };
      }
    }
    return {
      type: "split",
      dir,
      sizes: [0.5, 0.5],
      children: first ? [pane, target] : [target, pane],
    };
  };
  return replacePaneNode(root, targetPaneId, wrap(findPane(root, targetPaneId) ?? root));
}

export function splitPane(layout: SplitLayout, targetPaneId: string, side: SplitSide, threadId: string | null): SplitLayout {
  if (countPanes(layout.root) >= MAX_PANES) return layout;
  if (!findPane(layout.root, targetPaneId)) return layout;
  const pane: PaneNode = { type: "pane", paneId: nextPaneId(layout.root), threadId };
  return { root: insertPane(layout.root, targetPaneId, side, pane), focusedPaneId: pane.paneId };
}

export function replacePaneThread(layout: SplitLayout, paneId: string, threadId: string | null): SplitLayout {
  if (!findPane(layout.root, paneId)) return layout;
  return {
    root: replacePaneNode(layout.root, paneId, { type: "pane", paneId, threadId }),
    focusedPaneId: paneId,
  };
}

interface DetachResult {
  node: LayoutNode | null;
  detached: PaneNode | null;
}

function detachPane(node: LayoutNode, paneId: string): DetachResult {
  if (node.type === "pane") {
    return node.paneId === paneId ? { node: null, detached: node } : { node, detached: null };
  }
  const children: LayoutNode[] = [];
  const sizes: number[] = [];
  let detached: PaneNode | null = null;
  let removedSize = 0;
  node.children.forEach((c, i) => {
    const r = detachPane(c, paneId);
    if (r.detached) {
      detached = r.detached;
      removedSize = node.sizes[i] ?? 0;
    }
    if (r.node) {
      children.push(r.node);
      sizes.push(node.sizes[i] ?? 1 / node.children.length);
    }
  });
  if (children.length === 0) return { node: null, detached };
  if (children.length === 1) return { node: children[0], detached };
  /* Redistribute the removed child's share proportionally. */
  const rest = sizes.reduce((a, b) => a + b, 0);
  const norm = rest > 0 ? sizes.map((s) => s / rest) : sizes.map(() => 1 / sizes.length);
  void removedSize;
  return { node: { ...node, children, sizes: norm }, detached };
}

export function removePane(layout: SplitLayout, paneId: string): SplitLayout {
  const { node, detached } = detachPane(layout.root, paneId);
  if (!detached || !node) return layout; /* never remove the last pane */
  const panes = listPanes(node);
  const focusedPaneId =
    layout.focusedPaneId === paneId ? (panes[0]?.paneId ?? paneId) : layout.focusedPaneId;
  return { root: node, focusedPaneId };
}

function equalSizes(count: number): number[] {
  return Array.from({ length: count }, () => 1 / count);
}

function normalizeSizes(sizes: readonly number[], childCount: number): number[] {
  if (sizes.length !== childCount || sizes.some((s) => !(s > 0))) return equalSizes(childCount);
  const sum = sizes.reduce((a, b) => a + b, 0);
  if (Math.abs(sum - 1) > 1e-6) return sizes.map((s) => s / sum);
  return [...sizes];
}

function updateSplitAtPath(
  node: LayoutNode,
  splitPath: SplitPath,
  pathIndex: number,
  update: (split: SplitNode) => SplitNode | null,
): LayoutNode | null {
  if (pathIndex === splitPath.length) {
    if (node.type !== "split") return node;
    return update({ ...node, sizes: normalizeSizes(node.sizes, node.children.length) });
  }
  if (node.type !== "split") return node;
  const childIdx = splitPath[pathIndex];
  if (childIdx < 0 || childIdx >= node.children.length) return node;
  const next = updateSplitAtPath(node.children[childIdx], splitPath, pathIndex + 1, update);
  if (next === null) {
    /* Child collapsed: drop it and normalize. */
    const children = node.children.filter((_, i) => i !== childIdx);
    if (children.length === 0) return null;
    if (children.length === 1) return children[0];
    const sizes = node.sizes.filter((_, i) => i !== childIdx);
    return { ...node, children, sizes: normalizeSizes(sizes, children.length) };
  }
  const children = [...node.children];
  children[childIdx] = next;
  return { ...node, children, sizes: normalizeSizes(node.sizes, children.length) };
}

/* resizeSplit adjusts the pair (childIndex, childIndex+1) of the split at
 * splitPath so the first takes `fraction` of their combined share. */
export function resizeSplit(
  layout: SplitLayout,
  splitPath: SplitPath,
  childIndex: number,
  fraction: number,
): SplitLayout {
  const root = updateSplitAtPath(layout.root, splitPath, 0, (split) => {
    if (childIndex < 0 || childIndex + 1 >= split.children.length) return split;
    const a = split.sizes[childIndex];
    const b = split.sizes[childIndex + 1];
    const pair = a + b;
    if (pair <= 0) return split;
    const na = pair * clampSplitPairFraction(fraction);
    const sizes = [...split.sizes];
    sizes[childIndex] = na;
    sizes[childIndex + 1] = pair - na;
    return { ...split, sizes };
  });
  return root ? { ...layout, root } : layout;
}

export function setFocus(layout: SplitLayout, paneId: string): SplitLayout {
  if (!findPane(layout.root, paneId)) return layout;
  return { ...layout, focusedPaneId: paneId };
}

/* ── persistence ── */

export function serializeLayout(layout: SplitLayout): string {
  return JSON.stringify(layout);
}

export function parseLayout(raw: string | null): SplitLayout | null {
  if (!raw) return null;
  try {
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null) return null;
    if (!("root" in parsed) || !("focusedPaneId" in parsed)) return null;
    const { root, focusedPaneId } = parsed;
    if (typeof focusedPaneId !== "string" || !validNode(root)) return null;
    const layout: SplitLayout = { root, focusedPaneId };
    if (!findPane(root, focusedPaneId)) {
      const first = listPanes(root)[0];
      if (!first) return null;
      return { root, focusedPaneId: first.paneId };
    }
    return layout;
  } catch {
    return null;
  }
}

function validNode(node: unknown): node is LayoutNode {
  if (typeof node !== "object" || node === null || !("type" in node)) return false;
  if (node.type === "pane") {
    return (
      "paneId" in node &&
      typeof node.paneId === "string" &&
      "threadId" in node &&
      (typeof node.threadId === "string" || node.threadId === null)
    );
  }
  if (node.type === "split") {
    if (!("dir" in node) || (node.dir !== "row" && node.dir !== "col")) return false;
    if (!("sizes" in node) || !Array.isArray(node.sizes)) return false;
    if (!("children" in node) || !Array.isArray(node.children)) return false;
    return (
      node.sizes.length === node.children.length &&
      node.sizes.every((s) => typeof s === "number") &&
      node.children.every(validNode)
    );
  }
  return false;
}
