import { useSyncExternalStore } from "react";

/* Global slot for the current page's collapsible rail: SplitPageShell
 * registers its device rail here, and the app shell renders the toggle in
 * the logo slot at the top of the nav rail (bb: the sidebar trigger lives
 * in the window's top-left corner and never moves). Pages without a rail
 * register nothing and keep the logo. */

export interface RailControl {
  collapsed: boolean;
  toggle: () => void;
}

let current: RailControl | null = null;
const listeners = new Set<() => void>();

export function setRailControl(control: RailControl | null) {
  current = control;
  listeners.forEach((l) => l());
}

export function useRailControl(): RailControl | null {
  return useSyncExternalStore(
    (cb) => {
      listeners.add(cb);
      return () => listeners.delete(cb);
    },
    () => current,
  );
}
