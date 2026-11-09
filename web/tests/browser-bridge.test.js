import { readFileSync } from "node:fs";
import vm from "node:vm";
import { expect, it, vi } from "vitest";

it("defaults to preview only and inserts composed Unicode through the native CDP session", async () => {
 const original = readFileSync(new URL("../../internal/server/browser_bridge.js", import.meta.url), "utf8");
 const insertText = vi.fn(async () => ({ getError: () => undefined }));
 const app = {
  rootSplitWidget: { showBoth: vi.fn(), hideSidebar: vi.fn() }, screencastView: {},
  enabledSetting: { set: vi.fn() }, onScreencastEnabledChanged: vi.fn(),
  screenCaptureModel: { target: () => ({ inputAgent: () => ({ invoke_insertText: insertText }) }) },
 };
 const parent = { postMessage: vi.fn() };
 let receive;
 const context = {
  parent, location: { origin: "https://rdev.test" },
  window: { addEventListener: (name, listener) => { receive = listener; } },
  document: { getElementById: () => ({ remove() {} }) }, setTimeout,
  loadScreencast: async () => ({ ScreencastApp: { ScreencastApp: { instance: () => app } } }),
 };
 // Replace only module loading; exercise the shipped bridge's event contract.
 await vm.runInNewContext(`(async () => {${original.replace('import("./panels/screencast/screencast.js")', 'loadScreencast()')}})()`, context);
 expect(app.rootSplitWidget.hideSidebar).toHaveBeenCalledOnce();
 expect(app.rootSplitWidget.showBoth).not.toHaveBeenCalled();
 const send = data => receive({ origin: context.location.origin, source: parent, data });
 await send({ type: "rdev-browser-tools", visible: true });
 expect(app.rootSplitWidget.showBoth).toHaveBeenCalledOnce();
 await receive({ origin: "https://other.test", source: parent, data: { type: "rdev-browser-insert-text", text: "ignored" } });
 expect(insertText).not.toHaveBeenCalled();
 await send({ type: "rdev-browser-insert-text", id: "ime-1", text: "你好，世界 🌏\n第二行" });
 expect(insertText).toHaveBeenCalledWith({ text: "你好，世界 🌏\n第二行" });
 expect(parent.postMessage).toHaveBeenCalledWith({ type: "rdev-browser-text-result", id: "ime-1" }, "https://rdev.test");
});
