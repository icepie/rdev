import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useI18n } from "../lib/i18n";
import { Button } from "../components/button";
import { Input } from "../components/input";
import { Textarea } from "../components/textarea";
import { Switch } from "../components/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../components/select";
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

const NONE_OPTION = "__none__";

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
  const [previewing, setPreviewing] = useState(false);
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
    if (!spec.device) {
      setError(t("tasks.selectDevice"));
      return;
    }
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
    if (previewing) return;
    setPreviewing(true);
    setError("");
    try {
      setPreview(await request<string[]>("preview", { spec: scheduleSpecForRequest(spec, timingMode) }));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setPreviewing(false);
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
            <Button type="button" variant="outline" disabled={previewing} onClick={() => void previewRuns()}>
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
          <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive-text">
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
              <Select value={spec.kind} onValueChange={(value) => set("kind", value as Spec["kind"])}>
                <SelectTrigger aria-label={t("tasks.kind")}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="shell">{t("tasks.shell")}</SelectItem>
                  <SelectItem value="agent">{t("tasks.agent")}</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Field label={t("tasks.device")}>
              <Select
                disabled={Boolean(existing)}
                value={spec.device || NONE_OPTION}
                onValueChange={(value) => set("device", value === NONE_OPTION ? "" : value)}
              >
                <SelectTrigger aria-label={t("tasks.device")}>
                  <SelectValue placeholder={t("tasks.selectDevice")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NONE_OPTION}>{t("tasks.selectDevice")}</SelectItem>
                  {(options.devices || []).map((device) => (
                    <SelectItem key={device.id} value={device.id}>
                      {device.name || device.id}
                      {device.online === false ? ` (${t("tasks.offline")})` : ""}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field label={t("tasks.cwd")}>
              <Input value={spec.cwd} onChange={(event) => set("cwd", event.target.value)} className="font-mono" />
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
                  <Select
                    value={cronPreset}
                    onValueChange={(value) => {
                      if (value !== "custom") set("cron", value);
                    }}
                  >
                    <SelectTrigger aria-label={t("tasks.preset")}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="custom">{t("tasks.custom")}</SelectItem>
                      {CRON_PRESETS.map((preset) => (
                        <SelectItem key={preset.value} value={preset.value}>
                          {t(preset.labelKey)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Field label={t("tasks.cron")}>
                  <Input
                    required
                    value={spec.cron}
                    onChange={(event) => set("cron", event.target.value)}
                    placeholder="*/15 * * * *"
                    className="font-mono"
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
              <Textarea
                className="min-h-32 font-mono text-sm"
                required
                value={spec.kind === "shell" ? spec.command : spec.prompt}
                onChange={(event) => set(spec.kind === "shell" ? "command" : "prompt", event.target.value)}
              />
            </Field>
            {spec.kind === "agent" && (
              <div className="grid gap-4 md:grid-cols-2">
                <Field label={t("tasks.sessionMode")}>
                  <Select
                    value={spec.sessionMode}
                    onValueChange={(value) => set("sessionMode", value as Spec["sessionMode"])}
                  >
                    <SelectTrigger aria-label={t("tasks.sessionMode")}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="new">{t("tasks.newThread")}</SelectItem>
                      <SelectItem value="existing">{t("tasks.existingThread")}</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
                {spec.sessionMode === "new" ? (
                  <Field label={t("tasks.model")}>
                    <Select
                      value={spec.model || NONE_OPTION}
                      onValueChange={(value) => set("model", value === NONE_OPTION ? "" : value)}
                    >
                      <SelectTrigger aria-label={t("tasks.model")}>
                        <SelectValue placeholder={t("tasks.selectModel")} />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value={NONE_OPTION}>{t("tasks.selectModel")}</SelectItem>
                        {(options.models || []).map((model) => (
                          <SelectItem key={model.id} value={model.id}>{model.name || model.id}</SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </Field>
                ) : (
                  <Field label={t("tasks.thread")}>
                    <Select
                      value={spec.thread || NONE_OPTION}
                      onValueChange={(value) => set("thread", value === NONE_OPTION ? "" : value)}
                    >
                      <SelectTrigger aria-label={t("tasks.thread")}>
                        <SelectValue placeholder={t("tasks.selectThread")} />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value={NONE_OPTION}>{t("tasks.selectThread")}</SelectItem>
                        {(options.threads || [])
                          .filter((thread) => !spec.device || thread.device === spec.device)
                          .map((thread) => (
                            <SelectItem key={thread.id} value={thread.id}>{thread.title || thread.id}</SelectItem>
                          ))}
                      </SelectContent>
                    </Select>
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
              <Select
                value={spec.busyPolicy}
                onValueChange={(value) => set("busyPolicy", value as Spec["busyPolicy"])}
              >
                <SelectTrigger aria-label={t("tasks.busyPolicy")}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="skip">{t("tasks.skip")}</SelectItem>
                  <SelectItem value="wait">{t("tasks.wait")}</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Field label={t("tasks.maxWait")}>
              <Input type="number" min={1} value={spec.maxWaitSeconds} onChange={(event) => set("maxWaitSeconds", Number(event.target.value))} />
            </Field>
            <Field label={t("tasks.mutex")}>
              <Input value={spec.mutexGroup} onChange={(event) => set("mutexGroup", event.target.value)} />
            </Field>
            <Field label={t("tasks.misfire")}>
              <Select
                value={spec.misfirePolicy}
                onValueChange={(value) => set("misfirePolicy", value as Spec["misfirePolicy"])}
              >
                <SelectTrigger aria-label={t("tasks.misfire")}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="skip">{t("tasks.skip")}</SelectItem>
                  <SelectItem value="latest">{t("tasks.latest")}</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Field label={t("tasks.grace")}>
              <Input type="number" min={1} value={spec.graceSeconds} onChange={(event) => set("graceSeconds", Number(event.target.value))} />
            </Field>
          </CardContent>
        </Card>

        <Card>
          <CardContent className="grid gap-4 p-4">
            <label className="flex cursor-pointer items-start justify-between gap-4 text-sm">
              <span>
                <b className="block font-medium">{t("tasks.enabled")}</b>
                <span className="text-muted-foreground">{t("tasks.enabledHint")}</span>
              </span>
              <Switch
                size="default"
                className="mt-0.5"
                checked={spec.enabled}
                onCheckedChange={(checked) => set("enabled", checked)}
              />
            </label>
            <label className="flex cursor-pointer items-start justify-between gap-4 text-sm">
              <span>
                <b className="block font-medium">{t("tasks.authorized")}</b>
                <span className="text-muted-foreground">{t("tasks.authorizedHint")}</span>
              </span>
              <Switch
                size="default"
                className="mt-0.5"
                checked={spec.authorized}
                onCheckedChange={(checked) => set("authorized", checked)}
              />
            </label>
          </CardContent>
        </Card>

        {preview.length > 0 && (
          <Card>
            <CardHeader><CardTitle>{t("tasks.preview")}</CardTitle></CardHeader>
            <CardContent>
              <ol className="grid gap-2 text-sm tabular-nums sm:grid-cols-2">
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
