// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { DesktopCanvasInput, DesktopHeldInput } from "../ui/lib/desktop-input";

it("releases only held physical keys across repeat and modifier changes", () => {
  const held = new DesktopHeldInput();
  held.sent({ inputType: "key_down", code: "KeyA", key: "A" });
  held.sent({ inputType: "key_down", code: "KeyA", key: "a" });
  held.sent({ inputType: "key_up", code: "KeyA", key: "a" });
  held.sent({ inputType: "key_down", code: "ControlLeft", key: "Control", ctrlKey: true });
  const events = [];
  held.release(event => events.push(event));
  held.release(event => events.push(event));
  expect(events).toEqual([{ inputType: "key_up", code: "ControlLeft", key: "Control", ctrlKey: false, altKey: false, shiftKey: false, metaKey: false }]);
});

it("releases a cancelled pointer's original button at the last delivered position", () => {
  const held = new DesktopHeldInput();
  for (const [pointerId, button] of [[1, 2], [2, 0]]) {
    held.sent({ inputType: "mouse_down", x: 10, y: 20, pointerId, button });
  }
  held.sent({ inputType: "mouse_move", x: 30, y: 40, pointerId: 1, button: -1 });
  const events = [];
  held.releasePointer(1, event => events.push(event));
  held.releasePointer(1, event => events.push(event));
  expect(events).toEqual([{ inputType: "mouse_up", x: 30, y: 40, pointerId: 1, button: 2, pressure: 0 }]);
  held.clear();
  held.release(event => events.push(event));
  expect(events).toHaveLength(1);
});

afterEach(() => {
  document.body.replaceChildren();
  vi.unstubAllGlobals();
});

function viewer(extra = {}) {
  const frames = [];
  vi.stubGlobal("requestAnimationFrame", callback => frames.push(callback));
  // Invoke even cancelled callbacks to exercise stale-frame guards.
  vi.stubGlobal("cancelAnimationFrame", () => {});
  const canvas = document.createElement("canvas");
  canvas.tabIndex = 0;
  canvas.width = 200; canvas.height = 100;
  canvas.getBoundingClientRect = () => ({ left: 10, top: 20, width: 102, height: 52 });
  Object.defineProperties(canvas, {
    clientLeft: { value: 1 }, clientTop: { value: 1 },
    clientWidth: { value: 100 }, clientHeight: { value: 50 },
  });
  document.body.append(canvas);
  const messages = [];
  const state = { mode: "control", socket: { readyState: 1, bufferedAmount: 0,
    send: data => messages.push(JSON.parse(data)) } };
  const input = new DesktopCanvasInput();
  const detach = input.attach(canvas, { socket: () => state.socket, mode: () => state.mode, globalKeyboard: true, ...extra });
  function dispatch(type, props = {}) {
    const init = { clientX: 11, clientY: 21, cancelable: true, bubbles: true, ...props };
    const event = type.startsWith("key") ? new KeyboardEvent(type, init) : type === "wheel" ? new WheelEvent(type, init) : new MouseEvent(type, init);
    if (type.startsWith("pointer")) Object.defineProperties(event, {
      pointerId: { value: props.pointerId ?? 1 }, pointerType: { value: "mouse" }, pressure: { value: 0.5 },
    });
    if (props.movementX !== undefined) {
      Object.defineProperty(event, "movementX", { value: props.movementX });
      Object.defineProperty(event, "movementY", { value: props.movementY ?? 0 });
    }
    canvas.dispatchEvent(event);
    return event;
  }
  return { canvas, input, state, detach, dispatch, messages, frames };
}

it("keeps the latest move per pointer, including the last one after congestion clears", () => {
  const { dispatch, state, messages, frames } = viewer();
  state.socket.bufferedAmount = 150_000;
  dispatch("pointermove", { pointerId: 2, clientX: 18 });
  dispatch("pointermove", { pointerId: 1, clientX: 16 });
  dispatch("pointermove", { pointerId: 2, clientX: 21, altKey: true });
  expect(frames).toHaveLength(1);
  frames[0]();
  expect(messages).toEqual([]);
  state.socket.bufferedAmount = 0;
  frames[1]();
  expect(messages.map(m => [m.pointerId, m.x, m.altKey])).toEqual([[1, 10, false], [2, 20, true]]);
});

it("orders chorded mouse buttons around coalesced motion, clamping a captured release at the edge", () => {
  const { dispatch, canvas, messages, frames } = viewer();
  expect(dispatch("pointerdown", { clientX: 10, button: 0, buttons: 1 }).defaultPrevented).toBe(false); // border
  expect(dispatch("pointerdown", { button: 0, buttons: 1 }).defaultPrevented).toBe(true);
  expect(document.activeElement).toBe(canvas);
  dispatch("pointermove", { button: -1, buttons: 1, clientX: 16 });
  dispatch("pointermove", { button: 2, buttons: 3, clientX: 17, shiftKey: true });
  dispatch("pointermove", { button: 0, buttons: 2, clientX: 18 });
  dispatch("pointerup", { button: 2, buttons: 0, clientX: 200 });
  frames[0]();
  expect(messages.map(m => [m.inputType, m.button, m.x])).toEqual([
    ["mouse_down", 0, 0], ["mouse_move", -1, 10], ["mouse_down", 2, 12], ["mouse_up", 0, 14], ["mouse_up", 2, 199],
  ]);
  expect(messages[2].shiftKey).toBe(true);
});

it("cancels stale movement on cancel, blur, hiding, socket replacement and detach", () => {
  const { dispatch, state, detach, messages, frames, canvas } = viewer();
  dispatch("pointermove");
  dispatch("pointercancel");
  dispatch("pointermove");
  frames[0]();
  canvas.dispatchEvent(new Event("blur"));
  frames[1]();
  dispatch("pointermove");
  state.mode = null;
  frames[2]();
  state.mode = "control";
  dispatch("pointermove");
  state.socket = { ...state.socket };
  frames[3]();
  dispatch("pointermove");
  detach();
  frames[4]();
  expect(messages).toEqual([]);
});

it("never upgrades cursor-only input or consumes local shortcuts in view-only mode", () => {
  const { dispatch, state, frames, messages, canvas } = viewer();
  state.mode = "cursor";
  dispatch("pointermove");
  expect(dispatch("pointerdown").defaultPrevented).toBe(false);
  expect(dispatch("wheel", { deltaY: 1 }).defaultPrevented).toBe(false);
  expect(dispatch("contextmenu").defaultPrevented).toBe(false);
  canvas.focus();
  expect(dispatch("keydown", { code: "KeyA", key: "a" }).defaultPrevented).toBe(false);
  frames[0]();
  dispatch("pointermove");
  state.mode = "control";
  frames[1]();
  expect(messages.map(m => m.inputType)).toEqual(["cursor_move"]);
});

it("routes keyboard chords while control is active, flushes motion, and releases once on blur", () => {
  const { dispatch, state, messages, canvas } = viewer();
  const chord = { code: "KeyZ", key: "Z", metaKey: true, shiftKey: true };
  // Window-level capture means chords arrive without focusing the canvas.
  expect(dispatch("keydown", chord).defaultPrevented).toBe(true);
  canvas.focus();
  dispatch("pointermove");
  // A later key flushes the queued move first, keeping causal order.
  dispatch("keydown", { code: "KeyY", key: "y" });
  expect(messages.map(m => m.inputType)).toEqual(["key_down", "mouse_move", "key_down"]);
  expect(messages[0]).toMatchObject(chord);
  canvas.blur();
  const releases = messages.slice(3);
  expect(releases).toHaveLength(2);
  expect(releases.every(m => m.inputType === "key_up" && m.metaKey === false)).toBe(true);
  expect(releases.map(m => m.code).sort()).toEqual(["KeyY", "KeyZ"]);
  state.mode = null;
  expect(dispatch("keyup", chord).defaultPrevented).toBe(false);
  expect(messages).toHaveLength(5);
});

it("normalizes wheel line and page units to CSS pixels and retains fractional trackpad deltas", () => {
  const { dispatch, messages, canvas } = viewer();
  canvas.style.lineHeight = "20px";
  expect(dispatch("wheel", { deltaY: -0.6 }).defaultPrevented).toBe(true);
  expect(messages).toEqual([]);
  dispatch("wheel", { deltaY: -0.6, ctrlKey: true });
  dispatch("wheel", { deltaMode: 1, deltaX: 1.5, deltaY: -2 });
  dispatch("wheel", { deltaMode: 2, deltaX: 1, deltaY: 1.5 });
  expect(messages.map(m => [m.deltaX, m.deltaY])).toEqual([[0, -1], [30, -40], [100, 74]]);
  expect(messages[0]).toMatchObject({ inputType: "wheel", ctrlKey: true, x: 0, y: 0 });
});

it("captures keyboard at window level while control is active", () => {
  const { dispatch, messages, canvas } = viewer();
  document.body.focus();
  canvas.blur();
  expect(dispatch("keydown", { code: "KeyA", key: "a" }).defaultPrevented).toBe(true);
  expect(messages.map(m => m.inputType)).toEqual(["key_down"]);
  dispatch("keyup", { code: "KeyA", key: "a" });
  expect(messages.at(-1)).toMatchObject({ inputType: "key_up", code: "KeyA" });
});

it("leaves local text fields and view-only sessions alone", () => {
  const { dispatch, messages, state } = viewer();
  const field = document.createElement("input");
  document.body.append(field);
  field.focus();
  const typing = new KeyboardEvent("keydown", { code: "KeyB", key: "b", cancelable: true, bubbles: true });
  field.dispatchEvent(typing);
  expect(typing.defaultPrevented).toBe(false);
  expect(messages).toEqual([]);
  field.remove();
  state.mode = "cursor";
  expect(dispatch("keydown", { code: "KeyC", key: "c" }).defaultPrevented).toBe(false);
  expect(messages).toEqual([]);
});

it("does not steal keys from local controls such as selects and buttons", () => {
  const { dispatch, messages } = viewer();
  for (const role of ["combobox", "switch", "menuitem"]) {
    const control = document.createElement("div");
    control.setAttribute("role", role);
    control.tabIndex = 0;
    document.body.append(control);
    control.focus();
    const event = new KeyboardEvent("keydown", { key: "ArrowDown", code: "ArrowDown", cancelable: true, bubbles: true });
    control.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(false);
    control.remove();
  }
  expect(messages).toEqual([]);
});

it("drives the remote cursor from relative movement while the pointer is locked", () => {
  const locks = [];
  const { canvas, dispatch, messages, frames } = viewer({
    pointerLock: () => true,
    onPointerLockChange: locked => locks.push(locked),
  });
  canvas.requestPointerLock = () => {
    Object.defineProperty(document, "pointerLockElement", { value: canvas, configurable: true });
    document.dispatchEvent(new Event("pointerlockchange"));
  };
  dispatch("pointerdown", { button: 0, buttons: 1 });
  expect(locks.at(-1)).toBe(true);
  dispatch("pointermove", { movementX: 20, movementY: 10, buttons: 1 });
  frames.at(-1)();
  const move = messages.filter(m => m.inputType === "mouse_move").at(-1);
  expect(move).toMatchObject({ x: 139, y: 69 });
  Object.defineProperty(document, "pointerLockElement", { value: null, configurable: true });
  document.dispatchEvent(new Event("pointerlockchange"));
  expect(locks.at(-1)).toBe(false);
});

it("tracks the pointer across the whole page in global mode", () => {
  const { messages, frames } = viewer({ globalMouse: () => true });
  const outside = new MouseEvent("pointermove", { clientX: 400, clientY: 400, bubbles: true, cancelable: true });
  Object.defineProperties(outside, { pointerId: { value: 1 }, pointerType: { value: "mouse" }, pressure: { value: 0 } });
  document.body.dispatchEvent(outside);
  frames.at(-1)?.();
  const move = messages.filter(m => m.inputType === "mouse_move").at(-1);
  expect(move).toBeTruthy();
  expect(move.x).toBe(199);
  expect(move.y).toBe(99);
});
