/** Stable UI task IDs, fresh wire IDs for every start/resume attempt. */
export type TransferAttemptPhase = "preparing" | "receiving" | "verifying";

export const TRANSFER_PREPARATION_TIMEOUT_MS = 5 * 60_000;
export const TRANSFER_RECEIVE_STALL_MS = 12_000;
export const TRANSFER_VERIFY_TIMEOUT_MS = 5 * 60_000;

interface AttemptTiming {
  phase: TransferAttemptPhase;
  deadline: number;
}

function deadlineFor(phase: TransferAttemptPhase, now: number) {
  switch (phase) {
    case "preparing": return now + TRANSFER_PREPARATION_TIMEOUT_MS;
    case "receiving": return now + TRANSFER_RECEIVE_STALL_MS;
    case "verifying": return now + TRANSFER_VERIFY_TIMEOUT_MS;
  }
}

/** Attempt identity and phase deadlines stay outside React and persisted tasks. */
export function createTransferAttempts(makeId: () => string) {
  const current = new Map<string, string>();
  const owners = new Map<string, string>();
  const timings = new Map<string, AttemptTiming>();
  return {
    current(task: string) { return current.get(task); },
    resolve(wire: string) { return owners.get(wire); },
    begin(task: string, now = Date.now()) {
      const previous = current.get(task);
      if (previous) {
        owners.delete(previous);
        timings.delete(previous);
      }
      const wire = makeId();
      current.set(task, wire);
      owners.set(wire, task);
      timings.set(wire, { phase: "preparing", deadline: deadlineFor("preparing", now) });
      return wire;
    },
    phase(task: string, wire: string, phase: TransferAttemptPhase, now = Date.now()) {
      if (current.get(task) !== wire) return false;
      timings.set(wire, { phase, deadline: deadlineFor(phase, now) });
      return true;
    },
    progress(task: string, wire: string, now = Date.now()) {
      const timing = timings.get(wire);
      if (current.get(task) !== wire || !timing || timing.phase !== "receiving") return false;
      timing.deadline = deadlineFor("receiving", now);
      return true;
    },
    expired(task: string, now = Date.now()) {
      const wire = current.get(task);
      const timing = wire ? timings.get(wire) : undefined;
      return !!timing && now >= timing.deadline;
    },
    remove(task: string) {
      const wire = current.get(task);
      if (wire) {
        owners.delete(wire);
        timings.delete(wire);
      }
      current.delete(task);
    },
  };
}
