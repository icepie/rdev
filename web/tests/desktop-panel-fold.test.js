// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
const t = key => key;
const apiJSON = vi.fn();
vi.mock("../ui/lib/api", () => ({
  apiJSON: (...args) => apiJSON(...args), wsURL: path => `ws://localhost${path}`,
  getDevicePassword: () => "", rememberDevicePassword: vi.fn(), forgetDevicePassword: vi.fn(), deviceAuthBypassed: async () => false,
}));
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
import { PanelDesktop } from "../ui/components/agent/panel-desktop";

it.each([false, true])("preserves %s GPU desktop connection across folding, and releases it on unmount", async gpuDesktop => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  localStorage.clear();
  const sockets = [];
  class Socket {
    static OPEN = 1;
    static CONNECTING = 0;
    readyState = 1;
    send = vi.fn();
    close = vi.fn(() => { this.readyState = 3; });
    constructor(url) { this.url = url; sockets.push(this); }
  }
  vi.stubGlobal("WebSocket", Socket);
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(null);
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
  vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
  apiJSON.mockResolvedValue(["device", "other"].map(id => ({id,gpuDesktop,desktop:{supported:true,input:true,sources:[]}})));
  const host = document.createElement("div"); document.body.append(host);
  const root = createRoot(host);
  try {
    await act(() => root.render(createElement(PanelDesktop,{device:"device",visible:true,t})));
    expect(sockets).toHaveLength(1);
    const socket = sockets[0];
    for (const visible of [false, true, false, true]) {
      await act(() => root.render(createElement(PanelDesktop,{device:"device",visible,t})));
      expect(sockets).toHaveLength(1);
      expect(socket.close).not.toHaveBeenCalled();
      if (gpuDesktop) expect(socket.send).toHaveBeenCalledWith(JSON.stringify(visible ? "ResumeVideo" : "PauseVideo"));
    }
    // An explicit disconnect must survive folding as well.
    await act(() => [...host.querySelectorAll("button")].find(b => b.textContent === "desktop.disconnect").click());
    await act(() => root.render(createElement(PanelDesktop,{device:"device",visible:false,t})));
    await act(() => root.render(createElement(PanelDesktop,{device:"device",visible:true,t})));
    expect(sockets).toHaveLength(1);
    expect(socket.close).toHaveBeenCalledTimes(1);
    await act(() => root.render(createElement(PanelDesktop,{device:"other",visible:false,t})));
    expect(sockets).toHaveLength(1);
    await act(() => root.render(createElement(PanelDesktop,{device:"other",visible:true,t})));
    expect(sockets).toHaveLength(2);
  } finally {
    await act(() => root.unmount());
    expect(sockets.at(-1).close).toHaveBeenCalledTimes(1);
    host.remove(); localStorage.clear();
    vi.restoreAllMocks(); vi.unstubAllGlobals(); delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
