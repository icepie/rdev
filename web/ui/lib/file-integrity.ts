export interface UploadSource {
  readonly size: number;
  readonly name?: string;
  readonly lastModified?: number;
  readonly sha256?: string;
  slice?(start: number, end: number): { arrayBuffer(): Promise<ArrayBuffer> };
  read?(start: number, end: number, signal: AbortSignal): Promise<ArrayBuffer>;
}
export const HASH_CHUNK = 256 * 1024;
export const BLOB_DOWNLOAD_LIMIT = 64 * 1024 * 1024;
export const BLOB_LIMIT_MESSAGE = "In-memory downloads are limited to 64 MiB each and two retained downloads across all Files panes; finish or cancel another download before retrying, or use disk-save support.";
export function validSHA256(value: unknown): value is string { return typeof value === "string" && /^[a-f0-9]{64}$/.test(value); }
export function checkAbort(signal: AbortSignal) { if (signal.aborted) throw new DOMException("Transfer canceled", "AbortError"); }
export async function readSource(source: UploadSource, start: number, end: number, signal: AbortSignal) {
  checkAbort(signal);
  const data = source.read ? await source.read(start, end, signal) : await source.slice!(start, end).arrayBuffer();
  checkAbort(signal);
  if (data.byteLength !== end - start) throw new Error("Source changed or returned a short range");
  return data;
}
const K = new Uint32Array([
  0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,
  0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,
  0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,
  0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,
  0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,
  0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,
  0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,
  0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2,
]);
const rotate = (x: number, n: number) => (x >>> n) | (x << (32 - n));
/** Incremental SHA-256: one 64-byte block and one reusable schedule. */
export class FileSHA256 {
  private state = new Uint32Array([0x6a09e667,0xbb67ae85,0x3c6ef372,0xa54ff53a,0x510e527f,0x9b05688c,0x1f83d9ab,0x5be0cd19]);
  private block = new Uint8Array(64);
  private words = new Uint32Array(64);
  private used = 0;
  private bytes = 0;
  update(data: Uint8Array) {
    this.bytes += data.length;
    for (let offset = 0; offset < data.length;) {
      const count = Math.min(64 - this.used, data.length - offset);
      this.block.set(data.subarray(offset, offset + count), this.used);
      this.used += count; offset += count;
      if (this.used === 64) { this.compress(); this.used = 0; }
    }
    return this;
  }
  private compress() {
    const w = this.words, view = new DataView(this.block.buffer);
    for (let i = 0; i < 16; i++) w[i] = view.getUint32(i * 4);
    for (let i = 16; i < 64; i++) {
      const a = w[i - 15], b = w[i - 2];
      w[i] = w[i - 16] + (rotate(a,7) ^ rotate(a,18) ^ (a >>> 3)) + w[i - 7] + (rotate(b,17) ^ rotate(b,19) ^ (b >>> 10));
    }
    let [a,b,c,d,e,f,g,h] = this.state;
    for (let i = 0; i < 64; i++) {
      const t = (h + (rotate(e,6) ^ rotate(e,11) ^ rotate(e,25)) + ((e & f) ^ (~e & g)) + K[i] + w[i]) | 0;
      const u = ((rotate(a,2) ^ rotate(a,13) ^ rotate(a,22)) + ((a & b) ^ (a & c) ^ (b & c))) | 0;
      h=g; g=f; f=e; e=(d+t)|0; d=c; c=b; b=a; a=(t+u)|0;
    }
    this.state[0]+=a; this.state[1]+=b; this.state[2]+=c; this.state[3]+=d;
    this.state[4]+=e; this.state[5]+=f; this.state[6]+=g; this.state[7]+=h;
  }
  digest() {
    const copy = new FileSHA256();
    copy.state.set(this.state); copy.block.set(this.block); copy.used = this.used;
    copy.block[copy.used++] = 0x80;
    if (copy.used > 56) { copy.block.fill(0, copy.used); copy.compress(); copy.used = 0; }
    copy.block.fill(0, copy.used, 56);
    const view = new DataView(copy.block.buffer);
    view.setUint32(56, Math.floor(this.bytes / 0x20000000));
    view.setUint32(60, (this.bytes * 8) >>> 0);
    copy.compress();
    return Array.from(copy.state, word => word.toString(16).padStart(8, "0")).join("");
  }
}
const fingerprints = new WeakMap<UploadSource, string>();
export async function hashSource(source: UploadSource, signal: AbortSignal, end = source.size): Promise<string> {
  if (!Number.isSafeInteger(end) || end < 0 || end > source.size) throw new Error("Invalid hash prefix length");
  const hash = new FileSHA256();
  for (let offset = 0; offset < end; offset += HASH_CHUNK) {
    hash.update(new Uint8Array(await readSource(source, offset, Math.min(end, offset + HASH_CHUNK), signal)));
    // Yield even when the source is already resident in memory.
    const yielded = Promise.withResolvers<void>();
    setTimeout(yielded.resolve, 0);
    await yielded.promise;
    checkAbort(signal);
  }
  checkAbort(signal);
  return hash.digest();
}
export async function sourceFingerprint(source: UploadSource, signal: AbortSignal) {
  checkAbort(signal);
  if (validSHA256(source.sha256)) return source.sha256;
  const cached = fingerprints.get(source);
  if (cached) return cached;
  const result = await hashSource(source, signal);
  fingerprints.set(source, result);
  return result;
}
export class UploadPrefixMismatch extends Error {
  constructor() { super("Upload resume prefix does not match the source; retry to restart the destination partial"); }
}
export async function validateUploadPrefix(source: UploadSource, identity: string | undefined, reply: Record<string, unknown>, signal: AbortSignal) {
  const offset = Number(reply.offset ?? 0);
  if (!validSHA256(identity) || reply.sha256 !== identity || !validSHA256(reply.prefixSha256)) throw new Error("Upload integrity metadata missing or changed; update the device");
  if (await hashSource(source, signal, offset) !== reply.prefixSha256) throw new UploadPrefixMismatch();
  return offset;
}
export function downloadMetadata(reply: Record<string, unknown>, previous?: string) {
  const size = Number(reply.size);
  if (!Number.isSafeInteger(size) || size < 0 || !validSHA256(reply.sha256)) throw new Error("Download integrity metadata missing or invalid; update the device");
  if (previous && previous !== reply.sha256) throw new Error("Download source changed; restart the download");
  return { size, sha256: reply.sha256 };
}
export function checkBlobDownload(size: number, received = 0) {
  if (!Number.isSafeInteger(size) || size < 0 || size > BLOB_DOWNLOAD_LIMIT || received > BLOB_DOWNLOAD_LIMIT) throw new Error(BLOB_LIMIT_MESSAGE);
  if (received > size) throw new Error("Download exceeded its declared size");
}
