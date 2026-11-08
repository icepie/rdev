import { useCallback, useEffect, useState } from "react";
import type { ThreadInfo } from "./types";

// A picker changes the next message, not an in-flight turn. Keep that draft
// preference per thread so a completion or another pane cannot overwrite it.
export function useThreadModels(threads: readonly ThreadInfo[], fallback: string) {
  const [choices, setChoices] = useState(() => new Map<string, string>());
  const select = useCallback((thread: string, model: string) => {
    if (thread) setChoices(previous => new Map(previous).set(thread, model));
  }, []);
  const reset = useCallback(() => setChoices(new Map()), []);
  useEffect(() => {
    const present = new Set(threads.map(thread => thread.id));
    setChoices(previous => {
      const entries = [...previous].filter(([thread]) => present.has(thread));
      return entries.length === previous.size ? previous : new Map(entries);
    });
  }, [threads]);
  const modelFor = (thread: string) =>
    choices.get(thread) ?? threads.find(item => item.id === thread)?.model ?? fallback;
  return {modelFor, select, reset};
}
