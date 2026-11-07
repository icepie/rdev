/* Split-screen chat: recursive layout renderer (pane leaves + resizable
 * split handles) and SecondaryChatPane — a pane bound to a thread that is
 * NOT the connection's current one. It seeds its transcript via the
 * read-only peek_thread op and registers its sink ops so the page's WS
 * handler forwards that thread's tagged frames here. */
import {
  Fragment,
  useEffect,
  useId,
  useRef,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from "react";
import { cn } from "../../lib/utils";
import { listPanes } from "../../lib/split-layout";
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
  hidden,
}: {
  hidden?: boolean;
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
      hidden={hidden}
      role="separator"
      aria-orientation={dir === "row" ? "vertical" : "horizontal"}
      className={cn(
        hidden && "hidden",
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

interface CompactPanes {
  activePaneId: string;
  onSelect: (id: string) => void;
  labelForPane: (pane: PaneNode) => string;
  label: string;
}

export interface SplitContainerProps {
  root: LayoutNode;
  renderPane: (pane: PaneNode) => ReactNode;
  onResize: (path: SplitPath, childIndex: number, pairFraction: number) => void;
  compact?: CompactPanes;
}

export function SplitContainer({ root, renderPane, onResize, compact }: SplitContainerProps) {
  const prefix = useId();
  const panes = listPanes(root);
  return <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
    {compact && panes.length > 1 && <div role="tablist" aria-label={compact.label}
      className="flex shrink-0 gap-1 overflow-x-auto border-b border-border-seam bg-sidebar px-2 py-1">
      {panes.map((pane, index) => <button key={pane.paneId} type="button" role="tab"
        id={`${prefix}-tab-${pane.paneId}`} aria-controls={`${prefix}-pane-${pane.paneId}`}
        aria-selected={compact.activePaneId === pane.paneId}
        tabIndex={compact.activePaneId === pane.paneId ? 0 : -1}
        onClick={() => compact.onSelect(pane.paneId)}
        onKeyDown={event => {
          let target = index;
          if (event.key === "ArrowRight") target = (index + 1) % panes.length;
          else if (event.key === "ArrowLeft") target = (index + panes.length - 1) % panes.length;
          else if (event.key === "Home") target = 0;
          else if (event.key === "End") target = panes.length - 1;
          else return;
          event.preventDefault();
          compact.onSelect(panes[target].paneId);
          document.getElementById(`${prefix}-tab-${panes[target].paneId}`)?.focus();
        }}
        className={cn("min-h-10 max-w-52 shrink-0 truncate rounded-md px-3 text-xs transition-colors",
          compact.activePaneId === pane.paneId ? "bg-state-active text-foreground" : "text-muted-foreground hover:bg-state-hover")}
        title={compact.labelForPane(pane)}>{compact.labelForPane(pane)}</button>)}
    </div>}
    <NodeView node={root} path={[]} renderPane={renderPane} onResize={onResize} compact={compact} prefix={prefix} />
  </div>;
}

function NodeView({
  node,
  path,
  renderPane,
  onResize,
  compact,
  prefix,
}: {
  compact?: CompactPanes;
  prefix: string;
  node: LayoutNode;
  path: SplitPath;
  renderPane: (pane: PaneNode) => ReactNode;
  onResize: (path: SplitPath, childIndex: number, pairFraction: number) => void;
}) {
  if (node.type === "pane") {
    /* data-pane-id: the thread drag session locates drop targets and their
     * bounding rects through it. */
    return (
      <div data-pane-id={node.paneId} id={`${prefix}-pane-${node.paneId}`}
        role={compact ? "tabpanel" : undefined} aria-labelledby={compact ? `${prefix}-tab-${node.paneId}` : undefined}
        hidden={!!compact && compact.activePaneId !== node.paneId}
        inert={!!compact && compact.activePaneId !== node.paneId}
        className={cn("min-h-0 min-w-0 flex-1 flex-col overflow-hidden", compact && compact.activePaneId !== node.paneId ? "hidden" : "flex")}>
        {renderPane(node)}
      </div>
    );
  }
  return (
    <div
      className={cn(
        compact ? "contents" : "flex min-h-0 min-w-0 flex-1",
        !compact && (node.dir === "row" ? "flex-row" : "flex-col"),
      )}
    >
      {node.children.map((child, i) => (
        <Fragment key={child.type === "pane" ? child.paneId : i}>
          {i > 0 && (
            <SplitHandle
              dir={node.dir}
              hidden={!!compact}
              onDrag={(f) => onResize(path, i - 1, f)}
            />
          )}
          <div
            style={{ flexGrow: node.sizes[i] ?? 1, flexBasis: 0 }}
            className={compact ? "contents" : "flex min-h-0 min-w-0 flex-col overflow-hidden"}
          >
            <NodeView node={child} path={[...path, i]} renderPane={renderPane} onResize={onResize} compact={compact} prefix={prefix} />
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
