import { apiJSON } from "../../lib/api";

export async function request<T>(action: string, extra: Record<string, unknown> = {}, signal?: AbortSignal): Promise<T> {
  const response = await apiJSON<{ data?: T; error?: string }>("/api/schedules", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action, ...extra }), signal });
  if (response.error) throw new Error(response.error);
  return response.data as T;
}

export async function loadLogTail(runId: string, signal: AbortSignal): Promise<{ output: string; nextOffset: number }> {
  let offset = 0;
  let output = "";
  for (let page = 0; page < 8 && output.length < 262144; page++) {
    const logs = await request<{ output: string; nextOffset: number; reset?: boolean }>("logs", { id: runId, offset, limit: 65536 }, signal);
    if (logs.reset) output = "";
    output += logs.output || "";
    if (!logs.output || logs.nextOffset <= offset) return { output: output.slice(-262144), nextOffset: logs.nextOffset };
    offset = logs.nextOffset;
  }
  return { output: output.slice(-262144), nextOffset: offset };
}
