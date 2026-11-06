import { useCallback, useState, type Dispatch, type SetStateAction } from "react";

/** Drafts belong to a conversation, even when one pane switches threads. */
export function useComposerDraft<T>(key: string, empty: T): [T, Dispatch<SetStateAction<T>>] {
  const [drafts, setDrafts] = useState(() => new Map<string, T>());
  const update = useCallback<Dispatch<SetStateAction<T>>>((value) => {
    setDrafts(previous => {
      const current = previous.get(key) ?? empty;
      const next = typeof value === "function" ? (value as (draft: T) => T)(current) : value;
      if (Object.is(current, next)) return previous;
      return new Map(previous).set(key, next);
    });
  }, [key, empty]);
  return [drafts.get(key) ?? empty, update];
}
