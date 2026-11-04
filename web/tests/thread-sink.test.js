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
