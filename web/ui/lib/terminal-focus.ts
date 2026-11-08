// A delayed ready/auth reply must not steal focus from another pane or editor.
// Focus inside this terminal's auth/reconnect controls can return to its shell.
export function terminalMayTakeFocus(root: HTMLElement | null): boolean {
  if (!root?.isConnected || root.ownerDocument.hidden || root.closest("[inert]")) return false;
  const active = root.ownerDocument.activeElement;
  return !active || active === root.ownerDocument.body || root.contains(active);
}
