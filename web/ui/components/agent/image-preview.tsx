import { useState } from "react";
import { ImageLightbox } from "../image-lightbox";
import { getWrappedImageIndex, IMAGE_TRANSPARENCY_CHECKER_STYLE } from "../../lib/image-preview";
import { cn } from "../../lib/utils";

type PreviewImage = { src: string; name: string };
const isPreviewSource = (src: string) => /^data:image\/(?:png|jpeg|jpg|gif|webp|avif|bmp);base64,/.test(src);

// Shared by composer attachments, sent messages and remote read_file results.
export function ImagePreview({ src, name, className, gallery }: PreviewImage & { className?: string; gallery?: PreviewImage[] }) {
  const [open, setOpen] = useState(false);
  const [index, setIndex] = useState(0);
  if (!isPreviewSource(src)) return null;
  const images = gallery?.filter(image => isPreviewSource(image.src)) ?? [{ src, name }];
  const current = images[index] ?? { src, name };
  const move = (direction: "next" | "previous") => setIndex(value => getWrappedImageIndex({ currentIndex: value, direction, itemCount: images.length }));
  return <>
    <button type="button" aria-label={name} onClick={() => {
      setIndex(Math.max(0, images.findIndex(image => image.src === src && image.name === name)));
      setOpen(true);
    }} className="block max-w-full cursor-zoom-in overflow-hidden rounded-md border border-border bg-surface-recessed focus-visible:ring-2 focus-visible:ring-ring">
      <img src={src} alt={name} loading="lazy" decoding="async" style={IMAGE_TRANSPARENCY_CHECKER_STYLE} className={cn("max-h-64 max-w-full object-contain", className)} />
    </button>
    <ImageLightbox isOpen={open} imageSrc={current.src} imageAlt={current.name} title={current.name}
      hasMultipleImages={images.length > 1} navigationStatus={images.length > 1 ? `${index + 1} / ${images.length}` : undefined}
      onPrevious={() => move("previous")} onNext={() => move("next")} onClose={() => setOpen(false)} />
  </>;
}
