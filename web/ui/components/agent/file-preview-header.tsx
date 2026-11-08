import { useEffect, useLayoutEffect, useRef, useState, type ComponentProps } from "react";
import { Button } from "../button";
import { Icon } from "../icon";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "../tooltip";
import { DropdownMenu, DropdownMenuCheckboxItem, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from "../dropdown-menu";
import { copyText } from "../../lib/clipboard";
import { resolveFileIconName } from "./file-visuals";
import type { Translator } from "./types";

const iconClass = "h-5 w-5 shrink-0 rounded-sm p-0 text-muted-foreground hover:bg-state-hover hover:text-foreground [&_[data-icon-root]]:size-3 max-md:pointer-coarse:h-9 max-md:pointer-coarse:w-9 max-md:pointer-coarse:[&_[data-icon-root]]:size-5";
function Action({ label, icon, ...props }: ComponentProps<typeof Button> & { label: string; icon: ComponentProps<typeof Icon>["name"] }) {
  return <Tooltip><TooltipTrigger asChild><Button type="button" variant="ghost" size="icon" className={iconClass} aria-label={label} {...props}><Icon name={icon} /></Button></TooltipTrigger><TooltipContent side="bottom">{label}</TooltipContent></Tooltip>;
}

// BB f51e6b0 FilePreviewHeader / FilePreviewChrome: use actual pane width,
// keeping view modes visible and moving secondary actions into a narrow menu.
export function FilePreviewHeader({ path, name, content, loading, connected, hasPreview, viewMode, onViewModeChange, wrap, onWrapChange, onRefresh, t }: {
  path: string; name: string; content: string | null; loading: boolean; connected: boolean;
  hasPreview: boolean; viewMode: "preview" | "source"; onViewModeChange: (mode: "preview" | "source") => void;
  wrap: boolean; onWrapChange: (wrap: boolean) => void; onRefresh: () => void; t: Translator;
}) {
  const header = useRef<HTMLDivElement>(null);
  const [narrow, setNarrow] = useState(false);
  const [copyStatus, setCopyStatus] = useState<"path" | "content" | "error" | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const revision = useRef(0);
  useEffect(() => () => { revision.current++; clearTimeout(timer.current); }, []);
  useLayoutEffect(() => {
    const element = header.current;
    if (!element) return;
    const update = (width: number) => { if (width > 0) setNarrow(width < 560); };
    update(element.getBoundingClientRect().width);
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(entries => update(entries[0]?.contentRect.width ?? 0));
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  const copy = async (kind: "path" | "content", text: string) => {
    const current = ++revision.current;
    let result: typeof copyStatus = kind;
    try { await copyText(text); } catch { result = "error"; }
    if (current !== revision.current) return;
    setCopyStatus(result);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopyStatus(null), 2000);
  };
  const refreshLabel = t(loading ? "agent.refreshingFile" : "agent.refresh");
  const pathLabel = t("agent.copyPath", { label: name });
  const contentLabel = t("agent.copyFileContents");
  const showWrap = content !== null && (!hasPreview || viewMode === "source");
  return <div ref={header} className="sticky top-0 z-30 shrink-0 bg-sidebar">
    <TooltipProvider delayDuration={300}>
      <div className="flex min-h-9 items-center gap-2 bg-surface-raised px-4 max-md:pointer-coarse:min-h-12 max-md:pointer-coarse:px-3">
        <div className="flex min-w-0 flex-1 items-center gap-1.5">
          <Icon name={loading ? "Spinner" : resolveFileIconName(path)} className={`size-3.5 shrink-0 text-subtle-foreground max-md:pointer-coarse:size-5 ${loading ? "animate-spin motion-reduce:animate-none" : ""}`} />
          <Tooltip><TooltipTrigger asChild>
            <button type="button" aria-label={pathLabel} onClick={() => void copy("path", path)}
              className="min-w-0 cursor-pointer truncate rounded-sm text-left font-mono text-xs font-medium leading-5 text-file-accent underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring max-md:pointer-coarse:text-sm" dir="rtl">
              <span dir="ltr">{path}</span>
            </button>
          </TooltipTrigger><TooltipContent side="bottom">{copyStatus === "path" ? t("common.copied") : pathLabel}</TooltipContent></Tooltip>
          {!narrow && <>
            <Action label={refreshLabel} icon="Refresh" disabled={!connected || loading} onClick={onRefresh} />
            {content !== null && <Action label={contentLabel} icon={copyStatus === "content" ? "Check" : "Copy"} onClick={() => void copy("content", content)} />}
          </>}
        </div>
        <div className="ml-auto flex shrink-0 items-center gap-1">
          {showWrap && !narrow && <Action label={t("agent.toggleWrap")} icon="TextWrap" aria-pressed={wrap} onClick={() => onWrapChange(!wrap)} />}
          {hasPreview && <div role="group" aria-label={t("agent.fileViewMode")} className="inline-flex shrink-0 items-center gap-0.5 rounded-md border border-border p-0.5">
            {(["preview", "source"] as const).map(mode => <Button key={mode} type="button" variant="ghost" size="sm"
              className="h-5 rounded-sm px-2 text-muted-foreground max-md:pointer-coarse:h-9 max-md:pointer-coarse:text-sm"
              aria-label={t(mode === "preview" ? "agent.showPreview" : "agent.showSource")} aria-pressed={viewMode === mode} onClick={() => onViewModeChange(mode)}>
              {t(mode === "preview" ? "agent.canvas.preview" : "agent.canvas.source")}
            </Button>)}
          </div>}
          {narrow && <DropdownMenu modal={false}>
            <DropdownMenuTrigger asChild><Button variant="ghost" size="icon" className={`${iconClass} max-md:pointer-coarse:size-10`} aria-label={t("agent.fileActions")}><Icon name="MoreHorizontal" /></Button></DropdownMenuTrigger>
            <DropdownMenuContent align="end" mobileTitle={t("agent.fileActions")} className="max-md:pointer-coarse:[&_[role=menuitem]]:min-h-11 max-md:pointer-coarse:[&_[role=menuitem]]:text-sm max-md:pointer-coarse:[&_[role=menuitemcheckbox]]:min-h-11 max-md:pointer-coarse:[&_[role=menuitemcheckbox]]:text-sm">
              <DropdownMenuLabel className="max-w-80 break-all font-mono">{path}</DropdownMenuLabel>
              <DropdownMenuSeparator />
              <DropdownMenuItem onSelect={() => void copy("path", path)}><Icon name="Copy" />{pathLabel}</DropdownMenuItem>
              {content !== null && <DropdownMenuItem onSelect={() => void copy("content", content)}><Icon name="Copy" />{contentLabel}</DropdownMenuItem>}
              <DropdownMenuItem disabled={!connected || loading} onSelect={onRefresh}><Icon name="Refresh" />{refreshLabel}</DropdownMenuItem>
              {showWrap && <><DropdownMenuSeparator /><DropdownMenuCheckboxItem checked={wrap} onCheckedChange={onWrapChange}><Icon name="TextWrap" />{t("agent.toggleWrap")}</DropdownMenuCheckboxItem></>}
            </DropdownMenuContent>
          </DropdownMenu>}
        </div>
      </div>
    </TooltipProvider>
    <div role="status" className={copyStatus === "error" ? "px-4 py-1 text-xs text-destructive" : "sr-only"}>{copyStatus ? t(copyStatus === "error" ? "agent.copyFileFailed" : "common.copied") : ""}</div>
  </div>;
}
