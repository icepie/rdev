import AnsiToHtml from "ansi-to-html";

const colors = Object.fromEntries(
  Array.from({ length: 16 }, (_, index) => [index, `var(--ansi-${index})`]),
);

/** Each output snapshot is independent. CSS variables follow live BB themes;
 * terminal text must be escaped before inserting the generated span markup. */
export function renderAnsiOutput(text: string): string {
  return new AnsiToHtml({
    fg: "var(--foreground)",
    bg: "transparent",
    colors,
    newline: true,
    escapeXML: true,
    stream: false,
  }).toHtml(text);
}
