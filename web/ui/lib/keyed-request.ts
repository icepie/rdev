/** Share one in-flight request per resource, releasing it on either outcome. */
export function createKeyedRequest<T>() {
  const pending = new Map<string, Promise<T>>();
  return (key: string, request: () => Promise<T>): Promise<T> => {
    const current = pending.get(key);
    if (current) return current;
    const result = Promise.resolve().then(request).finally(() => {
      if (pending.get(key) === result) pending.delete(key);
    });
    pending.set(key, result);
    return result;
  };
}
