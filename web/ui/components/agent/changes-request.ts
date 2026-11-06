export interface ChangesRequest {
  epoch: number;
  id: string;
}

/** Consume explicit replies once; allow automatic updates after completion. */
export function consumeChangesReply(
  pending: { current: ChangesRequest | null },
  epoch: number,
  responseID: string,
): boolean {
  const request = pending.current;
  if (!responseID) return request === null;
  if (!request || request.epoch !== epoch || request.id !== responseID) return false;
  pending.current = null;
  return true;
}
