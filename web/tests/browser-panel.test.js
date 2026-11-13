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

it.each(["inline", "sidebar"])("%s selection starts the chosen device, connects DevTools and releases the browser", async deviceSelection => {
 apiFetch.mockReset();
 const dom = new JSDOM('<div id="root"></div>', { url: "https://rdev.test/browser" });
 Object.assign(globalThis, { window: dom.window, document: dom.window.document, location: dom.window.location, IS_REACT_ACT_ENVIRONMENT: true });
 apiFetch.mockResolvedValue(new Response(JSON.stringify({ base: "/api/browser/view/test-cap" })));
 for (const key of ["HTMLFormElement", "HTMLSelectElement", "HTMLElement", "Element", "Node", "MutationObserver", "Event", "CustomEvent"]) {
  vi.stubGlobal(key, dom.window[key]);
 }
 const originalFetch = globalThis.fetch;
  const fetched = vi.fn(async (url, options) => options?.method === "DELETE" ? new Response(null,{status:204}) : url.endsWith("/navigate") ? new Response(null,{status:204}) : new Response(JSON.stringify([{id:"page-1",title:"Test",url:"about:blank"}])));
 globalThis.fetch = fetched;
 const root = createRoot(document.getElementById("root"));
 try {
  await act(() => root.render(createElement(BrowserPanel,{device:"laptop",deviceSelection})));
  expect(apiFetch).not.toHaveBeenCalled();
  expect(!!document.querySelector('[role="combobox"]')).toBe(deviceSelection === "inline");
  expect(document.querySelector('input[type="password"]')).toBeNull();
  await act(async () => document.querySelector("form").dispatchEvent(new dom.window.Event("submit",{bubbles:true,cancelable:true})));
	  expect(JSON.parse(apiFetch.mock.calls[0][1].body)).toEqual({device:"laptop",password:"",lang:"en"});
  const frame = document.querySelector("iframe");
  const source = new URL(frame.src);
  expect(source.searchParams.get("wss")).toBe("rdev.test/api/browser/view/test-cap/devtools/page/page-1");
  expect(source.searchParams.has("token")).toBe(false);
  await act(() => window.dispatchEvent(new dom.window.MessageEvent("message", {origin:location.origin, source:frame.contentWindow, data:{type:"rdev-browser-ready"}})));
  await act(async () => { await Promise.resolve(); await Promise.resolve(); });
  const address = document.querySelector('[aria-label="browser.address"]');
  expect(address).not.toBeNull();
  const propsKey = Object.keys(address).find(key => key.startsWith("__reactProps$"));
  await act(() => address[propsKey].onChange({target:{value:"example.test"}}));
  expect(address.value).toBe("example.test");
  await act(async () => {
   address.closest("form").dispatchEvent(new dom.window.Event("submit", {bubbles:true,cancelable:true}));
   await Promise.resolve();
  });
  expect(address.value).toBe("https://example.test");
  expect(fetched).toHaveBeenCalledWith("/api/browser/view/test-cap/navigate", expect.objectContaining({method:"POST",body:JSON.stringify({target:"page-1",url:"https://example.test"})}));
  const tools = [...document.querySelectorAll("button")].find(b=>b.textContent==="DevTools");
  await act(() => tools.click());
  expect(tools.getAttribute("aria-pressed")).toBe("true");
  await act(() => root.render(createElement(BrowserPanel,{device:"laptop",deviceSelection,visible:false})));
  expect(document.querySelector("iframe")).toBe(frame);
  await act(() => root.render(createElement(BrowserPanel,{device:"laptop",deviceSelection,visible:true})));
  expect(document.querySelector("iframe")).toBe(frame);
  expect([...document.querySelectorAll("button")].find(b=>b.textContent==="DevTools").getAttribute("aria-pressed")).toBe("true");
  expect(apiFetch).toHaveBeenCalledTimes(1);
  await act(async () => [...document.querySelectorAll("button")].find(b=>b.textContent==="browser.stop").click());
  expect(fetched).toHaveBeenCalledWith("/api/browser/view/test-cap/session",{method:"DELETE"});
  expect(document.querySelector("iframe")).toBeNull();
 } finally { await act(() => root.unmount()); globalThis.fetch = originalFetch; vi.unstubAllGlobals(); dom.window.close(); }
});

it("attaches the conversation target without silently selecting another thread's tab", async () => {
 const dom = new JSDOM('<div id="root"></div>', { url: "https://rdev.test/agent" });
 Object.assign(globalThis, { window: dom.window, document: dom.window.document, location: dom.window.location, IS_REACT_ACT_ENVIRONMENT: true });
 for (const key of ["HTMLFormElement", "HTMLSelectElement", "HTMLElement", "Element", "Node", "MutationObserver", "Event", "CustomEvent"]) vi.stubGlobal(key, dom.window[key]);
 const originalFetch = globalThis.fetch;
 apiFetch.mockReset();
 globalThis.fetch = vi.fn(async () => new Response(JSON.stringify([{id:"other-thread",title:"Other",url:"about:blank"}])));
 const root = createRoot(document.getElementById("root"));
 try {
   const automation = {kind:"browser", device:"laptop", target:"owned-page", base:`/api/browser/view/${"a".repeat(64)}`, paused:false, action:"navigate"};
   await act(() => root.render(createElement(BrowserPanel,{device:"laptop",automation})));
   const frame = document.querySelector('iframe');
   await act(() => root.render(createElement(BrowserPanel,{device:'laptop',automation,visible:false})));
   expect(document.querySelector('iframe')).toBe(frame);
   expect(document.querySelector('[inert]')).toBeNull();
   expect(document.querySelector('img')).toBeNull();
   expect(document.body.textContent).not.toContain('browser.replay');
   expect(apiFetch).not.toHaveBeenCalled();
   expect(new URL(document.querySelector("iframe").src).searchParams.get("wss")).toContain("/page/owned-page");
   expect(document.querySelector('[aria-label="browser.tabs"]').disabled).toBe(true);
   expect([...document.querySelectorAll("button")].find(b=>b.textContent==="browser.stop").disabled).toBe(true);
   await act(() => root.render(createElement(BrowserPanel,{device:"laptop"})));
   expect(document.querySelector("iframe")).toBeNull();
   expect(document.querySelector("form")).not.toBeNull();
 } finally {await act(() => root.unmount());globalThis.fetch=originalFetch;vi.unstubAllGlobals();dom.window.close();}
});

it("restarts an expired browser capability once after a targets 404", async () => {
 const dom = new JSDOM('<div id="root"></div>', { url: "https://rdev.test/browser" });
 Object.assign(globalThis, { window: dom.window, document: dom.window.document, location: dom.window.location, IS_REACT_ACT_ENVIRONMENT: true });
 for (const key of ["HTMLFormElement", "HTMLSelectElement", "HTMLElement", "Element", "Node", "MutationObserver", "Event", "CustomEvent"]) vi.stubGlobal(key, dom.window[key]);
 const originalFetch = globalThis.fetch;
 apiFetch.mockReset();
 apiFetch.mockResolvedValueOnce(new Response(JSON.stringify({base:"/api/browser/view/old-cap"})))
   .mockResolvedValueOnce(new Response(JSON.stringify({base:"/api/browser/view/new-cap"})));
 let oldTargetRequests = 0;
 let staleSessionClosed = false;
 globalThis.fetch = vi.fn(async (url, options={}) => {
   if (url === "/api/browser/view/old-cap/session" && options.method === "DELETE") { staleSessionClosed=true; return new Response(null,{status:204}); }
   if (url.endsWith("/language")) return new Response(null,{status:204});
   if (url === "/api/browser/view/old-cap/targets") { oldTargetRequests++; return new Response("expired",{status:404}); }
   if (url === "/api/browser/view/new-cap/targets") return new Response(JSON.stringify([{id:"page-2",title:"Recovered",url:"about:blank"}]));
   throw new Error(`unexpected fetch ${url}`);
 });
 const root = createRoot(document.getElementById("root"));
 try {
   await act(() => root.render(createElement(BrowserPanel,{device:"laptop"})));
   await act(async () => document.querySelector("form").dispatchEvent(new dom.window.Event("submit",{bubbles:true,cancelable:true})));
   await act(async () => new Promise(resolve => setTimeout(resolve,25)));
   expect(apiFetch).toHaveBeenCalledTimes(2);
   expect(JSON.parse(apiFetch.mock.calls[1][1].body).device).toBe("laptop");
   expect(oldTargetRequests).toBe(1);
   expect(staleSessionClosed).toBe(true);
 } finally { await act(() => root.unmount()); globalThis.fetch=originalFetch; vi.unstubAllGlobals(); dom.window.close(); }
});
