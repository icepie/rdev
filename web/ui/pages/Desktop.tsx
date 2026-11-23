import { desktopEncoderRecoveryFormat, desktopEncoderStatus, desktopRuntimeEncoderStatus, desktopStreamRates } from "../lib/desktop-media";
import { connectDesktopRTC, sendGPUDesktopMessage, type DesktopRTCConnection } from "../lib/desktop-webrtc";
import { desktopOwnsKeyboard } from "../lib/desktop-input-focus";
import { DesktopCanvasInput } from "../lib/desktop-input";
import { isImeComposing } from "../lib/keyboard";
import { useDesktopClipboard } from "../lib/desktop-clipboard";
import { paintDesktopImage } from "../lib/desktop-frame";
import { DesktopH264Decoder } from "../lib/desktop-h264";
import {
  adaptiveDesktopSize,
  clampInt,
  desktopFrameLine,
  desktopFrameRates,
  errMessage,
  gpuDesktopFrameRate,
  gpuDesktopMaxVideoSize,
  inputBackendLabel,
  loadDesktopSettings,
  persistGpuDesktopSettings,
  sourceLabel,
  type ClipCaps,
  type ConnMode,
  type DesktopCaps,
  type DesktopDevice,
  type DesktopSettingsBase,
  type DesktopSource,
  type InputOption,
  type SelectOption,
  type StatusCls,
} from "../lib/desktop-model";
import {
  gpuKeyMessage,
  gpuPointerMessage as buildGpuPointerMessage,
  gpuPointerTypes as readGpuPointerTypes,
  gpuVideoContentRect as computeGpuVideoContentRect,
} from "../lib/desktop-gpu-input";
import { DisclosureBody } from "../components/disclosure-body";
import { Icon } from "../components/icon";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { useSearchParams } from "react-router-dom";
import { SplitPageShell, MobileSidebarTrigger } from "../components/split-layout";
import { DeviceDropTarget } from "../components/device-rail";
import { useI18n } from "../lib/i18n";
import {
  apiFetch,
  apiJSON,
  wsURL,
  getDevicePassword,
  rememberDevicePassword,
  forgetDevicePassword,
} from "../lib/api";
import { useServerCapabilities } from "../lib/server-capabilities";
import { cn } from "../lib/utils";
import { Button } from "../components/button";
import { Input } from "../components/input";
import { Switch } from "../components/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../components/select";
import { OsIcon } from "../components/os-icon";

/* Port of web/desktop.html + web/src/desktop.ts (vanilla remote desktop page).
 * Both stream modes are preserved: the legacy JPEG/H264 canvas stream over
 * /desktop and the GPU stream (MSE or WebCodecs) over /gpu-desktop/<id>/ws,
 * including input control, clipboard sync, screenshots and VNC sync.
 * localStorage keys (rdevDesktop.* / rdevGpuDesktop.*) are unchanged. */

const P = "rdevDesktop.";
const GP = "rdevGpuDesktop.";
const EMPTY_PARAMS = new URLSearchParams();

interface Settings extends DesktopSettingsBase {
  mode: string;
  maxWidth: string;
  maxHeight: string;
  gpuAdvancedOpen: boolean;
}

function loadSettings(): Settings {
  const mode = localStorage.getItem(P + "mode");
  const rates = desktopStreamRates(localStorage.getItem(P + "fps"), localStorage.getItem(P + "quality"));
  return {
    ...loadDesktopSettings(P, GP),
    mode: mode === "auto" || mode === "manual" ? mode : "auto",
    fps: String(rates.fps),
    quality: String(rates.quality),
    maxWidth: localStorage.getItem(P + "width") || "1600",
    maxHeight: localStorage.getItem(P + "height") || "1000",
    gpuAdvancedOpen: localStorage.getItem(GP + "advanced") === "true",
  };
}

/* ── Page ── */

/* One desktop session = one split leaf. Only the primary (first) pane syncs
 * ?device= to the URL and reuses the persisted device selection. */
function DesktopSession({
  primary,
  deviceRequest,
  onDeviceChange,
}: {
  primary: boolean;
  onDeviceChange?: (device: string) => void;
  /* Rail click/drop requests: {id, seq} identity change → connect. */
  deviceRequest?: { id: string; seq: number };
}) {
  const { devicePasswordInputsHidden } = useServerCapabilities();
  const { t } = useI18n();
  const [urlParams, setUrlParams] = useSearchParams();
  const searchParams = primary ? urlParams : EMPTY_PARAMS;

  const [settingsOpen, setSettingsOpen] = useState(false);
  const [settingsState, setSettingsState] = useState<Settings>(loadSettings);
  const [device, setDeviceState] = useState("");
  useEffect(() => { onDeviceChange?.(device); }, [device, onDeviceChange]);
  const [password, setPassword] = useState("");
  const [devices, setDevices] = useState<DesktopDevice[]>([]);
  const [status, setStatusState] = useState<{ text: string; cls: StatusCls }>({
    text: "",
    cls: "",
  });
  const [frameInfo, setFrameInfo] = useState("");
  const [gpuEncoderStatus, setGpuEncoderStatus] = useState("");
  const [clipStatus, setClipStatus] = useState("");
  const [clipCaps, setClipCapsState] = useState<ClipCaps | null>(null);
  const [isGpuDevice, setIsGpuDevice] = useState(false);
  const [sourceOptions, setSourceOptions] = useState<DesktopSource[]>([]);
  const [inputOptions, setInputOptions] = useState<SelectOption[]>([
    { value: "auto", label: "" },
  ]);
  const [inputDisabled, setInputDisabled] = useState(true);
  const [gpuSourceNames, setGpuSourceNamesState] = useState<string[] | null>(null);
  const [gpuEncoderOptions, setGpuEncoderOptions] = useState<SelectOption[]>([
    { value: "auto", label: "" },
  ]);
  const [gpuPointerOptions, setGpuPointerOptions] = useState<SelectOption[]>([
    { value: "auto", label: "" },
  ]);
  const [gpuKeyboardOptions, setGpuKeyboardOptions] = useState<SelectOption[]>([
    { value: "auto", label: "" },
  ]);
  const [connMode, setConnModeState] = useState<ConnMode>("");
  const [canvasShown, setCanvasShown] = useState(false);
  const [gpuDisplay, setGpuDisplay] = useState<"none" | "video" | "canvas">("none");
  const [gpuEmpty, setGpuEmpty] = useState<{ title: string; hint: string } | null>(null);
  const [controlActive, setControlActive] = useState(() => loadSettings().control);
  const [cursorHidden, setCursorHidden] = useState(() => loadSettings().showCursor);
  const [controlDisabled, setControlDisabled] = useState(false);
  const [gpuScaleOut, setGpuScaleOut] = useState("auto");
  const [gpuFrameRateOut, setGpuFrameRateOut] = useState("30");

  /* ── Refs (mutable connection state, mirrors of state the logic reads) ── */

  const settingsRef = useRef(settingsState);
  const deviceRef = useRef(device);
  const passwordRef = useRef(password);
  const devicesRef = useRef(devices);
  const gpuSourceNamesRef = useRef<string[] | null>(null);
  const gpuRTCSourceNamesRef = useRef<string[] | null>(null);
  const tRef = useRef(t);
  tRef.current = t;

  const stageRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const viewerRef = useRef<HTMLDivElement>(null);
  const videoRef = useRef<HTMLVideoElement>(null);
  const gpuCanvasRef = useRef<HTMLCanvasElement>(null);
  const overlayRef = useRef<HTMLDivElement>(null);

  const wsRef = useRef<WebSocket | null>(null);
  const rtcRef = useRef<DesktopRTCConnection | null>(null);
  const rtcSessionRef = useRef("");
  const rtcDispatchRef = useRef(false);
  const connSeqRef = useRef(0);
  const connModeRef = useRef<ConnMode>("");
  const manualDisconnectRef = useRef(false);
  const remoteInputRef = useRef(false);
  const lastCloseMsgRef = useRef("");
  const forcedFormatRef = useRef<string | null>(null);
  const controlPreferenceSet = useRef(localStorage.getItem(P + "control") !== null);

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

  /* ── State helpers (keep refs in sync, synchronously) ── */

  const updateSettings = (patch: Partial<Settings>) => {
    settingsRef.current = { ...settingsRef.current, ...patch };
    setSettingsState(settingsRef.current);
  };
  const settings = settingsState;
  const autoRates = desktopStreamRates(null, null, true);

  const setStatus = (text: string, cls: StatusCls = "") => setStatusState({ text, cls });

  const setConnMode = (mode: ConnMode) => {
    connModeRef.current = mode;
    setConnModeState(mode);
  };

  const setGpuSourceNames = (names: string[] | null) => {
    gpuSourceNamesRef.current = names;
    setGpuSourceNamesState(names);
  };

  /* ── Persistence (unchanged localStorage contract) ── */

  const saveDesktopSettings = () => {
    const s = settingsRef.current;
    if (deviceRef.current) localStorage.setItem(P + "device", deviceRef.current);
    localStorage.setItem(P + "mode", s.mode);
    localStorage.setItem(P + "source", s.source);
    localStorage.setItem(P + "inputBackend", s.inputBackend);
    localStorage.setItem(P + "fit", s.fit);
    localStorage.setItem(P + "control", String(s.control));
    localStorage.setItem(P + "showCursor", String(s.showCursor));
    localStorage.setItem(P + "clipboardSyncMode", s.clipboardMode);
    const rates = desktopStreamRates(s.fps, s.quality);
    localStorage.setItem(P + "fps", String(rates.fps));
    localStorage.setItem(P + "quality", String(rates.quality));
    localStorage.setItem(P + "width", String(clampInt(s.maxWidth, 320, 3840, 1600)));
    localStorage.setItem(P + "height", String(clampInt(s.maxHeight, 240, 2160, 1000)));
  };

  const saveGpuDesktopSettings = () => {
    const s = settingsRef.current;
    persistGpuDesktopSettings(GP, s);
    localStorage.setItem(GP + "advanced", String(s.gpuAdvancedOpen));
  };

  /* ── Derived device/stream parameters ── */

  const selectedDevice = (): DesktopDevice | undefined =>
    devicesRef.current.find((d) => d.id === deviceRef.current);

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

  const updateGpuRangeLabels = () => {
    const size = gpuMaxVideoSize();
    setGpuScaleOut(`${size.width}×${size.height}`);
    setGpuFrameRateOut(String(Math.round(gpuFrameRate())));
  };

  const gpuPointerTypes = () => readGpuPointerTypes(settingsRef.current);

  const adaptiveSize = () =>
    adaptiveDesktopSize(
      stageRef.current?.getBoundingClientRect(),
      window.innerWidth,
      window.innerHeight,
      window.devicePixelRatio,
    );

  const desktopOptions = () => {
    const s = settingsRef.current;
    const manual = s.mode === "manual";
    const adaptive = adaptiveSize();
    const videoCodecs = selectedDevice()?.desktop?.videoCodecs;
    return {
      mode: s.mode,
      source: s.source,
      ...desktopStreamRates(s.fps, s.quality, !manual),
      width: manual ? clampInt(s.maxWidth, 320, 3840, 1600) : adaptive.width,
      height: manual ? clampInt(s.maxHeight, 240, 2160, 1000) : adaptive.height,
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
    const d = selectedDevice() || ({} as DesktopDevice);
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
    if (canvas.width !== width || canvas.height !== height) {
      canvas.width = width;
      canvas.height = height;
    }
    setCanvasShown(true);
    canvas.getContext("2d")?.drawImage(image, 0, 0);
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
    // H.264 frames form one prediction chain. Feed every access unit in
    // arrival order; the latest-frame coalescing below is safe only for JPEG.
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
    ms.addEventListener(
      "sourceopen",
      () => {
        let mime = 'video/mp4; codecs="avc1.4D403D"';
        if (!MS.isTypeSupported(mime)) mime = "video/mp4";
        const sb = ms.addSourceBuffer(mime);
        // Sequence mode makes appends order-authoritative, which suits a
        // live stream where we keep chasing the newest fragment.
        try {
          sb.mode = "sequence";
        } catch {
          /* ignore */
        }
        gpuSourceBufferRef.current = sb;
        sb.addEventListener("updateend", gpuUpdateBuffer);
      },
      { once: true },
    );
  };

  const gpuStartWebCodecsVideo = (config: { codec?: string; width?: number; height?: number }) => {
    if (!("VideoDecoder" in window) || !("EncodedVideoChunk" in window)) {
      setStatus(tRef.current("desktop.gpu.webCodecsUnsupported"), "err");
      return;
    }
    closeGPUVideo();
    gpuVideoModeRef.current = "webcodecs";
    frameCountRef.current = 0;
    frameBytesRef.current = 0;
    statsStartedAtRef.current = performance.now();
    lastFrameAtRef.current = 0;
    setGpuDisplay("canvas");
    const gpuCanvas = gpuCanvasRef.current;
    const gpuCtx = gpuCanvas?.getContext("2d");
    if (gpuCanvas) {
      gpuCanvas.width = config.width || 320;
      gpuCanvas.height = config.height || 640;
    }
    const decoder = new VideoDecoder({
      output: (frame) => {
        try {
          if (gpuVideoDecoderRef.current !== decoder) return;
          if (gpuCanvas && gpuCtx) {
            gpuCanvas.width = frame.displayWidth || frame.codedWidth || gpuCanvas.width;
            gpuCanvas.height = frame.displayHeight || frame.codedHeight || gpuCanvas.height;
            gpuCtx.clearRect(0, 0, gpuCanvas.width, gpuCanvas.height);
            gpuCtx.drawImage(frame, 0, 0, gpuCanvas.width, gpuCanvas.height);
            setGpuEmpty(null);
          }
          gpuUpdateFrameInfo();
        } finally { frame.close(); }
      },
      error: (err) => {
        if (gpuVideoDecoderRef.current === decoder) setStatus(errMessage(err), "err");
      },
    });
    decoder.configure({
      codec: config.codec || "avc1.42E01F",
      codedWidth: config.width || 320,
      codedHeight: config.height || 640,
      optimizeForLatency: true,
      hardwareAcceleration: "prefer-hardware",
    });
    gpuVideoDecoderRef.current = decoder;
    gpuNeedKeyFrameRef.current = true;
    setStatus(tRef.current("index.desktopReady"), "ok");
  };

  const gpuHandleAndroidVideoPacket = (buffer: ArrayBuffer): boolean => {
    const view = new DataView(buffer);
    if (
      view.byteLength < 13 ||
      view.getUint8(0) !== 0x52 ||
      view.getUint8(1) !== 0x44 ||
      view.getUint8(2) !== 0x41 ||
      view.getUint8(3) !== 0x31
    )
      return false;
    const decoder = gpuVideoDecoderRef.current;
    if (!decoder || decoder.state !== "configured") return true;
    const isKey = view.getUint8(4) !== 0;
    if (gpuNeedKeyFrameRef.current && !isKey) return true;
    const timestamp = Number(view.getBigUint64(5));
    const data = new Uint8Array(buffer, 13);
    if (!isKey && decoder.decodeQueueSize > 2) {
      gpuNeedKeyFrameRef.current = true;
      return true;
    }
    frameCountRef.current++;
    frameBytesRef.current += data.byteLength;
    lastFrameAtRef.current = performance.now();
    try {
      decoder.decode(new EncodedVideoChunk({ type: isKey ? "key" : "delta", timestamp, data }));
      gpuNeedKeyFrameRef.current = false;
    } catch (err) {
      gpuNeedKeyFrameRef.current = true;
      setStatus(errMessage(err), "err");
    }
    return true;
  };

  /* ── GPU protocol ── */

  const gpuSend = (message: unknown) => {
    let rtcSource: number | undefined;
    if (rtcRef.current?.dataChannel.readyState === "open" && message && typeof message === "object" && "Config" in message) {
      const names = gpuRTCSourceNamesRef.current;
      // Wait for the fresh RTC list rather than applying a relay-local index.
      if (!names) return;
      const name = gpuSourceNamesRef.current?.[Number(settingsRef.current.gpuSource)];
      const index = name ? names.indexOf(name) : -1;
      if (!name || index < 0 || names.lastIndexOf(name) !== index || gpuSourceNamesRef.current?.filter(value => value === name).length !== 1) {
        rtcRef.current.close(); // Missing/ambiguous identity safely retains the relay.
      } else rtcSource = index;
    }
    sendGPUDesktopMessage(wsRef.current, rtcRef.current, message, rtcSource);
  };

  const gpuSendConfig = () => {
    const s = settingsRef.current;
    if (connModeRef.current !== "gpu" || !s.gpuSource) return;
    saveGpuDesktopSettings();
    updateGpuRangeLabels();
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
    gpuSend(s.gpuEnableVideo ? "ResumeVideo" : "PauseVideo");
  };

  const gpuSetSources = (list: string[]) => {
    const saved = localStorage.getItem(GP + "source") || settingsRef.current.gpuSource || "";
    const names = gpuSourceNamesRef.current;
    const currentIdx = Number(settingsRef.current.gpuSource);
    const previousName = names && Number.isInteger(currentIdx) ? names[currentIdx] || "" : "";
    if (!list || list.length === 0) {
      setGpuSourceNames([]);
      setGpuEmpty({
        title: tRef.current("desktop.gpu.noSources"),
        hint: tRef.current("desktop.gpu.noSourcesHint"),
      });
      setStatus(tRef.current("desktop.gpu.noDesktopSource"), "err");
      return;
    }
    setGpuSourceNames(list);
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

  const setSelectOptions = (
    options: SelectOption[] | undefined,
    current: string,
    apply: (list: SelectOption[], value: string) => void,
  ) => {
    const list = (options || []).filter((o) => o && o.value && o.value !== "auto");
    const withAuto: SelectOption[] = [
      { value: "auto", label: tRef.current("desktop.gpu.auto") },
      ...list,
    ];
    const value = withAuto.some((o) => o.value === current) ? current : "auto";
    apply(withAuto, value);
  };

  const gpuSetEncoders = (options: SelectOption[]) => {
    const current =
      localStorage.getItem(GP + "encoder") || settingsRef.current.gpuEncoder || "auto";
    setSelectOptions(options, current, (list, value) => {
      setGpuEncoderOptions(list);
      updateSettings({ gpuEncoder: value });
    });
  };

  const gpuSetInputCapabilities = (capabilities: {
    pointerOptions?: SelectOption[];
    keyboardOptions?: SelectOption[];
    options?: SelectOption[];
  }) => {
    setSelectOptions(
      capabilities.pointerOptions || capabilities.options || [],
      localStorage.getItem(GP + "pointerBackend") || settingsRef.current.gpuPointerBackend || "auto",
      (list, value) => {
        setGpuPointerOptions(list);
        updateSettings({ gpuPointerBackend: value });
      },
    );
    setSelectOptions(
      capabilities.keyboardOptions || capabilities.options || [],
      localStorage.getItem(GP + "keyboardBackend") || settingsRef.current.gpuKeyboardBackend || "auto",
      (list, value) => {
        setGpuKeyboardOptions(list);
        updateSettings({ gpuKeyboardBackend: value });
      },
    );
  };

  /* ── Clipboard sync ── */

  const clipboard = useDesktopClipboard({
    active: () => true,
    mode: () => settingsRef.current.clipboardMode,
    connMode: () => connModeRef.current,
    socket: () => wsRef.current,
    send: (message) => wsRef.current?.send(JSON.stringify(message)),
    gpuSend,
    setClipStatus,
    setCaps: setClipCapsState,
    t: (key) => tRef.current(key),
  });
  const pollClipboardSync = clipboard.poll;
  const startClipboardSync = clipboard.start;
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
        connModeRef.current === mode && dev === deviceRef.current
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
    rtcSessionRef.current = "";
    gpuRTCSourceNamesRef.current = null;
    rtcRef.current?.close();
    rtcRef.current = null;
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
    const ws = new WebSocket(gpuURL(dev));
    wsRef.current = ws;
    const connID = reconnecting ? connSeqRef.current : ++connSeqRef.current;
    lastCloseMsgRef.current = "";
    ws.binaryType = "arraybuffer";
    setStatus(tt("common.connecting"));
    ws.onopen = async () => {
      if (connID !== connSeqRef.current || wsRef.current !== ws) return;
      desktopReconnectDelayRef.current = 1000;
      setStatus(tRef.current("desktop.starting"));
      gpuSend("GetCapturableList");
      // CapturableList initializes this local session before Config selects a source.
      if (reconnecting) return; // Failed RTC stays on the relay until an explicit reconnect.
      const rtcSession = `gpu-${connID}-${crypto.randomUUID()}`;
      rtcSessionRef.current = rtcSession;
      let rtcWasOpen = false;
      const rtc: DesktopRTCConnection | null = await connectDesktopRTC({
        device: dev,
        session: rtcSession,
        password: passwordRef.current,
        supported: selectedDevice()?.desktop?.rtc === true,
        gpuFraming: true,
        isCurrent: () => connID === connSeqRef.current && wsRef.current === ws && ws.readyState === WebSocket.OPEN,
        onMessage: (event) => {
          if (rtc && rtcRef.current === rtc && connID === connSeqRef.current) {
            rtcDispatchRef.current = true;
            try {
              ws.onmessage?.(event);
            } finally {
              rtcDispatchRef.current = false;
            }
          }
        },
        onState: (state) => {
          if (!rtc || rtcRef.current !== rtc || connID !== connSeqRef.current || wsRef.current !== ws) return;
          if (state === "open") {
            rtcWasOpen = true;
            gpuRTCSourceNamesRef.current = null;
            // The RTC bridge has its own fresh local /ws session, not the relay's encoder.
            gpuReleaseKeyboard();
            closeGPUVideo();
            ws.send(JSON.stringify("PauseVideo"));
            rtc.send(JSON.stringify("GetCapturableList"));
          } else if (state === "failed" || state === "closed") {
            if (rtcRef.current !== rtc) return;
            rtcRef.current = null;
            rtcSessionRef.current = "";
            gpuRTCSourceNamesRef.current = null;
            rtc.close();
            if (rtcWasOpen && !manualDisconnectRef.current && connID === connSeqRef.current) {
              closeGPUVideo();
              gpuSendConfig();
            }
          }
        },
      });
      if (connID === connSeqRef.current && wsRef.current === ws) rtcRef.current = rtc;
      else rtc?.close();
    };
    ws.onerror = () => {
      if (connID !== connSeqRef.current || wsRef.current !== ws) return;
      setStatus(tRef.current("common.error"), "err");
    };
    ws.onclose = () => {
      if (connID !== connSeqRef.current || wsRef.current !== ws) return;
      gpuHeldKeysRef.current.clear();
      const rtc = rtcRef.current;
      rtcRef.current = null;
      rtcSessionRef.current = "";
      rtc?.close();
      wsRef.current = null;
      stopClipboardSync();
      setGpuEncoderStatus("");
      setClipboardCapabilities(null);
      setStatus(
        lastCloseMsgRef.current || tRef.current("desktop.closed"),
        lastCloseMsgRef.current ? "err" : "warn",
      );
      scheduleDesktopReconnect(dev, connID);
    };
    ws.onmessage = (evt) => {
      if (connID !== connSeqRef.current || wsRef.current !== ws) return;
      const relayDuringRTC = rtcRef.current?.dataChannel.readyState === "open" && !rtcDispatchRef.current;
      if (evt.data instanceof ArrayBuffer && relayDuringRTC) return;
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
      if (relayDuringRTC && (msg === "NewVideo" || msg?.AndroidVideoConfig || msg?.RuntimeStatus)) return;
      if (rtcDispatchRef.current && (msg?.InputCapabilities || msg?.ClipboardCapabilities || msg?.ClipboardContent)) return;
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
      } else if (msg && msg.CapturableList) {
        if (rtcDispatchRef.current) {
          if (!Array.isArray(msg.CapturableList) || !msg.CapturableList.every((name: unknown) => typeof name === "string")) {
            rtcRef.current?.close();
            return;
          }
          gpuRTCSourceNamesRef.current = msg.CapturableList;
          gpuSendConfig();
        } else gpuSetSources(msg.CapturableList);
      }
      else if (msg && msg.EncoderCapabilities) gpuSetEncoders(msg.EncoderCapabilities.options || []);
      else if (msg && msg.InputCapabilities) gpuSetInputCapabilities(msg.InputCapabilities);
      else if (msg && msg.ClipboardCapabilities) setClipboardCapabilities(msg.ClipboardCapabilities);
      else if (msg && msg.ClipboardContent) handleRemoteClipboard(msg.ClipboardContent);
      else if (msg && msg.RuntimeStatus) {
        const encoder = desktopRuntimeEncoderStatus(msg.RuntimeStatus, tt);
        if (encoder !== undefined) setGpuEncoderStatus(encoder);
      }
      else if (msg && msg.ConfigError) {
        if (rtcDispatchRef.current) {
          rtcRef.current?.close();
          return;
        }
        lastCloseMsgRef.current = msg.ConfigError;
        setStatus(msg.ConfigError, "err");
      } else if (msg && msg.Error) {
        if (String(msg.Error).startsWith("clipboard:")) {
          clipboard.failRemote(msg.Error);
        } else {
          if (rtcDispatchRef.current) {
            rtcRef.current?.close();
            return;
          }
          lastCloseMsgRef.current = msg.Error;
          setStatus(msg.Error, "err");
        }
      }
    };
  }

  async function connect(resetFormat = true, retry = false) {
    const retryDelay = desktopReconnectDelayRef.current;
    disconnect();
    if (retry) desktopReconnectDelayRef.current = retryDelay;
    if (resetFormat) forcedFormatRef.current = null;
    manualDisconnectRef.current = false;
    const dev = deviceRef.current;
    if (!dev) {
      setStatus(tRef.current("common.noDevices"), "err");
      return;
    }
    const sel = devicesRef.current.find((d) => d.id === dev);
    if (sel?.gpuDesktop) {
      connectGPUDesktop(dev);
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
    ws.onmessage = async (evt) => {
      if (connID !== connSeqRef.current || wsRef.current !== ws) return;
      if (evt.data instanceof ArrayBuffer) {
        if (rtcRef.current?.dataChannel.readyState === "open") return;
        drawFrame(evt.data);
        return;
      }
      const msg = JSON.parse(evt.data);
      const tt = tRef.current;
      if (msg.op === "auth") {
        setStatus(msg.message || tt("desktop.authRequired"), "warn");
        const saved = getDevicePassword(dev);
        if (saved && !passwordRef.current) {
          passwordRef.current = saved;
          setPassword(saved);
        }
        if (passwordRef.current)
          ws.send(JSON.stringify({ op: "auth", password: passwordRef.current, ...desktopOptions() }));
      } else if (msg.op === "auth_ok") {
        if (passwordRef.current) rememberDevicePassword(dev, passwordRef.current);
        setStatus(tt("desktop.starting"));
        if (typeof msg.session === "string" && msg.session) {
          rtcRef.current?.close();
          rtcSessionRef.current = msg.session;
          const session = msg.session;
          const rtc: DesktopRTCConnection | null = await connectDesktopRTC({
            device: dev,
            session,
            password: passwordRef.current,
            supported: sel?.desktop?.rtc === true,
            isCurrent: () => connID === connSeqRef.current && wsRef.current === ws && rtcSessionRef.current === session,
            onMessage: (event) => {
              if (!rtc || rtcRef.current !== rtc || rtcSessionRef.current !== session) return;
              if (event.data instanceof ArrayBuffer) drawFrame(event.data);
            },
            onState: (state) => {
              if (rtc && (state === "failed" || state === "closed") && rtcRef.current === rtc) {
                rtcRef.current = null;
                rtcSessionRef.current = "";
                rtc.close();
              }
            },
          });
          if (connID === connSeqRef.current && wsRef.current === ws && rtcSessionRef.current === session) rtcRef.current = rtc;
          else rtc?.close();
        }
      } else if (msg.op === "auth_fail") {
        retryAfterClose = false;
        forgetDevicePassword(dev);
        setStatus(msg.message || tt("common.passwordWrong"), "err");
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
      rtcRef.current?.close();
      rtcRef.current = null;
      rtcSessionRef.current = "";
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
    if (!wsRef.current) return;
    clearTimeout(resizeTimerRef.current);
    resizeTimerRef.current = window.setTimeout(() => {
      if (!wsRef.current) return;
      if (connModeRef.current === "gpu") {
        const next = gpuMaxVideoSize();
        const last = lastAdaptiveSizeRef.current;
        if (last && Math.abs(next.width - last.width) < 64 && Math.abs(next.height - last.height) < 64)
          return;
        lastAdaptiveSizeRef.current = next;
        gpuSendConfig();
        return;
      }
      if (settingsRef.current.mode !== "auto") return;
      const next = adaptiveSize();
      const last = lastAdaptiveSizeRef.current;
      if (last && Math.abs(next.width - last.width) < 64 && Math.abs(next.height - last.height) < 64)
        return;
      lastAdaptiveSizeRef.current = next;
      connect(false);
    }, 300);
  };

  /* ── Actions ── */

  const syncVNCSettings = async () => {
    if (!deviceRef.current) {
      setStatus(tRef.current("desktop.selectDeviceFirst"), "warn");
      return;
    }
    saveDesktopSettings();
    const opts = desktopOptions();
    try {
      const r = await apiFetch("/api/vnc/settings", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ device: deviceRef.current, ...opts }),
      });
      const data = await r.json().catch(() => ({}));
      if (!r.ok || !data.ok) throw new Error(data.message || data.error || `HTTP ${r.status}`);
      const suffix = data.closedSessions
        ? tRef.current("desktop.vncReconnectSuffix", { n: data.closedSessions })
        : "";
      setStatus(
        data.vncAddr
          ? `${tRef.current("desktop.vncSynced")}: ${data.vncAddr}${suffix}`
          : `${tRef.current("desktop.vncSynced")}${suffix}`,
        "ok",
      );
    } catch (e) {
      setStatus(errMessage(e), "err");
    }
  };

  const saveCanvasBlob = (targetCanvas: HTMLCanvasElement) => {
    targetCanvas.toBlob((blob) => {
      if (!blob) return;
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `rdev-${deviceRef.current || "desktop"}-${new Date()
        .toISOString()
        .replace(/[:.]/g, "-")}.png`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    }, "image/png");
  };

  const saveScreenshot = () => {
    const gpuCanvas = gpuCanvasRef.current;
    const video = videoRef.current;
    const canvas = canvasRef.current;
    if (
      connModeRef.current === "gpu" &&
      gpuVideoModeRef.current === "webcodecs" &&
      gpuCanvas?.width &&
      gpuCanvas?.height
    ) {
      saveCanvasBlob(gpuCanvas);
      return;
    }
    if (connModeRef.current === "gpu" && video?.videoWidth && video?.videoHeight) {
      const shot = document.createElement("canvas");
      shot.width = video.videoWidth;
      shot.height = video.videoHeight;
      shot.getContext("2d")?.drawImage(video, 0, 0, shot.width, shot.height);
      saveCanvasBlob(shot);
      return;
    }
    if (!canvas || !canvas.width || !canvas.height || !canvasShown) return;
    saveCanvasBlob(canvas);
  };

  const toggleFullscreen = () => {
    if (document.fullscreenElement) {
      if (document.exitFullscreen) void document.exitFullscreen();
      return;
    }
    if (stageRef.current?.requestFullscreen) void stageRef.current.requestFullscreen();
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
    connModeRef.current === "gpu" && settingsRef.current.control && remoteInputRef.current;

  /* ── Keep latest logic reachable from stable native listeners ── */

  const apiRef = useRef({
    connect,
    disconnect,
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
    updateGpuRangeLabels,
  });
  apiRef.current = {
    connect,
    disconnect,
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
    updateGpuRangeLabels,
  };

  /* ── Canvas/overlay listeners: elements unmount with connMode, so re-attach. ── */

  useEffect(() => {
    const canvas = canvasRef.current;
    const overlay = overlayRef.current;
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

    if (canvas) cleanups.push(desktopInput.attach(canvas, {
      socket: () => wsRef.current,
      mode: () => {
        if (connModeRef.current !== "desktop") return null;
        if (settingsRef.current.control && remoteInputRef.current) return "control";
        return settingsRef.current.showCursor ? "cursor" : null;
      },
      error: (error) => setStatus(errMessage(error), "err"),
    }));

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
          if (!apiRef.current.gpuInputAllowed()) return;
          if (!apiRef.current.gpuPointerTypes().includes(e.pointerType || "mouse")) return;
          if (type === "pointerdown") overlay.focus({ preventScroll: true });
          if (type === "pointerdown" && overlay.setPointerCapture)
            overlay.setPointerCapture(e.pointerId);
          const events =
            type === "pointermove" && typeof e.getCoalescedEvents === "function"
              ? e.getCoalescedEvents()
              : [e];
          for (const ev of events) apiRef.current.gpuSend(apiRef.current.gpuPointerMessage(ev, type));
          e.preventDefault();
          e.stopPropagation();
        });
      }
      on(
        overlay,
        "wheel",
        (e: WheelEvent) => {
          if (!apiRef.current.gpuInputAllowed() || !settingsRef.current.gpuEnableMouse) return;
          let scale = 1;
          if (e.deltaMode === 1) scale = 10;
          else if (e.deltaMode === 2) scale = 1000;
          apiRef.current.gpuSend({
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
        apiRef.current.gpuReleaseKeyboard();
        gpuComposingRef.current = false;
        gpuIgnoreNextTextInputRef.current = false;
      });
      on(overlay, "keydown", (e: KeyboardEvent) => {
        if (!apiRef.current.gpuInputAllowed() || wsRef.current?.readyState !== WebSocket.OPEN) return;
        if (gpuComposingRef.current || e.isComposing || e.key === "Process" || e.keyCode === 229) {
          e.preventDefault();
          e.stopPropagation();
          return;
        }
        const identity = `${e.code || e.key}:${e.location}`;
        const eventType = e.repeat && gpuHeldKeysRef.current.has(identity) ? "repeat" : "down";
        const msg = apiRef.current.gpuKeyMessage(e, eventType);
        apiRef.current.gpuSend(msg);
        if (eventType === "down") gpuHeldKeysRef.current.set(identity, msg.KeyboardEvent);
        e.preventDefault();
        e.stopPropagation();
      });
      on(overlay, "keyup", (e: KeyboardEvent) => {
        const identity = `${e.code || e.key}:${e.location}`;
        if (!gpuHeldKeysRef.current.has(identity)) return;
        const msg = apiRef.current.gpuKeyMessage(e, "up");
        apiRef.current.gpuSend(msg);
        gpuHeldKeysRef.current.delete(identity);
        e.preventDefault();
        e.stopPropagation();
      });
    }

    return () => {
      for (const fn of cleanups) fn();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connMode]);

  /* ── Window-level listeners + ResizeObserver (mount once) ── */

  useEffect(() => {
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
    const cleanups = [
      on(window, "focus", () => apiRef.current.pollClipboardSync(), true),
      on(window, "blur", () => {
        apiRef.current.desktopReleaseKeyboard();
        apiRef.current.gpuReleaseKeyboard();
      }),
      on(
        document,
        "visibilitychange",
        () => {
          if (document.hidden) {
            apiRef.current.gpuReleaseKeyboard();
            apiRef.current.desktopReleaseKeyboard();
            if (connModeRef.current === "gpu") apiRef.current.gpuSend("PauseVideo");
            return;
          }
          apiRef.current.pollClipboardSync();
          if (connModeRef.current === "gpu" && settingsRef.current.gpuEnableVideo)
            apiRef.current.gpuSend("ResumeVideo");
        },
        true,
      ),
      on(
        window,
        "beforeinput",
        (e: InputEvent) => {
          if (!apiRef.current.gpuInputAllowed() || !desktopOwnsKeyboard(overlayRef.current)) return;
          if (
            e.target instanceof HTMLInputElement ||
            e.target instanceof HTMLTextAreaElement ||
            e.target instanceof HTMLSelectElement
          )
            return;
          if (
            e.inputType === "insertCompositionText" ||
            (e.inputType === "insertText" && gpuIgnoreNextTextInputRef.current)
          ) {
            gpuIgnoreNextTextInputRef.current = false;
            e.preventDefault();
            e.stopPropagation();
            return;
          }
          if (e.inputType !== "insertText" && e.inputType !== "insertLineBreak") return;
          apiRef.current.gpuTextInput(e.inputType === "insertLineBreak" ? "\n" : e.data);
          e.preventDefault();
          e.stopPropagation();
        },
        true,
      ),
      on(
        window,
        "paste",
        (e: ClipboardEvent) => {
          if (!apiRef.current.gpuInputAllowed() || !desktopOwnsKeyboard(overlayRef.current)) return;
          if (
            e.target instanceof HTMLInputElement ||
            e.target instanceof HTMLTextAreaElement ||
            e.target instanceof HTMLSelectElement
          )
            return;
          const text = e.clipboardData?.getData("text") || "";
          apiRef.current.gpuTextInput(text);
          e.preventDefault();
          e.stopPropagation();
        },
        true,
      ),
      on(
        window,
        "compositionstart",
        (e: CompositionEvent) => {
          if (!apiRef.current.gpuInputAllowed() || !desktopOwnsKeyboard(overlayRef.current)) return;
          if (
            e.target instanceof HTMLInputElement ||
            e.target instanceof HTMLTextAreaElement ||
            e.target instanceof HTMLSelectElement
          )
            return;
          gpuComposingRef.current = true;
          e.preventDefault();
          e.stopPropagation();
        },
        true,
      ),
      on(
        window,
        "compositionend",
        (e: CompositionEvent) => {
          if (!apiRef.current.gpuInputAllowed() || !desktopOwnsKeyboard(overlayRef.current)) return;
          if (
            e.target instanceof HTMLInputElement ||
            e.target instanceof HTMLTextAreaElement ||
            e.target instanceof HTMLSelectElement
          )
            return;
          gpuComposingRef.current = false;
          gpuIgnoreNextTextInputRef.current = true;
          apiRef.current.gpuTextInput(e.data || "");
          setTimeout(() => {
            gpuIgnoreNextTextInputRef.current = false;
          }, 0);
          e.preventDefault();
          e.stopPropagation();
        },
        true,
      ),
      on(window, "beforeunload", () => apiRef.current.disconnect()),
    ];

    const stage = stageRef.current;
    const observer = new ResizeObserver(() => apiRef.current.scheduleAdaptiveReconnect());
    if (stage) observer.observe(stage);
    apiRef.current.updateGpuRangeLabels();

    return () => {
      for (const fn of cleanups) fn();
      observer.disconnect();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  /* ── Device loading + auto-connect (vanilla loadDevices) ── */

  useEffect(() => {
    let cancelled = false;
    apiJSON<DesktopDevice[]>("/api/devices")
      .then((list) => {
        if (cancelled) return;
        devicesRef.current = list;
        setDevices(list);
        const selected =
          searchParams.get("device") ||
          (primary ? localStorage.getItem(P + "device") : "") ||
          "";
        const chosen = list.some((d) => d.id === selected) ? selected : "";
        if (chosen) {
          deviceRef.current = chosen;
          setDeviceState(chosen);
        }
        updateSourceOptions();
        if (chosen) connect();
      })
      .catch((e) => setStatus(errMessage(e), "err"));
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  /* Unmount: close everything. */
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

  const setDevice = (id: string) => {
    deviceRef.current = id;
    setDeviceState(id);
    if (primary) {
      setUrlParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (id) next.set("device", id);
          else next.delete("device");
          return next;
        },
        { replace: true },
      );
    }
    updateSourceOptions();
    saveDesktopSettings();
    scheduleReconnectIfActive();
  };

  /* Rail pick/drop → connect this pane to the requested device. */
  useEffect(() => {
    if (!deviceRequest) return;
    setDevice(deviceRequest.id);
    connect();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [deviceRequest]);

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

  /* ── Render ── */

  return (
    <div className="page-workspace overflow-hidden">
      {/* Controls bar */}
      <div className="page-toolbar">
        <MobileSidebarTrigger />
        {/* Device comes from the left rail; the bar only shows it. */}
        <div className="flex h-8 items-center gap-1.5 rounded-md border border-border-seam px-2.5 text-xs text-muted-foreground select-none">
          <OsIcon
            os={devices.find((d) => d.id === device)?.os}
            className="shrink-0"
          />
          <span className="max-w-48 truncate">
            {device || t("terminal.devicePlaceholder")}
          </span>
        </div>
        {!devicePasswordInputsHidden && <Input
          type="password"
          aria-label={t("common.password")}
          autoComplete="current-password"
          value={password}
          onChange={(e) => {
            passwordRef.current = e.target.value;
            setPassword(e.target.value);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !isImeComposing(e.nativeEvent)) connect();
          }}
          placeholder={t("terminal.passwordPlaceholder")}
          className="h-8 min-w-24 w-36 flex-1 sm:flex-none text-xs"
        />}
        <Button size="sm" disabled={!device} onClick={() => connect()}>
          {t("common.connect")}
        </Button>
        <Button size="sm" variant="outline" disabled={!connMode} onClick={() => disconnect()}>
          {t("desktop.disconnect")}
        </Button>
        <Button size="sm" variant="ghost" aria-expanded={settingsOpen} onClick={() => setSettingsOpen((open) => !open)}>
          <Icon name="Settings" />{t("desktop.settings")}
        </Button>
        <span
          role="status"
          className={cn(
            "break-words text-xs",
            status.cls === "ok" && "text-success",
            status.cls === "warn" && "text-attention",
            status.cls === "err" && "text-destructive",
            !status.cls && "text-muted-foreground",
          )}
        >
          {status.text || t("desktop.idle")}
          {connMode === "gpu" && status.cls === "ok" && settings.gpuEnableVideo && gpuEncoderStatus ? ` · ${gpuEncoderStatus}` : ""}
        </span>
      </div>

      {/* Settings remain mounted so toggling the panel never resets a connection. */}
      <DisclosureBody open={settingsOpen} forceMount className="shrink-0">
      <div className="flex max-h-[35dvh] flex-wrap items-center gap-x-3 gap-y-2 overflow-auto border-b border-border-seam bg-sidebar px-3 py-3 text-xs text-muted-foreground">
        {!isGpuDevice && (
          <>
            <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
              <span>{t("desktop.mode")}</span>
              <Select
                value={settings.mode}
                onValueChange={(v) => {
                  updateSettings({ mode: v });
                  saveDesktopSettings();
                  scheduleReconnectIfActive();
                }}
              >
                <SelectTrigger aria-label={t("desktop.mode")} className="h-7 w-auto text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="auto">{t("desktop.modeAuto")}</SelectItem>
                  <SelectItem value="manual">{t("desktop.modeManual")}</SelectItem>
                </SelectContent>
              </Select>
            </label>
            <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
              <span>{t("desktop.source")}</span>
              <Select
                value={settings.source}
                onValueChange={(v) => {
                  updateSettings({ source: v });
                  saveDesktopSettings();
                  scheduleReconnectIfActive();
                }}
              >
                <SelectTrigger aria-label={t("desktop.source")} className="h-7 w-auto max-w-48 text-xs">
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
            </label>
            <fieldset
              disabled={settings.mode !== "manual"}
              aria-label={t("desktop.modeManual")}
              className={cn(
                "flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2",
                settings.mode !== "manual" && "opacity-45",
              )}
            >
              <label className="inline-flex items-center gap-1 whitespace-nowrap">
                <span>{t("desktop.fps")}</span>
                <Input
                  type="number"
                  min={1}
                  max={30}
                  value={settings.mode === "manual" ? settings.fps : autoRates.fps}
                  onChange={(e) => updateSettings({ fps: e.target.value })}
                  onBlur={() => {
                    updateSettings({ fps: String(desktopStreamRates(settings.fps, settings.quality).fps) });
                    saveDesktopSettings();
                    scheduleReconnectIfActive();
                  }}
                  className="h-7 w-16 px-2 text-xs"
                />
              </label>
              <label className="inline-flex items-center gap-1 whitespace-nowrap">
                <span>{t("desktop.quality")}</span>
                <Input
                  type="number"
                  min={25}
                  max={90}
                  value={settings.mode === "manual" ? settings.quality : autoRates.quality}
                  onChange={(e) => updateSettings({ quality: e.target.value })}
                  onBlur={() => {
                    updateSettings({ quality: String(desktopStreamRates(settings.fps, settings.quality).quality) });
                    saveDesktopSettings();
                    scheduleReconnectIfActive();
                  }}
                  className="h-7 w-16 px-2 text-xs"
                />
              </label>
              <label className="inline-flex items-center gap-1 whitespace-nowrap">
                <span>{t("desktop.maxWidth")}</span>
                <Input
                  type="number"
                  min={320}
                  max={3840}
                  value={settings.maxWidth}
                  onChange={(e) => updateSettings({ maxWidth: e.target.value })}
                  onBlur={() => {
                    saveDesktopSettings();
                    scheduleReconnectIfActive();
                  }}
                  className="h-7 w-20 px-2 text-xs"
                />
              </label>
              <label className="inline-flex items-center gap-1 whitespace-nowrap">
                <span>{t("desktop.maxHeight")}</span>
                <Input
                  type="number"
                  min={240}
                  max={2160}
                  value={settings.maxHeight}
                  onChange={(e) => updateSettings({ maxHeight: e.target.value })}
                  onBlur={() => {
                    saveDesktopSettings();
                    scheduleReconnectIfActive();
                  }}
                  className="h-7 w-20 px-2 text-xs"
                />
              </label>
            </fieldset>
          </>
        )}

        {isGpuDevice && (
          <>
            <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
              <span>{t("desktop.source")}</span>
              <Select
                value={settings.gpuSource}
                onValueChange={(v) => {
                  updateSettings({ gpuSource: v });
                  gpuSendConfig();
                }}
              >
                <SelectTrigger aria-label={t("desktop.source")} className="h-7 w-auto max-w-48 text-xs">
                  <SelectValue placeholder={t("desktop.gpu.loadingSources")} />
                </SelectTrigger>
                <SelectContent>
                  {gpuSourceNames === null ? (
                    <SelectItem value="__loading" disabled>
                      {t("desktop.gpu.loadingSources")}
                    </SelectItem>
                  ) : gpuSourceNames.length === 0 ? (
                    <SelectItem value="__none" disabled>
                      {t("desktop.gpu.noSourcesOption")}
                    </SelectItem>
                  ) : (
                    gpuSourceNames.map((name, index) => (
                      <SelectItem key={index} value={String(index)}>
                        {name}
                      </SelectItem>
                    ))
                  )}
                </SelectContent>
              </Select>
            </label>
            <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
              <span>{t("desktop.gpu.video")}</span>
              <Select
                value={settings.gpuEncoder}
                onValueChange={(v) => {
                  updateSettings({ gpuEncoder: v });
                  gpuSendConfig();
                }}
              >
                <SelectTrigger aria-label={t("desktop.gpu.video")} className="h-7 w-auto text-xs">
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
            </label>
            <Button
              size="sm"
              variant="outline"
              className="h-7 text-xs"
              onClick={() => gpuSend("GetCapturableList")}
            >
              {t("desktop.gpu.refreshSources")}
            </Button>
            <Button
              size="sm"
              variant="outline"
              className="h-7 text-xs"
              aria-expanded={settings.gpuAdvancedOpen}
              onClick={() => {
                updateSettings({ gpuAdvancedOpen: !settingsRef.current.gpuAdvancedOpen });
                saveGpuDesktopSettings();
              }}
            >
              {t("desktop.gpu.advanced")}
            </Button>
          </>
        )}

        <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
          <Switch
            checked={settings.control}
            disabled={controlDisabled}
            onCheckedChange={onControlChange}
          />
          <span>{t("desktop.control")}</span>
        </label>
        <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
          <Switch checked={settings.showCursor} onCheckedChange={onShowCursorChange} />
          <span>{t("desktop.showCursor")}</span>
        </label>

        {!isGpuDevice && (
          <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
            <span>{t("desktop.inputBackend")}</span>
            <Select
              value={settings.inputBackend}
              disabled={inputDisabled}
              onValueChange={(v) => {
                updateSettings({ inputBackend: v });
                saveDesktopSettings();
                scheduleReconnectIfActive();
              }}
            >
              <SelectTrigger aria-label={t("desktop.inputBackend")} className="h-7 w-auto max-w-48 text-xs">
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
          </label>
        )}

        <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
          <span>{t("desktop.view")}</span>
          <Select
            value={settings.fit}
            onValueChange={(v) => {
              updateSettings({ fit: v });
              saveDesktopSettings();
            }}
          >
            <SelectTrigger aria-label={t("desktop.view")} className="h-7 w-auto text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="fit">{t("desktop.fit")}</SelectItem>
              <SelectItem value="actual">{t("desktop.actual")}</SelectItem>
            </SelectContent>
          </Select>
        </label>

        <Button size="sm" variant="outline" className="h-7 text-xs" onClick={syncVNCSettings}>
          {t("desktop.syncVNC")}
        </Button>
        <Button size="sm" variant="outline" className="h-7 text-xs" onClick={saveScreenshot}>
          {t("desktop.screenshot")}
        </Button>
        <Button size="sm" variant="outline" className="h-7 text-xs" onClick={toggleFullscreen}>
          {t("desktop.fullscreen")}
        </Button>

        <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
          <span>{t("desktop.clipboardSync")}</span>
          <Select
            value={settings.clipboardMode}
            disabled={!clipCaps}
            onValueChange={(v) => {
              updateSettings({ clipboardMode: v });
              saveDesktopSettings();
              startClipboardSync();
            }}
          >
            <SelectTrigger aria-label={t("desktop.clipboardSync")} className="h-7 w-auto max-w-48 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="off">{t("desktop.clipboardOff")}</SelectItem>
              <SelectItem value="remote-to-local">{t("desktop.clipboardRemoteToLocal")}</SelectItem>
              <SelectItem value="local-to-remote">{t("desktop.clipboardLocalToRemote")}</SelectItem>
              <SelectItem value="bidirectional">{t("desktop.clipboardBidirectional")}</SelectItem>
            </SelectContent>
          </Select>
        </label>
        {clipStatus && (
          <span className="text-xs text-muted-foreground" aria-live="polite">
            {clipStatus}
          </span>
        )}
        <div className="ml-auto flex items-center gap-2">
          {frameInfo && (
            <span className="text-xs text-muted-foreground">{frameInfo}</span>
          )}
        </div>

        {isGpuDevice && settings.gpuAdvancedOpen && (
          <div className="flex w-full flex-wrap items-center gap-x-3 gap-y-2 border-t border-border-seam pt-2">
            <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
              <Switch
                checked={settings.gpuEnableVideo}
                onCheckedChange={(v) => {
                  updateSettings({ gpuEnableVideo: v });
                  updateGpuRangeLabels();
                  saveGpuDesktopSettings();
                  gpuSendConfig();
                }}
              />
              <span>{t("desktop.gpu.video")}</span>
            </label>
            <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
              <Switch
                checked={settings.gpuLowLatency}
                onCheckedChange={(v) => {
                  updateSettings({ gpuLowLatency: v });
                  saveGpuDesktopSettings();
                  gpuSendConfig();
                }}
              />
              <span>{t("desktop.gpu.lowLatency")}</span>
            </label>
            <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
              <Switch
                checked={settings.gpuStretch}
                onCheckedChange={(v) => {
                  updateSettings({ gpuStretch: v });
                  saveGpuDesktopSettings();
                  gpuSendConfig();
                }}
              />
              <span>{t("desktop.gpu.stretch")}</span>
            </label>
            <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
              <Switch
                checked={settings.gpuEnableMouse}
                onCheckedChange={(v) => {
                  updateSettings({ gpuEnableMouse: v });
                  saveGpuDesktopSettings();
                  gpuSendConfig();
                }}
              />
              <span>{t("desktop.gpu.mouse")}</span>
            </label>
            <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
              <Switch
                checked={settings.gpuEnablePen}
                onCheckedChange={(v) => {
                  updateSettings({ gpuEnablePen: v });
                  saveGpuDesktopSettings();
                  gpuSendConfig();
                }}
              />
              <span>{t("desktop.gpu.pen")}</span>
            </label>
            <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
              <Switch
                checked={settings.gpuEnableTouch}
                onCheckedChange={(v) => {
                  updateSettings({ gpuEnableTouch: v });
                  saveGpuDesktopSettings();
                  gpuSendConfig();
                }}
              />
              <span>{t("desktop.gpu.touch")}</span>
            </label>
            <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
              <span>{t("desktop.gpu.resolution")}</span>
              <input
                type="range"
                min={0.1}
                max={2}
                step={0.01}
                value={settings.gpuScale}
                onChange={(e) => {
                  updateSettings({ gpuScale: e.target.value });
                  updateGpuRangeLabels();
                  saveGpuDesktopSettings();
                }}
                onPointerUp={gpuSendConfig}
                onKeyUp={gpuSendConfig}
                className="w-28 accent-primary"
              />
              <output className="min-w-10 text-foreground">{gpuScaleOut}</output>
            </label>
            <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
              <span>{t("desktop.gpu.frameRate")}</span>
              <input
                type="range"
                min={0}
                max={120}
                step={1}
                value={settings.gpuFrameRate}
                onChange={(e) => {
                  updateSettings({ gpuFrameRate: e.target.value });
                  updateGpuRangeLabels();
                  saveGpuDesktopSettings();
                }}
                onPointerUp={gpuSendConfig}
                onKeyUp={gpuSendConfig}
                className="w-28 accent-primary"
              />
              <output className="min-w-8 text-foreground">{gpuFrameRateOut}</output>
            </label>
            <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
              <span>{t("desktop.gpu.pointerInput")}</span>
              <Select
                value={settings.gpuPointerBackend}
                onValueChange={(v) => {
                  updateSettings({ gpuPointerBackend: v });
                  saveGpuDesktopSettings();
                  gpuSendConfig();
                }}
              >
                <SelectTrigger aria-label={t("desktop.gpu.pointerInput")} className="h-7 w-auto max-w-48 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {gpuPointerOptions.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label || (o.value === "auto" ? t("desktop.gpu.auto") : o.value)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </label>
            <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
              <span>{t("desktop.gpu.keyboardInput")}</span>
              <Select
                value={settings.gpuKeyboardBackend}
                onValueChange={(v) => {
                  updateSettings({ gpuKeyboardBackend: v });
                  saveGpuDesktopSettings();
                  gpuSendConfig();
                }}
              >
                <SelectTrigger aria-label={t("desktop.gpu.keyboardInput")} className="h-7 w-auto max-w-48 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {gpuKeyboardOptions.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label || (o.value === "auto" ? t("desktop.gpu.auto") : o.value)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </label>
            <label className="inline-flex items-center gap-1.5 whitespace-nowrap">
              <span>{t("desktop.gpu.minPressure")}</span>
              <input
                type="range"
                min={0}
                max={1}
                step={0.01}
                value={settings.gpuMinPressure}
                onChange={(e) => {
                  updateSettings({ gpuMinPressure: e.target.value });
                  saveGpuDesktopSettings();
                }}
                onPointerUp={gpuSendConfig}
                onKeyUp={gpuSendConfig}
                className="w-28 accent-primary"
              />
            </label>
          </div>
        )}
      </div>
      </DisclosureBody>

      {/* Stage */}
      <div
        ref={stageRef}
        className={cn(
          "flex min-h-0 min-w-0 flex-1 bg-surface-recessed p-2 sm:p-3 overscroll-contain",
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
            className="relative min-h-0 w-full flex-1 overflow-hidden rounded-lg border border-border-seam bg-black"
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
              aria-label={t("title.desktop")}
              className="absolute inset-0 touch-none outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
              style={{ cursor: controlActive ? "crosshair" : undefined }}
            />
            {gpuEmpty && (
              <div
                className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center p-6 text-center leading-loose text-muted-foreground"
                style={{
                  background:
                    "radial-gradient(circle at center, color-mix(in oklab, var(--timeline-accent) 10%, transparent), transparent 42%)",
                }}
              >
                <strong className="mb-1 text-base text-foreground">{gpuEmpty.title}</strong>
                <span>{gpuEmpty.hint}</span>
              </div>
            )}
          </div>
        )}
        {connMode !== "gpu" && !canvasShown && (
          <div className="text-center leading-loose text-muted-foreground">
            {t("desktop.empty")}
          </div>
        )}
      </div>
    </div>
  );
}

/* ── Single-pane wrapper: the page IS the rail + desktop columns. ── */

export function DesktopPage() {
  const [selectedDevice, setSelectedDevice] = useState("");
  const seqRef = useRef(0);
  const [requests, setRequests] = useState<
    Map<number, { id: string; seq: number }>
  >(new Map());

  const requestDevice = (leafId: number, deviceId: string) => {
    setRequests((prev) =>
      new Map(prev).set(leafId, { id: deviceId, seq: ++seqRef.current }),
    );
  };

  return (
    <SplitPageShell
      storageKey="rdev.split.desktop.rail"
      selectedDevice={selectedDevice}
      onDevicePick={(id) => requestDevice(0, id)}
    >
      <DeviceDropTarget
        className="h-full min-h-0"
        onDropDevice={(id) => requestDevice(0, id)}
      >
        <DesktopSession onDeviceChange={setSelectedDevice} primary deviceRequest={requests.get(0)} />
      </DeviceDropTarget>
    </SplitPageShell>
  );
}
