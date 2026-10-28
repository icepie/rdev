import { useMemo, useState } from "react";
import { cn } from "../../lib/utils";
import { Icon } from "../icon";
import { Popover, PopoverContent, PopoverTrigger } from "../popover";
import type { Translator } from "./types";

/* bb ProjectSelector adapted: the rdev "project" is the current thread's
 * working directory, so options are the known per-thread cwds of this
 * device (bb pulls projects from a backend API; rdev has no project
 * entity). Selecting an option = set_cwd. bb's Reuse/Branch pickers are
 * environment/worktree concepts with no rdev counterpart — not ported.
 * Deviation: trigger uses h-6 (the status strip is a fixed 24px row; bb's
 * OPTION trigger is h-8 for the composer footer row). */
export function CwdPicker({
  cwd,
  options,
  disabled,
  onSelect,
  onCreateProject,
  t,
}: {
  cwd: string;
  /* Known device cwds (deduped, includes the current one when set). */
  options: string[];
  disabled?: boolean;
  onSelect: (path: string) => void;
  /* bb ProjectSelector: "New project" closes the popover and opens the
   * create-project dialog (ProjectPathDialog). */
  onCreateProject: () => void;
  t: Translator;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return q ? options.filter((o) => o.toLowerCase().includes(q)) : options;
  }, [options, query]);

  const label = cwd.split(/[\\/]+/).filter(Boolean).pop() ?? "";

  return (
    <Popover
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) setQuery("");
      }}
    >
      <PopoverTrigger asChild>
        <button
          type="button"
          disabled={disabled}
          title={cwd || undefined}
          aria-label={t("agent.project")}
          className="flex h-6 w-fit max-w-full min-w-0 cursor-pointer items-center justify-start gap-1 border-none bg-transparent px-1 text-xs leading-tight text-muted-foreground shadow-none hover:text-muted-foreground disabled:pointer-events-none disabled:opacity-50"
        >
          <Icon
            name={cwd ? "Folder" : "FolderMinus"}
            className="size-4 shrink-0"
          />
          <span className="min-w-0 truncate">
            {cwd ? label : t("agent.noProject")}
          </span>
          <Icon
            name="ChevronDown"
            className="size-3.5 shrink-0 text-subtle-foreground/75"
          />
        </button>
      </PopoverTrigger>
      <PopoverContent
        side="top"
        align="start"
        aria-label={t("agent.project")}
        className="w-52 flex-col overflow-hidden p-0"
      >
        {options.length > 5 && (
          <div className="shrink-0 border-b border-border-seam px-2">
            <input
              value={query}
              aria-label={t("agent.searchProjects")}
              placeholder={t("agent.searchProjects")}
              className="h-8 w-full bg-transparent text-xs outline-none placeholder:text-muted-foreground/70"
              onChange={(e) => setQuery(e.target.value)}
            />
          </div>
        )}
        <div className="shrink-0 px-2 pt-1.5 text-2xs text-muted-foreground">
          {t("agent.project")}
        </div>
        <div className="max-h-60 min-h-0 overflow-y-auto p-1">
          {filtered.map((path) => (
            <button
              key={path}
              type="button"
              title={path}
              aria-current={path === cwd ? "true" : undefined}
              className="flex w-full cursor-pointer items-center gap-2 rounded-sm px-2 py-[0.3125rem] text-left text-xs transition-colors hover:bg-state-hover"
              onClick={() => {
                onSelect(path);
                setOpen(false);
              }}
            >
              <Icon name="Folder" className="size-4 shrink-0 text-muted-foreground" />
              <span className="min-w-0 flex-1 truncate">
                {path.split(/[\\/]+/).filter(Boolean).pop() ?? path}
              </span>
              <Icon
                name="Check"
                className={cn(
                  "ml-auto size-4 shrink-0",
                  path === cwd ? "opacity-100" : "opacity-0",
                )}
              />
            </button>
          ))}
          {filtered.length === 0 && (
            <div className="px-2 py-1.5 text-xs text-muted-foreground">
              {t("agent.noProjectsFound")}
            </div>
          )}
        </div>
        <div className="my-1 h-px shrink-0 bg-border" />
        <div className="p-1">
          <button
            type="button"
            className="flex w-full cursor-pointer items-center gap-2 rounded-sm px-2 py-[0.3125rem] text-left text-xs transition-colors hover:bg-state-hover"
            onClick={() => {
              /* bb: close the popover, then open the create-project
               * dialog (ProjectPathDialog + RemotePathBrowser). */
              setOpen(false);
              onCreateProject();
            }}
          >
            <Icon name="FolderPlus" className="size-4 shrink-0 text-muted-foreground" />
            <span className="min-w-0 flex-1 truncate">{t("agent.newProject")}</span>
          </button>
          <button
            type="button"
            aria-current={!cwd ? "true" : undefined}
            className="flex w-full cursor-pointer items-center gap-2 rounded-sm px-2 py-[0.3125rem] text-left text-xs transition-colors hover:bg-state-hover"
            onClick={() => {
              onSelect("");
              setOpen(false);
            }}
          >
            <Icon name="FolderMinus" className="size-4 shrink-0 text-muted-foreground" />
            <span className="min-w-0 flex-1 truncate">
              {t("agent.dontWorkInProject")}
            </span>
            <Icon
              name="Check"
              className={cn("ml-auto size-4 shrink-0", cwd ? "opacity-0" : "opacity-100")}
            />
          </button>
        </div>
      </PopoverContent>
    </Popover>
  );
}
