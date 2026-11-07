import { useEffect, useId, useMemo, useRef, useState, type RefObject } from "react";
import { cn } from "../../lib/utils";
import { parseJobNotification } from "./job-notification";
import type { TranscriptItem, Translator } from "./types";

// BB ThreadTableOfContents: a quiet position rail opens two message indexes.
export function ThreadNavigation({ items, scrollRef, t }: {
  items: TranscriptItem[]; scrollRef: RefObject<HTMLDivElement | null>; t: Translator;
}) {
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState<"user" | "turn">("user");
  const [active, setActive] = useState<number | null>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const id = useId();
  const messages = useMemo(() => items.flatMap(item => {
    if (item.kind !== "user" && item.kind !== "turn") return [];
    if (item.kind === "user" && (item.job || parseJobNotification(item.text))) return [];
    const text = item.text.trim() || (item.kind === "user" ? item.images?.join(", ") : "");
    return text ? [{ key: item.key, kind: item.kind, text: text.replace(/\s+/g, " ").slice(0, 180) }] : [];
  }), [items]);
  const users = messages.filter(item => item.kind === "user");
  const signature = messages.map(item => item.key).join(",");
  useEffect(() => {
    const root = scrollRef.current;
    if (!root || typeof IntersectionObserver === "undefined") return;
    const visible = new Map<number, number>();
    const observer = new IntersectionObserver(entries => {
      for (const entry of entries) {
        const key = Number((entry.target as HTMLElement).dataset.threadMessage);
        if (entry.isIntersecting) visible.set(key, entry.boundingClientRect.top);
        else visible.delete(key);
      }
      const first = [...visible].sort((a, b) => a[1] - b[1])[0];
      if (first) setActive(first[0]);
    }, { root });
    root.querySelectorAll("[data-thread-message]").forEach(node => observer.observe(node));
    return () => observer.disconnect();
  }, [signature, scrollRef]);
  if (users.length < 3) return null;
  const rail = users.filter((_, index) => index % Math.max(1, Math.ceil(users.length / 40)) === 0);
  const activeUser = users.filter(item => active === null || item.key <= active).at(-1)?.key;
  return <div className="group/toc absolute right-0 top-4 z-10 w-8"
    onMouseEnter={() => setOpen(true)} onMouseLeave={event => {
      if (!event.currentTarget.contains(document.activeElement)) setOpen(false);
    }} onFocusCapture={() => setOpen(true)} onBlurCapture={event => {
      if (!event.currentTarget.contains(event.relatedTarget as Node)) setOpen(false);
    }} onKeyDown={event => { if (event.key === "Escape") { event.stopPropagation(); trigger.current?.focus(); setOpen(false); } }}>
    <button ref={trigger} type="button" aria-label={t("agent.navigation.title")} aria-expanded={open} aria-controls={id}
      onClick={() => setOpen(true)} className="flex max-h-64 w-8 cursor-pointer flex-col items-center gap-2 overflow-hidden py-2">
      {rail.map(item => <span key={item.key} aria-hidden className={cn("h-[3px] shrink-0 rounded-full transition-all duration-150", item.key === activeUser ? "w-5 bg-foreground/30 group-hover/toc:bg-foreground/70" : "w-3 bg-foreground/5 group-hover/toc:bg-foreground/20")} />)}
    </button>
    {open && <div id={id} className="absolute right-full top-0 w-[18.25rem] max-w-[calc(100vw-3rem)] pr-1">
      <div className="rounded-lg border border-border bg-popover p-1 shadow-lg">
        <div role="group" aria-label={t("agent.navigation.title")} className="flex gap-1 pb-1">
          {(["turn", "user"] as const).map(kind => <button key={kind} type="button" aria-pressed={tab === kind} onClick={() => setTab(kind)}
            className={cn("flex-1 cursor-pointer rounded-md px-2 py-1.5 text-xs", tab === kind ? "bg-state-hover text-foreground" : "text-muted-foreground hover:bg-state-hover")}>
            {t(kind === "turn" ? "agent.navigation.agent" : "agent.navigation.user")}
          </button>)}
        </div>
        <ul className="max-h-64 overflow-y-auto overflow-x-hidden">
          {messages.filter(item => item.kind === tab).map(item => <li key={item.key}>
            <button type="button" aria-current={active === item.key ? "true" : undefined}
              onClick={() => { scrollRef.current?.querySelector(`[data-thread-message="${item.key}"]`)?.scrollIntoView({ block: "start", behavior: window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" }); setActive(item.key); }}
              className={cn("w-full cursor-pointer rounded-md px-2 py-1.5 text-left text-xs leading-snug break-words hover:bg-state-hover", active === item.key ? "bg-state-hover text-foreground" : "text-muted-foreground")}>{item.text}</button>
          </li>)}
        </ul>
      </div>
    </div>}
  </div>;
}
