import { expect, it, vi } from "vitest";
import { walkTransferFiles } from "../ui/lib/file-transfer-route";

it("keeps both devices and the destination fixed while nested listings are pending", async () => {
  const pending = Promise.withResolvers();
  const destination = { targetLeaf: 2, device: "target-a", parentDir: "/original/" };
  const directory = { name: "folder", path: "/folder", isDir: true, size: 0 };
  const nested = { ...directory, name: "nested", path: "/folder/nested" };
  const file = { name: "hello.txt", path: "/folder/nested/hello.txt", isDir: false, size: 5 };
  const list = vi.fn().mockReturnValueOnce(pending.promise).mockResolvedValueOnce([file]);
  const download = vi.fn();
  const run = walkTransferFiles([directory], "source-a", destination, { list, download, mkdir: vi.fn().mockResolvedValue(undefined) });
  destination.device = "target-b";
  destination.parentDir = "/elsewhere";
  pending.resolve([nested]);
  await run;
  expect(list.mock.calls).toEqual([["/folder", "source-a"], ["/folder/nested", "source-a"]]);
  expect(download).toHaveBeenCalledExactlyOnceWith(file, "source-a", {
    targetLeaf: 2, device: "target-a", parentDir: "/original/folder/nested",
  });
});

it("propagates listing failures without scheduling files from later directories", async () => {
  const download = vi.fn();
  const list = vi.fn().mockRejectedValue(new Error("device disconnected"));
  await expect(walkTransferFiles([
    { name: "folder", path: "/folder", isDir: true, size: 0 },
    { name: "later", path: "/later", isDir: false, size: 1 },
  ], "source", { targetLeaf: 2, device: "target", parentDir: "/" }, { list, download, mkdir: vi.fn().mockResolvedValue(undefined) }))
    .rejects.toThrow("device disconnected");
  expect(download).not.toHaveBeenCalled();
});


it("preserves empty nested directories and waits for their creation", async () => {
  const pending = Promise.withResolvers();
  const mkdir = vi.fn().mockReturnValueOnce(pending.promise).mockResolvedValue(undefined);
  const list = vi.fn().mockResolvedValueOnce([{ name: "empty", path: "/folder/empty", isDir: true, size: 0 }]).mockResolvedValueOnce([]);
  const download = vi.fn();
  const run = walkTransferFiles([{ name: "folder", path: "/folder", isDir: true, size: 0 }], "source", {
    targetLeaf: 2, device: "target", parentDir: "/dest",
  }, { list, mkdir, download });
  await Promise.resolve();
  expect(list).toHaveBeenCalledTimes(1);
  expect(mkdir).toHaveBeenCalledExactlyOnceWith("/dest/folder", "target");
  pending.resolve();
  await run;
  expect(mkdir.mock.calls).toEqual([["/dest/folder", "target"], ["/dest/folder/empty", "target"]]);
  expect(download).not.toHaveBeenCalled();
});

it("stops before copying children when destination directory creation fails", async () => {
  const download = vi.fn();
  await expect(walkTransferFiles([{ name: "folder", path: "/folder", isDir: true, size: 0 }], "source", {
    targetLeaf: 2, device: "target", parentDir: "/dest",
  }, {
    list: vi.fn().mockResolvedValue([{ name: "file", path: "/folder/file", isDir: false, size: 1 }]),
    mkdir: vi.fn().mockRejectedValue(new Error("permission denied")), download,
  })).rejects.toThrow("permission denied");
  expect(download).not.toHaveBeenCalled();
});

it.each([
  ["/work", "/work", "work"],
  ["/work", "/work/archive", "work"],
  ["/work", "/", "work"],
  ["/work", "/other/../work/sub", "work"],
  ["C:\\Work", "c:/WORK/sub", "Work"],
  ["C:\\Work", "c:/Other/../Work", "Work"],
  ["\\\\host\\share\\Work", "\\\\HOST\\SHARE\\work\\sub", "Work"],
])("rejects recursive folder copies from %s into %s before any transfer starts", async (path, parentDir, name) => {
  const operations = { list: vi.fn(), mkdir: vi.fn(), download: vi.fn() };
  await expect(walkTransferFiles([
    { name: "first.txt", path: "/first.txt", isDir: false, size: 1 },
    { name, path, isDir: true, size: 0 },
  ], "same-device", { targetLeaf: 2, device: "same-device", parentDir }, operations))
    .rejects.toMatchObject({ name: "RecursiveTransferError" });
  expect(operations.list).not.toHaveBeenCalled();
  expect(operations.mkdir).not.toHaveBeenCalled();
  expect(operations.download).not.toHaveBeenCalled();
});

it.each([
  ["/work", "/workspace", "source"],
  ["/Work", "/work", "source"],
  ["/work", "/work", "another-device"],
  ["C:\\Work", "D:\\Work", "source"],
])("allows distinct destinations for %s into %s on %s", async (path, parentDir, device) => {
  const operations = { list: vi.fn().mockResolvedValue([]), mkdir: vi.fn().mockResolvedValue(undefined), download: vi.fn() };
  await walkTransferFiles([{ name: "work", path, isDir: true, size: 0 }], "source", { targetLeaf: 2, device, parentDir }, operations);
  expect(operations.mkdir).toHaveBeenCalledTimes(1);
});
