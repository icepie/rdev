import { useEffect, useRef, useState } from "react";
import { useI18n } from "../lib/i18n";
import { Button } from "./button";

/** Uses the inspector's existing CDP session; no second controller/socket. */
export function BrowserPreview({ src, visible = true }: { src: string; visible?: boolean }) {
  const { t } = useI18n();
  const frameRef = useRef<HTMLIFrameElement>(null);
  const [showTools, setShowTools] = useState(false);
  const [ready, setReady] = useState(false);
  const [error, setError] = useState("");
  const visibleRef = useRef(visible);
  visibleRef.current = visible;
  const toolsRef = useRef(showTools);
  toolsRef.current = showTools;

  useEffect(() => {
    const receive = (event: MessageEvent) => {
      if (event.origin !== location.origin || event.source !== frameRef.current?.contentWindow) return;
      if (event.data?.type === "rdev-browser-ready") {
        setReady(true);
        frameRef.current?.contentWindow?.postMessage({ type: "rdev-browser-visible", visible: visibleRef.current }, location.origin);
        frameRef.current?.contentWindow?.postMessage({ type: "rdev-browser-tools", visible: toolsRef.current }, location.origin);
      } else if (event.data?.type === "rdev-browser-bridge-error") {
        setError(t("browser.previewUnsupported"));
      } else if (event.data?.type === "rdev-browser-input-error") {
        setError(t("browser.textFailed"));
      }
    };
    window.addEventListener("message", receive);
    return () => {
      window.removeEventListener("message", receive);
    };
  }, [t]);

  useEffect(() => {
    if (ready) frameRef.current?.contentWindow?.postMessage({ type: "rdev-browser-visible", visible }, location.origin);
  }, [ready, visible]);

  return <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
    <div className="flex shrink-0 flex-wrap justify-end gap-1 border-b border-border-seam px-2 py-1">
      <Button variant="ghost" size="sm" disabled={!ready} aria-pressed={showTools} onClick={() => {
        const visible = !showTools;
        setShowTools(visible);
        frameRef.current?.contentWindow?.postMessage({ type: "rdev-browser-tools", visible }, location.origin);
      }}>DevTools</Button>
    </div>
    {error && <p role="alert" className="shrink-0 px-3 py-2 text-xs text-destructive-text">{error}</p>}
    <iframe ref={frameRef} title={t("browser.preview")} src={`${src}&rdevPreview=1`} referrerPolicy="no-referrer" className="min-h-0 min-w-0 w-full flex-1 border-0" allow="clipboard-read; clipboard-write" />
  </div>;
}
