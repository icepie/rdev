import { readFileSync } from "node:fs";
import vm from "node:vm";
import { expect, it, vi } from "vitest";

it("defaults to preview only and restricts parent controls to the same origin", async () => {
 const original = readFileSync(new URL("../../internal/server/browser_bridge.js", import.meta.url), "utf8");
 const metrics = vi.fn(async () => ({}));
 const bringToFront = vi.fn(async () => ({}));
 const surface = { contains: () => false };
 const app = {
  rootSplitWidget: { showBoth: vi.fn(), hideSidebar: vi.fn() }, screencastView: { contentElement: surface },
  screenCaptureModel: { target: () => ({ pageAgent: () => ({ invoke_bringToFront: bringToFront }), emulationAgent: () => ({invoke_setDeviceMetricsOverride:metrics}) }) },
  enabledSetting: { set: vi.fn() }, onScreencastEnabledChanged: vi.fn(),
 };
 const parent = { postMessage: vi.fn() };
 let receive;
 const documentListeners = {};
 const context = {
  parent, location: { origin: "https://rdev.test" },
  window: { addEventListener: (name, listener) => { if (name === "message") receive = listener; } },
  document: { addEventListener: (name, listener) => { documentListeners[name] = listener; }, visibilityState: "visible", getElementById: () => ({ remove() {} }) }, setTimeout,
  loadInput: async () => ({ installBrowserInput: vi.fn() }),
  loadScreencast: async () => ({ ScreencastApp: { ScreencastApp: { instance: () => app } } }),
 };
 // Replace only module loading; exercise the shipped bridge's event contract.
 await vm.runInNewContext(`(async () => {${original.replace('import("./panels/screencast/screencast.js")', 'loadScreencast()').replace('import("./rdev-input.js")', 'loadInput()')}})()`, context);
 expect(app.rootSplitWidget.hideSidebar).toHaveBeenCalledOnce();
 expect(app.rootSplitWidget.showBoth).not.toHaveBeenCalled();
 const send = data => receive({ origin: context.location.origin, source: parent, data });
 await send({type:"rdev-browser-viewport",width:390,height:844});
 expect(metrics).toHaveBeenCalledWith({width:390,height:844,deviceScaleFactor:1,mobile:false});
 await send({type:"rdev-browser-viewport",width:999999,height:844});
 await receive({origin:"https://other.test",source:parent,data:{type:"rdev-browser-viewport",width:800,height:600}});
 expect(metrics).toHaveBeenCalledTimes(1);
 await send({ type: "rdev-browser-tools", visible: true });
 expect(app.rootSplitWidget.showBoth).toHaveBeenCalledOnce();
 await receive({ origin: "https://other.test", source: parent, data: { type: "rdev-browser-tools", visible: false } });
 expect(app.rootSplitWidget.hideSidebar).toHaveBeenCalledOnce();
 await receive({ origin: "https://other.test", source: parent, data: { type: "rdev-browser-visible", visible: true } });
 expect(bringToFront).not.toHaveBeenCalled();
 await send({ type: "rdev-browser-visible", visible: true });
 await new Promise(resolve => setTimeout(resolve, 0));
 expect(bringToFront).toHaveBeenCalledOnce();
 documentListeners.pointerdown({ type: "pointerdown", composedPath: () => [surface] });
 await new Promise(resolve => setTimeout(resolve, 0));
 expect(bringToFront).toHaveBeenCalledTimes(2);
 await send({ type: "rdev-browser-visible", visible: false });
 documentListeners.pointerdown({ type: "pointerdown", composedPath: () => [surface] });
 expect(bringToFront).toHaveBeenCalledTimes(2);
});
