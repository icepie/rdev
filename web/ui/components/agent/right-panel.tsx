import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { File as PierreFile } from "@pierre/diffs/react";
import type { FileContents, FileOptions } from "@pierre/diffs/react";
import {
  DndContext,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  horizontalListSortingStrategy,
  useSortable,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { useI18n } from "../../lib/i18n";
import { cn } from "../../lib/utils";
import { Button } from "../button";
import { Popover, PopoverAnchor, PopoverContent } from "../popover";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
} from "../context-menu";
import { Icon } from "../icon";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "../tooltip";
import { copyText } from "../../lib/clipboard";
import { Markdown } from "../markdown";
import { ChangesDeck } from "./changes-deck";
import { DIFF_THEME, DIFF_VIEW_STYLE, useThemeType } from "./diff-view";
import { resolveFileIconName } from "./file-visuals";
import { PanelDesktop } from "./panel-desktop";
import { PanelFiles } from "./panel-files";
import { PanelTerminal } from "./panel-terminal";
import type { ChangesState, DiffState, FilePreviewState, Translator } from "./types";

export interface RightPanelInfo {
  device: string;
  cwd: string;
  branch: string;
  model: string;
  thread: string;
  /* Pre-formatted cumulative token count. */
  tokens: string;
}

function splitPath(p: string): { dir: string; base: string } {
  const i = p.replace(/\/+$/, "").lastIndexOf("/");
  if (i < 0) return { dir: "", base: p };
  return { dir: p.slice(0, i + 1), base: p.slice(i + 1) };
}

/* bb secondary-panel dock positions (SplitSide). */
export type PanelDock = "left" | "top" | "bottom" | "right";

/* Dynamic tab strip: file previews, terminal sessions, and at most one
 * "new tab" launcher. */
type DynTab =
  | { kind: "file"; path: string }
  | { kind: "terminal"; num: number }
  | { kind: "files" }
  | { kind: "desktop" }
  | { kind: "new" };

function tabId(tab: DynTab): string {
  return tab.kind === "file"
    ? "file:" + tab.path
    : tab.kind === "terminal"
      ? "term:" + tab.num
      : tab.kind === "files"
        ? "files"
        : tab.kind === "desktop"
          ? "desktop"
          : "new";
}

/* Persisted dynamic tabs (everything except the new-tab launcher). */
type StoredDynTab =
  | { kind: "file"; path: string }
  | { kind: "terminal"; num: number }
  | { kind: "files" }
  | { kind: "desktop" };

/* Per-thread persisted strip: file + terminal tabs (NOT the new-tab
 * launcher), the active tab, and the recently-closed paths shown on the
 * new-tab page. File contents are never cached — reopened tabs re-issue
 * file_read; terminal tabs reconnect with a fresh shell on restore. */
interface PanelTabStore {
  tabs: StoredDynTab[];
  active: { kind: "info" } | { kind: "diff" } | StoredDynTab | null;
  recent: string[];
}

const PANEL_TABS_KEY = "rdev.agent.panel.tabs";

function readPanelTabs(): Record<string, PanelTabStore> {
  try {
    const raw = localStorage.getItem(PANEL_TABS_KEY);
    if (!raw) return {};
    const o = JSON.parse(raw) as Record<string, unknown>;
    const out: Record<string, PanelTabStore> = {};
    for (const key of Object.keys(o)) {
      const v = o[key] as Partial<PanelTabStore> | null;
      if (!v || typeof v !== "object") continue;
      const parseStored = (x: unknown): StoredDynTab | null => {
        if (!x || typeof x !== "object") return null;
        const o = x as Record<string, unknown>;
        if (o.kind === "file" && typeof o.path === "string") {
          return { kind: "file", path: o.path };
        }
        if (o.kind === "terminal" && typeof o.num === "number") {
          return { kind: "terminal", num: o.num };
        }
        if (o.kind === "files" || o.kind === "desktop") {
          return { kind: o.kind };
        }
        return null;
      };
      const parsedActive: PanelTabStore["active"] =
        v.active && (v.active.kind === "info" || v.active.kind === "diff")
          ? { kind: v.active.kind }
          : parseStored(v.active);
      out[key] = {
        tabs: Array.isArray(v.tabs)
          ? v.tabs.flatMap((x): StoredDynTab[] => {
              const s = parseStored(x);
              return s ? [s] : [];
            })
          : [],
        active: parsedActive,
        recent: Array.isArray(v.recent)
          ? v.recent.filter((x): x is string => typeof x === "string")
          : [],
      };
    }
    return out;
  } catch {
    return {};
  }
}

/* bb PaneArrangementButton (views/thread-detail/PaneMaximizeButton.tsx):
 * trigger click toggles Full Screen directly; hovering the trigger opens
 * the arrangement menu — a Full Screen row and a Move grid of four
 * CSS-drawn dock glyphs. While full-screened the menu is unreachable and
 * the trigger is a plain tooltip button. */
const ARRANGEMENT_ACTIONS: ReadonlyArray<{
  labelKey: string;
  side: PanelDock;
}> = [
  { labelKey: "agent.moveLeft", side: "left" },
  { labelKey: "agent.moveRight", side: "right" },
  { labelKey: "agent.moveTop", side: "top" },
  { labelKey: "agent.moveBottom", side: "bottom" },
];

const ARRANGEMENT_REGION_CLASS: Record<PanelDock, string> = {
  left: "inset-y-[3px] left-[3px] w-2.5",
  right: "inset-y-[3px] right-[3px] w-2.5",
  top: "inset-x-[3px] top-[3px] h-1.5",
  bottom: "inset-x-[3px] bottom-[3px] h-1.5",
};

const ARRANGEMENT_MENU_ITEM_CLASS =
  "flex w-full cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-left text-xs text-foreground outline-none transition-colors hover:bg-state-hover focus-visible:bg-state-hover focus-visible:outline-none";

/* bb ArrangementGlyph: a mini layout diagram — the filled region marks
 * the side the pane would move to. */
function ArrangementGlyph({ side }: { side: PanelDock }) {
  return (
    <span
      data-pane-arrangement-glyph={side}
      aria-hidden
      className="relative block h-5 w-8 rounded-[5px] border-2 border-current"
    >
      <span
        className={cn(
          "absolute rounded-[2px] bg-current",
          ARRANGEMENT_REGION_CLASS[side],
        )}
      />
    </span>
  );
}

/* bb useHoverPopover, trimmed: open immediately on pointer-over, close
 * 160ms after the pointer leaves both trigger and content. */
function useHoverPopover(): {
  open: boolean;
  /* bb handleOpenChange: closing also clears every hover flag so the
   * pointer still resting over the trigger/content cannot reopen. */
  setOpen: (open: boolean) => void;
  triggerHoverProps: {
    onPointerEnter: () => void;
    onPointerLeave: () => void;
  };
  contentHoverProps: {
    onPointerEnter: () => void;
    onPointerLeave: () => void;
  };
} {
  const [open, setOpen] = useState(false);
  const [overTrigger, setOverTrigger] = useState(false);
  const [overContent, setOverContent] = useState(false);
  useEffect(() => {
    if (overTrigger || overContent) {
      if (!open) setOpen(true);
      return;
    }
    if (!open) return;
    const timer = window.setTimeout(() => setOpen(false), 160);
    return () => window.clearTimeout(timer);
  }, [overTrigger, overContent, open]);
  return {
    open,
    setOpen: (nextOpen: boolean) => {
      if (nextOpen) {
        setOpen(true);
        return;
      }
      setOverTrigger(false);
      setOverContent(false);
      setOpen(false);
    },
    triggerHoverProps: {
      onPointerEnter: () => setOverTrigger(true),
      onPointerLeave: () => setOverTrigger(false),
    },
    contentHoverProps: {
      onPointerEnter: () => setOverContent(true),
      onPointerLeave: () => setOverContent(false),
    },
  };
}

function PanelLayoutControl({
  full,
  onSetDock,
  onToggleFull,
  t,
}: {
  full: boolean;
  onSetDock: (dock: PanelDock) => void;
  onToggleFull: () => void;
  t: Translator;
}) {
  const { open, setOpen, triggerHoverProps, contentHoverProps } =
    useHoverPopover();
  const label = t(full ? "agent.exitFullScreen" : "agent.fullScreen");
  const button = (
    <Button
      variant="ghost"
      size="icon"
      className="size-7 shrink-0"
      aria-label={label}
      aria-pressed={full}
      aria-expanded={!full ? open : undefined}
      onClick={() => {
        setOpen(false);
        onToggleFull();
      }}
      {...(!full ? triggerHoverProps : {})}
    >
      <Icon name={full ? "Minimize" : "Maximize"} />
    </Button>
  );
  if (full) {
    return (
      <Tooltip>
        <TooltipTrigger asChild>{button}</TooltipTrigger>
        <TooltipContent side="bottom">{label}</TooltipContent>
      </Tooltip>
    );
  }
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverAnchor asChild>{button}</PopoverAnchor>
      <PopoverContent
        role="menu"
        aria-label={t("agent.paneArrangement")}
        side="bottom"
        align="end"
        sideOffset={6}
        className="w-52 space-y-1 rounded-lg p-1.5"
        {...contentHoverProps}
      >
        <button
          type="button"
          role="menuitem"
          className={ARRANGEMENT_MENU_ITEM_CLASS}
          onClick={() => {
            setOpen(false);
            onToggleFull();
          }}
        >
          <Icon name="Maximize" className="size-4 shrink-0" />
          <span className="flex-1">{t("agent.fullScreen")}</span>
        </button>
        <div className="border-t border-border-hairline pt-1.5">
          <div className="px-2 pb-0.5 text-2xs font-medium text-subtle-foreground">
            {t("agent.movePanel")}
          </div>
          <div
            className="grid grid-cols-4 gap-1"
            aria-label={t("agent.movePane")}
          >
            {ARRANGEMENT_ACTIONS.map((action) => (
              <Tooltip key={action.side}>
                <TooltipTrigger asChild>
                  <button
                    type="button"
                    role="menuitem"
                    aria-label={t(action.labelKey)}
                    className="flex h-11 cursor-pointer items-center justify-center rounded-md text-subtle-foreground outline-none transition-colors hover:bg-state-hover hover:text-foreground focus-visible:bg-state-hover focus-visible:text-foreground focus-visible:outline-none"
                    onClick={() => {
                      setOpen(false);
                      onSetDock(action.side);
                    }}
                  >
                    <ArrangementGlyph side={action.side} />
                  </button>
                </TooltipTrigger>
                <TooltipContent side="bottom">
                  {t(action.labelKey)}
                </TooltipContent>
              </Tooltip>
            ))}
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}

/* bb SecondaryPanelTabStrip tab: dnd-kit sortable pill with a loading
 * spinner status, middle-click close, and a right-click menu (close /
 * close others / close right). */
function SortablePanelTab({
  tab,
  active,
  loading,
  canCloseOthers,
  canCloseRight,
  t,
  onActivate,
  onClose,
  onCloseOthers,
  onCloseRight,
  suppressClickRef,
}: {
  tab: DynTab;
  active: boolean;
  loading: boolean;
  canCloseOthers: boolean;
  canCloseRight: boolean;
  t: Translator;
  onActivate: () => void;
  onClose: () => void;
  onCloseOthers: () => void;
  onCloseRight: () => void;
  suppressClickRef: { current: boolean };
}) {
  const id = tabId(tab);
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } =
    useSortable({ id });
  const iconName =
    tab.kind === "file"
      ? resolveFileIconName(tab.path)
      : tab.kind === "terminal"
        ? "Terminal"
        : tab.kind === "files"
          ? "Folder"
          : tab.kind === "desktop"
            ? "Monitor"
            : "NewTab";
  const label =
    tab.kind === "file"
      ? splitPath(tab.path).base
      : tab.kind === "terminal"
        ? t("agent.terminalTab", { num: tab.num })
        : tab.kind === "files"
          ? t("agent.filesTab")
          : tab.kind === "desktop"
            ? t("agent.desktopTab")
            : t("agent.newTabLabel");
  return (
    <ContextMenu modal={false}>
      <ContextMenuTrigger asChild>
        <button
          ref={setNodeRef}
          type="button"
          {...attributes}
          {...listeners}
          role="tab"
          aria-selected={active}
          title={tab.kind === "file" ? tab.path : label}
          style={{
            transform: CSS.Transform.toString(transform),
            transition,
            opacity: isDragging ? 0.6 : undefined,
          }}
          className={cn(
            "group/tab inline-flex h-7 max-w-[180px] cursor-pointer items-center gap-1.5 rounded-md pl-1.5 pr-2 text-xs text-muted-foreground transition-colors hover:bg-state-hover",
            active && "bg-state-active text-foreground",
          )}
          onClick={() => {
            if (!suppressClickRef.current) onActivate();
          }}
          onAuxClick={(e) => {
            if (e.button === 1) {
              e.preventDefault();
              onClose();
            }
          }}
        >
          <Icon
            name={loading ? "Spinner" : iconName}
            className={cn("size-3.5 shrink-0", loading && "animate-spin")}
          />
          <span className="min-w-0 truncate">{label}</span>
          <span
            role="button"
            tabIndex={-1}
            aria-label={t("agent.closeTab")}
            className="inline-flex size-4 shrink-0 items-center justify-center rounded-sm opacity-0 transition-opacity hover:bg-background group-hover/tab:opacity-100"
            onClick={(e) => {
              e.stopPropagation();
              onClose();
            }}
          >
            <Icon name="X" className="size-3" />
          </span>
        </button>
      </ContextMenuTrigger>
      <ContextMenuContent>
        <ContextMenuItem onSelect={onClose}>
          {t("agent.closeTab")}
        </ContextMenuItem>
        <ContextMenuSeparator />
        <ContextMenuItem disabled={!canCloseOthers} onSelect={onCloseOthers}>
          {t("agent.closeOtherTabs")}
        </ContextMenuItem>
        <ContextMenuItem disabled={!canCloseRight} onSelect={onCloseRight}>
          {t("agent.closeTabsRight")}
        </ContextMenuItem>
      </ContextMenuContent>
    </ContextMenu>
  );
}

/* bb file preview tab content (bb FilePreview, scoped port): header with
 * extension-resolved icon + path + icon buttons (markdown preview/source
 * toggle, line-wrap toggle, copy path); body = shiki code view (pierre
 * File) or rendered markdown for .md files in preview mode. A text
 * selection inside the body surfaces a floating "Add to chat" action (bb
 * SecondaryPanelSelectionActions, scoped port). Skipped bb surfaces:
 * CSV/image/video/iframe (file_read is text-only) and download/refresh/
 * open-external (no such ops). */
function FilePreview({
  path,
  state,
  t,
  onAddToChat,
  lineRange,
}: {
  path: string;
  state: FilePreviewState | undefined;
  t: Translator;
  onAddToChat: (text: string) => void;
  /* bb line-suffix highlight (markdown `/path:12` links): one-shot
   * selectedLines range on the pierre File. */
  lineRange?: { start: number; end: number } | null;
}) {
  const bodyRef = useRef<HTMLDivElement>(null);
  const [selMenu, setSelMenu] = useState<{ x: number; y: number; text: string } | null>(null);
  const [wrap, setWrap] = useState(false);
  const { dir, base } = splitPath(path);
  const ext = base.includes(".") ? (base.split(".").pop() ?? "").toLowerCase() : "";
  const isMarkdown = ext === "md" || ext === "markdown";
  /* bb getInitialFilePreviewViewMode: markdown starts in preview. */
  const [viewMode, setViewMode] = useState<"preview" | "source">(
    isMarkdown ? "preview" : "source",
  );
  const themeType = useThemeType();
  const fileOptions = useMemo<FileOptions<undefined, undefined>>(
    () => ({
      overflow: wrap ? "wrap" : "scroll",
      disableFileHeader: true,
      themeType,
      theme: DIFF_THEME,
      enableLineSelection: lineRange != null,
    }),
    [wrap, themeType, lineRange],
  );
  const fileContents = useMemo<FileContents>(
    () => ({ name: base, contents: state?.content ?? "", cacheKey: path }),
    [base, state?.content, path],
  );

  const captureSelection = () => {
    const body = bodyRef.current;
    const sel = window.getSelection();
    if (!body || !sel || sel.isCollapsed) {
      setSelMenu(null);
      return;
    }
    const range = sel.rangeCount > 0 ? sel.getRangeAt(0) : null;
    if (!range || !body.contains(range.commonAncestorContainer)) {
      setSelMenu(null);
      return;
    }
    const text = sel.toString().replace(/\n+$/, "");
    if (!text.trim()) {
      setSelMenu(null);
      return;
    }
    const rect = range.getBoundingClientRect();
    const host = body.getBoundingClientRect();
    setSelMenu({
      x: Math.min(Math.max(rect.right - host.left, 8), host.width - 96),
      y: rect.bottom - host.top + body.scrollTop + 6,
      text,
    });
  };

  /* Collapse the menu when the selection disappears (click elsewhere,
   * keyboard), but not while the menu button itself is the target. */
  useEffect(() => {
    if (selMenu === null) return;
    const onDocMouseDown = (e: MouseEvent) => {
      if (e.target instanceof HTMLElement && e.target.closest("[data-add-to-chat]")) return;
      setSelMenu(null);
    };
    const onSelChange = () => {
      const sel = window.getSelection();
      if (!sel || sel.isCollapsed) setSelMenu(null);
    };
    document.addEventListener("mousedown", onDocMouseDown);
    document.addEventListener("selectionchange", onSelChange);
    return () => {
      document.removeEventListener("mousedown", onDocMouseDown);
      document.removeEventListener("selectionchange", onSelChange);
    };
  }, [selMenu]);

  const headerIconBtn =
    "inline-flex size-5 cursor-pointer items-center justify-center rounded-sm text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground [&_svg]:size-3";

  return (
    <div className="flex h-full flex-col">
      {/* bb FilePreviewHeader: icon + name + icon buttons. */}
      <div className="flex shrink-0 items-center gap-1.5 px-3 py-1.5">
        <Icon
          name={resolveFileIconName(path)}
          className="size-3.5 shrink-0 text-muted-foreground"
        />
        <span className="min-w-0 truncate text-xs" title={path}>
          <span className="text-foreground">{base}</span>
          {dir && <span className="text-muted-foreground"> {dir}</span>}
        </span>
        <div className="ml-auto flex shrink-0 items-center gap-0.5">
          {isMarkdown && (
            <button
              type="button"
              className={headerIconBtn}
              title={viewMode === "preview" ? t("agent.showSource") : t("agent.showPreview")}
              aria-label={viewMode === "preview" ? t("agent.showSource") : t("agent.showPreview")}
              onClick={() => setViewMode(viewMode === "preview" ? "source" : "preview")}
            >
              <Icon name={viewMode === "preview" ? "Code" : "Eye"} />
            </button>
          )}
          {(viewMode === "source" || !isMarkdown) &&
            state?.encoding !== "base64" && (
            <button
              type="button"
              className={headerIconBtn}
              title={t("agent.toggleWrap")}
              aria-label={t("agent.toggleWrap")}
              data-active={wrap || undefined}
              onClick={() => setWrap(!wrap)}
            >
              <Icon name="TextWrap" />
            </button>
          )}
          <button
            type="button"
            className={headerIconBtn}
            title={t("agent.copyPath", { label: base })}
            aria-label={t("agent.copyPath", { label: base })}
            onClick={() => void copyText(path)}
          >
            <Icon name="Copy" />
          </button>
        </div>
      </div>
      {state === undefined || state.status === "loading" ? (
        <div className="flex flex-1 items-center justify-center text-muted-foreground">
          <Icon name="Spinner" className="size-4 animate-spin" />
        </div>
      ) : state.status === "error" ? (
        <div className="flex flex-1 items-center justify-center px-3 text-center text-xs break-words text-muted-foreground">
          {state.message}
        </div>
      ) : (
        <>
          {state.truncated && (
            <div className="shrink-0 bg-muted/50 px-3 py-1 text-xs text-muted-foreground">
              {t("agent.truncatedFile")}
            </div>
          )}
          <div
            ref={bodyRef}
            className="relative min-h-0 flex-1 overflow-auto"
            onMouseUp={captureSelection}
          >
            {state.encoding === "base64" && state.mime ? (
              /* Image read (agent read_file or file browser): render the
               * base64 body; checkerboard backdrop for transparency. */
              <div className="flex h-full w-full items-center justify-center bg-[repeating-conic-gradient(var(--surface-recessed)_0%_25%,transparent_0%_50%)] bg-[length:16px_16px] p-4">
                <img
                  src={`data:${state.mime};base64,${state.content}`}
                  alt={base}
                  className="max-h-full max-w-full rounded border border-border-seam object-contain"
                />
              </div>
            ) : isMarkdown && viewMode === "preview" ? (
              <div className="px-3 py-2 text-xs">
                <Markdown text={state.content} />
              </div>
            ) : (
              /* bb SOURCE_VIEW_STYLE: DIFF_VIEW_STYLE + 16px gap blocks. */
              <div style={{ ...DIFF_VIEW_STYLE, "--diffs-gap-block": "16px" } as CSSProperties}>
                <PierreFile
                  file={fileContents}
                  options={fileOptions}
                  selectedLines={lineRange}
                  disableWorkerPool
                />
              </div>
            )}
            {selMenu !== null && (
              <button
                type="button"
                data-add-to-chat
                className="absolute z-20 inline-flex h-6 cursor-pointer items-center gap-1 rounded-md border border-border bg-background px-2 text-2xs shadow-lift transition-colors hover:bg-state-hover"
                style={{ left: selMenu.x, top: selMenu.y }}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => {
                  onAddToChat(selMenu.text);
                  setSelMenu(null);
                  window.getSelection()?.removeAllRanges();
                }}
              >
                <Icon name="MessageSquarePlus" className="size-3" />
                {t("agent.addToChat")}
              </button>
            )}
          </div>
        </>
      )}
    </div>
  );
}

/* bb "new tab" page: file search (bb NewTabFileSearch, scoped port:
 * query input + debounced search_files + keyboard navigation), Actions
 * (jump to pinned tabs), recently closed. */
function NewTabPage({
  recent,
  onOpenChanges,
  onOpenInfo,
  onOpenTerminal,
  onOpenFiles,
  onOpenDesktop,
  onOpenFile,
  onSearchFiles,
  t,
}: {
  recent: string[];
  onOpenChanges: () => void;
  onOpenInfo: () => void;
  onOpenTerminal: () => void;
  onOpenFiles: () => void;
  onOpenDesktop: () => void;
  onOpenFile: (path: string) => void;
  onSearchFiles: (q: string) => Promise<string[]>;
  t: Translator;
}) {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<string[] | null>(null);
  const [searching, setSearching] = useState(false);
  const [activeIdx, setActiveIdx] = useState(0);
  const seqRef = useRef(0);

  useEffect(() => {
    const q = query.trim();
    if (!q) {
      setResults(null);
      setSearching(false);
      return;
    }
    setSearching(true);
    const seq = ++seqRef.current;
    const timer = window.setTimeout(() => {
      void onSearchFiles(q).then((files) => {
        if (seqRef.current !== seq) return;
        setResults(files);
        setActiveIdx(0);
        setSearching(false);
      });
    }, 250);
    return () => window.clearTimeout(timer);
  }, [query, onSearchFiles]);

  const pick = (path: string) => {
    setQuery("");
    setResults(null);
    onOpenFile(path);
  };

  const onSearchKeyDown = (e: React.KeyboardEvent) => {
    if (!results || results.length === 0) return;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActiveIdx((i) => Math.min(i + 1, results.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActiveIdx((i) => Math.max(i - 1, 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      pick(results[activeIdx]);
    }
  };

  return (
    <div className="flex h-full flex-col gap-3 overflow-y-auto bg-sidebar px-4 pb-3 pt-1">
      <div className="relative">
        <Icon
          name="Search"
          className="pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2 text-muted-foreground"
        />
        <input
          type="text"
          value={query}
          autoFocus
          placeholder={t("agent.searchFilesPlaceholder")}
          className="h-7 w-full rounded-md border border-border bg-background pr-2 pl-7 text-xs outline-none placeholder:text-muted-foreground/70 focus:border-timeline-accent"
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={onSearchKeyDown}
        />
      </div>
      {query.trim() !== "" && (
        <div>
          {searching && results === null ? (
            <div className="flex items-center gap-2 px-1 py-1.5 text-xs text-muted-foreground">
              <Icon name="Spinner" className="size-3.5 animate-spin" />
              {t("agent.searching")}
            </div>
          ) : results !== null && results.length === 0 ? (
            <div className="px-1 py-1.5 text-xs text-muted-foreground">
              {t("agent.noSearchResults")}
            </div>
          ) : (
            results?.map((p, i) => {
              const { dir, base } = splitPath(p);
              return (
                <button
                  key={p}
                  type="button"
                  title={p}
                  className={cn(
                    "flex w-full min-w-0 cursor-pointer items-center gap-1.5 rounded px-2 py-1.5 text-left text-xs transition-colors hover:bg-state-hover",
                    i === activeIdx && "bg-state-active",
                  )}
                  onMouseEnter={() => setActiveIdx(i)}
                  onClick={() => pick(p)}
                >
                  <Icon name={resolveFileIconName(p)} className="size-4 shrink-0 text-muted-foreground" />
                  <span className="min-w-0 truncate">
                    <span className="text-foreground">{base}</span>
                    {dir && <span className="text-muted-foreground"> {dir}</span>}
                  </span>
                </button>
              );
            })
          )}
        </div>
      )}
      <div>
        <div className="px-2 pb-2 text-xs font-normal leading-5 text-subtle-foreground/75">
          {t("agent.actionsLabel")}
        </div>
        <button
          type="button"
          className="flex w-full min-w-0 cursor-pointer items-center gap-1.5 rounded px-2 py-1.5 text-left text-xs transition-colors hover:bg-state-hover"
          onClick={onOpenChanges}
        >
          <Icon name="FileDiff" className="size-4 shrink-0 text-muted-foreground" />
          {t("agent.openChanges")}
        </button>
        <button
          type="button"
          className="flex w-full min-w-0 cursor-pointer items-center gap-1.5 rounded px-2 py-1.5 text-left text-xs transition-colors hover:bg-state-hover"
          onClick={onOpenInfo}
        >
          <Icon name="Info" className="size-4 shrink-0 text-muted-foreground" />
          {t("agent.openInfo")}
        </button>
        <button
          type="button"
          className="flex w-full min-w-0 cursor-pointer items-center gap-1.5 rounded px-2 py-1.5 text-left text-xs transition-colors hover:bg-state-hover"
          onClick={onOpenTerminal}
        >
          <Icon name="Terminal" className="size-4 shrink-0 text-muted-foreground" />
          {t("agent.newTerminal")}
        </button>
        <button
          type="button"
          className="flex w-full min-w-0 cursor-pointer items-center gap-1.5 rounded px-2 py-1.5 text-left text-xs transition-colors hover:bg-state-hover"
          onClick={onOpenFiles}
        >
          <Icon name="Folder" className="size-4 shrink-0 text-muted-foreground" />
          {t("agent.openFiles")}
        </button>
        <button
          type="button"
          className="flex w-full min-w-0 cursor-pointer items-center gap-1.5 rounded px-2 py-1.5 text-left text-xs transition-colors hover:bg-state-hover"
          onClick={onOpenDesktop}
        >
          <Icon name="Monitor" className="size-4 shrink-0 text-muted-foreground" />
          {t("agent.openDesktop")}
        </button>
      </div>
      <div>
        <div className="px-2 pb-2 text-xs font-normal leading-5 text-subtle-foreground/75">
          {t("agent.recentLabel")}
        </div>
        {recent.length === 0 ? (
          <div className="px-1 py-1.5 text-xs text-muted-foreground">
            {t("agent.noRecent")}
          </div>
        ) : (
          recent.map((p) => {
            const { dir, base } = splitPath(p);
            return (
              <button
                key={p}
                type="button"
                title={p}
                className="flex w-full min-w-0 cursor-pointer items-center gap-1.5 rounded px-2 py-1.5 text-left text-xs transition-colors hover:bg-state-hover"
                onClick={() => onOpenFile(p)}
              >
                <Icon name={resolveFileIconName(p)} className="size-4 shrink-0 text-muted-foreground" />
                <span className="min-w-0 truncate">
                  <span className="text-foreground">{base}</span>
                  {dir && <span className="text-muted-foreground"> {dir}</span>}
                </span>
              </button>
            );
          })
        )}
      </div>
    </div>
  );
}

/* Agent page right-hand panel with the bb thread-chrome: pinned Info/Diff
 * icon tabs + dynamic file-preview tabs + a new-tab launcher. Git changes
 * (file list → per-file diff) and thread info arrive over the agent WS;
 * file previews are fetched lazily via the file_read op.
 * Not ported (bb-only concepts, per contract): split views, embedded
 * terminal (needs a PTY bridge), side chat (needs fork-thread), tab drag
 * reordering, chevron overflow scrolling, the new-tab file search (no
 * directory-listing op), and the Open browser action. */
export function AgentRightPanel({
  changes,
  diffs,
  info,
  connected,
  className,
  threadKey,
  fileStates,
  onRefresh,
  onRequestDiff,
  onRequestFile,
  onSearchFiles,
  onAddToChat,
  onHide,
  openFileSignal,
  openDiffSignal,
  panelFull,
  onSetDock,
  onToggleFull,
}: {
  changes: ChangesState | null;
  /* Per-path get_diff replies, keyed by file path; missing = not fetched. */
  diffs: Record<string, DiffState>;
  info: RightPanelInfo;
  connected: boolean;
  /* aside wrapper classes: inline panel vs. narrow-screen fixed drawer. */
  className?: string;
  /* `device:thread` scoping the persisted strip; "" while none selected. */
  threadKey: string;
  fileStates: Record<string, FilePreviewState>;
  onRefresh: () => void;
  onRequestDiff: (path: string) => void;
  onRequestFile: (path: string) => void;
  onSearchFiles: (q: string) => Promise<string[]>;
  onAddToChat: (text: string) => void;
  onHide: () => void;
  /* bb thread→panel linkage: nonce-stamped request from the transcript to
   * raise a file tab (clicking tool-row paths / markdown file links). */
  openFileSignal?: {
    path: string;
    nonce: number;
    lineStart?: number;
    lineEnd?: number;
  } | null;
  /* bb open-file-diff: raise the changes deck focused on this file. */
  openDiffSignal?: { path: string; nonce: number } | null;
  /* bb panel layout control (Full Screen + Move); omitted in the narrow
   * drawer where docking does not apply. */
  panelFull?: boolean;
  onSetDock?: (dock: PanelDock) => void;
  onToggleFull?: () => void;
}) {
  const { t } = useI18n();
  const [active, setActive] = useState<string>("diff");
  const [dynTabs, setDynTabs] = useState<DynTab[]>([]);
  const [recent, setRecent] = useState<string[]>([]);
  const [refreshing, setRefreshing] = useState(false);

  /* Restore the persisted strip on thread switch. */
  useEffect(() => {
    if (!threadKey) {
      setDynTabs([]);
      setRecent([]);
      setActive("diff");
      return;
    }
    const rec = readPanelTabs()[threadKey];
    const tabs: DynTab[] = rec?.tabs ?? [];
    setDynTabs(tabs);
    setRecent(rec?.recent ?? []);
    const savedActive: PanelTabStore["active"] = rec?.active ?? null;
    if (savedActive === null) {
      setActive("diff");
    } else if (savedActive.kind === "info" || savedActive.kind === "diff") {
      setActive(savedActive.kind);
    } else if (tabs.some((x) => tabId(x) === tabId(savedActive))) {
      setActive(tabId(savedActive));
    } else {
      setActive("diff");
    }
  }, [threadKey]);

  /* Persist the strip — but never on the render where threadKey changed:
   * that render still carries the previous thread's state and the restore
   * effect above owns it (writes would clobber the new key's record). */
  const persistKeyRef = useRef("");
  useEffect(() => {
    if (persistKeyRef.current !== threadKey) {
      persistKeyRef.current = threadKey;
      return;
    }
    if (!threadKey) return;
    const store = readPanelTabs();
    store[threadKey] = {
      tabs: dynTabs.filter(
        (x): x is StoredDynTab =>
          x.kind === "file" ||
          x.kind === "terminal" ||
          x.kind === "files" ||
          x.kind === "desktop",
      ),
      active:
        active === "info" || active === "diff"
          ? { kind: active }
          : active === "files" || active === "desktop"
            ? { kind: active }
            : active.startsWith("file:")
              ? { kind: "file", path: active.slice(5) }
              : active.startsWith("term:")
                ? { kind: "terminal", num: Number(active.slice(5)) }
                : null,
      recent,
    };
    try {
      localStorage.setItem(PANEL_TABS_KEY, JSON.stringify(store));
    } catch {
      /* storage full/blocked — strip state stays session-only */
    }
  }, [threadKey, dynTabs, active, recent]);

  /* Stop the refresh spinner once fresh changes land; safety timeout in
   * case the device never answers. */
  useEffect(() => {
    setRefreshing(false);
  }, [changes]);
  useEffect(() => {
    if (!refreshing) return;
    const timer = window.setTimeout(() => setRefreshing(false), 5000);
    return () => window.clearTimeout(timer);
  }, [refreshing]);

  /* Lazily fetch the active file preview (covers restored tabs and the
   * re-fetch after a thread switch clears fileStates upstream). */
  useEffect(() => {
    if (!active.startsWith("file:")) return;
    const path = active.slice(5);
    if (fileStates[path] === undefined) onRequestFile(path);
  }, [active, fileStates, onRequestFile]);

  const openFileTab = (path: string) => {
    if (!dynTabs.some((x) => tabId(x) === "file:" + path)) {
      setDynTabs([...dynTabs, { kind: "file", path }]);
    }
    setActive("file:" + path);
    if (fileStates[path] === undefined) onRequestFile(path);
  };

  /* bb line-suffix intent (`/path:12`, `#L12-L20`): one-shot highlight
   * range handed to the FilePreview of that path; transient, never
   * persisted with the tab strip. */
  const [lineIntent, setLineIntent] = useState<{
    path: string;
    start: number;
    end: number;
    nonce: number;
  } | null>(null);

  /* Consume transcript-originated open requests (nonce retriggers the
   * effect even for the same path). */
  useEffect(() => {
    if (!openFileSignal) return;
    openFileTab(openFileSignal.path);
    if (openFileSignal.lineStart !== undefined) {
      setLineIntent({
        path: openFileSignal.path,
        start: openFileSignal.lineStart,
        end: openFileSignal.lineEnd ?? openFileSignal.lineStart,
        nonce: openFileSignal.nonce,
      });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [openFileSignal]);

  /* bb open-file-diff: switch to the changes deck; the deck consumes
   * focusPath (expand + scroll) itself. */
  useEffect(() => {
    if (openDiffSignal) setActive("diff");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [openDiffSignal]);

  const openNewTab = () => {
    if (dynTabs.some((x) => x.kind === "new")) {
      setActive("new");
    } else {
      setDynTabs([...dynTabs, { kind: "new" }]);
      setActive("new");
    }
  };

  /* bb terminal.open: new shell session tab, numbered from the highest
   * existing terminal tab. */
  const openTerminalTab = () => {
    let max = 0;
    for (const x of dynTabs) {
      if (x.kind === "terminal") max = Math.max(max, x.num);
    }
    const num = max + 1;
    setDynTabs([...dynTabs, { kind: "terminal", num }]);
    setActive("term:" + num);
  };

  /* Singleton device-scoped tabs: one file manager and one desktop per
   * strip (both bind to info.device). */
  const openSingletonTab = (kind: "files" | "desktop") => {
    if (!dynTabs.some((x) => x.kind === kind)) {
      setDynTabs([...dynTabs, { kind }]);
    }
    setActive(kind);
  };

  const closeTab = (id: string) => {
    const idx = dynTabs.findIndex((x) => tabId(x) === id);
    if (idx === -1) return;
    const closed = dynTabs[idx];
    const next = [...dynTabs.slice(0, idx), ...dynTabs.slice(idx + 1)];
    if (closed.kind === "file") {
      setRecent((r) =>
        [closed.path, ...r.filter((p) => p !== closed.path)].slice(0, 25),
      );
    }
    setDynTabs(next);
    if (active === id) {
      const neighbor = next[Math.min(idx, next.length - 1)];
      setActive(neighbor ? tabId(neighbor) : "diff");
    }
  };

  /* bb tab context-menu actions. */
  const closeOtherTabs = (id: string) => {
    const keep = dynTabs.filter((x) => tabId(x) === id);
    for (const x of dynTabs) {
      if (tabId(x) !== id && x.kind === "file") {
        setRecent((r) => [x.path, ...r.filter((p) => p !== x.path)].slice(0, 25));
      }
    }
    setDynTabs(keep);
    if (active !== id && (active.startsWith("file:") || active === "new")) {
      setActive(id);
    }
  };
  const closeTabsRight = (id: string) => {
    const idx = dynTabs.findIndex((x) => tabId(x) === id);
    if (idx === -1) return;
    const dropped = dynTabs.slice(idx + 1);
    for (const x of dropped) {
      if (x.kind === "file") {
        setRecent((r) => [x.path, ...r.filter((p) => p !== x.path)].slice(0, 25));
      }
    }
    const next = dynTabs.slice(0, idx + 1);
    setDynTabs(next);
    if (dropped.some((x) => tabId(x) === active)) setActive(id);
  };

  /* bb SecondaryPanelTabStrip: overflow fades + edge chevrons. */
  const stripRef = useRef<HTMLDivElement>(null);
  const viewportRef = useRef<HTMLDivElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  const [overflow, setOverflow] = useState({
    canScrollLeft: false,
    canScrollRight: false,
  });
  const measureStrip = useCallback(() => {
    const viewport = viewportRef.current;
    if (!viewport) return;
    const maxScrollLeft = Math.max(
      0,
      viewport.scrollWidth - viewport.clientWidth,
    );
    const next = {
      canScrollLeft: viewport.scrollLeft > 1,
      canScrollRight: viewport.scrollLeft < maxScrollLeft - 1,
    };
    setOverflow((prev) =>
      prev.canScrollLeft === next.canScrollLeft &&
      prev.canScrollRight === next.canScrollRight
        ? prev
        : next,
    );
  }, []);
  useEffect(() => {
    const viewport = viewportRef.current;
    if (!viewport) return;
    viewport.addEventListener("scroll", measureStrip, { passive: true });
    const observer = new ResizeObserver(measureStrip);
    observer.observe(viewport);
    if (contentRef.current) observer.observe(contentRef.current);
    return () => {
      viewport.removeEventListener("scroll", measureStrip);
      observer.disconnect();
    };
  }, [measureStrip]);
  useEffect(measureStrip, [dynTabs, measureStrip]);
  const scrollStripBy = (dir: 1 | -1) => {
    const viewport = viewportRef.current;
    if (viewport) {
      viewport.scrollBy({ left: dir * viewport.clientWidth * 0.8, behavior: "smooth" });
    }
  };

  /* bb: dnd-kit reorder with click suppression after a drag. */
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
  );
  const suppressClickRef = useRef(false);
  const onTabDragEnd = (e: DragEndEvent) => {
    const from = String(e.active.id);
    const to = e.over ? String(e.over.id) : null;
    if (!to || from === to) return;
    suppressClickRef.current = true;
    window.setTimeout(() => {
      suppressClickRef.current = false;
    }, 100);
    setDynTabs((prev) => {
      const a = prev.findIndex((x) => tabId(x) === from);
      const b = prev.findIndex((x) => tabId(x) === to);
      if (a === -1 || b === -1) return prev;
      const next = [...prev];
      const [moved] = next.splice(a, 1);
      next.splice(b, 0, moved);
      return next;
    });
  };

  const infoRows: { label: string; value: string }[] = [
    { label: t("agent.infoDevice"), value: info.device },
    { label: t("agent.infoProject"), value: info.cwd },
    { label: t("agent.infoBranch"), value: info.branch },
    { label: t("agent.infoModel"), value: info.model },
    { label: t("agent.infoThread"), value: info.thread },
    { label: t("agent.infoTokens"), value: info.tokens },
  ];

  const pinnedTab = (
    id: "info" | "diff",
    icon: string,
    label: string,
  ) => (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          role="tab"
          aria-selected={active === id}
          aria-label={label}
          className={cn(
            "inline-flex size-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-state-hover",
            active === id && "bg-state-active text-foreground",
          )}
          onClick={() => setActive(id)}
        >
          <Icon name={icon} className="size-3.5" />
        </button>
      </TooltipTrigger>
      <TooltipContent side="bottom">{label}</TooltipContent>
    </Tooltip>
  );

  return (
    <aside
      className={cn(
        "flex h-full min-h-0 flex-col overflow-hidden border-l border-border-seam bg-sidebar",
        className,
      )}
    >
      {/* bb thread-chrome row: pinned tabs, dynamic strip, +, actions. */}
      <div className="flex h-9 min-w-0 shrink-0 items-center justify-between gap-2 bg-sidebar px-4 select-none">
        <div className="flex min-w-0 flex-1 items-center gap-1" role="toolbar">
          {pinnedTab("info", "Info", t("agent.threadInfo"))}
          {pinnedTab("diff", "FileDiff", t("agent.changesTab"))}
          <div ref={stripRef} className="relative min-w-0 flex-1">
            {/* bb overflow fades: gradient masks at the scrolled edges. */}
            {overflow.canScrollLeft && (
              <div className="pointer-events-none absolute inset-y-0 left-0 z-10 w-6 bg-gradient-to-r from-sidebar to-transparent" />
            )}
            {overflow.canScrollRight && (
              <div className="pointer-events-none absolute inset-y-0 right-0 z-10 w-6 bg-gradient-to-l from-sidebar to-transparent" />
            )}
            {overflow.canScrollLeft && (
              <button
                type="button"
                aria-label={t("agent.scrollLeft")}
                className="absolute top-1/2 left-0 z-20 inline-flex size-5 -translate-y-1/2 cursor-pointer items-center justify-center rounded-sm text-muted-foreground transition-colors hover:bg-sidebar-accent hover:text-foreground"
                onClick={() => scrollStripBy(-1)}
              >
                <Icon name="ChevronLeft" className="size-3.5" />
              </button>
            )}
            {overflow.canScrollRight && (
              <button
                type="button"
                aria-label={t("agent.scrollRight")}
                className="absolute top-1/2 right-0 z-20 inline-flex size-5 -translate-y-1/2 cursor-pointer items-center justify-center rounded-sm text-muted-foreground transition-colors hover:bg-sidebar-accent hover:text-foreground"
                onClick={() => scrollStripBy(1)}
              >
                <Icon name="ChevronRight" className="size-3.5" />
              </button>
            )}
            <div
              ref={viewportRef}
              className="scrollbar-none min-w-0 overflow-x-auto"
            >
              <div ref={contentRef} className="flex w-max items-center gap-1">
                <DndContext
                  sensors={sensors}
                  collisionDetection={closestCenter}
                  onDragEnd={onTabDragEnd}
                >
                  <SortableContext
                    items={dynTabs.map(tabId)}
                    strategy={horizontalListSortingStrategy}
                  >
                    {dynTabs.map((tab, i) => (
                      <SortablePanelTab
                        key={tabId(tab)}
                        tab={tab}
                        active={active === tabId(tab)}
                        loading={
                          tab.kind === "file" &&
                          fileStates[tab.path]?.status === "loading"
                        }
                        canCloseOthers={dynTabs.length > 1}
                        canCloseRight={i < dynTabs.length - 1}
                        t={t}
                        onActivate={() => setActive(tabId(tab))}
                        onClose={() => closeTab(tabId(tab))}
                        onCloseOthers={() => closeOtherTabs(tabId(tab))}
                        onCloseRight={() => closeTabsRight(tabId(tab))}
                        suppressClickRef={suppressClickRef}
                      />
                    ))}
                  </SortableContext>
                </DndContext>
              </div>
            </div>
          </div>
          <button
            type="button"
            aria-label={t("agent.openNewTab")}
            title={t("agent.openNewTab")}
            className="inline-flex size-7 shrink-0 cursor-pointer items-center justify-center rounded-md text-muted-foreground/70 transition-colors hover:bg-sidebar-accent hover:text-foreground"
            onClick={openNewTab}
          >
            <Icon name="Plus" className="size-4" />
          </button>
        </div>
        <div className="flex min-w-0 shrink-0 items-center gap-1">
          {active === "diff" && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={t("agent.refresh")}
                  disabled={!connected}
                  className="size-7"
                  onClick={() => {
                    setRefreshing(true);
                    onRefresh();
                  }}
                >
                  <Icon
                    name="Refresh"
                    className={cn(refreshing && "animate-spin")}
                  />
                </Button>
              </TooltipTrigger>
              <TooltipContent>{t("agent.refresh")}</TooltipContent>
            </Tooltip>
          )}
          {panelFull !== undefined &&
            onSetDock &&
            onToggleFull && (
              <PanelLayoutControl
                full={panelFull}
                onSetDock={onSetDock}
                onToggleFull={onToggleFull}
                t={t}
              />
            )}
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                aria-label={t("agent.hideRightPanel")}
                className="size-7"
                onClick={onHide}
              >
                <Icon name="PanelRight" />
              </Button>
            </TooltipTrigger>
            <TooltipContent>{t("agent.hideRightPanel")}</TooltipContent>
          </Tooltip>
        </div>
      </div>

      <div className="flex min-h-0 flex-1 flex-col overflow-hidden bg-sidebar">
      {active === "info" ? (
        /* Info tab */
        <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
          <dl className="flex flex-col gap-2 text-xs">
            {infoRows.map((row) => (
              <div key={row.label} className="min-w-0">
                <dt className="text-muted-foreground">{row.label}</dt>
                <dd
                  className="break-all text-foreground"
                  title={row.value || undefined}
                >
                  {row.value || "—"}
                </dd>
              </div>
            ))}
          </dl>
        </div>
      ) : active === "new" ? (
        <NewTabPage
          recent={recent}
          onOpenChanges={() => setActive("diff")}
          onOpenInfo={() => setActive("info")}
          onOpenTerminal={openTerminalTab}
          onOpenFiles={() => openSingletonTab("files")}
          onOpenDesktop={() => openSingletonTab("desktop")}
          onOpenFile={openFileTab}
          onSearchFiles={onSearchFiles}
          t={t}
        />
      ) : active.startsWith("file:") ? (
        <FilePreview
          path={active.slice(5)}
          state={fileStates[active.slice(5)]}
          t={t}
          onAddToChat={onAddToChat}
          lineRange={
            lineIntent && lineIntent.path === active.slice(5)
              ? { start: lineIntent.start, end: lineIntent.end }
              : null
          }
        />
      ) : active.startsWith("term:") ||
        active === "files" ||
        active === "desktop" ? (
        /* Terminal/files/desktop tabs render below (kept mounted). */
        null
      ) : (
        /* Changes deck: one collapsible diff card per changed file. */
        changes === null ? (
          connected ? (
            <div className="flex items-center justify-center py-8 text-muted-foreground">
              <Icon name="Spinner" className="size-4 animate-spin" />
            </div>
          ) : (
            <div className="px-4 py-8 text-center text-xs text-muted-foreground">
              {t("agent.noChanges")}
            </div>
          )
        ) : changes.error ? (
          <div className="px-4 py-8 text-center text-xs text-muted-foreground">
            <div>{t("agent.notGitRepo")}</div>
            <div className="mt-1 break-words text-2xs opacity-75">
              {changes.error}
            </div>
          </div>
        ) : changes.files.length === 0 ? (
          <div className="px-4 py-8 text-center text-xs text-muted-foreground">
            {t("agent.noChanges")}
          </div>
        ) : (
          <ChangesDeck
            changes={changes}
            diffs={diffs}
            connected={connected}
            onRequestDiff={onRequestDiff}
            onOpenFile={openFileTab}
            focusPath={openDiffSignal}
            t={t}
          />
        )
      )}
      {/* Terminal/files/desktop tabs stay mounted (hidden) while their tab
       * exists: terminal keeps its shell + scrollback, the file manager
       * keeps running transfers, and the desktop disconnects itself while
       * hidden (visible=false) and reconnects on activation. */}
      {dynTabs.flatMap((tab) =>
        tab.kind === "terminal" ||
        tab.kind === "files" ||
        tab.kind === "desktop" ? (
          <div
            key={tabId(tab)}
            className={
              active === tabId(tab)
                ? "flex min-h-0 flex-1 flex-col"
                : "hidden"
            }
          >
            {tab.kind === "terminal" ? (
              <PanelTerminal
                device={info.device}
                visible={active === tabId(tab)}
                t={t}
              />
            ) : tab.kind === "files" ? (
              <PanelFiles
                device={info.device}
                visible={active === tabId(tab)}
                t={t}
              />
            ) : (
              <PanelDesktop
                device={info.device}
                visible={active === tabId(tab)}
                t={t}
              />
            )}
          </div>
        ) : (
          []
        ),
      )}
      </div>
    </aside>
  );
}
