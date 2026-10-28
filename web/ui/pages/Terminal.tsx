import {
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { useSearchParams } from "react-router-dom";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { WebglAddon } from "@xterm/addon-webgl";
import { ImageAddon } from "@xterm/addon-image";
import "@xterm/xterm/css/xterm.css";
import { useI18n } from "../lib/i18n";
import { useDevices } from "../lib/devices";
import {
  wsURL,
  getDevicePassword,
  rememberDevicePassword,
  forgetDevicePassword,
} from "../lib/api";
import {
  createKittyImageWriter,
  hasTerminalImageSequence,
  type KittyImageWriter,
} from "../lib/terminal-images";
import { readXtermTheme } from "../lib/xterm-theme";
import { cn } from "../lib/utils";
import { Button } from "../components/button";
import { Input } from "../components/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../components/select";
import { Icon } from "../components/icon";
import { OsIcon } from "../components/os-icon";
import { SplitPageShell } from "../components/split-layout";
import {
  clampSplitPairFraction,
  countPanes,
  listPanes,
  parseLayout,
  removePane,
  resizeSplit,
  serializeLayout,
  setFocus,
  singlePaneLayout,
  splitPane,
  type PaneNode,
  type SplitLayout,
} from "../lib/split-layout";
import { SplitContainer } from "../components/agent/split-panes";
import { DeviceDropTarget } from "../components/device-rail";

/* ── Tab model ── */

type TabStatus = "disconnected" | "connecting" | "authing" | "connected";

interface TermTab {
  id: number;
  deviceId: string;
  ws: WebSocket | null;
  term: XTerm;
  fitAddon: FitAddon;
  imageWriter: KittyImageWriter;
  decoder: TextDecoder;
  status: TabStatus;
  needAuth: boolean;
  authOpen: boolean;
  authRound: number;
  pendingPassword: string;
  opened: boolean;
  pane: HTMLElement | null;
  handlersBound: boolean;
}

interface TabMeta {
  id: number;
  deviceId: string;
  status: TabStatus;
  authOpen: boolean;
  authError: string;
  /** Bumped on every `auth` op so the overlay remounts (and re-auto-submits a
   * remembered password), matching the legacy page rebuilding its DOM box. */
  authRound: number;
}

/** Mirrors the legacy localStorage flag: webgl renderer is opt-in. */
const WEBGL_KEY = "rdevTerminal.webgl";
const SPLIT_LAYOUT_KEY = "rdev.terminal.splitLayout";
const MAX_TERMINAL_PANES = 8;

/* One pane's tab strip: ordered tab ids plus which one is shown. */
interface PaneTabs {
  tabs: number[];
  active: number | null;
}

const terminalInputEncoder = new TextEncoder();

const MOBILE_KEYS: { label: string; key: string }[] = [
  { label: "Esc", key: "\u001b" },
  { label: "Tab", key: "\t" },
  { label: "Ctrl+C", key: "\u0003" },
  { label: "Ctrl+D", key: "\u0004" },
  { label: "↑", key: "\u001b[A" },
  { label: "↓", key: "\u001b[B" },
  { label: "←", key: "\u001b[D" },
  { label: "→", key: "\u001b[C" },
  { label: "Enter", key: "\r" },
];

/* ── Layout helpers (ported from terminal.html) ── */

function refreshTerminalLayout(tab: TermTab) {
  if (!tab || !tab.opened) return;
  const fit = () => {
    try {
      tab.fitAddon.fit();
    } catch {
      /* not laid out yet */
    }
    try {
      tab.term.clearTextureAtlas();
    } catch {
      /* canvas renderer */
    }
    try {
      tab.term.refresh(0, Math.max(0, tab.term.rows - 1));
    } catch {
      /* ignore */
    }
  };
  fit();
  requestAnimationFrame(fit);
}

function doResize(tab: TermTab | undefined) {
  if (!tab) return;
  const pane = tab.pane;
  if (pane && (pane.clientWidth === 0 || pane.clientHeight === 0)) return;
  refreshTerminalLayout(tab);
}

function prepareTerminalInput(tab: TermTab, resize: () => void) {
  const textarea = tab.pane?.querySelector("textarea");
  if (!textarea) return;
  textarea.setAttribute("inputmode", "text");
  textarea.setAttribute("enterkeyhint", "enter");
  textarea.setAttribute("autocapitalize", "off");
  textarea.setAttribute("autocomplete", "off");
  textarea.setAttribute("autocorrect", "off");
  textarea.setAttribute("spellcheck", "false");
  textarea.addEventListener("compositionend", () => setTimeout(resize, 30));
  textarea.addEventListener("focus", () => setTimeout(resize, 80));
}

function sendTerminalData(tab: TermTab | undefined, data: string) {
  if (tab && tab.ws && tab.ws.readyState === WebSocket.OPEN && !tab.needAuth) {
    tab.ws.send(terminalInputEncoder.encode(data));
  }
}

/* ── Page ── */

export function TerminalPage() {
  const { t } = useI18n();
  const { devices } = useDevices();
  const [searchParams] = useSearchParams();

  const [metas, setMetas] = useState<TabMeta[]>([]);

  const tabsRef = useRef(new Map<number, TermTab>());
  const counterRef = useRef(0);
  const resizeTimer = useRef<number | undefined>(undefined);

  /* Panes come from the shared split-tree (same system as the agent page):
   * each pane owns a tab strip; only the active tab's xterm is shown (a DOM
   * node can only live in one place), the rest stay connected in the
   * hidden parking container. The layout persists in localStorage. */
  const [splitLayout, setSplitLayout] = useState<SplitLayout>(
    () => parseLayout(localStorage.getItem(SPLIT_LAYOUT_KEY)) ?? singlePaneLayout(),
  );
  const splitLayoutRef = useRef(splitLayout);
  splitLayoutRef.current = splitLayout;
  useEffect(() => {
    localStorage.setItem(SPLIT_LAYOUT_KEY, serializeLayout(splitLayout));
  }, [splitLayout]);
  const [paneTabs, setPaneTabs] = useState<Map<string, PaneTabs>>(new Map());
  const focusedPane = splitLayout.focusedPaneId;
  const paneTabsRef = useRef(paneTabs);
  paneTabsRef.current = paneTabs;
  const focusedLeafRef = useRef(focusedPane);
  focusedLeafRef.current = focusedPane;
  const activeTabId = paneTabs.get(focusedPane)?.active ?? null;
  const activeTabIdRef = useRef<number | null>(null);
  activeTabIdRef.current = activeTabId;
  const leafHostsRef = useRef(new Map<string, HTMLDivElement>());
  const parkingRef = useRef<HTMLDivElement>(null);

  const patchMeta = useCallback(
    (id: number, patch: Partial<Omit<TabMeta, "id">>) => {
      setMetas((prev) => {
        const idx = prev.findIndex((m) => m.id === id);
        if (idx === -1) return prev;
        const cur = prev[idx];
        const nextMeta = { ...cur, ...patch };
        if (
          cur.status === nextMeta.status &&
          cur.authOpen === nextMeta.authOpen &&
          cur.authError === nextMeta.authError &&
          cur.authRound === nextMeta.authRound &&
          cur.deviceId === nextMeta.deviceId
        ) {
          return prev;
        }
        const next = prev.slice();
        next[idx] = nextMeta;
        return next;
      });
    },
    [],
  );

  const removeAuthOverlay = useCallback(
    (tab: TermTab) => {
      if (!tab.authOpen) return;
      tab.authOpen = false;
      patchMeta(tab.id, { authOpen: false });
    },
    [patchMeta],
  );

  const handleTerminalBytes = useCallback(
    (tab: TermTab, data: ArrayBuffer) => {
      removeAuthOverlay(tab);
      if (tab.status !== "connected") {
        tab.status = "connected";
        patchMeta(tab.id, { status: "connected" });
      }
      const bytes = new Uint8Array(data);
      if (hasTerminalImageSequence(bytes)) {
        tab.imageWriter.write(bytes);
      } else {
        tab.term.write(tab.decoder.decode(bytes, { stream: true }));
      }
    },
    [patchMeta, removeAuthOverlay],
  );

  const connectTab = useCallback(
    (tab: TermTab) => {
      if (tab.ws) {
        tab.ws.onopen = null;
        tab.ws.onmessage = null;
        tab.ws.onclose = null;
        tab.ws.onerror = null;
        tab.ws.close();
        tab.ws = null;
      }
      tab.decoder = new TextDecoder();
      tab.status = "connecting";
      tab.needAuth = false;
      tab.authOpen = false;
      tab.pendingPassword = "";
      tab.term.reset();
      patchMeta(tab.id, { status: "connecting", authOpen: false, authError: "" });

      const ws = new WebSocket(
        wsURL("/terminal?device=" + encodeURIComponent(tab.deviceId)),
      );
      tab.ws = ws;
      ws.binaryType = "arraybuffer";

      ws.onopen = () => {
        if (tab.ws !== ws) return;
        tab.status = "authing";
        patchMeta(tab.id, { status: "authing" });
      };

      ws.onmessage = (evt) => {
        if (tab.ws !== ws) return;
        if (evt.data instanceof ArrayBuffer) {
          handleTerminalBytes(tab, evt.data);
          return;
        }
        if (evt.data instanceof Blob) {
          evt.data.arrayBuffer().then((buf) => {
            if (tab.ws === ws) handleTerminalBytes(tab, buf);
          });
          return;
        }
        // Text = JSON control
        let msg: Record<string, unknown>;
        try {
          msg = JSON.parse(String(evt.data));
        } catch {
          return;
        }
        if (msg.op === "auth") {
          tab.needAuth = true;
          tab.status = "authing";
          tab.authOpen = true;
          patchMeta(tab.id, {
            status: "authing",
            authOpen: true,
            authError: "",
            authRound: ++tab.authRound,
          });
        } else if (msg.op === "auth_ok") {
          if (tab.pendingPassword) {
            rememberDevicePassword(tab.deviceId, tab.pendingPassword);
            tab.pendingPassword = "";
          }
          tab.needAuth = false;
          tab.authOpen = false;
          tab.status = "connected";
          patchMeta(tab.id, { status: "connected", authOpen: false, authError: "" });
          tab.term.focus();
          doResize(tab);
        } else if (msg.op === "auth_fail") {
          forgetDevicePassword(tab.deviceId);
          tab.pendingPassword = "";
          patchMeta(tab.id, {
            authError:
              typeof msg.message === "string" && msg.message
                ? msg.message
                : t("common.passwordWrong"),
          });
        } else if (msg.op === "exit") {
          removeAuthOverlay(tab);
          tab.term.write(
            "\r\n\x1b[33m[" +
              t("terminal.exitCode", { code: String(msg.code ?? "") }) +
              "]\x1b[0m\r\n",
          );
          tab.status = "disconnected";
          patchMeta(tab.id, { status: "disconnected" });
          ws.close();
          if (tab.ws === ws) tab.ws = null;
        } else if (msg.op === "error") {
          removeAuthOverlay(tab);
          tab.term.write(
            "\r\n\x1b[31m[" +
              t("terminal.error", { message: String(msg.message ?? "") }) +
              "]\x1b[0m\r\n",
          );
          tab.status = "disconnected";
          patchMeta(tab.id, { status: "disconnected" });
        }
      };

      ws.onclose = () => {
        removeAuthOverlay(tab);
        if (tab.ws === ws) tab.ws = null;
        if (tab.status !== "disconnected") {
          tab.status = "disconnected";
          tab.term.write(
            "\r\n\x1b[31m[" + t("terminal.closed") + "]\x1b[0m\r\n",
          );
          patchMeta(tab.id, { status: "disconnected" });
        }
      };
      ws.onerror = () => {
        removeAuthOverlay(tab);
        tab.status = "disconnected";
        patchMeta(tab.id, { status: "disconnected" });
      };

      // Only send input when authenticated and connected
      if (!tab.handlersBound) {
        tab.handlersBound = true;
        tab.term.onData((data) => {
          sendTerminalData(tab, data);
        });
        tab.term.onResize(({ cols, rows }) => {
          if (tab.ws && tab.ws.readyState === WebSocket.OPEN && !tab.needAuth) {
            tab.ws.send(JSON.stringify({ op: "resize", rows, cols }));
          }
        });
      }
    },
    [handleTerminalBytes, patchMeta, removeAuthOverlay, t],
  );

  const createTab = useCallback(
    (deviceId: string, paneId?: string): number => {
      const id = ++counterRef.current;
      /* Pane host created imperatively (not React-rendered) so the node can
       * be re-parented between the parking container and split leaves
       * without React remounting it — the xterm instance survives moves. */
      const pane = document.createElement("div");
      pane.dataset.pane = String(id);
      pane.className =
        "absolute inset-0 touch-manipulation p-2 max-[720px]:p-1 [&>.xterm]:h-full [&>.xterm]:w-full [&_.xterm-screen]:box-content [&_.xterm-viewport]:box-content [&_.xterm-screen_canvas]:block invisible pointer-events-none";
      const term = new XTerm({
        cursorBlink: true,
        fontSize: 14,
        convertEol: true,
        fontFamily:
          "'SF Mono','Fira Code','Cascadia Code',Menlo,Monaco,monospace",
        lineHeight: 1.12,
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
            pixelLimit: 8192 * 8192,
            sixelSizeLimit: 16 * 1024 * 1024,
            iipSizeLimit: 16 * 1024 * 1024,
            storageLimit: 64,
            showPlaceholder: false,
          }),
        );
      } catch (e) {
        console.warn("RDev image addon disabled:", e);
      }

      const tab: TermTab = {
        id,
        deviceId,
        ws: null,
        term,
        fitAddon,
        imageWriter: createKittyImageWriter(term),
        decoder: new TextDecoder(),
        status: "disconnected",
        needAuth: false,
        authOpen: false,
        authRound: 0,
        pendingPassword: "",
        opened: false,
        pane,
        handlersBound: false,
      };
      tabsRef.current.set(id, tab);
      setMetas((prev) => [
        ...prev,
        {
          id,
          deviceId,
          status: "disconnected",
          authOpen: false,
          authError: "",
          authRound: 0,
        },
      ]);
      const target = paneId ?? focusedLeafRef.current;
      setPaneTabs((prev) => {
        const next = new Map(prev);
        const pane = next.get(target) ?? { tabs: [], active: null };
        next.set(target, { tabs: [...pane.tabs, id], active: id });
        return next;
      });
      return id;
    },
    [],
  );

  /* Activate a tab within its pane; the pane takes focus (clicking a tab in
   * another pane is also how that pane gets focused). */
  const activateTab = useCallback((paneId: string, id: number) => {
    setSplitLayout((l) => setFocus(l, paneId));
    setPaneTabs((prev) => {
      const pane = prev.get(paneId);
      if (!pane || pane.active === id || !pane.tabs.includes(id)) return prev;
      const next = new Map(prev);
      next.set(paneId, { ...pane, active: id });
      return next;
    });
    const tab = tabsRef.current.get(id);
    if (tab) {
      refreshTerminalLayout(tab);
      setTimeout(() => tab.term.focus(), 0);
    }
  }, []);

  const closeTab = useCallback(
    (id: number) => {
      const idx = metas.findIndex((m) => m.id === id);
      if (idx === -1) return;
      const tab = tabsRef.current.get(id);
      if (tab) {
        if (tab.ws) {
          tab.ws.onopen = null;
          tab.ws.onmessage = null;
          tab.ws.onclose = null;
          tab.ws.onerror = null;
          tab.ws.close();
          tab.ws = null;
        }
        tab.term.dispose();
        tab.pane?.remove();
        tabsRef.current.delete(id);
      }
      const next = metas.filter((m) => m.id !== id);
      setMetas(next);
      /* Remove from its pane strip: activate the sibling tab. Closing the
       * last tab immediately opens a fresh one on the same device — a pane
       * is never left empty. */
      const paneEntry = [...paneTabsRef.current.entries()].find(([, p]) =>
        p.tabs.includes(id),
      );
      if (paneEntry) {
        const [paneId, pane] = paneEntry;
        const idxInPane = pane.tabs.indexOf(id);
        const remaining = pane.tabs.filter((t) => t !== id);
        if (remaining.length === 0) {
          setPaneTabs((prev) => {
            const nextMap = new Map(prev);
            nextMap.delete(paneId);
            return nextMap;
          });
          /* Last tab gone: with multiple panes the pane itself closes; a
           * lone pane immediately opens a fresh tab on the same device — a
           * pane is never left empty. */
          if (countPanes(splitLayoutRef.current.root) > 1) {
            setSplitLayout((l) => removePane(l, paneId));
          } else {
            createTab(metas[idx].deviceId, paneId);
          }
          return;
        }
        const active =
          pane.active === id
            ? (remaining[Math.min(idxInPane, remaining.length - 1)] ?? null)
            : pane.active;
        setPaneTabs((prev) => {
          const nextMap = new Map(prev);
          nextMap.set(paneId, { tabs: remaining, active });
          return nextMap;
        });
        if (pane.active === id && focusedLeafRef.current === paneId) {
          const tab2 = active != null ? tabsRef.current.get(active) : undefined;
          if (tab2) setTimeout(() => tab2.term.focus(), 0);
        }
      }
    },
    [metas, createTab],
  );

  /* ── Split-tree pane management ── */

  const focusLeaf = useCallback((paneId: string) => {
    setSplitLayout((l) => setFocus(l, paneId));
    const tabId = paneTabsRef.current.get(paneId)?.active ?? null;
    if (tabId != null) {
      const tab = tabsRef.current.get(tabId);
      if (tab) setTimeout(() => tab.term.focus(), 0);
    }
  }, []);

  /* Rail drag-and-drop: the dropped device opens as a new tab in that
   * pane's strip. */
  const dropDeviceOnLeaf = useCallback(
    (paneId: string, deviceId: string) => {
      createTab(deviceId, paneId);
      setSplitLayout((l) => setFocus(l, paneId));
    },
    [createTab],
  );

  const handleLeafResize = useCallback((paneId: string) => {
    const tabId = paneTabsRef.current.get(paneId)?.active ?? null;
    const tab = tabId != null ? tabsRef.current.get(tabId) : undefined;
    if (tab) doResize(tab);
  }, []);

  /* Split a pane (agent-page style): the new pane immediately opens a tab
   * on the source pane's current device — no empty pane is ever shown. */
  const splitCurrentPane = useCallback(
    (paneId: string, side: "right" | "bottom") => {
      const l = splitLayoutRef.current;
      if (countPanes(l.root) >= MAX_TERMINAL_PANES) return;
      const before = new Set(listPanes(l.root).map((p) => p.paneId));
      const next = splitPane(l, paneId, side, null);
      const added = listPanes(next.root).find((p) => !before.has(p.paneId));
      setSplitLayout(
        added ? { ...next, focusedPaneId: added.paneId } : next,
      );
      if (added) {
        const srcActive = paneTabsRef.current.get(paneId)?.active ?? null;
        const deviceId =
          (srcActive != null
            ? tabsRef.current.get(srcActive)?.deviceId
            : undefined) ?? devices[0]?.id;
        if (deviceId) createTab(deviceId, added.paneId);
      }
    },
    [createTab, devices],
  );

  /* Close a pane outright: every tab in it is disposed, the pane leaves
   * the tree. Disposal is inlined (not a closeTab loop) because batched
   * updates would race on the shared pane map. */
  const closePane = useCallback((paneId: string) => {
    if (countPanes(splitLayoutRef.current.root) <= 1) return;
    const pane = paneTabsRef.current.get(paneId);
    if (!pane) return;
    for (const id of pane.tabs) {
      const tab = tabsRef.current.get(id);
      if (tab) {
        if (tab.ws) {
          tab.ws.onopen = null;
          tab.ws.onmessage = null;
          tab.ws.onclose = null;
          tab.ws.onerror = null;
          tab.ws.close();
          tab.ws = null;
        }
        tab.term.dispose();
        tab.pane?.remove();
        tabsRef.current.delete(id);
      }
    }
    const ids = new Set(pane.tabs);
    setMetas((prev) => prev.filter((m) => !ids.has(m.id)));
    setPaneTabs((prev) => {
      const next = new Map(prev);
      next.delete(paneId);
      return next;
    });
    setSplitLayout((l) => removePane(l, paneId));
  }, []);

  /* Re-parent pane DOM nodes: shown tabs live in their leaf host; the rest
   * stay mounted (and connected) in the hidden parking container. */
  useEffect(() => {
    const parking = parkingRef.current;
    if (!parking) return;
    const shown = new Map<number, string>();
    for (const [leafId, pane] of paneTabs) {
      if (pane.active != null) shown.set(pane.active, leafId);
    }
    for (const tab of tabsRef.current.values()) {
      const pane = tab.pane;
      if (!pane) continue;
      const leafId = shown.get(tab.id);
      const host =
        leafId != null ? leafHostsRef.current.get(leafId) : undefined;
      const target: HTMLElement = host ?? parking;
      if (pane.parentElement !== target) target.appendChild(pane);
      pane.classList.toggle("invisible", !host);
      pane.classList.toggle("pointer-events-none", !host);
      if (host) doResize(tab);
    }
  }, [paneTabs, metas, splitLayout]);

  /* Mount terminals into their panes once rendered, then connect. */
  useEffect(() => {
    for (const tab of tabsRef.current.values()) {
      if (tab.opened || !tab.pane) continue;
      tab.term.open(tab.pane);

      // Canvas is the default because it is more stable across font/DPR changes.
      if (localStorage.getItem(WEBGL_KEY) === "true") {
        try {
          const webgl = new WebglAddon();
          webgl.onContextLoss(() => {
            webgl.dispose();
          });
          tab.term.loadAddon(webgl);
        } catch {
          /* fallback to canvas is automatic */
        }
      }

      tab.opened = true;
      prepareTerminalInput(tab, () => doResize(tab));
      refreshTerminalLayout(tab);
      connectTab(tab);
      if (tab.id === activeTabIdRef.current) {
        setTimeout(() => tab.term.focus(), 0);
      }
    }
  }, [metas, connectTab]);

  /* A pane is never empty: on load, every restored pane opens a tab
   * immediately — ?device= if given, otherwise the first device once the
   * list has loaded (terminal sessions can't survive a reload, so each
   * pane reconnects fresh). */
  const preselectDone = useRef(false);
  useEffect(() => {
    if (preselectDone.current) return;
    const target = searchParams.get("device") ?? devices[0]?.id;
    if (!target) {
      if (!devices.length) return; // wait for the device list
      preselectDone.current = true;
      return;
    }
    preselectDone.current = true;
    for (const p of listPanes(splitLayoutRef.current.root)) {
      createTab(target, p.paneId);
    }
  }, [devices, searchParams, createTab]);

  /* Window / viewport resize → refit the active terminal. */
  useEffect(() => {
    const scheduleActiveResize = (delay = 80) => {
      window.clearTimeout(resizeTimer.current);
      resizeTimer.current = window.setTimeout(() => {
        const id = activeTabIdRef.current;
        const tab = id != null ? tabsRef.current.get(id) : undefined;
        if (tab) doResize(tab);
      }, delay);
    };
    const onWindowResize = () => scheduleActiveResize(100);
    window.addEventListener("resize", onWindowResize);
    const vp = window.visualViewport;
    const onVp = () => scheduleActiveResize(80);
    if (vp) {
      vp.addEventListener("resize", onVp);
      vp.addEventListener("scroll", onVp);
    }
    if (document.fonts?.ready) {
      document.fonts.ready.then(() => scheduleActiveResize(0)).catch(() => {});
    }
    return () => {
      window.removeEventListener("resize", onWindowResize);
      if (vp) {
        vp.removeEventListener("resize", onVp);
        vp.removeEventListener("scroll", onVp);
      }
      window.clearTimeout(resizeTimer.current);
    };
  }, []);

  /* Re-read xterm colors when the app theme flips (.dark on <html>). */
  useEffect(() => {
    const observer = new MutationObserver(() => {
      const theme = readXtermTheme();
      for (const tab of tabsRef.current.values()) {
        tab.term.options.theme = theme;
        refreshTerminalLayout(tab);
      }
    });
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["class"],
    });
    return () => observer.disconnect();
  }, []);

  /* Dispose everything on unmount. */
  useEffect(
    () => () => {
      for (const tab of tabsRef.current.values()) {
        if (tab.ws) {
          tab.ws.onopen = null;
          tab.ws.onmessage = null;
          tab.ws.onclose = null;
          tab.ws.onerror = null;
          tab.ws.close();
          tab.ws = null;
        }
        tab.term.dispose();
      }
      tabsRef.current.clear();
    },
    [],
  );

  const activeTab = () => {
    const id = activeTabIdRef.current;
    return id != null ? tabsRef.current.get(id) : undefined;
  };

  const submitAuth = (tabId: number, password: string) => {
    const tab = tabsRef.current.get(tabId);
    if (!tab || !password) return;
    tab.pendingPassword = password;
    if (tab.ws && tab.ws.readyState === WebSocket.OPEN) {
      tab.ws.send(JSON.stringify({ op: "auth", password }));
    }
  };

  return (
    <SplitPageShell
      storageKey="rdev.split.terminal.rail"
      onDevicePick={(id) => createTab(id)}
    >
    <div className="flex h-full min-w-0 flex-col">
      {/* Split panes: each pane owns a tab strip; only the pane's active
       * tab is shown, the rest stay mounted (and connected) in the hidden
       * parking container so sessions survive. */}
      <div className="relative flex min-h-0 flex-1 flex-col overflow-hidden">
        <SplitContainer
          root={splitLayout.root}
          onResize={(path, childIndex, f) =>
            setSplitLayout((l) =>
              resizeSplit(l, path, childIndex, clampSplitPairFraction(f)),
            )
          }
          renderPane={(node: PaneNode) => {
            const paneId = node.paneId;
            const pane = paneTabs.get(paneId);
            const paneMetas = (pane?.tabs ?? [])
              .map((id) => metas.find((m) => m.id === id))
              .filter((m): m is TabMeta => m !== undefined);
            const activeMeta = paneMetas.find((m) => m.id === pane?.active);
            const paneCount = countPanes(splitLayout.root);
            return (
              <DeviceDropTarget
                key={paneId}
                className="flex h-full min-h-0 flex-col bg-sidebar"
                onDropDevice={(deviceId) => dropDeviceOnLeaf(paneId, deviceId)}
                onPointerDown={() => {
                  if (focusedLeafRef.current !== paneId) focusLeaf(paneId);
                }}
              >
                {/* Pane tab strip */}
                <div
                  className={cn(
                    "flex h-8 shrink-0 items-center gap-1 border-b bg-sidebar pl-1.5 pr-1 text-xs select-none",
                    paneId === focusedPane
                      ? "border-b-timeline-accent"
                      : "border-b-border-seam",
                  )}
                >
                  <div className="scrollbar-none flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
                    {paneMetas.map((m) => (
                      <div
                        key={m.id}
                        onClick={() => activateTab(paneId, m.id)}
                        className={cn(
                          "flex h-7 cursor-pointer items-center gap-1.5 rounded-md pr-2 pl-1.5",
                          m.id === pane?.active
                            ? "bg-state-active text-foreground"
                            : "text-muted-foreground hover:bg-state-hover",
                        )}
                      >
                        <span
                          className={cn(
                            "size-1.5 shrink-0 rounded-full",
                            m.status === "connected"
                              ? "bg-success"
                              : m.status === "authing"
                                ? "bg-attention"
                                : "bg-destructive",
                          )}
                        />
                        <span className="max-w-[160px] truncate">{m.deviceId}</span>
                        <button
                          type="button"
                          aria-label={t("common.close")}
                          onClick={(e) => {
                            e.stopPropagation();
                            closeTab(m.id);
                          }}
                          className="cursor-pointer rounded-sm p-0.5 leading-none text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                        >
                          <Icon name="X" className="size-3" />
                        </button>
                      </div>
                    ))}
                    {/* New tab in THIS pane, on its active tab's device. */}
                    <button
                      type="button"
                      aria-label={t("terminal.newTab")}
                      disabled={!devices.length}
                      onClick={() => {
                        const deviceId =
                          activeMeta?.deviceId ?? devices[0]?.id;
                        if (!deviceId) return;
                        createTab(deviceId, paneId);
                        setSplitLayout((l) => setFocus(l, paneId));
                      }}
                      className="flex cursor-pointer items-center rounded-md p-1 text-muted-foreground transition-colors hover:bg-foreground/2 hover:text-foreground disabled:opacity-40"
                    >
                      <Icon name="Plus" className="size-3.5" />
                    </button>
                  </div>
                  {/* Pane split / close (agent-page style). */}
                  <div className="flex shrink-0 items-center gap-0.5">
                    <button
                      type="button"
                      aria-label={t("split.right")}
                      title={t("split.right")}
                      disabled={paneCount >= MAX_TERMINAL_PANES}
                      onClick={() => splitCurrentPane(paneId, "right")}
                      className="flex cursor-pointer items-center rounded-md p-1 text-muted-foreground transition-colors hover:bg-foreground/2 hover:text-foreground disabled:opacity-40"
                    >
                      <Icon name="PanelRight" className="size-3.5" />
                    </button>
                    <button
                      type="button"
                      aria-label={t("split.down")}
                      title={t("split.down")}
                      disabled={paneCount >= MAX_TERMINAL_PANES}
                      onClick={() => splitCurrentPane(paneId, "bottom")}
                      className="flex cursor-pointer items-center rounded-md p-1 text-muted-foreground transition-colors hover:bg-foreground/2 hover:text-foreground disabled:opacity-40"
                    >
                      <Icon name="PanelBottom" className="size-3.5" />
                    </button>
                    {paneCount > 1 && (
                      <button
                        type="button"
                        aria-label={t("split.close")}
                        title={t("split.close")}
                        onClick={() => closePane(paneId)}
                        className="flex cursor-pointer items-center rounded-md p-1 text-muted-foreground transition-colors hover:bg-foreground/2 hover:text-foreground"
                      >
                        <Icon name="X" className="size-3.5" />
                      </button>
                    )}
                  </div>
                </div>
                <div className="relative min-h-0 flex-1">
                  <LeafHost
                    leafId={paneId}
                    hostsRef={leafHostsRef}
                    onResize={handleLeafResize}
                  />
                  {!activeMeta && (
                    <div className="absolute inset-0 flex items-center justify-center px-4 text-center text-sm text-muted-foreground">
                      {t("split.empty")}
                    </div>
                  )}
                </div>
              </DeviceDropTarget>
            );
          }}
        />
        <div ref={parkingRef} className="hidden" aria-hidden="true" />
        {metas.map((m) => {
          const tab = tabsRef.current.get(m.id);
          return m.authOpen && tab?.pane
            ? createPortal(
                <TabAuthOverlay
                  key={m.authRound}
                  deviceId={m.deviceId}
                  error={m.authError}
                  onSubmit={(pw) => submitAuth(m.id, pw)}
                  t={t}
                />,
                tab.pane,
              )
            : null;
        })}
      </div>

      {/* Mobile helper keys */}
      <div className="scrollbar-none hidden shrink-0 gap-1.5 overflow-x-auto border-t border-border/50 bg-sidebar px-2 py-1.5 max-[720px]:flex">
        {MOBILE_KEYS.map((k) => (
          <button
            key={k.label}
            type="button"
            onClick={() => {
              const tab = activeTab();
              sendTerminalData(tab, k.key);
              if (tab) {
                tab.term.focus();
                setTimeout(() => doResize(tab), 50);
              }
            }}
            className="min-h-[34px] min-w-11 shrink-0 cursor-pointer rounded-lg border border-border bg-background px-2.5 py-1.5 text-[0.8rem] text-foreground"
          >
            {k.label}
          </button>
        ))}
      </div>
    </div>
    </SplitPageShell>
  );
}

/* Leaf drop-target for a tab's pane DOM node; reports resizes so the shown
 * terminal refits after divider drags. */
function LeafHost({
  leafId,
  hostsRef,
  onResize,
}: {
  leafId: string;
  hostsRef: { current: Map<string, HTMLDivElement> };
  onResize: (leafId: string) => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    hostsRef.current.set(leafId, el);
    const observer = new ResizeObserver(() => onResize(leafId));
    observer.observe(el);
    return () => {
      observer.disconnect();
      hostsRef.current.delete(leafId);
    };
  }, [leafId, hostsRef, onResize]);
  return <div ref={ref} className="absolute inset-0" />;
}

/* ── Per-tab device password overlay (in-pane, mirrors the legacy auth box
 * and the Agent page's PasswordDialog behavior). Also mounted by the agent
 * right-panel terminal tabs. ── */

export function TabAuthOverlay({
  deviceId,
  error,
  onSubmit,
  t,
}: {
  deviceId: string;
  error: string;
  onSubmit: (pw: string) => void;
  t: (k: string, v?: Record<string, string | number>) => string;
}) {
  const [value, setValue] = useState(() => getDevicePassword(deviceId));
  const [busy, setBusy] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const autoSubmitted = useRef(false);

  const submit = () => {
    if (!value) return;
    setBusy(true);
    onSubmit(value);
  };

  // Auto-submit a remembered password once per overlay, like the vanilla page.
  useEffect(() => {
    if (!autoSubmitted.current && value) {
      autoSubmitted.current = true;
      const timer = setTimeout(submit, 50);
      return () => clearTimeout(timer);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (error) {
      setBusy(false);
      setValue("");
      inputRef.current?.focus();
    }
  }, [error]);

  return (
    <div className="absolute inset-0 z-10 flex items-center justify-center bg-background/90">
      <div className="w-[340px] max-w-[92vw] rounded-2xl border border-border bg-card p-6 text-center max-[720px]:p-5">
        <h3 className="mb-1.5 flex items-center justify-center gap-1.5 text-base text-timeline-accent">
          <Icon name="Lock" className="size-4" />
          {t("terminal.authTitle")}
        </h3>
        <p className="mb-4 text-[0.85rem] text-muted-foreground">
          {t("terminal.authDesc", { id: deviceId })}
        </p>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          <Input
            ref={inputRef}
            type="password"
            autoFocus
            value={value}
            placeholder={t("terminal.passwordPlaceholder")}
            autoComplete="current-password"
            onChange={(e) => setValue(e.target.value)}
            className="mb-3"
          />
          <Button type="submit" className="w-full" disabled={busy || !value}>
            {busy ? t("common.connecting") : t("common.connect")}
          </Button>
          <div className="mt-2 min-h-[1.2em] text-xs text-destructive-text">
            {error}
          </div>
        </form>
      </div>
    </div>
  );
}
