const desktopInputBackpressureLimit = 512 * 1024;

export function desktopInputShouldDrop(inputType: unknown, bufferedAmount: number): boolean {
  if (bufferedAmount <= desktopInputBackpressureLimit) return false;
  return inputType === "mouse_move" || inputType === "wheel";
}

export function desktopEncoderRecoveryFormat(format?: string, backend?: string): "h264" | "mjpeg" | null {
  if (format !== "h264") return null;
  return backend && backend !== "cpu" ? "h264" : "mjpeg";
}

// Only call with the opened encoder reported for this stream. A selectable
// backend, a capture API, and a browser decoder preference aren't evidence.
export function desktopEncoderAcceleration(backend?: string, reported?: unknown): "hardware" | "software" | "unknown" | null {
  if (!backend) return null;
  if (reported === "hardware" || reported === "software") return reported;
  const normalized = backend.toLowerCase().replace(/[-_ ]/g, "");
  if (["nvenc", "vaapi", "h264nvenc", "h264vaapi", "h264vaapi/drmprime"].includes(normalized))
    return "hardware";
  if (["cpu", "processor", "jpeg", "mjpeg", "libx264"].includes(normalized)) return "software";
  return "unknown";
}

type EncoderReport = { encoderBackend?: unknown; encoderAcceleration?: unknown };
type EncoderLabel = "desktop.hardwareAcceleration" | "desktop.softwareEncoding" | "desktop.encoderUnverified";

export function desktopEncoderStatus(report: EncoderReport, t: (key: EncoderLabel) => string): string {
  const backend = typeof report.encoderBackend === "string" ? report.encoderBackend.trim() : "";
  if (!backend) return "";
  const kind = desktopEncoderAcceleration(backend, report.encoderAcceleration);
  const key = kind === "hardware" ? "desktop.hardwareAcceleration" : kind === "software" ? "desktop.softwareEncoding" : "desktop.encoderUnverified";
  return `${t(key)}: ${backend}`;
}

// Older Rust clients put lifecycle messages in encoderBackend; Android's old
// Config acknowledgement advertised MediaCodec even with video stopped.
// Input-only RuntimeStatus patches carry no encoder update at all.
export function desktopRuntimeEncoderStatus(report: EncoderReport, t: (key: EncoderLabel) => string): string | undefined {
  if (!("encoderBackend" in report)) return undefined;
  if (report.encoderBackend === "mediacodec-h264") return undefined;
  if (["未启动", "等待视频流", "重启中", "初始化失败", "zero-copy降级"].includes(String(report.encoderBackend))) return "";
  return desktopEncoderStatus(report, t);
}

export function desktopStreamRates(fps: string | null, quality: string | null, auto = false) {
  const integer = (value: string | null, min: number, max: number, fallback: number) => {
    const n = Number(value);
    return value?.trim() && Number.isInteger(n) ? Math.max(min, Math.min(max, n)) : fallback;
  };
  return {
    fps: auto ? 30 : integer(fps, 1, 30, 30),
    quality: auto ? 75 : integer(quality, 25, 90, 75),
  };
}

export const desktopFPSPresets = ["4", "8", "12", "20", "30"];
export const desktopQualityPresets = ["25", "50", "75", "90"];

export function desktopPanelPresets(fps: string | null, quality: string | null) {
  return {
    fps: fps && desktopFPSPresets.includes(fps) ? fps : "30",
    quality: quality && desktopQualityPresets.includes(quality) ? quality : "75",
  };
}

// Own the listeners as one stream lifetime. Browser media events may arrive
// after a socket/decoder has been replaced; never let them bind the new stream.
export function attachDesktopMediaSource(source: MediaSource, {
  current, supported, ready, update, error,
}: {
  current: () => boolean;
  supported: (mime: string) => boolean;
  ready: (buffer: SourceBuffer) => void;
  update: () => void;
  error: (cause: unknown) => void;
}) {
  let disposed = false;
  let buffer: SourceBuffer | null = null;
  const ownsStream = () => !disposed && current();
  const onUpdate = () => { if (ownsStream()) update(); };
  const onOpen = () => {
    if (!ownsStream() || source.readyState !== "open") return;
    try {
      const preferred = 'video/mp4; codecs="avc1.4D403D"';
      buffer = source.addSourceBuffer(supported(preferred) ? preferred : "video/mp4");
      try { buffer.mode = "sequence"; } catch { /* optional browser mode */ }
      buffer.addEventListener("updateend", onUpdate);
      ready(buffer);
      // Packets can arrive before sourceopen; do not require a later packet
      // to kick off the first append.
      onUpdate();
    } catch (cause) { if (ownsStream()) error(cause); }
  };
  source.addEventListener("sourceopen", onOpen, { once: true });
  return () => {
    disposed = true;
    source.removeEventListener("sourceopen", onOpen);
    buffer?.removeEventListener("updateend", onUpdate);
    buffer = null;
  };
}
