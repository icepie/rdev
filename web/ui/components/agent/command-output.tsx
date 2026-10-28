import { useMemo } from "react";
import AnsiToHtml from "ansi-to-html";
import { cn } from "../../lib/utils";
import type { ToolItem, Translator } from "./types";

const RESULT_CAP = 4000;

/* bb TerminalOutputBlock: ANSI-colored command output inside the shared
 * mono card. escapeXML keeps raw output safe for dangerouslySetInnerHTML;
 * newline turns \n into <br> so the pre-wrap card keeps its rhythm. */
const ANSI_TO_HTML = new AnsiToHtml({
  fg: "var(--fg)",
  bg: "transparent",
  newline: true,
  escapeXML: true,
});

function commandOf(tool: ToolItem): string {
  try {
    const parsed: unknown = JSON.parse(tool.args);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      const cmd = (parsed as Record<string, unknown>).command;
      if (typeof cmd === "string") return cmd;
    }
  } catch {
    /* fall through */
  }
  return "";
}

/* The shell tool wraps its reply as
 *   <status line>\n<stdout>\n…\n</stdout>\n<stderr>\n…\n</stderr>
 * (internal/server/agent_tools.go runShell). Parse it back apart so the
 * markers never reach the screen; unparseable results render raw. */
interface ShellResult {
  status: string;
  exitCode: number | null;
  stdout: string;
  stderr: string;
}

function parseShellResult(result: string): ShellResult | null {
  const m = /^([^\n]*)\n<stdout>\n([\s\S]*?)\n<\/stdout>\n<stderr>\n([\s\S]*?)\n<\/stderr>\s*$/.exec(
    result,
  );
  if (!m) return null;
  const code = /^exit code: (-?\d+)$/.exec(m[1]);
  return {
    status: m[1],
    exitCode: code ? Number(code[1]) : null,
    stdout: m[2],
    stderr: m[3],
  };
}

/* Shell tool body (bb TerminalOutputBlock): `$ <command>` header, ANSI
 * stdout (muted), danger-tinted stderr block, `exit code N` footer. The
 * args JSON block is redundant — the $ line already states what ran. */
export function CommandOutputBody({
  tool,
  t,
}: {
  tool: ToolItem;
  t: Translator;
}) {
  const command = commandOf(tool);
  const parsed = parseShellResult(tool.result);
  const cap = (s: string) =>
    s.length > RESULT_CAP
      ? s.slice(0, RESULT_CAP) + "\n" + t("agent.truncated")
      : s;
  const stdoutHtml = useMemo(
    () =>
      parsed && parsed.stdout ? ANSI_TO_HTML.toHtml(cap(parsed.stdout)) : null,
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [parsed],
  );
  const stderrHtml = useMemo(
    () =>
      parsed && parsed.stderr ? ANSI_TO_HTML.toHtml(cap(parsed.stderr)) : null,
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [parsed],
  );

  /* Legacy / in-flight results without the envelope: raw ANSI as before. */
  if (!parsed) {
    let output = tool.result;
    if (output.length > RESULT_CAP)
      output = output.slice(0, RESULT_CAP) + "\n" + t("agent.truncated");
    const html = output ? ANSI_TO_HTML.toHtml(output) : null;
    return (
      <div
        className={cn(
          "overflow-hidden rounded-lg border bg-card",
          tool.isError ? "border-destructive" : "border-border",
        )}
      >
        <div className="max-h-72 overflow-auto px-4 py-3 font-mono text-xs leading-tight break-words whitespace-pre-wrap text-foreground opacity-70 select-text">
          {command && <div className="text-muted-foreground">$ {command}</div>}
          {html ? (
            <div dangerouslySetInnerHTML={{ __html: html }} />
          ) : (
            !command && "…"
          )}
        </div>
      </div>
    );
  }

  const failed =
    tool.isError || (parsed.exitCode !== null && parsed.exitCode !== 0);
  return (
    <div
      className={cn(
        "overflow-hidden rounded-lg border bg-card",
        failed ? "border-destructive" : "border-border",
      )}
    >
      <div className="max-h-72 overflow-auto px-4 py-3 font-mono text-xs leading-tight break-words whitespace-pre-wrap text-foreground opacity-70 select-text">
        {command && <div className="text-muted-foreground">$ {command}</div>}
        {stdoutHtml && (
          /* bb AnsiBlock: stdout scrolls horizontally (whitespace-pre). */
          <div
            className={cn(
              command && "mt-1.5",
              "whitespace-pre text-muted-foreground",
            )}
            dangerouslySetInnerHTML={{ __html: stdoutHtml }}
          />
        )}
        {stderrHtml && (
          /* bb EventCodeBlock tone="danger" for stderr. */
          <div
            className="mt-1.5 rounded-md border border-destructive/40 bg-destructive/10 px-2 py-1.5 text-destructive-text"
            dangerouslySetInnerHTML={{ __html: stderrHtml }}
          />
        )}
        {parsed.status && (
          /* bb TerminalOutputBlock footer: "exit code N" (no colon). */
          <div
            className={cn(
              "mt-1.5",
              failed ? "text-destructive-text" : "text-muted-foreground",
            )}
          >
            {parsed.exitCode !== null
              ? `exit code ${parsed.exitCode}`
              : parsed.status}
          </div>
        )}
      </div>
    </div>
  );
}
