// @vitest-environment jsdom
import { act, createElement } from 'react';
import { createRoot } from 'react-dom/client';
import { expect, it, vi } from 'vitest';
import { useFilePreviewTabs } from '../ui/lib/file-preview-tabs';

it('routes concurrent replies by device/request and invalidates refreshed, closed and disconnected reads', async () => {
 vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true); vi.useFakeTimers();
 const host=document.createElement('div'), root=createRoot(host);
 let tabs;
 function App(){tabs=useFilePreviewTabs('timeout'); return null;}
 const reads=[];
 const read=id=>{reads.push(id);return Promise.resolve();};
 const reply=(id,device,content)=>tabs.accept({op:'file_content',requestId:id,deviceId:device,content});
 try {
  await act(async()=>root.render(createElement(App)));
  await act(async()=>{tabs.open('a','/same.txt',read);tabs.open('b','/same.txt',read);});
  const [a,b]=reads;
  await act(async()=>{expect(reply(a,'b','wrong')).toBe(false);reply(b,'b','second');reply(a,'a','first');});
  expect(tabs.tabs.map(t=>t.state.content)).toEqual(['first','second']);
  await act(async()=>tabs.open('a','/same.txt',read));
  expect(reads).toHaveLength(2);
  await act(async()=>tabs.open('a','/same.txt',read,true));
  const fresh=reads.at(-1);
  await act(async()=>{expect(reply(a,'a','stale')).toBe(false); reply(fresh,'a','fresh');});
  expect(tabs.tabs[0].state.content).toBe('fresh');
  await act(async()=>tabs.open('a','/other.txt',read));
  const closed=reads.at(-1), key=tabs.active;
  await act(async()=>tabs.close(key));
  expect(reply(closed,'a','late')).toBe(false);
  await act(async()=>tabs.open('a','/pending.txt',read));
  const disconnected=reads.at(-1);
  await act(async()=>tabs.failPending('offline'));
  expect(tabs.tabs.at(-1).state.message).toBe('offline');
  expect(tabs.tabs[0].state.content).toBe('fresh');
  expect(reply(disconnected,'a','late')).toBe(false);
  await act(async()=>tabs.open('a','/timeout.txt',read));
  await act(async()=>vi.advanceTimersByTimeAsync(30001));
  expect(tabs.tabs.at(-1).state.message).toBe('timeout');
  await act(async()=>tabs.reset());expect(tabs.tabs).toEqual([]);expect(tabs.active).toBe('');
 } finally {await act(async()=>root.unmount());vi.useRealTimers();vi.unstubAllGlobals();}
});
