// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
const api = vi.hoisted(() => ({ fetchDevices: vi.fn() }));
vi.mock("../ui/lib/api", () => ({ ...api, authURL: path => path, UnauthorizedError: class extends Error {} }));
import { DevicesProvider, useDevices } from "../ui/lib/devices";

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); });

it("refreshes on presence events, coalesces an in-flight change and closes its stream", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  let stream;
  class Events {
    constructor(url) { this.url = url; stream = this; }
    addEventListener(_, fn) { this.change = fn; }
    close = vi.fn();
  }
  vi.stubGlobal("EventSource", Events);
  let resolvePending;
  api.fetchDevices.mockReset().mockResolvedValueOnce([{ id: "first" }])
    .mockImplementationOnce(() => new Promise(resolve => { resolvePending = resolve; }))
    .mockResolvedValue([{ id: "replacement" }]);
  let devices;
  function Consumer() { devices = useDevices().devices; return null; }
  const host = document.createElement("div"); document.body.append(host);
  const root = createRoot(host);
  try {
    await act(async () => root.render(createElement(DevicesProvider, null, createElement(Consumer))));
    expect(devices.map(d => d.id)).toEqual(["first"]);
    expect(stream.url).toBe("/api/clients/events");
    await act(async () => { void stream.change(); });
    await act(async () => { void stream.change(); void stream.change(); });
    expect(api.fetchDevices).toHaveBeenCalledTimes(2);
    await act(async () => resolvePending([]));
    expect(api.fetchDevices).toHaveBeenCalledTimes(3);
    expect(devices.map(d => d.id)).toEqual(["replacement"]);
  } finally {
    await act(async () => root.unmount());
    expect(stream.close).toHaveBeenCalledOnce();
    host.remove(); delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
