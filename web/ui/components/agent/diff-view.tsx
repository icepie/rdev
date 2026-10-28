/* PierreFileDiff: thin wrapper over the @pierre/diffs FileDiff renderer
 * (shiki highlighting, unified/split, hunk headers, line numbers). Renders
 * on the main thread with stock shiki github themes. */
import { useEffect, useMemo, useState, type CSSProperties } from "react";
import { parsePatchFiles, type FileDiffMetadata } from "@pierre/diffs";
import { FileDiff } from "@pierre/diffs/react";
import type { FileDiffOptions } from "@pierre/diffs";
import { useTheme } from "../../lib/theme";

export type DiffView = "unified" | "split";
export type DiffOverflow = "scroll" | "wrap";

/* bb DIFF_VIEW_STYLE. */
export const DIFF_VIEW_STYLE = {
  "--diffs-font-size": "12px",
  "--diffs-line-height": "18px",
} as CSSProperties;

/* bb defaultResolvedCodeTheme (packages/domain code-theme.ts): the stock
 * pierre theme pair, used for both source preview and diffs. */
export const DIFF_THEME = {
  dark: "pierre-dark",
  light: "pierre-light",
} as const;

/* Resolved dark/light (bb usePreferredTheme): the ThemeProvider choice
 * plus the system preference when set to auto. Shared by the diff deck
 * and the file preview code view. */
export function useThemeType(): "dark" | "light" {
  const { choice } = useTheme();
  const [systemLight, setSystemLight] = useState(
    () => window.matchMedia("(prefers-color-scheme: light)").matches,
  );
  useEffect(() => {
    if (choice !== "auto") return;
    const mq = window.matchMedia("(prefers-color-scheme: light)");
    const onChange = () => setSystemLight(mq.matches);
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, [choice]);
  if (choice === "auto") return systemLight ? "light" : "dark";
  return choice;
}

/* Renders the FIRST file of a unified patch (rdev fetches diffs per
 * path). Returns null when the patch parses to no file (caller shows its
 * own notice). */
export function PierreFileDiff({
  patch,
  view,
  overflow = "scroll",
}: {
  patch: string;
  view: DiffView;
  overflow?: DiffOverflow;
}) {
  const themeType = useThemeType();
  const fileDiff = useMemo<FileDiffMetadata | null>(() => {
    try {
      return parsePatchFiles(patch)[0]?.files[0] ?? null;
    } catch {
      return null;
    }
  }, [patch]);
  const options = useMemo<FileDiffOptions<undefined, undefined>>(
    () => ({
      diffStyle: view,
      overflow,
      disableFileHeader: true,
      themeType,
      theme: DIFF_THEME,
    }),
    [view, overflow, themeType],
  );
  if (fileDiff === null) return null;
  return (
    <div style={DIFF_VIEW_STYLE}>
      <FileDiff fileDiff={fileDiff} options={options} disableWorkerPool />
    </div>
  );
}
