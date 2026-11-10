import { JSDOM } from 'jsdom';
import { act, createElement } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
vi.mock('../ui/components/icon', () => ({ Icon: () => null }));
vi.mock('../ui/components/context-menu', () => ({
  ContextMenu: ({children}) => children,
  ContextMenuTrigger: ({children}) => children,
  ContextMenuContent: () => null,
  ContextMenuItem: () => null,
  ContextMenuSeparator: () => null,
}));
vi.mock('../ui/components/dialog', () => ({ Dialog: () => null, DialogContent: () => null, DialogTitle: () => null }));
import { FileExplorer } from '../ui/components/file-explorer';
import { FileWorkspaceDivider } from '../ui/components/file-workspace-divider';
let dom, root;
beforeEach(() => {
  dom = new JSDOM('<main></main>', {url:'http://localhost'});
  for (const [key,value] of Object.entries({window:dom.window,document:dom.window.document,sessionStorage:dom.window.sessionStorage,IS_REACT_ACT_ENVIRONMENT:true})) vi.stubGlobal(key,value);
  root = createRoot(document.querySelector('main'));
});
afterEach(async () => { await act(async()=>root.unmount()); dom.window.close(); vi.unstubAllGlobals(); });
const directory = {path:'/work/src',name:'src',isDir:true};
const file = {path:'/work/src/main.go',name:'main.go'};
const render = props => act(async()=>root.render(createElement(FileExplorer,{path:'/work',entries:[directory],t:key=>key,onNavigate:()=>{},onOpen:()=>{},...props})));
it('restores expanded directories for each device without mixing their state', async () => {
  const load = vi.fn(async()=>[file]);
  const onOpen = vi.fn();
  await render({stateKey:'one',load,onOpen});
  await act(async()=>document.querySelector('[title="src"]').click());
  expect(load).toHaveBeenCalledTimes(1);
  await act(async()=>document.querySelector('[title="main.go"]').click());
  expect(onOpen).toHaveBeenCalledWith(file.path);
  await render({stateKey:'two',load});
  expect(document.querySelector('[title="main.go"]')).toBeNull();
  await render({stateKey:'one',load});
  expect(document.querySelector('[title="main.go"]')).not.toBeNull();
  expect(document.querySelector('[aria-selected="true"]')?.textContent).toContain('main.go');
});
it('supports keyboard traversal and collapsing to the parent', async () => {
  await render({load:async()=>[file]});
  const parent = document.querySelector('[title="src"]');
  await act(async()=>parent.dispatchEvent(new window.KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true})));
  await act(async()=>parent.dispatchEvent(new window.KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true})));
  expect(document.activeElement.title).toBe('main.go');
  await act(async()=>document.activeElement.dispatchEvent(new window.KeyboardEvent('keydown',{key:'ArrowLeft',bubbles:true})));
  expect(document.activeElement).toBe(parent);
});
it('persists divider changes per workspace and restores the default on another device', async () => {
  const renderDivider = storageKey => act(async()=>root.render(createElement('div',null,createElement(FileWorkspaceDivider,{storageKey,label:'Resize'}))));
  await renderDivider('one');
  await act(async()=>document.querySelector('button').dispatchEvent(new window.KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true})));
  expect(sessionStorage.getItem('one')).toBe('32');
  await renderDivider('two');
  expect(document.querySelector('button').getAttribute('aria-valuenow')).toBe('30');
  await renderDivider('one');
  expect(document.querySelector('button').getAttribute('aria-valuenow')).toBe('32');
});

it('updates the divider live but persists only once when dragging finishes', async () => {
  await act(async()=>root.render(createElement('div',null,createElement(FileWorkspaceDivider,{storageKey:'drag',label:'Resize'}))));
  const divider = document.querySelector('button');
  divider.setPointerCapture = () => {};
  divider.hasPointerCapture = () => false;
  divider.parentElement.getBoundingClientRect = () => ({left:0,top:0,width:100,height:100});
  const save = vi.spyOn(dom.window.Storage.prototype, 'setItem');
  const pointer = (type,x) => {
    const event = new window.Event(type,{bubbles:true});
    Object.defineProperties(event,{pointerId:{value:1},clientX:{value:x},clientY:{value:0}});
    return act(async()=>divider.dispatchEvent(event));
  };
  await pointer('pointerdown',30);
  for (const x of [35,40,45,50]) await pointer('pointermove',x);
  expect(divider.parentElement.style.getPropertyValue('--file-tree-split')).toBe('50%');
  expect(save).not.toHaveBeenCalled();
  await pointer('pointerup',50);
  await pointer('lostpointercapture',50);
  expect(save).toHaveBeenCalledTimes(1);
  expect(sessionStorage.getItem('drag')).toBe('50');
  save.mockRestore();
});
