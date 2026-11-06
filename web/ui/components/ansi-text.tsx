import { useMemo } from "react";
import { renderAnsiOutput } from "../lib/ansi-output";

export function AnsiText({ text }: { text: string }) {
  const html = useMemo(() => renderAnsiOutput(text), [text]);
  return <span dangerouslySetInnerHTML={{ __html: html }} />;
}
