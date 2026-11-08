import { useState } from "react";
import type { Translator } from "./types";

// BB FilePreviewVideo: native media controls, metadata-only preload and a
// bounded black stage. The URL grants only this remote file for a limited time.
export function VideoFilePreview({ src, name, t }: { src: string; name: string; t: Translator }) {
  const [failed, setFailed] = useState(false);
  return <div className="pt-4">
    <video src={src} title={name} className="block max-h-[34rem] w-full bg-black"
      controls playsInline preload="metadata" onError={() => setFailed(true)} />
    {failed && <p role="status" className="p-3 text-xs text-muted-foreground">{t("agent.videoPlaybackFailed")}</p>}
  </div>;
}
