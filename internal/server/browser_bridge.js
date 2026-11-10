// Runs inside Chrome's native inspector, sharing its existing CDP connection.
// Only rdev's parent frame may control the preview layout.
let app;
let resize;
let showTools = false;
let visible = false;
let activating;
// Activate on user intent, never on every visibility event: several preview
// frames can share Chrome and must not keep stealing the foreground tab.
const activate = () => {
  if (!visible || document.visibilityState === "hidden" || activating) return;
  const target = app?.screenCaptureModel?.target();
  if (!target) return;
  activating = target.pageAgent().invoke_bringToFront().then(result => {
    if (result.getError?.()) throw new Error(result.getError());
  }).catch(() => notify({ type: "rdev-browser-input-error" })).finally(() => { activating = null; });
};
const enterPreview = event => {
  const surface = app?.screencastView?.contentElement;
  if (!surface || !event.composedPath().includes(surface)) return;
  if (event.type === "pointerover" && event.relatedTarget && surface.contains(event.relatedTarget)) return;
  activate();
};
document.addEventListener("pointerover", enterPreview, true);
document.addEventListener("pointerdown", enterPreview, true);
window.addEventListener("focus", activate);
document.addEventListener("visibilitychange", activate);
const notify = message => parent.postMessage(message, location.origin);
const applyLayout = () => {
  if (!app?.rootSplitWidget || !app.screencastView) return;
  if (showTools) app.rootSplitWidget.showBoth();
  else app.rootSplitWidget.hideSidebar();
};
window.addEventListener("message", async event => {
  if (event.origin !== location.origin || event.source !== parent) return;
  if (event.data?.type === "rdev-browser-visible") {
    visible = event.data.visible === true;
    if (visible) activate();
  }
  if (event.data?.type === "rdev-browser-viewport") {
    const { width, height } = event.data;
    if (!Number.isInteger(width) || !Number.isInteger(height) || width<240 || width>3840 || height<240 || height>2160) return;
    try {
      const target = app?.screenCaptureModel?.target();
      if (!target) throw new Error('Page unavailable');
      const result = await target.emulationAgent().invoke_setDeviceMetricsOverride({width,height,deviceScaleFactor:1,mobile:false});
      if (result.getError?.()) throw new Error(result.getError());
      resize?.set({width,height});
      notify({type:"rdev-browser-viewport-applied",width,height});
    } catch { notify({type:"rdev-browser-viewport-error"}); }
  }
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
  const { installBrowserResize } = await import("./rdev-resize.js");
  resize = installBrowserResize(app, notify);
  reveal();
  notify({ type: "rdev-browser-ready" });
} catch {
  reveal();
  notify({ type: "rdev-browser-bridge-error" });
}
