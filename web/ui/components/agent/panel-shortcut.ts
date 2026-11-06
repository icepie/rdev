import { isImeComposing } from "../../lib/keyboard";

/** Leave editing and component-owned keystrokes to the focused control. */
export function shouldTogglePanel(event: KeyboardEvent): boolean {
  if (event.defaultPrevented || event.repeat || isImeComposing(event) ||
      event.altKey || event.shiftKey || !(event.metaKey || event.ctrlKey) ||
      event.key.toLowerCase() !== "j") return false;
  const target = event.target;
  if (!(target instanceof Element)) return true;
  if (target.closest("input, textarea, select, [role='textbox']")) return false;
  const editable = target.closest("[contenteditable]");
  return !editable || editable.getAttribute("contenteditable")?.toLowerCase() === "false";
}
