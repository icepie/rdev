import { useLayoutEffect, type RefObject } from "react";

/** CSS owns the height limits; measuring after reset also lets drafts shrink. */
export function useComposerAutosize(ref: RefObject<HTMLTextAreaElement | null>, value: string) {
  useLayoutEffect(() => {
    const editor = ref.current;
    if (!editor) return;
    const resize = () => {
      const scrollTop = editor.scrollTop;
      editor.style.height = "auto";
      editor.style.height = `${editor.scrollHeight}px`;
      editor.scrollTop = scrollTop;
    };
    resize();
    // Only width changes affect wrapping. Observing our own height writes
    // would otherwise cause unnecessary layouts (or a resize loop).
    if (typeof ResizeObserver === "undefined") return;
    let width = editor.getBoundingClientRect().width;
    const observer = new ResizeObserver(() => {
      const next = editor.getBoundingClientRect().width;
      if (next === width) return;
      width = next;
      resize();
    });
    observer.observe(editor);
    return () => observer.disconnect();
  }, [ref, value]);
}
