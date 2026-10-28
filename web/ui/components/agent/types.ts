/* Shared transcript model for the Agent page (browser ↔ server protocol
 * mirrors internal/server/agent.go agentMsg). */

export interface ToolItem {
  id: string;
  name: string;
  args: string;
  summary: string;
  result: string;
  diff: string;
  done: boolean;
  isError: boolean;
  /* Client-side wall clock of tool_start/tool_end (row duration); absent
   * for history replays. */
  startedAt?: number;
  doneAt?: number;
}

export interface TurnItem {
  kind: "turn";
  key: number;
  text: string;
  reasoning: string;
  streaming: boolean;
  streamingReasoning: boolean;
  tools: ToolItem[];
  files?: string[];
  usage?: { inputTokens?: number; outputTokens?: number };
  /* Client-side wall clock of the first delta (message ⋯ menu timestamp);
   * absent for history replays. */
  time?: number;
  /* Reasoning stream timing: first frame → last frame (bb Thought Ns).
   * Absent for history replays → header shows just "Thought". */
  reasoningStartedAt?: number;
  reasoningEndedAt?: number;
  /* Wall clock of the `done` frame (bb "Worked for 2m 5s" turn summary);
   * absent for history replays. */
  endedAt?: number;
}

export interface ApprovalItem {
  kind: "approval";
  key: number;
  id: string;
  name: string;
  args: string;
  summary: string;
  state: "pending" | "approved" | "denied" | "always";
}

export interface BubbleItem {
  kind: "user" | "error" | "system" | "setup";
  key: number;
  text: string;
  /* Attachment file names on user bubbles (turn_start.images / history). */
  images?: string[];
  /* Client-side wall clock for the ⋯ menu timestamp; absent for history. */
  time?: number;
}

export interface AskOption {
  label: string;
  description?: string;
}

/* Inline question card from the ask tool: pending until the browser
 * answers (or the question times out / the turn is stopped). */
export interface AskItem {
  kind: "ask";
  key: number;
  id: string;
  question: string;
  options: AskOption[];
  /* null = pending; "" = dismissed; otherwise the chosen/typed answer. */
  answer: string | null;
}

export type TranscriptItem = TurnItem | ApprovalItem | BubbleItem | AskItem;

export interface TodoItem {
  content: string;
  status: string;
}

export interface ThreadInfo {
  id: string;
  title: string;
  updatedAt: string;
  running: boolean;
  /* Currently executing tool name; only set while running. */
  tool: string;
}

export type Translator = (k: string, v?: Record<string, string | number>) => string;

/* Right-panel git state (browser ↔ server protocol: get_changes → changes,
 * get_diff → diff; mirrors internal/server/agent.go agentMsg fields). */
export interface ChangedFile {
  path: string;
  /* Porcelain status letters, e.g. "M", "A", "D", "??". */
  status: string;
  /* numstat line counts; 0 for untracked files and binary diffs. */
  adds: number;
  dels: number;
}

export interface ChangesState {
  cwd: string;
  branch: string;
  files: ChangedFile[];
  /* Non-empty when cwd is not a git repository or the probe failed. */
  error: string;
}

export interface DiffState {
  path: string;
  diff: string;
  /* "untracked" for untracked files; other values are probe failures. */
  error: string;
}

/* Right-panel file preview tab state (file_read → file_content op). */
export interface FilePreviewState {
  status: "loading" | "ready" | "error";
  content: string;
  truncated: boolean;
  /* Error message from the file_content frame (missing/binary/offline). */
  message: string;
  /* Image previews: content is base64, mime is the <img> type. Absent for
   * plain-text reads. */
  encoding?: string;
  mime?: string;
}
