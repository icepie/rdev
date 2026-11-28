import { FileWorkspaceDivider } from "../components/file-workspace-divider";
import { fileTreeMutation, createFileTreeRPC } from "../lib/file-tree-rpc";
import { useFilePreviewTabs } from "../lib/file-preview-tabs";
import { FileEditorTabs } from "../components/file-editor-tabs";
import { FileExplorer } from "../components/file-explorer";
import { FilesPaneLayout } from "../components/files-pane-layout";
import { isImeComposing } from "../lib/keyboard";
import { createKeyedRequest } from "../lib/keyed-request";
import { createTransferAttempts } from "../lib/file-transfer-attempts";
import { createFileTransferController, type FileTransferController } from "../lib/file-transfer-controller";
import { RecursiveTransferError, walkTransferFiles } from "../lib/file-transfer-route";
import { BLOB_LIMIT_MESSAGE, type UploadSource } from "../lib/file-integrity";
import { createRemoteFileSources } from "../lib/file-copy-source";
import { createFallbackDownloads } from "../lib/file-download-fallback";
import { createFileRTCTransfers } from "../lib/file-webrtc";
import { createDiskDownloads } from "../lib/file-download-disk";
import { availableFileTaskActions, createListingGuard, readListingPage, uploadResumeRequest } from "../lib/file-request-state";
import {
  DOWNLOAD_RETRY_MS,
  FILE_LIST_LIMIT,
  fmtBytes,
  loadPersistedTasks,
  makeId,
  persistTasks,
  statusBadgeClass,
  updateSpeed,
  type FileEntry,
  type FileTask,
  type ListView,
} from "../lib/file-task-model";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ChangeEvent,
  type ComponentProps,
  type DragEvent,
  type ReactNode,
} from "react";
import { useSearchParams } from "react-router-dom";
import { useI18n } from "../lib/i18n";
import { useDevices } from "../lib/devices";
import {
  wsURL,
  deviceAuthBypassed,
  getDevicePassword,
  rememberDevicePassword,
  forgetDevicePassword,
} from "../lib/api";
import { useServerCapabilities } from "../lib/server-capabilities";
import { cn } from "../lib/utils";
import { Button } from "../components/button";
import { Input } from "../components/input";
import { Badge } from "../components/badge";
import { Card } from "../components/card";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "../components/dropdown-menu";
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
import { SplitPageShell, MobileSidebarTrigger } from "../components/split-layout";
import { DeviceDropTarget } from "../components/device-rail";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../components/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "../components/tooltip";
import { Icon } from "../components/icon";
import { OsIcon } from "../components/os-icon";

/* ── Shared transfer-task model (see lib/file-task-model) ── */

const TASKS_KEY = "rdevFileTasks";

/* Drag payload + inter-pane API for moving files between split panes. */
export const FILE_DRAG_MIME = "application/x-rdev-pane-file";
export interface PaneDragItem {
  name: string;
  isDir: boolean;
  size: number;
  path: string;
}
export interface FilesPaneApi {
  deviceId(): string;
  currentPath(): string;
  directoryReady(): boolean;
  transferItemsTo(targetLeaf: number, items: PaneDragItem[]): Promise<void>;
  createDirectory(path: string, device: string): Promise<void>;
  receiveUpload(file: UploadSource & { name: string }, parentDir: string, device: string): void;
}

function FileIconButton({ label, children, className, ...props }: ComponentProps<typeof Button> & { label: string; children: ReactNode }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button type="button" size="icon" aria-label={label} className={cn("size-8 shrink-0 max-md:pointer-coarse:size-9", className)} {...props}>
          {children}
          <span className="sr-only">{label}</span>
        </Button>
      </TooltipTrigger>
      <TooltipContent side="bottom">{label}</TooltipContent>
    </Tooltip>
  );
}

/* ── Page ── */

const EMPTY_PARAMS = new URLSearchParams();

/* One file-browser instance = one split leaf. Only the primary (first) pane
 * syncs ?device= to the URL and loads/persists the transfer-task list, so
 * sibling panes never fight over shared storage. */
export function FilesBrowser({
  primary,
  deviceRequest,
  onDeviceChange,
  leafId,
  panesRef,
}: {
  primary: boolean;
  onDeviceChange?: (device: string) => void;
  /* Rail click/drop requests: {id, seq} identity change → open that device. */
  deviceRequest?: { id: string; seq: number };
  /* Pane identity + registry for cross-pane transfers. */
  leafId: number;
  panesRef: { current: Map<number, FilesPaneApi> };
}) {
  const { devicePasswordInputsHidden } = useServerCapabilities();
  const { t } = useI18n();
  const { devices } = useDevices();
  const [urlParams, setUrlParams] = useSearchParams();
  const searchParams = primary ? urlParams : EMPTY_PARAMS;

  const [device, setDeviceState] = useState("");
  useEffect(() => { onDeviceChange?.(device); }, [device, onDeviceChange]);
  const previews = useFilePreviewTabs(t("files.previewTimeout"));
  const { accept: acceptPreview, reset: closePreview, failPending: failPreviews } = previews;
  const [treeRevision, setTreeRevision] = useState(0);
  const [details, setDetails] = useState(false);
  const [showEditor, setShowEditor] = useState(false);
  const [pathInput, setPathInput] = useState("");
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [listView, setListView] = useState<ListView>({ truncated: false, shown: 0, total: 0 });
  const [tasksOpen, setTasksOpen] = useState(false);
  const [dragging, setDragging] = useState(false);
  const [alertMsg, setAlertMsg] = useState("");
  const [pwDialog, setPwDialog] = useState<{ deviceId: string; message: string } | null>(null);
  const [pwValue, setPwValue] = useState("");
  const [, setTasksTick] = useState(0);

  /* Refs: mutable transfer/listing state shared with WS handlers. */
  const wsRef = useRef<WebSocket | null>(null);
  const sendQueueRef = useRef<string[]>([]);
  const reconnectDelayRef = useRef(1000);
  const reconnectTimerRef = useRef(0);
  const tasksRef = useRef<Map<string, FileTask> | null>(null);
  if (!tasksRef.current)
    tasksRef.current = primary ? loadPersistedTasks(TASKS_KEY, t("files.reselect")) : new Map();
  const tasks = tasksRef.current;
  const transferAttempts = useRef(createTransferAttempts(makeId));
  const fileRTC = useRef(createFileRTCTransfers());
  const diskDownloads = useRef(createDiskDownloads());
  const fallbackDownloads = useRef(createFallbackDownloads());
  const directBinary = useRef<(frame: ArrayBuffer) => void>(() => {});
  const filesByTaskRef = useRef(new Map<string, UploadSource & { name: string }>());
  const copySources = useRef(createRemoteFileSources(message => {
    const socket = wsRef.current;
    if (!socket || socket.readyState !== WebSocket.OPEN) throw new Error("Copy source disconnected");
    socket.send(JSON.stringify(message));
  }, makeId));
  const currentPathRef = useRef("");
  const directoryReadyRef = useRef(false);
  const parentPathRef = useRef("");
  const listingGuard = useRef(createListingGuard());
  const listOffsetRef = useRef(0);
  const listTotalRef = useRef(0);
  const listTruncatedRef = useRef(false);
  const uploadRunsRef = useRef(new Map<string, AbortController>());
  const activeDeviceRef = useRef("");
  const listedForRef = useRef("");
  const devicesRef = useRef(devices);
  devicesRef.current = devices;
  const tRef = useRef(t);
  tRef.current = t;
  const pwReqRef = useRef<{ deviceId: string; resolve: (pw: string | null) => void } | null>(null);
  const connectionEpoch = useRef(0);
  const authRequests = useRef(createKeyedRequest<boolean>());
  const authWaitersRef = useRef(
    new Map<string, { resolve: (ok: boolean) => void; pw: string; timer: number }>(),
  );
  /* Auxiliary directory listings (recursive transfer walks), matched by
   * requestId so they never touch the visible listing. */
  const listWaitersRef = useRef(
    new Map<
      string,
      {
        path: string;
        device: string;
        entries: FileEntry[];
        resolve: (entries: FileEntry[]) => void;
        reject: (e: Error) => void;
      }
    >(),
  );
  const directoryWaiters = useRef(new Map<string, { resolve(): void; reject(error: Error): void }>());
  const fileInputRef = useRef<HTMLInputElement>(null);
  const reselectTaskRef = useRef<string | null>(null);
  const uploadDestinationRef = useRef<{ device: string; path: string } | null>(null);
  useEffect(() => {
    const input = fileInputRef.current;
    const canceled = () => {
      reselectTaskRef.current = null;
      uploadDestinationRef.current = null;
    };
    input?.addEventListener("cancel", canceled);
    return () => input?.removeEventListener("cancel", canceled);
  }, []);
  /* Latest list() for WS handlers declared before it. */
  const listRef = useRef<(path: string, append?: boolean) => Promise<void>>(
    () => Promise.resolve(),
  );

  const progressDirty = useRef(false);
  const bump = useCallback(() => setTasksTick((n) => n + 1), []);
  const saveTasks = useCallback(() => {
    fallbackDownloads.current.reconcile(tasksRef.current!);
    if (primary) persistTasks(TASKS_KEY, tasksRef.current!);
    progressDirty.current = false;
  }, [primary]);

  /* ── WS plumbing ── */

  const send = useCallback((msg: Record<string, unknown>) => {
    const payload = JSON.stringify(msg);
    const ws = wsRef.current;
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(payload);
    else sendQueueRef.current.push(payload);
  }, []);

  /* ── Device password auth ── */

  const requestDevicePassword = useCallback(
    (deviceId: string, message: string): Promise<string | null> => {
      const saved = getDevicePassword(deviceId);
      if (saved) return Promise.resolve(saved);
      const { promise, resolve } = Promise.withResolvers<string | null>();
      if (pwReqRef.current) pwReqRef.current.resolve(null);
      pwReqRef.current = { deviceId, resolve };
      setPwDialog({ deviceId, message });
      return promise;
    },
    [],
  );

  const ensureAuth = useCallback(
    (deviceId: string): Promise<boolean> => authRequests.current(deviceId, async () => {
      const epoch = connectionEpoch.current;
      const socket = wsRef.current;
      const connected = () => epoch === connectionEpoch.current && socket === wsRef.current && socket?.readyState === WebSocket.OPEN;
      if (!connected()) return false;
      const dev = devicesRef.current.find((d) => d.id === deviceId);
      if (!dev || !dev.hasPassword) return true;
      const bypassed = await deviceAuthBypassed();
      if (!connected()) return false;
      if (bypassed) return true;
      const pw = await requestDevicePassword(
        deviceId,
        tRef.current("files.authRequired", { id: deviceId }),
      );
      if (!pw || !connected()) return false;
      send({ op: "auth", deviceId, password: pw });
      const { promise, resolve } = Promise.withResolvers<boolean>();
      const timer = window.setTimeout(() => {
        authWaitersRef.current.delete(deviceId);
        resolve(false);
      }, 30000);
      authWaitersRef.current.set(deviceId, { resolve, pw, timer });
      return promise;
    }),
    [requestDevicePassword, send],
  );

  // New operations must never be replayed on a later connection.
  const requireConnectedDevice = useCallback(async (device: string): Promise<void> => {
    const socket = wsRef.current;
    const epoch = connectionEpoch.current;
    if (!device || socket?.readyState !== WebSocket.OPEN) {
      throw new Error(tRef.current("files.connectionLost"));
    }
    const authorized = await ensureAuth(device);
    if (epoch !== connectionEpoch.current || socket !== wsRef.current || socket.readyState !== WebSocket.OPEN) {
      throw new Error(tRef.current("files.connectionLost"));
    }
    if (!authorized) throw new Error(tRef.current("files.authCanceled"));
  }, [ensureAuth]);

  /* Shared transfer session; the callbacks carry this page's bookkeeping. */
  const transferRef = useRef<FileTransferController | null>(null);
  if (!transferRef.current) {
    transferRef.current = createFileTransferController(
      {
        attempts: transferAttempts,
        rtc: fileRTC,
        disk: diskDownloads,
        fallback: fallbackDownloads,
        uploadRuns: uploadRunsRef,
        directBinary,
        filesByTask: filesByTaskRef,
        progressDirty,
      },
      {
        send,
        sendUploadEnd: (_task, message) => { send(message); return true; },
        socket: () => wsRef.current,
        tasks: () => tasksRef.current!,
        saveTasks,
        bump,
        t: () => tRef.current,
        connectionGuard: () => {
          const epoch = connectionEpoch.current;
          return () => epoch === connectionEpoch.current;
        },
        authorize: task => requireConnectedDevice(task.device),
        onPhase: (task, wireId, phase) => { transferAttempts.current.phase(task.id, wireId, phase); },
        onProgress: (task, wireId) => { transferAttempts.current.progress(task.id, wireId); },
        claimBinary: frame => copySources.current.binary(frame),
        startFailed: (task, wireId, error) => {
          fileRTC.current.close(wireId);
          task.status = "failed";
          task.message = error === undefined
            ? tRef.current("files.connectionLost")
            : error instanceof Error ? error.message : String(error);
          saveTasks();
          bump();
        },
        diskChunkFailed: (task, _wireId, error) => {
          task.status = "failed";
          task.message = error instanceof Error ? error.message : String(error);
          diskDownloads.current.abort(task.id);
          saveTasks();
          bump();
        },
        uploadSettled: task => {
          if (task.device === activeDeviceRef.current && task.parentPath === currentPathRef.current) void listRef.current(currentPathRef.current);
        },
      },
    );
  }
  const transfer = transferRef.current;

  const openPreview = useCallback((path: string, refresh = false) => {
    const device = activeDeviceRef.current;
    setShowEditor(true);
    previews.open(device, path, async id => {
      await requireConnectedDevice(device);
      if (activeDeviceRef.current !== device) throw new Error(tRef.current("files.connectionLost"));
      send({ op: "read", deviceId: device, requestId: id, path });
    }, refresh);
  }, [requireConnectedDevice, send, previews.open]);

  /* ── Uploads ── */

  const startUpload = useCallback(
    (file: UploadSource & { name: string }, destDir?: string, activeDevice = activeDeviceRef.current) => {
      transfer.startUpload(file, destDir ?? currentPathRef.current, activeDevice);
    },
    [transfer],
  );

  /* ── WS message handlers ── */

  const treeRPC = useRef<ReturnType<typeof createFileTreeRPC> | null>(null);
  if (!treeRPC.current) treeRPC.current = createFileTreeRPC(message => {const ws=wsRef.current;if(!ws||ws.readyState!==WebSocket.OPEN)return false;ws.send(JSON.stringify(message));return true;});
  useEffect(() => () => treeRPC.current?.cancel(), [device]);

  const onText = useCallback(
    (msg: Record<string, unknown>) => {
      if (copySources.current.text(msg)) return;
      if (fileRTC.current.text(msg)) return;
      if (treeRPC.current?.accept(msg)) return;
      if (msg.op === "auth_ok" || msg.op === "auth_fail" || msg.op === "auth_required") {
        const id = typeof msg.deviceId === "string" ? msg.deviceId : "";
        const waiters = authWaitersRef.current;
        const keys = id ? [id] : [...waiters.keys()];
        const ok = msg.op === "auth_ok";
        for (const key of keys) {
          const w = waiters.get(key);
          if (!w) continue;
          clearTimeout(w.timer);
          waiters.delete(key);
          if (ok) rememberDevicePassword(key, w.pw);
          else forgetDevicePassword(key);
          w.resolve(ok);
        }
        if (!ok) {
          if (id && !waiters.has(id)) forgetDevicePassword(id);
          setAlertMsg(
            typeof msg.message === "string" && msg.message
              ? msg.message
              : tRef.current("common.passwordWrong"),
          );
        }
        if (msg.op !== "auth_required" || (!msg.taskId && !msg.requestId)) return;
        msg = { ...msg, op: "error" };
      }
      if (acceptPreview(msg)) return;
      if (msg.op === "file_content") return; // obsolete preview, never a directory response
      const directoryRequest = typeof msg.requestId === "string" ? directoryWaiters.current.get(msg.requestId) : undefined;
      if (directoryRequest && (msg.op === "mkdir_result" || msg.op === "error")) {
        directoryWaiters.current.delete(String(msg.requestId));
        if (msg.op === "mkdir_result" && msg.success === true) directoryRequest.resolve();
        else directoryRequest.reject(new Error(String(msg.error || msg.message || tRef.current("common.error"))));
        return;
      }
      if (msg.op === "error" && msg.requestId) {
        msg = { ...msg, op: "list_result", error: msg.message || msg.error || tRef.current("common.error") };
      }
      if (msg.op === "list_result") {
        const rid = typeof msg.requestId === "string" ? msg.requestId : "";
        const waiter = rid ? listWaitersRef.current.get(rid) : undefined;
        if (waiter) {
          /* Transfer walk page: accumulate, request the next page when
           * truncated, resolve when done. */
          if (msg.error) {
            listWaitersRef.current.delete(rid);
            waiter.reject(new Error(String(msg.error)));
            return;
          }
          const page = (Array.isArray(msg.entries) ? msg.entries : []) as FileEntry[];
          const pagination = readListingPage(msg, waiter.entries.length, page.length);
          if (!pagination) {
            listWaitersRef.current.delete(rid);
            waiter.reject(new Error(tRef.current("files.paginationFailed")));
            return;
          }
          waiter.entries = waiter.entries.concat(page);
          if (pagination.more) {
            send({
              op: "list",
              deviceId: waiter.device,
              requestId: rid,
              path: waiter.path,
              offset: waiter.entries.length,
              limit: FILE_LIST_LIMIT,
            });
          } else {
            listWaitersRef.current.delete(rid);
            waiter.resolve(waiter.entries);
          }
          return;
        }
        if (!listingGuard.current.consume(rid)) return;
        if (msg.error) {
          if (listingGuard.current.offset() > 0) {
            // Keep the loaded pages and their next offset available for retry.
            setAlertMsg(String(msg.error));
            return;
          }
          listOffsetRef.current = 0;
          listTotalRef.current = 0;
          listTruncatedRef.current = false;
          setEntries([]);
          setListView({ notice: String(msg.error), truncated: false, shown: 0, total: 0 });
          return;
        }
        const resultPath = typeof msg.path === "string" && msg.path ? msg.path : currentPathRef.current;
        const offset = Number(msg.offset || 0);
        const newEntries = (Array.isArray(msg.entries) ? msg.entries : []) as FileEntry[];
        const pagination = readListingPage(msg, listingGuard.current.offset(), newEntries.length);
        if (!pagination) {
          listTruncatedRef.current = false;
          setListView((previous) => ({ ...previous, truncated: false, notice: tRef.current("files.paginationFailed") }));
          return;
        }
        const append = offset > 0 && resultPath === currentPathRef.current;
        currentPathRef.current = resultPath;
        directoryReadyRef.current = !!resultPath;
        parentPathRef.current =
          typeof msg.parent === "string" && msg.parent ? msg.parent : resultPath;
        setPathInput(resultPath);
        setEntries((prev) => (append ? prev.concat(newEntries) : newEntries));
        if (!append) setTreeRevision(value => value + 1);
        listOffsetRef.current = pagination.nextOffset;
        listTotalRef.current = pagination.total;
        listTruncatedRef.current = pagination.more;
        setListView({
          truncated: listTruncatedRef.current,
          shown: append ? listOffsetRef.current : newEntries.length,
          total: listTotalRef.current,
        });
        return;
      }
      if (transfer.onTaskMessage(msg)) { bump(); return; }
      const taskId = typeof msg.taskId === "string" ? msg.taskId : "";
      const task = tasksRef.current!.get(transferAttempts.current.resolve(taskId) || "");
      if (msg.op === "canceled" && task) {
        if (task.status === "completed" || task.status === "failed" || task.status === "saving") return;
        transfer.stopUpload(task);
        if (task.status !== "paused") {
          task.status = "canceled";
          task.blobParts = undefined;
          filesByTaskRef.current.delete(task.id);
        }
        saveTasks();
      }
      bump();
    },
    [saveTasks, bump, transfer, acceptPreview],
  );

  directBinary.current = frame => transfer.onBinary(frame, true);

  /* Single shared /files socket with exponential-backoff reconnect. */
  useEffect(() => {
    let disposed = false;
    const nextDelay = () => {
      const base = reconnectDelayRef.current;
      reconnectDelayRef.current = Math.min(30000, reconnectDelayRef.current * 2);
      const spread = Math.max(1, Math.floor(base / 5));
      return base - spread + Math.floor(Math.random() * (spread * 2 + 1));
    };
    const connect = () => {
      if (disposed) return;
      window.clearTimeout(reconnectTimerRef.current);
      const ws = new WebSocket(wsURL("/files"));
      ws.binaryType = "arraybuffer";
      wsRef.current = ws;
      ws.onopen = () => {
        if (disposed || wsRef.current !== ws) return;
        reconnectDelayRef.current = 1000;
        const queue = sendQueueRef.current;
        while (queue.length && ws.readyState === WebSocket.OPEN) ws.send(queue.shift()!);
        tasksRef.current!.forEach((task) => {
          if (task.dir === "download" && task.status === "running" && !task.saved) {
            transfer.resumeDownload(task, task.done || 0, tRef.current("files.resuming"));
          }
        });
        if (activeDeviceRef.current) void listRef.current(currentPathRef.current);
      };
      ws.onmessage = (evt) => {
        if (disposed || wsRef.current !== ws) return;
        if (evt.data instanceof ArrayBuffer) return transfer.onBinary(evt.data);
        if (evt.data instanceof Blob) {
          void evt.data.arrayBuffer().then((data) => {
            if (!disposed && wsRef.current === ws) transfer.onBinary(data);
          });
          return;
        }
        try {
          onText(JSON.parse(String(evt.data)));
        } catch {
          /* ignore malformed frames */
        }
      };
      ws.onclose = () => {
        if (disposed || wsRef.current !== ws) return;
        wsRef.current = null;
        copySources.current.clear();
        fileRTC.current.clear();
        sendQueueRef.current = [];
        endPendingRequests();
        listingGuard.current.invalidate();
        directoryReadyRef.current = false;
        setListView((previous) => ({ ...previous, notice: tRef.current("files.connectionLost") }));
        tasksRef.current!.forEach((task) => {
          if (task.dir === "upload" && (task.status === "running" || task.status === "queued")) {
            transfer.stopUpload(task);
            task.status = "failed";
            task.message = tRef.current("files.disconnected");
          }
        });
        saveTasks();
        bump();
        window.clearTimeout(reconnectTimerRef.current);
        reconnectTimerRef.current = window.setTimeout(connect, nextDelay());
      };
    };
    const endPendingRequests = () => {
      if (!disposed) failPreviews(tRef.current("files.connectionLost"));
      connectionEpoch.current++;
      pwReqRef.current?.resolve(null);
      pwReqRef.current = null;
      if (!disposed) setPwDialog(null);
      treeRPC.current?.cancel();
      for (const waiter of listWaitersRef.current.values()) waiter.reject(new Error(tRef.current("files.connectionLost")));
      listWaitersRef.current.clear();
      for (const waiter of directoryWaiters.current.values()) waiter.reject(new Error(tRef.current("files.connectionLost")));
      directoryWaiters.current.clear();
      for (const waiter of authWaitersRef.current.values()) {
        window.clearTimeout(waiter.timer);
        waiter.resolve(false);
      }
      authWaitersRef.current.clear();
    };
    connect();
    return () => {
      disposed = true;
      fileRTC.current.clear();
      copySources.current.clear();
      endPendingRequests();
      uploadRunsRef.current.forEach(controller => controller.abort());
      uploadRunsRef.current.clear();
      window.clearTimeout(reconnectTimerRef.current);
      const ws = wsRef.current;
      if (ws) {
        ws.onclose = null;
        ws.close();
        wsRef.current = null;
      }
      diskDownloads.current.clear();
      fallbackDownloads.current.clear();
    };
  }, [onText, transfer, saveTasks, bump, ensureAuth, failPreviews]);

  /* Keep acknowledgments on the hot path; sample progress and persist at most
   * twice a second. Idle panes do not re-render. Terminal states save directly. */
  useEffect(() => {
    const timer = window.setInterval(() => {
      const now = Date.now();
      let running = false;
      tasksRef.current!.forEach((task) => {
        if (task.status === "running") {
          running = true;
          updateSpeed(task);
        }
        if (
          wsRef.current?.readyState === WebSocket.OPEN &&
          !fileRTC.current.direct(transferAttempts.current.current(task.id)) &&
          task.dir === "download" &&
          task.status === "running" &&
          !task.saved &&
          !diskDownloads.current.get(task.id)?.busy &&
          transferAttempts.current.expired(task.id, now) &&
          now - (task.lastRequested || 0) > DOWNLOAD_RETRY_MS
        ) {
          transfer.resumeDownload(task, task.done || 0, tRef.current("files.resuming"));
        }
      });
      if (progressDirty.current) saveTasks();
      if (running) bump();
    }, 500);
    const flush = () => { if (progressDirty.current) saveTasks(); };
    window.addEventListener("pagehide", flush);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener("pagehide", flush);
      flush();
    };
  }, [transfer, bump, saveTasks]);

  /* ── Directory listing ── */

  const list = useCallback(
    async (path: string, append = false) => {
      const activeDevice = activeDeviceRef.current;
      const requestId = makeId();
      const offset = append ? listOffsetRef.current : 0;
      if (!append) directoryReadyRef.current = false;
      listingGuard.current.begin(requestId, offset);
      if (!activeDevice || !(await ensureAuth(activeDevice)) ||
          !listingGuard.current.accepts(requestId) || activeDeviceRef.current !== activeDevice) return;
      const targetPath = append ? currentPathRef.current : path;
      if (!append && targetPath !== currentPathRef.current) {
        setEntries([]);
        listOffsetRef.current = 0;
        listTotalRef.current = 0;
        listTruncatedRef.current = false;
        setListView({
          notice: tRef.current("common.connecting"),
          truncated: false,
          shown: 0,
          total: 0,
        });
      }
      send({
        op: "list",
        deviceId: activeDevice,
        requestId,
        path: targetPath,
        offset,
        limit: FILE_LIST_LIMIT,
      });
    },
    [ensureAuth, send],
  );
  listRef.current = list;

  const setDevice = useCallback(
    (id: string) => {
      if (activeDeviceRef.current !== id) {
        closePreview();
        listingGuard.current.invalidate();
        directoryReadyRef.current = false;
        listedForRef.current = "";
        currentPathRef.current = "";
        parentPathRef.current = "";
        listOffsetRef.current = 0;
        listTotalRef.current = 0;
        listTruncatedRef.current = false;
        setEntries([]);
        setPathInput("");
        setListView({ truncated: false, shown: 0, total: 0 });
      }
      setDeviceState(id);
      activeDeviceRef.current = id;
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
      if (id && listedForRef.current !== id) {
        listedForRef.current = id;
        currentPathRef.current = "";
        parentPathRef.current = "";
        listOffsetRef.current = 0;
        listTotalRef.current = 0;
        listTruncatedRef.current = false;
        void list("");
      }
    },
    [list, primary, setUrlParams, closePreview],
  );

  /* Full (paginated) listing of one directory without touching the visible
   * listing; used by recursive transfer walks. */
  const listDirAll = useCallback(
    async (path: string, activeDevice: string): Promise<FileEntry[]> => {
      await requireConnectedDevice(activeDevice);
      const requestId = makeId();
      const { promise, resolve, reject } = Promise.withResolvers<FileEntry[]>();
      const timer = window.setTimeout(() => {
        const w = listWaitersRef.current.get(requestId);
        listWaitersRef.current.delete(requestId);
        w?.reject(new Error("timeout"));
      }, 30000);
      listWaitersRef.current.set(requestId, {
        path,
        device: activeDevice,
        entries: [],
        resolve: (v) => {
          window.clearTimeout(timer);
          resolve(v);
        },
        reject: (e) => {
          window.clearTimeout(timer);
          reject(e);
        },
      });
      send({
        op: "list",
        deviceId: activeDevice,
        requestId,
        path,
        offset: 0,
        limit: FILE_LIST_LIMIT,
      });
      return promise;
    },
    [requireConnectedDevice, send],
  );

  const loadTreeDirectory = useCallback((path: string) => listDirAll(path, device), [listDirAll, device]);

  const createDirectory = useCallback(async (path: string, device: string): Promise<void> => {
    await requireConnectedDevice(device);
    const requestId = makeId();
    const { promise, resolve, reject } = Promise.withResolvers<void>();
    const timer = window.setTimeout(() => {
      directoryWaiters.current.delete(requestId);
      reject(new Error(tRef.current("files.directoryTimeout")));
    }, 30000);
    directoryWaiters.current.set(requestId, {
      resolve: () => { window.clearTimeout(timer); resolve(); },
      reject: (error) => { window.clearTimeout(timer); reject(error); },
    });
    send({ op: "mkdir", requestId, deviceId: device, path });
    await promise;
    if (activeDeviceRef.current === device) void listRef.current(currentPathRef.current);
  }, [requireConnectedDevice, send]);

  /* Copy ranges are pulled only as the destination ACK window frees capacity. */
  const transferItemsTo = useCallback(
    async (targetLeaf: number, items: PaneDragItem[]) => {
      const target = panesRef.current.get(targetLeaf);
      if (!target || !target.deviceId()) {
        setAlertMsg(tRef.current("files.transferNoTarget"));
        return;
      }
      if (!target.directoryReady()) {
        setAlertMsg(tRef.current("files.transferTargetNotReady"));
        return;
      }
      const sourceDevice = activeDeviceRef.current;
      const destination = { targetLeaf, device: target.deviceId(), parentDir: target.currentPath() };
      try {
        if (!sourceDevice) return;
        await walkTransferFiles(items, sourceDevice, destination, {
          list: listDirAll,
          mkdir: (path, device) => target.createDirectory(path, device),
          download: async (item, device, transfer) => {
            await requireConnectedDevice(device);
            const source = await copySources.current.open(device, item.path, item.name, new AbortController().signal);
            if (panesRef.current.get(targetLeaf) !== target) throw new Error(tRef.current("files.transferNoTarget"));
            target.receiveUpload(source, transfer.parentDir, transfer.device);
          },
        });
      } catch (error) {
        setAlertMsg(error instanceof RecursiveTransferError
          ? tRef.current("files.transferIntoSelf")
          : error instanceof Error ? error.message : String(error));
      }
    },
    [panesRef, listDirAll, requireConnectedDevice],
  );

  /* Publish this pane's API for sibling panes. */
  useEffect(() => {
    panesRef.current.set(leafId, {
      deviceId: () => activeDeviceRef.current,
      currentPath: () => currentPathRef.current,
      directoryReady: () => directoryReadyRef.current,
      transferItemsTo,
      receiveUpload: startUpload,
      createDirectory,
    });
    return () => {
      panesRef.current.delete(leafId);
    };
  }, [leafId, panesRef, transferItemsTo, startUpload, createDirectory]);

  /* Rail pick/drop → open the requested device in this pane. */
  useEffect(() => {
    if (deviceRequest) setDevice(deviceRequest.id);
  }, [deviceRequest, setDevice]);

  /* Keep the picker valid as devices come and go: current wins, then the
   * ?device= preselect, then nothing. Mirrors the vanilla loadDevices. */
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

  /* ── Task actions ── */

  const taskAction = useCallback(
    (task: FileTask, act: "pause" | "resume" | "cancel" | "clear") => {
      if (tasksRef.current!.get(task.id) !== task || !availableFileTaskActions(task.status)[act]) return;
      if (act !== "resume") fileRTC.current.close(transferAttempts.current.current(task.id), false);
      if (act === "cancel" || act === "clear") diskDownloads.current.abort(task.id);
      if (act === "pause") {
        diskDownloads.current.get(task.id)?.suspend();
        transfer.stopUpload(task);
        task.status = "paused";
        send({ op: "cancel", taskId: transferAttempts.current.current(task.id), offset: task.done || 0 });
      }
      if (act === "resume") {
        task.status = "running";
        task.message = "";
        if (task.dir === "upload") {
          const file = filesByTaskRef.current.get(task.id);
          if (!file) {
            reselectTaskRef.current = task.id;
            fileInputRef.current?.click();
            task.status = "failed";
            task.message = tRef.current("files.reselect");
          } else {
            transfer.startTransfer(task, {
              op: "upload_start",
              deviceId: task.device,
              parentPath: task.parentPath,
              name: task.name,
              size: task.size,
            });
          }
        } else {
          task.blobParts = task.blobParts || [];
          transfer.resumeDownload(task, task.done || 0, tRef.current("files.resuming"));
        }
      }
      if (act === "cancel") {
        transfer.stopUpload(task);
        task.status = "canceled";
        task.blobParts = undefined;
        filesByTaskRef.current.delete(task.id);
        send({ op: "cancel", taskId: transferAttempts.current.current(task.id), offset: task.done || 0 });
      }
      if (act === "clear") {
        transfer.stopUpload(task);
        transferAttempts.current.remove(task.id);
        tasksRef.current!.delete(task.id);
        filesByTaskRef.current.delete(task.id);
      }
      saveTasks();
      bump();
    },
    [transfer, saveTasks, send, bump],
  );

  /* ── UI handlers ── */

  const onFilesPicked = (e: ChangeEvent<HTMLInputElement>) => {
    const files = [...(e.target.files || [])];
    const taskId = reselectTaskRef.current;
    const destination = uploadDestinationRef.current;
    reselectTaskRef.current = null;
    uploadDestinationRef.current = null;
    e.target.value = "";
    if (taskId) {
      const task = tasksRef.current!.get(taskId);
      if (!task || task.status !== "failed") return;
      const request = files.length === 1 ? uploadResumeRequest(task, files[0]) : null;
      if (!request) {
        setAlertMsg(t("files.reselectMismatch"));
        return;
      }
      filesByTaskRef.current.set(task.id, files[0]);
      task.status = "queued";
      task.message = "";
      saveTasks();
      bump();
      transfer.startTransfer(task, request);
      return;
    }
    if (destination) files.forEach((file) => startUpload(file, destination.path, destination.device));
  };

  const onDrop = (e: DragEvent) => {
    e.preventDefault();
    setDragging(false);
    /* Cross-pane transfer drop: ask the source pane to push the items. */
    const raw = e.dataTransfer.getData(FILE_DRAG_MIME);
    if (raw) {
      try {
        const payload = JSON.parse(raw) as { src?: number; items?: PaneDragItem[] };
        if (payload.src != null && payload.src !== leafId && payload.items?.length) {
          void panesRef.current.get(payload.src)?.transferItemsTo(leafId, payload.items);
        }
      } catch {
        /* malformed payload: ignore */
      }
      return;
    }
    [...e.dataTransfer.files].forEach((f) => startUpload(f));
  };

  const onPaneDragOver = (e: DragEvent) => {
    if (e.dataTransfer.types.includes(FILE_DRAG_MIME)) e.preventDefault();
  };

  const submitPassword = () => {
    if (!pwDialog || !pwValue) return;
    const req = pwReqRef.current;
    pwReqRef.current = null;
    setPwDialog(null);
    req?.resolve(pwValue);
  };

  const cancelPassword = () => {
    const req = pwReqRef.current;
    pwReqRef.current = null;
    setPwDialog(null);
    req?.resolve(null);
  };

  useEffect(() => {
    if (pwDialog) setPwValue(getDevicePassword(pwDialog.deviceId));
  }, [pwDialog]);

  /* ── Render ── */

  const sortedTasks = [...tasks.values()].sort((a, b) => b.created - a.created);
  const showInfoRow =
    !listView.notice && (listView.truncated || listView.total > entries.length);

  return (
    <div className="page-workspace @container/files overflow-hidden">
      <TooltipProvider delayDuration={300}>
      {!("showSaveFilePicker" in globalThis) && <p className="px-3 py-1 text-xs text-muted-foreground">{BLOB_LIMIT_MESSAGE}</p>}
      <div className="page-toolbar gap-1.5">
      {/* Device is picked from the left rail; keep the toolbar focused on navigation. */}
      <div className="flex h-8 min-w-0 max-w-48 items-center gap-1.5 rounded-md border border-border-seam bg-surface-recessed px-2 text-xs text-muted-foreground select-none">
        <OsIcon os={devices.find((d) => d.id === device)?.os} className="size-3.5 shrink-0" />
        <span className="truncate">{device || t("terminal.devicePlaceholder")}</span>
      </div>
      <div className="relative min-w-0 flex-1">
        <Icon name="Folder" className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input
          spellCheck={false}
          autoComplete="off"
          value={pathInput}
          aria-label={t("files.path")}
          disabled={!device}
          placeholder={t("files.path")}
          className="h-8 min-w-0 border-border-seam bg-surface-recessed pl-7 text-xs"
          onChange={(e) => setPathInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !isImeComposing(e.nativeEvent)) void list(pathInput.trim());
          }}
        />
      </div>
      <FileIconButton label={t("files.up")} variant="ghost" disabled={!device} onClick={() => void list(parentPathRef.current || currentPathRef.current)}>
        <Icon name="ArrowUp" className="size-4" />
      </FileIconButton>
      <FileIconButton label={t("files.refresh")} variant="ghost" disabled={!device} onClick={() => void list(currentPathRef.current)}>
        <Icon name="Refresh" className="size-4" />
      </FileIconButton>
      <FileIconButton label={t("files.upload")} variant="secondary" disabled={!device} onClick={() => { reselectTaskRef.current = null; fileInputRef.current?.click(); }}>
        <Icon name="Upload" className="size-4" />
      </FileIconButton>
      <FileIconButton label={t("files.tasks")} variant={tasksOpen ? "secondary" : "ghost"} aria-expanded={tasksOpen} onClick={() => setTasksOpen((open) => !open)}>
        <span className="relative">
          <Icon name="Files" className="size-4" />
          {sortedTasks.length > 0 && <span className="absolute -right-2 -top-2 min-w-3.5 rounded-full bg-foreground px-1 text-center text-[9px] font-medium leading-3.5 tabular-nums text-background">{sortedTasks.length > 99 ? "99+" : sortedTasks.length}</span>}
        </span>
      </FileIconButton>
    </div>

    <input ref={fileInputRef} type="file" multiple className="hidden" onChange={onFilesPicked}
      onClick={() => {
        uploadDestinationRef.current = reselectTaskRef.current ? null : {
          device: activeDeviceRef.current, path: currentPathRef.current,
        };
      }} />


      <div className="file-workspace-grid min-h-0 flex-1 overflow-hidden" data-tasks-open={tasksOpen}>
        <div className="file-editor-workspace" data-details={details} data-editor-open={showEditor && previews.tabs.length > 0}>
          <div
            className={cn("file-explorer-region relative min-h-0 min-w-0 overflow-hidden border-r border-border-seam", dragging && "ring-1 ring-inset ring-timeline-accent")}
            onDragOver={(e) => {
              onPaneDragOver(e);
              if (e.dataTransfer.types.includes("Files") || e.dataTransfer.types.includes(FILE_DRAG_MIME)) {
                e.preventDefault();
                setDragging(true);
              }
            }}
            onDragLeave={(e) => {
              if (!e.relatedTarget || !e.currentTarget.contains(e.relatedTarget as Node)) setDragging(false);
            }}
            onDrop={(e) => {
              setDragging(false);
              onDrop(e);
            }}
          >
            {dragging && (
              <div className="pointer-events-none absolute inset-2 z-20 flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed border-timeline-accent bg-background/90 text-sm text-timeline-accent shadow-lg">
                <Icon name="Upload" className="size-6" />
                <span>{t("files.dropHint")}</span>
              </div>
            )}
            <div className="flex min-h-9 shrink-0 items-center gap-2 border-b border-border-seam bg-surface-raised px-2">
              <div className="flex min-w-0 flex-1 items-center gap-1.5 text-xs text-muted-foreground">
                <Icon name="Folder" className="size-3.5 shrink-0 text-file-accent" />
                <span className="truncate font-mono">{currentPathRef.current || "/"}</span>
              </div>
              <div className="flex shrink-0 items-center gap-0.5">
                <FileIconButton label={t("files.explorer")} variant={!details ? "secondary" : "ghost"} aria-pressed={!details} onClick={() => setDetails(false)}>
                  <Icon name="Folder" className="size-4" />
                </FileIconButton>
                <FileIconButton label={t("files.listView")} variant={details ? "secondary" : "ghost"} aria-pressed={details} onClick={() => setDetails(true)}>
                  <Icon name="Files" className="size-4" />
                </FileIconButton>
                {previews.tabs.length > 0 && <FileIconButton label={t("files.openFiles")} variant={showEditor ? "secondary" : "ghost"} onClick={() => setShowEditor(true)}>
                  <Icon name="File" className="size-4" />
                </FileIconButton>}
              </div>
            </div>
            {details ? (
        <Card
          className="min-h-0 min-w-0 flex-1 overflow-auto rounded-none border-0 [&>div]:h-full"
        >
          <Table className="table-fixed">
            <TableHeader className="sticky top-0 z-10 bg-background">
              <TableRow>
                <TableHead>{t("files.name")}</TableHead>
                <TableHead className="w-20 whitespace-nowrap">{t("files.size")}</TableHead>
                <TableHead className="hidden w-48 @min-[800px]/files:table-cell">{t("files.modified")}</TableHead>
                <TableHead className="w-20"><span className="sr-only">{t("files.actions")}</span></TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {listView.notice ? (
                <TableRow>
                  <TableCell colSpan={4} className="py-12 text-center text-muted-foreground" role="status">
                    {listView.notice}
                  </TableCell>
                </TableRow>
              ) : (
                <>
                  {showInfoRow && (
                    <TableRow className="bg-timeline-accent/5 hover:bg-timeline-accent/5">
                      <TableCell colSpan={4} className="text-muted-foreground">
                        {t("files.showing", {
                          shown: listView.shown || entries.length,
                          total: listView.total || entries.length,
                        })}
                        {listView.truncated && (
                          <Button
                            variant="outline"
                            size="sm"
                            className="ml-2.5"
                            onClick={() => void list(currentPathRef.current, true)}
                          >
                            {t("files.loadMore")}
                          </Button>
                        )}
                      </TableCell>
                    </TableRow>
                  )}
                  {entries.length === 0 ? (
                    <TableRow>
                      <TableCell colSpan={4} className="py-12 text-center text-muted-foreground">
                        {device ? t("files.emptyDir") : t("files.empty")}
                      </TableCell>
                    </TableRow>
                  ) : (
                    entries.map((entry) => (
                      <TableRow
                        key={entry.path}
                        draggable
                        onDragStart={(e) => {
                          e.dataTransfer.setData(
                            FILE_DRAG_MIME,
                            JSON.stringify({
                              src: leafId,
                              items: [
                                {
                                  name: entry.name,
                                  isDir: entry.isDir,
                                  size: entry.size,
                                  path: entry.path,
                                },
                              ],
                            }),
                          );
                          e.dataTransfer.effectAllowed = "copy";
                        }}
                      >
                        <TableCell>
                          <button
                            type="button"
                            className={cn(
                              "inline-flex max-w-full min-w-0 cursor-pointer items-center gap-1.5 rounded-sm text-sm text-foreground hover:text-timeline-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                            )}
                            title={entry.name}
                            onClick={() => {
                              if (entry.isDir) void list(entry.path);
                              else openPreview(entry.path);
                            }}
                          >
                            <Icon
                              name={entry.isDir ? "Folder" : "File"}
                              className="size-4 shrink-0"
                            />
                            <span className="truncate">{entry.name}</span>
                          </button>
                        </TableCell>
                        <TableCell className="whitespace-nowrap tabular-nums">{entry.isDir ? "--" : fmtBytes(entry.size)}</TableCell>
                        <TableCell className="hidden text-muted-foreground @min-[800px]/files:table-cell">
                          {entry.modTime ? new Date(entry.modTime).toLocaleString() : ""}
                        </TableCell>
                        <TableCell>
                          <div className="flex items-center justify-end gap-0.5">
                            {!entry.isDir && (
                              <FileIconButton label={t("files.download")} variant="ghost" onClick={() => transfer.startDownload(entry.path, entry.name, entry.size, activeDeviceRef.current)}>
                                <Icon name="Download" className="size-4" />
                              </FileIconButton>
                            )}
                            {/* Cross-pane transfer menu (split view). */}
                            <DropdownMenu>
                              <DropdownMenuTrigger asChild>
                                <Button type="button" variant="ghost" size="icon" className="size-8 max-md:pointer-coarse:size-9" aria-label={t("files.transfer")}>
                                  <Icon name="MoreHorizontal" className="size-4" />
                                </Button>
                              </DropdownMenuTrigger>
                              <DropdownMenuContent align="end">
                                {[...panesRef.current.entries()].filter(
                                  ([id]) => id !== leafId,
                                ).length === 0 && (
                                  <DropdownMenuItem disabled>
                                    {t("files.noOtherPane")}
                                  </DropdownMenuItem>
                                )}
                                {[...panesRef.current.entries()]
                                  .filter(([id]) => id !== leafId)
                                  .map(([id, api]) => (
                                    <DropdownMenuItem
                                      key={id}
                                      disabled={!api.deviceId()}
                                      onSelect={() =>
                                        void transferItemsTo(id, [
                                          {
                                            name: entry.name,
                                            isDir: entry.isDir,
                                            size: entry.size,
                                            path: entry.path,
                                          },
                                        ])
                                      }
                                    >
                                      <Icon name="ArrowRight" className="size-4 shrink-0" />
                                      <span className="min-w-0 truncate">
                                        {t("files.transferTo", {
                                          device: api.deviceId() || "…",
                                        })}
                                      </span>
                                    </DropdownMenuItem>
                                  ))}
                              </DropdownMenuContent>
                            </DropdownMenu>
                          </div>
                        </TableCell>
                      </TableRow>
                    ))
                  )}
                </>
              )}
            </TableBody>
          </Table>
        </Card>

            ) : <FileExplorer operations={devices.find(d=>d.id===device)?.fileOperations??[]} key={JSON.stringify([device, currentPathRef.current])} refreshKey={treeRevision} path={currentPathRef.current} entries={entries} stateKey={JSON.stringify([device,leafId])}
              onDownload={entry=>transfer.startDownload(entry.path,entry.name,entry.size||0,activeDeviceRef.current)}
              onOperation={async(op,entry,name)=>{
                const mutation = fileTreeMutation(op, entry, name);
                await treeRPC.current!.request(device, mutation.op, mutation.args);
                await list(currentPathRef.current);
              }}
              loading={listView.notice===t("common.connecting")} notice={listView.notice} selected={previews.tabs.find(tab => tab.key === previews.active)?.path}
              onDragStart={(entry, event) => {
                event.dataTransfer.setData(FILE_DRAG_MIME, JSON.stringify({ src: leafId, items: [{ ...entry, size: entry.size || 0 }] }));
                event.dataTransfer.effectAllowed = "copy";
              }}
              load={loadTreeDirectory} onOpen={openPreview} onNavigate={path => void list(path)} t={t} />}
            {listView.truncated && !details && <Button variant="ghost" size="sm" onClick={() => void list(currentPathRef.current, true)}>{t("files.loadMore")}</Button>}
          </div>
          <FileWorkspaceDivider storageKey={JSON.stringify(["file-split",device,leafId])} label={t("files.resizeExplorer")} />
          <div className="file-editor-region min-h-0 min-w-0 overflow-hidden">
            <Button className="file-explorer-back shrink-0" variant="ghost" size="sm" onClick={() => setShowEditor(false)}><Icon name="ChevronLeft" />{t("files.explorer")}</Button>
            <div className="min-h-0 flex-1">
              <FileEditorTabs tabs={previews.tabs} active={previews.active} onSelect={previews.select} onClose={previews.close}
                onRefresh={path => openPreview(path, true)} connected={!!device && wsRef.current?.readyState === WebSocket.OPEN}
                onDownload={tab => transfer.startDownload(tab.path, tab.path.split(/[\\/]/).pop() || "download", 0, tab.device)} t={t} />
            </div>
          </div>
        </div>

        {/* Tasks */}
        <div className="file-task-region min-h-0 min-w-0 overflow-hidden" inert={!tasksOpen} aria-hidden={!tasksOpen}>
        <Card className="h-full min-h-0 min-w-0 overflow-y-auto rounded-none border-0 border-t border-border-seam bg-sidebar p-2.5 @min-[800px]/files:border-t-0 @min-[800px]/files:border-l">
          <div className="mb-2 flex items-center gap-2">
            <h2 className="flex min-w-0 flex-1 items-center gap-1.5 text-sm font-semibold">
              <Icon name="Files" className="size-4 shrink-0" />
              <span className="truncate">{t("files.tasks")}</span>
              {sortedTasks.length > 0 && <Badge className="border-transparent bg-muted text-xs text-muted-foreground">{sortedTasks.length}</Badge>}
            </h2>
            <FileIconButton label={t("files.tasks")} variant="ghost" onClick={() => setTasksOpen(false)}>
              <Icon name="X" className="size-4" />
            </FileIconButton>
          </div>
          <div className="flex flex-col gap-2.5">
            {sortedTasks.length === 0 && (
              <div className="text-sm text-muted-foreground">{t("files.noTasks")}</div>
            )}
            {sortedTasks.map((task) => {
              const actions = availableFileTaskActions(task.status);
              const pct = task.size
                ? Math.min(100, Math.floor(((task.done || 0) * 1000) / task.size) / 10)
                : task.status === "completed"
                  ? 100
                  : 0;
              const eta =
                task.speed && task.speed > 0 && task.size
                  ? Math.max(0, (task.size - (task.done || 0)) / task.speed)
                  : 0;
              const stat =
                `${pct}% · ${fmtBytes(task.done || 0)} / ${fmtBytes(task.size || 0)} · ${fmtBytes(task.speed || 0)}/s` +
                (eta ? ` · ${Math.ceil(eta)}s` : "");
              return (
                <div key={task.id} className="flex flex-col gap-2.5 rounded-lg border border-border p-2.5">
                  <div className="min-w-0">
                    <div className="flex min-w-0 items-center gap-2">
                      <Badge className={statusBadgeClass(task.status)}>
                        {t("files.status." + task.status)}
                      </Badge>
                      <strong className="truncate text-sm">{task.name}</strong>
                      <span className="text-muted-foreground">
                        {task.dir === "upload" ? "↑" : "↓"}
                      </span>
                    </div>
                    <div className="mt-1 truncate text-xs text-muted-foreground">
                      {task.device} · {task.path || task.parentPath || ""}
                    </div>
                    <div
                      role="progressbar"
                      aria-label={task.name}
                      aria-valuemin={0}
                      aria-valuemax={100}
                      aria-valuenow={pct}
                      className="mt-2 h-1.5 overflow-hidden rounded-full border border-border bg-muted"
                    >
                      <div
                        className="h-full bg-timeline-accent transition-[width] duration-200"
                        style={{ width: pct + "%" }}
                      />
                    </div>
                    <div className="mt-1.5 break-words text-xs text-muted-foreground">
                      {stat}
                      {task.message ? " · " + task.message : ""}
                    </div>
                    {task.diagnostics && <div className="text-xs text-muted-foreground" data-testid="file-transfer-diagnostics">{t(`files.transport.${task.diagnostics.transport}`)}{task.diagnostics.reason ? ` · ${t(`files.rtcReason.${task.diagnostics.reason}`)}` : ""}{task.diagnostics.rttMs !== undefined ? ` · RTT ${Math.round(task.diagnostics.rttMs)}ms` : ""}{task.diagnostics.bufferedBytes !== undefined ? ` · ${t("files.buffered")} ${fmtBytes(task.diagnostics.bufferedBytes)}` : ""}{task.diagnostics.negotiationMs !== undefined ? ` · ${t("files.negotiationTime")} ${Math.round(task.diagnostics.negotiationMs)}ms` : ""}</div>}
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    {(["pause", "resume", "cancel"] as const).map(action => actions[action] && (
                      <Button
                        key={action}
                        variant="outline"
                        size="icon"
                        className="size-8 max-md:pointer-coarse:size-9"
                        aria-label={t(`files.${action}`)}
                        onClick={() => taskAction(task, action)}
                      >
                        <Icon name={action === "pause" ? "Pause" : action === "resume" ? "Play" : "X"} />
                      </Button>
                    ))}
                    {actions.clear && <FileIconButton
                      label={t("files.clear")}
                      variant="outline"
                      onClick={() => taskAction(task, "clear")}
                    >
                      <Icon name="Trash2" className="size-4" />
                    </FileIconButton>}
                  </div>
                </div>
              );
            })}
          </div>
        </Card>
        </div>
      </div>

      {/* Device password prompt */}
      <AlertDialog open={!!pwDialog}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("common.password")}</AlertDialogTitle>
            <AlertDialogDescription>{pwDialog?.message}</AlertDialogDescription>
          </AlertDialogHeader>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              submitPassword();
            }}
          >
            {!devicePasswordInputsHidden && <Input
              type="password"
              aria-label={t("common.password")}
              autoComplete="current-password"
              autoFocus
              value={pwValue}
              placeholder={t("terminal.passwordPlaceholder")}
              onChange={(e) => setPwValue(e.target.value)}
            />}
            <AlertDialogFooter className="mt-4">
              <AlertDialogCancel type="button" onClick={cancelPassword}>
                {t("common.cancel")}
              </AlertDialogCancel>
              <AlertDialogAction type="submit" disabled={!devicePasswordInputsHidden && !pwValue}>
                {t("common.connect")}
              </AlertDialogAction>
            </AlertDialogFooter>
          </form>
        </AlertDialogContent>
      </AlertDialog>

      {/* Async error alert (auth_fail / auth_required) */}
      <AlertDialog open={!!alertMsg} onOpenChange={(open) => !open && setAlertMsg("")}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("common.error")}</AlertDialogTitle>
            <AlertDialogDescription>{alertMsg}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogAction>{t("common.ok")}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      </TooltipProvider>
    </div>
  );
}

/* Keep each browser mounted while the second pane is folded, so ongoing
 * transfers and directory navigation survive layout changes. */
export function FilesPage() {
  const { t } = useI18n();
  const [paneDevices, setPaneDevices] = useState<Record<number, string>>({});
  const [focusedPane, setFocusedPane] = useState(0);
  const [secondMounted, setSecondMounted] = useState(false);
  const [secondOpen, setSecondOpen] = useState(false);
  const seqRef = useRef(0);
  const [requests, setRequests] = useState<Map<number, { id: string; seq: number }>>(new Map());
  const panesRef = useRef(new Map<number, FilesPaneApi>());

  const requestDevice = (leafId: number, deviceId: string) => {
    setFocusedPane(leafId);
    setRequests((prev) => new Map(prev).set(leafId, { id: deviceId, seq: ++seqRef.current }));
  };
  const reportFirstDevice = useCallback((id: string) => setPaneDevices(prev =>
    prev[0] === id ? prev : { ...prev, 0: id }), []);
  const reportSecondDevice = useCallback((id: string) => setPaneDevices(prev =>
    prev[1] === id ? prev : { ...prev, 1: id }), []);

  return (
    <SplitPageShell
      storageKey="rdev.split.files.rail"
      selectedDevice={paneDevices[focusedPane] || ""}
      onDevicePick={(id) => requestDevice(focusedPane, id)}
    >
      <FilesPaneLayout toolbarStart={<MobileSidebarTrigger />} focusedPane={focusedPane} onFocus={setFocusedPane} dragMime={FILE_DRAG_MIME}
        secondMounted={secondMounted} secondOpen={secondOpen}
        label={t("files.title")} paneLabel={id => t("files.pane", { number: id + 1 })}
        toggleLabel={t(secondOpen ? "files.hideSecondPane" : "files.showSecondPane")}
        onToggleSecond={() => {
          setSecondMounted(true);
          setSecondOpen(!secondOpen);
          setFocusedPane(secondOpen ? 0 : 1);
        }}
        renderPane={leafId => <DeviceDropTarget className="h-full min-h-0" onDropDevice={id => requestDevice(leafId, id)}>
          <FilesBrowser
            onDeviceChange={leafId === 0 ? reportFirstDevice : reportSecondDevice}
            primary={leafId === 0}
            deviceRequest={requests.get(leafId)}
            leafId={leafId}
            panesRef={panesRef}
          />
        </DeviceDropTarget>}
      />
    </SplitPageShell>
  );
}
