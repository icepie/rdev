import { useEffect, useRef } from "react";
import { Button } from "../button";
import { Textarea } from "../textarea";
import { nextKey } from "./protocol";
import { useComposerDraft } from "./use-composer-draft";
import type { AgentJob, ThreadSinkOps } from "./thread-sink";
import type { Translator } from "./types";

type Draft = { text: string; pending: boolean; error: string; notice: string };
const EMPTY: Draft = { text: "", pending: false, error: "", notice: "" };

// Kept mounted by JobsPanel so switching the selected task preserves each
// draft. Replies capture that draft's key, never the currently selected task.
export function JobGuidance({ job, threadId, ready, ops, sendRaw, t }: {
  job?: AgentJob;
  threadId: string;
  ready: boolean;
  ops: ThreadSinkOps;
  sendRaw: (payload: Record<string, unknown>) => boolean;
  t: Translator;
}) {
  const key = JSON.stringify([threadId, job?.id]);
  const [draft, setDraft] = useComposerDraft(key, EMPTY);
  const inFlight = useRef(new Set<string>());
  const pending = useRef(new Map<string, () => void>());
  useEffect(() => {
    if (!ready) for (const settle of [...pending.current.values()]) settle();
  }, [ready]);
  useEffect(() => () => {
    for (const settle of [...pending.current.values()]) settle();
  }, []);
  if (job?.kind !== "subagent") return null;
  const canceling = job.state === "running" && job.cancelRequested;
  const running = job.state === "running" && !canceling;
  if (!running && !draft.text && !draft.pending && !draft.error && !draft.notice) return null;

  const submit = () => {
    const text = draft.text.trim();
    if (!ready || !running || !text || draft.pending || inFlight.current.has(key)) return;
    if (new TextEncoder().encode(text).byteLength > 4096) {
      setDraft(previous => ({ ...previous, error: t("agent.jobs.guidanceTooLong"), notice: "" }));
      return;
    }
    const id = `job-guidance-${nextKey()}`;
    const target = job.id;
    const submitted = draft.text;
    inFlight.current.add(key);
    setDraft(previous => ({ ...previous, pending: true, error: "", notice: "" }));
    let timer: number | undefined;
    const settle = (reply: Record<string, unknown> | null) => {
      if (!pending.current.delete(id)) return;
      inFlight.current.delete(key);
      window.clearTimeout(timer);
      ops.jobGuidanceReplies.current.delete(id);
      const accepted = reply?.jobId === target && reply?.thread === threadId && reply?.isError !== true;
      setDraft(previous => ({
        ...previous,
        text: accepted && previous.text === submitted ? "" : previous.text,
        pending: false,
        error: accepted ? "" : typeof reply?.message === "string" ? reply.message : t("agent.jobs.guidanceUnconfirmed"),
        notice: accepted ? t("agent.jobs.guidanceAccepted") : "",
      }));
    };
    pending.current.set(id, () => settle(null));
    ops.jobGuidanceReplies.current.set(id, settle);
    timer = window.setTimeout(() => settle(null), 15000);
    if (!sendRaw({ op: "steer_job", id, jobId: target, thread: threadId, text })) settle(null);
  };

  return (
    <form className="mt-3 space-y-2 border-t border-border-seam pt-3" onSubmit={event => { event.preventDefault(); submit(); }}>
      <Textarea aria-label={t("agent.jobs.guidance")} placeholder={t("agent.jobs.guidanceHint")}
        className="min-h-16 text-sm" rows={2} value={draft.text}
        onChange={event => setDraft(previous => ({ ...previous, text: event.target.value, error: "", notice: "" }))} />
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs text-muted-foreground">{t(running ? "agent.jobs.guidanceBoundary" : canceling ? "agent.jobs.guidanceCanceling" : "agent.jobs.guidanceFinished")}</span>
        <Button type="submit" size="sm" variant="secondary" disabled={!ready || !running || !draft.text.trim() || draft.pending}>
          {t(draft.pending ? "agent.jobs.guidanceSending" : "agent.jobs.guidance")}
        </Button>
      </div>
      {draft.error && <p role="alert" className="text-xs text-destructive-text">{draft.error}</p>}
      {draft.notice && <p role="status" className="text-xs text-muted-foreground">{draft.notice}</p>}
    </form>
  );
}
