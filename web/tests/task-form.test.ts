import { describe, expect, it } from "vitest";
import {
  cronPresetFor,
  scheduleSpecForRequest,
  timingModeFor,
} from "../ui/pages/tasks/schedule-form";

const base = {
  name: "backup",
  kind: "shell" as const,
  device: "device-1",
  cwd: "",
  cron: "0 * * * *",
  at: "2026-11-23T08:00:00.000Z",
  timezone: "",
  command: "echo ok",
  sessionMode: "new" as const,
  thread: "",
  model: "",
  reasoning: "",
  prompt: "",
  allowedTools: [],
  timeoutSeconds: 300,
  tokenBudget: 0,
  busyPolicy: "skip" as const,
  maxWaitSeconds: 300,
  maxRuns: 0,
  expiresAt: "",
  enabled: true,
  authorized: true,
  misfirePolicy: "skip" as const,
  graceSeconds: 1800,
  mutexGroup: "",
};

describe("schedule form normalization", () => {
  it("sends exactly one timing mode", () => {
    expect(scheduleSpecForRequest(base, "cron")).toMatchObject({
      cron: "0 * * * *",
      at: "",
      timezone: "UTC",
    });
    expect(scheduleSpecForRequest(base, "once", false)).toMatchObject({
      cron: "",
      at: base.at,
      enabled: false,
    });
  });

  it("derives mode and common presets from loaded schedules", () => {
    expect(timingModeFor({ at: base.at })).toBe("once");
    expect(timingModeFor({ at: "" })).toBe("cron");
    expect(cronPresetFor("*/15 * * * *")).toBe("*/15 * * * *");
    expect(cronPresetFor("3 4 * * 1")).toBe("custom");
  });
});
