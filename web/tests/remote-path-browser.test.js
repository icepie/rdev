// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
import { RemotePathBrowser } from "../ui/components/agent/remote-path-browser";

it("does not navigate back when folder creation finishes after a directory switch", async () => {
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
    await act(async () => creation.resolve());
    expect(listDir.mock.calls.map(([path]) => path)).toEqual(["/old", "/new"]);
    expect(onDirectoryChange).toHaveBeenLastCalledWith("/new");
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
