import AnsiToHtml from "ansi-to-html";

const colors = Object.fromEntries(
  Array.from({ length: 16 }, (_, index) => [index, `var(--ansi-${index})`]),
);

/** Each output snapshot is independent. CSS variables follow live BB themes;
 * terminal text must be escaped before inserting the generated span markup. */
export function renderAnsiOutput(text: string): string {
  const html = new AnsiToHtml({
    fg: "var(--muted-foreground)",
    bg: "var(--background)",
    colors,
    newline: true,
    escapeXML: true,
    stream: false,
  }).toHtml(text);
  // BB TerminalOutputBlock pairs each palette background with the theme's
  // contrast foreground. Rewrite generated styles only, never source text.
  return html.replace(/<span style="([^"]*)">/g, (_, styles: string) => {
    const contrasted = styles.replace(
      /background-color:var\(--(ansi-(\d+)|background)\)/g,
      (style, color: string, index: string | undefined) =>
        `${style};color:var(--${color === "background" ? "foreground" : `ansi-bg-fg-${index}`})`,
    );
    return `<span style="${contrasted}">`;
  });
}
