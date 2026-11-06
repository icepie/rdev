import { nordThemeCss } from "./nord";
import { draculaThemeCss } from "./dracula";
import { solarizedThemeCss } from "./solarized";
import { gruvboxThemeCss } from "./gruvbox";
import { catppuccinThemeCss } from "./catppuccin";

export const themePresets = {
  default: { label: "BB", css: "" },
  nord: { label: "Nord", css: nordThemeCss },
  dracula: { label: "Dracula", css: draculaThemeCss },
  solarized: { label: "Solarized", css: solarizedThemeCss },
  gruvbox: { label: "Gruvbox", css: gruvboxThemeCss },
  catppuccin: { label: "Catppuccin", css: catppuccinThemeCss },
} as const;
export type ThemePreset = keyof typeof themePresets;
export function parseThemePreset(value: string | null): ThemePreset {
  return value && Object.hasOwn(themePresets, value)
    ? (value as ThemePreset)
    : "default";
}
