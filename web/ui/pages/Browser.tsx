import { useServerCapabilities } from "../lib/server-capabilities";
import { Navigate, useSearchParams } from "react-router-dom";
import { BrowserPanel } from "../components/browser-panel";
import { MobileSidebarTrigger, SplitPageShell } from "../components/split-layout";

export function BrowserPage() {
  const [params, setParams] = useSearchParams();
  const { browserEnabled, loading } = useServerCapabilities();
  if (loading) return null;
  if (!browserEnabled) return <Navigate to="/" replace />;
  const device = params.get("device") || "";
  return <SplitPageShell storageKey="rdev.browser.rail" selectedDevice={device} onDevicePick={id => setParams({ device: id })}>
    <div className="flex h-full min-h-0 flex-col">
      <div className="md:hidden"><MobileSidebarTrigger /></div>
      <BrowserPanel key={device} device={device} />
    </div>
  </SplitPageShell>;
}
