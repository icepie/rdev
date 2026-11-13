import { afterEach, beforeEach, expect, it, vi } from "vitest";
const apiJSON = vi.hoisted(() => vi.fn());
vi.mock("../ui/lib/api", () => ({ apiJSON, wsURL: path => `ws://localhost${path}` }));
import { connectDesktopRTC, sendGPUDesktopMessage } from "../ui/lib/desktop-webrtc";

let sockets, peers;
beforeEach(() => {
  apiJSON.mockReset();
  sockets = []; peers = [];
  vi.stubGlobal("WebSocket", class {
    static OPEN = 1;
    static CONNECTING = 0;
    readyState = 1;
    send = vi.fn();
    close = vi.fn(() => { this.readyState = 3; });
    constructor(url) { this.url = url; sockets.push(this); }
  });
  vi.stubGlobal("RTCPeerConnection", class {
    channel = { readyState: "connecting", send: vi.fn(), close: vi.fn() };
    createDataChannel = vi.fn(() => this.channel);
    close = vi.fn();
    createOffer = vi.fn(async () => ({ type: "offer", sdp: "offer-sdp" }));
    setLocalDescription = vi.fn(async () => {});
    setRemoteDescription = vi.fn(async () => {});
    addIceCandidate = vi.fn(async () => {});
    constructor(config) { this.config = config; peers.push(this); }
  });
});
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers(); });
const options = () => ({ device: "device", session: "session", password: "password", supported: true, isCurrent: () => true, onMessage: vi.fn(), onState: vi.fn() });

it.each([{ enabled: false }, {}, { enabled: "true" }, null])("creates no peer or signaling socket when server response is %j", async config => {
  apiJSON.mockResolvedValue(config);
  expect(await connectDesktopRTC(options())).toBeNull();
  expect(apiJSON).toHaveBeenCalledWith("/api/desktop/rtc", { cache: "no-store" });
  expect(peers).toHaveLength(0); expect(sockets).toHaveLength(0);
});
it("fails closed on capability HTTP/auth/network/JSON errors", async () => {
  apiJSON.mockRejectedValue(new Error("401 Unauthorized"));
  expect(await connectDesktopRTC(options())).toBeNull();
  expect(peers).toHaveLength(0); expect(sockets).toHaveLength(0);
});
it("does not query or signal unsupported devices or browsers", async () => {
  expect(await connectDesktopRTC({ ...options(), supported: false })).toBeNull();
  vi.stubGlobal("RTCPeerConnection", undefined);
  expect(await connectDesktopRTC(options())).toBeNull();
  expect(apiJSON).not.toHaveBeenCalled(); expect(sockets).toHaveLength(0);
});
it("does not construct a late peer after disconnect or device replacement", async () => {
  let resolve;
  apiJSON.mockReturnValue(new Promise(done => { resolve = done; }));
  let current = true;
  const pending = connectDesktopRTC({ ...options(), isCurrent: () => current });
  current = false; resolve({ enabled: true });
  expect(await pending).toBeNull();
  expect(peers).toHaveLength(0); expect(sockets).toHaveLength(0);
});
it("negotiates only after opt-in and disposes signaling, channel and peer", async () => {
  apiJSON.mockResolvedValue({ enabled: true });
  const handlers = options();
  const rtc = await connectDesktopRTC(handlers);
  expect(peers).toHaveLength(1); expect(sockets).toHaveLength(1);
  expect(peers[0].createDataChannel).toHaveBeenCalledWith("rdev-desktop", { ordered: true });
  expect(sockets[0].url).toContain("/desktop/rtc?device=device&session=session&password=password");
  await sockets[0].onopen();
  expect(JSON.parse(sockets[0].send.mock.calls[0][0])).toMatchObject({ rtcType: "offer", session: "session" });
  peers[0].channel.readyState = "open"; peers[0].channel.onopen();
  expect(handlers.onState).toHaveBeenCalledWith("open");
  rtc.close(); rtc.close();
  expect(peers[0].close).toHaveBeenCalledTimes(1);
  expect(peers[0].channel.close).toHaveBeenCalledTimes(1);
  expect(sockets[0].close).toHaveBeenCalledTimes(1);
});
it("reports failed negotiation without autonomously retrying", async () => {
  vi.useFakeTimers(); apiJSON.mockResolvedValue({ enabled: true });
  const handlers = options();
  const rtc = await connectDesktopRTC(handlers);
  await vi.advanceTimersByTimeAsync(12000);
  expect(handlers.onState).toHaveBeenCalledWith("failed");
  expect(peers).toHaveLength(1); expect(sockets).toHaveLength(1);
  rtc.close();
});
it("keeps human input and clipboard on the relay while mirroring paused source config", () => {
  const ws = { readyState: 1, send: vi.fn() };
  const rtc = { dataChannel: { readyState: "open" }, send: vi.fn(() => true) };
  const config = { Config: { capturable_id: 2, frame_rate: 30, pointer_backend: "uinput" } };
  sendGPUDesktopMessage(ws, rtc, config, 5);
  expect(rtc.send).toHaveBeenCalledWith(JSON.stringify({ Config: { capturable_id: 5, frame_rate: 30 } }));
  expect(ws.send).toHaveBeenCalledWith(JSON.stringify({ Config: { ...config.Config, frame_rate: 0 } }));
  sendGPUDesktopMessage(ws, rtc, "ResumeVideo");
  expect(rtc.send).toHaveBeenCalledWith('"ResumeVideo"');
  expect(ws.send).toHaveBeenCalledWith('"PauseVideo"');
  rtc.send.mockClear(); ws.send.mockClear();
  for (const message of [{ PointerEvent: {} }, { KeyboardEvent: {} }, { WheelEvent: {} }, { TextInputEvent: {} }, { Cua: {} }, { AutoJs: {} }, "ReleaseKeyboard", "GetClipboard", { SetClipboard: {} }]) {
    sendGPUDesktopMessage(ws, rtc, message);
    expect(ws.send).toHaveBeenLastCalledWith(JSON.stringify(message));
  }
  expect(rtc.send).not.toHaveBeenCalled();
  rtc.dataChannel.readyState = "closed";
  sendGPUDesktopMessage(ws, rtc, config);
  sendGPUDesktopMessage(ws, rtc, "ResumeVideo");
  expect(ws.send).toHaveBeenCalledWith(JSON.stringify(config));
  expect(ws.send).toHaveBeenLastCalledWith('"ResumeVideo"');
  rtc.dataChannel.readyState = "open";
  rtc.send.mockReturnValue(false);
  sendGPUDesktopMessage(ws, rtc, config);
  expect(ws.send).toHaveBeenLastCalledWith(JSON.stringify(config));
  sendGPUDesktopMessage(ws, rtc, "ResumeVideo");
  expect(ws.send).toHaveBeenLastCalledWith('"ResumeVideo"');
});

function chunk(type, total, offset, payload) {
  const bytes = new Uint8Array(13 + payload.length);
  const view = new DataView(bytes.buffer);
  view.setUint32(0, 0x52444731); view.setUint8(4, type);
  view.setUint32(5, total); view.setUint32(9, offset);
  bytes.set(payload, 13);
  return bytes.buffer;
}

it("reassembles GPU binary and UTF8 text without exposing partial messages", async () => {
  apiJSON.mockResolvedValue({ enabled: true });
  const handlers = { ...options(), gpuFraming: true };
  const rtc = await connectDesktopRTC(handlers);
  const receive = peers[0].channel.onmessage;
  const data = new Uint8Array(20000).fill(7);
  receive({ data: chunk(0, data.length, 0, data.subarray(0, 16384)) });
  expect(handlers.onMessage).not.toHaveBeenCalled();
  receive({ data: chunk(0, data.length, 16384, data.subarray(16384)) });
  expect(new Uint8Array(handlers.onMessage.mock.calls[0][0].data)).toEqual(data);
  const text = new TextEncoder().encode('"屏幕"');
  receive({ data: chunk(1, text.length, 0, text.subarray(0, 2)) });
  receive({ data: chunk(1, text.length, 2, text.subarray(2)) });
  expect(handlers.onMessage.mock.calls[1][0].data).toBe('"屏幕"');
  rtc.close();
});

it.each([
  "not binary", new ArrayBuffer(12), chunk(2, 1, 0, [1]),
  chunk(0, 16 * 1024 * 1024 + 1, 0, [1]), chunk(0, 2, 1, [1]),
  chunk(0, 0, 0, [1]), chunk(0, 1, 0, []), chunk(1, 1, 0, [255]),
  chunk(0, 16385, 0, new Uint8Array(16385)),
])("closes and falls back on invalid GPU envelopes %#", async data => {
  apiJSON.mockResolvedValue({ enabled: true });
  const handlers = { ...options(), gpuFraming: true };
  await connectDesktopRTC(handlers);
  peers[0].channel.onmessage({ data });
  expect(handlers.onMessage).not.toHaveBeenCalled();
  expect(handlers.onState).toHaveBeenCalledWith("failed");
  expect(peers[0].close).toHaveBeenCalledTimes(1);
});

it("rejects interleaving and clears partial-frame timeout on close", async () => {
  vi.useFakeTimers(); apiJSON.mockResolvedValue({ enabled: true });
  const handlers = { ...options(), gpuFraming: true };
  const rtc = await connectDesktopRTC(handlers);
  peers[0].channel.readyState = "open"; peers[0].channel.onopen();
  peers[0].channel.onmessage({ data: chunk(0, 10, 0, [1]) });
  peers[0].channel.onmessage({ data: chunk(1, 10, 1, [1]) });
  expect(handlers.onState).toHaveBeenLastCalledWith("failed");
  rtc.close();
  await vi.advanceTimersByTimeAsync(16000);
  expect(handlers.onState.mock.calls.filter(([state]) => state === "failed")).toHaveLength(1);
});

it("times out incomplete GPU frames and leaves Go messages unchanged", async () => {
  vi.useFakeTimers(); apiJSON.mockResolvedValue({ enabled: true });
  const handlers = { ...options(), gpuFraming: true };
  await connectDesktopRTC(handlers);
  peers[0].channel.readyState = "open"; peers[0].channel.onopen();
  peers[0].channel.onmessage({ data: chunk(0, 10, 0, [1]) });
  await vi.advanceTimersByTimeAsync(15000);
  expect(handlers.onState).toHaveBeenLastCalledWith("failed");
  const goHandlers = options();
  const go = await connectDesktopRTC(goHandlers);
  const event = { data: new Uint8Array([1, 2, 3]).buffer };
  peers[1].channel.onmessage(event);
  expect(goHandlers.onMessage).toHaveBeenCalledWith(event);
  go.close();
});
