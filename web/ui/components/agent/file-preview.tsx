import { useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { File as PierreFile, type FileContents, type FileOptions } from "@pierre/diffs/react";
import { Button } from "../button";
import { Icon } from "../icon";
import { Markdown } from "../markdown";
import { copyText } from "../../lib/clipboard";
import { DIFF_THEME, DIFF_VIEW_STYLE, useThemeType } from "./diff-view";
import { resolveFileIconName } from "./file-visuals";
import type { FilePreviewState, Translator } from "./types";

function splitPath(path: string) {
  const index = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
  return { dir: path.slice(0, index + 1), base: path.slice(index + 1) };
}

/* bb file preview tab content (bb FilePreview, scoped port): header with
 * extension-resolved icon + path + icon buttons (markdown preview/source
 * toggle, line-wrap toggle, copy path); body = shiki code view (pierre
 * File) or rendered markdown for .md files in preview mode. A text
 * selection inside the body surfaces a floating "Add to chat" action (bb
 * SecondaryPanelSelectionActions, scoped port). Skipped bb surfaces:
 * CSV/video/iframe and download/
 * open-external (no such ops). */
export function FilePreview({
  path,
  state,
  connected,
  onRefresh,
  t,
  onAddToChat,
  lineRange,
}: {
  path: string;
  state: FilePreviewState | undefined;
  connected: boolean;
  onRefresh: () => void;
  t: Translator;
  onAddToChat?: (text: string) => void;
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
    if (!onAddToChat) return;
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
          <Button
            variant="ghost"
            size="icon"
            className="size-7 max-md:pointer-coarse:size-10"
            aria-label={t("agent.refresh")}
            disabled={!connected || state?.status === "loading"}
            onClick={onRefresh}
          >
            <Icon name="Refresh" className="size-3.5" />
          </Button>
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
        <div className="flex flex-1 flex-col items-center justify-center gap-3 px-3 text-center text-xs break-words text-muted-foreground">
          <span role="status">{state.message}</span>
          <Button variant="outline" size="sm" disabled={!connected} onClick={onRefresh}>
            <Icon name="Refresh" className="size-3.5" />
            {t("agent.refresh")}
          </Button>
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
                  onAddToChat?.(selMenu.text);
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

