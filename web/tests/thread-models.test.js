// @vitest-environment jsdom
import {act,createElement} from 'react';
import {createRoot} from 'react-dom/client';
import {expect,it} from 'vitest';
import {parseThreads} from '../ui/components/agent/protocol';
import {useThreadModels} from '../ui/components/agent/use-thread-models';

it('keeps pane model drafts isolated from other threads and completed turns',async()=>{
 globalThis.IS_REACT_ACT_ENVIRONMENT=true;
 const host=document.createElement('div');document.body.append(host);const root=createRoot(host);
 let api;
 let threads=parseThreads([{id:'a',model:'provider/fast'},{id:'b',model:'provider/deep'}]);
 const fallback='provider/default';
 function Harness(){api=useThreadModels(threads,fallback);return null;}
 const render=()=>act(async()=>root.render(createElement(Harness)));
 try {
  await render();
  expect(api.modelFor('a')).toBe('provider/fast');
  expect(api.modelFor('b')).toBe('provider/deep');
  await act(async()=>api.select('a','provider/next'));
  expect(api.modelFor('a')).toBe('provider/next');
  expect(api.modelFor('b')).toBe('provider/deep');
  // A running turn completes under its old model; the next-message choice wins.
  threads=parseThreads([{id:'a',model:'provider/fast',running:false},{id:'b',model:'provider/other'}]);
  await render();
  expect(api.modelFor('a')).toBe('provider/next');
  expect(api.modelFor('b')).toBe('provider/other');
  await act(async()=>api.select('b','provider/draft-b'));
  expect(api.modelFor('a')).toBe('provider/next');
  expect(api.modelFor('b')).toBe('provider/draft-b');
  // Server-backed state wins again after authentication/device reset.
  await act(async()=>api.reset());
  expect(api.modelFor('a')).toBe('provider/fast');
  expect(api.modelFor('b')).toBe('provider/other');
  await act(async()=>api.select('a','provider/deleted'));
  threads=parseThreads([{id:'b',model:'provider/other'}]);await render();
  threads=parseThreads([{id:'a',model:'provider/recreated'},{id:'b',model:42}]);await render();
  expect(api.modelFor('a')).toBe('provider/recreated');
  expect(api.modelFor('b')).toBe(fallback); // legacy/malformed catalogs
 } finally {await act(async()=>root.unmount());host.remove();delete globalThis.IS_REACT_ACT_ENVIRONMENT;}
});
