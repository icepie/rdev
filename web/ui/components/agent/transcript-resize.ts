export const TRANSCRIPT_BOTTOM_THRESHOLD = 120;

/** Keep bottom attachment across layout changes that don't produce a protocol
 * frame (images, disclosure rows, fonts and viewport resizing). Like BB's
 * position keeper, observe both the viewport and its content, per pane. */
export function observeTranscriptResize(
  viewport: HTMLElement,
  content: HTMLElement,
  onAttachment: (attached: boolean) => void,
): () => void {
  const measure = () => ({
    top: viewport.scrollTop,
    height: viewport.scrollHeight,
    viewport: viewport.clientHeight,
  });
  const nearBottom = (size: ReturnType<typeof measure>) =>
    size.height - size.top - size.viewport < TRANSCRIPT_BOTTOM_THRESHOLD;
  let previous = measure();
  let attached = nearBottom(previous);
  let disposed = false;
  onAttachment(attached);

  const reconcile = () => {
    if (disposed || viewport.clientHeight === 0) return;
    const current = measure();
    const resized = current.height !== previous.height || current.viewport !== previous.viewport;
    // Content shrinking can clamp scrollTop. A deliberate upward movement
    // beyond that clamp must still release attachment, even before RO fires.
    const clampedTop = Math.min(previous.top, Math.max(0, current.height - current.viewport));
    if (resized && attached && current.top >= clampedTop) {
      viewport.scrollTop = Math.max(0, current.height - current.viewport);
    }
    previous = measure();
    attached = nearBottom(previous);
    onAttachment(attached);
  };

  viewport.addEventListener("scroll", reconcile, { passive: true });
  const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(reconcile);
  observer?.observe(viewport);
  observer?.observe(content);
  return () => {
    disposed = true;
    viewport.removeEventListener("scroll", reconcile);
    observer?.disconnect();
  };
}
