import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { apiJSON } from "../ui/lib/api";
import { createFileRTCTransfers, encodeFileRTCFrame, FILE_RTC_CHUNK, FILE_RTC_WINDOW, remapFileRTCFrame } from "../ui/lib/file-webrtc";
import { decodeFrame, encodeFrame } from "../ui/lib/file-transfer-frame";
import { uploadChunks } from "../ui/lib/upload-chunks";

vi.mock("../ui/lib/api", () => ({ apiJSON: vi.fn() }));

class Channel extends EventTarget {
  readyState = "connecting";
  bufferedAmount = 0;
  sent = [];
  send(data) { this.sent.push(data); this.onSend?.(data); }
  open() { this.readyState = "open"; this.onopen?.(); this.dispatchEvent(new Event("open")); }
  receive(data) { this.onmessage?.({ data }); }
  close() { this.readyState = "closed"; this.onclose?.(); }
}
class Peer {
  static all = [];
  channel = new Channel();
  connectionState = "new";
  candidates = [];
  constructor() { Peer.all.push(this); }
  createDataChannel(label, options) { this.label = label; this.options = options; return this.channel; }
  async createOffer() { return { type: "offer", sdp: "test-offer" }; }
  async setLocalDescription() { this.onicecandidate?.({ candidate: { toJSON: () => ({ candidate: "early" }) } }); }
  async setRemoteDescription(value) { this.remoteDescription = value; }
  async addIceCandidate(value) { this.candidates.push(value); }
  close() { this.connectionState = "closed"; this.channel.close(); this.onconnectionstatechange?.(); }
}
let managers;
beforeEach(() => {
  vi.useFakeTimers();
  vi.stubGlobal("window", globalThis);
  vi.stubGlobal("RTCPeerConnection", Peer);
  apiJSON.mockResolvedValue({ enabled: true });
  Peer.all = [];
  managers = [];
});
afterEach(() => {
  for (const manager of managers) manager.clear();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});
async function setup(direction = "download", overrides = {}) {
  const manager = createFileRTCTransfers();
  managers.push(manager);
  const options = { taskId: "task", wireId: "wire", direction, isCurrent: () => true, send: vi.fn(), onBinary: vi.fn(), onFallback: vi.fn(), ...overrides };
  const offer = await manager.prepare(options);
  return { manager, options, offer, peer: Peer.all.at(-1) };
}
async function answer(manager, peer) {
  manager.text({ op: "rtc_signal", taskId: "wire", rtcType: "answer", rtcData: { type: "answer", sdp: "answer" } });
  await Promise.resolve();
  peer.channel.open();
}

it("remaps empty direct identities without weakening the ordinary WS decoder", () => {
  const frame = encodeFileRTCFrame(0x22, 258, new Uint8Array([0, 255]).buffer);
  expect([...new Uint8Array(frame)]).toEqual([0x22, 0, 0, 0, 0, 0, 0, 0, 1, 2, 0, 255]);
  expect(decodeFrame(frame)).toBeNull();
  expect(decodeFrame(remapFileRTCFrame(frame, "attempt-1"))).toMatchObject({ typ: 0x22, taskId: "attempt-1", offset: 258 });
  expect(remapFileRTCFrame(encodeFrame(0x22, "foreign", 0), "attempt-1")).toBeNull();
  expect(() => encodeFileRTCFrame(0x20, 0, new ArrayBuffer(FILE_RTC_CHUNK + 1))).toThrow();
  expect(remapFileRTCFrame(new ArrayBuffer(9), "wire")).toBeNull();
  new DataView(frame).setBigUint64(2, BigInt(Number.MAX_SAFE_INTEGER) + 1n);
  expect(remapFileRTCFrame(frame, "wire")).toBeNull();
});

it("creates no peer when the authenticated server gate is disabled", async () => {
  apiJSON.mockResolvedValue({ enabled: false });
  const { offer } = await setup();
  expect(offer).toEqual({});
  expect(Peer.all).toHaveLength(0);
  expect(apiJSON).toHaveBeenCalledWith("/api/desktop/rtc", expect.objectContaining({ cache: "no-store" }));
});

it("queues ICE until the owner sends start and queues remote ICE until answer", async () => {
  const { manager, options, peer, offer } = await setup();
  expect(offer).toEqual({ rtcType: "offer", rtcData: { type: "offer", sdp: "test-offer" } });
  expect(peer.label).toBe("rdev-file");
  expect(peer.options).toEqual({ ordered: true });
  expect(options.send).not.toHaveBeenCalled();
  manager.started("wire");
  expect(options.send).toHaveBeenCalledExactlyOnceWith({ op: "rtc_signal", taskId: "wire", rtcType: "candidate", rtcData: { candidate: "early" } });
  manager.text({ op: "rtc_signal", taskId: "wire", rtcType: "candidate", rtcData: { candidate: "remote" } });
  expect(peer.candidates).toEqual([]);
  await answer(manager, peer);
  expect(peer.candidates).toEqual([{ candidate: "remote" }]);
});

it("accepts unsupported-device relay metadata without canceling or restarting", async () => {
  const { manager, options, peer } = await setup();
  manager.started("wire");
  options.send.mockClear();
  expect(manager.text({ op: "download_start", taskId: "wire", offset: 0 })).toBe(false);
  expect(manager.direct("wire")).toBe(false);
  expect(peer.channel.readyState).toBe("closed");
  await vi.advanceTimersByTimeAsync(20000);
  expect(options.onFallback).not.toHaveBeenCalled();
  expect(options.send).not.toHaveBeenCalled();
});

it("forwards overtaking relay terminal while preserving ordered direct bytes", async () => {
  const { manager, options, peer } = await setup();
  manager.started("wire");
  await answer(manager, peer);
  expect(manager.text({ op: "transfer_end", taskId: "wire", offset: 3, success: true, sha256: "a".repeat(64) })).toBe(false);
  expect(options.onBinary).not.toHaveBeenCalled();
  peer.channel.receive(encodeFileRTCFrame(0x22, 0, new Uint8Array([1, 2, 3]).buffer));
  expect(manager.direct("wire")).toBe(true);
  peer.channel.receive(encodeFileRTCFrame(0x23, 3));
  expect(options.onBinary.mock.calls.map(([frame]) => decodeFrame(frame).typ)).toEqual([0x22, 0x23]);
  expect(options.send).toHaveBeenLastCalledWith({ op: "rtc_signal", taskId: "wire", rtcType: "close" });
  expect(manager.direct("wire")).toBe(false);
  expect(options.onFallback).not.toHaveBeenCalled();
});

it("falls back once after failure before direct end, and pins the new attempt to relay", async () => {
  const { manager, options, peer } = await setup();
  manager.started("wire");
  await answer(manager, peer);
  manager.text({ op: "transfer_end", taskId: "wire" });
  peer.close();
  peer.close();
  await vi.advanceTimersByTimeAsync(20000);
  expect(options.onFallback).toHaveBeenCalledTimes(1);
  expect(await manager.prepare({ ...options, wireId: "relay-attempt" })).toEqual({});
  expect(Peer.all).toHaveLength(1);
  peer.channel.receive(encodeFileRTCFrame(0x23, 0));
  expect(options.onBinary).not.toHaveBeenCalled();
});

it("bounds negotiation and established stalls without retry loops", async () => {
  const negotiation = await setup();
  negotiation.manager.started("wire");
  await vi.advanceTimersByTimeAsync(5000);
  expect(negotiation.options.onFallback).toHaveBeenCalledTimes(1);
  const connected = await setup();
  connected.manager.started("wire");
  await answer(connected.manager, connected.peer);
  await vi.advanceTimersByTimeAsync(14999);
  expect(connected.options.onFallback).not.toHaveBeenCalled();
  await vi.advanceTimersByTimeAsync(1);
  expect(connected.options.onFallback).toHaveBeenCalledTimes(1);
});

it("does not auto-resume canceled, stale or unmounted attempts", async () => {
  for (const mode of ["cancel", "stale", "unmount"]) {
    let current = true;
    const { manager, options, peer } = await setup("download", { isCurrent: () => current });
    manager.started("wire");
    if (mode === "cancel") manager.close("wire", false);
    else if (mode === "unmount") manager.clear();
    else current = false;
    peer.close();
    await vi.advanceTimersByTimeAsync(20000);
    expect(options.onFallback).not.toHaveBeenCalled();
  }
});

it("uploads one 16KiB chunk per exact device ACK, including the final short chunk", async () => {
  let offset = 7;
  let ack;
  const { manager, peer } = await setup("upload", {
    onBinary: frame => { offset = decodeFrame(frame).offset; ack.resolve(); },
  });
  manager.started("wire");
  await answer(manager, peer);
  peer.channel.onSend = frame => {
    const end = Number(new DataView(frame).getBigUint64(2)) + frame.byteLength - 10;
    peer.channel.receive(encodeFileRTCFrame(0x21, end));
  };
  const controller = new AbortController();
  const complete = await uploadChunks({
    file: new Blob([new Uint8Array(7 + FILE_RTC_CHUNK * 2 + 13)]),
    chunkSize: FILE_RTC_CHUNK, offset: () => offset, signal: controller.signal,
    write: async (payload, start) => {
      ack = Promise.withResolvers();
      await manager.send("wire", start, payload, controller.signal);
      return ack.promise;
    },
  });
  expect(complete).toBe(true);
  expect(peer.channel.sent.map(frame => frame.byteLength - 10)).toEqual([FILE_RTC_CHUNK, FILE_RTC_CHUNK, 13]);
  expect(peer.channel.sent.map(frame => Number(new DataView(frame).getBigUint64(2)))).toEqual([7, 7 + FILE_RTC_CHUNK, 7 + FILE_RTC_CHUNK * 2]);
});

it("rejects a partial ACK rather than advancing the upload loop", async () => {
  const { manager, options, peer } = await setup("upload");
  manager.started("wire");
  await answer(manager, peer);
  await manager.send("wire", 0, new ArrayBuffer(FILE_RTC_CHUNK), new AbortController().signal);
  peer.channel.receive(encodeFileRTCFrame(0x21, 1));
  expect(options.onBinary).not.toHaveBeenCalled();
  expect(options.onFallback).toHaveBeenCalledTimes(1);
});

it("bounds channel buffering and releases blocked writers on cancellation", async () => {
  const { manager, peer, options } = await setup("upload");
  manager.started("wire");
  await answer(manager, peer);
  peer.channel.bufferedAmount = 512 * 1024;
  const controller = new AbortController();
  const pending = manager.send("wire", 0, new ArrayBuffer(FILE_RTC_CHUNK), controller.signal);
  expect(peer.channel.sent).toHaveLength(0);
  controller.abort();
  await expect(pending).rejects.toThrow("closed");
  expect(options.onFallback).not.toHaveBeenCalled();
  peer.channel.bufferedAmount = 0;
  await manager.send("wire", 0, new ArrayBuffer(8), new AbortController().signal);
  expect(peer.channel.sent).toHaveLength(1);
});

it("accepts cumulative ACKs only at sent boundaries and rejects replay", async () => {
  const { manager, options, peer } = await setup("upload");
  manager.started("wire");
  await answer(manager, peer);
  const signal = new AbortController().signal;
  await manager.send("wire", 0, new ArrayBuffer(FILE_RTC_CHUNK), signal);
  await manager.send("wire", FILE_RTC_CHUNK, new ArrayBuffer(7), signal);
  peer.channel.receive(encodeFileRTCFrame(0x21, FILE_RTC_CHUNK + 7));
  expect(decodeFrame(options.onBinary.mock.calls[0][0]).offset).toBe(FILE_RTC_CHUNK + 7);
  expect(options.onFallback).not.toHaveBeenCalled();
  peer.channel.receive(encodeFileRTCFrame(0x21, FILE_RTC_CHUNK));
  expect(options.onFallback).toHaveBeenCalledTimes(1);
  expect(options.onBinary).toHaveBeenCalledTimes(1);
});

it("bounds the fixed RTC ACK window and releases it only at a sent boundary", async () => {
  const { manager, options, peer } = await setup("upload");
  manager.started("wire");
  await answer(manager, peer);
  const signal = new AbortController().signal;
  const payload = new ArrayBuffer(FILE_RTC_CHUNK);
  for (let offset = 0; offset < FILE_RTC_WINDOW; offset += FILE_RTC_CHUNK)
    await manager.send("wire", offset, payload, signal);
  expect(peer.channel.sent).toHaveLength(FILE_RTC_WINDOW / FILE_RTC_CHUNK);
  await expect(manager.send("wire", FILE_RTC_WINDOW, payload, signal)).rejects.toThrow("Invalid RTC upload window");
  peer.channel.receive(encodeFileRTCFrame(0x21, FILE_RTC_WINDOW));
  expect(options.onFallback).not.toHaveBeenCalled();
  await manager.send("wire", FILE_RTC_WINDOW, payload, signal);
  expect(peer.channel.sent).toHaveLength(FILE_RTC_WINDOW / FILE_RTC_CHUNK + 1);
});

it("finishes a full RTC upload window and short tail using cumulative ACKs", async () => {
  let offset = 7;
  const acks = new Map();
  const { manager, options, peer } = await setup("upload", {
    onBinary: frame => {
      offset = decodeFrame(frame).offset;
      for (const [end, resolve] of acks) {
        if (end <= offset) { acks.delete(end); resolve(); }
      }
    },
  });
  manager.started("wire");
  await answer(manager, peer);
  const size = 7 + FILE_RTC_WINDOW + 13;
  peer.channel.onSend = frame => {
    const end = Number(new DataView(frame).getBigUint64(2)) + frame.byteLength - 10;
    if (end === 7 + FILE_RTC_WINDOW || end === size)
      peer.channel.receive(encodeFileRTCFrame(0x21, end));
  };
  const controller = new AbortController();
  const complete = await uploadChunks({
    file: new Blob([new Uint8Array(size)]), chunkSize: FILE_RTC_CHUNK,
    windowBytes: FILE_RTC_WINDOW, offset: () => offset, signal: controller.signal,
    write: async (payload, start) => {
      const ack = Promise.withResolvers();
      acks.set(start + payload.byteLength, ack.resolve);
      await manager.send("wire", start, payload, controller.signal);
      return ack.promise;
    },
  });
  expect(complete).toBe(true);
  expect(offset).toBe(size);
  expect(acks.size).toBe(0);
  expect(peer.channel.sent).toHaveLength(FILE_RTC_WINDOW / FILE_RTC_CHUNK + 1);
  expect(peer.channel.sent.at(-1).byteLength - 10).toBe(13);
  expect(options.onFallback).not.toHaveBeenCalled();
});

it("keeps negotiated ranges on one peer and waits for disk credit", async () => {
  const { manager, peer, options, offer } = await setup("download", { boundedDownload: true });
  expect(offer.rtcData.rangeAck).toBe(true);
  manager.started("wire");
  manager.text({ op: "rtc_signal", taskId: "wire", rtcType: "answer", rtcData: { type: "answer", sdp: "answer", rangeAck: true } });
  await Promise.resolve();
  peer.channel.open();
  peer.channel.receive(encodeFileRTCFrame(0x22, 0, new Uint8Array([1, 2]).buffer));
  peer.channel.receive(encodeFileRTCFrame(0x23, 2));
  expect(peer.channel.readyState).toBe("open");
  expect(peer.channel.sent).toEqual([]);
  await vi.advanceTimersByTimeAsync(20000);
  expect(options.onFallback).not.toHaveBeenCalled();
  expect(manager.continueDownload("wire", 2)).toBe(true);
  expect([...new Uint8Array(peer.channel.sent[0])]).toEqual([0x25, 0, 0, 0, 0, 0, 0, 0, 0, 2]);
  peer.channel.receive(encodeFileRTCFrame(0x22, 2, new Uint8Array([3]).buffer));
  peer.channel.receive(encodeFileRTCFrame(0x23, 3));
  expect(options.onBinary.mock.calls.map(([frame]) => decodeFrame(frame).offset)).toEqual([0, 2, 2, 3]);
  manager.close("wire");
  expect(options.onFallback).not.toHaveBeenCalled();
});

it("rejects wrong credit boundaries and data arriving before credit", async () => {
  for (const invalidCredit of [true, false]) {
    const { manager, peer, options } = await setup("download", { boundedDownload: true });
    manager.started("wire");
    manager.text({ op: "rtc_signal", taskId: "wire", rtcType: "answer", rtcData: { type: "answer", sdp: "answer", rangeAck: true } });
    await Promise.resolve();
    peer.channel.open();
    peer.channel.receive(encodeFileRTCFrame(0x23, 4));
    if (invalidCredit) expect(manager.continueDownload("wire", 3)).toBe(false);
    else peer.channel.receive(encodeFileRTCFrame(0x22, 4, new Uint8Array([1]).buffer));
    expect(options.onFallback).toHaveBeenCalledTimes(1);
    expect(peer.channel.sent).toEqual([]);
  }
});

it("uses legacy per-range completion without negotiated credit", async () => {
  const { manager, peer, options } = await setup("download", { boundedDownload: true });
  manager.started("wire");
  await answer(manager, peer);
  peer.channel.receive(encodeFileRTCFrame(0x23, 4));
  expect(manager.continueDownload("wire", 4)).toBe(false);
  expect(peer.channel.readyState).toBe("closed");
  expect(options.onFallback).not.toHaveBeenCalled();
});

it("preserves chunk order when a later short chunk fits before a blocked full chunk", async () => {
  const { manager, peer } = await setup("upload");
  manager.started("wire");
  await answer(manager, peer);
  peer.channel.bufferedAmount = 512 * 1024 - FILE_RTC_CHUNK;
  const signal = new AbortController().signal;
  const full = manager.send("wire", 0, new ArrayBuffer(FILE_RTC_CHUNK), signal);
  const short = manager.send("wire", FILE_RTC_CHUNK, new ArrayBuffer(1), signal);
  await Promise.resolve();
  expect(peer.channel.sent).toEqual([]);
  peer.channel.bufferedAmount = 0;
  peer.channel.dispatchEvent(new Event("bufferedamountlow"));
  await Promise.all([full, short]);
  expect(peer.channel.sent.map(frame => Number(new DataView(frame).getBigUint64(2)))).toEqual([0, FILE_RTC_CHUNK]);
});
