// @vitest-environment jsdom
import { act, createContext, createElement } from "react";
import { createRoot } from "react-dom/client";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

const taskRequest = vi.fn();
const refreshDevices = vi.fn();
let devices = [];

vi.mock("../ui/lib/i18n", () => ({ useI18n: () => ({ t: (k) => k, lang: "en" }) }));
vi.mock("../ui/lib/devices", () => ({
  useDevices: () => ({ devices, loading: false, error: "", refresh: refreshDevices }),
}));
vi.mock("../ui/lib/api", () => ({
  apiJSON: async (path) => (path === "/api/batch/devices" ? devices : { sshPort: "2222", tcpPort: "8081", kcpPort: "8082" }),
  apiFetch: vi.fn(async () => new Response("[]")),
  getDevicePassword: () => "",
  rememberDevicePassword: () => {},
  forgetDevicePassword: () => {},
  deviceAuthBypassed: async () => false,
  wsURL: (path) => `ws://localhost${path}`,
}));
vi.mock("../ui/lib/server-capabilities", () => ({
  useServerCapabilities: () => ({ browserEnabled: true, devicePasswordInputsHidden: false, loading: false }),
}));
vi.mock("../ui/lib/clipboard", () => ({ useCopy: () => [null, vi.fn()] }));
vi.mock("../ui/shell/AppShell", () => ({ MobileNavContext: createContext({ open: () => {}, close: () => {} }) }));
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
vi.mock("../ui/components/os-icon", () => ({ OsIcon: () => null }));
vi.mock("../ui/components/code-block", () => ({ CodeBlock: ({ children }) => createElement("code", null, children) }));
vi.mock("../ui/components/tooltip", () => ({
  Tooltip: ({ children }) => children,
  TooltipTrigger: ({ children }) => children,
  TooltipContent: () => null,
}));
vi.mock("../ui/pages/tasks/api", () => ({
  request: (...args) => taskRequest(...args),
  loadLogTail: vi.fn(async () => ({ output: "", nextOffset: 0 })),
}));

import { DashboardPage } from "../ui/pages/Dashboard";
import { BatchPage } from "../ui/pages/Batch";
import { BrowserPanel } from "../ui/components/browser-panel";
import { TaskDetailPage } from "../ui/pages/Tasks";
import { TaskForm } from "../ui/pages/TaskForm";

let host;
let root;

beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  taskRequest.mockReset();
  refreshDevices.mockReset();
  devices = [
    { id: "alpha-box", displayName: "Alpha", os: "linux", hasPassword: true, connectedAt: new Date().toISOString() },
    { id: "beta-mac", displayName: "Beta", os: "darwin", hasPassword: false, connectedAt: new Date().toISOString() },
  ];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  delete globalThis.IS_REACT_ACT_ENVIRONMENT;
});

it("filters devices and supports manual refresh on Dashboard", async () => {
  await act(async () => root.render(createElement(MemoryRouter, null, createElement(DashboardPage))));
  expect(host.querySelectorAll("tbody tr")).toHaveLength(2);
  const search = host.querySelector('input[aria-label="rail.search"]');
  expect(search).not.toBeNull();
  const propsKey = Object.keys(search).find((k) => k.startsWith("__reactProps$"));
  await act(async () => search[propsKey].onChange({ target: { value: "beta" } }));
  expect(host.querySelectorAll("tbody tr")).toHaveLength(1);
  expect(host.querySelector("tbody").textContent).toContain("Beta");
  const refresh = host.querySelector('button[aria-label="files.refresh"]');
  await act(async () => refresh.click());
  expect(refreshDevices).toHaveBeenCalledOnce();
});

it("renders toolbarStart inline in BrowserPanel toolbar", async () => {
  await act(async () =>
    root.render(
      createElement(BrowserPanel, {
        device: "alpha-box",
        deviceSelection: "sidebar",
        toolbarStart: createElement("button", { type: "button", "data-testid": "sidebar-trigger" }, "menu"),
      }),
    ),
  );
  const trigger = host.querySelector('[data-testid="sidebar-trigger"]');
  expect(trigger).not.toBeNull();
  expect(trigger.parentElement.textContent).toContain("nav.browser");
});

it("uses AlertDialog instead of window.confirm when deleting a schedule", async () => {
  const nativeConfirm = vi.spyOn(window, "confirm");
  taskRequest.mockImplementation(async (action) => {
    if (action === "get") {
      return {
        id: "sched-1",
        name: "Nightly backup",
        kind: "shell",
        device: "alpha-box",
        state: "enabled",
        cron: "0 2 * * *",
        revision: 1,
        authorized: true,
        events: [],
      };
    }
    if (action === "runs") return [];
    return {};
  });
  await act(async () =>
    root.render(
      createElement(
        MemoryRouter,
        { initialEntries: ["/tasks/sched-1"] },
        createElement(
          Routes,
          null,
          createElement(Route, { path: "/tasks/:id", element: createElement(TaskDetailPage) }),
          createElement(Route, { path: "/tasks", element: createElement("div", null, "list") }),
        ),
      ),
    ),
  );
  const deleteBtn = [...host.querySelectorAll("button")].find((b) => b.textContent === "tasks.delete");
  await act(async () => deleteBtn.click());
  expect(nativeConfirm).not.toHaveBeenCalled();
  const dialog = document.querySelector('[role="alertdialog"]');
  expect(dialog?.textContent).toContain("tasks.deleteConfirm");
  const confirmBtn = [...dialog.querySelectorAll("button")].find((b) => b.textContent === "tasks.delete");
  await act(async () => confirmBtn.click());
  expect(taskRequest).toHaveBeenCalledWith("delete", { id: "sched-1" });
  nativeConfirm.mockRestore();
});

it("renders design-system Select and Switch controls in TaskForm without visible native selects", async () => {
  taskRequest.mockResolvedValue({ devices: [{ id: "alpha-box", name: "Alpha", online: true }] });
  await act(async () => root.render(createElement(MemoryRouter, null, createElement(TaskForm))));
  expect(host.querySelectorAll('select:not([aria-hidden="true"])')).toHaveLength(0);
  expect(host.querySelectorAll('[role="combobox"]').length).toBeGreaterThanOrEqual(4);
  expect(host.querySelectorAll('[role="switch"]')).toHaveLength(2);
  expect(host.closest("main")).toBeNull();
  expect(host.querySelectorAll("main")).toHaveLength(0);
});

it("renders page heading and selected counter on BatchPage", async () => {
  await act(async () => root.render(createElement(MemoryRouter, null, createElement(BatchPage))));
  const heading = host.querySelector("h1.page-title");
  expect(heading?.textContent).toContain("nav.batch");
  expect(host.textContent).toContain("0/2");
  const selectAll = [...host.querySelectorAll("button")].find((b) => b.textContent === "batch.selectAll");
  await act(async () => selectAll.click());
  expect(host.textContent).toContain("2/2");
});
