import { readSource, type UploadSource } from "./file-integrity";
/** Keep reads and unacknowledged writes bounded. Relay callers use the default
 * single chunk; RTC uses a fixed 512KiB ACK window independently of channel
 * backpressure, and validates wire ACK boundaries before resolving writes. */
export async function uploadChunks({ file, chunkSize, offset, signal, write, windowBytes = chunkSize, onSent }: {
  file: UploadSource;
  chunkSize: number;
  offset: () => number;
  signal: AbortSignal;
  write: (chunk: ArrayBuffer, offset: number) => Promise<void>;
  windowBytes?: number;
  onSent?: (offset: number) => void;
}): Promise<boolean> {
  let sent = offset();
  if (!Number.isSafeInteger(sent) || sent < 0 || sent > file.size || !Number.isSafeInteger(chunkSize) || chunkSize <= 0 || windowBytes < chunkSize)
    throw new Error("Invalid upload resume offset or window");
  const pending: { end: number; result: Promise<void> }[] = [];
  while (sent < file.size || pending.length) {
    if (signal.aborted) return false;
    if (sent < file.size && pending.length < Math.floor(windowBytes / chunkSize)) {
      const start = sent;
      let chunk: ArrayBuffer;
      try { chunk = await readSource(file, start, Math.min(file.size, start + chunkSize), signal); }
      catch (error) { if (signal.aborted) return false; throw error; }
      if (signal.aborted) return false;
      sent += chunk.byteLength;
      onSent?.(sent);
      const result = write(chunk, start);
      void result.catch(() => {});
      pending.push({ end: sent, result });
    } else {
      const first = pending.shift()!;
      const canceled = Promise.withResolvers<void>();
      const abort = () => canceled.resolve();
      signal.addEventListener("abort", abort, { once: true });
      if (signal.aborted) abort();
      const timer = setTimeout(() => canceled.reject(new Error("Upload acknowledgement timed out")), 30000);
      try {
        await Promise.race([first.result, canceled.promise]);
      } finally {
        clearTimeout(timer);
        signal.removeEventListener("abort", abort);
      }
      if (signal.aborted) return false;
      if (offset() < first.end || offset() > sent) throw new Error("Invalid upload acknowledgement offset");
    }
  }
  return !signal.aborted;
}
