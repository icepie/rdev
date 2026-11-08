import { useEffect, useState } from "react";
import { useI18n } from "../../lib/i18n";
import { Icon } from "../icon";
import { ImageLightbox } from "../image-lightbox";
import { getWrappedImageIndex, IMAGE_TRANSPARENCY_CHECKER_STYLE } from "../../lib/image-preview";
import { cn } from "../../lib/utils";

type PreviewImage = { src: string; name: string };
const isPreviewSource = (src: string) => /^data:image\/(?:png|jpeg|jpg|gif|webp|avif|bmp);base64,/.test(src);

// Shared by composer attachments, sent messages and remote read_file results.
export function ImagePreview({ src, name, className, gallery, sent = false }: PreviewImage & { className?: string; gallery?: PreviewImage[]; sent?: boolean }) {
  const { t } = useI18n();
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState<PreviewImage | null>(null);
  useEffect(() => { setOpen(false); setSelected(null); setFailed(false); setAttempt(0); }, [src, name]);
  if (!isPreviewSource(src)) return null;
  // Closed thumbnails do not scan their gallery or mount modal hooks.
  const images = (open ? gallery?.filter(image => isPreviewSource(image.src)) : undefined) ?? [{ src, name }];
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
    <button type="button" aria-label={failed ? `${t("agent.imageRetry")}: ${name}` : name}
      title={failed ? t("agent.imageLoadFailed") : name} onClick={() => {
      if (failed) {
        setFailed(false);
        setAttempt(value => value + 1);
        return;
      }
      setSelected({ src, name });
      setOpen(true);
    }} className={cn("block max-w-full overflow-hidden rounded-md border focus-visible:ring-2 focus-visible:ring-ring", sent ? "border-surface-selected-border bg-surface-raised" : "border-border bg-surface-recessed", failed ? "cursor-pointer" : "cursor-zoom-in")}>
      {failed ? <span role="status" className={cn("flex min-h-16 flex-col items-center justify-center gap-1 p-2 text-xs text-muted-foreground", className)}>
        <Icon name="Image" className="size-4 shrink-0" />
        <span className="sr-only">{t("agent.imageLoadFailed")}</span>
        <span>{t("agent.imageRetry")}</span>
      </span> : <img key={attempt} onError={() => setFailed(true)} src={src} alt={name} loading="lazy" decoding="async" style={IMAGE_TRANSPARENCY_CHECKER_STYLE} className={cn("max-h-64 max-w-full object-contain", className)} />}
    </button>
    {open && <ImageLightbox isOpen imageSrc={current.src} imageAlt={current.name} title={current.name}
      hasMultipleImages={images.length > 1} navigationStatus={images.length > 1 ? `${index + 1} / ${images.length}` : undefined}
      onPrevious={() => move("previous")} onNext={() => move("next")} onClose={() => setOpen(false)} />}
  </>;
}
