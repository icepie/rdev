// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
import { AskCard } from "../ui/components/agent/ask-card";

it.each([["click", false], ["Enter", false], ["click", true], ["Enter", true]])("submits text with %s (options=%s) without treating IME confirmation as submission", async (method, hasOptions) => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const onAnswer = vi.fn();
  try {
    await act(async () => root.render(createElement(AskCard, {
      item: { kind: "ask", id: "question", question: "Which path?", options: hasOptions ? [{ label: "Preset" }] : [], answer: null },
      onAnswer, t: key => key,
    })));
    const submit = [...host.querySelectorAll("button")].find(button => button.textContent === "agent.askSubmit");
    expect(submit.disabled).toBe(true);
    if (hasOptions) {
      await act(async () => [...host.querySelectorAll("button")].find(button => button.textContent === "agent.askOther").click());
    }
    const input = host.querySelector("textarea");
    const edit = value => act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value").set.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await edit("   ");
    expect(submit.disabled).toBe(true);
    await edit("  /项目  ");
    expect(submit.disabled).toBe(false);
    for (const flags of [{ isComposing: true }, { keyCode: 229 }, { shiftKey: true }]) {
      await act(async () => input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, ...flags })));
    }
    expect(onAnswer).not.toHaveBeenCalled();
    await act(async () => method === "click" ? submit.click() : input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    expect(onAnswer).toHaveBeenCalledExactlyOnceWith("question", "/项目");
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
