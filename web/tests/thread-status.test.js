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
