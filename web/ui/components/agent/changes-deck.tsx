/* ChangesDeck: the working-tree change set as a scrollable deck of
 * per-file cards (rounded border, sticky header: collapse chevron, mono
 * filename link, copy path, +N −M tally), each body rendered with
 * PierreFileDiff (diff-view.tsx). Toolbar: collapse-all + a settings
 * dropdown with Unified/Split and Wrap lines. Split view is the default
 * once the deck is ≥760px wide; the user's manual choice persists in
 * localStorage. */
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { cn } from "../../lib/utils";
import { copyText } from "../../lib/clipboard";
import { Button } from "../button";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "../dropdown-menu";
import { Icon } from "../icon";
import { Tooltip, TooltipContent, TooltipTrigger } from "../tooltip";
import { PierreFileDiff, type DiffView } from "./diff-view";
import type { ChangedFile, ChangesState, DiffState, Translator } from "./types";

const VIEW_STORAGE_KEY = "rdev.agent.diffView";
const WRAP_STORAGE_KEY = "rdev.agent.diffWrap";
/* Auto-collapse the deck when more than 10 files change. */
const AUTO_COLLAPSE_FILE_COUNT = 10;
/* Split view becomes the default at ≥760px panel width. */
const SPLIT_DEFAULT_MIN_WIDTH = 760;

function readStoredView(): DiffView | null {
  const v = localStorage.getItem(VIEW_STORAGE_KEY);
  return v === "unified" || v === "split" ? v : null;
}

function splitPath(path: string): { dir: string; base: string } {
  const i = path.lastIndexOf("/");
  return i === -1
    ? { dir: "", base: path }
    : { dir: path.slice(0, i + 1), base: path.slice(i + 1) };
}

/* "+adds -dels" tally; zero sides hidden. */
function DiffTally({ adds, dels }: { adds: number; dels: number }) {
  if (adds === 0 && dels === 0) return null;
  return (
    <span className="shrink-0 text-xs whitespace-nowrap">
      {adds > 0 && <span className="text-diff-added">+{adds}</span>}
      {adds > 0 && dels > 0 && " "}
      {dels > 0 && <span className="text-diff-removed">-{dels}</span>}
    </span>
  );
}

/* One file card: header + collapsible diff body. Requests its patch
 * lazily the first time it renders expanded. */
function DiffFileCard({
  file,
  diff,
  collapsed,
  view,
  wrap,
  connected,
  onToggleCollapsed,
  onRequestDiff,
  onOpenFile,
  t,
}: {
  file: ChangedFile;
  diff: DiffState | undefined;
  collapsed: boolean;
  view: DiffView;
  wrap: boolean;
  connected: boolean;
  onToggleCollapsed: () => void;
  onRequestDiff: (path: string) => void;
  onOpenFile: (path: string) => void;
  t: Translator;
}) {
  const { dir, base } = splitPath(file.path);

  useEffect(() => {
    if (!collapsed && diff === undefined && connected) {
      onRequestDiff(file.path);
    }
  }, [collapsed, diff, connected, file.path, onRequestDiff]);

  const body = () => {
    if (diff === undefined) {
      return (
        <div className="flex items-center justify-center py-6 text-muted-foreground">
          <Icon name="Spinner" className="size-4 animate-spin" />
        </div>
      );
    }
    if (diff.error === "untracked") {
      return (
        <div className="px-3 py-3 text-xs text-muted-foreground">
          {t("agent.untracked")}
        </div>
      );
    }
    if (diff.error) {
      return (
        <div className="px-3 py-3 text-xs break-words text-destructive">
          {diff.error}
        </div>
      );
    }
    if (!diff.diff) {
      return (
        <div className="px-3 py-3 text-xs text-muted-foreground">
          {t("agent.emptyDiff")}
        </div>
      );
    }
    return (
      <PierreFileDiff
        patch={diff.diff}
        view={view}
        overflow={wrap ? "wrap" : "scroll"}
      />
    );
  };

  return (
    <div
      data-diff-path={file.path}
      className="overflow-clip rounded-lg border border-border bg-background"
    >
      {/* Header: chevron, filename link, copy, tally. */}
      <div
        className={cn(
          "sticky top-0 z-30 flex w-full min-w-0 flex-wrap items-center justify-between gap-x-2 gap-y-1 border-b border-border bg-background py-1.5 pr-3 pl-2 text-xs font-medium text-foreground",
          collapsed && "border-b-0",
        )}
      >
        <span className="flex min-w-0 grow basis-48 items-center">
          <button
            type="button"
            className="inline-flex w-8 shrink-0 cursor-pointer items-center justify-center text-muted-foreground transition-colors hover:text-foreground"
            onClick={onToggleCollapsed}
            aria-label={
              collapsed
                ? t("agent.expandFile", { label: file.path })
                : t("agent.collapseFile", { label: file.path })
            }
            aria-expanded={!collapsed}
          >
            <Icon
              name="ChevronRight"
              className={cn(
                "size-3.5 shrink-0 transition-transform duration-150",
                !collapsed && "rotate-90",
              )}
            />
          </button>
          <span className="flex min-w-0 items-center gap-1.5 pl-[1ch]">
            <button
              type="button"
              title={file.path}
              className="min-w-0 cursor-pointer truncate text-left font-mono text-xs leading-5 font-medium text-foreground underline-offset-2 hover:underline"
              onClick={() => onOpenFile(file.path)}
            >
              {dir && (
                <span className="text-muted-foreground">{dir}</span>
              )}
              {base}
            </button>
            <button
              type="button"
              aria-label={t("agent.copyPath", { label: file.path })}
              title={t("agent.copyPath", { label: file.path })}
              className="inline-flex size-5 shrink-0 cursor-pointer items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground"
              onClick={() => void copyText(file.path)}
            >
              <Icon name="Copy" className="size-3" />
            </button>
          </span>
        </span>
        <span className="ml-auto flex shrink-0 items-center gap-2">
          <DiffTally adds={file.adds} dels={file.dels} />
        </span>
      </div>
      {collapsed ? null : body()}
    </div>
  );
}

export function ChangesDeck({
  changes,
  diffs,
  connected,
  onRequestDiff,
  onOpenFile,
  focusPath,
  t,
}: {
  changes: ChangesState;
  diffs: Record<string, DiffState>;
  connected: boolean;
  onRequestDiff: (path: string) => void;
  onOpenFile: (path: string) => void;
  /* bb pending-git-diff-intent: one-shot request to expand + scroll to a
   * file card (transcript file-change row title click). */
  focusPath?: { path: string; nonce: number } | null;
  t: Translator;
}) {
  const rootRef = useRef<HTMLDivElement>(null);
  const [wide, setWide] = useState(false);
  const [storedView, setStoredView] = useState<DiffView | null>(
    readStoredView,
  );
  const [wrap, setWrap] = useState(
    () => localStorage.getItem(WRAP_STORAGE_KEY) === "1",
  );
  const defaultCollapsed = changes.files.length > AUTO_COLLAPSE_FILE_COUNT;
  /* Per-path overrides of the deck-wide default; absent = follow default. */
  const [collapsedMap, setCollapsedMap] = useState<Record<string, boolean>>(
    {},
  );
  const isCollapsed = useCallback(
    (path: string) => collapsedMap[path] ?? defaultCollapsed,
    [collapsedMap, defaultCollapsed],
  );

  /* Width rule: split is the default once the panel is ≥760px; the
   * stored manual choice always wins. */
  useEffect(() => {
    const el = rootRef.current;
    if (!el) return;
    const ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width ?? 0;
      setWide(w >= SPLIT_DEFAULT_MIN_WIDTH);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const view: DiffView = storedView ?? (wide ? "split" : "unified");

  const totals = useMemo(
    () =>
      changes.files.reduce(
        (acc, f) => ({ adds: acc.adds + f.adds, dels: acc.dels + f.dels }),
        { adds: 0, dels: 0 },
      ),
    [changes.files],
  );

  const allCollapsed = changes.files.every((f) => isCollapsed(f.path));
  const toggleAll = () => {
    const next = !allCollapsed;
    const map: Record<string, boolean> = {};
    for (const f of changes.files) map[f.path] = next;
    setCollapsedMap(map);
  };

  /* One-shot focus intent: expand the target card, make sure its diff is
   * on the wire, then scroll it into view (retried once after the diff
   * reply lands and the card grows). */
  useEffect(() => {
    if (!focusPath) return;
    const { path } = focusPath;
    if (!changes.files.some((f) => f.path === path)) return;
    setCollapsedMap((prev) => ({ ...prev, [path]: false }));
    if (diffs[path] === undefined) onRequestDiff(path);
    const scroll = () =>
      rootRef.current
        ?.querySelector(`[data-diff-path="${CSS.escape(path)}"]`)
        ?.scrollIntoView({ block: "start" });
    const first = window.setTimeout(scroll, 50);
    const second = window.setTimeout(scroll, 600);
    return () => {
      window.clearTimeout(first);
      window.clearTimeout(second);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusPath]);

  const chooseView = (v: DiffView) => {
    localStorage.setItem(VIEW_STORAGE_KEY, v);
    setStoredView(v);
  };
  const chooseWrap = (checked: boolean) => {
    localStorage.setItem(WRAP_STORAGE_KEY, checked ? "1" : "0");
    setWrap(checked);
  };

  return (
    <div ref={rootRef} className="flex min-h-0 flex-1 flex-col">
      {/* Summary totals + toolbar. */}
      <div className="flex items-center gap-2 px-4 pt-3 pb-3 text-2xs text-muted-foreground">
        <span className="min-w-0 flex-1 truncate">
          {t("agent.changesCount", { count: changes.files.length })}
        </span>
        <DiffTally adds={totals.adds} dels={totals.dels} />
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="size-7 text-muted-foreground"
              aria-label={
                allCollapsed ? t("agent.expandAll") : t("agent.collapseAll")
              }
              onClick={toggleAll}
            >
              <Icon
                name={allCollapsed ? "ChevronsDown" : "ChevronsUp"}
                className="size-3.5"
              />
            </Button>
          </TooltipTrigger>
          <TooltipContent side="bottom">
            {allCollapsed ? t("agent.expandAll") : t("agent.collapseAll")}
          </TooltipContent>
        </Tooltip>
        <DropdownMenu>
          <Tooltip>
            <TooltipTrigger asChild>
              <DropdownMenuTrigger asChild>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="size-7 text-muted-foreground"
                  aria-label={t("agent.diffSettings")}
                >
                  <Icon name="Settings" className="size-3.5" />
                </Button>
              </DropdownMenuTrigger>
            </TooltipTrigger>
            <TooltipContent side="bottom">
              {t("agent.diffSettings")}
            </TooltipContent>
          </Tooltip>
          <DropdownMenuContent align="end">
            <DropdownMenuCheckboxItem
              checked={view === "unified"}
              onCheckedChange={() => chooseView("unified")}
            >
              {t("agent.diffUnified")}
            </DropdownMenuCheckboxItem>
            <DropdownMenuCheckboxItem
              checked={view === "split"}
              onCheckedChange={() => chooseView("split")}
            >
              {t("agent.diffSplit")}
            </DropdownMenuCheckboxItem>
            <DropdownMenuSeparator />
            <DropdownMenuCheckboxItem
              checked={wrap}
              onCheckedChange={chooseWrap}
            >
              {t("agent.diffWrap")}
            </DropdownMenuCheckboxItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <div className="flex min-h-0 flex-1 flex-col gap-2 overflow-x-hidden overflow-y-auto px-4 pb-3">
        {changes.files.map((f) => (
          <DiffFileCard
            key={f.path}
            file={f}
            diff={diffs[f.path]}
            collapsed={isCollapsed(f.path)}
            view={view}
            wrap={wrap}
            connected={connected}
            onToggleCollapsed={() =>
              setCollapsedMap((prev) => ({
                ...prev,
                [f.path]: !isCollapsed(f.path),
              }))
            }
            onRequestDiff={onRequestDiff}
            onOpenFile={onOpenFile}
            t={t}
          />
        ))}
      </div>
    </div>
  );
}
