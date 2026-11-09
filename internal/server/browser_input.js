// A real local editing host is required for OS input methods. The canvas remains
// the pointer surface; only keyboard focus moves to this transparent textarea.
export function bindRemoteInput(host, { send, key, shortcut }) {
  let composing = false;
  let compositionText = "";
  let trailingCommit = null;
  let trailingTimer;
  const suppressedKeys = new Set();
  const listeners = [];
  const on = (name, listener) => { host.addEventListener(name, listener); listeners.push([name, listener]); };
  const clear = () => { host.value = ""; };
  const insert = text => { if (text) send("insertText", { text }); };
  const finish = text => {
    if (!composing) return;
    composing = false;
    compositionText = "";
    // insertText commits the active remote composition instead of appending it.
    if (text) insert(text);
    else send("imeSetComposition", { text: "", selectionStart: 0, selectionEnd: 0 });
    trailingCommit = text;
    clearTimeout(trailingTimer);
    trailingTimer = setTimeout(() => { trailingCommit = null; }, 0);
    clear();
  };
  on("compositionstart", () => { composing = true; compositionText = ""; trailingCommit = null; });
  on("compositionupdate", event => {
    if (!composing) return;
    compositionText = event.data || "";
    send("imeSetComposition", { text: compositionText, selectionStart: compositionText.length, selectionEnd: compositionText.length });
  });
  on("compositionend", event => finish(event.data || ""));
  on("input", event => {
    event.stopPropagation();
    if (composing || event.isComposing) return;
    if (trailingCommit !== null && event.data === trailingCommit) {
      trailingCommit = null; clear(); return;
    }
    insert(event.data ?? host.value);
    clear();
  });
  on("keydown", event => {
    event.stopPropagation();
    if (composing || event.isComposing || event.keyCode === 229 || event.key === "Process" || event.key === "Dead") {
      suppressedKeys.add(event.code); return;
    }
    if (shortcut?.(event)) { event.preventDefault(); suppressedKeys.add(event.code); return; }
    // Pasting is handled by the local clipboard event, never the server clipboard.
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "v") { suppressedKeys.add(event.code); return; }
    const altGraph = event.getModifierState("AltGraph");
    const printable = event.key.length === 1 && !event.metaKey && (!event.ctrlKey || altGraph) && (!event.altKey || altGraph);
    if (!printable) event.preventDefault();
    key(event, printable ? "rawKeyDown" : "keyDown");
  });
  on("keyup", event => {
    event.stopPropagation();
    if (suppressedKeys.delete(event.code) || composing || event.isComposing || event.keyCode === 229) return;
    key(event, "keyUp");
  });
  on("keypress", event => event.stopPropagation());
  on("beforeinput", event => {
    if (composing || event.isComposing) return;
    const special = { deleteContentBackward: "Backspace", deleteContentForward: "Delete", insertLineBreak: "Enter", insertParagraph: "Enter" }[event.inputType];
    if (!special) return;
    event.preventDefault(); event.stopPropagation();
    const code = special === "Backspace" ? 8 : special === "Delete" ? 46 : 13;
    const stroke = { key: special, code: special, keyCode: code, getModifierState: () => false };
    key(stroke, "keyDown"); key(stroke, "keyUp"); clear();
  });
  on("paste", event => {
    event.preventDefault(); event.stopPropagation();
    if (composing) finish(compositionText);
    insert(event.clipboardData?.getData("text/plain") || ""); clear();
  });
  on("blur", () => { if (composing) finish(compositionText); clear(); });
  return {
    finish: () => finish(compositionText),
    dispose() {
      clearTimeout(trailingTimer);
      for (const [name, listener] of listeners) host.removeEventListener(name, listener);
    },
  };
}

export function installBrowserInput(app, notify) {
  const host = document.createElement("textarea");
  host.setAttribute("aria-label", new URLSearchParams(location.search).get("rdevLang") === "zh" ? "网页输入" : "Remote browser input");
  host.tabIndex = -1;
  host.setAttribute("autocomplete", "off");
  host.setAttribute("autocapitalize", "off");
  host.spellcheck = false;
  host.style.cssText = "position:fixed;left:0;top:0;width:1px;height:20px;padding:0;border:0;opacity:0.01;pointer-events:none;resize:none;overflow:hidden;color:transparent;caret-color:transparent;background:transparent;font-size:16px;z-index:2147483647";
  document.body.append(host);
  let disposed = false;
  let queue = Promise.resolve();
  const enqueue = operation => {
    queue = queue.then(() => { if (!disposed) return operation(); }).catch(() => notify({ type: "rdev-browser-input-error" }));
  };
  const send = (method, params) => {
    const target = app.screenCaptureModel?.target();
    enqueue(() => {
      if (!target || target !== app.screenCaptureModel?.target()) return;
      // Preserve wire order without adding one network round-trip per keystroke.
      void target.inputAgent()[`invoke_${method}`](params).then(result => {
        if (result.getError?.()) throw new Error(result.getError());
      }).catch(() => notify({ type: "rdev-browser-input-error" }));
    });
  };
  const key = (event, type) => {
    const modifiers = Number(event.getModifierState("Alt")) | Number(event.getModifierState("Control")) << 1 | Number(event.getModifierState("Meta")) << 2 | Number(event.getModifierState("Shift")) << 3;
    send("dispatchKeyEvent", {
      type, modifiers, key: event.key, code: event.code,
      windowsVirtualKeyCode: event.keyCode, nativeVirtualKeyCode: event.keyCode,
      text: type === "keyDown" && event.key === "Enter" ? "\r" : undefined,
      autoRepeat: !!event.repeat, isKeypad: event.location === 3,
      location: event.location === 3 ? undefined : event.location || 0,
    });
  };
  const input = bindRemoteInput(host, { send, key, shortcut(event) {
    if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === "l" && typeof app.screencastView?.focusNavigationBar === "function") {
      app.screencastView.focusNavigationBar();
      return true;
    }
    return false;
  } });
  let pointerDown = false;
  let handingOffFocus = false;
  let canvas;
  const wrapped = new Map();
  const prepareView = () => {
    const model = app.screencastView?.inputModel;
    if (!model || wrapped.has(model)) return;
    const original = model.emitMouseEvent;
    wrapped.set(model, original);
    // Composition must commit before a click changes remote input focus.
    model.emitMouseEvent = function(event, ...args) {
      // DevTools synthesizes mouseup at (0,0) on canvas blur. The real
      // release already arrived before our IME host takes keyboard focus.
      if (handingOffFocus && event.type === "mouseup") return;
      const modifiers = new Set(["Alt", "Control", "Meta", "Shift"].filter(name => event.getModifierState(name)));
      const snapshot = { type: event.type, offsetX: event.offsetX, offsetY: event.offsetY, button: event.button, detail: event.detail, getModifierState: name => modifiers.has(name) };
      enqueue(() => original.call(this, snapshot, ...args));
    };
  };
  const isCanvasEvent = event => {
    const path = event.composedPath();
    if (!path.includes(app.screencastView?.contentElement)) return false;
    const target = path.find(node => node instanceof HTMLCanvasElement);
    if (!target) return false;
    canvas = target; prepareView(); return true;
  };
  const position = event => {
    host.style.left = `${Math.max(0, Math.min(event.clientX, innerWidth - 2))}px`;
    host.style.top = `${Math.max(0, Math.min(event.clientY, innerHeight - 22))}px`;
  };
  const focus = () => {
    if (disposed || !canvas?.isConnected) return;
    handingOffFocus = true;
    try { host.focus({ preventScroll: true }); }
    finally { handingOffFocus = false; }
  };
  const down = event => {
    if (!isCanvasEvent(event)) return;
    input.finish(); pointerDown = true; position(event);
  };
  const up = event => {
    if (!isCanvasEvent(event)) { pointerDown = false; return; }
    pointerDown = false; position(event); queueMicrotask(focus);
  };
  const focused = event => {
    if (isCanvasEvent(event) && !pointerDown) queueMicrotask(focus);
  };
  document.addEventListener("mousedown", down, true);
  document.addEventListener("mouseup", up, true);
  document.addEventListener("focusin", focused, true);
  const dispose = () => {
    disposed = true; input.dispose(); host.remove();
    document.removeEventListener("mousedown", down, true);
    document.removeEventListener("mouseup", up, true);
    document.removeEventListener("focusin", focused, true);
    for (const [model, original] of wrapped) model.emitMouseEvent = original;
  };
  window.addEventListener("pagehide", dispose, { once: true });
  return dispose;
}
