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
