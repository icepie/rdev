import { cn } from "../../lib/utils";
import { Icon } from "../icon";
import { Markdown } from "../markdown";
import type { LocalFileLink } from "../markdown-local-file-link";
import { MessageActionBar } from "./message-action-bar";
import { ThinkingBlock } from "./thinking-block";
import { ToolBundleRow, ToolRow } from "./tool-row";
import { formatDuration, groupTools } from "./transcript-rows";
import type { Translator, TurnItem } from "./types";

/* Assistant turn: collapsible thinking block, streamed markdown, bb-style
 * work rows (per-kind ToolRow / collapsed ToolBundleRow via the pure
 * groupTools projection), clickable file chips, usage, and the bb
 * hover-revealed action bar (copy / add to chat / timestamp). Deviations:
 * the bar is only rendered once the turn settles (text && !streaming) so it
 * never jumps around while tools keep streaming in; bb renders the h-5 slot
 * for the top-level message regardless. The todo checklist lives in the
 * TodoCard above the composer (bb ThreadTodoCard), not in the transcript.
 * bb PAST_ROW_DIM_CLASS_NAME: rows of turns older than the transcript's
 * last turn render at opacity-40 (`dimmed` from the caller). */
export function TurnView({
  turn,
  t,
  copied,
  dimmed,
  onCopy,
  onAddToChat,
  onOpenFile,
  onOpenDiff,
  onOpenLocalFile,
}: {
  turn: TurnItem;
  t: Translator;
  copied: boolean;
  /* Past-turn dimming (bb pastRowDimClassName, work rows only). */
  dimmed?: boolean;
  onCopy: (text: string) => void;
  onAddToChat: (text: string) => void;
  /* bb thread→panel linkage: preview tab / focused diff deck / markdown
   * local-file links. */
  onOpenFile?: (path: string) => void;
  onOpenDiff?: (path: string) => void;
  onOpenLocalFile?: (link: LocalFileLink) => void;
}) {
  const groups = groupTools(turn.tools, turn.streaming);
  const workedMs =
    turn.time !== undefined && turn.endedAt !== undefined
      ? Math.max(0, turn.endedAt - turn.time)
      : null;
  return (
    <div className="group/message flex max-w-full min-w-0 flex-col gap-1.5 self-start">
      {turn.reasoning && (
        <ThinkingBlock
          text={turn.reasoning}
          streaming={turn.streamingReasoning}
          startedAt={turn.reasoningStartedAt}
          endedAt={turn.reasoningEndedAt}
          t={t}
        />
      )}
      {turn.text && (
        <div
          className={cn(
            "w-full px-2 text-sm font-normal leading-relaxed",
            turn.streaming &&
              "after:ml-0.5 after:animate-pulse after:text-timeline-accent after:content-['▍']",
          )}
        >
          <Markdown
            text={turn.text}
            className="leading-relaxed"
            onOpenLocalFile={onOpenLocalFile}
          />
        </div>
      )}
      {groups.length > 0 && (
        /* bb nested timeline group: consecutive work rows of one turn hang
         * off a 1px vertical seam on the left. */
        <div className="[--md-table-start-offset:0px] relative my-1 pl-3 pr-2 before:pointer-events-none before:absolute before:bottom-1 before:left-1.5 before:top-0 before:w-px before:bg-border-seam before:content-['']">
          {groups.map((group) =>
            group.type === "bundle" ? (
              <ToolBundleRow
                key={group.bundle.tools[0]?.id ?? group.bundle.kind}
                bundle={group.bundle}
                t={t}
                dimmed={dimmed}
                onOpenFile={onOpenFile}
                onOpenDiff={onOpenDiff}
              />
            ) : (
              <ToolRow
                key={group.tool.id || group.tool.name}
                tool={group.tool}
                t={t}
                dimmed={dimmed}
                onOpenFile={onOpenFile}
                onOpenDiff={onOpenDiff}
              />
            ),
          )}
        </div>
      )}
      {workedMs !== null && workedMs > 1000 && (
        /* bb turn summary: "Worked for 2m 5s". */
        <div className="px-2 text-sm tabular-nums text-subtle-foreground">
          {t("agent.workedFor", { duration: formatDuration(workedMs) })}
        </div>
      )}
      {turn.files && turn.files.length > 0 && (
        <div className="mt-2 flex flex-wrap gap-1.5">
          {turn.files.map((file) => (
            <button
              key={file}
              type="button"
              title={file}
              onClick={() => onOpenFile?.(file)}
              className="inline-flex cursor-pointer items-center gap-1 rounded-full border border-border bg-surface-recessed px-2 py-0.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
            >
              <Icon name="EditFile" className="size-3 shrink-0" />
              {file.split(/[\\/]/).pop()}
            </button>
          ))}
        </div>
      )}
      {turn.text && !turn.streaming && (
        <MessageActionBar
          align="start"
          text={turn.text}
          timestamp={turn.time}
          copied={copied}
          onCopy={onCopy}
          onAddToChat={onAddToChat}
          t={t}
        />
      )}
    </div>
  );
}
