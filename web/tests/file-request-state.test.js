import { expect, it } from "vitest";
import {
  canResumeUpload,
  createListingGuard,
  readListingPage,
  restoreFileTask,
  uploadResumeRequest,
} from "../ui/lib/file-request-state";

it("accepts legacy complete listings and valid multi-page replies", () => {
  expect(readListingPage({}, 0, 3)).toEqual({ nextOffset: 3, total: 3, more: false });
  expect(readListingPage({ total: 5, truncated: true }, 0, 3))
    .toEqual({ nextOffset: 3, total: 5, more: true });
  expect(readListingPage({ offset: 3, total: 5 }, 3, 2))
    .toEqual({ nextOffset: 5, total: 5, more: false });
  expect(readListingPage({}, 0, 0)).toEqual({ nextOffset: 0, total: 0, more: false });
});

it("stops repeated first pages, empty continuations and invalid pagination", () => {
  expect(readListingPage({ truncated: true }, 100, 100)).toBeNull();
  expect(readListingPage({ offset: 100, truncated: true }, 100, 0)).toBeNull();
  expect(readListingPage({ offset: 100, total: 200 }, 100, 0)).toBeNull();
  for (const offset of [-1, 0.5, Infinity, "invalid"]) {
    expect(readListingPage({ offset }, 0, 10)).toBeNull();
  }
  expect(readListingPage({ total: -1 }, 0, 10)).toBeNull();
  const guard = createListingGuard();
  guard.begin("next-page", 100);
  expect(guard.offset()).toBe(100);
  guard.begin("new-directory");
  expect(guard.offset()).toBe(0);
  expect(guard.accepts("next-page")).toBe(false);
});

it("never resumes a same-name upload on another device or directory", () => {
  const task = {
    dir: "upload",
    status: "failed",
    device: "linux-a",
    parentPath: "/work",
    name: "build.zip",
    size: 2048,
  };
  const target = {
    device: "linux-a",
    path: "/work",
    name: "build.zip",
    size: 2048,
  };
  expect(canResumeUpload(task, target)).toBe(true);
  for (const change of [
    { device: "linux-b" },
    { path: "/other" },
    { name: "other.zip" },
    { size: 4096 },
  ]) {
    expect(canResumeUpload(task, { ...target, ...change })).toBe(false);
  }
  expect(canResumeUpload({ ...task, status: "running" }, target)).toBe(false);
});

it("rejects delayed directory replies and superseded authentication continuations", () => {
  const guard = createListingGuard();
  guard.begin("old-directory");
  guard.begin("new-directory");
  expect(guard.accepts("old-directory")).toBe(false);
  expect(guard.accepts("new-directory")).toBe(true);
  expect(guard.accepts("")).toBe(false);
  guard.invalidate();
  expect(guard.accepts("new-directory")).toBe(false);
  guard.begin("another-device");
  expect(guard.accepts("new-directory")).toBe(false);
  expect(guard.accepts("another-device")).toBe(true);
});

it("recovers only uploads whose local file was lost on reload", () => {
  for (const status of ["running", "queued"]) {
    const task = { dir: "upload", status, id: "old-upload", done: 12 };
    expect(restoreFileTask(task, "reselect")).toEqual({
      ...task,
      status: "failed",
      message: "reselect",
    });
    expect(task.status).toBe(status);
  }
  for (const dir of ["upload", "download"]) {
    for (const status of ["completed", "canceled", "paused"]) {
      const task = { dir, status };
      expect(restoreFileTask(task, "reselect")).toBe(task);
    }
  }
});

it("reselecting a file resumes the original task destination and rejects mismatches", () => {
  const task = {
    id: "original-task",
    device: "original-device",
    parentPath: "/original",
    name: "build.zip",
    size: 42,
  };
  const file = { name: "build.zip", size: 42, lastModified: 0 };
  expect(uploadResumeRequest(task, file)).toMatchObject({
    op: "upload_start",
    taskId: "original-task",
    deviceId: "original-device",
    parentPath: "/original",
  });
  expect(
    uploadResumeRequest(task, { ...file, name: "another.zip" }),
  ).toBeNull();
  expect(uploadResumeRequest(task, { ...file, size: 43 })).toBeNull();
});

it("consumes each visible page once while allowing a failed page to be retried", () => {
  const guard = createListingGuard();
  guard.begin("first-page");
  expect(guard.consume("first-page")).toBe(true);
  expect(guard.consume("first-page")).toBe(false);
  expect(guard.accepts("first-page")).toBe(false);
  guard.begin("second-page", 100);
  expect(guard.consume("first-page")).toBe(false);
  expect(guard.accepts("second-page")).toBe(true);
  expect(guard.consume("second-page")).toBe(true);
  // The error handler still needs the offset after consuming the reply.
  expect(guard.offset()).toBe(100);
  guard.begin("retry-page", 100);
  expect(guard.consume("second-page")).toBe(false);
  expect(guard.consume("retry-page")).toBe(true);
  expect(guard.offset()).toBe(100);
  expect(guard.consume("retry-page")).toBe(false);
  guard.begin("other-device");
  guard.invalidate();
  expect(guard.consume("other-device")).toBe(false);
  expect(guard.consume("")).toBe(false);
});
