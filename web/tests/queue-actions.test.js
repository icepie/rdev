import { expect, it } from 'vitest';
import { requestQueueRemoval, acceptQueueRemoval } from '../ui/components/agent/queue-actions';

it('returns a message only after its correlated successful removal, once', () => {
  const pending = new Map(), sent = [], restored = [];
  expect(requestQueueRemoval(pending, msg => { sent.push(msg); return true; }, 'thread', 'stable-row', text => restored.push(text))).toBe(true);
  expect(sent[0]).toMatchObject({ op: 'dequeue', thread: 'thread', queueId: 'stable-row' });
  expect(sent[0]).not.toHaveProperty('index');
  expect(restored).toEqual([]);
  acceptQueueRemoval(pending, { id: 'other-request', removed: true, text: 'wrong' });
  const ack = { id: sent[0].id, removed: true, text: 'server-confirmed original' };
  acceptQueueRemoval(pending, ack);
  acceptQueueRemoval(pending, ack);
  expect(restored).toEqual(['server-confirmed original']);
  expect(pending.size).toBe(0);
});

it('does not change the draft on stale removal, failed send, or missing identity', () => {
  const pending = new Map(), sent = [], restored = [];
  const restore = text => restored.push(text);
  requestQueueRemoval(pending, msg => { sent.push(msg); return true; }, 'thread', 'already-running', restore);
  acceptQueueRemoval(pending, { id: sent[0].id, removed: false });
  expect(requestQueueRemoval(pending, () => false, 'thread', 'row', restore)).toBe(false);
  expect(requestQueueRemoval(pending, () => { throw new Error('must not send an index-only operation'); }, 'thread', undefined, restore)).toBe(false);
  expect(restored).toEqual([]);
  expect(pending.size).toBe(0);
});
