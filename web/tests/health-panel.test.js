// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
const api = vi.hoisted(() => ({ apiJSON: vi.fn() }));
vi.mock("../ui/lib/api", () => api);
import { HealthPanel } from "../ui/components/health-panel";

const healthy = {
  status: "ok", at: "2026-11-15T00:00:00Z", uptimeSeconds: 12,
  resources: { transfers: 1, agentTransfers: 0, agentRunning: 0, agentQueued: 0 },
  scheduler: { initialized: false, pending: 0, running: 0 },
  history: { loaded: 0, faulted: 0, faults: [] },
  identity: { initialized: false, durable: true, persistenceFault: false },
  transferFailures: { device: 0, transport: 0 },
};

it("keeps observed health while a refresh stalls, bounds requests and cancels on unmount", async () => {
  vi.useFakeTimers();
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const signals = [];
  api.apiJSON.mockReset().mockResolvedValueOnce(healthy).mockImplementation((_path, { signal }) => new Promise((_resolve, reject) => {
    signals.push(signal);
    signal.addEventListener("abort", () => reject(new Error("request timed out")), { once: true });
  }));
  const host = document.createElement("div"); document.body.append(host);
  const root = createRoot(host);
  try {
    await act(async () => root.render(createElement(HealthPanel)));
    expect(host.querySelector("dd").textContent).toBe("1");
    await act(async () => vi.advanceTimersByTimeAsync(10000));
    await act(async () => vi.advanceTimersByTimeAsync(5000));
    expect(api.apiJSON).toHaveBeenCalledTimes(2);
    expect(host.querySelector("dd").textContent).toBe("1");
    await act(async () => vi.advanceTimersByTimeAsync(5000));
    expect(signals[0].aborted).toBe(true);
    expect(host.querySelector('[role="alert"]').textContent).toContain("request timed out");
    expect(host.querySelector("dd").textContent).toBe("1");
    await act(async () => vi.advanceTimersByTimeAsync(10000));
    await act(async () => root.unmount());
    expect(signals[1].aborted).toBe(true);
  } finally {
    await act(async () => root.unmount());
    host.remove(); vi.useRealTimers(); delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
