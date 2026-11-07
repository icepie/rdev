// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { ThreadNavigation } from "../ui/components/agent/thread-navigation";

it("indexes both authors and jumps within the owning pane", async () => {
 globalThis.IS_REACT_ACT_ENVIRONMENT = true;
 const host = document.createElement("div"); document.body.append(host);
 const scroll = document.createElement("div");
 const target = document.createElement("div"); target.dataset.threadMessage = "4"; scroll.scrollTo = vi.fn(); Object.defineProperty(scroll, "clientWidth", { value: 1000 }); scroll.append(target);
 const root = createRoot(host);
 const items = [1,2,3].map(key => ({key,kind:"user",text:`question ${key}`}));
 items.push({key:4,kind:"turn",text:"answer"});
 try {
  await act(async () => root.render(createElement(ThreadNavigation,{items, scrollRef:{current:scroll}, t:key=>key})));
  await act(async () => host.querySelector("button").click());
  expect(host.textContent).toContain("question 1");
  await act(async () => [...host.querySelectorAll("button")].find(button=>button.textContent==="agent.navigation.agent").click());
  await act(async () => [...host.querySelectorAll("button")].find(button=>button.textContent==="answer").click());
  expect(scroll.scrollTo).toHaveBeenCalledOnce();
  expect(host.querySelector('[aria-current="true"]').textContent).toBe("answer");
 } finally { await act(async () => root.unmount()); host.remove(); delete globalThis.IS_REACT_ACT_ENVIRONMENT; }
});

it("hides navigation in narrow split panes and bounds the wide rail to 20 ticks", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const scroll = document.createElement("div");
  let width = 1200;
  Object.defineProperty(scroll, "clientWidth", { get: () => width });
  const items = Array.from({ length: 60 }, (_, key) => ({ key, kind: "user", text: `question ${key}` }));
  const ref = { current: scroll };
  try {
    await act(async () => root.render(createElement(ThreadNavigation, { items, scrollRef: ref, t: key => key })));
    await act(async () => new Promise(resolve => requestAnimationFrame(resolve)));
    expect(host.querySelectorAll('button[aria-controls] > span')).toHaveLength(20);
    await act(async () => host.querySelector("button").click());
    expect(host.textContent).toContain("question 59");
    width = 500;
    await act(async () => {
      scroll.dispatchEvent(new Event("scroll"));
      await new Promise(resolve => requestAnimationFrame(resolve));
    });
    expect(host.querySelector("button")).toBeNull();
    width = 1200;
    await act(async () => {
      scroll.dispatchEvent(new Event("scroll"));
      await new Promise(resolve => requestAnimationFrame(resolve));
    });
    expect(host.querySelector("button").getAttribute("aria-expanded")).toBe("false");
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
