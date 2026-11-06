import { useCallback, useRef, useState } from "react";

export async function copyText(text: string): Promise<void> {
  if (navigator.clipboard && window.isSecureContext)
    return navigator.clipboard.writeText(text);
  const area = document.createElement("textarea");
  const active = document.activeElement;
  const selection = active instanceof HTMLInputElement || active instanceof HTMLTextAreaElement
    ? { start: active.selectionStart, end: active.selectionEnd, direction: active.selectionDirection }
    : null;
  area.value = text;
  area.style.position = "fixed";
  area.style.left = "-9999px";
  document.body.appendChild(area);
  try {
    area.focus({ preventScroll: true });
    area.select();
    if (!document.execCommand("copy")) throw new Error("copy failed");
  } finally {
    area.remove();
    if (active instanceof HTMLElement && active.isConnected && !active.closest("[inert]")) {
      active.focus({ preventScroll: true });
      if (selection && selection.start !== null && selection.end !== null) {
        (active as HTMLInputElement | HTMLTextAreaElement).setSelectionRange(
          selection.start, selection.end, selection.direction ?? undefined,
        );
      }
    }
  }
}

/* copiedId/copy pair: copy(text) flashes copiedId for 1.5s. */
export function useCopy(): [string | null, (id: string, text: string) => void] {
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const timer = useRef<number | undefined>(undefined);
  const copy = useCallback((id: string, text: string) => {
    copyText(text)
      .then(() => {
        setCopiedId(id);
        window.clearTimeout(timer.current);
        timer.current = window.setTimeout(() => setCopiedId(null), 1500);
      })
      .catch(() => {});
  }, []);
  return [copiedId, copy];
}
