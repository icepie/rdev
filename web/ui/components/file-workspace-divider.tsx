import { useEffect, useRef } from "react";
/** Update a CSS variable directly while dragging; keep file/preview components mounted. */
export function FileWorkspaceDivider({
  axis = "x",
  storageKey,
  label,
}: {
  axis?: "x" | "y";
  storageKey: string;
  label: string;
}) {
  const ref = useRef<HTMLButtonElement>(null);
  const drag = useRef(false);
  const ratio = useRef(35);
  const persist = () => {
    try {
      sessionStorage.setItem(storageKey, String(ratio.current));
    } catch {}
  };
  const finishDrag = () => {
    if (!drag.current) return;
    drag.current = false;
    persist();
  };
  const apply = (value: number, save = true) => {
    ratio.current = Math.max(20, Math.min(75, value));
    ref.current?.parentElement?.style.setProperty(
      "--file-tree-split",
      `${ratio.current}%`,
    );
    ref.current?.setAttribute(
      "aria-valuenow",
      String(Math.round(ratio.current)),
    );
    if (save) persist();
  };
  useEffect(() => {
    let value = axis === "x" ? 30 : 45;
    try {
      const raw = sessionStorage.getItem(storageKey);
      if (raw !== null && Number.isFinite(Number(raw))) value = Number(raw);
    } catch {}
    apply(value);
  }, [storageKey, axis]);
  return (
    <button
      ref={ref}
      type="button"
      role="separator"
      aria-label={label}
      aria-orientation={axis === "x" ? "vertical" : "horizontal"}
      aria-valuemin={20}
      aria-valuemax={75}
      aria-valuenow={ratio.current}
      className={`file-workspace-divider file-workspace-divider-${axis} absolute z-10 touch-none bg-transparent hover:bg-timeline-accent/30 focus-visible:bg-timeline-accent/30 focus-visible:outline-none`}
      onPointerDown={(e) => {
        drag.current = true;
        e.currentTarget.setPointerCapture(e.pointerId);
      }}
      onPointerMove={(e) => {
        if (!drag.current) return;
        const r = e.currentTarget.parentElement!.getBoundingClientRect();
        const size = axis === "x" ? r.width : r.height;
        if (size > 0)
          apply(
            ((axis === "x" ? e.clientX - r.left : e.clientY - r.top) / size) *
              100,
            false,
          );
      }}
      onPointerUp={(e) => {
        finishDrag();
        if (e.currentTarget.hasPointerCapture(e.pointerId))
          e.currentTarget.releasePointerCapture(e.pointerId);
      }}
      onPointerCancel={finishDrag}
      onLostPointerCapture={finishDrag}
      onKeyDown={(e) => {
        if (
          ["ArrowLeft", "ArrowUp", "ArrowRight", "ArrowDown"].includes(e.key)
        ) {
          e.preventDefault();
          apply(
            ratio.current + (["ArrowLeft", "ArrowUp"].includes(e.key) ? -2 : 2),
          );
        }
      }}
    />
  );
}
