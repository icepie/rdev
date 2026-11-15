import { expect, it, vi } from "vitest";
import { createDiskDownloads, DiskDownload, DOWNLOAD_RANGE } from "../ui/lib/file-download-disk";

it("commits only completed disk writes and waits for close before completion", async () => {
  const write = Promise.withResolvers();
  const close = Promise.withResolvers();
  const writer = { write: vi.fn(() => write.promise), close: vi.fn(() => close.promise), abort: vi.fn() };
  const progress = vi.fn();
  const sink = new DiskDownload(writer, progress);
  await sink.begin();
  sink.receive(0, new Uint8Array([1, 2, 3]).buffer);
  const ending = sink.end(3, 3);
  expect(sink.committed).toBe(0);
  expect(writer.close).not.toHaveBeenCalled();
  write.resolve();
  await sink.drain();
  expect(sink.committed).toBe(3);
  expect(progress).toHaveBeenCalledWith(3);
  let complete = false;
  void ending.then(() => { complete = true; });
  await Promise.resolve();
  expect(complete).toBe(false);
  close.resolve();
  expect(await ending).toBe("complete");
});

it("bounds a range and resumes only after queued writes are committed", async () => {
  const write = Promise.withResolvers();
  const writer = { write: vi.fn(() => write.promise), close: vi.fn(), abort: vi.fn() };
  const sink = new DiskDownload(writer, () => {});
  await sink.begin();
  sink.receive(0, new ArrayBuffer(DOWNLOAD_RANGE));
  expect(() => sink.receive(DOWNLOAD_RANGE, new ArrayBuffer(1))).toThrow(/bounded/);
  const ending = sink.end(DOWNLOAD_RANGE, DOWNLOAD_RANGE + 1);
  write.resolve();
  expect(await ending).toBe("next");
  expect(await sink.begin()).toBe(DOWNLOAD_RANGE);
  sink.receive(DOWNLOAD_RANGE, new Uint8Array([42]).buffer);
  expect(await sink.end(DOWNLOAD_RANGE + 1, DOWNLOAD_RANGE + 1)).toBe("complete");
  expect(writer.write.mock.calls[1][0].position).toBe(DOWNLOAD_RANGE);
});

it("does not finalize a suspended generation and propagates disk errors", async () => {
  const write = Promise.withResolvers();
  const writer = { write: vi.fn(() => write.promise), close: vi.fn(), abort: vi.fn() };
  const failure = vi.fn();
  const sink = new DiskDownload(writer, () => {}, failure);
  await sink.begin();
  sink.receive(0, new ArrayBuffer(1));
  const ending = sink.end(1, 1);
  sink.suspend();
  write.resolve();
  expect(await ending).toBe("duplicate");
  expect(writer.close).not.toHaveBeenCalled();
  await sink.begin();
  const error = new Error("disk full");
  writer.write.mockRejectedValueOnce(error);
  sink.receive(1, new ArrayBuffer(1));
  await expect(sink.drain()).rejects.toThrow("disk full");
  expect(sink.committed).toBe(1);
  expect(failure).toHaveBeenCalledWith(error);
  await sink.abort();
});

it("keeps destination selection pending until writer creation finishes and discards cancellation", async () => {
  const downloads = createDiskDownloads();
  const picker = Promise.withResolvers();
  const writable = Promise.withResolvers();
  let current = true;
  const opening = downloads.open("task", picker.promise, () => current, () => {});
  expect(downloads.isOpening("task")).toBe(true);
  picker.resolve({ createWritable: () => writable.promise });
  await Promise.resolve();
  expect(downloads.isOpening("task")).toBe(true);
  current = false;
  const abort = vi.fn();
  writable.resolve({ abort });
  expect(await opening).toBe(false);
  expect(abort).toHaveBeenCalledOnce();
  expect(downloads.get("task")).toBeUndefined();
  expect(downloads.isOpening("task")).toBe(false);
});
it("still aborts while final writes are draining", async () => {
  const write = Promise.withResolvers();
  const writer = { write: vi.fn(() => write.promise), close: vi.fn(), abort: vi.fn() };
  const sink = new DiskDownload(writer, () => {});
  await sink.begin();
  sink.receive(0, new Uint8Array([1]).buffer);
  const ending = sink.end(1, 1);
  const aborting = sink.abort();
  write.resolve();
  expect(await ending).toBe("duplicate");
  await aborting;
  expect(writer.close).not.toHaveBeenCalled();
  expect(writer.abort).toHaveBeenCalledOnce();
});

it("ignores suspend and abort after close starts", async () => {
  const close = Promise.withResolvers();
  const writer = { write: vi.fn(), close: vi.fn(() => close.promise), abort: vi.fn() };
  const started = Promise.withResolvers();
  const saving = vi.fn(() => started.resolve());
  const sink = new DiskDownload(writer, () => {});
  await sink.begin();
  sink.receive(0, new Uint8Array([1]).buffer);
  const ending = sink.end(1, 1, saving);
  await started.promise;
  expect(saving).toHaveBeenCalledOnce();
  expect(writer.close).toHaveBeenCalledOnce();
  sink.suspend();
  await sink.abort();
  expect(writer.abort).not.toHaveBeenCalled();
  close.resolve();
  expect(await ending).toBe("complete");
});

it("reports writer close failures", async () => {
  const error = new Error("close failed");
  const writer = { write: vi.fn(), close: vi.fn().mockRejectedValue(error), abort: vi.fn() };
  const failure = vi.fn();
  const sink = new DiskDownload(writer, () => {}, failure);
  await sink.begin();
  sink.receive(0, new Uint8Array([1]).buffer);
  await expect(sink.end(1, 1)).rejects.toBe(error);
  expect(failure).toHaveBeenCalledOnce();
  expect(failure).toHaveBeenCalledWith(error);
  await sink.abort();
  expect(writer.abort).toHaveBeenCalledOnce();
});

it("verifies the full committed SHA before closing and preserves the target on mismatch", async () => {
  const writer = { write: vi.fn(), close: vi.fn(), abort: vi.fn() };
  const sink = new DiskDownload(writer, () => {});
  sink.setIdentity("0".repeat(64), 3);
  await sink.begin();
  sink.receive(0, new Uint8Array([1, 2, 3]).buffer);
  await expect(sink.end(3, 3)).rejects.toThrow(/SHA-256/);
  expect(writer.close).not.toHaveBeenCalled();
  await sink.abort();
  expect(writer.abort).toHaveBeenCalledOnce();
});

it("rejects changed identities and dishonest size before buffering", async () => {
  const sink = new DiskDownload({ write: vi.fn(), close: vi.fn(), abort: vi.fn() }, () => {});
  sink.setIdentity("a".repeat(64), 1);
  expect(() => sink.setIdentity("b".repeat(64), 1)).toThrow(/changed/);
  await sink.begin();
  expect(() => sink.receive(0, new ArrayBuffer(2))).toThrow(/bounded/);
});

it("closes a verified destination only after the full committed hash matches", async () => {
  const writer = { write: vi.fn(), close: vi.fn(), abort: vi.fn() };
  const sink = new DiskDownload(writer, () => {});
  sink.setIdentity("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", 3);
  await sink.begin();
  sink.receive(0, new TextEncoder().encode("abc").buffer);
  expect(await sink.end(3, 3)).toBe("complete");
  expect(writer.close).toHaveBeenCalledOnce();
  expect(writer.abort).not.toHaveBeenCalled();
});
