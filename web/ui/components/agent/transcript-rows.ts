/* Pure projection layer (bb packages/thread-view): turns the flat
 * ToolItem[] of a turn into bb-style work rows — per-kind classification,
 * natural-language titles, diff stats, durations, and bundle collapsing
 * ("Explored 5 files · Ran 3 commands"). No protocol change: everything is
 * derived client-side from tool name/args/summary/result/diff + client
 * wall-clock stamps. */

import type { ToolItem, Translator } from "./types";

export type WorkKind =
  | "command"
  | "file-read"
  | "file-change"
  | "search"
  | "todo"
  | "question"
  | "generic";

/* Tool registry mirrors internal/server/agent_tools.go Name() values. */
const TOOL_WORK_KIND: Record<string, WorkKind> = {
  shell: "command",
  read_file: "file-read",
  write_file: "file-change",
  edit: "file-change",
  grep: "search",
  glob: "search",
  list_dir: "search",
  todo_write: "todo",
  ask: "question",
};

export function workKindOf(toolName: string): WorkKind {
  return TOOL_WORK_KIND[toolName] ?? "generic";
}

/* Primary target shown after the verb: shell → the command, file tools →
 * the path, grep/glob → the pattern. Falls back to the summary/args. */
export function rowTarget(tool: ToolItem): string {
  let args: Record<string, unknown> | null = null;
  try {
    const parsed: unknown = JSON.parse(tool.args);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed))
      args = parsed as Record<string, unknown>;
  } catch {
    /* keep null */
  }
  const pick = (k: string): string | null => {
    const v = args?.[k];
    return typeof v === "string" && v ? v : null;
  };
  const target =
    pick("command") ??
    pick("path") ??
    pick("pattern") ??
    pick("dir") ??
    pick("question") ??
    tool.summary ??
    "";
  const flat = target.replace(/\s+/g, " ").trim();
  return flat.length > 80 ? flat.slice(0, 80) + "…" : flat;
}

/* bb timeline-row-title verbs: present tense while pending, past when
 * settled ("Reading"/"Read", "Searching"/"Searched", "Ran"). */
export function rowVerb(tool: ToolItem, t: Translator): string {
  const running = !tool.done;
  switch (workKindOf(tool.name)) {
    case "command":
      return running ? t("agent.rowRunning") : t("agent.rowRan");
    case "file-read":
      return running ? t("agent.rowReading") : t("agent.rowRead");
    case "file-change":
      return tool.name === "write_file"
        ? running
          ? t("agent.rowWriting")
          : t("agent.rowWrote")
        : running
          ? t("agent.rowEditing")
          : t("agent.rowEdited");
    case "search":
      return tool.name === "list_dir"
        ? running
          ? t("agent.rowListing")
          : t("agent.rowListed")
        : running
          ? t("agent.rowSearching")
          : t("agent.rowSearched");
    case "todo":
      return t("agent.rowUpdatedTodos");
    case "question":
      /* bb timeline-row-title: "Asking"/"Asked". */
      return running ? t("agent.rowAsking") : t("agent.rowAsked");
    case "generic":
      return running ? t("agent.rowRunningTool") : t("agent.rowRanTool");
  }
}

export interface DiffStats {
  added: number;
  removed: number;
}

/* Count +/- body lines of a unified diff (skipping +++/--- file headers). */
export function diffStatsOf(patch: string): DiffStats | null {
  if (!patch) return null;
  let added = 0;
  let removed = 0;
  for (const line of patch.split("\n")) {
    if (line.startsWith("+++") || line.startsWith("---")) continue;
    if (line.startsWith("+")) added++;
    else if (line.startsWith("-")) removed++;
  }
  return added === 0 && removed === 0 ? null : { added, removed };
}

/* bb durationToCompactString (shared with ThinkingBlock): <1s → "500ms",
 * <60s → "46s", else "2m 5s" / "1h 2m". ≤1s is never shown. */
export function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.max(100, Math.round(ms / 100) * 100)}ms`;
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  const rs = s % 60;
  if (m < 60) return rs ? `${m}m ${rs}s` : `${m}m`;
  const h = Math.floor(m / 60);
  const rm = m % 60;
  return rm ? `${h}h ${rm}m` : `${h}h`;
}

/* Client-stamped duration; absent for history replays (no stamps). */
export function toolDurationMs(tool: ToolItem): number | null {
  if (tool.startedAt === undefined || tool.doneAt === undefined) return null;
  return Math.max(0, tool.doneAt - tool.startedAt);
}

/* Bundle collapsing (bb timeline-view step/bundle summaries): a maximal run
 * of consecutive settled tools of one kind collapses into a single summary
 * row when it has ≥3 entries. Runs are computed per turn; the active
 * (streaming) turn never collapses so live rows stay visible. */
export interface ToolBundle {
  kind: WorkKind;
  tools: ToolItem[];
}

export type ToolGroup =
  | { type: "single"; tool: ToolItem }
  | { type: "bundle"; bundle: ToolBundle };

export const BUNDLE_MIN = 3;

export function groupTools(tools: ToolItem[], turnActive: boolean): ToolGroup[] {
  /* While the turn is live, an ask call renders as the interactive AskItem
   * card — a parallel tool row would duplicate it. Once settled, the row
   * (workKind "question", Q&A body) is the only representation, so it
   * stays. */
  const rows = turnActive
    ? tools.filter((tool) => tool.name !== "ask")
    : tools;
  if (turnActive) return rows.map((tool) => ({ type: "single", tool }));
  const groups: ToolGroup[] = [];
  let i = 0;
  while (i < rows.length) {
    const kind = workKindOf(rows[i].name);
    let j = i + 1;
    while (j < rows.length && workKindOf(rows[j].name) === kind) j++;
    const run = rows.slice(i, j);
    /* Questions never bundle: each ask is its own Q&A row. */
    if (run.length >= BUNDLE_MIN && kind !== "question" && run.every((x) => x.done)) {
      groups.push({ type: "bundle", bundle: { kind, tools: run } });
    } else {
      for (const tool of run) groups.push({ type: "single", tool });
    }
    i = j;
  }
  return groups;
}

/* bb bundle summary segment: "Explored 5 files" / "Ran 3 commands" /
 * "Edited 2 files" — joined with " · " by the caller. */
export function bundleSegment(bundle: ToolBundle, t: Translator): string {
  const n = bundle.tools.length;
  switch (bundle.kind) {
    case "command":
      return t("agent.bundleRan", { n });
    case "file-read":
    case "search":
      return t("agent.bundleExplored", { n });
    case "file-change":
      return t("agent.bundleEdited", { n });
    case "todo":
      return t("agent.bundleTodos", { n });
    case "question":
    case "generic":
      return t("agent.bundleTools", { n });
  }
}

/* bb PAST_ROW_DIM_CLASS_NAME: completed work rows outside the latest bundle
 * dim to opacity-40. rdev port: callers dim every tool/bundle row of turns
 * older than the transcript's last turn. */
export const PAST_ROW_DIM_CLASS = "opacity-40";

/* Image reads: read_file returns "[binary file, N bytes, base64-encoded]"
 * + the base64 body (internal/server/agent_tools.go). When the target path
 * is a browser-decodable image, the row body renders an <img> built from
 * this data URL instead of the raw blob. */
const IMAGE_MIME_BY_EXT: Record<string, string> = {
  png: "image/png",
  apng: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  svg: "image/svg+xml",
  bmp: "image/bmp",
  ico: "image/x-icon",
  avif: "image/avif",
};

export function imageMimeOf(path: string): string | null {
  const i = path.lastIndexOf(".");
  if (i < 0) return null;
  return IMAGE_MIME_BY_EXT[path.slice(i + 1).toLowerCase()] ?? null;
}

const BINARY_RESULT_PREFIX = /^\[binary file, \d+ bytes, base64-encoded\]\n/;

export function toolImageDataUrl(tool: ToolItem): string | null {
  if (workKindOf(tool.name) !== "file-read") return null;
  const mime = imageMimeOf(rowTarget(tool));
  if (!mime) return null;
  const match = BINARY_RESULT_PREFIX.exec(tool.result);
  if (!match) return null;
  const body = tool.result.slice(match[0].length).trim();
  if (!body) return null;
  return `data:${mime};base64,${body}`;
}
