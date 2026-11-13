import { wsURL } from "./api";

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

/** Establishes a browser-device data channel; the existing desktop socket is the fallback. */
export function connectDesktopRTC(options: {
  device: string;
  session: string;
  password?: string;
  onMessage: (event: MessageEvent) => void;
  onState: (state: DesktopRTCState) => void;
}): DesktopRTCConnection {
  const params = new URLSearchParams({ device: options.device, session: options.session });
  if (options.password) params.set("password", options.password);
  const signaling = new WebSocket(wsURL(`/desktop/rtc?${params.toString()}`));
  const peer = new RTCPeerConnection({
    iceServers: [{ urls: "stun:stun.l.google.com:19302" }],
  });
  const dataChannel = peer.createDataChannel("rdev-desktop", { ordered: true });
  const pendingSignals: DesktopRTCSignal[] = [];
  const pendingCandidates: RTCIceCandidateInit[] = [];
  let remoteDescriptionSet = false;
  let closed = false;
  let connectedTimer: ReturnType<typeof setTimeout> | undefined;

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
  const fail = () => {
    if (!closed) options.onState("failed");
  };

  dataChannel.binaryType = "arraybuffer";
  dataChannel.onopen = () => {
    clearTimeout(connectedTimer);
    options.onState("open");
  };
  dataChannel.onclose = () => {
    if (!closed) options.onState("closed");
  };
  dataChannel.onerror = fail;
  dataChannel.onmessage = options.onMessage;
  peer.onicecandidate = (event) => {
    if (event.candidate) signal("candidate", event.candidate.toJSON());
  };
  peer.onconnectionstatechange = () => {
    if (peer.connectionState === "failed") fail();
    else if (peer.connectionState === "closed" && !closed) options.onState("closed");
  };
  signaling.onopen = async () => {
    flushSignals();
    try {
      const offer = await peer.createOffer();
      await peer.setLocalDescription(offer);
      signal("offer", offer);
    } catch {
      fail();
    }
  };
  signaling.onmessage = async (event) => {
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
    if (!closed && dataChannel.readyState !== "open") fail();
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
      if (closed) return;
      closed = true;
      clearTimeout(connectedTimer);
      signal("close");
      dataChannel.close();
      peer.close();
      if (signaling.readyState === WebSocket.OPEN || signaling.readyState === WebSocket.CONNECTING) signaling.close(1000);
      options.onState("closed");
    },
  };
}
