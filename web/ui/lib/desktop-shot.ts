/** Screenshot capture shared by the standalone desktop page and the agent
 * desktop panel. Both stage the active surface (GPU canvas, MSE video frame or
 * the legacy canvas) and hand the resulting PNG to the browser as a download. */

/** Save a rendered canvas as `rdev-<device>-<timestamp>.png`. */
function downloadDesktopCanvas(targetCanvas: HTMLCanvasElement, device: string) {
  targetCanvas.toBlob((blob) => {
    if (!blob) return;
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `rdev-${device || "desktop"}-${new Date()
      .toISOString()
      .replace(/[:.]/g, "-")}.png`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }, "image/png");
}

/** Download whatever the current stream is painting; a no-op before the first
 * frame so an empty stage never yields a blank PNG. */
export function saveDesktopScreenshot({
  gpu,
  videoMode,
  gpuCanvas,
  video,
  canvas,
  canvasShown,
  device,
}: {
  gpu: boolean;
  videoMode: "mse" | "webcodecs";
  gpuCanvas: HTMLCanvasElement | null;
  video: HTMLVideoElement | null;
  canvas: HTMLCanvasElement | null;
  canvasShown: boolean;
  device: string;
}) {
  if (gpu && videoMode === "webcodecs" && gpuCanvas?.width && gpuCanvas?.height) {
    downloadDesktopCanvas(gpuCanvas, device);
    return;
  }
  if (gpu && video?.videoWidth && video?.videoHeight) {
    const shot = document.createElement("canvas");
    shot.width = video.videoWidth;
    shot.height = video.videoHeight;
    shot.getContext("2d")?.drawImage(video, 0, 0, shot.width, shot.height);
    downloadDesktopCanvas(shot, device);
    return;
  }
  if (!canvas || !canvas.width || !canvas.height || !canvasShown) return;
  downloadDesktopCanvas(canvas, device);
}
