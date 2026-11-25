import { desktopOwnsKeyboard } from "./desktop-input-focus";
import type { DesktopCanvasInput } from "./desktop-input";

/* GPU desktop input wiring shared by the standalone desktop page and the agent
 * desktop panel. Both attach the same native listeners to a stage whose
 * elements unmount with the connection mode, so the callers re-run the wiring
 * on every re-attach while the listeners themselves stay identical. */

/** The GPU input surface: the overlay covering the video/canvas. */
type DesktopGpuInputApi = {
  gpuInputAllowed: () => boolean;
  gpuPointerTypes: () => string[];
  gpuSend: (message: unknown) => void;
  gpuPointerMessage: (event: PointerEvent, eventType: string) => unknown;
  gpuKeyMessage: (
    event: KeyboardEvent,
    eventType: string,
  ) => { KeyboardEvent: Record<string, unknown> };
  gpuTextInput: (text: string | null) => void;
  gpuReleaseKeyboard: () => void;
};

/** Window-level capture needs the same calls plus the session lifecycle. */
type DesktopCaptureApi = DesktopGpuInputApi & {
  desktopReleaseKeyboard: () => void;
  pollClipboardSync: () => void;
};

type TextComposition = {
  composing: { current: boolean };
  ignoreNextTextInput: { current: boolean };
};

/** True while the target is a real form field: typing belongs to the page. */
function isFormField(target: EventTarget | null) {
  return (
    target instanceof HTMLInputElement ||
    target instanceof HTMLTextAreaElement ||
    target instanceof HTMLSelectElement
  );
}

/** Attach the legacy canvas input plus all GPU overlay listeners. The caller
 * owns `mode` because a folded panel also gates input on visibility. */
export function attachDesktopStageInput({
  canvas,
  overlay,
  input,
  api,
  composing,
  ignoreNextTextInput,
  heldKeys,
  socket,
  mode,
  mouseEnabled,
  socketOpen,
  fail,
}: TextComposition & {
  canvas: HTMLCanvasElement | null;
  overlay: HTMLElement | null;
  input: DesktopCanvasInput;
  api: () => DesktopGpuInputApi;
  heldKeys: { current: Map<string, Record<string, unknown>> };
  socket: () => WebSocket | null;
  mode: () => "control" | "cursor" | null;
  mouseEnabled: () => boolean;
  socketOpen: () => boolean;
  fail: (cause: unknown) => void;
}): () => void {
  const cleanups: (() => void)[] = [];
  const on = <E extends Event>(
    el: HTMLElement,
    type: string,
    fn: (e: E) => void,
    opts?: AddEventListenerOptions,
  ) => {
    // The listener is registered for one specific event type, so the
    // caller's narrower event type is sound at runtime.
    const listener = fn as EventListener;
    el.addEventListener(type, listener, opts);
    cleanups.push(() => el.removeEventListener(type, listener, opts));
  };

  if (canvas) {
    cleanups.push(
      input.attach(canvas, { socket, mode, error: fail, globalKeyboard: true }),
    );
  }

  if (overlay) {
    on(overlay, "contextmenu", (e: Event) => e.preventDefault());
    on(overlay, "auxclick", (e: Event) => e.preventDefault());
    for (const type of [
      "pointerdown",
      "pointermove",
      "pointerup",
      "pointercancel",
      "pointerover",
      "pointerenter",
      "pointerleave",
      "pointerout",
    ]) {
      on(overlay, type, (e: PointerEvent) => {
        const current = api();
        if (!current.gpuInputAllowed()) return;
        if (!current.gpuPointerTypes().includes(e.pointerType || "mouse")) return;
        if (type === "pointerdown") overlay.focus({ preventScroll: true });
        if (type === "pointerdown" && overlay.setPointerCapture)
          overlay.setPointerCapture(e.pointerId);
        const events =
          type === "pointermove" && typeof e.getCoalescedEvents === "function"
            ? e.getCoalescedEvents()
            : [e];
        for (const ev of events) current.gpuSend(current.gpuPointerMessage(ev, type));
        e.preventDefault();
        e.stopPropagation();
      });
    }
    on(
      overlay,
      "wheel",
      (e: WheelEvent) => {
        const current = api();
        if (!current.gpuInputAllowed() || !mouseEnabled()) return;
        let scale = 1;
        if (e.deltaMode === 1) scale = 10;
        else if (e.deltaMode === 2) scale = 1000;
        current.gpuSend({
          WheelEvent: {
            dx: Math.round(scale * e.deltaX),
            dy: -Math.round(scale * e.deltaY),
            timestamp: Math.round(e.timeStamp * 1000),
          },
        });
        e.preventDefault();
        e.stopPropagation();
      },
      { passive: false },
    );
    on(overlay, "blur", () => {
      api().gpuReleaseKeyboard();
      composing.current = false;
      ignoreNextTextInput.current = false;
    });
    on(overlay, "keydown", (e: KeyboardEvent) => {
      const current = api();
      if (!current.gpuInputAllowed() || !socketOpen()) return;
      if (composing.current || e.isComposing || e.key === "Process" || e.keyCode === 229) {
        e.preventDefault();
        e.stopPropagation();
        return;
      }
      const identity = `${e.code || e.key}:${e.location}`;
      const eventType = e.repeat && heldKeys.current.has(identity) ? "repeat" : "down";
      const msg = current.gpuKeyMessage(e, eventType);
      current.gpuSend(msg);
      if (eventType === "down") heldKeys.current.set(identity, msg.KeyboardEvent);
      e.preventDefault();
      e.stopPropagation();
    });
    on(overlay, "keyup", (e: KeyboardEvent) => {
      const identity = `${e.code || e.key}:${e.location}`;
      if (!heldKeys.current.has(identity)) return;
      const current = api();
      const msg = current.gpuKeyMessage(e, "up");
      current.gpuSend(msg);
      heldKeys.current.delete(identity);
      e.preventDefault();
      e.stopPropagation();
    });
  }

  return () => {
    for (const fn of cleanups) fn();
  };
}

/** Capture keystrokes/text aimed at the remote desktop instead of the page:
 * focus/blur, tab visibility and IME-aware text entry. */
export function attachDesktopCaptureListeners({
  overlay,
  api,
  composing,
  ignoreNextTextInput,
  streaming,
  resumeVideo,
}: TextComposition & {
  overlay: { current: HTMLElement | null };
  api: () => DesktopCaptureApi;
  /** True while a GPU stream owns the connection. */
  streaming: () => boolean;
  /** Whether a returning tab should resume video playback. */
  resumeVideo: () => boolean;
}): () => void {
  const on = <E extends Event>(
    el: Window | Document,
    type: string,
    fn: (e: E) => void,
    opts?: AddEventListenerOptions | boolean,
  ) => {
    // The listener is registered for one specific event type, so the
    // caller's narrower event type is sound at runtime.
    const listener = fn as EventListener;
    el.addEventListener(type, listener, opts);
    return () => el.removeEventListener(type, listener, opts);
  };
  const textTarget = () => {
    const current = api();
    if (!current.gpuInputAllowed() || !desktopOwnsKeyboard(overlay.current)) return null;
    return current;
  };
  const cleanups = [
    on(window, "focus", () => api().pollClipboardSync(), true),
    on(window, "blur", () => {
      const current = api();
      current.desktopReleaseKeyboard();
      current.gpuReleaseKeyboard();
    }),
    on(
      document,
      "visibilitychange",
      () => {
        const current = api();
        if (document.hidden) {
          current.gpuReleaseKeyboard();
          current.desktopReleaseKeyboard();
          if (streaming()) current.gpuSend("PauseVideo");
          return;
        }
        current.pollClipboardSync();
        if (streaming() && resumeVideo()) current.gpuSend("ResumeVideo");
      },
      true,
    ),
    on(
      window,
      "beforeinput",
      (e: InputEvent) => {
        const current = textTarget();
        if (!current || isFormField(e.target)) return;
        if (
          e.inputType === "insertCompositionText" ||
          (e.inputType === "insertText" && ignoreNextTextInput.current)
        ) {
          ignoreNextTextInput.current = false;
          e.preventDefault();
          e.stopPropagation();
          return;
        }
        if (e.inputType !== "insertText" && e.inputType !== "insertLineBreak") return;
        current.gpuTextInput(e.inputType === "insertLineBreak" ? "\n" : e.data);
        e.preventDefault();
        e.stopPropagation();
      },
      true,
    ),
    on(
      window,
      "paste",
      (e: ClipboardEvent) => {
        const current = textTarget();
        if (!current || isFormField(e.target)) return;
        current.gpuTextInput(e.clipboardData?.getData("text") || "");
        e.preventDefault();
        e.stopPropagation();
      },
      true,
    ),
    on(
      window,
      "compositionstart",
      (e: CompositionEvent) => {
        if (!textTarget()) return;
        composing.current = true;
        e.preventDefault();
        e.stopPropagation();
      },
      true,
    ),
    on(
      window,
      "compositionend",
      (e: CompositionEvent) => {
        const current = textTarget();
        if (!current) return;
        composing.current = false;
        ignoreNextTextInput.current = true;
        current.gpuTextInput(e.data || "");
        setTimeout(() => {
          ignoreNextTextInput.current = false;
        }, 0);
        e.preventDefault();
        e.stopPropagation();
      },
      true,
    ),
  ];
  return () => {
    for (const fn of cleanups) fn();
  };
}
