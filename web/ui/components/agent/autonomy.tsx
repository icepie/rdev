import { Button } from "../button";
import type { Translator } from "./types";

export interface GoalState {
  objective: string;
  status: string;
  tokenBudget?: number;
  tokensUsed: number;
}

export interface LoopState {
  prompt: string;
  status: string;
  iteration: number;
  maxIterations?: number;
  durationMs?: number;
  condition?: string;
  until?: boolean;
}

export interface AutonomyState {
  goal: GoalState | null;
  loop: LoopState | null;
  message: string;
}

function count(value: unknown): number | undefined {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : undefined;
}

export function parseAutonomy(msg: Record<string, unknown>): AutonomyState {
  const goal = msg.goal !== null && typeof msg.goal === "object" && !Array.isArray(msg.goal) ? msg.goal as Record<string, unknown> : null;
  const loop = msg.loop !== null && typeof msg.loop === "object" && !Array.isArray(msg.loop) ? msg.loop as Record<string, unknown> : null;
  return {
    goal: goal && typeof goal.objective === "string" && typeof goal.status === "string" ? {
      objective: goal.objective, status: goal.status,
      tokensUsed: count(goal.tokensUsed) ?? 0, tokenBudget: count(goal.tokenBudget),
    } : null,
    loop: loop && typeof loop.prompt === "string" && typeof loop.status === "string" ? {
      prompt: loop.prompt, status: loop.status, iteration: count(loop.iteration) ?? 0,
      maxIterations: count(loop.maxIterations), durationMs: count(loop.durationMs),
      condition: typeof loop.condition === "string" ? loop.condition : undefined, until: loop.until === true,
    } : null,
    message: typeof msg.message === "string" ? msg.message : "",
  };
}

export function AutonomyControls({ state, ready, thread, send, t }: {
  state: AutonomyState;
  ready: boolean;
  thread: string;
  send: (payload: Record<string, unknown>) => boolean;
  t: Translator;
}) {
  if (!state.goal && !state.loop && !state.message) return null;
  return <section aria-label={t("agent.autonomy.title")} className="min-w-0 overflow-hidden rounded-lg border border-border bg-surface-raised-solid">
    <div className="flex min-w-0 flex-col gap-2 px-3 py-2 text-xs">
      {(["goal", "loop"] as const).map(kind => {
        const mode = state[kind];
        if (!mode) return null;
        const active = mode.status === "active";
        const paused = mode.status === "paused";
        const label = t(`agent.autonomy.${kind}`);
        return <div key={kind} className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1.5">
          <div className="min-w-0 flex-1 basis-48">
            <div className="flex flex-wrap gap-x-2 text-muted-foreground">
              <span className="font-medium text-foreground">{label}</span>
              <span>{t(`agent.autonomy.${mode.status}`)}</span>
              {kind === "goal" && state.goal && <span>{t("agent.autonomy.tokens", { used: state.goal.tokensUsed })}{state.goal.tokenBudget !== undefined ? ` / ${state.goal.tokenBudget}` : ""}</span>}
              {kind === "loop" && state.loop && <span>{t("agent.autonomy.iteration", { count: state.loop.iteration })}{state.loop.maxIterations !== undefined ? ` / ${state.loop.maxIterations}` : ""}{state.loop.durationMs ? ` · ${state.loop.durationMs / 1000}s` : ""}</span>}
            </div>
            <p className="max-h-24 overflow-y-auto whitespace-pre-wrap break-words text-foreground [overflow-wrap:anywhere]">{kind === "goal" ? state.goal!.objective : state.loop!.prompt}</p>
            {kind === "loop" && state.loop?.condition && <p className="max-h-16 overflow-y-auto break-words font-mono text-muted-foreground [overflow-wrap:anywhere]">{state.loop.until ? "until" : "while"} {state.loop.condition}</p>}
          </div>
          <div className="ml-auto flex shrink-0 items-center gap-1">
          {(active || paused) && <Button size="sm" variant="ghost" disabled={!ready}
            aria-label={`${label}: ${t(active ? "agent.autonomy.pause" : "agent.autonomy.resume")}`}
            onClick={() => send({ op: kind, action: active ? "pause" : "resume", thread })}>
            {t(active ? "agent.autonomy.pause" : "agent.autonomy.resume")}
          </Button>}
          <Button size="sm" variant="ghost" disabled={!ready}
            aria-label={`${label}: ${t("agent.autonomy.drop")}`}
            onClick={() => send({ op: kind, action: kind === "goal" ? "drop" : "off", thread })}>{t("agent.autonomy.drop")}</Button>
          </div>
        </div>;
      })}
      {state.message && <p role="status" className="break-words text-muted-foreground">{state.message}</p>}
    </div>
  </section>;
}
