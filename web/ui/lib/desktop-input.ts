import { desktopOwnsKeyboard } from "./desktop-input-focus";
import { desktopInputShouldDrop } from "./desktop-media";

type Input = Record<string, unknown>;

type SendMove = (beforeCriticalEvent: boolean) => boolean;

/** Each pointer has one pending position. A congested socket keeps only the latest. */
class CanvasMotionQueue {
  private readonly pending = new Map<number, SendMove>();
  private frame: number | null = null;

  enqueue(pointerId: number, send: SendMove) {
    // Map iteration follows the arrival order of the surviving positions.
    this.pending.delete(pointerId);
    this.pending.set(pointerId, send);
    this.schedule();
  }

  private schedule() {
    if (this.frame !== null) return;
    const frame = requestAnimationFrame(() => {
      if (this.frame !== frame) return;
      this.frame = null;
      for (const [id, send] of this.pending) {
        if (send(false)) this.pending.delete(id);
      }
      if (this.pending.size) this.schedule();
    });
    this.frame = frame;
  }

  flush() {
    for (const send of this.pending.values()) send(true);
    this.cancel();
  }

  cancel(pointerId?: number) {
    if (pointerId === undefined) this.pending.clear();
    else this.pending.delete(pointerId);
    if (!this.pending.size && this.frame !== null) {
      cancelAnimationFrame(this.frame);
      this.frame = null;
    }
  }
}

class CanvasWheelAccumulator {
  private x = 0;
  private y = 0;

  consume(event: WheelEvent, canvas: HTMLCanvasElement) {
    // Read deltaMode first: browsers may expose compatibility units if it is never read.
    const mode = event.deltaMode;
    const line = mode === 1 ? parseFloat(getComputedStyle(canvas).lineHeight) || 16 : 1;
    this.x += event.deltaX * (mode === 2 ? canvas.clientWidth : line);
    this.y += event.deltaY * (mode === 2 ? canvas.clientHeight : line);
    const deltaX = Math.trunc(this.x);
    const deltaY = Math.trunc(this.y);
    this.x -= deltaX;
    this.y -= deltaY;
    return { deltaX, deltaY };
  }

  clear() {
    this.x = this.y = 0;
  }
}

type DesktopCanvasInputOptions = {
  socket: () => WebSocket | null;
  mode: () => "control" | "cursor" | null;
  error?: (error: unknown) => void;
  /** Capture keyboard events at window level while control is active, so the
   * remote session keeps typing after focus moves off the canvas. Editable
   * fields, dialogs and the settings panel stay local. */
  globalKeyboard?: boolean;
  /** Lock the pointer on click and drive the remote cursor from relative
   * movement. Esc always releases per browser policy. */
  pointerLock?: () => boolean;
  /** Track the pointer anywhere on the page instead of only over the canvas. */
  globalMouse?: () => boolean;
  onPointerLockChange?: (locked: boolean) => void;
};

const localEditableSelector = [
  'input', 'textarea', 'select', '[contenteditable=""]', '[contenteditable="true"]',
  '[role="textbox"]', 'button', '[role="button"]', '[role="combobox"]', '[role="switch"]',
  '[role="menuitem"]', '[role="tab"]', 'a[href]', 'summary', '[role="dialog"]',
].join(',');

function typingLocally(event: Event): boolean {
  const target = event.target;
  return target instanceof Element && target.closest(localEditableSelector) !== null;
}

// Both Go viewers render the frame into a padding-free canvas with a symmetric border.
function framePoint(canvas: HTMLCanvasElement, event: MouseEvent, captured: boolean) {
  const rect = canvas.getBoundingClientRect();
  const width = rect.width - 2 * canvas.clientLeft;
  const height = rect.height - 2 * canvas.clientTop;
  const x = event.clientX - rect.left - canvas.clientLeft;
  const y = event.clientY - rect.top - canvas.clientTop;
  if (width <= 0 || height <= 0 || !canvas.width || !canvas.height) return null;
  if (!captured && (x < 0 || x >= width || y < 0 || y >= height)) return null;
  return {
    x: Math.max(0, Math.min(canvas.width - 1, Math.floor(x * canvas.width / width))),
    y: Math.max(0, Math.min(canvas.height - 1, Math.floor(y * canvas.height / height))),
  };
}

function modifiers(event: MouseEvent | KeyboardEvent) {
  return { ctrlKey: event.ctrlKey, altKey: event.altKey, shiftKey: event.shiftKey, metaKey: event.metaKey };
}

export class DesktopCanvasInput {
  private readonly held = new DesktopHeldInput();
  private readonly motion = new CanvasMotionQueue();
  private readonly wheel = new CanvasWheelAccumulator();
  private locked = false;
  private virtualX = 0;
  private virtualY = 0;

  attach(canvas: HTMLCanvasElement, options: DesktopCanvasInputOptions) {
    const mode = () => canvas.isConnected && !canvas.ownerDocument.hidden && !canvas.closest("[inert]") &&
      options.socket()?.readyState === WebSocket.OPEN ? options.mode() : null;
    const send = (input: Input) => {
      const socket = options.socket();
      if (!socket || socket.readyState !== WebSocket.OPEN) return false;
      try {
        socket.send(JSON.stringify({ op: "input", ...input }));
        this.held.sent(input);
        return true;
      } catch (error) {
        options.error?.(error);
        return false;
      }
    };
    const lockedPoint = () => ({
      x: Math.max(0, Math.min(canvas.width - 1, Math.round(this.virtualX))),
      y: Math.max(0, Math.min(canvas.height - 1, Math.round(this.virtualY))),
    });
    const pointer = (event: PointerEvent) => {
      const access = mode();
      if (!access) return;
      const id = event.pointerId;
      const dragging = this.held.hasPointer(id);
      let point: { x: number; y: number } | null;
      if (this.locked) {
        if (event.type === "pointermove") {
          const rect = canvas.getBoundingClientRect();
          const scaleX = rect.width > 0 ? canvas.width / rect.width : 1;
          const scaleY = rect.height > 0 ? canvas.height / rect.height : 1;
          this.virtualX = Math.max(0, Math.min(canvas.width - 1, this.virtualX + (event.movementX || 0) * scaleX));
          this.virtualY = Math.max(0, Math.min(canvas.height - 1, this.virtualY + (event.movementY || 0) * scaleY));
        }
        point = lockedPoint();
      } else {
        point = framePoint(canvas, event, dragging || (options.globalMouse?.() === true && access === "control"));
      }
      if (!point) return;
      const data = { ...point, pointerType: event.pointerType, pointerId: id, pressure: event.pressure,
        button: event.button, ...modifiers(event) };
      let type = event.type;
      // Pointer Events report additional button presses/releases as pointermove, not down/up.
      if (type === "pointermove" && dragging && event.button >= 0) {
        const mask = [1, 4, 2, 8, 16, 32][event.button] || 0;
        const down = (event.buttons & mask) !== 0;
        if (mask && down !== this.held.hasButton(id, event.button)) type = down ? "pointerdown" : "pointerup";
      }
      if (type === "pointermove") {
        const socket = options.socket()!;
        const input = { inputType: access === "control" ? "mouse_move" : "cursor_move", ...data };
        this.motion.enqueue(id, (barrier) => {
          if (socket !== options.socket() || access !== mode()) return true;
          if (!barrier && socket.bufferedAmount > 128 * 1024) return false;
          send(input);
          return true;
        });
        return;
      }
      if (access !== "control") return;
      if (type === "pointerup" && !this.held.hasButton(id, event.button)) return;
      this.motion.flush();
      if (!send({ inputType: type === "pointerdown" ? "mouse_down" : "mouse_up", ...data })) return;
      event.preventDefault();
      if (event.type === "pointerdown") {
        canvas.focus({ preventScroll: true });
        if (!this.locked && options.pointerLock?.() && typeof canvas.requestPointerLock === "function") {
          try {
            const locked = canvas.requestPointerLock();
            if (locked && typeof (locked as Promise<void>).catch === "function") (locked as Promise<void>).catch(() => {});
          } catch {
            /* pointer lock is optional */
          }
        } else {
          canvas.setPointerCapture?.(id);
        }
      }
      if (event.type === "pointerup" && canvas.hasPointerCapture?.(id)) canvas.releasePointerCapture(id);
    };
    const cancelPointer = (event: PointerEvent) => {
      this.motion.cancel(event.pointerId);
      this.held.releasePointer(event.pointerId, send);
    };
    const key = (event: KeyboardEvent) => {
      if (mode() !== "control" || !desktopOwnsKeyboard(canvas)) return;
      if (event.type === "keyup" && !this.held.hasKey(event)) return;
      this.motion.flush();
      if (send({ inputType: event.type === "keydown" ? "key_down" : "key_up", key: event.key, code: event.code, ...modifiers(event) })) {
        event.preventDefault();
        event.stopPropagation();
      }
    };
    const globalKey = (event: KeyboardEvent) => {
      if (!options.globalKeyboard || mode() !== "control" || typingLocally(event)) return;
      if (event.type === "keyup" && !this.held.hasKey(event)) return;
      this.motion.flush();
      if (send({ inputType: event.type === "keydown" ? "key_down" : "key_up", key: event.key, code: event.code, ...modifiers(event) })) {
        event.preventDefault();
      }
    };
    const wheel = (event: WheelEvent) => {
      if (mode() !== "control") return;
      const point = this.locked ? lockedPoint() : framePoint(canvas, event, false);
      if (!point) return;
      event.preventDefault();
      event.stopPropagation();
      if (desktopInputShouldDrop("wheel", options.socket()!.bufferedAmount)) return;
      const delta = this.wheel.consume(event, canvas);
      if (!delta.deltaX && !delta.deltaY) return;
      this.motion.flush();
      send({ inputType: "wheel", ...point, ...delta, ...modifiers(event) });
    };
    const suppressLocalClick = (event: MouseEvent) => {
      if (mode() === "control" && framePoint(canvas, event, false)) event.preventDefault();
    };
    const listeners = new AbortController();
    const opts = { signal: listeners.signal };
    canvas.addEventListener("contextmenu", suppressLocalClick, opts);
    canvas.addEventListener("auxclick", suppressLocalClick, opts);
    canvas.addEventListener("pointermove", pointer, opts);
    canvas.addEventListener("pointerdown", pointer, opts);
    canvas.addEventListener("pointerup", pointer, opts);
    canvas.addEventListener("pointercancel", cancelPointer, opts);
    canvas.addEventListener("lostpointercapture", cancelPointer, opts);
    canvas.addEventListener("wheel", wheel, { ...opts, passive: false });
    canvas.addEventListener("keydown", key, opts);
    canvas.addEventListener("keyup", key, opts);
    canvas.addEventListener("blur", () => this.release(send), opts);
    const onLockChange = () => {
      const locked = canvas.ownerDocument.pointerLockElement === canvas;
      if (locked === this.locked) return;
      this.locked = locked;
      if (locked) {
        const center = framePoint(canvas, { clientX: canvas.getBoundingClientRect().left + canvas.getBoundingClientRect().width / 2, clientY: canvas.getBoundingClientRect().top + canvas.getBoundingClientRect().height / 2 } as MouseEvent, true);
        this.virtualX = center?.x ?? canvas.width / 2;
        this.virtualY = center?.y ?? canvas.height / 2;
      } else {
        this.release(send);
      }
      options.onPointerLockChange?.(locked);
    };
    canvas.ownerDocument.addEventListener("pointerlockchange", onLockChange, opts);
    canvas.ownerDocument.addEventListener("pointerlockerror", () => options.onPointerLockChange?.(false), opts);
    if (options.globalMouse) {
      canvas.ownerDocument.addEventListener("pointermove", (event: PointerEvent) => {
        if (this.locked || mode() !== "control" || options.globalMouse?.() !== true) return;
        const point = framePoint(canvas, event, true);
        if (!point) return;
        const id = event.pointerId;
        const input = { inputType: "mouse_move", ...point, pointerType: event.pointerType || "mouse", pointerId: id, pressure: event.pressure, button: -1, ...modifiers(event) };
        this.motion.enqueue(id, () => send(input));
      }, opts);
    }
    if (options.globalKeyboard) {
      const doc = canvas.ownerDocument;
      doc.addEventListener("keydown", globalKey, opts);
      doc.addEventListener("keyup", globalKey, opts);
      doc.defaultView?.addEventListener("blur", () => this.release(send), opts);
      doc.addEventListener("visibilitychange", () => {
        if (doc.hidden) this.release(send);
      }, opts);
    }
    return () => {
      listeners.abort();
      this.release(send);
    };
  }

  release(send: (input: Input) => void) {
    this.motion.cancel();
    this.wheel.clear();
    this.held.release(send);
  }

  clear() {
    this.motion.cancel();
    this.wheel.clear();
    this.held.clear();
  }
}

/** Track only events sent to this viewer's current desktop session. */
export class DesktopHeldInput {
  private keys = new Map<unknown, Input>();
  private buttons = new Map<unknown, Map<unknown, Input>>();

  hasKey(event: KeyboardEvent) {
    return this.keys.has(event.code || event.key);
  }

  hasPointer(id: number) {
    return this.buttons.has(id);
  }

  hasButton(id: number, button: number) {
    return this.buttons.get(id)?.has(button) ?? false;
  }

  sent(input: Input) {
    const id = input.code || input.key;
    // Every delivered pointer transition updates the position and modifiers of its other held buttons.
    if (input.inputType === "mouse_move" || input.inputType === "mouse_down" || input.inputType === "mouse_up") {
      const held = this.buttons.get(input.pointerId);
      if (held) for (const button of held.values()) {
        button.x = input.x;
        button.y = input.y;
        if (input.ctrlKey !== undefined) {
          button.ctrlKey = input.ctrlKey;
          button.altKey = input.altKey;
          button.shiftKey = input.shiftKey;
          button.metaKey = input.metaKey;
        }
      }
    }
    switch (input.inputType) {
      case "key_down":
        this.keys.set(id, input);
        break;
      case "key_up":
        this.keys.delete(id);
        break;
      case "mouse_down": {
        const held = this.buttons.get(input.pointerId) ?? new Map<unknown, Input>();
        held.set(input.button, input);
        this.buttons.set(input.pointerId, held);
        break;
      }
      case "mouse_up": {
        const held = this.buttons.get(input.pointerId);
        held?.delete(input.button);
        if (!held?.size) this.buttons.delete(input.pointerId);
        break;
      }
    }
  }

  releasePointer(id: unknown, send: (input: Input) => void) {
    const held = this.buttons.get(id);
    if (!held) return;
    for (const [button, input] of held) {
      send({ ...input, inputType: "mouse_up", pressure: 0 });
      held.delete(button);
    }
    this.buttons.delete(id);
  }

  release(send: (input: Input) => void) {
    for (const id of this.buttons.keys()) this.releasePointer(id, send);
    for (const [id, input] of this.keys) {
      send({ ...input, inputType: "key_up", ctrlKey: false, altKey: false, shiftKey: false, metaKey: false });
      this.keys.delete(id);
    }
  }

  clear() {
    this.keys.clear();
    this.buttons.clear();
  }
}
