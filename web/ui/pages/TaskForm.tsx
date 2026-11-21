import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useI18n } from "../lib/i18n";
import { Button } from "../components/button";
import { Input } from "../components/input";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "../components/card";
import { request } from "./tasks/api";
import {
  CRON_PRESETS,
  cronPresetFor,
  scheduleSpecForRequest,
  timingModeFor,
  type TimingMode,
} from "./tasks/schedule-form";
import type { Options, Schedule, Spec } from "./tasks/types";
import { fmt, localDateInput, Shell } from "./TasksShared";

export { TaskForm };

const selectClass = "h-9 rounded-md border border-input bg-transparent px-3 text-sm";

const blank: Spec = {
  name: "",
  kind: "shell",
  device: "",
  cwd: "",
  cron: "",
  at: "",
  timezone: "UTC",
  command: "",
  sessionMode: "new",
  thread: "",
  model: "",
  reasoning: "",
  prompt: "",
  allowedTools: [],
  timeoutSeconds: 300,
  tokenBudget: 0,
  busyPolicy: "skip",
  maxWaitSeconds: 300,
  maxRuns: 0,
  expiresAt: "",
  enabled: true,
  authorized: false,
  misfirePolicy: "skip",
  graceSeconds: 1800,
  mutexGroup: "",
};

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="grid gap-1.5 text-sm">
      <span className="font-medium">{label}</span>
      {children}
    </label>
  );
}

function TaskForm({ existing, duplicateId }: { existing?: Schedule; duplicateId?: string }) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const [spec, setSpec] = useState<Spec>(() => ({ ...blank, ...existing }));
  const [timingMode, setTimingMode] = useState<TimingMode>(() => timingModeFor(existing || blank));
  const [options, setOptions] = useState<Options>({});
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [preview, setPreview] = useState<string[]>([]);

  const set = <K extends keyof Spec>(key: K, value: Spec[K]) => {
    setSpec((current) => ({ ...current, [key]: value }));
  };

  useEffect(() => {
    if (!duplicateId || existing) return;
    const controller = new AbortController();
    void request<Schedule>("get", { id: duplicateId }, controller.signal)
      .then((source) => {
        if (controller.signal.aborted) return;
        setSpec({ ...blank, ...source, name: `${source.name} (copy)` });
        setTimingMode(timingModeFor(source));
      })
      .catch((cause) => {
        if (!controller.signal.aborted) {
          setError(cause instanceof Error ? cause.message : String(cause));
        }
      });
    return () => controller.abort();
  }, [duplicateId, existing]);

  useEffect(() => {
    const controller = new AbortController();
    void request<Options>("options", {}, controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setOptions(value);
      })
      .catch((cause) => {
        if (!controller.signal.aborted) {
          setError(cause instanceof Error ? cause.message : String(cause));
        }
      });
    return () => controller.abort();
  }, []);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const submitter = (event.nativeEvent as SubmitEvent).submitter;
    const mode = submitter instanceof HTMLButtonElement ? submitter.value : "save";
    const savedSpec = scheduleSpecForRequest(spec, timingMode, mode === "paused" ? false : spec.enabled);
    setSaving(true);
    setError("");
    try {
      const schedule = existing
        ? await request<Schedule>("update", {
            id: existing.id,
            revision: existing.revision,
            spec: savedSpec,
          })
        : await request<Schedule>("create", { spec: savedSpec });
      if (mode === "run_now") await request("run_now", { id: schedule.id });
      navigate(`/tasks/${encodeURIComponent(schedule.id)}`);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSaving(false);
    }
  };

  const previewRuns = async () => {
    setError("");
    try {
      setPreview(await request<string[]>("preview", { spec: scheduleSpecForRequest(spec, timingMode) }));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const cronPreset = cronPresetFor(spec.cron);

  return (
    <Shell>
      <form onSubmit={submit} className="grid gap-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <Link
              to={existing ? `/tasks/${encodeURIComponent(existing.id)}` : "/tasks"}
              className="text-sm text-muted-foreground hover:underline"
            >
              ← {t("tasks.back")}
            </Link>
            <h2 className="mt-2 text-xl font-semibold">
              {existing ? t("tasks.edit") : t("tasks.new")}
            </h2>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button type="button" variant="outline" onClick={() => void previewRuns()}>
              {t("tasks.preview")}
            </Button>
            <Button type="submit" value="save" disabled={saving}>
              {saving ? t("tasks.saving") : t("tasks.save")}
            </Button>
            <Button type="submit" value="run_now" variant="outline" disabled={saving}>
              {t("tasks.saveRunNow")}
            </Button>
            <Button type="submit" value="paused" variant="outline" disabled={saving}>
              {t("tasks.savePaused")}
            </Button>
          </div>
        </div>

        {error && (
          <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">
            {error}
          </p>
        )}

        <Card>
          <CardHeader>
            <CardTitle>{t("tasks.basics")}</CardTitle>
            <CardDescription>{t("tasks.basicsHint")}</CardDescription>
          </CardHeader>
          <CardContent className="grid gap-4 md:grid-cols-2">
            <Field label={t("tasks.name")}>
              <Input required value={spec.name} onChange={(event) => set("name", event.target.value)} />
            </Field>
            <Field label={t("tasks.kind")}>
              <select
                className={selectClass}
                value={spec.kind}
                onChange={(event) => set("kind", event.target.value as Spec["kind"])}
              >
                <option value="shell">{t("tasks.shell")}</option>
                <option value="agent">{t("tasks.agent")}</option>
              </select>
            </Field>
            <Field label={t("tasks.device")}>
              <select
                className={selectClass}
                required
                disabled={Boolean(existing)}
                value={spec.device}
                onChange={(event) => set("device", event.target.value)}
              >
                <option value="">{t("tasks.selectDevice")}</option>
                {(options.devices || []).map((device) => (
                  <option key={device.id} value={device.id}>
                    {device.name || device.id}
                    {device.online === false ? ` (${t("tasks.offline")})` : ""}
                  </option>
                ))}
              </select>
            </Field>
            <Field label={t("tasks.cwd")}>
              <Input value={spec.cwd} onChange={(event) => set("cwd", event.target.value)} />
            </Field>

            <div className="grid gap-2 md:col-span-2">
              <span className="text-sm font-medium">{t("tasks.timing")}</span>
              <div className="grid grid-cols-2 gap-2 rounded-lg bg-surface-recessed-solid p-1">
                <Button
                  type="button"
                  variant={timingMode === "cron" ? "secondary" : "ghost"}
                  aria-pressed={timingMode === "cron"}
                  onClick={() => setTimingMode("cron")}
                >
                  {t("tasks.recurring")}
                </Button>
                <Button
                  type="button"
                  variant={timingMode === "once" ? "secondary" : "ghost"}
                  aria-pressed={timingMode === "once"}
                  onClick={() => setTimingMode("once")}
                >
                  {t("tasks.runOnce")}
                </Button>
              </div>
            </div>

            {timingMode === "cron" ? (
              <>
                <Field label={t("tasks.preset")}>
                  <select
                    className={selectClass}
                    value={cronPreset}
                    onChange={(event) => {
                      if (event.target.value !== "custom") set("cron", event.target.value);
                    }}
                  >
                    <option value="custom">{t("tasks.custom")}</option>
                    {CRON_PRESETS.map((preset) => (
                      <option key={preset.value} value={preset.value}>
                        {t(preset.labelKey)}
                      </option>
                    ))}
                  </select>
                </Field>
                <Field label={t("tasks.cron")}>
                  <Input
                    required
                    value={spec.cron}
                    onChange={(event) => set("cron", event.target.value)}
                    placeholder="*/15 * * * *"
                  />
                </Field>
                <Field label={t("tasks.timezone")}>
                  <Input
                    required
                    value={spec.timezone}
                    onChange={(event) => set("timezone", event.target.value)}
                    placeholder="UTC"
                  />
                </Field>
              </>
            ) : (
              <Field label={t("tasks.at")}>
                <Input
                  required
                  type="datetime-local"
                  value={localDateInput(spec.at)}
                  onChange={(event) => set("at", event.target.value ? new Date(event.target.value).toISOString() : "")}
                />
              </Field>
            )}
            <Field label={t("tasks.expires")}>
              <Input
                type="datetime-local"
                value={localDateInput(spec.expiresAt)}
                onChange={(event) => set("expiresAt", event.target.value ? new Date(event.target.value).toISOString() : "")}
              />
            </Field>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>{spec.kind === "shell" ? t("tasks.shell") : t("tasks.agent")}</CardTitle>
          </CardHeader>
          <CardContent className="grid gap-4">
            <Field label={spec.kind === "shell" ? t("tasks.command") : t("tasks.prompt")}>
              <textarea
                className="min-h-32 rounded-md border border-input bg-transparent p-3 font-mono text-sm"
                required
                value={spec.kind === "shell" ? spec.command : spec.prompt}
                onChange={(event) => set(spec.kind === "shell" ? "command" : "prompt", event.target.value)}
              />
            </Field>
            {spec.kind === "agent" && (
              <div className="grid gap-4 md:grid-cols-2">
                <Field label={t("tasks.sessionMode")}>
                  <select
                    className={selectClass}
                    value={spec.sessionMode}
                    onChange={(event) => set("sessionMode", event.target.value as Spec["sessionMode"])}
                  >
                    <option value="new">{t("tasks.newThread")}</option>
                    <option value="existing">{t("tasks.existingThread")}</option>
                  </select>
                </Field>
                {spec.sessionMode === "new" ? (
                  <Field label={t("tasks.model")}>
                    <select className={selectClass} value={spec.model} onChange={(event) => set("model", event.target.value)}>
                      <option value="">{t("tasks.selectModel")}</option>
                      {(options.models || []).map((model) => (
                        <option key={model.id} value={model.id}>{model.name || model.id}</option>
                      ))}
                    </select>
                  </Field>
                ) : (
                  <Field label={t("tasks.thread")}>
                    <select className={selectClass} value={spec.thread} onChange={(event) => set("thread", event.target.value)}>
                      <option value="">{t("tasks.selectThread")}</option>
                      {(options.threads || [])
                        .filter((thread) => !spec.device || thread.device === spec.device)
                        .map((thread) => (
                          <option key={thread.id} value={thread.id}>{thread.title || thread.id}</option>
                        ))}
                    </select>
                  </Field>
                )}
                <Field label={t("tasks.reasoning")}>
                  <Input value={spec.reasoning} onChange={(event) => set("reasoning", event.target.value)} />
                </Field>
                <Field label={t("tasks.allowedTools")}>
                  <Input
                    value={spec.allowedTools.join(", ")}
                    onChange={(event) => set("allowedTools", event.target.value.split(",").map((value) => value.trim()).filter(Boolean))}
                  />
                </Field>
              </div>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>{t("tasks.execution")}</CardTitle>
            <CardDescription>{t("tasks.executionHint")}</CardDescription>
          </CardHeader>
          <CardContent className="grid gap-4 md:grid-cols-3">
            <Field label={t("tasks.timeout")}>
              <Input type="number" min={1} value={spec.timeoutSeconds} onChange={(event) => set("timeoutSeconds", Number(event.target.value))} />
            </Field>
            <Field label={t("tasks.tokenBudget")}>
              <Input type="number" min={0} value={spec.tokenBudget} onChange={(event) => set("tokenBudget", Number(event.target.value))} />
            </Field>
            <Field label={t("tasks.maxRuns")}>
              <Input type="number" min={0} value={spec.maxRuns} onChange={(event) => set("maxRuns", Number(event.target.value))} />
            </Field>
            <Field label={t("tasks.busyPolicy")}>
              <select className={selectClass} value={spec.busyPolicy} onChange={(event) => set("busyPolicy", event.target.value as Spec["busyPolicy"])}>
                <option value="skip">{t("tasks.skip")}</option>
                <option value="wait">{t("tasks.wait")}</option>
              </select>
            </Field>
            <Field label={t("tasks.maxWait")}>
              <Input type="number" min={1} value={spec.maxWaitSeconds} onChange={(event) => set("maxWaitSeconds", Number(event.target.value))} />
            </Field>
            <Field label={t("tasks.mutex")}>
              <Input value={spec.mutexGroup} onChange={(event) => set("mutexGroup", event.target.value)} />
            </Field>
            <Field label={t("tasks.misfire")}>
              <select className={selectClass} value={spec.misfirePolicy} onChange={(event) => set("misfirePolicy", event.target.value as Spec["misfirePolicy"])}>
                <option value="skip">{t("tasks.skip")}</option>
                <option value="latest">{t("tasks.latest")}</option>
              </select>
            </Field>
            <Field label={t("tasks.grace")}>
              <Input type="number" min={1} value={spec.graceSeconds} onChange={(event) => set("graceSeconds", Number(event.target.value))} />
            </Field>
          </CardContent>
        </Card>

        <Card>
          <CardContent className="grid gap-4 p-4">
            <label className="flex items-start gap-3 text-sm">
              <input className="mt-1" type="checkbox" checked={spec.enabled} onChange={(event) => set("enabled", event.target.checked)} />
              <span>
                <b className="block">{t("tasks.enabled")}</b>
                <span className="text-muted-foreground">{t("tasks.enabledHint")}</span>
              </span>
            </label>
            <label className="flex items-start gap-3 text-sm">
              <input className="mt-1" type="checkbox" checked={spec.authorized} onChange={(event) => set("authorized", event.target.checked)} />
              <span>
                <b className="block">{t("tasks.authorized")}</b>
                <span className="text-muted-foreground">{t("tasks.authorizedHint")}</span>
              </span>
            </label>
          </CardContent>
        </Card>

        {preview.length > 0 && (
          <Card>
            <CardHeader><CardTitle>{t("tasks.preview")}</CardTitle></CardHeader>
            <CardContent>
              <ol className="grid gap-2 text-sm sm:grid-cols-2">
                {preview.map((value, index) => (
                  <li key={value} className="rounded-md bg-surface-recessed-solid px-3 py-2">
                    <span className="mr-2 text-muted-foreground">{index + 1}.</span>{fmt(value)}
                  </li>
                ))}
              </ol>
            </CardContent>
          </Card>
        )}
      </form>
    </Shell>
  );
}
