import { expect, it } from "vitest";
import { consumeChangesReply } from "../ui/components/agent/changes-request";

it("resumes automatic Git updates after an explicit refresh completes", () => {
  const pending = { current: { epoch: 1, id: "refresh" } };
  expect(consumeChangesReply(pending, 1, "")).toBe(false);
  expect(consumeChangesReply(pending, 1, "refresh")).toBe(true);
  expect(consumeChangesReply(pending, 1, "")).toBe(true);
  expect(consumeChangesReply(pending, 1, "refresh")).toBe(false);
});

it("does not let a stale response consume the current refresh", () => {
  const pending = { current: { epoch: 2, id: "new" } };
  expect(consumeChangesReply(pending, 2, "old")).toBe(false);
  expect(pending.current.id).toBe("new");
  expect(consumeChangesReply(pending, 3, "new")).toBe(false);
  expect(pending.current.id).toBe("new");
  expect(consumeChangesReply(pending, 2, "new")).toBe(true);
});
