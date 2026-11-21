import type { Spec } from "./types";

export type TimingMode = "cron" | "once";

export const CRON_PRESETS = [
  { value: "*/5 * * * *", labelKey: "tasks.every5Minutes" },
  { value: "*/15 * * * *", labelKey: "tasks.every15Minutes" },
  { value: "*/30 * * * *", labelKey: "tasks.every30Minutes" },
  { value: "0 * * * *", labelKey: "tasks.everyHour" },
  { value: "0 0 * * *", labelKey: "tasks.everyDay" },
] as const;

export function timingModeFor(spec: Pick<Spec, "at">): TimingMode {
  return spec.at ? "once" : "cron";
}

export function cronPresetFor(cron: string): string {
  return CRON_PRESETS.some((preset) => preset.value === cron) ? cron : "custom";
}

export function scheduleSpecForRequest(
  spec: Spec,
  timingMode: TimingMode,
  enabled = spec.enabled,
): Spec {
  return {
    ...spec,
    cron: timingMode === "cron" ? spec.cron.trim() : "",
    at: timingMode === "once" ? spec.at : "",
    timezone: spec.timezone.trim() || "UTC",
    enabled,
  };
}
