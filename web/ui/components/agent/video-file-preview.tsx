import { useEffect, useRef, useState } from "react";
import type { Translator } from "./types";

// BB FilePreviewVideo layout; release remote media while the persistent side
// panel is hidden. Returning restores position without surprising autoplay.
export function VideoFilePreview({ src, name, visible = true, t }: {
  src: string; name: string; visible?: boolean; t: Translator;
}) {
  const [failed, setFailed] = useState(false);
  const video = useRef<HTMLVideoElement>(null);
  const position = useRef(0);
  useEffect(() => {
    const element = video.current;
    if (!element || !visible) return;
    setFailed(false);
    element.src = src;
    element.load();
    return () => {
      position.current = element.currentTime;
      element.pause();
      element.removeAttribute("src");
      element.load(); // abort pending Range requests, even after DOM removal
    };
  }, [src, visible]);
  return <div className="pt-4">
    <video ref={video} title={name} className="block max-h-[34rem] w-full bg-black"
      controls playsInline preload="metadata" onError={() => { if (visible) setFailed(true); }}
      onLoadedMetadata={() => {
        const element = video.current;
        if (element && visible && position.current > 0 && position.current < element.duration) {
          element.currentTime = position.current;
        }
      }} />
    {failed && <p role="status" className="p-3 text-xs text-muted-foreground">{t("agent.videoPlaybackFailed")}</p>}
  </div>;
}
