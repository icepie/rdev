import { describe, expect, it } from "vitest";
import { agentConversationURL, agentRouteState, readAgentRoute } from "../ui/lib/agent-route";

describe("Agent conversation URLs", () => {
  it("keeps device identity explicit and encodes both identifiers", () => {
    const url = new URL(agentConversationURL("device / +", "thread / +"), "https://rdev.test");
    expect(url.pathname).toBe("/agent/thread%20%2F%20%2B");
    expect(url.searchParams.get("device")).toBe("device / +");
    expect(readAgentRoute(undefined, new URLSearchParams())).toEqual({ device: "@server", thread: "" });
    expect(readAgentRoute("server-thread", new URLSearchParams())).toEqual({ device: "@server", thread: "server-thread" });
  });

  it("accepts scheduler query links and preserves the turn while canonicalizing", () => {
    const search = new URLSearchParams("device=laptop&thread=legacy&turn=run-123");
    expect(readAgentRoute(undefined, search)).toEqual({ device: "laptop", thread: "legacy" });
    expect(readAgentRoute("path-thread", search).thread).toBe("path-thread");
    const url = new URL(agentConversationURL("laptop", "legacy", search), "https://rdev.test");
    expect(url.pathname).toBe("/agent/legacy");
    expect(url.searchParams.get("turn")).toBe("run-123");
    expect(url.searchParams.has("thread")).toBe(false);
    expect(search.get("thread")).toBe("legacy");
  });

  it("never selects a fallback conversation for unknown or offline targets", () => {
    const route = { device: "offline", thread: "missing" };
    const threads = [{ id: "default" }];
    expect(agentRouteState(route, "@server", "default", true, threads)).toBe("waiting");
    expect(agentRouteState(route, "offline", "default", false, threads)).toBe("waiting");
    expect(agentRouteState(route, "offline", "default", true, threads)).toBe("missing");
    expect(agentRouteState({ device: "offline", thread: "" }, "offline", "default", true, threads)).toBe("default");
  });

  it("waits for the requested snapshot even when an older switch finishes late", () => {
    const route = { device: "laptop", thread: "a" };
    const threads = [{ id: "a" }, { id: "b" }];
    expect(agentRouteState(route, "laptop", "b", true, threads)).toBe("switch");
    expect(agentRouteState(route, "laptop", "a", true, threads)).toBe("selected");
    expect(agentRouteState(route, "laptop", "a", true, [{ id: "b" }])).toBe("missing");
  });

});
