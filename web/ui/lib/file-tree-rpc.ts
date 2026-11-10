import { readListingPage } from "./file-request-state";
export interface TreeEntry {
  path: string;
  name: string;
  isDir?: boolean;
  size?: number;
}
/** A separate request namespace prevents tree expansion from navigating the root. */
export function createFileTreeRPC(
  send: (message: Record<string, unknown>) => boolean,
) {
  const pending = new Map<
    string,
    {
      device: string;
      op: string;
      path: string;
      rows: TreeEntry[];
      resolve: (value: TreeEntry[]) => void;
      reject: (error: Error) => void;
      timer: ReturnType<typeof setTimeout>;
    }
  >();
  let sequence = 0;
  const cancel = () => {
    for (const item of pending.values()) {
      clearTimeout(item.timer);
      item.reject(new Error("Connection closed"));
    }
    pending.clear();
  };
  const request = (device: string, op: string, args: Record<string, unknown>) =>
    new Promise<TreeEntry[]>((resolve, reject) => {
      const requestId = `tree-${Date.now()}-${++sequence}`;
      const timer = setTimeout(() => {
        pending.delete(requestId);
        reject(new Error("File request timed out"));
      }, 30000);
      pending.set(requestId, {
        device,
        op,
        path: String(args.path ?? ""),
        rows: [],
        resolve,
        reject,
        timer,
      });
      if (
        !send({
          op,
          deviceId: device,
          requestId,
          ...args,
          ...(op === "list" ? { offset: 0, limit: 200 } : {}),
        })
      ) {
        clearTimeout(timer);
        pending.delete(requestId);
        reject(new Error("Device is not connected"));
      }
    });
  const accept = (msg: Record<string, unknown>) => {
    const id = String(msg.requestId ?? "");
    const item = pending.get(id);
    if (!item) return false;
    if (msg.deviceId && msg.deviceId !== item.device) return true;
    const finish = (error?: string) => {
      clearTimeout(item.timer);
      pending.delete(id);
      if (error) item.reject(new Error(error));
      else item.resolve(item.rows);
    };
    if (msg.op === "error" || msg.op === "auth_required" || msg.error) {
      finish(String(msg.error || msg.message || "File request failed"));
      return true;
    }
    if (msg.op !== `${item.op}_result`) return true;
    if (item.op !== "list") {
      finish(
        msg.success === true
          ? undefined
          : String(msg.message || "File operation failed"),
      );
      return true;
    }
    const rows = Array.isArray(msg.entries) ? (msg.entries as TreeEntry[]) : [];
    const page = readListingPage(msg, item.rows.length, rows.length);
    if (!page) {
      finish("Invalid directory pagination");
      return true;
    }
    item.rows.push(...rows);
    if (page.more) {
      if (item.rows.length >= 10000) {
        finish(
          "Directory exceeds 10000 entries; open it directly to browse pages",
        );
        return true;
      }
      if (
        !send({
          op: "list",
          deviceId: item.device,
          requestId: id,
          path: item.path,
          offset: page.nextOffset,
          limit: 200,
        })
      )
        finish("Device is not connected");
    } else finish();
    return true;
  };
  return { request, accept, cancel };
}

export type FileTreeOperation = 'mkdir' | 'rename' | 'delete' | 'newFile';
/** Shared wire contract for standalone and Agent file trees. */
export function fileTreeMutation(operation: FileTreeOperation, entry: TreeEntry, name: string) {
  const separator = entry.path.includes('\\') ? '\\' : '/';
  const child = entry.path.replace(/[\\/]$/, '') + separator + name;
  switch (operation) {
    case 'newFile': return { op: 'create', args: { path: child } };
    case 'mkdir': return { op: 'mkdir', args: { path: child } };
    case 'rename': return { op: 'rename', args: { path: entry.path, from: entry.path, to: entry.path.replace(/[^\\/]+$/, '') + name } };
    case 'delete': return { op: 'delete', args: { path: entry.path, recursive: !!entry.isDir } };
  }
}
