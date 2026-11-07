export type FileTaskStatus = "queued" | "running" | "paused" | "completed" | "failed" | "canceled";

/** An active request must be canceled before its task can be removed. */
export function availableFileTaskActions(status: FileTaskStatus) {
  return {
    pause: status === "running",
    resume: status === "paused" || status === "failed",
    cancel: status === "queued" || status === "running" || status === "paused",
    clear: status === "paused" || status === "failed" || status === "completed" || status === "canceled",
  };
}

/** Keep a failed upload tied to its original remote destination. */
export function canResumeUpload(
  task: {
    dir: string;
    status: string;
    device: string;
    parentPath?: string;
    name: string;
    size: number;
  },
  destination: { device: string; path: string; name: string; size: number },
): boolean {
  return (
    task.dir === "upload" &&
    task.status === "failed" &&
    task.device === destination.device &&
    task.parentPath === destination.path &&
    task.name === destination.name &&
    task.size === destination.size
  );
}

/** A visible directory has one owner request; auxiliary walks have their own IDs. */
export function createListingGuard() {
  let latest = "";
  let offset = 0;
  return {
    begin(id: string, requestedOffset = 0) {
      latest = id;
      offset = requestedOffset;
    },
    invalidate() {
      latest = "";
    },
    accepts(id: string) {
      return id !== "" && id === latest;
    },
    consume(id: string) {
      if (!id || id !== latest) return false;
      latest = "";
      return true;
    },
    offset() { return offset; },
  };
}

/** Legacy clients ignore offsets; never append their first page repeatedly. */
export function readListingPage(
  reply: { offset?: unknown; total?: unknown; fileMode?: unknown; truncated?: unknown },
  expectedOffset: number,
  count: number,
): { nextOffset: number; total: number; more: boolean } | null {
  const offset = Number(reply.offset ?? 0);
  const nextOffset = offset + count;
  const total = Number(reply.total || reply.fileMode || nextOffset);
  if (!Number.isSafeInteger(offset) || offset !== expectedOffset || offset < 0 ||
      !Number.isSafeInteger(total) || total < 0 || !Number.isSafeInteger(nextOffset)) return null;
  const more = !!reply.truncated || nextOffset < total;
  if (more && count === 0) return null;
  return { nextOffset, total, more };
}

/** A page reload loses local File objects, so active uploads need reselection. */
export function restoreFileTask<
  T extends { dir: string; status: string; message?: string },
>(task: T, reselectMessage: string): T {
  return task.dir === "upload" &&
    (task.status === "running" || task.status === "queued")
    ? { ...task, status: "failed", message: reselectMessage }
    : task;
}

/** A task-specific file picker must never use the currently browsed device. */
export function uploadResumeRequest(
  task: {
    id: string;
    device: string;
    parentPath?: string;
    name: string;
    size: number;
  },
  file: { name: string; size: number; lastModified: number },
): Record<string, unknown> | null {
  if (task.name !== file.name || task.size !== file.size) return null;
  return {
    op: "upload_start",
    deviceId: task.device,
    taskId: task.id,
    parentPath: task.parentPath,
    name: task.name,
    size: task.size,
    modTime: new Date(file.lastModified).toISOString(),
  };
}
