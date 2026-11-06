import { expect, it, vi } from "vitest";
import { writeOwnedClipboard } from "../ui/lib/clipboard-write";

it("does not commit a previous device's clipboard result after switching", async () => {
  let current = true;
  const pending = Promise.withResolvers();
  const commit = vi.fn(), failed = vi.fn();
  const run = writeOwnedClipboard(() => current, () => pending.promise, commit, failed);
  current = false;
  pending.resolve();
  await run;
  expect(commit).not.toHaveBeenCalled();
  expect(failed).not.toHaveBeenCalled();
  const write = vi.fn();
  await writeOwnedClipboard(() => false, write, commit, failed);
  expect(write).not.toHaveBeenCalled();
});

it("keeps failed contents retryable and commits only a successful current write", async () => {
  let fingerprint = "previous";
  const failure = new Error("clipboard permission denied");
  const failed = vi.fn();
  const commit = () => { fingerprint = "new"; };
  await writeOwnedClipboard(() => true, async () => { throw failure; }, commit, failed);
  expect(fingerprint).toBe("previous");
  expect(failed).toHaveBeenCalledWith(failure);
  await writeOwnedClipboard(() => true, async () => {}, commit, failed);
  expect(fingerprint).toBe("new");
});

it("ignores a delayed clipboard rejection once its owner is hidden", async () => {
  let visible = true;
  const pending = Promise.withResolvers();
  const commit = vi.fn(), failed = vi.fn();
  const run = writeOwnedClipboard(() => visible, () => pending.promise, commit, failed);
  visible = false;
  pending.reject(new Error("late rejection"));
  await run;
  expect(commit).not.toHaveBeenCalled();
  expect(failed).not.toHaveBeenCalled();
});
