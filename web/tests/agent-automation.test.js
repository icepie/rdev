import { expect, it } from "vitest";
import { parseAutomation } from "../ui/components/agent/automation";
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
