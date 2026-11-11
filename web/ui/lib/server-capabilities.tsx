import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { authURL } from "./api";
const ServerCapabilitiesContext = createContext({ browserEnabled: false, devicePasswordInputsHidden: false, loading: false });

/** Unknown capabilities fail closed, including when connected to older servers. */
export function ServerCapabilitiesProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState({ browserEnabled: false, devicePasswordInputsHidden: false, loading: true });
  useEffect(() => {
    let controller: AbortController | null = null;
    const load = () => {
      controller?.abort();
      const requestController = new AbortController();
      controller = requestController;
      void fetch(authURL("/api/config"), { signal: requestController.signal })
        .then(response => {
          if (!response.ok) throw new Error("server capabilities unavailable");
          return response.json();
        })
        .then(config => {
          if (!requestController.signal.aborted) {
            setState({
              browserEnabled: config.browserEnabled === "true",
              devicePasswordInputsHidden: config.deviceAuthBypassed === "true",
              loading: false,
            });
          }
        })
        .catch(() => {
          if (!requestController.signal.aborted) setState({ browserEnabled: false, devicePasswordInputsHidden: false, loading: false });
        });
    };
    load();
    window.addEventListener("rdev-admin-token", load);
    return () => {
      window.removeEventListener("rdev-admin-token", load);
      controller?.abort();
    };
  }, []);
  return <ServerCapabilitiesContext.Provider value={state}>{children}</ServerCapabilitiesContext.Provider>;
}

export function useServerCapabilities() { return useContext(ServerCapabilitiesContext); }
