import { expect, it } from "vitest";
import { takePendingResponse } from "../ui/components/agent/pending-response";
import { THREAD_SCOPED_OPS, TRANSCRIPT_OPS } from "../ui/components/agent/protocol";

it("correlates normalized searches independently of their query text", async () => {
  const first = Promise.withResolvers();
  const second = Promise.withResolvers();
  const pending = new Map([
    [" main ", { id: "first", resolve: first.resolve }],
    ["main", { id: "second", resolve: second.resolve }],
  ]);
  takePendingResponse(pending, "second", "main").resolve(["new.ts"]);
  takePendingResponse(pending, "first", "main").resolve(["old.ts"]);
  expect(await second.promise).toEqual(["new.ts"]);
  expect(await first.promise).toEqual(["old.ts"]);
  expect(pending.size).toBe(0);
  // Search results belong to the selected thread's panel, never its transcript.
  expect(THREAD_SCOPED_OPS.file_search).toBe(true);
  expect(TRANSCRIPT_OPS.file_search).toBeUndefined();
});

it("resolves normalized and empty paths by ID, removing the original key", () => {
  const request = { id: "directory-1" };
  for (const path of ["", " /repo "]) {
    const pending = new Map([[path, request]]);
    expect(takePendingResponse(pending, request.id, "/repo")).toBe(request);
    expect(pending.size).toBe(0);
    expect(takePendingResponse(pending, request.id, "/repo")).toBeUndefined();
  }
});

it("rejects stale IDs without consuming a newer same-path request", () => {
  const request = { id: "new" };
  const pending = new Map([["/repo", request]]);
  expect(takePendingResponse(pending, "old", "/repo")).toBeUndefined();
  expect(pending.get("/repo")).toBe(request);
  expect(takePendingResponse(pending, "new", "/repo")).toBe(request);
});

it("supports legacy responses, including empty-path errors", () => {
  const request = { id: "directory-1" };
  const pending = new Map([["", request]]);
  expect(takePendingResponse(pending, "", "")).toBe(request);
  expect(pending.size).toBe(0);
});

it("keeps concurrent rename replies independent across devices and ignores superseded replies", () => {
  const key = (device, thread) => JSON.stringify([device, thread]);
  const first = { id: "rename-1" }, second = { id: "rename-2" }, latest = { id: "rename-3" };
  const pending = new Map([[key("one", "same-thread"), first], [key("two", "same-thread"), second]]);
  pending.set(key("one", "same-thread"), latest);
  expect(takePendingResponse(pending, first.id, key("one", "same-thread"))).toBeUndefined();
  expect(takePendingResponse(pending, "delete-1", key("one", "same-thread"))).toBeUndefined();
  expect(takePendingResponse(pending, second.id, key("two", "same-thread"))).toBe(second);
  expect(pending.get(key("one", "same-thread"))).toBe(latest);
  expect(takePendingResponse(pending, latest.id, key("one", "same-thread"))).toBe(latest);
  expect(pending.size).toBe(0);
});
