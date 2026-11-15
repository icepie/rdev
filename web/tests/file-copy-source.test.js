import { createHash } from "node:crypto";
import { expect, it } from "vitest";
import { createRemoteFileSources } from "../ui/lib/file-copy-source";
import { uploadChunks } from "../ui/lib/upload-chunks";
const hash = value => createHash("sha256").update(value).digest("hex");
function fixture(bytes) {
  const requests = [];
  let id = 0;
  const sources = createRemoteFileSources(message => { if (message.op === "download_start") requests.push(message); }, () => `range-${++id}`);
  function reply(request, changed = false, terminal = true) {
    const sha256 = hash(changed ? "changed" : bytes);
    const end = Math.min(bytes.length, request.offset + request.length);
    sources.text({ op: "download_start", taskId: request.taskId, offset: request.offset, size: bytes.length, sha256 });
    if (end > request.offset) sources.binary({ typ: 0x22, taskId: request.taskId, offset: request.offset, payload: bytes.slice(request.offset, end).buffer });
    sources.binary({ typ: 0x23, taskId: request.taskId, offset: end, payload: new ArrayBuffer(0) });
    if (terminal) sources.text({ op: "transfer_end", taskId: request.taskId, offset: end, sha256, success: true });
  }
  return { sources, requests, reply };
}
it("gates range completion on authoritative terminal and echoes source identity", async () => {
  const bytes = new Uint8Array([1, 2, 3]);
  const { sources, requests, reply } = fixture(bytes);
  const opened = sources.open("device", "/file", "file", new AbortController().signal);
  reply(requests[0]);
  const source = await opened;
  const range = source.read(0, 3, new AbortController().signal);
  expect(requests[1].sha256).toBe(hash(bytes));
  reply(requests[1], false, false);
  let done = false;
  void range.then(() => { done = true; });
  await Promise.resolve();
  expect(done).toBe(false);
  sources.text({ op: "transfer_end", taskId: requests[1].taskId, offset: 3, sha256: hash(bytes), success: true });
  expect(new Uint8Array(await range)).toEqual(bytes);
});
it("pulls no next copy range until destination upload ACK releases capacity", async () => {
  const { sources, requests, reply } = fixture(new Uint8Array([1, 2, 3, 4]));
  const controller = new AbortController();
  const opened = sources.open("device", "/file", "file", controller.signal);
  reply(requests[0]);
  const source = await opened;
  const ack = Promise.withResolvers();
  const sent = Promise.withResolvers();
  let offset = 0;
  const copying = uploadChunks({ file: source, chunkSize: 2, offset: () => offset, signal: controller.signal,
    write: async () => { sent.resolve(); await ack.promise; },
  });
  reply(requests[1]);
  await sent.promise;
  expect(requests).toHaveLength(2);
  controller.abort();
  expect(await copying).toBe(false);
  offset = 2; ack.resolve();
  await Promise.resolve();
  expect(requests).toHaveLength(2);
});
it("rejects changed sources and ignores late frames after canceled ranges", async () => {
  const { sources, requests, reply } = fixture(new Uint8Array([1, 2, 3]));
  const controller = new AbortController();
  const opened = sources.open("device", "/file", "file", controller.signal);
  reply(requests[0]);
  const source = await opened;
  const changed = source.read(0, 3, controller.signal);
  reply(requests[1], true);
  await expect(changed).rejects.toThrow(/changed/);
  const canceled = source.read(0, 3, controller.signal);
  controller.abort();
  await expect(canceled).rejects.toMatchObject({ name: "AbortError" });
  expect(sources.binary({ taskId: requests[2].taskId, typ: 0x23, offset: 3, payload: new ArrayBuffer(0) })).toBe(false);
});
it("rejects ranges exceeding the memory limit before sending", async () => {
  const { sources, requests, reply } = fixture(new Uint8Array([1]));
  const opened = sources.open("device", "/file", "file", new AbortController().signal);
  reply(requests[0]);
  const source = await opened;
  await expect(source.read(0, 512 * 1024 + 1, new AbortController().signal)).rejects.toThrow(/bound/);
  expect(requests).toHaveLength(1);
});

it("rejects dishonest range overruns without retaining the extra payload", async () => {
  const { sources, requests } = fixture(new Uint8Array([1]));
  const opened = sources.open("device", "/file", "file", new AbortController().signal);
  const taskId = requests[0].taskId;
  sources.text({ op: "download_start", taskId, offset: 0, size: 1, sha256: hash(new Uint8Array([1])) });
  sources.binary({ taskId, typ: 0x22, offset: 0, payload: new ArrayBuffer(2) });
  await expect(opened).rejects.toThrow(/exceeded requested range/);
});

it("unblocks a pending source read on disconnect without resolving from stale terminal", async () => {
  const { sources, requests } = fixture(new Uint8Array([1]));
  const opened = sources.open("device", "/file", "file", new AbortController().signal);
  sources.clear();
  await expect(opened).rejects.toThrow(/disconnected/);
  expect(sources.text({ op: "transfer_end", taskId: requests[0].taskId, success: true })).toBe(false);
});
