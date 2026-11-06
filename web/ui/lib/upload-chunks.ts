/** Upload one acknowledged chunk at a time. Aborting releases an outstanding
 * acknowledgement wait and prevents late file reads from sending more data. */
export async function uploadChunks({
  file,
  chunkSize,
  offset,
  signal,
  write,
}: {
  file: Blob;
  chunkSize: number;
  offset: () => number;
  signal: AbortSignal;
  write: (chunk: ArrayBuffer, offset: number) => Promise<void>;
}): Promise<boolean> {
  const initialOffset = offset();
  if (
    !Number.isSafeInteger(initialOffset) ||
    initialOffset < 0 ||
    initialOffset > file.size
  )
    throw new Error("Invalid upload resume offset");
  while (offset() < file.size) {
    if (signal.aborted) return false;
    const start = offset();
    const chunk = await file.slice(start, start + chunkSize).arrayBuffer();
    if (signal.aborted) return false;
    const acknowledged = await new Promise<boolean>((resolve, reject) => {
      const abort = () => {
        cleanup();
        resolve(false);
      };
      const timer = setTimeout(() => {
        cleanup();
        reject(new Error("Upload acknowledgement timed out"));
      }, 30000);
      const cleanup = () => {
        clearTimeout(timer);
        signal.removeEventListener("abort", abort);
      };
      signal.addEventListener("abort", abort, { once: true });
      Promise.resolve()
        .then(() => {
          if (signal.aborted) {
            abort();
            return;
          }
          return write(chunk, start).then(() => {
            cleanup();
            resolve(true);
          });
        })
        .catch((error) => {
          cleanup();
          reject(error);
        });
    });
    if (!acknowledged || signal.aborted) return false;
    if (offset() !== start + chunk.byteLength)
      throw new Error("Invalid upload acknowledgement offset");
  }
  return !signal.aborted;
}
