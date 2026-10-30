import {
  Fragment,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type PointerEvent as ReactPointerEvent,
} from "react";
import {
  Panel,
  PanelGroup,
  PanelResizeHandle,
  type ImperativePanelHandle,
} from "react-resizable-panels";
import { useSearchParams } from "react-router-dom";
import { MobileNavList } from "../shell/AppShell";
import { useI18n } from "../lib/i18n";
import { useDevices } from "../lib/devices";
import {
  wsURL,
  getDevicePassword,
  rememberDevicePassword,
  forgetDevicePassword,
} from "../lib/api";
import { cn } from "../lib/utils";
import { Button } from "../components/button";
import { Input } from "../components/input";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "../components/alert-dialog";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../components/dialog";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "../components/tooltip";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "../components/dropdown-menu";
import { Icon } from "../components/icon";
import { OsIcon } from "../components/os-icon";
import { useCopy } from "../lib/clipboard";
import { ChatPaneView } from "../components/agent/chat-pane";
import { CreateProjectDialog } from "../components/agent/create-project-dialog";
import type { DirListing } from "../components/agent/remote-path-browser";
import {
  AgentRightPanel,
  type PanelDock,
} from "../components/agent/right-panel";
import {
  SecondaryChatPane,
  SplitContainer,
} from "../components/agent/split-panes";
import {
  applyScopedMessage,
  useThreadSink,
  type ThreadSinkOps,
} from "../components/agent/thread-sink";
import {
  asString,
  formatTokenCount,
  parseAllThreads,
  parsePermMode,
  parseReasoningLevel,
  parseStringArray,
  parseThreads,
  parseTodos,
  parseUsage,
  replayHistory,
  threadCreatedMs,
  THREAD_SCOPED_OPS,
  TRANSCRIPT_OPS,
  type AllThreadsDevice,
  type ModelInfo,
  type PermMode,
  type ReasoningLevel,
  type Status,
} from "../components/agent/protocol";
import {
  countPanes,
  findPane,
  listPanes,
  MAX_PANES,
  parseLayout,
  removePane,
  replacePaneThread,
  resizeSplit,
  serializeLayout,
  setFocus,
  singlePaneLayout,
  splitPane,
  type PaneNode,
  type SplitLayout,
} from "../lib/split-layout";
import {
  decideThreadDrop,
  ENGAGE_DISTANCE_PX,
  pickZone,
  zoneBox,
  type Rect,
  type SplitZone,
} from "../lib/split-drag";
import type {
  ChangedFile,
  ChangesState,
  DiffState,
  FilePreviewState,
  ThreadInfo,
} from "../components/agent/types";

const MODEL_KEY = "rdev.agent.model";
const SIDEBAR_KEY = "rdev.agent.sidebar";
const SIDEBAR_VIEW_KEY = "rdev.agent.sidebar.view";
const SIDEBAR_WIDTH_KEY = "rdev.agent.sidebar.width";
const SIDEBAR_MIN_WIDTH = 240;
const SIDEBAR_MAX_WIDTH = 460;
const SIDEBAR_DEFAULT_WIDTH = 320;
const REASONING_KEY = "rdev.agent.reasoning";
const PANEL_OPEN_KEY = "rdev.agent.panel.open";
const PANEL_WIDTH_KEY = "rdev.agent.panel.width";
const PANEL_DOCK_KEY = "rdev.agent.panel.dock";
const PANEL_FULL_KEY = "rdev.agent.panel.full";
const SPLIT_KEY_PREFIX = "rdev.agent.split.";

/* bb panel collapse transition: 220ms cubic-bezier(0.32,0.72,0,1); the
 * duration var is 0ms until two frames after mount (no initial animation). */
const PANEL_TRANSITION_CLASS =
  "duration-[var(--panel-collapse-duration,220ms)] ease-[cubic-bezier(0.32,0.72,0,1)] motion-reduce:transition-none";

/* bb thread-list view state (sort/direction/organize/filter/hidden groups),
 * persisted per browser. Hidden groups are plain device ids (rdev groups
 * are devices; bb's are `machine:<id>` sections). */
interface SidebarView {
  sort: "updated" | "created" | "alpha";
  dir: "asc" | "desc";
  organize: "flat" | "device";
  showRunning: boolean;
  showAll: boolean;
  hiddenGroups: string[];
  /* bb TopLevelSidebarSection: device group headers collapse (chevron)
   * independently of being hidden from the list. */
  collapsedGroups: string[];
  /* "" = 所有对话 (threads from every device); else a device id filter. */
  deviceFilter: string;
}

const DEFAULT_SIDEBAR_VIEW: SidebarView = {
  sort: "updated",
  dir: "desc",
  organize: "flat",
  showRunning: true,
  showAll: true,
  hiddenGroups: [],
  collapsedGroups: [],
  deviceFilter: "",
};

function loadSidebarView(): SidebarView {
  try {
    const raw = localStorage.getItem(SIDEBAR_VIEW_KEY);
    if (!raw) return DEFAULT_SIDEBAR_VIEW;
    const o = JSON.parse(raw) as Record<string, unknown>;
    return {
      sort: o.sort === "created" || o.sort === "alpha" ? o.sort : "updated",
      dir: o.dir === "asc" ? "asc" : "desc",
      organize: o.organize === "device" ? "device" : "flat",
      showRunning: o.showRunning !== false,
      showAll: o.showAll !== false,
      hiddenGroups: parseStringArray(o.hiddenGroups),
      collapsedGroups: parseStringArray(o.collapsedGroups),
      deviceFilter: typeof o.deviceFilter === "string" ? o.deviceFilter : "",
    };
  } catch {
    return DEFAULT_SIDEBAR_VIEW;
  }
}

const SIDEBAR_SORTS: { id: SidebarView["sort"]; label: string }[] = [
  { id: "updated", label: "agent.sortUpdated" },
  { id: "created", label: "agent.sortCreated" },
  { id: "alpha", label: "agent.sortAlpha" },
];

export function AgentPage() {
  const { t } = useI18n();
  const { devices } = useDevices();
  const [copiedId, copy] = useCopy();
  const [searchParams, setSearchParams] = useSearchParams();

  const [device, setDeviceState] = useState("");
  const [status, setStatus] = useState<Status>("disconnected");
  const [authed, setAuthed] = useState(false);
  const [model, setModel] = useState("");
  const [models, setModels] = useState<ModelInfo[]>([]);
  const [authError, setAuthError] = useState("");
  const [threads, setThreads] = useState<ThreadInfo[]>([]);
  /* Cross-device thread list (all_threads op); null until the first frame
   * arrives — older servers never send it and the sidebar falls back to
   * the current-device thread_list. */
  const [allThreads, setAllThreads] = useState<AllThreadsDevice[] | null>(null);
  const [thread, setThread] = useState("");
  /* Mirror of `thread` for the WS handler: turn frames are tagged with
   * their thread id and dropped when the user is viewing another thread. */
  const threadRef = useRef("");
  useEffect(() => {
    threadRef.current = thread;
  }, [thread]);
  const [permMode, setPermMode] = useState<PermMode>("write");
  const [reasoning, setReasoning] = useState<ReasoningLevel>(
    () => parseReasoningLevel(localStorage.getItem(REASONING_KEY)) ?? "medium",
  );
  const [cwd, setCwd] = useState("");
  const [branch, setBranch] = useState("");
  /* Known per-thread working directories on this device (session-scoped):
   * fed by auth_ok / thread_switched / thread_meta cwds for the picker. */
  const [knownCwds, setKnownCwds] = useState<string[]>([]);
  /* Right-panel file preview states keyed by path (file_read op). */
  const [fileStates, setFileStates] = useState<Record<string, FilePreviewState>>({});
  /* Create-project dialog (bb ProjectPathDialog port): WS request/response
   * for dir_list/dir_mkdir, correlated by the echoed path. */
  const [createProjectOpen, setCreateProjectOpen] = useState(false);
  /* In-app rename/delete dialogs (bb: no native prompt/confirm). */
  const [renameOpen, setRenameOpen] = useState(false);
  const [renameValue, setRenameValue] = useState("");
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);
  /* bb-style inline rename: id of the sidebar row currently being edited. */
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const dirReqsRef = useRef(
    new Map<string, { resolve: (l: DirListing) => void; reject: (e: Error) => void; timer: number }>(),
  );
  const mkdirReqsRef = useRef(
    new Map<string, { resolve: () => void; reject: (e: Error) => void; timer: number }>(),
  );
  const [sidebarView, setSidebarView] = useState<SidebarView>(loadSidebarView);
  const [reconnecting, setReconnecting] = useState(false);
  const [sidebarOpen, setSidebarOpen] = useState(() => {
    const saved = localStorage.getItem(SIDEBAR_KEY);
    if (saved === "0") return false;
    if (saved === "1") return true;
    return window.matchMedia("(min-width: 768px)").matches;
  });
  /* Resizable sidebar width (md+), persisted in px. */
  const [sidebarWidth, setSidebarWidth] = useState(() => {
    const saved = parseFloat(localStorage.getItem(SIDEBAR_WIDTH_KEY) ?? "");
    return Number.isFinite(saved)
      ? Math.min(SIDEBAR_MAX_WIDTH, Math.max(SIDEBAR_MIN_WIDTH, saved))
      : SIDEBAR_DEFAULT_WIDTH;
  });
  /* Disable the open/close width transition while dragging (bb rubber-band
   * lag fix, same as the right panel). */
  const [sidebarDragging, setSidebarDragging] = useState(false);
  const [panelOpen, setPanelOpen] = useState(
    () => localStorage.getItem(PANEL_OPEN_KEY) === "1",
  );
  /* bb compact shelf lazy-mount: the panel content mounts on first open
   * and stays mounted afterwards (terminal sessions survive a close). */
  const [panelTouched, setPanelTouched] = useState(
    () => localStorage.getItem(PANEL_OPEN_KEY) === "1",
  );
  useEffect(() => {
    if (panelOpen) setPanelTouched(true);
  }, [panelOpen]);
  const [initialPanelWidth] = useState(() => {
    const saved = parseFloat(localStorage.getItem(PANEL_WIDTH_KEY) ?? "");
    return Number.isFinite(saved) && saved >= 5 && saved <= 95 ? saved : 50;
  });
  /* bb secondary-panel layout: dock side (SplitSide) + full-screen toggle
   * collapsing the conversation area. */
  const [panelDock, setPanelDock] = useState<PanelDock>(() => {
    const saved = localStorage.getItem(PANEL_DOCK_KEY);
    return saved === "left" || saved === "top" || saved === "bottom"
      ? saved
      : "right";
  });
  const [panelFull, setPanelFull] = useState(
    () => localStorage.getItem(PANEL_FULL_KEY) === "1",
  );
  const setDock = useCallback((dock: PanelDock) => {
    setPanelDock(dock);
    localStorage.setItem(PANEL_DOCK_KEY, dock);
  }, []);
  const togglePanelFull = useCallback(() => {
    setPanelFull((prev) => {
      localStorage.setItem(PANEL_FULL_KEY, prev ? "0" : "1");
      return !prev;
    });
  }, []);
  const [changes, setChanges] = useState<ChangesState | null>(null);
  /* get_diff replies keyed by path (the changes deck fetches lazily per
   * expanded card); cleared with the change set. */
  const [diffs, setDiffs] = useState<Record<string, DiffState>>({});
  const [isNarrow, setIsNarrow] = useState(
    () => window.matchMedia("(max-width: 767px)").matches,
  );
  const [transitionsReady, setTransitionsReady] = useState(false);
  /* bb: transition disabled while dragging the divider, else every
   * pointermove animates 220ms toward the target → rubber-band lag. */
  const [panelDragging, setPanelDragging] = useState(false);
  const [groupWidth, setGroupWidth] = useState(0);

  const wsRef = useRef<WebSocket | null>(null);
  const rightPanelRef = useRef<ImperativePanelHandle>(null);
  const panelWidthRef = useRef(initialPanelWidth);
  const panelSizingRef = useRef<HTMLDivElement>(null);
  const statusRef = useRef<Status>(status);
  statusRef.current = status;
  const reconnectTimer = useRef<number | null>(null);
  const reconnectAttempt = useRef(0);
  const pingInterval = useRef<number | null>(null);

  /* Thread-scoped transcript state lives in sinks: the page owns the sink
   * of the connection's current thread; every secondary split pane owns
   * one for its own thread (registered here for WS frame forwarding). */
  const primary = useThreadSink();
  const primaryOps = primary.ops;
  const secondarySinksRef = useRef(new Map<string, ThreadSinkOps>());

  /* bb split layout (per device, persisted): pane threadId null = follow
   * the connection's current thread. */
  const [layout, setLayoutState] = useState<SplitLayout>(() => singlePaneLayout());
  const layoutRef = useRef(layout);
  /* thread_list/auth_ok seen since connect: gates the reconcile that drops
   * panes of deleted threads (an empty pre-auth list must not nuke them). */
  const threadsLoadedRef = useRef(false);

  /* Thread drag (sidebar → pane split/drop): press engages after
   * ENGAGE_DISTANCE_PX, then a ghost follows the pointer and the hovered
   * pane shows its zone highlight. */
  const [drag, setDrag] = useState<{
    id: string;
    title: string;
    x: number;
    y: number;
  } | null>(null);
  const [dropHover, setDropHover] = useState<{
    paneId: string;
    zone: SplitZone;
    rect: Rect;
  } | null>(null);
  const dropHoverRef = useRef<{
    paneId: string;
    zone: SplitZone;
    rect: Rect;
  } | null>(null);
  const dragSessionRef = useRef<{
    id: string;
    title: string;
    startX: number;
    startY: number;
    engaged: boolean;
  } | null>(null);
  const suppressClickRef = useRef(false);

  const clearReconnectTimer = useCallback(() => {
    if (reconnectTimer.current !== null) {
      window.clearTimeout(reconnectTimer.current);
      reconnectTimer.current = null;
    }
  }, []);

  const clearPing = useCallback(() => {
    if (pingInterval.current !== null) {
      window.clearInterval(pingInterval.current);
      pingInterval.current = null;
    }
  }, []);

  const startPing = useCallback(() => {
    clearPing();
    pingInterval.current = window.setInterval(() => {
      if (wsRef.current?.readyState === WebSocket.OPEN) {
        wsRef.current.send(JSON.stringify({ op: "ping" }));
      }
    }, 25000);
  }, [clearPing]);

  const currentDevice = devices.find((d) => d.id === device);

  const totalTokens = useMemo(() => {
    let sum = 0;
    for (const item of primary.state.items) {
      if (item.kind === "turn" && item.usage) {
        sum += (item.usage.inputTokens || 0) + (item.usage.outputTokens || 0);
      }
    }
    return sum;
  }, [primary.state.items]);

  /* ── Right panel (bb three-column layout) ── */

  /* Below 768px the inline resizable panel degrades to a fixed drawer. */
  useEffect(() => {
    const mq = window.matchMedia("(max-width: 767px)");
    const onChange = () => setIsNarrow(mq.matches);
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, []);

  /* Enable the 220ms open/close transition two frames after mount. rAF
   * never fires while the tab is hidden, so a timer fallback arms the
   * transitions anyway (a background-loaded tab must not stay frozen). */
  useEffect(() => {
    let second = 0;
    const first = window.requestAnimationFrame(() => {
      second = window.requestAnimationFrame(() => setTransitionsReady(true));
    });
    const fallback = window.setTimeout(() => setTransitionsReady(true), 500);
    return () => {
      window.cancelAnimationFrame(first);
      window.cancelAnimationFrame(second);
      window.clearTimeout(fallback);
    };
  }, []);

  /* v2 minSize is a percentage; observe the group width to give both
   * panels a 240px floor. */
  useEffect(() => {
    const el = panelSizingRef.current;
    if (!el) return;
    const ro = new ResizeObserver((entries) => {
      setGroupWidth(entries[0]?.contentRect.width ?? 0);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  /* Imperative open/close: collapse to 0 when closed (or in drawer mode),
   * restore the saved width when opened. Capture the width before expand():
   * its onResize(minSize) notification would overwrite the ref first. */
  useEffect(() => {
    const panel = rightPanelRef.current;
    if (!panel) return;
    if (panelOpen && !isNarrow) {
      const width = panelWidthRef.current;
      panel.expand();
      panel.resize(width);
    } else {
      panel.collapse();
    }
  }, [panelOpen, isNarrow, panelDock]);

  /* Full Screen (bb resolveConversationCollapseControl) is pure CSS: the
   * main Panel gets `hidden`, the flex panel soaks the row, and unhiding
   * restores the exact pre-full split — no imperative resize race. */
  const togglePanel = useCallback(() => {
    setPanelOpen((prev) => {
      const next = !prev;
      localStorage.setItem(PANEL_OPEN_KEY, next ? "1" : "0");
      /* Compact: the full-width shelf would cover the drawer — opening
       * one dismisses the other. */
      if (next && window.innerWidth < 768) {
        setSidebarOpen((wasOpen) => {
          if (wasOpen) localStorage.setItem(SIDEBAR_KEY, "0");
          return false;
        });
      }
      return next;
    });
  }, []);

  /* bb thread→panel linkage: clicking a file path in the transcript opens
   * (or focuses) the right panel and raises a file tab for the path. */
  const [openFileSignal, setOpenFileSignal] = useState<{
    path: string;
    nonce: number;
    lineStart?: number;
    lineEnd?: number;
  } | null>(null);
  const openFileInPanel = useCallback(
    (path: string, lineStart?: number, lineEnd?: number) => {
      setPanelOpen((prev) => {
        if (!prev) localStorage.setItem(PANEL_OPEN_KEY, "1");
        return true;
      });
      setOpenFileSignal({ path, nonce: Date.now(), lineStart, lineEnd });
    },
    [],
  );

  /* bb open-file-diff: file-change row titles raise the diff deck focused
   * on the file's card instead of the plain preview. */
  const [openDiffSignal, setOpenDiffSignal] = useState<{
    path: string;
    nonce: number;
  } | null>(null);
  const openDiffInPanel = useCallback((path: string) => {
    setPanelOpen((prev) => {
      if (!prev) localStorage.setItem(PANEL_OPEN_KEY, "1");
      return true;
    });
    setOpenDiffSignal({ path, nonce: Date.now() });
  }, []);

  /* mod+j toggles the right panel; skipped while typing. */
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.key.toLowerCase() !== "j") return;
      const target = e.target as HTMLElement | null;
      if (
        target &&
        (target.tagName === "INPUT" || target.tagName === "TEXTAREA")
      ) {
        return;
      }
      e.preventDefault();
      togglePanel();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [togglePanel]);

  const requestChanges = useCallback(() => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      /* Patches are stale once the working tree is re-probed; expanded
       * cards re-request on the next render. */
      setDiffs({});
      wsRef.current.send(JSON.stringify({ op: "get_changes" }));
    }
  }, []);

  const requestDiff = useCallback((path: string) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ op: "get_diff", path }));
    }
  }, []);

  /* Right-panel file preview: optimistic loading state, then file_read. */
  const requestFileRead = useCallback(
    (path: string) => {
      if (wsRef.current?.readyState === WebSocket.OPEN) {
        setFileStates((prev) => ({
          ...prev,
          [path]: {
            status: "loading",
            content: "",
            truncated: false,
            message: "",
          },
        }));
        wsRef.current.send(JSON.stringify({ op: "file_read", path }));
      } else {
        setFileStates((prev) => ({
          ...prev,
          [path]: {
            status: "error",
            content: "",
            truncated: false,
            message: t("agent.status.disconnected"),
          },
        }));
      }
    },
    [t],
  );

  /* First open (or a cleared cache) pulls the current thread's changes; the
   * server also pushes them after auth_ok / switch_thread / set_cwd / done. */
  useEffect(() => {
    if (panelOpen && authed && status === "connected" && changes === null) {
      requestChanges();
    }
  }, [panelOpen, authed, status, changes, requestChanges]);

  /* Right-panel selection "Add to chat": each pane registers its composer
   * inserter keyed by pane id; the panel targets the focused pane (bb's
   * onSelectionAddToChat). */
  const addToChatFnsRef = useRef(new Map<string, (text: string) => void>());
  const insertIntoFocusedComposer = useCallback(
    (text: string) => {
      const fn =
        addToChatFnsRef.current.get(layout.focusedPaneId) ??
        addToChatFnsRef.current.values().next().value;
      fn?.(text);
    },
    [layout.focusedPaneId],
  );

  /* New-tab file search (bb NewTabFileSearch): request/response over the
   * agent socket, pending entries keyed by the echoed query. */
  const searchReqsRef = useRef(
    new Map<
      string,
      {
        resolve: (files: string[]) => void;
        timer: number;
      }
    >(),
  );
  const requestFileSearch = useCallback((q: string): Promise<string[]> => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) return Promise.resolve([]);
    const { promise, resolve } = Promise.withResolvers<string[]>();
    const prev = searchReqsRef.current.get(q);
    if (prev) {
      window.clearTimeout(prev.timer);
      prev.resolve([]);
    }
    const timer = window.setTimeout(() => {
      searchReqsRef.current.delete(q);
      resolve([]);
    }, 8000);
    searchReqsRef.current.set(q, { resolve, timer });
    ws.send(JSON.stringify({ op: "search_files", q }));
    return promise;
  }, []);

  const panelMinPct =
    groupWidth > 0 ? Math.min(45, (240 / groupWidth) * 100) : 20;
  /* bb SidebarSplitContainer: top/bottom docks flip the split axis;
   * left/top mount the panel before the conversation area. */
  const verticalDock = panelDock === "top" || panelDock === "bottom";
  const panelBefore = panelDock === "left" || panelDock === "top";

  /* ── Connection lifecycle ── */

  /* Raw WS send used by the panes (chat/stop/steer/approve/ask/peek). */
  const sendRaw = useCallback((payload: Record<string, unknown>): boolean => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) return false;
    ws.send(JSON.stringify(payload));
    return true;
  }, []);

  /* Secondary panes register their sink ops keyed by thread id; the WS
   * handler forwards that thread's tagged frames to them. */
  const registerSecondary = useCallback((tid: string, ops: ThreadSinkOps) => {
    secondarySinksRef.current.set(tid, ops);
    return () => {
      if (secondarySinksRef.current.get(tid) === ops) {
        secondarySinksRef.current.delete(tid);
      }
    };
  }, []);

  const handleMessage = useCallback((msg: Record<string, unknown>) => {
    /* Thread routing: turn frames (delta/tool/done/…), approvals, queue
     * and changes/diff/thread_meta are tagged with their thread id. Frames
     * for a thread that is not the conn current go to the split pane
     * showing it (registry); with no pane they are dropped — switching to
     * that thread replays its history plus in-flight frames (live).
     * auth_ok and thread_switched are tagged too but must always pass:
     * they carry the NEW thread id and update threadRef themselves. */
    const op = String(msg.op);
    const frameThread = asString(msg.thread);
    if (
      frameThread !== "" &&
      threadRef.current !== "" &&
      frameThread !== threadRef.current &&
      THREAD_SCOPED_OPS[op] === true
    ) {
      if (TRANSCRIPT_OPS[op] === true) {
        const ops = secondarySinksRef.current.get(frameThread);
        if (ops) applyScopedMessage(ops, msg, t);
      }
      return;
    }
    /* Current thread (or untagged legacy frames): the same machinery the
     * panes use. A done frame may also carry an entry-level model switch. */
    if (TRANSCRIPT_OPS[op] === true && op !== "thread_peeked") {
      applyScopedMessage(primaryOps, msg, t);
      if (op === "done") {
        const m = asString(msg.model);
        if (m) setModel(m);
      }
      return;
    }
    switch (msg.op) {
      case "auth":
        setStatus("authing");
        setAuthError("");
        break;
      case "auth_ok": {
        setAuthed(true);
        setStatus("connected");
        primaryOps.setRunning(!!msg.running);
        const list = (Array.isArray(msg.models) ? msg.models : []).flatMap(
          (x): ModelInfo[] => {
            if (!x || typeof x !== "object" || !("id" in x)) return [];
            const id = asString(x.id);
            if (!id) return [];
            const cw =
              "contextWindow" in x &&
              typeof x.contextWindow === "number" &&
              x.contextWindow > 0
                ? x.contextWindow
                : undefined;
            return [
              {
                id,
                name: "name" in x ? asString(x.name) || undefined : undefined,
                provider:
                  "provider" in x ? asString(x.provider) || undefined : undefined,
                contextWindow: cw,
              },
            ];
          },
        );
        setModels(list);
        primaryOps.setTurnUsage(parseUsage(msg.usage));
        /* Server-persisted model wins; localStorage is a display cache only. */
        const m = asString(msg.model);
        setModel(m);
        if (m) localStorage.setItem(MODEL_KEY, m);
        setPermMode(parsePermMode(msg.permissionMode));
        const authCwd = asString(msg.cwd);
        setCwd(authCwd);
        if (authCwd) {
          setKnownCwds((prev) =>
            prev.includes(authCwd) ? prev : [...prev, authCwd],
          );
        }
        setBranch(asString(msg.branch));
        const serverReasoning = parseReasoningLevel(msg.reasoningLevel);
        if (serverReasoning) {
          setReasoning(serverReasoning);
          localStorage.setItem(REASONING_KEY, serverReasoning);
        }
        const authThread = asString(msg.thread);
        setThread(authThread);
        threadRef.current = authThread;
        const authThreads = parseThreads(msg.threads);
        setThreads(authThreads);
        /* Deep link /agent?device=X&thread=Y: adopt Y when it lives on this
         * device, then strip the param (replaceState) so later refreshes
         * don't fight manual thread switches. */
        const urlThread = new URLSearchParams(window.location.search).get("thread");
        if (urlThread) {
          if (
            urlThread !== authThread &&
            authThreads.some((th) => th.id === urlThread)
          ) {
            wsRef.current?.send(
              JSON.stringify({ op: "switch_thread", thread: urlThread }),
            );
          }
          setSearchParams(
            (prev) => {
              const next = new URLSearchParams(prev);
              next.delete("thread");
              return next;
            },
            { replace: true },
          );
        }
        primaryOps.setQueue(parseStringArray(msg.queue));
        primaryOps.setTodos(parseTodos(msg.todos));
        primaryOps.openToolIds.current.clear();
        const working = msg.working;
        if (msg.running && working && typeof working === "object") {
          const rawSince = "since" in working ? working.since : undefined;
          const since =
            typeof rawSince === "number"
              ? rawSince
              : Date.parse(typeof rawSince === "string" ? rawSince : "");
          primaryOps.setWorkingSince(Number.isFinite(since) ? since : Date.now());
          primaryOps.setCurrentTool(
            "tool" in working && typeof working.tool === "string"
              ? working.tool
              : "",
          );
        } else {
          primaryOps.setWorkingSince(msg.running ? Date.now() : null);
          primaryOps.setCurrentTool("");
        }
        /* Reconnect: secondary panes hold stale transcripts; they re-seed
         * via peek_thread when ready flips back to true. */
        for (const ops of secondarySinksRef.current.values()) {
          ops.resetTransient();
          ops.setItems([]);
          ops.setTodos([]);
          ops.setQueue([]);
          ops.setRunning(false);
          ops.setTurnUsage(null);
        }
        threadsLoadedRef.current = true;
        const history = Array.isArray(msg.history) ? msg.history : [];
        primaryOps.turnOpen.current = false;
        primaryOps.setItems(replayHistory(history));
        primaryOps.scrollDown(true);
        /* Reattach mid-turn: replay the in-flight frames (turn_start,
         * deltas, tool calls, pending approval) so the live transcript
         * looks exactly as if the tab had never gone away. */
        if (Array.isArray(msg.live)) {
          for (const raw of msg.live) {
            if (raw && typeof raw === "object") {
              handleMessage(raw as Record<string, unknown>);
            }
          }
        }
        reconnectAttempt.current = 0;
        setReconnecting(false);
        startPing();
        break;
      }
      case "auth_fail":
        forgetDevicePassword(device);
        setAuthError(asString(msg.message) || t("common.passwordWrong"));
        break;
      case "thread_peeked":
        /* A pane peeked the thread that IS the conn current. */
        applyScopedMessage(primaryOps, msg, t);
        break;
      case "thread_list":
        threadsLoadedRef.current = true;
        setThreads(parseThreads(msg.threads));
        break;
      case "all_threads":
        setAllThreads(parseAllThreads(msg.devices));
        break;
      case "thread_meta": {
        const metaCwd = asString(msg.cwd);
        setCwd(metaCwd);
        if (metaCwd) {
          setKnownCwds((prev) =>
            prev.includes(metaCwd) ? prev : [...prev, metaCwd],
          );
        }
        setBranch(asString(msg.branch));
        break;
      }
      /* Reply to the socket-scoped file_read op (right-panel preview). */
      case "file_content": {
        const path = asString(msg.path);
        if (!path) break;
        const message = asString(msg.message);
        setFileStates((prev) => ({
          ...prev,
          [path]: message
            ? { status: "error", content: "", truncated: false, message }
            : {
                status: "ready",
                content: asString(msg.content),
                truncated: !!msg.truncated,
                message: "",
                encoding: asString(msg.encoding) || undefined,
                mime: asString(msg.mime) || undefined,
              },
        }));
        break;
      }
      /* Replies to the create-project dialog's dir_list/dir_mkdir ops. */
      case "dir_entries": {
        const path = asString(msg.path);
        const pending = path ? dirReqsRef.current.get(path) : undefined;
        if (!pending) break;
        dirReqsRef.current.delete(path);
        window.clearTimeout(pending.timer);
        const message = asString(msg.message);
        if (message) {
          pending.reject(new Error(message));
          break;
        }
        const entries = (Array.isArray(msg.entries) ? msg.entries : []).flatMap(
          (x): DirListing["entries"] => {
            if (!x || typeof x !== "object") return [];
            const o = x as { name?: unknown; path?: unknown; dir?: unknown };
            const name = asString(o.name);
            const epath = asString(o.path);
            if (!name || !epath) return [];
            return [{ name, path: epath, dir: !!o.dir }];
          },
        );
        const roots = (Array.isArray(msg.roots) ? msg.roots : []).flatMap(
          (x): string[] => (typeof x === "string" && x ? [x] : []),
        );
        pending.resolve({ parent: asString(msg.parent), entries, roots });
        break;
      }
      case "dir_mkdir": {
        const path = asString(msg.path);
        const pending = path ? mkdirReqsRef.current.get(path) : undefined;
        if (!pending) break;
        mkdirReqsRef.current.delete(path);
        window.clearTimeout(pending.timer);
        const message = asString(msg.message);
        if (message) pending.reject(new Error(message));
        else pending.resolve();
        break;
      }
      case "changes": {
        const files = (Array.isArray(msg.files) ? msg.files : []).flatMap(
          (x): ChangedFile[] => {
            if (!x || typeof x !== "object") return [];
            const o = x as { path?: unknown; status?: unknown; adds?: unknown; dels?: unknown };
            const path = asString(o.path);
            if (!path) return [];
            return [
              {
                path,
                status: asString(o.status),
                adds: typeof o.adds === "number" ? o.adds : 0,
                dels: typeof o.dels === "number" ? o.dels : 0,
              },
            ];
          },
        );
        setChanges({
          cwd: asString(msg.cwd),
          branch: asString(msg.branch),
          files,
          error: asString(msg.error),
        });
        break;
      }
      case "diff": {
        const path = asString(msg.path);
        setDiffs((prev) => ({
          ...prev,
          [path]: {
            path,
            diff: asString(msg.diff),
            error: asString(msg.error),
          },
        }));
        break;
      }
      case "file_search": {
        const q = asString(msg.q);
        const pending = searchReqsRef.current.get(q);
        if (pending) {
          window.clearTimeout(pending.timer);
          searchReqsRef.current.delete(q);
          const files = (Array.isArray(msg.files) ? msg.files : []).flatMap(
            (x): string[] => (typeof x === "string" ? [x] : []),
          );
          pending.resolve(files);
        }
        break;
      }
      case "permission_mode":
        setPermMode(parsePermMode(msg.mode));
        break;
      case "reasoning_level": {
        const level = parseReasoningLevel(msg.level);
        if (level) {
          setReasoning(level);
          localStorage.setItem(REASONING_KEY, level);
        }
        break;
      }
      case "thread_switched": {
        primaryOps.turnOpen.current = false;
        primaryOps.openToolIds.current.clear();
        const switchThread = asString(msg.thread);
        setThread(switchThread);
        threadRef.current = switchThread;
        primaryOps.setTodos(parseTodos(msg.todos));
        primaryOps.setQueue(parseStringArray(msg.queue));
        primaryOps.setCurrentTool("");
        primaryOps.setRunning(!!msg.running);
        primaryOps.setWorkingSince(msg.running ? Date.now() : null);
        primaryOps.setTurnUsage(parseUsage(msg.usage));
        /* File previews are thread-scoped (relative paths resolve against
         * the new thread's cwd): drop them; open tabs re-fetch lazily. */
        setFileStates({});
        /* Stale until the server pushes the new thread's changes. */
        setChanges(null);
        setDiffs({});
        const history = Array.isArray(msg.history) ? msg.history : [];
        primaryOps.setItems(replayHistory(history));
        primaryOps.scrollDown(true);
        /* Replay the in-flight turn frames when switching into a running
         * thread (isolated while viewing another thread). */
        if (Array.isArray(msg.live)) {
          for (const raw of msg.live) {
            if (raw && typeof raw === "object") {
              handleMessage(raw as Record<string, unknown>);
            }
          }
        }
        break;
      }
    }
  }, [device, primaryOps, setSearchParams, startPing, t]);

  /* Auto-reconnect with exponential backoff: 1s → 2s → 4s → 8s … cap 30s.
   * Reset on successful auth_ok (see handleMessage) or manual connect.
   * Uses connectRef to break the connect ↔ scheduleReconnect cycle. */
  const connectRef = useRef<(deviceId: string, preserveBackoff?: boolean) => void>(() => {});
  const scheduleReconnect = useCallback((deviceId: string) => {
    if (reconnectTimer.current !== null) return;
    const delay = Math.min(1000 * 2 ** reconnectAttempt.current, 30000);
    reconnectAttempt.current += 1;
    setReconnecting(true);
    reconnectTimer.current = window.setTimeout(() => {
      reconnectTimer.current = null;
      connectRef.current(deviceId, true);
    }, delay);
  }, []);

  const connect = useCallback((deviceId: string, preserveBackoff = false) => {
    if (!deviceId) return;
    clearReconnectTimer();
    if (!preserveBackoff) {
      reconnectAttempt.current = 0;
      setReconnecting(false);
    }
    clearPing();
    if (wsRef.current) {
      wsRef.current.onclose = null;
      wsRef.current.close();
      wsRef.current = null;
    }
    setAuthed(false);
    setModels([]);
    setModel("");
    setThreads([]);
    setThread("");
    setPermMode("write");
    setReasoning(parseReasoningLevel(localStorage.getItem(REASONING_KEY)) ?? "medium");
    setCwd("");
    setBranch("");
    setKnownCwds([]);
    setFileStates({});
    setChanges(null);
    setDiffs({});
    primaryOps.resetTransient();
    primaryOps.setItems([]);
    primaryOps.setTodos([]);
    primaryOps.setQueue([]);
    primaryOps.setRunning(false);
    primaryOps.setTurnUsage(null);
    threadsLoadedRef.current = false;
    setStatus("connecting");

    const ws = new WebSocket(wsURL("/agent?device=" + encodeURIComponent(deviceId)));
    wsRef.current = ws;
    ws.onmessage = (evt) => {
      let msg: Record<string, unknown>;
      try {
        msg = JSON.parse(String(evt.data));
      } catch {
        return;
      }
      handleMessage(msg);
    };
    ws.onclose = () => {
      if (wsRef.current !== ws) return;
      clearPing();
      setAuthed(false);
      primaryOps.resetTransient();
      primaryOps.setRunning(false);
      primaryOps.finishTurn();
      setStatus("disconnected");
      scheduleReconnect(deviceId);
    };
    ws.onerror = () => {
      if (wsRef.current !== ws) return;
      if (statusRef.current !== "disconnected") {
        setStatus("disconnected");
      }
    };
  }, [clearPing, clearReconnectTimer, handleMessage, primaryOps, scheduleReconnect]);
  connectRef.current = connect;

  const setDevice = useCallback((id: string) => {
    setDeviceState(id);
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        if (id) next.set("device", id);
        else next.delete("device");
        return next;
      },
      { replace: true },
    );
    primaryOps.turnOpen.current = false;
    primaryOps.setItems([]);
    clearReconnectTimer();
    reconnectAttempt.current = 0;
    setReconnecting(false);
    clearPing();
    if (wsRef.current) {
      wsRef.current.onclose = null;
      wsRef.current.close();
      wsRef.current = null;
    }
    setAuthed(false);
    setCwd("");
    setBranch("");
    setKnownCwds([]);
    primaryOps.setTurnUsage(null);
    setFileStates({});
    setChanges(null);
    setDiffs({});
    setStatus("disconnected");
    if (id) connect(id);
  }, [clearPing, clearReconnectTimer, connect, primaryOps, setSearchParams]);

  /* Pick the device from the list: keep current while present, else the URL
   * preselect, else none. Mirrors the vanilla page's loadDevices. */
  useEffect(() => {
    if (!devices.length) return;
    const preselect = searchParams.get("device") || "";
    if (device && devices.some((d) => d.id === device)) return;
    if (preselect && devices.some((d) => d.id === preselect)) {
      setDevice(preselect);
    } else if (device) {
      setDevice("");
    }
  }, [devices, device, searchParams, setDevice]);

  useEffect(
    () => () => {
      clearReconnectTimer();
      clearPing();
      if (wsRef.current) {
        wsRef.current.onclose = null;
        wsRef.current.close();
      }
    },
    [clearPing, clearReconnectTimer],
  );

  /* ── Actions ── */

  const ready = authed && status === "connected" && wsRef.current?.readyState === WebSocket.OPEN;

  /* Manual reconnect: immediate, resets the backoff counter. */
  const manualReconnect = useCallback(() => {
    if (device) connect(device);
  }, [connect, device]);

  const sendSetCwd = (path: string) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ op: "set_cwd", path }));
    }
  };

  /* Create-project dialog data ops: request/response over the agent WS,
   * correlated by the echoed path (rapid same-path requests resolve
   * together harmlessly). ~20s timeout rejects as an error state. */
  const listDir = useCallback((path: string): Promise<DirListing> => {
    const { promise, resolve, reject } = Promise.withResolvers<DirListing>();
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      reject(new Error("offline"));
      return promise;
    }
    const timer = window.setTimeout(() => {
      dirReqsRef.current.delete(path);
      reject(new Error("timeout"));
    }, 20000);
    dirReqsRef.current.set(path, { resolve, reject, timer });
    ws.send(JSON.stringify({ op: "dir_list", path }));
    return promise;
  }, []);

  const mkdirDir = useCallback((path: string): Promise<void> => {
    const { promise, resolve, reject } = Promise.withResolvers<void>();
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      reject(new Error("offline"));
      return promise;
    }
    const timer = window.setTimeout(() => {
      mkdirReqsRef.current.delete(path);
      reject(new Error("timeout"));
    }, 20000);
    mkdirReqsRef.current.set(path, { resolve, reject, timer });
    ws.send(JSON.stringify({ op: "dir_mkdir", path }));
    return promise;
  }, []);

  const renameThread = (id: string, title: string) => {
    if (!title || !id) return;
    sendRaw({ op: "rename_thread", thread: id, title });
    setThreads((prev) => prev.map((th) => (th.id === id ? { ...th, title } : th)));
    setAllThreads((prev) =>
      prev?.map((d) =>
        d.device === device
          ? {
              ...d,
              threads: d.threads.map((th) =>
                th.id === id ? { ...th, title } : th,
              ),
            }
          : d,
      ) ?? null,
    );
  };

  const openRenameDialog = () => {
    setRenameValue(currentThreadTitle);
    setRenameOpen(true);
  };

  const confirmDeleteThread = () => {
    if (!deleteTarget) return;
    sendRaw({ op: "delete_thread", thread: deleteTarget });
    setDeleteTarget(null);
  };

  const selectPermMode = useCallback(
    (mode: PermMode) => {
      setPermMode(mode);
      sendRaw({ op: "set_permission_mode", mode });
    },
    [sendRaw],
  );

  const selectReasoning = useCallback(
    (level: ReasoningLevel) => {
      setReasoning(level);
      localStorage.setItem(REASONING_KEY, level);
      sendRaw({ op: "set_reasoning_level", level });
    },
    [sendRaw],
  );

  /* Composer model picker: display cache only; the model actually used
   * travels with each chat payload. */
  const selectModel = useCallback((id: string) => {
    setModel(id);
    localStorage.setItem(MODEL_KEY, id);
  }, []);

  const submitPassword = (pw: string) => {
    if (!pw) return;
    rememberDevicePassword(device, pw);
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ op: "auth", password: pw }));
    }
  };

  const currentThreadTitle =
    threads.find((th) => th.id === thread)?.title || t("agent.newThread");

  /* bb thread-list view state: one write per user gesture (menu handlers),
   * merged onto the persisted record. */
  const updateSidebarView = (patch: Partial<SidebarView>) => {
    const next = { ...sidebarView, ...patch };
    setSidebarView(next);
    localStorage.setItem(SIDEBAR_VIEW_KEY, JSON.stringify(next));
  };

  /* bb: re-clicking the active sort flips its direction; switching picks
   * the natural direction (recent first / A→Z). */
  const selectSort = (id: SidebarView["sort"]) => {
    if (sidebarView.sort === id) {
      updateSidebarView({ dir: sidebarView.dir === "asc" ? "desc" : "asc" });
    } else {
      updateSidebarView({ sort: id, dir: id === "alpha" ? "asc" : "desc" });
    }
  };

  const currentContextWindow =
    models.find((m) => m.id === model)?.contextWindow ?? undefined;

  /* ── Split-screen panes (bb) ── */

  /* Load the per-device layout; unknown/broken payloads fall back to one
   * pane following the conn current. */
  useEffect(() => {
    const saved = device
      ? parseLayout(localStorage.getItem(SPLIT_KEY_PREFIX + device))
      : null;
    setLayoutState(saved ?? singlePaneLayout());
  }, [device]);

  useEffect(() => {
    layoutRef.current = layout;
    if (device) {
      localStorage.setItem(SPLIT_KEY_PREFIX + device, serializeLayout(layout));
    }
  }, [layout, device]);

  /* The pane showing the conn current thread must always exist: after
   * thread_switched / new_thread into a layout of only explicit panes, the
   * focused pane falls back to following the connection. */
  useEffect(() => {
    if (!thread) return;
    const l = layoutRef.current;
    const visible = listPanes(l.root).some(
      (p) => (p.threadId ?? thread) === thread,
    );
    if (!visible) {
      setLayoutState(replacePaneThread(l, l.focusedPaneId, null));
    }
  }, [thread]);

  /* Panes pinned to deleted threads close (bb keeps layout consistent with
   * the conversation list). Gated on a loaded list so the empty pre-auth
   * state does not nuke restored panes. */
  useEffect(() => {
    if (!threadsLoadedRef.current) return;
    const ids = new Set(threads.map((th) => th.id));
    setLayoutState((l) => {
      let next = l;
      for (const p of listPanes(l.root)) {
        if (p.threadId !== null && !ids.has(p.threadId)) {
          next = removePane(next, p.paneId);
        }
      }
      return next;
    });
  }, [threads]);

  const onSplitResize = useCallback(
    (path: readonly number[], childIndex: number, pairFraction: number) => {
      setLayoutState((l) => resizeSplit(l, path, childIndex, pairFraction));
    },
    [],
  );

  /* The pane currently showing a thread (null-threadId panes resolve to
   * the conn current). */
  const paneShowing = useCallback((l: SplitLayout, tid: string) => {
    const cur = threadRef.current;
    return listPanes(l.root).find((p) => (p.threadId ?? cur) === tid) ?? null;
  }, []);

  /* bb decideThreadDrop: center → replace, edge → split (replace at the
   * pane cap), already-open → focus its pane. */
  const applyDrop = useCallback(
    (paneId: string, zone: SplitZone, threadId: string) => {
      const l = layoutRef.current;
      const already = paneShowing(l, threadId) !== null;
      const decision = decideThreadDrop({
        zone,
        threadAlreadyOpen: already,
        atMaxPanes: countPanes(l.root) >= MAX_PANES,
      });
      if (decision.kind === "focus") {
        const existing = paneShowing(l, threadId);
        if (existing) setLayoutState(setFocus(l, existing.paneId));
        return;
      }
      const target = findPane(l.root, paneId);
      if (!target) return;
      if (decision.kind === "replace") {
        if (target.threadId === null) {
          /* The following pane stays bound to the conn current: drive it
           * with switch_thread instead of pinning a thread id. */
          if (threadId !== threadRef.current) {
            sendRaw({ op: "switch_thread", thread: threadId });
          }
          setLayoutState(setFocus(l, paneId));
        } else if (target.threadId !== threadId) {
          setLayoutState(replacePaneThread(l, paneId, threadId));
        }
        return;
      }
      if (decision.kind === "split") {
        setLayoutState(splitPane(l, paneId, decision.side, threadId));
      }
    },
    [paneShowing, sendRaw],
  );
  const applyDropRef = useRef(applyDrop);
  applyDropRef.current = applyDrop;

  const clearDrag = useCallback(() => {
    window.removeEventListener("pointermove", onDragMoveRef.current);
    window.removeEventListener("keydown", onDragKeyRef.current);
    dragSessionRef.current = null;
    dropHoverRef.current = null;
    setDrag(null);
    setDropHover(null);
  }, []);

  const onDragMove = useCallback((e: PointerEvent) => {
    const s = dragSessionRef.current;
    if (!s) return;
    if (!s.engaged) {
      if (
        Math.hypot(e.clientX - s.startX, e.clientY - s.startY) <
        ENGAGE_DISTANCE_PX
      ) {
        return;
      }
      s.engaged = true;
      suppressClickRef.current = true;
    }
    setDrag({ id: s.id, title: s.title, x: e.clientX, y: e.clientY });
    const el = document
      .elementFromPoint(e.clientX, e.clientY)
      ?.closest("[data-pane-id]");
    const paneId = el?.getAttribute("data-pane-id") ?? null;
    if (el && paneId) {
      const r = el.getBoundingClientRect();
      const rect: Rect = { x: r.x, y: r.y, width: r.width, height: r.height };
      const hover = { paneId, zone: pickZone(rect, e.clientX, e.clientY), rect };
      dropHoverRef.current = hover;
      setDropHover(hover);
    } else {
      dropHoverRef.current = null;
      setDropHover(null);
    }
  }, []);
  const onDragMoveRef = useRef(onDragMove);
  onDragMoveRef.current = onDragMove;

  const onDragKey = useCallback(
    (e: KeyboardEvent) => {
      if (e.key === "Escape") clearDrag();
    },
    [clearDrag],
  );
  const onDragKeyRef = useRef(onDragKey);
  onDragKeyRef.current = onDragKey;

  const onDragUp = useCallback(() => {
    const s = dragSessionRef.current;
    const hover = dropHoverRef.current;
    clearDrag();
    if (s?.engaged && hover) {
      applyDropRef.current(hover.paneId, hover.zone, s.id);
    }
  }, [clearDrag]);

  /* Only same-device rows can drop into panes (one WS per device). */
  const onRowPointerDown = useCallback(
    (row: { id: string; title: string; rowDevice: string }) =>
      (e: ReactPointerEvent) => {
        if (e.button !== 0 || row.rowDevice !== device) return;
        dragSessionRef.current = {
          id: row.id,
          title: row.title,
          startX: e.clientX,
          startY: e.clientY,
          engaged: false,
        };
        window.addEventListener("pointermove", onDragMoveRef.current);
        window.addEventListener("pointerup", onDragUp, { once: true });
        window.addEventListener("keydown", onDragKeyRef.current);
      },
    [device, onDragUp],
  );

  /* Sidebar rows: bb-style global conversation list. With all_threads the
   * rows are every device's threads flattened and sorted by updatedAt
   * (desc); legacy servers fall back to the current-device thread_list. */
  type SidebarRow = ThreadInfo & { device: string; os?: string };
  const sidebarRows: SidebarRow[] = useMemo(() => {
    if (allThreads !== null) {
      return allThreads
        .flatMap((d) =>
          d.threads.map((th) => ({ ...th, device: d.device, os: d.os })),
        )
        .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
    }
    return threads.map((th) => ({ ...th, device, os: currentDevice?.os }));
  }, [allThreads, threads, device, currentDevice]);

  /* Sidebar device list: all_threads devices (online + offline, with OS)
   * when available, else the online-only REST list; the connected device
   * floats to the top, then id ascending. */
  const sidebarDevices: {
    id: string;
    os?: string;
    online: boolean;
    count?: number;
  }[] = useMemo(() => {
      const base =
        allThreads !== null
          ? allThreads.map((d) => ({
              id: d.device,
              os: d.os,
              online: d.online,
              count: d.threads.length,
            }))
          : devices.map((d) => ({ id: d.id, os: d.os, online: true }));
      return [...base].sort(
        (a, b) =>
          Number(b.id === device) - Number(a.id === device) ||
          Number(b.online) - Number(a.online) ||
          a.id.localeCompare(b.id),
      );
    }, [allThreads, devices, device]);

  /* Filter (All/Running) + title search + sort with the direction
   * multiplier. */
  const [threadSearchOpen, setThreadSearchOpen] = useState(false);
  const [threadQuery, setThreadQuery] = useState("");
  const filteredRows: SidebarRow[] = useMemo(() => {
    let rows =
      !sidebarView.showAll && sidebarView.showRunning
        ? sidebarRows.filter((r) => r.running)
        : sidebarRows;
    if (sidebarView.deviceFilter) {
      rows = rows.filter((r) => r.device === sidebarView.deviceFilter);
    }
    const q = threadQuery.trim().toLowerCase();
    if (q) {
      rows = rows.filter((r) =>
        (r.title || r.id).toLowerCase().includes(q) ||
        r.id.toLowerCase().includes(q) ||
        r.device.toLowerCase().includes(q),
      );
    }
    const mul = sidebarView.dir === "asc" ? 1 : -1;
    return [...rows].sort((a, b) => {
      if (sidebarView.sort === "alpha") {
        return (
          mul *
          ((a.title || a.id).localeCompare(b.title || b.id) ||
            a.id.localeCompare(b.id))
        );
      }
      if (sidebarView.sort === "created") {
        return mul * (threadCreatedMs(a.id) - threadCreatedMs(b.id));
      }
      return mul * a.updatedAt.localeCompare(b.updatedAt);
    });
  }, [sidebarRows, sidebarView, threadQuery]);

  /* Organize → By device: group the filtered rows; the current device
   * floats to the top, then device id ascending (bb section order). */
  const sidebarGroups: { device: string; os?: string; rows: SidebarRow[] }[] | null =
    useMemo(() => {
      if (sidebarView.organize !== "device") return null;
      const map = new Map<string, { device: string; os?: string; rows: SidebarRow[] }>();
      for (const row of filteredRows) {
        const g = map.get(row.device);
        if (g) {
          g.rows.push(row);
        } else {
          map.set(row.device, { device: row.device, os: row.os, rows: [row] });
        }
      }
      return [...map.values()].sort(
        (a, b) =>
          Number(b.device === device) - Number(a.device === device) ||
          a.device.localeCompare(b.device),
      );
    }, [filteredRows, sidebarView.organize, device]);

  const visibleGroups =
    sidebarGroups?.filter((g) => !sidebarView.hiddenGroups.includes(g.device)) ??
    null;
  /* Hidden groups that still have threads (the More … row restores them). */
  const hiddenExistingGroups =
    sidebarGroups?.filter((g) => sidebarView.hiddenGroups.includes(g.device)) ??
    [];

  const openSidebarThread = (row: SidebarRow) => {
    if (row.device === device) {
      if (row.id !== thread && wsRef.current?.readyState === WebSocket.OPEN) {
        wsRef.current.send(
          JSON.stringify({ op: "switch_thread", thread: row.id }),
        );
      }
    } else {
      /* Cross-device: switch in place — setDevice tears down and reconnects
       * the WS, and the auth_ok handler adopts the thread from the URL
       * param (and then strips it). */
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          next.set("thread", row.id);
          return next;
        },
        { replace: true },
      );
      setDevice(row.device);
      return;
    }
    /* Mobile keeps the left-right split: picking a thread does NOT
     * dismiss the drawer — the chat stays visible beside it; the user
     * closes via backdrop tap, edge swipe, or the header toggle. */
  };

  /* Sidebar click opens the thread in the FOCUSED pane: the pane following
   * the conn current switches threads; an explicit pane swaps its thread
   * (client-side, seeded by the pane's peek). Cross-device rows always
   * navigate the whole page. */
  const openInFocusedPane = (row: SidebarRow) => {
    if (row.device !== device) {
      openSidebarThread(row);
      return;
    }
    const l = layoutRef.current;
    const focused = findPane(l.root, l.focusedPaneId);
    if (!focused || focused.threadId === null || focused.threadId === thread) {
      openSidebarThread(row);
      return;
    }
    if (focused.threadId !== row.id) {
      setLayoutState(replacePaneThread(l, l.focusedPaneId, row.id));
    }
    /* Mobile keeps the left-right split: no auto-dismiss (see
     * openSidebarThread). */
  };

  /* Threads currently shown in any pane (sidebar highlight). */
  const openThreadIds = useMemo(() => {
    const ids = new Set<string>();
    for (const p of listPanes(layout.root)) ids.add(p.threadId ?? thread);
    return ids;
  }, [layout, thread]);

  /* One pane of the split layout: slim pane chrome (title, running dot,
   * close) when several panes are open + the chat surface. The pane whose
   * resolved thread is the conn current renders the page's primary sink;
   * every other pane owns a secondary sink fed by peek + forwarded frames. */
  const renderPane = (pane: PaneNode) => {
    const resolvedTid = pane.threadId ?? thread;
    const isCurrent = resolvedTid === thread;
    const focusedPane = layout.focusedPaneId === pane.paneId;
    const info = threads.find((th) => th.id === resolvedTid);
    const title = isCurrent
      ? currentThreadTitle
      : info?.title || resolvedTid;
    const multi = countPanes(layout.root) > 1;
    const registerAddToChat = (fn: ((text: string) => void) | null) => {
      if (fn) addToChatFnsRef.current.set(pane.paneId, fn);
      else addToChatFnsRef.current.delete(pane.paneId);
    };
    const shared = {
      t,
      sendRaw,
      ready,
      emptyHint: device ? t("agent.empty") : t("agent.selectDevice"),
      copiedId,
      onCopy: copy,
      model,
      models,
      onModelChange: selectModel,
      reasoning,
      onReasoningChange: selectReasoning,
      permMode,
      onPermModeChange: selectPermMode,
      device,
      deviceOs: currentDevice?.os,
      connStatus: status,
      contextWindow: currentContextWindow,
      onOpenFile: openFileInPanel,
      onOpenDiff: openDiffInPanel,
    };
    return (
      <div
        className="flex h-full min-h-0 flex-col bg-background"
        onPointerDown={() => {
          if (!focusedPane) {
            setLayoutState((l) => setFocus(l, pane.paneId));
          }
        }}
      >
        {multi && (
          <div
            className={cn(
              "flex h-8 shrink-0 items-center gap-1.5 border-b border-border-seam px-2 text-xs select-none",
              focusedPane ? "bg-accent/40" : "bg-transparent",
            )}
          >
            {info?.running && (
              <Icon
                name="Spinner"
                className="size-3 shrink-0 animate-spin text-muted-foreground"
              />
            )}
            <span
              className={cn(
                "min-w-0 flex-1 truncate",
                focusedPane ? "text-foreground" : "text-muted-foreground",
              )}
              title={title}
            >
              {title}
            </span>
            <button
              type="button"
              aria-label={t("agent.closePane")}
              className="flex h-5 w-5 shrink-0 cursor-pointer items-center justify-center rounded-sm text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground"
              onClick={() => setLayoutState((l) => removePane(l, pane.paneId))}
            >
              <Icon name="X" className="size-3.5" />
            </button>
          </div>
        )}
        {isCurrent ? (
          <ChatPaneView
            sink={primary}
            threadId={thread}
            {...shared}
            cwd={cwd}
            knownCwds={knownCwds}
            onSelectCwd={sendSetCwd}
            onCreateProject={() => setCreateProjectOpen(true)}
            branch={branch}
            onRegisterAddToChat={registerAddToChat}
          />
        ) : (
          <SecondaryChatPane
            threadId={resolvedTid}
            register={registerSecondary}
            onRegisterAddToChat={registerAddToChat}
            {...shared}
          />
        )}
      </div>
    );
  };

  /* Shared between the inline resizable panel and the narrow-screen drawer
   * (never mounted at the same time). */
  const rightPanelProps = {
    changes,
    diffs,
    connected: ready,
    info: {
      device,
      cwd,
      branch,
      model,
      thread,
      tokens: formatTokenCount(totalTokens),
    },
    /* Scopes the persisted tab strip; "" until a thread is attached. */
    threadKey: device && thread ? device + ":" + thread : "",
    fileStates,
    onRefresh: requestChanges,
    onRequestDiff: requestDiff,
    onRequestFile: requestFileRead,
    onSearchFiles: requestFileSearch,
    onAddToChat: insertIntoFocusedComposer,
    onHide: togglePanel,
    openFileSignal,
    openDiffSignal,
    panelFull,
    onSetDock: setDock,
    onToggleFull: togglePanelFull,
  };

  /* bb SidebarSplitContainer panel+handle slot: keyed so moving it before
   * /after the main panel (dock left|top vs. right|bottom) reorders the
   * DOM without remounting the panel — terminal sessions survive a Move. */
  const panelHandle = !isNarrow && panelOpen && (
    /* bb three-column handle: 0-wide transparent handle; the visible
     * 1px seam is a sibling span so hover/drag recolors only the
     * line, never the hit area. */
    <PanelResizeHandle
      key="handle"
      disabled={panelFull}
      className="group relative z-[25] w-0 shrink-0 overflow-visible bg-transparent transition-[width,opacity,background-color] duration-[var(--panel-collapse-duration,220ms)]"
      onDragging={(dragging) => {
        setPanelDragging(dragging);
        /* Persist only on drag end; onResize fires continuously (also
         * with intermediate values during the open/close transition). */
        if (!dragging) {
          localStorage.setItem(
            PANEL_WIDTH_KEY,
            String(Math.round(panelWidthRef.current * 100) / 100),
          );
        }
      }}
    >
      {verticalDock ? (
        <>
          <span className="absolute inset-x-0 top-1/2 h-4 -translate-y-1/2 cursor-row-resize touch-none bg-transparent pointer-coarse:h-8" />
          <span
            aria-hidden
            className={cn(
              "pointer-events-none absolute inset-x-0 top-full z-10 h-px transition-colors",
              panelDragging
                ? "bg-accent-foreground/50"
                : "bg-border-seam group-hover:bg-accent-foreground/35",
            )}
          />
        </>
      ) : (
        <>
          <span className="absolute inset-y-0 left-1/2 w-4 -translate-x-1/2 cursor-col-resize touch-none bg-transparent pointer-coarse:w-8" />
          <span
            aria-hidden
            className={cn(
              "pointer-events-none absolute inset-y-0 left-full z-10 w-px transition-colors",
              panelDragging
                ? "bg-accent-foreground/50"
                : "bg-border-seam group-hover:bg-accent-foreground/35",
            )}
          />
        </>
      )}
    </PanelResizeHandle>
  );
  const panelElement = !isNarrow && (
    /* Always mounted while wide: closed = imperative collapse() to 0,
     * so the 220ms transition animates both directions. */
    <Panel
      key="panel"
      ref={rightPanelRef}
      id="agent-right-panel"
      order={panelBefore ? 1 : 2}
      collapsible
      collapsedSize={0}
      defaultSize={
        panelOpen
          ? Math.min(
              100 - panelMinPct,
              Math.max(panelMinPct, panelWidthRef.current),
            )
          : 0
      }
      minSize={panelMinPct}
      maxSize={panelFull && panelOpen ? 100 : 100 - panelMinPct}
      onResize={(size) => {
        if (size > 0) panelWidthRef.current = size;
      }}
      className={cn(
        "min-w-0 overflow-clip transition-[flex-grow,flex-basis]",
        PANEL_TRANSITION_CLASS,
      )}
    >
      <AgentRightPanel {...rightPanelProps} />
    </Panel>
  );
  const dockSlot = (
    <Fragment key="dock">
      {panelBefore ? panelElement : panelHandle}
      {panelBefore ? panelHandle : panelElement}
    </Fragment>
  );

  /* bb mobile swipe: edge-drag right from the left 72px opens the drawer,
   * drag left closes it; vertical-dominant moves are ignored (bb intent
   * threshold 12px). The state flip animates via the CSS transition —
   * no live drag-follow. */
  const sidebarSwipeRef = useRef<{ x: number; y: number } | null>(null);
  const onInsetTouchStart = (e: React.TouchEvent) => {
    if (window.matchMedia("(min-width: 768px)").matches) return;
    const t = e.touches[0];
    sidebarSwipeRef.current = { x: t.clientX, y: t.clientY };
  };
  const onInsetTouchMove = (e: React.TouchEvent) => {
    const s = sidebarSwipeRef.current;
    if (!s) return;
    const t = e.touches[0];
    const dx = t.clientX - s.x;
    const dy = t.clientY - s.y;
    if (Math.abs(dx) < 12 && Math.abs(dy) < 12) return;
    sidebarSwipeRef.current = null;
    if (Math.abs(dy) > Math.abs(dx)) return;
    if (!sidebarOpen && !panelOpen && s.x <= 72 && dx > 48) {
      localStorage.setItem(SIDEBAR_KEY, "1");
      setSidebarOpen(true);
    } else if (
      !sidebarOpen &&
      !panelOpen &&
      s.x >= window.innerWidth - 72 &&
      dx < -48
    ) {
      /* bb CompactSecondaryPanelShelf: right-edge drag opens the shelf. */
      localStorage.setItem(PANEL_OPEN_KEY, "1");
      setPanelOpen(true);
    } else if (sidebarOpen && dx < -48) {
      localStorage.setItem(SIDEBAR_KEY, "0");
      setSidebarOpen(false);
    }
  };
  const onInsetTouchEnd = () => {
    sidebarSwipeRef.current = null;
  };

  /* bb shelf dismiss: drag right on the open right panel closes it. */
  const panelSwipeRef = useRef<{ x: number; y: number } | null>(null);
  const onPanelTouchStart = (e: React.TouchEvent) => {
    const t = e.touches[0];
    panelSwipeRef.current = { x: t.clientX, y: t.clientY };
  };
  const onPanelTouchMove = (e: React.TouchEvent) => {
    const s = panelSwipeRef.current;
    if (!s) return;
    const t = e.touches[0];
    const dx = t.clientX - s.x;
    const dy = t.clientY - s.y;
    if (Math.abs(dx) < 12 && Math.abs(dy) < 12) return;
    panelSwipeRef.current = null;
    if (Math.abs(dy) > Math.abs(dx)) return;
    if (dx > 48) {
      localStorage.setItem(PANEL_OPEN_KEY, "0");
      setPanelOpen(false);
    }
  };
  const onPanelTouchEnd = () => {
    panelSwipeRef.current = null;
  };

  const closeSidebar = () => {
    localStorage.setItem(SIDEBAR_KEY, "0");
    setSidebarOpen(false);
  };

  /* Sidebar resize: pointer-capture drag on the seam handle; width persists
   * on release only (live drags just move the var). */
  const sidebarDragRef = useRef<{ startX: number; startWidth: number } | null>(
    null,
  );
  const onSidebarHandleDown = (e: ReactPointerEvent<HTMLSpanElement>) => {
    if (e.button !== 0) return;
    e.preventDefault();
    // Capture keeps the drag alive outside the 16px strip; untrusted
    // (synthetic) events throw, so tolerate that.
    try {
      e.currentTarget.setPointerCapture(e.pointerId);
    } catch {
      /* untrusted event */
    }
    sidebarDragRef.current = { startX: e.clientX, startWidth: sidebarWidth };
    setSidebarDragging(true);
  };
  const onSidebarHandleMove = (e: ReactPointerEvent<HTMLSpanElement>) => {
    const d = sidebarDragRef.current;
    if (!d) return;
    setSidebarWidth(
      Math.min(
        SIDEBAR_MAX_WIDTH,
        Math.max(SIDEBAR_MIN_WIDTH, d.startWidth + e.clientX - d.startX),
      ),
    );
  };
  const onSidebarHandleUp = () => {
    if (!sidebarDragRef.current) return;
    sidebarDragRef.current = null;
    setSidebarDragging(false);
    setSidebarWidth((w) => {
      localStorage.setItem(SIDEBAR_WIDTH_KEY, String(Math.round(w)));
      return w;
    });
  };

  const renderSidebarRow = (th: SidebarRow) => (
    <div
      key={th.device + ":" + th.id}
      title={th.device + " · " + (th.title || th.id)}
      className={cn(
        "group/thread relative flex h-7 max-md:pointer-coarse:h-10 w-full cursor-pointer items-center gap-2 rounded-md pr-0 pl-2 text-sm text-sidebar-foreground transition-colors hover:bg-sidebar-accent hover:text-sidebar-accent-foreground",
        th.device === device && openThreadIds.has(th.id) && "bg-state-active",
      )}
      onPointerDown={onRowPointerDown({
        id: th.id,
        title: th.title || th.id,
        rowDevice: th.device,
      })}
      onClick={() => {
        /* A real drag ends in pointerup; the trailing click is swallowed. */
        if (suppressClickRef.current) {
          suppressClickRef.current = false;
          return;
        }
        openInFocusedPane(th);
      }}
    >
      {/* Device glyph on every row: it marks the device in the flat
       * mixed-device list, and grouped mode keeps it as the row icon. */}
      <OsIcon
        os={th.os}
        className="size-4 shrink-0 text-muted-foreground"
      />
      {renamingId === th.id ? (
        /* bb inline rename: input replaces the title; Enter/blur commits,
         * Escape cancels. */
        <input
          autoFocus
          defaultValue={th.title || th.id}
          className="h-5 min-w-0 flex-1 rounded-sm border border-timeline-accent bg-background px-1 text-sm outline-none"
          onClick={(e) => e.stopPropagation()}
          onPointerDown={(e) => e.stopPropagation()}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              renameThread(th.id, e.currentTarget.value.trim());
              setRenamingId(null);
            } else if (e.key === "Escape") {
              setRenamingId(null);
            }
          }}
          onBlur={(e) => {
            renameThread(th.id, e.currentTarget.value.trim());
            setRenamingId(null);
          }}
        />
      ) : (
        <span
          className={cn(
            "min-w-0 flex-1 truncate",
            th.device === device &&
              renamingId !== th.id &&
              "group-hover/thread:pr-7",
          )}
        >
          {th.title || th.id}
        </span>
      )}
      <span className="flex h-4 w-4 shrink-0 items-center justify-center">
        {th.running && (
          <Icon
            name="Spinner"
            className="size-4 animate-spin text-muted-foreground/50 motion-reduce:animate-none"
          />
        )}
      </span>
      {th.device === device && renamingId !== th.id && (
        /* bb row actions: hover-revealed ⋯ menu (rename inline / delete). */
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              type="button"
              aria-label={t("agent.threadActions")}
              className="absolute right-0 top-1/2 flex h-7 w-7 -translate-y-1/2 items-center justify-center rounded-md text-subtle-foreground opacity-0 transition-opacity group-hover/thread:opacity-100 max-md:pointer-coarse:opacity-100 hover:bg-state-hover hover:text-muted-foreground data-[state=open]:bg-state-active data-[state=open]:opacity-100"
              onClick={(e) => e.stopPropagation()}
              onPointerDown={(e) => e.stopPropagation()}
            >
              <Icon name="MoreHorizontal" className="size-4" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent side="right" align="start" sideOffset={8}>
            <DropdownMenuItem onSelect={() => setRenamingId(th.id)}>
              <Icon name="Edit" className="size-4 shrink-0" />
              {t("agent.rename")}
            </DropdownMenuItem>
            <DropdownMenuItem
              className="text-destructive-text focus:text-destructive-text"
              onSelect={() => setDeleteTarget(th.id)}
            >
              <Icon name="Trash2" className="size-4 shrink-0" />
              {t("agent.deleteThread")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </div>
  );

  /* Empty state: distinguish "nothing matched the search/filter" from
   * "no threads at all" so an active query never reads as data loss. */
  const sidebarEmpty = (
    <div className="flex items-center gap-2">
      <Icon
        name={threadQuery.trim() ? "Search" : "MessageSquare"}
        className="size-3.5 shrink-0 text-subtle-foreground/50"
      />
      <span className="text-xs leading-4 text-subtle-foreground/60">
        {threadQuery.trim() ? t("agent.noMatchingThreads") : t("agent.noThreads")}
      </span>
    </div>
  );

  return (
    <div className="relative flex h-full overflow-hidden">
      {/* Thread sidebar (bb ui/sidebar.tsx structure): the panel is ALWAYS
       * a fixed-width absolute column that slides horizontally — the
       * content never squishes. Desktop: a gap spacer (bb
       * data-sidebar="gap") in the flex row reserves layout space with a
       * 200ms linear width transition while the panel slides
       * (bb offcanvas: left 0→-w, 200ms linear). Compact (≤767px): bb
       * reveal drawer — panel min(86vw,360px) slides in, the inset
       * translates right with it keeping its own width, and a transparent
       * backdrop rides the inset as a click-catcher (220ms
       * cubic-bezier(0.32,0.72,0,1)). Transitions arm two frames after
       * mount via transitionsReady; resize drags skip them. */}
      <div
        aria-hidden
        style={{ "--sidebar-w": `${sidebarWidth}px` } as CSSProperties}
        className={cn(
          "hidden shrink-0 md:block",
          transitionsReady && !sidebarDragging
            ? "transition-[width] duration-200 ease-linear motion-reduce:transition-none"
            : "transition-none",
          sidebarOpen ? "md:w-[var(--sidebar-w)]" : "md:w-0",
        )}
      />
      <div
        className={cn(
          "absolute inset-0 z-10 bg-transparent md:hidden",
          transitionsReady &&
            "[transition:opacity_220ms_cubic-bezier(0.32,0.72,0,1),translate_220ms_cubic-bezier(0.32,0.72,0,1)] motion-reduce:transition-none",
          sidebarOpen
            ? "translate-x-[min(86vw,360px)] opacity-100"
            : "pointer-events-none opacity-0",
        )}
        onClick={closeSidebar}
      />
      <aside
        inert={!sidebarOpen}
        style={{ "--sidebar-w": `${sidebarWidth}px` } as CSSProperties}
        className={cn(
          "absolute inset-y-0 left-0 z-20 flex w-[min(86vw,360px)] shrink-0 flex-col overflow-hidden border-r border-border-seam bg-sidebar text-sidebar-foreground select-none md:w-[var(--sidebar-w)]",
          transitionsReady && !sidebarDragging
            ? sidebarOpen
              ? "max-md:[transition:transform_220ms_cubic-bezier(0.32,0.72,0,1),visibility_0s] md:[transition:transform_200ms_linear,visibility_0s] motion-reduce:transition-none"
              : "max-md:[transition:transform_220ms_cubic-bezier(0.32,0.72,0,1),visibility_0s_220ms] md:[transition:transform_200ms_linear,visibility_0s_200ms] motion-reduce:transition-none"
            : "transition-none",
          sidebarOpen ? "translate-x-0" : "invisible -translate-x-full",
        )}
      >
        <div className="flex h-full w-full shrink-0 flex-col md:w-[var(--sidebar-w)]">
          {/* bb sidebar navigation rows: primary actions + device panels as
           * full-width rows above the threads section. */}
          <div className="flex shrink-0 flex-col gap-1 px-2 pt-2 pb-2">
            <button
              type="button"
              disabled={!ready}
              onClick={() => {
                if (wsRef.current?.readyState === WebSocket.OPEN) {
                  wsRef.current.send(JSON.stringify({ op: "new_thread" }));
                  /* The fresh thread lands on the connected device: clear
                   * any device filter / search query that would hide it. */
                  if (sidebarView.deviceFilter) {
                    updateSidebarView({ deviceFilter: "" });
                  }
                  setThreadQuery("");
                }
                /* Mobile keeps the left-right split: no auto-dismiss. */
              }}
              className="flex h-7 max-md:pointer-coarse:h-10 w-full cursor-pointer items-center gap-2 rounded-md pr-0 pl-2 text-sm transition-colors hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 focus-visible:ring-sidebar-ring disabled:cursor-default disabled:opacity-70"
            >
              <Icon
                name="MessageSquarePlus"
                className="size-3.5 shrink-0 text-muted-foreground"
              />
              <span className="min-w-0 flex-1 truncate text-left">
                {t("agent.newThread")}
              </span>
            </button>
            <button
              type="button"
              aria-expanded={threadSearchOpen}
              onClick={() => {
                setThreadSearchOpen((v) => {
                  if (v) setThreadQuery("");
                  return !v;
                });
              }}
              className={cn(
                "flex h-7 max-md:pointer-coarse:h-10 w-full cursor-pointer items-center gap-2 rounded-md pr-0 pl-2 text-sm transition-colors hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 focus-visible:ring-sidebar-ring",
                threadSearchOpen && "bg-state-active",
              )}
            >
              <Icon
                name="Search"
                className="size-3.5 shrink-0 text-muted-foreground"
              />
              <span className="min-w-0 flex-1 truncate text-left">
                {t("agent.searchThreads")}
              </span>
            </button>
            {threadSearchOpen && (
              <div className="px-2 pb-0.5">
                <input
                  autoFocus
                  value={threadQuery}
                  onChange={(e) => setThreadQuery(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Escape") {
                      setThreadQuery("");
                      setThreadSearchOpen(false);
                    }
                  }}
                  placeholder={t("agent.searchThreads")}
                  className="h-7 w-full rounded-md border border-border bg-background px-2 text-sm outline-none placeholder:text-muted-foreground focus:border-timeline-accent"
                />
              </div>
            )}
          </div>
          {/* Device list: picking one filters the thread list below and
           * (when online) switches the connected device; 所有对话 = every
           * device's threads. Replaces the old header device picker. */}
          <div className="flex shrink-0 flex-col gap-1 px-2 pt-1.5">
            <div className="px-2 pb-0.5 text-xs leading-5 font-normal text-subtle-foreground/75">
              {t("agent.devices")}
            </div>
            <button
              type="button"
              onClick={() => updateSidebarView({ deviceFilter: "" })}
              className={cn(
                "flex h-7 max-md:pointer-coarse:h-10 w-full cursor-pointer items-center gap-2 rounded-md pr-0 pl-2 text-sm transition-colors hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 focus-visible:ring-sidebar-ring",
                !sidebarView.deviceFilter && "bg-state-active",
              )}
            >
              <Icon
                name="Inbox"
                className="size-3.5 shrink-0 text-muted-foreground"
              />
              <span className="min-w-0 flex-1 truncate text-left">
                {t("agent.allThreads")}
              </span>
            </button>
            {sidebarDevices.map((d) => (
              <button
                key={d.id}
                type="button"
                title={d.id}
                onClick={() => {
                  updateSidebarView({ deviceFilter: d.id });
                  if (d.online && d.id !== device) {
                    setDevice(d.id);
                  }
                }}
                className={cn(
                  "flex h-7 max-md:pointer-coarse:h-10 w-full cursor-pointer items-center gap-2 rounded-md pr-0 pl-2 text-sm transition-colors hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 focus-visible:ring-sidebar-ring",
                  sidebarView.deviceFilter === d.id && "bg-state-active",
                )}
              >
                <OsIcon
                  os={d.os}
                  className="size-3.5 shrink-0 text-muted-foreground"
                />
                <span className="min-w-0 flex-1 truncate text-left">{d.id}</span>
                {d.count !== undefined && d.count > 0 && (
                  <span className="shrink-0 pr-1 text-2xs text-muted-foreground tabular-nums">
                    {d.count}
                  </span>
                )}
                <span
                  className={cn(
                    "size-1.5 shrink-0 rounded-full",
                    d.online
                      ? "bg-success shadow-[0_0_4px_var(--success)]"
                      : "bg-muted-foreground/40",
                  )}
                />
              </button>
            ))}
          </div>
          {/* bb sidebar section header: label + new thread + list actions */}
          <div className="group/sidebar-section flex shrink-0 items-center justify-between gap-1 px-1 pt-1.5">
            <span
              className="min-w-0 truncate rounded-md pl-2 text-xs leading-5 font-normal text-subtle-foreground/75"
              title={t("agent.threads")}
            >
              {t("agent.threads")}
            </span>
            <span className="inline-flex h-7 shrink-0 items-center gap-0.5">
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon"
                    aria-label={t("agent.threadsActions")}
                    className="h-7 w-7 text-subtle-foreground hover:text-muted-foreground data-[state=open]:text-muted-foreground"
                  >
                    <Icon name="MoreHorizontal" className="size-4" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent side="right" align="start" sideOffset={8}>
                  <DropdownMenuSub>
                    <DropdownMenuSubTrigger>
                      <Icon name="Layers" className="size-4 shrink-0" />
                      {t("agent.organize")}
                    </DropdownMenuSubTrigger>
                    <DropdownMenuSubContent>
                      <DropdownMenuItem
                        onSelect={() => updateSidebarView({ organize: "flat" })}
                      >
                        <span className="flex-1">{t("agent.organizeFlat")}</span>
                        {sidebarView.organize === "flat" && (
                          <Icon name="Check" className="size-4 shrink-0" />
                        )}
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        onSelect={() => updateSidebarView({ organize: "device" })}
                      >
                        <span className="flex-1">{t("agent.organizeDevice")}</span>
                        {sidebarView.organize === "device" && (
                          <Icon name="Check" className="size-4 shrink-0" />
                        )}
                      </DropdownMenuItem>
                    </DropdownMenuSubContent>
                  </DropdownMenuSub>
                  <DropdownMenuSub>
                    <DropdownMenuSubTrigger>
                      <Icon name="ArrowUpDown" className="size-4 shrink-0" />
                      {t("agent.sortBy")}
                    </DropdownMenuSubTrigger>
                    <DropdownMenuSubContent>
                      {SIDEBAR_SORTS.map((o) => (
                        <DropdownMenuItem
                          key={o.id}
                          onSelect={() => selectSort(o.id)}
                        >
                          <span className="flex-1">{t(o.label)}</span>
                          {sidebarView.sort === o.id && (
                            <span className="ml-auto flex items-center gap-0.5">
                              <Icon name="Check" className="size-4" />
                              <Icon
                                name={
                                  sidebarView.dir === "asc"
                                    ? "ArrowUp"
                                    : "ArrowDown"
                                }
                                className="size-3.5"
                              />
                            </span>
                          )}
                        </DropdownMenuItem>
                      ))}
                    </DropdownMenuSubContent>
                  </DropdownMenuSub>
                  <DropdownMenuSub>
                    <DropdownMenuSubTrigger>
                      <Icon name="SlidersHorizontal" className="size-4 shrink-0" />
                      {t("agent.filter")}
                    </DropdownMenuSubTrigger>
                    <DropdownMenuSubContent>
                      {/* bb: at least one filter stays checked; the last
                       * checked item's uncheck is refused (hover title). */}
                      <DropdownMenuCheckboxItem
                        checked={sidebarView.showAll}
                        title={
                          sidebarView.showAll && !sidebarView.showRunning
                            ? t("agent.keepOneFilter")
                            : undefined
                        }
                        onCheckedChange={(v) => {
                          if (!v && !sidebarView.showRunning) return;
                          updateSidebarView({ showAll: !!v });
                        }}
                      >
                        {t("agent.filterAll")}
                      </DropdownMenuCheckboxItem>
                      <DropdownMenuCheckboxItem
                        checked={sidebarView.showRunning}
                        title={
                          sidebarView.showRunning && !sidebarView.showAll
                            ? t("agent.keepOneFilter")
                            : undefined
                        }
                        onCheckedChange={(v) => {
                          if (!v && !sidebarView.showAll) return;
                          updateSidebarView({ showRunning: !!v });
                        }}
                      >
                        {t("agent.filterRunning")}
                      </DropdownMenuCheckboxItem>
                    </DropdownMenuSubContent>
                  </DropdownMenuSub>
                </DropdownMenuContent>
              </DropdownMenu>
            </span>
          </div>
          <div className="flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto px-2 pb-2">
            {visibleGroups === null ? (
              filteredRows.length === 0 ? (
                sidebarEmpty
              ) : (
                filteredRows.map(renderSidebarRow)
              )
            ) : visibleGroups.length === 0 ? (
              sidebarEmpty
            ) : (
              visibleGroups.map((g) => {
                const collapsed = sidebarView.collapsedGroups.includes(g.device);
                return (
                <div key={g.device} className="flex flex-col gap-1">
                  {/* bb TopLevelSidebarSection: sticky label tier with a
                   * collapse chevron and a hover ⋯ → Hide from list. */}
                  <div className="group/devhead sticky top-0 z-10 flex h-7 max-md:pointer-coarse:h-10 items-center gap-1 rounded-md bg-sidebar pl-2 pr-0 text-xs text-subtle-foreground/75 select-none">
                    <span className="min-w-0 flex-1 truncate" title={g.device}>
                      {g.device}
                    </span>
                    <button
                      type="button"
                      aria-expanded={!collapsed}
                      aria-label={g.device}
                      onClick={() =>
                        updateSidebarView({
                          collapsedGroups: collapsed
                            ? sidebarView.collapsedGroups.filter(
                                (x) => x !== g.device,
                              )
                            : [...sidebarView.collapsedGroups, g.device],
                        })
                      }
                      className={cn(
                        "inline-flex h-7 w-7 shrink-0 cursor-pointer items-center justify-center rounded-md text-subtle-foreground transition-opacity hover:bg-state-hover hover:text-muted-foreground max-md:pointer-coarse:opacity-100",
                        collapsed
                          ? "opacity-100"
                          : "opacity-0 group-hover/devhead:opacity-100",
                      )}
                    >
                      <Icon
                        name="ChevronRight"
                        className={cn(
                          "size-3 transition-transform duration-150",
                          !collapsed && "rotate-90",
                        )}
                      />
                    </button>
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <button
                          type="button"
                          aria-label={t("agent.threadActions")}
                          className="inline-flex h-7 w-7 shrink-0 cursor-pointer items-center justify-center rounded-md text-subtle-foreground opacity-0 transition-opacity group-hover/devhead:opacity-100 max-md:pointer-coarse:opacity-100 hover:bg-state-hover hover:text-muted-foreground data-[state=open]:bg-state-active data-[state=open]:opacity-100"
                        >
                          <Icon name="MoreHorizontal" className="size-4" />
                        </button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent side="right" align="start" sideOffset={8}>
                        <DropdownMenuItem
                          onSelect={() =>
                            updateSidebarView({
                              hiddenGroups: [...sidebarView.hiddenGroups, g.device],
                            })
                          }
                        >
                          <Icon name="EyeOff" className="size-4 shrink-0" />
                          {t("agent.hideFromList")}
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                  {!collapsed && g.rows.map(renderSidebarRow)}
                </div>
                );
              })
            )}
          </div>
          {sidebarGroups !== null && hiddenExistingGroups.length > 0 && (
            /* bb More row: restores hidden device groups. */
            <div className="shrink-0 px-2 pb-1">
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <button
                    type="button"
                    className="flex h-7 max-md:pointer-coarse:h-10 w-full cursor-pointer items-center gap-2 rounded-md pl-2 pr-0 text-sm transition-colors hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 focus-visible:ring-sidebar-ring"
                  >
                    <Icon name="MoreHorizontal" className="size-4 shrink-0 text-subtle-foreground" />
                    {t("agent.more")}
                  </button>
                </DropdownMenuTrigger>
                <DropdownMenuContent side="right" align="start" sideOffset={8}>
                  {hiddenExistingGroups.map((g) => (
                    <DropdownMenuItem
                      key={g.device}
                      onSelect={() =>
                        updateSidebarView({
                          hiddenGroups: sidebarView.hiddenGroups.filter(
                            (x) => x !== g.device,
                          ),
                        })
                      }
                    >
                      <Icon name="Eye" className="size-4 shrink-0" />
                      <span className="min-w-0 flex-1 truncate">{g.device}</span>
                      <span className="ml-auto pl-2 text-2xs text-muted-foreground">
                        {t("agent.addToList")}
                      </span>
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          )}
          {/* Mobile-only: global page nav inside the same drawer (bb: one
           * button opens one drawer containing everything). */}
          <nav
            className="flex shrink-0 flex-col gap-0.5 border-t border-border-seam p-2 md:hidden"
            onClick={closeSidebar}
          >
            <MobileNavList />
          </nav>
          {/* Sidebar footer: current device + connection status */}
          <div className="flex shrink-0 items-center gap-1.5 border-t border-border-seam p-2 text-xs text-muted-foreground select-none">
            <span
              className={cn(
                "size-1.5 shrink-0 rounded-full",
                status === "connected"
                  ? "bg-success"
                  : status === "disconnected"
                    ? "bg-destructive"
                    : "bg-attention",
              )}
            />
            <span className="min-w-0 truncate">
              {device || t("agent.selectDeviceShort")}
            </span>
          </div>
        </div>
      </aside>
      {/* Sidebar resize seam (bb three-column handle pattern): 0-wide hit
       * container; visible 1px line brightens on hover/drag. */}
      {!isNarrow && sidebarOpen && (
        <div className="group relative z-20 hidden w-0 shrink-0 overflow-visible md:block">
          <span
            role="separator"
            aria-orientation="vertical"
            aria-label={t("agent.resizeSidebar")}
            className="absolute inset-y-0 left-0 w-3 -translate-x-1/2 cursor-col-resize touch-none"
            onPointerDown={onSidebarHandleDown}
            onPointerMove={onSidebarHandleMove}
            onPointerUp={onSidebarHandleUp}
            onPointerCancel={onSidebarHandleUp}
          />
          <span
            aria-hidden
            className={cn(
              "pointer-events-none absolute inset-y-0 left-0 z-10 w-px -translate-x-1/2 transition-colors",
              sidebarDragging
                ? "bg-sidebar-border"
                : "bg-transparent group-hover:bg-sidebar-border",
            )}
          />
        </div>
      )}

      <div
        ref={panelSizingRef}
        className={cn(
          "h-full min-w-0 flex-1",
          /* bb inset (ui/sidebar.tsx data-sidebar="inset"): translate
           * 220ms cubic-bezier(0.32,0.72,0,1) on compact — right by the
           * drawer width when the sidebar is open (chat keeps its width,
           * visible beside the list), fully left when the right-panel
           * shelf is open (panel takes precedence, bb source order). */
          transitionsReady &&
            !sidebarDragging &&
            "max-md:[transition:transform_220ms_cubic-bezier(0.32,0.72,0,1)] motion-reduce:transition-none",
          isNarrow && panelOpen
            ? "max-md:-translate-x-full"
            : sidebarOpen
              ? "max-md:translate-x-[min(86vw,360px)]"
              : "",
        )}
        style={
          {
            overflow: "clip",
            "--panel-collapse-duration":
              transitionsReady && !panelDragging ? "220ms" : "0ms",
          } as CSSProperties
        }
        onTouchStart={onInsetTouchStart}
        onTouchMove={onInsetTouchMove}
        onTouchEnd={onInsetTouchEnd}
      >
      <PanelGroup
        direction={verticalDock ? "vertical" : "horizontal"}
        className="@container h-full w-full"
        style={{ overflow: "clip" }}
      >
        {panelBefore && dockSlot}
        <Panel
          key="main"
          id="agent-main-panel"
          order={panelBefore ? 2 : 1}
          minSize={panelMinPct}
          className={cn(
            "min-w-0 overflow-clip transition-[flex-grow,flex-basis]",
            PANEL_TRANSITION_CLASS,
            panelFull && panelOpen && !isNarrow && "hidden",
          )}
        >
      <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col bg-background">
      {/* Slim header: sidebar toggle + status + thread title | device */}
      <div className="flex h-12 shrink-0 items-center gap-x-2 border-b border-border-seam px-3 text-xs text-muted-foreground select-none">
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              aria-label={t("agent.toggleSidebar")}
              aria-pressed={sidebarOpen}
              className="size-7"
              onClick={() => {
                const next = !sidebarOpen;
                localStorage.setItem(SIDEBAR_KEY, next ? "1" : "0");
                setSidebarOpen(next);
                /* Compact: the full-width shelf would cover the drawer —
                 * opening one dismisses the other. */
                if (next && window.innerWidth < 768 && panelOpen) {
                  localStorage.setItem(PANEL_OPEN_KEY, "0");
                  setPanelOpen(false);
                }
              }}
            >
              <Icon name="PanelLeft" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{t("agent.toggleSidebar")}</TooltipContent>
        </Tooltip>
        <span
          className={cn(
            "size-2 shrink-0 rounded-full",
            status === "connected"
              ? "bg-success"
              : status === "disconnected"
                ? "bg-destructive"
                : "bg-attention",
          )}
        />
        {reconnecting && (
          <span className="shrink-0">{t("agent.reconnecting")}</span>
        )}
        <span className="min-w-0 flex-1 truncate text-sm text-foreground">
          {currentThreadTitle}
        </span>
        {thread && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                aria-label={t("agent.threadActions")}
                className="size-7 shrink-0"
              >
                <Icon name="MoreHorizontal" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start">
              <DropdownMenuItem disabled={!ready} onSelect={openRenameDialog}>
                <Icon name="Edit" className="size-4 shrink-0" />
                {t("agent.rename")}
              </DropdownMenuItem>
              <DropdownMenuItem
                disabled={!ready}
                className="text-destructive-text focus:text-destructive-text"
                onSelect={() => setDeleteTarget(thread)}
              >
                <Icon name="Trash2" className="size-4 shrink-0" />
                {t("agent.deleteThread")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
        <div className="ml-auto flex min-w-0 items-center gap-2">
          {status === "disconnected" && device && (
            <Button variant="outline" size="sm" onClick={manualReconnect}>
              {t("agent.reconnect")}
            </Button>
          )}
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                aria-label={t("agent.togglePanel")}
                aria-pressed={panelOpen}
                className="size-7 shrink-0"
                onClick={togglePanel}
              >
                <Icon name="PanelRight" />
              </Button>
            </TooltipTrigger>
            <TooltipContent>{t("agent.togglePanel")}</TooltipContent>
          </Tooltip>
        </div>
      </div>

      <SplitContainer
        root={layout.root}
        renderPane={renderPane}
        onResize={onSplitResize}
      />

      {/* bb-style in-app rename dialog (no native prompt). */}
      <Dialog open={renameOpen} onOpenChange={setRenameOpen}>
        <DialogContent className="max-w-sm">
          <DialogHeader>
            <DialogTitle>{t("agent.renamePrompt")}</DialogTitle>
            <DialogDescription className="sr-only">
              {t("agent.renamePrompt")}
            </DialogDescription>
          </DialogHeader>
          <Input
            value={renameValue}
            autoFocus
            onChange={(e) => setRenameValue(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.nativeEvent.isComposing) {
                e.preventDefault();
                renameThread(thread, renameValue.trim());
                setRenameOpen(false);
              }
            }}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setRenameOpen(false)}>
              {t("common.cancel")}
            </Button>
            <Button
              disabled={!renameValue.trim()}
              onClick={() => {
                renameThread(thread, renameValue.trim());
                setRenameOpen(false);
              }}
            >
              {t("common.save")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete confirmation (no native confirm); the target is the current
       * thread from the header menu or any sidebar row. */}
      <AlertDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("agent.deleteThread")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("agent.deleteThreadConfirm")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={confirmDeleteThread}
            >
              {t("common.delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* bb ProjectPathDialog (create mode): browse the device filesystem,
       * submit sets the thread cwd. */}
      <CreateProjectDialog
        open={createProjectOpen}
        onOpenChange={setCreateProjectOpen}
        initialPath={cwd}
        listDir={listDir}
        mkdir={mkdirDir}
        onSubmit={sendSetCwd}
        t={t}
      />

      {/* Device password dialog */}
      <PasswordDialog
        open={status === "authing"}
        device={device}
        error={authError}
        onSubmit={submitPassword}
        t={t}
      />

      </div>
        </Panel>
        {!panelBefore && dockSlot}
      </PanelGroup>
      </div>

      {/* Narrow screens: bb CompactSecondaryPanelShelf — a full-width
       * shelf slides in from the right (220ms cubic-bezier(0.32,0.72,0,1))
       * while the inset slides fully left. The wrapper stays mounted so
       * both directions animate (visibility flips at transition end when
       * closing, bb's visibility_0s_linear_<dur> trick); the panel content
       * lazy-mounts on first open and stays alive afterwards so terminal
       * sessions survive a close. Drag right on the shelf dismisses it. */}
      {isNarrow && (
        <div
          inert={!panelOpen}
          className={cn(
            "fixed inset-y-0 right-0 z-30 w-full bg-background",
            transitionsReady
              ? panelOpen
                ? "[transition:transform_220ms_cubic-bezier(0.32,0.72,0,1),visibility_0s] motion-reduce:transition-none"
                : "[transition:transform_220ms_cubic-bezier(0.32,0.72,0,1),visibility_0s_220ms] motion-reduce:transition-none"
              : "transition-none",
            panelOpen
              ? "translate-x-0"
              : "invisible translate-x-full pointer-events-none",
          )}
          onTouchStart={onPanelTouchStart}
          onTouchMove={onPanelTouchMove}
          onTouchEnd={onPanelTouchEnd}
        >
          {(panelTouched || panelOpen) && (
            <AgentRightPanel
              {...rightPanelProps}
              /* Docking/full-screen do not apply to the compact shelf. */
              panelFull={undefined}
              onSetDock={undefined}
              onToggleFull={undefined}
              className="h-full w-full"
            />
          )}
        </div>
      )}

      {/* Thread drag: bb DragGhost follows the pointer; the hovered pane
       * shows its drop zone highlight. */}
      {drag && (
        <div
          className="pointer-events-none fixed z-50 flex max-w-64 items-center gap-2 rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground shadow-lift"
          style={{ left: drag.x + 12, top: drag.y + 8 }}
        >
          <Icon name="MessageSquare" className="size-4 shrink-0 text-muted-foreground" />
          <span className="truncate">{drag.title}</span>
        </div>
      )}
      {drag && dropHover && (
        <div
          className="pointer-events-none fixed z-40 rounded-md bg-accent/50 ring-1 ring-accent-foreground/40"
          style={(() => {
            const b = zoneBox(dropHover.rect, dropHover.zone);
            return { left: b.x, top: b.y, width: b.width, height: b.height };
          })()}
        />
      )}
    </div>
  );
}

/* ── Device password dialog ── */

function PasswordDialog({
  open,
  device,
  error,
  onSubmit,
  t,
}: {
  open: boolean;
  device: string;
  error: string;
  onSubmit: (pw: string) => void;
  t: (k: string, v?: Record<string, string | number>) => string;
}) {
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (open) {
      setValue(getDevicePassword(device));
      setBusy(false);
    }
  }, [open, device]);

  const submit = () => {
    if (!value) return;
    setBusy(true);
    onSubmit(value);
  };

  // Auto-submit a remembered password once per dialog open, like the vanilla page.
  const autoSubmitted = useRef(false);
  useEffect(() => {
    if (open && !autoSubmitted.current && value) {
      autoSubmitted.current = true;
      submit();
    }
    if (!open) autoSubmitted.current = false;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, value]);

  useEffect(() => {
    if (error) setBusy(false);
  }, [error]);

  return (
    <AlertDialog open={open}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("terminal.authTitle")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("terminal.authDesc", { id: device })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          <Input
            type="password"
            autoFocus
            value={value}
            placeholder={t("terminal.passwordPlaceholder")}
            onChange={(e) => setValue(e.target.value)}
          />
          {error && <div className="mt-2 text-xs text-destructive-text">{error}</div>}
          <AlertDialogFooter className="mt-4">
            <AlertDialogAction type="submit" disabled={busy || !value}>
              {busy ? t("common.connecting") : t("common.connect")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </form>
      </AlertDialogContent>
    </AlertDialog>
  );
}
