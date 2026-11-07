import { useCallback, useSyncExternalStore, type Dispatch, type SetStateAction } from "react";

const PREFIX = "rdev-composer-text:";
const MAX_SAVED_LENGTH = 64 * 1024;
const drafts = new Map<string, string>();
const listeners = new Map<string, Set<() => void>>();

function read(key: string): string {
  if (!drafts.has(key)) {
    let saved = "";
    try { saved = window.sessionStorage.getItem(PREFIX + key) || ""; } catch { /* Storage unavailable. */ }
    drafts.set(key, saved.length <= MAX_SAVED_LENGTH ? saved : "");
  }
  return drafts.get(key)!;
}

function writeDraft(key: string, value: string) {
  drafts.set(key, value);
  try {
    if (value && value.length <= MAX_SAVED_LENGTH) window.sessionStorage.setItem(PREFIX + key, value);
    else window.sessionStorage.removeItem(PREFIX + key);
  } catch { /* The live draft remains available when storage is full. */ }
  listeners.get(key)?.forEach(listener => listener());
}

/** Called only after the server confirms deletion of this device/thread. */
export function discardComposerTextDraft(key: string) {
  writeDraft(key, "");
}

/** Reconcile only an auth_ok full list, never a live or filtered sidebar list.
 * Missing/malformed legacy snapshots must not erase recoverable drafts. */
export function reconcileComposerTextDrafts(device: string, currentThread: string, threads: unknown) {
  if (!device || !currentThread || !Array.isArray(threads)) return;
  const present = new Set<string>();
  for (const thread of threads) {
    if (!thread || typeof thread !== "object" || typeof thread.id !== "string" || !thread.id) return;
    present.add(thread.id);
  }
  if (!present.has(currentThread)) return;
  const keys = new Set(drafts.keys());
  try {
    for (let index = 0; index < window.sessionStorage.length; index++) {
      const key = window.sessionStorage.key(index);
      if (key?.startsWith(PREFIX)) keys.add(key.slice(PREFIX.length));
    }
  } catch { /* Still reconcile live drafts when storage is unavailable. */ }
  for (const key of keys) {
    let identity: unknown;
    try { identity = JSON.parse(key); } catch { continue; }
    if (Array.isArray(identity) && identity.length === 2 && identity[0] === device &&
        typeof identity[1] === "string" && identity[1] && !present.has(identity[1])) {
      discardComposerTextDraft(key);
    }
  }
}

/** Text follows its conversation across pane remounts and tab reloads. */
export function useComposerTextDraft(key: string): [string, Dispatch<SetStateAction<string>>] {
  const subscribe = useCallback((listener: () => void) => {
    let group = listeners.get(key);
    if (!group) listeners.set(key, group = new Set());
    group.add(listener);
    return () => {
      group.delete(listener);
      if (!group.size) listeners.delete(key);
    };
  }, [key]);
  const snapshot = useCallback(() => read(key), [key]);
  const value = useSyncExternalStore(subscribe, snapshot, () => "");
  const update = useCallback<Dispatch<SetStateAction<string>>>((action) => {
    const current = read(key);
    const next = typeof action === "function" ? action(current) : action;
    if (current === next) return;
    writeDraft(key, next);
  }, [key]);
  return [value, update];
}
