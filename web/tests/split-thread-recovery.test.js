import { expect, it } from "vitest";
import { singlePaneLayout, splitPane, listPanes, reconcileThreadPanes } from "../ui/lib/split-layout";

it("removes a stale duplicate while retaining the current and distinct conversations", () => {
  let layout = splitPane(singlePaneLayout(), "p1", "right", "old");
  layout = splitPane(layout, "p2", "bottom", "other");
  const next = reconcileThreadPanes(layout, "old", new Set(["old", "other"]));
  expect(listPanes(next.root).map(p => p.threadId)).toEqual([null, "other"]);
  expect(reconcileThreadPanes(next, "old", new Set(["old", "other"]))).toBe(next);
  const removed = reconcileThreadPanes(next, "old", new Set(["old"]));
  expect(listPanes(removed.root).map(p => p.threadId)).toEqual([null]);
});
