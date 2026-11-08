import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { authURL, fetchDevices, UnauthorizedError, type DeviceInfo } from "./api";

interface DevicesCtx {
  devices: DeviceInfo[];
  error: string;
  loading: boolean;
  refresh: () => void;
}

const Ctx = createContext<DevicesCtx>({ devices: [], error: "", loading: true, refresh: () => {} });

export function DevicesProvider({ children }: { children: ReactNode }) {
  const [devices, setDevices] = useState<DeviceInfo[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [tick, setTick] = useState(0);

  const refresh = useCallback(() => {
    setLoading(true);
    setTick((n) => n + 1);
  }, []);

  useEffect(() => {
    let cancelled = false;
    let inFlight = false;
    let dirty = false;
    let streamConnected = false;
    let lastLoaded = 0;
    let events: EventSource | null = null;
    let eventURL = "";
    const connectEvents = () => {
      if (typeof EventSource === "undefined") return;
      const url = authURL("/api/clients/events");
      if (events && url === eventURL) return;
      events?.close();
      streamConnected = false;
      eventURL = url;
      events = new EventSource(url);
      events.onopen = () => { streamConnected = true; };
      events.onerror = () => { streamConnected = false; };
      events.addEventListener("devices", load);
    };
    const load = async () => {
      if (cancelled || document.hidden) return;
      if (inFlight) { dirty = true; return; }
      dirty = false;
      inFlight = true;
      try {
        const list = await fetchDevices();
        if (cancelled) return;
        const next = Array.isArray(list) ? list : [];
        setDevices(previous => JSON.stringify(previous) === JSON.stringify(next) ? previous : next);
        setError("");
        lastLoaded = Date.now();
        // Auth may have been refreshed by fetchDevices. Reconnect with that
        // token rather than endlessly retrying an expired stream URL.
        connectEvents();
      } catch (e) {
        if (!cancelled && !(e instanceof UnauthorizedError)) {
          setError(e instanceof Error ? e.message : String(e));
        }
      } finally {
        inFlight = false;
        if (!cancelled) {
          setLoading(false);
          if (dirty) void load();
        }
      }
    };
    void load();
    // Fast fallback for old servers/proxies; healthy streams only poll
    // occasional session counters, which do not emit presence changes.
    const timer = setInterval(() => {
      if (!streamConnected || Date.now() - lastLoaded >= 30000) void load();
    }, 5000);
    document.addEventListener("visibilitychange", load);
    return () => {
      cancelled = true;
      clearInterval(timer);
      events?.close();
      document.removeEventListener("visibilitychange", load);
    };
  }, [tick]);

  const value = useMemo(() => ({ devices, error, loading, refresh }), [devices, error, loading, refresh]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useDevices(): DevicesCtx {
  return useContext(Ctx);
}
