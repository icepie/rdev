import { useRef } from "react";
import { writeOwnedClipboard } from "./clipboard-write";
import {
  base64ToBytes,
  bytesToBase64,
  clipboardFingerprint,
  clipboardItems,
  errMessage,
  type ClipCaps,
  type ClipItem,
  type ConnMode,
} from "./desktop-model";

/** Clipboard sync shared by the standalone desktop page and the in-app agent
 * desktop panel. Both poll the browser clipboard and mirror it to/from the
 * remote device; `active()` is the only gating difference — the page is always
 * active, the panel stops touching the clipboard while folded away. */
export type DesktopClipboardDeps = {
  /** False while this surface must leave the browser clipboard alone. */
  active: () => boolean;
  /** Clipboard sync mode from the current settings. */
  mode: () => string;
  connMode: () => ConnMode;
  socket: () => WebSocket | null;
  /** Sends one JSON message over the browser WebSocket. */
  send: (message: unknown) => void;
  /** Sends one GPU desktop control message. */
  gpuSend: (message: unknown) => void;
  setClipStatus: (status: string) => void;
  /** Reflects capabilities so the page can enable its sync-mode select. */
  setCaps?: (capabilities: ClipCaps | null) => void;
  t: (key: string) => string;
};

export type DesktopClipboard = {
  setCapabilities: (capabilities: ClipCaps | null) => void;
  start: () => void;
  stop: () => void;
  poll: () => void;
  handleRemote: (event: { items?: ClipItem[]; text?: string }) => void;
  /** Clears the pending remote read after a clipboard error. */
  failRemote: (message: string) => void;
};

const POLL_MS = 1000;

/** Builds the state machine once per component; all inputs are getters so the
 * instance never captures stale refs. */
function createDesktopClipboard(deps: DesktopClipboardDeps): DesktopClipboard {
  let caps: ClipCaps | null = null;
  let generation = 0;
  let pollTimer: number | undefined;
  let readPending = false;
  let localReadPending = false;
  let remoteWritePending = false;
  let lastLocalFp: string | null = null;
  let lastRemoteFp: string | null = null;
  let pendingRemoteFp: string | null = null;

  const syncsRemoteToLocal = () =>
    (deps.mode() === "remote-to-local" || deps.mode() === "bidirectional") &&
    caps?.read !== false;

  const syncsLocalToRemote = () =>
    (deps.mode() === "local-to-remote" || deps.mode() === "bidirectional") &&
    caps?.write !== false;

  const stop = () => {
    generation++;
    remoteWritePending = false;
    clearInterval(pollTimer);
    pollTimer = undefined;
    readPending = false;
    localReadPending = false;
  };

  const requestRemoteClipboard = () => {
    if (!caps || !syncsRemoteToLocal() || readPending) return;
    readPending = true;
    if (deps.connMode() === "gpu") deps.gpuSend("GetClipboard");
    else if (deps.socket()?.readyState === WebSocket.OPEN) deps.send({ op: "clipboard_get" });
    else readPending = false;
  };

  const readBrowserClipboardItems = async (): Promise<ClipItem[]> => {
    const allowed: Record<string, boolean> = Object.fromEntries(
      (caps?.formats || ["text/plain"]).map((format) => [format, true]),
    );
    const result: ClipItem[] = [];
    if (navigator.clipboard?.read) {
      for (const clipboardItem of await navigator.clipboard.read()) {
        for (const type of clipboardItem.types) {
          const mime = type.split(";", 1)[0].toLowerCase();
          if (!allowed[mime]) continue;
          const blob = await clipboardItem.getType(type);
          result.push({
            mime,
            data: bytesToBase64(new Uint8Array(await blob.arrayBuffer())),
          });
        }
      }
    }
    if (!result.length && navigator.clipboard?.readText && allowed["text/plain"]) {
      const text = await navigator.clipboard.readText();
      if (text)
        result.push({ mime: "text/plain", data: bytesToBase64(new TextEncoder().encode(text)) });
    }
    return result;
  };

  const writeBrowserClipboardItems = async (event: { items?: ClipItem[]; text?: string }) => {
    const items = clipboardItems(event);
    if (!items.length) throw new Error(deps.t("desktop.clipboardEmpty"));
    if (navigator.clipboard?.write && typeof ClipboardItem === "function") {
      const data: Record<string, Blob> = {};
      for (const item of items) {
        if (typeof ClipboardItem.supports !== "function" || ClipboardItem.supports(item.mime))
          data[item.mime] = new Blob([base64ToBytes(item.data) as BlobPart], { type: item.mime });
      }
      if (Object.keys(data).length) await navigator.clipboard.write([new ClipboardItem(data)]);
      else throw new Error(deps.t("desktop.clipboardFormatUnsupported"));
    } else {
      const text = items.find((item) => item.mime === "text/plain");
      if (!text || !navigator.clipboard?.writeText)
        throw new Error(deps.t("desktop.clipboardFormatUnsupported"));
      await navigator.clipboard.writeText(new TextDecoder().decode(base64ToBytes(text.data)));
    }
  };

  const syncLocalClipboard = async () => {
    if (!caps || !syncsLocalToRemote() || localReadPending || remoteWritePending) return;
    const startGen = generation;
    localReadPending = true;
    try {
      const items = await readBrowserClipboardItems();
      if (
        startGen !== generation ||
        !deps.active() ||
        !syncsLocalToRemote() ||
        remoteWritePending ||
        !items.length
      )
        return;
      const fingerprint = clipboardFingerprint(items);
      if (lastLocalFp === null) {
        lastLocalFp = fingerprint;
        if (deps.mode() === "bidirectional") return;
      } else if (fingerprint === lastLocalFp) {
        return;
      } else {
        lastLocalFp = fingerprint;
      }
      pendingRemoteFp = fingerprint;
      if (deps.connMode() === "gpu") deps.gpuSend({ SetClipboard: { items } });
      else if (deps.socket()?.readyState === WebSocket.OPEN)
        deps.send({ op: "clipboard_set", clipboardItems: items });
      deps.setClipStatus(
        `${deps.t("desktop.clipboardSyncedRemote")}（${items.map((item) => item.mime).join(", ")}）`,
      );
    } catch (err) {
      if (startGen === generation)
        deps.setClipStatus(`${deps.t("desktop.clipboardReadFailed")}：${errMessage(err)}`);
    } finally {
      if (startGen === generation) localReadPending = false;
    }
  };

  const poll = () => {
    if (!deps.active()) return;
    requestRemoteClipboard();
    void syncLocalClipboard();
  };

  const start = () => {
    stop();
    lastLocalFp = null;
    lastRemoteFp = null;
    pendingRemoteFp = null;
    if (!caps || deps.mode() === "off") return;
    poll();
    pollTimer = window.setInterval(poll, POLL_MS);
  };

  const setCapabilities = (capabilities: ClipCaps | null) => {
    caps = capabilities && capabilities.supported ? capabilities : null;
    deps.setCaps?.(caps);
    if (!caps) {
      stop();
      deps.setClipStatus("");
      return;
    }
    const formats = caps.formats || ["text/plain"];
    deps.setClipStatus(`${deps.t("desktop.clipboardAvailable")}：${formats.join(", ")}`);
    start();
  };

  const handleRemote = (event: { items?: ClipItem[]; text?: string }) => {
    readPending = false;
    const items = clipboardItems(event);
    if (!deps.active() || !syncsRemoteToLocal() || !items.length) return;
    const fingerprint = clipboardFingerprint(items);
    if (pendingRemoteFp !== null) {
      if (fingerprint === pendingRemoteFp) {
        lastRemoteFp = fingerprint;
        pendingRemoteFp = null;
      }
      return;
    }
    if (lastRemoteFp === null && deps.mode() === "bidirectional") {
      lastRemoteFp = fingerprint;
      return;
    }
    if (fingerprint === lastRemoteFp || remoteWritePending) return;
    const startGen = generation;
    const socket = deps.socket();
    const current = () =>
      startGen === generation && socket === deps.socket() && deps.active() && syncsRemoteToLocal();
    remoteWritePending = true;
    void writeOwnedClipboard(current, () => writeBrowserClipboardItems({ items }), () => {
      lastRemoteFp = fingerprint;
      lastLocalFp = fingerprint;
      deps.setClipStatus(`${deps.t("desktop.clipboardSyncedLocal")}（${items.map((item) => item.mime).join(", ")}）`);
    }, (err) => {
      deps.setClipStatus(`${deps.t("desktop.clipboardWriteFailed")}：${errMessage(err)}`);
    }).finally(() => {
      if (startGen === generation) remoteWritePending = false;
    });
  };

  const failRemote = (message: string) => {
    readPending = false;
    pendingRemoteFp = null;
    deps.setClipStatus(message);
  };

  return { setCapabilities, start, stop, poll, handleRemote, failRemote };
}

/** One state machine per surface for the lifetime of the component. */
export function useDesktopClipboard(deps: DesktopClipboardDeps): DesktopClipboard {
  const ref = useRef<DesktopClipboard | null>(null);
  ref.current ??= createDesktopClipboard(deps);
  return ref.current;
}
