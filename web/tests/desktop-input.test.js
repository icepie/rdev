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

function viewer() {
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
  const detach = input.attach(canvas, { socket: () => state.socket, mode: () => state.mode });
  function dispatch(type, props = {}) {
    const init = { clientX: 11, clientY: 21, cancelable: true, bubbles: true, ...props };
    const event = type.startsWith("key") ? new KeyboardEvent(type, init) : type === "wheel" ? new WheelEvent(type, init) : new MouseEvent(type, init);
    if (type.startsWith("pointer")) Object.defineProperties(event, {
      pointerId: { value: props.pointerId ?? 1 }, pointerType: { value: "mouse" }, pressure: { value: 0.5 },
    });
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

it("routes keyboard chords only while focused, flushes earlier motion, and releases once on blur", () => {
  const { dispatch, state, messages, canvas } = viewer();
  const chord = { code: "KeyZ", key: "Z", metaKey: true, shiftKey: true };
  expect(dispatch("keydown", chord).defaultPrevented).toBe(false);
  canvas.focus();
  dispatch("pointermove");
  const bubbled = vi.fn();
  document.body.addEventListener("keydown", bubbled, { once: true });
  expect(dispatch("keydown", chord).defaultPrevented).toBe(true);
  expect(bubbled).not.toHaveBeenCalled();
  document.body.removeEventListener("keydown", bubbled);
  expect(messages.map(m => m.inputType)).toEqual(["mouse_move", "key_down"]);
  expect(messages[1]).toMatchObject(chord);
  canvas.blur();
  expect(messages[2]).toMatchObject({ inputType: "key_up", code: "KeyZ", metaKey: false });
  state.mode = null;
  expect(dispatch("keyup", chord).defaultPrevented).toBe(false);
  expect(messages).toHaveLength(3);
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
