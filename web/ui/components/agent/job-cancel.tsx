import { useEffect, useRef, useState } from "react";
import { Button } from "../button";
import { nextKey } from "./protocol";
import type { AgentJob, ThreadSinkOps } from "./thread-sink";
import type { Translator } from "./types";

export function JobCancel({ job, threadId, ready, ops, sendRaw, t }: {
  job: AgentJob;
  threadId: string;
  ready: boolean;
  ops: ThreadSinkOps;
  sendRaw: (payload: Record<string, unknown>) => boolean;
  t: Translator;
}) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const inFlight = useRef<(() => void) | null>(null);
  useEffect(() => {
    if (!ready) inFlight.current?.();
  }, [ready]);
  useEffect(() => () => inFlight.current?.(), []);

  const cancel = () => {
    if (!ready || job.cancelRequested || inFlight.current) return;
    const id = `job-cancel-${nextKey()}`;
    setPending(true);
    setError("");
    let timer: number | undefined;
    const settle = (reply: Record<string, unknown> | null) => {
      if (inFlight.current !== abandon) return;
      inFlight.current = null;
      window.clearTimeout(timer);
      ops.jobActionReplies.current.delete(id);
      const accepted = reply?.op === "job_cancelled" && reply?.thread === threadId && reply?.jobId === job.id && reply?.isError !== true && reply?.value === true;
      setPending(false);
      setError(accepted ? "" : typeof reply?.message === "string" ? reply.message : t("agent.jobs.cancelUnconfirmed"));
      if (accepted) ops.setJobs(previous => previous.map(item =>
        item.id === job.id && item.state === "running" ? {...item, cancelRequested: true} : item));
    };
    const abandon = () => settle(null);
    inFlight.current = abandon;
    ops.jobActionReplies.current.set(id, settle);
    timer = window.setTimeout(() => settle(null), 15000);
    if (!sendRaw({op:"cancel_job", thread:threadId, id, jobId:job.id})) settle(null);
  };

  return <div className="flex max-w-[45%] shrink-0 flex-col items-end">
    <Button size="sm" variant="ghost" disabled={!ready || pending || job.cancelRequested}
      aria-label={t("agent.jobs.stopTask", {label:job.label})} onClick={cancel}>
      {t(pending ? "agent.jobs.cancelSending" : "agent.cmd.stop")}
    </Button>
    {error && <p role="alert" className="mt-1 break-words text-xs text-destructive-text">{error}</p>}
  </div>;
}
