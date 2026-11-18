import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { agentConversationURL } from "../../lib/agent-route";
import { Button } from "../button";
import { Input } from "../input";
import { Popover, PopoverContent, PopoverTrigger } from "../popover";
import { nextKey } from "./protocol";
import { requestWorkspace } from "./workspace-history";
import type { ThreadSinkOps } from "./thread-sink";
import type { Translator } from "./types";

interface Hit { thread: string; messageId: string; title: string; snippet: string }

/** Only the authenticated device connection supplied by this pane is searched. */
export function HistorySearch({ device, thread, ready, ops, sendRaw, t }: {
  device: string; thread: string; ready: boolean; ops: ThreadSinkOps;
  sendRaw: (frame: Record<string, unknown>) => boolean; t: Translator;
}) {
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [q, setQuery] = useState("");
  const [all, setAll] = useState(false);
  const [hits, setHits] = useState<Hit[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => { setQuery(""); setHits([]); setError(""); }, [device, thread]);
  useEffect(() => {
    setHits([]); setError(""); setBusy(false);
    if (!open || !ready || !q.trim()) return;
    setBusy(true);
    let cancel = () => {};
    const timer = window.setTimeout(() => {
      cancel = requestWorkspace(ops.workspaceReplies.current, sendRaw,
        { op: "search_history", id: `search-${nextKey()}`, q: q.trim(), ...(all ? {} : { thread }) }, reply => {
          setBusy(false);
          if (!reply || reply.error) { setError(reply?.code === "timeout" ? t("agent.workspaceTimeout") : String(reply?.error || t("agent.workspaceDisconnected"))); return; }
          setHits((Array.isArray(reply.hits) ? reply.hits : []).filter((hit): hit is Hit => !!hit && typeof hit === "object" && typeof hit.thread === "string" && typeof hit.messageId === "string"));
        });
    }, 300);
    return () => { window.clearTimeout(timer); cancel(); };
  }, [q, all, open, ready, device, thread, ops, sendRaw, t]);
  return <Popover open={open} onOpenChange={setOpen}>
    <PopoverTrigger asChild><Button variant="ghost" size="sm" disabled={!ready}>{t("agent.searchHistory")}</Button></PopoverTrigger>
    <PopoverContent className="w-80" align="start">
      <div className="flex w-full flex-col gap-2">
        <Input value={q} maxLength={500} onChange={event => setQuery(event.target.value)} placeholder={t("agent.searchHistory")} aria-label={t("agent.searchHistory")} />
        <label className="flex items-center gap-2 text-xs"><input type="checkbox" checked={all} onChange={event => setAll(event.target.checked)} />{t("agent.searchDevice", { device })}</label>
        {busy && <p role="status" className="text-xs">{t("common.loading")}</p>}
        {error && <p role="alert" className="text-xs text-destructive-text">{error}</p>}
        {!busy && !error && q.trim() && !hits.length && <p className="text-xs text-muted-foreground">{t("agent.searchNoMessages")}</p>}
        <div className="max-h-80 overflow-y-auto">{hits.map(hit => <button type="button" key={`${hit.thread}:${hit.messageId}`} className="mb-1 block w-full rounded p-2 text-left text-xs hover:bg-state-hover" onClick={() => {
          navigate(agentConversationURL(device, hit.thread, new URLSearchParams({ message: hit.messageId })));
          setOpen(false);
        }}><strong className="block truncate">{hit.title || hit.thread}</strong><span className="whitespace-pre-wrap break-words">{hit.snippet}</span></button>)}</div>
      </div>
    </PopoverContent>
  </Popover>;
}
