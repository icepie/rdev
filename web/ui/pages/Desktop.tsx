import { useEffect, useRef, useState, type ReactNode } from "react";
import { useSearchParams } from "react-router-dom";
import { SplitPageShell } from "../components/split-layout";
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

/* ── Types ── */

interface DesktopSource {
  id: string;
  label?: string;
  kind?: string;
  backend?: string;
  width?: number;
  height?: number;
  x?: number;
  y?: number;
}

interface InputOption {
  id?: string;
  label?: string;
  kinds?: string[];
  requires?: string[];
}

interface DesktopCaps {
  supported?: boolean;
  reason?: string;
  platform?: string;
  displayServer?: string;
  sources?: DesktopSource[];
  inputOptions?: InputOption[];
  inputBackends?: string[];
  videoCodecs?: string[];
  clipboard?: boolean;
  input?: boolean;
}

interface DesktopDevice {
  id: string;
  hasPassword?: boolean;
  os?: string;
  desktop?: DesktopCaps;
  gpuDesktop?: boolean;
}

interface ClipCaps {
  supported?: boolean;
  read?: boolean;
  write?: boolean;
  formats?: string[];
  maxBytes?: number;
}

interface ClipItem {
  mime: string;
  data: string;
}

interface SelectOption {
  value: string;
  label: string;
}

interface Settings {
  mode: string;
  source: string;
  fps: string;
  quality: string;
  maxWidth: string;
  maxHeight: string;
  inputBackend: string;
  fit: string;
  control: boolean;
  showCursor: boolean;
  clipboardMode: string;
  gpuSource: string;
  gpuEncoder: string;
  gpuAdvancedOpen: boolean;
  gpuEnableVideo: boolean;
  gpuLowLatency: boolean;
  gpuStretch: boolean;
  gpuEnableMouse: boolean;
  gpuEnablePen: boolean;
  gpuEnableTouch: boolean;
  gpuScale: string;
  gpuFrameRate: string;
  gpuPointerBackend: string;
  gpuKeyboardBackend: string;
  gpuMinPressure: string;
}

type ConnMode = "" | "desktop" | "gpu";
type StatusCls = "" | "ok" | "warn" | "err";

/* ── Pure helpers ── */

function clampInt(value: string | number, min: number, max: number, fallback: number): number {
  const n = parseInt(String(value), 10);
  if (!Number.isFinite(n)) return fallback;
  return Math.max(min, Math.min(max, n));
}

function errMessage(err: unknown): string {
  return err instanceof Error ? err.message || String(err) : String(err);
}

function formatBytes(bytes: number): string {
  if (bytes >= 1024 * 1024) return (bytes / 1024 / 1024).toFixed(1) + " MB";
  if (bytes >= 1024) return (bytes / 1024).toFixed(1) + " KB";
  return bytes + " B";
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  const chunk = 0x8000;
  for (let offset = 0; offset < bytes.length; offset += chunk)
    binary += String.fromCharCode(...bytes.subarray(offset, offset + chunk));
  return btoa(binary);
}

function base64ToBytes(value: string): Uint8Array {
  const binary = atob(value || "");
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index);
  return bytes;
}

function clipboardItems(event: { items?: ClipItem[]; text?: string }): ClipItem[] {
  const items =
    Array.isArray(event?.items) && event.items.length
      ? event.items
      : event?.text
        ? [{ mime: "text/plain", data: bytesToBase64(new TextEncoder().encode(event.text)) }]
        : [];
  return items.map((item) => ({
    mime: String(item.mime || "").split(";", 1)[0].toLowerCase(),
    data: item.data || "",
  }));
}

function clipboardFingerprint(items: ClipItem[]): string {
  return items.map((item) => `${item.mime}:${item.data}`).sort().join("|");
}

function inputBackendLabel(backend: InputOption | string): string {
  const id = typeof backend === "string" ? backend : backend.id || "";
  const label = (typeof backend === "string" ? backend : backend.label) || id;
  const kinds =
    typeof backend !== "string" && backend.kinds && backend.kinds.length
      ? ` · ${backend.kinds.join("/")}`
      : "";
  const requires =
    typeof backend !== "string" && backend.requires && backend.requires.length
      ? ` · ${backend.requires.join(", ")}`
      : "";
  if (id === "x11-xtest") return `${label}${kinds || " · mouse/keyboard"}${requires}`;
  if (id === "uinput") return `${label}${kinds || " · mouse/keyboard/touch/pen"}${requires}`;
  if (id === "win32") return `${label}${kinds || " · mouse/keyboard"}${requires}`;
  if (id === "win32-touch") return `${label}${kinds || " · mouse/keyboard/touch/pen"}${requires}`;
  return `${label}${kinds}${requires}`;
}

function sourceLabel(source: DesktopSource): string {
  const parts = [source.label || source.id];
  if (source.kind) parts.push(source.kind);
  if (source.backend) parts.push(source.backend);
  if (source.width && source.height)
    parts.push(`${source.width}×${source.height}${source.x || source.y ? `@${source.x},${source.y}` : ""}`);
  return parts.join(" · ");
}

function loadSettings(): Settings {
  const mode = localStorage.getItem(P + "mode");
  const fit = localStorage.getItem(P + "fit");
  const clipboardMode = localStorage.getItem(P + "clipboardSyncMode");
  return {
    mode: mode === "auto" || mode === "manual" ? mode : "auto",
    source: localStorage.getItem(P + "source") || "auto",
    fps: localStorage.getItem(P + "fps") || "4",
    quality: localStorage.getItem(P + "quality") || "50",
    maxWidth: localStorage.getItem(P + "width") || "1600",
    maxHeight: localStorage.getItem(P + "height") || "1000",
    inputBackend: localStorage.getItem(P + "inputBackend") || "auto",
    fit: fit === "fit" || fit === "actual" ? fit : "fit",
    control: localStorage.getItem(P + "control") === "true",
    showCursor: localStorage.getItem(P + "showCursor") === "true",
    /* Clipboard sync defaults off (matches the Agent desktop panel): it
     * polls the local clipboard in the background — opt-in only. */
    clipboardMode: ["off", "remote-to-local", "local-to-remote", "bidirectional"].includes(
      clipboardMode || "",
    )
      ? (clipboardMode as string)
      : "off",
    gpuSource: localStorage.getItem(GP + "source") || "",
    gpuEncoder: localStorage.getItem(GP + "encoder") || "auto",
    gpuAdvancedOpen: localStorage.getItem(GP + "advanced") === "true",
    gpuEnableVideo: localStorage.getItem(GP + "enableVideo") !== "false",
    gpuLowLatency: localStorage.getItem(GP + "lowLatency") !== "false",
    gpuStretch: localStorage.getItem(GP + "stretch") === "true",
    gpuEnableMouse: localStorage.getItem(GP + "enableMouse") !== "false",
    gpuEnablePen: localStorage.getItem(GP + "enablePen") !== "false",
    gpuEnableTouch: localStorage.getItem(GP + "enableTouch") !== "false",
    gpuScale: localStorage.getItem(GP + "scale") || "1.5",
    gpuFrameRate: localStorage.getItem(GP + "frameRateSlider") || "30",
    gpuPointerBackend: localStorage.getItem(GP + "pointerBackend") || "auto",
    gpuKeyboardBackend: localStorage.getItem(GP + "keyboardBackend") || "auto",
    gpuMinPressure: localStorage.getItem(GP + "minPressure") || "0",
  };
}

/* ── Page ── */

/* One desktop session = one split leaf. Only the primary (first) pane syncs
 * ?device= to the URL and reuses the persisted device selection. */
function DesktopSession({
  primary,
  deviceRequest,
}: {
  primary: boolean;
  /* Rail click/drop requests: {id, seq} identity change → connect. */
  deviceRequest?: { id: string; seq: number };
}) {
  const { t } = useI18n();
  const [urlParams, setUrlParams] = useSearchParams();
  const searchParams = primary ? urlParams : EMPTY_PARAMS;

  const [settingsState, setSettingsState] = useState<Settings>(loadSettings);
  const [device, setDeviceState] = useState("");
  const [password, setPassword] = useState("");
  const [devices, setDevices] = useState<DesktopDevice[]>([]);
  const [status, setStatusState] = useState<{ text: string; cls: StatusCls }>({
    text: "",
    cls: "",
  });
  const [frameInfo, setFrameInfo] = useState("");
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
  const clipCapsRef = useRef<ClipCaps | null>(null);
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

  const drawingRef = useRef(false);
  const pendingFrameRef = useRef<ArrayBuffer | null>(null);
  const reconnectTimerRef = useRef<number | undefined>(undefined);
  const resizeTimerRef = useRef<number | undefined>(undefined);
  const lastMouseSentRef = useRef(0);
  const lastAdaptiveSizeRef = useRef<{ width: number; height: number } | null>(null);
  const gpuReconnectDelayRef = useRef(1000);

  const frameCountRef = useRef(0);
  const frameBytesRef = useRef(0);
  const statsStartedAtRef = useRef(0);
  const lastFrameAtRef = useRef(0);
  const currentSourceRef = useRef("");

  const h264DecoderRef = useRef<VideoDecoder | null>(null);
  const h264FrameIndexRef = useRef(0);
  const h264FrameDurationUsRef = useRef(125000);
  const h264NeedKeyframeRef = useRef(false);
  const videoFormatRef = useRef("");

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

  const clipGenRef = useRef(0);
  const clipPollTimerRef = useRef<number | undefined>(undefined);
  const clipReadPendingRef = useRef(false);
  const clipLocalReadPendingRef = useRef(false);
  const clipLastLocalFpRef = useRef<string | null>(null);
  const clipLastRemoteFpRef = useRef<string | null>(null);
  const clipPendingRemoteFpRef = useRef<string | null>(null);

  /* ── State helpers (keep refs in sync, synchronously) ── */

  const updateSettings = (patch: Partial<Settings>) => {
    settingsRef.current = { ...settingsRef.current, ...patch };
    setSettingsState(settingsRef.current);
  };
  const settings = settingsState;

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
    localStorage.setItem(P + "fps", String(clampInt(s.fps, 1, 12, 4)));
    localStorage.setItem(P + "quality", String(clampInt(s.quality, 25, 90, 50)));
    localStorage.setItem(P + "width", String(clampInt(s.maxWidth, 320, 3840, 1600)));
    localStorage.setItem(P + "height", String(clampInt(s.maxHeight, 240, 2160, 1000)));
  };

  const saveGpuDesktopSettings = () => {
    const s = settingsRef.current;
    localStorage.setItem(GP + "source", s.gpuSource || "");
    localStorage.setItem(GP + "encoder", s.gpuEncoder || "auto");
    localStorage.setItem(GP + "pointerBackend", s.gpuPointerBackend || "auto");
    localStorage.setItem(GP + "keyboardBackend", s.gpuKeyboardBackend || "auto");
    localStorage.setItem(GP + "enableVideo", String(s.gpuEnableVideo));
    localStorage.setItem(GP + "lowLatency", String(s.gpuLowLatency));
    localStorage.setItem(GP + "stretch", String(s.gpuStretch));
    localStorage.setItem(GP + "enableMouse", String(s.gpuEnableMouse));
    localStorage.setItem(GP + "enablePen", String(s.gpuEnablePen));
    localStorage.setItem(GP + "enableTouch", String(s.gpuEnableTouch));
    localStorage.setItem(GP + "scale", s.gpuScale);
    localStorage.setItem(GP + "frameRateSlider", s.gpuFrameRate);
    localStorage.setItem(GP + "minPressure", s.gpuMinPressure);
    localStorage.setItem(GP + "advanced", String(s.gpuAdvancedOpen));
  };

  /* ── Derived device/stream parameters ── */

  const selectedDevice = (): DesktopDevice | undefined =>
    devicesRef.current.find((d) => d.id === deviceRef.current);

  const isAndroidGpuDevice = () => {
    const desktop = selectedDevice()?.desktop || {};
    return desktop.platform === "android" || desktop.displayServer === "mediaprojection";
  };

  const gpuFrameRate = () => {
    const maxFps = isAndroidGpuDevice() ? 15 : 120;
    const v = parseFloat(settingsRef.current.gpuFrameRate) || 0;
    return Math.max(0, Math.min(maxFps, v));
  };

  const gpuMaxVideoSize = () => {
    if (isAndroidGpuDevice()) return { width: 1080, height: 1920 };
    const scale = Math.max(0.1, Math.min(2, parseFloat(settingsRef.current.gpuScale) || 1));
    const ratio = Math.min(window.devicePixelRatio || 1, 2);
    const viewer = viewerRef.current;
    return {
      width: Math.max(
        320,
        Math.min(3840, Math.round(scale * Math.max(1, viewer?.clientWidth || window.innerWidth) * ratio)),
      ),
      height: Math.max(
        240,
        Math.min(2160, Math.round(scale * Math.max(1, viewer?.clientHeight || window.innerHeight) * ratio)),
      ),
    };
  };

  const updateGpuRangeLabels = () => {
    const size = gpuMaxVideoSize();
    setGpuScaleOut(`${size.width}×${size.height}`);
    setGpuFrameRateOut(String(Math.round(gpuFrameRate())));
  };

  const gpuPointerTypes = () => {
    const s = settingsRef.current;
    const types: string[] = [];
    if (s.gpuEnableMouse) types.push("mouse");
    if (s.gpuEnablePen) types.push("pen");
    if (s.gpuEnableTouch) types.push("touch");
    return types;
  };

  const adaptiveSize = () => {
    const rect = stageRef.current?.getBoundingClientRect();
    const ratio = Math.min(window.devicePixelRatio || 1, 2);
    const width = Math.max(
      320,
      Math.min(3840, Math.round(((rect?.width || 0) || window.innerWidth || 1600) * ratio)),
    );
    const height = Math.max(
      240,
      Math.min(2160, Math.round(((rect?.height || 0) || window.innerHeight || 1000) * ratio)),
    );
    return { width, height };
  };

  const desktopOptions = () => {
    const s = settingsRef.current;
    const manual = s.mode === "manual";
    const adaptive = adaptiveSize();
    const videoCodecs = selectedDevice()?.desktop?.videoCodecs;
    return {
      mode: s.mode,
      source: s.source,
      fps: manual ? clampInt(s.fps, 1, 12, 4) : 8,
      quality: manual ? clampInt(s.quality, 25, 90, 50) : 55,
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
    const source = currentSourceRef.current ? ` · ${currentSourceRef.current}` : "";
    const elapsed = statsStartedAtRef.current
      ? Math.max(0.001, (performance.now() - statsStartedAtRef.current) / 1000)
      : 0;
    const fps = elapsed ? (frameCountRef.current / elapsed).toFixed(1) : "0.0";
    const kbps = elapsed ? ((frameBytesRef.current * 8) / elapsed / 1024).toFixed(0) : "0";
    const age = lastFrameAtRef.current
      ? Math.max(0, (performance.now() - lastFrameAtRef.current) / 1000).toFixed(1)
      : "0.0";
    setFrameInfo(
      `${canvas.width}×${canvas.height}${source} · ${fps} fps · ${kbps} kbps · ${formatBytes(frameBytesRef.current)} · ${age}s`,
    );
  };

  const gpuUpdateFrameInfo = () => {
    const now = performance.now();
    if (now - gpuLastStatsAtRef.current < 500) return;
    gpuLastStatsAtRef.current = now;
    const elapsed = statsStartedAtRef.current
      ? Math.max(0.001, (now - statsStartedAtRef.current) / 1000)
      : 0;
    const fps = elapsed ? (frameCountRef.current / elapsed).toFixed(1) : "0.0";
    const kbps = elapsed ? ((frameBytesRef.current * 8) / elapsed / 1024).toFixed(0) : "0";
    const gpuCanvas = gpuCanvasRef.current;
    const video = videoRef.current;
    const size =
      gpuVideoModeRef.current === "webcodecs"
        ? `${gpuCanvas?.width || 0}×${gpuCanvas?.height || 0}`
        : video && video.videoWidth && video.videoHeight
          ? `${video.videoWidth}×${video.videoHeight}`
          : "video";
    const source = currentSourceRef.current ? ` · ${currentSourceRef.current}` : "";
    setFrameInfo(`${size}${source} · ${fps} fps · ${kbps} kbps · ${formatBytes(frameBytesRef.current)}`);
  };

  /* ── H264 (legacy desktop stream) ── */

  const closeH264Decoder = () => {
    if (h264DecoderRef.current) {
      try {
        h264DecoderRef.current.close();
      } catch {
        /* already closed */
      }
      h264DecoderRef.current = null;
    }
    videoFormatRef.current = "";
    h264FrameIndexRef.current = 0;
    h264NeedKeyframeRef.current = false;
  };

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
    closeH264Decoder();
    const socket = wsRef.current;
    if (!socket || socket.readyState !== WebSocket.OPEN) return;
    clearTimeout(reconnectTimerRef.current);
    reconnectTimerRef.current = window.setTimeout(() => {
      if (wsRef.current === socket) connect(false);
    }, 150);
  };

  const setupH264Decoder = (msg: { codec?: string; description?: string; fps?: number }) => {
    closeH264Decoder();
    if (typeof VideoDecoder !== "function" || !msg.codec || !msg.description) return false;
    try {
      const config: VideoDecoderConfig = {
        codec: msg.codec,
        description: base64ToBytes(msg.description) as BufferSource,
        optimizeForLatency: true,
      };
      const decoder = new VideoDecoder({
        output: (frame) => {
          try {
            paintImage(frame, frame.displayWidth || frame.codedWidth, frame.displayHeight || frame.codedHeight);
          } finally {
            frame.close();
          }
        },
        error: (e) => {
          console.error("h264 decoder error", e);
          fallbackToMjpeg();
        },
      });
      decoder.configure(config);
      h264DecoderRef.current = decoder;
      videoFormatRef.current = "h264";
      h264FrameIndexRef.current = 0;
      h264NeedKeyframeRef.current = true;
      h264FrameDurationUsRef.current = Math.max(1000, Math.round(1000000 / (msg.fps || 8)));
      return true;
    } catch (e) {
      console.error("h264 configure failed", e);
      return false;
    }
  };

  const feedH264Frame = (data: ArrayBuffer): boolean => {
    const decoder = h264DecoderRef.current;
    if (!decoder || decoder.state !== "configured" || data.byteLength < 5) return false;
    const keyframe = (new Uint8Array(data, 0, 1)[0] & 1) !== 0;
    if (h264NeedKeyframeRef.current && !keyframe) return false;
    const chunk = new Uint8Array(data, 1);
    try {
      decoder.decode(
        new EncodedVideoChunk({
          type: keyframe ? "key" : "delta",
          timestamp: h264FrameIndexRef.current * h264FrameDurationUsRef.current,
          data: chunk,
        }),
      );
      h264NeedKeyframeRef.current = false;
      h264FrameIndexRef.current++;
      return true;
    } catch (e) {
      console.error("h264 decode feed failed", e);
      fallbackToMjpeg();
      return false;
    }
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
    pendingFrameRef.current = data;
    if (drawingRef.current) return;
    drawingRef.current = true;
    requestAnimationFrame(async () => {
      const frame = pendingFrameRef.current;
      pendingFrameRef.current = null;
      const format = videoFormatRef.current;
      try {
        if (format === "h264") {
          if (frame && feedH264Frame(frame)) {
            frameCountRef.current++;
            frameBytesRef.current += frame.byteLength || 0;
            if (!statsStartedAtRef.current) statsStartedAtRef.current = performance.now();
            lastFrameAtRef.current = performance.now();
            updateFrameInfo();
          }
        } else if (frame) {
          const blob = new Blob([frame], { type: "image/jpeg" });
          if (typeof window.createImageBitmap === "function") {
            try {
              const bitmap = await createImageBitmap(blob);
              if (videoFormatRef.current === format)
                paintImage(bitmap, bitmap.width, bitmap.height);
              bitmap.close();
            } catch {
              const img = await decodeImage(blob);
              if (videoFormatRef.current === format)
                paintImage(img, img.naturalWidth || img.width, img.naturalHeight || img.height);
            }
          } else {
            const img = await decodeImage(blob);
            if (videoFormatRef.current === format)
              paintImage(img, img.naturalWidth || img.width, img.naturalHeight || img.height);
          }
          if (videoFormatRef.current === format) {
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
      if (pendingFrameRef.current) drawFrame(pendingFrameRef.current);
    });
  };

  /* ── GPU video output (MSE + WebCodecs) ── */

  const closeGPUVideo = () => {
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
        if (gpuCanvas && gpuCtx) {
          gpuCanvas.width = frame.displayWidth || frame.codedWidth || gpuCanvas.width;
          gpuCanvas.height = frame.displayHeight || frame.codedHeight || gpuCanvas.height;
          gpuCtx.clearRect(0, 0, gpuCanvas.width, gpuCanvas.height);
          gpuCtx.drawImage(frame, 0, 0, gpuCanvas.width, gpuCanvas.height);
          setGpuEmpty(null);
        }
        frame.close();
        gpuUpdateFrameInfo();
      },
      error: (err) => setStatus(errMessage(err), "err"),
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
    const ws = wsRef.current;
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(message));
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

  const clipboardSyncsRemoteToLocal = () =>
    (settingsRef.current.clipboardMode === "remote-to-local" ||
      settingsRef.current.clipboardMode === "bidirectional") &&
    clipCapsRef.current?.read !== false;

  const clipboardSyncsLocalToRemote = () =>
    (settingsRef.current.clipboardMode === "local-to-remote" ||
      settingsRef.current.clipboardMode === "bidirectional") &&
    clipCapsRef.current?.write !== false;

  const stopClipboardSync = () => {
    clipGenRef.current++;
    clearInterval(clipPollTimerRef.current);
    clipPollTimerRef.current = undefined;
    clipReadPendingRef.current = false;
    clipLocalReadPendingRef.current = false;
  };

  const pollClipboardSync = () => {
    requestRemoteClipboard();
    syncLocalClipboard();
  };

  const startClipboardSync = () => {
    stopClipboardSync();
    clipLastLocalFpRef.current = null;
    clipLastRemoteFpRef.current = null;
    clipPendingRemoteFpRef.current = null;
    if (!clipCapsRef.current || settingsRef.current.clipboardMode === "off") return;
    pollClipboardSync();
    clipPollTimerRef.current = window.setInterval(pollClipboardSync, 1000);
  };

  const setClipboardCapabilities = (capabilities: ClipCaps | null) => {
    clipCapsRef.current = capabilities && capabilities.supported ? capabilities : null;
    setClipCapsState(clipCapsRef.current);
    if (!clipCapsRef.current) {
      stopClipboardSync();
      setClipStatus("");
      return;
    }
    const formats = clipCapsRef.current.formats || ["text/plain"];
    setClipStatus(`${tRef.current("desktop.clipboardAvailable")}：${formats.join(", ")}`);
    startClipboardSync();
  };

  const readBrowserClipboardItems = async (): Promise<ClipItem[]> => {
    const allowed: Record<string, boolean> = Object.fromEntries(
      (clipCapsRef.current?.formats || ["text/plain"]).map((format) => [format, true]),
    );
    const result: ClipItem[] = [];
    if (navigator.clipboard?.read) {
      for (const clipboardItem of await navigator.clipboard.read()) {
        for (const type of clipboardItem.types) {
          const mime = type.split(";", 1)[0].toLowerCase();
          if (!allowed[mime]) continue;
          const blob = await clipboardItem.getType(type);
          result.push({
            mime,
            data: bytesToBase64(new Uint8Array(await blob.arrayBuffer())),
          });
        }
      }
    }
    if (!result.length && navigator.clipboard?.readText && allowed["text/plain"]) {
      const text = await navigator.clipboard.readText();
      if (text)
        result.push({ mime: "text/plain", data: bytesToBase64(new TextEncoder().encode(text)) });
    }
    return result;
  };

  const writeBrowserClipboardItems = async (event: { items?: ClipItem[]; text?: string }) => {
    const items = clipboardItems(event);
    if (!items.length) throw new Error(tRef.current("desktop.clipboardEmpty"));
    if (navigator.clipboard?.write && typeof ClipboardItem === "function") {
      const data: Record<string, Blob> = {};
      for (const item of items) {
        if (typeof ClipboardItem.supports !== "function" || ClipboardItem.supports(item.mime))
          data[item.mime] = new Blob([base64ToBytes(item.data) as BlobPart], { type: item.mime });
      }
      if (Object.keys(data).length) await navigator.clipboard.write([new ClipboardItem(data)]);
      else throw new Error(tRef.current("desktop.clipboardFormatUnsupported"));
    } else {
      const text = items.find((item) => item.mime === "text/plain");
      if (!text || !navigator.clipboard?.writeText)
        throw new Error(tRef.current("desktop.clipboardFormatUnsupported"));
      await navigator.clipboard.writeText(new TextDecoder().decode(base64ToBytes(text.data)));
    }
    setClipStatus(
      `${tRef.current("desktop.clipboardSyncedLocal")}（${items.map((item) => item.mime).join(", ")}）`,
    );
  };

  const requestRemoteClipboard = () => {
    if (!clipCapsRef.current || !clipboardSyncsRemoteToLocal() || clipReadPendingRef.current)
      return;
    clipReadPendingRef.current = true;
    const ws = wsRef.current;
    if (connModeRef.current === "gpu") gpuSend("GetClipboard");
    else if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ op: "clipboard_get" }));
    else clipReadPendingRef.current = false;
  };

  const syncLocalClipboard = async () => {
    if (!clipCapsRef.current || !clipboardSyncsLocalToRemote() || clipLocalReadPendingRef.current)
      return;
    const generation = clipGenRef.current;
    clipLocalReadPendingRef.current = true;
    try {
      const items = await readBrowserClipboardItems();
      if (generation !== clipGenRef.current || !clipboardSyncsLocalToRemote() || !items.length)
        return;
      const fingerprint = clipboardFingerprint(items);
      if (clipLastLocalFpRef.current === null) {
        clipLastLocalFpRef.current = fingerprint;
        if (settingsRef.current.clipboardMode === "bidirectional") return;
      } else if (fingerprint === clipLastLocalFpRef.current) {
        return;
      } else {
        clipLastLocalFpRef.current = fingerprint;
      }
      clipPendingRemoteFpRef.current = fingerprint;
      const ws = wsRef.current;
      if (connModeRef.current === "gpu") gpuSend({ SetClipboard: { items } });
      else if (ws?.readyState === WebSocket.OPEN)
        ws.send(JSON.stringify({ op: "clipboard_set", clipboardItems: items }));
      setClipStatus(
        `${tRef.current("desktop.clipboardSyncedRemote")}（${items.map((item) => item.mime).join(", ")}）`,
      );
    } catch (err) {
      if (generation === clipGenRef.current)
        setClipStatus(`${tRef.current("desktop.clipboardReadFailed")}：${errMessage(err)}`);
    } finally {
      if (generation === clipGenRef.current) clipLocalReadPendingRef.current = false;
    }
  };

  const handleRemoteClipboard = (event: { items?: ClipItem[]; text?: string }) => {
    clipReadPendingRef.current = false;
    const items = clipboardItems(event);
    if (!clipboardSyncsRemoteToLocal() || !items.length) return;
    const fingerprint = clipboardFingerprint(items);
    if (clipPendingRemoteFpRef.current !== null) {
      if (fingerprint === clipPendingRemoteFpRef.current) {
        clipLastRemoteFpRef.current = fingerprint;
        clipPendingRemoteFpRef.current = null;
      }
      return;
    }
    if (clipLastRemoteFpRef.current === null) {
      clipLastRemoteFpRef.current = fingerprint;
      if (settingsRef.current.clipboardMode === "bidirectional") return;
    } else if (fingerprint === clipLastRemoteFpRef.current) {
      return;
    } else {
      clipLastRemoteFpRef.current = fingerprint;
    }
    clipLastLocalFpRef.current = fingerprint;
    writeBrowserClipboardItems({ items }).catch((err) => {
      setClipStatus(`${tRef.current("desktop.clipboardWriteFailed")}：${errMessage(err)}`);
    });
  };

  /* ── Connect / disconnect ── */

  const nextGPUReconnectDelay = () => {
    const base = gpuReconnectDelayRef.current;
    gpuReconnectDelayRef.current = Math.min(30000, gpuReconnectDelayRef.current * 2);
    const spread = Math.max(1, Math.floor(base / 5));
    return base - spread + Math.floor(Math.random() * (spread * 2 + 1));
  };

  const scheduleGPUReconnect = (dev: string, connID: number) => {
    if (manualDisconnectRef.current || connID !== connSeqRef.current || connModeRef.current !== "gpu")
      return;
    const delay = nextGPUReconnectDelay();
    clearTimeout(reconnectTimerRef.current);
    reconnectTimerRef.current = window.setTimeout(() => {
      if (
        !manualDisconnectRef.current &&
        connID === connSeqRef.current &&
        connModeRef.current === "gpu"
      )
        connectGPUDesktop(dev, true);
    }, delay);
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
    connSeqRef.current++;
    clearTimeout(reconnectTimerRef.current);
    if (wsRef.current) {
      wsRef.current.close();
      wsRef.current = null;
    }
    gpuReleaseKeyboard();
    closeH264Decoder();
    closeGPUVideo();
    remoteInputRef.current = false;
    stopClipboardSync();
    setClipboardCapabilities(null);
    setControlDisabled(false);
    setConnMode("");
    setCanvasShown(false);
    setGpuEmpty(null);
    lastCloseMsgRef.current = "";
    gpuReconnectDelayRef.current = 1000;
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
    ws.onopen = () => {
      if (connID !== connSeqRef.current) return;
      gpuReconnectDelayRef.current = 1000;
      setStatus(tRef.current("desktop.starting"));
      gpuSend("GetCapturableList");
      gpuSendConfig();
    };
    ws.onerror = () => {
      if (connID !== connSeqRef.current) return;
      setStatus(tRef.current("common.error"), "err");
    };
    ws.onclose = () => {
      if (connID !== connSeqRef.current) return;
      wsRef.current = null;
      stopClipboardSync();
      setClipboardCapabilities(null);
      setStatus(
        lastCloseMsgRef.current || tRef.current("desktop.closed"),
        lastCloseMsgRef.current ? "err" : "warn",
      );
      scheduleGPUReconnect(dev, connID);
    };
    ws.onmessage = (evt) => {
      if (connID !== connSeqRef.current) return;
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
        setGpuEmpty(null);
      } else if (msg === "ConfigOk") {
        setGpuEmpty(null);
        setStatus(tRef.current("index.desktopReady"), "ok");
      } else if (msg && msg.CapturableList) gpuSetSources(msg.CapturableList);
      else if (msg && msg.EncoderCapabilities) gpuSetEncoders(msg.EncoderCapabilities.options || []);
      else if (msg && msg.InputCapabilities) gpuSetInputCapabilities(msg.InputCapabilities);
      else if (msg && msg.ClipboardCapabilities) setClipboardCapabilities(msg.ClipboardCapabilities);
      else if (msg && msg.ClipboardContent) handleRemoteClipboard(msg.ClipboardContent);
      else if (msg && msg.RuntimeStatus) gpuUpdateFrameInfo();
      else if (msg && msg.ConfigError) {
        lastCloseMsgRef.current = msg.ConfigError;
        setStatus(msg.ConfigError, "err");
      } else if (msg && msg.Error) {
        if (String(msg.Error).startsWith("clipboard:")) {
          clipReadPendingRef.current = false;
          clipPendingRemoteFpRef.current = null;
          setClipStatus(msg.Error);
        } else {
          lastCloseMsgRef.current = msg.Error;
          setStatus(msg.Error, "err");
        }
      }
    };
  }

  async function connect(resetFormat = true) {
    disconnect();
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
    closeH264Decoder();
    resetFrameStats();
    const ws = new WebSocket(wsURL("/desktop?" + desktopQuery()));
    wsRef.current = ws;
    const connID = ++connSeqRef.current;
    lastCloseMsgRef.current = "";
    ws.binaryType = "arraybuffer";
    setStatus(tRef.current("common.connecting"));
    ws.onmessage = (evt) => {
      if (connID !== connSeqRef.current) return;
      if (evt.data instanceof ArrayBuffer) {
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
      } else if (msg.op === "auth_fail") {
        forgetDevicePassword(dev);
        setStatus(msg.message || tt("common.passwordWrong"), "err");
      } else if (msg.op === "starting") {
        setStatus(tt("desktop.starting"));
      } else if (msg.op === "ready") {
        const canvas = canvasRef.current;
        if (canvas) {
          canvas.width = msg.width || 1;
          canvas.height = msg.height || 1;
        }
        setCanvasShown(true);
        currentSourceRef.current = msg.source || "";
        if (msg.format === "h264" && !setupH264Decoder(msg)) fallbackToMjpeg();
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
        const w = canvas?.width || 0;
        const h = canvas?.height || 0;
        setStatus(`${tt("index.desktopReady")} · ${w}×${h}${source}`, "ok");
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
        lastCloseMsgRef.current = msg.message || tt("common.error");
        setStatus(lastCloseMsgRef.current, "err");
      } else if (msg.op === "closed") {
        lastCloseMsgRef.current = msg.message || tt("desktop.closed");
        setStatus(lastCloseMsgRef.current, msg.message ? "err" : "warn");
      } else if (msg.op === "clipboard_content") {
        handleRemoteClipboard({ items: msg.clipboardItems || [], text: msg.text || "" });
      } else if (msg.op === "clipboard_error") {
        clipReadPendingRef.current = false;
        clipPendingRemoteFpRef.current = null;
        setClipStatus(msg.message || tt("desktop.clipboardRemoteError"));
      } else if (msg.op === "input_error") {
        setStatus(msg.message || tt("common.error"), "err");
      }
    };
    ws.onclose = () => {
      if (connID !== connSeqRef.current) return;
      wsRef.current = null;
      stopClipboardSync();
      setClipboardCapabilities(null);
      closeH264Decoder();
      setStatus(
        lastCloseMsgRef.current || tRef.current("desktop.closed"),
        lastCloseMsgRef.current ? "err" : "warn",
      );
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

  /* ── Legacy canvas input ── */

  const canvasPoint = (event: { clientX: number; clientY: number }) => {
    const canvas = canvasRef.current;
    const rect = canvas?.getBoundingClientRect();
    const cw = canvas?.width || 1;
    const ch = canvas?.height || 1;
    const rw = rect?.width || 1;
    const rh = rect?.height || 1;
    const x = Math.max(
      0,
      Math.min(cw - 1, Math.round(((event.clientX - (rect?.left || 0)) * cw) / rw)),
    );
    const y = Math.max(
      0,
      Math.min(ch - 1, Math.round(((event.clientY - (rect?.top || 0)) * ch) / rh)),
    );
    return { x, y };
  };

  const sendInput = (input: Record<string, unknown>) => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN || !settingsRef.current.control || !remoteInputRef.current)
      return;
    if (ws.bufferedAmount > 512 * 1024) return;
    try {
      ws.send(JSON.stringify({ op: "input", ...input }));
    } catch (e) {
      setStatus(errMessage(e), "err");
    }
  };

  const sendCursor = (input: Record<string, unknown>) => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN || !settingsRef.current.showCursor) return;
    if (ws.bufferedAmount > 128 * 1024) return;
    try {
      ws.send(JSON.stringify({ op: "input", inputType: "cursor_move", ...input }));
    } catch (e) {
      setStatus(errMessage(e), "err");
    }
  };

  const sendMouse = (type: string, event: PointerEvent, extra: Record<string, unknown> = {}) => {
    const now = performance.now();
    const ws = wsRef.current;
    if (type === "mouse_move" && (now - lastMouseSentRef.current < 16 || (ws && ws.bufferedAmount > 128 * 1024)))
      return;
    lastMouseSentRef.current = now;
    const point = canvasPoint(event);
    if (type === "mouse_move" && (!settingsRef.current.control || !remoteInputRef.current))
      sendCursor({
        x: point.x,
        y: point.y,
        pointerType: event.pointerType || "mouse",
        pointerId: event.pointerId || 0,
        pressure: event.pressure || 0,
      });
    sendInput({
      inputType: type,
      x: point.x,
      y: point.y,
      button: event.button || 0,
      pointerType: event.pointerType || "mouse",
      pointerId: event.pointerId || 0,
      pressure: event.pressure || 0,
      ...extra,
    });
  };

  const sendKey = (type: string, event: KeyboardEvent) => {
    if (!settingsRef.current.control || !remoteInputRef.current) return;
    event.preventDefault();
    sendInput({
      inputType: type,
      key: event.key,
      code: event.code,
      ctrlKey: event.ctrlKey,
      altKey: event.altKey,
      shiftKey: event.shiftKey,
      metaKey: event.metaKey,
    });
  };

  const canvasWheel = (e: WheelEvent) => {
    if (!settingsRef.current.control || !remoteInputRef.current) return;
    e.preventDefault();
    const point = canvasPoint(e);
    sendInput({
      inputType: "wheel",
      x: point.x,
      y: point.y,
      deltaX: Math.round(e.deltaX),
      deltaY: Math.round(e.deltaY),
    });
  };

  /* ── GPU input ── */

  const gpuPointerBits = (button: number) => {
    if (button === 0) return 1;
    if (button === 2) return 2;
    if (button === 1) return 4;
    if (button === 3) return 8;
    if (button === 4) return 16;
    if (button === 5) return 32;
    return 0;
  };

  const gpuVideoContentRect = () => {
    const overlay = overlayRef.current;
    const rect = overlay?.getBoundingClientRect() || new DOMRect();
    const video = videoRef.current;
    const gpuCanvas = gpuCanvasRef.current;
    const videoWidth =
      gpuVideoModeRef.current === "webcodecs" ? gpuCanvas?.width || 0 : video?.videoWidth || 0;
    const videoHeight =
      gpuVideoModeRef.current === "webcodecs" ? gpuCanvas?.height || 0 : video?.videoHeight || 0;
    if (!videoWidth || !videoHeight || !rect.width || !rect.height || settingsRef.current.gpuStretch)
      return rect;
    if (settingsRef.current.fit === "actual") {
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
  };

  const gpuPointerMessage = (event: PointerEvent, eventType = event.type) => {
    const rect = gpuVideoContentRect();
    const x = Math.max(0, Math.min(1, (event.clientX - rect.left) / Math.max(1, rect.width)));
    const y = Math.max(0, Math.min(1, (event.clientY - rect.top) / Math.max(1, rect.height)));
    const previous = gpuLastPointerRef.current.get(event.pointerId) || {
      x: event.clientX,
      y: event.clientY,
    };
    gpuLastPointerRef.current.set(event.pointerId, { x: event.clientX, y: event.clientY });
    const diag = Math.max(1, Math.sqrt(rect.width * rect.width + rect.height * rect.height));
    const pressure = Math.max(
      event.pressure || (event.buttons ? 0.5 : 0),
      parseFloat(settingsRef.current.gpuMinPressure) || 0,
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
  };

  const gpuKeyMessage = (event: KeyboardEvent, eventType: string) => ({
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
  });

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
    sendMouse,
    sendKey,
    canvasWheel,
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
    sendMouse,
    sendKey,
    canvasWheel,
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

    if (canvas) {
      on(canvas, "contextmenu", (e: Event) => e.preventDefault());
      on(canvas, "auxclick", (e: Event) => e.preventDefault());
      on(canvas, "pointermove", (e: PointerEvent) => apiRef.current.sendMouse("mouse_move", e));
      on(canvas, "pointerdown", (e: PointerEvent) => {
        e.preventDefault();
        canvas.focus();
        if (canvas.setPointerCapture) canvas.setPointerCapture(e.pointerId);
        apiRef.current.sendMouse("mouse_down", e);
      });
      on(canvas, "pointerup", (e: PointerEvent) => {
        e.preventDefault();
        apiRef.current.sendMouse("mouse_up", e);
        if (canvas.releasePointerCapture && canvas.hasPointerCapture?.(e.pointerId))
          canvas.releasePointerCapture(e.pointerId);
      });
      on(canvas, "pointercancel", (e: PointerEvent) => {
        apiRef.current.sendMouse("mouse_up", e);
        if (canvas.releasePointerCapture && canvas.hasPointerCapture?.(e.pointerId))
          canvas.releasePointerCapture(e.pointerId);
      });
      on(canvas, "wheel", (e: WheelEvent) => apiRef.current.canvasWheel(e), { passive: false });
      on(canvas, "keydown", (e: KeyboardEvent) => apiRef.current.sendKey("key_down", e));
      on(canvas, "keyup", (e: KeyboardEvent) => apiRef.current.sendKey("key_up", e));
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
          if (!apiRef.current.gpuInputAllowed()) return;
          if (!apiRef.current.gpuPointerTypes().includes(e.pointerType || "mouse")) return;
          overlay.focus();
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
      on(overlay, "keydown", (e: KeyboardEvent) => {
        if (!apiRef.current.gpuInputAllowed()) return;
        if (gpuComposingRef.current || e.isComposing || e.key === "Process" || e.keyCode === 229) {
          e.preventDefault();
          e.stopPropagation();
          return;
        }
        const eventType = e.repeat ? "repeat" : "down";
        const msg = apiRef.current.gpuKeyMessage(e, eventType);
        if (eventType === "down")
          gpuHeldKeysRef.current.set(
            `${msg.KeyboardEvent.code}:${msg.KeyboardEvent.location}`,
            msg.KeyboardEvent,
          );
        apiRef.current.gpuSend(msg);
        e.preventDefault();
        e.stopPropagation();
      });
      on(overlay, "keyup", (e: KeyboardEvent) => {
        if (!apiRef.current.gpuInputAllowed()) return;
        if (gpuComposingRef.current || e.isComposing || e.key === "Process" || e.keyCode === 229) {
          e.preventDefault();
          e.stopPropagation();
          return;
        }
        const msg = apiRef.current.gpuKeyMessage(e, "up");
        gpuHeldKeysRef.current.delete(`${msg.KeyboardEvent.code}:${msg.KeyboardEvent.location}`);
        apiRef.current.gpuSend(msg);
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
      on(
        document,
        "visibilitychange",
        () => {
          if (document.hidden) {
            apiRef.current.gpuReleaseKeyboard();
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
          if (!apiRef.current.gpuInputAllowed()) return;
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
          if (!apiRef.current.gpuInputAllowed()) return;
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
          if (!apiRef.current.gpuInputAllowed()) return;
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
          if (!apiRef.current.gpuInputAllowed()) return;
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
      connSeqRef.current++;
      clearTimeout(reconnectTimerRef.current);
      clearTimeout(resizeTimerRef.current);
      clearInterval(clipPollTimerRef.current);
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
    <div className="flex h-full flex-col">
      {/* Controls bar */}
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-border-seam px-3 py-2">
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
        <Input
          type="password"
          value={password}
          onChange={(e) => {
            passwordRef.current = e.target.value;
            setPassword(e.target.value);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") connect();
          }}
          placeholder={t("terminal.passwordPlaceholder")}
          className="h-8 w-44 text-xs"
        />
        <Button size="sm" onClick={() => connect()}>
          {t("common.connect")}
        </Button>
        <Button size="sm" variant="outline" onClick={() => disconnect()}>
          {t("desktop.disconnect")}
        </Button>
        <span
          className={cn(
            "text-xs",
            status.cls === "ok" && "text-success",
            status.cls === "warn" && "text-attention",
            status.cls === "err" && "text-destructive",
            !status.cls && "text-muted-foreground",
          )}
        >
          {status.text || t("desktop.idle")}
        </span>
      </div>

      {/* Settings bar */}
      <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-2 border-b border-border-seam px-3 py-2 text-xs text-muted-foreground">
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
                <SelectTrigger className="h-7 w-auto text-xs">
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
                <SelectTrigger className="h-7 w-auto max-w-64 text-xs">
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
            <span
              className={cn(
                "flex flex-wrap items-center gap-x-3 gap-y-2",
                settings.mode !== "manual" && "pointer-events-none opacity-45",
              )}
            >
              <label className="inline-flex items-center gap-1 whitespace-nowrap">
                <span>{t("desktop.fps")}</span>
                <Input
                  type="number"
                  min={1}
                  max={12}
                  value={settings.fps}
                  onChange={(e) => updateSettings({ fps: e.target.value })}
                  onBlur={() => {
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
                  value={settings.quality}
                  onChange={(e) => updateSettings({ quality: e.target.value })}
                  onBlur={() => {
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
            </span>
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
                <SelectTrigger className="h-7 w-auto max-w-64 text-xs">
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
                <SelectTrigger className="h-7 w-auto text-xs">
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
              <SelectTrigger className="h-7 w-auto max-w-64 text-xs">
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
            <SelectTrigger className="h-7 w-auto text-xs">
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
            <SelectTrigger className="h-7 w-auto text-xs">
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
                <SelectTrigger className="h-7 w-auto text-xs">
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
                <SelectTrigger className="h-7 w-auto text-xs">
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

      {/* Stage */}
      <div
        ref={stageRef}
        className={cn(
          "flex min-h-0 flex-1 p-3 overscroll-contain",
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
            className="rounded-lg border border-border-seam bg-black outline-none"
            style={{
              display: canvasShown ? "block" : "none",
              cursor: canvasCursor,
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
                className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center p-6 text-center leading-loose text-muted-foreground"
                style={{
                  background:
                    "radial-gradient(circle at center, rgba(34,211,238,.07), transparent 42%)",
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
      onDevicePick={(id) => requestDevice(0, id)}
    >
      <DeviceDropTarget
        className="h-full min-h-0"
        onDropDevice={(id) => requestDevice(0, id)}
      >
        <DesktopSession primary deviceRequest={requests.get(0)} />
      </DeviceDropTarget>
    </SplitPageShell>
  );
}
