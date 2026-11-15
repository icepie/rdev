import { createHash } from "node:crypto";
import { expect, it, vi } from "vitest";
import { BLOB_DOWNLOAD_LIMIT, FileSHA256, HASH_CHUNK, UploadPrefixMismatch, checkBlobDownload, downloadMetadata, hashSource, sourceFingerprint, validateUploadPrefix } from "../ui/lib/file-integrity";

const digest = data => createHash("sha256").update(data).digest("hex");
const signal = () => new AbortController().signal;
it.each([0, 1, 55, 56, 63, 64, 65, 1024, HASH_CHUNK + 13])("matches independent SHA-256 at padding boundary %i", async size => {
  const bytes = Uint8Array.from({ length: size }, (_, i) => i % 251);
  const hash = new FileSHA256();
  for (let i = 0; i < size; i += 17) hash.update(bytes.subarray(i, i + 17));
  expect(hash.digest()).toBe(digest(bytes));
  expect(hash.digest()).toBe(digest(bytes));
  expect(await hashSource(new Blob([bytes]), signal())).toBe(digest(bytes));
});
it("bounds reads, yields between them and caches only the same source object", async () => {
  const reads = [];
  const source = { size: HASH_CHUNK * 2 + 1, read: vi.fn(async (start, end) => { reads.push(end - start); return new ArrayBuffer(end - start); }) };
  const timer = vi.fn();
  setTimeout(timer, 0);
  const first = await sourceFingerprint(source, signal());
  expect(timer).toHaveBeenCalled();
  expect(reads).toEqual([HASH_CHUNK, HASH_CHUNK, 1]);
  expect(await sourceFingerprint(source, signal())).toBe(first);
  expect(source.read).toHaveBeenCalledTimes(3);
});
it("verifies exact persisted prefix and rejects same-size wrong files before upload", async () => {
  const source = new Blob(["abcdef"]);
  const sha256 = digest("abcdef");
  expect(await validateUploadPrefix(source, sha256, { sha256, offset: 3, prefixSha256: digest("abc") }, signal())).toBe(3);
  await expect(validateUploadPrefix(source, sha256, { sha256, offset: 3, prefixSha256: digest("xyz") }, signal())).rejects.toBeInstanceOf(UploadPrefixMismatch);
  await expect(validateUploadPrefix(source, sha256, { sha256, offset: 7, prefixSha256: digest("abcdef") }, signal())).rejects.toThrow(/prefix length/);
  await expect(validateUploadPrefix(source, sha256, { offset: 0 }, signal())).rejects.toThrow(/metadata/);
  expect(await validateUploadPrefix(source, sha256, { sha256, offset: 0, prefixSha256: digest("") }, signal())).toBe(0);
});
it("discards hashing reads completing after cancellation", async () => {
  const read = Promise.withResolvers();
  const controller = new AbortController();
  const result = hashSource({ size: 1, read: () => read.promise }, controller.signal);
  controller.abort(); read.resolve(new ArrayBuffer(1));
  await expect(result).rejects.toMatchObject({ name: "AbortError" });
});
it("rejects missing, changed or dishonest metadata and enforces actual Blob bytes", () => {
  const sha256 = digest("abc");
  expect(downloadMetadata({ size: 3, sha256 }, sha256)).toEqual({ size: 3, sha256 });
  expect(() => downloadMetadata({ size: 3, sha256: sha256.toUpperCase() })).toThrow();
  expect(() => downloadMetadata({ size: 3, sha256 }, digest("xyz"))).toThrow(/changed/);
  expect(() => checkBlobDownload(BLOB_DOWNLOAD_LIMIT + 1)).toThrow(/64 MiB/);
  expect(() => checkBlobDownload(0, BLOB_DOWNLOAD_LIMIT + 1)).toThrow(/64 MiB/);
  expect(() => checkBlobDownload(3, 4)).toThrow(/declared size/);
});
