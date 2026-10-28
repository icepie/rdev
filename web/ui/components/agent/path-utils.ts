/* Port of bb packages/domain/src/project-path.ts: pure path helpers for the
 * create-project path browser. Handles both POSIX and Windows device paths
 * (the device OS decides which form shows up; a browser never mixes them). */

/** True for "C:\...", "C:/..." or UNC "\\..." paths. */
export function isWindowsPath(p: string): boolean {
  return /^[A-Za-z]:[\\/]/.test(p) || p.startsWith("\\\\");
}

/** Separator preferred by the given path's form ("/" unless Windows). */
export function pathSeparator(p: string): string {
  return isWindowsPath(p) ? "\\" : "/";
}

/** bb normalizeProjectPathInput: trim, strip trailing separators (but keep
 * the root itself: "/" and "C:\" survive). Returns "" for empty input. */
export function normalizePathInput(raw: string): string {
  let p = raw.trim();
  if (!p) return "";
  const root = isWindowsPath(p) ? p.slice(0, 3) : "/";
  while (p.length > root.length && /[\\/]$/.test(p)) p = p.slice(0, -1);
  return p;
}

/** bb deriveProjectNameFromPath: last non-empty path segment ("" when the
 * path is a root or otherwise has no usable segment). */
export function deriveNameFromPath(raw: string): string {
  const p = normalizePathInput(raw);
  if (!p) return "";
  const segs = p.split(/[\\/]+/).filter(Boolean);
  const last = segs[segs.length - 1] ?? "";
  // Windows drive root ("C:") is not a project name.
  return /^[A-Za-z]:$/.test(last) ? "" : last;
}

export type ProjectPathError = "absolute" | "root" | "unc";

/** bb getProjectPathValidationMessage, as a code the caller translates:
 * must be absolute, must not be a filesystem root, UNC unsupported. */
export function validateProjectPath(raw: string): ProjectPathError | null {
  const p = normalizePathInput(raw);
  if (!p) return "absolute";
  if (p.startsWith("\\\\")) return "unc";
  const win = /^[A-Za-z]:[\\/]/.test(p);
  if (!win && !p.startsWith("/")) return "absolute";
  if (p === "/" || /^[A-Za-z]:[\\/]?$/.test(p)) return "root";
  return null;
}

/** joinHostPath: append one segment, honoring the base's separator. */
export function joinPath(base: string, name: string): string {
  const sep = pathSeparator(base);
  return (base.endsWith(sep) ? base : base + sep) + name;
}

export type Crumb = { label: string; path: string };

/** toBreadcrumb: split an absolute path into clickable crumbs. POSIX starts
 * with a "/" crumb; Windows starts with the drive ("C:\"). */
export function toBreadcrumb(raw: string): Crumb[] {
  const p = normalizePathInput(raw);
  if (!p) return [];
  if (isWindowsPath(p)) {
    const drive = p.slice(0, 2);
    const rest = p.slice(2).split(/[\\/]+/).filter(Boolean);
    const crumbs: Crumb[] = [{ label: drive + "\\", path: drive + "\\" }];
    let acc = drive;
    for (const seg of rest) {
      acc += "\\" + seg;
      crumbs.push({ label: seg, path: acc });
    }
    return crumbs;
  }
  const crumbs: Crumb[] = [{ label: "/", path: "/" }];
  let acc = "";
  for (const seg of p.split("/").filter(Boolean)) {
    acc += "/" + seg;
    crumbs.push({ label: seg, path: acc });
  }
  return crumbs;
}
