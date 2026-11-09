import { JSDOM } from "jsdom";
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { ServerCapabilitiesProvider, useServerCapabilities } from "../ui/lib/server-capabilities";

it.each([{}, { browserEnabled: "false" }, { browserEnabled: "true" }])("only enables browser when explicitly advertised: %j", async config => {
 const dom = new JSDOM('<div id="root"></div>');
 Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });
 let resolve;
 vi.stubGlobal("fetch", () => new Promise(r => { resolve = r; }));
 const states = [];
 function Probe() { const state = useServerCapabilities(); states.push(state); return state.browserEnabled ? createElement("a", { href: "/browser" }, "Browser") : null; }
 const root = createRoot(document.getElementById("root"));
 try {
  await act(() => root.render(createElement(ServerCapabilitiesProvider, null, createElement(Probe))));
  expect(document.querySelector("a")).toBeNull();
  await act(async () => resolve(new Response(JSON.stringify(config))));
  expect(!!document.querySelector("a")).toBe(config.browserEnabled === "true");
  expect(states.at(-1).loading).toBe(false);
 } finally { await act(() => root.unmount()); vi.unstubAllGlobals(); dom.window.close(); }
});
