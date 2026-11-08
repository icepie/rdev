// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { dictionaries } from "../ui/lib/dictionaries";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { FilePreview } from "../ui/components/agent/file-preview";

vi.mock("@pierre/diffs/react", () => ({File: ({file, options, selectedLines}) => createElement("pre", {"data-source": true, "data-wrap":options.overflow, "data-line":selectedLines?.start}, file.contents)}));
vi.mock("../ui/lib/clipboard", () => ({copyText: vi.fn().mockResolvedValue(true)}));
import { copyText } from "../ui/lib/clipboard";

const t = (key, values = {}) => Object.entries(values).reduce((text, [name, value]) => text.replaceAll(`{${name}}`, String(value)), dictionaries.en[key] || key);
const ready = content => ({status: "ready", content});
afterEach(() => vi.restoreAllMocks());

async function mount(props, wrap = node => node) {
 globalThis.IS_REACT_ACT_ENVIRONMENT = true;
 if (!vi.isMockFunction(globalThis.fetch)) vi.spyOn(globalThis, "fetch").mockResolvedValue({ok:true});
 vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function () { return this.tagName === "TR" ? 29 : 400; });
 const host = document.createElement("div"); document.body.append(host);
 const root = createRoot(host);
 const defaults = {connected: true, onRefresh: vi.fn(), t};
 const render = next => act(async () => root.render(wrap(createElement(FilePreview, {...defaults, ...next}))));
 await render(props);
 return {host, render, dispose: async () => {await act(async () => root.unmount()); host.remove(); delete globalThis.IS_REACT_ACT_ENVIRONMENT;}};
}
const button = (host, label) => [...host.querySelectorAll("button")].find(element => element.getAttribute("aria-label") === label);

it("defaults CSV to a table, copies original text, switches source, and isolates file state", async () => {
 const content = 'name,notes\r\nAda,"one,two"\r\n';
 const {host, render, dispose} = await mount({path: "/a.csv", state: ready(content)});
 try {
  expect(host.querySelector('table').getAttribute("aria-label")).toBe("a.csv CSV preview");
  expect(host.querySelector('table').textContent).toContain("one,two");
  await act(async () => button(host, "Copy file contents").click());
  expect(copyText).toHaveBeenLastCalledWith(content);
  await act(async () => button(host, "Show source").click());
  expect(host.querySelector('table').closest('[data-file-view]').getAttribute('aria-hidden')).toBe('true');
  expect(host.querySelector('[data-source]').textContent).toBe(content);
  await act(async () => button(host, "Toggle line wrap").click());
  expect(host.querySelector('[data-source]').getAttribute('data-wrap')).toBe("wrap");
  await render({path: "/b.md", state: ready("# New file")});
  expect(host.querySelector('h1').textContent).toBe("New file");
  expect(host.querySelector('[data-source]')).toBeNull();
  await render({path: "/b.md", state: ready("# New file"), lineRange: {start: 1, end: 1}});
  expect(host.querySelector('[data-source]').getAttribute('data-line')).toBe("1");
  expect(host.querySelector('[data-source]').getAttribute('data-wrap')).toBe("scroll");
  await render({path: "/b.md", state: ready("# Refreshed")});
  expect(host.querySelector('[data-source]').textContent).toBe("# Refreshed");
 } finally {await dispose();}
});

it("virtualizes a large CSV and reaches its final preview row without mounting the whole grid", async () => {
 const columns = Array.from({length: 120}, (_, i) => `c${i}`);
 const content = [columns.join(","), ...Array.from({length: 600}, (_, r) => columns.map((_, c) => `r${r}c${c}`).join(","))].join("\n");
 const {host, dispose} = await mount({path: "/large.csv", state: ready(content)});
 try {
  const table = host.querySelector('table');
  expect(table.textContent).toContain("r0c0");
  expect(table.textContent).not.toContain("r499c0");
  expect(table.querySelectorAll('tbody tr[data-index]').length).toBeLessThan(60);
  expect(table.querySelectorAll('td').length).toBeLessThan(6000);
  expect(host.textContent).toContain("Showing the first 500 rows and 100 columns.");
  await act(async () => {table.parentElement.scrollTop = 499 * 29; table.parentElement.dispatchEvent(new Event("scroll"));});
  expect(table.textContent).toContain("r499c0");
  expect(table.textContent).not.toContain("r0c0");
  expect(table.querySelectorAll('tbody tr[data-index]').length).toBeLessThan(60);
 } finally {await dispose();}
});

it("opens the shared image lightbox and clears it when changing files", async () => {
 const {host, render, dispose} = await mount({path: "/image.png", state: {...ready("aGVsbG8="), encoding:"base64", mime:"image/png"}});
 try {
  await act(async () => button(host, "Open image.png in full screen preview").click());
  expect(document.querySelector('[role="dialog"] img').getAttribute("src")).toBe("data:image/png;base64,aGVsbG8=");
  await render({path: "/other.md", state: ready("# Other file")});
  expect(document.querySelector('[role="dialog"]')).toBeNull();
  expect(host.querySelector("h1").textContent).toBe("Other file");
 } finally {await dispose();}
});

it("keeps nested desktop image previews interactive and closes only the top layer", async () => {
 const outerChange = vi.fn();
 const wrap = child => createElement(DialogPrimitive.Root, {open: true, onOpenChange: outerChange},
  createElement(DialogPrimitive.Portal, {container: document.body},
   createElement(DialogPrimitive.Content, {"aria-describedby": undefined},
    createElement(DialogPrimitive.Title, null, "File dialog"), child)));
 const {dispose} = await mount({path: "/nested.png", state: {...ready("aGVsbG8="), encoding: "base64", mime: "image/png"}}, wrap);
 try {
  const opener = button(document.body, "Open nested.png in full screen preview");
  await act(async () => {opener.focus(); opener.click();});
  const dialogs = [...document.querySelectorAll('[role="dialog"]')];
  expect(dialogs).toHaveLength(2);
  const lightbox = dialogs[1];
  expect(getComputedStyle(lightbox).pointerEvents).toBe("auto");
  expect(lightbox.contains(document.activeElement)).toBe(true);
  await act(async () => document.activeElement.dispatchEvent(new KeyboardEvent("keydown", {key: "Escape", bubbles: true})));
  expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(1);
  expect(outerChange).not.toHaveBeenCalled();
  await act(async () => new Promise(resolve => setTimeout(resolve, 0))); // Radix restores focus after unmount.
  expect(document.activeElement).toBe(opener);
 } finally {await dispose();}
});

it("previews HTML in an isolated frame, preserves it across source toggles and replaces refreshed content", async () => {
 const content = '<!doctype html><html lang="en"><head><title>Counter</title><style>body{margin:0}</style></head><body><input value="draft"><button onclick="this.textContent=2">1</button></body></html>';
 const {host, render, dispose} = await mount({path:"/counter.html",state:ready(content)});
 try {
  const frame = host.querySelector('iframe');
  expect(frame.title).toBe('counter.html');
  expect(frame.getAttribute('sandbox')).toBe('allow-scripts');
  expect(frame.getAttribute('referrerpolicy')).toBe('no-referrer');
  const doc = new DOMParser().parseFromString(frame.srcdoc, 'text/html');
  expect(doc.head.firstElementChild.httpEquiv).toBe('Content-Security-Policy');
  expect(doc.head.firstElementChild.content).toContain("connect-src 'none'");
  expect(doc.documentElement.lang).toBe('en');
  expect(doc.querySelector('button').getAttribute('onclick')).toBe('this.textContent=2');
  expect(doc.querySelector('style').textContent).toBe('body{margin:0}');
  await act(async () => button(host, 'Show source').click());
  expect(host.querySelector('iframe')).toBe(frame);
  expect(frame.hidden).toBe(true);
  expect(frame.hasAttribute('inert')).toBe(true);
  expect(host.querySelector('[data-source]').textContent).toBe(content);
  await act(async () => button(host, 'Copy file contents').click());
  expect(copyText).toHaveBeenLastCalledWith(content);
  await act(async () => button(host, 'Show preview').click());
  expect(host.querySelector('iframe')).toBe(frame);
  expect(frame.hidden).toBe(false);
  await render({path:'/counter.html',state:ready('<h1>Updated</h1>')});
  expect(host.querySelector('iframe').srcdoc).toContain('<h1>Updated</h1>');
  await render({path:'/other.htm',state:ready('<h1>Other</h1>'),lineRange:{start:1,end:1}});
  expect(host.querySelector('iframe')).not.toBe(frame);
  expect(host.querySelector('iframe').hidden).toBe(true);
  expect(host.querySelector('[data-source]').textContent).toBe('<h1>Other</h1>');
 } finally {await dispose();}
});

it("renders native video controls without treating the media URL as file text", async () => {
 vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
 vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
 const state = { ...ready('/api/media/fixture'), encoding:'url', mime:'video/mp4' };
 const {host, render, dispose} = await mount({path:'/clip.mp4', state});
 try {
  const video = host.querySelector('video');
  expect(video.getAttribute('src')).toBe('/api/media/fixture');
  expect(video.controls).toBe(true);
  expect(video.preload).toBe('metadata');
  expect(host.querySelector('[data-source]')).toBeNull();
  expect(button(host,'Copy file contents')).toBeUndefined();
  expect(button(host,'Toggle line wrap')).toBeUndefined();
  await act(async () => video.dispatchEvent(new Event('error')));
  expect(video.parentElement.querySelector('[role="status"]').textContent).toContain('Unable to play video');
  await render({path:'/clip.mp4',state:{...state,content:'/api/media/refreshed'}});
  expect(host.querySelector('video')).not.toBe(video);
  expect(host.querySelector('video').parentElement.querySelector('[role="status"]')).toBeNull();
 } finally {await dispose();}
});


it("releases hidden media and restores its position without autoplay", async () => {
 const load = vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(() => {});
 const pause = vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
 const play = vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue();
 const props = {path:'/clip.mp4',state:{...ready('/api/media/fixture'),encoding:'url',mime:'video/mp4'}};
 const {host,render,dispose} = await mount({...props,visible:false});
 const video = host.querySelector('video');
 try {
  expect(video.hasAttribute('src')).toBe(false);
  expect(load).not.toHaveBeenCalled();
  await render({...props,visible:true});
  expect(video.getAttribute('src')).toBe('/api/media/fixture');
  video.currentTime=6;
  await render({...props,visible:false});
  expect(pause).toHaveBeenCalledOnce();
  expect(video.hasAttribute('src')).toBe(false);
  expect(load).toHaveBeenCalledTimes(2);
  video.currentTime=0;
  Object.defineProperty(video,'duration',{configurable:true,value:8});
  await render({...props,visible:true});
  await act(async()=>video.dispatchEvent(new Event('loadedmetadata')));
  expect(video.currentTime).toBe(6);
  expect(play).not.toHaveBeenCalled();
 } finally {await dispose();}
 expect(video.hasAttribute('src')).toBe(false);
 expect(pause).toHaveBeenCalledTimes(2);
 expect(load).toHaveBeenCalledTimes(4);
});


it("retains independent preview/source reading positions, lazily mounts source and resets on file change", async () => {
 const props = {path:'/long.md',state:ready('# Heading\n\n' + 'Long content\n\n'.repeat(200))};
 const {host,render,dispose} = await mount(props);
 try {
  const preview = host.querySelector('[data-file-view="preview"]');
  const source = host.querySelector('[data-file-view="source"]');
  const heading = host.querySelector('h1');
  expect(host.querySelector('[data-source]')).toBeNull();
  preview.scrollTop = 1200;
  await act(async () => button(host, 'Show source').click());
  const code = host.querySelector('[data-source]');
  expect(preview.getAttribute('aria-hidden')).toBe('true');
  expect(preview.inert || preview.hasAttribute('inert')).toBe(true);
  expect(source.getAttribute('aria-hidden')).toBe('false');
  expect(source.scrollTop).toBe(0);
  source.scrollTop = 800;
  source.scrollLeft = 90;
  await act(async () => button(host, 'Show preview').click());
  expect(host.querySelector('h1')).toBe(heading);
  expect(preview.scrollTop).toBe(1200);
  expect(source.getAttribute('aria-hidden')).toBe('true');
  await act(async () => button(host, 'Show source').click());
  expect(host.querySelector('[data-source]')).toBe(code);
  expect(source.scrollTop).toBe(800);
  expect(source.scrollLeft).toBe(90);
  await render({...props, visible:false});
  await render({...props, visible:true});
  expect(source.scrollTop).toBe(800);
  await render({...props,path:'/other.md'});
  expect(host.querySelector('[data-file-view="preview"]').scrollTop).toBe(0);
  expect(host.querySelector('[data-source]')).toBeNull();
 } finally {await dispose();}
});

it("uses the authorized HTML resource URL, retains its frame across modes, and reloads changed content", async () => {
 const previewUrl='/api/media/'+'b'.repeat(64)+'/index.html';
 const props={path:'/site/index.html',state:{...ready('<link rel="stylesheet" href="style.css">'),previewUrl}};
 const {host,render,dispose}=await mount(props);
 try {
  const frame=host.querySelector('iframe');
  expect(frame.getAttribute('src')).toBe(previewUrl);
  expect(frame.hasAttribute('srcdoc')).toBe(false);
  expect(frame.getAttribute('sandbox')).toBe('allow-scripts');
  await act(async()=>button(host,'Show source').click());
  await act(async()=>button(host,'Show preview').click());
  expect(host.querySelector('iframe')).toBe(frame);
  await render({...props,state:{...props.state,content:'<h1>Refreshed</h1>'}});
  const updated=host.querySelector('iframe');
  expect(updated).not.toBe(frame);
  expect(updated.getAttribute('src')).toBe(previewUrl);
  await render({...props,state:{...props.state,content:'<h1>Refreshed</h1>'},visible:false});
  expect(host.querySelector('iframe')).toBe(updated);
 } finally {await dispose();}
});

it("shows delayed HTML loading, isolates old frame events, and offers a connected retry on failure", async () => {
 vi.useFakeTimers();
 const refresh=vi.fn();
 const props={path:'/page.html',state:{...ready('<h1>Page</h1>'),previewUrl:'/api/media/'+'c'.repeat(64)+'/page.html'},onRefresh:refresh};
 const {host,render,dispose}=await mount(props);
 const loading=()=>[...host.querySelectorAll('[role="status"]')].some(e=>e.textContent==='Loading…');
 try {
  const first=host.querySelector('iframe');
  expect(loading()).toBe(false);
  await act(async()=>vi.advanceTimersByTimeAsync(159));
  expect(loading()).toBe(false);
  await act(async()=>vi.advanceTimersByTimeAsync(1));
  expect(loading()).toBe(true);
  await act(async()=>first.dispatchEvent(new Event('load')));
  expect(loading()).toBe(false);
  const next={...props,state:{...props.state,content:'<h1>Changed</h1>'}};
  await render(next);
  const second=host.querySelector('iframe');
  expect(second).not.toBe(first);
  await act(async()=>{first.dispatchEvent(new Event('load'));vi.advanceTimersByTime(160)});
  expect(loading()).toBe(true);
  await act(async()=>button(host,'Show source').click());
  expect(loading()).toBe(false);
  await act(async()=>button(host,'Show preview').click());
  expect(host.querySelector('iframe')).toBe(second);
  await act(async()=>second.dispatchEvent(new Event('load')));
  globalThis.fetch.mockResolvedValueOnce({ok:false,status:503});
  const failed={...next,state:{...next.state,content:'<h1>Offline</h1>'}};
  await render(failed);
  expect(loading()).toBe(false);
  expect(host.querySelector('[role="alert"]').textContent).toContain('Unable to load HTML preview');
  const retry=[...host.querySelectorAll('button')].find(e=>e.textContent==='Retry');
  await act(async()=>retry.click());
  expect(refresh).toHaveBeenCalledOnce();
  await render({...failed,connected:false});
  expect(retry.disabled).toBe(true);
  await act(async()=>retry.click());
  expect(refresh).toHaveBeenCalledOnce();
 } finally {await dispose();vi.useRealTimers();}
});

it("aborts obsolete HTML probes and ignores their late failure without refetching on mode switches",async()=>{
 const pending=Promise.withResolvers();
 const fetch=vi.spyOn(globalThis,'fetch').mockReturnValueOnce(pending.promise).mockResolvedValue({ok:true});
 const props={path:'/page.html',state:{...ready('<h1>Old</h1>'),previewUrl:'/api/media/'+'d'.repeat(64)+'/page.html'}};
 const {host,render,dispose}=await mount(props);
 try {
  expect(host.querySelector('iframe')).toBeNull();
  const signal=fetch.mock.calls[0][1].signal;
  await render({...props,state:{...props.state,content:'<h1>New</h1>'}});
  expect(signal.aborted).toBe(true);
  const frame=host.querySelector('iframe');
  await act(async()=>pending.resolve({ok:false,status:404}));
  expect(host.querySelector('[role="alert"]')).toBeNull();
  expect(host.querySelector('iframe')).toBe(frame);
  await act(async()=>button(host,'Show source').click());
  await act(async()=>button(host,'Show preview').click());
  expect(fetch).toHaveBeenCalledTimes(2);
  expect(fetch.mock.calls.every(([,options])=>options.method==='HEAD')).toBe(true);
 }finally{await dispose();}
});

it("shows decode failures, gates refresh offline, and resets errors for refreshed image content", async () => {
 const onRefresh=vi.fn();
 const image=content=>({...ready(content),encoding:"base64",mime:"image/png"});
 const {host,render,dispose}=await mount({path:"/photo.png",state:image("bad"),onRefresh});
 try {
  await act(async()=>host.querySelector("img").dispatchEvent(new Event("error")));
  expect(host.textContent).toContain("This image could not be displayed");
  const retry=[...host.querySelectorAll("button")].find(button=>button.textContent.includes("Reload image"));
  await act(async()=>retry.click());
  expect(onRefresh).toHaveBeenCalledOnce();
  await render({path:"/photo.png",state:image("bad"),connected:false,onRefresh});
  expect([...host.querySelectorAll("button")].find(button=>button.textContent.includes("Reload image")).disabled).toBe(true);
  await render({path:"/photo.png",state:image("new-content"),onRefresh});
  expect(host.textContent).not.toContain("This image could not be displayed");
  expect(host.querySelector("img").getAttribute("src")).toBe("data:image/png;base64,new-content");
  await act(async()=>button(host,"Open photo.png in full screen preview").click());
  expect(document.querySelector('[role="dialog"]')).not.toBeNull();
 } finally {await dispose();}
});
