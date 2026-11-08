import { afterEach, expect, it, vi } from "vitest";
import { createFileReadRequests } from "../ui/components/agent/file-read-requests";

afterEach(() => vi.useRealTimers());

it("settles each preview once and isolates paths, request attempts, and context epochs", () => {
  vi.useFakeTimers();
  const reads = createFileReadRequests();
  const expired = vi.fn();
  reads.begin("same.txt", 1, "old", expired);
  vi.advanceTimersByTime(20_000);
  reads.begin("same.txt", 2, "new", expired);
  reads.begin("other.txt", 2, "other", expired);
  expect(reads.consume("same.txt", 1, "old")).toBe(false);
  expect(reads.consume("same.txt", 1, "new")).toBe(false);
  expect(reads.consume("same.txt", 2, "old")).toBe(false);
  vi.advanceTimersByTime(10_000);
  expect(expired).not.toHaveBeenCalled();
  expect(reads.consume("same.txt", 2, "new")).toBe(true);
  expect(reads.consume("same.txt", 2, "new")).toBe(false);
  vi.advanceTimersByTime(20_000);
  expect(expired).toHaveBeenCalledTimes(1);
  expect(reads.consume("other.txt", 2, "other")).toBe(false);
});

it("clears pending reads and timers on disconnect or context reset", () => {
  vi.useFakeTimers();
  const reads = createFileReadRequests();
  const expired = vi.fn();
  reads.begin("a", 0, "a1", expired);
  reads.begin("b", 0, "b1", expired);
  expect(reads.clear()).toEqual(["a", "b"]);
  expect(reads.clear()).toEqual([]);
  reads.begin("a", 1, "a2", expired);
  expect(reads.consume("a", 1, "a1")).toBe(false);
  expect(reads.consume("a", 1, "a2")).toBe(true);
  vi.advanceTimersByTime(60_000);
  expect(expired).not.toHaveBeenCalled();
});

it("accepts a legacy id-less reply only while its context still has a pending read", () => {
  vi.useFakeTimers();
  const reads = createFileReadRequests();
  reads.begin("legacy", 1, "id", vi.fn());
  expect(reads.consume("legacy", 0, "")).toBe(false);
  expect(reads.consume("legacy", 1, "")).toBe(true);
  expect(reads.consume("legacy", 1, "")).toBe(false);
});
