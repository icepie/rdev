import { cn } from "../../lib/utils";
import { Popover, PopoverContent, PopoverTrigger } from "../popover";
import type { Translator } from "./types";

/* Context-window ring with details from the latest input snapshot. */

function toneFor(pct: number): string {
  if (pct >= 90) return "text-destructive-text";
  if (pct >= 75) return "text-warning-text";
  return "text-muted-foreground";
}

const RADIUS = 6.5;
const CIRCUMFERENCE = 2 * Math.PI * RADIUS;

export function ContextWindowIndicator({
  used,
  contextWindow,
  t,
  onCompact,
  busy = false,
}: {
  /* usage.inputTokens of the thread's latest completed turn. */
  used: number;
  /* Current model's context window (> 0). */
  contextWindow: number;
  t: Translator;
  onCompact?: () => void;
  busy?: boolean;
}) {
  const pct = Math.round(Math.min(1, Math.max(0, used / contextWindow)) * 100);
  const left = Math.max(0, 100 - pct);
  const tone = toneFor(pct);
  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label={t("agent.contextWindowUsed", { pct })}
          className="inline-flex size-6 shrink-0 cursor-pointer items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-state-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <svg aria-hidden="true" viewBox="0 0 16 16" className={cn("size-4 shrink-0", tone)}>
            <circle
              cx="8"
              cy="8"
              r={RADIUS}
              fill="none"
              strokeWidth="3"
              className="stroke-border-hairline"
            />
            <circle
              cx="8"
              cy="8"
              r={RADIUS}
              fill="none"
              stroke="currentColor"
              strokeWidth="3"
              strokeLinecap="round"
              strokeDasharray={CIRCUMFERENCE}
              strokeDashoffset={CIRCUMFERENCE * (1 - pct / 100)}
              transform="rotate(-90 8 8)"
            />
          </svg>
        </button>
      </PopoverTrigger>
      <PopoverContent
        side="top"
        align="end"
        sideOffset={8}
        className="w-auto border-0 bg-transparent p-0 shadow-none"
      >
        <div className="w-56 rounded-md border bg-popover p-2 text-popover-foreground shadow-md">
          <div className="flex flex-col gap-2">
            <div className="flex items-baseline justify-between gap-2 text-xs">
              <span className="text-muted-foreground">
                {t("agent.estimatedContext")}
              </span>
              <span className={cn("font-medium tabular-nums", tone)}>
                {t("agent.usageUsed", { pct })}
              </span>
            </div>
            <div className="relative h-1.5 w-full overflow-hidden rounded-full bg-border">
              <div
                className={cn("h-full rounded-full bg-current", tone)}
                style={{ width: pct + "%" }}
              />
            </div>
            <div className="flex items-baseline justify-between gap-2 text-xs tabular-nums text-muted-foreground">
              <span>{t("agent.usageLeft", { pct: left })}</span>
            </div>
            {onCompact && <button type="button" disabled={busy} onClick={onCompact}
              className="rounded-md border border-border px-2 py-1.5 text-xs hover:bg-state-hover disabled:cursor-not-allowed disabled:opacity-50">
              {t("agent.cmd.compact")}
            </button>}
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}
