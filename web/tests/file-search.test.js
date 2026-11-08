// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
vi.mock("../ui/components/agent/panel-terminal", () => ({ PanelTerminal: () => null }));
vi.mock("../ui/components/agent/panel-desktop", () => ({ PanelDesktop: ({visible}) => createElement("div", {"data-desktop-visible": String(visible)}) }));
vi.mock("../ui/components/agent/panel-files", () => ({ PanelFiles: () => null }));
vi.mock("../ui/components/agent/diff-view", () => ({ DIFF_THEME: {}, DIFF_VIEW_STYLE: {}, useThemeType: () => "dark", PierreFileDiff: () => null }));
vi.mock("@pierre/diffs/react", () => ({ File: () => null, MultiFileDiff: () => null }));
import { TooltipProvider } from "../ui/components/tooltip";
import { AgentRightPanel, NewTabPage } from "../ui/components/agent/right-panel";

it("clears obsolete search results, ignores stale replies, and recovers after failure", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers();
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const old = Promise.withResolvers();
  const search = vi.fn().mockResolvedValueOnce(["first.ts"]).mockReturnValueOnce(old.promise)
    .mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce(["latest.ts"]);
  const open = vi.fn();
  const noop = () => {};
  try {
    await act(async () => root.render(createElement(NewTabPage, {
      recent: [], onOpenChanges: noop, onOpenInfo: noop, onOpenTerminal: noop,
      onOpenFiles: noop, onOpenDesktop: noop, onOpenFile: open, onSearchFiles: search, t: key => key,
    })));
    const input = host.querySelector("input");
    const edit = value => act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const searchNow = () => act(async () => vi.advanceTimersByTimeAsync(250));
    await edit("first"); await searchNow();
    expect(host.querySelector('[title="first.ts"]')).not.toBeNull();
    await edit("old");
    expect(host.querySelector('[title="first.ts"]')).toBeNull();
    await searchNow();
    await edit("latest"); await searchNow();
    expect(host.querySelector('[role="alert"]').textContent).toBe("agent.searchFailed");
    await act(async () => old.resolve(["obsolete.ts"]));
    expect(host.querySelector('[title="obsolete.ts"]')).toBeNull();
    await act(async () => [...host.querySelectorAll("button")].find(b => b.textContent === "agent.retry").click());
    await searchNow();
    expect(search.mock.calls).toEqual([["first"], ["old"], ["latest"], ["latest"]]);
    await act(async () => input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    expect(open).toHaveBeenCalledExactlyOnceWith("latest.ts");
  } finally {
    await act(async () => root.unmount());
    host.remove();
    vi.useRealTimers();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});

it("exposes search results as an accessible combobox and keeps the active result valid", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers();
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const search = vi.fn().mockResolvedValue(["one.ts", "two.ts", "three.ts"]);
  const originalScroll = Element.prototype.scrollIntoView;
  try {
    await act(async () => root.render(createElement(NewTabPage, {
      recent: [], onOpenChanges: () => {}, onOpenInfo: () => {}, onOpenTerminal: () => {},
      onOpenFiles: () => {}, onOpenDesktop: () => {}, onOpenFile: () => {}, onSearchFiles: search, t: key => key,
    })));
    const input = host.querySelector("input");
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    expect(input.getAttribute("role")).toBe("combobox");
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(input, "file");
      input.dispatchEvent(new Event("input", { bubbles: true }));
      await vi.advanceTimersByTimeAsync(250);
    });
    expect(host.querySelector('[role="listbox"]')).not.toBeNull();
    expect(host.querySelectorAll('[role="option"]')).toHaveLength(3);
    expect(input.getAttribute("aria-activedescendant")).toBe(`${input.getAttribute("aria-controls")}-0`);
    await act(async () => input.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true })));
    expect(input.getAttribute("aria-activedescendant")).toBe(`${input.getAttribute("aria-controls")}-1`);
    expect(scroll).toHaveBeenLastCalledWith({ block: "nearest" });
    expect(scroll.mock.instances.at(-1).id).toBe(input.getAttribute("aria-activedescendant"));
    expect(host.querySelectorAll('[role="option"][tabindex="-1"]')).toHaveLength(3);
    await act(async () => input.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true })));
    await act(async () => input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(input.value).toBe("");
    expect(host.querySelector("[role=\"listbox\"]")).toBeNull();
  } finally {
    await act(async () => root.unmount());
    host.remove();
    if (originalScroll) Element.prototype.scrollIntoView = originalScroll;
    else delete Element.prototype.scrollIntoView;
    vi.useRealTimers();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});

it("resets the search page when its thread key changes", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const props = { recent: [], onOpenChanges: () => {}, onOpenInfo: () => {}, onOpenTerminal: () => {}, onOpenFiles: () => {}, onOpenDesktop: () => {}, onOpenFile: () => {}, onSearchFiles: vi.fn().mockResolvedValue([]), t: key => key };
  try {
    await act(async () => root.render(createElement(NewTabPage, { ...props, key: "thread-a" })));
    const input = host.querySelector("input");
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(input, "old-thread");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(input.value).toBe("old-thread");
    await act(async () => root.render(createElement(NewTabPage, { ...props, key: "thread-b" })));
    expect(host.querySelector("input").value).toBe("");
    expect(host.querySelector('[role="listbox"]')).toBeNull();
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});


it("resets project search on a confirmed cwd change and ignores the previous project's reply", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers();
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const pending = Promise.withResolvers();
  const search = vi.fn().mockReturnValueOnce(pending.promise).mockResolvedValue(["new-project.ts"]);
  const noop = () => {};
  const props = { changes: null, diffs: {}, connected: true, threadKey: "device:cwd-test",
    fileStates: {}, onRefresh: noop, onRequestDiff: noop, onRequestFile: noop,
    onSearchFiles: search, onAddToChat: noop, onHide: noop };
  const render = cwd => act(async () => root.render(createElement(TooltipProvider, null, createElement(AgentRightPanel, {
    ...props, info: { device: "device", thread: "cwd-test", cwd },
  }))));
  const edit = value => act(async () => {
    const input = host.querySelector("input");
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  try {
    await render("/old");
    await act(async () => host.querySelector('[aria-label="agent.openNewTab"]').click());
    await edit("file");
    await act(async () => vi.advanceTimersByTimeAsync(250));
    await render("/new");
    expect(host.querySelector("input").value).toBe("");
    await act(async () => pending.resolve(["old-project.ts"]));
    expect(host.querySelector('[title="old-project.ts"]')).toBeNull();
    await edit("file");
    await act(async () => vi.advanceTimersByTimeAsync(250));
    expect(host.querySelector('[title="new-project.ts"]')).not.toBeNull();
  } finally {
    await act(async () => root.unmount());
    host.remove();
    localStorage.clear();
    vi.unstubAllGlobals();
    vi.useRealTimers();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});


it("deactivates a persistent desktop tab when its containing panel closes", async () => {
 globalThis.IS_REACT_ACT_ENVIRONMENT = true;
 vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
 const key="device:visibility-test";
 localStorage.setItem("rdev.agent.panel.tabs",JSON.stringify({[key]:{tabs:[{kind:"desktop"}],active:{kind:"desktop"},recent:[]}}));
 const host=document.createElement("div");document.body.append(host);const root=createRoot(host);
 const noop=()=>{};
 const render=visible=>act(async()=>root.render(createElement(TooltipProvider,null,createElement(AgentRightPanel,{
  visible,changes:null,diffs:{},connected:true,threadKey:key,info:{device:"device",thread:"visibility-test",cwd:"/repo"},
  fileStates:{},onRefresh:noop,onRequestDiff:noop,onRequestFile:noop,onSearchFiles:async()=>[],onAddToChat:noop,onHide:noop,
 }))));
 try {
  await render(true);
  const desktop=host.querySelector('[data-desktop-visible]');
  expect(desktop.dataset.desktopVisible).toBe('true');
  await render(false);
  expect(host.querySelector('[data-desktop-visible]')).toBe(desktop);
  expect(desktop.dataset.desktopVisible).toBe('false');
  await render(true);
  expect(desktop.dataset.desktopVisible).toBe('true');
 } finally {await act(async()=>root.unmount());host.remove();localStorage.clear();vi.unstubAllGlobals();delete globalThis.IS_REACT_ACT_ENVIRONMENT;}
});
