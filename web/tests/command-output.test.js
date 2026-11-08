// @vitest-environment jsdom
import {act,createElement} from 'react';
import {createRoot} from 'react-dom/client';
import {renderToStaticMarkup} from 'react-dom/server';
import {expect,it,vi} from 'vitest';
vi.mock('../ui/lib/ansi-output', async original => {
 const actual=await original();
 return {...actual,renderAnsiOutput:vi.fn(actual.renderAnsiOutput)};
});
vi.mock('../ui/components/icon',()=>({Icon:()=>null}));
vi.mock('../ui/components/agent/diff-view',()=>({PierreFileDiff:()=>null}));
vi.mock('../ui/components/disclosure-body',()=>({DisclosureBody:({children})=>children}));
import {renderAnsiOutput} from '../ui/lib/ansi-output';
import {CommandOutputBody} from '../ui/components/agent/command-output';
import {ToolRow} from '../ui/components/agent/tool-row';

it.each(['shell','read_file'])('retains server-bounded output tails for %s results',name=>{
 const stdout='begin\n'.repeat(850)+'[earlier output truncated]\nFINAL RESULT';
 const result=name==='shell'? `exit code: 1\n<stdout>\n${stdout}\n</stdout>\n<stderr>\n${'warning\n'.repeat(650)}FINAL ERROR\n</stderr>`:stdout;
 const html=renderToStaticMarkup(createElement(ToolRow,{
  tool:{id:'long',name,done:true,args:'{}',result},t:key=>key,
 }));
 expect(html).toContain('FINAL RESULT');
 expect(html).toContain('[earlier output truncated]');
 expect(html).not.toContain('agent.truncated');
 if(name==='shell'){
  expect(html).toContain('FINAL ERROR');
  expect(html).not.toContain('&lt;stderr&gt;');
 }
});

it('memoizes raw ANSI conversion and preserves a reader position across output updates',async()=>{
 globalThis.IS_REACT_ACT_ENVIRONMENT=true;
 const host=document.createElement('div');document.body.append(host);
 const root=createRoot(host);
 const tool={id:'raw',name:'shell',done:false,args:'{}',result:'first\n'.repeat(900)+'LATEST'};
 const render=next=>act(async()=>root.render(createElement(CommandOutputBody,{tool:next,t:key=>key})));
 try {
  renderAnsiOutput.mockClear();
  await render(tool);
  expect(host.textContent).toContain('LATEST');
  expect(renderAnsiOutput).toHaveBeenCalledTimes(1);
  await render({...tool,summary:'another status'});
  expect(renderAnsiOutput).toHaveBeenCalledTimes(1);
  const scroll=host.querySelector('.select-text');
  Object.defineProperty(scroll,'scrollHeight',{configurable:true,value:1000});
  Object.defineProperty(scroll,'clientHeight',{configurable:true,value:200});
  scroll.scrollTop=80;
  await act(async()=>scroll.dispatchEvent(new Event('scroll')));
  await render({...tool,result:tool.result+'\nNEWEST'});
  expect(scroll.scrollTop).toBe(80);
  expect(host.textContent).toContain('NEWEST');
  scroll.scrollTop=800;
  await act(async()=>scroll.dispatchEvent(new Event('scroll')));
  await render({...tool,result:tool.result+'\nDONE'});
  expect(scroll.scrollTop).toBe(1000);
  expect(renderAnsiOutput).toHaveBeenCalledTimes(3);
 } finally {
  await act(async()=>root.unmount());host.remove();delete globalThis.IS_REACT_ACT_ENVIRONMENT;
 }
});

it.each([true, false])('opens completed output at the start and follows live output (done=%s)',async done=>{
 globalThis.IS_REACT_ACT_ENVIRONMENT=true;
 const host=document.createElement('div');document.body.append(host);
 const root=createRoot(host);
 const height=vi.spyOn(HTMLElement.prototype,'scrollHeight','get').mockReturnValue(1000);
 const tool={id:'initial',name:'shell',done,args:'{}',result:'first\n'.repeat(900)+'LATEST'};
 const render=next=>act(async()=>root.render(createElement(CommandOutputBody,{tool:next,t:key=>key})));
 try {
  await render(tool);
  const scroll=host.querySelector('.select-text');
  expect(scroll.scrollTop).toBe(done ? 0 : 1000);
  height.mockReturnValue(1200);
  await render({...tool,done:true,result:`exit code: 0\n<stdout>\n${tool.result}\nFINAL\n</stdout>\n<stderr>\n\n</stderr>`});
  expect(host.querySelector('.select-text')).toBe(scroll);
  expect(scroll.scrollTop).toBe(done ? 0 : 1200);
  expect(host.textContent).toContain('FINAL');
 } finally {
  await act(async()=>root.unmount());host.remove();height.mockRestore();delete globalThis.IS_REACT_ACT_ENVIRONMENT;
 }
});
