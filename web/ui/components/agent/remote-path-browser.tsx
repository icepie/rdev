import { useEffect, useRef, useState } from "react";
import { cn } from "../../lib/utils";
import { Icon } from "../icon";
import { joinPath, normalizePathInput, toBreadcrumb } from "./path-utils";
import type { Translator } from "./types";

/* Port of bb apps/app/src/components/dialogs/RemotePathBrowser.tsx: the
 * directory browser inside the create-project dialog. rdev deviations:
 *  - entries render as a plain list (bb virtualizes with
 *    @tanstack/react-virtual; the rdev server caps listings at 2000)
 *  - directory data comes from the agent WS dir_list/dir_mkdir ops instead
 *    of bb's REST host directory/mkdir endpoints (props injected)
 *  - entry order is the server's (bb also keeps server order)
 * ClassNames follow bb verbatim. */

export interface DirEntry {
  name: string;
  path: string;
  dir: boolean;
}

export interface DirListing {
  parent: string;
  entries: DirEntry[];
  roots: string[];
}

export function RemotePathBrowser({
  initialPath,
  allowCreateFolder,
  onDirectoryChange,
  disabled,
  listDir,
  mkdir,
  t,
}: {
  initialPath: string;
  allowCreateFolder: boolean;
  onDirectoryChange: (path: string | null) => void;
  disabled?: boolean;
  listDir: (path: string) => Promise<DirListing>;
  mkdir: (path: string) => Promise<void>;
  t: Translator;
}) {
  const [current, setCurrent] = useState(initialPath);
  const [parent, setParent] = useState("");
  const [entries, setEntries] = useState<DirEntry[]>([]);
  const [roots, setRoots] = useState<string[]>([]);
  const [status, setStatus] = useState<"loading" | "ready" | "error">(
    "loading",
  );
  const [editing, setEditing] = useState(false);
  const [editDraft, setEditDraft] = useState("");
  const [creating, setCreating] = useState(false);
  const [folderName, setFolderName] = useState("");
  const [folderErr, setFolderErr] = useState("");
  const [folderBusy, setFolderBusy] = useState(false);
  /* Stale-response guard: rapid navigation must not let an older reply
   * clobber a newer directory. */
  const seqRef = useRef(0);
  const onDirectoryChangeRef = useRef(onDirectoryChange);
  onDirectoryChangeRef.current = onDirectoryChange;

  const navigate = (path: string) => {
    const seq = ++seqRef.current;
    setStatus("loading");
    setEditing(false);
    onDirectoryChangeRef.current(null);
    listDir(path)
      .then((listing) => {
        if (seqRef.current !== seq) return;
        setCurrent(path);
        setParent(listing.parent);
        setEntries(listing.entries);
        setRoots(listing.roots);
        setStatus("ready");
        onDirectoryChangeRef.current(path);
      })
      .catch(() => {
        if (seqRef.current !== seq) return;
        setCurrent(path);
        setEntries([]);
        setStatus("error");
      });
  };
  const navigateRef = useRef(navigate);
  navigateRef.current = navigate;

  useEffect(() => {
    navigateRef.current(initialPath);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialPath]);

  const crumbs = toBreadcrumb(current);

  const commitEditor = () => {
    const normalized = normalizePathInput(editDraft);
    if (normalized) navigate(normalized);
    else setEditing(false);
  };

  const submitFolder = () => {
    const name = folderName.trim();
    if (!name) {
      setFolderErr(t("agent.folderNameRequired"));
      return;
    }
    if (/[/\\]/.test(name)) {
      setFolderErr(t("agent.folderNameNoSlashes"));
      return;
    }
    const path = joinPath(current, name);
    setFolderBusy(true);
    setFolderErr("");
    mkdir(path)
      .then(() => {
        setCreating(false);
        setFolderName("");
        navigate(path);
      })
      .catch(() => setFolderErr(t("agent.folderCreateFailed")))
      .finally(() => setFolderBusy(false));
  };

  const uiDisabled = disabled || status === "loading";

  return (
    <div className="flex flex-col rounded-md border">
      <div className="flex items-center gap-1 border-b px-1.5 py-1">
        <button
          type="button"
          aria-label={t("agent.goToParentFolder")}
          disabled={uiDisabled || !parent || parent === current}
          onClick={() => navigate(parent)}
          className="inline-flex size-7 shrink-0 cursor-pointer items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground disabled:pointer-events-none disabled:opacity-50"
        >
          <Icon name="ArrowUp" className="size-4" />
        </button>
        {editing ? (
          <input
            autoFocus
            value={editDraft}
            aria-label={t("agent.projectPath")}
            disabled={disabled}
            onChange={(e) => setEditDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                commitEditor();
              } else if (e.key === "Escape") {
                setEditing(false);
              }
            }}
            className="h-7 flex-1 rounded-md border border-input bg-transparent px-2 text-xs outline-none focus-visible:ring-1 focus-visible:ring-ring"
          />
        ) : (
          <div className="flex min-w-0 flex-1 items-center overflow-x-auto whitespace-nowrap text-xs text-muted-foreground">
            {crumbs.map((crumb, i) => (
              <span key={crumb.path} className="flex items-center">
                {i > 0 && <span className="px-0.5 opacity-50">/</span>}
                <button
                  type="button"
                  disabled={uiDisabled}
                  onClick={() => navigate(crumb.path)}
                  className={cn(
                    "cursor-pointer rounded px-1 py-0.5 hover:bg-muted hover:text-foreground disabled:pointer-events-none",
                    i === crumbs.length - 1 && "font-medium text-foreground",
                  )}
                >
                  {crumb.label}
                </button>
              </span>
            ))}
            {crumbs.length === 0 && roots.length > 0 && (
              <span className="px-1 py-0.5">{roots[0]}</span>
            )}
          </div>
        )}
        {allowCreateFolder && !creating && (
          <button
            type="button"
            aria-label={t("agent.newFolder")}
            title={t("agent.newFolder")}
            disabled={uiDisabled}
            onClick={() => {
              setCreating(true);
              setFolderName("");
              setFolderErr("");
            }}
            className="inline-flex size-7 shrink-0 cursor-pointer items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground disabled:pointer-events-none disabled:opacity-50"
          >
            <Icon name="FolderPlus" className="size-4" />
          </button>
        )}
        {!editing ? (
          <button
            type="button"
            aria-label={t("agent.editPath")}
            title={t("agent.editPath")}
            disabled={disabled}
            onClick={() => {
              setEditDraft(current);
              setEditing(true);
            }}
            className="inline-flex size-7 shrink-0 cursor-pointer items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground disabled:pointer-events-none disabled:opacity-50"
          >
            <Icon name="Edit" className="size-4" />
          </button>
        ) : (
          <button
            type="button"
            aria-label={t("agent.goToPath")}
            title={t("agent.goToPath")}
            disabled={disabled}
            onClick={commitEditor}
            className="inline-flex size-7 shrink-0 cursor-pointer items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground disabled:pointer-events-none disabled:opacity-50"
          >
            <Icon name="Check" className="size-4" />
          </button>
        )}
      </div>
      <div className="h-56 min-h-0 overflow-y-auto px-1.5 py-1">
        {creating && (
          <div
            role="group"
            aria-label={t("agent.createFolder")}
            className="mb-1 flex flex-col gap-1"
          >
            <div className="flex items-center gap-1">
              <input
                autoFocus
                value={folderName}
                aria-label={t("agent.folderName")}
                placeholder={t("agent.folderName")}
                disabled={folderBusy}
                onChange={(e) => setFolderName(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    e.preventDefault();
                    submitFolder();
                  } else if (e.key === "Escape") {
                    setCreating(false);
                  }
                }}
                className="-ml-1 h-7 min-w-0 flex-1 rounded-md border border-input bg-transparent px-1 text-sm outline-none focus-visible:ring-1 focus-visible:ring-ring"
              />
              <button
                type="button"
                aria-label={t("agent.cancelNewFolder")}
                disabled={folderBusy}
                onClick={() => setCreating(false)}
                className="inline-flex size-7 shrink-0 cursor-pointer items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground disabled:pointer-events-none disabled:opacity-50"
              >
                <Icon name="X" className="size-4" />
              </button>
            </div>
            {folderErr && (
              <p role="alert" className="px-2 text-xs text-destructive-text">
                {folderErr}
              </p>
            )}
          </div>
        )}
        {status === "loading" ? (
          <div className="flex items-center gap-2 px-2 py-3 text-sm text-muted-foreground">
            <Icon name="Spinner" className="size-4 animate-spin" />
            {t("agent.loadingFolders")}
          </div>
        ) : status === "error" ? (
          <div className="flex items-center gap-2 px-2 py-3 text-sm text-destructive-text">
            <Icon name="AlertCircle" className="size-4 shrink-0" />
            {t("agent.folderReadFailed")}
          </div>
        ) : entries.length === 0 ? (
          <div className="px-2 py-3 text-sm text-muted-foreground">
            {t("agent.folderEmpty")}
          </div>
        ) : (
          <ul>
            {entries.map((entry) =>
              entry.dir ? (
                <li key={entry.path}>
                  <button
                    type="button"
                    disabled={uiDisabled}
                    onClick={() => navigate(entry.path)}
                    className="flex w-full cursor-pointer items-center gap-2 rounded-sm px-2 py-1 text-left text-sm hover:bg-muted disabled:pointer-events-none"
                  >
                    <Icon
                      name="Folder"
                      className="size-4 shrink-0 text-muted-foreground"
                    />
                    <span className="min-w-0 flex-1 truncate">
                      {entry.name}
                    </span>
                    <Icon
                      name="ChevronRight"
                      className="size-3.5 shrink-0 text-muted-foreground"
                    />
                  </button>
                </li>
              ) : (
                <li
                  key={entry.path}
                  className="flex w-full items-center gap-2 px-2 py-1 text-sm text-muted-foreground"
                >
                  <Icon name="File" className="size-4 shrink-0" />
                  <span className="min-w-0 flex-1 truncate">{entry.name}</span>
                </li>
              ),
            )}
          </ul>
        )}
      </div>
    </div>
  );
}
