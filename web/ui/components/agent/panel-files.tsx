/* PanelFiles: one device's file manager inside an agent right-panel tab
 * (scoped port of the standalone Files page). Self-contained /files WS
 * client with the same wire protocol (list paging, upload with per-chunk
 * acks, download resume, cancel/pause/clear). The tab strip keeps the panel
 * mounted while hidden, so the socket and in-flight transfers survive tab
 * switches; only the speed-sampling/stall-watchdog ticker is gated on
 * visibility. Tasks persist per device under `rdevFileTasks.<device>` so the
 * panel never clobbers the standalone page's `rdevFileTasks`. */
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ChangeEvent,
  type DragEvent,
} from "react";
import {
  wsURL,
  getDevicePassword,
  rememberDevicePassword,
  forgetDevicePassword,
} from "../../lib/api";
import { cn } from "../../lib/utils";
import { Button } from "../button";
import { Input } from "../input";
import { Badge } from "../badge";
import { Icon } from "../icon";
import { TabAuthOverlay } from "../../pages/Terminal";
import type { Translator } from "./types";

/* ── Protocol constants (mirrors the Files page) ── */

const CHUNK = 512 * 1024;
const FILE_LIST_LIMIT = 100;
const DOWNLOAD_STALL_MS = 12000;
const DOWNLOAD_RETRY_MS = 2500;
const TASKS_KEY_PREFIX = "rdevFileTasks.";

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
}

interface ListView {
  notice?: string;
  truncated: boolean;
  shown: number;
  total: number;
}

/* ── Helpers (ported from the Files page) ── */

function makeId() {
  return (
    "task-" + Date.now().toString(36) + "-" + Math.random().toString(36).slice(2)
  );
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

function encodeFrame(
  typ: number,
  taskId: string,
  offset: number,
  payload?: ArrayBuffer,
) {
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

function loadPersistedTasks(device: string): Map<string, FileTask> {
  try {
    const raw = JSON.parse(
      localStorage.getItem(TASKS_KEY_PREFIX + device) || "[]",
    ) as FileTask[];
    return new Map(raw.map((task) => [task.id, task]));
  } catch {
    return new Map();
  }
}

function persistTasks(device: string, tasks: Map<string, FileTask>) {
  localStorage.setItem(
    TASKS_KEY_PREFIX + device,
    JSON.stringify(
      [...tasks.values()].map(({ blobParts, samples, waitAck, ...rest }) => {
        void blobParts;
        void samples;
        void waitAck;
        return rest;
      }),
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

/* ── Panel ── */

export function PanelFiles({
  device,
  visible,
  t,
}: {
  device: string;
  /* false while another panel tab is active: the panel stays mounted but is
   * display:none — the socket and transfers keep running, only the speed /
   * stall ticker is paused. */
  visible: boolean;
  t: Translator;
}) {
  const [pathInput, setPathInput] = useState("");
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [listView, setListView] = useState<ListView>({
    truncated: false,
    shown: 0,
    total: 0,
  });
  const [dragging, setDragging] = useState(false);
  const [authOpen, setAuthOpen] = useState(false);
  const [authError, setAuthError] = useState("");
  const [authRound, setAuthRound] = useState(0);
  const [, setTasksTick] = useState(0);

  /* Refs: mutable transfer/listing state shared with WS handlers. */
  const wsRef = useRef<WebSocket | null>(null);
  const sendQueueRef = useRef<string[]>([]);
  const reconnectDelayRef = useRef(1000);
  const reconnectTimerRef = useRef(0);
  const tasksRef = useRef<Map<string, FileTask> | null>(null);
  if (!tasksRef.current) tasksRef.current = loadPersistedTasks(device);
  const filesByTaskRef = useRef(new Map<string, File>());
  const currentPathRef = useRef("");
  const parentPathRef = useRef("");
  const listOffsetRef = useRef(0);
  const listTotalRef = useRef(0);
  const listTruncatedRef = useRef(false);
  const deviceRef = useRef(device);
  deviceRef.current = device;
  const tRef = useRef(t);
  tRef.current = t;
  const pendingPwRef = useRef("");
  const authOpenRef = useRef(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const bump = useCallback(() => setTasksTick((n) => n + 1), []);
  const saveTasks = useCallback(
    () => persistTasks(deviceRef.current, tasksRef.current!),
    [],
  );

  const openAuth = useCallback(() => {
    if (authOpenRef.current) return;
    authOpenRef.current = true;
    setAuthRound((r) => r + 1);
    setAuthOpen(true);
  }, []);

  const closeAuth = useCallback(() => {
    authOpenRef.current = false;
    setAuthOpen(false);
    setAuthError("");
  }, []);

  /* ── WS plumbing ── */

  const send = useCallback((msg: Record<string, unknown>) => {
    const payload = JSON.stringify(msg);
    const ws = wsRef.current;
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(payload);
    else sendQueueRef.current.push(payload);
  }, []);

  /* ── Directory listing ── */

  const list = useCallback(
    (path: string, append = false) => {
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
        deviceId: deviceRef.current,
        requestId: makeId(),
        path: targetPath,
        offset,
        limit: FILE_LIST_LIMIT,
      });
    },
    [send],
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
    [resumeDownload, saveTasks],
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
    (path: string, name: string, size: number) => {
      const activeDevice = deviceRef.current;
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
      };
      tasksRef.current!.set(task.id, task);
      saveTasks();
      bump();
      send({
        op: "download_start",
        deviceId: activeDevice,
        taskId: task.id,
        path,
        offset: 0,
      });
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
      send({
        op: "upload_end",
        taskId: task.id,
        path: task.path,
        size: file.size,
      });
    },
    [saveTasks, send, bump],
  );

  const sendUploadStart = useCallback(
    (task: FileTask, modTime?: string) => {
      send({
        op: "upload_start",
        deviceId: task.device,
        taskId: task.id,
        parentPath: task.parentPath,
        name: task.name,
        size: task.size,
        ...(modTime ? { modTime } : {}),
      });
    },
    [send],
  );

  const startUpload = useCallback(
    (file: File) => {
      const activeDevice = deviceRef.current;
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
        sendUploadStart(pending, new Date(file.lastModified).toISOString());
        return;
      }
      const task: FileTask = {
        id: makeId(),
        dir: "upload",
        device: activeDevice,
        name: file.name,
        parentPath: currentPathRef.current,
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
      sendUploadStart(task, new Date(file.lastModified).toISOString());
    },
    [saveTasks, sendUploadStart, bump],
  );

  /* After (re)connect or auth_ok: resume interrupted downloads, kick queued
   * uploads, and refresh the listing. */
  const resumeActive = useCallback(() => {
    tasksRef.current!.forEach((task) => {
      if (task.dir === "download" && task.status === "running" && !task.saved) {
        resumeDownload(task, task.done || 0, tRef.current("files.resuming"));
      } else if (task.dir === "upload" && task.status === "queued") {
        sendUploadStart(task);
      }
    });
    list(currentPathRef.current);
  }, [resumeDownload, sendUploadStart, list]);

  /* ── WS message handlers ── */

  const onText = useCallback(
    (msg: Record<string, unknown>) => {
      if (msg.op === "auth_required") {
        /* The server rejects any op on an unauthorized socket with this;
         * surface the password overlay (it auto-submits a remembered
         * password once, like the terminal tabs). */
        closeAuth();
        setAuthError("");
        openAuth();
        return;
      }
      if (msg.op === "auth_ok") {
        const pw = pendingPwRef.current;
        if (pw) {
          rememberDevicePassword(deviceRef.current, pw);
          pendingPwRef.current = "";
        }
        closeAuth();
        resumeActive();
        return;
      }
      if (msg.op === "auth_fail") {
        forgetDevicePassword(deviceRef.current);
        pendingPwRef.current = "";
        setAuthError(
          typeof msg.message === "string" && msg.message
            ? msg.message
            : tRef.current("common.passwordWrong"),
        );
        openAuth();
        return;
      }
      if (msg.op === "list_result") {
        if (msg.error) {
          setEntries([]);
          setListView({
            notice: String(msg.error),
            truncated: false,
            shown: 0,
            total: 0,
          });
          return;
        }
        const resultPath =
          typeof msg.path === "string" && msg.path
            ? msg.path
            : currentPathRef.current;
        const offset = Number(msg.offset || 0);
        const newEntries = (
          Array.isArray(msg.entries) ? msg.entries : []
        ) as FileEntry[];
        const append = offset > 0 && resultPath === currentPathRef.current;
        currentPathRef.current = resultPath;
        parentPathRef.current =
          typeof msg.parent === "string" && msg.parent ? msg.parent : resultPath;
        setPathInput(resultPath);
        setEntries((prev) => (append ? prev.concat(newEntries) : newEntries));
        listOffsetRef.current = offset + newEntries.length;
        listTotalRef.current = Number(
          msg.total || msg.fileMode || listOffsetRef.current,
        );
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
        task.name =
          typeof msg.name === "string" && msg.name ? msg.name : task.name;
        task.status = "running";
        task.message = "";
        task.lastProgress = Date.now();
        if (Number(msg.offset || 0) === 0 && (task.done || 0) === 0)
          task.blobParts = [];
        saveTasks();
      } else if (msg.op === "transfer_end" && task) {
        completeDownload(task, Number(msg.offset || task.done || 0));
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
      } else if (msg.op === "error" && !task) {
        /* Socket-level error with no task (e.g. device not connected). */
        setListView((v) => ({
          ...v,
          notice:
            typeof msg.message === "string" && msg.message
              ? msg.message
              : tRef.current("common.error"),
        }));
        return;
      }
      bump();
    },
    [
      saveTasks,
      uploadLoop,
      completeDownload,
      resumeActive,
      openAuth,
      closeAuth,
      bump,
    ],
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

  /* Single /files socket per device with exponential-backoff reconnect.
   * Re-runs when the device prop changes: clears per-device state, drops the
   * old socket, reconnects, and lists the home directory. */
  useEffect(() => {
    tasksRef.current = loadPersistedTasks(device);
    filesByTaskRef.current.clear();
    currentPathRef.current = "";
    parentPathRef.current = "";
    listOffsetRef.current = 0;
    listTotalRef.current = 0;
    listTruncatedRef.current = false;
    sendQueueRef.current = [];
    reconnectDelayRef.current = 1000;
    pendingPwRef.current = "";
    closeAuth();
    setEntries([]);
    setPathInput("");
    setListView({
      notice: tRef.current("common.connecting"),
      truncated: false,
      shown: 0,
      total: 0,
    });
    bump();

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
        if (wsRef.current !== ws) return;
        reconnectDelayRef.current = 1000;
        const queue = sendQueueRef.current;
        while (queue.length && ws.readyState === WebSocket.OPEN)
          ws.send(queue.shift()!);
        /* With a remembered password, authenticate first and defer the
         * resume/list to auth_ok so resumed transfers don't bounce off
         * auth_required; otherwise optimistically resume and let the server
         * ask for a password if it needs one. */
        const savedPw = getDevicePassword(device);
        if (savedPw) {
          pendingPwRef.current = savedPw;
          ws.send(JSON.stringify({ op: "auth", deviceId: device, password: savedPw }));
        } else {
          resumeActive();
        }
      };
      ws.onmessage = (evt) => {
        if (wsRef.current !== ws) return;
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
  }, [device, onBinary, onText, resumeActive, closeAuth, bump]);

  /* Speed sampling + stalled-download watchdog + task re-render, every 500ms.
   * Paused while the tab is hidden; transfers themselves keep running. */
  useEffect(() => {
    if (!visible) return;
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
  }, [visible, resumeDownload, bump]);

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
            sendUploadStart(task);
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
    [resumeDownload, saveTasks, send, sendUploadStart, bump],
  );

  /* ── UI handlers ── */

  const onFilesPicked = (e: ChangeEvent<HTMLInputElement>) => {
    [...(e.target.files || [])].forEach(startUpload);
    e.target.value = "";
  };

  const onDrop = (e: DragEvent) => {
    e.preventDefault();
    setDragging(false);
    [...e.dataTransfer.files].forEach(startUpload);
  };

  const submitAuth = (password: string) => {
    if (!password) return;
    pendingPwRef.current = password;
    send({ op: "auth", deviceId: deviceRef.current, password });
  };

  /* ── Render ── */

  const sortedTasks = [...tasksRef.current!.values()].sort(
    (a, b) => b.created - a.created,
  );
  const showInfoRow =
    !listView.notice && (listView.truncated || listView.total > entries.length);

  return (
    <div className="relative flex h-full min-h-0 flex-col bg-background">
      {/* Toolbar */}
      <div className="flex shrink-0 items-center gap-1.5 border-b border-border-seam p-2">
        <Input
          spellCheck={false}
          autoComplete="off"
          value={pathInput}
          placeholder={t("files.path")}
          className="h-8 min-w-0 flex-1 text-xs"
          onChange={(e) => setPathInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") list(pathInput.trim());
          }}
        />
        <Button
          variant="outline"
          size="icon"
          className="size-8 shrink-0"
          onClick={() => list(parentPathRef.current || currentPathRef.current)}
        >
          <Icon name="ArrowUp" className="size-4" />
        </Button>
        <Button
          variant="outline"
          size="icon"
          className="size-8 shrink-0"
          onClick={() => list(currentPathRef.current)}
        >
          <Icon name="Refresh" className="size-4" />
        </Button>
        <Button
          variant="outline"
          size="icon"
          className="size-8 shrink-0"
          onClick={() => fileInputRef.current?.click()}
        >
          <Icon name="Upload" className="size-4" />
        </Button>
      </div>

      {/* Drop zone */}
      <div
        className={cn(
          "mx-2 mt-2 flex min-h-10 shrink-0 items-center justify-center gap-2 rounded-lg border-2 border-dashed border-border p-2 text-xs text-muted-foreground",
          dragging &&
            "border-timeline-accent bg-timeline-accent/5 text-timeline-accent",
        )}
        onDragOver={(e) => {
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={onDrop}
      >
        <Icon name="Upload" className="size-4" />
        {t("files.dropHint")}
      </div>
      <input
        ref={fileInputRef}
        type="file"
        multiple
        className="hidden"
        onChange={onFilesPicked}
      />

      {/* Entries + tasks (container queries gate the optional columns) */}
      <div className="@container mt-2 min-h-0 flex-1 overflow-y-auto px-2 pb-2">
        <div className="overflow-hidden rounded-lg border border-border">
          {/* Header */}
          <div className="flex items-center gap-2 border-b border-border bg-muted/50 px-2 py-1.5 text-xs font-medium text-muted-foreground">
            <span className="min-w-0 flex-1">{t("files.name")}</span>
            <span className="w-14 shrink-0 text-right @max-[320px]:hidden">
              {t("files.size")}
            </span>
            <span className="w-36 shrink-0 @max-[480px]:hidden">
              {t("files.modified")}
            </span>
            <span className="w-7 shrink-0" />
          </div>

          {listView.notice ? (
            <div className="px-2 py-8 text-center text-xs text-muted-foreground">
              {listView.notice}
            </div>
          ) : (
            <>
              {showInfoRow && (
                <div className="flex items-center gap-2 border-b border-border/50 bg-timeline-accent/5 px-2 py-1.5 text-xs text-muted-foreground">
                  <span className="min-w-0 flex-1">
                    {t("files.showing", {
                      shown: listView.shown || entries.length,
                      total: listView.total || entries.length,
                    })}
                  </span>
                  {listView.truncated && (
                    <Button
                      variant="outline"
                      size="sm"
                      className="h-6 shrink-0 px-2 text-xs"
                      onClick={() => list(currentPathRef.current, true)}
                    >
                      {t("files.loadMore")}
                    </Button>
                  )}
                </div>
              )}
              {entries.length === 0 ? (
                <div className="px-2 py-8 text-center text-xs text-muted-foreground">
                  {device ? t("files.emptyDir") : t("files.empty")}
                </div>
              ) : (
                entries.map((entry) => (
                  <div
                    key={entry.path}
                    className="flex items-center gap-2 border-b border-border/50 px-2 py-1 last:border-b-0 hover:bg-muted/30"
                  >
                    <button
                      type="button"
                      className={cn(
                        "flex min-w-0 flex-1 items-center gap-1.5 text-left text-sm text-foreground",
                        entry.isDir && "cursor-pointer hover:text-timeline-accent",
                      )}
                      onClick={() => {
                        if (entry.isDir) list(entry.path);
                      }}
                    >
                      <Icon
                        name={entry.isDir ? "Folder" : "File"}
                        className="size-4 shrink-0"
                      />
                      <span className="truncate">{entry.name}</span>
                    </button>
                    <span className="w-14 shrink-0 text-right text-xs text-muted-foreground @max-[320px]:hidden">
                      {entry.isDir ? "--" : fmtBytes(entry.size)}
                    </span>
                    <span className="w-36 shrink-0 truncate text-xs text-muted-foreground @max-[480px]:hidden">
                      {entry.modTime
                        ? new Date(entry.modTime).toLocaleString()
                        : ""}
                    </span>
                    {!entry.isDir ? (
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-7 shrink-0"
                        onClick={() =>
                          startDownload(entry.path, entry.name, entry.size)
                        }
                      >
                        <Icon name="Download" className="size-3.5" />
                      </Button>
                    ) : (
                      <span className="size-7 shrink-0" />
                    )}
                  </div>
                ))
              )}
            </>
          )}
        </div>

        {/* Tasks */}
        <div className="mt-2 rounded-lg border border-border p-2">
          <h2 className="mb-2 flex items-center gap-1.5 text-sm font-semibold">
            <Icon name="Files" className="size-4" />
            {t("files.tasks")}
          </h2>
          <div className="flex flex-col gap-2">
            {sortedTasks.length === 0 && (
              <div className="text-xs text-muted-foreground">
                {t("files.noTasks")}
              </div>
            )}
            {sortedTasks.map((task) => {
              const pct = task.size
                ? Math.min(
                    100,
                    Math.floor(((task.done || 0) * 1000) / task.size) / 10,
                  )
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
                <div
                  key={task.id}
                  className="flex flex-col gap-1.5 rounded-lg border border-border p-2"
                >
                  <div className="flex min-w-0 items-center gap-1.5">
                    <Badge className={statusBadgeClass(task.status)}>
                      {t("files.status." + task.status)}
                    </Badge>
                    <strong className="truncate text-xs">{task.name}</strong>
                    <span className="shrink-0 text-muted-foreground">
                      {task.dir === "upload" ? "↑" : "↓"}
                    </span>
                  </div>
                  <div className="h-1.5 overflow-hidden rounded-full border border-border bg-muted">
                    <div
                      className="h-full bg-timeline-accent transition-[width] duration-200"
                      style={{ width: pct + "%" }}
                    />
                  </div>
                  <div className="text-xs text-muted-foreground">
                    {stat}
                    {task.message ? " · " + task.message : ""}
                  </div>
                  <div className="flex flex-wrap gap-1">
                    <Button
                      variant="outline"
                      size="icon"
                      className="size-7"
                      disabled={task.status !== "running"}
                      onClick={() => taskAction(task, "pause")}
                    >
                      <Icon name="Pause" className="size-3.5" />
                    </Button>
                    <Button
                      variant="outline"
                      size="icon"
                      className="size-7"
                      disabled={
                        task.status === "running" ||
                        task.status === "completed" ||
                        task.status === "canceled"
                      }
                      onClick={() => taskAction(task, "resume")}
                    >
                      <Icon name="Play" className="size-3.5" />
                    </Button>
                    <Button
                      variant="outline"
                      size="icon"
                      className="size-7"
                      disabled={
                        task.status === "completed" ||
                        task.status === "canceled"
                      }
                      onClick={() => taskAction(task, "cancel")}
                    >
                      <Icon name="X" className="size-3.5" />
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      className="h-7 px-2 text-xs"
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
        </div>
      </div>

      {/* Device password prompt (auto-submits a remembered password once) */}
      {authOpen && (
        <TabAuthOverlay
          key={authRound}
          deviceId={device}
          error={authError}
          onSubmit={submitAuth}
          t={t}
        />
      )}
    </div>
  );
}
