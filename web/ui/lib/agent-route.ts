import { SERVER_AGENT_ID } from "../components/agent/protocol";

export function readAgentRoute(threadId: string | undefined, search: URLSearchParams) {
  return {
    device: search.get("device") || SERVER_AGENT_ID,
    thread: threadId || search.get("thread") || "",
  };
}

export function agentConversationURL(device: string, thread = "", search = new URLSearchParams()) {
  const params = new URLSearchParams(search);
  params.delete("thread");
  params.set("device", device || SERVER_AGENT_ID);
  return `/agent${thread ? `/${encodeURIComponent(thread)}` : ""}?${params}`;
}

export function agentRouteState(route: { device: string; thread: string }, device: string, thread: string, authenticated: boolean, threads: readonly { id: string }[]) {
  if (route.device !== device || !authenticated) return "waiting";
  if (!route.thread) return "default";
  if (!threads.some(item => item.id === route.thread)) return "missing";
  return route.thread === thread ? "selected" : "switch";
}
