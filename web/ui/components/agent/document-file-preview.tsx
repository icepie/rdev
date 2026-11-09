import { useEffect, useRef, useState } from "react";
import type { Translator } from "./types";

export function DocumentFilePreview({ src, mime, name, visible, t }: {
  src: string; mime: string; name: string; visible: boolean; t: Translator;
}) {
  const audio = useRef<HTMLAudioElement>(null);
  const position = useRef(0);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const element = audio.current;
    if (!element || !visible) return;
    setFailed(false); element.src = src; element.load();
    return () => { position.current = element.currentTime; element.pause(); element.removeAttribute("src"); element.load(); };
  }, [src, visible]);
  if (mime === "application/pdf") return <div className="flex h-full flex-col">
    <a href={src} target="_blank" rel="noreferrer" className="shrink-0 px-3 py-2 text-xs text-timeline-accent">{t("files.pdfFallback")}</a>
    {visible && <iframe title={name} src={src} referrerPolicy="no-referrer" className="min-h-0 w-full flex-1 border-0" />}
  </div>;
  return <div className="flex h-full flex-col items-center justify-center gap-4 p-6">
    <p className="max-w-full truncate text-sm">{name}</p>
    <audio ref={audio} title={name} controls preload="metadata" className="w-full max-w-lg"
      onError={() => { if (visible) setFailed(true); }} onLoadedMetadata={() => {
        if (audio.current && position.current < audio.current.duration) audio.current.currentTime = position.current;
      }} />
    {failed && <p role="status" className="text-xs text-muted-foreground">{t("files.mediaFailed")}</p>}
  </div>;
}
