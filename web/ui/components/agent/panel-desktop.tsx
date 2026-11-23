import { attachDesktopMediaSource, desktopEncoderRecoveryFormat, desktopEncoderStatus, desktopFPSPresets, desktopPanelPresets, desktopQualityPresets, desktopRuntimeEncoderStatus } from "../../lib/desktop-media";
import { DesktopCanvasInput } from "../../lib/desktop-input";
import { useDesktopClipboard } from "../../lib/desktop-clipboard";
import { paintDesktopCanvas, paintDesktopImage } from "../../lib/desktop-frame";
import { androidVideoPacket, startWebCodecsVideo, type DesktopFrameCounters } from "../../lib/desktop-gpu-video";
import { attachDesktopCaptureListeners, attachDesktopStageInput } from "../../lib/desktop-stage-input";
import { saveDesktopScreenshot } from "../../lib/desktop-shot";
import { DesktopH264Decoder } from "../../lib/desktop-h264";
import {
  adaptiveDesktopSize,
  desktopFrameLine,
  desktopFrameRates,
  errMessage,
  gpuDesktopFrameRate,
  gpuDesktopMaxVideoSize,
  inputBackendLabel,
  loadDesktopSettings,
  persistGpuDesktopSettings,
  sourceLabel,
  type ConnMode,
  type DesktopCaps,
  type DesktopDevice,
  type DesktopSettingsBase,
  type DesktopSource,
  type InputOption,
  type SelectOption,
  type StatusCls,
} from "../../lib/desktop-model";
import {
  gpuKeyMessage,
  gpuPointerMessage as buildGpuPointerMessage,
  gpuPointerTypes as readGpuPointerTypes,
  gpuVideoContentRect as computeGpuVideoContentRect,
} from "../../lib/desktop-gpu-input";
/* PanelDesktop: one device remote-desktop session inside an agent right-panel
 * tab. Self-contained WS client derived from the Desktop page, preserving both
 * stream modes: the legacy JPEG/H264 canvas stream over /desktop and the GPU
 * stream (MSE or WebCodecs fMP4) over /gpu-desktop/<id>/ws, including input
 * control, clipboard ops and screenshots.
 *
 * Differences from the page:
 * - The device comes from a prop (no device picker / URL mirroring).
 * - Folding keeps the transport and display state. GPU video pauses while
 *   hidden; input, clipboard access and adaptive resizing are visibility-gated.
 * - Settings persist under rdevDesktop.panel.* / rdevGpuDesktop.panel.* so
 *   panel and page never clobber each other; clipboard sync defaults to off.
 * - Device passwords are collected through TabAuthOverlay (legacy sends an
 *   auth op, GPU carries ?password= in the socket URL). */
import { useEffect, useRef, useState } from "react";
import {
  apiJSON,
  wsURL,
  getDevicePassword,
  rememberDevicePassword,
  forgetDevicePassword,
  deviceAuthBypassed,
} from "../../lib/api";
import { cn } from "../../lib/utils";
import { Button } from "../button";
import { Switch } from "../switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../select";
import { Icon } from "../icon";
import { TabAuthOverlay } from "../../pages/Terminal";
import type { Translator } from "./types";

const P = "rdevDesktop.panel.";
const GP = "rdevGpuDesktop.panel.";

type Settings = DesktopSettingsBase;

function loadSettings(): Settings {
  return {
    ...loadDesktopSettings(P, GP),
    ...desktopPanelPresets(localStorage.getItem(P + "fps"), localStorage.getItem(P + "quality")),
  };
}

/* ── Panel component ── */

export function PanelDesktop({
  initialSource,
  device,
  visible,
  t,
}: {
  initialSource?: string;
  device: string;
  /* false while another panel tab is active: the socket is closed and all
   * window-level capture is detached; the stage DOM stays mounted so tab
   * switches reconnect with the same settings. */
  visible: boolean;
  t: Translator;
}) {
  const [settingsState, setSettingsState] = useState<Settings>(() => ({ ...loadSettings(), ...(initialSource?.startsWith("gpu:") ? { gpuSource: initialSource.slice(4) } : initialSource ? { source: initialSource } : {}) }));
  const [status, setStatusState] = useState<{ text: string; cls: StatusCls }>({
    text: "",
    cls: "",
  });
  const [frameInfo, setFrameInfo] = useState("");
  const [clipStatus, setClipStatus] = useState("");
  const [isGpuDevice, setIsGpuDevice] = useState(false);
  const [sourceOptions, setSourceOptions] = useState<DesktopSource[]>([]);
  const [inputOptions, setInputOptions] = useState<SelectOption[]>([
    { value: "auto", label: "" },
  ]);
  const [inputDisabled, setInputDisabled] = useState(true);
  const [gpuEncoderOptions, setGpuEncoderOptions] = useState<SelectOption[]>([
    { value: "auto", label: "" },
  ]);
  const [connMode, setConnModeState] = useState<ConnMode>("");
  const [gpuEncoderStatus, setGpuEncoderStatus] = useState("");
  const [canvasShown, setCanvasShown] = useState(false);
  const [gpuDisplay, setGpuDisplay] = useState<"none" | "video" | "canvas">("none");
  const [gpuEmpty, setGpuEmpty] = useState<{ title: string; hint: string } | null>(null);
  const [controlActive, setControlActive] = useState(() => loadSettings().control);
  const [cursorHidden, setCursorHidden] = useState(() => loadSettings().showCursor);
  const [controlDisabled, setControlDisabled] = useState(false);
  const [authOpen, setAuthOpen] = useState(false);
  const [authError, setAuthError] = useState("");
  const [authRound, setAuthRound] = useState(0);

  /* ── Refs (mutable connection state, mirrors of state the logic reads) ── */

  const settingsRef = useRef(settingsState);
  const deviceRef = useRef(device);
  deviceRef.current = device;
  const visibleRef = useRef(visible);
  visibleRef.current = visible;
  const passwordRef = useRef("");
  const deviceCapsRef = useRef<DesktopDevice | null>(null);
  const gpuSourceNamesRef = useRef<string[] | null>(null);
  const tRef = useRef(t);
  tRef.current = t;

  const stageRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const viewerRef = useRef<HTMLDivElement>(null);
  const videoRef = useRef<HTMLVideoElement>(null);
  const gpuCanvasRef = useRef<HTMLCanvasElement>(null);
  const overlayRef = useRef<HTMLDivElement>(null);

  const wsRef = useRef<WebSocket | null>(null);
  const connSeqRef = useRef(0);
  const connModeRef = useRef<ConnMode>("");
  const manualDisconnectRef = useRef(false);
  const remoteInputRef = useRef(false);
  const lastCloseMsgRef = useRef("");
  const forcedFormatRef = useRef<string | null>(null);
  const controlPreferenceSet = useRef(localStorage.getItem(P + "control") !== null);
  const pendingPwRef = useRef("");
  const gpuAuthPendingRef = useRef(false);
  const gpuBypassRef = useRef(false);
  const gpuHadMsgRef = useRef(false);

  const drawingRef = useRef(false);
  const pendingFrameRef = useRef<{ data: ArrayBuffer; connection: number; socket: WebSocket | null } | null>(null);
  const reconnectTimerRef = useRef<number | undefined>(undefined);
  const resizeTimerRef = useRef<number | undefined>(undefined);
  const lastAdaptiveSizeRef = useRef<{ width: number; height: number } | null>(null);
  const desktopReconnectDelayRef = useRef(1000);

  const frameCountRef = useRef(0);
  const frameBytesRef = useRef(0);
  const statsStartedAtRef = useRef(0);
  const lastFrameAtRef = useRef(0);
  const currentSourceRef = useRef("");

  const h264Ref = useRef<DesktopH264Decoder | null>(null);
  const h264 = () => (h264Ref.current ??= new DesktopH264Decoder(paintImage, fallbackToMjpeg));
  const [desktopInput] = useState(() => new DesktopCanvasInput());

  const gpuMediaSourceRef = useRef<MediaSource | null>(null);
  const gpuMediaCleanupRef = useRef<(() => void) | null>(null);
  const gpuSourceBufferRef = useRef<SourceBuffer | null>(null);
  const gpuQueueRef = useRef<ArrayBuffer[]>([]);
  const gpuLastPointerRef = useRef(new Map<number, { x: number; y: number }>());
  const gpuHeldKeysRef = useRef(new Map<string, Record<string, unknown>>());
  const gpuVideoDecoderRef = useRef<VideoDecoder | null>(null);
  const gpuVideoModeRef = useRef<"mse" | "webcodecs">("mse");
  const gpuNeedKeyFrameRef = useRef(false);
  const gpuLastStatsAtRef = useRef(0);
  const gpuComposingRef = useRef(false);
  const gpuIgnoreNextTextInputRef = useRef(false);
  /* Grouped for the shared GPU video helpers (androidVideoPacket/WebCodecs). */
  const gpuFrameCounters: DesktopFrameCounters = {
    count: frameCountRef,
    bytes: frameBytesRef,
    lastAt: lastFrameAtRef,
  };

  /* ── State helpers (keep refs in sync, synchronously) ── */

  const updateSettings = (patch: Partial<Settings>) => {
    settingsRef.current = { ...settingsRef.current, ...patch };
    setSettingsState(settingsRef.current);
  };
  const settings = settingsState;

  const setStatus = (text: string, cls: StatusCls = "") => setStatusState({ text, cls });
  const setGpuVideoError = (cause: unknown) => setStatus(errMessage(cause), "err");

  const setConnMode = (mode: ConnMode) => {
    connModeRef.current = mode;
    setConnModeState(mode);
  };

  /* ── Persistence (panel-namespaced localStorage contract) ── */

  const saveDesktopSettings = () => {
    const s = settingsRef.current;
    localStorage.setItem(P + "source", s.source);
    localStorage.setItem(P + "inputBackend", s.inputBackend);
    localStorage.setItem(P + "fit", s.fit);
    localStorage.setItem(P + "control", String(s.control));
    localStorage.setItem(P + "showCursor", String(s.showCursor));
    localStorage.setItem(P + "clipboardSyncMode", s.clipboardMode);
    localStorage.setItem(P + "fps", s.fps);
    localStorage.setItem(P + "quality", s.quality);
  };

  const saveGpuDesktopSettings = () => persistGpuDesktopSettings(GP, settingsRef.current);

  /* ── Derived device/stream parameters ── */

  const selectedDevice = (): DesktopDevice | undefined =>
    deviceCapsRef.current || undefined;

  const isAndroidGpuDevice = () => {
    const desktop = selectedDevice()?.desktop || {};
    return desktop.platform === "android" || desktop.displayServer === "mediaprojection";
  };

  const gpuFrameRate = () =>
    gpuDesktopFrameRate(settingsRef.current.gpuFrameRate, isAndroidGpuDevice());

  const gpuMaxVideoSize = () =>
    gpuDesktopMaxVideoSize({
      android: isAndroidGpuDevice(),
      scale: parseFloat(settingsRef.current.gpuScale) || 1,
      ratio: Math.min(window.devicePixelRatio || 1, 2),
      viewer: viewerRef.current,
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
    });

  const gpuPointerTypes = () => readGpuPointerTypes(settingsRef.current);

  const adaptiveSize = () =>
    adaptiveDesktopSize(
      stageRef.current?.getBoundingClientRect(),
      window.innerWidth,
      window.innerHeight,
      window.devicePixelRatio,
    );

  /* The panel always sizes adaptively (the stage follows the panel width);
   * fps/quality stay user-selectable via the compact toolbar presets. */
  const desktopOptions = () => {
    const s = settingsRef.current;
    const adaptive = adaptiveSize();
    const videoCodecs = selectedDevice()?.desktop?.videoCodecs;
    return {
      mode: "auto",
      source: s.source,
      fps: Number(s.fps),
      quality: Number(s.quality),
      width: adaptive.width,
      height: adaptive.height,
      inputBackend: s.inputBackend || "auto",
      showCursor: s.showCursor,
      format:
        forcedFormatRef.current ||
        (Array.isArray(videoCodecs) && videoCodecs.includes("h264") && typeof VideoDecoder === "function"
          ? "h264"
          : "mjpeg"),
    };
  };

  const desktopQuery = () => {
    const opts = desktopOptions();
    const q = new URLSearchParams({
      device: deviceRef.current,
      mode: opts.mode,
      source: opts.source,
      fps: String(opts.fps),
      quality: String(opts.quality),
      width: String(opts.width),
      height: String(opts.height),
      inputBackend: opts.inputBackend,
      showCursor: String(opts.showCursor),
      format: opts.format,
    });
    return q.toString();
  };

  /* ── Option lists ── */

  const updateInputBackendOptions = (desktop: DesktopCaps) => {
    const backends: (InputOption | string)[] =
      desktop.inputOptions && desktop.inputOptions.length
        ? desktop.inputOptions
        : (desktop.inputBackends || []).map((b) => ({ id: b }));
    const saved = localStorage.getItem(P + "inputBackend") || "auto";
    const automatic = backends[0] ? inputBackendLabel(backends[0]) : "";
    const options: SelectOption[] = [
      {
        value: "auto",
        label: automatic
          ? `${tRef.current("desktop.inputAuto")} · ${automatic}`
          : tRef.current("desktop.inputAuto"),
      },
    ].concat(
      backends.map((b) => ({
        value: (typeof b === "string" ? b : b.id) || "",
        label: inputBackendLabel(b),
      })),
    );
    setInputOptions(options.filter((o) => o.value));
    setInputDisabled(backends.length === 0);
    const value = options.some((o) => o.value === saved) ? saved : "auto";
    if (value !== settingsRef.current.inputBackend) updateSettings({ inputBackend: value });
  };

  const updateSourceOptions = () => {
    const d = selectedDevice() || ({ id: deviceRef.current } as DesktopDevice);
    setIsGpuDevice(!!d.gpuDesktop);
    const sources =
      d.desktop && d.desktop.sources && d.desktop.sources.length
        ? d.desktop.sources
        : [
            { id: "auto", label: tRef.current("desktop.sourceAuto"), kind: "screen" },
            { id: "screen:all", label: tRef.current("desktop.sourceAll"), kind: "screen" },
          ];
    const saved = localStorage.getItem(P + "source") || "";
    let current = settingsRef.current.source || saved || "auto";
    if (current === "virtual" || current === "screen:virtual" || current === "screen:root")
      current = "screen:all";
    if (current === "primary") current = "screen:primary";
    setSourceOptions(sources);
    updateInputBackendOptions(d.desktop || {});
    const value = sources.some((s) => s.id === current) ? current : "auto";
    if (value !== settingsRef.current.source) updateSettings({ source: value });
    saveDesktopSettings();
  };

  /* ── Frame stats ── */

  const resetFrameStats = () => {
    frameCountRef.current = 0;
    frameBytesRef.current = 0;
    statsStartedAtRef.current = 0;
    lastFrameAtRef.current = 0;
    currentSourceRef.current = "";
    setFrameInfo("");
  };

  const updateFrameInfo = () => {
    const canvas = canvasRef.current;
    if (!canvas || !canvas.width || !canvas.height) return;
    const now = performance.now();
    const { fps, kbps } = desktopFrameRates({
      count: frameCountRef.current,
      bytes: frameBytesRef.current,
      startedAt: statsStartedAtRef.current,
      now,
    });
    const age = lastFrameAtRef.current
      ? Math.max(0, (now - lastFrameAtRef.current) / 1000).toFixed(1)
      : "0.0";
    setFrameInfo(
      desktopFrameLine({
        size: `${canvas.width}×${canvas.height}`,
        source: currentSourceRef.current,
        fps,
        kbps,
        bytes: frameBytesRef.current,
        age,
      }),
    );
  };

  const gpuUpdateFrameInfo = () => {
    const now = performance.now();
    if (now - gpuLastStatsAtRef.current < 500) return;
    gpuLastStatsAtRef.current = now;
    const { fps, kbps } = desktopFrameRates({
      count: frameCountRef.current,
      bytes: frameBytesRef.current,
      startedAt: statsStartedAtRef.current,
      now,
    });
    const gpuCanvas = gpuCanvasRef.current;
    const video = videoRef.current;
    const size =
      gpuVideoModeRef.current === "webcodecs"
        ? `${gpuCanvas?.width || 0}×${gpuCanvas?.height || 0}`
        : video && video.videoWidth && video.videoHeight
          ? `${video.videoWidth}×${video.videoHeight}`
          : "video";
    setFrameInfo(
      desktopFrameLine({ size, source: currentSourceRef.current, fps, kbps, bytes: frameBytesRef.current }),
    );
  };

  /* ── H264 (legacy desktop stream) ── */

  const paintImage = (image: CanvasImageSource, width: number, height: number) => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const context = canvas.getContext("2d");
    if (!context) return;
    paintDesktopCanvas(context, image, width, height);
    setCanvasShown(true);
  };

  const fallbackToMjpeg = () => {
    if (forcedFormatRef.current === "mjpeg") return;
    forcedFormatRef.current = "mjpeg";
    pendingFrameRef.current = null;
    h264Ref.current?.close();
    const socket = wsRef.current;
    if (!socket || socket.readyState !== WebSocket.OPEN) return;
    clearTimeout(reconnectTimerRef.current);
    reconnectTimerRef.current = window.setTimeout(() => {
      if (wsRef.current === socket) connect(false);
    }, 150);
  };

  const decodeImage = (blob: Blob): Promise<HTMLImageElement> => {
    const { promise, resolve, reject } = Promise.withResolvers<HTMLImageElement>();
    const url = URL.createObjectURL(blob);
    const img = new Image();
    img.onload = () => {
      URL.revokeObjectURL(url);
      resolve(img);
    };
    img.onerror = () => {
      URL.revokeObjectURL(url);
      reject(new Error("image decode failed"));
    };
    img.src = url;
    return promise;
  };

  const drawFrame = (data: ArrayBuffer) => {
    // Predictive H.264 access units cannot use JPEG's latest-frame coalescing.
    if (h264Ref.current?.format === "h264") {
      if (h264().feed(data)) {
        frameCountRef.current++;
        frameBytesRef.current += data.byteLength || 0;
        if (!statsStartedAtRef.current) statsStartedAtRef.current = performance.now();
        lastFrameAtRef.current = performance.now();
        updateFrameInfo();
      }
      return;
    }
    pendingFrameRef.current = { data, connection: connSeqRef.current, socket: wsRef.current };
    if (drawingRef.current) return;
    drawingRef.current = true;
    requestAnimationFrame(async () => {
      const pending = pendingFrameRef.current;
      pendingFrameRef.current = null;
      const format = h264Ref.current?.format;
      const current = () => !!pending && pending.connection === connSeqRef.current &&
        pending.socket === wsRef.current && h264Ref.current?.format === format;
      const frame = current() ? pending?.data : null;
      try {
        if (frame) {
          const blob = new Blob([frame], { type: "image/jpeg" });
          if (await paintDesktopImage(blob, current, paintImage, decodeImage)) {
            frameCountRef.current++;
            frameBytesRef.current += frame.byteLength || 0;
            if (!statsStartedAtRef.current) statsStartedAtRef.current = performance.now();
            lastFrameAtRef.current = performance.now();
            updateFrameInfo();
          }
        }
      } catch (e) {
        console.error(e);
      }
      drawingRef.current = false;
      // New socket frames can arrive while decoding awaits.
      const next = pendingFrameRef.current as typeof pending;
      if (next && next.connection === connSeqRef.current && next.socket === wsRef.current) drawFrame(next.data);
      else pendingFrameRef.current = null;
    });
  };

  /* ── GPU video output (MSE + WebCodecs) ── */

  const closeGPUVideo = () => {
    setGpuEncoderStatus("");
    gpuMediaCleanupRef.current?.();
    gpuMediaCleanupRef.current = null;
    gpuQueueRef.current = [];
    gpuSourceBufferRef.current = null;
    const ms = gpuMediaSourceRef.current;
    if (ms && ms.readyState === "open") {
      try {
        ms.endOfStream();
      } catch {
        /* ignore */
      }
    }
    if (gpuVideoDecoderRef.current) {
      try {
        gpuVideoDecoderRef.current.close();
      } catch {
        /* ignore */
      }
    }
    gpuVideoDecoderRef.current = null;
    gpuVideoModeRef.current = "mse";
    gpuNeedKeyFrameRef.current = false;
    gpuMediaSourceRef.current = null;
    const video = videoRef.current;
    if (video) {
      if (video.src) URL.revokeObjectURL(video.src);
      video.removeAttribute("src");
      video.load();
    }
    setGpuDisplay("none");
  };

  const gpuUpdateBuffer = () => {
    const sb = gpuSourceBufferRef.current;
    const ms = gpuMediaSourceRef.current;
    if (!sb || sb.updating || !ms || ms.readyState !== "open" || gpuQueueRef.current.length === 0)
      return;
    gpuTrimBuffer();
    if (sb.updating) return;
    try {
      sb.appendBuffer(gpuQueueRef.current.shift() as ArrayBuffer);
    } catch (err) {
      gpuQueueRef.current = [];
      setStatus(errMessage(err), "err");
    }
  };

  // Long sessions would otherwise grow the buffered ranges without bound;
  // keep a sliding window behind the playhead.
  const gpuTrimBuffer = () => {
    const sb = gpuSourceBufferRef.current;
    const ms = gpuMediaSourceRef.current;
    const video = videoRef.current;
    if (!sb || sb.updating || !ms || ms.readyState !== "open" || !video) return;
    const buffered = sb.buffered;
    if (buffered.length === 0) return;
    const start = buffered.start(0);
    const removeUntil = video.currentTime - 10;
    if (removeUntil - start > 20) {
      try {
        sb.remove(start, removeUntil);
      } catch {
        /* ignore */
      }
    }
  };

  const gpuStartMedia = () => {
    // ManagedMediaSource (Safari) is not in the TS DOM lib yet.
    const win = window as unknown as { ManagedMediaSource?: typeof MediaSource };
    const MS = win.ManagedMediaSource || window.MediaSource;
    if (!MS) {
      setStatus(tRef.current("desktop.gpu.mediaSourceUnsupported"), "err");
      return;
    }
    closeGPUVideo();
    gpuVideoModeRef.current = "mse";
    const ms = new MS();
    gpuMediaSourceRef.current = ms;
    frameCountRef.current = 0;
    frameBytesRef.current = 0;
    statsStartedAtRef.current = performance.now();
    lastFrameAtRef.current = 0;
    setGpuDisplay("video");
    const video = videoRef.current;
    if (video) video.src = URL.createObjectURL(ms);
    gpuMediaCleanupRef.current = attachDesktopMediaSource(ms, {
      current: () => gpuMediaSourceRef.current === ms && gpuVideoModeRef.current === "mse",
      supported: mime => MS.isTypeSupported(mime),
      ready: buffer => { gpuSourceBufferRef.current = buffer; },
      update: gpuUpdateBuffer,
      error: cause => setStatus(errMessage(cause), "err"),
    });
  };

  const gpuStartWebCodecsVideo = (config: { codec?: string; width?: number; height?: number }) => {
    const started = startWebCodecsVideo(config, {
      canvas: gpuCanvasRef.current,
      decoder: gpuVideoDecoderRef,
      mode: gpuVideoModeRef,
      needKeyframe: gpuNeedKeyFrameRef,
      frames: gpuFrameCounters,
      startedAt: statsStartedAtRef,
      reset: closeGPUVideo,
      showCanvas: () => setGpuDisplay("canvas"),
      onPainted: () => setGpuEmpty(null),
      onFrame: gpuUpdateFrameInfo,
      fail: setGpuVideoError,
      onReady: () => setStatus(tRef.current("index.desktopReady"), "ok"),
    });
    if (!started) setStatus(tRef.current("desktop.gpu.webCodecsUnsupported"), "err");
  };

  const gpuHandleAndroidVideoPacket = (buffer: ArrayBuffer): boolean =>
    androidVideoPacket(buffer, gpuVideoDecoderRef.current, gpuNeedKeyFrameRef, gpuFrameCounters, setGpuVideoError);

  /* ── GPU protocol ── */

  const gpuSend = (message: unknown) => {
    const ws = wsRef.current;
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(message));
  };

  const gpuSendConfig = () => {
    const s = settingsRef.current;
    if (connModeRef.current !== "gpu" || !s.gpuSource) return;
    saveGpuDesktopSettings();
    const size = gpuMaxVideoSize();
    lastAdaptiveSizeRef.current = size;
    gpuSend({
      Config: {
        capturable_id: Number(s.gpuSource),
        capture_cursor: s.showCursor,
        max_width: size.width,
        max_height: size.height,
        client_name: "rdev-browser",
        frame_rate: s.gpuEnableVideo ? gpuFrameRate() : 0,
        encoder: s.gpuEncoder || "auto",
        pointer_backend: s.gpuPointerBackend || "auto",
        keyboard_backend: s.gpuKeyboardBackend || "auto",
      },
    });
    gpuSend(s.gpuEnableVideo && visibleRef.current && !document.hidden ? "ResumeVideo" : "PauseVideo");
  };

  const gpuSetSources = (list: string[]) => {
    const saved = initialSourceRef.current?.startsWith("gpu:") ? initialSourceRef.current.slice(4) : localStorage.getItem(GP + "source") || settingsRef.current.gpuSource || "";
    const names = gpuSourceNamesRef.current;
    const currentIdx = Number(settingsRef.current.gpuSource);
    const previousName = names && Number.isInteger(currentIdx) ? names[currentIdx] || "" : "";
    if (!list || list.length === 0) {
      gpuSourceNamesRef.current = [];
      setGpuEmpty({
        title: tRef.current("desktop.gpu.noSources"),
        hint: tRef.current("desktop.gpu.noSourcesHint"),
      });
      setStatus(tRef.current("desktop.gpu.noDesktopSource"), "err");
      return;
    }
    gpuSourceNamesRef.current = list;
    let value: string;
    if (saved && String(Number(saved)) === saved && list[Number(saved)] !== undefined) {
      value = saved;
    } else if (previousName && list.includes(previousName)) {
      value = String(list.indexOf(previousName));
    } else {
      value = String(Math.max(0, list.findIndex((name) => /\(GDI\)/.test(name))));
    }
    currentSourceRef.current = list[Number(value)] || "";
    updateSettings({ gpuSource: value });
    gpuSendConfig();
  };

  /* Pick a persisted value when still offered, else "auto" (the panel keeps
   * the page's option validation but not its select dropdowns). */
  const pickOptionValue = (options: SelectOption[] | undefined, current: string): string => {
    const list = (options || []).filter((o) => o && o.value && o.value !== "auto");
    return current === "auto" || list.some((o) => o.value === current) ? current : "auto";
  };

  const gpuSetEncoders = (options: SelectOption[]) => {
    const current =
      localStorage.getItem(GP + "encoder") || settingsRef.current.gpuEncoder || "auto";
    const list: SelectOption[] = [
      { value: "auto", label: tRef.current("desktop.gpu.auto") },
      ...(options || []).filter((o) => o && o.value && o.value !== "auto"),
    ];
    setGpuEncoderOptions(list);
    updateSettings({ gpuEncoder: pickOptionValue(options, current) });
  };

  const gpuSetInputCapabilities = (capabilities: {
    pointerOptions?: SelectOption[];
    keyboardOptions?: SelectOption[];
    options?: SelectOption[];
  }) => {
    updateSettings({
      gpuPointerBackend: pickOptionValue(
        capabilities.pointerOptions || capabilities.options || [],
        localStorage.getItem(GP + "pointerBackend") || settingsRef.current.gpuPointerBackend || "auto",
      ),
      gpuKeyboardBackend: pickOptionValue(
        capabilities.keyboardOptions || capabilities.options || [],
        localStorage.getItem(GP + "keyboardBackend") || settingsRef.current.gpuKeyboardBackend || "auto",
      ),
    });
  };

  /* ── Clipboard sync ── */

  const clipboard = useDesktopClipboard({
    // Folded-away panels never touch the browser clipboard.
    active: () => visibleRef.current,
    mode: () => settingsRef.current.clipboardMode,
    connMode: () => connModeRef.current,
    socket: () => wsRef.current,
    send: (message) => wsRef.current?.send(JSON.stringify(message)),
    gpuSend,
    setClipStatus,
    t: (key) => tRef.current(key),
  });
  const pollClipboardSync = clipboard.poll;
  const stopClipboardSync = clipboard.stop;
  const setClipboardCapabilities = clipboard.setCapabilities;
  const handleRemoteClipboard = clipboard.handleRemote;

  /* ── Connect / disconnect ── */

  const nextDesktopReconnectDelay = () => {
    const base = desktopReconnectDelayRef.current;
    desktopReconnectDelayRef.current = Math.min(30000, desktopReconnectDelayRef.current * 2);
    const spread = Math.max(1, Math.floor(base / 5));
    return base - spread + Math.floor(Math.random() * (spread * 2 + 1));
  };

  const scheduleDesktopReconnect = (dev: string, connID: number) => {
    const mode = connModeRef.current;
    if (manualDisconnectRef.current || connID !== connSeqRef.current || !mode || dev !== deviceRef.current)
      return;
    const delay = nextDesktopReconnectDelay();
    clearTimeout(reconnectTimerRef.current);
    reconnectTimerRef.current = window.setTimeout(() => {
      if (
        !manualDisconnectRef.current &&
        connID === connSeqRef.current &&
        connModeRef.current === mode &&
        dev === deviceRef.current &&
        visibleRef.current
      ) {
        if (mode === "gpu") connectGPUDesktop(dev, true);
        else void connect(false, true);
      }
    }, delay);
  };

  const desktopReleaseKeyboard = () => {
    const ws = wsRef.current;
    if (connModeRef.current !== "desktop" || !ws || ws.readyState !== WebSocket.OPEN) {
      desktopInput.clear();
      return;
    }
    try {
      const release = (input: Record<string, unknown>) => ws.send(JSON.stringify({ op: "input", ...input }));
      desktopInput.release(release);
    } catch (e) {
      setStatus(errMessage(e), "err");
    }
  };

  const gpuReleaseKeyboard = () => {
    const ws = wsRef.current;
    if (connModeRef.current !== "gpu" || !ws || ws.readyState !== WebSocket.OPEN) {
      gpuHeldKeysRef.current.clear();
      return;
    }
    for (const held of gpuHeldKeysRef.current.values())
      gpuSend({
        KeyboardEvent: { ...held, event_type: "up", ctrl: false, alt: false, shift: false, meta: false },
      });
    gpuHeldKeysRef.current.clear();
    gpuSend("ReleaseKeyboard");
  };

  function disconnect() {
    manualDisconnectRef.current = true;
    desktopReleaseKeyboard();
    gpuReleaseKeyboard();
    connSeqRef.current++;
    clearTimeout(reconnectTimerRef.current);
    clearTimeout(resizeTimerRef.current);
    if (wsRef.current) {
      wsRef.current.close();
      wsRef.current = null;
    }
    h264Ref.current?.close();
    closeGPUVideo();
    remoteInputRef.current = false;
    stopClipboardSync();
    setClipboardCapabilities(null);
    setControlDisabled(false);
    setConnMode("");
    setCanvasShown(false);
    setGpuEmpty(null);
    lastCloseMsgRef.current = "";
    desktopReconnectDelayRef.current = 1000;
    pendingPwRef.current = "";
    gpuAuthPendingRef.current = false;
    setAuthOpen(false);
    setAuthError("");
    resetFrameStats();
    setStatus(tRef.current("desktop.closed"), "warn");
  }

  const gpuURL = (dev: string) => {
    const params = new URLSearchParams();
    if (passwordRef.current) params.set("password", passwordRef.current);
    const path = `/gpu-desktop/${encodeURIComponent(dev)}/ws`;
    return wsURL(path + (params.toString() ? "?" + params.toString() : ""));
  };

  function connectGPUDesktop(dev: string, reconnecting = false) {
    const tt = tRef.current;
    setConnMode("gpu");
    remoteInputRef.current = true;
    setControlDisabled(false);
    let control = settingsRef.current.control;
    if (!controlPreferenceSet.current) {
      control = true;
      updateSettings({ control: true });
    }
    setControlActive(control);
    setCursorHidden(settingsRef.current.showCursor);
    setCanvasShown(false);
    setGpuEmpty({ title: tt("desktop.gpu.connecting"), hint: tt("desktop.gpu.connectingHint") });
    resetFrameStats();
    gpuHadMsgRef.current = false;
    const ws = new WebSocket(gpuURL(dev));
    wsRef.current = ws;
    const connID = reconnecting ? connSeqRef.current : ++connSeqRef.current;
    lastCloseMsgRef.current = "";
    ws.binaryType = "arraybuffer";
    setStatus(tt("common.connecting"));
    ws.onopen = () => {
      if (connID !== connSeqRef.current || wsRef.current !== ws) return;
      desktopReconnectDelayRef.current = 1000;
      setStatus(tRef.current("desktop.starting"));
      gpuSend("GetCapturableList");
      gpuSendConfig();
    };
    ws.onerror = () => {
      if (connID !== connSeqRef.current || wsRef.current !== ws) return;
      setStatus(tRef.current("common.error"), "err");
    };
    ws.onclose = () => {
      if (connID !== connSeqRef.current || wsRef.current !== ws) return;
      gpuHeldKeysRef.current.clear();
      wsRef.current = null;
      setGpuEncoderStatus("");
      stopClipboardSync();
      setClipboardCapabilities(null);
      // A password-protected device rejects a bad ?password= at the HTTP
      // upgrade (401), so the socket closes before any protocol message.
      // Treat that signature as an auth failure and ask again.
      if (
        !gpuHadMsgRef.current &&
        !gpuBypassRef.current &&
        !manualDisconnectRef.current &&
        selectedDevice()?.hasPassword &&
        passwordRef.current
      ) {
        forgetDevicePassword(dev);
        passwordRef.current = "";
        gpuAuthPendingRef.current = true;
        setStatus(tRef.current("common.passwordWrong"), "err");
        setAuthError(tRef.current("common.passwordWrong"));
        setAuthRound((r) => r + 1);
        setAuthOpen(true);
        return;
      }
      setStatus(
        lastCloseMsgRef.current || tRef.current("desktop.closed"),
        lastCloseMsgRef.current ? "err" : "warn",
      );
      scheduleDesktopReconnect(dev, connID);
    };
    ws.onmessage = (evt) => {
      if (connID !== connSeqRef.current || wsRef.current !== ws) return;
      gpuHadMsgRef.current = true;
      if (evt.data instanceof ArrayBuffer) {
        if (gpuHandleAndroidVideoPacket(evt.data)) return;
        frameCountRef.current++;
        frameBytesRef.current += evt.data.byteLength || 0;
        lastFrameAtRef.current = performance.now();
        if (gpuQueueRef.current.length > 30)
          gpuQueueRef.current.splice(0, gpuQueueRef.current.length - 30);
        gpuQueueRef.current.push(evt.data);
        gpuUpdateBuffer();
        gpuUpdateFrameInfo();
        const video = videoRef.current;
        if (video && video.seekable.length > 0) {
          const end = video.seekable.end(video.seekable.length - 1);
          if (Number.isFinite(end)) {
            // Chase the live edge while keeping a small playback buffer;
            // bounds latency even when decoding briefly stalls and
            // readyState drops.
            const keep = settingsRef.current.gpuLowLatency ? 0.15 : 0.75;
            if (end - video.currentTime > keep + 0.75) video.currentTime = end - keep;
          }
        }
        return;
      }
      const msg = JSON.parse(evt.data);
      if (msg === "NewVideo") {
        gpuStartMedia();
        setGpuEmpty(null);
      } else if (msg && msg.AndroidVideoConfig) {
        gpuStartWebCodecsVideo(msg.AndroidVideoConfig);
        setGpuEncoderStatus(desktopEncoderStatus({ encoderBackend: "mediacodec-h264", ...msg.AndroidVideoConfig }, tt));
        setGpuEmpty(null);
      } else if (msg === "ConfigOk") {
        setGpuEmpty(null);
        setStatus(tRef.current("index.desktopReady"), "ok");
      } else if (msg && msg.CapturableList) gpuSetSources(msg.CapturableList);
      else if (msg && msg.EncoderCapabilities) gpuSetEncoders(msg.EncoderCapabilities.options || []);
      else if (msg && msg.InputCapabilities) gpuSetInputCapabilities(msg.InputCapabilities);
      else if (msg && msg.ClipboardCapabilities) setClipboardCapabilities(msg.ClipboardCapabilities);
      else if (msg && msg.ClipboardContent) handleRemoteClipboard(msg.ClipboardContent);
      else if (msg && msg.RuntimeStatus) {
        const encoder = desktopRuntimeEncoderStatus(msg.RuntimeStatus, tt);
        if (encoder !== undefined) setGpuEncoderStatus(encoder);
      }
      else if (msg && msg.ConfigError) {
        lastCloseMsgRef.current = msg.ConfigError;
        setStatus(msg.ConfigError, "err");
      } else if (msg && msg.Error) {
        if (String(msg.Error).startsWith("clipboard:")) {
          clipboard.failRemote(msg.Error);
        } else {
          lastCloseMsgRef.current = msg.Error;
          setStatus(msg.Error, "err");
        }
      }
    };
  }

  /* GPU auth: the password travels in the socket URL, so collect it through
   * the overlay before connecting when the device requires one. */
  const connectGPUWithAuth = async (dev: string) => {
    const caps = selectedDevice();
    gpuBypassRef.current = caps?.hasPassword ? await deviceAuthBypassed() : false;
    if (manualDisconnectRef.current || deviceRef.current !== dev || !visibleRef.current) return;
    if (caps?.hasPassword && !gpuBypassRef.current && !passwordRef.current) {
      const saved = getDevicePassword(dev);
      if (saved) passwordRef.current = saved;
    }
    if (caps?.hasPassword && !gpuBypassRef.current && !passwordRef.current) {
      gpuAuthPendingRef.current = true;
      setStatus(tRef.current("desktop.authRequired"), "warn");
      setAuthError("");
      setAuthRound((r) => r + 1);
      setAuthOpen(true);
      return;
    }
    connectGPUDesktop(dev);
  };

  async function connect(resetFormat = true, retry = false) {
    const retryDelay = desktopReconnectDelayRef.current;
    disconnect();
    if (retry) desktopReconnectDelayRef.current = retryDelay;
    if (resetFormat) forcedFormatRef.current = null;
    manualDisconnectRef.current = false;
    const dev = deviceRef.current;
    if (!dev || !visibleRef.current) {
      if (!dev) setStatus(tRef.current("common.noDevices"), "err");
      return;
    }
    const sel = selectedDevice();
    if (sel?.gpuDesktop) {
      void connectGPUWithAuth(dev);
      return;
    }
    setConnMode("desktop");
    remoteInputRef.current = false;
    h264Ref.current?.close();
    resetFrameStats();
    const ws = new WebSocket(wsURL("/desktop?" + desktopQuery()));
    wsRef.current = ws;
    const connID = ++connSeqRef.current;
    lastCloseMsgRef.current = "";
    ws.binaryType = "arraybuffer";
    setStatus(tRef.current("common.connecting"));
    let retryAfterClose = true;
    ws.onmessage = (evt) => {
      if (connID !== connSeqRef.current || wsRef.current !== ws) return;
      if (evt.data instanceof ArrayBuffer) {
        drawFrame(evt.data);
        return;
      }
      const msg = JSON.parse(evt.data);
      const tt = tRef.current;
      if (msg.op === "auth") {
        setStatus(msg.message || tt("desktop.authRequired"), "warn");
        setAuthError("");
        setAuthRound((r) => r + 1);
        setAuthOpen(true);
      } else if (msg.op === "auth_ok") {
        if (pendingPwRef.current) {
          rememberDevicePassword(dev, pendingPwRef.current);
          pendingPwRef.current = "";
        }
        setAuthOpen(false);
        setAuthError("");
        setStatus(tt("desktop.starting"));
      } else if (msg.op === "auth_fail") {
        retryAfterClose = false;
        forgetDevicePassword(dev);
        pendingPwRef.current = "";
        setAuthError(msg.message || tt("common.passwordWrong"));
      } else if (msg.op === "starting") {
        setStatus(tt("desktop.starting"));
      } else if (msg.op === "ready") {
        desktopReleaseKeyboard();
        desktopReconnectDelayRef.current = 1000;
        const canvas = canvasRef.current;
        if (canvas) {
          canvas.width = msg.width || 1;
          canvas.height = msg.height || 1;
        }
        setCanvasShown(true);
        currentSourceRef.current = msg.source || "";
        if (msg.format === "h264" && !h264().setup(msg)) fallbackToMjpeg();
        if (msg.inputBackend && msg.inputBackend !== settingsRef.current.inputBackend)
          updateSettings({ inputBackend: msg.inputBackend });
        remoteInputRef.current = !!(msg.desktop && msg.desktop.input);
        setControlDisabled(!remoteInputRef.current);
        let control = settingsRef.current.control;
        if (remoteInputRef.current && !controlPreferenceSet.current) control = true;
        if (!remoteInputRef.current) control = false;
        if (control !== settingsRef.current.control) updateSettings({ control });
        setControlActive(control && remoteInputRef.current);
        setCursorHidden(settingsRef.current.showCursor);
        if (remoteInputRef.current && !controlPreferenceSet.current) saveDesktopSettings();
        updateFrameInfo();
        const source = currentSourceRef.current ? ` · ${currentSourceRef.current}` : "";
        const encoder = desktopEncoderStatus(msg, tt);
        const w = canvas?.width || 0;
        const h = canvas?.height || 0;
        setStatus(`${tt("index.desktopReady")} · ${w}×${h}${source}${encoder ? ` · ${encoder}` : ""}`, "ok");
        setClipboardCapabilities(
          msg.desktop && msg.desktop.clipboard
            ? {
                supported: true,
                read: true,
                write: true,
                formats: ["text/plain", "text/html", "image/png", "text/uri-list"],
                maxBytes: 16777216,
              }
            : null,
        );
      } else if (msg.op === "error") {
        retryAfterClose = msg.retryable === true;
        lastCloseMsgRef.current = msg.message || tt("common.error");
        setStatus(lastCloseMsgRef.current, "err");
      } else if (msg.op === "closed") {
        desktopInput.clear();
        remoteInputRef.current = false;
        const recoveryFormat = desktopEncoderRecoveryFormat(msg.format, msg.encoderBackend);
        if (recoveryFormat) {
          forcedFormatRef.current = recoveryFormat;
          retryAfterClose = true;
          desktopReconnectDelayRef.current = 1000;
        }
        lastCloseMsgRef.current = msg.message || tt("desktop.closed");
        setStatus(lastCloseMsgRef.current, msg.message ? "err" : "warn");
      } else if (msg.op === "clipboard_content") {
        handleRemoteClipboard({ items: msg.clipboardItems || [], text: msg.text || "" });
      } else if (msg.op === "clipboard_error") {
        clipboard.failRemote(msg.message || tt("desktop.clipboardRemoteError"));
      } else if (msg.op === "input_error") {
        setStatus(msg.message || tt("common.error"), "err");
      }
    };
    ws.onclose = () => {
      if (connID !== connSeqRef.current || wsRef.current !== ws) return;
      desktopInput.clear();
      wsRef.current = null;
      stopClipboardSync();
      setClipboardCapabilities(null);
      h264Ref.current?.close();
      setStatus(
        lastCloseMsgRef.current || tRef.current("desktop.closed"),
        lastCloseMsgRef.current ? "err" : "warn",
      );
      if (retryAfterClose) scheduleDesktopReconnect(dev, connID);
    };
  }

  const scheduleReconnectIfActive = () => {
    if (!wsRef.current) return;
    if (connModeRef.current === "gpu") {
      gpuSendConfig();
      return;
    }
    clearTimeout(reconnectTimerRef.current);
    reconnectTimerRef.current = window.setTimeout(() => connect(false), 150);
  };

  const scheduleAdaptiveReconnect = () => {
    if (!visibleRef.current || !wsRef.current) return;
    clearTimeout(resizeTimerRef.current);
    resizeTimerRef.current = window.setTimeout(() => {
      if (!visibleRef.current || !wsRef.current) return;
      if (connModeRef.current === "gpu") {
        const next = gpuMaxVideoSize();
        const last = lastAdaptiveSizeRef.current;
        if (last && Math.abs(next.width - last.width) < 64 && Math.abs(next.height - last.height) < 64)
          return;
        lastAdaptiveSizeRef.current = next;
        gpuSendConfig();
        return;
      }
      const next = adaptiveSize();
      const last = lastAdaptiveSizeRef.current;
      if (last && Math.abs(next.width - last.width) < 64 && Math.abs(next.height - last.height) < 64)
        return;
      lastAdaptiveSizeRef.current = next;
      connect(false);
    }, 300);
  };

  /* ── Actions ── */

  const saveScreenshot = () =>
    saveDesktopScreenshot({
      gpu: connModeRef.current === "gpu",
      videoMode: gpuVideoModeRef.current,
      gpuCanvas: gpuCanvasRef.current,
      video: videoRef.current,
      canvas: canvasRef.current,
      canvasShown,
      device: deviceRef.current,
    });

  const toggleGpuVideo = () => {
    const next = !settingsRef.current.gpuEnableVideo;
    updateSettings({ gpuEnableVideo: next });
    saveGpuDesktopSettings();
    gpuSend(next ? "ResumeVideo" : "PauseVideo");
  };

  /* ── GPU input ── */

  const gpuVideoContentRect = () =>
    computeGpuVideoContentRect({
      rect: overlayRef.current?.getBoundingClientRect() || new DOMRect(),
      videoWidth:
        gpuVideoModeRef.current === "webcodecs"
          ? gpuCanvasRef.current?.width || 0
          : videoRef.current?.videoWidth || 0,
      videoHeight:
        gpuVideoModeRef.current === "webcodecs"
          ? gpuCanvasRef.current?.height || 0
          : videoRef.current?.videoHeight || 0,
      stretch: settingsRef.current.gpuStretch,
      fit: settingsRef.current.fit,
    });

  const gpuPointerMessage = (event: PointerEvent, eventType = event.type) => {
    const previous = gpuLastPointerRef.current.get(event.pointerId) || {
      x: event.clientX,
      y: event.clientY,
    };
    gpuLastPointerRef.current.set(event.pointerId, { x: event.clientX, y: event.clientY });
    return buildGpuPointerMessage(event, {
      rect: gpuVideoContentRect(),
      previous,
      minPressure: settingsRef.current.gpuMinPressure,
      eventType,
    });
  };

  const gpuTextInput = (text: string | null) => {
    if (!text || connModeRef.current !== "gpu" || !settingsRef.current.control || !remoteInputRef.current)
      return;
    gpuSend({ TextInputEvent: { text } });
  };

  const gpuInputAllowed = () =>
    visibleRef.current &&
    connModeRef.current === "gpu" &&
    settingsRef.current.control &&
    remoteInputRef.current;

  /* ── Keep latest logic reachable from stable native listeners ── */

  const apiRef = useRef({
    connect,
    disconnect,
    updateSourceOptions,
    gpuSendConfig,
    gpuPointerMessage,
    gpuKeyMessage,
    gpuTextInput,
    gpuInputAllowed,
    gpuPointerTypes,
    gpuSend,
    gpuReleaseKeyboard,
    desktopReleaseKeyboard,
    pollClipboardSync,
    scheduleAdaptiveReconnect,
  });
  apiRef.current = {
    connect,
    disconnect,
    updateSourceOptions,
    gpuSendConfig,
    gpuPointerMessage,
    gpuKeyMessage,
    gpuTextInput,
    gpuInputAllowed,
    gpuPointerTypes,
    gpuSend,
    gpuReleaseKeyboard,
    desktopReleaseKeyboard,
    pollClipboardSync,
    scheduleAdaptiveReconnect,
  };

  /* ── Canvas/overlay listeners: elements unmount with connMode, so re-attach. ── */

  useEffect(
    () =>
      attachDesktopStageInput({
        canvas: canvasRef.current,
        overlay: overlayRef.current,
        input: desktopInput,
        api: () => apiRef.current,
        composing: gpuComposingRef,
        ignoreNextTextInput: gpuIgnoreNextTextInputRef,
        heldKeys: gpuHeldKeysRef,
        socket: () => wsRef.current,
        mode: () => {
          if (!visibleRef.current || connModeRef.current !== "desktop") return null;
          if (settingsRef.current.control && remoteInputRef.current) return "control";
          return settingsRef.current.showCursor ? "cursor" : null;
        },
        mouseEnabled: () => settingsRef.current.gpuEnableMouse,
        socketOpen: () => wsRef.current?.readyState === WebSocket.OPEN,
        fail: setGpuVideoError,
      }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [connMode],
  );

  /* ── Stage ResizeObserver + unload guard (mount once) ── */

  useEffect(() => {
    const onUnload = () => apiRef.current.disconnect();
    window.addEventListener("beforeunload", onUnload);
    const stage = stageRef.current;
    const observer = new ResizeObserver(() => apiRef.current.scheduleAdaptiveReconnect());
    if (stage) observer.observe(stage);
    return () => {
      window.removeEventListener("beforeunload", onUnload);
      observer.disconnect();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  /* ── Window-level capture: attached only while the tab is visible so a
   * hidden panel never touches the clipboard or steals composer keystrokes. ── */

  useEffect(() => {
    if (!visible) return;
    return attachDesktopCaptureListeners({
      overlay: overlayRef,
      api: () => apiRef.current,
      composing: gpuComposingRef,
      ignoreNextTextInput: gpuIgnoreNextTextInputRef,
      streaming: () => connModeRef.current === "gpu",
      resumeVideo: () => settingsRef.current.gpuEnableVideo,
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visible]);

  const initialSourceRef = useRef(initialSource);
  initialSourceRef.current = initialSource;
  useEffect(() => {
    if (!initialSource) return;
    if (initialSource.startsWith("gpu:")) {
      const source = initialSource.slice(4);
      if (settingsRef.current.gpuSource === source) return;
      updateSettings({ gpuSource: source });
      if (connModeRef.current === "gpu") apiRef.current.gpuSendConfig();
    } else if (settingsRef.current.source !== initialSource) {
      updateSettings({ source: initialSource });
      if (connModeRef.current === "desktop") apiRef.current.connect();
    }
  }, [initialSource]);

  /* A fold is not a disconnect. Keep capture/decoder/auth state, but do not
   * start a hidden tab or resize an existing capture to a hidden stage. */
  const connectionDeviceRef = useRef(device);
  useEffect(() => {
    let cancelled = false;
    if (connectionDeviceRef.current !== device) {
      apiRef.current.disconnect();
      connectionDeviceRef.current = device;
      deviceCapsRef.current = null;
      passwordRef.current = "";
      manualDisconnectRef.current = false;
    }
    if (!visible) {
      clearTimeout(resizeTimerRef.current);
      apiRef.current.desktopReleaseKeyboard();
      apiRef.current.gpuReleaseKeyboard();
      if (connModeRef.current === "gpu") apiRef.current.gpuSend("PauseVideo");
      return;
    }
    const cached = deviceCapsRef.current;
    if (cached && cached.id === device) {
      if (gpuAuthPendingRef.current) return;
      const socket = wsRef.current;
      if (socket && socket.readyState <= WebSocket.OPEN) {
        if (connModeRef.current === "gpu" && settingsRef.current.gpuEnableVideo && !document.hidden)
          apiRef.current.gpuSend("ResumeVideo");
      } else if (!manualDisconnectRef.current) {
        apiRef.current.connect();
      }
      return;
    }
    apiJSON<DesktopDevice[]>("/api/devices")
      .then((list) => {
        if (cancelled) return;
        deviceCapsRef.current = list.find((d) => d.id === device) || null;
        apiRef.current.updateSourceOptions();
        if (visibleRef.current) apiRef.current.connect();
      })
      .catch((e) => {
        if (!cancelled) setStatus(errMessage(e), "err");
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visible, device]);

  /* Unmount: close everything (no setState — the panel is gone). */
  useEffect(
    () => () => {
      manualDisconnectRef.current = true;
      apiRef.current.desktopReleaseKeyboard();
      apiRef.current.gpuReleaseKeyboard();
      connSeqRef.current++;
      clearTimeout(reconnectTimerRef.current);
      clearTimeout(resizeTimerRef.current);
      clipboard.stop();
      if (wsRef.current) {
        wsRef.current.close();
        wsRef.current = null;
      }
    },
    [],
  );

  /* ── UI event handlers ── */

  const onControlChange = (checked: boolean) => {
    controlPreferenceSet.current = true;
    if (!checked) {
      desktopReleaseKeyboard();
      gpuReleaseKeyboard();
    }
    updateSettings({ control: checked });
    setControlActive(checked && remoteInputRef.current);
    if (checked) {
      const target = connModeRef.current === "gpu" ? overlayRef.current : canvasRef.current;
      target?.focus();
    }
    saveDesktopSettings();
  };

  const onShowCursorChange = (checked: boolean) => {
    updateSettings({ showCursor: checked });
    setCursorHidden(checked);
    saveDesktopSettings();
    scheduleReconnectIfActive();
  };

  const submitAuth = (password: string) => {
    if (!password) return;
    const dev = deviceRef.current;
    if (connModeRef.current === "gpu" || gpuAuthPendingRef.current) {
      // GPU transport authenticates via the ?password= socket URL query.
      gpuAuthPendingRef.current = false;
      passwordRef.current = password;
      rememberDevicePassword(dev, password);
      setAuthOpen(false);
      setAuthError("");
      manualDisconnectRef.current = false;
      connectGPUDesktop(dev);
      return;
    }
    pendingPwRef.current = password;
    const ws = wsRef.current;
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ op: "auth", password, ...desktopOptions() }));
    }
  };

  const gpuObjectFit =
    settings.fit === "actual" ? "none" : settings.gpuStretch ? "fill" : "contain";

  const canvasCursor = cursorHidden ? "none" : controlActive ? "crosshair" : undefined;

  const sourceOptionLabel = (s: DesktopSource) =>
    s.label ||
    (s.id === "auto"
      ? t("desktop.sourceAuto")
      : s.id === "screen:all"
        ? t("desktop.sourceAll")
        : sourceLabel(s));

  const toolbarStatus = [status.text || t("desktop.idle"),
    connMode === "gpu" && status.cls === "ok" && settings.gpuEnableVideo && gpuEncoderStatus,
    connMode && frameInfo,
  ].filter(Boolean).join(" · ");

  /* ── Render ── */

  return (
    <div className="relative flex h-full min-h-0 flex-col bg-background">
      {/* Compact toolbar */}
      <div className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-border-seam px-2 py-1 text-xs text-muted-foreground">
        <Button
          size="sm"
          variant={connMode ? "outline" : "default"}
          className="h-6 px-2 text-xs"
          onClick={() => (connMode ? disconnect() : connect())}
        >
          {connMode ? t("desktop.disconnect") : t("common.connect")}
        </Button>
        <label className="inline-flex items-center gap-1 whitespace-nowrap" title={t("desktop.control")}>
          <Switch
            checked={settings.control}
            disabled={controlDisabled}
            onCheckedChange={onControlChange}
          />
          <span>{t("desktop.control")}</span>
        </label>
        <label className="inline-flex items-center gap-1 whitespace-nowrap" title={t("desktop.showCursor")}>
          <Switch checked={settings.showCursor} onCheckedChange={onShowCursorChange} />
          <span>{t("desktop.showCursor")}</span>
        </label>

        {!isGpuDevice && sourceOptions.length > 1 && (
          <Select
            value={settings.source}
            onValueChange={(v) => {
              updateSettings({ source: v });
              saveDesktopSettings();
              scheduleReconnectIfActive();
            }}
          >
            <SelectTrigger className="h-6 w-auto max-w-36 text-xs" title={t("desktop.source")}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {sourceOptions.map((s) => (
                <SelectItem key={s.id} value={s.id}>
                  {sourceOptionLabel(s)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        {!isGpuDevice && !inputDisabled && inputOptions.length > 1 && (
          <Select
            value={settings.inputBackend}
            onValueChange={(v) => {
              updateSettings({ inputBackend: v });
              saveDesktopSettings();
              scheduleReconnectIfActive();
            }}
          >
            <SelectTrigger className="h-6 w-auto max-w-36 text-xs" title={t("desktop.inputBackend")}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {inputOptions.map((o) => (
                <SelectItem key={o.value} value={o.value}>
                  {o.label || (o.value === "auto" ? t("desktop.inputAuto") : o.value)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        {!isGpuDevice && (
          <>
            <Select
              value={settings.fps}
              onValueChange={(v) => {
                updateSettings({ fps: v });
                saveDesktopSettings();
                scheduleReconnectIfActive();
              }}
            >
              <SelectTrigger className="h-6 w-auto text-xs" title={t("desktop.fps")}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {desktopFPSPresets.map((v) => (
                  <SelectItem key={v} value={v}>
                    {v} fps
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select
              value={settings.quality}
              onValueChange={(v) => {
                updateSettings({ quality: v });
                saveDesktopSettings();
                scheduleReconnectIfActive();
              }}
            >
              <SelectTrigger className="h-6 w-auto text-xs" title={t("desktop.quality")}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {desktopQualityPresets.map((v) => (
                  <SelectItem key={v} value={v}>
                    Q{v}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </>
        )}

        {isGpuDevice && gpuEncoderOptions.length > 1 && (
          <Select
            value={settings.gpuEncoder}
            onValueChange={(v) => {
              updateSettings({ gpuEncoder: v });
              gpuSendConfig();
            }}
          >
            <SelectTrigger className="h-6 w-auto max-w-36 text-xs" title={t("desktop.gpu.video")}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {gpuEncoderOptions.map((o) => (
                <SelectItem key={o.value} value={o.value}>
                  {o.label || (o.value === "auto" ? t("desktop.gpu.auto") : o.value)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        {isGpuDevice && connMode === "gpu" && (
          <Button
            size="sm"
            variant="ghost"
            className="h-6 w-6 p-0"
            aria-label={
              settings.gpuEnableVideo
                ? t("agent.desktopPauseVideo")
                : t("agent.desktopResumeVideo")
            }
            onClick={toggleGpuVideo}
          >
            <Icon name={settings.gpuEnableVideo ? "Pause" : "Play"} className="size-3.5" />
          </Button>
        )}
        <Button
          size="sm"
          variant="ghost"
          className="h-6 w-6 p-0"
          aria-label={t("desktop.screenshot")}
          onClick={saveScreenshot}
        >
          <Icon name="Download" className="size-3.5" />
        </Button>

        <span
          className={cn(
            "min-w-0 flex-1 truncate text-right text-[11px]",
            status.cls === "ok" && "text-success",
            status.cls === "warn" && "text-warning-text",
            status.cls === "err" && "text-destructive-text",
            !status.cls && "text-muted-foreground",
          )}
          title={toolbarStatus}
        >
          {toolbarStatus}
        </span>
        {clipStatus && (
          <span className="min-w-0 truncate text-[11px] text-muted-foreground" aria-live="polite">
            {clipStatus}
          </span>
        )}
      </div>

      {/* Stage */}
      <div
        ref={stageRef}
        className={cn(
          "flex min-h-0 flex-1 p-1.5 overscroll-contain",
          connMode === "gpu"
            ? "flex-col items-stretch justify-start gap-2 overflow-hidden"
            : cn(
                "overflow-auto",
                settings.fit === "actual"
                  ? "items-start justify-start"
                  : "items-center justify-center",
              ),
        )}
      >
        {connMode !== "gpu" && (
          <canvas
            ref={canvasRef}
            tabIndex={0}
            aria-label={t("title.desktop")}
            className="rounded-lg border border-border-seam bg-black outline-none focus-visible:ring-2 focus-visible:ring-ring"
            style={{
              display: canvasShown ? "block" : "none",
              cursor: canvasCursor,
              touchAction: controlActive ? "none" : undefined,
              width: "auto",
              height: "auto",
              maxWidth: settings.fit === "fit" ? "100%" : "none",
              maxHeight: settings.fit === "fit" ? "100%" : "none",
            }}
          />
        )}
        {connMode === "gpu" && (
          <div
            ref={viewerRef}
            className="relative min-h-0 w-full flex-1 overflow-hidden rounded-lg border border-border-seam bg-black shadow-[0_16px_40px_rgba(0,0,0,.20)]"
          >
            <video
              ref={videoRef}
              autoPlay
              muted
              playsInline
              className="block h-full w-full bg-black"
              style={{ objectFit: gpuObjectFit, display: gpuDisplay === "video" ? "block" : "none" }}
            />
            <canvas
              ref={gpuCanvasRef}
              className="block h-full w-full bg-black"
              style={{ objectFit: gpuObjectFit, display: gpuDisplay === "canvas" ? "block" : "none" }}
            />
            <div
              ref={overlayRef}
              tabIndex={0}
              aria-label="remote desktop input area"
              className="absolute inset-0 touch-none outline-none"
              style={{ cursor: controlActive ? "crosshair" : undefined }}
            />
            {gpuEmpty && (
              <div
                className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center p-4 text-center leading-loose text-muted-foreground"
                style={{
                  background:
                    "radial-gradient(circle at center, color-mix(in oklab, var(--timeline-accent) 10%, transparent), transparent 42%)",
                }}
              >
                <strong className="mb-1 text-sm text-foreground">{gpuEmpty.title}</strong>
                <span className="text-xs">{gpuEmpty.hint}</span>
              </div>
            )}
          </div>
        )}
        {connMode !== "gpu" && !canvasShown && (
          <div className="text-center text-xs leading-loose text-muted-foreground">
            {t("desktop.empty")}
          </div>
        )}
      </div>

      {authOpen && (
        <TabAuthOverlay
          key={authRound}
          deviceId={device}
          error={authError}
          onSubmit={submitAuth}
          t={t}
        />
      )}
    </div>
  );
}
