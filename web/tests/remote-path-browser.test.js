// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
import { RemotePathBrowser } from "../ui/components/agent/remote-path-browser";

it("keeps failed paths unselectable and restores creation only after a successful retry", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const retry = Promise.withResolvers();
  const listDir = vi.fn().mockRejectedValueOnce(new Error("offline")).mockReturnValueOnce(retry.promise);
  const onDirectoryChange = vi.fn();
  const props = { initialPath: "/repo", allowCreateFolder: true, mkdir: vi.fn(), listDir, onDirectoryChange, t: key => key };
  try {
    await act(async () => root.render(createElement(RemotePathBrowser, props)));
    expect(host.querySelectorAll('[role="alert"]')).toHaveLength(1);
    expect(host.querySelector('[aria-label="agent.newFolder"]').disabled).toBe(true);
    expect(onDirectoryChange).toHaveBeenLastCalledWith(null);
    const retryButton = [...host.querySelectorAll("button")].find(button => button.textContent === "agent.retry");
    await act(async () => retryButton.click());
    expect(listDir.mock.calls).toEqual([["/repo"], ["/repo"]]);
    expect(host.querySelector('[aria-label="agent.newFolder"]').disabled).toBe(true);
    expect(host.querySelector('[role="alert"]')).toBeNull();
    await act(async () => retry.resolve({ parent: "/", entries: [], roots: [] }));
    expect(host.querySelector('[aria-label="agent.newFolder"]').disabled).toBe(false);
    expect(onDirectoryChange).toHaveBeenLastCalledWith("/repo");
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});

it.each(["resolve", "reject"])("ignores stale folder creation %s after a directory switch", async (outcome) => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const creation = Promise.withResolvers();
  const mkdir = vi.fn(() => creation.promise);
  const listDir = vi.fn(async () => ({ parent: "/", entries: [], roots: [] }));
  const onDirectoryChange = vi.fn();
  const props = { initialPath: "/old", allowCreateFolder: true, mkdir, listDir, onDirectoryChange, t: key => key };
  const render = () => act(async () => root.render(createElement(RemotePathBrowser, props)));
  try {
    await render();
    await act(async () => host.querySelector('[aria-label="agent.newFolder"]').click());
    const input = host.querySelector('input[aria-label="agent.folderName"]');
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(input, "child");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => host.querySelector('button[aria-label="agent.createFolder"]').click());
    expect(mkdir).toHaveBeenCalledWith("/old/child");
    props.initialPath = "/new";
    await render();
    await act(async () => creation[outcome](new Error("old request")));
    expect(listDir.mock.calls.map(([path]) => path)).toEqual(["/old", "/new"]);
    expect(onDirectoryChange).toHaveBeenLastCalledWith("/new");
    expect(host.querySelector('[role="alert"]')).toBeNull();
    expect(host.querySelector('[aria-label="agent.newFolder"]').disabled).toBe(false);
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});

it("keeps retry accessible when navigation fails with a folder draft open", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const failed = Promise.withResolvers();
  const listDir = vi.fn()
    .mockResolvedValueOnce({ parent: "/", entries: [], roots: [] })
    .mockReturnValueOnce(failed.promise)
    .mockResolvedValue({ parent: "/", entries: [], roots: [] });
  const props = { initialPath: "/old", allowCreateFolder: true, mkdir: vi.fn(), listDir, onDirectoryChange: vi.fn(), t: key => key };
  try {
    await act(async () => root.render(createElement(RemotePathBrowser, props)));
    await act(async () => host.querySelector('[aria-label="agent.newFolder"]').click());
    const input = host.querySelector('[aria-label="agent.folderName"]');
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(input, "draft");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => host.querySelector('[aria-label="agent.goToParentFolder"]').click());
    expect(host.querySelector('[aria-label="agent.folderName"]')).toBeNull();
    await act(async () => failed.reject(new Error("offline")));
    expect(host.querySelector('[role="alert"]').textContent).toBe("agent.pathLoadFailed");
    expect(host.querySelector('[aria-label="agent.newFolder"]').disabled).toBe(true);
    expect(props.mkdir).not.toHaveBeenCalled();
    expect(props.onDirectoryChange).toHaveBeenLastCalledWith(null);
    const retryButton = [...host.querySelectorAll("button")].find(button => button.textContent === "agent.retry");
    await act(async () => retryButton.click());
    expect(listDir.mock.calls).toEqual([["/old"], ["/"], ["/"]]);
    expect(props.onDirectoryChange).toHaveBeenLastCalledWith("/");
    await act(async () => host.querySelector('[aria-label="agent.newFolder"]').click());
    expect(host.querySelector('[aria-label="agent.folderName"]').value).toBe("");
    expect(host.querySelector('button[aria-label="agent.createFolder"]').disabled).toBe(true);
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
