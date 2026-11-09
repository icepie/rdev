import { createContext, useContext, useEffect, useState, type ReactNode } from "react";

const ServerCapabilitiesContext = createContext({ browserEnabled: false, loading: false });

/** Unknown capabilities fail closed, including when connected to older servers. */
export function ServerCapabilitiesProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState({ browserEnabled: false, loading: true });
  useEffect(() => {
    const controller = new AbortController();
    void fetch("/api/config", { signal: controller.signal })
      .then(response => {
        if (!response.ok) throw new Error("server capabilities unavailable");
        return response.json();
      })
      .then(config => {
        if (!controller.signal.aborted) setState({ browserEnabled: config.browserEnabled === "true", loading: false });
      })
      .catch(() => {
        if (!controller.signal.aborted) setState({ browserEnabled: false, loading: false });
      });
    return () => controller.abort();
  }, []);
  return <ServerCapabilitiesContext.Provider value={state}>{children}</ServerCapabilitiesContext.Provider>;
}

export function useServerCapabilities() { return useContext(ServerCapabilitiesContext); }
