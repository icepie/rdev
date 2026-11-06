/** IDs identify requests even when the server normalizes the resource key. */
export function takePendingResponse<T extends { id: string }>(
  pending: Map<string, T>,
  responseID: string,
  legacyKey: string,
): T | undefined {
  if (responseID) {
    for (const [key, request] of pending) {
      if (request.id !== responseID) continue;
      pending.delete(key);
      return request;
    }
    return undefined;
  }
  const request = pending.get(legacyKey);
  if (request) pending.delete(legacyKey);
  return request;
}
