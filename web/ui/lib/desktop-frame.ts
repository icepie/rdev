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


/** Resizing a canvas resets its backing store and context state, even when the
 * assigned dimension is unchanged. Preserve it across same-resolution frames. */
export function paintDesktopCanvas(context: CanvasRenderingContext2D, image: CanvasImageSource, width: number, height: number) {
  const canvas = context.canvas;
  const resized = canvas.width !== width || canvas.height !== height;
  if (canvas.width !== width) canvas.width = width;
  if (canvas.height !== height) canvas.height = height;
  // Resize already cleared the canvas. Otherwise remove old pixels before
  // painting, including when the next frame contains transparent areas.
  if (!resized) context.clearRect(0, 0, width, height);
  context.drawImage(image, 0, 0, width, height);
}
