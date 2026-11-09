import { readFileSync } from "node:fs";
import vm from "node:vm";
import { expect, it, vi } from "vitest";

it("defaults to preview only and restricts parent controls to the same origin", async () => {
 const original = readFileSync(new URL("../../internal/server/browser_bridge.js", import.meta.url), "utf8");
 const app = {
  rootSplitWidget: { showBoth: vi.fn(), hideSidebar: vi.fn() }, screencastView: {},
  enabledSetting: { set: vi.fn() }, onScreencastEnabledChanged: vi.fn(),
 };
 const parent = { postMessage: vi.fn() };
 let receive;
 const context = {
  parent, location: { origin: "https://rdev.test" },
  window: { addEventListener: (name, listener) => { receive = listener; } },
  document: { getElementById: () => ({ remove() {} }) }, setTimeout,
  loadInput: async () => ({ installBrowserInput: vi.fn() }),
  loadScreencast: async () => ({ ScreencastApp: { ScreencastApp: { instance: () => app } } }),
 };
 // Replace only module loading; exercise the shipped bridge's event contract.
 await vm.runInNewContext(`(async () => {${original.replace('import("./panels/screencast/screencast.js")', 'loadScreencast()').replace('import("./rdev-input.js")', 'loadInput()')}})()`, context);
 expect(app.rootSplitWidget.hideSidebar).toHaveBeenCalledOnce();
 expect(app.rootSplitWidget.showBoth).not.toHaveBeenCalled();
 const send = data => receive({ origin: context.location.origin, source: parent, data });
 await send({ type: "rdev-browser-tools", visible: true });
 expect(app.rootSplitWidget.showBoth).toHaveBeenCalledOnce();
 await receive({ origin: "https://other.test", source: parent, data: { type: "rdev-browser-tools", visible: false } });
 expect(app.rootSplitWidget.hideSidebar).toHaveBeenCalledOnce();
});
