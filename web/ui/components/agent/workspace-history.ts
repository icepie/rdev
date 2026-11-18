import type { TranscriptItem } from "./types";

export function messageID(item: TranscriptItem): string | undefined {
  return "messageId" in item ? item.messageId : undefined;
}

/** Reuse mounted rows for overlap: selection, disclosures and media survive paging. */
export function mergeHistory(current: TranscriptItem[], incoming: TranscriptItem[], mode: "before" | "refresh" | "around", liveIDs: Set<string> = new Set()): TranscriptItem[] {
  const old = new Map(current.flatMap(item => messageID(item) ? [[messageID(item)!, item] as const] : []));
  const page = incoming.map(item => {
    const prior = old.get(messageID(item) || "");
    if (prior && mode !== "refresh" && liveIDs.has(messageID(item) || "")) return prior;
    return prior ? { ...item, key: prior.key } : item;
  });
  const ids = new Set(page.map(messageID).filter(Boolean));
  const retained = current.filter(item => !ids.has(messageID(item)) && (mode !== "around" || liveIDs.has(messageID(item) || "") || item.kind === "approval" || item.kind === "ask"));
  const combined = mode === "refresh" ? [...retained, ...page] : [...page, ...retained];
  const seen = new Set<string>();
  return combined.filter(item => {
    const id = messageID(item);
    if (!id) return true;
    if (seen.has(id)) return false;
    seen.add(id);
    return true;
  });
}

export type WorkspaceReply = (reply: Record<string, unknown> | null) => void;

/** Request ownership lives with a pane; cancelled requests cannot mutate a new view. */
export function requestWorkspace(replies: Map<string, WorkspaceReply>, send: (frame: Record<string, unknown>) => boolean, frame: Record<string, unknown>, reply: WorkspaceReply, timeout = 15000): () => void {
  const id = String(frame.id);
  const timer = window.setTimeout(() => settle({ error: "timeout", code: "timeout", id, thread: frame.thread }), timeout);
  const settle: WorkspaceReply = value => {
    if (!replies.delete(id)) return;
    window.clearTimeout(timer);
    reply(value);
  };
  replies.set(id, settle);
  if (!send(frame)) settle(null);
  return () => { replies.delete(id); window.clearTimeout(timer); };
}
