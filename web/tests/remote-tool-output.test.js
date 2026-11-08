import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it, vi } from "vitest";
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
vi.mock("../ui/components/agent/diff-view", () => ({ PierreFileDiff: () => null }));
vi.mock("../ui/components/disclosure-body", () => ({ DisclosureBody: ({ children }) => children }));
import { commandOutputText } from "../ui/components/agent/command-output";
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

it.each(["", "\n</stdout>\n<stderr>\nretained error line"])("renders legacy compacted shell history without leaking stream tags (%s)", (stderr) => {
  const html = renderToStaticMarkup(createElement(ToolRow, {
    tool: { id: "old-output", name: "shell", done: true, args: '{"command":"large-log"}',
      result: "exit code: 0\n<stdout>\nretained log line" + stderr + "\n...[earlier output truncated]" },
    t: key => key,
  }));
  expect(html).toContain("retained log line");
  expect(html).toContain("earlier output truncated");
  expect(html).toContain("exit code 0");
  expect(html).not.toContain("&lt;stdout&gt;");
  expect(html).not.toContain("&lt;stderr&gt;");
});

it.each([
  ["Build passed", ""], ["", "Build failed"], ["", ""],
])("renders and copies envelopes with compact empty streams (%j, %j)", (stdout, stderr) => {
  const result = `exit code: 0\n<stdout>\n${stdout ? stdout + "\n" : ""}</stdout>\n<stderr>\n${stderr ? stderr + "\n" : ""}</stderr>`;
  const html = renderToStaticMarkup(createElement(ToolRow, {
    tool: {id:"empty-stream", name:"shell", done:true, args:"{}", result}, t:key=>key,
  }));
  expect(html).toContain("exit code 0");
  expect(html).not.toContain("&lt;stdout&gt;");
  expect(html).not.toContain("&lt;stderr&gt;");
  expect(commandOutputText(result)).toBe([stdout,stderr,"exit code: 0"].filter(Boolean).join("\n"));
});

it.each(["context canceled", "error: context deadline exceeded"])("renders interrupted partial streams and keeps the cause when copying (%s)", cause => {
  const prefix = `${cause}; partial output:`;
  const result = `${prefix}\nexit code: unknown (interrupted)\n<stdout>\nretained output\n</stdout>\n<stderr>\nretained error\n</stderr>`;
  const html = renderToStaticMarkup(createElement(ToolRow, {
    tool: {id:"interrupted",name:"shell",done:true,args:'{"command":"slow-command"}',result,isError:true},t:key=>key,
  }));
  expect(html).toContain(prefix);
  expect(html).toContain("retained output");
  expect(html).toContain("retained error");
  expect(html).toContain("exit code: unknown (interrupted)");
  expect(html).not.toContain("&lt;stdout&gt;");
  expect(html).not.toContain("&lt;stderr&gt;");
  expect(commandOutputText(result)).toBe(`${prefix}\nretained output\nretained error\nexit code: unknown (interrupted)`);
});

it("does not strip arbitrary output that resembles an interrupted envelope", () => {
  const output = "example partial output:\nexit code: unknown (interrupted)\n<stdout>\nraw text\n</stdout>\n<stderr>\n\n</stderr>";
  expect(commandOutputText(output)).toBe(output);
});
