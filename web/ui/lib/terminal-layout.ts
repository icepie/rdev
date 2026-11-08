// Coalesce divider/viewport/observer updates without retaining a disposed
// terminal in animation callbacks. Hidden panes keep invalidation until shown.
export function createTerminalLayout({ visible, fit, repaint, invalidate }: {
  visible: () => boolean;
  fit: () => void;
  repaint: () => void;
  invalidate: () => void;
}) {
  let frame: number | null = null;
  let dirty = false;
  let disposed = false;
  const cancel = () => {
    if (frame !== null) window.cancelAnimationFrame(frame);
    frame = null;
  };
  const flush = () => {
    cancel();
    if (disposed || !visible()) return;
    try { fit(); } catch { /* layout is not ready yet */ }
    if (dirty) {
      dirty = false;
      try { invalidate(); } catch { /* renderer may have no atlas */ }
    }
    try { repaint(); } catch { /* renderer may not be initialized */ }
  };
  return {
    request(invalidateAtlas = false) {
      if (disposed) return;
      dirty ||= invalidateAtlas;
      if (frame === null) frame = window.requestAnimationFrame(flush);
    },
    flush,
    dispose() { disposed = true; cancel(); },
  };
}
