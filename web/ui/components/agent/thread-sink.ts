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
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type RefObject,
  type SetStateAction,
} from "react";
import {
  asString,
  nextKey,
  parseStringArray,
  parseTodos,
  parseUsage,
  replayHistory,
  type TurnUsage,
} from "./protocol";
import type {
  TodoItem,
  TranscriptItem,
  Translator,
  TurnItem,
} from "./types";

export interface ThreadSinkState {
  items: TranscriptItem[];
  todos: TodoItem[];
  queue: string[];
  running: boolean;
  turnUsage: TurnUsage | null;
  currentTool: string;
  workingSince: number | null;
}

export interface ThreadSinkOps {
  setItems: Dispatch<SetStateAction<TranscriptItem[]>>;
  setTodos: Dispatch<SetStateAction<TodoItem[]>>;
  setQueue: Dispatch<SetStateAction<string[]>>;
  setRunning: Dispatch<SetStateAction<boolean>>;
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
  ) => void;
  mutateOpenTurn: (fn: (turn: TurnItem) => TurnItem) => void;
  appendDelta: (text: string) => void;
  appendReasoning: (text: string) => void;
  finishTurn: (
    usage?: { inputTokens?: number; outputTokens?: number },
    reasoning?: string,
    files?: string[],
    text?: string,
  ) => void;
  /* Scroll container of the pane showing this sink (ChatPaneView registers
   * it); mutators keep the view pinned to the bottom like bb. */
  registerScrollEl: (el: HTMLDivElement | null) => void;
  scrollDown: (force?: boolean) => void;
  /* Mid-turn markers cleared on reconnect / device switch; history stays. */
  resetTransient: () => void;
}

export interface ThreadSink {
  state: ThreadSinkState;
  ops: ThreadSinkOps;
}

export function useThreadSink(): ThreadSink {
  const [items, setItems] = useState<TranscriptItem[]>([]);
  const [todos, setTodos] = useState<TodoItem[]>([]);
  const [queue, setQueue] = useState<string[]>([]);
  const [running, setRunning] = useState(false);
  const [turnUsage, setTurnUsage] = useState<TurnUsage | null>(null);
  const [currentTool, setCurrentTool] = useState("");
  const [workingSince, setWorkingSince] = useState<number | null>(null);
  const turnOpen = useRef(false);
  const openToolIds = useRef<Set<string>>(new Set());
  const scrollEl = useRef<HTMLDivElement | null>(null);

  const registerScrollEl = useCallback((el: HTMLDivElement | null) => {
    scrollEl.current = el;
  }, []);

  const scrollDown = useCallback((force?: boolean) => {
    requestAnimationFrame(() => {
      const el = scrollEl.current;
      if (!el) return;
      const near = el.scrollHeight - el.scrollTop - el.clientHeight < 120;
      if (force || near) el.scrollTop = el.scrollHeight;
    });
  }, []);

  const pushBubble = useCallback(
    (kind: "user" | "error" | "system" | "setup", text: string, images?: string[]) => {
      turnOpen.current = false;
      setItems((prev) => [
        ...prev,
        {
          kind,
          key: nextKey(),
          text,
          ...(images?.length ? { images } : {}),
          /* bb ⋯ menu timestamp: live bubbles only (history has none). */
          time: Date.now(),
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
      mutateOpenTurn((turn) => ({ ...turn, text: turn.text + text, streaming: true }));
    },
    [mutateOpenTurn],
  );

  const appendReasoning = useCallback(
    (text: string) => {
      mutateOpenTurn((turn) => ({
        ...turn,
        reasoning: turn.reasoning + text,
        streamingReasoning: true,
        /* bb "Thought 46s": first frame → latest frame, client wall clock. */
        reasoningStartedAt: turn.reasoningStartedAt ?? Date.now(),
        reasoningEndedAt: Date.now(),
      }));
    },
    [mutateOpenTurn],
  );

  const finishTurn = useCallback(
    (
      usage?: { inputTokens?: number; outputTokens?: number },
      reasoning?: string,
      files?: string[],
      text?: string,
    ) => {
      mutateOpenTurn((turn) => ({
        ...turn,
        text: turn.text ? turn.text : (text ?? turn.text),
        streaming: false,
        streamingReasoning: false,
        usage: usage ?? turn.usage,
        reasoning: reasoning ? reasoning : turn.reasoning,
        files: files ?? turn.files,
        endedAt: Date.now(),
      }));
      turnOpen.current = false;
    },
    [mutateOpenTurn],
  );

  const resetTransient = useCallback(() => {
    turnOpen.current = false;
    openToolIds.current.clear();
    setCurrentTool("");
    setWorkingSince(null);
  }, []);

  const ops = useMemo<ThreadSinkOps>(
    () => ({
      setItems,
      setTodos,
      setQueue,
      setRunning,
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
    ],
  );

  const state = useMemo<ThreadSinkState>(
    () => ({ items, todos, queue, running, turnUsage, currentTool, workingSince }),
    [items, todos, queue, running, turnUsage, currentTool, workingSince],
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
  sendRaw({ op: "approve", id, allow, always });
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
    case "thread_peeked": {
      /* Read-only snapshot for a secondary pane: history + live frames,
       * without switching the connection's current thread. */
      ops.turnOpen.current = false;
      ops.openToolIds.current.clear();
      ops.setTodos(parseTodos(msg.todos));
      ops.setQueue(parseStringArray(msg.queue));
      const running = !!msg.running;
      ops.setRunning(running);
      ops.setCurrentTool("");
      ops.setWorkingSince(running ? Date.now() : null);
      ops.setTurnUsage(parseUsage(msg.usage));
      const history = Array.isArray(msg.history) ? msg.history : [];
      ops.setItems(replayHistory(history));
      ops.scrollDown(true);
      if (Array.isArray(msg.live)) {
        for (const raw of msg.live) {
          if (raw && typeof raw === "object") {
            applyScopedMessage(ops, raw as Record<string, unknown>, t);
          }
        }
      }
      return;
    }
    case "turn_start":
      ops.pushBubble("user", asString(msg.text), parseStringArray(msg.images));
      ops.setRunning(true);
      ops.setWorkingSince(Date.now());
      ops.setCurrentTool("");
      ops.openToolIds.current.clear();
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
      ops.setItems((prev) => [
        ...prev,
        {
          kind: "ask",
          key: nextKey(),
          id: asString(msg.id),
          question: asString(msg.text),
          options,
          answer: null,
        },
      ]);
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
    case "queue":
      ops.setQueue(parseStringArray(msg.items));
      return;
    case "done": {
      const usage = msg.usage;
      const doneUsage = parseUsage(usage);
      if (doneUsage) ops.setTurnUsage(doneUsage);
      ops.finishTurn(
        usage && typeof usage === "object"
          ? {
              inputTokens:
                "inputTokens" in usage && typeof usage.inputTokens === "number"
                  ? usage.inputTokens
                  : undefined,
              outputTokens:
                "outputTokens" in usage && typeof usage.outputTokens === "number"
                  ? usage.outputTokens
                  : undefined,
            }
          : undefined,
        asString(msg.reasoning) || undefined,
        Array.isArray(msg.files) ? parseStringArray(msg.files) : undefined,
        asString(msg.text) || undefined,
      );
      ops.setRunning(false);
      ops.setCurrentTool("");
      ops.openToolIds.current.clear();
      return;
    }
    case "error": {
      const message = asString(msg.message) || t("common.error");
      if (message === "agent is busy" || message === "agent is busy; stop first") {
        ops.pushBubble("error", t("agent.toolError") + ": " + message);
      } else {
        ops.finishTurn();
        ops.pushBubble("error", t("agent.toolError") + ": " + message);
        if (/api[-_ ]?key/i.test(message)) {
          ops.pushBubble("setup", t("agent.notConfiguredHint"));
        }
      }
      ops.setRunning(false);
      ops.setCurrentTool("");
      ops.openToolIds.current.clear();
      return;
    }
    case "stopped":
      ops.finishTurn();
      ops.setRunning(false);
      ops.setCurrentTool("");
      ops.openToolIds.current.clear();
      return;
    case "reset_ok":
      ops.turnOpen.current = false;
      ops.setItems([]);
      ops.pushBubble("system", t("agent.resetDone"));
      return;
  }
}
