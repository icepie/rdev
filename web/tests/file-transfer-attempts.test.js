import { expect, it } from "vitest";
import { createTransferAttempts } from "../ui/lib/file-transfer-attempts";

it("rejects delayed cancel, completion and binary replies after a task resumes", () => {
  let sequence = 0;
  const attempts = createTransferAttempts(() => `wire-${++sequence}`);
  const first = attempts.begin("upload");
  const other = attempts.begin("download");
  expect(attempts.resolve(first)).toBe("upload");
  const resumed = attempts.begin("upload");
  expect(resumed).not.toBe(first);
  // Every incoming text or binary frame is resolved before touching a task.
  expect(attempts.resolve(first)).toBeUndefined();
  expect(attempts.resolve(resumed)).toBe("upload");
  expect(attempts.current("upload")).toBe(resumed);
  expect(attempts.resolve(other)).toBe("download");
  attempts.remove("upload");
  expect(attempts.resolve(resumed)).toBeUndefined();
  expect(attempts.current("upload")).toBeUndefined();
  expect(attempts.resolve(other)).toBe("download");
});

it("does not accept persisted UI task IDs or unknown protocol IDs", () => {
  const attempts = createTransferAttempts(() => "fresh-wire");
  expect(attempts.resolve("persisted-task")).toBeUndefined();
  attempts.begin("persisted-task");
  expect(attempts.resolve("persisted-task")).toBeUndefined();
  expect(attempts.resolve("unknown")).toBeUndefined();
  expect(attempts.resolve("fresh-wire")).toBe("persisted-task");
});

it("gives preparation time its own deadline and resets deadlines per attempt", () => {
  let sequence = 0;
  const attempts = createTransferAttempts(() => `wire-${++sequence}`);
  const first = attempts.begin("download", 0);
  expect(attempts.expired("download", 15_000)).toBe(false);
  attempts.phase("download", first, "receiving", 15_000);
  expect(attempts.expired("download", 26_999)).toBe(false);
  expect(attempts.expired("download", 27_000)).toBe(true);

  const retry = attempts.begin("download", 27_000);
  expect(attempts.resolve(first)).toBeUndefined();
  expect(attempts.progress("download", first, 30_000)).toBe(false);
  expect(attempts.expired("download", 30_000)).toBe(false);
  attempts.phase("download", retry, "receiving", 30_000);
  expect(attempts.progress("download", retry, 40_000)).toBe(true);
  expect(attempts.expired("download", 51_999)).toBe(false);
  expect(attempts.expired("download", 52_000)).toBe(true);
});

it("separates verification deadlines from receive stalls", () => {
  const attempts = createTransferAttempts(() => "wire");
  attempts.begin("download", 1_000);
  attempts.phase("download", "wire", "verifying", 20_000);
  expect(attempts.expired("download", 20_001)).toBe(false);
  expect(attempts.expired("download", 319_999)).toBe(false);
  expect(attempts.expired("download", 320_000)).toBe(true);
});
