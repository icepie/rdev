import { checkAbort, downloadMetadata, type UploadSource } from "./file-integrity";
import type { FileTransferFrame } from "./file-transfer-frame";
/** One requested range per source; the upload window is the only producer. */
export function createRemoteFileSources(send: (message: Record<string, unknown>) => void, makeId: () => string) {
  const pending = new Map<string, { text(message: Record<string, unknown>): void; binary(frame: FileTransferFrame): void; fail(error: Error): void }>();
  async function range(deviceId: string, path: string, start: number, end: number, sha256: string | undefined, signal: AbortSignal) {
    if (end - start > 512 * 1024) throw new Error("Copy range exceeds memory bound");
    checkAbort(signal);
    const taskId = makeId();
    const result = Promise.withResolvers<{ size: number; sha256: string; data: ArrayBuffer }>();
    const data = new Uint8Array(end - start);
    let received = start;
    let metadata: { size: number; sha256: string } | undefined;
    let ended = false;
    let confirmed = false;
    const complete = () => {
      if (ended && confirmed && metadata) result.resolve({ ...metadata, data: received - start === data.length ? data.buffer : data.buffer.slice(0, received - start) });
    };
    const fail = (error: Error) => result.reject(error);
    const abort = () => fail(new DOMException("Copy canceled", "AbortError"));
    const timer = setTimeout(() => fail(new Error("Copy source range timed out")), 30000);
    pending.set(taskId, {
      fail,
      text(message) {
        try {
          if (message.op === "download_start") {
            metadata = downloadMetadata(message, metadata?.sha256 ?? sha256);
            if (Number(message.offset ?? 0) !== start) throw new Error("Invalid copy range start");
          } else if (message.op === "transfer_error" || message.op === "error" || message.op === "canceled") fail(new Error(String(message.message || message.error || "Copy source unavailable")));
          else if (message.op === "transfer_end") {
            if (!metadata || message.success !== true || message.sha256 !== metadata.sha256 || Number(message.offset) !== Math.min(end, metadata.size)) throw new Error("Copy terminal integrity verification failed");
            confirmed = true;
            complete();
          }
        } catch (error) { fail(error as Error); }
      },
      binary(frame) {
        try {
          if (!metadata) throw new Error("Copy bytes arrived before integrity metadata");
          if (frame.typ === 0x22) {
            if (frame.offset !== received || !frame.payload.byteLength || received + frame.payload.byteLength > Math.min(end, metadata.size)) throw new Error("Copy source exceeded requested range");
            data.set(new Uint8Array(frame.payload), received - start);
            received += frame.payload.byteLength;
          } else if (frame.typ === 0x23) {
            if (frame.offset !== received || received !== Math.min(end, metadata.size)) throw new Error("Incomplete copy range");
            ended = true;
            complete();
          }
        } catch (error) { fail(error as Error); }
      },
    });
    signal.addEventListener("abort", abort, { once: true });
    try {
      send({ op: "download_start", taskId, deviceId, path, offset: start, length: end - start, ...(sha256 ? { sha256 } : {}) });
      return await result.promise;
    } finally {
      clearTimeout(timer); signal.removeEventListener("abort", abort); pending.delete(taskId);
      try { send({ op: "cancel", taskId, offset: received }); } catch { /* Disconnected source already rejected the range. */ }
    }
  }
  return {
    async open(device: string, path: string, name: string, signal: AbortSignal): Promise<UploadSource & { name: string }> {
      const metadata = await range(device, path, 0, 1, undefined, signal);
      return { size: metadata.size, name, sha256: metadata.sha256,
        async read(start, end, readSignal) {
          const reply = await range(device, path, start, end, metadata.sha256, readSignal);
          if (reply.size !== metadata.size) throw new Error("Copy source size changed");
          return reply.data;
        },
      };
    },
    text(message: Record<string, unknown>) { const entry = pending.get(String(message.taskId || "")); entry?.text(message); return !!entry; },
    binary(frame: FileTransferFrame) { const entry = pending.get(frame.taskId); entry?.binary(frame); return !!entry; },
    clear() { for (const entry of pending.values()) entry.fail(new Error("Copy source disconnected")); pending.clear(); },
  };
}
