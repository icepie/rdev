// @vitest-environment jsdom
import { afterEach, expect, it } from "vitest";
import { loadDesktopSettings } from "../ui/lib/desktop-model";

afterEach(() => localStorage.clear());

it("keeps absolute cursor behaviour unless the user opts into lock or page tracking", () => {
  const settings = loadDesktopSettings("rdev.device.", "rdev.gpu.");
  expect(settings.pointerLock).toBe(false);
  expect(settings.globalMouse).toBe(false);
});

it("honours persisted pointer capture choices", () => {
  localStorage.setItem("rdev.device.pointerLock", "true");
  localStorage.setItem("rdev.device.globalMouse", "true");
  const settings = loadDesktopSettings("rdev.device.", "rdev.gpu.");
  expect(settings.pointerLock).toBe(true);
  expect(settings.globalMouse).toBe(true);
});
