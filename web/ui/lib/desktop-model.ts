/* Shared desktop-viewer model: protocol payload shapes, clipboard helpers and
 * the sizing/statistics math both Go desktop viewers render with. The page and
 * the agent panel keep their own transports and settings persistence. */

export interface DesktopSource {
  id: string;
  label?: string;
  kind?: string;
  backend?: string;
  width?: number;
  height?: number;
  x?: number;
  y?: number;
}

export interface InputOption {
  id?: string;
  label?: string;
  kinds?: string[];
  requires?: string[];
}

export interface DesktopCaps {
  supported?: boolean;
  rtc?: boolean;
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

export interface DesktopDevice {
  id: string;
  hasPassword?: boolean;
  os?: string;
  desktop?: DesktopCaps;
  gpuDesktop?: boolean;
}

export interface ClipCaps {
  supported?: boolean;
  read?: boolean;
  write?: boolean;
  formats?: string[];
  maxBytes?: number;
}

export interface ClipItem {
  mime: string;
  data: string;
}

export interface SelectOption {
  value: string;
  label: string;
}

/** Fields both viewers persist; the page adds manual-mode sizing and the
 * panel keeps its own namespaced copies of the same keys. */
export interface DesktopSettingsBase {
  source: string;
  fps: string;
  quality: string;
  inputBackend: string;
  fit: string;
  control: boolean;
  showCursor: boolean;
  clipboardMode: string;
  gpuSource: string;
  gpuEncoder: string;
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

export type ConnMode = "" | "desktop" | "gpu";
export type StatusCls = "" | "ok" | "warn" | "err";

type DesktopStoredSettings = Omit<DesktopSettingsBase, "fps" | "quality">;

/** Settings both viewers read from the same keys, under caller-chosen prefixes
 * (the agent panel uses its own so page and panel never overwrite each other).
 * fps/quality stay caller-derived: the page normalizes stream rates, the panel
 * snaps to its preset list. */
export function loadDesktopSettings(settingsPrefix: string, gpuPrefix: string): DesktopStoredSettings {
  const fit = localStorage.getItem(settingsPrefix + "fit");
  const clipboardMode = localStorage.getItem(settingsPrefix + "clipboardSyncMode");
  return {
    source: localStorage.getItem(settingsPrefix + "source") || "auto",
    inputBackend: localStorage.getItem(settingsPrefix + "inputBackend") || "auto",
    fit: fit === "fit" || fit === "actual" ? fit : "fit",
    control: localStorage.getItem(settingsPrefix + "control") === "true",
    showCursor: localStorage.getItem(settingsPrefix + "showCursor") === "true",
    // Clipboard sync is opt-in: it polls the local clipboard in the background.
    clipboardMode: ["off", "remote-to-local", "local-to-remote", "bidirectional"].includes(
      clipboardMode || "",
    )
      ? (clipboardMode as string)
      : "off",
    gpuSource: localStorage.getItem(gpuPrefix + "source") || "",
    gpuEncoder: localStorage.getItem(gpuPrefix + "encoder") || "auto",
    gpuEnableVideo: localStorage.getItem(gpuPrefix + "enableVideo") !== "false",
    gpuLowLatency: localStorage.getItem(gpuPrefix + "lowLatency") !== "false",
    gpuStretch: localStorage.getItem(gpuPrefix + "stretch") === "true",
    gpuEnableMouse: localStorage.getItem(gpuPrefix + "enableMouse") !== "false",
    gpuEnablePen: localStorage.getItem(gpuPrefix + "enablePen") !== "false",
    gpuEnableTouch: localStorage.getItem(gpuPrefix + "enableTouch") !== "false",
    gpuScale: localStorage.getItem(gpuPrefix + "scale") || "1.5",
    gpuFrameRate: localStorage.getItem(gpuPrefix + "frameRateSlider") || "30",
    gpuPointerBackend: localStorage.getItem(gpuPrefix + "pointerBackend") || "auto",
    gpuKeyboardBackend: localStorage.getItem(gpuPrefix + "keyboardBackend") || "auto",
    gpuMinPressure: localStorage.getItem(gpuPrefix + "minPressure") || "0",
  };
}

/** GPU settings are namespaced by the caller so the page and the agent panel
 * never clobber each other's persisted stream configuration. */
export function persistGpuDesktopSettings(prefix: string, s: DesktopSettingsBase): void {
  localStorage.setItem(prefix + "source", s.gpuSource || "");
  localStorage.setItem(prefix + "encoder", s.gpuEncoder || "auto");
  localStorage.setItem(prefix + "pointerBackend", s.gpuPointerBackend || "auto");
  localStorage.setItem(prefix + "keyboardBackend", s.gpuKeyboardBackend || "auto");
  localStorage.setItem(prefix + "enableVideo", String(s.gpuEnableVideo));
  localStorage.setItem(prefix + "lowLatency", String(s.gpuLowLatency));
  localStorage.setItem(prefix + "stretch", String(s.gpuStretch));
  localStorage.setItem(prefix + "enableMouse", String(s.gpuEnableMouse));
  localStorage.setItem(prefix + "enablePen", String(s.gpuEnablePen));
  localStorage.setItem(prefix + "enableTouch", String(s.gpuEnableTouch));
  localStorage.setItem(prefix + "scale", s.gpuScale);
  localStorage.setItem(prefix + "frameRateSlider", s.gpuFrameRate);
  localStorage.setItem(prefix + "minPressure", s.gpuMinPressure);
}

export function clampInt(value: string | number, min: number, max: number, fallback: number): number {
  const n = parseInt(String(value), 10);
  if (!Number.isFinite(n)) return fallback;
  return Math.max(min, Math.min(max, n));
}

export function errMessage(err: unknown): string {
  return err instanceof Error ? err.message || String(err) : String(err);
}

function formatBytes(bytes: number): string {
  if (bytes >= 1024 * 1024) return (bytes / 1024 / 1024).toFixed(1) + " MB";
  if (bytes >= 1024) return (bytes / 1024).toFixed(1) + " KB";
  return bytes + " B";
}

export function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  const chunk = 0x8000;
  for (let offset = 0; offset < bytes.length; offset += chunk)
    binary += String.fromCharCode(...bytes.subarray(offset, offset + chunk));
  return btoa(binary);
}

export function base64ToBytes(value: string): Uint8Array {
  const binary = atob(value || "");
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index);
  return bytes;
}

export function clipboardItems(event: { items?: ClipItem[]; text?: string }): ClipItem[] {
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

export function clipboardFingerprint(items: ClipItem[]): string {
  return items.map((item) => `${item.mime}:${item.data}`).sort().join("|");
}

export function inputBackendLabel(backend: InputOption | string): string {
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

export function sourceLabel(source: DesktopSource): string {
  const parts = [source.label || source.id];
  if (source.kind) parts.push(source.kind);
  if (source.backend) parts.push(source.backend);
  if (source.width && source.height)
    parts.push(`${source.width}×${source.height}${source.x || source.y ? `@${source.x},${source.y}` : ""}`);
  return parts.join(" · ");
}

export interface DesktopSize {
  width: number;
  height: number;
}

/** The capture follows the visible stage; the device-pixel ratio is capped so
 * a 2× display does not quadruple the stream. */
export function adaptiveDesktopSize(
  rect: { width?: number; height?: number } | null | undefined,
  viewportWidth: number,
  viewportHeight: number,
  devicePixelRatio: number,
): DesktopSize {
  const ratio = Math.min(devicePixelRatio || 1, 2);
  const width = Math.max(
    320,
    Math.min(3840, Math.round(((rect?.width || 0) || viewportWidth || 1600) * ratio)),
  );
  const height = Math.max(
    240,
    Math.min(2160, Math.round(((rect?.height || 0) || viewportHeight || 1000) * ratio)),
  );
  return { width, height };
}

/** GPU sessions request device-native sizes: Android is portrait-locked and
 * every other platform scales the viewer by the stored scale factor. */
export function gpuDesktopMaxVideoSize({
  android,
  scale,
  ratio,
  viewer,
  viewportWidth,
  viewportHeight,
}: {
  android: boolean;
  scale: number;
  ratio: number;
  viewer: { clientWidth?: number; clientHeight?: number } | null;
  viewportWidth: number;
  viewportHeight: number;
}): DesktopSize {
  if (android) return { width: 1080, height: 1920 };
  const bounded = Math.max(0.1, Math.min(2, scale));
  return {
    width: Math.max(
      320,
      Math.min(3840, Math.round(bounded * Math.max(1, viewer?.clientWidth || viewportWidth) * ratio)),
    ),
    height: Math.max(
      240,
      Math.min(2160, Math.round(bounded * Math.max(1, viewer?.clientHeight || viewportHeight) * ratio)),
    ),
  };
}

export function gpuDesktopFrameRate(value: string, android: boolean): number {
  const maxFps = android ? 15 : 120;
  const v = parseFloat(value) || 0;
  return Math.max(0, Math.min(maxFps, v));
}

/** Frame counters are cumulative; the display needs rates over the elapsed
 * window and a fixed decimal shape so the toolbar does not jitter in width. */
export function desktopFrameRates({
  count,
  bytes,
  startedAt,
  now,
}: {
  count: number;
  bytes: number;
  startedAt: number;
  now: number;
}): { fps: string; kbps: string } {
  const elapsed = startedAt ? Math.max(0.001, (now - startedAt) / 1000) : 0;
  return {
    fps: elapsed ? (count / elapsed).toFixed(1) : "0.0",
    kbps: elapsed ? ((bytes * 8) / elapsed / 1024).toFixed(0) : "0",
  };
}

/* Frame metrics only: geometry, source and encoder live in the session info
 * line so the bar does not repeat them on every refresh. */
export function desktopFrameLine({
  fps,
  kbps,
  bytes,
  age,
}: {
  fps: string;
  kbps: string;
  bytes: number;
  age?: string;
}): string {
  const freshness = age === undefined ? "" : ` · ${age}s`;
  return `${fps} fps · ${kbps} kbps · ${formatBytes(bytes)}${freshness}`;
}
