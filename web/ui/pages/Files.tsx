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
import { SplitPageShell } from "../components/split-layout";
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

const enc = new TextEncoder();
const dec = new TextDecoder();

/* ── Types ── */

interface FileEntry {
  name: string;
  path: string;
  isDir: boolean;
  size: number;
  modTime: string;
}

type TaskStatus =
  | "queued"
  | "running"
  | "paused"
  | "completed"
  | "failed"
  | "canceled";

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
  transfer?: { targetLeaf: number; parentDir: string };
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
  transferItemsTo(targetLeaf: number, items: PaneDragItem[]): Promise<void>;
  receiveUpload(file: File, parentDir: string): void;
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

function encodeFrame(typ: number, taskId: string, offset: number, payload?: ArrayBuffer) {
  const idb = enc.encode(taskId);
  const data = payload ? new Uint8Array(payload) : new Uint8Array(0);
  const buf = new ArrayBuffer(2 + idb.length + 8 + data.length);
  const out = new Uint8Array(buf);
  const view = new DataView(buf);
  out[0] = typ;
  out[1] = idb.length;
  out.set(idb, 2);
  view.setBigUint64(2 + idb.length, BigInt(offset));
  out.set(data, 2 + idb.length + 8);
  return buf;
}

function decodeFrame(buf: ArrayBuffer) {
  const u = new Uint8Array(buf);
  const len = u[1];
  const taskId = dec.decode(u.slice(2, 2 + len));
  const offset = Number(new DataView(buf).getBigUint64(2 + len));
  return { typ: u[0], taskId, offset, payload: buf.slice(2 + len + 8) };
}

function loadPersistedTasks(): Map<string, FileTask> {
  try {
    const raw = JSON.parse(localStorage.getItem(TASKS_KEY) || "[]") as FileTask[];
    return new Map(raw.map((task) => [task.id, task]));
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
function FilesBrowser({
  primary,
  deviceRequest,
  leafId,
  panesRef,
}: {
  primary: boolean;
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
  const [pathInput, setPathInput] = useState("");
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [listView, setListView] = useState<ListView>({ truncated: false, shown: 0, total: 0 });
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
    tasksRef.current = primary ? loadPersistedTasks() : new Map();
  const tasks = tasksRef.current;
  const filesByTaskRef = useRef(new Map<string, File>());
  const currentPathRef = useRef("");
  const parentPathRef = useRef("");
  const listOffsetRef = useRef(0);
  const listTotalRef = useRef(0);
  const listTruncatedRef = useRef(false);
  const activeDeviceRef = useRef("");
  const listedForRef = useRef("");
  const devicesRef = useRef(devices);
  devicesRef.current = devices;
  const tRef = useRef(t);
  tRef.current = t;
  const pwReqRef = useRef<{ deviceId: string; resolve: (pw: string | null) => void } | null>(null);
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
        entries: FileEntry[];
        resolve: (entries: FileEntry[]) => void;
        reject: (e: Error) => void;
      }
    >(),
  );
  const fileInputRef = useRef<HTMLInputElement>(null);
  /* Latest list() for WS handlers declared before it. */
  const listRef = useRef<(path: string, append?: boolean) => Promise<void>>(
    () => Promise.resolve(),
  );

  const bump = useCallback(() => setTasksTick((n) => n + 1), []);
  const saveTasks = useCallback(() => {
    if (primary) persistTasks(tasksRef.current!);
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
    async (deviceId: string): Promise<boolean> => {
      const dev = devicesRef.current.find((d) => d.id === deviceId);
      if (!dev || !dev.hasPassword) return true;
      if (await deviceAuthBypassed()) return true;
      const pw = await requestDevicePassword(
        deviceId,
        tRef.current("files.authRequired", { id: deviceId }),
      );
      if (!pw) return false;
      send({ op: "auth", deviceId, password: pw });
      const { promise, resolve } = Promise.withResolvers<boolean>();
      const timer = window.setTimeout(() => {
        authWaitersRef.current.delete(deviceId);
        resolve(false);
      }, 30000);
      authWaitersRef.current.set(deviceId, { resolve, pw, timer });
      return promise;
    },
    [requestDevicePassword, send],
  );

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
      send({
        op: "download_start",
        deviceId: task.device,
        taskId: task.id,
        path: task.path,
        offset: resumeOffset,
      });
      bump();
    },
    [saveTasks, send, bump],
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
        if (target) {
          target.receiveUpload(
            new File(task.blobParts, task.name || "download"),
            task.transfer.parentDir,
          );
        }
        task.saved = true;
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
      saveTasks();
    },
    [resumeDownload, saveTasks, panesRef],
  );

  const completeDownload = useCallback(
    (task: FileTask, offset: number) => {
      if (!task || task.dir !== "download" || task.saved) return;
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
      transfer?: { targetLeaf: number; parentDir: string },
    ) => {
      const activeDevice = activeDeviceRef.current;
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
      send({ op: "download_start", deviceId: activeDevice, taskId: task.id, path, offset: 0 });
    },
    [saveTasks, send, bump],
  );

  /* ── Uploads ── */

  const uploadLoop = useCallback(
    async (task: FileTask) => {
      const file = filesByTaskRef.current.get(task.id);
      if (!file) {
        task.status = "failed";
        task.message = tRef.current("files.reselect");
        saveTasks();
        bump();
        return;
      }
      for (let off = task.done || 0; off < file.size; off = task.done) {
        if (task.status !== "running") return;
        const chunk = await file.slice(off, off + CHUNK).arrayBuffer();
        const ws = wsRef.current;
        if (!ws || ws.readyState !== WebSocket.OPEN) return;
        ws.send(encodeFrame(FRAME_UPLOAD_CHUNK, task.id, off, chunk));
        const ack = Promise.withResolvers<void>();
        task.waitAck = ack.resolve;
        await ack.promise;
      }
      send({ op: "upload_end", taskId: task.id, path: task.path, size: file.size });
    },
    [saveTasks, send, bump],
  );

  const startUpload = useCallback(
    (file: File, destDir?: string) => {
      const activeDevice = activeDeviceRef.current;
      if (!activeDevice || !file) return;
      const pending = [...tasksRef.current!.values()].find(
        (task) =>
          task.dir === "upload" &&
          task.status === "failed" &&
          task.name === file.name &&
          task.size === file.size &&
          !filesByTaskRef.current.has(task.id),
      );
      if (pending) {
        filesByTaskRef.current.set(pending.id, file);
        pending.status = "running";
        pending.message = "";
        saveTasks();
        bump();
        send({
          op: "upload_start",
          deviceId: pending.device,
          taskId: pending.id,
          parentPath: pending.parentPath,
          name: pending.name,
          size: pending.size,
          modTime: new Date(file.lastModified).toISOString(),
        });
        return;
      }
      const parentDir = destDir ?? currentPathRef.current;
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
      send({
        op: "upload_start",
        deviceId: activeDevice,
        taskId: task.id,
        parentPath: parentDir,
        name: file.name,
        size: file.size,
        modTime: new Date(file.lastModified).toISOString(),
      });
    },
    [saveTasks, send, bump],
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
        return;
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
          waiter.entries = waiter.entries.concat(page);
          const total = Number(msg.total || msg.fileMode || waiter.entries.length);
          if (msg.truncated || waiter.entries.length < total) {
            send({
              op: "list",
              deviceId: activeDeviceRef.current,
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
        if (msg.error) {
          setEntries([]);
          setListView({ notice: String(msg.error), truncated: false, shown: 0, total: 0 });
          return;
        }
        const resultPath = typeof msg.path === "string" && msg.path ? msg.path : currentPathRef.current;
        const offset = Number(msg.offset || 0);
        const newEntries = (Array.isArray(msg.entries) ? msg.entries : []) as FileEntry[];
        const append = offset > 0 && resultPath === currentPathRef.current;
        currentPathRef.current = resultPath;
        parentPathRef.current =
          typeof msg.parent === "string" && msg.parent ? msg.parent : resultPath;
        setPathInput(resultPath);
        setEntries((prev) => (append ? prev.concat(newEntries) : newEntries));
        listOffsetRef.current = offset + newEntries.length;
        listTotalRef.current = Number(msg.total || msg.fileMode || listOffsetRef.current);
        listTruncatedRef.current =
          !!msg.truncated || listOffsetRef.current < listTotalRef.current;
        setListView({
          truncated: listTruncatedRef.current,
          shown: append ? listOffsetRef.current : newEntries.length,
          total: listTotalRef.current,
        });
        return;
      }
      const taskId = typeof msg.taskId === "string" ? msg.taskId : "";
      const task = tasksRef.current!.get(taskId);
      if (msg.op === "upload_ready" && task) {
        task.path = typeof msg.path === "string" ? msg.path : task.path;
        task.done = Number(msg.offset || 0);
        task.status = "running";
        saveTasks();
        void uploadLoop(task);
      } else if (msg.op === "download_start" && task) {
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
        if (task.dir === "upload") void listRef.current(currentPathRef.current);
      } else if ((msg.op === "transfer_error" || msg.op === "error") && task) {
        task.status = "failed";
        task.message =
          (typeof msg.message === "string" && msg.message) ||
          (typeof msg.error === "string" && msg.error) ||
          tRef.current("common.error");
        saveTasks();
      } else if (msg.op === "canceled" && task) {
        task.status = "canceled";
        saveTasks();
      }
      bump();
    },
    [saveTasks, uploadLoop, completeDownload, bump, send],
  );

  const onBinary = useCallback(
    (buf: ArrayBuffer) => {
      const f = decodeFrame(buf);
      const task = tasksRef.current!.get(f.taskId);
      if (!task) return;
      if (f.typ === FRAME_UPLOAD_ACK) {
        task.done = f.offset;
        updateSpeed(task);
        saveTasks();
        if (task.waitAck) {
          const r = task.waitAck;
          task.waitAck = null;
          r();
        }
      } else if (f.typ === FRAME_DOWNLOAD_CHUNK) {
        if (f.offset === (task.done || 0)) {
          task.blobParts = task.blobParts || [];
          task.blobParts.push(f.payload);
          task.done = f.offset + f.payload.byteLength;
          task.lastProgress = Date.now();
          task.message = "";
          updateSpeed(task);
          saveTasks();
        } else if (f.offset < (task.done || 0)) {
          // Duplicate chunk from a resumed request; ignore it.
        } else if (Date.now() - (task.lastRequested || 0) > 1000) {
          resumeDownload(task, task.done || 0, tRef.current("files.resuming"));
        }
      } else if (f.typ === FRAME_DOWNLOAD_END) {
        completeDownload(task, f.offset || task.done || 0);
      }
      bump();
    },
    [saveTasks, resumeDownload, completeDownload, bump],
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
        reconnectDelayRef.current = 1000;
        const queue = sendQueueRef.current;
        while (queue.length && ws.readyState === WebSocket.OPEN) ws.send(queue.shift()!);
        tasksRef.current!.forEach((task) => {
          if (task.dir === "download" && task.status === "running" && !task.saved) {
            resumeDownload(task, task.done || 0, tRef.current("files.resuming"));
          }
        });
      };
      ws.onmessage = (evt) => {
        if (evt.data instanceof ArrayBuffer) return onBinary(evt.data);
        if (evt.data instanceof Blob) {
          void evt.data.arrayBuffer().then(onBinary);
          return;
        }
        try {
          onText(JSON.parse(String(evt.data)));
        } catch {
          /* ignore malformed frames */
        }
      };
      ws.onclose = () => {
        if (wsRef.current === ws) wsRef.current = null;
        if (disposed) return;
        window.clearTimeout(reconnectTimerRef.current);
        reconnectTimerRef.current = window.setTimeout(connect, nextDelay());
      };
    };
    connect();
    return () => {
      disposed = true;
      window.clearTimeout(reconnectTimerRef.current);
      const ws = wsRef.current;
      if (ws) {
        ws.onclose = null;
        ws.close();
        wsRef.current = null;
      }
    };
  }, [onBinary, onText, resumeDownload]);

  /* Speed sampling + stalled-download watchdog + task re-render, every 500ms. */
  useEffect(() => {
    const timer = window.setInterval(() => {
      const now = Date.now();
      tasksRef.current!.forEach((task) => {
        if (task.status === "running") updateSpeed(task);
        if (
          task.dir === "download" &&
          task.status === "running" &&
          !task.saved &&
          now - (task.lastProgress || task.created || 0) > DOWNLOAD_STALL_MS &&
          now - (task.lastRequested || 0) > DOWNLOAD_RETRY_MS
        ) {
          resumeDownload(task, task.done || 0, tRef.current("files.resuming"));
        }
      });
      bump();
    }, 500);
    return () => window.clearInterval(timer);
  }, [resumeDownload, bump]);

  /* ── Directory listing ── */

  const list = useCallback(
    async (path: string, append = false) => {
      const activeDevice = activeDeviceRef.current;
      if (!activeDevice || !(await ensureAuth(activeDevice))) return;
      const targetPath = append ? currentPathRef.current : path;
      const offset = append ? listOffsetRef.current : 0;
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
        requestId: makeId(),
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
    [list, primary, setUrlParams],
  );

  /* Full (paginated) listing of one directory without touching the visible
   * listing; used by recursive transfer walks. */
  const listDirAll = useCallback(
    async (path: string): Promise<FileEntry[]> => {
      const activeDevice = activeDeviceRef.current;
      if (!activeDevice || !(await ensureAuth(activeDevice))) {
        throw new Error("device unavailable");
      }
      const requestId = makeId();
      const { promise, resolve, reject } = Promise.withResolvers<FileEntry[]>();
      const timer = window.setTimeout(() => {
        const w = listWaitersRef.current.get(requestId);
        listWaitersRef.current.delete(requestId);
        w?.reject(new Error("timeout"));
      }, 30000);
      listWaitersRef.current.set(requestId, {
        path,
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
    [ensureAuth, send],
  );

  /* Transfer entries from THIS pane's device to another pane's device:
   * files download to memory here, then upload there; directories walk
   * recursively (the target creates subdirs on upload). */
  const transferItemsTo = useCallback(
    async (targetLeaf: number, items: PaneDragItem[]) => {
      const target = panesRef.current.get(targetLeaf);
      if (!target || !target.deviceId()) {
        setAlertMsg(tRef.current("files.transferNoTarget"));
        return;
      }
      const destBase = target.currentPath();
      const walk = async (item: PaneDragItem, parentRel: string): Promise<void> => {
        if (!item.isDir) {
          startDownload(item.path, item.name, item.size, {
            targetLeaf,
            parentDir: destBase + parentRel,
          });
          return;
        }
        const sub = await listDirAll(item.path);
        for (const e of sub) {
          await walk(
            { name: e.name, isDir: e.isDir, size: e.size, path: e.path },
            parentRel + "/" + item.name,
          );
        }
      };
      for (const item of items) await walk(item, "");
    },
    [panesRef, startDownload, listDirAll],
  );

  /* Publish this pane's API for sibling panes. */
  useEffect(() => {
    panesRef.current.set(leafId, {
      deviceId: () => activeDeviceRef.current,
      currentPath: () => currentPathRef.current,
      transferItemsTo,
      receiveUpload: startUpload,
    });
    return () => {
      panesRef.current.delete(leafId);
    };
  }, [leafId, panesRef, transferItemsTo, startUpload]);

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
      if (act === "pause") {
        task.status = "paused";
        send({ op: "cancel", taskId: task.id, offset: task.done || 0 });
      }
      if (act === "resume") {
        task.status = "running";
        task.message = "";
        if (task.dir === "upload") {
          const file = filesByTaskRef.current.get(task.id);
          if (!file) {
            fileInputRef.current?.click();
            task.status = "failed";
            task.message = tRef.current("files.reselect");
          } else {
            send({
              op: "upload_start",
              deviceId: task.device,
              taskId: task.id,
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
        task.status = "canceled";
        send({ op: "cancel", taskId: task.id, offset: task.done || 0 });
      }
      if (act === "clear") tasksRef.current!.delete(task.id);
      saveTasks();
      bump();
    },
    [resumeDownload, saveTasks, send, bump],
  );

  /* ── UI handlers ── */

  const onFilesPicked = (e: ChangeEvent<HTMLInputElement>) => {
    [...(e.target.files || [])].forEach((f) => startUpload(f));
    e.target.value = "";
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
    <div className="flex h-full flex-col gap-3 overflow-y-auto p-4">
      {/* Toolbar */}
      <Card className="flex shrink-0 flex-wrap items-center gap-2 p-3">
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
          placeholder={t("files.path")}
          className="h-9 min-w-40 flex-1"
          onChange={(e) => setPathInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") void list(pathInput.trim());
          }}
        />
        <Button
          variant="outline"
          onClick={() => void list(parentPathRef.current || currentPathRef.current)}
        >
          <Icon name="Folder" />
          {t("files.up")}
        </Button>
        <Button variant="outline" onClick={() => void list(currentPathRef.current)}>
          <Icon name="Refresh" />
          {t("files.refresh")}
        </Button>
        <Button onClick={() => fileInputRef.current?.click()}>
          <Icon name="Upload" />
          {t("files.upload")}
        </Button>
      </Card>

      {/* Drop zone */}
      <div
        className={cn(
          "flex min-h-14 shrink-0 items-center justify-center gap-2 rounded-lg border-2 border-dashed border-border p-3 text-sm text-muted-foreground",
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
      <input ref={fileInputRef} type="file" multiple className="hidden" onChange={onFilesPicked} />

      <div className="grid items-start gap-3 xl:grid-cols-[minmax(0,1fr)_minmax(320px,400px)]">
        {/* Browser (also a cross-pane transfer drop target) */}
        <Card
          className="min-h-[40vh] overflow-hidden"
          onDragOver={onPaneDragOver}
          onDrop={onDrop}
        >
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("files.name")}</TableHead>
                <TableHead>{t("files.size")}</TableHead>
                <TableHead>{t("files.modified")}</TableHead>
                <TableHead>{t("files.actions")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {listView.notice ? (
                <TableRow>
                  <TableCell colSpan={4} className="py-12 text-center text-muted-foreground">
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
                              "inline-flex max-w-full cursor-pointer items-center gap-1.5 text-sm text-foreground hover:text-timeline-accent",
                            )}
                            onClick={() => {
                              if (entry.isDir) void list(entry.path);
                            }}
                          >
                            <Icon
                              name={entry.isDir ? "Folder" : "File"}
                              className="size-4 shrink-0"
                            />
                            <span className="truncate">{entry.name}</span>
                          </button>
                        </TableCell>
                        <TableCell>{entry.isDir ? "--" : fmtBytes(entry.size)}</TableCell>
                        <TableCell className="text-muted-foreground">
                          {entry.modTime ? new Date(entry.modTime).toLocaleString() : ""}
                        </TableCell>
                        <TableCell>
                          <div className="flex items-center gap-1">
                            {!entry.isDir && (
                              <Button
                                variant="outline"
                                size="sm"
                                onClick={() => startDownload(entry.path, entry.name, entry.size)}
                              >
                                <Icon name="Download" />
                                {t("files.download")}
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
        <Card className="p-3">
          <h2 className="mb-3 flex items-center gap-1.5 text-base font-semibold">
            <Icon name="Files" className="size-4" />
            {t("files.tasks")}
          </h2>
          <div className="flex max-h-[calc(100vh-260px)] flex-col gap-2.5 overflow-y-auto pr-0.5">
            {sortedTasks.length === 0 && (
              <div className="text-sm text-muted-foreground">{t("files.noTasks")}</div>
            )}
            {sortedTasks.map((task) => {
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
                    <div className="mt-2 h-1.5 overflow-hidden rounded-full border border-border bg-muted">
                      <div
                        className="h-full bg-timeline-accent transition-[width] duration-200"
                        style={{ width: pct + "%" }}
                      />
                    </div>
                    <div className="mt-1.5 text-xs text-muted-foreground">
                      {stat}
                      {task.message ? " · " + task.message : ""}
                    </div>
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    <Button
                      variant="outline"
                      size="icon"
                      className="size-8"
                      disabled={task.status !== "running"}
                      onClick={() => taskAction(task, "pause")}
                    >
                      <Icon name="Pause" />
                    </Button>
                    <Button
                      variant="outline"
                      size="icon"
                      className="size-8"
                      disabled={
                        task.status === "running" ||
                        task.status === "completed" ||
                        task.status === "canceled"
                      }
                      onClick={() => taskAction(task, "resume")}
                    >
                      <Icon name="Play" />
                    </Button>
                    <Button
                      variant="outline"
                      size="icon"
                      className="size-8"
                      disabled={task.status === "completed" || task.status === "canceled"}
                      onClick={() => taskAction(task, "cancel")}
                    >
                      <Icon name="X" />
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      className="h-8"
                      disabled={task.status === "running"}
                      onClick={() => taskAction(task, "clear")}
                    >
                      {t("files.clear")}
                    </Button>
                  </div>
                </div>
              );
            })}
          </div>
        </Card>
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

/* ── Single-pane wrapper: the page IS the rail + browser columns. ── */

export function FilesPage() {
  const seqRef = useRef(0);
  const [requests, setRequests] = useState<
    Map<number, { id: string; seq: number }>
  >(new Map());
  const panesRef = useRef(new Map<number, FilesPaneApi>());

  const requestDevice = (leafId: number, deviceId: string) => {
    setRequests((prev) =>
      new Map(prev).set(leafId, { id: deviceId, seq: ++seqRef.current }),
    );
  };

  return (
    <SplitPageShell
      storageKey="rdev.split.files.rail"
      onDevicePick={(id) => requestDevice(0, id)}
    >
      <DeviceDropTarget
        className="h-full min-h-0"
        onDropDevice={(id) => requestDevice(0, id)}
      >
        <FilesBrowser
          primary
          deviceRequest={requests.get(0)}
          leafId={0}
          panesRef={panesRef}
        />
      </DeviceDropTarget>
    </SplitPageShell>
  );
}
