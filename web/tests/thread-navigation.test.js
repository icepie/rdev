// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { ThreadNavigation } from "../ui/components/agent/thread-navigation";

it("indexes both authors and jumps within the owning pane", async () => {
 globalThis.IS_REACT_ACT_ENVIRONMENT = true;
 const host = document.createElement("div"); document.body.append(host);
 const scroll = document.createElement("div");
 const target = document.createElement("div"); target.dataset.threadMessage = "4"; target.scrollIntoView = vi.fn(); scroll.append(target);
 const root = createRoot(host);
 const items = [1,2,3].map(key => ({key,kind:"user",text:`question ${key}`}));
 items.push({key:4,kind:"turn",text:"answer"});
 try {
  await act(async () => root.render(createElement(ThreadNavigation,{items, scrollRef:{current:scroll}, t:key=>key})));
  await act(async () => host.querySelector("button").click());
  expect(host.textContent).toContain("question 1");
  await act(async () => [...host.querySelectorAll("button")].find(button=>button.textContent==="agent.navigation.agent").click());
  await act(async () => [...host.querySelectorAll("button")].find(button=>button.textContent==="answer").click());
  expect(target.scrollIntoView).toHaveBeenCalledOnce();
  expect(host.querySelector('[aria-current="true"]').textContent).toBe("answer");
 } finally { await act(async () => root.unmount()); host.remove(); delete globalThis.IS_REACT_ACT_ENVIRONMENT; }
});
