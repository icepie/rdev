import { Participants, parseParticipants, type Participant } from "../components/agent/participants";
import { createAutomationRevealTracker, parseAutomation, type AutomationView } from "../components/agent/automation";
import { createFileReadRequests } from "../components/agent/file-read-requests";
import { useThreadModels } from "../components/agent/use-thread-models";
import { createStreamBatch } from "../components/agent/stream-batch";
import { clearQueueReturns } from "../components/agent/queue-actions";
import { discardComposerTextDraft, reconcileComposerTextDrafts } from "../components/agent/use-composer-text-draft";
import { AgentConnectionError } from "../components/agent/connection-error";
import { useDevicePassword } from "../hooks/use-device-password";
import { shouldTogglePanel } from "../components/agent/panel-shortcut";
import { ThreadRenameDialog, type RenameTarget } from "../components/agent/thread-rename-dialog";
import { ThreadSidebar } from "../components/agent/thread-sidebar";
import { takePendingResponse } from "../components/agent/pending-response";
import { consumeChangesReply } from "../components/agent/changes-request";
import { SIDEBAR_MIN_WIDTH, SIDEBAR_MAX_WIDTH, SIDEBAR_DEFAULT_WIDTH } from "../components/workspace-sidebar";
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
import { useLocation, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { agentConversationURL, agentRouteState, readAgentRoute } from "../lib/agent-route";
import { setRailControl } from "../lib/rail-control";
import { useI18n } from "../lib/i18n";
import { useDevices } from "../lib/devices";
import {
  wsURL,
  rememberDevicePassword,
  forgetDevicePassword,
} from "../lib/api";
import { useServerCapabilities } from "../lib/server-capabilities";
import { requestWebNotificationPermission, showWebNotification, webNotificationPermission, type WebNotificationPermission } from "../lib/web-notifications";
import { cn } from "../lib/utils";
import { Button } from "../components/button";
import { COARSE_POINTER_HEADER_ICON_BUTTON_CLASS } from "../components/coarse-pointer-sizing";
import { useIsCompactViewport } from "../components/hooks/use-compact-viewport";
import { usePointerCoarse } from "../components/hooks/use-pointer-coarse";
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
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "../components/tooltip";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "../components/dropdown-menu";
import { Icon } from "../components/icon";
import { useCopy } from "../lib/clipboard";
import { SERVER_AGENT_ID } from "../components/agent/protocol";
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
  isLiveReplay,
  setLiveReplay,
  useThreadSink,
  type ThreadSinkOps,
} from "../components/agent/thread-sink";
import {
  asString,
  parseAllThreads,
  parsePermMode,
  parseReasoningLevel,
  parseStringArray,
  parseThreads,
  parseTodos,
  parseUsage,
  parseModelCatalog,
  parseWorking,
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
  reconcileThreadPanes,
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
const SIDEBAR_WIDTH_KEY = "rdev.agent.sidebar.width";
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

export function AgentPage() {
  const { t } = useI18n();
  const { devices: connectedDevices } = useDevices();
  const devices = useMemo(() => [{ id: SERVER_AGENT_ID, displayName: undefined, hostname: t("agent.server"), hasPassword: false, connectedAt: "" }, ...connectedDevices], [connectedDevices, t]);
  const [copiedId, copy] = useCopy();
  const [searchParams] = useSearchParams();
  const { threadId } = useParams();
  const location = useLocation();
  const navigate = useNavigate();
  const route = readAgentRoute(threadId, searchParams);
  const routeRef = useRef(route);
  routeRef.current = route;
  const locationRef = useRef(location);
  locationRef.current = location;
  const pendingRouteSwitch = useRef("");
  const pendingNewThread = useRef<string | null>(null);
  const [routeError, setRouteError] = useState("");
  const navigateThread = useCallback((targetDevice: string, targetThread = "", replace = false, preserveTurn = false) => {
    const params = new URLSearchParams(locationRef.current.search);
    if (!preserveTurn) params.delete("turn");
    if (!preserveTurn) params.delete("message");
    navigate(agentConversationURL(targetDevice, targetThread, params), { replace });
  }, [navigate]);

  const [device, setDeviceState] = useState("");
  const [status, setStatus] = useState<Status>("disconnected");
  const [authed, setAuthed] = useState(false);
  const [model, setModel] = useState("");
  const [models, setModels] = useState<ModelInfo[]>([]);
  const [authError, setAuthError] = useState("");
  const [notificationPermission, setNotificationPermission] = useState<WebNotificationPermission>(webNotificationPermission);
  const enableWebNotifications = useCallback(() => {
    void requestWebNotificationPermission().then(setNotificationPermission).catch(() => setNotificationPermission("denied"));
  }, []);
  const pendingPasswordRef = useRef<{ device: string; password: string } | null>(null);
  const [threads, setThreads] = useState<ThreadInfo[]>([]);
  const {modelFor, select: selectThreadModel, reset: resetThreadModels} = useThreadModels(threads, model);
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
  const [renameTarget, setRenameTarget] = useState<RenameTarget | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<{ id: string; device: string; title: string; running?: boolean } | null>(null);
  const [deleteBusy, setDeleteBusy] = useState(false);
  const pendingDeleteRef = useRef<{ id: string; device: string; requestID: string } | null>(null);
  const pendingRenameRef = useRef(new Map<string, { id: string }>());
  const [managementError, setManagementError] = useState("");
  /* bb-style inline rename: id of the sidebar row currently being edited. */
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const dirReqsRef = useRef(new Map<string, { resolve: (l: DirListing) => void; reject: (e: Error) => void; timer: number; id: string }>());
  const mkdirReqsRef = useRef(new Map<string, { resolve: () => void; reject: (e: Error) => void; timer: number; id: string }>());
  const [reconnecting, setReconnecting] = useState(false);
  const isNarrow = useIsCompactViewport();
  const isCoarse = usePointerCoarse();
  const [desktopSidebarOpen, setDesktopSidebarOpen] = useState(
    () => localStorage.getItem(SIDEBAR_KEY) !== "0",
  );
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false);
  const sidebarOpen = isNarrow ? mobileSidebarOpen : desktopSidebarOpen;
  const setSidebarOpen = useCallback((open: boolean) => {
    if (isNarrow) {
      setMobileSidebarOpen(open);
    } else {
      localStorage.setItem(SIDEBAR_KEY, open ? "1" : "0");
      setDesktopSidebarOpen(open);
    }
  }, [isNarrow]);
  useEffect(() => {
    setRailControl({ collapsed: !sidebarOpen, toggle: () => setSidebarOpen(!sidebarOpen) });
    return () => setRailControl(null);
  }, [sidebarOpen, setSidebarOpen]);
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
  const dismissedAutomationPanelsRef = useRef(new Set<string>());
  const automationRevealRef = useRef(createAutomationRevealTracker());
  const participantPasswordsRef = useRef(new Map<string,{device:string;password:string}>());
  const [participantsByThread,setParticipantsByThread] = useState<Record<string,Participant[]>>({});
  const [participantErrors,setParticipantErrors] = useState<Record<string,string>>({});
  const [automationViews, setAutomationViews] = useState<Record<string, AutomationView>>({});
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
  /* Incremented whenever the right-panel working context changes. Device
   * probes have no request id in the wire protocol, so this local epoch
   * prevents late replies from an older cwd/thread view overwriting current
   * panel state. */
  const panelRequestEpochRef = useRef(0);
  const diffRequestEpochRef = useRef(new Map<string, { epoch: number; id: string }>());
  const panelTranslatorRef = useRef(t);
  panelTranslatorRef.current = t;
  const fileRequestsRef = useRef<ReturnType<typeof createFileReadRequests> | null>(null);
  if (!fileRequestsRef.current) fileRequestsRef.current = createFileReadRequests();
  const changesRequestRef = useRef<{ epoch: number; id: string } | null>(null);
  const requestSeqRef = useRef(0);
  useEffect(() => {
    if (isNarrow && panelOpen) setMobileSidebarOpen(false);
  }, [isNarrow, panelOpen]);
  const [transitionsReady, setTransitionsReady] = useState(false);
  /* bb: transition disabled while dragging the divider, else every
   * pointermove animates 220ms toward the target → rubber-band lag. */
  const [panelDragging, setPanelDragging] = useState(false);
  const [groupWidth, setGroupWidth] = useState(0);

  const wsRef = useRef<WebSocket | null>(null);
  const streamBatchRef = useRef<ReturnType<typeof createStreamBatch> | null>(null);
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

  /* ── Right panel (bb three-column layout) ── */


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
      if (!next) dismissedAutomationPanelsRef.current.add(JSON.stringify([device, threadRef.current]));
      localStorage.setItem(PANEL_OPEN_KEY, next ? "1" : "0");
      /* Compact: the full-width shelf would cover the drawer — opening
       * one dismisses the other. */
      if (next && window.innerWidth < 768) {
        setMobileSidebarOpen(false);
      }
      return next;
    });
  }, [device]);

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


  /* mod+j toggles the right panel; skipped while typing. */
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (!shouldTogglePanel(e)) return;
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
      const id = `changes_${++requestSeqRef.current}`;
      const epoch = panelRequestEpochRef.current;
      changesRequestRef.current = { epoch, id };
      wsRef.current.send(JSON.stringify({ op: "get_changes", id }));
    }
  }, []);

  const requestDiff = useCallback((path: string) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      const id = `diff_${++requestSeqRef.current}`;
      diffRequestEpochRef.current.set(path, { epoch: panelRequestEpochRef.current, id });
      wsRef.current.send(JSON.stringify({ op: "get_diff", id, path }));
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
        const id = `file_${++requestSeqRef.current}`;
        fileRequestsRef.current!.begin(path, panelRequestEpochRef.current, id, () => {
          setFileStates(previous => previous[path]?.status === "loading" ? {
            ...previous, [path]: { ...previous[path], status: "error", message: t("files.previewTimeout") },
          } : previous);
        });
        wsRef.current.send(JSON.stringify({ op: "file_read", id, path }));
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
        reject: (error: Error) => void;
        timer: number;
        id: string;
      }
    >(),
  );
  const rejectPendingPanelRequests = useCallback((reason = "disconnected") => {
    const paths = fileRequestsRef.current!.clear();
    if (paths.length) setFileStates(previous => {
      const next = { ...previous };
      for (const path of paths) {
        if (next[path]?.status === "loading") next[path] = {
          ...next[path], status: "error", message: panelTranslatorRef.current("agent.status.disconnected"),
        };
      }
      return next;
    });
    for (const [key, pending] of dirReqsRef.current) {
      window.clearTimeout(pending.timer);
      pending.reject(new Error(reason));
      dirReqsRef.current.delete(key);
    }
    for (const [key, pending] of mkdirReqsRef.current) {
      window.clearTimeout(pending.timer);
      pending.reject(new Error(reason));
      mkdirReqsRef.current.delete(key);
    }
    for (const [key, pending] of searchReqsRef.current) {
      window.clearTimeout(pending.timer);
      pending.reject(new Error(reason));
      searchReqsRef.current.delete(key);
    }
  }, []);
  const requestFileSearch = useCallback((q: string): Promise<string[]> => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) return Promise.reject(new Error("offline"));
    const { promise, resolve, reject } = Promise.withResolvers<string[]>();
    const prev = searchReqsRef.current.get(q);
    if (prev) {
      window.clearTimeout(prev.timer);
      prev.reject(new Error("superseded"));
    }
    const id = `search_${++requestSeqRef.current}`;
    const timer = window.setTimeout(() => {
      const current = searchReqsRef.current.get(q);
      if (current?.id !== id) return;
      searchReqsRef.current.delete(q);
      reject(new Error("timeout"));
    }, 8000);
    searchReqsRef.current.set(q, { resolve, reject, timer, id });
    ws.send(JSON.stringify({ op: "search_files", id, q }));
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
    if (!ws || ws.readyState !== WebSocket.OPEN || statusRef.current !== "connected") return false;
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

  const handleMessage = useCallback((msg: Record<string, unknown>, sourceDevice: string) => {
    /* Thread routing: turn frames (delta/tool/done/…), approvals, queue
     * and changes/diff/thread_meta are tagged with their thread id. Frames
     * for a thread that is not the conn current go to the split pane
     * showing it (registry); with no pane they are dropped — switching to
     * that thread replays its history plus in-flight frames (live).
     * auth_ok and thread_switched are tagged too but must always pass:
     * they carry the NEW thread id and update threadRef themselves. */
    const op = String(msg.op);
    const frameThread = asString(msg.thread);
    if (msg.type === "response" && msg.command === "get_subagent_messages") {
      const id = asString(msg.id);
      const owner = [primaryOps, ...secondarySinksRef.current.values()].find(ops => ops.workspaceReplies.current.has(id));
      owner?.workspaceReplies.current.get(id)?.(msg);
      return;
    }
    if (op === "search_history") {
      const id = asString(msg.id);
      const owner = [primaryOps, ...secondarySinksRef.current.values()].find(ops => ops.workspaceReplies.current.has(id));
      if (owner) applyScopedMessage(owner, msg, t);
      return;
    }
    if (op === "thread_switched") {
      const id = asString(msg.id);
      const owner = [primaryOps, ...secondarySinksRef.current.values()].find(ops => ops.workspaceReplies.current.has(id));
      if (owner && id.startsWith("fork-")) {
        owner.workspaceReplies.current.get(id)?.(msg);
        navigateThread(sourceDevice, frameThread);
      }
    }
    if (op === "error" && frameThread && frameThread === routeRef.current.thread && pendingRouteSwitch.current === frameThread) {
      setRouteError(asString(msg.message) || t("common.error"));
      return;
    }
    if (!isLiveReplay() && document.visibilityState === "hidden" && (op === "done" || op === "error" || op === "stopped")) {
      const statusKey = op === "done" ? "agent.notificationDone" : op === "error" ? "agent.notificationError" : "agent.notificationStopped";
      showWebNotification(t("agent.notificationTitle"), {
        body: t(statusKey),
        tag: `rdev-agent-${sourceDevice}-${frameThread || "current"}`,
        onClick: () => window.focus(),
      });
    }
    if (["participants","auth_ok","thread_switched","reset_ok"].includes(op)) {
      const key=JSON.stringify([sourceDevice,frameThread]);
      const pending=participantPasswordsRef.current.get(String(msg.id??""));
      if(pending){participantPasswordsRef.current.delete(String(msg.id));if(!msg.error&&parseParticipants(msg.participants).some(p=>p.device===pending.device&&p.authorized))rememberDevicePassword(pending.device,pending.password);}

      setParticipantsByThread(current=>({...current,[key]:parseParticipants(msg.participants)}));
      setParticipantErrors(current=>({...current,[key]:typeof msg.error==="string"?msg.error:""}));
      if(op==="participants")return;
    }

    if (op === "automation" || op === "auth_ok" || op === "thread_switched" || op === "reset_ok") {
      const view = parseAutomation(msg.automation);
      const key = JSON.stringify([sourceDevice, frameThread]);
      setAutomationViews(current => {
        const next = { ...current };
        if (view) next[key] = view; else delete next[key];
        return next;
      });
      const reveal = automationRevealRef.current(key, view, op === "automation");
      if (reveal && !dismissedAutomationPanelsRef.current.has(key) && frameThread === threadRef.current) setPanelOpen(true);
      if (op === "automation") return;
    }
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
      case "thread_deleted":
        if (frameThread && asString(msg.device)) {
          discardComposerTextDraft(JSON.stringify([asString(msg.device), frameThread]));
        }
        if (!pendingDeleteRef.current ||
            (asString(msg.id) && pendingDeleteRef.current.requestID !== asString(msg.id)) ||
            pendingDeleteRef.current.id !== frameThread ||
            pendingDeleteRef.current.device !== asString(msg.device)) break;
        pendingDeleteRef.current = null;
        setDeleteBusy(false);
        setDeleteTarget((target) =>
          target?.id === frameThread && target.device === asString(msg.device) ? null : target,
        );
        setManagementError("");
        break;
      case "thread_pinned":
      case "thread_renamed":
        takePendingResponse(pendingRenameRef.current, asString(msg.id), JSON.stringify([asString(msg.device), frameThread]));
        break;
      case "thread_action_error":
        if (pendingDeleteRef.current &&
            (!asString(msg.id) || pendingDeleteRef.current.requestID === asString(msg.id)) &&
            pendingDeleteRef.current.id === frameThread &&
            pendingDeleteRef.current.device === asString(msg.device)) {
          pendingDeleteRef.current = null;
          setDeleteBusy(false);
          setManagementError(asString(msg.message) || t("common.error"));
          break;
        }
        if (takePendingResponse(pendingRenameRef.current, asString(msg.id), JSON.stringify([asString(msg.device), frameThread]))) {
          setManagementError(asString(msg.message) || t("common.error"));
        }
        break;
      case "auth":
        setStatus("authing");
        setAuthError("");
        break;
      case "auth_ok": {
        if (pendingPasswordRef.current) {
          rememberDevicePassword(pendingPasswordRef.current.device, pendingPasswordRef.current.password);
          pendingPasswordRef.current = null;
        }
        setAuthed(true);
        setStatus("connected");
        primaryOps.resetTransient();
        setAuthError("");
        setFileStates({});
        setChanges(null);
        setDiffs({});
        panelRequestEpochRef.current++;
        diffRequestEpochRef.current.clear();
        fileRequestsRef.current!.clear();
        changesRequestRef.current = null;
        primaryOps.setRunning(!!msg.running);
        setModels(parseModelCatalog(msg.models));
        primaryOps.setTurnUsage(parseUsage(msg.usage));
        /* Server-persisted model wins; localStorage is a display cache only. */
        const m = asString(msg.model);
        setModel(m);
        resetThreadModels();
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
        reconcileComposerTextDrafts(sourceDevice, authThread, msg.threads);
        const authThreads = parseThreads(msg.threads);
        setThreads(authThreads);
        pendingRouteSwitch.current = "";
        primaryOps.setQueue(parseStringArray(msg.queue));
        primaryOps.setQueueIds(parseStringArray(msg.queueIds));
        primaryOps.setQueueModes(parseStringArray(msg.queueModes));
        primaryOps.setQueueError("");
        clearQueueReturns(primaryOps.queueReturns.current);
        primaryOps.setTodos(parseTodos(msg.todos));
        applyScopedMessage(primaryOps, { ...msg, op: "autonomy" }, t);
        primaryOps.openToolIds.current.clear();
        const working = parseWorking(msg.working, !!msg.running);
        primaryOps.setWorkingSince(working.since);
        primaryOps.setCurrentTool(working.tool);
        /* Reconnect: secondary panes hold stale transcripts; they re-seed
         * via peek_thread when ready flips back to true. */
        for (const ops of secondarySinksRef.current.values()) {
          ops.reset();
        }
        threadsLoadedRef.current = true;
        primaryOps.turnOpen.current = false;
        primaryOps.restoreHistory(msg, "snapshot");
		primaryOps.setHistoryError(asString(msg.historyError));
        primaryOps.scrollDown(true);
        /* Reattach mid-turn: replay the in-flight frames (turn_start,
         * deltas, tool calls, pending approval) so the live transcript
         * looks exactly as if the tab had never gone away. */
        if (Array.isArray(msg.live)) {
          setLiveReplay(true);
          try {
            for (const raw of msg.live) {
              if (raw && typeof raw === "object") {
                handleMessage(raw as Record<string, unknown>, sourceDevice);
              }
            }
          } finally {
            setLiveReplay(false);
          }
        }
        reconnectAttempt.current = 0;
        setReconnecting(false);
        startPing();
        break;
      }
      case "auth_fail":
        pendingPasswordRef.current = null;
        forgetDevicePassword(sourceDevice);
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
        const responseID = asString(msg.id);
        if (!fileRequestsRef.current!.consume(path, panelRequestEpochRef.current, responseID)) break;
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
                previewUrl: asString(msg.previewUrl) || undefined,
              },
        }));
        break;
      }
      /* Replies to the create-project dialog's dir_list/dir_mkdir ops. */
      case "dir_entries": {
        const path = asString(msg.path);
        const pending = takePendingResponse(dirReqsRef.current, asString(msg.id), path);
        if (!pending) break;
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
        pending.resolve({ path, parent: asString(msg.parent), entries, roots });
        break;
      }
      case "dir_mkdir": {
        const path = asString(msg.path);
        const pending = takePendingResponse(mkdirReqsRef.current, asString(msg.id), path);
        if (!pending) break;
        window.clearTimeout(pending.timer);
        const message = asString(msg.message);
        if (message) pending.reject(new Error(message));
        else pending.resolve();
        break;
      }
      case "changes": {
        const responseID = asString(msg.id);
        if (!consumeChangesReply(changesRequestRef, panelRequestEpochRef.current, responseID)) break;
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
        const request = diffRequestEpochRef.current.get(path);
        const responseID = asString(msg.id);
        if (!request || request.epoch !== panelRequestEpochRef.current || (responseID && request.id !== responseID)) break;
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
        const pending = takePendingResponse(searchReqsRef.current, asString(msg.id), q);
        if (pending) {
          window.clearTimeout(pending.timer);
          if (asString(msg.message)) {
            pending.reject(new Error(asString(msg.message)));
            break;
          }
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
        rejectPendingPanelRequests("thread changed");
        primaryOps.resetTransient();
        const switchThread = asString(msg.thread);
        setThread(switchThread);
        threadRef.current = switchThread;
        pendingRouteSwitch.current = "";
        if (pendingNewThread.current === locationRef.current.key && routeRef.current.device === sourceDevice) {
          pendingNewThread.current = null;
          navigateThread(sourceDevice, switchThread);
        }
        primaryOps.setTodos(parseTodos(msg.todos));
        applyScopedMessage(primaryOps, { ...msg, op: "autonomy" }, t);
        primaryOps.setQueue(parseStringArray(msg.queue));
        primaryOps.setQueueIds(parseStringArray(msg.queueIds));
        primaryOps.setQueueModes(parseStringArray(msg.queueModes));
        primaryOps.setQueueError("");
        clearQueueReturns(primaryOps.queueReturns.current);
        primaryOps.setRunning(!!msg.running);
        const switchWorking = parseWorking(msg.working, !!msg.running);
        primaryOps.setWorkingSince(switchWorking.since);
        primaryOps.setCurrentTool(switchWorking.tool);
        primaryOps.setTurnUsage(parseUsage(msg.usage));
        /* File previews are thread-scoped (relative paths resolve against
         * the new thread's cwd): drop them; open tabs re-fetch lazily. */
        setFileStates({});
        /* Stale until the server pushes the new thread's changes. */
        setChanges(null);
        setDiffs({});
        changesRequestRef.current = null;
        diffRequestEpochRef.current.clear();
        fileRequestsRef.current!.clear();
        panelRequestEpochRef.current++;
        primaryOps.restoreHistory(msg, "snapshot");
		primaryOps.setHistoryError(asString(msg.historyError));
        primaryOps.scrollDown(true);
        const switchCwd = asString(msg.cwd);
        setCwd(switchCwd);
        setBranch(asString(msg.branch));
        if (switchCwd) setKnownCwds(previous => previous.includes(switchCwd) ? previous : [...previous, switchCwd]);
        /* The new thread's own model (threads may differ; the frame
         * carries the entry-level model only for legacy servers). */
        const switchModel = asString(msg.model);
        if (switchModel) {
          setModel(switchModel);
          localStorage.setItem(MODEL_KEY, switchModel);
        }
        /* Replay the in-flight turn frames when switching into a running
         * thread (isolated while viewing another thread). Replayed
         * turn_start must not restart the elapsed timer. */
        if (Array.isArray(msg.live)) {
          setLiveReplay(true);
          try {
            for (const raw of msg.live) {
              if (raw && typeof raw === "object") {
                handleMessage(raw as Record<string, unknown>, sourceDevice);
              }
            }
          } finally {
            setLiveReplay(false);
          }
        }
        break;
      }
    }
  }, [primaryOps, rejectPendingPanelRequests, navigateThread, startPing, t]);

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
    // Replacing a socket disables its onclose handler, so settle its pending
    // operations here as well as on an unexpected disconnect.
    rejectPendingPanelRequests("connection replaced");
    for (const ops of [primaryOps, ...secondarySinksRef.current.values()]) {
      [...ops.workspaceReplies.current.values()].forEach(reply => reply(null));
    }
    if (pendingDeleteRef.current || pendingRenameRef.current.size > 0) {
      setManagementError(t("common.disconnected"));
    }
    pendingDeleteRef.current = null;
    pendingRenameRef.current.clear();
    setDeleteBusy(false);
    if (wsRef.current) {
      streamBatchRef.current?.dispose();
      streamBatchRef.current = null;
      wsRef.current.onclose = null;
      wsRef.current.close();
      wsRef.current = null;
    }
    pendingPasswordRef.current = null;
    setAuthed(false);
    setStatus("connecting");
    setAuthError("");
    // Per-device state (transcript, threads, models, cwd…) is NOT cleared
    // here: a reconnect must keep the partial transcript visible until the
    // next auth_ok replaces it with the server-side truth. Device switches
    // clear it in setDevice before calling connect.

    const ws = new WebSocket(wsURL("/agent?device=" + encodeURIComponent(deviceId)));
    wsRef.current = ws;
    const streamBatch = createStreamBatch(msg => {
      if (wsRef.current === ws) handleMessage(msg, deviceId);
    });
    streamBatchRef.current = streamBatch;
    // These belong to this socket, not the previous connection's React state.
    let authenticated = false;
    let rejected = false;
    ws.onmessage = (evt) => {
      if (wsRef.current !== ws) return;
      let msg: Record<string, unknown>;
      try {
        msg = JSON.parse(String(evt.data));
      } catch {
        return;
      }
      if (msg.op === "auth_ok") authenticated = true;
      if (msg.op === "error" && !authenticated && !asString(msg.thread)) {
        // Setup failures arrive as an error followed by an empty close reason;
        // misconfiguration may leave the socket open. Neither is a network retry.
        rejected = true;
        setAuthed(false);
        setStatus("disconnected");
        setReconnecting(false);
        setAuthError(msg.code === "offline_admin_required" ? t("agent.offlineAdminRequired") : asString(msg.message) || t("common.error"));
        clearPing();
        ws.close();
        return;
      }
      streamBatch.push(msg);
    };
    ws.onclose = (event) => {
      if (wsRef.current !== ws) return;
      streamBatch.flush();
      streamBatch.dispose();
      streamBatchRef.current = null;
      clearPing();
      rejectPendingPanelRequests();
      for (const ops of [primaryOps, ...secondarySinksRef.current.values()]) {
        [...ops.workspaceReplies.current.values()].forEach(reply => reply(null));
      }
      setAuthed(false);
      if (primaryOps.turnOpen.current) primaryOps.finishTurn();
      primaryOps.resetTransient();
      primaryOps.setRunning(false);
      for (const ops of secondarySinksRef.current.values()) {
        if (ops.turnOpen.current) ops.finishTurn();
        ops.resetTransient();
        ops.setRunning(false);
      }
      setStatus("disconnected");
      pendingDeleteRef.current = null;
      pendingRenameRef.current.clear();
      setDeleteBusy(false);
      if (rejected) return;
      // A clean 1000 close with a reason is the server rejecting the
      // session at open time (invalid configuration). Offline devices use
      // the retryable 1013 close code; for permanent failures surface the
      // reason and stop.
      if (event.code === 1000 && event.reason) {
        setReconnecting(false);
        setAuthError(event.reason);
        return;
      }
      scheduleReconnect(deviceId);
    };
    ws.onerror = () => {
      if (wsRef.current !== ws) return;
      if (statusRef.current !== "disconnected") {
        setStatus("disconnected");
      }
    };
  }, [clearPing, clearReconnectTimer, handleMessage, primaryOps, rejectPendingPanelRequests, scheduleReconnect, t]);
  connectRef.current = connect;

  const activateDevice = useCallback((id: string) => {
    setDeviceState(id);
    rejectPendingPanelRequests("device changed");
    pendingDeleteRef.current = null;
    pendingRenameRef.current.clear();
    setDeleteBusy(false);
    setManagementError("");
    pendingRouteSwitch.current = "";
    pendingNewThread.current = null;
    primaryOps.reset();
    for (const ops of secondarySinksRef.current.values()) ops.reset();
    setModels([]);
    setModel("");
    resetThreadModels();
    setThreads([]);
    setThread("");
    threadRef.current = "";
    setAuthError("");
    setPermMode("write");
    setReasoning(parseReasoningLevel(localStorage.getItem(REASONING_KEY)) ?? "medium");
    threadsLoadedRef.current = false;
    clearReconnectTimer();
    reconnectAttempt.current = 0;
    setReconnecting(false);
    clearPing();
    if (wsRef.current) {
      streamBatchRef.current?.dispose();
      streamBatchRef.current = null;
      wsRef.current.onclose = null;
      wsRef.current.close();
      wsRef.current = null;
    }
    setAuthed(false);
    setCwd("");
    setBranch("");
    setKnownCwds([]);
    setFileStates({});
    setChanges(null);
    setDiffs({});
    panelRequestEpochRef.current++;
    diffRequestEpochRef.current.clear();
    fileRequestsRef.current!.clear();
    setStatus("disconnected");
    if (id) connect(id);
  }, [clearPing, clearReconnectTimer, connect, primaryOps, rejectPendingPanelRequests]);

  const setDevice = useCallback((id: string) => navigateThread(id), [navigateThread]);
  const routeState = agentRouteState(route, device, thread, authed, threads);
  const routeBlocked = routeState !== "selected" && routeState !== "default";

  // URL is the selection source of truth, including browser Back/Forward.
  // Explicit offline devices stay selected; the socket owns their recovery.
  useEffect(() => {
    setRouteError("");
    pendingRouteSwitch.current = "";
    pendingNewThread.current = null;
    if (route.device !== device) activateDevice(route.device);
  }, [route.device, route.thread, location.key, device, activateDevice]);

  useEffect(() => {
    if (routeState === "default" && thread) {
      navigateThread(device, thread, true, true);
    } else if (routeState === "selected" && (!threadId || searchParams.has("thread"))) {
      navigateThread(device, thread, true, true);
    } else if (routeState === "switch" && !routeError && pendingNewThread.current === null && pendingRouteSwitch.current !== route.thread) {
      pendingRouteSwitch.current = route.thread;
      wsRef.current?.send(JSON.stringify({ op: "switch_thread", thread: route.thread }));
    }
  }, [routeState, route.thread, routeError, device, thread, threadId, searchParams, navigateThread]);

  useEffect(
    () => () => {
      clearReconnectTimer();
      clearPing();
      fileRequestsRef.current!.clear();
      rejectPendingPanelRequests();
      if (wsRef.current) {
        streamBatchRef.current?.dispose();
        streamBatchRef.current = null;
        wsRef.current.onclose = null;
        wsRef.current.close();
        wsRef.current = null;
      }
    },
    [clearPing, clearReconnectTimer, rejectPendingPanelRequests],
  );

  /* ── Actions ── */

  const ready = !routeBlocked && !routeError && authed && status === "connected" && wsRef.current?.readyState === WebSocket.OPEN;

  const newThread = () => {
    if (!authed || wsRef.current?.readyState !== WebSocket.OPEN) return;
    pendingNewThread.current = locationRef.current.key;
    wsRef.current.send(JSON.stringify({ op: "new_thread" }));
  };

  /* Manual reconnect: immediate, resets the backoff counter. */
  const manualReconnect = useCallback(() => {
    if (device) connect(device);
  }, [connect, device]);

  const sendSetCwd = (path: string) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      panelRequestEpochRef.current++;
      diffRequestEpochRef.current.clear();
      fileRequestsRef.current!.clear();
      changesRequestRef.current = null;
      wsRef.current.send(JSON.stringify({ op: "set_cwd", path }));
      setFileStates({});
    }
  };

  /* IDs correlate replies even when the server resolves an empty path to
   * cwd. Repeating a path supersedes its previous request. */
  const listDir = useCallback((path: string): Promise<DirListing> => {
    const { promise, resolve, reject } = Promise.withResolvers<DirListing>();
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      reject(new Error("offline"));
      return promise;
    }
    const id = `dir_${++requestSeqRef.current}`;
    const previous = dirReqsRef.current.get(path);
    if (previous) {
      window.clearTimeout(previous.timer);
      previous.reject(new Error("superseded"));
    }
    const timer = window.setTimeout(() => {
      const current = dirReqsRef.current.get(path);
      if (current?.id !== id) return;
      dirReqsRef.current.delete(path);
      reject(new Error("timeout"));
    }, 20000);
    dirReqsRef.current.set(path, { resolve, reject, timer, id });
    ws.send(JSON.stringify({ op: "dir_list", id, path }));
    return promise;
  }, []);

  const mkdirDir = useCallback((path: string): Promise<void> => {
    const { promise, resolve, reject } = Promise.withResolvers<void>();
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      reject(new Error("offline"));
      return promise;
    }
    const id = `mkdir_${++requestSeqRef.current}`;
    const previous = mkdirReqsRef.current.get(path);
    if (previous) {
      window.clearTimeout(previous.timer);
      previous.reject(new Error("superseded"));
    }
    const timer = window.setTimeout(() => {
      const current = mkdirReqsRef.current.get(path);
      if (current?.id !== id) return;
      mkdirReqsRef.current.delete(path);
      reject(new Error("timeout"));
    }, 20000);
    mkdirReqsRef.current.set(path, { resolve, reject, timer, id });
    ws.send(JSON.stringify({ op: "dir_mkdir", id, path }));
    return promise;
  }, []);

  const renameThread = (id: string, title: string, targetDevice = device): boolean => {
    if (!title || !id) return false;
    setManagementError("");
    const requestID = `rename_${++requestSeqRef.current}`;
    const sent = sendRaw({ op: "rename_thread", id: requestID, thread: id, device: targetDevice, title });
    if (sent) pendingRenameRef.current.set(JSON.stringify([targetDevice, id]), { id: requestID });
    else setManagementError(t("common.disconnected"));
    return sent;
  };

  const openRenameDialog = () => {
    setRenameTarget({ id: thread, device, title: currentThreadTitle });
    setRenameOpen(true);
  };

  const confirmDeleteThread = () => {
    if (!deleteTarget || deleteBusy) return;
    setManagementError("");
    const requestID = `delete_${++requestSeqRef.current}`;
    if (sendRaw({ op: "delete_thread", id: requestID, thread: deleteTarget.id, device: deleteTarget.device })) {
      pendingDeleteRef.current = { id: deleteTarget.id, device: deleteTarget.device, requestID };
      setDeleteBusy(true);
    }
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

  const submitPassword = (pw: string): boolean => {
    const ws = wsRef.current;
    if (!pw || ws?.readyState !== WebSocket.OPEN) {
      setAuthError(t("common.disconnected"));
      return false;
    }
    setAuthError("");
    ws.send(JSON.stringify({ op: "auth", password: pw }));
    pendingPasswordRef.current = { device, password: pw };
    return true;
  };

  const currentThreadTitle = routeBlocked
    ? route.thread || t("common.loading")
    : threads.find((th) => th.id === thread)?.title || t("agent.newThread");

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

  useEffect(() => {
    if (routeState !== "selected") return;
    setLayoutState(current => {
      const pane = listPanes(current.root).find(item => item.threadId === null || item.threadId === route.thread);
      return pane ? setFocus(current, pane.paneId) : current;
    });
  }, [routeState, route.thread]);

  /* Panes pinned to deleted threads close (bb keeps layout consistent with
   * the conversation list). Gated on a loaded list so the empty pre-auth
   * state does not nuke restored panes. */
  useEffect(() => {
    if (!threadsLoadedRef.current) return;
    const ids = new Set(threads.map((th) => th.id));
    setLayoutState(l => reconcileThreadPanes(l, thread, ids));
  }, [threads, thread]);

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
            navigateThread(device, threadId);
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
    [paneShowing, navigateThread, device],
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

  /* Threads currently shown in any pane (sidebar highlight). */
  const openThreadIds = useMemo(() => {
    const ids = new Set<string>();
    for (const p of listPanes(layout.root)) {
      if (!isNarrow || p.paneId === layout.focusedPaneId) ids.add(p.threadId ?? thread);
    }
    return ids;
  }, [layout, thread, isNarrow]);

  /* One pane of the split layout: slim pane chrome (title, running dot,
   * close) when several panes are open + the chat surface. The pane whose
   * resolved thread is the conn current renders the page's primary sink;
   * every other pane owns a secondary sink fed by peek + forwarded frames. */
  const renderPane = (pane: PaneNode) => {
    const resolvedTid = pane.threadId ?? thread;
    const isCurrent = resolvedTid === thread;
    const focusedPane = layout.focusedPaneId === pane.paneId;
    const info = threads.find((th) => th.id === resolvedTid);
    const paneModel = modelFor(resolvedTid);
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
      offline: device !== SERVER_AGENT_ID && !connectedDevices.some((item) => item.id === device) && !(participantsByThread[JSON.stringify([device,resolvedTid])]??[]).some(p=>p.authorized&&connectedDevices.some(d=>d.id===p.device)),
      emptyHint: !device ? t("agent.selectDevice")
        : !ready ? (reconnecting ? t("agent.reconnecting") : t(`agent.status.${status}`))
        : device === SERVER_AGENT_ID ? t("agent.serverEmpty") : t("agent.empty"),
      copiedId,
      onCopy: copy,
      model: paneModel,
      models,
      onModelChange: (id: string) => selectThreadModel(resolvedTid, id),
      reasoning,
      onReasoningChange: selectReasoning,
      permMode,
      onPermModeChange: selectPermMode,
      device,
      deviceOs: currentDevice?.os,
      connStatus: status,
      contextWindow: models.find(item => item.id === paneModel)?.contextWindow,
      onOpenFile: openFileInPanel,
    };
    return (
      <div
        className="flex h-full min-h-0 flex-col bg-background"
        onPointerDown={() => {
          if (!focusedPane) {
            setLayoutState((l) => setFocus(l, pane.paneId));
          }
        }}
        onFocus={() => {
          if (!focusedPane) setLayoutState((l) => setFocus(l, pane.paneId));
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
            key={device}
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
            key={device + ":" + resolvedTid}
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
  const automationView = automationViews[JSON.stringify([device, thread])] ?? null;
  const rightPanelProps = {
    automation: automationView,
    visible: panelOpen,
    changes,
    diffs,
    connected: ready,
    info: {
      device,
      cwd,
      branch,
      model,
      thread,
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
    panelFull,
    onSetDock: setDock,
    onToggleFull: togglePanelFull,
  };

  /* bb SidebarSplitContainer panel+handle slot: keyed so moving it before
   * /after the main panel (dock left|top vs. right|bottom) reorders the
   * DOM without remounting the panel — terminal sessions survive a Move. */
  const panelHandle = !isNarrow && (
    /* bb three-column handle: 0-wide transparent handle; the visible
     * 1px seam is a sibling span so hover/drag recolors only the
     * line, never the hit area. */
    <PanelResizeHandle
      key="handle"
      disabled={panelFull || !panelOpen}
      aria-hidden={!panelOpen}
      tabIndex={panelOpen ? 0 : -1}
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
      maxSize={panelFull && panelOpen && !routeBlocked ? 100 : 100 - panelMinPct}
      onResize={(size) => {
        if (size > 0) panelWidthRef.current = size;
      }}
      className={cn(
        "min-w-0 overflow-clip transition-[flex-grow,flex-basis]",
        PANEL_TRANSITION_CLASS,
      )}
    >
      <div className={cn("h-full", routeBlocked && "invisible")} inert={routeBlocked}>
        <AgentRightPanel {...rightPanelProps} />
      </div>
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
      dismissedAutomationPanelsRef.current.add(JSON.stringify([device, threadRef.current]));
      localStorage.setItem(PANEL_OPEN_KEY, "0");
      setPanelOpen(false);
    }
  };
  const onPanelTouchEnd = () => {
    panelSwipeRef.current = null;
  };

  const closeSidebar = () => {
    setSidebarOpen(false);
  };

  /* Sidebar width persists on seam release (the sidebar owns the live drag
   * and commits once). */
  const commitSidebarWidth = () => {
    setSidebarWidth((w) => {
      localStorage.setItem(SIDEBAR_WIDTH_KEY, String(Math.round(w)));
      return w;
    });
  };

  /* bb row action: pin/unpin the thread on its own device. */
  const pinSidebarThread = (th: ThreadInfo & { device: string }) => {
    const id = crypto.randomUUID();
    if (sendRaw({ op: "pin_thread", id, device: th.device, thread: th.id, pinned: !th.pinnedAt })) {
      pendingRenameRef.current.set(JSON.stringify([th.device, th.id]), { id });
    } else setManagementError(t("common.disconnected"));
  };

  /* Sidebar new-thread button: only clear the filter/query when the frame
   * actually goes out (the fresh thread must stay visible). */
  const requestNewThread = (): boolean => {
    if (wsRef.current?.readyState !== WebSocket.OPEN) return false;
    newThread();
    return true;
  };

  return (
    <div className="relative flex h-full overflow-hidden [--agent-mobile-sidebar-width:min(calc(100vw_-_48px),360px)]">
      {/* BB offcanvas on desktop; below 768px reveal the fixed-width drawer
       * and translate the full-width conversation without squeezing it. */}
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
      {isNarrow && (
        <div
          className={cn(
            "absolute inset-0 z-10 bg-transparent",
            transitionsReady &&
              "[transition:opacity_220ms_cubic-bezier(0.32,0.72,0,1),translate_220ms_cubic-bezier(0.32,0.72,0,1)] motion-reduce:transition-none",
            sidebarOpen
              ? "translate-x-[var(--agent-mobile-sidebar-width)] opacity-100"
              : "pointer-events-none opacity-0",
          )}
          onClick={closeSidebar}
        />
      )}
      <ThreadSidebar
        devices={devices}
        allThreads={allThreads}
        threads={threads}
        device={device}
        currentDevice={currentDevice}
        open={sidebarOpen}
        width={sidebarWidth}
        dragging={sidebarDragging}
        transitionsReady={transitionsReady}
        narrow={isNarrow}
        coarse={isCoarse}
        status={status}
        authed={authed}
        ready={ready}
        openThreadIds={openThreadIds}
        renamingId={renamingId}
        managementError={deleteTarget ? "" : managementError}
        suppressClickRef={suppressClickRef}
        onSelectThread={navigateThread}
        onRowPointerDown={onRowPointerDown}
        onRenameThread={renameThread}
        onRenameChange={setRenamingId}
        onPinThread={pinSidebarThread}
        onDeleteThread={setDeleteTarget}
        onNewThread={requestNewThread}
        onReconnect={manualReconnect}
        onSelectDevice={setDevice}
        onClose={closeSidebar}
        onResize={setSidebarWidth}
        onResizeCommit={commitSidebarWidth}
        onDraggingChange={setSidebarDragging}
      />

      <div
        ref={panelSizingRef}
        className={cn(
          "h-full min-w-0 flex-1",
          /* Compact shelves translate the conversation, never resize it. */
          transitionsReady &&
            !sidebarDragging &&
            isNarrow &&
            "[transition:translate_220ms_cubic-bezier(0.32,0.72,0,1)] motion-reduce:transition-none",
          isNarrow && panelOpen && !routeBlocked
            ? "-translate-x-full"
            : isNarrow && sidebarOpen
              ? "translate-x-[var(--agent-mobile-sidebar-width)]"
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
            panelFull && panelOpen && !isNarrow && !routeBlocked && "hidden",
          )}
        >
      <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col bg-background">
      {/* Slim header: sidebar toggle + status + thread title | device */}
      <div className="flex h-12 shrink-0 items-center gap-x-2 border-b border-border-seam px-3 text-xs text-muted-foreground select-none">

        <Button variant="ghost" size="icon" aria-label={t("rail.expand")} aria-expanded={sidebarOpen}
          onClick={() => setSidebarOpen(true)}
          className={cn(COARSE_POINTER_HEADER_ICON_BUTTON_CLASS, "shrink-0 md:hidden")}>
          <Icon name="PanelLeft" />
        </Button>
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
        {!routeBlocked && thread && device !== SERVER_AGENT_ID && <Participants key={JSON.stringify([device,thread])} primary={device} rows={participantsByThread[JSON.stringify([device,thread])]??[]} error={participantErrors[JSON.stringify([device,thread])]??""} ready={ready} t={t} onRefresh={()=>{sendRaw({op:"participants",mode:"list",thread});}} onUpdate={(mode,target,password,cwd)=>{const id=`participants_${++requestSeqRef.current}`;if(password)participantPasswordsRef.current.set(id,{device:target,password});if(!sendRaw({op:"participants",mode,thread,id,device:target,password,path:cwd}))participantPasswordsRef.current.delete(id);}} />}
        {thread && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                aria-label={t("agent.threadActions")}
                className={cn(COARSE_POINTER_HEADER_ICON_BUTTON_CLASS, "shrink-0")}
              >
                <Icon name="MoreHorizontal" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start">
              <DropdownMenuItem disabled={!ready} onSelect={() => copy("conversation-link", new URL(agentConversationURL(device, thread), window.location.origin).href)}>
                <Icon name="Copy" className="size-4 shrink-0" />
                {t(copiedId === "conversation-link" ? "common.copied" : "agent.copyLink")}
              </DropdownMenuItem>
              <DropdownMenuItem
                disabled={notificationPermission === "granted" || notificationPermission === "denied" || notificationPermission === "unsupported"}
                onSelect={enableWebNotifications}
              >
                {t(notificationPermission === "granted" ? "agent.notificationsEnabled" : notificationPermission === "denied" ? "agent.notificationsBlocked" : "agent.enableNotifications")}
              </DropdownMenuItem>
              <DropdownMenuItem disabled={!ready} onSelect={openRenameDialog}>
                <Icon name="Edit" className="size-4 shrink-0" />
                {t("agent.rename")}
              </DropdownMenuItem>
              <DropdownMenuItem
                disabled={!ready}
                className="text-destructive-text focus:text-destructive-text"
                onSelect={() => setDeleteTarget({ id: thread, device, title: currentThreadTitle, running: primary.state.running })}
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
                className={cn(COARSE_POINTER_HEADER_ICON_BUTTON_CLASS, "shrink-0")}
                onClick={togglePanel}
              >
                <Icon name="PanelRight" />
              </Button>
            </TooltipTrigger>
            <TooltipContent>{t("agent.togglePanel")}</TooltipContent>
          </Tooltip>
        </div>
      </div>
      {status === "disconnected" && authError && (
        <AgentConnectionError message={authError} t={t} />
      )}

      {routeBlocked || routeError ? (
        <div role={routeState === "missing" || routeError ? "alert" : "status"} className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 p-6 text-sm text-muted-foreground">
          <p>{routeError || (routeState === "missing" ? t("agent.threadUnavailable", { thread: route.thread, device: route.device }) : t("common.loading"))}</p>
          {(routeState === "missing" || routeError) && <Button variant="outline" onClick={() => navigateThread(route.device)}>{t("agent.openDevice")}</Button>}
        </div>
      ) : <SplitContainer
        root={layout.root}
        renderPane={renderPane}
        onResize={onSplitResize}
        compact={isNarrow && countPanes(layout.root) > 1 ? {
          activePaneId: layout.focusedPaneId,
          onSelect: id => setLayoutState(value => setFocus(value, id)),
          labelForPane: pane => (pane.threadId ?? thread) === thread
            ? currentThreadTitle
            : threads.find(item => item.id === pane.threadId)?.title || t("agent.newThread"),
          label: t("agent.splitConversations"),
        } : undefined}
      />}

      {/* bb-style in-app rename dialog (no native prompt). */}
      {renameTarget && (
        <ThreadRenameDialog
          target={renameTarget}
          open={renameOpen}
          disabled={!ready}
          onCommit={renameThread}
          onClose={() => setRenameOpen(false)}
          t={t}
        />
      )}

      {/* Delete confirmation (no native confirm); the target is the current
       * thread from the header menu or any sidebar row. */}
      <AlertDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => {
          if (!open && !deleteBusy) { setDeleteTarget(null); setManagementError(""); }
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("agent.deleteThread")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("agent.deleteThreadConfirm", { title: deleteTarget?.title || "" })}
              <span className="mt-2 block">{t("agent.deleteStopsTasks")}</span>
            </AlertDialogDescription>
          </AlertDialogHeader>
          {managementError && <p role="alert" className="text-sm text-destructive-text">{managementError}</p>}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteBusy}>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              disabled={deleteBusy || !ready}
              onClick={(event) => { event.preventDefault(); confirmDeleteThread(); }}
            >
              {t("common.delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* bb ProjectPathDialog (create mode): browse the device filesystem,
       * submit sets the thread cwd. */}
      <CreateProjectDialog
        key={JSON.stringify([device, thread])}
        open={createProjectOpen}
        disabled={!ready}
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
          inert={!panelOpen || routeBlocked}
          className={cn(
            "fixed inset-y-0 right-0 z-30 w-full bg-background",
            transitionsReady
              ? panelOpen
                ? "[transition:translate_220ms_cubic-bezier(0.32,0.72,0,1),visibility_0s] motion-reduce:transition-none"
                : "[transition:translate_220ms_cubic-bezier(0.32,0.72,0,1),visibility_0s_220ms] motion-reduce:transition-none"
              : "transition-none",
            panelOpen && !routeBlocked
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
  onSubmit: (pw: string) => boolean | void;
  t: (k: string, v?: Record<string, string | number>) => string;
}) {
  const { devicePasswordInputsHidden } = useServerCapabilities();
  const { value, setValue, busy, submit } = useDevicePassword({
    device, open, error, onSubmit,
  });

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
          {!devicePasswordInputsHidden && <Input
            type="password"
            autoFocus
            value={value}
            placeholder={t("terminal.passwordPlaceholder")}
            onChange={(e) => setValue(e.target.value)}
          />}
          {error && <div className="mt-2 text-xs text-destructive-text">{error}</div>}
          <AlertDialogFooter className="mt-4">
            <AlertDialogAction type="submit" disabled={busy || (!devicePasswordInputsHidden && !value)}>
              {busy ? t("common.connecting") : t("common.connect")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </form>
      </AlertDialogContent>
    </AlertDialog>
  );
}
