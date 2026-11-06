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
    await act(async () => retry.resolve({ path: "/repo", parent: "/", entries: [], roots: [] }));
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
  const listDir = vi.fn(async (path) => ({ path, parent: "/", entries: [], roots: [] }));
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
    .mockResolvedValueOnce({ path: "/old", parent: "/", entries: [], roots: [] })
    .mockReturnValueOnce(failed.promise)
    .mockResolvedValue({ path: "/", parent: "/", entries: [], roots: [] });
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

it.each([
  ["/home/user", "/home/user/child"],
  ["C:\\Users\\user", "C:\\Users\\user\\child"],
])("uses the resolved default directory %s for selection and creation", async (path, child) => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const listDir = vi.fn(async (requested) => ({ path: requested || path, parent: "", entries: [], roots: [] }));
  const mkdir = vi.fn().mockResolvedValue(undefined);
  const onDirectoryChange = vi.fn();
  try {
    await act(async () => root.render(createElement(RemotePathBrowser, {
      initialPath: "", allowCreateFolder: true, listDir, mkdir, onDirectoryChange, t: key => key,
    })));
    expect(listDir).toHaveBeenCalledWith("");
    expect(onDirectoryChange).toHaveBeenLastCalledWith(path);
    expect([...host.querySelectorAll("button")].some(button => button.textContent === "user")).toBe(true);
    await act(async () => host.querySelector('[aria-label="agent.newFolder"]').click());
    const input = host.querySelector('[aria-label="agent.folderName"]');
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(input, "child");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => host.querySelector('button[aria-label="agent.createFolder"]').click());
    expect(mkdir).toHaveBeenCalledExactlyOnceWith(child);
    expect(listDir).toHaveBeenLastCalledWith(child);
    expect(onDirectoryChange).toHaveBeenLastCalledWith(child);
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});

it("does not allow selection or creation without a resolved directory", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const onDirectoryChange = vi.fn();
  try {
    await act(async () => root.render(createElement(RemotePathBrowser, {
      initialPath: "", allowCreateFolder: true,
      listDir: async () => ({ path: "", parent: "", entries: [], roots: [] }),
      mkdir: vi.fn(), onDirectoryChange, t: key => key,
    })));
    expect(onDirectoryChange).toHaveBeenCalledExactlyOnceWith(null);
    expect(host.querySelector('[aria-label="agent.newFolder"]').disabled).toBe(true);
    expect(host.querySelector('[role="alert"]').textContent).toBe("agent.pathLoadFailed");
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});

it.each([
  ["/missing/project", "/missing"],
  ["D:\\missing\\project", "D:\\missing"],
  ["/", null],
  ["D:\\", null],
])("uses the failed path's own parent when recovering from %s", async (path, parent) => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const listDir = vi.fn()
    .mockResolvedValueOnce({ path: "/old/repo", parent: "/old", entries: [], roots: [] })
    .mockRejectedValueOnce(new Error("not found"))
    .mockImplementation(async requested => ({ path: requested, parent: "", entries: [], roots: [] }));
  const onDirectoryChange = vi.fn();
  try {
    await act(async () => root.render(createElement(RemotePathBrowser, {
      initialPath: "/old/repo", allowCreateFolder: true, listDir, mkdir: vi.fn(), onDirectoryChange, t: key => key,
    })));
    await act(async () => host.querySelector('[aria-label="agent.editPath"]').click());
    const input = host.querySelector('[aria-label="agent.projectPath"]');
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(input, path);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => host.querySelector('[aria-label="agent.goToPath"]').click());
    expect(host.querySelector('[role="alert"]').textContent).toBe("agent.pathLoadFailed");
    expect(onDirectoryChange).toHaveBeenLastCalledWith(null);
    const up = host.querySelector('[aria-label="agent.goToParentFolder"]');
    expect(up.disabled).toBe(parent === null);
    await act(async () => up.click());
    expect(listDir.mock.calls).toEqual(parent ? [["/old/repo"], [path], [parent]] : [["/old/repo"], [path]]);
    if (parent) {
      expect(onDirectoryChange).toHaveBeenLastCalledWith(parent);
      expect(host.querySelector('[role="alert"]')).toBeNull();
    }
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
