import { expect, it, vi } from "vitest";
import { uploadChunks } from "../ui/lib/upload-chunks";

it("sends sequential chunks from the acknowledged resume offset", async () => {
  let offset = 2;
  const sent = [];
  const complete = await uploadChunks({
    file: new Blob(["abcdefg"]),
    chunkSize: 2,
    offset: () => offset,
    signal: new AbortController().signal,
    write: async (bytes, start) => {
      sent.push([start, new TextDecoder().decode(bytes)]);
      offset += bytes.byteLength;
    },
  });
  expect(complete).toBe(true);
  expect(sent).toEqual([
    [2, "cd"],
    [4, "ef"],
    [6, "g"],
  ]);
});

it("releases a pending acknowledgement on cancel and ignores its late completion", async () => {
  const controller = new AbortController();
  const sent = Promise.withResolvers();
  const ack = Promise.withResolvers();
  let offset = 0;
  const write = vi.fn(() => {
    sent.resolve();
    return ack.promise;
  });
  const result = uploadChunks({
    file: new Blob(["abcdef"]),
    chunkSize: 2,
    offset: () => offset,
    signal: controller.signal,
    write,
  });
  await sent.promise;
  controller.abort();
  expect(await result).toBe(false);
  offset = 2;
  ack.resolve();
  await Promise.resolve();
  expect(write).toHaveBeenCalledTimes(1);
});

it("does not send a file read that finishes after cancellation", async () => {
  const read = Promise.withResolvers();
  const controller = new AbortController();
  const write = vi.fn();
  const result = uploadChunks({
    file: { size: 2, slice: () => ({ arrayBuffer: () => read.promise }) },
    chunkSize: 2,
    offset: () => 0,
    signal: controller.signal,
    write,
  });
  controller.abort();
  read.resolve(new ArrayBuffer(2));
  expect(await result).toBe(false);
  expect(write).not.toHaveBeenCalled();
});

it("fails stalled or invalid acknowledgements instead of looping forever", async () => {
  await expect(
    uploadChunks({
      file: new Blob(["ab"]),
      chunkSize: 2,
      offset: () => 0,
      signal: new AbortController().signal,
      write: async () => {},
    }),
  ).rejects.toThrow("Invalid upload acknowledgement");
  vi.useFakeTimers();
  try {
    const result = uploadChunks({
      file: {
        size: 2,
        slice: () => ({ arrayBuffer: async () => new ArrayBuffer(2) }),
      },
      chunkSize: 2,
      offset: () => 0,
      signal: new AbortController().signal,
      write: () => new Promise(() => {}),
    });
    const rejected = expect(result).rejects.toThrow("timed out");
    await vi.advanceTimersByTimeAsync(30000);
    await rejected;
    expect(vi.getTimerCount()).toBe(0);
  } finally {
    vi.useRealTimers();
  }
});

it("rejects corrupt resume positions before sending file bytes", async () => {
  for (const offset of [-1, NaN, Infinity, 3, 0.5]) {
    const write = vi.fn();
    await expect(
      uploadChunks({
        file: new Blob(["ab"]),
        chunkSize: 2,
        offset: () => offset,
        signal: new AbortController().signal,
        write,
      }),
    ).rejects.toThrow("resume offset");
    expect(write).not.toHaveBeenCalled();
  }
});
