// Runs inside Chrome's native inspector, sharing its existing CDP connection.
// Only rdev's parent frame may control the preview layout.
let app;
let showTools = false;
const notify = message => parent.postMessage(message, location.origin);
const applyLayout = () => {
  if (!app?.rootSplitWidget || !app.screencastView) return;
  if (showTools) app.rootSplitWidget.showBoth();
  else app.rootSplitWidget.hideSidebar();
};
window.addEventListener("message", async event => {
  if (event.origin !== location.origin || event.source !== parent) return;
  if (event.data?.type === "rdev-browser-tools") {
    showTools = event.data.visible === true;
    applyLayout();
  }

});
const reveal = () => document.getElementById("rdev-preview-loading")?.remove();
try {
  const { ScreencastApp } = await import("./panels/screencast/screencast.js");
  const deadline = Date.now() + 15000;
  while (Date.now() < deadline) {
    try { app = ScreencastApp.ScreencastApp.instance(); } catch { /* Inspector initialization is asynchronous. */ }
    if (app?.rootSplitWidget && app.screencastView) break;
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  if (!app?.rootSplitWidget || !app.screencastView) throw new Error("Unsupported Chrome preview API");
  // Keep the requested layout after target reconnects and screencast changes.
  const update = app.onScreencastEnabledChanged.bind(app);
  app.onScreencastEnabledChanged = () => { update(); applyLayout(); };
  app.enabledSetting.set(true);
  app.onScreencastEnabledChanged();
  const { installBrowserInput } = await import("./rdev-input.js");
  installBrowserInput(app, notify);
  reveal();
  notify({ type: "rdev-browser-ready" });
} catch {
  reveal();
  notify({ type: "rdev-browser-bridge-error" });
}
