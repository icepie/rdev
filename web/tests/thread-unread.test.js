// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it } from "vitest";
import { parseAllThreads } from "../ui/components/agent/protocol";
import { threadIdentity, useThreadUnread } from "../ui/hooks/use-thread-unread";

it("tracks completion across devices and background work, clearing only visible conversations", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  const root = createRoot(host);
  let unread;
  let rows = [{ device: "a", id: "same", running: false }, { device: "b", id: "same", running: false }];
  let device = "a";
  let openIds = new Set(["same"]);
  function Harness() { unread = useThreadUnread(rows, device, openIds); return null; }
  const render = () => act(async () => root.render(createElement(Harness)));
  const visibility = async value => {
    Object.defineProperty(document, "visibilityState", { configurable: true, value });
    await act(async () => document.dispatchEvent(new Event("visibilitychange")));
  };
  try {
    await visibility("visible");
    await render();
    expect(unread.size).toBe(0);
    rows = rows.map(row => ({ ...row, running: true }));
    await render();
    rows = rows.map(row => ({ ...row, running: false, backgroundJobs: row.device === "b" ? 1 : 0 }));
    await render();
    expect(unread.size).toBe(0);
    rows = rows.map(row => ({ ...row, backgroundJobs: 0 }));
    await render();
    expect([...unread]).toEqual([threadIdentity("b", "same")]);
    device = "b";
    await render();
    expect(unread.size).toBe(0);
    await visibility("hidden");
    rows = rows.map(row => ({ ...row, running: true }));
    await render();
    rows = rows.map(row => ({ ...row, running: false }));
    await render();
    expect(unread.size).toBe(2);
    await visibility("visible");
    expect([...unread]).toEqual([threadIdentity("a", "same")]);
  } finally {
    await act(async () => root.unmount());
    delete document.visibilityState;
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});

it("restores unread and observes a completion across reload without marking old history", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  sessionStorage.clear();
  const openIds = new Set();
  let rows = [{ device: "a", id: "job", running: true }];
  let unread;
  function Harness() {
    const decoded = rows.flatMap(row => parseAllThreads([{ device: row.device, threads: [row] }])
      .flatMap(group => group.threads.map(thread => ({ ...thread, device: group.device }))));
    unread = useThreadUnread(decoded, "a", openIds);
    return null;
  }
  let root = createRoot(document.createElement("div"));
  try {
    await act(async () => root.render(createElement(Harness)));
    await act(async () => root.unmount());
    rows = [{ device: "a", id: "job", running: false }, { device: "a", id: "old", running: false }];
    root = createRoot(document.createElement("div"));
    await act(async () => root.render(createElement(Harness)));
    expect([...unread]).toEqual([threadIdentity("a", "job")]);
    await act(async () => root.unmount());
    root = createRoot(document.createElement("div"));
    await act(async () => root.render(createElement(Harness)));
    expect([...unread]).toEqual([threadIdentity("a", "job")]);
  } finally {
    await act(async () => root.unmount());
    sessionStorage.clear();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});

it("detects unseen turns across reload without treating renames or initial history as unread", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  sessionStorage.clear();
  let rows = [
    { device: "a", id: "thread", running: false, outcomeAt: "completion-1" },
    { device: "b", id: "thread", running: false },
  ];
  let openIds = new Set();
  let unread;
  function Harness() {
    const decoded = rows.flatMap(row => parseAllThreads([{ device: row.device, threads: [row] }])
      .flatMap(group => group.threads.map(thread => ({ ...thread, device: group.device }))));
    unread = useThreadUnread(decoded, "a", openIds);
    return null;
  }
  let root = createRoot(document.createElement("div"));
  const render = () => act(async () => root.render(createElement(Harness)));
  try {
    await render();
    expect(unread.size).toBe(0);
    rows = rows.map(row => ({ ...row, title: "renamed", updatedAt: "newer" }));
    await render();
    expect(unread.size).toBe(0);
    await act(async () => root.unmount());
    rows = rows.map(row => ({ ...row, outcomeAt: "completion-2" }));
    root = createRoot(document.createElement("div"));
    await render();
    expect(unread.size).toBe(2);
    openIds = new Set(["thread"]);
    await render();
    expect([...unread]).toEqual([threadIdentity("b", "thread")]);
    // Reset and metadata updates do not manufacture another completion.
    rows = rows.map(row => ({ ...row, outcomeAt: undefined }));
    openIds = new Set();
    await render();
    expect([...unread]).toEqual([threadIdentity("b", "thread")]);
  } finally {
    await act(async () => root.unmount());
    sessionStorage.clear();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
