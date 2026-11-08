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
            detailState: "running",
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
    expect(document.querySelector("[class*=select-text]").textContent).toBe("<build>");
    expect(document.querySelector("[class*=select-text]").innerHTML).toContain("var(--ansi-1)");
    expect(document.querySelector("build")).toBeNull();
    expect(sent).toContainEqual({
      op: "get_jobs",
      thread: "thread-a",
      id: "job-a",
    });
    sent.length = 0;
    props.sink.state.jobsRevision = 1;
    await render();
    expect(sent).toEqual([{op:'get_jobs',thread:'thread-a'},{op:'get_jobs',thread:'thread-a',id:'job-a'}]);
    props.sink.state.jobs[0] = {...props.sink.state.jobs[0], cancelRequested: true};
    await render();
    const stoppingButton = [...document.querySelectorAll('button')].find(button => button.textContent === 'agent.cmd.stop');
    expect(stoppingButton.disabled).toBe(true);
    const countBeforeCancel = sent.length;
    await act(() => stoppingButton.click());
    expect(sent).toHaveLength(countBeforeCancel);
    expect(document.body.textContent).toContain('agent.jobs.canceling');
    props.sink.state.jobs[0] = {...props.sink.state.jobs[0], cancelRequested: false};
    props.sink.state.jobs[0] = {
      ...props.sink.state.jobs[0], state: "completed", output: "background verified\n",
      result: "exit code: 0\n<stdout>\nbackground verified\n\n</stdout>\n<stderr>\n\n</stderr>",
    };
    await render();
    const result = document.querySelector("[class*=select-text]");
    expect(result.textContent.match(/background verified/g)).toHaveLength(1);
    expect(result.textContent).toContain("exit code 0");
    expect(result.textContent).not.toContain("<stdout>");
    expect(result.textContent).not.toContain("<stderr>");
    // A terminal list update is not a final detail reply, even if an earlier
    // running detail supplied a result field. Keep requesting the selected job.
    sent.length = 0;
    await act(async () => vi.advanceTimersByTimeAsync(2000));
    expect(sent).toContainEqual({op:"get_jobs",thread:"thread-a",id:"job-a"});
    props.sink.state.jobs[0] = {
      ...props.sink.state.jobs[0], detailState:"completed", output:"", result:"",
    };
    await render();
    expect(document.body.textContent).toContain("agent.jobs.noOutput");
    expect(document.body.textContent).not.toContain("agent.jobs.waiting");
    sent.length = 0;
    await act(async () => vi.advanceTimersByTimeAsync(6000));
    expect(sent).toEqual(Array.from({length:3}, () => ({op:"get_jobs",thread:"thread-a"})));
    props.sink.state.jobs[0] = {
      ...props.sink.state.jobs[0], state: "failed", error: "command exited with code 2",
      result: "exit code: 2\n<stdout>\n\n</stdout>\n<stderr>\n\u001b[31mbuild failed\u001b[0m\n</stderr>",
    };
    await render();
    expect(document.querySelector("[class*=select-text]").textContent).toContain("build failed");
    expect(document.querySelector("[class*=select-text]").textContent).toContain("exit code 2");
    expect(document.querySelector('[role="alert"]').textContent).toContain("command exited with code 2");
    sent.length = 0;
    props.sink.state.jobs[0] = { ...props.sink.state.jobs[0], state: "running" };
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
    props.sink.state.jobsRevision = 2;
    await render();
    expect(sent).toEqual([]);
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
