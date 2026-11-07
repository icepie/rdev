// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";

it("keeps drafts isolated, synchronizes matching panes, and restores after module reload", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  sessionStorage.clear();
  let hook = (await import("../ui/components/agent/use-composer-text-draft")).useComposerTextDraft;
  const snapshots = {};
  function Pane({ name, identity }) {
    snapshots[name] = hook(identity);
    return null;
  }
  let root = createRoot(document.createElement("div"));
  const show = () => act(async () => root.render(createElement("div", null,
    createElement(Pane, { name: "left", identity: '["a","thread"]' }),
    createElement(Pane, { name: "mirror", identity: '["a","thread"]' }),
    createElement(Pane, { name: "right", identity: '["b","thread"]' }))));
  try {
    await show();
    await act(async () => snapshots.left[1]("left text"));
    await act(async () => snapshots.right[1]("right text"));
    expect(snapshots.mirror[0]).toBe("left text");
    expect(snapshots.right[0]).toBe("right text");
    await act(async () => root.unmount());
    vi.resetModules();
    hook = (await import("../ui/components/agent/use-composer-text-draft")).useComposerTextDraft;
    root = createRoot(document.createElement("div"));
    await show();
    expect(snapshots.left[0]).toBe("left text");
    expect(snapshots.right[0]).toBe("right text");
    const { discardComposerTextDraft } = await import("../ui/components/agent/use-composer-text-draft");
    await act(async () => discardComposerTextDraft('["a","thread"]'));
    expect(snapshots.mirror[0]).toBe("");
    expect(sessionStorage.getItem('rdev-composer-text:["a","thread"]')).toBeNull();
    expect(snapshots.right[0]).toBe("right text");
    await act(async () => root.unmount());
    vi.resetModules();
    hook = (await import("../ui/components/agent/use-composer-text-draft")).useComposerTextDraft;
    root = createRoot(document.createElement("div"));
    await show();
    expect(snapshots.left[0]).toBe("");
    expect(snapshots.right[0]).toBe("right text");
  } finally {
    await act(async () => root.unmount());
    sessionStorage.clear();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});

it("reconciles missed deletions only against a complete authenticated device list", async () => {
  vi.resetModules();
  sessionStorage.clear();
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const { useComposerTextDraft, reconcileComposerTextDrafts } = await import("../ui/components/agent/use-composer-text-draft");
  const prefix = "rdev-composer-text:";
  const key = (device, thread) => JSON.stringify([device, thread]);
  const initial = new Map([
    [key("a", "gone"), "deleted while offline"], [key("a", "unopened"), "storage only"],
    [key("a", "keep"), "keep draft"], [key("b", "gone"), "other device"],
    [key("a", ""), "not yet assigned"], ["bad-json", "legacy"],
  ]);
  for (const [identity, value] of initial) sessionStorage.setItem(prefix + identity, value);
  sessionStorage.setItem("unrelated", "keep");
  let current;
  function Pane() { current = useComposerTextDraft(key("a", "gone")); return null; }
  const root = createRoot(document.createElement("div"));
  try {
    await act(async () => root.render(createElement(Pane)));
    for (const threads of [undefined, null, [], [{ id: "other" }], [{ id: "keep" }, {}], [{ id: "keep" }, { id: 12 }]]) {
      await act(async () => reconcileComposerTextDrafts("a", "keep", threads));
      expect(current[0]).toBe("deleted while offline");
      expect(sessionStorage.getItem(prefix + key("a", "unopened"))).toBe("storage only");
    }
    await act(async () => reconcileComposerTextDrafts("a", "keep", [{ id: "keep" }]));
    expect(current[0]).toBe("");
    expect(sessionStorage.getItem(prefix + key("a", "gone"))).toBeNull();
    expect(sessionStorage.getItem(prefix + key("a", "unopened"))).toBeNull();
    for (const identity of [key("a", "keep"), key("b", "gone"), key("a", ""), "bad-json"]) {
      expect(sessionStorage.getItem(prefix + identity)).toBe(initial.get(identity));
    }
    expect(sessionStorage.getItem("unrelated")).toBe("keep");
  } finally {
    await act(async () => root.unmount());
    sessionStorage.clear();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
