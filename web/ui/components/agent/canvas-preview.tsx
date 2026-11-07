import bbThemeCSS from "../../styles/bb-theme.css?inline";
import { useTheme } from "../../lib/theme";
import { themePresets } from "../../lib/themes";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Markdown } from "../markdown";
import { SyntaxCode } from "../syntax-code";
import { Button } from "../button";
import { useCopy } from "../../lib/clipboard";
import type { ToolItem, Translator } from "./types";

export function canvasDocument(content: string, mode: "light" | "dark" = "light", presetCSS = ""): string {
  // Opaque origin + CSP follow Hope's preview separation. No host bridge is
  // exposed: the only parent-to-frame message updates theme CSS; the frame
  // cannot invoke rdev tools or send application actions.
  return `<!doctype html><html class="${mode}"><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:; connect-src 'none'; form-action 'none'; base-uri 'none'"><meta name="viewport" content="width=device-width,initial-scale=1"><style>${bbThemeCSS}body{margin:0;padding:16px;font:14px/1.5 var(--font-sans);color:var(--foreground);background:var(--background)}*{box-sizing:border-box}button,input,select,textarea{font:inherit;color:inherit}button{cursor:pointer;border:1px solid var(--border);border-radius:6px;background:var(--secondary);padding:6px 12px}button:hover{background:var(--state-hover)}img,svg,canvas{max-width:100%}</style><style id="rdev-canvas-theme">${presetCSS}</style><script>
(()=>{const style=document.getElementById('rdev-canvas-theme');
window.addEventListener('message',event=>{const theme=event.data;
if(event.source!==parent||!theme||theme.type!=='rdev:canvas-theme'||!['light','dark'].includes(theme.mode)||typeof theme.css!=='string')return;
document.documentElement.classList.remove('light','dark');document.documentElement.classList.add(theme.mode);style.textContent=theme.css;
});})();
</script></head><body>${content}</body></html>`;
}

export function CanvasPreview({ tool, t }: { tool: ToolItem; t: Translator }) {
  const [source, setSource] = useState(false);
  const [copied, copy] = useCopy();
  const canvas = useMemo(() => {
    try {
      const args = JSON.parse(tool.args);
      const result = JSON.parse(tool.result || "null");
      if (result?.status !== "ready") return null;
      return typeof args.title === "string" && typeof args.content === "string" &&
        args.content.length <= 65536 && ["html", "markdown"].includes(args.format) ? args : null;
    } catch { return null; }
  }, [tool.args, tool.result]);
  if (!canvas || !tool.done || tool.isError) return <pre className="whitespace-pre-wrap text-xs">{tool.result || "…"}</pre>;
  return <section className="overflow-hidden rounded-lg border border-border bg-card">
    <header className="flex min-w-0 items-center gap-2 border-b border-border-seam px-3 py-1">
      <span className="min-w-0 flex-1 truncate text-sm" title={canvas.title}>{canvas.title}</span>
      <Button size="sm" variant="ghost" aria-pressed={source} onClick={() => setSource(value => !value)}>{t(source ? "agent.canvas.preview" : "agent.canvas.source")}</Button>
      <Button size="sm" variant="ghost" onClick={() => copy(tool.id, canvas.content)}>{t(copied === tool.id ? "common.copied" : "common.copy")}</Button>
    </header>
    {source && <pre className="max-h-96 overflow-auto p-3 text-xs"><SyntaxCode code={canvas.content} language={canvas.format} /></pre>}
    {canvas.format === "html" ? <CanvasFrame key={canvas.content} content={canvas.content} title={canvas.title} hidden={source} />
      : !source && <div className="max-h-[65dvh] overflow-auto p-4 text-sm"><Markdown text={canvas.content} /></div>}
  </section>;
}


// One immutable document per content revision. Theme changes are presentation
// updates, so neither they nor the source toggle reload interactive state.
function CanvasFrame({ content, title, hidden }: { content: string; title: string; hidden: boolean }) {
  const theme = useTheme();
  const frame = useRef<HTMLIFrameElement>(null);
  const [doc] = useState(() => canvasDocument(content, theme.resolved, themePresets[theme.preset].css));
  const sendTheme = useCallback(() => {
    // sandbox omits allow-same-origin, hence the target has an opaque origin.
    frame.current?.contentWindow?.postMessage({ type: "rdev:canvas-theme", mode: theme.resolved, css: themePresets[theme.preset].css }, "*");
  }, [theme.resolved, theme.preset]);
  useEffect(sendTheme, [sendTheme]);
  return <iframe ref={frame} title={title} hidden={hidden} inert={hidden} onLoad={sendTheme}
    sandbox="allow-scripts" referrerPolicy="no-referrer" srcDoc={doc}
    className={hidden ? "hidden" : "h-[min(32rem,65dvh)] w-full border-0 bg-card"} />;
}
