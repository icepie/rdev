import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it, vi } from "vitest";
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
vi.mock("../ui/components/agent/diff-view", () => ({ PierreFileDiff: () => null }));
vi.mock("../ui/components/disclosure-body", () => ({ DisclosureBody: ({ children }) => children }));
import { ToolRow } from "../ui/components/agent/tool-row";

it.each(["\n", "\r\n"])("renders remote shell streams using terminal presentation (%j)", (newline) => {
  const html = renderToStaticMarkup(createElement(ToolRow, {
    tool: { id: "remote-shell", name: "device_tool", done: true,
      args: JSON.stringify({ device: "mac-mini", tool: "shell", arguments: { command: "check-firewall" } }),
      result: ["exit code: 0", "<stdout>", "=== APP FIREWALL ===", "Firewall is disabled. (State = 0)", "", "</stdout>", "<stderr>", "", "</stderr>"].join(newline),
    }, t: (key) => key,
  }));
  expect(html).toContain("mac-mini");
  expect(html).toContain("check-firewall");
  expect(html).toContain("Firewall is disabled. (State = 0)");
  expect(html).toContain("exit code 0");
  expect(html).not.toContain("&lt;stdout&gt;");
  expect(html).not.toContain("&lt;stderr&gt;");
});

it("retains malformed remote calls as generic tool details", () => {
  const html = renderToStaticMarkup(createElement(ToolRow, {
    tool: { id: "bad", name: "device_tool", done: true, args: '{"tool":"shell"}', result: "arguments are required", isError: true },
    t: (key) => key,
  }));
  expect(html).toContain("arguments are required");
});
