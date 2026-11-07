export type QueueReturns = Map<string, (text: string) => void>;
let sequence = 0;

// A successful socket write is not proof that this item was still queued.
// Only the correlated server acknowledgement may restore it to the composer.
export function requestQueueRemoval(
  pending: QueueReturns,
  send: (payload: Record<string, unknown>) => boolean,
  thread: string,
  queueId: string | undefined,
  onReturn?: (text: string) => void,
): boolean {
  if (!queueId) return false;
  const id = `dequeue_${++sequence}`;
  if (onReturn) pending.set(id, onReturn);
  if (send({ op: "dequeue", id, thread, queueId })) return true;
  pending.delete(id);
  return false;
}

export function acceptQueueRemoval(pending: QueueReturns, message: Record<string, unknown>) {
  const id = typeof message.id === "string" ? message.id : "";
  const onReturn = pending.get(id);
  pending.delete(id);
  if (message.removed === true && typeof message.text === "string") onReturn?.(message.text);
}
