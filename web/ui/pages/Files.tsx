import { readFilePreviewReply } from "../lib/file-preview-reply";
import { FilePreview } from "../components/agent/file-preview";
import type { FilePreviewState } from "../components/agent/types";
import { Dialog, DialogContent, DialogTitle } from "../components/dialog";
import { FilesPaneLayout } from "../components/files-pane-layout";
import { isImeComposing } from "../lib/keyboard";
import { createKeyedRequest } from "../lib/keyed-request";
import { createTransferAttempts } from "../lib/file-transfer-attempts";
import { RecursiveTransferError, walkTransferFiles, type TransferDestination } from "../lib/file-transfer-route";
import { encodeFrame, decodeFrame } from "../lib/file-transfer-frame";
import { uploadChunks } from "../lib/upload-chunks";
import { availableFileTaskActions, type FileTaskStatus, canResumeUpload, createListingGuard, readListingPage, restoreFileTask, uploadResumeRequest } from "../lib/file-request-state";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ChangeEvent,
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
import { Icon } from "../components/icon";
import { OsIcon } from "../components/os-icon";

/* ── Protocol constants (unchanged from the vanilla page) ── */

const CHUNK = 512 * 1024;
const FILE_LIST_LIMIT = 100;
const DOWNLOAD_STALL_MS = 12000;
const DOWNLOAD_RETRY_MS = 2500;
const TASKS_KEY = "rdevFileTasks";

const FRAME_UPLOAD_CHUNK = 0x20;
const FRAME_UPLOAD_ACK = 0x21;
const FRAME_DOWNLOAD_CHUNK = 0x22;
const FRAME_DOWNLOAD_END = 0x23;


/* ── Types ── */

interface FileEntry {
  name: string;
  path: string;
  isDir: boolean;
  size: number;
  modTime: string;
}

type TaskStatus = FileTaskStatus;

interface FileTask {
  id: string;
  dir: "upload" | "download";
  device: string;
  name: string;
  path?: string;
  parentPath?: string;
  size: number;
  done: number;
  status: TaskStatus;
  created: number;
  message?: string;
  speed?: number;
  samples?: { t: number; n: number }[];
  lastProgress?: number;
  lastRequested?: number;
  saved?: boolean;
  blobParts?: ArrayBuffer[];
  waitAck?: (() => void) | null;
  /* Cross-pane transfer: on completion the bytes go to the target pane's
   * upload queue (parentDir on the target device) instead of a browser save. */
  transfer?: TransferDestination;
}

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
  receiveUpload(file: File, parentDir: string, device: string): void;
}

interface ListView {
  notice?: string;
  truncated: boolean;
  shown: number;
  total: number;
}

/* ── Helpers (ported verbatim from files.html) ── */

function makeId() {
  return "task-" + Date.now().toString(36) + "-" + Math.random().toString(36).slice(2);
}

function fmtBytes(n: number) {
  if (!Number.isFinite(n)) return "--";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return (i ? v.toFixed(v < 10 ? 1 : 0) : v.toFixed(0)) + " " + u[i];
}

function updateSpeed(task: FileTask) {
  const now = Date.now();
  task.samples = (task.samples || []).filter((p) => now - p.t < 5000);
  task.samples.push({ t: now, n: task.done || 0 });
  const first = task.samples[0];
  const last = task.samples[task.samples.length - 1];
  task.speed =
    first && last && last.t > first.t
      ? Math.max(0, ((last.n - first.n) * 1000) / (last.t - first.t))
      : 0;
}

function loadPersistedTasks(reselectMessage: string): Map<string, FileTask> {
  try {
    const raw = JSON.parse(localStorage.getItem(TASKS_KEY) || "[]") as FileTask[];
    return new Map(raw.map((task) => [task.id, restoreFileTask(task, reselectMessage)]));
  } catch {
    return new Map();
  }
}

function persistTasks(tasks: Map<string, FileTask>) {
  localStorage.setItem(
    TASKS_KEY,
    JSON.stringify(
      [...tasks.values()].map(
        ({ blobParts, samples, waitAck, ...rest }) => {
          void blobParts;
          void samples;
          void waitAck;
          return rest;
        },
      ),
    ),
  );
}

function statusBadgeClass(s: TaskStatus) {
  switch (s) {
    case "running":
      return "border-transparent bg-timeline-accent/15 text-timeline-accent";
    case "completed":
      return "border-transparent bg-success/15 text-success";
    case "failed":
      return "border-transparent bg-destructive/15 text-destructive-text";
    case "paused":
      return "border-transparent bg-attention/15 text-warning-text";
    default:
      return "border-transparent bg-muted text-muted-foreground";
  }
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
  const { t } = useI18n();
  const { devices } = useDevices();
  const [urlParams, setUrlParams] = useSearchParams();
  const searchParams = primary ? urlParams : EMPTY_PARAMS;

  const [device, setDeviceState] = useState("");
  useEffect(() => { onDeviceChange?.(device); }, [device, onDeviceChange]);
  const [preview, setPreview] = useState<{ id: string; device: string; path: string; state: FilePreviewState } | null>(null);
  const previewRequest = useRef("");
  const closePreview = useCallback(() => { previewRequest.current = ""; setPreview(null); }, []);
  useEffect(() => {
    if (!preview || preview.state.status !== "loading") return;
    const id = preview.id;
    const timer = window.setTimeout(() => {
      if (previewRequest.current !== id) return;
      previewRequest.current = "";
      setPreview(current => current?.id === id ? { ...current, state: { ...current.state, status: "error", message: t("files.previewTimeout") } } : current);
    }, 30000);
    return () => window.clearTimeout(timer);
  }, [preview, t]);
  useEffect(() => () => { previewRequest.current = ""; }, []);
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
    tasksRef.current = primary ? loadPersistedTasks(t("files.reselect")) : new Map();
  const tasks = tasksRef.current;
  const transferAuth = useRef(new Map<string, string>());
  const transferAttempts = useRef(createTransferAttempts(makeId));
  const filesByTaskRef = useRef(new Map<string, File>());
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
    if (primary) persistTasks(tasksRef.current!);
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

  const startTransfer = useCallback((task: FileTask, request: Record<string, unknown>) => {
    const previous = transferAttempts.current.current(task.id);
    if (previous && wsRef.current?.readyState === WebSocket.OPEN) send({ op: "cancel", taskId: previous, offset: task.done || 0 });
    const wireId = transferAttempts.current.begin(task.id);
    const epoch = connectionEpoch.current;
    transferAuth.current.set(task.id, wireId);
    const current = () => epoch === connectionEpoch.current &&
      transferAttempts.current.current(task.id) === wireId &&
      (task.status === "running" || task.status === "queued");
    void requireConnectedDevice(task.device).then(() => {
      if (!current()) return;
      send({ ...request, deviceId: task.device, taskId: wireId });
    }).catch((error) => {
      if (!current()) return;
      task.status = "failed";
      task.message = error instanceof Error ? error.message : String(error);
      saveTasks();
      bump();
    }).finally(() => {
      if (transferAuth.current.get(task.id) === wireId) transferAuth.current.delete(task.id);
    });
  }, [send, requireConnectedDevice, saveTasks, bump]);

  const openPreview = useCallback((path: string) => {
    const device = activeDeviceRef.current;
    const id = makeId();
    previewRequest.current = id;
    setPreview({ id, device, path, state: { status: "loading", content: "", message: "", truncated: false } });
    void requireConnectedDevice(device).then(() => {
      if (previewRequest.current === id && activeDeviceRef.current === device) {
        send({ op: "read", deviceId: device, requestId: id, path });
      }
    }).catch(error => {
      if (previewRequest.current !== id) return;
      previewRequest.current = "";
      setPreview(current => current?.id === id ? { ...current, state: { ...current.state, status: "error", message: String(error instanceof Error ? error.message : error) } } : current);
    });
  }, [requireConnectedDevice, send]);

  /* ── Downloads ── */

  const resumeDownload = useCallback(
    (task: FileTask, offset: number, message?: string) => {
      if (!task || task.dir !== "download" || task.saved) return;
      let resumeOffset = Number(offset || 0);
      const buffered = (task.blobParts || []).reduce(
        (sum, part) => sum + (part.byteLength || 0),
        0,
      );
      if (resumeOffset > 0 && buffered !== resumeOffset) {
        resumeOffset = 0;
        task.blobParts = [];
      }
      task.done = resumeOffset;
      task.status = "running";
      task.message = message || "";
      task.lastRequested = Date.now();
      saveTasks();
      startTransfer(task, {
        op: "download_start",
        deviceId: task.device,
        path: task.path,
        offset: resumeOffset,
      });
      bump();
    },
    [saveTasks, send, bump, startTransfer],
  );

  const finishDownload = useCallback(
    (task: FileTask) => {
      if (task.dir !== "download" || !task.blobParts || task.saved) return;
      const blob = new Blob(task.blobParts);
      if (task.size && blob.size < task.size) {
        resumeDownload(task, blob.size, tRef.current("files.resuming"));
        return;
      }
      /* Cross-pane transfer: hand the bytes to the target pane's upload
       * queue instead of saving in the browser. */
      if (task.transfer) {
        const target = panesRef.current.get(task.transfer.targetLeaf);
        if (!target || !task.transfer.device) {
          task.status = "failed";
          task.message = tRef.current("files.transferNoTarget");
          saveTasks();
          return;
        }
        target.receiveUpload(
          new File(task.blobParts, task.name || "download"),
          task.transfer.parentDir,
          task.transfer.device,
        );
        task.saved = true;
        task.blobParts = undefined;
        saveTasks();
        return;
      }
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = task.name || "download";
      document.body.appendChild(a);
      a.click();
      setTimeout(() => {
        URL.revokeObjectURL(a.href);
        a.remove();
      }, 1000);
      task.saved = true;
      task.blobParts = undefined;
      saveTasks();
    },
    [resumeDownload, saveTasks, panesRef],
  );

  const completeDownload = useCallback(
    (task: FileTask, offset: number) => {
      if (!task || task.dir !== "download" || task.saved || task.status !== "running") return;
      const finalOffset = Number(offset || task.done || 0);
      if (task.size && finalOffset < task.size) {
        task.done = Math.max(task.done || 0, finalOffset);
        resumeDownload(task, task.done || 0, tRef.current("files.resuming"));
        return;
      }
      task.done = finalOffset;
      task.status = "completed";
      task.message = "";
      saveTasks();
      finishDownload(task);
    },
    [resumeDownload, saveTasks, finishDownload],
  );

  const startDownload = useCallback(
    (
      path: string,
      name: string,
      size: number,
      transfer?: TransferDestination,
      activeDevice = activeDeviceRef.current,
    ) => {
      const now = Date.now();
      const task: FileTask = {
        id: makeId(),
        dir: "download",
        device: activeDevice,
        name,
        path,
        size: Number(size) || 0,
        done: 0,
        status: "running",
        created: now,
        lastProgress: now,
        lastRequested: now,
        speed: 0,
        blobParts: [],
        transfer,
      };
      tasksRef.current!.set(task.id, task);
      saveTasks();
      bump();
      startTransfer(task, { op: "download_start", path, offset: 0 });
    },
    [saveTasks, send, bump, startTransfer],
  );

  /* ── Uploads ── */

  const stopUpload = useCallback((task: FileTask) => {
    uploadRunsRef.current.get(task.id)?.abort();
    uploadRunsRef.current.delete(task.id);
    task.waitAck = null;
  }, []);

  const uploadLoop = useCallback(
    async (task: FileTask) => {
      stopUpload(task);
      const wireId = transferAttempts.current.current(task.id);
      if (!wireId) return;
      const controller = new AbortController();
      uploadRunsRef.current.set(task.id, controller);
      try {
        const file = filesByTaskRef.current.get(task.id);
        if (!file) throw new Error(tRef.current("files.reselect"));
        const complete = await uploadChunks({
          file, chunkSize: CHUNK, offset: () => task.done || 0, signal: controller.signal,
          write: (chunk, offset) => {
            const ws = wsRef.current;
            if (!ws || ws.readyState !== WebSocket.OPEN) throw new Error(tRef.current("files.disconnected"));
            const ack = Promise.withResolvers<void>();
            task.waitAck = ack.resolve;
            ws.send(encodeFrame(FRAME_UPLOAD_CHUNK, wireId, offset, chunk));
            return ack.promise;
          },
        });
        if (complete && !controller.signal.aborted && transferAttempts.current.current(task.id) === wireId && task.status === "running") send({ op: "upload_end", taskId: wireId, path: task.path, size: file.size });
      } catch (error) {
        if (!controller.signal.aborted) {
          task.status = "failed";
          task.message = error instanceof Error ? error.message : String(error);
          saveTasks();
          bump();
        }
      } finally {
        if (uploadRunsRef.current.get(task.id) === controller) {
          uploadRunsRef.current.delete(task.id);
          task.waitAck = null;
        }
      }
    },
    [saveTasks, send, bump, stopUpload],
  );

  const startUpload = useCallback(
    (file: File, destDir?: string, activeDevice = activeDeviceRef.current) => {
      if (!activeDevice || !file) return;
      const parentDir = destDir ?? currentPathRef.current;
      const pending = [...tasksRef.current!.values()].find(
        (task) =>
          canResumeUpload(task, { device: activeDevice, path: parentDir, name: file.name, size: file.size }) &&
          !filesByTaskRef.current.has(task.id),
      );
      if (pending) {
        filesByTaskRef.current.set(pending.id, file);
        pending.status = "running";
        pending.message = "";
        saveTasks();
        bump();
        startTransfer(pending, {
          op: "upload_start",
          deviceId: pending.device,
          parentPath: pending.parentPath,
          name: pending.name,
          size: pending.size,
          modTime: new Date(file.lastModified).toISOString(),
        });
        return;
      }
      const task: FileTask = {
        id: makeId(),
        dir: "upload",
        device: activeDevice,
        name: file.name,
        parentPath: parentDir,
        size: file.size,
        done: 0,
        status: "queued",
        created: Date.now(),
        speed: 0,
      };
      tasksRef.current!.set(task.id, task);
      filesByTaskRef.current.set(task.id, file);
      saveTasks();
      bump();
      startTransfer(task, {
        op: "upload_start",
        deviceId: activeDevice,
        parentPath: parentDir,
        name: file.name,
        size: file.size,
        modTime: new Date(file.lastModified).toISOString(),
      });
    },
    [saveTasks, send, bump, startTransfer],
  );

  /* ── WS message handlers ── */

  const onText = useCallback(
    (msg: Record<string, unknown>) => {
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
      const previewReply = readFilePreviewReply(previewRequest.current, activeDeviceRef.current, msg);
      if (previewReply) {
        const id = previewRequest.current;
        previewRequest.current = "";
        setPreview(current => current?.id === id ? { ...current, state: previewReply } : current);
        return;
      }
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
      const taskId = typeof msg.taskId === "string" ? msg.taskId : "";
      const task = tasksRef.current!.get(transferAttempts.current.resolve(taskId) || "");
      if (msg.op === "upload_ready" && task) {
        if (task.status !== "queued" && task.status !== "running") return;
        task.path = typeof msg.path === "string" ? msg.path : task.path;
        task.done = Number(msg.offset || 0);
        task.status = "running";
        saveTasks();
        void uploadLoop(task);
      } else if (msg.op === "download_start" && task) {
        if (task.dir !== "download" || task.status !== "running" || task.saved) return;
        task.size = Number(msg.size || 0) || task.size;
        task.name = typeof msg.name === "string" && msg.name ? msg.name : task.name;
        task.status = "running";
        task.message = "";
        task.lastProgress = Date.now();
        if (Number(msg.offset || 0) === 0 && (task.done || 0) === 0) task.blobParts = [];
        saveTasks();
      } else if (msg.op === "transfer_end" && task) {
        completeDownload(task, Number(msg.offset || task.done || 0));
        /* Upload finished (browser upload or cross-pane transfer): refresh
         * the visible listing so the new file shows up. */
        if (task.dir === "upload" && task.status === "running") {
          stopUpload(task);
          task.status = "completed";
          task.done = task.size;
          task.message = "";
          filesByTaskRef.current.delete(task.id);
          saveTasks();
          if (task.device === activeDeviceRef.current && task.parentPath === currentPathRef.current) void listRef.current(currentPathRef.current);
        }
      } else if ((msg.op === "transfer_error" || msg.op === "error") && task) {
        if (task.status !== "running" && task.status !== "queued") return;
        stopUpload(task);
        task.status = "failed";
        task.message =
          (typeof msg.message === "string" && msg.message) ||
          (typeof msg.error === "string" && msg.error) ||
          tRef.current("common.error");
        saveTasks();
      } else if (msg.op === "canceled" && task) {
        if (task.status === "completed" || task.status === "failed") return;
        stopUpload(task);
        if (task.status !== "paused") {
          task.status = "canceled";
          task.blobParts = undefined;
          filesByTaskRef.current.delete(task.id);
        }
        saveTasks();
      }
      bump();
    },
    [saveTasks, uploadLoop, completeDownload, bump, send, stopUpload],
  );

  const onBinary = useCallback(
    (buf: ArrayBuffer) => {
      const f = decodeFrame(buf);
      if (!f) return;
      const task = tasksRef.current!.get(transferAttempts.current.resolve(f.taskId) || "");
      if (!task) return;
      if (f.typ === FRAME_UPLOAD_ACK) {
        if (task.dir !== "upload" || task.status !== "running") return;
        task.done = f.offset;
        progressDirty.current = true;
        if (task.waitAck) {
          const r = task.waitAck;
          task.waitAck = null;
          r();
        }
      } else if (f.typ === FRAME_DOWNLOAD_CHUNK) {
        if (task.dir !== "download" || task.status !== "running" || task.saved) return;
        if (f.offset === (task.done || 0)) {
          task.blobParts = task.blobParts || [];
          task.blobParts.push(f.payload);
          task.done = f.offset + f.payload.byteLength;
          task.lastProgress = Date.now();
          task.message = "";
          progressDirty.current = true;
        } else if (f.offset < (task.done || 0)) {
          // Duplicate chunk from a resumed request; ignore it.
        } else if (Date.now() - (task.lastRequested || 0) > 1000) {
          resumeDownload(task, task.done || 0, tRef.current("files.resuming"));
        }
      } else if (f.typ === FRAME_DOWNLOAD_END) {
        completeDownload(task, f.offset || task.done || 0);
        bump();
      }
    },
    [resumeDownload, completeDownload, bump],
  );

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
            resumeDownload(task, task.done || 0, tRef.current("files.resuming"));
          }
        });
        if (activeDeviceRef.current) void listRef.current(currentPathRef.current);
      };
      ws.onmessage = (evt) => {
        if (disposed || wsRef.current !== ws) return;
        if (evt.data instanceof ArrayBuffer) return onBinary(evt.data);
        if (evt.data instanceof Blob) {
          void evt.data.arrayBuffer().then((data) => {
            if (!disposed && wsRef.current === ws) onBinary(data);
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
        sendQueueRef.current = [];
        endPendingRequests();
        listingGuard.current.invalidate();
        directoryReadyRef.current = false;
        setListView((previous) => ({ ...previous, notice: tRef.current("files.connectionLost") }));
        tasksRef.current!.forEach((task) => {
          if (task.dir === "upload" && (task.status === "running" || task.status === "queued")) {
            stopUpload(task);
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
      const pendingPreview = previewRequest.current;
      previewRequest.current = "";
      if (!disposed && pendingPreview) {
        setPreview(current => current?.id === pendingPreview && current.state.status === "loading"
          ? { ...current, state: { ...current.state, status: "error", message: tRef.current("files.connectionLost") } }
          : current);
      }
      connectionEpoch.current++;
      transferAuth.current.clear();
      pwReqRef.current?.resolve(null);
      pwReqRef.current = null;
      if (!disposed) setPwDialog(null);
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
    };
  }, [onBinary, onText, resumeDownload, stopUpload, saveTasks, bump, ensureAuth]);

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
          !transferAuth.current.has(task.id) &&
          task.dir === "download" &&
          task.status === "running" &&
          !task.saved &&
          now - (task.lastProgress || task.created || 0) > DOWNLOAD_STALL_MS &&
          now - (task.lastRequested || 0) > DOWNLOAD_RETRY_MS
        ) {
          resumeDownload(task, task.done || 0, tRef.current("files.resuming"));
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
  }, [resumeDownload, bump, saveTasks]);

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
      if (!append) {
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

  /* Transfer entries from THIS pane's device to another pane's device:
   * files download to memory here, then upload there; directories walk
   * recursively, explicitly creating empty directories on the destination. */
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
          download: (item, device, transfer) => {
            startDownload(item.path, item.name, item.size, transfer, device);
          },
        });
      } catch (error) {
        setAlertMsg(error instanceof RecursiveTransferError
          ? tRef.current("files.transferIntoSelf")
          : error instanceof Error ? error.message : String(error));
      }
    },
    [panesRef, startDownload, listDirAll],
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
      if (act === "pause") {
        stopUpload(task);
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
            startTransfer(task, {
              op: "upload_start",
              deviceId: task.device,
              parentPath: task.parentPath,
              name: task.name,
              size: task.size,
            });
          }
        } else {
          task.blobParts = task.blobParts || [];
          resumeDownload(task, task.done || 0, tRef.current("files.resuming"));
        }
      }
      if (act === "cancel") {
        stopUpload(task);
        task.status = "canceled";
        task.blobParts = undefined;
        filesByTaskRef.current.delete(task.id);
        send({ op: "cancel", taskId: transferAttempts.current.current(task.id), offset: task.done || 0 });
      }
      if (act === "clear") {
        stopUpload(task);
        transferAttempts.current.remove(task.id);
        tasksRef.current!.delete(task.id);
        filesByTaskRef.current.delete(task.id);
      }
      saveTasks();
      bump();
    },
    [resumeDownload, saveTasks, send, bump, stopUpload, startTransfer],
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
      startTransfer(task, request);
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
      {/* Toolbar */}
      <div className="page-toolbar">
        {/* Device is picked from the left rail; the toolbar only shows it. */}
        <div className="flex h-9 items-center gap-1.5 rounded-md border border-border-seam px-2.5 text-sm text-muted-foreground select-none">
          <OsIcon
            os={devices.find((d) => d.id === device)?.os}
            className="shrink-0"
          />
          <span className="max-w-48 truncate">
            {device || t("terminal.devicePlaceholder")}
          </span>
        </div>
        <Input
          spellCheck={false}
          autoComplete="off"
          value={pathInput}
          aria-label={t("files.path")}
          disabled={!device}
          placeholder={t("files.path")}
          className="h-9 min-w-40 flex-1"
          onChange={(e) => setPathInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !isImeComposing(e.nativeEvent)) void list(pathInput.trim());
          }}
        />
        <Button
          variant="outline"
          aria-label={t("files.up")}
          disabled={!device}
          onClick={() => void list(parentPathRef.current || currentPathRef.current)}
        >
          <Icon name="Folder" />
          <span className="hidden @min-[480px]/files:inline">{t("files.up")}</span>
        </Button>
        <Button variant="outline" aria-label={t("files.refresh")} disabled={!device} onClick={() => void list(currentPathRef.current)}>
          <Icon name="Refresh" />
          <span className="hidden @min-[480px]/files:inline">{t("files.refresh")}</span>
        </Button>
        <Button disabled={!device} onClick={() => { reselectTaskRef.current = null; fileInputRef.current?.click(); }}>
          <Icon name="Upload" />
          {t("files.upload")}
        </Button>
        <Button variant="ghost" aria-label={t("files.tasks")} aria-expanded={tasksOpen} onClick={() => setTasksOpen((open) => !open)}>
          <Icon name="Files" /><span className="hidden @min-[480px]/files:inline">{t("files.tasks")}</span>{sortedTasks.length > 0 && ` (${sortedTasks.length})`}
        </Button>
      </div>

      {/* Drop zone */}
      <div
        className={cn(
          "flex min-h-9 shrink-0 items-center justify-center gap-2 border-b border-dashed border-border-seam px-3 py-1 text-xs text-muted-foreground",
          dragging && "border-timeline-accent bg-timeline-accent/5 text-timeline-accent",
        )}
        onDragOver={(e) => {
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={onDrop}
      >
        <Icon name="Upload" />
        {t("files.dropHint")}
      </div>
      <input ref={fileInputRef} type="file" multiple className="hidden" onChange={onFilesPicked}
        onClick={() => {
          uploadDestinationRef.current = reselectTaskRef.current ? null : {
            device: activeDeviceRef.current, path: currentPathRef.current,
          };
        }} />

      <div className="file-workspace-grid min-h-0 flex-1 overflow-hidden" data-tasks-open={tasksOpen}>
        {/* Browser (also a cross-pane transfer drop target) */}
        <Card
          className="min-h-0 min-w-0 flex-1 overflow-auto rounded-none border-0 [&>div]:h-full"
          onDragOver={onPaneDragOver}
          onDrop={onDrop}
        >
          <Table className="table-fixed">
            <TableHeader className="sticky top-0 z-10 bg-background">
              <TableRow>
                <TableHead>{t("files.name")}</TableHead>
                <TableHead className="w-20 whitespace-nowrap">{t("files.size")}</TableHead>
                <TableHead className="hidden w-48 @min-[800px]/files:table-cell">{t("files.modified")}</TableHead>
                <TableHead className="w-28 @min-[480px]/files:w-40">{t("files.actions")}</TableHead>
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
                          <div className="flex items-center gap-1">
                            {!entry.isDir && (
                              <Button
                                variant="outline"
                                size="sm"
                                aria-label={t("files.download")}
                                onClick={() => startDownload(entry.path, entry.name, entry.size)}
                              >
                                <Icon name="Download" />
                                <span className="hidden @min-[480px]/files:inline">{t("files.download")}</span>
                              </Button>
                            )}
                            {/* Cross-pane transfer menu (split view). */}
                            <DropdownMenu>
                              <DropdownMenuTrigger asChild>
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  className="size-7"
                                  aria-label={t("files.transfer")}
                                >
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

        {/* Tasks */}
        <div className="file-task-region min-h-0 min-w-0 overflow-hidden" inert={!tasksOpen} aria-hidden={!tasksOpen}>
        <Card className="h-full min-h-0 min-w-0 overflow-y-auto rounded-none border-0 border-t border-border-seam bg-sidebar p-3 @min-[800px]/files:border-t-0 @min-[800px]/files:border-l">
          <h2 className="mb-3 flex items-center gap-1.5 text-base font-semibold">
            <Icon name="Files" className="size-4" />
            {t("files.tasks")}
          </h2>
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
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    {(["pause", "resume", "cancel"] as const).map(action => actions[action] && (
                      <Button
                        key={action}
                        variant="outline"
                        size="icon"
                        className="size-8"
                        aria-label={t(`files.${action}`)}
                        onClick={() => taskAction(task, action)}
                      >
                        <Icon name={action === "pause" ? "Pause" : action === "resume" ? "Play" : "X"} />
                      </Button>
                    ))}
                    {actions.clear && <Button
                      variant="outline"
                      size="sm"
                      className="h-8"
                      onClick={() => taskAction(task, "clear")}
                    >
                      {t("files.clear")}
                    </Button>}
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
            <Input
              type="password"
              aria-label={t("common.password")}
              autoComplete="current-password"
              autoFocus
              value={pwValue}
              placeholder={t("terminal.passwordPlaceholder")}
              onChange={(e) => setPwValue(e.target.value)}
            />
            <AlertDialogFooter className="mt-4">
              <AlertDialogCancel type="button" onClick={cancelPassword}>
                {t("common.cancel")}
              </AlertDialogCancel>
              <AlertDialogAction type="submit" disabled={!pwValue}>
                {t("common.connect")}
              </AlertDialogAction>
            </AlertDialogFooter>
          </form>
        </AlertDialogContent>
      </AlertDialog>

      <Dialog open={preview !== null} onOpenChange={open => { if (!open) closePreview(); }}>
        <DialogContent className="flex h-[min(80dvh,800px)] min-h-0 w-full md:w-[calc(100vw-2rem)] max-w-5xl flex-col overflow-hidden p-0">
          <DialogTitle className="sr-only">{preview?.path || t("files.name")}</DialogTitle>
          {preview && <div className="min-h-0 flex-1 md:pt-8">
            <FilePreview key={preview.device + ":" + preview.path} path={preview.path} state={preview.state}
              connected={device === preview.device} onRefresh={() => openPreview(preview.path)} t={t} />
          </div>}
          <div className="flex shrink-0 justify-end gap-2 border-t border-border-seam px-3 py-2">
            <Button variant="outline" size="sm" onClick={() => {
              if (preview) startDownload(preview.path, preview.path.split(/[\\/]/).pop() || "download", 0, undefined, preview.device);
            }}>{t("files.download")}</Button>
            <Button variant="ghost" size="sm" onClick={closePreview}>{t("common.close")}</Button>
          </div>
        </DialogContent>
      </Dialog>

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
