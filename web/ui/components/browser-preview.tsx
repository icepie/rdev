import { validBrowserViewport, loadBrowserViewport, viewportStorageKey } from "./browser-viewport";
import { Input } from "./input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./select";
import { useEffect, useRef, useState } from "react";
import { useI18n } from "../lib/i18n";
import { Button } from "./button";

/** Keep an editable draft so clearing/retyping a dimension does not clamp each keystroke. */
function DimensionInput({ value, max, label, onCommit }: { value: number; max: number; label: string; onCommit: (value: number) => void }) {
  const [draft, setDraft] = useState(String(value));
  useEffect(() => setDraft(String(value)), [value]);
  const commit = () => {
    const parsed = Number(draft);
    const next = draft.trim() && Number.isFinite(parsed) ? Math.min(max, Math.max(240, Math.round(parsed))) : value;
    setDraft(String(next));
    onCommit(next);
  };
  return <Input type="number" aria-label={label} min={240} max={max} value={draft}
    onChange={e => setDraft(e.target.value)} onBlur={commit}
    onKeyDown={e => { if (e.key === "Enter") { e.preventDefault(); commit(); } }} className="h-7 w-20 text-xs" />;
}

/** Uses the inspector's existing CDP session; no second controller/socket. */
export function BrowserPreview({ src, visible = true }: { src: string; visible?: boolean }) {
  const { t } = useI18n();
  const frameRef = useRef<HTMLIFrameElement>(null);
  const [viewport, setViewport] = useState(() => loadBrowserViewport(src));
  const viewportRef = useRef(viewport);
  viewportRef.current = viewport;
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
        frameRef.current?.contentWindow?.postMessage({type:"rdev-browser-viewport", ...viewportRef.current},location.origin);
        frameRef.current?.contentWindow?.postMessage({ type: "rdev-browser-visible", visible: visibleRef.current }, location.origin);
        frameRef.current?.contentWindow?.postMessage({ type: "rdev-browser-tools", visible: toolsRef.current }, location.origin);
      } else if (event.data?.type === "rdev-browser-resize" && validBrowserViewport(event.data)) {
        setViewport({width:event.data.width,height:event.data.height});
      } else if (event.data?.type === "rdev-browser-viewport-error") {
        setError(t("browser.resizeFailed"));
      } else if (event.data?.type === "rdev-browser-viewport-applied") {
        setError("");
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

  useEffect(() => {
    if (!ready) return;
    const timer = setTimeout(() => {
      frameRef.current?.contentWindow?.postMessage({type:"rdev-browser-viewport", ...viewport},location.origin);
      try { sessionStorage.setItem(viewportStorageKey(src),JSON.stringify(viewport)); } catch { /* Optional preference only. */ }
    },150);
    return () => clearTimeout(timer);
  }, [ready, viewport, src]);

  return <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
    <div className="flex shrink-0 flex-wrap items-center justify-end gap-1 border-b border-border-seam px-2 py-1">
      <Select value={`${viewport.width}x${viewport.height}`} onValueChange={value=>{const [width,height]=value.split('x').map(Number);setViewport({width,height});}}>
        <SelectTrigger aria-label={t("browser.viewport")} className="h-7 w-28 text-xs"><SelectValue>{[[390,844],[768,1024],[1280,800],[1920,1080]].some(([w,h])=>w===viewport.width&&h===viewport.height)?`${viewport.width} × ${viewport.height}`:t("browser.customSize")}</SelectValue></SelectTrigger>
        <SelectContent>{[[390,844],[768,1024],[1280,800],[1920,1080]].map(([w,h])=><SelectItem key={`${w}x${h}`} value={`${w}x${h}`}>{w} × {h}</SelectItem>)}</SelectContent>
      </Select>
      <DimensionInput label={t("browser.width")} max={3840} value={viewport.width} onCommit={width=>setViewport(v=>({...v,width}))} />
      <span className="text-xs text-muted-foreground">×</span>
      <DimensionInput label={t("browser.height")} max={2160} value={viewport.height} onCommit={height=>setViewport(v=>({...v,height}))} />
      <Button variant="ghost" size="sm" disabled={!ready} aria-pressed={showTools} onClick={() => {
        const visible = !showTools;
        setShowTools(visible);
        frameRef.current?.contentWindow?.postMessage({ type: "rdev-browser-tools", visible }, location.origin);
      }}>DevTools</Button>
    </div>
    {error && <p role="alert" className="shrink-0 px-3 py-2 text-xs text-destructive-text">{error}</p>}
    <div className="relative flex min-h-0 min-w-0 flex-1 flex-col">
    <iframe ref={frameRef} title={t("browser.preview")} src={`${src}&rdevPreview=1`} referrerPolicy="no-referrer" className="min-h-0 min-w-0 w-full flex-1 border-0" allow="clipboard-read; clipboard-write" />
    </div>
  </div>;
}
