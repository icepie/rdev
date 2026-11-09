import { useEffect, useRef, useState } from "react";
import { useI18n } from "../lib/i18n";
import { Button } from "./button";
import { Textarea } from "./textarea";

/** Uses the inspector's existing CDP session; no second controller/socket. */
export function BrowserPreview({ src }: { src: string }) {
  const { t } = useI18n();
  const frameRef = useRef<HTMLIFrameElement>(null);
  const pendingRef = useRef<{ id: string; timer: ReturnType<typeof setTimeout> } | null>(null);
  const composingRef = useRef(false);
  const [showTools, setShowTools] = useState(false);
  const [showText, setShowText] = useState(false);
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [ready, setReady] = useState(false);
  const [error, setError] = useState("");
  const toolsRef = useRef(showTools);
  toolsRef.current = showTools;

  useEffect(() => {
    const receive = (event: MessageEvent) => {
      if (event.origin !== location.origin || event.source !== frameRef.current?.contentWindow) return;
      if (event.data?.type === "rdev-browser-ready") {
        setReady(true);
        frameRef.current?.contentWindow?.postMessage({ type: "rdev-browser-tools", visible: toolsRef.current }, location.origin);
      } else if (event.data?.type === "rdev-browser-bridge-error") {
        setError(t("browser.previewUnsupported"));
      } else if (event.data?.type === "rdev-browser-text-result" && event.data.id === pendingRef.current?.id) {
        clearTimeout(pendingRef.current!.timer);
        pendingRef.current = null;
        setBusy(false);
        if (event.data.error) setError(t("browser.textFailed"));
        else { setText(""); setError(""); }
      }
    };
    window.addEventListener("message", receive);
    return () => {
      window.removeEventListener("message", receive);
      if (pendingRef.current) clearTimeout(pendingRef.current.timer);
    };
  }, [t]);

  const insert = () => {
    if (!text || busy || !ready || composingRef.current || !frameRef.current?.contentWindow) return;
    const id = crypto.randomUUID();
    setBusy(true); setError("");
    pendingRef.current = { id, timer: setTimeout(() => {
      pendingRef.current = null; setBusy(false); setError(t("browser.textFailed"));
    }, 10000) };
    frameRef.current.contentWindow.postMessage({ type: "rdev-browser-insert-text", id, text }, location.origin);
  };

  return <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
    <div className="flex shrink-0 flex-wrap justify-end gap-1 border-b border-border-seam px-2 py-1">
      <Button variant="ghost" size="sm" disabled={!ready} aria-pressed={showText} onClick={() => setShowText(value => !value)}>{t("browser.textInput")}</Button>
      <Button variant="ghost" size="sm" disabled={!ready} aria-pressed={showTools} onClick={() => {
        const visible = !showTools;
        setShowTools(visible);
        frameRef.current?.contentWindow?.postMessage({ type: "rdev-browser-tools", visible }, location.origin);
      }}>DevTools</Button>
    </div>
    {error && <p role="alert" className="shrink-0 px-3 py-2 text-xs text-destructive-text">{error}</p>}
    <iframe ref={frameRef} title={t("browser.preview")} src={`${src}&rdevPreview=1`} referrerPolicy="no-referrer" className="min-h-0 min-w-0 w-full flex-1 border-0" allow="clipboard-read; clipboard-write" />
    {showText && <div className="flex shrink-0 flex-col gap-2 border-t border-border-seam p-3">
      <p className="text-xs text-muted-foreground">{t("browser.textHint")}</p>
      <Textarea aria-label={t("browser.textInput")} value={text} disabled={busy} maxLength={65536} rows={2} className="min-h-16 max-h-32 resize-y"
        onChange={event => setText(event.target.value)}
        onCompositionStart={() => { composingRef.current = true; }}
        onCompositionEnd={() => { composingRef.current = false; }}
        onKeyDown={event => {
          if (event.nativeEvent.isComposing || event.keyCode === 229 || composingRef.current) return;
          if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) { event.preventDefault(); insert(); }
        }} />
      <Button size="sm" className="self-end" disabled={!text || busy || !ready} onClick={insert}>{t("browser.insertText")}</Button>
    </div>}
  </div>;
}
