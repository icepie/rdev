// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
vi.mock("../ui/components/dialog", () => {
  const wrapper = ({ children }) => createElement("div", null, children);
  return Object.fromEntries(["Dialog", "DialogContent", "DialogDescription", "DialogFooter", "DialogHeader", "DialogTitle"].map(name => [name, wrapper]));
});
import { ThreadRenameDialog } from "../ui/components/agent/thread-rename-dialog";

it("keeps failed renames editable and submits the captured target after reconnect", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const target = { id: "original-thread", device: "original-device", title: "original" };
  const props = { target, open: true, disabled: false, onCommit: vi.fn().mockReturnValueOnce(false).mockReturnValue(true), onClose: vi.fn(), t: key => key };
  const render = () => act(async () => root.render(createElement(ThreadRenameDialog, props)));
  const submit = () => act(async () => host.querySelector("form").dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
  try {
    await render();
    const input = host.querySelector("input");
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(input, "  新名称  ");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    for (const options of [{ isComposing: true }, { keyCode: 229 }]) {
      const event = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true, ...options });
      await act(async () => input.dispatchEvent(event));
      expect(event.defaultPrevented).toBe(true);
    }
    expect(props.onCommit).not.toHaveBeenCalled();
    await submit();
    expect(host.querySelector('[role="alert"]').textContent).toBe("common.disconnected");
    expect(props.onClose).not.toHaveBeenCalled();
    props.disabled = true;
    await render();
    expect(host.querySelector('button[type="submit"]').disabled).toBe(true);
    await submit();
    expect(props.onCommit).toHaveBeenCalledTimes(1);
    props.disabled = false;
    await render();
    expect(input.value).toBe("  新名称  ");
    await submit();
    expect(props.onCommit.mock.calls).toEqual([
      ["original-thread", "新名称", "original-device"],
      ["original-thread", "新名称", "original-device"],
    ]);
    expect(props.onClose).toHaveBeenCalledTimes(1);
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
