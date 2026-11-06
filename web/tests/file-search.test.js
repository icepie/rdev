// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
vi.mock("../ui/components/agent/panel-terminal", () => ({ PanelTerminal: () => null }));
vi.mock("../ui/components/agent/panel-desktop", () => ({ PanelDesktop: () => null }));
vi.mock("../ui/components/agent/panel-files", () => ({ PanelFiles: () => null }));
vi.mock("../ui/components/agent/diff-view", () => ({ DIFF_THEME: {}, DIFF_VIEW_STYLE: {}, useThemeType: () => "dark", PierreFileDiff: () => null }));
vi.mock("@pierre/diffs/react", () => ({ File: () => null, MultiFileDiff: () => null }));
import { NewTabPage } from "../ui/components/agent/right-panel";

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
