import { useCallback, useEffect, useRef, useState } from "react";
import { apiFetch, apiJSON } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { Button } from "./button";
import { Card, CardContent, CardHeader, CardTitle } from "./card";

type Identity = {
  identity: string;
  clientId: string;
  revoked: boolean;
  createdAt: string;
  revokedAt?: string;
};
type Audit = { at: string; action: string; identity: string; clientId: string };
type Snapshot = {
  devices: Identity[];
  audit: Audit[];
  openMode: boolean;
  health: { durable: boolean; persistenceFault: boolean; initialized: boolean };
};

export function IdentityManagement() {
  const { t } = useI18n();
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(false);
  const mounted = useRef(false);
  const readRequest = useRef<AbortController | null>(null);
  const revokeRequest = useRef<AbortController | null>(null);
  const refresh = useCallback(async () => {
    if (!mounted.current || readRequest.current) return;
    const controller = new AbortController();
    readRequest.current = controller;
    setLoading(true);
    const deadline = window.setTimeout(() => controller.abort(), 10000);
    try {
      const result = await apiJSON<Snapshot>("/api/identity", { signal: controller.signal });
      if (mounted.current && readRequest.current === controller) { setSnapshot(result); setError(""); }
    } catch (cause) {
      if (mounted.current && readRequest.current === controller) setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      clearTimeout(deadline);
      if (readRequest.current === controller) {
        readRequest.current = null;
        if (mounted.current) setLoading(false);
      }
    }
  }, []);
  useEffect(() => {
    mounted.current = true;
    void refresh();
    return () => {
      mounted.current = false;
      readRequest.current?.abort();
      revokeRequest.current?.abort();
      readRequest.current = null;
      revokeRequest.current = null;
    };
  }, [refresh]);
  const revoke = async (identity: Identity) => {
    if (revokeRequest.current || !window.confirm(t("identity.confirmRevoke", { device: identity.clientId }))) return;
    const controller = new AbortController();
    revokeRequest.current = controller;
    readRequest.current?.abort();
    readRequest.current = null;
    setLoading(false);
    setBusy(true);
    const deadline = window.setTimeout(() => controller.abort(), 10000);
    try {
      const response = await apiFetch("/api/identity/revoke", { method: "POST", signal: controller.signal, headers: { "Content-Type": "application/json" }, body: JSON.stringify({ deviceIdentity: identity.identity }) });
      if (!response.ok) throw new Error(`${response.status} ${response.statusText}`);
      if (mounted.current && revokeRequest.current === controller) await refresh();
    } catch (cause) {
      if (mounted.current && revokeRequest.current === controller) setError(`${t("identity.revokeFailed")} ${cause instanceof Error ? cause.message : String(cause)}`);
    } finally {
      clearTimeout(deadline);
      if (revokeRequest.current === controller) {
        revokeRequest.current = null;
        if (mounted.current) setBusy(false);
      }
    }
  };
  return <Card className="min-w-0">
    <CardHeader><CardTitle>{t("identity.title")}</CardTitle></CardHeader>
    <CardContent className="space-y-3" aria-busy={busy || loading}>
      {snapshot && <p className="text-sm text-muted-foreground">{t("identity.registry")}: {t(snapshot.health.durable ? "identity.durable" : "identity.memory")}{snapshot.health.persistenceFault ? ` · ${t("identity.persistenceFault")}` : ""} · {t(snapshot.openMode ? "identity.openMode" : "identity.protectedMode")}</p>}
      {!snapshot && loading && <p className="text-sm text-muted-foreground">{t("identity.loading")}</p>}
      {error && <p role="alert" className="text-sm text-destructive-text">{error}</p>}
      {snapshot?.devices.length === 0 && <p className="text-sm text-muted-foreground">{t("identity.empty")}</p>}
      <div className="space-y-2">{snapshot?.devices.map((device) => <div key={device.identity} className="flex flex-wrap items-center gap-2 rounded-md border p-2 text-sm">
        <span className="font-medium">{device.clientId}</span><span className="min-w-0 break-all text-muted-foreground">{device.identity}</span><span>{t(device.revoked ? "identity.revoked" : "identity.enrolled")}</span>
        {!device.revoked && <Button variant="destructive" size="sm" disabled={busy} onClick={() => void revoke(device)}>{t("identity.revoke")}</Button>}
      </div>)}</div>
      <div className="space-y-1 border-t pt-3"><h3 className="text-sm font-medium">{t("identity.audit")}</h3>{snapshot?.audit.slice(-20).reverse().map((item, index) => <p key={`${item.at}-${index}`} className="text-xs text-muted-foreground">{item.at} · {t(`identity.action.${item.action}`)} · {item.clientId}</p>)}</div>
      <Button variant="outline" size="sm" disabled={busy || loading} onClick={() => void refresh()}>{t("identity.refresh")}</Button>
    </CardContent>
  </Card>;
}
