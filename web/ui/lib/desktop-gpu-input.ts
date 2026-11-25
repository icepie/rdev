/* GPU-stream input payloads. Coordinates are normalized against the video
 * content box (letterboxing excluded) because the encoder maps 0..1 itself. */

function gpuPointerBits(button: number): number {
  if (button === 0) return 1;
  if (button === 2) return 2;
  if (button === 1) return 4;
  if (button === 3) return 8;
  if (button === 4) return 16;
  if (button === 5) return 32;
  return 0;
}

/** object-fit: contain / actual leave bars inside the overlay; pointing at the
 * bars must not move the remote cursor, so map onto the painted rectangle. */
export function gpuVideoContentRect({
  rect,
  videoWidth,
  videoHeight,
  stretch,
  fit,
}: {
  rect: DOMRect;
  videoWidth: number;
  videoHeight: number;
  stretch: boolean;
  fit: string;
}): DOMRect {
  if (!videoWidth || !videoHeight || !rect.width || !rect.height || stretch) return rect;
  if (fit === "actual") {
    return new DOMRect(
      rect.left + (rect.width - videoWidth) / 2,
      rect.top + (rect.height - videoHeight) / 2,
      videoWidth,
      videoHeight,
    );
  }
  const containerRatio = rect.width / rect.height;
  const videoRatio = videoWidth / videoHeight;
  if (containerRatio > videoRatio) {
    const width = rect.height * videoRatio;
    return new DOMRect(rect.left + (rect.width - width) / 2, rect.top, width, rect.height);
  }
  const height = rect.width / videoRatio;
  return new DOMRect(rect.left, rect.top + (rect.height - height) / 2, rect.width, height);
}

export function gpuPointerMessage(
  event: PointerEvent,
  {
    rect,
    previous,
    minPressure,
    eventType,
    point,
  }: {
    rect: DOMRect;
    previous: { x: number; y: number };
    minPressure: string;
    eventType: string;
    /** Already-normalized cursor position, used while the pointer is locked;
     * client coordinates freeze in that mode. */
    point?: { x: number; y: number };
  },
) {
  const x = Math.max(0, Math.min(1, point?.x ?? (event.clientX - rect.left) / Math.max(1, rect.width)));
  const y = Math.max(0, Math.min(1, point?.y ?? (event.clientY - rect.top) / Math.max(1, rect.height)));
  const diag = Math.max(1, Math.sqrt(rect.width * rect.width + rect.height * rect.height));
  const pressure = Math.max(
    event.pressure || (event.buttons ? 0.5 : 0),
    parseFloat(minPressure) || 0,
  );
  return {
    PointerEvent: {
      event_type: eventType,
      pointer_id: event.pointerId,
      timestamp: Math.round(event.timeStamp * 1000),
      is_primary: event.isPrimary,
      pointer_type: event.pointerType || "mouse",
      button: gpuPointerBits(event.button),
      buttons: event.buttons,
      x,
      y,
      movement_x: Math.round(event.movementX || event.clientX - previous.x),
      movement_y: Math.round(event.movementY || event.clientY - previous.y),
      pressure,
      tilt_x: event.tiltX || 0,
      tilt_y: event.tiltY || 0,
      twist: event.twist || 0,
      width: (event.width || 1) / diag,
      height: (event.height || 1) / diag,
    },
  };
}

export function gpuKeyMessage(event: KeyboardEvent, eventType: string) {
  return {
    KeyboardEvent: {
      event_type: eventType,
      code: event.code,
      key: event.key,
      location: event.location,
      alt: event.altKey,
      ctrl: event.ctrlKey || false,
      shift: event.shiftKey,
      meta: event.metaKey,
    },
  };
}

export function gpuPointerTypes({
  gpuEnableMouse,
  gpuEnablePen,
  gpuEnableTouch,
}: {
  gpuEnableMouse: boolean;
  gpuEnablePen: boolean;
  gpuEnableTouch: boolean;
}): string[] {
  const types: string[] = [];
  if (gpuEnableMouse) types.push("mouse");
  if (gpuEnablePen) types.push("pen");
  if (gpuEnableTouch) types.push("touch");
  return types;
}
