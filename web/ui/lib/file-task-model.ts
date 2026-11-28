/* Shared transfer-task model for the standalone Files page and the agent
 * file panel: wire constants, task/list view types, and the small helpers both
 * file managers render from. Storage keys stay per-surface (the page uses
 * `rdevFileTasks`, the panel `rdevFileTasks.<device>`) and are passed in. */

import type { FileRTCDiagnostics } from "./file-webrtc";
import { restoreFileTask, type FileTaskStatus } from "./file-request-state";

/* ── Protocol constants ── */

export const CHUNK = 512 * 1024;
export const FILE_LIST_LIMIT = 100;
export const DOWNLOAD_RETRY_MS = 2500;

export const FRAME_UPLOAD_CHUNK = 0x20;
export const FRAME_UPLOAD_ACK = 0x21;
export const FRAME_DOWNLOAD_CHUNK = 0x22;
export const FRAME_DOWNLOAD_END = 0x23;

/* ── Types ── */

export interface FileEntry {
  name: string;
  path: string;
  isDir: boolean;
  size: number;
  modTime: string;
}

export type TaskStatus = FileTaskStatus;

export interface FileTask {
  id: string;
  dir: "upload" | "download";
  device: string;
  name: string;
  path?: string;
  parentPath?: string;
  size: number;
  sha256?: string;
  restart?: boolean;
  ready?: Record<string, unknown>;
  terminal?: boolean;
  binaryEnd?: number;
  verifying?: boolean;
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
  uploadAcks?: Map<number, () => void>;
  sent?: number;
  diagnostics?: FileRTCDiagnostics;
  diskDownload?: boolean;
}

export interface ListView {
  notice?: string;
  pageError?: string;
  truncated: boolean;
  shown: number;
  total: number;
}

/* ── Helpers ── */

export function makeId() {
  return "task-" + Date.now().toString(36) + "-" + Math.random().toString(36).slice(2);
}

export function fmtBytes(n: number) {
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

export function updateSpeed(task: FileTask) {
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

export function loadPersistedTasks(storageKey: string, reselectMessage: string): Map<string, FileTask> {
  try {
    const raw = JSON.parse(localStorage.getItem(storageKey) || "[]") as FileTask[];
    return new Map(raw.map((task) => [task.id, restoreFileTask(task, reselectMessage)]));
  } catch {
    return new Map();
  }
}

export function persistTasks(storageKey: string, tasks: Map<string, FileTask>) {
  localStorage.setItem(
    storageKey,
    JSON.stringify(
      [...tasks.values()].map(
        ({ blobParts, samples, waitAck, uploadAcks, ready, verifying, ...rest }) => {
          void blobParts;
          void samples;
          void waitAck;
          void uploadAcks;
          void ready;
          void verifying;
          return rest;
        },
      ),
    ),
  );
}

export function statusBadgeClass(s: TaskStatus) {
  switch (s) {
    case "running":
      return "border-transparent bg-timeline-accent/15 text-timeline-accent";
    case "completed":
      return "border-transparent bg-success/15 text-success-foreground";
    case "failed":
      return "border-transparent bg-destructive/15 text-destructive-text";
    case "paused":
      return "border-transparent bg-attention/15 text-warning-text";
    default:
      return "border-transparent bg-muted text-muted-foreground";
  }
}
