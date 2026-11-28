import { useMemo, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { useI18n } from "../lib/i18n";
import { useCopy } from "../lib/clipboard";
import { cn } from "../lib/utils";
import { request, loadLogTail } from "./tasks/api";
import { useTaskResource } from "./tasks/useTaskResource";
import { Button } from "../components/button";
import { Input } from "../components/input";
import { Switch } from "../components/switch";
import { Card, CardContent } from "../components/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../components/tabs";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "../components/alert-dialog";
import { SecondaryPanelContentSkeleton } from "../components/loading-skeletons";
import { Icon } from "../components/icon";
import { fmt, Status, Shell } from "./TasksShared";
import { TaskForm } from "./TaskForm";
import type { Schedule, Run } from "./tasks/types";

export function TasksPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const [tab, setTab] = useState("schedules");
  const [filter, setFilter] = useState("");
  const { data, loading, error, refresh: load } = useTaskResource(
    "task-list",
    async (signal) => {
      const [items, runs] = await Promise.all([
        request<Schedule[]>("list", {}, signal),
        request<Run[]>("runs", {}, signal),
      ]);
      return { items, runs };
    },
  );
  const items = data?.items ?? [];
  const runs = data?.runs ?? [];
  const shown = useMemo(() => {
    const q = filter.toLowerCase();
    return items.filter(
      (x) =>
        !q ||
        [x.name, x.kind, x.device, x.state].some((v) =>
          v?.toLowerCase().includes(q),
        ),
    );
  }, [items, filter]);
  const filteredRuns = useMemo(
    () =>
      runs.filter(
        (r) =>
          !filter ||
          [r.name, r.state, r.device].some((v) =>
            v?.toLowerCase().includes(filter.toLowerCase()),
          ),
      ),
    [runs, filter],
  );
  const runsBySchedule = useMemo(() => {
    const grouped = new Map<string, Run[]>();
    for (const run of runs) {
      grouped.set(run.scheduleId, [...(grouped.get(run.scheduleId) || []), run]);
    }
    return grouped;
  }, [runs]);

  return (
    <Shell>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">{t("tasks.subtitle")}</p>
        <Button onClick={() => navigate("/tasks/new")}>
          <Icon name="Plus" />
          {t("tasks.new")}
        </Button>
      </div>
      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="schedules">{t("tasks.schedules")}</TabsTrigger>
          <TabsTrigger value="runs">{t("tasks.runs")}</TabsTrigger>
        </TabsList>
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <Input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder={t("tasks.filter")}
            aria-label={t("tasks.filter")}
            className="min-w-[180px] flex-1"
          />
          <Button variant="outline" onClick={() => void load()}>
            <Icon name="Refresh" />
            {t("tasks.refresh")}
          </Button>
        </div>
        {error && (
          <div
            role="alert"
            className="mt-3 rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive-text"
          >
            {error}
          </div>
        )}
        <TabsContent value="schedules">
          <Card>
            <CardContent className="p-0">
              {loading && !data ? (
                <SecondaryPanelContentSkeleton label={t("tasks.loading")} />
              ) : shown.length === 0 ? (
                <div className="flex flex-col items-center gap-2 p-8 text-center text-muted-foreground">
                  <Icon name="TimeSchedule" className="size-8 opacity-30" />
                  <p className="text-sm">{t("tasks.empty")}</p>
                </div>
              ) : (
                <div className="divide-y divide-border">
                  {shown.map((schedule) => {
                    const history = [
                      ...(runsBySchedule.get(schedule.id) || []),
                    ].sort(
                      (a, b) =>
                        new Date(
                          b.finishedAt || b.startedAt || b.scheduledAt || 0,
                        ).valueOf() -
                        new Date(
                          a.finishedAt || a.startedAt || a.scheduledAt || 0,
                        ).valueOf(),
                    );
                    const latest = history[0];
                    const active = history.find((run) =>
                      ["pending", "dispatching", "running"].includes(run.state),
                    );
                    return (
                      <div
                        key={schedule.id}
                        className="flex flex-wrap items-center justify-between gap-3 p-4 transition-colors hover:bg-state-hover"
                      >
                        <div className="min-w-0 flex-1">
                          <Link
                            to={`/tasks/${encodeURIComponent(schedule.id)}`}
                            className="font-medium hover:underline"
                          >
                            {schedule.name || schedule.id}
                          </Link>
                          <p className="text-xs text-muted-foreground">
                            {schedule.kind} · {schedule.device || "—"} ·{" "}
                            <span className="font-mono">
                              {schedule.cron || schedule.at || "—"}
                            </span>
                          </p>
                          <p className="mt-1 text-xs text-muted-foreground">
                            {active ? (
                              <Link
                                className="underline underline-offset-2"
                                to={`/tasks/runs/${encodeURIComponent(active.id)}`}
                              >
                                {t("tasks.currentRun")}:{" "}
                                {t(`tasks.status.${active.state}`)}
                              </Link>
                            ) : latest ? (
                              <Link
                                className="underline underline-offset-2"
                                to={`/tasks/runs/${encodeURIComponent(latest.id)}`}
                              >
                                {t("tasks.lastResult")}:{" "}
                                {t(`tasks.status.${latest.state}`)} ·{" "}
                                {fmt(
                                  latest.finishedAt ||
                                    latest.startedAt ||
                                    latest.scheduledAt,
                                )}
                              </Link>
                            ) : (
                              t("tasks.noRunsYet")
                            )}
                          </p>
                        </div>
                        <div className="flex items-center gap-2">
                          <Status state={schedule.state} />
                          <span className="text-xs tabular-nums text-muted-foreground">
                            {schedule.nextRunAt ? fmt(schedule.nextRunAt) : "—"}
                          </span>
                        </div>
                      </div>
                    );
                  })}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>
        <TabsContent value="runs">
          <Card>
            <CardContent className="p-0">
              {loading && !data ? (
                <SecondaryPanelContentSkeleton label={t("tasks.loading")} />
              ) : filteredRuns.length === 0 ? (
                <div className="flex flex-col items-center gap-2 p-8 text-center text-muted-foreground">
                  <Icon name="Inbox" className="size-8 opacity-30" />
                  <p className="text-sm">{t("tasks.emptyRuns")}</p>
                </div>
              ) : (
                <div className="divide-y divide-border">
                  {filteredRuns.map((run) => (
                    <div
                      key={run.id}
                      className="flex flex-wrap items-center justify-between gap-3 p-4 transition-colors hover:bg-state-hover"
                    >
                      <div className="min-w-0 flex-1">
                        <Link
                          to={`/tasks/runs/${encodeURIComponent(run.id)}`}
                          className="font-medium hover:underline"
                        >
                          {run.name || run.id}
                        </Link>
                        <p className="text-xs text-muted-foreground">
                          <span className="tabular-nums">
                            {fmt(run.startedAt || run.scheduledAt)}
                          </span>{" "}
                          ·{" "}
                          <Link
                            className="underline underline-offset-2"
                            to={`/tasks/${encodeURIComponent(run.scheduleId)}`}
                          >
                            {run.scheduleId}
                          </Link>
                        </p>
                      </div>
                      <Status state={run.state} />
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </Shell>
  );
}

export function TaskDetailPage() {
  const { t } = useI18n();
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [actionBusy, setActionBusy] = useState(false);
  const { data, error, setError, refresh: load } = useTaskResource(
    `task:${id}`,
    async (signal) => {
      const [schedule, runs] = await Promise.all([
        request<Schedule>("get", { id }, signal),
        request<Run[]>("runs", { scheduleId: id }, signal),
      ]);
      return { schedule, runs };
    },
  );
  const schedule = data?.schedule;
  const runs = data?.runs ?? [];

  const action = async (name: string) => {
    if (actionBusy) return;
    setActionBusy(true);
    setError("");
    try {
      await request(name, { id });
      if (name === "delete") {
        setDeleteOpen(false);
        navigate("/tasks");
      } else {
        await load();
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setActionBusy(false);
    }
  };

  if (!schedule) {
    return (
      <Shell>
        {error ? (
          <p role="alert" className="text-sm text-destructive-text">
            {error}
          </p>
        ) : (
          <Card>
            <CardContent className="p-0">
              <SecondaryPanelContentSkeleton label={t("tasks.loading")} />
            </CardContent>
          </Card>
        )}
      </Shell>
    );
  }

  return (
    <Shell>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <Link
            to="/tasks"
            className="text-sm text-muted-foreground hover:underline"
          >
            ← {t("tasks.back")}
          </Link>
          <h2 className="mt-2 break-words text-xl font-semibold">
            {schedule.name}
          </h2>
          <div className="mt-1 flex flex-wrap items-center gap-2">
            <Status state={schedule.state} />
            <span className="text-xs text-muted-foreground">
              {schedule.kind} · {schedule.device}
            </span>
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            disabled={actionBusy}
            onClick={() => navigate(`/tasks/${encodeURIComponent(id)}/edit`)}
          >
            {t("tasks.edit")}
          </Button>
          <Button
            variant="outline"
            disabled={actionBusy}
            onClick={() =>
              navigate(`/tasks/new?duplicate=${encodeURIComponent(id)}`)
            }
          >
            {t("tasks.duplicate")}
          </Button>
          <Button
            variant="outline"
            disabled={actionBusy}
            onClick={() =>
              void action(schedule.state === "paused" ? "enable" : "pause")
            }
          >
            {schedule.state === "paused" ? t("tasks.enable") : t("tasks.pause")}
          </Button>
          <Button
            variant="outline"
            disabled={actionBusy}
            onClick={() => void action("run_now")}
          >
            {t("tasks.runNow")}
          </Button>
          <Button
            variant="destructive"
            disabled={actionBusy}
            onClick={() => setDeleteOpen(true)}
          >
            {t("tasks.delete")}
          </Button>
        </div>
      </div>
      {error && (
        <div
          role="alert"
          className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive-text"
        >
          {error}
        </div>
      )}
      <Tabs defaultValue="overview">
        <TabsList>
          <TabsTrigger value="overview">{t("tasks.overview")}</TabsTrigger>
          <TabsTrigger value="history">{t("tasks.history")}</TabsTrigger>
          <TabsTrigger value="changes">{t("tasks.changes")}</TabsTrigger>
        </TabsList>
        <TabsContent value="overview">
          <Card>
            <CardContent className="grid gap-3 p-4 text-sm md:grid-cols-2">
              <div>
                <b className="font-medium text-muted-foreground">
                  {t("tasks.schedule")}
                </b>
                <p className="mt-0.5 font-mono">
                  {schedule.cron || schedule.at || "—"}
                </p>
              </div>
              <div>
                <b className="font-medium text-muted-foreground">
                  {t("tasks.nextRun")}
                </b>
                <p className="mt-0.5 tabular-nums">{fmt(schedule.nextRunAt)}</p>
              </div>
              <div>
                <b className="font-medium text-muted-foreground">
                  {t("tasks.created")}
                </b>
                <p className="mt-0.5 tabular-nums">{fmt(schedule.createdAt)}</p>
              </div>
              <div>
                <b className="font-medium text-muted-foreground">
                  {t("tasks.updated")}
                </b>
                <p className="mt-0.5 tabular-nums">{fmt(schedule.updatedAt)}</p>
              </div>
              <div>
                <b className="font-medium text-muted-foreground">
                  {t("tasks.authorization")}
                </b>
                <p className="mt-0.5">
                  {schedule.authorized
                    ? t("tasks.authorizedYes")
                    : t("tasks.authorizedNo")}
                </p>
              </div>
              <div>
                <b className="font-medium text-muted-foreground">
                  {t("tasks.revision")}
                </b>
                <p className="mt-0.5 tabular-nums">{schedule.revision}</p>
              </div>
            </CardContent>
          </Card>
          <p className="mt-3 text-xs text-muted-foreground">
            {t("tasks.deleteNote")}
          </p>
          <h3 className="mb-2 mt-5 text-sm font-semibold">
            {t("tasks.recentRuns")}
          </h3>
          <RunList
            runs={[...runs]
              .sort(
                (a, b) =>
                  new Date(
                    b.finishedAt || b.startedAt || b.scheduledAt || 0,
                  ).valueOf() -
                  new Date(
                    a.finishedAt || a.startedAt || a.scheduledAt || 0,
                  ).valueOf(),
              )
              .slice(0, 5)}
          />
        </TabsContent>
        <TabsContent value="history">
          <RunList runs={runs} />
        </TabsContent>
        <TabsContent value="changes">
          <Card>
            <CardContent className="p-0">
              <div className="divide-y divide-border">
                {(schedule.events || []).length === 0 ? (
                  <p className="p-4 text-sm text-muted-foreground">
                    {t("tasks.noChanges")}
                  </p>
                ) : (
                  schedule.events?.map((e, i) => (
                    <div key={`${e.at}-${i}`} className="p-4 text-sm">
                      <div className="flex justify-between gap-3">
                        <b className="font-medium">{e.action}</b>
                        <span className="text-xs tabular-nums text-muted-foreground">
                          {fmt(e.at)}
                        </span>
                      </div>
                      <p className="text-xs text-muted-foreground">
                        {e.source} · rev {e.revision}
                        {e.detail ? ` · ${e.detail}` : ""}
                      </p>
                    </div>
                  ))
                )}
              </div>
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      <AlertDialog
        open={deleteOpen}
        onOpenChange={(open) => {
          if (!open && !actionBusy) setDeleteOpen(false);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("tasks.delete")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("tasks.deleteConfirm")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={actionBusy}>
              {t("common.cancel")}
            </AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              disabled={actionBusy}
              onClick={(event) => {
                event.preventDefault();
                void action("delete");
              }}
            >
              {t("tasks.delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Shell>
  );
}

function RunList({ runs }: { runs: Run[] }) {
  const { t } = useI18n();
  return (
    <Card>
      <CardContent className="p-0">
        {runs.length === 0 ? (
          <p className="p-4 text-sm text-muted-foreground">
            {t("tasks.emptyRuns")}
          </p>
        ) : (
          <div className="divide-y divide-border">
            {runs.map((r) => (
              <Link
                to={`/tasks/runs/${encodeURIComponent(r.id)}`}
                key={r.id}
                className="flex flex-wrap items-center justify-between gap-3 p-4 transition-colors hover:bg-state-hover"
              >
                <div className="min-w-0">
                  <b className="font-medium tabular-nums">
                    {fmt(r.startedAt || r.scheduledAt)}
                  </b>
                  <p className="text-xs text-muted-foreground">
                    {r.reason || r.source}
                  </p>
                </div>
                <Status state={r.state} />
              </Link>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

export function TaskRunPage() {
  const { t } = useI18n();
  const { id = "" } = useParams();
  const [search, setSearch] = useState("");
  const [follow, setFollow] = useState(true);
  const [canceling, setCanceling] = useState(false);
  const [copiedId, copy] = useCopy();
  const { data, error, setError, refresh: load } = useTaskResource(
    `run:${id}`,
    async (signal) => {
      const [run, logs] = await Promise.all([
        request<Run>("run", { id }, signal),
        loadLogTail(id, signal),
      ]);
      return { run, ...logs };
    },
    follow,
  );
  const run = data?.run;
  const output = data?.output ?? "";
  const offset = data?.nextOffset ?? 0;

  const cancel = async () => {
    if (canceling) return;
    setCanceling(true);
    try {
      await request("cancel", { id });
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setCanceling(false);
    }
  };

  const shown = search
    ? output
        .split("\n")
        .filter((line) => line.toLowerCase().includes(search.toLowerCase()))
        .join("\n")
    : output;

  if (!run) {
    return (
      <Shell>
        {error ? (
          <p role="alert" className="text-sm text-destructive-text">
            {error}
          </p>
        ) : (
          <Card>
            <CardContent className="p-0">
              <SecondaryPanelContentSkeleton label={t("tasks.loading")} />
            </CardContent>
          </Card>
        )}
      </Shell>
    );
  }

  return (
    <Shell>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <Link
            to={`/tasks/${encodeURIComponent(run.scheduleId)}`}
            className="text-sm text-muted-foreground hover:underline"
          >
            ← {t("tasks.backToTask")}
          </Link>
          <h2 className="mt-2 break-words text-xl font-semibold">{run.name}</h2>
          <div className="mt-1 flex flex-wrap items-center gap-2">
            <Status state={run.state} />
            <span className="text-xs tabular-nums text-muted-foreground">
              {fmt(run.startedAt || run.scheduledAt)}
            </span>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <label className="inline-flex cursor-pointer items-center gap-2 text-sm">
            <Switch
              size="default"
              checked={follow}
              onCheckedChange={setFollow}
            />
            <span>{t("tasks.follow")}</span>
          </label>
          {["pending", "dispatching", "running"].includes(run.state) && (
            <Button
              variant="destructive"
              disabled={canceling}
              onClick={() => void cancel()}
            >
              {t("tasks.cancel")}
            </Button>
          )}
        </div>
      </div>
      {error && (
        <div
          role="alert"
          className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive-text"
        >
          {error}
        </div>
      )}
      <Card>
        <CardContent className="grid gap-3 p-4 text-sm md:grid-cols-3">
          <div>
            <b className="font-medium text-muted-foreground">
              {t("tasks.scheduled")}
            </b>
            <p className="mt-0.5 tabular-nums">{fmt(run.scheduledAt)}</p>
          </div>
          <div>
            <b className="font-medium text-muted-foreground">
              {t("tasks.started")}
            </b>
            <p className="mt-0.5 tabular-nums">{fmt(run.startedAt)}</p>
          </div>
          <div>
            <b className="font-medium text-muted-foreground">
              {t("tasks.finished")}
            </b>
            <p className="mt-0.5 tabular-nums">{fmt(run.finishedAt)}</p>
          </div>
          {run.thread && (
            <div className="min-w-0 md:col-span-3">
              <b className="font-medium text-muted-foreground">
                {t("tasks.thread")}
              </b>
              <p className="mt-0.5 break-all">
                <Link
                  className="underline underline-offset-2"
                  to={`/agent?device=${encodeURIComponent(run.device)}&thread=${encodeURIComponent(run.thread)}&turn=${encodeURIComponent(run.turn || run.id)}`}
                >
                  {run.thread}
                </Link>
              </p>
            </div>
          )}
        </CardContent>
      </Card>
      <div className="flex flex-wrap items-center gap-2">
        <Input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder={t("tasks.searchLogs")}
          aria-label={t("tasks.searchLogs")}
          className="min-w-[180px] flex-1"
        />
        <Button
          variant="outline"
          className={cn(
            copiedId === "run-log" && "border-success text-success-foreground",
          )}
          onClick={() => copy("run-log", output)}
        >
          {copiedId === "run-log" ? (
            <>
              <Icon name="Check" />
              {t("common.copied")}
            </>
          ) : (
            <>
              <Icon name="Copy" />
              {t("tasks.copy")}
            </>
          )}
        </Button>
        <Button
          variant="outline"
          onClick={() => {
            const blob = new Blob([output], { type: "text/plain" });
            const url = URL.createObjectURL(blob);
            const a = document.createElement("a");
            a.href = url;
            a.download = `${run.name || run.id}.log`;
            a.click();
            URL.revokeObjectURL(url);
          }}
        >
          <Icon name="Download" />
          {t("tasks.download")}
        </Button>
      </div>
      <pre className="max-h-[60vh] min-h-48 overflow-auto rounded-lg border border-border bg-surface-recessed-solid p-4 font-mono text-xs leading-relaxed whitespace-pre-wrap break-words">
        {shown || t("tasks.noLogs")}
      </pre>
      <p className="text-xs tabular-nums text-muted-foreground">
        {t("tasks.logOffset")} {offset}
        {run.reason ? ` · ${run.reason}` : ""}
      </p>
    </Shell>
  );
}

export function NewTaskPage() {
  const [params] = useSearchParams();
  const duplicateId = params.get("duplicate") || undefined;
  return <TaskForm key={duplicateId || "new"} duplicateId={duplicateId} />;
}

export function EditTaskPage() {
  const { id = "" } = useParams();
  const { t } = useI18n();
  const { data: schedule, error } = useTaskResource(
    `edit:${id}`,
    (signal) => request<Schedule>("get", { id }, signal),
    false,
  );
  return schedule ? (
    <TaskForm key={id} existing={schedule} />
  ) : (
    <Shell>
      {error ? (
        <p role="alert" className="text-sm text-destructive-text">
          {error}
        </p>
      ) : (
        <Card>
          <CardContent className="p-0">
            <SecondaryPanelContentSkeleton label={t("tasks.loading")} />
          </CardContent>
        </Card>
      )}
    </Shell>
  );
}
