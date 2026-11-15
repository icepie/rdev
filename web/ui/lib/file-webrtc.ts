import { apiJSON } from "./api";
import { encodeFrame } from "./file-transfer-frame";

export const FILE_RTC_CHUNK = 16 * 1024;
export const FILE_RTC_WINDOW = 512 * 1024;
export interface FileRTCDiagnostics {
  transport: "negotiating" | "direct" | "relay";
  reason?: string;
  rttMs?: number;
  bufferedBytes?: number;
  negotiationMs?: number;
}
const HIGH_WATER = 512 * 1024;
const NEGOTIATION_MS = 5000;
const STALL_MS = 15000;
const MAX_CANDIDATES = 128;

export type FileRTCMessage = Record<string, unknown>;
export type FileRTCAttemptOptions = {
  taskId: string;
  wireId: string;
  direction: "upload" | "download";
  boundedDownload?: boolean;
  isCurrent(): boolean;
  send(message: FileRTCMessage): void;
  onBinary(frame: ArrayBuffer): void;
  onFallback(): void;
  onDiagnostics?(value: FileRTCDiagnostics): void;
};
type Message = FileRTCMessage;
type AttemptOptions = FileRTCAttemptOptions;

type Attempt = {
  options: AttemptOptions;
  peer: RTCPeerConnection;
  channel: RTCDataChannel;
  answered: boolean;
  started: boolean;
  closed: boolean;
  sentOffsets: number[];
  sentEnd?: number;
  rangeAck?: boolean;
  rangeEnd?: number;
  sending?: Promise<void>;
  created: number;
  statsTimer?: number;
  remoteSet: boolean;
  localCandidates: RTCIceCandidateInit[];
  remoteCandidates: RTCIceCandidateInit[];
  timer?: number;
  release: Set<() => void>;
};

export interface FileRTCTransfers {
  prepare(options: AttemptOptions): Promise<Message>;
  started(wireId: string): void;
  /** True means signaling was consumed; transfer terminals belong to the owner. */
  text(message: Message): boolean;
  direct(wireId: string | undefined): boolean;
  continueDownload(wireId: string | undefined, offset: number): boolean;
  send(wireId: string, offset: number, payload: ArrayBuffer, signal: AbortSignal): Promise<void>;
  fail(wireId: string): void;
  close(wireId: string | undefined, notify?: boolean): void;
  clear(): void;
}

/** RTC uses the same big-endian offset header, but never carries an identity.
 * WS encode/decode remain strict; only the bound peer can supply the wire ID. */
export function encodeFileRTCFrame(typ: number, offset: number, payload?: ArrayBuffer): ArrayBuffer {
  if (!Number.isSafeInteger(offset) || offset < 0 || (payload?.byteLength || 0) > FILE_RTC_CHUNK)
    throw new Error("Invalid file RTC frame");
  const frame = new ArrayBuffer(10 + (payload?.byteLength || 0));
  new Uint8Array(frame)[0] = typ;
  new DataView(frame).setBigUint64(2, BigInt(offset));
  if (payload) new Uint8Array(frame, 10).set(new Uint8Array(payload));
  return frame;
}

export function remapFileRTCFrame(frame: ArrayBuffer, wireId: string): ArrayBuffer | null {
  if (frame.byteLength < 10 || frame.byteLength > FILE_RTC_CHUNK + 10 || new Uint8Array(frame)[1] !== 0) return null;
  const offset = new DataView(frame).getBigUint64(2);
  if (offset > BigInt(Number.MAX_SAFE_INTEGER)) return null;
  return encodeFrame(new Uint8Array(frame)[0], wireId, Number(offset), frame.slice(10));
}

/** Each failed direct task is pinned to relay for subsequent automatic resumes.
 * No data frame is ever silently resent through WS. The owner cancels the old
 * wire task and starts a fresh authorized attempt from its confirmed offset. */
export function createFileRTCTransfers(): FileRTCTransfers {
  const attempts = new Map<string, Attempt>();
  const relayTasks = new Set<string>();
  const preparing = new Map<string, AbortController>();

  const close = (wireId: string | undefined, notify = true) => {
    if (!wireId) return;
    preparing.get(wireId)?.abort();
    preparing.delete(wireId);
    const attempt = attempts.get(wireId);
    if (!attempt) return;
    attempts.delete(wireId);
    attempt.closed = true;
    window.clearTimeout(attempt.timer);
    window.clearTimeout(attempt.statsTimer);
    for (const release of attempt.release) release();
    attempt.release.clear();
    if (notify && attempt.started) attempt.options.send({ op: "rtc_signal", taskId: wireId, rtcType: "close" });
    attempt.channel.close();
    attempt.peer.close();
  };
  const fail = (wireId: string, reason = "connection-failed") => {
    const attempt = attempts.get(wireId);
    if (!attempt || attempt.closed) return;
    relayTasks.add(attempt.options.taskId);
    attempt.options.onDiagnostics?.({ transport: "relay", reason });
    const retry = attempt.started && attempt.options.isCurrent();
    close(wireId, false);
    if (retry) attempt.options.onFallback();
  };
  const arm = (attempt: Attempt, milliseconds: number) => {
    window.clearTimeout(attempt.timer);
    attempt.timer = window.setTimeout(() => fail(attempt.options.wireId, attempt.channel.readyState === "open" ? "stalled" : "negotiation-timeout"), milliseconds);
  };
  const receiveSignal = async (attempt: Attempt, message: Message) => {
    try {
      if (message.rtcType === "close") { fail(attempt.options.wireId); return; }
      if (message.rtcType === "answer") {
        attempt.answered = true;
        const answer = message.rtcData as RTCSessionDescriptionInit & { rangeAck?: boolean };
        attempt.rangeAck = attempt.options.direction === "download" && !!attempt.options.boundedDownload && answer?.rangeAck === true;
        await attempt.peer.setRemoteDescription(message.rtcData as RTCSessionDescriptionInit);
        if (attempt.closed) return;
        attempt.remoteSet = true;
        for (const candidate of attempt.remoteCandidates.splice(0)) {
          if (attempt.closed) return;
          await attempt.peer.addIceCandidate(candidate);
        }
      } else if (message.rtcType === "candidate") {
        if (attempt.remoteSet) await attempt.peer.addIceCandidate(message.rtcData as RTCIceCandidateInit);
        else if (attempt.remoteCandidates.length < MAX_CANDIDATES) attempt.remoteCandidates.push(message.rtcData as RTCIceCandidateInit);
        else fail(attempt.options.wireId);
      }
    } catch { fail(attempt.options.wireId); }
  };

  const sample = async (attempt: Attempt) => {
    if (attempt.closed || !attempt.options.isCurrent()) return;
    let rttMs: number | undefined;
    try {
      const stats = await attempt.peer.getStats();
      stats.forEach(report => {
        if (report.type === "candidate-pair" && report.state === "succeeded" && report.nominated && typeof report.currentRoundTripTime === "number") rttMs = report.currentRoundTripTime * 1000;
      });
    } catch { /* Stats are optional and never affect transfer correctness. */ }
    if (attempt.closed || !attempt.options.isCurrent()) return;
    attempt.options.onDiagnostics?.({ transport: "direct", rttMs, bufferedBytes: attempt.channel.bufferedAmount });
    attempt.statsTimer = window.setTimeout(() => { void sample(attempt); }, 2000);
  };
  return {
    async prepare(options) {
      if (!options.isCurrent()) return {};
      if (relayTasks.has(options.taskId) || typeof RTCPeerConnection !== "function") {
        options.onDiagnostics?.({ transport: "relay", ...(relayTasks.has(options.taskId) ? {} : { reason: "browser-unsupported" }) });
        return {};
      }
      options.onDiagnostics?.({ transport: "negotiating" });
      const controller = new AbortController();
      preparing.set(options.wireId, controller);
      const deadline = Promise.withResolvers<Message>();
      const timer = window.setTimeout(() => {
        options.onDiagnostics?.({ transport: "relay", reason: "negotiation-timeout" });
        close(options.wireId, false);
        deadline.resolve({});
      }, NEGOTIATION_MS);
      const prepare = async (): Promise<Message> => {
        try {
          const config = await apiJSON<{ enabled?: boolean; iceServers?: RTCIceServer[] }>("/api/desktop/rtc", { cache: "no-store", signal: controller.signal });
          if (controller.signal.aborted || !options.isCurrent()) return {};
          if (!config?.enabled) { options.onDiagnostics?.({ transport: "relay", reason: "disabled" }); return {}; }
          const iceServers = config.iceServers === undefined ? [{ urls: "stun:stun.l.google.com:19302" }] : config.iceServers;
          const peer = new RTCPeerConnection({ iceServers });
          let channel: RTCDataChannel;
          try { channel = peer.createDataChannel("rdev-file", { ordered: true }); }
          catch (error) { peer.close(); throw error; }
          const attempt: Attempt = { options, peer, channel, answered: false, started: false, closed: false, sentOffsets: [], created: performance.now(), remoteSet: false, localCandidates: [], remoteCandidates: [], release: new Set() };
          attempts.set(options.wireId, attempt);
          channel.binaryType = "arraybuffer";
          channel.bufferedAmountLowThreshold = HIGH_WATER / 2;
          peer.onicecandidate = event => {
            if (!event.candidate || attempt.closed) return;
            const candidate = event.candidate.toJSON();
            if (attempt.started) options.send({ op: "rtc_signal", taskId: options.wireId, rtcType: "candidate", rtcData: candidate });
            else if (attempt.localCandidates.length < MAX_CANDIDATES) attempt.localCandidates.push(candidate);
          };
          channel.onopen = () => {
            if (attempt.closed) return;
            arm(attempt, STALL_MS);
            options.onDiagnostics?.({ transport: "direct", negotiationMs: performance.now() - attempt.created });
            void sample(attempt);
          };
          channel.onclose = channel.onerror = () => fail(options.wireId);
          peer.onconnectionstatechange = () => {
            if (peer.connectionState === "failed" || peer.connectionState === "closed") fail(options.wireId);
          };
          channel.onmessage = event => {
            if (attempt.closed || !options.isCurrent()) return;
            const frame = event.data instanceof ArrayBuffer ? remapFileRTCFrame(event.data, options.wireId) : null;
            const type = frame ? new Uint8Array(frame)[0] : -1;
            if (!frame || (options.direction === "upload" ? type !== 0x21 : type !== 0x22 && type !== 0x23)) {
              fail(options.wireId); return;
            }
            if (type === 0x21) {
              const offset = Number(new DataView(event.data).getBigUint64(2));
              const boundary = attempt.sentOffsets.indexOf(offset);
              if (event.data.byteLength !== 10 || boundary < 0) { fail(options.wireId, "invalid-ack"); return; }
              attempt.sentOffsets.splice(0, boundary + 1);
            }
            if (attempt.rangeEnd !== undefined) { fail(options.wireId, "invalid-ack"); return; }
            if (type === 0x23) {
              if (event.data.byteLength !== 10) { fail(options.wireId); return; }
              if (attempt.rangeAck) attempt.rangeEnd = Number(new DataView(event.data).getBigUint64(2));
            }
            arm(attempt, type === 0x23 && attempt.rangeAck ? 30000 : STALL_MS);
            options.onBinary(frame);
            // Legacy peers end after one range; negotiated peers wait for disk ACK.
            if (type === 0x23 && !attempt.rangeAck) close(options.wireId);
          };
          const offer = await peer.createOffer();
          if (attempt.closed || !options.isCurrent()) { close(options.wireId, false); return {}; }
          await peer.setLocalDescription(offer);
          if (attempt.closed || !options.isCurrent()) { close(options.wireId, false); return {}; }
          return { rtcType: "offer", rtcData: { type: offer.type, sdp: offer.sdp, ...(options.direction === "download" && options.boundedDownload ? { rangeAck: true } : {}) } };
        } catch { close(options.wireId, false); if (options.isCurrent()) options.onDiagnostics?.({ transport: "relay", reason: "setup-failed" }); return {}; }
      };
      try { return await Promise.race([prepare(), deadline.promise]); }
      finally { window.clearTimeout(timer); preparing.delete(options.wireId); }
    },
    started(wireId) {
      const attempt = attempts.get(wireId);
      if (!attempt) return;
      attempt.started = true;
      arm(attempt, NEGOTIATION_MS);
      for (const candidate of attempt.localCandidates.splice(0))
        attempt.options.send({ op: "rtc_signal", taskId: wireId, rtcType: "candidate", rtcData: candidate });
    },
    text(message) {
      const wireId = String(message.taskId || "");
      const attempt = attempts.get(wireId);
      if (message.op === "rtc_signal") {
        if (attempt && attempt.options.isCurrent()) void receiveSignal(attempt, message);
        return true;
      }
      if (!attempt) return false;
      if (message.op === "upload_ready" || message.op === "download_start") {
        // Unsupported/disabled devices immediately send ordinary relay metadata;
        // do not cancel or issue another start for that already-running request.
        if (!attempt.answered && attempt.channel.readyState !== "open") {
          relayTasks.add(attempt.options.taskId);
          attempt.options.onDiagnostics?.({ transport: "relay", reason: "device-unsupported" });
          close(wireId, false);
        } else arm(attempt, STALL_MS);
      } else if (message.op === "transfer_end") {
        // Download bytes may still be queued on the independent data channel.
        // The owner joins binary end with this authoritative terminal before close.
        if (attempt.options.direction === "upload") close(wireId);
      } else if (message.op === "transfer_error" || message.op === "error" || message.op === "canceled") close(wireId);
      return false;
    },
    direct(wireId) { return !!wireId && attempts.has(wireId); },
    continueDownload(wireId, offset) {
      const attempt = wireId ? attempts.get(wireId) : undefined;
      if (!attempt?.rangeAck || attempt.closed || !attempt.options.isCurrent()) return false;
      if (attempt.rangeEnd !== offset || attempt.channel.readyState !== "open") { fail(wireId!); return false; }
      attempt.rangeEnd = undefined;
      try {
        attempt.channel.send(encodeFileRTCFrame(0x25, offset));
        arm(attempt, STALL_MS);
        return true;
      } catch { fail(wireId!); return false; }
    },
    async send(wireId, offset, payload, signal) {
      const attempt = attempts.get(wireId);
      if (!attempt || attempt.closed || signal.aborted) throw new Error("File RTC attempt closed");
      const previous = attempt.sending;
      const sent = Promise.withResolvers<void>();
      attempt.sending = sent.promise;
      await previous;
      try {
        const channel = attempt.channel;
        while (!attempt.closed && !signal.aborted && (channel.readyState !== "open" || channel.bufferedAmount + payload.byteLength + 10 > HIGH_WATER)) {
          const wait = Promise.withResolvers<void>();
          const release = () => wait.resolve();
          attempt.release.add(release);
          channel.addEventListener("open", release);
          channel.addEventListener("bufferedamountlow", release);
          signal.addEventListener("abort", release, { once: true });
          await wait.promise;
          attempt.release.delete(release);
          channel.removeEventListener("open", release);
          channel.removeEventListener("bufferedamountlow", release);
          signal.removeEventListener("abort", release);
        }
        if (attempt.closed || signal.aborted || channel.readyState !== "open" || channel.bufferedAmount + payload.byteLength + 10 > HIGH_WATER)
          throw new Error("File RTC attempt closed");
        if (attempt.sentOffsets.length >= FILE_RTC_WINDOW / FILE_RTC_CHUNK || (attempt.sentEnd !== undefined && attempt.sentEnd !== offset)) throw new Error("Invalid RTC upload window");
        attempt.sentEnd = offset + payload.byteLength;
        attempt.sentOffsets.push(attempt.sentEnd);
        try { channel.send(encodeFileRTCFrame(0x20, offset, payload)); }
        catch (error) { fail(wireId); throw error; }
      } finally { sent.resolve(); }
    },
    fail,
    close,
    clear() {
      for (const wireId of preparing.keys()) close(wireId, false);
      for (const [wireId, attempt] of attempts) {
        if (attempt.started) relayTasks.add(attempt.options.taskId);
        close(wireId, false);
      }
    },
  };
}
