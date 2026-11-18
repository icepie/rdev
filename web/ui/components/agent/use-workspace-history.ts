import { useCallback, useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react";
import { useParams, useSearchParams } from "react-router-dom";
import { agentConversationURL } from "../../lib/agent-route";
import { captureTranscriptAnchor, restoreTranscriptAnchor, type TranscriptAnchor } from "./transcript-resize";
import { messageID, requestWorkspace } from "./workspace-history";
import { nextKey } from "./protocol";
import type { ThreadSink } from "./thread-sink";
import type { Translator } from "./types";

export function useWorkspaceHistory({ sink, device, thread, ready, sendRaw, viewport, t, onCopy }: {
  sink: ThreadSink; device: string; thread: string; ready: boolean;
  sendRaw: (frame: Record<string, unknown>) => boolean;
  viewport: RefObject<HTMLDivElement | null>; t: Translator;
  onCopy: (id: string, text: string) => void;
}) {
  const { state, ops } = sink;
  const [params] = useSearchParams();
  const route = useParams();
  const linked = (route.threadId || route.thread || "") === thread && params.get("device") === device ? params.get("message") || "" : "";
  const [highlight, setHighlight] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [forking, setForking] = useState(false);
  const [edit, setEdit] = useState<{ messageId: string; text: string } | null>(null);
  const cancelPage = useRef<(() => void) | null>(null);
  const cancelFork = useRef<(() => void) | null>(null);
  const anchor = useRef<TranscriptAnchor | null>(null);
  const attempted = useRef("");
  const jumped = useRef("");
  useEffect(() => {
    setError(""); setLoading(false); setForking(false); setEdit(null); setHighlight(""); attempted.current = ""; jumped.current = "";
    return () => { cancelPage.current?.(); cancelFork.current?.(); };
  }, [device, thread]);
  useEffect(() => {
    attempted.current = ""; jumped.current = "";
    return () => cancelPage.current?.();
  }, [linked, ready]);
  const page = useCallback((around?: string, latest = false) => {
    cancelPage.current?.(); setLoading(true); setError("");
    cancelPage.current = requestWorkspace(ops.workspaceReplies.current, sendRaw,
      { op: "history_page", id: `history-${nextKey()}`, thread, limit: 50, ...(around ? { around } : latest ? {} : { before: state.historyBefore }) }, reply => {
        setLoading(false);
        if (!reply || reply.error) {
          if (!reply) attempted.current = "";
          setError(reply?.code === "timeout" ? t("agent.workspaceTimeout") : String(reply?.error || t("agent.workspaceDisconnected"))); return;
        }
        if (around && (!Array.isArray(reply.history) || !reply.history.some(item => item && typeof item === "object" && item.messageId === around))) {
          setError(t("agent.messageUnavailable")); return;
        }
        anchor.current = !around && !latest && viewport.current ? captureTranscriptAnchor(viewport.current) : null;
        ops.restoreHistory(reply, around || latest ? "around" : "before");
        if (latest) ops.scrollDown(true);
      });
  }, [ops, sendRaw, thread, state.historyBefore, viewport, t]);
  useLayoutEffect(() => {
    if (anchor.current && viewport.current) {
      restoreTranscriptAnchor(viewport.current, anchor.current);
      viewport.current.dispatchEvent(new Event("transcript-anchor"));
      anchor.current = null;
    }
  }, [state.items, viewport]);
  useEffect(() => {
    if (!ready || !linked || jumped.current === linked) return;
    const item = state.items.find(item => messageID(item) === linked);
    if (!item) {
      if (attempted.current !== linked) { attempted.current = linked; page(linked); }
      return;
    }
    const timer = requestAnimationFrame(() => {
      const node = viewport.current?.querySelector<HTMLElement>(`[data-thread-message="${item.key}"]`);
      if (!node) return;
      node.scrollIntoView({ block: "center" }); node.focus({ preventScroll: true });
      jumped.current = linked; setHighlight(linked);
    });
    return () => cancelAnimationFrame(timer);
  }, [ready, linked, state.items, page, viewport]);
  useEffect(() => {
    if (!highlight) return;
    const timer = window.setTimeout(() => setHighlight(""), 5000);
    return () => window.clearTimeout(timer);
  }, [highlight]);
  const fork = (messageId: string, editText?: string) => {
    setForking(true); setError("");
    cancelFork.current?.();
    cancelFork.current = requestWorkspace(ops.workspaceReplies.current, sendRaw,
      { op: "fork_thread", id: `fork-${nextKey()}`, thread, messageId, ...(editText !== undefined ? { editText } : {}) }, reply => {
        setForking(false);
        if (!reply || reply.error || reply.op === "error") setError(reply?.code === "timeout" ? t("agent.workspaceTimeout") : String(reply?.error || reply?.message || t("agent.workspaceDisconnected")));
        else setEdit(null);
      });
  };
  const copyLink = (messageId: string) => onCopy(`link-${messageId}`, new URL(agentConversationURL(device, thread, new URLSearchParams({ message: messageId })), window.location.origin).href);
  return { loading, error, highlight, page, fork, forking, edit, setEdit, copyLink };
}
