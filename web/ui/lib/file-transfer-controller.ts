/* Shared /files transfer session for the standalone Files page and the agent
 * file panel. Both surfaces speak the same wire protocol: resumable downloads
 * (blob or bounded disk ranges), uploads with per-chunk acknowledgements over
 * WebSocket or RTC, terminal integrity checks, and retry attempt bookkeeping.
 * This module owns that session; callers inject their socket, task store,
 * authorization, and the few per-surface bookkeeping callbacks. */

import { canResumeUpload } from "./file-request-state";
import {
  UploadPrefixMismatch,
  checkBlobDownload,
  downloadMetadata,
  hashSource,
  sourceFingerprint,
  validateUploadPrefix,
  type UploadSource,
} from "./file-integrity";
import type { FallbackDownloads } from "./file-download-fallback";
import { DOWNLOAD_RANGE, pickDownloadDestination, type DiskDownloads, type SaveHandle } from "./file-download-disk";
import { decodeFrame, encodeFrame, type FileTransferFrame } from "./file-transfer-frame";
import type { TransferAttempts, TransferAttemptPhase } from "./file-transfer-attempts";
import { FILE_RTC_CHUNK, FILE_RTC_WINDOW, type FileRTCTransfers } from "./file-webrtc";
import { uploadChunks } from "./upload-chunks";
import {
  CHUNK,
  FRAME_DOWNLOAD_CHUNK,
  FRAME_DOWNLOAD_END,
  FRAME_UPLOAD_ACK,
  FRAME_UPLOAD_CHUNK,
  makeId,
  type FileTask,
} from "./file-task-model";

export type Translate = (key: string, vars?: Record<string, string | number>) => string;

/** Mutable state both surfaces already keep in refs; the controller reads and
 * writes through them so page-side watchdogs and task actions stay in sync. */
export interface FileTransferRuntime {
  attempts: { current: TransferAttempts };
  rtc: { current: FileRTCTransfers };
  disk: { current: DiskDownloads };
  fallback: { current: FallbackDownloads };
  uploadRuns: { current: Map<string, AbortController> };
  directBinary: { current: (frame: ArrayBuffer) => void };
  filesByTask: { current: Map<string, UploadSource & { name: string }> };
  progressDirty: { current: boolean };
}

export interface FileTransferHost {
  send(message: Record<string, unknown>): boolean | void;
  /** The panel fails an upload whose terminal message cannot leave the socket;
   * the page queues it for the next connection. Returns whether it was sent. */
  sendUploadEnd(task: FileTask, message: Record<string, unknown>): boolean;
  /** The live socket, for raw binary frames and liveness checks. */
  socket(): WebSocket | null;
  tasks(): Map<string, FileTask>;
  saveTasks(): void;
  bump(): void;
  t(): Translate;
  /** Connection identity captured per attempt so reconnects isolate stale work. */
  connectionGuard(): () => boolean;
  /** Device authorization before the wire request (panel: already authorized). */
  authorize(task: FileTask): Promise<void>;
  /** Attempt phase/progress bookkeeping read by the page's watchdog. */
  onPhase?(task: FileTask, wireId: string, phase: TransferAttemptPhase): void;
  onProgress?(task: FileTask, wireId: string): void;
  /** Copy-source frames belong to the page's auxiliary range sessions. */
  claimBinary?(frame: FileTransferFrame): boolean;
  /** Terminal cleanup differs: the panel drops attempt bookkeeping. */
  dropAttempt?(task: FileTask): void;
  startFailed(task: FileTask, wireId: string, error?: unknown): void;
  diskChunkFailed(task: FileTask, wireId: string, error: unknown): void;
  uploadSettled(task: FileTask): void;
}

export interface FileTransferController {
  stopUpload(task: FileTask): void;
  startTransfer(task: FileTask, request: Record<string, unknown>): void;
  sendUploadStart(task: FileTask, modTime?: string): void;
  startUpload(file: UploadSource & { name: string }, destDir: string, activeDevice: string): void;
  startDownload(path: string, name: string, size: number, activeDevice: string): void;
  resumeDownload(task: FileTask, offset: number, message?: string): void;
  completeDownload(task: FileTask, offset: number, binary?: boolean): void;
  finishDownload(task: FileTask): void;
  uploadLoop(task: FileTask): Promise<void>;
  onTaskMessage(msg: Record<string, unknown>): boolean;
  onBinary(buf: ArrayBuffer, direct?: boolean): void;
}

function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

export function createFileTransferController(
  runtime: FileTransferRuntime,
  host: FileTransferHost,
): FileTransferController {
  const { attempts, rtc, disk, fallback, uploadRuns, directBinary, filesByTask, progressDirty } = runtime;
  const t = host.t;
  const tasks = host.tasks;

  const stopUpload = (task: FileTask) => {
    uploadRuns.current.get(task.id)?.abort();
    uploadRuns.current.delete(task.id);
    task.waitAck = null;
    task.uploadAcks?.clear();
  };

  const downloadRequest = (task: FileTask, offset: number) => ({
    op: "download_start",
    path: task.path,
    offset,
    ...(task.diskDownload ? { length: DOWNLOAD_RANGE } : {}),
  });

  /* Every (re)start gets a fresh wire id; the old attempt is canceled and its
   * upload run aborted before the new offer is negotiated. */
  const startTransfer = (task: FileTask, request: Record<string, unknown>): void => {
    if (task.dir === "download" && !task.diskDownload) {
      try { fallback.current.acquire(task); }
      catch (error) { task.status = "failed"; task.message = errorText(error); host.saveTasks(); host.bump(); return; }
    }
    disk.current.get(task.id)?.suspend();
    const previous = attempts.current.current(task.id);
    rtc.current.close(previous, false);
    uploadRuns.current.get(task.id)?.abort();
    task.verifying = false;
    task.terminal = false;
    task.binaryEnd = undefined;
    if (previous && host.socket()?.readyState === WebSocket.OPEN) host.send({ op: "cancel", taskId: previous, offset: task.done || 0 });
    const wireId = attempts.current.begin(task.id);
    const connected = host.connectionGuard();
    const current = () => connected() &&
      attempts.current.current(task.id) === wireId &&
      (task.status === "running" || task.status === "queued");
    const run = async () => {
      if (!current()) { host.startFailed(task, wireId); return; }
      await host.authorize(task);
      if (!current()) return;
      const controller = new AbortController();
      uploadRuns.current.set(task.id, controller);
      if (task.dir === "upload") {
        const source = filesByTask.current.get(task.id);
        if (!source) throw new Error(t()("files.reselect"));
        task.message = "Computing SHA-256…";
        task.sha256 = await sourceFingerprint(source, controller.signal);
        if (!current()) return;
        request = { ...request, sha256: task.sha256, size: source.size, restart: task.restart === true };
      } else request = { ...request, ...(task.sha256 ? { sha256: task.sha256 } : {}) };
      const offer = await rtc.current.prepare({
        taskId: task.id, wireId, direction: task.dir, isCurrent: current,
        boundedDownload: task.diskDownload,
        send: message => { host.send(message); },
        onBinary: frame => directBinary.current(frame),
        onDiagnostics: value => { if (current()) { task.diagnostics = { ...task.diagnostics, ...value }; progressDirty.current = true; } },
        onFallback: () => {
          if (!current()) return;
          const retry = async () => {
            const sink = disk.current.get(task.id);
            await sink?.drain();
            if (!current()) return;
            const offset = task.dir === "download" ? sink?.committed ?? (task.blobParts || []).reduce((sum, part) => sum + part.byteLength, 0) : task.done;
            task.done = offset;
            task.lastRequested = Date.now();
            startTransfer(task, { ...request, offset });
          };
          void retry().catch(error => { if (!current()) return; task.status = "failed"; task.message = errorText(error); disk.current.abort(task.id); host.saveTasks(); host.bump(); });
        },
      });
      if (!current()) { rtc.current.close(wireId, false); return; }
      const sink = disk.current.get(task.id);
      if (sink) {
        const offset = await sink.begin();
        if (!current()) return;
        request = { ...request, offset, length: DOWNLOAD_RANGE };
      }
      task.lastRequested = Date.now();
      task.lastProgress = task.lastRequested;
      host.send({ ...request, ...offer, deviceId: task.device, taskId: wireId });
      rtc.current.started(wireId);
    };
    void run().catch(error => { if (current()) host.startFailed(task, wireId, error); });
  };

  const sendUploadStart = (task: FileTask, modTime?: string) => {
    stopUpload(task);
    startTransfer(task, {
      op: "upload_start",
      parentPath: task.parentPath,
      name: task.name,
      size: task.size,
      ...(modTime ? { modTime } : {}),
    });
  };

  /* A bounded disk download opens its writer once; a failed open or write
   * settles the task and drops its attempt. */
  const openDiskDownload = (task: FileTask, destination: Promise<SaveHandle>) => {
    void disk.current.open(
      task.id,
      destination,
      () => tasks().get(task.id) === task && task.status === "running",
      offset => {
        task.done = offset;
        task.lastProgress = Date.now();
        host.onProgress?.(task, attempts.current.current(task.id) || "");
        progressDirty.current = true;
      },
      error => {
        const wire = attempts.current.current(task.id);
        rtc.current.close(wire, false);
        if (wire) host.send({ op: "cancel", taskId: wire, offset: task.done });
        attempts.current.remove(task.id);
        task.status = "failed"; task.message = errorText(error); disk.current.abort(task.id);
        host.saveTasks(); host.bump();
      },
    ).then(ok => {
      if (ok) startTransfer(task, downloadRequest(task, task.done));
    }).catch(error => {
      if (tasks().get(task.id) !== task || task.status !== "running") return;
      task.status = error instanceof DOMException && error.name === "AbortError" ? "canceled" : "failed";
      task.message = errorText(error);
      host.saveTasks(); host.bump();
    });
  };

  const resumeDownload = (task: FileTask, offset: number, message?: string) => {
    if (!task || task.dir !== "download" || task.saved) return;
    if (disk.current.isOpening(task.id)) return;
    const sink = disk.current.get(task.id);
    if (task.diskDownload && !sink) {
      const destination = pickDownloadDestination(task.name);
      if (!destination) { task.status = "failed"; task.message = t()("files.diskRestart"); host.saveTasks(); host.bump(); return; }
      task.status = "running";
      task.done = 0;
      openDiskDownload(task, destination);
      return;
    }
    let resumeOffset = sink?.committed ?? Number(offset || 0);
    const buffered = (task.blobParts || []).reduce(
      (sum, part) => sum + (part.byteLength || 0),
      0,
    );
    if (!sink && resumeOffset > 0 && buffered !== resumeOffset) {
      resumeOffset = 0;
      task.blobParts = [];
    }
    task.done = resumeOffset;
    task.status = "running";
    task.message = message || "";
    task.lastRequested = Date.now();
    host.saveTasks();
    startTransfer(task, downloadRequest(task, resumeOffset));
    host.bump();
  };

  const finishDownload = (task: FileTask) => {
    if (task.dir !== "download" || !task.blobParts || task.saved || task.status !== "completed") return;
    const blob = new Blob(task.blobParts);
    if (task.size && blob.size < task.size) {
      resumeDownload(task, blob.size, t()("files.resuming"));
      return;
    }
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = task.name || "download";
    document.body.appendChild(a);
    a.click();
    fallback.current.retire(task, () => { URL.revokeObjectURL(a.href); a.remove(); });
    task.saved = true;
    task.blobParts = undefined;
    attempts.current.remove(task.id);
    host.saveTasks();
  };

  const completeDownload = (task: FileTask, offset: number, binary = true) => {
    if (!task || task.dir !== "download" || task.saved || task.status !== "running") return;
    if (binary) task.binaryEnd = offset;
    if (!binary && task.binaryEnd !== offset) return;
    if (offset === task.size && (!task.terminal || task.binaryEnd !== offset)) return;
    if (task.diskDownload) {
      const sink = disk.current.get(task.id);
      if (!sink) return;
      const wireId = attempts.current.current(task.id);
      void sink.end(offset, task.size, () => { rtc.current.close(wireId); task.status = "saving"; task.speed = 0; host.saveTasks(); host.bump(); }).then(async result => {
        if ((task.status !== "running" && task.status !== "saving") || attempts.current.current(task.id) !== wireId) return;
        task.done = sink.committed;
        if (result === "next") {
          const next = await sink.begin();
          if (task.status !== "running" || attempts.current.current(task.id) !== wireId) return;
          task.lastRequested = Date.now();
          if (!rtc.current.continueDownload(wireId, next) && attempts.current.current(task.id) === wireId) resumeDownload(task, next, t()("files.resuming"));
        } else if (result === "complete") {
          task.status = "completed"; task.saved = true; task.message = ""; disk.current.abort(task.id);
          host.saveTasks(); host.bump();
        }
      }).catch(error => {
        if (tasks().get(task.id) !== task || attempts.current.current(task.id) !== wireId || (task.status !== "running" && task.status !== "saving")) return;
        task.status = "failed"; task.message = errorText(error); disk.current.abort(task.id);
        host.saveTasks(); host.bump();
      });
      return;
    }
    const finalOffset = Number(offset);
    if (finalOffset !== task.done || finalOffset > task.size) { task.status = "failed"; task.message = "Invalid download end"; host.saveTasks(); host.bump(); return; }
    if (finalOffset < task.size) { resumeDownload(task, task.done, t()("files.resuming")); return; }
    if (task.verifying) return;
    task.verifying = true;
    const wireId = attempts.current.current(task.id);
    host.onPhase?.(task, wireId || "", "verifying");
    const controller = new AbortController();
    uploadRuns.current.set(task.id, controller);
    void hashSource(new Blob(task.blobParts), controller.signal).then(hash => {
      if (controller.signal.aborted || task.status !== "running" || attempts.current.current(task.id) !== wireId) return;
      if (hash !== task.sha256) throw new Error("Download SHA-256 verification failed");
      task.status = "completed"; task.message = ""; finishDownload(task); host.saveTasks(); host.bump();
    }).catch(error => {
      if (controller.signal.aborted || attempts.current.current(task.id) !== wireId) return;
      task.status = "failed"; task.message = errorText(error); host.saveTasks(); host.bump();
    }).finally(() => {
      if (uploadRuns.current.get(task.id) === controller) { task.verifying = false; uploadRuns.current.delete(task.id); }
    });
  };

  const startDownload = (path: string, name: string, size: number, activeDevice: string) => {
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
    tasks().set(task.id, task);
    host.saveTasks();
    host.bump();
    const destination = pickDownloadDestination(name);
    if (destination) {
      task.diskDownload = true;
      openDiskDownload(task, destination);
    } else startTransfer(task, downloadRequest(task, 0));
  };

  const uploadLoop = async (task: FileTask) => {
    stopUpload(task);
    const wireId = attempts.current.current(task.id);
    if (!wireId) return;
    const controller = new AbortController();
    uploadRuns.current.set(task.id, controller);
    try {
      const file = filesByTask.current.get(task.id);
      if (!file) throw new Error(t()("files.reselect"));
      if (!task.ready) throw new Error("Missing upload integrity metadata");
      task.message = "Verifying upload prefix…";
      const offset = await validateUploadPrefix(file, task.sha256, task.ready, controller.signal);
      if (controller.signal.aborted || attempts.current.current(task.id) !== wireId || task.status !== "running") return;
      if (task.restart && offset !== 0) throw new Error("Device did not reset the corrupt partial");
      task.restart = false;
      task.done = offset;
      task.message = "";
      task.uploadAcks = new Map();
      const complete = await uploadChunks({
        file, chunkSize: rtc.current.direct(wireId) ? FILE_RTC_CHUNK : CHUNK,
        offset: () => task.done || 0, signal: controller.signal,
        windowBytes: rtc.current.direct(wireId) ? FILE_RTC_WINDOW : CHUNK,
        onSent: offset => { task.sent = offset; },
        write: async (chunk, offset) => {
          const ws = host.socket();
          if (!ws || ws.readyState !== WebSocket.OPEN) throw new Error(t()("files.disconnected"));
          const ack = Promise.withResolvers<void>();
          task.uploadAcks!.set(offset + chunk.byteLength, ack.resolve);
          if (rtc.current.direct(wireId)) await rtc.current.send(wireId, offset, chunk, controller.signal);
          else ws.send(encodeFrame(FRAME_UPLOAD_CHUNK, wireId, offset, chunk));
          return ack.promise;
        },
      });
      if (complete && !controller.signal.aborted && attempts.current.current(task.id) === wireId && task.status === "running") {
        if (!host.sendUploadEnd(task, { op: "upload_end", taskId: wireId, path: task.path, size: file.size })) {
          host.startFailed(task, wireId);
        }
      }
    } catch (error) {
      if (controller.signal.aborted || attempts.current.current(task.id) !== wireId || task.status !== "running") return;
      if (error instanceof UploadPrefixMismatch) task.restart = true;
      if (!task.restart && rtc.current.direct(wireId)) { rtc.current.fail(wireId); return; }
      if (attempts.current.current(task.id) !== wireId) return;
      task.status = "failed";
      rtc.current.close(wireId, false);
      host.send({ op: "cancel", taskId: wireId });
      task.message = errorText(error);
      host.saveTasks();
      host.bump();
    } finally {
      if (uploadRuns.current.get(task.id) === controller) {
        uploadRuns.current.delete(task.id);
        task.waitAck = null;
      }
    }
  };

  const startUpload = (file: UploadSource & { name: string }, destDir: string, activeDevice: string) => {
    if (!activeDevice || !file) return;
    const modTime = file.lastModified !== undefined ? new Date(file.lastModified).toISOString() : undefined;
    const pending = [...tasks().values()].find(
      (task) =>
        canResumeUpload(task, { device: activeDevice, path: destDir, name: file.name, size: file.size }) &&
        !filesByTask.current.has(task.id),
    );
    if (pending) {
      filesByTask.current.set(pending.id, file);
      pending.status = "running";
      pending.message = "";
      host.saveTasks();
      host.bump();
      sendUploadStart(pending, modTime);
      return;
    }
    const task: FileTask = {
      id: makeId(),
      dir: "upload",
      device: activeDevice,
      name: file.name,
      parentPath: destDir,
      size: file.size,
      done: 0,
      status: "queued",
      created: Date.now(),
      speed: 0,
    };
    tasks().set(task.id, task);
    filesByTask.current.set(task.id, file);
    host.saveTasks();
    host.bump();
    sendUploadStart(task, modTime);
  };

  /* Task-scoped text ops; listing/auth/preview ops stay with each surface. */
  const onTaskMessage = (msg: Record<string, unknown>): boolean => {
    const taskId = typeof msg.taskId === "string" ? msg.taskId : "";
    const task = tasks().get(attempts.current.resolve(taskId) || "");
    if (msg.op === "upload_ready" && task) {
      if (task.status !== "running" && task.status !== "queued") return true;
      task.path = typeof msg.path === "string" ? msg.path : task.path;
      if (task.ready?.taskId === taskId) return true;
      task.ready = msg;
      task.status = "running";
      host.onPhase?.(task, taskId, "receiving");
      host.saveTasks();
      void uploadLoop(task);
      return true;
    }
    if (msg.op === "download_start" && task) {
      if (task.dir !== "download" || task.status !== "running" || task.saved) return true;
      try {
        const metadata = downloadMetadata(msg, task.sha256);
        if (!task.diskDownload) checkBlobDownload(metadata.size, task.done);
        task.size = metadata.size; task.sha256 = metadata.sha256;
        disk.current.get(task.id)?.setIdentity(metadata.sha256, metadata.size);
      } catch (error) {
        task.status = "failed"; task.message = errorText(error); task.blobParts = undefined;
        rtc.current.close(taskId, false); host.send({ op: "cancel", taskId }); disk.current.abort(task.id);
        host.saveTasks(); host.bump(); return true;
      }
      task.name =
        typeof msg.name === "string" && msg.name ? msg.name : task.name;
      task.status = "running";
      task.message = "";
      host.onPhase?.(task, taskId, "receiving");
      task.lastProgress = Date.now();
      if (Number(msg.offset || 0) === 0 && (task.done || 0) === 0)
        task.blobParts = [];
      host.saveTasks();
      return true;
    }
    if (msg.op === "transfer_end" && task) {
      if (task.status !== "running") return true;
      if (!task.sha256 || msg.success !== true || msg.sha256 !== task.sha256) {
        stopUpload(task); task.status = "failed"; task.message = "Transfer terminal integrity verification failed";
        disk.current.abort(task.id); host.saveTasks(); host.bump(); return true;
      }
      task.terminal = true;
      if (task.dir === "download") completeDownload(task, Number(msg.offset ?? task.done), false);
      else {
        stopUpload(task);
        host.dropAttempt?.(task);
        task.status = "completed";
        task.done = task.size;
        task.message = "";
        filesByTask.current.delete(task.id);
        host.saveTasks();
        host.uploadSettled(task);
      }
      return true;
    }
    if ((msg.op === "transfer_error" || msg.op === "error") && task) {
      if (task.status !== "running" && task.status !== "queued") return true;
      stopUpload(task);
      host.dropAttempt?.(task);
      task.status = "failed";
      disk.current.abort(task.id);
      task.message =
        (typeof msg.message === "string" && msg.message) ||
        (typeof msg.error === "string" && msg.error) ||
        t()("common.error");
      host.saveTasks();
      return true;
    }
    return false;
  };

  const onBinary = (buf: ArrayBuffer, direct = false) => {
    const frame = decodeFrame(buf);
    if (!frame) return;
    if (host.claimBinary?.(frame)) return;
    if (!direct && rtc.current.direct(frame.taskId)) return;
    const task = tasks().get(attempts.current.resolve(frame.taskId) || "");
    if (!task) return;
    if (frame.typ === FRAME_UPLOAD_ACK) {
      if (task.dir !== "upload" || task.status !== "running") return;
      if (!task.uploadAcks?.has(frame.offset) || frame.offset <= task.done || frame.payload.byteLength) {
        if (direct) rtc.current.fail(frame.taskId);
        else { stopUpload(task); task.status = "failed"; task.message = "Invalid upload acknowledgement"; host.saveTasks(); host.bump(); }
        return;
      }
      task.done = frame.offset;
      progressDirty.current = true;
      for (const [end, resolve] of task.uploadAcks) {
        if (end > frame.offset) break;
        task.uploadAcks.delete(end);
        resolve();
      }
    } else if (frame.typ === FRAME_DOWNLOAD_CHUNK) {
      if (task.dir !== "download" || task.status !== "running" || task.saved) return;
      if (!task.sha256) { task.status = "failed"; task.message = "Download bytes arrived before integrity metadata"; host.saveTasks(); host.bump(); return; }
      if (task.diskDownload) {
        const sink = disk.current.get(task.id);
        if (!sink) return;
        try { sink.receive(frame.offset, frame.payload); }
        catch (error) { host.diskChunkFailed(task, frame.taskId, error); }
        return;
      }
      if (frame.offset === (task.done || 0)) {
        try { checkBlobDownload(task.size, frame.offset + frame.payload.byteLength); } catch (error) {
          task.status = "failed"; task.message = errorText(error); task.blobParts = undefined;
          rtc.current.close(frame.taskId, false); host.send({ op: "cancel", taskId: frame.taskId });
          host.saveTasks(); host.bump(); return;
        }
        task.blobParts = task.blobParts || [];
        task.blobParts.push(frame.payload);
        task.done = frame.offset + frame.payload.byteLength;
        task.lastProgress = Date.now();
        host.onProgress?.(task, frame.taskId);
        task.message = "";
        progressDirty.current = true;
      } else if (frame.offset < (task.done || 0)) {
        // Duplicate chunk from a resumed request; ignore it.
      } else if (direct) {
        rtc.current.fail(frame.taskId);
      } else if (Date.now() - (task.lastRequested || 0) > 1000) {
        resumeDownload(task, task.done || 0, t()("files.resuming"));
      }
    } else if (frame.typ === FRAME_DOWNLOAD_END) {
      if (task.dir !== "download" || task.status !== "running" || task.saved) return;
      if (task.diskDownload) { completeDownload(task, frame.offset); return; }
      if (direct && (frame.offset !== task.done || (task.size > 0 && frame.offset !== task.size))) { rtc.current.fail(frame.taskId); return; }
      completeDownload(task, frame.offset || task.done || 0);
      host.bump();
    }
  };

  return {
    stopUpload,
    startTransfer,
    sendUploadStart,
    startUpload,
    startDownload,
    resumeDownload,
    completeDownload,
    finishDownload,
    uploadLoop,
    onTaskMessage,
    onBinary,
  };
}
