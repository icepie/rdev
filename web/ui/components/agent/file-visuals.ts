/* Extension-based file icon resolution for right-panel file surfaces
 * (tab strip, new-tab search results, recently closed, preview header):
 * markdown in a reports/ directory → ChartColumn, markdown → File,
 * html → AppWindow, everything else → Code. */

function fileName(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1) || path;
}

function fileExtension(path: string): string {
  const name = fileName(path);
  const dotIndex = name.lastIndexOf(".");
  return dotIndex <= 0 ? "" : name.slice(dotIndex + 1).toLowerCase();
}

function hasPathDirectorySegment(path: string, segment: string): boolean {
  return path.toLowerCase().split("/").slice(0, -1).includes(segment);
}

export function resolveFileIconName(path: string): string {
  const extension = fileExtension(path);
  const inReports = hasPathDirectorySegment(path, "reports");
  const isMarkdown = extension === "md" || extension === "markdown";
  const isHtml = extension === "html" || extension === "htm";

  if (inReports && (isMarkdown || isHtml)) {
    return "ChartColumn";
  }
  if (isMarkdown) {
    return "File";
  }
  if (isHtml) {
    return "AppWindow";
  }
  return "Code";
}
