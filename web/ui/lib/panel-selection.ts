// Scope text selections through the open Shadow DOM used by Pierre's source
// view. Both endpoints must belong to this panel: a split-pane drag must never
// attach another conversation's text.
export function isWithinPanel(panel: HTMLElement, node: Node): boolean {
  let current: Node | null = node;
  while (current) {
    if (current === panel) return true;
    current = current instanceof ShadowRoot ? current.host : current.parentNode;
  }
  return false;
}

export interface PanelSelection { text: string; rect: DOMRect }

export function readPanelSelection(panel: HTMLElement): PanelSelection | null {
  const bounds = panel.getBoundingClientRect();
  if (!panel.isConnected || bounds.width <= 0 || bounds.height <= 0) return null;
  const selection = panel.ownerDocument.getSelection();
  if (!selection || selection.isCollapsed || selection.rangeCount !== 1) return null;
  let range: Range;
  if (typeof selection.getComposedRanges === "function") {
    const shadowRoots = Array.from(panel.querySelectorAll("diffs-container"))
      .flatMap(host => host.shadowRoot ? [host.shadowRoot] : []);
    const ranges = selection.getComposedRanges({ shadowRoots });
    if (ranges.length !== 1) return null;
    const selected = ranges[0];
    if (!isWithinPanel(panel, selected.startContainer) || !isWithinPanel(panel, selected.endContainer)
      || selected.startContainer.getRootNode() !== selected.endContainer.getRootNode()) return null;
    range = panel.ownerDocument.createRange();
    range.setStart(selected.startContainer, selected.startOffset);
    range.setEnd(selected.endContainer, selected.endOffset);
  } else {
    range = selection.getRangeAt(0);
    if (!isWithinPanel(panel, range.startContainer) || !isWithinPanel(panel, range.endContainer)) return null;
  }
  const text = selection.toString().replace(/\n+$/, "");
  if (!text.trim() || range.collapsed) return null;
  const rect = range.getClientRects()[0] ?? range.getBoundingClientRect();
  return { text, rect };
}
