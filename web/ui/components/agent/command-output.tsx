import { useEffect, useMemo, useRef } from "react";
import { renderAnsiOutput } from "../../lib/ansi-output";
import { SyntaxCode } from "../syntax-code";
import { cn } from "../../lib/utils";
import type { ToolItem, Translator } from "./types";

const RESULT_CAP = 4000;

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
  // Transport output may contain CRLF (including Windows command results).
  const normalized = result.replace(/\r\n/g, "\n");
  let m: string[] | null =
    /^([^\n]*)\n<stdout>\n(?:([\s\S]*?)\n)?<\/stdout>\n<stderr>\n(?:([\s\S]*?)\n)?<\/stderr>\s*$/.exec(
      normalized,
    );
  if (!m) {
    // Older histories compacted the entire envelope to a head fragment.
    // Recover only this known format; arbitrary shell text stays untouched.
    const compacted = /^(exit code: [^\n]*)\n<stdout>\n([\s\S]*\n\.\.\.\[earlier output truncated\])$/.exec(normalized);
    if (!compacted) return null;
    const boundary = "\n</stdout>\n<stderr>\n";
    const split = compacted[2].indexOf(boundary);
    if (split >= 0) {
      m = [compacted[0], compacted[1], compacted[2].slice(0, split), compacted[2].slice(split + boundary.length)];
    } else {
      if (compacted[2].includes("\n</stdout>")) return null;
      m = [compacted[0], compacted[1], compacted[2], ""];
    }
  }
  const code = /^exit code: (-?\d+)$/.exec(m[1]);
  return {
    status: m[1],
    exitCode: code ? Number(code[1]) : null,
    stdout: m[2] ?? "",
    stderr: m[3] ?? "",
  };
}

export function commandOutputText(result: string): string {
  const parsed = parseShellResult(result);
  if (!parsed) return result;
  return [parsed.stdout, parsed.stderr, parsed.status].filter(Boolean).join("\n");
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
  const outputRef = useRef<HTMLDivElement>(null);
  const following = useRef(true);
  useEffect(() => {
    const node = outputRef.current;
    if (node && following.current) node.scrollTop = node.scrollHeight;
  }, [tool.result]);
  const trackScroll = () => {
    const node = outputRef.current;
    if (node) following.current = node.scrollHeight - node.clientHeight - node.scrollTop < 24;
  };
  const command = commandOf(tool);
  const parsed = useMemo(() => parseShellResult(tool.result), [tool.result]);
  const cap = (s: string) =>
    s.length > RESULT_CAP
      ? s.slice(0, RESULT_CAP) + "\n" + t("agent.truncated")
      : s;
  const stdoutHtml = useMemo(
    () =>
      parsed && parsed.stdout ? renderAnsiOutput(cap(parsed.stdout)) : null,
    [parsed, t],
  );
  const stderrHtml = useMemo(
    () =>
      parsed && parsed.stderr ? renderAnsiOutput(cap(parsed.stderr)) : null,
    [parsed, t],
  );

  /* Legacy / in-flight results without the envelope: raw ANSI as before. */
  if (!parsed) {
    let output = tool.result;
    if (output.length > RESULT_CAP)
      output = output.slice(0, RESULT_CAP) + "\n" + t("agent.truncated");
    const html = output ? renderAnsiOutput(output) : null;
    return (
      <div
        className={cn(
          "overflow-hidden rounded-lg border bg-card",
          tool.isError ? "border-destructive" : "border-border",
        )}
      >
        <div ref={outputRef} onScroll={trackScroll} className="max-h-72 overflow-auto px-4 py-3 font-mono text-xs leading-tight break-words whitespace-pre-wrap text-foreground select-text">
          {command && (
            <div className="text-muted-foreground">
              $ <SyntaxCode code={command} language="shell" />
            </div>
          )}
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
      <div ref={outputRef} onScroll={trackScroll} className="max-h-72 overflow-auto px-4 py-3 font-mono text-xs leading-tight break-words whitespace-pre-wrap text-foreground select-text">
        {command && (
          <div className="text-muted-foreground">
            $ <SyntaxCode code={command} language="shell" />
          </div>
        )}
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
