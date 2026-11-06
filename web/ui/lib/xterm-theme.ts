import type { ITheme } from "@xterm/xterm";

/* Theme-aware xterm colors (CSS vars → rgb via a probe element, since
 * xterm's color parser does not understand oklch/color-mix). Shared by the
 * Terminal page and the agent right-panel terminal tabs. */

let colorProbe: HTMLSpanElement | null = null;
let colorContext: CanvasRenderingContext2D | null = null;

function resolveColor(value: string, fallback: string): string {
  if (!value) return fallback;
  if (!colorProbe) {
    colorProbe = document.createElement("span");
    colorProbe.style.display = "none";
    document.body.appendChild(colorProbe);
  }
  colorProbe.style.color = "";
  colorProbe.style.color = value;
  if (!colorProbe.style.color) return fallback;
  const computed = getComputedStyle(colorProbe).color;
  // CSSOM retains modern color spaces. Rasterize into an sRGB canvas so
  // xterm receives rgba even for oklch and color-mix theme variables.
  colorContext ??= document.createElement("canvas").getContext("2d", {
    willReadFrequently: true,
  });
  if (!colorContext) return fallback;
  colorContext.clearRect(0, 0, 1, 1);
  colorContext.fillStyle = computed;
  colorContext.fillRect(0, 0, 1, 1);
  const [red, green, blue, alpha] = colorContext.getImageData(0, 0, 1, 1).data;
  return `rgba(${red}, ${green}, ${blue}, ${alpha / 255})`;
}

function cssVar(name: string): string {
  return getComputedStyle(document.documentElement)
    .getPropertyValue(name)
    .trim();
}

export function readXtermTheme(): ITheme {
  const background = resolveColor(cssVar("--background"), "#0f1117");
  const foreground = resolveColor(cssVar("--foreground"), "#e4e4e7");
  const accent = resolveColor(cssVar("--timeline-accent"), "#22d3ee");
  const ansi = (i: number, fallback: string) =>
    resolveColor(cssVar(`--ansi-${i}`), fallback);
  return {
    background,
    foreground,
    cursor: accent,
    cursorAccent: background,
    selectionBackground: resolveColor(
      `color-mix(in srgb, ${accent} 25%, transparent)`,
      "rgba(34,211,238,0.25)",
    ),
    black: ansi(0, "#1a1d27"),
    red: ansi(1, "#f87171"),
    green: ansi(2, "#4ade80"),
    yellow: ansi(3, "#facc15"),
    blue: ansi(4, "#60a5fa"),
    magenta: ansi(5, "#c084fc"),
    cyan: ansi(6, "#22d3ee"),
    white: ansi(7, "#e4e4e7"),
    brightBlack: ansi(8, "#3f3f46"),
    brightRed: ansi(9, "#fca5a5"),
    brightGreen: ansi(10, "#86efac"),
    brightYellow: ansi(11, "#fde68a"),
    brightBlue: ansi(12, "#93c5fd"),
    brightMagenta: ansi(13, "#d8b4fe"),
    brightCyan: ansi(14, "#67e8f9"),
    brightWhite: ansi(15, "#f4f4f5"),
  };
}
