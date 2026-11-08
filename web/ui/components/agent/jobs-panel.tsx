import { CommandOutputBody, commandOutputText } from "./command-output";
import { useCopy } from "../../lib/clipboard";
import { formatDuration } from "./transcript-rows";
import { Markdown } from "../markdown";
import { DisclosureBody } from "../disclosure-body";
import { useEffect, useState } from "react";
import type { ThreadSink } from "./thread-sink";
import type { Translator } from "./types";
import { Button } from "../button";
import { Icon } from "../icon";

export function JobsPanel({
  open,
  onOpenChange,
  full,
  sink,
  threadId,
  ready,
  sendRaw,
  t,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  full: boolean;
  sink: ThreadSink;
  threadId: string;
  ready: boolean;
  sendRaw: (payload: Record<string, unknown>) => boolean;
  t: Translator;
}) {
  const [copiedId, copy] = useCopy();
  const [selection, setSelection] = useState<{
    thread: string;
    id: string;
  } | null>(null);
  const selected =
    selection?.thread === threadId &&
    sink.state.jobs.some((job) => job.id === selection.id)
      ? selection.id
      : "";
  const selectedJob = sink.state.jobs.find(job => job.id === selected);
  const detailLoaded = selectedJob?.state !== "running" && selectedJob?.detailState === selectedJob?.state;
  useEffect(() => {
    sink.ops.setJobs([]);
    sink.ops.setJobsError("");
  }, [threadId, sink.ops]);
  useEffect(() => {
    if (!ready || !threadId) return;
    const refresh = () => {
      if (document.hidden) return;
      sendRaw({ op: "get_jobs", thread: threadId });
      if (open && selected && !detailLoaded)
        sendRaw({ op: "get_jobs", thread: threadId, id: selected });
    };
    let timer: number | undefined;
    const syncVisibility = () => {
      window.clearInterval(timer);
      timer = undefined;
      if (document.hidden) return;
      refresh();
      timer = window.setInterval(refresh, open ? 2000 : 5000);
    };
    syncVisibility();
    document.addEventListener("visibilitychange", syncVisibility);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", syncVisibility);
    };
  }, [ready, threadId, sendRaw, open, selected, detailLoaded]);
  const jobs = sink.state.jobs;
  const job = jobs.find((item) => item.id === selected);
  const emptyOutput = t(job?.state === "running" || !detailLoaded ? "agent.jobs.waiting" : "agent.jobs.noOutput");
  const started = Date.parse(job?.started || "");
  const finished = Date.parse(job?.finished || "");
  const duration = Number.isFinite(started)
    ? Math.max(0, (Number.isFinite(finished) ? finished : Date.now()) - started) : null;
  const running = jobs.filter((item) => item.state === "running").length;
  if (!jobs.length && !open && !sink.state.jobsError) return null;
  return (
    <div className="min-w-0">
      <Button
        variant="ghost"
        size="sm"
        className="h-8 gap-1 px-1 text-xs text-muted-foreground max-md:pointer-coarse:h-11"
        onClick={() => onOpenChange(!open)}
        aria-expanded={open}
      >
        <Icon name="ListTodo" className="size-3.5" />
        {t("agent.jobs.title")}
        {running > 0 && ` (${running})`}
      </Button>
      {sink.state.jobsError && (
        <p
          role="alert"
          className="mb-2 break-words text-xs text-destructive-text"
        >
          {sink.state.jobsError}
        </p>
      )}
      <DisclosureBody open={open}>
        <section
          aria-label={t("agent.jobs.title")}
          className="mb-2 max-h-72 overflow-auto rounded-lg border border-border bg-background p-2 text-xs"
        >
          {!jobs.length && (
            <p className="p-2 text-muted-foreground">{t("agent.jobs.empty")}</p>
          )}
          {jobs.map((item) => (
            <div
              key={item.id}
              className="flex min-w-0 items-start gap-2 border-b border-border-seam py-2 last:border-b-0"
            >
              <button
                type="button"
                onClick={() => setSelection({ thread: threadId, id: item.id })}
                aria-pressed={selected === item.id}
                className={`min-w-0 flex-1 cursor-pointer rounded px-1 text-left hover:bg-state-hover ${full ? "whitespace-pre-wrap break-words" : "truncate"} ${selected === item.id ? "bg-state-active" : ""}`}
                title={`${item.id}: ${item.label}`}
              >
                {item.kind === "subagent" ? "◈ " : "› "}
                {item.label}
              </button>
              <span className="shrink-0 text-muted-foreground">
                {t(`agent.jobs.${item.state}`)}
              </span>
              {item.state === "running" && (
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={!ready}
                  onClick={() =>
                    sendRaw({ op: "cancel_job", thread: threadId, id: item.id })
                  }
                >
                  {t("agent.cmd.stop")}
                </Button>
              )}
            </div>
          ))}
          {job && (
            <div className="mt-2 flex items-center gap-2 text-xs text-muted-foreground">
              <span className="min-w-0 flex-1 truncate" title={job.id}>{job.label}</span>
              {duration !== null && <span className="shrink-0 tabular-nums">{formatDuration(duration)}</span>}
              <Button size="sm" variant="ghost" disabled={!job.result && !job.output}
                onClick={() => copy(job.id, job.kind === "shell" ? commandOutputText(job.result || job.output || "") : job.result || job.output || "")}>
                {t(copiedId === job.id ? "common.copied" : "common.copy")}
              </Button>
            </div>
          )}
          {job && (
            <div className="mt-2">
              {job.kind === "shell" ? (
                <CommandOutputBody
                  key={job.id}
                  tool={{
                    id: job.id, name: "shell", args: "{}", summary: job.label,
                    result: job.result || job.output || emptyOutput,
                    diff: "", done: job.state !== "running", isError: job.state === "failed",
                  }}
                  t={t}
                />
              ) : (
                <div className="max-h-40 min-w-0 overflow-auto rounded bg-card p-2 text-sm">
                  <Markdown text={job.result || job.output || emptyOutput} />
                </div>
              )}
              {job.error && <p role="alert" className="mt-2 whitespace-pre-wrap break-words text-destructive-text">{job.error}</p>}
            </div>
          )}
        </section>
      </DisclosureBody>
    </div>
  );
}
