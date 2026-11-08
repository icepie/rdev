import { MAX_IMAGE_BYTES } from "./protocol";

// Providers and server history decoders share PNG support. Rasterize only the
// browser formats missing from that common set; ordinary photos remain intact.
export function needsAttachmentRaster(mime: string) {
  return /^(?:image\/avif|image\/svg\+xml|image\/(?:x-icon|vnd\.microsoft\.icon))$/i.test(mime);
}

export function rasterizeAttachment(src: string, signal: AbortSignal): Promise<string> {
  return new Promise((resolve, reject) => {
    const image = new Image();
    let timer: ReturnType<typeof setTimeout>;
    let settled = false;
    const finish = (result?: string) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      signal.removeEventListener("abort", abort);
      image.onload = image.onerror = null;
      image.removeAttribute("src");
      if (result) resolve(result); else reject(new Error("image conversion failed"));
    };
    const abort = () => finish();
    signal.addEventListener("abort", abort, { once: true });
    if (signal.aborted) { finish(); return; }
    timer = setTimeout(abort, 10000);
    image.onerror = abort;
    image.onload = () => {
      try {
        const pixels = image.naturalWidth * image.naturalHeight;
        if (!pixels || pixels > 16_000_000) { finish(); return; }
        const canvas = document.createElement("canvas");
        let width = image.naturalWidth, height = image.naturalHeight;
        for (;;) {
          canvas.width = width; canvas.height = height;
          const context = canvas.getContext("2d");
          if (!context) { finish(); return; }
          context.drawImage(image, 0, 0, width, height);
          const url = canvas.toDataURL("image/png");
          if (!url.startsWith("data:image/png;base64,")) { finish(); return; }
          if (url.length - url.indexOf(",") - 1 <= Math.floor(MAX_IMAGE_BYTES / 3) * 4) {
            finish(url); return;
          }
          if (width === 1 && height === 1) { finish(); return; }
          width = Math.max(1, Math.floor(width / 2));
          height = Math.max(1, Math.floor(height / 2));
        }
      } catch { finish(); }
    };
    image.src = src;
  });
}
