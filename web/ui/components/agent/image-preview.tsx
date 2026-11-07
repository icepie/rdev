import { useState } from "react";
import { Dialog, DialogContent, DialogTitle, DialogTrigger } from "../dialog";
import { cn } from "../../lib/utils";

// Shared by composer attachments, sent messages and remote read_file results.
export function ImagePreview({ src, name, className }: { src: string; name: string; className?: string }) {
  const [actualSize, setActualSize] = useState(false);
  if (!/^data:image\/(?:png|jpeg|jpg|gif|webp|avif|bmp);base64,/.test(src)) return null;
  return <Dialog onOpenChange={() => setActualSize(false)}>
    <DialogTrigger asChild>
      <button type="button" aria-label={name} className="block max-w-full cursor-zoom-in overflow-hidden rounded-md border border-border bg-surface-recessed focus-visible:ring-2 focus-visible:ring-ring">
        <img src={src} alt={name} loading="lazy" decoding="async" className={cn("max-h-64 max-w-full object-contain", className)} />
      </button>
    </DialogTrigger>
    <DialogContent aria-describedby={undefined} className="max-w-[min(90vw,80rem)]">
      <DialogTitle className="truncate pr-6 text-sm" title={name}>{name}</DialogTitle>
      <div className="max-h-[75dvh] overflow-auto rounded bg-surface-recessed">
        <button type="button" aria-label={name} aria-pressed={actualSize} onClick={() => setActualSize(value => !value)}
          className={cn("block min-w-full", actualSize ? "cursor-zoom-out" : "cursor-zoom-in")}>
          <img src={src} alt={name} className={cn("mx-auto", actualSize ? "max-w-none" : "max-h-[70dvh] max-w-full object-contain")} />
        </button>
      </div>
    </DialogContent>
  </Dialog>;
}
