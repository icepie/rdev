import { acceptDesktopH264Frame, paintDesktopCanvas } from "./desktop-frame";

/** Video decoding shared by the standalone desktop page and the agent desktop
 * panel: the Android WebCodecs path and its bitstream framing. */

export type DesktopFrameCounters = {
  count: { current: number };
  bytes: { current: number };
  lastAt: { current: number };
};

/** Android clients multiplex raw H.264 on the same binary socket as fMP4
 * segments, framed as "RD A1" + keyframe flag + u64 timestamp. Returns false
 * for every other binary packet, true once the packet was consumed. */
export function androidVideoPacket(
  buffer: ArrayBuffer,
  decoder: VideoDecoder | null,
  needKeyframe: { current: boolean },
  frames: DesktopFrameCounters,
  fail: (cause: unknown) => void,
): boolean {
  const view = new DataView(buffer);
  if (
    view.byteLength < 13 ||
    view.getUint8(0) !== 0x52 ||
    view.getUint8(1) !== 0x44 ||
    view.getUint8(2) !== 0x41 ||
    view.getUint8(3) !== 0x31
  )
    return false;
  if (!decoder || decoder.state !== "configured") return true;
  const isKey = view.getUint8(4) !== 0;
  if (!acceptDesktopH264Frame(isKey, decoder.decodeQueueSize, needKeyframe)) return true;
  const timestamp = Number(view.getBigUint64(5));
  const data = new Uint8Array(buffer, 13);
  frames.count.current++;
  frames.bytes.current += data.byteLength;
  frames.lastAt.current = performance.now();
  try {
    decoder.decode(new EncodedVideoChunk({ type: isKey ? "key" : "delta", timestamp, data }));
  } catch (err) {
    needKeyframe.current = true;
    fail(err);
  }
  return true;
}

/** Start the WebCodecs video path and paint decoded frames onto the GPU canvas.
 * Returns false when the browser has no WebCodecs support, leaving the caller
 * to report it; nothing is torn down or reset in that case. */
export function startWebCodecsVideo(
  config: { codec?: string; width?: number; height?: number },
  {
    canvas,
    decoder,
    mode,
    needKeyframe,
    frames,
    startedAt,
    reset,
    showCanvas,
    onPainted,
    onFrame,
    fail,
    onReady,
  }: {
    canvas: HTMLCanvasElement | null;
    decoder: { current: VideoDecoder | null };
    mode: { current: "mse" | "webcodecs" };
    needKeyframe: { current: boolean };
    frames: DesktopFrameCounters;
    startedAt: { current: number };
    /** Releases the previous MSE/WebCodecs pipeline. */
    reset: () => void;
    showCanvas: () => void;
    onPainted: () => void;
    onFrame: () => void;
    fail: (cause: unknown) => void;
    /** Reports the ready status once the decoder accepts frames. */
    onReady: () => void;
  },
): boolean {
  if (!("VideoDecoder" in window) || !("EncodedVideoChunk" in window)) return false;
  reset();
  mode.current = "webcodecs";
  frames.count.current = 0;
  frames.bytes.current = 0;
  startedAt.current = performance.now();
  frames.lastAt.current = 0;
  showCanvas();
  const context = canvas?.getContext("2d");
  if (canvas) {
    canvas.width = config.width || 320;
    canvas.height = config.height || 640;
  }
  const videoDecoder = new VideoDecoder({
    output: (frame) => {
      try {
        if (decoder.current !== videoDecoder) return;
        if (canvas && context) {
          paintDesktopCanvas(
            context,
            frame,
            frame.displayWidth || frame.codedWidth || canvas.width,
            frame.displayHeight || frame.codedHeight || canvas.height,
          );
          onPainted();
        }
        onFrame();
      } finally {
        frame.close();
      }
    },
    error: (err) => {
      if (decoder.current === videoDecoder) fail(err);
    },
  });
  videoDecoder.configure({
    codec: config.codec || "avc1.42E01F",
    codedWidth: config.width || 320,
    codedHeight: config.height || 640,
    optimizeForLatency: true,
    hardwareAcceleration: "prefer-hardware",
  });
  decoder.current = videoDecoder;
  needKeyframe.current = true;
  onReady();
  return true;
}
