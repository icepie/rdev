/** IME confirmation can report keyCode 229 even after isComposing turns false. */
export function isImeComposing(event: Pick<KeyboardEvent, "isComposing" | "keyCode" | "key">): boolean {
  return event.isComposing || event.keyCode === 229 || event.key === "Process";
}
