import { useCallback, useContext, useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { MobileNavContext } from "../shell/AppShell";
import { useI18n } from "../lib/i18n";
import { apiJSON } from "../lib/api";
import { Button } from "../components/button";
import { Input } from "../components/input";
import { Label } from "../components/label";
import { Card, CardContent, CardHeader, CardTitle } from "../components/card";
import { Badge } from "../components/badge";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../components/tabs";
import { Icon } from "../components/icon";
import { cn } from "../lib/utils";

type Spec = {
  name: string; kind: "shell" | "agent"; device: string; cwd: string; cron: string; at: string; timezone: string;
  command: string; sessionMode: "new" | "existing"; thread: string; model: string; reasoning: string; prompt: string;
  allowedTools: string[]; timeoutSeconds: number; tokenBudget: number; busyPolicy: "skip" | "wait"; maxWaitSeconds: number;
  maxRuns: number; expiresAt: string; enabled: boolean; authorized: boolean; misfirePolicy: "skip" | "latest"; graceSeconds: number; mutexGroup: string;
};
type Schedule = Spec & { id: string; revision: number; state: string; nextRunAt?: string; createdAt?: string; updatedAt?: string; runs: number; events?: Change[] };
type Change = { at: string; action: string; source: string; revision: number; detail?: string };
type Run = { id: string; scheduleId: string; name: string; kind: string; device: string; source: string; state: string; reason?: string; scheduleRevision: number; scheduledAt?: string; startedAt?: string; finishedAt?: string; snapshot?: Spec; thread?: string; turn?: string; output?: string; truncated?: boolean; exitCode?: number; usage?: Record<string, unknown> };
type Options = { devices?: { id: string; name?: string; online?: boolean }[]; models?: { id: string; name?: string }[]; threads?: { id: string; title?: string; device?: string; cwd?: string; model?: string }[] };

const blank: Spec = { name: "", kind: "shell", device: "", cwd: "", cron: "", at: "", timezone: "UTC", command: "", sessionMode: "new", thread: "", model: "", reasoning: "", prompt: "", allowedTools: [], timeoutSeconds: 300, tokenBudget: 0, busyPolicy: "skip", maxWaitSeconds: 0, maxRuns: 0, expiresAt: "", enabled: true, authorized: false, misfirePolicy: "skip", graceSeconds: 0, mutexGroup: "" };

function unwrap<T>(response: { data?: T } | T): T {
  if (response && typeof response === "object" && "data" in response) return response.data as T;
  return response as T;
}
async function request<T>(action: string, extra: Record<string, unknown> = {}): Promise<T> {
  const response = await apiJSON<{ data?: T; error?: string }>("/api/schedules", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action, ...extra }) });
  if (response.error) throw new Error(response.error);
  return unwrap(response);
}
function fmt(value?: string) { if (!value) return "—"; const date = new Date(value); return Number.isNaN(date.valueOf()) || date.getFullYear() < 2000 ? "—" : date.toLocaleString(); }
function localDateInput(value: string) { if (!value) return ""; const date = new Date(value); if (!Number.isFinite(date.valueOf())) return ""; return new Date(date.valueOf() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16); }
function statusClass(state: string) { return state === "succeeded" || state === "enabled" ? "bg-success/15 text-success-foreground" : state === "failed" || state === "paused" ? "bg-destructive/15 text-destructive" : "bg-secondary text-secondary-foreground"; }
function Status({ state }: { state: string }) { const { t } = useI18n(); return <Badge variant="outline" className={cn("border-transparent", statusClass(state))}>{t(`tasks.status.${state}`)}</Badge>; }

function Shell({ children }: { children: ReactNode }) {
  const { open } = useContext(MobileNavContext);
  const { t } = useI18n();
  return <main className="flex min-h-0 flex-1 flex-col overflow-auto"><header className="flex min-h-14 items-center gap-3 border-b border-border-seam px-4"><Button variant="ghost" size="icon" className="md:hidden" onClick={() => open()} aria-label={t("nav.label")}><Icon name="PanelLeft" /></Button><h1 className="text-lg font-semibold">{t("tasks.title")}</h1></header><div className="mx-auto w-full max-w-6xl flex-1 p-4 md:p-6">{children}</div></main>;
}

export function TasksPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const [tab, setTab] = useState("schedules");
  const [items, setItems] = useState<Schedule[]>([]);
  const [runs, setRuns] = useState<Run[]>([]);
  const [filter, setFilter] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    if (document.visibilityState !== "visible") return;
    setLoading(true);
    setError("");
    try {
      if (tab === "schedules") {
        const [schedules, recentRuns] = await Promise.all([request<Schedule[]>("list"), request<Run[]>("runs")]);
        setItems(schedules);
        setRuns(recentRuns);
      } else setRuns(await request<Run[]>("runs"));
    } catch (e) { setError(e instanceof Error ? e.message : String(e)); }
    finally { setLoading(false); }
  }, [tab]);
  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 3000);
    const visible = () => { if (document.visibilityState === "visible") void load(); };
    document.addEventListener("visibilitychange", visible);
    return () => { window.clearInterval(timer); document.removeEventListener("visibilitychange", visible); };
  }, [load]);
  const shown = useMemo(() => {
    const q = filter.toLowerCase();
    return items.filter(x => !q || [x.name, x.kind, x.device, x.state].some(v => v?.toLowerCase().includes(q)));
  }, [items, filter]);
  const filteredRuns = useMemo(() => runs.filter(r => !filter || [r.name, r.state, r.device].some(v => v?.toLowerCase().includes(filter.toLowerCase()))), [runs, filter]);
  const runsBySchedule = useMemo(() => {
    const grouped = new Map<string, Run[]>();
    for (const run of runs) grouped.set(run.scheduleId, [...(grouped.get(run.scheduleId) || []), run]);
    return grouped;
  }, [runs]);
  return <Shell>
    <div className="mb-4 flex flex-wrap items-center justify-between gap-3"><p className="text-sm text-muted-foreground">{t("tasks.subtitle")}</p><Button onClick={() => navigate("/tasks/new")}><Icon name="Plus" />{t("tasks.new")}</Button></div>
    <Tabs value={tab} onValueChange={setTab}>
      <TabsList><TabsTrigger value="schedules">{t("tasks.schedules")}</TabsTrigger><TabsTrigger value="runs">{t("tasks.runs")}</TabsTrigger></TabsList>
      <div className="mt-3 flex gap-2"><Input value={filter} onChange={e => setFilter(e.target.value)} placeholder={t("tasks.filter")} aria-label={t("tasks.filter")} /><Button variant="outline" onClick={() => void load()}><Icon name="Refresh" />{t("tasks.refresh")}</Button></div>
      <TabsContent value="schedules"><Card><CardContent className="p-0">{loading ? <p className="p-6 text-sm text-muted-foreground">{t("tasks.loading")}</p> : error ? <p role="alert" className="p-6 text-sm text-destructive">{error}</p> : shown.length === 0 ? <p className="p-6 text-sm text-muted-foreground">{t("tasks.empty")}</p> : <div className="divide-y divide-border">{shown.map(schedule => {
        const history = [...(runsBySchedule.get(schedule.id) || [])].sort((a, b) => new Date(b.finishedAt || b.startedAt || b.scheduledAt || 0).valueOf() - new Date(a.finishedAt || a.startedAt || a.scheduledAt || 0).valueOf());
        const latest = history[0];
        const active = history.find(run => ["pending", "dispatching", "running"].includes(run.state));
        return <div key={schedule.id} className="flex flex-wrap items-center justify-between gap-3 p-4"><div className="min-w-0"><Link to={`/tasks/${encodeURIComponent(schedule.id)}`} className="font-medium hover:underline">{schedule.name || schedule.id}</Link><p className="text-xs text-muted-foreground">{schedule.kind} · {schedule.device || "—"} · {schedule.cron || schedule.at || "—"}</p><p className="mt-1 text-xs text-muted-foreground">{active ? <Link className="underline" to={`/tasks/runs/${encodeURIComponent(active.id)}`}>{t("tasks.currentRun")}: {t(`tasks.status.${active.state}`)}</Link> : latest ? <Link className="underline" to={`/tasks/runs/${encodeURIComponent(latest.id)}`}>{t("tasks.lastResult")}: {t(`tasks.status.${latest.state}`)} · {fmt(latest.finishedAt || latest.startedAt || latest.scheduledAt)}</Link> : t("tasks.noRunsYet")}</p></div><div className="flex items-center gap-2"><Status state={schedule.state} /><span className="text-xs text-muted-foreground">{schedule.nextRunAt ? fmt(schedule.nextRunAt) : "—"}</span></div></div>;
      })}</div>}</CardContent></Card></TabsContent>
      <TabsContent value="runs"><Card><CardContent className="p-0">{loading ? <p className="p-6 text-sm text-muted-foreground">{t("tasks.loading")}</p> : error ? <p role="alert" className="p-6 text-sm text-destructive">{error}</p> : filteredRuns.length === 0 ? <p className="p-6 text-sm text-muted-foreground">{t("tasks.emptyRuns")}</p> : <div className="divide-y divide-border">{filteredRuns.map(run => <div key={run.id} className="flex flex-wrap items-center justify-between gap-3 p-4"><div className="min-w-0"><Link to={`/tasks/runs/${encodeURIComponent(run.id)}`} className="font-medium hover:underline">{run.name || run.id}</Link><p className="text-xs text-muted-foreground">{fmt(run.startedAt || run.scheduledAt)} · <Link className="underline" to={`/tasks/${encodeURIComponent(run.scheduleId)}`}>{run.scheduleId}</Link></p></div><Status state={run.state} /></div>)}</div>}</CardContent></Card></TabsContent>
    </Tabs>
  </Shell>;
}

function Field({ label, children }: { label: string; children: ReactNode }) { return <label className="grid gap-1 text-sm"><span className="font-medium">{label}</span>{children}</label>; }
function TaskForm({ existing, duplicateId }: { existing?: Schedule; duplicateId?: string }) {
  const { t } = useI18n(); const navigate = useNavigate(); const [spec, setSpec] = useState<Spec>(existing ? { ...blank, ...existing } : blank); const [options, setOptions] = useState<Options>({}); const [error, setError] = useState(""); const [saving, setSaving] = useState(false); const [preview, setPreview] = useState<string[]>([]);
  useEffect(() => { if (!duplicateId || existing) return; void request<Schedule>("get", { id: duplicateId }).then(source => setSpec({ ...blank, ...source, name: `${source.name} (copy)`, state: "enabled" } as Spec)).catch(e => setError(e instanceof Error ? e.message : String(e))); }, [duplicateId, existing]);
  const set = <K extends keyof Spec>(key: K, value: Spec[K]) => setSpec(s => ({ ...s, [key]: value }));
  useEffect(() => { void request<Options>("options").then(setOptions).catch(e => setError(e instanceof Error ? e.message : String(e))); }, []);
  const submit = async (e: FormEvent) => { const submitter = (e.nativeEvent as SubmitEvent).submitter; const mode = submitter instanceof HTMLButtonElement ? submitter.value : "save"; e.preventDefault(); setSaving(true); setError(""); try { const savedSpec = mode === "paused" ? { ...spec, enabled: false } : spec; const schedule = existing ? await request<Schedule>("update", { id: existing.id, revision: existing.revision, spec: savedSpec }) : await request<Schedule>("create", { spec: savedSpec }); if (mode === "run_now") await request("run_now", { id: schedule.id }); navigate(`/tasks/${encodeURIComponent(schedule.id)}`); } catch (e) { setError(e instanceof Error ? e.message : String(e)); } finally { setSaving(false); } };
  const doPreview = async () => { try { setPreview(await request<string[]>("preview", { spec })); } catch (e) { setError(e instanceof Error ? e.message : String(e)); } };
  return <Shell><form onSubmit={submit} className="grid gap-4"><div className="flex flex-wrap items-center justify-between gap-3"><div><Link to={existing ? `/tasks/${existing.id}` : "/tasks"} className="text-sm text-muted-foreground hover:underline">← {t("tasks.back")}</Link><h2 className="mt-2 text-xl font-semibold">{existing ? t("tasks.edit") : t("tasks.new")}</h2></div><div className="flex gap-2"><Button type="button" variant="outline" onClick={() => void doPreview()}>{t("tasks.preview")}</Button><><Button type="submit" value="save" disabled={saving}>{saving ? t("tasks.saving") : t("tasks.save")}</Button><Button type="submit" value="run_now" variant="outline" disabled={saving}>{t("tasks.saveRunNow")}</Button><Button type="submit" value="paused" variant="outline" disabled={saving}>{t("tasks.savePaused")}</Button></></div></div>{error && <p role="alert" className="text-sm text-destructive">{error}</p>}<Card><CardHeader><CardTitle>{t("tasks.basics")}</CardTitle></CardHeader><CardContent className="grid gap-4 md:grid-cols-2"><Field label={t("tasks.name")}><Input required value={spec.name} onChange={e => set("name", e.target.value)} /></Field><Field label={t("tasks.kind")}><select className="h-9 rounded-md border border-input bg-transparent px-3" value={spec.kind} onChange={e => set("kind", e.target.value as Spec["kind"])}><option value="shell">shell</option><option value="agent">agent</option></select></Field><Field label={t("tasks.device")}><select className="h-9 rounded-md border border-input bg-transparent px-3" required disabled={Boolean(existing)} value={spec.device} onChange={e => set("device", e.target.value)}><option value="">{t("tasks.selectDevice")}</option>{(options.devices || []).map(d => <option key={d.id} value={d.id}>{d.name || d.id}{d.online === false ? ` (${t("tasks.offline")})` : ""}</option>)}</select></Field><Field label={t("tasks.timezone")}><Input value={spec.timezone} onChange={e => set("timezone", e.target.value)} /></Field><Field label={t("tasks.cron")}><><Input list="task-cron-presets" value={spec.cron} onChange={e => set("cron", e.target.value)} placeholder="*/15 * * * *" /><datalist id="task-cron-presets"><option value="*/5 * * * *">5m</option><option value="*/15 * * * *">15m</option><option value="*/30 * * * *">30m</option><option value="0 * * * *">1h</option><option value="0 0 * * *">1d</option></datalist></></Field><Field label={t("tasks.at")}><Input type="datetime-local" value={localDateInput(spec.at)} onChange={e => set("at", e.target.value ? new Date(e.target.value).toISOString() : "")} /></Field><Field label={t("tasks.cwd")}><Input value={spec.cwd} onChange={e => set("cwd", e.target.value)} /></Field><Field label={t("tasks.expires")}><Input type="datetime-local" value={localDateInput(spec.expiresAt)} onChange={e => set("expiresAt", e.target.value ? new Date(e.target.value).toISOString() : "")} /></Field></CardContent></Card><Card><CardHeader><CardTitle>{spec.kind === "shell" ? t("tasks.shell") : t("tasks.agent")}</CardTitle></CardHeader><CardContent className="grid gap-4"><Field label={spec.kind === "shell" ? t("tasks.command") : t("tasks.prompt")}><textarea className="min-h-28 rounded-md border border-input bg-transparent p-3 text-sm" required value={spec.kind === "shell" ? spec.command : spec.prompt} onChange={e => set(spec.kind === "shell" ? "command" : "prompt", e.target.value)} /></Field>{spec.kind === "agent" && <div className="grid gap-4 md:grid-cols-2"><Field label={t("tasks.sessionMode")}><select className="h-9 rounded-md border border-input bg-transparent px-3" value={spec.sessionMode} onChange={e => set("sessionMode", e.target.value as Spec["sessionMode"])}><option value="new">{t("tasks.newThread")}</option><option value="existing">{t("tasks.existingThread")}</option></select></Field>{spec.sessionMode === "new" ? <Field label={t("tasks.model")}><select className="h-9 rounded-md border border-input bg-transparent px-3" value={spec.model} onChange={e => set("model", e.target.value)}><option value="">{t("tasks.selectModel")}</option>{(options.models || []).map(m => <option key={m.id} value={m.id}>{m.name || m.id}</option>)}</select></Field> : <Field label={t("tasks.thread")}><select className="h-9 rounded-md border border-input bg-transparent px-3" value={spec.thread} onChange={e => set("thread", e.target.value)}><option value="">{t("tasks.selectThread")}</option>{(options.threads || []).filter(th => !spec.device || th.device === spec.device).map(th => <option key={th.id} value={th.id}>{th.title || th.id}</option>)}</select></Field>}<Field label={t("tasks.reasoning")}><Input value={spec.reasoning} onChange={e => set("reasoning", e.target.value)} /></Field><Field label={t("tasks.allowedTools")}><Input value={spec.allowedTools.join(", ")} onChange={e => set("allowedTools", e.target.value.split(",").map(x => x.trim()).filter(Boolean))} /></Field></div>}</CardContent></Card><Card><CardHeader><CardTitle>{t("tasks.execution")}</CardTitle></CardHeader><CardContent className="grid gap-4 md:grid-cols-3"><Field label={t("tasks.timeout")}><Input type="number" min={0} value={spec.timeoutSeconds} onChange={e => set("timeoutSeconds", Number(e.target.value))} /></Field><Field label={t("tasks.tokenBudget")}><Input type="number" min={0} value={spec.tokenBudget} onChange={e => set("tokenBudget", Number(e.target.value))} /></Field><Field label={t("tasks.maxRuns")}><Input type="number" min={0} value={spec.maxRuns} onChange={e => set("maxRuns", Number(e.target.value))} /></Field><Field label={t("tasks.busyPolicy")}><select className="h-9 rounded-md border border-input bg-transparent px-3" value={spec.busyPolicy} onChange={e => set("busyPolicy", e.target.value as Spec["busyPolicy"])}><option value="skip">skip</option><option value="wait">wait</option></select></Field><Field label={t("tasks.maxWait")}><Input type="number" min={0} value={spec.maxWaitSeconds} onChange={e => set("maxWaitSeconds", Number(e.target.value))} /></Field><Field label={t("tasks.mutex")}><Input value={spec.mutexGroup} onChange={e => set("mutexGroup", e.target.value)} /></Field><Field label={t("tasks.misfire")}><select className="h-9 rounded-md border border-input bg-transparent px-3" value={spec.misfirePolicy} onChange={e => set("misfirePolicy", e.target.value as Spec["misfirePolicy"])}><option value="skip">skip</option><option value="latest">latest</option></select></Field><Field label={t("tasks.grace")}><Input type="number" min={0} value={spec.graceSeconds} onChange={e => set("graceSeconds", Number(e.target.value))} /></Field></CardContent></Card><Card><CardContent className="grid gap-3 p-4"><label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={spec.enabled} onChange={e => set("enabled", e.target.checked)} /><span>{t("tasks.enabled")}</span></label><label className="flex items-start gap-2 text-sm"><input type="checkbox" checked={spec.authorized} onChange={e => set("authorized", e.target.checked)} /><span>{t("tasks.authorized")}</span></label></CardContent></Card>{preview.length > 0 && <Card><CardHeader><CardTitle>{t("tasks.preview")}</CardTitle></CardHeader><CardContent><ul className="list-disc pl-5 text-sm">{preview.map(x => <li key={x}>{fmt(x)}</li>)}</ul></CardContent></Card>}</form></Shell>;
}

export function TaskDetailPage() {
  const { t } = useI18n(); const { id = "" } = useParams(); const navigate = useNavigate(); const [schedule, setSchedule] = useState<Schedule | null>(null); const [runs, setRuns] = useState<Run[]>([]); const [error, setError] = useState(""); const load = useCallback(async () => { if (document.visibilityState !== "visible") return; setError(""); try { const [s, r] = await Promise.all([request<Schedule>("get", { id }), request<Run[]>("runs", { scheduleId: id })]); setSchedule(s); setRuns(r); } catch (e) { setError(e instanceof Error ? e.message : String(e)); } }, [id]); useEffect(() => { setSchedule(null); setRuns([]); void load(); const timer = window.setInterval(() => void load(), 3000); const visible = () => { if (document.visibilityState === "visible") void load(); }; document.addEventListener("visibilitychange", visible); return () => { window.clearInterval(timer); document.removeEventListener("visibilitychange", visible); }; }, [load]);
  const action = async (name: string) => { if (name === "delete" && !window.confirm(t("tasks.deleteConfirm"))) return; try { await request(name, { id }); if (name === "delete") navigate("/tasks"); else await load(); } catch (e) { setError(e instanceof Error ? e.message : String(e)); } };
  if (!schedule) return <Shell><p className="text-sm text-muted-foreground">{error || t("tasks.loading")}</p></Shell>;
  return <Shell><div className="mb-4 flex flex-wrap items-start justify-between gap-3"><div><Link to="/tasks" className="text-sm text-muted-foreground hover:underline">← {t("tasks.back")}</Link><h2 className="mt-2 text-xl font-semibold">{schedule.name}</h2><div className="mt-1 flex gap-2"><Status state={schedule.state} /><span className="text-xs text-muted-foreground">{schedule.kind} · {schedule.device}</span></div></div><div className="flex flex-wrap gap-2"><Button variant="outline" onClick={() => navigate(`/tasks/${encodeURIComponent(id)}/edit`)}>{t("tasks.edit")}</Button><Button variant="outline" onClick={() => navigate(`/tasks/new?duplicate=${encodeURIComponent(id)}`)}>{t("tasks.duplicate")}</Button><Button variant="outline" onClick={() => void action(schedule.state === "paused" ? "enable" : "pause")}>{schedule.state === "paused" ? t("tasks.enable") : t("tasks.pause")}</Button><Button variant="outline" onClick={() => void action("run_now")}>{t("tasks.runNow")}</Button><Button variant="destructive" onClick={() => void action("delete")}>{t("tasks.delete")}</Button></div></div>{error && <p role="alert" className="mb-3 text-sm text-destructive">{error}</p>}<Tabs defaultValue="overview"><TabsList><TabsTrigger value="overview">{t("tasks.overview")}</TabsTrigger><TabsTrigger value="history">{t("tasks.history")}</TabsTrigger><TabsTrigger value="changes">{t("tasks.changes")}</TabsTrigger></TabsList><TabsContent value="overview"><Card><CardContent className="grid gap-3 p-4 text-sm md:grid-cols-2"><div><b>{t("tasks.schedule")}</b><p>{schedule.cron || schedule.at || "—"}</p></div><div><b>{t("tasks.nextRun")}</b><p>{fmt(schedule.nextRunAt)}</p></div><div><b>{t("tasks.created")}</b><p>{fmt(schedule.createdAt)}</p></div><div><b>{t("tasks.updated")}</b><p>{fmt(schedule.updatedAt)}</p></div><div><b>{t("tasks.authorization")}</b><p>{schedule.authorized ? t("tasks.authorizedYes") : t("tasks.authorizedNo")}</p></div><div><b>{t("tasks.revision")}</b><p>{schedule.revision}</p></div></CardContent></Card><p className="mt-3 text-xs text-muted-foreground">{t("tasks.deleteNote")}</p><h3 className="mb-2 mt-5 font-semibold">{t("tasks.recentRuns")}</h3><RunList runs={[...runs].sort((a, b) => new Date(b.finishedAt || b.startedAt || b.scheduledAt || 0).valueOf() - new Date(a.finishedAt || a.startedAt || a.scheduledAt || 0).valueOf()).slice(0, 5)} /></TabsContent><TabsContent value="history"><RunList runs={runs} /></TabsContent><TabsContent value="changes"><Card><CardContent className="p-0"><div className="divide-y divide-border">{(schedule.events || []).length === 0 ? <p className="p-4 text-sm text-muted-foreground">{t("tasks.noChanges")}</p> : schedule.events?.map((e, i) => <div key={`${e.at}-${i}`} className="p-4 text-sm"><div className="flex justify-between gap-3"><b>{e.action}</b><span className="text-xs text-muted-foreground">{fmt(e.at)}</span></div><p className="text-xs text-muted-foreground">{e.source} · rev {e.revision}{e.detail ? ` · ${e.detail}` : ""}</p></div>)}</div></CardContent></Card></TabsContent></Tabs></Shell>;
}
function RunList({ runs }: { runs: Run[] }) { const { t } = useI18n(); return <Card><CardContent className="p-0">{runs.length === 0 ? <p className="p-4 text-sm text-muted-foreground">{t("tasks.emptyRuns")}</p> : <div className="divide-y divide-border">{runs.map(r => <Link to={`/tasks/runs/${encodeURIComponent(r.id)}`} key={r.id} className="flex flex-wrap items-center justify-between gap-3 p-4 hover:bg-state-hover"><div><b>{fmt(r.startedAt || r.scheduledAt)}</b><p className="text-xs text-muted-foreground">{r.reason || r.source}</p></div><Status state={r.state} /></Link>)}</div>}</CardContent></Card>; }

async function loadLogTail(runId: string): Promise<{ output: string; nextOffset: number }> {
  let offset = 0;
  let output = "";
  for (let page = 0; page < 8 && output.length < 262144; page++) {
    const logs = await request<{ output: string; nextOffset: number; reset?: boolean }>("logs", { id: runId, offset, limit: 65536 });
    if (logs.reset) output = "";
    output += logs.output || "";
    if (!logs.output || logs.nextOffset <= offset) return { output: output.slice(-262144), nextOffset: logs.nextOffset };
    offset = logs.nextOffset;
  }
  return { output: output.slice(-262144), nextOffset: offset };
}

export function TaskRunPage() {
  const { t } = useI18n(); const { id = "" } = useParams(); const [run, setRun] = useState<Run | null>(null); const [output, setOutput] = useState(""); const [offset, setOffset] = useState(0); const [search, setSearch] = useState(""); const [follow, setFollow] = useState(true); const [error, setError] = useState("");
  const load = useCallback(async () => { try { const r = await request<Run>("run", { id }); setRun(r); const logs = await loadLogTail(id); setOutput(logs.output); setOffset(logs.nextOffset); } catch (e) { setError(e instanceof Error ? e.message : String(e)); } }, [id]);
  useEffect(() => { void load(); const timer = window.setInterval(() => { if (follow) void load(); }, 3000); return () => window.clearInterval(timer); }, [load, follow]);
  const cancel = async () => { try { await request("cancel", { id }); await load(); } catch (e) { setError(e instanceof Error ? e.message : String(e)); } };
  const shown = search ? output.split("\n").filter(line => line.toLowerCase().includes(search.toLowerCase())).join("\n") : output;
  if (!run) return <Shell><p className="text-sm text-muted-foreground">{error || t("tasks.loading")}</p></Shell>;
  return <Shell><div className="mb-4 flex flex-wrap items-start justify-between gap-3"><div><Link to={`/tasks/${encodeURIComponent(run.scheduleId)}`} className="text-sm text-muted-foreground hover:underline">← {t("tasks.backToTask")}</Link><h2 className="mt-2 text-xl font-semibold">{run.name}</h2><div className="mt-1 flex items-center gap-2"><Status state={run.state} /><span className="text-xs text-muted-foreground">{fmt(run.startedAt || run.scheduledAt)}</span></div></div><div className="flex gap-2"><label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={follow} onChange={e => setFollow(e.target.checked)} />{t("tasks.follow")}</label>{["pending", "dispatching", "running"].includes(run.state) && <Button variant="destructive" onClick={() => void cancel()}>{t("tasks.cancel")}</Button>}</div></div>{error && <p role="alert" className="mb-3 text-sm text-destructive">{error}</p>}<Card className="mb-4"><CardContent className="grid gap-2 p-4 text-sm md:grid-cols-3"><div><b>{t("tasks.scheduled")}</b><p>{fmt(run.scheduledAt)}</p></div><div><b>{t("tasks.started")}</b><p>{fmt(run.startedAt)}</p></div><div><b>{t("tasks.finished")}</b><p>{fmt(run.finishedAt)}</p></div>{run.thread && <div><b>{t("tasks.thread")}</b><p><Link className="underline" to={`/agent?device=${encodeURIComponent(run.device)}&thread=${encodeURIComponent(run.thread)}&turn=${encodeURIComponent(run.turn || run.id)}`}>{run.thread}</Link></p></div>}</CardContent></Card><div className="mb-2 flex flex-wrap gap-2"><Input value={search} onChange={e => setSearch(e.target.value)} placeholder={t("tasks.searchLogs")} /><Button variant="outline" onClick={() => void navigator.clipboard?.writeText(output)}>{t("tasks.copy")}</Button><Button variant="outline" onClick={() => { const blob = new Blob([output], { type: "text/plain" }); const url = URL.createObjectURL(blob); const a = document.createElement("a"); a.href = url; a.download = `${run.name || run.id}.log`; a.click(); URL.revokeObjectURL(url); }}>{t("tasks.download")}</Button></div><pre className="max-h-[60vh] min-h-48 overflow-auto rounded-lg border border-border bg-surface-recessed-solid p-4 font-mono text-xs leading-relaxed whitespace-pre-wrap break-words">{shown || t("tasks.noLogs")}</pre><p className="mt-2 text-xs text-muted-foreground">{t("tasks.logOffset")} {offset}{run.reason ? ` · ${run.reason}` : ""}</p></Shell>;
}

export function NewTaskPage() { const [params] = useSearchParams(); return <TaskForm duplicateId={params.get("duplicate") || undefined} />; }
export function EditTaskPage() { const { id = "" } = useParams(); const [schedule, setSchedule] = useState<Schedule>(); const [error, setError] = useState(""); useEffect(() => { void request<Schedule>("get", { id }).then(setSchedule).catch(e => setError(e instanceof Error ? e.message : String(e))); }, [id]); return schedule ? <TaskForm existing={schedule} /> : <Shell><p className="text-sm text-muted-foreground">{error || "Loading…"}</p></Shell>; }
