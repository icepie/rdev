// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { readPanelSelection } from "../ui/lib/panel-selection";
import { FileSelectionMenu } from "../ui/components/agent/file-selection-menu";

const bounds = () => new DOMRect(20, 20, 360, 300);
const selectedRect = () => new DOMRect(40, 70, 120, 20);
afterEach(() => {vi.restoreAllMocks(); document.body.replaceChildren(); delete globalThis.IS_REACT_ACT_ENVIRONMENT;});

function fixture() {
 const a=document.createElement("div"), b=document.createElement("div");
 document.body.append(a,b);
 for(const panel of [a,b]) {
  panel.getBoundingClientRect=bounds;
  const host=document.createElement("diffs-container");panel.append(host);
  host.attachShadow({mode:"open"}).innerHTML='<pre><span>  const answer = 42;\n</span><span>next line</span></pre>';
 }
 const text=a.firstChild.shadowRoot.querySelector("span").firstChild;
 const createRange=document.createRange.bind(document);
 vi.spyOn(document,"createRange").mockImplementation(()=>Object.assign(createRange(), {getClientRects:()=>[selectedRect()], getBoundingClientRect:selectedRect}));
 const range=document.createRange();range.selectNodeContents(text);
 const selection={isCollapsed:false,rangeCount:1,getRangeAt:()=>range,toString:()=>range.toString(),getComposedRanges:vi.fn(()=>[range]),removeAllRanges:vi.fn()};
 vi.spyOn(document,"getSelection").mockReturnValue(selection);
 return {a,b,range,selection};
}

it("reads Pierre shadow selections, preserving indentation and excluding other panes", () => {
 const {a,b,range,selection}=fixture();
 expect(readPanelSelection(a).text).toBe("  const answer = 42;");
 expect(selection.getComposedRanges).toHaveBeenCalledWith({shadowRoots:[a.firstChild.shadowRoot]});
 expect(readPanelSelection(b)).toBeNull();
 // Browsers without composed ranges still expose a usable in-shadow Range.
 delete selection.getComposedRanges;
 expect(readPanelSelection(a).text).toBe("  const answer = 42;");
 a.getBoundingClientRect=()=>new DOMRect();
 expect(readPanelSelection(a)).toBeNull();
 expect(range.collapsed).toBe(false);
});

it("rejects a range spanning neighboring panes", () => {
 const {a,b,selection}=fixture();
 const left=document.createTextNode("left"),right=document.createTextNode("right");
 a.append(left);b.append(right);
 const cross=document.createRange();cross.setStart(left,0);cross.setEnd(right,5);
 selection.getComposedRanges.mockReturnValue([cross]);
 selection.getRangeAt=()=>cross;
 expect(readPanelSelection(a)).toBeNull();expect(readPanelSelection(b)).toBeNull();
 delete selection.getComposedRanges;
 expect(readPanelSelection(a)).toBeNull();expect(readPanelSelection(b)).toBeNull();
});

async function menuFixture() {
 globalThis.IS_REACT_ACT_ENVIRONMENT=true;
 const f=fixture();
 const host=document.createElement("div");document.body.append(host);
 const root=createRoot(host);const added=vi.fn();
 const props={panelRef:{current:f.a},content:"version1",mode:"source",onAddToChat:added,t:key=>key};
 const render=()=>act(async()=>root.render(createElement(FileSelectionMenu,props)));
 await render();
 const select=()=>act(async()=>{document.dispatchEvent(new Event("selectionchange"));await new Promise(requestAnimationFrame);});
 return {...f,added,props,render,select,dispose:()=>act(async()=>root.unmount())};
}

it("updates keyboard selections before attaching text, and dismisses on scroll or refresh",async()=>{
 const f=await menuFixture();
 try {
  await f.select();expect(document.querySelector('[data-add-to-chat]')).not.toBeNull();
  f.range.setStart(f.range.startContainer,2);
  await act(async()=>{document.dispatchEvent(new KeyboardEvent("keyup",{key:"ArrowRight"}));await new Promise(requestAnimationFrame);});
  await act(async()=>document.querySelector('[data-add-to-chat]').click());
  expect(f.added).toHaveBeenCalledExactlyOnceWith("const answer = 42;");
  expect(f.selection.removeAllRanges).toHaveBeenCalledOnce();
  await f.select();
  await act(async()=>f.a.firstChild.dispatchEvent(new Event("scroll")));
  expect(document.querySelector('[data-add-to-chat]')).toBeNull();
  await f.select();f.props.content="version2";await f.render();
  expect(document.querySelector('[data-add-to-chat]')).toBeNull();
  await f.select();
  await act(async()=>document.dispatchEvent(new KeyboardEvent("keyup",{key:"Escape"})));
  expect(document.querySelector('[data-add-to-chat]')).toBeNull();
 } finally {await f.dispose();}
});

it("never submits stale text when selection changes in another pane before the next frame",async()=>{
 const f=await menuFixture();
 try {
  await f.select();
  const other=document.createRange();other.selectNodeContents(f.b.firstChild.shadowRoot.querySelector("span"));
  f.selection.getComposedRanges.mockReturnValue([other]);
  await act(async()=>document.querySelector('[data-add-to-chat]').click());
  expect(f.added).not.toHaveBeenCalled();
  expect(f.selection.removeAllRanges).not.toHaveBeenCalled();
  await act(async()=>document.dispatchEvent(new Event("selectionchange")));
 } finally {await f.dispose();}
 await act(async()=>new Promise(requestAnimationFrame));
 expect(document.querySelector('[data-add-to-chat]')).toBeNull();
});
