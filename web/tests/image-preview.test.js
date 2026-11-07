// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it } from "vitest";
import { ImagePreview } from "../ui/components/agent/image-preview";
import { replayHistory } from "../ui/components/agent/protocol";

it("opens an attachment preview and preserves image metadata on history replay", async () => {
 globalThis.IS_REACT_ACT_ENVIRONMENT = true;
 const host = document.createElement("div"); document.body.append(host);
 const root = createRoot(host);
 const src = "data:image/png;base64,aGVsbG8=";
 try {
  await act(async () => root.render(createElement(ImagePreview, {src, name: "photo.png"})));
  await act(async () => host.querySelector("button").click());
  expect(document.querySelector('[role="dialog"] img').getAttribute("src")).toBe(src);
  expect(document.querySelector('[role="dialog"]').textContent).toContain("photo.png");
  const items = replayHistory([{role: "user", text: "look", images: ["photo.png"], imagePreviews: [src]}]);
  expect(items[0].imagePreviews).toEqual([src]);
 } finally { await act(async () => root.unmount()); host.remove(); delete globalThis.IS_REACT_ACT_ENVIRONMENT; }
});
