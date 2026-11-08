interface FileReadRequest {
  epoch: number;
  id: string;
  timer: ReturnType<typeof setTimeout>;
}

/** Pending preview reads are scoped to both a path and a working context. */
export function createFileReadRequests(timeoutMs = 30_000) {
  const pending = new Map<string, FileReadRequest>();
  const remove = (path: string) => {
    const request = pending.get(path);
    if (request) clearTimeout(request.timer);
    pending.delete(path);
  };
  return {
    begin(path: string, epoch: number, id: string, onTimeout: () => void) {
      remove(path);
      const request: FileReadRequest = {
        epoch, id,
        timer: setTimeout(() => {
          if (pending.get(path) !== request) return;
          pending.delete(path);
          onTimeout();
        }, timeoutMs),
      };
      pending.set(path, request);
    },
    consume(path: string, epoch: number, id: string): boolean {
      const request = pending.get(path);
      // Legacy servers omit IDs; modern replies must match the exact attempt.
      if (!request || request.epoch !== epoch || (id && request.id !== id)) return false;
      remove(path);
      return true;
    },
    clear(): string[] {
      const paths = [...pending.keys()];
      for (const path of paths) remove(path);
      return paths;
    },
  };
}
