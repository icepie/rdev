import { useState, type ReactNode } from "react";
import { useDevices } from "../lib/devices";
import { useI18n } from "../lib/i18n";
import { cn } from "../lib/utils";
import { Icon } from "./icon";
import { OsIcon } from "./os-icon";

/* Left device-list rail shared by the Terminal / Files / Desktop pages,
 * mirroring the agent page's sidebar: rows are clickable (open in the
 * focused pane) and draggable onto any split pane. */

export const DEVICE_DRAG_MIME = "application/x-rdev-device";

export function DeviceRail({
  onPick,
  onCollapse,
}: {
  onPick: (deviceId: string) => void;
  onCollapse?: () => void;
}) {
  const { devices } = useDevices();
  const { t } = useI18n();
  return (
    <div className="flex h-full min-h-0 flex-col bg-sidebar">
      <div className="flex items-center gap-1 px-3 pt-2.5 pb-1">
        <span className="flex-1 text-2xs font-medium uppercase tracking-wide text-muted-foreground">
          {t("rail.devices")}
        </span>
        {onCollapse && (
          <button
            type="button"
            aria-label={t("rail.collapse")}
            onClick={onCollapse}
            className="flex size-6 cursor-pointer items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground"
          >
            <Icon name="PanelLeft" className="size-3.5" />
          </button>
        )}
      </div>
      <div className="scrollbar-thin min-h-0 flex-1 overflow-y-auto p-1.5">
        {devices.map((d) => (
          <button
            key={d.id}
            type="button"
            draggable
            title={d.id}
            onDragStart={(e) => {
              e.dataTransfer.setData(DEVICE_DRAG_MIME, d.id);
              e.dataTransfer.effectAllowed = "copy";
            }}
            onClick={() => onPick(d.id)}
            className="flex w-full cursor-pointer items-center gap-2 rounded-lg px-2 py-1.5 text-left text-sm text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground"
          >
            <OsIcon os={d.os} className="shrink-0 text-muted-foreground" />
            <span className="min-w-0 flex-1 truncate">{d.id}</span>
            {d.hasPassword && (
              <Icon
                name="Lock"
                className="size-3 shrink-0 text-muted-foreground"
              />
            )}
          </button>
        ))}
        {devices.length === 0 && (
          <div className="px-2 py-3 text-center text-xs text-muted-foreground">
            {t("rail.empty")}
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
      className={cn(className, over && "ring-2 ring-inset ring-timeline-accent")}
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
