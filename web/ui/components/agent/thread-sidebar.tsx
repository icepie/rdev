import {
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type PointerEvent as ReactPointerEvent,
  type RefObject,
} from "react";
import { MobileWorkspaceNavigation } from "../../shell/AppShell";
import { isImeComposing } from "../../lib/keyboard";
import { useI18n } from "../../lib/i18n";
import { cn } from "../../lib/utils";
import type { DeviceInfo } from "../../lib/api";
import { threadIdentity, useThreadUnread } from "../../hooks/use-thread-unread";
import { Button } from "../button";
import { Input } from "../input";
import { COARSE_POINTER_HEADER_ICON_BUTTON_CLASS } from "../coarse-pointer-sizing";
import { DeviceIcon } from "../device-icon";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "../dropdown-menu";
import { Icon } from "../icon";
import {
  SIDEBAR_MAX_WIDTH,
  SIDEBAR_MIN_WIDTH,
  SIDEBAR_ROW_CLASS,
} from "../workspace-sidebar";
import {
  parseStringArray,
  threadCreatedMs,
  SERVER_AGENT_ID,
  type AllThreadsDevice,
  type Status,
} from "./protocol";
import { ThreadRenameInput } from "./thread-rename-input";
import type { ThreadInfo } from "./types";

const SIDEBAR_VIEW_KEY = "rdev.agent.sidebar.view";

/* bb thread-list view state (sort/direction/organize/filter/hidden groups),
 * persisted per browser. Hidden groups are plain device ids (rdev groups
 * are devices; bb's are `machine:<id>` sections). */
interface SidebarView {
  sort: "updated" | "created" | "alpha";
  dir: "asc" | "desc";
  organize: "flat" | "device";
  showRunning: boolean;
  showAll: boolean;
  hiddenGroups: string[];
  /* bb TopLevelSidebarSection: device group headers collapse (chevron)
   * independently of being hidden from the list. */
  collapsedGroups: string[];
  /* "" = 所有对话 (threads from every device); else a device id filter. */
  deviceFilter: string;
}

const DEFAULT_SIDEBAR_VIEW: SidebarView = {
  sort: "updated",
  dir: "desc",
  organize: "device",
  showRunning: true,
  showAll: true,
  hiddenGroups: [],
  collapsedGroups: [],
  deviceFilter: "",
};

function loadSidebarView(): SidebarView {
  try {
    const raw = localStorage.getItem(SIDEBAR_VIEW_KEY);
    if (!raw) return DEFAULT_SIDEBAR_VIEW;
    const o = JSON.parse(raw) as Record<string, unknown>;
    return {
      sort: o.sort === "created" || o.sort === "alpha" ? o.sort : "updated",
      dir: o.dir === "asc" ? "asc" : "desc",
      organize: o.organize === "device" ? "device" : "flat",
      showRunning: o.showRunning !== false,
      showAll: o.showAll !== false,
      hiddenGroups: parseStringArray(o.hiddenGroups),
      collapsedGroups: parseStringArray(o.collapsedGroups),
      deviceFilter: typeof o.deviceFilter === "string" ? o.deviceFilter : "",
    };
  } catch {
    return DEFAULT_SIDEBAR_VIEW;
  }
}

const SIDEBAR_SORTS: { id: SidebarView["sort"]; label: string }[] = [
  { id: "updated", label: "agent.sortUpdated" },
  { id: "created", label: "agent.sortCreated" },
  { id: "alpha", label: "agent.sortAlpha" },
];

export type SidebarRow = ThreadInfo & { device: string; os?: string };

export interface ThreadSidebarProps {
  /* Thread sources the sidebar derives its rows and device list from. */
  devices: DeviceInfo[];
  allThreads: AllThreadsDevice[] | null;
  threads: ThreadInfo[];
  device: string;
  currentDevice?: DeviceInfo;
  /* Shell/offcanvas state owned by the page layout. */
  open: boolean;
  width: number;
  dragging: boolean;
  transitionsReady: boolean;
  narrow: boolean;
  coarse: boolean;
  /* Connection state. */
  status: Status;
  authed: boolean;
  ready: boolean;
  /* Thread management owned by the page. */
  openThreadIds: Set<string>;
  renamingId: string | null;
  managementError: string;
  suppressClickRef: RefObject<boolean>;
  onSelectThread: (device: string, thread: string) => void;
  onRowPointerDown: (row: { id: string; title: string; rowDevice: string }) => (e: ReactPointerEvent) => void;
  onRenameThread: (id: string, title: string, targetDevice?: string) => boolean;
  onRenameChange: (id: string | null) => void;
  onPinThread: (row: SidebarRow) => void;
  onDeleteThread: (target: { id: string; device: string; title: string; running?: boolean }) => void;
  /* Returns whether a new_thread frame was actually sent. */
  onNewThread: () => boolean;
  onReconnect: () => void;
  onSelectDevice: (id: string) => void;
  onClose: () => void;
  onResize: (width: number) => void;
  onResizeCommit: () => void;
  onDraggingChange: (dragging: boolean) => void;
}

export function ThreadSidebar({
  devices,
  allThreads,
  threads,
  device,
  currentDevice,
  open,
  width,
  dragging,
  transitionsReady,
  narrow,
  coarse,
  status,
  authed,
  ready,
  openThreadIds,
  renamingId,
  managementError,
  suppressClickRef,
  onSelectThread,
  onRowPointerDown,
  onRenameThread,
  onRenameChange,
  onPinThread,
  onDeleteThread,
  onNewThread,
  onReconnect,
  onSelectDevice,
  onClose,
  onResize,
  onResizeCommit,
  onDraggingChange,
}: ThreadSidebarProps) {
  const { t } = useI18n();
  const [sidebarView, setSidebarView] = useState<SidebarView>(loadSidebarView);

  /* bb thread-list view state: one write per user gesture (menu handlers),
   * merged onto the persisted record. */
  const updateSidebarView = (patch: Partial<SidebarView>) => {
    const next = { ...sidebarView, ...patch };
    setSidebarView(next);
    localStorage.setItem(SIDEBAR_VIEW_KEY, JSON.stringify(next));
  };

  /* bb: re-clicking the active sort flips its direction; switching picks
   * the natural direction (recent first / A→Z). */
  const selectSort = (id: SidebarView["sort"]) => {
    if (sidebarView.sort === id) {
      updateSidebarView({ dir: sidebarView.dir === "asc" ? "desc" : "asc" });
    } else {
      updateSidebarView({ sort: id, dir: id === "alpha" ? "asc" : "desc" });
    }
  };

  /* Sidebar rows: bb-style global conversation list. With all_threads the
   * rows are every device's threads flattened and sorted by updatedAt
   * (desc); legacy servers fall back to the current-device thread_list. */
  const sidebarRows: SidebarRow[] = useMemo(() => {
    if (allThreads !== null) {
      return allThreads
        .flatMap((d) =>
          d.threads.map((th) => ({ ...th, device: d.device, os: d.os })),
        )
        .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
    }
    return threads.map((th) => ({ ...th, device, os: currentDevice?.os }));
  }, [allThreads, threads, device, currentDevice]);

  /* Sidebar device list merges live REST devices with all_threads history.
   * The server coordinator has no client row, so keep @server from `devices`
   * even when the history snapshot omits it. Live status wins over stale
   * history; historical-only devices remain visible but disabled. */
  const sidebarDevices: {
    id: string;
    displayName?: string;
    os?: string;
    online: boolean;
    count?: number;
  }[] = useMemo(() => {
    const merged = new Map<string, {
      id: string;
      displayName?: string;
      os?: string;
      online: boolean;
      count?: number;
    }>();
    for (const d of allThreads ?? []) {
      merged.set(d.device, {
        id: d.device,
        os: d.os,
        online: d.online,
        count: d.threads.length,
      });
    }
    for (const d of devices) {
      const previous = merged.get(d.id);
      merged.set(d.id, {
        id: d.id,
        displayName: d.displayName,
        os: d.os ?? previous?.os,
        online: true,
        count: previous?.count,
      });
    }
    return [...merged.values()].sort(
      (a, b) =>
        Number(b.id === device) - Number(a.id === device) ||
        Number(b.online) - Number(a.online) ||
        a.id.localeCompare(b.id),
    );
  }, [allThreads, devices, device]);

  /* Filter (All/Running) + title search + sort with the direction
   * multiplier. */
  const [threadSearchOpen, setThreadSearchOpen] = useState(false);
  const [threadQuery, setThreadQuery] = useState("");
  const filteredRows: SidebarRow[] = useMemo(() => {
    let rows =
      !sidebarView.showAll && sidebarView.showRunning
        ? sidebarRows.filter((r) => r.running || (r.backgroundJobs ?? 0) > 0)
        : sidebarRows;
    if (sidebarView.deviceFilter) {
      rows = rows.filter((r) => r.device === sidebarView.deviceFilter);
    }
    const q = threadQuery.trim().toLowerCase();
    if (q) {
      rows = rows.filter((r) =>
        (r.title || r.id).toLowerCase().includes(q) ||
        r.id.toLowerCase().includes(q) ||
        r.device.toLowerCase().includes(q),
      );
    }
    const mul = sidebarView.dir === "asc" ? 1 : -1;
    return [...rows].sort((a, b) => {
      if (sidebarView.sort === "alpha") {
        return (
          mul *
          ((a.title || a.id).localeCompare(b.title || b.id) ||
            a.id.localeCompare(b.id))
        );
      }
      if (sidebarView.sort === "created") {
        return mul * (threadCreatedMs(a.id) - threadCreatedMs(b.id));
      }
      return mul * a.updatedAt.localeCompare(b.updatedAt);
    });
  }, [sidebarRows, sidebarView, threadQuery]);

  const pinnedRows = useMemo(
    () => filteredRows.filter(row => row.pinnedAt).sort((a, b) => (b.pinnedAt ?? 0) - (a.pinnedAt ?? 0)),
    [filteredRows],
  );
  const unpinnedRows = useMemo(() => filteredRows.filter(row => !row.pinnedAt), [filteredRows]);

  /* Organize → By device: group the filtered rows; the current device
   * floats to the top, then device id ascending (bb section order). */
  const sidebarGroups: { device: string; os?: string; rows: SidebarRow[] }[] | null =
    useMemo(() => {
      if (sidebarView.organize !== "device") return null;
      const map = new Map<string, { device: string; os?: string; rows: SidebarRow[] }>();
      if (!threadQuery.trim() && sidebarView.showAll) {
        for (const d of sidebarDevices) {
          if (!sidebarView.deviceFilter || sidebarView.deviceFilter === d.id) map.set(d.id, { device: d.id, os: d.os, rows: [] });
        }
      }
      for (const row of unpinnedRows) {
        const g = map.get(row.device);
        if (g) {
          g.rows.push(row);
        } else {
          map.set(row.device, { device: row.device, os: row.os, rows: [row] });
        }
      }
      return [...map.values()].sort(
        (a, b) =>
          Number(b.device === device) - Number(a.device === device) ||
          a.device.localeCompare(b.device),
      );
    }, [unpinnedRows, sidebarView.organize, sidebarView.showAll, sidebarView.deviceFilter, device, sidebarDevices, threadQuery]);

  const visibleGroups =
    sidebarGroups?.filter((g) => threadQuery.trim() || !sidebarView.hiddenGroups.includes(g.device)) ??
    null;
  /* Hidden groups that still have threads (the More … row restores them). */
  const hiddenExistingGroups =
    sidebarGroups?.filter((g) => sidebarView.hiddenGroups.includes(g.device)) ??
    [];

  const unreadThreads = useThreadUnread(sidebarRows, device, openThreadIds);

  /* Sidebar resize: pointer-capture drag on the seam handle; width persists
   * on release only (live drags just move the var). */
  const sidebarDragRef = useRef<{ startX: number; startWidth: number } | null>(
    null,
  );
  const onSidebarHandleDown = (e: ReactPointerEvent<HTMLSpanElement>) => {
    if (e.button !== 0) return;
    e.preventDefault();
    // Capture keeps the drag alive outside the 16px strip; untrusted
    // (synthetic) events throw, so tolerate that.
    try {
      e.currentTarget.setPointerCapture(e.pointerId);
    } catch {
      /* untrusted event */
    }
    sidebarDragRef.current = { startX: e.clientX, startWidth: width };
    onDraggingChange(true);
  };
  const onSidebarHandleMove = (e: ReactPointerEvent<HTMLSpanElement>) => {
    const d = sidebarDragRef.current;
    if (!d) return;
    onResize(
      Math.min(
        SIDEBAR_MAX_WIDTH,
        Math.max(SIDEBAR_MIN_WIDTH, d.startWidth + e.clientX - d.startX),
      ),
    );
  };
  const onSidebarHandleUp = () => {
    if (!sidebarDragRef.current) return;
    sidebarDragRef.current = null;
    onDraggingChange(false);
    onResizeCommit();
  };

  const renderSidebarRow = (th: SidebarRow) => (
    <div
      key={th.device + ":" + th.id}
      title={th.device + " · " + (th.title || th.id)}
      className={cn(
        "group/thread relative",
        SIDEBAR_ROW_CLASS,
        th.device === device && openThreadIds.has(th.id) && "bg-state-active font-medium",
      )}
      onPointerDown={onRowPointerDown({
        id: th.id,
        title: th.title || th.id,
        rowDevice: th.device,
      })}
      onClick={() => {
        /* A real drag ends in pointerup; the trailing click is swallowed. */
        if (suppressClickRef.current) {
          suppressClickRef.current = false;
          return;
        }
        onSelectThread(th.device, th.id);
      }}
    >
      {sidebarView.organize === "flat" && <DeviceIcon device={th.device} os={th.os} />}
      {renamingId === `${th.device}:${th.id}` ? (
        /* bb inline rename: input replaces the title; Enter/blur commits,
         * Escape cancels. */
        <ThreadRenameInput
          title={th.title || th.id}
          label={t("agent.renamePrompt")}
          onCommit={title => onRenameThread(th.id, title, th.device)}
          onFinish={() => onRenameChange(null)}
        />
      ) : (
        <button
          type="button"
          aria-pressed={th.device === device && openThreadIds.has(th.id)}
          className="h-full min-w-0 flex-1 truncate text-left font-inherit focus-visible:outline focus-visible:outline-ring"
        >
          {th.title || th.id}
        </button>
      )}
      {(th.backgroundJobs ?? 0) > 0 && (
        <span className="shrink-0 rounded bg-state-hover px-1 text-2xs tabular-nums text-muted-foreground"
          aria-label={t("agent.backgroundJobs", { count: th.backgroundJobs! })}
          title={t("agent.backgroundJobs", { count: th.backgroundJobs! })}>
          {th.backgroundJobs}
        </span>
      )}
      <span className="flex h-4 w-4 shrink-0 items-center justify-center">
        {!th.running && !(th.backgroundJobs ?? 0) && unreadThreads.has(threadIdentity(th.device, th.id)) && (
          <span role="img" aria-label={t(th.outcome === "error" ? "agent.failedUnread" : th.outcome === "stopped" ? "agent.stoppedUnread" : th.outcome === "done" ? "agent.completedUnread" : "agent.updatedUnread")} title={t(th.outcome === "error" ? "agent.failedUnread" : th.outcome === "stopped" ? "agent.stoppedUnread" : th.outcome === "done" ? "agent.completedUnread" : "agent.updatedUnread")}
            className={cn("size-2 rounded-full", th.outcome === "error" ? "bg-destructive" : th.outcome === "stopped" ? "bg-muted-foreground" : "bg-timeline-accent")} />
        )}
        {th.running && (
          <span role="img" aria-label={t(th.waiting === "approval" ? "agent.waitingApproval" : th.waiting === "answer" ? "agent.waitingAnswer" : "agent.filterRunning")}
            title={t(th.waiting === "approval" ? "agent.waitingApproval" : th.waiting === "answer" ? "agent.waitingAnswer" : "agent.filterRunning")}>
            <Icon
              name={th.waiting ? "MessageCircle" : "Spinner"}
              className={th.waiting ? "size-3.5 text-warning-text" : "size-3.5 animate-spin text-timeline-accent motion-reduce:animate-none"}
            />
          </span>
        )}
      </span>
      {renamingId !== `${th.device}:${th.id}` && (
        /* bb row actions: hover-revealed ⋯ menu (rename inline / delete). */
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              type="button"
              aria-label={t("agent.threadActions")}
              className="flex h-7 w-7 pointer-coarse:h-10 pointer-coarse:w-10 shrink-0 items-center justify-center rounded-md text-subtle-foreground opacity-0 transition-opacity group-hover/thread:opacity-100 group-focus-within/thread:opacity-100 pointer-coarse:opacity-100 focus-visible:outline focus-visible:outline-ring hover:bg-state-hover hover:text-muted-foreground data-[state=open]:bg-state-active data-[state=open]:opacity-100"
              onClick={(e) => e.stopPropagation()}
              onPointerDown={(e) => e.stopPropagation()}
            >
              <Icon name="MoreHorizontal" className="size-4" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent side="right" align="start" sideOffset={8}>
            <DropdownMenuItem onSelect={() => onPinThread(th)}>
              <Icon name="Pin" className="size-4 shrink-0" />
              {t(th.pinnedAt ? "agent.unpin" : "agent.pin")}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => onRenameChange(`${th.device}:${th.id}`)}>
              <Icon name="Edit" className="size-4 shrink-0" />
              {t("agent.rename")}
            </DropdownMenuItem>
            <DropdownMenuItem
              className="text-destructive-text focus:text-destructive-text"
              onSelect={() => onDeleteThread({ id: th.id, device: th.device, title: th.title || th.id, running: th.running })}
            >
              <Icon name="Trash2" className="size-4 shrink-0" />
              {t("agent.deleteThread")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </div>
  );

  /* 对话操作 menu items, shared by the desktop submenu layout and the
   * compact flat layout. Compact renders them inline because radix
   * MenuSub requires a Menu root, which the responsive drawer shell
   * does not provide (bb flattens the same way). */
  const sidebarOrganizeItems = (
    <>
      <DropdownMenuItem
        onSelect={() => updateSidebarView({ organize: "flat" })}
      >
        <span className="flex-1">{t("agent.organizeFlat")}</span>
        {sidebarView.organize === "flat" && (
          <Icon name="Check" className="size-4 shrink-0" />
        )}
      </DropdownMenuItem>
      <DropdownMenuItem
        onSelect={() => updateSidebarView({ organize: "device" })}
      >
        <span className="flex-1">{t("agent.organizeDevice")}</span>
        {sidebarView.organize === "device" && (
          <Icon name="Check" className="size-4 shrink-0" />
        )}
      </DropdownMenuItem>
    </>
  );
  const sidebarSortItems = SIDEBAR_SORTS.map((o) => (
    <DropdownMenuItem key={o.id} onSelect={() => selectSort(o.id)}>
      <span className="flex-1">{t(o.label)}</span>
      {sidebarView.sort === o.id && (
        <span className="ml-auto flex items-center gap-0.5">
          <Icon name="Check" className="size-4" />
          <Icon
            name={sidebarView.dir === "asc" ? "ArrowUp" : "ArrowDown"}
            className="size-3.5"
          />
        </span>
      )}
    </DropdownMenuItem>
  ));
  const sidebarFilterItems = (
    <>
      {/* bb: at least one filter stays checked; the last checked item's
       * uncheck is refused (hover title). */}
      <DropdownMenuCheckboxItem
        checked={sidebarView.showAll}
        title={
          sidebarView.showAll && !sidebarView.showRunning
            ? t("agent.keepOneFilter")
            : undefined
        }
        onCheckedChange={(v) => {
          if (!v && !sidebarView.showRunning) return;
          updateSidebarView({ showAll: !!v });
        }}
      >
        {t("agent.filterAll")}
      </DropdownMenuCheckboxItem>
      <DropdownMenuCheckboxItem
        checked={sidebarView.showRunning}
        title={
          sidebarView.showRunning && !sidebarView.showAll
            ? t("agent.keepOneFilter")
            : undefined
        }
        onCheckedChange={(v) => {
          if (!v && !sidebarView.showAll) return;
          updateSidebarView({ showRunning: !!v });
        }}
      >
        {t("agent.filterRunning")}
      </DropdownMenuCheckboxItem>
    </>
  );

  /* Empty state: distinguish "nothing matched the search/filter" from
   * "no threads at all" so an active query never reads as data loss. */
  const sidebarEmpty = (
    <div className="flex items-center gap-2">
      <Icon
        name={threadQuery.trim() ? "Search" : "MessageSquare"}
        className="size-3.5 shrink-0 text-subtle-foreground/50"
      />
      <span className="text-xs leading-4 text-muted-foreground">
        {threadQuery.trim() ? t("agent.noMatchingThreads") : t("agent.noThreads")}
      </span>
    </div>
  );

  return (
    <>
      <aside
        inert={!open}
        style={{ "--sidebar-w": `${width}px` } as CSSProperties}
        className={cn(
          "flex shrink-0 flex-col overflow-hidden border-r border-border-seam bg-sidebar text-sidebar-foreground select-none max-md:pl-12",
          narrow
            ? "absolute inset-y-0 left-0 z-20 w-[var(--agent-mobile-sidebar-width)]"
            : "absolute inset-y-0 left-0 z-20 w-[var(--sidebar-w)]",
          transitionsReady && !dragging
            ? narrow
              ? open
                ? "[transition:translate_220ms_cubic-bezier(0.32,0.72,0,1),visibility_0s] motion-reduce:transition-none"
                : "[transition:translate_220ms_cubic-bezier(0.32,0.72,0,1),visibility_0s_220ms] motion-reduce:transition-none"
              : open
                ? "[transition:translate_200ms_linear,visibility_0s] motion-reduce:transition-none"
                : "[transition:translate_200ms_linear,visibility_0s_200ms] motion-reduce:transition-none"
            : "transition-none",
          open ? "translate-x-0" : "invisible -translate-x-full",
        )}
      >
        <div className="flex h-full min-h-0 w-full shrink-0 flex-col md:w-[var(--sidebar-w)]">
          <MobileWorkspaceNavigation />
          {/* bb sidebar navigation rows: primary actions + device panels as
           * full-width rows above the threads section. */}
          <div className="flex min-h-12 shrink-0 flex-wrap items-center gap-1 border-b border-border-seam px-2 py-2">
            <button
              type="button"
              disabled={!ready}
              onClick={() => {
                if (onNewThread()) {
                  /* The fresh thread lands on the connected device: clear
                   * any device filter / search query that would hide it. */
                  if (sidebarView.deviceFilter) {
                    updateSidebarView({ deviceFilter: "" });
                  }
                  setThreadQuery("");
                }
                /* Mobile keeps the left-right split: no auto-dismiss. */
              }}
              className="flex h-7 max-md:pointer-coarse:h-10 min-w-0 flex-1 cursor-pointer items-center gap-2 rounded-md pr-0 pl-2 text-sm transition-colors hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 focus-visible:ring-sidebar-ring disabled:cursor-default disabled:opacity-70"
            >
              <Icon
                name="MessageSquarePlus"
                className="size-3.5 shrink-0 text-muted-foreground"
              />
              <span className="min-w-0 flex-1 truncate text-left">
                {t("agent.newThread")}
              </span>
            </button>
            <button
              type="button"
              aria-label={t("agent.searchThreads")}
              aria-expanded={threadSearchOpen}
              onClick={() => {
                setThreadSearchOpen((v) => {
                  if (v) setThreadQuery("");
                  return !v;
                });
              }}
              className={cn(
                "flex size-7 max-md:pointer-coarse:size-10 shrink-0 cursor-pointer items-center justify-center rounded-md text-sm transition-colors hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 focus-visible:ring-sidebar-ring",
                threadSearchOpen && "bg-state-active",
              )}
            >
              <Icon
                name="Search"
                className="size-3.5 shrink-0 text-muted-foreground"
              />
              <span className="sr-only">
                {t("agent.searchThreads")}
              </span>
            </button>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon"
                    aria-label={t("agent.threadsActions")}
                    className={cn(COARSE_POINTER_HEADER_ICON_BUTTON_CLASS, "text-subtle-foreground hover:text-muted-foreground data-[state=open]:text-muted-foreground")}
                  >
                    <Icon name="MoreHorizontal" className="size-4" />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent side="right" align="start" sideOffset={8}>
                  {narrow ? (
                    /* Compact drawer shell has no radix Menu root: flatten
                     * the submenus into labeled sections (bb pattern). */
                    <>
                      <DropdownMenuLabel>{t("agent.organize")}</DropdownMenuLabel>
                      {sidebarOrganizeItems}
                      <DropdownMenuSeparator />
                      <DropdownMenuLabel>{t("agent.sortBy")}</DropdownMenuLabel>
                      {sidebarSortItems}
                      <DropdownMenuSeparator />
                      <DropdownMenuLabel>{t("agent.filter")}</DropdownMenuLabel>
                      {sidebarFilterItems}
                    </>
                  ) : (
                    <>
                      <DropdownMenuSub>
                        <DropdownMenuSubTrigger>
                          <Icon name="Layers" className="size-4 shrink-0" />
                          {t("agent.organize")}
                        </DropdownMenuSubTrigger>
                        <DropdownMenuSubContent>
                          {sidebarOrganizeItems}
                        </DropdownMenuSubContent>
                      </DropdownMenuSub>
                      <DropdownMenuSub>
                        <DropdownMenuSubTrigger>
                          <Icon name="ArrowUpDown" className="size-4 shrink-0" />
                          {t("agent.sortBy")}
                        </DropdownMenuSubTrigger>
                        <DropdownMenuSubContent>
                          {sidebarSortItems}
                        </DropdownMenuSubContent>
                      </DropdownMenuSub>
                      <DropdownMenuSub>
                        <DropdownMenuSubTrigger>
                          <Icon name="SlidersHorizontal" className="size-4 shrink-0" />
                          {t("agent.filter")}
                        </DropdownMenuSubTrigger>
                        <DropdownMenuSubContent>
                          {sidebarFilterItems}
                        </DropdownMenuSubContent>
                      </DropdownMenuSub>
                    </>
                  )}
                </DropdownMenuContent>
              </DropdownMenu>
            {threadSearchOpen && (
              <div className="w-full px-2 pb-0.5">
                <Input
                  autoFocus
                  value={threadQuery}
                  onChange={(e) => setThreadQuery(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Escape" && !isImeComposing(e.nativeEvent)) {
                      e.stopPropagation();
                      setThreadQuery("");
                      setThreadSearchOpen(false);
                    }
                  }}
                  aria-label={t("agent.searchThreads")}
                  placeholder={t("agent.searchThreads")}
                  className="bg-background"
                />
              </div>
            )}
          </div>
          {managementError && <p role="alert" className="px-3 py-2 text-xs text-destructive-text">{managementError}</p>}
          <div className="flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto px-2 py-2">
            {pinnedRows.length > 0 && <section aria-label={t("agent.pinned")} className="mb-2">
              <div className="flex h-8 items-center gap-2 px-2 text-xs font-medium text-muted-foreground"><Icon name="Pin" className="size-3.5" />{t("agent.pinned")}</div>
              {pinnedRows.map(renderSidebarRow)}
            </section>}
            {visibleGroups === null ? (
              unpinnedRows.length === 0 ? (
                pinnedRows.length ? null : sidebarEmpty
              ) : (
                unpinnedRows.map(renderSidebarRow)
              )
            ) : visibleGroups.length === 0 ? (
              sidebarEmpty
            ) : (
              visibleGroups.map((g) => {
                const collapsed = !threadQuery.trim() && sidebarView.collapsedGroups.includes(g.device);
                return (
                <div key={g.device} className="mb-2 flex flex-col gap-0.5">
                  {/* bb TopLevelSidebarSection: sticky label tier with a
                   * collapse chevron and a hover ⋯ → Hide from list. */}
                  <div className="group/devhead sticky top-0 z-10 flex min-h-9 items-center gap-1 bg-sidebar px-1 text-xs text-muted-foreground select-none">
                    <button
                      type="button"
                      aria-expanded={!collapsed}
                      title={g.device}
                      onClick={() => updateSidebarView({ collapsedGroups: collapsed
                        ? sidebarView.collapsedGroups.filter((id) => id !== g.device)
                        : [...sidebarView.collapsedGroups, g.device] })}
                      className="flex min-h-9 min-w-0 flex-1 cursor-pointer items-center gap-1.5 rounded-md px-1 text-left hover:bg-state-hover hover:text-foreground"
                    >
                      <Icon name="ChevronRight" className={cn("size-3 shrink-0 transition-transform", !collapsed && "rotate-90")} />
                      <DeviceIcon device={g.device} os={g.os} />
                      <span className="min-w-0 flex-1 truncate font-medium">{g.device === SERVER_AGENT_ID ? t("agent.server") : g.device}</span>
                      <span className="shrink-0 text-2xs tabular-nums text-subtle-foreground">{g.rows.length}</span>
                      <span className={cn("size-1.5 shrink-0 rounded-full", sidebarDevices.some((d) => d.id === g.device && d.online) ? "bg-success" : "bg-muted-foreground/40")} />
                    </button>
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <button
                          type="button"
                          aria-label={t("agent.threadActions")}
                          className="inline-flex h-7 w-7 pointer-coarse:h-10 pointer-coarse:w-10 shrink-0 cursor-pointer items-center justify-center rounded-md text-subtle-foreground opacity-0 transition-opacity group-hover/devhead:opacity-100 group-focus-within/devhead:opacity-100 pointer-coarse:opacity-100 focus-visible:outline focus-visible:outline-ring hover:bg-state-hover hover:text-muted-foreground data-[state=open]:bg-state-active data-[state=open]:opacity-100"
                        >
                          <Icon name="MoreHorizontal" className="size-4" />
                        </button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent side="right" align="start" sideOffset={8}>
                        <DropdownMenuItem
                          onSelect={() =>
                            updateSidebarView({
                              hiddenGroups: [...sidebarView.hiddenGroups, g.device],
                            })
                          }
                        >
                          <Icon name="EyeOff" className="size-4 shrink-0" />
                          {t("agent.hideFromList")}
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                  {!collapsed && <div className="ml-3 flex flex-col gap-0.5 border-l border-border-seam pl-1">{g.rows.length ? g.rows.map(renderSidebarRow) : threadQuery.trim() ? <div className="px-2 py-2">{sidebarEmpty}</div> : (
                    <button
                      type="button"
                      disabled={!sidebarDevices.some((d) => d.id === g.device && d.online)}
                      onClick={() => {
                        if (device !== g.device) onSelectDevice(g.device);
                        else if (authed) onNewThread();
                        else onReconnect();
                        if (narrow) onClose();
                      }}
                      className="flex min-h-9 pointer-coarse:min-h-10 items-center gap-2 rounded-md px-2 text-left text-xs text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground focus-visible:outline focus-visible:outline-ring disabled:cursor-not-allowed disabled:opacity-50"
                    >
                      <Icon name="MessageSquarePlus" className="size-3.5 shrink-0" />
                      {t("agent.newThread")}
                    </button>
                  )}</div>}
                </div>
                );
              })
            )}
          </div>
          {sidebarGroups !== null && hiddenExistingGroups.length > 0 && (
            /* bb More row: restores hidden device groups. */
            <div className="shrink-0 px-2 pb-1">
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <button
                    type="button"
                    className="flex h-7 max-md:pointer-coarse:h-10 w-full cursor-pointer items-center gap-2 rounded-md pl-2 pr-0 text-sm transition-colors hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 focus-visible:ring-sidebar-ring"
                  >
                    <Icon name="MoreHorizontal" className="size-4 shrink-0 text-subtle-foreground" />
                    {t("agent.more")}
                  </button>
                </DropdownMenuTrigger>
                <DropdownMenuContent side="right" align="start" sideOffset={8}>
                  {hiddenExistingGroups.map((g) => (
                    <DropdownMenuItem
                      key={g.device}
                      onSelect={() =>
                        updateSidebarView({
                          hiddenGroups: sidebarView.hiddenGroups.filter(
                            (x) => x !== g.device,
                          ),
                        })
                      }
                    >
                      <Icon name="Eye" className="size-4 shrink-0" />
                      <span className="min-w-0 flex-1 truncate">{g.device === SERVER_AGENT_ID ? t("agent.server") : g.device}</span>
                      <span className="ml-auto pl-2 text-2xs text-muted-foreground">
                        {t("agent.addToList")}
                      </span>
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          )}
          {/* Sidebar footer: current device + connection status */}
          <div className="flex shrink-0 items-center gap-1.5 border-t border-border-seam p-2 text-xs text-muted-foreground select-none">
            <span
              className={cn(
                "size-1.5 shrink-0 rounded-full",
                status === "connected"
                  ? "bg-success"
                  : status === "disconnected"
                    ? "bg-destructive"
                    : "bg-attention",
              )}
            />
            <DropdownMenu>
              <DropdownMenuTrigger asChild><button type="button" className="flex min-w-0 flex-1 items-center gap-1 rounded px-1 py-1 text-left hover:bg-state-hover"><span className="min-w-0 flex-1 truncate">{device === SERVER_AGENT_ID ? t("agent.server") : currentDevice?.displayName && currentDevice.displayName !== device ? `${currentDevice.displayName} (${device})` : device || t("agent.selectDeviceShort")}</span><Icon name="ChevronDown" className="size-3 shrink-0" /></button></DropdownMenuTrigger>
              <DropdownMenuContent side="top" align="start">
                <DropdownMenuItem onSelect={() => updateSidebarView({ deviceFilter: "" })}>{t("agent.allThreads")}</DropdownMenuItem>
                <DropdownMenuSeparator />
                {sidebarDevices.map((d) => <DropdownMenuItem key={d.id} onSelect={() => { updateSidebarView({ deviceFilter: "" }); onSelectDevice(d.id); }}><DeviceIcon device={d.id} os={d.os} /><span className="max-w-64 truncate">{d.id === SERVER_AGENT_ID ? t("agent.server") : d.displayName && d.displayName !== d.id ? `${d.displayName} (${d.id})` : d.id}</span>{d.id === device && <Icon name="Check" className="size-3.5" />}</DropdownMenuItem>)}
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </div>
      </aside>
      {/* Desktop resize seam; compact drawers have a viewport-derived width. */}
      {!narrow && !coarse && open && (
        <div className="group relative z-20 w-0 shrink-0 overflow-visible">
          <span
            role="separator"
            aria-orientation="vertical"
            aria-label={t("agent.resizeSidebar")}
            tabIndex={0}
            aria-valuemin={SIDEBAR_MIN_WIDTH}
            aria-valuemax={SIDEBAR_MAX_WIDTH}
            aria-valuenow={Math.round(width)}
            onKeyDown={(e) => {
              if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)) return;
              e.preventDefault();
              const next = e.key === "Home" ? SIDEBAR_MIN_WIDTH
                : e.key === "End" ? SIDEBAR_MAX_WIDTH
                : Math.max(SIDEBAR_MIN_WIDTH, Math.min(SIDEBAR_MAX_WIDTH,
                    width + (e.key === "ArrowRight" ? 16 : -16)));
              onResize(next);
              onResizeCommit();
            }}
            className="absolute inset-y-0 left-0 w-3 -translate-x-1/2 cursor-col-resize touch-none"
            onPointerDown={onSidebarHandleDown}
            onPointerMove={onSidebarHandleMove}
            onPointerUp={onSidebarHandleUp}
            onPointerCancel={onSidebarHandleUp}
          />
          <span
            aria-hidden
            className={cn(
              "pointer-events-none absolute inset-y-0 left-0 z-10 w-px -translate-x-1/2 transition-colors",
              dragging
                ? "bg-sidebar-border"
                : "bg-transparent group-hover:bg-sidebar-border",
            )}
          />
        </div>
      )}
    </>
  );
}
