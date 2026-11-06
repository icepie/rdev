/** Stable UI task IDs, fresh wire IDs for every start/resume attempt. */
export function createTransferAttempts(makeId: () => string) {
  const current = new Map<string, string>();
  const owners = new Map<string, string>();
  return {
    current(task: string) { return current.get(task); },
    resolve(wire: string) { return owners.get(wire); },
    begin(task: string) {
      const previous = current.get(task);
      if (previous) owners.delete(previous);
      const wire = makeId();
      current.set(task, wire);
      owners.set(wire, task);
      return wire;
    },
    remove(task: string) {
      const wire = current.get(task);
      if (wire) owners.delete(wire);
      current.delete(task);
    },
  };
}
