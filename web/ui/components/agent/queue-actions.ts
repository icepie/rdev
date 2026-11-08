import { MAX_IMAGES, MAX_IMAGE_BYTES, type PendingImage } from "./protocol";

type QueueReturn = { restore: (text: string, images: PendingImage[]) => void; settled?: () => void };
export type QueueReturns = Map<string, QueueReturn>;
let sequence = 0;

export function clearQueueReturns(pending: QueueReturns) {
  const callbacks = [...pending.values()];
  pending.clear();
  for (const callback of callbacks) callback.settled?.();
}

// Only a correlated successful acknowledgement may restore queued content.
export function requestQueueRemoval(
  pending: QueueReturns,
  send: (payload: Record<string, unknown>) => boolean,
  thread: string,
  queueId: string | undefined,
  onReturn?: QueueReturn["restore"],
  settled?: () => void,
): boolean {
  if (!queueId) return false;
  const id = `dequeue_${++sequence}`;
  if (onReturn) pending.set(id, { restore: onReturn, settled });
  if (send({ op: "dequeue", id, thread, queueId, restore: !!onReturn })) return true;
  pending.delete(id);
  return false;
}

// Originals only travel back to the requesting composer, not queue broadcasts.
function returnedImages(value: unknown): PendingImage[] {
  if (!Array.isArray(value)) return [];
  return value.slice(0, MAX_IMAGES).flatMap(image => {
    if (!image || typeof image !== "object" || typeof image.name !== "string" ||
        typeof image.mime !== "string" || !/^image\/[a-z0-9.+-]+$/i.test(image.mime) ||
        typeof image.data !== "string" || image.data.length > Math.ceil(MAX_IMAGE_BYTES / 3) * 4 ||
        !/^[A-Za-z0-9+/]*={0,2}$/.test(image.data)) return [];
    return [{ name: image.name, mime: image.mime, data: image.data, preview: `data:${image.mime};base64,${image.data}` }];
  });
}

export function acceptQueueRemoval(pending: QueueReturns, message: Record<string, unknown>) {
  const id = typeof message.id === "string" ? message.id : "";
  const callback = pending.get(id);
  pending.delete(id);
  if (!callback) return;
  try {
    if (message.removed === true && typeof message.text === "string") callback.restore(message.text, returnedImages(message.images));
  } finally { callback.settled?.(); }
}


export function requestQueueMutation(pending: QueueReturns, send: (payload: Record<string, unknown>) => boolean,
  payload: Record<string, unknown>, success: () => void, settled: () => void): boolean {
  const id = `queue_mutation_${++sequence}`;
  pending.set(id, { restore: success, settled });
  if (send({ ...payload, id })) return true;
  pending.delete(id);
  settled();
  return false;
}
