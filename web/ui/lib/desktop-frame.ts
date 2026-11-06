/** Decode asynchronously, but paint only while the originating stream owns it. */
export async function paintDesktopImage(
  blob: Blob,
  current: () => boolean,
  paint: (image: CanvasImageSource, width: number, height: number) => void,
  fallback: (blob: Blob) => Promise<HTMLImageElement>,
): Promise<boolean> {
  let bitmap: ImageBitmap | undefined;
  if (typeof window.createImageBitmap === "function") {
    try { bitmap = await window.createImageBitmap(blob); } catch { /* use image decoding */ }
  }
  if (bitmap) {
    try {
      if (!current()) return false;
      paint(bitmap, bitmap.width, bitmap.height);
      return true;
    } finally { bitmap.close(); }
  }
  if (!current()) return false;
  const image = await fallback(blob);
  if (!current()) return false;
  paint(image, image.naturalWidth || image.width, image.naturalHeight || image.height);
  return true;
}
