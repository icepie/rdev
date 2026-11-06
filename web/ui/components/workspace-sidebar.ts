/** Shared BB sidebar geometry for Agent and device workspaces. */
export const SIDEBAR_MIN_WIDTH = 240;
export const SIDEBAR_MAX_WIDTH = 460;
export const SIDEBAR_DEFAULT_WIDTH = 312;
export const SIDEBAR_ROW_CLASS =
  "flex h-8 pointer-coarse:h-10 w-full cursor-pointer items-center gap-1.5 rounded-md px-2 text-left text-sm text-sidebar-foreground transition-colors hover:bg-sidebar-accent hover:text-sidebar-accent-foreground";

export function clampSidebarWidth(width: number): number {
  return Number.isFinite(width)
    ? Math.min(SIDEBAR_MAX_WIDTH, Math.max(SIDEBAR_MIN_WIDTH, width))
    : SIDEBAR_DEFAULT_WIDTH;
}
