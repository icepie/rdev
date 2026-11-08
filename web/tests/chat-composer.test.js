import { parseModelCatalog } from "../ui/components/agent/protocol";
import { acceptQueueRemoval } from "../ui/components/agent/queue-actions";
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
  dom.window.HTMLElement.prototype.scrollIntoView = vi.fn();
  const readers = [];
  class Reader {
    abort = vi.fn();
    constructor() { readers.push(this); }
    readAsDataURL(file) { if (file.name === "sync-failure.png") throw new Error("read unavailable"); }
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
    sink: { state: { items: [], todos: [], queue: [], queueIds: [], running: false, stopping: false, turnUsage: null, currentTool: "", workingSince: null },
      ops: { turnOpen: { current: true }, queueReturns: { current: new Map() }, registerScrollEl() {}, pushBubble: vi.fn(), setRunning() {}, setWorkingSince() {} } },
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
    Object.defineProperty(picker, "files", { configurable: true, value: (Array.isArray(name) ? name : [name]).map(value => new dom.window.File(["image"], value, { type: "image/png" })) });
    await act(async () => picker.dispatchEvent(new dom.window.Event("change", { bubbles: true })));
  };
  const sendButton = () => document.querySelector('button[aria-label="agent.send"]');
  try {
    await render();
    props.models = parseModelCatalog([null, { name: "invalid" }, { id: "model", reasoningLevels: ["low", "medium", "high", "invalid", null] }]);
    expect(props.models).toHaveLength(1);
    props.reasoning = "off"; // legacy preference under an adaptive-only model
    await render();
    const reasoningButton = document.querySelector('button[aria-label="agent.reasoning"]');
    expect(reasoningButton.textContent).toBe("agent.reasoningLow");
    await act(async () => reasoningButton.dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true })));
    expect([...document.querySelectorAll('[role="menuitem"]')].map(item => item.textContent)).toEqual(["agent.reasoningLow", "agent.reasoningMedium", "agent.reasoningHigh"]);
    await act(async () => document.querySelector('[role="menu"]').dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    props.models = parseModelCatalog([{ id: "model" }]); // old catalogs retain all four choices
    await render();
    expect(document.querySelector('button[aria-label="agent.reasoning"]').textContent).toBe("agent.reasoningOff");
    props.models = [{id:"model"},{id:"provider/model-b"}];
    props.onModelChange = vi.fn(id => { props.model = id; });
    props.threadId = "model-invalid";
    await render();
    await act(async () => insertDraft("/model unavailable"));
    await act(async () => sendButton().click());
    expect(document.querySelector("textarea").value).toBe("/model unavailable");
    expect(document.querySelector('[role="alert"]').textContent).toBe("agent.cmd.unknownModel");
    expect(props.onModelChange).not.toHaveBeenCalled();
    expect(sent).toEqual([]);
    props.threadId = "model-valid";
    await render();
    await act(async () => insertDraft("/models provider/model-b"));
    await act(async () => sendButton().click());
    expect(props.onModelChange).toHaveBeenCalledWith("provider/model-b");
    expect(document.querySelector("textarea").value).toBe("");
    expect(sent).toEqual([]);
    await render();
    await act(async () => insertDraft("use selected model"));
    await act(async () => sendButton().click());
    expect(sent.at(-1)).toMatchObject({op:"chat",model:"provider/model-b",text:"use selected model"});
    sent.length=0;
    props.threadId = "model-picker";
    await render();
    await act(async () => insertDraft("/models"));
    await act(async () => sendButton().click());
    expect(document.querySelector("textarea").value).toBe("");
    const modelPicker = document.querySelector('[role="listbox"][aria-label="agent.cmd.model"]');
    expect(modelPicker).not.toBeNull();
    expect(modelPicker.textContent).toContain("provider/model-b");
    props.threadId = "model-unavailable";
    props.models = [];
    await render();
    expect(document.querySelector('[role="listbox"][aria-label="agent.cmd.model"]')).toBeNull();
    await act(async () => insertDraft("/model"));
    await act(async () => sendButton().click());
    expect(document.querySelector("textarea").value).toBe("/model");
    expect(document.querySelector('[role="alert"]').textContent).toBe("agent.cmd.noModels");
    expect(sent).toEqual([]);
    props.threadId = "thread-a";
    props.model = "model";
    props.reasoning = "medium";
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
      expect(document.querySelector('[role="alert"]').textContent).toBe("agent.commandRequiresArgument");
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
        text: `/${name} ${argument}`,
      }]);
      expect(composer.value).toBe("");
      sent.length = 0;
    }
    props.model = "model";
    for (const [command, running, error] of [
      ["/jobs kill", true, "/jobs [full|kill <id>|kill all]"],
      ["/compact preserve errors", true, "agent.compactWhileRunning"],
      ["/steer ", true, "agent.commandRequiresArgument"],
      ["/steer check logs", false, "agent.commandRequiresRunning"],
      ["/stop", false, "agent.commandRequiresRunning"],
    ]) {
      props.threadId = `invalid-command-${command}`;
      props.sink.state.running = running;
      await render();
      await act(async () => insertDraft(command));
      const count = sent.length;
      await act(async () => composer.dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })));
      expect(sent).toHaveLength(count);
      expect(composer.value).toBe(command);
      expect(document.querySelector('[role="alert"]').textContent).toBe(error);
      expect(props.sink.ops.pushBubble).not.toHaveBeenCalled();
      expect(props.sink.ops.turnOpen.current).toBe(true);
    }
    props.sink.state.running = false;
    for (const command of ["/COMPACT", "/bg\necho hi", "/subagent inspect", "/jobs", "/jobs kill all", "/reset", "/new", "/stop", "/model", "/models provider/model-b"]) {
      props.threadId = `image-command-${command}`;
      await render();
      await act(async () => insertDraft(command));
      await attach("command.png");
      await act(async () => readers.at(-1).finish("data:image/png;base64,bmV3"));
      await act(async () => sendButton().click());
      expect(sent).toEqual([]);
      expect(composer.value).toBe(command);
      expect(document.querySelectorAll("img")).toHaveLength(1);
      expect(document.querySelector('[role="alert"]').textContent).toBe("agent.commandImagesUnsupported");
    }
    // Unknown slash text remains ordinary multimodal chat, not a rejected
    // built-in command; recognition must come from the actual command catalog.
    props.threadId = "image-custom-command";
    await render();
    await act(async () => insertDraft("/custom inspect"));
    await attach("custom.png");
    await act(async () => readers.at(-1).finish("data:image/png;base64,YQ=="));
    await act(async () => sendButton().click());
    expect(sent).toHaveLength(1);
    expect(sent[0]).toMatchObject({op:"chat",text:"/custom inspect",images:[{name:"custom.png",data:"YQ=="}]});
    expect(composer.value).toBe("");
    sent.length = 0;
    for (const [draft, expectedText, slash] of [["", "", false], ["compare this image", "compare this image", false], ["/steer inspect", "inspect", true], ["/steer", "", true]]) {
      props.threadId = `image-steering-${draft}`;
      props.sink.state.running = true;
      await render();
      if (draft) await act(async () => insertDraft(draft));
      await attach("guidance.png");
      const steer = () => document.querySelector('button[aria-label="agent.steer"]');
      expect(steer().disabled).toBe(true);
      await act(async () => readers.at(-1).finish("data:image/png;base64,Z3VpZGU="));
      expect(steer().disabled).toBe(false);
      const successful = props.sendRaw;
      props.sendRaw = vi.fn(() => false);
      await render();
      const submit = () => act(async () => slash
        ? composer.dispatchEvent(new dom.window.KeyboardEvent("keydown", {key:"Enter",bubbles:true,cancelable:true}))
        : steer().click());
      await submit();
      expect(props.sendRaw).toHaveBeenCalledOnce();
      expect(composer.value).toBe(draft);
      expect(document.querySelectorAll("img")).toHaveLength(1);
      props.sendRaw = successful;
      await render();
      await submit();
      expect(sent.at(-1)).toEqual({op:"steer",text:expectedText,thread:props.threadId,images:[{name:"guidance.png",mime:"image/png",data:"Z3VpZGU="}]});
      expect(composer.value).toBe("");
      expect(document.querySelectorAll("img")).toHaveLength(0);
      sent.length = 0;
    }
    props.sink.state.running = false;
    props.threadId = "thread-a";
    await render();
    expect(document.querySelector('[role="alert"]')).toBeNull();
    readers.length = 0;
    await attach("first.png");
    expect(sendButton().disabled).toBe(true);
    expect(document.querySelector('[role="status"]').textContent).toBe("agent.readingImages");
    const first = readers[0];
    props.sink.ops.queueReturns.current.set("pending-old-thread", () => {});
    props.threadId = "thread-b";
    await render();
    expect(first.abort).toHaveBeenCalledOnce();
    expect(props.sink.ops.queueReturns.current.size).toBe(0);
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
    props.sink.state.running = true;
    props.sink.state.queue = ["same", "same"];
    props.sink.state.queueIds = ["guidance", "followup"];
    props.sink.state.queueModes = ["steer", "follow_up"];
    await render();
    const promote=document.querySelector('button[aria-label="agent.queuePromote"]');
    expect(document.querySelectorAll('button[aria-label="agent.queuePromote"]')).toHaveLength(1);
    await act(async()=>promote.click());
    expect(sent.at(-1)).toEqual({op:"promote_queue",thread:"thread-b",queueId:"followup"});
    expect(document.querySelector('textarea').value).toBe("");
    props.sink.state.queueModes = ["steer", "command"];
    await render();
    expect(document.querySelector('button[aria-label="agent.queuePromote"]')).toBeNull();
    props.sink.state.running = false;
    props.sink.state.queueModes = [];
    props.sink.state.queue = [""];
    props.sink.state.queueIds = ["image-row"];
    await render();
    expect(document.querySelector('button[title="agent.queueEditHint"]').textContent).toBe("agent.queuedImages");
    await act(async () => document.querySelector('button[title="agent.queueEditHint"]').click());
    const removal = sent.at(-1);
    expect(removal).toMatchObject({ op: "dequeue", queueId: "image-row", restore: true });
    expect(sendButton().disabled).toBe(true);
    await attach("blocked-during-return.png");
    expect(readers).toHaveLength(2);
    await act(async () => acceptQueueRemoval(props.sink.ops.queueReturns.current, {
      id: removal.id, removed: true, text: "queued image",
      images: [{ name: "original.png", mime: "image/png", data: "b3JpZ2luYWw=" }],
    }));
    expect(document.querySelector('img').getAttribute('src')).toBe("data:image/png;base64,b3JpZ2luYWw=");
    expect(document.querySelector('button[title="agent.queueEditImagesHint"]').disabled).toBe(true);
    await act(async () => sendButton().click());
    expect(sent.at(-1)).toMatchObject({ op: "chat", text: "queued image", images: [{ name: "original.png", mime: "image/png", data: "b3JpZ2luYWw=" }] });
    props.sink.state.running = true;
    await render();
    await attach("unreadable.png");
    await act(async () => readers[2].onerror());
    expect(document.querySelector('[role="alert"]').textContent).toBe("agent.imageReadFailed");
    expect(props.sink.ops.pushBubble).not.toHaveBeenCalled();
    props.sink.state.running = false;
    await render();
    expect(document.querySelector('[role="status"]')).toBeNull();
    expect(sendButton().disabled).toBe(true);
    // Parallel reads may finish in reverse order, including separate picks.
    await attach(["first-in-order.png", "second-in-order.png"]);
    const [earlier, later] = readers.slice(-2);
    await act(async () => later.finish("data:image/png;base64,c2Vjb25k"));
    expect(sendButton().disabled).toBe(true);
    expect(document.querySelectorAll("img")).toHaveLength(0);
    await act(async () => earlier.finish("data:image/png;base64,Zmlyc3Q="));
    expect([...document.querySelectorAll("img")].map(image => image.alt)).toEqual(["first-in-order.png", "second-in-order.png"]);
    await act(async () => sendButton().click());
    expect(sent.at(-1).images.map(image => image.name)).toEqual(["first-in-order.png", "second-in-order.png"]);
    // A failed earlier read unblocks later successful reads without reordering.
    await attach("failed-earlier.png");
    await attach("surviving-later.png");
    const [failed, surviving] = readers.slice(-2);
    await act(async () => surviving.finish("data:image/png;base64,b2s="));
    await act(async () => failed.onerror());
    expect([...document.querySelectorAll("img")].map(image => image.alt)).toEqual(["surviving-later.png"]);
    await act(async () => sendButton().click());
    expect(sent.at(-1).images.map(image => image.name)).toEqual(["surviving-later.png"]);
    await attach("sync-failure.png");
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
