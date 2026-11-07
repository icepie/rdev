// @vitest-environment jsdom
import { act, createElement, useState } from "react";
import { createRoot } from "react-dom/client";
import { expect, it } from "vitest";
import { MobileTrigger, ResponsiveDrawerShell } from "../ui/components/responsive-overlay";

it("restores focus to a pointer-activated mobile trigger after Escape", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  function Harness() {
    const [open, setOpen] = useState(false);
    return createElement("div", null,
      createElement(MobileTrigger, { open, onOpenChange: setOpen, haspopup: "menu" }, "Actions"),
      createElement(ResponsiveDrawerShell, { open, onOpenChange: setOpen, srLabel: "Actions" }, "Menu"));
  }
  try {
    await act(async () => root.render(createElement(Harness)));
    const trigger = host.querySelector("button");
    await act(async () => trigger.click());
    expect(document.activeElement?.getAttribute("aria-modal")).toBe("true");
    await act(async () => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(document.activeElement).toBe(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
