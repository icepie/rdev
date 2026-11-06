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
