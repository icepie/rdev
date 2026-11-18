import { useEffect, useId, useRef, useState } from "react";
import { Button } from "../button";
import type { ThreadSinkOps } from "./thread-sink";
import type { Translator } from "./types";
import { requestWorkspace } from "./workspace-history";

/** Page the durable record stream rather than the bounded live output tail. */
export function ChildTranscript({ thread, job, ready, ops, sendRaw, t }: {
  thread: string; job: string; ready: boolean; ops: ThreadSinkOps;
  sendRaw: (frame: Record<string, unknown>) => boolean; t: Translator;
}) {
  const [open, setOpen] = useState(false);
  const [cursor, setCursor] = useState(0);
  const [previous, setPrevious] = useState<number[]>([]);
  const [revision, setRevision] = useState(0);
  const [page, setPage] = useState<{ entries: unknown[]; next: number; more: boolean } | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const sequence = useRef(0);
  const requestOwner = useId();
  useEffect(() => {
    if (!open || !ready) return;
    setLoading(true);
    setError("");
    return requestWorkspace(ops.workspaceReplies.current, sendRaw, {
      type: "get_subagent_messages", id: `child-${requestOwner}-${++sequence.current}`,
      thread, subagentId: job, fromByte: cursor,
    }, reply => {
      setLoading(false);
      if (!reply || reply.success !== true) {
        setError(String(reply?.error || reply?.message || t("common.disconnected")));
        return;
      }
      const data = reply.data as { entries?: unknown[]; nextByte?: number; hasMore?: boolean; reset?: boolean } | undefined;
      if (!data || !Array.isArray(data.entries) || typeof data.nextByte !== "number") {
        setError(t("common.error"));
        return;
      }
      if (data.reset && cursor !== 0) {
        setPrevious([]);
        setCursor(0);
        return;
      }
      setPage({ entries: data.entries, next: data.nextByte, more: !!data.hasMore });
    });
  }, [open, ready, thread, job, cursor, revision, requestOwner, ops.workspaceReplies, sendRaw, t]);
  return <details className="mt-2 rounded border border-border p-2" open={open}
    onToggle={event => setOpen(event.currentTarget.open)}>
    <summary className="cursor-pointer text-xs">{t("agent.jobs.transcript")}</summary>
    {open && <div className="mt-2 space-y-2">
      {error && <p role="alert" className="whitespace-pre-wrap text-destructive-text">{error}</p>}
      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant="ghost" disabled={!ready || loading || !previous.length}
          onClick={() => { setCursor(previous[previous.length - 1]); setPrevious(previous.slice(0, -1)); }}>
          {t("agent.jobs.previousRecords")}
        </Button>
        <Button size="sm" variant="ghost" disabled={!ready || loading}
          onClick={() => setRevision(value => value + 1)}>{t("agent.jobs.refreshRecords")}</Button>
        <Button size="sm" variant="ghost" disabled={!ready || loading || !page?.more || page.next <= cursor}
          onClick={() => { setPrevious(values => [...values, cursor]); setCursor(page!.next); }}>
          {t("agent.jobs.nextRecords")}
        </Button>
      </div>
      {loading ? <p role="status">{t("common.loading")}</p> : page &&
        <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words rounded bg-card p-2 text-xs"
          aria-label={t("agent.jobs.transcript")}>
          {page.entries.map(entry => JSON.stringify(entry, null, 2)).join("\n\n") || t("agent.jobs.waiting")}
        </pre>}
    </div>}
  </details>;
}
