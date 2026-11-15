export type { Spec, Schedule, Change, Run, Options };

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
