import { JSDOM } from "jsdom";
import { act, createElement, useEffect } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";

let compact = false;
let control;
vi.mock("../ui/components/hooks/use-compact-viewport", () => ({
  useIsCompactViewport: () => compact,
}));
vi.mock("../ui/lib/i18n", () => ({ useI18n: () => ({ t: (key) => key }) }));
vi.mock("../ui/lib/rail-control", () => ({
  setRailControl: (value) => {
    control = value;
  },
}));
vi.mock("../ui/components/device-rail", () => ({ DeviceRail: () => null }));
import { SplitPageShell } from "../ui/components/split-layout";

it("preserves a live workspace across collapse and breakpoints and persists bounded pixel resizing", async () => {
  const dom = new JSDOM(
    '<button id="toggle">Sidebar</button><div id="root"></div>',
    { url: "http://localhost" },
  );
  const keys = [
    "window",
    "document",
    "localStorage",
    "IS_REACT_ACT_ENVIRONMENT",
  ];
  const previous = Object.fromEntries(
    keys.map((key) => [key, Object.getOwnPropertyDescriptor(globalThis, key)]),
  );
  Object.assign(globalThis, {
    window: dom.window,
    document: dom.window.document,
    localStorage: dom.window.localStorage,
    IS_REACT_ACT_ENVIRONMENT: true,
  });
  compact = false;
  let mounts = 0,
    unmounts = 0;
  function Workspace() {
    useEffect(() => {
      mounts++;
      return () => {
        unmounts++;
      };
    }, []);
    return createElement("textarea", { defaultValue: "keep this session" });
  }
  const root = createRoot(document.getElementById("root"));
  const render = () =>
    root.render(
      createElement(
        SplitPageShell,
        { storageKey: "review", onDevicePick: () => {} },
        createElement(Workspace),
      ),
    );
  try {
    await act(render);
    const pane = document.querySelector("textarea");
    expect(document.querySelector("aside").style.width).toBe("312px");
    await act(() => control.toggle());
    expect(document.querySelector("aside").hasAttribute("inert")).toBe(true);
    await act(() => control.toggle());
    const separator = document.querySelector('[role="separator"]');
    await act(() =>
      separator.dispatchEvent(
        new window.KeyboardEvent("keydown", { key: "End", bubbles: true }),
      ),
    );
    expect(document.querySelector("aside").style.width).toBe("460px");
    expect(localStorage.getItem("review.width")).toBe("460");
    document.querySelector("aside").focus();
    compact = true;
    await act(render);
    expect(document.activeElement).toBe(pane.parentElement);
    expect(document.activeElement.closest("[inert]")).toBeNull();
    const toggle = document.getElementById("toggle");
    toggle.focus();
    await act(() => control.toggle());
    expect(document.activeElement).toBe(document.querySelector("aside"));
    expect(pane.closest("[inert]")).not.toBeNull();
    await act(() =>
      document.dispatchEvent(
        new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      ),
    );
    expect(pane.closest("[inert]")).toBeNull();
    expect(document.activeElement).toBe(toggle);
    compact = false;
    await act(render);
    expect(document.querySelector("textarea")).toBe(pane);
    expect(document.querySelector("aside").style.width).toBe("460px");
    separator.focus();
    compact = true;
    await act(render);
    expect(document.activeElement).toBe(pane.parentElement);
    expect(document.querySelector("textarea")).toBe(pane);
    expect({ mounts, unmounts }).toEqual({ mounts: 1, unmounts: 0 });
  } finally {
    await act(() => root.unmount());
    dom.window.close();
    for (const [key, descriptor] of Object.entries(previous)) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else delete globalThis[key];
    }
  }
});
