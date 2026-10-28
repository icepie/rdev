import { cn } from "../../lib/utils";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "../dropdown-menu";
import { Icon } from "../icon";
import { Tooltip, TooltipContent, TooltipTrigger } from "../tooltip";
import type { Translator } from "./types";

const ACTION_BUTTON_CLASS =
  "inline-flex size-5 cursor-pointer items-center justify-center text-muted-foreground hover:text-foreground disabled:pointer-events-none disabled:opacity-40";

/* bb MessageActionBar: 20px hover-revealed slot under a message (assistant
 * and user alike). Inline Copy + ⋯ overflow (Add to chat + timestamp
 * footer). bb's Edit / Fork / Copy link are not ported: rdev has no inline
 * message editor, no fork-thread op, and no message links. */
export function MessageActionBar({
  align,
  text,
  timestamp,
  copied,
  onCopy,
  onAddToChat,
  t,
}: {
  /* "start" = assistant (left), "end" = user (right). */
  align: "start" | "end";
  /* Markdown source copied / inserted into the composer. */
  text: string;
  /* Wall-clock ms; history items have none and skip the footer row. */
  timestamp?: number;
  copied: boolean;
  onCopy: (text: string) => void;
  onAddToChat: (text: string) => void;
  t: Translator;
}) {
  return (
    /* 20px slot: the bar appears on hover without shifting the layout. */
    <div className="relative h-5 w-full">
      <div
        className={cn(
          "absolute top-0 flex max-w-full items-center gap-2 overflow-hidden opacity-0 transition-opacity group-hover/message:opacity-100 group-focus-within/message:opacity-100 has-[[data-state=open]]:opacity-100",
          align === "start" ? "left-0 -ml-1" : "right-[13px]",
        )}
      >
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              type="button"
              aria-label={t("agent.copyMessage")}
              className={cn(ACTION_BUTTON_CLASS, "focus-visible:opacity-100")}
              onClick={() => onCopy(text)}
            >
              <Icon name={copied ? "Check" : "Copy"} className="size-3" />
            </button>
          </TooltipTrigger>
          <TooltipContent side="bottom">{t("agent.copyMessage")}</TooltipContent>
        </Tooltip>
        <DropdownMenu>
          <Tooltip>
            <TooltipTrigger asChild>
              <DropdownMenuTrigger asChild>
                <button
                  type="button"
                  aria-label={t("agent.messageActions")}
                  className={cn(
                    ACTION_BUTTON_CLASS,
                    "data-[state=open]:text-foreground data-[state=open]:opacity-100",
                  )}
                >
                  <Icon name="MoreHorizontal" className="size-3" />
                </button>
              </DropdownMenuTrigger>
            </TooltipTrigger>
            <TooltipContent side="bottom">
              {t("agent.messageActions")}
            </TooltipContent>
          </Tooltip>
          <DropdownMenuContent
            align={align === "start" ? "start" : "end"}
            className="max-w-[min(16rem,calc(100vw-1rem))]"
          >
            <DropdownMenuItem onSelect={() => onAddToChat(text)}>
              <Icon name="MessageSquarePlus" className="size-4 shrink-0" />
              {t("agent.addToChat")}
            </DropdownMenuItem>
            {timestamp !== undefined && (
              <div className="mt-1 border-t border-border px-2 pt-2 pb-1 text-xs text-subtle-foreground">
                <time
                  dateTime={new Date(timestamp).toISOString()}
                  title={new Date(timestamp).toLocaleString()}
                >
                  {new Date(timestamp).toLocaleString()}
                </time>
              </div>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </div>
  );
}
