import { useEffect, type RefObject } from "react";

/** Keep the composer inside the visible viewport when mobile keyboards pan or
 * resize the page. Pinch zoom retains the browser's normal layout behavior. */
export function useMobileViewport(
  shell: RefObject<HTMLDivElement | null>,
  enabled: boolean,
) {
  useEffect(() => {
    const element = shell.current;
    const viewport = window.visualViewport;
    if (!enabled || !element || !viewport) return;

    let frame = 0;
    let unfocusedHeight = viewport.height;
    const reset = () => {
      element.style.removeProperty("height");
      element.style.removeProperty("top");
      element.style.removeProperty("--app-safe-area-bottom");
    };
    const update = () => {
      frame = 0;
      if (viewport.scale !== 1) {
        reset();
        return;
      }
      const active = document.activeElement;
      const editing = active instanceof HTMLElement && (
        active.isContentEditable ||
        active instanceof HTMLInputElement ||
        active instanceof HTMLTextAreaElement ||
        active instanceof HTMLSelectElement
      );
      if (!editing) unfocusedHeight = viewport.height;
      const keyboardOpen = editing && unfocusedHeight - viewport.height >= 80;
      element.style.height = `${Math.round(viewport.height)}px`;
      element.style.top = `${Math.round(viewport.offsetTop)}px`;
      if (keyboardOpen) {
        element.style.setProperty("--app-safe-area-bottom", "0px");
      } else {
        element.style.removeProperty("--app-safe-area-bottom");
      }
    };
    const schedule = () => {
      if (!frame) frame = requestAnimationFrame(update);
    };
    update();
    viewport.addEventListener("resize", schedule);
    viewport.addEventListener("scroll", schedule);
    window.addEventListener("resize", schedule);
    document.addEventListener("focusin", schedule);
    document.addEventListener("focusout", schedule);
    return () => {
      cancelAnimationFrame(frame);
      viewport.removeEventListener("resize", schedule);
      viewport.removeEventListener("scroll", schedule);
      window.removeEventListener("resize", schedule);
      document.removeEventListener("focusin", schedule);
      document.removeEventListener("focusout", schedule);
      reset();
    };
  }, [enabled, shell]);
}
