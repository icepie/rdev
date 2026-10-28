/* Agent WS protocol shared by the page and the split-screen panes: frame
 * parsers, composer constants and the transcript key sequence. Moved out of
 * pages/Agent.tsx so secondary panes (split view) reuse the exact same
 * decoding. */
import type { ThreadInfo, TodoItem, TranscriptItem } from "./types";

export type PermMode = "full" | "write" | "ask";
export type ReasoningLevel = "off" | "low" | "medium" | "high";
export type Status = "disconnected" | "connecting" | "authing" | "connected";

export interface ModelInfo {
  id: string;
  name?: string;
  provider?: string;
  contextWindow?: number;
}

export interface TurnUsage {
  inputTokens: number;
  outputTokens: number;
}

/* bb PERMISSION_MODE_OPTIONS: icons SquareUnlock02/FolderEdit/SecurityCheck;
 * "full" carries tone "warning" → trigger + menu item in --warning-text
 * (bb's orange). */
export const PERM_MODES: { mode: PermMode; icon: string; label: string; hint: string }[] = [
  { mode: "full", icon: "SquareUnlock02", label: "agent.permFull", hint: "agent.permFullHint" },
  { mode: "write", icon: "FolderEdit", label: "agent.permWrite", hint: "agent.permWriteHint" },
  { mode: "ask", icon: "SecurityCheck", label: "agent.permAsk", hint: "agent.permAskHint" },
];

export const REASONING_LEVELS: { level: ReasoningLevel; label: string }[] = [
  { level: "off", label: "agent.reasoningOff" },
  { level: "low", label: "agent.reasoningLow" },
  { level: "medium", label: "agent.reasoningMedium" },
  { level: "high", label: "agent.reasoningHigh" },
];

export const MAX_IMAGES = 4;
export const MAX_IMAGE_BYTES = 4 * 1024 * 1024;

export interface PendingImage {
  name: string;
  mime: string;
  data: string; // base64, no data: prefix
  preview: string; // data URL for the thumbnail
}

export function parsePermMode(v: unknown): PermMode {
  return v === "full" || v === "ask" ? v : "write";
}

export function parseReasoningLevel(v: unknown): ReasoningLevel | null {
  return v === "off" || v === "low" || v === "medium" || v === "high" ? v : null;
}

/* Transcript item keys: one process-wide sequence is enough — keys only
 * need to be unique within a pane's list. */
let keySeq = 1;

export function nextKey(): number {
  return keySeq++;
}

export function asString(v: unknown): string {
  return typeof v === "string" ? v : "";
}

export function parseStringArray(v: unknown): string[] {
  return Array.isArray(v)
    ? v.filter((x): x is string => typeof x === "string")
    : [];
}

export function parseTodos(v: unknown): TodoItem[] {
  if (!Array.isArray(v)) return [];
  return v.flatMap((x) => {
    if (!x || typeof x !== "object") return [];
    const o = x as { content?: unknown; status?: unknown };
    return [
      {
        content: asString(o.content),
        status: asString(o.status) || "pending",
      },
    ];
  });
}

export function parseThreads(v: unknown): ThreadInfo[] {
  if (!Array.isArray(v)) return [];
  return v.flatMap((x) => {
    if (!x || typeof x !== "object") return [];
    const o = x as {
      id?: unknown;
      title?: unknown;
      updatedAt?: unknown;
      running?: unknown;
      tool?: unknown;
    };
    const id = asString(o.id);
    if (!id) return [];
    return [
      {
        id,
        title: asString(o.title),
        updatedAt: asString(o.updatedAt),
        running: !!o.running,
        tool: asString(o.tool),
      },
    ];
  });
}

/* all_threads op: every device's threads, flattened into the sidebar. */
export interface AllThreadsDevice {
  device: string;
  os?: string;
  online: boolean;
  threads: ThreadInfo[];
}

export function parseAllThreads(v: unknown): AllThreadsDevice[] {
  if (!Array.isArray(v)) return [];
  return v.flatMap((x) => {
    if (!x || typeof x !== "object") return [];
    const o = x as {
      device?: unknown;
      os?: unknown;
      online?: unknown;
      threads?: unknown;
    };
    const deviceId = asString(o.device);
    if (!deviceId) return [];
    return [
      {
        device: deviceId,
        os: asString(o.os) || undefined,
        online: !!o.online,
        threads: parseThreads(o.threads),
      },
    ];
  });
}

/* Ops whose frames are scoped to one thread: frames tagged with another
 * thread's id are routed to that thread's pane (split view) or dropped when
 * no pane shows it. auth_ok / thread_switched are deliberately absent (they
 * switch the view). */
export const THREAD_SCOPED_OPS: Record<string, true> = {
  thread_peeked: true,
  turn_start: true,
  delta: true,
  reasoning: true,
  tool_start: true,
  tool_end: true,
  todo: true,
  done: true,
  stopped: true,
  error: true,
  approval_request: true,
  ask_request: true,
  ask_resolved: true,
  queue: true,
  changes: true,
  diff: true,
  thread_meta: true,
  file_content: true,
  dir_entries: true,
  dir_mkdir: true,
};

/* Subset of THREAD_SCOPED_OPS that mutates a pane's transcript state (the
 * rest feeds the page-level right panel / dialogs, current thread only). */
export const TRANSCRIPT_OPS: Record<string, true> = {
  thread_peeked: true,
  turn_start: true,
  delta: true,
  reasoning: true,
  tool_start: true,
  tool_end: true,
  todo: true,
  done: true,
  stopped: true,
  error: true,
  approval_request: true,
  ask_request: true,
  ask_resolved: true,
  queue: true,
  reset_ok: true,
};

/* auth_ok / thread_switched / thread_peeked history → transcript items,
 * with the extended reasoning/summary/diff fields. */
export function replayHistory(history: unknown[]): TranscriptItem[] {
  const restored: TranscriptItem[] = [];
  for (const raw of history) {
    if (!raw || typeof raw !== "object") continue;
    const entry = raw as {
      role?: unknown;
      text?: unknown;
      reasoning?: unknown;
      tools?: unknown;
      images?: unknown;
    };
    if (entry.role === "user") {
      const images = parseStringArray(entry.images);
      restored.push({
        kind: "user",
        key: nextKey(),
        text: asString(entry.text),
        ...(images.length ? { images } : {}),
      });
      continue;
    }
    const tools = Array.isArray(entry.tools) ? entry.tools : [];
    const text = asString(entry.text);
    const reasoning = asString(entry.reasoning);
    if (!tools.length && !text && !reasoning) continue;
    restored.push({
      kind: "turn",
      key: nextKey(),
      text,
      reasoning,
      streaming: false,
      streamingReasoning: false,
      tools: tools.map((rawTool, i) => {
        const tool = (rawTool ?? {}) as {
          name?: unknown;
          args?: unknown;
          result?: unknown;
          summary?: unknown;
          diff?: unknown;
        };
        return {
          id: `h${i}`,
          name: asString(tool.name),
          args: asString(tool.args),
          summary: asString(tool.summary),
          result: asString(tool.result),
          diff: asString(tool.diff),
          done: true,
          isError: false,
        };
      }),
    });
  }
  return restored;
}

export function formatTokenCount(n: number): string {
  if (n >= 1000) return (n / 1000).toFixed(1) + "k";
  return String(n);
}

/* auth_ok / thread_switched / done usage frame: cumulative tokens of the
 * thread's latest completed turn (drives the context-window indicator). */
export function parseUsage(v: unknown): TurnUsage | null {
  if (!v || typeof v !== "object") return null;
  const o = v as { inputTokens?: unknown; outputTokens?: unknown };
  if (typeof o.inputTokens !== "number" && typeof o.outputTokens !== "number") {
    return null;
  }
  return {
    inputTokens: typeof o.inputTokens === "number" ? o.inputTokens : 0,
    outputTokens: typeof o.outputTokens === "number" ? o.outputTokens : 0,
  };
}

/* Thread ids minted by the server start with the creation timestamp
 * (`t_<13 digits>`); unknown formats sort as epoch 0. */
export function threadCreatedMs(id: string): number {
  const m = /^t_(\d{13})/.exec(id);
  return m ? Number(m[1]) : 0;
}
