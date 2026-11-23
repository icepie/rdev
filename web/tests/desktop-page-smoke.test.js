// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";

/* The page owns the whole remote-desktop session; the rail shell, the device
 * list transport and the socket are irrelevant to the two states exercised
 * here: "pick a device" and "one device auto-connects". */
const state = vi.hoisted(() => ({ devices: [], sockets: [] }));

vi.mock("../ui/lib/devices", () => ({
  useDevices: () => ({ devices: state.devices, error: "", loading: false, refresh: () => {} }),
}));
vi.mock("../ui/lib/i18n", () => ({ useI18n: () => ({ t: (key) => key, lang: "en" }) }));
vi.mock("../ui/lib/api", () => ({
  apiJSON: async (path) => (path === "/api/devices" ? state.devices : null),
  apiFetch: async () => ({ ok: true, json: async () => ({}) }),
  wsURL: (path) => `ws://test${path}`,
  getDevicePassword: () => "",
  rememberDevicePassword: () => {},
  forgetDevicePassword: () => {},
}));
vi.mock("../ui/lib/server-capabilities", () => ({
  useServerCapabilities: () => ({ devicePasswordInputsHidden: false, browserEnabled: false, loading: false }),
}));
vi.mock("../ui/shell/AppShell", () => ({ MobileWorkspaceNavigation: () => null }));
vi.mock("../ui/components/split-layout", () => ({
  SplitPageShell: ({ children }) => createElement("div", { "data-rail": "shell" }, children),
  MobileSidebarTrigger: () => null,
}));
vi.mock("../ui/components/device-rail", () => ({
  DeviceRail: () => null,
  DeviceDropTarget: ({ children }) => children,
}));
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
vi.mock("../ui/components/os-icon", () => ({ OsIcon: () => null }));

import { DesktopPage } from "../ui/pages/Desktop";

const device = {
  id: "laptop",
  displayName: "Laptop",
  hostname: "laptop",
  os: "linux",
  hasPassword: false,
  connectedAt: "",
  gpuDesktop: false,
  desktop: { supported: true, input: true, sources: [] },
};

class Socket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  readyState = Socket.CONNECTING;
  binaryType = "blob";
  sent = [];
  constructor(url) {
    this.url = url;
    state.sockets.push(this);
  }
  send(data) {
    this.sent.push(data);
  }
  close() {
    this.readyState = Socket.CLOSED;
  }
}

vi.stubGlobal("WebSocket", Socket);
vi.stubGlobal("ResizeObserver", class {
  observe() {}
  unobserve() {}
  disconnect() {}
});

afterEach(() => {
  document.body.replaceChildren();
  localStorage.clear();
  state.devices = [];
  state.sockets.length = 0;
  delete globalThis.IS_REACT_ACT_ENVIRONMENT;
});

async function renderPage() {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  await act(async () =>
    root.render(createElement(MemoryRouter, null, createElement(DesktopPage))),
  );
  return { host, root };
}

it("renders the select-a-device state without a device", async () => {
  state.devices = [];
  const { host, root } = await renderPage();
  try {
    const buttons = [...host.querySelectorAll("button")];
    expect(buttons.find((b) => b.textContent === "common.connect").disabled).toBe(true);
    expect(buttons.find((b) => b.textContent === "desktop.disconnect").disabled).toBe(true);
    /* No session yet: the stage keeps its placeholder and never connects. */
    expect(host.textContent).toContain("desktop.empty");
    expect(host.querySelector('[role="status"]').textContent).toBe("desktop.idle");
    expect(state.sockets).toHaveLength(0);
  } finally {
    await act(async () => root.unmount());
  }
});

it("auto-connects a single configured device without crashing", async () => {
  localStorage.setItem("rdevDesktop.device", "laptop");
  state.devices = [device];
  const { host, root } = await renderPage();
  try {
    expect(host.querySelector('[role="status"]').textContent).toBe("common.connecting");
    expect(state.sockets).toHaveLength(1);
    const query = new URL(state.sockets[0].url).searchParams;
    expect(new URL(state.sockets[0].url).pathname).toBe("/desktop");
    expect(query.get("device")).toBe("laptop");
    /* The toolbar mirrors the resolved device, and the stage still offers the
     * placeholder until the first frame arrives. */
    expect(host.textContent).toContain("laptop");
    expect(host.querySelector('[role="status"]')).toBeTruthy();
  } finally {
    await act(async () => root.unmount());
    expect(state.sockets[0].close).toBeTruthy();
    expect(state.sockets[0].readyState).toBe(Socket.CLOSED);
  }
});
