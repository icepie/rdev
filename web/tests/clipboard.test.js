import { JSDOM } from "jsdom";
import { expect, it, vi } from "vitest";
import { copyText } from "../ui/lib/clipboard";

it("restores the composer selection and removes fallback nodes on success or failure", async () => {
  const dom = new JSDOM('<textarea id="composer">hello world</textarea>');
  const names = ["window", "document", "navigator", "HTMLElement", "HTMLInputElement", "HTMLTextAreaElement"];
  const previous = names.map(name => [name, Object.getOwnPropertyDescriptor(globalThis, name)]);
  for (const name of names) Object.defineProperty(globalThis, name, { configurable: true, value: name === "window" ? dom.window : dom.window[name] });
  try {
    const composer = document.querySelector("textarea");
    for (const outcome of [true, false, new Error("unsupported")]) {
      composer.focus();
      composer.setSelectionRange(2, 7, "backward");
      document.execCommand = vi.fn(() => {
        expect(document.activeElement.value).toBe("copied output");
        if (outcome instanceof Error) throw outcome;
        return outcome;
      });
      const result = copyText("copied output");
      if (outcome === true) await expect(result).resolves.toBeUndefined();
      else await expect(result).rejects.toThrow();
      expect(document.activeElement).toBe(composer);
      expect([composer.selectionStart, composer.selectionEnd, composer.selectionDirection]).toEqual([2, 7, "backward"]);
      expect(document.querySelectorAll("textarea")).toHaveLength(1);
    }
  } finally {
    dom.window.close();
    for (const [name, descriptor] of previous) {
      if (descriptor) Object.defineProperty(globalThis, name, descriptor);
      else delete globalThis[name];
    }
  }
});
