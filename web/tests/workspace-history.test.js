// @vitest-environment jsdom
import { act, createElement, useRef } from 'react';
import { createRoot } from 'react-dom/client';
import { MemoryRouter, Routes, Route, useLocation } from 'react-router-dom';
import { beforeEach, afterEach, expect, it, vi } from 'vitest';
import { HistorySearch } from '../ui/components/agent/history-search';
import { WorktreeControl } from '../ui/components/agent/worktree-control';
import { useWorkspaceHistory } from '../ui/components/agent/use-workspace-history';
import { useThreadSink, applyScopedMessage } from '../ui/components/agent/thread-sink';
import { requestWorkspace } from '../ui/components/agent/workspace-history';

vi.mock('../ui/components/popover', () => ({
  Popover: ({children, onOpenChange}) => createElement('section', null, createElement('button', {onClick:()=>onOpenChange(true), 'data-open':true}, 'open'), children),
  PopoverTrigger: ({children}) => children,
  PopoverContent: ({children}) => children,
}));
const t = key => key;
let root, host, sent, ops, location, sink, workspace;
const sendRaw = frame => { sent.push(frame); return true; };
beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  host = document.createElement('div'); document.body.append(host); root=createRoot(host);
  sent=[]; ops={workspaceReplies:{current:new Map()}};
});
afterEach(async () => { await act(async()=>root.unmount()); host.remove(); vi.useRealTimers(); vi.unstubAllGlobals(); });
const click = async text => act(async()=>[...host.querySelectorAll('button')].find(button=>button.textContent===text).click());
const respond = async (frame, value) => act(async()=>ops.workspaceReplies.current.get(frame.id)?.({id:frame.id,thread:frame.thread,...value}));
const input = async (node, value) => act(async()=>{
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set.call(node,value);
  node.dispatchEvent(new Event('input',{bubbles:true}));
});
function Location() { location=useLocation(); return null; }

it('debounces authorized device body search, ignores stale replies, and jumps to a stable message URL', async()=>{
  vi.useFakeTimers();
  await act(async()=>root.render(createElement(MemoryRouter,null,
    createElement(HistorySearch,{device:'authorized-device',thread:'one',ready:true,ops,sendRaw,t}),createElement(Location))));
  await click('open');
  await input(host.querySelector('input'), 'first');
  await act(async()=>vi.advanceTimersByTime(299)); expect(sent).toHaveLength(0);
  await act(async()=>vi.advanceTimersByTime(1)); const first=sent.at(-1);
  expect(first).toMatchObject({op:'search_history',thread:'one',q:'first'});
  const stale=ops.workspaceReplies.current.get(first.id);
  await input(host.querySelector('input'), 'second');
  await act(async()=>vi.advanceTimersByTime(300)); const second=sent.at(-1);
  await act(async()=>stale({hits:[{thread:'wrong',messageId:'wrong',title:'stale',snippet:'stale'}]}));
  expect(host.textContent).not.toContain('stale');
  await respond(second,{hits:[{thread:'two',messageId:'durable',title:'found',snippet:'message body'}]});
  const hit=[...host.querySelectorAll('button')].find(button=>button.textContent.includes('message body'));
  await act(async()=>hit.click());
  expect(location.pathname).toBe('/agent/two');
  expect(new URLSearchParams(location.search).get('device')).toBe('authorized-device');
  expect(new URLSearchParams(location.search).get('message')).toBe('durable');
});

it('requires removal confirmation and sends only correlated managed-worktree actions', async()=>{
  await act(async()=>root.render(createElement(WorktreeControl,{device:'remote',thread:'one',cwd:'/repo',ready:true,ops,sendRaw,t})));
  await click('open');
  expect(sent.at(-1)).toMatchObject({op:'worktree',action:'status',thread:'one'});
  await respond(sent.at(-1),{state:'none'});
  await input(host.querySelector('input'),'main');
  await click('agent.worktreeCreate');
  expect(sent.at(-1)).toMatchObject({action:'create',base:'main',thread:'one'});
  await respond(sent.at(-1),{state:'ready',path:'/worktree',branch:'managed'});
  const count=sent.length;
  await click('agent.worktreeRemove'); expect(sent).toHaveLength(count);
  expect(host.textContent).toContain('agent.worktreeConfirm');
  await click('common.cancel'); expect(sent).toHaveLength(count);
  await click('agent.worktreeRemove'); await click('agent.worktreeRemove');
  expect(sent.at(-1)).toMatchObject({action:'remove',thread:'one'});
  await respond(sent.at(-1),{state:'error',path:'/worktree',error:'Uncommitted files'});
  expect(host.textContent).toContain('Uncommitted files');
  const beforeMerge=sent.length;
  await click('agent.worktreeMerge'); expect(sent).toHaveLength(beforeMerge);
  expect(host.textContent).toContain('agent.worktreeMergeConfirm');
  await click('agent.worktreeMerge');
  expect(sent.at(-1)).toMatchObject({action:'merge',thread:'one'});
  expect(host.textContent).not.toContain('agent.worktreeMerged');
  await respond(sent.at(-1),{state:'error',path:'/worktree',error:'Source branch diverged'});
  expect(host.textContent).toContain('Source branch diverged');
  expect(host.textContent).not.toContain('agent.worktreeMerged');
  await click('agent.worktreeMerge'); await click('agent.worktreeMerge');
  await respond(sent.at(-1),{state:'ready',path:'/worktree',branch:'managed'});
  expect(host.textContent).toContain('agent.worktreeMerged');
});

it('around-loads direct links and prepares an edit fork without sending a chat turn', async()=>{
  const scrolled=vi.fn();
  const copied=vi.fn();
  vi.stubGlobal('requestAnimationFrame', fn => { fn(); return 1; });
  vi.stubGlobal('cancelAnimationFrame', vi.fn());
  HTMLElement.prototype.scrollIntoView=scrolled;
  function Harness() {
    sink=useThreadSink(); ops=sink.ops;
    const viewport=useRef(null);
    workspace=useWorkspaceHistory({sink,device:'remote',thread:'one',ready:true,sendRaw,viewport,t,onCopy:copied});
    return createElement('div',{ref:viewport},sink.state.items.map(item=>createElement('div',{key:item.key,'data-thread-message':item.key,tabIndex:-1},item.text)));
  }
  await act(async()=>root.render(createElement(MemoryRouter,{initialEntries:['/agent/one?device=remote&message=old']},createElement(Routes,null,createElement(Route,{path:'/agent/:threadId',element:createElement(Harness)})))));
  expect(sent[0]).toMatchObject({op:'history_page',around:'old',limit:50,thread:'one'});
  await respond(sent[0],{history:[{role:'user',messageId:'old',text:'original'}],historyHasMore:true,historyBefore:'old'});
  expect(scrolled).toHaveBeenCalled(); expect(workspace.highlight).toBe('old');
  await act(async()=>workspace.copyLink('old'));
  const copiedURL=new URL(copied.mock.calls[0][1]);
  expect(copiedURL.pathname).toBe('/agent/one');
  expect(copiedURL.searchParams.get('device')).toBe('remote');
  expect(copiedURL.searchParams.get('message')).toBe('old');
  await act(async()=>workspace.fork('old','review this edit'));
  expect(sent.at(-1)).toMatchObject({op:'fork_thread',messageId:'old',editText:'review this edit',thread:'one'});
  expect(sent.some(frame=>frame.op==='chat')).toBe(false);
  await respond(sent.at(-1),{op:'thread_switched',thread:'fork',draft:'review this edit'});
  await act(async()=>applyScopedMessage(ops,{op:'thread_peeked',thread:'fork',history:[],draft:'review this edit'},t));
  expect(sink.state.draft).toBe('review this edit'); expect(sink.state.running).toBe(false);
});

it('does not deliver a canceled request even if a previously captured callback fires',()=>{
  const replies=new Map(), accepted=vi.fn();
  const cancel=requestWorkspace(replies,()=>true,{op:'history_page',id:'old',thread:'one'},accepted);
  const stale=replies.get('old'); cancel(); stale({history:[]});
  expect(accepted).not.toHaveBeenCalled();
});

it('merges repeated pages without replacing mounted row identities or live content', async()=>{
  const {mergeHistory}=await import('../ui/components/agent/workspace-history');
  const {replayHistory}=await import('../ui/components/agent/protocol');
  const current=replayHistory([{role:'user',messageId:'u',text:'request'},{role:'assistant',messageId:'a',text:'stream'}]);
  current[1].streaming=true;
  const page=replayHistory([{role:'user',messageId:'old',text:'older'},{role:'user',messageId:'u',text:'request'},{role:'assistant',messageId:'a',text:'stale'}]);
  const next=mergeHistory(current,page,'before',new Set(['u','a']));
  expect(next.map(item=>item.messageId)).toEqual(['old','u','a']);
  expect(next[1]).toBe(current[0]); expect(next[2]).toBe(current[1]);
  expect(next[2].text).toBe('stream');
  expect(mergeHistory(next,page,'before',new Set(['u','a'])).map(item=>item.messageId)).toEqual(['old','u','a']);
});
