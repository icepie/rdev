// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { useTaskResource } from "../ui/pages/tasks/useTaskResource";

let root, host, current;
function Probe({ resourceKey, read, poll = true }) {
  current = useTaskResource(resourceKey, read, poll);
  return createElement("div", null, current.data || (current.loading ? "loading" : ""), current.error);
}
const render = props => act(async () => root.render(createElement(Probe, props)));
beforeEach(() => {
  vi.useFakeTimers();
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.useRealTimers();
  delete globalThis.IS_REACT_ACT_ENVIRONMENT;
});

it("coalesces slow reads and starts polling only after completion", async () => {
  const pending = Promise.withResolvers();
  const read = vi.fn(() => pending.promise);
  await render({ resourceKey: "list", read });
  await act(async () => { void current.refresh(); document.dispatchEvent(new Event("visibilitychange")); await vi.advanceTimersByTimeAsync(9000); });
  expect(read).toHaveBeenCalledTimes(1);
  await act(async () => pending.resolve("saved rows"));
  expect(current.loading).toBe(false);
  await act(async () => vi.advanceTimersByTimeAsync(2999));
  expect(read).toHaveBeenCalledTimes(1);
  await act(async () => vi.advanceTimersByTimeAsync(1));
  expect(read).toHaveBeenCalledTimes(2);
});

it("retains rows during refresh and failure, and retries a hung transport", async () => {
  const hung = Promise.withResolvers();
  const read = vi.fn().mockResolvedValueOnce("saved rows").mockImplementationOnce(() => hung.promise).mockResolvedValue("new rows");
  await render({ resourceKey: "list", read });
  await act(async () => vi.advanceTimersByTimeAsync(3000));
  expect(current.data).toBe("saved rows");
  expect(current.loading).toBe(false);
  await act(async () => vi.advanceTimersByTimeAsync(10000));
  expect(current.error).toContain("timed out");
  expect(current.data).toBe("saved rows");
  expect(read.mock.calls[1][0].aborted).toBe(true);
  await act(async () => vi.advanceTimersByTimeAsync(3000));
  expect(current.data).toBe("new rows");
  await act(async () => hung.resolve("stale timeout rows"));
  expect(current.data).toBe("new rows");
});

it("aborts and ignores old route responses even if transport ignores cancellation", async () => {
  const old = Promise.withResolvers();
  const read = vi.fn(() => old.promise);
  await render({ resourceKey: "task:old", read });
  const signal = read.mock.calls[0][0];
  await render({ resourceKey: "task:new", read: async () => "new task" });
  expect(signal.aborted).toBe(true);
  await act(async () => old.resolve("old task"));
  expect(current.data).toBe("new task");
  expect(current.error).toBe("");
});

it("does not poll hidden documents and refreshes when visible", async () => {
  const read = vi.fn().mockResolvedValue("rows");
  await render({ resourceKey: "list", read });
  Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
  await act(async () => vi.advanceTimersByTimeAsync(10000));
  expect(read).toHaveBeenCalledTimes(1);
  Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
  await act(async () => document.dispatchEvent(new Event("visibilitychange")));
  expect(read).toHaveBeenCalledTimes(2);
});
