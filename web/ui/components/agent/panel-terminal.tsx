/* PanelTerminal: one device shell session inside an agent right-panel tab
 * (bb usePanelTerminals, scoped port). Self-contained xterm + /terminal WS
 * client mirroring the Terminal page's protocol (auth / auth_ok / auth_fail
 * / exit / error control frames, binary I/O, resize). The tab strip keeps
 * every terminal tab mounted (hidden when inactive) so sessions survive tab
 * switches; a page reload or panel unmount ends the session and the next
 * mount reconnects with a fresh shell. */
import { useCallback, useEffect, useRef, useState } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { ImageAddon } from "@xterm/addon-image";
import "@xterm/xterm/css/xterm.css";
import {
  wsURL,
  rememberDevicePassword,
  forgetDevicePassword,
} from "../../lib/api";
import {
  createKittyImageWriter,
  hasTerminalImageSequence,
  type KittyImageWriter,
} from "../../lib/terminal-images";
import { readXtermTheme } from "../../lib/xterm-theme";
import { TabAuthOverlay } from "../../pages/Terminal";
import { Button } from "../button";
import { Icon } from "../icon";
import type { Translator } from "./types";

const inputEncoder = new TextEncoder();

type SessionStatus = "connecting" | "authing" | "connected" | "disconnected";

export function PanelTerminal({
  device,
  visible,
  t,
}: {
  device: string;
  /* false while another panel tab is active: the xterm stays mounted but is
   * display:none — fitting is deferred until the tab becomes visible. */
  visible: boolean;
  t: Translator;
}) {
  const hostRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<XTerm | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const imageRef = useRef<KittyImageWriter | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const decoderRef = useRef<TextDecoder>(new TextDecoder());
  const pendingPwRef = useRef("");
  const needAuthRef = useRef(false);
  const [status, setStatus] = useState<SessionStatus>("connecting");
  const [authOpen, setAuthOpen] = useState(false);
  const [authError, setAuthError] = useState("");
  const [authRound, setAuthRound] = useState(0);
  const statusRef = useRef(status);
  statusRef.current = status;

  const connect = useCallback(() => {
    const term = termRef.current;
    if (!term) return;
    const prev = wsRef.current;
    if (prev) {
      prev.onopen = null;
      prev.onmessage = null;
      prev.onclose = null;
      prev.onerror = null;
      prev.close();
      wsRef.current = null;
    }
    decoderRef.current = new TextDecoder();
    pendingPwRef.current = "";
    needAuthRef.current = false;
    term.reset();
    setStatus("connecting");
    setAuthOpen(false);
    setAuthError("");

    const ws = new WebSocket(
      wsURL("/terminal?device=" + encodeURIComponent(device)),
    );
    ws.binaryType = "arraybuffer";
    wsRef.current = ws;

    const writeBytes = (buf: ArrayBuffer) => {
      setAuthOpen(false);
      if (statusRef.current !== "connected") setStatus("connected");
      const bytes = new Uint8Array(buf);
      if (hasTerminalImageSequence(bytes)) {
        imageRef.current?.write(bytes);
      } else {
        term.write(decoderRef.current.decode(bytes, { stream: true }));
      }
    };

    ws.onopen = () => {
      if (wsRef.current !== ws) return;
      setStatus((s) => (s === "connected" ? s : "authing"));
    };
    ws.onmessage = (evt) => {
      if (wsRef.current !== ws) return;
      if (evt.data instanceof ArrayBuffer) {
        writeBytes(evt.data);
        return;
      }
      if (evt.data instanceof Blob) {
        void evt.data.arrayBuffer().then((buf) => {
          if (wsRef.current === ws) writeBytes(buf);
        });
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
        setStatus("authing");
        setAuthError("");
        setAuthRound((r) => r + 1);
        setAuthOpen(true);
      } else if (msg.op === "auth_ok") {
        if (pendingPwRef.current) {
          rememberDevicePassword(device, pendingPwRef.current);
          pendingPwRef.current = "";
        }
        needAuthRef.current = false;
        setAuthOpen(false);
        setAuthError("");
        setStatus("connected");
        term.focus();
      } else if (msg.op === "auth_fail") {
        forgetDevicePassword(device);
        pendingPwRef.current = "";
        setAuthError(
          typeof msg.message === "string" && msg.message
            ? msg.message
            : t("common.passwordWrong"),
        );
      } else if (msg.op === "exit") {
        setAuthOpen(false);
        term.write(
          "\r\n\x1b[33m[" +
            t("terminal.exitCode", { code: String(msg.code ?? "") }) +
            "]\x1b[0m\r\n",
        );
        setStatus("disconnected");
        ws.close();
        if (wsRef.current === ws) wsRef.current = null;
      } else if (msg.op === "error") {
        setAuthOpen(false);
        term.write(
          "\r\n\x1b[31m[" +
            t("terminal.error", { message: String(msg.message ?? "") }) +
            "]\x1b[0m\r\n",
        );
        setStatus("disconnected");
      }
    };
    ws.onclose = () => {
      setAuthOpen(false);
      if (wsRef.current === ws) wsRef.current = null;
      if (statusRef.current !== "disconnected") {
        setStatus("disconnected");
        term.write("\r\n\x1b[31m[" + t("terminal.closed") + "]\x1b[0m\r\n");
      }
    };
    ws.onerror = () => {
      setAuthOpen(false);
      setStatus("disconnected");
    };
  }, [device, t]);

  /* Mount xterm once; bind input/resize; connect; tear down on unmount. */
  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const term = new XTerm({
      cursorBlink: true,
      fontSize: 12,
      convertEol: true,
      fontFamily:
        "'SF Mono','Fira Code','Cascadia Code',Menlo,Monaco,monospace",
      letterSpacing: 0,
      theme: readXtermTheme(),
      allowProposedApi: true,
    });
    const fitAddon = new FitAddon();
    term.loadAddon(fitAddon);
    term.loadAddon(new WebLinksAddon());
    try {
      term.loadAddon(
        new ImageAddon({
          sixelSupport: true,
          iipSupport: true,
          sixelScrolling: true,
        }),
      );
    } catch {
      /* image protocols optional */
    }
    term.open(host);
    termRef.current = term;
    fitRef.current = fitAddon;
    imageRef.current = createKittyImageWriter(term);

    term.onData((data) => {
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN && !needAuthRef.current) {
        ws.send(inputEncoder.encode(data));
      }
    });
    term.onResize(({ cols, rows }) => {
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN && !needAuthRef.current) {
        ws.send(JSON.stringify({ op: "resize", rows, cols }));
      }
    });

    /* Re-read xterm colors when the app theme flips (.dark on <html>). */
    const themeObserver = new MutationObserver(() => {
      term.options.theme = readXtermTheme();
    });
    themeObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["class"],
    });

    connect();
    return () => {
      themeObserver.disconnect();
      const ws = wsRef.current;
      if (ws) {
        ws.onopen = null;
        ws.onmessage = null;
        ws.onclose = null;
        ws.onerror = null;
        ws.close();
        wsRef.current = null;
      }
      term.dispose();
      termRef.current = null;
      fitRef.current = null;
      imageRef.current = null;
    };
  }, [connect]);

  /* Fit when the tab becomes visible and on container resizes. */
  useEffect(() => {
    const host = hostRef.current;
    if (!host || !visible) return;
    const fit = () => {
      try {
        fitRef.current?.fit();
      } catch {
        /* not laid out yet */
      }
    };
    fit();
    const raf = requestAnimationFrame(fit);
    const observer = new ResizeObserver(fit);
    observer.observe(host);
    return () => {
      cancelAnimationFrame(raf);
      observer.disconnect();
    };
  }, [visible]);

  const submitAuth = (password: string) => {
    if (!password) return;
    pendingPwRef.current = password;
    const ws = wsRef.current;
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ op: "auth", password }));
    }
  };

  return (
    <div className="relative flex h-full min-h-0 flex-col bg-sidebar">
      <div
        ref={hostRef}
        className="min-h-0 w-full flex-1 overflow-hidden p-2"
      />
      {status === "disconnected" && (
        <div className="absolute top-2 right-2 z-10 flex items-center gap-2 rounded-md border border-border bg-popover px-2 py-1 shadow-sm">
          <span className="text-xs text-muted-foreground">
            {t("agent.terminalClosed")}
          </span>
          <Button
            size="sm"
            variant="outline"
            className="h-6 px-2 text-xs"
            onClick={connect}
          >
            <Icon name="RotateCcw" className="size-3" />
            {t("agent.terminalReconnect")}
          </Button>
        </div>
      )}
      {authOpen && (
        <TabAuthOverlay
          key={authRound}
          deviceId={device}
          error={authError}
          onSubmit={submitAuth}
          t={t}
        />
      )}
      {status === "connecting" && (
        <div className="absolute inset-0 z-10 flex items-center justify-center bg-background/60">
          <Icon name="Spinner" className="size-4 animate-spin text-muted-foreground" />
        </div>
      )}
    </div>
  );
}
