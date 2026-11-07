import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { AgentConnectionError } from "../ui/components/agent/connection-error";

it("explains missing configuration without concealing unrelated errors", () => {
  const render = message => renderToStaticMarkup(createElement(AgentConnectionError, { message, t: key => key }));
  const setup = render("agent not configured: set --agent-api-key / RDEV_AGENT_API_KEY");
  expect(setup).toContain("agent.notConfiguredHint");
  expect(setup).toContain("agent.reconnectAfterSetup");
  expect(setup).not.toContain("agent not configured:");
  expect(render("Device is offline")).toContain("Device is offline");
  expect(render("Device is offline")).not.toContain("agent.notConfiguredHint");
});
