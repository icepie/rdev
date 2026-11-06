// @vitest-environment jsdom
import { JSDOM } from "jsdom";
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";

vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
vi.mock("../ui/components/os-icon", () => ({ OsIcon: () => null }));
vi.mock("../ui/components/tooltip", () => ({ Tooltip: ({ children }) => children, TooltipTrigger: ({ children }) => children, TooltipContent: () => null }));
vi.mock("../ui/components/agent/jobs-panel", () => ({ JobsPanel: () => null }));
vi.mock("../ui/components/agent/cwd-picker", () => ({ CwdPicker: () => null }));
vi.mock("../ui/components/agent/turn-view", () => ({ TurnView: () => null }));
import { ChatPaneView } from "../ui/components/agent/chat-pane";

it("preserves composer intent across modified keys, image loading and thread switches", async () => {
  const dom = new JSDOM('<div id="root"></div>', { pretendToBeVisual: true });
  const readers = [];
  class Reader {
    abort = vi.fn();
    constructor() { readers.push(this); }
    readAsDataURL() {}
    finish(data) { this.result = data; this.onload?.(); }
  }
  const replacements = {
    window: dom.window, document: dom.window.document, navigator: dom.window.navigator,
    HTMLElement: dom.window.HTMLElement, MutationObserver: dom.window.MutationObserver,
    FileReader: Reader, IS_REACT_ACT_ENVIRONMENT: true,
  };
  const previous = Object.keys(replacements).map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]);
  for (const [key, value] of Object.entries(replacements)) Object.defineProperty(globalThis, key, { configurable: true, value });
  const sent = [];
  let insertDraft;
  const props = {
    sink: { state: { items: [], todos: [], queue: [], running: false, stopping: false, turnUsage: null, currentTool: "", workingSince: null },
      ops: { registerScrollEl() {}, pushBubble: vi.fn(), setRunning() {}, setWorkingSince() {} } },
    t: key => key, threadId: "thread-a", device: "device", ready: true,
    sendRaw: payload => { sent.push(payload); return true; },
    emptyHint: "empty", copiedId: null, onCopy() {}, model: "model", models: [],
    onModelChange() {}, reasoning: "medium", onReasoningChange() {}, permMode: "write", onPermModeChange() {}, connStatus: "connected",
    onRegisterAddToChat: insert => { insertDraft = insert; },
  };
  const root = createRoot(document.getElementById("root"));
  const render = () => act(async () => root.render(createElement(ChatPaneView, props)));
  const attach = async name => {
    const picker = document.querySelector('input[type="file"]');
    Object.defineProperty(picker, "files", { configurable: true, value: [new dom.window.File(["image"], name, { type: "image/png" })] });
    await act(async () => picker.dispatchEvent(new dom.window.Event("change", { bubbles: true })));
  };
  const sendButton = () => document.querySelector('button[aria-label="agent.send"]');
  try {
    await render();
    await act(async () => insertDraft("/reset"));
    const composer = document.querySelector("textarea");
    for (const init of [
      { key: "Enter", shiftKey: true },
      { key: "Tab", shiftKey: true },
      { key: "Enter", isComposing: true },
      { key: "Enter", keyCode: 229 },
    ]) {
      const event = new dom.window.KeyboardEvent("keydown", { ...init, bubbles: true, cancelable: true });
      await act(async () => composer.dispatchEvent(event));
      expect(event.defaultPrevented).toBe(false);
      expect(sent).toEqual([]);
      expect(composer.value).toBe("/reset");
    }
    await act(async () => composer.dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true })));
    expect(sent).toEqual([{ op: "reset", thread: "thread-a" }]);
    expect(composer.value).toBe("");
    sent.length = 0;
    // Exercise the real slash menu and handlers, not a mock command table.
    for (const name of ["bg", "subagent"]) {
      await act(async () => insertDraft(`/${name}`));
      await act(async () => composer.dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true })));
      expect(composer.value).toBe(`/${name} `);
      await act(async () => sendButton().click());
      expect(sent).toEqual([]);
      const argument = name === "bg" ? "printf 'hello'\nprintf 'world'" : "检查日志\n保留错误详情";
      await act(async () => insertDraft(argument));
      const draft = composer.value;
      const successful = props.sendRaw;
      props.sendRaw = vi.fn(() => false);
      props.model = `model-${name}`;
      await render();
      await act(async () => sendButton().click());
      expect(composer.value).toBe(draft);
      expect(props.sendRaw).toHaveBeenCalledOnce();
      props.sendRaw = successful;
      await render();
      await act(async () => sendButton().click());
      expect(sent).toEqual([{
        op: "chat", thread: "thread-a", model: `model-${name}`,
        text: name === "bg" ? `Use the bg tool to run this command in the background: ${argument}` : `Use the subagent tool to complete this delegated task: ${argument}`,
      }]);
      expect(composer.value).toBe("");
      sent.length = 0;
    }
    props.model = "model";
    await render();
    await attach("first.png");
    expect(sendButton().disabled).toBe(true);
    expect(document.querySelector('[role="status"]').textContent).toBe("agent.readingImages");
    const first = readers[0];
    props.threadId = "thread-b";
    await render();
    expect(first.abort).toHaveBeenCalledOnce();
    await act(async () => first.finish("data:image/png;base64,b2xk"));
    expect(document.querySelectorAll("img")).toHaveLength(0);
    expect(document.querySelector('[role="status"]')).toBeNull();
    await attach("second.png");
    await act(async () => readers[1].finish("data:image/png;base64,bmV3"));
    expect(sendButton().disabled).toBe(false);
    props.threadId = "thread-a";
    await render();
    expect(document.querySelectorAll("img")).toHaveLength(0);
    expect(sendButton().disabled).toBe(true);
    props.threadId = "thread-b";
    await render();
    expect(document.querySelectorAll("img")).toHaveLength(1);
    const successfulSend = props.sendRaw;
    props.sendRaw = vi.fn(() => false);
    await render();
    await act(async () => sendButton().click());
    expect(document.querySelectorAll("img")).toHaveLength(1);
    expect(props.sendRaw).toHaveBeenCalledOnce();
    props.sendRaw = successfulSend;
    await render();
    await act(async () => sendButton().click());
    expect(sent).toEqual([{ op: "chat", text: "", thread: "thread-b", model: "model", images: [{ name: "second.png", mime: "image/png", data: "bmV3" }] }]);
    expect(document.querySelectorAll("img")).toHaveLength(0);
    await attach("unreadable.png");
    await act(async () => readers[2].onerror());
    expect(props.sink.ops.pushBubble).toHaveBeenCalledWith("error", "agent.imageReadFailed");
    expect(document.querySelector('[role="status"]')).toBeNull();
    expect(sendButton().disabled).toBe(true);
    await attach("pending-at-unmount.png");
  } finally {
    await act(async () => root.unmount());
    dom.window.close();
    for (const [key, descriptor] of previous) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else delete globalThis[key];
    }
  }
  expect(readers.at(-1).abort).toHaveBeenCalledOnce();
});
