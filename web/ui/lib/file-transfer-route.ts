export interface TransferDestination {
  targetLeaf: number;
  device: string;
  parentDir: string;
}

interface TransferItem {
  name: string;
  path: string;
  isDir: boolean;
  size: number;
}

export class RecursiveTransferError extends Error {
  constructor() {
    super("Cannot copy a folder into itself or one of its subfolders");
    this.name = "RecursiveTransferError";
  }
}

// Compare path segments, not string prefixes (/work and /workspace differ).
// Windows device paths are case-insensitive and accept either separator.
function comparablePath(path: string): string[] {
  const windows = /^[A-Za-z]:[\\/]/.test(path) || path.startsWith("\\\\");
  const normalized = windows ? path.replace(/\\/g, "/").toLowerCase() : path;
  const parts: string[] = [];
  for (const part of normalized.split("/")) {
    if (!part || part === ".") continue;
    if (part === "..") parts.pop();
    else parts.push(part);
  }
  return parts;
}

/** Keep the original endpoints for the entire asynchronous directory walk. */
export async function walkTransferFiles(
  items: readonly TransferItem[],
  sourceDevice: string,
  destination: TransferDestination,
  operations: {
    mkdir(path: string, device: string): Promise<void>;
    list(path: string, device: string): Promise<TransferItem[]>;
    download(item: TransferItem, device: string, destination: TransferDestination): void;
  },
): Promise<void> {
  const target = { ...destination };
  if (sourceDevice === target.device) {
    // Validate the whole selection before creating directories or scheduling files.
    for (const item of items) {
      if (!item.isDir) continue;
      const source = comparablePath(item.path);
      const output = comparablePath(`${target.parentDir}/${item.name}`);
      if (source.length <= output.length && source.every((part, index) => output[index] === part)) {
        throw new RecursiveTransferError();
      }
    }
  }
  const walk = async (item: TransferItem, parentDir: string): Promise<void> => {
    if (!item.isDir) {
      operations.download(item, sourceDevice, { ...target, parentDir });
      return;
    }
    const children = await operations.list(item.path, sourceDevice);
    const directory = `${parentDir.replace(/\/$/, "")}/${item.name}`;
    await operations.mkdir(directory, target.device);
    for (const child of children) await walk(child, directory);
  };
  for (const item of items) await walk(item, target.parentDir);
}
