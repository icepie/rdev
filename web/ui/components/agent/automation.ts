/** Server-supplied, conversation-scoped view; capabilities stay in memory. */
export interface AutomationView {
  kind: "browser" | "desktop";
  device: string;
  target?: string;
  source?: string;
  base?: string;
  action: string;
  paused: boolean;
}
export function parseAutomation(value: unknown): AutomationView | null {
  if (!value || typeof value !== "object") return null;
  const v = value as Record<string, unknown>;
  if ((v.kind !== "browser" && v.kind !== "desktop") || typeof v.device !== "string" || !v.device) return null;
  if (v.kind === "browser" && (typeof v.base !== "string" || !/^\/api\/browser\/view\/[a-f0-9]{64}$/.test(v.base) || typeof v.target !== "string" || !/^[a-zA-Z0-9_-]+$/.test(v.target))) return null;
  return { kind: v.kind, device: v.device, action: typeof v.action === "string" ? v.action : "", paused: v.paused === true, source: typeof v.source === "string" ? v.source : undefined,
    ...(v.kind === "browser" ? { base: v.base as string, target: v.target as string } : {}) };
}
