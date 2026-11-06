const encoder = new TextEncoder();
const decoder = new TextDecoder("utf-8", { fatal: true });

export function encodeFrame(
  typ: number,
  taskId: string,
  offset: number,
  payload?: ArrayBuffer,
): ArrayBuffer {
  const id = encoder.encode(taskId);
  if (
    !id.length ||
    id.length > 255 ||
    !Number.isSafeInteger(offset) ||
    offset < 0
  )
    throw new Error("Invalid file transfer frame");
  const data = payload ? new Uint8Array(payload) : new Uint8Array(0);
  const buffer = new ArrayBuffer(10 + id.length + data.length);
  const bytes = new Uint8Array(buffer);
  bytes[0] = typ;
  bytes[1] = id.length;
  bytes.set(id, 2);
  new DataView(buffer).setBigUint64(2 + id.length, BigInt(offset));
  bytes.set(data, 10 + id.length);
  return buffer;
}

/** Reject truncated frames and offsets JS cannot represent without data loss. */
export function decodeFrame(
  buffer: ArrayBuffer,
): {
  typ: number;
  taskId: string;
  offset: number;
  payload: ArrayBuffer;
} | null {
  const bytes = new Uint8Array(buffer);
  if (bytes.length < 2 || bytes[1] === 0) return null;
  const headerLength = 10 + bytes[1];
  if (bytes.length < headerLength) return null;
  const offset = new DataView(buffer).getBigUint64(2 + bytes[1]);
  if (offset > BigInt(Number.MAX_SAFE_INTEGER)) return null;
  try {
    return {
      typ: bytes[0],
      taskId: decoder.decode(bytes.subarray(2, 2 + bytes[1])),
      offset: Number(offset),
      payload: buffer.slice(headerLength),
    };
  } catch {
    return null;
  }
}
