import { afterEach, expect, it, vi } from "vitest";
import { createFallbackDownloads, FALLBACK_BUSY_MESSAGE } from "../ui/lib/file-download-fallback";

const owners = [];
function owner() { const value = createFallbackDownloads(); owners.push(value); return value; }
function task(id, status = "running") { return { id, dir: "download", status, blobParts: [new ArrayBuffer(1)] }; }
afterEach(() => { owners.splice(0).forEach(value => value.clear()); vi.useRealTimers(); });

it("shares two retained download slots across independent panes and Agent panels", () => {
  const first = owner(), second = owner(), third = owner();
  const a = task("same-id"), b = task("same-id"), c = task("third");
  first.acquire(a); second.acquire(b);
  first.acquire(a); // Resume does not reserve another slot.
  expect(() => third.acquire(c)).toThrow(FALLBACK_BUSY_MESSAGE);
  a.status = "paused";
  first.reconcile(new Map([[a.id, a]]));
  expect(() => third.acquire(c)).toThrow(FALLBACK_BUSY_MESSAGE);
  a.status = "canceled";
  first.reconcile(new Map([[a.id, a]]));
  expect(a.blobParts).toBeUndefined();
  expect(() => third.acquire(c)).not.toThrow();
});

it.each(["failed", "canceled"])("reclaims %s task buffers and permits a retry", status => {
  const pane = owner(), other = owner();
  const a = task("a"), b = task("b"), c = task("c");
  pane.acquire(a); pane.acquire(b);
  a.status = status;
  pane.reconcile(new Map([[a.id, a], [b.id, b]]));
  expect(a.blobParts).toBeUndefined();
  expect(() => other.acquire(c)).not.toThrow();
});

it("keeps saved object URLs admitted until revoked, then frees capacity", async () => {
  vi.useFakeTimers();
  const pane = owner(), other = owner();
  const a = task("a", "completed"), b = task("b"), c = task("c");
  pane.acquire(a); pane.acquire(b);
  const revoke = vi.fn();
  pane.retire(a, revoke);
  pane.reconcile(new Map([[a.id, a], [b.id, b]]));
  expect(() => other.acquire(c)).toThrow(FALLBACK_BUSY_MESSAGE);
  await vi.advanceTimersByTimeAsync(1000);
  expect(revoke).toHaveBeenCalledOnce();
  expect(() => other.acquire(c)).not.toThrow();
});

it("unmount clears retained bytes and saved URLs immediately without double release", async () => {
  vi.useFakeTimers();
  const pane = owner(), other = owner();
  const a = task("a"), b = task("b");
  pane.acquire(a); pane.acquire(b);
  const revoke = vi.fn();
  pane.retire(a, revoke);
  pane.clear();
  expect(a.blobParts).toBeUndefined();
  expect(b.blobParts).toBeUndefined();
  expect(revoke).toHaveBeenCalledOnce();
  other.acquire(task("c")); other.acquire(task("d"));
  await vi.advanceTimersByTimeAsync(1000);
  expect(revoke).toHaveBeenCalledOnce();
  expect(() => pane.acquire(task("e"))).toThrow(FALLBACK_BUSY_MESSAGE);
});
