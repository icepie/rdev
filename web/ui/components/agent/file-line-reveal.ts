import { useCallback, useEffect, useRef, type RefObject } from "react";
import type { FileOptions } from "@pierre/diffs/react";

// Reveal only inside the file pane. scrollIntoView would also move outer
// dialogs/the conversation, and the renderer's rows live in a shadow root.
export function revealFileLine(container: HTMLElement, host: HTMLElement, line: number): boolean {
  if (!Number.isInteger(line) || line < 1 || !host.isConnected || !container.contains(host) || container.clientHeight === 0) return false;
  const row = (host.shadowRoot ?? host).querySelector<HTMLElement>(`[data-line="${line}"]`);
  if (!row) return false;
  const bounds = row.getBoundingClientRect();
  if (bounds.height === 0) return false;
  container.scrollTop = Math.max(0, container.scrollTop + bounds.top - container.getBoundingClientRect().top - container.clientTop - Math.max(0, (container.clientHeight - bounds.height) / 2));
  return true;
}

export function useFileLineReveal(container: RefObject<HTMLDivElement | null>, range: { start: number; end: number } | null | undefined, enabled: boolean): NonNullable<FileOptions<undefined, undefined>["onPostRender"]> {
  const host = useRef<HTMLElement | null>(null);
  const revealed = useRef<{ host: HTMLElement; target: string } | null>(null);
  const frame = useRef<number | undefined>(undefined);
  const start = range?.start, end = range?.end;
  const cancel = useCallback(() => { if (frame.current !== undefined) window.cancelAnimationFrame(frame.current); frame.current = undefined; }, []);
  const schedule = useCallback(() => {
    cancel();
    if (start === undefined) { revealed.current = null; return; }
    if (!enabled) return;
    frame.current = window.requestAnimationFrame(() => {
      frame.current = undefined;
      const node = host.current;
      const target = `${start}:${end}`;
      if (!node || !container.current || (revealed.current?.host === node && revealed.current.target === target)) return;
      if (revealFileLine(container.current, node, start)) revealed.current = { host: node, target };
    });
  }, [cancel, container, start, end, enabled]);
  useEffect(() => { schedule(); return cancel; }, [schedule, cancel]);
  return useCallback((node, _instance, phase) => {
    if (phase === "unmount") {
      if (host.current === node) { host.current = null; cancel(); }
      return;
    }
    host.current = node;
    schedule();
  }, [schedule, cancel]);
}
