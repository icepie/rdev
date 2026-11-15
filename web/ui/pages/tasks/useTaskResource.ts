import { useCallback, useEffect, useRef, useState } from "react";

// One in-flight read per mounted resource. Refreshes coalesce, cleanup aborts,
// and generation checks reject even transports that ignore AbortSignal.
export function useTaskResource<T>(key: string, read: (signal: AbortSignal) => Promise<T>, poll = true) {
  const reader = useRef(read);
  reader.current = read;
  const [snapshot, setSnapshot] = useState<{ key: string; data?: T; error: string }>({ key, error: "" });
  const refreshRef = useRef<() => Promise<void>>(async () => {});
  useEffect(() => {
    let stopped = false;
    let controller: AbortController | undefined;
    let flight: Promise<void> | undefined;
    let timer: number | undefined;
    const refresh = (): Promise<void> => {
      if (stopped || document.visibilityState === "hidden") return Promise.resolve();
      if (flight) return flight;
      clearTimeout(timer);
      controller = new AbortController();
      const signal = controller.signal;
      let timedOut = false;
      const { promise: timeout, reject } = Promise.withResolvers<never>();
      const aborted = () => reject(new Error("Task request canceled"));
      signal.addEventListener("abort", aborted, { once: true });
      const deadline = window.setTimeout(() => {
        timedOut = true;
        controller?.abort();
        reject(new Error("Task request timed out"));
      }, 10000);
      flight = (async () => {
        try {
          const data = await Promise.race([reader.current(signal), timeout]);
          if (!stopped && !signal.aborted) setSnapshot({ key, data, error: "" });
        } catch (error) {
          if (!stopped && (!signal.aborted || timedOut)) setSnapshot(old => ({ key, data: old.key === key ? old.data : undefined, error: timedOut ? "Task request timed out" : error instanceof Error ? error.message : String(error) }));
        } finally {
          signal.removeEventListener("abort", aborted);
          controller?.abort();
          clearTimeout(deadline);
          flight = undefined;
          if (!stopped && poll) timer = window.setTimeout(() => void refresh(), 3000);
        }
      })();
      return flight;
    };
    refreshRef.current = refresh;
    const visible = () => { if (document.visibilityState !== "hidden") void refresh(); };
    document.addEventListener("visibilitychange", visible);
    void refresh();
    return () => { stopped = true; controller?.abort(); clearTimeout(timer); document.removeEventListener("visibilitychange", visible); };
  }, [key, poll]);
  const refresh = useCallback(() => refreshRef.current(), []);
  const setError = useCallback((error: string) => setSnapshot(old => ({ key, data: old.key === key ? old.data : undefined, error })), [key]);
  const data = snapshot.key === key ? snapshot.data : undefined;
  return { data, loading: data === undefined, error: snapshot.key === key ? snapshot.error : "", refresh, setError };
}
