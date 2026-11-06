// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { ThreadRenameInput } from "../ui/components/agent/thread-rename-input";

it("ignores IME confirmation, retries failed sends, and commits only once on Enter then blur", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const commit = vi.fn().mockReturnValueOnce(false).mockReturnValue(true);
  const finish = vi.fn();
  try {
    await act(async () => root.render(createElement(ThreadRenameInput, { title: "draft", label: "rename", onCommit: commit, onFinish: finish })));
    const input = host.querySelector("input");
    const key = options => act(async () => input.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter", ...options })));
    await key({ isComposing: true });
    await key({ keyCode: 229 });
    expect(commit).not.toHaveBeenCalled();
    input.value = "  new title  ";
    await key({});
    expect(finish).not.toHaveBeenCalled();
    await act(async () => input.blur());
    expect(commit.mock.calls).toEqual([["new title"], ["new title"]]);
    expect(finish).toHaveBeenCalledTimes(1);
    await key({});
    expect(commit).toHaveBeenCalledTimes(2);
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});

it("cancels on Escape without submitting when the input subsequently blurs", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const commit = vi.fn(), finish = vi.fn();
  try {
    await act(async () => root.render(createElement(ThreadRenameInput, { title: "draft", label: "rename", onCommit: commit, onFinish: finish })));
    const input = host.querySelector("input");
    await act(async () => input.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Escape" })));
    await act(async () => input.blur());
    expect(commit).not.toHaveBeenCalled();
    expect(finish).toHaveBeenCalledTimes(1);
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
