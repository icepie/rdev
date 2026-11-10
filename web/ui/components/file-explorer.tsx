import { useEffect, useMemo, useState, type DragEvent } from "react";
import { Icon } from "./icon";
import { Input } from "./input";
import { Button } from "./button";
import {
  ContextMenu,
  ContextMenuTrigger,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
} from "./context-menu";
import { Dialog, DialogContent, DialogTitle } from "./dialog";
import { SecondaryPanelContentSkeleton } from "./loading-skeletons";
import { resolveFileIconName } from "./agent/file-visuals";
import type { Translator } from "./agent/types";
import type { TreeEntry } from "../lib/file-tree-rpc";

type Operation = "mkdir" | "rename" | "delete" | "newFile";
type Props = {
  entries: TreeEntry[];
  selected?: string;
  load: (path: string) => Promise<TreeEntry[]>;
  onOpen: (path: string) => void;
  onNavigate: (path: string) => void;
  onDragStart?: (entry: TreeEntry, event: DragEvent<HTMLButtonElement>) => void;
  onDownload?: (entry: TreeEntry) => void;
  onOperation?: (
    op: Operation,
    entry: TreeEntry,
    name: string,
  ) => Promise<void>;
  t: Translator;
};
type TreeState = { expanded: string[]; selected: string };
function readState(key: string): TreeState {
  try {
    const v = JSON.parse(sessionStorage.getItem(key) || "null");
    if (v && Array.isArray(v.expanded))
      return {
        expanded: v.expanded
          .filter((p: unknown) => typeof p === "string")
          .slice(0, 500),
        selected: typeof v.selected === "string" ? v.selected : "",
      };
  } catch {}
  return { expanded: [], selected: "" };
}
export function relativeTreePath(root: string, path: string) {
  const a = root.replace(/\\/g, "/").replace(/\/$/, "");
  const b = path.replace(/\\/g, "/");
  return b.startsWith(a + "/") ? b.slice(a.length + 1) : path;
}
type Shared = Omit<Props, "entries"> & {
  root: string;
  query: string;
  state: TreeState;
  setState: (update: (state: TreeState) => TreeState) => void;
  mutate: (op: Operation, entry: TreeEntry) => void;
  report: (error: string) => void;
};
function Branch({
  entry,
  depth,
  ...props
}: Shared & { entry: TreeEntry; depth: number }) {
  const expanded = props.state.expanded.includes(entry.path);
  const [children, setChildren] = useState<TreeEntry[] | null>(null);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  useEffect(() => {
    if (!expanded || children) return;
    let canceled = false;
    setError("");
    void props
      .load(entry.path)
      .then((rows) => {
        if (!canceled) setChildren(rows);
      })
      .catch((e) => {
        if (!canceled) setError(String(e));
      });
    return () => {
      canceled = true;
    };
  }, [expanded, children, props.load, entry.path, retry]);
  const toggle = (open: boolean) =>
    props.setState((s) => ({
      ...s,
      expanded: open
        ? [...s.expanded.filter((p) => p !== entry.path), entry.path].slice(
            -500,
          )
        : s.expanded.filter((p) => p !== entry.path),
    }));
  const copy = (value: string) =>
    void navigator.clipboard
      .writeText(value)
      .catch((e) => props.report(String(e)));
  return (
    <li
      className="group/tree-row relative"
      role="treeitem"
      aria-expanded={entry.isDir ? expanded : undefined}
      aria-selected={entry.path === props.selected}
    >
      <ContextMenu>
        <ContextMenuTrigger asChild>
          <button
            type="button"
            title={entry.name}
            draggable={!!props.onDragStart}
            onDragStart={(e) => props.onDragStart?.(entry, e)}
            style={{ paddingLeft: 8 + depth * 14 }}
            className={`flex h-7 w-full items-center gap-1.5 pr-2 text-left text-xs hover:bg-state-hover focus-visible:outline focus-visible:outline-ring ${entry.path === props.selected ? "bg-state-active text-foreground" : "text-muted-foreground"}`}
            onClick={() => {
              if (entry.isDir) toggle(!expanded);
              else {
                props.setState((s) => ({ ...s, selected: entry.path }));
                props.onOpen(entry.path);
              }
            }}
            onDoubleClick={() => {
              if (entry.isDir) props.onNavigate(entry.path);
            }}
            onKeyDown={(e) => {
              const tree = e.currentTarget.closest('[role="tree"]');
              const rows = Array.from(
                tree?.querySelectorAll<HTMLButtonElement>(
                  '[role="treeitem"] > button[title]',
                ) ?? [],
              );
              if (["ArrowDown", "ArrowUp", "Home", "End"].includes(e.key)) {
                e.preventDefault();
                const n =
                  e.key === "Home"
                    ? 0
                    : e.key === "End"
                      ? rows.length - 1
                      : rows.indexOf(e.currentTarget) +
                        (e.key === "ArrowDown" ? 1 : -1);
                rows[n]?.focus();
              }
              if (e.key === "ArrowRight" && entry.isDir) {
                e.preventDefault();
                if (!expanded) toggle(true);
                else rows[rows.indexOf(e.currentTarget) + 1]?.focus();
              }
              if (e.key === "ArrowLeft") {
                e.preventDefault();
                if (entry.isDir && expanded) toggle(false);
                else
                  e.currentTarget.parentElement?.parentElement
                    ?.closest('[role="treeitem"]')
                    ?.querySelector<HTMLButtonElement>(":scope > button")
                    ?.focus();
              }
            }}
          >
            <Icon
              name={
                entry.isDir
                  ? expanded
                    ? "ChevronDown"
                    : "ChevronRight"
                  : resolveFileIconName(entry.path)
              }
              className="size-3.5 shrink-0"
            />
            {entry.isDir && (
              <Icon name="Folder" className="size-3.5 shrink-0" />
            )}
            <span className="truncate">{entry.name}</span>
          </button>
        </ContextMenuTrigger>
        <ContextMenuContent>
          <ContextMenuItem onSelect={() => copy(entry.path)}>
            {props.t("files.copyAbsolutePath")}
          </ContextMenuItem>
          <ContextMenuItem
            onSelect={() => copy(relativeTreePath(props.root, entry.path))}
          >
            {props.t("files.copyRelativePath")}
          </ContextMenuItem>
          <ContextMenuItem onSelect={() => copy(entry.name)}>
            {props.t("files.copyFilename")}
          </ContextMenuItem>
          {entry.isDir && (
            <ContextMenuItem onSelect={() => props.onNavigate(entry.path)}>
              {props.t("files.openFolder")}
            </ContextMenuItem>
          )}
          {!entry.isDir && props.onDownload && (
            <ContextMenuItem onSelect={() => props.onDownload?.(entry)}>
              {props.t("files.download")}
            </ContextMenuItem>
          )}
          {props.onOperation && (
            <>
              <ContextMenuSeparator />
              {entry.isDir && (
                <>
                  <ContextMenuItem
                    onSelect={() => props.mutate("mkdir", entry)}
                  >
                    {props.t("files.newFolder")}
                  </ContextMenuItem>
                  <ContextMenuItem
                    onSelect={() => props.mutate("newFile", entry)}
                  >
                    {props.t("files.newFile")}
                  </ContextMenuItem>
                </>
              )}
              <ContextMenuItem onSelect={() => props.mutate("rename", entry)}>
                {props.t("files.rename")}
              </ContextMenuItem>
              <ContextMenuItem
                className="text-destructive-text"
                onSelect={() => props.mutate("delete", entry)}
              >
                {props.t("files.delete")}
              </ContextMenuItem>
            </>
          )}
        </ContextMenuContent>
      </ContextMenu>
      {!entry.isDir && props.onDownload && (
        <button
          type="button"
          aria-label={props.t("files.download") + " " + entry.name}
          className="absolute right-1 top-0.5 flex size-6 items-center justify-center rounded bg-background text-muted-foreground opacity-0 group-hover/tree-row:opacity-100 focus:opacity-100 pointer-coarse:opacity-100"
          onClick={() => props.onDownload?.(entry)}
        >
          <Icon name="Download" className="size-3.5" />
        </button>
      )}
      {entry.isDir &&
        expanded &&
        (children ? (
          <Nodes {...props} entries={children} depth={depth + 1} />
        ) : error ? (
          <div className="p-2 text-xs text-destructive-text" role="alert">
            {error}
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setRetry((n) => n + 1)}
            >
              {props.t("common.retry")}
            </Button>
          </div>
        ) : (
          <SecondaryPanelContentSkeleton label={props.t("common.loading")} />
        ))}
    </li>
  );
}
function Nodes({
  entries,
  depth,
  ...props
}: Shared & { entries: TreeEntry[]; depth: number }) {
  const [limit, setLimit] = useState(200);
  const rows = useMemo(
    () =>
      [...entries]
        .filter(
          (e) =>
            e.isDir ||
            !props.query ||
            e.name.toLowerCase().includes(props.query.toLowerCase()),
        )
        .sort(
          (a, b) =>
            Number(!!b.isDir) - Number(!!a.isDir) ||
            a.name.localeCompare(b.name, undefined, { numeric: true }),
        ),
    [entries, props.query],
  );
  return (
    <ul
      role={depth ? "group" : "tree"}
      aria-label={depth ? undefined : props.t("files.explorer")}
    >
      {rows.slice(0, limit).map((entry) => (
        <Branch key={entry.path} entry={entry} depth={depth} {...props} />
      ))}
      {rows.length > limit && (
        <li role="none">
          <button
            className="w-full p-2 text-xs text-muted-foreground"
            onClick={() => setLimit((n) => n + 200)}
          >
            {props.t("files.loadMore")}
          </button>
        </li>
      )}
    </ul>
  );
}
export function FileExplorer(
  props: Props & {
    path: string;
    notice?: string;
    stateKey?: string;
    loading?: boolean;
    refreshKey?: number;
  },
) {
  const storageKey = JSON.stringify([
    "rdev-file-tree",
    props.stateKey ?? "",
    props.path,
  ]);
  const [state, setState] = useState(() => readState(storageKey));
  const [query, setQuery] = useState("");
  const [revision, setRevision] = useState(0);
  const [error, setError] = useState("");
  const [operation, setOperation] = useState<{
    op: Operation;
    entry: TreeEntry;
  } | null>(null);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    setState(readState(storageKey));
    setQuery("");
    setOperation(null);
  }, [storageKey]);
  const update = (fn: (state: TreeState) => TreeState) =>
    setState((previous) => {
      const next = fn(previous);
      try {
        sessionStorage.setItem(storageKey, JSON.stringify(next));
      } catch {}
      return next;
    });
  const mutate = (op: Operation, entry: TreeEntry) => {
    setOperation({ op, entry });
    setName(op === "rename" ? entry.name : "");
    setError("");
  };
  const submit = async () => {
    if (!operation || !props.onOperation || busy) return;
    if (
      operation.op !== "delete" &&
      (!name.trim() || /[\\/]/.test(name) || [".", ".."].includes(name))
    ) {
      setError(props.t("files.invalidName"));
      return;
    }
    setBusy(true);
    try {
      if (operation.op === "newFile") {
        const existing = await props.load(operation.entry.path);
        if (
          existing.some(
            (entry) =>
              entry.name.toLocaleLowerCase() === name.toLocaleLowerCase(),
          )
        )
          throw new Error(props.t("files.nameExists"));
      }
      await props.onOperation(operation.op, operation.entry, name);
      setRevision((n) => n + 1);
      setOperation(null);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="flex min-h-0 flex-1 flex-col bg-background">
      <div className="flex shrink-0 items-center gap-1 border-b border-border-seam p-2">
        <Input
          aria-label={props.t("files.filterLoaded")}
          placeholder={props.t("files.filterLoaded")}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          className="h-7 min-w-0 flex-1 border-0 bg-surface-recessed text-xs"
        />
        {props.onOperation && (
          <Button
            size="icon"
            variant="ghost"
            className="size-7"
            aria-label={props.t("files.newFolder")}
            onClick={() =>
              mutate("mkdir", {
                path: props.path,
                name: props.path,
                isDir: true,
              })
            }
          >
            <Icon name="Plus" className="size-3.5" />
          </Button>
        )}
      </div>
      {error && !operation && (
        <p role="alert" className="px-2 text-xs text-destructive-text">
          {error}
        </p>
      )}
      <div className="min-h-0 flex-1 overflow-auto">
        {props.loading ? (
          <SecondaryPanelContentSkeleton label={props.t("common.loading")} />
        ) : props.notice ? (
          <p role="status" className="p-3 text-xs text-muted-foreground">
            {props.notice}
          </p>
        ) : props.entries.length === 0 ? (
          <p className="p-3 text-xs text-muted-foreground">
            {props.t("files.emptyDir")}
          </p>
        ) : (
          <Nodes
            key={`${storageKey}:${revision}:${props.refreshKey ?? 0}`}
            {...props}
            root={props.path}
            depth={0}
            query={query}
            state={state}
            setState={update}
            selected={props.selected ?? state.selected}
            mutate={mutate}
            report={setError}
          />
        )}
      </div>
      <Dialog
        open={operation !== null}
        onOpenChange={(open) => {
          if (!open && !busy) setOperation(null);
        }}
      >
        <DialogContent className="max-w-sm">
          <DialogTitle>
            {props.t(
              `files.${operation?.op === "mkdir" ? "newFolder" : (operation?.op ?? "rename")}`,
            )}
          </DialogTitle>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void submit();
            }}
            className="space-y-3"
          >
            <p className="break-all text-xs text-muted-foreground">
              {operation?.entry.path}
            </p>
            {operation?.op === "delete" ? (
              <p className="text-sm">{props.t("files.confirmDeleteTree")}</p>
            ) : (
              <Input
                autoFocus
                aria-label={props.t("files.name")}
                value={name}
                onChange={(e) => setName(e.target.value)}
                disabled={busy}
              />
            )}
            {error && (
              <p role="alert" className="text-xs text-destructive-text">
                {error}
              </p>
            )}
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="ghost"
                disabled={busy}
                onClick={() => setOperation(null)}
              >
                {props.t("common.cancel")}
              </Button>
              <Button type="submit" disabled={busy}>
                {props.t(busy ? "common.loading" : "common.confirm")}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}
