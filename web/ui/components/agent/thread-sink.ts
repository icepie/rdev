import { acceptQueueRemoval, clearQueueReturns, type QueueReturns } from "./queue-actions";
import { TRANSCRIPT_BOTTOM_THRESHOLD } from "./transcript-resize";
/* Per-thread transcript state ("sink"): one instance backs the connection's
 * current thread, and every secondary split-screen pane owns one for the
 * thread it shows. The WS handler routes tagged frames to the right sink,
 * so a pane's transcript stays live even while it is not the conn current.
 *
 * Split into `state` (re-rendered values) and `ops` (stable callbacks +
 * refs) so panes can register their ops in a Map keyed by thread id without
 * re-registering on every render. */
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type RefObject,
  type SetStateAction,
} from "react";
import {
  asString,
  parseDisplay,
  nextKey,
  parseStringArray,
  parseTodos,
  parseUsage,
  parseWorking,
  replayHistory,
  type TurnUsage,
} from "./protocol";
import type {
  TodoItem,
  TranscriptItem,
  Translator,
  TurnItem,
} from "./types";

export interface AgentJob {
  id: string;
  kind: string;
  label: string;
  state: "running" | "completed" | "failed" | "canceled";
  started?: string;
  finished?: string;
  output?: string;
  /** State of the last explicit detail reply; list summaries are not details. */
  detailState?: AgentJob["state"];
  result?: string;
  error?: string;
  pendingGuidance?: string[];
}

function parseJob(value: unknown): AgentJob | null {
  if (!value || typeof value !== "object") return null;
  const job = value as Record<string, unknown>;
  if (typeof job.id !== "string" || !["running", "completed", "failed", "canceled"].includes(String(job.state))) return null;
  return { pendingGuidance: parseStringArray(job.pendingGuidance), started: asString(job.started), finished: asString(job.finished), id: job.id, kind: asString(job.kind), label: asString(job.label), state: job.state as AgentJob["state"], output: asString(job.output), result: asString(job.result), error: asString(job.error) };
}

export interface ThreadSinkState {
  jobs: AgentJob[];
  jobsError: string;
  items: TranscriptItem[];
  todos: TodoItem[];
  queue: string[];
  queueIds: string[];
  running: boolean;
  /* Stop requested, server not yet acked (the turn unwind's stopped frame
   * clears it); keeps the stop button disabled in the meantime. */
  stopping: boolean;
  turnUsage: TurnUsage | null;
  currentTool: string;
  workingSince: number | null;
}

interface TurnCompletion {
  usage?: { inputTokens?: number; outputTokens?: number };
  reasoning?: string;
  files?: string[];
  text?: string;
  display?: unknown;
  unfinishedToolResult?: string;
}

export interface ThreadSinkOps {
  jobGuidanceReplies: RefObject<Map<string, (reply: Record<string, unknown> | null) => void>>;
  setJobsError: Dispatch<SetStateAction<string>>;
  setJobs: Dispatch<SetStateAction<AgentJob[]>>;
  setItems: Dispatch<SetStateAction<TranscriptItem[]>>;
  setTodos: Dispatch<SetStateAction<TodoItem[]>>;
  setQueue: Dispatch<SetStateAction<string[]>>;
  setQueueIds: Dispatch<SetStateAction<string[]>>;
  queueReturns: RefObject<QueueReturns>;
  setRunning: Dispatch<SetStateAction<boolean>>;
  setStopping: Dispatch<SetStateAction<boolean>>;
  setTurnUsage: Dispatch<SetStateAction<TurnUsage | null>>;
  setCurrentTool: Dispatch<SetStateAction<string>>;
  setWorkingSince: Dispatch<SetStateAction<number | null>>;
  /* An unfinished assistant turn sits at the end of `items`. */
  turnOpen: RefObject<boolean>;
  openToolIds: RefObject<Set<string>>;
  pushBubble: (
    kind: "user" | "error" | "system" | "setup",
    text: string,
    images?: string[],
    imagePreviews?: string[],
    display?: unknown,
  ) => void;
  mutateOpenTurn: (fn: (turn: TurnItem) => TurnItem) => void;
  appendDelta: (text: string) => void;
  appendReasoning: (text: string) => void;
  finishTurn: (completion?: TurnCompletion) => void;
  /* Scroll container of the pane showing this sink (ChatPaneView registers
   * it); mutators keep the view pinned to the bottom like bb. */
  registerScrollEl: (el: HTMLDivElement | null) => void;
  scrollDown: (force?: boolean) => void;
  /* Mid-turn markers cleared on reconnect / device switch; history stays. */
  resetTransient: () => void;
  /* Clear a reset/device-switch snapshot, including non-transcript state. */
  reset: () => void;
}

export interface ThreadSink {
  state: ThreadSinkState;
  ops: ThreadSinkOps;
}

/* True while auth_ok / thread_switched / thread_peeked replay the server's
 * live-frame backlog: replayed frames must not restart the elapsed timer
 * (the server's working.since already set it). */
let liveReplay = false;

export function setLiveReplay(v: boolean): void {
  liveReplay = v;
}

export function useThreadSink(): ThreadSink {
  const jobGuidanceReplies = useRef(new Map<string, (reply: Record<string, unknown> | null) => void>());
  const [jobsError, setJobsError] = useState("");
  const [jobs, setJobs] = useState<AgentJob[]>([]);
  const [items, setItems] = useState<TranscriptItem[]>([]);
  const [todos, setTodos] = useState<TodoItem[]>([]);
  const [queue, setQueue] = useState<string[]>([]);
  const [queueIds, setQueueIds] = useState<string[]>([]);
  const queueReturns = useRef<QueueReturns>(new Map());
  const [running, setRunning] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [turnUsage, setTurnUsage] = useState<TurnUsage | null>(null);
  const [currentTool, setCurrentTool] = useState("");
  const [workingSince, setWorkingSince] = useState<number | null>(null);
  const turnOpen = useRef(false);
  const openToolIds = useRef<Set<string>>(new Set());
  const scrollEl = useRef<HTMLDivElement | null>(null);
  const scrollAnchor = useRef<{ element: HTMLDivElement; top: number; follow: boolean } | null>(null);

  const registerScrollEl = useCallback((el: HTMLDivElement | null) => {
    scrollEl.current = el;
    scrollAnchor.current = null;
  }, []);

  const scrollFrame = useRef<number | null>(null);
  const forceScroll = useRef(false);
  const requestedForce = useRef(false);
  const [scrollRequest, setScrollRequest] = useState(0);
  useEffect(() => () => {
    if (scrollFrame.current !== null) window.cancelAnimationFrame(scrollFrame.current);
  }, []);
  const scrollDown = useCallback((force?: boolean) => {
    // Capture attachment before React commits a taller row. Measuring only
    // afterward mistakes content growth for the user leaving the bottom.
    const el = scrollEl.current;
    if (el && !scrollAnchor.current) {
      scrollAnchor.current = {
        element: el, top: el.scrollTop,
        follow: el.scrollHeight - el.scrollTop - el.clientHeight < TRANSCRIPT_BOTTOM_THRESHOLD,
      };
    }
    requestedForce.current ||= !!force;
    setScrollRequest(request => request + 1);
  }, []);
  // Write only after React commits restored/streamed rows. Attachment above
  // is captured against the old geometry; the destination uses the new DOM.
  useLayoutEffect(() => {
    if (!scrollRequest) return;
    forceScroll.current ||= requestedForce.current;
    requestedForce.current = false;
    if (scrollFrame.current !== null) return;
    scrollFrame.current = window.requestAnimationFrame(() => {
      scrollFrame.current = null;
      const forced = forceScroll.current;
      forceScroll.current = false;
      const el = scrollEl.current;
      const anchor = scrollAnchor.current;
      scrollAnchor.current = null;
      if (!el) return;
      const near = el.scrollHeight - el.scrollTop - el.clientHeight < TRANSCRIPT_BOTTOM_THRESHOLD;
      // An upward scroll while the frame was pending cancels automatic
      // following. Never apply another pane's captured attachment.
      const followed = anchor?.element === el && anchor.follow && el.scrollTop >= anchor.top;
      if (forced || near || followed) el.scrollTop = el.scrollHeight;
    });
  }, [scrollRequest]);

  const pushBubble = useCallback(
    (kind: "user" | "error" | "system" | "setup", text: string, images?: string[], imagePreviews?: string[], display?: unknown) => {
      turnOpen.current = false;
      setItems((prev) => [
        ...prev,
        {
          kind,
          key: nextKey(),
          text,
          ...(images?.length ? { images, imagePreviews } : {}),
          /* bb ⋯ menu timestamp: live bubbles only (history has none). */
          time: Date.now(),
          ...parseDisplay(display),
        },
      ]);
      scrollDown(true);
    },
    [scrollDown],
  );

  /* Patch the open assistant turn wherever it sits in the list (approval
   * cards may be appended after it while the turn is still running). The
   * open flag is captured synchronously: setItems updaters run deferred,
   * after later statements may already have flipped the ref. */
  const mutateOpenTurn = useCallback(
    (fn: (turn: TurnItem) => TurnItem) => {
      const open = turnOpen.current;
      turnOpen.current = true;
      setItems((prev) => {
        if (open) {
          for (let i = prev.length - 1; i >= 0; i--) {
            const item = prev[i];
            if (item.kind === "turn") {
              const next = prev.slice();
              next[i] = fn(item);
              return next;
            }
          }
        }
        return [
          ...prev,
          fn({
            kind: "turn",
            key: nextKey(),
            text: "",
            reasoning: "",
            streaming: false,
            streamingReasoning: false,
            tools: [],
            time: Date.now(),
          }),
        ];
      });
      scrollDown(false);
    },
    [scrollDown],
  );

  const appendDelta = useCallback(
    (text: string) => {
      if (!text) return;
      mutateOpenTurn((turn) => ({ ...turn, text: turn.text + text, streaming: true, streamingReasoning: false }));
    },
    [mutateOpenTurn],
  );

  const appendReasoning = useCallback(
    (text: string) => {
      if (!text) return;
      mutateOpenTurn((turn) => ({
        ...turn,
        reasoning: turn.reasoning + text,
        streaming: false,
        streamingReasoning: true,
        /* bb "Thought 46s": first frame → latest frame, client wall clock. */
        reasoningStartedAt: turn.reasoningStartedAt ?? Date.now(),
        reasoningEndedAt: Date.now(),
      }));
    },
    [mutateOpenTurn],
  );

  const finishTurn = useCallback(
    ({ usage, reasoning, files, text, display, unfinishedToolResult }: TurnCompletion = {}) => {
      const endedAt = Date.now();
      mutateOpenTurn((turn) => ({
        ...turn,
        text: turn.text ? turn.text : (text ?? turn.text),
        streaming: false,
        streamingReasoning: false,
        usage: usage ?? turn.usage,
        reasoning: reasoning ? reasoning : turn.reasoning,
        files: files ?? turn.files,
        tools: unfinishedToolResult === undefined ? turn.tools : turn.tools.map(tool => tool.done ? tool : {
          ...tool, done: true, isError: true, doneAt: endedAt,
          result: tool.result ? tool.result + "\n\n" + unfinishedToolResult : unfinishedToolResult,
        }),
        endedAt,
        ...parseDisplay(display),
      }));
      turnOpen.current = false;
    },
    [mutateOpenTurn],
  );

  const resetTransient = useCallback(() => {
    const pendingGuidance = [...jobGuidanceReplies.current.values()];
    jobGuidanceReplies.current.clear();
    pendingGuidance.forEach(reply => reply(null));
    turnOpen.current = false;
    openToolIds.current.clear();
    setCurrentTool("");
    setWorkingSince(null);
    // The socket carrying any in-flight stop request is gone; the user must
    // be able to press stop again after a reconnect.
    setStopping(false);
  }, []);

  const reset = useCallback(() => {
    resetTransient();
    setJobs([]);
    setJobsError("");
    setItems([]);
    setTodos([]);
    setQueue([]);
    setQueueIds([]);
    clearQueueReturns(queueReturns.current);
    setRunning(false);
    setTurnUsage(null);
  }, [resetTransient]);

  const ops = useMemo<ThreadSinkOps>(
    () => ({
      jobGuidanceReplies,
      setJobs,
      setJobsError,
      setItems,
      setTodos,
      setQueue,
      setQueueIds,
      queueReturns,
      setRunning,
      setStopping,
      setTurnUsage,
      setCurrentTool,
      setWorkingSince,
      turnOpen,
      openToolIds,
      pushBubble,
      mutateOpenTurn,
      appendDelta,
      appendReasoning,
      finishTurn,
      registerScrollEl,
      scrollDown,
      resetTransient,
      reset,
    }),
    [
      pushBubble,
      mutateOpenTurn,
      appendDelta,
      appendReasoning,
      finishTurn,
      registerScrollEl,
      scrollDown,
      resetTransient,
      reset,
    ],
  );

  const state = useMemo<ThreadSinkState>(
    () => ({ jobs, jobsError, items, todos, queue, queueIds, running, stopping, turnUsage, currentTool, workingSince }),
    [jobs, jobsError, items, todos, queue, queueIds, running, stopping, turnUsage, currentTool, workingSince],
  );

  return useMemo(() => ({ state, ops }), [state, ops]);
}

/* Approval/ask settle helpers: the server resolves by request id across ALL
 * threads, so panes need no thread tag — they only settle their own card. */
export function decideApprovalIn(
  ops: ThreadSinkOps,
  sendRaw: (payload: Record<string, unknown>) => boolean,
  id: string,
  allow: boolean,
  always: boolean,
): void {
  if (!sendRaw({ op: "approve", id, allow, always })) return;
  ops.setItems((prev) =>
    prev.map((x) =>
      x.kind === "approval" && x.id === id
        ? { ...x, state: always ? "always" : allow ? "approved" : "denied" }
        : x,
    ),
  );
}

export function decideAskIn(
  sendRaw: (payload: Record<string, unknown>) => boolean,
  id: string,
  answer: string,
): void {
  sendRaw({ op: "ask_response", id, answer });
  /* Server broadcasts ask_resolved (turn-scoped) which settles the card. */
}

/* Apply one transcript-scoped frame to a sink. Shared by the page (current
 * thread) and secondary panes (their own thread, fed via the registry or a
 * thread_peeked seed). */
export function applyScopedMessage(
  ops: ThreadSinkOps,
  msg: Record<string, unknown>,
  t: Translator,
): void {
  switch (String(msg.op)) {
    case "job_steered": {
      const id = asString(msg.id);
      const reply = ops.jobGuidanceReplies.current.get(id);
      ops.jobGuidanceReplies.current.delete(id);
      reply?.(msg);
      return;
    }
    case "job_error":
      ops.setJobsError(asString(msg.message) || t("common.error"));
      return;
    case "jobs":
      ops.setJobs((previous) => (Array.isArray(msg.items) ? msg.items : []).flatMap((item) => {
        const job = parseJob(item);
        if (!job) return [];
        const old = previous.find((entry) => entry.id === job.id);
        const raw = item && typeof item === "object" ? item as Record<string, unknown> : {};
        return [{
          ...job,
          detailState: old?.detailState,
          pendingGuidance: old?.pendingGuidance,
          output: typeof raw.output === "string" ? job.output : old?.output,
          result: typeof raw.result === "string" ? job.result : old?.result,
          error: typeof raw.error === "string" ? job.error : old?.error,
        }];
      }));
      return;
    case "job": {
      ops.setJobsError("");
      const job = parseJob(msg.items);
      if (job) job.detailState = job.state;
      if (job) ops.setJobs((previous) => previous.some((entry) => entry.id === job.id)
        ? previous.map((entry) => {
            if (entry.id !== job.id) return entry;
            const raw = msg.items && typeof msg.items === "object" ? msg.items as Record<string, unknown> : {};
            return {
              ...entry,
              ...job,
              output: typeof raw.output === "string" ? job.output : entry.output,
              result: typeof raw.result === "string" ? job.result : entry.result,
              error: typeof raw.error === "string" ? job.error : entry.error,
            };
          })
        : [...previous, job]);
      return;
    }
    case "thread_peeked": {
      /* Read-only snapshot for a secondary pane: history + live frames,
       * without switching the connection's current thread. */
      ops.resetTransient();
      ops.setTodos(parseTodos(msg.todos));
      clearQueueReturns(ops.queueReturns.current);
      ops.setQueue(parseStringArray(msg.queue));
      ops.setQueueIds(parseStringArray(msg.queueIds));
      const running = !!msg.running;
      ops.setRunning(running);
      const peekWorking = parseWorking(msg.working, running);
      ops.setCurrentTool(peekWorking.tool);
      ops.setWorkingSince(peekWorking.since);
      ops.setTurnUsage(parseUsage(msg.usage));
      const history = Array.isArray(msg.history) ? msg.history : [];
      ops.setItems(replayHistory(history));
      ops.scrollDown(true);
      if (Array.isArray(msg.live)) {
        setLiveReplay(true);
        try {
          for (const raw of msg.live) {
            if (raw && typeof raw === "object") {
              applyScopedMessage(ops, raw as Record<string, unknown>, t);
            }
          }
        } finally {
          setLiveReplay(false);
        }
      }
      return;
    }
    case "steering":
    case "turn_start":
      if (msg.op === "steering" && ops.turnOpen.current) {
        ops.mutateOpenTurn(turn => ({ ...turn, streaming: false, streamingReasoning: false }));
      }
      ops.pushBubble("user", asString(msg.text), parseStringArray(msg.images), parseStringArray(msg.imagePreviews), msg.display);
      ops.setRunning(true);
      /* Replayed live frames (reattach mid-turn) must not restart the
       * elapsed timer: working.since from the server already set it. */
      if (!liveReplay && msg.op === "turn_start") ops.setWorkingSince(parseDisplay(msg.display).time ?? Date.now());
      ops.setCurrentTool(/^\/compact(?:\s|$)/i.test(asString(msg.text)) ? "/compact" : "");
      ops.openToolIds.current.clear();
      return;
    case "assistant_start":
      // A model response is a separate message, even when tools keep the
      // enclosing user turn running. Delay creating it until output arrives.
      if (ops.turnOpen.current) {
        ops.mutateOpenTurn(turn => ({ ...turn, streaming: false, streamingReasoning: false }));
      }
      ops.turnOpen.current = false;
      return;
    case "reasoning":
      ops.appendReasoning(asString(msg.text));
      return;
    case "delta":
      ops.appendDelta(asString(msg.text));
      return;
    case "tool_start": {
      const name = asString(msg.name);
      const id = String(msg.id ?? "");
      ops.openToolIds.current.add(id);
      ops.setCurrentTool(name);
      ops.mutateOpenTurn((turn) => ({
        ...turn,
        streaming: false,
        streamingReasoning: false,
        tools: [
          ...turn.tools.filter((x) => x.id !== id),
          {
            id,
            name,
            args: asString(msg.args),
            summary: asString(msg.summary),
            result: "",
            diff: "",
            done: false,
            isError: false,
            startedAt: Date.now(),
          },
        ],
      }));
      return;
    }
    case "tool_end": {
      const id = String(msg.id ?? "");
      ops.openToolIds.current.delete(id);
      if (ops.openToolIds.current.size === 0) ops.setCurrentTool("");
      const result = asString(msg.result);
      const isError = !!msg.isError;
      const diff = asString(msg.diff);
      ops.mutateOpenTurn((turn) => {
        const existing = turn.tools.find((x) => x.id === id);
        if (!existing) {
          return {
            ...turn,
            tools: [
              ...turn.tools,
              { id, name: asString(msg.name), args: "", summary: "", result, diff, done: true, isError, doneAt: Date.now() },
            ],
          };
        }
        return {
          ...turn,
          tools: turn.tools.map((x) =>
            x.id === id ? { ...x, result, diff, done: true, isError, doneAt: Date.now() } : x,
          ),
        };
      });
      return;
    }
    case "approval_request":
      ops.setItems((prev) => [
        ...prev,
        {
          kind: "approval",
          key: nextKey(),
          id: asString(msg.id),
          name: asString(msg.name),
          args: asString(msg.args),
          summary: asString(msg.summary),
          state: "pending",
        },
      ]);
      ops.scrollDown(true);
      return;
    case "ask_request": {
      let options: { label: string; description?: string }[] = [];
      try {
        // options arrives as an already-decoded array (agentMsg.Options is
        // json.RawMessage → re-encoded as a JSON array, not a string).
        const parsed: unknown = Array.isArray(msg.options)
          ? msg.options
          : JSON.parse(asString(msg.options) || "[]");
        if (Array.isArray(parsed)) {
          options = parsed.flatMap((o) => {
            if (typeof o !== "object" || o === null || !("label" in o)) return [];
            const label = o.label;
            if (typeof label !== "string" || !label) return [];
            const description =
              "description" in o && typeof o.description === "string"
                ? o.description
                : undefined;
            return [{ label, description }];
          });
        }
      } catch {
        /* keep empty */
      }
      const open = ops.turnOpen.current;
      ops.setItems((prev) => {
        let owner: { turnKey?: number; toolId?: string } = {};
        if (open) {
          for (let i = prev.length - 1; i >= 0; i--) {
            const turn = prev[i];
            if (turn.kind !== "turn") continue;
            const tool = [...turn.tools].reverse().find(tool => tool.name === "ask" && !tool.done);
            if (tool) owner = { turnKey: turn.key, toolId: tool.id };
            break;
          }
        }
        return [...prev, {
          kind: "ask", key: nextKey(), id: asString(msg.id),
          question: asString(msg.text), options, answer: null, ...owner,
        }];
      });
      ops.scrollDown(true);
      return;
    }
    case "ask_resolved": {
      const id = asString(msg.id);
      const answer = asString(msg.answer);
      ops.setItems((prev) =>
        prev.map((x) =>
          x.kind === "ask" && x.id === id && x.answer === null
            ? { ...x, answer }
            : x,
        ),
      );
      return;
    }
    case "todo":
      ops.setTodos(parseTodos(msg.items));
      return;
    case "dequeued":
      acceptQueueRemoval(ops.queueReturns.current, msg);
      return;
    case "queue":
      ops.setQueue(parseStringArray(msg.items));
      ops.setQueueIds(parseStringArray(msg.queueIds));
      return;
    case "done": {
      const usage = msg.usage;
      const doneUsage = parseUsage(usage);
      if (doneUsage) ops.setTurnUsage(doneUsage);
      if (msg.compacted === true && Array.isArray(msg.history)) {
        ops.setItems(replayHistory(msg.history));
        ops.pushBubble("system", t("agent.compacted"));
        ops.setRunning(false);
        ops.resetTransient();
        ops.scrollDown(true);
        return;
      }
      ops.finishTurn({
        usage: doneUsage ?? undefined,
        reasoning: asString(msg.reasoning) || undefined,
        files: Array.isArray(msg.files) ? parseStringArray(msg.files) : undefined,
        text: asString(msg.text) || undefined,
        display: msg.display,
        unfinishedToolResult: t("agent.toolResultMissing"),
      });
      ops.setRunning(false);
      ops.resetTransient();
      return;
    }
    case "error": {
      const message = asString(msg.message) || t("common.error");
      if (message === "agent is busy" || message === "agent is busy; stop first") {
        // A rejected command does not end the already-running turn.
        ops.setItems((prev) => [
          ...prev,
          { kind: "error", key: nextKey(), text: t("agent.toolError") + ": " + message },
        ]);
        ops.scrollDown(true);
        return;
      } else {
        if (ops.turnOpen.current) ops.finishTurn({ display: msg.display, unfinishedToolResult: message });
        ops.pushBubble("error", t("agent.toolError") + ": " + message);
        if (/api[-_ ]?key/i.test(message)) {
          ops.pushBubble("setup", t("agent.notConfiguredHint"));
        }
      }
      ops.setRunning(false);
      ops.resetTransient();
      return;
    }
    case "stopped": {
      /* A stop of a running turn closes the transcript with a marker; the
       * idle-stop ack (canceled:false, no open turn) is a no-op — calling
       * finishTurn unconditionally would append a phantom empty turn. */
      const wasOpen = ops.turnOpen.current;
      if (wasOpen) {
        ops.finishTurn({ display: msg.display, unfinishedToolResult: t("agent.stopped") });
        ops.pushBubble("system", t("agent.stopped"));
      }
      ops.setRunning(false);
      ops.resetTransient();
      return;
    }
    case "reset_ok":
      ops.reset();
      ops.pushBubble("system", t("agent.resetDone"));
      return;
  }
}
