import { useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { File as PierreFile, type FileContents, type FileOptions } from "@pierre/diffs/react";
import { FileSelectionMenu } from "./file-selection-menu";
import { VideoFilePreview } from "./video-file-preview";
import { HtmlFilePreview } from "./html-file-preview";
import { CsvFilePreview } from "./csv-preview";
import { ImageLightbox } from "../image-lightbox";
import { IMAGE_TRANSPARENCY_CHECKER_STYLE } from "../../lib/image-preview";
import { Button } from "../button";
import { Icon } from "../icon";
import { Markdown } from "../markdown";
import { FilePreviewHeader } from "./file-preview-header";
import { DIFF_THEME, DIFF_VIEW_STYLE, useThemeType } from "./diff-view";
import type { FilePreviewState, Translator } from "./types";

function splitPath(path: string) {
  const index = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
  return { dir: path.slice(0, index + 1), base: path.slice(index + 1) };
}

/* BB f51e6b0 FilePreview: shared by Agent tabs and the standalone Files
 * dialog. Scope transient UI state to the file identity, not the panel slot. */
type FilePreviewProps = {
  path: string;
  state: FilePreviewState | undefined;
  connected: boolean;
  visible?: boolean;
  onRefresh: () => void;
  t: Translator;
  onAddToChat?: (text: string) => void;
  lineRange?: { start: number; end: number } | null;
};

export function FilePreview(props: FilePreviewProps) {
  return <FilePreviewContent key={props.path} {...props} />;
}

function FilePreviewContent({
  path,
  state,
  connected,
  visible = true,
  onRefresh,
  t,
  onAddToChat,
  lineRange,
}: FilePreviewProps) {
  const bodyRef = useRef<HTMLDivElement>(null);
  const [wrap, setWrap] = useState(false);
  const [imageOpen, setImageOpen] = useState(false);
  const { base } = splitPath(path);
  const ext = base.includes(".") ? (base.split(".").pop() ?? "").toLowerCase() : "";
  const isMarkdown = ext === "md" || ext === "markdown";
  const isCSV = ext === "csv";
  const isHTML = ext === "html" || ext === "htm";
  const hasPreview = isMarkdown || isCSV || isHTML;
  /* BB renders Markdown, CSV and HTML previews by default. */
  const [viewMode, setViewMode] = useState<"preview" | "source">(
    hasPreview && !lineRange ? "preview" : "source",
  );
  useEffect(() => { if (lineRange) setViewMode("source"); }, [lineRange?.start, lineRange?.end]);
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
    () => ({ name: base, contents: state?.content ?? "" }),
    [base, state?.content],
  );

  return (
    <div className="flex h-full min-w-0 flex-col">
      <FilePreviewHeader path={path} name={base} content={state?.status === "ready" && !state.encoding ? state.content : null}
        loading={!state || state.status === "loading"} connected={connected} hasPreview={hasPreview}
        viewMode={viewMode} onViewModeChange={setViewMode} wrap={wrap} onWrapChange={setWrap} onRefresh={onRefresh} t={t} />
      {state === undefined || state.status === "loading" ? (
        <div className="flex flex-1 items-center justify-center text-muted-foreground">
          <Icon name="Spinner" className="size-4 animate-spin motion-reduce:animate-none" />
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
            className={`relative min-h-0 flex-1 ${(isCSV || isHTML) && viewMode === "preview" ? "overflow-hidden" : "overflow-auto"}`}
          >
            {isHTML && state.encoding !== "base64" && <HtmlFilePreview content={state.content} name={base} hidden={viewMode !== "preview"} />}
            {state.encoding === "url" && state.mime?.startsWith("video/") ? (
              <VideoFilePreview key={state.content} src={state.content} name={base} visible={visible} t={t} />
            ) : isHTML && state.encoding !== "base64" && viewMode === "preview" ? null : state.encoding === "base64" && state.mime ? (
              <div className="min-h-full w-full p-4" style={IMAGE_TRANSPARENCY_CHECKER_STYLE}>
                <button type="button" className="block w-full cursor-zoom-in" aria-label={t("agent.openImagePreview", { name: base })} onClick={() => setImageOpen(true)}>
                  <img src={`data:${state.mime};base64,${state.content}`} alt={base}
                    decoding="async" className="mx-auto block h-auto max-w-full" />
                </button>
                <ImageLightbox isOpen={imageOpen} imageSrc={imageOpen ? `data:${state.mime};base64,${state.content}` : null}
                  imageAlt={base} title={base} onClose={() => setImageOpen(false)} />
              </div>
            ) : isCSV && viewMode === "preview" ? (
              <CsvFilePreview name={base} content={state.content} t={t} />
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
            {onAddToChat && <FileSelectionMenu panelRef={bodyRef} content={state.content} mode={viewMode} onAddToChat={onAddToChat} t={t} />}
          </div>
        </>
      )}
    </div>
  );
}

