import { useContext, useEffect, useRef, useState, type ReactNode } from "react";
import {
  Panel,
  PanelGroup,
  PanelResizeHandle,
  type ImperativePanelHandle,
} from "react-resizable-panels";
import { Icon } from "./icon";
import { useI18n } from "../lib/i18n";
import { DeviceRail } from "./device-rail";
import { MobileNavContext } from "../shell/AppShell";
import { setRailControl } from "../lib/rail-control";

const HANDLE_BASE =
  "group relative z-10 shrink-0 bg-transparent transition-colors";

/* Page shell: draggable device rail on the left (agent-sidebar style,
 * width persisted per page), content on the right. The rail is hidden on
 * narrow screens like the agent page's side panels. */
export function SplitPageShell({
  storageKey,
  onDevicePick,
  children,
}: {
  storageKey: string;
  onDevicePick: (deviceId: string) => void;
  children: ReactNode;
}) {
  const railRef = useRef<ImperativePanelHandle>(null);
  const [railCollapsed, setRailCollapsed] = useState(false);
  const mobileNav = useContext(MobileNavContext);
  const { t } = useI18n();

  /* The rail toggle lives in the nav rail's logo slot (window top-left,
   * bb window-chrome style); register it while this page is mounted. */
  useEffect(() => {
    setRailControl({
      collapsed: railCollapsed,
      toggle: () =>
        railCollapsed
          ? railRef.current?.expand()
          : railRef.current?.collapse(),
    });
    return () => setRailControl(null);
  }, [railCollapsed]);

  return (
    <PanelGroup direction="horizontal" autoSaveId={storageKey} className="h-full min-h-0">
      <Panel
        ref={railRef}
        defaultSize={16}
        minSize={10}
        maxSize={40}
        collapsible
        collapsedSize={0}
        onCollapse={() => setRailCollapsed(true)}
        onExpand={() => setRailCollapsed(false)}
        className="max-[767px]:hidden"
      >
        <DeviceRail onPick={onDevicePick} onCollapse={() => railRef.current?.collapse()} />
      </Panel>
      <PanelResizeHandle
        className={`${HANDLE_BASE} w-1 cursor-col-resize bg-border-seam/60 hover:bg-timeline-accent data-[resize-handle-state=drag]:bg-timeline-accent max-[767px]:hidden ${railCollapsed ? "hidden" : ""}`}
      />
      <Panel minSize={30} className="min-w-0">
        <div className="relative flex h-full min-h-0 flex-col">
          {/* Narrow screens: ONE in-flow trigger (bb style) opening the
           * global drawer with this page's device rail as its section. */}
          <div className="flex h-9 shrink-0 items-center gap-1 border-b border-border-seam bg-card px-1.5 md:hidden">
            <button
              type="button"
              aria-label={t("nav.label")}
              onClick={() =>
                mobileNav.open(
                  <DeviceRail
                    onPick={(id) => {
                      onDevicePick(id);
                      mobileNav.close();
                    }}
                    onCollapse={mobileNav.close}
                  />,
                )
              }
              className="flex size-7 cursor-pointer items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground"
            >
              <Icon name="AlignLeft" className="size-4" />
            </button>
          </div>
          <div className="relative min-h-0 flex-1">{children}</div>
        </div>
      </Panel>
    </PanelGroup>
  );
}
