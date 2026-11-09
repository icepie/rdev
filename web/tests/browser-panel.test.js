import { JSDOM } from "jsdom";
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
const apiFetch = vi.fn();
const t = key => key;
vi.mock("../ui/lib/api", () => ({ apiFetch: (...args) => apiFetch(...args), getDevicePassword: () => "" }));
vi.mock("../ui/lib/i18n", () => ({ useI18n: () => ({ t }) }));
vi.mock("../ui/lib/devices", () => ({ useDevices: () => ({ devices: [{ id: "laptop" }] }) }));
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
const { BrowserPanel } = await import("../ui/components/browser-panel");

it("starts only on request, connects native DevTools over wss and releases the browser", async () => {
 const dom = new JSDOM('<div id="root"></div>', { url: "https://rdev.test/browser" });
 Object.assign(globalThis, { window: dom.window, document: dom.window.document, location: dom.window.location, IS_REACT_ACT_ENVIRONMENT: true });
 apiFetch.mockResolvedValue(new Response(JSON.stringify({ base: "/api/browser/view/test-cap" })));
 for (const key of ["HTMLFormElement", "HTMLSelectElement", "HTMLElement", "Element", "Node", "MutationObserver", "Event", "CustomEvent"]) {
  vi.stubGlobal(key, dom.window[key]);
 }
 const originalFetch = globalThis.fetch;
 const fetched = vi.fn(async (url, options) => options?.method === "DELETE" ? new Response(null,{status:204}) : new Response(JSON.stringify([{id:"page-1",title:"Test",url:"about:blank"}])));
 globalThis.fetch = fetched;
 const root = createRoot(document.getElementById("root"));
 try {
  await act(() => root.render(createElement(BrowserPanel,{device:"laptop"})));
  expect(apiFetch).not.toHaveBeenCalled();
  await act(async () => document.querySelector("form").dispatchEvent(new dom.window.Event("submit",{bubbles:true,cancelable:true})));
  expect(JSON.parse(apiFetch.mock.calls[0][1].body)).toEqual({device:"laptop",password:""});
  const frame = document.querySelector("iframe");
  const source = new URL(frame.src);
  expect(source.searchParams.get("wss")).toBe("rdev.test/api/browser/view/test-cap/devtools/page/page-1");
  expect(source.searchParams.has("token")).toBe(false);
  await act(async () => [...document.querySelectorAll("button")].find(b=>b.textContent==="browser.stop").click());
  expect(fetched).toHaveBeenCalledWith("/api/browser/view/test-cap/session",{method:"DELETE"});
  expect(document.querySelector("iframe")).toBeNull();
 } finally { await act(() => root.unmount()); globalThis.fetch = originalFetch; vi.unstubAllGlobals(); dom.window.close(); }
});
