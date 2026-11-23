const FALLBACK_DOWNLOAD_SLOTS = 2;
export const FALLBACK_BUSY_MESSAGE = "Only two in-memory downloads can be retained at once across Files panes. Finish or cancel another download, then retry; use disk-save support for larger transfers.";
interface FallbackTask {
  id: string;
  dir: string;
  status: string;
  blobParts?: ArrayBuffer[];
}
// Shared across every workspace and Agent panel in this browser application.
const admitted = new Set<object>();
export function createFallbackDownloads() {
  const retiring = new Map<string, { timer: ReturnType<typeof setTimeout>; dispose(): void }>();
  const owned = new Map<string, FallbackTask>();
  return {
    acquire(task: FallbackTask) {
      if (owned.get(task.id) === task) return;
      if (admitted.size >= FALLBACK_DOWNLOAD_SLOTS) throw new Error(FALLBACK_BUSY_MESSAGE);
      admitted.add(task);
      owned.set(task.id, task);
    },
    retire(task: FallbackTask, dispose: () => void) {
      if (owned.get(task.id) !== task) { dispose(); return; }
      task.blobParts = undefined;
      const timer = setTimeout(() => {
        retiring.delete(task.id);
        dispose();
        admitted.delete(task);
        owned.delete(task.id);
      }, 1000);
      retiring.set(task.id, { timer, dispose });
    },
    reconcile(tasks: Map<string, FallbackTask>) {
      for (const [id, task] of owned) {
        if (tasks.get(id) === task && task.status !== "failed" && task.status !== "canceled") continue;
        admitted.delete(task);
        owned.delete(id);
        const saved = retiring.get(id);
        if (saved) { clearTimeout(saved.timer); saved.dispose(); retiring.delete(id); }
        task.blobParts = undefined;
      }
    },
    clear() {
      for (const task of owned.values()) { admitted.delete(task); task.blobParts = undefined; }
      owned.clear();
      for (const saved of retiring.values()) { clearTimeout(saved.timer); saved.dispose(); }
      retiring.clear();
    },
  };
}
