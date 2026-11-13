import type { AutomationView } from "./agent/automation";
import { BrowserPreview } from "./browser-preview";
import { type FormEvent, useEffect, useRef, useState } from "react";
import { apiFetch, getDevicePassword } from "../lib/api";
import { useDevices } from "../lib/devices";
import { useServerCapabilities } from "../lib/server-capabilities";
import { useI18n } from "../lib/i18n";
import { Button } from "./button";
import { Input } from "./input";
import { Icon } from "./icon";
import { DeviceIcon } from "./device-icon";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./select";

interface Target { id: string; title: string; url: string }

/** Chrome's own inspector frontend includes its interactive Screencast app. */
export function BrowserPanel({ device, visible = true, deviceSelection = "inline", automation }: { automation?: AutomationView; device: string; visible?: boolean; deviceSelection?: "inline" | "sidebar" }) {
  const { devicePasswordInputsHidden } = useServerCapabilities();
  const { devices } = useDevices();
  const { t, lang } = useI18n();
  const [selectedDevice, setExitDevice] = useState(device === "@server" ? "" : device);
  const exitDevice = deviceSelection === "sidebar" ? device : selectedDevice;
  const [password, setPassword] = useState(() => getDevicePassword(device));
  const [base, setBase] = useState("");
  const [targets, setTargets] = useState<Target[]>([]);
  const [selected, setSelected] = useState("");
  const [busy, setBusy] = useState(false);
  const [address, setAddress] = useState("");
  const addressRef = useRef<HTMLInputElement>(null);
  const [error, setError] = useState("");
  const [targetError, setTargetError] = useState("");
  const [languageError, setLanguageError] = useState("");
  const online = devices.some(d => d.id === exitDevice);
  const attachedAutomation = useRef(false);
  const recoveryAttempted = useRef(new Set<string>());
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
    if (!base) { setLanguageError(""); return; }
    const controller = new AbortController();
    const updateLanguage = async () => {
      try {
        const response = await fetch(`${base}/language`, {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ lang: lang === "zh" ? "zh" : "en" }),
          signal: controller.signal,
        });
        if (!response.ok) throw new Error(`Language update failed (${response.status})`);
        if (!controller.signal.aborted) setLanguageError("");
      } catch (e) {
        if (!controller.signal.aborted) setLanguageError(e instanceof Error ? e.message : String(e));
      }
    };
    void updateLanguage();
    return () => controller.abort();
  }, [base, lang]);

  useEffect(() => {
    if (!base || !visible) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const refresh = async () => {
      try {
        const response = await fetch(`${base}/targets`, { signal: controller.signal });
        if (response.status === 404 && !recoveryAttempted.current.has(base)) {
          recoveryAttempted.current.add(base);
          const targetDevice = automation?.device ?? exitDevice;
          const stopped = await fetch(`${base}/session`, { method: "DELETE", signal: controller.signal });
          if (!stopped.ok && stopped.status !== 404) throw new Error(t("browser.expired"));
          const restart = await apiFetch("/api/browser", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ device: targetDevice, password: targetDevice === exitDevice ? password : getDevicePassword(targetDevice), lang: lang === "zh" ? "zh" : "en" }),
          });
          if (restart.ok) {
            const session: { base: string } = await restart.json();
            if (session.base !== base && !controller.signal.aborted) {
              setBase(session.base);
              setTargets([]);
              setSelected("");
              setTargetError("");
              return;
            }
          }
        }
        if (!response.ok) throw new Error(t("browser.expired"));
        const rows: Target[] = await response.json();
        if (controller.signal.aborted) return;
        setTargets(rows);
        setSelected(current => automation?.target ?? (rows.some(row => row.id === current) ? current : rows[0]?.id ?? ""));
        setTargetError("");
      } catch (e) {
        if (!controller.signal.aborted) setTargetError(e instanceof Error ? e.message : String(e));
      } finally {
        if (!controller.signal.aborted) timer = setTimeout(refresh, 5000);
      }
    };
    void refresh();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [base, visible, t, automation?.target, automation?.device, exitDevice, password, lang]);

  const selectedTarget = targets.find(target => target.id === selected);
  useEffect(() => {
    if (selectedTarget && document.activeElement !== addressRef.current) setAddress(selectedTarget.url);
  }, [selectedTarget?.url, selected]);

  const start = async () => {
    if (busy || !online) return;
    setBusy(true); setError("");
    try {
      const response = await apiFetch("/api/browser", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ device: exitDevice, password, lang: lang === "zh" ? "zh" : "en" }) });
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

  const navigate = async (event: FormEvent) => {
    event.preventDefault();
    const raw = address.trim();
    if (busy || automation || !selected || !raw) return;
    const url = /^https?:\/\//i.test(raw) ? raw : `https://${raw}`;
    setBusy(true); setError("");
    try {
      const response = await fetch(`${base}/navigate`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ target: selected, url }),
      });
      if (!response.ok) throw new Error((await response.text()).trim() || t("browser.navigateFailed"));
      setAddress(url);
    } catch (e) { setError(e instanceof Error ? e.message : String(e)); }
    finally { setBusy(false); }
  };
  const inspector = base && selected ? `${base}/devtools/inspector.html?${new URLSearchParams({ [location.protocol === "https:" ? "wss" : "ws"]: `${location.host}${base}/devtools/page/${selected}`, dockSide: "undocked", rdevLang: lang === "zh" ? "zh" : "en-US" })}` : "";

  return <div className="flex min-h-0 min-w-0 w-full flex-1 flex-col overflow-hidden bg-background">
    <div className="flex min-h-11 shrink-0 flex-wrap items-center gap-2 border-b border-border-seam px-3 py-2 text-xs">
      <Icon name="Globe" className="size-4 text-muted-foreground" />
      <span className="min-w-0 max-w-48 truncate">{t("nav.browser")}{exitDevice && ` · ${exitDevice}`}</span>
      {base && <>
        <Button variant="ghost" size="sm" disabled={busy || !!automation} onClick={() => void newTab()} aria-label={t("agent.newTabLabel")}><Icon name="Plus" className="size-4" /></Button>
        <Select value={selected} onValueChange={value => { setSelected(value); setAddress(targets.find(target => target.id === value)?.url ?? ""); }} disabled={targets.length === 0 || !!automation}>
          <SelectTrigger aria-label={t("browser.tabs")} className="h-8 w-auto min-w-0 max-w-48 text-xs">
            <SelectValue placeholder={t("browser.tabs")} />
          </SelectTrigger>
          <SelectContent>
            {targets.map(target => <SelectItem key={target.id} value={target.id}>{target.title || target.url}</SelectItem>)}
          </SelectContent>
        </Select>
        <form onSubmit={navigate} className="flex min-w-0 flex-1 items-center gap-1">
          <Input ref={addressRef} aria-label={t("browser.address")} placeholder={t("browser.addressPlaceholder")} value={address} onChange={event => setAddress(event.target.value)} disabled={busy || !selected || !!automation} className="h-8 min-w-0 flex-1 text-xs" />
          <Button type="submit" size="sm" disabled={busy || !selected || !!automation}>{t("browser.go")}</Button>
        </form>
        {inspector && <a href={inspector} target="_blank" rel="noreferrer" className="rounded px-2 py-1 hover:bg-state-hover">{t("browser.expand")}</a>}
        <Button variant="ghost" size="sm" disabled={busy || !!automation} onClick={() => void stop()}>{t("browser.stop")}</Button>
      </>}
    </div>
    {error && <p role="alert" className="px-3 py-2 text-xs text-destructive-text">{error}</p>}
    {languageError && <p role="alert" className="px-3 py-2 text-xs text-destructive-text">{languageError}</p>}
    {targetError && <p role="alert" className="px-3 py-2 text-xs text-destructive-text">{targetError}</p>}
    {!online && base && <p role="status" className="px-3 py-2 text-xs text-muted-foreground">{t("browser.offline")}</p>}
    <div className="relative flex min-h-0 flex-1 flex-col overflow-hidden">
    <div className="flex min-h-0 flex-1 flex-col">
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
        {!devicePasswordInputsHidden && devices.find(d => d.id === exitDevice)?.hasPassword && <Input disabled={busy} type="password" aria-label={t("browser.password")} placeholder={t("browser.password")} value={password} onChange={e => setPassword(e.target.value)} />}
        <Button type="submit" disabled={!online || busy}>{t(busy ? "common.loading" : "browser.start")}</Button>
      </>}
      <p className="text-xs text-muted-foreground">{t("browser.lifetime")}</p>
    </form> : inspector ? <BrowserPreview key={inspector} src={inspector} visible={visible} /> : <p role="status" className="p-4 text-xs text-muted-foreground">{t("common.loading")}</p>}
    </div>
    </div>

  </div>;
}
