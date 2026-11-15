import { useMemo, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { useI18n } from "../lib/i18n";
import { request, loadLogTail } from "./tasks/api";
import { useTaskResource } from "./tasks/useTaskResource";
import { Button } from "../components/button";
import { Input } from "../components/input";
import { Card, CardContent, CardHeader, CardTitle } from "../components/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../components/tabs";
import { Icon } from "../components/icon";
import { fmt, Status, Shell } from "./TasksShared";
import { TaskForm } from "./TaskForm";
import type { Schedule, Run } from "./tasks/types";

export function TasksPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const [tab, setTab] = useState("schedules");
  const [filter, setFilter] = useState("");
  const { data, loading, error, refresh: load } = useTaskResource("task-list", async signal => {
    const [items, runs] = await Promise.all([request<Schedule[]>("list", {}, signal), request<Run[]>("runs", {}, signal)]);
    return { items, runs };
  });
  const items = data?.items ?? [];
  const runs = data?.runs ?? [];
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
      {error && <p role="alert" className="p-3 text-sm text-destructive">{error}</p>}
      <TabsContent value="schedules"><Card><CardContent className="p-0">{loading ? <p className="p-6 text-sm text-muted-foreground">{error || t("tasks.loading")}</p> : shown.length === 0 ? <p className="p-6 text-sm text-muted-foreground">{t("tasks.empty")}</p> : <div className="divide-y divide-border">{shown.map(schedule => {
        const history = [...(runsBySchedule.get(schedule.id) || [])].sort((a, b) => new Date(b.finishedAt || b.startedAt || b.scheduledAt || 0).valueOf() - new Date(a.finishedAt || a.startedAt || a.scheduledAt || 0).valueOf());
        const latest = history[0];
        const active = history.find(run => ["pending", "dispatching", "running"].includes(run.state));
        return <div key={schedule.id} className="flex flex-wrap items-center justify-between gap-3 p-4"><div className="min-w-0"><Link to={`/tasks/${encodeURIComponent(schedule.id)}`} className="font-medium hover:underline">{schedule.name || schedule.id}</Link><p className="text-xs text-muted-foreground">{schedule.kind} · {schedule.device || "—"} · {schedule.cron || schedule.at || "—"}</p><p className="mt-1 text-xs text-muted-foreground">{active ? <Link className="underline" to={`/tasks/runs/${encodeURIComponent(active.id)}`}>{t("tasks.currentRun")}: {t(`tasks.status.${active.state}`)}</Link> : latest ? <Link className="underline" to={`/tasks/runs/${encodeURIComponent(latest.id)}`}>{t("tasks.lastResult")}: {t(`tasks.status.${latest.state}`)} · {fmt(latest.finishedAt || latest.startedAt || latest.scheduledAt)}</Link> : t("tasks.noRunsYet")}</p></div><div className="flex items-center gap-2"><Status state={schedule.state} /><span className="text-xs text-muted-foreground">{schedule.nextRunAt ? fmt(schedule.nextRunAt) : "—"}</span></div></div>;
      })}</div>}</CardContent></Card></TabsContent>
      <TabsContent value="runs"><Card><CardContent className="p-0">{loading ? <p className="p-6 text-sm text-muted-foreground">{error || t("tasks.loading")}</p> : filteredRuns.length === 0 ? <p className="p-6 text-sm text-muted-foreground">{t("tasks.emptyRuns")}</p> : <div className="divide-y divide-border">{filteredRuns.map(run => <div key={run.id} className="flex flex-wrap items-center justify-between gap-3 p-4"><div className="min-w-0"><Link to={`/tasks/runs/${encodeURIComponent(run.id)}`} className="font-medium hover:underline">{run.name || run.id}</Link><p className="text-xs text-muted-foreground">{fmt(run.startedAt || run.scheduledAt)} · <Link className="underline" to={`/tasks/${encodeURIComponent(run.scheduleId)}`}>{run.scheduleId}</Link></p></div><Status state={run.state} /></div>)}</div>}</CardContent></Card></TabsContent>
    </Tabs>
  </Shell>;
}


export function TaskDetailPage() {
  const { t } = useI18n();
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const { data, error, setError, refresh: load } = useTaskResource(`task:${id}`, async signal => {
    const [schedule, runs] = await Promise.all([request<Schedule>("get", { id }, signal), request<Run[]>("runs", { scheduleId: id }, signal)]);
    return { schedule, runs };
  });
  const schedule = data?.schedule;
  const runs = data?.runs ?? [];
  const action = async (name: string) => { if (name === "delete" && !window.confirm(t("tasks.deleteConfirm"))) return; try { await request(name, { id }); if (name === "delete") navigate("/tasks"); else await load(); } catch (e) { setError(e instanceof Error ? e.message : String(e)); } };
  if (!schedule) return <Shell><p className="text-sm text-muted-foreground">{error || t("tasks.loading")}</p></Shell>;
  return <Shell><div className="mb-4 flex flex-wrap items-start justify-between gap-3"><div><Link to="/tasks" className="text-sm text-muted-foreground hover:underline">← {t("tasks.back")}</Link><h2 className="mt-2 text-xl font-semibold">{schedule.name}</h2><div className="mt-1 flex gap-2"><Status state={schedule.state} /><span className="text-xs text-muted-foreground">{schedule.kind} · {schedule.device}</span></div></div><div className="flex flex-wrap gap-2"><Button variant="outline" onClick={() => navigate(`/tasks/${encodeURIComponent(id)}/edit`)}>{t("tasks.edit")}</Button><Button variant="outline" onClick={() => navigate(`/tasks/new?duplicate=${encodeURIComponent(id)}`)}>{t("tasks.duplicate")}</Button><Button variant="outline" onClick={() => void action(schedule.state === "paused" ? "enable" : "pause")}>{schedule.state === "paused" ? t("tasks.enable") : t("tasks.pause")}</Button><Button variant="outline" onClick={() => void action("run_now")}>{t("tasks.runNow")}</Button><Button variant="destructive" onClick={() => void action("delete")}>{t("tasks.delete")}</Button></div></div>{error && <p role="alert" className="mb-3 text-sm text-destructive">{error}</p>}<Tabs defaultValue="overview"><TabsList><TabsTrigger value="overview">{t("tasks.overview")}</TabsTrigger><TabsTrigger value="history">{t("tasks.history")}</TabsTrigger><TabsTrigger value="changes">{t("tasks.changes")}</TabsTrigger></TabsList><TabsContent value="overview"><Card><CardContent className="grid gap-3 p-4 text-sm md:grid-cols-2"><div><b>{t("tasks.schedule")}</b><p>{schedule.cron || schedule.at || "—"}</p></div><div><b>{t("tasks.nextRun")}</b><p>{fmt(schedule.nextRunAt)}</p></div><div><b>{t("tasks.created")}</b><p>{fmt(schedule.createdAt)}</p></div><div><b>{t("tasks.updated")}</b><p>{fmt(schedule.updatedAt)}</p></div><div><b>{t("tasks.authorization")}</b><p>{schedule.authorized ? t("tasks.authorizedYes") : t("tasks.authorizedNo")}</p></div><div><b>{t("tasks.revision")}</b><p>{schedule.revision}</p></div></CardContent></Card><p className="mt-3 text-xs text-muted-foreground">{t("tasks.deleteNote")}</p><h3 className="mb-2 mt-5 font-semibold">{t("tasks.recentRuns")}</h3><RunList runs={[...runs].sort((a, b) => new Date(b.finishedAt || b.startedAt || b.scheduledAt || 0).valueOf() - new Date(a.finishedAt || a.startedAt || a.scheduledAt || 0).valueOf()).slice(0, 5)} /></TabsContent><TabsContent value="history"><RunList runs={runs} /></TabsContent><TabsContent value="changes"><Card><CardContent className="p-0"><div className="divide-y divide-border">{(schedule.events || []).length === 0 ? <p className="p-4 text-sm text-muted-foreground">{t("tasks.noChanges")}</p> : schedule.events?.map((e, i) => <div key={`${e.at}-${i}`} className="p-4 text-sm"><div className="flex justify-between gap-3"><b>{e.action}</b><span className="text-xs text-muted-foreground">{fmt(e.at)}</span></div><p className="text-xs text-muted-foreground">{e.source} · rev {e.revision}{e.detail ? ` · ${e.detail}` : ""}</p></div>)}</div></CardContent></Card></TabsContent></Tabs></Shell>;
}
function RunList({ runs }: { runs: Run[] }) { const { t } = useI18n(); return <Card><CardContent className="p-0">{runs.length === 0 ? <p className="p-4 text-sm text-muted-foreground">{t("tasks.emptyRuns")}</p> : <div className="divide-y divide-border">{runs.map(r => <Link to={`/tasks/runs/${encodeURIComponent(r.id)}`} key={r.id} className="flex flex-wrap items-center justify-between gap-3 p-4 hover:bg-state-hover"><div><b>{fmt(r.startedAt || r.scheduledAt)}</b><p className="text-xs text-muted-foreground">{r.reason || r.source}</p></div><Status state={r.state} /></Link>)}</div>}</CardContent></Card>; }


export function TaskRunPage() {
  const { t } = useI18n();
  const { id = "" } = useParams();
  const [search, setSearch] = useState("");
  const [follow, setFollow] = useState(true);
  const { data, error, setError, refresh: load } = useTaskResource(`run:${id}`, async signal => {
    const [run, logs] = await Promise.all([request<Run>("run", { id }, signal), loadLogTail(id, signal)]);
    return { run, ...logs };
  }, follow);
  const run = data?.run;
  const output = data?.output ?? "";
  const offset = data?.nextOffset ?? 0;
  const cancel = async () => { try { await request("cancel", { id }); await load(); } catch (e) { setError(e instanceof Error ? e.message : String(e)); } };
  const shown = search ? output.split("\n").filter(line => line.toLowerCase().includes(search.toLowerCase())).join("\n") : output;
  if (!run) return <Shell><p className="text-sm text-muted-foreground">{error || t("tasks.loading")}</p></Shell>;
  return <Shell><div className="mb-4 flex flex-wrap items-start justify-between gap-3"><div><Link to={`/tasks/${encodeURIComponent(run.scheduleId)}`} className="text-sm text-muted-foreground hover:underline">← {t("tasks.backToTask")}</Link><h2 className="mt-2 text-xl font-semibold">{run.name}</h2><div className="mt-1 flex items-center gap-2"><Status state={run.state} /><span className="text-xs text-muted-foreground">{fmt(run.startedAt || run.scheduledAt)}</span></div></div><div className="flex gap-2"><label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={follow} onChange={e => setFollow(e.target.checked)} />{t("tasks.follow")}</label>{["pending", "dispatching", "running"].includes(run.state) && <Button variant="destructive" onClick={() => void cancel()}>{t("tasks.cancel")}</Button>}</div></div>{error && <p role="alert" className="mb-3 text-sm text-destructive">{error}</p>}<Card className="mb-4"><CardContent className="grid gap-2 p-4 text-sm md:grid-cols-3"><div><b>{t("tasks.scheduled")}</b><p>{fmt(run.scheduledAt)}</p></div><div><b>{t("tasks.started")}</b><p>{fmt(run.startedAt)}</p></div><div><b>{t("tasks.finished")}</b><p>{fmt(run.finishedAt)}</p></div>{run.thread && <div><b>{t("tasks.thread")}</b><p><Link className="underline" to={`/agent?device=${encodeURIComponent(run.device)}&thread=${encodeURIComponent(run.thread)}&turn=${encodeURIComponent(run.turn || run.id)}`}>{run.thread}</Link></p></div>}</CardContent></Card><div className="mb-2 flex flex-wrap gap-2"><Input value={search} onChange={e => setSearch(e.target.value)} placeholder={t("tasks.searchLogs")} /><Button variant="outline" onClick={() => void navigator.clipboard?.writeText(output)}>{t("tasks.copy")}</Button><Button variant="outline" onClick={() => { const blob = new Blob([output], { type: "text/plain" }); const url = URL.createObjectURL(blob); const a = document.createElement("a"); a.href = url; a.download = `${run.name || run.id}.log`; a.click(); URL.revokeObjectURL(url); }}>{t("tasks.download")}</Button></div><pre className="max-h-[60vh] min-h-48 overflow-auto rounded-lg border border-border bg-surface-recessed-solid p-4 font-mono text-xs leading-relaxed whitespace-pre-wrap break-words">{shown || t("tasks.noLogs")}</pre><p className="mt-2 text-xs text-muted-foreground">{t("tasks.logOffset")} {offset}{run.reason ? ` · ${run.reason}` : ""}</p></Shell>;
}

export function NewTaskPage() { const [params] = useSearchParams(); const duplicateId = params.get("duplicate") || undefined; return <TaskForm key={duplicateId || "new"} duplicateId={duplicateId} />; }
export function EditTaskPage() {
  const { id = "" } = useParams();
  const { t } = useI18n();
  const { data: schedule, error } = useTaskResource(`edit:${id}`, signal => request<Schedule>("get", { id }, signal), false);
  return schedule ? <TaskForm key={id} existing={schedule} /> : <Shell><p className="text-sm text-muted-foreground">{error || t("tasks.loading")}</p></Shell>;
}
