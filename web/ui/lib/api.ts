/** Admin-token and HTTP/WS plumbing. Token travels as `?token=` query param,
 * matching the server's auth contract; it is persisted in localStorage once
 * seen in the URL so bare reloads keep working. */

const TOKEN_KEY = "rdevAdminToken";

let adminToken = (() => {
  const fromURL = new URLSearchParams(window.location.search).get("token");
  if (fromURL) {
    localStorage.setItem(TOKEN_KEY, fromURL);
    return fromURL;
  }
  return localStorage.getItem(TOKEN_KEY) || "";
})();

/** Called by the app when a request fails with 401. Should prompt and return
 * a new token, or null when the user cancels. */
let onUnauthorized: (() => Promise<string | null>) | null = null;
export function setUnauthorizedHandler(fn: () => Promise<string | null>) {
  onUnauthorized = fn;
}

let tokenGen = 0;
export function setAdminToken(token: string) {
  adminToken = token;
  tokenGen++;
  localStorage.setItem(TOKEN_KEY, token);
}

export function authURL(path: string): string {
  if (!adminToken) return path;
  return (
    path + (path.includes("?") ? "&" : "?") + "token=" + encodeURIComponent(adminToken)
  );
}

export class UnauthorizedError extends Error {
  constructor() {
    super("unauthorized");
    this.name = "UnauthorizedError";
  }
}

/** fetch with token attached; on 401 asks the handler once for a new token
 * and retries. Throws UnauthorizedError when no token is provided. */
export async function apiFetch(
  path: string,
  init?: RequestInit,
): Promise<Response> {
  const sentGen = tokenGen;
  let r = await fetch(authURL(path), init);
  if (r.status === 401 && onUnauthorized) {
    if (tokenGen !== sentGen && adminToken) {
      // Token was refreshed while this request was in flight; the 401 is
      // stale — retry with the new token instead of prompting again.
      r = await fetch(authURL(path), init);
    } else {
      const token = await onUnauthorized();
      if (token) {
        setAdminToken(token);
        r = await fetch(authURL(path), init);
      }
    }
  }
  if (r.status === 401) throw new UnauthorizedError();
  return r;
}

export async function apiJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const r = await apiFetch(path, init);
  if (!r.ok) throw new Error(`${r.status} ${r.statusText}`);
  return (await r.json()) as T;
}

export function wsURL(path: string): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${location.host}${authURL(path)}`;
}

/** True when the server requires an admin token and we hold a valid one, in
 * which case the server skips per-device password checks (see
 * Server.deviceAuthBypassed). Cached; retries after a failed fetch (e.g. the
 * config request raced the first 401 token prompt). */
let bypassCache: Promise<boolean> | null = null;
export function deviceAuthBypassed(): Promise<boolean> {
  if (!bypassCache) {
    bypassCache = apiJSON<{ authRequired?: string }>("/api/config")
      .then((c) => c.authRequired === "true" && adminToken !== "")
      .catch(() => {
        bypassCache = null;
        return false;
      });
  }
  return bypassCache;
}

/* ── Per-device remembered passwords (unchanged storage contract) ── */

function passwordKey(deviceId: string): string {
  return "rdev.devicePassword." + deviceId;
}

export function getDevicePassword(deviceId: string): string {
  if (!deviceId) return "";
  return localStorage.getItem(passwordKey(deviceId)) || "";
}

export function rememberDevicePassword(deviceId: string, password: string) {
  if (!deviceId || !password) return;
  localStorage.setItem(passwordKey(deviceId), password);
}

export function forgetDevicePassword(deviceId: string) {
  if (!deviceId) return;
  localStorage.removeItem(passwordKey(deviceId));
}

/* ── Device list (shared shape with /api/clients) ── */

export interface DeviceInfo {
  id: string;
  hostname?: string;
  os?: string;
  arch?: string;
  version?: string;
  hasPassword: boolean;
  /* Real access password, only present when fetched with ?passwords=1. */
  password?: string;
  connectedAt: string;
  remoteAddr?: string;
  [k: string]: unknown;
}

export function fetchDevices(): Promise<DeviceInfo[]> {
  return apiJSON<DeviceInfo[]>("/api/clients");
}
