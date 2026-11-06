/** OMP's /jobs grammar; invalid commands never fall through to the model. */
export function parseJobsCommand(argument: string): { full: boolean; payload: Record<string, unknown> } | null {
  const parts = argument.trim().split(/\s+/).filter(Boolean);
  if (!parts.length || (parts.length === 1 && parts[0] === "full")) {
    return { full: parts[0] === "full", payload: { op: "get_jobs" } };
  }
  if (parts.length === 2 && parts[0] === "kill") {
    return { full: false, payload: { op: "cancel_job", id: parts[1] } };
  }
  return null;
}
