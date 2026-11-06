import { JSDOM } from "jsdom";
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
import { JobsPanel } from "../ui/components/agent/jobs-panel";

it("isolates detail requests by thread and refreshes on visibility without polling after disconnect", async () => {
  const dom = new JSDOM('<div id="root"></div>', { pretendToBeVisual: true });
  const keys = ["window", "document", "IS_REACT_ACT_ENVIRONMENT"];
  const previous = Object.fromEntries(
    keys.map((key) => [key, Object.getOwnPropertyDescriptor(globalThis, key)]),
  );
  Object.assign(globalThis, {
    window: dom.window,
    document: dom.window.document,
    IS_REACT_ACT_ENVIRONMENT: true,
  });
  vi.useFakeTimers();
  const sent = [];
  const props = {
    open: true,
    onOpenChange: () => {},
    full: false,
    ready: true,
    threadId: "thread-a",
    sendRaw: (payload) => {
      sent.push(payload);
      return true;
    },
    t: (key) => key,
    sink: {
      ops: { setJobs: () => {}, setJobsError: () => {} },
      state: {
        jobs: [
          {
            id: "job-a",
            label: "build",
            state: "running",
            kind: "shell",
            output: "\x1b[31m<build>\x1b[0m",
          },
        ],
      },
    },
  };
  const root = createRoot(document.getElementById("root"));
  const render = () => act(() => root.render(createElement(JobsPanel, props)));
  try {
    await render();
    await act(() =>
      document.querySelector('button[title="job-a: build"]').click(),
    );
    expect(document.querySelector("pre").textContent).toBe("<build>");
    expect(document.querySelector("pre").innerHTML).toContain("var(--ansi-1)");
    expect(document.querySelector("pre build")).toBeNull();
    expect(sent).toContainEqual({
      op: "get_jobs",
      thread: "thread-a",
      id: "job-a",
    });
    sent.length = 0;
    props.threadId = "thread-b";
    await render();
    expect(sent).toEqual([{ op: "get_jobs", thread: "thread-b" }]);
    sent.length = 0;
    await act(() =>
      document.dispatchEvent(new window.Event("visibilitychange")),
    );
    expect(sent).toEqual([{ op: "get_jobs", thread: "thread-b" }]);
    sent.length = 0;
    await act(async () => vi.advanceTimersByTimeAsync(6000));
    expect(sent).toEqual(Array.from({ length: 3 }, () => ({ op: "get_jobs", thread: "thread-b" })));
    Object.defineProperty(document, "hidden", { configurable: true, value: true });
    await act(() => document.dispatchEvent(new window.Event("visibilitychange")));
    expect(vi.getTimerCount()).toBe(0);
    sent.length = 0;
    await act(async () => vi.advanceTimersByTimeAsync(20000));
    expect(sent).toEqual([]);
    Object.defineProperty(document, "hidden", { configurable: true, value: false });
    await act(() => document.dispatchEvent(new window.Event("visibilitychange")));
    expect(sent).toEqual([{ op: "get_jobs", thread: "thread-b" }]);
    // Repeated visibility events replace the timer instead of multiplying it.
    await act(() => document.dispatchEvent(new window.Event("visibilitychange")));
    sent.length = 0;
    await act(async () => vi.advanceTimersByTimeAsync(2000));
    expect(sent).toEqual([{ op: "get_jobs", thread: "thread-b" }]);
    props.ready = false;
    await render();
    sent.length = 0;
    await act(() =>
      document.dispatchEvent(new window.Event("visibilitychange")),
    );
    const stop = [...document.querySelectorAll("button")].find(
      (button) => button.textContent === "agent.cmd.stop",
    );
    expect(vi.getTimerCount()).toBe(0);
    await act(async () => vi.advanceTimersByTimeAsync(10000));
    expect(stop.disabled).toBe(true);
    await act(() => stop.click());
    expect(sent).toEqual([]);
    props.ready = true;
    props.threadId = "thread-a";
    props.sink.state.jobs = [];
    await render();
    expect(sent).toEqual([{ op: "get_jobs", thread: "thread-a" }]);
  } finally {
    await act(() => root.unmount());
    expect(vi.getTimerCount()).toBe(0);
    vi.useRealTimers();
    dom.window.close();
    for (const [key, descriptor] of Object.entries(previous)) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else delete globalThis[key];
    }
  }
});
