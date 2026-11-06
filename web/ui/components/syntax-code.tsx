import { useMemo } from "react";
import { highlightMarkdownCode } from "../lib/code-highlight";
import { cn } from "../lib/utils";

export function SyntaxCode({
  code,
  language = null,
  className,
}: {
  code: string;
  language?: string | null;
  className?: string;
}) {
  const html = useMemo(() => {
    try {
      return highlightMarkdownCode({
        code,
        language: language?.toLowerCase() ?? null,
      });
    } catch {
      return null;
    }
  }, [code, language]);
  return html === null ? (
    <code className={cn("font-mono", className)}>{code}</code>
  ) : (
    <code
      className={cn("code-highlight font-mono", className)}
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}
