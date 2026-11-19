// @vitest-environment jsdom
import { act, createContext, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";

let devices;
const t = key => key;
vi.mock("../ui/lib/api", () => ({
  apiJSON: async path => {
    expect(path).toBe("/api/batch/devices");
    return devices;
  },
}));
vi.mock("../ui/lib/i18n", () => ({ useI18n: () => ({ t }) }));
vi.mock("../ui/lib/server-capabilities", () => ({ useServerCapabilities: () => ({ devicePasswordInputsHidden: false }) }));
vi.mock("../ui/shell/AppShell", () => ({ MobileNavContext: createContext({ open: () => {} }) }));
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
vi.mock("../ui/components/os-icon", () => ({ OsIcon: () => null }));
vi.mock("../ui/components/tabs", () => ({
  Tabs: ({ children }) => children,
  TabsList: ({ children }) => children,
  TabsTrigger: ({ children }) => createElement("button", null, children),
  TabsContent: () => null,
}));
import { BatchPage } from "../ui/pages/Batch";

it("distinguishes same-name devices and retains ID-based selections when names change", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers();
  devices = [{ id: "first", displayName: "Worker" }, { id: "second", displayName: "Worker" }, { id: "legacy" }];
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  try {
    await act(() => root.render(createElement(BatchPage)));
    const checkboxes = [...host.querySelectorAll('input[type="checkbox"]')];
    expect(checkboxes.map(input => input.closest("label").textContent)).toEqual(["Worker (first)", "Worker (second)", "legacy"]);
    await act(() => checkboxes[1].click());
    expect(checkboxes.map(input => input.checked)).toEqual([false, true, false]);
    devices = [{ id: "first", displayName: "Renamed first" }, { id: "second", displayName: "Renamed second" }, { id: "legacy" }];
    await act(async () => vi.advanceTimersByTimeAsync(5000));
    expect([...host.querySelectorAll('input[type="checkbox"]')]).toEqual(checkboxes);
    expect(checkboxes[1].closest("label").textContent).toBe("Renamed second (second)");
    expect(checkboxes.map(input => input.checked)).toEqual([false, true, false]);
    devices = [{ id: "first", displayName: "Renamed second" }, { id: "replacement", displayName: "Renamed second" }];
    await act(async () => vi.advanceTimersByTimeAsync(5000));
    expect([...host.querySelectorAll('input[type="checkbox"]')].map(input => input.checked)).toEqual([false, false]);
  } finally {
    await act(() => root.unmount());
    host.remove();
    vi.useRealTimers();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
