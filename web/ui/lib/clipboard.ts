import { useCallback, useRef, useState } from "react";

export function copyText(text: string): Promise<void> {
  if (navigator.clipboard && window.isSecureContext)
    return navigator.clipboard.writeText(text);
  const area = document.createElement("textarea");
  area.value = text;
  area.style.position = "fixed";
  area.style.left = "-9999px";
  document.body.appendChild(area);
  area.focus();
  area.select();
  const ok = document.execCommand("copy");
  area.remove();
  return ok ? Promise.resolve() : Promise.reject(new Error("copy failed"));
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
