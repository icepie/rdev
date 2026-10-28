/* Split-screen chat: recursive layout renderer (pane leaves + resizable
 * split handles) and SecondaryChatPane — a pane bound to a thread that is
 * NOT the connection's current one. It seeds its transcript via the
 * read-only peek_thread op and registers its sink ops so the page's WS
 * handler forwards that thread's tagged frames here. */
import {
  Fragment,
  useEffect,
  useRef,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from "react";
import { cn } from "../../lib/utils";
import type {
  LayoutNode,
  PaneNode,
  SplitPath,
} from "../../lib/split-layout";
import { useThreadSink, type ThreadSinkOps } from "./thread-sink";
import { ChatPaneView } from "./chat-pane";
import type { ChatPaneViewProps } from "./chat-pane";

/* ── Resize handle between two split children ── */

function SplitHandle({
  dir,
  onDrag,
}: {
  dir: "row" | "col";
  /* fraction of the PAIR occupied by the first child (0.15–0.85). */
  onDrag: (pairFraction: number) => void;
}) {
  const session = useRef<{
    start: number;
    size: number;
    baseFraction: number;
  } | null>(null);

  const onPointerDown = (e: ReactPointerEvent<HTMLSpanElement>) => {
    const parent = e.currentTarget.closest('[role="separator"]')?.parentElement;
    if (!parent) return;
    const rect = parent.getBoundingClientRect();
    e.currentTarget.setPointerCapture(e.pointerId);
    session.current = {
      start: dir === "row" ? e.clientX : e.clientY,
      size: dir === "row" ? rect.width : rect.height,
      baseFraction: NaN, // set on first move from the live DOM siblings
    };
  };

  const onPointerMove = (e: ReactPointerEvent<HTMLSpanElement>) => {
    const s = session.current;
    const handle = e.currentTarget.closest('[role="separator"]');
    if (!s || !handle) return;
    /* The two siblings around this handle carry flexGrow = their split
     * sizes; their rendered pixel extents give the live pair fraction. */
    const prev = handle.previousElementSibling;
    const next = handle.nextElementSibling;
    if (!prev || !next) return;
    const prevRect = prev.getBoundingClientRect();
    const nextRect = next.getBoundingClientRect();
    const pairPx =
      dir === "row"
        ? prevRect.width + nextRect.width
        : prevRect.height + nextRect.height;
    if (pairPx <= 0 || s.size <= 0) return;
    const base = dir === "row" ? prevRect.width / pairPx : prevRect.height / pairPx;
    const delta =
      ((dir === "row" ? e.clientX : e.clientY) - s.start) / pairPx;
    onDrag(base + delta);
    session.current = { ...s, start: dir === "row" ? e.clientX : e.clientY };
  };

  const onPointerUp = (e: ReactPointerEvent<HTMLSpanElement>) => {
    session.current = null;
    e.currentTarget.releasePointerCapture(e.pointerId);
  };

  return (
    <span
      role="separator"
      aria-orientation={dir === "row" ? "vertical" : "horizontal"}
      className={cn(
        "relative z-10 shrink-0 bg-border-seam transition-colors hover:bg-ring/40 active:bg-ring/40",
        dir === "row" ? "w-px cursor-col-resize" : "h-px cursor-row-resize",
      )}
    >
      <span
        aria-hidden
        className={cn(
          "absolute z-10 touch-none bg-transparent",
          dir === "row"
            ? "top-0 -left-1.5 h-full w-3 cursor-col-resize"
            : "-top-1.5 left-0 h-3 w-full cursor-row-resize",
        )}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerUp}
      />
    </span>
  );
}

/* ── Recursive layout renderer ── */

export interface SplitContainerProps {
  root: LayoutNode;
  renderPane: (pane: PaneNode) => ReactNode;
  onResize: (path: SplitPath, childIndex: number, pairFraction: number) => void;
}

export function SplitContainer({ root, renderPane, onResize }: SplitContainerProps) {
  return <NodeView node={root} path={[]} renderPane={renderPane} onResize={onResize} />;
}

function NodeView({
  node,
  path,
  renderPane,
  onResize,
}: {
  node: LayoutNode;
  path: SplitPath;
  renderPane: (pane: PaneNode) => ReactNode;
  onResize: (path: SplitPath, childIndex: number, pairFraction: number) => void;
}) {
  if (node.type === "pane") {
    /* data-pane-id: the thread drag session locates drop targets and their
     * bounding rects through it. */
    return (
      <div data-pane-id={node.paneId} className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
        {renderPane(node)}
      </div>
    );
  }
  return (
    <div
      className={cn(
        "flex min-h-0 min-w-0 flex-1",
        node.dir === "row" ? "flex-row" : "flex-col",
      )}
    >
      {node.children.map((child, i) => (
        <Fragment key={child.type === "pane" ? child.paneId : i}>
          {i > 0 && (
            <SplitHandle
              dir={node.dir}
              onDrag={(f) => onResize(path, i - 1, f)}
            />
          )}
          <div
            style={{ flexGrow: node.sizes[i] ?? 1, flexBasis: 0 }}
            className="flex min-h-0 min-w-0 flex-col overflow-hidden"
          >
            <NodeView node={child} path={[...path, i]} renderPane={renderPane} onResize={onResize} />
          </div>
        </Fragment>
      ))}
    </div>
  );
}

/* ── Secondary pane (any thread except the connection's current) ── */

export interface SecondaryChatPaneProps
  extends Omit<ChatPaneViewProps, "sink" | "threadId"> {
  threadId: string;
  register: (threadId: string, ops: ThreadSinkOps) => () => void;
}

export function SecondaryChatPane({
  threadId,
  register,
  ready,
  sendRaw,
  ...rest
}: SecondaryChatPaneProps) {
  const sink = useThreadSink();

  /* Register ops for frame forwarding; re-register when the pane's thread
   * changes (a drag replace) or the sink identity flips (never — ops are
   * memoized). */
  useEffect(() => register(threadId, sink.ops), [register, threadId, sink.ops]);

  /* Seed history once connected (and re-seed after every reconnect: auth_ok
   * clears secondary sinks page-side). */
  useEffect(() => {
    if (ready) sendRaw({ op: "peek_thread", thread: threadId });
  }, [ready, threadId, sendRaw]);

  return (
    <ChatPaneView
      sink={sink}
      threadId={threadId}
      ready={ready}
      sendRaw={sendRaw}
      {...rest}
    />
  );
}
