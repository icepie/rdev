import { useId, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { Button } from "./button";
import { Icon } from "./icon";
import { cn } from "../lib/utils";

type Props = {
  toolbarStart?: ReactNode;
  focusedPane: number;
  onFocus: (id: number) => void;
  secondMounted: boolean;
  secondOpen: boolean;
  onToggleSecond: () => void;
  label: string;
  paneLabel: (id: number) => string;
  toggleLabel: string;
  dragMime?: string;
  renderPane: (id: number) => ReactNode;
};

// Compact file panes keep their browsers mounted, just like Agent/Terminal
// tabs. Measure workspace space rather than the viewport: the device rail
// can make a wide screen's content too narrow for two usable file browsers.
export function FilesPaneLayout(props: Props) {
  const { focusedPane, onFocus, secondMounted, secondOpen } = props;
  const workspace = useRef<HTMLDivElement>(null);
  const activeTab = useRef<HTMLButtonElement>(null);
  const [compact, setCompact] = useState(false);
  const prefix = useId();
  useLayoutEffect(() => {
    const element = workspace.current;
    if (!element) return;
    const measure = () => setCompact(element.clientWidth > 0 && element.clientWidth < 900);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  useLayoutEffect(() => {
    if (!compact) return;
    activeTab.current?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
    const focused = document.activeElement;
    if (focused instanceof HTMLElement && workspace.current?.contains(focused) && focused.closest('[hidden], [inert]')) {
      activeTab.current?.focus();
    }
  }, [compact, focusedPane, secondOpen]);

  const visible = secondOpen ? [0, 1] : [0];
  return <div ref={workspace} className="flex h-full min-h-0 min-w-0 flex-col">
    <div className={cn("page-toolbar shrink-0", compact && "bg-sidebar")}>
      {props.toolbarStart}
      <div role={compact ? "tablist" : undefined} aria-label={props.label} className="flex min-w-0 gap-1 overflow-x-auto">
        {visible.map((id, index) => <Button key={id} size="sm" variant={focusedPane === id ? "secondary" : "ghost"}
          ref={focusedPane === id ? activeTab : undefined}
          id={`${prefix}-tab-${id}`} role={compact ? "tab" : undefined}
          aria-controls={`${prefix}-pane-${id}`} aria-selected={compact ? focusedPane === id : undefined}
          aria-pressed={!compact ? focusedPane === id : undefined}
          tabIndex={compact && focusedPane !== id ? -1 : 0}
          onClick={() => onFocus(id)}
          onDragEnter={event => {
            if (compact && props.dragMime && event.dataTransfer.types.includes(props.dragMime)) onFocus(id);
          }}
          onKeyDown={event => {
            if (!compact || event.altKey || event.ctrlKey || event.metaKey) return;
            const target = event.key === "ArrowRight" ? (index + 1) % visible.length
              : event.key === "ArrowLeft" ? (index + visible.length - 1) % visible.length
              : event.key === "Home" ? 0 : event.key === "End" ? visible.length - 1 : -1;
            if (target < 0) return;
            event.preventDefault();
            onFocus(visible[target]);
            document.getElementById(`${prefix}-tab-${visible[target]}`)?.focus();
          }}>{props.paneLabel(id)}</Button>)}
      </div>
      <Button variant="ghost" size="sm" className="ml-auto" aria-expanded={secondOpen} onClick={props.onToggleSecond}>
        <Icon name="PanelRight" />{props.toggleLabel}
      </Button>
    </div>
    <div className="flex min-h-0 flex-1">
      {[0, ...(secondMounted ? [1] : [])].map(id => {
        const hidden = (id === 1 && !secondOpen) || (compact && id !== focusedPane);
        return <section key={id} id={`${prefix}-pane-${id}`} role={compact ? "tabpanel" : undefined}
          aria-labelledby={`${prefix}-tab-${id}`} hidden={hidden} inert={hidden}
          className={cn("min-h-0 min-w-0 flex-1 overflow-hidden", hidden && "hidden", id === 1 && !compact && secondOpen && "border-l border-border-seam")}
          onPointerDownCapture={() => onFocus(id)} onFocusCapture={() => onFocus(id)}>
          {props.renderPane(id)}
        </section>;
      })}
    </div>
  </div>;
}
