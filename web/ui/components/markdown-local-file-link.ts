/* bb-aligned local-file link parsing for markdown anchors.
 * Recognizes paths with optional line suffixes (`#L12`, `#L12-L20`,
 * `:12`, `:12-20`, `:12:5`) and file:// URLs; rejects web/mail/editor
 * schemes, protocol-relative URLs, and bare #anchors. */

export interface LocalFileLink {
  path: string;
  lineStart?: number;
  lineEnd?: number;
}

interface LocalFileHrefParts {
  lineStart: number | null;
  lineEnd: number | null;
  path: string;
}

interface LineRange {
  start: number;
  end: number;
}

const URI_SCHEME_PATTERN = /^[a-zA-Z][a-zA-Z0-9+.-]*:/u;
const WINDOWS_DRIVE_PATH_PATTERN = /^[A-Za-z]:[\\/]/u;
const FILE_URL_WINDOWS_DRIVE_PATHNAME_PATTERN = /^\/[A-Za-z]:\//u;

function safeDecodeURIComponent(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

function parsePositiveInteger(value: string): number | null {
  if (!/^[0-9]+$/u.test(value)) {
    return null;
  }
  const parsedValue = Number(value);
  return Number.isSafeInteger(parsedValue) && parsedValue > 0
    ? parsedValue
    : null;
}

function parseLineRange(
  startValue: string,
  endValue: string | undefined,
): LineRange | null {
  const start = parsePositiveInteger(startValue);
  if (start === null) {
    return null;
  }
  const end = endValue === undefined ? start : parsePositiveInteger(endValue);
  if (end === null || end < start) {
    return null;
  }
  return { start, end };
}

function partsWithRange(
  path: string,
  lineRange: LineRange | null,
): LocalFileHrefParts {
  return {
    lineStart: lineRange?.start ?? null,
    lineEnd: lineRange?.end ?? null,
    path,
  };
}

function parseLineSuffix(value: string): LocalFileHrefParts | null {
  const hashLineMatch = value.match(
    /#L([0-9]+)(?:C[0-9]+)?(?:-L?([0-9]+)(?:C[0-9]+)?)?$/u,
  );
  if (hashLineMatch) {
    const lineRange = parseLineRange(hashLineMatch[1] ?? "", hashLineMatch[2]);
    if (lineRange === null) {
      return null;
    }
    return partsWithRange(value.slice(0, hashLineMatch.index), lineRange);
  }

  const hashIndex = value.indexOf("#");
  if (hashIndex !== -1) {
    const fragment = value.slice(hashIndex + 1);
    if (
      fragment.length === 0 ||
      fragment.includes("/") ||
      fragment.includes("#")
    ) {
      return null;
    }
    return partsWithRange(value.slice(0, hashIndex), null);
  }

  const colonLineRangeMatch = value.match(/:([0-9]+)-([0-9]+)$/u);
  if (colonLineRangeMatch) {
    const lineRange = parseLineRange(
      colonLineRangeMatch[1] ?? "",
      colonLineRangeMatch[2],
    );
    if (lineRange === null) {
      return null;
    }
    return partsWithRange(value.slice(0, colonLineRangeMatch.index), lineRange);
  }

  const colonLineColumnMatch = value.match(/:([0-9]+):[0-9]+$/u);
  if (colonLineColumnMatch) {
    const lineRange = parseLineRange(colonLineColumnMatch[1] ?? "", undefined);
    if (lineRange === null) {
      return null;
    }
    return partsWithRange(
      value.slice(0, colonLineColumnMatch.index),
      lineRange,
    );
  }

  const colonLineMatch = value.match(/:([0-9]+)$/u);
  if (colonLineMatch) {
    const lineRange = parseLineRange(colonLineMatch[1] ?? "", undefined);
    if (lineRange === null) {
      return null;
    }
    return partsWithRange(value.slice(0, colonLineMatch.index), lineRange);
  }

  return partsWithRange(value, null);
}

function hasControlCharacter(value: string): boolean {
  for (const character of value) {
    const codePoint = character.codePointAt(0);
    if (codePoint !== undefined && codePoint < 0x20) {
      return true;
    }
  }
  return false;
}

/* Strip a file:// URL down to its path (+ #fragment for line parsing);
 * returns null for non-file URLs, hosted file URLs, and query strings. */
function stripFileUrl(href: string): string | null {
  let url: URL;
  try {
    url = new URL(href);
  } catch {
    return null;
  }
  if (url.protocol !== "file:" || url.host.length > 0 || url.search.length > 0) {
    return null;
  }
  const pathname = FILE_URL_WINDOWS_DRIVE_PATHNAME_PATTERN.test(url.pathname)
    ? url.pathname.slice(1)
    : url.pathname;
  return pathname + url.hash;
}

export function parseLocalFileHref(href: string): LocalFileLink | null {
  if (href.length === 0 || href.trim() !== href) {
    return null;
  }
  if (href.startsWith("#") || href.startsWith("//")) {
    return null;
  }

  let value = href;
  if (URI_SCHEME_PATTERN.test(href) && !WINDOWS_DRIVE_PATH_PATTERN.test(href)) {
    if (!/^file:/iu.test(href)) {
      return null;
    }
    const stripped = stripFileUrl(href);
    if (stripped === null) {
      return null;
    }
    value = stripped;
  }

  const parts = parseLineSuffix(safeDecodeURIComponent(value));
  if (
    parts === null ||
    parts.path.length === 0 ||
    parts.path === "/" ||
    parts.path.endsWith("/") ||
    (WINDOWS_DRIVE_PATH_PATTERN.test(parts.path) &&
      parts.path.endsWith("\\")) ||
    parts.path.includes("?") ||
    parts.path.includes("#") ||
    hasControlCharacter(parts.path)
  ) {
    return null;
  }

  if (parts.lineStart === null) {
    return { path: parts.path };
  }
  return {
    path: parts.path,
    lineStart: parts.lineStart,
    lineEnd: parts.lineEnd ?? parts.lineStart,
  };
}
