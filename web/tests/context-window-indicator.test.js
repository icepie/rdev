// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { ContextWindowIndicator } from "../ui/components/agent/context-window-indicator";

it("opens context details from a ring without a visible token count", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const t = (key, vars) => `${key} ${Object.values(vars || {}).join(" / ")}`;
  try {
    await act(async () => root.render(createElement(ContextWindowIndicator, {
      used: 32000, contextWindow: 128000, t,
    })));
    const button = host.querySelector("button");
    expect(button.textContent).toBe("");
    expect(button.getAttribute("aria-label")).toContain("25");
    await act(async () => button.click());
    const details = document.querySelector('[role="dialog"]');
    expect(details.textContent).not.toContain("32k");
    expect(details.textContent).not.toContain("128k");
    expect(details.textContent).toContain("agent.usageLeft 75");
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});


it("names the mobile sheet and disables compaction while a turn is busy", async () => {
  const { CompactViewportOverrideProvider } = await import("../ui/components/hooks/use-compact-viewport");
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const onCompact = vi.fn();
  const render = (busy) => createElement(CompactViewportOverrideProvider, { isCompactViewport: true },
    createElement(ContextWindowIndicator, { used: 32000, contextWindow: 128000, t: (key) => key, onCompact, busy }));
  try {
    await act(async () => root.render(render(true)));
    await act(async () => host.querySelector("button").click());
    // The drawer realizes its body after the opening transition fallback.
    await act(async () => new Promise((resolve) => setTimeout(resolve, 150)));
    const dialog = document.querySelector('[role="dialog"]');
    expect(document.getElementById(dialog.getAttribute("aria-labelledby"))?.textContent).toBe("agent.estimatedContext");
    const compact = Array.from(dialog.querySelectorAll("button")).find((button) => button.textContent === "agent.cmd.compact");
    expect(compact.disabled).toBe(true);
    await act(async () => compact.click());
    expect(onCompact).not.toHaveBeenCalled();
    await act(async () => root.render(render(false)));
    await act(async () => compact.click());
    expect(onCompact).toHaveBeenCalledTimes(1);
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
