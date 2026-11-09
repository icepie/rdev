import { useEffect, useState } from "react";
import { apiFetch, getDevicePassword } from "../lib/api";
import { useDevices } from "../lib/devices";
import { useI18n } from "../lib/i18n";
import { Button } from "./button";
import { Input } from "./input";
import { Icon } from "./icon";

interface Target { id: string; title: string; url: string }

/** Chrome's own inspector frontend includes its interactive Screencast app. */
export function BrowserPanel({ device, visible = true }: { device: string; visible?: boolean }) {
  const { t } = useI18n();
  const { devices } = useDevices();
  const [exitDevice, setExitDevice] = useState(device === "@server" ? "" : device);
  const [password, setPassword] = useState(() => getDevicePassword(device));
  const [base, setBase] = useState("");
  const [targets, setTargets] = useState<Target[]>([]);
  const [selected, setSelected] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const online = devices.some(d => d.id === exitDevice);

  useEffect(() => {
    if (!base || !visible) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const refresh = async () => {
      try {
        const response = await fetch(`${base}/targets`, { signal: controller.signal });
        if (!response.ok) throw new Error(t("browser.expired"));
        const rows: Target[] = await response.json();
        if (controller.signal.aborted) return;
        setTargets(rows);
        setSelected(current => rows.some(row => row.id === current) ? current : rows[0]?.id ?? "");
        setError("");
      } catch (e) {
        if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
      } finally {
        if (!controller.signal.aborted) timer = setTimeout(refresh, 5000);
      }
    };
    void refresh();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [base, visible, t]);

  const start = async () => {
    if (busy || !online) return;
    setBusy(true); setError("");
    try {
      const response = await apiFetch("/api/browser", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ device: exitDevice, password }) });
      if (!response.ok) throw new Error((await response.text()).trim());
      const session: { base: string } = await response.json();
      setBase(session.base);
    } catch (e) { setError(e instanceof Error ? e.message : String(e)); }
    finally { setBusy(false); }
  };
  const stop = async () => {
    setBusy(true);
    try {
      const response = await fetch(`${base}/session`, { method: "DELETE" });
      if (!response.ok && response.status !== 404) throw new Error(t("browser.stopFailed"));
      setBase(""); setSelected(""); setTargets([]); setError("");
    } catch (e) { setError(e instanceof Error ? e.message : String(e)); }
    finally { setBusy(false); }
  };
  const newTab = async () => {
    setBusy(true); setError("");
    try {
      const response = await fetch(`${base}/targets`, { method: "POST" });
      if (!response.ok) throw new Error(t("browser.expired"));
      const target: Target = await response.json();
      setTargets(current => [...current, target]); setSelected(target.id);
    } catch (e) { setError(e instanceof Error ? e.message : String(e)); }
    finally { setBusy(false); }
  };
  const inspector = base && selected ? `${base}/devtools/inspector.html?${new URLSearchParams({ [location.protocol === "https:" ? "wss" : "ws"]: `${location.host}${base}/devtools/page/${selected}`, dockSide: "undocked" })}` : "";

  return <div className="flex min-h-0 flex-1 flex-col bg-background">
    <div className="flex min-h-11 flex-wrap items-center gap-2 border-b border-border-seam px-3 py-2 text-xs">
      <Icon name="Globe" className="size-4 text-muted-foreground" />
      <span className="min-w-0 flex-1 truncate">{t("nav.browser")}{base && ` · ${exitDevice}`}</span>
      {base && <>
        <Button variant="ghost" size="sm" disabled={busy} onClick={() => void newTab()} aria-label={t("agent.newTabLabel")}><Icon name="Plus" className="size-4" /></Button>
        <select aria-label={t("browser.tabs")} value={selected} onChange={e => setSelected(e.target.value)} className="max-w-48 min-w-0 rounded border border-border bg-background px-2 py-1">
          {targets.map(target => <option key={target.id} value={target.id}>{target.title || target.url}</option>)}
        </select>
        {inspector && <a href={inspector} target="_blank" rel="noreferrer" className="rounded px-2 py-1 hover:bg-state-hover">{t("browser.expand")}</a>}
        <Button variant="ghost" size="sm" disabled={busy} onClick={() => void stop()}>{t("browser.stop")}</Button>
      </>}
    </div>
    {error && <p role="alert" className="px-3 py-2 text-xs text-destructive-text">{error}</p>}
    {!online && base && <p role="status" className="px-3 py-2 text-xs text-muted-foreground">{t("browser.offline")}</p>}
    {!base ? <form onSubmit={e => { e.preventDefault(); void start(); }} className="m-auto flex w-full max-w-sm flex-col gap-3 p-6">
      <p className="text-sm text-muted-foreground">{t("browser.intro")}</p>
      <label className="flex flex-col gap-1 text-xs">{t("browser.exitDevice")}
        <select disabled={busy} value={exitDevice} onChange={e => { setExitDevice(e.target.value); setPassword(getDevicePassword(e.target.value)); }} className="h-9 rounded-md border border-border bg-background px-2">
          <option value="">{t("agent.selectDeviceShort")}</option>
          {devices.map(d => <option key={d.id} value={d.id}>{d.id}</option>)}
        </select>
      </label>
      <Input disabled={busy} type="password" aria-label={t("browser.password")} placeholder={t("browser.password")} value={password} onChange={e => setPassword(e.target.value)} />
      <Button type="submit" disabled={!online || busy}>{t(busy ? "common.loading" : "browser.start")}</Button>
      <p className="text-xs text-muted-foreground">{t("browser.lifetime")}</p>
    </form> : inspector && visible ? <iframe title={t("browser.preview")} src={inspector} referrerPolicy="no-referrer" className="min-h-0 w-full flex-1 border-0" allow="clipboard-read; clipboard-write" /> : <p role="status" className="p-4 text-xs text-muted-foreground">{t("common.loading")}</p>}
  </div>;
}
