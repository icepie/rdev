import { DisclosureBody } from "../disclosure-body";
import { useId, useState } from "react";
import { cn } from "../../lib/utils";
import { Icon } from "../icon";
import type { TodoItem, Translator } from "./types";

/* bb ThreadTodoCard sort order: active work first, then pending, done last. */
const STATUS_SORT_RANK: Record<string, number> = {
  in_progress: 0,
  blocked: 1,
  pending: 2,
  abandoned: 3,
  completed: 4,
};

function statusIcon(status: string) {
  if (status === "completed")
    return (
      <Icon name="Check" className="size-3.5 shrink-0 text-subtle-foreground" />
    );
  if (status === "in_progress")
    return (
      <Icon name="Square" className="size-3.5 shrink-0 text-foreground" />
    );
  if (status === "blocked")
    return <Icon name="AlertCircle" className="size-3.5 shrink-0 text-warning-text" />;
  if (status === "abandoned")
    return <Icon name="CircleX" className="size-3.5 shrink-0 text-muted-foreground" />;
  return (
    <Icon name="Square" className="size-3.5 shrink-0 text-muted-foreground/45" />
  );
}

/* bb promptbox/banner/ThreadTodoCard: a composer-width card ABOVE the
 * composer. Collapsed: "x/y completed" + hover chevron; expanded: the
 * sorted checklist (max-h-40 scroll). */
export function TodoCard({ todos, t }: { todos: TodoItem[]; t: Translator }) {
  const bodyId = useId();
  const [open, setOpen] = useState(false);
  if (todos.length === 0) return null;
  const done = todos.filter((x) => x.status === "completed").length;
  const ordered = [...todos].sort(
    (a, b) => (STATUS_SORT_RANK[a.status] ?? 1) - (STATUS_SORT_RANK[b.status] ?? 1),
  );
  return (
    <div className="w-full overflow-hidden rounded-lg border border-border bg-surface-raised-solid">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        aria-controls={bodyId}
        className="flex min-h-8 w-full cursor-pointer items-center gap-1.5 px-3 py-1.5 text-left transition-colors hover:bg-background/80"
      >
        <Icon name="ListTodo" className="size-3.5 shrink-0 text-foreground" />
        <span className="min-w-0 flex-1 truncate text-xs text-foreground">
          {t("agent.todosDone", { done, total: todos.length })}
        </span>
        <Icon
          name="ChevronDown"
          className={cn(
            "size-3.5 shrink-0 text-muted-foreground transition-transform",
            open && "rotate-180",
          )}
        />
      </button>
      <DisclosureBody open={open}>
        <ul id={bodyId} className="max-h-40 space-y-1 overflow-y-auto px-2.5 pt-2 pb-2">
          {ordered.map((todo, i) => (
            <li
              key={i}
              className={cn(
                "flex min-w-0 items-center gap-2 rounded-md px-2 py-0.5 text-xs",
                todo.status === "in_progress" && "bg-background/70",
                todo.status === "blocked" && "bg-surface-attention",
              )}
            >
              {statusIcon(todo.status)}
              <span
                className={cn(
                  "min-w-0 flex-1 truncate",
                  todo.status === "completed"
                    ? "text-subtle-foreground line-through decoration-subtle-foreground/70"
                    : todo.status === "abandoned"
                      ? "text-muted-foreground line-through decoration-muted-foreground/60"
                      : todo.status === "blocked"
                        ? "font-medium text-warning-text"
                    : todo.status === "in_progress"
                      ? "font-medium text-foreground"
                      : "text-muted-foreground",
                )}
                title={todo.content}
              >
                {todo.content}
              </span>
              {todo.status === "blocked" && <span className="text-[10px] text-warning-text">{t("agent.todoBlocked")}</span>}
              {todo.blocker && <span className="max-w-[35%] truncate text-[10px] text-warning-text" title={todo.blocker}>{todo.blocker}</span>}
              {todo.status === "abandoned" && <span className="text-[10px] text-muted-foreground">{t("agent.todoAbandoned")}</span>}
            </li>
          ))}
        </ul>
      </DisclosureBody>
    </div>
  );
}
