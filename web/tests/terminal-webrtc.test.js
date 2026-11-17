import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { createTerminalRTC } from "../ui/lib/terminal-webrtc";

class Channel {
  readyState = "connecting";
  bufferedAmount = 0;
  sent = [];
  send(data) { this.sent.push([...data]); }
  close() { this.readyState = "closed"; this.onclose?.(); }
  open() { this.readyState = "open"; this.onopen?.(); }
}
class Peer {
  static all = [];
  channel = new Channel();
  candidates = [];
  constructor() { Peer.all.push(this); }
  createDataChannel() { return this.channel; }
  async createOffer() { return { type: "offer", sdp: "fixture" }; }
  async setLocalDescription(value) { this.localDescription = value; }
  async setRemoteDescription(value) { this.remoteDescription = value; }
  async addIceCandidate(value) { this.candidates.push(value); }
  close() { this.channel.close(); this.connectionState = "closed"; this.onconnectionstatechange?.(); }
}
let transports;
beforeEach(() => {
  vi.useFakeTimers();
  vi.stubGlobal("WebSocket", { OPEN: 1 });
  vi.stubGlobal("RTCPeerConnection", Peer);
  Peer.all = [];
  transports = [];
});
afterEach(() => { for (const transport of transports) transport.close(); vi.useRealTimers(); vi.unstubAllGlobals(); });
function setup() {
  const ws = { readyState: 1, bufferedAmount: 0, send: vi.fn(), close: vi.fn() };
  const warn = vi.fn();
  const rtc = createTerminalRTC(ws, "session", [], vi.fn(), warn);
  transports.push(rtc);
  return { ws, warn, rtc, peer: Peer.all.at(-1) };
}
const binaries = ws => ws.send.mock.calls.map(([data]) => data).filter(data => typeof data !== "string").map(data => [...data]);

it("waits for PTY readiness and flushes unsent bytes only after fallback barrier without replay", async () => {
  const { ws, warn, rtc, peer } = setup();
  peer.channel.open();
  rtc.send(new Uint8Array([1]));
  expect(peer.channel.sent).toEqual([]);
  rtc.ready();
  expect(peer.channel.sent).toEqual([[1]]);
  peer.channel.bufferedAmount = 256 * 1024;
  rtc.send(new Uint8Array([2, 3]));
  peer.channel.close();
  expect(binaries(ws)).toEqual([]);
  await vi.advanceTimersByTimeAsync(100);
  expect(binaries(ws)).toEqual([]);
  rtc.signal({ op: "rtc_fallback", sessionId: "foreign" });
  expect(binaries(ws)).toEqual([]);
  rtc.signal({ op: "rtc_fallback", sessionId: "session" });
  expect(binaries(ws)).toEqual([[2, 3]]);
  expect(warn).toHaveBeenCalledWith(expect.stringContaining("in-flight"));
});

it("stages remote candidates until answer and disposes pending negotiation without a late offer", async () => {
  const { ws, rtc, peer } = setup();
  rtc.signal({ op: "rtc_signal", rtcType: "candidate", rtcData: { candidate: "early" } });
  await vi.advanceTimersByTimeAsync(0);
  expect(peer.candidates).toEqual([]);
  rtc.signal({ op: "rtc_signal", rtcType: "answer", rtcData: { type: "answer", sdp: "fixture" } });
  await vi.advanceTimersByTimeAsync(0);
  expect(peer.candidates).toEqual([{ candidate: "early" }]);
  rtc.close();
  ws.send.mockClear();
  await vi.advanceTimersByTimeAsync(9000);
  expect(ws.send).not.toHaveBeenCalled();
});

it("fails visibly instead of growing the input queue beyond its bound", () => {
  const { ws, warn, rtc, peer } = setup();
  peer.channel.open(); rtc.ready();
  peer.channel.bufferedAmount = 256 * 1024;
  rtc.send(new Uint8Array(1024 * 1024));
  rtc.send(new Uint8Array([1]));
  expect(peer.channel.sent).toEqual([]);
  expect(warn).toHaveBeenCalledWith(expect.stringContaining("queue is full"));
  expect(ws.close).toHaveBeenCalledOnce();
});

it("falls back when WebRTC is unavailable, retaining input until shell readiness", () => {
  vi.stubGlobal("RTCPeerConnection", undefined);
  const { ws, rtc } = setup();
  rtc.send(new Uint8Array([7]));
  rtc.signal({ op: "rtc_fallback", sessionId: "session" });
  expect(binaries(ws)).toEqual([]);
  rtc.ready();
  expect(binaries(ws)).toEqual([[7]]);
});
