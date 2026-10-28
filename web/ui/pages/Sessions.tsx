import {
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
} from "react";
import { useSearchParams } from "react-router-dom";
import { MobileNavContext } from "../shell/AppShell";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { WebglAddon } from "@xterm/addon-webgl";
import { ImageAddon } from "@xterm/addon-image";
import "@xterm/xterm/css/xterm.css";
import { useI18n } from "../lib/i18n";
import {
  apiJSON,
  wsURL,
  getDevicePassword,
  rememberDevicePassword,
  forgetDevicePassword,
} from "../lib/api";
import { cn } from "../lib/utils";
import { Button } from "../components/button";
import { Input } from "../components/input";
import { Icon } from "../components/icon";

/* ── Types ── */

interface SessionInfo {
  id: string;
  clientId: string;
  type: string; // "shell" | "exec" | "sftp"
  command: string;
  pty: boolean;
  term: string;
  rows: number;
  cols: number;
  createdAt: string;
  observers: number;
}

type Translate = (key: string, vars?: Record<string, string | number>) => string;

/* ── Theme-aware xterm palette (re-read on theme change) ── */

function readXtermTheme() {
  const cs = getComputedStyle(document.documentElement);
  const v = (name: string) => cs.getPropertyValue(name).trim();
  const ansi = (i: number) => v(`--ansi-${i}`);
  return {
    background: v("--background") || undefined,
    foreground: v("--foreground") || undefined,
    cursor: v("--foreground") || undefined,
    cursorAccent: v("--background") || undefined,
    selectionBackground: "color-mix(in oklab, var(--foreground) 25%, transparent)",
    black: ansi(0),
    red: ansi(1),
    green: ansi(2),
    yellow: ansi(3),
    blue: ansi(4),
    magenta: ansi(5),
    cyan: ansi(6),
    white: ansi(7),
    brightBlack: ansi(8),
    brightRed: ansi(9),
    brightGreen: ansi(10),
    brightYellow: ansi(11),
    brightBlue: ansi(12),
    brightMagenta: ansi(13),
    brightCyan: ansi(14),
    brightWhite: ansi(15),
  };
}

/* ── Kitty graphics protocol writer ──
 * Port of web/public/terminal-images.js: intercepts APC ESC_G ... ESC\
 * sequences in the output stream and renders them as <img> overlays. */

interface KittyWriter {
  write(data: string | ArrayBuffer | Uint8Array): void;
}

function createKittyWriter(term: Terminal): KittyWriter {
  const ESC = "\x1b";
  const START = ESC + "_G";
  const END = ESC + "\\";
  const PLACEHOLDER = 0x10eeee;
  const maxSequence = 24 * 1024 * 1024;
  const maxStorage = 64;
  const decoder = new TextDecoder();
  let pending = "";
  let chunks = new Map<string, { params: Record<string, string>; payload: string }>();
  let currentChunkKey = "_default";
  let rendered: HTMLImageElement[] = [];
  let virtualCursor = { row: 0, col: 0 };
  let activePlaceholderImage: HTMLImageElement | null = null;

  function parseParams(raw: string): Record<string, string> {
    const params: Record<string, string> = {};
    if (!raw) return params;
    raw.split(",").forEach((part) => {
      const idx = part.indexOf("=");
      if (idx <= 0) return;
      params[part.slice(0, idx)] = part.slice(idx + 1);
    });
    return params;
  }

  function mimeFor(params: Record<string, string>): string {
    if (params.f === "100") return "image/png";
    if (params.f === "101") return "image/jpeg";
    if (params.f === "102") return "image/gif";
    return "image/png";
  }

  function getCellSize(): { width: number; height: number } {
    const core = term as unknown as {
      _core?: { _renderService?: { dimensions?: { css?: { cell?: { width: number; height: number } } } } };
    };
    const dims = core?._core?._renderService?.dimensions?.css?.cell;
    if (dims && dims.width && dims.height) return { width: dims.width, height: dims.height };
    const row = term.element?.querySelector(".xterm-rows > div");
    if (row) {
      const rect = row.getBoundingClientRect();
      const cols = Math.max(1, term.cols || 80);
      return { width: rect.width / cols, height: rect.height || 17 };
    }
    return { width: 8, height: 17 };
  }

  function ensureLayer(): HTMLElement | null {
    if (!term.element) return null;
    term.element.style.position = term.element.style.position || "relative";
    let layer = term.element.querySelector<HTMLElement>(".rdev-kitty-image-layer");
    if (layer) return layer;
    layer = document.createElement("div");
    layer.className = "rdev-kitty-image-layer";
    layer.style.cssText =
      "position:absolute;inset:0;pointer-events:none;overflow:hidden;z-index:4;";
    term.element.appendChild(layer);
    return layer;
  }

  function base64ToBytes(raw: string): Uint8Array {
    const bin = atob(raw.replace(/\s+/g, ""));
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    return bytes;
  }

  function bytesToBase64(bytes: Uint8Array): string {
    let out = "";
    const chunk = 0x8000;
    for (let i = 0; i < bytes.length; i += chunk) {
      out += String.fromCharCode.apply(
        null,
        bytes.subarray(i, i + chunk) as unknown as number[],
      );
    }
    return btoa(out);
  }

  async function inflateZlib(bytes: Uint8Array): Promise<Uint8Array> {
    if (!("DecompressionStream" in window)) {
      throw new Error("compressed Kitty images are not supported by this browser");
    }
    const stream = new Blob([bytes as unknown as BlobPart])
      .stream()
      .pipeThrough(new DecompressionStream("deflate"));
    return new Uint8Array(await new Response(stream).arrayBuffer());
  }

  function rawPixelSize(
    params: Record<string, string>,
    bytes: Uint8Array,
    channels: number,
  ): { width: number; height: number } {
    let width = parseInt(params.s || params.c || "", 10);
    let height = parseInt(params.v || params.r || "", 10);
    if (width > 0 && height > 0) return { width, height };
    if (width > 0)
      return { width, height: Math.max(1, Math.floor(bytes.length / channels / width)) };
    if (height > 0)
      return { width: Math.max(1, Math.floor(bytes.length / channels / height)), height };
    const pixels = Math.max(1, Math.floor(bytes.length / channels));
    width = Math.max(1, Math.round(Math.sqrt(pixels)));
    height = Math.max(1, Math.ceil(pixels / width));
    return { width, height };
  }

  function rawPixelsToPNG(params: Record<string, string>, bytes: Uint8Array): string | null {
    const channels = params.f === "24" ? 3 : params.f === "32" ? 4 : 0;
    if (!channels) return null;
    const size = rawPixelSize(params, bytes, channels);
    const canvas = document.createElement("canvas");
    canvas.width = size.width;
    canvas.height = size.height;
    const ctx = canvas.getContext("2d");
    if (!ctx) return null;
    const image = ctx.createImageData(size.width, size.height);
    const pixels = size.width * size.height;
    for (let i = 0, j = 0; i < pixels; i++, j += channels) {
      const out = i * 4;
      image.data[out] = bytes[j] || 0;
      image.data[out + 1] = bytes[j + 1] || 0;
      image.data[out + 2] = bytes[j + 2] || 0;
      image.data[out + 3] = channels === 4 ? (bytes[j + 3] ?? 255) : 255;
    }
    ctx.putImageData(image, 0, 0);
    return canvas.toDataURL("image/png");
  }

  async function payloadToDataURL(
    params: Record<string, string>,
    payload: string,
  ): Promise<string> {
    let bytes = base64ToBytes(payload);
    if (params.o === "z") bytes = await inflateZlib(bytes);
    const rawPNG = rawPixelsToPNG(params, bytes);
    if (rawPNG) return rawPNG;
    return "data:" + mimeFor(params) + ";base64," + bytesToBase64(bytes);
  }

  function fitImageToPlaceholder(
    img: HTMLImageElement,
    bounds: { top: number; left: number; bottom: number; right: number },
  ) {
    const cell = getCellSize();
    img.style.left = Math.max(0, bounds.left * cell.width) + "px";
    img.style.top = Math.max(0, bounds.top * cell.height) + "px";
    img.style.width = Math.max(1, bounds.right - bounds.left + 1) * cell.width + "px";
    img.style.height = Math.max(1, bounds.bottom - bounds.top + 1) * cell.height + "px";
  }

  function notePlaceholder(row: number, col: number) {
    const img = activePlaceholderImage || rendered[rendered.length - 1];
    if (!img) return;
    const imgAny = img as HTMLImageElement & {
      _rdevPlaceholderBounds?: { top: number; left: number; bottom: number; right: number };
    };
    const bounds = imgAny._rdevPlaceholderBounds || { top: row, left: col, bottom: row, right: col };
    bounds.top = Math.min(bounds.top, row);
    bounds.left = Math.min(bounds.left, col);
    bounds.bottom = Math.max(bounds.bottom, row);
    bounds.right = Math.max(bounds.right, col);
    imgAny._rdevPlaceholderBounds = bounds;
    fitImageToPlaceholder(img, bounds);
  }

  function observeTerminalLayout(text: string) {
    let idx = 0;
    while (idx < text.length) {
      if (text.charCodeAt(idx) === 0x1b && text[idx + 1] === "[") {
        const match = /^\x1b\[([0-9;?]*)([@-~])/.exec(text.slice(idx));
        if (match) {
          const args = match[1]
            .replace(/^\?/, "")
            .split(";")
            .filter(Boolean)
            .map((value) => parseInt(value, 10));
          const cmd = match[2];
          if (cmd === "H" || cmd === "f") {
            virtualCursor.row = Math.max(0, (args[0] || 1) - 1);
            virtualCursor.col = Math.max(0, (args[1] || 1) - 1);
          } else if (cmd === "A") {
            virtualCursor.row = Math.max(0, virtualCursor.row - (args[0] || 1));
          } else if (cmd === "B") {
            virtualCursor.row += args[0] || 1;
          } else if (cmd === "C") {
            virtualCursor.col += args[0] || 1;
          } else if (cmd === "D") {
            virtualCursor.col = Math.max(0, virtualCursor.col - (args[0] || 1));
          }
          idx += match[0].length;
          continue;
        }
      }
      const code = text.codePointAt(idx)!;
      const char = String.fromCodePoint(code);
      const charLength = char.length;
      if (code === PLACEHOLDER) {
        notePlaceholder(virtualCursor.row, virtualCursor.col);
        virtualCursor.col += 1;
      } else if (char === "\r") {
        virtualCursor.col = 0;
      } else if (char === "\n") {
        virtualCursor.row += 1;
      } else if (!/\p{Mark}/u.test(char) && code >= 0x20) {
        virtualCursor.col += 1;
      }
      idx += charLength;
    }
  }

  function writePlain(text: string) {
    if (text) {
      observeTerminalLayout(text);
      term.write(text);
    }
  }

  function trimRendered() {
    while (rendered.length > maxStorage) {
      const node = rendered.shift();
      node?.parentNode?.removeChild(node);
    }
  }

  function deleteImages(params: Record<string, string>) {
    if (params.d === "A" || (!params.i && !params.I)) {
      rendered.forEach((node) => node.parentNode?.removeChild(node));
      rendered = [];
      activePlaceholderImage = null;
      chunks.clear();
      currentChunkKey = "_default";
      return;
    }
    const id = params.i || params.I;
    rendered = rendered.filter((node) => {
      if (node.dataset.rdevKittyId !== id) return true;
      if (activePlaceholderImage === node) activePlaceholderImage = null;
      node.parentNode?.removeChild(node);
      return false;
    });
    chunks.delete(id);
    if (currentChunkKey === id) currentChunkKey = "_default";
  }

  function render(params: Record<string, string>, payload: string) {
    if (!payload || params.t === "f" || params.t === "t") {
      writePlain("\r\n\x1b[33m[RDev: kitty image path mode is not supported yet]\x1b[0m\r\n");
      return;
    }
    const layer = ensureLayer();
    if (!layer) return;
    const cell = getCellSize();
    const cursorX = term.buffer?.active?.cursorX ?? virtualCursor.col ?? 0;
    const cursorY = term.buffer?.active?.cursorY ?? virtualCursor.row ?? 0;
    const img = document.createElement("img");
    if (params.i || params.I) img.dataset.rdevKittyId = params.i || params.I;
    img.decoding = "async";
    img.loading = "eager";
    img.style.cssText = "position:absolute;object-fit:contain;image-rendering:auto;";
    img.style.left = Math.max(0, cursorX * cell.width) + "px";
    img.style.top = Math.max(0, cursorY * cell.height) + "px";
    if (params.c) img.style.width = Math.max(1, parseInt(params.c, 10)) * cell.width + "px";
    else if (params.s) img.style.width = Math.max(1, parseInt(params.s, 10)) + "px";
    if (params.r) img.style.height = Math.max(1, parseInt(params.r, 10)) * cell.height + "px";
    else if (params.v) img.style.height = Math.max(1, parseInt(params.v, 10)) + "px";
    payloadToDataURL(params, payload)
      .then((src) => {
        img.src = src;
      })
      .catch((err) => {
        if (img.parentNode) img.parentNode.removeChild(img);
        writePlain(
          "\r\n\x1b[31m[RDev: kitty image decode failed: " + err.message + "]\x1b[0m\r\n",
        );
      });
    layer.appendChild(img);
    rendered.push(img);
    if (params.U === "1") activePlaceholderImage = img;
    trimRendered();
    if (params.r) writePlain("\n".repeat(Math.max(1, parseInt(params.r, 10))));
  }

  function handleSequence(body: string) {
    const sep = body.indexOf(";");
    const params = parseParams(sep >= 0 ? body.slice(0, sep) : body);
    const payload = sep >= 0 ? body.slice(sep + 1) : "";
    const key = params.i || params.I || currentChunkKey || "_default";
    if (params.a === "d") {
      deleteImages(params);
      return;
    }
    if (params.a && params.a !== "T" && params.a !== "t") return;
    if (params.m === "1") {
      const existing = chunks.get(key) || { params: {}, payload: "" };
      chunks.set(key, {
        params: { ...existing.params, ...params },
        payload: existing.payload + payload,
      });
      currentChunkKey = key;
      return;
    }
    const existing = chunks.get(key);
    const fullPayload = (existing?.payload || "") + payload;
    const fullParams = { ...(existing?.params || {}), ...params };
    chunks.delete(key);
    if (chunks.size === 0 || key === currentChunkKey) currentChunkKey = "_default";
    render(fullParams, fullPayload);
  }

  function processText(text: string) {
    pending += text;
    while (pending) {
      const start = pending.indexOf(START);
      if (start < 0) {
        const keep =
          pending.endsWith(ESC) || pending.endsWith(ESC + "_")
            ? pending.length - (pending.endsWith(ESC) ? 1 : 2)
            : pending.length;
        writePlain(pending.slice(0, keep));
        pending = pending.slice(keep);
        return;
      }
      writePlain(pending.slice(0, start));
      const sequence = pending.slice(start + START.length);
      const end = sequence.indexOf(END);
      if (end < 0) {
        pending = pending.slice(start);
        if (pending.length > maxSequence) {
          writePlain("\r\n\x1b[31m[RDev: kitty image sequence too large]\x1b[0m\r\n");
          pending = "";
        }
        return;
      }
      const body = sequence.slice(0, end);
      pending = sequence.slice(end + END.length);
      handleSequence(body);
    }
  }

  return {
    write(data) {
      if (typeof data === "string") {
        processText(data);
      } else if (ArrayBuffer.isView(data)) {
        processText(
          decoder.decode(new Uint8Array(data.buffer, data.byteOffset, data.byteLength), {
            stream: true,
          }),
        );
      } else {
        processText(decoder.decode(new Uint8Array(data), { stream: true }));
      }
    },
  };
}

/* ── Mobile helper keys (same set as the vanilla page) ── */

const MOBILE_KEYS: { label: string; key: string }[] = [
  { label: "Esc", key: "\x1b" },
  { label: "Tab", key: "\t" },
  { label: "Ctrl+C", key: "\x03" },
  { label: "Ctrl+D", key: "\x04" },
  { label: "↑", key: "\x1b[A" },
  { label: "↓", key: "\x1b[B" },
  { label: "←", key: "\x1b[D" },
  { label: "→", key: "\x1b[C" },
  { label: "Enter", key: "\r" },
];

/* ── Page ── */

export function SessionsPage() {
  const { t } = useI18n();
  const { open: openMobileNav } = useContext(MobileNavContext);
  const [searchParams, setSearchParams] = useSearchParams();

  const [sessions, setSessions] = useState<SessionInfo[]>([]);
  const [search, setSearch] = useState("");
  const [activeId, setActiveIdState] = useState<string | null>(
    () => searchParams.get("session"),
  );
  const [needAuth, setNeedAuth] = useState(false);
  const [authError, setAuthError] = useState("");
  const [authBusy, setAuthBusy] = useState(false);
  const [password, setPasswordState] = useState("");
  const passwordRef = useRef("");
  // State + ref move in lockstep (4 call sites); WS closures read the ref.
  const setPassword = useCallback((pw: string) => {
    passwordRef.current = pw;
    setPasswordState(pw);
  }, []);

  const tRef = useRef(t);
  tRef.current = t;
  const sessionsRef = useRef<SessionInfo[]>(sessions);
  sessionsRef.current = sessions;

  const attachedRef = useRef<string | null>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const writerRef = useRef<KittyWriter | null>(null);
  const needAuthRef = useRef(false);
  const hostRef = useRef<HTMLDivElement>(null);
  const outputQueue = useRef<ArrayBuffer[]>([]);
  const outputFlushScheduled = useRef(false);
  const resizeTimer = useRef<number | undefined>(undefined);
  const lastResize = useRef<{ rows: number; cols: number } | null>(null);
  const themeObserver = useRef<MutationObserver | null>(null);

  /* ── Session list polling (3s, same as vanilla) ── */

  useEffect(() => {
    let alive = true;
    const load = async () => {
      try {
        const next = await apiJSON<SessionInfo[] | null>("/api/sessions");
        if (!alive) return;
        const list = next ?? [];
        setSessions((prev) =>
          JSON.stringify(prev) === JSON.stringify(list) ? prev : list,
        );
      } catch (e) {
        console.error("loadSessions", e);
      }
    };
    load();
    const timer = setInterval(load, 3000);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, []);

  /* ── Output queue (rAF-batched writes, 12ms budget per frame) ── */

  const flushOutput = useCallback(() => {
    outputFlushScheduled.current = false;
    const writer = writerRef.current;
    if (!writer) {
      outputQueue.current = [];
      return;
    }
    const started = performance.now();
    while (outputQueue.current.length) {
      writer.write(outputQueue.current.shift()!);
      if (performance.now() - started > 12) {
        outputFlushScheduled.current = true;
        requestAnimationFrame(flushOutput);
        return;
      }
    }
  }, []);

  const enqueueOutput = useCallback(
    (data: ArrayBuffer) => {
      outputQueue.current.push(data);
      if (!outputFlushScheduled.current) {
        outputFlushScheduled.current = true;
        requestAnimationFrame(flushOutput);
      }
    },
    [flushOutput],
  );

  const drainOutput = useCallback(() => {
    const writer = writerRef.current;
    if (!outputQueue.current.length || !writer) return;
    const queued = outputQueue.current;
    outputQueue.current = [];
    outputFlushScheduled.current = false;
    queued.forEach((data) => writer.write(data));
  }, []);

  /* ── Send helpers ── */

  const sendData = useCallback((data: string) => {
    const ws = wsRef.current;
    if (ws && ws.readyState === WebSocket.OPEN && !needAuthRef.current) {
      ws.send(new TextEncoder().encode(data));
    }
  }, []);

  const sendResize = useCallback((rows: number, cols: number) => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN || needAuthRef.current) return;
    if (
      lastResize.current &&
      lastResize.current.rows === rows &&
      lastResize.current.cols === cols
    )
      return;
    lastResize.current = { rows, cols };
    clearTimeout(resizeTimer.current);
    resizeTimer.current = window.setTimeout(() => {
      const cur = wsRef.current;
      if (cur && cur.readyState === WebSocket.OPEN && !needAuthRef.current) {
        cur.send(JSON.stringify({ op: "resize", rows, cols }));
      }
    }, 80);
  }, []);

  /* ── Terminal / WebSocket lifecycle ── */

  const teardownTerminal = useCallback(() => {
    themeObserver.current?.disconnect();
    themeObserver.current = null;
    const ws = wsRef.current;
    if (ws) {
      wsRef.current = null;
      ws.onclose = null;
      ws.close();
    }
    clearTimeout(resizeTimer.current);
    outputQueue.current = [];
    outputFlushScheduled.current = false;
    lastResize.current = null;
    if (termRef.current) {
      termRef.current.dispose();
      termRef.current = null;
    }
    fitRef.current = null;
    writerRef.current = null;
    needAuthRef.current = false;
  }, []);

  const attach = useCallback(
    (sessionId: string): boolean => {
      const sess = sessionsRef.current.find((s) => s.id === sessionId);
      if (!sess) return false;
      const host = hostRef.current;
      if (!host) return false;

      teardownTerminal();
      attachedRef.current = sessionId;
      setNeedAuth(false);
      setAuthError("");
      setAuthBusy(false);
      setPassword(getDevicePassword(sess.clientId));

      // Build the terminal
      host.innerHTML = "";
      const term = new Terminal({
        cursorBlink: true,
        fontSize: 14,
        fontFamily: "'SF Mono','Fira Code','Cascadia Code',Menlo,Monaco,monospace",
        theme: readXtermTheme(),
        allowProposedApi: true,
      });
      const fit = new FitAddon();
      term.loadAddon(fit);
      term.loadAddon(new WebLinksAddon());
      try {
        term.loadAddon(
          new ImageAddon({
            sixelSupport: true,
            iipSupport: true,
            sixelScrolling: true,
            pixelLimit: 8192 * 8192,
            sixelSizeLimit: 16 * 1024 * 1024,
            iipSizeLimit: 16 * 1024 * 1024,
            storageLimit: 64,
            showPlaceholder: true,
          }),
        );
      } catch (e) {
        console.warn("RDev image addon disabled:", e);
      }
      term.open(host);
      writerRef.current = createKittyWriter(term);

      // Mobile-friendly input attributes (same as vanilla page)
      const textarea = host.querySelector("textarea");
      if (textarea) {
        textarea.setAttribute("inputmode", "text");
        textarea.setAttribute("enterkeyhint", "enter");
        textarea.setAttribute("autocapitalize", "off");
        textarea.setAttribute("autocomplete", "off");
        textarea.setAttribute("autocorrect", "off");
        textarea.setAttribute("spellcheck", "false");
        textarea.addEventListener("compositionend", () =>
          setTimeout(() => {
            try {
              fit.fit();
            } catch {
              /* noop */
            }
          }, 30),
        );
        textarea.addEventListener("focus", () =>
          setTimeout(() => {
            try {
              fit.fit();
            } catch {
              /* noop */
            }
          }, 80),
        );
      }

      try {
        const webgl = new WebglAddon();
        webgl.onContextLoss(() => webgl.dispose());
        term.loadAddon(webgl);
      } catch {
        /* webgl unavailable */
      }
      try {
        fit.fit();
      } catch {
        /* noop */
      }

      termRef.current = term;
      fitRef.current = fit;

      // Re-apply palette when the theme flips (.dark class on <html>)
      const observer = new MutationObserver(() => {
        term.options.theme = readXtermTheme();
      });
      observer.observe(document.documentElement, {
        attributes: true,
        attributeFilter: ["class"],
      });
      themeObserver.current = observer;

      // Connect the attach WebSocket
      const ws = new WebSocket(wsURL("/session?session=" + encodeURIComponent(sessionId)));
      ws.binaryType = "arraybuffer";
      wsRef.current = ws;

      ws.onmessage = (evt) => {
        if (evt.data instanceof ArrayBuffer) {
          needAuthRef.current = false;
          setNeedAuth(false);
          enqueueOutput(evt.data);
          return;
        }
        let msg: Record<string, unknown>;
        try {
          msg = JSON.parse(String(evt.data));
        } catch {
          return;
        }
        if (msg.op === "auth") {
          needAuthRef.current = true;
          setNeedAuth(true);
          setAuthError("");
          setAuthBusy(false);
          setPassword(getDevicePassword(sess.clientId));
        } else if (msg.op === "auth_ok") {
          if (passwordRef.current)
            rememberDevicePassword(sess.clientId, passwordRef.current);
          needAuthRef.current = false;
          setNeedAuth(false);
          setAuthError("");
          term.focus();
          try {
            fit.fit();
          } catch {
            /* noop */
          }
        } else if (msg.op === "auth_fail") {
          forgetDevicePassword(sess.clientId);
          setAuthError(
            typeof msg.message === "string" && msg.message
              ? msg.message
              : tRef.current("common.passwordWrong"),
          );
          setAuthBusy(false);
          setPassword("");
        } else if (msg.op === "exit") {
          drainOutput();
          needAuthRef.current = false;
          setNeedAuth(false);
          term.write(
            "\r\n\x1b[33m[" +
              tRef.current("sessions.exitCode", { code: Number(msg.code) }) +
              "]\x1b[0m\r\n",
          );
        } else if (msg.op === "error") {
          drainOutput();
          needAuthRef.current = false;
          setNeedAuth(false);
          term.write(
            "\r\n\x1b[31m[" +
              tRef.current("sessions.error", { message: String(msg.message ?? "") }) +
              "]\x1b[0m\r\n",
          );
        }
      };

      ws.onclose = () => {
        if (wsRef.current !== ws) return;
        drainOutput();
        term.write("\r\n\x1b[31m" + tRef.current("sessions.disconnected") + "\x1b[0m\r\n");
      };

      term.onData((data) => sendData(data));
      term.onResize(({ cols, rows }) => sendResize(rows, cols));
      return true;
    },
    [drainOutput, enqueueOutput, sendData, sendResize, setPassword, teardownTerminal],
  );

  /* Selection is synced to the ?session= search param. */
  const setActiveId = useCallback(
    (id: string | null) => {
      setActiveIdState(id);
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (id) next.set("session", id);
          else next.delete("session");
          return next;
        },
        { replace: true },
      );
    },
    [setSearchParams],
  );

  const detach = useCallback(() => {
    teardownTerminal();
    attachedRef.current = null;
    setNeedAuth(false);
    setAuthError("");
    setAuthBusy(false);
    setActiveId(null);
  }, [setActiveId, teardownTerminal]);

  /* Select a session (click in list); mirrors the vanilla attachSession guard. */
  const selectSession = useCallback(
    (sessionId: string) => {
      const ws = wsRef.current;
      if (activeId === sessionId && ws && ws.readyState !== WebSocket.CLOSED) return;
      setActiveId(sessionId);
    },
    [activeId, setActiveId],
  );

  /* Attach when the selection changes; retry once the list knows the session. */
  useEffect(() => {
    if (activeId && attachedRef.current !== activeId) attach(activeId);
  }, [activeId, sessions, attach]);

  /* Full teardown on unmount. */
  useEffect(
    () => () => {
      teardownTerminal();
      attachedRef.current = null;
    },
    [teardownTerminal],
  );

  /* ── Fit on container / viewport resize (same triggers as vanilla) ── */

  useEffect(() => {
    if (!activeId) return;
    const host = hostRef.current;
    if (!host) return;
    let timer: number | undefined;
    const schedule = (delay = 80) => {
      clearTimeout(timer);
      timer = window.setTimeout(() => {
        const fit = fitRef.current;
        if (fit)
          try {
            fit.fit();
          } catch {
            /* noop */
          }
      }, delay);
    };
    const ro = new ResizeObserver(() => schedule(80));
    ro.observe(host);
    const onWinResize = () => schedule(100);
    window.addEventListener("resize", onWinResize);
    const vv = window.visualViewport;
    if (vv) {
      vv.addEventListener("resize", onWinResize);
      vv.addEventListener("scroll", onWinResize);
    }
    return () => {
      clearTimeout(timer);
      ro.disconnect();
      window.removeEventListener("resize", onWinResize);
      if (vv) {
        vv.removeEventListener("resize", onWinResize);
        vv.removeEventListener("scroll", onWinResize);
      }
    };
  }, [activeId]);

  /* ── Auth submit ── */

  const submitAuth = useCallback(() => {
    const ws = wsRef.current;
    const pw = passwordRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN || !pw) return;
    setAuthBusy(true);
    ws.send(JSON.stringify({ op: "auth", password: pw }));
  }, []);

  /* Auto-submit a remembered password once per auth prompt (vanilla parity). */
  const autoSubmitted = useRef(false);
  useEffect(() => {
    if (needAuth) {
      if (!autoSubmitted.current && passwordRef.current) {
        autoSubmitted.current = true;
        submitAuth();
      }
    } else {
      autoSubmitted.current = false;
    }
  }, [needAuth, submitAuth]);

  /* ── Render ── */

  const q = search.toLowerCase();
  const filtered = sessions.filter(
    (s) =>
      !q ||
      s.clientId.toLowerCase().includes(q) ||
      s.id.toLowerCase().includes(q) ||
      (s.command || "").toLowerCase().includes(q),
  );

  const attachedInfo = sessions.find((s) => s.id === activeId) || null;

  const timeAgo = (iso: string): string => {
    if (!iso) return "";
    const d = new Date(iso);
    const s = Math.floor((Date.now() - d.getTime()) / 1000);
    if (s < 60) return t("common.justNow");
    if (s < 3600) return t("common.minutesAgo", { n: Math.floor(s / 60) });
    if (s < 86400) return t("common.hoursAgo", { n: Math.floor(s / 3600) });
    return t("common.daysAgo", { n: Math.floor(s / 86400) });
  };

  return (
    <div className="flex h-full min-h-0 flex-col min-[781px]:flex-row">
      {/* ── Session list panel ── */}
      <div className="flex max-h-[38dvh] w-full shrink-0 flex-col border-b border-border-seam bg-card min-[781px]:max-h-none min-[781px]:w-[360px] min-[781px]:min-w-[260px] min-[781px]:border-r min-[781px]:border-b-0">
        <div className="flex items-center gap-2 border-b border-border-seam px-3.5 py-3">
          <button
            type="button"
            aria-label={t("nav.label")}
            onClick={() => openMobileNav()}
            className="-ml-1 flex size-7 shrink-0 cursor-pointer items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground md:hidden"
          >
            <Icon name="AlignLeft" className="size-4" />
          </button>
          <Icon name="ComputerTerminal01" className="size-4 text-muted-foreground" />
          <h2 className="text-sm font-semibold">{t("sessions.active")}</h2>
          <span className="rounded-full bg-surface-recessed px-2 py-0.5 text-2xs text-muted-foreground">
            {sessions.length}
          </span>
        </div>
        <div className="border-b border-border-seam px-3.5 py-2">
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t("sessions.searchPlaceholder")}
            className="h-8 text-xs"
          />
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">
          {!filtered.length ? (
            <div className="px-4 py-12 text-center text-sm text-muted-foreground">
              <Icon name="Inbox" className="mx-auto mb-3 size-10 opacity-30" />
              {t("sessions.noActive")}
            </div>
          ) : (
            filtered.map((s) => {
              const isActive = s.id === activeId;
              const cmdText =
                s.type === "shell"
                  ? t("sessions.interactiveShell")
                  : s.type === "sftp"
                    ? t("sessions.sftpSession")
                    : s.command || t("sessions.noCommand");
              return (
                <div
                  key={s.id}
                  onClick={() => selectSession(s.id)}
                  className={cn(
                    "cursor-pointer border-b border-border-seam px-3.5 py-2.5 transition-colors hover:bg-state-hover",
                    isActive && "border-l-[3px] border-l-primary bg-surface-selected",
                  )}
                >
                  <div className="mb-1 flex items-center gap-2">
                    <span
                      className={cn(
                        "rounded px-1.5 py-px text-2xs font-medium",
                        s.type === "shell" && "bg-success/15 text-success",
                        s.type === "sftp" && "bg-timeline-accent/15 text-timeline-accent",
                        s.type !== "shell" && s.type !== "sftp" && "bg-primary/15 text-primary",
                      )}
                    >
                      {s.type.toUpperCase()}
                    </span>
                    <span className="truncate text-xs font-medium">{s.clientId}</span>
                    <span className="shrink-0 font-mono text-2xs text-muted-foreground">
                      {s.id.slice(0, 8)}…
                    </span>
                  </div>
                  <div className="truncate text-2xs text-muted-foreground">{cmdText}</div>
                  <div className="mt-1 flex flex-wrap gap-2.5 text-2xs text-muted-foreground">
                    <span>
                      {s.pty ? s.term || "xterm" : t("sessions.pipe")} {s.cols}×{s.rows}
                    </span>
                    <span>{timeAgo(s.createdAt)}</span>
                    {s.observers > 0 && (
                      <span className="inline-flex items-center gap-1 text-warning-text">
                        <Icon name="Monitor" className="size-3" />
                        {s.observers}
                      </span>
                    )}
                  </div>
                </div>
              );
            })
          )}
        </div>
      </div>

      {/* ── Attach panel ── */}
      {activeId ? (
        <div className="flex min-w-0 flex-1 flex-col">
          <div className="flex shrink-0 flex-wrap items-center gap-2.5 border-b border-border-seam bg-card px-3 py-2">
            <span className="text-xs whitespace-nowrap text-muted-foreground">
              {attachedInfo && (
                <>
                  <b className="font-medium text-foreground">{attachedInfo.clientId}</b>
                  {" · "}
                  {attachedInfo.type.toUpperCase()}
                  {" · "}
                  {attachedInfo.command || t("sessions.shell")}
                </>
              )}
            </span>
            <span className="rounded bg-success/15 px-2 py-0.5 text-2xs font-semibold text-success">
              {t("sessions.writable")}
            </span>
            <div className="flex-1" />
            <Button variant="destructive" size="sm" onClick={detach}>
              <Icon name="X" />
              {t("sessions.detach")}
            </Button>
          </div>
          <div
            className="relative min-h-0 flex-1 touch-manipulation overflow-hidden p-1"
            onPointerDown={() => termRef.current?.focus()}
          >
            <div ref={hostRef} className="absolute inset-1 [&_.xterm]:h-full" />
            {needAuth && (
              <div className="absolute inset-0 z-10 flex items-center justify-center bg-background/90">
                <div className="w-[min(92vw,340px)] rounded-2xl border border-border bg-card p-6 text-center">
                  <h3 className="mb-2 inline-flex items-center gap-1.5 text-base font-medium text-primary">
                    <Icon name="Lock" className="size-4" />
                    {t("sessions.authTitle")}
                  </h3>
                  <p className="mb-4 text-sm text-muted-foreground">
                    {t("sessions.authDesc", { id: attachedInfo?.clientId ?? "" })}
                  </p>
                  <form
                    onSubmit={(e) => {
                      e.preventDefault();
                      submitAuth();
                    }}
                  >
                    <Input
                      type="password"
                      autoFocus
                      value={password}
                      placeholder={t("terminal.passwordPlaceholder")}
                      autoComplete="current-password"
                      onChange={(e) => setPassword(e.target.value)}
                      className="mb-3"
                    />
                    <Button
                      type="submit"
                      className="w-full"
                      disabled={authBusy || !password}
                    >
                      {authBusy ? t("common.connecting") : t("common.connect")}
                    </Button>
                  </form>
                  <div className="mt-2 min-h-[1.2em] text-xs text-destructive-text">
                    {authError}
                  </div>
                </div>
              </div>
            )}
          </div>
          {/* Mobile helper keys */}
          <div className="flex shrink-0 gap-1.5 overflow-x-auto border-t border-border-seam bg-card px-2 py-1.5 min-[781px]:hidden">
            {MOBILE_KEYS.map((k) => (
              <button
                key={k.label}
                type="button"
                className="min-h-[34px] min-w-[44px] shrink-0 rounded-md border border-border bg-background px-2.5 py-1.5 text-xs text-foreground"
                onClick={() => {
                  sendData(k.key);
                  termRef.current?.focus();
                  setTimeout(() => {
                    const fit = fitRef.current;
                    if (fit)
                      try {
                        fit.fit();
                      } catch {
                        /* noop */
                      }
                  }, 50);
                }}
              >
                {k.label}
              </button>
            ))}
          </div>
        </div>
      ) : (
        <div className="flex flex-1 items-center justify-center text-muted-foreground">
          <div className="text-center">
            <Icon
              name="ComputerTerminal01"
              className="mx-auto mb-4 size-16 opacity-20"
            />
            <div className="text-sm">{t("sessions.emptyHint")}</div>
          </div>
        </div>
      )}
    </div>
  );
}
