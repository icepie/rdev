import { useCallback, useRef, useState } from "react";
import { wsURL } from "../../lib/api";
import { asString, type Status } from "./protocol";
import { createStreamBatch, type StreamBatch } from "./stream-batch";

/* Page-owned reactions to socket lifecycle events. Kept behind a ref so the
 * hook's socket callbacks always see the current page closures without
 * rebuilding `connect` (which owns the backoff timers). */
export interface AgentConnectionHandlers {
  /** Every parsed frame for this connection, in arrival order. */
  message: (msg: Record<string, unknown>, deviceId: string) => void;
  /** Settle page-owned pending work before a replacement socket opens. */
  replace: (reason: string) => void;
  /** The socket closed; the page tears down its connection-scoped state. */
  closed: (event: CloseEvent, deviceId: string, rejected: boolean) => void;
}

export interface AgentConnectionOptions {
  t: (key: string, vars?: Record<string, string | number>) => string;
  handlers: { current: AgentConnectionHandlers };
}

/* One agent WebSocket per page: open/close, frame parsing, ping, and the
 * exponential-backoff reconnect that follows an unexpected close. The
 * connection states live here because the socket transitions change them. */
export function useAgentConnection({ t, handlers }: AgentConnectionOptions) {
  const [status, setStatus] = useState<Status>("disconnected");
  const [authed, setAuthed] = useState(false);
  const [authError, setAuthError] = useState("");
  const [reconnecting, setReconnecting] = useState(false);

  const wsRef = useRef<WebSocket | null>(null);
  const streamBatchRef = useRef<StreamBatch | null>(null);
  /* Mirror of `status` for socket callbacks and page send guards. */
  const statusRef = useRef<Status>(status);
  statusRef.current = status;
  const reconnectTimer = useRef<number | null>(null);
  const reconnectAttempt = useRef(0);
  const pingInterval = useRef<number | null>(null);
  const tRef = useRef(t);
  tRef.current = t;

  const clearReconnectTimer = useCallback(() => {
    if (reconnectTimer.current !== null) {
      window.clearTimeout(reconnectTimer.current);
      reconnectTimer.current = null;
    }
  }, []);

  const clearPing = useCallback(() => {
    if (pingInterval.current !== null) {
      window.clearInterval(pingInterval.current);
      pingInterval.current = null;
    }
  }, []);

  const startPing = useCallback(() => {
    clearPing();
    pingInterval.current = window.setInterval(() => {
      if (wsRef.current?.readyState === WebSocket.OPEN) {
        wsRef.current.send(JSON.stringify({ op: "ping" }));
      }
    }, 25000);
  }, [clearPing]);

  /* Auto-reconnect with exponential backoff: 1s → 2s → 4s → 8s … cap 30s.
   * Reset on successful auth_ok (see noteAuthenticated) or manual connect.
   * Uses connectRef to break the connect ↔ scheduleReconnect cycle. */
  const connectRef = useRef<(deviceId: string, preserveBackoff?: boolean) => void>(() => {});
  const scheduleReconnect = useCallback((deviceId: string) => {
    if (reconnectTimer.current !== null) return;
    const delay = Math.min(1000 * 2 ** reconnectAttempt.current, 30000);
    reconnectAttempt.current += 1;
    setReconnecting(true);
    reconnectTimer.current = window.setTimeout(() => {
      reconnectTimer.current = null;
      connectRef.current(deviceId, true);
    }, delay);
  }, []);

  /* Drop the socket without its onclose handler: the caller settles the
   * connection-scoped page state itself before replacing or unmounting. */
  const closeSocket = useCallback(() => {
    if (!wsRef.current) return;
    streamBatchRef.current?.dispose();
    streamBatchRef.current = null;
    wsRef.current.onclose = null;
    wsRef.current.close();
    wsRef.current = null;
  }, []);

  const connect = useCallback((deviceId: string, preserveBackoff = false) => {
    if (!deviceId) return;
    clearReconnectTimer();
    if (!preserveBackoff) {
      reconnectAttempt.current = 0;
      setReconnecting(false);
    }
    clearPing();
    // Replacing a socket disables its onclose handler, so settle the page's
    // pending operations here as well as on an unexpected disconnect.
    handlers.current.replace("connection replaced");
    closeSocket();
    setAuthed(false);
    setStatus("connecting");
    setAuthError("");
    // Per-device state (transcript, threads, models, cwd…) is NOT cleared
    // here: a reconnect must keep the partial transcript visible until the
    // next auth_ok replaces it with the server-side truth.

    const ws = new WebSocket(wsURL("/agent?device=" + encodeURIComponent(deviceId)));
    wsRef.current = ws;
    const streamBatch = createStreamBatch(msg => {
      if (wsRef.current === ws) handlers.current.message(msg, deviceId);
    });
    streamBatchRef.current = streamBatch;
    // These belong to this socket, not the previous connection's React state.
    let authenticated = false;
    let rejected = false;
    ws.onmessage = (evt) => {
      if (wsRef.current !== ws) return;
      let msg: Record<string, unknown>;
      try {
        msg = JSON.parse(String(evt.data));
      } catch {
        return;
      }
      if (msg.op === "auth_ok") authenticated = true;
      if (msg.op === "error" && !authenticated && !asString(msg.thread)) {
        // Setup failures arrive as an error followed by an empty close reason;
        // misconfiguration may leave the socket open. Neither is a network retry.
        rejected = true;
        setAuthed(false);
        setStatus("disconnected");
        setReconnecting(false);
        setAuthError(msg.code === "offline_admin_required" ? tRef.current("agent.offlineAdminRequired") : asString(msg.message) || tRef.current("common.error"));
        clearPing();
        ws.close();
        return;
      }
      streamBatch.push(msg);
    };
    ws.onclose = (event) => {
      if (wsRef.current !== ws) return;
      streamBatch.flush();
      streamBatch.dispose();
      streamBatchRef.current = null;
      clearPing();
      handlers.current.closed(event, deviceId, rejected);
      setAuthed(false);
      setStatus("disconnected");
      if (rejected) return;
      // A clean 1000 close with a reason is the server rejecting the
      // session at open time (invalid configuration). Offline devices use
      // the retryable 1013 close code; for permanent failures surface the
      // reason and stop.
      if (event.code === 1000 && event.reason) {
        setReconnecting(false);
        setAuthError(event.reason);
        return;
      }
      scheduleReconnect(deviceId);
    };
    ws.onerror = () => {
      if (wsRef.current !== ws) return;
      if (statusRef.current !== "disconnected") {
        setStatus("disconnected");
      }
    };
  }, [clearPing, clearReconnectTimer, closeSocket, scheduleReconnect]);
  connectRef.current = connect;

  /* auth_ok arrived: the server accepted the session, so the backoff cycle
   * is over and the keepalive starts. */
  const noteAuthenticated = useCallback(() => {
    reconnectAttempt.current = 0;
    setReconnecting(false);
    startPing();
  }, [startPing]);

  /* Manual teardown: cancel timers/ping and close the socket. */
  const disconnect = useCallback((resetBackoff = true) => {
    clearReconnectTimer();
    clearPing();
    closeSocket();
    if (resetBackoff) {
      reconnectAttempt.current = 0;
      setReconnecting(false);
    }
  }, [clearPing, clearReconnectTimer, closeSocket]);

  return {
    wsRef,
    status,
    setStatus,
    authed,
    setAuthed,
    authError,
    setAuthError,
    reconnecting,
    setReconnecting,
    statusRef,
    connect,
    disconnect,
    noteAuthenticated,
  };
}
