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
 const fetched = vi.fn(async (url, options) => options?.method === "DELETE" ? new Response(null,{status:204}) : new Response(JSON.stringify([{id:"page-1",title:"Test",url:"about:blank"}])));
 globalThis.fetch = fetched;
 const root = createRoot(document.getElementById("root"));
 try {
  await act(() => root.render(createElement(BrowserPanel,{device:"laptop",deviceSelection})));
  expect(apiFetch).not.toHaveBeenCalled();
  expect(!!document.querySelector('[role="combobox"]')).toBe(deviceSelection === "inline");
  expect(document.querySelector('input[type="password"]')).toBeNull();
  await act(async () => document.querySelector("form").dispatchEvent(new dom.window.Event("submit",{bubbles:true,cancelable:true})));
  expect(JSON.parse(apiFetch.mock.calls[0][1].body)).toEqual({device:"laptop",password:""});
  const frame = document.querySelector("iframe");
  const postMessage = vi.spyOn(frame.contentWindow, "postMessage");
  const source = new URL(frame.src);
  expect(source.searchParams.get("wss")).toBe("rdev.test/api/browser/view/test-cap/devtools/page/page-1");
  expect(source.searchParams.has("token")).toBe(false);
  await act(() => window.dispatchEvent(new dom.window.MessageEvent("message", {origin:location.origin, source:frame.contentWindow, data:{type:"rdev-browser-ready"}})));
  const tools = [...document.querySelectorAll("button")].find(b=>b.textContent==="DevTools");
  await act(() => tools.click());
  expect(tools.getAttribute("aria-pressed")).toBe("true");
  await act(() => root.render(createElement(BrowserPanel,{device:"laptop",deviceSelection,visible:false})));
  expect(document.querySelector("iframe")).toBe(frame);
  expect(postMessage).toHaveBeenLastCalledWith({type:"rdev-browser-visible",visible:false},location.origin);
  await act(() => root.render(createElement(BrowserPanel,{device:"laptop",deviceSelection,visible:true})));
  expect(document.querySelector("iframe")).toBe(frame);
  expect([...document.querySelectorAll("button")].find(b=>b.textContent==="DevTools").getAttribute("aria-pressed")).toBe("true");
  expect(postMessage).toHaveBeenLastCalledWith({type:"rdev-browser-visible",visible:true},location.origin);
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
   const history = [{id:'step-1',device:'laptop',action:'click',startedAt:1,durationMs:20,ok:true,thumbnail:'data:image/jpeg;base64,YQ==',width:1280,height:800}];
   const onReplay = vi.fn();
   await act(() => root.render(createElement(BrowserPanel,{device:'laptop',automation,history,replayId:'step-1',onReplay})));
   expect(document.querySelector('iframe')).toBe(frame);
   expect(document.querySelector('[inert]')).not.toBeNull();
   expect(document.querySelector('img[alt*="browser.replay"]')).not.toBeNull();
   await act(()=>[...document.querySelectorAll('button')].find(b=>b.textContent==='browser.live').click());
   expect(onReplay).toHaveBeenCalledWith('');
   await act(() => root.render(createElement(BrowserPanel,{device:'laptop',automation,history,replayId:'',onReplay})));
   expect(document.querySelector('iframe')).toBe(frame);
   expect(document.querySelector('[inert]')).toBeNull();
   expect(apiFetch).not.toHaveBeenCalled();
   expect(new URL(document.querySelector("iframe").src).searchParams.get("wss")).toContain("/page/owned-page");
   expect(document.querySelector('[aria-label="browser.tabs"]').disabled).toBe(true);
   expect([...document.querySelectorAll("button")].find(b=>b.textContent==="browser.stop").disabled).toBe(true);
   await act(() => root.render(createElement(BrowserPanel,{device:"laptop"})));
   expect(document.querySelector("iframe")).toBeNull();
   expect(document.querySelector("form")).not.toBeNull();
 } finally {await act(() => root.unmount());globalThis.fetch=originalFetch;vi.unstubAllGlobals();dom.window.close();}
});
