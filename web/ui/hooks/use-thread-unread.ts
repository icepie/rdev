import { useEffect, useRef, useState } from "react";
import type { ThreadInfo } from "../components/agent/types";

type Row = ThreadInfo & { device: string };
export const threadIdentity = (device: string, id: string) => JSON.stringify([device, id]);
const active = (row: Row) => row.running || (row.backgroundJobs ?? 0) > 0;

// Only observed active → idle transitions count as new completions. Loading
// history for the first time must not mark every old conversation unread.
export function useThreadUnread(rows: Row[], device: string, openIds: Set<string>) {
  const previous = useRef(new Map<string, boolean>());
  const [unread, setUnread] = useState<Set<string>>(() => new Set());
  const [visible, setVisible] = useState(() => document.visibilityState !== "hidden");
  useEffect(() => {
    const update = () => setVisible(document.visibilityState !== "hidden");
    document.addEventListener("visibilitychange", update);
    return () => document.removeEventListener("visibilitychange", update);
  }, []);
  useEffect(() => {
    const completed = new Set<string>();
    const seen = new Set<string>();
    for (const row of rows) {
      const key = threadIdentity(row.device, row.id);
      if (previous.current.get(key) === true && !active(row)) completed.add(key);
      previous.current.set(key, active(row));
      if (visible && row.device === device && openIds.has(row.id)) seen.add(key);
    }
    setUnread(current => {
      const next = new Set(current);
      for (const key of completed) next.add(key);
      for (const key of seen) next.delete(key);
      return next.size === current.size && [...next].every(key => current.has(key)) ? current : next;
    });
  }, [rows, device, openIds, visible]);
  return unread;
}
