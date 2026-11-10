import { expect, it } from "vitest";
import { createAutomationRevealTracker, parseAutomation } from "../ui/components/agent/automation";
import { toolImageDataUrl } from "../ui/components/agent/transcript-rows";

it("accepts only scoped browser capabilities and device views", () => {
  const value = { kind: "browser", device: "exit-device", target: "page-1", base: `/api/browser/view/${"a".repeat(64)}`, action: "snapshot", paused: false };
  expect(parseAutomation(value)).toMatchObject(value);
  expect(parseAutomation({ ...value, base: "https://other.test" })).toBeNull();
  expect(parseAutomation({ ...value, target: "../../browser/root" })).toBeNull();
  expect(parseAutomation({ kind: "desktop", device: "screen-device", paused: true, source: "screen:1" })).toMatchObject({ kind: "desktop", source: "screen:1", paused: true });
  expect(parseAutomation({ kind: "desktop", device: "" })).toBeNull();
});
it.each(["browser", "computer"])("renders %s image results as previews", name => {
  expect(toolImageDataUrl({ name, result: "[image preview]\ndata:image/jpeg;base64,YWJj" })).toBe("data:image/jpeg;base64,YWJj");
  expect(toolImageDataUrl({ name, result: "[image preview]\ndata:image/jpeg;base64,YWJj\n[truncated]" })).toBeNull();
});


it("reveals automation once per conversation and respects hidden panels on later calls", () => {
  const reveal = createAutomationRevealTracker();
  const view = { kind: "desktop", device: "phone", action: "screenshot", paused: false };
  expect(reveal("phone:one", view, true)).toBe(true);
  // Hiding the panel must not be undone by another screenshot, input, or browser call.
  expect(reveal("phone:one", { ...view, action: "click" }, true)).toBe(false);
  expect(reveal("phone:one", { ...view, kind: "browser" }, true)).toBe(false);
  expect(reveal("phone:two", view, true)).toBe(true);
  expect(reveal("phone:one", view, true)).toBe(false);
});

it("does not reveal panels from replayed automation state or reconnects", () => {
  const reveal = createAutomationRevealTracker();
  const view = { kind: "desktop", device: "phone", action: "screenshot", paused: false };
  expect(reveal("phone:one", view, false)).toBe(false);
  expect(reveal("phone:one", view, true)).toBe(false);
  expect(reveal("phone:two", null, false)).toBe(false);
  expect(reveal("phone:two", view, true)).toBe(true);
});
