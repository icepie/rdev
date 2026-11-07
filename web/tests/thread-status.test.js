import { expect, it } from "vitest";
import { parseThreads } from "../ui/components/agent/protocol";

it("restores supported waiting states only for running threads", () => {
  const rows = parseThreads([
    { id: "a", running: true, waiting: "approval" },
    { id: "b", running: true, waiting: "answer" },
    { id: "c", running: false, waiting: "approval" },
    { id: "d", running: true, waiting: "unexpected" },
    { id: "e", running: true },
  ]);
  expect(rows.map(row => row.waiting)).toEqual(["approval", "answer", undefined, undefined, undefined]);
});

it("accepts only nonnegative integral background task counts", () => {
  const values = [2, 0, -1, 1.5, "3", Infinity, undefined];
  expect(parseThreads(values.map((backgroundJobs, i) => ({ id: String(i), backgroundJobs })))
    .map(row => row.backgroundJobs)).toEqual([2, 0, 0, 0, 0, 0, 0]);
});
