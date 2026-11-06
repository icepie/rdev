// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import { shouldTogglePanel } from "../ui/components/agent/panel-shortcut";

it("toggles once per shortcut while preserving editing and handled events", () => {
  const host = document.createElement("div");
  host.innerHTML = '<button>panel</button><input><textarea></textarea><select></select><div contenteditable="true"><span>text</span></div><div contenteditable="plaintext-only"><span>plain</span></div><div role="textbox"><span>editor</span></div>';
  document.body.append(host);
  const toggle = vi.fn();
  const handler = event => { if (shouldTogglePanel(event)) { event.preventDefault(); toggle(); } };
  window.addEventListener("keydown", handler);
  const press = (target, flags = {}, handled = false) => {
    const event = new KeyboardEvent("keydown", { key: "j", ctrlKey: true, bubbles: true, cancelable: true, ...flags });
    if (handled) event.preventDefault();
    target.dispatchEvent(event);
    return event;
  };
  try {
    for (const selector of ["input", "textarea", "select", "[contenteditable=true] span", "[contenteditable=plaintext-only] span", "[role=textbox] span"]) {
      expect(press(host.querySelector(selector)).defaultPrevented).toBe(false);
    }
    const button = host.querySelector("button");
    for (const flags of [{ repeat: true }, { isComposing: true }, { keyCode: 229 }, { altKey: true }, { shiftKey: true }, { ctrlKey: false }, { key: "k" }]) {
      expect(press(button, flags).defaultPrevented).toBe(false);
    }
    press(button, {}, true);
    expect(toggle).not.toHaveBeenCalled();
    expect(press(button).defaultPrevented).toBe(true);
    expect(press(button, { ctrlKey: false, metaKey: true }).defaultPrevented).toBe(true);
    expect(toggle).toHaveBeenCalledTimes(2);
  } finally {
    window.removeEventListener("keydown", handler);
    host.remove();
  }
});
