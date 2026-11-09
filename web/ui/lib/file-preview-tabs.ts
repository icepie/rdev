import { useCallback, useEffect, useRef, useState } from "react";
import type { FilePreviewState } from "../components/agent/types";
import { readFilePreviewReply } from "./file-preview-reply";

export type PreviewTab = { key: string; device: string; path: string; state: FilePreviewState };

/** Request ownership survives tab switches; closing/refreshing invalidates late replies. */
export function useFilePreviewTabs(timeoutMessage: string) {
  const [tabs, setTabs] = useState<PreviewTab[]>([]);
  const tabsRef = useRef(tabs); tabsRef.current = tabs;
  const [active, setActive] = useState("");
  const pending = useRef(new Map<string, { key: string; device: string; timer: number }>());
  const cancelKey = useCallback((key?: string) => {
    for (const [id, request] of pending.current) if (key === undefined || request.key === key) {
      clearTimeout(request.timer); pending.current.delete(id);
    }
  }, []);
  useEffect(() => () => cancelKey(), [cancelKey]);
  const fail = useCallback((id: string, message: string) => {
    const request = pending.current.get(id);
    if (!request) return;
    clearTimeout(request.timer); pending.current.delete(id);
    setTabs(tabs => tabs.map(tab => tab.key === request.key ? { ...tab, state: { ...tab.state, status: "error", message } } : tab));
  }, []);
  const open = useCallback((device: string, path: string, read: (id: string) => Promise<void>, refresh = false) => {
    const key = JSON.stringify([device, path]);
    setActive(key);
    if (!refresh && tabsRef.current.some(tab => tab.key === key)) return;
    cancelKey(key);
    // getRandomValues also works on HTTP LAN origins (randomUUID does not).
    const id = "preview-" + Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, "0")).join("");
    const next: PreviewTab = { key, device, path, state: { status: "loading", content: "", message: "", truncated: false } };
    setTabs(tabs => tabs.some(tab => tab.key === key) ? tabs.map(tab => tab.key === key ? next : tab) : [...tabs, next]);
    pending.current.set(id, { key, device, timer: window.setTimeout(() => fail(id, timeoutMessage), 30000) });
    void Promise.resolve().then(() => { if (pending.current.has(id)) return read(id); }).catch(error => fail(id, String(error instanceof Error ? error.message : error)));
  }, [cancelKey, fail, timeoutMessage]);
  const accept = useCallback((msg: Record<string, unknown>) => {
    const id = typeof msg.requestId === "string" ? msg.requestId : "";
    const request = pending.current.get(id);
    if (!request) return false;
    const state = readFilePreviewReply(id, request.device, msg);
    if (!state) return false;
    clearTimeout(request.timer); pending.current.delete(id);
    setTabs(tabs => tabs.map(tab => tab.key === request.key ? { ...tab, state } : tab));
    return true;
  }, []);
  const failPending = useCallback((message: string) => { for (const id of pending.current.keys()) fail(id, message); }, [fail]);
  const reset = useCallback(() => { cancelKey(); setTabs([]); setActive(""); }, [cancelKey]);
  const close = useCallback((key: string) => {
    cancelKey(key);
    const remaining = tabsRef.current.filter(tab => tab.key !== key);
    setTabs(remaining);
    setActive(current => current === key ? remaining.at(-1)?.key ?? "" : current);
  }, [cancelKey]);
  return { tabs, active, select: setActive, open, close, reset, accept, failPending };
}
