/* Port of bb's split-drag zones (apps/app/src/lib/split-drag/zones.ts):
 * edge-band zone picking + highlight boxes for thread drags over panes. */

import type { SplitSide } from "./split-layout";

export type SplitZone = SplitSide | "center";

export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

const EDGE_X_FRACTION = 0.28;
const EDGE_Y_FRACTION = 0.3;
const ZONE_MARGIN_PX = 5;

/* Distance the pointer must travel before a sidebar press becomes a drag. */
export const ENGAGE_DISTANCE_PX = 8;

export function pickZone(rect: Rect, clientX: number, clientY: number): SplitZone {
  const fx = rect.width > 0 ? (clientX - rect.x) / rect.width : 0.5;
  const fy = rect.height > 0 ? (clientY - rect.y) / rect.height : 0.5;
  if (fx < EDGE_X_FRACTION) return "left";
  if (fx > 1 - EDGE_X_FRACTION) return "right";
  if (fy < EDGE_Y_FRACTION) return "top";
  if (fy > 1 - EDGE_Y_FRACTION) return "bottom";
  return "center";
}

/* zoneBox returns the highlight rectangle for a zone inside a pane rect. */
export function zoneBox(rect: Rect, zone: SplitZone): Rect {
  const m = ZONE_MARGIN_PX;
  const inner: Rect = {
    x: rect.x + m,
    y: rect.y + m,
    width: Math.max(0, rect.width - 2 * m),
    height: Math.max(0, rect.height - 2 * m),
  };
  switch (zone) {
    case "left":
      return { ...inner, width: inner.width / 2 };
    case "right":
      return { ...inner, x: inner.x + inner.width / 2, width: inner.width / 2 };
    case "top":
      return { ...inner, height: inner.height / 2 };
    case "bottom":
      return { ...inner, y: inner.y + inner.height / 2, height: inner.height / 2 };
    case "center":
      return inner;
  }
}

export interface ThreadDropInput {
  zone: SplitZone;
  threadAlreadyOpen: boolean;
  atMaxPanes: boolean;
}

export type ZoneDecision =
  | { kind: "replace" }
  | { kind: "split"; side: SplitSide }
  | { kind: "focus" }
  | { kind: "noop" };

/* bb decideThreadDrop: center replaces the target pane; edges split unless
 * the layout is full (then replace); an already-open thread just focuses its
 * existing pane. */
export function decideThreadDrop({ zone, threadAlreadyOpen, atMaxPanes }: ThreadDropInput): ZoneDecision {
  if (threadAlreadyOpen) return { kind: "focus" };
  if (zone === "center") return { kind: "replace" };
  if (atMaxPanes) return { kind: "replace" };
  return { kind: "split", side: zone };
}
