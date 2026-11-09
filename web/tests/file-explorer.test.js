// @vitest-environment jsdom
import {act,createElement} from 'react';
import {createRoot} from 'react-dom/client';
import {expect,it,vi} from 'vitest';
vi.mock('../ui/components/icon',()=>({Icon:()=>null}));
import {FileExplorer} from '../ui/components/file-explorer';
it('loads directories on expansion, sorts folders first, reuses children and navigates by keyboard',async()=>{
 vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT',true);
 const host=document.createElement('div');document.body.append(host);const root=createRoot(host);
 const load=vi.fn(async()=>[{name:'child.md',path:'/dir/child.md'}]),onOpen=vi.fn();
 try {
  await act(async()=>root.render(createElement(FileExplorer,{path:'/',entries:[{name:'z.txt',path:'/z.txt'},{name:'dir',path:'/dir',isDir:true}],load,onOpen,onNavigate:vi.fn(),t:key=>key})));
  expect(load).not.toHaveBeenCalled();
  const folder=host.querySelector('[role=treeitem] > button');expect(folder.title).toBe('dir');
  await act(async()=>folder.click());expect(load).toHaveBeenCalledExactlyOnceWith('/dir');
  const child=host.querySelector('button[title="child.md"]');
  await act(async()=>child.click());expect(onOpen).toHaveBeenCalledWith('/dir/child.md');
  folder.focus();await act(async()=>folder.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true})));
  expect(document.activeElement).toBe(child);
  await act(async()=>folder.click());expect(host.querySelector('button[title="child.md"]')).toBeNull();
  await act(async()=>folder.click());expect(load).toHaveBeenCalledTimes(1);
 }finally{await act(async()=>root.unmount());host.remove();vi.unstubAllGlobals();}
});
