// @vitest-environment jsdom
import { act, createElement } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, expect, it, vi } from 'vitest';
import { FilePreviewHeader } from '../ui/components/agent/file-preview-header';
import { dictionaries } from '../ui/lib/dictionaries';
vi.mock('../ui/lib/clipboard',()=>({copyText:vi.fn().mockResolvedValue()}));
import { copyText } from '../ui/lib/clipboard';
const t=(key,args={})=>Object.entries(args).reduce((s,[k,v])=>s.replaceAll(`{${k}}`,String(v)),dictionaries.en[key]||key);
afterEach(()=>{vi.restoreAllMocks();vi.unstubAllGlobals();vi.useRealTimers()});

it('moves actions at the actual pane breakpoint and preserves keyboard menu actions',async()=>{
 globalThis.IS_REACT_ACT_ENVIRONMENT=true;
 let resize;
 const disconnected=vi.fn();
 vi.stubGlobal('ResizeObserver',class{constructor(cb){resize=cb}observe(){}disconnect(){disconnected()}});
 const host=document.createElement('div');document.body.append(host);const root=createRoot(host);
 const refresh=vi.fn(),wrap=vi.fn();
 const props={path:'/workspace/long-name.csv',name:'long-name.csv',content:'a,b\n1,2',externalUrl:'/api/media/'+'a'.repeat(64),loading:false,connected:true,hasPreview:true,viewMode:'source',onViewModeChange:vi.fn(),wrap:false,onWrapChange:wrap,onRefresh:refresh,t};
 const render=next=>act(async()=>root.render(createElement(FilePreviewHeader,{...props,...next})));
 try {
  await render({});
  await act(async()=>resize([{contentRect:{width:560}}]));
  expect(host.querySelector('[aria-label="File actions"]')).toBeNull();
  const external=host.querySelector('a[aria-label="Open in browser"]');
  expect(external.getAttribute('href')).toBe(props.externalUrl);
  expect(external.target).toBe('_blank');
  expect(external.rel).toBe('noopener noreferrer');
  expect(host.querySelector('[aria-label="Copy file contents"]')).not.toBeNull();
  expect(host.querySelector('[aria-label="Show source"]').getAttribute('aria-pressed')).toBe('true');
  await act(async()=>resize([{contentRect:{width:559}}]));
  expect(host.querySelector('[aria-label="Copy file contents"]')).toBeNull();
  expect(host.querySelector('a[aria-label="Open in browser"]')).not.toBeNull();
  const scoped=props.externalUrl+'/page%20one.html';
  await render({externalUrl:scoped});
  expect(host.querySelector('a[aria-label="Open in browser"]').getAttribute('href')).toBe(scoped);
  const trigger=host.querySelector('[aria-label="File actions"]');
  await act(async()=>{trigger.focus();trigger.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}))});
  const menu=document.querySelector('[role="menu"]');expect(menu).not.toBeNull();
  const copy=[...menu.querySelectorAll('[role="menuitem"]')].find(e=>e.textContent.includes('Copy file contents'));
  await act(async()=>copy.click());
  expect(copyText).toHaveBeenLastCalledWith(props.content);
  await render({loading:true,externalUrl:'javascript:alert(1)'});
  expect(host.querySelector('a')).toBeNull();
  await act(async()=>trigger.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true})));
  const refreshing=[...document.querySelectorAll('[role="menuitem"]')].find(e=>e.textContent.includes('Refreshing file'));
  expect(refreshing.getAttribute('aria-disabled')).toBe('true');
  await act(async()=>refreshing.click());expect(refresh).not.toHaveBeenCalled();
 }finally{await act(async()=>root.unmount());host.remove();delete globalThis.IS_REACT_ACT_ENVIRONMENT}
 expect(disconnected).toHaveBeenCalled();
});

it('reports clipboard failure and prevents stale copy completion from replacing a newer result',async()=>{
 globalThis.IS_REACT_ACT_ENVIRONMENT=true;
 vi.useFakeTimers();
 const host=document.createElement('div');document.body.append(host);const root=createRoot(host);
 const pending=Promise.withResolvers();copyText.mockReturnValueOnce(pending.promise).mockRejectedValueOnce(Error('denied'));
 const props={path:'/file.txt',name:'file.txt',content:'body',loading:false,connected:true,hasPreview:false,viewMode:'source',onViewModeChange:()=>{},wrap:false,onWrapChange:()=>{},onRefresh:()=>{},t};
 try {
  await act(async()=>root.render(createElement(FilePreviewHeader,props)));
  await act(async()=>host.querySelector('[aria-label="Copy path for file.txt"]').click());
  await act(async()=>host.querySelector('[aria-label="Copy file contents"]').click());
  expect(host.querySelector('[role="status"]').textContent).toContain('Could not copy');
  await act(async()=>pending.resolve());
  expect(host.querySelector('[role="status"]').textContent).toContain('Could not copy');
  await act(async()=>vi.advanceTimersByTimeAsync(2000));
  expect(host.querySelector('[role="status"]').textContent).toBe('');
 }finally{await act(async()=>root.unmount());host.remove();delete globalThis.IS_REACT_ACT_ENVIRONMENT}
});
