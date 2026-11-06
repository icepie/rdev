import { JSDOM } from "jsdom";
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it } from "vitest";
import { ThemeProvider, useTheme } from "../ui/lib/theme";

it("keeps mode, BB palette, system changes, and other browser tabs in sync", async () => {
  const dom = new JSDOM(
    '<html><head></head><body><div id="root"></div></body></html>',
    { url: "http://localhost" },
  );
  const keys = [
    "window",
    "document",
    "localStorage",
    "getComputedStyle",
    "IS_REACT_ACT_ENVIRONMENT",
  ];
  const previous = Object.fromEntries(
    keys.map((k) => [k, Object.getOwnPropertyDescriptor(globalThis, k)]),
  );
  Object.assign(globalThis, {
    window: dom.window,
    document: dom.window.document,
    localStorage: dom.window.localStorage,
    getComputedStyle: dom.window.getComputedStyle.bind(dom.window),
    IS_REACT_ACT_ENVIRONMENT: true,
  });
  let systemDark = false,
    theme;
  const listeners = new Set();
  window.matchMedia = () => ({
    get matches() {
      return systemDark;
    },
    addEventListener: (_, fn) => listeners.add(fn),
    removeEventListener: (_, fn) => listeners.delete(fn),
  });
  function Harness() {
    theme = useTheme();
    return null;
  }
  const root = createRoot(document.getElementById("root"));
  try {
    await act(async () =>
      root.render(createElement(ThemeProvider, null, createElement(Harness))),
    );
    expect(document.documentElement.classList.contains("light")).toBe(true);
    await act(async () => {
      theme.setChoice("light");
      systemDark = true;
      listeners.forEach((fn) => fn());
    });
    expect(theme.resolved).toBe("light");
    await act(async () => theme.setChoice("auto"));
    expect(theme.resolved).toBe("dark");
    await act(async () => theme.setPreset("nord"));
    expect(document.getElementById("rdev-app-theme").textContent).toContain(
      "--canvas: #2e3440",
    );
    expect(document.documentElement.dataset.themePreset).toBe("nord");
    await act(async () => {
      localStorage.setItem("rdevTheme", "light");
      window.dispatchEvent(
        new window.StorageEvent("storage", { key: "rdevTheme" }),
      );
    });
    expect(theme.resolved).toBe("light");
    await act(async () => theme.setPreset("default"));
    expect(document.getElementById("rdev-app-theme").textContent).toBe("");
  } finally {
    await act(async () => root.unmount());
    dom.window.close();
    for (const [key, descriptor] of Object.entries(previous)) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else delete globalThis[key];
    }
  }
});
