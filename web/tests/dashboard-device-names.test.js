// @vitest-environment jsdom
import { act, createContext, createElement } from "react";
import { createRoot } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

let devices;
const copy = vi.fn();
const apiFetch = vi.fn();
const t = key => key;
vi.mock("../ui/lib/devices", () => ({ useDevices: () => ({ devices, error: "", refresh: vi.fn() }) }));
vi.mock("../ui/lib/i18n", () => ({ useI18n: () => ({ t }) }));
vi.mock("../ui/lib/api", () => ({ apiJSON: async () => ({ sshPort: "2222", tcpPort: "8081", kcpPort: "8082", vncAddr: ":5900", proxyAddr: ":1080" }), apiFetch: (...args) => apiFetch(...args) }));
vi.mock("../ui/lib/clipboard", () => ({ useCopy: () => [null, copy] }));
vi.mock("../ui/shell/AppShell", () => ({ MobileNavContext: createContext({ open: () => {} }) }));
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
vi.mock("../ui/components/os-icon", () => ({ OsIcon: () => null }));
vi.mock("../ui/components/code-block", () => ({ CodeBlock: ({ children }) => createElement("code", null, children) }));
vi.mock("../ui/components/tabs", () => ({
  Tabs: ({ children }) => children,
  TabsList: ({ children }) => children,
  TabsTrigger: ({ children }) => createElement("button", null, children),
  TabsContent: () => null,
}));
vi.mock("../ui/components/tooltip", () => ({ Tooltip: ({ children }) => children, TooltipTrigger: ({ children }) => children, TooltipContent: () => null }));
vi.mock("../ui/components/dropdown-menu", () => ({
  DropdownMenu: ({ children }) => children,
  DropdownMenuTrigger: ({ children }) => children,
  DropdownMenuContent: ({ children }) => createElement("div", { role: "menu" }, children),
  DropdownMenuItem: ({ onSelect, children }) => createElement("button", { role: "menuitem", onClick: onSelect }, children),
}));
import { DashboardPage } from "../ui/pages/Dashboard";

let host;
let root;
const render = () => root.render(createElement(MemoryRouter, null, createElement(DashboardPage)));
const deviceRows = () => [...host.querySelectorAll("tbody tr")];
const identityCell = row => row.querySelector('[aria-label="index.deviceId"]');

beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  copy.mockReset();
  apiFetch.mockReset();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(() => root.unmount());
  host.remove();
  delete globalThis.IS_REACT_ACT_ENVIRONMENT;
});

it("renders display names with distinct IDs and falls back for unnamed devices", async () => {
  devices = [
    { id: "host/one", displayName: "Build host" },
    { id: "host/two", displayName: "Build host" },
    { id: "legacy" },
    { id: "empty", displayName: "" },
    { id: "same", displayName: "same" },
  ].map(device => ({ ...device, hasPassword: false, connectedAt: new Date().toISOString() }));
  await act(render);
  expect(deviceRows().map(row => identityCell(row).textContent)).toEqual([
    "Build hosthost/one", "Build hosthost/two", "legacy", "empty", "same",
  ]);
  const row = deviceRows()[0];
  const routes = ["terminal", "remote-desktop", "files", "agent"].map(page => `/${page}?device=host%2Fone`);
  expect([...row.querySelectorAll("a")].map(link => link.getAttribute("href"))).toEqual(routes);
  devices = devices.map(device => device.id === "host/one" ? { ...device, displayName: "Renamed host" } : device);
  await act(render);
  expect(deviceRows()[0]).toBe(row);
  expect(identityCell(row).textContent).toBe("Renamed hosthost/one");
  expect([...row.querySelectorAll("a")].map(link => link.getAttribute("href"))).toEqual(routes);
});

it("copies connection commands and resolves credentials using IDs, never shared names", async () => {
  const id = "host/one";
  const displayName = "Shared workstation";
  devices = [{ id, displayName, hasPassword: true, connectedAt: new Date().toISOString() }];
  apiFetch.mockImplementation(async () => new Response(JSON.stringify([
    { id: "other", displayName, password: "wrong-password" },
    { id, displayName, password: "correct-password" },
  ])));
  await act(render);
  const row = deviceRows()[0];
  const select = async label => {
    const item = [...row.querySelectorAll('[role="menuitem"]')].find(node => node.textContent === label);
    expect(item).toBeTruthy();
    await act(async () => item.click());
    return copy.mock.calls.at(-1);
  };
  const ssh = `ssh -p 2222 '${id}@${location.hostname}'`;
  expect(await select("index.copyConnSsh")).toEqual([`conn:ssh:${id}`, ssh]);
  const client = await select("index.copyConnClient");
  expect(client[0]).toBe(`conn:client:${id}`);
  expect(client[1]).toContain(`-i '${id}' -p 'correct-password'`);
  expect(client[1]).not.toContain(displayName);
  expect(apiFetch).toHaveBeenCalledWith("/api/clients?passwords=1");
  const desktopURL = `${location.origin}/remote-desktop?device=host%2Fone`;
  expect(await select("index.copyConnDesktop")).toEqual([`conn:desktop:${id}`, desktopURL]);
  const full = await select("index.copyConnFull");
  expect(full[0]).toBe(`conn:full:${id}`);
  for (const text of [`RDev device: ${id}`, `SSH: ${ssh}`, `Desktop: ${desktopURL}`, `username=${id}`, "host%2Fone:correct-password@", `-i '${id}'`]) {
    expect(full[1]).toContain(text);
  }
  expect(full[1]).not.toContain(displayName);
  expect(full[1]).not.toContain("wrong-password");
});
