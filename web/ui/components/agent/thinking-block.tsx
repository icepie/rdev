import { useEffect, useState } from "react";
import { cn } from "../../lib/utils";
import { Icon } from "../icon";
import { formatDuration } from "./transcript-rows";
import type { Translator } from "./types";

/* Collapsible reasoning row (bb TimelineReasoningDetail): same header rhythm
 * as tool rows; the body hangs off a 1px left seam (`ms-1.5 border-l pl-3`)
 * inside the shared `px-2 pb-1 pt-0` expansion wrapper. Header carries the
 * bb "Thought 46s" duration decoration: client-side timing from the first
 * reasoning frame to the last (live 1s tick while streaming). */
export function ThinkingBlock({
  text,
  streaming,
  startedAt,
  endedAt,
  t,
}: {
  text: string;
  streaming: boolean;
  /* First reasoning frame (wall clock ms); absent for history replays. */
  startedAt?: number;
  /* Last reasoning frame; irrelevant while streaming. */
  endedAt?: number;
  t: Translator;
}) {
  /* bb TimelineReasoningExpansion: auto-expanded while the reasoning stream
   * is live, collapses when it settles; a manual toggle sticks. */
  const [userOpen, setUserOpen] = useState<boolean | null>(null);
  const open = userOpen ?? streaming;
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!streaming) return;
    const iv = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(iv);
  }, [streaming]);

  const durationMs =
    startedAt !== undefined
      ? Math.max(0, (streaming ? now : (endedAt ?? now)) - startedAt)
      : null;

  return (
    <div className="w-full">
      <button
        type="button"
        onClick={() => setUserOpen(!open)}
        aria-expanded={open}
        className="group/toggle flex cursor-pointer items-center gap-1 px-2 py-0 text-sm leading-5 text-muted-foreground transition-colors hover:text-foreground"
      >
        <Icon
          name="AiBrain01"
          className="size-3.5 shrink-0 text-muted-foreground"
        />
        <span className="inline-flex min-w-0 max-w-full items-baseline gap-1 overflow-hidden whitespace-nowrap">
          <span className={cn(streaming && "animate-pulse")}>
            {streaming ? t("agent.thinking") : t("agent.thought")}
          </span>
          {durationMs !== null && durationMs > 1000 && (
            <span className="shrink-0 whitespace-pre tabular-nums text-muted-foreground">
              {" "}
              {formatDuration(durationMs)}
            </span>
          )}
        </span>
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
        <div className="px-2 pb-1 pt-0.5">
          <div
            className={cn(
              "ms-1.5 max-h-80 overflow-auto border-l border-border-seam pl-3 text-sm leading-relaxed break-words whitespace-pre-wrap text-muted-foreground select-text",
              streaming && "animate-pulse",
            )}
          >
            {text}
          </div>
        </div>
      )}
    </div>
  );
}
