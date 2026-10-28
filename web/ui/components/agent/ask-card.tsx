import { useEffect, useRef, useState } from "react";
import { cn } from "../../lib/utils";
import { Button } from "../button";
import { Icon } from "../icon";
import type { AskItem, Translator } from "./types";

/* bb QuestionForm (packages/shared-ui question-form.tsx) adapted to the
 * single-question ask op: radio option rows with numeric shortcuts, an
 * "Other…" row revealing an auto-grow free-text textarea, and a
 * Cancel / Submit footer. rdev answers are single-select only. */
const FREE_TEXT_MIN_HEIGHT = 84;
const FREE_TEXT_MAX_HEIGHT = 158;
const OTHER_VALUE = "__other__";

function AskOptionRow({
  checked,
  label,
  description,
  shortcut,
  onSelect,
}: {
  checked: boolean;
  label: string;
  description?: string;
  shortcut?: string;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={checked}
      onClick={onSelect}
      className={cn(
        "flex w-full cursor-pointer items-start gap-2.5 rounded-md px-2.5 py-1.5 text-left transition-colors",
        checked ? "bg-surface-selected" : "hover:bg-state-hover",
      )}
    >
      <span
        className={cn(
          "flex size-4 shrink-0 items-center justify-center rounded-full border",
          checked
            ? "border-primary bg-primary text-primary-foreground"
            : "border-input",
        )}
      >
        {checked ? <Icon name="Check" className="size-3" aria-hidden /> : null}
      </span>
      <span className="min-w-0 flex-1">
        <span className="block text-xs text-foreground">{label}</span>
        {description ? (
          <span className="mt-0.5 block text-xs leading-snug text-muted-foreground">
            {description}
          </span>
        ) : null}
      </span>
      {shortcut ? (
        <kbd
          aria-hidden="true"
          className="shrink-0 text-xs font-normal text-subtle-foreground"
        >
          {shortcut}
        </kbd>
      ) : null}
    </button>
  );
}

export function AskCard({
  item,
  t,
  onAnswer,
}: {
  item: AskItem;
  t: Translator;
  onAnswer: (id: string, answer: string) => void;
}) {
  const [selected, setSelected] = useState<string | null>(null);
  const [freeText, setFreeText] = useState("");
  const textRef = useRef<HTMLTextAreaElement>(null);
  const pending = item.answer === null;
  const dismissed = item.answer === "";
  const otherSelected = selected === OTHER_VALUE;

  /* bb auto-grow free-text area. */
  useEffect(() => {
    const el = textRef.current;
    if (!el) return;
    el.style.height = "0px";
    el.style.height = `${Math.min(Math.max(el.scrollHeight, FREE_TEXT_MIN_HEIGHT), FREE_TEXT_MAX_HEIGHT)}px`;
  }, [freeText, otherSelected]);

  const submit = () => {
    if (otherSelected) {
      const value = freeText.trim();
      if (value) onAnswer(item.id, value);
      return;
    }
    if (selected !== null) onAnswer(item.id, selected);
  };

  /* bb numeric shortcuts: 1..9 pick the option, 0/"o"-style rows are not
   * part of the ask op. Only when focus is inside the card. */
  const handleKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (!pending || e.nativeEvent.isComposing) return;
    const target = e.target as HTMLElement;
    if (target.tagName === "TEXTAREA" || target.tagName === "INPUT") return;
    const n = Number(e.key);
    if (Number.isInteger(n) && n >= 1 && n <= item.options.length) {
      e.preventDefault();
      setSelected(item.options[n - 1].label);
    }
  };

  return (
    <div
      tabIndex={-1}
      onKeyDown={handleKeyDown}
      className={cn(
        "w-full rounded-lg border px-3.5 py-2.5 text-xs focus-visible:outline-none",
        pending
          ? "border-border bg-surface-recessed"
          : "border-border",
      )}
    >
      <div className="flex items-start gap-2 text-xs leading-5 text-muted-foreground">
        {pending && (
          <span className="mt-1.5 size-2 shrink-0 rounded-full bg-attention ring-[3px] ring-surface-attention" />
        )}
        <Icon
          name="CircleQuestion"
          className="mt-0.5 size-4 shrink-0 text-muted-foreground"
        />
        <span className="break-words whitespace-pre-wrap select-text">
          {item.question}
        </span>
      </div>
      {pending ? (
        <div className="mt-2">
          <div className="space-y-0.5" role="radiogroup">
            {item.options.map((o, i) => (
              <AskOptionRow
                key={o.label}
                checked={selected === o.label}
                label={o.label}
                description={o.description}
                shortcut={i < 9 ? String(i + 1) : undefined}
                onSelect={() => setSelected(o.label)}
              />
            ))}
            <AskOptionRow
              checked={otherSelected}
              label={t("agent.askOther")}
              onSelect={() => {
                setSelected(OTHER_VALUE);
                textRef.current?.focus();
              }}
            />
          </div>
          {otherSelected || item.options.length === 0 ? (
            <textarea
              ref={textRef}
              value={freeText}
              onChange={(e) => setFreeText(e.target.value)}
              onKeyDown={(e) => {
                if (
                  e.key === "Enter" &&
                  !e.shiftKey &&
                  !e.nativeEvent.isComposing
                ) {
                  e.preventDefault();
                  submit();
                }
              }}
              placeholder={t("agent.askPlaceholder")}
              style={{
                minHeight: FREE_TEXT_MIN_HEIGHT,
                maxHeight: FREE_TEXT_MAX_HEIGHT,
              }}
              className="mt-2 block w-full resize-none overflow-y-auto rounded-md border border-border bg-surface-raised px-3 py-2 text-xs leading-relaxed text-foreground placeholder:text-muted-foreground focus-visible:border-ring/50 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring/40"
            />
          ) : null}
          <div className="mt-3 flex shrink-0 items-center justify-between gap-2">
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={() => onAnswer(item.id, "")}
            >
              {t("agent.askCancel")}
            </Button>
            <Button
              type="button"
              size="sm"
              disabled={
                selected === null || (otherSelected && !freeText.trim())
              }
              onClick={submit}
            >
              {t("agent.askSubmit")}
            </Button>
          </div>
        </div>
      ) : (
        <div className="mt-1.5 text-xs text-muted-foreground">
          {dismissed
            ? t("agent.askDismissed")
            : t("agent.askAnswered", { answer: item.answer ?? "" })}
        </div>
      )}
    </div>
  );
}
