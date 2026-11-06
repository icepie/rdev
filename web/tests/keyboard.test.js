import { expect, it } from "vitest";
import { isImeComposing } from "../ui/lib/keyboard";

it("recognizes IME confirmation across browser event variants", () => {
  const enter = { key: "Enter", keyCode: 13, isComposing: false };
  expect(isImeComposing(enter)).toBe(false);
  expect(isImeComposing({ ...enter, isComposing: true })).toBe(true);
  expect(isImeComposing({ ...enter, keyCode: 229 })).toBe(true);
  expect(isImeComposing({ ...enter, key: "Process" })).toBe(true);
  expect(isImeComposing({ key: "Escape", keyCode: 27, isComposing: false })).toBe(false);
});
