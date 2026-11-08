import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { CommandOutputBody } from "../ui/components/agent/command-output";
import { Markdown, CodeBlock } from "../ui/components/markdown";
import { highlightMarkdownCode } from "../ui/lib/code-highlight";

it("renders an unlabeled single-line fence as a code block, preserving inline code", () => {
  const html = renderToStaticMarkup(
    createElement(Markdown, { text: "```\nconst x = 1;\n```\n\n`inline`" }),
  );
  expect(html).toContain("<pre");
  expect(html).toContain("code-highlight");
  expect(html).toContain("var(--sh-keyword)");
  expect(html).toContain(">inline</code>");
});
it("highlights tool JSON and BB language aliases without allowing HTML injection", () => {
  const html = renderToStaticMarkup(
    createElement(CodeBlock, { language: "json" }, '{"key":true}'),
  );
  expect(html).toContain("var(--sh-");
  for (const language of ["console", "hpp", "python", "go", "rust", "diff"]) {
    const output = highlightMarkdownCode({
      code: '<script>alert("x")</script>\n+ added',
      language,
    });
    expect(output).not.toContain("<script>");
    expect(output).toContain("&lt;");
  }
});

it("uses live BB ANSI variables and escapes remote command output", () => {
  const html = renderToStaticMarkup(
    createElement(CommandOutputBody, {
      tool: {
        args: JSON.stringify({ command: "echo hello" }),
        result:
          "exit code: 0\n<stdout>\n\x1b[31m<script>bad</script>\x1b[0m\n</stdout>\n<stderr>\n\n</stderr>",
        isError: false,
      },
      t: (key) => key,
    }),
  );
  expect(html).toContain("var(--ansi-1)");
  expect(html).toContain("&lt;script&gt;");
  expect(html).not.toContain("<script>");
  expect(html).toContain("code-highlight");
});

it("renders independent ANSI snapshots without leaking colors between jobs", async () => {
  const { renderAnsiOutput } = await import("../ui/lib/ansi-output");
  expect(renderAnsiOutput("\x1b[31mfirst task")).toContain("var(--ansi-1)");
  expect(renderAnsiOutput("\x1b[41mred background\x1b[0m")).toContain("color:var(--ansi-bg-fg-1)");
  const next = renderAnsiOutput('<img src=x onerror="alert(1)">plain task');
  expect(next).not.toContain("var(--ansi-1)");
  expect(next).not.toContain("<img");
  expect(next).toContain("&lt;img");
  expect(renderAnsiOutput("\x1b[94mblue\x1b[0m")).toContain("var(--ansi-12)");
});

it("renders oversized code as complete escaped text without a token span tree", () => {
  const code = 'const x = "<&>";\n'.repeat(9000);
  const html = highlightMarkdownCode({ code, language: "typescript" });
  expect(html).not.toContain("<span");
  expect(html).not.toContain("<&>");
  expect(html).toBe(
    code
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;"),
  );
  // Large inputs must not change normal small-code highlighting.
  expect(
    highlightMarkdownCode({ code: "const x = 1;", language: "typescript" }),
  ).toContain("var(--sh-keyword)");
});

it("pairs all BB palette backgrounds and background resets without altering literal output", async () => {
  const { renderAnsiOutput } = await import("../ui/lib/ansi-output");
  for (let index = 0; index < 16; index++) {
    const code = index < 8 ? 40 + index : 100 + index - 8;
    expect(renderAnsiOutput(`\x1b[${code}mstatus\x1b[0m`)).toContain(`background-color:var(--ansi-${index});color:var(--ansi-bg-fg-${index})`);
  }
  expect(renderAnsiOutput("\x1b[41mred\x1b[49mdefault")).toContain("background-color:var(--background);color:var(--foreground)");
  const literal = 'background-color:var(--ansi-1) <span style="background-color:var(--ansi-2)">';
  const escaped = renderAnsiOutput(literal);
  expect(escaped).toContain("background-color:var(--ansi-1)");
  expect(escaped).not.toContain("ansi-bg-fg");
  expect(escaped).not.toContain("<span");
});
