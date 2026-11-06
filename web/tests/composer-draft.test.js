import { JSDOM } from "jsdom";
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it } from "vitest";
import { useComposerDraft } from "../ui/components/agent/use-composer-draft";

it("restores each conversation draft and keeps delayed updates bound to their original device and thread", async () => {
  const dom = new JSDOM('<div id="root"></div>');
  const previous = ["window", "document", "IS_REACT_ACT_ENVIRONMENT"].map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]);
  Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });
  const empty = { text: "", images: [] };
  let draft, update;
  function Harness({ device, thread }) {
    [draft, update] = useComposerDraft(JSON.stringify([device, thread]), empty);
    return null;
  }
  const root = createRoot(document.getElementById("root"));
  const show = (device, thread) => act(async () => root.render(createElement(Harness, { device, thread })));
  try {
    await show("a", "first");
    await act(async () => update({ text: "first draft", images: ["first.png"] }));
    const updateFirst = update;
    await show("a", "second");
    expect(draft).toEqual(empty);
    await act(async () => update({ text: "second draft", images: [] }));
    await act(async () => updateFirst(value => ({ ...value, text: value.text + " updated" })));
    expect(draft.text).toBe("second draft");
    await show("b", "first");
    expect(draft).toEqual(empty);
    await show("a", "first");
    expect(draft).toEqual({ text: "first draft updated", images: ["first.png"] });
    await act(async () => update(empty));
    await show("a", "second");
    expect(draft.text).toBe("second draft");
    await show("a", "first");
    expect(draft).toEqual(empty);
  } finally {
    await act(async () => root.unmount());
    dom.window.close();
    for (const [key, descriptor] of previous) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else delete globalThis[key];
    }
  }
});
