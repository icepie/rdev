import { MobileWorkspaceNavigation } from "../shell/AppShell";
import { Button } from "./button";
import { Icon } from "./icon";
import { COARSE_POINTER_HEADER_ICON_BUTTON_CLASS } from "./coarse-pointer-sizing";
import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { DeviceRail } from "./device-rail";
import { useIsCompactViewport } from "./hooks/use-compact-viewport";
import { setRailControl } from "../lib/rail-control";
import { useI18n } from "../lib/i18n";
import {
  clampSidebarWidth,
  SIDEBAR_DEFAULT_WIDTH,
  SIDEBAR_MAX_WIDTH,
  SIDEBAR_MIN_WIDTH,
} from "./workspace-sidebar";

/** Pixel-sized BB sidebar. Children stay mounted through resizing and folding. */
export function SplitPageShell({
  storageKey,
  onDevicePick,
  selectedDevice,
  children,
}: {
  storageKey: string;
  onDevicePick: (deviceId: string) => void;
  selectedDevice?: string;
  children: ReactNode;
}) {
  const { t } = useI18n();
  const widthKey = `${storageKey}.width`;
  const [width, setWidth] = useState(() => {
    try {
      const stored = localStorage.getItem(widthKey);
      return stored ? clampSidebarWidth(Number(stored)) : SIDEBAR_DEFAULT_WIDTH;
    } catch {
      return SIDEBAR_DEFAULT_WIDTH;
    }
  });
  const [collapsed, setCollapsed] = useState(false);
  const [mobileOpen, setMobileOpen] = useState(false);
  const [dragging, setDragging] = useState(false);
  const sidebarRef = useRef<HTMLElement>(null);
  const separatorRef = useRef<HTMLDivElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  const dismissRef = useRef<HTMLButtonElement>(null);
  const openerRef = useRef<HTMLElement | null>(null);
  const wasMobileOpen = useRef(false);
  const drag = useRef<{ x: number; width: number } | null>(null);
  const compact = useIsCompactViewport();
  const open = compact ? mobileOpen : !collapsed;
  const rememberWidth = (value: number) => {
    try {
      localStorage.setItem(widthKey, String(Math.round(value)));
    } catch {
      /* Session-only when storage is restricted. */
    }
  };

  useLayoutEffect(() => {
    const showing = compact && mobileOpen;
    const active = document.activeElement;
    if (showing && !wasMobileOpen.current) {
      openerRef.current = active instanceof window.HTMLElement ? active : null;
      // The workspace becomes inert while revealed. Move keyboard focus to
      // the device list without forcing focus again on subsequent renders.
      sidebarRef.current?.focus({ preventScroll: true });
    } else if (!showing && wasMobileOpen.current && (compact || !open)) {
      if (
        active === document.body ||
        sidebarRef.current?.contains(active) ||
        active === dismissRef.current
      ) {
        const opener = openerRef.current;
        if (
          opener?.isConnected &&
          opener !== document.body &&
          !opener.closest("[inert]")
        )
          opener.focus({ preventScroll: true });
        else contentRef.current?.focus({ preventScroll: true });
      }
      openerRef.current = null;
    }
    // A breakpoint can hide the desktop rail without ever opening the mobile
    // drawer. Do not leave focus inside an inert rail or its hidden handle.
    if (
      (!open && sidebarRef.current?.contains(document.activeElement)) ||
      ((compact || collapsed) && document.activeElement === separatorRef.current)
    ) {
      contentRef.current?.focus({ preventScroll: true });
    }
    wasMobileOpen.current = showing;
  }, [compact, mobileOpen, open, collapsed]);

  useEffect(() => {
    setRailControl({
      collapsed: !open,
      toggle: () =>
        compact
          ? setMobileOpen((value) => !value)
          : setCollapsed((value) => !value),
    });
    return () => setRailControl(null);
  }, [open, compact]);

  useEffect(() => {
    if (!compact || !mobileOpen) return;
    const close = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !event.defaultPrevented)
        setMobileOpen(false);
    };
    document.addEventListener("keydown", close);
    return () => document.removeEventListener("keydown", close);
  }, [compact, mobileOpen]);

  return (
    <div
      className="workspace-split relative flex h-full min-h-0 overflow-hidden"
      data-dragging={dragging || undefined}
    >
      <aside
        ref={sidebarRef}
        tabIndex={-1}
        aria-label={t("rail.devices")}
        aria-hidden={!open}
        inert={!open}
        data-open={open}
        className="workspace-device-sidebar relative flex shrink-0 flex-col overflow-hidden bg-sidebar outline-none"
        style={{ width: compact ? undefined : open ? width : 0 }}
      >
        <MobileWorkspaceNavigation />
        <div className="min-h-0 flex-1 overflow-hidden">
        <DeviceRail
          selectedDevice={selectedDevice}
          onPick={(id) => {
            onDevicePick(id);
            if (compact) setMobileOpen(false);
          }}
        />
        </div>
      </aside>
      <div
        ref={separatorRef}
        role="separator"
        tabIndex={compact || collapsed ? -1 : 0}
        aria-hidden={compact || collapsed}
        aria-label={t("rail.resize")}
        aria-orientation="vertical"
        aria-valuemin={SIDEBAR_MIN_WIDTH}
        aria-valuemax={SIDEBAR_MAX_WIDTH}
        aria-valuenow={Math.round(width)}
        className="workspace-sidebar-handle relative z-30 w-0 shrink-0 cursor-col-resize outline-none max-md:hidden before:absolute before:inset-y-0 before:-left-2 before:w-4 after:absolute after:inset-y-0 after:left-0 after:w-px after:bg-border-seam hover:after:bg-timeline-accent focus-visible:after:bg-timeline-accent"
        style={{ display: collapsed ? "none" : undefined, touchAction: "none" }}
        onPointerDown={(event) => {
          if (event.button !== 0) return;
          event.preventDefault();
          event.currentTarget.setPointerCapture(event.pointerId);
          drag.current = { x: event.clientX, width };
          setDragging(true);
        }}
        onPointerMove={(event) => {
          if (drag.current)
            setWidth(
              clampSidebarWidth(
                drag.current.width + event.clientX - drag.current.x,
              ),
            );
        }}
        onLostPointerCapture={() => {
          drag.current = null;
          setDragging(false);
          rememberWidth(width);
        }}
        onDoubleClick={() => {
          setWidth(SIDEBAR_DEFAULT_WIDTH);
          rememberWidth(SIDEBAR_DEFAULT_WIDTH);
        }}
        onKeyDown={(event) => {
          const next =
            event.key === "ArrowLeft"
              ? width - 16
              : event.key === "ArrowRight"
                ? width + 16
                : event.key === "Home"
                  ? SIDEBAR_MIN_WIDTH
                  : event.key === "End"
                    ? SIDEBAR_MAX_WIDTH
                    : null;
          if (next === null) return;
          event.preventDefault();
          const value = clampSidebarWidth(next);
          setWidth(value);
          rememberWidth(value);
        }}
      />
      <div
        className="workspace-device-content relative min-w-0 flex-1"
        data-sidebar-open={compact && mobileOpen}
      >
        <div
          ref={contentRef}
          tabIndex={-1}
          className="flex h-full min-h-0 flex-col outline-none"
          inert={compact && mobileOpen}
        >
          <div className="flex h-12 shrink-0 items-center border-b border-border-seam px-3 md:hidden">
            <Button variant="ghost" size="icon" className={COARSE_POINTER_HEADER_ICON_BUTTON_CLASS}
              aria-label={t("rail.expand")} aria-expanded={mobileOpen} onClick={() => setMobileOpen(true)}>
              <Icon name="PanelLeft" />
            </Button>
          </div>
          <div className="min-h-0 flex-1">{children}</div>
        </div>
        {compact && mobileOpen && (
          <button
            ref={dismissRef}
            type="button"
            aria-label={t("rail.collapse")}
            className="absolute inset-0 cursor-default bg-transparent"
            onClick={() => setMobileOpen(false)}
          />
        )}
      </div>
    </div>
  );
}
