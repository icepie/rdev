import { useEffect, useMemo, useState, type DragEvent } from "react";
import { Icon } from "./icon";
import { resolveFileIconName } from "./agent/file-visuals";
import type { Translator } from "./agent/types";

type Entry = { path: string; name: string; isDir?: boolean; size?: number };
type Props = { entries: Entry[]; selected?: string; load: (path: string) => Promise<Entry[]>; onOpen: (path: string) => void; onNavigate: (path: string) => void; onDragStart?: (entry: Entry, event: DragEvent<HTMLButtonElement>) => void; t: Translator };

function Branch({ entry, depth, ...props }: Omit<Props, 'entries'> & { entry: Entry; depth: number }) {
  const [expanded, setExpanded] = useState(false);
  const [children, setChildren] = useState<Entry[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!expanded || children) return;
    let canceled = false;
    setError("");
    void props.load(entry.path).then(rows => { if (!canceled) setChildren(rows); }).catch(error => { if (!canceled) setError(String(error)); });
    return () => { canceled = true; };
  }, [expanded, children, props.load, entry.path]);
  return <li role="treeitem" aria-expanded={entry.isDir ? expanded : undefined} aria-selected={entry.path === props.selected}>
    <button title={entry.name} draggable={!!props.onDragStart} onDragStart={event => props.onDragStart?.(entry, event)} style={{ paddingLeft: 8 + depth * 14 }}
      className={`flex h-8 w-full items-center gap-1.5 pr-2 text-left text-xs hover:bg-state-hover ${entry.path === props.selected ? "bg-state-hover text-foreground" : "text-muted-foreground"}`}
      onClick={() => entry.isDir ? setExpanded(value => !value) : props.onOpen(entry.path)}
      onDoubleClick={() => { if (entry.isDir) props.onNavigate(entry.path); }}
      onKeyDown={event => {
        if (event.key === "ArrowDown" || event.key === "ArrowUp") {
          const rows = Array.from(event.currentTarget.closest('[role="tree"]')?.querySelectorAll<HTMLButtonElement>('[role="treeitem"] > button') ?? []);
          const next = rows.indexOf(event.currentTarget) + (event.key === "ArrowDown" ? 1 : -1);
          event.preventDefault(); rows[next]?.focus();
        }
        if (entry.isDir && (event.key === "ArrowRight" || event.key === "ArrowLeft")) { event.preventDefault(); setExpanded(event.key === "ArrowRight"); }
      }}>
      <Icon name={entry.isDir ? expanded ? "ChevronDown" : "ChevronRight" : resolveFileIconName(entry.path)} className="size-3.5 shrink-0" />
      {entry.isDir && <Icon name="Folder" className="size-3.5 shrink-0" />}<span className="truncate">{entry.name}</span>
    </button>
    {entry.isDir && expanded && (children ? <Nodes {...props} entries={children} depth={depth + 1} /> : <div className="px-4 py-2 text-xs text-muted-foreground" role="status">{error || props.t("common.loading")}</div>)}
  </li>;
}
function Nodes({ entries, depth, ...props }: Props & { depth: number }) {
  const [limit, setLimit] = useState(200);
  const sorted = useMemo(() => [...entries].sort((a, b) => Number(!!b.isDir) - Number(!!a.isDir) || a.name.localeCompare(b.name, undefined, { numeric: true })), [entries]);
  return <ul role={depth ? "group" : "tree"} aria-label={depth ? undefined : props.t("files.explorer")}>
    {sorted.slice(0, limit).map(entry => <Branch key={entry.path} entry={entry} depth={depth} {...props} />)}
    {sorted.length > limit && <li role="none"><button className="w-full p-2 text-xs text-muted-foreground" onClick={() => setLimit(limit + 200)}>{props.t("files.loadMore")}</button></li>}
  </ul>;
}
export function FileExplorer(props: Props & { path: string; notice?: string }) {
  return <div className="flex min-h-0 flex-1 flex-col bg-sidebar">
    <div className="truncate border-b border-border-seam px-3 py-2 text-xs text-muted-foreground" title={props.path}>{props.path || "…"}</div>
    <div className="min-h-0 flex-1 overflow-auto">
      {props.notice ? <p role="status" className="p-3 text-xs text-muted-foreground">{props.notice}</p> : props.entries.length === 0 ? <p className="p-3 text-xs text-muted-foreground">{props.t("files.emptyDir")}</p> : <Nodes {...props} depth={0} />}
    </div>
  </div>;
}
