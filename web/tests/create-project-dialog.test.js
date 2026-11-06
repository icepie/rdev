// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
// Keep the form and directory browser real; omit overlay positioning only.
vi.mock("../ui/components/dialog", () => {
  const wrapper = ({ children }) => createElement("div", null, children);
  return Object.fromEntries(["Dialog", "DialogContent", "DialogDescription", "DialogFooter", "DialogHeader", "DialogTitle"].map(name => [name, wrapper]));
});
import { CreateProjectDialog } from "../ui/components/agent/create-project-dialog";

it("preserves the selection across disconnection without submitting or dismissing", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const props = {
    open: true, initialPath: "/repo", disabled: false,
    listDir: vi.fn(async () => ({ path: "/repo", parent: "/", entries: [], roots: [] })),
    mkdir: vi.fn(), onSubmit: vi.fn(), onOpenChange: vi.fn(), t: key => key,
  };
  const render = () => act(async () => root.render(createElement(CreateProjectDialog, props)));
  try {
    await render();
    expect(host.querySelector('button[type="submit"]').disabled).toBe(false);
    props.disabled = true;
    await render();
    expect(host.querySelector('button[type="submit"]').disabled).toBe(true);
    expect(host.querySelector('[aria-label="agent.newFolder"]').disabled).toBe(true);
    // Guard the form handler too, including implicit keyboard submission.
    await act(async () => host.querySelector("form").dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(props.onSubmit).not.toHaveBeenCalled();
    expect(props.onOpenChange).not.toHaveBeenCalled();
    props.disabled = false;
    await render();
    await act(async () => host.querySelector('button[type="submit"]').click());
    expect(props.onSubmit).toHaveBeenCalledExactlyOnceWith("/repo");
    expect(props.onOpenChange).toHaveBeenCalledExactlyOnceWith(false);
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});

it("discards the prior session selection even when the new session has the same cwd", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const oldListing = Promise.withResolvers();
  const newListing = Promise.withResolvers();
  const listDir = vi.fn().mockReturnValueOnce(oldListing.promise).mockReturnValueOnce(newListing.promise);
  const props = { open: true, initialPath: "/repo", listDir, mkdir: vi.fn(), onSubmit: vi.fn(), onOpenChange: vi.fn(), t: key => key };
  try {
    await act(async () => root.render(createElement(CreateProjectDialog, { ...props, key: "old-session" })));
    await act(async () => root.render(createElement(CreateProjectDialog, { ...props, key: "new-session" })));
    await act(async () => oldListing.resolve({ path: "/old-device/repo", parent: "/", entries: [], roots: [] }));
    expect(host.querySelector('button[type="submit"]').disabled).toBe(true);
    await act(async () => newListing.resolve({ path: "/new-device/repo", parent: "/", entries: [], roots: [] }));
    await act(async () => host.querySelector('button[type="submit"]').click());
    expect(props.onSubmit).toHaveBeenCalledExactlyOnceWith("/new-device/repo");
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
