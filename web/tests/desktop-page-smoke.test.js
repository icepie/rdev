// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

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

class FakeSourceBuffer extends EventTarget {
  mode = "segments";
  updating = false;
  buffered = { length: 0 };
  appends = [];
  appendBuffer(data) {
    this.appends.push(data);
  }
  remove() {}
}

class FakeMediaSource extends EventTarget {
  static sources = [];
  static isTypeSupported = () => true;
  readyState = "closed";
  buffers = [];
  constructor() {
    super();
    FakeMediaSource.sources.push(this);
  }
  addSourceBuffer() {
    const buffer = new FakeSourceBuffer();
    this.buffers.push(buffer);
    return buffer;
  }
  endOfStream() {}
}

beforeEach(() => {
  vi.stubGlobal("WebSocket", Socket);
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    unobserve() {}
    disconnect() {}
  });
  FakeMediaSource.sources.length = 0;
  vi.stubGlobal("MediaSource", FakeMediaSource);
  URL.createObjectURL = vi.fn(() => "blob:gpu-media");
  URL.revokeObjectURL = vi.fn();
});

afterEach(() => {
  document.body.replaceChildren();
  localStorage.clear();
  state.devices = [];
  state.sockets.length = 0;
  delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  vi.unstubAllGlobals();
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

it("starts queued GPU video at sourceopen and ignores stale stream events", async () => {
  localStorage.setItem("rdevDesktop.device", "laptop");
  state.devices = [{ ...device, gpuDesktop: true }];
  const { root } = await renderPage();
  try {
    expect(state.sockets).toHaveLength(1);
    const socket = state.sockets[0];
    expect(new URL(socket.url).pathname).toBe("/gpu-desktop/laptop/ws");
    const deliver = (data) => act(async () => socket.onmessage({ data }));

    await deliver(JSON.stringify("NewVideo"));
    expect(FakeMediaSource.sources).toHaveLength(1);
    const first = FakeMediaSource.sources[0];

    /* Video packets commonly arrive before sourceopen; the first append must
     * happen at open time, not on a later packet. */
    const packet = new Uint8Array([1, 2, 3]).buffer;
    await deliver(packet);
    expect(first.buffers).toHaveLength(0);
    await act(async () => {
      first.readyState = "open";
      first.dispatchEvent(new Event("sourceopen"));
    });
    expect(first.buffers).toHaveLength(1);
    expect(first.buffers[0].appends).toEqual([packet]);

    /* Reconnect: a new MediaSource owns the stream, so a late sourceopen from
     * the previous one must not create a second buffer on it. */
    await deliver(JSON.stringify("NewVideo"));
    expect(FakeMediaSource.sources).toHaveLength(2);
    const second = FakeMediaSource.sources[1];
    await act(async () => first.dispatchEvent(new Event("sourceopen")));
    expect(first.buffers).toHaveLength(1);

    await act(async () => {
      second.readyState = "open";
      second.dispatchEvent(new Event("sourceopen"));
    });
    expect(second.buffers).toHaveLength(1);

    /* Unmount detaches the listeners that outlive the socket. */
    await act(async () => root.unmount());
    await act(async () => second.dispatchEvent(new Event("sourceopen")));
    expect(second.buffers).toHaveLength(1);
  } finally {
    await act(async () => root.unmount());
  }
});
