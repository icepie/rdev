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
    await act(async () => snapshots.left[1](""));
    expect(snapshots.mirror[0]).toBe("");
    expect(sessionStorage.getItem('rdev-composer-text:["a","thread"]')).toBeNull();
    expect(snapshots.right[0]).toBe("right text");
  } finally {
    await act(async () => root.unmount());
    sessionStorage.clear();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
