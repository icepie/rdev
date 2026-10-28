import { useState } from "react";
import { cn } from "../../lib/utils";
import { Button } from "../button";
import { CodeBlock } from "../markdown";
import { Icon } from "../icon";
import { ToolIcon } from "./tool-icon";
import type { ApprovalItem, Translator } from "./types";

/* Inline approval card for dangerous tool calls: attention border while
 * pending with allow / always-allow / deny actions; recolors once decided. */
export function ApprovalCard({
  item,
  t,
  onDecide,
}: {
  item: ApprovalItem;
  t: Translator;
  onDecide: (id: string, allow: boolean, always: boolean) => void;
}) {
  const [showArgs, setShowArgs] = useState(false);
  let prettyArgs = item.args;
  try {
    prettyArgs = JSON.stringify(JSON.parse(item.args), null, 2);
  } catch {
    /* keep raw */
  }
  const pending = item.state === "pending";
  return (
    <div
      className={cn(
        "w-full rounded-lg border px-3.5 py-2.5 text-xs",
        pending
          ? "border-border bg-surface-recessed"
          : item.state === "denied"
            ? "border-destructive"
            : "border-success",
      )}
    >
      <div className="flex items-center gap-2 text-xs leading-5 text-muted-foreground">
        {pending && (
          <span className="size-2 shrink-0 rounded-full bg-attention ring-[3px] ring-surface-attention" />
        )}
        <ToolIcon
          name={item.name}
          className="size-3.5 shrink-0 text-muted-foreground"
        />
        <span className="shrink-0 font-medium text-foreground">{item.name}</span>
        <span className="flex-1 truncate font-medium text-foreground">
          {item.summary || item.args.replace(/\s+/g, " ").trim()}
        </span>
        {!pending && (
          <span
            className={cn(
              "shrink-0 text-xs font-medium",
              item.state === "denied"
                ? "text-destructive-text"
                : "text-success",
            )}
          >
            {item.state === "denied"
              ? t("agent.denied")
              : item.state === "always"
                ? t("agent.alwaysApproved")
                : t("agent.approved")}
          </span>
        )}
      </div>
      <button
        type="button"
        onClick={() => setShowArgs((o) => !o)}
        aria-expanded={showArgs}
        className="mt-1 flex cursor-pointer items-center gap-1 text-2xs tracking-wide text-muted-foreground uppercase transition-colors hover:text-foreground"
      >
        <Icon
          name="ChevronRight"
          className={cn(
            "size-3 shrink-0 text-subtle-foreground/60 transition-transform",
            showArgs && "rotate-90",
          )}
        />
        {t("agent.toolArgs")}
      </button>
      {showArgs && (
        <CodeBlock className="mt-1 max-h-60 overflow-auto break-words whitespace-pre-wrap select-text">
          {prettyArgs}
        </CodeBlock>
      )}
      {pending && (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <Button
            variant="ghost"
            size="sm"
            className="mr-auto"
            onClick={() => onDecide(item.id, false, false)}
          >
            {t("agent.deny")}
          </Button>
          <Button size="sm" onClick={() => onDecide(item.id, true, false)}>
            {t("agent.approve")}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => onDecide(item.id, true, true)}
          >
            {t("agent.alwaysAllow")}
          </Button>
        </div>
      )}
    </div>
  );
}
