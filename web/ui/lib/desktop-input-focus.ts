/** Global IME/paste handlers must belong to exactly one focused desktop.
 * Hovering a stream or keeping it mounted in a hidden tab grants no input. */
export function desktopOwnsKeyboard(overlay: HTMLElement | null): boolean {
  return !!overlay && overlay.isConnected &&
    overlay.ownerDocument.visibilityState !== "hidden" &&
    overlay.ownerDocument.activeElement === overlay &&
    !overlay.closest("[inert]");
}
