import { apiJSON, wsURL } from "./api";

export type DesktopRTCSignal = {
  op: "rtc_signal";
  session?: string;
  rtcType: "offer" | "answer" | "candidate" | "close";
  rtcData?: unknown;
};

export type DesktopRTCState = "connecting" | "open" | "failed" | "closed";

export interface DesktopRTCConnection {
  readonly dataChannel: RTCDataChannel;
  send(data: string | ArrayBuffer | Blob): boolean;
  close(): void;
}

/** Video/config may be direct; all human controls retain the server relay. */
export function sendGPUDesktopMessage(ws: WebSocket | null, rtc: DesktopRTCConnection | null, message: unknown, rtcSource?: number): void {
  let directVideo = rtc?.dataChannel.readyState === "open";
  if (message && typeof message === "object" && "Config" in message) {
    const config = message as { Config: Record<string, unknown> };
    if (rtc && directVideo) {
      const videoConfig = { ...config.Config };
      if (rtcSource !== undefined) videoConfig.capturable_id = rtcSource;
      delete videoConfig.pointer_backend;
      delete videoConfig.keyboard_backend;
      directVideo = rtc.send(JSON.stringify({ Config: videoConfig }));
    }
    if (ws?.readyState === WebSocket.OPEN)
      ws.send(JSON.stringify(directVideo ? { Config: { ...config.Config, frame_rate: 0 } } : config));
  } else if (rtc && directVideo && (message === "ResumeVideo" || message === "PauseVideo")) {
    const sent = rtc.send(JSON.stringify(message));
    if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify(sent ? "PauseVideo" : message));
  } else if (ws?.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify(message));
  }
}

interface DesktopRTCOptions {
  device: string;
  session: string;
  password?: string;
  supported: boolean;
  gpuFraming?: boolean;
  isCurrent: () => boolean;
  onMessage: (event: MessageEvent) => void;
  onState: (state: DesktopRTCState) => void;
}

/** No peer or signaling socket is created until both device and server opt in. */
export async function connectDesktopRTC(options: DesktopRTCOptions): Promise<DesktopRTCConnection | null> {
  if (!options.supported || typeof RTCPeerConnection !== "function" || !options.isCurrent()) return null;
  try {
    const config = await apiJSON<{ enabled: boolean }>("/api/desktop/rtc", { cache: "no-store" });
    if (config?.enabled !== true || !options.isCurrent()) return null;
    return createDesktopRTC(options);
  } catch {
    return null; // Old servers, auth failures and unavailable WebRTC keep the relay.
  }
}

function createDesktopRTC(options: DesktopRTCOptions): DesktopRTCConnection {
  const params = new URLSearchParams({ device: options.device, session: options.session });
  if (options.password) params.set("password", options.password);
  const peer = new RTCPeerConnection({
    iceServers: [{ urls: "stun:stun.l.google.com:19302" }],
  });
  let dataChannel: RTCDataChannel;
  let signaling: WebSocket;
  try {
    dataChannel = peer.createDataChannel("rdev-desktop", { ordered: true });
    signaling = new WebSocket(wsURL(`/desktop/rtc?${params.toString()}`));
  } catch (error) {
    peer.close();
    throw error;
  }
  const pendingSignals: DesktopRTCSignal[] = [];
  const pendingCandidates: RTCIceCandidateInit[] = [];
  let remoteDescriptionSet = false;
  let closed = false;
  let connectedTimer: ReturnType<typeof setTimeout> | undefined;
  let frameTimer: ReturnType<typeof setTimeout> | undefined;
  let frame: Uint8Array | null = null;
  let frameType = 0;
  let frameOffset = 0;
  const textDecoder = new TextDecoder("utf-8", { fatal: true });

  const signal = (rtcType: DesktopRTCSignal["rtcType"], rtcData?: unknown) => {
    const message: DesktopRTCSignal = { op: "rtc_signal", session: options.session, rtcType, rtcData };
    if (signaling.readyState === WebSocket.OPEN) signaling.send(JSON.stringify(message));
    else if (signaling.readyState === WebSocket.CONNECTING) pendingSignals.push(message);
  };
  const flushSignals = () => {
    while (pendingSignals.length > 0 && signaling.readyState === WebSocket.OPEN) {
      signaling.send(JSON.stringify(pendingSignals.shift()));
    }
  };
  const terminate = (state: "failed" | "closed") => {
    if (closed) return;
    closed = true;
    clearTimeout(connectedTimer);
    clearTimeout(frameTimer);
    frame = null;
    signal("close");
    dataChannel.close();
    peer.close();
    if (signaling.readyState === WebSocket.OPEN || signaling.readyState === WebSocket.CONNECTING) signaling.close(1000);
    options.onState(state);
  };
  const fail = () => terminate("failed");

  dataChannel.binaryType = "arraybuffer";
  dataChannel.onopen = () => {
    clearTimeout(connectedTimer);
    if (!closed) options.onState("open");
  };
  dataChannel.onclose = () => {
    terminate("closed");
  };
  dataChannel.onerror = fail;
  dataChannel.onmessage = (event) => {
    if (closed) return;
    if (!options.gpuFraming) {
      options.onMessage(event);
      return;
    }
    let data: string | ArrayBuffer;
    try {
      if (!(event.data instanceof ArrayBuffer) || event.data.byteLength < 13 || event.data.byteLength > 13 + 16 * 1024)
        throw new Error("invalid GPU RTC envelope");
      const view = new DataView(event.data);
      const kind = view.getUint8(4);
      const total = view.getUint32(5);
      const offset = view.getUint32(9);
      const length = view.byteLength - 13;
      if (view.getUint32(0) !== 0x52444731 || kind > 1 || total > 16 * 1024 * 1024 || offset + length > total || (length === 0 && total !== 0))
        throw new Error("invalid GPU RTC chunk");
      if (frame ? (kind !== frameType || total !== frame.byteLength || offset !== frameOffset) : offset !== 0)
        throw new Error("out-of-order GPU RTC chunk");
      let complete: ArrayBuffer;
      if (!frame && length === total) {
        complete = event.data.slice(13);
      } else {
        if (!frame) {
          frame = new Uint8Array(total);
          frameType = kind;
          frameOffset = 0;
          frameTimer = setTimeout(fail, 15000);
        }
        frame.set(new Uint8Array(event.data, 13), offset);
        frameOffset += length;
        if (frameOffset !== total) return;
        complete = frame.buffer as ArrayBuffer;
        frame = null;
        clearTimeout(frameTimer);
      }
      data = kind === 1 ? textDecoder.decode(complete) : complete;
    } catch {
      fail();
      return;
    }
    options.onMessage(new MessageEvent("message", { data }));
  };
  peer.onicecandidate = (event) => {
    if (event.candidate) signal("candidate", event.candidate.toJSON());
  };
  peer.onconnectionstatechange = () => {
    if (peer.connectionState === "failed") fail();
    else if (peer.connectionState === "closed") terminate("closed");
  };
  signaling.onopen = async () => {
    if (closed) return;
    flushSignals();
    try {
      const offer = await peer.createOffer();
      if (closed) return;
      await peer.setLocalDescription(offer);
      if (closed) return;
      signal("offer", offer);
    } catch {
      fail();
    }
  };
  signaling.onmessage = async (event) => {
    if (closed) return;
    let message: DesktopRTCSignal;
    try {
      message = JSON.parse(String(event.data)) as DesktopRTCSignal;
    } catch {
      return;
    }
    if (message.op !== "rtc_signal" || (message.rtcType !== "close" && !message.rtcData)) return;
    try {
      if (message.rtcType === "answer") {
        await peer.setRemoteDescription(message.rtcData as RTCSessionDescriptionInit);
        if (closed) return;
        remoteDescriptionSet = true;
        for (const candidate of pendingCandidates.splice(0)) await peer.addIceCandidate(candidate);
      } else if (message.rtcType === "candidate") {
        const candidate = message.rtcData as RTCIceCandidateInit;
        if (remoteDescriptionSet) await peer.addIceCandidate(candidate);
        else pendingCandidates.push(candidate);
      } else if (message.rtcType === "close") {
        fail();
      }
    } catch {
      fail();
    }
  };
  signaling.onerror = fail;
  signaling.onclose = () => {
    if (!closed) fail();
  };
  connectedTimer = setTimeout(() => {
    if (dataChannel.readyState !== "open") fail();
  }, 12000);

  return {
    dataChannel,
    send(data) {
      if (closed || dataChannel.readyState !== "open") return false;
      try {
        if (typeof data === "string") dataChannel.send(data);
        else if (data instanceof Blob) dataChannel.send(data);
        else dataChannel.send(new Uint8Array(data));
        return true;
      } catch {
        fail();
        return false;
      }
    },
    close() {
      terminate("closed");
    },
  };
}
