import { JSDOM } from 'jsdom';
import { act, createElement } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, it } from 'vitest';
import { applyScopedMessage, useThreadSink } from '../ui/components/agent/thread-sink';

let dom, root, sink, previous;
beforeEach(async () => {
  dom = new JSDOM('<div id="root"></div>', { pretendToBeVisual: true });
  previous = Object.fromEntries(['window', 'document', 'requestAnimationFrame', 'IS_REACT_ACT_ENVIRONMENT'].map(k => [k, Object.getOwnPropertyDescriptor(globalThis, k)]));
  Object.assign(globalThis, { window: dom.window, document: dom.window.document, requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window), IS_REACT_ACT_ENVIRONMENT: true });
  function Harness() { sink = useThreadSink(); return null; }
  root = createRoot(document.getElementById('root'));
  await act(async () => root.render(createElement(Harness)));
});
afterEach(async () => {
  await act(async () => root.unmount());
  dom.window.close();
  for (const [key, descriptor] of Object.entries(previous)) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor);
    else delete globalThis[key];
  }
});
const frame = async message => act(async () => applyScopedMessage(sink.ops, message, key => key));

it('keeps streamed output in the same running turn after a busy-command rejection', async () => {
  await frame({ op: 'turn_start', text: 'hello' });
  await act(async () => sink.ops.appendDelta('before'));
  await frame({ op: 'error', message: 'agent is busy; stop first' });
  await act(async () => sink.ops.appendDelta(' after'));
  expect(sink.state.running).toBe(true);
  expect(sink.state.items.filter(x => x.kind === 'turn').map(x => x.text)).toEqual(['before after']);
});

it('clears all transient state on reset and ignores an idle stop acknowledgement', async () => {
  await frame({ op: 'turn_start', text: 'hello' });
  await act(async () => {
    sink.ops.appendDelta('partial');
    sink.ops.setStopping(true);
    sink.ops.setTodos([{ text: 'pending', status: 'pending' }]);
    sink.ops.setQueue(['later']);
    sink.ops.setTurnUsage({ inputTokens: 3, outputTokens: 2 });
    sink.ops.setCurrentTool('run');
    sink.ops.openToolIds.current.add('tool1');
  });
  await frame({ op: 'reset_ok' });
  expect(sink.state).toMatchObject({ running: false, stopping: false, todos: [], queue: [], turnUsage: null, currentTool: '', workingSince: null });
  expect(sink.ops.openToolIds.current.size).toBe(0);
  expect(sink.ops.turnOpen.current).toBe(false);
  const items = sink.state.items;
  await frame({ op: 'stopped', canceled: false });
  expect(sink.state.items).toEqual(items);
  expect(items.map(x => x.kind)).toEqual(['system']);
});

it('replaces stale state and unlocks stop when a secondary snapshot arrives', async () => {
  await act(async () => { sink.ops.setStopping(true); sink.ops.setQueue(['stale']); sink.ops.appendDelta('stale'); });
  await frame({ op: 'thread_peeked', running: false, history: [], queue: [], todos: [] });
  expect(sink.state).toMatchObject({ items: [], queue: [], todos: [], running: false, stopping: false, workingSince: null });
});

it('keeps inspected job output during list refresh and removes it on reset', async () => {
  await frame({ op: 'job', items: { id: 'job1', kind: 'shell', label: 'build', state: 'running', output: 'compiling' } });
  await frame({ op: 'jobs', items: [{ id: 'job1', kind: 'shell', label: 'build', state: 'completed' }] });
  expect(sink.state.jobs).toMatchObject([{ id: 'job1', state: 'completed', output: 'compiling' }]);
  await frame({ op: 'job', items: { id: 'job1', kind: 'shell', label: 'build', state: 'completed', output: 'compiled', result: 'exit code: 0' } });
  expect(sink.state.jobs).toHaveLength(1);
  expect(sink.state.jobs[0].result).toBe('exit code: 0');
  await frame({ op: 'job', items: { id: 'job1', kind: 'shell', label: 'build', state: 'failed', error: 'failed' } });
  expect(sink.state.jobs[0]).toMatchObject({ output: 'compiled', result: 'exit code: 0', error: 'failed' });
  await frame({ op: 'reset_ok' });
  expect(sink.state.jobs).toEqual([]);
});

it('replaces the live transcript with the authoritative compacted history', async () => {
  await frame({ op: 'turn_start', text: '/compact' });
  await frame({ op: 'delta', text: 'compressing' });
  await frame({ op: 'done', compacted: true, history: [{ role: 'user', text: 'Summary with task state' }, { role: 'assistant', text: 'Latest preserved answer' }] });
  expect(sink.state.running).toBe(false);
  expect(sink.ops.turnOpen.current).toBe(false);
  expect(sink.state.items.map(item => item.text)).toEqual(['Summary with task state', 'Latest preserved answer', 'agent.compacted']);
});

it('keeps task list order when refreshing an inspected task', async () => {
  const jobs = ['first', 'second'].map(id => ({ id, kind: 'shell', label: id, state: 'running' }));
  await frame({ op: 'jobs', items: jobs });
  await frame({ op: 'job', items: { ...jobs[0], output: 'progress' } });
  expect(sink.state.jobs.map(job => job.id)).toEqual(['first', 'second']);
  expect(sink.state.jobs[0].output).toBe('progress');
});

it('keeps foreground streaming intact after a background job operation fails', async () => {
  await frame({ op: 'turn_start', text: 'continue working' });
  await act(async () => sink.ops.appendDelta('before'));
  await frame({ op: 'job_error', message: 'unknown job in this thread' });
  await act(async () => sink.ops.appendDelta(' after'));
  expect(sink.state.running).toBe(true);
  expect(sink.ops.turnOpen.current).toBe(true);
  expect(sink.state.jobsError).toBe('unknown job in this thread');
  expect(sink.state.items.filter(x => x.kind === 'turn').map(x => x.text)).toEqual(['before after']);
  await frame({ op: 'reset_ok' });
  expect(sink.state.jobsError).toBe('');
});

it('coalesces streaming scroll requests without losing a forced jump', async () => {
  const callbacks = [];
  window.requestAnimationFrame = callback => { callbacks.push(callback); return callbacks.length; };
  const el = { scrollHeight: 1000, scrollTop: 0, clientHeight: 200 };
  sink.ops.registerScrollEl(el);
  await act(async () => {
    sink.ops.setItems([{ kind: 'user', key: 1, text: 'restored history' }]);
    sink.ops.scrollDown(false);
    sink.ops.scrollDown(true);
    for (let i = 0; i < 100; i++) sink.ops.scrollDown(false);
    expect(callbacks.length).toBe(0); // no measurement before commit
  });
  expect(callbacks.length).toBe(1);
  callbacks[0]();
  expect(el.scrollTop).toBe(1000);
  el.scrollTop = 0;
  await act(async () => sink.ops.scrollDown(false));
  callbacks[1]();
  expect(el.scrollTop).toBe(0);
});

it('restores structured notifications, steering and server timing from history', async () => {
  const job = {id:'job',kind:'shell',state:'completed',label:'Check'};
  await frame({op:'thread_peeked',history:[
    {role:'user',text:'internal data',display:{job,steering:true,time:1000}},
    {role:'assistant',text:'Complete',display:{time:1000,endedAt:9000}},
  ]});
  expect(sink.state.items[0]).toMatchObject({job,steering:true,time:1000});
  expect(sink.state.items[1]).toMatchObject({time:1000,endedAt:9000});
});

it('renders boundary steering as a new user message without interrupting the run or its clock', async () => {
  await frame({ op: 'turn_start', text: 'original', display: { time: 1000 } });
  await frame({ op: 'delta', text: 'before steering' });
  await frame({ op: 'reasoning', text: 'thinking before steering' });
  await frame({ op: 'tool_start', id: 'read', name: 'read_file' });
  await frame({ op: 'tool_end', id: 'read', name: 'read_file', result: 'finished' });
  await frame({ op: 'steering', text: 'new direction', display: { time: 2000, steering: true } });
  await frame({ op: 'delta', text: 'adjusted response' });
  expect(sink.state.running).toBe(true);
  expect(sink.state.workingSince).toBe(1000);
  expect(sink.state.items.map(item => item.kind)).toEqual(['user', 'turn', 'user', 'turn']);
  expect(sink.state.items[1]).toMatchObject({ streaming: false, streamingReasoning: false });
  expect(sink.state.items[1].tools[0]).toMatchObject({ done: true, result: 'finished' });
  expect(sink.state.items[2]).toMatchObject({ text: 'new direction', steering: true, time: 2000 });
  expect(sink.state.items[3].text).toBe('adjusted response');
});

it('shows reasoning before completion and finalizes it without duplication', async () => {
  await frame({ op: 'turn_start', text: 'inspect' });
  await frame({ op: 'reasoning', text: '检查设备' });
  expect(sink.state.items[1]).toMatchObject({ reasoning: '检查设备', streamingReasoning: true });
  await frame({ op: 'reasoning', text: '，再执行。' });
  await frame({ op: 'delta', text: 'ready' });
  expect(sink.state.items[1]).toMatchObject({ reasoning: '检查设备，再执行。', text: 'ready' });
  await frame({ op: 'done', reasoning: '检查设备，再执行。' });
  expect(sink.state.items[1]).toMatchObject({
    reasoning: '检查设备，再执行。', text: 'ready', streamingReasoning: false, streaming: false,
  });
  expect(sink.state.running).toBe(false);
});

it('keeps following when a streamed block grows beyond the bottom threshold', async () => {
  const callbacks = [];
  window.requestAnimationFrame = callback => { callbacks.push(callback); return callbacks.length; };
  const el = { scrollHeight: 1000, scrollTop: 800, clientHeight: 200 };
  sink.ops.registerScrollEl(el);
  await act(async () => sink.ops.appendDelta('a tall code block'));
  el.scrollHeight = 1800; // committed content grew by more than the 120px threshold
  await act(async () => sink.ops.appendDelta('more content before the same animation frame'));
  el.scrollHeight = 2200;
  expect(callbacks).toHaveLength(1);
  callbacks[0]();
  expect(el.scrollTop).toBe(2200);
});

it('does not pull the reader back down after an upward scroll before paint', async () => {
  const callbacks = [];
  window.requestAnimationFrame = callback => { callbacks.push(callback); return callbacks.length; };
  const el = { scrollHeight: 1000, scrollTop: 800, clientHeight: 200 };
  sink.ops.registerScrollEl(el);
  await act(async () => sink.ops.appendDelta('new content'));
  el.scrollHeight = 1800;
  el.scrollTop = 500;
  callbacks[0]();
  expect(el.scrollTop).toBe(500);
  await act(async () => sink.ops.appendDelta('still streaming'));
  el.scrollHeight = 2200;
  callbacks[1]();
  expect(el.scrollTop).toBe(500);
});

it('does not carry bottom attachment into a replacement transcript element', async () => {
  const callbacks = [];
  window.requestAnimationFrame = callback => { callbacks.push(callback); return callbacks.length; };
  sink.ops.registerScrollEl({ scrollHeight: 1000, scrollTop: 800, clientHeight: 200 });
  await act(async () => sink.ops.appendDelta('old pane'));
  const replacement = { scrollHeight: 2000, scrollTop: 500, clientHeight: 200 };
  sink.ops.registerScrollEl(replacement);
  callbacks[0]();
  expect(replacement.scrollTop).toBe(500);
});
