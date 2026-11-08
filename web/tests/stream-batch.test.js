import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { createStreamBatch } from '../ui/components/agent/stream-batch';

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());
const delta = (text, thread = 'a') => ({ op: 'delta', text, thread });

it('combines a burst without mutating frames, and delivers by the next timer', () => {
  const output = [], batch = createStreamBatch(frame => output.push(frame));
  const first = Object.freeze(delta('中文🙂'));
  batch.push(first);
  for (let i = 0; i < 199; i++) batch.push(delta('x'));
  expect(output).toEqual([]);
  vi.advanceTimersByTime(16);
  expect(output).toEqual([delta('中文🙂' + 'x'.repeat(199))]);
  expect(first.text).toBe('中文🙂');
  expect(vi.getTimerCount()).toBe(0);
});

it('preserves ordering across reasoning, threads, tools, switches and completion', () => {
  const output = [], batch = createStreamBatch(frame => output.push(frame));
  const frames = [delta('a'), {op:'reasoning',text:'thought',thread:'a'}, delta('b','b'),
    {op:'tool_start',id:'t',thread:'b'}, delta('c','b'), {op:'thread_switched',thread:'b'},
    delta('d','b'), {op:'done',thread:'b'}];
  frames.forEach(batch.push);
  expect(output).toEqual(frames);
  expect(vi.getTimerCount()).toBe(0);
});

it('keeps future metadata intact rather than combining it into a text frame', () => {
  const output = [], batch = createStreamBatch(frame => output.push(frame));
  const frames = [delta('before'), {...delta('special'), sequence:42}, delta('after')];
  frames.forEach(batch.push);
  batch.flush();
  expect(output).toEqual(frames);
});

it('bounds pending data and preserves large and Unicode fragments exactly', () => {
  const output = [], batch = createStreamBatch(frame => output.push(frame));
  const chunks = ['a'.repeat(32000), '🙂'.repeat(1000), 'z'.repeat(40000), 'tail'];
  chunks.forEach(text => batch.push(delta(text)));
  batch.flush();
  expect(output.map(frame => frame.text).join('')).toBe(chunks.join(''));
  expect(output).toHaveLength(4);
  expect(vi.getTimerCount()).toBe(0);
});

it('flushes the disconnect tail once and discards replaced socket work', () => {
  const output = [], first = createStreamBatch(frame => output.push(frame));
  first.push(delta('tail'));
  first.flush(); first.dispose();
  const replacement = createStreamBatch(frame => output.push(frame));
  replacement.push(delta('stale')); replacement.dispose();
  replacement.push(delta('late'));
  vi.runAllTimers();
  expect(output).toEqual([delta('tail')]);
  expect(vi.getTimerCount()).toBe(0);
});

it('does not let empty frames grow the batch or change its phase', () => {
  const output = [], batch = createStreamBatch(frame => output.push(frame));
  batch.push(delta('answer'));
  for(let i=0;i<10000;i++) batch.push({op:'reasoning',text:'',thread:'b'});
  expect(vi.getTimerCount()).toBe(1);
  vi.runAllTimers();
  expect(output).toEqual([delta('answer')]);
});
