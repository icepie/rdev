// @vitest-environment jsdom
import { afterEach, expect, it } from "vitest";
import { desktopOwnsKeyboard } from "../ui/lib/desktop-input-focus";

afterEach(() => document.body.replaceChildren());
it("routes global paste and IME only to the focused desktop, not another pane or composer", () => {
  const left = document.createElement("div"); left.tabIndex = 0;
  const right = document.createElement("div"); right.tabIndex = 0;
  const composer = document.createElement("textarea");
  document.body.append(left, right, composer);
  left.focus();
  expect(desktopOwnsKeyboard(left)).toBe(true);
  expect(desktopOwnsKeyboard(right)).toBe(false);
  composer.focus();
  expect(desktopOwnsKeyboard(left)).toBe(false);
  expect(desktopOwnsKeyboard(right)).toBe(false);
  right.focus();
  expect(desktopOwnsKeyboard(right)).toBe(true);
  right.setAttribute("inert", "");
  expect(desktopOwnsKeyboard(right)).toBe(false);
  right.remove();
  expect(desktopOwnsKeyboard(right)).toBe(false);
});
