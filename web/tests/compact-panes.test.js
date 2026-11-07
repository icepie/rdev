// @vitest-environment jsdom
import { act, createElement, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
vi.mock("../ui/components/agent/chat-pane", () => ({ ChatPaneView: () => null }));
import { SplitContainer } from "../ui/components/agent/split-panes";

it("switches accessible mobile tabs without remounting panes across breakpoints", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const mounts = vi.fn();
  const layout = { type: "split", dir: "row", sizes: [0.4, 0.6], children: [
    { type: "pane", paneId: "left", threadId: null },
    { type: "pane", paneId: "right", threadId: "thread" },
  ] };
  function Pane({ id }) {
    const [value, setValue] = useState(0);
    useEffect(() => { mounts(id); }, []);
    return createElement("button", { onClick: () => setValue(v => v + 1) }, `${id}:${value}`);
  }
  function App({ narrow }) {
    const [activePaneId, onSelect] = useState("left");
    return createElement(SplitContainer, {
      root: layout, onResize: vi.fn(), renderPane: p => createElement(Pane, { id: p.paneId }),
      compact: narrow ? { activePaneId, onSelect, label: "Conversations", labelForPane: p => p.paneId } : undefined,
    });
  }
  const show = narrow => act(async () => root.render(createElement(App, { narrow })));
  const pane = id => host.querySelector(`[data-pane-id="${id}"]`);
  try {
    await show(true);
    expect(pane("left").hidden).toBe(false);
    expect(pane("right").hasAttribute("inert")).toBe(true);
    expect(host.querySelector('[role="separator"]').hidden).toBe(true);
    await act(async () => pane("left").querySelector("button").click());
    const tabs = host.querySelectorAll('[role="tab"]');
    expect(document.getElementById(tabs[0].getAttribute("aria-controls"))).toBe(pane("left"));
    await act(async () => tabs[0].dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true })));
    expect(document.activeElement).toBe(tabs[1]);
    expect(tabs[1].getAttribute("aria-selected")).toBe("true");
    expect(pane("left").hidden).toBe(true);
    expect(pane("right").hidden).toBe(false);
    await show(false);
    expect(host.querySelector('[role="tablist"]')).toBeNull();
    expect(pane("left").hidden).toBe(false);
    expect(pane("right").hasAttribute("inert")).toBe(false);
    expect(host.querySelector('[role="separator"]').hidden).toBe(false);
    expect(pane("left").parentElement.style.flexGrow).toBe("0.4");
    await show(true);
    await act(async () => host.querySelectorAll('[role="tab"]')[0].click());
    expect(pane("left").textContent).toBe("left:1");
    expect(mounts).toHaveBeenCalledTimes(2);
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
