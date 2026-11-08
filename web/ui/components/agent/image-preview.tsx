import { useEffect, useState } from "react";
import { ImageLightbox } from "../image-lightbox";
import { getWrappedImageIndex, IMAGE_TRANSPARENCY_CHECKER_STYLE } from "../../lib/image-preview";
import { cn } from "../../lib/utils";

type PreviewImage = { src: string; name: string };
const isPreviewSource = (src: string) => /^data:image\/(?:png|jpeg|jpg|gif|webp|avif|bmp);base64,/.test(src);

// Shared by composer attachments, sent messages and remote read_file results.
export function ImagePreview({ src, name, className, gallery }: PreviewImage & { className?: string; gallery?: PreviewImage[] }) {
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState<PreviewImage | null>(null);
  useEffect(() => { setOpen(false); setSelected(null); }, [src, name]);
  if (!isPreviewSource(src)) return null;
  const images = gallery?.filter(image => isPreviewSource(image.src)) ?? [{ src, name }];
  // The trigger must remain reachable even if a partially restored gallery
  // omits it. Selection follows image identity through insertions/reordering.
  let triggerIndex = images.findIndex(image => image.src === src && image.name === name);
  if (triggerIndex < 0) triggerIndex = images.push({ src, name }) - 1;
  const selectedIndex = selected ? images.findIndex(image => image.src === selected.src && image.name === selected.name) : -1;
  const index = selectedIndex < 0 ? triggerIndex : selectedIndex;
  const current = images[index];
  const move = (direction: "next" | "previous") => setSelected(previous => {
    const previousIndex = previous ? images.findIndex(image => image.src === previous.src && image.name === previous.name) : -1;
    return images[getWrappedImageIndex({ currentIndex: previousIndex < 0 ? triggerIndex : previousIndex, direction, itemCount: images.length })];
  });
  return <>
    <button type="button" aria-label={name} onClick={() => {
      setSelected({ src, name });
      setOpen(true);
    }} className="block max-w-full cursor-zoom-in overflow-hidden rounded-md border border-border bg-surface-recessed focus-visible:ring-2 focus-visible:ring-ring">
      <img src={src} alt={name} loading="lazy" decoding="async" style={IMAGE_TRANSPARENCY_CHECKER_STYLE} className={cn("max-h-64 max-w-full object-contain", className)} />
    </button>
    <ImageLightbox isOpen={open} imageSrc={current.src} imageAlt={current.name} title={current.name}
      hasMultipleImages={images.length > 1} navigationStatus={images.length > 1 ? `${index + 1} / ${images.length}` : undefined}
      onPrevious={() => move("previous")} onNext={() => move("next")} onClose={() => setOpen(false)} />
  </>;
}
