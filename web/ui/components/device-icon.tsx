import { SERVER_AGENT_ID } from "./agent/protocol";
import { Icon } from "./icon";
import { OsIcon } from "./os-icon";
import { cn } from "../lib/utils";

/** Shared identity mark for device lists, conversation groups and pickers. */
export function DeviceIcon({ device, os, className }: { device: string; os?: string; className?: string }) {
  return device === SERVER_AGENT_ID
    ? <Icon name="Bot" className={cn("size-3.5 shrink-0", className)} style={{ color: "light-dark(#7c3aed, #b49aff)" }} aria-hidden="true" />
    : <OsIcon os={os} className={className} />;
}
