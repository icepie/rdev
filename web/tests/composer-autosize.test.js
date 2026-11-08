// @vitest-environment jsdom
import { act, createElement, useRef } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { useComposerAutosize } from "../ui/components/agent/use-composer-autosize";

it("grows and shrinks drafts, reflows on width changes, and disconnects observers", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  let notify;
  const disconnects = [];
  vi.stubGlobal("ResizeObserver", class {
    constructor(callback) { notify = callback; this.disconnect = vi.fn(); disconnects.push(this.disconnect); }
    observe() {}
  });
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  let width = 400;
  const measure = vi.spyOn(HTMLTextAreaElement.prototype, "scrollHeight", "get")
    .mockImplementation(function () { return this.value.length * (width < 300 ? 4 : 2); });
  vi.spyOn(HTMLTextAreaElement.prototype, "getBoundingClientRect").mockImplementation(() => ({ width }));
  function Harness({ value }) {
    const ref = useRef(null);
    useComposerAutosize(ref, value);
    return createElement("textarea", { ref, value, readOnly: true });
  }
  const render = value => act(async () => root.render(createElement(Harness, { value })));
  try {
    await render("x".repeat(100));
    const editor = host.querySelector("textarea");
    expect(editor.style.height).toBe("200px");
    await render("short");
    expect(editor.style.height).toBe("10px");
    expect(disconnects[0]).toHaveBeenCalledOnce();
    editor.focus();
    editor.setSelectionRange(2, 4);
    width = 200;
    notify();
    expect(editor.style.height).toBe("20px");
    expect(document.activeElement).toBe(editor);
    expect([editor.selectionStart, editor.selectionEnd]).toEqual([2, 4]);
    const reads = measure.mock.calls.length;
    notify();
    expect(measure.mock.calls).toHaveLength(reads);
    await act(async () => root.unmount());
    expect(disconnects.every(disconnect => disconnect.mock.calls.length === 1)).toBe(true);
  } finally {
    host.remove();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
