import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import { fetchDevices, UnauthorizedError, type DeviceInfo } from "./api";

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
    const load = async () => {
      if (cancelled || inFlight || document.hidden) return;
      inFlight = true;
      try {
        const list = await fetchDevices();
        if (cancelled) return;
        setDevices(Array.isArray(list) ? list : []);
        setError("");
      } catch (e) {
        if (!cancelled && !(e instanceof UnauthorizedError)) {
          setError(e instanceof Error ? e.message : String(e));
        }
      } finally {
        inFlight = false;
        if (!cancelled) setLoading(false);
      }
    };
    void load();
    const timer = setInterval(load, 5000);
    document.addEventListener("visibilitychange", load);
    return () => {
      cancelled = true;
      clearInterval(timer);
      document.removeEventListener("visibilitychange", load);
    };
  }, [tick]);

  return <Ctx.Provider value={{ devices, error, loading, refresh }}>{children}</Ctx.Provider>;
}

export function useDevices(): DevicesCtx {
  return useContext(Ctx);
}
