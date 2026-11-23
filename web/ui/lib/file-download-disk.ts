import { FileSHA256, HASH_CHUNK, validSHA256 } from "./file-integrity";
export const DOWNLOAD_RANGE = 4 * 1024 * 1024;

export interface DownloadWriter {
  write(data: { type: "write"; position: number; data: ArrayBuffer }): Promise<void>;
  close(): Promise<void>;
  abort(): Promise<void>;
}
export interface SaveHandle { createWritable(): Promise<DownloadWriter> }
type SavePicker = (options: { suggestedName: string }) => Promise<SaveHandle>;
export function pickDownloadDestination(name: string): Promise<SaveHandle> | undefined {
  const picker = (globalThis as typeof globalThis & { showSaveFilePicker?: SavePicker }).showSaveFilePicker;
  // Invoke synchronously in the click handler, before any auth/network await.
  try { return picker?.call(globalThis, { suggestedName: name }); }
  catch (error) { return Promise.reject(error); }
}

/** A single bounded range, not a per-frame promise chain. The committed cursor
 * advances only after disk writes; new ranges wait for the old worker to drain. */
export class DiskDownload {
  committed = 0;
  received = 0;
  private limit = 0;
  private queue: { offset: number; data: ArrayBuffer }[] = [];
  private worker?: Promise<void>;
  private error?: unknown;
  private stopped = false;
  private ending = false;
  private terminal?: Promise<void>;
  private generation = 0;
  private hash = new FileSHA256();
  private identity?: string;
  private size?: number;
  setIdentity(sha256: string, size: number) {
    if (!validSHA256(sha256) || (this.identity && this.identity !== sha256) || (this.size !== undefined && this.size !== size)) throw new Error("Download source changed");
    this.identity = sha256;
    this.size = size;
  }
  private closing = false;
  get busy() { return !!this.worker || this.ending; }
  suspend() { if (!this.closing) this.generation++; }
  constructor(private writer: DownloadWriter, private progress: (offset: number) => void, private failure: (error: unknown) => void = () => {}) {}
  async begin() {
    await this.drain();
    await this.terminal;
    if (this.stopped) throw new Error("Download destination closed; choose a new destination");
    this.received = this.committed;
    this.limit = this.committed + DOWNLOAD_RANGE;
    this.ending = false;
    return this.committed;
  }
  receive(offset: number, data: ArrayBuffer) {
    if (this.error || this.stopped || this.ending || offset !== this.received || !data.byteLength || offset + data.byteLength > this.limit || (this.size !== undefined && offset + data.byteLength > this.size))
      throw new Error("Device does not support bounded download ranges; update the device and retry");
    this.received += data.byteLength;
    this.queue.push({ offset, data });
    if (!this.worker) {
      this.worker = this.flush();
      void this.worker.catch(() => {});
    }
  }
  private async flush() {
    try {
      while (this.queue.length && !this.stopped) {
        const chunk = this.queue.shift()!;
        await this.writer.write({ type: "write", position: chunk.offset, data: chunk.data });
        const bytes = new Uint8Array(chunk.data);
        for (let offset = 0; offset < bytes.length; offset += HASH_CHUNK) {
          this.hash.update(bytes.subarray(offset, offset + HASH_CHUNK));
          if (offset + HASH_CHUNK < bytes.length) {
            const yielded = Promise.withResolvers<void>();
            setTimeout(yielded.resolve, 0);
            await yielded.promise;
          }
        }
        this.committed = chunk.offset + chunk.data.byteLength;
        this.progress(this.committed);
      }
    } catch (error) {
      this.failure(error);
      this.error = error;
      this.queue = [];
      throw error;
    } finally { this.worker = undefined; }
  }
  async drain() {
    await this.worker;
    if (this.error) throw this.error;
  }
  async end(offset: number, size: number, onSaving?: () => void) {
    if (this.ending) return "duplicate" as const;
    const generation = this.generation;
    this.ending = true;
    if (offset !== this.received || offset > size || (offset < size && offset !== this.limit))
      throw new Error("Invalid bounded download end; update the device and retry");
    await this.drain();
    if (this.stopped || generation !== this.generation) return "duplicate" as const;
    if (this.committed < size) return "next" as const;
    if (this.identity && this.hash.digest() !== this.identity) throw new Error("Download SHA-256 verification failed");
    this.closing = true;
    onSaving?.();
    try {
      this.terminal = this.writer.close();
      await this.terminal;
    } catch (error) {
      this.closing = false;
      this.error = error;
      this.failure(error);
      throw error;
    }
    this.stopped = true;
    return "complete" as const;
  }
  async abort() {
    if (this.stopped || this.closing) return;
    this.stopped = true;
    this.queue = [];
    try { await this.worker; } catch { /* Preserve the original write error. */ }
    try { await this.writer.abort(); } catch { /* Already closed or aborted. */ }
  }
}

export interface DiskDownloads {
  get(id: string): DiskDownload | undefined;
  isOpening(id: string): boolean;
  open(id: string, destination: Promise<SaveHandle>, current: () => boolean, progress: (offset: number) => void, failure?: (error: unknown) => void): Promise<boolean>;
  abort(id: string): void;
  clear(): void;
}

export function createDiskDownloads(): DiskDownloads {
  const sinks = new Map<string, DiskDownload>();
  const opening = new Set<string>();
  return {
    get: (id: string) => sinks.get(id),
    isOpening: (id: string) => opening.has(id),
    async open(id: string, destination: Promise<SaveHandle>, current: () => boolean, progress: (offset: number) => void, failure?: (error: unknown) => void) {
      opening.add(id);
      try {
        const handle = await destination;
        if (!current()) return false;
        const writer = await handle.createWritable();
        if (!current()) { await writer.abort(); return false; }
        sinks.set(id, new DiskDownload(writer, progress, failure));
        return true;
      } finally { opening.delete(id); }
    },
    abort(id: string) { const sink = sinks.get(id); sinks.delete(id); if (sink) void sink.abort(); },
    clear() { for (const sink of sinks.values()) void sink.abort(); sinks.clear(); },
  };
}
