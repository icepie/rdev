import { JobGuidance } from "./job-guidance";
import { CommandOutputBody, commandOutputText } from "./command-output";
import { useCopy } from "../../lib/clipboard";
import { formatDuration } from "./transcript-rows";
import { Markdown } from "../markdown";
import { DisclosureBody } from "../disclosure-body";
import { useEffect, useId, useRef, useState } from "react";
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
  const bodyId = useId();
  const triggerRef = useRef<HTMLButtonElement>(null);
  const bodyRef = useRef<HTMLElement>(null);
  useEffect(() => {
    if (!open && bodyRef.current?.contains(document.activeElement)) {
      triggerRef.current?.focus();
    }
  }, [open]);
  const collapse = () => {
    triggerRef.current?.focus();
    onOpenChange(false);
  };
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
  }, [ready, threadId, sendRaw, open, selected, detailLoaded, sink.state.jobsRevision]);
  const jobs = sink.state.jobs;
  const job = jobs.find((item) => item.id === selected);
  const emptyOutput = t(job?.state === "running" || !detailLoaded ? "agent.jobs.waiting" : "agent.jobs.noOutput");
  const started = Date.parse(job?.started || "");
  const finished = Date.parse(job?.finished || "");
  const duration = Number.isFinite(started)
    ? Math.max(0, (Number.isFinite(finished) ? finished : Date.now()) - started) : null;
  const running = jobs.filter((item) => item.state === "running").length;
  return (
    <div className="min-w-0 overflow-hidden rounded-lg border border-border bg-surface-raised-solid" hidden={!jobs.length && !open && !sink.state.jobsError}>
      <button
        ref={triggerRef}
        type="button"
        className="flex min-h-8 w-full cursor-pointer items-center gap-1.5 px-3 py-1.5 text-left text-xs text-foreground transition-colors hover:bg-background/80 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-inset focus-visible:ring-ring max-md:pointer-coarse:min-h-11"
        onClick={() => onOpenChange(!open)}
        aria-expanded={open}
        aria-controls={bodyId}
      >
        <Icon name="ListTodo" className="size-3.5 shrink-0" />
        <span className="min-w-0 flex-1 truncate">{t("agent.jobs.title")}</span>
        {running > 0 && <span className="inline-flex h-4 min-w-4 shrink-0 items-center justify-center rounded-full bg-surface-recessed px-1 text-[10px] tabular-nums text-subtle-foreground">{running}</span>}
        <Icon name="ChevronDown" className={`size-3.5 shrink-0 text-muted-foreground transition-transform motion-reduce:transition-none ${open ? "rotate-180" : ""}`} />
      </button>
      {sink.state.jobsError && (
        <p
          role="alert"
          className="px-3 pb-2 break-words text-xs text-destructive-text"
        >
          {sink.state.jobsError}
        </p>
      )}
      <DisclosureBody open={open}>
        <section
          ref={bodyRef}
          id={bodyId}
          onKeyDown={event => {
            if (event.key === "Escape" && !event.defaultPrevented && !event.nativeEvent.isComposing) {
              event.preventDefault();
              event.stopPropagation();
              collapse();
            }
          }}
          aria-label={t("agent.jobs.title")}
          className="max-h-72 overflow-auto border-t border-border-seam p-2 text-xs"
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
                <Icon name={item.kind === "subagent" ? "UserRoundPlus" : "Terminal"} className="mr-1 inline-block size-3.5 shrink-0 align-text-bottom text-muted-foreground" />
                {item.label}
              </button>
              <span className="shrink-0 text-muted-foreground">
                {t(item.state === "running" && item.cancelRequested ? "agent.jobs.canceling" : `agent.jobs.${item.state}`)}
              </span>
              {item.state === "running" && (
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={!ready || item.cancelRequested}
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
              {!!job.pendingGuidance?.length && <div className="mt-2 text-xs text-muted-foreground">
                <p>{t("agent.jobs.guidancePending")}</p>
                <ul className="list-disc space-y-1 pl-4">{job.pendingGuidance.map((text, index) => <li key={index} className="whitespace-pre-wrap break-words">{text}</li>)}</ul>
              </div>}
              {job.error && <p role="alert" className="mt-2 whitespace-pre-wrap break-words text-destructive-text">{job.error}</p>}
            </div>
          )}
          <JobGuidance job={job} threadId={threadId} ready={ready} ops={sink.ops} sendRaw={sendRaw} t={t} />
          <button type="button" onClick={collapse} aria-controls={bodyId} aria-expanded={open}
            aria-label={t("agent.jobs.collapse")}
            className="mt-1 flex min-h-6 w-full cursor-pointer items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring max-md:pointer-coarse:min-h-11">
            <Icon name="ChevronUp" className="size-3.5" />
          </button>
        </section>
      </DisclosureBody>
    </div>
  );
}
