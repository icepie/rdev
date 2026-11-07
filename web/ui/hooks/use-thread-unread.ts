import { useEffect, useRef, useState } from "react";
import type { ThreadInfo } from "../components/agent/types";

type Row = ThreadInfo & { device: string };
export const threadIdentity = (device: string, id: string) => JSON.stringify([device, id]);
const active = (row: Row) => row.running || (row.backgroundJobs ?? 0) > 0;

const STORAGE_KEY = "rdev-thread-unread-v1";

type Observation = { active: boolean; outcomeAt: string };

function restoreUnread(): { previous: Map<string, Observation>; unread: Set<string> } {
  try {
    const stored = JSON.parse(sessionStorage.getItem(STORAGE_KEY) || "null");
    if (Array.isArray(stored)) {
      const entries = stored.filter((entry: unknown): entry is [string, boolean, boolean, string?] =>
        Array.isArray(entry) && (entry.length === 3 || (entry.length === 4 && typeof entry[3] === "string")) && typeof entry[0] === "string" &&
        typeof entry[1] === "boolean" && typeof entry[2] === "boolean").slice(-1000);
      return { previous: new Map(entries.map(([key, running, , outcomeAt]) => [key, { active: running, outcomeAt: outcomeAt || "" }])),
        unread: new Set(entries.filter(([, , unread]) => unread).map(([key]) => key)) };
    }
  } catch { /* Storage may be unavailable or contain obsolete data. */ }
  return { previous: new Map(), unread: new Set() };
}

// Tab-scoped persistence survives reload without another tab marking these
// conversations read. Persist observed running states too, so completion while
// disconnected is detected on the next authoritative thread list.
export function useThreadUnread(rows: Row[], device: string, openIds: Set<string>) {
  const [restored] = useState(restoreUnread);
  const previous = useRef(restored.previous);
  const lastStored = useRef("");
  const [unread, setUnread] = useState<Set<string>>(restored.unread);
  const [visible, setVisible] = useState(() => document.visibilityState !== "hidden");
  useEffect(() => {
    const update = () => setVisible(document.visibilityState !== "hidden");
    document.addEventListener("visibilitychange", update);
    return () => document.removeEventListener("visibilitychange", update);
  }, []);
  useEffect(() => {
    const next = new Set(unread);
    for (const row of rows) {
      const key = threadIdentity(row.device, row.id);
      const before = previous.current.get(key);
      const outcomeAt = row.outcomeAt || "";
      if (before && ((before.active && !active(row)) ||
        (outcomeAt && outcomeAt !== before.outcomeAt))) next.add(key);
      // Move current rows to the end, retaining recent observations when capped.
      previous.current.delete(key);
      previous.current.set(key, { active: active(row), outcomeAt });
      if (visible && row.device === device && openIds.has(row.id)) next.delete(key);
    }
    while (previous.current.size > 1000) {
      const key = previous.current.keys().next().value!;
      previous.current.delete(key);
      next.delete(key);
    }
    const serialized = JSON.stringify([...previous.current].map(([key, observed]) => [key, observed.active, next.has(key), observed.outcomeAt]));
    if (serialized !== lastStored.current) {
      try { sessionStorage.setItem(STORAGE_KEY, serialized); lastStored.current = serialized; }
      catch { /* Keep in-memory behavior when storage is blocked/full. */ }
    }
    if (next.size !== unread.size || [...next].some(key => !unread.has(key))) setUnread(next);
  }, [rows, device, openIds, visible, unread]);
  return unread;
}
