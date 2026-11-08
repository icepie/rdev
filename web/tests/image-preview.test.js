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


it("keeps gallery selection stable when images change and closes on trigger replacement", async () => {
 globalThis.IS_REACT_ACT_ENVIRONMENT = true;
 const host=document.createElement('div');document.body.append(host);
 const root=createRoot(host);
 const a={src:'data:image/png;base64,YQ==',name:'a.png'};
 const b={src:'data:image/png;base64,Yg==',name:'b.png'};
 const c={src:'data:image/png;base64,Yw==',name:'c.png'};
 const render=(props)=>act(async()=>root.render(createElement(ImagePreview,props)));
 const next=()=>act(async()=>window.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true})));
 const current=()=>document.querySelector('[role="dialog"][data-state="open"] img')?.getAttribute('alt');
 try {
  await render({...a,gallery:[a,b,c]});
  await act(async()=>host.querySelector('button').click());
  await next();expect(current()).toBe('b.png');
  await render({...a,gallery:[c,a,b]});
  expect(current()).toBe('b.png');
  expect(document.querySelector('[role="dialog"]').textContent).toContain('3 / 3');
  await render({...a,gallery:[c,a]});
  expect(current()).toBe('a.png');
  expect(document.querySelector('[role="dialog"]').textContent).toContain('2 / 2');
  await next();expect(current()).toBe('c.png');
  await render({...b,gallery:[b,c]});
  expect(current()).toBeUndefined();
  await act(async()=>host.querySelector('button').click());
  expect(current()).toBe('b.png');
  await render({...b,gallery:[]});
  expect(current()).toBe('b.png');
  expect(document.querySelector('[role="dialog"]').textContent).not.toMatch(/\d+ \/ 0/);
 } finally {await act(async()=>root.unmount());host.remove();delete globalThis.IS_REACT_ACT_ENVIRONMENT;}
});
