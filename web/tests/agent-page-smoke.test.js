// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { AgentPage } from "../ui/pages/Agent";

/* The page only needs the device list, the translator and a socket that never
 * opens: with no authenticated connection it renders its route-blocked empty
 * state. Everything else (chat transcript, right panel, app rail) is
 * DOM-heavy at module scope and irrelevant to that state. */
const state = vi.hoisted(() => ({ devices: [] }));

vi.mock("../ui/lib/i18n", () => ({ useI18n: () => ({ t: key => key }) }));
vi.mock("../ui/lib/devices", () => ({ useDevices: () => ({ devices: state.devices }) }));
vi.mock("../ui/lib/api", () => ({
  wsURL: path => `ws://test${path}`,
  rememberDevicePassword: () => {},
  forgetDevicePassword: () => {},
}));
vi.mock("../ui/lib/server-capabilities", () => ({
  useServerCapabilities: () => ({ devicePasswordInputsHidden: true }),
}));
vi.mock("../ui/shell/AppShell", () => ({ MobileWorkspaceNavigation: () => null }));
vi.mock("../ui/components/tooltip", () => ({
  Tooltip: ({ children }) => children,
  TooltipTrigger: ({ children }) => children,
  TooltipContent: () => null,
}));
vi.mock("../ui/components/agent/chat-pane", () => ({
  ChatPaneView: ({ emptyHint }) => createElement("div", { "data-empty-hint": emptyHint }),
}));
vi.mock("../ui/components/agent/right-panel", () => ({ AgentRightPanel: () => null }));
vi.mock("react-resizable-panels", () => ({
  PanelGroup: ({ children }) => createElement("div", null, children),
  Panel: ({ children }) => createElement("div", null, children),
  PanelResizeHandle: () => null,
}));

class IdleSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  readyState = IdleSocket.CONNECTING;
  send() {}
  close() {}
}
vi.stubGlobal("WebSocket", IdleSocket);
vi.stubGlobal("ResizeObserver", class {
  observe() {}
  unobserve() {}
  disconnect() {}
});

afterEach(() => {
  document.body.replaceChildren();
  delete globalThis.IS_REACT_ACT_ENVIRONMENT;
});

async function renderPage() {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  await act(async () => root.render(createElement(MemoryRouter, null, createElement(AgentPage))));
  return { host, root };
}

it("renders the empty state without a device", async () => {
  state.devices = [];
  const { host, root } = await renderPage();
  try {
    /* No authenticated socket: the conversation pane shows the loading
     * placeholder instead of panes, and the sidebar is still there. */
    expect(host.querySelector("aside")).toBeTruthy();
    expect(host.querySelector('[role="status"]').textContent).toBe("common.loading");
    expect(host.textContent).toContain("agent.newThread");
  } finally {
    await act(async () => root.unmount());
  }
});

it("renders with one connected device", async () => {
  state.devices = [{ id: "laptop", displayName: "Laptop", hostname: "laptop", hasPassword: false, connectedAt: "" }];
  const { host, root } = await renderPage();
  try {
    expect(host.querySelector("aside")).toBeTruthy();
    /* By-device organization lists the live device as a group. */
    expect(host.textContent).toContain("laptop");
    expect(host.querySelector('[role="status"]').textContent).toBe("common.loading");
  } finally {
    await act(async () => root.unmount());
  }
});
