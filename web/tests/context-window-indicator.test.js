// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it } from "vitest";
import { ContextWindowIndicator } from "../ui/components/agent/context-window-indicator";

it("opens context details from the cumulative token entry without conflating their counts", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const t = (key, vars) => `${key} ${Object.values(vars || {}).join(" / ")}`;
  try {
    await act(async () => root.render(createElement(ContextWindowIndicator, {
      used: 32000, contextWindow: 128000, totalTokens: 70800, t,
    })));
    const button = host.querySelector("button");
    expect(button.textContent).toContain("70.8k");
    expect(button.getAttribute("aria-label")).toContain("25");
    await act(async () => button.click());
    const details = document.querySelector('[role="dialog"]');
    expect(details.textContent).toContain("32k / 128k");
    expect(details.textContent).toContain("agent.usageLeft 75");
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
