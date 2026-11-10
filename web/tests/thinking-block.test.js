import { JSDOM } from 'jsdom';
import { act, createElement } from 'react';
import { createRoot } from 'react-dom/client';
import { expect, it, vi } from 'vitest';
vi.mock('../ui/components/icon',()=>({Icon:()=>null}));
vi.mock('../ui/components/markdown',()=>({Markdown:({text})=>createElement('p',{'data-answer':true},text)}));
vi.mock('../ui/components/agent/tool-row',()=>({ToolRow:()=>null,ToolBundleRow:()=>null}));
vi.mock('../ui/components/agent/message-action-bar',()=>({MessageActionBar:({text,onCopy})=>createElement('button',{onClick:()=>onCopy(text),'data-copy':true},'Copy')}));
import { TurnView } from '../ui/components/agent/turn-view';
it('uses BB disclosure lifecycle for inline thinking and copies only the answer',async()=>{
 const dom=new JSDOM('<div id="root"></div>');
 vi.stubGlobal('window',dom.window);vi.stubGlobal('document',dom.window.document);vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT',true);
 const root=createRoot(document.getElementById('root'));
 const onCopy=vi.fn();
 const turn={kind:'turn',key:1,text:'<think>Planning retry',reasoning:'',streaming:true,streamingReasoning:false,tools:[]};
 const render=(text,active)=>root.render(createElement(TurnView,{turn:{...turn,text,streaming:active},active,t:k=>k,copied:false,onCopy,onAddToChat(){}}));
 try {
  await act(()=>render(turn.text,true));
  expect(document.querySelector('[aria-expanded]').getAttribute('aria-expanded')).toBe('true');
  expect(document.querySelector('[data-answer]')).toBeNull();
  expect(document.body.textContent).toContain('Planning retry');
  expect(document.body.textContent).not.toContain('<think>');
  await act(()=>render('<think>Planning retry</think>Done',false));
  expect(document.querySelector('[aria-expanded]').getAttribute('aria-expanded')).toBe('false');
  expect(document.querySelector('[data-answer]').textContent).toBe('Done');
  await act(()=>document.querySelector('[aria-expanded]').click());
  expect(document.querySelector('[aria-expanded]').getAttribute('aria-expanded')).toBe('true');
  await act(()=>document.querySelector('[data-copy]').click());
  expect(onCopy).toHaveBeenCalledWith('Done');
 } finally {await act(()=>root.unmount());vi.unstubAllGlobals();dom.window.close();}
});
