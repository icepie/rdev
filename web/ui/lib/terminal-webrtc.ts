export interface TerminalRTC {
  signal(message: Record<string, unknown>): void;
  send(data: Uint8Array): void;
  ready(): void;
  close(): void;
}

// Input is never replayed once submitted to RTC: its delivery is uncertain on
// failure. Only unsent bytes survive the device's relay-recovery barrier.
export function createTerminalRTC(
  ws: WebSocket,
  sessionId: string,
  iceServers: RTCIceServer[],
  output: (data: ArrayBuffer) => void,
  warning: (message: string) => void,
): TerminalRTC {
  let peer: RTCPeerConnection | undefined;
  let channel: RTCDataChannel | undefined;
  let state: "negotiating" | "direct" | "recovering" | "relay" | "closed" = "negotiating";
  let timer: ReturnType<typeof setTimeout> | undefined;
  let flushTimer: ReturnType<typeof setTimeout> | undefined;
  let sessionReady = false;
  let queuedBytes = 0;
  const queue: Uint8Array[] = [];
  const pending: RTCIceCandidateInit[] = [];
  let signals = Promise.resolve();
  const sendControl = (value: unknown) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(value));
  };
  const signal = (rtcType: string, rtcData?: unknown) => sendControl({ op: "rtc_signal", sessionId, rtcType, rtcData });
  function disposePeer() {
    clearTimeout(timer);
    timer = undefined;
    if (channel) {
      channel.onopen = channel.onclose = channel.onerror = channel.onmessage = channel.onbufferedamountlow = null;
      channel.close();
      channel = undefined;
    }
    if (peer) {
      peer.onicecandidate = peer.onconnectionstatechange = null;
      peer.close();
      peer = undefined;
    }
  }
  function fail(message: string) {
    warning(message);
    close();
    ws.close();
  }
  function recover() {
    if (state === "closed" || state === "relay" || state === "recovering") return;
    state = "recovering";
    disposePeer();
    sendControl({ op: "rtc_fallback", sessionId });
    timer = setTimeout(() => fail("Terminal relay recovery timed out; reconnect required."), 8000);
  }
  function flush() {
    clearTimeout(flushTimer);
    flushTimer = undefined;
    if (!sessionReady || state === "closed" || state === "recovering" || state === "negotiating") return;
    while (queue.length) {
      const target = state === "direct" ? channel : ws;
      if (!target || (state === "direct" ? channel?.readyState !== "open" : ws.readyState !== WebSocket.OPEN)) { recover(); return; }
      if (target.bufferedAmount >= 256 * 1024) {
        flushTimer = setTimeout(flush, 20);
        return;
      }
      const data = queue[0];
      // Remove before send. Even a thrown send must not replay an uncertain command.
      queue.shift();
      queuedBytes -= data.byteLength;
      try {
        if (state === "direct") channel!.send(data as Uint8Array<ArrayBuffer>);
        else ws.send(data as Uint8Array<ArrayBuffer>);
      } catch { recover(); return; }
    }
  }
  function close() {
    state = "closed";
    disposePeer();
    clearTimeout(flushTimer);
    queue.length = 0;
    queuedBytes = 0;
  }
  try {
    peer = new RTCPeerConnection({ iceServers });
    const current = peer;
    channel = current.createDataChannel("rdev-terminal", { ordered: true });
    const dc = channel;
    dc.binaryType = "arraybuffer";
    dc.bufferedAmountLowThreshold = 128 * 1024;
    dc.onbufferedamountlow = flush;
    dc.onopen = () => {
      if (channel !== dc || state !== "negotiating") return;
      state = "direct";
      sendControl({ op: "rtc_ready", sessionId });
      flush();
    };
    dc.onmessage = (event) => {
      if (channel === dc && event.data instanceof ArrayBuffer) output(event.data);
    };
    dc.onclose = dc.onerror = recover;
    current.onicecandidate = (event) => {
      if (peer === current && event.candidate) signal("candidate", event.candidate.toJSON());
    };
    current.onconnectionstatechange = () => {
      if (peer === current && ["failed", "closed", "disconnected"].includes(current.connectionState)) recover();
    };
    timer = setTimeout(recover, 5000);
    void (async () => {
      const offer = await current.createOffer();
      if (peer !== current) return;
      await current.setLocalDescription(offer);
      if (peer === current) signal("offer", current.localDescription);
    })().catch(recover);
  } catch { recover(); }

  return {
    signal(message) {
      if (state === "closed") return;
      if (message.op === "rtc_fallback" && message.sessionId === sessionId) {
        disposePeer();
        state = "relay";
        if (sessionReady) warning("Direct connection interrupted; in-flight terminal bytes may be missing. Continuing the same shell via relay.");
        flush();
        return;
      }
      if (message.op !== "rtc_signal") return;
      if (message.rtcType === "close") { recover(); return; }
      if (!peer || state === "recovering" || state === "relay") return;
      const current = peer;
      signals = signals.then(async () => {
        if (peer !== current) return;
        if (message.rtcType === "answer") {
          if (current.remoteDescription) return;
          await current.setRemoteDescription(message.rtcData as RTCSessionDescriptionInit);
          for (const candidate of pending.splice(0)) await current.addIceCandidate(candidate);
        } else if (message.rtcType === "candidate") {
          if (current.remoteDescription) await current.addIceCandidate(message.rtcData as RTCIceCandidateInit);
          else if (pending.length < 128) pending.push(message.rtcData as RTCIceCandidateInit);
          else recover();
        }
      }).catch(recover);
    },
    send(data) {
      if (state === "closed") return;
      if (queuedBytes + data.byteLength > 1024 * 1024) { fail("Terminal input queue is full; reconnect required. Input was not replayed."); return; }
      for (let i = 0; i < data.byteLength; i += 16384) queue.push(data.slice(i, i + 16384));
      queuedBytes += data.byteLength;
      flush();
    },
    ready() { sessionReady = true; if (state !== "recovering") clearTimeout(timer); flush(); },
    close,
  };
}
