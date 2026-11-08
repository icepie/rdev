import { useEffect, useMemo, useState } from "react";
import { filePreviewURL } from "../../lib/file-preview-url";
import { Button } from "../button";
import { Icon } from "../icon";
import type { Translator } from "./types";

// Remote files must not inherit the application's origin or access its APIs.
// Keep authored HTML/styles intact, unlike the theme-injected canvas surface.
export function htmlPreviewDocument(content: string): string {
  // Place the policy before any authored resources. Do not parse the input in
  // the host document (even an inert DOMParser may initiate resource loads).
  // HTML parsing merges an authored html/head/body into the implied document.
  return `<!doctype html><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; media-src data: blob:; font-src data:; connect-src 'none'; form-action 'none'; base-uri 'none'">${content}`;
}

type HtmlPreviewProps = {
  content: string; name: string; hidden: boolean; previewUrl?: string;
  connected: boolean; onRetry: () => void; onLoadingChange: (loading: boolean) => void; t: Translator;
};

export function HtmlFilePreview(props: HtmlPreviewProps) {
  const previewUrl = filePreviewURL(props.previewUrl);
  const [source, setSource] = useState({ content: props.content, previewUrl, revision: 0 });
  if (source.content !== props.content || source.previewUrl !== previewUrl) {
    setSource({ content: props.content, previewUrl, revision: source.revision + 1 });
  }
  // BB keys the frame by file revision. Replacing it also isolates late events
  // from the previous document; mode/theme changes preserve the same instance.
  return <HtmlPreviewFrame key={source.revision} {...props} previewUrl={previewUrl} />;
}

function HtmlPreviewFrame({ content, name, hidden, previewUrl, connected, onRetry, onLoadingChange, t }: HtmlPreviewProps) {
  const document = useMemo(() => previewUrl ? undefined : htmlPreviewDocument(content), [content, previewUrl]);
  const [ready, setReady] = useState(!previewUrl);
  const [status, setStatus] = useState<"loading" | "loaded" | "error">("loading");
  useEffect(() => {
    if (!previewUrl) return;
    const controller = new AbortController();
    // Browsers fire iframe load even for HTTP errors. Check the bounded HEAD
    // response first; frame events alone cannot report an expired/offline grant.
    void fetch(previewUrl, { method: "HEAD", signal: controller.signal }).then(response => {
      if (controller.signal.aborted) return;
      if (response.ok) setReady(true);
      else setStatus("error");
    }).catch(() => { if (!controller.signal.aborted) setStatus("error"); });
    return () => controller.abort();
  }, [previewUrl]);
  useEffect(() => {
    onLoadingChange(status === "loading");
    return () => onLoadingChange(false);
  }, [status, onLoadingChange]);
  const hideFrame = hidden || status === "error";
  return <>
    {ready && <iframe title={name} src={previewUrl} srcDoc={document} sandbox="allow-scripts" referrerPolicy="no-referrer"
      onLoad={() => setStatus("loaded")} onError={() => setStatus("error")}
      hidden={hideFrame} inert={hideFrame}
      className={hideFrame ? "hidden" : "absolute inset-0 h-full w-full border-0 bg-white"} />}
    {!hidden && status === "error" && <div className="flex h-full flex-col items-center justify-center gap-3 px-3 text-center text-xs text-muted-foreground">
      <p role="alert">{t("agent.htmlPreviewFailed")}</p>
      <Button size="sm" variant="outline" disabled={!connected} onClick={onRetry}>
        <Icon name="Refresh" className="size-3.5" />{t("agent.retry")}
      </Button>
    </div>}
  </>;
}
