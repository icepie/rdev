import { createStreamBatch } from '../ui/components/agent/stream-batch';
import { JSDOM } from 'jsdom';
import { act, createElement } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, it } from 'vitest';
import { applyScopedMessage, useThreadSink } from '../ui/components/agent/thread-sink';

let dom, root, sink, previous, renders;
beforeEach(async () => {
  dom = new JSDOM('<div id="root"></div>', { pretendToBeVisual: true });
  previous = Object.fromEntries(['window', 'document', 'requestAnimationFrame', 'IS_REACT_ACT_ENVIRONMENT'].map(k => [k, Object.getOwnPropertyDescriptor(globalThis, k)]));
  Object.assign(globalThis, { window: dom.window, document: dom.window.document, requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window), IS_REACT_ACT_ENVIRONMENT: true });
  renders = 0;
  function Harness() { renders++; sink = useThreadSink(); return null; }
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

it.each(['error', 'stopped'])('replaces pre-compaction transcript on terminal %s without duplicating the open turn', async op => {
  await frame({ op: 'turn_start', text: 'old request' });
  await act(async () => sink.ops.appendDelta('stale partial output'));
  await frame({ op, compacted: true, canceled: true, message: 'provider unavailable', history: [
    { role: 'user', text: 'Preserved task summary' },
    { role: 'assistant', text: 'Retained answer' },
  ] });
  expect(sink.state.running).toBe(false);
  expect(sink.ops.turnOpen.current).toBe(false);
  const text = JSON.stringify(sink.state.items);
  expect(text).toContain('Preserved task summary');
  expect(text).toContain('Retained answer');
  expect(text).not.toContain('stale partial output');
  expect(text).not.toContain('old request');
  const notices = sink.state.items.filter(item => item.kind === (op === 'error' ? 'error' : 'system'));
  expect(notices.map(item => item.text)).toEqual([op === 'error' ? 'agent.toolError: provider unavailable' : 'agent.stopped']);
});

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

it('preserves autonomy during transport loss but replaces it on thread snapshots and reset', async () => {
  const goal = { objective: 'verify release', status: 'active', tokensUsed: 24, tokenBudget: 1000 };
  await frame({ op: 'turn_start', text: 'work' });
  await frame({ op: 'autonomy', goal, loop: null, message: 'Cannot start a loop during goal mode' });
  expect(sink.state.running).toBe(true);
  expect(sink.state.autonomy).toEqual({ goal, loop: null, message: 'Cannot start a loop during goal mode' });
  await act(async () => sink.ops.resetTransient());
  expect(sink.state.autonomy.goal).toEqual(goal);
  await frame({ op: 'thread_peeked', running: false, history: [], goal: null,
    loop: { prompt: 'inspect changes', status: 'paused', iteration: 2, maxIterations: 3 } });
  expect(sink.state.autonomy.goal).toBeNull();
  expect(sink.state.autonomy.loop).toMatchObject({ status: 'paused', iteration: 2 });
  expect(sink.state.autonomy.message).toBe('');
  await frame({ op: 'reset_ok' });
  expect(sink.state.autonomy).toEqual({ goal: null, loop: null, message: '' });
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

it('tracks reasoning, answer and tool phases without leaving old streaming indicators active', async () => {
  await frame({ op: 'turn_start', text: 'inspect then answer' });
  await frame({ op: 'reasoning', text: 'checking' });
  expect(sink.state.items[1]).toMatchObject({ streaming: false, streamingReasoning: true });
  await frame({ op: 'delta', text: 'I will inspect' });
  expect(sink.state.items[1]).toMatchObject({ streaming: true, streamingReasoning: false });
  await frame({ op: 'tool_start', id: 'read', name: 'read_file' });
  expect(sink.state.items[1]).toMatchObject({ streaming: false, streamingReasoning: false });
  expect(sink.state).toMatchObject({ running: true, currentTool: 'read_file' });
  await frame({ op: 'tool_end', id: 'read', name: 'read_file', result: 'contents' });
  await frame({ op: 'reasoning', text: 'reviewing contents' });
  expect(sink.state.items[1]).toMatchObject({ streaming: false, streamingReasoning: true });
  await frame({ op: 'tool_start', id: 'check', name: 'shell' });
  expect(sink.state.items[1]).toMatchObject({ streaming: false, streamingReasoning: false });
  await frame({ op: 'tool_end', id: 'check', name: 'shell', result: 'ok' });
  await frame({ op: 'delta', text: 'complete' });
  expect(sink.state.items[1]).toMatchObject({ streaming: true, streamingReasoning: false });
  await frame({ op: 'done' });
  expect(sink.state.items[1]).toMatchObject({ streaming: false, streamingReasoning: false });
  expect(sink.state.running).toBe(false);
});

it('ignores empty streaming fragments without creating a turn or switching phases', async () => {
  await frame({ op: 'delta', text: '' });
  await frame({ op: 'reasoning', text: '' });
  expect(sink.state.items).toEqual([]);
  await frame({ op: 'delta', text: 'answer' });
  await frame({ op: 'reasoning', text: '' });
  expect(sink.state.items[0]).toMatchObject({ streaming: true, streamingReasoning: false });
});

it.each([
  [{ op: 'error', message: 'provider failed' }, 'provider failed'],
  [{ op: 'stopped', canceled: true }, 'agent.stopped'],
  [{ op: 'done' }, 'agent.toolResultMissing'],
])('settles unfinished tools on terminal frame %j without changing completed results', async (terminal, reason) => {
  await frame({ op: 'turn_start', text: 'check' });
  await frame({ op: 'tool_start', id: 'complete', name: 'read_file' });
  await frame({ op: 'tool_end', id: 'complete', name: 'read_file', result: 'original output' });
  const completed = sink.state.items[1].tools[0];
  await frame({ op: 'tool_start', id: 'pending', name: 'shell' });
  await act(async () => sink.ops.mutateOpenTurn(turn => ({ ...turn, tools: turn.tools.map(tool => tool.id === 'pending' ? { ...tool, result: 'partial output' } : tool) })));
  await frame(terminal);
  const tools = sink.state.items[1].tools;
  expect(tools[0]).toEqual(completed);
  expect(tools[1]).toMatchObject({ done: true, isError: true, result: 'partial output\n\n' + reason });
  expect(tools[1].doneAt).toBeTypeOf('number');
  expect(sink.state.running).toBe(false);
  expect(sink.ops.openToolIds.current.size).toBe(0);
});

it('keeps an in-flight tool unresolved when only the browser connection closes', async () => {
  await frame({ op: 'tool_start', id: 'pending', name: 'shell' });
  await act(async () => sink.ops.finishTurn());
  expect(sink.state.items[0].tools[0]).toMatchObject({ done: false, isError: false });
});

it('links a question card to the pending ask call rather than its request ID', async () => {
  await frame({ op: 'turn_start', text: 'ask me' });
  await frame({ op: 'tool_start', id: 'model-call', name: 'ask' });
  await frame({ op: 'ask_request', id: 'ui-request', text: 'Choose one', options: [] });
  expect(sink.state.items[2]).toMatchObject({ kind: 'ask', id: 'ui-request', turnKey: sink.state.items[1].key, toolId: 'model-call', answer: null });
  await frame({ op: 'ask_resolved', id: 'ui-request', answer: 'chosen' });
  expect(sink.state.items[2].answer).toBe('chosen');
});

it('keeps model responses in history order across tools and reconnect replay', async () => {
  const frames = [
    { op: 'turn_start', text: 'inspect', display: { time: 1000 } },
    { op: 'assistant_start' },
    { op: 'reasoning', text: 'First thought' },
    { op: 'delta', text: 'Before tools' },
    { op: 'tool_start', id: 'read', name: 'read_file', args: '{}' },
    { op: 'tool_end', id: 'read', name: 'read_file', result: 'file contents' },
    { op: 'assistant_start' },
    { op: 'reasoning', text: 'Second thought' },
    { op: 'delta', text: 'After tools' },
  ];
  // A single batched replay must behave like separately delivered frames.
  await act(async () => frames.forEach(message => applyScopedMessage(sink.ops, message, key => key)));
  const project = items => items.map(({ kind, text, reasoning, tools }) => ({ kind, text, reasoning, tools: tools?.map(({name, args, result}) => ({name, args, result})) }));
  const live = project(sink.state.items);
  expect(sink.state.running).toBe(true);
  expect(sink.state.workingSince).toBe(1000);
  expect(sink.state.items).toHaveLength(3);
  expect(sink.state.items[1]).toMatchObject({ text: 'Before tools', reasoning: 'First thought', streaming: false, streamingReasoning: false });
  expect(sink.state.items[2]).toMatchObject({ text: 'After tools', reasoning: 'Second thought', streaming: true });
  await frame({ op: 'thread_peeked', running: true, history: [], live: frames, working: { since: 1000 } });
  expect(project(sink.state.items)).toEqual(live);
  await frame({ op: 'done', reasoning: 'Second thought' });
  expect(project(sink.state.items)).toEqual(live);
  await frame({ op: 'thread_peeked', history: [
    { role: 'user', text: 'inspect' },
    { role: 'assistant', text: 'Before tools', reasoning: 'First thought', tools: [{ name: 'read_file', args: '{}', result: 'file contents' }] },
    { role: 'assistant', text: 'After tools', reasoning: 'Second thought' },
  ] });
  expect(project(sink.state.items)).toEqual(live);
});

it('does not create an empty message when the next response fails before output', async () => {
  await frame({ op: 'turn_start', text: 'inspect' });
  await frame({ op: 'assistant_start' });
  await frame({ op: 'delta', text: 'Before tools' });
  await frame({ op: 'tool_start', id: 'read', name: 'read_file' });
  await frame({ op: 'tool_end', id: 'read', result: 'contents' });
  await frame({ op: 'assistant_start' });
  await frame({ op: 'error', message: 'provider unavailable' });
  expect(sink.state.items.map(item => item.kind)).toEqual(['user', 'turn', 'error']);
  expect(sink.state.items[1]).toMatchObject({ text: 'Before tools', streaming: false });
});

it('separates tool-only responses even if the provider reuses a tool call id', async () => {
  await frame({ op: 'turn_start', text: 'inspect twice' });
  for (const result of ['first', 'second']) {
    await frame({ op: 'assistant_start' });
    await frame({ op: 'tool_start', id: 'call', name: 'read_file' });
    await frame({ op: 'tool_end', id: 'call', result });
  }
  await frame({ op: 'assistant_start' });
  await frame({ op: 'delta', text: 'Complete' });
  await frame({ op: 'done' });
  const turns = sink.state.items.filter(item => item.kind === 'turn');
  expect(turns.map(turn => turn.text)).toEqual(['', '', 'Complete']);
  expect(turns.flatMap(turn => turn.tools.map(tool => tool.result))).toEqual(['first', 'second']);
});

it('restores explicit tool failures without guessing from result text', async () => {
  await frame({ op: 'thread_peeked', history: [{ role:'assistant', tools:[
    {name:'read_file',result:'permission denied',isError:true},
    {name:'read_file',result:'error: literal file content',isError:false},
    {name:'read_file',result:'legacy result'},
  ] }, { role:'assistant',reasoning:'Thinking before cancellation' }] });
  expect(sink.state.items[0].tools.map(tool=>tool.isError)).toEqual([true,false,false]);
  expect(sink.state.items[1].reasoning).toBe('Thinking before cancellation');
});


it('reduces burst commits while preserving the actual thread sink transcript', async () => {
  const messages = Array.from({length:200}, () => ({op:'delta',thread:'a',text:'中文🙂 output\n'}));
  const before = renders;
  for (const message of messages) await frame(message);
  const directCommits = renders - before;
  const expected = sink.state.items[0].text;
  await act(async () => sink.ops.reset());
  const start = renders;
  const batch = createStreamBatch(message => applyScopedMessage(sink.ops, message, key => key));
  // Separate event deliveries in the same burst, followed by the real terminal
  // boundary: done must flush the tail before finalizing the assistant turn.
  await act(async () => {
    messages.forEach(batch.push);
    batch.push({op:'done',thread:'a'});
  });
  batch.dispose();
  expect(sink.state.items[0]).toMatchObject({text:expected, streaming:false});
  expect(sink.state.running).toBe(false);
  expect(directCommits).toBe(200);
  expect(renders-start).toBe(1);
});


it.each(['completed', 'failed', 'canceled'])('requires final details after a running job becomes %s', async state => {
  await frame({op:'job', items:{id:'job',kind:'shell',label:'work',state:'running',output:'partial'}});
  expect(sink.state.jobs[0].detailState).toBe('running');
  await frame({op:'jobs', items:[{id:'job',kind:'shell',label:'work',state}]});
  expect(sink.state.jobs[0]).toMatchObject({state,detailState:'running',output:'partial'});
  await frame({op:'job', items:{id:'job',kind:'shell',label:'work',state,output:'final'}});
  expect(sink.state.jobs[0]).toMatchObject({state,detailState:state,output:'final'});
  await frame({op:'jobs', items:[{id:'job',kind:'shell',label:'work',state}]});
  expect(sink.state.jobs[0]).toMatchObject({detailState:state,output:'final'});
});

it('recognizes a loaded terminal detail even when omitempty removed empty result fields', async () => {
  await frame({op:'job',items:{id:'empty',kind:'subagent',label:'empty',state:'completed'}});
  expect(sink.state.jobs[0]).toMatchObject({state:'completed',detailState:'completed',output:'',result:''});
});

it.each(['job_steered','job_cancelled'])('routes %s receipts without closing the foreground turn and settles on disconnect', async (op) => {
  const replies = [];
  sink.ops.jobActionReplies.current.set('guidance-1', message => replies.push(message));
  await frame({op:'turn_start',text:'task'});
  await frame({op:'delta',text:'before'});
  await frame({op,id:'unknown',thread:'a',jobId:'child'});
  expect(replies).toEqual([]);
  await frame({op,id:'guidance-1',thread:'a',jobId:'child'});
  expect(replies).toMatchObject([{id:'guidance-1',jobId:'child'}]);
  expect(sink.ops.jobActionReplies.current.size).toBe(0);
  expect(sink.ops.turnOpen.current).toBe(true);
  sink.ops.jobActionReplies.current.set('guidance-2', message => replies.push(message));
  await act(async () => sink.ops.resetTransient());
  expect(replies.at(-1)).toBeNull();
  expect(sink.ops.jobActionReplies.current.size).toBe(0);
});

it('retains pending guidance on summaries and replaces it on explicit details', async () => {
  const job = {id:'child',kind:'subagent',label:'inspect',state:'running'};
  await frame({op:'job',items:{...job,pendingGuidance:['first',null,7,'second']}});
  expect(sink.state.jobs[0].pendingGuidance).toEqual(['first','second']);
  await frame({op:'jobs',items:[job]});
  expect(sink.state.jobs[0].pendingGuidance).toEqual(['first','second']);
  await frame({op:'job',items:job});
  expect(sink.state.jobs[0].pendingGuidance).toEqual([]);
});

it('tracks cancellation requests from detail and list snapshots until a terminal reply', async () => {
  const job = {id:'slow',kind:'shell',label:'cleanup',state:'running'};
  await frame({op:'jobs',items:[job]});
  await frame({op:'job',items:{...job,cancelRequested:true}});
  expect(sink.state.jobs[0]).toMatchObject({state:'running',cancelRequested:true});
  await frame({op:'jobs',items:[{...job,cancelRequested:true}]});
  expect(sink.state.jobs[0].cancelRequested).toBe(true);
  await frame({op:'job',items:{...job,state:'canceled'}});
  expect(sink.state.jobs[0]).toMatchObject({state:'canceled',cancelRequested:false});
});

it('job invalidations trigger refresh without replacing data or closing the active turn', async () => {
  await frame({op:'job',items:{id:'work',kind:'shell',label:'work',state:'running',output:'keep'}});
  await frame({op:'turn_start',text:'foreground'});
  await frame({op:'delta',text:'ongoing'});
  const revision = sink.state.jobsRevision;
  await frame({op:'jobs_changed',thread:'thread-a'});
  expect(sink.state.jobsRevision).toBe(revision+1);
  expect(sink.state.jobs[0].output).toBe('keep');
  expect(sink.ops.turnOpen.current).toBe(true);
});

it('renders image-only guidance with preview while preserving the active turn timer', async () => {
  await frame({op:'turn_start',text:'inspect',display:{time:1000}});
  await frame({op:'delta',text:'first response'});
  const since = sink.state.workingSince;
  await frame({op:'steering',text:'',images:['guide.png'],imagePreviews:['data:image/png;base64,aW1hZ2U='],display:{steering:true,time:2000}});
  expect(sink.state.items.at(-1)).toMatchObject({kind:'user',text:'',images:['guide.png'],imagePreviews:['data:image/png;base64,aW1hZ2U='],steering:true});
  expect(sink.state.running).toBe(true);
  expect(sink.state.workingSince).toBe(since);
});


it('updates queue modes and promotion errors without interrupting the active turn',async()=>{
 await frame({op:'turn_start',text:'working'});
 await act(async()=>sink.ops.appendDelta('partial'));
 await frame({op:'queue',items:['same','same'],queueIds:['one','two'],queueModes:['steer','follow_up']});
 await frame({op:'queue_promoted',queueId:'two',isError:true,message:'already consumed'});
 expect(sink.state).toMatchObject({queueModes:['steer','follow_up'],queueError:'already consumed',running:true});
 expect(sink.ops.turnOpen.current).toBe(true);
 await frame({op:'queue_promoted',queueId:'two'});
 expect(sink.state.queueError).toBe('');
 await frame({op:'thread_peeked',running:true,history:[],queue:['later'],queueIds:['three'],queueModes:['model']});
 expect(sink.state.queueModes).toEqual(['model']);
 await frame({op:'reset_ok'});
 expect(sink.state).toMatchObject({queueModes:[],queueError:''});
});

it('shows content filtering as a provider notice while preserving partial output', async () => {
  await frame({ op: 'turn_start', text: 'request' });
  await act(async () => sink.ops.appendDelta('partial answer'));
  await frame({ op: 'error', code: 'provider_content_filter', message: 'provider filtering' });
  expect(sink.state.running).toBe(false);
  expect(sink.state.items.some(item => item.kind === 'turn' && item.text === 'partial answer')).toBe(true);
  expect(sink.state.items.some(item => item.kind === 'system' && item.text === 'agent.providerContentFilter')).toBe(true);
  expect(sink.state.items.some(item => item.kind === 'error')).toBe(false);
});
