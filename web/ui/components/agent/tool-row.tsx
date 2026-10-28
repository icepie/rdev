import { Fragment, useState } from "react";
import { cn } from "../../lib/utils";
import { Icon } from "../icon";
import { CommandOutputBody } from "./command-output";
import { PierreFileDiff } from "./diff-view";
import { ToolIcon } from "./tool-icon";
import {
  PAST_ROW_DIM_CLASS,
  bundleSegment,
  diffStatsOf,
  formatDuration,
  rowTarget,
  rowVerb,
  toolDurationMs,
  toolImageDataUrl,
  workKindOf,
  type ToolBundle,
} from "./transcript-rows";
import type { ToolItem, Translator } from "./types";

const RESULT_CAP = 4000;

/* Per-kind work row (bb ExpandableTimelineRow + TimelineTitleView): header
 * rhythm matches the old ToolCard — `px-2 py-0 text-sm leading-5 gap-1.5`,
 * hover-revealed chevron — with bb verb/target titles, diff stats, and
 * wall-clock durations. Command rows auto-expand while running and collapse
 * when done (bb terminalFrontierRowIds); a manual toggle sticks. The target
 * of a file-change row opens the diff (bb TimelineTitleAction), other kinds
 * open the file preview. Bodies vary by workKindOf: ANSI terminal output
 * for commands, a Pierre diff card for file changes, key:value args +
 * result card (bb ToolCallDetailBlock) for everything else. */

interface RowProps {
  tool: ToolItem;
  t: Translator;
  dimmed?: boolean;
  onOpenFile?: (path: string) => void;
  onOpenDiff?: (path: string) => void;
}

function parseArgs(args: string): Record<string, unknown> | null {
  try {
    const parsed: unknown = JSON.parse(args);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed))
      return parsed as Record<string, unknown>;
  } catch {
    /* keep null */
  }
  return null;
}

/* bb thread→panel linkage: the path arg a row can hand to the side panel. */
function pathArgOf(tool: ToolItem): string | null {
  const path = parseArgs(tool.args)?.path;
  return typeof path === "string" && path ? path : null;
}

/* bb ToolCallDetailBlock formatArgValue, compact variant: strings raw,
 * everything else JSON.stringify without indent. */
function formatArgValue(value: unknown): string {
  if (typeof value === "string") return value;
  if (value === null) return "null";
  if (typeof value === "number" || typeof value === "boolean")
    return String(value);
  return JSON.stringify(value) ?? "undefined";
}

/* Standard result card shared by the non-command bodies (old ToolCard
 * styling, RESULT_CAP truncation notice preserved). */
function ResultCard({ tool, t }: { tool: ToolItem; t: Translator }) {
  let result = tool.result;
  if (result.length > RESULT_CAP)
    result = result.slice(0, RESULT_CAP) + "\n" + t("agent.truncated");
  return (
    <div
      className={cn(
        "overflow-hidden rounded-lg border bg-card",
        tool.isError ? "border-destructive" : "border-border",
      )}
    >
      <div className="max-h-72 overflow-auto px-4 py-3 font-mono text-xs leading-tight break-words whitespace-pre-wrap text-foreground opacity-70 select-text">
        {result || "…"}
      </div>
    </div>
  );
}

/* bb ToolCallDetailBlock CollapsibleHeader: `key: value` lines clamped to
 * 3 lines with a gradient fade and a Show more/less toggle. */
function ArgsBlock({
  tool,
  t,
}: {
  tool: ToolItem;
  t: Translator;
}) {
  const [expanded, setExpanded] = useState(false);
  const parsed = parseArgs(tool.args);
  const entries: [string, string][] = parsed
    ? Object.entries(parsed).map(([k, v]) => [k, formatArgValue(v)])
    : tool.args.trim()
      ? [["args", tool.args]]
      : [];
  if (entries.length === 0) return null;
  const joined = entries.map(([k, v]) => `${k}: ${v}`).join("\n");
  const collapsible = joined.split("\n").length > 3 || joined.length > 240;
  return (
    <div className="overflow-hidden rounded-lg border border-border bg-card">
      <div className="px-4 py-3 font-mono text-xs leading-tight text-foreground opacity-70 select-text">
        <div className="relative">
          <div
            className={cn(
              "whitespace-pre-wrap break-words",
              collapsible && !expanded && "line-clamp-3",
            )}
          >
            {entries.map(([key, value]) => (
              <Fragment key={key}>
                {"\n"}
                <span className="text-muted-foreground">{key}: </span>
                <span>{value}</span>
              </Fragment>
            ))}
          </div>
          {collapsible && !expanded && (
            <div className="absolute inset-x-0 bottom-0 flex items-end justify-end bg-gradient-to-t from-card from-30% to-transparent pt-3">
              <button
                type="button"
                onClick={() => setExpanded(true)}
                aria-expanded={false}
                className="text-2xs text-timeline-accent hover:underline"
              >
                {t("agent.showMore")}
              </button>
            </div>
          )}
        </div>
        {collapsible && expanded && (
          <div className="mt-1 flex justify-end">
            <button
              type="button"
              onClick={() => setExpanded(false)}
              aria-expanded={true}
              className="text-2xs text-timeline-accent hover:underline"
            >
              {t("agent.showLess")}
            </button>
          </div>
        )}
      </div>
    </div>
  );
}

/* bb QuestionWorkRowBody (settled): the prompt in subtle text, the answer
 * in foreground, "No answer" when dismissed. Live asks use the interactive
 * AskItem card instead — this body only appears for settled/history rows. */
function QuestionRowBody({
  tool,
  t,
}: {
  tool: ToolItem;
  t: Translator;
}) {
  let question = tool.summary;
  try {
    const parsed: unknown = JSON.parse(tool.args);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      const q = (parsed as Record<string, unknown>).question;
      if (typeof q === "string" && q) question = q;
    }
  } catch {
    /* keep summary */
  }
  const answer = tool.result.trim();
  const dismissed = answer.startsWith("[question dismissed");
  return (
    <div className="rounded-lg border border-border bg-card px-4 py-3 text-xs leading-snug">
      <div className="text-subtle-foreground whitespace-pre-wrap break-words">
        {question}
      </div>
      <div
        className={cn(
          "mt-0.5 whitespace-pre-wrap break-words",
          dismissed ? "text-subtle-foreground" : "text-foreground",
        )}
      >
        {dismissed || !answer ? t("agent.rowNoAnswer") : answer}
      </div>
    </div>
  );
}

function ToolRowBody({
  tool,
  t,
  onOpenFile,
}: {
  tool: ToolItem;
  t: Translator;
  onOpenFile?: (path: string) => void;
}) {
  const kind = workKindOf(tool.name);
  const result = tool.result.trim();
  if (kind === "command") return <CommandOutputBody tool={tool} t={t} />;
  if (kind === "question") return <QuestionRowBody tool={tool} t={t} />;
  /* Image read: the base64 tool result renders as an <img> (click opens the
   * full preview tab) instead of the raw blob. */
  const imageUrl = toolImageDataUrl(tool);
  if (imageUrl) {
    return (
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          onOpenFile?.(rowTarget(tool));
        }}
        className="block cursor-zoom-in overflow-hidden rounded-lg border border-border bg-[repeating-conic-gradient(var(--surface-recessed)_0%_25%,transparent_0%_50%)] bg-[length:12px_12px] p-2"
        title={rowTarget(tool)}
      >
        <img
          src={imageUrl}
          alt={rowTarget(tool)}
          className="max-h-64 max-w-full rounded object-contain"
        />
      </button>
    );
  }
  if (kind === "file-change") {
    /* bb TimelineFileDiffBlock; write_file without a diff falls back to the
     * plain result card. Result card repeats only when it adds information
     * beyond the patch itself. */
    if (!tool.diff) return <ResultCard tool={tool} t={t} />;
    const redundant = !result || result === tool.diff.trim();
    return (
      <>
        <div className="max-h-80 overflow-auto rounded-lg border border-border bg-background">
          <PierreFileDiff patch={tool.diff} view="unified" overflow="wrap" />
        </div>
        {!redundant && <ResultCard tool={tool} t={t} />}
      </>
    );
  }
  /* file-read / search / todo / generic (bb ToolCallDetailBlock). */
  const skipResult =
    kind === "todo" && (result === "" || result === "{}");
  return (
    <>
      <ArgsBlock tool={tool} t={t} />
      {!skipResult && <ResultCard tool={tool} t={t} />}
    </>
  );
}

export function ToolRow({ tool, t, dimmed, onOpenFile, onOpenDiff }: RowProps) {
  /* bb terminalFrontierRowIds: running command rows stay expanded and
   * collapse when settled; userOpen ?? auto makes a manual toggle stick. */
  const [userOpen, setUserOpen] = useState<boolean | null>(null);
  const kind = workKindOf(tool.name);
  const open = userOpen ?? (kind === "command" && !tool.done);
  const stats =
    kind === "file-change" && tool.diff ? diffStatsOf(tool.diff) : null;
  const durationMs = toolDurationMs(tool);
  const pathArg =
    kind === "file-read" || kind === "file-change" || kind === "search"
      ? pathArgOf(tool)
      : null;
  /* bb TimelineTitleAction: file-change targets open the diff when a diff
   * handler is wired, falling back to the plain file preview. */
  const pathHandler =
    kind === "file-change" ? (onOpenDiff ?? onOpenFile) : onOpenFile;
  const pathLink =
    pathArg !== null && pathHandler
      ? { path: pathArg, open: pathHandler }
      : null;
  const runningTool = !tool.done;
  return (
    <div className={cn("w-full", dimmed && PAST_ROW_DIM_CLASS)}>
      <button
        type="button"
        onClick={() => setUserOpen(!open)}
        aria-expanded={open}
        className="group/toggle flex w-full max-w-full cursor-pointer items-center gap-1 overflow-hidden px-2 py-0 text-left text-sm leading-5 text-muted-foreground transition-colors hover:text-foreground"
      >
        <ToolIcon
          name={tool.name}
          className="size-3.5 shrink-0 text-muted-foreground"
        />
        <span className="shrink-0">{rowVerb(tool, t)}</span>
        {pathLink ? (
          <span
            role="button"
            tabIndex={0}
            title={pathLink.path}
            onClick={(e) => {
              e.stopPropagation();
              pathLink.open(pathLink.path);
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.stopPropagation();
                e.preventDefault();
                pathLink.open(pathLink.path);
              }
            }}
            className="min-w-0 flex-1 cursor-pointer truncate font-medium text-foreground underline-offset-2 hover:underline focus-visible:underline"
          >
            {rowTarget(tool)}
          </span>
        ) : (
          <span className="min-w-0 flex-1 truncate font-medium text-foreground opacity-70">
            {rowTarget(tool)}
          </span>
        )}
        {stats && (
          <span className="shrink-0 tabular-nums">
            {stats.added > 0 && (
              <span className="text-diff-added">+{stats.added}</span>
            )}
            {stats.added > 0 && stats.removed > 0 && " "}
            {stats.removed > 0 && (
              <span className="text-diff-removed">-{stats.removed}</span>
            )}
          </span>
        )}
        {durationMs !== null && durationMs > 1000 && (
          <span className="shrink-0 tabular-nums text-muted-foreground">
            {formatDuration(durationMs)}
          </span>
        )}
        {runningTool ? (
          <Icon name="Spinner" className="size-3.5 shrink-0 animate-spin" />
        ) : tool.isError ? (
          <Icon
            name="CircleX"
            className="size-3.5 shrink-0 text-destructive-text"
          />
        ) : (
          <Icon name="CircleCheck" className="size-3.5 shrink-0 text-success" />
        )}
        <Icon
          name="ChevronRight"
          className={cn(
            "pointer-events-none size-3 shrink-0 origin-center text-subtle-foreground/60 transition-[opacity,rotate] duration-200 ease-out",
            open
              ? "rotate-90"
              : "opacity-0 group-hover/toggle:opacity-100 group-focus-visible/toggle:opacity-100",
          )}
        />
      </button>
      {open && (
        <div className="flex flex-col gap-1.5 px-2 pb-1 pt-0.5">
          <ToolRowBody tool={tool} t={t} onOpenFile={onOpenFile} />
        </div>
      )}
    </div>
  );
}

/* bb step/bundle summary: a settled run of ≥BUNDLE_MIN same-kind tools
 * collapses into one summary row ("Explored 5 files"); expanding renders
 * each tool as a nested ToolRow with no extra indent beyond the seam. The
 * root dim already covers nested rows, so they are not dimmed again. */
export function ToolBundleRow({
  bundle,
  t,
  dimmed,
  onOpenFile,
  onOpenDiff,
}: {
  bundle: ToolBundle;
  t: Translator;
  dimmed?: boolean;
  onOpenFile?: (path: string) => void;
  onOpenDiff?: (path: string) => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className={cn("w-full", dimmed && PAST_ROW_DIM_CLASS)}>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="group/toggle flex w-full max-w-full cursor-pointer items-center gap-1 overflow-hidden px-2 py-0 text-left text-sm leading-5 text-muted-foreground transition-colors hover:text-foreground"
      >
        <ToolIcon
          name={bundle.tools[0].name}
          className="size-3.5 shrink-0 text-muted-foreground"
        />
        <span className="shrink-0">{bundleSegment(bundle, t)}</span>
        <span className="min-w-0 flex-1" />
        <Icon
          name="ChevronRight"
          className={cn(
            "pointer-events-none size-3 shrink-0 origin-center text-subtle-foreground/60 transition-[opacity,rotate] duration-200 ease-out",
            open
              ? "rotate-90"
              : "opacity-0 group-hover/toggle:opacity-100 group-focus-visible/toggle:opacity-100",
          )}
        />
      </button>
      {open && (
        <div className="flex flex-col">
          {bundle.tools.map((tool) => (
            <ToolRow
              key={tool.id}
              tool={tool}
              t={t}
              onOpenFile={onOpenFile}
              onOpenDiff={onOpenDiff}
            />
          ))}
        </div>
      )}
    </div>
  );
}
