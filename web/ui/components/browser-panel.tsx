import type { BrowserActionRecord } from "./agent/browser-history";
import type { AutomationView } from "./agent/automation";
import { BrowserPreview } from "./browser-preview";
import { useEffect, useRef, useState } from "react";
import { apiFetch, getDevicePassword } from "../lib/api";
import { useDevices } from "../lib/devices";
import { useI18n } from "../lib/i18n";
import { Button } from "./button";
import { Input } from "./input";
import { Icon } from "./icon";
import { DeviceIcon } from "./device-icon";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./select";

interface Target { id: string; title: string; url: string }

/** Chrome's own inspector frontend includes its interactive Screencast app. */
export function BrowserPanel({ device, visible = true, deviceSelection = "inline", automation, history = [], replayId = "", onReplay }: { history?: BrowserActionRecord[]; replayId?: string; onReplay?: (id: string) => void; automation?: AutomationView; device: string; visible?: boolean; deviceSelection?: "inline" | "sidebar" }) {
  const { t, lang } = useI18n();
  const replay = history.find(row => row.id === replayId && row.thumbnail);
  const { devices } = useDevices();
  const [selectedDevice, setExitDevice] = useState(device === "@server" ? "" : device);
  const exitDevice = deviceSelection === "sidebar" ? device : selectedDevice;
  const [password, setPassword] = useState(() => getDevicePassword(device));
  const [base, setBase] = useState("");
  const [targets, setTargets] = useState<Target[]>([]);
  const [selected, setSelected] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const online = devices.some(d => d.id === exitDevice);
  const attachedAutomation = useRef(false);
  useEffect(() => {
    if (!automation?.base || !automation.target) {
      if (attachedAutomation.current) { setBase(""); setSelected(""); setTargets([]); }
      attachedAutomation.current = false;
      return;
    }
    attachedAutomation.current = true;
    setExitDevice(automation.device);
    setBase(automation.base);
    setSelected(automation.target);
  }, [automation?.base, automation?.target, automation?.device]);

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
        setSelected(current => automation?.target ?? (rows.some(row => row.id === current) ? current : rows[0]?.id ?? ""));
        setError("");
      } catch (e) {
        if (!controller.signal.aborted) setError(e instanceof Error ? e.message : String(e));
      } finally {
        if (!controller.signal.aborted) timer = setTimeout(refresh, 5000);
      }
    };
    void refresh();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [base, visible, t, automation?.target]);

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
  const inspector = base && selected ? `${base}/devtools/inspector.html?${new URLSearchParams({ [location.protocol === "https:" ? "wss" : "ws"]: `${location.host}${base}/devtools/page/${selected}`, dockSide: "undocked", rdevLang: lang === "zh" ? "zh" : "en-US" })}` : "";

  return <div className="flex min-h-0 min-w-0 w-full flex-1 flex-col overflow-hidden bg-background">
    <div className="flex min-h-11 shrink-0 flex-wrap items-center gap-2 border-b border-border-seam px-3 py-2 text-xs">
      <Icon name="Globe" className="size-4 text-muted-foreground" />
      <span className="min-w-0 flex-1 truncate">{t("nav.browser")}{exitDevice && ` · ${exitDevice}`}</span>
      {base && <>
        <Button variant="ghost" size="sm" disabled={busy || !!automation} onClick={() => void newTab()} aria-label={t("agent.newTabLabel")}><Icon name="Plus" className="size-4" /></Button>
        <Select value={selected} onValueChange={setSelected} disabled={targets.length === 0 || !!automation}>
          <SelectTrigger aria-label={t("browser.tabs")} className="h-8 w-auto min-w-0 max-w-48 text-xs">
            <SelectValue placeholder={t("browser.tabs")} />
          </SelectTrigger>
          <SelectContent>
            {targets.map(target => <SelectItem key={target.id} value={target.id}>{target.title || target.url}</SelectItem>)}
          </SelectContent>
        </Select>
        {inspector && <a href={inspector} target="_blank" rel="noreferrer" className="rounded px-2 py-1 hover:bg-state-hover">{t("browser.expand")}</a>}
        <Button variant="ghost" size="sm" disabled={busy || !!automation} onClick={() => void stop()}>{t("browser.stop")}</Button>
      </>}
    </div>
    {error && <p role="alert" className="px-3 py-2 text-xs text-destructive-text">{error}</p>}
    {!online && base && <p role="status" className="px-3 py-2 text-xs text-muted-foreground">{t("browser.offline")}</p>}
    <div className="relative flex min-h-0 flex-1 flex-col overflow-hidden">
    <div className="flex min-h-0 flex-1 flex-col" inert={!!replay} aria-hidden={!!replay}>
    {!base ? <form onSubmit={e => { e.preventDefault(); void start(); }} className="m-auto flex min-h-0 min-w-0 w-full max-w-sm flex-col gap-3 overflow-y-auto p-4">
      <p className="text-sm text-muted-foreground">{t("browser.intro")}</p>
      {deviceSelection === "sidebar" ? (
        exitDevice ? <div className="flex min-w-0 items-center gap-2 text-sm">
          <DeviceIcon device={exitDevice} os={devices.find(d => d.id === exitDevice)?.os} />
          <span className="min-w-0 truncate" title={exitDevice}>{exitDevice}</span>
        </div> : <p className="text-sm text-muted-foreground">{t("browser.selectFromSidebar")}</p>
      ) : <label className="flex flex-col gap-1 text-xs">{t("browser.exitDevice")}
        <Select disabled={busy} value={exitDevice} onValueChange={value => { setExitDevice(value); setPassword(getDevicePassword(value)); }}>
          <SelectTrigger aria-label={t("browser.exitDevice")} className="min-w-0 w-full">
            <SelectValue placeholder={t("agent.selectDeviceShort")} />
          </SelectTrigger>
          <SelectContent>
            {devices.map(d => <SelectItem key={d.id} value={d.id}>{d.id}</SelectItem>)}
          </SelectContent>
        </Select>
      </label>}
      {exitDevice && <>
        {devices.find(d => d.id === exitDevice)?.hasPassword && <Input disabled={busy} type="password" aria-label={t("browser.password")} placeholder={t("browser.password")} value={password} onChange={e => setPassword(e.target.value)} />}
        <Button type="submit" disabled={!online || busy}>{t(busy ? "common.loading" : "browser.start")}</Button>
      </>}
      <p className="text-xs text-muted-foreground">{t("browser.lifetime")}</p>
    </form> : inspector ? <BrowserPreview key={inspector} src={inspector} visible={visible} /> : <p role="status" className="p-4 text-xs text-muted-foreground">{t("common.loading")}</p>}
    </div>
    {replay && <div className="absolute inset-0 z-10 flex min-h-0 flex-col bg-background">
      <div className="flex shrink-0 items-center justify-between gap-2 border-b border-border-seam px-3 py-1 text-xs">
        <span>{t("browser.replay")} · {history.indexOf(replay)+1}/{history.length} · {replay.width} × {replay.height}</span>
        <Button size="sm" variant="ghost" onClick={() => onReplay?.("")}>{t("browser.live")}</Button>
      </div>
      <img src={replay.thumbnail} alt={t("browser.replay") + " · " + replay.action} className="min-h-0 w-full flex-1 object-contain" />
    </div>}
    </div>
    {history.length > 0 && <section aria-label={t("browser.history")} className="flex max-h-[35%] min-h-24 shrink-0 flex-col border-t border-border-seam">
      <div className="flex shrink-0 items-center justify-between px-3 py-2 text-xs text-muted-foreground">
        <span>{t("browser.history")} · {history.length} · {history.filter(r=>!r.ok).length} {t("browser.failures")}</span>
        <span>{(history.reduce((n,r)=>n+r.durationMs,0)/1000).toFixed(1)}s</span>
      </div>
      <ol className="min-h-0 overflow-y-auto px-2 pb-2">
        {history.map((row,index)=><li key={row.id}><button type="button" disabled={!row.thumbnail} aria-pressed={replay?.id===row.id} onClick={()=>onReplay?.(replay?.id===row.id?"":row.id)} className="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-xs hover:bg-state-hover aria-pressed:bg-state-hover disabled:cursor-default disabled:opacity-60">
          <span className={row.ok?"text-muted-foreground":"text-destructive-text"}>{row.ok?"✓":"!"}</span>
          <span className="min-w-0 flex-1 truncate">{index+1}. {row.action}</span>
          <time className="text-muted-foreground">{new Date(row.startedAt).toLocaleTimeString()}</time>
          <span className="text-muted-foreground">{row.durationMs}ms</span>
          {row.thumbnail && <img src={row.thumbnail} alt="" className="h-7 w-10 rounded object-cover" />}
        </button></li>)}
      </ol>
    </section>}
  </div>;
}
