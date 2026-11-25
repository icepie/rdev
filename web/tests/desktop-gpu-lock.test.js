// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { attachDesktopStageInput } from "../ui/lib/desktop-stage-input";
import { gpuPointerMessage } from "../ui/lib/desktop-gpu-input";

afterEach(() => {
  document.body.replaceChildren();
  vi.unstubAllGlobals();
});

function stage({ lock = true } = {}) {
  const overlay = document.createElement("div");
  overlay.tabIndex = 0;
  overlay.getBoundingClientRect = () => ({ left: 0, top: 0, width: 1000, height: 500 });
  document.body.append(overlay);
  const sent = [];
  const input = { attach: vi.fn(() => () => {}) };
  const api = {
    gpuInputAllowed: () => true,
    gpuPointerTypes: () => ["mouse"],
    gpuSend: (message) => sent.push(message),
    gpuPointerMessage: (event, type, point) => ({ type, point, event }),
    gpuKeyMessage: () => ({ KeyboardEvent: {} }),
    gpuTextInput: () => {},
    gpuReleaseKeyboard: () => {},
    desktopReleaseKeyboard: () => {},
    pollClipboardSync: () => {},
  };
  const detach = attachDesktopStageInput({
    canvas: null,
    overlay,
    input,
    api: () => api,
    composing: { current: false },
    ignoreNextTextInput: { current: false },
    heldKeys: { current: new Map() },
    socket: () => null,
    mode: () => "control",
    pointerLock: () => lock,
    mouseEnabled: () => true,
    socketOpen: () => true,
    fail: () => {},
  });
  return { overlay, sent, detach };
}

function pointerEvent(type, props) {
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, ...props });
  Object.defineProperties(event, {
    pointerId: { value: 1 },
    pointerType: { value: "mouse" },
    movementX: { value: 0 },
    movementY: { value: 0 },
    ...(props.movementX !== undefined ? { movementX: { value: props.movementX } } : {}),
    ...(props.movementY !== undefined ? { movementY: { value: props.movementY } } : {}),
  });
  return event;
}

describe("GPU overlay pointer lock", () => {
  it("sends a virtual position built from relative movement while locked", () => {
    const { overlay, sent, detach } = stage();
    overlay.requestPointerLock = () => {
      Object.defineProperty(document, "pointerLockElement", { value: overlay, configurable: true });
      document.dispatchEvent(new Event("pointerlockchange"));
    };
    overlay.dispatchEvent(pointerEvent("pointerdown", { button: 0, buttons: 1 }));
    expect(sent.at(-1).type).toBe("pointerdown");
    expect(sent.at(-1).point).toBeUndefined();

    overlay.dispatchEvent(pointerEvent("pointermove", { movementX: 100, movementY: 50, buttons: 1 }));
    expect(sent.at(-1).type).toBe("pointermove");
    expect(sent.at(-1).point.x).toBeCloseTo(0.6);
    expect(sent.at(-1).point.y).toBeCloseTo(0.6);

    overlay.dispatchEvent(pointerEvent("pointermove", { movementX: -2000, movementY: 5000, buttons: 1 }));
    expect(sent.at(-1).point.x).toBeCloseTo(0);
    expect(sent.at(-1).point.y).toBeCloseTo(1);

    Object.defineProperty(document, "pointerLockElement", { value: null, configurable: true });
    document.dispatchEvent(new Event("pointerlockchange"));
    overlay.dispatchEvent(pointerEvent("pointermove", { clientX: 300, clientY: 100, buttons: 1 }));
    expect(sent.at(-1).point).toBeUndefined();
    detach();
  });

  it("keeps absolute coordinates when relative movement is disabled", () => {
    const { overlay, detach } = stage({ lock: false });
    overlay.requestPointerLock = vi.fn();
    overlay.dispatchEvent(pointerEvent("pointerdown", { button: 0, buttons: 1 }));
    expect(overlay.requestPointerLock).not.toHaveBeenCalled();
  });
});

describe("gpuPointerMessage point override", () => {
  it("uses the provided normalized point and ignores frozen client coordinates", () => {
    const event = new MouseEvent("pointermove", { clientX: 0, clientY: 0 });
    const message = gpuPointerMessage(event, {
      rect: new DOMRect(0, 0, 100, 100),
      previous: { x: 0, y: 0 },
      minPressure: "0",
      eventType: "pointermove",
      point: { x: 0.25, y: 1.5 },
    });
    expect(message.PointerEvent.x).toBe(0.25);
    expect(message.PointerEvent.y).toBe(1);
  });
});

it("releases pressed buttons when the lock drops", () => {
  const { overlay, sent, detach } = stage();
  overlay.requestPointerLock = () => {
    Object.defineProperty(document, "pointerLockElement", { value: overlay, configurable: true });
    document.dispatchEvent(new Event("pointerlockchange"));
  };
  overlay.dispatchEvent(pointerEvent("pointerdown", { button: 0, buttons: 1 }));
  Object.defineProperty(document, "pointerLockElement", { value: null, configurable: true });
  document.dispatchEvent(new Event("pointerlockchange"));
  expect(sent.at(-1).type).toBe("pointerup");
  expect(sent.at(-1).point).toEqual({ x: 0.5, y: 0.5 });
  detach();
});
