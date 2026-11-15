import { useEffect, useState } from "react";
import { apiJSON } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { Card, CardContent, CardHeader, CardTitle } from "./card";
import { Badge } from "./badge";

interface Health {
  status: "ok" | "degraded";
  at: string;
  uptimeSeconds: number;
  resources: { devices: number; sessions: number; forwards: number; transfers: number; agentTransfers: number; agentRunning: number; agentQueued: number };
  scheduler: { initialized: boolean; stopped: boolean; schedules: number; pending: number; running: number; persistenceFault?: string; storedBytes: number; lastCommitBytes: number; lastCommitAt: string };
  history: { loaded: number; faulted: number; faults?: { device: string; thread: string; error: string }[] };
  identity: { initialized: boolean; durable: boolean; persistenceFault: boolean };
  transferFailures: { device: number; transport: number };
}

// Completion-paced polling: no overlap, bounded request lifetime, no background
// tab traffic, and no stale updates after unmount. Existing data stays visible.
export function HealthPanel() {
  const { t } = useI18n();
  const [health, setHealth] = useState<Health | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let disposed = false;
    let controller: AbortController | undefined;
    let timer: number | undefined;
    const load = async () => {
      if (disposed || controller || document.visibilityState === "hidden") return;
      clearTimeout(timer);
      const request = new AbortController();
      controller = request;
      const deadline = window.setTimeout(() => request.abort(), 10000);
      try {
        const result = await apiJSON<Health>("/api/health", { signal: request.signal });
        if (!disposed) { setHealth(result); setError(""); }
      } catch (cause) {
        if (!disposed) setError(cause instanceof Error ? cause.message : String(cause));
      } finally {
        clearTimeout(deadline);
        controller = undefined;
        if (!disposed) timer = window.setTimeout(() => void load(), 10000);
      }
    };
    const onVisibility = () => {
      if (document.visibilityState === "visible") void load();
      else clearTimeout(timer);
    };
    void load();
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      disposed = true;
      clearTimeout(timer);
      controller?.abort();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, []);
  const counters = health ? [
    ["health.transfers", health.resources.transfers + health.resources.agentTransfers],
    ["health.agentRunning", health.resources.agentRunning],
    ["health.agentQueued", health.resources.agentQueued],
    ["health.schedulePending", health.scheduler.pending],
    ["health.scheduleRunning", health.scheduler.running],
    ["health.historyFaults", health.history.faulted],
    ["health.deviceFailures", health.transferFailures.device],
    ["health.transportFailures", health.transferFailures.transport],
  ] as const : [];
  return <Card>
    <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2 pb-3">
      <CardTitle className="text-base">{t("health.title")}</CardTitle>
      <Badge variant="outline">{t(error ? "health.stale" : health ? `health.${health.status}` : "health.loading")}</Badge>
    </CardHeader>
    <CardContent className="space-y-3">
      {error && <p role="alert" className="break-words text-sm text-destructive-text">{t("health.loadFailed")}: {error}</p>}
      {health && <>
        <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          {counters.map(([label, value]) => <div key={label}><dt className="text-xs text-muted-foreground">{t(label)}</dt><dd className="text-lg font-semibold tabular-nums">{value}</dd></div>)}
        </dl>
        <p className="text-xs text-muted-foreground">{t("health.observed", { time: new Date(health.at).toLocaleTimeString(), seconds: health.uptimeSeconds })}</p>
        {!health.scheduler.initialized && <p className="text-xs text-muted-foreground">{t("health.schedulerIdle")}</p>}
        {health.scheduler.stopped && <p role="alert" className="text-sm text-destructive-text">{t("health.schedulerStopped")}</p>}
        {health.scheduler.persistenceFault && <p role="alert" className="break-words text-sm text-destructive-text">{health.scheduler.persistenceFault}</p>}
        {health.identity.persistenceFault && <p role="alert" className="text-sm text-destructive-text">{t("health.identityFault")}</p>}
        {!health.identity.durable && <p className="text-xs text-muted-foreground">{t("health.identityEphemeral")}</p>}
        {(health.history.faults ?? []).map(fault => <p role="alert" key={`${fault.device}/${fault.thread}`} className="break-words text-sm text-destructive-text">{fault.device} / {fault.thread}: {fault.error}</p>)}
        <p className="text-xs text-muted-foreground">{t("health.scope")}</p>
      </>}
    </CardContent>
  </Card>;
}
