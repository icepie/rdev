import { JSDOM } from "jsdom";
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";

const terminals = vi.hoisted(() => []);
const auth = vi.hoisted(() => ({ current: null }));
vi.mock("@xterm/xterm", () => ({ Terminal: class {
  options = {}; focus = vi.fn(); write = vi.fn(); reset = vi.fn(); dispose = vi.fn();
  constructor() { terminals.push(this); }
  loadAddon() {} open() {} onData() {} onResize() {}
} }));
vi.mock("@xterm/addon-fit", () => ({ FitAddon: class { fit() {} } }));
vi.mock("@xterm/addon-web-links", () => ({ WebLinksAddon: class {} }));
vi.mock("@xterm/addon-image", () => ({ ImageAddon: class {} }));
vi.mock("../ui/lib/api", () => ({ wsURL: path => path, rememberDevicePassword() {}, forgetDevicePassword() {} }));
vi.mock("../ui/lib/xterm-theme", () => ({ readXtermTheme: () => ({}) }));
vi.mock("../ui/lib/terminal-images", () => ({ createKittyImageWriter: () => ({}), hasTerminalImageSequence: () => false }));
vi.mock("../ui/pages/Terminal", () => ({ TabAuthOverlay: props => { auth.current = props; return null; } }));
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
import { PanelTerminal } from "../ui/components/agent/panel-terminal";

it("keeps hidden shells connected without stealing focus and ignores replaced socket events", async () => {
  const dom = new JSDOM('<div id="root"></div>', { pretendToBeVisual: true });
  const sockets = [];
  class Socket {
    static OPEN = 1;
    readyState = 1;
    constructor() { sockets.push(this); }
    close() {} send() {}
    message(frame) { this.onmessage?.({ data: JSON.stringify(frame) }); }
  }
  const replacements = {
    window: dom.window, document: dom.window.document, WebSocket: Socket,
    MutationObserver: dom.window.MutationObserver, ResizeObserver: class { observe() {} disconnect() {} },
    requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window),
    cancelAnimationFrame: dom.window.cancelAnimationFrame.bind(dom.window), IS_REACT_ACT_ENVIRONMENT: true,
  };
  const previous = Object.keys(replacements).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]);
  for (const [key, value] of Object.entries(replacements)) Object.defineProperty(globalThis, key, { configurable: true, value });
  const root = createRoot(document.getElementById("root"));
  const props = { device: "device", visible: false, t: key => key };
  const render = () => act(async () => root.render(createElement(PanelTerminal, props)));
  try {
    await render();
    const terminal = terminals.at(-1), first = sockets[0];
    await act(async () => first.message({ op: "auth" }));
    for (let attempt = 0; attempt < 3; attempt++) {
      await act(async () => auth.current.onSubmit("incorrect"));
      expect(auth.current.error).toBe("");
      await act(async () => first.message({ op: "auth_fail", message: "same rejection" }));
      expect(auth.current.error).toBe("same rejection");
    }

    await act(async () => first.message({ op: "auth_ok" }));
    expect(terminal.focus).not.toHaveBeenCalled();
    props.visible = true;
    await render();
    expect(sockets).toHaveLength(1);
    expect(terminal.dispose).not.toHaveBeenCalled();
    await act(async () => first.message({ op: "auth_ok" }));
    expect(terminal.focus).toHaveBeenCalledTimes(1);
    const lateClose = first.onclose, lateError = first.onerror;
    await act(async () => first.onerror());
    await act(async () => document.querySelector("button").click());
    expect(sockets).toHaveLength(2);
    await act(async () => sockets[1].message({ op: "auth_ok" }));
    await act(async () => { lateClose(); lateError(); });
    expect(document.body.textContent).not.toContain("agent.terminalClosed");
    expect(terminal.write).not.toHaveBeenCalled();
  } finally {
    await act(async () => root.unmount());
    dom.window.close();
    for (const [key, descriptor] of previous) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else delete globalThis[key];
    }
  }
});
