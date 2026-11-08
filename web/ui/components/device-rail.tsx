import { useRef, useState, type ReactNode } from "react";
import { useDevices } from "../lib/devices";
import { useI18n } from "../lib/i18n";
import { cn } from "../lib/utils";
import { Input } from "./input";
import { Icon } from "./icon";
import { OsIcon } from "./os-icon";
import { SIDEBAR_ROW_CLASS } from "./workspace-sidebar";

/* Left device-list rail shared by the Terminal / Files / Desktop pages,
 * mirroring the agent page's sidebar: rows are clickable (open in the
 * focused pane) and draggable onto any split pane. */

export const DEVICE_DRAG_MIME = "application/x-rdev-device";

export function DeviceRail({
  onPick,
  selectedDevice,
}: {
  onPick: (deviceId: string) => void;
  selectedDevice?: string;
}) {
  const { devices, error, loading, refresh } = useDevices();
  const { t } = useI18n();
  const searchTriggerRef = useRef<HTMLButtonElement>(null);
  const deviceRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const [searchOpen, setSearchOpen] = useState(false);
  const [query, setQuery] = useState("");
  const visibleDevices = devices.filter((device) =>
    device.id.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()),
  );
  const selectedOffline = Boolean(
    selectedDevice && !loading && !error && !devices.some((device) => device.id === selectedDevice),
  );
  return (
    <div className="flex h-full min-h-0 flex-col bg-sidebar">
      <div className="flex min-h-12 shrink-0 flex-wrap items-center gap-1 border-b border-border-seam px-2 py-2">
        <span className="min-w-0 flex-1 px-2 text-sm font-medium text-sidebar-foreground">
          {t("rail.devices")}
        </span>
        <span className="text-2xs tabular-nums text-subtle-foreground">
          {devices.length}
        </span>
        <button
          type="button"
          aria-label={t("rail.search")}
          ref={searchTriggerRef}
          aria-expanded={searchOpen}
          onClick={() => {
            setSearchOpen(!searchOpen);
            setQuery("");
          }}
          className="flex size-7 max-md:pointer-coarse:size-10 shrink-0 cursor-pointer items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:ring-2 focus-visible:ring-sidebar-ring"
        >
          <Icon name="Search" className="size-4" />
        </button>
        {searchOpen && (
          <div className="w-full px-2 pb-0.5">
            <Input
              autoFocus
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Escape") {
                  event.preventDefault();
                  setQuery("");
                  setSearchOpen(false);
                  searchTriggerRef.current?.focus();
                }
              }}
              aria-label={t("rail.search")}
              placeholder={t("rail.search")}
            />
          </div>
        )}
      </div>
      <div className="scrollbar-thin min-h-0 flex-1 overflow-y-auto px-2 py-2">
        {error && (
          <div className="flex items-start gap-2 px-2 py-2 text-xs text-destructive-text">
          <p
            role="alert"
            className="min-w-0 flex-1 break-words"
          >
            {error}
          </p>
            <button type="button" onClick={refresh} disabled={loading} className="shrink-0 underline underline-offset-2 hover:no-underline disabled:opacity-50">{t(loading ? "common.loading" : "common.retry")}</button>
          </div>
        )}
        {loading && devices.length === 0 && !error && (
          <div role="status" className="flex items-center gap-2 px-2 py-3 text-xs text-muted-foreground">
            <Icon name="Spinner" className="size-3.5 animate-spin motion-reduce:animate-none" />
            {t("common.loading")}
          </div>
        )}
        {visibleDevices.map((d, index) => (
          <button
            key={d.id}
            type="button"
            draggable
            aria-pressed={d.id === selectedDevice}
            title={d.id}
            onDragStart={(e) => {
              e.dataTransfer.setData(DEVICE_DRAG_MIME, d.id);
              e.dataTransfer.effectAllowed = "copy";
            }}
            onClick={() => onPick(d.id)}
            ref={(node) => { deviceRefs.current[index] = node; }}
            onKeyDown={(event) => {
              if (!visibleDevices.length) return;
              let next = -1;
              if (event.key === "ArrowDown") next = (index + 1) % visibleDevices.length;
              else if (event.key === "ArrowUp") next = (index - 1 + visibleDevices.length) % visibleDevices.length;
              else if (event.key === "Home") next = 0;
              else if (event.key === "End") next = visibleDevices.length - 1;
              if (next >= 0) {
                event.preventDefault();
                deviceRefs.current[next]?.focus();
              }
            }}
            className={cn(
              SIDEBAR_ROW_CLASS,
              "mb-0.5",
              d.id === selectedDevice && "bg-state-active font-medium",
            )}
          >
            <OsIcon
              os={d.os}
              className="size-3.5 shrink-0 text-subtle-foreground"
            />
            <span className="min-w-0 flex-1 truncate">{d.id}</span>
            <span
              className="size-1.5 shrink-0 rounded-full bg-success"
              aria-hidden="true"
            />
            {d.hasPassword && (
              <Icon
                name="Lock"
                className="size-3 shrink-0 text-muted-foreground"
              />
            )}
          </button>
        ))}
        {selectedOffline && (
          <div
            role="status"
            aria-label={`${selectedDevice}: ${t("rail.selectedOffline")}`}
            className={cn(
              SIDEBAR_ROW_CLASS,
              "mb-0.5 cursor-default bg-state-active/60 text-muted-foreground",
            )}
          >
            <span className="size-1.5 shrink-0 rounded-full bg-muted-foreground/50" aria-hidden="true" />
            <span className="min-w-0 flex-1 truncate">{selectedDevice}</span>
            <span className="shrink-0 text-2xs">{t("rail.selectedOffline")}</span>
          </div>
        )}
        {visibleDevices.length === 0 && !error && !loading && (
          <div className="px-2 py-3 text-center text-xs text-muted-foreground">
            {t(query.trim() ? "rail.noMatches" : "rail.empty")}
          </div>
        )}
      </div>
    </div>
  );
}

/* Drop target for device rows dragged from the rail; highlights while a
 * device row hovers it. */
export function DeviceDropTarget({
  onDropDevice,
  onPointerDown,
  className,
  children,
}: {
  onDropDevice: (deviceId: string) => void;
  onPointerDown?: () => void;
  className?: string;
  children: ReactNode;
}) {
  const [over, setOver] = useState(false);
  return (
    <div
      className={cn(
        className,
        over && "ring-2 ring-inset ring-timeline-accent",
      )}
      onPointerDown={onPointerDown}
      onDragOver={(e) => {
        if (e.dataTransfer.types.includes(DEVICE_DRAG_MIME)) {
          e.preventDefault();
          e.dataTransfer.dropEffect = "copy";
          setOver(true);
        }
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => {
        const id = e.dataTransfer.getData(DEVICE_DRAG_MIME);
        setOver(false);
        if (id) {
          e.preventDefault();
          onDropDevice(id);
        }
      }}
    >
      {children}
    </div>
  );
}
